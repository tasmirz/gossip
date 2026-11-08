package coord

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"strings"
	"time"
)

func (r *Remote) CreateProjectWithWorkspace(ctx context.Context, name, ownerName, repositoryName, node string, observation GitObservation) (Project, Credential, Repository, Workspace, error) {
	operation, token := "", ""
	if r.local {
		operation = "project.create_workspace_local"
	} else {
		return Project{}, Credential{}, Repository{}, Workspace{}, ErrUnauthorized
	}
	result, err := remoteCall[struct {
		Project    Project    `json:"project"`
		Credential Credential `json:"credential"`
		Repository Repository `json:"repository"`
		Workspace  Workspace  `json:"workspace"`
	}](ctx, r, token, rpcRequest{Operation: operation, Name: name, OwnerName: ownerName, Title: repositoryName, Node: node, Observation: observation})
	return result.Project, result.Credential, result.Repository, result.Workspace, err
}

func (r *Remote) CreatePreparedProjectWithWorkspace(ctx context.Context, projectID, name, ownerName, repositoryName, node string, observation GitObservation, credential Credential) (Project, Credential, Repository, Workspace, error) {
	operation, token := "", ""
	if r.local {
		operation = "project.create_workspace_local_prepared"
	} else if r.bootstrapKey != "" {
		operation, token = "project.create_workspace_bootstrap_prepared", r.bootstrapKey
	} else {
		return Project{}, Credential{}, Repository{}, Workspace{}, ErrUnauthorized
	}
	result, err := remoteCall[struct {
		Project    Project    `json:"project"`
		Credential Credential `json:"credential"`
		Repository Repository `json:"repository"`
		Workspace  Workspace  `json:"workspace"`
	}](ctx, r, token, rpcRequest{Operation: operation, Project: projectID, Name: name, OwnerName: ownerName, Title: repositoryName, Node: node, Observation: observation, PreparedCredential: credential})
	return result.Project, result.Credential, result.Repository, result.Workspace, err
}

// CreatePreparedProjectWithWorkspaceUsingBootstrapKey authenticates the local
// create-only MCP path against the home database or the configured remote key.
func (r *Remote) CreatePreparedProjectWithWorkspaceUsingBootstrapKey(ctx context.Context, key, projectID, name, ownerName, repositoryName, node string, observation GitObservation, credential Credential) (Project, Credential, Repository, Workspace, error) {
	if r.local {
		result, err := remoteCall[struct {
			Project    Project    `json:"project"`
			Credential Credential `json:"credential"`
			Repository Repository `json:"repository"`
			Workspace  Workspace  `json:"workspace"`
		}](ctx, r, key, rpcRequest{Operation: "project.create_workspace_local_bootstrap_prepared", Project: projectID, Name: name, OwnerName: ownerName, Title: repositoryName, Node: node, Observation: observation, PreparedCredential: credential})
		return result.Project, result.Credential, result.Repository, result.Workspace, err
	}
	provided := sha256.Sum256([]byte(strings.TrimSpace(key)))
	expected := sha256.Sum256([]byte(strings.TrimSpace(r.bootstrapKey)))
	if r.bootstrapKey == "" || subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
		return Project{}, Credential{}, Repository{}, Workspace{}, ErrUnauthorized
	}
	return r.CreatePreparedProjectWithWorkspace(ctx, projectID, name, ownerName, repositoryName, node, observation, credential)
}

func (r *Remote) GetSettings(c context.Context, p, t string) (Settings, error) {
	return remoteCall[Settings](c, r, t, rpcRequest{Operation: "settings.read", Project: p})
}
func (r *Remote) UpdateSettings(c context.Context, p, t string, settings Settings) (Settings, error) {
	return remoteCall[Settings](c, r, t, rpcRequest{Operation: "settings.update", Project: p, Settings: settings})
}

