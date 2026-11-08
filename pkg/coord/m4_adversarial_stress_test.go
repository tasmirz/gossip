package coord_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tasmirz/gossip/pkg/coord"
)

// ============================================================================
// Adversarial Stress Test 1: Concurrent Certificate Grant Revocation
// ============================================================================
// Floods the TLS RPC endpoint with concurrent requests while simultaneously
// revoking the client certificate grant.
// Asserts:
// - All in-flight requests return EITHER HTTP 200 OR HTTP 403 (never 500/panic/deadlock).
// - Once RevokeCertificateGrant returns, ALL subsequent requests return HTTP 403.
// - Revocation is permanent and does not oscillate.
// - Other certificates (e.g. OtherCert) are unaffected.
// - Local token validity in SQLite remains unaffected.
func TestM4Challenger_ConcurrentCertRevocation(t *testing.T) {
	fixture := setupMultiWorktreeTLS(t)
	ctx := context.Background()

	// Verify baseline connectivity: HTTP 200
	status, _ := makeRawTLSCall(t, fixture.URL, fixture.CAFile, fixture.ClientCertFile, fixture.ClientKeyFile, fixture.Worker.Token, fixture.Project.ID, "plan.read", nil)
	if status != http.StatusOK {
		t.Fatalf("expected baseline HTTP 200, got %d", status)
	}

	const numWorkers = 16
	const requestsPerWorker = 30
	var wg sync.WaitGroup

	var count200 int64
	var count403 int64
	var countOther int64
	var unexpectedErrors []string
	var errMu sync.Mutex

	recordErr := func(msg string) {
		errMu.Lock()
		unexpectedErrors = append(unexpectedErrors, msg)
		errMu.Unlock()
	}

	startGate := make(chan struct{})

	// Launch worker goroutines flooding requests
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			<-startGate
			for i := 0; i < requestsPerWorker; i++ {
				st, body := makeRawTLSCall(t, fixture.URL, fixture.CAFile, fixture.ClientCertFile, fixture.ClientKeyFile, fixture.Worker.Token, fixture.Project.ID, "plan.read", nil)
				switch st {
				case http.StatusOK:
					atomic.AddInt64(&count200, 1)
				case http.StatusForbidden:
					atomic.AddInt64(&count403, 1)
					if !strings.Contains(body, "client certificate is not authorized") {
						recordErr(fmt.Sprintf("worker %d req %d: 403 body unexpected: %s", workerID, i, body))
					}
				default:
					atomic.AddInt64(&countOther, 1)
					recordErr(fmt.Sprintf("worker %d req %d: unexpected status %d (body: %s)", workerID, i, st, body))
				}
				time.Sleep(1 * time.Millisecond)
			}
		}(w)
	}

	// Concurrently revoke certificate grant
	revokeDone := make(chan struct{})
	var revokeErr error
	go func() {
		<-startGate
		// Brief sleep so some requests hit before revocation
		time.Sleep(15 * time.Millisecond)
		revokeErr = fixture.Store.RevokeCertificateGrant(ctx, fixture.Project.ID, fixture.Owner.Token, fixture.ClientCertFingerprint)
		close(revokeDone)
	}()

	// Fire all goroutines
	close(startGate)

	// Wait for revocation to finish
	<-revokeDone
	if revokeErr != nil {
		t.Fatalf("RevokeCertificateGrant failed: %v", revokeErr)
	}

	// Wait for workers to finish
	wg.Wait()

	if len(unexpectedErrors) > 0 {
		t.Fatalf("encountered %d unexpected errors during concurrent cert revocation: %v", len(unexpectedErrors), unexpectedErrors[:min(len(unexpectedErrors), 5)])
	}
	if atomic.LoadInt64(&countOther) > 0 {
		t.Fatalf("expected only 200 and 403 statuses, got %d other status codes", countOther)
	}
	if atomic.LoadInt64(&count403) == 0 {
		t.Fatalf("expected at least some 403 responses after revocation, got 0 (count200=%d)", count200)
	}

	// POST-CONDITION 1: All subsequent calls MUST return HTTP 403 Forbidden
	for i := 0; i < 50; i++ {
		st, body := makeRawTLSCall(t, fixture.URL, fixture.CAFile, fixture.ClientCertFile, fixture.ClientKeyFile, fixture.Worker.Token, fixture.Project.ID, "plan.read", nil)
		if st != http.StatusForbidden {
			t.Fatalf("post-revocation request %d returned %d, want 403 (body: %s)", i, st, body)
		}
	}

	// POST-CONDITION 2: Local credential remains valid in SQLite
	valid, err := fixture.Store.CredentialValid(ctx, fixture.Project.ID, fixture.Worker.Token)
	if err != nil || !valid {
		t.Fatalf("worker principal token should remain valid in store after certificate revocation: %v (%v)", valid, err)
	}

	// POST-CONDITION 3: Other granted certificate (OtherCert) is unaffected
	if _, err := fixture.Store.AddCertificateGrant(ctx, fixture.Project.ID, fixture.Owner.Token, fixture.OtherCertFingerprint); err != nil {
		t.Fatalf("failed to grant OtherCert: %v", err)
	}
	stOther, bodyOther := makeRawTLSCall(t, fixture.URL, fixture.CAFile, fixture.OtherCertFile, fixture.OtherKeyFile, fixture.Worker.Token, fixture.Project.ID, "plan.read", nil)
	if stOther != http.StatusOK {
		t.Fatalf("expected OtherCert to succeed with HTTP 200 after ClientCert was revoked, got %d: %s", stOther, bodyOther)
	}
}

