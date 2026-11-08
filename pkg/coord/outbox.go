package coord

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Outbox persists messages on the agent host; it never stores the credential.
type Outbox struct {
	db           *sql.DB
	replicaID    string
	cancel       context.CancelFunc
	done         chan struct{}
	wake         chan struct{}
	closeOnce    sync.Once
	reportDoneMu sync.Mutex
	reportDone   func(string, int)
}
type OutboxMessage struct {
	RequestID string `json:"request_id"`
	Recipient string `json:"recipient,omitempty"`
	Thread    string `json:"thread_id,omitempty"`
	Subject   string `json:"subject,omitempty"`
	Body      string `json:"body,omitempty"`
	Status    string `json:"status"`
	MessageID string `json:"message_id,omitempty"`
	Error     string `json:"error,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

// OpenOutbox binds a private local queue to one home origin and credential.
func OpenOutbox(path, origin, project, principal, token string, backend Backend) (*Outbox, error) {
	if path == "" || path == ":memory:" || origin == "" || project == "" || principal == "" || token == "" {
		return nil, errors.New("outbox path, home, project and credential required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs), RawQuery: q.Encode()}).String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE IF NOT EXISTS identity (id INTEGER PRIMARY KEY CHECK(id=1), binding TEXT NOT NULL); CREATE TABLE IF NOT EXISTS outbound (request_id TEXT PRIMARY KEY, recipient TEXT NOT NULL, thread TEXT NOT NULL, subject TEXT NOT NULL, body TEXT NOT NULL, status TEXT NOT NULL, message_id TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL); CREATE TABLE IF NOT EXISTS cached_snapshots (kind TEXT PRIMARY KEY, value BLOB NOT NULL, observed_at INTEGER NOT NULL); CREATE TABLE IF NOT EXISTS cached_events (sequence INTEGER PRIMARY KEY, value BLOB NOT NULL, observed_at INTEGER NOT NULL); CREATE TABLE IF NOT EXISTS cached_event_meta (id INTEGER PRIMARY KEY CHECK(id=1), truncated_through INTEGER NOT NULL DEFAULT 0); INSERT OR IGNORE INTO cached_event_meta(id,truncated_through) VALUES(1,0); CREATE TABLE IF NOT EXISTS cached_event_cursor (id INTEGER PRIMARY KEY CHECK(id=1), sequence INTEGER NOT NULL DEFAULT 0); INSERT OR IGNORE INTO cached_event_cursor(id,sequence) VALUES(1,0); CREATE TABLE IF NOT EXISTS cached_home_instance (id INTEGER PRIMARY KEY CHECK(id=1), instance_id TEXT NOT NULL DEFAULT ''); INSERT OR IGNORE INTO cached_home_instance(id,instance_id) VALUES(1,''); CREATE TABLE IF NOT EXISTS candidate_outbound (request_id TEXT PRIMARY KEY, task_id TEXT NOT NULL, attempt INTEGER NOT NULL, baseline TEXT NOT NULL, body TEXT NOT NULL, status TEXT NOT NULL, candidate_id TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL); CREATE TABLE IF NOT EXISTS review_outbound (request_id TEXT PRIMARY KEY, task_id TEXT NOT NULL, attempt INTEGER NOT NULL, status TEXT NOT NULL, error TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, retry_at INTEGER NOT NULL DEFAULT 0, UNIQUE(task_id,attempt)); CREATE TABLE IF NOT EXISTS report_outbound (request_id TEXT PRIMARY KEY, task_id TEXT NOT NULL, attempt INTEGER NOT NULL, baseline TEXT NOT NULL, body TEXT NOT NULL, status TEXT NOT NULL, report_id TEXT NOT NULL DEFAULT '', digest TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, retry_at INTEGER NOT NULL DEFAULT 0, UNIQUE(task_id,attempt));`)
	if err != nil {
		db.Close()
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS integration_outbound (request_id TEXT PRIMARY KEY, task_id TEXT NOT NULL, attempt INTEGER NOT NULL, combined_revision TEXT NOT NULL, payload_json TEXT NOT NULL, status TEXT NOT NULL, integration_id TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, retry_at INTEGER NOT NULL DEFAULT 0, UNIQUE(task_id,attempt))`)
	if err != nil {
		db.Close()
		return nil, err
	}
	for table := range map[string]bool{"outbound": true, "candidate_outbound": true, "integration_outbound": true, "report_outbound": true} {
		columns, e := db.Query(`PRAGMA table_info(` + table + `)`)
		if e != nil {
			db.Close()
			return nil, e
		}
		found := map[string]bool{}
		for columns.Next() {
			var cid, notnull, pk int
			var name, typ string
			var dflt any
			if e = columns.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); e != nil {
				columns.Close()
				db.Close()
				return nil, e
			}
			found[name] = true
		}
		columns.Close()
		if !found["attempts"] {
			if _, e = db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0`); e != nil {
				db.Close()
				return nil, e
			}
		}
		if !found["retry_at"] {
			if _, e = db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN retry_at INTEGER NOT NULL DEFAULT 0`); e != nil {
				db.Close()
				return nil, e
			}
		}
	}
	digest := sha256.Sum256([]byte(origin + "\x00" + project + "\x00" + principal + "\x00" + token))
	binding := hex.EncodeToString(digest[:])
	if _, err = db.Exec(`INSERT OR IGNORE INTO identity(id,binding) VALUES(1,?)`, binding); err != nil {
		db.Close()
		return nil, err
	}
	var existing string
	if err = db.QueryRow(`SELECT binding FROM identity WHERE id=1`).Scan(&existing); err != nil || existing != binding {
		db.Close()
		return nil, errors.New("outbox belongs to a different home or credential")
	}
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS replica_identity (id INTEGER PRIMARY KEY CHECK(id=1), replica_id TEXT NOT NULL)`); err != nil {
		db.Close()
		return nil, err
	}
	var replicaID string
	err = db.QueryRow(`SELECT replica_id FROM replica_identity WHERE id=1`).Scan(&replicaID)
	if errors.Is(err, sql.ErrNoRows) {
		candidate, idErr := randomID("replica_")
		err = idErr
		if err == nil {
			_, err = db.Exec(`INSERT OR IGNORE INTO replica_identity(id,replica_id) VALUES(1,?)`, candidate)
		}
		if err == nil {
			err = db.QueryRow(`SELECT replica_id FROM replica_identity WHERE id=1`).Scan(&replicaID)
		}
	}
	_, replicaHexErr := hex.DecodeString(strings.TrimPrefix(replicaID, "replica_"))
	if err != nil || !strings.HasPrefix(replicaID, "replica_") || len(replicaID) != len("replica_")+32 || replicaHexErr != nil {
		db.Close()
		if err == nil {
			err = errors.New("invalid persisted replica identity")
		}
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	o := &Outbox{db: db, replicaID: replicaID, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1)}
	if binder, ok := backend.(interface{ SetReplicaID(string) }); ok {
		binder.SetReplicaID(replicaID)
	}
	go o.run(ctx, backend, project, token)
	return o, nil
}
func (o *Outbox) ReplicaID() string { return o.replicaID }
func (o *Outbox) Close()            { o.closeOnce.Do(func() { o.cancel(); <-o.done; o.db.Close() }) }
func (o *Outbox) setReportDelivered(fn func(string, int)) {
	if o == nil {
		return
	}
	o.reportDoneMu.Lock()
	o.reportDone = fn
	o.reportDoneMu.Unlock()
}
func (o *Outbox) notifyReportDelivered(task string, attempt int) {
	o.reportDoneMu.Lock()
	fn := o.reportDone
	o.reportDoneMu.Unlock()
	if fn != nil {
		fn(task, attempt)
	}
}
func (o *Outbox) signal() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}
func (o *Outbox) Enqueue(ctx context.Context, recipient, thread, subject, body, id string) (OutboxMessage, error) {
	var m OutboxMessage
	if strings.TrimSpace(body) == "" || len(body) > 64*1024 || len(subject) > 512 || len(thread) > 256 || len(recipient) > 256 || len(id) > 128 || !utf8.ValidString(body) || !utf8.ValidString(subject) || !utf8.ValidString(thread) || !utf8.ValidString(recipient) || !utf8.ValidString(id) {
		return m, errors.New("invalid message")
	}
	if id == "" {
		var err error
		id, err = randomID("request_")
		if err != nil {
			return m, err
		}
	}
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return m, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `SELECT request_id,recipient,thread,subject,body,status,message_id,error,created_at FROM outbound WHERE request_id=?`, id).Scan(&m.RequestID, &m.Recipient, &m.Thread, &m.Subject, &m.Body, &m.Status, &m.MessageID, &m.Error, &m.CreatedAt)
	if err == nil {
		if m.Recipient != recipient || m.Thread != thread || m.Subject != subject || m.Body != body {
			return OutboxMessage{}, ErrConflict
		}
		return m, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return m, err
	}
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbound`).Scan(&n); err != nil {
		return m, err
	}
	if n >= 1000 {
		return m, errors.New("outbox full; remove delivered entries")
	}
	m = OutboxMessage{RequestID: id, Recipient: recipient, Thread: thread, Subject: subject, Body: body, Status: "pending", CreatedAt: time.Now().UnixMilli()}
	_, err = tx.ExecContext(ctx, `INSERT INTO outbound(request_id,recipient,thread,subject,body,status,created_at) VALUES(?,?,?,?,?,'pending',?)`, id, recipient, thread, subject, body, m.CreatedAt)
	if err != nil {
		return OutboxMessage{}, err
	}
	if err = tx.Commit(); err != nil {
		return OutboxMessage{}, err
	}
	o.signal()
	return m, nil
}
func (o *Outbox) List(ctx context.Context, limit int) ([]OutboxMessage, error) {
	if limit < 1 || limit > 200 {
		return nil, errors.New("invalid limit")
	}
	rows, err := o.db.QueryContext(ctx, `SELECT request_id,recipient,thread,subject,status,message_id,error,created_at FROM outbound ORDER BY created_at DESC,request_id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OutboxMessage{}
	for rows.Next() {
		var m OutboxMessage
		if err = rows.Scan(&m.RequestID, &m.Recipient, &m.Thread, &m.Subject, &m.Status, &m.MessageID, &m.Error, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (o *Outbox) Prune(ctx context.Context) (int64, error) {
	r, err := o.db.ExecContext(ctx, `DELETE FROM outbound WHERE status='delivered'`)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

// Retry requeues a permanently rejected message after the underlying issue is fixed.
func (o *Outbox) Retry(ctx context.Context, id string) error {
	res, err := o.db.ExecContext(ctx, `UPDATE outbound SET status='pending',error='' WHERE request_id=? AND status='failed'`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	o.signal()
	return nil
}

func (o *Outbox) run(ctx context.Context, b Backend, project, token string) {
	defer close(o.done)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-o.wake:
		case <-timer.C:
		}
		// Review requests are coordination control traffic; deliver them before
		// chat and report payloads so a large backlog cannot delay handoff.
		o.deliverReviews(ctx, b, project, token)
		o.deliverReports(ctx, b, project, token)
		for ctx.Err() == nil {
			var m OutboxMessage
			err := o.db.QueryRowContext(ctx, `SELECT request_id,recipient,thread,subject,body FROM outbound WHERE status='pending' AND retry_at<=? ORDER BY created_at,request_id LIMIT 1`, time.Now().UnixMilli()).Scan(&m.RequestID, &m.Recipient, &m.Thread, &m.Subject, &m.Body)
			if err != nil {
				break
			}
			request, cancel := context.WithTimeout(ctx, 20*time.Second)
			sent, err := b.SendMessageWithID(request, project, token, m.Recipient, m.Thread, m.Subject, m.Body, m.RequestID)
			cancel()
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				if errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
					_, _ = o.db.ExecContext(ctx, `UPDATE outbound SET status='failed',error=? WHERE request_id=? AND status='pending'`, err.Error(), m.RequestID)
					continue
				}
				o.deferOutbound(ctx, "outbound", m.RequestID, err.Error())
				break
			}
			if _, err = o.db.ExecContext(ctx, `UPDATE outbound SET status='delivered',message_id=?,error='' WHERE request_id=? AND status='pending'`, sent.ID, m.RequestID); err != nil {
				break
			}
		}
		o.deliverCandidates(ctx, b, project, token)
		o.deliverIntegrations(ctx, b, project, token)
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(time.Second)
	}
}

func (o *Outbox) deferOutbound(ctx context.Context, table, id, message string) {
	var attempts int
	_ = o.db.QueryRowContext(ctx, `SELECT attempts FROM `+table+` WHERE request_id=?`, id).Scan(&attempts)
	attempts++
	delay := time.Second << min(attempts-1, 10)
	if delay > time.Hour {
		delay = time.Hour
	}
	j := sha256.Sum256([]byte(id + string(rune(attempts))))
	delay = delay * time.Duration(80+int(j[0])%41) / 100
	_, _ = o.db.ExecContext(ctx, `UPDATE `+table+` SET attempts=?,retry_at=?,error=? WHERE request_id=? AND status='pending'`, attempts, time.Now().Add(delay).UnixMilli(), message, id)
}
