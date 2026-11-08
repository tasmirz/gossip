package coord_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/mark3labs/mcp-go/mcp"
	"testing"
	"time"

	"github.com/tasmirz/gossip/pkg/coord"
)

type unreliableSender struct {
	coord.Backend
	offline   atomic.Bool
	loseReply atomic.Bool
}

type transientFirstMessageSender struct {
	coord.Backend
	firstAttempt atomic.Bool
	attempted    chan struct{}
}

func (s *transientFirstMessageSender) SendMessageWithID(ctx context.Context, p, t, recipient, thread, subject, body, id string) (coord.Message, error) {
	if body == "transient first message" {
		if s.firstAttempt.CompareAndSwap(false, true) {
			close(s.attempted)
		}
		return coord.Message{}, errors.New("temporary network failure")
	}
	return s.Backend.SendMessageWithID(ctx, p, t, recipient, thread, subject, body, id)
}

func (s *unreliableSender) SendMessageWithID(ctx context.Context, p, t, recipient, thread, subject, body, id string) (coord.Message, error) {
	if s.offline.Load() {
		return coord.Message{}, errors.New("offline")
	}
	m, err := s.Backend.SendMessageWithID(ctx, p, t, recipient, thread, subject, body, id)
	if err == nil && s.loseReply.Swap(false) {
		return coord.Message{}, errors.New("response lost after commit")
	}
	return m, err
}
func (s *unreliableSender) ReadEvents(ctx context.Context, project, token string, after int64, limit int) ([]coord.Event, error) {
	if s.offline.Load() {
		return nil, errors.New("offline")
	}
	return s.Backend.ReadEvents(ctx, project, token, after, limit)
}

func TestOutboxCachesProjectEventsAcrossRestart(t *testing.T) {
	store, _, project, _, worker := setup(t)
	ctx := context.Background()
	backend := &unreliableSender{Backend: store}
	path := filepath.Join(t.TempDir(), "outbox.db")
	open := func() *coord.Outbox {
		t.Helper()
		box, err := coord.OpenOutbox(path, "https://home", project.ID, worker.PrincipalID, worker.Token, backend)
		if err != nil {
			t.Fatal(err)
		}
		return box
	}
	box := open()
	replicaID := box.ReplicaID()
	if len(replicaID) != len("replica_")+32 || !strings.HasPrefix(replicaID, "replica_") {
		t.Fatalf("invalid persistent replica identity: %q", replicaID)
	}
	adapter := coord.NewMCPAdapterWithOutbox(backend, project.ID, worker.Token, box)
	read := func(operation string) *mcp.CallToolResult {
		t.Helper()
		result, err := adapter.Server.GetTool("gossip_sync").Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_sync", Arguments: map[string]any{"operation": operation, "limit": 100}}})
		if err != nil || result.IsError {
			t.Fatalf("sync %s: %+v %v", operation, result, err)
		}
		return result
	}
	result := read("events")
	var live []coord.Event
	if err := json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &live); err != nil || len(live) == 0 {
		t.Fatalf("live events: %+v %v", live, err)
	}
	adapter.Close()
	backend.offline.Store(true)
	box = open()
	if box.ReplicaID() != replicaID {
		t.Fatalf("replica identity changed after outbox restart: before=%q after=%q", replicaID, box.ReplicaID())
	}
	adapter = coord.NewMCPAdapterWithOutbox(backend, project.ID, worker.Token, box)
	defer adapter.Close()
	result, err := adapter.Server.GetTool("gossip_sync").Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_sync", Arguments: map[string]any{"operation": "cached", "after": 0, "limit": 100}}})
	if err != nil || result.IsError {
		t.Fatalf("cached sync while offline: %+v %v", result, err)
	}
	var cached coord.CachedEventPage
	if err = json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &cached); err != nil {
		t.Fatal(err)
	}
	if cached.Source == "" || cached.ObservedAt <= 0 || len(cached.Events) != len(live) || cached.FirstSequence != live[0].Sequence || cached.LastSequence != live[len(live)-1].Sequence {
		t.Fatalf("cached events lost scope, freshness, or sequence metadata: %+v", cached)
	}
	other, otherOwner, err := store.CreateProject(ctx, "isolated cache", "other owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = coord.OpenOutbox(path, "https://home", other.ID, otherOwner.PrincipalID, otherOwner.Token, backend); err == nil {
		t.Fatal("event cache was reopened under another project identity")
	}
}

