package coord

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
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

//go:embed ui/*
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
	EventRetentionStatus(context.Context, string, string) (EventRetention, error)
	WhoAmI(context.Context, string, string) (Principal, error)
	FindRepositoriesByRemote(context.Context, string, string, string) ([]Repository, error)
	AddPreparedPrincipal(context.Context, string, string, string, string, Credential) (Credential, error)
	AddPreparedRemotePrincipal(context.Context, string, string, string, string, Credential, string, int64) (Credential, error)
	TaskCounts(context.Context, string, string) (map[string]int, error)
	RecentEvents(context.Context, string, string, int) ([]Event, error)
	RecentMessages(context.Context, string, string, int) ([]Message, error)
	ListLegacyImports(context.Context, string, string) ([]LegacyImport, error)
	ReadLegacyRecords(context.Context, string, string, string, string, string, int) ([]LegacyRecord, error)
	ReadLegacyRecordChunk(context.Context, string, string, string, string, string, int, int) (LegacyRecordChunk, error)
	GetCandidatePage(context.Context, string, string, string, int, int) (CandidatePage, error)
}

type WebHandler struct {
	store                    WebBackend
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
	return &WebHandler{store: store, sessions: map[string]*browserSession{}, allowedHosts: map[string]bool{}, clientProjects: map[string]map[string]bool{}}
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
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	u, err := url.Parse(o)
	return err == nil && ((u.Scheme == "http" && r.TLS == nil) || (u.Scheme == "https" && r.TLS != nil)) && strings.EqualFold(u.Host, r.Host) && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}
