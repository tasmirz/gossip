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

	"github.com/tasmirz/gossip/pkg/coord"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestInspectGitWorktrees(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	other := filepath.Join(root, "other")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
	}
	run(repo, "init", "--initial-branch=main")
	run(repo, "remote", "add", "origin", "https://agent:secret@example.com/org/repo.git?token=hidden")
	unborn, err := coord.InspectGitWorkspace(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if unborn.Branch != "main" || unborn.Head != "" || unborn.Dirty || len(unborn.RemoteHints) != 1 || unborn.RemoteHints[0] != "example.com/org/repo" {
		t.Fatalf("unborn: %+v", unborn)
	}
	if err = os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a"), 0600); err != nil {
		t.Fatal(err)
	}
	run(repo, "add", "a.txt")
	run(repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "initial")
	run(repo, "worktree", "add", "--detach", other)
	t.Setenv("GIT_DIR", filepath.Join(root, "incorrect"))
	main, err := coord.InspectGitWorkspace(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	detached, err := coord.InspectGitWorkspace(context.Background(), other)
	if err != nil {
		t.Fatal(err)
	}
	if main.Branch != "main" || main.Head == "" || main.Dirty || detached.Branch != "" || detached.Head != main.Head || detached.CommonDirFingerprint != main.CommonDirFingerprint || detached.Root == main.Root {
		t.Fatalf("worktrees: %+v %+v", main, detached)
	}
	if err = os.WriteFile(filepath.Join(other, "untracked.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	detached, err = coord.InspectGitWorkspace(context.Background(), other)
	if err != nil || !detached.Dirty {
		t.Fatalf("dirty: %+v %v", detached, err)
	}
	if _, err = coord.InspectGitWorkspace(context.Background(), root); err == nil {
		t.Fatal("non-repository accepted")
	}
	s, _, project, owner, worker := setup(t)
	repository, err := s.CreateRepository(context.Background(), project.ID, owner.Token, "repository")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AddRepositoryRemoteHint(context.Background(), project.ID, owner.Token, repository.ID, "git@example.com:org/repo.git"); err != nil {
		t.Fatal(err)
	}
	tool := coord.MCPServer(s, project.ID, worker.Token).GetTool("gossip_workspace")
	recognized, err := tool.Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_workspace", Arguments: map[string]any{"operation": "recognize_git", "local_path": other}}})
	if err != nil || recognized.IsError {
		t.Fatalf("recognize observed workspace: %+v %v", recognized, err)
	}
	var suggestion struct {
		Observation  coord.GitObservation `json:"observation"`
		Repositories []coord.Repository   `json:"repositories"`
	}
	if err = json.Unmarshal([]byte(recognized.Content[0].(mcp.TextContent).Text), &suggestion); err != nil {
		t.Fatal(err)
	}
	if len(suggestion.Repositories) != 1 || suggestion.Repositories[0].ID != repository.ID || len(suggestion.Observation.RemoteHints) != 1 || suggestion.Observation.RemoteHints[0] != "example.com/org/repo" {
		t.Fatalf("unexpected suggestion: %+v", suggestion)
	}
	if workspaces, err := s.ListWorkspaces(context.Background(), project.ID, worker.Token); err != nil || len(workspaces) != 0 {
		t.Fatalf("recognition registered a workspace: %+v %v", workspaces, err)
	}
	res, err := tool.Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_workspace", Arguments: map[string]any{"operation": "register_git", "repository_id": repository.ID, "name": "worker", "local_path": other}}})
	if err != nil || res.IsError {
		t.Fatalf("register observed workspace: %+v %v", res, err)
	}
	var workspace coord.Workspace
	if err = json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &workspace); err != nil {
		t.Fatal(err)
	}
	if workspace.Head != main.Head || !workspace.Dirty || workspace.LocalPath != detached.Root {
		t.Fatalf("registered metadata: %+v", workspace)
	}
	if err = os.Remove(filepath.Join(other, "untracked.txt")); err != nil {
		t.Fatal(err)
	}
	task := create(t, s, project, owner, nil)
	claimTool := coord.MCPServer(s, project.ID, worker.Token).GetTool("gossip_task")
	claimed, err := claimTool.Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_task", Arguments: map[string]any{"operation": "claim", "task_id": task.ID, "workspace_id": workspace.ID}}})
	if err != nil || claimed.IsError {
		t.Fatalf("claim with refreshed observation: %+v %v", claimed, err)
	}
	var claimedTask coord.Task
	if err = json.Unmarshal([]byte(claimed.Content[0].(mcp.TextContent).Text), &claimedTask); err != nil {
		t.Fatal(err)
	}
	attempt, err := s.GetAttempt(context.Background(), project.ID, owner.Token, task.ID, claimedTask.Attempt)
	if err != nil || attempt.WorkspaceDirty || attempt.WorkspaceHead != main.Head {
		t.Fatalf("claim did not refresh workspace: %+v %v", attempt, err)
	}
}

func TestCaptureGitDiffIncludesTrackedAndUntrackedChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, output, err)
		}
	}
	run("init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(repo, "tracked.go"), []byte("package example\nconst Value = 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", "tracked.go")
	run("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "baseline")
	baseline, err := coord.InspectGitWorkspace(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(repo, "tracked.go"), []byte("package example\nconst Value = 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(repo, "new.go"), []byte("package example\nconst Added = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "not-the-repo"))
	diff, err := coord.CaptureGitDiff(context.Background(), repo, baseline.Head)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"diff --git a/tracked.go b/tracked.go", "-const Value = 1", "+const Value = 2", "new.go", "+const Added = true"} {
		if !strings.Contains(diff, expected) {
			t.Errorf("captured diff is missing %q:\n%s", expected, diff)
		}
	}
	if _, err = coord.CaptureGitDiff(context.Background(), repo, "not-a-commit"); err == nil {
		t.Fatal("accepted a non-commit baseline")
	}
}

