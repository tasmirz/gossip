package coord_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/mark3labs/mcp-go/mcp"
	"testing"
	"time"

	"github.com/tasmirz/gossip/pkg/coord"
)

type candidateSender struct {
	coord.Backend
	offline atomic.Bool
	lost    atomic.Bool
}

type transientFirstCandidateSender struct {
	coord.Backend
	firstAttempt atomic.Bool
	attempted    chan struct{}
}

func (b *transientFirstCandidateSender) SaveCandidateWithID(ctx context.Context, p, t, task string, attempt int, baseline, body, id string) (coord.CandidateReport, error) {
	if body == "transient first candidate" {
		if b.firstAttempt.CompareAndSwap(false, true) {
			close(b.attempted)
		}
		return coord.CandidateReport{}, errors.New("temporary network failure")
	}
	return b.Backend.SaveCandidateWithID(ctx, p, t, task, attempt, baseline, body, id)
}

func (b *candidateSender) SaveCandidateWithID(ctx context.Context, p, t, task string, attempt int, baseline, body, id string) (coord.CandidateReport, error) {
	if b.offline.Load() {
		return coord.CandidateReport{}, errors.New("offline")
	}
	c, err := b.Backend.SaveCandidateWithID(ctx, p, t, task, attempt, baseline, body, id)
	if err == nil && b.lost.Swap(false) {
		return coord.CandidateReport{}, errors.New("response lost")
	}
	return c, err
}
func TestOfflineCandidateReplayIsIdempotentAndNonAuthoritative(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()
	task := create(t, s, p, owner, nil)
	claim, err := s.ClaimTask(ctx, p.ID, worker.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	backend := &candidateSender{Backend: s}
	backend.offline.Store(true)
	path := filepath.Join(t.TempDir(), "offline.db")
	open := func() *coord.Outbox {
		o, err := coord.OpenOutbox(path, "https://home", p.ID, worker.PrincipalID, worker.Token, backend)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	box := open()
	c, err := box.EnqueueCandidate(ctx, task.ID, claim.Attempt, "commit:abc", "research findings", "retry-key")
	if err != nil || c.Status != "pending" {
		t.Fatalf("queue: %+v %v", c, err)
	}
	if _, err = box.EnqueueCandidate(ctx, task.ID, claim.Attempt, "commit:abc", "changed", "retry-key"); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("mismatched retry: %v", err)
	}
	box.Close()
	backend.offline.Store(false)
	backend.lost.Store(true)
	box = open()
	defer box.Close()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		items, err := box.ListCandidateQueue(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) == 1 && items[0].Status == "delivered" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	items, err := box.ListCandidateQueue(ctx, 10)
	if err != nil || len(items) != 1 || items[0].Status != "delivered" || items[0].CandidateID == "" || items[0].Body != "" {
		t.Fatalf("queue: %+v %v", items, err)
	}
	list, err := s.ListCandidates(ctx, p.ID, owner.Token, task.ID, 20)
	if err != nil || len(list) != 1 || list[0].ID != items[0].CandidateID {
		t.Fatalf("dedup: %+v %v", list, err)
	}
	after, err := s.GetTask(ctx, p.ID, worker.Token, task.ID)
	if err != nil || after.Status != "running" || after.Attempt != claim.Attempt {
		t.Fatalf("task changed: %+v %v", after, err)
	}
	if _, err = s.SaveCandidateWithID(ctx, p.ID, worker.Token, task.ID, claim.Attempt, "commit:abc", "changed", "retry-key"); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("home mismatch: %v", err)
	}
}

