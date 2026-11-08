package coord

import (
	"context"
	"errors"
	"strings"
	"time"
)

// TransitionTask records a blocker, resolves one, or cancels work. Blocking or
// cancelling fences the current attempt; a resumed worker must claim anew.
func (s *Store) TransitionTask(ctx context.Context, project, token, id, action, reason string, attempt int) (Task, error) {
	if len(reason) > 64*1024 || ((action == "block" || action == "cancel") && strings.TrimSpace(reason) == "") {
		return Task{}, errors.New("a bounded reason is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	permission := PermissionTaskDelegate
	if action == "block" {
		permission = PermissionTaskClaim
	}
	actor, err := authorizePermission(ctx, tx, project, token, permission)
	if err != nil {
		return Task{}, err
	}
	now := time.Now().UnixMilli()
	switch action {
	case "block":
		res, err := tx.ExecContext(ctx, `UPDATE tasks SET status='blocked',owner=NULL,lease_until=0 WHERE id=? AND project_id=? AND status='running' AND owner=? AND attempt=? AND lease_until>? AND plan_revision=(SELECT plan_revision FROM projects WHERE id=?)`, id, project, actor.ID, attempt, now, project)
		if err != nil {
			return Task{}, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return Task{}, err
		}
		if n != 1 {
			return Task{}, ErrConflict
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO task_blockers(task_id,reason,reported_by,attempt,created_at) VALUES(?,?,?,?,?) ON CONFLICT(task_id) DO UPDATE SET reason=excluded.reason,reported_by=excluded.reported_by,attempt=excluded.attempt,created_at=excluded.created_at`, id, reason, actor.ID, attempt, now); err != nil {
			return Task{}, err
		}
	case "resolve":
		res, err := tx.ExecContext(ctx, `DELETE FROM task_blockers WHERE task_id=? AND EXISTS(SELECT 1 FROM tasks WHERE id=? AND project_id=? AND status='blocked' AND plan_revision=(SELECT plan_revision FROM projects WHERE id=?))`, id, id, project, project)
		if err != nil {
			return Task{}, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return Task{}, err
		}
		if n != 1 {
			return Task{}, ErrConflict
		}
		if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status=CASE WHEN EXISTS(SELECT 1 FROM dependencies d JOIN tasks p ON p.id=d.prerequisite_id WHERE d.task_id=tasks.id AND p.status<>'accepted') THEN 'blocked' ELSE 'ready' END WHERE id=? AND project_id=?`, id, project); err != nil {
			return Task{}, err
		}
	case "cancel":
		res, err := tx.ExecContext(ctx, `UPDATE tasks SET status='cancelled',owner=NULL,lease_until=0 WHERE id=? AND project_id=? AND status IN ('ready','blocked','running','submitted','needs_replan')`, id, project)
		if err != nil {
			return Task{}, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return Task{}, err
		}
		if n != 1 {
			return Task{}, ErrConflict
		}
	default:
		return Task{}, errors.New("unknown task transition")
	}
	if err = event(ctx, tx, project, actor.ID, "task."+action, id, map[string]any{"reason": reason, "attempt": attempt}); err != nil {
		return Task{}, err
	}
	task, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id=? AND project_id=?`, id, project))
	if err != nil {
		return Task{}, err
	}
	if err = tx.Commit(); err != nil {
		return Task{}, err
	}
	return task, nil
}