func TestOutboxReplayAfterRestartAndLostReply(t *testing.T) {
	store, _, p, owner, worker := setup(t)
	ctx := context.Background()
	backend := &unreliableSender{Backend: store}
	backend.offline.Store(true)
	path := filepath.Join(t.TempDir(), "outbox.db")
	open := func() *coord.Outbox {
		t.Helper()
		o, err := coord.OpenOutbox(path, "https://home", p.ID, worker.PrincipalID, worker.Token, backend)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	box := open()
	msg, err := box.Enqueue(ctx, owner.PrincipalID, "", "subject", "private message", "stable")
	if err != nil || msg.Status != "pending" {
		t.Fatalf("enqueue: %+v %v", msg, err)
	}
	if _, err = box.Enqueue(ctx, owner.PrincipalID, "", "subject", "different", "stable"); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("changed retry: %v", err)
	}
	box.Close()
	if _, err = coord.OpenOutbox(path, "https://other", p.ID, worker.PrincipalID, worker.Token, backend); err == nil {
		t.Fatal("wrong home accepted")
	}
	backend.offline.Store(false)
	backend.loseReply.Store(true)
	box = open()
	defer box.Close()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		list, err := box.List(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) == 1 && list[0].Status == "delivered" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	list, err := box.List(ctx, 10)
	if err != nil || len(list) != 1 || list[0].Status != "delivered" || list[0].MessageID == "" || list[0].Body != "" {
		t.Fatalf("delivery: %+v %v", list, err)
	}
	messages, err := store.ListMessages(ctx, p.ID, owner.Token, 0, 100)
	if err != nil || len(messages) != 1 || messages[0].Body != "private message" {
		t.Fatalf("deduplication: %+v %v", messages, err)
	}
	if n, err := box.Prune(ctx); err != nil || n != 1 {
		t.Fatalf("prune: %d %v", n, err)
	}
}

func TestOutboxBackoffDoesNotBlockLaterMessages(t *testing.T) {
	store, _, project, owner, worker := setup(t)
	backend := &transientFirstMessageSender{Backend: store, attempted: make(chan struct{})}
	box, err := coord.OpenOutbox(filepath.Join(t.TempDir(), "outbox.db"), "https://home", project.ID, worker.PrincipalID, worker.Token, backend)
	if err != nil {
		t.Fatal(err)
	}
	defer box.Close()
	ctx := context.Background()
	if _, err = box.Enqueue(ctx, owner.PrincipalID, "", "first", "transient first message", "older"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-backend.attempted:
	case <-time.After(2 * time.Second):
		t.Fatal("older message was not attempted")
	}
	if _, err = box.Enqueue(ctx, owner.PrincipalID, "", "second", "deliver this message", "later"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		items, listErr := box.List(ctx, 10)
		if listErr != nil {
			t.Fatal(listErr)
		}
		for _, item := range items {
			if item.RequestID == "later" && item.Status == "delivered" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("a message waiting for backoff blocked a later eligible message")
}

func TestOutboxMCPExposesPendingWithoutBody(t *testing.T) {
	store, _, p, _, worker := setup(t)
	backend := &unreliableSender{Backend: store}
	backend.offline.Store(true)
	box, err := coord.OpenOutbox(filepath.Join(t.TempDir(), "outbox.db"), "https://home", p.ID, worker.PrincipalID, worker.Token, backend)
	if err != nil {
		t.Fatal(err)
	}
	adapter := coord.NewMCPAdapterWithOutbox(backend, p.ID, worker.Token, box)
	defer adapter.Close()
	tool := adapter.Server.GetTool("gossip_send")
	invoke := func(args map[string]any) *mcp.CallToolResult {
		t.Helper()
		r, err := tool.Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_send", Arguments: args}})
		if err != nil || r.IsError {
			t.Fatalf("tool: %+v %v", r, err)
		}
		return r
	}
	result := invoke(map[string]any{"operation": "enqueue", "body": "secret body", "request_id": "once"})
	if strings.Contains(result.Content[0].(mcp.TextContent).Text, "secret body") {
		t.Fatal("enqueue echoed body")
	}
	result = invoke(map[string]any{"operation": "outbox"})
	if text := result.Content[0].(mcp.TextContent).Text; !strings.Contains(text, "pending") || strings.Contains(text, "secret body") {
		t.Fatalf("unexpected list: %s", text)
	}
}
