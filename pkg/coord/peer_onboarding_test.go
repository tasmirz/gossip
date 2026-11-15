package coord_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
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

func reserveFreeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate free TCP port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func doLoopbackJSON(t *testing.T, h http.Handler, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode request body: %v", err)
		}
	}
	req := httptest.NewRequest(method, target, &buf)
	req.RemoteAddr = "127.0.0.1:54321"
	origin := "http://127.0.0.1:47281"
	if req.URL.Scheme != "" && req.Host != "" {
		origin = req.URL.Scheme + "://" + req.Host
	}
	req.Header.Set("Origin", origin)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func buildTestFRPBinary(t *testing.T) string {
	t.Helper()
	frpDir, err := filepath.Abs(filepath.Join("..", "..", "frp"))
	if err != nil {
		t.Fatalf("resolve frp dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(frpDir, "main.go")); err != nil {
		t.Skipf("companion frp source not present at %s: %v", frpDir, err)
	}
	binPath := filepath.Join(t.TempDir(), "gossip-frp")
	cmd := exec.Command("go", "build", "-o", binPath, ".")
	cmd.Dir = frpDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build companion gossip-frp: %v\n%s", err, out)
	}
	return binPath
}

func waitForTCPReady(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 150*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(80 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for TCP listener %s", addr)
}

func TestEncryptedInviteRoundTripAndNoCleartextLeak(t *testing.T) {
	pass, err := coord.GenerateInvitePassphrase()
	if err != nil {
		t.Fatalf("GenerateInvitePassphrase: %v", err)
	}
	if len(pass) != 19 || strings.Count(pass, "-") != 3 {
		t.Fatalf("unexpected generated passphrase format %q", pass)
	}
	pass2, err := coord.GenerateInvitePassphrase()
	if err != nil {
		t.Fatalf("GenerateInvitePassphrase second call: %v", err)
	}
	if pass == pass2 {
		t.Fatalf("expected distinct generated passphrases, got duplicate %q", pass)
	}

	// Self-signed test CA PEM for bundle validation.
	s, _, proj, ownerCred, _ := setup(t)
	_ = ownerCred
	ctx := context.Background()
	caDir := t.TempDir()
	pm, err := coord.NewPeerRuntime(s, caDir)
	if err != nil {
		t.Fatalf("NewPeerRuntime: %v", err)
	}
	t.Cleanup(func() { _ = pm.Close() })

	port := reserveFreeTCPPort(t)
	status, err := pm.Configure(ctx, coord.PeerAccessUpdate{
		LANEnabled: boolPtr(true),
		ListenPort: &port,
	})
	if err != nil {
		t.Fatalf("Configure LAN: %v", err)
	}
	if status.ServerCA == "" {
		t.Fatal("expected auto-generated ServerCA")
	}

	invite, code, err := s.CreateRemoteInvitationWithOptions(ctx, proj.ID, ownerCred.Token, "Teammate", "worker", true)
	if err != nil {
		t.Fatalf("CreateRemoteInvitationWithOptions: %v", err)
	}
	secretEndpoint := "https://192.168.42.99:8443"
	payload := coord.EncryptedInvitePayload{
		Version:     1,
		ProjectName: proj.Name,
		Mode:        "lan",
		Invitation:  invite,
		Endpoint:    secretEndpoint,
		ServerCA:    status.ServerCA,
		HomeID:      s.InstanceID(),
		Code:        code,
	}

	if _, err := coord.EncryptEnrollmentBundle(payload, "short"); err == nil {
		t.Fatal("expected short passphrase (<8 chars) to be rejected")
	}

	sealed, err := coord.EncryptEnrollmentBundle(payload, pass)
	if err != nil {
		t.Fatalf("EncryptEnrollmentBundle: %v", err)
	}
	if !coord.IsEncryptedInviteEnvelope(sealed) {
		t.Fatalf("IsEncryptedInviteEnvelope returned false for %s", string(sealed))
	}

	sealedStr := string(sealed)
	for _, sensitive := range []string{secretEndpoint, s.InstanceID(), proj.ID, invite.ID, code, "BEGIN CERTIFICATE"} {
		if strings.Contains(sealedStr, sensitive) {
			t.Fatalf("encrypted invite leaked sensitive string %q in envelope", sensitive)
		}
	}

	if _, err := coord.DecryptEnrollmentBundle(sealed, "WRONG-PASS-9999"); err == nil {
		t.Fatal("expected wrong passphrase to fail decryption")
	}

	// Tamper ciphertext byte and verify AES-GCM tag failure.
	var env coord.EncryptedEnrollmentEnvelope
	if err := json.Unmarshal(sealed, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	rawCT, err := base64.StdEncoding.DecodeString(env.Ciphertext)
	if err != nil || len(rawCT) == 0 {
		t.Fatalf("decode ciphertext: %v", err)
	}
	rawCT[0] ^= 0xff
	env.Ciphertext = base64.StdEncoding.EncodeToString(rawCT)
	tampered, _ := json.Marshal(env)
	if _, err := coord.DecryptEnrollmentBundle(tampered, pass); err == nil {
		t.Fatal("expected tampered ciphertext to fail authentication")
	}

	got, err := coord.DecryptEnrollmentBundle(sealed, pass)
	if err != nil {
		t.Fatalf("DecryptEnrollmentBundle: %v", err)
	}
	if got.Endpoint != secretEndpoint || got.HomeID != s.InstanceID() || got.Code != code {
		t.Fatalf("decrypted payload mismatch: %+v", got)
	}
	if got.Invitation.AutoApprove {
		t.Fatalf("expected AutoApprove to always be forced false, got true")
	}
}

func completeManualCodeApproval(t *testing.T, hostWeb, peerWeb http.Handler, projID string, acceptBody map[string]any) (string, string) {
	t.Helper()
	pendingRec := doLoopbackJSON(
		t,
		peerWeb,
		http.MethodPost,
		"http://127.0.0.1:8081/api/local/accept-invitation",
		acceptBody,
	)
	if pendingRec.Code != http.StatusOK {
		t.Fatalf("peer accept-invitation (step 1) failed: %d %s", pendingRec.Code, pendingRec.Body.String())
	}
	var pendingResp struct {
		ID               string `json:"id"`
		State            string `json:"state"`
		ProjectID        string `json:"project_id"`
		HomeID           string `json:"home_id"`
		VerificationCode string `json:"verification_code"`
		SSHFingerprint   string `json:"ssh_fingerprint"`
		HostFingerprint  string `json:"host_fingerprint"`
	}
	if err := json.Unmarshal(pendingRec.Body.Bytes(), &pendingResp); err != nil {
		t.Fatalf("unmarshal pending join response: %v", err)
	}
	if pendingResp.State != "pending" {
		t.Fatalf("expected mandatory manual approval state=pending even when auto_approve=true was requested, got %q (%s)", pendingResp.State, pendingRec.Body.String())
	}
	if len(pendingResp.VerificationCode) != 9 || pendingResp.VerificationCode[4] != '-' {
		t.Fatalf("expected XXXX-XXXX verification code, got %q", pendingResp.VerificationCode)
	}
	if !strings.HasPrefix(pendingResp.SSHFingerprint, "SHA256:") || !strings.HasPrefix(pendingResp.HostFingerprint, "SHA256:") {
		t.Fatalf("expected SHA256 fingerprints on joiner, got ssh=%q host=%q", pendingResp.SSHFingerprint, pendingResp.HostFingerprint)
	}

	// Host checks pending requests and sees the exact same verification code & SSH key fingerprint.
	hostListRec := doLoopbackJSON(t, hostWeb, http.MethodGet, "http://127.0.0.1:8081/api/projects/"+projID+"/remote-invitations", nil)
	if hostListRec.Code != http.StatusOK {
		t.Fatalf("host list remote-invitations failed: %d %s", hostListRec.Code, hostListRec.Body.String())
	}
	var hostInvites []coord.RemoteInvitation
	if err := json.Unmarshal(hostListRec.Body.Bytes(), &hostInvites); err != nil {
		t.Fatalf("unmarshal host remote-invitations: %v", err)
	}
	var matched bool
	for _, hi := range hostInvites {
		if hi.ID == pendingResp.ID {
			matched = true
			if hi.State != "pending" {
				t.Fatalf("expected host invitation %s state=pending, got %q", hi.ID, hi.State)
			}
			if hi.VerificationCode != pendingResp.VerificationCode || hi.SSHFingerprint != pendingResp.SSHFingerprint {
				t.Fatalf("verification mismatch host(%q, %q) vs joiner(%q, %q)", hi.VerificationCode, hi.SSHFingerprint, pendingResp.VerificationCode, pendingResp.SSHFingerprint)
			}
		}
	}
	if !matched {
		t.Fatalf("host remote-invitations did not contain invite %s", pendingResp.ID)
	}

	// Host owner clicks Accept.
	approveRec := doLoopbackJSON(
		t,
		hostWeb,
		http.MethodPost,
		"http://127.0.0.1:8081/api/projects/"+projID+"/remote-invitations",
		map[string]any{"operation": "approved", "id": pendingResp.ID},
	)
	if approveRec.Code != http.StatusOK {
		t.Fatalf("host approve failed: %d %s", approveRec.Code, approveRec.Body.String())
	}

	// Joiner polls again and completes mTLS enrollment automatically.
	finalRec := doLoopbackJSON(
		t,
		peerWeb,
		http.MethodPost,
		"http://127.0.0.1:8081/api/local/accept-invitation",
		acceptBody,
	)
	if finalRec.Code != http.StatusOK {
		t.Fatalf("peer accept-invitation (step 2) failed: %d %s", finalRec.Code, finalRec.Body.String())
	}
	var finalResp struct {
		State     string `json:"state"`
		ProjectID string `json:"project_id"`
		HomeID    string `json:"home_id"`
	}
	if err := json.Unmarshal(finalRec.Body.Bytes(), &finalResp); err != nil {
		t.Fatalf("unmarshal final join response: %v", err)
	}
	if finalResp.State != "enrolled" || finalResp.ProjectID != projID {
		t.Fatalf("expected enrolled join after manual acceptance, got %+v", finalResp)
	}
	return finalResp.HomeID, pendingResp.VerificationCode
}

func TestPeerOnboardingAutoStartLANAndOneStepEncryptedJoin(t *testing.T) {
	hostStore, _, proj, ownerCred, _ := setup(t)
	ctx := context.Background()
	if _, err := hostStore.PublishPlan(ctx, proj.ID, ownerCred.Token, "# LAN Sync Plan\n\nVerified end to end", 1); err != nil {
		t.Fatalf("PublishPlan: %v", err)
	}

	hostRegistry, err := coord.OpenDeviceRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("OpenDeviceRegistry(host): %v", err)
	}
	t.Cleanup(func() { _ = hostRegistry.Close() })

	pm, err := coord.NewPeerRuntime(hostStore, t.TempDir())
	if err != nil {
		t.Fatalf("NewPeerRuntime: %v", err)
	}
	t.Cleanup(func() { _ = pm.Close() })

	// Default kill switch must start disabled.
	if enabled, err := hostStore.RemoteAccessEnabled(ctx); err != nil || enabled {
		t.Fatalf("expected default remote access disabled, got enabled=%v err=%v", enabled, err)
	}
	listenPort := reserveFreeTCPPort(t)
	if _, err := pm.Configure(ctx, coord.PeerAccessUpdate{ListenPort: &listenPort}); err != nil {
		t.Fatalf("set listen port while disabled: %v", err)
	}

	hostWeb := coord.NewWebHandlerWithRegistry(hostStore, hostRegistry, "default", hostStore.InstanceID())
	hostWeb.SetPeerRuntime(pm)

	// Non-loopback requests must be rejected from peer-access config & auto-start.
	nonLoopback := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8081/api/local/peer-access", nil)
	nonLoopback.RemoteAddr = "192.168.1.55:44444"
	nonLoopbackRec := httptest.NewRecorder()
	hostWeb.ServeHTTP(nonLoopbackRec, nonLoopback)
	if nonLoopbackRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-loopback peer-access read, got %d", nonLoopbackRec.Code)
	}

	// Owner creates an encrypted LAN invite without manually toggling settings first.
	pass, err := coord.GenerateInvitePassphrase()
	if err != nil {
		t.Fatalf("GenerateInvitePassphrase: %v", err)
	}
	createRec := doLoopbackJSON(
		t,
		hostWeb,
		http.MethodPost,
		"http://127.0.0.1:8081/api/projects/"+proj.ID+"/remote-invitations",
		map[string]any{
			"operation":    "create",
			"mode":         "lan",
			"name":         "Laptop-B",
			"role":         "worker",
			"encrypt":      true,
			"passphrase":   pass,
			"auto_approve": true, // Server must ignore/disable auto_approve.
		},
	)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create encrypted LAN invite failed: %d %s", createRec.Code, createRec.Body.String())
	}

	var createResp struct {
		Endpoint        string `json:"endpoint"`
		HomeID          string `json:"home_id"`
		Mode            string `json:"mode"`
		AutoApprove     bool   `json:"auto_approve"`
		HostFingerprint string `json:"host_fingerprint"`
		EncryptedFile   string `json:"encrypted_file"`
		Filename        string `json:"filename"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("unmarshal create response: %v", err)
	}
	if createResp.Mode != "lan" || createResp.AutoApprove || !strings.HasSuffix(createResp.Filename, ".gossip") {
		t.Fatalf("unexpected create response metadata (auto_approve must be false): %+v", createResp)
	}
	if !strings.HasPrefix(createResp.HostFingerprint, "SHA256:") {
		t.Fatalf("expected SHA256 host_fingerprint in create response, got %q", createResp.HostFingerprint)
	}
	if !coord.IsEncryptedInviteEnvelope([]byte(createResp.EncryptedFile)) {
		t.Fatalf("expected encrypted envelope in encrypted_file")
	}

	// Creating the LAN invite must have auto-started the mTLS listener on 0.0.0.0:<listenPort>.
	status, err := pm.Status(ctx)
	if err != nil {
		t.Fatalf("pm.Status: %v", err)
	}
	if !status.Enabled || !status.LANEnabled || !status.ListenerRunning || status.ActivePort != listenPort {
		t.Fatalf("expected auto-started LAN listener on %d, got %+v", listenPort, status)
	}
	if !strings.HasPrefix(status.ActiveListenAddr, "0.0.0.0:") && !strings.HasPrefix(status.ActiveListenAddr, "[::]:") {
		t.Fatalf("expected wildcard LAN bind (0.0.0.0 or [::]) when LAN enabled, got %q", status.ActiveListenAddr)
	}
	if len(status.DetectedLANIPs) > 0 && !strings.Contains(createResp.Endpoint, status.DetectedLANIPs[0]) {
		t.Fatalf("expected LAN invite endpoint %q to include primary detected LAN IP %q", createResp.Endpoint, status.DetectedLANIPs[0])
	}

	// Second independent peer node decrypts .gossip, compares code with host, waits for host Accept, then reads plan.
	peerStore, err := coord.Open(filepath.Join(t.TempDir(), "peer.db"))
	if err != nil {
		t.Fatalf("OpenStore(peer): %v", err)
	}
	t.Cleanup(func() { _ = peerStore.Close() })
	peerRegistry, err := coord.OpenDeviceRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("OpenDeviceRegistry(peer): %v", err)
	}
	t.Cleanup(func() { _ = peerRegistry.Close() })
	peerWeb := coord.NewWebHandlerWithRegistry(peerStore, peerRegistry, "default", peerStore.InstanceID())

	joinedHomeID, _ := completeManualCodeApproval(t, hostWeb, peerWeb, proj.ID, map[string]any{
		"encrypted_file": createResp.EncryptedFile,
		"passphrase":     pass,
	})
	if joinedHomeID != hostStore.InstanceID() {
		t.Fatalf("unexpected joined home_id %q, want %q", joinedHomeID, hostStore.InstanceID())
	}

	// Peer dashboard sees the joined remote project and can read its plan over TLS.
	planRec := doLoopbackJSON(
		t,
		peerWeb,
		http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:8081/api/projects/%s/plan?home=%s", proj.ID, hostStore.InstanceID()),
		nil,
	)
	if planRec.Code != http.StatusOK || !strings.Contains(planRec.Body.String(), "LAN Sync Plan") {
		t.Fatalf("peer read remote plan failed: %d %s", planRec.Code, planRec.Body.String())
	}

	// Disabling all remote access stops the peer listener and sets RemoteAccessEnabled=false.
	disableRec := doLoopbackJSON(
		t,
		hostWeb,
		http.MethodPost,
		"http://127.0.0.1:8081/api/local/peer-access",
		map[string]any{"disable_all": true},
	)
	if disableRec.Code != http.StatusOK {
		t.Fatalf("disable all peer access failed: %d %s", disableRec.Code, disableRec.Body.String())
	}
	if enabled, _ := hostStore.RemoteAccessEnabled(ctx); enabled {
		t.Fatal("expected SQLite RemoteAccessEnabled to be false after disabling all")
	}
	afterDisablePlan := doLoopbackJSON(
		t,
		peerWeb,
		http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:8081/api/projects/%s/plan?home=%s", proj.ID, hostStore.InstanceID()),
		nil,
	)
	if afterDisablePlan.Code == http.StatusOK {
		t.Fatalf("expected peer connection to fail after host disabled remote access")
	}
}

func TestPeerOnboardingRealFRPEndToEnd(t *testing.T) {
	frpBin := buildTestFRPBinary(t)

	hostStore, _, proj, ownerCred, _ := setup(t)
	ctx := context.Background()
	if _, err := hostStore.PublishPlan(ctx, proj.ID, ownerCred.Token, "# FRP Tunnel A\n\nOver real frps #1", 1); err != nil {
		t.Fatalf("PublishPlan(projA): %v", err)
	}
	projB, ownerCredB, err := hostStore.CreateProject(ctx, "Isolated Project B", "Owner")
	if err != nil {
		t.Fatalf("CreateProject(projB): %v", err)
	}
	if _, err := hostStore.PublishPlan(ctx, projB.ID, ownerCredB.Token, "# FRP Tunnel B\n\nOver real frps #2", 0); err != nil {
		t.Fatalf("PublishPlan(projB): %v", err)
	}

	hostRegistry, err := coord.OpenDeviceRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("OpenDeviceRegistry(host): %v", err)
	}
	t.Cleanup(func() { _ = hostRegistry.Close() })

	pm, err := coord.NewPeerRuntime(hostStore, t.TempDir())
	if err != nil {
		t.Fatalf("NewPeerRuntime: %v", err)
	}
	t.Cleanup(func() { _ = pm.Close() })

	hostWeb := coord.NewWebHandlerWithRegistry(hostStore, hostRegistry, "default", hostStore.InstanceID())
	hostWeb.SetPeerRuntime(pm)

	// 1. Binary-level segregation: when companion gossip-frp binary is missing,
	// FRP fails clearly while non-FRP LAN remains available.
	t.Setenv("GOSSIP_FRP_BIN", filepath.Join(t.TempDir(), "missing-gossip-frp"))
	noBinStatusRec := doLoopbackJSON(t, hostWeb, http.MethodGet, "http://127.0.0.1:8081/api/local/peer-access?project="+proj.ID, nil)
	if noBinStatusRec.Code != http.StatusOK {
		t.Fatalf("GET peer-access without FRP binary failed: %d", noBinStatusRec.Code)
	}
	var noBinStatus coord.PeerAccessStatus
	_ = json.Unmarshal(noBinStatusRec.Body.Bytes(), &noBinStatus)
	if noBinStatus.FRPBinaryAvailable {
		t.Fatal("expected FRPBinaryAvailable=false when gossip-frp is missing")
	}
	enableNoBinRec := doLoopbackJSON(
		t,
		hostWeb,
		http.MethodPost,
		"http://127.0.0.1:8081/api/local/peer-access",
		map[string]any{
			"project_id":      proj.ID,
			"frp_enabled":     true,
			"frp_server":      "127.0.0.1",
			"frp_server_port": 7000,
			"frp_remote_port": 18443,
			"frp_token":       "tok",
		},
	)
	if enableNoBinRec.Code == http.StatusOK {
		t.Fatal("expected configuring FRP without gossip-frp binary to fail")
	}

	// Switch to built companion gossip-frp binary.
	t.Setenv("GOSSIP_FRP_BIN", frpBin)

	// 2. Share menu cannot auto-enable/configure FRP; must configure in Settings first.
	pass, err := coord.GenerateInvitePassphrase()
	if err != nil {
		t.Fatalf("GenerateInvitePassphrase: %v", err)
	}
	unconfiguredShareRec := doLoopbackJSON(
		t,
		hostWeb,
		http.MethodPost,
		"http://127.0.0.1:8081/api/projects/"+proj.ID+"/remote-invitations",
		map[string]any{
			"operation":    "create",
			"mode":         "frp",
			"name":         "Early-Joiner",
			"role":         "worker",
			"encrypt":      true,
			"passphrase":   pass,
			"auto_approve": true,
		},
	)
	if unconfiguredShareRec.Code == http.StatusOK {
		t.Fatal("expected Share FRP invite to fail when project FRP is not enabled in Settings")
	}

	// Start two independent frps servers (one for proj, one for projB).
	startTestFRPS := func(tok string) (int, int, *bytes.Buffer) {
		srvPort := reserveFreeTCPPort(t)
		remPort := reserveFreeTCPPort(t)
		cmd := exec.Command(frpBin, "server", "--port", fmt.Sprintf("%d", srvPort), "--token", tok)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Start(); err != nil {
			t.Fatalf("start gossip-frp server: %v", err)
		}
		t.Cleanup(func() {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
				_, _ = cmd.Process.Wait()
			}
		})
		waitForTCPReady(t, fmt.Sprintf("127.0.0.1:%d", srvPort), 5*time.Second)
		return srvPort, remPort, &out
	}
	frpTokenA := "secret-token-project-A-1111"
	frpTokenB := "secret-token-project-B-2222"
	serverPortA, remotePortA, frpsOutA := startTestFRPS(frpTokenA)
	serverPortB, remotePortB, frpsOutB := startTestFRPS(frpTokenB)
	_ = frpsOutB

	// 3. Configure and enable ONLY Project A's FRP tunnel in Settings.
	cfgARec := doLoopbackJSON(
		t,
		hostWeb,
		http.MethodPost,
		"http://127.0.0.1:8081/api/local/peer-access",
		map[string]any{
			"project_id":      proj.ID,
			"frp_enabled":     true,
			"frp_server":      "127.0.0.1",
			"frp_server_port": serverPortA,
			"frp_remote_port": remotePortA,
			"frp_token":       frpTokenA,
		},
	)
	if cfgARec.Code != http.StatusOK {
		t.Fatalf("configure project A FRP in Settings failed: %d %s", cfgARec.Code, cfgARec.Body.String())
	}
	if strings.Contains(cfgARec.Body.String(), frpTokenA) {
		t.Fatal("raw FRP token leaked in Settings HTTP response")
	}
	var statusA coord.PeerAccessStatus
	if err := json.Unmarshal(cfgARec.Body.Bytes(), &statusA); err != nil {
		t.Fatalf("unmarshal statusA: %v", err)
	}
	if statusA.LANEnabled || !statusA.FRPEnabled || statusA.ListenerRunning {
		t.Fatalf("expected LAN=false (ListenerRunning=false), ProjectA FRP=true, got %+v", statusA)
	}
	if !strings.HasPrefix(statusA.FRPLocalAddr, "127.0.0.1:") {
		t.Fatalf("expected project A isolated loopback listener 127.0.0.1:<port>, got %q", statusA.FRPLocalAddr)
	}

	// Verify Project B status shows FRP still disabled on Project B.
	statusBRec := doLoopbackJSON(t, hostWeb, http.MethodGet, "http://127.0.0.1:8081/api/local/peer-access?project="+projB.ID, nil)
	var statusB coord.PeerAccessStatus
	_ = json.Unmarshal(statusBRec.Body.Bytes(), &statusB)
	if statusB.FRPEnabled {
		t.Fatalf("expected Project B FRP to remain disabled while only Project A enabled, got %+v", statusB)
	}

	// 4. Create Project A encrypted invite from Share (zero FRP config fields in Share).
	createRec := doLoopbackJSON(
		t,
		hostWeb,
		http.MethodPost,
		"http://127.0.0.1:8081/api/projects/"+proj.ID+"/remote-invitations",
		map[string]any{
			"operation":    "create",
			"mode":         "frp",
			"name":         "Remote-Teammate-A",
			"role":         "worker",
			"encrypt":      true,
			"passphrase":   pass,
			"auto_approve": true,
		},
	)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create FRP encrypted invite failed: %d %s\nfrpsA:\n%s", createRec.Code, createRec.Body.String(), frpsOutA.String())
	}
	var createResp struct {
		Endpoint      string `json:"endpoint"`
		EncryptedFile string `json:"encrypted_file"`
		Mode          string `json:"mode"`
	}
	_ = json.Unmarshal(createRec.Body.Bytes(), &createResp)
	wantEndpointA := fmt.Sprintf("https://127.0.0.1:%d", remotePortA)
	if createResp.Endpoint != wantEndpointA || createResp.Mode != "frp" {
		t.Fatalf("unexpected FRP endpoint/mode: got (%q, %q), want (%q, frp)", createResp.Endpoint, createResp.Mode, wantEndpointA)
	}

	waitForTLSForwardReady(t, fmt.Sprintf("127.0.0.1:%d", remotePortA), 8*time.Second)

	// 5. Strict project-tunnel isolation: probing Project B over Project A's FRP tunnel MUST return 403.
	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM([]byte(statusA.ServerCA))
	probeClient := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: caPool}},
	}
	crossReq, _ := http.NewRequest(
		http.MethodPost,
		wantEndpointA+"/v1/enrollment/accept",
		strings.NewReader(fmt.Sprintf(`{"project_id":%q,"id":"inv_fake"}`, projB.ID)),
	)
	crossReq.Header.Set("Content-Type", "application/json")
	crossResp, err := probeClient.Do(crossReq)
	if err != nil {
		t.Fatalf("cross-project probe over Tunnel A failed at transport layer: %v", err)
	}
	_ = crossResp.Body.Close()
	if crossResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected HTTP 403 when accessing Project B through Project A FRP tunnel, got %d", crossResp.StatusCode)
	}

	// Separate peer joins Project A via encrypted .gossip + passphrase over Tunnel A.
	peerStoreA, err := coord.Open(filepath.Join(t.TempDir(), "frp-peer-a.db"))
	if err != nil {
		t.Fatalf("Open(peerA): %v", err)
	}
	t.Cleanup(func() { _ = peerStoreA.Close() })
	peerRegistryA, err := coord.OpenDeviceRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("OpenDeviceRegistry(peerA): %v", err)
	}
	t.Cleanup(func() { _ = peerRegistryA.Close() })
	peerWebA := coord.NewWebHandlerWithRegistry(peerStoreA, peerRegistryA, "default", peerStoreA.InstanceID())

	completeManualCodeApproval(t, hostWeb, peerWebA, proj.ID, map[string]any{"encrypted_file": createResp.EncryptedFile, "passphrase": pass})
	planRecA := doLoopbackJSON(
		t,
		peerWebA,
		http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:8081/api/projects/%s/plan?home=%s", proj.ID, hostStore.InstanceID()),
		nil,
	)
	if planRecA.Code != http.StatusOK || !strings.Contains(planRecA.Body.String(), "Over real frps #1") {
		t.Fatalf("peer A read remote plan via FRP Tunnel A failed: %d %s", planRecA.Code, planRecA.Body.String())
	}

	// 6. Configure Project B on a SECOND independent FRP server & join Project B over Tunnel B!
	cfgBRec := doLoopbackJSON(
		t,
		hostWeb,
		http.MethodPost,
		"http://127.0.0.1:8081/api/local/peer-access",
		map[string]any{
			"project_id":      projB.ID,
			"frp_enabled":     true,
			"frp_server":      "127.0.0.1",
			"frp_server_port": serverPortB,
			"frp_remote_port": remotePortB,
			"frp_token":       frpTokenB,
		},
	)
	if cfgBRec.Code != http.StatusOK {
		t.Fatalf("configure project B FRP in Settings failed: %d %s", cfgBRec.Code, cfgBRec.Body.String())
	}
	passB, _ := coord.GenerateInvitePassphrase()
	createBRec := doLoopbackJSON(
		t,
		hostWeb,
		http.MethodPost,
		"http://127.0.0.1:8081/api/projects/"+projB.ID+"/remote-invitations",
		map[string]any{
			"operation":    "create",
			"mode":         "frp",
			"name":         "Remote-Teammate-B",
			"role":         "worker",
			"encrypt":      true,
			"passphrase":   passB,
			"auto_approve": true,
		},
	)
	if createBRec.Code != http.StatusOK {
		t.Fatalf("create Project B FRP invite failed: %d %s", createBRec.Code, createBRec.Body.String())
	}
	var createBResp struct {
		Endpoint      string `json:"endpoint"`
		EncryptedFile string `json:"encrypted_file"`
	}
	_ = json.Unmarshal(createBRec.Body.Bytes(), &createBResp)
	wantEndpointB := fmt.Sprintf("https://127.0.0.1:%d", remotePortB)
	if createBResp.Endpoint != wantEndpointB {
		t.Fatalf("unexpected Project B FRP endpoint %q, want %q", createBResp.Endpoint, wantEndpointB)
	}
	waitForTLSForwardReady(t, fmt.Sprintf("127.0.0.1:%d", remotePortB), 8*time.Second)

	peerStoreB, _ := coord.Open(filepath.Join(t.TempDir(), "frp-peer-b.db"))
	t.Cleanup(func() { _ = peerStoreB.Close() })
	peerRegB, _ := coord.OpenDeviceRegistry(t.TempDir())
	t.Cleanup(func() { _ = peerRegB.Close() })
	peerWebB := coord.NewWebHandlerWithRegistry(peerStoreB, peerRegB, "default", peerStoreB.InstanceID())

	completeManualCodeApproval(t, hostWeb, peerWebB, projB.ID, map[string]any{"encrypted_file": createBResp.EncryptedFile, "passphrase": passB})
	planRecB := doLoopbackJSON(
		t,
		peerWebB,
		http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:8081/api/projects/%s/plan?home=%s", projB.ID, hostStore.InstanceID()),
		nil,
	)
	if planRecB.Code != http.StatusOK || !strings.Contains(planRecB.Body.String(), "Over real frps #2") {
		t.Fatalf("peer B read remote plan via FRP Tunnel B failed: %d %s", planRecB.Code, planRecB.Body.String())
	}

	// 7. Turning off Project A's FRP keeps Project B's FRP running cleanly!
	offARec := doLoopbackJSON(
		t,
		hostWeb,
		http.MethodPost,
		"http://127.0.0.1:8081/api/local/peer-access",
		map[string]any{"project_id": proj.ID, "frp_enabled": false},
	)
	if offARec.Code != http.StatusOK {
		t.Fatalf("turn off Project A FRP failed: %d %s", offARec.Code, offARec.Body.String())
	}
	if enabled, _ := hostStore.RemoteAccessEnabled(ctx); !enabled {
		t.Fatal("expected RemoteAccessEnabled to remain true while Project B FRP is still active")
	}
	planBStillWorks := doLoopbackJSON(
		t,
		peerWebB,
		http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:8081/api/projects/%s/plan?home=%s", projB.ID, hostStore.InstanceID()),
		nil,
	)
	if planBStillWorks.Code != http.StatusOK {
		t.Fatalf("expected Project B FRP tunnel to stay unaffected after Project A disabled, got %d", planBStillWorks.Code)
	}

	// 8. Disable all shuts down every FRP tunnel & sets RemoteAccessEnabled=false.
	disableAllRec := doLoopbackJSON(
		t,
		hostWeb,
		http.MethodPost,
		"http://127.0.0.1:8081/api/local/peer-access",
		map[string]any{"disable_all": true},
	)
	if disableAllRec.Code != http.StatusOK {
		t.Fatalf("disable_all failed: %d %s", disableAllRec.Code, disableAllRec.Body.String())
	}
	if enabled, _ := hostStore.RemoteAccessEnabled(ctx); enabled {
		t.Fatal("expected RemoteAccessEnabled=false after disable_all")
	}
}

func waitForTLSForwardReady(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 250 * time.Millisecond}, "tcp", addr, &tls.Config{InsecureSkipVerify: true})
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for forwarded TLS endpoint %s", addr)
}

func boolPtr(v bool) *bool { return &v }

func buildMockSSHBinary(t *testing.T) string {
	t.Helper()
	srcDir := t.TempDir()
	src := `package main

import (
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

func main() {
	var spec string
	for i := 1; i < len(os.Args)-1; i++ {
		if os.Args[i] == "-L" {
			spec = os.Args[i+1]
			break
		}
	}
	parts := strings.Split(spec, ":")
	if len(parts) != 4 {
		os.Exit(2)
	}
	listenAddr := net.JoinHostPort(parts[0], parts[1])
	targetAddr := net.JoinHostPort(parts[2], parts[3])
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		os.Exit(3)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(src net.Conn) {
				defer src.Close()
				dst, err := net.Dial("tcp", targetAddr)
				if err != nil {
					return
				}
				defer dst.Close()
				go io.Copy(dst, src)
				_, _ = io.Copy(src, dst)
			}(c)
		}
	}()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
}
`
	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), []byte(src), 0644); err != nil {
		t.Fatalf("write mock ssh source: %v", err)
	}
	binPath := filepath.Join(srcDir, "mock-ssh")
	cmd := exec.Command("go", "build", "-o", binPath, "main.go")
	cmd.Dir = srcDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build mock ssh binary: %v\n%s", err, out)
	}
	return binPath
}

func TestSSHValidationAndCommandArgs(t *testing.T) {
	for _, bad := range []string{"", "-oProxyCommand=id", "alice@host -v", "bad;target", "user@host\n"} {
		if _, err := coord.ValidateSSHTarget(bad); err == nil {
			t.Fatalf("expected ValidateSSHTarget(%q) to fail", bad)
		}
	}
	if got, err := coord.ValidateSSHTarget(" alice@bastion.example "); err != nil || got != "alice@bastion.example" {
		t.Fatalf("ValidateSSHTarget valid failed: got=%q err=%v", got, err)
	}
	if _, err := coord.ValidateSSHIdentityFile("-id_ed25519"); err == nil {
		t.Fatal("expected ValidateSSHIdentityFile leading dash to fail")
	}

	revArgs, err := coord.BuildReverseSSHArgs(coord.SSHTunnelSpec{
		Target:     "alice@relay.example",
		SSHPort:    2222,
		RemotePort: 19443,
	}, 9443)
	if err != nil {
		t.Fatalf("BuildReverseSSHArgs: %v", err)
	}
	joined := strings.Join(revArgs, " ")
	for _, want := range []string{"BatchMode=yes", "ExitOnForwardFailure=yes", "-R 127.0.0.1:19443:127.0.0.1:9443", "-- alice@relay.example"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("BuildReverseSSHArgs missing %q in %v", want, revArgs)
		}
	}

	locArgs, err := coord.BuildLocalSSHArgs(coord.SSHTunnelSpec{
		Target:     "alice@host.example",
		SSHPort:    22,
		RemotePort: 9443,
	}, 45678)
	if err != nil {
		t.Fatalf("BuildLocalSSHArgs: %v", err)
	}
	joinedLoc := strings.Join(locArgs, " ")
	for _, want := range []string{"BatchMode=yes", "ExitOnForwardFailure=yes", "-L 127.0.0.1:45678:127.0.0.1:9443", "-- alice@host.example"} {
		if !strings.Contains(joinedLoc, want) {
			t.Fatalf("BuildLocalSSHArgs missing %q in %v", want, locArgs)
		}
	}
}

func TestKnownHostsTOFUAndMITMProtection(t *testing.T) {
	ctx := context.Background()

	// Host #1 with SSH loopback listener.
	host1Store, _, proj1, _, _ := setup(t)
	pm1, err := coord.NewPeerRuntime(host1Store, t.TempDir())
	if err != nil {
		t.Fatalf("NewPeerRuntime(host1): %v", err)
	}
	t.Cleanup(func() { _ = pm1.Close() })
	port := reserveFreeTCPPort(t)
	trueVal := true
	falseVal := false
	status1, err := pm1.Configure(ctx, coord.PeerAccessUpdate{
		ProjectID:    proj1.ID,
		SSHEnabled:   &trueVal,
		SSHLocalPort: &port,
	})
	if err != nil {
		t.Fatalf("Configure host1 SSH listener: %v", err)
	}
	endpoint := fmt.Sprintf("https://127.0.0.1:%d", port)
	probe1, err := coord.ProbePeerEndpoint(ctx, endpoint)
	if err != nil {
		t.Fatalf("ProbePeerEndpoint(host1): %v", err)
	}
	if probe1.HostFingerprint != status1.HostFingerprint || !strings.HasPrefix(probe1.HostFingerprint, "SHA256:") {
		t.Fatalf("unexpected probed fingerprint %q vs %q", probe1.HostFingerprint, status1.HostFingerprint)
	}

	peerStore, _ := coord.Open(filepath.Join(t.TempDir(), "tofu-peer.db"))
	t.Cleanup(func() { _ = peerStore.Close() })
	peerReg, err := coord.OpenDeviceRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("OpenDeviceRegistry: %v", err)
	}
	t.Cleanup(func() { _ = peerReg.Close() })
	peerWeb := coord.NewWebHandlerWithRegistry(peerStore, peerReg, "default", peerStore.InstanceID())

	// Step 1: Unseen host reports status="untrusted" when trust_host_key=false.
	probeReq1 := doLoopbackJSON(t, peerWeb, http.MethodPost, "http://127.0.0.1:8081/api/local/probe-host", map[string]any{
		"endpoint":       endpoint,
		"trust_host_key": false,
	})
	if probeReq1.Code != http.StatusOK || !strings.Contains(probeReq1.Body.String(), `"status":"untrusted"`) {
		t.Fatalf("expected status=untrusted on first contact, got %d %s", probeReq1.Code, probeReq1.Body.String())
	}

	// Step 2: Explicitly trusting saves host key in known_hosts.
	trustRec := doLoopbackJSON(t, peerWeb, http.MethodPost, "http://127.0.0.1:8081/api/local/probe-host", map[string]any{
		"endpoint":       endpoint,
		"trust_host_key": true,
	})
	if trustRec.Code != http.StatusOK || !strings.Contains(trustRec.Body.String(), `"status":"trusted"`) {
		t.Fatalf("expected status=trusted after TOFU trust, got %d %s", trustRec.Code, trustRec.Body.String())
	}

	// Close host1 listener and start a DIFFERENT host2 (different CA key) on the same port -> MITM alert!
	if _, err := pm1.Configure(ctx, coord.PeerAccessUpdate{ProjectID: proj1.ID, SSHEnabled: &falseVal}); err != nil {
		t.Fatalf("disable host1: %v", err)
	}
	host2Store, _, proj2, _, _ := setup(t)
	pm2, err := coord.NewPeerRuntime(host2Store, t.TempDir())
	if err != nil {
		t.Fatalf("NewPeerRuntime(host2): %v", err)
	}
	t.Cleanup(func() { _ = pm2.Close() })
	if _, err := pm2.Configure(ctx, coord.PeerAccessUpdate{
		ProjectID:    proj2.ID,
		SSHEnabled:   &trueVal,
		SSHLocalPort: &port,
	}); err != nil {
		t.Fatalf("Configure host2 SSH listener: %v", err)
	}

	mitmRec := doLoopbackJSON(t, peerWeb, http.MethodPost, "http://127.0.0.1:8081/api/local/probe-host", map[string]any{
		"endpoint":       endpoint,
		"trust_host_key": true, // Even with trust_host_key=true, changed keys MUST reject!
	})
	if mitmRec.Code != http.StatusConflict || !strings.Contains(mitmRec.Body.String(), "REMOTE HOST IDENTIFICATION HAS CHANGED") {
		t.Fatalf("expected 409 Host Key Changed MITM alert, got %d %s", mitmRec.Code, mitmRec.Body.String())
	}
}

func TestPeerOnboardingSSHLoopbackAndClientForwardEndToEnd(t *testing.T) {
	mockSSH := buildMockSSHBinary(t)
	t.Setenv("GOSSIP_SSH_BIN", mockSSH)

	hostStore, _, projA, credA, _ := setup(t)
	ctx := context.Background()
	if _, err := hostStore.PublishPlan(ctx, projA.ID, credA.Token, "# SSH Tunnel Plan\n\nVerified via mock OpenSSH -L + manual verification code", 1); err != nil {
		t.Fatalf("PublishPlan(projA): %v", err)
	}
	projB, _, err := hostStore.CreateProject(ctx, "SSH Isolated Project B", "Owner")
	if err != nil {
		t.Fatalf("CreateProject(projB): %v", err)
	}

	hostReg, err := coord.OpenDeviceRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("OpenDeviceRegistry(host): %v", err)
	}
	t.Cleanup(func() { _ = hostReg.Close() })
	pm, err := coord.NewPeerRuntime(hostStore, t.TempDir())
	if err != nil {
		t.Fatalf("NewPeerRuntime: %v", err)
	}
	t.Cleanup(func() { _ = pm.Close() })

	hostWeb := coord.NewWebHandlerWithRegistry(hostStore, hostReg, "default", hostStore.InstanceID())
	hostWeb.SetPeerRuntime(pm)

	sshLocalPort := reserveFreeTCPPort(t)
	cfgRec := doLoopbackJSON(
		t,
		hostWeb,
		http.MethodPost,
		"http://127.0.0.1:8081/api/local/peer-access",
		map[string]any{
			"project_id":     projA.ID,
			"ssh_enabled":    true,
			"ssh_mode":       "direct",
			"ssh_local_port": sshLocalPort,
			"ssh_target":     "alice@host.example",
			"ssh_port":       22,
		},
	)
	if cfgRec.Code != http.StatusOK {
		t.Fatalf("configure project SSH in Settings failed: %d %s", cfgRec.Code, cfgRec.Body.String())
	}
	var sshStatus coord.PeerAccessStatus
	if err := json.Unmarshal(cfgRec.Body.Bytes(), &sshStatus); err != nil {
		t.Fatalf("unmarshal sshStatus: %v", err)
	}
	if !sshStatus.SSHEnabled || sshStatus.SSHLocalAddr != fmt.Sprintf("127.0.0.1:%d", sshLocalPort) {
		t.Fatalf("expected isolated 127.0.0.1:%d SSH listener, got %+v", sshLocalPort, sshStatus)
	}

	// Cross-project isolation on SSH listener: requesting Project B MUST return 403.
	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM([]byte(sshStatus.ServerCA))
	probeClient := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: caPool}},
	}
	crossReq, _ := http.NewRequest(
		http.MethodPost,
		fmt.Sprintf("https://127.0.0.1:%d/v1/enrollment/accept", sshLocalPort),
		strings.NewReader(fmt.Sprintf(`{"project_id":%q,"id":"inv_other"}`, projB.ID)),
	)
	crossReq.Header.Set("Content-Type", "application/json")
	crossResp, err := probeClient.Do(crossReq)
	if err != nil {
		t.Fatalf("cross-project SSH listener request failed: %v", err)
	}
	_ = crossResp.Body.Close()
	if crossResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected HTTP 403 when accessing Project B through Project A SSH listener, got %d", crossResp.StatusCode)
	}

	// Owner creates encrypted SSH .gossip invite in Share.
	pass, _ := coord.GenerateInvitePassphrase()
	createRec := doLoopbackJSON(
		t,
		hostWeb,
		http.MethodPost,
		"http://127.0.0.1:8081/api/projects/"+projA.ID+"/remote-invitations",
		map[string]any{
			"operation":  "create",
			"mode":       "ssh",
			"name":       "SSH-Peer",
			"role":       "worker",
			"encrypt":    true,
			"passphrase": pass,
		},
	)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create SSH invite failed: %d %s", createRec.Code, createRec.Body.String())
	}
	var createResp struct {
		Mode          string `json:"mode"`
		SSHTarget     string `json:"ssh_target"`
		SSHRemotePort int    `json:"ssh_remote_port"`
		EncryptedFile string `json:"encrypted_file"`
	}
	_ = json.Unmarshal(createRec.Body.Bytes(), &createResp)
	if createResp.Mode != "ssh" || createResp.SSHTarget != "alice@host.example" || createResp.SSHRemotePort != sshLocalPort {
		t.Fatalf("unexpected SSH invite metadata: %+v", createResp)
	}

	// Peer joins using encrypted .gossip file: registry launches mock `ssh -L`, probes TLS, compares verification code, waits for owner Accept, and syncs plan!
	peerStore, _ := coord.Open(filepath.Join(t.TempDir(), "ssh-peer.db"))
	t.Cleanup(func() { _ = peerStore.Close() })
	peerReg, err := coord.OpenDeviceRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("OpenDeviceRegistry(peer): %v", err)
	}
	t.Cleanup(func() { _ = peerReg.Close() })
	peerWeb := coord.NewWebHandlerWithRegistry(peerStore, peerReg, "default", peerStore.InstanceID())

	completeManualCodeApproval(t, hostWeb, peerWeb, projA.ID, map[string]any{
		"encrypted_file": createResp.EncryptedFile,
		"passphrase":     pass,
	})

	planRec := doLoopbackJSON(
		t,
		peerWeb,
		http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:8081/api/projects/%s/plan?home=%s", projA.ID, hostStore.InstanceID()),
		nil,
	)
	if planRec.Code != http.StatusOK || !strings.Contains(planRec.Body.String(), "Verified via mock OpenSSH -L") {
		t.Fatalf("peer read remote plan over SSH -L tunnel failed: %d %s", planRec.Code, planRec.Body.String())
	}
}

func TestPeerOnboardingSeparateLANPortsPerProject(t *testing.T) {
	hostStore, _, projA, credA, _ := setup(t)
	ctx := context.Background()
	if _, err := hostStore.PublishPlan(ctx, projA.ID, credA.Token, "# Plan Alpha\n\nLAN Port A isolated plan", 1); err != nil {
		t.Fatalf("PublishPlan(projA): %v", err)
	}
	projB, credB, err := hostStore.CreateProject(ctx, "LAN Isolated Project B", "Owner")
	if err != nil {
		t.Fatalf("CreateProject(projB): %v", err)
	}
	if _, err := hostStore.PublishPlan(ctx, projB.ID, credB.Token, "# Plan Beta\n\nLAN Port B isolated plan", 0); err != nil {
		t.Fatalf("PublishPlan(projB): %v", err)
	}

	hostReg, err := coord.OpenDeviceRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("OpenDeviceRegistry(host): %v", err)
	}
	t.Cleanup(func() { _ = hostReg.Close() })
	pm, err := coord.NewPeerRuntime(hostStore, t.TempDir())
	if err != nil {
		t.Fatalf("NewPeerRuntime: %v", err)
	}
	t.Cleanup(func() { _ = pm.Close() })

	hostWeb := coord.NewWebHandlerWithRegistry(hostStore, hostReg, "default", hostStore.InstanceID())
	hostWeb.SetPeerRuntime(pm)

	portA := reserveFreeTCPPort(t)
	portB := reserveFreeTCPPort(t)
	for portB == portA {
		portB = reserveFreeTCPPort(t)
	}

	cfgARec := doLoopbackJSON(t, hostWeb, http.MethodPost, "http://127.0.0.1:8081/api/local/peer-access", map[string]any{
		"project_id":  projA.ID,
		"lan_enabled": true,
		"listen_port": portA,
	})
	if cfgARec.Code != http.StatusOK {
		t.Fatalf("configure LAN for projA failed: %d %s", cfgARec.Code, cfgARec.Body.String())
	}

	// Colliding LAN port on Project B MUST be rejected.
	collideRec := doLoopbackJSON(t, hostWeb, http.MethodPost, "http://127.0.0.1:8081/api/local/peer-access", map[string]any{
		"project_id":  projB.ID,
		"lan_enabled": true,
		"listen_port": portA,
	})
	if collideRec.Code != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400 for colliding LAN port across projects, got %d %s", collideRec.Code, collideRec.Body.String())
	}

	// Unconfigured Project B should auto-suggest a distinct default LAN port != portA.
	suggestRec := doLoopbackJSON(t, hostWeb, http.MethodGet, "http://127.0.0.1:8081/api/local/peer-access?project="+projB.ID, nil)
	if suggestRec.Code != http.StatusOK {
		t.Fatalf("status for unconfigured projB failed: %d %s", suggestRec.Code, suggestRec.Body.String())
	}
	var suggested coord.PeerAccessStatus
	if err := json.Unmarshal(suggestRec.Body.Bytes(), &suggested); err != nil {
		t.Fatalf("unmarshal suggested status: %v", err)
	}
	if suggested.ListenPort == 0 || suggested.ListenPort == portA {
		t.Fatalf("expected suggested LAN port != %d for projB, got %d", portA, suggested.ListenPort)
	}

	// Enable Project B on its own separate LAN portB.
	cfgBRec := doLoopbackJSON(t, hostWeb, http.MethodPost, "http://127.0.0.1:8081/api/local/peer-access", map[string]any{
		"project_id":  projB.ID,
		"lan_enabled": true,
		"listen_port": portB,
	})
	if cfgBRec.Code != http.StatusOK {
		t.Fatalf("configure LAN for projB failed: %d %s", cfgBRec.Code, cfgBRec.Body.String())
	}
	var statusB coord.PeerAccessStatus
	if err := json.Unmarshal(cfgBRec.Body.Bytes(), &statusB); err != nil {
		t.Fatalf("unmarshal statusB: %v", err)
	}
	if !statusB.LANEnabled || statusB.ListenPort != portB || len(statusB.LANListeners) != 2 {
		t.Fatalf("expected 2 isolated LAN listeners active, got %+v", statusB)
	}

	// Cross-project isolation check: projB request on portA -> 403; projA request on portB -> 403.
	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM([]byte(statusB.ServerCA))
	probeClient := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: caPool}},
	}
	for _, tc := range []struct {
		port    int
		projID  string
		portLbl string
	}{
		{port: portA, projID: projB.ID, portLbl: "portA"},
		{port: portB, projID: projA.ID, portLbl: "portB"},
	} {
		req, _ := http.NewRequest(
			http.MethodPost,
			fmt.Sprintf("https://127.0.0.1:%d/v1/enrollment/accept", tc.port),
			strings.NewReader(fmt.Sprintf(`{"project_id":%q,"id":"inv_cross"}`, tc.projID)),
		)
		req.Header.Set("Content-Type", "application/json")
		resp, err := probeClient.Do(req)
		if err != nil {
			t.Fatalf("cross-project request on %s failed: %v", tc.portLbl, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected HTTP 403 for %s on %s, got %d", tc.projID, tc.portLbl, resp.StatusCode)
		}
	}

	// Owner creates encrypted LAN invites for Project A and Project B -> peer joins both on their separate ports!
	joinAndRead := func(projID, name, expectedText string, wantPort int) {
		t.Helper()
		pass, _ := coord.GenerateInvitePassphrase()
		createRec := doLoopbackJSON(
			t,
			hostWeb,
			http.MethodPost,
			"http://127.0.0.1:8081/api/projects/"+projID+"/remote-invitations",
			map[string]any{
				"operation":  "create",
				"mode":       "lan",
				"lan_ip":     "127.0.0.1",
				"name":       name,
				"role":       "worker",
				"encrypt":    true,
				"passphrase": pass,
			},
		)
		if createRec.Code != http.StatusOK {
			t.Fatalf("create LAN invite (%s) failed: %d %s", name, createRec.Code, createRec.Body.String())
		}
		var createResp struct {
			Endpoint      string `json:"endpoint"`
			EncryptedFile string `json:"encrypted_file"`
		}
		_ = json.Unmarshal(createRec.Body.Bytes(), &createResp)
		wantEP := fmt.Sprintf("https://127.0.0.1:%d", wantPort)
		if createResp.Endpoint != wantEP {
			t.Fatalf("expected endpoint %s for %s, got %s", wantEP, projID, createResp.Endpoint)
		}

		peerStore, _ := coord.Open(filepath.Join(t.TempDir(), "lan-peer.db"))
		t.Cleanup(func() { _ = peerStore.Close() })
		peerReg, _ := coord.OpenDeviceRegistry(t.TempDir())
		t.Cleanup(func() { _ = peerReg.Close() })
		peerWeb := coord.NewWebHandlerWithRegistry(peerStore, peerReg, "default", peerStore.InstanceID())

		completeManualCodeApproval(t, hostWeb, peerWeb, projID, map[string]any{
			"encrypted_file": createResp.EncryptedFile,
			"passphrase":     pass,
		})

		planRec := doLoopbackJSON(
			t,
			peerWeb,
			http.MethodGet,
			fmt.Sprintf("http://127.0.0.1:8081/api/projects/%s/plan?home=%s", projID, hostStore.InstanceID()),
			nil,
		)
		if planRec.Code != http.StatusOK || !strings.Contains(planRec.Body.String(), expectedText) {
			t.Fatalf("peer read remote plan (%s) failed: %d %s", projID, planRec.Code, planRec.Body.String())
		}
	}

	joinAndRead(projA.ID, "Peer-Alpha", "LAN Port A isolated plan", portA)
	joinAndRead(projB.ID, "Peer-Beta", "LAN Port B isolated plan", portB)

	// Disabling Project A LAN leaves Project B active; disable_all stops everything.
	offARec := doLoopbackJSON(t, hostWeb, http.MethodPost, "http://127.0.0.1:8081/api/local/peer-access", map[string]any{
		"project_id":  projA.ID,
		"lan_enabled": false,
	})
	if offARec.Code != http.StatusOK {
		t.Fatalf("disable projA LAN failed: %d %s", offARec.Code, offARec.Body.String())
	}
	var afterOffA coord.PeerAccessStatus
	_ = json.Unmarshal(offARec.Body.Bytes(), &afterOffA)
	if !afterOffA.Enabled {
		t.Fatalf("expected host still enabled because projB LAN is active: %+v", afterOffA)
	}

	offAllRec := doLoopbackJSON(t, hostWeb, http.MethodPost, "http://127.0.0.1:8081/api/local/peer-access", map[string]any{
		"disable_all": true,
	})
	if offAllRec.Code != http.StatusOK {
		t.Fatalf("disable_all failed: %d %s", offAllRec.Code, offAllRec.Body.String())
	}
	var afterAll coord.PeerAccessStatus
	_ = json.Unmarshal(offAllRec.Body.Bytes(), &afterAll)
	if afterAll.Enabled {
		t.Fatalf("expected all listeners disabled after disable_all, got %+v", afterAll)
	}
}

func TestPeerOnboardingAutomatedFRPZeroConfigAndShareAutoStart(t *testing.T) {
	frpBin := buildTestFRPBinary(t)
	t.Setenv("GOSSIP_FRP_BIN", frpBin)

	hostStore, _, projA, ownerCredA, _ := setup(t)
	ctx := context.Background()
	if _, err := hostStore.PublishPlan(ctx, projA.ID, ownerCredA.Token, "# Automated Relay Plan A", 1); err != nil {
		t.Fatalf("PublishPlan(projA): %v", err)
	}
	projB, ownerCredB, err := hostStore.CreateProject(ctx, "Auto-FRP B", "Owner")
	if err != nil {
		t.Fatalf("CreateProject(projB): %v", err)
	}
	if _, err := hostStore.PublishPlan(ctx, projB.ID, ownerCredB.Token, "# Automated Relay Plan B", 0); err != nil {
		t.Fatalf("PublishPlan(projB): %v", err)
	}

	hostReg, err := coord.OpenDeviceRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("OpenDeviceRegistry: %v", err)
	}
	t.Cleanup(func() { _ = hostReg.Close() })

	cfgDir := t.TempDir()
	pm, err := coord.NewPeerRuntime(hostStore, cfgDir)
	if err != nil {
		t.Fatalf("NewPeerRuntime: %v", err)
	}
	t.Cleanup(func() { _ = pm.Close() })

	hostWeb := coord.NewWebHandlerWithRegistry(hostStore, hostReg, "default", hostStore.InstanceID())
	hostWeb.SetPeerRuntime(pm)

	// 1. VPS helper endpoint auto-generates & persists CSPRNG token without exposing it via status GET.
	cmdRec := doLoopbackJSON(t, hostWeb, http.MethodPost, "http://127.0.0.1:8081/api/local/frp-relay-command", map[string]any{})
	if cmdRec.Code != http.StatusOK {
		t.Fatalf("frp-relay-command failed: %d %s", cmdRec.Code, cmdRec.Body.String())
	}
	var cmdResp struct {
		Command    string `json:"command"`
		ServerPort int    `json:"server_port"`
	}
	_ = json.Unmarshal(cmdRec.Body.Bytes(), &cmdResp)
	if !strings.Contains(cmdResp.Command, "gossip-frp server --port 7000 --token '") {
		t.Fatalf("unexpected relay command format: %q", cmdResp.Command)
	}
	tokStart := strings.Index(cmdResp.Command, "--token '") + len("--token '")
	tokEnd := strings.LastIndex(cmdResp.Command, "'")
	rawTok := cmdResp.Command[tokStart:tokEnd]
	if len(rawTok) < 20 {
		t.Fatalf("expected high-entropy CSPRNG token, got len=%d", len(rawTok))
	}

	stRec := doLoopbackJSON(t, hostWeb, http.MethodGet, "http://127.0.0.1:8081/api/local/peer-access?project="+projA.ID, nil)
	if stRec.Code != http.StatusOK {
		t.Fatalf("GET peer-access failed: %d", stRec.Code)
	}
	if strings.Contains(stRec.Body.String(), rawTok) {
		t.Fatal("security leak: raw FRP token appeared in peer-access status JSON")
	}

	// Verify peer-access.json keeps 0600 permissions.
	info, err := os.Stat(filepath.Join(cfgDir, "peer-access.json"))
	if err != nil {
		t.Fatalf("stat peer-access.json: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("expected peer-access.json mode 0600, got %o", info.Mode().Perm())
	}

	// 2. Enable Project A with built-in local relay and NO manual token supplied.
	srvPort := reserveFreeTCPPort(t)
	remPortA := reserveFreeTCPPort(t)
	enableARec := doLoopbackJSON(t, hostWeb, http.MethodPost, "http://127.0.0.1:8081/api/local/peer-access", map[string]any{
		"project_id":      projA.ID,
		"auto_setup":      true,
		"frp_local_relay": true,
		"frp_enabled":     true,
		"frp_server":      "127.0.0.1",
		"frp_server_port": srvPort,
		"frp_remote_port": remPortA,
	})
	if enableARec.Code != http.StatusOK {
		t.Fatalf("auto-enable Project A with local relay failed: %d %s", enableARec.Code, enableARec.Body.String())
	}
	var stA coord.PeerAccessStatus
	_ = json.Unmarshal(enableARec.Body.Bytes(), &stA)
	if !stA.FRPEnabled || !stA.FRPLocalRelayEnabled || stA.FRPRemotePort != remPortA {
		t.Fatalf("unexpected Project A automated FRP status: %+v", stA)
	}
	waitForTLSForwardReady(t, fmt.Sprintf("127.0.0.1:%d", remPortA), 8*time.Second)

	// 3. Project B status inherits host relay defaults and pre-suggests a different free port.
	stBRec := doLoopbackJSON(t, hostWeb, http.MethodGet, "http://127.0.0.1:8081/api/remote-access?project="+projB.ID, nil)
	if stBRec.Code != http.StatusOK {
		t.Fatalf("GET remote-access for projB failed: %d", stBRec.Code)
	}
	var stB coord.PeerAccessStatus
	_ = json.Unmarshal(stBRec.Body.Bytes(), &stB)
	if stB.FRPEnabled || stB.FRPRelayServer != "127.0.0.1" || stB.FRPRemotePort == remPortA || !stB.FRPTokenConfigured {
		t.Fatalf("unexpected Project B inherited FRP defaults: %+v", stB)
	}

	// 4. Share Project B directly in "frp" mode with ZERO Settings configuration for Project B!
	pass, err := coord.GenerateInvitePassphrase()
	if err != nil {
		t.Fatalf("GenerateInvitePassphrase: %v", err)
	}
	createBRec := doLoopbackJSON(
		t,
		hostWeb,
		http.MethodPost,
		"http://127.0.0.1:8081/api/projects/"+projB.ID+"/remote-invitations",
		map[string]any{
			"operation":  "create",
			"mode":       "frp",
			"name":       "Auto-Peer-B",
			"role":       "worker",
			"encrypt":    true,
			"passphrase": pass,
		},
	)
	if createBRec.Code != http.StatusOK {
		t.Fatalf("auto-start Share FRP invite for projB failed: %d %s", createBRec.Code, createBRec.Body.String())
	}
	var createBResp struct {
		Endpoint      string `json:"endpoint"`
		EncryptedFile string `json:"encrypted_file"`
	}
	_ = json.Unmarshal(createBRec.Body.Bytes(), &createBResp)
	_, portB, err := net.SplitHostPort(strings.TrimPrefix(createBResp.Endpoint, "https://"))
	if err != nil || portB == fmt.Sprintf("%d", remPortA) {
		t.Fatalf("expected Project B to auto-allocate a separate port != %d, got endpoint %q (err=%v)", remPortA, createBResp.Endpoint, err)
	}

	waitForTLSForwardReady(t, strings.TrimPrefix(createBResp.Endpoint, "https://"), 8*time.Second)

	peerStore, _ := coord.Open(filepath.Join(t.TempDir(), "auto-frp-peer.db"))
	t.Cleanup(func() { _ = peerStore.Close() })
	peerReg, _ := coord.OpenDeviceRegistry(t.TempDir())
	t.Cleanup(func() { _ = peerReg.Close() })
	peerWeb := coord.NewWebHandlerWithRegistry(peerStore, peerReg, "default", peerStore.InstanceID())

	completeManualCodeApproval(t, hostWeb, peerWeb, projB.ID, map[string]any{
		"encrypted_file": createBResp.EncryptedFile,
		"passphrase":     pass,
	})

	planBRec := doLoopbackJSON(
		t,
		peerWeb,
		http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:8081/api/projects/%s/plan?home=%s", projB.ID, hostStore.InstanceID()),
		nil,
	)
	if planBRec.Code != http.StatusOK || !strings.Contains(planBRec.Body.String(), "Automated Relay Plan B") {
		t.Fatalf("peer read projB plan via auto-started FRP failed: %d %s", planBRec.Code, planBRec.Body.String())
	}

	// 5. disable_all cleanly shuts down both local relay and all project tunnels.
	offAll := doLoopbackJSON(t, hostWeb, http.MethodPost, "http://127.0.0.1:8081/api/local/peer-access", map[string]any{
		"disable_all": true,
	})
	if offAll.Code != http.StatusOK {
		t.Fatalf("disable_all failed: %d %s", offAll.Code, offAll.Body.String())
	}
	var afterOff coord.PeerAccessStatus
	_ = json.Unmarshal(offAll.Body.Bytes(), &afterOff)
	if afterOff.Enabled || afterOff.FRPLocalRelayEnabled || afterOff.FRPLocalRelayState != "disabled" {
		t.Fatalf("expected all tunnels and local relay disabled, got %+v", afterOff)
	}
}

func TestPeerPortDefaultsAndCustomization(t *testing.T) {
	if coord.DefaultDashboardPort != 47281 {
		t.Fatalf("expected DefaultDashboardPort=47281, got %d", coord.DefaultDashboardPort)
	}
	if coord.DefaultPeerListenPort != 47282 {
		t.Fatalf("expected DefaultPeerListenPort=47282, got %d", coord.DefaultPeerListenPort)
	}

	hostStore, _, proj, _, _ := setup(t)
	hostDir := t.TempDir()
	hostReg, err := coord.OpenDeviceRegistry(hostDir)
	if err != nil {
		t.Fatalf("open host device registry: %v", err)
	}
	defer func() { _ = hostReg.Close() }()

	rt, err := coord.NewPeerRuntime(hostStore, hostDir)
	if err != nil {
		t.Fatalf("NewPeerRuntime: %v", err)
	}
	defer func() { _ = rt.Close() }()
	rt.SetDashboardPort(coord.DefaultDashboardPort)

	var switchedPorts []int
	failSwitch := false
	rt.SetDashboardPortChanger(func(port int) (string, error) {
		if failSwitch {
			return "", fmt.Errorf("simulated bind failure on %d", port)
		}
		switchedPorts = append(switchedPorts, port)
		return fmt.Sprintf("http://127.0.0.1:%d", port), nil
	})

	hostWeb := coord.NewWebHandlerWithRegistry(hostStore, hostReg, "default", hostStore.InstanceID())
	hostWeb.SetPeerRuntime(rt)

	// 1. Default status exposes dedicated non-dev ports (47281 & 47282), never 8081 or 8443.
	getRec := doLoopbackJSON(t, hostWeb, http.MethodGet, "http://127.0.0.1:47281/api/local/peer-access?project="+proj.ID, nil)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get peer-access status failed: %d %s", getRec.Code, getRec.Body.String())
	}
	var initial coord.PeerAccessStatus
	if err := json.Unmarshal(getRec.Body.Bytes(), &initial); err != nil {
		t.Fatalf("decode initial status: %v", err)
	}
	if initial.DashboardPort != 47281 || initial.ListenPort != 47282 {
		t.Fatalf("expected default ports 47281/47282, got dashboard=%d listen=%d", initial.DashboardPort, initial.ListenPort)
	}

	// 2. Customize dashboard port, LAN mTLS port, FRP control port, and FRP public remote port.
	customDash := reserveFreeTCPPort(t)
	customLAN := reserveFreeTCPPort(t)
	customFRPSrv := reserveFreeTCPPort(t)
	customFRPRem := reserveFreeTCPPort(t)
	customRec := doLoopbackJSON(t, hostWeb, http.MethodPost, "http://127.0.0.1:47281/api/local/peer-access", map[string]any{
		"project_id":      proj.ID,
		"dashboard_port":  customDash,
		"lan_enabled":     true,
		"listen_port":     customLAN,
		"frp_server_port": customFRPSrv,
		"frp_remote_port": customFRPRem,
	})
	if customRec.Code != http.StatusOK {
		t.Fatalf("customize ports failed: %d %s", customRec.Code, customRec.Body.String())
	}
	var updated coord.PeerAccessStatus
	if err := json.Unmarshal(customRec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode custom status: %v", err)
	}
	expectedRedirect := fmt.Sprintf("http://127.0.0.1:%d", customDash)
	if updated.DashboardPort != customDash || updated.DashboardRedirectURL != expectedRedirect {
		t.Fatalf("expected dashboard_port=%d redirect=%q, got %d %q", customDash, expectedRedirect, updated.DashboardPort, updated.DashboardRedirectURL)
	}
	if updated.ListenPort != customLAN || updated.FRPServerPort != customFRPSrv || updated.FRPRemotePort != customFRPRem {
		t.Fatalf("expected custom LAN/FRP ports (%d, %d, %d), got (%d, %d, %d)",
			customLAN, customFRPSrv, customFRPRem, updated.ListenPort, updated.FRPServerPort, updated.FRPRemotePort)
	}
	if len(switchedPorts) != 1 || switchedPorts[0] != customDash {
		t.Fatalf("expected SetDashboardPortChanger callback for %d, got %v", customDash, switchedPorts)
	}
	if gotSaved := coord.LoadSavedDashboardPort(hostDir); gotSaved != customDash {
		t.Fatalf("expected LoadSavedDashboardPort=%d, got %d", customDash, gotSaved)
	}

	// 3. Conflict check: LAN mTLS port cannot collide with the dashboard port.
	conflictRec := doLoopbackJSON(t, hostWeb, http.MethodPost, "http://127.0.0.1:47281/api/local/peer-access", map[string]any{
		"project_id":  proj.ID,
		"lan_enabled": true,
		"listen_port": customDash,
	})
	if conflictRec.Code == http.StatusOK {
		t.Fatalf("expected conflict error when listen_port == dashboard_port (%d)", customDash)
	}

	// 4. Failed live dashboard port switch must reject cleanly without corrupting saved port.
	failSwitch = true
	badDash := reserveFreeTCPPort(t)
	failRec := doLoopbackJSON(t, hostWeb, http.MethodPost, "http://127.0.0.1:47281/api/local/peer-access", map[string]any{
		"project_id":     proj.ID,
		"dashboard_port": badDash,
	})
	if failRec.Code == http.StatusOK {
		t.Fatalf("expected simulated dashboard bind failure to return non-200")
	}
	if gotSaved := coord.LoadSavedDashboardPort(hostDir); gotSaved != customDash {
		t.Fatalf("expected saved dashboard port to remain %d after failed switch, got %d", customDash, gotSaved)
	}
}

func TestPeerInviteFileAndSixDigitPINZeroConfig(t *testing.T) {
	pin, err := coord.GenerateInvitePIN()
	if err != nil {
		t.Fatalf("GenerateInvitePIN: %v", err)
	}
	if len(pin) != 6 {
		t.Fatalf("expected 6-digit PIN, got %q", pin)
	}
	for i := 0; i < len(pin); i++ {
		if pin[i] < '0' || pin[i] > '9' {
			t.Fatalf("expected numeric PIN digit, got %q", pin)
		}
	}

	// Short 5-digit PIN rejected; 6-digit PIN succeeds.
	hostDir := t.TempDir()
	hostStore, _, proj, ownerCred, _ := setup(t)
	ctx := context.Background()
	if _, err := hostStore.PublishPlan(ctx, proj.ID, ownerCred.Token, "# PIN Plan", 1); err != nil {
		t.Fatalf("PublishPlan: %v", err)
	}
	frpBin := buildTestFRPBinary(t)
	t.Setenv("GOSSIP_FRP_BIN", frpBin)

	hostReg, err := coord.OpenDeviceRegistry(hostDir)
	if err != nil {
		t.Fatalf("OpenDeviceRegistry(host): %v", err)
	}
	t.Cleanup(func() { _ = hostReg.Close() })

	pm, err := coord.NewPeerRuntime(hostStore, hostDir)
	if err != nil {
		t.Fatalf("NewPeerRuntime: %v", err)
	}
	t.Cleanup(func() { _ = pm.Close() })

	// Pre-assign free ephemeral FRP server & remote ports without configuring a server host first.
	srvPort := reserveFreeTCPPort(t)
	remPort := reserveFreeTCPPort(t)
	if _, err := pm.Configure(ctx, coord.PeerAccessUpdate{
		ProjectID:     proj.ID,
		FRPServerPort: &srvPort,
		FRPRemotePort: &remPort,
	}); err != nil {
		t.Fatalf("configure ports: %v", err)
	}

	hostWeb := coord.NewWebHandlerWithRegistry(hostStore, hostReg, "default", hostStore.InstanceID())
	hostWeb.SetPeerRuntime(pm)

	// Zero-config Share in FRP mode with blank name & blank passphrase -> defaults to "Remote peer" + 6-digit PIN + auto local relay.
	tooShort := doLoopbackJSON(t, hostWeb, http.MethodPost, "http://127.0.0.1:47281/api/projects/"+proj.ID+"/remote-invitations", map[string]any{
		"operation":  "create",
		"mode":       "frp",
		"encrypt":    true,
		"passphrase": "12345",
	})
	if tooShort.Code == http.StatusOK {
		t.Fatalf("expected 5-digit PIN to be rejected")
	}

	createRec := doLoopbackJSON(t, hostWeb, http.MethodPost, "http://127.0.0.1:47281/api/projects/"+proj.ID+"/remote-invitations", map[string]any{
		"operation":        "create",
		"mode":             "frp",
		"auto_local_relay": true,
		"encrypt":          true,
	})
	if createRec.Code != http.StatusOK {
		t.Fatalf("zero-config file+PIN invite create failed: %d %s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		Invitation    coord.RemoteInvitation `json:"invitation"`
		EncryptedFile string                 `json:"encrypted_file"`
		Passphrase    string                 `json:"passphrase"`
		PIN           string                 `json:"pin"`
		Endpoint      string                 `json:"endpoint"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Invitation.Name != "Remote peer" {
		t.Fatalf("expected default name 'Remote peer', got %q", created.Invitation.Name)
	}
	if len(created.PIN) != 6 || created.PIN != created.Passphrase {
		t.Fatalf("expected 6-digit auto PIN, got pin=%q passphrase=%q", created.PIN, created.Passphrase)
	}
	if _, err := coord.DecryptEnrollmentBundle([]byte(created.EncryptedFile), "000000"); err == nil && created.PIN != "000000" {
		t.Fatal("wrong 6-digit PIN unexpectedly decrypted bundle")
	}

	waitForTLSForwardReady(t, fmt.Sprintf("127.0.0.1:%d", remPort), 8*time.Second)

	peerStore, err := coord.Open(filepath.Join(t.TempDir(), "peer.db"))
	if err != nil {
		t.Fatalf("Open(peerStore): %v", err)
	}
	t.Cleanup(func() { _ = peerStore.Close() })
	peerReg, err := coord.OpenDeviceRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("OpenDeviceRegistry(peer): %v", err)
	}
	t.Cleanup(func() { _ = peerReg.Close() })
	peerWeb := coord.NewWebHandlerWithRegistry(peerStore, peerReg, "default", peerStore.InstanceID())

	completeManualCodeApproval(t, hostWeb, peerWeb, proj.ID, map[string]any{
		"encrypted_file": created.EncryptedFile,
		"passphrase":     created.PIN,
	})

	planRec := doLoopbackJSON(
		t,
		peerWeb,
		http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:47281/api/projects/%s/plan?home=%s", proj.ID, hostStore.InstanceID()),
		nil,
	)
	if planRec.Code != http.StatusOK || !strings.Contains(planRec.Body.String(), "PIN Plan") {
		t.Fatalf("peer read plan after file+PIN join failed: %d %s", planRec.Code, planRec.Body.String())
	}
}
