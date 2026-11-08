package benchmarks_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tasmirz/gossip/benchmarks"
	"github.com/tasmirz/gossip/pkg/coord"
)

func TestRunAllBenchmarks(t *testing.T) {
	pricing := benchmarks.DefaultPricingModel()
	results := benchmarks.RunAllBenchmarks(pricing)

	if len(results) != 3 {
		t.Fatalf("expected 3 benchmark results, got %d", len(results))
	}

	expectedTasks := map[string]string{
		"bugfix-lease-nil-deref":     "bugfix",
		"api-ext-auth-token-expiry":  "api_extension",
		"refactor-db-pool-isolation": "refactor",
	}

	for _, res := range results {
		kind, ok := expectedTasks[res.TaskID]
		if !ok {
			t.Errorf("unexpected task ID in results: %s", res.TaskID)
		}
		_ = kind

		// 1. Truthfulness and Provenance Checks
		if res.FullCode.CostSource != benchmarks.CostSourceEstimated {
			t.Errorf("[%s] full code cost source = %s, want %s", res.TaskID, res.FullCode.CostSource, benchmarks.CostSourceEstimated)
		}
		if res.DiagramReport.CostSource != benchmarks.CostSourceEstimated {
			t.Errorf("[%s] diagram report cost source = %s, want %s", res.TaskID, res.DiagramReport.CostSource, benchmarks.CostSourceEstimated)
		}
		if !strings.Contains(res.TruthfulnessNotice, "internal estimates") {
			t.Errorf("[%s] missing truthfulness notice: %s", res.TaskID, res.TruthfulnessNotice)
		}

		// 2. Token and Cost Non-negativity Checks
		if res.FullCode.PromptTokens <= 0 || res.FullCode.CompletionTokens <= 0 {
			t.Errorf("[%s] full code tokens non-positive: prompt=%d completion=%d", res.TaskID, res.FullCode.PromptTokens, res.FullCode.CompletionTokens)
		}
		if res.DiagramReport.PromptTokens <= 0 || res.DiagramReport.CompletionTokens <= 0 {
			t.Errorf("[%s] diagram report tokens non-positive: prompt=%d completion=%d", res.TaskID, res.DiagramReport.PromptTokens, res.DiagramReport.CompletionTokens)
		}
		if res.FullCode.EstimatedCostMicros <= 0 || res.DiagramReport.EstimatedCostMicros <= 0 {
			t.Errorf("[%s] estimated costs non-positive: full=%d diag=%d", res.TaskID, res.FullCode.EstimatedCostMicros, res.DiagramReport.EstimatedCostMicros)
		}

		// 3. Comparative Token Reduction Checks
		// In all representative tasks, the diagram report review prompt is more compact than full raw source diffs
		if res.PromptTokenReductionPct <= 0 {
			t.Errorf("[%s] expected positive prompt token reduction, got %.2f%%", res.TaskID, res.PromptTokenReductionPct)
		}
		if res.CostReductionPct <= 0 {
			t.Errorf("[%s] expected positive cost reduction, got %.2f%%", res.TaskID, res.CostReductionPct)
		}

		// 4. Quality Rubric Checks (Ground Truth Verification)
		if res.FullCode.QualityRubric.DefectDetectionScore < 100.0 {
			t.Errorf("[%s] full code defect detection score = %.1f, want 100.0", res.TaskID, res.FullCode.QualityRubric.DefectDetectionScore)
		}
		if res.DiagramReport.QualityRubric.DefectDetectionScore < 100.0 {
			t.Errorf("[%s] diagram report defect detection score = %.1f, want 100.0", res.TaskID, res.DiagramReport.QualityRubric.DefectDetectionScore)
		}
		if res.FullCode.QualityRubric.CoverageScore < 100.0 {
			t.Errorf("[%s] full code coverage score = %.1f, want 100.0", res.TaskID, res.FullCode.QualityRubric.CoverageScore)
		}
		if res.DiagramReport.QualityRubric.CoverageScore < 100.0 {
			t.Errorf("[%s] diagram report coverage score = %.1f, want 100.0", res.TaskID, res.DiagramReport.QualityRubric.CoverageScore)
		}
		if res.FullCode.QualityRubric.Precision < 100.0 || res.DiagramReport.QualityRubric.Precision < 100.0 {
			t.Errorf("[%s] precision below 100: full=%.1f diag=%.1f", res.TaskID, res.FullCode.QualityRubric.Precision, res.DiagramReport.QualityRubric.Precision)
		}
		if res.FullCode.QualityRubric.FalsePositives != 0 || res.DiagramReport.QualityRubric.FalsePositives != 0 {
			t.Errorf("[%s] expected 0 false positives, got full=%d diag=%d", res.TaskID, res.FullCode.QualityRubric.FalsePositives, res.DiagramReport.QualityRubric.FalsePositives)
		}

		// 5. Latency Checks
		if res.WallTimeDeltaMs >= 0 {
			t.Errorf("[%s] expected diagram report review to have lower latency than full code: delta=%d ms", res.TaskID, res.WallTimeDeltaMs)
		}
	}
}

