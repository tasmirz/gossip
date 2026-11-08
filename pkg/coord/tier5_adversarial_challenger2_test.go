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
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tasmirz/gossip/benchmarks"
	"github.com/tasmirz/gossip/pkg/coord"
	_ "modernc.org/sqlite"
)

// tier5CertBundle holds ephemeral root CA and issued credentials for mTLS tests.
type tier5CertBundle struct {
	caCert     *x509.Certificate
	caDER      []byte
	serverCert tls.Certificate
	clientCert tls.Certificate
	clientPEM  []byte
	clientKey  []byte
	clientFP   string
	otherCert  tls.Certificate
	otherPEM   []byte
	otherKey   []byte
	otherFP    string
	roots      *x509.CertPool
}

func setupTier5Certs(t *testing.T) tier5CertBundle {
	t.Helper()
	now := time.Now()
	genKey := func() *ecdsa.PrivateKey {
		t.Helper()
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}

	caKey := genKey()
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(100),
		Subject:               pkix.Name{CommonName: "Tier5 Test Root CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("Create CA cert: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("Parse CA cert: %v", err)
	}

	issueCert := func(serial int64, usage x509.ExtKeyUsage) (tls.Certificate, []byte, []byte, string) {
		t.Helper()
		k := genKey()
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			NotBefore:    now.Add(-time.Hour),
			NotAfter:     now.Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{usage},
			IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &k.PublicKey, caKey)
		if err != nil {
			t.Fatalf("Create cert serial %d: %v", serial, err)
		}
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		keyDER, err := x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			t.Fatalf("Marshal private key: %v", err)
		}
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

		tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			t.Fatalf("X509KeyPair: %v", err)
		}
		sum := sha256.Sum256(der)
		fp := hex.EncodeToString(sum[:])
		return tlsCert, certPEM, keyPEM, fp
	}

	serverCert, _, _, _ := issueCert(101, x509.ExtKeyUsageServerAuth)
	clientCert, clientPEM, clientKey, clientFP := issueCert(102, x509.ExtKeyUsageClientAuth)
	otherCert, otherPEM, otherKey, otherFP := issueCert(103, x509.ExtKeyUsageClientAuth)

	roots := x509.NewCertPool()
	roots.AddCert(caCert)

	return tier5CertBundle{
		caCert:     caCert,
		caDER:      caDER,
		serverCert: serverCert,
		clientCert: clientCert,
		clientPEM:  clientPEM,
		clientKey:  clientKey,
		clientFP:   clientFP,
		otherCert:  otherCert,
		otherPEM:   otherPEM,
		otherKey:   otherKey,
		otherFP:    otherFP,
		roots:      roots,
	}
}

// -----------------------------------------------------------------------------
// 1. Dynamic Kill Switch Cutoff Under High-Frequency RPC Traffic & Zero Token Leak
// -----------------------------------------------------------------------------

func TestTier5_Adversarial_DynamicKillSwitchCutoffUnderHighConcurrency(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()

	certs := setupTier5Certs(t)

	// Enroll client certificate fingerprint into project
	if _, err := s.AddCertificateGrant(ctx, p.ID, owner.Token, certs.clientFP); err != nil {
		t.Fatalf("AddCertificateGrant: %v", err)
	}

	// Remote access enabled initially
	if err := s.SetRemoteAccessEnabled(ctx, true); err != nil {
		t.Fatalf("SetRemoteAccessEnabled(true): %v", err)
	}

	handler := coord.RPCHandler(s)
	ts := httptest.NewUnstartedServer(handler)
	ts.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certs.serverCert},
		ClientCAs:    certs.roots,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}
	ts.StartTLS()
	defer ts.Close()

	// High concurrency setup
	const numGoroutines = 30
	var stopFlag int32
	var killSwitchFlipped int32

	var preCutoff200 int64
	var preCutoff401 int64
	var postCutoff503 int64
	var postCutoffLeaked401 int64
	var postCutoffLeaked200 int64

	clientTransport := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion:   tls.VersionTLS13,
			RootCAs:      certs.roots,
			Certificates: []tls.Certificate{certs.clientCert},
		},
		MaxIdleConnsPerHost: 50,
	}
	httpClient := &http.Client{Transport: clientTransport, Timeout: 5 * time.Second}

	var wg sync.WaitGroup

	// Launch worker goroutines
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			// Decide token category for this worker:
			// 0: valid token
			// 1: invalid token (garbage)
			// 2: empty token
			category := workerID % 3

			for atomic.LoadInt32(&stopFlag) == 0 {
				token := ""
				switch category {
				case 0:
					token = worker.Token
				case 1:
					token = "bogus-unauthorized-token-12345"
				case 2:
					token = ""
				}

				reqBody, _ := json.Marshal(map[string]any{
					"operation": "identity.read",
					"project":   p.ID,
				})

				req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/v1/call", bytes.NewReader(reqBody))
				if err != nil {
					return
				}
				req.Header.Set("Content-Type", "application/json")
				if token != "" {
					req.Header.Set("Authorization", "Bearer "+token)
				}

				isFlippedPre := atomic.LoadInt32(&killSwitchFlipped) == 1
				resp, err := httpClient.Do(req)
				if err != nil {
					// Client might fail if transport closes, ignore
					continue
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()

				if isFlippedPre {
					if resp.StatusCode == http.StatusServiceUnavailable {
						atomic.AddInt64(&postCutoff503, 1)
						if !strings.Contains(string(body), "remote coordination is disabled by the local kill switch") {
							t.Errorf("expected kill switch rejection message, got: %s", string(body))
						}
					} else if resp.StatusCode == http.StatusUnauthorized {
						atomic.AddInt64(&postCutoffLeaked401, 1)
					} else if resp.StatusCode == http.StatusOK {
						atomic.AddInt64(&postCutoffLeaked200, 1)
					}
				} else {
					if resp.StatusCode == http.StatusOK {
						atomic.AddInt64(&preCutoff200, 1)
					} else if resp.StatusCode == http.StatusUnauthorized {
						atomic.AddInt64(&preCutoff401, 1)
					}
				}

				time.Sleep(2 * time.Millisecond)
			}
		}(i)
	}

	// Let traffic run for 100ms while enabled to establish active high-frequency connections
	time.Sleep(100 * time.Millisecond)

	// DYNAMIC CUTOFF: Toggle kill switch off in SQLite
	if err := s.SetRemoteAccessEnabled(ctx, false); err != nil {
		t.Fatalf("SetRemoteAccessEnabled(false): %v", err)
	}
	atomic.StoreInt32(&killSwitchFlipped, 1)

	// Let traffic hit the disabled kill switch for 150ms
	time.Sleep(150 * time.Millisecond)

	// Stop workers
	atomic.StoreInt32(&stopFlag, 1)
	wg.Wait()

	t.Logf("Traffic Stats: PreCutoff[200=%d, 401=%d] PostCutoff[503=%d, Leaked401=%d, Leaked200=%d]",
		preCutoff200, preCutoff401, postCutoff503, postCutoffLeaked401, postCutoffLeaked200)

	if postCutoff503 == 0 {
		t.Fatal("expected post-cutoff requests to receive HTTP 503, got 0")
	}

	// Verify ZERO TOKEN LEAK: After cutoff, invalid tokens must NOT receive 401 and valid tokens must NOT receive 200.
	// Both must be rejected with 503 before any authentication or token parsing!
	if postCutoffLeaked401 > 0 {
		t.Fatalf("CRITICAL SECURITY FLAW: %d requests leaked HTTP 401 after kill switch cutoff (token checking bypassed kill switch)", postCutoffLeaked401)
	}
	if postCutoffLeaked200 > 0 {
		t.Fatalf("CRITICAL SECURITY FLAW: %d requests succeeded with HTTP 200 after kill switch cutoff", postCutoffLeaked200)
	}

	// Verify immediate restoration when toggled back on
	if err := s.SetRemoteAccessEnabled(ctx, true); err != nil {
		t.Fatalf("SetRemoteAccessEnabled(true): %v", err)
	}

	reqBody, _ := json.Marshal(map[string]any{
		"operation": "identity.read",
		"project":   p.ID,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/v1/call", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+worker.Token)

	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("restoration request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected HTTP 200 after restoration, got %d", resp.StatusCode)
	}
}

