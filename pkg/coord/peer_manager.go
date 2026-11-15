package coord

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tasmirz/gossip/pkg/tunnel"
)

const (
	// DefaultDashboardPort is Gossip's dedicated loopback dashboard HTTP port (avoiding common dev ports 8080/8081).
	DefaultDashboardPort = 47281
	// DefaultPeerListenPort is Gossip's dedicated LAN mTLS base port (avoiding common dev port 8443).
	DefaultPeerListenPort = 47282

	defaultPeerListenPort = DefaultPeerListenPort
	defaultFRPServerPort  = 7000
	defaultFRPRemotePort  = 18443
)

// ProjectFRPConfig persists one project's isolated FRP tunnel configuration.
type ProjectFRPConfig struct {
	Enabled    bool   `json:"enabled"`
	Server     string `json:"server,omitempty"`
	ServerPort int    `json:"server_port,omitempty"`
	RemotePort int    `json:"remote_port,omitempty"`
	Token      string `json:"token,omitempty"`
}

// ProjectSSHConfig persists one project's isolated OpenSSH peer tunnel/listener configuration.
type ProjectSSHConfig struct {
	Enabled      bool   `json:"enabled"`
	Mode         string `json:"mode,omitempty"`         // "direct" (loopback + peer ssh -L) or "relay" (host ssh -R)
	LocalPort    int    `json:"local_port,omitempty"`   // loopback port on host for direct ssh -L
	Target       string `json:"target,omitempty"`       // user@host hint (direct) or SSH relay target (relay)
	SSHPort      int    `json:"ssh_port,omitempty"`     // OpenSSH server port (default 22)
	RemotePort   int    `json:"remote_port,omitempty"`  // remote forwarded port on SSH relay (default 19443)
	IdentityFile string `json:"identity_file,omitempty"`
}

// FRPRelayConfig persists host-wide default FRP relay details so new projects and
// one-click Share -> Remote (FRP) inherit the relay host and CSPRNG token automatically.
type FRPRelayConfig struct {
	Server            string `json:"server,omitempty"`
	ServerPort        int    `json:"server_port,omitempty"`
	Token             string `json:"token,omitempty"`
	LocalServerActive bool   `json:"local_server_active,omitempty"`
}

// ProjectLANConfig persists a single project's separate-port LAN mTLS listener configuration.
type ProjectLANConfig struct {
	Enabled    bool `json:"enabled"`
	ListenPort int  `json:"listen_port"`
}

// PeerAccessConfig persists local host LAN preferences and per-project LAN/FRP/SSH settings.
// All remote modes stay disabled by default until explicitly enabled on the home host.
type PeerAccessConfig struct {
	DashboardPort int                         `json:"dashboard_port,omitempty"`
	LANEnabled    bool                        `json:"lan_enabled"`
	ListenPort    int                         `json:"listen_port,omitempty"`
	FRPRelay      FRPRelayConfig              `json:"frp_relay,omitempty"`
	ProjectLAN    map[string]ProjectLANConfig `json:"project_lan,omitempty"`
	ProjectFRP    map[string]ProjectFRPConfig `json:"project_frp,omitempty"`
	ProjectSSH    map[string]ProjectSSHConfig `json:"project_ssh,omitempty"`
}

// PeerAccessUpdate mutates local peer-access configuration from loopback Settings.
type PeerAccessUpdate struct {
	ProjectID       string  `json:"project_id,omitempty"`
	DisableAll      bool    `json:"disable_all,omitempty"`
	AutoSetup       bool    `json:"auto_setup,omitempty"`
	DashboardPort   *int    `json:"dashboard_port,omitempty"`
	LANEnabled      *bool   `json:"lan_enabled,omitempty"`
	ListenPort      *int    `json:"listen_port,omitempty"`
	FRPLocalRelay   *bool   `json:"frp_local_relay,omitempty"`
	FRPAutoGenerate bool    `json:"frp_auto_generate,omitempty"`
	FRPEnabled      *bool   `json:"frp_enabled,omitempty"`
	FRPServer       *string `json:"frp_server,omitempty"`
	FRPServerPort   *int    `json:"frp_server_port,omitempty"`
	FRPRemotePort   *int    `json:"frp_remote_port,omitempty"`
	FRPToken        *string `json:"frp_token,omitempty"`
	SSHEnabled      *bool   `json:"ssh_enabled,omitempty"`
	SSHMode         *string `json:"ssh_mode,omitempty"`
	SSHLocalPort    *int    `json:"ssh_local_port,omitempty"`
	SSHTarget       *string `json:"ssh_target,omitempty"`
	SSHPort         *int    `json:"ssh_port,omitempty"`
	SSHRemotePort   *int    `json:"ssh_remote_port,omitempty"`
	SSHIdentityFile *string `json:"ssh_identity_file,omitempty"`
}

// ProjectFRPStatus describes one project's sanitized FRP tunnel configuration/state.
type ProjectFRPStatus struct {
	ProjectID       string `json:"project_id"`
	Enabled         bool   `json:"enabled"`
	Server          string `json:"server,omitempty"`
	ServerPort      int    `json:"server_port"`
	RemotePort      int    `json:"remote_port"`
	TokenConfigured bool   `json:"token_configured"`
	TokenMasked     string `json:"token_masked,omitempty"`
	Endpoint        string `json:"endpoint,omitempty"`
	LocalAddr       string `json:"local_addr,omitempty"`
	State           string `json:"state"`
	Error           string `json:"error,omitempty"`
}

// ProjectSSHStatus describes one project's sanitized SSH listener/tunnel state.
type ProjectSSHStatus struct {
	ProjectID    string `json:"project_id"`
	Enabled      bool   `json:"enabled"`
	Mode         string `json:"mode"`
	LocalPort    int    `json:"local_port"`
	Target       string `json:"target,omitempty"`
	SSHPort      int    `json:"ssh_port"`
	RemotePort   int    `json:"remote_port"`
	IdentityFile string `json:"identity_file,omitempty"`
	Endpoint     string `json:"endpoint,omitempty"`
	LocalAddr    string `json:"local_addr,omitempty"`
	State        string `json:"state"`
	Error        string `json:"error,omitempty"`
}

// InviteEndpointInfo summarizes resolved transport details for creating a peer invitation.
type InviteEndpointInfo struct {
	Mode            string `json:"mode"`
	Endpoint        string `json:"endpoint"`
	ServerCA        string `json:"server_ca"`
	HostFingerprint string `json:"host_fingerprint"`
	SSHTarget       string `json:"ssh_target,omitempty"`
	SSHPort         int    `json:"ssh_port,omitempty"`
	SSHRemotePort   int    `json:"ssh_remote_port,omitempty"`
}

// ProjectLANStatus describes one project's separate-port LAN mTLS listener state.
type ProjectLANStatus struct {
	ProjectID  string   `json:"project_id"`
	Enabled    bool     `json:"enabled"`
	ListenPort int      `json:"listen_port"`
	ActivePort int      `json:"active_port,omitempty"`
	LocalAddr  string   `json:"local_addr,omitempty"`
	Endpoints  []string `json:"endpoints,omitempty"`
	State      string   `json:"state"`
	Error      string   `json:"error,omitempty"`
}

// PeerAccessStatus reports the sanitized live state of local LAN & per-project FRP/SSH access.
// Private keys and raw FRP tokens are never included.
type PeerAccessStatus struct {
	DashboardPort        int                `json:"dashboard_port"`
	DashboardRedirectURL string             `json:"dashboard_redirect_url,omitempty"`
	Enabled              bool               `json:"enabled"`
	LANEnabled         bool               `json:"lan_enabled"`
	ListenPort         int                `json:"listen_port"`
	ActivePort         int                `json:"active_port,omitempty"`
	ActiveListenAddr   string             `json:"active_listen_addr,omitempty"`
	DetectedLANIPs     []string           `json:"detected_lan_ips"`
	LANEndpoints       []string           `json:"lan_endpoints"`
	LANListeners       []ProjectLANStatus `json:"lan_listeners"`
	ProjectID          string             `json:"project_id,omitempty"`
	FRPEnabled         bool               `json:"frp_enabled"`
	FRPServer          string             `json:"frp_server,omitempty"`
	FRPServerPort      int                `json:"frp_server_port"`
	FRPRemotePort      int                `json:"frp_remote_port"`
	FRPTokenConfigured bool               `json:"frp_token_configured"`
	FRPTokenMasked     string             `json:"frp_token_masked,omitempty"`
	FRPEndpoint        string             `json:"frp_endpoint,omitempty"`
	FRPLocalAddr       string             `json:"frp_local_addr,omitempty"`
	FRPBinaryAvailable bool               `json:"frp_binary_available"`
	FRPBinaryName      string             `json:"frp_binary_name,omitempty"`
	FRPState                string             `json:"frp_state"`
	FRPError                string             `json:"frp_error,omitempty"`
	FRPTunnels              []ProjectFRPStatus `json:"frp_tunnels"`
	FRPRelayServer          string             `json:"frp_relay_server,omitempty"`
	FRPRelayServerPort      int                `json:"frp_relay_server_port"`
	FRPRelayTokenConfigured bool               `json:"frp_relay_token_configured"`
	FRPRelayTokenMasked     string             `json:"frp_relay_token_masked,omitempty"`
	FRPLocalRelayEnabled    bool               `json:"frp_local_relay_enabled"`
	FRPLocalRelayState      string             `json:"frp_local_relay_state"`
	FRPLocalRelayError      string             `json:"frp_local_relay_error,omitempty"`
	SSHEnabled         bool               `json:"ssh_enabled"`
	SSHMode            string             `json:"ssh_mode"`
	SSHLocalPort       int                `json:"ssh_local_port"`
	SSHTarget          string             `json:"ssh_target,omitempty"`
	SSHPort            int                `json:"ssh_port"`
	SSHRemotePort      int                `json:"ssh_remote_port"`
	SSHIdentityFile    string             `json:"ssh_identity_file,omitempty"`
	SSHEndpoint        string             `json:"ssh_endpoint,omitempty"`
	SSHLocalAddr       string             `json:"ssh_local_addr,omitempty"`
	SSHBinaryAvailable bool               `json:"ssh_binary_available"`
	SSHState           string             `json:"ssh_state"`
	SSHError           string             `json:"ssh_error,omitempty"`
	SSHTunnels         []ProjectSSHStatus `json:"ssh_tunnels"`
	HostFingerprint    string             `json:"host_fingerprint,omitempty"`
	ListenerRunning    bool               `json:"listener_running"`
	ListenerError      string             `json:"listener_error,omitempty"`
	ServerCA           string             `json:"server_ca,omitempty"`
}

type managedFRPTunnel struct {
	projectID  string
	cfg        ProjectFRPConfig
	certRaw    []byte
	listener   net.Listener
	httpServer *http.Server
	localAddr  string
	localPort  int

	mu     sync.Mutex
	gen    uint64
	cancel context.CancelFunc
	done   chan struct{}
	state  string
	errMsg string
}

type managedSSHTunnel struct {
	projectID  string
	cfg        ProjectSSHConfig
	certRaw    []byte
	listener   net.Listener
	httpServer *http.Server
	localAddr  string
	localPort  int

	mu     sync.Mutex
	gen    uint64
	cancel context.CancelFunc
	done   chan struct{}
	state  string
	errMsg string
}

type managedLANListener struct {
	projectID  string
	port       int
	certRaw    []byte
	listener   net.Listener
	httpServer *http.Server
	activeAddr string
	activePort int

	mu     sync.Mutex
	errMsg string
}

type managedFRPRelay struct {
	serverPort int
	token      string

	mu     sync.Mutex
	gen    uint64
	state  string
	errMsg string
	cancel context.CancelFunc
	done   chan struct{}
}

// PeerRuntime manages the host PKI, automatic LAN mTLS listeners (with per-project separate ports),
// and isolated per-project companion FRP / OpenSSH tunnel processes for the loopback owner dashboard.
type PeerRuntime struct {
	store     *Store
	configDir string

	mu            sync.Mutex
	cfg           PeerAccessConfig
	caPEM         string
	listener      net.Listener
	httpServer    *http.Server
	activeAddr    string
	activePort    int
	activeCertRaw []byte
	listenerError string

	frpRelay              *managedFRPRelay
	lanListeners          map[string]*managedLANListener
	frpTunnels            map[string]*managedFRPTunnel
	sshTunnels            map[string]*managedSSHTunnel
	dashboardPort         int
	onDashboardPortChange func(newPort int) (string, error)
	closed                bool
}

// LoadSavedDashboardPort reads the owner-saved dashboard HTTP port from peer-access.json,
// falling back to DefaultDashboardPort if unset or invalid.
func LoadSavedDashboardPort(configDir string) int {
	if configDir == "" {
		dir, err := DefaultGossipConfigDir()
		if err != nil {
			return DefaultDashboardPort
		}
		configDir = dir
	}
	data, err := os.ReadFile(filepath.Join(configDir, "peer-access.json"))
	if err != nil {
		return DefaultDashboardPort
	}
	var cfg PeerAccessConfig
	if json.Unmarshal(data, &cfg) != nil {
		return DefaultDashboardPort
	}
	if cfg.DashboardPort >= 1 && cfg.DashboardPort <= 65535 {
		return cfg.DashboardPort
	}
	return DefaultDashboardPort
}

