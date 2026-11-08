package coord_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tasmirz/gossip/pkg/coord"
	_ "modernc.org/sqlite"
)

// helper to create PKI infrastructure for tests
func makePKI(t *testing.T) (caCert *x509.Certificate, caKey *ecdsa.PrivateKey, pool *x509.CertPool, issue func(int64, x509.ExtKeyUsage) (tls.Certificate, []byte, *x509.Certificate)) {
	t.Helper()
	now := time.Now()
	key := func() *ecdsa.PrivateKey {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	caKey = key()
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Adversarial Root CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err = x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	pool = x509.NewCertPool()
	pool.AddCert(caCert)

	issue = func(serial int64, usage x509.ExtKeyUsage) (tls.Certificate, []byte, *x509.Certificate) {
		k := key()
		template := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			Subject:      pkix.Name{CommonName: fmt.Sprintf("Cert-%d", serial)},
			NotBefore:    now.Add(-time.Hour),
			NotAfter:     now.Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{usage},
			IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
			DNSNames:     []string{"localhost", "example.com"},
		}
		der, err := x509.CreateCertificate(rand.Reader, template, caCert, &k.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			t.Fatal(err)
		}
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
		tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			t.Fatal(err)
		}
		return tlsCert, certPEM, parsed
	}
	return
}

// 1. Verify remote_access_control defaults to enabled=0 on fresh and upgraded SQLite databases.
func TestAdversarial_RemoteAccessDefaultsDisabled(t *testing.T) {
	ctx := context.Background()

	// A. Fresh database
	t.Run("FreshDatabaseDefault", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "fresh.db")
		s, err := coord.Open(dbPath)
		if err != nil {
			t.Fatalf("coord.Open failed: %v", err)
		}
		defer s.Close()

		enabled, err := s.RemoteAccessEnabled(ctx)
		if err != nil {
			t.Fatalf("RemoteAccessEnabled query failed: %v", err)
		}
		if enabled {
			t.Fatalf("expected remote_access_control.enabled=0 on fresh database, got enabled=true")
		}

		// Raw inspection of SQLite row
		rawDB, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer rawDB.Close()

		var id, enabledVal, updatedAt int64
		err = rawDB.QueryRow(`SELECT id, enabled, updated_at FROM remote_access_control WHERE id=1`).Scan(&id, &enabledVal, &updatedAt)
		if err != nil {
			t.Fatalf("raw query of remote_access_control failed: %v", err)
		}
		if id != 1 || enabledVal != 0 {
			t.Fatalf("expected id=1, enabled=0; got id=%d, enabled=%d", id, enabledVal)
		}
	})

	// B. Upgraded database (predating remote_access_control table)
	t.Run("UpgradedDatabaseDefault", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "legacy.db")
		rawDB, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		// Create legacy schema without remote_access_control
		_, err = rawDB.Exec(`
			CREATE TABLE projects (id TEXT PRIMARY KEY, name TEXT NOT NULL, plan_revision INTEGER NOT NULL DEFAULT 0);
			CREATE TABLE node_identity (id INTEGER PRIMARY KEY CHECK(id=1), instance_id TEXT NOT NULL UNIQUE);
			INSERT INTO node_identity (id, instance_id) VALUES (1, 'legacy-node-42');
			INSERT INTO projects (id, name, plan_revision) VALUES ('proj-legacy', 'Legacy Project', 1);
		`)
		rawDB.Close()
		if err != nil {
			t.Fatal(err)
		}

		// Open with modern coord.Open to trigger schema migration
		s, err := coord.Open(dbPath)
		if err != nil {
			t.Fatalf("coord.Open on legacy DB failed: %v", err)
		}
		defer s.Close()

		enabled, err := s.RemoteAccessEnabled(ctx)
		if err != nil {
			t.Fatalf("RemoteAccessEnabled on upgraded DB failed: %v", err)
		}
		if enabled {
			t.Fatalf("expected remote_access_control.enabled=0 on upgraded database, got enabled=true")
		}
	})
}

