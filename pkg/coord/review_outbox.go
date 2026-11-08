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

// QueuedReviewRequest is a durable, project-bound request to notify the
// orchestrator that a worker is ready for its planned review.
type QueuedReviewRequest struct {
	RequestID string `json:"request_id"`
	TaskID    string `json:"task_id"`
	Attempt   int    `json:"attempt"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

func (o *Outbox) EnqueueReview(ctx context.Context, task string, attempt int) (QueuedReviewRequest, error) {
	var queued QueuedReviewRequest
	if strings.TrimSpace(task) == "" || len(task) > 256 || attempt < 1 || !utf8.ValidString(task) {
		return queued, errors.New("invalid review request")
	}
	idHash := sha256.Sum256([]byte(task + "\x00" + strconv.Itoa(attempt)))
	id := "review_" + hex.EncodeToString(idHash[:16])
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return queued, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `SELECT request_id,task_id,attempt,status,error,created_at FROM review_outbound WHERE task_id=? AND attempt=?`, task, attempt).Scan(&queued.RequestID, &queued.TaskID, &queued.Attempt, &queued.Status, &queued.Error, &queued.CreatedAt)
	if err == nil {
		return queued, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return queued, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM review_outbound WHERE status!='delivered'`).Scan(&count); err != nil {
		return queued, err
	}
	if count >= 256 {
		return queued, errors.New("review request outbox full; remove delivered or failed entries")
	}
	queued = QueuedReviewRequest{RequestID: id, TaskID: task, Attempt: attempt, Status: "pending", CreatedAt: time.Now().UnixMilli()}
	if _, err = tx.ExecContext(ctx, `INSERT INTO review_outbound(request_id,task_id,attempt,status,created_at) VALUES(?,?,?,'pending',?)`, id, task, attempt, queued.CreatedAt); err != nil {
		return QueuedReviewRequest{}, err
	}
	if err = tx.Commit(); err != nil {
		return QueuedReviewRequest{}, err
	}
	o.signal()
	return queued, nil
}

func (o *Outbox) ListReviewQueue(ctx context.Context, limit int) ([]QueuedReviewRequest, error) {
	if limit < 1 || limit > 200 {
		return nil, errors.New("invalid limit")
	}
	rows, err := o.db.QueryContext(ctx, `SELECT request_id,task_id,attempt,status,error,created_at FROM review_outbound ORDER BY created_at DESC,request_id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []QueuedReviewRequest{}
	for rows.Next() {
		var item QueuedReviewRequest
		if err = rows.Scan(&item.RequestID, &item.TaskID, &item.Attempt, &item.Status, &item.Error, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (o *Outbox) RetryReview(ctx context.Context, id string) error {
	result, err := o.db.ExecContext(ctx, `UPDATE review_outbound SET status='pending',error='',retry_at=0 WHERE request_id=? AND status='failed'`, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	o.signal()
	return nil
}

func (o *Outbox) DiscardReview(ctx context.Context, id string) error {
	result, err := o.db.ExecContext(ctx, `DELETE FROM review_outbound WHERE request_id=? AND status='failed'`, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

func (o *Outbox) PruneDeliveredReviews(ctx context.Context) (int64, error) {
	result, err := o.db.ExecContext(ctx, `DELETE FROM review_outbound WHERE status='delivered'`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (o *Outbox) deliverReviews(ctx context.Context, backend Backend, project, token string) {
	for ctx.Err() == nil {
		var item QueuedReviewRequest
		err := o.db.QueryRowContext(ctx, `SELECT request_id,task_id,attempt FROM review_outbound WHERE status='pending' AND retry_at<=? ORDER BY created_at,request_id LIMIT 1`, time.Now().UnixMilli()).Scan(&item.RequestID, &item.TaskID, &item.Attempt)
		if err != nil {
			return
		}
		request, cancel := context.WithTimeout(ctx, 20*time.Second)
		err = backend.RequestReview(request, project, token, item.TaskID, item.Attempt)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			if errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
				if _, e := o.db.ExecContext(ctx, `UPDATE review_outbound SET status='failed',error=? WHERE request_id=? AND status='pending'`, err.Error(), item.RequestID); e != nil {
					return
				}
				continue
			}
			o.deferOutbound(ctx, "review_outbound", item.RequestID, err.Error())
			return
		}
		if _, err = o.db.ExecContext(ctx, `UPDATE review_outbound SET status='delivered',error='' WHERE request_id=? AND status='pending'`, item.RequestID); err != nil {
			return
		}
	}
}
