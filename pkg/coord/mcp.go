package coord

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Backend is the project-scoped coordination interface used by MCP adapters.
type Backend interface {
	CredentialValid(context.Context, string, string) (bool, error)
	ListPrincipals(context.Context, string, string) ([]Principal, error)
	HeartbeatAgent(context.Context, string, string, string) error
	HeartbeatAgentWithCapabilities(context.Context, string, string, string, []AgentCapability) error
	HeartbeatAgentWithMetadata(context.Context, string, string, string, []AgentCapability, MCPClientInfo) error
	EndAgentSession(context.Context, string, string, string) error
	ListAgentPresence(context.Context, string, string) ([]AgentPresence, error)
	RevokePrincipal(context.Context, string, string, string) error
	AddCertificateGrant(context.Context, string, string, string) (CertificateGrant, error)
	AddCertificateGrantUntil(context.Context, string, string, string, int64) (CertificateGrant, error)
	AddPreparedCertificateInvitation(context.Context, string, string, string, string, string, int64) (CertificateInvitation, error)
	ListCertificateInvitations(context.Context, string, string) ([]CertificateInvitation, error)
	RevokeCertificateInvitation(context.Context, string, string, string) error
	GrantPermission(context.Context, string, string, string, string) (PermissionGrant, error)
	ListPermissions(context.Context, string, string) ([]PermissionGrant, error)
	RevokePermission(context.Context, string, string, string, string) error
	ListCertificateGrants(context.Context, string, string) ([]CertificateGrant, error)
	RevokeCertificateGrant(context.Context, string, string, string) error
	CertificateAuthorized(context.Context, string, string) (bool, error)
	ListWorkRoles(context.Context, string, string) ([]WorkRole, error)
	SetWorkRole(context.Context, string, string, string, string, string, string, int64, int) (WorkRole, error)
	GetSettings(context.Context, string, string) (Settings, error)
	GetBudget(context.Context, string, string) (Budget, error)
	RecordAttemptUsage(context.Context, string, string, string, int, string, UsageReport) (UsageReport, error)
	SetBudget(context.Context, string, string, int, int64) (Budget, error)
	ReserveBudget(context.Context, string, string, string, int64) (Budget, error)
	UpdateSettings(context.Context, string, string, Settings) (Settings, error)
	CurrentPlan(context.Context, string, string) (Plan, error)
	PublishPlan(context.Context, string, string, string, int) (int, error)
	AcknowledgePlan(context.Context, string, string, int) error
	CreateRepository(context.Context, string, string, string) (Repository, error)
	AddRepositoryRemoteHint(context.Context, string, string, string, string) (Repository, error)
	RemoveRepositoryRemoteHint(context.Context, string, string, string, string) error
	ListRepositories(context.Context, string, string) ([]Repository, error)
	FindRepositoriesByRemote(context.Context, string, string, string) ([]Repository, error)
	RegisterWorkspace(context.Context, string, string, string, string, string, string, string, string, bool) (Workspace, error)
	UpdateWorkspace(context.Context, string, string, string, string, string, bool) (Workspace, error)
	ListWorkspaces(context.Context, string, string) ([]Workspace, error)
	GetAttempt(context.Context, string, string, string, int) (Attempt, error)
	CreateTask(context.Context, string, string, int, string, string, string, ReportContract, []string) (Task, error)
	CreateTaskWithID(context.Context, string, string, string, int, string, string, string, ReportContract, []string) (Task, error)
	RequestReview(context.Context, string, string, string, int) error
	ListTasks(context.Context, string, string, int) ([]Task, error)
	SearchTasks(context.Context, string, string, TaskFilter) (TaskPage, error)
	TaskCounts(context.Context, string, string) (map[string]int, error)
	GetTask(context.Context, string, string, string) (Task, error)
	AssignTask(context.Context, string, string, string, string) (Task, error)
	TransitionTask(context.Context, string, string, string, string, string, int) (Task, error)
	ClaimTaskInWorkspace(context.Context, string, string, string, string, time.Duration) (Task, error)
	ClaimObservedWorkspace(context.Context, string, string, string, string, GitObservation, time.Duration) (Task, error)
	RenewLease(context.Context, string, string, string, int, time.Duration) (Task, error)
	ReconcileTask(context.Context, string, string, string, int) (Task, error)
	SubmitReport(context.Context, string, string, string, int, string, string) (Report, error)
	SaveCandidate(context.Context, string, string, string, int, string, string) (CandidateReport, error)
	SaveCandidateWithID(context.Context, string, string, string, int, string, string, string) (CandidateReport, error)
	ListCandidates(context.Context, string, string, string, int) ([]CandidateReport, error)
	GetCandidate(context.Context, string, string, string) (CandidateReport, error)
	GetCandidatePage(context.Context, string, string, string, int, int) (CandidatePage, error)
	RecordIntegration(context.Context, string, string, IntegrationSubmission) (IntegrationRecord, error)
	ListIntegrations(context.Context, string, string, int) ([]IntegrationRecord, error)
	AcceptReport(context.Context, string, string, string, string) error
	RequestRework(context.Context, string, string, string, string, string) error
	GetReport(context.Context, string, string, string) (Report, error)
	GetReportPage(context.Context, string, string, string, int, int) (ReportPage, error)
	SendMessage(context.Context, string, string, string, string, string, string) (Message, error)
	SendMessageWithID(context.Context, string, string, string, string, string, string, string) (Message, error)
	ListMessages(context.Context, string, string, int64, int) ([]Message, error)
	AcknowledgeMessage(context.Context, string, string, string) (MessageAcknowledgment, error)
	MessageAcknowledgments(context.Context, string, string, string) ([]MessageAcknowledgment, error)
	ReadEvents(context.Context, string, string, int64, int) ([]Event, error)
	SyncProject(context.Context, string, string, int64, int) (ProjectSyncFrame, error)
	ListProjectReplicas(context.Context, string, string) ([]ProjectReplica, error)
	EventCheckpoint(context.Context, string, string) (int64, error)
	AcknowledgeEvents(context.Context, string, string, int64) (int64, error)
	PruneProjectEvents(context.Context, string, string, int64) (EventRetention, error)
	ListLegacyImports(context.Context, string, string) ([]LegacyImport, error)
	ReadLegacyRecords(context.Context, string, string, string, string, string, int) ([]LegacyRecord, error)
	ReadLegacyRecordChunk(context.Context, string, string, string, string, string, int, int) (LegacyRecordChunk, error)
}

// MCPServer binds one project's credential to an MCP adapter. Credentials are
// never accepted as tool arguments or returned in tool results.
func MCPServer(store Backend, project, token string) *server.MCPServer {
	return mcpServer(store, project, token, nil, nil, nil)
}