// 2. Test SQLite UPSERT behavior in pkg/coord/store.go:
//   - Deletion of row 1 followed by SetRemoteAccessEnabled (recovery)
//   - Concurrent calls stress test across single and multiple Store handles
//   - Schema CHECK constraint enforcement
func TestAdversarial_StoreUPSERTBehavior(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "upsert_stress.db")
	s, err := coord.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer rawDB.Close()

	// A. Deletion of row 1 followed by SetRemoteAccessEnabled(true)
	t.Run("DeleteRow1AndReinsertTrue", func(t *testing.T) {
		if _, err := rawDB.Exec(`DELETE FROM remote_access_control WHERE id=1`); err != nil {
			t.Fatal(err)
		}
		// Confirm row 1 is gone
		_, err := s.RemoteAccessEnabled(ctx)
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("expected sql.ErrNoRows when row 1 is deleted, got %v", err)
		}

		// Re-insert via UPSERT
		if err := s.SetRemoteAccessEnabled(ctx, true); err != nil {
			t.Fatalf("SetRemoteAccessEnabled failed after row 1 deleted: %v", err)
		}
		enabled, err := s.RemoteAccessEnabled(ctx)
		if err != nil || !enabled {
			t.Fatalf("expected enabled=true after reinsertion, got %v %v", enabled, err)
		}
	})

	// B. Deletion of row 1 followed by SetRemoteAccessEnabled(false)
	t.Run("DeleteRow1AndReinsertFalse", func(t *testing.T) {
		if _, err := rawDB.Exec(`DELETE FROM remote_access_control WHERE id=1`); err != nil {
			t.Fatal(err)
		}
		if err := s.SetRemoteAccessEnabled(ctx, false); err != nil {
			t.Fatalf("SetRemoteAccessEnabled(false) failed after row 1 deleted: %v", err)
		}
		enabled, err := s.RemoteAccessEnabled(ctx)
		if err != nil || enabled {
			t.Fatalf("expected enabled=false after reinsertion, got %v %v", enabled, err)
		}
	})

	// C. Schema CHECK constraints
	t.Run("SchemaCheckConstraints", func(t *testing.T) {
		// id != 1 must fail
		_, err := rawDB.Exec(`INSERT INTO remote_access_control (id, enabled, updated_at) VALUES (2, 1, 0)`)
		if err == nil {
			t.Fatal("expected CHECK(id=1) constraint violation when inserting id=2, but succeeded")
		}
		// enabled > 1 must fail
		_, err = rawDB.Exec(`UPDATE remote_access_control SET enabled=5 WHERE id=1`)
		if err == nil {
			t.Fatal("expected CHECK(enabled IN (0,1)) violation for enabled=5, but succeeded")
		}
		// enabled < 0 must fail
		_, err = rawDB.Exec(`UPDATE remote_access_control SET enabled=-1 WHERE id=1`)
		if err == nil {
			t.Fatal("expected CHECK(enabled IN (0,1)) violation for enabled=-1, but succeeded")
		}
	})

	// D. Concurrent stress on single Store handle (50 writers + 50 readers)
	t.Run("ConcurrentSingleStoreStress", func(t *testing.T) {
		var wg sync.WaitGroup
		errs := make(chan error, 100)

		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				val := idx%2 == 0
				if err := s.SetRemoteAccessEnabled(ctx, val); err != nil {
					errs <- fmt.Errorf("writer %d: %w", idx, err)
				}
			}(i)

			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				_, err := s.RemoteAccessEnabled(ctx)
				if err != nil {
					errs <- fmt.Errorf("reader %d: %w", idx, err)
				}
			}(i)
		}
		wg.Wait()
		close(errs)

		for e := range errs {
			t.Fatalf("concurrent single store stress failed: %v", e)
		}
	})

	// E. Multi-Store concurrent inter-process stress (10 distinct Store handles on same DB)
	t.Run("MultiStoreInterProcessStress", func(t *testing.T) {
		var wg sync.WaitGroup
		errs := make(chan error, 20)

		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				storeHandle, err := coord.Open(dbPath)
				if err != nil {
					errs <- fmt.Errorf("open %d: %w", idx, err)
					return
				}
				defer storeHandle.Close()

				for step := 0; step < 10; step++ {
					val := (idx+step)%2 == 0
					if err := storeHandle.SetRemoteAccessEnabled(ctx, val); err != nil {
						errs <- fmt.Errorf("multi-store write [%d,%d]: %w", idx, step, err)
						return
					}
					if _, err := storeHandle.RemoteAccessEnabled(ctx); err != nil {
						errs <- fmt.Errorf("multi-store read [%d,%d]: %w", idx, step, err)
						return
					}
				}
			}(i)
		}
		wg.Wait()
		close(errs)

		for e := range errs {
			t.Fatalf("multi-store concurrency failed: %v", e)
		}
	})
}

