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

	// Test Re-identification across sessions
	origRegisteredAt := agents[0].RegisteredAt
	time.Sleep(10 * time.Millisecond)
	reidentSession := state.AgentSession{
		ID:        "test-agent-1",
		SessionID: "sess-2",
	}
	if err := store.RegisterAgent(reidentSession); err != nil {
		t.Fatalf("RegisterAgent re-identification failed: %v", err)
	}
	fetched, found, err := store.GetAgent("test-agent-1")
	if err != nil || !found {
		t.Fatalf("GetAgent failed: found=%v, err=%v", found, err)
	}
	if fetched.SessionCount != 2 {
		t.Fatalf("Expected SessionCount=2, got %d", fetched.SessionCount)
	}
	if !fetched.RegisteredAt.Equal(origRegisteredAt) {
		t.Fatalf("Expected RegisteredAt to be preserved across sessions")
	}
	if fetched.Role != "qa_engineer" {
		t.Fatalf("Expected Role='qa_engineer' to be retained, got %s", fetched.Role)
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

func TestNewStoreResolution(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-store-res-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Test GOSSIP_DIR env variable
	os.Setenv("GOSSIP_DIR", tmpDir)
	defer os.Unsetenv("GOSSIP_DIR")

	s, err := state.NewStore("")
	if err != nil {
		t.Fatalf("NewStore with GOSSIP_DIR failed: %v", err)
	}
	if s.Dir() != tmpDir {
		t.Fatalf("Expected dir %s, got %s", tmpDir, s.Dir())
	}
}
