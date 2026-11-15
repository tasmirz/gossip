package coord

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

type ProjectRole struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
}

func (s *Store) SaveProjectRole(ctx context.Context, project, token string, role ProjectRole) (ProjectRole, error) {
	role.Name = strings.TrimSpace(role.Name)
	if role.Name == "" || len(role.Name) > 80 {
		return role, errors.New("role name required (maximum 80 bytes)")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return role, err
	}
	defer tx.Rollback()
	actor, err := authenticate(ctx, tx, project, token, "owner")
	if err != nil {
		return role, err
	}
	if role.ID == "" {
		role.ID, err = randomID("role_")
		if err != nil {
			return role, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO project_roles VALUES(?,?,?)`, project, role.ID, role.Name)
	} else {
		var result sql.Result
		result, err = tx.ExecContext(ctx, `UPDATE project_roles SET name=? WHERE project_id=? AND id=?`, role.Name, project, role.ID)
		if err == nil {
			n, e := result.RowsAffected()
			if e != nil {
				return role, e
			}
			if n == 0 {
				return role, ErrNotFound
			}
		}
	}
	if err != nil {
		return role, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM project_role_permissions WHERE project_id=? AND role_id=?`, project, role.ID); err != nil {
		return role, err
	}
	seen := map[string]bool{}
	for _, permission := range role.Permissions {
		if !supportedPermission(permission) || seen[permission] {
			return role, errors.New("invalid or duplicate permission")
		}
		seen[permission] = true
		if _, err = tx.ExecContext(ctx, `INSERT INTO project_role_permissions VALUES(?,?,?)`, project, role.ID, permission); err != nil {
			return role, err
		}
	}
	if !seen[PermissionProjectRead] {
		return role, errors.New("a project role requires project.read")
	}
	if err = event(ctx, tx, project, actor.ID, "access.role_saved", role.ID, role); err != nil {
		return role, err
	}
	return role, tx.Commit()
}

func (s *Store) AssignProjectRole(ctx context.Context, project, token, principal, role string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := authenticate(ctx, tx, project, token, "owner")
	if err != nil {
		return err
	}
	var eligible, exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE project_id=? AND id=? AND role<>'owner' AND revoked=0), EXISTS(SELECT 1 FROM project_roles WHERE project_id=? AND id=?)`, project, principal, project, role).Scan(&eligible, &exists); err != nil {
		return err
	}
	if !eligible || (!exists && role != "viewer" && role != "worker" && role != "reviewer") {
		return ErrNotFound
	}
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT role_id FROM project_role_assignments WHERE project_id=? AND principal_id=?`, project, principal).Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		var enrollment string
		if err = tx.QueryRowContext(ctx, `SELECT role FROM principals WHERE project_id=? AND id=?`, project, principal).Scan(&enrollment); err != nil {
			return err
		}
		baseline := map[string]bool{}
		for _, permission := range initialPermissionsForRole(enrollment) {
			baseline[permission] = true
		}
		for _, permission := range projectActions {
			allowed, err := effectiveProjectPermission(ctx, tx, project, principal, permission)
			if err != nil {
				return err
			}
			if allowed != baseline[permission] {
				if err := saveRoleOverride(ctx, tx, project, principal, permission, allowed); err != nil {
					return err
				}
			}
		}
	} else if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO project_role_assignments VALUES(?,?,?) ON CONFLICT(project_id,principal_id) DO UPDATE SET role_id=excluded.role_id`, project, principal, role); err != nil {
		return err
	}
	if err = event(ctx, tx, project, actor.ID, "access.role_assigned", principal, map[string]string{"role_id": role}); err != nil {
		return err
	}
	return tx.Commit()
}

func effectiveProjectPermission(ctx context.Context, tx *sql.Tx, project, principal, permission string) (bool, error) {
	var role string
	err := tx.QueryRowContext(ctx, `SELECT role_id FROM project_role_assignments WHERE project_id=? AND principal_id=?`, project, principal).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		var allowed bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM principal_permissions WHERE project_id=? AND principal_id=? AND permission=?)`, project, principal, permission).Scan(&allowed)
		return allowed, err
	}
	if err != nil {
		return false, err
	}
	var override bool
	err = tx.QueryRowContext(ctx, `SELECT allowed FROM project_permission_overrides WHERE project_id=? AND principal_id=? AND permission=?`, project, principal, permission).Scan(&override)
	if !errors.Is(err, sql.ErrNoRows) {
		return override, err
	}
	if role == "viewer" {
		return permission == PermissionProjectRead, nil
	}
	if role == "worker" || role == "reviewer" {
		for _, p := range initialPermissionsForRole(role) {
			if p == permission {
				return true, nil
			}
		}
		return false, nil
	}
	var allowed bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM project_role_permissions WHERE project_id=? AND role_id=? AND permission=?)`, project, role, permission).Scan(&allowed)
	return allowed, err
}

var projectActions = []string{PermissionProjectRead, PermissionMessageSend, PermissionTaskClaim, PermissionTaskDelegate, PermissionResultSubmit, PermissionPlanEdit, PermissionSettingsEdit, PermissionReviewDecide, PermissionMembershipManage, PermissionIntegration}

func saveRoleOverride(ctx context.Context, tx *sql.Tx, project, principal, permission string, allowed bool) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO project_permission_overrides VALUES(?,?,?,?) ON CONFLICT(project_id,principal_id,permission) DO UPDATE SET allowed=excluded.allowed`, project, principal, permission, allowed)
	return err
}