// ============================================================================
// Adversarial Stress Test 2A: Concurrent Lease Renewal and Candidate Saving
// ============================================================================
// While Worker A attempts lease renewals, candidate saves, and messages concurrently
// across multiple goroutines, the owner revokes Worker A via RevokePrincipal.
// Asserts:
// - Operations during/after revocation cleanly transition to ErrUnauthorized.
// - Zero deadlocks, zero SQLite lock failures, zero panics.
// - Running task is immediately reset to status='ready', owner=NULL, lease_until=0.
// - Worker A is permanently locked out (CredentialValid = false).
// - Worker B can cleanly claim the released task with attempt 2 and complete it.
func TestM4Challenger_ConcurrentPrincipalTokenInvalidation_RunningTask(t *testing.T) {
	fixture := setupMultiWorktreeTLS(t)
	ctx := context.Background()

	s := fixture.Store
	p := fixture.Project
	owner := fixture.Owner
	workerA := fixture.Worker

	// Create Worker B
	workerB, err := s.AddPrincipal(ctx, p.ID, owner.Token, "Worker B", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AcknowledgePlan(ctx, p.ID, workerB.Token, 1); err != nil {
		t.Fatal(err)
	}

	// Create and claim a task by Worker A
	contract := coord.ReportContract{ReviewType: "full_code", Formats: []string{"markdown"}}
	task, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Concurrency Task", "Desc", "implementation", contract, nil)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimTask(ctx, p.ID, workerA.Token, task.ID, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Attempt != 1 || claimed.Owner != workerA.PrincipalID || claimed.Status != "running" {
		t.Fatalf("unexpected claim: %+v", claimed)
	}

	const numGoroutines = 20
	const opsPerGoroutine = 25
	var wg sync.WaitGroup

	var preRevokeSuccess int64
	var postRevokeUnauthorized int64
	var unexpectedErrors []string
	var mu sync.Mutex

	addErr := func(msg string) {
		mu.Lock()
		unexpectedErrors = append(unexpectedErrors, msg)
		mu.Unlock()
	}

	startGate := make(chan struct{})

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			<-startGate
			for i := 0; i < opsPerGoroutine; i++ {
				var opErr error
				switch i % 3 {
				case 0:
					_, opErr = s.RenewLease(ctx, p.ID, workerA.Token, task.ID, 1, 10*time.Minute)
				case 1:
					_, opErr = s.SaveCandidate(ctx, p.ID, workerA.Token, task.ID, 1, "head", "diff --git a/f b/f\n")
				case 2:
					_, opErr = s.SendMessage(ctx, p.ID, workerA.Token, workerB.PrincipalID, "t", "s", "b")
				}

				if opErr == nil {
					atomic.AddInt64(&preRevokeSuccess, 1)
				} else if errors.Is(opErr, coord.ErrUnauthorized) {
					atomic.AddInt64(&postRevokeUnauthorized, 1)
				} else if errors.Is(opErr, coord.ErrConflict) {
					atomic.AddInt64(&postRevokeUnauthorized, 1)
				} else {
					addErr(fmt.Sprintf("goroutine %d op %d: unexpected error: %v", gid, i, opErr))
				}
				time.Sleep(500 * time.Microsecond)
			}
		}(g)
	}

	// Concurrently revoke Worker A
	revokeDone := make(chan struct{})
	var revokeErr error
	go func() {
		<-startGate
		time.Sleep(10 * time.Millisecond)
		revokeErr = s.RevokePrincipal(ctx, p.ID, owner.Token, workerA.PrincipalID)
		close(revokeDone)
	}()

	close(startGate)
	<-revokeDone
	if revokeErr != nil {
		t.Fatalf("RevokePrincipal failed: %v", revokeErr)
	}

	wg.Wait()

	if len(unexpectedErrors) > 0 {
		t.Fatalf("unexpected errors during concurrent principal revocation: %v", unexpectedErrors[:min(len(unexpectedErrors), 5)])
	}
	if atomic.LoadInt64(&postRevokeUnauthorized) == 0 {
		t.Fatalf("expected unauthorized rejections after revocation, got 0")
	}

	// POST-CONDITION 1: CredentialValid must be false for Worker A
	valid, err := s.CredentialValid(ctx, p.ID, workerA.Token)
	if err != nil || valid {
		t.Fatalf("worker A token should be invalid: valid=%v, err=%v", valid, err)
	}

	// POST-CONDITION 2: Active running task must be reset to status='ready', owner=NULL, lease_until=0
	tReset, err := s.GetTask(ctx, p.ID, owner.Token, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tReset.Status != "ready" || tReset.Owner != "" || tReset.LeaseUntil != 0 {
		t.Fatalf("running task was not reset properly: %+v", tReset)
	}

	// POST-CONDITION 3: Worker A can NEVER mutate or query project
	if _, err = s.RenewLease(ctx, p.ID, workerA.Token, task.ID, 1, time.Hour); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("Worker A RenewLease want ErrUnauthorized, got %v", err)
	}
	if _, err = s.ClaimTask(ctx, p.ID, workerA.Token, task.ID, time.Hour); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("Worker A ClaimTask want ErrUnauthorized, got %v", err)
	}

	// POST-CONDITION 4: Worker B can claim the task with attempt counter incremented to 2
	claimB, err := s.ClaimTask(ctx, p.ID, workerB.Token, task.ID, time.Hour)
	if err != nil {
		t.Fatalf("Worker B claim failed: %v", err)
	}
	if claimB.Attempt != 2 || claimB.Owner != workerB.PrincipalID || claimB.Status != "running" {
		t.Fatalf("Worker B claim unexpected: %+v", claimB)
	}

	// Worker B completes the task and owner accepts
	candB, err := s.SaveCandidate(ctx, p.ID, workerB.Token, task.ID, claimB.Attempt, "head", "diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1 +1 @@\n-old\n+new\n")
	if err != nil {
		t.Fatal(err)
	}
	_ = candB
	if err = s.RequestReview(ctx, p.ID, workerB.Token, task.ID, claimB.Attempt); err != nil {
		t.Fatal(err)
	}
	repB, err := s.SubmitReport(ctx, p.ID, workerB.Token, task.ID, claimB.Attempt, "head", "Completed by Worker B")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AcceptReport(ctx, p.ID, owner.Token, repB.ID, repB.Digest); err != nil {
		t.Fatal(err)
	}

	finalTask, err := s.GetTask(ctx, p.ID, owner.Token, task.ID)
	if err != nil || finalTask.Status != "accepted" {
		t.Fatalf("final task status not accepted: %+v (%v)", finalTask, err)
	}
}

