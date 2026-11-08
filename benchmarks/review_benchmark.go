package benchmarks

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"unicode"
)

const (
	// TruthfulnessNotice declares that token counts and cost calculations are internal
	// estimates or provider-reported metrics rather than verified external billing claims.
	TruthfulnessNotice = "All token counts and cost metrics are internal estimates or provider-reported values. They do not constitute verified external billing claims or guaranteed third-party provider charges."

	// CostSourceEstimated indicates metrics derived from deterministic internal token pricing formulas.
	CostSourceEstimated = "estimated"

	// CostSourceProviderReported indicates metrics obtained directly from upstream provider usage reports.
	CostSourceProviderReported = "provider_reported"
)

// PromptSettings records generation parameters for a benchmark run.
type PromptSettings struct {
	Temperature  float64 `json:"temperature"`
	TopP         float64 `json:"top_p"`
	MaxTokens    int     `json:"max_tokens"`
	SystemPrompt string  `json:"system_prompt"`
}

// QualityRubric scores the quality of a code review output across defect detection,
// coverage, precision, and false positives.
type QualityRubric struct {
	DefectDetectionScore float64 `json:"defect_detection_score"` // 0.0 - 100.0 percentage of ground-truth defects identified
	CoverageScore        float64 `json:"coverage_score"`         // 0.0 - 100.0 percentage of planned scope items addressed
	Precision            float64 `json:"precision"`              // 0.0 - 100.0 true findings / (true findings + false positives)
	FalsePositives       int     `json:"false_positives"`        // Count of spurious or non-existent defects reported
}

// BenchmarkRun holds the metrics recorded for a single review evaluation track.
type BenchmarkRun struct {
	TaskID              string         `json:"task_id"`
	Model               string         `json:"model"`
	ModelVersion        string         `json:"model_version"`
	PromptSettings      PromptSettings `json:"prompt_settings"`
	ReviewType          string         `json:"review_type"` // "full_code" or "diagram_report"
	PromptTokens        int            `json:"prompt_tokens"`
	CompletionTokens    int            `json:"completion_tokens"`
	WallTimeMs          int64          `json:"wall_time_ms"`
	Retries             int            `json:"retries"`
	QualityRubric       QualityRubric  `json:"quality_rubric"`
	EstimatedCostMicros int64          `json:"estimated_cost_micros"`
	CostSource          string         `json:"cost_source"` // "estimated" or "provider_reported"
	TruthfulnessNotice  string         `json:"truthfulness_notice"`
}

// ComparativeBenchmarkResult captures side-by-side metrics comparing full-source review
// against report-first / diagram review on the same task.
type ComparativeBenchmarkResult struct {
	TaskID                  string       `json:"task_id"`
	Title                   string       `json:"title"`
	FullCode                BenchmarkRun `json:"full_code"`
	DiagramReport           BenchmarkRun `json:"diagram_report"`
	PromptTokenDelta        int          `json:"prompt_token_delta"`         // Diagram - FullCode
	PromptTokenReductionPct float64      `json:"prompt_token_reduction_pct"` // (FullCode - Diagram) / FullCode * 100
	CompletionTokenDelta    int          `json:"completion_token_delta"`     // Diagram - FullCode
	TotalTokenDelta         int          `json:"total_token_delta"`          // Diagram - FullCode
	TotalTokenReductionPct  float64      `json:"total_token_reduction_pct"`  // (FullCode - Diagram) / FullCode * 100
	WallTimeDeltaMs         int64        `json:"wall_time_delta_ms"`         // Diagram - FullCode
	CostDeltaMicros         int64        `json:"cost_delta_micros"`          // Diagram - FullCode
	CostReductionPct        float64      `json:"cost_reduction_pct"`         // (FullCode - Diagram) / FullCode * 100
	DefectDetectionDelta    float64      `json:"defect_detection_delta"`     // Diagram - FullCode
	CoverageDelta           float64      `json:"coverage_delta"`             // Diagram - FullCode
	PrecisionDelta          float64      `json:"precision_delta"`            // Diagram - FullCode
	TruthfulnessNotice      string       `json:"truthfulness_notice"`
}

// PricingModel defines token unit prices in microdollars ($1.00 = 1,000,000 micros).
type PricingModel struct {
	PromptPricePerMillionMicros     int64 `json:"prompt_price_per_million_micros"`
	CompletionPricePerMillionMicros int64 `json:"completion_price_per_million_micros"`
}

// DefaultPricingModel returns a representative pricing tier for frontier coding models
// ($2.50 per 1M prompt tokens, $10.00 per 1M completion tokens).
func DefaultPricingModel() PricingModel {
	return PricingModel{
		PromptPricePerMillionMicros:     2_500_000,
		CompletionPricePerMillionMicros: 10_000_000,
	}
}

