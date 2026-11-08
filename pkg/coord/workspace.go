package coord

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type Repository struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	RemoteHints []string `json:"remote_hints,omitempty"`
}
type Workspace struct {
	ID           string `json:"id"`
	RepositoryID string `json:"repository_id"`
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	NodeLabel    string `json:"node_label"`
	LocalPath    string `json:"local_path,omitempty"`
	Branch       string `json:"branch"`
	Head         string `json:"head"`
	Dirty        bool   `json:"dirty"`
	UpdatedAt    int64  `json:"updated_at"`
}
type Attempt struct {
	TaskID         string `json:"task_id"`
	Number         int    `json:"number"`
	PrincipalID    string `json:"principal_id"`
	WorkspaceID    string `json:"workspace_id,omitempty"`
	WorkspaceHead  string `json:"workspace_head,omitempty"`
	WorkspaceDirty bool   `json:"workspace_dirty"`
	StartedAt      int64  `json:"started_at"`
}

// validateAttemptBaseline binds a submitted report or candidate to the commit
// captured when that attempt claimed its workspace. The dirty flag remains a
// separate part of the attempt snapshot; the commit identifies the base tree.
func validateAttemptBaseline(ctx context.Context, tx *sql.Tx, project, taskID string, number int, principalID, baseline string) error {
	var owner, workspaceID, head string
	err := tx.QueryRowContext(ctx, `SELECT a.principal_id,COALESCE(a.workspace_id,''),a.workspace_head FROM attempts a
		JOIN tasks t ON t.id=a.task_id WHERE t.project_id=? AND a.task_id=? AND a.number=?`, project, taskID, number).Scan(&owner, &workspaceID, &head)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if owner != principalID {
		return ErrUnauthorized
	}
	if workspaceID != "" && head != "" && baseline != head && baseline != "commit:"+head {
		return errors.New("report baseline must match the workspace commit recorded when the attempt was claimed")
	}
	return nil
}

