package coord_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/tasmirz/gossip/pkg/coord"
)

func TestMCPBoundResearchHandoff(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ownerServer := coord.MCPServer(s, p.ID, owner.Token)
	workerServer := coord.MCPServer(s, p.ID, worker.Token)
	call := func(serverToken bool, name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		srv := ownerServer
		if !serverToken {
			srv = workerServer
		}
		tool := srv.GetTool(name)
		if tool == nil {
			t.Fatalf("missing %s", name)
		}
		res, err := tool.Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: args}})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := call(true, "gossip_task", map[string]any{"operation": "create", "request_id": "task-create-test", "plan_revision": 1, "title": "Analyze", "description": "Study behavior", "kind": "research", "questions": []any{"How does it work?"}, "formats": []any{"mermaid_sequence"}, "required_role": "researcher", "review_mode": "report_first", "review_type": "full_code"})
	if res.IsError {
		t.Fatalf("create: %+v", res)
	}
	text := res.Content[0].(mcp.TextContent).Text
	var task coord.Task
	if err := json.Unmarshal([]byte(text), &task); err != nil {
		t.Fatal(err)
	}
	if len(task.Contract.Questions) != 1 || len(task.Contract.Formats) != 1 || task.Contract.RequiredRole != "researcher" {
		t.Fatalf("contract lost: %+v", task.Contract)
	}
	if _, err := s.SetWorkRole(context.Background(), p.ID, owner.Token, worker.PrincipalID, "", "Researcher", "Investigate bounded questions", 0, 0); err != nil {
		t.Fatal(err)
	}
	res = call(false, "gossip_task", map[string]any{"operation": "claim", "task_id": task.ID, "lease_seconds": int(time.Minute.Seconds())})
	if res.IsError {
		t.Fatalf("claim: %+v", res)
	}
	if err := json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &task); err != nil {
		t.Fatal(err)
	}
	diff := "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new"
	res = call(false, "gossip_submit", map[string]any{"operation": "save_candidate", "task_id": task.ID, "attempt": task.Attempt, "baseline": "commit:abc", "body": diff})
	if res.IsError {
		t.Fatalf("save full-code source diff: %+v", res)
	}
	var candidate coord.CandidateReport
	if err := json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &candidate); err != nil {
		t.Fatal(err)
	}
	res = call(false, "gossip_task", map[string]any{"operation": "request_review", "task_id": task.ID, "attempt": task.Attempt})
	if res.IsError {
		t.Fatalf("request planned review: %+v", res)
	}
	res = call(true, "gossip_submit", map[string]any{"operation": "candidate_page", "candidate_id": candidate.ID, "offset": 0, "max_chars": 16})
	if res.IsError || !strings.Contains(res.Content[0].(mcp.TextContent).Text, candidate.Digest) || !strings.Contains(res.Content[0].(mcp.TextContent).Text, "diff --git") {
		t.Fatalf("orchestrator could not page the pinned code evidence: %+v", res)
	}
	res = call(false, "gossip_submit", map[string]any{"operation": "save_candidate", "task_id": task.ID, "attempt": task.Attempt, "baseline": "commit:abc", "body": strings.ReplaceAll(diff, "+new", "+newer")})
	if res.IsError {
		t.Fatalf("save later candidate: %+v", res)
	}
	res = call(false, "gossip_submit", map[string]any{"task_id": task.ID, "attempt": task.Attempt, "baseline": "commit:abc", "body": "Sequence: inspect -> report"})
	if res.IsError {
		t.Fatalf("submit: %+v", res)
	}
	var report coord.Report
	if err := json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &report); err != nil {
		t.Fatal(err)
	}
	if report.SourceCandidateID != candidate.ID || report.SourceCandidateDigest != candidate.Digest {
		t.Fatalf("full-code report lost its pinned source evidence: %+v", report)
	}
	res = call(false, "gossip_review", map[string]any{"report_id": report.ID, "digest": report.Digest})
	if !res.IsError {
		t.Fatal("worker could accept a report")
	}
	res = call(true, "gossip_review", map[string]any{"report_id": report.ID, "digest": report.Digest})
	if res.IsError {
		t.Fatalf("review: %+v", res)
	}
	events, err := s.ReadEvents(context.Background(), p.ID, owner.Token, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	acceptedEvidence := false
	for _, event := range events {
		if event.Kind != "report.accepted" || event.EntityID != report.ID {
			continue
		}
		var payload map[string]any
		if err = json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		acceptedEvidence = payload["source_candidate_id"] == candidate.ID && payload["source_candidate_digest"] == candidate.Digest
	}
	if !acceptedEvidence {
		t.Fatal("report acceptance did not preserve the exact reviewed source artifact")
	}
	res = call(true, "gossip_sync", map[string]any{"after": 0, "limit": 100})
	if res.IsError || strings.Contains(res.Content[0].(mcp.TextContent).Text, worker.Token) {
		t.Fatal("sync failed or exposed credential")
	}
}

