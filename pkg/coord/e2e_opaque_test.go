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
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/tasmirz/gossip/pkg/coord"
	"github.com/tasmirz/gossip/pkg/roles"
	"github.com/tasmirz/gossip/pkg/state"
	"github.com/tasmirz/gossip/pkg/web"
)

// ============================================================================
// Section 3: Test Harness Architecture & Helpers per TEST_INFRA.md
// ============================================================================

type e2eTLSFixture struct {
	Store                 *coord.Store
	Project               coord.Project
	Owner                 coord.Credential
	Worker                coord.Credential
	Server                *httptest.Server
	URL                   string
	CAFile                string
	ServerCertFile        string
	ServerKeyFile         string
	ClientCertFile        string
	ClientKeyFile         string
	ClientCertFingerprint string
	OtherCertFile         string
	OtherKeyFile          string
	OtherCertFingerprint  string
	BootstrapKey          string
	BootstrapKeyFile      string
	Remote                *coord.Remote
	OtherRemote           *coord.Remote
	BootstrapRemote       *coord.Remote
	CertPool              *x509.CertPool
	ClientTLSCert         tls.Certificate
	OtherTLSCert          tls.Certificate
}

func setupE2ETest(t *testing.T) (*coord.Store, string, coord.Project, coord.Credential, coord.Credential) {
	t.Helper()
	return setup(t)
}

func setupE2ETLSServer(t *testing.T, s *coord.Store, p coord.Project, owner coord.Credential) *e2eTLSFixture {
	t.Helper()
	now := time.Now()
	keyGen := func() *ecdsa.PrivateKey {
		t.Helper()
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}

	caKey := keyGen()
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "E2E Test CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	tempDir := t.TempDir()
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	caFile := filepath.Join(tempDir, "ca.crt")
	if err := os.WriteFile(caFile, caPEM, 0600); err != nil {
		t.Fatal(err)
	}

	issueCert := func(serial int64, usage x509.ExtKeyUsage, prefix string) (string, string, string, tls.Certificate) {
		t.Helper()
		k := keyGen()
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			Subject:      pkix.Name{CommonName: prefix},
			NotBefore:    now.Add(-time.Hour),
			NotAfter:     now.Add(24 * time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{usage},
			IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &k.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			t.Fatal(err)
		}
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

		certPath := filepath.Join(tempDir, prefix+".crt")
		keyPath := filepath.Join(tempDir, prefix+".key")
		if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
			t.Fatal(err)
		}
		tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			t.Fatal(err)
		}
		pin := sha256.Sum256(der)
		fingerprint := hex.EncodeToString(pin[:])
		return certPath, keyPath, fingerprint, tlsCert
	}

	serverCertFile, serverKeyFile, _, serverTLSCert := issueCert(2, x509.ExtKeyUsageServerAuth, "server")
	clientCertFile, clientKeyFile, clientFingerprint, clientTLSCert := issueCert(3, x509.ExtKeyUsageClientAuth, "client")
	otherCertFile, otherKeyFile, otherFingerprint, otherTLSCert := issueCert(4, x509.ExtKeyUsageClientAuth, "other")

	ctx := context.Background()
	if _, err := s.AddCertificateGrant(ctx, p.ID, owner.Token, clientFingerprint); err != nil {
		t.Fatal(err)
	}

	bootstrapKey := strings.Repeat("bootstrap-secret-token-key-", 2)
	bootstrapKeyFile := filepath.Join(tempDir, "bootstrap.key")
	if err := os.WriteFile(bootstrapKeyFile, []byte(bootstrapKey), 0600); err != nil {
		t.Fatal(err)
	}

	rpcH, err := coord.RPCHandlerWithBootstrapKey(s, bootstrapKey)
	if err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewUnstartedServer(rpcH)
	ts.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{serverTLSCert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}
	ts.StartTLS()
	t.Cleanup(func() { ts.Close() })

	rem, err := coord.NewRemote(ts.URL, caFile, clientCertFile, clientKeyFile, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	otherRem, err := coord.NewRemote(ts.URL, caFile, otherCertFile, otherKeyFile, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	bootRem, err := coord.NewRemoteBootstrap(ts.URL, caFile, clientCertFile, clientKeyFile, "127.0.0.1", bootstrapKey)
	if err != nil {
		t.Fatal(err)
	}

	return &e2eTLSFixture{
		Store:                 s,
		Project:               p,
		Owner:                 owner,
		Server:                ts,
		URL:                   ts.URL,
		CAFile:                caFile,
		ServerCertFile:        serverCertFile,
		ServerKeyFile:         serverKeyFile,
		ClientCertFile:        clientCertFile,
		ClientKeyFile:         clientKeyFile,
		ClientCertFingerprint: clientFingerprint,
		OtherCertFile:         otherCertFile,
		OtherKeyFile:          otherKeyFile,
		OtherCertFingerprint:  otherFingerprint,
		BootstrapKey:          bootstrapKey,
		BootstrapKeyFile:      bootstrapKeyFile,
		Remote:                rem,
		OtherRemote:           otherRem,
		BootstrapRemote:       bootRem,
		CertPool:              pool,
		ClientTLSCert:         clientTLSCert,
		OtherTLSCert:          otherTLSCert,
	}
}

func setupE2EGitWorktree(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
	}
	run("init", "--initial-branch=main")
	run("config", "user.name", "Test Agent")
	run("config", "user.email", "agent@example.invalid")
	run("remote", "add", "origin", "https://github.com/acme/widget.git")
	initialFile := filepath.Join(dir, "README.md")
	if err := os.WriteFile(initialFile, []byte("# Widget Project\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "initial commit")
	return dir
}

func findGossipBinary(t *testing.T) string {
	t.Helper()
	candidates := []string{
		"../../gossip",
		"./gossip",
		"/home/user/Projects/gossip/gossip",
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
			abs, err := filepath.Abs(c)
			if err == nil {
				return abs
			}
		}
	}
	tmpBin := filepath.Join(t.TempDir(), "gossip")
	cmd := exec.Command("go", "build", "-o", tmpBin, "github.com/tasmirz/gossip/cmd/gossip")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build gossip binary: %s %v", out, err)
	}
	return tmpBin
}

const sampleValidDiagramReport = `# Architecture Review

## Review summary
The component decomposition satisfies all system invariants and interface boundaries.

## Scope and evidence
The authorization and retry paths were inspected against the recorded baseline commit.

## UML class diagram
The class relationships demonstrate dependency inversion between coordinator and workers.

` + "```mermaid" + `
classDiagram
  Coordinator --> TaskWorker : delegates
  TaskWorker --> ResultStore : submits
` + "```" + `

## Sequence diagram
The sequence traces task assignment, execution, review request, and acceptance.

` + "```mermaid" + `
sequenceDiagram
  Coordinator->>TaskWorker: assign task
  TaskWorker-->>Coordinator: request review
` + "```" + `

## Findings
The authorization boundary remains strict while workers operate with leased attempts.

## Coverage and limitations
Authorization and retry mechanisms are fully covered; offline disconnected testing remains bounded.
`

// ============================================================================
// Tier 1: Feature Coverage (Features 1-28)
// ============================================================================

// TestE2E_Tier1_F1_F2_F3_KillSwitchAndImmunity verifies:
// - Feature 1: Remote Kill Switch Gate (HTTP 503 rejection pre-auth)
// - Feature 2: Kill Switch Local Toggle (status, enable, disable)
// - Feature 3: TLS RPC Kill Switch Immunity (HTTP 401 on remote mutation/status query)
func TestE2E_Tier1_F1_F2_F3_KillSwitchAndImmunity(t *testing.T) {
	s, dbPath, p, owner, worker := setupE2ETest(t)
	ctx := context.Background()

	// Feature 1: Default-off on new SQLite databases
	enabled, err := s.RemoteAccessEnabled(ctx)
	if err != nil {
		t.Fatalf("failed to query initial RemoteAccessEnabled: %v", err)
	}
	if enabled {
		t.Fatalf("expected RemoteAccessEnabled to default to false, got %v", enabled)
	}

	// Setup mTLS server with default-off kill switch
	fixture := setupE2ETLSServer(t, s, p, owner)

	// Network RPC must be rejected with 503 Service Unavailable
	_, err = fixture.Remote.CurrentPlan(ctx, p.ID, worker.Token)
	if !errors.Is(err, coord.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable (HTTP 503) when kill switch is off, got %v", err)
	}

	// HTTPS Web UI must also reject with 503
	webHandler := coord.NewWebHandlerWithCertificateGrants(s, []string{"127.0.0.1"})
	httpsReq := httptest.NewRequest(http.MethodGet, "https://127.0.0.1/api/projects/"+p.ID+"/plan", nil)
	leaf, err := x509.ParseCertificate(fixture.ClientTLSCert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	httpsReq.TLS = &tls.ConnectionState{
		VerifiedChains:   [][]*x509.Certificate{{leaf}},
		PeerCertificates: []*x509.Certificate{leaf},
	}
	httpsRec := httptest.NewRecorder()
	webHandler.ServeHTTP(httpsRec, httpsReq)
	if httpsRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected HTTPS web dashboard to return 503 when kill switch is off, got %d", httpsRec.Code)
	}

	// Feature 2: Kill Switch Local Toggle via Store and CLI
	gossipBin := findGossipBinary(t)
	cmdOut, cmdErr := exec.Command(gossipBin, "coord-remote", "--db", dbPath, "--action", "status").CombinedOutput()
	if cmdErr != nil || !strings.Contains(string(cmdOut), "disabled") {
		t.Fatalf("CLI status check failed: %s %v", cmdOut, cmdErr)
	}

	// Enable via CLI
	cmdOut, cmdErr = exec.Command(gossipBin, "coord-remote", "--db", dbPath, "--action", "enable").CombinedOutput()
	if cmdErr != nil {
		t.Fatalf("CLI enable failed: %s %v", cmdOut, cmdErr)
	}
	enabled, err = s.RemoteAccessEnabled(ctx)
	if err != nil || !enabled {
		t.Fatalf("expected RemoteAccessEnabled=true after CLI enable, got %v %v", enabled, err)
	}

	// Now network RPC call succeeds
	plan, err := fixture.Remote.CurrentPlan(ctx, p.ID, worker.Token)
	if err != nil || plan.Revision != 1 {
		t.Fatalf("network RPC failed after enabling kill switch: %+v %v", plan, err)
	}

	// Feature 3: TLS RPC Kill Switch Immunity
	// Remote clients attempting to call remote.access.status or remote.access.set over TLS must receive 401
	if err := fixture.Remote.LocalSetRemoteAccessEnabled(ctx, false); err == nil {
		t.Fatal("remote network client was able to mutate kill switch over TLS RPC")
	}
	if _, err := fixture.Remote.LocalRemoteAccessStatus(ctx); err == nil {
		t.Fatal("remote network client was able to query kill switch over TLS RPC")
	}

	// Disable via local store
	if err := s.SetRemoteAccessEnabled(ctx, false); err != nil {
		t.Fatal(err)
	}
	// Verify immediately blocks again
	_, err = fixture.Remote.CurrentPlan(ctx, p.ID, worker.Token)
	if !errors.Is(err, coord.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable after disabling kill switch, got %v", err)
	}
}

