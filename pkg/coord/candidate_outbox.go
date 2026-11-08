package coord

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxCandidateOutboxEntries = 100
	maxCandidateOutboxBytes   = 32 * 1024 * 1024
)

type QueuedCandidate struct {
	RequestID   string `json:"request_id"`
	TaskID      string `json:"task_id"`
	Attempt     int    `json:"attempt"`
	Baseline    string `json:"baseline"`
	Body        string `json:"body,omitempty"`
	Status      string `json:"status"`
	CandidateID string `json:"candidate_id,omitempty"`
	Error       string `json:"error,omitempty"`
	CreatedAt   int64  `json:"created_at"`
}

func (o *Outbox) EnqueueCandidate(ctx context.Context, task string, attempt int, baseline, body, id string) (QueuedCandidate, error) {
	var c QueuedCandidate
	if task == "" || len(task) > 256 || attempt < 1 || strings.TrimSpace(baseline) == "" || len(baseline) > 4096 || strings.TrimSpace(body) == "" || len(body) > 1024*1024 || len(id) > 128 || !utf8.ValidString(task) || !utf8.ValidString(baseline) || !utf8.ValidString(body) || !utf8.ValidString(id) {
		return c, errors.New("invalid candidate")
	}
	if id == "" {
		var err error
		id, err = randomID("candidate_request_")
		if err != nil {
			return c, err
		}
	}
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return c, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `SELECT request_id,task_id,attempt,baseline,body,status,candidate_id,error,created_at FROM candidate_outbound WHERE request_id=?`, id).Scan(&c.RequestID, &c.TaskID, &c.Attempt, &c.Baseline, &c.Body, &c.Status, &c.CandidateID, &c.Error, &c.CreatedAt)
	if err == nil {
		if c.TaskID != task || c.Attempt != attempt || c.Baseline != baseline || c.Body != body {
			return QueuedCandidate{}, ErrConflict
		}
		return c, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return c, err
	}
	var n int
	var queuedBytes int64
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(body)),0) FROM candidate_outbound`).Scan(&n, &queuedBytes); err != nil {
		return c, err
	}
	if n >= maxCandidateOutboxEntries {
		return c, errors.New("candidate outbox full; remove delivered entries")
	}
	if queuedBytes+int64(len(body)) > maxCandidateOutboxBytes {
		return c, errors.New("candidate outbox byte limit reached; remove delivered or discard failed entries")
	}
	c = QueuedCandidate{RequestID: id, TaskID: task, Attempt: attempt, Baseline: baseline, Body: body, Status: "pending", CreatedAt: time.Now().UnixMilli()}
	_, err = tx.ExecContext(ctx, `INSERT INTO candidate_outbound(request_id,task_id,attempt,baseline,body,status,created_at) VALUES(?,?,?,?,?,'pending',?)`, id, task, attempt, baseline, body, c.CreatedAt)
	if err != nil {
		return QueuedCandidate{}, err
	}
	if err = tx.Commit(); err != nil {
		return QueuedCandidate{}, err
	}
	o.signal()
	return c, nil
}
func (o *Outbox) ListCandidateQueue(ctx context.Context, limit int) ([]QueuedCandidate, error) {
	if limit < 1 || limit > 200 {
		return nil, errors.New("invalid limit")
	}
	rows, err := o.db.QueryContext(ctx, `SELECT request_id,task_id,attempt,baseline,status,candidate_id,error,created_at FROM candidate_outbound ORDER BY created_at DESC,request_id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []QueuedCandidate{}
	for rows.Next() {
		var c QueuedCandidate
		if err = rows.Scan(&c.RequestID, &c.TaskID, &c.Attempt, &c.Baseline, &c.Status, &c.CandidateID, &c.Error, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (o *Outbox) RetryCandidate(ctx context.Context, id string) error {
	r, err := o.db.ExecContext(ctx, `UPDATE candidate_outbound SET status='pending',error='' WHERE request_id=? AND status='failed'`, id)
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

// DiscardCandidate removes a permanently failed candidate and its local report
// body. Pending work cannot be discarded while a delivery outcome is unknown.
func (o *Outbox) DiscardCandidate(ctx context.Context, id string) error {
	result, err := o.db.ExecContext(ctx, `DELETE FROM candidate_outbound WHERE request_id=? AND status='failed'`, id)
	if err != nil {
		return err
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if removed != 1 {
		return ErrConflict
	}
	return nil
}

func (o *Outbox) PruneCandidates(ctx context.Context) (int64, error) {
	r, err := o.db.ExecContext(ctx, `DELETE FROM candidate_outbound WHERE status='delivered'`)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}
func (o *Outbox) deliverCandidates(ctx context.Context, b Backend, project, token string) {
	for ctx.Err() == nil {
		var c QueuedCandidate
		err := o.db.QueryRowContext(ctx, `SELECT request_id,task_id,attempt,baseline,body FROM candidate_outbound WHERE status='pending' AND retry_at<=? ORDER BY created_at,request_id LIMIT 1`, time.Now().UnixMilli()).Scan(&c.RequestID, &c.TaskID, &c.Attempt, &c.Baseline, &c.Body)
		if err != nil {
			return
		}
		request, cancel := context.WithTimeout(ctx, 20*time.Second)
		saved, err := b.SaveCandidateWithID(request, project, token, c.TaskID, c.Attempt, c.Baseline, c.Body, c.RequestID)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			if errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
				if _, e := o.db.ExecContext(ctx, `UPDATE candidate_outbound SET status='failed',error=? WHERE request_id=? AND status='pending'`, err.Error(), c.RequestID); e != nil {
					return
				}
				continue
			}
			o.deferOutbound(ctx, "candidate_outbound", c.RequestID, err.Error())
			return
		}
		if _, err = o.db.ExecContext(ctx, `UPDATE candidate_outbound SET status='delivered',candidate_id=?,error='' WHERE request_id=? AND status='pending'`, saved.ID, c.RequestID); err != nil {
			return
		}
	}
}
