package coord_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tasmirz/gossip/pkg/coord"
)

func setup(t *testing.T) (*coord.Store, string, coord.Project, coord.Credential, coord.Credential) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "coord.db")
	s, err := coord.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	p, owner, err := s.CreateProject(ctx, "test", "owner")
	if err != nil {
		t.Fatal(err)
	}
	worker, err := s.AddPrincipal(ctx, p.ID, owner.Token, "worker", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PublishPlan(ctx, p.ID, owner.Token, "plan", 0); err != nil {
		t.Fatal(err)
	}
	if err = s.AcknowledgePlan(ctx, p.ID, worker.Token, 1); err != nil {
		t.Fatal(err)
	}
	return s, path, p, owner, worker
}
func create(t *testing.T, s *coord.Store, p coord.Project, owner coord.Credential, deps []string) coord.Task {
	t.Helper()
	task, err := s.CreateTask(context.Background(), p.ID, owner.Token, 1, "Research", "Investigate behavior", "research", coord.ReportContract{ReviewType: "full_code", Formats: []string{"mermaid_sequence"}, ReviewMode: "report_first"}, deps)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestCreateTaskWithIDIsIdempotentAndRejectsPayloadReuse(t *testing.T) {
	s, _, project, owner, _ := setup(t)
	ctx := context.Background()
	contract := coord.ReportContract{ReviewType: "full_code", Formats: []string{"markdown"}}
	first, err := s.CreateTaskWithID(ctx, project.ID, owner.Token, "delegate-17", 1, "Inspect login", "Map the local auth flow", "research", contract, nil)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.CreateTaskWithID(ctx, project.ID, owner.Token, "delegate-17", 1, "Inspect login", "Map the local auth flow", "research", contract, nil)
	if err != nil || retry.ID != first.ID {
		t.Fatalf("idempotent retry returned another task: first=%+v retry=%+v err=%v", first, retry, err)
	}
	var group sync.WaitGroup
	results := make(chan coord.Task, 8)
	errorsFound := make(chan error, 8)
	for i := 0; i < cap(results); i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			task, callErr := s.CreateTaskWithID(ctx, project.ID, owner.Token, "delegate-17", 1, "Inspect login", "Map the local auth flow", "research", contract, nil)
			if callErr != nil {
				errorsFound <- callErr
				return
			}
			results <- task
		}()
	}
	group.Wait()
	close(results)
	close(errorsFound)
	for callErr := range errorsFound {
		t.Errorf("concurrent duplicate task request: %v", callErr)
	}
	for task := range results {
		if task.ID != first.ID {
			t.Errorf("concurrent retry returned task %s, want %s", task.ID, first.ID)
		}
	}
	if _, err = s.CreateTaskWithID(ctx, project.ID, owner.Token, "delegate-17", 1, "Different task", "Changed after uncertain response", "research", contract, nil); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("request ID accepted a different payload: %v", err)
	}
	page, err := s.SearchTasks(ctx, project.ID, owner.Token, coord.TaskFilter{Query: "Inspect login", Limit: 10})
	if err != nil || len(page.Tasks) != 1 || page.Tasks[0].ID != first.ID {
		t.Fatalf("retry duplicated the task: %+v %v", page, err)
	}
	items, err := s.ReadEvents(ctx, project.ID, owner.Token, 0, 20)
	created := 0
	for _, event := range items {
		if event.Kind == "task.created" && event.EntityID == first.ID {
			created++
		}
	}
	if err != nil || created != 1 {
		t.Fatalf("retry duplicated task.created event: count=%d err=%v", created, err)
	}
}

