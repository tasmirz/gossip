package coord_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tasmirz/gossip/pkg/coord"
)

func generateTestClientCertPair(t *testing.T, cn string) (caPEM, certPEM, keyPEM []byte, tlsCert tls.Certificate, fingerprint string) {
	t.Helper()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn + "-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})

	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(leafKey)})
	tlsCert, err = tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(leafDER)
	fingerprint = hex.EncodeToString(sum[:])
	return caPEM, certPEM, keyPEM, tlsCert, fingerprint
}

func TestLocalDashboardOpensExistingProjectWithoutCredentialsOrCookies(t *testing.T) {
	s, _, proj, ownerCred, _ := setup(t)
	projectID, ownerToken := proj.ID, ownerCred.Token
	ctx := context.Background()
	if _, err := s.PublishPlan(ctx, projectID, ownerToken, "# Shared Goal\n\nExisting plan text", 1); err != nil {
		t.Fatal(err)
	}

	// Empty device registry (no saved credentials for setup's project).
	registry, err := coord.OpenDeviceRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()

	h := coord.NewWebHandlerWithRegistry(s, registry, "default", s.InstanceID())

	// Fresh browser with NO cookies lists existing DB project cleanly.
	reqProjects := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8081/api/projects", nil)
	reqProjects.RemoteAddr = "127.0.0.1:54321"
	recProjects := httptest.NewRecorder()
	h.ServeHTTP(recProjects, reqProjects)
	if recProjects.Code != http.StatusOK {
		t.Fatalf("GET /api/projects failed: %d %s", recProjects.Code, recProjects.Body.String())
	}
	var principals []coord.Principal
	if err := json.Unmarshal(recProjects.Body.Bytes(), &principals); err != nil || len(principals) != 1 || principals[0].ProjectID != projectID {
		t.Fatalf("unexpected projects response: err=%v body=%s", err, recProjects.Body.String())
	}

	// Direct project URL access works immediately with zero cookies.
	reqPlan := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8081/api/projects/"+projectID+"/plan", nil)
	reqPlan.RemoteAddr = "127.0.0.1:54321"
	recPlan := httptest.NewRecorder()
	h.ServeHTTP(recPlan, reqPlan)
	if recPlan.Code != http.StatusOK {
		t.Fatalf("GET /plan failed: %d %s", recPlan.Code, recPlan.Body.String())
	}
	if !strings.Contains(recPlan.Body.String(), "Existing plan text") {
		t.Fatalf("missing plan content: %s", recPlan.Body.String())
	}

	// Writing via local owner authority succeeds and records real owner principal.
	msgBody := `{"body":"Hello from local dashboard","request_id":"req-local-1"}`
	reqMsg := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8081/api/projects/"+projectID+"/messages", strings.NewReader(msgBody))
	reqMsg.RemoteAddr = "127.0.0.1:54321"
	reqMsg.Header.Set("Origin", "http://127.0.0.1:8081")
	reqMsg.Header.Set("Content-Type", "application/json")
	recMsg := httptest.NewRecorder()
	h.ServeHTTP(recMsg, reqMsg)
	if recMsg.Code != http.StatusOK {
		t.Fatalf("POST /messages failed: %d %s", recMsg.Code, recMsg.Body.String())
	}

	// Recovery export on an unimported legacy project must NOT fabricate a placeholder token.
	reqExport := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8081/api/projects/"+projectID+"/recovery-credential", strings.NewReader(`{}`))
	reqExport.RemoteAddr = "127.0.0.1:54321"
	reqExport.Header.Set("Origin", "http://127.0.0.1:8081")
	reqExport.Header.Set("Content-Type", "application/json")
	recExport := httptest.NewRecorder()
	h.ServeHTTP(recExport, reqExport)
	if recExport.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when no recovery credential saved, got %d %s", recExport.Code, recExport.Body.String())
	}
	if strings.Contains(recExport.Body.String(), "local-dashboard") {
		t.Fatalf("synthetic token leaked in response: %s", recExport.Body.String())
	}
}

