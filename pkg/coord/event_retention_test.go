package coord

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func TestEventRetentionPruneForcesStaleCacheReset(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "home.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, owner, err := store.CreateProject(ctx, "retention", "owner")
	if err != nil {
		t.Fatal(err)
	}
	worker, err := store.AddPrincipal(ctx, project.ID, owner.Token, "worker", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.PublishPlan(ctx, project.ID, owner.Token, "keep this plan", 0); err != nil {
		t.Fatal(err)
	}
	outbox, err := OpenOutbox(filepath.Join(t.TempDir(), "replica.db"), "https://home", project.ID, worker.PrincipalID, worker.Token, store)
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	initial, err := outbox.refreshCache(ctx, store, project.ID, worker.Token)
	if err != nil {
		t.Fatalf("initial cache refresh failed: status=%+v err=%v", initial, err)
	}
	var oldCursor int64
	if err = outbox.db.QueryRowContext(ctx, `SELECT sequence FROM cached_event_cursor WHERE id=1`).Scan(&oldCursor); err != nil {
		t.Fatal(err)
	}
	if oldCursor == 0 {
		t.Fatal("initial refresh did not cache the existing event history")
	}
	if _, err = store.SendMessage(ctx, project.ID, owner.Token, worker.PrincipalID, "", "before prune", "keep this message"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SendMessage(ctx, project.ID, owner.Token, worker.PrincipalID, "", "after cache", "retained event"); err != nil {
		t.Fatal(err)
	}
	allEvents, err := store.ReadEvents(ctx, project.ID, owner.Token, 0, 200)
	if err != nil || len(allEvents) < 2 {
		t.Fatalf("could not read pre-prune event history: %d events, %v", len(allEvents), err)
	}
	cutoff := allEvents[len(allEvents)-2].Sequence
	if _, err = store.PruneProjectEvents(ctx, project.ID, worker.Token, cutoff); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("worker was allowed to prune project history: %v", err)
	}
	pruned, err := store.PruneProjectEvents(ctx, project.ID, owner.Token, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if pruned.PrunedThrough != cutoff || pruned.PrunedCount == 0 {
		t.Fatalf("prune did not report its boundary and deletion count: %+v", pruned)
	}
	if _, err = store.PruneProjectEvents(ctx, project.ID, owner.Token, cutoff); err != nil {
		t.Fatalf("retrying the same prune boundary should be idempotent: %v", err)
	}
	statusAfterPrune, err := store.EventRetentionStatus(ctx, project.ID, owner.Token)
	if err != nil || statusAfterPrune.PrunedThrough != cutoff {
		t.Fatalf("owner could not read retention status: %+v %v", statusAfterPrune, err)
	}
	retainedEvents, err := store.ReadEvents(ctx, project.ID, owner.Token, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	foundAudit := false
	for _, event := range retainedEvents {
		if event.Sequence <= cutoff {
			t.Fatalf("event sequence %d remained below prune watermark %d", event.Sequence, cutoff)
		}
		foundAudit = foundAudit || event.Kind == "events.pruned"
	}
	if !foundAudit {
		t.Fatal("prune audit event was not retained")
	}
	status, err := outbox.refreshCache(ctx, store, project.ID, worker.Token)
	if err != nil {
		t.Fatal(err)
	}
	if !status.ResetRequired || status.HistoryPrunedThrough != cutoff {
		t.Fatalf("stale cache did not receive the prune reset watermark: %+v", status)
	}
	var cachedEvents int
	if err = outbox.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cached_events`).Scan(&cachedEvents); err != nil {
		t.Fatal(err)
	}
	if cachedEvents == 0 {
		t.Fatal("cache reset failed to repopulate retained and audit events")
	}
	var cachedPlan []byte
	if err = outbox.db.QueryRowContext(ctx, `SELECT value FROM cached_snapshots WHERE kind='plan'`).Scan(&cachedPlan); err != nil {
		t.Fatalf("cache reset discarded the project snapshot: %v", err)
	}
	var plan Plan
	if err = json.Unmarshal(cachedPlan, &plan); err != nil || plan.Body != "keep this plan" {
		t.Fatalf("cache reset did not restore the current plan: %+v %v", plan, err)
	}
	messages, err := store.ListMessages(ctx, project.ID, worker.Token, 0, 50)
	if err != nil || len(messages) != 2 {
		t.Fatalf("pruning event history changed durable project messages: %d, %v", len(messages), err)
	}
	if oldCursor >= cutoff {
		t.Fatalf("test setup did not leave the replica behind the retention watermark: old=%d cutoff=%d", oldCursor, cutoff)
	}
}