func (r *Remote) CurrentPlan(c context.Context, p, t string) (Plan, error) {
	return remoteCall[Plan](c, r, t, rpcRequest{Operation: "plan.read", Project: p})
}
func (r *Remote) PublishPlan(c context.Context, p, t, b string, n int) (int, error) {
	return remoteCall[int](c, r, t, rpcRequest{Operation: "plan.publish", Project: p, Body: b, Revision: n})
}
func (r *Remote) AcknowledgePlan(c context.Context, p, t string, n int) error {
	_, e := remoteCall[bool](c, r, t, rpcRequest{Operation: "plan.acknowledge", Project: p, Revision: n})
	return e
}
func (r *Remote) CreateRepository(c context.Context, p, t, n string) (Repository, error) {
	return remoteCall[Repository](c, r, t, rpcRequest{Operation: "repository.create", Project: p, Name: n})
}
func (r *Remote) AddRepositoryRemoteHint(c context.Context, p, t, id, remoteURL string) (Repository, error) {
	return remoteCall[Repository](c, r, t, rpcRequest{Operation: "repository.remote_hint.add", Project: p, RepositoryID: id, RemoteURL: remoteURL})
}
func (r *Remote) RemoveRepositoryRemoteHint(c context.Context, p, t, id, remoteURL string) error {
	_, err := remoteCall[map[string]bool](c, r, t, rpcRequest{Operation: "repository.remote_hint.remove", Project: p, RepositoryID: id, RemoteURL: remoteURL})
	return err
}
func (r *Remote) ListRepositories(c context.Context, p, t string) ([]Repository, error) {
	return remoteCall[[]Repository](c, r, t, rpcRequest{Operation: "repository.list", Project: p})
}
func (r *Remote) FindRepositoriesByRemote(c context.Context, p, t, remoteURL string) ([]Repository, error) {
	return remoteCall[[]Repository](c, r, t, rpcRequest{Operation: "repository.match_remote", Project: p, RemoteURL: remoteURL})
}
func (r *Remote) RegisterWorkspace(c context.Context, p, t, repo, name, node, path, branch, head string, dirty bool) (Workspace, error) {
	return remoteCall[Workspace](c, r, t, rpcRequest{Operation: "workspace.register", Project: p, RepositoryID: repo, Name: name, Node: node, Path: path, Branch: branch, Head: head, Dirty: dirty})
}
func (r *Remote) UpdateWorkspace(c context.Context, p, t, id, branch, head string, dirty bool) (Workspace, error) {
	return remoteCall[Workspace](c, r, t, rpcRequest{Operation: "workspace.update", Project: p, WorkspaceID: id, Branch: branch, Head: head, Dirty: dirty})
}
func (r *Remote) ListWorkspaces(c context.Context, p, t string) ([]Workspace, error) {
	return remoteCall[[]Workspace](c, r, t, rpcRequest{Operation: "workspace.list", Project: p})
}
func (r *Remote) GetAttempt(c context.Context, p, t, id string, n int) (Attempt, error) {
	return remoteCall[Attempt](c, r, t, rpcRequest{Operation: "attempt.get", Project: p, ID: id, Attempt: n})
}
func (r *Remote) CreateTask(c context.Context, p, t string, n int, title, desc, kind string, contract ReportContract, deps []string) (Task, error) {
	return Task{}, errors.New("remote task creation requires a stable request ID; use CreateTaskWithID")
}
func (r *Remote) CreateTaskWithID(c context.Context, p, t, requestID string, n int, title, desc, kind string, contract ReportContract, deps []string) (Task, error) {
	return remoteCall[Task](c, r, t, rpcRequest{Operation: "task.create", Project: p, RequestID: requestID, Revision: n, Title: title, Description: desc, Kind: kind, Contract: contract, Dependencies: deps})
}
func (r *Remote) RequestReview(c context.Context, p, t, id string, attempt int) error {
	_, err := remoteCall[map[string]bool](c, r, t, rpcRequest{Operation: "task.request_review", Project: p, ID: id, Attempt: attempt})
	return err
}
func (r *Remote) ListTasks(c context.Context, p, t string, n int) ([]Task, error) {
	return remoteCall[[]Task](c, r, t, rpcRequest{Operation: "task.list", Project: p, Limit: n})
}
func (r *Remote) GetTask(c context.Context, p, t, id string) (Task, error) {
	return remoteCall[Task](c, r, t, rpcRequest{Operation: "task.get", Project: p, ID: id})
}
func (r *Remote) ClaimTaskInWorkspace(c context.Context, p, t, id, ws string, lease time.Duration) (Task, error) {
	return remoteCall[Task](c, r, t, rpcRequest{Operation: "task.claim", Project: p, ID: id, WorkspaceID: ws, LeaseSeconds: int(lease / time.Second)})
}
func (r *Remote) RenewLease(c context.Context, p, t, id string, n int, lease time.Duration) (Task, error) {
	return remoteCall[Task](c, r, t, rpcRequest{Operation: "task.renew", Project: p, ID: id, Attempt: n, LeaseSeconds: int(lease / time.Second)})
}
func (r *Remote) ReconcileTask(c context.Context, p, t, id string, n int) (Task, error) {
	return remoteCall[Task](c, r, t, rpcRequest{Operation: "task.reconcile", Project: p, ID: id, Revision: n})
}
func (r *Remote) SubmitReport(c context.Context, p, t, id string, n int, base, body string) (Report, error) {
	return r.SubmitReportWithID(c, p, t, id, n, base, body, stableReportRequestID(id, n))
}
func (r *Remote) SubmitReportWithID(c context.Context, p, t, id string, n int, base, body, requestID string) (Report, error) {
	report, err := remoteCall[Report](c, r, t, rpcRequest{Operation: "report.submit", Project: p, ID: id, Attempt: n, Baseline: base, Body: body, RequestID: requestID})
	if err == nil {
		report.Body = body
	}
	return report, err
}
func (r *Remote) RecordAttemptUsage(c context.Context, p, t, id string, n int, requestID string, usage UsageReport) (UsageReport, error) {
	return remoteCall[UsageReport](c, r, t, rpcRequest{Operation: "usage.record", Project: p, ID: id, Attempt: n, RequestID: requestID, Usage: &usage})
}
func (r *Remote) AcceptReport(c context.Context, p, t, id, digest string) error {
	_, e := remoteCall[bool](c, r, t, rpcRequest{Operation: "report.accept", Project: p, ID: id, Digest: digest})
	return e
}
func (r *Remote) RequestRework(c context.Context, p, t, id, digest, feedback string) error {
	_, e := remoteCall[bool](c, r, t, rpcRequest{Operation: "report.rework", Project: p, ID: id, Digest: digest, Body: feedback})
	return e
}
func (r *Remote) GetReport(c context.Context, p, t, id string) (Report, error) {
	return remoteCall[Report](c, r, t, rpcRequest{Operation: "report.get", Project: p, ID: id})
}
func (r *Remote) SendMessage(c context.Context, p, t, to, thread, subject, body string) (Message, error) {
	return remoteCall[Message](c, r, t, rpcRequest{Operation: "message.send", Project: p, Recipient: to, Thread: thread, Subject: subject, Body: body})
}
func (r *Remote) SendMessageWithID(c context.Context, p, t, to, thread, subject, body, requestID string) (Message, error) {
	return remoteCall[Message](c, r, t, rpcRequest{Operation: "message.send", Project: p, Recipient: to, Thread: thread, Subject: subject, Body: body, RequestID: requestID})
}
func (r *Remote) ListMessages(c context.Context, p, t string, after int64, n int) ([]Message, error) {
	return remoteCall[[]Message](c, r, t, rpcRequest{Operation: "message.list", Project: p, After: after, Limit: n})
}
func (r *Remote) ReadEvents(c context.Context, p, t string, after int64, n int) ([]Event, error) {
	return remoteCall[[]Event](c, r, t, rpcRequest{Operation: "event.list", Project: p, After: after, Limit: n})
}
func (r *Remote) SyncProject(c context.Context, p, t string, after int64, n int) (ProjectSyncFrame, error) {
	return remoteCall[ProjectSyncFrame](c, r, t, rpcRequest{Operation: "project.sync", Project: p, ReplicaID: r.replicaID, After: after, Limit: n})
}
func (r *Remote) ListProjectReplicas(c context.Context, p, t string) ([]ProjectReplica, error) {
	return remoteCall[[]ProjectReplica](c, r, t, rpcRequest{Operation: "replica.list", Project: p})
}