func TestLocalDashboardSecurityAndAgentProfileBoundaries(t *testing.T) {
	s, _, proj, ownerCred, _ := setup(t)
	projectID, ownerID := proj.ID, ownerCred.PrincipalID
	registry, err := coord.OpenDeviceRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()

	h := coord.NewWebHandlerWithRegistry(s, registry, "default", s.InstanceID())

	// 1. Cross-origin POST must fail.
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8081/api/projects/"+projectID+"/messages", strings.NewReader(`{"body":"csrf"}`))
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set("Origin", "http://evil.example:8081")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-origin POST, got %d", rec.Code)
	}

	// 2. Cross-site Sec-Fetch-Site must fail on GET API too.
	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8081/api/projects", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-site Sec-Fetch-Site, got %d", rec.Code)
	}

	// 3. Spoofed forwarding headers must fail on loopback HTTP.
	for _, hdr := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Real-IP", "Via"} {
		req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8081/api/projects", nil)
		req.RemoteAddr = "127.0.0.1:50000"
		req.Header.Set(hdr, "198.51.100.10")
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403 for header %s, got %d", hdr, rec.Code)
		}
	}

	// 4. Direct LAN / non-loopback peer address or host fails.
	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8081/api/projects", nil)
	req.RemoteAddr = "192.168.1.44:50000"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-loopback peer, got %d", rec.Code)
	}

	// 5. Forged owner-selection or unknown-field payloads fail on normal operations;
	// unknown local-mode flags are rejected by DisallowUnknownFields.
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8081/api/projects/"+projectID+"/messages", strings.NewReader(`{"body":"x","local_owner":"`+ownerID+`"}`))
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set("Origin", "http://127.0.0.1:8081")
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 rejecting unknown local_owner field, got %d", rec.Code)
	}

	// 6. Security framing and referrer headers are present on responses.
	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8081/", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Frame-Options") != "DENY" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("missing security headers: XFO=%q RP=%q", rec.Header().Get("X-Frame-Options"), rec.Header().Get("Referrer-Policy"))
	}

	// 7. Non-default agent profile NEVER gets device-owner authority even with registry.
	agentHandler := coord.NewWebHandlerWithRegistry(s, registry, "worker-agent", s.InstanceID())
	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8081/api/projects/"+projectID+"/plan", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	rec = httptest.NewRecorder()
	agentHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for non-default profile without credential login, got %d %s", rec.Code, rec.Body.String())
	}

	// 8. Empty token passed directly to Store fails without server-side local authority.
	if _, err := s.WhoAmI(context.Background(), projectID, ""); err == nil {
		t.Fatal("expected Store.WhoAmI with empty token to fail without local authority context")
	}
}

