package coord_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tasmirz/gossip/pkg/coord"
)

type integrationSender struct {
	coord.Backend
	offline atomic.Bool
	lost    atomic.Bool
}

func (b *integrationSender) RecordIntegration(ctx context.Context, project, token string, submission coord.IntegrationSubmission) (coord.IntegrationRecord, error) {
	if b.offline.Load() {
		return coord.IntegrationRecord{}, coord.ErrUnavailable
	}
	record, err := b.Backend.RecordIntegration(ctx, project, token, submission)
	if err == nil && b.lost.Swap(false) {
		return coord.IntegrationRecord{}, coord.ErrUnavailable
	}
	return record, err
}

func TestIntegrationOutboxSurvivesDisconnectAndLostResponse(t *testing.T) {
	store, _, project, owner, worker := setup(t)
	ctx := context.Background()
	repo, err := store.CreateRepository(ctx, project.ID, owner.Token, "repo")
	if err != nil {
		t.Fatal(err)
	}
	base, combined := strings.Repeat("1", 40), strings.Repeat("2", 40)
	workspace, err := store.RegisterWorkspace(ctx, project.ID, worker.Token, repo.ID, "integration", "node", "/private/repo", "main", base, false)
	if err != nil {
		t.Fatal(err)
	}
	var candidateIDs []string
	for i := 0; i < 2; i++ {
		task, e := store.CreateTask(ctx, project.ID, owner.Token, 1, "Source", "Parallel change", "implementation", coord.ReportContract{ReviewType: "full_code"}, nil)
		if e != nil {
			t.Fatal(e)
		}
		claim, e := store.ClaimTaskInWorkspace(ctx, project.ID, worker.Token, task.ID, workspace.ID, time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		candidate, e := store.SaveCandidate(ctx, project.ID, worker.Token, task.ID, claim.Attempt, base, "candidate")
		if e != nil {
			t.Fatal(e)
		}
		candidateIDs = append(candidateIDs, candidate.ID)
	}
	task, err := store.CreateTask(ctx, project.ID, owner.Token, 1, "Integrate", "Combine and verify", "integration", coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.AssignTask(ctx, project.ID, owner.Token, task.ID, worker.PrincipalID); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimTaskInWorkspace(ctx, project.ID, worker.Token, task.ID, workspace.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.GrantPermission(ctx, project.ID, owner.Token, worker.PrincipalID, coord.PermissionIntegration); err != nil {
		t.Fatal(err)
	}
	backend := &integrationSender{Backend: store}
	backend.offline.Store(true)
	path := filepath.Join(t.TempDir(), "integration-outbox.db")
	open := func() *coord.Outbox {
		t.Helper()
		box, e := coord.OpenOutbox(path, "https://home", project.ID, worker.PrincipalID, worker.Token, backend)
		if e != nil {
			t.Fatal(e)
		}
		return box
	}
	submission := coord.IntegrationSubmission{TaskID: task.ID, Attempt: claim.Attempt, WorkspaceID: workspace.ID, BaseRevision: base, CombinedRevision: combined, Observation: coord.GitObservation{Branch: "main", Head: combined}, CandidateIDs: candidateIDs, Checks: []coord.IntegrationCheck{{Name: "tests", Command: "go test ./...", Passed: true, Revision: combined}}, Summary: "Combined branches and verified the merge."}
	box := open()
	queued, err := box.EnqueueIntegration(ctx, submission)
	if err != nil || queued.Status != "pending" {
		t.Fatalf("enqueue: %+v %v", queued, err)
	}
	if _, err = box.EnqueueIntegration(ctx, submission); err != nil {
		t.Fatalf("idempotent enqueue: %v", err)
	}
	changed := submission
	changed.Summary = "changed"
	if _, err = box.EnqueueIntegration(ctx, changed); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("changed request reused id: %v", err)
	}
	box.Close()
	backend.offline.Store(false)
	backend.lost.Store(true)
	box = open()
	defer box.Close()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		items, e := box.ListIntegrationQueue(ctx, 10)
		if e != nil {
			t.Fatal(e)
		}
		if len(items) == 1 && items[0].Status == "delivered" {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	items, err := box.ListIntegrationQueue(ctx, 10)
	if err != nil || len(items) != 1 || items[0].Status != "delivered" || items[0].IntegrationID == "" {
		t.Fatalf("replayed integration: %+v %v", items, err)
	}
	integrations, err := store.ListIntegrations(ctx, project.ID, owner.Token, 20)
	if err != nil || len(integrations) != 1 || integrations[0].ID != items[0].IntegrationID {
		t.Fatalf("duplicate or missing integration: %+v %v", integrations, err)
	}
}
