package coord_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tasmirz/gossip/pkg/coord"
	_ "modernc.org/sqlite"
)

// TestHelperCrashWorker is a helper subprocess for crash simulation.
func TestHelperCrashWorker(t *testing.T) {
	if os.Getenv("TEST_CRASH_WORKER") != "1" {
		return
	}
	dbPath := os.Getenv("TEST_CRASH_DB")
	projID := os.Getenv("TEST_CRASH_PROJ")
	rawDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		os.Exit(10)
	}
	tx, err := rawDB.Begin()
	if err != nil {
		os.Exit(11)
	}
	_, err = tx.Exec(`INSERT INTO tasks(id,project_id,plan_revision,title,description,kind,report_contract,status,created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		"task_dirty_uncommitted_subprocess", projID, 1, "Dirty Subprocess Task", "Should not exist", "research", `{"review_type":"full_code"}`, "ready", time.Now().UnixMilli())
	if err != nil {
		os.Exit(12)
	}
	// Abrupt crash without commit or rollback: os.Exit(42) terminates process immediately
	os.Exit(42)
}

// TestAdversarialM4_ConcurrentPlanAcknowledgments tests high-concurrency plan acknowledgments:
// 1. Same principal issuing concurrent acknowledgments for the same revision (idempotency race).
// 2. Multiple distinct principals acknowledging the same revision concurrently.
// 3. Concurrent acknowledgments racing against plan publication / revision bumps.
// 4. Invalid revision boundary values (0, negative, non-existent future revisions).
func TestAdversarialM4_ConcurrentPlanAcknowledgments(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "ack_stress.db")
	s, err := coord.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	proj, owner, err := s.CreateProject(ctx, "concurrent-ack", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishPlan(ctx, proj.ID, owner.Token, "Revision 1 Initial Plan", 0); err != nil {
		t.Fatal(err)
	}

	// 1. Same principal: 20 concurrent goroutines acknowledging revision 1
	worker, err := s.AddPrincipal(ctx, proj.ID, owner.Token, "worker-solo", "worker")
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.AcknowledgePlan(ctx, proj.ID, worker.Token, 1); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)

	for e := range errCh {
		t.Fatalf("concurrent plan acknowledgment for same principal failed: %v", e)
	}

	// 2. 20 distinct principals acknowledging revision 1 simultaneously
	const numWorkers = 20
	workers := make([]coord.Credential, numWorkers)
	for i := 0; i < numWorkers; i++ {
		w, err := s.AddPrincipal(ctx, proj.ID, owner.Token, fmt.Sprintf("worker-%d", i), "worker")
		if err != nil {
			t.Fatalf("failed to add principal %d: %v", i, err)
		}
		workers[i] = w
	}

	startBarrier := make(chan struct{})
	errCh2 := make(chan error, numWorkers)
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(w coord.Credential) {
			defer wg.Done()
			<-startBarrier
			if err := s.AcknowledgePlan(ctx, proj.ID, w.Token, 1); err != nil {
				errCh2 <- err
			}
		}(workers[i])
	}
	close(startBarrier)
	wg.Wait()
	close(errCh2)

	for e := range errCh2 {
		t.Fatalf("concurrent plan acknowledgment across distinct principals failed: %v", e)
	}

	// 3. Racing plan publication (revision bump 1 -> 2) against worker acknowledgments
	if _, err := s.PublishPlan(ctx, proj.ID, owner.Token, "Revision 2 Plan", 1); err != nil {
		t.Fatal(err)
	}

	var ackRev1Err, ackRev2Success int64
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(w coord.Credential) {
			defer wg.Done()
			// Acknowledging outdated revision 1 should fail with ErrConflict
			if err := s.AcknowledgePlan(ctx, proj.ID, w.Token, 1); errors.Is(err, coord.ErrConflict) {
				atomic.AddInt64(&ackRev1Err, 1)
			}
			// Acknowledging current revision 2 should succeed
			if err := s.AcknowledgePlan(ctx, proj.ID, w.Token, 2); err == nil {
				atomic.AddInt64(&ackRev2Success, 1)
			}
		}(workers[i])
	}
	wg.Wait()

	if ackRev1Err != numWorkers {
		t.Errorf("expected %d conflicts acknowledging outdated revision 1, got %d", numWorkers, ackRev1Err)
	}
	if ackRev2Success != numWorkers {
		t.Errorf("expected %d successes acknowledging current revision 2, got %d", numWorkers, ackRev2Success)
	}

	// 4. Invalid revision numbers must fail with ErrConflict
	invalidRevs := []int{0, -1, 9999}
	for _, invRev := range invalidRevs {
		if err := s.AcknowledgePlan(ctx, proj.ID, worker.Token, invRev); !errors.Is(err, coord.ErrConflict) {
			t.Errorf("expected ErrConflict for invalid revision %d, got %v", invRev, err)
		}
	}
}

// TestAdversarialM4_RapidTaskClaimsAcrossMultipleWorktrees verifies atomic claim exclusivity
// across multiple distinct Git worktrees and workers, and confirms concurrency limit enforcement.
func TestAdversarialM4_RapidTaskClaimsAcrossMultipleWorktrees(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable is not available")
	}

	ctx := context.Background()
	tempDir := t.TempDir()
	mainRepo := filepath.Join(tempDir, "main-repo")
	const numWorktrees = 6

	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %s (%v)", args, string(output), err)
		}
	}

	if err := os.MkdirAll(mainRepo, 0700); err != nil {
		t.Fatal(err)
	}
	runGit(mainRepo, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(mainRepo, "file.txt"), []byte("seed content\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(mainRepo, "add", "file.txt")
	runGit(mainRepo, "-c", "user.name=Tester", "-c", "user.email=t@example.invalid", "commit", "-m", "init")

	dbPath := filepath.Join(tempDir, "claims.db")
	s, err := coord.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	proj, owner, err := s.CreateProject(ctx, "rapid-claims", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishPlan(ctx, proj.ID, owner.Token, "Plan", 0); err != nil {
		t.Fatal(err)
	}
	// Increase MaxParallel to 10 to allow parallel running tasks
	if _, err := s.UpdateSettings(ctx, proj.ID, owner.Token, coord.Settings{
		Revision:           1,
		MaxParallel:        10,
		MaxLeaseSeconds:    3600,
		SummaryTokenTarget: 1000,
		DefaultReviewMode:  "report_first",
	}); err != nil {
		t.Fatal(err)
	}

	repo, err := s.CreateRepository(ctx, proj.ID, owner.Token, "main-repo")
	if err != nil {
		t.Fatal(err)
	}

	// Create 6 worktrees and workers
	type workerNode struct {
		cred      coord.Credential
		worktree  string
		workspace coord.Workspace
	}
	nodes := make([]workerNode, numWorktrees)
	for i := 0; i < numWorktrees; i++ {
		wtPath := filepath.Join(tempDir, fmt.Sprintf("worktree-%d", i))
		runGit(mainRepo, "worktree", "add", "--detach", wtPath)

		w, err := s.AddPrincipal(ctx, proj.ID, owner.Token, fmt.Sprintf("worker-wt-%d", i), "worker")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.AcknowledgePlan(ctx, proj.ID, w.Token, 1); err != nil {
			t.Fatal(err)
		}

		obs, err := coord.InspectGitWorkspace(ctx, wtPath)
		if err != nil {
			t.Fatal(err)
		}
		ws, err := s.RegisterWorkspace(ctx, proj.ID, w.Token, repo.ID, fmt.Sprintf("ws-%d", i), fmt.Sprintf("node-%d", i), wtPath, obs.Branch, obs.Head, obs.Dirty)
		if err != nil {
			t.Fatal(err)
		}

		nodes[i] = workerNode{cred: w, worktree: wtPath, workspace: ws}
	}

	// Run rapid claim races for 5 successive tasks
	for taskIdx := 0; taskIdx < 5; taskIdx++ {
		task, err := s.CreateTask(ctx, proj.ID, owner.Token, 1,
			fmt.Sprintf("Concurrent Task %d", taskIdx), "Task under race", "implementation",
			coord.ReportContract{ReviewType: "full_code"}, nil)
		if err != nil {
			t.Fatalf("failed to create task %d: %v", taskIdx, err)
		}

		var wg sync.WaitGroup
		barrier := make(chan struct{})
		results := make([]error, numWorktrees)

		for i := 0; i < numWorktrees; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				<-barrier
				_, claimErr := s.ClaimTaskInWorkspace(ctx, proj.ID, nodes[idx].cred.Token, task.ID, nodes[idx].workspace.ID, 10*time.Second)
				results[idx] = claimErr
			}(i)
		}

		close(barrier)
		wg.Wait()

		var successCount, conflictCount int
		var winnerIdx int
		for i, rErr := range results {
			if rErr == nil {
				successCount++
				winnerIdx = i
			} else if errors.Is(rErr, coord.ErrConflict) {
				conflictCount++
			} else {
				t.Fatalf("unexpected claim error for node %d: %v", i, rErr)
			}
		}

		if successCount != 1 {
			t.Fatalf("task %d: expected exactly 1 successful claim, got %d", taskIdx, successCount)
		}
		if conflictCount != numWorktrees-1 {
			t.Fatalf("task %d: expected exactly %d conflicts, got %d", taskIdx, numWorktrees-1, conflictCount)
		}

		// Verify task state in database
		storedTask, err := s.GetTask(ctx, proj.ID, owner.Token, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if storedTask.Status != "running" {
			t.Errorf("task %d: status = %q, want running", taskIdx, storedTask.Status)
		}
		if storedTask.Owner != nodes[winnerIdx].cred.PrincipalID {
			t.Errorf("task %d: owner = %q, want %q", taskIdx, storedTask.Owner, nodes[winnerIdx].cred.PrincipalID)
		}
		if storedTask.Attempt != 1 {
			t.Errorf("task %d: attempt = %d, want 1", taskIdx, storedTask.Attempt)
		}
	}

	// Verify Concurrency Limit Enforcement (MaxParallel Gate)
	// Currently 5 tasks are running. Set MaxParallel to 5.
	if _, err := s.UpdateSettings(ctx, proj.ID, owner.Token, coord.Settings{
		Revision:           2,
		MaxParallel:        5,
		MaxLeaseSeconds:    3600,
		SummaryTokenTarget: 1000,
		DefaultReviewMode:  "report_first",
	}); err != nil {
		t.Fatal(err)
	}

	// Create a 6th task
	task6, err := s.CreateTask(ctx, proj.ID, owner.Token, 1, "Task 6 Over Limit", "Concurrency exhaustion", "implementation",
		coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Any worker attempting to claim task 6 must be rejected with ErrConflict because running == 5 >= MaxParallel!
	for i := 0; i < numWorktrees; i++ {
		_, err := s.ClaimTaskInWorkspace(ctx, proj.ID, nodes[i].cred.Token, task6.ID, nodes[i].workspace.ID, 10*time.Second)
		if !errors.Is(err, coord.ErrConflict) {
			t.Fatalf("expected ErrConflict due to concurrency limit exhaustion, got %v", err)
		}
	}
}

// TestAdversarialM4_LeaseStealingBoundaryConditions tests exact boundary conditions of leases:
// 1. Premature steal attempt while lease is active (must return ErrConflict).
// 2. Exact expiration transition (steal must succeed, incrementing attempt counter).
// 3. Stale attempt fencing (original worker cannot renew or submit under superseded attempt).
// 4. Renewal vs Steal concurrency race around the boundary.
// 5. Parameter boundaries for lease duration (sub-second or exceeding max lease).
func TestAdversarialM4_LeaseStealingBoundaryConditions(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "lease_boundary.db")
	s, err := coord.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	proj, owner, err := s.CreateProject(ctx, "lease-boundary", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishPlan(ctx, proj.ID, owner.Token, "Plan", 0); err != nil {
		t.Fatal(err)
	}

	workerA, err := s.AddPrincipal(ctx, proj.ID, owner.Token, "worker-A", "worker")
	if err != nil {
		t.Fatal(err)
	}
	workerB, err := s.AddPrincipal(ctx, proj.ID, owner.Token, "worker-B", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgePlan(ctx, proj.ID, workerA.Token, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgePlan(ctx, proj.ID, workerB.Token, 1); err != nil {
		t.Fatal(err)
	}

	task, err := s.CreateTask(ctx, proj.ID, owner.Token, 1, "Lease Test Task", "Testing boundary", "research",
		coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Parameter checks on RenewLease
	// A. Renew on un-claimed task
	if _, err := s.RenewLease(ctx, proj.ID, workerA.Token, task.ID, 1, 5*time.Second); !errors.Is(err, coord.ErrConflict) {
		t.Errorf("expected ErrConflict renewing unclaimed task, got %v", err)
	}

	// B. Sub-second lease duration check
	if _, err := s.RenewLease(ctx, proj.ID, workerA.Token, task.ID, 1, 500*time.Millisecond); err == nil || !strings.Contains(err.Error(), "between one second and one hour") {
		t.Errorf("expected sub-second lease validation error, got %v", err)
	}

	// Claim task with 1-second lease by Worker A
	claimed, err := s.ClaimTask(ctx, proj.ID, workerA.Token, task.ID, 1*time.Second)
	if err != nil {
		t.Fatalf("worker A claim failed: %v", err)
	}
	if claimed.Attempt != 1 || claimed.Owner != workerA.PrincipalID {
		t.Fatalf("unexpected claim state: attempt=%d, owner=%s", claimed.Attempt, claimed.Owner)
	}

	// 1. Premature steal attempt: Worker B tries to claim immediately while lease is active
	_, err = s.ClaimTask(ctx, proj.ID, workerB.Token, task.ID, 5*time.Second)
	if !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("expected ErrConflict on premature steal attempt, got %v", err)
	}

	// Verify task remains untouched
	taskState, err := s.GetTask(ctx, proj.ID, owner.Token, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if taskState.Attempt != 1 || taskState.Owner != workerA.PrincipalID {
		t.Fatalf("task state corrupted by premature steal: attempt=%d, owner=%s", taskState.Attempt, taskState.Owner)
	}

	// 2. Successful renewal by Worker A while lease is still unexpired
	renewed, err := s.RenewLease(ctx, proj.ID, workerA.Token, task.ID, 1, 2*time.Second)
	if err != nil {
		t.Fatalf("worker A renew failed: %v", err)
	}
	if renewed.Attempt != 1 {
		t.Errorf("renewed attempt changed: want 1, got %d", renewed.Attempt)
	}
	if renewed.LeaseUntil <= claimed.LeaseUntil {
		t.Errorf("renewed lease_until (%d) not greater than previous (%d)", renewed.LeaseUntil, claimed.LeaseUntil)
	}

	// 3. Wait for lease to expire completely
	time.Sleep(2100 * time.Millisecond)

	// 4. Exact expiration transition: Worker B steals expired task
	stolen, err := s.ClaimTask(ctx, proj.ID, workerB.Token, task.ID, 10*time.Second)
	if err != nil {
		t.Fatalf("worker B failed to steal expired lease: %v", err)
	}
	if stolen.Attempt != 2 {
		t.Errorf("stolen attempt number = %d, want 2 (fencing increment)", stolen.Attempt)
	}
	if stolen.Owner != workerB.PrincipalID {
		t.Errorf("stolen owner = %q, want %q", stolen.Owner, workerB.PrincipalID)
	}

	// 5. Stale attempt fencing: Worker A tries to renew superseded attempt 1
	_, err = s.RenewLease(ctx, proj.ID, workerA.Token, task.ID, 1, 5*time.Second)
	if !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("expected ErrConflict when worker A renews stale attempt 1, got %v", err)
	}

	// Worker A tries to renew attempt 2 (which belongs to Worker B)
	_, err = s.RenewLease(ctx, proj.ID, workerA.Token, task.ID, 2, 5*time.Second)
	if !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("expected ErrConflict when worker A tries to renew worker B's attempt 2, got %v", err)
	}

	// Worker A tries to submit a report for stale attempt 1
	_, err = s.SubmitReport(ctx, proj.ID, workerA.Token, task.ID, 1, "deadbeef", "stale report attempt")
	if !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("expected ErrConflict when submitting report under stale attempt 1, got %v", err)
	}

	// 6. Concurrency race: Renewal vs Steal
	// Create another task with 1-second lease
	taskRace, err := s.CreateTask(ctx, proj.ID, owner.Token, 1, "Race Task", "Race", "research",
		coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ClaimTask(ctx, proj.ID, workerA.Token, taskRace.ID, 1*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	// Wait 980ms so we are right at the boundary
	time.Sleep(980 * time.Millisecond)

	var wg sync.WaitGroup
	var renewErr, stealErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, renewErr = s.RenewLease(ctx, proj.ID, workerA.Token, taskRace.ID, 1, 2*time.Second)
	}()
	go func() {
		defer wg.Done()
		_, stealErr = s.ClaimTask(ctx, proj.ID, workerB.Token, taskRace.ID, 5*time.Second)
	}()
	wg.Wait()

	// Invariant: Exactly one must succeed, the other must get ErrConflict
	if renewErr == nil && stealErr == nil {
		t.Fatal("CRITICAL INVARIANT VIOLATION: both renewal and steal succeeded simultaneously")
	}
	if renewErr != nil && stealErr != nil {
		// Both could fail if renew arrived after expiration and steal arrived before expiration,
		// but both errors must be ErrConflict
		if !errors.Is(renewErr, coord.ErrConflict) || !errors.Is(stealErr, coord.ErrConflict) {
			t.Fatalf("unexpected error pair: renewErr=%v, stealErr=%v", renewErr, stealErr)
		}
	} else if renewErr == nil {
		if !errors.Is(stealErr, coord.ErrConflict) {
			t.Errorf("worker B steal should have returned ErrConflict, got %v", stealErr)
		}
	} else {
		if !errors.Is(renewErr, coord.ErrConflict) {
			t.Errorf("worker A renew should have returned ErrConflict, got %v", renewErr)
		}
	}
}

// TestAdversarialM4_DependencyCascadeAndDAGEnforcement tests:
// 1. Multi-prerequisite AND gate (Task C depends on [A, B]; unblocks ONLY when both A and B are accepted).
// 2. Diamond DAG: Root -> [Branch1, Branch2] -> Join.
// 3. Deep sequential cascade (4-step dependency chain).
// 4. Duplicate prerequisite rejection and self-dependency / cycle isolation.
// 5. Interaction between prerequisite unblocking and explicit blockers (TransitionTask block/resolve).
func TestAdversarialM4_DependencyCascadeAndDAGEnforcement(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "dep_cascade.db")
	s, err := coord.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	proj, owner, err := s.CreateProject(ctx, "dep-cascade", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishPlan(ctx, proj.ID, owner.Token, "Plan", 0); err != nil {
		t.Fatal(err)
	}
	worker, err := s.AddPrincipal(ctx, proj.ID, owner.Token, "worker", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgePlan(ctx, proj.ID, worker.Token, 1); err != nil {
		t.Fatal(err)
	}

	helperCompleteTask := func(taskID string) {
		t.Helper()
		claimed, err := s.ClaimTask(ctx, proj.ID, worker.Token, taskID, 30*time.Second)
		if err != nil {
			t.Fatalf("failed to claim task %s: %v", taskID, err)
		}
		digest := "digest-" + taskID
		rep, err := s.SubmitReport(ctx, proj.ID, worker.Token, taskID, claimed.Attempt, digest, "completed "+taskID)
		if err != nil {
			t.Fatalf("failed to submit report for %s: %v", taskID, err)
		}
		if err := s.AcceptReport(ctx, proj.ID, owner.Token, rep.ID, rep.Digest); err != nil {
			t.Fatalf("failed to accept report for %s: %v", taskID, err)
		}
	}

	// 1. Multi-prerequisite AND gate: Task C depends on [Task A, Task B]
	taskA, err := s.CreateTask(ctx, proj.ID, owner.Token, 1, "Task A", "Prereq A", "research",
		coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	taskB, err := s.CreateTask(ctx, proj.ID, owner.Token, 1, "Task B", "Prereq B", "research",
		coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	taskC, err := s.CreateTask(ctx, proj.ID, owner.Token, 1, "Task C", "Dependent on A and B", "implementation",
		coord.ReportContract{ReviewType: "full_code"}, []string{taskA.ID, taskB.ID})
	if err != nil {
		t.Fatal(err)
	}

	if taskC.Status != "blocked" {
		t.Fatalf("task C status = %q, want blocked", taskC.Status)
	}

	// Complete Task A only
	helperCompleteTask(taskA.ID)

	// Verify Task C is STILL blocked because Task B is not accepted
	stateC, err := s.GetTask(ctx, proj.ID, owner.Token, taskC.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stateC.Status != "blocked" {
		t.Fatalf("task C status after only A completed = %q, want blocked", stateC.Status)
	}

	// Complete Task B
	helperCompleteTask(taskB.ID)

	// Verify Task C is now AUTOMATICALLY ready
	stateC2, err := s.GetTask(ctx, proj.ID, owner.Token, taskC.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stateC2.Status != "ready" {
		t.Fatalf("task C status after A and B completed = %q, want ready", stateC2.Status)
	}

	// 2. Diamond DAG: Root -> [Left, Right] -> Join
	taskRoot, err := s.CreateTask(ctx, proj.ID, owner.Token, 1, "Diamond Root", "Root", "research",
		coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	taskLeft, err := s.CreateTask(ctx, proj.ID, owner.Token, 1, "Diamond Left", "Left", "research",
		coord.ReportContract{ReviewType: "full_code"}, []string{taskRoot.ID})
	if err != nil {
		t.Fatal(err)
	}
	taskRight, err := s.CreateTask(ctx, proj.ID, owner.Token, 1, "Diamond Right", "Right", "research",
		coord.ReportContract{ReviewType: "full_code"}, []string{taskRoot.ID})
	if err != nil {
		t.Fatal(err)
	}
	taskJoin, err := s.CreateTask(ctx, proj.ID, owner.Token, 1, "Diamond Join", "Join", "implementation",
		coord.ReportContract{ReviewType: "full_code"}, []string{taskLeft.ID, taskRight.ID})
	if err != nil {
		t.Fatal(err)
	}

	if taskLeft.Status != "blocked" || taskRight.Status != "blocked" || taskJoin.Status != "blocked" {
		t.Fatalf("diamond initial status mismatch: left=%s, right=%s, join=%s", taskLeft.Status, taskRight.Status, taskJoin.Status)
	}

	// Accept Root -> Left and Right become ready, Join remains blocked
	helperCompleteTask(taskRoot.ID)

	sLeft, _ := s.GetTask(ctx, proj.ID, owner.Token, taskLeft.ID)
	sRight, _ := s.GetTask(ctx, proj.ID, owner.Token, taskRight.ID)
	sJoin, _ := s.GetTask(ctx, proj.ID, owner.Token, taskJoin.ID)

	if sLeft.Status != "ready" || sRight.Status != "ready" {
		t.Fatalf("branches not ready after root accept: left=%s, right=%s", sLeft.Status, sRight.Status)
	}
	if sJoin.Status != "blocked" {
		t.Fatalf("join should remain blocked: got %s", sJoin.Status)
	}

	// Complete Left -> Join remains blocked
	helperCompleteTask(taskLeft.ID)
	sJoin2, _ := s.GetTask(ctx, proj.ID, owner.Token, taskJoin.ID)
	if sJoin2.Status != "blocked" {
		t.Fatalf("join should remain blocked after only left accepted: got %s", sJoin2.Status)
	}

	// Complete Right -> Join becomes ready
	helperCompleteTask(taskRight.ID)
	sJoin3, _ := s.GetTask(ctx, proj.ID, owner.Token, taskJoin.ID)
	if sJoin3.Status != "ready" {
		t.Fatalf("join should be ready after both branches accepted: got %s", sJoin3.Status)
	}

	// 3. Deep cascade chain (T1 -> T2 -> T3 -> T4)
	tasks := make([]coord.Task, 4)
	for i := 0; i < 4; i++ {
		var deps []string
		if i > 0 {
			deps = []string{tasks[i-1].ID}
		}
		tItem, err := s.CreateTask(ctx, proj.ID, owner.Token, 1, fmt.Sprintf("Chain %d", i), "chain", "research",
			coord.ReportContract{ReviewType: "full_code"}, deps)
		if err != nil {
			t.Fatal(err)
		}
		tasks[i] = tItem
	}

	// Check cascading unblocking sequentially
	for i := 0; i < 3; i++ {
		curr, _ := s.GetTask(ctx, proj.ID, owner.Token, tasks[i].ID)
		if curr.Status != "ready" {
			t.Fatalf("chain task %d should be ready, got %s", i, curr.Status)
		}
		next, _ := s.GetTask(ctx, proj.ID, owner.Token, tasks[i+1].ID)
		if next.Status != "blocked" {
			t.Fatalf("chain task %d should be blocked, got %s", i+1, next.Status)
		}

		helperCompleteTask(tasks[i].ID)

		nextAfter, _ := s.GetTask(ctx, proj.ID, owner.Token, tasks[i+1].ID)
		if nextAfter.Status != "ready" {
			t.Fatalf("chain task %d should transition to ready after predecessor accepted, got %s", i+1, nextAfter.Status)
		}
	}

	// 4. Duplicate prerequisite validation
	_, err = s.CreateTask(ctx, proj.ID, owner.Token, 1, "Dup Dep", "Dup", "research",
		coord.ReportContract{ReviewType: "full_code"}, []string{tasks[0].ID, tasks[0].ID})
	if err == nil || !strings.Contains(err.Error(), "duplicate prerequisite") {
		t.Fatalf("expected duplicate prerequisite error, got %v", err)
	}

	// Non-existent prerequisite validation
	_, err = s.CreateTask(ctx, proj.ID, owner.Token, 1, "Missing Dep", "Missing", "research",
		coord.ReportContract{ReviewType: "full_code"}, []string{"task_nonexistent_xyz"})
	if !errors.Is(err, coord.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for non-existent prerequisite, got %v", err)
	}

	// 5. Interaction between prerequisite acceptance and explicit blockers
	pBlock, err := s.CreateTask(ctx, proj.ID, owner.Token, 1, "Prereq for Blocked", "p", "research",
		coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	depBlocked, err := s.CreateTask(ctx, proj.ID, owner.Token, 1, "Blocked Dependent", "d", "research",
		coord.ReportContract{ReviewType: "full_code"}, []string{pBlock.ID})
	if err != nil {
		t.Fatal(err)
	}

	// Claim depBlocked to transition it, then block it with reason
	helperCompleteTask(pBlock.ID)
	stReady, _ := s.GetTask(ctx, proj.ID, owner.Token, depBlocked.ID)
	if stReady.Status != "ready" {
		t.Fatalf("expected depBlocked ready, got %s", stReady.Status)
	}

	// Claim depBlocked
	claimDep, err := s.ClaimTask(ctx, proj.ID, worker.Token, depBlocked.ID, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	// Worker blocks it
	blockedTask, err := s.TransitionTask(ctx, proj.ID, worker.Token, depBlocked.ID, "block", "waiting for external API", claimDep.Attempt)
	if err != nil {
		t.Fatalf("failed to block task: %v", err)
	}
	if blockedTask.Status != "blocked" || blockedTask.BlockerReason != "waiting for external API" {
		t.Fatalf("unexpected blocked state: status=%s, reason=%s", blockedTask.Status, blockedTask.BlockerReason)
	}

	// Owner resolves the blocker
	resolvedTask, err := s.TransitionTask(ctx, proj.ID, owner.Token, depBlocked.ID, "resolve", "", 0)
	if err != nil {
		t.Fatalf("failed to resolve blocker: %v", err)
	}
	if resolvedTask.Status != "ready" {
		t.Fatalf("expected task ready after blocker resolved, got %s", resolvedTask.Status)
	}
}

// TestAdversarialM4_CoordinatorRestartStress tests SQLite WAL resilience:
// 1. Rapid open/close cycles under load without connection leaks or database locking.
// 2. Uncommitted in-flight transactions (crash simulation via killed subprocess) ensuring rollback and no dirty writes.
// 3. Active lease recovery across coordinator restart and expiration steal after restart.
func TestAdversarialM4_CoordinatorRestartStress(t *testing.T) {
	ctx := context.Background()
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "restart_stress.db")

	var savedOwnerToken string
	var savedProjID string

	// Phase 1: Rapid Open / Write / Close loop (50 iterations)
	for i := 0; i < 50; i++ {
		s, err := coord.Open(dbPath)
		if err != nil {
			t.Fatalf("iteration %d: failed to open store: %v", i, err)
		}

		if i == 0 {
			proj, owner, err := s.CreateProject(ctx, "restart-test", "owner")
			if err != nil {
				s.Close()
				t.Fatalf("iteration 0: failed to create project: %v", err)
			}
			if _, err := s.PublishPlan(ctx, proj.ID, owner.Token, "Plan 1", 0); err != nil {
				s.Close()
				t.Fatal(err)
			}
			savedProjID = proj.ID
			savedOwnerToken = owner.Token
		} else {
			plan, err := s.CurrentPlan(ctx, savedProjID, savedOwnerToken)
			if err != nil {
				s.Close()
				t.Fatalf("iteration %d: failed to get current plan: %v", i, err)
			}
			if plan.Revision != 1 {
				s.Close()
				t.Fatalf("iteration %d: plan revision mismatch: %d != 1", i, plan.Revision)
			}
		}

		if err := s.Close(); err != nil {
			t.Fatalf("iteration %d: failed to close store: %v", i, err)
		}
	}

	// Phase 2: In-flight uncommitted transaction crash simulation via subprocess
	// A helper subprocess opens SQLite directly, begins tx, inserts a dirty row, and calls os.Exit(42)
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperCrashWorker$")
	cmd.Env = append(os.Environ(),
		"TEST_CRASH_WORKER=1",
		"TEST_CRASH_DB="+dbPath,
		"TEST_CRASH_PROJ="+savedProjID,
	)
	_ = cmd.Run() // Subprocess terminated abnormally with exit code 42

	// Re-open store through coordinator
	s2, err := coord.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to reopen store after uncommitted crash: %v", err)
	}
	defer s2.Close()

	// Verify dirty task does NOT exist in the reopened database
	var count int
	rawCheckDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer rawCheckDB.Close()

	if err := rawCheckDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE id='task_dirty_uncommitted_subprocess'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("CRITICAL FLAW: dirty uncommitted task survived crash rollback! count=%d", count)
	}

	// Phase 3: Active lease persistence across restart and post-restart steal
	pFresh, ownerFresh, err := s2.CreateProject(ctx, "restart-lease", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.PublishPlan(ctx, pFresh.ID, ownerFresh.Token, "Plan", 0); err != nil {
		t.Fatal(err)
	}
	w1, err := s2.AddPrincipal(ctx, pFresh.ID, ownerFresh.Token, "w1", "worker")
	if err != nil {
		t.Fatal(err)
	}
	w2, err := s2.AddPrincipal(ctx, pFresh.ID, ownerFresh.Token, "w2", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.AcknowledgePlan(ctx, pFresh.ID, w1.Token, 1); err != nil {
		t.Fatal(err)
	}
	if err := s2.AcknowledgePlan(ctx, pFresh.ID, w2.Token, 1); err != nil {
		t.Fatal(err)
	}

	leaseTask, err := s2.CreateTask(ctx, pFresh.ID, ownerFresh.Token, 1, "Lease Restart Task", "Restart", "research",
		coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Worker 1 claims task with 1-second lease
	claimed, err := s2.ClaimTask(ctx, pFresh.ID, w1.Token, leaseTask.ID, 1*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Status != "running" || claimed.Attempt != 1 {
		t.Fatalf("unexpected claim: status=%s, attempt=%d", claimed.Status, claimed.Attempt)
	}

	// Abruptly close coordinator store
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}

	// Wait 1200ms so lease expires while coordinator is offline
	time.Sleep(1200 * time.Millisecond)

	// Reopen coordinator
	s3, err := coord.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s3.Close()

	// Worker 2 should now be able to steal the expired lease on the restarted coordinator
	stolen, err := s3.ClaimTask(ctx, pFresh.ID, w2.Token, leaseTask.ID, 10*time.Second)
	if err != nil {
		t.Fatalf("worker 2 failed to steal expired task on restarted coordinator: %v", err)
	}
	if stolen.Owner != w2.PrincipalID || stolen.Attempt != 2 {
		t.Fatalf("post-restart steal corrupted: owner=%s, attempt=%d", stolen.Owner, stolen.Attempt)
	}
}

// TestAdversarialM4_MessageOrderingAndOutboxIdempotency tests message exchange:
// 1. Concurrent messages between multiple workers preserve ordering and project isolation.
// 2. Private direct messaging between workers is invisible to project owner.
// 3. Duplicate request_id submissions are deduplicated idempotently.
func TestAdversarialM4_MessageOrderingAndOutboxIdempotency(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "msg_stress.db")
	s, err := coord.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	proj, owner, err := s.CreateProject(ctx, "msg-test", "owner")
	if err != nil {
		t.Fatal(err)
	}
	w1, err := s.AddPrincipal(ctx, proj.ID, owner.Token, "worker-1", "worker")
	if err != nil {
		t.Fatal(err)
	}
	w2, err := s.AddPrincipal(ctx, proj.ID, owner.Token, "worker-2", "worker")
	if err != nil {
		t.Fatal(err)
	}

	// 1. Worker 1 sends message to Worker 2 with stable request_id
	const reqID = "stable-msg-req-001"
	body := "Private peer message for coordination"
	msg1, err := s.SendMessageWithID(ctx, proj.ID, w1.Token, w2.PrincipalID, "thread-1", "Subject", body, reqID)
	if err != nil {
		t.Fatalf("send message failed: %v", err)
	}

	// Retry with identical request_id and body (idempotency check)
	msg2, err := s.SendMessageWithID(ctx, proj.ID, w1.Token, w2.PrincipalID, "thread-1", "Subject", body, reqID)
	if err != nil {
		t.Fatalf("retrying message with identical request_id failed: %v", err)
	}
	if msg1.ID != msg2.ID {
		t.Errorf("idempotent message ID mismatch: %s != %s", msg1.ID, msg2.ID)
	}

	// Retry with same request_id but DIFFERENT body (conflict check)
	_, err = s.SendMessageWithID(ctx, proj.ID, w1.Token, w2.PrincipalID, "thread-1", "Subject", "Altered payload", reqID)
	if !errors.Is(err, coord.ErrConflict) {
		t.Errorf("expected ErrConflict on altered payload for same request_id, got %v", err)
	}

	// 2. Worker 2 reads recent messages and receives it
	msgsW2, err := s.RecentMessages(ctx, proj.ID, w2.Token, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgsW2) != 1 || msgsW2[0].Body != body {
		t.Fatalf("worker 2 did not receive message properly: %v", msgsW2)
	}

	// 3. Owner reads recent messages: MUST NOT see the direct peer message between w1 and w2
	msgsOwner, err := s.RecentMessages(ctx, proj.ID, owner.Token, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgsOwner) != 0 {
		t.Fatalf("PRIVACY LEAK: owner can see private direct message between peers: %v", msgsOwner)
	}

	// 4. Concurrent message bombardment (30 messages sent concurrently)
	var wg sync.WaitGroup
	errCh := make(chan error, 30)
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			rID := fmt.Sprintf("req-concurrent-%03d", idx)
			mBody := fmt.Sprintf("Concurrent message payload %d", idx)
			_, sendErr := s.SendMessageWithID(ctx, proj.ID, w1.Token, w2.PrincipalID, "thread-concurrent", "Subject", mBody, rID)
			if sendErr != nil {
				errCh <- sendErr
			}
		}(i)
	}
	wg.Wait()
	close(errCh)

	for e := range errCh {
		t.Fatalf("concurrent message send failed: %v", e)
	}

	// Verify all 31 messages are present for w2
	allMsgs, err := s.RecentMessages(ctx, proj.ID, w2.Token, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(allMsgs) != 31 {
		t.Errorf("expected 31 total messages for worker 2, got %d", len(allMsgs))
	}
}