// -----------------------------------------------------------------------------
// 2. Local Loopback WebHandler and CLI Immunity During Kill Switch Cutoff
// -----------------------------------------------------------------------------

func TestTier5_Adversarial_LocalLoopbackAndCLIImmunityDuringCutoff(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()

	certs := setupTier5Certs(t)

	// Ensure remote kill switch is DISABLED
	if err := s.SetRemoteAccessEnabled(ctx, false); err != nil {
		t.Fatalf("SetRemoteAccessEnabled(false): %v", err)
	}

	// Verify remote access reports disabled in Store
	enabled, err := s.RemoteAccessEnabled(ctx)
	if err != nil || enabled {
		t.Fatalf("expected RemoteAccessEnabled=false, got %v, err=%v", enabled, err)
	}

	// 1. Remote HTTPS dashboard test (WebHandler over TLS) -> MUST be blocked with 503
	tsRemoteWeb := httptest.NewUnstartedServer(nil)
	tsRemoteWeb.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certs.serverCert},
		ClientCAs:    certs.roots,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}
	tsRemoteWeb.StartTLS()
	defer tsRemoteWeb.Close()

	host, port, _ := net.SplitHostPort(tsRemoteWeb.Listener.Addr().String())
	hostPort := net.JoinHostPort(host, port)
	tsRemoteWeb.Config.Handler = coord.NewWebHandlerWithCertificateGrants(s, []string{hostPort})

	tlsClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion:   tls.VersionTLS13,
				RootCAs:      certs.roots,
				Certificates: []tls.Certificate{certs.clientCert},
			},
		},
	}
	respRemote, err := tlsClient.Get(tsRemoteWeb.URL + "/")
	if err != nil {
		t.Fatalf("remote web request failed: %v", err)
	}
	bodyRemote, _ := io.ReadAll(respRemote.Body)
	respRemote.Body.Close()

	if respRemote.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected remote HTTPS dashboard to be rejected with HTTP 503, got %d: %s", respRemote.StatusCode, string(bodyRemote))
	}
	if !strings.Contains(string(bodyRemote), "remote dashboard is disabled by the local kill switch") {
		t.Fatalf("expected kill switch message for remote dashboard, got: %s", string(bodyRemote))
	}

	// 2. Local loopback HTTP WebHandler test (r.TLS == nil, RemoteAddr is loopback) -> MUST SUCCEED
	localWebHandler := coord.NewWebHandler(s)

	doLoopback := func(method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
		var reqData []byte
		if body != nil {
			reqData, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, "http://127.0.0.1:8080"+path, bytes.NewReader(reqData))
		req.RemoteAddr = "127.0.0.1:45678"
		req.Host = "127.0.0.1:8080"
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "http://127.0.0.1:8080")
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		localWebHandler.ServeHTTP(rec, req)
		return rec
	}

	// Loopback UI assets
	recUI := doLoopback(http.MethodGet, "/", nil, nil)
	if recUI.Code != http.StatusOK {
		t.Fatalf("expected loopback GET / to succeed with HTTP 200 during cutoff, got %d", recUI.Code)
	}

	recAppJS := doLoopback(http.MethodGet, "/app.js", nil, nil)
	if recAppJS.Code != http.StatusOK {
		t.Fatalf("expected loopback GET /app.js to succeed with HTTP 200, got %d", recAppJS.Code)
	}

	// Loopback query of remote access status endpoint
	recRemoteAccess := doLoopback(http.MethodGet, "/api/remote-access", nil, nil)
	if recRemoteAccess.Code != http.StatusOK {
		t.Fatalf("expected loopback GET /api/remote-access to succeed, got %d: %s", recRemoteAccess.Code, recRemoteAccess.Body.String())
	}
	var remoteAccessResp struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(recRemoteAccess.Body.Bytes(), &remoteAccessResp); err != nil {
		t.Fatalf("unmarshal remote-access response: %v", err)
	}
	if remoteAccessResp.Enabled {
		t.Fatal("expected remote-access status to report enabled=false")
	}

	// Loopback login
	recLogin := doLoopback(http.MethodPost, "/api/login", map[string]string{
		"project_id": p.ID,
		"token":      owner.Token,
	}, nil)
	if recLogin.Code != http.StatusOK {
		t.Fatalf("expected loopback login to succeed, got %d: %s", recLogin.Code, recLogin.Body.String())
	}

	var sessionCookie *http.Cookie
	for _, c := range recLogin.Result().Cookies() {
		if c.Name == "gossip_session" {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("expected gossip_session cookie from loopback login")
	}

	// Loopback project list
	recProjects := doLoopback(http.MethodGet, "/api/projects", nil, sessionCookie)
	if recProjects.Code != http.StatusOK {
		t.Fatalf("expected loopback GET /api/projects to succeed, got %d", recProjects.Code)
	}

	// Loopback local project creation from git checkout
	gitDir := t.TempDir()
	initCmd := exec.Command("git", "init", gitDir)
	if err := initCmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	recCreate := doLoopback(http.MethodPost, "/api/projects/create", map[string]string{
		"name":       "Loopback Created Project",
		"local_path": gitDir,
	}, sessionCookie)
	if recCreate.Code != http.StatusOK {
		t.Fatalf("expected loopback project creation to succeed during cutoff, got %d: %s", recCreate.Code, recCreate.Body.String())
	}

	// 3. Local CLI / Store direct operations -> MUST SUCCEED
	plan, err := s.CurrentPlan(ctx, p.ID, owner.Token)
	if err != nil {
		t.Fatalf("Store.CurrentPlan failed during cutoff: %v", err)
	}
	_ = plan

	task, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Local Immunity Task", "Description", "research", coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil {
		t.Fatalf("Store.CreateTask failed during cutoff: %v", err)
	}

	claimed, err := s.ClaimTask(ctx, p.ID, worker.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatalf("Store.ClaimTask failed during cutoff: %v", err)
	}
	if claimed.Status != "running" {
		t.Fatalf("claimed status = %s, want running", claimed.Status)
	}
}

