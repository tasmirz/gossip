package coord

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	maxCachedTaskCount   = 10000
	maxCachedTaskBytes   = 8 * 1024 * 1024
	maxRefreshEventCount = 10000
	maxRefreshEventBytes = 64 * 1024 * 1024
)

// CachedSnapshot is a point-in-time observation, not current authority.
type CachedSnapshot struct {
	Source     string          `json:"source"`
	ObservedAt int64           `json:"observed_at"`
	Value      json.RawMessage `json:"value"`
}

type CacheSyncStatus struct {
	Source               string `json:"source"`
	ObservedAt           int64  `json:"observed_at"`
	PlanAvailable        bool   `json:"plan_available"`
	PlanRevision         int    `json:"plan_revision"`
	TasksCached          int    `json:"tasks_cached"`
	TaskNextCursor       string `json:"task_next_cursor,omitempty"`
	EventsCached         int    `json:"events_cached"`
	EventsAfter          int64  `json:"events_after"`
	NextEventCursor      int64  `json:"next_event_cursor"`
	SnapshotSequence     int64  `json:"snapshot_sequence"`
	HistoryPrunedThrough int64  `json:"history_pruned_through"`
	LastEventSequence    int64  `json:"last_event_sequence"`
	EventsMayContinue    bool   `json:"events_may_continue"`
	ResetRequired        bool   `json:"reset_required"`
}

func (o *Outbox) cacheSnapshot(ctx context.Context, kind string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(b) > 8*1024*1024 {
		return errors.New("snapshot exceeds size limit")
	}
	_, err = o.db.ExecContext(ctx, `INSERT INTO cached_snapshots(kind,value,observed_at) VALUES(?,?,?) ON CONFLICT(kind) DO UPDATE SET value=excluded.value,observed_at=excluded.observed_at`, kind, b, time.Now().UnixMilli())
	return err
}

