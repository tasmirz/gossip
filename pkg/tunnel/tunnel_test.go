package tunnel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tasmirz/gossip/pkg/state"
)

func TestGenerateFRPCTOML(t *testing.T) {
	cfg := FRPClientConfig{
		ServerAddr:     "tunnel.example.com",
		ServerPort:     7000,
		AuthToken:      "my-secret-token",
		ProxyName:      "gossip-mesh-web",
		ProxyType:      "http",
		LocalIP:        "127.0.0.1",
		LocalPort:      8080,
		CustomDomains:  []string{"gossip.example.com"},
		HTTPUser:       "meshadmin",
		HTTPPassword:   "SuperSecretPass123",
		UseEncryption:  true,
		UseCompression: true,
	}

	toml := GenerateFRPCTOML(cfg)
	if !strings.Contains(toml, `serverAddr = "tunnel.example.com"`) {
		t.Errorf("Missing serverAddr in TOML: %s", toml)
	}
	if !strings.Contains(toml, `auth.token = "my-secret-token"`) {
		t.Errorf("Missing auth.token in TOML: %s", toml)
	}
	if !strings.Contains(toml, `httpUser = "meshadmin"`) {
		t.Errorf("Missing httpUser in TOML: %s", toml)
	}
	if !strings.Contains(toml, `httpPassword = "SuperSecretPass123"`) {
		t.Errorf("Missing httpPassword in TOML: %s", toml)
	}
	if !strings.Contains(toml, `customDomains = ["gossip.example.com"]`) {
		t.Errorf("Missing customDomains in TOML: %s", toml)
	}
}

func TestGenerateFRPCINI(t *testing.T) {
	cfg := FRPClientConfig{
		ServerAddr:     "192.168.1.10",
		ServerPort:     7000,
		AuthToken:      "tok-xyz",
		ProxyName:      "gossip-ui",
		ProxyType:      "http",
		LocalIP:        "127.0.0.1",
		LocalPort:      8080,
		CustomDomains:  []string{"local.gossip.mesh"},
		HTTPUser:       "user1",
		HTTPPassword:   "pass12345",
		UseEncryption:  true,
		UseCompression: false,
	}

	ini := GenerateFRPCINI(cfg)
	if !strings.Contains(ini, "server_addr = 192.168.1.10") {
		t.Errorf("Missing server_addr in INI: %s", ini)
	}
	if !strings.Contains(ini, "http_user = user1") {
		t.Errorf("Missing http_user in INI: %s", ini)
	}
	if !strings.Contains(ini, "http_pwd = pass12345") {
		t.Errorf("Missing http_pwd in INI: %s", ini)
	}
}

func TestGenerateFRPSConfigs(t *testing.T) {
	srvCfg := FRPServerConfig{
		BindPort:      7000,
		AuthToken:     "srv-token",
		VhostHTTPPort: 80,
		DashboardPort: 7500,
		DashboardUser: "admin",
		DashboardPwd:  "adminpass",
	}

	toml := GenerateFRPSTOML(srvCfg)
	if !strings.Contains(toml, "bindPort = 7000") || !strings.Contains(toml, `auth.token = "srv-token"`) {
		t.Errorf("Bad server TOML: %s", toml)
	}

	ini := GenerateFRPSINI(srvCfg)
	if !strings.Contains(ini, "bind_port = 7000") || !strings.Contains(ini, "token = srv-token") {
		t.Errorf("Bad server INI: %s", ini)
	}
}

func TestValidateCredentials(t *testing.T) {
	cases := []struct {
		user    string
		pass    string
		wantErr bool
	}{
		{"", "secretpass", true},
		{"ab", "secretpass", true},
		{"validuser", "12345", true}, // pass too short (< 6)
		{"user:name", "secretpass", true},
		{"valid_user", "ValidPass123", false},
	}

	for _, c := range cases {
		err := ValidateCredentials(c.user, c.pass)
		if (err != nil) != c.wantErr {
			t.Errorf("ValidateCredentials(%q, %q) error = %v, wantErr %v", c.user, c.pass, err, c.wantErr)
		}
	}
}

func TestMaskSecret(t *testing.T) {
	if got := MaskSecret(""); got != "" {
		t.Errorf("expected empty string, got %s", got)
	}
	if got := MaskSecret("1234"); got != "********" {
		t.Errorf("expected 8 stars, got %s", got)
	}
	masked := MaskSecret("SuperSecretPassword")
	if !strings.HasPrefix(masked, "Sup...") || !strings.HasSuffix(masked, "******") {
		t.Errorf("unexpected masked output: %s", masked)
	}
}

