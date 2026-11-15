package coord

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// DeletePlanRevision removes a plan version and its exclusive task work. Tasks
// carried forward into another version retain their identity and all reports.
func (s *Store) DeletePlanRevision(ctx context.Context, project, token string, revision, expectedCurrent int) error {
	if revision < 1 || expectedCurrent < 1 {
		return fmt.Errorf("revision and expected current revision are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := authorizePermission(ctx, tx, project, token, PermissionPlanEdit)
	if err != nil {
		return err
	}
	var current int
	if err = tx.QueryRowContext(ctx, `SELECT plan_revision FROM projects WHERE id=?`, project).Scan(&current); err != nil {
		return err
	}
	if current != expectedCurrent {
		return ErrConflict
	}
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT 1 FROM plans WHERE project_id=? AND revision=?`, project, revision).Scan(&exists); err == sql.ErrNoRows {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM task_plan_links WHERE project_id=? AND revision=?`, project, revision); err != nil {
		return err
	}
	var next int
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision),0) FROM plans WHERE project_id=? AND revision<>?`, project, revision).Scan(&next); err != nil {
		return err
	}
	if revision == current {
		if _, err = tx.ExecContext(ctx, `UPDATE projects SET plan_revision=? WHERE id=? AND plan_revision=?`, next, project, current); err != nil {
			return err
		}
	}
	// Tasks with another version link stay in the project. Point their workflow
	// metadata at the latest retained version so the plan FK remains valid.
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET plan_revision=(SELECT MAX(l.revision) FROM task_plan_links l WHERE l.task_id=tasks.id)
		WHERE project_id=? AND EXISTS(SELECT 1 FROM task_plan_links l WHERE l.task_id=tasks.id)`, project); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM tasks WHERE project_id=? AND NOT EXISTS(SELECT 1 FROM task_plan_links l WHERE l.task_id=tasks.id)`, project)
	if err != nil {
		return err
	}
	var orphanIDs []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		orphanIDs = append(orphanIDs, id)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, id := range orphanIDs {
		// Remove leaf records first; event history is deliberately preserved.
		for _, stmt := range []string{
			`DELETE FROM project_context_links WHERE project_id=? AND target_kind='task' AND target_id=?`,
			`DELETE FROM work_roles WHERE project_id=? AND task_id=?`,
			`DELETE FROM report_review_evidence WHERE report_id IN (SELECT id FROM reports WHERE task_id=?) OR candidate_id IN (SELECT id FROM candidate_reports WHERE task_id=?)`,
			`DELETE FROM integration_requests WHERE integration_id IN (SELECT id FROM integration_records WHERE task_id=?)`,
			`DELETE FROM integration_records WHERE task_id=?`,
			`DELETE FROM report_requests WHERE report_id IN (SELECT id FROM reports WHERE task_id=?)`,
			`DELETE FROM candidate_requests WHERE candidate_id IN (SELECT id FROM candidate_reports WHERE task_id=?)`,
			`DELETE FROM review_request_candidates WHERE task_id=?`,
			`DELETE FROM attempt_usage WHERE task_id=?`,
			`DELETE FROM candidate_reports WHERE task_id=?`,
			`DELETE FROM reports WHERE task_id=?`,
			`DELETE FROM task_review_requests WHERE task_id=?`,
			`DELETE FROM task_blockers WHERE task_id=?`,
			`DELETE FROM task_assignments WHERE task_id=?`,
			`DELETE FROM task_requests WHERE task_id=?`,
			`DELETE FROM budget_attempt_holds WHERE task_id=?`,
			`DELETE FROM budget_reservations WHERE task_id=?`,
			`DELETE FROM dependencies WHERE task_id=? OR prerequisite_id=?`,
			`DELETE FROM attempts WHERE task_id=?`,
			`DELETE FROM tasks WHERE project_id=? AND id=?`,
		} {
			var result sql.Result
			switch stmt {
			case `DELETE FROM report_review_evidence WHERE report_id IN (SELECT id FROM reports WHERE task_id=?) OR candidate_id IN (SELECT id FROM candidate_reports WHERE task_id=?)`:
				result, err = tx.ExecContext(ctx, stmt, id, id)
			case `DELETE FROM dependencies WHERE task_id=? OR prerequisite_id=?`:
				result, err = tx.ExecContext(ctx, stmt, id, id)
			case `DELETE FROM tasks WHERE project_id=? AND id=?`:
				result, err = tx.ExecContext(ctx, stmt, project, id)
			default:
				result, err = tx.ExecContext(ctx, stmt, id)
			}
			_ = result
			if err != nil {
				return err
			}
		}
	}
	for _, stmt := range []string{
		`DELETE FROM project_context_links WHERE project_id=? AND target_kind='plan' AND target_id=?`,
		`DELETE FROM plan_acknowledgments WHERE project_id=? AND revision=?`,
		`DELETE FROM plan_variants WHERE project_id=? AND revision=?`,
		`DELETE FROM plan_completions WHERE project_id=? AND revision=?`,
		`DELETE FROM plans WHERE project_id=? AND revision=?`,
	} {
		var arg any = revision
		if stmt == `DELETE FROM project_context_links WHERE project_id=? AND target_kind='plan' AND target_id=?` {
			arg = strconv.Itoa(revision)
		}
		if _, err = tx.ExecContext(ctx, stmt, project, arg); err != nil {
			return err
		}
	}
	if err = event(ctx, tx, project, actor.ID, "plan.deleted", project, map[string]int{"revision": revision, "orphan_tasks_deleted": len(orphanIDs)}); err != nil {
		return err
	}
	return tx.Commit()
}

// DeletePlanThread permanently removes one independent plan and its exclusive
// task work. Tasks also linked to another plan remain and point at their newest
// retained plan revision.
func (s *Store) DeletePlanThread(ctx context.Context, project, token, planID string, expectedCurrent int) error {
	if strings.TrimSpace(planID) == "" || expectedCurrent < 0 {
		return fmt.Errorf("plan and expected current revision are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := authorizePermission(ctx, tx, project, token, PermissionPlanEdit)
	if err != nil {
		return err
	}
	var current int
	if err = tx.QueryRowContext(ctx, `SELECT plan_revision FROM projects WHERE id=?`, project).Scan(&current); err != nil {
		return err
	}
	if current != expectedCurrent {
		return ErrConflict
	}
	var title string
	if err = tx.QueryRowContext(ctx, `SELECT title FROM plan_threads WHERE project_id=? AND id=?`, project, planID).Scan(&title); err == sql.ErrNoRows {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	var targetHasCurrent int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM plans WHERE project_id=? AND plan_id=? AND revision=?)`, project, planID, current).Scan(&targetHasCurrent); err != nil {
		return err
	}
	if targetHasCurrent != 0 {
		var next int
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision),0) FROM plans WHERE project_id=? AND plan_id<>?`, project, planID).Scan(&next); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE projects SET plan_revision=? WHERE id=? AND plan_revision=?`, next, project, current); err != nil {
			return err
		}
	}
	for _, stmt := range []string{
		`DELETE FROM task_plan_links WHERE project_id=? AND revision IN (SELECT revision FROM plans WHERE project_id=? AND plan_id=?)`,
		`DELETE FROM project_context_links WHERE project_id=? AND target_kind='plan' AND target_id IN (SELECT CAST(revision AS TEXT) FROM plans WHERE project_id=? AND plan_id=?)`,
	} {
		if _, err = tx.ExecContext(ctx, stmt, project, project, planID); err != nil {
			return err
		}
	}
	// Re-home tasks that were intentionally shared with another plan before
	// deleting records that became exclusive to this plan.
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET plan_revision=(
		SELECT MAX(l.revision) FROM task_plan_links l WHERE l.project_id=tasks.project_id AND l.task_id=tasks.id)
		WHERE project_id=? AND EXISTS(SELECT 1 FROM task_plan_links l WHERE l.project_id=tasks.project_id AND l.task_id=tasks.id)`, project); err != nil {
		return err
	}
	orphanIDs, err := unlinkedTaskIDs(ctx, tx, project)
	if err != nil {
		return err
	}
	for _, id := range orphanIDs {
		if err = deleteTaskRecords(ctx, tx, project, id); err != nil {
			return err
		}
	}
	for _, stmt := range []string{
		`DELETE FROM plan_acknowledgments WHERE project_id=? AND revision IN (SELECT revision FROM plans WHERE project_id=? AND plan_id=?)`,
		`DELETE FROM plan_variants WHERE project_id=? AND revision IN (SELECT revision FROM plans WHERE project_id=? AND plan_id=?)`,
		`DELETE FROM plan_completions WHERE project_id=? AND revision IN (SELECT revision FROM plans WHERE project_id=? AND plan_id=?)`,
		`DELETE FROM plans WHERE project_id=? AND plan_id=?`,
		`DELETE FROM plan_threads WHERE project_id=? AND id=?`,
	} {
		if strings.Contains(stmt, "plan_id=?") && !strings.Contains(stmt, "revision IN") {
			if _, err = tx.ExecContext(ctx, stmt, project, planID); err != nil {
				return err
			}
		} else if _, err = tx.ExecContext(ctx, stmt, project, project, planID); err != nil {
			return err
		}
	}
	if err = event(ctx, tx, project, actor.ID, "plan.deleted", planID, map[string]any{"title": title, "deleted_tasks": len(orphanIDs)}); err != nil {
		return err
	}
	return tx.Commit()
}

func unlinkedTaskIDs(ctx context.Context, tx *sql.Tx, project string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM tasks WHERE project_id=? AND NOT EXISTS(SELECT 1 FROM task_plan_links l WHERE l.project_id=tasks.project_id AND l.task_id=tasks.id)`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func deleteTaskRecords(ctx context.Context, tx *sql.Tx, project, id string) error {
	for _, stmt := range []string{
		`DELETE FROM project_context_links WHERE project_id=? AND target_kind='task' AND target_id=?`,
		`DELETE FROM work_roles WHERE project_id=? AND task_id=?`,
		`DELETE FROM report_review_evidence WHERE report_id IN (SELECT id FROM reports WHERE task_id=?) OR candidate_id IN (SELECT id FROM candidate_reports WHERE task_id=?)`,
		`DELETE FROM integration_requests WHERE integration_id IN (SELECT id FROM integration_records WHERE task_id=?)`,
		`DELETE FROM integration_records WHERE task_id=?`,
		`DELETE FROM report_requests WHERE report_id IN (SELECT id FROM reports WHERE task_id=?)`,
		`DELETE FROM candidate_requests WHERE candidate_id IN (SELECT id FROM candidate_reports WHERE task_id=?)`,
		`DELETE FROM review_request_candidates WHERE task_id=?`,
		`DELETE FROM attempt_usage WHERE task_id=?`,
		`DELETE FROM candidate_reports WHERE task_id=?`,
		`DELETE FROM reports WHERE task_id=?`,
		`DELETE FROM task_review_requests WHERE task_id=?`,
		`DELETE FROM task_blockers WHERE task_id=?`,
		`DELETE FROM task_assignments WHERE task_id=?`,
		`DELETE FROM task_requests WHERE task_id=?`,
		`DELETE FROM budget_attempt_holds WHERE task_id=?`,
		`DELETE FROM budget_reservations WHERE task_id=?`,
		`DELETE FROM dependencies WHERE task_id=? OR prerequisite_id=?`,
		`DELETE FROM attempts WHERE task_id=?`,
		`DELETE FROM tasks WHERE project_id=? AND id=?`,
	} {
		var err error
		switch stmt {
		case `DELETE FROM report_review_evidence WHERE report_id IN (SELECT id FROM reports WHERE task_id=?) OR candidate_id IN (SELECT id FROM candidate_reports WHERE task_id=?)`:
			_, err = tx.ExecContext(ctx, stmt, id, id)
		case `DELETE FROM dependencies WHERE task_id=? OR prerequisite_id=?`:
			_, err = tx.ExecContext(ctx, stmt, id, id)
		case `DELETE FROM tasks WHERE project_id=? AND id=?`, `DELETE FROM project_context_links WHERE project_id=? AND target_kind='task' AND target_id=?`, `DELETE FROM work_roles WHERE project_id=? AND task_id=?`:
			_, err = tx.ExecContext(ctx, stmt, project, id)
		default:
			_, err = tx.ExecContext(ctx, stmt, id)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
