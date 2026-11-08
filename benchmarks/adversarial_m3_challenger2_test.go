package benchmarks_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tasmirz/gossip/benchmarks"
	"github.com/tasmirz/gossip/pkg/coord"
	_ "modernc.org/sqlite"
)

func setupM3ChallengerTest(t *testing.T) (*coord.Store, string, coord.Project, coord.Credential, []coord.Credential) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "m3_adversarial_challenger2.db")
	s, err := coord.Open(dbPath)
	if err != nil {
		t.Fatalf("coord.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	ctx := context.Background()
	p, owner, err := s.CreateProject(ctx, "m3-challenge-project", "Owner Alice")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	workers := make([]coord.Credential, 4)
	for i := 0; i < 4; i++ {
		w, err := s.AddPrincipal(ctx, p.ID, owner.Token, fmt.Sprintf("Worker-%d", i), "worker")
		if err != nil {
			t.Fatalf("AddPrincipal %d: %v", i, err)
		}
		workers[i] = w
	}

	if _, err = s.PublishPlan(ctx, p.ID, owner.Token, "Milestone 3 Challenge Plan", 0); err != nil {
		t.Fatalf("PublishPlan: %v", err)
	}

	for i, w := range workers {
		if err = s.AcknowledgePlan(ctx, p.ID, w.Token, 1); err != nil {
			t.Fatalf("AcknowledgePlan worker %d: %v", i, err)
		}
	}

	settings, err := s.GetSettings(ctx, p.ID, owner.Token)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	settings.MaxParallel = 128
	if _, err := s.UpdateSettings(ctx, p.ID, owner.Token, settings); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	return s, dbPath, p, owner, workers
}

// TestAdversarial_ConcurrentSQLiteTaskLifecycle verifies that under high concurrent load,
// task creation, claiming (with mutual exclusion race), report submission, and report acceptance
// operate reliably against a live SQLite WAL store without corruption or unexpected locks.
func TestAdversarial_ConcurrentSQLiteTaskLifecycle(t *testing.T) {
	s, dbPath, project, owner, workers := setupM3ChallengerTest(t)
	ctx := context.Background()

	// Verify WAL mode directly
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open for pragma check: %v", err)
	}
	defer rawDB.Close()
	var journalMode string
	if err := rawDB.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("PRAGMA journal_mode query: %v", err)
	}
	if strings.ToLower(journalMode) != "wal" {
		t.Fatalf("database journal_mode = %s, want wal", journalMode)
	}

	fixtures := benchmarks.GetRepresentativeTaskFixtures()
	const numTasks = 12
	type taskMeta struct {
		task    coord.Task
		fixture benchmarks.BenchmarkTaskFixture
	}
	tasks := make([]taskMeta, numTasks)

	// Phase 1: Concurrently create 12 tasks with diagram_report contracts
	var createWg sync.WaitGroup
	createErrCh := make(chan error, numTasks)

	for i := 0; i < numTasks; i++ {
		createWg.Add(1)
		go func(idx int) {
			defer createWg.Done()
			fix := fixtures[idx%len(fixtures)]
			contract := coord.ReportContract{
				ReviewType: "diagram_report",
				Formats:    []string{"markdown", "mermaid_sequence", "mermaid_class"},
				Coverage:   fix.PlannedCoverage,
			}
			task, err := s.CreateTask(ctx, project.ID, owner.Token, 1,
				fmt.Sprintf("Concurrent Task %d: %s", idx, fix.Title),
				"Concurrent stress review task", "review", contract, nil)
			if err != nil {
				createErrCh <- fmt.Errorf("task %d creation failed: %w", idx, err)
				return
			}
			tasks[idx] = taskMeta{task: task, fixture: fix}
		}(i)
	}
	createWg.Wait()
	close(createErrCh)
	for err := range createErrCh {
		t.Fatal(err)
	}

	// Phase 2: Concurrent Task Claiming & Mutual Exclusion Stress
	// For each task, multiple workers race to claim it.
	// Exactly ONE worker must succeed for each task.
	var claimWg sync.WaitGroup
	type claimResult struct {
		taskID  string
		worker  coord.Credential
		claimed coord.Task
		fixture benchmarks.BenchmarkTaskFixture
	}
	claimResults := make(chan claimResult, numTasks)
	var totalClaimsSucceeded int32
	var totalConflicts int32

	for i := 0; i < numTasks; i++ {
		for _, w := range workers {
			claimWg.Add(1)
			go func(tm taskMeta, workerCred coord.Credential) {
				defer claimWg.Done()
				claimed, err := s.ClaimTask(ctx, project.ID, workerCred.Token, tm.task.ID, 2*time.Minute)
				if err == nil {
					atomic.AddInt32(&totalClaimsSucceeded, 1)
					claimResults <- claimResult{
						taskID:  tm.task.ID,
						worker:  workerCred,
						claimed: claimed,
						fixture: tm.fixture,
					}
				} else if errors.Is(err, coord.ErrConflict) {
					atomic.AddInt32(&totalConflicts, 1)
				} else {
					t.Errorf("unexpected claim error for task %s: %v", tm.task.ID, err)
				}
			}(tasks[i], w)
		}
	}
	claimWg.Wait()
	close(claimResults)

	if totalClaimsSucceeded != int32(numTasks) {
		t.Fatalf("total successful claims = %d, want %d", totalClaimsSucceeded, numTasks)
	}
	expectedConflicts := int32(numTasks * (len(workers) - 1))
	if totalConflicts != expectedConflicts {
		t.Fatalf("total conflicts = %d, want %d", totalConflicts, expectedConflicts)
	}

	// Phase 3: Concurrently Submit Diagram Reports and Accept them
	var submitAcceptWg sync.WaitGroup
	errCh := make(chan error, numTasks*2)

	for cr := range claimResults {
		submitAcceptWg.Add(1)
		go func(res claimResult) {
			defer submitAcceptWg.Done()
			report, err := s.SubmitReport(ctx, project.ID, res.worker.Token, res.claimed.ID, res.claimed.Attempt,
				"commit:concurrent-baseline", res.fixture.DiagramReportBody)
			if err != nil {
				errCh <- fmt.Errorf("SubmitReport for task %s: %w", res.taskID, err)
				return
			}
			if report.TaskID != res.taskID {
				errCh <- fmt.Errorf("report task ID mismatch: got %s, want %s", report.TaskID, res.taskID)
				return
			}

			// Owner accepts report
			if err := s.AcceptReport(ctx, project.ID, owner.Token, report.ID, report.Digest); err != nil {
				errCh <- fmt.Errorf("AcceptReport for task %s: %w", res.taskID, err)
				return
			}

			// Verify status is accepted
			finalTask, err := s.GetTask(ctx, project.ID, owner.Token, res.taskID)
			if err != nil {
				errCh <- fmt.Errorf("GetTask for task %s: %w", res.taskID, err)
				return
			}
			if finalTask.Status != "accepted" {
				errCh <- fmt.Errorf("task %s final status = %s, want accepted", res.taskID, finalTask.Status)
				return
			}
		}(cr)
	}
	submitAcceptWg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

