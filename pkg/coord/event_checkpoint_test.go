package coord_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tasmirz/gossip/pkg/coord"
)

func TestEventCheckpointDurableScopedAndMonotonic(t *testing.T) {
	s, path, p, owner, worker := setup(t)
	ctx := context.Background()
	events, err := s.ReadEvents(ctx, p.ID, worker.Token, 0, 200)
	if err != nil || len(events) < 2 {
		t.Fatalf("events: %v %v", events, err)
	}
	last := events[len(events)-1].Sequence
	if n, err := s.AcknowledgeEvents(ctx, p.ID, worker.Token, last); err != nil || n != last {
		t.Fatalf("ack: %d %v", n, err)
	}
	if n, err := s.AcknowledgeEvents(ctx, p.ID, worker.Token, events[0].Sequence); err != nil || n != last {
		t.Fatalf("regressed: %d %v", n, err)
	}
	if n, err := s.EventCheckpoint(ctx, p.ID, owner.Token); err != nil || n != 0 {
		t.Fatalf("principal isolation: %d %v", n, err)
	}
	other, otherOwner, err := s.CreateProject(ctx, "other", "owner")
	if err != nil {
		t.Fatal(err)
	}
	otherEvents, err := s.ReadEvents(ctx, other.ID, otherOwner.Token, 0, 200)
	if err != nil || len(otherEvents) == 0 {
		t.Fatalf("other events: %v %v", otherEvents, err)
	}
	if _, err := s.AcknowledgeEvents(ctx, p.ID, worker.Token, otherEvents[0].Sequence); !errors.Is(err, coord.ErrNotFound) {
		t.Fatalf("cross-project sequence: %v", err)
	}
	if _, err := s.EventCheckpoint(ctx, p.ID, "bad"); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("bad credential: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := coord.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if n, err := reopened.EventCheckpoint(ctx, p.ID, worker.Token); err != nil || n != last {
		t.Fatalf("restart: %d %v", n, err)
	}
}