func (s *Store) CreateRepository(ctx context.Context, project, token, name string) (Repository, error) {
	var repo Repository
	if strings.TrimSpace(name) == "" || len(name) > 256 {
		return repo, errors.New("invalid repository name")
	}
	id, err := randomID("repo_")
	if err != nil {
		return repo, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return repo, err
	}
	defer tx.Rollback()
	actor, err := authenticate(ctx, tx, project, token, "owner")
	if err != nil {
		return repo, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO repositories(id,project_id,name,created_at) VALUES(?,?,?,?)`, id, project, name, time.Now().UnixMilli()); err != nil {
		return repo, err
	}
	if err = event(ctx, tx, project, actor.ID, "repository.created", id, map[string]string{"name": name}); err != nil {
		return repo, err
	}
	if err = tx.Commit(); err != nil {
		return repo, err
	}
	return Repository{ID: id, Name: name}, nil
}

func (s *Store) ListRepositories(ctx context.Context, project, token string) ([]Repository, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = authorizeProjectRead(ctx, tx, project, token); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT r.id,r.name,COALESCE((SELECT group_concat(remote_key,char(10)) FROM (SELECT h.remote_key FROM repository_remote_hints h WHERE h.project_id=r.project_id AND h.repository_id=r.id ORDER BY h.remote_key)),'') FROM repositories r WHERE r.project_id=? ORDER BY r.name,r.id LIMIT 200`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Repository{}
	for rows.Next() {
		var r Repository
		var hints string
		if err = rows.Scan(&r.ID, &r.Name, &hints); err != nil {
			return nil, err
		}
		if hints != "" {
			r.RemoteHints = strings.Split(hints, "\n")
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FindRepositoriesByRemote uses the indexed hint key so recognition does not
// depend on the paged repository list used by the dashboard.
func (s *Store) FindRepositoriesByRemote(ctx context.Context, project, token, rawRemote string) ([]Repository, error) {
	key, err := NormalizeRepositoryRemote(rawRemote)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = authorizeProjectRead(ctx, tx, project, token); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT r.id,r.name FROM repository_remote_hints h JOIN repositories r ON r.id=h.repository_id AND r.project_id=h.project_id WHERE h.project_id=? AND h.remote_key=? ORDER BY r.name,r.id LIMIT 201`, project, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Repository{}
	for rows.Next() {
		var r Repository
		if err = rows.Scan(&r.ID, &r.Name); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > 200 {
		return nil, errors.New("too many repository matches; narrow the project mapping")
	}
	return out, nil
}

// AddRepositoryRemoteHint stores a sanitized Git remote for project matching.
// It is a recognition hint only; it never creates membership or permissions.
func (s *Store) AddRepositoryRemoteHint(ctx context.Context, project, token, repositoryID, rawRemote string) (Repository, error) {
	key, err := NormalizeRepositoryRemote(rawRemote)
	if err != nil {
		return Repository{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Repository{}, err
	}
	defer tx.Rollback()
	actor, err := authenticate(ctx, tx, project, token, "owner")
	if err != nil {
		return Repository{}, err
	}
	var repo Repository
	err = tx.QueryRowContext(ctx, `SELECT id,name FROM repositories WHERE id=? AND project_id=?`, repositoryID, project).Scan(&repo.ID, &repo.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return Repository{}, ErrNotFound
	}
	if err != nil {
		return Repository{}, err
	}
	var count, exists int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM repository_remote_hints WHERE project_id=? AND repository_id=?`, project, repositoryID).Scan(&count); err != nil {
		return Repository{}, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM repository_remote_hints WHERE project_id=? AND repository_id=? AND remote_key=?`, project, repositoryID, key).Scan(&exists); err != nil {
		return Repository{}, err
	}
	if exists == 0 && count >= 32 {
		return Repository{}, errors.New("repository already has the maximum of 32 remote hints")
	}
	res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO repository_remote_hints(project_id,repository_id,remote_key,added_by,created_at) VALUES(?,?,?,?,?)`, project, repositoryID, key, actor.ID, time.Now().UnixMilli())
	if err != nil {
		return Repository{}, err
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return Repository{}, err
	}
	if inserted == 1 {
		if err = event(ctx, tx, project, actor.ID, "repository.remote_hint_added", repositoryID, map[string]string{"remote_key": key}); err != nil {
			return Repository{}, err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT remote_key FROM repository_remote_hints WHERE project_id=? AND repository_id=? ORDER BY remote_key`, project, repositoryID)
	if err != nil {
		return Repository{}, err
	}
	for rows.Next() {
		var hint string
		if err = rows.Scan(&hint); err != nil {
			rows.Close()
			return Repository{}, err
		}
		repo.RemoteHints = append(repo.RemoteHints, hint)
	}
	if err = rows.Close(); err != nil {
		return Repository{}, err
	}
	if err = tx.Commit(); err != nil {
		return Repository{}, err
	}
	return repo, nil
}

func (s *Store) RemoveRepositoryRemoteHint(ctx context.Context, project, token, repositoryID, rawRemote string) error {
	key, err := NormalizeRepositoryRemote(rawRemote)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := authenticate(ctx, tx, project, token, "owner")
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM repository_remote_hints WHERE project_id=? AND repository_id=? AND remote_key=? AND EXISTS(SELECT 1 FROM repositories WHERE id=? AND project_id=?)`, project, repositoryID, key, repositoryID, project)
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
	if err = event(ctx, tx, project, actor.ID, "repository.remote_hint_removed", repositoryID, map[string]string{"remote_key": key}); err != nil {
		return err
	}
	return tx.Commit()
}

// RegisterWorkspace records a caller-reported observation, not a verified
// filesystem identity. Repository membership is always explicit.
func (s *Store) RegisterWorkspace(ctx context.Context, project, token, repo, name, node, path, branch, head string, dirty bool) (Workspace, error) {
	var w Workspace
	if strings.TrimSpace(name) == "" || strings.TrimSpace(path) == "" || len(path) > 4096 || len(name) > 256 || len(node) > 256 || len(branch) > 512 || len(head) > 512 {
		return w, errors.New("invalid workspace metadata")
	}
	id, err := randomID("workspace_")
	if err != nil {
		return w, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return w, err
	}
	defer tx.Rollback()
	actor, err := authorizeProjectRead(ctx, tx, project, token)
	if err != nil {
		return w, err
	}
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM repositories WHERE id=? AND project_id=?`, repo, project).Scan(&exists); err != nil {
		return w, err
	}
	if exists != 1 {
		return w, ErrNotFound
	}
	now := time.Now().UnixMilli()
	_, err = tx.ExecContext(ctx, `INSERT INTO workspaces(id,project_id,repository_id,owner,name,node_label,local_path,branch,head,dirty,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, project, repo, actor.ID, name, node, path, branch, head, dirty, now)
	if err != nil {
		return w, err
	}
	if err = event(ctx, tx, project, actor.ID, "workspace.registered", id, map[string]string{"repository_id": repo, "name": name}); err != nil {
		return w, err
	}
	if err = tx.Commit(); err != nil {
		return w, err
	}
	return Workspace{ID: id, RepositoryID: repo, Owner: actor.ID, Name: name, NodeLabel: node, LocalPath: path, Branch: branch, Head: head, Dirty: dirty, UpdatedAt: now}, nil
}

func (s *Store) ListWorkspaces(ctx context.Context, project, token string) ([]Workspace, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	actor, err := authorizeProjectRead(ctx, tx, project, token)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,repository_id,owner,name,node_label,local_path,branch,head,dirty,updated_at FROM workspaces WHERE project_id=? ORDER BY name,id LIMIT 200`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Workspace{}
	for rows.Next() {
		var w Workspace
		if err = rows.Scan(&w.ID, &w.RepositoryID, &w.Owner, &w.Name, &w.NodeLabel, &w.LocalPath, &w.Branch, &w.Head, &w.Dirty, &w.UpdatedAt); err != nil {
			return nil, err
		}
		if w.Owner != actor.ID {
			w.LocalPath = ""
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// UpdateWorkspace records a new observation; existing attempt snapshots remain unchanged.
func (s *Store) UpdateWorkspace(ctx context.Context, project, token, id, branch, head string, dirty bool) (Workspace, error) {
	var w Workspace
	if len(branch) > 512 || len(head) > 512 {
		return w, errors.New("invalid workspace metadata")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return w, err
	}
	defer tx.Rollback()
	actor, err := authorizeProjectRead(ctx, tx, project, token)
	if err != nil {
		return w, err
	}
	now := time.Now().UnixMilli()
	res, err := tx.ExecContext(ctx, `UPDATE workspaces SET branch=?,head=?,dirty=?,updated_at=? WHERE id=? AND project_id=? AND owner=?`, branch, head, dirty, now, id, project, actor.ID)
	if err != nil {
		return w, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return w, err
	}
	if n != 1 {
		return w, ErrNotFound
	}
	err = tx.QueryRowContext(ctx, `SELECT id,repository_id,owner,name,node_label,local_path,branch,head,dirty,updated_at FROM workspaces WHERE id=? AND project_id=?`, id, project).Scan(&w.ID, &w.RepositoryID, &w.Owner, &w.Name, &w.NodeLabel, &w.LocalPath, &w.Branch, &w.Head, &w.Dirty, &w.UpdatedAt)
	if err != nil {
		return Workspace{}, err
	}
	if err = event(ctx, tx, project, actor.ID, "workspace.updated", id, map[string]string{"head": head}); err != nil {
		return Workspace{}, err
	}
	if err = tx.Commit(); err != nil {
		return Workspace{}, err
	}
	return w, nil
}

func (s *Store) GetAttempt(ctx context.Context, project, token, taskID string, number int) (Attempt, error) {
	var a Attempt
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return a, err
	}
	defer tx.Rollback()
	if _, err = authorizeProjectRead(ctx, tx, project, token); err != nil {
		return a, err
	}
	err = tx.QueryRowContext(ctx, `SELECT a.task_id,a.number,a.principal_id,COALESCE(a.workspace_id,''),a.workspace_head,a.workspace_dirty,a.started_at FROM attempts a JOIN tasks t ON t.id=a.task_id WHERE t.project_id=? AND a.task_id=? AND a.number=?`, project, taskID, number).Scan(&a.TaskID, &a.Number, &a.PrincipalID, &a.WorkspaceID, &a.WorkspaceHead, &a.WorkspaceDirty, &a.StartedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}