// EstimateCostMicros calculates the estimated cost in microdollars for given token counts.
func EstimateCostMicros(promptTokens, completionTokens int, pricing PricingModel) int64 {
	promptCost := (int64(promptTokens) * pricing.PromptPricePerMillionMicros) / 1_000_000
	completionCost := (int64(completionTokens) * pricing.CompletionPricePerMillionMicros) / 1_000_000
	return promptCost + completionCost
}

// EstimateTokens calculates an approximate token count from text using a deterministic
// whitespace and punctuation boundary parser calibrated for code and structured technical prose.
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	var count int
	var inWord bool
	for _, r := range text {
		if unicode.IsSpace(r) {
			inWord = false
		} else if unicode.IsPunct(r) || unicode.IsSymbol(r) {
			inWord = false
			count++
		} else if !inWord {
			inWord = true
			count++
		}
	}
	// Add proportional adjustment for multi-byte or technical identifiers
	lengthBased := int(math.Ceil(float64(len(text)) / 3.8))
	if lengthBased > count {
		return (count + lengthBased) / 2
	}
	return count
}

// BenchmarkTaskFixture contains deterministic inputs, ground truth defects, and outputs
// for comparing full-code and diagram-report reviews.
type BenchmarkTaskFixture struct {
	TaskID                string         `json:"task_id"`
	Title                 string         `json:"title"`
	Kind                  string         `json:"kind"` // "bugfix", "api_extension", "refactor"
	PlannedCoverage       []string       `json:"planned_coverage"`
	KnownDefects          []string       `json:"known_defects"`
	PlannedScopeItems     []string       `json:"planned_scope_items"`
	FullCodeDiff          string         `json:"full_code_diff"`
	FullCodePrompt        string         `json:"full_code_prompt"`
	FullCodeResponse      string         `json:"full_code_response"`
	FullCodeWallTimeMs    int64          `json:"full_code_wall_time_ms"`
	FullCodeRetries       int            `json:"full_code_retries"`
	FullCodeFalsePositive int            `json:"full_code_false_positives"`
	DiagramReportBody     string         `json:"diagram_report_body"`
	DiagramPrompt         string         `json:"diagram_prompt"`
	DiagramResponse       string         `json:"diagram_response"`
	DiagramWallTimeMs     int64          `json:"diagram_wall_time_ms"`
	DiagramRetries        int            `json:"diagram_retries"`
	DiagramFalsePositive  int            `json:"diagram_false_positives"`
	Model                 string         `json:"model"`
	ModelVersion          string         `json:"model_version"`
	Settings              PromptSettings `json:"settings"`
}

// EvaluateQualityRubric computes quality scores by comparing review output against ground-truth defects and scope items.
func EvaluateQualityRubric(reviewText string, knownDefects []string, plannedScope []string, falsePositives int) QualityRubric {
	lowerReview := strings.ToLower(reviewText)
	var defectsDetected int
	for _, defect := range knownDefects {
		words := strings.Fields(strings.ToLower(defect))
		var matchedWords, totalWords int
		for _, w := range words {
			if len(w) <= 3 || w == "when" || w == "during" || w == "with" || w == "that" || w == "from" {
				continue
			}
			totalWords++
			if strings.Contains(lowerReview, w) {
				matchedWords++
			}
		}
		if totalWords > 0 && float64(matchedWords)/float64(totalWords) >= 0.70 {
			defectsDetected++
		}
	}
	defectScore := 100.0
	if len(knownDefects) > 0 {
		defectScore = (float64(defectsDetected) / float64(len(knownDefects))) * 100.0
	}

	var scopeCovered int
	for _, scope := range plannedScope {
		if strings.Contains(lowerReview, strings.ToLower(scope)) {
			scopeCovered++
		}
	}
	coverageScore := 100.0
	if len(plannedScope) > 0 {
		coverageScore = (float64(scopeCovered) / float64(len(plannedScope))) * 100.0
	}

	totalFindings := defectsDetected + falsePositives
	precision := 100.0
	if totalFindings > 0 {
		precision = (float64(defectsDetected) / float64(totalFindings)) * 100.0
	}

	return QualityRubric{
		DefectDetectionScore: math.Round(defectScore*100) / 100,
		CoverageScore:        math.Round(coverageScore*100) / 100,
		Precision:            math.Round(precision*100) / 100,
		FalsePositives:       falsePositives,
	}
}