func TestProjectBootstrapMCPCreatesInferredWorkspaceAndPrivateCredential(t *testing.T) {
	store, _, _, _, _ := setup(t)
	root := filepath.Join(t.TempDir(), "local-checkout")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"remote", "add", "origin", "https://github.com/acme/widget.git"}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	credentialDir := filepath.Join(t.TempDir(), "credentials")
	srv := coord.ProjectBootstrapMCPServer(store, credentialDir)
	tool := srv.GetTool("gossip_create_project")
	if tool == nil {
		t.Fatal("bootstrap project tool missing")
	}
	requestID := "bootstrap-project-test"
	result, err := tool.Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_create_project", Arguments: map[string]any{"local_path": root, "request_id": requestID}}})
	if err != nil || result.IsError {
		t.Fatalf("bootstrap create failed: result=%+v err=%v", result, err)
	}
	var created struct {
		Project                  coord.Project    `json:"project"`
		Repository               coord.Repository `json:"repository"`
		Workspace                coord.Workspace  `json:"workspace"`
		CredentialFile           string           `json:"owner_credential_file"`
		ClientCertificateGranted bool             `json:"client_certificate_granted"`
		NextStep                 string           `json:"next_step"`
	}
	if err = json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &created); err != nil {
		t.Fatal(err)
	}
	if created.Project.Name != "widget" || created.Repository.Name != "widget" || len(created.Repository.RemoteHints) != 1 || created.Repository.RemoteHints[0] != "github.com/acme/widget" || created.Workspace.LocalPath != root || created.Workspace.RepositoryID != created.Repository.ID || created.CredentialFile == "" || created.ClientCertificateGranted || !strings.Contains(created.NextStep, "separate coord-mcp") || !strings.Contains(created.NextStep, "same home/TLS connection settings") {
		t.Fatalf("project details were not inferred: %+v", created)
	}
	info, err := os.Stat(created.CredentialFile)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credential file is not private: mode=%v err=%v", info.Mode(), err)
	}
	credentialBytes, err := os.ReadFile(created.CredentialFile)
	if err != nil {
		t.Fatal(err)
	}
	var credential struct {
		ProjectID string `json:"project_id"`
		Token     string `json:"token"`
	}
	if err = json.Unmarshal(credentialBytes, &credential); err != nil || credential.ProjectID != created.Project.ID || credential.Token == "" {
		t.Fatalf("invalid saved credential: %+v err=%v", credential, err)
	}
	if strings.Contains(result.Content[0].(mcp.TextContent).Text, credential.Token) {
		t.Fatal("MCP result exposed owner token")
	}
	retry, err := tool.Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_create_project", Arguments: map[string]any{"local_path": root, "request_id": requestID, "credential_file": created.CredentialFile}}})
	if err != nil || retry.IsError {
		t.Fatalf("prepared MCP creation retry failed: result=%+v err=%v", retry, err)
	}
	var retried struct {
		Project    coord.Project    `json:"project"`
		Repository coord.Repository `json:"repository"`
		Workspace  coord.Workspace  `json:"workspace"`
	}
	if err = json.Unmarshal([]byte(retry.Content[0].(mcp.TextContent).Text), &retried); err != nil {
		t.Fatal(err)
	}
	if retried.Project.ID != created.Project.ID || retried.Repository.ID != created.Repository.ID || retried.Workspace.ID != created.Workspace.ID {
		t.Fatalf("retry created duplicate project objects: first=%+v retry=%+v", created, retried)
	}
}

func TestMCPProjectCredentialRevocation(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	call := func(token string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		tool := coord.MCPServer(s, p.ID, token).GetTool("gossip_agents")
		result, err := tool.Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_agents", Arguments: args}})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if result := call(worker.Token, map[string]any{"operation": "revoke", "principal_id": owner.PrincipalID}); !result.IsError {
		t.Fatal("worker revoked a project principal")
	}
	if result := call(owner.Token, map[string]any{"operation": "revoke", "principal_id": worker.PrincipalID}); result.IsError {
		t.Fatalf("owner failed to revoke project credential: %+v", result)
	}
	if _, err := s.WhoAmI(context.Background(), p.ID, worker.Token); err == nil {
		t.Fatal("revoked credential remains valid")
	}
}

