package coord

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// EventCheckpoint returns the last explicitly acknowledged project event for
// this principal. It is shared by all sessions using that credential.
func (s *Store) EventCheckpoint(ctx context.Context, project, token string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	actor, err := authorizeProjectRead(ctx, tx, project, token)
	if err != nil {
		return 0, err
	}
	var seq int64
	err = tx.QueryRowContext(ctx, `SELECT sequence FROM event_checkpoints WHERE project_id=? AND principal_id=?`, project, actor.ID).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return seq, err
}

// AcknowledgeEvents advances the checkpoint monotonically after the caller has
// processed events through sequence. A sequence must exist in this project.
func (s *Store) AcknowledgeEvents(ctx context.Context, project, token string, sequence int64) (int64, error) {
	if sequence <= 0 {
		return 0, errors.New("event sequence must be positive")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	actor, err := authorizeProjectRead(ctx, tx, project, token)
	if err != nil {
		return 0, err
	}
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM events e WHERE e.project_id=? AND e.seq=?
		AND (?='owner' OR e.kind NOT LIKE 'certificate.%')
		AND (e.kind NOT IN ('message.sent','message.acknowledged') OR EXISTS (
			SELECT 1 FROM messages m WHERE m.id=e.entity_id AND m.project_id=e.project_id
			AND (m.recipient IS NULL OR m.recipient=? OR m.sender=?)))`, project, sequence, actor.Role, actor.ID, actor.ID).Scan(&exists); err != nil {
		return 0, err
	}
	if exists == 0 {
		return 0, ErrNotFound
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO event_checkpoints(project_id,principal_id,sequence,updated_at) VALUES(?,?,?,?) ON CONFLICT(project_id,principal_id) DO UPDATE SET sequence=MAX(sequence,excluded.sequence),updated_at=CASE WHEN excluded.sequence>sequence THEN excluded.updated_at ELSE updated_at END`, project, actor.ID, sequence, time.Now().UnixMilli())
	if err != nil {
		return 0, err
	}
	var current int64
	if err = tx.QueryRowContext(ctx, `SELECT sequence FROM event_checkpoints WHERE project_id=? AND principal_id=?`, project, actor.ID).Scan(&current); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return current, nil
}
