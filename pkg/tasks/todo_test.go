package tasks_test

import (
	"os"
	"testing"

	"github.com/tasmirz/gossip/pkg/state"
	"github.com/tasmirz/gossip/pkg/tasks"
)

func TestPersonalTodosLifecycle(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-ptodo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := state.NewStore(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	mgr := tasks.NewManager(store)

	// Add personal items
	item1, err := mgr.AddPersonalTodo("worker-1", "Inspect packet framing buffer", "high", "task-10", "Need to verify memory alignment")
	if err != nil {
		t.Fatalf("AddPersonalTodo failed: %v", err)
	}
	if item1.Status != tasks.PersonalTodoPending {
		t.Fatalf("Expected status pending, got %s", item1.Status)
	}

	item2, err := mgr.AddPersonalTodo("worker-1", "Add race-detector unit test", "normal", "task-10", "")
	if err != nil {
		t.Fatalf("AddPersonalTodo failed: %v", err)
	}

	// List personal items
	list, err := mgr.ListPersonalTodos("worker-1", "")
	if err != nil {
		t.Fatalf("ListPersonalTodos failed: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("Expected 2 personal todos, got %d", len(list))
	}

	// Update personal item
	updated, err := mgr.UpdatePersonalTodo("worker-1", item1.ID, tasks.PersonalTodoDone, "Memory alignment verified")
	if err != nil {
		t.Fatalf("UpdatePersonalTodo failed: %v", err)
	}
	if updated.Status != tasks.PersonalTodoDone {
		t.Fatalf("Expected status done, got %s", updated.Status)
	}

	// Filter by status
	doneList, err := mgr.ListPersonalTodos("worker-1", tasks.PersonalTodoDone)
	if err != nil {
		t.Fatal(err)
	}
	if len(doneList) != 1 {
		t.Fatalf("Expected 1 done item, got %d", len(doneList))
	}

	// Delete item
	err = mgr.DeletePersonalTodo("worker-1", item2.ID)
	if err != nil {
		t.Fatalf("DeletePersonalTodo failed: %v", err)
	}

	afterDelete, _ := mgr.ListPersonalTodos("worker-1", "")
	if len(afterDelete) != 1 {
		t.Fatalf("Expected 1 item after delete, got %d", len(afterDelete))
	}
}

func TestSystemTodoListAndOrchestratorAssignments(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-system-todo-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := state.NewStore(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	// Register goals
	_, _ = store.SetGoal("Deliver High-Throughput Protocol", "Binary framing and zero-copy buffers", []state.Milestone{
		{ID: "ms-1", Title: "Frame Header Spec", Completed: false},
		{ID: "ms-2", Title: "Integration Benchmark", Completed: false},
	})

	// Register agents
	_ = store.RegisterAgent(state.AgentSession{
		ID:     "agent:orch",
		Role:   "orchestrator",
		Name:   "Orchestrator Alpha",
		Status: "active",
	})
	_ = store.RegisterAgent(state.AgentSession{
		ID:     "agent:worker1",
		Role:   "worker",
		Name:   "Worker One",
		Status: "active",
	})

	mgr := tasks.NewManager(store)

	// Create root and dependent tasks
	t1, err := mgr.CreateTask("Root Architecture", "Design binary framing format", "worker", "high", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = mgr.CreateTask("Socket Transport", "Implement TCP socket loop", "worker", "normal", []string{t1.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = mgr.CreateTask("Packet Benchmark", "Benchmark buffer allocation", "worker", "normal", []string{t1.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Assign t1 to worker1
	_, err = mgr.UpdateTask(t1.ID, "in_progress", "agent:worker1", "Working on header", "")
	if err != nil {
		t.Fatal(err)
	}

	// 1. Test SystemTodoList
	sysTodos, err := mgr.GetSystemTodoList()
	if err != nil {
		t.Fatalf("GetSystemTodoList failed: %v", err)
	}

	if sysTodos.TotalActionItems == 0 {
		t.Fatalf("Expected action items > 0")
	}
	if sysTodos.PendingMilestonesCount != 2 {
		t.Fatalf("Expected 2 pending milestones, got %d", sysTodos.PendingMilestonesCount)
	}

	md := sysTodos.RenderMarkdown()
	if len(md) == 0 {
		t.Fatalf("Expected non-empty SystemTodoList markdown")
	}

	// 2. Test OrchestratorAssignmentMatrix
	matrix, err := mgr.GetOrchestratorAssignments()
	if err != nil {
		t.Fatalf("GetOrchestratorAssignments failed: %v", err)
	}

	if matrix.TotalTasks != 3 {
		t.Fatalf("Expected 3 total tasks, got %d", matrix.TotalTasks)
	}
	if matrix.AssignedTasksCount != 1 {
		t.Fatalf("Expected 1 assigned task, got %d", matrix.AssignedTasksCount)
	}
	if matrix.UnassignedTasksCount != 2 {
		t.Fatalf("Expected 2 unassigned tasks, got %d", matrix.UnassignedTasksCount)
	}
	if len(matrix.CriticalBottlenecks) == 0 {
		t.Fatalf("Expected t1 to be identified as critical bottleneck (blocking t2 and t3)")
	}

	matrixMD := matrix.RenderMarkdown()
	if len(matrixMD) == 0 {
		t.Fatalf("Expected non-empty Orchestrator matrix markdown")
	}
}