var _ Backend = (*Remote)(nil)
var _ Backend = (*Store)(nil)

func (r *Remote) ClaimObservedWorkspace(c context.Context, p, t, id, workspace string, o GitObservation, lease time.Duration) (Task, error) {
	return remoteCall[Task](c, r, t, rpcRequest{Operation: "task.claim_observed", Project: p, ID: id, WorkspaceID: workspace, Path: o.Root, Branch: o.Branch, Head: o.Head, Dirty: o.Dirty, LeaseSeconds: int(lease / time.Second)})
}

func (r *Remote) GetReportPage(c context.Context, p, t, id string, offset, limit int) (ReportPage, error) {
	return remoteCall[ReportPage](c, r, t, rpcRequest{Operation: "report.page", Project: p, ID: id, After: int64(offset), Limit: limit})
}

func (r *Remote) SearchTasks(c context.Context, p, t string, f TaskFilter) (TaskPage, error) {
	return remoteCall[TaskPage](c, r, t, rpcRequest{Operation: "task.search", Project: p, Filter: f})
}

func (r *Remote) TransitionTask(c context.Context, p, t, id, action, reason string, attempt int) (Task, error) {
	return remoteCall[Task](c, r, t, rpcRequest{Operation: "task.transition", Project: p, ID: id, Kind: action, Body: reason, Attempt: attempt})
}

