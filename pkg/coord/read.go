package coord

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// RecentEvents returns the latest project events in chronological order.
func (s *Store) RecentEvents(ctx context.Context, project, token string, limit int) ([]Event, error) {
	if limit < 1 || limit > 200 {
		return nil, errors.New("invalid limit")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	actor, err := authorizeProjectRead(ctx, tx, project, token)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT e.seq,e.actor,e.kind,e.entity_id,e.payload,e.created_at FROM events e WHERE e.project_id=?
		AND (?='owner' OR e.kind NOT LIKE 'certificate.%')
		AND (e.kind NOT IN ('message.sent','message.acknowledged') OR EXISTS (
			SELECT 1 FROM messages m WHERE m.id=e.entity_id AND m.project_id=e.project_id
			AND (m.recipient IS NULL OR m.recipient=? OR m.sender=?)))
		ORDER BY e.seq DESC LIMIT ?`, project, actor.Role, actor.ID, actor.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var payload string
		if err = rows.Scan(&e.Sequence, &e.Actor, &e.Kind, &e.EntityID, &payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Payload = json.RawMessage(payload)
		out = append(out, e)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

type Plan struct {
	Revision     int    `json:"revision"`
	Body         string `json:"body"`
	Author       string `json:"author"`
	CreatedAt    int64  `json:"created_at"`
	Acknowledged bool   `json:"acknowledged"`
}

type Principal struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	ProjectID   string `json:"project_id"`
	ProjectName string `json:"project_name"`
}

// ListPrincipals returns active project members without credential material.
func (s *Store) ListPrincipals(ctx context.Context, project, token string) ([]Principal, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = authorizeProjectRead(ctx, tx, project, token); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT p.id,p.name,p.role,j.id,j.name FROM principals p JOIN projects j ON j.id=p.project_id WHERE p.project_id=? AND p.revoked=0 ORDER BY p.name,p.id`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Principal{}
	for rows.Next() {
		var p Principal
		if err = rows.Scan(&p.ID, &p.Name, &p.Role, &p.ProjectID, &p.ProjectName); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// WhoAmI validates a project-bound credential without returning the token.
func (s *Store) WhoAmI(ctx context.Context, project, token string) (Principal, error) {
	var p Principal
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	id, err := authorizeProjectRead(ctx, tx, project, token)
	if err != nil {
		return p, err
	}
	err = tx.QueryRowContext(ctx, `SELECT p.id,p.name,p.role,j.id,j.name FROM principals p JOIN projects j ON j.id=p.project_id WHERE p.id=? AND j.id=?`, id.ID, project).Scan(&p.ID, &p.Name, &p.Role, &p.ProjectID, &p.ProjectName)
	return p, err
}

func (s *Store) CurrentPlan(ctx context.Context, project, token string) (Plan, error) {
	var p Plan
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	actor, err := authorizeProjectRead(ctx, tx, project, token)
	if err != nil {
		return p, err
	}
	err = tx.QueryRowContext(ctx, `SELECT p.revision,p.body,p.author,p.created_at,EXISTS(SELECT 1 FROM plan_acknowledgments a WHERE a.project_id=p.project_id AND a.principal_id=? AND a.revision=p.revision) FROM plans p JOIN projects j ON j.id=p.project_id AND j.plan_revision=p.revision WHERE p.project_id=?`, actor.ID, project).Scan(&p.Revision, &p.Body, &p.Author, &p.CreatedAt, &p.Acknowledged)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// AcknowledgePlan records that this principal has seen the current revision.
func (s *Store) AcknowledgePlan(ctx context.Context, project, token string, revision int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := authorizeProjectRead(ctx, tx, project, token)
	if err != nil {
		return err
	}
	var current int
	if err = tx.QueryRowContext(ctx, `SELECT plan_revision FROM projects WHERE id=?`, project).Scan(&current); err != nil {
		return err
	}
	if revision < 1 || revision != current {
		return ErrConflict
	}
	var previous int
	err = tx.QueryRowContext(ctx, `SELECT revision FROM plan_acknowledgments WHERE project_id=? AND principal_id=?`, project, actor.ID).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if previous == revision {
		return nil
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO plan_acknowledgments(project_id,principal_id,revision,acknowledged_at) VALUES(?,?,?,?) ON CONFLICT(project_id,principal_id) DO UPDATE SET revision=excluded.revision,acknowledged_at=excluded.acknowledged_at`, project, actor.ID, revision, time.Now().UnixMilli()); err != nil {
		return err
	}
	if err = event(ctx, tx, project, actor.ID, "plan.acknowledged", project, map[string]int{"revision": revision}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListTasks(ctx context.Context, project, token string, limit int) ([]Task, error) {
	if limit < 1 || limit > 200 {
		return nil, errors.New("invalid limit")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = authorizeProjectRead(ctx, tx, project, token); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE project_id=? ORDER BY created_at,id LIMIT ?`, project, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (s *Store) GetReport(ctx context.Context, project, token, id string) (Report, error) {
	var r Report
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	if _, err = authorizeProjectRead(ctx, tx, project, token); err != nil {
		return r, err
	}
	err = tx.QueryRowContext(ctx, `SELECT r.id,r.task_id,r.attempt,r.author,r.body,r.digest,r.baseline,COALESCE(e.candidate_id,''),COALESCE(e.digest,''),COALESCE(e.baseline,'') FROM reports r JOIN tasks t ON t.id=r.task_id LEFT JOIN report_review_evidence e ON e.report_id=r.id WHERE r.id=? AND t.project_id=?`, id, project).Scan(&r.ID, &r.TaskID, &r.Attempt, &r.Author, &r.Body, &r.Digest, &r.Baseline, &r.SourceCandidateID, &r.SourceCandidateDigest, &r.SourceCandidateBase)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// ReportPage uses Unicode code point offsets, so clients can reconstruct the
// exact submitted text without splitting a UTF-8 sequence.
type ReportPage struct {
	ID                    string `json:"id"`
	TaskID                string `json:"task_id"`
	Attempt               int    `json:"attempt"`
	Author                string `json:"author"`
	Digest                string `json:"digest"`
	Baseline              string `json:"baseline"`
	SourceCandidateID     string `json:"source_candidate_id,omitempty"`
	SourceCandidateDigest string `json:"source_candidate_digest,omitempty"`
	SourceCandidateBase   string `json:"source_candidate_baseline,omitempty"`
	Excerpt               string `json:"excerpt"`
	Offset                int    `json:"offset"`
	TotalCharacters       int    `json:"total_characters"`
	NextOffset            *int   `json:"next_offset,omitempty"`
}

func (s *Store) GetReportPage(ctx context.Context, project, token, id string, offset, limit int) (ReportPage, error) {
	if offset < 0 || limit < 1 || limit > 16384 {
		return ReportPage{}, errors.New("invalid report offset or limit")
	}
	r, err := s.GetReport(ctx, project, token, id)
	if err != nil {
		return ReportPage{}, err
	}
	text := []rune(r.Body)
	if offset > len(text) {
		return ReportPage{}, errors.New("report offset exceeds text length")
	}
	end := offset + limit
	if end > len(text) {
		end = len(text)
	}
	page := ReportPage{ID: r.ID, TaskID: r.TaskID, Attempt: r.Attempt, Author: r.Author, Digest: r.Digest, Baseline: r.Baseline, SourceCandidateID: r.SourceCandidateID, SourceCandidateDigest: r.SourceCandidateDigest, SourceCandidateBase: r.SourceCandidateBase, Excerpt: string(text[offset:end]), Offset: offset, TotalCharacters: len(text)}
	if end < len(text) {
		page.NextOffset = &end
	}
	return page, nil
}

// ReconcileTask explicitly requeues an invalidated task against the current plan.
func (s *Store) ReconcileTask(ctx context.Context, project, token, taskID string, revision int) (Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	actor, err := authorizePermission(ctx, tx, project, token, PermissionTaskDelegate)
	if err != nil {
		return Task{}, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE tasks SET plan_revision=?, status=CASE WHEN EXISTS (SELECT 1 FROM task_blockers b WHERE b.task_id=tasks.id) OR EXISTS (SELECT 1 FROM dependencies d JOIN tasks p ON p.id=d.prerequisite_id WHERE d.task_id=tasks.id AND p.status<>'accepted') THEN 'blocked' ELSE 'ready' END, owner=NULL,lease_until=0 WHERE id=? AND project_id=? AND status='needs_replan' AND ?=(SELECT plan_revision FROM projects WHERE id=?)`, revision, taskID, project, revision, project)
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
	t, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id=? AND project_id=?`, taskID, project))
	if err != nil {
		return Task{}, err
	}
	if err = event(ctx, tx, project, actor.ID, "task.reconciled", taskID, map[string]int{"revision": revision}); err != nil {
		return Task{}, err
	}
	if err = tx.Commit(); err != nil {
		return Task{}, err
	}
	return t, nil
}

// RenewLease extends an unexpired claim without changing its fencing number.
func (s *Store) RenewLease(ctx context.Context, project, token, taskID string, attempt int, lease time.Duration) (Task, error) {
	if lease < time.Second || lease > time.Hour {
		return Task{}, errors.New("lease must be between one second and one hour")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	actor, err := authorizePermission(ctx, tx, project, token, PermissionTaskClaim)
	if err != nil {
		return Task{}, err
	}
	settings, err := readSettings(ctx, tx, project)
	if err != nil {
		return Task{}, err
	}
	if lease > time.Duration(settings.MaxLeaseSeconds)*time.Second {
		return Task{}, errors.New("lease exceeds project limit")
	}
	now := time.Now().UnixMilli()
	res, err := tx.ExecContext(ctx, `UPDATE tasks SET lease_until=? WHERE id=? AND project_id=? AND status='running' AND owner=? AND attempt=? AND lease_until>? AND plan_revision=(SELECT plan_revision FROM projects WHERE id=?)`, now+lease.Milliseconds(), taskID, project, actor.ID, attempt, now, project)
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
	t, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id=? AND project_id=?`, taskID, project))
	if err != nil {
		return Task{}, err
	}
	if err = tx.Commit(); err != nil {
		return Task{}, err
	}
	return t, nil
}

func (s *Store) RevokePrincipal(ctx context.Context, project, token, principalID string) error {
	if strings.TrimSpace(principalID) == "" {
		return errors.New("principal is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := authorizePermission(ctx, tx, project, token, PermissionMembershipManage)
	if err != nil {
		return err
	}
	if actor.ID == principalID {
		return errors.New("cannot revoke your own credential")
	}
	res, err := tx.ExecContext(ctx, `UPDATE principals SET revoked=1 WHERE id=? AND project_id=? AND revoked=0`, principalID, project)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrNotFound
	}
	// Revocation fences in-flight claims and makes the work available again.
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='ready',owner=NULL,lease_until=0 WHERE project_id=? AND owner=? AND status='running'`, project, principalID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM task_assignments WHERE principal_id=? AND task_id IN (SELECT id FROM tasks WHERE project_id=?)`, principalID, project); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM principal_permissions WHERE project_id=? AND principal_id=?`, project, principalID); err != nil {
		return err
	}
	if err = event(ctx, tx, project, actor.ID, "principal.revoked", principalID, map[string]string{}); err != nil {
		return err
	}
	return tx.Commit()
}