func TestMCPExplicitTaskDelegation(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	call := func(token, toolName string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		tool := coord.MCPServer(s, p.ID, token).GetTool(toolName)
		if tool == nil {
			t.Fatalf("missing %s", toolName)
		}
		result, err := tool.Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: toolName, Arguments: args}})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	create := map[string]any{"operation": "create", "request_id": "task-create-test", "plan_revision": 1, "title": "Research", "description": "Investigate behavior", "kind": "research", "review_type": "full_code"}
	if result := call(worker.Token, "gossip_task", create); !result.IsError {
		t.Fatal("worker created a task without delegation permission")
	}
	grant := map[string]any{"operation": "grant", "principal_id": worker.PrincipalID, "permission": coord.PermissionTaskDelegate}
	if result := call(worker.Token, "gossip_permissions", grant); !result.IsError {
		t.Fatal("worker granted their own permission")
	}
	if result := call(owner.Token, "gossip_permissions", grant); result.IsError {
		t.Fatalf("owner permission grant: %+v", result)
	}
	if result := call(worker.Token, "gossip_task", create); result.IsError {
		t.Fatalf("delegated MCP task creation: %+v", result)
	}
	if result := call(owner.Token, "gossip_permissions", map[string]any{"operation": "revoke", "principal_id": worker.PrincipalID, "permission": coord.PermissionTaskDelegate}); result.IsError {
		t.Fatalf("owner permission revoke: %+v", result)
	}
	if result := call(worker.Token, "gossip_task", create); !result.IsError {
		t.Fatal("revoked worker created an MCP task")
	}
	planGrant := map[string]any{"operation": "grant", "principal_id": worker.PrincipalID, "permission": coord.PermissionPlanEdit}
	if result := call(owner.Token, "gossip_permissions", planGrant); result.IsError {
		t.Fatalf("owner plan permission grant: %+v", result)
	}
	if result := call(worker.Token, "gossip_plan", map[string]any{"operation": "publish", "body": "delegated plan", "expected_revision": 1}); result.IsError {
		t.Fatalf("granted MCP plan edit: %+v", result)
	}
	if result := call(owner.Token, "gossip_permissions", map[string]any{"operation": "revoke", "principal_id": worker.PrincipalID, "permission": coord.PermissionPlanEdit}); result.IsError {
		t.Fatalf("owner revoked plan permission: %+v", result)
	}
	if result := call(worker.Token, "gossip_plan", map[string]any{"operation": "publish", "body": "revoked plan", "expected_revision": 2}); !result.IsError {
		t.Fatal("revoked MCP plan permission remained effective")
	}
	settingsGrant := map[string]any{"operation": "grant", "principal_id": worker.PrincipalID, "permission": coord.PermissionSettingsEdit}
	if result := call(owner.Token, "gossip_permissions", settingsGrant); result.IsError {
		t.Fatalf("owner settings permission grant: %+v", result)
	}
	settings := map[string]any{"operation": "update", "revision": 1, "max_parallel": 3, "max_lease_seconds": 3600, "summary_token_target": 1000, "default_review_mode": "report_first"}
	if result := call(worker.Token, "gossip_settings", settings); result.IsError {
		t.Fatalf("granted MCP settings edit: %+v", result)
	}
	if result := call(owner.Token, "gossip_permissions", map[string]any{"operation": "revoke", "principal_id": worker.PrincipalID, "permission": coord.PermissionSettingsEdit}); result.IsError {
		t.Fatalf("owner revoked settings permission: %+v", result)
	}
	settings["revision"] = 2
	settings["max_parallel"] = 2
	if result := call(worker.Token, "gossip_settings", settings); !result.IsError {
		t.Fatal("revoked MCP settings permission remained effective")
	}
}

func TestMCPProjectCertificateAccess(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	fingerprint := strings.Repeat("12", 32)
	call := func(token string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		tool := coord.MCPServer(s, p.ID, token).GetTool("gossip_access")
		result, err := tool.Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_access", Arguments: args}})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if result := call(worker.Token, map[string]any{"operation": "grant", "fingerprint": fingerprint}); !result.IsError {
		t.Fatal("worker granted certificate access")
	}
	if result := call(owner.Token, map[string]any{"operation": "grant", "fingerprint": fingerprint, "expires_at": "invalid"}); !result.IsError {
		t.Fatal("invalid expiry granted indefinite certificate access")
	}
	if allowed, err := s.CertificateAuthorized(context.Background(), p.ID, fingerprint); err != nil || allowed {
		t.Fatalf("invalid expiry persisted a grant: %v %v", allowed, err)
	}
	if result := call(owner.Token, map[string]any{"operation": "grant", "fingerprint": fingerprint}); result.IsError {
		t.Fatalf("owner failed to grant certificate access: %+v", result)
	}
	if allowed, err := s.CertificateAuthorized(context.Background(), p.ID, fingerprint); err != nil || !allowed {
		t.Fatalf("MCP grant not persisted: %v %v", allowed, err)
	}
	if result := call(worker.Token, map[string]any{"operation": "list"}); !result.IsError {
		t.Fatal("worker listed certificate access")
	}
	if result := call(owner.Token, map[string]any{"operation": "revoke", "fingerprint": fingerprint}); result.IsError {
		t.Fatalf("owner failed to revoke certificate access: %+v", result)
	}
}

