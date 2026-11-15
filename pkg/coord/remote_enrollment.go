package coord

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"time"
)

type RemoteInvitation struct {
	ID               string `json:"id"`
	Project          string `json:"project_id"`
	Name             string `json:"name"`
	Role             string `json:"role"`
	State            string `json:"state"`
	Fingerprint      string `json:"fingerprint,omitempty"`
	SSHFingerprint   string `json:"ssh_fingerprint,omitempty"`
	VerificationCode string `json:"verification_code,omitempty"`
	ExpiresAt        int64  `json:"expires_at"`
	AutoApprove      bool   `json:"auto_approve,omitempty"`
}

// SSHFingerprintSHA256 formats raw DER bytes as an OpenSSH-style SHA256 fingerprint.
func SSHFingerprintSHA256(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// HostCertSSHFingerprint parses the first PEM CERTIFICATE block and returns its SHA256 fingerprint.
func HostCertSSHFingerprint(certPEM string) string {
	block, _ := pem.Decode([]byte(strings.TrimSpace(certPEM)))
	if block == nil || len(block.Bytes) == 0 {
		return ""
	}
	return SSHFingerprintSHA256(block.Bytes)
}

// PeerVerificationCode derives a deterministic human-readable SAS code (XXXX-XXXX)
// from the device's public key fingerprint hex and invitation ID so both host and
// joining peer can compare codes before manual approval.
func PeerVerificationCode(fingerprintHex, inviteID string) string {
	fp := strings.ToLower(strings.TrimSpace(fingerprintHex))
	id := strings.TrimSpace(inviteID)
	if fp == "" || id == "" {
		return ""
	}
	const alphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	sum := sha256.Sum256([]byte("gossip-verify-v1\x00" + fp + "\x00" + id))
	var b strings.Builder
	b.Grow(9)
	for i := range 8 {
		if i == 4 {
			b.WriteByte('-')
		}
		b.WriteByte(alphabet[int(sum[i])%len(alphabet)])
	}
	return b.String()
}

func populateVerificationDisplay(inv *RemoteInvitation) {
	if inv == nil || inv.Fingerprint == "" || inv.ID == "" {
		return
	}
	inv.VerificationCode = PeerVerificationCode(inv.Fingerprint, inv.ID)
	if raw, err := hex.DecodeString(strings.TrimSpace(inv.Fingerprint)); err == nil && len(raw) == sha256.Size {
		inv.SSHFingerprint = "SHA256:" + base64.RawStdEncoding.EncodeToString(raw)
	}
}

func (s *Store) CreateRemoteInvitation(ctx context.Context, project, token, name, role string) (RemoteInvitation, string, error) {
	return s.CreateRemoteInvitationWithOptions(ctx, project, token, name, role, false)
}

// CreateRemoteInvitationWithOptions issues a remote invitation. Manual owner approval
// with verification code comparison is always required (auto-approve is disabled).
func (s *Store) CreateRemoteInvitationWithOptions(ctx context.Context, project, token, name, role string, _ bool) (RemoteInvitation, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 160 {
		return RemoteInvitation{}, "", errors.New("invitee name required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RemoteInvitation{}, "", err
	}
	defer tx.Rollback()
	actor, err := authenticate(ctx, tx, project, token, "owner")
	if err != nil {
		return RemoteInvitation{}, "", err
	}
	if err = enrollmentEnabled(ctx, tx); err != nil {
		return RemoteInvitation{}, "", err
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM project_roles WHERE project_id=? AND id=?)`, project, role).Scan(&exists); err != nil {
		return RemoteInvitation{}, "", err
	}
	if !exists && role != "viewer" && role != "worker" && role != "reviewer" {
		return RemoteInvitation{}, "", errors.New("invalid invite role")
	}
	id, err := randomID("invite_")
	if err != nil {
		return RemoteInvitation{}, "", err
	}
	code, hash, err := newToken()
	if err != nil {
		return RemoteInvitation{}, "", err
	}
	inv := RemoteInvitation{ID: id, Project: project, Name: name, Role: role, State: "issued", ExpiresAt: time.Now().Add(30 * time.Minute).UnixMilli(), AutoApprove: false}
	_, err = tx.ExecContext(ctx, `INSERT INTO remote_enrollments(id,project_id,name,role_id,state,expires_at,code_hash,issued_by,auto_approve) VALUES(?,?,?,?,?,?,?,?,?)`, id, project, name, role, inv.State, inv.ExpiresAt, hash, actor.ID, 0)
	if err != nil {
		return RemoteInvitation{}, "", err
	}
	if err = event(ctx, tx, project, actor.ID, "enrollment.issued", id, inv); err != nil {
		return RemoteInvitation{}, "", err
	}
	if err = tx.Commit(); err != nil {
		return RemoteInvitation{}, "", err
	}
	return inv, code, nil
}

func enrollmentEnabled(ctx context.Context, tx *sql.Tx) error {
	var enabled bool
	if err := tx.QueryRowContext(ctx, `SELECT enabled FROM remote_access_control WHERE id=1`).Scan(&enabled); err != nil {
		return err
	}
	if !enabled {
		return ErrUnauthorized
	}
	return nil
}

// AcceptRemoteInvitation persists a verified certificate request. Access is
// granted only by a later owner decision and certificate finalization.
func (s *Store) AcceptRemoteInvitation(ctx context.Context, project, id, code string, der []byte) (RemoteInvitation, error) {
	var inv RemoteInvitation
	if len(der) == 0 || len(der) > 8192 || len(code) != 64 {
		return inv, ErrUnauthorized
	}
	csr, err := x509.ParseCertificateRequest(der)
	if err != nil || csr.CheckSignature() != nil {
		return inv, errors.New("invalid device certificate request")
	}
	fingerprint := sha256.Sum256(csr.RawSubjectPublicKeyInfo)
	keyID := hex.EncodeToString(fingerprint[:])
	hash := sha256.Sum256([]byte(code))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return inv, err
	}
	defer tx.Rollback()
	if err = enrollmentEnabled(ctx, tx); err != nil {
		return inv, err
	}
	inv.ID, inv.Project = id, project
	var expected []byte
	var issuer string
	err = tx.QueryRowContext(ctx, `SELECT name,role_id,state,expires_at,code_hash,fingerprint,issued_by FROM remote_enrollments WHERE id=? AND project_id=?`, id, project).Scan(&inv.Name, &inv.Role, &inv.State, &inv.ExpiresAt, &expected, &inv.Fingerprint, &issuer)
	if errors.Is(err, sql.ErrNoRows) {
		return RemoteInvitation{}, ErrUnauthorized
	}
	if err != nil {
		return RemoteInvitation{}, err
	}
	inv.AutoApprove = false
	if subtle.ConstantTimeCompare(expected, hash[:]) != 1 || inv.ExpiresAt <= time.Now().UnixMilli() {
		return RemoteInvitation{}, ErrUnauthorized
	}
	if inv.State == "pending" || inv.State == "approved" || inv.State == "enrolled" {
		if inv.Fingerprint != keyID {
			return RemoteInvitation{}, ErrUnauthorized
		}
		populateVerificationDisplay(&inv)
		return inv, nil
	}
	if inv.State != "issued" {
		return RemoteInvitation{}, ErrUnauthorized
	}
	const nextState = "pending"
	inv.State, inv.Fingerprint = nextState, keyID
	populateVerificationDisplay(&inv)
	_, err = tx.ExecContext(ctx, `UPDATE remote_enrollments SET state=?,fingerprint=?,csr=? WHERE id=? AND project_id=?`, nextState, keyID, der, id, project)
	if err != nil {
		return RemoteInvitation{}, err
	}
	if err = event(ctx, tx, project, issuer, "enrollment.accepted", id, map[string]any{"invitation": inv, "actor_type": "device", "issued_by": issuer}); err != nil {
		return RemoteInvitation{}, err
	}
	return inv, tx.Commit()
}

func (s *Store) DecideRemoteInvitation(ctx context.Context, project, token, id, decision string) error {
	var from string
	switch decision {
	case "approved", "denied":
		from = "pending"
	case "cancelled":
	default:
		return errors.New("invalid enrollment decision")
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
	if decision == "approved" {
		if err = enrollmentEnabled(ctx, tx); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE remote_enrollments SET state=? WHERE project_id=? AND id=? AND expires_at>? AND (state=? OR (?='' AND state IN ('issued','pending','approved')))`, decision, project, id, time.Now().UnixMilli(), from, from)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	if err = event(ctx, tx, project, actor.ID, "enrollment."+decision, id, nil); err != nil {
		return err
	}
	return tx.Commit()
}

// IssueEnrollmentCertificate signs the approved CSR with the configured client
// CA. The certificate alone grants no access to ordinary peer operations.
func (s *Store) IssueEnrollmentCertificate(ctx context.Context, project, id, code string, csrDER []byte, ca *x509.Certificate, signer crypto.Signer) ([]byte, error) {
	if ca == nil || signer == nil || !ca.IsCA || len(csrDER) > 8192 || len(code) != 64 {
		return nil, ErrUnauthorized
	}
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil || csr.CheckSignature() != nil {
		return nil, ErrUnauthorized
	}
	key := sha256.Sum256(csr.RawSubjectPublicKeyInfo)
	supplied := sha256.Sum256([]byte(code))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = enrollmentEnabled(ctx, tx); err != nil {
		return nil, err
	}
	var expected, cert []byte
	var state, keyID string
	var expiry int64
	err = tx.QueryRowContext(ctx, `SELECT code_hash,state,fingerprint,expires_at,certificate FROM remote_enrollments WHERE project_id=? AND id=?`, project, id).Scan(&expected, &state, &keyID, &expiry, &cert)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if subtle.ConstantTimeCompare(supplied[:], expected) != 1 || hex.EncodeToString(key[:]) != keyID || expiry <= now.UnixMilli() || (state != "approved" && state != "enrolled") {
		return nil, ErrUnauthorized
	}
	if len(cert) > 0 {
		return cert, nil
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	until := now.Add(90 * 24 * time.Hour)
	if ca.NotAfter.Before(until) {
		until = ca.NotAfter
	}
	if !until.After(now) {
		return nil, errors.New("client CA has expired")
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: id}, NotBefore: now.Add(-time.Minute), NotAfter: until,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, BasicConstraintsValid: true,
	}
	cert, err = x509.CreateCertificate(rand.Reader, template, ca, csr.PublicKey, signer)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE remote_enrollments SET certificate=? WHERE project_id=? AND id=?`, cert, project, id); err != nil {
		return nil, err
	}
	return cert, tx.Commit()
}

// FinalizeRemoteInvitation activates a membership after the peer listener has
// verified the TLS client certificate. The client prepares and retains the
// credential locally so the home only needs to store its hash.
func (s *Store) FinalizeRemoteInvitation(ctx context.Context, project, id string, verifiedCert *x509.Certificate, c Credential) error {
	if verifiedCert == nil || !validPrepared(project, c) {
		return ErrUnauthorized
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = enrollmentEnabled(ctx, tx); err != nil {
		return err
	}
	var state, name, role, issuer, principal string
	var expiry int64
	var cert []byte
	err = tx.QueryRowContext(ctx, `SELECT state,name,role_id,issued_by,expires_at,certificate,enrolled_principal FROM remote_enrollments WHERE project_id=? AND id=?`, project, id).Scan(&state, &name, &role, &issuer, &expiry, &cert, &principal)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUnauthorized
	}
	if err != nil {
		return err
	}
	now := time.Now()
	if expiry <= now.UnixMilli() || !now.Before(verifiedCert.NotAfter) || len(cert) == 0 || !bytes.Equal(cert, verifiedCert.Raw) {
		return ErrUnauthorized
	}
	digest := sha256.Sum256(cert)
	fingerprint := hex.EncodeToString(digest[:])
	hash := sha256.Sum256([]byte(c.Token))
	if state == "enrolled" {
		if principal != c.PrincipalID {
			return ErrUnauthorized
		}
		var active bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM principals p JOIN certificate_grants g ON p.project_id=g.project_id
   LEFT JOIN certificate_grant_expirations e ON e.project_id=g.project_id AND e.fingerprint=g.fingerprint
   WHERE p.project_id=? AND p.id=? AND p.token_hash=? AND p.revoked=0 AND g.fingerprint=? AND (e.expires_at IS NULL OR e.expires_at>?))`, project, principal, hash[:], fingerprint, now.UnixMilli()).Scan(&active)
		if err != nil {
			return err
		}
		if !active {
			return ErrUnauthorized
		}
		return nil
	}
	if state != "approved" {
		return ErrUnauthorized
	}
	baseRole := "worker"
	if role == "reviewer" {
		baseRole = "reviewer"
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO principals(id,project_id,name,role,token_hash) VALUES(?,?,?,?,?)`, c.PrincipalID, project, name, baseRole, hash[:]); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO project_role_assignments VALUES(?,?,?)`, project, c.PrincipalID, role); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO certificate_grants(project_id,fingerprint,granted_by,granted_at) VALUES(?,?,?,?)`, project, fingerprint, issuer, now.UnixMilli()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO certificate_grant_expirations(project_id,fingerprint,expires_at) VALUES(?,?,?)`, project, fingerprint, verifiedCert.NotAfter.UnixMilli()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE remote_enrollments SET state='enrolled',enrolled_principal=? WHERE project_id=? AND id=?`, c.PrincipalID, project, id); err != nil {
		return err
	}
	if err = event(ctx, tx, project, issuer, "enrollment.finalized", id, map[string]string{"principal_id": c.PrincipalID, "certificate_fingerprint": fingerprint, "role_id": role}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RemoteInvitations(ctx context.Context, project, token string) ([]RemoteInvitation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = authenticate(ctx, tx, project, token, "owner"); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,name,role_id,CASE WHEN expires_at<=? AND state IN ('issued','pending','approved') THEN 'expired' ELSE state END,fingerprint,expires_at FROM remote_enrollments WHERE project_id=? ORDER BY expires_at DESC,id LIMIT 100`, time.Now().UnixMilli(), project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RemoteInvitation{}
	for rows.Next() {
		item := RemoteInvitation{Project: project}
		if err := rows.Scan(&item.ID, &item.Name, &item.Role, &item.State, &item.Fingerprint, &item.ExpiresAt); err != nil {
			return nil, err
		}
		populateVerificationDisplay(&item)
		out = append(out, item)
	}
	return out, rows.Err()
}
