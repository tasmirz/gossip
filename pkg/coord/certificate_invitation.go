package coord

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// CertificateInvitation authorizes one CA-verified client certificate to
// acquire a project transport grant. It never supplies project membership.
type CertificateInvitation struct {
	ID                  string `json:"id"`
	ProjectID           string `json:"project_id"`
	CreatedBy           string `json:"created_by"`
	CreatedAt           int64  `json:"created_at"`
	ExpiresAt           int64  `json:"expires_at"`
	ExpectedFingerprint string `json:"expected_fingerprint,omitempty"`
	RedeemedFingerprint string `json:"redeemed_fingerprint,omitempty"`
	RedeemedAt          int64  `json:"redeemed_at,omitempty"`
	RevokedAt           int64  `json:"revoked_at,omitempty"`
}

// PrepareCertificateInvitation creates the secret before any server mutation.
// The caller must save it privately before submitting the invitation.
func PrepareCertificateInvitation() (string, string, error) {
	id, err := randomID("certinvite_")
	if err != nil {
		return "", "", err
	}
	token, _, err := newToken()
	return id, token, err
}

func validCertificateInvitation(id, token string) bool {
	if !strings.HasPrefix(id, "certinvite_") || len(id) != len("certinvite_")+32 || len(token) != 64 {
		return false
	}
	_, idErr := hex.DecodeString(strings.TrimPrefix(id, "certinvite_"))
	_, tokenErr := hex.DecodeString(token)
	return idErr == nil && tokenErr == nil
}

// AddPreparedCertificateInvitation records an owner-prepared, short-lived
// invitation. Retrying identical input returns the same metadata.
func (s *Store) AddPreparedCertificateInvitation(ctx context.Context, project, ownerToken, id, inviteToken, expectedFingerprint string, expiresAt int64) (CertificateInvitation, error) {
	var invite CertificateInvitation
	if !validCertificateInvitation(id, inviteToken) {
		return invite, errors.New("invalid prepared certificate invitation")
	}
	if expectedFingerprint != "" {
		var err error
		expectedFingerprint, err = NormalizeCertificateFingerprint(expectedFingerprint)
		if err != nil {
			return invite, err
		}
	}
	now := time.Now().UnixMilli()
	if expiresAt <= now || expiresAt > now+int64((24*time.Hour)/time.Millisecond) {
		return invite, errors.New("certificate invitation must expire within 24 hours")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return invite, err
	}
	defer tx.Rollback()
	actor, err := authenticate(ctx, tx, project, ownerToken, "owner")
	if err != nil {
		return invite, err
	}
	hash := sha256.Sum256([]byte(inviteToken))
	var existingHash []byte
	err = tx.QueryRowContext(ctx, `SELECT project_id,created_by,created_at,expires_at,expected_fingerprint,redeemed_fingerprint,redeemed_at,revoked_at,token_hash FROM certificate_invitations WHERE id=?`, id).Scan(&invite.ProjectID, &invite.CreatedBy, &invite.CreatedAt, &invite.ExpiresAt, &invite.ExpectedFingerprint, &invite.RedeemedFingerprint, &invite.RedeemedAt, &invite.RevokedAt, &existingHash)
	if err == nil {
		if invite.RevokedAt != 0 || invite.ProjectID != project || invite.ExpiresAt != expiresAt || invite.ExpectedFingerprint != expectedFingerprint || subtle.ConstantTimeCompare(existingHash, hash[:]) != 1 {
			return CertificateInvitation{}, ErrConflict
		}
		invite.ID = id
		return invite, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return invite, err
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM certificate_invitations WHERE project_id=? AND redeemed_at=0 AND revoked_at=0 AND expires_at>?`, project, now).Scan(&active); err != nil {
		return invite, err
	}
	if active >= 100 {
		return invite, errors.New("project has too many active certificate invitations")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO certificate_invitations(id,project_id,token_hash,created_by,created_at,expires_at,expected_fingerprint) VALUES(?,?,?,?,?,?,?)`, id, project, hash[:], actor.ID, now, expiresAt, expectedFingerprint); err != nil {
		return invite, err
	}
	if err = event(ctx, tx, project, actor.ID, "certificate.invitation_created", id, map[string]any{"expires_at": expiresAt, "expected_fingerprint": expectedFingerprint}); err != nil {
		return invite, err
	}
	if err = tx.Commit(); err != nil {
		return invite, err
	}
	return CertificateInvitation{ID: id, ProjectID: project, CreatedBy: actor.ID, CreatedAt: now, ExpiresAt: expiresAt, ExpectedFingerprint: expectedFingerprint}, nil
}

