package coord_test

import (
	"context"
	"errors"
	"sync"
	"testing"

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