// ============================================================================
// Adversarial Stress Test 2B: Revocation Fencing Boundary on Submitted Tasks
// ============================================================================
// Probes the boundary where a task has been submitted before RevokePrincipal.
// Observations:
//   - RevokePrincipal executes: UPDATE tasks SET status='ready',owner=NULL,lease_until=0 WHERE ... AND status='running'
//   - Because the status is 'submitted', the task is NOT reset to ready.
//   - Worker B cannot claim the task (ClaimTask requires status='ready').
//   - The task remains orphaned in 'submitted' owned by the revoked principal
//     unless the project owner requests rework (RequestRework).
func TestM4Challenger_RevocationBoundary_SubmittedTask(t *testing.T) {
	fixture := setupMultiWorktreeTLS(t)
	ctx := context.Background()

	s := fixture.Store
	p := fixture.Project
	owner := fixture.Owner
	workerA := fixture.Worker

	workerB, err := s.AddPrincipal(ctx, p.ID, owner.Token, "Worker B2", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AcknowledgePlan(ctx, p.ID, workerB.Token, 1); err != nil {
		t.Fatal(err)
	}

	// Create and claim task
	task, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Submitted Revocation Task", "Desc", "implementation", coord.ReportContract{ReviewType: "full_code", Formats: []string{"markdown"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	claimA, err := s.ClaimTask(ctx, p.ID, workerA.Token, task.ID, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	// Worker A saves candidate, requests review, and submits report
	validDiff := "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n"
	if _, err = s.SaveCandidate(ctx, p.ID, workerA.Token, task.ID, claimA.Attempt, "head", validDiff); err != nil {
		t.Fatal(err)
	}
	if err = s.RequestReview(ctx, p.ID, workerA.Token, task.ID, claimA.Attempt); err != nil {
		t.Fatal(err)
	}
	repA, err := s.SubmitReport(ctx, p.ID, workerA.Token, task.ID, claimA.Attempt, "head", "Report by worker A")
	if err != nil {
		t.Fatal(err)
	}

	// Verify task is in status='submitted'
	taskSubmitted, err := s.GetTask(ctx, p.ID, owner.Token, task.ID)
	if err != nil || taskSubmitted.Status != "submitted" {
		t.Fatalf("expected status=submitted, got: %+v (%v)", taskSubmitted, err)
	}

	// Owner revokes Worker A
	if err = s.RevokePrincipal(ctx, p.ID, owner.Token, workerA.PrincipalID); err != nil {
		t.Fatalf("RevokePrincipal failed: %v", err)
	}

	// Check task state after RevokePrincipal:
	// Empirical finding: The task is NOT reset to ready because RevokePrincipal only checks status='running'.
	taskAfterRevoke, err := s.GetTask(ctx, p.ID, owner.Token, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Observed task state after principal revocation of submitted report: status=%s, owner=%s", taskAfterRevoke.Status, taskAfterRevoke.Owner)

	// Worker B attempts to claim: fails with ErrConflict because status is 'submitted'
	_, claimBErr := s.ClaimTask(ctx, p.ID, workerB.Token, task.ID, 10*time.Minute)
	if !errors.Is(claimBErr, coord.ErrConflict) {
		t.Fatalf("expected ErrConflict when claiming submitted task of revoked worker, got: %v", claimBErr)
	}

	// Owner can resolve the orphaned task by requesting rework:
	if err = s.RequestRework(ctx, p.ID, owner.Token, repA.ID, repA.Digest, "Worker revoked, resetting task"); err != nil {
		t.Fatalf("RequestRework failed: %v", err)
	}

	// Now task is status='ready' and Worker B can claim it cleanly:
	claimBAfterRework, err := s.ClaimTask(ctx, p.ID, workerB.Token, task.ID, 10*time.Minute)
	if err != nil {
		t.Fatalf("Worker B could not claim task after rework: %v", err)
	}
	if claimBAfterRework.Attempt != 2 || claimBAfterRework.Owner != workerB.PrincipalID {
		t.Fatalf("Worker B claim after rework unexpected: %+v", claimBAfterRework)
	}
}

// ============================================================================
// Adversarial Stress Test 3: Kill Switch Rapid Oscillation Under Request Flood
// ============================================================================
// Repeatedly flips kill switch on and off (rapid oscillation) while concurrent
// TLS RPC requests flood the endpoint. Also verifies that local loopback HTTP
// and Unix socket daemon continue serving without disruption.
// Asserts:
// - Remote responses are STRICTLY HTTP 200 (when on) or HTTP 503 (when off).
// - Zero HTTP 500, zero deadlocks, zero panics.
// - Local loopback and Unix socket calls remain 100% HTTP 200 / success throughout.
// - Remote callers can never mutate the switch (HTTP 503 when off, HTTP 401 when on).
// - Setting switch to off halts 100% of remote traffic; setting to on resumes 100%.
func TestM4Challenger_KillSwitchRapidOscillation(t *testing.T) {
	fixture := setupMultiWorktreeTLS(t)
	ctx := context.Background()

	// Local loopback web handler
	webH := coord.NewWebHandler(fixture.Store)

	// Local Unix domain socket daemon
	sockDir := filepath.Join(t.TempDir(), "priv_sock")
	if err := os.Mkdir(sockDir, 0700); err != nil {
		t.Fatal(err)
	}
	sockPath := filepath.Join(sockDir, "coord.sock")
	daemonCtx, daemonCancel := context.WithCancel(context.Background())
	t.Cleanup(daemonCancel)
	go func() {
		_ = coord.ServeLocal(daemonCtx, fixture.Store, sockPath)
	}()

	var localRemote *coord.Remote
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(sockPath); err == nil {
			var errConnect error
			localRemote, errConnect = coord.NewLocalRemote(sockPath)
			if errConnect == nil {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if localRemote == nil {
		t.Fatal("failed to connect to local Unix socket daemon")
	}

	const numRemoteWorkers = 16
	const remoteReqsPerWorker = 40
	const numFlips = 30

	stopWorkers := make(chan struct{})
	var wg sync.WaitGroup

	var count200 int64
	var count503 int64
	var countOther int64
	var unexpectedErrors []string
	var errMu sync.Mutex

	recordErr := func(msg string) {
		errMu.Lock()
		unexpectedErrors = append(unexpectedErrors, msg)
		errMu.Unlock()
	}

	startGate := make(chan struct{})

	// Remote TLS RPC workers
	for w := 0; w < numRemoteWorkers; w++ {
		wg.Add(1)
		go func(wid int) {
			defer wg.Done()
			<-startGate
			for i := 0; i < remoteReqsPerWorker; i++ {
				select {
				case <-stopWorkers:
					return
				default:
				}

				st, body := makeRawTLSCall(t, fixture.URL, fixture.CAFile, fixture.ClientCertFile, fixture.ClientKeyFile, fixture.Worker.Token, fixture.Project.ID, "plan.read", nil)
				switch st {
				case http.StatusOK:
					atomic.AddInt64(&count200, 1)
				case http.StatusServiceUnavailable:
					atomic.AddInt64(&count503, 1)
					if !strings.Contains(body, "remote coordination is disabled by the local kill switch") {
						recordErr(fmt.Sprintf("worker %d req %d: 503 body unexpected: %s", wid, i, body))
					}
				default:
					atomic.AddInt64(&countOther, 1)
					recordErr(fmt.Sprintf("worker %d req %d: unexpected status %d: %s", wid, i, st, body))
				}
				time.Sleep(1 * time.Millisecond)
			}
		}(w)
	}

	// Local Unix socket worker
	var countLocalSocket200 int64
	var countLocalSocketErr int64
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-startGate
		for {
			select {
			case <-stopWorkers:
				return
			default:
			}
			p, err := localRemote.CurrentPlan(ctx, fixture.Project.ID, fixture.Worker.Token)
			if err != nil || p.Revision != 1 {
				atomic.AddInt64(&countLocalSocketErr, 1)
				recordErr(fmt.Sprintf("local socket error during oscillation: %v", err))
			} else {
				atomic.AddInt64(&countLocalSocket200, 1)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()

	// Local WebHandler worker
	var countLocalWeb200 int64
	var countLocalWebErr int64
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-startGate
		for {
			select {
			case <-stopWorkers:
				return
			default:
			}
			reqWeb := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8081/api/projects", nil)
			reqWeb.RemoteAddr = "127.0.0.1:12345"
			recWeb := httptest.NewRecorder()
			webH.ServeHTTP(recWeb, reqWeb)
			if recWeb.Code != http.StatusOK {
				atomic.AddInt64(&countLocalWebErr, 1)
				recordErr(fmt.Sprintf("local web error during oscillation: code %d", recWeb.Code))
			} else {
				atomic.AddInt64(&countLocalWeb200, 1)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()

	// Switcher goroutine rapidly toggling kill switch
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-startGate
		for i := 0; i < numFlips; i++ {
			state := (i%2 == 0)
			if err := fixture.Store.SetRemoteAccessEnabled(ctx, state); err != nil {
				recordErr(fmt.Sprintf("flip %d (state=%v) failed: %v", i, state, err))
			}
			time.Sleep(3 * time.Millisecond)
		}
	}()

	// Trigger start
	close(startGate)

	// Wait for flip routine and workers
	time.Sleep(200 * time.Millisecond)
	close(stopWorkers)
	wg.Wait()

	if len(unexpectedErrors) > 0 {
		t.Fatalf("unexpected errors during kill switch oscillation: %v", unexpectedErrors[:min(len(unexpectedErrors), 5)])
	}
	if atomic.LoadInt64(&countOther) > 0 {
		t.Fatalf("remote calls produced %d non-200/503 statuses", countOther)
	}
	if atomic.LoadInt64(&count200) == 0 || atomic.LoadInt64(&count503) == 0 {
		t.Fatalf("expected both 200 and 503 responses during oscillation: 200=%d, 503=%d", count200, count503)
	}

	// Local calls must have zero errors
	if countLocalSocketErr > 0 || countLocalWebErr > 0 {
		t.Fatalf("local calls failed during oscillation: socketErr=%d, webErr=%d", countLocalSocketErr, countLocalWebErr)
	}
	if countLocalSocket200 == 0 || countLocalWeb200 == 0 {
		t.Fatalf("expected successful local calls during oscillation: socket200=%d, web200=%d", countLocalSocket200, countLocalWeb200)
	}

	// POST-CONDITION 1: Explicitly disable remote access -> 100% of remote calls return 503
	if err := fixture.Store.SetRemoteAccessEnabled(ctx, false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		st, body := makeRawTLSCall(t, fixture.URL, fixture.CAFile, fixture.ClientCertFile, fixture.ClientKeyFile, fixture.Worker.Token, fixture.Project.ID, "plan.read", nil)
		if st != http.StatusServiceUnavailable {
			t.Fatalf("post-disable call %d: want 503, got %d (%s)", i, st, body)
		}
	}

	// POST-CONDITION 2: Remote caller cannot mutate kill switch over TLS RPC
	// While disabled -> 503
	stRemoteSetOff, _ := makeRawTLSCall(t, fixture.URL, fixture.CAFile, fixture.ClientCertFile, fixture.ClientKeyFile, fixture.Owner.Token, fixture.Project.ID, "remote.access.set", map[string]any{"enabled": true})
	if stRemoteSetOff != http.StatusServiceUnavailable {
		t.Fatalf("remote set while disabled want 503, got %d", stRemoteSetOff)
	}

	// POST-CONDITION 3: Explicitly enable remote access -> 100% of remote calls return 200
	if err := fixture.Store.SetRemoteAccessEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		st, body := makeRawTLSCall(t, fixture.URL, fixture.CAFile, fixture.ClientCertFile, fixture.ClientKeyFile, fixture.Worker.Token, fixture.Project.ID, "plan.read", nil)
		if st != http.StatusOK {
			t.Fatalf("post-enable call %d: want 200, got %d (%s)", i, st, body)
		}
	}

	// While enabled -> remote set returns 401 Unauthorized
	stRemoteSetOn, _ := makeRawTLSCall(t, fixture.URL, fixture.CAFile, fixture.ClientCertFile, fixture.ClientKeyFile, fixture.Owner.Token, fixture.Project.ID, "remote.access.set", map[string]any{"enabled": false})
	if stRemoteSetOn != http.StatusUnauthorized {
		t.Fatalf("remote set while enabled want 401 Unauthorized, got %d", stRemoteSetOn)
	}
}

// ============================================================================
// Adversarial Stress Test 4: Cross-Project Token and Cert Forging Matrix
// ============================================================================
// Thoroughly stress-tests the multi-tenant isolation boundaries across:
// - Valid cert P1 + Valid token P1 -> 200 OK
// - Valid cert P1 + Valid token P2 against P1 -> 401 Unauthorized (token not in P1)
// - Valid cert P1 + Valid token P2 against P2 -> 403 Forbidden (cert not granted to P2)
// - Valid cert P2 + Valid token P1 against P1 -> 403 Forbidden (cert not granted to P1)
// - Valid cert P2 + Valid token P1 against P2 -> 401 Unauthorized (token not in P2)
// - Revoked cert P1 + Valid token P1 against P1 -> 403 Forbidden (cert revoked)
// - Valid cert P1 + Revoked token P1 against P1 -> 401 Unauthorized (token revoked)
// - Revoked cert P1 + Revoked token P1 against P1 -> 403 Forbidden (cert check precedes token)
// - Valid cert P1 + Random garbage token -> 401 Unauthorized
// - Valid cert P1 + Missing Bearer -> 401 Unauthorized
// - Valid cert P1 + Cross-project mutations on P2 (task.create, message.send) -> 403 Forbidden
func TestM4Challenger_CrossProjectForgingSemantics(t *testing.T) {
	fixture := setupMultiWorktreeTLS(t)
	ctx := context.Background()

	s := fixture.Store
	p1 := fixture.Project
	owner1 := fixture.Owner
	worker1 := fixture.Worker

	// Create Project 2
	p2, owner2, err := s.CreateProject(ctx, "project-isolation-2", "owner-2")
	if err != nil {
		t.Fatal(err)
	}
	worker2, err := s.AddPrincipal(ctx, p2.ID, owner2.Token, "worker-2", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PublishPlan(ctx, p2.ID, owner2.Token, "Plan 2", 0); err != nil {
		t.Fatal(err)
	}

	// Grant OtherCert to Project 2:
	if _, err = s.AddCertificateGrant(ctx, p2.ID, owner2.Token, fixture.OtherCertFingerprint); err != nil {
		t.Fatal(err)
	}

	// Create Worker 1Revoked in P1
	worker1Revoked, err := s.AddPrincipal(ctx, p1.ID, owner1.Token, "worker-1-revoked", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RevokePrincipal(ctx, p1.ID, owner1.Token, worker1Revoked.PrincipalID); err != nil {
		t.Fatal(err)
	}

	// Create test matrix table
	type testCase struct {
		name       string
		certFile   string
		keyFile    string
		token      string
		targetProj string
		op         string
		extra      map[string]any
		wantStatus int
		wantBody   string
	}

	cases := []testCase{
		{
			name:       "Case_A_ValidCert1_ValidToken1_TargetP1_Golden",
			certFile:   fixture.ClientCertFile,
			keyFile:    fixture.ClientKeyFile,
			token:      worker1.Token,
			targetProj: p1.ID,
			op:         "plan.read",
			wantStatus: http.StatusOK,
		},
		{
			name:       "Case_B_ValidCert1_ValidToken2_TargetP1_TokenIsolation",
			certFile:   fixture.ClientCertFile,
			keyFile:    fixture.ClientKeyFile,
			token:      worker2.Token, // Token belongs to P2, not P1
			targetProj: p1.ID,
			op:         "plan.read",
			wantStatus: http.StatusUnauthorized, // 401
			wantBody:   "unauthorized",
		},
		{
			name:       "Case_C_ValidCert1_ValidToken2_TargetP2_CertIsolation",
			certFile:   fixture.ClientCertFile,
			keyFile:    fixture.ClientKeyFile,
			token:      worker2.Token, // Valid token for P2, but Cert1 is NOT granted to P2
			targetProj: p2.ID,
			op:         "plan.read",
			wantStatus: http.StatusForbidden, // 403
			wantBody:   "client certificate is not authorized",
		},
		{
			name:       "Case_D_ValidCert2_ValidToken1_TargetP1_CertIsolation",
			certFile:   fixture.OtherCertFile,
			keyFile:    fixture.OtherKeyFile,
			token:      worker1.Token, // Valid token for P1, but Cert2 is NOT granted to P1
			targetProj: p1.ID,
			op:         "plan.read",
			wantStatus: http.StatusForbidden, // 403
			wantBody:   "client certificate is not authorized",
		},
		{
			name:       "Case_E_ValidCert2_ValidToken1_TargetP2_TokenIsolation",
			certFile:   fixture.OtherCertFile,
			keyFile:    fixture.OtherKeyFile,
			token:      worker1.Token, // Token belongs to P1, not P2
			targetProj: p2.ID,
			op:         "plan.read",
			wantStatus: http.StatusUnauthorized, // 401
			wantBody:   "unauthorized",
		},
		{
			name:       "Case_F_ValidCert1_RevokedToken_TargetP1_RevokedPrincipal",
			certFile:   fixture.ClientCertFile,
			keyFile:    fixture.ClientKeyFile,
			token:      worker1Revoked.Token, // Principal revoked
			targetProj: p1.ID,
			op:         "plan.read",
			wantStatus: http.StatusUnauthorized, // 401
			wantBody:   "unauthorized",
		},
		{
			name:       "Case_G_ValidCert1_GarbageToken_TargetP1",
			certFile:   fixture.ClientCertFile,
			keyFile:    fixture.ClientKeyFile,
			token:      "completely-fake-garbage-token-1234567890",
			targetProj: p1.ID,
			op:         "plan.read",
			wantStatus: http.StatusUnauthorized, // 401
			wantBody:   "unauthorized",
		},
		{
			name:       "Case_H_ValidCert1_EmptyToken_TargetP1",
			certFile:   fixture.ClientCertFile,
			keyFile:    fixture.ClientKeyFile,
			token:      "",
			targetProj: p1.ID,
			op:         "plan.read",
			wantStatus: http.StatusUnauthorized, // 401
		},
		{
			name:       "Case_I_ValidCert1_ForgedMutation_TaskCreate_TargetP2",
			certFile:   fixture.ClientCertFile,
			keyFile:    fixture.ClientKeyFile,
			token:      worker2.Token,
			targetProj: p2.ID,
			op:         "task.create",
			extra: map[string]any{
				"request_id":  "forged-task",
				"revision":    1,
				"title":       "Injected Task",
				"description": "Should be blocked by 403",
				"kind":        "implementation",
				"contract":    coord.ReportContract{ReviewType: "full_code", Formats: []string{"markdown"}},
			},
			wantStatus: http.StatusForbidden, // 403
			wantBody:   "client certificate is not authorized",
		},
		{
			name:       "Case_J_ValidCert2_ForgedMutation_TaskCreate_WrongToken_TargetP2",
			certFile:   fixture.OtherCertFile,
			keyFile:    fixture.OtherKeyFile,
			token:      worker1.Token, // Wrong token
			targetProj: p2.ID,
			op:         "task.create",
			extra: map[string]any{
				"request_id":  "forged-task-2",
				"revision":    1,
				"title":       "Injected Task 2",
				"description": "Should be blocked by 401",
				"kind":        "implementation",
				"contract":    coord.ReportContract{ReviewType: "full_code", Formats: []string{"markdown"}},
			},
			wantStatus: http.StatusUnauthorized, // 401
			wantBody:   "unauthorized",
		},
		{
			name:       "Case_K_ValidCert1_ForgedMutation_MessageSend_TargetP2",
			certFile:   fixture.ClientCertFile,
			keyFile:    fixture.ClientKeyFile,
			token:      worker2.Token,
			targetProj: p2.ID,
			op:         "message.send",
			extra: map[string]any{
				"recipient":  worker2.PrincipalID,
				"thread":     "forged-thread",
				"subject":    "Forged Message",
				"body":       "Forged Body",
				"request_id": "forged-msg",
			},
			wantStatus: http.StatusForbidden, // 403
			wantBody:   "client certificate is not authorized",
		},
		{
			name:       "Case_L_ValidCert2_ForgedMutation_MessageSend_WrongToken_TargetP2",
			certFile:   fixture.OtherCertFile,
			keyFile:    fixture.OtherKeyFile,
			token:      worker1.Token,
			targetProj: p2.ID,
			op:         "message.send",
			extra: map[string]any{
				"recipient":  worker2.PrincipalID,
				"thread":     "forged-thread",
				"subject":    "Forged Message",
				"body":       "Forged Body",
				"request_id": "forged-msg-2",
			},
			wantStatus: http.StatusUnauthorized, // 401
			wantBody:   "unauthorized",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, body := makeRawTLSCall(t, fixture.URL, fixture.CAFile, tc.certFile, tc.keyFile, tc.token, tc.targetProj, tc.op, tc.extra)
			if st != tc.wantStatus {
				t.Fatalf("%s: expected HTTP %d, got %d (body: %s)", tc.name, tc.wantStatus, st, body)
			}
			if tc.wantBody != "" && !strings.Contains(strings.ToLower(body), strings.ToLower(tc.wantBody)) {
				t.Fatalf("%s: expected body containing %q, got %s", tc.name, tc.wantBody, body)
			}
		})
	}

	// Now test Revoked Cert on P1:
	// Revoke Cert 1 on P1
	if err := s.RevokeCertificateGrant(ctx, p1.ID, owner1.Token, fixture.ClientCertFingerprint); err != nil {
		t.Fatalf("failed to revoke Cert 1: %v", err)
	}

	// Case M: Revoked Cert 1 + Valid Token 1 on P1 -> HTTP 403 Forbidden
	t.Run("Case_M_RevokedCert1_ValidToken1_TargetP1", func(t *testing.T) {
		st, body := makeRawTLSCall(t, fixture.URL, fixture.CAFile, fixture.ClientCertFile, fixture.ClientKeyFile, worker1.Token, p1.ID, "plan.read", nil)
		if st != http.StatusForbidden {
			t.Fatalf("expected HTTP 403 Forbidden for revoked cert, got %d: %s", st, body)
		}
		if !strings.Contains(body, "client certificate is not authorized") {
			t.Fatalf("expected unauthorized cert message, got: %s", body)
		}
	})

	// Case N: Revoked Cert 1 + Revoked Token 1 on P1 -> HTTP 403 Forbidden (Cert gate evaluates first)
	t.Run("Case_N_RevokedCert1_RevokedToken1_TargetP1", func(t *testing.T) {
		st, body := makeRawTLSCall(t, fixture.URL, fixture.CAFile, fixture.ClientCertFile, fixture.ClientKeyFile, worker1Revoked.Token, p1.ID, "plan.read", nil)
		if st != http.StatusForbidden {
			t.Fatalf("expected HTTP 403 Forbidden (cert gate first), got %d: %s", st, body)
		}
	})

	// Verify that Project 2 remains 100% clean and unpolluted
	tasksP2, err := s.ListTasks(ctx, p2.ID, owner2.Token, 10)
	if err != nil || len(tasksP2) != 0 {
		t.Fatalf("Project 2 tasks were polluted: %+v", tasksP2)
	}
	msgsP2, err := s.ListMessages(ctx, p2.ID, owner2.Token, 0, 10)
	if err != nil || len(msgsP2) != 0 {
		t.Fatalf("Project 2 messages were polluted: %+v", msgsP2)
	}
}
