package coord_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/tasmirz/gossip/pkg/coord"
)

type unavailableReviewBackend struct {
	coord.Backend
	offline atomic.Bool
}

func (b *unavailableReviewBackend) RequestReview(ctx context.Context, project, token, task string, attempt int) error {
	if b.offline.Load() {
		return coord.ErrUnavailable
	}
	return b.Backend.RequestReview(ctx, project, token, task, attempt)
}

func TestReviewRequestWaitsForRemoteAndDeliversOnce(t *testing.T) {
	store, _, project, owner, worker := setup(t)
	ctx := context.Background()
	task, err := store.CreateTask(ctx, project.ID, owner.Token, 1, "Review", "Request full-code review", "implementation", coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimTask(ctx, project.ID, worker.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	backend := &unavailableReviewBackend{Backend: store}
	backend.offline.Store(true)
	path := filepath.Join(t.TempDir(), "review-outbox.db")
	open := func() *coord.Outbox {
		t.Helper()
		o, openErr := coord.OpenOutbox(path, "https://home", project.ID, worker.PrincipalID, worker.Token, backend)
		if openErr != nil {
			t.Fatal(openErr)
		}
		return o
	}
	box := open()
	adapter := coord.NewMCPAdapterWithOutbox(backend, project.ID, worker.Token, box)
	codeDiff := "diff --git a/pkg/review.go b/pkg/review.go\n--- a/pkg/review.go\n+++ b/pkg/review.go\n@@ -1 +1 @@\n-old\n+new"
	candidateResult, err := adapter.Server.GetTool("gossip_submit").Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_submit", Arguments: map[string]any{"operation": "enqueue_candidate", "task_id": task.ID, "attempt": claim.Attempt, "baseline": "commit:abc", "body": codeDiff, "request_id": "review-source-1"}}})
	if err != nil || candidateResult.IsError {
		t.Fatalf("queue offline source diff: %+v %v", candidateResult, err)
	}
	result, err := adapter.Server.GetTool("gossip_task").Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_task", Arguments: map[string]any{"operation": "request_review", "task_id": task.ID, "attempt": claim.Attempt}}})
	if err != nil || result.IsError {
		t.Fatalf("queue offline review request: %+v %v", result, err)
	}
	var queued map[string]any
	if err = json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &queued); err != nil || queued["queued_for_delivery"] != true {
		t.Fatalf("review request was not reported as queued: %+v %v", queued, err)
	}
	adapter.Close()
	box = open()
	backend.offline.Store(false)
	defer box.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		items, listErr := box.ListReviewQueue(ctx, 10)
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(items) == 1 && items[0].Status == "delivered" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	items, err := box.ListReviewQueue(ctx, 10)
	if err != nil || len(items) != 1 || items[0].Status != "delivered" {
		t.Fatalf("review request was not delivered after reconnect: %+v %v", items, err)
	}
	candidates, err := box.ListCandidateQueue(ctx, 10)
	if err != nil || len(candidates) != 1 || candidates[0].Status != "delivered" || candidates[0].CandidateID == "" {
		t.Fatalf("source diff was not delivered before its review request completed: %+v %v", candidates, err)
	}
	if err = store.RequestReview(ctx, project.ID, worker.Token, task.ID, claim.Attempt); err != nil {
		t.Fatalf("idempotent repeat after uncertain delivery failed: %v", err)
	}
	events, err := store.ReadEvents(ctx, project.ID, owner.Token, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Kind == "review.requested" && event.EntityID == task.ID {
			count++
			var payload map[string]any
			if json.Unmarshal(event.Payload, &payload) != nil || payload["review_type"] != "full_code" || payload["candidate_id"] != candidates[0].CandidateID {
				t.Fatalf("planned review type or pinned source diff was lost in delivery: %s", event.Payload)
			}
		}
	}
	if count != 1 {
		t.Fatalf("expected one durable review notification, got %d", count)
	}
	if _, err = box.EnqueueReview(ctx, task.ID, claim.Attempt); err != nil {
		t.Fatalf("duplicate queue insertion was not idempotent: %v", err)
	}
	if err = box.RetryReview(ctx, "missing"); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("retry of missing queue item: %v", err)
	}
}

func TestQueuedReviewCannotCrossProjectRevocationDuringPartition(t *testing.T) {
	store, _, project, owner, worker := setup(t)
	ctx := context.Background()
	task, err := store.CreateTask(ctx, project.ID, owner.Token, 1, "Review", "Request a full source review", "implementation", coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimTask(ctx, project.ID, worker.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	backend := &unavailableReviewBackend{Backend: store}
	backend.offline.Store(true)
	path := filepath.Join(t.TempDir(), "revoked-review-outbox.db")
	box, err := coord.OpenOutbox(path, "https://home", project.ID, worker.PrincipalID, worker.Token, backend)
	if err != nil {
		t.Fatal(err)
	}
	adapter := coord.NewMCPAdapterWithOutbox(backend, project.ID, worker.Token, box)
	result, err := adapter.Server.GetTool("gossip_task").Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_task", Arguments: map[string]any{"operation": "request_review", "task_id": task.ID, "attempt": claim.Attempt}}})
	if err != nil || result.IsError {
		adapter.Close()
		t.Fatalf("queue offline review request: %+v %v", result, err)
	}
	adapter.Close()
	if err = store.RevokePrincipal(ctx, project.ID, owner.Token, worker.PrincipalID); err != nil {
		t.Fatal(err)
	}
	backend.offline.Store(false)
	box, err = coord.OpenOutbox(path, "https://home", project.ID, worker.PrincipalID, worker.Token, backend)
	if err != nil {
		t.Fatal(err)
	}
	defer box.Close()
	deadline := time.Now().Add(5 * time.Second)
	var items []coord.QueuedReviewRequest
	for time.Now().Before(deadline) {
		items, err = box.ListReviewQueue(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) == 1 && items[0].Status == "failed" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if len(items) != 1 || items[0].Status != "failed" || items[0].Error == "" {
		t.Fatalf("revoked review request was not retained as a failed item: %+v", items)
	}
	events, err := store.ReadEvents(ctx, project.ID, owner.Token, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == "review.requested" && event.EntityID == task.ID {
			t.Fatal("revoked worker's pending request reached the project")
		}
	}
}