func (r *Remote) ListPrincipals(c context.Context, p, t string) ([]Principal, error) {
	return remoteCall[[]Principal](c, r, t, rpcRequest{Operation: "principal.list", Project: p})
}

func (r *Remote) HeartbeatAgent(c context.Context, p, t, instanceID string) error {
	return r.HeartbeatAgentWithCapabilities(c, p, t, instanceID, nil)
}

func (r *Remote) HeartbeatAgentWithCapabilities(c context.Context, p, t, instanceID string, capabilities []AgentCapability) error {
	return r.HeartbeatAgentWithMetadata(c, p, t, instanceID, capabilities, MCPClientInfo{})
}
func (r *Remote) HeartbeatAgentWithMetadata(c context.Context, p, t, instanceID string, capabilities []AgentCapability, client MCPClientInfo) error {
	_, err := remoteCall[map[string]bool](c, r, t, rpcRequest{Operation: "agent.heartbeat", Project: p, InstanceID: instanceID, Capabilities: capabilities, ClientInfo: client})
	return err
}
func (r *Remote) EndAgentSession(c context.Context, p, t, instanceID string) error {
	_, err := remoteCall[map[string]bool](c, r, t, rpcRequest{Operation: "agent.end", Project: p, InstanceID: instanceID})
	return err
}
func (r *Remote) ListAgentPresence(c context.Context, p, t string) ([]AgentPresence, error) {
	return remoteCall[[]AgentPresence](c, r, t, rpcRequest{Operation: "agent.presence", Project: p})
}

func (r *Remote) RevokePrincipal(c context.Context, p, t, principalID string) error {
	_, err := remoteCall[map[string]bool](c, r, t, rpcRequest{Operation: "principal.revoke", Project: p, ID: principalID})
	return err
}