// SetDashboardPort records the active loopback HTTP dashboard port for UI display and collision checks.
func (pm *PeerRuntime) SetDashboardPort(port int) {
	pm.mu.Lock()
	pm.dashboardPort = port
	pm.mu.Unlock()
}

// SetDashboardPortChanger registers a loopback-only callback invoked when the owner customizes the dashboard port.
func (pm *PeerRuntime) SetDashboardPortChanger(fn func(newPort int) (string, error)) {
	pm.mu.Lock()
	pm.onDashboardPortChange = fn
	pm.mu.Unlock()
}

// NewPeerRuntime constructs a peer runtime manager rooted in configDir.
func NewPeerRuntime(store *Store, configDir string) (*PeerRuntime, error) {
	if configDir == "" {
		var err error
		configDir, err = DefaultGossipConfigDir()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return nil, err
	}
	pm := &PeerRuntime{
		store:        store,
		configDir:    configDir,
		lanListeners: make(map[string]*managedLANListener),
		frpTunnels:   make(map[string]*managedFRPTunnel),
		sshTunnels:   make(map[string]*managedSSHTunnel),
	}
	cfg, err := pm.loadConfigLocked()
	if err != nil {
		return nil, err
	}
	pm.cfg = cfg
	return pm, nil
}

// StartIfEnabled restores previously enabled LAN/project FRP listeners on boot while
// strictly respecting the SQLite remote-access kill switch (disabled by default).
func (pm *PeerRuntime) StartIfEnabled(ctx context.Context) error {
	enabled, err := pm.store.RemoteAccessEnabled(ctx)
	if err != nil || !enabled {
		return err
	}
	pm.mu.Lock()
	cfg := clonePeerConfig(pm.cfg)
	pm.mu.Unlock()
	if !cfg.LANEnabled && !cfg.FRPRelay.LocalServerActive && !hasAnyEnabledLAN(cfg) && !hasAnyEnabledFRP(cfg) && !hasAnyEnabledSSH(cfg) {
		return nil
	}
	_, err = pm.applyConfig(ctx, cfg, "")
	return err
}

// Status returns a host snapshot without selecting a specific project's FRP details.
func (pm *PeerRuntime) Status(ctx context.Context) (PeerAccessStatus, error) {
	return pm.StatusForProject(ctx, "")
}

