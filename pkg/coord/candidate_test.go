package coord_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tasmirz/gossip/pkg/coord"
)

func TestStaleAttemptCandidateDoesNotCompleteTask(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()
	task := create(t, s, p, owner, nil)
	claim, err := s.ClaimTask(ctx, p.ID, worker.Token, task.ID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveCandidate(ctx, p.ID, worker.Token, task.ID, claim.Attempt, "old", "patch\x00binary"); err == nil {
		t.Fatal("source candidate accepted an embedded NUL that breaks text paging")
	}
	time.Sleep(1100 * time.Millisecond)
	newer, err := s.ClaimTask(ctx, p.ID, owner.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SubmitReport(ctx, p.ID, worker.Token, task.ID, claim.Attempt, "old", "old findings"); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("stale submit: %v", err)
	}
	c, err := s.SaveCandidate(ctx, p.ID, worker.Token, task.ID, claim.Attempt, "old", "old 🐈 findings")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveCandidate(ctx, p.ID, owner.Token, task.ID, claim.Attempt, "old", "forged"); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("forged candidate: %v", err)
	}
	got, err := s.GetTask(ctx, p.ID, owner.Token, task.ID)
	if err != nil || got.Status != "running" || got.Attempt != newer.Attempt || got.Owner != owner.PrincipalID {
		t.Fatalf("task changed: %+v %v", got, err)
	}
	list, err := s.ListCandidates(ctx, p.ID, owner.Token, task.ID, 50)
	if err != nil || len(list) != 1 || list[0].ID != c.ID || list[0].Body != "" {
		t.Fatalf("list: %+v %v", list, err)
	}
	full, err := s.GetCandidate(ctx, p.ID, owner.Token, c.ID)
	if err != nil || full.Body != "old 🐈 findings" {
		t.Fatalf("candidate: %+v %v", full, err)
	}
	first, err := s.GetCandidatePage(ctx, p.ID, owner.Token, c.ID, 0, 5)
	if err != nil || first.Excerpt != "old 🐈" || first.NextOffset == nil || *first.NextOffset != 5 || first.Digest != c.Digest {
		t.Fatalf("first candidate page: %+v %v", first, err)
	}
	second, err := s.GetCandidatePage(ctx, p.ID, owner.Token, c.ID, *first.NextOffset, 50)
	if err != nil || second.Excerpt != " findings" || second.NextOffset != nil {
		t.Fatalf("second candidate page: %+v %v", second, err)
	}
	if _, err = s.GetCandidatePage(ctx, p.ID, owner.Token, c.ID, 0, 16385); err == nil {
		t.Fatal("candidate page accepted an oversized request")
	}
	if err = s.AcceptReport(ctx, p.ID, owner.Token, c.ID, c.Digest); !errors.Is(err, coord.ErrNotFound) {
		t.Fatalf("candidate accepted: %v", err)
	}
	other, otherOwner, err := s.CreateProject(ctx, "other", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetCandidate(ctx, other.ID, otherOwner.Token, c.ID); !errors.Is(err, coord.ErrNotFound) {
		t.Fatalf("cross-project read: %v", err)
	}
	if _, err = s.GetCandidatePage(ctx, other.ID, otherOwner.Token, c.ID, 0, 10); !errors.Is(err, coord.ErrNotFound) {
		t.Fatalf("cross-project page read: %v", err)
	}
}
