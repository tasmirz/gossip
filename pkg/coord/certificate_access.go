package coord

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

type CertificateGrant struct {
	ProjectID   string `json:"project_id"`
	Fingerprint string `json:"fingerprint"`
	GrantedBy   string `json:"granted_by"`
	GrantedAt   int64  `json:"granted_at"`
	ExpiresAt   int64  `json:"expires_at,omitempty"`
}

func NormalizeCertificateFingerprint(value string) (string, error) {
	value = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), ":", ""))
	b, err := hex.DecodeString(value)
	if err != nil || len(b) != sha256.Size {
		return "", errors.New("certificate fingerprint must be a SHA-256 hex digest")
	}
	return hex.EncodeToString(b), nil
}

// AddCertificateGrant allows one verified client certificate to authenticate
// to a project. The principal credential still controls role-level actions.
func (s *Store) AddCertificateGrant(ctx context.Context, project, token, fingerprint string) (CertificateGrant, error) {
	return s.AddCertificateGrantUntil(ctx, project, token, fingerprint, 0)
}

// AddCertificateGrantUntil grants or renews access. A zero expiry is indefinite;
// any other expiry is an absolute Unix millisecond timestamp in the future.
func (s *Store) AddCertificateGrantUntil(ctx context.Context, project, token, fingerprint string, expiresAt int64) (CertificateGrant, error) {
	var grant CertificateGrant
	fingerprint, err := NormalizeCertificateFingerprint(fingerprint)
	if err != nil {
		return grant, err
	}
	now := time.Now().UnixMilli()
	if expiresAt != 0 && expiresAt <= now {
		return grant, errors.New("certificate grant expiry must be in the future")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return grant, err
	}
	defer tx.Rollback()
	actor, err := authenticate(ctx, tx, project, token, "owner")
	if err != nil {
		return grant, err
	}
	res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO certificate_grants(project_id,fingerprint,granted_by,granted_at) VALUES(?,?,?,?)`, project, fingerprint, actor.ID, now)
	if err != nil {
		return grant, err
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return grant, err
	}
	var previous int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(e.expires_at,0) FROM certificate_grants g LEFT JOIN certificate_grant_expirations e ON e.project_id=g.project_id AND e.fingerprint=g.fingerprint WHERE g.project_id=? AND g.fingerprint=?`, project, fingerprint).Scan(&previous); err != nil {
		return grant, err
	}
	if expiresAt == 0 {
		_, err = tx.ExecContext(ctx, `DELETE FROM certificate_grant_expirations WHERE project_id=? AND fingerprint=?`, project, fingerprint)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO certificate_grant_expirations(project_id,fingerprint,expires_at) VALUES(?,?,?) ON CONFLICT(project_id,fingerprint) DO UPDATE SET expires_at=excluded.expires_at`, project, fingerprint, expiresAt)
	}
	if err != nil {
		return grant, err
	}
	if inserted == 1 || previous != expiresAt {
		if err = event(ctx, tx, project, actor.ID, "certificate.granted", fingerprint, map[string]any{"fingerprint": fingerprint, "expires_at": expiresAt}); err != nil {
			return grant, err
		}
	}
	if err = tx.QueryRowContext(ctx, `SELECT g.project_id,g.fingerprint,g.granted_by,g.granted_at,COALESCE(e.expires_at,0) FROM certificate_grants g LEFT JOIN certificate_grant_expirations e ON e.project_id=g.project_id AND e.fingerprint=g.fingerprint WHERE g.project_id=? AND g.fingerprint=?`, project, fingerprint).Scan(&grant.ProjectID, &grant.Fingerprint, &grant.GrantedBy, &grant.GrantedAt, &grant.ExpiresAt); err != nil {
		return CertificateGrant{}, err
	}
	if err = tx.Commit(); err != nil {
		return CertificateGrant{}, err
	}
	return grant, nil
}

func (s *Store) ListCertificateGrants(ctx context.Context, project, token string) ([]CertificateGrant, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = authenticate(ctx, tx, project, token, "owner"); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT g.project_id,g.fingerprint,g.granted_by,g.granted_at,COALESCE(e.expires_at,0) FROM certificate_grants g LEFT JOIN certificate_grant_expirations e ON e.project_id=g.project_id AND e.fingerprint=g.fingerprint WHERE g.project_id=? ORDER BY g.granted_at,g.fingerprint`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CertificateGrant{}
	for rows.Next() {
		var grant CertificateGrant
		if err = rows.Scan(&grant.ProjectID, &grant.Fingerprint, &grant.GrantedBy, &grant.GrantedAt, &grant.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, grant)
	}
	return out, rows.Err()
}

func (s *Store) RevokeCertificateGrant(ctx context.Context, project, token, fingerprint string) error {
	fingerprint, err := NormalizeCertificateFingerprint(fingerprint)
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
	res, err := tx.ExecContext(ctx, `DELETE FROM certificate_grants WHERE project_id=? AND fingerprint=?`, project, fingerprint)
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
	if err = event(ctx, tx, project, actor.ID, "certificate.revoked", fingerprint, map[string]string{"fingerprint": fingerprint}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CertificateAuthorized(ctx context.Context, project, fingerprint string) (bool, error) {
	fingerprint, err := NormalizeCertificateFingerprint(fingerprint)
	if err != nil {
		return false, err
	}
	var found int
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM certificate_grants g LEFT JOIN certificate_grant_expirations e ON e.project_id=g.project_id AND e.fingerprint=g.fingerprint WHERE g.project_id=? AND g.fingerprint=? AND (e.expires_at IS NULL OR e.expires_at>?)`, project, fingerprint, time.Now().UnixMilli()).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
