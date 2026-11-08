package coord

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// rpcRequest is an internal wire type. The project credential travels only in
// the HTTP Authorization header, over a mutually authenticated TLS channel.
type rpcRequest struct {
	Operation           string                 `json:"operation"`
	Project             string                 `json:"project"`
	ID                  string                 `json:"id,omitempty"`
	WorkspaceID         string                 `json:"workspace_id,omitempty"`
	RepositoryID        string                 `json:"repository_id,omitempty"`
	RemoteURL           string                 `json:"remote_url,omitempty"`
	Name                string                 `json:"name,omitempty"`
	OwnerName           string                 `json:"owner_name,omitempty"`
	Observation         GitObservation         `json:"observation,omitempty"`
	PreparedCredential  Credential             `json:"prepared_credential,omitempty"`
	Node                string                 `json:"node,omitempty"`
	Path                string                 `json:"path,omitempty"`
	Branch              string                 `json:"branch,omitempty"`
	Head                string                 `json:"head,omitempty"`
	Dirty               bool                   `json:"dirty,omitempty"`
	Revision            int                    `json:"revision,omitempty"`
	Title               string                 `json:"title,omitempty"`
	Description         string                 `json:"description,omitempty"`
	Kind                string                 `json:"kind,omitempty"`
	Contract            ReportContract         `json:"contract"`
	Dependencies        []string               `json:"dependencies,omitempty"`
	Integration         *IntegrationSubmission `json:"integration,omitempty"`
	LeaseSeconds        int                    `json:"lease_seconds,omitempty"`
	Attempt             int                    `json:"attempt,omitempty"`
	Baseline            string                 `json:"baseline,omitempty"`
	Body                string                 `json:"body,omitempty"`
	Usage               *UsageReport           `json:"usage,omitempty"`
	Digest              string                 `json:"digest,omitempty"`
	Recipient           string                 `json:"recipient,omitempty"`
	Thread              string                 `json:"thread,omitempty"`
	Subject             string                 `json:"subject,omitempty"`
	After               int64                  `json:"after,omitempty"`
	PruneThrough        int64                  `json:"prune_through,omitempty"`
	Limit               int                    `json:"limit,omitempty"`
	MaxChars            int                    `json:"max_chars,omitempty"`
	Settings            Settings               `json:"settings"`
	Filter              TaskFilter             `json:"filter"`
	RequestID           string                 `json:"request_id,omitempty"`
	InstanceID          string                 `json:"instance_id,omitempty"`
	Capabilities        []AgentCapability      `json:"capabilities,omitempty"`
	ClientInfo          MCPClientInfo          `json:"client_info,omitempty"`
	ReplicaID           string                 `json:"replica_id,omitempty"`
	Amount              int64                  `json:"amount,omitempty"`
	ExpiresAt           int64                  `json:"expires_at,omitempty"`
	ExpectedFingerprint string                 `json:"expected_fingerprint,omitempty"`
	Fingerprint         string                 `json:"fingerprint,omitempty"`
	Enabled             bool                   `json:"enabled,omitempty"`
}

// Remote speaks to an explicitly configured home node. It never follows
// redirects or retries mutations automatically after uncertain outcomes.
type Remote struct {
	endpoint     string
	client       *http.Client
	local        bool
	bootstrapKey string
	replicaID    string
}

func (r *Remote) SetReplicaID(id string) { r.replicaID = id }
func (r *Remote) CloseIdleConnections()  { r.client.CloseIdleConnections() }

// LocalRemoteAccessStatus reads the network-access kill switch through the private socket.
func (r *Remote) LocalRemoteAccessStatus(ctx context.Context) (bool, error) {
	if !r.local {
		return false, errors.New("remote-access control is local-only")
	}
	return remoteCall[bool](ctx, r, "", rpcRequest{Operation: "remote.access.status"})
}

func (r *Remote) RemoteAccessEnabled(ctx context.Context) (bool, error) {
	return r.LocalRemoteAccessStatus(ctx)
}

func (r *Remote) SetRemoteAccessEnabled(ctx context.Context, enabled bool) error {
	return r.LocalSetRemoteAccessEnabled(ctx, enabled)
}

// LocalSetRemoteAccessEnabled changes the network-access kill switch through the private socket.
func (r *Remote) LocalSetRemoteAccessEnabled(ctx context.Context, enabled bool) error {
	if !r.local {
		return errors.New("remote-access control is local-only")
	}
	_, err := remoteCall[struct {
		Enabled bool `json:"enabled"`
	}](ctx, r, "", rpcRequest{Operation: "remote.access.set", Enabled: enabled})
	return err
}