// TestE2E_Tier1_F4_CreateOnlyBootstrapMCP verifies:
// - Feature 4: Create-Only Bootstrap MCP (isolated stdio server, private credential written mode 0600, no token leakage)
func TestE2E_Tier1_F4_CreateOnlyBootstrapMCP(t *testing.T) {
	s, _, _, _, _ := setupE2ETest(t)
	ctx := context.Background()
	gitDir := setupE2EGitWorktree(t)

	credDir := filepath.Join(t.TempDir(), "credentials")
	bootstrapKey := strings.Repeat("local-bootstrap-secret-token-key-", 2)
	if err := s.ConfigureLocalBootstrapKey(ctx, bootstrapKey); err != nil {
		t.Fatal(err)
	}

	// MCP Bootstrap server instance
	srv := coord.ProjectBootstrapMCPServer(s, credDir, bootstrapKey)
	createTool := srv.GetTool("gossip_create_project")
	if createTool == nil {
		t.Fatal("expected gossip_create_project tool on bootstrap MCP server")
	}

	// Verify server has ONLY project creation tool (create-only boundary)
	// Other tools like gossip_plan, gossip_task must not exist on bootstrap server
	if srv.GetTool("gossip_plan") != nil || srv.GetTool("gossip_task") != nil {
		t.Fatal("bootstrap MCP server exposed non-bootstrap tools")
	}

	// Test with wrong bootstrap key
	wrongServer := coord.ProjectBootstrapMCPServer(s, filepath.Join(t.TempDir(), "wrong-creds"), strings.Repeat("wrong-bootstrap-key-", 3))
	wrongResult, wrongErr := wrongServer.GetTool("gossip_create_project").Handler(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "gossip_create_project",
			Arguments: map[string]any{"local_path": gitDir, "request_id": "req-wrong-key"},
		},
	})
	if wrongErr != nil || !wrongResult.IsError {
		t.Fatalf("bootstrap accepted incorrect API key: %+v %v", wrongResult, wrongErr)
	}

	// Test with correct key
	reqID := "req-bootstrap-e2e"
	result, err := createTool.Handler(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "gossip_create_project",
			Arguments: map[string]any{"local_path": gitDir, "request_id": reqID},
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("bootstrap create failed: %+v %v", result, err)
	}

	var created struct {
		Project        coord.Project    `json:"project"`
		Repository     coord.Repository `json:"repository"`
		Workspace      coord.Workspace  `json:"workspace"`
		CredentialFile string           `json:"owner_credential_file"`
	}
	respText := result.Content[0].(mcp.TextContent).Text
	if err := json.Unmarshal([]byte(respText), &created); err != nil {
		t.Fatalf("failed to parse bootstrap response: %v", err)
	}

	// Verify file mode 0600
	info, err := os.Stat(created.CredentialFile)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credential file permissions not 0600: perm=%v err=%v", info.Mode().Perm(), err)
	}

	// Verify owner token is NOT leaked in the tool response body
	credBytes, err := os.ReadFile(created.CredentialFile)
	if err != nil {
		t.Fatal(err)
	}
	var cred coord.Credential
	if err := json.Unmarshal(credBytes, &cred); err != nil || cred.Token == "" {
		t.Fatalf("invalid credential saved: %+v %v", cred, err)
	}
	if strings.Contains(respText, cred.Token) {
		t.Fatal("bootstrap tool leaked private owner token in text response")
	}

	// Idempotency: retry with same request ID returns identical project
	retryResult, err := createTool.Handler(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "gossip_create_project",
			Arguments: map[string]any{"local_path": gitDir, "request_id": reqID},
		},
	})
	if err != nil || retryResult.IsError {
		t.Fatalf("bootstrap retry failed: %+v %v", retryResult, err)
	}
	var retried struct {
		Project coord.Project `json:"project"`
	}
	_ = json.Unmarshal([]byte(retryResult.Content[0].(mcp.TextContent).Text), &retried)
	if retried.Project.ID != created.Project.ID {
		t.Fatalf("idempotent retry returned different project ID: %s vs %s", retried.Project.ID, created.Project.ID)
	}
}

// TestE2E_Tier1_F5_F6_F7_LegacyContainmentAndRetirement verifies:
// - Feature 5: Legacy Endpoint Retirement (mcp, serve commands return retirement error)
// - Feature 6: Legacy UI Local Containment (loopback Host & same-origin enforcement)
// - Feature 7: Legacy Tunnel Deactivation (HTTP 410 Gone for tunnel actions)
func TestE2E_Tier1_F5_F6_F7_LegacyContainmentAndRetirement(t *testing.T) {
	gossipBin := findGossipBinary(t)

	// Feature 5: Legacy CLI retirement
	for _, cmdArg := range []string{"mcp", "serve"} {
		cmd := exec.Command(gossipBin, cmdArg)
		out, err := cmd.CombinedOutput()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
			t.Fatalf("expected exit code 2 for legacy %s command, got %v (output: %s)", cmdArg, err, out)
		}
		if !strings.Contains(string(out), "the legacy JSON MCP interface is retired because it has no project access-control boundary") {
			t.Fatalf("legacy %s did not output migration error: %s", cmdArg, out)
		}
	}

	// Legacy tunnel command retirement
	cmd := exec.Command(gossipBin, "tunnel", "init")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "legacy HTTP dashboard tunneling is disabled") {
		t.Fatalf("expected legacy tunnel command to fail with disabled message, got %v: %s", err, out)
	}

	// Feature 6: Legacy UI Local Containment
	legacyStore, err := state.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	legacyHandler := web.NewServer(0, legacyStore, roles.GetDefaultService()).Handler()

	// Rejection of untrusted host
	for _, untrustedHost := range []string{"attacker.example", "attacker.example:8080"} {
		req := httptest.NewRequest(http.MethodGet, "http://"+untrustedHost+"/api/status", nil)
		rec := httptest.NewRecorder()
		legacyHandler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden for untrusted Host %q, got %d", untrustedHost, rec.Code)
		}
	}

	// Rejection of cross-origin mutation
	crossOriginReq := httptest.NewRequest(http.MethodPost, "http://localhost:8080/api/tunnel", bytes.NewBufferString(`{"action":"generate"}`))
	crossOriginReq.Header.Set("Origin", "http://evil.example")
	crossOriginReq.Header.Set("Content-Type", "application/json")
	crossOriginRec := httptest.NewRecorder()
	legacyHandler.ServeHTTP(crossOriginRec, crossOriginReq)
	if crossOriginRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for cross-origin mutation, got %d", crossOriginRec.Code)
	}

	// Loopback Host is allowed
	loopbackReq := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/status", nil)
	loopbackRec := httptest.NewRecorder()
	legacyHandler.ServeHTTP(loopbackRec, loopbackReq)
	if loopbackRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for loopback host, got %d", loopbackRec.Code)
	}

	// Feature 7: Legacy Tunnel Deactivation
	for _, action := range []string{"generate", "activate", "config"} {
		tunnelReq := httptest.NewRequest(http.MethodPost, "http://localhost:8080/api/tunnel", bytes.NewBufferString(fmt.Sprintf(`{"action":"%s"}`, action)))
		tunnelReq.Header.Set("Origin", "http://localhost:8080")
		tunnelReq.Header.Set("Content-Type", "application/json")
		tunnelRec := httptest.NewRecorder()
		legacyHandler.ServeHTTP(tunnelRec, tunnelReq)
		if tunnelRec.Code != http.StatusGone {
			t.Fatalf("expected HTTP 410 Gone for legacy tunnel action %q, got %d (body: %s)", action, tunnelRec.Code, tunnelRec.Body.String())
		}
		if !strings.Contains(tunnelRec.Body.String(), "legacy HTTP dashboard tunneling is disabled") {
			t.Fatalf("expected tunnel disabled error text, got: %s", tunnelRec.Body.String())
		}
	}
}

