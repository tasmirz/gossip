package coord_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/tasmirz/gossip/pkg/coord"
)

type loseFirstReportResponse struct {
	coord.Backend
	store *coord.Store
	calls atomic.Int32
}

func (b *loseFirstReportResponse) SubmitReportWithID(ctx context.Context, project, token, task string, attempt int, baseline, body, requestID string) (coord.Report, error) {
	report, err := b.store.SubmitReportWithID(ctx, project, token, task, attempt, baseline, body, requestID)
	if err == nil && b.calls.Add(1) == 1 {
		return coord.Report{}, coord.ErrUnavailable
	}
	return report, err
}

func TestSubmitReportWithIDIsIdempotentAndRejectsChangedPayload(t *testing.T) {
	store, _, project, owner, worker := setup(t)
	ctx := context.Background()
	task := create(t, store, project, owner, nil)
	claim, err := store.ClaimTask(ctx, project.ID, worker.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.SubmitReportWithID(ctx, project.ID, worker.Token, task.ID, claim.Attempt, "commit:a", "final findings", "final-report-a")
	if err != nil {
		t.Fatal(err)
	}
	retry, err := store.SubmitReportWithID(ctx, project.ID, worker.Token, task.ID, claim.Attempt, "commit:a", "final findings", "final-report-a")
	if err != nil || retry.ID != first.ID || retry.Digest != first.Digest {
		t.Fatalf("same report retry created a different result: first=%+v retry=%+v err=%v", first, retry, err)
	}
	if _, err = store.SubmitReportWithID(ctx, project.ID, worker.Token, task.ID, claim.Attempt, "commit:a", "changed findings", "final-report-a"); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("request ID accepted changed report content: %v", err)
	}
	events, err := store.ReadEvents(ctx, project.ID, worker.Token, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Kind == "report.submitted" && event.EntityID == first.ID {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("idempotent submission created %d report events", count)
	}
}

func TestSubmitReportWithIDDigestSeparatesEmbeddedDelimiters(t *testing.T) {
	store, _, project, owner, worker := setup(t)
	ctx := context.Background()
	task := create(t, store, project, owner, nil)
	claim, err := store.ClaimTask(ctx, project.ID, worker.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SubmitReportWithID(ctx, project.ID, worker.Token, task.ID, claim.Attempt, "commit:a\x00", "b", "delimiter-collision"); err != nil {
		t.Fatalf("initial report: %v", err)
	}
	if _, err = store.SubmitReportWithID(ctx, project.ID, worker.Token, task.ID, claim.Attempt, "commit:a", "\x00b", "delimiter-collision"); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("changed tuple with ambiguous delimiter encoding was treated as a retry: %v", err)
	}
}

func TestMCPQueuesReportAndSafelyRetriesLostHomeResponse(t *testing.T) {
	store, _, project, owner, worker := setup(t)
	ctx := context.Background()
	task := create(t, store, project, owner, nil)
	claim, err := store.ClaimTask(ctx, project.ID, worker.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	backend := &loseFirstReportResponse{Backend: store, store: store}
	outbox, err := coord.OpenOutbox(filepath.Join(t.TempDir(), "outbox.db"), "https://home.example", project.ID, worker.PrincipalID, worker.Token, backend)
	if err != nil {
		t.Fatal(err)
	}
	adapter := coord.NewMCPAdapterWithOutbox(backend, project.ID, worker.Token, outbox)
	defer adapter.Close()
	tool := adapter.Server.GetTool("gossip_submit")
	response, err := tool.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_submit", Arguments: map[string]any{
		"task_id": task.ID, "attempt": claim.Attempt, "baseline": "commit:a", "body": "final findings after offline work",
		"request_id": "stable-final-report",
	}}})
	if err != nil || response.IsError {
		t.Fatalf("MCP report handoff failed instead of queueing: response=%+v err=%v", response, err)
	}
	var queued coord.QueuedReport
	if err = json.Unmarshal([]byte(response.Content[0].(mcp.TextContent).Text), &queued); err != nil {
		t.Fatal(err)
	}
	if queued.RequestID != "stable-final-report" || queued.Status != "pending" {
		t.Fatalf("report response did not identify the durable pending item: %+v", queued)
	}
	deadline := time.After(5 * time.Second)
	for {
		items, listErr := outbox.ListReportQueue(ctx, 20)
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(items) == 1 && items[0].Status == "delivered" {
			if items[0].ReportID == "" || items[0].Digest == "" {
				t.Fatalf("delivered report lacks its acknowledgment: %+v", items[0])
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("queued report did not become delivered: %+v", items)
		case <-time.After(20 * time.Millisecond):
		}
	}
	if got := backend.calls.Load(); got < 2 {
		t.Fatalf("lost home response was not retried idempotently: calls=%d", got)
	}
	updated, err := store.GetTask(ctx, project.ID, owner.Token, task.ID)
	if err != nil || updated.Status != "submitted" {
		t.Fatalf("home did not keep the committed report: task=%+v err=%v", updated, err)
	}
	events, err := store.ReadEvents(ctx, project.ID, owner.Token, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Kind == "report.submitted" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("lost response produced %d committed report events", count)
	}
	if removed, pruneErr := outbox.PruneReports(ctx); pruneErr != nil || removed != 1 {
		t.Fatalf("delivered report entry was not pruned: removed=%d err=%v", removed, pruneErr)
	}
	if items, listErr := outbox.ListReportQueue(ctx, 20); listErr != nil || len(items) != 0 {
		t.Fatalf("pruned report remained in local queue: items=%+v err=%v", items, listErr)
	}
}

func TestReportOutboxEnqueueIsBoundedAndIdempotent(t *testing.T) {
	store, _, project, _, worker := setup(t)
	outbox, err := coord.OpenOutbox(filepath.Join(t.TempDir(), "outbox.db"), "https://home.example", project.ID, worker.PrincipalID, worker.Token, store)
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	ctx := context.Background()
	queued, err := outbox.EnqueueReport(ctx, "task-test", 1, "commit:a", "report body", "report-request-test")
	if err != nil || queued.Status != "pending" {
		t.Fatalf("enqueue report: %+v %v", queued, err)
	}
	retry, err := outbox.EnqueueReport(ctx, "task-test", 1, "commit:a", "report body", "report-request-test")
	if err != nil || retry.RequestID != queued.RequestID {
		t.Fatalf("same report enqueue was not idempotent: %+v %v", retry, err)
	}
	if _, err = outbox.EnqueueReport(ctx, "task-test", 1, "commit:a", "changed body", "report-request-test"); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("same outbox request accepted changed payload: %v", err)
	}
	if err = outbox.RetryReport(ctx, queued.RequestID); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("pending report was requeued unnecessarily: %v", err)
	}
	if err = outbox.DiscardReport(ctx, queued.RequestID); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("pending report was discarded while delivery outcome is unknown: %v", err)
	}
	if _, err = outbox.ListReportQueue(ctx, 0); err == nil {
		t.Fatal("invalid report queue limit was accepted")
	}
	items, err := outbox.ListReportQueue(ctx, 20)
	encoded, encodeErr := json.Marshal(items)
	if err != nil || encodeErr != nil || len(items) != 1 || strings.Contains(string(encoded), "report body") {
		t.Fatalf("queue listing exposed body or lost item: %+v %s %v %v", items, encoded, err, encodeErr)
	}
}
