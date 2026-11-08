package coord

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// PrepareCredential generates a credential locally before any mutation.
func PrepareCredential(project string) (string, Credential, error) {
	var err error
	if project == "" {
		project, err = randomID("proj_")
		if err != nil {
			return "", Credential{}, err
		}
	}
	id, err := randomID("principal_")
	if err != nil {
		return "", Credential{}, err
	}
	token, _, err := newToken()
	if err != nil {
		return "", Credential{}, err
	}
	return project, Credential{PrincipalID: id, Token: token}, nil
}
func validPrepared(project string, c Credential) bool {
	if !strings.HasPrefix(project, "proj_") || len(project) != 37 || !strings.HasPrefix(c.PrincipalID, "principal_") || len(c.PrincipalID) != 42 || len(c.Token) != 64 {
		return false
	}
	for _, s := range []string{project[5:], c.PrincipalID[10:], c.Token} {
		if _, err := hex.DecodeString(s); err != nil {
			return false
		}
	}
	return true
}

// CreatePreparedProject is idempotent for the same identity and input.
func (s *Store) CreatePreparedProject(ctx context.Context, project, name, owner string, c Credential) (Project, error) {
	if !validPrepared(project, c) || strings.TrimSpace(name) == "" || strings.TrimSpace(owner) == "" || len(name) > 512 || len(owner) > 512 {
		return Project{}, errors.New("invalid project or prepared credential")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback()
	hash := sha256.Sum256([]byte(c.Token))
	var existingName, principalName, role string
	var existingHash []byte
	var revoked int
	err = tx.QueryRowContext(ctx, `SELECT p.name,a.name,a.role,a.token_hash,a.revoked FROM projects p JOIN principals a ON a.project_id=p.id WHERE p.id=? AND a.id=?`, project, c.PrincipalID).Scan(&existingName, &principalName, &role, &existingHash, &revoked)
	if err == nil {
		if existingName != name || principalName != owner || role != "owner" || revoked != 0 || !bytes.Equal(existingHash, hash[:]) {
			return Project{}, ErrConflict
		}
		return Project{ID: project, Name: name}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Project{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO projects(id,name) VALUES(?,?)`, project, name); err != nil {
		return Project{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO project_settings(project_id,revision,max_parallel,max_lease_seconds,summary_token_target,default_review_mode) VALUES(?,1,4,3600,1000,'report_first')`, project); err != nil {
		return Project{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO principals(id,project_id,name,role,token_hash) VALUES(?,?,?,?,?)`, c.PrincipalID, project, owner, "owner", hash[:]); err != nil {
		return Project{}, err
	}
	if err = event(ctx, tx, project, c.PrincipalID, "project.created", project, map[string]string{"name": name}); err != nil {
		return Project{}, err
	}
	if err = tx.Commit(); err != nil {
		return Project{}, err
	}
	return Project{ID: project, Name: name}, nil
}
func (s *Store) AddPreparedPrincipal(ctx context.Context, project, ownerToken, name, role string, c Credential) (Credential, error) {
	if !validPrepared(project, c) || strings.TrimSpace(name) == "" || len(name) > 512 || (role != "owner" && role != "worker" && role != "reviewer") {
		return Credential{}, errors.New("invalid principal or prepared credential")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Credential{}, err
	}
	defer tx.Rollback()
	actor, err := authorizePermission(ctx, tx, project, ownerToken, PermissionMembershipManage)
	if err != nil {
		return Credential{}, err
	}
	if role == "owner" && actor.Role != "owner" {
		return Credential{}, ErrUnauthorized
	}
	hash := sha256.Sum256([]byte(c.Token))
	var existingProject, existingName, existingRole string
	var stored []byte
	var revoked int
	err = tx.QueryRowContext(ctx, `SELECT project_id,name,role,token_hash,revoked FROM principals WHERE id=?`, c.PrincipalID).Scan(&existingProject, &existingName, &existingRole, &stored, &revoked)
	if err == nil {
		if existingProject != project || existingName != name || existingRole != role || revoked != 0 || !bytes.Equal(stored, hash[:]) {
			return Credential{}, ErrConflict
		}
		return c, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Credential{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO principals(id,project_id,name,role,token_hash) VALUES(?,?,?,?,?)`, c.PrincipalID, project, name, role, hash[:]); err != nil {
		return Credential{}, err
	}
	if err = grantInitialPermissions(ctx, tx, project, actor.ID, c.PrincipalID, role); err != nil {
		return Credential{}, err
	}
	if err = event(ctx, tx, project, actor.ID, "principal.enrolled", c.PrincipalID, map[string]string{"name": name, "role": role}); err != nil {
		return Credential{}, err
	}
	if err = tx.Commit(); err != nil {
		return Credential{}, err
	}
	return c, nil
}

// AddPreparedRemotePrincipal atomically enrolls a principal and grants one
// client certificate access to this project. The project credential and
// certificate grant remain independently revocable after enrollment.
func (s *Store) AddPreparedRemotePrincipal(ctx context.Context, project, ownerToken, name, role string, c Credential, fingerprint string, expiresAt int64) (Credential, error) {
	if !validPrepared(project, c) || strings.TrimSpace(name) == "" || len(name) > 512 || (role != "worker" && role != "reviewer" && role != "owner") {
		return Credential{}, errors.New("invalid remote principal or prepared credential")
	}
	fingerprint, err := NormalizeCertificateFingerprint(fingerprint)
	if err != nil {
		return Credential{}, err
	}
	now := time.Now().UnixMilli()
	if expiresAt != 0 && expiresAt <= now {
		return Credential{}, errors.New("certificate grant expiry must be in the future")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Credential{}, err
	}
	defer tx.Rollback()
	actor, err := authenticate(ctx, tx, project, ownerToken, "owner")
	if err != nil {
		return Credential{}, err
	}
	hash := sha256.Sum256([]byte(c.Token))
	var existingProject, existingName, existingRole string
	var stored []byte
	var revoked int
	err = tx.QueryRowContext(ctx, `SELECT project_id,name,role,token_hash,revoked FROM principals WHERE id=?`, c.PrincipalID).Scan(&existingProject, &existingName, &existingRole, &stored, &revoked)
	if err == nil {
		if existingProject != project || existingName != name || existingRole != role || revoked != 0 || !bytes.Equal(stored, hash[:]) {
			return Credential{}, ErrConflict
		}
		var grantExists int
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM certificate_grants WHERE project_id=? AND fingerprint=?`, project, fingerprint).Scan(&grantExists)
		if errors.Is(err, sql.ErrNoRows) {
			return Credential{}, ErrConflict // Retrying cannot restore a revoked project grant.
		}
		if err != nil {
			return Credential{}, err
		}
		return c, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Credential{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO principals(id,project_id,name,role,token_hash) VALUES(?,?,?,?,?)`, c.PrincipalID, project, name, role, hash[:]); err != nil {
		return Credential{}, err
	}
	if err = grantInitialPermissions(ctx, tx, project, actor.ID, c.PrincipalID, role); err != nil {
		return Credential{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO certificate_grants(project_id,fingerprint,granted_by,granted_at) VALUES(?,?,?,?) ON CONFLICT(project_id,fingerprint) DO NOTHING`, project, fingerprint, actor.ID, now); err != nil {
		return Credential{}, err
	}
	if expiresAt == 0 {
		if _, err = tx.ExecContext(ctx, `DELETE FROM certificate_grant_expirations WHERE project_id=? AND fingerprint=?`, project, fingerprint); err != nil {
			return Credential{}, err
		}
	} else if _, err = tx.ExecContext(ctx, `INSERT INTO certificate_grant_expirations(project_id,fingerprint,expires_at) VALUES(?,?,?) ON CONFLICT(project_id,fingerprint) DO UPDATE SET expires_at=excluded.expires_at`, project, fingerprint, expiresAt); err != nil {
		return Credential{}, err
	}
	if err = event(ctx, tx, project, actor.ID, "principal.enrolled", c.PrincipalID, map[string]string{"name": name, "role": role}); err != nil {
		return Credential{}, err
	}
	if err = event(ctx, tx, project, actor.ID, "certificate.granted", fingerprint, map[string]any{"fingerprint": fingerprint, "expires_at": expiresAt}); err != nil {
		return Credential{}, err
	}
	if err = tx.Commit(); err != nil {
		return Credential{}, err
	}
	return c, nil
}