// ProjectBootstrapMCPServer exposes only project creation. Its process must be
// launched with the private bootstrap API key configured by the MCP client;
// created project actions use the separately saved owner credential.
func ProjectBootstrapMCPServer(store interface {
	CreatePreparedProjectWithWorkspaceUsingBootstrapKey(context.Context, string, string, string, string, string, string, GitObservation, Credential) (Project, Credential, Repository, Workspace, error)
}, credentialDir, bootstrapKey string) *server.MCPServer {
	srv := server.NewMCPServer("Gossip project bootstrap", "0.1.0", server.WithToolCapabilities(true))
	srv.AddTool(mcp.NewTool("gossip_create_project",
		mcp.WithDescription("Create a Gossip project from a local Git checkout. The project name defaults to the inferred repository name. Supply a stable request_id and reuse it after an uncertain result; the owner credential is durably saved before creation and never returned in the tool result."),
		mcp.WithString("local_path", mcp.Required(), mcp.Description("Absolute or relative path to a Git checkout on this MCP server host")),
		mcp.WithString("request_id", mcp.Required(), mcp.Description("Stable unique identifier; reuse it to safely retry this creation")),
		mcp.WithString("name", mcp.Description("Optional project name; inferred from the Git remote or folder when omitted")),
		mcp.WithString("credential_file", mcp.Description("Optional private credential file from a previous attempt; normally request_id finds it automatically"))),
		func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := request.GetArguments()
			path, _ := args["local_path"].(string)
			observation, err := InspectGitWorkspace(ctx, path)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			repoName := filepath.Base(observation.Root)
			if len(observation.RemoteHints) > 0 {
				parts := strings.Split(observation.RemoteHints[0], "/")
				if len(parts) > 0 && parts[len(parts)-1] != "" {
					repoName = parts[len(parts)-1]
				}
			}
			name, _ := args["name"].(string)
			if strings.TrimSpace(name) == "" {
				name = repoName
			}
			if err = os.MkdirAll(credentialDir, 0o700); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("cannot prepare private credential directory: %v", err)), nil
			}
			dirInfo, statErr := os.Lstat(credentialDir)
			if statErr != nil || !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && dirInfo.Mode().Perm()&0o077 != 0) {
				return mcp.NewToolResultError("credential directory must be a real private directory with permissions 0700 or stricter"), nil
			}
			owner := "MCP owner"
			if current, e := user.Current(); e == nil && current.Username != "" {
				owner = current.Username
			}
			node, _ := os.Hostname()
			credentialPathInput, _ := args["credential_file"].(string)
			requestID, _ := args["request_id"].(string)
			projectID, credential, credentialPath, err := prepareBootstrapCredential(credentialDir, requestID, credentialPathInput)
			if err != nil {
				if credentialPath != "" {
					return mcp.NewToolResultError(fmt.Sprintf("owner credential is retained at %s but could not be fully synchronized: %v", credentialPath, err)), nil
				}
				return mcp.NewToolResultError(err.Error()), nil
			}
			project, credential, repository, workspace, err := store.CreatePreparedProjectWithWorkspaceUsingBootstrapKey(ctx, bootstrapKey, projectID, name, owner, repoName, node, observation, credential)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("project creation failed or its outcome is uncertain: %v. The owner credential is retained at %s. Retry the same checkout and project name using credential_file with that path.", err, credentialPath)), nil
			}
			clientCertificateGranted := false
			if remote, ok := store.(*Remote); ok && !remote.local && remote.bootstrapKey != "" {
				clientCertificateGranted = true
			}
			result, err := json.Marshal(map[string]any{"project": project, "repository": repository, "workspace": workspace, "owner_credential_file": credentialPath, "client_certificate_granted": clientCertificateGranted, "next_step": "Configure a separate coord-mcp server for this project with --credential-file set to owner_credential_file and the same home/TLS connection settings. This create-only bootstrap server cannot read project data."})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(result)), nil
		})
	return srv
}

type bootstrapCredentialFile struct {
	ProjectID   string `json:"project_id"`
	PrincipalID string `json:"principal_id"`
	Token       string `json:"token"`
}

func prepareBootstrapCredential(directory, requestID, requestedPath string) (string, Credential, string, error) {
	var credential Credential
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || len(requestID) > 256 {
		return "", credential, "", errors.New("request_id is required and must be at most 256 bytes")
	}
	absoluteDir, err := filepath.Abs(directory)
	if err != nil {
		return "", credential, "", err
	}
	if requestedPath != "" {
		path := filepath.Clean(requestedPath)
		if filepath.Dir(path) != absoluteDir || filepath.Base(path) != bootstrapCredentialFilename(requestID) {
			return "", credential, "", errors.New("credential_file does not match this request_id and configured credential directory")
		}
		info, err := os.Lstat(path)
		if err != nil {
			return "", credential, "", err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
			return "", credential, "", errors.New("credential_file must be a private regular file")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", credential, "", err
		}
		var saved bootstrapCredentialFile
		if err = json.Unmarshal(data, &saved); err != nil {
			return "", credential, "", errors.New("credential_file is not a valid Gossip credential")
		}
		credential = Credential{PrincipalID: saved.PrincipalID, Token: saved.Token}
		if !validPrepared(saved.ProjectID, credential) {
			return "", Credential{}, "", errors.New("credential_file does not contain a prepared project owner credential")
		}
		return saved.ProjectID, credential, path, nil
	}
	path := filepath.Join(absoluteDir, bootstrapCredentialFilename(requestID))
	if _, statErr := os.Lstat(path); statErr == nil {
		return prepareBootstrapCredential(directory, requestID, path)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", Credential{}, "", statErr
	}
	projectID, credential, err := PrepareCredential("")
	if err != nil {
		return "", Credential{}, "", err
	}
	data, err := json.MarshalIndent(bootstrapCredentialFile{ProjectID: projectID, PrincipalID: credential.PrincipalID, Token: credential.Token}, "", "  ")
	if err != nil {
		return "", Credential{}, "", err
	}
	data = append(data, '\n')
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", Credential{}, "", err
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return "", Credential{}, "", errors.New("could not persist the private owner credential")
	}
	dir, err := os.Open(absoluteDir)
	if err != nil {
		return projectID, credential, path, err
	}
	syncErr = dir.Sync()
	closeErr = dir.Close()
	if syncErr != nil {
		return projectID, credential, path, syncErr
	}
	if closeErr != nil {
		return projectID, credential, path, closeErr
	}
	return projectID, credential, path, nil
}

func bootstrapCredentialFilename(requestID string) string {
	digest := sha256.Sum256([]byte(requestID))
	return "bootstrap-" + hex.EncodeToString(digest[:16]) + ".json"
}