func (s *Store) ListCertificateInvitations(ctx context.Context, project, ownerToken string) ([]CertificateInvitation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = authenticate(ctx, tx, project, ownerToken, "owner"); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,project_id,created_by,created_at,expires_at,expected_fingerprint,redeemed_fingerprint,redeemed_at,revoked_at FROM certificate_invitations WHERE project_id=? ORDER BY created_at DESC,id DESC LIMIT 100`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CertificateInvitation{}
	for rows.Next() {
		var invite CertificateInvitation
		if err = rows.Scan(&invite.ID, &invite.ProjectID, &invite.CreatedBy, &invite.CreatedAt, &invite.ExpiresAt, &invite.ExpectedFingerprint, &invite.RedeemedFingerprint, &invite.RedeemedAt, &invite.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, invite)
	}
	return out, rows.Err()
}

func (s *Store) RevokeCertificateInvitation(ctx context.Context, project, ownerToken, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := authenticate(ctx, tx, project, ownerToken, "owner")
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE certificate_invitations SET revoked_at=? WHERE id=? AND project_id=? AND redeemed_at=0 AND revoked_at=0`, time.Now().UnixMilli(), id, project)
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
	if err = event(ctx, tx, project, actor.ID, "certificate.invitation_revoked", id, map[string]any{}); err != nil {
		return err
	}
	return tx.Commit()
}

// RedeemCertificateInvitation is called only after the RPC transport verifies
// the client certificate. The supplied fingerprint must come from that peer.
func (s *Store) RedeemCertificateInvitation(ctx context.Context, project, id, inviteToken, fingerprint string) (CertificateGrant, error) {
	var grant CertificateGrant
	if !validCertificateInvitation(id, inviteToken) {
		return grant, ErrUnauthorized
	}
	fingerprint, err := NormalizeCertificateFingerprint(fingerprint)
	if err != nil {
		return grant, ErrUnauthorized
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return grant, err
	}
	defer tx.Rollback()
	var invitedBy, expected, redeemed string
	var expiresAt, redeemedAt, revokedAt int64
	var storedHash []byte
	err = tx.QueryRowContext(ctx, `SELECT i.created_by,i.expires_at,i.expected_fingerprint,i.redeemed_fingerprint,i.redeemed_at,i.revoked_at,i.token_hash FROM certificate_invitations i JOIN principals p ON p.id=i.created_by AND p.project_id=i.project_id AND p.revoked=0 AND p.role='owner' WHERE i.id=? AND i.project_id=?`, id, project).Scan(&invitedBy, &expiresAt, &expected, &redeemed, &redeemedAt, &revokedAt, &storedHash)
	if errors.Is(err, sql.ErrNoRows) {
		return grant, ErrUnauthorized
	}
	if err != nil {
		return grant, err
	}
	hash := sha256.Sum256([]byte(inviteToken))
	if subtle.ConstantTimeCompare(storedHash, hash[:]) != 1 {
		return grant, ErrUnauthorized
	}
	if revokedAt != 0 || expected != "" && expected != fingerprint || redeemedAt != 0 && redeemed != fingerprint {
		return grant, ErrUnauthorized
	}
	now := time.Now().UnixMilli()
	if redeemedAt == 0 {
		if expiresAt <= now {
			return grant, ErrUnauthorized
		}
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO certificate_grants(project_id,fingerprint,granted_by,granted_at) VALUES(?,?,?,?)`, project, fingerprint, invitedBy, now)
		if err != nil {
			return grant, err
		}
		inserted, err := res.RowsAffected()
		if err != nil {
			return grant, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE certificate_invitations SET redeemed_fingerprint=?,redeemed_at=? WHERE id=? AND project_id=? AND redeemed_at=0`, fingerprint, now, id, project); err != nil {
			return grant, err
		}
		if inserted == 1 {
			if err = event(ctx, tx, project, invitedBy, "certificate.granted", fingerprint, map[string]any{"source_invitation": id}); err != nil {
				return grant, err
			}
		}
		if err = event(ctx, tx, project, invitedBy, "certificate.invitation_redeemed", id, map[string]any{"fingerprint": fingerprint}); err != nil {
			return grant, err
		}
	}
	err = tx.QueryRowContext(ctx, `SELECT g.project_id,g.fingerprint,g.granted_by,g.granted_at,COALESCE(e.expires_at,0) FROM certificate_grants g LEFT JOIN certificate_grant_expirations e ON e.project_id=g.project_id AND e.fingerprint=g.fingerprint WHERE g.project_id=? AND g.fingerprint=? AND (e.expires_at IS NULL OR e.expires_at>?)`, project, fingerprint, now).Scan(&grant.ProjectID, &grant.Fingerprint, &grant.GrantedBy, &grant.GrantedAt, &grant.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return CertificateGrant{}, ErrUnauthorized
	}
	if err != nil {
		return CertificateGrant{}, err
	}
	if err = tx.Commit(); err != nil {
		return CertificateGrant{}, err
	}
	return grant, nil
}