// -----------------------------------------------------------------------------
// 3. Decoupling of Certificate Grant Revocation vs Principal Token Validity
// -----------------------------------------------------------------------------

func TestTier5_Adversarial_CertificateRevocationDecoupling(t *testing.T) {
	s, dbPath, p, owner, worker := setup(t)
	ctx := context.Background()

	certs := setupTier5Certs(t)

	// Enable remote access and grant worker client cert
	if err := s.SetRemoteAccessEnabled(ctx, true); err != nil {
		t.Fatalf("SetRemoteAccessEnabled: %v", err)
	}
	if _, err := s.AddCertificateGrant(ctx, p.ID, owner.Token, certs.clientFP); err != nil {
		t.Fatalf("AddCertificateGrant: %v", err)
	}

	handler := coord.RPCHandler(s)
	ts := httptest.NewUnstartedServer(handler)
	ts.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certs.serverCert},
		ClientCAs:    certs.roots,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}
	ts.StartTLS()
	defer ts.Close()

	tlsClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion:   tls.VersionTLS13,
				RootCAs:      certs.roots,
				Certificates: []tls.Certificate{certs.clientCert},
			},
		},
	}

	callRPC := func(token string) (int, string) {
		reqBody, _ := json.Marshal(map[string]any{
			"operation": "identity.read",
			"project":   p.ID,
		})
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/v1/call", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := tlsClient.Do(req)
		if err != nil {
			t.Fatalf("RPC call failed: %v", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, strings.TrimSpace(string(b))
	}

	// 1. Initial State: Worker has valid certificate grant and valid token -> HTTP 200
	code, body := callRPC(worker.Token)
	if code != http.StatusOK {
		t.Fatalf("initial RPC call expected HTTP 200, got %d: %s", code, body)
	}

	// 2. Owner revokes the certificate grant
	if err := s.RevokeCertificateGrant(ctx, p.ID, owner.Token, certs.clientFP); err != nil {
		t.Fatalf("RevokeCertificateGrant: %v", err)
	}

	// 3. Worker calls over mTLS using revoked certificate -> MUST produce HTTP 403 Forbidden
	code, body = callRPC(worker.Token)
	if code != http.StatusForbidden {
		t.Fatalf("expected HTTP 403 Forbidden for revoked certificate grant, got %d: %s", code, body)
	}
	if !strings.Contains(body, "client certificate is not authorized for this project") {
		t.Fatalf("expected cert unauthorized message, got: %s", body)
	}

	// 4. White-box SQLite State Inspection:
	// Verify certificate grant was removed from certificate_grants
	var grantCount int
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer rawDB.Close()

	if err := rawDB.QueryRow("SELECT COUNT(*) FROM certificate_grants WHERE project_id=? AND fingerprint=?", p.ID, certs.clientFP).Scan(&grantCount); err != nil {
		t.Fatalf("query certificate_grants: %v", err)
	}
	if grantCount != 0 {
		t.Fatalf("expected 0 certificate grants, got %d", grantCount)
	}

	// CRITICAL DECOUPLING INVARIANT: Worker's principal identity in SQLite MUST REMAIN 100% INTACT!
	var principalRevoked int
	var principalRole string
	if err := rawDB.QueryRow("SELECT revoked, role FROM principals WHERE id=? AND project_id=?", worker.PrincipalID, p.ID).Scan(&principalRevoked, &principalRole); err != nil {
		t.Fatalf("query principals: %v", err)
	}
	if principalRevoked != 0 {
		t.Fatalf("CRITICAL BUG: Revoking certificate grant also marked principal as revoked! revoked=%d", principalRevoked)
	}
	if principalRole != "worker" {
		t.Fatalf("principal role altered: got %s, want worker", principalRole)
	}

	// Direct verification via CredentialValid:
	valid, err := s.CredentialValid(ctx, p.ID, worker.Token)
	if err != nil || !valid {
		t.Fatalf("expected CredentialValid=true after cert revocation, got %v, err=%v", valid, err)
	}

	// 5. Re-granting the certificate immediately restores access over mTLS
	if _, err := s.AddCertificateGrant(ctx, p.ID, owner.Token, certs.clientFP); err != nil {
		t.Fatalf("AddCertificateGrant re-grant: %v", err)
	}
	code, body = callRPC(worker.Token)
	if code != http.StatusOK {
		t.Fatalf("expected HTTP 200 after cert re-grant, got %d: %s", code, body)
	}
}

// -----------------------------------------------------------------------------
// 4. Principal Revocation: Immediate Fence, In-Flight Task Unlock & 401 Unauthorized
// -----------------------------------------------------------------------------

func TestTier5_Adversarial_PrincipalRevocationImmediateFenceAndTaskUnlock(t *testing.T) {
	s, dbPath, p, owner, worker := setup(t)
	ctx := context.Background()

	// Add second worker to verify task claiming after first worker is revoked
	worker2, err := s.AddPrincipal(ctx, p.ID, owner.Token, "Worker-Two", "worker")
	if err != nil {
		t.Fatalf("AddPrincipal worker2: %v", err)
	}

	// Create two tasks:
	// Task 1 will be claimed and running by Worker 1
	// Task 2 will be assigned to Worker 1
	task1, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "In-Flight Task 1", "Desc 1", "implementation", coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil {
		t.Fatalf("CreateTask 1: %v", err)
	}
	task2, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Assigned Task 2", "Desc 2", "research", coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil {
		t.Fatalf("CreateTask 2: %v", err)
	}

	// Worker 1 claims Task 1 with 10 minute lease
	claimed1, err := s.ClaimTask(ctx, p.ID, worker.Token, task1.ID, 10*time.Minute)
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if claimed1.Status != "running" || claimed1.Owner != worker.PrincipalID || claimed1.LeaseUntil <= time.Now().UnixMilli() {
		t.Fatalf("unexpected claimed task state: %+v", claimed1)
	}

	// Assign Task 2 to Worker 1
	assigned2, err := s.AssignTask(ctx, p.ID, owner.Token, task2.ID, worker.PrincipalID)
	if err != nil {
		t.Fatalf("AssignTask: %v", err)
	}
	if assigned2.AssignedTo != worker.PrincipalID {
		t.Fatalf("assigned task assignee mismatch: got %s, want %s", assigned2.AssignedTo, worker.PrincipalID)
	}

	// Grant Worker 1 a specific permission
	if _, err := s.GrantPermission(ctx, p.ID, owner.Token, worker.PrincipalID, "task.delegate"); err != nil {
		t.Fatalf("GrantPermission: %v", err)
	}

	// REVOKE WORKER 1
	if err := s.RevokePrincipal(ctx, p.ID, owner.Token, worker.PrincipalID); err != nil {
		t.Fatalf("RevokePrincipal: %v", err)
	}

	// 1. Verify in-flight Task 1 was immediately reset to 'ready', lease zeroed, owner NULLed
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer rawDB.Close()

	var t1Status string
	var t1Owner sql.NullString
	var t1Lease int64
	if err := rawDB.QueryRow("SELECT status, owner, lease_until FROM tasks WHERE id=?", task1.ID).Scan(&t1Status, &t1Owner, &t1Lease); err != nil {
		t.Fatalf("query task 1: %v", err)
	}
	if t1Status != "ready" {
		t.Fatalf("Task 1 status after revocation = %s, want ready", t1Status)
	}
	if t1Owner.Valid {
		t.Fatalf("Task 1 owner after revocation = %s, want NULL", t1Owner.String)
	}
	if t1Lease != 0 {
		t.Fatalf("Task 1 lease_until after revocation = %d, want 0", t1Lease)
	}

	// 2. Verify Task 2 assignment was cleaned up
	var assignCount int
	if err := rawDB.QueryRow("SELECT COUNT(*) FROM task_assignments WHERE task_id=? AND principal_id=?", task2.ID, worker.PrincipalID).Scan(&assignCount); err != nil {
		t.Fatalf("query task_assignments: %v", err)
	}
	if assignCount != 0 {
		t.Fatalf("task_assignments count = %d, want 0", assignCount)
	}

	// 3. Verify permissions were cleaned up
	var permCount int
	if err := rawDB.QueryRow("SELECT COUNT(*) FROM principal_permissions WHERE principal_id=?", worker.PrincipalID).Scan(&permCount); err != nil {
		t.Fatalf("query principal_permissions: %v", err)
	}
	if permCount != 0 {
		t.Fatalf("principal_permissions count = %d, want 0", permCount)
	}

	// 4. Verify principals.revoked is set to 1
	var isRevoked int
	if err := rawDB.QueryRow("SELECT revoked FROM principals WHERE id=?", worker.PrincipalID).Scan(&isRevoked); err != nil {
		t.Fatalf("query principals revoked: %v", err)
	}
	if isRevoked != 1 {
		t.Fatalf("principals.revoked = %d, want 1", isRevoked)
	}

	// 5. Worker 2 acknowledges current plan revision and can immediately claim the unlocked Task 1
	if err := s.AcknowledgePlan(ctx, p.ID, worker2.Token, 1); err != nil {
		t.Fatalf("Worker 2 AcknowledgePlan: %v", err)
	}
	reclaimed, err := s.ClaimTask(ctx, p.ID, worker2.Token, task1.ID, time.Minute)
	if err != nil {
		t.Fatalf("Worker 2 failed to claim unlocked task: %v", err)
	}
	if reclaimed.Owner != worker2.PrincipalID || reclaimed.Status != "running" {
		t.Fatalf("Worker 2 reclaimed task invalid: %+v", reclaimed)
	}

	// 6. Revoked Worker 1 making future calls yields ErrUnauthorized (HTTP 401)
	_, err = s.WhoAmI(ctx, p.ID, worker.Token)
	if !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("WhoAmI on revoked worker error = %v, want ErrUnauthorized", err)
	}

	_, err = s.ListTasks(ctx, p.ID, worker.Token, 50)
	if !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("ListTasks on revoked worker error = %v, want ErrUnauthorized", err)
	}

	// 7. Defensive revocation boundary tests:
	// A. Revoking already-revoked principal returns ErrNotFound
	err = s.RevokePrincipal(ctx, p.ID, owner.Token, worker.PrincipalID)
	if !errors.Is(err, coord.ErrNotFound) {
		t.Fatalf("revoking already-revoked principal error = %v, want ErrNotFound", err)
	}

	// B. Owner revoking self is rejected
	err = s.RevokePrincipal(ctx, p.ID, owner.Token, owner.PrincipalID)
	if err == nil || !strings.Contains(err.Error(), "cannot revoke your own credential") {
		t.Fatalf("owner revoking self error = %v, want 'cannot revoke your own credential'", err)
	}
}

