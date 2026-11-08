package coord_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/tasmirz/gossip/pkg/coord"
)

func TestBudgetReservationsPreventOvercommitAndGateClaims(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()
	a := create(t, s, p, owner, nil)
	b := create(t, s, p, owner, nil)
	budget, err := s.SetBudget(ctx, p.ID, owner.Token, 0, 100)
	if err != nil || !budget.Enabled {
		t.Fatalf("enable: %+v %v", budget, err)
	}
	if _, err = s.ClaimTask(ctx, p.ID, worker.Token, a.ID, time.Minute); err == nil {
		t.Fatal("unreserved claim succeeded")
	}
	if _, err = s.ReserveBudget(ctx, p.ID, worker.Token, a.ID, 80); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("worker reserved: %v", err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{a.ID, b.ID} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, e := s.ReserveBudget(ctx, p.ID, owner.Token, id, 80)
			results <- e
		}(id)
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, coord.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("success %d conflict %d", success, conflicts)
	}
	budget, err = s.GetBudget(ctx, p.ID, worker.Token)
	if err != nil || budget.Reserved != 80 || len(budget.Reservations) != 1 {
		t.Fatalf("budget: %+v %v", budget, err)
	}
	id := budget.Reservations[0].TaskID
	claim, err := s.ClaimTask(ctx, p.ID, worker.Token, id, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReserveBudget(ctx, p.ID, owner.Token, id, 0); err == nil {
		t.Fatal("released running reservation")
	}
	if _, err = s.SetBudget(ctx, p.ID, owner.Token, budget.Revision, 79); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("reduced below reservation: %v", err)
	}
	if _, err = s.TransitionTask(ctx, p.ID, owner.Token, id, "cancel", "stop", claim.Attempt); err != nil {
		t.Fatal(err)
	}
	budget, err = s.GetBudget(ctx, p.ID, owner.Token)
	if err != nil || budget.Reserved != 80 {
		t.Fatalf("cancel released reservation: %+v %v", budget, err)
	}
	budget, err = s.ReserveBudget(ctx, p.ID, owner.Token, id, 0)
	if err != nil || budget.Reserved != 0 {
		t.Fatalf("release: %+v %v", budget, err)
	}
}

func TestBudgetRetryHoldsPriorAttempt(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()
	task := create(t, s, p, owner, nil)
	if _, err := s.SetBudget(ctx, p.ID, owner.Token, 0, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveBudget(ctx, p.ID, owner.Token, task.ID, 80); err != nil {
		t.Fatal(err)
	}
	old, err := s.ClaimTask(ctx, p.ID, worker.Token, task.ID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err = s.ClaimTask(ctx, p.ID, owner.Token, task.ID, time.Minute); err == nil {
		t.Fatal("retry exceeded envelope")
	}
	current, err := s.GetTask(ctx, p.ID, worker.Token, task.ID)
	if err != nil || current.Attempt != old.Attempt {
		t.Fatalf("failed claim changed attempt: %+v %v", current, err)
	}
	budget, err := s.GetBudget(ctx, p.ID, owner.Token)
	if err != nil || budget.Reserved != 80 || budget.Reservations[0].Held != 80 {
		t.Fatalf("initial hold: %+v %v", budget, err)
	}
	if _, err = s.SetBudget(ctx, p.ID, owner.Token, budget.Revision, 160); err != nil {
		t.Fatal(err)
	}
	next, err := s.ClaimTask(ctx, p.ID, owner.Token, task.ID, time.Minute)
	if err != nil || next.Attempt != old.Attempt+1 {
		t.Fatalf("retry: %+v %v", next, err)
	}
	budget, err = s.GetBudget(ctx, p.ID, owner.Token)
	if err != nil || budget.Reserved != 160 || budget.Reservations[0].Held != 160 {
		t.Fatalf("retry hold: %+v %v", budget, err)
	}
	if _, err = s.SubmitReport(ctx, p.ID, worker.Token, task.ID, old.Attempt, "old", "stale"); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("stale submit: %v", err)
	}
	if _, err = s.TransitionTask(ctx, p.ID, owner.Token, task.ID, "cancel", "stop", next.Attempt); err != nil {
		t.Fatal(err)
	}
	budget, err = s.ReserveBudget(ctx, p.ID, owner.Token, task.ID, 0)
	if err != nil || budget.Reserved != 0 {
		t.Fatalf("release: %+v %v", budget, err)
	}
}

func TestBudgetHoldMigrationIsAppliedOnce(t *testing.T) {
	s, path, p, owner, worker := setup(t)
	ctx := context.Background()
	task := create(t, s, p, owner, nil)
	if _, err := s.SetBudget(ctx, p.ID, owner.Token, 0, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveBudget(ctx, p.ID, owner.Token, task.ID, 80); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimTask(ctx, p.ID, worker.Token, task.ID, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM budget_attempt_holds; DELETE FROM coord_migrations WHERE name='budget_attempt_holds_v1'`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	reopened, err := coord.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	budget, err := reopened.GetBudget(ctx, p.ID, owner.Token)
	if err != nil || budget.Reservations[0].Held != 80 {
		t.Fatalf("migration: %+v %v", budget, err)
	}
	reopened.Close()
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM budget_attempt_holds`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	reopened, err = coord.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	budget, err = reopened.GetBudget(ctx, p.ID, owner.Token)
	if err != nil || budget.Reservations[0].Held != 0 {
		t.Fatalf("migration repeated: %+v %v", budget, err)
	}
}