// TestAdversarial_MutateDiagramReportFixtures systematically subjects all representative
// benchmark task diagram reports to adversarial mutations (omitting sections, breaking Mermaid syntax,
// dropping coverage items, removing prose) to ensure validateReportBody strictly fails on broken
// reports while passing on valid ones.
func TestAdversarial_MutateDiagramReportFixtures(t *testing.T) {
	s, _, project, owner, workers := setupM3ChallengerTest(t)
	worker := workers[0]
	ctx := context.Background()

	fixtures := benchmarks.GetRepresentativeTaskFixtures()
	if len(fixtures) < 3 {
		t.Fatalf("expected at least 3 fixtures, got %d", len(fixtures))
	}

	for _, fixture := range fixtures {
		contract := coord.ReportContract{
			ReviewType: "diagram_report",
			Formats:    []string{"markdown", "mermaid_sequence", "mermaid_class"},
			Coverage:   fixture.PlannedCoverage,
		}

		// Helper to create and claim a fresh task for mutation testing
		newClaimedTask := func(t *testing.T, customCoverage []string) coord.Task {
			t.Helper()
			c := contract
			if customCoverage != nil {
				c.Coverage = customCoverage
			}
			task, err := s.CreateTask(ctx, project.ID, owner.Token, 1,
				"Mutation Test: "+fixture.Title, "Description", "review", c, nil)
			if err != nil {
				t.Fatalf("CreateTask: %v", err)
			}
			claimed, err := s.ClaimTask(ctx, project.ID, worker.Token, task.ID, time.Minute)
			if err != nil {
				t.Fatalf("ClaimTask: %v", err)
			}
			return claimed
		}

		// Baseline: Unmutated fixture report MUST succeed
		t.Run(fixture.TaskID+"/BaselineValid", func(t *testing.T) {
			task := newClaimedTask(t, nil)
			report, err := s.SubmitReport(ctx, project.ID, worker.Token, task.ID, task.Attempt, "commit:baseline", fixture.DiagramReportBody)
			if err != nil {
				t.Fatalf("valid baseline report rejected: %v", err)
			}
			if err := s.AcceptReport(ctx, project.ID, owner.Token, report.ID, report.Digest); err != nil {
				t.Fatalf("AcceptReport on valid baseline failed: %v", err)
			}
		})

		// Mutation 1: Drop required narrative sections
		requiredSections := []string{"Review summary", "Scope and evidence", "Findings", "Coverage and limitations"}
		for _, sec := range requiredSections {
			secName := sec
			t.Run(fixture.TaskID+"/DropSection_"+secName, func(t *testing.T) {
				task := newClaimedTask(t, nil)
				mutated := strings.Replace(fixture.DiagramReportBody, "## "+secName, "## Deprecated "+secName, 1)
				_, err := s.SubmitReport(ctx, project.ID, worker.Token, task.ID, task.Attempt, "commit:baseline", mutated)
				if err == nil {
					t.Fatalf("expected error when omitting section %q, got nil", secName)
				}
				expectedErrMsg := fmt.Sprintf("diagram_report requires a populated Markdown '%s' section", secName)
				if !strings.Contains(err.Error(), expectedErrMsg) {
					t.Fatalf("error = %q, want substring %q", err.Error(), expectedErrMsg)
				}
			})
		}

		// Mutation 2: Trivial non-substantive narrative (less than 4 words or 24 chars)
		t.Run(fixture.TaskID+"/NonSubstantiveReviewSummary", func(t *testing.T) {
			task := newClaimedTask(t, nil)
			lines := strings.Split(fixture.DiagramReportBody, "\n")
			var newLines []string
			skip := false
			for _, line := range lines {
				if strings.HasPrefix(line, "## Review summary") {
					newLines = append(newLines, line, "Looks ok.")
					skip = true
					continue
				}
				if skip && strings.HasPrefix(line, "## ") {
					skip = false
				}
				if !skip {
					newLines = append(newLines, line)
				}
			}
			mutated := strings.Join(newLines, "\n")
			_, err := s.SubmitReport(ctx, project.ID, worker.Token, task.ID, task.Attempt, "commit:baseline", mutated)
			if err == nil {
				t.Fatal("expected rejection for non-substantive Review summary, got nil")
			}
			if !strings.Contains(err.Error(), "Review summary") {
				t.Fatalf("error = %q, want mention of Review summary", err.Error())
			}
		})

		// Mutation 3: Break Mermaid UML Class Diagram
		t.Run(fixture.TaskID+"/MissingUMLClassSection", func(t *testing.T) {
			task := newClaimedTask(t, nil)
			mutated := strings.Replace(fixture.DiagramReportBody, "## UML class diagram", "## Architecture Classes", 1)
			_, err := s.SubmitReport(ctx, project.ID, worker.Token, task.ID, task.Attempt, "commit:baseline", mutated)
			if err == nil {
				t.Fatal("expected rejection for missing UML class diagram section, got nil")
			}
			if !strings.Contains(err.Error(), "diagram sections") {
				t.Fatalf("error = %q, want diagram sections error", err.Error())
			}
		})

		t.Run(fixture.TaskID+"/BrokenUMLClassDiagram_NoRelationships", func(t *testing.T) {
			task := newClaimedTask(t, nil)
			brokenClass := "\nThis diagram shows an isolated component without relationships.\n\n```mermaid\nclassDiagram\n  class IsolatedClass\n```\n"
			mutated := replaceSectionContent(fixture.DiagramReportBody, "UML class diagram", brokenClass)
			_, err := s.SubmitReport(ctx, project.ID, worker.Token, task.ID, task.Attempt, "commit:baseline", mutated)
			if err == nil {
				t.Fatal("expected rejection for class diagram without relationships, got nil")
			}
			if !strings.Contains(err.Error(), "diagram sections") {
				t.Fatalf("error = %q, want diagram sections error", err.Error())
			}
		})

		// Mutation 4: Break Mermaid Sequence Diagram
		t.Run(fixture.TaskID+"/MissingSequenceDiagramSection", func(t *testing.T) {
			task := newClaimedTask(t, nil)
			mutated := strings.Replace(fixture.DiagramReportBody, "## Sequence diagram", "## Interaction Flow", 1)
			_, err := s.SubmitReport(ctx, project.ID, worker.Token, task.ID, task.Attempt, "commit:baseline", mutated)
			if err == nil {
				t.Fatal("expected rejection for missing Sequence diagram section, got nil")
			}
			if !strings.Contains(err.Error(), "diagram sections") {
				t.Fatalf("error = %q, want diagram sections error", err.Error())
			}
		})

		t.Run(fixture.TaskID+"/BrokenSequenceDiagram_NoMessages", func(t *testing.T) {
			task := newClaimedTask(t, nil)
			brokenSeq := "\nThis sequence diagram has participants but zero message arrows.\n\n```mermaid\nsequenceDiagram\n  participant Client\n  participant Server\n```\n"
			mutated := replaceSectionContent(fixture.DiagramReportBody, "Sequence diagram", brokenSeq)
			_, err := s.SubmitReport(ctx, project.ID, worker.Token, task.ID, task.Attempt, "commit:baseline", mutated)
			if err == nil {
				t.Fatal("expected rejection for sequence diagram without messages, got nil")
			}
			if !strings.Contains(err.Error(), "diagram sections") {
				t.Fatalf("error = %q, want diagram sections error", err.Error())
			}
		})

		// Mutation 5: Drop Planned Coverage Item
		if len(fixture.PlannedCoverage) > 0 {
			missingItem := fixture.PlannedCoverage[0]
			t.Run(fixture.TaskID+"/DropCoverageItem_"+missingItem, func(t *testing.T) {
				task := newClaimedTask(t, nil)
				// Case-insensitive removal of all occurrences of missingItem
				re := regexp.MustCompile("(?i)" + regexp.QuoteMeta(missingItem))
				mutated := re.ReplaceAllString(fixture.DiagramReportBody, "[OMITTED_COVERAGE_ITEM]")
				_, err := s.SubmitReport(ctx, project.ID, worker.Token, task.ID, task.Attempt, "commit:baseline", mutated)
				if err == nil {
					t.Fatalf("expected rejection when dropping coverage item %q, got nil", missingItem)
				}
				expectedCoverageErr := "missing: " + missingItem
				if !strings.Contains(err.Error(), expectedCoverageErr) {
					t.Fatalf("error = %q, want %q", err.Error(), expectedCoverageErr)
				}
			})
		}

		t.Run(fixture.TaskID+"/UnaddressedExtraPlannedCoverage", func(t *testing.T) {
			unaddressedItem := "unaddressed_strict_cryptographic_nonce_validation"
			task := newClaimedTask(t, append(fixture.PlannedCoverage, unaddressedItem))
			_, err := s.SubmitReport(ctx, project.ID, worker.Token, task.ID, task.Attempt, "commit:baseline", fixture.DiagramReportBody)
			if err == nil {
				t.Fatalf("expected rejection when report omits planned coverage item %q, got nil", unaddressedItem)
			}
			expectedCoverageErr := "missing: " + unaddressedItem
			if !strings.Contains(err.Error(), expectedCoverageErr) {
				t.Fatalf("error = %q, want %q", err.Error(), expectedCoverageErr)
			}
		})

		// Mutation 6: Strip all Markdown prose outside fence blocks
		t.Run(fixture.TaskID+"/DiagramOnlyWithoutProse", func(t *testing.T) {
			task := newClaimedTask(t, nil)
			diagramOnly := "## Review summary\n\n## Scope and evidence\n\n## UML class diagram\n\n```mermaid\nclassDiagram\n  A --> B\n```\n\n## Sequence diagram\n\n```mermaid\nsequenceDiagram\n  A->>B: ping\n```\n\n## Findings\n\n## Coverage and limitations\n"
			_, err := s.SubmitReport(ctx, project.ID, worker.Token, task.ID, task.Attempt, "commit:baseline", diagramOnly)
			if err == nil {
				t.Fatal("expected rejection for body without substantive prose, got nil")
			}
		})

		// Mutation 7: Valid Invariances (Case-varied headings should be accepted)
		t.Run(fixture.TaskID+"/CaseInsensitiveHeadingsValid", func(t *testing.T) {
			task := newClaimedTask(t, nil)
			varied := fixture.DiagramReportBody
			varied = strings.Replace(varied, "## Review summary", "## REVIEW SUMMARY", 1)
			varied = strings.Replace(varied, "## UML class diagram", "## Uml Class Diagram", 1)
			varied = strings.Replace(varied, "## Sequence diagram", "## sequence diagram", 1)
			varied = strings.Replace(varied, "## Findings", "## findings", 1)
			varied = strings.Replace(varied, "## Coverage and limitations", "## COVERAGE AND LIMITATIONS", 1)

			report, err := s.SubmitReport(ctx, project.ID, worker.Token, task.ID, task.Attempt, "commit:baseline", varied)
			if err != nil {
				t.Fatalf("case-varied valid headings rejected: %v", err)
			}
			if err := s.AcceptReport(ctx, project.ID, owner.Token, report.ID, report.Digest); err != nil {
				t.Fatalf("AcceptReport failed: %v", err)
			}
		})
	}
}