func TestResearchHandoffAndDependencies(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()
	root := create(t, s, p, owner, nil)
	child := create(t, s, p, owner, []string{root.ID})
	if child.Status != "blocked" {
		t.Fatalf("dependent status = %s", child.Status)
	}
	if _, err := s.ClaimTask(ctx, p.ID, worker.Token, child.ID, time.Minute); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("blocked claim: %v", err)
	}
	claim, err := s.ClaimTask(ctx, p.ID, worker.Token, root.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.SubmitReport(ctx, p.ID, worker.Token, root.ID, claim.Attempt, "commit:abc", "sequence and evidence")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AcceptReport(ctx, p.ID, worker.Token, r.ID, r.Digest); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("worker self review: %v", err)
	}
	if err = s.AcceptReport(ctx, p.ID, owner.Token, r.ID, "wrong"); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("bad digest: %v", err)
	}
	if err = s.AcceptReport(ctx, p.ID, owner.Token, r.ID, r.Digest); err != nil {
		t.Fatal(err)
	}
	child, err = s.GetTask(ctx, p.ID, worker.Token, child.ID)
	if err != nil || child.Status != "ready" {
		t.Fatalf("dependency not unblocked: %+v %v", child, err)
	}
	events, err := s.ReadEvents(ctx, p.ID, owner.Token, 0, 100)
	if err != nil || len(events) < 6 {
		t.Fatalf("events: %+v %v", events, err)
	}
	for i := 1; i < len(events); i++ {
		if events[i].Sequence <= events[i-1].Sequence {
			t.Fatal("events out of order")
		}
	}
}

func TestWorkerRequestsPlannedOrchestratorReview(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()
	task, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Review architecture", "Map control flow", "research", coord.ReportContract{ReviewType: "diagram_report", Formats: []string{"markdown", "mermaid_sequence", "mermaid_class"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateTask(ctx, p.ID, owner.Token, 1, "Invalid diagram review", "Map control flow", "research", coord.ReportContract{ReviewType: "diagram_report", Formats: []string{"markdown"}}, nil); err == nil {
		t.Fatal("diagram review accepted without required UML formats")
	}
	if _, err = s.CreateTask(ctx, p.ID, owner.Token, 1, "Unplanned review", "Choose review type first", "research", coord.ReportContract{}, nil); err == nil {
		t.Fatal("task accepted without asking for an explicit review type")
	}
	if err = s.RequestReview(ctx, p.ID, worker.Token, task.ID, 1); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("review request before claim: %v", err)
	}
	claim, err := s.ClaimTask(ctx, p.ID, worker.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RequestReview(ctx, p.ID, worker.Token, task.ID, claim.Attempt); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SubmitReport(ctx, p.ID, worker.Token, task.ID, claim.Attempt, "commit:abc", "Architecture notes without diagrams"); err == nil {
		t.Fatal("diagram review accepted a report without the required UML blocks")
	}
	emptyDiagrams := "```mermaid\nclassDiagram\n```\n```mermaid\nsequenceDiagram\n```"
	if _, err = s.SubmitReport(ctx, p.ID, worker.Token, task.ID, claim.Attempt, "commit:abc", emptyDiagrams); err == nil {
		t.Fatal("diagram review accepted empty UML blocks")
	}
	disconnectedClasses := "```mermaid\nclassDiagram\nclass Coordinator\nclass Worker\n```\n```mermaid\nsequenceDiagram\nCoordinator->>Worker: request review\n```"
	if _, err = s.SubmitReport(ctx, p.ID, worker.Token, task.ID, claim.Attempt, "commit:abc", disconnectedClasses); err == nil {
		t.Fatal("diagram review accepted classes without a UML relationship")
	}
	malformedDiagrams := "```mermaid\nclassDiagram\nclass\n```\n```mermaid\nsequenceDiagram\nA -> B\n```"
	if _, err = s.SubmitReport(ctx, p.ID, worker.Token, task.ID, claim.Attempt, "commit:abc", malformedDiagrams); err == nil {
		t.Fatal("diagram review accepted markers without valid class or sequence structures")
	}
	body := "# Architecture review\n\n## Review summary\nThe coordinator verifies project access before delegating the requested review.\n\n## Scope and evidence\nThis report traces the task request and authorization boundary at the recorded source baseline.\n\n## UML class diagram\nThe relationship below shows the coordinator assigning work to a worker.\n\n```mermaid\nclassDiagram\n  Coordinator --> Worker\n```\n\n## Sequence diagram\nThis sequence shows the review request crossing the coordinator and worker boundary.\n\n```mermaid\nsequenceDiagram\n  Coordinator->>Worker: request review\n```\n\n## Findings\nThe request preserves its planned review type after the worker claims the task.\n\n## Coverage and limitations\nThe task handoff is covered; this report does not claim a live runtime trace."
	if _, err = s.SubmitReport(ctx, p.ID, worker.Token, task.ID, claim.Attempt, "commit:abc", body); err != nil {
		t.Fatalf("valid Markdown UML report rejected: %v", err)
	}
	fullCodeTask, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Review implementation", "Inspect the full change", "review", coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fullCodeClaim, err := s.ClaimTask(ctx, p.ID, worker.Token, fullCodeTask.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RequestReview(ctx, p.ID, worker.Token, fullCodeTask.ID, fullCodeClaim.Attempt); err == nil || !strings.Contains(err.Error(), "source diff") {
		t.Fatalf("full-code review request without attached source evidence: %v", err)
	}
	if _, err = s.SaveCandidate(ctx, p.ID, worker.Token, fullCodeTask.ID, fullCodeClaim.Attempt, "commit:abc", "The implementation is complete."); err != nil {
		t.Fatal(err)
	}
	if err = s.RequestReview(ctx, p.ID, worker.Token, fullCodeTask.ID, fullCodeClaim.Attempt); err == nil || !strings.Contains(err.Error(), "unified Git diff") {
		t.Fatalf("full-code review accepted prose instead of source evidence: %v", err)
	}
	malformedDiff := "diff --git a/pkg/worker.go b/pkg/worker.go\n--- a/pkg/worker.go\n+++ b/pkg/worker.go\n@@ -1 +1 @@\n+new"
	if _, err = s.SaveCandidate(ctx, p.ID, worker.Token, fullCodeTask.ID, fullCodeClaim.Attempt, "commit:abc", malformedDiff); err != nil {
		t.Fatal(err)
	}
	if err = s.RequestReview(ctx, p.ID, worker.Token, fullCodeTask.ID, fullCodeClaim.Attempt); err == nil || !strings.Contains(err.Error(), "hunk") {
		t.Fatalf("full-code review accepted an incomplete unified diff: %v", err)
	}
	codeDiff := "diff --git a/pkg/worker.go b/pkg/worker.go\n--- a/pkg/worker.go\n+++ b/pkg/worker.go\n@@ -1 +1 @@\n-old\n+new"
	codeCandidate, err := s.SaveCandidate(ctx, p.ID, worker.Token, fullCodeTask.ID, fullCodeClaim.Attempt, "commit:abc", codeDiff)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RequestReview(ctx, p.ID, worker.Token, fullCodeTask.ID, fullCodeClaim.Attempt); err != nil {
		t.Fatal(err)
	}
	events, err := s.ReadEvents(ctx, p.ID, owner.Token, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	foundDiagram, foundFullCode := false, false
	for _, event := range events {
		if event.Kind != "review.requested" || (event.EntityID != task.ID && event.EntityID != fullCodeTask.ID) {
			continue
		}
		var payload map[string]any
		if err = json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["review_type"] == "diagram_report" && payload["attempt"] == float64(claim.Attempt) {
			foundDiagram = true
		}
		if payload["review_type"] == "full_code" && payload["attempt"] == float64(fullCodeClaim.Attempt) {
			if payload["candidate_id"] != codeCandidate.ID || payload["candidate_digest"] != codeCandidate.Digest || payload["candidate_baseline"] != codeCandidate.Baseline {
				t.Fatalf("full-code review event did not pin exact source evidence: %+v", payload)
			}
			foundFullCode = true
		}
	}
	if !foundDiagram || !foundFullCode {
		t.Fatalf("orchestrator did not receive both planned review types and attempts: diagram=%v full_code=%v", foundDiagram, foundFullCode)
	}
}

