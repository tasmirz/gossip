package coord

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Message struct {
	AcknowledgedByMe  bool   `json:"acknowledged_by_me,omitempty"`
	Sequence          int64  `json:"sequence"`
	ID                string `json:"id"`
	Hash              string `json:"hash,omitempty"`
	Sender            string `json:"sender"`
	Recipient         string `json:"recipient,omitempty"`
	ThreadID          string `json:"thread_id"`
	Subject           string `json:"subject"`
	Body              string `json:"body"`
	CreatedAt         int64  `json:"created_at"`
	CheckAfterSeconds int    `json:"check_after_seconds,omitempty"`
	CheckAfterAt      int64  `json:"check_after_at,omitempty"`
	Mode              string `json:"mode,omitempty"`
	TTLSeconds        int    `json:"ttl_seconds,omitempty"`
	ExpiresAt         int64  `json:"expires_at,omitempty"`
	RTOSeconds        int    `json:"rto_seconds,omitempty"`
	Expired           bool   `json:"expired,omitempty"`
}

const maxCheckAfterSeconds = 24 * 60 * 60

// MessageOptions carries optional coordination mode and timing parameters for
// sending or acknowledging messages.
type MessageOptions struct {
	Mode              string
	CheckAfterSeconds int
	TTLSeconds        int
	RTOSeconds        int
}

// PeerMode advertises a peer's active coordination mode and timing hints.
type PeerMode struct {
	PrincipalID       string `json:"principal_id"`
	Name              string `json:"name,omitempty"`
	Role              string `json:"role,omitempty"`
	Mode              string `json:"mode"`
	CheckAfterSeconds int    `json:"check_after_seconds,omitempty"`
	CheckAfterAt      int64  `json:"check_after_at,omitempty"`
	TTLSeconds        int    `json:"ttl_seconds,omitempty"`
	RTOSeconds        int    `json:"rto_seconds,omitempty"`
	UpdatedAt         int64  `json:"updated_at"`
}

// RTODueMessage reports an unacknowledged outbound message whose RTO timer has
// elapsed without expiring its TTL, along with TCP-like exponential backoff.
type RTODueMessage struct {
	Sequence       int64  `json:"sequence"`
	ID             string `json:"id"`
	Recipient      string `json:"recipient,omitempty"`
	ThreadID       string `json:"thread_id"`
	Subject        string `json:"subject"`
	CreatedAt      int64  `json:"created_at"`
	Mode           string `json:"mode,omitempty"`
	RTOSeconds     int    `json:"rto_seconds"`
	NextRTOSeconds int    `json:"next_rto_seconds"`
	DueAt          int64  `json:"due_at"`
	TTLSeconds     int    `json:"ttl_seconds,omitempty"`
	ExpiresAt      int64  `json:"expires_at,omitempty"`
}

// NormalizeAgentMode validates an advertised peer coordination mode.
func NormalizeAgentMode(raw string) (string, error) {
	m := strings.ToLower(strings.TrimSpace(raw))
	switch m {
	case "", "working", "planning", "decision", "communication":
		return m, nil
	default:
		return "", fmt.Errorf("invalid mode %q: must be working, planning, decision, or communication", raw)
	}
}

// ParseDurationSeconds parses a relative duration parameter (e.g. "3m", "90s", or integer seconds).
func ParseDurationSeconds(label string, raw any) (int, error) {
	if raw == nil {
		return 0, nil
	}
	var secs int
	switch v := raw.(type) {
	case int:
		secs = v
	case int64:
		secs = int(v)
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Trunc(v) != v {
			return 0, fmt.Errorf("%s must be a whole number of seconds or duration string like 3m", label)
		}
		secs = int(v)
	case json.Number:
		n, err := strconv.Atoi(string(v))
		if err != nil {
			return 0, fmt.Errorf("invalid %s number: %w", label, err)
		}
		secs = n
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return 0, nil
		}
		if n, err := strconv.Atoi(s); err == nil {
			secs = n
		} else {
			d, derr := time.ParseDuration(s)
			if derr != nil {
				return 0, fmt.Errorf("invalid %s duration %q: %w", label, v, derr)
			}
			secs = int(math.Ceil(d.Seconds()))
		}
	default:
		return 0, fmt.Errorf("%s must be seconds or a duration string like 3m or 90s", label)
	}
	if secs < 0 || secs > maxCheckAfterSeconds {
		return 0, fmt.Errorf("%s must be between 0 and %d seconds", label, maxCheckAfterSeconds)
	}
	return secs, nil
}