// StatusForProject returns a snapshot of LAN status plus FRP status scoped to projectID.
func (pm *PeerRuntime) StatusForProject(ctx context.Context, projectID string) (PeerAccessStatus, error) {
	enabled, err := pm.store.RemoteAccessEnabled(ctx)
	if err != nil {
		return PeerAccessStatus{}, err
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.statusLocked(enabled, strings.TrimSpace(projectID)), nil
}

// Configure updates host LAN and/or project-scoped FRP settings, starts or stops
// listeners and companion FRP clients accordingly, and updates SQLite.
func (pm *PeerRuntime) Configure(ctx context.Context, update PeerAccessUpdate) (PeerAccessStatus, error) {
	projectID := strings.TrimSpace(update.ProjectID)
	pm.mu.Lock()
	if pm.closed {
		pm.mu.Unlock()
		return PeerAccessStatus{}, errors.New("peer runtime is closed")
	}
	next := clonePeerConfig(pm.cfg)
	dashChanger := pm.onDashboardPortChange
	currentDashPort := pm.dashboardPort
	if currentDashPort == 0 {
		if next.DashboardPort > 0 {
			currentDashPort = next.DashboardPort
		} else {
			currentDashPort = DefaultDashboardPort
		}
	}
	if update.DisableAll {
		next.LANEnabled = false
		next.FRPRelay.LocalServerActive = false
		for pid, lcfg := range next.ProjectLAN {
			lcfg.Enabled = false
			next.ProjectLAN[pid] = lcfg
		}
		for pid, pcfg := range next.ProjectFRP {
			pcfg.Enabled = false
			next.ProjectFRP[pid] = pcfg
		}
		for pid, scfg := range next.ProjectSSH {
			scfg.Enabled = false
			next.ProjectSSH[pid] = scfg
		}
		pm.mu.Unlock()
		return pm.applyConfig(ctx, next, projectID)
	}
	wantDashChange := false
	requestedDashPort := 0
	if update.DashboardPort != nil {
		requestedDashPort = *update.DashboardPort
		if requestedDashPort < 1 || requestedDashPort > 65535 {
			pm.mu.Unlock()
			return PeerAccessStatus{}, errors.New("dashboard port must be between 1 and 65535")
		}
		next.DashboardPort = requestedDashPort
		wantDashChange = requestedDashPort != currentDashPort
	}
	if update.LANEnabled != nil || update.ListenPort != nil {
		if projectID != "" {
			if next.ProjectLAN == nil {
				next.ProjectLAN = make(map[string]ProjectLANConfig)
			}
			lcfg, ok := next.ProjectLAN[projectID]
			if !ok {
				preferredPort := next.ListenPort
				if preferredPort == 0 || isLANPortClaimedByOther(next, projectID, preferredPort) || !isTCPPortBindable(preferredPort) {
					preferredPort = pickNextFreeLANPort(next, projectID)
				}
				lcfg = ProjectLANConfig{ListenPort: preferredPort}
			}
			if update.LANEnabled != nil {
				lcfg.Enabled = *update.LANEnabled
			}
			if update.ListenPort != nil {
				lcfg.ListenPort = *update.ListenPort
			}
			if lcfg.ListenPort == 0 || (update.ListenPort == nil && (update.AutoSetup || lcfg.Enabled) && isLANPortClaimedByOther(next, projectID, lcfg.ListenPort)) {
				lcfg.ListenPort = pickNextFreeLANPort(next, projectID)
			}
			next.ProjectLAN[projectID] = lcfg
		} else {
			if update.LANEnabled != nil {
				next.LANEnabled = *update.LANEnabled
			}
			if update.ListenPort != nil {
				next.ListenPort = *update.ListenPort
			}
		}
	}

	// Seed default FRP relay from environment variables if host config is still empty.
	if next.FRPRelay.Server == "" {
		if envSrv := strings.TrimSpace(os.Getenv("GOSSIP_FRP_SERVER")); envSrv != "" {
			next.FRPRelay.Server = envSrv
			if p, err := strconv.Atoi(strings.TrimSpace(os.Getenv("GOSSIP_FRP_SERVER_PORT"))); err == nil && p > 0 && p <= 65535 {
				next.FRPRelay.ServerPort = p
			}
			if envTok := strings.TrimSpace(os.Getenv("GOSSIP_FRP_TOKEN")); envTok != "" && next.FRPRelay.Token == "" {
				next.FRPRelay.Token = envTok
			}
		}
	}
	if next.FRPRelay.ServerPort == 0 {
		next.FRPRelay.ServerPort = defaultFRPServerPort
	}
	if update.FRPAutoGenerate {
		tok, genErr := generateSecureFRPToken()
		if genErr != nil {
			pm.mu.Unlock()
			return PeerAccessStatus{}, genErr
		}
		next.FRPRelay.Token = tok
	}
	if update.FRPLocalRelay != nil {
		next.FRPRelay.LocalServerActive = *update.FRPLocalRelay
		if *update.FRPLocalRelay {
			if update.FRPServer != nil && strings.TrimSpace(*update.FRPServer) != "" {
				next.FRPRelay.Server = strings.TrimSpace(*update.FRPServer)
			} else if next.FRPRelay.Server == "" {
				if ips := orderedLANIPs(); len(ips) > 0 {
					next.FRPRelay.Server = ips[0]
				} else {
					next.FRPRelay.Server = "127.0.0.1"
				}
			}
			if update.FRPServerPort != nil && *update.FRPServerPort > 0 {
				next.FRPRelay.ServerPort = *update.FRPServerPort
			}
			if next.FRPRelay.ServerPort == 0 {
				next.FRPRelay.ServerPort = defaultFRPServerPort
			}
			if update.FRPToken != nil && strings.TrimSpace(*update.FRPToken) != "" {
				next.FRPRelay.Token = strings.TrimSpace(*update.FRPToken)
			}
			if next.FRPRelay.Token == "" {
				tok, genErr := generateSecureFRPToken()
				if genErr != nil {
					pm.mu.Unlock()
					return PeerAccessStatus{}, genErr
				}
				next.FRPRelay.Token = tok
			}
		}
	}

	hasFRPUpdate := update.FRPEnabled != nil || update.FRPServer != nil || update.FRPServerPort != nil || update.FRPRemotePort != nil || update.FRPToken != nil || (update.AutoSetup && projectID != "") || (update.FRPAutoGenerate && projectID != "")
	if hasFRPUpdate {
		if projectID == "" {
			if update.FRPServer != nil && strings.TrimSpace(*update.FRPServer) != "" {
				next.FRPRelay.Server = strings.TrimSpace(*update.FRPServer)
			}
			if update.FRPServerPort != nil && *update.FRPServerPort > 0 {
				next.FRPRelay.ServerPort = *update.FRPServerPort
			}
			if update.FRPToken != nil && strings.TrimSpace(*update.FRPToken) != "" {
				next.FRPRelay.Token = strings.TrimSpace(*update.FRPToken)
			}
			if update.FRPEnabled != nil {
				pm.mu.Unlock()
				return PeerAccessStatus{}, errors.New("project_id is required to configure an FRP tunnel")
			}
		} else {
			if next.ProjectFRP == nil {
				next.ProjectFRP = make(map[string]ProjectFRPConfig)
			}
			pcfg, existed := next.ProjectFRP[projectID]
			if !existed {
				pcfg = ProjectFRPConfig{
					Server:     next.FRPRelay.Server,
					ServerPort: next.FRPRelay.ServerPort,
					Token:      next.FRPRelay.Token,
					RemotePort: pickNextFreeFRPRemotePort(next, next.FRPRelay.Server, projectID),
				}
			}
			if update.FRPEnabled != nil {
				pcfg.Enabled = *update.FRPEnabled
			}
			if update.FRPServer != nil {
				srvTrim := strings.TrimSpace(*update.FRPServer)
				if srvTrim != "" || update.FRPLocalRelay == nil {
					pcfg.Server = srvTrim
				}
			}
			if update.FRPServerPort != nil {
				pcfg.ServerPort = *update.FRPServerPort
			}
			if update.FRPRemotePort != nil {
				pcfg.RemotePort = *update.FRPRemotePort
			}
			if update.FRPToken != nil {
				trimmed := strings.TrimSpace(*update.FRPToken)
				if trimmed != "" {
					pcfg.Token = trimmed
				}
			}
			if update.FRPAutoGenerate {
				pcfg.Token = next.FRPRelay.Token
			}
			// Auto-fill missing relay host / port / token for zero-friction multi-project setup.
			if pcfg.Server == "" && next.FRPRelay.Server != "" {
				pcfg.Server = next.FRPRelay.Server
			}
			if pcfg.ServerPort == 0 {
				if next.FRPRelay.ServerPort > 0 {
					pcfg.ServerPort = next.FRPRelay.ServerPort
				} else {
					pcfg.ServerPort = defaultFRPServerPort
				}
			}
			if pcfg.Token == "" {
				if next.FRPRelay.Token != "" {
					pcfg.Token = next.FRPRelay.Token
				} else if pcfg.Enabled || update.AutoSetup || pcfg.Server != "" {
					tok, genErr := generateSecureFRPToken()
					if genErr != nil {
						pm.mu.Unlock()
						return PeerAccessStatus{}, genErr
					}
					pcfg.Token = tok
				}
			}
			if pcfg.RemotePort == 0 || (update.FRPRemotePort == nil && (update.AutoSetup || pcfg.Enabled) && isFRPRemotePortClaimedByOther(next, pcfg.Server, projectID, pcfg.RemotePort)) {
				pcfg.RemotePort = pickNextFreeFRPRemotePort(next, pcfg.Server, projectID)
			}
			// Persist host-level relay defaults from first working relay config so subsequent projects need zero manual re-entry.
			if pcfg.Server != "" && (next.FRPRelay.Server == "" || next.FRPRelay.Server == pcfg.Server) {
				next.FRPRelay.Server = pcfg.Server
				if pcfg.ServerPort > 0 {
					next.FRPRelay.ServerPort = pcfg.ServerPort
				}
				if pcfg.Token != "" && (next.FRPRelay.Token == "" || update.FRPToken != nil || update.FRPAutoGenerate) {
					next.FRPRelay.Token = pcfg.Token
				}
			}
			hasCustomFRPPorts := (update.FRPServerPort != nil && *update.FRPServerPort > 0 && *update.FRPServerPort != defaultFRPServerPort) ||
				(update.FRPRemotePort != nil && *update.FRPRemotePort > 0 && *update.FRPRemotePort != defaultFRPRemotePort)
			if hasCustomFRPPorts && update.FRPServerPort != nil && *update.FRPServerPort > 0 && next.FRPRelay.Server == "" {
				next.FRPRelay.ServerPort = *update.FRPServerPort
			}
			if !existed && !pcfg.Enabled && strings.TrimSpace(pcfg.Server) == "" && !hasCustomFRPPorts {
				delete(next.ProjectFRP, projectID)
			} else {
				next.ProjectFRP[projectID] = pcfg
			}
		}
	}
	hasSSHUpdate := update.SSHEnabled != nil || update.SSHMode != nil || update.SSHLocalPort != nil || update.SSHTarget != nil || update.SSHPort != nil || update.SSHRemotePort != nil || update.SSHIdentityFile != nil
	if hasSSHUpdate {
		if projectID == "" {
			pm.mu.Unlock()
			return PeerAccessStatus{}, errors.New("project_id is required to configure SSH access")
		}
		if next.ProjectSSH == nil {
			next.ProjectSSH = make(map[string]ProjectSSHConfig)
		}
		scfg, ok := next.ProjectSSH[projectID]
		if !ok {
			scfg = ProjectSSHConfig{
				Mode:       "direct",
				LocalPort:  defaultSSHRemotePort,
				SSHPort:    defaultSSHPort,
				RemotePort: defaultSSHRemotePort,
			}
		}
		if update.SSHEnabled != nil {
			scfg.Enabled = *update.SSHEnabled
		}
		if update.SSHMode != nil {
			scfg.Mode = strings.ToLower(strings.TrimSpace(*update.SSHMode))
		}
		if update.SSHLocalPort != nil {
			scfg.LocalPort = *update.SSHLocalPort
		}
		if update.SSHTarget != nil {
			scfg.Target = strings.TrimSpace(*update.SSHTarget)
		}
		if update.SSHPort != nil {
			scfg.SSHPort = *update.SSHPort
		}
		if update.SSHRemotePort != nil {
			scfg.RemotePort = *update.SSHRemotePort
		}
		if update.SSHIdentityFile != nil {
			scfg.IdentityFile = strings.TrimSpace(*update.SSHIdentityFile)
		}
		next.ProjectSSH[projectID] = scfg
	}
	pm.mu.Unlock()
	var redirectURL string
	if wantDashChange {
		if _, normErr := normalizePeerConfig(next); normErr != nil {
			return PeerAccessStatus{}, normErr
		}
		if dashChanger != nil {
			var chgErr error
			redirectURL, chgErr = dashChanger(requestedDashPort)
			if chgErr != nil {
				return PeerAccessStatus{}, chgErr
			}
			pm.SetDashboardPort(requestedDashPort)
		} else {
			pm.SetDashboardPort(requestedDashPort)
		}
	}
	st, err := pm.applyConfig(ctx, next, projectID)
	if err != nil {
		return PeerAccessStatus{}, err
	}
	if redirectURL != "" {
		st.DashboardRedirectURL = redirectURL
	}
	return st, nil
}

// EnsureLANActive turns on LAN peer access cleanly for frictionless LAN sharing.
func (pm *PeerRuntime) EnsureLANActive(ctx context.Context) (PeerAccessStatus, error) {
	enable := true
	return pm.Configure(ctx, PeerAccessUpdate{LANEnabled: &enable})
}

// EnsureLANActiveForProject enables projectID's separate-port LAN mTLS listener cleanly.
func (pm *PeerRuntime) EnsureLANActiveForProject(ctx context.Context, projectID string) (PeerAccessStatus, error) {
	projectID = strings.TrimSpace(projectID)
	enable := true
	return pm.Configure(ctx, PeerAccessUpdate{ProjectID: projectID, LANEnabled: &enable, AutoSetup: true})
}

// EnsureFRPActiveForProject enables projectID's isolated FRP tunnel automatically using saved host relay
// defaults (or local relay), an auto-generated CSPRNG token, and an automatically allocated remote port.
// EnsureFRPActiveForProject enables projectID's isolated FRP tunnel automatically using saved relay defaults.
func (pm *PeerRuntime) EnsureFRPActiveForProject(ctx context.Context, projectID string) (PeerAccessStatus, error) {
	return pm.EnsureFRPActiveForProjectWithOptions(ctx, projectID, "", false)
}

// EnsureFRPActiveForProjectWithOptions enables projectID's isolated FRP tunnel automatically,
// optionally setting the host/project relay server or auto-starting the built-in local relay when autoLocalRelay is true.
func (pm *PeerRuntime) EnsureFRPActiveForProjectWithOptions(ctx context.Context, projectID, frpServer string, autoLocalRelay bool) (PeerAccessStatus, error) {
	projectID = strings.TrimSpace(projectID)
	frpServer = strings.TrimSpace(frpServer)
	enable := true
	upd := PeerAccessUpdate{
		ProjectID:  projectID,
		FRPEnabled: &enable,
		AutoSetup:  true,
	}
	if frpServer != "" {
		upd.FRPServer = &frpServer
	} else if autoLocalRelay {
		pm.mu.Lock()
		hostServer := strings.TrimSpace(pm.cfg.FRPRelay.Server)
		localActive := pm.cfg.FRPRelay.LocalServerActive
		projServer := ""
		if projectID != "" {
			projServer = strings.TrimSpace(pm.cfg.ProjectFRP[projectID].Server)
		}
		pm.mu.Unlock()
		if hostServer == "" && projServer == "" && !localActive {
			upd.FRPLocalRelay = &enable
		}
	}
	return pm.Configure(ctx, upd)
}

// FRPRelaySetupCommand returns the ready-to-paste `gossip-frp server` command (generating & persisting
// a high-entropy token if one does not exist yet). Only exposed to local loopback owners.
func (pm *PeerRuntime) FRPRelaySetupCommand(ctx context.Context) (command string, port int, localActive bool, err error) {
	pm.mu.Lock()
	if pm.closed {
		pm.mu.Unlock()
		return "", 0, false, errors.New("peer runtime is closed")
	}
	next := clonePeerConfig(pm.cfg)
	if next.FRPRelay.ServerPort == 0 {
		next.FRPRelay.ServerPort = defaultFRPServerPort
	}
	changed := false
	if next.FRPRelay.Token == "" {
		tok, genErr := generateSecureFRPToken()
		if genErr != nil {
			pm.mu.Unlock()
			return "", 0, false, genErr
		}
		next.FRPRelay.Token = tok
		changed = true
	}
	if changed {
		pm.cfg = next
		err = pm.saveConfigLocked(next)
	}
	port = next.FRPRelay.ServerPort
	localActive = next.FRPRelay.LocalServerActive
	tok := next.FRPRelay.Token
	pm.mu.Unlock()
	if err != nil {
		return "", 0, false, err
	}
	command = fmt.Sprintf("gossip-frp server --port %d --token '%s'", port, tok)
	return command, port, localActive, nil
}

// ResolveInviteEndpoint resolves the peer HTTPS URL and CA for projectID in "lan", "frp", or "ssh" mode.
func (pm *PeerRuntime) ResolveInviteEndpoint(ctx context.Context, projectID, mode, lanIP string) (endpoint, serverCA string, err error) {
	info, err := pm.ResolveInviteDetails(ctx, projectID, mode, lanIP)
	if err != nil {
		return "", "", err
	}
	return info.Endpoint, info.ServerCA, nil
}

// ResolveInviteDetails resolves the peer endpoint, ServerCA, fingerprint, and SSH tunnel hints.
func (pm *PeerRuntime) ResolveInviteDetails(ctx context.Context, projectID, mode, lanIP string) (InviteEndpointInfo, error) {
	projectID = strings.TrimSpace(projectID)
	st, err := pm.StatusForProject(ctx, projectID)
	if err != nil {
		return InviteEndpointInfo{}, err
	}
	if st.ServerCA == "" {
		return InviteEndpointInfo{}, errors.New("host PKI is not initialized")
	}
	hostFP := st.HostFingerprint
	if hostFP == "" {
		hostFP = HostCertSSHFingerprint(st.ServerCA)
	}
	normMode := strings.ToLower(strings.TrimSpace(mode))
	switch normMode {
	case "lan":
		if !st.LANEnabled || !st.ListenerRunning || st.ActivePort <= 0 {
			return InviteEndpointInfo{}, errors.New("LAN peer access is not running")
		}
		ip := strings.TrimSpace(lanIP)
		if ip == "" {
			if len(st.DetectedLANIPs) == 0 {
				return InviteEndpointInfo{}, errors.New("no non-loopback LAN IPv4 address was detected on this host")
			}
			ip = st.DetectedLANIPs[0]
		} else if !slices.Contains(st.DetectedLANIPs, ip) && net.ParseIP(ip) == nil {
			return InviteEndpointInfo{}, errors.New("selected LAN address is invalid")
		}
		return InviteEndpointInfo{
			Mode:            "lan",
			Endpoint:        "https://" + net.JoinHostPort(ip, strconv.Itoa(st.ActivePort)),
			ServerCA:        st.ServerCA,
			HostFingerprint: hostFP,
		}, nil
	case "frp":
		if !st.FRPBinaryAvailable {
			return InviteEndpointInfo{}, errors.New("gossip-frp binary is not installed; Gossip is running without FRP support")
		}
		if !st.FRPEnabled || st.FRPEndpoint == "" {
			return InviteEndpointInfo{}, errors.New("FRP tunnel is not enabled for this project; configure and enable it in Settings -> Connections -> Remote access")
		}
		return InviteEndpointInfo{
			Mode:            "frp",
			Endpoint:        st.FRPEndpoint,
			ServerCA:        st.ServerCA,
			HostFingerprint: hostFP,
		}, nil
	case "ssh":
		if !st.SSHEnabled || st.SSHEndpoint == "" {
			return InviteEndpointInfo{}, errors.New("SSH access is not enabled for this project; configure and enable it in Settings -> Connections -> Remote access")
		}
		sshPort := st.SSHPort
		if sshPort <= 0 {
			sshPort = defaultSSHPort
		}
		remotePort := st.SSHLocalPort
		if strings.EqualFold(st.SSHMode, "relay") && st.SSHRemotePort > 0 {
			remotePort = st.SSHRemotePort
		}
		if remotePort <= 0 {
			remotePort = defaultSSHRemotePort
		}
		target := strings.TrimSpace(st.SSHTarget)
		if target == "" {
			target = defaultHostSSHTargetHint(st.DetectedLANIPs)
		}
		return InviteEndpointInfo{
			Mode:            "ssh",
			Endpoint:        st.SSHEndpoint,
			ServerCA:        st.ServerCA,
			HostFingerprint: hostFP,
			SSHTarget:       target,
			SSHPort:         sshPort,
			SSHRemotePort:   remotePort,
		}, nil
	default:
		return InviteEndpointInfo{}, fmt.Errorf("unsupported invite mode %q", mode)
	}
}

func defaultHostSSHTargetHint(lanIPs []string) string {
	username := ""
	if cur, err := user.Current(); err == nil && cur.Username != "" && !strings.HasPrefix(cur.Username, "-") && !strings.ContainsAny(cur.Username, " \t\r\n") {
		username = cur.Username
	}
	host := ""
	if len(lanIPs) > 0 {
		host = lanIPs[0]
	} else if h, err := os.Hostname(); err == nil && h != "" && !strings.ContainsAny(h, " \t\r\n") {
		host = h
	}
	if host == "" {
		return ""
	}
	if username != "" {
		return username + "@" + host
	}
	return host
}

// Close shuts down all LAN mTLS listeners and every isolated per-project FRP/SSH tunnel.
func (pm *PeerRuntime) Close() error {
	pm.mu.Lock()
	pm.closed = true
	err := pm.stopListenerLocked()
	relay := pm.frpRelay
	pm.frpRelay = nil
	lanListeners := pm.drainAllLANListenersLocked()
	tunnels := pm.drainAllFRPTunnelsLocked()
	sshTunnels := pm.drainAllSSHTunnelsLocked()
	pm.mu.Unlock()
	pm.stopLocalFRPRelay(relay)
	for _, l := range lanListeners {
		pm.stopManagedLANListener(l)
	}
	for _, t := range tunnels {
		pm.stopTunnel(t)
	}
	for _, t := range sshTunnels {
		pm.stopSSHTunnel(t)
	}
	return err
}

func clonePeerConfig(in PeerAccessConfig) PeerAccessConfig {
	out := PeerAccessConfig{
		DashboardPort: in.DashboardPort,
		LANEnabled:    in.LANEnabled,
		ListenPort:    in.ListenPort,
		FRPRelay:      in.FRPRelay,
	}
	if len(in.ProjectLAN) > 0 {
		out.ProjectLAN = make(map[string]ProjectLANConfig, len(in.ProjectLAN))
		for k, v := range in.ProjectLAN {
			out.ProjectLAN[k] = v
		}
	}
	if len(in.ProjectFRP) > 0 {
		out.ProjectFRP = make(map[string]ProjectFRPConfig, len(in.ProjectFRP))
		for k, v := range in.ProjectFRP {
			out.ProjectFRP[k] = v
		}
	}
	if len(in.ProjectSSH) > 0 {
		out.ProjectSSH = make(map[string]ProjectSSHConfig, len(in.ProjectSSH))
		for k, v := range in.ProjectSSH {
			out.ProjectSSH[k] = v
		}
	}
	return out
}

func hasAnyEnabledLAN(cfg PeerAccessConfig) bool {
	for _, lcfg := range cfg.ProjectLAN {
		if lcfg.Enabled {
			return true
		}
	}
	return false
}

func isLANPortClaimedByOther(cfg PeerAccessConfig, excludeProject string, port int) bool {
	if port <= 0 {
		return false
	}
	if excludeProject != "" && cfg.LANEnabled && cfg.ListenPort == port {
		return true
	}
	for pid, lcfg := range cfg.ProjectLAN {
		if pid != excludeProject && lcfg.Enabled && lcfg.ListenPort == port {
			return true
		}
	}
	return false
}

func isTCPPortBindable(port int) bool {
	if port < 1 || port > 65535 {
		return false
	}
	probe, err := net.Listen("tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = probe.Close()
	return true
}

func isReservedDashboardPort(cfg PeerAccessConfig, port int) bool {
	dashPort := cfg.DashboardPort
	if dashPort == 0 {
		dashPort = DefaultDashboardPort
	}
	return port == dashPort || port == DefaultDashboardPort
}

func pickNextFreeLANPort(cfg PeerAccessConfig, excludeProject string) int {
	for port := defaultPeerListenPort; port < 65535; port++ {
		if isReservedDashboardPort(cfg, port) || isLANPortClaimedByOther(cfg, excludeProject, port) {
			continue
		}
		if isTCPPortBindable(port) {
			return port
		}
	}
	return defaultPeerListenPort
}

func generateSecureFRPToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", fmt.Errorf("generate FRP token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func isFRPRemotePortClaimedByOther(cfg PeerAccessConfig, server, excludeProject string, port int) bool {
	if port <= 0 {
		return false
	}
	normSrv := strings.ToLower(strings.TrimSpace(server))
	for pid, pcfg := range cfg.ProjectFRP {
		if pid == excludeProject {
			continue
		}
		if pcfg.RemotePort != port {
			continue
		}
		otherSrv := strings.ToLower(strings.TrimSpace(pcfg.Server))
		if normSrv == "" || otherSrv == "" || normSrv == otherSrv {
			if pcfg.Enabled || otherSrv != "" {
				return true
			}
		}
	}
	return false
}

func pickNextFreeFRPRemotePort(cfg PeerAccessConfig, server, excludeProject string) int {
	for port := defaultFRPRemotePort; port < 65535; port++ {
		if isReservedDashboardPort(cfg, port) || isFRPRemotePortClaimedByOther(cfg, server, excludeProject, port) {
			continue
		}
		if cfg.FRPRelay.LocalServerActive && !isTCPPortBindable(port) {
			continue
		}
		return port
	}
	return defaultFRPRemotePort
}

func hasAnyEnabledFRP(cfg PeerAccessConfig) bool {
	for _, pcfg := range cfg.ProjectFRP {
		if pcfg.Enabled {
			return true
		}
	}
	return false
}

func hasAnyEnabledSSH(cfg PeerAccessConfig) bool {
	for _, scfg := range cfg.ProjectSSH {
		if scfg.Enabled {
			return true
		}
	}
	return false
}

func hasAnyEnabledSSHRelay(cfg PeerAccessConfig) bool {
	for _, scfg := range cfg.ProjectSSH {
		if scfg.Enabled && strings.EqualFold(scfg.Mode, "relay") {
			return true
		}
	}
	return false
}

func normalizeProjectSSHConfig(projectID string, scfg ProjectSSHConfig) (ProjectSSHConfig, error) {
	if projectID == "" || strings.ContainsAny(projectID, "/\\ \t\r\n") {
		return scfg, errors.New("invalid project_id for SSH configuration")
	}
	mode := strings.ToLower(strings.TrimSpace(scfg.Mode))
	if mode == "" {
		mode = "direct"
	}
	if mode != "direct" && mode != "relay" {
		return scfg, errors.New("SSH mode must be 'direct' or 'relay'")
	}
	scfg.Mode = mode
	if scfg.LocalPort == 0 {
		scfg.LocalPort = defaultSSHRemotePort
	}
	if scfg.LocalPort < 1 || scfg.LocalPort > 65535 {
		return scfg, errors.New("SSH host port must be between 1 and 65535")
	}
	if scfg.SSHPort == 0 {
		scfg.SSHPort = defaultSSHPort
	}
	if scfg.SSHPort < 1 || scfg.SSHPort > 65535 {
		return scfg, errors.New("SSH daemon port must be between 1 and 65535")
	}
	if scfg.RemotePort == 0 {
		scfg.RemotePort = defaultSSHRemotePort
	}
	if scfg.RemotePort < 1 || scfg.RemotePort > 65535 {
		return scfg, errors.New("SSH remote forward port must be between 1 and 65535")
	}
	scfg.Target = strings.TrimSpace(scfg.Target)
	if scfg.Target != "" {
		validTarget, err := ValidateSSHTarget(scfg.Target)
		if err != nil {
			return scfg, err
		}
		scfg.Target = validTarget
	} else if scfg.Enabled && scfg.Mode == "relay" {
		return scfg, errors.New("enter an SSH relay target (e.g. user@relay.example.com) before enabling SSH relay")
	}
	scfg.IdentityFile = strings.TrimSpace(scfg.IdentityFile)
	if scfg.IdentityFile != "" {
		validIdent, err := ValidateSSHIdentityFile(scfg.IdentityFile)
		if err != nil {
			return scfg, err
		}
		scfg.IdentityFile = validIdent
	}
	return scfg, nil
}

func normalizeProjectFRPConfig(projectID string, pcfg ProjectFRPConfig) (ProjectFRPConfig, error) {
	if projectID == "" || strings.ContainsAny(projectID, "/\\ \t\r\n") {
		return pcfg, errors.New("invalid project_id for FRP configuration")
	}
	if pcfg.ServerPort == 0 {
		pcfg.ServerPort = defaultFRPServerPort
	}
	if pcfg.ServerPort < 1 || pcfg.ServerPort > 65535 {
		return pcfg, errors.New("FRP control port must be between 1 and 65535")
	}
	if pcfg.RemotePort == 0 {
		pcfg.RemotePort = defaultFRPRemotePort
	}
	if pcfg.RemotePort < 1 || pcfg.RemotePort > 65535 {
		return pcfg, errors.New("FRP remote port must be between 1 and 65535")
	}
	pcfg.Server = strings.TrimSpace(pcfg.Server)
	pcfg.Token = strings.TrimSpace(pcfg.Token)
	if pcfg.Server != "" && strings.ContainsAny(pcfg.Server, "/\\ \t\r\n\"'") {
		return pcfg, errors.New("FRP server address contains invalid characters")
	}
	if strings.ContainsAny(pcfg.Token, "\r\n\"'") {
		return pcfg, errors.New("FRP token contains invalid characters")
	}
	if len(pcfg.Token) > 4096 {
		return pcfg, errors.New("FRP token exceeds maximum length")
	}
	if pcfg.Enabled {
		if pcfg.Server == "" {
			return pcfg, errors.New("enter this project's FRP server host or IP before enabling FRP")
		}
		if pcfg.Token == "" {
			return pcfg, errors.New("enter this project's FRP authentication token before enabling FRP")
		}
	}
	return pcfg, nil
}

func normalizePeerConfig(cfg PeerAccessConfig) (PeerAccessConfig, error) {
	if cfg.DashboardPort != 0 && (cfg.DashboardPort < 1 || cfg.DashboardPort > 65535) {
		return cfg, errors.New("dashboard port must be between 1 and 65535")
	}
	if cfg.ListenPort == 0 {
		cfg.ListenPort = defaultPeerListenPort
	}
	if cfg.ListenPort < 1 || cfg.ListenPort > 65535 {
		return cfg, errors.New("peer listen port must be between 1 and 65535")
	}
	relay := cfg.FRPRelay
	relay.Server = strings.TrimSpace(relay.Server)
	relay.Token = strings.TrimSpace(relay.Token)
	if relay.ServerPort == 0 {
		relay.ServerPort = defaultFRPServerPort
	}
	if relay.ServerPort < 1 || relay.ServerPort > 65535 {
		return cfg, errors.New("FRP relay control port must be between 1 and 65535")
	}
	if relay.Server != "" && strings.ContainsAny(relay.Server, "/\\ \t\r\n\"'") {
		return cfg, errors.New("FRP relay server address contains invalid characters")
	}
	if strings.ContainsAny(relay.Token, "\r\n\"'") {
		return cfg, errors.New("FRP relay token contains invalid characters")
	}
	if relay.LocalServerActive && relay.Token == "" {
		return cfg, errors.New("local FRP relay token is required")
	}
	dashPort := cfg.DashboardPort
	if dashPort == 0 {
		dashPort = DefaultDashboardPort
	}
	if cfg.LANEnabled && cfg.ListenPort == dashPort {
		return cfg, fmt.Errorf("LAN port %d conflicts with dashboard port %d", cfg.ListenPort, dashPort)
	}
	if relay.LocalServerActive && relay.ServerPort == dashPort {
		return cfg, fmt.Errorf("local FRP control port %d conflicts with dashboard port %d", relay.ServerPort, dashPort)
	}
	out := PeerAccessConfig{
		DashboardPort: cfg.DashboardPort,
		LANEnabled:    cfg.LANEnabled,
		ListenPort:    cfg.ListenPort,
		FRPRelay:      relay,
	}
	usedLANPorts := make(map[int]string)
	if out.LANEnabled {
		usedLANPorts[out.ListenPort] = "host-default"
	}
	if len(cfg.ProjectLAN) > 0 {
		out.ProjectLAN = make(map[string]ProjectLANConfig, len(cfg.ProjectLAN))
		pids := make([]string, 0, len(cfg.ProjectLAN))
		for pid := range cfg.ProjectLAN {
			pids = append(pids, pid)
		}
		sort.Strings(pids)
		for _, pid := range pids {
			lcfg := cfg.ProjectLAN[pid]
			if pid == "" || strings.ContainsAny(pid, "/\\ \t\r\n") {
				return out, errors.New("invalid project_id for LAN configuration")
			}
			if lcfg.ListenPort == 0 {
				lcfg.ListenPort = defaultPeerListenPort
			}
			if lcfg.ListenPort < 1 || lcfg.ListenPort > 65535 {
				return out, errors.New("project LAN listen port must be between 1 and 65535")
			}
			if lcfg.Enabled {
				if lcfg.ListenPort == dashPort {
					return out, fmt.Errorf("LAN port %d conflicts with dashboard port %d", lcfg.ListenPort, dashPort)
				}
				if otherPID, exists := usedLANPorts[lcfg.ListenPort]; exists {
					return out, fmt.Errorf("LAN port %d is already used by project %s; choose a separate port for each project", lcfg.ListenPort, otherPID)
				}
				usedLANPorts[lcfg.ListenPort] = pid
			}
			out.ProjectLAN[pid] = lcfg
		}
	}
	if len(cfg.ProjectFRP) > 0 {
		out.ProjectFRP = make(map[string]ProjectFRPConfig, len(cfg.ProjectFRP))
		activeEndpoints := make(map[string]string)
		pids := make([]string, 0, len(cfg.ProjectFRP))
		for pid := range cfg.ProjectFRP {
			pids = append(pids, pid)
		}
		sort.Strings(pids)
		for _, pid := range pids {
			pcfg, err := normalizeProjectFRPConfig(pid, cfg.ProjectFRP[pid])
			if err != nil {
				return out, err
			}
			if pcfg.Enabled {
				key := strings.ToLower(pcfg.Server) + ":" + strconv.Itoa(pcfg.RemotePort)
				if otherPID, exists := activeEndpoints[key]; exists {
					return out, fmt.Errorf("FRP remote port %d on %s is already enabled for project %s", pcfg.RemotePort, pcfg.Server, otherPID)
				}
				activeEndpoints[key] = pid
			}
			out.ProjectFRP[pid] = pcfg
		}
	}
	if len(cfg.ProjectSSH) > 0 {
		out.ProjectSSH = make(map[string]ProjectSSHConfig, len(cfg.ProjectSSH))
		usedLocalPorts := make(map[int]string)
		usedRelayEndpoints := make(map[string]string)
		pids := make([]string, 0, len(cfg.ProjectSSH))
		for pid := range cfg.ProjectSSH {
			pids = append(pids, pid)
		}
		sort.Strings(pids)
		for _, pid := range pids {
			scfg, err := normalizeProjectSSHConfig(pid, cfg.ProjectSSH[pid])
			if err != nil {
				return out, err
			}
			if scfg.Enabled {
				if scfg.Mode == "direct" {
					if cfg.LANEnabled && scfg.LocalPort == cfg.ListenPort {
						return out, fmt.Errorf("SSH local port %d conflicts with Host LAN Listen Port %d", scfg.LocalPort, cfg.ListenPort)
					}
					if otherPID, exists := usedLocalPorts[scfg.LocalPort]; exists {
						return out, fmt.Errorf("SSH port %d is already enabled for project %s", scfg.LocalPort, otherPID)
					}
					usedLocalPorts[scfg.LocalPort] = pid
				} else {
					key := strings.ToLower(scfg.Target) + ":" + strconv.Itoa(scfg.RemotePort)
					if otherPID, exists := usedRelayEndpoints[key]; exists {
						return out, fmt.Errorf("SSH remote port %d on %s is already enabled for project %s", scfg.RemotePort, scfg.Target, otherPID)
					}
					usedRelayEndpoints[key] = pid
				}
			}
			out.ProjectSSH[pid] = scfg
		}
	}
	return out, nil
}

func (pm *PeerRuntime) applyConfig(ctx context.Context, candidate PeerAccessConfig, focusProjectID string) (PeerAccessStatus, error) {
	normalized, err := normalizePeerConfig(candidate)
	if err != nil {
		return PeerAccessStatus{}, err
	}
	anyLAN := hasAnyEnabledLAN(normalized)
	anyFRP := hasAnyEnabledFRP(normalized)
	anyLocalRelay := normalized.FRPRelay.LocalServerActive
	if anyFRP || anyLocalRelay {
		if _, findErr := findFRPBinary(); findErr != nil {
			return PeerAccessStatus{}, findErr
		}
	}
	anySSH := hasAnyEnabledSSH(normalized)
	if hasAnyEnabledSSHRelay(normalized) {
		if _, findErr := FindSSHBinary(); findErr != nil {
			return PeerAccessStatus{}, findErr
		}
	}

	// Shut everything down cleanly when LAN, local FRP relay, and all project LAN/FRP/SSH listeners are disabled.
	if !normalized.LANEnabled && !anyLocalRelay && !anyLAN && !anyFRP && !anySSH {
		pm.mu.Lock()
		if pm.closed {
			pm.mu.Unlock()
			return PeerAccessStatus{}, errors.New("peer runtime is closed")
		}
		if stopErr := pm.stopListenerLocked(); stopErr != nil {
			pm.listenerError = stopErr.Error()
		} else {
			pm.listenerError = ""
		}
		stopRelay := pm.frpRelay
		pm.frpRelay = nil
		toStopLAN := pm.drainAllLANListenersLocked()
		toStopFRP := pm.drainAllFRPTunnelsLocked()
		toStopSSH := pm.drainAllSSHTunnelsLocked()
		pm.cfg = normalized
		saveErr := pm.saveConfigLocked(normalized)
		pm.mu.Unlock()

		pm.stopLocalFRPRelay(stopRelay)
		for _, l := range toStopLAN {
			pm.stopManagedLANListener(l)
		}
		for _, t := range toStopFRP {
			pm.stopTunnel(t)
		}
		for _, t := range toStopSSH {
			pm.stopSSHTunnel(t)
		}
		if saveErr != nil {
			return PeerAccessStatus{}, saveErr
		}
		if dbErr := pm.store.SetRemoteAccessEnabled(ctx, false); dbErr != nil {
			return PeerAccessStatus{}, dbErr
		}
		return pm.StatusForProject(ctx, focusProjectID)
	}

	var extraSANs []string
	if normalized.FRPRelay.Server != "" {
		extraSANs = append(extraSANs, normalized.FRPRelay.Server)
	}
	for _, pcfg := range normalized.ProjectFRP {
		if pcfg.Server != "" {
			extraSANs = append(extraSANs, pcfg.Server)
		}
	}
	caCert, caSigner, serverTLSCert, caPEM, err := ensureHostPKI(pm.configDir, extraSANs)
	if err != nil {
		return PeerAccessStatus{}, err
	}
	var leafRaw []byte
	if len(serverTLSCert.Certificate) > 0 {
		leafRaw = serverTLSCert.Certificate[0]
	}

	pm.mu.Lock()
	if pm.closed {
		pm.mu.Unlock()
		return PeerAccessStatus{}, errors.New("peer runtime is closed")
	}
	pm.caPEM = caPEM

	if normalized.LANEnabled {
		desiredAddr := net.JoinHostPort("0.0.0.0", strconv.Itoa(normalized.ListenPort))
		if err := pm.ensureListenerLocked(desiredAddr, normalized.ListenPort, caCert, caSigner, serverTLSCert); err != nil {
			pm.listenerError = err.Error()
			pm.mu.Unlock()
			return PeerAccessStatus{}, err
		}
		pm.listenerError = ""
	} else {
		_ = pm.stopListenerLocked()
		pm.listenerError = ""
	}

	// Reconcile separate per-project LAN mTLS listeners (strictly scoped per project & port).
	var stopLAN []*managedLANListener
	var startLAN []struct {
		pid  string
		lcfg ProjectLANConfig
	}
	for pid, existing := range pm.lanListeners {
		desired, wanted := normalized.ProjectLAN[pid]
		if !wanted || !desired.Enabled || existing.port != desired.ListenPort || !bytes.Equal(existing.certRaw, leafRaw) {
			stopLAN = append(stopLAN, existing)
			delete(pm.lanListeners, pid)
		}
	}
	for pid, desired := range normalized.ProjectLAN {
		if !desired.Enabled {
			continue
		}
		if _, running := pm.lanListeners[pid]; !running {
			startLAN = append(startLAN, struct {
				pid  string
				lcfg ProjectLANConfig
			}{pid: pid, lcfg: desired})
		}
	}
	pm.mu.Unlock()

	for _, l := range stopLAN {
		pm.stopManagedLANListener(l)
	}
	for _, item := range startLAN {
		ml, startErr := pm.startProjectLANListener(item.pid, item.lcfg, caCert, caSigner, serverTLSCert)
		if startErr != nil {
			pm.mu.Lock()
			stillActive := normalized.LANEnabled || len(pm.lanListeners) > 0 || len(pm.frpTunnels) > 0 || len(pm.sshTunnels) > 0
			pm.mu.Unlock()
			if !stillActive {
				_ = pm.store.SetRemoteAccessEnabled(ctx, false)
			}
			return PeerAccessStatus{}, startErr
		}
		pm.mu.Lock()
		if pm.closed {
			pm.mu.Unlock()
			pm.stopManagedLANListener(ml)
			return PeerAccessStatus{}, errors.New("peer runtime is closed")
		}
		pm.lanListeners[item.pid] = ml
		pm.mu.Unlock()
	}

	if err := pm.store.SetRemoteAccessEnabled(ctx, true); err != nil {
		return PeerAccessStatus{}, err
	}

	// Reconcile optional built-in self-hosted FRP relay server before starting client tunnels.
	var frpBin string
	if anyLocalRelay || anyFRP {
		frpBin, err = findFRPBinary()
		if err != nil {
			return PeerAccessStatus{}, err
		}
	}
	pm.mu.Lock()
	if pm.closed {
		pm.mu.Unlock()
		return PeerAccessStatus{}, errors.New("peer runtime is closed")
	}
	var relayToStop *managedFRPRelay
	needStartRelay := false
	if !normalized.FRPRelay.LocalServerActive {
		if pm.frpRelay != nil {
			relayToStop = pm.frpRelay
			pm.frpRelay = nil
		}
	} else if pm.frpRelay == nil || pm.frpRelay.serverPort != normalized.FRPRelay.ServerPort || pm.frpRelay.token != normalized.FRPRelay.Token {
		if pm.frpRelay != nil {
			relayToStop = pm.frpRelay
			pm.frpRelay = nil
		}
		needStartRelay = true
	}
	pm.mu.Unlock()
	if relayToStop != nil {
		pm.stopLocalFRPRelay(relayToStop)
	}
	if needStartRelay {
		relayProc, startErr := pm.startLocalFRPRelay(normalized.FRPRelay.ServerPort, normalized.FRPRelay.Token, frpBin)
		if startErr != nil {
			return PeerAccessStatus{}, startErr
		}
		pm.mu.Lock()
		if pm.closed {
			pm.mu.Unlock()
			pm.stopLocalFRPRelay(relayProc)
			return PeerAccessStatus{}, errors.New("peer runtime is closed")
		}
		pm.frpRelay = relayProc
		pm.mu.Unlock()
	}

	// Reconcile per-project isolated FRP tunnels.
	pm.mu.Lock()
	var toStop []*managedFRPTunnel
	var toStart []struct {
		pid  string
		pcfg ProjectFRPConfig
	}
	for pid, existing := range pm.frpTunnels {
		desired, wanted := normalized.ProjectFRP[pid]
		if !wanted || !desired.Enabled || existing.cfg != desired || !bytes.Equal(existing.certRaw, leafRaw) {
			toStop = append(toStop, existing)
			delete(pm.frpTunnels, pid)
		}
	}
	for pid, desired := range normalized.ProjectFRP {
		if !desired.Enabled {
			continue
		}
		if _, running := pm.frpTunnels[pid]; !running {
			toStart = append(toStart, struct {
				pid  string
				pcfg ProjectFRPConfig
			}{pid: pid, pcfg: desired})
		}
	}
	pm.mu.Unlock()

	for _, t := range toStop {
		pm.stopTunnel(t)
	}

	for _, item := range toStart {
		tunnelProc, startErr := pm.startProjectFRPTunnel(item.pid, item.pcfg, frpBin, caCert, caSigner, serverTLSCert)
		if startErr != nil {
			pm.mu.Lock()
			stillActive := normalized.LANEnabled || len(pm.frpTunnels) > 0 || len(pm.sshTunnels) > 0
			pm.mu.Unlock()
			if !stillActive {
				_ = pm.store.SetRemoteAccessEnabled(ctx, false)
			}
			return PeerAccessStatus{}, startErr
		}
		pm.mu.Lock()
		if pm.closed {
			pm.mu.Unlock()
			pm.stopTunnel(tunnelProc)
			return PeerAccessStatus{}, errors.New("peer runtime is closed")
		}
		pm.frpTunnels[item.pid] = tunnelProc
		pm.mu.Unlock()
	}

	// Reconcile per-project isolated SSH access listeners & reverse relay tunnels.
	pm.mu.Lock()
	if pm.closed {
		pm.mu.Unlock()
		return PeerAccessStatus{}, errors.New("peer runtime is closed")
	}
	var sshToStop []*managedSSHTunnel
	var sshToStart []struct {
		pid  string
		scfg ProjectSSHConfig
	}
	for pid, existing := range pm.sshTunnels {
		desired, wanted := normalized.ProjectSSH[pid]
		if !wanted || !desired.Enabled || existing.cfg != desired || !bytes.Equal(existing.certRaw, leafRaw) {
			sshToStop = append(sshToStop, existing)
			delete(pm.sshTunnels, pid)
		}
	}
	for pid, desired := range normalized.ProjectSSH {
		if !desired.Enabled {
			continue
		}
		if _, running := pm.sshTunnels[pid]; !running {
			sshToStart = append(sshToStart, struct {
				pid  string
				scfg ProjectSSHConfig
			}{pid: pid, scfg: desired})
		}
	}
	pm.mu.Unlock()

	for _, t := range sshToStop {
		pm.stopSSHTunnel(t)
	}
	for _, item := range sshToStart {
		stunnel, startErr := pm.startProjectSSHTunnel(item.pid, item.scfg, caCert, caSigner, serverTLSCert)
		if startErr != nil {
			pm.mu.Lock()
			stillActive := normalized.LANEnabled || len(pm.frpTunnels) > 0 || len(pm.sshTunnels) > 0
			pm.mu.Unlock()
			if !stillActive {
				_ = pm.store.SetRemoteAccessEnabled(ctx, false)
			}
			return PeerAccessStatus{}, startErr
		}
		pm.mu.Lock()
		if pm.closed {
			pm.mu.Unlock()
			pm.stopSSHTunnel(stunnel)
			return PeerAccessStatus{}, errors.New("peer runtime is closed")
		}
		pm.sshTunnels[item.pid] = stunnel
		pm.mu.Unlock()
	}

	pm.mu.Lock()
	pm.cfg = normalized
	saveErr := pm.saveConfigLocked(normalized)
	status := pm.statusLocked(true, focusProjectID)
	pm.mu.Unlock()
	if saveErr != nil {
		return PeerAccessStatus{}, saveErr
	}
	return status, nil
}

func (pm *PeerRuntime) drainAllLANListenersLocked() []*managedLANListener {
	if len(pm.lanListeners) == 0 {
		return nil
	}
	out := make([]*managedLANListener, 0, len(pm.lanListeners))
	for pid, l := range pm.lanListeners {
		out = append(out, l)
		delete(pm.lanListeners, pid)
	}
	return out
}

func (pm *PeerRuntime) startProjectLANListener(
	projectID string,
	cfg ProjectLANConfig,
	caCert *x509.Certificate,
	caSigner crypto.Signer,
	serverCert tls.Certificate,
) (*managedLANListener, error) {
	inner := EnrollmentHandler(pm.store, RPCHandlerWithCertificateGrants(pm.store), caCert, caSigner)
	scoped := scopedProjectHandler(projectID, inner)
	ml := &managedLANListener{
		projectID: projectID,
		port:      cfg.ListenPort,
	}
	if len(serverCert.Certificate) > 0 {
		ml.certRaw = serverCert.Certificate[0]
	}
	desiredAddr := net.JoinHostPort("0.0.0.0", strconv.Itoa(cfg.ListenPort))
	listener, srv, actualPort, actualAddr, err := startMTLSListener(desiredAddr, caCert, serverCert, scoped, &ml.errMsg, &ml.mu, &ml.httpServer)
	if err != nil {
		return nil, fmt.Errorf("start isolated LAN listener for project %s on port %d: %w", projectID, cfg.ListenPort, err)
	}
	ml.listener = listener
	ml.httpServer = srv
	ml.activePort = actualPort
	ml.activeAddr = actualAddr
	return ml, nil
}

func (pm *PeerRuntime) stopManagedLANListener(l *managedLANListener) {
	if l == nil {
		return
	}
	if l.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = l.httpServer.Shutdown(ctx)
		cancel()
	}
	if l.listener != nil {
		_ = l.listener.Close()
	}
}

func (pm *PeerRuntime) drainAllFRPTunnelsLocked() []*managedFRPTunnel {
	if len(pm.frpTunnels) == 0 {
		return nil
	}
	out := make([]*managedFRPTunnel, 0, len(pm.frpTunnels))
	for pid, t := range pm.frpTunnels {
		out = append(out, t)
		delete(pm.frpTunnels, pid)
	}
	return out
}

func (pm *PeerRuntime) drainAllSSHTunnelsLocked() []*managedSSHTunnel {
	if len(pm.sshTunnels) == 0 {
		return nil
	}
	out := make([]*managedSSHTunnel, 0, len(pm.sshTunnels))
	for pid, t := range pm.sshTunnels {
		out = append(out, t)
		delete(pm.sshTunnels, pid)
	}
	return out
}

func (pm *PeerRuntime) ensureListenerLocked(desiredAddr string, preferredPort int, caCert *x509.Certificate, caSigner crypto.Signer, serverCert tls.Certificate) error {
	var leafRaw []byte
	if len(serverCert.Certificate) > 0 {
		leafRaw = serverCert.Certificate[0]
	}
	sameBind := pm.listener != nil && bytes.Equal(pm.activeCertRaw, leafRaw)
	if sameBind {
		wantHost, wantPort, _ := net.SplitHostPort(desiredAddr)
		haveHost, _, _ := net.SplitHostPort(pm.activeAddr)
		if haveHost == wantHost && (wantPort == strconv.Itoa(pm.activePort) || pm.activePort != 0 && wantPort == strconv.Itoa(preferredPort)) {
			return nil
		}
	}
	if pm.listener != nil {
		_ = pm.stopListenerLocked()
	}
	listener, srv, port, actualAddr, err := startMTLSListener(desiredAddr, caCert, serverCert, EnrollmentHandler(pm.store, RPCHandlerWithCertificateGrants(pm.store), caCert, caSigner), &pm.listenerError, &pm.mu, &pm.httpServer)
	if err != nil && preferredPort != 0 {
		host, _, _ := net.SplitHostPort(desiredAddr)
		fallbackAddr := net.JoinHostPort(host, "0")
		listener, srv, port, actualAddr, err = startMTLSListener(fallbackAddr, caCert, serverCert, EnrollmentHandler(pm.store, RPCHandlerWithCertificateGrants(pm.store), caCert, caSigner), &pm.listenerError, &pm.mu, &pm.httpServer)
	}
	if err != nil {
		return fmt.Errorf("start peer TLS listener on %s: %w", desiredAddr, err)
	}
	pm.listener = listener
	pm.httpServer = srv
	pm.activeAddr = actualAddr
	pm.activePort = port
	pm.activeCertRaw = leafRaw
	return nil
}

func startMTLSListener(
	addr string,
	caCert *x509.Certificate,
	serverCert tls.Certificate,
	handler http.Handler,
	errTarget *string,
	errMu *sync.Mutex,
	activeSrv **http.Server,
) (net.Listener, *http.Server, int, string, error) {
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	tlsCfg := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    pool,
		ClientAuth:   tls.VerifyClientCertIfGiven,
	}
	listener, err := tls.Listen("tcp", addr, tlsCfg)
	if err != nil {
		return nil, nil, 0, "", err
	}
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		_ = listener.Close()
		return nil, nil, 0, "", err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		_ = listener.Close()
		return nil, nil, 0, "", err
	}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		serveErr := srv.Serve(listener)
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) && !errors.Is(serveErr, net.ErrClosed) && errMu != nil && errTarget != nil {
			errMu.Lock()
			if activeSrv == nil || *activeSrv == srv {
				*errTarget = serveErr.Error()
			}
			errMu.Unlock()
		}
	}()
	return listener, srv, port, listener.Addr().String(), nil
}

