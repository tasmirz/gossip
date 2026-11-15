package coord

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const channelThreadPrefix = "channel:"

var channelSlugInvalid = regexp.MustCompile(`[^a-z0-9-]+`)

// ChannelMember describes a project member's status inside a group chat.
type ChannelMember struct {
	PrincipalID string `json:"principal_id"`
	Name        string `json:"name,omitempty"`
	Role        string `json:"role,omitempty"`
	Status      string `json:"status"`
	InvitedBy   string `json:"invited_by,omitempty"`
	UpdatedAt   int64  `json:"updated_at"`
}

// ProjectChannel represents a named project group chat room.
type ProjectChannel struct {
	ID           string          `json:"id"`
	ProjectID    string          `json:"project_id"`
	Name         string          `json:"name"`
	Topic        string          `json:"topic,omitempty"`
	CreatedBy    string          `json:"created_by"`
	CreatedAt    int64           `json:"created_at"`
	MyStatus     string          `json:"my_status"`
	MemberCount  int             `json:"member_count"`
	PendingCount int             `json:"pending_count"`
	Members      []ChannelMember `json:"members,omitempty"`
}

// ChannelBackend defines group chat lifecycle and message queries decoupled
// from the core Backend interface so existing mock backends keep compiling.
type ChannelBackend interface {
	ListChannels(ctx context.Context, project, token string) ([]ProjectChannel, error)
	CreateChannel(ctx context.Context, project, token, name, topic string, memberIDs []string, autoSuffix, announce bool) (ProjectChannel, error)
	RequestJoinChannel(ctx context.Context, project, token, channelID, note string, announce bool) (ProjectChannel, error)
	InviteToChannel(ctx context.Context, project, token, channelID, targetPrincipalID, note string, announce bool) (ProjectChannel, error)
	RespondChannelMembership(ctx context.Context, project, token, channelID, targetPrincipalID, action string) (ProjectChannel, error)
	ChannelMessages(ctx context.Context, project, token, channelID string, after, before int64, limit int, recent bool) ([]Message, error)
}

func channelThreadID(channelID string) string {
	return channelThreadPrefix + strings.TrimSpace(channelID)
}

func visibleMessageSQL(alias string) string {
	return fmt.Sprintf(`(
  (%[1]s.recipient IS NULL AND (
    %[1]s.thread_id NOT LIKE 'channel:%%' OR ?='owner' OR EXISTS(
      SELECT 1 FROM project_channels pc WHERE pc.project_id=%[1]s.project_id AND ('channel:' || pc.id)=%[1]s.thread_id AND pc.created_by=?
    ) OR EXISTS(
      SELECT 1 FROM channel_members cm WHERE cm.project_id=%[1]s.project_id AND ('channel:' || cm.channel_id)=%[1]s.thread_id AND cm.principal_id=? AND cm.status='joined'
    )
  ))
  OR %[1]s.recipient=? OR %[1]s.sender=?
)`, alias)
}

func normalizeChannelSlug(raw string, addSuffix bool) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.TrimPrefix(s, "#")
	s = channelSlugInvalid.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) < 2 || len(s) > 48 || s == "general" {
		return "", errors.New("channel name must be 2-48 slug characters and not 'general'")
	}
	if !addSuffix {
		return s, nil
	}
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	suffix := hex.EncodeToString(b[:])
	if len(s) > 43 {
		s = strings.TrimRight(s[:43], "-")
	}
	return s + "-" + suffix, nil
}

func postGeneralNoticeTx(ctx context.Context, tx *sql.Tx, project, actorID, subject, body string) error {
	id, err := randomID("msg_")
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	res, err := tx.ExecContext(ctx, `INSERT INTO messages(id,project_id,sender,recipient,thread_id,subject,body,created_at) VALUES(?,?,?,NULL,'general',?,?,?)`, id, project, actorID, subject, body, now)
	if err != nil {
		return err
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return err
	}
	return event(ctx, tx, project, actorID, "message.sent", id, map[string]any{"sequence": seq})
}

