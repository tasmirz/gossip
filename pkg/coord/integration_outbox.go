package coord

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxIntegrationOutboxEntries = 100
	maxIntegrationOutboxBytes   = 32 * 1024 * 1024
)

type QueuedIntegration struct {
	RequestID        string `json:"request_id"`
	TaskID           string `json:"task_id"`
	Attempt          int    `json:"attempt"`
	CombinedRevision string `json:"combined_revision"`
	Status           string `json:"status"`
	IntegrationID    string `json:"integration_id,omitempty"`
	Error            string `json:"error,omitempty"`
	CreatedAt        int64  `json:"created_at"`
}

func (o *Outbox) EnqueueIntegration(ctx context.Context, submission IntegrationSubmission) (QueuedIntegration, error) {
	var queued QueuedIntegration
	if strings.TrimSpace(submission.TaskID) == "" || submission.Attempt < 1 || strings.TrimSpace(submission.WorkspaceID) == "" || len(submission.CandidateIDs) < 2 || len(submission.CandidateIDs) > 50 || len(submission.Checks) == 0 || len(submission.Checks) > 100 || strings.TrimSpace(submission.Summary) == "" || len(submission.Summary) > 64*1024 {
		return queued, errors.New("invalid integration submission")
	}
	if submission.RequestID == "" {
		submission.RequestID = stableIntegrationRequestID(submission.TaskID, submission.Attempt)
	}
	if len(submission.RequestID) > 128 || !utf8.ValidString(submission.RequestID) || !validRevision(submission.BaseRevision) || !validRevision(submission.CombinedRevision) || submission.Observation.Head != submission.CombinedRevision || submission.Observation.Dirty || strings.TrimSpace(submission.Observation.Branch) == "" || len(submission.Observation.Branch) > 512 {
		return queued, errors.New("integration revision observation or request ID is invalid")
	}
	seen := map[string]bool{}
	for _, candidateID := range submission.CandidateIDs {
		if candidateID == "" || len(candidateID) > 256 || seen[candidateID] {
			return queued, errors.New("candidate IDs must be unique and non-empty")
		}
		seen[candidateID] = true
	}
	for _, check := range submission.Checks {
		if strings.TrimSpace(check.Name) == "" || strings.TrimSpace(check.Command) == "" || check.Revision != submission.CombinedRevision || len(check.Name) > 256 || len(check.Command) > 2048 || len(check.Evidence) > 16*1024 {
			return queued, errors.New("checks must be bounded and tied to the combined revision")
		}
	}
	submission.Observation.Root = ""
	submission.Observation.CommonDirFingerprint = ""
	submission.Observation.RemoteHints = nil
	payload, err := json.Marshal(submission)
	if err != nil {
		return queued, err
	}
	if len(payload) > 1024*1024 {
		return queued, errors.New("integration submission exceeds the outbox size limit")
	}
	digest := sha256.Sum256(payload)
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return queued, err
	}
	defer tx.Rollback()
	var priorPayload []byte
	err = tx.QueryRowContext(ctx, `SELECT request_id,payload_json,status,integration_id,error,created_at FROM integration_outbound WHERE request_id=?`, submission.RequestID).Scan(&queued.RequestID, &priorPayload, &queued.Status, &queued.IntegrationID, &queued.Error, &queued.CreatedAt)
	if err == nil {
		priorDigest := sha256.Sum256(priorPayload)
		if priorDigest != digest {
			return QueuedIntegration{}, ErrConflict
		}
		queued.TaskID, queued.Attempt, queued.CombinedRevision = submission.TaskID, submission.Attempt, submission.CombinedRevision
		return queued, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return queued, err
	}
	var priorID string
	err = tx.QueryRowContext(ctx, `SELECT request_id,payload_json,status,integration_id,error,created_at FROM integration_outbound WHERE task_id=? AND attempt=?`, submission.TaskID, submission.Attempt).Scan(&priorID, &priorPayload, &queued.Status, &queued.IntegrationID, &queued.Error, &queued.CreatedAt)
	if err == nil {
		priorDigest := sha256.Sum256(priorPayload)
		if priorDigest != digest {
			return QueuedIntegration{}, ErrConflict
		}
		queued.RequestID, queued.TaskID, queued.Attempt, queued.CombinedRevision = priorID, submission.TaskID, submission.Attempt, submission.CombinedRevision
		return queued, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return queued, err
	}
	var count int
	var bytes int64
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(payload_json)),0) FROM integration_outbound WHERE status!='delivered'`).Scan(&count, &bytes); err != nil {
		return queued, err
	}
	if count >= maxIntegrationOutboxEntries {
		return queued, errors.New("integration outbox full; remove delivered or failed entries")
	}
	if bytes+int64(len(payload)) > maxIntegrationOutboxBytes {
		return queued, errors.New("integration outbox byte limit reached; prune delivered or discard failed entries")
	}
	queued = QueuedIntegration{RequestID: submission.RequestID, TaskID: submission.TaskID, Attempt: submission.Attempt, CombinedRevision: submission.CombinedRevision, Status: "pending", CreatedAt: time.Now().UnixMilli()}
	if _, err = tx.ExecContext(ctx, `INSERT INTO integration_outbound(request_id,task_id,attempt,combined_revision,payload_json,status,created_at) VALUES(?,?,?,?,?,'pending',?)`, queued.RequestID, queued.TaskID, queued.Attempt, queued.CombinedRevision, string(payload), queued.CreatedAt); err != nil {
		return QueuedIntegration{}, err
	}
	if err = tx.Commit(); err != nil {
		return QueuedIntegration{}, err
	}
	o.signal()
	return queued, nil
}

func (o *Outbox) ListIntegrationQueue(ctx context.Context, limit int) ([]QueuedIntegration, error) {
	if limit < 1 || limit > 200 {
		return nil, errors.New("invalid limit")
	}
	rows, err := o.db.QueryContext(ctx, `SELECT request_id,task_id,attempt,combined_revision,status,integration_id,error,created_at FROM integration_outbound ORDER BY created_at DESC,request_id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []QueuedIntegration{}
	for rows.Next() {
		var item QueuedIntegration
		if err = rows.Scan(&item.RequestID, &item.TaskID, &item.Attempt, &item.CombinedRevision, &item.Status, &item.IntegrationID, &item.Error, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (o *Outbox) RetryIntegration(ctx context.Context, id string) error {
	res, err := o.db.ExecContext(ctx, `UPDATE integration_outbound SET status='pending',error='',retry_at=0 WHERE request_id=? AND status='failed'`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	o.signal()
	return nil
}

func (o *Outbox) DiscardIntegration(ctx context.Context, id string) error {
	res, err := o.db.ExecContext(ctx, `DELETE FROM integration_outbound WHERE request_id=? AND status='failed'`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

func (o *Outbox) PruneIntegrations(ctx context.Context) (int64, error) {
	res, err := o.db.ExecContext(ctx, `DELETE FROM integration_outbound WHERE status='delivered'`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (o *Outbox) deliverIntegrations(ctx context.Context, backend Backend, project, token string) {
	for ctx.Err() == nil {
		var item QueuedIntegration
		var raw string
		err := o.db.QueryRowContext(ctx, `SELECT request_id,task_id,attempt,payload_json FROM integration_outbound WHERE status='pending' AND retry_at<=? ORDER BY created_at,request_id LIMIT 1`, time.Now().UnixMilli()).Scan(&item.RequestID, &item.TaskID, &item.Attempt, &raw)
		if err != nil {
			return
		}
		var submission IntegrationSubmission
		if err = json.Unmarshal([]byte(raw), &submission); err != nil {
			_, _ = o.db.ExecContext(ctx, `UPDATE integration_outbound SET status='failed',error=? WHERE request_id=? AND status='pending'`, err.Error(), item.RequestID)
			continue
		}
		if submission.RequestID != item.RequestID || submission.TaskID != item.TaskID || submission.Attempt != item.Attempt {
			_, _ = o.db.ExecContext(ctx, `UPDATE integration_outbound SET status='failed',error='stored integration payload does not match its queue key' WHERE request_id=? AND status='pending'`, item.RequestID)
			continue
		}
		request, cancel := context.WithTimeout(ctx, 30*time.Second)
		record, deliverErr := backend.RecordIntegration(request, project, token, submission)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if deliverErr != nil {
			if errors.Is(deliverErr, ErrUnauthorized) || errors.Is(deliverErr, ErrConflict) || errors.Is(deliverErr, ErrNotFound) {
				_, _ = o.db.ExecContext(ctx, `UPDATE integration_outbound SET status='failed',error=? WHERE request_id=? AND status='pending'`, deliverErr.Error(), item.RequestID)
				continue
			}
			o.deferOutbound(ctx, "integration_outbound", item.RequestID, deliverErr.Error())
			return
		}
		if _, err = o.db.ExecContext(ctx, `UPDATE integration_outbound SET status='delivered',integration_id=?,error='' WHERE request_id=? AND status='pending'`, record.ID, item.RequestID); err != nil {
			return
		}
	}
}