func (pm *PeerRuntime) stopListenerLocked() error {
	var err error
	if pm.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err = pm.httpServer.Shutdown(ctx)
		cancel()
		pm.httpServer = nil
	}
	if pm.listener != nil {
		if closeErr := pm.listener.Close(); err == nil && !errors.Is(closeErr, net.ErrClosed) {
			err = closeErr
		}
		pm.listener = nil
	}
	pm.activeAddr = ""
	pm.activePort = 0
	pm.activeCertRaw = nil
	return err
}

// scopedProjectHandler restricts an isolated per-project FRP tunnel listener so
// enrollment and RPC requests can touch ONLY allowedProjectID.
func scopedProjectHandler(allowedProjectID string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/enrollment/"):
			var probe struct {
				Project string `json:"project_id"`
			}
			if err := json.Unmarshal(body, &probe); err != nil || probe.Project == "" {
				http.Error(w, "invalid enrollment request", http.StatusBadRequest)
				return
			}
			if probe.Project != allowedProjectID {
				http.Error(w, "this FRP tunnel is restricted to a different project", http.StatusForbidden)
				return
			}
		case r.URL.Path == "/v1/call":
			var probe struct {
				Project string `json:"project"`
			}
			if err := json.Unmarshal(body, &probe); err != nil || probe.Project == "" {
				http.Error(w, "project scope required", http.StatusBadRequest)
				return
			}
			if probe.Project != allowedProjectID {
				http.Error(w, "this FRP tunnel is restricted to a different project", http.StatusForbidden)
				return
			}
		default:
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (pm *PeerRuntime) startProjectFRPTunnel(
	projectID string,
	cfg ProjectFRPConfig,
	binPath string,
	caCert *x509.Certificate,
	caSigner crypto.Signer,
	serverCert tls.Certificate,
) (*managedFRPTunnel, error) {
	inner := EnrollmentHandler(pm.store, RPCHandlerWithCertificateGrants(pm.store), caCert, caSigner)
	scoped := scopedProjectHandler(projectID, inner)

	t := &managedFRPTunnel{
		projectID: projectID,
		cfg:       cfg,
		state:     "starting",
	}
	if len(serverCert.Certificate) > 0 {
		t.certRaw = serverCert.Certificate[0]
	}
	listener, srv, localPort, localAddr, err := startMTLSListener("127.0.0.1:0", caCert, serverCert, scoped, &t.errMsg, &t.mu, nil)
	if err != nil {
		return nil, fmt.Errorf("start isolated loopback listener for project %s: %w", projectID, err)
	}
	t.listener = listener
	t.httpServer = srv
	t.localPort = localPort
	t.localAddr = localAddr

	frpDir := filepath.Join(pm.configDir, "frp")
	if err := os.MkdirAll(frpDir, 0700); err != nil {
		pm.stopTunnel(t)
		return nil, err
	}
	safeName := projectTunnelSlug(projectID)
	tomlPath := filepath.Join(frpDir, safeName+".toml")
	frpCfg := tunnel.FRPClientConfig{
		ServerAddr:     cfg.Server,
		ServerPort:     cfg.ServerPort,
		AuthToken:      cfg.Token,
		ProxyName:      "gossip-" + safeName,
		ProxyType:      "tcp",
		LocalIP:        "127.0.0.1",
		LocalPort:      localPort,
		RemotePort:     cfg.RemotePort,
		UseEncryption:  true,
		UseCompression: true,
	}
	if err := writePrivateFile(tomlPath, []byte(tunnel.GenerateFRPCTOML(frpCfg)), 0600); err != nil {
		pm.stopTunnel(t)
		return nil, err
	}

	procCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.mu.Lock()
	t.gen++
	gen := t.gen
	t.cancel = cancel
	t.done = done
	t.state = "starting"
	t.errMsg = ""
	t.mu.Unlock()

	go superviseProjectFRP(procCtx, t, gen, binPath, tomlPath, cfg.Token, done)
	return t, nil
}

func (pm *PeerRuntime) stopTunnel(t *managedFRPTunnel) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.gen++
	cancel := t.cancel
	done := t.done
	t.cancel = nil
	t.done = nil
	t.state = "disabled"
	t.errMsg = ""
	t.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
	if t.httpServer != nil {
		ctx, cancelSrv := context.WithTimeout(context.Background(), 2*time.Second)
		_ = t.httpServer.Shutdown(ctx)
		cancelSrv()
	}
	if t.listener != nil {
		_ = t.listener.Close()
	}
}