func TestLegacyOwnerMigrationAndExplicitRepair(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "coord.db")

	s, err := coord.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	p1, o1Cred, err := s.CreateProject(ctx, "Single Owner Legacy", "Owner One")
	if err != nil {
		t.Fatal(err)
	}
	p2, o2ACred, err := s.CreateProject(ctx, "Multi Owner Legacy", "Owner A")
	if err != nil {
		t.Fatal(err)
	}
	o2BCred, err := s.AddPrincipal(ctx, p2.ID, o2ACred.Token, "Owner B", "owner")
	if err != nil {
		t.Fatal(err)
	}
	p3, o3Cred, err := s.CreateProject(ctx, "Zero Owner Legacy", "Owner Gone")
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	// Simulate legacy DB created before local_dashboard_owners existed:
	// drop the mapping & migration record, plus mark p3's owner revoked directly.
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rawDB.Exec(`DELETE FROM local_dashboard_owners; DELETE FROM coord_migrations WHERE name='local_dashboard_owners_v6'; UPDATE principals SET revoked=1 WHERE project_id=? AND id=?;`, p3.ID, o3Cred.PrincipalID); err != nil {
		t.Fatal(err)
	}
	_ = rawDB.Close()

	// Re-open triggers local_dashboard_owners_v6 migration.
	s, err = coord.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	registry, err := coord.OpenDeviceRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	h := coord.NewWebHandlerWithRegistry(s, registry, "default", s.InstanceID())

	// Check dashboard project states:
	// - p1 auto-migrated cleanly ("ready", owner o1Cred.PrincipalID)
	// - p2 is "ambiguous_owner" (NOT guessed via MIN(id)!)
	// - p3 is "missing_owner"
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8081/api/dashboard/projects", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard projects failed: %d %s", rec.Code, rec.Body.String())
	}
	var dash coord.DashboardProjectsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &dash); err != nil {
		t.Fatal(err)
	}
	byID := map[string]coord.DashboardProjectLocal{}
	for _, item := range dash.Local {
		byID[item.ProjectID] = item
	}
	if byID[p1.ID].Status != "ready" || byID[p1.ID].Owner == nil || byID[p1.ID].Owner.ID != o1Cred.PrincipalID {
		t.Fatalf("p1 should auto-migrate cleanly: %+v", byID[p1.ID])
	}
	if byID[p2.ID].Status != "ambiguous_owner" || len(byID[p2.ID].Candidates) != 2 {
		t.Fatalf("p2 should require explicit owner selection, not auto-pick MIN(id): %+v", byID[p2.ID])
	}
	if byID[p3.ID].Status != "missing_owner" {
		t.Fatalf("p3 should report missing_owner: %+v", byID[p3.ID])
	}

	// Opening ambiguous p2 before owner selection fails with 409.
	reqOpen := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8081/api/projects/"+p2.ID+"/agents", nil)
	reqOpen.RemoteAddr = "127.0.0.1:50000"
	recOpen := httptest.NewRecorder()
	h.ServeHTTP(recOpen, reqOpen)
	if recOpen.Code != http.StatusConflict {
		t.Fatalf("expected 409 for ambiguous owner project, got %d %s", recOpen.Code, recOpen.Body.String())
	}

	// Explicitly choose Owner B -> now p2 opens as Owner B.
	selBody, _ := json.Marshal(map[string]string{"principal_id": o2BCred.PrincipalID})
	reqSel := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8081/api/projects/"+p2.ID+"/local-owner", bytes.NewReader(selBody))
	reqSel.RemoteAddr = "127.0.0.1:50000"
	reqSel.Header.Set("Origin", "http://127.0.0.1:8081")
	reqSel.Header.Set("Content-Type", "application/json")
	recSel := httptest.NewRecorder()
	h.ServeHTTP(recSel, reqSel)
	if recSel.Code != http.StatusOK {
		t.Fatalf("select local owner failed: %d %s", recSel.Code, recSel.Body.String())
	}
	// Verify write happens as Owner B specifically, not Owner A!
	reqMsg := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8081/api/projects/"+p2.ID+"/messages", strings.NewReader(`{"body":"as owner B","request_id":"req-b1"}`))
	reqMsg.RemoteAddr = "127.0.0.1:50000"
	reqMsg.Header.Set("Origin", "http://127.0.0.1:8081")
	reqMsg.Header.Set("Content-Type", "application/json")
	recMsg := httptest.NewRecorder()
	h.ServeHTTP(recMsg, reqMsg)
	if recMsg.Code != http.StatusOK {
		t.Fatalf("message as selected Owner B failed: %d %s", recMsg.Code, recMsg.Body.String())
	}
	var msg coord.Message
	if err := json.Unmarshal(recMsg.Body.Bytes(), &msg); err != nil || msg.Sender != o2BCred.PrincipalID {
		t.Fatalf("expected sender Owner B (%s), got %+v err=%v", o2BCred.PrincipalID, msg, err)
	}
	_ = o1Cred
}

