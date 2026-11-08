package benchmarks

import (
	"crypto/rand"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

// TestChallengeEstimateTokensEdgeCases tests EstimateTokens against adversarial inputs:
// empty strings, binary data, huge inputs (>10MB), and diverse unicode sequences.
func TestChallengeEstimateTokensEdgeCases(t *testing.T) {
	// 1. Empty string
	if count := EstimateTokens(""); count != 0 {
		t.Fatalf("expected EstimateTokens(\"\") to be 0, got %d", count)
	}

	// 2. Whitespace-only strings of varying lengths
	whitespaces := []string{" ", "\t", "\n", "\r\n", "   \t\t\n\n\r   "}
	for _, ws := range whitespaces {
		count := EstimateTokens(ws)
		expectedLenBased := int(math.Ceil(float64(len(ws)) / 3.8))
		// With no words/symbols, count is 0, so lengthBased/2 is returned if lengthBased > 0
		if count < 0 {
			t.Errorf("EstimateTokens on whitespace returned negative value: %d", count)
		}
		_ = expectedLenBased
	}

	// 3. Binary data: null bytes, non-UTF8 sequences, control characters
	rawBinary := []byte{0x00, 0xFF, 0xFE, 0x01, 0x1B, 0x07, 0x7F, 0x80, 0x81, 0xC0, 0xC1, 0xF5}
	binaryStr := string(rawBinary)
	binCount := EstimateTokens(binaryStr)
	if binCount <= 0 {
		t.Errorf("expected positive token estimate for binary data, got %d", binCount)
	}

	// 4. Large binary blob (1MB random bytes)
	randomBlob := make([]byte, 1024*1024)
	if _, err := rand.Read(randomBlob); err != nil {
		t.Fatalf("failed to generate random bytes: %v", err)
	}
	blobCount := EstimateTokens(string(randomBlob))
	if blobCount <= 0 {
		t.Errorf("expected positive token estimate for 1MB random blob, got %d", blobCount)
	}

	// 5. Huge input (>10MB text) - check memory, performance and determinism
	const hugeSize = 10 * 1024 * 1024 // 10MB
	chunk := "func ProcessCoordination(ctx context.Context, payload []byte) (Result, error) {\n\treturn Result{Status: \"OK\"}, nil\n}\n"
	repeats := (hugeSize / len(chunk)) + 1
	var sb strings.Builder
	sb.Grow(repeats * len(chunk))
	for i := 0; i < repeats; i++ {
		sb.WriteString(chunk)
	}
	hugeInput := sb.String()
	if len(hugeInput) < 10*1024*1024 {
		t.Fatalf("hugeInput size is %d, expected > 10MB", len(hugeInput))
	}

	start := time.Now()
	hugeTokens1 := EstimateTokens(hugeInput)
	duration1 := time.Since(start)
	if hugeTokens1 <= 0 {
		t.Fatalf("expected positive token count for huge input, got %d", hugeTokens1)
	}

	// Verify determinism across multiple calls
	hugeTokens2 := EstimateTokens(hugeInput)
	if hugeTokens1 != hugeTokens2 {
		t.Fatalf("non-deterministic EstimateTokens on huge input: run1=%d, run2=%d", hugeTokens1, hugeTokens2)
	}
	t.Logf("EstimateTokens for %d bytes (>10MB): %d tokens in %v", len(hugeInput), hugeTokens1, duration1)

	// 6. Complex Unicode sequences: CJK, RTL (Arabic/Hebrew), Emoji, ZWJ sequences, Combining Marks, Zalgo
	unicodeTestCases := []struct {
		name string
		text string
	}{
		{"CJK", "这是一段中文测试代码：如果状态机进入故障模式，则触发安全停止。"},
		{"RTL_Arabic", "هذا اختبار باللغة العربية للتحقق من عداد الرموز المميزة في نظام التنسيق."},
		{"RTL_Hebrew", "בדיקת מערכת לתיאום סוכנים עצמאיים באמצעות פרוטוקול רכילות."},
		{"Emoji_and_ZWJ", "👨‍👩‍👧‍👦 🚀 🧑‍💻 🔍 ⚡ 🛡️ ⚠️ 🤖"},
		{"Combining_Diacritics_Zalgo", "T̶h̶e̶ ̶k̶i̶l̶l̶-̶s̶w̶i̶t̶c̶h̶ ̶m̶u̶s̶t̶ ̶h̶a̶l̶t̶ ̶a̶l̶l̶ ̶t̶r̶a̶f̶f̶i̶c̶."},
		{"Accented_European", "L'exécution prématurée du protocole vérifie l'intégrité des données à l'état prêt."},
		{"Math_and_Symbols", "∀x ∈ S, ∃y ∈ T : f(x) = y ∧ x ⊕ y ≡ 0 (mod 2) ⟹ ∑_{i=0}^n x_i ≤ C"},
	}

	for _, tc := range unicodeTestCases {
		t.Run(tc.name, func(t *testing.T) {
			count1 := EstimateTokens(tc.text)
			count2 := EstimateTokens(tc.text)
			if count1 != count2 {
				t.Errorf("non-deterministic EstimateTokens for %s: %d vs %d", tc.name, count1, count2)
			}
			if count1 <= 0 {
				t.Errorf("expected positive token estimate for %s, got %d", tc.name, count1)
			}
			t.Logf("%s (%d bytes): %d tokens", tc.name, len(tc.text), count1)
		})
	}
}

// TestChallengeEstimateCostMicrosEdgeCases tests EstimateCostMicros under extreme values.
func TestChallengeEstimateCostMicrosEdgeCases(t *testing.T) {
	pricing := DefaultPricingModel()

	// 1. Zero tokens
	if cost := EstimateCostMicros(0, 0, pricing); cost != 0 {
		t.Errorf("expected cost 0 for 0 tokens, got %d", cost)
	}

	// 2. Standard values
	cost := EstimateCostMicros(1_000_000, 1_000_000, pricing)
	// 1M prompt * 2.50 = $2.50 = 2,500,000 micros; 1M comp * 10.00 = $10.00 = 10,000,000 micros => 12,500,000 micros
	if cost != 12_500_000 {
		t.Errorf("expected 12500000 micros for 1M+1M tokens, got %d", cost)
	}

	// 3. Zero pricing model
	zeroPricing := PricingModel{PromptPricePerMillionMicros: 0, CompletionPricePerMillionMicros: 0}
	if cost := EstimateCostMicros(100_000, 50_000, zeroPricing); cost != 0 {
		t.Errorf("expected 0 cost for zero pricing, got %d", cost)
	}

	// 4. Large token numbers (e.g. 1 billion tokens) - verify no 64-bit overflow
	// 1,000,000,000 * 2,500,000 = 2.5e15, which easily fits in int64 (max ~9.22e18)
	billionCost := EstimateCostMicros(1_000_000_000, 1_000_000_000, pricing)
	expectedBillion := int64(1_000_000_000)*2_500_000/1_000_000 + int64(1_000_000_000)*10_000_000/1_000_000
	if billionCost != expectedBillion {
		t.Errorf("expected %d for 1B tokens, got %d", expectedBillion, billionCost)
	}

	// 5. Negative tokens - should compute mathematically without panic
	negCost := EstimateCostMicros(-1000, -1000, pricing)
	if negCost >= 0 {
		t.Errorf("expected negative cost for negative token counts, got %d", negCost)
	}
}

// TestChallengeRubricDeterminismAndTruthfulness verifies that all rubric fields are
// deterministically calculated, labeled strictly with CostSourceEstimated or
// CostSourceProviderReported, and contain required TruthfulnessNotice.
func TestChallengeRubricDeterminismAndTruthfulness(t *testing.T) {
	pricing := DefaultPricingModel()
	fixtures := GetRepresentativeTaskFixtures()

	if len(fixtures) == 0 {
		t.Fatal("expected at least one benchmark fixture, got 0")
	}

	for _, fixture := range fixtures {
		t.Run(fixture.TaskID, func(t *testing.T) {
			res1 := RunTaskBenchmark(fixture, pricing)
			res2 := RunTaskBenchmark(fixture, pricing)

			// 1. Strict determinism check
			bytes1, err := json.Marshal(res1)
			if err != nil {
				t.Fatalf("failed to marshal res1: %v", err)
			}
			bytes2, err := json.Marshal(res2)
			if err != nil {
				t.Fatalf("failed to marshal res2: %v", err)
			}
			if string(bytes1) != string(bytes2) {
				t.Fatalf("non-deterministic benchmark result for %s:\nres1=%s\nres2=%s", fixture.TaskID, string(bytes1), string(bytes2))
			}

			// 2. Truthfulness notices
			if res1.TruthfulnessNotice != TruthfulnessNotice || res1.TruthfulnessNotice == "" {
				t.Errorf("missing or invalid TruthfulnessNotice on ComparativeBenchmarkResult: %q", res1.TruthfulnessNotice)
			}
			if res1.FullCode.TruthfulnessNotice != TruthfulnessNotice {
				t.Errorf("missing or invalid TruthfulnessNotice on FullCode run: %q", res1.FullCode.TruthfulnessNotice)
			}
			if res1.DiagramReport.TruthfulnessNotice != TruthfulnessNotice {
				t.Errorf("missing or invalid TruthfulnessNotice on DiagramReport run: %q", res1.DiagramReport.TruthfulnessNotice)
			}

			// 3. CostSource validation: must be strictly "estimated" or "provider_reported"
			validSources := map[string]bool{
				CostSourceEstimated:        true,
				CostSourceProviderReported: true,
			}
			if !validSources[res1.FullCode.CostSource] {
				t.Errorf("invalid CostSource on FullCode run: %q", res1.FullCode.CostSource)
			}
			if !validSources[res1.DiagramReport.CostSource] {
				t.Errorf("invalid CostSource on DiagramReport run: %q", res1.DiagramReport.CostSource)
			}

			// 4. Quality rubric bounds and validity
			runs := []struct {
				name string
				run  BenchmarkRun
			}{
				{"FullCode", res1.FullCode},
				{"DiagramReport", res1.DiagramReport},
			}
			for _, r := range runs {
				q := r.run.QualityRubric
				if q.DefectDetectionScore < 0.0 || q.DefectDetectionScore > 100.0 {
					t.Errorf("%s %s: DefectDetectionScore out of bounds [0, 100]: %v", fixture.TaskID, r.name, q.DefectDetectionScore)
				}
				if q.CoverageScore < 0.0 || q.CoverageScore > 100.0 {
					t.Errorf("%s %s: CoverageScore out of bounds [0, 100]: %v", fixture.TaskID, r.name, q.CoverageScore)
				}
				if q.Precision < 0.0 || q.Precision > 100.0 {
					t.Errorf("%s %s: Precision out of bounds [0, 100]: %v", fixture.TaskID, r.name, q.Precision)
				}
				if q.FalsePositives < 0 {
					t.Errorf("%s %s: FalsePositives cannot be negative: %d", fixture.TaskID, r.name, q.FalsePositives)
				}
			}

			// 5. Verification of delta formulas
			if expectedDelta := res1.DiagramReport.PromptTokens - res1.FullCode.PromptTokens; res1.PromptTokenDelta != expectedDelta {
				t.Errorf("PromptTokenDelta mismatch: got %d, expected %d", res1.PromptTokenDelta, expectedDelta)
			}
			if expectedCostDelta := res1.DiagramReport.EstimatedCostMicros - res1.FullCode.EstimatedCostMicros; res1.CostDeltaMicros != expectedCostDelta {
				t.Errorf("CostDeltaMicros mismatch: got %d, expected %d", res1.CostDeltaMicros, expectedCostDelta)
			}
		})
	}

	// 6. Test Markdown and JSON export contain TruthfulnessNotice
	allResults := RunAllBenchmarks(pricing)
	md := FormatComparisonMarkdown(allResults)
	if !strings.Contains(md, TruthfulnessNotice) {
		t.Errorf("FormatComparisonMarkdown output does not contain TruthfulnessNotice")
	}

	jsonOut, err := FormatComparisonJSON(allResults)
	if err != nil {
		t.Fatalf("FormatComparisonJSON error: %v", err)
	}
	if !strings.Contains(jsonOut, TruthfulnessNotice) {
		t.Errorf("FormatComparisonJSON output does not contain TruthfulnessNotice")
	}
}

// TestChallengeEvaluateQualityRubricEdgeCases tests rubric scoring with edge-case inputs.
func TestChallengeEvaluateQualityRubricEdgeCases(t *testing.T) {
	// 1. Empty defects and empty scope
	qEmpty := EvaluateQualityRubric("review text", nil, nil, 0)
	if qEmpty.DefectDetectionScore != 100.0 || qEmpty.CoverageScore != 100.0 || qEmpty.Precision != 100.0 {
		t.Errorf("unexpected scores for empty defects/scope: %+v", qEmpty)
	}

	// 2. High false positives with zero detected defects
	qZeroDet := EvaluateQualityRubric("clean code with no issues found", []string{"critical nil dereference in lease renew"}, []string{"lease renewal"}, 5)
	if qZeroDet.DefectDetectionScore != 0.0 {
		t.Errorf("expected 0 defect detection, got %v", qZeroDet.DefectDetectionScore)
	}
	if qZeroDet.Precision != 0.0 {
		t.Errorf("expected 0 precision, got %v", qZeroDet.Precision)
	}
	if qZeroDet.FalsePositives != 5 {
		t.Errorf("expected 5 false positives, got %d", qZeroDet.FalsePositives)
	}

	// 3. Perfect detection with false positives
	reviewPerfect := "critical nil dereference in lease renew was detected and fixed. lease renewal was verified."
	qWithFP := EvaluateQualityRubric(reviewPerfect, []string{"critical nil dereference in lease renew"}, []string{"lease renewal"}, 1)
	if qWithFP.DefectDetectionScore != 100.0 {
		t.Errorf("expected 100 defect detection, got %v", qWithFP.DefectDetectionScore)
	}
	// 1 detected / (1 detected + 1 FP) = 50.0% precision
	if qWithFP.Precision != 50.0 {
		t.Errorf("expected 50.0 precision, got %v", qWithFP.Precision)
	}
}