func (pm *PeerRuntime) startLocalFRPRelay(serverPort int, token, binPath string) (*managedFRPRelay, error) {
	if serverPort == 0 {
		serverPort = defaultFRPServerPort
	}
	frpDir := filepath.Join(pm.configDir, "frp")
	if err := os.MkdirAll(frpDir, 0700); err != nil {
		return nil, err
	}
	tomlPath := filepath.Join(frpDir, "frps-relay.toml")
	srvCfg := tunnel.FRPServerConfig{
		BindPort:  serverPort,
		AuthToken: token,
	}
	if err := writePrivateFile(tomlPath, []byte(tunnel.GenerateFRPSTOML(srvCfg)), 0600); err != nil {
		return nil, err
	}
	procCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	r := &managedFRPRelay{
		serverPort: serverPort,
		token:      token,
		gen:        1,
		state:      "starting",
		cancel:     cancel,
		done:       done,
	}
	go superviseLocalFRPRelay(procCtx, r, 1, binPath, tomlPath, token, done)
	// Wait briefly for the local frps control port to accept connections before starting client tunnels.
	deadline := time.Now().Add(1500 * time.Millisecond)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(serverPort))
	for time.Now().Before(deadline) {
		conn, dialErr := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return r, nil
}

func (pm *PeerRuntime) stopLocalFRPRelay(r *managedFRPRelay) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.gen++
	cancel := r.cancel
	done := r.done
	r.cancel = nil
	r.done = nil
	r.state = "disabled"
	r.errMsg = ""
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
}

