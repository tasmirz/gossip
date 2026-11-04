package tasks_test

import (
	"os"
	"strings"
	"testing"

	"github.com/tasmirz/gossip/pkg/state"
	"github.com/tasmirz/gossip/pkg/tasks"
)

func TestTasksLifecycleAndDAG(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-task-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := state.NewStore(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	mgr := tasks.NewManager(store)

	// Create root task 1
	t1, err := mgr.CreateTask("Design binary framing", "Define length prefix and CRC fields", "network_engineer", tasks.PriorityHigh, nil, []string{"Header format documented", "CRC selected"})
	if err != nil {
		t.Fatalf("CreateTask 1 failed: %v", err)
	}
	if t1.Status != tasks.StatusTodo {
		t.Fatalf("Expected task 1 status todo, got %s", t1.Status)
	}

	// Create dependent task 2 (blocked by task 1)
	t2, err := mgr.CreateTask("Implement decoder", "Write parser in Go", "worker", tasks.PriorityNormal, []string{t1.ID}, []string{"Decodes 4KB frames", "Validates CRC"})
	if err != nil {
		t.Fatalf("CreateTask 2 failed: %v", err)
	}
	if t2.Status != tasks.StatusBlocked {
		t.Fatalf("Expected task 2 status blocked, got %s", t2.Status)
	}

	// Attempting to advance t2 to in_progress should fail because t1 is incomplete
	_, err = mgr.UpdateTask(t2.ID, tasks.StatusInProgress, "worker-1", "Starting", "")
	if err == nil {
		t.Fatalf("Expected error starting blocked task, but succeeded")
	}

	// Now advance t1: todo -> in_progress -> completed
	t1Claimed, err := mgr.UpdateTask(t1.ID, tasks.StatusInProgress, "net-1", "Working on header", "")
	if err != nil {
		t.Fatalf("Failed to advance t1: %v", err)
	}
	if t1Claimed.Status != tasks.StatusInProgress || t1Claimed.AssignedTo != "net-1" {
		t.Fatalf("t1 state mismatch: %+v", t1Claimed)
	}

	// Complete t1
	t1Completed, err := mgr.UpdateTask(t1.ID, tasks.StatusCompleted, "net-1", "Framing finalized", "doc/framing.md")
	if err != nil {
		t.Fatalf("Failed to complete t1: %v", err)
	}
	if t1Completed.Status != tasks.StatusCompleted {
		t.Fatalf("t1 not completed")
	}

	// Verify t2 is now automatically unblocked and transitioned to 'todo'
	t2Fresh, err := mgr.GetTask(t2.ID)
	if err != nil {
		t.Fatalf("Failed to get t2: %v", err)
	}
	if t2Fresh.Status != tasks.StatusTodo {
		t.Fatalf("Expected t2 to be auto-unblocked to todo, got %s", t2Fresh.Status)
	}

	// Now t2 can transition to in_progress
	t2Working, err := mgr.UpdateTask(t2.ID, tasks.StatusInProgress, "worker-1", "Decoding implemented", "")
	if err != nil {
		t.Fatalf("Failed to start t2: %v", err)
	}
	if t2Working.Status != tasks.StatusInProgress {
		t.Fatalf("t2 status error")
	}

	// Test BOARD.md rendering
	board, err := mgr.RenderBoard()
	if err != nil {
		t.Fatalf("RenderBoard failed: %v", err)
	}
	if !strings.Contains(board, "Gossip Multi-Agent Task Board") {
		t.Fatalf("Board header missing")
	}
	if !strings.Contains(board, t1.Title) || !strings.Contains(board, t2.Title) {
		t.Fatalf("Board missing task titles")
	}

	// Verify BOARD.md was written to disk
	boardBytes, err := store.ReadFile("BOARD.md")
	if err != nil || len(boardBytes) == 0 {
		t.Fatalf("BOARD.md not written to disk: %v", err)
	}
}

func TestDAGCycleDetection(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-dag-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := state.NewStore(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	mgr := tasks.NewManager(store)

	t1, err := mgr.CreateTask("Task A", "Desc", "worker", tasks.PriorityNormal, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	t2, err := mgr.CreateTask("Task B", "Desc", "worker", tasks.PriorityNormal, []string{t1.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Attempting to create Task C that depends on Task B and is depended on by Task A would make a cycle
	// Or creating Task C depending on B, then updating A to depend on C
	c, _ := mgr.LoadContainer()
	// artificially introduce cycle to test validateNoCycles
	taskA := c.Tasks[t1.ID]
	taskA.BlockedBy = append(taskA.BlockedBy, t2.ID)
	c.Tasks[t1.ID] = taskA

	err = mgr.SaveContainer(c)
	// creating another task will fail validation
	_, err = mgr.CreateTask("Task C", "Desc", "worker", tasks.PriorityNormal, []string{t2.ID}, nil)
	if err == nil {
		t.Fatalf("Expected cycle detection failure, but succeeded")
	}
}

func TestModifyTask(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-task-modify-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := state.NewStore(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	mgr := tasks.NewManager(store)
	task, err := mgr.CreateTask("Original Title", "Original Desc", "worker", tasks.PriorityLow, nil, []string{"criteria 1"})
	if err != nil {
		t.Fatal(err)
	}

	newTitle := "Updated Title"
	newDesc := "Updated Description"
	newRole := "systems_architect"
	newPriority := tasks.PriorityHigh
	newAssigned := "sysarch-1"
	newCriteria := []string{"criteria 1", "criteria 2"}

	modified, err := mgr.ModifyTask(task.ID, tasks.TaskModifications{
		Title:              &newTitle,
		Description:        &newDesc,
		Role:               &newRole,
		Priority:           &newPriority,
		AssignedTo:         &newAssigned,
		AcceptanceCriteria: &newCriteria,
		ProgressNote:       "Updated details from dashboard",
	})
	if err != nil {
		t.Fatalf("ModifyTask failed: %v", err)
	}

	if modified.Title != newTitle || modified.Role != newRole || modified.Priority != newPriority {
		t.Fatalf("ModifyTask did not update fields correctly: %+v", modified)
	}
	if len(modified.Notes) != 1 {
		t.Fatalf("Expected 1 note, got %d", len(modified.Notes))
	}
}
