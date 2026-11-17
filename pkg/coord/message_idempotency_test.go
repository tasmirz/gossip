package coord_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/tasmirz/gossip/pkg/coord"
)

func TestMessageRetriesAreAtomicAndProjectScoped(t *testing.T) {
	s, path, p, owner, worker := setup(t)
	ctx := context.Background()
	other, err := coord.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	type outcome struct {
		m   coord.Message
		err error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	var wg sync.WaitGroup
	for _, db := range []*coord.Store{s, other} {
		wg.Add(1)
		go func(db *coord.Store) {
			defer wg.Done()
			<-start
			m, err := db.SendMessageWithID(ctx, p.ID, owner.Token, worker.PrincipalID, "", "", "hello", "request-1")
			results <- outcome{m, err}
		}(db)
	}
	close(start)
	wg.Wait()
	close(results)
	var first coord.Message
	for r := range results {
		if r.err != nil {
			t.Fatal(r.err)
		}
		if first.ID == "" {
			first = r.m
		} else if first != r.m {
			t.Fatalf("duplicate produced different message: %+v %+v", first, r.m)
		}
	}
	if _, err = s.SendMessageWithID(ctx, p.ID, owner.Token, worker.PrincipalID, "", "", "changed", "request-1"); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("different payload: %v", err)
	}
	independent, err := s.SendMessageWithID(ctx, p.ID, worker.Token, "", "", "", "hello", "request-1")
	if err != nil || independent.ID == first.ID {
		t.Fatalf("principal scope: %+v %v", independent, err)
	}
	restarted, err := coord.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	retry, err := restarted.SendMessageWithID(ctx, p.ID, owner.Token, worker.PrincipalID, "", "", "hello", "request-1")
	if err != nil || retry != first {
		t.Fatalf("restart retry: %+v %v", retry, err)
	}
	events, err := s.ReadEvents(ctx, p.ID, owner.Token, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range events {
		if e.Kind == "message.sent" && e.EntityID == first.ID {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("message events: %d", count)
	}
}

func TestConversationMessagesFilterBeforePagination(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()
	other, err := s.AddPrincipal(ctx, p.ID, owner.Token, "other", "worker")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SendMessage(ctx, p.ID, owner.Token, "", "", "", "room one")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SendMessage(ctx, p.ID, owner.Token, worker.PrincipalID, "", "", "owner to worker")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SendMessage(ctx, p.ID, worker.Token, other.PrincipalID, "", "", "private worker pair")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SendMessage(ctx, p.ID, worker.Token, owner.PrincipalID, "", "", "worker to owner")
	if err != nil {
		t.Fatal(err)
	}
	room, err := s.ConversationMessages(ctx, p.ID, owner.Token, "", 0, 0, 1, true)
	if err != nil || len(room) != 1 || room[0].Body != "room one" {
		t.Fatalf("room filter/pagination: %#v err=%v", room, err)
	}
	dm, err := s.ConversationMessages(ctx, p.ID, owner.Token, worker.PrincipalID, 0, 0, 1, true)
	if err != nil || len(dm) != 1 || dm[0].Body != "worker to owner" {
		t.Fatalf("DM filter/pagination: %#v err=%v", dm, err)
	}
	if _, err := s.AcknowledgeMessage(ctx, p.ID, owner.Token, dm[0].ID); err != nil {
		t.Fatal(err)
	}
	for _, viewer := range []struct {
		token, peer  string
		acknowledged bool
	}{
		{owner.Token, worker.PrincipalID, true},
		{worker.Token, owner.PrincipalID, false},
	} {
		page, err := s.ConversationMessages(ctx, p.ID, viewer.token, viewer.peer, 0, 0, 1, true)
		if err != nil || len(page) != 1 || page[0].ID != dm[0].ID || page[0].AcknowledgedByMe != viewer.acknowledged {
			t.Fatalf("viewer-scoped acknowledgment: %#v, %v", page, err)
		}
	}
	if _, err = s.ConversationMessages(ctx, p.ID, owner.Token, other.PrincipalID, 0, 0, 20, true); err != nil {
		t.Fatal(err)
	}
	for _, message := range dm {
		if message.Body == "private worker pair" {
			t.Fatal("nonparticipant direct message leaked")
		}
	}
}

func TestMessagesHashAnchorReturnsOnePrevPlusNew(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()
	other, err := s.AddPrincipal(ctx, p.ID, owner.Token, "other", "worker")
	if err != nil {
		t.Fatal(err)
	}

	m1, err := s.SendMessageWithID(ctx, p.ID, owner.Token, "", "", "one", "msg 1", "h-1")
	if err != nil {
		t.Fatal(err)
	}
	m2, err := s.SendMessageWithID(ctx, p.ID, owner.Token, "", "", "two", "msg 2", "h-2")
	if err != nil {
		t.Fatal(err)
	}
	secretDM, err := s.SendMessageWithID(ctx, p.ID, owner.Token, other.PrincipalID, "", "secret", "owner to other", "h-dm")
	if err != nil {
		t.Fatal(err)
	}
	m3, err := s.SendMessageWithID(ctx, p.ID, owner.Token, "", "", "three", "msg 3", "h-3")
	if err != nil {
		t.Fatal(err)
	}
	m4, err := s.SendMessageWithID(ctx, p.ID, worker.Token, "", "", "four", "msg 4", "h-4")
	if err != nil {
		t.Fatal(err)
	}
	if len(m1.Hash) != 64 || m1.Hash == m2.Hash || secretDM.Hash == "" {
		t.Fatalf("expected distinct 64-char SHA-256 message hashes, got m1=%q m2=%q", m1.Hash, m2.Hash)
	}

	workerMCP := coord.MCPServer(s, p.ID, worker.Token)
	readTool := workerMCP.GetTool("gossip_read")
	syncTool := workerMCP.GetTool("gossip_sync")

	// 1. Omitting last_read_hash returns all visible messages (m1..m4, excluding secretDM).
	res, err := readTool.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "gossip_read",
		Arguments: map[string]any{"operation": "messages"},
	}})
	if err != nil || res.IsError {
		t.Fatalf("read all messages: err=%v res=%+v", err, res)
	}
	var all []coord.Message
	if err := json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &all); err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 || all[0].Hash != m1.Hash || all[3].Hash != m4.Hash {
		t.Fatalf("expected all 4 visible messages, got %+v", all)
	}

	// 2. Passing last_read_hash=m2.Hash returns 1 prev (m2) + new ones (m3, m4).
	res, err = readTool.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "gossip_read",
		Arguments: map[string]any{"operation": "messages", "last_read_hash": m2.Hash},
	}})
	if err != nil || res.IsError {
		t.Fatalf("read from hash m2: err=%v res=%+v", err, res)
	}
	var delta []coord.Message
	if err := json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &delta); err != nil {
		t.Fatal(err)
	}
	if len(delta) != 3 || delta[0].ID != m2.ID || delta[1].ID != m3.ID || delta[2].ID != m4.ID {
		t.Fatalf("expected [m2, m3, m4] (1 prev + new), got %+v", delta)
	}

	// 3. Passing last_msg_hash=m4.Hash when caught up returns only [m4] (1 prev + 0 new).
	res, err = readTool.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "gossip_read",
		Arguments: map[string]any{"operation": "messages", "last_msg_hash": m4.Hash},
	}})
	if err != nil || res.IsError {
		t.Fatalf("read caught-up hash m4: err=%v res=%+v", err, res)
	}
	var caughtUp []coord.Message
	_ = json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &caughtUp)
	if len(caughtUp) != 1 || caughtUp[0].ID != m4.ID {
		t.Fatalf("expected only [m4] when caught up, got %+v", caughtUp)
	}

	// 4. Hidden DM hash must be rejected without leaking secret message existence.
	res, err = readTool.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "gossip_read",
		Arguments: map[string]any{"operation": "messages", "last_read_hash": secretDM.Hash},
	}})
	if err != nil || !res.IsError {
		t.Fatalf("expected error for hidden DM hash, got res=%+v err=%v", res, err)
	}

	// 5. gossip_sync(operation=inbox) with last_read_hash=m3.Hash returns [m3, m4] previews and LatestMessageHash.
	res, err = syncTool.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "gossip_sync",
		Arguments: map[string]any{"operation": "inbox", "last_read_hash": m3.Hash},
	}})
	if err != nil || res.IsError {
		t.Fatalf("sync inbox from hash m3: err=%v res=%+v", err, res)
	}
	var frame coord.ProjectSyncFrame
	if err := json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &frame); err != nil {
		t.Fatal(err)
	}
	if len(frame.InboxMessages) != 2 || frame.InboxMessages[0].ID != m3.ID || frame.InboxMessages[1].ID != m4.ID || frame.LatestMessageHash != m4.Hash {
		t.Fatalf("expected inbox [m3, m4] with LatestMessageHash=%q, got %+v", m4.Hash, frame)
	}
}