func TestSecureCompare(t *testing.T) {
	if !SecureCompare("correcthorsebatterystaple", "correcthorsebatterystaple") {
		t.Error("identical strings should compare true")
	}
	if SecureCompare("correct", "wrong") {
		t.Error("different strings should compare false")
	}
}

func TestTunnelStateSaveLoad(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-tunnel-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	state := TunnelState{
		Active:         true,
		Type:           "frp",
		ServerAddr:     "tunnel.host.net:7000",
		RemoteEndpoint: "http://gossip.host.net",
		LocalIP:        "127.0.0.1",
		LocalPort:      8080,
		AuthEnabled:    true,
		AuthUser:       "meshadmin",
		AuthPassMasked: MaskSecret("mypassword123"),
		StartedAt:      time.Now(),
	}

	if err := SaveTunnelState(tmpDir, state); err != nil {
		t.Fatalf("SaveTunnelState failed: %v", err)
	}

	fi, err := os.Stat(filepath.Join(tmpDir, TunnelStateFileName))
	if err != nil {
		t.Fatalf("Stat on tunnel.json failed: %v", err)
	}
	// Check file mode permissions on unix
	if fi.Mode().Perm() != 0600 {
		t.Logf("Notice: tunnel.json perm is %v (expected 0600 on POSIX)", fi.Mode().Perm())
	}

	loaded, err := LoadTunnelState(tmpDir)
	if err != nil {
		t.Fatalf("LoadTunnelState failed: %v", err)
	}
	if !loaded.Active || loaded.AuthUser != "meshadmin" || loaded.RemoteEndpoint != "http://gossip.host.net" {
		t.Errorf("Loaded state mismatch: %+v", loaded)
	}

	if err := ClearTunnelState(tmpDir); err != nil {
		t.Fatalf("ClearTunnelState failed: %v", err)
	}

	afterClear, err := LoadTunnelState(tmpDir)
	if err != nil {
		t.Fatalf("LoadTunnelState after clear failed: %v", err)
	}
	if afterClear.Active {
		t.Error("Expected inactive tunnel state after clear")
	}
}

func TestComputeDisclosure(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-disclosure-test-*")
	if err != nil {
		t.Fatalf("temp dir err: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := state.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("store init err: %v", err)
	}

	// 1. Initial local-only state
	disc := ComputeDisclosure(store)
	if disc.TunnelActive {
		t.Error("expected inactive tunnel initially")
	}
	if !strings.Contains(disc.DisclosureNotice, "LOCAL ONLY") {
		t.Errorf("expected LOCAL ONLY notice, got %s", disc.DisclosureNotice)
	}
	if pill := disc.ShortPill(); pill != "[LOCAL ONLY]" {
		t.Errorf("expected [LOCAL ONLY] pill, got %s", pill)
	}

	// 2. Add an active protected tunnel
	tState := TunnelState{
		Active:         true,
		Type:           "frp",
		ServerAddr:     "tunnel.mesh.cloud:7000",
		RemoteEndpoint: "frp://tunnel.mesh.cloud:7000",
		LocalIP:        "127.0.0.1",
		LocalPort:      8080,
		AuthEnabled:    true,
		AuthUser:       "admin",
	}
	if err := SaveTunnelState(tmpDir, tState); err != nil {
		t.Fatalf("failed to save tunnel: %v", err)
	}

	// 3. Register a remote peer agent
	err = store.RegisterAgent(state.AgentSession{
		ID:             "remote-agent-1",
		Role:           "security_specialist",
		IsRemote:       true,
		RemoteEndpoint: "frp://tunnel.mesh.cloud:7000",
		EndpointType:   "frp",
		Status:         "active",
	})
	if err != nil {
		t.Fatalf("failed to register agent: %v", err)
	}

	// 4. Recompute disclosure
	disc2 := ComputeDisclosure(store)
	if !disc2.TunnelActive {
		t.Error("expected active tunnel")
	}
	if !disc2.AuthProtected {
		t.Error("expected AuthProtected == true")
	}
	if disc2.TotalRemoteCount != 1 {
		t.Errorf("expected 1 remote peer, got %d", disc2.TotalRemoteCount)
	}
	if !strings.Contains(disc2.DisclosureNotice, "REMOTE MESH ACTIVE") {
		t.Errorf("expected REMOTE MESH ACTIVE notice, got %s", disc2.DisclosureNotice)
	}
	banner := disc2.MarkdownBanner()
	if !strings.Contains(banner, "remote-agent-1") || !strings.Contains(banner, "frp://tunnel.mesh.cloud:7000") {
		t.Errorf("bad markdown banner: %s", banner)
	}
}
