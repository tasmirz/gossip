package state_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tasmirz/gossip/pkg/state"
)

func TestStateStoreLifecycle(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-state-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := state.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	// Test Atomic Write & Read
	testData := []byte("hello atomic world")
	if err := store.AtomicWriteFile("test.txt", testData); err != nil {
		t.Fatalf("AtomicWriteFile failed: %v", err)
	}

	readBytes, err := store.ReadFile("test.txt")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(readBytes) != string(testData) {
		t.Fatalf("Expected %s, got %s", testData, readBytes)
	}

	// Test Agent Registration & Heartbeat
	session := state.AgentSession{
		ID:           "test-agent-1",
		Name:         "Tester",
		Role:         "qa_engineer",
		Capabilities: []string{"unit-test", "fuzzing"},
	}

	if err := store.RegisterAgent(session); err != nil {
		t.Fatalf("RegisterAgent failed: %v", err)
	}

	agents, err := store.ListAgents()
	if err != nil {
		t.Fatalf("ListAgents failed: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("Expected 1 agent, got %d", len(agents))
	}
	if agents[0].ID != "test-agent-1" || agents[0].Status != "active" {
		t.Fatalf("Agent mismatch: %+v", agents[0])
	}

	// Test Heartbeat
	time.Sleep(10 * time.Millisecond)
	if err := store.Heartbeat("test-agent-1", "task-100", "active"); err != nil {
		t.Fatalf("Heartbeat failed: %v", err)
	}

	agents, _ = store.ListAgents()
	if agents[0].CurrentTaskID != "task-100" {
		t.Fatalf("Expected task-100, got %s", agents[0].CurrentTaskID)
	}

	// Test Goals & Milestones
	goal, err := store.SetGoal("Design Protocol", "Design binary framing", []state.Milestone{
		{ID: "ms-1", Title: "Define Header", Completed: false},
		{ID: "ms-2", Title: "Implement CRC", Completed: true},
	})
	if err != nil {
		t.Fatalf("SetGoal failed: %v", err)
	}
	if goal.Title != "Design Protocol" {
		t.Fatalf("Goal title mismatch: %s", goal.Title)
	}

	goals, err := store.GetGoals()
	if err != nil || len(goals) != 1 {
		t.Fatalf("Expected 1 goal, got %d", len(goals))
	}
	if len(goals[0].Milestones) != 2 {
		t.Fatalf("Expected 2 milestones, got %d", len(goals[0].Milestones))
	}

	// Verify lock acquisition
	unlock, err := store.Lock()
	if err != nil {
		t.Fatalf("Failed to acquire lock: %v", err)
	}
	unlock()

	// Ensure lockfile exists
	lockFile := filepath.Join(tmpDir, ".lock")
	if _, err := os.Stat(lockFile); err != nil {
		t.Fatalf("Expected .lock file to exist: %v", err)
	}
}