func (r *Remote) AddCertificateGrant(c context.Context, p, t, fingerprint string) (CertificateGrant, error) {
	return r.AddCertificateGrantUntil(c, p, t, fingerprint, 0)
}
func (r *Remote) AddCertificateGrantUntil(c context.Context, p, t, fingerprint string, expiresAt int64) (CertificateGrant, error) {
	return remoteCall[CertificateGrant](c, r, t, rpcRequest{Operation: "certificate.add", Project: p, Body: fingerprint, ExpiresAt: expiresAt})
}
func (r *Remote) AddPreparedCertificateInvitation(c context.Context, p, t, id, inviteToken, expectedFingerprint string, expiresAt int64) (CertificateInvitation, error) {
	return remoteCall[CertificateInvitation](c, r, t, rpcRequest{Operation: "certificate.invitation.create", Project: p, ID: id, Body: inviteToken, ExpectedFingerprint: expectedFingerprint, ExpiresAt: expiresAt})
}
func (r *Remote) ListCertificateInvitations(c context.Context, p, t string) ([]CertificateInvitation, error) {
	return remoteCall[[]CertificateInvitation](c, r, t, rpcRequest{Operation: "certificate.invitation.list", Project: p})
}
func (r *Remote) RevokeCertificateInvitation(c context.Context, p, t, id string) error {
	_, err := remoteCall[map[string]bool](c, r, t, rpcRequest{Operation: "certificate.invitation.revoke", Project: p, ID: id})
	return err
}
func (r *Remote) RedeemCertificateInvitation(c context.Context, p, id, inviteToken string) (CertificateGrant, error) {
	return remoteCall[CertificateGrant](c, r, inviteToken, rpcRequest{Operation: "certificate.invitation.redeem", Project: p, ID: id})
}
func (r *Remote) GrantPermission(c context.Context, p, t, principalID, permission string) (PermissionGrant, error) {
	return remoteCall[PermissionGrant](c, r, t, rpcRequest{Operation: "permission.grant", Project: p, ID: principalID, Kind: permission})
}
func (r *Remote) ListPermissions(c context.Context, p, t string) ([]PermissionGrant, error) {
	return remoteCall[[]PermissionGrant](c, r, t, rpcRequest{Operation: "permission.list", Project: p})
}
func (r *Remote) RevokePermission(c context.Context, p, t, principalID, permission string) error {
	_, err := remoteCall[map[string]bool](c, r, t, rpcRequest{Operation: "permission.revoke", Project: p, ID: principalID, Kind: permission})
	return err
}
func (r *Remote) ListCertificateGrants(c context.Context, p, t string) ([]CertificateGrant, error) {
	return remoteCall[[]CertificateGrant](c, r, t, rpcRequest{Operation: "certificate.list", Project: p})
}
func (r *Remote) ListLegacyImports(c context.Context, p, t string) ([]LegacyImport, error) {
	return remoteCall[[]LegacyImport](c, r, t, rpcRequest{Operation: "legacy.list", Project: p})
}
func (r *Remote) ReadLegacyRecords(c context.Context, p, t, importID, recordType, after string, limit int) ([]LegacyRecord, error) {
	return remoteCall[[]LegacyRecord](c, r, t, rpcRequest{Operation: "legacy.read", Project: p, ID: importID, Kind: recordType, Body: after, Limit: limit})
}
func (r *Remote) ReadLegacyRecordChunk(c context.Context, p, t, importID, recordType, recordID string, offset, maxChars int) (LegacyRecordChunk, error) {
	return remoteCall[LegacyRecordChunk](c, r, t, rpcRequest{Operation: "legacy.record", Project: p, ID: importID, Kind: recordType, Recipient: recordID, After: int64(offset), MaxChars: maxChars})
}
func (r *Remote) RevokeCertificateGrant(c context.Context, p, t, fingerprint string) error {
	_, err := remoteCall[map[string]bool](c, r, t, rpcRequest{Operation: "certificate.revoke", Project: p, Body: fingerprint})
	return err
}
func (r *Remote) CertificateAuthorized(c context.Context, p, fingerprint string) (bool, error) {
	if !r.local {
		return false, ErrUnauthorized
	}
	return remoteCall[bool](c, r, "", rpcRequest{Operation: "certificate.authorized", Project: p, Body: fingerprint})
}