func superviseLocalFRPRelay(ctx context.Context, r *managedFRPRelay, gen uint64, binPath, tomlPath, token string, done chan struct{}) {
	defer close(done)
	base := filepath.Base(binPath)
	var args []string
	if strings.HasPrefix(base, "gossip-frp") {
		args = []string{"server", "-c", tomlPath}
	} else {
		args = []string{"-c", tomlPath}
	}
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		cmd := exec.CommandContext(ctx, binPath, args...)
		var stderr tailBuffer
		stderr.max = 2048
		cmd.Stdout = &stderr
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			r.mu.Lock()
			if r.gen == gen {
				r.state = "error"
				r.errMsg = sanitizeFRPError(err.Error(), token)
			}
			r.mu.Unlock()
			return
		}
		r.mu.Lock()
		if r.gen == gen {
			r.state = "running"
			r.errMsg = ""
		}
		r.mu.Unlock()

		started := time.Now()
		waitErr := cmd.Wait()
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > 15*time.Second {
			backoff = time.Second
		}
		msg := "local FRP relay exited unexpectedly"
		if waitErr != nil {
			msg = waitErr.Error()
		}
		if out := strings.TrimSpace(stderr.String()); out != "" {
			msg = lastNonEmptyLine(out)
		}
		r.mu.Lock()
		if r.gen == gen {
			r.state = "error"
			r.errMsg = sanitizeFRPError(msg, token)
		}
		r.mu.Unlock()

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 15*time.Second {
			backoff *= 2
		}
	}
}