// replaceSectionContent replaces the content under markdown heading with newContent.
func replaceSectionContent(markdown, heading, newContent string) string {
	lines := strings.Split(markdown, "\n")
	var out []string
	inTarget := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			title := strings.TrimSpace(trimmed[3:])
			if strings.EqualFold(title, heading) {
				inTarget = true
				out = append(out, line, newContent)
				continue
			} else if inTarget {
				inTarget = false
			}
		}
		if !inTarget {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// TestAdversarial_ConcurrentBenchmarkComparativeReviewTracks stresses BenchmarkComparativeReviewTracks
// under concurrent execution across 20 goroutines, verifying determinism, thread safety, and race freedom.
func TestAdversarial_ConcurrentBenchmarkComparativeReviewTracks(t *testing.T) {
	pricing := benchmarks.DefaultPricingModel()
	const numGoroutines = 20
	const iterationsPerGoroutine = 5

	var wg sync.WaitGroup
	errCh := make(chan error, numGoroutines)

	// Capture golden reference result from single-threaded run
	baselineResults := benchmarks.RunAllBenchmarks(pricing)
	if len(baselineResults) != 3 {
		t.Fatalf("expected 3 baseline results, got %d", len(baselineResults))
	}

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for it := 0; it < iterationsPerGoroutine; it++ {
				results := benchmarks.RunAllBenchmarks(pricing)
				if len(results) != len(baselineResults) {
					errCh <- fmt.Errorf("goroutine %d: got %d results, want %d", gid, len(results), len(baselineResults))
					return
				}
				for i, res := range results {
					base := baselineResults[i]
					if res.TaskID != base.TaskID {
						errCh <- fmt.Errorf("goroutine %d: task ID mismatch %s vs %s", gid, res.TaskID, base.TaskID)
						return
					}
					if res.FullCode.PromptTokens != base.FullCode.PromptTokens ||
						res.DiagramReport.PromptTokens != base.DiagramReport.PromptTokens {
						errCh <- fmt.Errorf("goroutine %d: prompt token mismatch on %s", gid, res.TaskID)
						return
					}
					if res.FullCode.EstimatedCostMicros != base.FullCode.EstimatedCostMicros ||
						res.DiagramReport.EstimatedCostMicros != base.DiagramReport.EstimatedCostMicros {
						errCh <- fmt.Errorf("goroutine %d: cost micros mismatch on %s", gid, res.TaskID)
						return
					}
					if res.FullCode.QualityRubric.DefectDetectionScore != base.FullCode.QualityRubric.DefectDetectionScore ||
						res.DiagramReport.QualityRubric.DefectDetectionScore != base.DiagramReport.QualityRubric.DefectDetectionScore {
						errCh <- fmt.Errorf("goroutine %d: quality defect score mismatch on %s", gid, res.TaskID)
						return
					}
					if res.TruthfulnessNotice != benchmarks.TruthfulnessNotice {
						errCh <- fmt.Errorf("goroutine %d: missing truthfulness notice", gid)
						return
					}
				}
			}
		}(g)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

// TestAdversarial_ConcurrentSetWorkRoleOptimisticRevision stresses SetWorkRole with concurrent goroutines
// attempting simultaneous role assignments to verify that optimistic revision checking strictly enforces
// mutual exclusion and eliminates lost updates.
func TestAdversarial_ConcurrentSetWorkRoleOptimisticRevision(t *testing.T) {
	s, _, project, owner, workers := setupM3ChallengerTest(t)
	worker := workers[0]
	ctx := context.Background()

	// Initial State: Worker has no assigned work role. Revision in DB is conceptually 0.

	// Phase 1: 16 concurrent goroutines race to assign initial role with revision=0.
	// EXACTLY ONE must succeed and produce revision=1. All others must receive ErrConflict.
	const racers = 16
	var wg sync.WaitGroup
	var successCount int32
	var conflictCount int32
	var otherErrCount int32

	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			roleName := "researcher"
			if idx%2 == 1 {
				roleName = "coordinator"
			}
			_, err := s.SetWorkRole(ctx, project.ID, owner.Token, worker.PrincipalID, "", roleName, "", 0, 0)
			if err == nil {
				atomic.AddInt32(&successCount, 1)
			} else if errors.Is(err, coord.ErrConflict) {
				atomic.AddInt32(&conflictCount, 1)
			} else {
				atomic.AddInt32(&otherErrCount, 1)
			}
		}(i)
	}
	wg.Wait()

	if successCount != 1 {
		t.Fatalf("Phase 1: expected exactly 1 successful SetWorkRole, got %d (conflicts=%d, otherErrors=%d)",
			successCount, conflictCount, otherErrCount)
	}
	if conflictCount != racers-1 {
		t.Fatalf("Phase 1: expected %d conflicts, got %d", racers-1, conflictCount)
	}

	// Verify current role in store has revision=1
	roles, err := s.ListWorkRoles(ctx, project.ID, owner.Token)
	if err != nil {
		t.Fatalf("ListWorkRoles: %v", err)
	}
	if len(roles) != 1 {
		t.Fatalf("expected 1 work role, got %d", len(roles))
	}
	if roles[0].Revision != 1 {
		t.Fatalf("expected role revision 1, got %d", roles[0].Revision)
	}

	// Phase 2: Stale and Future revision rejections
	// Stale revision (0) must fail with ErrConflict
	_, err = s.SetWorkRole(ctx, project.ID, owner.Token, worker.PrincipalID, "", "reviewer", "", 0, 0)
	if !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("stale revision 0 returned error: %v, want ErrConflict", err)
	}

	// Future revision (5) must fail with ErrConflict
	_, err = s.SetWorkRole(ctx, project.ID, owner.Token, worker.PrincipalID, "", "reviewer", "", 0, 5)
	if !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("future revision 5 returned error: %v, want ErrConflict", err)
	}

	// Phase 3: 16 concurrent goroutines race to update from revision=1 to revision=2.
	// EXACTLY ONE must succeed and produce revision=2. All others must receive ErrConflict.
	successCount = 0
	conflictCount = 0
	otherErrCount = 0

	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			roleName := "implementer"
			if idx%2 == 1 {
				roleName = "reviewer"
			}
			_, err := s.SetWorkRole(ctx, project.ID, owner.Token, worker.PrincipalID, "", roleName, "", 0, 1)
			if err == nil {
				atomic.AddInt32(&successCount, 1)
			} else if errors.Is(err, coord.ErrConflict) {
				atomic.AddInt32(&conflictCount, 1)
			} else {
				atomic.AddInt32(&otherErrCount, 1)
			}
		}(i)
	}
	wg.Wait()

	if successCount != 1 {
		t.Fatalf("Phase 3: expected exactly 1 successful SetWorkRole, got %d (conflicts=%d, otherErrors=%d)",
			successCount, conflictCount, otherErrCount)
	}
	if conflictCount != racers-1 {
		t.Fatalf("Phase 3: expected %d conflicts, got %d", racers-1, conflictCount)
	}

	// Verify revision in store is now 2
	roles, err = s.ListWorkRoles(ctx, project.ID, owner.Token)
	if err != nil {
		t.Fatalf("ListWorkRoles: %v", err)
	}
	if len(roles) != 1 || roles[0].Revision != 2 {
		t.Fatalf("expected role revision 2, got %+v", roles)
	}

	// Phase 4: Sequential monotonic updates
	updated, err := s.SetWorkRole(ctx, project.ID, owner.Token, worker.PrincipalID, "", "verifier", "", 0, 2)
	if err != nil {
		t.Fatalf("valid update to revision 3 failed: %v", err)
	}
	if updated.Revision != 3 {
		t.Fatalf("updated revision = %d, want 3", updated.Revision)
	}
}

