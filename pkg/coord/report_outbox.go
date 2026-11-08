package coord

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxReportOutboxEntries = 100
	maxReportOutboxBytes   = 32 * 1024 * 1024
)

type QueuedReport struct {
	RequestID string `json:"request_id"`
	TaskID    string `json:"task_id"`
	Attempt   int    `json:"attempt"`
	Baseline  string `json:"baseline"`
	Status    string `json:"status"`
	ReportID  string `json:"report_id,omitempty"`
	Digest    string `json:"digest,omitempty"`
	Error     string `json:"error,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

func stableReportRequestID(taskID string, attempt int) string {
	h := sha256.Sum256([]byte(taskID + "\x00" + strconv.Itoa(attempt)))
	return "report_" + hex.EncodeToString(h[:16])
}

func (o *Outbox) EnqueueReport(ctx context.Context, task string, attempt int, baseline, body, requestID string) (QueuedReport, error) {
	var item QueuedReport
	if task == "" || len(task) > 256 || attempt < 1 || strings.TrimSpace(baseline) == "" || len(baseline) > 4096 || strings.TrimSpace(body) == "" || len(body) > 1024*1024 || !utf8.ValidString(task) || !utf8.ValidString(baseline) || !utf8.ValidString(body) || len(requestID) > 128 || !utf8.ValidString(requestID) {
		return item, errors.New("invalid report")
	}
	if requestID == "" {
		requestID = stableReportRequestID(task, attempt)
	}
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return item, err
	}
	defer tx.Rollback()
	var storedBody string
	err = tx.QueryRowContext(ctx, `SELECT request_id,task_id,attempt,baseline,body,status,report_id,digest,error,created_at FROM report_outbound WHERE request_id=?`, requestID).Scan(&item.RequestID, &item.TaskID, &item.Attempt, &item.Baseline, &storedBody, &item.Status, &item.ReportID, &item.Digest, &item.Error, &item.CreatedAt)
	if err == nil {
		if item.TaskID != task || item.Attempt != attempt || item.Baseline != baseline || storedBody != body {
			return QueuedReport{}, ErrConflict
		}
		return item, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return item, err
	}
	var count int
	var queuedBytes int64
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(body)),0) FROM report_outbound`).Scan(&count, &queuedBytes); err != nil {
		return item, err
	}
	if count >= maxReportOutboxEntries || queuedBytes+int64(len(body)) > maxReportOutboxBytes {
		return item, errors.New("report outbox full; remove delivered or failed entries")
	}
	item = QueuedReport{RequestID: requestID, TaskID: task, Attempt: attempt, Baseline: baseline, Status: "pending", CreatedAt: time.Now().UnixMilli()}
	if _, err = tx.ExecContext(ctx, `INSERT INTO report_outbound(request_id,task_id,attempt,baseline,body,status,created_at) VALUES(?,?,?,?,?,'pending',?)`, requestID, task, attempt, baseline, body, item.CreatedAt); err != nil {
		return QueuedReport{}, err
	}
	if err = tx.Commit(); err != nil {
		return QueuedReport{}, err
	}
	o.signal()
	return item, nil
}

func (o *Outbox) ListReportQueue(ctx context.Context, limit int) ([]QueuedReport, error) {
	if limit < 1 || limit > 200 {
		return nil, errors.New("invalid limit")
	}
	rows, err := o.db.QueryContext(ctx, `SELECT request_id,task_id,attempt,baseline,status,report_id,digest,error,created_at FROM report_outbound ORDER BY created_at DESC,request_id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []QueuedReport{}
	for rows.Next() {
		var item QueuedReport
		if err = rows.Scan(&item.RequestID, &item.TaskID, &item.Attempt, &item.Baseline, &item.Status, &item.ReportID, &item.Digest, &item.Error, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (o *Outbox) RetryReport(ctx context.Context, id string) error {
	r, err := o.db.ExecContext(ctx, `UPDATE report_outbound SET status='pending',error='',retry_at=0 WHERE request_id=? AND status='failed'`, id)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	o.signal()
	return nil
}

func (o *Outbox) DiscardReport(ctx context.Context, id string) error {
	r, err := o.db.ExecContext(ctx, `DELETE FROM report_outbound WHERE request_id=? AND status='failed'`, id)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

func (o *Outbox) PruneReports(ctx context.Context) (int64, error) {
	r, err := o.db.ExecContext(ctx, `DELETE FROM report_outbound WHERE status='delivered'`)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

func (o *Outbox) deliverReports(ctx context.Context, backend Backend, project, token string) {
	submitter, ok := backend.(interface {
		SubmitReportWithID(context.Context, string, string, string, int, string, string, string) (Report, error)
	})
	if !ok {
		return
	}
	for ctx.Err() == nil {
		var q QueuedReport
		var body string
		err := o.db.QueryRowContext(ctx, `SELECT request_id,task_id,attempt,baseline,body FROM report_outbound WHERE status='pending' AND retry_at<=? ORDER BY created_at,request_id LIMIT 1`, time.Now().UnixMilli()).Scan(&q.RequestID, &q.TaskID, &q.Attempt, &q.Baseline, &body)
		if err != nil {
			return
		}
		request, cancel := context.WithTimeout(ctx, 30*time.Second)
		report, submitErr := submitter.SubmitReportWithID(request, project, token, q.TaskID, q.Attempt, q.Baseline, body, q.RequestID)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if submitErr != nil {
			if errors.Is(submitErr, ErrUnauthorized) || errors.Is(submitErr, ErrConflict) || errors.Is(submitErr, ErrNotFound) {
				_, _ = o.db.ExecContext(ctx, `UPDATE report_outbound SET status='failed',error=? WHERE request_id=? AND status='pending'`, submitErr.Error(), q.RequestID)
				continue
			}
			o.deferOutbound(ctx, "report_outbound", q.RequestID, submitErr.Error())
			return
		}
		if _, err = o.db.ExecContext(ctx, `UPDATE report_outbound SET status='delivered',report_id=?,digest=?,error='' WHERE request_id=? AND status='pending'`, report.ID, report.Digest, q.RequestID); err != nil {
			return
		}
		o.notifyReportDelivered(q.TaskID, q.Attempt)
	}
}
