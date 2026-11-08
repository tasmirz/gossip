package coord_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/tasmirz/gossip/pkg/coord"
)

func TestIntegrationRecordRequiresScopedGrantAndExactCleanRevision(t *testing.T) {
	s, _, project, owner, worker := setup(t)
	ctx := context.Background()
	repo, err := s.CreateRepository(ctx, project.ID, owner.Token, "shared")
	if err != nil {
		t.Fatal(err)
	}
	base := strings.Repeat("1", 40)
	combined := strings.Repeat("2", 40)
	workerWS, err := s.RegisterWorkspace(ctx, project.ID, worker.Token, repo.ID, "worker", "node-a", "/work/shared-worker", "feature", base, false)
	if err != nil {
		t.Fatal(err)
	}
	otherBranchRevision := strings.Repeat("3", 40)
	workerWS2, err := s.RegisterWorkspace(ctx, project.ID, worker.Token, repo.ID, "worker second checkout", "node-b", "/work/shared-worker-2", "feature", otherBranchRevision, false)
	if err != nil {
		t.Fatal(err)
	}
	ownerWS, err := s.RegisterWorkspace(ctx, project.ID, owner.Token, repo.ID, "owner", "node-a", "/work/shared-owner", "main", base, false)
	if err != nil {
		t.Fatal(err)
	}

	candidates := make([]string, 0, 2)
	for i := 0; i < 2; i++ {
		task, e := s.CreateTask(ctx, project.ID, owner.Token, 1, "Source change", "Parallel implementation candidate", "implementation", coord.ReportContract{ReviewType: "full_code"}, nil)
		if e != nil {
			t.Fatal(e)
		}
		workspaceID := workerWS.ID
		candidateRevision := base
		if i == 1 {
			workspaceID = workerWS2.ID
			candidateRevision = otherBranchRevision
		}
		claim, e := s.ClaimTaskInWorkspace(ctx, project.ID, worker.Token, task.ID, workspaceID, time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		candidate, e := s.SaveCandidate(ctx, project.ID, worker.Token, task.ID, claim.Attempt, candidateRevision, "candidate summary")
		if e != nil {
			t.Fatal(e)
		}
		candidates = append(candidates, candidate.ID)
	}
	integrationTask, err := s.CreateTask(ctx, project.ID, owner.Token, 1, "Combine candidates", "Reconcile parallel branches and verify the result", "integration", coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AssignTask(ctx, project.ID, owner.Token, integrationTask.ID, worker.PrincipalID); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimTaskInWorkspace(ctx, project.ID, worker.Token, integrationTask.ID, workerWS.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	checks := []coord.IntegrationCheck{{Name: "unit", Command: "go test ./...", Passed: true, Revision: combined, Evidence: "all packages passed"}}
	observation := coord.GitObservation{Root: workerWS.LocalPath, Branch: "feature", Head: combined, Dirty: false}
	submission := coord.IntegrationSubmission{TaskID: integrationTask.ID, Attempt: claim.Attempt, WorkspaceID: workerWS.ID, BaseRevision: base, CombinedRevision: combined, Observation: observation, CandidateIDs: candidates, Checks: checks, Summary: "Combined both candidate changes.", RequestID: "record-1"}
	if _, err = s.RecordIntegration(ctx, project.ID, worker.Token, submission); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("integration without explicit grant = %v", err)
	}
	if _, err = s.GrantPermission(ctx, project.ID, owner.Token, worker.PrincipalID, coord.PermissionIntegration); err != nil {
		t.Fatal(err)
	}
	bad := observation
	bad.Dirty = true
	badSubmission := submission
	badSubmission.Observation = bad
	badSubmission.Summary, badSubmission.RequestID = "dirty", "dirty"
	if _, err = s.RecordIntegration(ctx, project.ID, worker.Token, badSubmission); err == nil {
		t.Fatal("accepted a dirty integration workspace")
	}
	badChecks := []coord.IntegrationCheck{{Name: "unit", Command: "go test ./...", Passed: true, Revision: base}}
	badSubmission = submission
	badSubmission.Checks, badSubmission.Summary, badSubmission.RequestID = badChecks, "wrong revision", "wrong-revision"
	if _, err = s.RecordIntegration(ctx, project.ID, worker.Token, badSubmission); err == nil {
		t.Fatal("accepted checks recorded against the base revision")
	}
	badSubmission = submission
	badSubmission.WorkspaceID, badSubmission.Observation, badSubmission.Summary, badSubmission.RequestID = ownerWS.ID, coord.GitObservation{Root: ownerWS.LocalPath, Head: combined, Branch: "main"}, "wrong workspace", "wrong-workspace"
	if _, err = s.RecordIntegration(ctx, project.ID, worker.Token, badSubmission); err == nil {
		t.Fatal("accepted a workspace not bound to the active attempt")
	}
	record, err := s.RecordIntegration(ctx, project.ID, worker.Token, submission)
	if err != nil || !record.Passed || record.CombinedRevision != combined || record.EvidenceTrust != "host_reported" {
		t.Fatalf("record: %+v %v", record, err)
	}
	workspaces, err := s.ListWorkspaces(ctx, project.ID, worker.Token)
	if err != nil || len(workspaces) != 3 || workspaces[0].Head != combined && workspaces[1].Head != combined && workspaces[2].Head != combined {
		t.Fatalf("combined workspace observation was not committed with integration: %+v %v", workspaces, err)
	}
	replayed, err := s.RecordIntegration(ctx, project.ID, worker.Token, submission)
	if err != nil || replayed.ID != record.ID {
		t.Fatalf("idempotent replay: %+v %v", replayed, err)
	}
	badSubmission = submission
	badSubmission.Summary = "changed payload"
	if _, err = s.RecordIntegration(ctx, project.ID, worker.Token, badSubmission); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("request ID payload conflict = %v", err)
	}
	listed, err := s.ListIntegrations(ctx, project.ID, owner.Token, 10)
	if err != nil || len(listed) != 1 || listed[0].ID != record.ID {
		t.Fatalf("listed: %+v %v", listed, err)
	}
	// Exercise the same project-scoped endpoint the live Workspaces view uses.
	handler := coord.NewWebHandler(s)
	webRequest := func(method, path string, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "http://localhost:8081"+path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:12345"
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "http://localhost:8081")
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	login := webRequest(http.MethodPost, "/api/login", `{"project_id":"`+project.ID+`","token":"`+owner.Token+`"}`, nil)
	if login.Code != http.StatusOK || len(login.Result().Cookies()) != 1 {
		t.Fatalf("integration UI login: %d %s", login.Code, login.Body.String())
	}
	path := "/api/projects/" + project.ID + "/integrations"
	webList := webRequest(http.MethodGet, path, "", login.Result().Cookies()[0])
	var webRecords []coord.IntegrationRecord
	if webList.Code != http.StatusOK || json.Unmarshal(webList.Body.Bytes(), &webRecords) != nil || len(webRecords) != 1 || webRecords[0].ID != record.ID || webRecords[0].CombinedRevision != combined || webRecords[0].EvidenceTrust != "host_reported" {
		t.Fatalf("project Workspaces view did not receive exact integration evidence: %d %s", webList.Code, webList.Body.String())
	}
	other, otherOwner, err := s.CreateProject(ctx, "other", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if result, e := s.ListIntegrations(ctx, other.ID, otherOwner.Token, 10); e != nil || len(result) != 0 {
		t.Fatalf("cross-project read: %+v %v", result, e)
	}
	otherLogin := webRequest(http.MethodPost, "/api/login", `{"project_id":"`+other.ID+`","token":"`+otherOwner.Token+`"}`, nil)
	if otherLogin.Code != http.StatusOK || len(otherLogin.Result().Cookies()) != 1 {
		t.Fatalf("second project UI login: %d %s", otherLogin.Code, otherLogin.Body.String())
	}
	otherWebList := webRequest(http.MethodGet, "/api/projects/"+other.ID+"/integrations", "", otherLogin.Result().Cookies()[0])
	var otherWebRecords []coord.IntegrationRecord
	if otherWebList.Code != http.StatusOK || json.Unmarshal(otherWebList.Body.Bytes(), &otherWebRecords) != nil || len(otherWebRecords) != 0 {
		t.Fatalf("integration UI leaked records across projects: %d %s", otherWebList.Code, otherWebList.Body.String())
	}
}

func TestMCPIntegrationRecordsDivergentSameBranchCheckoutsAtCombinedRevision(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	s, _, project, owner, worker := setup(t)
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "integration")
	bare := filepath.Join(filepath.Dir(root), "origin.git")
	wa, wb := filepath.Join(filepath.Dir(root), "worker-a"), filepath.Join(filepath.Dir(root), "worker-b")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	runGit := func(path string, args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", path}, args...)...)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s (%v)", args, output, err)
		}
		return strings.TrimSpace(string(output))
	}
	runGit(root, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(root, "add", "base.txt")
	runGit(root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "base")
	base := runGit(root, "rev-parse", "HEAD")
	if output, err := exec.Command("git", "clone", "--bare", root, bare).CombinedOutput(); err != nil {
		t.Fatalf("create bare origin: %s (%v)", output, err)
	}
	for _, path := range []string{wa, wb} {
		if output, err := exec.Command("git", "clone", bare, path).CombinedOutput(); err != nil {
			t.Fatalf("clone shared branch into %s: %s (%v)", path, output, err)
		}
	}
	for _, item := range []struct{ path, file, branch string }{{wa, "a.txt", "a"}, {wb, "b.txt", "b"}} {
		if err := os.WriteFile(filepath.Join(item.path, item.file), []byte(item.branch+" change\n"), 0600); err != nil {
			t.Fatal(err)
		}
		runGit(item.path, "add", item.file)
		runGit(item.path, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "change "+item.branch)
	}
	repo, err := s.CreateRepository(ctx, project.ID, owner.Token, "shared repo")
	if err != nil {
		t.Fatal(err)
	}
	workspaceTool := coord.MCPServer(s, project.ID, worker.Token).GetTool("gossip_workspace")
	register := func(name, path string) coord.Workspace {
		t.Helper()
		result, callErr := workspaceTool.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_workspace", Arguments: map[string]any{"operation": "register_git", "repository_id": repo.ID, "name": name, "local_path": path}}})
		if callErr != nil || result.IsError {
			t.Fatalf("register %s: %+v %v", name, result, callErr)
		}
		var workspace coord.Workspace
		if callErr = json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &workspace); callErr != nil {
			t.Fatal(callErr)
		}
		return workspace
	}
	baseWS, aWS, bWS := register("integrator", root), register("worker a", wa), register("worker b", wb)
	var candidates []string
	for _, source := range []struct {
		workspace coord.Workspace
		branch    string
	}{{aWS, "worker-a"}, {bWS, "worker-b"}} {
		task, e := s.CreateTask(ctx, project.ID, owner.Token, 1, "Implement change", "Create a source candidate", "implementation", coord.ReportContract{ReviewType: "full_code"}, nil)
		if e != nil {
			t.Fatal(e)
		}
		claim, e := s.ClaimTaskInWorkspace(ctx, project.ID, worker.Token, task.ID, source.workspace.ID, time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		candidate, e := s.SaveCandidate(ctx, project.ID, worker.Token, task.ID, claim.Attempt, source.workspace.Head, "change from "+source.branch)
		if e != nil {
			t.Fatal(e)
		}
		candidates = append(candidates, candidate.ID)
	}
	integrationTask, err := s.CreateTask(ctx, project.ID, owner.Token, 1, "Integrate changes", "Merge parallel worktrees", "integration", coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AssignTask(ctx, project.ID, owner.Token, integrationTask.ID, worker.PrincipalID); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimTaskInWorkspace(ctx, project.ID, worker.Token, integrationTask.ID, baseWS.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.GrantPermission(ctx, project.ID, owner.Token, worker.PrincipalID, coord.PermissionIntegration); err != nil {
		t.Fatal(err)
	}
	runGit(root, "fetch", wa, "main:refs/remotes/worker-a/main")
	runGit(root, "fetch", wb, "main:refs/remotes/worker-b/main")
	runGit(root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "merge", "--no-edit", "refs/remotes/worker-a/main")
	runGit(root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "merge", "--no-edit", "refs/remotes/worker-b/main")
	combined := runGit(root, "rev-parse", "HEAD")
	if combined == base {
		t.Fatal("integration merge did not advance beyond the shared base")
	}
	checks, _ := json.Marshal([]coord.IntegrationCheck{{Name: "worktree clean", Command: "git status --porcelain", Passed: true, Revision: combined, Evidence: "no output"}})
	integrationTool := coord.MCPServer(s, project.ID, worker.Token).GetTool("gossip_integration")
	result, err := integrationTool.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_integration", Arguments: map[string]any{"operation": "record", "task_id": integrationTask.ID, "attempt": claim.Attempt, "workspace_id": baseWS.ID, "local_path": root, "base_revision": base, "combined_revision": combined, "candidate_ids": candidates, "checks_json": string(checks), "summary": "Both independent branch changes are present in the merge commit.", "request_id": "merged-worktrees"}}})
	if err != nil || result.IsError {
		t.Fatalf("record integration: %+v %v", result, err)
	}
	var record coord.IntegrationRecord
	if err = json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &record); err != nil {
		t.Fatal(err)
	}
	if !record.Passed || record.CombinedRevision != combined || record.EvidenceTrust != "host_reported" || len(record.CandidateIDs) != 2 {
		t.Fatalf("integration provenance: %+v", record)
	}
	if strings.Contains(result.Content[0].(mcp.TextContent).Text, root) {
		t.Fatal("local checkout path was disclosed in integration result")
	}
	workspaces, err := s.ListWorkspaces(ctx, project.ID, worker.Token)
	if err != nil {
		t.Fatal(err)
	}
	updated := false
	for _, workspace := range workspaces {
		if workspace.ID == baseWS.ID && workspace.Head == combined && !workspace.Dirty {
			updated = true
		}
	}
	if !updated {
		t.Fatalf("combined checkout was not recorded: %+v", workspaces)
	}
}
