package coord_test

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tasmirz/gossip/pkg/coord"
	_ "modernc.org/sqlite"
)

func setupM2Test(t *testing.T) (*coord.Store, string, coord.Project, coord.Credential, coord.Credential, coord.Credential) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "coord_m2.db")
	s, err := coord.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	p, owner, err := s.CreateProject(ctx, "m2_project", "Owner Alice")
	if err != nil {
		t.Fatal(err)
	}
	worker, err := s.AddPrincipal(ctx, p.ID, owner.Token, "Worker Bob", "worker")
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := s.AddPrincipal(ctx, p.ID, owner.Token, "Reviewer Carol", "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PublishPlan(ctx, p.ID, owner.Token, "Initial M2 Plan", 0); err != nil {
		t.Fatal(err)
	}
	if err = s.AcknowledgePlan(ctx, p.ID, worker.Token, 1); err != nil {
		t.Fatal(err)
	}
	if err = s.AcknowledgePlan(ctx, p.ID, reviewer.Token, 1); err != nil {
		t.Fatal(err)
	}
	return s, path, p, owner, worker, reviewer
}

// TestAdversarial_PresenceAndHostCapabilitiesTruthfulness verifies that
// host capabilities are strictly validated as descriptive "host_reported"
// metadata and that agent presence reflects adapter transport connectivity.
func TestAdversarial_PresenceAndHostCapabilitiesTruthfulness(t *testing.T) {
	s, _, p, owner, worker, _ := setupM2Test(t)
	ctx := context.Background()

	instanceID := "agent-instance-adversarial-1"

	// 1. Adapter vs Host_Reported Source Integrity
	// A. Valid adapter capability
	err := s.HeartbeatAgentWithMetadata(ctx, p.ID, worker.Token, instanceID, []coord.AgentCapability{
		{Name: "mcp_tools", Source: "adapter"},
	}, coord.MCPClientInfo{Name: "GossipTestAdapter", Version: "1.0.0"})
	if err != nil {
		t.Fatalf("Valid adapter capability rejected: %v", err)
	}

	// B. Invalid source: reporting adapter capability with source "host_reported"
	err = s.HeartbeatAgentWithMetadata(ctx, p.ID, worker.Token, instanceID, []coord.AgentCapability{
		{Name: "mcp_tools", Source: "host_reported"},
	}, coord.MCPClientInfo{})
	if err == nil {
		t.Fatalf("Expected error for mcp_tools with source host_reported, got nil")
	}

	// C. Invalid source: reporting host capability with source "adapter"
	err = s.HeartbeatAgentWithMetadata(ctx, p.ID, worker.Token, instanceID, []coord.AgentCapability{
		{Name: "wake", Source: "adapter"},
	}, coord.MCPClientInfo{})
	if err == nil {
		t.Fatalf("Expected error for wake with source adapter, got nil")
	}

	// D. Reporting unverified / spoofed capability claims
	err = s.HeartbeatAgentWithMetadata(ctx, p.ID, worker.Token, instanceID, []coord.AgentCapability{
		{Name: "root_execution", Source: "adapter"},
	}, coord.MCPClientInfo{})
	if err == nil {
		t.Fatalf("Expected error for unknown capability root_execution, got nil")
	}

	err = s.HeartbeatAgentWithMetadata(ctx, p.ID, worker.Token, instanceID, []coord.AgentCapability{
		{Name: "verified_container_sandbox", Source: "host_reported"},
	}, coord.MCPClientInfo{})
	if err == nil {
		t.Fatalf("Expected error for unknown host capability verified_container_sandbox, got nil")
	}

	// E. Duplicate capabilities in the same heartbeat
	err = s.HeartbeatAgentWithMetadata(ctx, p.ID, worker.Token, instanceID, []coord.AgentCapability{
		{Name: "mcp_tools", Source: "adapter"},
		{Name: "mcp_tools", Source: "adapter"},
	}, coord.MCPClientInfo{})
	if err == nil {
		t.Fatalf("Expected error for duplicate capabilities, got nil")
	}

	// F. Exceeding 20 capabilities
	tooManyCaps := make([]coord.AgentCapability, 21)
	for i := 0; i < 21; i++ {
		tooManyCaps[i] = coord.AgentCapability{Name: "mcp_tools", Source: "adapter"}
	}
	err = s.HeartbeatAgentWithMetadata(ctx, p.ID, worker.Token, instanceID, tooManyCaps, coord.MCPClientInfo{})
	if err == nil || !strings.Contains(err.Error(), "at most 20") {
		t.Fatalf("Expected 'at most 20' error, got %v", err)
	}

	// 2. HostReportedCapabilities helper validation
	caps, err := coord.HostReportedCapabilities([]string{"wake", "launch", "isolated_workspace"})
	if err != nil {
		t.Fatalf("HostReportedCapabilities failed: %v", err)
	}
	if len(caps) != 3 {
		t.Fatalf("Expected 3 capabilities, got %d", len(caps))
	}
	for _, c := range caps {
		if c.Source != "host_reported" {
			t.Fatalf("Expected source 'host_reported', got %q", c.Source)
		}
	}
	// Invalid host capability name
	_, err = coord.HostReportedCapabilities([]string{"unverified_claim"})
	if err == nil {
		t.Fatalf("Expected error for unverified_claim in HostReportedCapabilities")
	}

	// 3. Presence reflects transport connectivity (heartbeats within TTL)
	validCaps := []coord.AgentCapability{
		{Name: "mcp_tools", Source: "adapter"},
		{Name: "wake", Source: "host_reported"},
	}
	err = s.HeartbeatAgentWithMetadata(ctx, p.ID, worker.Token, instanceID, validCaps, coord.MCPClientInfo{
		Name:    "EmpiricalTestClient",
		Version: "2.5.0",
	})
	if err != nil {
		t.Fatalf("HeartbeatAgentWithMetadata failed: %v", err)
	}

	presenceList, err := s.ListAgentPresence(ctx, p.ID, worker.Token)
	if err != nil {
		t.Fatalf("ListAgentPresence failed: %v", err)
	}
	var workerPresence *coord.AgentPresence
	for i := range presenceList {
		if presenceList[i].PrincipalID == worker.PrincipalID {
			workerPresence = &presenceList[i]
			break
		}
	}
	if workerPresence == nil {
		t.Fatalf("Worker not found in presence list")
	}
	if !workerPresence.Online {
		t.Fatalf("Expected worker to be Online after recent heartbeat")
	}
	if workerPresence.MCPClientName != "EmpiricalTestClient" || workerPresence.MCPClientVersion != "2.5.0" {
		t.Fatalf("Client metadata mismatch: %s %s", workerPresence.MCPClientName, workerPresence.MCPClientVersion)
	}

	// End session explicitly -> immediately Online: false
	err = s.EndAgentSession(ctx, p.ID, worker.Token, instanceID)
	if err != nil {
		t.Fatalf("EndAgentSession failed: %v", err)
	}
	presenceList, err = s.ListAgentPresence(ctx, p.ID, worker.Token)
	if err != nil {
		t.Fatalf("ListAgentPresence failed: %v", err)
	}
	for _, pr := range presenceList {
		if pr.PrincipalID == worker.PrincipalID && pr.Online {
			t.Fatalf("Expected worker to be Offline after EndAgentSession, got Online: true")
		}
	}

	// 4. Required capabilities gating on task claims
	// Re-establish session with only "mcp_tools" (missing "wake")
	sessionID2 := "agent-instance-adversarial-2"
	err = s.HeartbeatAgentWithMetadata(ctx, p.ID, worker.Token, sessionID2, []coord.AgentCapability{
		{Name: "mcp_tools", Source: "adapter"},
	}, coord.MCPClientInfo{})
	if err != nil {
		t.Fatalf("Heartbeat failed: %v", err)
	}

	task, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Task With Wake Req", "Desc", "implementation", coord.ReportContract{
		ReviewType:           "full_code",
		RequiredCapabilities: []string{"wake"},
	}, nil)
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	// Worker attempting to claim without reported "wake" capability MUST fail with ErrConflict
	_, err = s.ClaimTask(ctx, p.ID, worker.Token, task.ID, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "task requires a current authenticated session reporting capabilities wake") {
		t.Fatalf("Expected required capabilities failure, got: %v", err)
	}

	// Heartbeat with "wake" (host_reported) + "mcp_tools" (adapter)
	err = s.HeartbeatAgentWithMetadata(ctx, p.ID, worker.Token, sessionID2, []coord.AgentCapability{
		{Name: "mcp_tools", Source: "adapter"},
		{Name: "wake", Source: "host_reported"},
	}, coord.MCPClientInfo{})
	if err != nil {
		t.Fatalf("Heartbeat failed: %v", err)
	}

	// Claim should now succeed
	claimed, err := s.ClaimTask(ctx, p.ID, worker.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatalf("Claim should have succeeded once capability reported, got: %v", err)
	}
	if claimed.Status != "running" {
		t.Fatalf("Expected task status 'running', got %s", claimed.Status)
	}
}

