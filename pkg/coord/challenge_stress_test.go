package coord_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/tasmirz/gossip/pkg/coord"
)

// TestChallengeReviewTypeAdversarialInputs probes ReviewType parsing and validation
// with leading/trailing spaces, mixed case, newlines, tabs, and invalid strings.
func TestChallengeReviewTypeAdversarialInputs(t *testing.T) {
	s, _, p, owner, _ := setup(t)
	ctx := context.Background()

	// 1. Whitespace variations (spaces, tabs, newlines, mixed)
	validWhitespaceInputs := []struct {
		name       string
		reviewType string
		formats    []string
	}{
		{"leading_trailing_spaces_full_code", "   full_code   ", nil},
		{"newlines_full_code", "\n\nfull_code\n\n", nil},
		{"tabs_full_code", "\t\tfull_code\t", nil},
		{"mixed_whitespace_full_code", " \t\r\n full_code \n\r\t ", nil},
		{"leading_trailing_spaces_diagram", "   diagram_report   ", []string{"markdown", "mermaid_sequence", "mermaid_class"}},
		{"newlines_diagram", "\ndiagram_report\n", []string{"markdown", "mermaid_sequence", "mermaid_class"}},
		{"tabs_diagram", "\tdiagram_report\t", []string{"markdown", "mermaid_sequence", "mermaid_class"}},
		{"mixed_whitespace_diagram", " \t \ndiagram_report\n\t ", []string{"markdown", "mermaid_sequence", "mermaid_class"}},
	}

	for _, tc := range validWhitespaceInputs {
		t.Run(tc.name, func(t *testing.T) {
			task, err := s.CreateTaskWithID(ctx, p.ID, owner.Token, tc.name, 1, "Review Deliverable", "Testing whitespace", "review", coord.ReportContract{
				ReviewType: tc.reviewType,
				Formats:    tc.formats,
			}, nil)
			if err != nil {
				t.Fatalf("expected whitespace-trimmed review_type %q to succeed, got error: %v", tc.reviewType, err)
			}
			expectedTrimmed := strings.TrimSpace(tc.reviewType)
			if task.Contract.ReviewType != expectedTrimmed {
				t.Fatalf("task.Contract.ReviewType = %q, want %q", task.Contract.ReviewType, expectedTrimmed)
			}
		})
	}

	// 2. Mixed case: The specification explicitly recognizes lowercase "full_code" and "diagram_report".
	// Mixed case strings like "Full_Code", "FULL_CODE", "Diagram_Report", "DIAGRAM_REPORT" should be rejected as "invalid review type".
	mixedCaseInputs := []string{
		"Full_Code", "FULL_CODE", "full_CODE", "Full_code",
		"Diagram_Report", "DIAGRAM_REPORT", "diagram_Report", "Diagram_report",
	}

	for _, mc := range mixedCaseInputs {
		t.Run("mixed_case_"+mc, func(t *testing.T) {
			_, err := s.CreateTaskWithID(ctx, p.ID, owner.Token, "mixed-"+mc, 1, "Review Mixed", "Testing case", "review", coord.ReportContract{
				ReviewType: mc,
				Formats:    []string{"markdown", "mermaid_sequence", "mermaid_class"},
			}, nil)
			if err == nil {
				t.Fatalf("expected mixed-case review_type %q to be rejected, but it succeeded", mc)
			}
			if !strings.Contains(err.Error(), "invalid review type") {
				t.Fatalf("expected 'invalid review type' error for %q, got: %v", mc, err)
			}
		})
	}

	// 3. Unselected or empty review_type: must prompt the user during planning
	emptyInputs := []string{"", "   ", "\t\t", "\n\n"}
	for i, empty := range emptyInputs {
		t.Run("empty_review_type_"+string(rune('A'+i)), func(t *testing.T) {
			_, err := s.CreateTaskWithID(ctx, p.ID, owner.Token, "empty-"+string(rune('A'+i)), 1, "Review Empty", "Testing empty", "review", coord.ReportContract{
				ReviewType: empty,
			}, nil)
			if err == nil {
				t.Fatal("expected empty review_type to be rejected")
			}
			if !strings.Contains(err.Error(), "review_type must be chosen during planning") {
				t.Fatalf("expected planning prompt error for empty review_type, got: %v", err)
			}
		})
	}

	// 4. Invalid strings and review modes passed as review types
	invalidStrings := []string{
		"summary", "report_first", "targeted_source", "full_source",
		"code", "diagram", "markdown", "full", "diagram_only",
		"full_code\x00null", "diagram_report; DROP TABLE tasks;",
	}
	for _, inv := range invalidStrings {
		t.Run("invalid_"+inv, func(t *testing.T) {
			_, err := s.CreateTaskWithID(ctx, p.ID, owner.Token, "inv-"+inv, 1, "Review Invalid", "Testing invalid", "review", coord.ReportContract{
				ReviewType: inv,
				Formats:    []string{"markdown", "mermaid_sequence", "mermaid_class"},
			}, nil)
			if err == nil {
				t.Fatalf("expected invalid review_type %q to be rejected", inv)
			}
			if !strings.Contains(err.Error(), "invalid review type") {
				t.Fatalf("expected 'invalid review type' for %q, got: %v", inv, err)
			}
		})
	}
}