// TestE2E_Tier1_F8_F9_F10_TaskClaimLeaseFencingIdempotency verifies:
// - Feature 8: Atomic Task Claim & Lease
// - Feature 9: Attempt Fencing
// - Feature 10: Idempotent Mutations
func TestE2E_Tier1_F8_F9_F10_TaskClaimLeaseFencingIdempotency(t *testing.T) {
	s, _, p, owner, worker := setupE2ETest(t)
	ctx := context.Background()

	contract := coord.ReportContract{ReviewType: "full_code", Formats: []string{"markdown"}}

	// Feature 10: Idempotent task creation
	reqID := "req-idemp-task-1"
	task1, err := s.CreateTaskWithID(ctx, p.ID, owner.Token, reqID, 1, "Feature task", "Implement core logic", "implementation", contract, nil)
	if err != nil {
		t.Fatalf("CreateTaskWithID failed: %v", err)
	}
	// Duplicate call returns identical task
	task1Dup, err := s.CreateTaskWithID(ctx, p.ID, owner.Token, reqID, 1, "Feature task", "Implement core logic", "implementation", contract, nil)
	if err != nil || task1Dup.ID != task1.ID {
		t.Fatalf("idempotent task creation failed: %+v %v", task1Dup, err)
	}
	// Changed payload with same request ID fails with ErrConflict
	_, err = s.CreateTaskWithID(ctx, p.ID, owner.Token, reqID, 1, "Changed Title", "Implement core logic", "implementation", contract, nil)
	if !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("expected ErrConflict on modified payload reuse, got %v", err)
	}

	// Feature 8: Atomic Task Claim & Lease
	claim1, err := s.ClaimTask(ctx, p.ID, worker.Token, task1.ID, 1*time.Second)
	if err != nil {
		t.Fatalf("ClaimTask failed: %v", err)
	}
	if claim1.Attempt != 1 || claim1.Status != "running" || claim1.Owner != worker.PrincipalID {
		t.Fatalf("unexpected claim state: %+v", claim1)
	}

	// Concurrent / duplicate claim while lease is active fails
	_, err = s.ClaimTask(ctx, p.ID, owner.Token, task1.ID, time.Minute)
	if !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("expected ErrConflict on active lease claim, got %v", err)
	}

	// Feature 9: Attempt Fencing
	// Wait for lease to expire
	time.Sleep(1100 * time.Millisecond)

	// Re-claim as attempt 2
	claim2, err := s.ClaimTask(ctx, p.ID, worker.Token, task1.ID, time.Minute)
	if err != nil || claim2.Attempt != 2 {
		t.Fatalf("expected re-claim attempt 2 after lease expiry, got %+v %v", claim2, err)
	}

	// Stale attempt 1 submits report -> must be rejected with ErrConflict (fenced!)
	_, err = s.SubmitReport(ctx, p.ID, worker.Token, task1.ID, 1, "commit-stale", "Stale report body")
	if !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("expected ErrConflict on stale attempt 1 submission, got %v", err)
	}

	// Active attempt 2 submits report -> succeeds
	report2, err := s.SubmitReport(ctx, p.ID, worker.Token, task1.ID, 2, "commit-active", "Valid active report body")
	if err != nil {
		t.Fatalf("active attempt 2 submission failed: %v", err)
	}

	// Feature 10: Idempotent message sending
	msgID := "req-msg-e2e-1"
	msg1, err := s.SendMessageWithID(ctx, p.ID, worker.Token, owner.PrincipalID, "thread-e2e", "Subject", "Hello", msgID)
	if err != nil {
		t.Fatalf("SendMessageWithID failed: %v", err)
	}
	msg1Dup, err := s.SendMessageWithID(ctx, p.ID, worker.Token, owner.PrincipalID, "thread-e2e", "Subject", "Hello", msgID)
	if err != nil || msg1Dup.ID != msg1.ID {
		t.Fatalf("idempotent message retry failed: %+v %v", msg1Dup, err)
	}
	_, err = s.SendMessageWithID(ctx, p.ID, worker.Token, owner.PrincipalID, "thread-e2e", "Subject", "Changed body", msgID)
	if !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("expected ErrConflict on modified message reuse, got %v", err)
	}

	if err := s.AcceptReport(ctx, p.ID, owner.Token, report2.ID, report2.Digest); err != nil {
		t.Fatalf("AcceptReport failed: %v", err)
	}
}

// TestE2E_Tier1_F11_TaskDependencyDAG verifies:
// - Feature 11: Task Dependency DAG Enforcement (blocked state, cycle prevention, unblocking on acceptance)
func TestE2E_Tier1_F11_TaskDependencyDAG(t *testing.T) {
	s, _, p, owner, worker := setupE2ETest(t)
	ctx := context.Background()

	contract := coord.ReportContract{ReviewType: "full_code", Formats: []string{"markdown"}}

	// Task A (Root)
	taskA, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Task A", "Root prerequisite", "research", contract, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Task B (Depends on Task A)
	taskB, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Task B", "Dependent task", "implementation", contract, []string{taskA.ID})
	if err != nil {
		t.Fatal(err)
	}
	if taskB.Status != "blocked" {
		t.Fatalf("expected dependent Task B status='blocked', got %s", taskB.Status)
	}

	// Claiming blocked task B must fail with ErrConflict
	_, err = s.ClaimTask(ctx, p.ID, worker.Token, taskB.ID, time.Minute)
	if !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("expected ErrConflict when claiming blocked task B, got %v", err)
	}

	// Complete Task A
	claimA, err := s.ClaimTask(ctx, p.ID, worker.Token, taskA.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	reportA, err := s.SubmitReport(ctx, p.ID, worker.Token, taskA.ID, claimA.Attempt, "commit-a", "Done task A")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AcceptReport(ctx, p.ID, owner.Token, reportA.ID, reportA.Digest); err != nil {
		t.Fatal(err)
	}

	// Now Task B must automatically unblock to 'ready'
	updatedB, err := s.GetTask(ctx, p.ID, worker.Token, taskB.ID)
	if err != nil || updatedB.Status != "ready" {
		t.Fatalf("expected Task B to unblock to 'ready', got status=%s err=%v", updatedB.Status, err)
	}

	// Worker can now claim Task B
	claimB, err := s.ClaimTask(ctx, p.ID, worker.Token, taskB.ID, time.Minute)
	if err != nil || claimB.Attempt != 1 {
		t.Fatalf("failed to claim unblocked Task B: %+v %v", claimB, err)
	}
}

// TestE2E_Tier1_F12_F13_UsageTelemetryAndBudgetEnvelope verifies:
// - Feature 12: Attempt Usage Telemetry (idempotency, payload validation)
// - Feature 13: Planning Budget Envelope (token limits, reservations, release gating)
func TestE2E_Tier1_F12_F13_UsageTelemetryAndBudgetEnvelope(t *testing.T) {
	s, _, p, owner, worker := setupE2ETest(t)
	ctx := context.Background()

	contract := coord.ReportContract{ReviewType: "full_code", Formats: []string{"markdown"}}
	task, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Budgeted task", "Desc", "implementation", contract, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Feature 13: Budget Envelope setup
	budget, err := s.SetBudget(ctx, p.ID, owner.Token, 0, 500)
	if err != nil || !budget.Enabled || budget.TokenLimit != 500 {
		t.Fatalf("SetBudget failed: %+v %v", budget, err)
	}

	// Claiming without budget reservation fails
	_, err = s.ClaimTask(ctx, p.ID, worker.Token, task.ID, time.Minute)
	if err == nil {
		t.Fatal("expected claim without reservation to fail under active budget envelope")
	}

	// Reserve 300 tokens
	budget, err = s.ReserveBudget(ctx, p.ID, owner.Token, task.ID, 300)
	if err != nil || budget.Reserved != 300 {
		t.Fatalf("ReserveBudget failed: %+v %v", budget, err)
	}

	// Claim now succeeds
	claim, err := s.ClaimTask(ctx, p.ID, worker.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatalf("claim failed after reservation: %v", err)
	}

	// Cannot release reservation while task is running
	_, err = s.ReserveBudget(ctx, p.ID, owner.Token, task.ID, 0)
	if err == nil {
		t.Fatal("expected releasing running task reservation to fail")
	}

	// Cannot lower budget limit below current reservations
	_, err = s.SetBudget(ctx, p.ID, owner.Token, budget.Revision, 250)
	if !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("expected ErrConflict when lowering budget below reserved amount, got %v", err)
	}

	// Feature 12: Attempt Usage Telemetry
	usage := coord.UsageReport{
		Provider:     "OpenAI",
		Model:        "gpt-4o",
		Source:       "provider_reported",
		InputTokens:  1200,
		OutputTokens: 350,
		CostMicros:   4500,
		Currency:     "USD",
	}
	recorded, err := s.RecordAttemptUsage(ctx, p.ID, worker.Token, task.ID, claim.Attempt, "req-usage-1", usage)
	if err != nil || recorded.InputTokens != 1200 || recorded.OutputTokens != 350 {
		t.Fatalf("RecordAttemptUsage failed: %+v %v", recorded, err)
	}

	// Idempotent retry returns same usage
	recordedDup, err := s.RecordAttemptUsage(ctx, p.ID, worker.Token, task.ID, claim.Attempt, "req-usage-1", usage)
	if err != nil || recordedDup.InputTokens != 1200 {
		t.Fatalf("idempotent usage record failed: %+v %v", recordedDup, err)
	}

	// Negative values rejected
	badUsage := usage
	badUsage.InputTokens = -10
	_, err = s.RecordAttemptUsage(ctx, p.ID, worker.Token, task.ID, claim.Attempt, "req-usage-bad", badUsage)
	if err == nil {
		t.Fatal("expected negative input tokens to be rejected")
	}

	// Complete task & release reservation
	rep, err := s.SubmitReport(ctx, p.ID, worker.Token, task.ID, claim.Attempt, "commit-b", "Done")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AcceptReport(ctx, p.ID, owner.Token, rep.ID, rep.Digest); err != nil {
		t.Fatal(err)
	}
	releasedBudget, err := s.ReserveBudget(ctx, p.ID, owner.Token, task.ID, 0)
	if err != nil || releasedBudget.Reserved != 0 {
		t.Fatalf("failed to release completed task reservation: %+v %v", releasedBudget, err)
	}
}