// TestAdversarial_BudgetReservationsInternalPlanningTruthfulness verifies
// that budget envelopes and token holds are strictly internal SQLite planning
// reservations and that reported usage reflects unverified telemetry.
func TestAdversarial_BudgetReservationsInternalPlanningTruthfulness(t *testing.T) {
	s, _, p, owner, worker, _ := setupM2Test(t)
	ctx := context.Background()

	// 1. Initial budget state and truthfulness disclaimer
	b, err := s.GetBudget(ctx, p.ID, owner.Token)
	if err != nil {
		t.Fatalf("GetBudget failed: %v", err)
	}
	if b.Enabled {
		t.Fatalf("Expected budget to be disabled by default")
	}
	const expectedNotice = "agent-reported totals are self-reported; provider billing is not independently observed or enforced"
	if b.Usage != expectedNotice {
		t.Fatalf("Budget.Usage notice mismatch. Expected %q, got %q", expectedNotice, b.Usage)
	}

	// 2. SetBudget with optimistic concurrency
	// Invalid parameters
	_, err = s.SetBudget(ctx, p.ID, owner.Token, -1, 1000)
	if err == nil {
		t.Fatalf("Expected error for negative revision")
	}
	_, err = s.SetBudget(ctx, p.ID, owner.Token, 0, 0)
	if err == nil {
		t.Fatalf("Expected error for limit < 1")
	}

	// Valid enable at revision 0
	b, err = s.SetBudget(ctx, p.ID, owner.Token, 0, 50000)
	if err != nil {
		t.Fatalf("SetBudget failed: %v", err)
	}
	if !b.Enabled || b.Revision != 1 || b.TokenLimit != 50000 {
		t.Fatalf("Unexpected budget state: %+v", b)
	}

	// Stale revision must fail with ErrConflict
	_, err = s.SetBudget(ctx, p.ID, owner.Token, 0, 60000)
	if !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("Expected ErrConflict for stale revision, got: %v", err)
	}

	// 3. Task budget reservation enforcement
	task, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Budgeted Task", "Desc", "research", coord.ReportContract{
		ReviewType: "full_code",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Attempt to claim without budget reservation -> fails
	_, err = s.ClaimTask(ctx, p.ID, worker.Token, task.ID, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "task requires a budget reservation before claim") {
		t.Fatalf("Expected 'task requires a budget reservation' error, got %v", err)
	}

	// Over-reservation beyond token limit (limit is 50000, reserving 60000)
	_, err = s.ReserveBudget(ctx, p.ID, owner.Token, task.ID, 60000)
	if !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("Expected ErrConflict when reserving beyond token limit, got: %v", err)
	}

	// Valid reservation
	b, err = s.ReserveBudget(ctx, p.ID, owner.Token, task.ID, 20000)
	if err != nil {
		t.Fatalf("ReserveBudget failed: %v", err)
	}
	if b.Reserved != 20000 || len(b.Reservations) != 1 || b.Reservations[0].Amount != 20000 {
		t.Fatalf("Unexpected reservation state: %+v", b)
	}

	// Claim task -> succeeds
	claimed, err := s.ClaimTask(ctx, p.ID, worker.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatalf("Claim failed after reservation: %v", err)
	}
	if claimed.Status != "running" {
		t.Fatalf("Expected status 'running', got %s", claimed.Status)
	}

	// Changing reservation on running task MUST fail
	_, err = s.ReserveBudget(ctx, p.ID, owner.Token, task.ID, 30000)
	if err == nil || !strings.Contains(err.Error(), "cannot change reservation while task is running") {
		t.Fatalf("Expected 'cannot change reservation while task is running' error, got %v", err)
	}

	// 4. Usage reporting telemetry (unverified self-reported tracking)
	// Invalid source
	_, err = s.RecordAttemptUsage(ctx, p.ID, worker.Token, task.ID, claimed.Attempt, "req-usage-1", coord.UsageReport{
		Provider: "openai",
		Model:    "gpt-4",
		Source:   "verified_provider_billing", // invalid source!
	})
	if err == nil || !strings.Contains(err.Error(), "source must be provider_reported, host_reported, or estimated") {
		t.Fatalf("Expected source validation error, got %v", err)
	}

	// Negative tokens
	_, err = s.RecordAttemptUsage(ctx, p.ID, worker.Token, task.ID, claimed.Attempt, "req-usage-2", coord.UsageReport{
		Provider:    "openai",
		Model:       "gpt-4",
		Source:      "provider_reported",
		InputTokens: -10,
	})
	if err == nil || !strings.Contains(err.Error(), "token counts are outside supported limits") {
		t.Fatalf("Expected token counts error, got %v", err)
	}

	// Token count overflow
	_, err = s.RecordAttemptUsage(ctx, p.ID, worker.Token, task.ID, claimed.Attempt, "req-usage-3", coord.UsageReport{
		Provider:     "openai",
		Model:        "gpt-4",
		Source:       "provider_reported",
		InputTokens:  math.MaxInt64,
		OutputTokens: 10,
	})
	if err == nil || !strings.Contains(err.Error(), "token counts are outside supported limits") {
		t.Fatalf("Expected token overflow error, got %v", err)
	}

	// Valid usage report
	_, err = s.RecordAttemptUsage(ctx, p.ID, worker.Token, task.ID, claimed.Attempt, "req-usage-valid-1", coord.UsageReport{
		Provider:     "anthropic",
		Model:        "claude-3-5-sonnet",
		Source:       "provider_reported",
		InputTokens:  1250,
		OutputTokens: 350,
		CostMicros:   45000,
		Currency:     "USD",
	})
	if err != nil {
		t.Fatalf("RecordAttemptUsage failed: %v", err)
	}

	// Read back budget and verify telemetry summary
	b, err = s.GetBudget(ctx, p.ID, owner.Token)
	if err != nil {
		t.Fatalf("GetBudget failed: %v", err)
	}
	if b.ReportedUsage.TotalTokens != 1600 || b.ReportedUsage.InputTokens != 1250 || b.ReportedUsage.OutputTokens != 350 {
		t.Fatalf("Telemetry summary mismatch: %+v", b.ReportedUsage)
	}
	if len(b.ReportedUsage.Costs) != 1 || b.ReportedUsage.Costs[0].CostMicros != 45000 || b.ReportedUsage.Costs[0].Currency != "USD" {
		t.Fatalf("Cost summary mismatch: %+v", b.ReportedUsage.Costs)
	}
}