// TestChallengeMCPGossipTaskFormatsStress tests MCP gossip_task with omitted formats,
// empty list, single format, partial formats, and invalid review types.
func TestChallengeMCPGossipTaskFormatsStress(t *testing.T) {
	store, _, project, owner, _ := setup(t)
	tool := coord.MCPServer(store, project.ID, owner.Token).GetTool("gossip_task")
	if tool == nil {
		t.Fatal("task planning tool missing")
	}
	ctx := context.Background()

	// 1. Omitted formats key when review_type == "diagram_report" -> auto-populates
	t.Run("diagram_report_omitted_formats_key", func(t *testing.T) {
		res, err := tool.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_task", Arguments: map[string]any{
			"operation": "create", "request_id": "omitted-formats-req", "plan_revision": 1,
			"title": "Review auto-populate", "description": "Omitted formats key", "kind": "review",
			"review_type": "diagram_report",
		}}})
		if err != nil {
			t.Fatal(err)
		}
		if res.IsError {
			t.Fatalf("diagram_report with omitted formats key failed: %+v", res)
		}
		var task coord.Task
		if err := json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &task); err != nil {
			t.Fatal(err)
		}
		if len(task.Contract.Formats) != 3 {
			t.Fatalf("expected 3 auto-populated formats, got %d: %+v", len(task.Contract.Formats), task.Contract.Formats)
		}
	})

	// 2. Empty formats list when review_type == "diagram_report" -> auto-populates
	t.Run("diagram_report_empty_formats_list", func(t *testing.T) {
		res, err := tool.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_task", Arguments: map[string]any{
			"operation": "create", "request_id": "empty-formats-req", "plan_revision": 1,
			"title": "Review empty formats", "description": "Empty formats list", "kind": "review",
			"review_type": "diagram_report",
			"formats":     []any{},
		}}})
		if err != nil {
			t.Fatal(err)
		}
		if res.IsError {
			t.Fatalf("diagram_report with empty formats list failed: %+v", res)
		}
		var task coord.Task
		if err := json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &task); err != nil {
			t.Fatal(err)
		}
		if len(task.Contract.Formats) != 3 {
			t.Fatalf("expected 3 auto-populated formats, got %d: %+v", len(task.Contract.Formats), task.Contract.Formats)
		}
	})

	// 3. Single format when review_type == "diagram_report" -> rejected
	singleFormats := [][]any{
		{"markdown"},
		{"mermaid_sequence"},
		{"mermaid_class"},
	}
	for _, sf := range singleFormats {
		t.Run("diagram_report_single_format_"+sf[0].(string), func(t *testing.T) {
			res, err := tool.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_task", Arguments: map[string]any{
				"operation": "create", "request_id": "single-fmt-" + sf[0].(string), "plan_revision": 1,
				"title": "Review single format", "description": "Single format test", "kind": "review",
				"review_type": "diagram_report",
				"formats":     sf,
			}}})
			if err != nil {
				t.Fatal(err)
			}
			if !res.IsError {
				t.Fatalf("expected single format %v to be rejected, but it succeeded", sf)
			}
			errMsg := res.Content[0].(mcp.TextContent).Text
			if !strings.Contains(errMsg, "diagram_report requires markdown, mermaid_sequence, and mermaid_class formats") {
				t.Fatalf("unexpected error message: %s", errMsg)
			}
		})
	}

	// 4. Two formats (missing one required) -> rejected
	partialPairs := [][2]string{
		{"markdown", "mermaid_sequence"},
		{"markdown", "mermaid_class"},
		{"mermaid_sequence", "mermaid_class"},
	}
	for _, pair := range partialPairs {
		t.Run("diagram_report_partial_"+pair[0]+"_"+pair[1], func(t *testing.T) {
			res, err := tool.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_task", Arguments: map[string]any{
				"operation": "create", "request_id": "partial-" + pair[0] + "-" + pair[1], "plan_revision": 1,
				"title": "Review partial formats", "description": "Partial formats test", "kind": "review",
				"review_type": "diagram_report",
				"formats":     []any{pair[0], pair[1]},
			}}})
			if err != nil {
				t.Fatal(err)
			}
			if !res.IsError {
				t.Fatalf("expected partial formats %v to be rejected, but it succeeded", pair)
			}
			errMsg := res.Content[0].(mcp.TextContent).Text
			if !strings.Contains(errMsg, "diagram_report requires markdown, mermaid_sequence, and mermaid_class formats") {
				t.Fatalf("unexpected error message: %s", errMsg)
			}
		})
	}

	// 5. full_code review type with empty formats or arbitrary formats -> succeeds
	t.Run("full_code_omitted_formats", func(t *testing.T) {
		res, err := tool.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_task", Arguments: map[string]any{
			"operation": "create", "request_id": "full-code-omitted-fmt", "plan_revision": 1,
			"title": "Review full code", "description": "Full code omitted formats", "kind": "review",
			"review_type": "full_code",
		}}})
		if err != nil {
			t.Fatal(err)
		}
		if res.IsError {
			t.Fatalf("full_code with omitted formats failed: %+v", res)
		}
	})

	// 6. Invalid review type in MCP call -> rejected
	t.Run("invalid_review_type_in_mcp", func(t *testing.T) {
		res, err := tool.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_task", Arguments: map[string]any{
			"operation": "create", "request_id": "invalid-rt-req", "plan_revision": 1,
			"title": "Review invalid", "description": "Invalid review type", "kind": "review",
			"review_type": "unsupported_type",
		}}})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError {
			t.Fatal("expected unsupported review type to be rejected")
		}
		errMsg := res.Content[0].(mcp.TextContent).Text
		if !strings.Contains(errMsg, "invalid review type") {
			t.Fatalf("unexpected error message: %s", errMsg)
		}
	})
}