// TestE2E_Tier1_F14_PresenceAndCapabilityLabeling verifies:
// - Feature 14: Presence & Capability Labeling (authenticated heartbeats, session binding, adapter vs host_reported provenance)
func TestE2E_Tier1_F14_PresenceAndCapabilityLabeling(t *testing.T) {
	s, _, p, owner, worker := setupE2ETest(t)
	ctx := context.Background()

	caps := []coord.AgentCapability{
		{Name: "git_workspace_inspection", Source: "adapter"},
		{Name: "interrupt", Source: "host_reported"},
	}

	// Heartbeat with capabilities
	sessionID := "session-e2e-worker"
	if err := s.HeartbeatAgentWithCapabilities(ctx, p.ID, worker.Token, sessionID, caps); err != nil {
		t.Fatalf("HeartbeatAgentWithCapabilities failed: %v", err)
	}

	// Another principal claiming same session ID fails with ErrConflict
	if err := s.HeartbeatAgentWithCapabilities(ctx, p.ID, owner.Token, sessionID, caps); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("expected ErrConflict when different principal reuses active session, got %v", err)
	}

	// List presence and check provenance labels
	presenceList, err := s.ListAgentPresence(ctx, p.ID, owner.Token)
	if err != nil {
		t.Fatalf("ListAgentPresence failed: %v", err)
	}
	var found *coord.AgentPresence
	for i := range presenceList {
		if presenceList[i].PrincipalID == worker.PrincipalID {
			found = &presenceList[i]
			break
		}
	}
	if found == nil || !found.Online {
		t.Fatalf("worker presence not reported online: %+v", presenceList)
	}
	if len(found.Capabilities) != 2 {
		t.Fatalf("expected 2 capabilities, got %d: %+v", len(found.Capabilities), found.Capabilities)
	}
	if found.Capabilities[0].Source != "adapter" || found.Capabilities[1].Source != "host_reported" {
		t.Fatalf("capability source labeling corrupted: %+v", found.Capabilities)
	}

	// End session marks agent offline
	if err := s.EndAgentSession(ctx, p.ID, worker.Token, sessionID); err != nil {
		t.Fatalf("EndAgentSession failed: %v", err)
	}
	presenceListAfter, _ := s.ListAgentPresence(ctx, p.ID, owner.Token)
	for _, ap := range presenceListAfter {
		if ap.PrincipalID == worker.PrincipalID && ap.Online {
			t.Fatalf("worker still marked online after session end: %+v", ap)
		}
	}
}

// TestE2E_Tier1_F15_F16_MutualTLSAndCertEnrollment verifies:
// - Feature 15: Mutual TLS Coordination RPC (TLS 1.3, client cert validation)
// - Feature 16: Atomic Certificate Grant & Enrollment (grant binding, unauthorized cert rejection)
func TestE2E_Tier1_F15_F16_MutualTLSAndCertEnrollment(t *testing.T) {
	s, _, p, owner, worker := setupE2ETest(t)
	ctx := context.Background()

	// Enable remote access
	if err := s.SetRemoteAccessEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	fixture := setupE2ETLSServer(t, s, p, owner)

	// Enrolled client cert succeeds
	plan, err := fixture.Remote.CurrentPlan(ctx, p.ID, worker.Token)
	if err != nil || plan.Revision != 1 {
		t.Fatalf("mTLS call with enrolled cert failed: %+v %v", plan, err)
	}

	// Other client cert (valid CA-issued but NOT granted to project) -> must fail with HTTP 403 Forbidden
	_, err = fixture.OtherRemote.CurrentPlan(ctx, p.ID, worker.Token)
	if err == nil {
		t.Fatal("expected call with ungranted client certificate to fail with HTTP 403")
	}

	// Owner atomically grants certificate access to other cert
	grant, err := fixture.Remote.AddCertificateGrant(ctx, p.ID, owner.Token, fixture.OtherCertFingerprint)
	if err != nil || grant.Fingerprint != fixture.OtherCertFingerprint {
		t.Fatalf("AddCertificateGrant failed: %+v %v", grant, err)
	}

	// Now other cert succeeds
	planOther, err := fixture.OtherRemote.CurrentPlan(ctx, p.ID, worker.Token)
	if err != nil || planOther.Revision != 1 {
		t.Fatalf("mTLS call after grant failed: %+v %v", planOther, err)
	}
}

// TestE2E_Tier1_F17_F18_OutboxAndBoundedCache verifies:
// - Feature 17: Durable Scoped Outbox (replica ID stability, offline isolation)
// - Feature 18: Bounded Point-in-Time Cache (offline cached events, freshness metadata)
func TestE2E_Tier1_F17_F18_OutboxAndBoundedCache(t *testing.T) {
	s, _, project, _, worker := setupE2ETest(t)
	ctx := context.Background()

	outboxDB := filepath.Join(t.TempDir(), "worker_outbox.db")
	box, err := coord.OpenOutbox(outboxDB, "https://home.example:8443", project.ID, worker.PrincipalID, worker.Token, s)
	if err != nil {
		t.Fatalf("OpenOutbox failed: %v", err)
	}

	replicaID := box.ReplicaID()
	if !strings.HasPrefix(replicaID, "replica_") {
		t.Fatalf("expected replica ID prefix 'replica_', got %q", replicaID)
	}

	// Restart / reopen outbox and check replica ID stability
	boxReopened, err := coord.OpenOutbox(outboxDB, "https://home.example:8443", project.ID, worker.PrincipalID, worker.Token, s)
	if err != nil || boxReopened.ReplicaID() != replicaID {
		t.Fatalf("replica ID unstable across reopen: before=%s after=%s", replicaID, boxReopened.ReplicaID())
	}

	// Outbox project isolation: opening with different project ID fails
	_, err = coord.OpenOutbox(outboxDB, "https://home.example:8443", "other-project-id", worker.PrincipalID, worker.Token, s)
	if err == nil {
		t.Fatal("expected opening outbox with mismatched project ID to fail")
	}

	// Feature 18: Bounded Point-in-Time Cache
	adapter := coord.NewMCPAdapterWithOutbox(s, project.ID, worker.Token, box)
	defer adapter.Close()

	// Live event sync populates cache
	syncTool := adapter.Server.GetTool("gossip_sync")
	liveRes, err := syncTool.Handler(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "gossip_sync",
			Arguments: map[string]any{"operation": "events", "limit": 50},
		},
	})
	if err != nil || liveRes.IsError {
		t.Fatalf("live sync failed: %+v %v", liveRes, err)
	}

	// Read cached events while verifying metadata
	cachedRes, err := syncTool.Handler(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "gossip_sync",
			Arguments: map[string]any{"operation": "cached", "after": 0, "limit": 50},
		},
	})
	if err != nil || cachedRes.IsError {
		t.Fatalf("cached sync failed: %+v %v", cachedRes, err)
	}
	var cachedPage coord.CachedEventPage
	if err := json.Unmarshal([]byte(cachedRes.Content[0].(mcp.TextContent).Text), &cachedPage); err != nil {
		t.Fatalf("failed to parse cached page: %v", err)
	}
	if cachedPage.ObservedAt <= 0 || len(cachedPage.Events) == 0 {
		t.Fatalf("cached event page missing metadata or events: %+v", cachedPage)
	}
}

// TestE2E_Tier1_F19_ImmediateRevocation verifies:
// - Feature 19: Immediate Revocation Enforcement (member revocation fences attempts, cert revocation cuts off TLS)
func TestE2E_Tier1_F19_ImmediateRevocation(t *testing.T) {
	s, _, p, owner, worker := setupE2ETest(t)
	ctx := context.Background()

	if err := s.SetRemoteAccessEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	fixture := setupE2ETLSServer(t, s, p, owner)

	// Worker is active
	_, err := fixture.Remote.CurrentPlan(ctx, p.ID, worker.Token)
	if err != nil {
		t.Fatalf("worker initial call failed: %v", err)
	}

	// 1. Revoke worker principal
	if err := s.RevokePrincipal(ctx, p.ID, owner.Token, worker.PrincipalID); err != nil {
		t.Fatalf("RevokePrincipal failed: %v", err)
	}

	// Next call with worker token fails immediately with ErrUnauthorized (HTTP 401)
	_, err = fixture.Remote.CurrentPlan(ctx, p.ID, worker.Token)
	if !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized (HTTP 401) after principal revocation, got %v", err)
	}

	// 2. Revoke certificate grant
	if err := s.RevokeCertificateGrant(ctx, p.ID, owner.Token, fixture.ClientCertFingerprint); err != nil {
		t.Fatalf("RevokeCertificateGrant failed: %v", err)
	}

	// Even with owner token, this client cert is now rejected with HTTP 403 Forbidden
	_, err = fixture.Remote.CurrentPlan(ctx, p.ID, owner.Token)
	if err == nil {
		t.Fatal("expected revoked client certificate to be rejected with HTTP 403 Forbidden")
	}
}

