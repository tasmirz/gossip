package coord

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	maxSyncTaskBytes  = 3 * 1024 * 1024
	maxSyncEventBytes = 2 * 1024 * 1024
	maxSyncFrameBytes = 7 * 1024 * 1024
)

// ProjectSyncFrame is a bounded, authorization-filtered view read from one
// SQLite transaction. SnapshotSequence is the home event high-water mark at
// that transaction's snapshot. NextEventCursor advances only over events the
// caller is allowed to see.
type ProjectSyncFrame struct {
	Plan                 *Plan          `json:"plan,omitempty"`
	Tasks                TaskPage       `json:"tasks"`
	Counts               map[string]int `json:"counts"`
	Events               []Event        `json:"events"`
	EventsAfter          int64          `json:"events_after"`
	NextEventCursor      int64          `json:"next_event_cursor"`
	SnapshotSequence     int64          `json:"snapshot_sequence"`
	HistoryPrunedThrough int64          `json:"history_pruned_through"`
	HomeInstanceID       string         `json:"home_instance_id"`
	EventsMayContinue    bool           `json:"events_may_continue"`
	ResetRequired        bool           `json:"reset_required"`
}

// ProjectReplica records a remote MCP host that has synchronized this project.
// It is a receipt, not an access grant or an authority delegation.
type ProjectReplica struct {
	ProjectID              string `json:"project_id"`
	ReplicaID              string `json:"replica_id"`
	PrincipalID            string `json:"principal_id"`
	CertificateFingerprint string `json:"certificate_fingerprint"`
	FirstSeenAt            int64  `json:"first_seen_at"`
	LastSeenAt             int64  `json:"last_seen_at"`
	LastEventCursor        int64  `json:"last_event_cursor"`
	SnapshotSequence       int64  `json:"snapshot_sequence"`
}

// SyncProject returns a consistent first-page snapshot and a bounded event
// delta. It is a read protocol for reconnecting caches; it does not grant a
// replica authority to accept writes or complete attempts.
func (s *Store) SyncProject(ctx context.Context, project, token string, after int64, limit int) (ProjectSyncFrame, error) {
	return s.syncProject(ctx, project, token, "", "", after, limit)
}

// SyncProjectFromReplica records a receipt in the same transaction as the
// authorized snapshot. The replica identity is bound to one project member
// and one currently granted client certificate.
func (s *Store) SyncProjectFromReplica(ctx context.Context, project, token, replicaID, fingerprint string, after int64, limit int) (ProjectSyncFrame, error) {
	if !strings.HasPrefix(replicaID, "replica_") || len(replicaID) != len("replica_")+32 {
		return ProjectSyncFrame{}, errors.New("invalid replica identity")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(replicaID, "replica_")); err != nil {
		return ProjectSyncFrame{}, errors.New("invalid replica identity")
	}
	fingerprint, err := NormalizeCertificateFingerprint(fingerprint)
	if err != nil {
		return ProjectSyncFrame{}, err
	}
	return s.syncProject(ctx, project, token, replicaID, fingerprint, after, limit)
}