// TestAdversarial_TaskStateTransitionsSQLiteTruthfulness verifies all 7 task states:
// ready, running, blocked, submitted, accepted, needs_replan, cancelled.
// It verifies that transitions are strictly transactional SQLite mutations,
// with NO OS process supervisors or POSIX signals.
func TestAdversarial_TaskStateTransitionsSQLiteTruthfulness(t *testing.T) {
	s, dbPath, p, owner, worker, reviewer := setupM2Test(t)
	ctx := context.Background()

	// Direct raw SQL inspect function
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer rawDB.Close()

	getRawTask := func(taskID string) (status string, owner string, attempt int, leaseUntil int64) {
		var o sql.NullString
		err := rawDB.QueryRow("SELECT status, owner, attempt, lease_until FROM tasks WHERE id=?", taskID).Scan(&status, &o, &attempt, &leaseUntil)
		if err != nil {
			t.Fatalf("getRawTask failed: %v", err)
		}
		if o.Valid {
			owner = o.String
		}
		return
	}

	// 1. Task 1: Created with no dependencies -> STATE 1: "ready"
	task1, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Task 1", "Root work", "research", coord.ReportContract{
		ReviewType: "full_code",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if task1.Status != "ready" {
		t.Fatalf("Expected Task 1 status 'ready', got %s", task1.Status)
	}
	st, ow, att, lz := getRawTask(task1.ID)
	if st != "ready" || ow != "" || att != 0 || lz != 0 {
		t.Fatalf("Raw SQLite Task 1 initial state mismatch: status=%s, owner=%s, att=%d, lz=%d", st, ow, att, lz)
	}

	// Task 2: Created with dependency on Task 1 -> STATE: "blocked"
	task2, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Task 2", "Dependent work", "implementation", coord.ReportContract{
		ReviewType: "full_code",
	}, []string{task1.ID})
	if err != nil {
		t.Fatal(err)
	}
	if task2.Status != "blocked" {
		t.Fatalf("Expected Task 2 status 'blocked', got %s", task2.Status)
	}

	// 2. Claim Task 1 -> STATE 2: "running"
	claimed1, err := s.ClaimTask(ctx, p.ID, worker.Token, task1.ID, 10*time.Minute)
	if err != nil {
		t.Fatalf("Claim failed: %v", err)
	}
	if claimed1.Status != "running" || claimed1.Attempt != 1 {
		t.Fatalf("Claimed state mismatch: status=%s, attempt=%d", claimed1.Status, claimed1.Attempt)
	}
	st, ow, att, lz = getRawTask(task1.ID)
	if st != "running" || ow != worker.PrincipalID || att != 1 || lz <= time.Now().UnixMilli() {
		t.Fatalf("Raw SQLite Task 1 running state mismatch: status=%s, owner=%s, att=%d, lz=%d", st, ow, att, lz)
	}

	// 3. Worker reports a blocker on Task 1 -> STATE 3: "blocked"
	blocked1, err := s.TransitionTask(ctx, p.ID, worker.Token, task1.ID, "block", "Waiting for upstream spec clarification", claimed1.Attempt)
	if err != nil {
		t.Fatalf("TransitionTask(block) failed: %v", err)
	}
	if blocked1.Status != "blocked" {
		t.Fatalf("Expected status 'blocked', got %s", blocked1.Status)
	}
	st, ow, att, lz = getRawTask(task1.ID)
	if st != "blocked" || ow != "" || lz != 0 {
		t.Fatalf("Raw SQLite Task 1 blocked state mismatch: status=%s, owner=%s, lz=%d", st, ow, lz)
	}

	// Resolve blocker -> transitions back to "ready"
	resolved1, err := s.TransitionTask(ctx, p.ID, owner.Token, task1.ID, "resolve", "", 0)
	if err != nil {
		t.Fatalf("TransitionTask(resolve) failed: %v", err)
	}
	if resolved1.Status != "ready" {
		t.Fatalf("Expected status 'ready' after resolve, got %s", resolved1.Status)
	}

	// Claim again (attempt 2)
	claimed1_att2, err := s.ClaimTask(ctx, p.ID, worker.Token, task1.ID, 10*time.Minute)
	if err != nil {
		t.Fatalf("Claim attempt 2 failed: %v", err)
	}
	if claimed1_att2.Attempt != 2 || claimed1_att2.Status != "running" {
		t.Fatalf("Unexpected claim state: attempt=%d, status=%s", claimed1_att2.Attempt, claimed1_att2.Status)
	}

	// 4. Submit report -> STATE 4: "submitted"
	reportBody := "## Overview\nReport for task 1.\n"
	report1, err := s.SubmitReport(ctx, p.ID, worker.Token, task1.ID, claimed1_att2.Attempt, "abc1234", reportBody)
	if err != nil {
		t.Fatalf("SubmitReport failed: %v", err)
	}
	st, ow, att, lz = getRawTask(task1.ID)
	if st != "submitted" || lz != 0 {
		t.Fatalf("Raw SQLite Task 1 submitted state mismatch: status=%s, lz=%d", st, lz)
	}

	// Reviewer requests rework -> returns to "ready"
	err = s.RequestRework(ctx, p.ID, reviewer.Token, report1.ID, report1.Digest, "Please expand details")
	if err != nil {
		t.Fatalf("RequestRework failed: %v", err)
	}
	st, ow, _, _ = getRawTask(task1.ID)
	if st != "ready" || ow != "" {
		t.Fatalf("Expected Task 1 status 'ready' after rework request, got %s (owner=%s)", st, ow)
	}

	// Claim again (attempt 3) and submit updated report
	claimed1_att3, err := s.ClaimTask(ctx, p.ID, worker.Token, task1.ID, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	updatedBody := "## Overview\nExpanded report for task 1 with full details.\n"
	report1_v2, err := s.SubmitReport(ctx, p.ID, worker.Token, task1.ID, claimed1_att3.Attempt, "abc1234", updatedBody)
	if err != nil {
		t.Fatal(err)
	}

	// 5. Reviewer accepts report -> STATE 5: "accepted"
	err = s.AcceptReport(ctx, p.ID, reviewer.Token, report1_v2.ID, report1_v2.Digest)
	if err != nil {
		t.Fatalf("AcceptReport failed: %v", err)
	}
	st, _, _, _ = getRawTask(task1.ID)
	if st != "accepted" {
		t.Fatalf("Expected Task 1 status 'accepted', got %s", st)
	}

	// Dependent Task 2 must now have automatically unblocked in SQLite -> "ready"
	st2, _, _, _ := getRawTask(task2.ID)
	if st2 != "ready" {
		t.Fatalf("Expected Task 2 to transition from 'blocked' to 'ready' upon Task 1 acceptance, got %s", st2)
	}

	// 6. Owner publishes new plan revision -> STATE 6: "needs_replan"
	newRev, err := s.PublishPlan(ctx, p.ID, owner.Token, "Plan Revision 2 with major scope changes", 1)
	if err != nil {
		t.Fatalf("PublishPlan failed: %v", err)
	}
	if newRev != 2 {
		t.Fatalf("Expected plan revision 2, got %d", newRev)
	}

	// Both Task 1 (previously accepted) and Task 2 (previously ready) must now be 'needs_replan' in SQLite
	st1_replan, ow1, _, lz1 := getRawTask(task1.ID)
	if st1_replan != "needs_replan" || ow1 != "" || lz1 != 0 {
		t.Fatalf("Task 1 needs_replan state mismatch: status=%s, owner=%s, lz=%d", st1_replan, ow1, lz1)
	}
	st2_replan, ow2, _, lz2 := getRawTask(task2.ID)
	if st2_replan != "needs_replan" || ow2 != "" || lz2 != 0 {
		t.Fatalf("Task 2 needs_replan state mismatch: status=%s, owner=%s, lz=%d", st2_replan, ow2, lz2)
	}

	// 7. Owner cancels Task 2 -> STATE 7: "cancelled"
	cancelled2, err := s.TransitionTask(ctx, p.ID, owner.Token, task2.ID, "cancel", "Deprecated under revision 2", 0)
	if err != nil {
		t.Fatalf("TransitionTask(cancel) failed: %v", err)
	}
	if cancelled2.Status != "cancelled" {
		t.Fatalf("Expected Task 2 status 'cancelled', got %s", cancelled2.Status)
	}
	st2_canc, ow2_canc, _, lz2_canc := getRawTask(task2.ID)
	if st2_canc != "cancelled" || ow2_canc != "" || lz2_canc != 0 {
		t.Fatalf("Task 2 cancelled raw state mismatch: status=%s, owner=%s, lz=%d", st2_canc, ow2_canc, lz2_canc)
	}
}

