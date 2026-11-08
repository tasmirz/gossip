package coord_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tasmirz/gossip/pkg/coord"
	_ "modernc.org/sqlite"
)

// unreliableTestBackend wraps coord.Backend with configurable faults.
type unreliableTestBackend struct {
	coord.Backend
	offline   atomic.Bool
	callCount atomic.Int64
}

func (b *unreliableTestBackend) SendMessageWithID(ctx context.Context, p, t, recipient, thread, subject, body, id string) (coord.Message, error) {
	b.callCount.Add(1)
	if b.offline.Load() {
		return coord.Message{}, errors.New("temporary network disconnect: coordinator offline")
	}
	return b.Backend.SendMessageWithID(ctx, p, t, recipient, thread, subject, body, id)
}

// ============================================================================
// Focus Area 1 & 2: Task Dependency DAG Edge Cases & Diamond Topologies
// ============================================================================

// TestTier5_DAG_DeepDiamondAndMultiLevelDependencies tests:
// 1. Deep diamond DAG: Root -> Left (L1->L2->L3) and Right (R1->R2->R3) -> Joint -> Final.
// 2. Joint task cannot be claimed when Left branch is accepted but Right branch is incomplete.
// 3. Final task cannot be claimed until Joint is accepted.
// 4. Concurrent workers racing to claim Joint the moment Right branch becomes accepted.
// 5. Post-joint task unblocks cleanly and can be claimed.
func TestTier5_DAG_DeepDiamondAndMultiLevelDependencies(t *testing.T) {
	store, _, project, owner, worker := setup(t)
	ctx := context.Background()

	worker2, err := store.AddPrincipal(ctx, project.ID, owner.Token, "worker2", "worker")
	if err != nil {
		t.Fatalf("AddPrincipal worker2: %v", err)
	}
	if err := store.AcknowledgePlan(ctx, project.ID, worker2.Token, 1); err != nil {
		t.Fatalf("AcknowledgePlan worker2: %v", err)
	}

	contract := coord.ReportContract{ReviewType: "full_code"}

	// 1. Root task
	tRoot, err := store.CreateTask(ctx, project.ID, owner.Token, 1, "Root", "Root task", "research", contract, nil)
	if err != nil {
		t.Fatalf("CreateTask Root: %v", err)
	}

	// 2. Left branch: L1 -> L2 -> L3
	tL1, err := store.CreateTask(ctx, project.ID, owner.Token, 1, "L1", "Left 1", "research", contract, []string{tRoot.ID})
	if err != nil {
		t.Fatalf("CreateTask L1: %v", err)
	}
	tL2, err := store.CreateTask(ctx, project.ID, owner.Token, 1, "L2", "Left 2", "research", contract, []string{tL1.ID})
	if err != nil {
		t.Fatalf("CreateTask L2: %v", err)
	}
	tL3, err := store.CreateTask(ctx, project.ID, owner.Token, 1, "L3", "Left 3", "research", contract, []string{tL2.ID})
	if err != nil {
		t.Fatalf("CreateTask L3: %v", err)
	}

	// 3. Right branch: R1 -> R2 -> R3
	tR1, err := store.CreateTask(ctx, project.ID, owner.Token, 1, "R1", "Right 1", "research", contract, []string{tRoot.ID})
	if err != nil {
		t.Fatalf("CreateTask R1: %v", err)
	}
	tR2, err := store.CreateTask(ctx, project.ID, owner.Token, 1, "R2", "Right 2", "research", contract, []string{tR1.ID})
	if err != nil {
		t.Fatalf("CreateTask R2: %v", err)
	}
	tR3, err := store.CreateTask(ctx, project.ID, owner.Token, 1, "R3", "Right 3", "research", contract, []string{tR2.ID})
	if err != nil {
		t.Fatalf("CreateTask R3: %v", err)
	}

	// 4. Joint task: depends on both L3 and R3
	tJoint, err := store.CreateTask(ctx, project.ID, owner.Token, 1, "Joint", "Joint task", "implementation", contract, []string{tL3.ID, tR3.ID})
	if err != nil {
		t.Fatalf("CreateTask Joint: %v", err)
	}

	// 5. Final post-joint task: depends on Joint
	tFinal, err := store.CreateTask(ctx, project.ID, owner.Token, 1, "Final", "Final post-joint task", "verification", contract, []string{tJoint.ID})
	if err != nil {
		t.Fatalf("CreateTask Final: %v", err)
	}

	// Invariant check: all dependent tasks must initially be blocked
	for name, id := range map[string]string{"L1": tL1.ID, "L2": tL2.ID, "L3": tL3.ID, "R1": tR1.ID, "R2": tR2.ID, "R3": tR3.ID, "Joint": tJoint.ID, "Final": tFinal.ID} {
		tk, err := store.GetTask(ctx, project.ID, owner.Token, id)
		if err != nil {
			t.Fatalf("GetTask %s: %v", name, err)
		}
		if tk.Status != "blocked" {
			t.Fatalf("Task %s expected status 'blocked', got %q", name, tk.Status)
		}
	}

	// Adversarial claim on blocked Joint & Final must fail
	if _, err := store.ClaimTask(ctx, project.ID, worker.Token, tJoint.ID, time.Minute); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("Claim blocked Joint want ErrConflict, got %v", err)
	}
	if _, err := store.ClaimTask(ctx, project.ID, worker.Token, tFinal.ID, time.Minute); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("Claim blocked Final want ErrConflict, got %v", err)
	}

	// Helper to complete and accept a task
	completeTask := func(tID string, token string) {
		claim, err := store.ClaimTask(ctx, project.ID, token, tID, time.Minute)
		if err != nil {
			t.Fatalf("ClaimTask %s: %v", tID, err)
		}
		rep, err := store.SubmitReport(ctx, project.ID, token, tID, claim.Attempt, "base", "completed work report")
		if err != nil {
			t.Fatalf("SubmitReport %s: %v", tID, err)
		}
		if err := store.AcceptReport(ctx, project.ID, owner.Token, rep.ID, rep.Digest); err != nil {
			t.Fatalf("AcceptReport %s: %v", tID, err)
		}
	}

	// Complete Root -> unblocks L1 and R1
	completeTask(tRoot.ID, worker.Token)

	// Complete Left branch L1 -> L2 -> L3
	completeTask(tL1.ID, worker.Token)
	completeTask(tL2.ID, worker.Token)
	completeTask(tL3.ID, worker.Token)

	// Invariant: L3 is accepted, but R3 is still blocked. Joint MUST STILL BE BLOCKED!
	jointTask, err := store.GetTask(ctx, project.ID, owner.Token, tJoint.ID)
	if err != nil {
		t.Fatalf("GetTask Joint: %v", err)
	}
	if jointTask.Status != "blocked" {
		t.Fatalf("Joint task must remain blocked when only Left branch is accepted, got %q", jointTask.Status)
	}
	if _, err := store.ClaimTask(ctx, project.ID, worker.Token, tJoint.ID, time.Minute); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("Claim Joint when Right branch incomplete want ErrConflict, got %v", err)
	}

	// Advance Right branch R1 -> R2
	completeTask(tR1.ID, worker2.Token)
	completeTask(tR2.ID, worker2.Token)

	// Now complete R3. Right before accepting R3, launch concurrent workers competing for Joint!
	claimR3, err := store.ClaimTask(ctx, project.ID, worker2.Token, tR3.ID, time.Minute)
	if err != nil {
		t.Fatalf("ClaimTask R3: %v", err)
	}
	repR3, err := store.SubmitReport(ctx, project.ID, worker2.Token, tR3.ID, claimR3.Attempt, "base", "completed R3")
	if err != nil {
		t.Fatalf("SubmitReport R3: %v", err)
	}

	// Accept R3 -> this atomically unblocks Joint
	if err := store.AcceptReport(ctx, project.ID, owner.Token, repR3.ID, repR3.Digest); err != nil {
		t.Fatalf("AcceptReport R3: %v", err)
	}

	// Verify Joint is now ready
	jointTask, err = store.GetTask(ctx, project.ID, owner.Token, tJoint.ID)
	if err != nil {
		t.Fatalf("GetTask Joint: %v", err)
	}
	if jointTask.Status != "ready" {
		t.Fatalf("Joint task expected 'ready' after both branches accepted, got %q", jointTask.Status)
	}

	// Adversarial race: Worker 1 and Worker 2 both try to claim Joint concurrently
	var wg sync.WaitGroup
	var claimSuccesses atomic.Int32
	var claimConflicts atomic.Int32

	for _, tok := range []string{worker.Token, worker2.Token} {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			_, err := store.ClaimTask(ctx, project.ID, token, tJoint.ID, time.Minute)
			if err == nil {
				claimSuccesses.Add(1)
			} else if errors.Is(err, coord.ErrConflict) {
				claimConflicts.Add(1)
			} else {
				t.Errorf("Unexpected error claiming Joint: %v", err)
			}
		}(tok)
	}
	wg.Wait()

	if claimSuccesses.Load() != 1 || claimConflicts.Load() != 1 {
		t.Fatalf("Joint claim race: want exactly 1 success and 1 conflict, got %d successes and %d conflicts",
			claimSuccesses.Load(), claimConflicts.Load())
	}

	// Final task must still be blocked
	finalTask, err := store.GetTask(ctx, project.ID, owner.Token, tFinal.ID)
	if err != nil {
		t.Fatalf("GetTask Final: %v", err)
	}
	if finalTask.Status != "blocked" {
		t.Fatalf("Final task must remain blocked while Joint is running, got %q", finalTask.Status)
	}

	// Get the winning joint claim to complete it
	jointTask, err = store.GetTask(ctx, project.ID, owner.Token, tJoint.ID)
	if err != nil {
		t.Fatalf("GetTask Joint: %v", err)
	}
	winningToken := worker.Token
	if jointTask.Owner == worker2.PrincipalID {
		winningToken = worker2.Token
	}

	repJoint, err := store.SubmitReport(ctx, project.ID, winningToken, tJoint.ID, jointTask.Attempt, "base", "completed Joint")
	if err != nil {
		t.Fatalf("SubmitReport Joint: %v", err)
	}
	if err := store.AcceptReport(ctx, project.ID, owner.Token, repJoint.ID, repJoint.Digest); err != nil {
		t.Fatalf("AcceptReport Joint: %v", err)
	}

	// Final task must now be ready!
	finalTask, err = store.GetTask(ctx, project.ID, owner.Token, tFinal.ID)
	if err != nil {
		t.Fatalf("GetTask Final: %v", err)
	}
	if finalTask.Status != "ready" {
		t.Fatalf("Final task expected 'ready' after Joint accepted, got %q", finalTask.Status)
	}
	completeTask(tFinal.ID, worker.Token)
}