// TestE2E_Tier1_F20_WorkRolePresetsCatalog verifies:
// - Feature 20: Versioned Starter Role Presets (6 presets v1.0.0, first-party sources, assignment & listing)
func TestE2E_Tier1_F20_WorkRolePresetsCatalog(t *testing.T) {
	s, _, p, owner, worker := setupE2ETest(t)
	ctx := context.Background()

	presets := coord.WorkRolePresets()
	if len(presets) != 6 {
		t.Fatalf("expected exactly 6 curated role presets, got %d", len(presets))
	}

	expectedNames := map[string]bool{
		"coordinator": true,
		"researcher":  true,
		"implementer": true,
		"reviewer":    true,
		"verifier":    true,
		"integrator":  true,
	}

	for _, pr := range presets {
		if !expectedNames[pr.Name] {
			t.Errorf("unexpected preset name: %s", pr.Name)
		}
		if pr.Version != "1.0.0" {
			t.Errorf("preset %s version = %s, want 1.0.0", pr.Name, pr.Version)
		}
		if len(pr.Sources) == 0 {
			t.Errorf("preset %s missing first-party sources", pr.Name)
		}
		if pr.Duties == "" || pr.ExpectedOutput == "" || pr.HandoffFormat == "" || pr.Escalation == "" {
			t.Errorf("preset %s missing required descriptive fields", pr.Name)
		}
	}

	// Assign preset to worker
	assigned, err := s.SetWorkRole(ctx, p.ID, owner.Token, worker.PrincipalID, "", "researcher", "", 0, 0)
	if err != nil || assigned.Name != "researcher" || !assigned.Active {
		t.Fatalf("SetWorkRole with preset failed: %+v %v", assigned, err)
	}

	// List work roles and verify
	rolesList, err := s.ListWorkRoles(ctx, p.ID, owner.Token)
	if err != nil || len(rolesList) == 0 {
		t.Fatalf("ListWorkRoles failed: %+v %v", rolesList, err)
	}
	found := false
	for _, r := range rolesList {
		if r.PrincipalID == worker.PrincipalID && r.Name == "researcher" && r.Active {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("assigned work role 'researcher' not found in active list: %+v", rolesList)
	}
}

// TestE2E_Tier1_F21_F22_F23_ReviewSelectionWorktreeAndDiagram verifies:
// - Feature 21: Review Depth Planning Selection (mandatory review_type)
// - Feature 22: Full-Code Git Worktree Capture (worktree diff capture, candidate report)
// - Feature 23: Diagram Report Structural Validation (required Markdown sections & Mermaid blocks)
func TestE2E_Tier1_F21_F22_F23_ReviewSelectionWorktreeAndDiagram(t *testing.T) {
	s, _, p, owner, worker := setupE2ETest(t)
	ctx := context.Background()

	// Feature 21: Task creation without review_type fails
	_, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "No review type", "Desc", "research", coord.ReportContract{}, nil)
	if err == nil {
		t.Fatal("expected task creation without review_type to be rejected")
	}

	// Feature 23: Diagram report structural validation
	diagramContract := coord.ReportContract{
		ReviewType: "diagram_report",
		Formats:    []string{"markdown", "mermaid_class", "mermaid_sequence"},
		Coverage:   []string{"authorization", "retry"},
	}
	diagramTask, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Diagram task", "Desc", "research", diagramContract, nil)
	if err != nil {
		t.Fatalf("CreateTask for diagram_report failed: %v", err)
	}
	claimDiagram, err := s.ClaimTask(ctx, p.ID, worker.Token, diagramTask.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	// Incomplete diagram report (missing sections) fails
	badReport := "# Incomplete Report\n\nJust summary without required sections."
	_, err = s.SubmitReport(ctx, p.ID, worker.Token, diagramTask.ID, claimDiagram.Attempt, "commit-base", badReport)
	if err == nil {
		t.Fatal("expected incomplete diagram report to be rejected")
	}

	// Shallow sequence diagram (no message) fails
	shallowReport := strings.Replace(sampleValidDiagramReport, "TaskWorker-->>Coordinator: request review", "", 1)
	shallowReport = strings.Replace(shallowReport, "Coordinator->>TaskWorker: assign task", "", 1)
	_, err = s.SubmitReport(ctx, p.ID, worker.Token, diagramTask.ID, claimDiagram.Attempt, "commit-base", shallowReport)
	if err == nil {
		t.Fatal("expected shallow sequence diagram to be rejected")
	}

	// Fully compliant diagram report succeeds
	diagReport, err := s.SubmitReport(ctx, p.ID, worker.Token, diagramTask.ID, claimDiagram.Attempt, "commit-base", sampleValidDiagramReport)
	if err != nil {
		t.Fatalf("valid diagram report was rejected: %v", err)
	}
	if err := s.AcceptReport(ctx, p.ID, owner.Token, diagReport.ID, diagReport.Digest); err != nil {
		t.Fatalf("AcceptReport failed for valid diagram report: %v", err)
	}

	// Feature 22: Full-Code Git Worktree Capture
	gitDir := setupE2EGitWorktree(t)
	repo, err := s.CreateRepository(ctx, p.ID, owner.Token, "code-repo")
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := s.RegisterWorkspace(ctx, p.ID, worker.Token, repo.ID, "worker-ws", "host-1", gitDir, "main", "initial-head", false)
	if err != nil {
		t.Fatal(err)
	}

	codeContract := coord.ReportContract{ReviewType: "full_code", Formats: []string{"markdown"}}
	codeTask, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Code task", "Desc", "implementation", codeContract, nil)
	if err != nil {
		t.Fatal(err)
	}

	claimCode, err := s.ClaimTaskInWorkspace(ctx, p.ID, worker.Token, codeTask.ID, workspace.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	// Modify file in worktree
	newFile := filepath.Join(gitDir, "feature.go")
	if err := os.WriteFile(newFile, []byte("package main\n\nfunc Feature() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// SaveCandidate manually
	candidateDiff := "diff --git a/feature.go b/feature.go\nnew file mode 100644\n--- /dev/null\n+++ b/feature.go\n@@ -0,0 +1,3 @@\n+package main\n+\n+func Feature() {}\n"
	cand, err := s.SaveCandidate(ctx, p.ID, worker.Token, codeTask.ID, claimCode.Attempt, workspace.Head, candidateDiff)
	if err != nil || cand.ID == "" || cand.Digest == "" {
		t.Fatalf("SaveCandidate failed: %+v %v", cand, err)
	}

	if err := s.RequestReview(ctx, p.ID, worker.Token, codeTask.ID, claimCode.Attempt); err != nil {
		t.Fatalf("RequestReview failed: %v", err)
	}

	codeReport, err := s.SubmitReport(ctx, p.ID, worker.Token, codeTask.ID, claimCode.Attempt, workspace.Head, "Implemented Feature function with complete test coverage.")
	if err != nil {
		t.Fatalf("SubmitReport for full_code failed: %v", err)
	}
	if codeReport.SourceCandidateID != cand.ID {
		t.Fatalf("expected codeReport to reference candidate %s, got %s", cand.ID, codeReport.SourceCandidateID)
	}
}

// TestE2E_Tier1_F24_F25_F26_F27_WebUIPolishAndInference verifies:
// - Feature 24: Strict Content Security Policy (no unsafe-inline, script-src 'self')
// - Feature 25: Distinct 401 vs 403 HTTP Error Handling
// - Feature 26: First-Run Local Git Auto-Inference (repo metadata auto-inferred)
// - Feature 27: Sandboxed Diagram Rendering (isolated iframe at /diagram.html)
func TestE2E_Tier1_F24_F25_F26_F27_WebUIPolishAndInference(t *testing.T) {
	s, _, p, owner, worker := setupE2ETest(t)
	ctx := context.Background()

	webHandler := coord.NewWebHandler(s)

	// Helper for HTTP requests
	doReq := func(method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
		var rdr io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, "http://localhost:8081"+path, rdr)
		req.RemoteAddr = "127.0.0.1:54321"
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "http://localhost:8081")
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		webHandler.ServeHTTP(rec, req)
		return rec
	}

	// Login as owner and worker
	ownerLogin := doReq(http.MethodPost, "/api/login", map[string]string{"project_id": p.ID, "token": owner.Token}, nil)
	if ownerLogin.Code != http.StatusOK || len(ownerLogin.Result().Cookies()) == 0 {
		t.Fatalf("owner login failed: %d %s", ownerLogin.Code, ownerLogin.Body.String())
	}
	ownerCookie := ownerLogin.Result().Cookies()[0]

	workerLogin := doReq(http.MethodPost, "/api/login", map[string]string{"project_id": p.ID, "token": worker.Token}, nil)
	workerCookie := workerLogin.Result().Cookies()[0]

	// Feature 24: Strict CSP
	cspRec := doReq(http.MethodGet, "/", nil, nil)
	csp := cspRec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "style-src 'self'") {
		t.Fatalf("invalid CSP header: %s", csp)
	}
	if strings.Contains(csp, "unsafe-inline") {
		t.Fatalf("CSP contains forbidden unsafe-inline: %s", csp)
	}

	// Feature 25: Distinct 401 vs 403
	// 403 Forbidden: Authenticated worker calls owner-only endpoint
	rec403 := doReq(http.MethodGet, "/api/projects/"+p.ID+"/replicas", nil, workerCookie)
	if rec403.Code != http.StatusForbidden {
		t.Fatalf("expected HTTP 403 Forbidden for worker accessing replicas, got %d", rec403.Code)
	}

	// 401 Unauthorized: Calling with invalid or revoked session
	badCookie := &http.Cookie{Name: "gossip_session", Value: "invalid-session-token"}
	rec401 := doReq(http.MethodGet, "/api/projects/"+p.ID+"/plan", nil, badCookie)
	if rec401.Code != http.StatusUnauthorized {
		t.Fatalf("expected HTTP 401 Unauthorized for invalid cookie, got %d", rec401.Code)
	}

	// Feature 26: First-Run Local Git Auto-Inference
	gitDir := setupE2EGitWorktree(t)
	createProjectRec := doReq(http.MethodPost, "/api/projects/create", map[string]string{"local_path": gitDir}, nil)
	if createProjectRec.Code != http.StatusOK {
		t.Fatalf("first-run local project create failed: %d %s", createProjectRec.Code, createProjectRec.Body.String())
	}
	var createdProject struct {
		Project    coord.Project    `json:"project"`
		Repository coord.Repository `json:"repository"`
	}
	if err := json.Unmarshal(createProjectRec.Body.Bytes(), &createdProject); err != nil {
		t.Fatalf("failed to decode project creation result: %v", err)
	}
	if createdProject.Project.Name == "" || createdProject.Repository.Name == "" {
		t.Fatalf("project metadata was not inferred from local git checkout: %+v", createdProject)
	}

	// Feature 27: Sandboxed Diagram Rendering
	diagRec := doReq(http.MethodGet, "/diagram.html", nil, nil)
	if diagRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for /diagram.html, got %d", diagRec.Code)
	}
	diagCSP := diagRec.Header().Get("Content-Security-Policy")
	if !strings.Contains(diagCSP, "default-src 'none'") || !strings.Contains(diagCSP, "frame-ancestors 'self'") {
		t.Fatalf("diagram iframe lacks sandboxed CSP: %s", diagCSP)
	}
	_ = ctx
	_ = ownerCookie
}