func (s *Store) ResetPermissionOverride(ctx context.Context, project, token, principal, permission string) error {
	if !supportedPermission(permission) {
		return errors.New("invalid project permission")
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
	result, err := tx.ExecContext(ctx, `DELETE FROM project_permission_overrides WHERE project_id=? AND principal_id=? AND permission=?`, project, principal, permission)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if err = event(ctx, tx, project, actor.ID, "permission.reset", principal, map[string]string{"permission": permission}); err != nil {
		return err
	}
	return tx.Commit()
}

type ProjectRoleDirectory struct {
	Roles       []ProjectRole     `json:"roles"`
	Assignments map[string]string `json:"assignments"`
	Actions     []string          `json:"actions"`
}

func (s *Store) ProjectRoles(ctx context.Context, project, token string) (ProjectRoleDirectory, error) {
	out := ProjectRoleDirectory{Assignments: map[string]string{}, Actions: projectActions}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err = authenticate(ctx, tx, project, token, "owner"); err != nil {
		return out, err
	}
	out.Roles = []ProjectRole{
		{ID: "viewer", Name: "Viewer", Permissions: []string{PermissionProjectRead}},
		{ID: "worker", Name: "Worker", Permissions: initialPermissionsForRole("worker")},
		{ID: "reviewer", Name: "Reviewer", Permissions: initialPermissionsForRole("reviewer")},
	}
	rows, err := tx.QueryContext(ctx, `SELECT r.id,r.name,p.permission FROM project_roles r JOIN project_role_permissions p ON p.project_id=r.project_id AND p.role_id=r.id WHERE r.project_id=? ORDER BY r.id,p.permission`, project)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name, permission string
		if err := rows.Scan(&id, &name, &permission); err != nil {
			return out, err
		}
		i := len(out.Roles) - 1
		if out.Roles[i].ID != id {
			out.Roles = append(out.Roles, ProjectRole{ID: id, Name: name})
			i++
		}
		out.Roles[i].Permissions = append(out.Roles[i].Permissions, permission)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT a.principal_id,a.role_id FROM project_role_assignments a JOIN principals p ON p.project_id=a.project_id AND p.id=a.principal_id WHERE a.project_id=? AND p.revoked=0`, project)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var principal, role string
		if err := rows.Scan(&principal, &role); err != nil {
			return out, err
		}
		out.Assignments[principal] = role
	}
	return out, rows.Err()
}