// TestAdversarial_TruthfulnessAudit verifies that:
// 1. Token and cost metrics are strictly labeled as internal estimates or provider-reported values.
// 2. Truthfulness notices are explicitly attached.
// 3. No synthetic external provider billing or fake charges are recorded as verified spend.
// 4. Budget reporting strictly segregates sources without claiming billing settlement.
func TestAdversarial_TruthfulnessAudit(t *testing.T) {
	s, _, project, owner, workers := setupM3ChallengerTest(t)
	worker := workers[0]
	ctx := context.Background()

	// 1. Truthfulness notice content audit
	const expectedNoticeSubstring = "internal estimates or provider-reported values"
	if !strings.Contains(benchmarks.TruthfulnessNotice, expectedNoticeSubstring) {
		t.Fatalf("benchmarks.TruthfulnessNotice does not contain %q: %s", expectedNoticeSubstring, benchmarks.TruthfulnessNotice)
	}

	// 2. Benchmark runs and comparative results must carry the notice
	pricing := benchmarks.DefaultPricingModel()
	results := benchmarks.RunAllBenchmarks(pricing)
	for _, res := range results {
		if res.TruthfulnessNotice != benchmarks.TruthfulnessNotice {
			t.Errorf("[%s] comparative result missing exact TruthfulnessNotice", res.TaskID)
		}
		if res.FullCode.TruthfulnessNotice != benchmarks.TruthfulnessNotice {
			t.Errorf("[%s] FullCode run missing exact TruthfulnessNotice", res.TaskID)
		}
		if res.DiagramReport.TruthfulnessNotice != benchmarks.TruthfulnessNotice {
			t.Errorf("[%s] DiagramReport run missing exact TruthfulnessNotice", res.TaskID)
		}
		if res.FullCode.CostSource != benchmarks.CostSourceEstimated {
			t.Errorf("[%s] FullCode cost source = %s, want %s", res.TaskID, res.FullCode.CostSource, benchmarks.CostSourceEstimated)
		}
		if res.DiagramReport.CostSource != benchmarks.CostSourceEstimated {
			t.Errorf("[%s] DiagramReport cost source = %s, want %s", res.TaskID, res.DiagramReport.CostSource, benchmarks.CostSourceEstimated)
		}
	}

	// 3. Verification of deterministic arithmetic pricing (no external network billing claims)
	// Price: $2.50 / 1M prompt, $10.00 / 1M completion
	calcCost := benchmarks.EstimateCostMicros(1_000_000, 1_000_000, pricing)
	expectedTotalMicros := int64(2_500_000 + 10_000_000)
	if calcCost != expectedTotalMicros {
		t.Fatalf("EstimateCostMicros(1M, 1M) = %d, want %d", calcCost, expectedTotalMicros)
	}

	// 4. Usage reporting in coordinator store audit
	task, err := s.CreateTask(ctx, project.ID, owner.Token, 1, "Audit Task", "Desc", "review",
		coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	claimed, err := s.ClaimTask(ctx, project.ID, worker.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}

	// Reject invalid / negative token counts
	_, err = s.RecordAttemptUsage(ctx, project.ID, worker.Token, task.ID, claimed.Attempt, "usage-bad-tokens",
		coord.UsageReport{Provider: "TestProvider", Model: "test-model", Source: "estimated", InputTokens: -100})
	if err == nil {
		t.Fatal("expected negative input tokens to be rejected, got nil")
	}

	// Valid usage report labeled with "estimated"
	estUsage, err := s.RecordAttemptUsage(ctx, project.ID, worker.Token, task.ID, claimed.Attempt, "usage-est-1",
		coord.UsageReport{Provider: "TestProvider", Model: "test-model", Source: "estimated", InputTokens: 500, OutputTokens: 100, CostMicros: 2250, Currency: "USD"})
	if err != nil {
		t.Fatalf("valid estimated usage recording failed: %v", err)
	}
	if estUsage.Source != "estimated" {
		t.Fatalf("usage source = %s, want estimated", estUsage.Source)
	}

	// Check budget tracking reflects the source as internal tracking, not third-party billing settlement
	budget, err := s.GetBudget(ctx, project.ID, owner.Token)
	if err != nil {
		t.Fatalf("GetBudget: %v", err)
	}
	if budget.ReportedUsage.Sources["estimated"] != 1 {
		t.Fatalf("budget sources count for estimated = %d, want 1", budget.ReportedUsage.Sources["estimated"])
	}
}