func TestMCPReadsProjectScopedLegacyHistory(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "state.json"), []byte(`{"project_name":"legacy","agents":{},"goals":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	archive, _, err := s.ImportLegacyArchive(context.Background(), p.ID, owner.Token, source, false)
	if err != nil {
		t.Fatal(err)
	}
	tool := coord.MCPServer(s, p.ID, worker.Token).GetTool("gossip_history")
	if tool == nil {
		t.Fatal("gossip_history tool missing")
	}
	result, err := tool.Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_history", Arguments: map[string]any{"operation": "imports"}}})
	if err != nil || result.IsError || !strings.Contains(result.Content[0].(mcp.TextContent).Text, archive.ID) {
		t.Fatalf("project member could not read legacy archive: %+v %v", result, err)
	}
	result, err = tool.Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_history", Arguments: map[string]any{"operation": "records", "import_id": archive.ID, "record_type": "goal", "limit": 10}}})
	if err != nil || result.IsError {
		t.Fatalf("project member could not read archived record page: %+v %v", result, err)
	}
}

func TestMCPSendSupportsRetryKey(t *testing.T) {
	s, _, p, owner, _ := setup(t)
	srv := coord.MCPServer(s, p.ID, owner.Token)
	tool := srv.GetTool("gossip_send")
	if tool == nil {
		t.Fatal("missing send tool")
	}
	call := func(body string) *mcp.CallToolResult {
		t.Helper()
		r, err := tool.Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_send", Arguments: map[string]any{"body": body, "request_id": "retry-1"}}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	first, second := call("hello"), call("hello")
	if first.IsError || second.IsError || first.Content[0].(mcp.TextContent).Text != second.Content[0].(mcp.TextContent).Text {
		t.Fatal("retry did not return the same message")
	}
	if !call("different").IsError {
		t.Fatal("reused retry key accepted different message")
	}
}

func TestMCPAdapterRenewsClaimUntilSubmission(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	task, err := s.CreateTask(context.Background(), p.ID, owner.Token, 1, "Lease", "Check renewal", "research", coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	adapter := coord.NewMCPAdapter(s, p.ID, worker.Token)
	defer adapter.Close()
	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		res, err := adapter.Server.GetTool(name).Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: args}})
		if err != nil || res.IsError {
			t.Fatalf("%s: %+v %v", name, res, err)
		}
		return res
	}
	res := call("gossip_task", map[string]any{"operation": "claim", "task_id": task.ID, "lease_seconds": 1})
	if err := json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &task); err != nil {
		t.Fatal(err)
	}
	original := task.LeaseUntil
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
		task, err = s.GetTask(context.Background(), p.ID, worker.Token, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if task.LeaseUntil > original {
			break
		}
	}
	if task.LeaseUntil <= original {
		t.Fatal("lease was not renewed")
	}
	call("gossip_submit", map[string]any{"task_id": task.ID, "attempt": task.Attempt, "baseline": "commit:abc", "body": "Findings"})
}

func TestMCPSyncCheckpoint(t *testing.T) {
	s, _, p, _, worker := setup(t)
	srv := coord.MCPServer(s, p.ID, worker.Token)
	call := func(args map[string]any) *mcp.CallToolResult {
		t.Helper()
		result, err := srv.GetTool("gossip_sync").Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_sync", Arguments: args}})
		if err != nil || result.IsError {
			t.Fatalf("sync: %+v %v", result, err)
		}
		return result
	}
	res := call(map[string]any{"operation": "events"})
	var events []coord.Event
	if err := json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &events); err != nil || len(events) == 0 {
		t.Fatalf("events: %+v %v", events, err)
	}
	last := events[len(events)-1].Sequence
	call(map[string]any{"operation": "acknowledge", "after": int(last)})
	res = call(map[string]any{"operation": "checkpoint"})
	var checkpoint struct {
		Sequence int64 `json:"sequence"`
	}
	if err := json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &checkpoint); err != nil || checkpoint.Sequence != last {
		t.Fatalf("checkpoint: %+v %v", checkpoint, err)
	}
}