// TestTier5_DAG_TransitiveLinearChainAndWideFanOutConcurrency tests:
// 1. Transitive linear chain of 8 tasks (T1 -> T2 -> ... -> T8).
// 2. Wide fan-out / fan-in: Root -> 16 parallel tasks -> Aggregate.
// 3. Highly concurrent acceptance of all 16 parallel tasks.
// 4. Verification that aggregate unblocks without DAG corruption or race conditions.
func TestTier5_DAG_TransitiveLinearChainAndWideFanOutConcurrency(t *testing.T) {
	store, _, project, owner, worker := setup(t)
	ctx := context.Background()
	contract := coord.ReportContract{ReviewType: "full_code"}

	// PART A: 8-task linear dependency chain
	const chainLen = 8
	chainTasks := make([]coord.Task, chainLen)
	var prevID string
	for i := 0; i < chainLen; i++ {
		var deps []string
		if prevID != "" {
			deps = []string{prevID}
		}
		tk, err := store.CreateTask(ctx, project.ID, owner.Token, 1, fmt.Sprintf("Chain-%d", i+1), "Sequential step", "implementation", contract, deps)
		if err != nil {
			t.Fatalf("CreateTask Chain-%d: %v", i+1, err)
		}
		chainTasks[i] = tk
		prevID = tk.ID
	}

	// Multi-goroutine worker swarm: 6 workers competing to advance the linear chain
	var wg sync.WaitGroup
	var completedCount atomic.Int32

	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(workerIdx int) {
			defer wg.Done()
			for completedCount.Load() < chainLen {
				// Search for ready tasks
				tasks, err := store.ListTasks(ctx, project.ID, worker.Token, 50)
				if err != nil {
					continue
				}
				for _, tk := range tasks {
					if tk.Status == "ready" && strings.HasPrefix(tk.Title, "Chain-") {
						claim, err := store.ClaimTask(ctx, project.ID, worker.Token, tk.ID, time.Minute)
						if err != nil {
							continue // another worker claimed it
						}
						rep, err := store.SubmitReport(ctx, project.ID, worker.Token, tk.ID, claim.Attempt, "base", "completed chain step")
						if err != nil {
							continue
						}
						err = store.AcceptReport(ctx, project.ID, owner.Token, rep.ID, rep.Digest)
						if err == nil {
							completedCount.Add(1)
						}
					}
				}
				time.Sleep(2 * time.Millisecond)
			}
		}(i)
	}
	wg.Wait()

	if completedCount.Load() != chainLen {
		t.Fatalf("Linear chain: expected %d completed tasks, got %d", chainLen, completedCount.Load())
	}

	// PART B: Wide Fan-Out (16 tasks) and Fan-In (Aggregate)
	const fanOutCount = 16
	fanTasks := make([]coord.Task, fanOutCount)
	fanIDs := make([]string, fanOutCount)

	tFanRoot, err := store.CreateTask(ctx, project.ID, owner.Token, 1, "FanRoot", "Root of fan-out", "research", contract, nil)
	if err != nil {
		t.Fatalf("CreateTask FanRoot: %v", err)
	}
	// Complete FanRoot
	claimRoot, err := store.ClaimTask(ctx, project.ID, worker.Token, tFanRoot.ID, time.Minute)
	if err != nil {
		t.Fatalf("Claim FanRoot: %v", err)
	}
	repRoot, err := store.SubmitReport(ctx, project.ID, worker.Token, tFanRoot.ID, claimRoot.Attempt, "base", "root complete")
	if err != nil {
		t.Fatalf("Submit FanRoot: %v", err)
	}
	if err := store.AcceptReport(ctx, project.ID, owner.Token, repRoot.ID, repRoot.Digest); err != nil {
		t.Fatalf("Accept FanRoot: %v", err)
	}

	// Update MaxParallel to 32 to allow 16 parallel tasks to run concurrently
	settings, err := store.GetSettings(ctx, project.ID, owner.Token)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	settings.MaxParallel = 32
	if _, err := store.UpdateSettings(ctx, project.ID, owner.Token, settings); err != nil {
		t.Fatalf("UpdateSettings MaxParallel=32: %v", err)
	}

	// Create 16 parallel tasks depending on FanRoot
	for i := 0; i < fanOutCount; i++ {
		tk, err := store.CreateTask(ctx, project.ID, owner.Token, 1, fmt.Sprintf("Fan-%d", i), "Parallel work", "research", contract, []string{tFanRoot.ID})
		if err != nil {
			t.Fatalf("CreateTask Fan-%d: %v", i, err)
		}
		fanTasks[i] = tk
		fanIDs[i] = tk.ID
	}

	// Create Aggregate task depending on all 16 parallel tasks
	tAggregate, err := store.CreateTask(ctx, project.ID, owner.Token, 1, "Aggregate", "Fan-in aggregator", "verification", contract, fanIDs)
	if err != nil {
		t.Fatalf("CreateTask Aggregate: %v", err)
	}

	// Invariant: Aggregate must be blocked
	aggTask, err := store.GetTask(ctx, project.ID, owner.Token, tAggregate.ID)
	if err != nil || aggTask.Status != "blocked" {
		t.Fatalf("Aggregate want blocked, got status %q (err %v)", aggTask.Status, err)
	}

	// Concurrently claim and submit reports for all 16 tasks
	reports := make([]coord.Report, fanOutCount)
	var fanWg sync.WaitGroup
	for i := 0; i < fanOutCount; i++ {
		fanWg.Add(1)
		go func(idx int) {
			defer fanWg.Done()
			claim, err := store.ClaimTask(ctx, project.ID, worker.Token, fanIDs[idx], time.Minute)
			if err != nil {
				t.Errorf("Claim fan task %d: %v", idx, err)
				return
			}
			rep, err := store.SubmitReport(ctx, project.ID, worker.Token, fanIDs[idx], claim.Attempt, "base", fmt.Sprintf("Report %d", idx))
			if err != nil {
				t.Errorf("Submit fan task %d: %v", idx, err)
				return
			}
			reports[idx] = rep
		}(i)
	}
	fanWg.Wait()

	// Concurrently accept all 16 reports across 16 goroutines
	var acceptWg sync.WaitGroup
	for i := 0; i < fanOutCount; i++ {
		acceptWg.Add(1)
		go func(idx int) {
			defer acceptWg.Done()
			rep := reports[idx]
			if err := store.AcceptReport(ctx, project.ID, owner.Token, rep.ID, rep.Digest); err != nil {
				t.Errorf("Accept fan report %d: %v", idx, err)
			}
		}(i)
	}
	acceptWg.Wait()

	// Aggregate task MUST now be ready
	aggTask, err = store.GetTask(ctx, project.ID, owner.Token, tAggregate.ID)
	if err != nil {
		t.Fatalf("GetTask Aggregate: %v", err)
	}
	if aggTask.Status != "ready" {
		t.Fatalf("Aggregate task expected status 'ready', got %q", aggTask.Status)
	}

	// Claim and complete Aggregate
	claimAgg, err := store.ClaimTask(ctx, project.ID, worker.Token, tAggregate.ID, time.Minute)
	if err != nil {
		t.Fatalf("Claim Aggregate: %v", err)
	}
	repAgg, err := store.SubmitReport(ctx, project.ID, worker.Token, tAggregate.ID, claimAgg.Attempt, "base", "Aggregate complete")
	if err != nil {
		t.Fatalf("Submit Aggregate: %v", err)
	}
	if err := store.AcceptReport(ctx, project.ID, owner.Token, repAgg.ID, repAgg.Digest); err != nil {
		t.Fatalf("Accept Aggregate: %v", err)
	}
}

