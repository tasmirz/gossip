package coord

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

type Message struct {
	AcknowledgedByMe bool   `json:"acknowledged_by_me,omitempty"`
	Sequence         int64  `json:"sequence"`
	ID               string `json:"id"`
	Hash             string `json:"hash,omitempty"`
	Sender           string `json:"sender"`
	Recipient        string `json:"recipient,omitempty"`
	ThreadID         string `json:"thread_id"`
	Subject          string `json:"subject"`
	Body             string `json:"body"`
	CreatedAt        int64  `json:"created_at"`
}

// MessageHash returns a stable SHA-256 hex digest identifying an immutable message record.
func MessageHash(m Message) string {
	payload, _ := json.Marshal([]any{m.ID, m.Sequence, m.Sender, m.Recipient, m.ThreadID, m.Subject, m.Body, m.CreatedAt})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

type MessageAcknowledgment struct {
	MessageID      string `json:"message_id"`
	PrincipalID    string `json:"principal_id"`
	AcknowledgedAt int64  `json:"acknowledged_at"`
}

// AcknowledgeMessage explicitly records a recipient's acknowledgment. Merely
// listing a message does not prove that an agent processed it.
func (s *Store) AcknowledgeMessage(ctx context.Context, project, token, id string) (MessageAcknowledgment, error) {
	var ack MessageAcknowledgment
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
	res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO message_acknowledgments(message_id,principal_id,acknowledged_at) VALUES(?,?,?)`, id, actor.ID, now)
	if err != nil {
		return ack, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return ack, err
	}
	if n == 1 {
		if err = event(ctx, tx, project, actor.ID, "message.acknowledged", id, map[string]any{}); err != nil {
			return ack, err
		}
	}
	err = tx.QueryRowContext(ctx, `SELECT acknowledged_at FROM message_acknowledgments WHERE message_id=? AND principal_id=?`, id, actor.ID).Scan(&now)
	if err != nil {
		return ack, err
	}
	if err = tx.Commit(); err != nil {
		return ack, err
	}
	return MessageAcknowledgment{MessageID: id, PrincipalID: actor.ID, AcknowledgedAt: now}, nil
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
	rows, err := tx.QueryContext(ctx, `SELECT message_id,principal_id,acknowledged_at FROM message_acknowledgments WHERE message_id=? ORDER BY acknowledged_at,principal_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MessageAcknowledgment{}
	for rows.Next() {
		var a MessageAcknowledgment
		if err = rows.Scan(&a.MessageID, &a.PrincipalID, &a.AcknowledgedAt); err != nil {
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
	var m Message
	if strings.TrimSpace(body) == "" || len(body) > 64*1024 || len(subject) > 512 || len(thread) > 256 || len(requestID) > 128 || !utf8.ValidString(body) || !utf8.ValidString(subject) || !utf8.ValidString(thread) || !utf8.ValidString(recipient) || !utf8.ValidString(requestID) {
		return m, errors.New("invalid message")
	}
	payload, err := json.Marshal([]string{recipient, thread, subject, body})
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
			err = tx.QueryRowContext(ctx, `SELECT m.seq,m.id,m.sender,COALESCE(m.recipient,''),m.thread_id,m.subject,m.body,m.created_at FROM message_requests r JOIN messages m ON m.id=r.message_id WHERE r.project_id=? AND r.principal_id=? AND r.request_id=?`, project, actor.ID, requestID).Scan(&m.Sequence, &m.ID, &m.Sender, &m.Recipient, &m.ThreadID, &m.Subject, &m.Body, &m.CreatedAt)
			if err == nil {
				m.Hash = MessageHash(m)
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
	res, err := tx.ExecContext(ctx, `INSERT INTO messages(id,project_id,sender,recipient,thread_id,subject,body,created_at) VALUES(?,?,?,?,?,?,?,?)`, id, project, actor.ID, target, thread, subject, body, now)
	if err != nil {
		return m, err
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return m, err
	}
	m = Message{Sequence: seq, ID: id, Sender: actor.ID, Recipient: recipient, ThreadID: thread, Subject: subject, Body: body, CreatedAt: now}
	m.Hash = MessageHash(m)
	// Event omits message contents so a project's other principals cannot read a direct message.
	if err = event(ctx, tx, project, actor.ID, "message.sent", id, map[string]any{"sequence": seq}); err != nil {
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
	rows, err := tx.QueryContext(ctx, `SELECT seq,id,sender,COALESCE(recipient,''),thread_id,subject,body,created_at FROM messages WHERE project_id=? AND `+seqOp+` AND `+visibleMessageSQL("messages")+` ORDER BY seq LIMIT ?`, append([]any{project}, queryArgs...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := []Message{}
	for rows.Next() {
		var m Message
		if err = rows.Scan(&m.Sequence, &m.ID, &m.Sender, &m.Recipient, &m.ThreadID, &m.Subject, &m.Body, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.Hash = MessageHash(m)
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
	rows, err := tx.QueryContext(ctx, `SELECT seq,id,sender,COALESCE(recipient,''),thread_id,subject,body,created_at FROM messages WHERE project_id=? AND `+visibleMessageSQL("messages")+` ORDER BY seq DESC LIMIT ?`, project, actor.Role, actor.ID, actor.ID, actor.ID, actor.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		if err = rows.Scan(&m.Sequence, &m.ID, &m.Sender, &m.Recipient, &m.ThreadID, &m.Subject, &m.Body, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.Hash = MessageHash(m)
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
	query := `SELECT seq,id,sender,COALESCE(recipient,''),thread_id,subject,body,created_at,
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
	out := []Message{}
	for rows.Next() {
		var m Message
		if err = rows.Scan(&m.Sequence, &m.ID, &m.Sender, &m.Recipient, &m.ThreadID, &m.Subject, &m.Body, &m.CreatedAt, &m.AcknowledgedByMe); err != nil {
			return nil, err
		}
		m.Hash = MessageHash(m)
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
