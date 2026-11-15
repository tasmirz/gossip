package coord

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
)

const (
	encryptedInviteFormat     = "gossip-encrypted-invite-v1"
	encryptedInviteKDF        = "pbkdf2-sha256"
	encryptedInviteIterations = 210000
	minInviteIterations       = 100000
	maxInviteIterations       = 2000000
	minInvitePassphraseLen    = 6
	maxInviteEnvelopeBytes    = 65536
)

// EncryptedEnrollmentEnvelope wraps an encrypted invitation bundle without
// leaking endpoints, home IDs, project IDs, or pairing secrets in cleartext.
type EncryptedEnrollmentEnvelope struct {
	Format     string `json:"format"`
	KDF        string `json:"kdf"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

// EncryptedInvitePayload contains everything required on a peer device to
// decrypt, verify the home TLS CA, authenticate the invitation, and request enrollment.
type EncryptedInvitePayload struct {
	Version         int              `json:"version"`
	ProjectName     string           `json:"project_name,omitempty"`
	Mode            string           `json:"mode,omitempty"` // "lan", "frp", or "ssh"
	Invitation      RemoteInvitation `json:"invitation"`
	Endpoint        string           `json:"endpoint"`
	ServerCA        string           `json:"server_ca"`
	HostFingerprint string           `json:"host_fingerprint,omitempty"`
	HomeID          string           `json:"home_id"`
	Code            string           `json:"code"`
	SSHTarget       string           `json:"ssh_target,omitempty"`
	SSHPort         int              `json:"ssh_port,omitempty"`
	SSHRemotePort   int              `json:"ssh_remote_port,omitempty"`
}

// GenerateInvitePIN produces a uniform 6-digit cryptographic numeric PIN (e.g. "482917")
// for frictionless file + PIN invite sharing paired with manual host approval.
func GenerateInvitePIN() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// GenerateInvitePassphrase produces a high-entropy readable passphrase formatted
// in 4 hyphen-separated groups using an unambiguous alphabet (~80 bits).
func GenerateInvitePassphrase() (string, error) {
	const alphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	var b strings.Builder
	b.Grow(19)
	for i, v := range raw {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(alphabet[int(v)%len(alphabet)])
	}
	return b.String(), nil
}

// NormalizeInvitePassphrase validates and normalizes a user-supplied or
// generated invite passphrase without mutating internal case/characters when custom.
func NormalizeInvitePassphrase(passphrase string) (string, error) {
	trimmed := strings.TrimSpace(passphrase)
	if len(trimmed) < minInvitePassphraseLen {
		return "", fmt.Errorf("passphrase must be at least %d characters", minInvitePassphraseLen)
	}
	if len(trimmed) > 512 {
		return "", errors.New("passphrase is too long")
	}
	return trimmed, nil
}

// EncryptEnrollmentBundle symmetrically encrypts an invite payload with
// AES-256-GCM using a key derived from passphrase via PBKDF2-HMAC-SHA256.
func EncryptEnrollmentBundle(payload EncryptedInvitePayload, passphrase string) ([]byte, error) {
	pass, err := NormalizeInvitePassphrase(passphrase)
	if err != nil {
		return nil, err
	}
	if payload.Version == 0 {
		payload.Version = 1
	}
	if err := validateEnrollmentHome(payload.Endpoint, payload.ServerCA); err != nil {
		return nil, err
	}
	if payload.Invitation.ID == "" || payload.Invitation.Project == "" || payload.HomeID == "" || len(payload.Code) != 64 {
		return nil, errors.New("incomplete enrollment payload")
	}
	plain, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err = io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	key, err := pbkdf2.Key(sha256.New, pass, salt, encryptedInviteIterations, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nil, nonce, plain, []byte(encryptedInviteFormat))
	env := EncryptedEnrollmentEnvelope{
		Format:     encryptedInviteFormat,
		KDF:        encryptedInviteKDF,
		Iterations: encryptedInviteIterations,
		Salt:       base64.StdEncoding.EncodeToString(salt),
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(ciphertext),
	}
	out, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// DecryptEnrollmentBundle decrypts and authenticates a symmetrically encrypted
// invite envelope using passphrase.
func DecryptEnrollmentBundle(raw []byte, passphrase string) (EncryptedInvitePayload, error) {
	var payload EncryptedInvitePayload
	if len(raw) == 0 || len(raw) > maxInviteEnvelopeBytes {
		return payload, errors.New("invalid encrypted invite file size")
	}
	pass, err := NormalizeInvitePassphrase(passphrase)
	if err != nil {
		return payload, err
	}
	var env EncryptedEnrollmentEnvelope
	if err = json.Unmarshal(raw, &env); err != nil {
		return payload, errors.New("invalid encrypted invite file")
	}
	if env.Format != encryptedInviteFormat || env.KDF != encryptedInviteKDF {
		return payload, errors.New("unsupported encrypted invite format")
	}
	if env.Iterations < minInviteIterations || env.Iterations > maxInviteIterations {
		return payload, errors.New("invalid encrypted invite KDF parameters")
	}
	salt, err := base64.StdEncoding.DecodeString(env.Salt)
	if err != nil || len(salt) < 16 || len(salt) > 64 {
		return payload, errors.New("invalid encrypted invite salt")
	}
	nonce, err := base64.StdEncoding.DecodeString(env.Nonce)
	if err != nil || len(nonce) != 12 {
		return payload, errors.New("invalid encrypted invite nonce")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(env.Ciphertext)
	if err != nil || len(ciphertext) < 16 {
		return payload, errors.New("invalid encrypted invite ciphertext")
	}
	key, err := pbkdf2.Key(sha256.New, pass, salt, env.Iterations, 32)
	if err != nil {
		return payload, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return payload, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return payload, err
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, []byte(encryptedInviteFormat))
	if err != nil {
		return payload, errors.New("wrong passphrase or damaged encrypted invite file")
	}
	if err = json.Unmarshal(plain, &payload); err != nil {
		return payload, errors.New("corrupted encrypted invite payload")
	}
	if payload.Version != 1 || payload.Invitation.ID == "" || payload.Invitation.Project == "" || payload.HomeID == "" || len(payload.Code) != 64 {
		return payload, errors.New("incomplete decrypted invite payload")
	}
	if err = validateEnrollmentHome(payload.Endpoint, payload.ServerCA); err != nil {
		return payload, err
	}
	return payload, nil
}

// IsEncryptedInviteEnvelope reports whether raw bytes appear to be a valid
// Gossip symmetrically encrypted invite envelope.
func IsEncryptedInviteEnvelope(raw []byte) bool {
	if len(raw) == 0 || len(raw) > maxInviteEnvelopeBytes {
		return false
	}
	var probe struct {
		Format string `json:"format"`
	}
	return json.Unmarshal(raw, &probe) == nil && probe.Format == encryptedInviteFormat
}