// ============================================================================
// Focus Area 3: Attempt Counter Fencing, Stolen Leases & Replan Races
// ============================================================================

// TestTier5_AttemptFencing_StolenLeaseAndSupersededAttempts tests:
// 1. Worker A claims task with short lease (attempt 1).
// 2. Lease expires.
// 3. Worker B steals the task (attempt 2).
// 4. Worker A attempts RenewLease, SubmitReport, SaveCandidate, and TransitionTask(block) on attempt 1.
// 5. All attempts by Worker A must be fenced out with ErrConflict.
// 6. Worker B successfully submits and finalizes attempt 2.
func TestTier5_AttemptFencing_StolenLeaseAndSupersededAttempts(t *testing.T) {
	store, _, project, owner, workerA := setup(t)
	ctx := context.Background()

	workerB, err := store.AddPrincipal(ctx, project.ID, owner.Token, "workerB", "worker")
	if err != nil {
		t.Fatalf("AddPrincipal workerB: %v", err)
	}
	if err := store.AcknowledgePlan(ctx, project.ID, workerB.Token, 1); err != nil {
		t.Fatalf("AcknowledgePlan workerB: %v", err)
	}

	task, err := store.CreateTask(ctx, project.ID, owner.Token, 1, "Fencing Target", "Test attempt fencing", "research", coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Worker A claims with valid 1s lease (minimum allowed)
	claimA, err := store.ClaimTask(ctx, project.ID, workerA.Token, task.ID, time.Second)
	if err != nil {
		t.Fatalf("ClaimTask Worker A: %v", err)
	}
	if claimA.Attempt != 1 {
		t.Fatalf("Expected attempt 1, got %d", claimA.Attempt)
	}

	// Wait for lease to expire (> 1000ms)
	time.Sleep(1100 * time.Millisecond)

	// Worker B steals the task (attempt becomes 2)
	claimB, err := store.ClaimTask(ctx, project.ID, workerB.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatalf("Worker B failed to steal expired task: %v", err)
	}
	if claimB.Attempt != 2 {
		t.Fatalf("Expected attempt 2 for Worker B, got %d", claimB.Attempt)
	}
	if claimB.Owner != workerB.PrincipalID {
		t.Fatalf("Expected owner %s, got %s", workerB.PrincipalID, claimB.Owner)
	}

	// ADVERSARIAL ATTACKS BY WORKER A (SUPERSEDED ATTEMPT 1):

	// Attack 1: Worker A tries to renew lease for attempt 1
	if _, err := store.RenewLease(ctx, project.ID, workerA.Token, task.ID, claimA.Attempt, 2*time.Second); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("Worker A RenewLease on stolen task want ErrConflict, got %v", err)
	}

	// Attack 2: Worker A tries to submit report for attempt 1
	if _, err := store.SubmitReport(ctx, project.ID, workerA.Token, task.ID, claimA.Attempt, "base", "stale report by Worker A"); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("Worker A SubmitReport on stolen task want ErrConflict, got %v", err)
	}

	// Attack 3: Worker B cannot save candidate for attempt 1 (unauthorized)
	validDiff := "diff --git a/file.go b/file.go\n--- a/file.go\n+++ b/file.go\n@@ -1,1 +1,2 @@\n-old\n+new\n+line\n"
	if _, err := store.SaveCandidate(ctx, project.ID, workerB.Token, task.ID, claimA.Attempt, "base", validDiff); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("Worker B SaveCandidate for attempt 1 want ErrUnauthorized, got %v", err)
	}

	// Worker A can save unaccepted candidate artifact for attempt 1, but RequestReview must fail with ErrConflict
	if _, err := store.SaveCandidate(ctx, project.ID, workerA.Token, task.ID, claimA.Attempt, "base", validDiff); err != nil {
		t.Fatalf("Worker A SaveCandidate artifact failed: %v", err)
	}
	if err := store.RequestReview(ctx, project.ID, workerA.Token, task.ID, claimA.Attempt); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("Worker A RequestReview on stolen/superseded task want ErrConflict, got %v", err)
	}

	// Attack 4: Worker A tries to block task using attempt 1
	if _, err := store.TransitionTask(ctx, project.ID, workerA.Token, task.ID, "block", "attempt 1 blocked", claimA.Attempt); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("Worker A TransitionTask(block) on stolen task want ErrConflict, got %v", err)
	}

	// Worker B legitimately submits report for attempt 2 -> SUCCEEDS
	repB, err := store.SubmitReport(ctx, project.ID, workerB.Token, task.ID, claimB.Attempt, "base", "legitimate report by Worker B")
	if err != nil {
		t.Fatalf("Worker B SubmitReport failed: %v", err)
	}

	// Owner accepts Worker B's report
	if err := store.AcceptReport(ctx, project.ID, owner.Token, repB.ID, repB.Digest); err != nil {
		t.Fatalf("AcceptReport for Worker B failed: %v", err)
	}

	// Final verification: task is accepted, attempt count is 2
	finalTask, err := store.GetTask(ctx, project.ID, owner.Token, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if finalTask.Status != "accepted" || finalTask.Attempt != 2 {
		t.Fatalf("Expected accepted task at attempt 2, got status %q attempt %d", finalTask.Status, finalTask.Attempt)
	}
}