func isChannelParticipantTx(ctx context.Context, tx *sql.Tx, project, channelID, actorID, actorRole string) (exists bool, allowed bool, createdBy string, err error) {
	err = tx.QueryRowContext(ctx, `SELECT created_by FROM project_channels WHERE project_id=? AND id=?`, project, channelID).Scan(&createdBy)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, "", nil
	}
	if err != nil {
		return false, false, "", err
	}
	if actorRole == "owner" || createdBy == actorID {
		return true, true, createdBy, nil
	}
	var joined int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM channel_members WHERE project_id=? AND channel_id=? AND principal_id=? AND status='joined'`, project, channelID, actorID).Scan(&joined); err != nil {
		return true, false, createdBy, err
	}
	return true, joined > 0, createdBy, nil
}

func loadProjectChannelTx(ctx context.Context, tx *sql.Tx, project, channelID, actorID, actorRole string) (ProjectChannel, error) {
	var ch ProjectChannel
	err := tx.QueryRowContext(ctx, `SELECT id,project_id,name,topic,created_by,created_at FROM project_channels WHERE project_id=? AND id=?`, project, channelID).
		Scan(&ch.ID, &ch.ProjectID, &ch.Name, &ch.Topic, &ch.CreatedBy, &ch.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ch, ErrNotFound
	}
	if err != nil {
		return ch, err
	}
	ch.MyStatus = "none"
	rows, err := tx.QueryContext(ctx, `
		SELECT cm.principal_id, p.name, p.role, cm.status, cm.invited_by, cm.updated_at
		FROM channel_members cm
		JOIN principals p ON p.id=cm.principal_id AND p.project_id=cm.project_id
		WHERE cm.project_id=? AND cm.channel_id=? AND p.revoked=0
		ORDER BY cm.updated_at ASC, p.name ASC`, project, channelID)
	if err != nil {
		return ch, err
	}
	defer rows.Close()
	members := []ChannelMember{}
	for rows.Next() {
		var m ChannelMember
		if err = rows.Scan(&m.PrincipalID, &m.Name, &m.Role, &m.Status, &m.InvitedBy, &m.UpdatedAt); err != nil {
			return ch, err
		}
		if m.PrincipalID == actorID {
			ch.MyStatus = m.Status
		}
		if m.Status == "joined" {
			ch.MemberCount++
		} else if m.Status == "requested" || m.Status == "invited" {
			ch.PendingCount++
		}
		members = append(members, m)
	}
	if err = rows.Err(); err != nil {
		return ch, err
	}
	if actorRole == "owner" || ch.CreatedBy == actorID || ch.MyStatus == "joined" {
		ch.Members = members
	}
	return ch, nil
}

// ListChannels lists all project group chats so members can participate or ask to join.
func (s *Store) ListChannels(ctx context.Context, project, token string) ([]ProjectChannel, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	actor, err := authorizeProjectRead(ctx, tx, project, token)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM project_channels WHERE project_id=? ORDER BY created_at DESC, name ASC`, project)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	out := make([]ProjectChannel, 0, len(ids))
	for _, id := range ids {
		ch, loadErr := loadProjectChannelTx(ctx, tx, project, id, actor.ID, actor.Role)
		if loadErr != nil {
			return nil, loadErr
		}
		out = append(out, ch)
	}
	return out, nil
}

