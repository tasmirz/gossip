package coord

import (
	"context"
	"errors"
	"strings"
	"time"
)

type PersonalItemInput struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Notes  string `json:"notes"`
}

func (s *Store) PersonalItems(ctx context.Context, project, token string) (PersonalChecklistResponse, error) {
	out := PersonalChecklistResponse{Available: true, Items: []PersonalChecklistEntry{}}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err := authorizeProjectRead(ctx, tx, project, token); err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,principal_id,title,status,notes,updated_at FROM personal_items WHERE project_id=? ORDER BY updated_at DESC,id LIMIT 1000`, project)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		item := PersonalChecklistEntry{Source: "coordinator"}
		if err := rows.Scan(&item.ID, &item.AgentID, &item.Title, &item.Status, &item.Notes, &item.UpdatedAt); err != nil {
			return out, err
		}
		out.Items = append(out.Items, item)
	}
	return out, rows.Err()
}

func (s *Store) SavePersonalItem(ctx context.Context, project, token string, in PersonalItemInput) (PersonalChecklistEntry, error) {
	var out PersonalChecklistEntry
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" || len(in.Title) > 240 || len(in.Notes) > 8192 || (in.Status != "todo" && in.Status != "doing" && in.Status != "done") {
		return out, errors.New("title and a status of todo, doing, or done are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	actor, err := authorizeProjectRead(ctx, tx, project, token)
	if err != nil {
		return out, err
	}
	updated := time.Now().UTC().Format(time.RFC3339Nano)
	if in.ID == "" {
		in.ID, err = randomID("todo_")
		if err != nil {
			return out, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO personal_items(project_id,principal_id,id,title,status,notes,updated_at) VALUES(?,?,?,?,?,?,?)`, project, actor.ID, in.ID, in.Title, in.Status, in.Notes, updated)
	} else {
		result, e := tx.ExecContext(ctx, `UPDATE personal_items SET title=?,status=?,notes=?,updated_at=? WHERE project_id=? AND principal_id=? AND id=?`, in.Title, in.Status, in.Notes, updated, project, actor.ID, in.ID)
		if e != nil {
			return out, e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return out, e
		}
		if n == 0 {
			return out, ErrNotFound
		}
	}
	if err != nil {
		return out, err
	}
	out = PersonalChecklistEntry{ID: in.ID, AgentID: actor.ID, Title: in.Title, Status: in.Status, Notes: in.Notes, UpdatedAt: updated, Source: "coordinator"}
	return out, tx.Commit()
}

type personalItemsBackend interface {
	PersonalItems(context.Context, string, string) (PersonalChecklistResponse, error)
	SavePersonalItem(context.Context, string, string, PersonalItemInput) (PersonalChecklistEntry, error)
}

func (r *Remote) PersonalItems(ctx context.Context, project, token string) (PersonalChecklistResponse, error) {
	return remoteCall[PersonalChecklistResponse](ctx, r, token, rpcRequest{Operation: "personal.list", Project: project})
}

func (r *Remote) SavePersonalItem(ctx context.Context, project, token string, input PersonalItemInput) (PersonalChecklistEntry, error) {
	return remoteCall[PersonalChecklistEntry](ctx, r, token, rpcRequest{Operation: "personal.save", Project: project, ID: input.ID, Title: input.Title, PersonalStatus: input.Status, Body: input.Notes})
}