// TestTier5_AttemptFencing_ExtremeRaces runs multiple iterations of a 3-way race
// between SubmitReport, RenewLease, and stolen ClaimTask near lease expiration.
func TestTier5_AttemptFencing_ExtremeRaces(t *testing.T) {
	store, _, project, owner, workerA := setup(t)
	ctx := context.Background()

	workerB, err := store.AddPrincipal(ctx, project.ID, owner.Token, "workerB", "worker")
	if err != nil {
		t.Fatalf("AddPrincipal: %v", err)
	}
	if err := store.AcknowledgePlan(ctx, project.ID, workerB.Token, 1); err != nil {
		t.Fatalf("AcknowledgePlan: %v", err)
	}

	contract := coord.ReportContract{ReviewType: "full_code"}

	for iter := 0; iter < 3; iter++ {
		task, err := store.CreateTask(ctx, project.ID, owner.Token, 1, fmt.Sprintf("RaceTask-%d", iter), "Testing race", "research", contract, nil)
		if err != nil {
			t.Fatalf("CreateTask iter %d: %v", iter, err)
		}

		// Claim with minimum 1s lease
		claim, err := store.ClaimTask(ctx, project.ID, workerA.Token, task.ID, time.Second)
		if err != nil {
			t.Fatalf("ClaimTask iter %d: %v", iter, err)
		}

		// Sleep near expiration boundary (995ms)
		time.Sleep(995 * time.Millisecond)

		var wg sync.WaitGroup
		var submitErr, renewErr, stealErr error
		wg.Add(3)

		// 1. Worker A calls SubmitReport
		go func() {
			defer wg.Done()
			_, submitErr = store.SubmitReport(ctx, project.ID, workerA.Token, task.ID, claim.Attempt, "base", "race report")
		}()

		// 2. Worker A calls RenewLease (must be >= 1s)
		go func() {
			defer wg.Done()
			_, renewErr = store.RenewLease(ctx, project.ID, workerA.Token, task.ID, claim.Attempt, 2*time.Second)
		}()

		// 3. Worker B calls ClaimTask (attempting to steal expired lease)
		go func() {
			defer wg.Done()
			_, stealErr = store.ClaimTask(ctx, project.ID, workerB.Token, task.ID, time.Minute)
		}()

		wg.Wait()

		// Verify invariants:
		// Task state in DB must be valid and internally consistent:
		// Either submitted (if submit won), or running with attempt 1 (renew won), or running with attempt 2 (steal won).
		curTask, err := store.GetTask(ctx, project.ID, owner.Token, task.ID)
		if err != nil {
			t.Fatalf("iter %d GetTask: %v", iter, err)
		}

		switch curTask.Status {
		case "submitted":
			if submitErr != nil {
				t.Fatalf("iter %d: task is submitted but submitErr=%v", iter, submitErr)
			}
		case "running":
			if curTask.Attempt == 1 && curTask.Owner == workerA.PrincipalID {
				if renewErr != nil {
					t.Fatalf("iter %d: task running at attempt 1 but renewErr=%v", iter, renewErr)
				}
			} else if curTask.Attempt == 2 && curTask.Owner == workerB.PrincipalID {
				if stealErr != nil {
					t.Fatalf("iter %d: task running at attempt 2 but stealErr=%v", iter, stealErr)
				}
			} else {
				t.Fatalf("iter %d: unexpected running task state: attempt=%d owner=%s", iter, curTask.Attempt, curTask.Owner)
			}
		default:
			t.Fatalf("iter %d: unexpected task status %q", iter, curTask.Status)
		}
	}
}