// CreateChannel lets any authorized member or owner create a named group chat.
func (s *Store) CreateChannel(ctx context.Context, project, token, name, topic string, memberIDs []string, autoSuffix, announce bool) (ProjectChannel, error) {
	if len(topic) > 256 || !utf8.ValidString(name) || !utf8.ValidString(topic) {
		return ProjectChannel{}, errors.New("invalid channel details")
	}
	slug, err := normalizeChannelSlug(name, autoSuffix)
	if err != nil {
		return ProjectChannel{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProjectChannel{}, err
	}
	defer tx.Rollback()
	actor, err := authorizePermission(ctx, tx, project, token, PermissionMessageSend)
	if err != nil {
		return ProjectChannel{}, err
	}
	var existing int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM project_channels WHERE project_id=? AND name=?`, project, slug).Scan(&existing); err != nil {
		return ProjectChannel{}, err
	}
	if existing > 0 {
		slug, err = normalizeChannelSlug(name, true)
		if err != nil {
			return ProjectChannel{}, err
		}
	}
	channelID, err := randomID("ch_")
	if err != nil {
		return ProjectChannel{}, err
	}
	now := time.Now().UnixMilli()
	if _, err = tx.ExecContext(ctx, `INSERT INTO project_channels(project_id,id,name,topic,created_by,created_at) VALUES(?,?,?,?,?,?)`, project, channelID, slug, strings.TrimSpace(topic), actor.ID, now); err != nil {
		return ProjectChannel{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO channel_members(project_id,channel_id,principal_id,status,invited_by,updated_at) VALUES(?,?,?,'joined',?,?)`, project, channelID, actor.ID, actor.ID, now); err != nil {
		return ProjectChannel{}, err
	}
	for _, mid := range memberIDs {
		mid = strings.TrimSpace(mid)
		if mid == "" || mid == actor.ID {
			continue
		}
		var valid int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM principals WHERE id=? AND project_id=? AND revoked=0`, mid, project).Scan(&valid); err != nil {
			return ProjectChannel{}, err
		}
		if valid != 1 {
			return ProjectChannel{}, ErrNotFound
		}
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO channel_members(project_id,channel_id,principal_id,status,invited_by,updated_at) VALUES(?,?,?,'joined',?,?)`, project, channelID, mid, actor.ID, now); err != nil {
			return ProjectChannel{}, err
		}
	}
	if err = event(ctx, tx, project, actor.ID, "channel.created", channelID, map[string]string{"name": slug}); err != nil {
		return ProjectChannel{}, err
	}
	if announce {
		msgBody := fmt.Sprintf("Created group chat #%s (%s).", slug, channelID)
		if strings.TrimSpace(topic) != "" {
			msgBody += " Topic: " + strings.TrimSpace(topic)
		}
		if err = postGeneralNoticeTx(ctx, tx, project, actor.ID, "channel:"+slug, msgBody); err != nil {
			return ProjectChannel{}, err
		}
	}
	ch, err := loadProjectChannelTx(ctx, tx, project, channelID, actor.ID, actor.Role)
	if err != nil {
		return ProjectChannel{}, err
	}
	if err = tx.Commit(); err != nil {
		return ProjectChannel{}, err
	}
	return ch, nil
}