func (pm *PeerRuntime) startProjectSSHTunnel(
	projectID string,
	cfg ProjectSSHConfig,
	caCert *x509.Certificate,
	caSigner crypto.Signer,
	serverCert tls.Certificate,
) (*managedSSHTunnel, error) {
	inner := EnrollmentHandler(pm.store, RPCHandlerWithCertificateGrants(pm.store), caCert, caSigner)
	scoped := scopedProjectHandler(projectID, inner)

	t := &managedSSHTunnel{
		projectID: projectID,
		cfg:       cfg,
		state:     "running",
	}
	if len(serverCert.Certificate) > 0 {
		t.certRaw = serverCert.Certificate[0]
	}

	bindAddr := "127.0.0.1:0"
	if cfg.Mode == "direct" {
		bindAddr = net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.LocalPort))
	}
	listener, srv, localPort, localAddr, err := startMTLSListener(bindAddr, caCert, serverCert, scoped, &t.errMsg, &t.mu, nil)
	if err != nil {
		return nil, fmt.Errorf("start isolated SSH listener (%s) for project %s: %w", bindAddr, projectID, err)
	}
	t.listener = listener
	t.httpServer = srv
	t.localPort = localPort
	t.localAddr = localAddr

	if cfg.Mode == "relay" {
		sshBin, err := FindSSHBinary()
		if err != nil {
			pm.stopSSHTunnel(t)
			return nil, err
		}
		args, err := BuildReverseSSHArgs(SSHTunnelSpec{
			Target:       cfg.Target,
			SSHPort:      cfg.SSHPort,
			RemotePort:   cfg.RemotePort,
			IdentityFile: cfg.IdentityFile,
		}, localPort)
		if err != nil {
			pm.stopSSHTunnel(t)
			return nil, err
		}
		procCtx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		t.mu.Lock()
		t.gen++
		gen := t.gen
		t.cancel = cancel
		t.done = done
		t.state = "starting"
		t.errMsg = ""
		t.mu.Unlock()
		go superviseProjectSSH(procCtx, t, gen, sshBin, args, done)
	}
	return t, nil
}

func (pm *PeerRuntime) stopSSHTunnel(t *managedSSHTunnel) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.gen++
	cancel := t.cancel
	done := t.done
	t.cancel = nil
	t.done = nil
	t.state = "disabled"
	t.errMsg = ""
	t.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
	if t.httpServer != nil {
		ctx, cancelSrv := context.WithTimeout(context.Background(), 2*time.Second)
		_ = t.httpServer.Shutdown(ctx)
		cancelSrv()
	}
	if t.listener != nil {
		_ = t.listener.Close()
	}
}

func superviseProjectSSH(ctx context.Context, t *managedSSHTunnel, gen uint64, binPath string, args []string, done chan struct{}) {
	defer close(done)
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		cmd := exec.CommandContext(ctx, binPath, args...)
		var stderr tailBuffer
		stderr.max = 2048
		cmd.Stdout = &stderr
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.mu.Lock()
			if t.gen == gen {
				t.state = "error"
				t.errMsg = strings.TrimSpace(err.Error())
			}
			t.mu.Unlock()
			return
		}
		t.mu.Lock()
		if t.gen == gen {
			t.state = "running"
			t.errMsg = ""
		}
		t.mu.Unlock()

		started := time.Now()
		waitErr := cmd.Wait()
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > 15*time.Second {
			backoff = time.Second
		}
		msg := "SSH relay exited unexpectedly"
		if waitErr != nil {
			msg = waitErr.Error()
		}
		if out := strings.TrimSpace(stderr.String()); out != "" {
			msg = lastNonEmptyLine(out)
		}
		t.mu.Lock()
		if t.gen == gen {
			t.state = "error"
			t.errMsg = strings.TrimSpace(msg)
		}
		t.mu.Unlock()

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 15*time.Second {
			backoff *= 2
		}
	}
}

func projectTunnelSlug(projectID string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(projectID)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	prefix := b.String()
	if len(prefix) > 20 {
		prefix = prefix[:20]
	}
	if prefix == "" {
		prefix = "proj"
	}
	sum := sha256.Sum256([]byte(projectID))
	return prefix + "-" + hex.EncodeToString(sum[:4])
}

func superviseProjectFRP(ctx context.Context, t *managedFRPTunnel, gen uint64, binPath, tomlPath, token string, done chan struct{}) {
	defer close(done)
	base := filepath.Base(binPath)
	var args []string
	if strings.HasPrefix(base, "gossip-frp") {
		args = []string{"client", "-c", tomlPath}
	} else {
		args = []string{"-c", tomlPath}
	}

	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		cmd := exec.CommandContext(ctx, binPath, args...)
		var stderr tailBuffer
		stderr.max = 2048
		cmd.Stdout = &stderr
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.mu.Lock()
			if t.gen == gen {
				t.state = "error"
				t.errMsg = sanitizeFRPError(err.Error(), token)
			}
			t.mu.Unlock()
			return
		}
		t.mu.Lock()
		if t.gen == gen {
			t.state = "running"
			t.errMsg = ""
		}
		t.mu.Unlock()

		started := time.Now()
		waitErr := cmd.Wait()
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > 15*time.Second {
			backoff = time.Second
		}
		msg := "FRP tunnel exited unexpectedly"
		if waitErr != nil {
			msg = waitErr.Error()
		}
		if out := strings.TrimSpace(stderr.String()); out != "" {
			msg = lastNonEmptyLine(out)
		}
		t.mu.Lock()
		if t.gen == gen {
			t.state = "error"
			t.errMsg = sanitizeFRPError(msg, token)
		}
		t.mu.Unlock()

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 15*time.Second {
			backoff *= 2
		}
	}
}

func (pm *PeerRuntime) buildProjectFRPStatusLocked(pid string, pcfg ProjectFRPConfig) ProjectFRPStatus {
	srvPort := pcfg.ServerPort
	if srvPort == 0 {
		srvPort = defaultFRPServerPort
	}
	remPort := pcfg.RemotePort
	if remPort == 0 {
		remPort = defaultFRPRemotePort
	}
	ep := ""
	if pcfg.Server != "" {
		ep = "https://" + net.JoinHostPort(pcfg.Server, strconv.Itoa(remPort))
	}
	st := ProjectFRPStatus{
		ProjectID:       pid,
		Enabled:         pcfg.Enabled,
		Server:          pcfg.Server,
		ServerPort:      srvPort,
		RemotePort:      remPort,
		TokenConfigured: pcfg.Token != "",
		TokenMasked:     tunnel.MaskSecret(pcfg.Token),
		Endpoint:        ep,
		State:           "disabled",
	}
	if active, ok := pm.frpTunnels[pid]; ok {
		active.mu.Lock()
		st.State = active.state
		st.Error = active.errMsg
		active.mu.Unlock()
		st.LocalAddr = active.localAddr
	}
	return st
}

func (pm *PeerRuntime) buildProjectSSHStatusLocked(pid string, scfg ProjectSSHConfig) ProjectSSHStatus {
	mode := strings.ToLower(strings.TrimSpace(scfg.Mode))
	if mode == "" {
		mode = "direct"
	}
	localPort := scfg.LocalPort
	if localPort == 0 {
		localPort = defaultSSHRemotePort
	}
	sshPort := scfg.SSHPort
	if sshPort == 0 {
		sshPort = defaultSSHPort
	}
	remPort := scfg.RemotePort
	if remPort == 0 {
		remPort = defaultSSHRemotePort
	}
	epPort := localPort
	if mode == "relay" {
		epPort = remPort
	}
	ep := "https://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(epPort))
	st := ProjectSSHStatus{
		ProjectID:    pid,
		Enabled:      scfg.Enabled,
		Mode:         mode,
		LocalPort:    localPort,
		Target:       scfg.Target,
		SSHPort:      sshPort,
		RemotePort:   remPort,
		IdentityFile: scfg.IdentityFile,
		Endpoint:     ep,
		State:        "disabled",
	}
	if active, ok := pm.sshTunnels[pid]; ok {
		active.mu.Lock()
		st.State = active.state
		st.Error = active.errMsg
		active.mu.Unlock()
		st.LocalAddr = active.localAddr
		if active.cfg.Mode == "direct" && active.localPort > 0 {
			st.LocalPort = active.localPort
			st.Endpoint = "https://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(active.localPort))
		}
	}
	return st
}

func (pm *PeerRuntime) buildProjectLANStatusLocked(pid string, lcfg ProjectLANConfig, lanIPs []string) ProjectLANStatus {
	port := lcfg.ListenPort
	if port == 0 {
		port = defaultPeerListenPort
	}
	st := ProjectLANStatus{
		ProjectID:  pid,
		Enabled:    lcfg.Enabled,
		ListenPort: port,
		State:      "disabled",
	}
	displayPort := port
	if active, ok := pm.lanListeners[pid]; ok && active.listener != nil {
		active.mu.Lock()
		st.Error = active.errMsg
		active.mu.Unlock()
		st.ActivePort = active.activePort
		st.LocalAddr = active.activeAddr
		if active.activePort > 0 {
			displayPort = active.activePort
		}
		if st.Error != "" {
			st.State = "error"
		} else {
			st.State = "running"
		}
	} else if lcfg.Enabled {
		st.State = "stopped"
	}
	for _, ip := range lanIPs {
		st.Endpoints = append(st.Endpoints, "https://"+net.JoinHostPort(ip, strconv.Itoa(displayPort)))
	}
	return st
}

func (pm *PeerRuntime) statusLocked(remoteEnabled bool, projectID string) PeerAccessStatus {
	lanIPs := orderedLANIPs()
	port := pm.cfg.ListenPort
	if port == 0 {
		port = defaultPeerListenPort
	}
	displayPort := port
	if pm.activePort > 0 {
		displayPort = pm.activePort
	}

	lanPIDs := make([]string, 0, len(pm.cfg.ProjectLAN))
	for pid := range pm.cfg.ProjectLAN {
		lanPIDs = append(lanPIDs, pid)
	}
	sort.Strings(lanPIDs)
	lanListeners := make([]ProjectLANStatus, 0, len(lanPIDs))
	for _, pid := range lanPIDs {
		lanListeners = append(lanListeners, pm.buildProjectLANStatusLocked(pid, pm.cfg.ProjectLAN[pid], lanIPs))
	}

	lanEnabled := pm.cfg.LANEnabled
	activePort := pm.activePort
	activeAddr := pm.activeAddr
	listenerRunning := pm.listener != nil
	listenerError := pm.listenerError

	if projectID != "" {
		if lcfg, ok := pm.cfg.ProjectLAN[projectID]; ok {
			pst := pm.buildProjectLANStatusLocked(projectID, lcfg, lanIPs)
			lanEnabled = pst.Enabled
			port = pst.ListenPort
			displayPort = port
			if pst.ActivePort > 0 {
				displayPort = pst.ActivePort
			}
			activePort = pst.ActivePort
			activeAddr = pst.LocalAddr
			listenerRunning = pst.State == "running"
			listenerError = pst.Error
		} else if !pm.cfg.LANEnabled {
			// Suggest a distinct free port for this project so separate projects never collide.
			if isLANPortClaimedByOther(pm.cfg, projectID, port) {
				port = pickNextFreeLANPort(pm.cfg, projectID)
				displayPort = port
			}
			lanEnabled = false
			activePort = 0
			activeAddr = ""
			listenerRunning = false
		}
	} else if !lanEnabled && len(lanListeners) > 0 {
		for _, ls := range lanListeners {
			if ls.Enabled {
				lanEnabled = true
				port = ls.ListenPort
				displayPort = ls.ListenPort
				if ls.ActivePort > 0 {
					displayPort = ls.ActivePort
					activePort = ls.ActivePort
				}
				activeAddr = ls.LocalAddr
				listenerRunning = ls.State == "running"
				listenerError = ls.Error
				break
			}
		}
	}

	endpoints := make([]string, 0, len(lanIPs))
	for _, ip := range lanIPs {
		endpoints = append(endpoints, "https://"+net.JoinHostPort(ip, strconv.Itoa(displayPort)))
	}
	frpBin, frpErr := findFRPBinary()
	frpBinName := ""
	if frpErr == nil {
		frpBinName = filepath.Base(frpBin)
	}
	_, sshErr := FindSSHBinary()

	pids := make([]string, 0, len(pm.cfg.ProjectFRP))
	for pid := range pm.cfg.ProjectFRP {
		pids = append(pids, pid)
	}
	sort.Strings(pids)
	tunnels := make([]ProjectFRPStatus, 0, len(pids))
	for _, pid := range pids {
		tunnels = append(tunnels, pm.buildProjectFRPStatusLocked(pid, pm.cfg.ProjectFRP[pid]))
	}

	relayPort := pm.cfg.FRPRelay.ServerPort
	if relayPort == 0 {
		relayPort = defaultFRPServerPort
	}
	localRelayState := "disabled"
	localRelayErr := ""
	if pm.frpRelay != nil {
		pm.frpRelay.mu.Lock()
		localRelayState = pm.frpRelay.state
		localRelayErr = pm.frpRelay.errMsg
		pm.frpRelay.mu.Unlock()
	} else if pm.cfg.FRPRelay.LocalServerActive {
		localRelayState = "stopped"
	}

	selStatus := ProjectFRPStatus{
		ProjectID:       projectID,
		Server:          pm.cfg.FRPRelay.Server,
		ServerPort:      relayPort,
		RemotePort:      pickNextFreeFRPRemotePort(pm.cfg, pm.cfg.FRPRelay.Server, projectID),
		TokenConfigured: pm.cfg.FRPRelay.Token != "",
		TokenMasked:     tunnel.MaskSecret(pm.cfg.FRPRelay.Token),
		State:           "disabled",
	}
	if selStatus.Server != "" {
		selStatus.Endpoint = "https://" + net.JoinHostPort(selStatus.Server, strconv.Itoa(selStatus.RemotePort))
	}
	if projectID != "" {
		if pcfg, ok := pm.cfg.ProjectFRP[projectID]; ok {
			selStatus = pm.buildProjectFRPStatusLocked(projectID, pcfg)
		}
	}

	sshPIDs := make([]string, 0, len(pm.cfg.ProjectSSH))
	for pid := range pm.cfg.ProjectSSH {
		sshPIDs = append(sshPIDs, pid)
	}
	sort.Strings(sshPIDs)
	sshTunnels := make([]ProjectSSHStatus, 0, len(sshPIDs))
	for _, pid := range sshPIDs {
		sshTunnels = append(sshTunnels, pm.buildProjectSSHStatusLocked(pid, pm.cfg.ProjectSSH[pid]))
	}
	selSSH := ProjectSSHStatus{
		ProjectID:  projectID,
		Mode:       "direct",
		LocalPort:  defaultSSHRemotePort,
		SSHPort:    defaultSSHPort,
		RemotePort: defaultSSHRemotePort,
		State:      "disabled",
	}
	if projectID != "" {
		if scfg, ok := pm.cfg.ProjectSSH[projectID]; ok {
			selSSH = pm.buildProjectSSHStatusLocked(projectID, scfg)
		}
	}

	dashPort := pm.dashboardPort
	if dashPort == 0 {
		if pm.cfg.DashboardPort > 0 {
			dashPort = pm.cfg.DashboardPort
		} else {
			dashPort = DefaultDashboardPort
		}
	}

	return PeerAccessStatus{
		DashboardPort:           dashPort,
		Enabled:                 remoteEnabled,
		LANEnabled:              lanEnabled,
		ListenPort:              port,
		ActivePort:              activePort,
		ActiveListenAddr:        activeAddr,
		DetectedLANIPs:          lanIPs,
		LANEndpoints:            endpoints,
		LANListeners:            lanListeners,
		ProjectID:               projectID,
		FRPEnabled:              selStatus.Enabled,
		FRPServer:               selStatus.Server,
		FRPServerPort:           selStatus.ServerPort,
		FRPRemotePort:           selStatus.RemotePort,
		FRPTokenConfigured:      selStatus.TokenConfigured,
		FRPTokenMasked:          selStatus.TokenMasked,
		FRPEndpoint:             selStatus.Endpoint,
		FRPLocalAddr:            selStatus.LocalAddr,
		FRPBinaryAvailable:      frpErr == nil,
		FRPBinaryName:           frpBinName,
		FRPState:                selStatus.State,
		FRPError:                selStatus.Error,
		FRPTunnels:              tunnels,
		FRPRelayServer:          pm.cfg.FRPRelay.Server,
		FRPRelayServerPort:      relayPort,
		FRPRelayTokenConfigured: pm.cfg.FRPRelay.Token != "",
		FRPRelayTokenMasked:     tunnel.MaskSecret(pm.cfg.FRPRelay.Token),
		FRPLocalRelayEnabled:    pm.cfg.FRPRelay.LocalServerActive,
		FRPLocalRelayState:      localRelayState,
		FRPLocalRelayError:      localRelayErr,
		SSHEnabled:              selSSH.Enabled,
		SSHMode:            selSSH.Mode,
		SSHLocalPort:       selSSH.LocalPort,
		SSHTarget:          selSSH.Target,
		SSHPort:            selSSH.SSHPort,
		SSHRemotePort:      selSSH.RemotePort,
		SSHIdentityFile:    selSSH.IdentityFile,
		SSHEndpoint:        selSSH.Endpoint,
		SSHLocalAddr:       selSSH.LocalAddr,
		SSHBinaryAvailable: sshErr == nil,
		SSHState:           selSSH.State,
		SSHError:           selSSH.Error,
		SSHTunnels:         sshTunnels,
		HostFingerprint:    HostCertSSHFingerprint(pm.caPEM),
		ListenerRunning:    listenerRunning,
		ListenerError:      listenerError,
		ServerCA:           pm.caPEM,
	}
}