func (h *WebHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	if !h.hostAllowed(r) {
		http.Error(w, "invalid host", http.StatusForbidden)
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
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src "+scheme+"://"+r.Host+"; style-src 'unsafe-inline'; img-src data:; connect-src 'none'; frame-ancestors 'self'; base-uri 'none'; form-action 'none'")
		data, err := webAssets.ReadFile("ui/diagram.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	case (r.URL.Path == "/app.js" || r.URL.Path == "/app.css" || r.URL.Path == "/diagram.js" || r.URL.Path == "/mermaid.min.js") && r.Method == "GET":
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
	case r.URL.Path == "/api/login" && r.Method == "POST":
		h.login(w, r)
	case r.URL.Path == "/api/remote-access" && r.Method == http.MethodGet:
		h.localRemoteAccess(w, r)
	case r.URL.Path == "/api/logout" && r.Method == "POST":
		h.logout(w, r)
	case r.URL.Path == "/api/projects" && r.Method == "GET":
		h.projects(w, r)
	case r.URL.Path == "/api/projects/create" && r.Method == "POST":
		h.createLocalProject(w, r)
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
	enabled, err := h.store.RemoteAccessEnabled(r.Context())
	if err != nil {
		jsonError(w, err)
		return
	}
	jsonReply(w, struct {
		Enabled bool `json:"enabled"`
	}{enabled})
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
	project, credential, repository, workspace, err := h.store.CreateProjectWithWorkspace(r.Context(), projectName, ownerName, repositoryName, node, observation)
	if err != nil {
		jsonError(w, err)
		return
	}
	p := Principal{ID: credential.PrincipalID, Name: ownerName, Role: "owner", ProjectID: project.ID, ProjectName: project.Name}
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
	{
		h.mu.Lock()
		h.sessions[sessionID] = session
		h.mu.Unlock()
	}
	// A cookie may exist but point to an in-memory session lost when the
	// dashboard restarted. Always replace it when creating a fresh session.
	if newSession {
		h.setSessionCookie(w, r, sessionID)
	}
	jsonReply(w, map[string]any{"project": p, "repository": repository, "workspace": workspace, "credential": map[string]string{"project_id": project.ID, "principal_id": credential.PrincipalID, "token": credential.Token}})
}

func (h *WebHandler) setSessionCookie(w http.ResponseWriter, r *http.Request, sid string) {
	http.SetCookie(w, &http.Cookie{Name: "gossip_session", Value: sid, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: 12 * 60 * 60})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "gossip_session", Value: "", Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

// recognizeProject searches only projects already connected to this browser
// session. A remote match is a suggestion, never project membership.
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
	_, session := h.session(r)
	if session == nil || session.clientPin != webClientPin(r) {
		jsonError(w, ErrUnauthorized)
		return
	}
	suggestions := []ProjectSuggestion{}
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
func (h *WebHandler) projectError(w http.ResponseWriter, r *http.Request, project, token string, err error) {
	if errors.Is(err, ErrUnauthorized) {
		valid, authErr := h.store.CredentialValid(r.Context(), project, token)
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
func (h *WebHandler) projects(w http.ResponseWriter, r *http.Request) {
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
	_, s := h.session(r)
	if s == nil {
		http.Error(w, "no project credential is connected to this browser; connect a credential for this project", http.StatusUnauthorized)
		return
	}
	token, ok := s.projects[project]
	if !ok {
		http.Error(w, "this project is not connected to the browser; connect its project credential first", http.StatusUnauthorized)
		return
	}
	ctx := r.Context()
	if s.clientPin != webClientPin(r) || !h.clientProjectAllowed(ctx, webClientPin(r), project) {
		http.Error(w, "this client certificate is not authorized for the selected project", http.StatusForbidden)
		return
	}
	if r.Method == "GET" {
		var v any
		var err error
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		switch action {
		case "plan":
			v, err = h.store.CurrentPlan(ctx, project, token)
		case "tasks":
			v, err = h.store.ListTasks(ctx, project, token, 200)
		case "task":
			v, err = h.store.GetTask(ctx, project, token, r.URL.Query().Get("id"))
		case "attempt":
			number, parseErr := strconv.Atoi(r.URL.Query().Get("number"))
			if parseErr != nil || number < 1 {
				err = errors.New("valid attempt number required")
			} else {
				v, err = h.store.GetAttempt(ctx, project, token, r.URL.Query().Get("id"), number)
			}
		case "task-search":
			v, err = h.store.SearchTasks(ctx, project, token, TaskFilter{Status: r.URL.Query().Get("status"), Kind: r.URL.Query().Get("kind"), Assignee: r.URL.Query().Get("assignee"), Query: r.URL.Query().Get("query"), Cursor: r.URL.Query().Get("cursor"), Limit: 50})
		case "task-counts":
			v, err = h.store.TaskCounts(ctx, project, token)
		case "settings":
			v, err = h.store.GetSettings(ctx, project, token)
		case "budget":
			v, err = h.store.GetBudget(ctx, project, token)
		case "agents":
			v, err = h.store.ListPrincipals(ctx, project, token)
		case "presence":
			v, err = h.store.ListAgentPresence(ctx, project, token)
		case "certificates":
			v, err = h.store.ListCertificateGrants(ctx, project, token)
		case "certificate-invitations":
			v, err = h.store.ListCertificateInvitations(ctx, project, token)
		case "replicas":
			v, err = h.store.ListProjectReplicas(ctx, project, token)
		case "permissions":
			v, err = h.store.ListPermissions(ctx, project, token)
		case "roles":
			v, err = h.store.ListWorkRoles(ctx, project, token)
		case "role-presets":
			_, err = h.store.WhoAmI(ctx, project, token)
			if err == nil {
				v = WorkRolePresets()
			}
		case "repositories":
			v, err = h.store.ListRepositories(ctx, project, token)
		case "workspaces":
			v, err = h.store.ListWorkspaces(ctx, project, token)
		case "events":
			if r.URL.Query().Get("recent") == "1" {
				v, err = h.store.RecentEvents(ctx, project, token, 100)
			} else {
				v, err = h.store.ReadEvents(ctx, project, token, after, 100)
			}
		case "retention":
			v, err = h.store.EventRetentionStatus(ctx, project, token)
		case "messages":
			if r.URL.Query().Get("recent") == "1" {
				v, err = h.store.RecentMessages(ctx, project, token, 100)
			} else {
				v, err = h.store.ListMessages(ctx, project, token, after, 100)
			}
		case "acknowledgments":
			v, err = h.store.MessageAcknowledgments(ctx, project, token, r.URL.Query().Get("id"))
		case "report":
			v, err = h.store.GetReport(ctx, project, token, r.URL.Query().Get("id"))
		case "candidate":
			v, err = h.store.GetCandidate(ctx, project, token, r.URL.Query().Get("id"))
		case "candidate-page":
			offset, parseErr := strconv.Atoi(r.URL.Query().Get("offset"))
			if parseErr != nil && r.URL.Query().Get("offset") != "" {
				err = errors.New("valid candidate offset required")
			} else {
				limit, limitErr := strconv.Atoi(r.URL.Query().Get("limit"))
				if limitErr != nil || r.URL.Query().Get("limit") == "" {
					limit = 4096
				}
				v, err = h.store.GetCandidatePage(ctx, project, token, r.URL.Query().Get("id"), offset, limit)
			}
		case "candidates":
			v, err = h.store.ListCandidates(ctx, project, token, r.URL.Query().Get("task_id"), 50)
		case "integrations":
			v, err = h.store.ListIntegrations(ctx, project, token, 100)
		case "legacy-imports":
			v, err = h.store.ListLegacyImports(ctx, project, token)
		case "legacy-records":
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			if limit < 1 || limit > 50 {
				limit = 20
			}
			v, err = h.store.ReadLegacyRecords(ctx, project, token, r.URL.Query().Get("import_id"), r.URL.Query().Get("type"), r.URL.Query().Get("after"), limit)
		case "legacy-record":
			offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			maxChars, _ := strconv.Atoi(r.URL.Query().Get("max_chars"))
			v, err = h.store.ReadLegacyRecordChunk(ctx, project, token, r.URL.Query().Get("import_id"), r.URL.Query().Get("type"), r.URL.Query().Get("id"), offset, maxChars)
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			h.projectError(w, r, project, token, err)
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
		Operation       string             `json:"operation"`
		ID              string             `json:"id"`
		Revision        int                `json:"revision"`
		Title           string             `json:"title"`
		Description     string             `json:"description"`
		Kind            string             `json:"kind"`
		Contract        ReportContract     `json:"contract"`
		Dependencies    []string           `json:"dependencies"`
		Body            string             `json:"body"`
		Recipient       string             `json:"recipient"`
		Subject         string             `json:"subject"`
		ThreadID        string             `json:"thread_id"`
		Attempt         int                `json:"attempt"`
		Baseline        string             `json:"baseline"`
		Digest          string             `json:"digest"`
		RepositoryID    string             `json:"repository_id"`
		RemoteURL       string             `json:"remote_url"`
		WorkspaceID     string             `json:"workspace_id"`
		Name            string             `json:"name"`
		Role            string             `json:"role"`
		PrincipalID     string             `json:"principal_id"`
		Credential      string             `json:"credential_token"`
		NodeLabel       string             `json:"node_label"`
		LocalPath       string             `json:"local_path"`
		Branch          string             `json:"branch"`
		Head            string             `json:"head"`
		Dirty           bool               `json:"dirty"`
		Settings        Settings           `json:"settings"`
		RequestID       string             `json:"request_id"`
		Amount          int64              `json:"amount"`
		ExpiresAt       int64              `json:"expires_at"`
		ThroughSequence int64              `json:"through_sequence"`
		Fingerprint     string             `json:"fingerprint"`
		InvitationToken string             `json:"invitation_token"`
		Permission      string             `json:"permission"`
		CandidateIDs    []string           `json:"candidate_ids"`
		Checks          []IntegrationCheck `json:"checks"`
		Observation     GitObservation     `json:"observation"`
	}
	if err := decodeBody(w, r, &req); err != nil {
		jsonError(w, err)
		return
	}
	var v any
	var err error
	switch action {
	case "plan":
		if req.Operation == "acknowledge" {
			err = h.store.AcknowledgePlan(ctx, project, token, req.Revision)
			v = map[string]bool{"acknowledged": err == nil}
		} else if req.Operation == "" || req.Operation == "publish" {
			v, err = h.store.PublishPlan(ctx, project, token, req.Body, req.Revision)
		} else {
			err = errors.New("unknown plan operation")
		}
	case "tasks":
		switch req.Operation {
		case "create":
			v, err = h.store.CreateTaskWithID(ctx, project, token, req.RequestID, req.Revision, req.Title, req.Description, req.Kind, req.Contract, req.Dependencies)
		case "request_review":
			err = h.store.RequestReview(ctx, project, token, req.ID, req.Attempt)
			v = map[string]bool{"requested": err == nil}
		case "claim":
			settings, e := h.store.GetSettings(ctx, project, token)
			if e != nil {
				err = e
				break
			}
			lease := settings.MaxLeaseSeconds
			if lease > 300 {
				lease = 300
			}
			v, err = h.store.ClaimTaskInWorkspace(ctx, project, token, req.ID, req.WorkspaceID, time.Duration(lease)*time.Second)
		case "assign":
			v, err = h.store.AssignTask(ctx, project, token, req.ID, req.Recipient)
		case "block", "resolve", "cancel":
			v, err = h.store.TransitionTask(ctx, project, token, req.ID, req.Operation, req.Body, req.Attempt)
		case "renew":
			settings, e := h.store.GetSettings(ctx, project, token)
			if e != nil {
				err = e
				break
			}
			lease := settings.MaxLeaseSeconds
			if lease > 300 {
				lease = 300
			}
			v, err = h.store.RenewLease(ctx, project, token, req.ID, req.Attempt, time.Duration(lease)*time.Second)
		case "reconcile":
			v, err = h.store.ReconcileTask(ctx, project, token, req.ID, req.Revision)
		default:
			err = errors.New("unknown task operation")
		}
	case "agents":
		if req.Operation == "invite" {
			v, err = h.store.AddPreparedPrincipal(ctx, project, token, req.Name, req.Role, Credential{PrincipalID: req.PrincipalID, Token: req.Credential})
			break
		}
		if req.Operation == "invite_remote" {
			v, err = h.store.AddPreparedRemotePrincipal(ctx, project, token, req.Name, req.Role, Credential{PrincipalID: req.PrincipalID, Token: req.Credential}, req.Fingerprint, req.ExpiresAt)
			break
		}
		if req.Operation != "revoke" {
			err = errors.New("unknown agent operation")
			break
		}
		err = h.store.RevokePrincipal(ctx, project, token, req.ID)
		v = map[string]bool{"revoked": err == nil}
	case "certificates":
		switch req.Operation {
		case "grant":
			v, err = h.store.AddCertificateGrantUntil(ctx, project, token, req.Fingerprint, req.ExpiresAt)
		case "revoke":
			err = h.store.RevokeCertificateGrant(ctx, project, token, req.Fingerprint)
			v = map[string]bool{"revoked": err == nil}
		default:
			err = errors.New("unknown certificate operation")
		}
	case "certificate-invitations":
		switch req.Operation {
		case "create":
			v, err = h.store.AddPreparedCertificateInvitation(ctx, project, token, req.ID, req.InvitationToken, req.Fingerprint, req.ExpiresAt)
		case "revoke":
			err = h.store.RevokeCertificateInvitation(ctx, project, token, req.ID)
			v = map[string]bool{"revoked": err == nil}
		default:
			err = errors.New("unknown certificate invitation operation")
		}
	case "permissions":
		switch req.Operation {
		case "grant":
			v, err = h.store.GrantPermission(ctx, project, token, req.PrincipalID, req.Permission)
		case "revoke":
			err = h.store.RevokePermission(ctx, project, token, req.PrincipalID, req.Permission)
			v = map[string]bool{"revoked": err == nil}
		default:
			err = errors.New("unknown permission operation")
		}
	case "messages":
		if req.Operation == "acknowledge" {
			v, err = h.store.AcknowledgeMessage(ctx, project, token, req.ID)
		} else if req.Operation == "" || req.Operation == "send" {
			v, err = h.store.SendMessageWithID(ctx, project, token, req.Recipient, req.ThreadID, req.Subject, req.Body, req.RequestID)
		} else {
			err = errors.New("unknown message operation")
		}
	case "repositories":
		switch req.Operation {
		case "add_hint":
			v, err = h.store.AddRepositoryRemoteHint(ctx, project, token, req.RepositoryID, req.RemoteURL)
		case "remove_hint":
			err = h.store.RemoveRepositoryRemoteHint(ctx, project, token, req.RepositoryID, req.RemoteURL)
			v = map[string]bool{"removed": err == nil}
		case "":
			v, err = h.store.CreateRepository(ctx, project, token, req.Name)
		default:
			err = errors.New("unknown repository operation")
		}
	case "workspaces":
		if req.Operation == "update" {
			v, err = h.store.UpdateWorkspace(ctx, project, token, req.WorkspaceID, req.Branch, req.Head, req.Dirty)
		} else if req.Operation == "" || req.Operation == "register" {
			v, err = h.store.RegisterWorkspace(ctx, project, token, req.RepositoryID, req.Name, req.NodeLabel, req.LocalPath, req.Branch, req.Head, req.Dirty)
		} else {
			err = errors.New("unknown workspace operation")
		}
	case "integrations":
		v, err = h.store.RecordIntegration(ctx, project, token, IntegrationSubmission{TaskID: req.ID, Attempt: req.Attempt, WorkspaceID: req.WorkspaceID, BaseRevision: req.Baseline, CombinedRevision: req.Head, Observation: req.Observation, CandidateIDs: req.CandidateIDs, Checks: req.Checks, Summary: req.Body, RequestID: req.RequestID})
	case "events":
		if req.Operation != "prune" {
			err = errors.New("unknown event operation")
		} else {
			v, err = h.store.PruneProjectEvents(ctx, project, token, req.ThroughSequence)
		}
	case "reports":
		v, err = h.store.SubmitReport(ctx, project, token, req.ID, req.Attempt, req.Baseline, req.Body)
	case "review":
		if req.Operation == "rework" {
			err = h.store.RequestRework(ctx, project, token, req.ID, req.Digest, req.Body)
			v = map[string]bool{"rework_requested": err == nil}
		} else if req.Operation == "" || req.Operation == "accept" {
			err = h.store.AcceptReport(ctx, project, token, req.ID, req.Digest)
			v = map[string]bool{"accepted": err == nil}
		} else {
			err = errors.New("unknown review operation")
		}
	case "roles":
		v, err = h.store.SetWorkRole(ctx, project, token, req.Recipient, req.ID, req.Name, req.Body, req.ExpiresAt, req.Revision)
	case "budget":
		if req.Operation == "set" {
			v, err = h.store.SetBudget(ctx, project, token, req.Revision, req.Amount)
		} else if req.Operation == "reserve" {
			v, err = h.store.ReserveBudget(ctx, project, token, req.ID, req.Amount)
		} else {
			err = errors.New("unknown budget operation")
		}
	case "settings":
		v, err = h.store.UpdateSettings(ctx, project, token, req.Settings)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.projectError(w, r, project, token, err)
		return
	}
	jsonReply(w, v)
}
