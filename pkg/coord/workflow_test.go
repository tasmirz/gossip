package coord_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tasmirz/gossip/pkg/coord"
)

func TestTwoAgentsSharePlanChatAndRequestBothReviewTypesAcrossWorktrees(t *testing.T) {
	store, _, project, owner, workerA := setup(t)
	ctx := context.Background()
	workerB, err := store.AddPrincipal(ctx, project.ID, owner.Token, "Worker B", "worker")
	if err != nil {
		t.Fatal(err)
	}
	repository, err := store.CreateRepository(ctx, project.ID, owner.Token, "shared repository")
	if err != nil {
		t.Fatal(err)
	}
	workspaceA, err := store.RegisterWorkspace(ctx, project.ID, workerA.Token, repository.ID, "worker A checkout", "machine-a", "/worktrees/agent-a", "feature/a", "commit-a", false)
	if err != nil {
		t.Fatal(err)
	}
	workspaceB, err := store.RegisterWorkspace(ctx, project.ID, workerB.Token, repository.ID, "worker B checkout", "machine-b", "/worktrees/agent-b", "feature/b", "commit-b", true)
	if err != nil {
		t.Fatal(err)
	}
	visibleToB, err := store.ListWorkspaces(ctx, project.ID, workerB.Token)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{}
	for _, workspace := range visibleToB {
		paths[workspace.ID] = workspace.LocalPath
	}
	if paths[workspaceB.ID] != workspaceB.LocalPath || paths[workspaceA.ID] != "" {
		t.Fatalf("workspace paths were not owner-private: worker A=%q worker B=%q", paths[workspaceA.ID], paths[workspaceB.ID])
	}

	message, err := store.SendMessageWithID(ctx, project.ID, workerA.Token, workerB.PrincipalID, "shared-plan", "Parallel work", "I will inspect the API boundary; please map the UI flow.", "handoff-a")
	if err != nil {
		t.Fatal(err)
	}
	messagesB, err := store.RecentMessages(ctx, project.ID, workerB.Token, 20)
	if err != nil || len(messagesB) != 1 || messagesB[0].ID != message.ID {
		t.Fatalf("direct handoff not visible to recipient: %+v %v", messagesB, err)
	}
	messagesOwner, err := store.RecentMessages(ctx, project.ID, owner.Token, 20)
	if err != nil || len(messagesOwner) != 0 {
		t.Fatalf("direct handoff leaked to unrelated project owner: %+v %v", messagesOwner, err)
	}
	if _, err = store.AcknowledgeMessage(ctx, project.ID, workerB.Token, message.ID); err != nil {
		t.Fatal(err)
	}
	acks, err := store.MessageAcknowledgments(ctx, project.ID, workerA.Token, message.ID)
	if err != nil || len(acks) != 1 || acks[0].PrincipalID != workerB.PrincipalID {
		t.Fatalf("recipient acknowledgment missing: %+v %v", acks, err)
	}

	fullCodeTask, err := store.CreateTask(ctx, project.ID, owner.Token, 1, "Review API change", "Inspect the complete worker diff", "implementation", coord.ReportContract{ReviewType: "full_code", Formats: []string{"markdown"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	diagramTask, err := store.CreateTask(ctx, project.ID, owner.Token, 1, "Map UI flow", "Return detailed Markdown with class and sequence diagrams", "research", coord.ReportContract{ReviewType: "diagram_report", Formats: []string{"markdown", "mermaid_class", "mermaid_sequence"}, Coverage: []string{"authorization", "retry"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, assignment := range []struct {
		task   coord.Task
		worker coord.Credential
	}{{fullCodeTask, workerA}, {diagramTask, workerB}} {
		if _, err = store.AssignTask(ctx, project.ID, owner.Token, assignment.task.ID, assignment.worker.PrincipalID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = store.ClaimTaskInWorkspace(ctx, project.ID, workerB.Token, diagramTask.ID, workspaceB.ID, time.Minute); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("worker without current plan acknowledgment claimed work: %v", err)
	}
	if err = store.AcknowledgePlan(ctx, project.ID, workerB.Token, 1); err != nil {
		t.Fatal(err)
	}
	claimA, err := store.ClaimTaskInWorkspace(ctx, project.ID, workerA.Token, fullCodeTask.ID, workspaceA.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	claimB, err := store.ClaimTaskInWorkspace(ctx, project.ID, workerB.Token, diagramTask.ID, workspaceB.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	candidateDiff := "diff --git a/api.go b/api.go\n--- a/api.go\n+++ b/api.go\n@@ -1 +1 @@\n-old\n+new\n"
	candidate, err := store.SaveCandidate(ctx, project.ID, workerA.Token, fullCodeTask.ID, claimA.Attempt, "commit-a", candidateDiff)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RequestReview(ctx, project.ID, workerA.Token, fullCodeTask.ID, claimA.Attempt); err != nil {
		t.Fatal(err)
	}
	if err = store.RequestReview(ctx, project.ID, workerB.Token, diagramTask.ID, claimB.Attempt); err != nil {
		t.Fatal(err)
	}

	diagramReport := "# UI review\n\n## Review summary\nThe UI work can proceed with the current project boundary.\n\n## Scope and evidence\nThe authorization and retry paths were checked at the recorded baseline.\n\n## UML class diagram\nThe class relationship shows the browser delegating to the project service.\n\n```mermaid\nclassDiagram\n  Browser --> ProjectService\n```\n\n## Sequence diagram\nThe sequence covers project authorization followed by retry handling.\n\n```mermaid\nsequenceDiagram\n  Browser->>ProjectService: authorization\n  ProjectService-->>Browser: retry result\n```\n\n## Findings\nThe owner retains project control while each worker uses its own workspace.\n\n## Coverage and limitations\nAuthorization and retry are covered; no live deployment trace was captured."
	reportA, err := store.SubmitReport(ctx, project.ID, workerA.Token, fullCodeTask.ID, claimA.Attempt, "commit-a", "Reviewed the full source diff and found no blocking issue.")
	if err != nil {
		t.Fatal(err)
	}
	reportB, err := store.SubmitReport(ctx, project.ID, workerB.Token, diagramTask.ID, claimB.Attempt, "commit-b", diagramReport)
	if err != nil {
		t.Fatal(err)
	}
	if reportA.SourceCandidateID != candidate.ID || reportA.SourceCandidateDigest != candidate.Digest || !strings.Contains(reportB.Body, "sequenceDiagram") {
		t.Fatalf("review artifacts lost their planned evidence: full-code=%+v diagram=%+v", reportA, reportB)
	}
	if err = store.AcceptReport(ctx, project.ID, owner.Token, reportA.ID, reportA.Digest); err != nil {
		t.Fatal(err)
	}
	if err = store.AcceptReport(ctx, project.ID, owner.Token, reportB.ID, reportB.Digest); err != nil {
		t.Fatal(err)
	}

	tasks, err := store.ListTasks(ctx, project.ID, owner.Token, 20)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("expected both completed tasks: %+v %v", tasks, err)
	}
	for _, task := range tasks {
		if task.Status != "accepted" {
			t.Errorf("task %s ended in %s", task.ID, task.Status)
		}
	}

	events, err := store.ReadEvents(ctx, project.ID, owner.Token, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	requested := map[string]bool{}
	for _, event := range events {
		if event.Kind != "review.requested" {
			continue
		}
		var payload struct {
			ReviewType      string `json:"review_type"`
			CandidateID     string `json:"candidate_id"`
			CandidateDigest string `json:"candidate_digest"`
		}
		if err = json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.ReviewType == "full_code" {
			requested[payload.ReviewType] = payload.CandidateID == candidate.ID && payload.CandidateDigest == candidate.Digest
		} else {
			requested[payload.ReviewType] = true
		}
	}
	if !requested["full_code"] || !requested["diagram_report"] {
		t.Fatalf("orchestrator did not receive both planned review requests: %+v", requested)
	}
}

func TestTaskRequiredCapabilitiesNeedOneLiveAuthenticatedSession(t *testing.T) {
	store, _, project, owner, worker := setup(t)
	ctx := context.Background()
	task, err := store.CreateTask(ctx, project.ID, owner.Token, 1, "Inspect Git state", "Check the workspace before editing", "research", coord.ReportContract{
		ReviewType: "full_code", RequiredCapabilities: []string{"git_workspace_inspection", "lease_renewal"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ClaimTask(ctx, project.ID, worker.Token, task.ID, time.Minute); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("claim without reported capabilities = %v, want conflict", err)
	}
	if err = store.HeartbeatAgentWithCapabilities(ctx, project.ID, worker.Token, "session-one", []coord.AgentCapability{{Name: "git_workspace_inspection", Source: "adapter"}}); err != nil {
		t.Fatal(err)
	}
	if err = store.HeartbeatAgentWithCapabilities(ctx, project.ID, worker.Token, "session-two", []coord.AgentCapability{{Name: "lease_renewal", Source: "adapter"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ClaimTask(ctx, project.ID, worker.Token, task.ID, time.Minute); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("capabilities from separate sessions were combined: %v", err)
	}
	if err = store.HeartbeatAgentWithCapabilities(ctx, project.ID, worker.Token, "session-one", []coord.AgentCapability{{Name: "git_workspace_inspection", Source: "adapter"}, {Name: "lease_renewal", Source: "adapter"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ClaimTask(ctx, project.ID, worker.Token, task.ID, time.Minute); err != nil {
		t.Fatalf("claim with one live capable session: %v", err)
	}
	endedSessionTask, err := store.CreateTask(ctx, project.ID, owner.Token, 1, "Check session expiry", "Ended sessions must not satisfy capability gates", "verification", coord.ReportContract{
		ReviewType: "full_code", RequiredCapabilities: []string{"git_workspace_inspection", "lease_renewal"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.EndAgentSession(ctx, project.ID, worker.Token, "session-one"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ClaimTask(ctx, project.ID, worker.Token, endedSessionTask.ID, time.Minute); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("ended session still satisfied required capabilities: %v", err)
	}
}

func TestTaskRejectsUnknownRequiredCapability(t *testing.T) {
	store, _, project, owner, _ := setup(t)
	_, err := store.CreateTask(context.Background(), project.ID, owner.Token, 1, "Invalid", "Unknown capability", "research", coord.ReportContract{
		ReviewType: "full_code", RequiredCapabilities: []string{"filesystem.root_access"},
	}, nil)
	if err == nil {
		t.Fatal("task accepted an unsupported required capability")
	}
}

func TestTaskRequiredRoleBlocksClaimUntilOwnerAssignsMatchingDynamicRole(t *testing.T) {
	store, _, project, owner, worker := setup(t)
	ctx := context.Background()
	task, err := store.CreateTask(ctx, project.ID, owner.Token, 1, "Map the interface", "Return a concise API map", "research", coord.ReportContract{ReviewType: "diagram_report", Formats: []string{"markdown", "mermaid_sequence", "mermaid_class"}, RequiredRole: "api researcher"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ClaimTask(ctx, project.ID, worker.Token, task.ID, time.Minute); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("worker without matching owner-assigned role claimed task: %v", err)
	}
	if _, err = store.AssignTask(ctx, project.ID, owner.Token, task.ID, worker.PrincipalID); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("owner assigned constrained task to a worker without its required role: %v", err)
	}
	role, err := store.SetWorkRole(ctx, project.ID, owner.Token, worker.PrincipalID, "", "API Researcher", "Map endpoints and data contracts", 0, 0)
	if err != nil || !role.Active {
		t.Fatalf("owner could not assign project role: %+v %v", role, err)
	}
	if _, err = store.AssignTask(ctx, project.ID, owner.Token, task.ID, worker.PrincipalID); err != nil {
		t.Fatalf("owner could not assign task after granting matching role: %v", err)
	}
	claimed, err := store.ClaimTask(ctx, project.ID, worker.Token, task.ID, time.Minute)
	if err != nil || claimed.Owner != worker.PrincipalID {
		t.Fatalf("matching dynamic role did not make task claimable: %+v %v", claimed, err)
	}

	other, err := store.CreateTask(ctx, project.ID, owner.Token, 1, "Implement endpoint", "Add the planned endpoint", "implementation", coord.ReportContract{ReviewType: "full_code", RequiredRole: "implementer"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ClaimTask(ctx, project.ID, worker.Token, other.ID, time.Minute); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("role assignment leaked across role requirements: %v", err)
	}
}