// TestChallengeRolePresetsPrivilegeDecouplingAndRevocation tests role preset sources,
// attempt to claim with revoked/expired/unassigned principals, and verifies that work
// roles never grant permissions.
func TestChallengeRolePresetsPrivilegeDecouplingAndRevocation(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()

	// 1. Invariant: Starter role presets must pin first-party sources without untrusted URLs
	presets := coord.WorkRolePresets()
	approvedDomains := map[string]bool{
		"developers.openai.com": true,
		"openai.github.io":      true,
	}
	for _, preset := range presets {
		if len(preset.Sources) == 0 {
			t.Fatalf("preset %s has no sources", preset.Name)
		}
		for _, src := range preset.Sources {
			if !strings.HasPrefix(src, "https://") {
				t.Fatalf("preset %s source %q does not use https", preset.Name, src)
			}
			u, err := url.Parse(src)
			if err != nil {
				t.Fatalf("failed to parse preset source URL %q: %v", src, err)
			}
			if !approvedDomains[u.Hostname()] {
				t.Fatalf("preset %s uses unapproved source domain %q", preset.Name, u.Hostname())
			}
		}
	}

	// 2. Privilege Decoupling: Assigning "coordinator" role MUST NEVER grant project permissions
	role, err := s.SetWorkRole(ctx, p.ID, owner.Token, worker.PrincipalID, "", "coordinator", "", 0, 0)
	if err != nil {
		t.Fatalf("failed to assign coordinator role: %v", err)
	}
	if role.Name != "coordinator" {
		t.Fatalf("expected coordinator role, got %s", role.Name)
	}

	// Verify permissions list for worker: only initial worker permissions exist
	workerPerms, err := s.ListPermissions(ctx, p.ID, worker.Token)
	if err != nil {
		t.Fatalf("failed to list worker permissions: %v", err)
	}
	for _, perm := range workerPerms {
		if perm.Permission == coord.PermissionTaskDelegate || perm.Permission == coord.PermissionPlanEdit ||
			perm.Permission == coord.PermissionSettingsEdit || perm.Permission == coord.PermissionMembershipManage {
			t.Fatalf("CRITICAL SECURITY VIOLATION: work role granted privileged permission %q to worker!", perm.Permission)
		}
	}

	// Directly attempt privileged actions with worker token:
	// A. CreateTask (requires task.delegate)
	_, err = s.CreateTask(ctx, p.ID, worker.Token, 1, "Unauthorized Task", "Desc", "review", coord.ReportContract{ReviewType: "full_code"}, nil)
	if !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("worker with coordinator role was able to create task: %v", err)
	}

	// B. PublishPlan (requires plan.edit)
	_, err = s.PublishPlan(ctx, p.ID, worker.Token, "Tampered Plan", 0)
	if !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("worker with coordinator role was able to edit plan: %v", err)
	}

	// C. UpdateSettings (requires settings.edit)
	validSettings := coord.Settings{Revision: 0, MaxParallel: 5, MaxLeaseSeconds: 300, SummaryTokenTarget: 1000, DefaultReviewMode: "summary"}
	_, err = s.UpdateSettings(ctx, p.ID, worker.Token, validSettings)
	if !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("worker with coordinator role was able to update settings: %v", err)
	}

	// D. AddPrincipal (requires membership.manage)
	_, err = s.AddPrincipal(ctx, p.ID, worker.Token, "intruder", "worker")
	if !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("worker with coordinator role was able to invite principal: %v", err)
	}

	// 3. Attempt to claim role-protected task with unassigned, wrong, expired, or revoked principal
	// Create task requiring "implementer"
	roleTask, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Implement Feature", "Needs implementer", "implementation", coord.ReportContract{
		ReviewType:   "full_code",
		RequiredRole: "implementer",
	}, nil)
	if err != nil {
		t.Fatalf("failed to create roleTask: %v", err)
	}

	// A. Worker has "coordinator" role, NOT "implementer" -> claim must fail
	_, err = s.ClaimTask(ctx, p.ID, worker.Token, roleTask.ID, time.Hour)
	if err == nil || !strings.Contains(err.Error(), "requires the active \"implementer\" role assignment") {
		t.Fatalf("worker with wrong role claimed task: %v", err)
	}

	// Create a second worker
	worker2, err := s.AddPrincipal(ctx, p.ID, owner.Token, "worker2", "worker")
	if err != nil {
		t.Fatalf("failed to invite worker2: %v", err)
	}
	if err := s.AcknowledgePlan(ctx, p.ID, worker2.Token, 1); err != nil {
		t.Fatalf("failed to acknowledge plan for worker2: %v", err)
	}

	// B. Worker 2 has NO role assigned -> claim must fail
	_, err = s.ClaimTask(ctx, p.ID, worker2.Token, roleTask.ID, time.Hour)
	if err == nil || !strings.Contains(err.Error(), "requires the active \"implementer\" role assignment") {
		t.Fatalf("worker with no role claimed task: %v", err)
	}

	// C. Assign "implementer" to Worker 2 -> claim should now SUCCEED
	role2, err := s.SetWorkRole(ctx, p.ID, owner.Token, worker2.PrincipalID, "", "implementer", "", 0, 0)
	if err != nil {
		t.Fatalf("failed to assign implementer role: %v", err)
	}
	claimedTask, err := s.ClaimTask(ctx, p.ID, worker2.Token, roleTask.ID, time.Hour)
	if err != nil {
		t.Fatalf("worker2 with implementer role failed to claim task: %v", err)
	}
	if claimedTask.Status != "running" {
		t.Fatalf("claimed task status = %s, want running", claimedTask.Status)
	}

	// Create another task requiring "implementer"
	roleTask2, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Implement Feature 2", "Needs implementer", "implementation", coord.ReportContract{
		ReviewType:   "full_code",
		RequiredRole: "implementer",
	}, nil)
	if err != nil {
		t.Fatalf("failed to create roleTask2: %v", err)
	}

	// D. Revoke Worker 2 -> attempt to claim roleTask2 must FAIL even though work_roles row exists!
	if err := s.RevokePrincipal(ctx, p.ID, owner.Token, worker2.PrincipalID); err != nil {
		t.Fatalf("failed to revoke worker2: %v", err)
	}
	_, err = s.ClaimTask(ctx, p.ID, worker2.Token, roleTask2.ID, time.Hour)
	if err == nil {
		t.Fatal("revoked worker2 was able to claim roleTask2!")
	}
	if !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized for revoked worker2, got: %v", err)
	}

	// Owner attempts to AssignTask to revoked worker2 -> must FAIL
	_, err = s.AssignTask(ctx, p.ID, owner.Token, roleTask2.ID, worker2.PrincipalID)
	if err == nil {
		t.Fatal("owner was able to assign roleTask2 to revoked worker2")
	}

	// E. Expired role assignment test
	worker3, err := s.AddPrincipal(ctx, p.ID, owner.Token, "worker3", "worker")
	if err != nil {
		t.Fatalf("failed to invite worker3: %v", err)
	}
	if err := s.AcknowledgePlan(ctx, p.ID, worker3.Token, 1); err != nil {
		t.Fatalf("failed to acknowledge plan for worker3: %v", err)
	}
	// Assign role with expiration 50ms in the future
	expiresAt := time.Now().Add(50 * time.Millisecond).UnixMilli()
	_, err = s.SetWorkRole(ctx, p.ID, owner.Token, worker3.PrincipalID, "", "implementer", "", expiresAt, 0)
	if err != nil {
		t.Fatalf("failed to assign expiring role to worker3: %v", err)
	}

	// Sleep to let role expire
	time.Sleep(100 * time.Millisecond)

	// Worker 3 attempts to claim roleTask2 with expired role -> must FAIL
	_, err = s.ClaimTask(ctx, p.ID, worker3.Token, roleTask2.ID, time.Hour)
	if err == nil || !strings.Contains(err.Error(), "requires the active \"implementer\" role assignment") {
		t.Fatalf("worker3 with expired role was able to claim task: %v", err)
	}

	// F. Task-scoped role test
	worker4, err := s.AddPrincipal(ctx, p.ID, owner.Token, "worker4", "worker")
	if err != nil {
		t.Fatalf("failed to invite worker4: %v", err)
	}
	if err := s.AcknowledgePlan(ctx, p.ID, worker4.Token, 1); err != nil {
		t.Fatalf("failed to acknowledge plan for worker4: %v", err)
	}
	// Assign implementer role SCOPED only to roleTask.ID
	_, err = s.SetWorkRole(ctx, p.ID, owner.Token, worker4.PrincipalID, roleTask.ID, "implementer", "", 0, 0)
	if err != nil {
		t.Fatalf("failed to set task-scoped role: %v", err)
	}
	// Worker 4 attempts to claim roleTask2 (different task) -> must FAIL
	_, err = s.ClaimTask(ctx, p.ID, worker4.Token, roleTask2.ID, time.Hour)
	if err == nil || !strings.Contains(err.Error(), "requires the active \"implementer\" role assignment") {
		t.Fatalf("worker4 with task-scoped role claimed a different task: %v", err)
	}

	_ = role2
}