// RequestJoinChannel records a principal's request to join an existing group chat.
func (s *Store) RequestJoinChannel(ctx context.Context, project, token, channelID, note string, announce bool) (ProjectChannel, error) {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" || len(note) > 256 || !utf8.ValidString(note) {
		return ProjectChannel{}, errors.New("invalid join request")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProjectChannel{}, err
	}
	defer tx.Rollback()
	actor, err := authorizePermission(ctx, tx, project, token, PermissionMessageSend)
	if err != nil {
		return ProjectChannel{}, err
	}
	var name string
	err = tx.QueryRowContext(ctx, `SELECT name FROM project_channels WHERE project_id=? AND id=?`, project, channelID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectChannel{}, ErrNotFound
	}
	if err != nil {
		return ProjectChannel{}, err
	}
	var currentStatus string
	err = tx.QueryRowContext(ctx, `SELECT status FROM channel_members WHERE project_id=? AND channel_id=? AND principal_id=?`, project, channelID, actor.ID).Scan(&currentStatus)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ProjectChannel{}, err
	}
	now := time.Now().UnixMilli()
	nextStatus := "requested"
	if currentStatus == "joined" {
		ch, lErr := loadProjectChannelTx(ctx, tx, project, channelID, actor.ID, actor.Role)
		return ch, lErr
	}
	if currentStatus == "invited" || actor.Role == "owner" {
		nextStatus = "joined"
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO channel_members(project_id,channel_id,principal_id,status,invited_by,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(project_id,channel_id,principal_id) DO UPDATE SET status=excluded.status, updated_at=excluded.updated_at`, project, channelID, actor.ID, nextStatus, actor.ID, now); err != nil {
		return ProjectChannel{}, err
	}
	if err = event(ctx, tx, project, actor.ID, "channel.join_"+nextStatus, channelID, map[string]string{"name": name}); err != nil {
		return ProjectChannel{}, err
	}
	if announce && nextStatus == "requested" {
		body := fmt.Sprintf("Asking to join #%s (%s).", name, channelID)
		if strings.TrimSpace(note) != "" {
			body += " Note: " + strings.TrimSpace(note)
		}
		if err = postGeneralNoticeTx(ctx, tx, project, actor.ID, "channel:"+name, body); err != nil {
			return ProjectChannel{}, err
		}
	}
	ch, err := loadProjectChannelTx(ctx, tx, project, channelID, actor.ID, actor.Role)
	if err != nil {
		return ProjectChannel{}, err
	}
	if err = tx.Commit(); err != nil {
		return ProjectChannel{}, err
	}
	return ch, nil
}

// InviteToChannel lets a project owner or channel creator invite a member,
// optionally posting the invitation into # general so agents can see and join.
func (s *Store) InviteToChannel(ctx context.Context, project, token, channelID, targetPrincipalID, note string, announce bool) (ProjectChannel, error) {
	channelID = strings.TrimSpace(channelID)
	targetPrincipalID = strings.TrimSpace(targetPrincipalID)
	if channelID == "" || targetPrincipalID == "" || len(note) > 256 || !utf8.ValidString(note) {
		return ProjectChannel{}, errors.New("invalid channel invitation")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProjectChannel{}, err
	}
	defer tx.Rollback()
	actor, err := authorizePermission(ctx, tx, project, token, PermissionMessageSend)
	if err != nil {
		return ProjectChannel{}, err
	}
	var name, createdBy string
	err = tx.QueryRowContext(ctx, `SELECT name, created_by FROM project_channels WHERE project_id=? AND id=?`, project, channelID).Scan(&name, &createdBy)
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectChannel{}, ErrNotFound
	}
	if err != nil {
		return ProjectChannel{}, err
	}
	if actor.Role != "owner" && createdBy != actor.ID {
		return ProjectChannel{}, ErrUnauthorized
	}
	var targetName string
	err = tx.QueryRowContext(ctx, `SELECT name FROM principals WHERE id=? AND project_id=? AND revoked=0`, targetPrincipalID, project).Scan(&targetName)
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectChannel{}, ErrNotFound
	}
	if err != nil {
		return ProjectChannel{}, err
	}
	var currentStatus string
	err = tx.QueryRowContext(ctx, `SELECT status FROM channel_members WHERE project_id=? AND channel_id=? AND principal_id=?`, project, channelID, targetPrincipalID).Scan(&currentStatus)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ProjectChannel{}, err
	}
	if currentStatus == "joined" {
		ch, lErr := loadProjectChannelTx(ctx, tx, project, channelID, actor.ID, actor.Role)
		return ch, lErr
	}
	now := time.Now().UnixMilli()
	nextStatus := "invited"
	if currentStatus == "requested" {
		nextStatus = "joined"
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO channel_members(project_id,channel_id,principal_id,status,invited_by,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(project_id,channel_id,principal_id) DO UPDATE SET status=excluded.status, invited_by=excluded.invited_by, updated_at=excluded.updated_at`, project, channelID, targetPrincipalID, nextStatus, actor.ID, now); err != nil {
		return ProjectChannel{}, err
	}
	if err = event(ctx, tx, project, actor.ID, "channel.member_"+nextStatus, channelID, map[string]string{"principal_id": targetPrincipalID, "name": name}); err != nil {
		return ProjectChannel{}, err
	}
	if announce {
		body := fmt.Sprintf("Invited %s (%s) to join #%s (%s).", targetName, targetPrincipalID, name, channelID)
		if nextStatus == "joined" {
			body = fmt.Sprintf("Approved %s (%s) to join #%s (%s).", targetName, targetPrincipalID, name, channelID)
		}
		if strings.TrimSpace(note) != "" {
			body += " " + strings.TrimSpace(note)
		}
		if err = postGeneralNoticeTx(ctx, tx, project, actor.ID, "channel:"+name, body); err != nil {
			return ProjectChannel{}, err
		}
	}
	ch, err := loadProjectChannelTx(ctx, tx, project, channelID, actor.ID, actor.Role)
	if err != nil {
		return ProjectChannel{}, err
	}
	if err = tx.Commit(); err != nil {
		return ProjectChannel{}, err
	}
	return ch, nil
}