//  3. Verify network RPC (coord-serve), bootstrap RPC, and HTTPS dashboard requests
//     are immediately rejected with HTTP 503 when the kill switch is disabled.
//     Also verify that loopback HTTP dashboard is NOT rejected.
func TestAdversarial_NetworkIngressRejectionHTTP503(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "ingress_test.db")
	s, err := coord.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Initial project setup
	p, owner, err := s.CreateProject(ctx, "sec-test", "owner")
	if err != nil {
		t.Fatal(err)
	}

	_, _, pool, issue := makePKI(t)
	serverCert, _, _ := issue(10, x509.ExtKeyUsageServerAuth)
	clientCert, _, clientParsed := issue(11, x509.ExtKeyUsageClientAuth)

	// Enroll client certificate
	clientPin := sha256.Sum256(clientParsed.Raw)
	fingerprint := hex.EncodeToString(clientPin[:])
	if _, err := s.AddCertificateGrant(ctx, p.ID, owner.Token, fingerprint); err != nil {
		t.Fatal(err)
	}

	tlsClientConfig := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		RootCAs:      pool,
		Certificates: []tls.Certificate{clientCert},
	}
	tlsClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: tlsClientConfig},
		Timeout:   5 * time.Second,
	}

	// Setup servers:
	// A. Network RPC handler (coord-serve)
	rpcHandler := coord.RPCHandler(s)
	rpcServer := httptest.NewUnstartedServer(rpcHandler)
	rpcServer.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}
	rpcServer.StartTLS()
	defer rpcServer.Close()

	// B. Bootstrap RPC handler
	bootstrapKey := strings.Repeat("test-bootstrap-secret-key-32ch", 2)
	bootstrapHandler, err := coord.RPCHandlerWithBootstrapKey(s, bootstrapKey)
	if err != nil {
		t.Fatal(err)
	}
	bootstrapServer := httptest.NewUnstartedServer(bootstrapHandler)
	bootstrapServer.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}
	bootstrapServer.StartTLS()
	defer bootstrapServer.Close()

	// C. Web UI Dashboard handler
	webServer := httptest.NewUnstartedServer(nil)
	webHost, webPort, err := net.SplitHostPort(webServer.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	webHandler := coord.NewWebHandlerWithCertificateGrants(s, []string{net.JoinHostPort(webHost, webPort)})
	webServer.Config.Handler = webHandler
	webServer.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}
	webServer.StartTLS()
	defer webServer.Close()

	// Helper to send RPC POST
	sendRPC := func(serverURL string, bodyMap map[string]any, bearerToken string) (int, string) {
		reqBytes, _ := json.Marshal(bodyMap)
		req, err := http.NewRequest(http.MethodPost, serverURL+"/v1/call", bytes.NewReader(reqBytes))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if bearerToken != "" {
			req.Header.Set("Authorization", "Bearer "+bearerToken)
		}
		resp, err := tlsClient.Do(req)
		if err != nil {
			t.Fatalf("TLS request failed: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	// 1. Initial State: Kill switch disabled (default)
	// All network endpoints must return HTTP 503
	t.Run("DisabledByDefaultRejectsWith503", func(t *testing.T) {
		// Network RPC
		code, body := sendRPC(rpcServer.URL, map[string]any{
			"project":   p.ID,
			"operation": "project.sync",
			"limit":     50,
		}, owner.Token)
		if code != http.StatusServiceUnavailable {
			t.Fatalf("expected network RPC 503, got %d (body: %s)", code, body)
		}
		if !strings.Contains(body, "remote coordination is disabled by the local kill switch") {
			t.Fatalf("unexpected body message: %s", body)
		}

		// Bootstrap RPC
		code, body = sendRPC(bootstrapServer.URL, map[string]any{
			"operation": "project.create_workspace_bootstrap_prepared",
		}, bootstrapKey)
		if code != http.StatusServiceUnavailable {
			t.Fatalf("expected bootstrap RPC 503, got %d (body: %s)", code, body)
		}
		if !strings.Contains(body, "remote coordination is disabled by the local kill switch") {
			t.Fatalf("unexpected body message: %s", body)
		}

		// HTTPS Dashboard
		dashReq, _ := http.NewRequest(http.MethodGet, webServer.URL+"/", nil)
		dashResp, err := tlsClient.Do(dashReq)
		if err != nil {
			t.Fatal(err)
		}
		dashBody, _ := io.ReadAll(dashResp.Body)
		dashResp.Body.Close()
		if dashResp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("expected HTTPS dashboard 503, got %d (body: %s)", dashResp.StatusCode, string(dashBody))
		}
		if !strings.Contains(string(dashBody), "remote dashboard is disabled by the local kill switch") {
			t.Fatalf("unexpected body message: %s", string(dashBody))
		}
	})

	// 2. Loopback HTTP Dashboard is NOT rejected by kill switch
	t.Run("LoopbackHTTPDashboardNotBlocked", func(t *testing.T) {
		loopbackServer := httptest.NewServer(webHandler)
		defer loopbackServer.Close()

		resp, err := http.Get(loopbackServer.URL + "/")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusServiceUnavailable {
			t.Fatal("loopback HTTP dashboard was blocked by kill switch")
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK for loopback HTTP dashboard, got %d", resp.StatusCode)
		}
	})

	// 3. Dynamic Flip: Enable -> all succeed; Disable -> all immediately reject with 503
	t.Run("DynamicStateFlip", func(t *testing.T) {
		// Enable kill switch
		if err := s.SetRemoteAccessEnabled(ctx, true); err != nil {
			t.Fatal(err)
		}

		// Network RPC succeeds
		code, body := sendRPC(rpcServer.URL, map[string]any{
			"project":   p.ID,
			"operation": "project.sync",
			"limit":     50,
		}, owner.Token)
		if code != http.StatusOK {
			t.Fatalf("expected network RPC 200 after enabling, got %d (body: %s)", code, body)
		}

		// HTTPS Dashboard succeeds
		dashReq, _ := http.NewRequest(http.MethodGet, webServer.URL+"/", nil)
		dashResp, err := tlsClient.Do(dashReq)
		if err != nil {
			t.Fatal(err)
		}
		dashResp.Body.Close()
		if dashResp.StatusCode != http.StatusOK {
			t.Fatalf("expected HTTPS dashboard 200 after enabling, got %d", dashResp.StatusCode)
		}

		// Disable kill switch again
		if err := s.SetRemoteAccessEnabled(ctx, false); err != nil {
			t.Fatal(err)
		}

		// Network RPC immediately returns 503
		code, body = sendRPC(rpcServer.URL, map[string]any{
			"project":   p.ID,
			"operation": "project.sync",
			"limit":     50,
		}, owner.Token)
		if code != http.StatusServiceUnavailable {
			t.Fatalf("expected immediate network RPC 503 after disabling, got %d (body: %s)", code, body)
		}

		// Bootstrap RPC immediately returns 503
		code, body = sendRPC(bootstrapServer.URL, map[string]any{
			"operation": "project.create_workspace_bootstrap_prepared",
		}, bootstrapKey)
		if code != http.StatusServiceUnavailable {
			t.Fatalf("expected immediate bootstrap RPC 503 after disabling, got %d (body: %s)", code, body)
		}

		// HTTPS Dashboard immediately returns 503
		dashReq, _ = http.NewRequest(http.MethodGet, webServer.URL+"/", nil)
		dashResp, err = tlsClient.Do(dashReq)
		if err != nil {
			t.Fatal(err)
		}
		dashResp.Body.Close()
		if dashResp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("expected immediate HTTPS dashboard 503 after disabling, got %d", dashResp.StatusCode)
		}
	})
}