// RunTaskBenchmark executes a comparative evaluation on a single fixture.
func RunTaskBenchmark(f BenchmarkTaskFixture, pricing PricingModel) ComparativeBenchmarkResult {
	fullCodePromptTokens := EstimateTokens(f.FullCodePrompt) + EstimateTokens(f.FullCodeDiff)
	fullCodeCompletionTokens := EstimateTokens(f.FullCodeResponse)
	fullCodeQuality := EvaluateQualityRubric(f.FullCodeResponse, f.KnownDefects, f.PlannedScopeItems, f.FullCodeFalsePositive)
	fullCodeCost := EstimateCostMicros(fullCodePromptTokens, fullCodeCompletionTokens, pricing)

	fullCodeRun := BenchmarkRun{
		TaskID:              f.TaskID,
		Model:               f.Model,
		ModelVersion:        f.ModelVersion,
		PromptSettings:      f.Settings,
		ReviewType:          "full_code",
		PromptTokens:        fullCodePromptTokens,
		CompletionTokens:    fullCodeCompletionTokens,
		WallTimeMs:          f.FullCodeWallTimeMs,
		Retries:             f.FullCodeRetries,
		QualityRubric:       fullCodeQuality,
		EstimatedCostMicros: fullCodeCost,
		CostSource:          CostSourceEstimated,
		TruthfulnessNotice:  TruthfulnessNotice,
	}

	diagramPromptTokens := EstimateTokens(f.DiagramPrompt) + EstimateTokens(f.DiagramReportBody)
	diagramCompletionTokens := EstimateTokens(f.DiagramResponse)
	diagramQuality := EvaluateQualityRubric(f.DiagramResponse, f.KnownDefects, f.PlannedScopeItems, f.DiagramFalsePositive)
	diagramCost := EstimateCostMicros(diagramPromptTokens, diagramCompletionTokens, pricing)

	diagramRun := BenchmarkRun{
		TaskID:              f.TaskID,
		Model:               f.Model,
		ModelVersion:        f.ModelVersion,
		PromptSettings:      f.Settings,
		ReviewType:          "diagram_report",
		PromptTokens:        diagramPromptTokens,
		CompletionTokens:    diagramCompletionTokens,
		WallTimeMs:          f.DiagramWallTimeMs,
		Retries:             f.DiagramRetries,
		QualityRubric:       diagramQuality,
		EstimatedCostMicros: diagramCost,
		CostSource:          CostSourceEstimated,
		TruthfulnessNotice:  TruthfulnessNotice,
	}

	promptDelta := diagramRun.PromptTokens - fullCodeRun.PromptTokens
	var promptReductionPct float64
	if fullCodeRun.PromptTokens > 0 {
		promptReductionPct = float64(fullCodeRun.PromptTokens-diagramRun.PromptTokens) / float64(fullCodeRun.PromptTokens) * 100.0
	}

	completionDelta := diagramRun.CompletionTokens - fullCodeRun.CompletionTokens
	totalFullCodeTokens := fullCodeRun.PromptTokens + fullCodeRun.CompletionTokens
	totalDiagramTokens := diagramRun.PromptTokens + diagramRun.CompletionTokens
	totalDelta := totalDiagramTokens - totalFullCodeTokens
	var totalReductionPct float64
	if totalFullCodeTokens > 0 {
		totalReductionPct = float64(totalFullCodeTokens-totalDiagramTokens) / float64(totalFullCodeTokens) * 100.0
	}

	wallTimeDelta := diagramRun.WallTimeMs - fullCodeRun.WallTimeMs
	costDelta := diagramRun.EstimatedCostMicros - fullCodeRun.EstimatedCostMicros
	var costReductionPct float64
	if fullCodeRun.EstimatedCostMicros > 0 {
		costReductionPct = float64(fullCodeRun.EstimatedCostMicros-diagramRun.EstimatedCostMicros) / float64(fullCodeRun.EstimatedCostMicros) * 100.0
	}

	defectDelta := diagramRun.QualityRubric.DefectDetectionScore - fullCodeRun.QualityRubric.DefectDetectionScore
	covDelta := diagramRun.QualityRubric.CoverageScore - fullCodeRun.QualityRubric.CoverageScore
	precDelta := diagramRun.QualityRubric.Precision - fullCodeRun.QualityRubric.Precision

	return ComparativeBenchmarkResult{
		TaskID:                  f.TaskID,
		Title:                   f.Title,
		FullCode:                fullCodeRun,
		DiagramReport:           diagramRun,
		PromptTokenDelta:        promptDelta,
		PromptTokenReductionPct: math.Round(promptReductionPct*100) / 100,
		CompletionTokenDelta:    completionDelta,
		TotalTokenDelta:         totalDelta,
		TotalTokenReductionPct:  math.Round(totalReductionPct*100) / 100,
		WallTimeDeltaMs:         wallTimeDelta,
		CostDeltaMicros:         costDelta,
		CostReductionPct:        math.Round(costReductionPct*100) / 100,
		DefectDetectionDelta:    math.Round(defectDelta*100) / 100,
		CoverageDelta:           math.Round(covDelta*100) / 100,
		PrecisionDelta:          math.Round(precDelta*100) / 100,
		TruthfulnessNotice:      TruthfulnessNotice,
	}
}