func (pm *PeerRuntime) configFilePath() string {
	return filepath.Join(pm.configDir, "peer-access.json")
}

func (pm *PeerRuntime) loadConfigLocked() (PeerAccessConfig, error) {
	cfg := PeerAccessConfig{
		ListenPort: defaultPeerListenPort,
	}
	data, err := os.ReadFile(pm.configFilePath())
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	if err = json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("read peer-access config: %w", err)
	}
	if cfg.ListenPort == 0 || (!cfg.LANEnabled && cfg.ListenPort == 8443) {
		cfg.ListenPort = defaultPeerListenPort
	}
	for pid, lcfg := range cfg.ProjectLAN {
		if !lcfg.Enabled && lcfg.ListenPort == 8443 {
			lcfg.ListenPort = defaultPeerListenPort
			cfg.ProjectLAN[pid] = lcfg
		}
	}
	return cfg, nil
}

func (pm *PeerRuntime) saveConfigLocked(cfg PeerAccessConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateFile(pm.configFilePath(), append(data, '\n'), 0600)
}

func findFRPBinary() (string, error) {
	if override := strings.TrimSpace(os.Getenv("GOSSIP_FRP_BIN")); override != "" {
		info, err := os.Stat(override)
		if err == nil && !info.IsDir() {
			return override, nil
		}
		return "", fmt.Errorf("configured GOSSIP_FRP_BIN (%s) is unavailable", override)
	}
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		if resolved, rErr := filepath.EvalSymlinks(exe); rErr == nil {
			exe = resolved
		}
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), frpExeName("gossip-frp")))
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), frpExeName("frpc")))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".local", "share", "gossip", "bin", frpExeName("gossip-frp")),
			filepath.Join(home, ".local", "bin", frpExeName("gossip-frp")),
		)
	}
	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	for _, name := range []string{"gossip-frp", "frpc"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", errors.New("gossip-frp (or frpc) binary not found; build/install gossip-frp beside gossip to enable FRP mode")
}

func orderedLANIPs() []string {
	ips := tunnel.DetectLocalLANIPs()
	if len(ips) <= 1 {
		return ips
	}
	primary := ""
	if conn, err := net.DialTimeout("udp4", "1.1.1.1:80", 50*time.Millisecond); err == nil {
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok && addr.IP != nil && addr.IP.To4() != nil && !addr.IP.IsLoopback() {
			primary = addr.IP.String()
		}
		_ = conn.Close()
	}
	if primary == "" || !slices.Contains(ips, primary) {
		return ips
	}
	out := make([]string, 0, len(ips))
	out = append(out, primary)
	for _, ip := range ips {
		if ip != primary {
			out = append(out, ip)
		}
	}
	return out
}

func frpExeName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

func ensureHostPKI(configDir string, extraSANs []string) (*x509.Certificate, crypto.Signer, tls.Certificate, string, error) {
	pkiDir := filepath.Join(configDir, "pki")
	if err := os.MkdirAll(pkiDir, 0700); err != nil {
		return nil, nil, tls.Certificate{}, "", err
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(pkiDir, 0700)
	}
	caCertPath := filepath.Join(pkiDir, "ca.crt")
	caKeyPath := filepath.Join(pkiDir, "ca.key")
	srvCertPath := filepath.Join(pkiDir, "server.crt")
	srvKeyPath := filepath.Join(pkiDir, "server.key")

	caCert, caSigner, caPEMBytes, err := loadOrCreateCA(caCertPath, caKeyPath)
	if err != nil {
		return nil, nil, tls.Certificate{}, "", err
	}
	wantIPs, wantDNS := buildServerSANs(extraSANs)
	srvCert, err := loadValidServerCert(srvCertPath, srvKeyPath, caCert, wantIPs, wantDNS)
	if err == nil {
		if len(srvCert.Certificate) == 1 {
			srvCert.Certificate = append(srvCert.Certificate, caCert.Raw)
		}
		return caCert, caSigner, srvCert, string(caPEMBytes), nil
	}

	srvKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, tls.Certificate{}, "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, tls.Certificate{}, "", err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "Gossip Peer Coordinator"},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  wantIPs,
		DNSNames:     wantDNS,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &srvKey.PublicKey, caSigner)
	if err != nil {
		return nil, nil, tls.Certificate{}, "", err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(srvKey)
	if err != nil {
		return nil, nil, tls.Certificate{}, "", err
	}
	srvCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	srvKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := writePrivateFile(srvCertPath, srvCertPEM, 0644); err != nil {
		return nil, nil, tls.Certificate{}, "", err
	}
	if err := writePrivateFile(srvKeyPath, srvKeyPEM, 0600); err != nil {
		return nil, nil, tls.Certificate{}, "", err
	}
	pair, err := tls.X509KeyPair(srvCertPEM, srvKeyPEM)
	if err != nil {
		return nil, nil, tls.Certificate{}, "", err
	}
	if len(pair.Certificate) == 1 {
		pair.Certificate = append(pair.Certificate, caCert.Raw)
	}
	return caCert, caSigner, pair, string(caPEMBytes), nil
}

func loadOrCreateCA(certPath, keyPath string) (*x509.Certificate, crypto.Signer, []byte, error) {
	if certPEM, err := os.ReadFile(certPath); err == nil {
		if pair, pErr := tls.LoadX509KeyPair(certPath, keyPath); pErr == nil && len(pair.Certificate) == 1 {
			if cert, cErr := x509.ParseCertificate(pair.Certificate[0]); cErr == nil && cert.IsCA && time.Now().Add(30*24*time.Hour).Before(cert.NotAfter) {
				if signer, ok := pair.PrivateKey.(crypto.Signer); ok {
					return cert, signer, certPEM, nil
				}
			}
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Gossip Local Home CA"},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := writePrivateFile(certPath, certPEM, 0644); err != nil {
		return nil, nil, nil, err
	}
	if err := writePrivateFile(keyPath, keyPEM, 0600); err != nil {
		return nil, nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, err
	}
	return cert, key, certPEM, nil
}

func buildServerSANs(extra []string) ([]net.IP, []string) {
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	dns := []string{"localhost"}
	if host, err := os.Hostname(); err == nil && host != "" && !strings.ContainsAny(host, " \t\r\n") {
		if ip := net.ParseIP(host); ip != nil {
			ips = appendIPUnique(ips, ip)
		} else {
			dns = appendStringUnique(dns, host)
		}
	}
	for _, lan := range tunnel.DetectLocalLANIPs() {
		if ip := net.ParseIP(lan); ip != nil {
			ips = appendIPUnique(ips, ip)
		}
	}
	for _, entry := range extra {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if ip := net.ParseIP(entry); ip != nil {
			ips = appendIPUnique(ips, ip)
		} else {
			dns = appendStringUnique(dns, entry)
		}
	}
	return ips, dns
}

func appendIPUnique(list []net.IP, ip net.IP) []net.IP {
	for _, existing := range list {
		if existing.Equal(ip) {
			return list
		}
	}
	return append(list, ip)
}

func appendStringUnique(list []string, s string) []string {
	if slices.Contains(list, s) {
		return list
	}
	return append(list, s)
}

func loadValidServerCert(certPath, keyPath string, ca *x509.Certificate, wantIPs []net.IP, wantDNS []string) (tls.Certificate, error) {
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil || len(pair.Certificate) == 0 {
		return tls.Certificate{}, errors.New("missing server certificate")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return tls.Certificate{}, err
	}
	if time.Now().Add(14 * 24 * time.Hour).After(leaf.NotAfter) {
		return tls.Certificate{}, errors.New("server certificate expiring soon")
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	if _, err = leaf.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return tls.Certificate{}, err
	}
	for _, ip := range wantIPs {
		found := false
		for _, have := range leaf.IPAddresses {
			if have.Equal(ip) {
				found = true
				break
			}
		}
		if !found {
			return tls.Certificate{}, errors.New("server certificate missing SAN IP")
		}
	}
	for _, name := range wantDNS {
		if !slices.Contains(leaf.DNSNames, name) {
			return tls.Certificate{}, errors.New("server certificate missing SAN DNS")
		}
	}
	return pair, nil
}

func writePrivateFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if runtime.GOOS != "windows" {
		if err = tmp.Chmod(mode); err != nil {
			_ = tmp.Close()
			return err
		}
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func sanitizeFRPError(msg, token string) string {
	if token != "" {
		msg = strings.ReplaceAll(msg, token, "***")
	}
	return strings.TrimSpace(msg)
}

func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			return line
		}
	}
	return ""
}

type tailBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (tb *tailBuffer) Write(p []byte) (int, error) {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	if tb.max <= 0 {
		tb.max = 2048
	}
	tb.buf.Write(p)
	if tb.buf.Len() > tb.max {
		b := tb.buf.Bytes()
		tb.buf.Reset()
		tb.buf.Write(b[len(b)-tb.max:])
	}
	return len(p), nil
}

func (tb *tailBuffer) String() string {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return tb.buf.String()
}