func (r *Remote) AssignTask(c context.Context, p, t, id, assignee string) (Task, error) {
	return remoteCall[Task](c, r, t, rpcRequest{Operation: "task.assign", Project: p, ID: id, Recipient: assignee})
}

func (r *Remote) AcknowledgeMessage(c context.Context, p, t, id string) (MessageAcknowledgment, error) {
	return remoteCall[MessageAcknowledgment](c, r, t, rpcRequest{Operation: "message.acknowledge", Project: p, ID: id})
}
func (r *Remote) MessageAcknowledgments(c context.Context, p, t, id string) ([]MessageAcknowledgment, error) {
	return remoteCall[[]MessageAcknowledgment](c, r, t, rpcRequest{Operation: "message.acknowledgments", Project: p, ID: id})
}

func (r *Remote) EventCheckpoint(c context.Context, p, t string) (int64, error) {
	return remoteCall[int64](c, r, t, rpcRequest{Operation: "event.checkpoint", Project: p})
}
func (r *Remote) AcknowledgeEvents(c context.Context, p, t string, seq int64) (int64, error) {
	return remoteCall[int64](c, r, t, rpcRequest{Operation: "event.acknowledge", Project: p, After: seq})
}

func (r *Remote) EventRetentionStatus(c context.Context, p, t string) (EventRetention, error) {
	return remoteCall[EventRetention](c, r, t, rpcRequest{Operation: "event.retention", Project: p})
}

func (r *Remote) PruneProjectEvents(c context.Context, p, t string, through int64) (EventRetention, error) {
	return remoteCall[EventRetention](c, r, t, rpcRequest{Operation: "event.prune", Project: p, PruneThrough: through})
}

func (r *Remote) SaveCandidate(c context.Context, p, t, id string, n int, baseline, body string) (CandidateReport, error) {
	return remoteCall[CandidateReport](c, r, t, rpcRequest{Operation: "candidate.save", Project: p, ID: id, Attempt: n, Baseline: baseline, Body: body})
}
func (r *Remote) ListCandidates(c context.Context, p, t, id string, limit int) ([]CandidateReport, error) {
	return remoteCall[[]CandidateReport](c, r, t, rpcRequest{Operation: "candidate.list", Project: p, ID: id, Limit: limit})
}
func (r *Remote) GetCandidate(c context.Context, p, t, id string) (CandidateReport, error) {
	return remoteCall[CandidateReport](c, r, t, rpcRequest{Operation: "candidate.get", Project: p, ID: id})
}
func (r *Remote) GetCandidatePage(c context.Context, p, t, id string, offset, limit int) (CandidatePage, error) {
	return remoteCall[CandidatePage](c, r, t, rpcRequest{Operation: "candidate.page", Project: p, ID: id, After: int64(offset), Limit: limit})
}

func (r *Remote) SaveCandidateWithID(c context.Context, p, t, id string, n int, baseline, body, requestID string) (CandidateReport, error) {
	return remoteCall[CandidateReport](c, r, t, rpcRequest{Operation: "candidate.save", Project: p, ID: id, Attempt: n, Baseline: baseline, Body: body, RequestID: requestID})
}

func (r *Remote) RecordIntegration(c context.Context, p, t string, submission IntegrationSubmission) (IntegrationRecord, error) {
	submission.Observation.Root = ""
	submission.Observation.CommonDirFingerprint = ""
	submission.Observation.RemoteHints = nil
	return remoteCall[IntegrationRecord](c, r, t, rpcRequest{Operation: "integration.record", Project: p, Integration: &submission})
}

