package coord

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"strconv"
	"time"
)

type inboxBackend interface {
	ListPermissions(context.Context, string, string) ([]PermissionGrant, error)
	WhoAmI(context.Context, string, string) (Principal, error)
	SyncInbox(context.Context, string, string, int64, int64, int) (ProjectSyncFrame, error)
}

// RunInboxWorker polls saved identities independently of agent sessions.
func (r *DeviceRegistry) RunInboxWorker(ctx context.Context, local inboxBackend) {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	retry := map[DeviceConnection]time.Time{}
	for {
		connections, err := r.List(ctx)
		if err == nil {
			for _, c := range connections {
				if ctx.Err() != nil {
					return
				}
				if time.Now().Before(retry[c]) {
					continue
				}
				poll, cancel := context.WithTimeout(ctx, 30*time.Second)
				err := r.pollInbox(poll, local, c)
				cancel()
				if err != nil {
					retry[c] = time.Now().Add(3 * time.Minute)
				} else {
					delete(retry, c)
					wakeErr := r.deliverInboxWake(ctx, c)
					failure := ""
					if wakeErr != nil {
						failure = wakeErr.Error()
					}
					if _, err := r.db.ExecContext(ctx, `UPDATE service_inbox SET payload=? WHERE profile=? AND home=? AND project=? AND principal=? AND item=''`, failure, c.Profile, c.HomeID, c.ProjectID, c.PrincipalID); err != nil {
						log.Printf("Gossip inbox status: %v", err)
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *DeviceRegistry) pollInbox(ctx context.Context, backend inboxBackend, c DeviceConnection) error {
	token, err := r.Token(c)
	if err != nil {
		return err
	}
	if c.Endpoint != "" {
		remote, err := NewRemote(c.Endpoint, c.CAFile, c.CertFile, c.KeyFile, "")
		if err != nil {
			return err
		}
		defer remote.CloseIdleConnections()
		backend = remote
	}
	key := []any{c.Profile, c.HomeID, c.ProjectID, c.PrincipalID}
	actor, err := backend.WhoAmI(ctx, c.ProjectID, token)
	if errors.Is(err, ErrUnauthorized) {
		_, clearErr := r.db.ExecContext(ctx, `DELETE FROM service_inbox WHERE profile=? AND home=? AND project=? AND principal=?`, key...)
		return errors.Join(err, clearErr)
	}
	if err != nil {
		return err
	}
	if actor.ID != c.PrincipalID {
		return ErrUnauthorized
	}
	var events, messages int64
	err = r.db.QueryRowContext(ctx, `SELECT events,messages FROM service_inbox WHERE profile=? AND home=? AND project=? AND principal=? AND item=''`, key...).Scan(&events, &messages)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	for {
		frame, err := backend.SyncInbox(ctx, c.ProjectID, token, events, messages, 100)
		if err != nil {
			return err
		}
		if frame.HomeInstanceID != c.HomeID {
			return ErrUnauthorized
		}
		canReview := actor.Role == "owner"
		for _, event := range frame.Events {
			if canReview || event.Kind != "review.requested" || event.Actor == actor.ID {
				continue
			}
			grants, err := backend.ListPermissions(ctx, c.ProjectID, token)
			if err != nil {
				return err
			}
			for _, grant := range grants {
				if grant.PrincipalID == actor.ID && grant.Permission == PermissionReviewDecide {
					canReview = true
					break
				}
			}
			break
		}
		if err := r.saveInboxPage(ctx, c, frame, canReview); err != nil {
			return err
		}
		if !frame.EventsMayContinue && !frame.InboxHasMore {
			return nil
		}
		if events == frame.NextEventCursor && messages == frame.NextInboxCursor {
			return errors.New("inbox pagination did not advance")
		}
		events, messages = frame.NextEventCursor, frame.NextInboxCursor
	}
}

func (r *DeviceRegistry) saveInboxPage(ctx context.Context, c DeviceConnection, f ProjectSyncFrame, canReview bool) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM device_connections WHERE profile=? AND home_id=? AND project_id=? AND principal_id=? AND secret_id=?)`, c.Profile, c.HomeID, c.ProjectID, c.PrincipalID, c.SecretID).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return ErrUnauthorized
	}
	insert := func(id string, value any) error {
		payload, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO service_inbox(profile,home,project,principal,item,payload) VALUES(?,?,?,?,?,?)`, c.Profile, c.HomeID, c.ProjectID, c.PrincipalID, id, payload)
		return err
	}
	for _, msg := range f.InboxMessages {
		if msg.Sender != c.PrincipalID {
			if err := insert(msg.ID, msg); err != nil {
				return err
			}
		}
	}
	for _, event := range f.Events {
		if event.Actor == c.PrincipalID {
			continue
		}
		switch event.Kind {
		case "plan.published":
		case "review.requested":
			if !canReview {
				continue
			}
		case "task.assigned":
			var assignment struct {
				Assignee string `json:"assignee"`
			}
			if json.Unmarshal(event.Payload, &assignment) != nil || assignment.Assignee != c.PrincipalID {
				continue
			}
		default:
			continue
		}
		id := "event:" + strconv.FormatInt(event.Sequence, 10)
		if err := insert(id, struct {
			ID string `json:"id"`
			Event
		}{id, event}); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO service_inbox(profile,home,project,principal,item,events,messages,polled_at) VALUES(?,?,?,?,'',?,?,?) ON CONFLICT(profile,home,project,principal,item) DO UPDATE SET events=excluded.events,messages=excluded.messages,polled_at=excluded.polled_at`, c.Profile, c.HomeID, c.ProjectID, c.PrincipalID, f.NextEventCursor, f.NextInboxCursor, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ServiceInbox returns previews without changing the coordinator's read state.
// Dismissal keeps the item key so cursor resets cannot requeue handled items.
func (r *DeviceRegistry) ServiceInbox(ctx context.Context, c DeviceConnection, dismiss string) (any, error) {
	key := []any{c.Profile, c.HomeID, c.ProjectID, c.PrincipalID}
	if dismiss != "" {
		result, err := r.db.ExecContext(ctx, `UPDATE service_inbox SET payload=NULL WHERE profile=? AND home=? AND project=? AND principal=? AND item=?`, append(key, dismiss)...)
		if err != nil {
			return nil, err
		}
		n, err := result.RowsAffected()
		return map[string]bool{"dismissed": n > 0}, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT item,payload,polled_at FROM service_inbox WHERE profile=? AND home=? AND project=? AND principal=? AND (item='' OR payload IS NOT NULL) ORDER BY item LIMIT 101`, key...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []json.RawMessage{}
	var polled int64
	var wakeFailure string
	for rows.Next() {
		var id string
		var payload []byte
		var at int64
		if err := rows.Scan(&id, &payload, &at); err != nil {
			return nil, err
		}
		if id == "" {
			polled = at
			wakeFailure = string(payload)
			continue
		}
		items = append(items, json.RawMessage(payload))
	}
	return map[string]any{"items": items, "last_successful_poll": polled, "wake_failure": wakeFailure}, rows.Err()
}

// InboxStatus contains local polling metadata without message previews.
type InboxStatus struct {
	LastSuccessfulPoll int64  `json:"last_successful_poll"`
	QueueCount         int    `json:"queue_count"`
	WakeFailure        string `json:"wake_failure,omitempty"`
}

func (r *DeviceRegistry) InboxStatus(ctx context.Context, c DeviceConnection) (InboxStatus, error) {
	var status InboxStatus
	err := r.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(CASE WHEN item='' THEN polled_at END),0), COUNT(CASE WHEN item<>'' AND payload IS NOT NULL THEN 1 END),COALESCE(MAX(CASE WHEN item='' THEN CAST(payload AS TEXT) END),'') FROM service_inbox WHERE profile=? AND home=? AND project=? AND principal=?`, c.Profile, c.HomeID, c.ProjectID, c.PrincipalID).Scan(&status.LastSuccessfulPoll, &status.QueueCount, &status.WakeFailure)
	return status, err
}