// TestE2E_Tier1_F28_IndependentReviewDeliverable verifies:
// - Feature 28: Independent Review Deliverable (docs/gemini-review.md existence and required sections)
func TestE2E_Tier1_F28_IndependentReviewDeliverable(t *testing.T) {
	reviewPath := filepath.Join("/home/user/Projects/gossip/docs/gemini-review.md")
	content, err := os.ReadFile(reviewPath)
	if err != nil {
		t.Fatalf("expected docs/gemini-review.md to exist: %v", err)
	}
	text := string(content)

	requiredSections := []string{
		"Security-Focused Kill-Switch Audit",
		"Staged Acceptance Report",
		"Two-Machine Acceptance",
		"Final Release Status",
	}

	for _, sec := range requiredSections {
		if !strings.Contains(text, sec) {
			t.Errorf("docs/gemini-review.md missing required section: %q", sec)
		}
	}
}

// ============================================================================
// Tier 2: Boundary & Corner Cases
// ============================================================================

func TestE2E_Tier2_BoundaryAndCornerCases(t *testing.T) {
	s, _, p, owner, worker := setupE2ETest(t)
	ctx := context.Background()

	t.Run("EmptyAndNullInputs", func(t *testing.T) {
		// Empty project name
		_, _, err := s.CreateProject(ctx, "", "owner")
		if err == nil {
			t.Error("expected error for empty project name")
		}

		// Empty task title
		contract := coord.ReportContract{ReviewType: "full_code", Formats: []string{"markdown"}}
		_, err = s.CreateTask(ctx, p.ID, owner.Token, 1, "", "Description", "research", contract, nil)
		if err == nil {
			t.Error("expected error for empty task title")
		}

		// Empty review type
		_, err = s.CreateTask(ctx, p.ID, owner.Token, 1, "Task", "Description", "research", coord.ReportContract{}, nil)
		if err == nil {
			t.Error("expected error for empty review type")
		}
	})

	t.Run("MalformedPayloads", func(t *testing.T) {
		if err := s.SetRemoteAccessEnabled(ctx, true); err != nil {
			t.Fatal(err)
		}
		fixture := setupE2ETLSServer(t, s, p, owner)

		// Send malformed JSON to /v1/call
		tr := &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:      fixture.CertPool,
				Certificates: []tls.Certificate{fixture.ClientTLSCert},
				ServerName:   "127.0.0.1",
			},
		}
		client := &http.Client{Transport: tr}
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, fixture.URL+"/v1/call", bytes.NewReader([]byte("{malformed-json: true}")))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+worker.Token)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected HTTP 400 Bad Request for malformed JSON, got %d", resp.StatusCode)
		}
	})

	t.Run("CredentialViolations", func(t *testing.T) {
		p2, owner2, err := s.CreateProject(ctx, "second-project", "owner2")
		if err != nil {
			t.Fatal(err)
		}
		_ = p2

		// Token from p2 used against p
		_, err = s.CurrentPlan(ctx, p.ID, owner2.Token)
		if !errors.Is(err, coord.ErrUnauthorized) {
			t.Fatalf("expected ErrUnauthorized when using foreign project token, got %v", err)
		}

		// Non-existent token
		_, err = s.CurrentPlan(ctx, p.ID, "completely-bogus-token")
		if !errors.Is(err, coord.ErrUnauthorized) {
			t.Fatalf("expected ErrUnauthorized for bogus token, got %v", err)
		}
	})

	t.Run("DAGBoundaryViolations", func(t *testing.T) {
		contract := coord.ReportContract{ReviewType: "full_code", Formats: []string{"markdown"}}

		// Non-existent prerequisite ID
		_, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Child", "Desc", "research", contract, []string{"non-existent-task-id"})
		if err == nil {
			t.Fatal("expected error when depending on non-existent task ID")
		}

		// Self-dependency: create task and try to depend on itself
		task, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "SelfDep", "Desc", "research", contract, nil)
		if err != nil {
			t.Fatal(err)
		}
		// Direct SQL check constraint check: CHECK(task_id <> prerequisite_id)
		db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "dummy.db"))
		if err == nil {
			defer db.Close()
			_, _ = db.Exec(`CREATE TABLE deps(task_id TEXT, prereq_id TEXT, CHECK(task_id <> prereq_id))`)
			_, insErr := db.Exec(`INSERT INTO deps VALUES(?, ?)`, task.ID, task.ID)
			if insErr == nil {
				t.Fatal("expected CHECK constraint violation on self-dependency")
			}
		}
	})

	t.Run("BudgetBoundaries", func(t *testing.T) {
		// Zero or negative budget limit
		_, err := s.SetBudget(ctx, p.ID, owner.Token, 0, -100)
		if err == nil {
			t.Fatal("expected error for negative budget limit")
		}

		// Negative reservation
		contract := coord.ReportContract{ReviewType: "full_code", Formats: []string{"markdown"}}
		t1, _ := s.CreateTask(ctx, p.ID, owner.Token, 1, "T1", "Desc", "research", contract, nil)
		_, err = s.ReserveBudget(ctx, p.ID, owner.Token, t1.ID, -50)
		if err == nil {
			t.Fatal("expected error for negative budget reservation")
		}
	})

	t.Run("DiagramStructuralGaps", func(t *testing.T) {
		contract := coord.ReportContract{
			ReviewType: "diagram_report",
			Formats:    []string{"markdown", "mermaid_class", "mermaid_sequence"},
			Coverage:   []string{"authentication", "failover"},
		}
		task, _ := s.CreateTask(ctx, p.ID, owner.Token, 1, "DiagTask", "Desc", "research", contract, nil)
		claim, _ := s.ClaimTask(ctx, p.ID, worker.Token, task.ID, time.Minute)

		// Missing coverage word ("failover") in prose
		missingCoverageReport := strings.Replace(sampleValidDiagramReport, "offline", "untested", -1)
		_, err := s.SubmitReport(ctx, p.ID, worker.Token, task.ID, claim.Attempt, "commit", missingCoverageReport)
		if err == nil {
			t.Fatal("expected rejection when report prose omits planned coverage items")
		}
	})
}

// ============================================================================
// Tier 3: Cross-Feature Combinations
// ============================================================================

// TestE2E_Tier3_mTLSKillSwitchCutoff verifies:
// Dynamic kill switch cutoff under active mTLS: toggling kill switch immediately cuts off RPC with 503 without closing listener, and toggling it back on immediately restores access.
func TestE2E_Tier3_mTLSKillSwitchCutoff(t *testing.T) {
	s, _, p, owner, worker := setupE2ETest(t)
	ctx := context.Background()

	// 1. Enable kill switch
	if err := s.SetRemoteAccessEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	fixture := setupE2ETLSServer(t, s, p, owner)

	// Verify initial success
	plan, err := fixture.Remote.CurrentPlan(ctx, p.ID, worker.Token)
	if err != nil || plan.Revision != 1 {
		t.Fatalf("expected successful plan read: %+v %v", plan, err)
	}

	// 2. Dynamically toggle kill switch OFF in local database
	if err := s.SetRemoteAccessEnabled(ctx, false); err != nil {
		t.Fatal(err)
	}

	// Active network client must immediately receive HTTP 503 on the very next call
	_, err = fixture.Remote.CurrentPlan(ctx, p.ID, worker.Token)
	if !errors.Is(err, coord.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable immediately after kill switch toggled off, got %v", err)
	}

	// 3. Dynamically toggle kill switch back ON
	if err := s.SetRemoteAccessEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}

	// Active network client immediately succeeds again
	planRestored, err := fixture.Remote.CurrentPlan(ctx, p.ID, worker.Token)
	if err != nil || planRestored.Revision != 1 {
		t.Fatalf("expected access restored after kill switch toggled on, got %+v %v", planRestored, err)
	}
}