// ParseCheckAfterSeconds parses a relative follow-up wait hint (e.g. "3m", "90s", or integer seconds).
func ParseCheckAfterSeconds(raw any) (int, error) {
	return ParseDurationSeconds("checkAfter", raw)
}

// MessageHash returns a stable SHA-256 hex digest identifying an immutable message record.
func MessageHash(m Message) string {
	payload, _ := json.Marshal([]any{m.ID, m.Sequence, m.Sender, m.Recipient, m.ThreadID, m.Subject, m.Body, m.CreatedAt})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

type MessageAcknowledgment struct {
	MessageID         string `json:"message_id"`
	PrincipalID       string `json:"principal_id"`
	AcknowledgedAt    int64  `json:"acknowledged_at"`
	CheckAfterSeconds int    `json:"check_after_seconds,omitempty"`
	CheckAfterAt      int64  `json:"check_after_at,omitempty"`
	Mode              string `json:"mode,omitempty"`
}

func upsertPeerModeTx(ctx context.Context, tx *sql.Tx, project, principalID, mode string, checkAfterSeconds, ttlSeconds, rtoSeconds int, now int64) (PeerMode, error) {
	var checkAfterAt int64
	if checkAfterSeconds > 0 {
		checkAfterAt = now + int64(checkAfterSeconds)*1000
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO peer_modes(project_id,principal_id,mode,check_after_seconds,check_after_at,ttl_seconds,rto_seconds,updated_at)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(project_id,principal_id) DO UPDATE SET
			mode=excluded.mode,
			check_after_seconds=CASE WHEN excluded.check_after_seconds>0 THEN excluded.check_after_seconds ELSE peer_modes.check_after_seconds END,
			check_after_at=CASE WHEN excluded.check_after_at>0 THEN excluded.check_after_at ELSE peer_modes.check_after_at END,
			ttl_seconds=CASE WHEN excluded.ttl_seconds>0 THEN excluded.ttl_seconds ELSE peer_modes.ttl_seconds END,
			rto_seconds=CASE WHEN excluded.rto_seconds>0 THEN excluded.rto_seconds ELSE peer_modes.rto_seconds END,
			updated_at=excluded.updated_at`,
		project, principalID, mode, checkAfterSeconds, checkAfterAt, ttlSeconds, rtoSeconds, now)
	if err != nil {
		return PeerMode{}, err
	}
	var pm PeerMode
	err = tx.QueryRowContext(ctx, `
		SELECT pm.principal_id, COALESCE(p.name,''), COALESCE(p.role,''), pm.mode, pm.check_after_seconds, pm.check_after_at, pm.ttl_seconds, pm.rto_seconds, pm.updated_at
		FROM peer_modes pm
		LEFT JOIN principals p ON p.id=pm.principal_id AND p.project_id=pm.project_id
		WHERE pm.project_id=? AND pm.principal_id=?`, project, principalID).
		Scan(&pm.PrincipalID, &pm.Name, &pm.Role, &pm.Mode, &pm.CheckAfterSeconds, &pm.CheckAfterAt, &pm.TTLSeconds, &pm.RTOSeconds, &pm.UpdatedAt)
	return pm, err
}

func listPeerModesTx(ctx context.Context, tx *sql.Tx, project string) ([]PeerMode, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT pm.principal_id, p.name, p.role, pm.mode, pm.check_after_seconds, pm.check_after_at, pm.ttl_seconds, pm.rto_seconds, pm.updated_at
		FROM peer_modes pm
		JOIN principals p ON p.id=pm.principal_id AND p.project_id=pm.project_id
		WHERE pm.project_id=? AND p.revoked=0 AND pm.mode<>''
		ORDER BY pm.updated_at DESC, pm.principal_id ASC`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PeerMode{}
	for rows.Next() {
		var pm PeerMode
		if err = rows.Scan(&pm.PrincipalID, &pm.Name, &pm.Role, &pm.Mode, &pm.CheckAfterSeconds, &pm.CheckAfterAt, &pm.TTLSeconds, &pm.RTOSeconds, &pm.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, pm)
	}
	return out, rows.Err()
}

// SetPeerMode advertises the caller's active coordination mode (working,
// planning, decision, communication) and default timing parameters to peers.
func (s *Store) SetPeerMode(ctx context.Context, project, token, mode string, checkAfterSeconds, ttlSeconds, rtoSeconds int) (PeerMode, error) {
	var pm PeerMode
	normMode, err := NormalizeAgentMode(mode)
	if err != nil {
		return pm, err
	}
	if normMode == "" {
		return pm, errors.New("mode is required (working, planning, decision, or communication)")
	}
	if checkAfterSeconds < 0 || checkAfterSeconds > maxCheckAfterSeconds {
		return pm, fmt.Errorf("checkAfter must be between 0 and %d seconds", maxCheckAfterSeconds)
	}
	if ttlSeconds < 0 || ttlSeconds > maxCheckAfterSeconds {
		return pm, fmt.Errorf("ttl must be between 0 and %d seconds", maxCheckAfterSeconds)
	}
	if rtoSeconds < 0 || rtoSeconds > maxCheckAfterSeconds {
		return pm, fmt.Errorf("rto must be between 0 and %d seconds", maxCheckAfterSeconds)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return pm, err
	}
	defer tx.Rollback()
	actor, err := authorizeProjectRead(ctx, tx, project, token)
	if err != nil {
		return pm, err
	}
	now := time.Now().UnixMilli()
	// Explicit SetPeerMode replaces any prior checkAfter/ttl/rto values with the newly advertised values.
	var checkAfterAt int64
	if checkAfterSeconds > 0 {
		checkAfterAt = now + int64(checkAfterSeconds)*1000
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO peer_modes(project_id,principal_id,mode,check_after_seconds,check_after_at,ttl_seconds,rto_seconds,updated_at)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(project_id,principal_id) DO UPDATE SET
			mode=excluded.mode,
			check_after_seconds=excluded.check_after_seconds,
			check_after_at=excluded.check_after_at,
			ttl_seconds=excluded.ttl_seconds,
			rto_seconds=excluded.rto_seconds,
			updated_at=excluded.updated_at`,
		project, actor.ID, normMode, checkAfterSeconds, checkAfterAt, ttlSeconds, rtoSeconds, now); err != nil {
		return pm, err
	}
	err = tx.QueryRowContext(ctx, `
		SELECT pm.principal_id, COALESCE(p.name,''), COALESCE(p.role,''), pm.mode, pm.check_after_seconds, pm.check_after_at, pm.ttl_seconds, pm.rto_seconds, pm.updated_at
		FROM peer_modes pm
		LEFT JOIN principals p ON p.id=pm.principal_id AND p.project_id=pm.project_id
		WHERE pm.project_id=? AND pm.principal_id=?`, project, actor.ID).
		Scan(&pm.PrincipalID, &pm.Name, &pm.Role, &pm.Mode, &pm.CheckAfterSeconds, &pm.CheckAfterAt, &pm.TTLSeconds, &pm.RTOSeconds, &pm.UpdatedAt)
	if err != nil {
		return pm, err
	}
	evPayload := map[string]any{"mode": normMode}
	if checkAfterSeconds > 0 {
		evPayload["check_after_seconds"] = checkAfterSeconds
		evPayload["check_after_at"] = checkAfterAt
	}
	if ttlSeconds > 0 {
		evPayload["ttl_seconds"] = ttlSeconds
	}
	if rtoSeconds > 0 {
		evPayload["rto_seconds"] = rtoSeconds
	}
	if err = event(ctx, tx, project, actor.ID, "agent.mode_changed", actor.ID, evPayload); err != nil {
		return PeerMode{}, err
	}
	if err = tx.Commit(); err != nil {
		return PeerMode{}, err
	}
	return pm, nil
}

// ListPeerModes returns all active peer coordination modes advertised in the project.
func (s *Store) ListPeerModes(ctx context.Context, project, token string) ([]PeerMode, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = authorizeProjectRead(ctx, tx, project, token); err != nil {
		return nil, err
	}
	return listPeerModesTx(ctx, tx, project)
}

// AcknowledgeMessage explicitly records a recipient's acknowledgment. Merely
// listing a message does not prove that an agent processed it.
func (s *Store) AcknowledgeMessage(ctx context.Context, project, token, id string) (MessageAcknowledgment, error) {
	return s.AcknowledgeMessageWithOptions(ctx, project, token, id, MessageOptions{})
}

// AcknowledgeMessageWithCheckAfter records an explicit recipient receipt along
// with an optional ETA hint (checkAfterSeconds) telling peers when to follow up.
func (s *Store) AcknowledgeMessageWithCheckAfter(ctx context.Context, project, token, id string, checkAfterSeconds int) (MessageAcknowledgment, error) {
	return s.AcknowledgeMessageWithOptions(ctx, project, token, id, MessageOptions{CheckAfterSeconds: checkAfterSeconds})
}

// AcknowledgeMessageWithOptions records an explicit recipient receipt along
// with optional coordination mode and checkAfter ETA hint.
func (s *Store) AcknowledgeMessageWithOptions(ctx context.Context, project, token, id string, opts MessageOptions) (MessageAcknowledgment, error) {
	var ack MessageAcknowledgment
	mode, err := NormalizeAgentMode(opts.Mode)
	if err != nil {
		return ack, err
	}
	checkAfterSeconds := opts.CheckAfterSeconds
	if checkAfterSeconds < 0 || checkAfterSeconds > maxCheckAfterSeconds {
		return ack, fmt.Errorf("checkAfter must be between 0 and %d seconds", maxCheckAfterSeconds)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ack, err
	}
	defer tx.Rollback()
	actor, err := authorizeProjectRead(ctx, tx, project, token)
	if err != nil {
		return ack, err
	}
	var sender string
	var recipient sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT sender,recipient FROM messages WHERE id=? AND project_id=? AND (recipient IS NOT NULL OR `+visibleMessageSQL("messages")+`)`, id, project, actor.Role, actor.ID, actor.ID, actor.ID, actor.ID).Scan(&sender, &recipient)
	if errors.Is(err, sql.ErrNoRows) {
		return ack, ErrNotFound
	}
	if err != nil {
		return ack, err
	}
	if actor.ID == sender || (recipient.Valid && recipient.String != actor.ID) {
		return ack, ErrUnauthorized
	}
	now := time.Now().UnixMilli()
	var checkAfterAt int64
	if checkAfterSeconds > 0 {
		checkAfterAt = now + int64(checkAfterSeconds)*1000
	}
	res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO message_acknowledgments(message_id,principal_id,acknowledged_at,check_after_seconds,check_after_at,mode) VALUES(?,?,?,?,?,?)`, id, actor.ID, now, checkAfterSeconds, checkAfterAt, mode)
	if err != nil {
		return ack, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return ack, err
	}
	emitAckEvent := n == 1
	if n == 0 && (checkAfterSeconds > 0 || mode != "") {
		if _, err = tx.ExecContext(ctx, `
			UPDATE message_acknowledgments SET
				check_after_seconds=CASE WHEN ?>0 THEN ? ELSE check_after_seconds END,
				check_after_at=CASE WHEN ?>0 THEN ? ELSE check_after_at END,
				mode=CASE WHEN ?<>'' THEN ? ELSE mode END
			WHERE message_id=? AND principal_id=?`,
			checkAfterSeconds, checkAfterSeconds, checkAfterAt, checkAfterAt, mode, mode, id, actor.ID); err != nil {
			return ack, err
		}
		emitAckEvent = true
	}
	if mode != "" {
		if _, err = upsertPeerModeTx(ctx, tx, project, actor.ID, mode, checkAfterSeconds, opts.TTLSeconds, opts.RTOSeconds, now); err != nil {
			return ack, err
		}
	}
	if emitAckEvent {
		payload := map[string]any{}
		if checkAfterSeconds > 0 {
			payload["check_after_seconds"] = checkAfterSeconds
			payload["check_after_at"] = checkAfterAt
		}
		if mode != "" {
			payload["mode"] = mode
		}
		if err = event(ctx, tx, project, actor.ID, "message.acknowledged", id, payload); err != nil {
			return ack, err
		}
	}
	err = tx.QueryRowContext(ctx, `SELECT acknowledged_at,check_after_seconds,check_after_at,mode FROM message_acknowledgments WHERE message_id=? AND principal_id=?`, id, actor.ID).Scan(&now, &checkAfterSeconds, &checkAfterAt, &mode)
	if err != nil {
		return ack, err
	}
	if err = tx.Commit(); err != nil {
		return ack, err
	}
	return MessageAcknowledgment{
		MessageID:         id,
		PrincipalID:       actor.ID,
		AcknowledgedAt:    now,
		CheckAfterSeconds: checkAfterSeconds,
		CheckAfterAt:      checkAfterAt,
		Mode:              mode,
	}, nil
}

// MessageAcknowledgments is visible only to a message sender or recipient.
func (s *Store) MessageAcknowledgments(ctx context.Context, project, token, id string) ([]MessageAcknowledgment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	actor, err := authorizeProjectRead(ctx, tx, project, token)
	if err != nil {
		return nil, err
	}
	var sender string
	var recipient sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT sender,recipient FROM messages WHERE id=? AND project_id=? AND (recipient IS NOT NULL OR `+visibleMessageSQL("messages")+`)`, id, project, actor.Role, actor.ID, actor.ID, actor.ID, actor.ID).Scan(&sender, &recipient)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if actor.ID != sender && (recipient.Valid && recipient.String != actor.ID) {
		return nil, ErrUnauthorized
	}
	rows, err := tx.QueryContext(ctx, `SELECT message_id,principal_id,acknowledged_at,check_after_seconds,check_after_at FROM message_acknowledgments WHERE message_id=? ORDER BY acknowledged_at,principal_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MessageAcknowledgment{}
	for rows.Next() {
		var a MessageAcknowledgment
		if err = rows.Scan(&a.MessageID, &a.PrincipalID, &a.AcknowledgedAt, &a.CheckAfterSeconds, &a.CheckAfterAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SendMessage commits the message and its event together. An empty recipient
// addresses the project; otherwise only the sender and recipient can read it.
func (s *Store) SendMessage(ctx context.Context, project, token, recipient, thread, subject, body string) (Message, error) {
	return s.SendMessageWithID(ctx, project, token, recipient, thread, subject, body, "")
}

// SendMessageWithID deduplicates retries with the same principal, project,
// request ID and payload. A reused ID with different content is rejected.
func (s *Store) SendMessageWithID(ctx context.Context, project, token, recipient, thread, subject, body, requestID string) (Message, error) {
	return s.SendMessageWithCheckAfter(ctx, project, token, recipient, thread, subject, body, requestID, 0)
}

func (s *Store) SendMessageWithCheckAfter(ctx context.Context, project, token, recipient, thread, subject, body, requestID string, checkAfterSeconds int) (Message, error) {
	return s.SendMessageWithOptions(ctx, project, token, recipient, thread, subject, body, requestID, MessageOptions{CheckAfterSeconds: checkAfterSeconds})
}

func (s *Store) SendMessageWithOptions(ctx context.Context, project, token, recipient, thread, subject, body, requestID string, opts MessageOptions) (Message, error) {
	var m Message
	mode, err := NormalizeAgentMode(opts.Mode)
	if err != nil {
		return m, err
	}
	checkAfterSeconds := opts.CheckAfterSeconds
	if checkAfterSeconds < 0 || checkAfterSeconds > maxCheckAfterSeconds {
		return m, fmt.Errorf("checkAfter must be between 0 and %d seconds", maxCheckAfterSeconds)
	}
	ttlSeconds := opts.TTLSeconds
	if ttlSeconds < 0 || ttlSeconds > maxCheckAfterSeconds {
		return m, fmt.Errorf("ttl must be between 0 and %d seconds", maxCheckAfterSeconds)
	}
	rtoSeconds := opts.RTOSeconds
	if rtoSeconds < 0 || rtoSeconds > maxCheckAfterSeconds {
		return m, fmt.Errorf("rto must be between 0 and %d seconds", maxCheckAfterSeconds)
	}
	if strings.TrimSpace(body) == "" || len(body) > 64*1024 || len(subject) > 512 || len(thread) > 256 || len(requestID) > 128 || !utf8.ValidString(body) || !utf8.ValidString(subject) || !utf8.ValidString(thread) || !utf8.ValidString(recipient) || !utf8.ValidString(requestID) {
		return m, errors.New("invalid message")
	}
	var payload []byte
	if mode == "" && ttlSeconds == 0 && rtoSeconds == 0 {
		if checkAfterSeconds > 0 {
			payload, err = json.Marshal([]any{recipient, thread, subject, body, checkAfterSeconds})
		} else {
			payload, err = json.Marshal([]string{recipient, thread, subject, body})
		}
	} else {
		payload, err = json.Marshal([]any{recipient, thread, subject, body, checkAfterSeconds, mode, ttlSeconds, rtoSeconds})
	}
	if err != nil {
		return m, err
	}
	digest := sha256.Sum256(payload)
	id, err := randomID("msg_")
	if err != nil {
		return m, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return m, err
	}
	defer tx.Rollback()
	actor, err := authorizePermission(ctx, tx, project, token, PermissionMessageSend)
	if err != nil {
		return m, err
	}
	if requestID != "" {
		var previousDigest []byte
		err = tx.QueryRowContext(ctx, `SELECT payload_digest FROM message_requests WHERE project_id=? AND principal_id=? AND request_id=?`, project, actor.ID, requestID).Scan(&previousDigest)
		if err == nil {
			if !bytes.Equal(previousDigest, digest[:]) {
				return m, ErrConflict
			}
			err = tx.QueryRowContext(ctx, `SELECT m.seq,m.id,m.sender,COALESCE(m.recipient,''),m.thread_id,m.subject,m.body,m.created_at,m.check_after_seconds,m.check_after_at,m.mode,m.ttl_seconds,m.expires_at,m.rto_seconds FROM message_requests r JOIN messages m ON m.id=r.message_id WHERE r.project_id=? AND r.principal_id=? AND r.request_id=?`, project, actor.ID, requestID).Scan(&m.Sequence, &m.ID, &m.Sender, &m.Recipient, &m.ThreadID, &m.Subject, &m.Body, &m.CreatedAt, &m.CheckAfterSeconds, &m.CheckAfterAt, &m.Mode, &m.TTLSeconds, &m.ExpiresAt, &m.RTOSeconds)
			if err == nil {
				m.Hash = MessageHash(m)
				m.Expired = m.ExpiresAt > 0 && time.Now().UnixMilli() >= m.ExpiresAt
			}
			return m, err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return m, err
		}
	}
	if thread == "" {
		thread, err = randomID("thread_")
		if err != nil {
			return m, err
		}
	}
	var target any
	if strings.HasPrefix(thread, channelThreadPrefix) {
		if recipient != "" {
			return m, errors.New("group channel messages cannot set recipient")
		}
		channelID := strings.TrimPrefix(thread, channelThreadPrefix)
		exists, allowed, _, chErr := isChannelParticipantTx(ctx, tx, project, channelID, actor.ID, actor.Role)
		if chErr != nil {
			return m, chErr
		}
		if !exists {
			return m, ErrNotFound
		}
		if !allowed {
			return m, ErrUnauthorized
		}
	}
	if recipient != "" {
		var exists int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM principals WHERE id=? AND project_id=? AND revoked=0`, recipient, project).Scan(&exists); err != nil {
			return m, err
		}
		if exists != 1 {
			return m, ErrNotFound
		}
		target = recipient
	}
	now := time.Now().UnixMilli()
	var checkAfterAt int64
	if checkAfterSeconds > 0 {
		checkAfterAt = now + int64(checkAfterSeconds)*1000
	}
	var expiresAt int64
	if ttlSeconds > 0 {
		expiresAt = now + int64(ttlSeconds)*1000
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO messages(id,project_id,sender,recipient,thread_id,subject,body,created_at,check_after_seconds,check_after_at,mode,ttl_seconds,expires_at,rto_seconds) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, project, actor.ID, target, thread, subject, body, now, checkAfterSeconds, checkAfterAt, mode, ttlSeconds, expiresAt, rtoSeconds)
	if err != nil {
		return m, err
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return m, err
	}
	if mode != "" {
		if _, err = upsertPeerModeTx(ctx, tx, project, actor.ID, mode, checkAfterSeconds, ttlSeconds, rtoSeconds, now); err != nil {
			return m, err
		}
	}
	m = Message{
		Sequence:          seq,
		ID:                id,
		Sender:            actor.ID,
		Recipient:         recipient,
		ThreadID:          thread,
		Subject:           subject,
		Body:              body,
		CreatedAt:         now,
		CheckAfterSeconds: checkAfterSeconds,
		CheckAfterAt:      checkAfterAt,
		Mode:              mode,
		TTLSeconds:        ttlSeconds,
		ExpiresAt:         expiresAt,
		RTOSeconds:        rtoSeconds,
	}
	m.Hash = MessageHash(m)
	// Event omits message contents so a project's other principals cannot read a direct message.
	evPayload := map[string]any{"sequence": seq}
	if checkAfterSeconds > 0 {
		evPayload["check_after_seconds"] = checkAfterSeconds
		evPayload["check_after_at"] = checkAfterAt
	}
	if mode != "" {
		evPayload["mode"] = mode
	}
	if ttlSeconds > 0 {
		evPayload["ttl_seconds"] = ttlSeconds
		evPayload["expires_at"] = expiresAt
	}
	if rtoSeconds > 0 {
		evPayload["rto_seconds"] = rtoSeconds
	}
	if err = event(ctx, tx, project, actor.ID, "message.sent", id, evPayload); err != nil {
		return Message{}, err
	}
	if requestID != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO message_requests(project_id,principal_id,request_id,payload_digest,message_id) VALUES(?,?,?,?,?)`, project, actor.ID, requestID, digest[:], id); err != nil {
			return Message{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return Message{}, err
	}
	return m, nil
}

func resolveVisibleMessageSeqByHashTx(ctx context.Context, tx *sql.Tx, targetHash, whereSQL string, baseArgs []any) (int64, error) {
	targetHash = strings.ToLower(strings.TrimSpace(targetHash))
	if len(targetHash) != 64 {
		return 0, errors.New("unknown or invalid last_read_hash; omit it to fetch visible messages and obtain the latest message hash")
	}
	rows, err := tx.QueryContext(ctx, `SELECT seq,id,sender,COALESCE(recipient,''),thread_id,subject,body,created_at FROM messages WHERE `+whereSQL+` ORDER BY seq ASC`, baseArgs...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var m Message
		if err = rows.Scan(&m.Sequence, &m.ID, &m.Sender, &m.Recipient, &m.ThreadID, &m.Subject, &m.Body, &m.CreatedAt); err != nil {
			return 0, err
		}
		if MessageHash(m) == targetHash {
			return m.Sequence, nil
		}
	}
	if err = rows.Err(); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	return 0, errors.New("unknown last_read_hash in visible messages; omit it to fetch history and obtain the latest message hash")
}

func (s *Store) ListMessages(ctx context.Context, project, token string, after int64, limit int) ([]Message, error) {
	return s.listMessages(ctx, project, token, after, "", limit)
}

// ListMessagesSinceHash returns visible project messages starting at the
// message matching lastReadHash (1 previous context anchor) plus all newer
// messages in ascending sequence order. When lastReadHash is empty, all
// visible messages from seq > 0 (up to limit) are returned.
func (s *Store) ListMessagesSinceHash(ctx context.Context, project, token, lastReadHash string, limit int) ([]Message, error) {
	return s.listMessages(ctx, project, token, 0, lastReadHash, limit)
}

func (s *Store) listMessages(ctx context.Context, project, token string, after int64, lastReadHash string, limit int) ([]Message, error) {
	if after < 0 || limit < 1 || limit > 200 {
		return nil, errors.New("invalid cursor or limit")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	actor, err := authorizeProjectRead(ctx, tx, project, token)
	if err != nil {
		return nil, err
	}
	visArgs := []any{project, actor.Role, actor.ID, actor.ID, actor.ID, actor.ID}
	visWhere := `project_id=? AND ` + visibleMessageSQL("messages")
	var seqOp string
	var seqVal int64
	if strings.TrimSpace(lastReadHash) != "" {
		anchorSeq, aErr := resolveVisibleMessageSeqByHashTx(ctx, tx, lastReadHash, visWhere, visArgs)
		if aErr != nil {
			return nil, aErr
		}
		seqOp = `seq>=?`
		seqVal = anchorSeq
	} else {
		seqOp = `seq>?`
		seqVal = after
	}
	queryArgs := append([]any{seqVal}, visArgs[1:]...)
	queryArgs = append(queryArgs, limit)
	rows, err := tx.QueryContext(ctx, `SELECT seq,id,sender,COALESCE(recipient,''),thread_id,subject,body,created_at,check_after_seconds,check_after_at,mode,ttl_seconds,expires_at,rto_seconds FROM messages WHERE project_id=? AND `+seqOp+` AND `+visibleMessageSQL("messages")+` ORDER BY seq LIMIT ?`, append([]any{project}, queryArgs...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nowMs := time.Now().UnixMilli()
	messages := []Message{}
	for rows.Next() {
		var m Message
		if err = rows.Scan(&m.Sequence, &m.ID, &m.Sender, &m.Recipient, &m.ThreadID, &m.Subject, &m.Body, &m.CreatedAt, &m.CheckAfterSeconds, &m.CheckAfterAt, &m.Mode, &m.TTLSeconds, &m.ExpiresAt, &m.RTOSeconds); err != nil {
			return nil, err
		}
		m.Hash = MessageHash(m)
		m.Expired = m.ExpiresAt > 0 && nowMs >= m.ExpiresAt
		messages = append(messages, m)
	}
	if err = rows.Err(); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return messages, nil
}

// RecentMessages returns the latest messages visible to the caller.
func (s *Store) RecentMessages(ctx context.Context, project, token string, limit int) ([]Message, error) {
	if limit < 1 || limit > 200 {
		return nil, errors.New("invalid limit")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	actor, err := authorizeProjectRead(ctx, tx, project, token)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT seq,id,sender,COALESCE(recipient,''),thread_id,subject,body,created_at,check_after_seconds,check_after_at,mode,ttl_seconds,expires_at,rto_seconds FROM messages WHERE project_id=? AND `+visibleMessageSQL("messages")+` ORDER BY seq DESC LIMIT ?`, project, actor.Role, actor.ID, actor.ID, actor.ID, actor.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nowMs := time.Now().UnixMilli()
	out := []Message{}
	for rows.Next() {
		var m Message
		if err = rows.Scan(&m.Sequence, &m.ID, &m.Sender, &m.Recipient, &m.ThreadID, &m.Subject, &m.Body, &m.CreatedAt, &m.CheckAfterSeconds, &m.CheckAfterAt, &m.Mode, &m.TTLSeconds, &m.ExpiresAt, &m.RTOSeconds); err != nil {
			return nil, err
		}
		m.Hash = MessageHash(m)
		m.Expired = m.ExpiresAt > 0 && nowMs >= m.ExpiresAt
		out = append(out, m)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// SearchConversations searches messages visible to the current project identity.
func (s *Store) SearchConversations(ctx context.Context, project, token, query string) ([]Message, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 256 {
		return nil, errors.New("enter a search query of up to 256 bytes")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	actor, err := authorizeProjectRead(ctx, tx, project, token)
	if err != nil {
		return nil, err
	}
	return queryRoomMessagesTx(ctx, tx, actor.ID, "project_id=? AND "+visibleMessageSQL("messages")+
		" AND (instr(lower(body), lower(?))>0 OR instr(lower(subject), lower(?))>0)",
		[]any{project, actor.Role, actor.ID, actor.ID, actor.ID, actor.ID, query, query}, 0, 0, 40, true)
}

// ConversationMessage reads one message within the caller's ordinary conversation scope.
func (s *Store) ConversationMessage(ctx context.Context, project, token, id string) (Message, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, err
	}
	defer tx.Rollback()
	actor, err := authorizeProjectRead(ctx, tx, project, token)
	if err != nil {
		return Message{}, err
	}
	items, err := queryRoomMessagesTx(ctx, tx, actor.ID, "messages.project_id=? AND messages.id=? AND "+visibleMessageSQL("messages"),
		[]any{project, id, actor.Role, actor.ID, actor.ID, actor.ID, actor.ID}, 0, 0, 1, false)
	if err != nil {
		return Message{}, err
	}
	if len(items) == 0 {
		return Message{}, ErrNotFound
	}
	return items[0], nil
}

func queryRoomMessagesTx(ctx context.Context, tx *sql.Tx, principal, whereSQL string, baseArgs []any, after, before int64, limit int, recent bool) ([]Message, error) {
	query := `SELECT seq,id,sender,COALESCE(recipient,''),thread_id,subject,body,created_at,check_after_seconds,check_after_at,mode,ttl_seconds,expires_at,rto_seconds,
		EXISTS(SELECT 1 FROM message_acknowledgments a WHERE a.message_id=messages.id AND a.principal_id=?)
		FROM messages WHERE ` + whereSQL + ` `
	args := append([]any{principal}, baseArgs...)
	if after > 0 {
		query += `AND seq>? `
		args = append(args, after)
	}
	if before > 0 {
		query += `AND seq<? `
		args = append(args, before)
	}
	descending := recent || before > 0
	if descending {
		query += `ORDER BY seq DESC `
	} else {
		query += `ORDER BY seq ASC `
	}
	query += `LIMIT ?`
	args = append(args, limit)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nowMs := time.Now().UnixMilli()
	out := []Message{}
	for rows.Next() {
		var m Message
		if err = rows.Scan(&m.Sequence, &m.ID, &m.Sender, &m.Recipient, &m.ThreadID, &m.Subject, &m.Body, &m.CreatedAt, &m.CheckAfterSeconds, &m.CheckAfterAt, &m.Mode, &m.TTLSeconds, &m.ExpiresAt, &m.RTOSeconds, &m.AcknowledgedByMe); err != nil {
			return nil, err
		}
		m.Hash = MessageHash(m)
		m.Expired = m.ExpiresAt > 0 && nowMs >= m.ExpiresAt
		out = append(out, m)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if descending {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out, nil
}

// ConversationMessages returns # general or one participant's DM history.
// Filtering occurs in SQLite before limit/cursor handling so room pagination
// cannot be displaced by unrelated direct messages or named group channels.
func (s *Store) ConversationMessages(ctx context.Context, project, token, recipient string, after, before int64, limit int, recent bool) ([]Message, error) {
	if after < 0 || before < 0 || (after > 0 && before > 0) || limit < 1 || limit > 200 {
		return nil, errors.New("invalid conversation cursor or limit")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	actor, err := authorizeProjectRead(ctx, tx, project, token)
	if err != nil {
		return nil, err
	}
	if recipient == "" {
		return queryRoomMessagesTx(ctx, tx, actor.ID, `project_id=? AND recipient IS NULL AND thread_id NOT LIKE 'channel:%'`, []any{project}, after, before, limit, recent)
	}
	return queryRoomMessagesTx(ctx, tx, actor.ID, `project_id=? AND ((sender=? AND recipient=?) OR (sender=? AND recipient=?))`, []any{project, actor.ID, recipient, recipient, actor.ID}, after, before, limit, recent)
}
