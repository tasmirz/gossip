package coord

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type ContextRecord struct {
	ID           string `json:"id"`
	Revision     int    `json:"revision"`
	Kind         string `json:"kind"`
	Title        string `json:"title"`
	Body         string `json:"body"`
	Verification string `json:"verification"`
	Source       string `json:"source"`
	Status       string `json:"status"`
	Author       string `json:"author"`
	CreatedAt    int64  `json:"created_at"`
}

type projectContextBackend interface {
	ContextLinks(context.Context, string, string, string, string) ([]ContextLink, error)
	LinkContext(context.Context, string, string, ContextLink) error
	SearchContext(context.Context, string, string, string) ([]ContextHit, error)
	ProjectContext(context.Context, string, string, string, int) ([]ContextRecord, error)
	PublishContext(context.Context, string, string, ContextRecord) (ContextRecord, error)
}

func (s *Store) ProjectContext(ctx context.Context, project, token, id string, revision int) ([]ContextRecord, error) {
	if revision < 0 || (id == "" && revision != 0) {
		return nil, errors.New("invalid context revision")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = authorizeProjectRead(ctx, tx, project, token); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.id,c.revision,c.kind,c.title,c.body,c.verification,c.source,c.status,c.author,c.created_at
 FROM project_context c WHERE project_id=? AND
 ((?='' AND c.revision=(SELECT MAX(h.revision) FROM project_context h WHERE h.project_id=c.project_id AND h.id=c.id))
 OR (c.id=? AND (?=0 OR c.revision=?)))
 ORDER BY c.created_at DESC,c.id,c.revision DESC LIMIT 50`, project, id, id, revision, revision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ContextRecord{}
	for rows.Next() {
		var v ContextRecord
		if err := rows.Scan(&v.ID, &v.Revision, &v.Kind, &v.Title, &v.Body, &v.Verification, &v.Source, &v.Status, &v.Author, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) PublishContext(ctx context.Context, project, token string, in ContextRecord) (ContextRecord, error) {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" || len(in.Title) > 240 || strings.TrimSpace(in.Body) == "" || len(in.Body) > 16384 || len(in.Verification) > 8192 || len(in.Source) > 2048 ||
		(in.Kind != "story" && in.Kind != "invariant" && in.Kind != "decision") || (in.Status != "active" && in.Status != "superseded") ||
		(in.Kind != "decision" && strings.TrimSpace(in.Verification) == "") {
		return ContextRecord{}, errors.New("provide a title, body, valid kind and status, and acceptance criteria or verification for stories and invariants")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ContextRecord{}, err
	}
	defer tx.Rollback()
	actor, err := authorizePermission(ctx, tx, project, token, PermissionPlanEdit)
	if err != nil {
		return ContextRecord{}, err
	}
	if in.ID == "" {
		if in.Revision != 0 {
			return ContextRecord{}, ErrConflict
		}
		in.ID, err = randomID("context_")
		if err != nil {
			return ContextRecord{}, err
		}
	} else {
		var current int
		var kind string
		err = tx.QueryRowContext(ctx, `SELECT revision,kind FROM project_context WHERE project_id=? AND id=? ORDER BY revision DESC LIMIT 1`, project, in.ID).Scan(&current, &kind)
		if err != nil {
			return ContextRecord{}, err
		}
		if current != in.Revision || kind != in.Kind {
			return ContextRecord{}, ErrConflict
		}
	}
	in.Revision++
	in.Author = actor.ID
	in.CreatedAt = time.Now().UnixMilli()
	_, err = tx.ExecContext(ctx, `INSERT INTO project_context(project_id,id,revision,kind,title,body,verification,source,status,author,created_at)
 VALUES(?,?,?,?,?,?,?,?,?,?,?)`, project, in.ID, in.Revision, in.Kind, in.Title, in.Body, in.Verification, in.Source, in.Status, in.Author, in.CreatedAt)
	if err != nil {
		return ContextRecord{}, err
	}
	if err = event(ctx, tx, project, actor.ID, "context.published", in.ID, map[string]any{"kind": in.Kind, "revision": in.Revision, "status": in.Status}); err != nil {
		return ContextRecord{}, err
	}
	return in, tx.Commit()
}

func (r *Remote) ProjectContext(ctx context.Context, project, token, id string, revision int) ([]ContextRecord, error) {
	return remoteCall[[]ContextRecord](ctx, r, token, rpcRequest{Operation: "context.list", Project: project, ID: id, Revision: revision})
}

func (r *Remote) PublishContext(ctx context.Context, project, token string, input ContextRecord) (ContextRecord, error) {
	return remoteCall[ContextRecord](ctx, r, token, rpcRequest{Operation: "context.publish", Project: project, ContextRecord: &input})
}

type ContextHit struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Excerpt  string `json:"excerpt"`
}

func (s *Store) SearchContext(ctx context.Context, project, token, query string) ([]ContextHit, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := authorizeProjectRead(ctx, tx, project, token); err != nil {
		return nil, err
	}
	hits := []ContextHit{}
	query = strings.TrimSpace(query)
	if query == "" {
		return hits, nil
	}
	if len(query) > 256 {
		return nil, errors.New("context search query is too long")
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.id,c.revision,c.kind,c.title,c.status,substr(c.body,1,240)
 FROM project_context c WHERE c.project_id=? AND c.revision=(SELECT MAX(h.revision) FROM project_context h WHERE h.project_id=c.project_id AND h.id=c.id)
 AND instr(lower(c.id||' '||c.kind||' '||c.title||' '||c.body||' '||c.verification||' '||c.source),lower(?))>0
 ORDER BY c.status='active' DESC,c.created_at DESC,c.id LIMIT 40`, project, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var hit ContextHit
		if err := rows.Scan(&hit.ID, &hit.Revision, &hit.Kind, &hit.Title, &hit.Status, &hit.Excerpt); err != nil {
			return nil, err
		}
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}

func (r *Remote) SearchContext(ctx context.Context, project, token, query string) ([]ContextHit, error) {
	return remoteCall[[]ContextHit](ctx, r, token, rpcRequest{Operation: "context.search", Project: project, Body: query})
}

type ContextLink struct {
	TargetKind string `json:"target_kind"`
	TargetID   string `json:"target_id"`
	ContextID  string `json:"context_id"`
	Revision   int    `json:"revision"`
}

func (s *Store) ContextLinks(ctx context.Context, project, token, kind, id string) ([]ContextLink, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = authorizeProjectRead(ctx, tx, project, token); err != nil {
		return nil, err
	}
	return contextLinksTx(ctx, tx, project, kind, id, 200)
}

func contextLinksTx(ctx context.Context, tx *sql.Tx, project, kind, id string, limit int) ([]ContextLink, error) {
	rows, err := tx.QueryContext(ctx, `SELECT target_kind,target_id,context_id,revision FROM project_context_links
 WHERE project_id=? AND (?='' OR target_kind=?) AND (?='' OR target_id=?)
 ORDER BY target_kind,target_id,context_id,revision LIMIT ?`, project, kind, kind, id, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ContextLink{}
	for rows.Next() {
		var link ContextLink
		if err := rows.Scan(&link.TargetKind, &link.TargetID, &link.ContextID, &link.Revision); err != nil {
			return nil, err
		}
		out = append(out, link)
	}
	return out, rows.Err()
}

func (s *Store) LinkContext(ctx context.Context, project, token string, link ContextLink) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := authorizePermission(ctx, tx, project, token, PermissionPlanEdit)
	if err != nil {
		return err
	}
	var query string
	switch link.TargetKind {
	case "task":
		query = `SELECT EXISTS(SELECT 1 FROM tasks WHERE project_id=? AND id=?)`
	case "plan":
		query = `SELECT EXISTS(SELECT 1 FROM plans WHERE project_id=? AND CAST(revision AS TEXT)=?)`
	case "review":
		query = `SELECT EXISTS(SELECT 1 FROM reports r JOIN tasks t ON t.id=r.task_id WHERE t.project_id=? AND r.id=?)`
	default:
		return errors.New("target kind must be task, plan, or review")
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, query, project, link.TargetID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM project_context WHERE project_id=? AND id=? AND revision=?)`, project, link.ContextID, link.Revision).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO project_context_links VALUES(?,?,?,?,?)`, project, link.TargetKind, link.TargetID, link.ContextID, link.Revision)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 0 {
		if err := event(ctx, tx, project, actor.ID, "context.linked", link.ContextID, link); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *Remote) ContextLinks(ctx context.Context, project, token, kind, id string) ([]ContextLink, error) {
	return remoteCall[[]ContextLink](ctx, r, token, rpcRequest{Operation: "context.links", Project: project, ContextLink: &ContextLink{TargetKind: kind, TargetID: id}})
}

func (r *Remote) LinkContext(ctx context.Context, project, token string, link ContextLink) error {
	_, err := remoteCall[bool](ctx, r, token, rpcRequest{Operation: "context.link", Project: project, ContextLink: &link})
	return err
}