// -----------------------------------------------------------------------------
// 5. Web UI Strict CSP & Sandboxed Diagram Iframe
// -----------------------------------------------------------------------------

func TestTier5_Adversarial_WebUI_StrictCSPAndSandboxedDiagramIframe(t *testing.T) {
	s, _, _, _, _ := setup(t)

	handler := coord.NewWebHandler(s)

	doGet := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "http://localhost:8080"+path, nil)
		req.RemoteAddr = "127.0.0.1:23456"
		req.Host = "localhost:8080"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// 1. Verify Main Dashboard CSP Header on GET /
	recRoot := doGet("/")
	if recRoot.Code != http.StatusOK {
		t.Fatalf("GET / returned %d", recRoot.Code)
	}

	cspMain := recRoot.Header().Get("Content-Security-Policy")
	if cspMain == "" {
		t.Fatal("missing Content-Security-Policy header on /")
	}

	// Required CSP directives for main UI
	mustContain := []string{
		"default-src 'self'",
		"script-src 'self'",
		"style-src 'self'",
		"img-src 'self' data:",
		"base-uri 'none'",
		"frame-ancestors 'none'",
		"form-action 'self'",
	}
	for _, directive := range mustContain {
		if !strings.Contains(cspMain, directive) {
			t.Errorf("main CSP missing %q: full CSP = %s", directive, cspMain)
		}
	}

	// FORBIDDEN in main CSP: unsafe-inline, unsafe-eval, external http/https
	if strings.Contains(cspMain, "'unsafe-inline'") {
		t.Errorf("SECURITY VULNERABILITY: main CSP contains 'unsafe-inline': %s", cspMain)
	}
	if strings.Contains(cspMain, "'unsafe-eval'") {
		t.Errorf("SECURITY VULNERABILITY: main CSP contains 'unsafe-eval': %s", cspMain)
	}
	if strings.Contains(cspMain, "http:") || strings.Contains(cspMain, "https:") {
		t.Errorf("SECURITY VULNERABILITY: main CSP allows external origins: %s", cspMain)
	}

	// 2. Verify Diagram Iframe CSP on GET /diagram.html
	recDiagram := doGet("/diagram.html")
	if recDiagram.Code != http.StatusOK {
		t.Fatalf("GET /diagram.html returned %d", recDiagram.Code)
	}

	cspDiagram := recDiagram.Header().Get("Content-Security-Policy")
	if cspDiagram == "" {
		t.Fatal("missing Content-Security-Policy header on /diagram.html")
	}

	diagramDirectives := []string{
		"default-src 'none'",
		"connect-src 'none'", // Crucial: prevents diagram iframe from calling home or exfiltrating data
		"frame-ancestors 'self'",
		"base-uri 'none'",
		"form-action 'none'",
	}
	for _, directive := range diagramDirectives {
		if !strings.Contains(cspDiagram, directive) {
			t.Errorf("diagram CSP missing %q: full CSP = %s", directive, cspDiagram)
		}
	}

	// 3. Static Code Audit of UI Assets
	// A. Audit ui/index.html: No inline scripts, no inline styles, no inline event attributes
	recIndexHTML := doGet("/")
	indexHTML := recIndexHTML.Body.String()

	// Check for <script> tags with inline body
	scriptBodyRegex := regexp.MustCompile(`(?i)<script\b[^>]*>(.+?)</script>`)
	for _, match := range scriptBodyRegex.FindAllStringSubmatch(indexHTML, -1) {
		body := strings.TrimSpace(match[1])
		if len(body) > 0 {
			t.Errorf("SECURITY AUDIT: index.html has inline script body: %s", body)
		}
	}

	// Check for <style> tags
	if strings.Contains(strings.ToLower(indexHTML), "<style") {
		t.Errorf("SECURITY AUDIT: index.html contains inline <style> tag")
	}

	// Check for inline onclick, onload, onerror attributes
	inlineEventRegex := regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
	if match := inlineEventRegex.FindString(indexHTML); match != "" {
		t.Errorf("SECURITY AUDIT: index.html contains inline event handler attribute %s", match)
	}

	// B. Audit ui/app.js: Sandboxed diagram iframe creation
	recAppJS := doGet("/app.js")
	appJS := recAppJS.Body.String()

	if !strings.Contains(appJS, "frame.setAttribute('sandbox','allow-scripts')") &&
		!strings.Contains(appJS, `frame.setAttribute("sandbox","allow-scripts")`) {
		t.Errorf("SECURITY AUDIT: app.js does not set sandbox='allow-scripts' on diagram iframe")
	}

	// Verify absence of allow-same-origin on diagram iframe (which would allow sandbox escape)
	if strings.Contains(appJS, "allow-same-origin") {
		t.Errorf("SECURITY VULNERABILITY: diagram iframe includes 'allow-same-origin', breaking sandbox containment")
	}
}