func TestConcurrentClaimsAcrossConnections(t *testing.T) {
	s, path, p, owner, worker := setup(t)
	ctx := context.Background()
	other, err := s.AddPrincipal(ctx, p.ID, owner.Token, "other", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AcknowledgePlan(ctx, p.ID, other.Token, 1); err != nil {
		t.Fatal(err)
	}
	task := create(t, s, p, owner, nil)
	s2, err := coord.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, pair := range []struct {
		s     *coord.Store
		token string
	}{{s, worker.Token}, {s2, other.Token}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := pair.s.ClaimTask(ctx, p.ID, pair.token, task.ID, time.Minute)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, coord.ErrConflict) {
			conflict++
		} else {
			t.Fatalf("unexpected claim error: %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
}

func TestPlanChangesFenceAttemptAndScopeCredentials(t *testing.T) {
	s, path, p, owner, worker := setup(t)
	ctx := context.Background()
	task := create(t, s, p, owner, nil)
	claim, err := s.ClaimTask(ctx, p.ID, worker.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	p2, owner2, err := s.CreateProject(ctx, "other", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetTask(ctx, p.ID, owner2.Token, task.ID); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("cross-project access: %v", err)
	}
	if _, err = s.PublishPlan(ctx, p.ID, owner.Token, "changed", 1); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SubmitReport(ctx, p.ID, worker.Token, task.ID, claim.Attempt, "abc", "old"); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("stale submission: %v", err)
	}
	task, err = s.ReconcileTask(ctx, p.ID, owner.Token, task.ID, 2)
	if err != nil || task.Status != "ready" {
		t.Fatalf("reconcile: %+v %v", task, err)
	}
	if _, err = s.ClaimTask(ctx, p.ID, worker.Token, task.ID, time.Minute); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("claim without acknowledging changed plan: %v", err)
	}
	if err = s.AcknowledgePlan(ctx, p.ID, worker.Token, 1); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("acknowledged stale plan: %v", err)
	}
	if err = s.AcknowledgePlan(ctx, p.ID, worker.Token, 2); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimTask(ctx, p.ID, worker.Token, task.ID, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err = s.RevokePrincipal(ctx, p.ID, owner.Token, worker.PrincipalID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetTask(ctx, p.ID, worker.Token, task.ID); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("revoked credential: %v", err)
	}
	s2, err := coord.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	plan, err := s2.CurrentPlan(ctx, p.ID, owner.Token)
	if err != nil || plan.Revision != 2 {
		t.Fatalf("restart plan: %+v %v", plan, err)
	}
	_ = p2
}

func TestDirectMessagesAndCursors(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()
	reviewer, err := s.AddPrincipal(ctx, p.ID, owner.Token, "reviewer", "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	private, err := s.SendMessage(ctx, p.ID, owner.Token, worker.PrincipalID, "", "private", "secret handoff")
	if err != nil {
		t.Fatal(err)
	}
	broadcast, err := s.SendMessage(ctx, p.ID, worker.Token, "", private.ThreadID, "update", "public handoff")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ListMessages(ctx, p.ID, reviewer.Token, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != broadcast.ID {
		t.Fatalf("reviewer saw wrong messages: %+v", got)
	}
	got, err = s.ListMessages(ctx, p.ID, worker.Token, private.Sequence, 20)
	if err != nil || len(got) != 1 || got[0].ID != broadcast.ID {
		t.Fatalf("cursor: %+v %v", got, err)
	}
	got, err = s.ListMessages(ctx, p.ID, owner.Token, 0, 20)
	if err != nil || len(got) != 2 {
		t.Fatalf("sender view: %+v %v", got, err)
	}
	if _, err = s.SendMessage(ctx, p.ID, owner.Token, "missing", "", "", "test"); !errors.Is(err, coord.ErrNotFound) {
		t.Fatalf("invalid recipient: %v", err)
	}
	events, err := s.ReadEvents(ctx, p.ID, reviewer.Token, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.EntityID == private.ID || strings.Contains(string(e.Payload), private.Body) {
			t.Fatal("private message metadata leaked through events")
		}
	}
	recent, err := s.RecentEvents(ctx, p.ID, reviewer.Token, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range recent {
		if e.EntityID == private.ID {
			t.Fatal("private message metadata leaked through recent events")
		}
	}
}

func TestMessageAcknowledgmentIsScopedAndIdempotent(t *testing.T) {
	s, path, p, owner, worker := setup(t)
	ctx := context.Background()
	reviewer, err := s.AddPrincipal(ctx, p.ID, owner.Token, "reviewer", "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.SendMessage(ctx, p.ID, owner.Token, worker.PrincipalID, "", "", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcknowledgeMessage(ctx, p.ID, reviewer.Token, m.ID); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("unauthorized ack: %v", err)
	}
	if _, err = s.MessageAcknowledgments(ctx, p.ID, reviewer.Token, m.ID); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("unauthorized receipts: %v", err)
	}
	if _, err = s.AcknowledgeMessage(ctx, p.ID, owner.Token, m.ID); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("sender acknowledged: %v", err)
	}
	first, err := s.AcknowledgeMessage(ctx, p.ID, worker.Token, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	outsiderEvents, err := s.ReadEvents(ctx, p.ID, reviewer.Token, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range outsiderEvents {
		if e.EntityID == m.ID {
			t.Fatalf("direct message acknowledgment leaked through events: %+v", e)
		}
	}
	ownerEvents, err := s.ReadEvents(ctx, p.ID, owner.Token, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ownerEvents {
		if e.EntityID == m.ID {
			if _, err := s.AcknowledgeEvents(ctx, p.ID, reviewer.Token, e.Sequence); !errors.Is(err, coord.ErrNotFound) {
				t.Fatalf("unseen direct-message event accepted as checkpoint: %v", err)
			}
		}
	}
	again, err := s.AcknowledgeMessage(ctx, p.ID, worker.Token, m.ID)
	if err != nil || first != again {
		t.Fatalf("duplicate ack: %+v %+v %v", first, again, err)
	}
	s2, err := coord.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	acks, err := s2.MessageAcknowledgments(ctx, p.ID, owner.Token, m.ID)
	if err != nil || len(acks) != 1 || acks[0] != first {
		t.Fatalf("persisted receipts: %+v %v", acks, err)
	}
	events, err := s2.ReadEvents(ctx, p.ID, owner.Token, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range events {
		if e.Kind == "message.acknowledged" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("duplicate events: %d", count)
	}
}

func TestAssignmentRestrictsClaimsAndRevocationReleasesWork(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()
	other, err := s.AddPrincipal(ctx, p.ID, owner.Token, "other", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AcknowledgePlan(ctx, p.ID, other.Token, 1); err != nil {
		t.Fatal(err)
	}
	task := create(t, s, p, owner, nil)
	if _, err = s.AssignTask(ctx, p.ID, worker.Token, task.ID, other.PrincipalID); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("worker assigned work: %v", err)
	}
	task, err = s.AssignTask(ctx, p.ID, owner.Token, task.ID, worker.PrincipalID)
	if err != nil || task.AssignedTo != worker.PrincipalID {
		t.Fatalf("assign: %+v %v", task, err)
	}
	if _, err = s.ClaimTask(ctx, p.ID, other.Token, task.ID, time.Minute); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("wrong agent claimed task: %v", err)
	}
	if _, err = s.ClaimTask(ctx, p.ID, worker.Token, task.ID, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AssignTask(ctx, p.ID, owner.Token, task.ID, other.PrincipalID); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("reassigned running task: %v", err)
	}
	if err = s.RevokePrincipal(ctx, p.ID, owner.Token, worker.PrincipalID); err != nil {
		t.Fatal(err)
	}
	task, err = s.GetTask(ctx, p.ID, owner.Token, task.ID)
	if err != nil || task.Status != "ready" || task.AssignedTo != "" {
		t.Fatalf("revocation: %+v %v", task, err)
	}
	if _, err = s.ClaimTask(ctx, p.ID, other.Token, task.ID, time.Minute); err != nil {
		t.Fatal(err)
	}
}

func TestTaskBlockerAndCancellationFenceAttempts(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()
	task := create(t, s, p, owner, nil)
	claim, err := s.ClaimTask(ctx, p.ID, worker.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.TransitionTask(ctx, p.ID, owner.Token, task.ID, "block", "wrong owner", claim.Attempt); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("other agent blocked: %v", err)
	}
	blocked, err := s.TransitionTask(ctx, p.ID, worker.Token, task.ID, "block", "Need interface", claim.Attempt)
	if err != nil || blocked.Status != "blocked" {
		t.Fatalf("block: %+v %v", blocked, err)
	}
	if _, err = s.SubmitReport(ctx, p.ID, worker.Token, task.ID, claim.Attempt, "abc", "stale"); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("blocked attempt submitted: %v", err)
	}
	if _, err = s.PublishPlan(ctx, p.ID, owner.Token, "revised", 1); err != nil {
		t.Fatal(err)
	}
	blocked, err = s.ReconcileTask(ctx, p.ID, owner.Token, task.ID, 2)
	if err != nil || blocked.Status != "blocked" {
		t.Fatalf("reconcile lost blocker: %+v %v", blocked, err)
	}
	if _, err = s.TransitionTask(ctx, p.ID, worker.Token, task.ID, "resolve", "", 0); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("worker resolved: %v", err)
	}
	ready, err := s.TransitionTask(ctx, p.ID, owner.Token, task.ID, "resolve", "interface agreed", 0)
	if err != nil || ready.Status != "ready" {
		t.Fatalf("resolve: %+v %v", ready, err)
	}
	if err = s.AcknowledgePlan(ctx, p.ID, worker.Token, 2); err != nil {
		t.Fatal(err)
	}
	claim, err = s.ClaimTask(ctx, p.ID, worker.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := s.TransitionTask(ctx, p.ID, owner.Token, task.ID, "cancel", "scope changed", 0)
	if err != nil || cancelled.Status != "cancelled" {
		t.Fatalf("cancel: %+v %v", cancelled, err)
	}
	if _, err = s.SubmitReport(ctx, p.ID, worker.Token, task.ID, claim.Attempt, "abc", "stale"); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("cancelled attempt submitted: %v", err)
	}
}

func TestExpiredClaimCannotSubmitAfterReassignment(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()
	other, err := s.AddPrincipal(ctx, p.ID, owner.Token, "second", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AcknowledgePlan(ctx, p.ID, other.Token, 1); err != nil {
		t.Fatal(err)
	}
	task := create(t, s, p, owner, nil)
	old, err := s.ClaimTask(ctx, p.ID, worker.Token, task.ID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	next, err := s.ClaimTask(ctx, p.ID, other.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if next.Attempt <= old.Attempt {
		t.Fatalf("fencing number did not advance: %d -> %d", old.Attempt, next.Attempt)
	}
	if _, err = s.SubmitReport(ctx, p.ID, worker.Token, task.ID, old.Attempt, "commit:old", "late result"); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("stale result: %v", err)
	}
	if _, err = s.SubmitReport(ctx, p.ID, other.Token, task.ID, next.Attempt, "commit:new", "current result"); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceBaselinesAndPrivacy(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()
	repo, err := s.CreateRepository(ctx, p.ID, owner.Token, "repo")
	if err != nil {
		t.Fatal(err)
	}
	ws1, err := s.RegisterWorkspace(ctx, p.ID, owner.Token, repo.ID, "owner checkout", "laptop", "/private/owner", "main", "aaa", false)
	if err != nil {
		t.Fatal(err)
	}
	ws2, err := s.RegisterWorkspace(ctx, p.ID, worker.Token, repo.ID, "worker checkout", "workstation", "/private/worker", "main", "bbb", true)
	if err != nil {
		t.Fatal(err)
	}
	spaces, err := s.ListWorkspaces(ctx, p.ID, owner.Token)
	if err != nil || len(spaces) != 2 {
		t.Fatalf("workspaces: %+v %v", spaces, err)
	}
	for _, w := range spaces {
		if w.ID == ws2.ID && w.LocalPath != "" {
			t.Fatal("other principal's path exposed")
		}
		if w.ID == ws1.ID && w.LocalPath == "" {
			t.Fatal("own path missing")
		}
	}
	task := create(t, s, p, owner, nil)
	if _, err = s.ClaimTaskInWorkspace(ctx, p.ID, worker.Token, task.ID, ws1.ID, time.Minute); !errors.Is(err, coord.ErrNotFound) {
		t.Fatalf("claimed using another principal's workspace: %v", err)
	}
	claim, err := s.ClaimTaskInWorkspace(ctx, p.ID, worker.Token, task.ID, ws2.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateWorkspace(ctx, p.ID, owner.Token, ws2.ID, "main", "ccc", false); !errors.Is(err, coord.ErrNotFound) {
		t.Fatalf("modified another principal's workspace: %v", err)
	}
	if _, err = s.UpdateWorkspace(ctx, p.ID, worker.Token, ws2.ID, "main", "ccc", false); err != nil {
		t.Fatal(err)
	}
	a, err := s.GetAttempt(ctx, p.ID, owner.Token, task.ID, claim.Attempt)
	if err != nil || a.WorkspaceID != ws2.ID || a.WorkspaceHead != "bbb" || !a.WorkspaceDirty {
		t.Fatalf("attempt baseline: %+v %v", a, err)
	}
}

func TestReportAndCandidateBaselinesMatchClaimedWorkspaceCommit(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()
	repo, err := s.CreateRepository(ctx, p.ID, owner.Token, "shared repo")
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := s.RegisterWorkspace(ctx, p.ID, worker.Token, repo.ID, "worker tree", "worker node", "/private/repo", "feature/shared-plan", "abc123", false)
	if err != nil {
		t.Fatal(err)
	}
	task := create(t, s, p, owner, nil)
	claim, err := s.ClaimTaskInWorkspace(ctx, p.ID, worker.Token, task.ID, workspace.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SubmitReport(ctx, p.ID, worker.Token, task.ID, claim.Attempt, "commit:wrong", "findings"); err == nil {
		t.Fatal("accepted report that named a different commit than the claimed workspace")
	}
	if _, err = s.SaveCandidate(ctx, p.ID, worker.Token, task.ID, claim.Attempt, "commit:wrong", "candidate findings"); err == nil {
		t.Fatal("accepted candidate that named a different commit than the claimed workspace")
	}
	if _, err = s.SubmitReport(ctx, p.ID, worker.Token, task.ID, claim.Attempt, "commit:abc123", "findings"); err != nil {
		t.Fatalf("matching workspace baseline rejected: %v", err)
	}
}

func TestProjectSettingsEnforceConcurrency(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()
	cfg, err := s.GetSettings(ctx, p.ID, owner.Token)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxParallel = 1
	cfg.MaxLeaseSeconds = 30
	cfg.SummaryTokenTarget = 500
	cfg.DefaultReviewMode = "targeted_source"
	if _, err = s.UpdateSettings(ctx, p.ID, worker.Token, cfg); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("worker changed settings: %v", err)
	}
	updated, err := s.UpdateSettings(ctx, p.ID, owner.Token, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateSettings(ctx, p.ID, owner.Token, cfg); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("stale settings update: %v", err)
	}
	if updated.Revision != cfg.Revision+1 {
		t.Fatal("settings revision did not advance")
	}
	a, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "First", "Investigate", "research", coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Contract.ReviewMode != "targeted_source" || a.Contract.SummaryTokenTarget != 500 {
		t.Fatalf("defaults not applied: %+v", a.Contract)
	}
	b := create(t, s, p, owner, nil)
	if _, err = s.ClaimTask(ctx, p.ID, worker.Token, a.ID, time.Minute); err == nil {
		t.Fatal("claimed with over-limit lease")
	}
	claim, err := s.ClaimTask(ctx, p.ID, worker.Token, a.ID, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimTask(ctx, p.ID, owner.Token, b.ID, 20*time.Second); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("parallel limit: %v", err)
	}
	if _, err = s.SubmitReport(ctx, p.ID, worker.Token, a.ID, claim.Attempt, "abc", "done"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimTask(ctx, p.ID, owner.Token, b.ID, 20*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestReworkPreservesReportAndFencesOldReview(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()
	task := create(t, s, p, owner, nil)
	attempt, err := s.ClaimTask(ctx, p.ID, worker.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	report, err := s.SubmitReport(ctx, p.ID, worker.Token, task.ID, attempt.Attempt, "abc", "first report")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RequestRework(ctx, p.ID, owner.Token, report.ID, report.Digest, ""); err == nil {
		t.Fatal("empty feedback accepted")
	}
	if err = s.RequestRework(ctx, p.ID, worker.Token, report.ID, report.Digest, "revise"); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("worker review: %v", err)
	}
	if err = s.RequestRework(ctx, p.ID, owner.Token, report.ID, report.Digest, "Add retry evidence"); err != nil {
		t.Fatal(err)
	}
	if err = s.AcceptReport(ctx, p.ID, owner.Token, report.ID, report.Digest); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("old report accepted: %v", err)
	}
	next, err := s.ClaimTask(ctx, p.ID, worker.Token, task.ID, time.Minute)
	if err != nil || next.Attempt <= attempt.Attempt {
		t.Fatalf("new attempt: %+v %v", next, err)
	}
	saved, err := s.GetReport(ctx, p.ID, worker.Token, report.ID)
	if err != nil || saved.Body != "first report" {
		t.Fatalf("original report: %+v %v", saved, err)
	}
	events, err := s.ReadEvents(ctx, p.ID, worker.Token, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Kind == "report.rework_requested" && strings.Contains(string(e.Payload), "Add retry evidence") {
			found = true
		}
	}
	if !found {
		t.Fatal("review feedback missing from event stream")
	}
}