func TestRemoteProjectCollisionAndSearchConfidentiality(t *testing.T) {
	ctx := context.Background()
	caPEM, certPEM, keyPEM, tlsCert, fp := generateTestClientCertPair(t, "peer-node")
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.pem")
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")
	for p, b := range map[string][]byte{caFile: caPEM, certFile: certPEM, keyFile: keyPEM} {
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Prepare a shared project ID so local DB and remote peer collide on project_id.
	collidingID, peerOwnerCred, err := coord.PrepareCredential("")
	if err != nil {
		t.Fatal(err)
	}
	_, localOwnerCred, err := coord.PrepareCredential(collidingID)
	if err != nil {
		t.Fatal(err)
	}
	_, peerWorkerCred, err := coord.PrepareCredential(collidingID)
	if err != nil {
		t.Fatal(err)
	}

	// Local store owns collidingID locally.
	localStore, err := coord.Open(filepath.Join(t.TempDir(), "local.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer localStore.Close()
	if _, err := localStore.CreatePreparedProject(ctx, collidingID, "Local Instance Project", "Local Owner", localOwnerCred); err != nil {
		t.Fatal(err)
	}
	if _, err := localStore.PublishPlan(ctx, collidingID, localOwnerCred.Token, "# Local Only Plan", 0); err != nil {
		t.Fatal(err)
	}
	// Seed a DM between two non-owner principals + a general message to test SearchProject DM isolation.
	w1, err := localStore.AddPrincipal(ctx, collidingID, localOwnerCred.Token, "Worker1", "worker")
	if err != nil {
		t.Fatal(err)
	}
	w2, err := localStore.AddPrincipal(ctx, collidingID, localOwnerCred.Token, "Worker2", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := localStore.SendMessage(ctx, collidingID, w1.Token, w2.PrincipalID, "", "", "secret-dm-needle only for w1 and w2"); err != nil {
		t.Fatal(err)
	}
	if _, err := localStore.SendMessage(ctx, collidingID, w1.Token, "", "", "", "shared-channel secret-dm-needle public"); err != nil {
		t.Fatal(err)
	}

	// Remote peer store ALSO has collidingID (distinct HomeID), where local peer is ONLY a non-owner worker!
	peerStore, err := coord.Open(filepath.Join(t.TempDir(), "peer.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer peerStore.Close()
	if err := peerStore.SetRemoteAccessEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, err := peerStore.CreatePreparedProject(ctx, collidingID, "Remote Peer Project", "Peer Owner", peerOwnerCred); err != nil {
		t.Fatal(err)
	}
	if _, err := peerStore.AddPreparedPrincipal(ctx, collidingID, peerOwnerCred.Token, "Joined Worker", "worker", peerWorkerCred); err != nil {
		t.Fatal(err)
	}
	if _, err := peerStore.AddCertificateGrant(ctx, collidingID, peerOwnerCred.Token, fp); err != nil {
		t.Fatal(err)
	}
	if _, err := peerStore.PublishPlan(ctx, collidingID, peerOwnerCred.Token, "# Remote Peer Plan", 0); err != nil {
		t.Fatal(err)
	}

	// Start authenticated peer mTLS RPC server.
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	peerSrv := httptest.NewUnstartedServer(coord.RPCHandlerWithCertificateGrants(peerStore))
	peerSrv.TLS = &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
	}
	peerSrv.StartTLS()
	defer peerSrv.Close()

	// Join remote project into local DeviceRegistry.
	registry, err := coord.OpenDeviceRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	h := coord.NewWebHandlerWithRegistry(localStore, registry, "default", localStore.InstanceID())

	joinPayload, _ := json.Marshal(map[string]string{
		"name":         "Remote Joined Copy",
		"endpoint":     peerSrv.URL,
		"project_id":   collidingID,
		"principal_id": peerWorkerCred.PrincipalID,
		"token":        peerWorkerCred.Token,
		"ca_file":      caFile,
		"cert_file":    certFile,
		"key_file":     keyFile,
	})
	reqJoin := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8081/api/local/join-remote", bytes.NewReader(joinPayload))
	reqJoin.RemoteAddr = "127.0.0.1:50000"
	reqJoin.Header.Set("Origin", "http://127.0.0.1:8081")
	reqJoin.Header.Set("Content-Type", "application/json")
	recJoin := httptest.NewRecorder()
	h.ServeHTTP(recJoin, reqJoin)
	if recJoin.Code != http.StatusOK {
		t.Fatalf("join remote failed: %d %s", recJoin.Code, recJoin.Body.String())
	}

	// Opening with ?home=<peerHomeID> MUST hit the remote peer as remote worker, NEVER acquiring local owner authority!
	reqRemPlan := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8081/api/projects/"+collidingID+"/plan?home="+peerStore.InstanceID(), nil)
	reqRemPlan.RemoteAddr = "127.0.0.1:50000"
	recRemPlan := httptest.NewRecorder()
	h.ServeHTTP(recRemPlan, reqRemPlan)
	if recRemPlan.Code != http.StatusOK || !strings.Contains(recRemPlan.Body.String(), "Remote Peer Plan") {
		t.Fatalf("remote project with colliding ID did not route by home_id: code=%d body=%s", recRemPlan.Code, recRemPlan.Body.String())
	}

	// Attempting owner-only settings write against ?home=<peerHomeID> must fail with 403 because remote principal is a worker!
	reqRemSettings := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8081/api/projects/"+collidingID+"/settings?home="+peerStore.InstanceID(), strings.NewReader(`{"settings":{"revision":1,"max_parallel":8,"max_lease_seconds":3600,"summary_token_target":1000,"default_review_mode":"report_first"}}`))
	reqRemSettings.RemoteAddr = "127.0.0.1:50000"
	reqRemSettings.Header.Set("Origin", "http://127.0.0.1:8081")
	reqRemSettings.Header.Set("Content-Type", "application/json")
	recRemSettings := httptest.NewRecorder()
	h.ServeHTTP(recRemSettings, reqRemSettings)
	if recRemSettings.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for remote worker modifying settings even though local project has same ID, got %d %s", recRemSettings.Code, recRemSettings.Body.String())
	}

	// Authorized search on local project MUST exclude DMs between Worker1 and Worker2 while returning shared message!
	reqSearch := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8081/api/projects/"+collidingID+"/search?q=secret-dm-needle", nil)
	reqSearch.RemoteAddr = "127.0.0.1:50000"
	recSearch := httptest.NewRecorder()
	h.ServeHTTP(recSearch, reqSearch)
	if recSearch.Code != http.StatusOK {
		t.Fatalf("search failed: %d %s", recSearch.Code, recSearch.Body.String())
	}
	var searchRes coord.ProjectSearchResult
	if err := json.Unmarshal(recSearch.Body.Bytes(), &searchRes); err != nil {
		t.Fatal(err)
	}
	if len(searchRes.Messages) != 1 || !strings.Contains(searchRes.Messages[0].Body, "shared-channel") {
		t.Fatalf("search leaked or missed expected messages: %+v", searchRes.Messages)
	}
}

func TestLocalDashboardInspectCheckoutPlansGraphifyChecklists(t *testing.T) {
	ctx := context.Background()
	s, err := coord.Open(filepath.Join(t.TempDir(), "coord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	registry, err := coord.OpenDeviceRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()

	// Create a local Git repository checkout for SuggestRepositoryInfo + InspectCheckout.
	checkoutDir := filepath.Join(t.TempDir(), "sample-repo")
	if err := os.MkdirAll(filepath.Join(checkoutDir, ".gossip"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-C", checkoutDir, "init"},
		{"-C", checkoutDir, "config", "user.email", "test@example.com"},
		{"-C", checkoutDir, "config", "user.name", "Test"},
		{"-C", checkoutDir, "commit", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v %s", args, err, string(out))
		}
	}

	h := coord.NewWebHandlerWithRegistry(s, registry, "default", s.InstanceID())

	// 1. Inspect checkout before creating project
	inspectBody, _ := json.Marshal(map[string]string{"local_path": checkoutDir})
	reqInspect := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8081/api/local/inspect-checkout", bytes.NewReader(inspectBody))
	reqInspect.RemoteAddr = "127.0.0.1:50000"
	reqInspect.Header.Set("Origin", "http://127.0.0.1:8081")
	reqInspect.Header.Set("Content-Type", "application/json")
	recInspect := httptest.NewRecorder()
	h.ServeHTTP(recInspect, reqInspect)
	if recInspect.Code != http.StatusOK || !strings.Contains(recInspect.Body.String(), "sample-repo") {
		t.Fatalf("inspect-checkout failed: %d %s", recInspect.Code, recInspect.Body.String())
	}

	// 2. Create project anchored to checkoutDir via local dashboard
	createBody, _ := json.Marshal(map[string]string{"name": "Sample Repo", "local_path": checkoutDir, "request_id": "req-create-1"})
	reqCreate := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8081/api/projects/create", bytes.NewReader(createBody))
	reqCreate.RemoteAddr = "127.0.0.1:50000"
	reqCreate.Header.Set("Origin", "http://127.0.0.1:8081")
	reqCreate.Header.Set("Content-Type", "application/json")
	recCreate := httptest.NewRecorder()
	h.ServeHTTP(recCreate, reqCreate)
	if recCreate.Code != http.StatusOK {
		t.Fatalf("create local project failed: %d %s", recCreate.Code, recCreate.Body.String())
	}
	var created struct {
		Project coord.Principal `json:"project"`
	}
	if err := json.Unmarshal(recCreate.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	// 3. Re-inspecting same checkout reports existing match
	reqInspect2 := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8081/api/local/inspect-checkout", bytes.NewReader(inspectBody))
	reqInspect2.RemoteAddr = "127.0.0.1:50000"
	reqInspect2.Header.Set("Origin", "http://127.0.0.1:8081")
	reqInspect2.Header.Set("Content-Type", "application/json")
	recInspect2 := httptest.NewRecorder()
	h.ServeHTTP(recInspect2, reqInspect2)
	if recInspect2.Code != http.StatusOK || !strings.Contains(recInspect2.Body.String(), created.Project.ProjectID) {
		t.Fatalf("expected inspect-checkout to report existing project %s, got: %s", created.Project.ProjectID, recInspect2.Body.String())
	}

	// 4. Write second plan revision, then fetch /plans history
	_, _, err = s.CreateProject(ctx, "Dummy", "Owner") // unused extra project
	if err != nil {
		t.Fatal(err)
	}
	planBody, _ := json.Marshal(map[string]any{"body": "# Architecture\nRevised step", "revision": 0, "request_id": "req-plan-1"})
	reqPlanPost := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8081/api/projects/"+created.Project.ProjectID+"/plan", bytes.NewReader(planBody))
	reqPlanPost.RemoteAddr = "127.0.0.1:50000"
	reqPlanPost.Header.Set("Origin", "http://127.0.0.1:8081")
	reqPlanPost.Header.Set("Content-Type", "application/json")
	recPlanPost := httptest.NewRecorder()
	h.ServeHTTP(recPlanPost, reqPlanPost)
	if recPlanPost.Code != http.StatusOK {
		t.Fatalf("plan update failed: %d %s", recPlanPost.Code, recPlanPost.Body.String())
	}
	reqPlans := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8081/api/projects/"+created.Project.ProjectID+"/plans", nil)
	reqPlans.RemoteAddr = "127.0.0.1:50000"
	recPlans := httptest.NewRecorder()
	h.ServeHTTP(recPlans, reqPlans)
	if recPlans.Code != http.StatusOK || !strings.Contains(recPlans.Body.String(), "Revised step") {
		t.Fatalf("plans history failed: %d %s", recPlans.Code, recPlans.Body.String())
	}

	// 5. Write Graphify artifact & personal checklist into checkoutDir/.gossip and verify /graphify + /checklists
	if err := os.WriteFile(filepath.Join(checkoutDir, ".gossip", "graph.json"), []byte(`{"nodes":{"pkg/coord/web.go":{"id":"pkg/coord/web.go","type":"file","label":"web.go","source_path":"pkg/coord/web.go"}},"edges":{"e1":{"source":"pkg/coord/web.go","target":"pkg/coord/store.go"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkoutDir, ".gossip", "personal_todos.json"), []byte(`{"todos":{"antigravity":[{"id":"pt-1","title":"Verify dark theme contrast","status":"open","priority":"high"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	reqGraph := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8081/api/projects/"+created.Project.ProjectID+"/graphify?q=web", nil)
	reqGraph.RemoteAddr = "127.0.0.1:50000"
	recGraph := httptest.NewRecorder()
	h.ServeHTTP(recGraph, reqGraph)
	if recGraph.Code != http.StatusOK || !strings.Contains(recGraph.Body.String(), `"status":"ready"`) || !strings.Contains(recGraph.Body.String(), "pkg/coord/web.go") {
		t.Fatalf("graphify failed: %d %s", recGraph.Code, recGraph.Body.String())
	}

	reqTodos := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8081/api/projects/"+created.Project.ProjectID+"/checklists", nil)
	reqTodos.RemoteAddr = "127.0.0.1:50000"
	recTodos := httptest.NewRecorder()
	h.ServeHTTP(recTodos, reqTodos)
	if recTodos.Code != http.StatusOK || !strings.Contains(recTodos.Body.String(), "Verify dark theme contrast") {
		t.Fatalf("checklists failed: %d %s", recTodos.Code, recTodos.Body.String())
	}
}