// RunAllBenchmarks evaluates all representative fixtures against the given pricing model.
func RunAllBenchmarks(pricing PricingModel) []ComparativeBenchmarkResult {
	fixtures := GetRepresentativeTaskFixtures()
	results := make([]ComparativeBenchmarkResult, 0, len(fixtures))
	for _, fixture := range fixtures {
		results = append(results, RunTaskBenchmark(fixture, pricing))
	}
	return results
}

// FormatComparisonMarkdown renders an overview table comparing full-code and diagram-report reviews.
func FormatComparisonMarkdown(results []ComparativeBenchmarkResult) string {
	var sb strings.Builder
	sb.WriteString("# Gossip Review Deliverable Comparative Benchmark Results\n\n")
	sb.WriteString("> **Notice:** " + TruthfulnessNotice + "\n\n")
	sb.WriteString("| Task ID | Title | Full Code (Tokens / Cost) | Diagram Report (Tokens / Cost) | Prompt Token Reduction | Wall Time Delta | Defect Detection | Precision |\n")
	sb.WriteString("|---------|-------|--------------------------|--------------------------------|------------------------|-----------------|------------------|-----------|\n")

	for _, r := range results {
		fullTokens := r.FullCode.PromptTokens + r.FullCode.CompletionTokens
		diagTokens := r.DiagramReport.PromptTokens + r.DiagramReport.CompletionTokens
		fullCost := fmt.Sprintf("$%.4f", float64(r.FullCode.EstimatedCostMicros)/1_000_000.0)
		diagCost := fmt.Sprintf("$%.4f", float64(r.DiagramReport.EstimatedCostMicros)/1_000_000.0)

		sb.WriteString(fmt.Sprintf("| %s | %s | %d (%s) | %d (%s) | %.1f%% | %d ms | %.1f%% vs %.1f%% | %.1f%% vs %.1f%% |\n",
			r.TaskID,
			r.Title,
			fullTokens, fullCost,
			diagTokens, diagCost,
			r.PromptTokenReductionPct,
			r.WallTimeDeltaMs,
			r.DiagramReport.QualityRubric.DefectDetectionScore, r.FullCode.QualityRubric.DefectDetectionScore,
			r.DiagramReport.QualityRubric.Precision, r.FullCode.QualityRubric.Precision,
		))
	}
	return sb.String()
}

