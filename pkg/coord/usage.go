package coord

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// UsageCost groups agent-reported cost totals by currency. Values are stored
// in millionths of the currency unit to avoid floating-point rounding.
type UsageCost struct {
	Currency   string `json:"currency"`
	CostMicros int64  `json:"cost_micros"`
	Records    int    `json:"records"`
}

// UsageSummary is self-reported runtime accounting, not provider-verified
// billing or a server-enforced usage meter.
type UsageSummary struct {
	Records      int            `json:"records"`
	InputTokens  int64          `json:"input_tokens"`
	OutputTokens int64          `json:"output_tokens"`
	TotalTokens  int64          `json:"total_tokens"`
	Sources      map[string]int `json:"sources"`
	Costs        []UsageCost    `json:"costs"`
}

func validateUsageReport(u UsageReport) (UsageReport, error) {
	u.Provider = strings.TrimSpace(u.Provider)
	u.Model = strings.TrimSpace(u.Model)
	u.Source = strings.TrimSpace(u.Source)
	u.Currency = strings.ToUpper(strings.TrimSpace(u.Currency))
	if u.Provider == "" || len(u.Provider) > 128 || !utf8.ValidString(u.Provider) || u.Model == "" || len(u.Model) > 256 || !utf8.ValidString(u.Model) {
		return u, errors.New("provider and model are required within size limits")
	}
	switch u.Source {
	case "provider_reported", "host_reported", "estimated":
	default:
		return u, errors.New("source must be provider_reported, host_reported, or estimated")
	}
	if u.InputTokens < 0 || u.InputTokens > 1_000_000_000_000 || u.OutputTokens < 0 || u.OutputTokens > 1_000_000_000_000 || u.InputTokens > math.MaxInt64-u.OutputTokens {
		return u, errors.New("token counts are outside supported limits")
	}
	if u.CostMicros < 0 || u.CostMicros > 1_000_000_000_000_000 {
		return u, errors.New("cost_micros is outside supported limits")
	}
	if u.CostMicros > 0 {
		if len(u.Currency) != 3 {
			return u, errors.New("a three-letter currency is required when cost is reported")
		}
		for _, r := range u.Currency {
			if r < 'A' || r > 'Z' {
				return u, errors.New("currency must contain three letters")
			}
		}
	} else if u.Currency != "" {
		if len(u.Currency) != 3 {
			return u, errors.New("currency must contain three letters")
		}
		for _, r := range u.Currency {
			if r < 'A' || r > 'Z' {
				return u, errors.New("currency must contain three letters")
			}
		}
	}
	return u, nil
}