func (r *Remote) ListIntegrations(c context.Context, p, t string, limit int) ([]IntegrationRecord, error) {
	return remoteCall[[]IntegrationRecord](c, r, t, rpcRequest{Operation: "integration.list", Project: p, Limit: limit})
}

func (r *Remote) GetBudget(c context.Context, p, t string) (Budget, error) {
	return remoteCall[Budget](c, r, t, rpcRequest{Operation: "budget.read", Project: p})
}
func (r *Remote) SetBudget(c context.Context, p, t string, revision int, limit int64) (Budget, error) {
	return remoteCall[Budget](c, r, t, rpcRequest{Operation: "budget.set", Project: p, Revision: revision, Amount: limit})
}
func (r *Remote) ReserveBudget(c context.Context, p, t, id string, amount int64) (Budget, error) {
	return remoteCall[Budget](c, r, t, rpcRequest{Operation: "budget.reserve", Project: p, ID: id, Amount: amount})
}

func (r *Remote) ListWorkRoles(c context.Context, p, t string) ([]WorkRole, error) {
	return remoteCall[[]WorkRole](c, r, t, rpcRequest{Operation: "role.list", Project: p})
}
func (r *Remote) SetWorkRole(c context.Context, p, t, principal, task, name, duties string, expires int64, revision int) (WorkRole, error) {
	return remoteCall[WorkRole](c, r, t, rpcRequest{Operation: "role.assign", Project: p, Recipient: principal, ID: task, Name: name, Body: duties, ExpiresAt: expires, Revision: revision})
}

func (r *Remote) WhoAmI(c context.Context, p, t string) (Principal, error) {
	return remoteCall[Principal](c, r, t, rpcRequest{Operation: "identity.read", Project: p})
}
func (r *Remote) CredentialValid(c context.Context, p, t string) (bool, error) {
	result, err := remoteCall[map[string]bool](c, r, t, rpcRequest{Operation: "identity.validate", Project: p})
	return result["valid"], err
}
func (r *Remote) TaskCounts(c context.Context, p, t string) (map[string]int, error) {
	return remoteCall[map[string]int](c, r, t, rpcRequest{Operation: "task.counts", Project: p})
}
func (r *Remote) RecentEvents(c context.Context, p, t string, limit int) ([]Event, error) {
	return remoteCall[[]Event](c, r, t, rpcRequest{Operation: "event.recent", Project: p, Limit: limit})
}
func (r *Remote) RecentMessages(c context.Context, p, t string, limit int) ([]Message, error) {
	return remoteCall[[]Message](c, r, t, rpcRequest{Operation: "message.recent", Project: p, Limit: limit})
}

func (r *Remote) CreatePreparedProject(c context.Context, project, name, owner string, credential Credential) (Project, error) {
	if !r.local {
		return Project{}, ErrUnauthorized
	}
	return remoteCall[Project](c, r, "", rpcRequest{Operation: "project.create_prepared_local", Project: project, Name: name, Description: owner, ID: credential.PrincipalID, Body: credential.Token})
}
func (r *Remote) AddPreparedPrincipal(c context.Context, project, token, name, role string, credential Credential) (Credential, error) {
	_, err := remoteCall[map[string]bool](c, r, token, rpcRequest{Operation: "principal.add_prepared", Project: project, Name: name, Kind: role, ID: credential.PrincipalID, Body: credential.Token})
	return credential, err
}
func (r *Remote) AddPreparedRemotePrincipal(c context.Context, project, token, name, role string, credential Credential, fingerprint string, expiresAt int64) (Credential, error) {
	_, err := remoteCall[map[string]bool](c, r, token, rpcRequest{Operation: "principal.add_remote_prepared", Project: project, Name: name, Kind: role, ID: credential.PrincipalID, Body: credential.Token, Fingerprint: fingerprint, ExpiresAt: expiresAt})
	if err != nil {
		return Credential{}, err
	}
	return credential, nil
}
