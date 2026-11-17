package coord

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed ui/* ui/css/* ui/js/*
var webAssets embed.FS

type browserSession struct {
	projects  map[string]string
	clientPin string
	expires   time.Time
}

// WebHandler serves the coordination UI. Loopback is available to the local OS
// user; remote HTTPS deployments require enrolled client certificate grants.
type WebBackend interface {
	Backend
	RemoteAccessEnabled(context.Context) (bool, error)
	CreateProjectWithWorkspace(context.Context, string, string, string, string, GitObservation) (Project, Credential, Repository, Workspace, error)
	CreatePreparedProjectWithWorkspace(context.Context, string, string, string, string, string, GitObservation, Credential) (Project, Credential, Repository, Workspace, error)
	EventRetentionStatus(context.Context, string, string) (EventRetention, error)
	WhoAmI(context.Context, string, string) (Principal, error)
	FindRepositoriesByRemote(context.Context, string, string, string) ([]Repository, error)
	AddPreparedPrincipal(context.Context, string, string, string, string, Credential) (Credential, error)
	AddPreparedRemotePrincipal(context.Context, string, string, string, string, Credential, string, int64) (Credential, error)
	TaskCounts(context.Context, string, string) (map[string]int, error)
	RecentEvents(context.Context, string, string, int) ([]Event, error)
	RecentMessages(context.Context, string, string, int) ([]Message, error)
	ConversationMessages(context.Context, string, string, string, int64, int64, int, bool) ([]Message, error)
	ListLegacyImports(context.Context, string, string) ([]LegacyImport, error)
	ReadLegacyRecords(context.Context, string, string, string, string, string, int) ([]LegacyRecord, error)
	ReadLegacyRecordChunk(context.Context, string, string, string, string, string, int, int) (LegacyRecordChunk, error)
	GetCandidatePage(context.Context, string, string, string, int, int) (CandidatePage, error)
}

type WebHandler struct {
	store                    WebBackend
	localStore               LocalDashboardBackend
	queryStore               DashboardQueryBackend
	channelStore             ChannelBackend
	registry                 *DeviceRegistry
	peerManager              *PeerRuntime
	profile                  string
	homeID                   string
	localAuthority           bool
	mu                       sync.Mutex
	sessions                 map[string]*browserSession
	allowedHosts             map[string]bool
	clientProjects           map[string]map[string]bool
	dynamicCertificateGrants bool
}

type ProjectSuggestion struct {
	ProjectID      string `json:"project_id"`
	ProjectName    string `json:"project_name"`
	RepositoryID   string `json:"repository_id"`
	RepositoryName string `json:"repository_name"`
}

// ProjectOverview summarizes only projects already connected to the browser
// session and still authorized for its client certificate.
type ProjectOverview struct {
	ProjectID    string         `json:"project_id"`
	ProjectName  string         `json:"project_name"`
	PrincipalID  string         `json:"principal_id"`
	Principal    string         `json:"principal_name"`
	Role         string         `json:"role"`
	ActiveAgents int            `json:"active_agents"`
	PlanRevision int            `json:"plan_revision"`
	Counts       map[string]int `json:"counts"`
	RecentEvents []Event        `json:"recent_events"`
}

func NewWebHandler(store WebBackend) *WebHandler {
	qs, _ := store.(DashboardQueryBackend)
	cs, _ := store.(ChannelBackend)
	return &WebHandler{store: store, queryStore: qs, channelStore: cs, sessions: map[string]*browserSession{}, allowedHosts: map[string]bool{}, clientProjects: map[string]map[string]bool{}}
}

// NewWebHandlerWithRegistry gives the loopback dashboard explicit local
// device-owner access when using the default human profile and read-only
// access to joined remote connections stored privately in the registry.
func NewWebHandlerWithRegistry(store WebBackend, registry *DeviceRegistry, profile, homeID string) *WebHandler {
	h := NewWebHandler(store)
	h.registry, h.profile, h.homeID = registry, profile, homeID
	if h.profile == "" {
		h.profile = "default"
	}
	if h.profile == "default" {
		if lb, ok := store.(LocalDashboardBackend); ok {
			h.localStore = lb
			h.localAuthority = true
		}
	}
	return h
}

func (h *WebHandler) SetPeerRuntime(pm *PeerRuntime) {
	h.peerManager = pm
}

// NewWebHandlerWithHosts enables an explicit HTTPS deployment host allowlist.
func NewWebHandlerWithHosts(store WebBackend, hosts []string) *WebHandler {
	h := NewWebHandler(store)
	for _, host := range hosts {
		h.allowedHosts[strings.ToLower(host)] = true
	}
	return h
}

// NewWebHandlerWithProjectPins adds a process-local allowlist; database-backed
// certificate/project grants remain mandatory.
func NewWebHandlerWithProjectPins(store WebBackend, hosts []string, access map[string][]string) *WebHandler {
	h := NewWebHandlerWithHosts(store, hosts)
	h.dynamicCertificateGrants = true
	for pin, projects := range access {
		grant := make(map[string]bool, len(projects))
		for _, project := range projects {
			grant[project] = true
		}
		h.clientProjects[strings.ToLower(strings.ReplaceAll(pin, ":", ""))] = grant
	}
	return h
}

func NewWebHandlerWithCertificateGrants(store WebBackend, hosts []string) *WebHandler {
	h := NewWebHandlerWithHosts(store, hosts)
	h.dynamicCertificateGrants = true
	return h
}

func webClientPin(r *http.Request) string {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return ""
	}
	sum := sha256.Sum256(r.TLS.PeerCertificates[0].Raw)
	return hex.EncodeToString(sum[:])
}

func (h *WebHandler) clientProjectAllowed(ctx context.Context, pin, project string) bool {
	if pin == "" {
		return true // Loopback sessions use the local OS trust boundary.
	}
	if h.dynamicCertificateGrants {
		if len(h.clientProjects) > 0 && !h.clientProjects[pin][project] {
			return false
		}
		allowed, err := h.store.CertificateAuthorized(ctx, project, pin)
		return err == nil && allowed
	}
	return h.clientProjects[pin][project]
}
func (h *WebHandler) hostAllowed(r *http.Request) bool {
	if r.TLS != nil {
		return h.allowedHosts[strings.ToLower(r.Host)]
	}
	return localHost(r.Host) && loopbackPeer(r.RemoteAddr)
}

func loopbackPeer(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func localHost(hostport string) bool {
	host := hostport
	if h, port, err := net.SplitHostPort(hostport); err == nil {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
func isSingleSegmentAsset(reqPath, prefix, ext string) bool {
	if !strings.HasPrefix(reqPath, prefix) || !strings.HasSuffix(reqPath, ext) {
		return false
	}
	name := strings.TrimPrefix(reqPath, prefix)
	return name != "" && !strings.Contains(name, "/") && !strings.Contains(name, "..")
}

func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	u, err := url.Parse(o)
	return err == nil && ((u.Scheme == "http" && r.TLS == nil) || (u.Scheme == "https" && r.TLS != nil)) && strings.EqualFold(u.Host, r.Host) && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}

func hasForwardedHeaders(r *http.Request) bool {
	for _, name := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-Port", "X-Real-Ip", "Via"} {
		if strings.TrimSpace(r.Header.Get(name)) != "" {
			return true
		}
	}
	return false
}

func hasCrossSiteBrowserRequest(r *http.Request) bool {
	if sfs := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))); sfs != "" {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if sfs != "same-origin" {
				return true
			}
		} else if sfs != "same-origin" && sfs != "none" {
			return true
		}
	}
	if strings.HasPrefix(r.URL.Path, "/api/") && r.Header.Get("Origin") != "" && !sameOrigin(r) {
		return true
	}
	return false
}

func (h *WebHandler) hasLocalDashboardAccess(r *http.Request) bool {
	return h.localAuthority &&
		h.localStore != nil &&
		!h.dynamicCertificateGrants &&
		h.profile == "default" &&
		r.TLS == nil &&
		localHost(r.Host) &&
		loopbackPeer(r.RemoteAddr) &&
		!hasForwardedHeaders(r) &&
		!hasCrossSiteBrowserRequest(r)
}

func (h *WebHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	if !h.hostAllowed(r) {
		http.Error(w, "invalid host", http.StatusForbidden)
		return
	}
	if r.TLS == nil && hasForwardedHeaders(r) {
		http.Error(w, "forwarded requests are not allowed on the local dashboard", http.StatusForbidden)
		return
	}
	if r.TLS != nil {
		enabled, err := h.store.RemoteAccessEnabled(r.Context())
		if err != nil || !enabled {
			http.Error(w, "remote dashboard is disabled by the local kill switch", http.StatusServiceUnavailable)
			return
		}
		pin := webClientPin(r)
		if len(r.TLS.VerifiedChains) == 0 || pin == "" || (!h.dynamicCertificateGrants && len(h.clientProjects[pin]) == 0) {
			http.Error(w, "client certificate is not enrolled", http.StatusForbidden)
			return
		}
	}
	if strings.HasPrefix(r.URL.Path, "/api/") && hasCrossSiteBrowserRequest(r) {
		http.Error(w, "cross-site API request forbidden", http.StatusForbidden)
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" && (!sameOrigin(r) || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json")) {
		http.Error(w, "same-origin JSON required", http.StatusForbidden)
		return
	}
	switch {
	case r.URL.Path == "/" && r.Method == "GET":
		data, err := webAssets.ReadFile("ui/index.html")
		if err != nil {
			http.Error(w, "UI unavailable", 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	case r.URL.Path == "/diagram.html" && r.Method == "GET":
		// Diagram rendering is isolated in a sandboxed, opaque-origin iframe.
		// Its local scripts cannot contact the network or navigate the parent.
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src "+scheme+"://"+r.Host+"; style-src 'unsafe-inline'; img-src data:; connect-src 'none'; frame-ancestors 'self'; base-uri 'none'; form-action 'none'")
		data, err := webAssets.ReadFile("ui/diagram.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	case r.URL.Path == "/favicon.ico" && r.Method == "GET":
		w.WriteHeader(http.StatusNoContent)
	case r.Method == "GET" && (r.URL.Path == "/app.js" || r.URL.Path == "/app.css" || r.URL.Path == "/diagram.js" || r.URL.Path == "/mermaid.min.js" || isSingleSegmentAsset(r.URL.Path, "/css/", ".css") || isSingleSegmentAsset(r.URL.Path, "/js/", ".js")):
		name := "ui" + r.URL.Path
		data, err := webAssets.ReadFile(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(name, ".js") {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		} else {
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		}
		_, _ = w.Write(data)
	case (r.URL.Path == "/openapi.json" || r.URL.Path == "/api/openapi.json" || r.URL.Path == "/v1/openapi.json") && r.Method == http.MethodGet:
		serveOpenAPIHTTP(w, r)
	case r.URL.Path == "/api/login" && r.Method == "POST":
		h.login(w, r)
	case r.URL.Path == "/api/remote-access" && r.Method == http.MethodGet:
		h.localRemoteAccess(w, r)
	case r.URL.Path == "/api/local/peer-access" && (r.Method == http.MethodGet || r.Method == http.MethodPost):
		h.localPeerAccess(w, r)
	case r.URL.Path == "/api/local/frp-relay-command" && r.Method == http.MethodPost:
		h.localFRPRelayCommand(w, r)
	case r.URL.Path == "/api/logout" && r.Method == "POST":
		h.logout(w, r)
	case r.URL.Path == "/api/projects" && r.Method == "GET":
		h.projects(w, r)
	case r.URL.Path == "/api/dashboard/projects" && r.Method == "GET":
		h.dashboardProjects(w, r)
	case r.URL.Path == "/api/local/inspect-checkout" && r.Method == http.MethodPost:
		h.inspectLocalCheckout(w, r)
	case r.URL.Path == "/api/local/pick-checkout" && r.Method == http.MethodPost:
		h.pickLocalCheckout(w, r)
	case r.URL.Path == "/api/local/accept-invitation" && r.Method == http.MethodPost:
		h.acceptInvitation(w, r)
	case r.URL.Path == "/api/local/probe-host" && r.Method == http.MethodPost:
		h.probePeerHost(w, r)
	case r.URL.Path == "/api/local/known-hosts" && (r.Method == http.MethodGet || r.Method == http.MethodPost || r.Method == http.MethodDelete):
		h.localKnownHosts(w, r)
	case r.URL.Path == "/api/local/join-remote" && r.Method == http.MethodPost:
		h.joinRemoteProject(w, r)
	case r.URL.Path == "/api/projects/create" && r.Method == "POST":
		h.createLocalProject(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/projects/") && strings.HasSuffix(r.URL.Path, "/recovery-credential") && r.Method == http.MethodPost:
		h.exportRecoveryCredential(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/projects/") && strings.HasSuffix(r.URL.Path, "/local-owner") && r.Method == http.MethodPost:
		h.selectLocalOwner(w, r)
	case r.URL.Path == "/api/overview" && r.Method == "GET":
		h.projectOverview(w, r)
	case r.URL.Path == "/api/recognize" && r.Method == "POST":
		h.recognizeProject(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/projects/"):
		h.project(w, r)
	default:
		http.NotFound(w, r)
	}
}

// localRemoteAccess exposes the network kill switch only to a loopback HTTP
// dashboard. TLS dashboard clients can use the dashboard but cannot toggle the
// host-wide remote-access boundary.
func (h *WebHandler) localRemoteAccess(w http.ResponseWriter, r *http.Request) {
	if r.TLS != nil || !localHost(r.Host) || !loopbackPeer(r.RemoteAddr) {
		http.Error(w, "remote access controls are available only from the local loopback dashboard", http.StatusForbidden)
		return
	}
	if h.peerManager != nil {
		status, err := h.peerManager.StatusForProject(r.Context(), r.URL.Query().Get("project"))
		if err != nil {
			jsonError(w, err)
			return
		}
		jsonReply(w, status)
		return
	}
	enabled, err := h.store.RemoteAccessEnabled(r.Context())
	if err != nil {
		jsonError(w, err)
		return
	}
	jsonReply(w, struct {
		Enabled        bool     `json:"enabled"`
		DetectedLANIPs []string `json:"detected_lan_ips,omitempty"`
	}{Enabled: enabled, DetectedLANIPs: orderedLANIPs()})
}

func (h *WebHandler) localPeerAccess(w http.ResponseWriter, r *http.Request) {
	if !h.hasLocalDashboardAccess(r) || r.TLS != nil || !localHost(r.Host) || !loopbackPeer(r.RemoteAddr) {
		http.Error(w, "peer access configuration is available only from the local loopback dashboard", http.StatusForbidden)
		return
	}
	if h.peerManager == nil {
		if r.Method == http.MethodGet {
			h.localRemoteAccess(w, r)
			return
		}
		http.Error(w, "managed peer access is unavailable on this backend", http.StatusNotImplemented)
		return
	}
	if r.Method == http.MethodGet {
		status, err := h.peerManager.StatusForProject(r.Context(), r.URL.Query().Get("project"))
		if err != nil {
			jsonError(w, err)
			return
		}
		jsonReply(w, status)
		return
	}
	var update PeerAccessUpdate
	if err := decodeBody(w, r, &update); err != nil {
		jsonError(w, err)
		return
	}
	status, err := h.peerManager.Configure(r.Context(), update)
	if err != nil {
		jsonError(w, err)
		return
	}
	jsonReply(w, status)
}

func (h *WebHandler) localFRPRelayCommand(w http.ResponseWriter, r *http.Request) {
	if !h.hasLocalDashboardAccess(r) || r.TLS != nil || !localHost(r.Host) || !loopbackPeer(r.RemoteAddr) {
		http.Error(w, "FRP relay command helper is available only from the local loopback dashboard", http.StatusForbidden)
		return
	}
	if h.peerManager == nil {
		http.Error(w, "managed peer access is unavailable on this backend", http.StatusNotImplemented)
		return
	}
	cmd, port, localActive, err := h.peerManager.FRPRelaySetupCommand(r.Context())
	if err != nil {
		jsonError(w, err)
		return
	}
	jsonReply(w, struct {
		Command          string `json:"command"`
		ServerPort       int    `json:"server_port"`
		LocalRelayActive bool   `json:"local_relay_active"`
	}{
		Command:          cmd,
		ServerPort:       port,
		LocalRelayActive: localActive,
	})
}

// createLocalProject is an onboarding shortcut for a dashboard bound to the
// local machine. Project creation stays unavailable to remote dashboard users.
func (h *WebHandler) createLocalProject(w http.ResponseWriter, r *http.Request) {
	if r.TLS != nil || !loopbackPeer(r.RemoteAddr) {
		http.Error(w, "project creation is available only from the local dashboard", http.StatusForbidden)
		return
	}
	var req struct {
		Name      string `json:"name"`
		LocalPath string `json:"local_path"`
		RequestID string `json:"request_id"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		jsonError(w, err)
		return
	}
	observation, err := InspectGitWorkspace(r.Context(), strings.TrimSpace(req.LocalPath))
	if err != nil {
		jsonError(w, err)
		return
	}
	baseName := filepath.Base(observation.Root)
	repositoryName := baseName
	if len(observation.RemoteHints) > 0 {
		parts := strings.Split(observation.RemoteHints[0], "/")
		if len(parts) > 0 && parts[len(parts)-1] != "" {
			repositoryName = parts[len(parts)-1]
		}
	}
	projectName := strings.TrimSpace(req.Name)
	if projectName == "" {
		projectName = repositoryName
	}
	ownerName := "Local owner"
	if current, e := user.Current(); e == nil && strings.TrimSpace(current.Username) != "" {
		ownerName = current.Username
	}
	node, _ := os.Hostname()
	var project Project
	var credential Credential
	var repository Repository
	var workspace Workspace
	if h.registry != nil {
		setupPayload := struct {
			Name, OwnerName, RepositoryName, Node string
			Observation                           GitObservation
		}{projectName, ownerName, repositoryName, node, observation}
		projectID, prepared, secretID, prepErr := h.registry.PrepareLocalProject(r.Context(), req.RequestID, setupPayload)
		if prepErr != nil {
			jsonError(w, prepErr)
			return
		}
		project, credential, repository, workspace, err = h.store.CreatePreparedProjectWithWorkspace(r.Context(), projectID, projectName, ownerName, repositoryName, node, observation, prepared)
		if err == nil {
			homeID := h.homeID
			if homeID == "" {
				err = errors.New("local coordinator identity is unavailable")
			} else {
				err = h.registry.Put(r.Context(), DeviceConnection{Profile: h.profile, HomeID: homeID, ProjectID: project.ID, PrincipalID: credential.PrincipalID, Name: project.Name, Checkout: observation.Root, SecretID: secretID}, credential.Token)
			}
		}
	} else {
		project, credential, repository, workspace, err = h.store.CreateProjectWithWorkspace(r.Context(), projectName, ownerName, repositoryName, node, observation)
	}
	if err != nil {
		jsonError(w, err)
		return
	}
	p := Principal{ID: credential.PrincipalID, Name: ownerName, Role: "owner", ProjectID: project.ID, ProjectName: project.Name}
	if h.localAuthority {
		h.ensureDeviceSession(w, r)
	} else {
		sessionID, session := h.session(r)
		newSession := session == nil
		if session == nil {
			var idErr error
			sessionID, idErr = newSessionID()
			if idErr != nil {
				http.Error(w, "session unavailable", http.StatusInternalServerError)
				return
			}
			session = &browserSession{projects: map[string]string{}, clientPin: ""}
		}
		session.projects[project.ID] = credential.Token
		session.expires = time.Now().Add(12 * time.Hour)
		h.mu.Lock()
		h.sessions[sessionID] = session
		h.mu.Unlock()
		// A cookie may exist but point to an in-memory session lost when the
		// dashboard restarted. Always replace it when creating a fresh session.
		if newSession {
			h.setSessionCookie(w, r, sessionID)
		}
	}
	response := map[string]any{"project": p, "repository": repository, "workspace": workspace, "credential_saved": h.registry != nil}
	// Preserve the legacy constructor behavior for embedded callers during the
	// migration; the installed local dashboard always uses registry mode.
	if h.registry == nil {
		response["credential"] = map[string]string{"project_id": project.ID, "principal_id": credential.PrincipalID, "token": credential.Token}
	}
	jsonReply(w, response)
}

func (h *WebHandler) exportRecoveryCredential(w http.ResponseWriter, r *http.Request) {
	if h.registry == nil || !h.hasLocalDashboardAccess(r) {
		http.Error(w, "recovery export is available only from the local dashboard", http.StatusForbidden)
		return
	}
	project := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/projects/"), "/recovery-credential")
	if requestedHome := strings.TrimSpace(r.URL.Query().Get("home")); requestedHome != "" && requestedHome != h.homeID {
		http.Error(w, "recovery export is only available for local projects owned on this device", http.StatusForbidden)
		return
	}
	backend, _, _, token, callCtx, isRemote, statusCode, statusMsg := h.resolveProjectCall(r, project)
	if statusCode != 0 {
		http.Error(w, statusMsg, statusCode)
		return
	}
	if isRemote {
		http.Error(w, "recovery export is only available for local projects owned on this device", http.StatusForbidden)
		return
	}
	principal, err := backend.WhoAmI(callCtx, project, token)
	if err != nil {
		jsonError(w, err)
		return
	}
	if principal.Role != "owner" {
		http.Error(w, "only the project owner can export its recovery credential", http.StatusForbidden)
		return
	}
	if token == "" {
		connections, err := h.registry.List(r.Context())
		if err != nil {
			jsonError(w, err)
			return
		}
		for _, c := range connections {
			if c.Profile == h.profile && c.HomeID == h.homeID && c.Endpoint == "" && c.ProjectID == project && c.PrincipalID == principal.ID {
				saved, err := h.registry.Token(c)
				if err != nil {
					continue
				}
				if valid, err := h.store.CredentialValid(r.Context(), project, saved); err == nil && valid {
					token = saved
					break
				}
			}
		}
		if token == "" {
			http.Error(w, "No saved recovery credential for this project", http.StatusNotFound)
			return
		}
	}
	jsonReply(w, map[string]string{"project_id": project, "principal_id": principal.ID, "token": token})
}

func (h *WebHandler) setSessionCookie(w http.ResponseWriter, r *http.Request, sid string) {
	http.SetCookie(w, &http.Cookie{Name: "gossip_session", Value: sid, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: 12 * 60 * 60})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "gossip_session", Value: "", Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

// recognizeProject searches only projects visible to the caller (local device
// projects on the loopback dashboard, or explicitly connected session projects).
func (h *WebHandler) recognizeProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RemoteURL string `json:"remote_url"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		jsonError(w, err)
		return
	}
	_, err := NormalizeRepositoryRemote(req.RemoteURL)
	if err != nil {
		jsonError(w, err)
		return
	}
	suggestions := []ProjectSuggestion{}
	if h.hasLocalDashboardAccess(r) {
		localCtx := withLocalOwnerAuthority(r.Context(), h.homeID)
		localProjects, lErr := h.localStore.ListLocalDashboardProjects(localCtx)
		if lErr != nil {
			http.Error(w, "project recognition unavailable", http.StatusInternalServerError)
			return
		}
		for _, lp := range localProjects {
			if lp.Status != "ready" || lp.Owner == nil {
				continue
			}
			repositories, listErr := h.store.FindRepositoriesByRemote(localCtx, lp.ProjectID, "", req.RemoteURL)
			if errors.Is(listErr, ErrUnauthorized) {
				continue
			}
			if listErr != nil {
				http.Error(w, "project recognition unavailable", http.StatusInternalServerError)
				return
			}
			for _, repository := range repositories {
				suggestions = append(suggestions, ProjectSuggestion{ProjectID: lp.ProjectID, ProjectName: lp.ProjectName, RepositoryID: repository.ID, RepositoryName: repository.Name})
			}
		}
	} else {
		_, session := h.session(r)
		if session == nil || session.clientPin != webClientPin(r) {
			jsonError(w, ErrUnauthorized)
			return
		}
		for project, token := range session.projects {
			if !h.clientProjectAllowed(r.Context(), session.clientPin, project) {
				continue
			}
			principal, authErr := h.store.WhoAmI(r.Context(), project, token)
			if errors.Is(authErr, ErrUnauthorized) {
				continue
			}
			if authErr != nil {
				http.Error(w, "project recognition unavailable", http.StatusInternalServerError)
				return
			}
			repositories, listErr := h.store.FindRepositoriesByRemote(r.Context(), project, token, req.RemoteURL)
			if errors.Is(listErr, ErrUnauthorized) {
				continue
			}
			if listErr != nil {
				http.Error(w, "project recognition unavailable", http.StatusInternalServerError)
				return
			}
			for _, repository := range repositories {
				suggestions = append(suggestions, ProjectSuggestion{ProjectID: project, ProjectName: principal.ProjectName, RepositoryID: repository.ID, RepositoryName: repository.Name})
			}
		}
	}
	sort.Slice(suggestions, func(i, j int) bool {
		if suggestions[i].ProjectName != suggestions[j].ProjectName {
			return suggestions[i].ProjectName < suggestions[j].ProjectName
		}
		return suggestions[i].RepositoryName < suggestions[j].RepositoryName
	})
	jsonReply(w, suggestions)
}
func jsonReply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func jsonError(w http.ResponseWriter, err error) {
	code := http.StatusBadRequest
	if errors.Is(err, ErrUnauthorized) {
		code = http.StatusUnauthorized
	} else if errors.Is(err, ErrNotFound) {
		code = http.StatusNotFound
	} else if errors.Is(err, ErrConflict) {
		code = http.StatusConflict
	}
	http.Error(w, err.Error(), code)
}

// projectError keeps authentication failures distinct from authorization
// failures. Store methods use ErrUnauthorized for both an invalid credential
// and a valid member lacking a capability, so validate the already-connected
// credential before translating the latter to HTTP 403.
func (h *WebHandler) projectError(w http.ResponseWriter, backend WebBackend, ctx context.Context, project, token string, err error) {
	if errors.Is(err, ErrUnauthorized) {
		valid, authErr := backend.CredentialValid(ctx, project, token)
		if authErr == nil && valid {
			http.Error(w, "this project role is not allowed to perform that action", http.StatusForbidden)
			return
		} else if authErr == nil || errors.Is(authErr, ErrUnauthorized) || errors.Is(authErr, ErrNotFound) {
			http.Error(w, "project access is no longer valid; reconnect a credential for this project", http.StatusUnauthorized)
			return
		} else {
			http.Error(w, "project authorization could not be checked", http.StatusInternalServerError)
			return
		}
	}
	jsonError(w, err)
}
func decodeBody(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1024*1024+8192)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err == nil {
		return errors.New("unexpected extra JSON")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
func newSessionID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func (h *WebHandler) session(r *http.Request) (string, *browserSession) {
	c, err := r.Cookie("gossip_session")
	if err != nil {
		return "", nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessions[c.Value]
	if s == nil {
		return "", nil
	}
	if time.Now().After(s.expires) {
		delete(h.sessions, c.Value)
		return "", nil
	}
	copy := &browserSession{projects: map[string]string{}, clientPin: s.clientPin, expires: s.expires}
	for id, token := range s.projects {
		copy.projects[id] = token
	}
	return c.Value, copy
}

func (h *WebHandler) ensureDeviceSession(w http.ResponseWriter, r *http.Request) {
	sid, s := h.session(r)
	if s != nil && sid != "" {
		return
	}
	newID, err := newSessionID()
	if err != nil {
		return
	}
	h.mu.Lock()
	h.sessions[newID] = &browserSession{projects: map[string]string{}, clientPin: "", expires: time.Now().Add(12 * time.Hour)}
	h.mu.Unlock()
	h.setSessionCookie(w, r, newID)
}
func (h *WebHandler) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID string `json:"project_id"`
		Token     string `json:"token"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		jsonError(w, err)
		return
	}
	p, err := h.store.WhoAmI(r.Context(), req.ProjectID, req.Token)
	if err != nil {
		jsonError(w, err)
		return
	}
	pin := webClientPin(r)
	if !h.clientProjectAllowed(r.Context(), pin, req.ProjectID) {
		http.Error(w, "this client certificate is not authorized for the selected project", http.StatusForbidden)
		return
	}
	sid, existing := h.session(r)
	// A browser may retain a cookie from a previously selected client
	// certificate. The credential and current certificate are independently
	// authorized above, so establish a fresh cookie instead of treating that
	// stale session binding as a failed project login.
	if sid == "" || existing.clientPin != pin {
		sid, err = newSessionID()
		if err != nil {
			http.Error(w, "session unavailable", 500)
			return
		}
	}
	h.mu.Lock()
	s := h.sessions[sid]
	if s == nil {
		s = &browserSession{projects: map[string]string{}, clientPin: pin}
		h.sessions[sid] = s
	}
	if s.clientPin != pin {
		h.mu.Unlock()
		http.Error(w, "session certificate mismatch", http.StatusUnauthorized)
		return
	}
	s.projects[req.ProjectID] = req.Token
	s.expires = time.Now().Add(12 * time.Hour)
	h.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "gossip_session", Value: sid, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: 12 * 60 * 60})
	jsonReply(w, p)
}
func (h *WebHandler) logout(w http.ResponseWriter, r *http.Request) {
	sid, session := h.session(r)
	if session != nil && session.clientPin != webClientPin(r) {
		jsonError(w, ErrUnauthorized)
		return
	}
	h.mu.Lock()
	delete(h.sessions, sid)
	h.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "gossip_session", Value: "", Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	jsonReply(w, map[string]bool{"logged_out": true})
}

type DashboardProjectLocal struct {
	ProjectID      string      `json:"project_id"`
	ProjectName    string      `json:"project_name"`
	HomeID         string      `json:"home_id"`
	Status         string      `json:"status"`
	Owner          *Principal  `json:"owner,omitempty"`
	Candidates     []Principal `json:"candidates,omitempty"`
	PlanRevision   int         `json:"plan_revision"`
	LastActivityAt int64       `json:"last_activity_at,omitempty"`
}

type DashboardProjectRemote struct {
	ProjectID       string `json:"project_id"`
	ProjectName     string `json:"project_name"`
	HomeID          string `json:"home_id"`
	PrincipalID     string `json:"principal_id"`
	PrincipalName   string `json:"principal_name,omitempty"`
	Role            string `json:"role,omitempty"`
	Endpoint        string `json:"endpoint"`
	HostFingerprint string `json:"host_fingerprint,omitempty"`
	SSHTarget       string `json:"ssh_target,omitempty"`
	Status          string `json:"status"` // "connected", "unauthorized", "offline"
	Error           string `json:"error,omitempty"`
}

type DashboardProjectsResponse struct {
	LocalAccess bool                     `json:"local_access"`
	HomeID      string                   `json:"home_id,omitempty"`
	Profile     string                   `json:"profile,omitempty"`
	Local       []DashboardProjectLocal  `json:"local"`
	Remote      []DashboardProjectRemote `json:"remote"`
}

func (h *WebHandler) remoteConnections(ctx context.Context) ([]DeviceConnection, error) {
	if h.registry == nil {
		return nil, nil
	}
	connections, err := h.registry.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]DeviceConnection, 0)
	for _, c := range connections {
		if c.Profile != h.profile || c.Endpoint == "" {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

func (h *WebHandler) resolveProjectCall(r *http.Request, projectID string) (backend WebBackend, queryBackend DashboardQueryBackend, targetHome string, token string, ctx context.Context, isRemote bool, statusCode int, statusMsg string) {
	ctx = r.Context()
	requestedHome := strings.TrimSpace(r.URL.Query().Get("home"))
	_, s := h.session(r)
	if s != nil && s.clientPin != webClientPin(r) {
		return nil, nil, "", "", ctx, false, http.StatusForbidden, "this client certificate is not authorized for the selected project"
	}
	if !h.hasLocalDashboardAccess(r) {
		if s == nil {
			return nil, nil, "", "", ctx, false, http.StatusUnauthorized, "no project credential is connected to this browser; connect a credential for this project"
		}
		tok, ok := s.projects[projectID]
		if !ok {
			return nil, nil, "", "", ctx, false, http.StatusUnauthorized, "this project is not connected to the browser; connect its project credential first"
		}
		if !h.clientProjectAllowed(ctx, webClientPin(r), projectID) {
			return nil, nil, "", "", ctx, false, http.StatusForbidden, "this client certificate is not authorized for the selected project"
		}
		return h.store, h.queryStore, "", tok, ctx, false, 0, ""
	}

	resolveRemoteConn := func(c DeviceConnection) (WebBackend, DashboardQueryBackend, string, string, context.Context, bool, int, string) {
		tok, err := h.registry.Token(c)
		if err != nil || tok == "" {
			return nil, nil, "", "", ctx, true, http.StatusUnauthorized, "saved remote credential is unavailable"
		}
		remote, err := h.registry.DialRemote(ctx, c)
		if err != nil {
			return nil, nil, "", "", ctx, true, http.StatusBadGateway, "remote connection is misconfigured: " + err.Error()
		}
		return remote, nil, c.HomeID, tok, r.Context(), true, 0, ""
	}

	if requestedHome != "" && requestedHome != h.homeID {
		conns, err := h.remoteConnections(ctx)
		if err != nil {
			return nil, nil, "", "", ctx, true, http.StatusInternalServerError, "remote connections unavailable"
		}
		for _, c := range conns {
			if c.HomeID == requestedHome && c.ProjectID == projectID {
				return resolveRemoteConn(c)
			}
		}
		return nil, nil, "", "", ctx, true, http.StatusUnauthorized, "remote project connection is not configured for this home"
	}

	if s != nil {
		if explicitToken, ok := s.projects[projectID]; ok && explicitToken != "" {
			return h.store, h.queryStore, h.homeID, explicitToken, ctx, false, 0, ""
		}
	}

	localCtx := withLocalOwnerAuthority(ctx, h.homeID)
	localProjects, err := h.localStore.ListLocalDashboardProjects(localCtx)
	if err != nil {
		return nil, nil, "", "", ctx, false, http.StatusInternalServerError, "local coordinator database unavailable"
	}
	for _, lp := range localProjects {
		if lp.ProjectID == projectID {
			if lp.Status != "ready" {
				return nil, nil, "", "", ctx, false, http.StatusConflict, "this local project needs device-owner selection before opening"
			}
			return h.store, h.queryStore, h.homeID, "", localCtx, false, 0, ""
		}
	}

	conns, err := h.remoteConnections(ctx)
	if err == nil {
		var matched []DeviceConnection
		for _, c := range conns {
			if c.ProjectID == projectID {
				matched = append(matched, c)
			}
		}
		if len(matched) == 1 {
			return resolveRemoteConn(matched[0])
		}
		if len(matched) > 1 {
			return nil, nil, "", "", ctx, true, http.StatusConflict, "multiple remote homes share this project ID; select one from Projects"
		}
	}
	return nil, nil, "", "", ctx, false, http.StatusNotFound, "project not found on this device"
}

func (h *WebHandler) dashboardProjects(w http.ResponseWriter, r *http.Request) {
	if !h.hasLocalDashboardAccess(r) {
		sid, s := h.session(r)
		if s == nil {
			jsonReply(w, DashboardProjectsResponse{LocalAccess: false, Local: []DashboardProjectLocal{}, Remote: []DashboardProjectRemote{}})
			return
		}
		if s.clientPin != webClientPin(r) {
			clearSessionCookie(w, r)
			h.mu.Lock()
			delete(h.sessions, sid)
			h.mu.Unlock()
			jsonReply(w, DashboardProjectsResponse{LocalAccess: false, Local: []DashboardProjectLocal{}, Remote: []DashboardProjectRemote{}})
			return
		}
		out := DashboardProjectsResponse{LocalAccess: false, Local: []DashboardProjectLocal{}, Remote: []DashboardProjectRemote{}}
		for projectID, tok := range s.projects {
			if !h.clientProjectAllowed(r.Context(), s.clientPin, projectID) {
				continue
			}
			p, err := h.store.WhoAmI(r.Context(), projectID, tok)
			if err != nil {
				continue
			}
			ownerCopy := p
			out.Local = append(out.Local, DashboardProjectLocal{
				ProjectID:   p.ProjectID,
				ProjectName: p.ProjectName,
				Status:      "ready",
				Owner:       &ownerCopy,
			})
		}
		jsonReply(w, out)
		return
	}

	h.ensureDeviceSession(w, r)
	localCtx := withLocalOwnerAuthority(r.Context(), h.homeID)
	localProjects, err := h.localStore.ListLocalDashboardProjects(localCtx)
	if err != nil {
		jsonError(w, err)
		return
	}
	resp := DashboardProjectsResponse{
		LocalAccess: true,
		HomeID:      h.homeID,
		Profile:     h.profile,
		Local:       make([]DashboardProjectLocal, 0, len(localProjects)),
		Remote:      []DashboardProjectRemote{},
	}
	for _, lp := range localProjects {
		resp.Local = append(resp.Local, DashboardProjectLocal{
			ProjectID:      lp.ProjectID,
			ProjectName:    lp.ProjectName,
			HomeID:         h.homeID,
			Status:         lp.Status,
			Owner:          lp.Owner,
			Candidates:     lp.Candidates,
			PlanRevision:   lp.PlanRevision,
			LastActivityAt: lp.LastActivityAt,
		})
	}
	conns, err := h.remoteConnections(r.Context())
	if err == nil {
		for _, c := range conns {
			item := DashboardProjectRemote{
				ProjectID:       c.ProjectID,
				ProjectName:     c.Name,
				HomeID:          c.HomeID,
				PrincipalID:     c.PrincipalID,
				Endpoint:        c.Endpoint,
				HostFingerprint: c.HostFingerprint,
				SSHTarget:       c.SSHTarget,
				Status:          "offline",
			}
			tok, tokErr := h.registry.Token(c)
			if tokErr != nil || tok == "" {
				item.Status = "unauthorized"
				item.Error = "credential unavailable in device registry"
				resp.Remote = append(resp.Remote, item)
				continue
			}
			timeout := 1500 * time.Millisecond
			if c.SSHTarget != "" {
				timeout = 5 * time.Second
			}
			checkCtx, cancel := context.WithTimeout(r.Context(), timeout)
			remote, remErr := h.registry.DialRemote(checkCtx, c)
			if remErr != nil {
				cancel()
				item.Error = remErr.Error()
				resp.Remote = append(resp.Remote, item)
				continue
			}
			who, whoErr := remote.WhoAmI(checkCtx, c.ProjectID, tok)
			cancel()
			if whoErr != nil {
				if errors.Is(whoErr, ErrUnauthorized) {
					item.Status = "unauthorized"
				} else {
					item.Status = "offline"
				}
				item.Error = whoErr.Error()
			} else {
				item.Status = "connected"
				if who.ProjectName != "" {
					item.ProjectName = who.ProjectName
				}
				item.PrincipalName = who.Name
				item.Role = who.Role
			}
			resp.Remote = append(resp.Remote, item)
		}
	}
	jsonReply(w, resp)
}

func (h *WebHandler) inspectLocalCheckout(w http.ResponseWriter, r *http.Request) {
	if !h.hasLocalDashboardAccess(r) {
		http.Error(w, "checkout inspection is available only from the local dashboard", http.StatusForbidden)
		return
	}
	var req struct {
		LocalPath string `json:"local_path"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		jsonError(w, err)
		return
	}
	observation, err := InspectGitWorkspace(r.Context(), strings.TrimSpace(req.LocalPath))
	if err != nil {
		jsonError(w, err)
		return
	}
	repositoryName := filepath.Base(observation.Root)
	if len(observation.RemoteHints) > 0 {
		parts := strings.Split(observation.RemoteHints[0], "/")
		if len(parts) > 0 && parts[len(parts)-1] != "" {
			repositoryName = parts[len(parts)-1]
		}
	}
	localCtx := withLocalOwnerAuthority(r.Context(), h.homeID)
	matches, err := h.localStore.FindLocalProjectsByCheckout(localCtx, observation.Root, observation.RemoteHints)
	if err != nil {
		jsonError(w, err)
		return
	}
	jsonReply(w, map[string]any{
		"observation":      observation,
		"repository_name":  repositoryName,
		"suggested_name":   repositoryName,
		"existing_matches": matches,
	})
}

func (h *WebHandler) pickLocalCheckout(w http.ResponseWriter, r *http.Request) {
	if !h.hasLocalDashboardAccess(r) {
		http.Error(w, "folder picking is available only from the local dashboard", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	path, selected, err := chooseLocalDirectory(ctx)
	if err != nil {
		jsonError(w, err)
		return
	}
	jsonReply(w, map[string]any{"selected": selected, "local_path": path})
}

func (h *WebHandler) selectLocalOwner(w http.ResponseWriter, r *http.Request) {
	if !h.hasLocalDashboardAccess(r) {
		http.Error(w, "local owner selection is available only from the local dashboard", http.StatusForbidden)
		return
	}
	project := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/projects/"), "/local-owner")
	var req struct {
		PrincipalID string `json:"principal_id"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		jsonError(w, err)
		return
	}
	localCtx := withLocalOwnerAuthority(r.Context(), h.homeID)
	p, err := h.localStore.SelectLocalDashboardOwner(localCtx, project, req.PrincipalID)
	if err != nil {
		jsonError(w, err)
		return
	}
	jsonReply(w, p)
}

func (h *WebHandler) joinRemoteProject(w http.ResponseWriter, r *http.Request) {
	if !h.hasLocalDashboardAccess(r) || h.registry == nil {
		http.Error(w, "joining remote projects is available only from the local dashboard", http.StatusForbidden)
		return
	}
	var req struct {
		Name        string `json:"name"`
		Endpoint    string `json:"endpoint"`
		HomeID      string `json:"home_id"`
		ProjectID   string `json:"project_id"`
		PrincipalID string `json:"principal_id"`
		Token       string `json:"token"`
		CAFile      string `json:"ca_file"`
		CertFile    string `json:"cert_file"`
		KeyFile     string `json:"key_file"`
		Checkout    string `json:"checkout"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		jsonError(w, err)
		return
	}
	req.Endpoint = strings.TrimSpace(req.Endpoint)
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.PrincipalID = strings.TrimSpace(req.PrincipalID)
	req.HomeID = strings.TrimSpace(req.HomeID)
	if req.Endpoint == "" || req.ProjectID == "" || req.PrincipalID == "" || req.Token == "" || req.CAFile == "" || req.CertFile == "" || req.KeyFile == "" {
		http.Error(w, "endpoint, project_id, principal_id, token, ca_file, cert_file, and key_file are required", http.StatusBadRequest)
		return
	}
	remote, err := NewRemote(req.Endpoint, req.CAFile, req.CertFile, req.KeyFile, "")
	if err != nil {
		jsonError(w, err)
		return
	}
	checkCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	valid, err := remote.CredentialValid(checkCtx, req.ProjectID, req.Token)
	if err != nil {
		http.Error(w, "remote verification failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if !valid {
		http.Error(w, "remote project credential was rejected", http.StatusUnauthorized)
		return
	}
	frame, err := remote.SyncProject(checkCtx, req.ProjectID, req.Token, 0, 1)
	if err != nil {
		http.Error(w, "remote project read failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if frame.HomeInstanceID == "" {
		http.Error(w, "remote coordinator did not report a stable home identity", http.StatusBadGateway)
		return
	}
	if req.HomeID != "" && req.HomeID != frame.HomeInstanceID {
		http.Error(w, "remote home identity does not match expected home_id", http.StatusConflict)
		return
	}
	who, err := remote.WhoAmI(checkCtx, req.ProjectID, req.Token)
	if err != nil {
		http.Error(w, "remote identity verification failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if who.ID != req.PrincipalID {
		http.Error(w, "credential belongs to a different principal", http.StatusConflict)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		if who.ProjectName != "" {
			name = who.ProjectName
		} else {
			name = req.ProjectID
		}
	}
	conn := DeviceConnection{
		Profile:     h.profile,
		HomeID:      frame.HomeInstanceID,
		ProjectID:   req.ProjectID,
		PrincipalID: who.ID,
		Name:        name,
		Endpoint:    req.Endpoint,
		CAFile:      req.CAFile,
		CertFile:    req.CertFile,
		KeyFile:     req.KeyFile,
		Checkout:    strings.TrimSpace(req.Checkout),
	}
	if err := h.registry.Put(r.Context(), conn, req.Token); err != nil {
		jsonError(w, err)
		return
	}
	jsonReply(w, map[string]any{
		"project_id":   req.ProjectID,
		"project_name": name,
		"home_id":      frame.HomeInstanceID,
		"principal_id": who.ID,
		"status":       "connected",
	})
}

func (h *WebHandler) projects(w http.ResponseWriter, r *http.Request) {
	if h.hasLocalDashboardAccess(r) {
		h.ensureDeviceSession(w, r)
		localCtx := withLocalOwnerAuthority(r.Context(), h.homeID)
		localProjects, err := h.localStore.ListLocalDashboardProjects(localCtx)
		if err != nil {
			jsonError(w, err)
			return
		}
		out := []Principal{}
		seen := map[string]bool{}
		for _, lp := range localProjects {
			if lp.Status != "ready" || lp.Owner == nil {
				continue
			}
			out = append(out, *lp.Owner)
			seen[lp.ProjectID] = true
		}
		if _, s := h.session(r); s != nil {
			for project, tok := range s.projects {
				if seen[project] || tok == "" {
					continue
				}
				if p, err := h.store.WhoAmI(r.Context(), project, tok); err == nil {
					out = append(out, p)
				}
			}
		}
		jsonReply(w, out)
		return
	}
	sid, s := h.session(r)
	if s == nil {
		jsonReply(w, []Principal{})
		return
	}
	if s.clientPin != webClientPin(r) {
		// A stale browser cookie is an empty dashboard state, not an
		// authorization failure. Clear it so the user can reconnect with a
		// currently authorized project credential.
		clearSessionCookie(w, r)
		h.mu.Lock()
		delete(h.sessions, sid)
		h.mu.Unlock()
		jsonReply(w, []Principal{})
		return
	}
	out := []Principal{}
	for project, token := range s.projects {
		if !h.clientProjectAllowed(r.Context(), s.clientPin, project) {
			continue
		}
		p, err := h.store.WhoAmI(r.Context(), project, token)
		if err == nil {
			out = append(out, p)
		}
	}
	jsonReply(w, out)
}

func (h *WebHandler) projectOverview(w http.ResponseWriter, r *http.Request) {
	if h.hasLocalDashboardAccess(r) {
		localCtx := withLocalOwnerAuthority(r.Context(), h.homeID)
		localProjects, err := h.localStore.ListLocalDashboardProjects(localCtx)
		if err != nil {
			http.Error(w, "project overview unavailable", http.StatusInternalServerError)
			return
		}
		out := make([]ProjectOverview, 0, len(localProjects))
		for _, lp := range localProjects {
			if lp.Status != "ready" || lp.Owner == nil {
				continue
			}
			counts, err := h.store.TaskCounts(localCtx, lp.ProjectID, "")
			if err != nil {
				continue
			}
			plan, planErr := h.store.CurrentPlan(localCtx, lp.ProjectID, "")
			if planErr != nil && !errors.Is(planErr, ErrNotFound) {
				continue
			}
			events, err := h.store.RecentEvents(localCtx, lp.ProjectID, "", 3)
			if err != nil {
				continue
			}
			presence, err := h.store.ListAgentPresence(localCtx, lp.ProjectID, "")
			if err != nil {
				continue
			}
			activeAgents := 0
			for _, agent := range presence {
				if agent.Online {
					activeAgents++
				}
			}
			out = append(out, ProjectOverview{
				ProjectID:    lp.ProjectID,
				ProjectName:  lp.ProjectName,
				PrincipalID:  lp.Owner.ID,
				Principal:    lp.Owner.Name,
				Role:         lp.Owner.Role,
				ActiveAgents: activeAgents,
				PlanRevision: plan.Revision,
				Counts:       counts,
				RecentEvents: events,
			})
		}
		jsonReply(w, out)
		return
	}
	sid, session := h.session(r)
	// An empty browser session is a normal state after loading the dashboard or
	// restarting it. Match /api/projects and let the UI render its empty state;
	// only reject sessions whose established certificate identity changed.
	if session == nil {
		jsonReply(w, []ProjectOverview{})
		return
	}
	if session.clientPin != webClientPin(r) {
		clearSessionCookie(w, r)
		h.mu.Lock()
		delete(h.sessions, sid)
		h.mu.Unlock()
		jsonReply(w, []ProjectOverview{})
		return
	}
	ids := make([]string, 0, len(session.projects))
	for id := range session.projects {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]ProjectOverview, 0, len(ids))
	for _, id := range ids {
		token := session.projects[id]
		if !h.clientProjectAllowed(r.Context(), session.clientPin, id) {
			continue
		}
		principal, err := h.store.WhoAmI(r.Context(), id, token)
		if errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			http.Error(w, "project overview unavailable", http.StatusInternalServerError)
			return
		}
		counts, err := h.store.TaskCounts(r.Context(), id, token)
		if err != nil {
			if errors.Is(err, ErrUnauthorized) {
				continue
			}
			http.Error(w, "project overview unavailable", http.StatusInternalServerError)
			return
		}
		plan, planErr := h.store.CurrentPlan(r.Context(), id, token)
		if planErr != nil && !errors.Is(planErr, ErrNotFound) {
			if errors.Is(planErr, ErrUnauthorized) {
				continue
			}
			http.Error(w, "project overview unavailable", http.StatusInternalServerError)
			return
		}
		events, err := h.store.RecentEvents(r.Context(), id, token, 3)
		if err != nil {
			if errors.Is(err, ErrUnauthorized) {
				continue
			}
			http.Error(w, "project overview unavailable", http.StatusInternalServerError)
			return
		}
		presence, err := h.store.ListAgentPresence(r.Context(), id, token)
		if err != nil {
			if errors.Is(err, ErrUnauthorized) {
				continue
			}
			http.Error(w, "project overview unavailable", http.StatusInternalServerError)
			return
		}
		activeAgents := 0
		for _, agent := range presence {
			if agent.Online {
				activeAgents++
			}
		}
		out = append(out, ProjectOverview{ProjectID: id, ProjectName: principal.ProjectName, PrincipalID: principal.ID, Principal: principal.Name, Role: principal.Role, ActiveAgents: activeAgents, PlanRevision: plan.Revision, Counts: counts, RecentEvents: events})
	}
	jsonReply(w, out)
}

func (h *WebHandler) project(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/projects/"), "/")
	if len(parts) != 2 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	project, action := parts[0], parts[1]
	backend, queryBackend, _, token, ctx, isRemote, statusCode, statusMsg := h.resolveProjectCall(r, project)
	if statusCode != 0 {
		http.Error(w, statusMsg, statusCode)
		return
	}
	if action == "agent-dms" {
		store, ok := backend.(*Store)
		if !ok || isRemote {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
			items, err := store.AgentDMThreads(ctx, project, token, r.URL.Query().Get("agent"), before)
			if err != nil {
				jsonError(w, err)
				return
			}
			jsonReply(w, items)
		case http.MethodPost:
			var input struct {
				First  string `json:"first"`
				Second string `json:"second"`
				Before int64  `json:"before"`
			}
			if err := decodeBody(w, r, &input); err != nil {
				jsonError(w, err)
				return
			}
			items, err := store.InspectAgentDM(ctx, project, token, input.First, input.Second, input.Before)
			if err != nil {
				jsonError(w, err)
				return
			}
			jsonReply(w, items)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}
	if action == "remote-invitations" {
		store, ok := backend.(*Store)
		if !ok || isRemote {
			http.NotFound(w, r)
			return
		}
		var value any
		var err error
		switch r.Method {
		case http.MethodGet:
			value, err = store.RemoteInvitations(ctx, project, token)
		case http.MethodPost:
			var req struct {
				Operation      string `json:"operation"`
				ID             string `json:"id"`
				Name           string `json:"name"`
				Role           string `json:"role"`
				Mode           string `json:"mode"`
				LanIP          string `json:"lan_ip"`
				FRPServer      string `json:"frp_server"`
				AutoLocalRelay bool   `json:"auto_local_relay"`
				Endpoint       string `json:"endpoint"`
				ServerCA       string `json:"server_ca"`
				Encrypt        bool   `json:"encrypt"`
				Passphrase     string `json:"passphrase"`
				AutoApprove    *bool  `json:"auto_approve"`
			}
			if err = decodeBody(w, r, &req); err == nil {
				switch req.Operation {
				case "create":
					if strings.TrimSpace(req.Name) == "" {
						req.Name = "Remote peer"
					}
					if strings.TrimSpace(req.Role) == "" {
						req.Role = "worker"
					}
					endpoint := strings.TrimSpace(req.Endpoint)
					serverCA := strings.TrimSpace(req.ServerCA)
					mode := strings.ToLower(strings.TrimSpace(req.Mode))
					var details InviteEndpointInfo
					if endpoint == "" && serverCA == "" && mode != "" {
						if h.peerManager == nil || !h.hasLocalDashboardAccess(r) || r.TLS != nil || !localHost(r.Host) || !loopbackPeer(r.RemoteAddr) {
							err = errors.New("automatic LAN/FRP/SSH sharing requires the local loopback dashboard")
							break
						}
						if mode == "lan" {
							if _, err = h.peerManager.EnsureLANActiveForProject(ctx, project); err != nil {
								break
							}
						} else if mode == "frp" {
							if _, err = h.peerManager.EnsureFRPActiveForProjectWithOptions(ctx, project, req.FRPServer, req.AutoLocalRelay); err != nil {
								break
							}
						}
						details, err = h.peerManager.ResolveInviteDetails(ctx, project, mode, req.LanIP)
						if err != nil {
							break
						}
						endpoint = details.Endpoint
						serverCA = details.ServerCA
					}
					if err = validateEnrollmentHome(endpoint, serverCA); err != nil {
						break
					}
					hostFP := details.HostFingerprint
					if hostFP == "" {
						hostFP = HostCertSSHFingerprint(serverCA)
					}
					// Manual owner approval with matching verification code is always required.
					const autoApprove = false
					var invite RemoteInvitation
					var code string
					invite, code, err = store.CreateRemoteInvitationWithOptions(ctx, project, token, req.Name, req.Role, autoApprove)
					if err != nil {
						break
					}
					if req.Encrypt {
						passphrase := strings.TrimSpace(req.Passphrase)
						if passphrase == "" {
							passphrase, err = GenerateInvitePIN()
							if err != nil {
								break
							}
						}
						projectName := project
						if who, whoErr := store.WhoAmI(ctx, project, token); whoErr == nil && who.ProjectName != "" {
							projectName = who.ProjectName
						}
						var sealed []byte
						sealed, err = EncryptEnrollmentBundle(EncryptedInvitePayload{
							Version:         1,
							ProjectName:     projectName,
							Mode:            mode,
							Invitation:      invite,
							Endpoint:        endpoint,
							ServerCA:        serverCA,
							HostFingerprint: hostFP,
							HomeID:          store.InstanceID(),
							Code:            code,
							SSHTarget:       details.SSHTarget,
							SSHPort:         details.SSHPort,
							SSHRemotePort:   details.SSHRemotePort,
						}, passphrase)
						if err != nil {
							break
						}
						value = map[string]any{
							"invitation":       invite,
							"endpoint":         endpoint,
							"home_id":          store.InstanceID(),
							"mode":             mode,
							"host_fingerprint": hostFP,
							"ssh_target":       details.SSHTarget,
							"ssh_port":         details.SSHPort,
							"ssh_remote_port":  details.SSHRemotePort,
							"auto_approve":     false,
							"encrypted_file":   string(sealed),
							"passphrase":       passphrase,
							"pin":              passphrase,
							"filename":         inviteFileName(projectName, req.Name),
						}
						break
					}
					value = map[string]any{
						"invitation":       invite,
						"code":             code,
						"endpoint":         endpoint,
						"server_ca":        serverCA,
						"home_id":          store.InstanceID(),
						"host_fingerprint": hostFP,
						"ssh_target":       details.SSHTarget,
						"ssh_port":         details.SSHPort,
						"ssh_remote_port":  details.SSHRemotePort,
						"auto_approve":     false,
					}
				case "approved", "denied", "cancelled":
					err = store.DecideRemoteInvitation(ctx, project, token, req.ID, req.Operation)
					value = map[string]bool{"ok": err == nil}
				default:
					err = errors.New("unknown invitation operation")
				}
			}
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err != nil {
			jsonError(w, err)
			return
		}
		jsonReply(w, value)
		return
	}
	if action == "project-roles" {
		store, ok := backend.(*Store)
		if !ok || isRemote {
			http.NotFound(w, r)
			return
		}
		var value any
		var err error
		switch r.Method {
		case http.MethodGet:
			value, err = store.ProjectRoles(ctx, project, token)
		case http.MethodPost:
			var req struct {
				Operation  string      `json:"operation"`
				Role       ProjectRole `json:"role"`
				Principal  string      `json:"principal_id"`
				Permission string      `json:"permission"`
			}
			if err = decodeBody(w, r, &req); err == nil {
				switch req.Operation {
				case "save":
					value, err = store.SaveProjectRole(ctx, project, token, req.Role)
				case "assign":
					err = store.AssignProjectRole(ctx, project, token, req.Principal, req.Role.ID)
				case "reset":
					err = store.ResetPermissionOverride(ctx, project, token, req.Principal, req.Permission)
				default:
					err = errors.New("unknown project role operation")
				}
			}
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err != nil {
			jsonError(w, err)
			return
		}
		if value == nil {
			value = map[string]bool{"ok": true}
		}
		jsonReply(w, value)
		return
	}
	if action == "context-links" {
		store, ok := backend.(projectContextBackend)
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			links, err := store.ContextLinks(ctx, project, token, r.URL.Query().Get("target_kind"), r.URL.Query().Get("target_id"))
			if err != nil {
				jsonError(w, err)
				return
			}
			jsonReply(w, links)
		case http.MethodPost:
			var link ContextLink
			if err := decodeBody(w, r, &link); err != nil {
				jsonError(w, err)
				return
			}
			if err := store.LinkContext(ctx, project, token, link); err != nil {
				jsonError(w, err)
				return
			}
			jsonReply(w, link)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}
	if action == "context" {
		store, ok := backend.(projectContextBackend)
		if !ok {
			http.NotFound(w, r)
			return
		}
		var value any
		var err error
		switch r.Method {
		case http.MethodGet:
			rev := 0
			if raw := r.URL.Query().Get("revision"); raw != "" {
				rev, err = strconv.Atoi(raw)
			}
			if err == nil {
				value, err = store.ProjectContext(ctx, project, token, r.URL.Query().Get("id"), rev)
			}
		case http.MethodPost:
			var input ContextRecord
			if err = decodeBody(w, r, &input); err == nil {
				value, err = store.PublishContext(ctx, project, token, input)
			}
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err != nil {
			jsonError(w, err)
			return
		}
		jsonReply(w, value)
		return
	}
	if action == "personal-items" {
		store, ok := backend.(personalItemsBackend)
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			items, err := store.PersonalItems(ctx, project, token)
			if err != nil {
				jsonError(w, err)
				return
			}
			if !isRemote && queryBackend != nil {
				if legacy, err := queryBackend.ListPersonalChecklists(ctx, project, token); err == nil {
					items.Items = append(items.Items, legacy.Items...)
				}
			}
			jsonReply(w, items)
		case http.MethodPost:
			var input PersonalItemInput
			if err := decodeBody(w, r, &input); err != nil {
				jsonError(w, err)
				return
			}
			item, err := store.SavePersonalItem(ctx, project, token, input)
			if err != nil {
				jsonError(w, err)
				return
			}
			jsonReply(w, item)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}
	var channelBackend ChannelBackend
	if !isRemote {
		channelBackend = h.channelStore
	}
	if r.Method == "GET" {
		var v any
		var err error
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		switch action {
		case "channels":
			if channelBackend != nil {
				v, err = channelBackend.ListChannels(ctx, project, token)
			} else {
				v = []ProjectChannel{}
			}
		case "identity":
			v, err = backend.WhoAmI(ctx, project, token)
		case "plan":
			v, err = backend.CurrentPlan(ctx, project, token)
		case "plan-variants":
			variants, ok := backend.(interface {
				PlanVariants(context.Context, string, string, int) ([]PlanVariant, error)
			})
			if !ok {
				http.NotFound(w, r)
				return
			}
			revision, parseErr := strconv.Atoi(r.URL.Query().Get("revision"))
			if parseErr != nil || revision < 1 {
				err = errors.New("invalid plan revision")
				break
			}
			v, err = variants.PlanVariants(ctx, project, token, revision)
		case "plan-threads":
			if planBackend, ok := backend.(interface {
				ListPlanThreads(context.Context, string, string) ([]PlanThread, error)
			}); ok {
				v, err = planBackend.ListPlanThreads(ctx, project, token)
			} else {
				v = []PlanThread{}
			}
		case "plans":
			if planBackend, ok := backend.(interface {
				ListPlanHistory(context.Context, string, string) ([]Plan, error)
			}); ok {
				v, err = planBackend.ListPlanHistory(ctx, project, token)
			} else {
				var p Plan
				p, err = backend.CurrentPlan(ctx, project, token)
				if errors.Is(err, ErrNotFound) {
					v, err = []Plan{}, nil
				} else if err == nil {
					v = []Plan{p}
				}
			}
		case "search":
			q := r.URL.Query().Get("q")
			if queryBackend != nil {
				v, err = queryBackend.SearchProject(ctx, project, token, q)
			} else {
				res := ProjectSearchResult{Query: q, Tasks: []Task{}, Plans: []Plan{}, Messages: []Message{}, Events: []Event{}}
				if page, e := backend.SearchTasks(ctx, project, token, TaskFilter{Query: q, Limit: 40}); e == nil {
					res.Tasks = page.Tasks
				} else {
					err = e
					break
				}
				lq := strings.ToLower(strings.TrimSpace(q))
				if lq != "" {
					if p, e := backend.CurrentPlan(ctx, project, token); e == nil && strings.Contains(strings.ToLower(p.Body), lq) {
						res.Plans = append(res.Plans, p)
					}
					if msgs, e := backend.ListMessages(ctx, project, token, 0, 100); e == nil {
						for _, m := range msgs {
							if strings.Contains(strings.ToLower(m.Body), lq) || strings.Contains(strings.ToLower(m.Subject), lq) {
								res.Messages = append(res.Messages, m)
							}
						}
					}
					if evts, e := backend.RecentEvents(ctx, project, token, 100); e == nil {
						for _, ev := range evts {
							if strings.Contains(strings.ToLower(ev.Kind), lq) || strings.Contains(strings.ToLower(ev.EntityID), lq) || strings.Contains(strings.ToLower(string(ev.Payload)), lq) {
								res.Events = append(res.Events, ev)
							}
						}
					}
				}
				v = res
			}
			if err == nil {
				if search, ok := backend.(projectContextBackend); ok {
					if result, ok := v.(ProjectSearchResult); ok {
						result.Context, err = search.SearchContext(ctx, project, token, q)
						v = result
					}
				}
			}
		case "graphify":
			q := r.URL.Query().Get("q")
			if queryBackend != nil && !isRemote {
				v, err = queryBackend.InspectGraphify(ctx, project, token, q)
			} else {
				_, err = backend.WhoAmI(ctx, project, token)
				if err == nil {
					v = GraphifyInspection{
						Status:       "unavailable",
						Reason:       "Graphify inspection is only available for local workspaces on this device.",
						MatchedNodes: []GraphifyNodeHit{},
					}
				}
			}
		case "checklists":
			if queryBackend != nil && !isRemote {
				v, err = queryBackend.ListPersonalChecklists(ctx, project, token)
			} else {
				_, err = backend.WhoAmI(ctx, project, token)
				if err == nil {
					v = PersonalChecklistResponse{
						Available: false,
						Reason:    "Personal checklists are only inspected from local project workspaces.",
						Items:     []PersonalChecklistEntry{},
					}
				}
			}
		case "tasks":
			v, err = backend.ListTasks(ctx, project, token, 200)
		case "task":
			v, err = backend.GetTask(ctx, project, token, r.URL.Query().Get("id"))
		case "attempt":
			number, parseErr := strconv.Atoi(r.URL.Query().Get("number"))
			if parseErr != nil || number < 1 {
				err = errors.New("valid attempt number required")
			} else {
				v, err = backend.GetAttempt(ctx, project, token, r.URL.Query().Get("id"), number)
			}
		case "task-search":
			revision := 0
			if raw := r.URL.Query().Get("revision"); raw != "" {
				revision, err = strconv.Atoi(raw)
				if err != nil || revision < 1 {
					err = errors.New("invalid plan revision")
					break
				}
			}
			v, err = backend.SearchTasks(ctx, project, token, TaskFilter{Revision: revision, Status: r.URL.Query().Get("status"), Kind: r.URL.Query().Get("kind"), Assignee: r.URL.Query().Get("assignee"), Query: r.URL.Query().Get("query"), Cursor: r.URL.Query().Get("cursor"), Limit: 50})
		case "task-counts":
			v, err = backend.TaskCounts(ctx, project, token)
		case "settings":
			v, err = backend.GetSettings(ctx, project, token)
		case "budget":
			v, err = backend.GetBudget(ctx, project, token)
		case "agents":
			v, err = backend.ListPrincipals(ctx, project, token)
		case "presence":
			v, err = backend.ListAgentPresence(ctx, project, token)
		case "certificates":
			v, err = backend.ListCertificateGrants(ctx, project, token)
		case "certificate-invitations":
			v, err = backend.ListCertificateInvitations(ctx, project, token)
		case "replicas":
			v, err = backend.ListProjectReplicas(ctx, project, token)
		case "permissions":
			v, err = backend.ListPermissions(ctx, project, token)
		case "roles":
			v, err = backend.ListWorkRoles(ctx, project, token)
		case "role-presets":
			_, err = backend.WhoAmI(ctx, project, token)
			if err == nil {
				v = WorkRolePresets()
			}
		case "repositories":
			v, err = backend.ListRepositories(ctx, project, token)
		case "workspaces":
			v, err = backend.ListWorkspaces(ctx, project, token)
		case "file-reservations":
			v = []FileReservation{}
			if h.registry == nil || isRemote {
				break
			}
			var actor Principal
			actor, err = backend.WhoAmI(ctx, project, token)
			if err != nil {
				break
			}
			var connections []DeviceConnection
			connections, err = h.registry.List(r.Context())
			if err != nil {
				break
			}
			for _, c := range connections {
				if c.Profile != h.profile || c.HomeID != h.homeID || c.ProjectID != project || c.PrincipalID != actor.ID || c.Endpoint != "" || c.Checkout == "" {
					continue
				}
				var reservations []FileReservation
				reservations, err = h.registry.FileReservations(r.Context(), c, "list", "", nil)
				for i := range reservations {
					reservations[i].Handle = ""
				}
				if err == nil {
					v = reservations
				}
				break
			}

		case "events":
			limit := 100
			if raw := r.URL.Query().Get("limit"); raw != "" {
				if parsed, e := strconv.Atoi(raw); e != nil {
					err = errors.New("invalid event limit")
					break
				} else {
					limit = parsed
				}
			}
			if beforeRaw := r.URL.Query().Get("before"); beforeRaw != "" {
				before, parseErr := strconv.ParseInt(beforeRaw, 10, 64)
				if parseErr != nil {
					err = errors.New("invalid event cursor")
					break
				}
				if eventBeforeBackend, ok := backend.(interface {
					RecentEventsBefore(context.Context, string, string, int64, int) ([]Event, error)
				}); ok {
					v, err = eventBeforeBackend.RecentEventsBefore(ctx, project, token, before, limit)
					break
				}
			}
			if r.URL.Query().Get("recent") == "1" {
				v, err = backend.RecentEvents(ctx, project, token, limit)
			} else {
				v, err = backend.ReadEvents(ctx, project, token, after, limit)
			}
		case "retention":
			v, err = backend.EventRetentionStatus(ctx, project, token)
		case "conversation-search":
			searcher, ok := backend.(interface {
				SearchConversations(context.Context, string, string, string) ([]Message, error)
			})
			if !ok {
				http.NotFound(w, r)
				return
			}
			v, err = searcher.SearchConversations(ctx, project, token, r.URL.Query().Get("q"))
		case "conversation-message":
			reader, ok := backend.(interface {
				ConversationMessage(context.Context, string, string, string) (Message, error)
			})
			if !ok {
				http.NotFound(w, r)
				return
			}
			v, err = reader.ConversationMessage(ctx, project, token, r.URL.Query().Get("id"))
		case "conversation-activity":
			activityBackend, ok := backend.(interface {
				ConversationActivity(context.Context, string, string) ([]ConversationActivity, error)
			})
			if !ok {
				http.NotFound(w, r)
				return
			}
			v, err = activityBackend.ConversationActivity(ctx, project, token)
		case "messages":
			before := int64(0)
			if raw := r.URL.Query().Get("before"); raw != "" {
				var parseErr error
				before, parseErr = strconv.ParseInt(raw, 10, 64)
				if parseErr != nil {
					err = errors.New("invalid message cursor")
					break
				}
			}
			limit := 100
			if raw := r.URL.Query().Get("limit"); raw != "" {
				if parsed, e := strconv.Atoi(raw); e != nil {
					err = errors.New("invalid message limit")
					break
				} else {
					limit = parsed
				}
			}
			if chID := strings.TrimSpace(r.URL.Query().Get("channel")); chID != "" {
				if channelBackend == nil {
					err = errors.New("group channels are unavailable on this backend")
					break
				}
				v, err = channelBackend.ChannelMessages(ctx, project, token, chID, after, before, limit, r.URL.Query().Get("recent") == "1")
				break
			}
			v, err = backend.ConversationMessages(ctx, project, token, r.URL.Query().Get("recipient"), after, before, limit, r.URL.Query().Get("recent") == "1")
		case "acknowledgments":
			v, err = backend.MessageAcknowledgments(ctx, project, token, r.URL.Query().Get("id"))
		case "report":
			if taskID := r.URL.Query().Get("task"); taskID != "" {
				reportBackend, ok := backend.(interface {
					GetAttemptReport(context.Context, string, string, string, int) (Report, error)
				})
				if !ok {
					http.NotFound(w, r)
					return
				}
				attempt, _ := strconv.Atoi(r.URL.Query().Get("attempt"))
				v, err = reportBackend.GetAttemptReport(ctx, project, token, taskID, attempt)
			} else {
				v, err = backend.GetReport(ctx, project, token, r.URL.Query().Get("id"))
			}
		case "candidate":
			v, err = backend.GetCandidate(ctx, project, token, r.URL.Query().Get("id"))
		case "candidate-page":
			offset, parseErr := strconv.Atoi(r.URL.Query().Get("offset"))
			if parseErr != nil && r.URL.Query().Get("offset") != "" {
				err = errors.New("valid candidate offset required")
			} else {
				limit, limitErr := strconv.Atoi(r.URL.Query().Get("limit"))
				if limitErr != nil || r.URL.Query().Get("limit") == "" {
					limit = 4096
				}
				v, err = backend.GetCandidatePage(ctx, project, token, r.URL.Query().Get("id"), offset, limit)
			}
		case "candidates":
			v, err = backend.ListCandidates(ctx, project, token, r.URL.Query().Get("task_id"), 50)
		case "integrations":
			v, err = backend.ListIntegrations(ctx, project, token, 100)
		case "legacy-imports":
			v, err = backend.ListLegacyImports(ctx, project, token)
		case "legacy-records":
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			if limit < 1 || limit > 50 {
				limit = 20
			}
			v, err = backend.ReadLegacyRecords(ctx, project, token, r.URL.Query().Get("import_id"), r.URL.Query().Get("type"), r.URL.Query().Get("after"), limit)
		case "legacy-record":
			offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			maxChars, _ := strconv.Atoi(r.URL.Query().Get("max_chars"))
			v, err = backend.ReadLegacyRecordChunk(ctx, project, token, r.URL.Query().Get("import_id"), r.URL.Query().Get("type"), r.URL.Query().Get("id"), offset, maxChars)
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			h.projectError(w, backend, ctx, project, token, err)
			return
		}
		jsonReply(w, v)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req struct {
		Operation        string             `json:"operation"`
		ID               string             `json:"id"`
		ParentID         string             `json:"parent_id"`
		Revision         int                `json:"revision"`
		ExpectedRevision int                `json:"expected_revision"`
		Title            string             `json:"title"`
		Description      string             `json:"description"`
		Kind             string             `json:"kind"`
		Contract         ReportContract     `json:"contract"`
		Dependencies     []string           `json:"dependencies"`
		Body             string             `json:"body"`
		Recipient        string             `json:"recipient"`
		Subject          string             `json:"subject"`
		ThreadID         string             `json:"thread_id"`
		Attempt          int                `json:"attempt"`
		Baseline         string             `json:"baseline"`
		Digest           string             `json:"digest"`
		RepositoryID     string             `json:"repository_id"`
		RemoteURL        string             `json:"remote_url"`
		WorkspaceID      string             `json:"workspace_id"`
		Name             string             `json:"name"`
		Role             string             `json:"role"`
		PrincipalID      string             `json:"principal_id"`
		Credential       string             `json:"credential_token"`
		NodeLabel        string             `json:"node_label"`
		LocalPath        string             `json:"local_path"`
		Branch           string             `json:"branch"`
		Head             string             `json:"head"`
		Dirty            bool               `json:"dirty"`
		Settings         Settings           `json:"settings"`
		RequestID        string             `json:"request_id"`
		Amount           int64              `json:"amount"`
		ExpiresAt        int64              `json:"expires_at"`
		ThroughSequence  int64              `json:"through_sequence"`
		Fingerprint      string             `json:"fingerprint"`
		InvitationToken  string             `json:"invitation_token"`
		Permission       string             `json:"permission"`
		CandidateIDs     []string           `json:"candidate_ids"`
		Checks           []IntegrationCheck `json:"checks"`
		Observation      GitObservation     `json:"observation"`
		ChannelID        string             `json:"channel_id"`
		Topic            string             `json:"topic"`
		Members          []string           `json:"members"`
		AutoSuffix       bool               `json:"auto_suffix"`
		Announce         bool               `json:"announce"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		jsonError(w, err)
		return
	}
	var v any
	var err error
	switch action {
	case "plan-variants":
		variants, ok := backend.(interface {
			CreatePlanVariant(context.Context, string, string, int, string, string) (PlanVariant, error)
		})
		if !ok {
			http.NotFound(w, r)
			return
		}
		v, err = variants.CreatePlanVariant(ctx, project, token, req.Revision, req.Name, req.Body)
	case "plan":
		if req.Operation == "delete_thread" {
			planDelete, ok := backend.(interface {
				DeletePlanThread(context.Context, string, string, string, int) error
			})
			if !ok {
				http.NotFound(w, r)
				return
			}
			err = planDelete.DeletePlanThread(ctx, project, token, req.ID, req.ExpectedRevision)
			v = map[string]bool{"deleted": err == nil}
		} else if req.Operation == "delete" {
			planDelete, ok := backend.(interface {
				DeletePlanRevision(context.Context, string, string, int, int) error
			})
			if !ok {
				http.NotFound(w, r)
				return
			}
			err = planDelete.DeletePlanRevision(ctx, project, token, req.Revision, req.ExpectedRevision)
			v = map[string]bool{"deleted": err == nil}
		} else if req.Operation == "complete" {
			planBackend, ok := backend.(interface {
				CompletePlan(context.Context, string, string, int) error
			})
			if !ok {
				http.NotFound(w, r)
				return
			}
			err = planBackend.CompletePlan(ctx, project, token, req.Revision)
			v = map[string]bool{"completed": err == nil}
		} else if req.Operation == "acknowledge" {
			err = backend.AcknowledgePlan(ctx, project, token, req.Revision)
			v = map[string]bool{"acknowledged": err == nil}
		} else if req.Operation == "" || req.Operation == "publish" {
			v, err = backend.PublishPlan(ctx, project, token, req.Body, req.Revision)
		} else {
			err = errors.New("unknown plan operation")
		}
	case "plan-threads":
		if req.Operation != "create" {
			err = errors.New("unknown plan thread operation")
			break
		}
		planBackend, ok := backend.(interface {
			CreatePlan(context.Context, string, string, string, string, int) (Plan, error)
		})
		if !ok {
			http.NotFound(w, r)
			return
		}
		v, err = planBackend.CreatePlan(ctx, project, token, req.Name, req.Body, req.ExpectedRevision)
	case "tasks":
		switch req.Operation {
		case "create":
			v, err = backend.CreateTaskWithID(ctx, project, token, req.RequestID, req.Revision, req.Title, req.Description, req.Kind, req.Contract, req.Dependencies)
		case "request_review":
			err = backend.RequestReview(ctx, project, token, req.ID, req.Attempt)
			v = map[string]bool{"requested": err == nil}
		case "claim":
			settings, e := backend.GetSettings(ctx, project, token)
			if e != nil {
				err = e
				break
			}
			lease := settings.MaxLeaseSeconds
			if lease > 300 {
				lease = 300
			}
			v, err = backend.ClaimTaskInWorkspace(ctx, project, token, req.ID, req.WorkspaceID, time.Duration(lease)*time.Second)
		case "assign":
			v, err = backend.AssignTask(ctx, project, token, req.ID, req.Recipient)
		case "block", "resolve", "cancel":
			v, err = backend.TransitionTask(ctx, project, token, req.ID, req.Operation, req.Body, req.Attempt)
		case "renew":
			settings, e := backend.GetSettings(ctx, project, token)
			if e != nil {
				err = e
				break
			}
			lease := settings.MaxLeaseSeconds
			if lease > 300 {
				lease = 300
			}
			v, err = backend.RenewLease(ctx, project, token, req.ID, req.Attempt, time.Duration(lease)*time.Second)
		case "reconcile":
			v, err = backend.ReconcileTask(ctx, project, token, req.ID, req.Revision)
		default:
			err = errors.New("unknown task operation")
		}
	case "agents":
		if req.Operation == "parent" {
			err = backend.SetAgentParent(ctx, project, token, req.ID, req.ParentID)
			v = map[string]bool{"updated": err == nil}
			break
		}
		if req.Operation == "invite" {
			v, err = backend.AddPreparedPrincipal(ctx, project, token, req.Name, req.Role, Credential{PrincipalID: req.PrincipalID, Token: req.Credential})
			break
		}
		if req.Operation == "invite_remote" {
			v, err = backend.AddPreparedRemotePrincipal(ctx, project, token, req.Name, req.Role, Credential{PrincipalID: req.PrincipalID, Token: req.Credential}, req.Fingerprint, req.ExpiresAt)
			break
		}
		if req.Operation != "revoke" {
			err = errors.New("unknown agent operation")
			break
		}
		err = backend.RevokePrincipal(ctx, project, token, req.ID)
		v = map[string]bool{"revoked": err == nil}
	case "certificates":
		switch req.Operation {
		case "grant":
			v, err = backend.AddCertificateGrantUntil(ctx, project, token, req.Fingerprint, req.ExpiresAt)
		case "revoke":
			err = backend.RevokeCertificateGrant(ctx, project, token, req.Fingerprint)
			v = map[string]bool{"revoked": err == nil}
		default:
			err = errors.New("unknown certificate operation")
		}
	case "certificate-invitations":
		switch req.Operation {
		case "create":
			v, err = backend.AddPreparedCertificateInvitation(ctx, project, token, req.ID, req.InvitationToken, req.Fingerprint, req.ExpiresAt)
		case "revoke":
			err = backend.RevokeCertificateInvitation(ctx, project, token, req.ID)
			v = map[string]bool{"revoked": err == nil}
		default:
			err = errors.New("unknown certificate invitation operation")
		}
	case "permissions":
		switch req.Operation {
		case "grant":
			v, err = backend.GrantPermission(ctx, project, token, req.PrincipalID, req.Permission)
		case "revoke":
			err = backend.RevokePermission(ctx, project, token, req.PrincipalID, req.Permission)
			v = map[string]bool{"revoked": err == nil}
		default:
			err = errors.New("unknown permission operation")
		}
	case "channels":
		if channelBackend == nil {
			err = errors.New("group channels are unavailable on this backend")
			break
		}
		switch req.Operation {
		case "", "create":
			v, err = channelBackend.CreateChannel(ctx, project, token, req.Name, req.Topic, req.Members, req.AutoSuffix, req.Announce)
		case "ask_join":
			v, err = channelBackend.RequestJoinChannel(ctx, project, token, req.ChannelID, req.Body, req.Announce)
		case "invite":
			v, err = channelBackend.InviteToChannel(ctx, project, token, req.ChannelID, req.Recipient, req.Body, req.Announce)
		case "join", "approve", "decline", "leave", "remove":
			v, err = channelBackend.RespondChannelMembership(ctx, project, token, req.ChannelID, req.Recipient, req.Operation)
		default:
			err = errors.New("unknown channel operation")
		}
	case "messages":
		if req.Operation == "acknowledge" {
			v, err = backend.AcknowledgeMessage(ctx, project, token, req.ID)
		} else if req.Operation == "" || req.Operation == "send" {
			threadID := req.ThreadID
			if strings.TrimSpace(req.ChannelID) != "" && threadID == "" {
				threadID = channelThreadID(req.ChannelID)
			}
			v, err = backend.SendMessageWithID(ctx, project, token, req.Recipient, threadID, req.Subject, req.Body, req.RequestID)
		} else {
			err = errors.New("unknown message operation")
		}
	case "repositories":
		switch req.Operation {
		case "add_hint":
			v, err = backend.AddRepositoryRemoteHint(ctx, project, token, req.RepositoryID, req.RemoteURL)
		case "remove_hint":
			err = backend.RemoveRepositoryRemoteHint(ctx, project, token, req.RepositoryID, req.RemoteURL)
			v = map[string]bool{"removed": err == nil}
		case "":
			v, err = backend.CreateRepository(ctx, project, token, req.Name)
		default:
			err = errors.New("unknown repository operation")
		}
	case "workspaces":
		if req.Operation == "update" {
			v, err = backend.UpdateWorkspace(ctx, project, token, req.WorkspaceID, req.Branch, req.Head, req.Dirty)
		} else if req.Operation == "" || req.Operation == "register" {
			v, err = backend.RegisterWorkspace(ctx, project, token, req.RepositoryID, req.Name, req.NodeLabel, req.LocalPath, req.Branch, req.Head, req.Dirty)
		} else {
			err = errors.New("unknown workspace operation")
		}
	case "integrations":
		v, err = backend.RecordIntegration(ctx, project, token, IntegrationSubmission{TaskID: req.ID, Attempt: req.Attempt, WorkspaceID: req.WorkspaceID, BaseRevision: req.Baseline, CombinedRevision: req.Head, Observation: req.Observation, CandidateIDs: req.CandidateIDs, Checks: req.Checks, Summary: req.Body, RequestID: req.RequestID})
	case "events":
		if req.Operation != "prune" {
			err = errors.New("unknown event operation")
		} else {
			v, err = backend.PruneProjectEvents(ctx, project, token, req.ThroughSequence)
		}
	case "reports":
		if submitter, ok := backend.(interface {
			SubmitReportWithID(context.Context, string, string, string, int, string, string, string) (Report, error)
		}); ok && req.RequestID != "" {
			v, err = submitter.SubmitReportWithID(ctx, project, token, req.ID, req.Attempt, req.Baseline, req.Body, req.RequestID)
		} else {
			v, err = backend.SubmitReport(ctx, project, token, req.ID, req.Attempt, req.Baseline, req.Body)
		}
	case "review":
		if req.Operation == "rework" {
			err = backend.RequestRework(ctx, project, token, req.ID, req.Digest, req.Body)
			v = map[string]bool{"rework_requested": err == nil}
		} else if req.Operation == "" || req.Operation == "accept" {
			err = backend.AcceptReport(ctx, project, token, req.ID, req.Digest)
			v = map[string]bool{"accepted": err == nil}
		} else {
			err = errors.New("unknown review operation")
		}
	case "roles":
		v, err = backend.SetWorkRole(ctx, project, token, req.Recipient, req.ID, req.Name, req.Body, req.ExpiresAt, req.Revision)
	case "budget":
		if req.Operation == "set" {
			v, err = backend.SetBudget(ctx, project, token, req.Revision, req.Amount)
		} else if req.Operation == "reserve" {
			v, err = backend.ReserveBudget(ctx, project, token, req.ID, req.Amount)
		} else {
			err = errors.New("unknown budget operation")
		}
	case "settings":
		v, err = backend.UpdateSettings(ctx, project, token, req.Settings)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.projectError(w, backend, ctx, project, token, err)
		return
	}
	jsonReply(w, v)
}

func (h *WebHandler) acceptInvitation(w http.ResponseWriter, r *http.Request) {
	if !h.hasLocalDashboardAccess(r) || h.registry == nil {
		http.Error(w, "local dashboard required", http.StatusForbidden)
		return
	}
	var input struct {
		Bundle          EnrollmentBundle `json:"bundle"`
		Endpoint        string           `json:"endpoint"`
		ProjectID       string           `json:"project_id"`
		InvitationID    string           `json:"invitation_id"`
		ServerCA        string           `json:"server_ca"`
		Code            string           `json:"code"`
		EncryptedFile   string           `json:"encrypted_file"`
		Passphrase      string           `json:"passphrase"`
		SSHTarget       string           `json:"ssh_target"`
		SSHPort         int              `json:"ssh_port"`
		SSHRemotePort   int              `json:"ssh_remote_port"`
		SSHIdentityFile string           `json:"ssh_identity_file"`
		TrustHostKey    *bool            `json:"trust_host_key"`
	}
	if err := decodeBody(w, r, &input); err != nil {
		jsonError(w, err)
		return
	}
	bundle := input.Bundle
	if bundle.Endpoint == "" && strings.TrimSpace(input.Endpoint) != "" {
		bundle.Endpoint = strings.TrimSpace(input.Endpoint)
	}
	if bundle.ServerCA == "" && strings.TrimSpace(input.ServerCA) != "" {
		bundle.ServerCA = input.ServerCA
	}
	if bundle.Invitation.Project == "" && strings.TrimSpace(input.ProjectID) != "" {
		bundle.Invitation.Project = strings.TrimSpace(input.ProjectID)
	}
	if bundle.Invitation.ID == "" && strings.TrimSpace(input.InvitationID) != "" {
		bundle.Invitation.ID = strings.TrimSpace(input.InvitationID)
	}
	code := strings.TrimSpace(input.Code)
	mode := ""
	projectName := ""
	sshTarget := strings.TrimSpace(input.SSHTarget)
	sshPort := input.SSHPort
	sshRemotePort := input.SSHRemotePort
	trustHost := true
	if input.TrustHostKey != nil {
		trustHost = *input.TrustHostKey
	}
	encryptedUsed := strings.TrimSpace(input.EncryptedFile) != ""
	if encryptedUsed {
		payload, err := DecryptEnrollmentBundle([]byte(input.EncryptedFile), input.Passphrase)
		if err != nil {
			jsonError(w, err)
			return
		}
		bundle = EnrollmentBundle{
			Invitation: payload.Invitation,
			Endpoint:   payload.Endpoint,
			ServerCA:   payload.ServerCA,
			HomeID:     payload.HomeID,
		}
		code = payload.Code
		mode = payload.Mode
		projectName = payload.ProjectName
		if sshTarget == "" {
			sshTarget = payload.SSHTarget
		}
		if sshPort == 0 {
			sshPort = payload.SSHPort
		}
		if sshRemotePort == 0 {
			sshRemotePort = payload.SSHRemotePort
		}
	}
	if bundle.Endpoint == "" && sshTarget != "" {
		remPort := sshRemotePort
		if remPort <= 0 {
			remPort = DefaultPeerListenPort
		}
		bundle.Endpoint = fmt.Sprintf("https://127.0.0.1:%d", remPort)
	}
	if bundle.Endpoint != "" && !strings.Contains(bundle.Endpoint, "://") {
		bundle.Endpoint = "https://" + bundle.Endpoint
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	if bundle.ServerCA == "" && bundle.Endpoint != "" {
		dialEndpoint := bundle.Endpoint
		if sshTarget != "" {
			var err error
			dialEndpoint, err = h.registry.EnsureSSHClientForward(ctx, sshTarget, bundle.Endpoint, sshPort, sshRemotePort, input.SSHIdentityFile)
			if err != nil {
				jsonError(w, err)
				return
			}
		}
		probe, err := ProbePeerEndpoint(ctx, dialEndpoint)
		if err != nil {
			jsonError(w, err)
			return
		}
		bundle.ServerCA = probe.ServerCA
	}
	invite, err := h.registry.AcceptEnrollmentWithOptions(ctx, h.profile, EnrollmentAcceptOptions{
		Bundle:          bundle,
		Code:            code,
		SSHTarget:       sshTarget,
		SSHPort:         sshPort,
		SSHRemotePort:   sshRemotePort,
		SSHIdentityFile: input.SSHIdentityFile,
		TrustHostKey:    trustHost,
	})
	if err != nil {
		jsonError(w, err)
		return
	}
	hostFP := HostCertSSHFingerprint(bundle.ServerCA)
	jsonReply(w, map[string]any{
		"id":                invite.ID,
		"project_id":        invite.Project,
		"name":              invite.Name,
		"role":              invite.Role,
		"state":             invite.State,
		"fingerprint":       invite.Fingerprint,
		"ssh_fingerprint":   invite.SSHFingerprint,
		"verification_code": invite.VerificationCode,
		"host_fingerprint":  hostFP,
		"expires_at":        invite.ExpiresAt,
		"auto_approve":      false,
		"home_id":           bundle.HomeID,
		"endpoint":          bundle.Endpoint,
		"project_name":      projectName,
		"mode":              mode,
		"ssh_target":        sshTarget,
		"ssh_port":          sshPort,
		"ssh_remote_port":   sshRemotePort,
	})
}

func (h *WebHandler) probePeerHost(w http.ResponseWriter, r *http.Request) {
	if !h.hasLocalDashboardAccess(r) || h.registry == nil {
		http.Error(w, "local dashboard required", http.StatusForbidden)
		return
	}
	var req struct {
		Endpoint        string `json:"endpoint"`
		SSHTarget       string `json:"ssh_target"`
		SSHPort         int    `json:"ssh_port"`
		SSHRemotePort   int    `json:"ssh_remote_port"`
		SSHIdentityFile string `json:"ssh_identity_file"`
		HomeID          string `json:"home_id"`
		TrustHostKey    bool   `json:"trust_host_key"`
		ResetKnownHost  bool   `json:"reset_known_host"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		jsonError(w, err)
		return
	}
	endpoint := strings.TrimSpace(req.Endpoint)
	if endpoint != "" && !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	if endpoint == "" && strings.TrimSpace(req.SSHTarget) != "" {
		remPort := req.SSHRemotePort
		if remPort <= 0 {
			remPort = defaultSSHRemotePort
		}
		endpoint = fmt.Sprintf("https://127.0.0.1:%d", remPort)
	}
	if endpoint == "" {
		jsonError(w, errors.New("enter a host endpoint or SSH target"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	hostKey := KnownHostIdentity(endpoint, req.SSHTarget, req.SSHRemotePort)
	if req.ResetKnownHost {
		_ = h.registry.RemoveKnownHost(ctx, hostKey)
	}
	dialEndpoint := endpoint
	if strings.TrimSpace(req.SSHTarget) != "" {
		var err error
		dialEndpoint, err = h.registry.EnsureSSHClientForward(ctx, req.SSHTarget, endpoint, req.SSHPort, req.SSHRemotePort, req.SSHIdentityFile)
		if err != nil {
			jsonError(w, err)
			return
		}
	}
	probe, err := ProbePeerEndpoint(ctx, dialEndpoint)
	if err != nil {
		jsonError(w, err)
		return
	}
	check, err := h.registry.VerifyOrRecordKnownHost(ctx, hostKey, req.HomeID, probe.ServerCA, req.TrustHostKey)
	if err != nil {
		if errors.Is(err, ErrHostKeyChanged) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":               "changed",
				"host_key":             hostKey,
				"host_fingerprint":     check.Fingerprint,
				"expected_fingerprint": check.ExpectedFingerprint,
				"error":                err.Error(),
			})
			return
		}
		jsonError(w, err)
		return
	}
	jsonReply(w, map[string]any{
		"status":           check.Status,
		"host_key":         check.HostKey,
		"endpoint":         endpoint,
		"host_fingerprint": probe.HostFingerprint,
		"server_ca":        probe.ServerCA,
	})
}

func (h *WebHandler) localKnownHosts(w http.ResponseWriter, r *http.Request) {
	if !h.hasLocalDashboardAccess(r) || h.registry == nil {
		http.Error(w, "local dashboard required", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodGet {
		hosts, err := h.registry.ListKnownHosts(r.Context())
		if err != nil {
			jsonError(w, err)
			return
		}
		jsonReply(w, hosts)
		return
	}
	var req struct {
		HostKey string `json:"host_key"`
	}
	if r.Method == http.MethodDelete {
		req.HostKey = strings.TrimSpace(r.URL.Query().Get("host_key"))
	} else if err := decodeBody(w, r, &req); err != nil {
		jsonError(w, err)
		return
	}
	if err := h.registry.RemoveKnownHost(r.Context(), req.HostKey); err != nil {
		jsonError(w, err)
		return
	}
	jsonReply(w, map[string]bool{"ok": true})
}

func inviteFileName(projectName, invitee string) string {
	slug := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		var b strings.Builder
		for _, r := range s {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
			} else if r == '-' || r == '_' || r == ' ' {
				if b.Len() > 0 && b.String()[b.Len()-1] != '-' {
					b.WriteByte('-')
				}
			}
		}
		return strings.Trim(b.String(), "-")
	}
	p := slug(projectName)
	n := slug(invitee)
	if p == "" {
		p = "project"
	}
	if n == "" {
		return "gossip-" + p + "-invite.gossip"
	}
	return "gossip-" + p + "-" + n + "-invite.gossip"
}
