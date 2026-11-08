package coord

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"
)

type TaskFilter struct {
	Status   string `json:"status"`
	Kind     string `json:"kind"`
	Assignee string `json:"assignee"`
	Query    string `json:"query"`
	Cursor   string `json:"cursor"`
	Limit    int    `json:"limit"`
}
type TaskPage struct {
	Tasks      []Task `json:"tasks"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// SearchTasks uses a stable, project-scoped keyset cursor; later pages do not
// repeat earlier records when new tasks arrive.
func (s *Store) SearchTasks(ctx context.Context, project, token string, f TaskFilter) (TaskPage, error) {
	page := TaskPage{Tasks: []Task{}}
	if f.Limit < 1 || f.Limit > 200 {
		return page, errors.New("invalid limit")
	}
	if len(f.Query) > 256 || !utf8.ValidString(f.Query) {
		return page, errors.New("invalid task search query")
	}
	f.Query = strings.TrimSpace(f.Query)
	switch f.Status {
	case "", "ready", "running", "blocked", "submitted", "accepted", "needs_replan", "cancelled":
	default:
		return page, errors.New("invalid status")
	}
	switch f.Kind {
	case "", "research", "design", "implementation", "review", "verification", "integration":
	default:
		return page, errors.New("invalid task kind")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if _, err = authorizeProjectRead(ctx, tx, project, token); err != nil {
		return page, err
	}
	var cursorRowID int64
	if f.Cursor != "" {
		err = tx.QueryRowContext(ctx, `SELECT rowid FROM tasks WHERE project_id=? AND id=?`, project, f.Cursor).Scan(&cursorRowID)
		if errors.Is(err, sql.ErrNoRows) {
			return page, ErrNotFound
		}
		if err != nil {
			return page, err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE project_id=?
		AND (?='' OR status=?) AND (?='' OR kind=?)
		AND (?='' OR owner=? OR EXISTS(SELECT 1 FROM task_assignments a WHERE a.task_id=tasks.id AND a.principal_id=?))
		AND (?='' OR instr(lower(id||' '||title||' '||description),lower(?))>0)
		AND (?='' OR rowid<?)
		ORDER BY rowid DESC LIMIT ?`, project, f.Status, f.Status, f.Kind, f.Kind, f.Assignee, f.Assignee, f.Assignee, f.Query, f.Query, f.Cursor, cursorRowID, f.Limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return page, err
		}
		page.Tasks = append(page.Tasks, task)
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	if len(page.Tasks) > f.Limit {
		page.Tasks = page.Tasks[:f.Limit]
		page.NextCursor = page.Tasks[len(page.Tasks)-1].ID
	}
	return page, nil
}

func (s *Store) TaskCounts(ctx context.Context, project, token string) (map[string]int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = authorizeProjectRead(ctx, tx, project, token); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT status,COUNT(*) FROM tasks WHERE project_id=? GROUP BY status`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var status string
		var count int
		if err = rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		counts[status] = count
	}
	return counts, rows.Err()
}
