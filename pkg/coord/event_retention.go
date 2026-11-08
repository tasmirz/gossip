package coord

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// EventRetention describes an explicit owner-directed event history prune.
// Project state (plans, tasks, messages, reports, and pending work) is retained;
// replicas behind PrunedThrough must replace cached event history from a sync
// snapshot before continuing from the retained event stream.
type EventRetention struct {
	ProjectID        string `json:"project_id"`
	PrunedThrough    int64  `json:"pruned_through"`
	PrunedCount      int64  `json:"pruned_count"`
	SnapshotSequence int64  `json:"snapshot_sequence"`
	UpdatedAt        int64  `json:"updated_at"`
}

func (s *Store) EventRetentionStatus(ctx context.Context, project, token string) (EventRetention, error) {
	var status EventRetention
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return status, err
	}
	defer tx.Rollback()
	if _, err = authorizePermission(ctx, tx, project, token, PermissionProjectRead); err != nil {
		return status, err
	}
	status.ProjectID = project
	err = tx.QueryRowContext(ctx, `SELECT pruned_through,updated_at FROM event_retention WHERE project_id=?`, project).Scan(&status.PrunedThrough, &status.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return EventRetention{}, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM events WHERE project_id=?`, project).Scan(&status.SnapshotSequence); err != nil {
		return EventRetention{}, err
	}
	return status, tx.Commit()
}

// PruneProjectEvents removes only event rows through an explicit project-local
// sequence. Owners are the only callers allowed to destroy history. The
// retention watermark and audit event commit with the deletion so reconnecting
// replicas can detect the gap and rebuild snapshots. Other project records and
// local outbound queues are untouched.
func (s *Store) PruneProjectEvents(ctx context.Context, project, ownerToken string, through int64) (EventRetention, error) {
	var status EventRetention
	if through <= 0 {
		return status, errors.New("prune sequence must be positive")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return status, err
	}
	defer tx.Rollback()
	actor, err := authenticate(ctx, tx, project, ownerToken, "owner")
	if err != nil {
		return status, err
	}
	var current, maxSequence int64
	err = tx.QueryRowContext(ctx, `SELECT pruned_through FROM event_retention WHERE project_id=?`, project).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return status, err
	}
	if through < current {
		return status, ErrConflict
	}
	if through == current && current != 0 {
		status = EventRetention{ProjectID: project, PrunedThrough: current}
		if err = tx.QueryRowContext(ctx, `SELECT updated_at FROM event_retention WHERE project_id=?`, project).Scan(&status.UpdatedAt); err != nil {
			return EventRetention{}, err
		}
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM events WHERE project_id=?`, project).Scan(&status.SnapshotSequence); err != nil {
			return EventRetention{}, err
		}
		return status, tx.Commit()
	}
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM events WHERE project_id=?`, project).Scan(&maxSequence); err != nil {
		return status, err
	}
	if through > maxSequence {
		return status, errors.New("prune sequence is ahead of this project's event history")
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM events WHERE project_id=? AND seq<=?`, project, through)
	if err != nil {
		return status, err
	}
	status.PrunedCount, err = res.RowsAffected()
	if err != nil {
		return status, err
	}
	status.ProjectID = project
	status.PrunedThrough = through
	status.UpdatedAt = time.Now().UnixMilli()
	if _, err = tx.ExecContext(ctx, `INSERT INTO event_retention(project_id,pruned_through,updated_at,pruned_by) VALUES(?,?,?,?) ON CONFLICT(project_id) DO UPDATE SET pruned_through=excluded.pruned_through,updated_at=excluded.updated_at,pruned_by=excluded.pruned_by`, project, through, status.UpdatedAt, actor.ID); err != nil {
		return EventRetention{}, err
	}
	if err = event(ctx, tx, project, actor.ID, "events.pruned", project, map[string]any{"pruned_through": through, "pruned_count": status.PrunedCount}); err != nil {
		return EventRetention{}, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM events WHERE project_id=?`, project).Scan(&status.SnapshotSequence); err != nil {
		return EventRetention{}, err
	}
	if err = tx.Commit(); err != nil {
		return EventRetention{}, err
	}
	return status, nil
}