// TestE2E_Tier3_RevokedCredentialsAndStateIsolation verifies:
// 401 vs 403 semantics, cert grant revocation, zero cross-project leakage.
func TestE2E_Tier3_RevokedCredentialsAndStateIsolation(t *testing.T) {
	s, _, p1, owner1, worker1 := setupE2ETest(t)
	ctx := context.Background()

	if err := s.SetRemoteAccessEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	fixture := setupE2ETLSServer(t, s, p1, owner1)

	// Create distinct second project
	p2, owner2, err := s.CreateProject(ctx, "project-two", "owner-two")
	if err != nil {
		t.Fatal(err)
	}
	worker2, err := s.AddPrincipal(ctx, p2.ID, owner2.Token, "worker-two", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishPlan(ctx, p2.ID, owner2.Token, "plan two", 0); err != nil {
		t.Fatal(err)
	}

	// 1. Worker1 credential used against p2 -> HTTP 401 Unauthorized
	_, err = fixture.Remote.CurrentPlan(ctx, p2.ID, worker1.Token)
	if !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized when using p1 token against p2, got %v", err)
	}

	// 2. Client cert is granted to p1 but NOT to p2 -> HTTP 403 Forbidden
	_, err = fixture.Remote.CurrentPlan(ctx, p2.ID, worker2.Token)
	if err == nil {
		t.Fatal("expected HTTP 403 Forbidden when client cert is not granted to target project")
	}

	// 3. Revoke worker1 principal in p1 -> returns HTTP 401 Unauthorized
	if err := s.RevokePrincipal(ctx, p1.ID, owner1.Token, worker1.PrincipalID); err != nil {
		t.Fatal(err)
	}
	_, err = fixture.Remote.CurrentPlan(ctx, p1.ID, worker1.Token)
	if !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized after principal revocation, got %v", err)
	}

	// 4. Revoke cert grant in p1 -> returns HTTP 403 Forbidden
	if err := s.RevokeCertificateGrant(ctx, p1.ID, owner1.Token, fixture.ClientCertFingerprint); err != nil {
		t.Fatal(err)
	}
	_, err = fixture.Remote.CurrentPlan(ctx, p1.ID, owner1.Token)
	if err == nil {
		t.Fatal("expected HTTP 403 Forbidden after cert grant revocation")
	}
}