// -----------------------------------------------------------------------------
// 6. In-Memory Credential & Sensitive Data Clearing on 401/403 Access Loss
// -----------------------------------------------------------------------------

func TestTier5_Adversarial_WebUI_InMemoryCredentialClearingOn401And403(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()

	handler := coord.NewWebHandler(s)

	doLoopback := func(method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
		var reqData []byte
		if body != nil {
			reqData, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, "http://localhost:8080"+path, bytes.NewReader(reqData))
		req.RemoteAddr = "127.0.0.1:34567"
		req.Host = "localhost:8080"
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "http://localhost:8080")
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// 1. Server-side session logout test
	recLogin := doLoopback(http.MethodPost, "/api/login", map[string]string{
		"project_id": p.ID,
		"token":      owner.Token,
	}, nil)
	if recLogin.Code != http.StatusOK {
		t.Fatalf("login failed: %d", recLogin.Code)
	}

	var sessionCookie *http.Cookie
	for _, c := range recLogin.Result().Cookies() {
		if c.Name == "gossip_session" {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("missing session cookie")
	}

	// Verify project access before logout
	recProjectsPre := doLoopback(http.MethodGet, "/api/projects", nil, sessionCookie)
	if recProjectsPre.Code != http.StatusOK || !strings.Contains(recProjectsPre.Body.String(), p.ID) {
		t.Fatalf("expected project %s in /api/projects", p.ID)
	}

	// Call logout
	recLogout := doLoopback(http.MethodPost, "/api/logout", map[string]any{}, sessionCookie)
	if recLogout.Code != http.StatusOK {
		t.Fatalf("logout returned %d: %s", recLogout.Code, recLogout.Body.String())
	}

	// Verify session cookie was cleared in response header
	var clearedCookie *http.Cookie
	for _, c := range recLogout.Result().Cookies() {
		if c.Name == "gossip_session" {
			clearedCookie = c
			break
		}
	}
	if clearedCookie == nil || clearedCookie.MaxAge != -1 {
		t.Fatalf("expected cookie MaxAge -1 after logout, got: %+v", clearedCookie)
	}

	// Verify that calls with old session cookie return empty projects
	recProjectsPost := doLoopback(http.MethodGet, "/api/projects", nil, sessionCookie)
	if recProjectsPost.Code != http.StatusOK || strings.TrimSpace(recProjectsPost.Body.String()) != "[]" {
		t.Fatalf("expected empty array [] after logout, got %d: %s", recProjectsPost.Code, recProjectsPost.Body.String())
	}

	// 2. White-box frontend code audit of app.js session clearing invariants
	recAppJS := doLoopback(http.MethodGet, "/app.js", nil, nil)
	appJS := recAppJS.Body.String()

	// A. Verify expireProjectSession exists and resets all sensitive state collections
	expectedResetFields := []string{
		"projects:[]",
		"projectOverview:[]",
		"project:null",
		"plan:null",
		"tasks:[]",
		"agents:[]",
		"messages:[]",
		"events:[]",
		"repositories:[]",
		"workspaces:[]",
		"integrations:[]",
	}
	for _, field := range expectedResetFields {
		if !strings.Contains(appJS, field) {
			t.Errorf("expireProjectSession missing state reset for %q in app.js", field)
		}
	}

	// B. Verify automatic invocation on HTTP 401
	if !strings.Contains(appJS, "if(response.status===401&&projectPath){expireProjectSession(decodeURIComponent(projectPath[1]));}") &&
		!strings.Contains(appJS, "response.status===401") {
		t.Error("app.js request handler does not automatically trigger expireProjectSession on HTTP 401")
	}

	// C. Verify handling of HTTP 403 in refresh() clears read data
	if !strings.Contains(appJS, "e.status===403") || !strings.Contains(appJS, "state.projectReadDenied=true") {
		t.Error("app.js refresh catch block does not handle 403 access denial")
	}

	// D. Verify #logout click handler invokes /api/logout and expireProjectSession
	if !strings.Contains(appJS, "$('#logout').addEventListener('click'") {
		t.Error("app.js missing #logout click event listener")
	}
	if !strings.Contains(appJS, "expireProjectSession(state.project?.project_id)") {
		t.Error("app.js #logout does not call expireProjectSession")
	}

	// 3. Worker 401 on Revoked Principal via Web API
	// Worker logs into web session
	recWorkerLogin := doLoopback(http.MethodPost, "/api/login", map[string]string{
		"project_id": p.ID,
		"token":      worker.Token,
	}, nil)
	if recWorkerLogin.Code != http.StatusOK {
		t.Fatalf("worker login failed: %d", recWorkerLogin.Code)
	}
	var workerCookie *http.Cookie
	for _, c := range recWorkerLogin.Result().Cookies() {
		if c.Name == "gossip_session" {
			workerCookie = c
			break
		}
	}

	// Revoke worker
	if err := s.RevokePrincipal(ctx, p.ID, owner.Token, worker.PrincipalID); err != nil {
		t.Fatalf("RevokePrincipal: %v", err)
	}

	// Worker attempts to read tasks via project endpoint
	recTasks := doLoopback(http.MethodGet, "/api/projects/"+p.ID+"/tasks", nil, workerCookie)
	if recTasks.Code != http.StatusUnauthorized {
		t.Fatalf("expected HTTP 401 Unauthorized for revoked worker web session, got %d: %s", recTasks.Code, recTasks.Body.String())
	}
	if !strings.Contains(recTasks.Body.String(), "access is no longer valid") {
		t.Fatalf("expected 'access is no longer valid' error message, got: %s", recTasks.Body.String())
	}
}

// -----------------------------------------------------------------------------
// 7. Versioned Starter Role Presets (v1.0.0), Canonicalization & Defense-in-Depth
// -----------------------------------------------------------------------------

func TestTier5_Adversarial_RolePresetsVersion100AndDefenseInDepthSQL(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()

	// 1. Verify WorkRolePresets specification
	presets := coord.WorkRolePresets()
	if len(presets) != 6 {
		t.Fatalf("expected exactly 6 starter role presets, got %d", len(presets))
	}

	expectedPresets := map[string]bool{
		"coordinator": true,
		"researcher":  true,
		"implementer": true,
		"reviewer":    true,
		"verifier":    true,
		"integrator":  true,
	}

	allowedSourceDomains := []string{
		"openai.github.io",
		"developers.openai.com",
	}

	for _, preset := range presets {
		if !expectedPresets[preset.Name] {
			t.Errorf("unexpected preset name: %s", preset.Name)
		}
		if preset.Version != "1.0.0" {
			t.Errorf("[%s] version = %s, want 1.0.0", preset.Name, preset.Version)
		}
		if !strings.Contains(preset.Provenance, "Gossip-authored") {
			t.Errorf("[%s] provenance missing 'Gossip-authored': %s", preset.Name, preset.Provenance)
		}
		if len(strings.TrimSpace(preset.Duties)) == 0 ||
			len(strings.TrimSpace(preset.ExpectedOutput)) == 0 ||
			len(strings.TrimSpace(preset.HandoffFormat)) == 0 ||
			len(strings.TrimSpace(preset.Escalation)) == 0 {
			t.Errorf("[%s] has empty duty or handoff metadata", preset.Name)
		}

		// Security check: Only verified first-party doc sources are referenced
		for _, src := range preset.Sources {
			if !strings.HasPrefix(src, "https://") {
				t.Errorf("[%s] insecure source URL: %s", preset.Name, src)
			}
			domainValid := false
			for _, domain := range allowedSourceDomains {
				if strings.Contains(src, domain) {
					domainValid = true
					break
				}
			}
			if !domainValid {
				t.Errorf("[%s] unvetted external source domain: %s", preset.Name, src)
			}
		}
	}

	// 2. Role Canonicalization & Auto-population
	// A. Case-insensitive matching: "RESEARCHER" with empty duties auto-canonicalizes to "researcher"
	r1, err := s.SetWorkRole(ctx, p.ID, owner.Token, worker.PrincipalID, "", "RESEARCHER", "", 0, 0)
	if err != nil {
		t.Fatalf("SetWorkRole with 'RESEARCHER' failed: %v", err)
	}
	if r1.Name != "researcher" {
		t.Fatalf("expected canonicalized Name 'researcher', got %s", r1.Name)
	}
	if !strings.Contains(r1.Duties, "Preset version: 1.0.0") || !strings.Contains(r1.Duties, "Answer only the assigned questions") {
		t.Fatalf("expected populated duties from preset, got: %s", r1.Duties)
	}

	// B. Whitespace padded: "  CoOrDiNaToR  "
	r2, err := s.SetWorkRole(ctx, p.ID, owner.Token, worker.PrincipalID, "", "  CoOrDiNaToR  ", "", 0, 1)
	if err != nil {
		t.Fatalf("SetWorkRole with '  CoOrDiNaToR  ' failed: %v", err)
	}
	if r2.Name != "coordinator" {
		t.Fatalf("expected canonicalized Name 'coordinator', got %s", r2.Name)
	}
	if !strings.Contains(r2.Duties, "Maintain the user-approved goal and plan") {
		t.Fatalf("expected populated coordinator duties, got: %s", r2.Duties)
	}

	// 3. Boundary & Error Validations
	// A. Custom role without duties -> REJECTED
	_, err = s.SetWorkRole(ctx, p.ID, owner.Token, worker.PrincipalID, "", "custom_qa_lead", "", 0, 2)
	if err == nil || !strings.Contains(err.Error(), "custom roles require duties") {
		t.Fatalf("expected error for custom role without duties, got: %v", err)
	}

	// B. Cleared role (name="") with duties -> REJECTED
	_, err = s.SetWorkRole(ctx, p.ID, owner.Token, worker.PrincipalID, "", "", "lingering duties", 0, 2)
	if err == nil || !strings.Contains(err.Error(), "cleared role cannot have duties or expiration") {
		t.Fatalf("expected error for cleared role with duties, got: %v", err)
	}

	// C. Role expiration in the past -> REJECTED
	pastTime := time.Now().Add(-10 * time.Minute).UnixMilli()
	_, err = s.SetWorkRole(ctx, p.ID, owner.Token, worker.PrincipalID, "", "reviewer", "", pastTime, 2)
	if err == nil || !strings.Contains(err.Error(), "expiration must be in the future") {
		t.Fatalf("expected error for past expiration, got: %v", err)
	}

	// 4. SQL Defense-in-Depth Verification:
	// requireAssignedWorkRole joins principals with p.revoked=0.
	// Assign a valid role to Worker, then revoke Worker, and verify task claiming with required role is rejected.
	_, err = s.SetWorkRole(ctx, p.ID, owner.Token, worker.PrincipalID, "", "implementer", "", 0, 2)
	if err != nil {
		t.Fatalf("SetWorkRole implementer: %v", err)
	}

	task, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Role Protected Task", "Desc", "implementation", coord.ReportContract{
		ReviewType:   "full_code",
		RequiredRole: "implementer",
	}, nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Revoke Worker
	if err := s.RevokePrincipal(ctx, p.ID, owner.Token, worker.PrincipalID); err != nil {
		t.Fatalf("RevokePrincipal: %v", err)
	}

	// Even though work_roles table still contains an active 'implementer' row for worker,
	// claiming must fail because principal is revoked
	_, err = s.ClaimTask(ctx, p.ID, worker.Token, task.ID, time.Minute)
	if !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized when revoked worker claims role-required task, got: %v", err)
	}
}

