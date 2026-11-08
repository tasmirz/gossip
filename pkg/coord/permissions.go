package coord

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const (
	PermissionProjectRead      = "project.read"
	PermissionMessageSend      = "message.send"
	PermissionTaskClaim        = "task.claim"
	PermissionTaskDelegate     = "task.delegate"
	PermissionResultSubmit     = "result.submit"
	PermissionPlanEdit         = "plan.edit"
	PermissionSettingsEdit     = "settings.edit"
	PermissionReviewDecide     = "review.decide"
	PermissionMembershipManage = "membership.manage"
	PermissionIntegration      = "integration"
)

func initialPermissionsForRole(role string) []string {
	switch role {
	case "worker":
		return []string{PermissionProjectRead, PermissionMessageSend, PermissionTaskClaim, PermissionResultSubmit}
	case "reviewer":
		return []string{PermissionProjectRead, PermissionMessageSend, PermissionReviewDecide}
	default:
		return nil
	}
}

func grantInitialPermissions(ctx context.Context, tx *sql.Tx, project, actor, principal, role string) error {
	for _, permission := range initialPermissionsForRole(role) {
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO principal_permissions(project_id,principal_id,permission,granted_by,granted_at) VALUES(?,?,?,?,?)`, project, principal, permission, actor, time.Now().UnixMilli())
		if err != nil {
			return err
		}
		inserted, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if inserted == 1 {
			if err = event(ctx, tx, project, actor, "permission.granted", principal, map[string]string{"permission": permission, "source": "enrollment_profile"}); err != nil {
				return err
			}
		}
	}
	return nil
}

type PermissionGrant struct {
	ProjectID   string `json:"project_id"`
	PrincipalID string `json:"principal_id"`
	Permission  string `json:"permission"`
	GrantedBy   string `json:"granted_by"`
	GrantedAt   int64  `json:"granted_at"`
}

func supportedPermission(permission string) bool {
	switch permission {
	case PermissionProjectRead, PermissionMessageSend, PermissionTaskClaim, PermissionTaskDelegate, PermissionResultSubmit, PermissionPlanEdit, PermissionSettingsEdit, PermissionReviewDecide, PermissionMembershipManage, PermissionIntegration:
		return true
	default:
		return false
	}
}

func authorizeProjectRead(ctx context.Context, tx *sql.Tx, project, token string) (identity, error) {
	return authorizePermission(ctx, tx, project, token, PermissionProjectRead)
}

// CredentialValid checks current project membership without requiring a data
// permission. It is used only to distinguish HTTP authentication from
// authorization failures; it does not grant access to project data.
func (s *Store) CredentialValid(ctx context.Context, project, token string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	_, err = authenticate(ctx, tx, project, token, "owner", "worker", "reviewer")
	if errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// authorizePermission checks one explicit project action grant after project
// membership is authenticated. Owners retain implicit project authority.
func authorizePermission(ctx context.Context, tx *sql.Tx, project, token, permission string) (identity, error) {
	actor, err := authenticate(ctx, tx, project, token, "owner", "worker", "reviewer")
	if err != nil || actor.Role == "owner" {
		return actor, err
	}
	if permission != PermissionProjectRead {
		var canRead int
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM principal_permissions WHERE project_id=? AND principal_id=? AND permission=?`, project, actor.ID, PermissionProjectRead).Scan(&canRead)
		if errors.Is(err, sql.ErrNoRows) {
			return identity{}, ErrUnauthorized
		}
		if err != nil {
			return identity{}, err
		}
	}
	var exists int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM principal_permissions WHERE project_id=? AND principal_id=? AND permission=?`, project, actor.ID, permission).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return identity{}, ErrUnauthorized
	}
	if err != nil {
		return identity{}, err
	}
	return actor, nil
}

// authorizeTaskDelegate gates task creation and assignment without granting
// project membership, plan editing, or administration.
func authorizeTaskDelegate(ctx context.Context, tx *sql.Tx, project, token string) (identity, error) {
	return authorizePermission(ctx, tx, project, token, PermissionTaskDelegate)
}

func (s *Store) GrantPermission(ctx context.Context, project, ownerToken, principalID, permission string) (PermissionGrant, error) {
	var grant PermissionGrant
	if !supportedPermission(permission) || principalID == "" {
		return grant, errors.New("invalid project permission")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return grant, err
	}
	defer tx.Rollback()
	actor, err := authenticate(ctx, tx, project, ownerToken, "owner")
	if err != nil {
		return grant, err
	}
	var role string
	err = tx.QueryRowContext(ctx, `SELECT role FROM principals WHERE id=? AND project_id=? AND revoked=0`, principalID, project).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return grant, ErrNotFound
	}
	if err != nil {
		return grant, err
	}
	if role == "owner" {
		return grant, errors.New("owners already have implicit project authority")
	}
	now := time.Now().UnixMilli()
	res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO principal_permissions(project_id,principal_id,permission,granted_by,granted_at) VALUES(?,?,?,?,?)`, project, principalID, permission, actor.ID, now)
	if err != nil {
		return grant, err
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return grant, err
	}
	if inserted == 1 {
		if err = event(ctx, tx, project, actor.ID, "permission.granted", principalID, map[string]string{"permission": permission}); err != nil {
			return grant, err
		}
	}
	if err = tx.QueryRowContext(ctx, `SELECT project_id,principal_id,permission,granted_by,granted_at FROM principal_permissions WHERE project_id=? AND principal_id=? AND permission=?`, project, principalID, permission).Scan(&grant.ProjectID, &grant.PrincipalID, &grant.Permission, &grant.GrantedBy, &grant.GrantedAt); err != nil {
		return PermissionGrant{}, err
	}
	if err = tx.Commit(); err != nil {
		return PermissionGrant{}, err
	}
	return grant, nil
}

// ListPermissions shows owners all active grants and other members only their
// own grants. Owner authority is implicit and is not represented by a row.
func (s *Store) ListPermissions(ctx context.Context, project, token string) ([]PermissionGrant, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	actor, err := authorizeProjectRead(ctx, tx, project, token)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT g.project_id,g.principal_id,g.permission,g.granted_by,g.granted_at FROM principal_permissions g JOIN principals p ON p.id=g.principal_id AND p.project_id=g.project_id AND p.revoked=0 WHERE g.project_id=? AND (?='owner' OR g.principal_id=?) ORDER BY g.principal_id,g.permission LIMIT 200`, project, actor.Role, actor.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PermissionGrant{}
	for rows.Next() {
		var grant PermissionGrant
		if err = rows.Scan(&grant.ProjectID, &grant.PrincipalID, &grant.Permission, &grant.GrantedBy, &grant.GrantedAt); err != nil {
			return nil, err
		}
		out = append(out, grant)
	}
	return out, rows.Err()
}

func (s *Store) RevokePermission(ctx context.Context, project, ownerToken, principalID, permission string) error {
	if !supportedPermission(permission) || principalID == "" {
		return errors.New("invalid project permission")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := authenticate(ctx, tx, project, ownerToken, "owner")
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM principal_permissions WHERE project_id=? AND principal_id=? AND permission=?`, project, principalID, permission)
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
	if err = event(ctx, tx, project, actor.ID, "permission.revoked", principalID, map[string]string{"permission": permission}); err != nil {
		return err
	}
	return tx.Commit()
}