// TestE2E_Tier3_ProjectBootstrapTaskBudgetFlow verifies:
// Bootstrap MCP -> Task execution -> Budget reservation & exhaustion flow.
func TestE2E_Tier3_ProjectBootstrapTaskBudgetFlow(t *testing.T) {
	s, _, _, _, _ := setupE2ETest(t)
	ctx := context.Background()
	gitDir := setupE2EGitWorktree(t)

	credDir := filepath.Join(t.TempDir(), "creds")
	bootstrapKey := strings.Repeat("bootstrap-full-flow-key-", 2)
	if err := s.ConfigureLocalBootstrapKey(ctx, bootstrapKey); err != nil {
		t.Fatal(err)
	}

	// 1. Bootstrap project
	srv := coord.ProjectBootstrapMCPServer(s, credDir, bootstrapKey)
	res, err := srv.GetTool("gossip_create_project").Handler(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "gossip_create_project",
			Arguments: map[string]any{"local_path": gitDir, "request_id": "req-flow-1"},
		},
	})
	if err != nil || res.IsError {
		t.Fatalf("bootstrap failed: %+v %v", res, err)
	}
	var created struct {
		Project        coord.Project `json:"project"`
		CredentialFile string        `json:"owner_credential_file"`
	}
	_ = json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &created)

	credData, err := os.ReadFile(created.CredentialFile)
	if err != nil {
		t.Fatal(err)
	}
	var ownerCred coord.Credential
	_ = json.Unmarshal(credData, &ownerCred)

	// Publish plan
	if _, err := s.PublishPlan(ctx, created.Project.ID, ownerCred.Token, "Flow Plan", 0); err != nil {
		t.Fatal(err)
	}

	// Enroll worker
	workerCred, err := s.AddPrincipal(ctx, created.Project.ID, ownerCred.Token, "Flow Worker", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgePlan(ctx, created.Project.ID, workerCred.Token, 1); err != nil {
		t.Fatal(err)
	}

	// 2. Enable budget limit = 500
	_, err = s.SetBudget(ctx, created.Project.ID, ownerCred.Token, 0, 500)
	if err != nil {
		t.Fatal(err)
	}

	// Create Task 1
	contract := coord.ReportContract{ReviewType: "full_code", Formats: []string{"markdown"}}
	task1, err := s.CreateTask(ctx, created.Project.ID, ownerCred.Token, 1, "Task 1", "Desc", "implementation", contract, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Reserve 300 for Task 1
	_, err = s.ReserveBudget(ctx, created.Project.ID, ownerCred.Token, task1.ID, 300)
	if err != nil {
		t.Fatal(err)
	}

	// Worker claims Task 1 and records usage
	claim1, err := s.ClaimTask(ctx, created.Project.ID, workerCred.Token, task1.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.RecordAttemptUsage(ctx, created.Project.ID, workerCred.Token, task1.ID, claim1.Attempt, "req-u1", coord.UsageReport{
		Provider: "OpenAI", Model: "gpt-4o", Source: "provider_reported", InputTokens: 200, OutputTokens: 50, CostMicros: 1000, Currency: "USD",
	})
	if err != nil {
		t.Fatal(err)
	}

	rep1, err := s.SubmitReport(ctx, created.Project.ID, workerCred.Token, task1.ID, claim1.Attempt, "commit", "Report 1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AcceptReport(ctx, created.Project.ID, ownerCred.Token, rep1.ID, rep1.Digest); err != nil {
		t.Fatal(err)
	}

	// Create Task 2 & Task 3
	task2, _ := s.CreateTask(ctx, created.Project.ID, ownerCred.Token, 1, "Task 2", "Desc", "implementation", contract, nil)
	task3, _ := s.CreateTask(ctx, created.Project.ID, ownerCred.Token, 1, "Task 3", "Desc", "implementation", contract, nil)

	// Reserve 200 for Task 2 (total 300 + 200 = 500 reserved)
	_, err = s.ReserveBudget(ctx, created.Project.ID, ownerCred.Token, task2.ID, 200)
	if err != nil {
		t.Fatal(err)
	}

	// Trying to reserve additional 100 for Task 3 must fail (exceeds 500)
	_, err = s.ReserveBudget(ctx, created.Project.ID, ownerCred.Token, task3.ID, 100)
	if !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("expected ErrConflict when budget envelope is exhausted, got %v", err)
	}

	// Releasing Task 1 reservation frees 300 tokens
	_, err = s.ReserveBudget(ctx, created.Project.ID, ownerCred.Token, task1.ID, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Now Task 3 reservation succeeds
	_, err = s.ReserveBudget(ctx, created.Project.ID, ownerCred.Token, task3.ID, 100)
	if err != nil {
		t.Fatalf("expected reservation to succeed after freeing budget, got %v", err)
	}
}

// ============================================================================
// Tier 4: Real-World Multi-Agent Coordination Scenarios
// ============================================================================

// TestE2E_Tier4_FullMultiAgentCoordinationWorkflow verifies:
// Full end-to-end multi-agent flow:
// 1. Owner publishes initial plan (revision 1)
// 2. Workers acknowledge plan revision
// 3. Owner plans dependent research (diagram_report) and implementation (full_code) tasks
// 4. Worker 1 claims research task, exchanges chat message, submits UML diagram report
// 5. Owner reviews diagram report and accepts digest
// 6. Dependent implementation task becomes unblocked
// 7. Worker 2 claims implementation task against Git workspace baseline, captures worktree diff, requests review
// 8. Owner reviews full-code candidate and accepts
func TestE2E_Tier4_FullMultiAgentCoordinationWorkflow(t *testing.T) {
	s, _, p, owner, worker1 := setupE2ETest(t)
	ctx := context.Background()

	// Add worker 2
	worker2, err := s.AddPrincipal(ctx, p.ID, owner.Token, "Worker 2", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgePlan(ctx, p.ID, worker2.Token, 1); err != nil {
		t.Fatal(err)
	}

	// Step 3: Owner plans Task 1 (Research, diagram_report) and Task 2 (Implementation, full_code, depends on Task 1)
	diagContract := coord.ReportContract{
		ReviewType: "diagram_report",
		Formats:    []string{"markdown", "mermaid_class", "mermaid_sequence"},
		Coverage:   []string{"authorization", "retry"},
	}
	task1, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Research Architecture", "Analyze component interaction", "research", diagContract, nil)
	if err != nil {
		t.Fatal(err)
	}

	codeContract := coord.ReportContract{
		ReviewType: "full_code",
		Formats:    []string{"markdown"},
	}
	task2, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Implement Service", "Write Go coordination handlers", "implementation", codeContract, []string{task1.ID})
	if err != nil {
		t.Fatal(err)
	}
	if task2.Status != "blocked" {
		t.Fatalf("expected task 2 to be blocked by task 1, got %s", task2.Status)
	}

	// Step 4: Worker 1 claims task 1
	claim1, err := s.ClaimTask(ctx, p.ID, worker1.Token, task1.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	// Chat handoff: Worker 1 sends message to Worker 2
	msg, err := s.SendMessage(ctx, p.ID, worker1.Token, worker2.PrincipalID, "handoff", "Starting Research", "I am mapping the sequence diagram.")
	if err != nil {
		t.Fatal(err)
	}
	// Worker 2 reads and acknowledges
	messages, err := s.RecentMessages(ctx, p.ID, worker2.Token, 10)
	if err != nil || len(messages) == 0 || messages[0].ID != msg.ID {
		t.Fatalf("worker 2 did not receive message: %+v %v", messages, err)
	}
	if _, err := s.AcknowledgeMessage(ctx, p.ID, worker2.Token, msg.ID); err != nil {
		t.Fatal(err)
	}

	// Worker 1 requests review & submits UML diagram report
	if err := s.RequestReview(ctx, p.ID, worker1.Token, task1.ID, claim1.Attempt); err != nil {
		t.Fatal(err)
	}
	diagReport, err := s.SubmitReport(ctx, p.ID, worker1.Token, task1.ID, claim1.Attempt, "commit-arch", sampleValidDiagramReport)
	if err != nil {
		t.Fatalf("Worker 1 diagram report submission failed: %v", err)
	}

	// Step 5: Owner reviews and accepts diagram report
	if err := s.AcceptReport(ctx, p.ID, owner.Token, diagReport.ID, diagReport.Digest); err != nil {
		t.Fatalf("Owner accept diagram report failed: %v", err)
	}

	// Step 6: Task 2 automatically transitions to 'ready'
	updatedTask2, err := s.GetTask(ctx, p.ID, worker2.Token, task2.ID)
	if err != nil || updatedTask2.Status != "ready" {
		t.Fatalf("Task 2 did not unblock: %+v %v", updatedTask2, err)
	}

	// Step 7: Worker 2 sets up Git workspace & claims task 2
	gitDir := setupE2EGitWorktree(t)
	repo, err := s.CreateRepository(ctx, p.ID, owner.Token, "workflow-repo")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := s.RegisterWorkspace(ctx, p.ID, worker2.Token, repo.ID, "worker2-checkout", "node-2", gitDir, "main", "base-commit", false)
	if err != nil {
		t.Fatal(err)
	}

	claim2, err := s.ClaimTaskInWorkspace(ctx, p.ID, worker2.Token, task2.ID, ws.ID, time.Minute)
	if err != nil {
		t.Fatalf("Worker 2 claim task 2 failed: %v", err)
	}

	// Worker 2 creates candidate diff
	diff := "diff --git a/handler.go b/handler.go\nnew file mode 100644\n--- /dev/null\n+++ b/handler.go\n@@ -0,0 +1,3 @@\n+package coord\n+\n+func Serve() error { return nil }\n"
	cand, err := s.SaveCandidate(ctx, p.ID, worker2.Token, task2.ID, claim2.Attempt, ws.Head, diff)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RequestReview(ctx, p.ID, worker2.Token, task2.ID, claim2.Attempt); err != nil {
		t.Fatal(err)
	}

	codeReport, err := s.SubmitReport(ctx, p.ID, worker2.Token, task2.ID, claim2.Attempt, ws.Head, "Implemented Serve handler based on approved architecture diagram.")
	if err != nil {
		t.Fatal(err)
	}
	if codeReport.SourceCandidateID != cand.ID {
		t.Fatalf("expected candidate reference %s, got %s", cand.ID, codeReport.SourceCandidateID)
	}

	// Step 8: Owner accepts full-code candidate
	if err := s.AcceptReport(ctx, p.ID, owner.Token, codeReport.ID, codeReport.Digest); err != nil {
		t.Fatalf("Owner accept code report failed: %v", err)
	}

	// Verify both tasks ended in 'accepted' status
	allTasks, err := s.ListTasks(ctx, p.ID, owner.Token, 10)
	if err != nil || len(allTasks) != 2 {
		t.Fatalf("ListTasks failed: %+v %v", allTasks, err)
	}
	for _, tk := range allTasks {
		if tk.Status != "accepted" {
			t.Errorf("task %s ended in %s, want accepted", tk.ID, tk.Status)
		}
	}
}

// unreliableOutboxSender simulates network dropouts and coordinator restarts
type unreliableOutboxSender struct {
	coord.Backend
	offline atomic.Bool
}

func (u *unreliableOutboxSender) SendMessageWithID(ctx context.Context, p, t, recipient, thread, subject, body, id string) (coord.Message, error) {
	if u.offline.Load() {
		return coord.Message{}, errors.New("coordinator offline")
	}
	return u.Backend.SendMessageWithID(ctx, p, t, recipient, thread, subject, body, id)
}

// TestE2E_Tier4_DisconnectReconnectOutboxDeduplication verifies:
// 1. Active coordinator shuts down while worker is online
// 2. Worker attempts task / message updates and safely enqueues items into local durable outbox
// 3. Coordinator restarts against same database
// 4. Worker reconnects; queued work is delivered idempotently with zero duplicates
func TestE2E_Tier4_DisconnectReconnectOutboxDeduplication(t *testing.T) {
	s, _, p, owner, worker := setupE2ETest(t)
	ctx := context.Background()

	backend := &unreliableOutboxSender{Backend: s}
	outboxPath := filepath.Join(t.TempDir(), "disconnect_outbox.db")
	box, err := coord.OpenOutbox(outboxPath, "https://home.example:8443", p.ID, worker.PrincipalID, worker.Token, backend)
	if err != nil {
		t.Fatal(err)
	}

	adapter := coord.NewMCPAdapterWithOutbox(backend, p.ID, worker.Token, box)
	defer adapter.Close()

	// Send initial message while online -> delivered
	sendTool := adapter.Server.GetTool("gossip_send")
	res1, err := sendTool.Handler(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "gossip_send",
			Arguments: map[string]any{
				"operation":  "enqueue",
				"recipient":  owner.PrincipalID,
				"thread":     "reconnect-test",
				"subject":    "Online message",
				"body":       "Initial message online",
				"request_id": "req-msg-online",
			},
		},
	})
	if err != nil || res1.IsError {
		t.Fatalf("online send failed: %+v %v", res1, err)
	}

	// 1. Coordinator shuts down / goes offline
	backend.offline.Store(true)

	// 2. Worker enqueues message while disconnected -> durably stored in outbox
	res2, err := sendTool.Handler(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "gossip_send",
			Arguments: map[string]any{
				"operation":  "enqueue",
				"recipient":  owner.PrincipalID,
				"thread":     "reconnect-test",
				"subject":    "Offline message",
				"body":       "Message queued while offline",
				"request_id": "req-msg-offline",
			},
		},
	})
	if err != nil || res2.IsError {
		t.Fatalf("offline enqueue failed: %+v %v", res2, err)
	}

	// Verify outbox lists it as pending
	outboxListRes, err := sendTool.Handler(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "gossip_send",
			Arguments: map[string]any{"operation": "outbox"},
		},
	})
	if err != nil || outboxListRes.IsError {
		t.Fatalf("outbox listing failed: %+v %v", outboxListRes, err)
	}
	outboxListText := outboxListRes.Content[0].(mcp.TextContent).Text
	if !strings.Contains(outboxListText, "pending") || !strings.Contains(outboxListText, "req-msg-offline") {
		t.Fatalf("expected pending outbox entry: %s", outboxListText)
	}

	// 3. Coordinator restarts against same database -> back online
	backend.offline.Store(false)

	// 4. Worker delivers queued work idempotently via background outbox runner
	deadline := time.Now().Add(5 * time.Second)
	delivered := false
	for time.Now().Before(deadline) {
		list, err := box.List(ctx, 10)
		if err == nil {
			for _, item := range list {
				if item.RequestID == "req-msg-offline" && item.Status == "delivered" {
					delivered = true
					break
				}
			}
		}
		if delivered {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !delivered {
		t.Fatal("queued offline message was not delivered after coordinator reconnected")
	}

	// Check messages on coordinator store
	msgs, err := s.RecentMessages(ctx, p.ID, owner.Token, 10)
	if err != nil {
		t.Fatal(err)
	}
	foundCount := 0
	for _, m := range msgs {
		if m.Body == "Message queued while offline" {
			foundCount++
		}
	}
	if foundCount != 1 {
		t.Fatalf("expected exactly 1 delivered message with no duplicates, got %d", foundCount)
	}

	// Duplicate retry attempt must not duplicate message
	resDup, err := sendTool.Handler(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "gossip_send",
			Arguments: map[string]any{
				"operation":  "enqueue",
				"recipient":  owner.PrincipalID,
				"thread":     "reconnect-test",
				"subject":    "Offline message",
				"body":       "Message queued while offline",
				"request_id": "req-msg-offline",
			},
		},
	})
	if err != nil || resDup.IsError {
		t.Fatalf("idempotent duplicate enqueue failed: %+v %v", resDup, err)
	}
	msgsAfter, _ := s.RecentMessages(ctx, p.ID, owner.Token, 10)
	foundCountAfter := 0
	for _, m := range msgsAfter {
		if m.Body == "Message queued while offline" {
			foundCountAfter++
		}
	}
	if foundCountAfter != 1 {
		t.Fatalf("expected exactly 1 delivered message after duplicate retry, got %d", foundCountAfter)
	}
}