func TestFormatComparisonOutputs(t *testing.T) {
	pricing := benchmarks.DefaultPricingModel()
	results := benchmarks.RunAllBenchmarks(pricing)

	md := benchmarks.FormatComparisonMarkdown(results)
	if !strings.Contains(md, "Gossip Review Deliverable Comparative Benchmark Results") {
		t.Fatal("missing markdown title")
	}
	if !strings.Contains(md, benchmarks.TruthfulnessNotice) {
		t.Fatal("missing truthfulness notice in markdown")
	}
	for _, r := range results {
		if !strings.Contains(md, r.TaskID) {
			t.Fatalf("markdown table missing task %s", r.TaskID)
		}
	}

	jsonStr, err := benchmarks.FormatComparisonJSON(results)
	if err != nil {
		t.Fatalf("JSON format error: %v", err)
	}
	var unmarshaled []benchmarks.ComparativeBenchmarkResult
	if err := json.Unmarshal([]byte(jsonStr), &unmarshaled); err != nil {
		t.Fatalf("unmarshal JSON output: %v", err)
	}
	if len(unmarshaled) != len(results) {
		t.Fatalf("JSON roundtrip count = %d, want %d", len(unmarshaled), len(results))
	}
}

func TestDiagramReportFixturesPassGossipStoreValidation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "benchmarks_coord.db")
	store, err := coord.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	project, owner, err := store.CreateProject(ctx, "benchmark-validation", "Owner")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	worker, err := store.AddPrincipal(ctx, project.ID, owner.Token, "worker", "worker")
	if err != nil {
		t.Fatalf("AddPrincipal: %v", err)
	}

	if _, err := store.PublishPlan(ctx, project.ID, owner.Token, "Benchmark validation plan", 0); err != nil {
		t.Fatalf("PublishPlan: %v", err)
	}
	if err := store.AcknowledgePlan(ctx, project.ID, worker.Token, 1); err != nil {
		t.Fatalf("AcknowledgePlan: %v", err)
	}

	fixtures := benchmarks.GetRepresentativeTaskFixtures()
	for _, f := range fixtures {
		contract := coord.ReportContract{
			ReviewType: "diagram_report",
			Formats:    []string{"markdown", "mermaid_sequence", "mermaid_class"},
			Coverage:   f.PlannedCoverage,
		}

		task, err := store.CreateTask(ctx, project.ID, owner.Token, 1, f.Title, "Review task", "review", contract, nil)
		if err != nil {
			t.Fatalf("[%s] CreateTask: %v", f.TaskID, err)
		}

		claimed, err := store.ClaimTask(ctx, project.ID, worker.Token, task.ID, time.Minute)
		if err != nil {
			t.Fatalf("[%s] ClaimTask: %v", f.TaskID, err)
		}

		// Submit the fixture diagram report body to Gossip's real store
		report, err := store.SubmitReport(ctx, project.ID, worker.Token, claimed.ID, claimed.Attempt, "commit:baseline", f.DiagramReportBody)
		if err != nil {
			t.Fatalf("[%s] SubmitReport failed structural validation: %v", f.TaskID, err)
		}

		if report.TaskID != task.ID || report.Attempt != claimed.Attempt {
			t.Fatalf("[%s] submitted report mismatch: %+v", f.TaskID, report)
		}

		// Verify owner accepts the valid diagram report
		if err = store.AcceptReport(ctx, project.ID, owner.Token, report.ID, report.Digest); err != nil {
			t.Fatalf("[%s] AcceptReport: %v", f.TaskID, err)
		}
		acceptedTask, err := store.GetTask(ctx, project.ID, owner.Token, task.ID)
		if err != nil {
			t.Fatalf("[%s] GetTask: %v", f.TaskID, err)
		}
		if acceptedTask.Status != "accepted" {
			t.Fatalf("[%s] task status = %s, want accepted", f.TaskID, acceptedTask.Status)
		}
	}
}

func TestEstimateTokens(t *testing.T) {
	if benchmarks.EstimateTokens("") != 0 {
		t.Errorf("empty text token count should be 0")
	}

	short := "Hello world"
	longer := "Hello world, this is an extended technical description of a coordination protocol."
	if benchmarks.EstimateTokens(longer) <= benchmarks.EstimateTokens(short) {
		t.Errorf("longer text should have higher token count: %d vs %d", benchmarks.EstimateTokens(longer), benchmarks.EstimateTokens(short))
	}
}

func TestEstimateCostMicros(t *testing.T) {
	pricing := benchmarks.PricingModel{
		PromptPricePerMillionMicros:     2_000_000, // $2.00 / 1M
		CompletionPricePerMillionMicros: 8_000_000, // $8.00 / 1M
	}

	// 1,000 prompt tokens = 2,000 micros ($0.002)
	// 500 completion tokens = 4,000 micros ($0.004)
	cost := benchmarks.EstimateCostMicros(1000, 500, pricing)
	expected := int64(6000)
	if cost != expected {
		t.Errorf("EstimateCostMicros(1000, 500) = %d micros, want %d", cost, expected)
	}
}

func BenchmarkComparativeReviewTracks(b *testing.B) {
	pricing := benchmarks.DefaultPricingModel()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = benchmarks.RunAllBenchmarks(pricing)
	}
}
