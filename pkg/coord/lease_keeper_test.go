package coord

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func awaitRenewal(t *testing.T, ch <-chan int) int {
	t.Helper()
	select {
	case n := <-ch:
		return n
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for renewal")
		return 0
	}
}

func TestLeaseKeeperReplacesAttemptsAndStops(t *testing.T) {
	calls := make(chan int, 20)
	k := newLeaseKeeper(func(ctx context.Context, id string, attempt int, lease time.Duration) error {
		calls <- attempt
		return nil
	})
	k.track("task", 1, 30*time.Millisecond)
	if n := awaitRenewal(t, calls); n != 1 {
		t.Fatal(n)
	}
	k.track("task", 2, 30*time.Millisecond)
	k.stop("task", 1) // A late result for the previous attempt must not stop its successor.
	for {
		if n := awaitRenewal(t, calls); n == 2 {
			break
		}
	}
	k.stop("task", 2)
	k.close()
	for len(calls) > 0 {
		<-calls
	}
	select {
	case n := <-calls:
		t.Fatalf("renewal after close: %d", n)
	case <-time.After(40 * time.Millisecond):
	}
}

func TestLeaseKeeperStopsOnConflictAndCloseCancelsRequests(t *testing.T) {
	var count atomic.Int32
	called := make(chan int, 1)
	k := newLeaseKeeper(func(ctx context.Context, id string, attempt int, lease time.Duration) error {
		count.Add(1)
		called <- attempt
		if attempt == 1 {
			return ErrConflict
		}
		<-ctx.Done()
		return ctx.Err()
	})
	k.track("task", 1, 30*time.Millisecond)
	awaitRenewal(t, called)
	time.Sleep(40 * time.Millisecond)
	if n := count.Load(); n != 1 {
		t.Fatalf("renewed after conflict: %d", n)
	}
	k.track("task", 2, 30*time.Millisecond)
	awaitRenewal(t, called)
	done := make(chan struct{})
	go func() { k.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close did not cancel renewal")
	}
}