// -----------------------------------------------------------------------------
// 8. Review Deliverable Enforcement & Benchmark Rubric Evaluation
// -----------------------------------------------------------------------------

func TestTier5_Adversarial_ReviewDeliverableEnforcementAndBenchmarkRubrics(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()

	// 1. Task Planning Review Deliverable Enforcement
	// A. Missing review_type -> REJECTED with explicit planning guidance prompt
	_, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Missing Review Type Task", "Desc", "research", coord.ReportContract{
		ReviewType: "",
	}, nil)
	if err == nil {
		t.Fatal("expected task creation without review_type to be rejected")
	}
	if !strings.Contains(err.Error(), "review_type must be chosen during planning") {
		t.Fatalf("error = %q, want review_type planning prompt guidance", err.Error())
	}

	// B. Invalid review_type -> REJECTED
	_, err = s.CreateTask(ctx, p.ID, owner.Token, 1, "Invalid Review Type Task", "Desc", "research", coord.ReportContract{
		ReviewType: "pseudo_review",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "invalid review type") {
		t.Fatalf("expected 'invalid review type' error, got: %v", err)
	}

	// C. diagram_report missing required formats -> REJECTED
	_, err = s.CreateTask(ctx, p.ID, owner.Token, 1, "Incomplete Diagram Formats", "Desc", "research", coord.ReportContract{
		ReviewType: "diagram_report",
		Formats:    []string{"markdown"}, // missing mermaid_sequence, mermaid_class
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "diagram_report requires markdown, mermaid_sequence, and mermaid_class formats") {
		t.Fatalf("expected diagram_report format requirement error, got: %v", err)
	}

	// D. full_code review request without saved candidate -> REJECTED
	fullCodeTask, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Full Code Task", "Desc", "implementation", coord.ReportContract{
		ReviewType: "full_code",
	}, nil)
	if err != nil {
		t.Fatalf("CreateTask full_code: %v", err)
	}

	claimedFC, err := s.ClaimTask(ctx, p.ID, worker.Token, fullCodeTask.ID, time.Minute)
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}

	err = s.RequestReview(ctx, p.ID, worker.Token, fullCodeTask.ID, claimedFC.Attempt)
	if err == nil || !strings.Contains(err.Error(), "full_code review requires a source diff saved as a candidate") {
		t.Fatalf("expected missing candidate error, got: %v", err)
	}

	// 2. Token & Cost Estimation Accuracy
	// Empty string -> 0 tokens
	if tokens := benchmarks.EstimateTokens(""); tokens != 0 {
		t.Fatalf("EstimateTokens(\"\") = %d, want 0", tokens)
	}

	codeSnippet := `package main

import "fmt"

func main() {
	fmt.Println("Hello, Gossip!")
}`
	estimatedTokens := benchmarks.EstimateTokens(codeSnippet)
	if estimatedTokens < 10 || estimatedTokens > 40 {
		t.Fatalf("EstimateTokens returned unusual count %d for code snippet", estimatedTokens)
	}

	// Pricing calculation:
	// 50,000 prompt tokens @ $2.50/1M = 125,000 micros
	// 10,000 completion tokens @ $10.00/1M = 100,000 micros
	// Total = 225,000 micros
	pricing := benchmarks.DefaultPricingModel()
	calcCost := benchmarks.EstimateCostMicros(50000, 10000, pricing)
	if calcCost != 225000 {
		t.Fatalf("EstimateCostMicros = %d micros, want 225000", calcCost)
	}

	// 3. Quality Rubric Scoring Accuracy & Boundary Cases
	// Known defects: 2 items
	knownDefects := []string{
		"unhandled nil session pointer during lease renewal",
		"missing mutex unlock on error exit path",
	}
	plannedScope := []string{
		"pkg/coord/lease.go",
		"session validation",
		"lock release",
	}

	// Case A: Review identifies 1 defect, 0 false positives
	partialReview := "In pkg/coord/lease.go, we fixed the unhandled nil session pointer during lease renewal. Session validation was improved."
	rubricA := benchmarks.EvaluateQualityRubric(partialReview, knownDefects, plannedScope, 0)
	if rubricA.DefectDetectionScore != 50.0 {
		t.Fatalf("DefectDetectionScore = %.1f, want 50.0", rubricA.DefectDetectionScore)
	}
	if rubricA.Precision != 100.0 {
		t.Fatalf("Precision = %.1f, want 100.0", rubricA.Precision)
	}
	if rubricA.FalsePositives != 0 {
		t.Fatalf("FalsePositives = %d, want 0", rubricA.FalsePositives)
	}

	// Case B: Review identifies 1 defect, 1 false positive
	rubricB := benchmarks.EvaluateQualityRubric(partialReview, knownDefects, plannedScope, 1)
	if rubricB.DefectDetectionScore != 50.0 {
		t.Fatalf("DefectDetectionScore = %.1f, want 50.0", rubricB.DefectDetectionScore)
	}
	// Precision: 1 true / (1 true + 1 false positive) * 100 = 50.0%
	if rubricB.Precision != 50.0 {
		t.Fatalf("Precision = %.1f, want 50.0", rubricB.Precision)
	}
	if rubricB.FalsePositives != 1 {
		t.Fatalf("FalsePositives = %d, want 1", rubricB.FalsePositives)
	}

	// Case C: Empty review text -> 0% defect detection, 0% coverage
	rubricEmpty := benchmarks.EvaluateQualityRubric("", knownDefects, plannedScope, 0)
	if rubricEmpty.DefectDetectionScore != 0.0 {
		t.Fatalf("empty review DefectDetectionScore = %.1f, want 0.0", rubricEmpty.DefectDetectionScore)
	}
	if rubricEmpty.CoverageScore != 0.0 {
		t.Fatalf("empty review CoverageScore = %.1f, want 0.0", rubricEmpty.CoverageScore)
	}

	// 4. Concurrency & Reproducibility Check
	var rubricWg sync.WaitGroup
	const rubricRacers = 16
	for i := 0; i < rubricRacers; i++ {
		rubricWg.Add(1)
		go func() {
			defer rubricWg.Done()
			r := benchmarks.EvaluateQualityRubric(partialReview, knownDefects, plannedScope, 1)
			if r.DefectDetectionScore != 50.0 || r.Precision != 50.0 || r.FalsePositives != 1 {
				t.Errorf("concurrent rubric mismatch: %+v", r)
			}
		}()
	}
	rubricWg.Wait()
}