func NewRemote(endpoint, caFile, certFile, keyFile, serverName string) (*Remote, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("remote endpoint must be an https origin")
	}
	ca, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("invalid server CA certificate")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool, Certificates: []tls.Certificate{cert}, ServerName: serverName}}
	return &Remote{endpoint: strings.TrimSuffix(endpoint, "/") + "/v1/call", client: &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// NewRemoteBootstrap creates a TLS client authorized only for the server's
// create-only project bootstrap operation. Its API key is never used for
// project-scoped work after the owner credential is issued.
func NewRemoteBootstrap(endpoint, caFile, certFile, keyFile, serverName, apiKey string) (*Remote, error) {
	if len(strings.TrimSpace(apiKey)) < 32 {
		return nil, errors.New("bootstrap API key must contain at least 32 characters")
	}
	r, err := NewRemote(endpoint, caFile, certFile, keyFile, serverName)
	if err != nil {
		return nil, err
	}
	r.bootstrapKey = apiKey
	return r, nil
}

func remoteCall[T any](ctx context.Context, r *Remote, token string, in rpcRequest) (T, error) {
	var out T
	b, err := json.Marshal(in)
	if err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(b))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := r.client.Do(req)
	if err != nil {
		return out, fmt.Errorf("%w: remote request failed; mutation outcome may be unknown: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return out, ErrUnauthorized
		case http.StatusConflict:
			return out, ErrConflict
		case http.StatusNotFound:
			return out, ErrNotFound
		}
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if resp.StatusCode >= 500 {
			return out, fmt.Errorf("%w: remote operation failed (%d): %s", ErrUnavailable, resp.StatusCode, strings.TrimSpace(string(msg)))
		}
		return out, fmt.Errorf("remote operation failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 8*1024*1024)).Decode(&out)
	if err != nil {
		return out, fmt.Errorf("%w: invalid remote response; mutation outcome may be unknown: %v", ErrUnavailable, err)
	}
	return out, err
}

// RPCHandler requires a verified client certificate with a database-backed
// certificate/project grant and a project-scoped bearer credential.
func RPCHandler(s *Store) http.Handler { return rpcHandler(s, true, nil, true, "") }

// RPCHandlerWithBootstrapKey additionally enables one project creation
// operation for CA-verified TLS clients that possess the create-only key.
func RPCHandlerWithBootstrapKey(s *Store, apiKey string) (http.Handler, error) {
	if len(strings.TrimSpace(apiKey)) < 32 {
		return nil, errors.New("bootstrap API key must contain at least 32 characters")
	}
	return rpcHandler(s, true, nil, true, apiKey), nil
}

func RPCHandlerWithCertificateGrants(s *Store) http.Handler { return RPCHandler(s) }

// RPCHandlerWithClientPins additionally applies a process-local certificate
// allowlist; database-backed project grants remain mandatory.
func RPCHandlerWithClientPins(s *Store, fingerprints []string) (http.Handler, error) {
	if len(fingerprints) == 0 {
		return nil, errors.New("at least one client certificate fingerprint is required")
	}
	pins := make(map[[32]byte]map[string]bool, len(fingerprints))
	for _, value := range fingerprints {
		raw, err := hex.DecodeString(strings.ReplaceAll(strings.TrimSpace(value), ":", ""))
		if err != nil || len(raw) != 32 {
			return nil, errors.New("client fingerprint must be a SHA-256 hex digest")
		}
		var pin [32]byte
		copy(pin[:], raw)
		pins[pin] = map[string]bool{"*": true}
	}
	return rpcHandler(s, true, pins, true, ""), nil
}

// RPCHandlerWithProjectPins adds a process-local project allowlist on top of
// database-backed certificate grants.
func RPCHandlerWithProjectPins(s *Store, access map[string][]string) (http.Handler, error) {
	pins := make(map[[32]byte]map[string]bool, len(access))
	for value, projects := range access {
		raw, err := hex.DecodeString(strings.ReplaceAll(strings.TrimSpace(value), ":", ""))
		if err != nil || len(raw) != 32 || len(projects) == 0 {
			return nil, errors.New("invalid certificate fingerprint or empty project grant")
		}
		var pin [32]byte
		copy(pin[:], raw)
		grants := map[string]bool{}
		for _, project := range projects {
			if strings.TrimSpace(project) == "" {
				return nil, errors.New("empty project grant")
			}
			grants[project] = true
		}
		pins[pin] = grants
	}
	if len(pins) == 0 {
		return nil, errors.New("at least one client project grant is required")
	}
	return rpcHandler(s, true, pins, true, ""), nil
}

func rpcHandler(s *Store, requireTLS bool, pins map[[32]byte]map[string]bool, certificateGrants bool, bootstrapKey string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/call" {
			http.NotFound(w, r)
			return
		}
		if requireTLS && (r.TLS == nil || len(r.TLS.VerifiedChains) == 0) {
			http.Error(w, "mutual TLS required", http.StatusUnauthorized)
			return
		}
		if requireTLS {
			enabled, err := s.RemoteAccessEnabled(r.Context())
			if err != nil || !enabled {
				http.Error(w, "remote coordination is disabled by the local kill switch", http.StatusServiceUnavailable)
				return
			}
		}
		var q rpcRequest
		if err := decodeBody(w, r, &q); err != nil {
			jsonError(w, err)
			return
		}
		if pins != nil {
			if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
				http.Error(w, "client certificate required", http.StatusUnauthorized)
				return
			}
			leaf := r.TLS.PeerCertificates[0]
			fingerprint := sha256.Sum256(leaf.Raw)
			projects, enrolled := pins[fingerprint]
			if !enrolled || (!projects["*"] && !projects[q.Project]) {
				http.Error(w, "client certificate is not authorized for this project", http.StatusForbidden)
				return
			}
		}
		invitationRedemption := q.Operation == "certificate.invitation.redeem" && requireTLS
		bootstrapOperation := q.Operation == "project.create_workspace_bootstrap_prepared"
		if certificateGrants && !invitationRedemption && !bootstrapOperation {
			if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 || q.Project == "" {
				http.Error(w, "client certificate is not authorized for this project", http.StatusForbidden)
				return
			}
			fingerprint := sha256.Sum256(r.TLS.PeerCertificates[0].Raw)
			allowed, err := s.CertificateAuthorized(r.Context(), q.Project, hex.EncodeToString(fingerprint[:]))
			if err != nil || !allowed {
				http.Error(w, "client certificate is not authorized for this project", http.StatusForbidden)
				return
			}
		}
		if bootstrapOperation {
			if !requireTLS || r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
				http.Error(w, "verified bootstrap client certificate required", http.StatusUnauthorized)
				return
			}
			fingerprint := sha256.Sum256(r.TLS.PeerCertificates[0].Raw)
			q.Fingerprint = hex.EncodeToString(fingerprint[:])
		}
		header := r.Header.Get("Authorization")
		token := ""
		localCertificateQuery := q.Operation == "certificate.authorized" && !requireTLS
		localSwitch := q.Operation == "remote.access.status" || q.Operation == "remote.access.set"
		if (q.Operation != "project.create_prepared_local" && q.Operation != "project.create_workspace_local" && q.Operation != "project.create_workspace_local_prepared" && !localCertificateQuery && !localSwitch) || requireTLS {
			if !strings.HasPrefix(header, "Bearer ") || len(header) > 256 {
				http.Error(w, "credential required", http.StatusUnauthorized)
				return
			}
			token = strings.TrimPrefix(header, "Bearer ")
		}
		ctx := r.Context()
		var v any
		var err error
		switch q.Operation {
		case "remote.access.status":
			if requireTLS {
				err = ErrUnauthorized
				break
			}
			v, err = s.RemoteAccessEnabled(ctx)
		case "remote.access.set":
			if requireTLS {
				err = ErrUnauthorized
				break
			}
			err = s.SetRemoteAccessEnabled(ctx, q.Enabled)
			v = struct {
				Enabled bool `json:"enabled"`
			}{q.Enabled}
		case "certificate.authorized":
			if requireTLS {
				err = ErrUnauthorized
				break
			}
			v, err = s.CertificateAuthorized(ctx, q.Project, q.Body)
		case "project.create_prepared_local":
			if requireTLS {
				err = ErrUnauthorized
				break
			}
			v, err = s.CreatePreparedProject(ctx, q.Project, q.Name, q.Description, Credential{PrincipalID: q.ID, Token: q.Body})
		case "project.create_workspace_local":
			if requireTLS {
				err = ErrUnauthorized
				break
			}
			var project Project
			var credential Credential
			var repository Repository
			var workspace Workspace
			project, credential, repository, workspace, err = s.CreateProjectWithWorkspace(ctx, q.Name, q.OwnerName, q.Title, q.Node, q.Observation)
			v = struct {
				Project    Project    `json:"project"`
				Credential Credential `json:"credential"`
				Repository Repository `json:"repository"`
				Workspace  Workspace  `json:"workspace"`
			}{project, credential, repository, workspace}
		case "project.create_workspace_local_prepared":
			if requireTLS {
				err = ErrUnauthorized
				break
			}
			var project Project
			var credential Credential
			var repository Repository
			var workspace Workspace
			project, credential, repository, workspace, err = s.CreatePreparedProjectWithWorkspace(ctx, q.Project, q.Name, q.OwnerName, q.Title, q.Node, q.Observation, q.PreparedCredential)
			v = struct {
				Project    Project    `json:"project"`
				Credential Credential `json:"credential"`
				Repository Repository `json:"repository"`
				Workspace  Workspace  `json:"workspace"`
			}{project, credential, repository, workspace}
		case "project.create_workspace_bootstrap_prepared":
			provided := sha256.Sum256([]byte(token))
			expected := sha256.Sum256([]byte(bootstrapKey))
			if !requireTLS || bootstrapKey == "" || subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
				err = ErrUnauthorized
				break
			}
			var project Project
			var credential Credential
			var repository Repository
			var workspace Workspace
			project, credential, repository, workspace, err = s.CreatePreparedProjectWithWorkspaceAndCertificate(ctx, q.Project, q.Name, q.OwnerName, q.Title, q.Node, q.Observation, q.PreparedCredential, q.Fingerprint)
			v = struct {
				Project    Project    `json:"project"`
				Credential Credential `json:"credential"`
				Repository Repository `json:"repository"`
				Workspace  Workspace  `json:"workspace"`
			}{project, credential, repository, workspace}
		case "principal.add_prepared":
			_, err = s.AddPreparedPrincipal(ctx, q.Project, token, q.Name, q.Kind, Credential{PrincipalID: q.ID, Token: q.Body})
			v = map[string]bool{"created": err == nil}
		case "principal.add_remote_prepared":
			_, err = s.AddPreparedRemotePrincipal(ctx, q.Project, token, q.Name, q.Kind, Credential{PrincipalID: q.ID, Token: q.Body}, q.Fingerprint, q.ExpiresAt)
			v = map[string]bool{"created": err == nil}
		case "identity.read":
			v, err = s.WhoAmI(ctx, q.Project, token)
		case "identity.validate":
			var valid bool
			valid, err = s.CredentialValid(ctx, q.Project, token)
			v = map[string]bool{"valid": valid}
		case "task.counts":
			v, err = s.TaskCounts(ctx, q.Project, token)
		case "event.recent":
			v, err = s.RecentEvents(ctx, q.Project, token, q.Limit)
		case "message.recent":
			v, err = s.RecentMessages(ctx, q.Project, token, q.Limit)
		case "plan.read":
			v, err = s.CurrentPlan(ctx, q.Project, token)
		case "role.list":
			v, err = s.ListWorkRoles(ctx, q.Project, token)
		case "role.assign":
			v, err = s.SetWorkRole(ctx, q.Project, token, q.Recipient, q.ID, q.Name, q.Body, q.ExpiresAt, q.Revision)
		case "principal.list":
			v, err = s.ListPrincipals(ctx, q.Project, token)
		case "agent.heartbeat":
			err = s.HeartbeatAgentWithMetadata(ctx, q.Project, token, q.InstanceID, q.Capabilities, q.ClientInfo)
			v = map[string]bool{"recorded": err == nil}
		case "agent.end":
			err = s.EndAgentSession(ctx, q.Project, token, q.InstanceID)
			v = map[string]bool{"ended": err == nil}
		case "agent.presence":
			v, err = s.ListAgentPresence(ctx, q.Project, token)
		case "principal.revoke":
			err = s.RevokePrincipal(ctx, q.Project, token, q.ID)
			v = map[string]bool{"revoked": err == nil}
		case "certificate.list":
			v, err = s.ListCertificateGrants(ctx, q.Project, token)
		case "certificate.add":
			v, err = s.AddCertificateGrantUntil(ctx, q.Project, token, q.Body, q.ExpiresAt)
		case "certificate.revoke":
			err = s.RevokeCertificateGrant(ctx, q.Project, token, q.Body)
			v = map[string]bool{"revoked": err == nil}
		case "certificate.invitation.create":
			v, err = s.AddPreparedCertificateInvitation(ctx, q.Project, token, q.ID, q.Body, q.ExpectedFingerprint, q.ExpiresAt)
		case "certificate.invitation.list":
			v, err = s.ListCertificateInvitations(ctx, q.Project, token)
		case "certificate.invitation.revoke":
			err = s.RevokeCertificateInvitation(ctx, q.Project, token, q.ID)
			v = map[string]bool{"revoked": err == nil}
		case "certificate.invitation.redeem":
			if !invitationRedemption || r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
				err = ErrUnauthorized
				break
			}
			fingerprint := sha256.Sum256(r.TLS.PeerCertificates[0].Raw)
			v, err = s.RedeemCertificateInvitation(ctx, q.Project, q.ID, token, hex.EncodeToString(fingerprint[:]))
		case "permission.grant":
			v, err = s.GrantPermission(ctx, q.Project, token, q.ID, q.Kind)
		case "permission.list":
			v, err = s.ListPermissions(ctx, q.Project, token)
		case "permission.revoke":
			err = s.RevokePermission(ctx, q.Project, token, q.ID, q.Kind)
			v = map[string]bool{"revoked": err == nil}
		case "legacy.list":
			v, err = s.ListLegacyImports(ctx, q.Project, token)
		case "legacy.read":
			v, err = s.ReadLegacyRecords(ctx, q.Project, token, q.ID, q.Kind, q.Body, q.Limit)
		case "legacy.record":
			v, err = s.ReadLegacyRecordChunk(ctx, q.Project, token, q.ID, q.Kind, q.Recipient, int(q.After), q.MaxChars)
		case "budget.read":
			v, err = s.GetBudget(ctx, q.Project, token)
		case "budget.set":
			v, err = s.SetBudget(ctx, q.Project, token, q.Revision, q.Amount)
		case "budget.reserve":
			v, err = s.ReserveBudget(ctx, q.Project, token, q.ID, q.Amount)
		case "settings.read":
			v, err = s.GetSettings(ctx, q.Project, token)
		case "settings.update":
			v, err = s.UpdateSettings(ctx, q.Project, token, q.Settings)
		case "plan.publish":
			v, err = s.PublishPlan(ctx, q.Project, token, q.Body, q.Revision)
		case "plan.acknowledge":
			err = s.AcknowledgePlan(ctx, q.Project, token, q.Revision)
			v = true
		case "repository.create":
			v, err = s.CreateRepository(ctx, q.Project, token, q.Name)
		case "repository.remote_hint.add":
			v, err = s.AddRepositoryRemoteHint(ctx, q.Project, token, q.RepositoryID, q.RemoteURL)
		case "repository.remote_hint.remove":
			err = s.RemoveRepositoryRemoteHint(ctx, q.Project, token, q.RepositoryID, q.RemoteURL)
			v = map[string]bool{"removed": err == nil}
		case "repository.list":
			v, err = s.ListRepositories(ctx, q.Project, token)
		case "repository.match_remote":
			v, err = s.FindRepositoriesByRemote(ctx, q.Project, token, q.RemoteURL)
		case "workspace.register":
			v, err = s.RegisterWorkspace(ctx, q.Project, token, q.RepositoryID, q.Name, q.Node, q.Path, q.Branch, q.Head, q.Dirty)
		case "workspace.update":
			v, err = s.UpdateWorkspace(ctx, q.Project, token, q.WorkspaceID, q.Branch, q.Head, q.Dirty)
		case "workspace.list":
			v, err = s.ListWorkspaces(ctx, q.Project, token)
		case "attempt.get":
			v, err = s.GetAttempt(ctx, q.Project, token, q.ID, q.Attempt)
		case "task.create":
			v, err = s.CreateTaskWithID(ctx, q.Project, token, q.RequestID, q.Revision, q.Title, q.Description, q.Kind, q.Contract, q.Dependencies)
		case "task.list":
			v, err = s.ListTasks(ctx, q.Project, token, q.Limit)
		case "task.search":
			v, err = s.SearchTasks(ctx, q.Project, token, q.Filter)
		case "task.get":
			v, err = s.GetTask(ctx, q.Project, token, q.ID)
		case "task.assign":
			v, err = s.AssignTask(ctx, q.Project, token, q.ID, q.Recipient)
		case "task.request_review":
			err = s.RequestReview(ctx, q.Project, token, q.ID, q.Attempt)
			v = map[string]bool{"requested": err == nil}
		case "task.transition":
			v, err = s.TransitionTask(ctx, q.Project, token, q.ID, q.Kind, q.Body, q.Attempt)
		case "task.claim":
			v, err = s.ClaimTaskInWorkspace(ctx, q.Project, token, q.ID, q.WorkspaceID, time.Duration(q.LeaseSeconds)*time.Second)
		case "task.claim_observed":
			v, err = s.ClaimObservedWorkspace(ctx, q.Project, token, q.ID, q.WorkspaceID, GitObservation{Root: q.Path, Branch: q.Branch, Head: q.Head, Dirty: q.Dirty}, time.Duration(q.LeaseSeconds)*time.Second)
		case "task.renew":
			v, err = s.RenewLease(ctx, q.Project, token, q.ID, q.Attempt, time.Duration(q.LeaseSeconds)*time.Second)
		case "task.reconcile":
			v, err = s.ReconcileTask(ctx, q.Project, token, q.ID, q.Revision)
		case "candidate.save":
			v, err = s.SaveCandidateWithID(ctx, q.Project, token, q.ID, q.Attempt, q.Baseline, q.Body, q.RequestID)
		case "candidate.list":
			v, err = s.ListCandidates(ctx, q.Project, token, q.ID, q.Limit)
		case "candidate.get":
			v, err = s.GetCandidate(ctx, q.Project, token, q.ID)
		case "candidate.page":
			v, err = s.GetCandidatePage(ctx, q.Project, token, q.ID, int(q.After), q.Limit)
		case "integration.record":
			if q.Integration == nil {
				err = errors.New("integration submission required")
			} else {
				v, err = s.RecordIntegration(ctx, q.Project, token, *q.Integration)
			}
		case "integration.list":
			v, err = s.ListIntegrations(ctx, q.Project, token, q.Limit)
		case "report.submit":
			var report Report
			if q.RequestID != "" {
				report, err = s.SubmitReportWithID(ctx, q.Project, token, q.ID, q.Attempt, q.Baseline, q.Body, q.RequestID)
			} else {
				report, err = s.SubmitReport(ctx, q.Project, token, q.ID, q.Attempt, q.Baseline, q.Body)
			}
			report.Body = ""
			v = report
		case "usage.record":
			if q.Usage == nil {
				err = errors.New("usage report is required")
			} else {
				v, err = s.RecordAttemptUsage(ctx, q.Project, token, q.ID, q.Attempt, q.RequestID, *q.Usage)
			}
		case "report.accept":
			err = s.AcceptReport(ctx, q.Project, token, q.ID, q.Digest)
			v = true
		case "report.rework":
			err = s.RequestRework(ctx, q.Project, token, q.ID, q.Digest, q.Body)
			v = true
		case "report.get":
			v, err = s.GetReport(ctx, q.Project, token, q.ID)
		case "report.page":
			v, err = s.GetReportPage(ctx, q.Project, token, q.ID, int(q.After), q.Limit)
		case "message.send":
			v, err = s.SendMessageWithID(ctx, q.Project, token, q.Recipient, q.Thread, q.Subject, q.Body, q.RequestID)
		case "message.list":
			v, err = s.ListMessages(ctx, q.Project, token, q.After, q.Limit)
		case "message.acknowledge":
			v, err = s.AcknowledgeMessage(ctx, q.Project, token, q.ID)
		case "message.acknowledgments":
			v, err = s.MessageAcknowledgments(ctx, q.Project, token, q.ID)
		case "event.list":
			v, err = s.ReadEvents(ctx, q.Project, token, q.After, q.Limit)
		case "project.sync":
			if q.ReplicaID == "" {
				v, err = s.SyncProject(ctx, q.Project, token, q.After, q.Limit)
			} else if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
				err = ErrUnauthorized
			} else {
				fingerprint := sha256.Sum256(r.TLS.PeerCertificates[0].Raw)
				v, err = s.SyncProjectFromReplica(ctx, q.Project, token, q.ReplicaID, hex.EncodeToString(fingerprint[:]), q.After, q.Limit)
			}
		case "replica.list":
			v, err = s.ListProjectReplicas(ctx, q.Project, token)
		case "event.checkpoint":
			v, err = s.EventCheckpoint(ctx, q.Project, token)
		case "event.retention":
			v, err = s.EventRetentionStatus(ctx, q.Project, token)
		case "event.acknowledge":
			v, err = s.AcknowledgeEvents(ctx, q.Project, token, q.After)
		case "event.prune":
			v, err = s.PruneProjectEvents(ctx, q.Project, token, q.PruneThrough)
		default:
			err = errors.New("unknown operation")
		}
		if err != nil {
			jsonError(w, err)
			return
		}
		jsonReply(w, v)
	})
}