// TestAdversarial_WebUI401Vs403SessionWiping checks that pkg/coord/ui/app.js
// enforces in-memory data wiping on both HTTP 401 and HTTP 403, and verifies
// the WebHandler status codes.
func TestAdversarial_WebUI401Vs403SessionWiping(t *testing.T) {
	s, _, p, owner, worker, _ := setupM2Test(t)
	ctx := context.Background()

	// 1. Inspect app.js static asset served by WebHandler
	h := coord.NewWebHandler(s)

	doReq := func(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "http://127.0.0.1:8080"+path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:12345"
		req.Host = "127.0.0.1:8080"
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "http://127.0.0.1:8080")
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	login := func(token string) *http.Cookie {
		t.Helper()
		payload := `{"project_id":"` + p.ID + `","token":"` + token + `"}`
		rec := doReq(http.MethodPost, "/api/login", payload, nil)
		if rec.Code != http.StatusOK || len(rec.Result().Cookies()) != 1 {
			t.Fatalf("login failed: %d %s", rec.Code, rec.Body.String())
		}
		return rec.Result().Cookies()[0]
	}

	rec := doReq("GET", "/app.js", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("Failed to retrieve /app.js: status %d", rec.Code)
	}
	appJS := rec.Body.String()

	// A valid project session survives a 403 action denial; only 401 expires credentials.
	if !strings.Contains(appJS, "response.status===401&&projectPath){expireProjectSession") || !strings.Contains(appJS, "e.status===403&&state.project?.project_id===project.project_id") {
		t.Fatalf("app.js does not distinguish invalid credentials from project permission denial")
	}

	// Verify app.js contains comprehensive state wiping on access loss in refresh()
	if !strings.Contains(appJS, "tasks:[],messages:[],repositories:[],workspaces:[],integrations:[],agents:[],presence:[]") {
		t.Fatalf("app.js missing sensitive memory wiping on access denial")
	}

	// 2. HTTP status code verification on WebHandler
	// Unauthenticated request -> 401
	rec401 := doReq("GET", "/api/projects/"+p.ID+"/tasks", "", nil)
	if rec401.Code != http.StatusUnauthorized {
		t.Fatalf("Expected 401 Unauthorized for missing session cookie, got %d", rec401.Code)
	}

	// Authenticated worker requesting owner-only endpoint (/replicas or settings modification) -> 403 Forbidden
	workerCookie := login(worker.Token)
	rec403 := doReq("GET", "/api/projects/"+p.ID+"/replicas", "", workerCookie)
	if rec403.Code != http.StatusForbidden {
		t.Fatalf("Expected 403 Forbidden for worker accessing owner replicas, got %d: %s", rec403.Code, rec403.Body.String())
	}
	if !strings.Contains(rec403.Body.String(), "project role is not allowed to perform that action") {
		t.Fatalf("Expected role denial message, got: %s", rec403.Body.String())
	}

	// Revoke worker principal -> accessing project tasks returns 401 Unauthorized
	err := s.RevokePrincipal(ctx, p.ID, owner.Token, worker.PrincipalID)
	if err != nil {
		t.Fatalf("RevokePrincipal failed: %v", err)
	}

	recRevoked := doReq("GET", "/api/projects/"+p.ID+"/tasks", "", workerCookie)
	if recRevoked.Code != http.StatusUnauthorized {
		t.Fatalf("Expected 401 Unauthorized for revoked principal, got %d", recRevoked.Code)
	}
}
