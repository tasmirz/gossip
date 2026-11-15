package coord

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestPlanCompletionSurvivesNewRevisionsAndRestart(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "coord.db")
	store, err := Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	project, owner, err := store.CreateProject(ctx, "Project", "Owner")
	if err != nil {
		t.Fatal(err)
	}
	worker, err := store.AddPrincipal(ctx, project.ID, owner.Token, "Worker", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PublishPlan(ctx, project.ID, owner.Token, "First", 0); err != nil {
		t.Fatal(err)
	}
	if err := store.CompletePlan(ctx, project.ID, worker.Token, 1); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("worker completion: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := store.CompletePlan(ctx, project.ID, owner.Token, 1); err != nil {
			t.Fatal(err)
		}
	}
	if plan, err := store.CurrentPlan(ctx, project.ID, worker.Token); err != nil || plan.Status != "done" || plan.Acknowledged {
		t.Fatalf("plan completion and acknowledgement: %+v, %v", plan, err)
	}
	for revision, body := range []string{"Second", "Third"} {
		if _, err := store.PublishPlan(ctx, project.ID, owner.Token, body, revision+1); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CompletePlan(ctx, project.ID, owner.Token, 2); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale completion: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(database)
	if err != nil {
		t.Fatal(err)
	}
	history, err := store.ListPlanHistory(ctx, project.ID, worker.Token)
	if err != nil || len(history) != 3 || history[0].Status != "active" || history[1].Status != "superseded" || history[2].Status != "done" {
		t.Fatalf("history after restart: %+v, %v", history, err)
	}
	events, err := store.RecentEvents(ctx, project.ID, owner.Token, 20)
	if err != nil {
		t.Fatal(err)
	}
	var completions int
	for _, event := range events {
		if event.Kind == "plan.completed" {
			completions++
		}
	}
	if completions != 1 {
		t.Fatalf("expected one completion event, got %d", completions)
	}
}