// RecordAttemptUsage stores one immutable, idempotent usage summary for an
// attempt. The active attempt owner must submit it with result.submit authority.
func (s *Store) RecordAttemptUsage(ctx context.Context, project, token, taskID string, attempt int, requestID string, usage UsageReport) (UsageReport, error) {
	if strings.TrimSpace(taskID) == "" || len(taskID) > 256 || !utf8.ValidString(taskID) || attempt < 1 || strings.TrimSpace(requestID) == "" || len(requestID) > 128 || !utf8.ValidString(requestID) {
		return UsageReport{}, errors.New("task_id, positive attempt, and stable request_id are required")
	}
	u, err := validateUsageReport(usage)
	if err != nil {
		return UsageReport{}, err
	}
	payload, err := json.Marshal(struct {
		TaskID  string      `json:"task_id"`
		Attempt int         `json:"attempt"`
		Usage   UsageReport `json:"usage"`
	}{taskID, attempt, u})
	if err != nil {
		return UsageReport{}, err
	}
	digest := sha256.Sum256(payload)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return UsageReport{}, err
	}
	defer tx.Rollback()
	actor, err := authorizePermission(ctx, tx, project, token, PermissionResultSubmit)
	if err != nil {
		return UsageReport{}, err
	}
	var attemptOwner string
	err = tx.QueryRowContext(ctx, `SELECT a.principal_id FROM attempts a JOIN tasks t ON t.id=a.task_id WHERE t.project_id=? AND a.task_id=? AND a.number=?`, project, taskID, attempt).Scan(&attemptOwner)
	if errors.Is(err, sql.ErrNoRows) {
		return UsageReport{}, ErrNotFound
	}
	if err != nil {
		return UsageReport{}, err
	}
	if attemptOwner != actor.ID {
		return UsageReport{}, ErrUnauthorized
	}
	var previousDigest []byte
	var previous UsageReport
	err = tx.QueryRowContext(ctx, `SELECT payload_digest,principal_id,request_id,provider,model,source,input_tokens,output_tokens,cost_micros,currency,recorded_at FROM attempt_usage WHERE task_id=? AND attempt=?`, taskID, attempt).Scan(&previousDigest, &previous.PrincipalID, &previous.RequestID, &previous.Provider, &previous.Model, &previous.Source, &previous.InputTokens, &previous.OutputTokens, &previous.CostMicros, &previous.Currency, &previous.RecordedAt)
	if err == nil {
		if previous.PrincipalID != actor.ID || previous.RequestID != requestID || !equalDigest(previousDigest, digest[:]) {
			return UsageReport{}, ErrConflict
		}
		previous.Attempt = attempt
		return previous, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return UsageReport{}, err
	}
	now := time.Now().UnixMilli()
	_, err = tx.ExecContext(ctx, `INSERT INTO attempt_usage(task_id,attempt,project_id,principal_id,request_id,payload_digest,provider,model,source,input_tokens,output_tokens,cost_micros,currency,recorded_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, taskID, attempt, project, actor.ID, requestID, digest[:], u.Provider, u.Model, u.Source, u.InputTokens, u.OutputTokens, u.CostMicros, u.Currency, now)
	if err != nil {
		return UsageReport{}, err
	}
	u.TaskID, u.Attempt, u.PrincipalID, u.RequestID, u.RecordedAt = taskID, attempt, actor.ID, requestID, now
	if err = event(ctx, tx, project, actor.ID, "usage.reported", taskID, map[string]any{"attempt": attempt, "provider": u.Provider, "model": u.Model, "source": u.Source, "input_tokens": u.InputTokens, "output_tokens": u.OutputTokens, "cost_micros": u.CostMicros, "currency": u.Currency}); err != nil {
		return UsageReport{}, err
	}
	if err = tx.Commit(); err != nil {
		return UsageReport{}, err
	}
	return u, nil
}

func equalDigest(a, b []byte) bool {
	return bytes.Equal(a, b)
}

func readReportedUsage(ctx context.Context, tx *sql.Tx, project string) (UsageSummary, error) {
	summary := UsageSummary{Sources: map[string]int{}, Costs: []UsageCost{}}
	rows, err := tx.QueryContext(ctx, `SELECT source,input_tokens,output_tokens,cost_micros,currency FROM attempt_usage WHERE project_id=? ORDER BY recorded_at,task_id,attempt`, project)
	if err != nil {
		return summary, err
	}
	defer rows.Close()
	costs := map[string]int64{}
	costCounts := map[string]int{}
	for rows.Next() {
		var source, currency string
		var input, output, cost int64
		if err = rows.Scan(&source, &input, &output, &cost, &currency); err != nil {
			return summary, err
		}
		total := input + output
		if summary.InputTokens > math.MaxInt64-input || summary.OutputTokens > math.MaxInt64-output || summary.TotalTokens > math.MaxInt64-total {
			return summary, errors.New("reported usage aggregate exceeds supported limits")
		}
		summary.InputTokens += input
		summary.OutputTokens += output
		summary.TotalTokens += total
		summary.Records++
		summary.Sources[source]++
		if currency != "" {
			if costs[currency] > math.MaxInt64-cost {
				return summary, errors.New("reported cost aggregate exceeds supported limits")
			}
			costs[currency] += cost
			costCounts[currency]++
		}
	}
	if err = rows.Err(); err != nil {
		return summary, err
	}
	for currency, cost := range costs {
		summary.Costs = append(summary.Costs, UsageCost{Currency: currency, CostMicros: cost, Records: costCounts[currency]})
	}
	sort.Slice(summary.Costs, func(i, j int) bool { return summary.Costs[i].Currency < summary.Costs[j].Currency })
	return summary, nil
}