func (s *Store) syncProject(ctx context.Context, project, token, replicaID, fingerprint string, after int64, limit int) (ProjectSyncFrame, error) {
	frame := ProjectSyncFrame{Tasks: TaskPage{Tasks: []Task{}}, Counts: map[string]int{}, Events: []Event{}, EventsAfter: after, HomeInstanceID: s.instanceID}
	if after < 0 || limit < 1 || limit > 200 {
		return frame, errors.New("invalid sync cursor or limit")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return frame, err
	}
	defer tx.Rollback()
	actor, err := authorizeProjectRead(ctx, tx, project, token)
	if err != nil {
		return frame, err
	}
	if replicaID != "" {
		var allowed int
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM certificate_grants g LEFT JOIN certificate_grant_expirations e ON e.project_id=g.project_id AND e.fingerprint=g.fingerprint WHERE g.project_id=? AND g.fingerprint=? AND (e.expires_at IS NULL OR e.expires_at>?))`, project, fingerprint, time.Now().UnixMilli()).Scan(&allowed)
		if err != nil {
			return frame, err
		}
		if allowed != 1 {
			return frame, ErrUnauthorized
		}
		var boundPrincipal, boundFingerprint string
		err = tx.QueryRowContext(ctx, `SELECT principal_id,certificate_fingerprint FROM project_replicas WHERE project_id=? AND replica_id=?`, project, replicaID).Scan(&boundPrincipal, &boundFingerprint)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return frame, err
		}
		if err == nil && (boundPrincipal != actor.ID || boundFingerprint != fingerprint) {
			return frame, ErrConflict
		}
	}

	var plan Plan
	err = tx.QueryRowContext(ctx, `SELECT p.revision,p.body,p.author,p.created_at,EXISTS(SELECT 1 FROM plan_acknowledgments a WHERE a.project_id=p.project_id AND a.principal_id=? AND a.revision=p.revision) FROM plans p JOIN projects j ON j.id=p.project_id AND j.plan_revision=p.revision WHERE p.project_id=?`, actor.ID, project).Scan(&plan.Revision, &plan.Body, &plan.Author, &plan.CreatedAt, &plan.Acknowledged)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return frame, err
	}
	if err == nil {
		frame.Plan = &plan
	}
	baseBytes := 1024
	if frame.Plan != nil {
		encodedPlan, marshalErr := json.Marshal(frame.Plan)
		if marshalErr != nil {
			return frame, marshalErr
		}
		baseBytes += len(encodedPlan)
	}
	if baseBytes >= maxSyncFrameBytes {
		return frame, errors.New("plan exceeds sync transfer size limit")
	}

	rows, err := tx.QueryContext(ctx, `SELECT status,COUNT(*) FROM tasks WHERE project_id=? GROUP BY status`, project)
	if err != nil {
		return frame, err
	}
	for rows.Next() {
		var status string
		var count int
		if err = rows.Scan(&status, &count); err != nil {
			rows.Close()
			return frame, err
		}
		frame.Counts[status] = count
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return frame, err
	}
	if err = rows.Close(); err != nil {
		return frame, err
	}

	taskRows, err := tx.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE project_id=? ORDER BY rowid DESC LIMIT 201`, project)
	if err != nil {
		return frame, err
	}
	taskBytes := 0
	for taskRows.Next() {
		task, scanErr := scanTask(taskRows)
		if scanErr != nil {
			taskRows.Close()
			return frame, scanErr
		}
		if len(frame.Tasks.Tasks) == 200 {
			frame.Tasks.NextCursor = frame.Tasks.Tasks[len(frame.Tasks.Tasks)-1].ID
			break
		}
		encodedTask, marshalErr := json.Marshal(task)
		if marshalErr != nil {
			taskRows.Close()
			return frame, marshalErr
		}
		if taskBytes+len(encodedTask) > maxSyncTaskBytes {
			if len(frame.Tasks.Tasks) == 0 {
				taskRows.Close()
				return frame, errors.New("task exceeds sync transfer size limit")
			}
			frame.Tasks.NextCursor = frame.Tasks.Tasks[len(frame.Tasks.Tasks)-1].ID
			break
		}
		frame.Tasks.Tasks = append(frame.Tasks.Tasks, task)
		taskBytes += len(encodedTask)
	}
	if err = taskRows.Err(); err != nil {
		taskRows.Close()
		return frame, err
	}
	if err = taskRows.Close(); err != nil {
		return frame, err
	}
	eventBudget := maxSyncFrameBytes - baseBytes - taskBytes
	if eventBudget > maxSyncEventBytes {
		eventBudget = maxSyncEventBytes
	}
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM events WHERE project_id=?`, project).Scan(&frame.SnapshotSequence)
	if err != nil {
		return frame, err
	}
	// An explicit retention prune can remove history while leaving the home
	// instance unchanged. A cursor before that watermark cannot safely consume
	// deltas alone; force the adapter to replace cached state from this snapshot.
	err = tx.QueryRowContext(ctx, `SELECT pruned_through FROM event_retention WHERE project_id=?`, project).Scan(&frame.HistoryPrunedThrough)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return frame, err
	}
	if after < frame.HistoryPrunedThrough {
		frame.ResetRequired = true
		frame.EventsAfter = 0
		after = 0
	}
	if after > frame.SnapshotSequence {
		frame.ResetRequired = true
		frame.EventsAfter = 0
		after = 0
	}
	eventRows, err := tx.QueryContext(ctx, `SELECT e.seq,e.actor,e.kind,e.entity_id,e.payload,e.created_at FROM events e WHERE e.project_id=? AND e.seq>? AND e.seq<=?
		AND (?='owner' OR e.kind NOT LIKE 'certificate.%')
		AND (e.kind NOT IN ('message.sent','message.acknowledged') OR EXISTS (
			SELECT 1 FROM messages m WHERE m.id=e.entity_id AND m.project_id=e.project_id
			AND (m.recipient IS NULL OR m.recipient=? OR m.sender=?)))
		ORDER BY e.seq LIMIT ?`, project, after, frame.SnapshotSequence, actor.Role, actor.ID, actor.ID, limit+1)
	if err != nil {
		return frame, err
	}
	eventBytes := 0
	eventByteLimitHit := false
	for eventRows.Next() {
		var event Event
		var payload string
		if err = eventRows.Scan(&event.Sequence, &event.Actor, &event.Kind, &event.EntityID, &payload, &event.CreatedAt); err != nil {
			eventRows.Close()
			return frame, err
		}
		if len(frame.Events) == limit {
			frame.EventsMayContinue = true
			frame.NextEventCursor = frame.Events[len(frame.Events)-1].Sequence
			break
		}
		event.Payload = []byte(payload)
		encodedEvent, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			eventRows.Close()
			return frame, marshalErr
		}
		if eventBytes+len(encodedEvent) > eventBudget {
			if len(frame.Events) == 0 {
				eventRows.Close()
				return frame, errors.New("event exceeds sync transfer size limit")
			}
			frame.EventsMayContinue = true
			frame.NextEventCursor = frame.Events[len(frame.Events)-1].Sequence
			eventByteLimitHit = true
			break
		}
		frame.Events = append(frame.Events, event)
		eventBytes += len(encodedEvent)
	}
	if err = eventRows.Err(); err != nil {
		eventRows.Close()
		return frame, err
	}
	if err = eventRows.Close(); err != nil {
		return frame, err
	}
	if !eventByteLimitHit && !frame.EventsMayContinue {
		frame.NextEventCursor = frame.SnapshotSequence
	}
	if replicaID != "" {
		now := time.Now().UnixMilli()
		_, err = tx.ExecContext(ctx, `INSERT INTO project_replicas(project_id,replica_id,principal_id,certificate_fingerprint,first_seen_at,last_seen_at,last_event_cursor,snapshot_sequence) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(project_id,replica_id) DO UPDATE SET last_seen_at=excluded.last_seen_at,last_event_cursor=excluded.last_event_cursor,snapshot_sequence=excluded.snapshot_sequence`, project, replicaID, actor.ID, fingerprint, now, now, frame.NextEventCursor, frame.SnapshotSequence)
		if err != nil {
			return frame, err
		}
	}
	return frame, tx.Commit()
}

// ListProjectReplicas exposes federation receipts to project owners only.
func (s *Store) ListProjectReplicas(ctx context.Context, project, token string) ([]ProjectReplica, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = authenticate(ctx, tx, project, token, "owner"); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT project_id,replica_id,principal_id,certificate_fingerprint,first_seen_at,last_seen_at,last_event_cursor,snapshot_sequence FROM project_replicas WHERE project_id=? ORDER BY last_seen_at DESC,replica_id`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	replicas := []ProjectReplica{}
	for rows.Next() {
		var replica ProjectReplica
		if err = rows.Scan(&replica.ProjectID, &replica.ReplicaID, &replica.PrincipalID, &replica.CertificateFingerprint, &replica.FirstSeenAt, &replica.LastSeenAt, &replica.LastEventCursor, &replica.SnapshotSequence); err != nil {
			return nil, err
		}
		replicas = append(replicas, replica)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return replicas, tx.Commit()
}
