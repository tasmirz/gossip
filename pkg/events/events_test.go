package events_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/tasmirz/gossip/pkg/deliberation"
	"github.com/tasmirz/gossip/pkg/events"
	"github.com/tasmirz/gossip/pkg/messages"
	"github.com/tasmirz/gossip/pkg/sop"
	"github.com/tasmirz/gossip/pkg/state"
	"github.com/tasmirz/gossip/pkg/tasks"
)

func setupTestMesh(t *testing.T) (*events.Manager, *state.Store, *messages.Manager, *tasks.Manager, string) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "gossip-events-test-*")
	if err != nil {
		t.Fatal(err)
	}

	store, err := state.NewStore(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	msgMgr := messages.NewManager(store)
	taskMgr := tasks.NewManager(store)
	delibMgr := deliberation.NewManager(store)
	sopMgr := sop.NewManager(store)

	evtMgr := events.NewManager(store, msgMgr, taskMgr, delibMgr, sopMgr)
	return evtMgr, store, msgMgr, taskMgr, tmpDir
}

func TestImmediatePollAndExplicitEvent(t *testing.T) {
	evtMgr, _, _, _, tmpDir := setupTestMesh(t)
	defer os.RemoveAll(tmpDir)

	before := time.Now().Add(-5 * time.Second)

	// Log explicit event
	err := evtMgr.LogEvent(events.Event{
		ID:        "test-evt-1",
		Type:      events.TypeCustomEvent,
		Timestamp: time.Now(),
		Summary:   "Deploy initiated",
	})
	if err != nil {
		t.Fatalf("LogEvent failed: %v", err)
	}

	resp, err := evtMgr.Poll(context.Background(), events.PollRequest{
		AgentID:   "agent-1",
		Role:      "worker",
		SinceTime: before,
		Timeout:   0,
	})
	if err != nil {
		t.Fatalf("Poll failed: %v", err)
	}

	if !resp.HasUpdates {
		t.Fatalf("Expected HasUpdates=true")
	}
	if len(resp.Events) == 0 {
		t.Fatalf("Expected at least 1 event, got 0")
	}
	if resp.Events[0].ID != "test-evt-1" {
		t.Fatalf("Expected event ID test-evt-1, got %s", resp.Events[0].ID)
	}
}

func TestLongPollingWakeupOnIncomingMessage(t *testing.T) {
	evtMgr, _, msgMgr, _, tmpDir := setupTestMesh(t)
	defer os.RemoveAll(tmpDir)

	startTime := time.Now()

	// Launch long-poll in background
	type pollResult struct {
		resp *events.PollResponse
		err  error
	}
	resChan := make(chan pollResult, 1)

	go func() {
		resp, err := evtMgr.Poll(context.Background(), events.PollRequest{
			AgentID:       "worker-42",
			Role:          "worker",
			SinceTime:     startTime,
			Timeout:       3 * time.Second,
			AutoHeartbeat: true,
		})
		resChan <- pollResult{resp: resp, err: err}
	}()

	// Sleep 400ms then post a targeted message
	time.Sleep(400 * time.Millisecond)
	_, err := msgMgr.SendMessage("lead", "orchestrator", "worker-42", "th-1", "Urgent Fix", "Please address memory leak")
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	// Poll should wake up well before the 3-second timeout!
	select {
	case res := <-resChan:
		if res.err != nil {
			t.Fatalf("Poll error: %v", res.err)
		}
		if !res.resp.HasUpdates {
			t.Fatalf("Expected HasUpdates=true upon receiving message")
		}
		if len(res.resp.Events) == 0 {
			t.Fatalf("Expected events to contain the message")
		}
		if res.resp.Events[0].Type != events.TypeMessageReceived {
			t.Fatalf("Expected TypeMessageReceived, got %s", res.resp.Events[0].Type)
		}
		if res.resp.PollDurationMs >= 2500 {
			t.Fatalf("Expected long-poll to wake up quickly, took %d ms", res.resp.PollDurationMs)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("Long-poll timed out without waking up")
	}
}

func TestTaskReadyEventDetection(t *testing.T) {
	evtMgr, _, _, taskMgr, tmpDir := setupTestMesh(t)
	defer os.RemoveAll(tmpDir)

	before := time.Now().Add(-1 * time.Second)

	// Create prerequisite task
	t1, err := taskMgr.CreateTask("Root Task", "Prepare foundation", "worker", "normal", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Create blocked task
	t2, err := taskMgr.CreateTask("Dependent Task", "Follow up", "worker", "high", []string{t1.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Poll before t1 is completed - t2 should not be ready
	resp1, _ := evtMgr.Poll(context.Background(), events.PollRequest{
		AgentID:   "w1",
		Role:      "worker",
		SinceTime: before,
		Timeout:   0,
	})
	// t1 is ready
	if resp1.ReadyTasksCount != 1 {
		t.Fatalf("Expected 1 ready task (t1), got %d", resp1.ReadyTasksCount)
	}

	// Complete t1
	_, err = taskMgr.UpdateTask(t1.ID, "completed", "w1", "Done", "out.txt")
	if err != nil {
		t.Fatal(err)
	}

	// Poll again - t2 should now be ready!
	resp2, _ := evtMgr.Poll(context.Background(), events.PollRequest{
		AgentID:   "w1",
		Role:      "worker",
		SinceTime: before,
		Timeout:   0,
	})
	if resp2.ReadyTasksCount != 1 { // t1 completed, so only t2 is ready now
		t.Fatalf("Expected 1 ready task (t2), got %d", resp2.ReadyTasksCount)
	}

	hasT2Ready := false
	for _, e := range resp2.Events {
		if e.Type == events.TypeTaskReady && e.TargetID == t2.ID {
			hasT2Ready = true
			break
		}
	}
	if !hasT2Ready {
		t.Fatalf("Expected task_ready event for %s", t2.ID)
	}
}