func (o *Outbox) refreshCache(ctx context.Context, backend Backend, project, token string) (CacheSyncStatus, error) {
	var status CacheSyncStatus
	var after int64
	if err := o.db.QueryRowContext(ctx, `SELECT sequence FROM cached_event_cursor WHERE id=1`).Scan(&after); err != nil {
		return status, err
	}
	var knownHome string
	if err := o.db.QueryRowContext(ctx, `SELECT instance_id FROM cached_home_instance WHERE id=1`).Scan(&knownHome); err != nil {
		return status, err
	}
	frame, err := backend.SyncProject(ctx, project, token, after, 200)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			if clearErr := o.ClearCache(ctx); clearErr != nil {
				return status, fmt.Errorf("project access revoked; cached data cleanup failed: %w", clearErr)
			}
		}
		return status, err
	}
	if frame.HomeInstanceID == "" {
		return status, errors.New("home sync response omitted its instance identity")
	}
	if knownHome == "" || knownHome != frame.HomeInstanceID {
		frame, err = backend.SyncProject(ctx, project, token, 0, 200)
		if err != nil {
			if errors.Is(err, ErrUnauthorized) {
				if clearErr := o.ClearCache(ctx); clearErr != nil {
					return status, fmt.Errorf("project access revoked; cached data cleanup failed: %w", clearErr)
				}
			}
			return status, err
		}
		if frame.HomeInstanceID == "" {
			return status, errors.New("home sync response omitted its instance identity")
		}
		frame.ResetRequired = true
		frame.EventsAfter = 0
	}
	// Drain additional bounded event pages in this refresh. Keep the final
	// cursor at the last event actually committed if a local work limit stops
	// the drain, so the next call can continue without skipping unseen events.
	firstEventCursor := frame.EventsAfter
	events := append([]Event(nil), frame.Events...)
	eventBytes := 0
	for _, event := range events {
		encoded, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			return status, marshalErr
		}
		eventBytes += len(encoded)
	}
	eventCursor := frame.NextEventCursor
	eventsMayContinue := frame.EventsMayContinue
	for eventsMayContinue && len(events) < maxRefreshEventCount && eventBytes < maxRefreshEventBytes {
		page, syncErr := backend.SyncProject(ctx, project, token, eventCursor, 200)
		if syncErr != nil {
			return status, syncErr
		}
		if page.HomeInstanceID != frame.HomeInstanceID || page.ResetRequired || page.EventsAfter != eventCursor || page.HistoryPrunedThrough != frame.HistoryPrunedThrough {
			return status, errors.New("home sync identity, event cursor, or retention boundary changed during cache refresh; retry")
		}
		if len(page.Events) == 0 && page.EventsMayContinue {
			return status, errors.New("home event cursor did not advance during cache refresh")
		}
		localLimitReached := false
		for _, event := range page.Events {
			encoded, marshalErr := json.Marshal(event)
			if marshalErr != nil {
				return status, marshalErr
			}
			if len(events) >= maxRefreshEventCount || eventBytes+len(encoded) > maxRefreshEventBytes {
				eventsMayContinue = true
				localLimitReached = true
				break
			}
			events = append(events, event)
			eventBytes += len(encoded)
			eventCursor = event.Sequence
		}
		if localLimitReached {
			break
		}
		eventCursor = page.NextEventCursor
		eventsMayContinue = page.EventsMayContinue
		frame.SnapshotSequence = page.SnapshotSequence
	}
	frame.Events = events
	frame.EventsAfter = firstEventCursor
	frame.NextEventCursor = eventCursor
	frame.EventsMayContinue = eventsMayContinue
	// SyncProject returns the first page with the plan and event watermark in
	// one read transaction. Continue older tasks with the stable keyset cursor
	// so caches cover the whole project instead of silently stopping at 200.
	tasks := append([]Task(nil), frame.Tasks.Tasks...)
	cursor := frame.Tasks.NextCursor
	taskBytes := 0
	for _, task := range tasks {
		encoded, marshalErr := json.Marshal(task)
		if marshalErr != nil {
			return status, marshalErr
		}
		taskBytes += len(encoded)
	}
	for cursor != "" {
		if len(tasks) >= maxCachedTaskCount {
			return status, errors.New("project task snapshot exceeds the cache task limit")
		}
		page, searchErr := backend.SearchTasks(ctx, project, token, TaskFilter{Cursor: cursor, Limit: 200})
		if searchErr != nil {
			return status, searchErr
		}
		if page.NextCursor != "" && (page.NextCursor == cursor || len(page.Tasks) == 0) {
			return status, errors.New("project task snapshot cursor did not advance")
		}
		for _, task := range page.Tasks {
			encoded, marshalErr := json.Marshal(task)
			if marshalErr != nil {
				return status, marshalErr
			}
			taskBytes += len(encoded)
			if taskBytes > maxCachedTaskBytes {
				return status, errors.New("project task snapshot exceeds the cache byte limit")
			}
			tasks = append(tasks, task)
			if len(tasks) > maxCachedTaskCount {
				return status, errors.New("project task snapshot exceeds the cache task limit")
			}
		}
		cursor = page.NextCursor
	}
	frame.Tasks = TaskPage{Tasks: tasks}
	values := map[string]any{"tasks": frame.Tasks.Tasks, "counts": frame.Counts}
	if frame.Plan != nil {
		values["plan"] = frame.Plan
	}
	encoded := make(map[string][]byte, len(values))
	for kind, value := range values {
		data, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return status, marshalErr
		}
		if len(data) > maxCachedTaskBytes {
			return status, errors.New("snapshot exceeds size limit")
		}
		encoded[kind] = data
	}
	now := time.Now().UnixMilli()
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return status, err
	}
	defer tx.Rollback()
	if frame.ResetRequired {
		if _, err = tx.ExecContext(ctx, `DELETE FROM cached_events`); err != nil {
			return status, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE cached_event_meta SET truncated_through=0 WHERE id=1`); err != nil {
			return status, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE cached_home_instance SET instance_id='' WHERE id=1`); err != nil {
			return status, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM cached_snapshots WHERE kind IN ('plan','tasks','counts')`); err != nil {
			return status, err
		}
	}
	for kind, data := range encoded {
		if _, err = tx.ExecContext(ctx, `INSERT INTO cached_snapshots(kind,value,observed_at) VALUES(?,?,?) ON CONFLICT(kind) DO UPDATE SET value=excluded.value,observed_at=excluded.observed_at`, kind, data, now); err != nil {
			return status, err
		}
	}
	if frame.Plan == nil {
		if _, err = tx.ExecContext(ctx, `DELETE FROM cached_snapshots WHERE kind='plan'`); err != nil {
			return status, err
		}
	}
	if err = cacheEventsTx(ctx, tx, frame.Events, now); err != nil {
		return status, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE cached_event_cursor SET sequence=? WHERE id=1`, frame.NextEventCursor); err != nil {
		return status, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE cached_home_instance SET instance_id=? WHERE id=1`, frame.HomeInstanceID); err != nil {
		return status, err
	}
	if err = tx.Commit(); err != nil {
		return status, err
	}
	status = CacheSyncStatus{Source: "bounded home task pages and event deltas committed to project-scoped local cache; pages may observe concurrent changes and remain non-authoritative", ObservedAt: now, PlanAvailable: frame.Plan != nil, TasksCached: len(frame.Tasks.Tasks), TaskNextCursor: frame.Tasks.NextCursor, EventsCached: len(frame.Events), EventsAfter: frame.EventsAfter, NextEventCursor: frame.NextEventCursor, SnapshotSequence: frame.SnapshotSequence, HistoryPrunedThrough: frame.HistoryPrunedThrough, EventsMayContinue: frame.EventsMayContinue, ResetRequired: frame.ResetRequired}
	if frame.Plan != nil {
		status.PlanRevision = frame.Plan.Revision
	}
	if len(frame.Events) > 0 {
		status.LastEventSequence = frame.Events[len(frame.Events)-1].Sequence
	} else {
		status.LastEventSequence = frame.NextEventCursor
	}
	return status, nil
}

// ClearCache removes only previously received project snapshots and events.
// Pending outbound requests remain available for explicit owner review.
func (o *Outbox) ClearCache(ctx context.Context) error {
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, query := range []string{
		`DELETE FROM cached_snapshots`,
		`DELETE FROM cached_events`,
		`UPDATE cached_event_meta SET truncated_through=0 WHERE id=1`,
		`UPDATE cached_event_cursor SET sequence=0 WHERE id=1`,
		`UPDATE cached_home_instance SET instance_id='' WHERE id=1`,
	} {
		if _, err = tx.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (o *Outbox) readSnapshot(ctx context.Context, kind string) (CachedSnapshot, error) {
	snapshot := CachedSnapshot{Source: "cached; freshness and current authorization are unknown"}
	err := o.db.QueryRowContext(ctx, `SELECT value,observed_at FROM cached_snapshots WHERE kind=?`, kind).Scan(&snapshot.Value, &snapshot.ObservedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return CachedSnapshot{}, ErrNotFound
	}
	return snapshot, err
}

// CachedEventPage contains only events previously observed by this project
// credential. It is a local cache, not an authoritative view of current state.
type CachedEventPage struct {
	Source           string  `json:"source"`
	ObservedAt       int64   `json:"observed_at"`
	FirstSequence    int64   `json:"first_sequence"`
	LastSequence     int64   `json:"last_sequence"`
	TruncatedThrough int64   `json:"truncated_through"`
	Events           []Event `json:"events"`
}

func (o *Outbox) cacheEvents(ctx context.Context, events []Event) error {
	if len(events) > 200 {
		return errors.New("event page exceeds size limit")
	}
	if len(events) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = cacheEventsTx(ctx, tx, events, now); err != nil {
		return err
	}
	return tx.Commit()
}

func cacheEventsTx(ctx context.Context, tx *sql.Tx, events []Event, now int64) error {
	if len(events) > maxRefreshEventCount {
		return errors.New("event cache refresh exceeds the event count limit")
	}
	for _, event := range events {
		if event.Sequence <= 0 {
			return errors.New("invalid cached event")
		}
		value, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			return marshalErr
		}
		if len(value) > 256*1024 {
			return errors.New("cached event exceeds size limit")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO cached_events(sequence,value,observed_at) VALUES(?,?,?) ON CONFLICT(sequence) DO UPDATE SET value=excluded.value,observed_at=excluded.observed_at`, event.Sequence, value, now); err != nil {
			return err
		}
	}
	var cutoff sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MIN(sequence) FROM (
		SELECT sequence, ROW_NUMBER() OVER (ORDER BY sequence DESC) AS row_number,
		SUM(length(value)) OVER (ORDER BY sequence DESC) AS total_bytes
		FROM cached_events
	) WHERE row_number<=10000 AND total_bytes<=67108864`).Scan(&cutoff); err != nil {
		return err
	}
	if cutoff.Valid {
		var deletedThrough sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT MAX(sequence) FROM cached_events WHERE sequence<?`, cutoff.Int64).Scan(&deletedThrough); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM cached_events WHERE sequence<?`, cutoff.Int64); err != nil {
			return err
		}
		if deletedThrough.Valid {
			if _, err := tx.ExecContext(ctx, `UPDATE cached_event_meta SET truncated_through=MAX(truncated_through,?) WHERE id=1`, deletedThrough.Int64); err != nil {
				return err
			}
		}
	}
	return nil
}

func (o *Outbox) readCachedEvents(ctx context.Context, after, limit int64) (CachedEventPage, error) {
	page := CachedEventPage{Source: "cached; may be stale and does not reflect current authorization", Events: []Event{}}
	if after < 0 || limit < 1 || limit > 200 {
		return page, errors.New("invalid event cursor or limit")
	}
	if err := o.db.QueryRowContext(ctx, `SELECT truncated_through FROM cached_event_meta WHERE id=1`).Scan(&page.TruncatedThrough); err != nil {
		return page, err
	}
	rows, err := o.db.QueryContext(ctx, `SELECT value,observed_at FROM cached_events WHERE sequence>? ORDER BY sequence LIMIT ?`, after, limit)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var value []byte
		var observed int64
		var event Event
		if err = rows.Scan(&value, &observed); err != nil {
			return page, err
		}
		if err = json.Unmarshal(value, &event); err != nil {
			return page, err
		}
		page.Events = append(page.Events, event)
		if page.FirstSequence == 0 {
			page.FirstSequence = event.Sequence
		}
		page.LastSequence = event.Sequence
		if observed > page.ObservedAt {
			page.ObservedAt = observed
		}
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	if len(page.Events) == 0 {
		return page, ErrNotFound
	}
	return page, nil
}
