package coord

import (
	"context"
	"errors"
	"strings"
	"time"
)

type PlanVariant struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Body      string `json:"body"`
	Author    string `json:"author"`
	CreatedAt int64  `json:"created_at"`
}

func (s *Store) PlanVariants(ctx context.Context, project, token string, revision int) ([]PlanVariant, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := authorizeProjectRead(ctx, tx, project, token); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,name,body,author,created_at FROM plan_variants WHERE project_id=? AND revision=? ORDER BY created_at,id`, project, revision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []PlanVariant{}
	for rows.Next() {
		var item PlanVariant
		if err := rows.Scan(&item.ID, &item.Name, &item.Body, &item.Author, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CreatePlanVariant(ctx context.Context, project, token string, revision int, name, body string) (PlanVariant, error) {
	name = strings.TrimSpace(name)
	if revision < 1 || name == "" || len(name) > 160 || strings.TrimSpace(body) == "" || len(body) > 256*1024 {
		return PlanVariant{}, errors.New("provide a revision, variant name, and plan text")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PlanVariant{}, err
	}
	defer tx.Rollback()
	actor, err := authorizePermission(ctx, tx, project, token, PermissionPlanEdit)
	if err != nil {
		return PlanVariant{}, err
	}
	id, err := randomID("variant_")
	if err != nil {
		return PlanVariant{}, err
	}
	item := PlanVariant{ID: id, Name: name, Body: body, Author: actor.ID, CreatedAt: time.Now().UnixMilli()}
	result, err := tx.ExecContext(ctx, `INSERT INTO plan_variants(id,project_id,revision,name,body,author,created_at)
		SELECT ?,project_id,revision,?,?,?,? FROM plans WHERE project_id=? AND revision=?`, item.ID, item.Name, item.Body, item.Author, item.CreatedAt, project, revision)
	if err != nil {
		return PlanVariant{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return PlanVariant{}, err
	}
	if count == 0 {
		return PlanVariant{}, ErrNotFound
	}
	if err := event(ctx, tx, project, actor.ID, "plan.variant_created", item.ID, map[string]int{"revision": revision}); err != nil {
		return PlanVariant{}, err
	}
	return item, tx.Commit()
}

func (r *Remote) PlanVariants(ctx context.Context, project, token string, revision int) ([]PlanVariant, error) {
	return remoteCall[[]PlanVariant](ctx, r, token, rpcRequest{Operation: "plan.variants", Project: project, Revision: revision})
}

func (r *Remote) CreatePlanVariant(ctx context.Context, project, token string, revision int, name, body string) (PlanVariant, error) {
	return remoteCall[PlanVariant](ctx, r, token, rpcRequest{Operation: "plan.variant_create", Project: project, Revision: revision, Name: name, Body: body})
}
