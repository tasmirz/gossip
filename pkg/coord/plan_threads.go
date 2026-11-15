package coord

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// PlanThread is an independent project plan. Revisions are selected only
// within one thread; a completed plan may remain alongside a newer plan.
type PlanThread struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	LatestRevision int    `json:"latest_revision"`
	Status         string `json:"status"`
	CreatedAt      int64  `json:"created_at"`
}

func planDisplayTitle(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#"))
		if line != "" {
			if len(line) > 160 {
				line = line[:160]
			}
			return line
		}
	}
	return "Untitled plan"
}

// ListPlanThreads returns independent plans, with the coordinator's current
// plan first and the remaining plans ordered by creation time.
func (s *Store) ListPlanThreads(ctx context.Context, project, token string) ([]PlanThread, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = authorizeProjectRead(ctx, tx, project, token); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT t.id,t.title,COALESCE(MAX(p.revision),0),
	 CASE WHEN EXISTS(SELECT 1 FROM plan_completions c JOIN plans cp ON cp.project_id=c.project_id AND cp.revision=c.revision WHERE cp.plan_id=t.id AND cp.revision=(SELECT MAX(last.revision) FROM plans last WHERE last.project_id=t.project_id AND last.plan_id=t.id)) THEN 'done'
	 ELSE 'active' END,
	 t.created_at FROM plan_threads t LEFT JOIN plans p ON p.project_id=t.project_id AND p.plan_id=t.id
	 WHERE t.project_id=? GROUP BY t.id ORDER BY (SELECT plan_revision FROM projects WHERE id=t.project_id)=MAX(p.revision) DESC,t.created_at DESC`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PlanThread{}
	for rows.Next() {
		var item PlanThread
		if err = rows.Scan(&item.ID, &item.Title, &item.LatestRevision, &item.Status, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// CreatePlan starts a new, independent plan thread and its first revision.
// Tasks from other plans are intentionally untouched.
func (s *Store) CreatePlan(ctx context.Context, project, token, title, body string, expectedCurrent int) (Plan, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = planDisplayTitle(body)
	}
	if len(title) > 160 || strings.TrimSpace(body) == "" || len(body) > 256*1024 {
		return Plan{}, errors.New("provide a plan title and body (maximum 256 KiB)")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Plan{}, err
	}
	defer tx.Rollback()
	actor, err := authorizePermission(ctx, tx, project, token, PermissionPlanEdit)
	if err != nil {
		return Plan{}, err
	}
	var current int
	if err = tx.QueryRowContext(ctx, `SELECT plan_revision FROM projects WHERE id=?`, project).Scan(&current); err != nil {
		return Plan{}, err
	}
	if current != expectedCurrent {
		return Plan{}, ErrConflict
	}
	var seq int
	if err = tx.QueryRowContext(ctx, `SELECT plan_sequence+1 FROM projects WHERE id=?`, project).Scan(&seq); err != nil {
		return Plan{}, err
	}
	threadID, err := randomID("plan_")
	if err != nil {
		return Plan{}, err
	}
	now := time.Now().UnixMilli()
	if _, err = tx.ExecContext(ctx, `INSERT INTO plan_threads(id,project_id,title,created_at,created_by) VALUES(?,?,?,?,?)`, threadID, project, title, now, actor.ID); err != nil {
		return Plan{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO plans(project_id,revision,body,author,created_at,plan_id) VALUES(?,?,?,?,?,?)`, project, seq, body, actor.ID, now, threadID); err != nil {
		return Plan{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE projects SET plan_revision=?,plan_sequence=? WHERE id=? AND plan_revision=?`, seq, seq, project, current); err != nil {
		return Plan{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO plan_acknowledgments(project_id,principal_id,revision,acknowledged_at) VALUES(?,?,?,?) ON CONFLICT(project_id,principal_id) DO UPDATE SET revision=excluded.revision,acknowledged_at=excluded.acknowledged_at`, project, actor.ID, seq, now); err != nil {
		return Plan{}, err
	}
	if err = event(ctx, tx, project, actor.ID, "plan.created", threadID, map[string]any{"revision": seq, "title": title}); err != nil {
		return Plan{}, err
	}
	if err = tx.Commit(); err != nil {
		return Plan{}, err
	}
	return Plan{PlanID: threadID, PlanTitle: title, Revision: seq, Body: body, Author: actor.ID, CreatedAt: now, Status: "active", Acknowledged: true}, nil
}

// ThreadForRevision resolves the owning plan for a project-scoped revision.
func (s *Store) ThreadForRevision(ctx context.Context, project string, revision int) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT plan_id FROM plans WHERE project_id=? AND revision=?`, project, revision).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}
