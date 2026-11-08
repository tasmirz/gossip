package coord_test

import (
	"context"
	"errors"
	"github.com/tasmirz/gossip/pkg/coord"
	"sync"
	"testing"
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