// TestChallengeDiagramReportMutationsAndValidation tests that SubmitReport strictly validates
// diagram_report requirements: all required sections, substantive narrative, valid UML class
// diagrams with relationships, valid sequence diagrams with message arrows, and planned coverage.
func TestChallengeDiagramReportMutationsAndValidation(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()

	// Acknowledge plan
	if err := s.AcknowledgePlan(ctx, p.ID, worker.Token, 1); err != nil {
		t.Fatalf("failed to acknowledge plan: %v", err)
	}
	// Increase MaxParallel to accommodate multiple subtests without hitting concurrency cap
	curSettings, err := s.GetSettings(ctx, p.ID, owner.Token)
	if err != nil {
		t.Fatalf("failed to get settings: %v", err)
	}
	curSettings.MaxParallel = 100
	if _, err := s.UpdateSettings(ctx, p.ID, owner.Token, curSettings); err != nil {
		t.Fatalf("failed to update settings: %v", err)
	}

	validReport := strings.Join([]string{
		"# Architecture review",
		"",
		"## Review summary",
		"The reviewed architecture enforces project-scoped access before processing a worker review request.",
		"",
		"## Scope and evidence",
		"This report traces the coordination service at the recorded baseline and compares the flow with the project access criteria.",
		"",
		"## UML class diagram",
		"The classes and relationship below show which service objects enforce this boundary.",
		"",
		"```mermaid",
		"classDiagram",
		"  Coordinator --> Worker",
		"```",
		"",
		"## Sequence diagram",
		"This sequence records the request path from the coordinator through authorization to the worker.",
		"",
		"```mermaid",
		"sequenceDiagram",
		"  Coordinator->>Worker: request review",
		"```",
		"",
		"## Findings",
		"The coordinator delegates only after checking project access; the request preserves the selected review contract.",
		"",
		"## Coverage and limitations",
		"Project access and review handoff are covered; runtime behavior was not traced and should be checked separately.",
	}, "\n")

	// Helper to create and claim task
	createAndClaim := func(coverage []string) coord.Task {
		task, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Review Deliverable Task", "Desc", "review", coord.ReportContract{
			ReviewType: "diagram_report",
			Formats:    []string{"markdown", "mermaid_sequence", "mermaid_class"},
			Coverage:   coverage,
		}, nil)
		if err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		claimed, err := s.ClaimTask(ctx, p.ID, worker.Token, task.ID, time.Hour)
		if err != nil {
			t.Fatalf("ClaimTask: %v", err)
		}
		return claimed
	}

	// 1. Valid baseline report passes
	t.Run("valid_baseline_report_succeeds", func(t *testing.T) {
		task := createAndClaim([]string{"project access", "review handoff"})
		rep, err := s.SubmitReport(ctx, p.ID, worker.Token, task.ID, task.Attempt, "commit:base", validReport)
		if err != nil {
			t.Fatalf("expected valid report to succeed, got: %v", err)
		}
		if rep.ID == "" {
			t.Fatal("expected non-empty report ID")
		}
	})

	// 2. Missing UML class diagram section
	t.Run("missing_uml_class_section_rejected", func(t *testing.T) {
		task := createAndClaim([]string{"project access", "review handoff"})
		mutated := strings.Replace(validReport, "## UML class diagram", "## Component Architecture", 1)
		_, err := s.SubmitReport(ctx, p.ID, worker.Token, task.ID, task.Attempt, "commit:base", mutated)
		if err == nil {
			t.Fatal("expected missing UML class diagram section to be rejected, got nil")
		}
		if !strings.Contains(err.Error(), "diagram sections") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	// 3. Broken class diagram without relationships (isolated class only)
	t.Run("class_diagram_without_relationships_rejected", func(t *testing.T) {
		task := createAndClaim([]string{"project access", "review handoff"})
		mutated := strings.Replace(validReport, "  Coordinator --> Worker", "  class IsolatedClass", 1)
		_, err := s.SubmitReport(ctx, p.ID, worker.Token, task.ID, task.Attempt, "commit:base", mutated)
		if err == nil {
			t.Fatal("expected class diagram without relationships to be rejected, got nil")
		}
		if !strings.Contains(err.Error(), "diagram sections") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	// 4. Missing Sequence diagram section
	t.Run("missing_sequence_diagram_section_rejected", func(t *testing.T) {
		task := createAndClaim([]string{"project access", "review handoff"})
		mutated := strings.Replace(validReport, "## Sequence diagram", "## Communication Flow", 1)
		_, err := s.SubmitReport(ctx, p.ID, worker.Token, task.ID, task.Attempt, "commit:base", mutated)
		if err == nil {
			t.Fatal("expected missing sequence diagram section to be rejected, got nil")
		}
		if !strings.Contains(err.Error(), "diagram sections") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	// 5. Broken sequence diagram without message arrows (participants only)
	t.Run("sequence_diagram_without_messages_rejected", func(t *testing.T) {
		task := createAndClaim([]string{"project access", "review handoff"})
		mutated := strings.Replace(validReport, "  Coordinator->>Worker: request review", "  participant Coordinator\n  participant Worker", 1)
		_, err := s.SubmitReport(ctx, p.ID, worker.Token, task.ID, task.Attempt, "commit:base", mutated)
		if err == nil {
			t.Fatal("expected sequence diagram without messages to be rejected, got nil")
		}
		if !strings.Contains(err.Error(), "diagram sections") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	// 6. Dropping a planned coverage item
	t.Run("missing_planned_coverage_item_rejected", func(t *testing.T) {
		task := createAndClaim([]string{"project access", "review handoff", "unaddressed_security_boundary"})
		// validReport addresses "project access" and "review handoff", but NOT "unaddressed_security_boundary"
		_, err := s.SubmitReport(ctx, p.ID, worker.Token, task.ID, task.Attempt, "commit:base", validReport)
		if err == nil {
			t.Fatal("expected report missing planned coverage item to be rejected, got nil")
		}
		if !strings.Contains(err.Error(), "missing: unaddressed_security_boundary") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	// 7. Non-substantive narrative (< 24 chars or < 4 words)
	t.Run("non_substantive_narrative_rejected", func(t *testing.T) {
		task := createAndClaim([]string{"project access", "review handoff"})
		mutated := strings.Replace(validReport, "The reviewed architecture enforces project-scoped access before processing a worker review request.", "Looks ok.", 1)
		_, err := s.SubmitReport(ctx, p.ID, worker.Token, task.ID, task.Attempt, "commit:base", mutated)
		if err == nil {
			t.Fatal("expected non-substantive narrative to be rejected, got nil")
		}
		if !strings.Contains(err.Error(), "populated Markdown 'Review summary' section") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})
}
