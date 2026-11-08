package tasks_test

import (
	"os"
	"strings"
	"testing"

	"github.com/tasmirz/gossip/pkg/state"
	"github.com/tasmirz/gossip/pkg/tasks"
)

func TestRejectInvalidDependenciesAndStatus(t *testing.T) {
	store, err := state.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mgr := tasks.NewManager(store)
	root, err := mgr.CreateTask("root", "", "", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, deps := range [][]string{{"missing"}, {root.ID, "missing"}, {root.ID, root.ID}} {
		if _, err := mgr.CreateTask("invalid", "", "", "", deps, nil); err == nil {
			t.Fatalf("accepted invalid dependencies %v", deps)
		}
	}
	if _, err := mgr.UpdateTask(root.ID, "made_up", "", "", ""); err == nil {
		t.Fatal("accepted invalid status")
	}
	duplicate := []string{root.ID, root.ID}
	other, err := mgr.CreateTask("other", "", "", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.ModifyTask(other.ID, tasks.TaskModifications{BlockedBy: &duplicate}); err == nil {
		t.Fatal("accepted duplicate dependencies on modification")
	}
	stored, err := mgr.GetTask(other.ID)
	if err != nil || len(stored.BlockedBy) != 0 {
		t.Fatalf("failed modification changed task: %+v, %v", stored, err)
	}
}

func TestDependencyChangesAndReopening(t *testing.T) {
	store, err := state.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := tasks.NewManager(store)
	root, err := m.CreateTask("root", "", "", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	child, err := m.CreateTask("child", "", "", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := []string{root.ID}
	child, err = m.ModifyTask(child.ID, tasks.TaskModifications{BlockedBy: &deps})
	if err != nil || child.Status != tasks.StatusBlocked {
		t.Fatalf("dependency edit: %+v %v", child, err)
	}
	for _, status := range []string{tasks.StatusTodo, tasks.StatusInProgress, tasks.StatusInReview, tasks.StatusCompleted} {
		if _, err := m.UpdateTask(child.ID, status, "", "", ""); err == nil {
			t.Fatalf("accepted %s with incomplete dependency", status)
		}
	}
	if _, err := m.UpdateTask(root.ID, tasks.StatusCompleted, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.UpdateTask(root.ID, tasks.StatusTodo, "", "", ""); err != nil {
		t.Fatal(err)
	}
	child, err = m.GetTask(child.ID)
	if err != nil || child.Status != tasks.StatusBlocked {
		t.Fatalf("reopen should block child: %+v %v", child, err)
	}
	if _, err := m.UpdateTask(root.ID, tasks.StatusCompleted, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.UpdateTask(child.ID, tasks.StatusInProgress, "worker", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.UpdateTask(root.ID, tasks.StatusTodo, "", "", ""); err == nil {
		t.Fatal("reopened prerequisite of active task")
	}
	root, err = m.GetTask(root.ID)
	if err != nil || root.Status != tasks.StatusCompleted {
		t.Fatalf("failed reopen persisted: %+v %v", root, err)
	}
}

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