func mcpServer(store Backend, project, token string, keeper *leaseKeeper, outbox *Outbox, onInitialize func(mcp.Implementation)) *server.MCPServer {
	options := []server.ServerOption{server.WithToolCapabilities(true)}
	if onInitialize != nil {
		hooks := &server.Hooks{}
		hooks.AddAfterInitialize(func(_ context.Context, _ any, request *mcp.InitializeRequest, _ *mcp.InitializeResult) {
			onInitialize(request.Params.ClientInfo)
		})
		options = append(options, server.WithHooks(hooks))
	}
	srv := server.NewMCPServer("Gossip coordination", "0.1.0", options...)
	result := func(v any, err error) (*mcp.CallToolResult, error) {
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		b, e := json.Marshal(v)
		if e != nil {
			return mcp.NewToolResultError(e.Error()), nil
		}
		return mcp.NewToolResultText(string(b)), nil
	}
	args := func(r mcp.CallToolRequest) map[string]any {
		if a, ok := r.Params.Arguments.(map[string]any); ok {
			return a
		}
		return map[string]any{}
	}
	str := func(a map[string]any, k string) string { v, _ := a[k].(string); return v }
	num := func(a map[string]any, k string, defaultValue int) int {
		switch v := a[k].(type) {
		case float64:
			return int(v)
		case int:
			return v
		case json.Number:
			n, _ := strconv.Atoi(string(v))
			return n
		}
		return defaultValue
	}
	millis := func(a map[string]any, k string) (int64, error) {
		switch v := a[k].(type) {
		case nil:
			return 0, nil
		case float64:
			if math.IsNaN(v) || math.IsInf(v, 0) || math.Trunc(v) != v || v < 0 || v >= float64(uint64(1)<<53) {
				return 0, fmt.Errorf("%s must be a nonnegative integer timestamp", k)
			}
			return int64(v), nil
		case int64:
			return v, nil
		case int:
			return int64(v), nil
		case json.Number:
			n, err := strconv.ParseInt(string(v), 10, 64)
			return n, err
		}
		return 0, fmt.Errorf("%s must be an integer timestamp", k)
	}
	list := func(a map[string]any, k string) []string {
		var out []string
		switch v := a[k].(type) {
		case []string:
			return v
		case []any:
			for _, x := range v {
				if s, ok := x.(string); ok {
					out = append(out, s)
				}
			}
		}
		return out
	}
	srv.AddTool(mcp.NewTool("gossip_workspace", mcp.WithDescription("Register and inspect project repositories/workspaces. Owners can add sanitized Git remote hints for suggestions; hints never grant membership."),
		mcp.WithString("operation", mcp.Required(), mcp.Description("create_repository, list_repositories, add_remote_hint, remove_remote_hint, inspect_git, recognize_git, register_git, register, update, list, or attempt")),
		mcp.WithString("repository_id"), mcp.WithString("remote_url"), mcp.WithString("workspace_id"), mcp.WithString("name"), mcp.WithString("node_label"),
		mcp.WithString("local_path"), mcp.WithString("branch"), mcp.WithString("head"), mcp.WithBoolean("dirty"),
		mcp.WithString("task_id"), mcp.WithInteger("attempt")),
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			a := args(r)
			switch str(a, "operation") {
			case "inspect_git", "recognize_git", "register_git":
				// Inspect on the adapter host, never on the remote home node.
				if _, err := store.ListRepositories(ctx, project, token); err != nil {
					return result(nil, err)
				}
				observation, err := InspectGitWorkspace(ctx, str(a, "local_path"))
				if err != nil {
					return result(nil, err)
				}
				if str(a, "operation") == "inspect_git" {
					return result(observation, nil)
				}
				if str(a, "operation") == "recognize_git" {
					matches := []Repository{}
					seen := make(map[string]bool)
					for _, hint := range observation.RemoteHints {
						found, err := store.FindRepositoriesByRemote(ctx, project, token, "https://"+hint)
						if err != nil {
							return result(nil, err)
						}
						for _, repo := range found {
							if !seen[repo.ID] {
								seen[repo.ID] = true
								matches = append(matches, repo)
							}
						}
					}
					return result(struct {
						Observation  GitObservation `json:"observation"`
						Repositories []Repository   `json:"repositories"`
					}{observation, matches}, nil)
				}
				v, err := store.RegisterWorkspace(ctx, project, token, str(a, "repository_id"), str(a, "name"), str(a, "node_label"), observation.Root, observation.Branch, observation.Head, observation.Dirty)
				return result(v, err)
			case "create_repository":
				v, err := store.CreateRepository(ctx, project, token, str(a, "name"))
				return result(v, err)
			case "list_repositories":
				v, err := store.ListRepositories(ctx, project, token)
				return result(v, err)
			case "add_remote_hint":
				v, err := store.AddRepositoryRemoteHint(ctx, project, token, str(a, "repository_id"), str(a, "remote_url"))
				return result(v, err)
			case "remove_remote_hint":
				err := store.RemoveRepositoryRemoteHint(ctx, project, token, str(a, "repository_id"), str(a, "remote_url"))
				return result(map[string]bool{"removed": err == nil}, err)
			case "register":
				dirty, _ := a["dirty"].(bool)
				v, err := store.RegisterWorkspace(ctx, project, token, str(a, "repository_id"), str(a, "name"), str(a, "node_label"), str(a, "local_path"), str(a, "branch"), str(a, "head"), dirty)
				return result(v, err)
			case "list":
				v, err := store.ListWorkspaces(ctx, project, token)
				return result(v, err)
			case "update":
				dirty, _ := a["dirty"].(bool)
				v, err := store.UpdateWorkspace(ctx, project, token, str(a, "workspace_id"), str(a, "branch"), str(a, "head"), dirty)
				return result(v, err)
			case "attempt":
				v, err := store.GetAttempt(ctx, project, token, str(a, "task_id"), num(a, "attempt", 0))
				return result(v, err)
			default:
				return result(nil, fmt.Errorf("unknown workspace operation"))
			}
		})
	srv.AddTool(mcp.NewTool("gossip_integration", mcp.WithDescription("Record or queue a clean combined Git revision and checks for an active integration task. The adapter inspects the local checkout; Gossip stores candidate inputs and host-reported checks but does not merge source or execute commands. Queueing uses the configured persistent outbox and retries safely across disconnects."),
		mcp.WithString("operation", mcp.Description("record, enqueue, list, outbox, retry, discard, or prune_delivered")), mcp.WithString("task_id"), mcp.WithInteger("attempt"), mcp.WithString("workspace_id"), mcp.WithString("local_path"), mcp.WithString("base_revision"), mcp.WithString("combined_revision"), mcp.WithArray("candidate_ids", mcp.WithStringItems(mcp.MinLength(1), mcp.MaxLength(128))), mcp.WithString("checks_json", mcp.Description("JSON array of checks; each revision must equal combined_revision")), mcp.WithString("summary"), mcp.WithString("request_id"), mcp.WithInteger("limit")),
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			a := args(r)
			switch op := str(a, "operation"); op {
			case "list":
				limit := num(a, "limit", 50)
				if limit == 0 {
					limit = 50
				}
				v, err := store.ListIntegrations(ctx, project, token, limit)
				return result(v, err)
			case "outbox", "retry", "discard", "prune_delivered":
				if outbox == nil {
					return result(nil, fmt.Errorf("outbox is not configured"))
				}
				switch op {
				case "outbox":
					limit := num(a, "limit", 50)
					if limit == 0 {
						limit = 50
					}
					v, err := outbox.ListIntegrationQueue(ctx, limit)
					return result(v, err)
				case "retry":
					err := outbox.RetryIntegration(ctx, str(a, "request_id"))
					return result(map[string]bool{"queued": err == nil}, err)
				case "discard":
					err := outbox.DiscardIntegration(ctx, str(a, "request_id"))
					return result(map[string]bool{"discarded": err == nil}, err)
				default:
					n, err := outbox.PruneIntegrations(ctx)
					return result(map[string]int64{"removed": n}, err)
				}
			case "", "record", "enqueue":
				if op == "enqueue" && outbox == nil {
					return result(nil, fmt.Errorf("outbox is not configured"))
				}
				var checks []IntegrationCheck
				if err := json.Unmarshal([]byte(str(a, "checks_json")), &checks); err != nil {
					return result(nil, fmt.Errorf("checks_json must be a JSON array: %w", err))
				}
				observation, err := InspectGitWorkspace(ctx, str(a, "local_path"))
				if err != nil {
					return result(nil, err)
				}
				observedRoot := observation.Root
				// File paths and machine-local Git identifiers stay on this agent host.
				observation = GitObservation{Branch: observation.Branch, Head: observation.Head, Dirty: observation.Dirty}
				submission := IntegrationSubmission{TaskID: str(a, "task_id"), Attempt: num(a, "attempt", 0), WorkspaceID: str(a, "workspace_id"), BaseRevision: str(a, "base_revision"), CombinedRevision: str(a, "combined_revision"), Observation: observation, CandidateIDs: list(a, "candidate_ids"), Checks: checks, Summary: str(a, "summary"), RequestID: str(a, "request_id")}
				if op == "enqueue" {
					v, err := outbox.EnqueueIntegration(ctx, submission)
					return result(v, err)
				}
				workspaces, err := store.ListWorkspaces(ctx, project, token)
				if err != nil {
					return result(nil, err)
				}
				matched := false
				for _, workspace := range workspaces {
					if workspace.ID == submission.WorkspaceID && workspace.LocalPath != "" && filepath.Clean(workspace.LocalPath) == filepath.Clean(observedRoot) {
						matched = true
						break
					}
				}
				if !matched {
					return result(nil, fmt.Errorf("local path does not match this agent's registered workspace"))
				}
				v, err := store.RecordIntegration(ctx, project, token, submission)
				return result(v, err)
			default:
				return result(nil, fmt.Errorf("unknown integration operation"))
			}
		})
	srv.AddTool(mcp.NewTool("gossip_join", mcp.WithDescription("Read the current project plan using the adapter's bound identity.")),
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			p, err := store.CurrentPlan(ctx, project, token)
			return result(p, err)
		})
	srv.AddTool(mcp.NewTool("gossip_agents", mcp.WithDescription("List enrolled project principals, or revoke a principal's project credential. Membership changes require owner authority or a project-scoped membership.manage grant; delegated managers can enroll or revoke workers and reviewers, but cannot create owners or grant TLS certificate access."),
		mcp.WithString("operation", mcp.Description("list (default) or revoke")), mcp.WithString("principal_id", mcp.Description("Principal to revoke"))),
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			a := args(r)
			if str(a, "operation") == "revoke" {
				err := store.RevokePrincipal(ctx, project, token, str(a, "principal_id"))
				return result(map[string]bool{"revoked": err == nil}, err)
			}
			if op := str(a, "operation"); op != "" && op != "list" {
				return result(nil, fmt.Errorf("unknown agent operation"))
			}
			v, err := store.ListPrincipals(ctx, project, token)
			return result(v, err)
		})
	srv.AddTool(mcp.NewTool("gossip_access", mcp.WithDescription("List, grant, or revoke client certificate access to this project. Granting and revoking require the owner role; members still need a project credential."),
		mcp.WithString("operation", mcp.Required(), mcp.Description("list, grant, or revoke")), mcp.WithString("fingerprint", mcp.Description("SHA-256 fingerprint of the client certificate")), mcp.WithInteger("expires_at", mcp.Description("Optional future Unix milliseconds; zero grants indefinitely"))),
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			a := args(r)
			switch str(a, "operation") {
			case "list":
				v, err := store.ListCertificateGrants(ctx, project, token)
				return result(v, err)
			case "grant":
				expiresAt, err := millis(a, "expires_at")
				if err != nil {
					return result(nil, err)
				}
				v, err := store.AddCertificateGrantUntil(ctx, project, token, str(a, "fingerprint"), expiresAt)
				return result(v, err)
			case "revoke":
				err := store.RevokeCertificateGrant(ctx, project, token, str(a, "fingerprint"))
				return result(map[string]bool{"revoked": err == nil}, err)
			default:
				return result(nil, fmt.Errorf("unknown access operation"))
			}
		})
	srv.AddTool(mcp.NewTool("gossip_permissions", mcp.WithDescription("List project-scoped action grants or let an owner grant/revoke project.read, message.send, task.claim, task.delegate, result.submit, review.decide, plan.edit, settings.edit, integration, or membership.manage. Worker and reviewer enrollment creates a one-time starter grant set; later work-role changes never change permissions."),
		mcp.WithString("operation", mcp.Description("list (default), grant, or revoke")), mcp.WithString("principal_id"), mcp.WithString("permission", mcp.Description("project.read, message.send, task.claim, task.delegate, result.submit, review.decide, plan.edit, settings.edit, integration, or membership.manage"))),
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			a := args(r)
			switch str(a, "operation") {
			case "", "list":
				v, err := store.ListPermissions(ctx, project, token)
				return result(v, err)
			case "grant":
				v, err := store.GrantPermission(ctx, project, token, str(a, "principal_id"), str(a, "permission"))
				return result(v, err)
			case "revoke":
				err := store.RevokePermission(ctx, project, token, str(a, "principal_id"), str(a, "permission"))
				return result(map[string]bool{"revoked": err == nil}, err)
			default:
				return result(nil, fmt.Errorf("unknown permission operation"))
			}
		})
	srv.AddTool(mcp.NewTool("gossip_history", mcp.WithDescription("Browse imported legacy project history. Historical identities and roles do not grant permissions or reactivate tasks."),
		mcp.WithString("operation", mcp.Required(), mcp.Description("imports or records")),
		mcp.WithString("import_id", mcp.Description("Import ID required for records")),
		mcp.WithString("record_type", mcp.Description("goal, actor, task, message, workflow, sop_artifact, or artifact_file")),
		mcp.WithString("record_id", mcp.Description("Record ID required for record operation")),
		mcp.WithString("after", mcp.Description("Record ID cursor")), mcp.WithInteger("limit", mcp.Description("Page size up to 100")), mcp.WithInteger("offset", mcp.Description("Character offset for record content")), mcp.WithInteger("max_chars", mcp.Description("Record content chunk size up to 262144"))),
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			a := args(r)
			switch str(a, "operation") {
			case "imports":
				v, err := store.ListLegacyImports(ctx, project, token)
				return result(v, err)
			case "records":
				v, err := store.ReadLegacyRecords(ctx, project, token, str(a, "import_id"), str(a, "record_type"), str(a, "after"), num(a, "limit", 50))
				return result(v, err)
			case "record":
				v, err := store.ReadLegacyRecordChunk(ctx, project, token, str(a, "import_id"), str(a, "record_type"), str(a, "record_id"), num(a, "offset", 0), num(a, "max_chars", 16384))
				return result(v, err)
			default:
				return result(nil, fmt.Errorf("unknown history operation"))
			}
		})
	srv.AddTool(mcp.NewTool("gossip_roles", mcp.WithDescription("List role presets and assignments, or assign work duties. Work roles never grant permissions."),
		mcp.WithString("operation", mcp.Required(), mcp.Description("presets, list, or assign")), mcp.WithString("principal_id"), mcp.WithString("task_id", mcp.Description("Optional task scope")), mcp.WithString("name", mcp.Description("Preset or custom name; empty clears")), mcp.WithString("duties", mcp.Description("Custom duties or override")), mcp.WithInteger("expires_at", mcp.Description("Optional Unix milliseconds")), mcp.WithInteger("revision", mcp.Description("Expected assignment revision; zero for new scope"))),
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			a := args(r)
			switch str(a, "operation") {
			case "presets":
				return result(WorkRolePresets(), nil)
			case "list":
				v, e := store.ListWorkRoles(ctx, project, token)
				return result(v, e)
			case "assign":
				v, e := store.SetWorkRole(ctx, project, token, str(a, "principal_id"), str(a, "task_id"), str(a, "name"), str(a, "duties"), int64(num(a, "expires_at", 0)), num(a, "revision", 0))
				return result(v, e)
			default:
				return result(nil, fmt.Errorf("unknown role operation"))
			}
		})
	srv.AddTool(mcp.NewTool("gossip_settings", mcp.WithDescription("Read or update project coordination settings. Updating requires owner authority or a project-scoped settings.edit grant, plus the current settings revision."),
		mcp.WithString("operation", mcp.Required(), mcp.Description("read or update")),
		mcp.WithInteger("revision"), mcp.WithInteger("max_parallel"), mcp.WithInteger("max_lease_seconds"), mcp.WithInteger("summary_token_target"), mcp.WithString("default_review_mode")),
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			a := args(r)
			switch str(a, "operation") {
			case "read":
				v, err := store.GetSettings(ctx, project, token)
				return result(v, err)
			case "update":
				v, err := store.UpdateSettings(ctx, project, token, Settings{Revision: num(a, "revision", 0), MaxParallel: num(a, "max_parallel", 0), MaxLeaseSeconds: num(a, "max_lease_seconds", 0), SummaryTokenTarget: num(a, "summary_token_target", 0), DefaultReviewMode: str(a, "default_review_mode")})
				return result(v, err)
			default:
				return result(nil, fmt.Errorf("unknown settings operation"))
			}
		})
	srv.AddTool(mcp.NewTool("gossip_budget", mcp.WithDescription("Read or manage project token reservations. Usage is not measured or enforced by connected hosts."),
		mcp.WithString("operation", mcp.Required(), mcp.Description("read, set, reserve, or release")), mcp.WithInteger("revision"), mcp.WithInteger("token_limit"), mcp.WithInteger("amount"), mcp.WithString("task_id")),
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			a := args(r)
			switch str(a, "operation") {
			case "read":
				v, e := store.GetBudget(ctx, project, token)
				return result(v, e)
			case "set":
				v, e := store.SetBudget(ctx, project, token, num(a, "revision", 0), int64(num(a, "token_limit", 0)))
				return result(v, e)
			case "reserve":
				if num(a, "amount", 0) <= 0 {
					return result(nil, fmt.Errorf("reservation must be positive; use release to free it"))
				}
				v, e := store.ReserveBudget(ctx, project, token, str(a, "task_id"), int64(num(a, "amount", 0)))
				return result(v, e)
			case "release":
				v, e := store.ReserveBudget(ctx, project, token, str(a, "task_id"), 0)
				return result(v, e)
			default:
				return result(nil, fmt.Errorf("unknown budget operation"))
			}
		})
	srv.AddTool(mcp.NewTool("gossip_plan", mcp.WithDescription("Read or publish a versioned project plan. Publishing requires owner authority or a project-scoped plan.edit grant."),
		mcp.WithString("operation", mcp.Required(), mcp.Description("read, cached, publish, or acknowledge")),
		mcp.WithString("body", mcp.Description("Plan text for publish")),
		mcp.WithInteger("expected_revision", mcp.Description("Current revision; zero for the first plan"))),
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			a := args(r)
			switch str(a, "operation") {
			case "read":
				p, err := store.CurrentPlan(ctx, project, token)
				if err == nil && outbox != nil {
					_ = outbox.cacheSnapshot(ctx, "plan", p)
				}
				return result(p, err)
			case "cached":
				if outbox == nil {
					return result(nil, fmt.Errorf("offline cache is not configured"))
				}
				snapshot, err := outbox.readSnapshot(ctx, "plan")
				return result(snapshot, err)
			case "publish":
				n, err := store.PublishPlan(ctx, project, token, str(a, "body"), num(a, "expected_revision", 0))
				return result(map[string]int{"revision": n}, err)
			case "acknowledge":
				err := store.AcknowledgePlan(ctx, project, token, num(a, "expected_revision", 0))
				return result(map[string]bool{"acknowledged": err == nil}, err)
			default:
				return result(nil, fmt.Errorf("unknown plan operation"))
			}
		})
	srv.AddTool(mcp.NewTool("gossip_task", mcp.WithDescription("Create, inspect, claim, renew, request orchestrator review, manage queued review requests, or reconcile a project task."),
		mcp.WithString("operation", mcp.Required(), mcp.Description("create, list, cached, search, get, assign, claim, renew, request_review, review_outbox, retry_review, discard_review, prune_reviews, block, resolve, cancel, or reconcile")),
		mcp.WithString("task_id", mcp.Description("Task ID")),
		mcp.WithString("assigned_to", mcp.Description("Project principal ID; empty clears assignment")),
		mcp.WithString("status", mcp.Description("Status filter for search")),
		mcp.WithString("cursor", mcp.Description("Next-page cursor from search")),
		mcp.WithString("query", mcp.Description("Case-insensitive search across task IDs, titles, and descriptions (up to 256 characters)")),
		mcp.WithString("reason", mcp.Description("Reason for blocking or cancellation")),
		mcp.WithInteger("plan_revision", mcp.Description("Current plan revision")),
		mcp.WithString("title", mcp.Description("Task title")),
		mcp.WithString("description", mcp.Description("Task scope and acceptance criteria")),
		mcp.WithString("kind", mcp.Description("research, design, implementation, review, verification, or integration")),
		mcp.WithArray("questions", mcp.Description("Research questions")),
		mcp.WithArray("formats", mcp.Description("Requested report formats")),
		mcp.WithArray("coverage", mcp.Description("Requested coverage")),
		mcp.WithArray("dependencies", mcp.Description("Prerequisite task IDs")),
		mcp.WithString("required_role", mcp.Description("Optional active project or task role assignment required to claim this task; choose during planning")),
		mcp.WithArray("required_capabilities", mcp.Description("Optional configured adapter/host capabilities required at claim time; candidates must have a recent authenticated session reporting all values")),
		mcp.WithString("review_mode", mcp.Description("summary, report_first, targeted_source, or full_source")),
		mcp.WithString("review_type", mcp.Description("Ask the user during planning if unspecified; choose full_code or diagram_report and record it in the contract")),
		mcp.WithInteger("summary_token_target", mcp.Description("Advisory summary target")),
		mcp.WithInteger("attempt", mcp.Description("Current fencing number for renewal")),
		mcp.WithString("request_id", mcp.Description("Required stable ID for create retries; otherwise an ID in review/candidate/integration outboxes")),
		mcp.WithString("workspace_id", mcp.Description("Caller-owned workspace to snapshot when claiming")),
		mcp.WithBoolean("refresh_git", mcp.Description("Refresh the selected Git workspace before claiming (default true); set false for a non-Git workspace")),
		mcp.WithInteger("lease_seconds", mcp.Description("Claim or renewal lease, 1 to 3600 seconds")),
		mcp.WithBoolean("auto_renew", mcp.Description("Renew this claim while the MCP adapter runs (default true); set false for a fixed lease")),
		mcp.WithInteger("limit", mcp.Description("Maximum list results, 1 to 200"))),
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			a := args(r)
			id := str(a, "task_id")
			rev := num(a, "plan_revision", 0)
			switch str(a, "operation") {
			case "create":
				c := ReportContract{Questions: list(a, "questions"), Formats: list(a, "formats"), Coverage: list(a, "coverage"), RequiredRole: str(a, "required_role"), RequiredCapabilities: list(a, "required_capabilities"), ReviewMode: str(a, "review_mode"), ReviewType: str(a, "review_type"), SummaryTokenTarget: num(a, "summary_token_target", 0)}
				if strings.TrimSpace(c.ReviewType) == "diagram_report" && len(c.Formats) == 0 {
					c.Formats = []string{"markdown", "mermaid_sequence", "mermaid_class"}
				}
				t, err := store.CreateTaskWithID(ctx, project, token, str(a, "request_id"), rev, str(a, "title"), str(a, "description"), str(a, "kind"), c, list(a, "dependencies"))
				return result(t, err)
			case "list":
				t, err := store.ListTasks(ctx, project, token, num(a, "limit", 50))
				if err == nil && outbox != nil {
					_ = outbox.cacheSnapshot(ctx, "tasks", t)
				}
				return result(t, err)
			case "cached":
				if outbox == nil {
					return result(nil, fmt.Errorf("offline cache is not configured"))
				}
				snapshot, err := outbox.readSnapshot(ctx, "tasks")
				return result(snapshot, err)
			case "search":
				page, err := store.SearchTasks(ctx, project, token, TaskFilter{Status: str(a, "status"), Kind: str(a, "kind"), Assignee: str(a, "assigned_to"), Query: str(a, "query"), Cursor: str(a, "cursor"), Limit: num(a, "limit", 50)})
				return result(page, err)
			case "get":
				t, err := store.GetTask(ctx, project, token, id)
				return result(t, err)
			case "assign":
				t, err := store.AssignTask(ctx, project, token, id, str(a, "assigned_to"))
				return result(t, err)
			case "request_review":
				err := store.RequestReview(ctx, project, token, id, num(a, "attempt", 0))
				if errors.Is(err, ErrUnavailable) && outbox != nil {
					queued, queueErr := outbox.EnqueueReview(ctx, id, num(a, "attempt", 0))
					if queueErr != nil {
						return result(nil, queueErr)
					}
					return result(map[string]any{"requested": false, "queued_for_delivery": true, "status": queued.Status, "request_id": queued.RequestID}, nil)
				}
				return result(map[string]bool{"requested": err == nil}, err)
			case "review_outbox":
				if outbox == nil {
					return result(nil, fmt.Errorf("offline outbox is not configured"))
				}
				items, err := outbox.ListReviewQueue(ctx, num(a, "limit", 50))
				return result(items, err)
			case "retry_review":
				if outbox == nil {
					return result(nil, fmt.Errorf("offline outbox is not configured"))
				}
				err := outbox.RetryReview(ctx, str(a, "request_id"))
				return result(map[string]bool{"retried": err == nil}, err)
			case "discard_review":
				if outbox == nil {
					return result(nil, fmt.Errorf("offline outbox is not configured"))
				}
				err := outbox.DiscardReview(ctx, str(a, "request_id"))
				return result(map[string]bool{"discarded": err == nil}, err)
			case "prune_reviews":
				if outbox == nil {
					return result(nil, fmt.Errorf("offline outbox is not configured"))
				}
				count, err := outbox.PruneDeliveredReviews(ctx)
				return result(map[string]int64{"pruned": count}, err)
			case "block", "resolve", "cancel":
				t, err := store.TransitionTask(ctx, project, token, id, str(a, "operation"), str(a, "reason"), num(a, "attempt", 0))
				if err == nil && str(a, "operation") != "resolve" {
					keeper.stop(id, t.Attempt)
				}
				return result(t, err)
			case "claim":
				workspace := str(a, "workspace_id")
				refresh := true
				if v, ok := a["refresh_git"].(bool); ok {
					refresh = v
				}
				lease := time.Duration(num(a, "lease_seconds", 300)) * time.Second
				autoRenew := true
				if v, ok := a["auto_renew"].(bool); ok {
					autoRenew = v
				}
				if workspace != "" && refresh {
					spaces, err := store.ListWorkspaces(ctx, project, token)
					if err != nil {
						return result(nil, err)
					}
					var path string
					for _, w := range spaces {
						if w.ID == workspace {
							path = w.LocalPath
							break
						}
					}
					if path == "" {
						return result(nil, ErrNotFound)
					}
					observation, err := InspectGitWorkspace(ctx, path)
					if err != nil {
						return result(nil, err)
					}
					t, err := store.ClaimObservedWorkspace(ctx, project, token, id, workspace, observation, lease)
					if err == nil && autoRenew {
						keeper.track(id, t.Attempt, lease)
					}
					return result(t, err)
				}
				t, err := store.ClaimTaskInWorkspace(ctx, project, token, id, workspace, lease)
				if err == nil && autoRenew {
					keeper.track(id, t.Attempt, lease)
				}
				return result(t, err)
			case "renew":
				t, err := store.RenewLease(ctx, project, token, id, num(a, "attempt", 0), time.Duration(num(a, "lease_seconds", 300))*time.Second)
				return result(t, err)
			case "reconcile":
				t, err := store.ReconcileTask(ctx, project, token, id, rev)
				return result(t, err)
			default:
				return result(nil, fmt.Errorf("unknown task operation"))
			}
		})
	srv.AddTool(mcp.NewTool("gossip_usage", mcp.WithDescription("Record one immutable, retry-safe usage summary for the calling agent's task attempt. Usage is agent-reported evidence, not provider-verified billing or a hard budget meter. Use source provider_reported only when returned by the provider, host_reported only when observed by the agent host, otherwise estimated. Cost uses integer millionths of the currency unit; currency must be a three-letter code when cost is nonzero."),
		mcp.WithString("task_id", mcp.Required()), mcp.WithInteger("attempt", mcp.Required()), mcp.WithString("request_id", mcp.Required(), mcp.Description("Stable ID to reuse if the response is lost")),
		mcp.WithString("provider", mcp.Required()), mcp.WithString("model", mcp.Required()), mcp.WithString("source", mcp.Required(), mcp.Description("provider_reported, host_reported, or estimated")),
		mcp.WithInteger("input_tokens", mcp.Required()), mcp.WithInteger("output_tokens", mcp.Required()), mcp.WithInteger("cost_micros", mcp.Required()), mcp.WithString("currency")),
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			a := args(r)
			usage, err := store.RecordAttemptUsage(ctx, project, token, str(a, "task_id"), num(a, "attempt", 0), str(a, "request_id"), UsageReport{Provider: str(a, "provider"), Model: str(a, "model"), Source: str(a, "source"), InputTokens: int64(num(a, "input_tokens", 0)), OutputTokens: int64(num(a, "output_tokens", 0)), CostMicros: int64(num(a, "cost_micros", 0)), Currency: str(a, "currency")})
			return result(usage, err)
		})
	srv.AddTool(mcp.NewTool("gossip_submit", mcp.WithDescription("Submit a report for a live task attempt with its source baseline. For full_code review, use capture_diff to capture the registered worktree from the attempt's recorded commit, then request review; the orchestrator receives the candidate ID and digest and can fetch bounded pages. Capture is limited to the current MCP process worktree and includes tracked changes plus non-ignored untracked files. For diagram_report, include populated Markdown sections titled Review summary, Scope and evidence, UML class diagram, Sequence diagram, Findings, and Coverage and limitations. Put the classDiagram and sequenceDiagram Mermaid blocks in their matching sections, cover every planned coverage item, and state evidence limitations."),
		mcp.WithString("operation", mcp.Description("submit (default), capture_diff, save_candidate, enqueue_candidate, candidate_outbox, retry_candidate, discard_candidate, prune_candidates, report_outbox, enqueue_report, retry_report, discard_report, prune_reports, list_candidates, get_candidate, or candidate_page")),
		mcp.WithString("task_id"), mcp.WithInteger("attempt"), mcp.WithString("candidate_id"), mcp.WithString("request_id"), mcp.WithInteger("limit"), mcp.WithInteger("offset"), mcp.WithInteger("max_chars"),
		mcp.WithString("baseline", mcp.Description("Source revision or workspace baseline")),
		mcp.WithString("body", mcp.Description("Markdown report or result"))),
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			a := args(r)
			switch str(a, "operation") {
			case "report_outbox":
				if outbox == nil {
					return result(nil, errors.New("report outbox is not enabled for this adapter"))
				}
				limit := num(a, "limit", 50)
				if limit == 0 {
					limit = 50
				}
				items, err := outbox.ListReportQueue(ctx, limit)
				return result(items, err)
			case "enqueue_report":
				if outbox == nil {
					return result(nil, errors.New("report outbox is not enabled for this adapter"))
				}
				queued, err := outbox.EnqueueReport(ctx, str(a, "task_id"), num(a, "attempt", 0), str(a, "baseline"), str(a, "body"), str(a, "request_id"))
				return result(queued, err)
			case "retry_report":
				if outbox == nil {
					return result(nil, errors.New("report outbox is not enabled for this adapter"))
				}
				err := outbox.RetryReport(ctx, str(a, "request_id"))
				return result(map[string]bool{"retried": err == nil}, err)
			case "discard_report":
				if outbox == nil {
					return result(nil, errors.New("report outbox is not enabled for this adapter"))
				}
				err := outbox.DiscardReport(ctx, str(a, "request_id"))
				return result(map[string]bool{"discarded": err == nil}, err)
			case "prune_reports":
				if outbox == nil {
					return result(nil, errors.New("report outbox is not enabled for this adapter"))
				}
				count, err := outbox.PruneReports(ctx)
				return result(map[string]int64{"removed": count}, err)
			case "", "submit":
				taskID, attempt, baseline, body := str(a, "task_id"), num(a, "attempt", 0), str(a, "baseline"), str(a, "body")
				requestID := str(a, "request_id")
				if requestID == "" {
					requestID = stableReportRequestID(taskID, attempt)
				}
				var report Report
				var err error
				if submitter, ok := store.(interface {
					SubmitReportWithID(context.Context, string, string, string, int, string, string, string) (Report, error)
				}); ok {
					report, err = submitter.SubmitReportWithID(ctx, project, token, taskID, attempt, baseline, body, requestID)
				} else {
					report, err = store.SubmitReport(ctx, project, token, taskID, attempt, baseline, body)
				}
				if errors.Is(err, ErrUnavailable) && outbox != nil {
					queued, queueErr := outbox.EnqueueReport(ctx, taskID, attempt, baseline, body, requestID)
					if queueErr != nil {
						return result(nil, queueErr)
					}
					return result(queued, nil)
				}
				if err == nil {
					keeper.stop(str(a, "task_id"), report.Attempt)
				}
				report.Body = ""
				return result(report, err)
			case "save_candidate":
				candidate, err := store.SaveCandidate(ctx, project, token, str(a, "task_id"), num(a, "attempt", 0), str(a, "baseline"), str(a, "body"))
				candidate.Body = ""
				return result(candidate, err)
			case "capture_diff":
				taskID, number, requestID := str(a, "task_id"), num(a, "attempt", 0), str(a, "request_id")
				if requestID == "" {
					return result(nil, errors.New("capture_diff requires a stable request_id so retries cannot create duplicate candidates"))
				}
				attempt, err := store.GetAttempt(ctx, project, token, taskID, number)
				if err != nil {
					return result(nil, err)
				}
				if attempt.WorkspaceID == "" || attempt.WorkspaceHead == "" {
					return result(nil, errors.New("capture_diff requires an attempt claimed with a registered Git workspace and commit baseline"))
				}
				workspaces, err := store.ListWorkspaces(ctx, project, token)
				if err != nil {
					return result(nil, err)
				}
				var selected *Workspace
				for index := range workspaces {
					if workspaces[index].ID == attempt.WorkspaceID {
						selected = &workspaces[index]
						break
					}
				}
				if selected == nil {
					return result(nil, errors.New("attempt workspace is no longer registered"))
				}
				current, err := InspectGitWorkspace(ctx, ".")
				if err != nil {
					return result(nil, err)
				}
				registered, err := filepath.Abs(selected.LocalPath)
				if err != nil {
					return result(nil, err)
				}
				if resolved, resolveErr := filepath.EvalSymlinks(registered); resolveErr == nil {
					registered = resolved
				}
				if filepath.Clean(registered) != filepath.Clean(current.Root) {
					return result(nil, errors.New("capture_diff is limited to this MCP process worktree; its registered path does not match the current Git root"))
				}
				body, err := CaptureGitDiff(ctx, current.Root, attempt.WorkspaceHead)
				if err != nil {
					return result(nil, err)
				}
				var candidate CandidateReport
				if outbox != nil {
					queued, queueErr := outbox.EnqueueCandidate(ctx, taskID, number, attempt.WorkspaceHead, body, requestID)
					queued.Body = ""
					return result(queued, queueErr)
				} else {
					candidate, err = store.SaveCandidateWithID(ctx, project, token, taskID, number, attempt.WorkspaceHead, body, requestID)
				}
				candidate.Body = ""
				return result(candidate, err)
			case "enqueue_candidate", "candidate_outbox", "retry_candidate", "discard_candidate", "prune_candidates":
				if outbox == nil {
					return result(nil, fmt.Errorf("outbox is not configured"))
				}
				switch str(a, "operation") {
				case "enqueue_candidate":
					c, err := outbox.EnqueueCandidate(ctx, str(a, "task_id"), num(a, "attempt", 0), str(a, "baseline"), str(a, "body"), str(a, "request_id"))
					c.Body = ""
					return result(c, err)
				case "candidate_outbox":
					cs, err := outbox.ListCandidateQueue(ctx, num(a, "limit", 50))
					return result(cs, err)
				case "retry_candidate":
					err := outbox.RetryCandidate(ctx, str(a, "request_id"))
					return result(map[string]bool{"queued": err == nil}, err)
				case "discard_candidate":
					err := outbox.DiscardCandidate(ctx, str(a, "request_id"))
					return result(map[string]bool{"discarded": err == nil}, err)
				default:
					n, err := outbox.PruneCandidates(ctx)
					return result(map[string]int64{"removed": n}, err)
				}
			case "list_candidates":
				candidates, err := store.ListCandidates(ctx, project, token, str(a, "task_id"), num(a, "limit", 50))
				return result(candidates, err)
			case "get_candidate":
				candidate, err := store.GetCandidate(ctx, project, token, str(a, "candidate_id"))
				return result(candidate, err)
			case "candidate_page":
				page, err := store.GetCandidatePage(ctx, project, token, str(a, "candidate_id"), num(a, "offset", 0), num(a, "max_chars", 2048))
				return result(page, err)
			default:
				return result(nil, fmt.Errorf("unknown submission operation"))
			}
		})
	srv.AddTool(mcp.NewTool("gossip_review", mcp.WithDescription("Accept a report or request rework on its exact digest; the author cannot review their own submission."),
		mcp.WithString("report_id", mcp.Required()), mcp.WithString("digest", mcp.Required()), mcp.WithString("operation", mcp.Description("accept (default) or rework")), mcp.WithString("feedback", mcp.Description("Required for rework"))),
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			a := args(r)
			switch str(a, "operation") {
			case "", "accept":
				err := store.AcceptReport(ctx, project, token, str(a, "report_id"), str(a, "digest"))
				return result(map[string]bool{"accepted": err == nil}, err)
			case "rework":
				err := store.RequestRework(ctx, project, token, str(a, "report_id"), str(a, "digest"), str(a, "feedback"))
				return result(map[string]bool{"rework_requested": err == nil}, err)
			default:
				return result(nil, fmt.Errorf("unknown review operation"))
			}
		})
	srv.AddTool(mcp.NewTool("gossip_send", mcp.WithDescription("Send a project message or a direct message to a project principal."),
		mcp.WithString("operation", mcp.Description("send (default), enqueue, outbox, retry, or prune_delivered")),
		mcp.WithString("recipient", mcp.Description("Principal ID, or empty for the project")),
		mcp.WithString("thread_id"), mcp.WithString("subject"), mcp.WithString("body"), mcp.WithInteger("limit"), mcp.WithString("request_id", mcp.Description("Stable unique ID for safe retries of the same message"))),
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			a := args(r)
			switch str(a, "operation") {
			case "", "send":
				message, err := store.SendMessageWithID(ctx, project, token, str(a, "recipient"), str(a, "thread_id"), str(a, "subject"), str(a, "body"), str(a, "request_id"))
				return result(message, err)
			case "enqueue", "outbox", "retry", "prune_delivered":
				if outbox == nil {
					return result(nil, fmt.Errorf("outbox is not configured"))
				}
				switch str(a, "operation") {
				case "enqueue":
					message, err := outbox.Enqueue(ctx, str(a, "recipient"), str(a, "thread_id"), str(a, "subject"), str(a, "body"), str(a, "request_id"))
					message.Body = ""
					return result(message, err)
				case "outbox":
					messages, err := outbox.List(ctx, num(a, "limit", 50))
					return result(messages, err)
				case "retry":
					err := outbox.Retry(ctx, str(a, "request_id"))
					return result(map[string]bool{"queued": err == nil}, err)
				default:
					count, err := outbox.Prune(ctx)
					return result(map[string]int64{"removed": count}, err)
				}
			default:
				return result(nil, fmt.Errorf("unknown send operation"))
			}
		})
	srv.AddTool(mcp.NewTool("gossip_read", mcp.WithDescription("Read an authorized report or messages, explicitly acknowledge a received message, or inspect acknowledgments."),
		mcp.WithString("operation", mcp.Description("report (default), messages, acknowledge, or acknowledgments")),
		mcp.WithString("report_id"), mcp.WithString("message_id"), mcp.WithInteger("after"), mcp.WithInteger("limit"), mcp.WithInteger("offset", mcp.Description("Report offset in Unicode code points")), mcp.WithInteger("max_chars", mcp.Description("Report excerpt size, 1 to 16384; defaults to 2048"))),
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			a := args(r)
			if str(a, "operation") == "messages" {
				messages, err := store.ListMessages(ctx, project, token, int64(num(a, "after", 0)), num(a, "limit", 50))
				return result(messages, err)
			}
			if str(a, "operation") == "acknowledge" {
				v, err := store.AcknowledgeMessage(ctx, project, token, str(a, "message_id"))
				return result(v, err)
			}
			if str(a, "operation") == "acknowledgments" {
				v, err := store.MessageAcknowledgments(ctx, project, token, str(a, "message_id"))
				return result(v, err)
			}
			if str(a, "operation") != "" && str(a, "operation") != "report" {
				return result(nil, fmt.Errorf("unknown read operation"))
			}
			report, err := store.GetReportPage(ctx, project, token, str(a, "report_id"), num(a, "offset", 0), num(a, "max_chars", 2048))
			return result(report, err)
		})
	srv.AddTool(mcp.NewTool("gossip_sync", mcp.WithDescription("Read or wait for project events, inspect or clear the offline cache, refresh project snapshots, acknowledge a durable sequence cursor, or explicitly prune old event history as the project owner."),
		mcp.WithString("operation", mcp.Description("events (default), wait, cached, refresh_cache, clear_cache, checkpoint, acknowledge, or owner-only prune_events")),
		mcp.WithInteger("after", mcp.Description("Last event sequence read or processed for acknowledgment")), mcp.WithInteger("through_sequence", mcp.Description("For prune_events only: permanently remove project event rows through this sequence; stale replicas will be forced to refresh their snapshots")), mcp.WithInteger("limit", mcp.Description("Maximum results, 1 to 200")), mcp.WithInteger("wait_seconds", mcp.Description("For wait only; bounded to 25 seconds"))),
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			a := args(r)
			switch str(a, "operation") {
			case "", "events":
				events, err := store.ReadEvents(ctx, project, token, int64(num(a, "after", 0)), num(a, "limit", 50))
				if err == nil && outbox != nil {
					_ = outbox.cacheEvents(ctx, events)
				}
				return result(events, err)
			case "wait":
				waitSeconds := num(a, "wait_seconds", 20)
				if waitSeconds < 0 || waitSeconds > 25 {
					return result(nil, fmt.Errorf("wait_seconds must be between 0 and 25"))
				}
				page, err := waitEvents(ctx, store, project, token, int64(num(a, "after", 0)), num(a, "limit", 50), time.Duration(waitSeconds)*time.Second)
				if err == nil && outbox != nil && len(page.Events) > 0 {
					_ = outbox.cacheEvents(ctx, page.Events)
				}
				return result(page, err)
			case "cached":
				if outbox == nil {
					return result(nil, fmt.Errorf("offline cache is not configured"))
				}
				page, err := outbox.readCachedEvents(ctx, int64(num(a, "after", 0)), int64(num(a, "limit", 50)))
				return result(page, err)
			case "refresh_cache":
				if outbox == nil {
					return result(nil, fmt.Errorf("offline cache is not configured"))
				}
				status, err := outbox.refreshCache(ctx, store, project, token)
				return result(status, err)
			case "clear_cache":
				if outbox == nil {
					return result(nil, fmt.Errorf("offline cache is not configured"))
				}
				err := outbox.ClearCache(ctx)
				return result(map[string]bool{"cleared": err == nil}, err)
			case "checkpoint":
				seq, err := store.EventCheckpoint(ctx, project, token)
				return result(map[string]int64{"sequence": seq}, err)
			case "acknowledge":
				seq, err := store.AcknowledgeEvents(ctx, project, token, int64(num(a, "after", 0)))
				return result(map[string]int64{"sequence": seq}, err)
			case "prune_events":
				status, err := store.PruneProjectEvents(ctx, project, token, int64(num(a, "through_sequence", 0)))
				return result(status, err)
			default:
				return result(nil, fmt.Errorf("unknown sync operation"))
			}
		})
	return srv
}