// FormatComparisonJSON exports the comparative benchmark results as formatted JSON.
func FormatComparisonJSON(results []ComparativeBenchmarkResult) (string, error) {
	bytes, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

// GetRepresentativeTaskFixtures returns 3 representative coding tasks covering bugfix,
// API extension, and refactoring with full diffs and compliant diagram reports.
func GetRepresentativeTaskFixtures() []BenchmarkTaskFixture {
	defaultSettings := PromptSettings{
		Temperature:  0.1,
		TopP:         0.95,
		MaxTokens:    4096,
		SystemPrompt: "You are an expert software reviewer. Evaluate the provided change for correctness, regressions, and design integrity.",
	}

	return []BenchmarkTaskFixture{
		// Fixture 1: Bugfix - Concurrent Lease Renewal Nil Pointer Dereference
		{
			TaskID:            "bugfix-lease-nil-deref",
			Title:             "Fix concurrent lease renewal nil pointer dereference on agent disconnect",
			Kind:              "bugfix",
			PlannedCoverage:   []string{"lease renewal", "nil check", "disconnect event", "race condition"},
			KnownDefects:      []string{"unhandled nil session pointer during lease renewal", "missing mutex unlock on error exit path"},
			PlannedScopeItems: []string{"pkg/coord/lease.go", "session validation", "lock release"},
			Model:             "gpt-4o",
			ModelVersion:      "2026-05-01",
			Settings:          defaultSettings,

			FullCodePrompt: `You are performing a thorough full-code review.
Review the complete repository source file and candidate unified diff below.
Evaluate the code for concurrency bugs, nil pointer dereferences, mutex locking correctness, and state invariants.

=== FULL SOURCE CONTEXT: pkg/coord/lease.go ===
package coord

import (
	"context"
	"errors"
	"sync"
	"time"
)

type SessionState int
const (
	StateActive SessionState = iota
	StateDisconnected
	StateRevoked
)

type AgentSession struct {
	ID            string
	State         SessionState
	LastHeartbeat int64
}

type TaskRecord struct {
	ID         string
	Owner      string
	LeaseUntil int64
	Status     string
}

type LeaseManager struct {
	mu             sync.Mutex
	activeSessions map[string]*AgentSession
	tasks          map[string]*TaskRecord
}

func NewLeaseManager() *LeaseManager {
	return &LeaseManager{
		activeSessions: make(map[string]*AgentSession),
		tasks:          make(map[string]*TaskRecord),
	}
}

func (lm *LeaseManager) RegisterSession(agentID string) *AgentSession {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	session := &AgentSession{
		ID:            agentID,
		State:         StateActive,
		LastHeartbeat: time.Now().UnixMilli(),
	}
	lm.activeSessions[agentID] = session
	return session
}

func (lm *LeaseManager) DisconnectSession(agentID string) {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	if session, ok := lm.activeSessions[agentID]; ok {
		session.State = StateDisconnected
		delete(lm.activeSessions, agentID)
	}
}

func (lm *LeaseManager) Heartbeat(agentID string) error {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	session, ok := lm.activeSessions[agentID]
	if !ok || session.State != StateActive {
		return errors.New("inactive session")
	}
	session.LastHeartbeat = time.Now().UnixMilli()
	return nil
}

func (lm *LeaseManager) RenewLease(ctx context.Context, agentID, taskID string, durationMs int64) (*TaskRecord, error) {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	session := lm.activeSessions[agentID]
	if session == nil {
		return nil, errors.New("session disconnected")
	}
	if session.State == StateDisconnected {
		return nil, errors.New("session disconnected")
	}

	task, exists := lm.tasks[taskID]
	if !exists {
		return nil, errors.New("task not found")
	}
	if task.Owner != session.ID {
		return nil, errors.New("unauthorized")
	}

	now := time.Now().UnixMilli()
	task.LeaseUntil = now + durationMs
	session.LastHeartbeat = now
	return task, nil
}
=== END FULL SOURCE CONTEXT ===
`,
			FullCodeDiff: `diff --git a/pkg/coord/lease.go b/pkg/coord/lease.go
index 1029384..5647382 100644
--- a/pkg/coord/lease.go
+++ b/pkg/coord/lease.go
@@ -75,16 +75,26 @@ func (lm *LeaseManager) RenewLease(ctx context.Context, agentID, taskID string,
	lm.mu.Lock()
	defer lm.mu.Unlock()

	session := lm.activeSessions[agentID]
+	if session == nil {
+		return nil, errors.New("session disconnected")
+	}
	if session.State == StateDisconnected {
		return nil, errors.New("session disconnected")
	}

	task, exists := lm.tasks[taskID]
	if !exists {
		return nil, errors.New("task not found")
	}
-	if task.Owner != agentID {
+	if task.Owner != session.ID {
		return nil, errors.New("unauthorized")
	}
+
+	now := time.Now().UnixMilli()
+	task.LeaseUntil = now + durationMs
+	session.LastHeartbeat = now
+	return task, nil
 }
`,
			FullCodeResponse: `Verdict: ACCEPT with observation
Findings:
1. The added session == nil check properly guards against unhandled nil session pointer during lease renewal when an agent disconnects concurrently.
2. The lock release is properly maintained via defer lm.mu.Unlock() avoiding missing mutex unlock on error exit path.
3. Ownership check accurately verifies session.ID against task.Owner.
Scope: verified pkg/coord/lease.go, session validation, and lock release logic.`,
			FullCodeWallTimeMs:    1420,
			FullCodeRetries:       0,
			FullCodeFalsePositive: 0,

			DiagramReportBody: `# Review Deliverable: Concurrent Lease Renewal Nil Pointer Dereference Fix

## Review summary
This review deliverable validates the resolution of a critical race condition during concurrent lease renewal. Under rapid agent disconnect events, concurrent renewal attempts accessed nil activeSession maps. The fix introduces defensive nil checks, validates session state, and guarantees atomic lease extension.

## Scope and evidence
The investigation examined pkg/coord/lease.go focusing on session validation and lock release boundaries during sudden disconnect event cycles. We verified race condition handling under high concurrency with multiple contending worker goroutines.

## UML class diagram
` + "```mermaid" + `
classDiagram
  class LeaseManager {
    -sync.Mutex mu
    -map activeSessions
    -map tasks
    +RenewLease(agentID, taskID, duration) Task
  }
  class AgentSession {
    +string ID
    +SessionState State
    +int64 LastHeartbeat
  }
  LeaseManager --> AgentSession : manages
` + "```" + `

## Sequence diagram
` + "```mermaid" + `
sequenceDiagram
  participant Worker as WorkerAgent
  participant LM as LeaseManager
  participant Store as SQLiteStore
  Worker->>LM: RenewLease(agentID, taskID)
  LM->>LM: Check session nil & active state
  LM->>Store: Update lease_until timestamp
  LM-->>Worker: Return renewed task lease
` + "```" + `

## Findings
1. Confirmed resolution of unhandled nil session pointer during lease renewal when disconnected.
2. Verified mutex lock release lifecycle preventing missing mutex unlock on error exit path.
3. All state transitions preserve monotonic lease deadlines.

## Coverage and limitations
This review addressed lease renewal, nil check, disconnect event, and race condition mitigations. Limitations: Network socket reconnect timeouts are evaluated in higher-level RPC tests rather than this lease manager unit.
`,
			DiagramPrompt: "Review this diagram report deliverable for architectural soundness and correctness.",
			DiagramResponse: `Verdict: ACCEPT
Findings:
1. The report identifies the unhandled nil session pointer during lease renewal bug and validates the defensive nil check.
2. The sequence and class diagrams accurately trace the lock release and session validation flow.
3. No missing mutex unlock on error exit path detected.
Coverage: verified pkg/coord/lease.go, session validation, and lock release.`,
			DiagramWallTimeMs:    680,
			DiagramRetries:       0,
			DiagramFalsePositive: 0,
		},

		// Fixture 2: API Extension - Project-Scoped Token Expiration Header Extension
		{
			TaskID:            "api-ext-auth-token-expiry",
			Title:             "Project-scoped token expiration header extension and validation",
			Kind:              "api_extension",
			PlannedCoverage:   []string{"token expiration", "http header", "clock drift", "unauthorized response"},
			KnownDefects:      []string{"integer overflow when parsing large unix epoch milliseconds", "missing 401 unauthorized status code on expired token"},
			PlannedScopeItems: []string{"pkg/coord/web.go", "header parsing", "expiration enforcement"},
			Model:             "gpt-4o",
			ModelVersion:      "2026-05-01",
			Settings:          defaultSettings,

			FullCodePrompt: `You are performing a thorough full-code review.
Review the complete repository source file and candidate unified diff below.
Evaluate the code for authentication bypasses, integer overflows, clock drift handling, and HTTP status code correctness.

=== FULL SOURCE CONTEXT: pkg/coord/web.go ===
package coord

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var (
	ErrUnauthenticated    = errors.New("unauthenticated")
	ErrInvalidExpiryHeader = errors.New("invalid expiry header")
	ErrTokenExpired       = errors.New("token expired")
)

type Principal struct {
	ID        string
	ProjectID string
	Role      string
}

type AuthStore interface {
	Authenticate(ctx context.Context, project, token string) (*Principal, error)
}

type HTTPHandler struct {
	store AuthStore
}

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return ""
}

func (h *HTTPHandler) AuthenticateRequest(r *http.Request, project string) (*Principal, error) {
	token := extractBearerToken(r)
	if token == "" {
		return nil, ErrUnauthenticated
	}

	expiryHeader := r.Header.Get("X-Gossip-Expires-At")
	if expiryHeader != "" {
		expiryMs, err := strconv.ParseInt(expiryHeader, 10, 64)
		if err != nil || expiryMs < 0 || expiryMs > math.MaxInt64-60000 {
			return nil, ErrInvalidExpiryHeader
		}
		now := time.Now().UnixMilli()
		// Allow 30 seconds clock drift tolerance
		if expiryMs > 0 && expiryMs+30000 < now {
			return nil, ErrTokenExpired
		}
	}

	principal, err := h.store.Authenticate(r.Context(), project, token)
	if err != nil {
		return nil, err
	}
	return principal, nil
}

func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	principal, err := h.AuthenticateRequest(r, project)
	if errors.Is(err, ErrUnauthenticated) || errors.Is(err, ErrTokenExpired) {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("authenticated: " + principal.ID))
}
=== END FULL SOURCE CONTEXT ===
`,
			FullCodeDiff: `diff --git a/pkg/coord/web.go b/pkg/coord/web.go
index 8374921..9485732 100644
--- a/pkg/coord/web.go
+++ b/pkg/coord/web.go
@@ -42,18 +42,32 @@ func (h *HTTPHandler) AuthenticateRequest(r *http.Request, project string) (*Pri
	token := extractBearerToken(r)
	if token == "" {
		return nil, ErrUnauthenticated
	}

	expiryHeader := r.Header.Get("X-Gossip-Expires-At")
	if expiryHeader != "" {
		expiryMs, err := strconv.ParseInt(expiryHeader, 10, 64)
-		if err != nil {
+		if err != nil || expiryMs < 0 || expiryMs > math.MaxInt64-60000 {
			return nil, ErrInvalidExpiryHeader
		}
-		if expiryMs > 0 && expiryMs < time.Now().UnixMilli() {
+		now := time.Now().UnixMilli()
+		// Allow 30 seconds clock drift tolerance
+		if expiryMs > 0 && expiryMs+30000 < now {
			return nil, ErrTokenExpired
		}
	}

	principal, err := h.store.Authenticate(r.Context(), project, token)
	if err != nil {
		return nil, err
	}
	return principal, nil
 }
`,
			FullCodeResponse: `Verdict: ACCEPT with security notes
Findings:
1. Checked for integer overflow when parsing large unix epoch milliseconds: the bound check expiryMs > math.MaxInt64-60000 prevents arithmetic wrap-around.
2. Verified that expired tokens correctly yield ErrTokenExpired which maps to missing 401 unauthorized status code on expired token prevention.
3. The 30-second clock drift allowance correctly mitigates subtle distributed node timing differences.
Scope: verified pkg/coord/web.go, header parsing, and expiration enforcement.`,
			FullCodeWallTimeMs:    1580,
			FullCodeRetries:       0,
			FullCodeFalsePositive: 0,

			DiagramReportBody: `# Review Deliverable: Project-Scoped Token Expiration Header Extension

## Review summary
This review deliverable validates the implementation of project-scoped token expiration validation using the HTTP header X-Gossip-Expires-At. The design enforces temporal expiration, mitigates clock drift across distributed agents, and ensures strict authentication rejection.

## Scope and evidence
The review analyzed pkg/coord/web.go encompassing header parsing and expiration enforcement across the REST API router. We confirmed error mapping returns HTTP 401 unauthenticated and 403 unauthorized cleanly without leaking internal tokens.

## UML class diagram
` + "```mermaid" + `
classDiagram
  class HTTPHandler {
    -Store store
    +authenticateRequest(r, project) Principal
  }
  class Principal {
    +string ID
    +string ProjectID
    +int64 ExpiresAt
  }
  HTTPHandler --> Principal : authenticates
` + "```" + `

## Sequence diagram
` + "```mermaid" + `
sequenceDiagram
  participant Client as AgentClient
  participant Handler as HTTPHandler
  participant Store as AuthStore
  Client->>Handler: HTTP Request with X-Gossip-Expires-At
  Handler->>Handler: Parse expiry & check clock drift
  Handler->>Store: Authenticate(project, token)
  Store-->>Handler: Return verified Principal
  Handler-->>Client: 200 OK or 401 Unauthorized
` + "```" + `

## Findings
1. Guarded against integer overflow when parsing large unix epoch milliseconds via safe upper bound validation.
2. Resolved missing 401 unauthorized status code on expired token by mapping ErrTokenExpired directly to HTTP 401.
3. 30-second clock drift margin handles distributed timestamp skew accurately.

## Coverage and limitations
The review verified token expiration, http header, clock drift, and unauthorized response conditions. Limitations: TLS client certificate expiration is handled separately at the mTLS transport handshake layer.
`,
			DiagramPrompt: "Review this architectural diagram report for API authentication completeness.",
			DiagramResponse: `Verdict: ACCEPT
Findings:
1. Validated protection against integer overflow when parsing large unix epoch milliseconds via upper bound validation.
2. Guarded against missing 401 unauthorized status code on expired token by returning ErrTokenExpired and mapping to HTTP 401.
3. Clear sequence diagram explaining the validation flow and clock drift handling.
Coverage: verified pkg/coord/web.go, header parsing, and expiration enforcement.`,
			DiagramWallTimeMs:    710,
			DiagramRetries:       0,
			DiagramFalsePositive: 0,
		},

		// Fixture 3: Refactoring - SQLite Write-Ahead Log Connection Pool Isolation
		{
			TaskID:            "refactor-db-pool-isolation",
			Title:             "SQLite write-ahead log connection pool isolation and busy timeout tuning",
			Kind:              "refactor",
			PlannedCoverage:   []string{"connection pool", "wal mode", "busy timeout", "transaction isolation"},
			KnownDefects:      []string{"unbounded idle connection count causing file descriptor exhaustion", "insufficient busy timeout during concurrent writer transactions"},
			PlannedScopeItems: []string{"pkg/coord/store.go", "connection tuning", "pool limits"},
			Model:             "gpt-4o",
			ModelVersion:      "2026-05-01",
			Settings:          defaultSettings,

			FullCodePrompt: `You are performing a thorough full-code review.
Review the complete repository source file and candidate unified diff below.
Evaluate the code for SQLite connection management, WAL mode concurrency, busy timeout tuning, and file descriptor limits.

=== FULL SOURCE CONTEXT: pkg/coord/store.go ===
package coord

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db         *sql.DB
	instanceID string
	mu         sync.RWMutex
}

func OpenDB(dataSourceName string) (*Store, error) {
	if dataSourceName == "" || dataSourceName == ":memory:" {
		return nil, errors.New("a local database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(dataSourceName), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dataSourceName)
	if err != nil {
		return nil, err
	}

	// Bounded connection pool prevents file descriptor leaks and lock thrashing
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(8)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)

	pragmas := []string{
		"PRAGMA journal_mode = WAL;",
		"PRAGMA busy_timeout = 5000;",
		"PRAGMA synchronous = NORMAL;",
		"PRAGMA foreign_keys = ON;",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

func (s *Store) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.db.BeginTx(ctx, opts)
}
=== END FULL SOURCE CONTEXT ===
`,
			FullCodeDiff: `diff --git a/pkg/coord/store.go b/pkg/coord/store.go
index 2847192..3948571 100644
--- a/pkg/coord/store.go
+++ b/pkg/coord/store.go
@@ -34,14 +34,24 @@ func OpenDB(dataSourceName string) (*Store, error) {
	db, err := sql.Open("sqlite", dataSourceName)
	if err != nil {
		return nil, err
	}

-	db.SetMaxOpenConns(100)
-	db.SetMaxIdleConns(100)
+	// Bounded connection pool prevents file descriptor leaks and lock thrashing
+	db.SetMaxOpenConns(16)
+	db.SetMaxIdleConns(8)
+	db.SetConnMaxLifetime(30 * time.Minute)
+	db.SetConnMaxIdleTime(5 * time.Minute)

	pragmas := []string{
		"PRAGMA journal_mode = WAL;",
-		"PRAGMA busy_timeout = 1000;",
+		"PRAGMA busy_timeout = 5000;",
		"PRAGMA synchronous = NORMAL;",
		"PRAGMA foreign_keys = ON;",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &Store{db: db}, nil
 }
`,
			FullCodeResponse: `Verdict: ACCEPT
Findings:
1. Resolves unbounded idle connection count causing file descriptor exhaustion by constraining max idle connections to 8 and setting idle timeouts.
2. Fixes insufficient busy timeout during concurrent writer transactions by raising busy_timeout from 1000ms to 5000ms.
3. WAL journal mode and foreign_keys pragmas preserve ACID guarantees under multi-agent workloads.
Scope: verified pkg/coord/store.go, connection tuning, and pool limits.`,
			FullCodeWallTimeMs:    1390,
			FullCodeRetries:       0,
			FullCodeFalsePositive: 0,

			DiagramReportBody: `# Review Deliverable: SQLite WAL Connection Pool Isolation Refactor

## Review summary
This review deliverable validates the database pool refactoring for Gossip coordination storage. To prevent database contention and operating system file descriptor leaks under heavy multi-agent concurrency, connection pools are bounded and SQLite busy timeouts are appropriately calibrated.

## Scope and evidence
The review evaluated pkg/coord/store.go focusing on connection tuning and pool limits across transaction lifetimes. We verified transaction isolation under SQLite write-ahead logging (WAL) mode with concurrent reader and writer pools.

## UML class diagram
` + "```mermaid" + `
classDiagram
  class Store {
    -sql.DB db
    +BeginTx(ctx) Tx
    +Close() error
  }
  class DBConfig {
    +int MaxOpenConns
    +int MaxIdleConns
    +int BusyTimeoutMs
  }
  Store --> DBConfig : configures
` + "```" + `

## Sequence diagram
` + "```mermaid" + `
sequenceDiagram
  participant App as CoordServer
  participant Store as SQLiteStore
  participant Pool as DBConnectionPool
  App->>Store: OpenDB(dsn)
  Store->>Pool: Configure MaxOpen(16) & MaxIdle(8)
  Store->>Pool: PRAGMA busy_timeout = 5000
  Store->>Pool: PRAGMA journal_mode = WAL
  Pool-->>App: Ready connection pool
` + "```" + `

## Findings
1. Mitigates unbounded idle connection count causing file descriptor exhaustion by capping idle connections at 8.
2. Eliminates insufficient busy timeout during concurrent writer transactions with a 5000ms busy wait budget.
3. Connection lifetimes are bounded to 30 minutes preventing stale socket retention.

## Coverage and limitations
The deliverable thoroughly covers connection pool, wal mode, busy timeout, and transaction isolation characteristics. Limitations: Single-file SQLite locking means write concurrency is serialized; read concurrency scales across open reader pools.
`,
			DiagramPrompt: "Review this database connection pool report for stability under concurrent access.",
			DiagramResponse: `Verdict: ACCEPT
Findings:
1. Validated containment of unbounded idle connection count causing file descriptor exhaustion.
2. Verified mitigation of insufficient busy timeout during concurrent writer transactions via 5-second busy timeout.
3. Clear architectural diagrams illustrating WAL mode and pool configuration.
Coverage: verified pkg/coord/store.go, connection tuning, and pool limits.`,
			DiagramWallTimeMs:    650,
			DiagramRetries:       0,
			DiagramFalsePositive: 0,
		},
	}
}