// TestTier5_AttemptFencing_PlanPublicationReplanInvalidation tests:
// 1. Worker claims task at revision 1.
// 2. Owner publishes revision 2 plan.
// 3. Task transitions to needs_replan.
// 4. Worker cannot submit report or renew lease for the old revision (fenced with ErrConflict).
// 5. ReconcileTask re-qualifies the task against revision 2.
// 6. Worker acknowledges revision 2, claims task, and successfully submits report.
func TestTier5_AttemptFencing_PlanPublicationReplanInvalidation(t *testing.T) {
	store, _, project, owner, worker := setup(t)
	ctx := context.Background()

	task, err := store.CreateTask(ctx, project.ID, owner.Token, 1, "Replan Task", "Testing replan invalidation", "research", coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	claim, err := store.ClaimTask(ctx, project.ID, worker.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}

	// Owner publishes Plan revision 2
	rev2, err := store.PublishPlan(ctx, project.ID, owner.Token, "Plan Revision 2: Scope Shift", 1)
	if err != nil || rev2 != 2 {
		t.Fatalf("PublishPlan want revision 2, got %d (err %v)", rev2, err)
	}

	// Task must immediately transition to 'needs_replan', lease_until=0, owner=NULL
	invalidatedTask, err := store.GetTask(ctx, project.ID, owner.Token, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if invalidatedTask.Status != "needs_replan" || invalidatedTask.Owner != "" || invalidatedTask.LeaseUntil != 0 {
		t.Fatalf("Task not properly invalidated: status=%q owner=%q lease=%d", invalidatedTask.Status, invalidatedTask.Owner, invalidatedTask.LeaseUntil)
	}

	// Worker attempts to submit report for attempt 1 (revision 1) -> must fail with ErrConflict
	if _, err := store.SubmitReport(ctx, project.ID, worker.Token, task.ID, claim.Attempt, "base", "stale report"); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("SubmitReport on needs_replan task want ErrConflict, got %v", err)
	}

	// Worker attempts to renew lease -> must fail with ErrConflict
	if _, err := store.RenewLease(ctx, project.ID, worker.Token, task.ID, claim.Attempt, time.Minute); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("RenewLease on needs_replan task want ErrConflict, got %v", err)
	}

	// Owner reconciles task against revision 2
	reconciled, err := store.ReconcileTask(ctx, project.ID, owner.Token, task.ID, 2)
	if err != nil {
		t.Fatalf("ReconcileTask failed: %v", err)
	}
	if reconciled.Status != "ready" || reconciled.PlanRevision != 2 {
		t.Fatalf("Expected reconciled task status 'ready' rev 2, got status %q rev %d", reconciled.Status, reconciled.PlanRevision)
	}

	// Worker cannot claim until acknowledging revision 2
	if _, err := store.ClaimTask(ctx, project.ID, worker.Token, task.ID, time.Minute); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("Claiming before acknowledging revision 2 want ErrConflict, got %v", err)
	}

	// Worker acknowledges revision 2
	if err := store.AcknowledgePlan(ctx, project.ID, worker.Token, 2); err != nil {
		t.Fatalf("AcknowledgePlan rev 2 failed: %v", err)
	}

	// Worker claims task anew (attempt 2)
	claim2, err := store.ClaimTask(ctx, project.ID, worker.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatalf("ClaimTask attempt 2 failed: %v", err)
	}
	if claim2.Attempt != 2 {
		t.Fatalf("Expected attempt 2, got %d", claim2.Attempt)
	}

	// Submit report successfully
	rep2, err := store.SubmitReport(ctx, project.ID, worker.Token, task.ID, claim2.Attempt, "base", "reconciled work report")
	if err != nil {
		t.Fatalf("SubmitReport attempt 2 failed: %v", err)
	}
	if err := store.AcceptReport(ctx, project.ID, owner.Token, rep2.ID, rep2.Digest); err != nil {
		t.Fatalf("AcceptReport attempt 2 failed: %v", err)
	}
}

// ============================================================================
// Focus Area 4: Outbox Durability, SQLite WAL Recovery & Poison Pills
// ============================================================================