// RespondChannelMembership updates channel membership:
//   - "join": accepts an invitation (or owner joins directly)
//   - "approve": owner or channel creator promotes a requested/invited member to joined
//   - "decline" / "leave" / "remove": removes pending/joined membership
func (s *Store) RespondChannelMembership(ctx context.Context, project, token, channelID, targetPrincipalID, action string) (ProjectChannel, error) {
	channelID = strings.TrimSpace(channelID)
	targetPrincipalID = strings.TrimSpace(targetPrincipalID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProjectChannel{}, err
	}
	defer tx.Rollback()
	actor, err := authorizeProjectRead(ctx, tx, project, token)
	if err != nil {
		return ProjectChannel{}, err
	}
	if targetPrincipalID == "" && (action == "join" || action == "leave" || action == "decline") {
		targetPrincipalID = actor.ID
	}
	if targetPrincipalID == "" {
		return ProjectChannel{}, errors.New("principal_id is required")
	}
	var name, createdBy string
	err = tx.QueryRowContext(ctx, `SELECT name, created_by FROM project_channels WHERE project_id=? AND id=?`, project, channelID).Scan(&name, &createdBy)
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectChannel{}, ErrNotFound
	}
	if err != nil {
		return ProjectChannel{}, err
	}
	now := time.Now().UnixMilli()
	switch action {
	case "join":
		var currentStatus string
		err = tx.QueryRowContext(ctx, `SELECT status FROM channel_members WHERE project_id=? AND channel_id=? AND principal_id=?`, project, channelID, actor.ID).Scan(&currentStatus)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return ProjectChannel{}, err
		}
		if currentStatus == "joined" {
			break
		}
		if actor.Role != "owner" && currentStatus != "invited" {
			return ProjectChannel{}, ErrUnauthorized
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO channel_members(project_id,channel_id,principal_id,status,invited_by,updated_at) VALUES(?,?,?,'joined',?,?) ON CONFLICT(project_id,channel_id,principal_id) DO UPDATE SET status='joined', updated_at=excluded.updated_at`, project, channelID, actor.ID, actor.ID, now); err != nil {
			return ProjectChannel{}, err
		}
	case "approve":
		if actor.Role != "owner" && createdBy != actor.ID {
			return ProjectChannel{}, ErrUnauthorized
		}
		var valid int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM principals WHERE id=? AND project_id=? AND revoked=0`, targetPrincipalID, project).Scan(&valid); err != nil {
			return ProjectChannel{}, err
		}
		if valid != 1 {
			return ProjectChannel{}, ErrNotFound
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO channel_members(project_id,channel_id,principal_id,status,invited_by,updated_at) VALUES(?,?,?,'joined',?,?) ON CONFLICT(project_id,channel_id,principal_id) DO UPDATE SET status='joined', updated_at=excluded.updated_at`, project, channelID, targetPrincipalID, actor.ID, now); err != nil {
			return ProjectChannel{}, err
		}
	case "decline", "leave", "remove":
		if targetPrincipalID != actor.ID && actor.Role != "owner" && createdBy != actor.ID {
			return ProjectChannel{}, ErrUnauthorized
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM channel_members WHERE project_id=? AND channel_id=? AND principal_id=?`, project, channelID, targetPrincipalID); err != nil {
			return ProjectChannel{}, err
		}
	default:
		return ProjectChannel{}, errors.New("unknown channel membership action")
	}
	if err = event(ctx, tx, project, actor.ID, "channel.membership_"+action, channelID, map[string]string{"principal_id": targetPrincipalID, "name": name}); err != nil {
		return ProjectChannel{}, err
	}
	ch, err := loadProjectChannelTx(ctx, tx, project, channelID, actor.ID, actor.Role)
	if err != nil {
		return ProjectChannel{}, err
	}
	if err = tx.Commit(); err != nil {
		return ProjectChannel{}, err
	}
	return ch, nil
}

// ChannelMessages lists messages inside one named group chat. Only joined
// members, the channel creator, or a project owner can read its history.
func (s *Store) ChannelMessages(ctx context.Context, project, token, channelID string, after, before int64, limit int, recent bool) ([]Message, error) {
	if after < 0 || before < 0 || (after > 0 && before > 0) || limit < 1 || limit > 200 {
		return nil, errors.New("invalid channel cursor or limit")
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
	exists, allowed, _, err := isChannelParticipantTx(ctx, tx, project, channelID, actor.ID, actor.Role)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}
	if !allowed {
		return nil, ErrUnauthorized
	}
	return queryRoomMessagesTx(ctx, tx, actor.ID, `project_id=? AND recipient IS NULL AND thread_id=?`, []any{project, channelThreadID(channelID)}, after, before, limit, recent)
}