func TestCandidateBackoffDoesNotBlockLaterCandidates(t *testing.T) {
	store, _, project, owner, worker := setup(t)
	firstTask := create(t, store, project, owner, nil)
	secondTask, err := store.CreateTask(context.Background(), project.ID, owner.Token, 1, "Second research", "Investigate another question", "research", coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	firstAttempt, err := store.ClaimTask(context.Background(), project.ID, worker.Token, firstTask.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	secondAttempt, err := store.ClaimTask(context.Background(), project.ID, worker.Token, secondTask.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	backend := &transientFirstCandidateSender{Backend: store, attempted: make(chan struct{})}
	box, err := coord.OpenOutbox(filepath.Join(t.TempDir(), "outbox.db"), "https://home", project.ID, worker.PrincipalID, worker.Token, backend)
	if err != nil {
		t.Fatal(err)
	}
	defer box.Close()
	ctx := context.Background()
	if _, err = box.EnqueueCandidate(ctx, firstTask.ID, firstAttempt.Attempt, "commit:first", "transient first candidate", "older-candidate"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-backend.attempted:
	case <-time.After(2 * time.Second):
		t.Fatal("older candidate was not attempted")
	}
	if _, err = box.EnqueueCandidate(ctx, secondTask.ID, secondAttempt.Attempt, "commit:second", "deliver this candidate", "later-candidate"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		items, listErr := box.ListCandidateQueue(ctx, 10)
		if listErr != nil {
			t.Fatal(listErr)
		}
		for _, item := range items {
			if item.RequestID == "later-candidate" && item.Status == "delivered" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("a candidate waiting for backoff blocked a later eligible candidate")
}

func TestCandidateOutboxHasAggregateByteLimit(t *testing.T) {
	_, _, p, _, worker := setup(t)
	backend := &candidateSender{}
	backend.offline.Store(true)
	box, err := coord.OpenOutbox(filepath.Join(t.TempDir(), "offline.db"), "https://home", p.ID, worker.PrincipalID, worker.Token, backend)
	if err != nil {
		t.Fatal(err)
	}
	defer box.Close()
	body := strings.Repeat("x", 1024*1024)
	for i := 0; i < 32; i++ {
		if _, err = box.EnqueueCandidate(context.Background(), "task", 1, "commit:abc", body, fmt.Sprintf("candidate-%02d", i)); err != nil {
			t.Fatalf("queue candidate %d: %v", i, err)
		}
	}
	if _, err = box.EnqueueCandidate(context.Background(), "task", 1, "commit:abc", "x", "over-limit"); err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("aggregate body limit was not enforced: %v", err)
	}
}

func TestMCPCandidateQueueOffline(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	task := create(t, s, p, owner, nil)
	ctx := context.Background()
	claim, err := s.ClaimTask(ctx, p.ID, worker.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	backend := &candidateSender{Backend: s}
	backend.offline.Store(true)
	box, err := coord.OpenOutbox(filepath.Join(t.TempDir(), "offline.db"), "https://home", p.ID, worker.PrincipalID, worker.Token, backend)
	if err != nil {
		t.Fatal(err)
	}
	adapter := coord.NewMCPAdapterWithOutbox(backend, p.ID, worker.Token, box)
	defer adapter.Close()
	call := func(args map[string]any) *mcp.CallToolResult {
		t.Helper()
		res, err := adapter.Server.GetTool("gossip_submit").Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_submit", Arguments: args}})
		if err != nil || res.IsError {
			t.Fatalf("tool: %+v %v", res, err)
		}
		return res
	}
	res := call(map[string]any{"operation": "enqueue_candidate", "task_id": task.ID, "attempt": claim.Attempt, "baseline": "commit:abc", "body": "private findings", "request_id": "id"})
	if text := res.Content[0].(mcp.TextContent).Text; strings.Contains(text, "private findings") || !strings.Contains(text, "pending") {
		t.Fatal(text)
	}
	res = call(map[string]any{"operation": "candidate_outbox"})
	if text := res.Content[0].(mcp.TextContent).Text; strings.Contains(text, "private findings") || !strings.Contains(text, task.ID) {
		t.Fatal(text)
	}
	if err = box.DiscardCandidate(ctx, "id"); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("pending candidate was discarded while delivery outcome was unknown: %v", err)
	}
	if _, err = box.EnqueueCandidate(ctx, "missing-task", 1, "commit:abc", "failed report", "failed-id"); err != nil {
		t.Fatal(err)
	}
	backend.offline.Store(false)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		items, listErr := box.ListCandidateQueue(ctx, 10)
		if listErr != nil {
			t.Fatal(listErr)
		}
		failed := false
		for _, item := range items {
			if item.RequestID == "failed-id" && item.Status == "failed" {
				failed = true
			}
		}
		if failed {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err = box.DiscardCandidate(ctx, "failed-id"); err != nil {
		t.Fatalf("failed candidate could not be discarded: %v", err)
	}
}