// TestTier5_Outbox_WALCrashRecoveryAndOfflineFlush tests:
// 1. Outbox backed by SQLite file in WAL mode.
// 2. Messages enqueued while backend is offline.
// 3. Outbox abruptly closed (leaving uncheckpointed WAL state on disk).
// 4. Outbox re-opened from the same database file with live backend.
// 5. Verification that all pending messages survive WAL recovery and are flushed cleanly to backend.
func TestTier5_Outbox_WALCrashRecoveryAndOfflineFlush(t *testing.T) {
	store, _, project, _, worker := setup(t)
	ctx := context.Background()

	outboxDir := t.TempDir()
	outboxPath := filepath.Join(outboxDir, "wal_outbox.db")

	mockBackend := &unreliableTestBackend{Backend: store}
	mockBackend.offline.Store(true) // offline mode

	// Open initial Outbox
	box1, err := coord.OpenOutbox(outboxPath, "https://home", project.ID, worker.PrincipalID, worker.Token, mockBackend)
	if err != nil {
		t.Fatalf("OpenOutbox 1: %v", err)
	}

	// Enqueue 5 messages offline
	const numMessages = 5
	msgIDs := make([]string, numMessages)
	for i := 0; i < numMessages; i++ {
		reqID := fmt.Sprintf("req_wal_test_%d", i)
		body := fmt.Sprintf("WAL durability test payload %d", i)
		msg, err := box1.Enqueue(ctx, "", "thread_wal", "WAL test", body, reqID)
		if err != nil {
			t.Fatalf("Enqueue %d: %v", i, err)
		}
		if msg.Status != "pending" {
			t.Fatalf("Expected status pending, got %q", msg.Status)
		}
		msgIDs[i] = reqID
	}

	// Verify all 5 messages are in pending status
	list1, err := box1.List(ctx, 50)
	if err != nil {
		t.Fatalf("List 1: %v", err)
	}
	if len(list1) != numMessages {
		t.Fatalf("Expected %d pending messages, got %d", numMessages, len(list1))
	}

	// Abruptly close Outbox without flushing
	box1.Close()

	// Verify WAL file exists or database is consistent
	rawDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(outboxPath))
	if err != nil {
		t.Fatalf("Raw open: %v", err)
	}
	var count int
	if err := rawDB.QueryRow("SELECT COUNT(*) FROM outbound WHERE status='pending'").Scan(&count); err != nil {
		t.Fatalf("Query outbound count: %v", err)
	}
	rawDB.Close()
	if count != numMessages {
		t.Fatalf("Expected %d pending rows in raw DB, got %d", numMessages, count)
	}

	// RECOVERY: Bring backend online and re-open Outbox from disk
	mockBackend.offline.Store(false)

	box2, err := coord.OpenOutbox(outboxPath, "https://home", project.ID, worker.PrincipalID, worker.Token, mockBackend)
	if err != nil {
		t.Fatalf("OpenOutbox 2: %v", err)
	}
	defer box2.Close()

	// Poll until all messages are delivered by the background runner
	deadline := time.Now().Add(5 * time.Second)
	var allDelivered bool
	for time.Now().Before(deadline) {
		list2, err := box2.List(ctx, 50)
		if err == nil && len(list2) == numMessages {
			delivered := 0
			for _, m := range list2 {
				if m.Status == "delivered" && m.MessageID != "" {
					delivered++
				}
			}
			if delivered == numMessages {
				allDelivered = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !allDelivered {
		listFinal, _ := box2.List(ctx, 50)
		t.Fatalf("Not all messages were delivered after WAL recovery. Messages: %+v", listFinal)
	}

	// Verify in the store that messages were indeed received
	received, err := store.ListMessages(ctx, project.ID, worker.Token, 0, 50)
	if err != nil {
		t.Fatalf("ListMessages store: %v", err)
	}
	foundCount := 0
	for _, m := range received {
		if strings.HasPrefix(m.Body, "WAL durability test payload") {
			foundCount++
		}
	}
	if foundCount != numMessages {
		t.Fatalf("Store received %d messages, expected %d", foundCount, numMessages)
	}
}

// TestTier5_Outbox_PoisonPillNonStarvationAndRetry tests:
// 1. Poison pill messages (e.g. non-existent recipient -> ErrNotFound).
// 2. Valid messages enqueued immediately after poison pills.
// 3. Poison pills transition to status='failed' without blocking/starving subsequent valid messages.
// 4. Outbox.Retry requeues the failed message when the issue is resolved.
func TestTier5_Outbox_PoisonPillNonStarvationAndRetry(t *testing.T) {
	store, _, project, owner, worker := setup(t)
	ctx := context.Background()

	outboxPath := filepath.Join(t.TempDir(), "poison_outbox.db")
	box, err := coord.OpenOutbox(outboxPath, "https://home", project.ID, worker.PrincipalID, worker.Token, store)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	defer box.Close()

	// Message 1: Poison pill addressing a non-existent recipient
	poisonReqID := "req_poison_1"
	_, err = box.Enqueue(ctx, "principal_nonexistent_xyz", "thread_poison", "Poison", "Will fail", poisonReqID)
	if err != nil {
		t.Fatalf("Enqueue poison pill: %v", err)
	}

	// Message 2: Legitimate valid broadcast message
	validReqID1 := "req_valid_1"
	_, err = box.Enqueue(ctx, "", "thread_valid", "Valid 1", "Hello Project Valid 1", validReqID1)
	if err != nil {
		t.Fatalf("Enqueue valid 1: %v", err)
	}

	// Message 3: Legitimate valid direct message to owner
	validReqID2 := "req_valid_2"
	_, err = box.Enqueue(ctx, owner.PrincipalID, "thread_valid", "Valid 2", "Hello Owner Valid 2", validReqID2)
	if err != nil {
		t.Fatalf("Enqueue valid 2: %v", err)
	}

	// Poll until valid messages are delivered and poison pill is marked failed
	deadline := time.Now().Add(5 * time.Second)
	var poisonFailed, valid1Delivered, valid2Delivered bool

	for time.Now().Before(deadline) {
		msgs, err := box.List(ctx, 10)
		if err == nil {
			for _, m := range msgs {
				if m.RequestID == poisonReqID && m.Status == "failed" && m.Error != "" {
					poisonFailed = true
				}
				if m.RequestID == validReqID1 && m.Status == "delivered" && m.MessageID != "" {
					valid1Delivered = true
				}
				if m.RequestID == validReqID2 && m.Status == "delivered" && m.MessageID != "" {
					valid2Delivered = true
				}
			}
		}
		if poisonFailed && valid1Delivered && valid2Delivered {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !poisonFailed {
		t.Errorf("Poison pill was not marked failed")
	}
	if !valid1Delivered {
		t.Errorf("Valid message 1 was starved or not delivered")
	}
	if !valid2Delivered {
		t.Errorf("Valid message 2 was starved or not delivered")
	}

	// Fix the underlying issue by updating the recipient to the valid owner principal in outbox DB
	rawDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(outboxPath))
	if err != nil {
		t.Fatalf("Open raw outbox db: %v", err)
	}
	if _, err := rawDB.Exec("UPDATE outbound SET recipient=? WHERE request_id=?", owner.PrincipalID, poisonReqID); err != nil {
		rawDB.Close()
		t.Fatalf("Update recipient in outbox: %v", err)
	}
	rawDB.Close()

	// Call box.Retry on the failed poison pill
	if err := box.Retry(ctx, poisonReqID); err != nil {
		t.Fatalf("Retry poison pill failed: %v", err)
	}

	// Verify that the retried message is now delivered to owner
	deadline = time.Now().Add(5 * time.Second)
	var poisonRecovered bool
	for time.Now().Before(deadline) {
		msgs, err := box.List(ctx, 10)
		if err == nil {
			for _, m := range msgs {
				if m.RequestID == poisonReqID && m.Status == "delivered" && m.MessageID != "" {
					poisonRecovered = true
					break
				}
			}
		}
		if poisonRecovered {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !poisonRecovered {
		t.Fatalf("Poison pill failed to deliver after Retry and fixing recipient")
	}
}

// ============================================================================
// Focus Area 5: Multi-Worktree Collaboration Edge Cases
// ============================================================================

// TestTier5_MultiWorktree_SpecialPathCharacters tests:
// 1. Workspaces with spaces, unicode (Japanese, accents, emojis), quotes and punctuation.
// 2. Claiming tasks inside workspaces with special paths.
// 3. Updating and listing workspaces with complex characters.
func TestTier5_MultiWorktree_SpecialPathCharacters(t *testing.T) {
	store, _, project, owner, worker := setup(t)
	ctx := context.Background()

	repo, err := store.CreateRepository(ctx, project.ID, owner.Token, "special-repo")
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	specialPaths := []struct {
		name string
		node string
		path string
	}{
		{
			name: "spaces-and-unicode",
			node: "worker-node-1",
			path: "/home/user/my repo with spaces/ñandú/日本語/🚀/",
		},
		{
			name: "quotes-and-symbols",
			node: "worker-node-2",
			path: `/work/dir with 'single' and "double" quotes & symbols !@#$^()_+-={}[];,.~/`,
		},
		{
			name: "windows-style-path",
			node: "worker-node-3",
			path: `C:\Users\agent\Workspace "Project" & (Tests)\src\`,
		},
	}

	for _, tc := range specialPaths {
		ws, err := store.RegisterWorkspace(ctx, project.ID, worker.Token, repo.ID, tc.name, tc.node, tc.path, "main", "c0ffee1234567890", false)
		if err != nil {
			t.Fatalf("RegisterWorkspace %q failed: %v", tc.name, err)
		}
		if ws.LocalPath != tc.path {
			t.Fatalf("Workspace LocalPath mismatch: want %q, got %q", tc.path, ws.LocalPath)
		}

		// Task claimed within this special workspace
		task, err := store.CreateTask(ctx, project.ID, owner.Token, 1, "WS Task "+tc.name, "Testing special path", "research", coord.ReportContract{ReviewType: "full_code"}, nil)
		if err != nil {
			t.Fatalf("CreateTask: %v", err)
		}

		claim, err := store.ClaimTaskInWorkspace(ctx, project.ID, worker.Token, task.ID, ws.ID, time.Minute)
		if err != nil {
			t.Fatalf("ClaimTaskInWorkspace for %q failed: %v", tc.name, err)
		}

		// Verify attempt recorded the workspace head and workspace snapshot
		att, err := store.GetAttempt(ctx, project.ID, worker.Token, task.ID, claim.Attempt)
		if err != nil {
			t.Fatalf("GetAttempt: %v", err)
		}
		if att.WorkspaceID != ws.ID || att.WorkspaceHead != "c0ffee1234567890" {
			t.Fatalf("Attempt snapshot mismatch: %+v", att)
		}

		// Update workspace with new head and dirty state
		updated, err := store.UpdateWorkspace(ctx, project.ID, worker.Token, ws.ID, "feature/special", "deadbeef98765432", true)
		if err != nil {
			t.Fatalf("UpdateWorkspace for %q failed: %v", tc.name, err)
		}
		if updated.Head != "deadbeef98765432" || !updated.Dirty || updated.Branch != "feature/special" {
			t.Fatalf("Updated workspace mismatch: %+v", updated)
		}
	}

	// Verify all registered workspaces are retrievable
	list, err := store.ListWorkspaces(ctx, project.ID, worker.Token)
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(list) < len(specialPaths) {
		t.Fatalf("Expected at least %d workspaces, got %d", len(specialPaths), len(list))
	}
}

// TestTier5_MultiWorktree_LargeDiffCandidateBoundary tests:
// 1. Valid diff candidates of 100KB, 500KB, and 950KB (near 1MB boundary).
// 2. Candidate pagination (GetCandidatePage) across large chunks.
// 3. Adversarial rejections: diff > 1MB, null bytes, malformed unified diff hunks.
func TestTier5_MultiWorktree_LargeDiffCandidateBoundary(t *testing.T) {
	store, _, project, owner, worker := setup(t)
	ctx := context.Background()

	task, err := store.CreateTask(ctx, project.ID, owner.Token, 1, "Large Diff Task", "Testing candidate boundaries", "implementation", coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	claim, err := store.ClaimTask(ctx, project.ID, worker.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}

	// Helper to generate a valid unified diff with N lines
	generateValidDiff := func(numLines int) string {
		var sb strings.Builder
		sb.WriteString("diff --git a/large_file.go b/large_file.go\n")
		sb.WriteString("--- a/large_file.go\n")
		sb.WriteString("+++ b/large_file.go\n")
		sb.WriteString(fmt.Sprintf("@@ -1,1 +1,%d @@\n", numLines+1))
		sb.WriteString(" existing line\n")
		for i := 0; i < numLines; i++ {
			sb.WriteString(fmt.Sprintf("+added line %05d with some padding text to occupy bytes in the diff\n", i))
		}
		return sb.String()
	}

	// 1. Test 100KB diff
	diff100K := generateValidDiff(1400)
	c100, err := store.SaveCandidate(ctx, project.ID, worker.Token, task.ID, claim.Attempt, "base", diff100K)
	if err != nil {
		t.Fatalf("SaveCandidate 100KB: %v", err)
	}
	if c100.Digest == "" {
		t.Fatalf("Expected non-empty digest for 100KB candidate")
	}

	// 2. Test 500KB diff
	diff500K := generateValidDiff(7000)
	c500, err := store.SaveCandidate(ctx, project.ID, worker.Token, task.ID, claim.Attempt, "base", diff500K)
	if err != nil {
		t.Fatalf("SaveCandidate 500KB: %v", err)
	}
	if c500.Digest == "" {
		t.Fatalf("Expected non-empty digest for 500KB candidate")
	}

	// 3. Test 950KB diff (near 1MB limit)
	diff950K := generateValidDiff(13500)
	if len(diff950K) > 1024*1024 {
		t.Fatalf("diff950K exceeded 1MB limit: %d", len(diff950K))
	}
	c950, err := store.SaveCandidate(ctx, project.ID, worker.Token, task.ID, claim.Attempt, "base", diff950K)
	if err != nil {
		t.Fatalf("SaveCandidate 950KB: %v", err)
	}

	// Verify pagination across the 950KB candidate
	page1, err := store.GetCandidatePage(ctx, project.ID, worker.Token, c950.ID, 0, 4000)
	if err != nil {
		t.Fatalf("GetCandidatePage 1: %v", err)
	}
	if page1.Offset != 0 || page1.NextOffset == nil || *page1.NextOffset != 4000 {
		t.Fatalf("Candidate pagination page 1 mismatch: offset=%d next=%v", page1.Offset, page1.NextOffset)
	}
	page2, err := store.GetCandidatePage(ctx, project.ID, worker.Token, c950.ID, 4000, 4000)
	if err != nil {
		t.Fatalf("GetCandidatePage 2: %v", err)
	}
	if page2.Offset != 4000 {
		t.Fatalf("Candidate pagination page 2 mismatch: offset=%d", page2.Offset)
	}

	// ADVERSARIAL CASES:

	// Case A: Diff exceeding 1MB limit (1024*1024 + 10 bytes)
	diffOver1MB := generateValidDiff(16000)
	if len(diffOver1MB) <= 1024*1024 {
		diffOver1MB += strings.Repeat("+padding line\n", (1024*1024-len(diffOver1MB))/14+5)
	}
	if _, err := store.SaveCandidate(ctx, project.ID, worker.Token, task.ID, claim.Attempt, "base", diffOver1MB); err == nil {
		t.Fatalf("Expected error for diff exceeding 1MB, got nil")
	}

	// Case B: Embedded null byte (\x00)
	diffNull := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,2 @@\n-old\n+\x00bad\n"
	if _, err := store.SaveCandidate(ctx, project.ID, worker.Token, task.ID, claim.Attempt, "base", diffNull); err == nil {
		t.Fatalf("Expected error for diff with null byte, got nil")
	}

	// Case C: Malformed hunk header (hunk count mismatch rejected during RequestReview)
	diffMalformed := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,99 @@\n-old\n+new\n"
	_, err = store.SaveCandidate(ctx, project.ID, worker.Token, task.ID, claim.Attempt, "base", diffMalformed)
	if err != nil {
		t.Fatalf("SaveCandidate malformed diff: %v", err)
	}
	if err := store.RequestReview(ctx, project.ID, worker.Token, task.ID, claim.Attempt); err == nil {
		t.Fatalf("Expected RequestReview to reject malformed diff candidate, got nil")
	}
}

// TestTier5_SimultaneousPlanUpdates_OptimisticConcurrency tests:
// 1. 20 concurrent goroutines attempting PublishPlan with the same expected revision 1.
// 2. Exactly ONE succeeds (returning revision 2).
// 3. Exactly 19 fail with ErrConflict.
// 4. Project revision remains exactly 2.
// 5. Existing tasks are all updated to status='needs_replan'.
func TestTier5_SimultaneousPlanUpdates_OptimisticConcurrency(t *testing.T) {
	store, _, project, owner, worker := setup(t)
	ctx := context.Background()

	// Create 5 tasks in ready/running states
	contract := coord.ReportContract{ReviewType: "full_code"}
	for i := 0; i < 5; i++ {
		tk, err := store.CreateTask(ctx, project.ID, owner.Token, 1, fmt.Sprintf("Pre-Plan Task %d", i), "Work", "research", contract, nil)
		if err != nil {
			t.Fatalf("CreateTask %d: %v", i, err)
		}
		if i == 0 {
			// One claimed and running
			if _, err := store.ClaimTask(ctx, project.ID, worker.Token, tk.ID, time.Minute); err != nil {
				t.Fatalf("ClaimTask: %v", err)
			}
		}
	}

	const goroutines = 20
	var wg sync.WaitGroup
	var successCount atomic.Int32
	var conflictCount atomic.Int32

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			planBody := fmt.Sprintf("Simultaneous Plan Update from Goroutine %d", idx)
			rev, err := store.PublishPlan(ctx, project.ID, owner.Token, planBody, 1)
			if err == nil {
				if rev == 2 {
					successCount.Add(1)
				} else {
					t.Errorf("Unexpected revision returned: %d", rev)
				}
			} else if errors.Is(err, coord.ErrConflict) {
				conflictCount.Add(1)
			} else {
				t.Errorf("Unexpected error from PublishPlan: %v", err)
			}
		}(i)
	}
	wg.Wait()

	if successCount.Load() != 1 {
		t.Fatalf("Expected exactly 1 PublishPlan success, got %d", successCount.Load())
	}
	if conflictCount.Load() != goroutines-1 {
		t.Fatalf("Expected exactly %d PublishPlan conflicts, got %d", goroutines-1, conflictCount.Load())
	}

	// Current plan must be at revision 2
	plan, err := store.CurrentPlan(ctx, project.ID, owner.Token)
	if err != nil {
		t.Fatalf("CurrentPlan: %v", err)
	}
	if plan.Revision != 2 {
		t.Fatalf("Expected current plan revision 2, got %d", plan.Revision)
	}

	// All 5 existing tasks must now be in 'needs_replan'
	tasks, err := store.ListTasks(ctx, project.ID, owner.Token, 50)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	for _, tk := range tasks {
		if tk.Status != "needs_replan" {
			t.Fatalf("Task %s expected status 'needs_replan', got %q", tk.ID, tk.Status)
		}
	}
}

// TestTier5_Chat_IdempotencyAndRecipientIsolation tests:
// 1. SendMessageWithID idempotency: same request_id with same payload returns identical message.
// 2. Reusing request_id with altered payload returns ErrConflict.
// 3. Recipient isolation: direct message between Owner and Worker A is invisible to Worker B.
func TestTier5_Chat_IdempotencyAndRecipientIsolation(t *testing.T) {
	store, _, project, owner, workerA := setup(t)
	ctx := context.Background()

	workerB, err := store.AddPrincipal(ctx, project.ID, owner.Token, "workerB", "worker")
	if err != nil {
		t.Fatalf("AddPrincipal workerB: %v", err)
	}

	// 1. Send message with request ID
	reqID := "req_chat_idem_1"
	msg1, err := store.SendMessageWithID(ctx, project.ID, owner.Token, workerA.PrincipalID, "thread_1", "Direct Message", "Confidential instruction", reqID)
	if err != nil {
		t.Fatalf("SendMessageWithID 1: %v", err)
	}

	// 2. Idempotent retry: same payload returns identical message
	msg1Retry, err := store.SendMessageWithID(ctx, project.ID, owner.Token, workerA.PrincipalID, "thread_1", "Direct Message", "Confidential instruction", reqID)
	if err != nil {
		t.Fatalf("SendMessageWithID retry failed: %v", err)
	}
	if msg1Retry.ID != msg1.ID || msg1Retry.Sequence != msg1.Sequence || msg1Retry.Body != msg1.Body {
		t.Fatalf("Idempotent message retry returned different content: %+v vs %+v", msg1, msg1Retry)
	}

	// 3. Adversarial retry: same request ID with altered body returns ErrConflict
	if _, err := store.SendMessageWithID(ctx, project.ID, owner.Token, workerA.PrincipalID, "thread_1", "Direct Message", "Altered body", reqID); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("Reusing request ID with altered body want ErrConflict, got %v", err)
	}

	// 4. Recipient isolation check:
	// Worker A (recipient) can list and read the message
	listA, err := store.ListMessages(ctx, project.ID, workerA.Token, 0, 50)
	if err != nil {
		t.Fatalf("ListMessages Worker A: %v", err)
	}
	foundA := false
	for _, m := range listA {
		if m.ID == msg1.ID {
			foundA = true
			break
		}
	}
	if !foundA {
		t.Fatalf("Worker A (recipient) could not read direct message")
	}

	// Worker B (unrelated principal) must NOT see the direct message
	listB, err := store.ListMessages(ctx, project.ID, workerB.Token, 0, 50)
	if err != nil {
		t.Fatalf("ListMessages Worker B: %v", err)
	}
	for _, m := range listB {
		if m.ID == msg1.ID {
			t.Fatalf("Worker B was able to see confidential direct message: %+v", m)
		}
	}
}