//  4. Verify that TLS RPC calls attempting remote.access.status or remote.access.set
//     are rejected with HTTP 401 Unauthorized, and do not mutate state.
func TestAdversarial_TLSRPCKillSwitchImmunity_HTTP401(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "tls_immunity.db")
	s, err := coord.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Enable remote access so requests reach the RPC operation dispatcher
	if err := s.SetRemoteAccessEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}

	p, owner, err := s.CreateProject(ctx, "sec-immunity", "owner")
	if err != nil {
		t.Fatal(err)
	}

	_, _, pool, issue := makePKI(t)
	serverCert, _, _ := issue(20, x509.ExtKeyUsageServerAuth)
	clientCert, _, clientParsed := issue(21, x509.ExtKeyUsageClientAuth)

	pin := sha256.Sum256(clientParsed.Raw)
	if _, err := s.AddCertificateGrant(ctx, p.ID, owner.Token, hex.EncodeToString(pin[:])); err != nil {
		t.Fatal(err)
	}

	tlsClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion:   tls.VersionTLS13,
				RootCAs:      pool,
				Certificates: []tls.Certificate{clientCert},
			},
		},
		Timeout: 5 * time.Second,
	}

	rpcServer := httptest.NewUnstartedServer(coord.RPCHandler(s))
	rpcServer.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}
	rpcServer.StartTLS()
	defer rpcServer.Close()

	sendCall := func(op string, extra map[string]any) (int, string) {
		payload := map[string]any{
			"project":   p.ID,
			"operation": op,
		}
		for k, v := range extra {
			payload[k] = v
		}
		b, _ := json.Marshal(payload)
		req, err := http.NewRequest(http.MethodPost, rpcServer.URL+"/v1/call", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+owner.Token)
		resp, err := tlsClient.Do(req)
		if err != nil {
			t.Fatalf("TLS call error: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	// A. Attempt remote.access.status over TLS
	code, body := sendCall("remote.access.status", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("expected HTTP 401 Unauthorized for TLS remote.access.status, got %d (body: %s)", code, body)
	}

	// B. Attempt remote.access.set over TLS
	code, body = sendCall("remote.access.set", map[string]any{"enabled": false})
	if code != http.StatusUnauthorized {
		t.Fatalf("expected HTTP 401 Unauthorized for TLS remote.access.set, got %d (body: %s)", code, body)
	}

	// C. Verify kill switch state was NOT mutated
	enabled, err := s.RemoteAccessEnabled(ctx)
	if err != nil || !enabled {
		t.Fatalf("kill switch was mutated or errored after TLS attack: %v %v", enabled, err)
	}
}

// 5. Verify local Unix domain socket RPC for kill switch works as intended
func TestAdversarial_LocalUnixSocketRPC(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "unix_rpc.db")
	s, err := coord.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	sockPath := filepath.Join(dir, "coord.sock")
	sockCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- coord.ServeLocal(sockCtx, s, sockPath) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()

	// Wait for socket to become ready
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := filepath.Glob(sockPath); err == nil && len(info) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	remote, err := coord.NewLocalRemote(sockPath)
	if err != nil {
		t.Fatalf("NewLocalRemote: %v", err)
	}
	defer remote.CloseIdleConnections()

	// Initial status is false
	status, err := remote.LocalRemoteAccessStatus(ctx)
	if err != nil || status {
		t.Fatalf("expected initial status=false over socket: %v %v", status, err)
	}

	// Enable over socket
	if err := remote.LocalSetRemoteAccessEnabled(ctx, true); err != nil {
		t.Fatalf("LocalSetRemoteAccessEnabled(true) failed: %v", err)
	}
	status, err = remote.LocalRemoteAccessStatus(ctx)
	if err != nil || !status {
		t.Fatalf("expected status=true after socket enable: %v %v", status, err)
	}

	// Disable over socket
	if err := remote.LocalSetRemoteAccessEnabled(ctx, false); err != nil {
		t.Fatalf("LocalSetRemoteAccessEnabled(false) failed: %v", err)
	}
	status, err = remote.LocalRemoteAccessStatus(ctx)
	if err != nil || status {
		t.Fatalf("expected status=false after socket disable: %v %v", status, err)
	}
}