func TestMCPFullCodeCaptureUsesAttemptWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	s, _, project, owner, worker := setup(t)
	ctx := context.Background()
	repoPath := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(repoPath, 0700); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repoPath}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, output, err)
		}
	}
	run("init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(repoPath, "main.go"), []byte("package example\nconst Value = 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", "main.go")
	run("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "baseline")
	observation, err := coord.InspectGitWorkspace(ctx, repoPath)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := s.CreateRepository(ctx, project.ID, owner.Token, "repo")
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := s.RegisterWorkspace(ctx, project.ID, worker.Token, repository.ID, "worker", "test-node", observation.Root, observation.Branch, observation.Head, false)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTask(ctx, project.ID, owner.Token, 1, "Review source", "Review complete worktree diff", "implementation", coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AcknowledgePlan(ctx, project.ID, worker.Token, 1); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimObservedWorkspace(ctx, project.ID, worker.Token, task.ID, workspace.ID, observation, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(repoPath, "main.go"), []byte("package example\nconst Value = 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(repoPath, "new.go"), []byte("package example\nconst Added = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chdir(repoPath); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if restoreErr := os.Chdir(workingDirectory); restoreErr != nil {
			t.Errorf("restore working directory: %v", restoreErr)
		}
	}()
	tool := coord.MCPServer(s, project.ID, worker.Token).GetTool("gossip_submit")
	result, err := tool.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_submit", Arguments: map[string]any{"operation": "capture_diff", "task_id": task.ID, "attempt": claimed.Attempt, "request_id": "capture-full-code"}}})
	if err != nil || result.IsError {
		t.Fatalf("capture diff: %+v %v", result, err)
	}
	var candidate coord.CandidateReport
	if err = json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &candidate); err != nil || candidate.ID == "" || candidate.Digest == "" || candidate.Body != "" {
		t.Fatalf("capture result did not return candidate metadata only: %+v %v", candidate, err)
	}
	stored, err := s.GetCandidate(ctx, project.ID, owner.Token, candidate.ID)
	if err != nil || !strings.Contains(stored.Body, "-const Value = 1") || !strings.Contains(stored.Body, "+const Value = 2") || !strings.Contains(stored.Body, "new.go") {
		t.Fatalf("stored source candidate is incomplete: %+v %v", stored, err)
	}
	if err = s.RequestReview(ctx, project.ID, worker.Token, task.ID, claimed.Attempt); err != nil {
		t.Fatalf("orchestrator could not receive captured full-code review: %v", err)
	}
	wrongWorkspace := filepath.Join(t.TempDir(), "other")
	if err = os.Mkdir(wrongWorkspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Chdir(wrongWorkspace); err != nil {
		t.Fatal(err)
	}
	wrongResult, callErr := tool.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_submit", Arguments: map[string]any{"operation": "capture_diff", "task_id": task.ID, "attempt": claimed.Attempt, "request_id": "capture-other-path"}}})
	if callErr != nil || !wrongResult.IsError {
		t.Fatalf("capture accepted a workspace outside the MCP process checkout: %+v %v", wrongResult, callErr)
	}
}
