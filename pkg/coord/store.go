// Package coord implements the transactional local collaboration core.
package coord

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrUnauthorized = errors.New("unauthorized")
	ErrConflict     = errors.New("conflict")
	ErrNotFound     = errors.New("not found")
	ErrUnavailable  = errors.New("remote service unavailable")
)

type Store struct {
	db         *sql.DB
	instanceID string
}

// Open opens a local database. Do not place it on a network filesystem.
func Open(path string) (*Store, error) {
	if path == "" || path == ":memory:" {
		return nil, errors.New("a local database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	params := url.Values{}
	params.Add("_pragma", "foreign_keys(1)")
	params.Add("_pragma", "busy_timeout(5000)")
	params.Set("_txlock", "immediate")
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs), RawQuery: params.Encode()}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, query := range []string{"PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL", schema} {
		if _, err := db.Exec(query); err != nil {
			db.Close()
			return nil, fmt.Errorf("initialize coordination database: %w", err)
		}
	}
	// Add descriptive MCP client fields to existing databases without replacing their data.
	for _, column := range []struct{ name, ddl string }{{"client_name", `ALTER TABLE agent_sessions ADD COLUMN client_name TEXT NOT NULL DEFAULT ''`}, {"client_version", `ALTER TABLE agent_sessions ADD COLUMN client_version TEXT NOT NULL DEFAULT ''`}} {
		rows, queryErr := db.Query(`PRAGMA table_info(agent_sessions)`)
		if queryErr != nil {
			db.Close()
			return nil, queryErr
		}
		found := false
		for rows.Next() {
			var cid, notnull, pk int
			var name, typ string
			var defaultValue any
			if queryErr = rows.Scan(&cid, &name, &typ, &notnull, &defaultValue, &pk); queryErr != nil {
				break
			}
			if name == column.name {
				found = true
			}
		}
		if closeErr := rows.Close(); queryErr == nil {
			queryErr = closeErr
		}
		if queryErr != nil {
			db.Close()
			return nil, queryErr
		}
		if !found {
			if _, queryErr = db.Exec(column.ddl); queryErr != nil {
				db.Close()
				return nil, fmt.Errorf("migrate coordination database: %w", queryErr)
			}
		}
	}
	// Seed conservative holds for installations predating per-attempt accounting.
	tx, err := db.Begin()
	if err != nil {
		db.Close()
		return nil, err
	}
	if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS coord_migrations (name TEXT PRIMARY KEY)`); err == nil {
		var done int
		err = tx.QueryRow(`SELECT COUNT(*) FROM coord_migrations WHERE name='budget_attempt_holds_v1'`).Scan(&done)
		if err == nil && done == 0 {
			_, err = tx.Exec(`INSERT OR IGNORE INTO budget_attempt_holds(task_id,attempt,amount) SELECT a.task_id,a.number,r.amount FROM attempts a JOIN budget_reservations r ON r.task_id=a.task_id`)
			if err == nil {
				_, err = tx.Exec(`INSERT INTO coord_migrations(name) VALUES('budget_attempt_holds_v1')`)
			}
		}
		if err == nil {
			var permissionsDone int
			err = tx.QueryRow(`SELECT COUNT(*) FROM coord_migrations WHERE name='permission_catalog_v1'`).Scan(&permissionsDone)
			if err == nil && permissionsDone == 0 {
				for _, query := range []string{
					`ALTER TABLE principal_permissions RENAME TO principal_permissions_legacy`,
					`CREATE TABLE principal_permissions (
  project_id TEXT NOT NULL REFERENCES projects(id), principal_id TEXT NOT NULL REFERENCES principals(id),
  permission TEXT NOT NULL CHECK(permission IN ('task.delegate','plan.edit','settings.edit')),
  granted_by TEXT NOT NULL REFERENCES principals(id), granted_at INTEGER NOT NULL,
  PRIMARY KEY(project_id,principal_id,permission)
)`,
					`INSERT INTO principal_permissions(project_id,principal_id,permission,granted_by,granted_at)
  SELECT project_id,principal_id,permission,granted_by,granted_at FROM principal_permissions_legacy`,
					`DROP TABLE principal_permissions_legacy`,
					`INSERT INTO coord_migrations(name) VALUES('permission_catalog_v1')`,
				} {
					if _, err = tx.Exec(query); err != nil {
						break
					}
				}
			}
		}
		if err == nil {
			var permissionReviewDone int
			err = tx.QueryRow(`SELECT COUNT(*) FROM coord_migrations WHERE name='permission_review_decide_v2'`).Scan(&permissionReviewDone)
			if err == nil && permissionReviewDone == 0 {
				for _, query := range []string{
					`ALTER TABLE principal_permissions RENAME TO principal_permissions_previous`,
					`CREATE TABLE principal_permissions (
  project_id TEXT NOT NULL REFERENCES projects(id), principal_id TEXT NOT NULL REFERENCES principals(id),
  permission TEXT NOT NULL CHECK(permission IN ('task.delegate','plan.edit','settings.edit','review.decide')),
  granted_by TEXT NOT NULL REFERENCES principals(id), granted_at INTEGER NOT NULL,
  PRIMARY KEY(project_id,principal_id,permission)
)`,
					`INSERT INTO principal_permissions(project_id,principal_id,permission,granted_by,granted_at)
  SELECT project_id,principal_id,permission,granted_by,granted_at FROM principal_permissions_previous`,
					`DROP TABLE principal_permissions_previous`,
					`INSERT INTO coord_migrations(name) VALUES('permission_review_decide_v2')`,
				} {
					if _, err = tx.Exec(query); err != nil {
						break
					}
				}
			}
		}
		if err == nil {
			var actionPermissionsDone int
			err = tx.QueryRow(`SELECT COUNT(*) FROM coord_migrations WHERE name='project_action_permissions_v3'`).Scan(&actionPermissionsDone)
			if err == nil && actionPermissionsDone == 0 {
				queries := []string{
					`ALTER TABLE principal_permissions RENAME TO principal_permissions_v2`,
					`CREATE TABLE principal_permissions (
  project_id TEXT NOT NULL REFERENCES projects(id), principal_id TEXT NOT NULL REFERENCES principals(id),
  permission TEXT NOT NULL CHECK(permission IN ('project.read','message.send','task.claim','task.delegate','result.submit','review.decide','plan.edit','settings.edit')),
  granted_by TEXT NOT NULL REFERENCES principals(id), granted_at INTEGER NOT NULL,
  PRIMARY KEY(project_id,principal_id,permission)
)`,
					`INSERT INTO principal_permissions(project_id,principal_id,permission,granted_by,granted_at)
  SELECT project_id,principal_id,permission,granted_by,granted_at FROM principal_permissions_v2`,
					`DROP TABLE principal_permissions_v2`,
				}
				for _, permission := range []struct{ name, role string }{
					{"project.read", "p.role IN ('worker','reviewer')"},
					{"message.send", "p.role IN ('worker','reviewer')"},
					{"task.claim", "p.role='worker'"},
					{"result.submit", "p.role='worker'"},
					{"review.decide", "p.role='reviewer'"},
				} {
					queries = append(queries, fmt.Sprintf(`INSERT OR IGNORE INTO principal_permissions(project_id,principal_id,permission,granted_by,granted_at)
  SELECT p.project_id,p.id,'%s',o.id,CAST(strftime('%%s','now') AS INTEGER)*1000
  FROM principals p JOIN principals o ON o.project_id=p.project_id AND o.role='owner' AND o.revoked=0
  WHERE p.revoked=0 AND %s`, permission.name, permission.role))
				}
				queries = append(queries, `INSERT INTO coord_migrations(name) VALUES('project_action_permissions_v3')`)
				for _, query := range queries {
					if _, err = tx.Exec(query); err != nil {
						break
					}
				}
			}
		}
		if err == nil {
			var membershipManageDone int
			err = tx.QueryRow(`SELECT COUNT(*) FROM coord_migrations WHERE name='membership_manage_v4'`).Scan(&membershipManageDone)
			if err == nil && membershipManageDone == 0 {
				for _, query := range []string{
					`ALTER TABLE principal_permissions RENAME TO principal_permissions_v3`,
					`CREATE TABLE principal_permissions (
  project_id TEXT NOT NULL REFERENCES projects(id), principal_id TEXT NOT NULL REFERENCES principals(id),
  permission TEXT NOT NULL CHECK(permission IN ('project.read','message.send','task.claim','task.delegate','result.submit','review.decide','plan.edit','settings.edit','membership.manage')),
  granted_by TEXT NOT NULL REFERENCES principals(id), granted_at INTEGER NOT NULL,
  PRIMARY KEY(project_id,principal_id,permission)
)`,
					`INSERT INTO principal_permissions(project_id,principal_id,permission,granted_by,granted_at)
  SELECT project_id,principal_id,permission,granted_by,granted_at FROM principal_permissions_v3`,
					`DROP TABLE principal_permissions_v3`,
					`INSERT INTO coord_migrations(name) VALUES('membership_manage_v4')`,
				} {
					if _, err = tx.Exec(query); err != nil {
						break
					}
				}
			}
		}
		if err == nil {
			var integrationPermissionDone int
			err = tx.QueryRow(`SELECT COUNT(*) FROM coord_migrations WHERE name='integration_permission_v5'`).Scan(&integrationPermissionDone)
			if err == nil && integrationPermissionDone == 0 {
				for _, query := range []string{
					`ALTER TABLE principal_permissions RENAME TO principal_permissions_v4`,
					`CREATE TABLE principal_permissions (
  project_id TEXT NOT NULL REFERENCES projects(id), principal_id TEXT NOT NULL REFERENCES principals(id),
  permission TEXT NOT NULL CHECK(permission IN ('project.read','message.send','task.claim','task.delegate','result.submit','review.decide','plan.edit','settings.edit','membership.manage','integration')),
  granted_by TEXT NOT NULL REFERENCES principals(id), granted_at INTEGER NOT NULL,
  PRIMARY KEY(project_id,principal_id,permission)
)`,
					`INSERT INTO principal_permissions(project_id,principal_id,permission,granted_by,granted_at)
  SELECT project_id,principal_id,permission,granted_by,granted_at FROM principal_permissions_v4`,
					`DROP TABLE principal_permissions_v4`,
					`INSERT INTO coord_migrations(name) VALUES('integration_permission_v5')`,
				} {
					if _, err = tx.Exec(query); err != nil {
						break
					}
				}
			}
		}
	}
	if err != nil {
		tx.Rollback()
		db.Close()
		return nil, fmt.Errorf("migrate coordination database: %w", err)
	}
	if err = tx.Commit(); err != nil {
		db.Close()
		return nil, err
	}
	instanceID, err := randomID("node_")
	if err != nil {
		db.Close()
		return nil, err
	}
	// The home identity is durable: replicas must distinguish a routine
	// process restart from an authority change. INSERT OR IGNORE also makes
	// concurrent opens converge on the same identity.
	if _, err = db.Exec(`INSERT OR IGNORE INTO node_identity(id,instance_id) VALUES(1,?)`, instanceID); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize node identity: %w", err)
	}
	if err = db.QueryRow(`SELECT instance_id FROM node_identity WHERE id=1`).Scan(&instanceID); err != nil || instanceID == "" {
		db.Close()
		if err == nil {
			err = errors.New("node identity is empty")
		}
		return nil, fmt.Errorf("read node identity: %w", err)
	}
	return &Store{db: db, instanceID: instanceID}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// RemoteAccessEnabled reports whether network-facing coordination endpoints are enabled.
func (s *Store) RemoteAccessEnabled(ctx context.Context) (bool, error) {
	var enabled int
	err := s.db.QueryRowContext(ctx, `SELECT enabled FROM remote_access_control WHERE id=1`).Scan(&enabled)
	return enabled != 0, err
}

// SetRemoteAccessEnabled changes the local remote-access kill switch.
func (s *Store) SetRemoteAccessEnabled(ctx context.Context, enabled bool) error {
	value := 0
	if enabled {
		value = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO remote_access_control(id, enabled, updated_at) VALUES(1, ?, ?) ON CONFLICT(id) DO UPDATE SET enabled=excluded.enabled, updated_at=excluded.updated_at`, value, time.Now().Unix())
	return err
}

const schema = `
CREATE TABLE IF NOT EXISTS remote_access_control (id INTEGER PRIMARY KEY CHECK(id=1), enabled INTEGER NOT NULL DEFAULT 0 CHECK(enabled IN (0,1)), updated_at INTEGER NOT NULL DEFAULT 0);
INSERT OR IGNORE INTO remote_access_control(id,enabled,updated_at) VALUES(1,0,0);
CREATE TABLE IF NOT EXISTS local_bootstrap_key (id INTEGER PRIMARY KEY CHECK(id=1), key_hash BLOB NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS projects (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, plan_revision INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS node_identity (
  id INTEGER PRIMARY KEY CHECK(id=1), instance_id TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS project_replicas (
  project_id TEXT NOT NULL REFERENCES projects(id), replica_id TEXT NOT NULL,
  principal_id TEXT NOT NULL REFERENCES principals(id), certificate_fingerprint TEXT NOT NULL CHECK(length(certificate_fingerprint)=64),
  first_seen_at INTEGER NOT NULL, last_seen_at INTEGER NOT NULL,
  last_event_cursor INTEGER NOT NULL DEFAULT 0, snapshot_sequence INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(project_id,replica_id)
);
CREATE TABLE IF NOT EXISTS principals (
  id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), name TEXT NOT NULL,
  role TEXT NOT NULL CHECK(role IN ('owner','worker','reviewer')), token_hash BLOB NOT NULL UNIQUE,
  revoked INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS agent_sessions (
  instance_id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id),
  principal_id TEXT NOT NULL REFERENCES principals(id), started_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL, ended_at INTEGER NOT NULL DEFAULT 0,
  client_name TEXT NOT NULL DEFAULT '', client_version TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS agent_sessions_project_seen ON agent_sessions(project_id,last_seen_at);
CREATE TABLE IF NOT EXISTS agent_session_capabilities (
  instance_id TEXT NOT NULL REFERENCES agent_sessions(instance_id) ON DELETE CASCADE,
  capability TEXT NOT NULL, source TEXT NOT NULL CHECK(source IN ('adapter','host_reported')),
  PRIMARY KEY(instance_id,capability,source)
);
CREATE TABLE IF NOT EXISTS principal_permissions (
  project_id TEXT NOT NULL REFERENCES projects(id), principal_id TEXT NOT NULL REFERENCES principals(id),
  permission TEXT NOT NULL CHECK(permission IN ('project.read','message.send','task.claim','task.delegate','result.submit','review.decide','plan.edit','settings.edit','membership.manage')),
  granted_by TEXT NOT NULL REFERENCES principals(id), granted_at INTEGER NOT NULL,
  PRIMARY KEY(project_id,principal_id,permission)
);
CREATE TABLE IF NOT EXISTS certificate_grants (
  project_id TEXT NOT NULL REFERENCES projects(id), fingerprint TEXT NOT NULL CHECK(length(fingerprint)=64),
  granted_by TEXT NOT NULL REFERENCES principals(id), granted_at INTEGER NOT NULL,
  PRIMARY KEY(project_id,fingerprint)
);
CREATE TABLE IF NOT EXISTS certificate_grant_expirations (
  project_id TEXT NOT NULL, fingerprint TEXT NOT NULL, expires_at INTEGER NOT NULL,
  PRIMARY KEY(project_id,fingerprint),
  FOREIGN KEY(project_id,fingerprint) REFERENCES certificate_grants(project_id,fingerprint) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS certificate_invitations (
  id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id),
  token_hash BLOB NOT NULL, created_by TEXT NOT NULL REFERENCES principals(id), created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL, expected_fingerprint TEXT NOT NULL DEFAULT '',
  redeemed_fingerprint TEXT NOT NULL DEFAULT '', redeemed_at INTEGER NOT NULL DEFAULT 0,
  revoked_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS certificate_invitations_project ON certificate_invitations(project_id,created_at);
CREATE TABLE IF NOT EXISTS work_roles (project_id TEXT NOT NULL REFERENCES projects(id), principal_id TEXT NOT NULL REFERENCES principals(id), task_id TEXT NOT NULL DEFAULT '', revision INTEGER NOT NULL, name TEXT NOT NULL, duties TEXT NOT NULL, expires_at INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL, updated_by TEXT NOT NULL REFERENCES principals(id), PRIMARY KEY(project_id,principal_id,task_id));
CREATE TABLE IF NOT EXISTS project_settings (
  project_id TEXT PRIMARY KEY REFERENCES projects(id), revision INTEGER NOT NULL,
  max_parallel INTEGER NOT NULL, max_lease_seconds INTEGER NOT NULL,
  summary_token_target INTEGER NOT NULL, default_review_mode TEXT NOT NULL
);
INSERT OR IGNORE INTO project_settings(project_id,revision,max_parallel,max_lease_seconds,summary_token_target,default_review_mode)
  SELECT id,1,4,3600,1000,'report_first' FROM projects;
CREATE TABLE IF NOT EXISTS budget_envelopes (project_id TEXT PRIMARY KEY REFERENCES projects(id), revision INTEGER NOT NULL, token_limit INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS budget_reservations (task_id TEXT PRIMARY KEY REFERENCES tasks(id), amount INTEGER NOT NULL CHECK(amount>0), updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS budget_attempt_holds (task_id TEXT NOT NULL REFERENCES tasks(id), attempt INTEGER NOT NULL, amount INTEGER NOT NULL CHECK(amount>0), PRIMARY KEY(task_id,attempt));
CREATE TABLE IF NOT EXISTS plans (
  project_id TEXT NOT NULL REFERENCES projects(id), revision INTEGER NOT NULL, body TEXT NOT NULL,
  author TEXT NOT NULL REFERENCES principals(id), created_at INTEGER NOT NULL,
  PRIMARY KEY(project_id,revision)
);
CREATE TABLE IF NOT EXISTS plan_acknowledgments (
  project_id TEXT NOT NULL REFERENCES projects(id), principal_id TEXT NOT NULL REFERENCES principals(id),
  revision INTEGER NOT NULL, acknowledged_at INTEGER NOT NULL,
  PRIMARY KEY(project_id,principal_id),
  FOREIGN KEY(project_id,revision) REFERENCES plans(project_id,revision)
);
CREATE TABLE IF NOT EXISTS repositories (
  id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), name TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS repository_remote_hints (
  project_id TEXT NOT NULL REFERENCES projects(id), repository_id TEXT NOT NULL REFERENCES repositories(id),
  remote_key TEXT NOT NULL, added_by TEXT NOT NULL REFERENCES principals(id), created_at INTEGER NOT NULL,
  PRIMARY KEY(project_id,repository_id,remote_key)
);
CREATE INDEX IF NOT EXISTS repository_remote_key ON repository_remote_hints(project_id,remote_key,repository_id);
CREATE TABLE IF NOT EXISTS workspaces (
  id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id),
  repository_id TEXT NOT NULL REFERENCES repositories(id), owner TEXT NOT NULL REFERENCES principals(id),
  name TEXT NOT NULL, node_label TEXT NOT NULL, local_path TEXT NOT NULL,
  branch TEXT NOT NULL, head TEXT NOT NULL, dirty INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS tasks (
  id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), plan_revision INTEGER NOT NULL,
  title TEXT NOT NULL, description TEXT NOT NULL, kind TEXT NOT NULL,
  report_contract TEXT NOT NULL, status TEXT NOT NULL,
  owner TEXT REFERENCES principals(id), attempt INTEGER NOT NULL DEFAULT 0,
  lease_until INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL,
  FOREIGN KEY(project_id,plan_revision) REFERENCES plans(project_id,revision)
);
CREATE TABLE IF NOT EXISTS attempts (
  task_id TEXT NOT NULL REFERENCES tasks(id), number INTEGER NOT NULL,
  principal_id TEXT NOT NULL REFERENCES principals(id), workspace_id TEXT REFERENCES workspaces(id),
  workspace_head TEXT NOT NULL DEFAULT '', workspace_dirty INTEGER NOT NULL DEFAULT 0,
  started_at INTEGER NOT NULL, PRIMARY KEY(task_id,number)
);
CREATE TABLE IF NOT EXISTS dependencies (
  task_id TEXT NOT NULL REFERENCES tasks(id), prerequisite_id TEXT NOT NULL REFERENCES tasks(id),
  PRIMARY KEY(task_id,prerequisite_id), CHECK(task_id <> prerequisite_id)
);
CREATE TABLE IF NOT EXISTS task_assignments (
  task_id TEXT PRIMARY KEY REFERENCES tasks(id), principal_id TEXT NOT NULL REFERENCES principals(id),
  assigned_by TEXT NOT NULL REFERENCES principals(id), assigned_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS task_requests (
  project_id TEXT NOT NULL REFERENCES projects(id), principal_id TEXT NOT NULL REFERENCES principals(id),
  request_id TEXT NOT NULL, payload_digest BLOB NOT NULL, task_id TEXT NOT NULL REFERENCES tasks(id), created_at INTEGER NOT NULL,
  PRIMARY KEY(project_id,principal_id,request_id)
);
CREATE TABLE IF NOT EXISTS task_review_requests (
  task_id TEXT NOT NULL REFERENCES tasks(id), attempt INTEGER NOT NULL,
  principal_id TEXT NOT NULL REFERENCES principals(id), review_type TEXT NOT NULL,
  created_at INTEGER NOT NULL, PRIMARY KEY(task_id,attempt)
);
CREATE TABLE IF NOT EXISTS task_blockers (
  task_id TEXT PRIMARY KEY REFERENCES tasks(id), reason TEXT NOT NULL,
  reported_by TEXT NOT NULL REFERENCES principals(id), attempt INTEGER NOT NULL, created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS reports (
  id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id), attempt INTEGER NOT NULL,
  author TEXT NOT NULL REFERENCES principals(id), body TEXT NOT NULL, digest TEXT NOT NULL,
  baseline TEXT NOT NULL, created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS attempt_usage (
  task_id TEXT NOT NULL, attempt INTEGER NOT NULL, project_id TEXT NOT NULL REFERENCES projects(id),
  principal_id TEXT NOT NULL REFERENCES principals(id), request_id TEXT NOT NULL, payload_digest BLOB NOT NULL,
  provider TEXT NOT NULL, model TEXT NOT NULL, source TEXT NOT NULL,
  input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL, cost_micros INTEGER NOT NULL, currency TEXT NOT NULL DEFAULT '',
  recorded_at INTEGER NOT NULL,
  PRIMARY KEY(task_id,attempt),
  FOREIGN KEY(task_id,attempt) REFERENCES attempts(task_id,number)
);
CREATE INDEX IF NOT EXISTS attempt_usage_project ON attempt_usage(project_id,recorded_at);
CREATE TABLE IF NOT EXISTS report_requests (
  project_id TEXT NOT NULL REFERENCES projects(id), principal_id TEXT NOT NULL REFERENCES principals(id),
  request_id TEXT NOT NULL, payload_digest TEXT NOT NULL, report_id TEXT NOT NULL REFERENCES reports(id),
  created_at INTEGER NOT NULL, PRIMARY KEY(project_id,principal_id,request_id)
);
CREATE TABLE IF NOT EXISTS messages (
  seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE,
  project_id TEXT NOT NULL REFERENCES projects(id), sender TEXT NOT NULL REFERENCES principals(id),
  recipient TEXT REFERENCES principals(id), thread_id TEXT NOT NULL,
  subject TEXT NOT NULL, body TEXT NOT NULL, created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS messages_project_seq ON messages(project_id,seq);
CREATE TABLE IF NOT EXISTS message_requests (
  project_id TEXT NOT NULL REFERENCES projects(id), principal_id TEXT NOT NULL REFERENCES principals(id),
  request_id TEXT NOT NULL, payload_digest BLOB NOT NULL, message_id TEXT NOT NULL REFERENCES messages(id),
  PRIMARY KEY(project_id,principal_id,request_id)
);
CREATE TABLE IF NOT EXISTS message_acknowledgments (
  message_id TEXT NOT NULL REFERENCES messages(id),
  principal_id TEXT NOT NULL REFERENCES principals(id),
  acknowledged_at INTEGER NOT NULL,
  PRIMARY KEY(message_id,principal_id)
);
CREATE TABLE IF NOT EXISTS candidate_reports (
  id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id), attempt INTEGER NOT NULL,
  author TEXT NOT NULL REFERENCES principals(id), baseline TEXT NOT NULL, body TEXT NOT NULL,
  digest TEXT NOT NULL, created_at INTEGER NOT NULL,
  FOREIGN KEY(task_id,attempt) REFERENCES attempts(task_id,number)
);
CREATE INDEX IF NOT EXISTS candidate_reports_task ON candidate_reports(task_id,created_at,id);
CREATE TABLE IF NOT EXISTS candidate_requests (project_id TEXT NOT NULL REFERENCES projects(id), principal_id TEXT NOT NULL REFERENCES principals(id), request_id TEXT NOT NULL, payload_digest BLOB NOT NULL, candidate_id TEXT NOT NULL REFERENCES candidate_reports(id), PRIMARY KEY(project_id,principal_id,request_id));
CREATE TABLE IF NOT EXISTS review_request_candidates (
  task_id TEXT NOT NULL, attempt INTEGER NOT NULL, candidate_id TEXT NOT NULL REFERENCES candidate_reports(id),
  digest TEXT NOT NULL, baseline TEXT NOT NULL, PRIMARY KEY(task_id,attempt),
  FOREIGN KEY(task_id,attempt) REFERENCES task_review_requests(task_id,attempt)
);
CREATE TABLE IF NOT EXISTS report_review_evidence (
  report_id TEXT PRIMARY KEY REFERENCES reports(id), candidate_id TEXT NOT NULL REFERENCES candidate_reports(id),
  digest TEXT NOT NULL, baseline TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS integration_records (
  id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), task_id TEXT NOT NULL,
  attempt INTEGER NOT NULL, author TEXT NOT NULL REFERENCES principals(id), repository_id TEXT NOT NULL REFERENCES repositories(id),
  workspace_id TEXT NOT NULL REFERENCES workspaces(id), base_revision TEXT NOT NULL, combined_revision TEXT NOT NULL,
  candidate_ids TEXT NOT NULL, checks_json TEXT NOT NULL, summary TEXT NOT NULL, digest TEXT NOT NULL, created_at INTEGER NOT NULL,
  FOREIGN KEY(task_id,attempt) REFERENCES attempts(task_id,number), UNIQUE(task_id,attempt)
);
CREATE TABLE IF NOT EXISTS integration_requests (
  project_id TEXT NOT NULL REFERENCES projects(id), principal_id TEXT NOT NULL REFERENCES principals(id),
  request_id TEXT NOT NULL, payload_digest BLOB NOT NULL, integration_id TEXT NOT NULL REFERENCES integration_records(id),
  PRIMARY KEY(project_id,principal_id,request_id)
);
CREATE TABLE IF NOT EXISTS events (
  seq INTEGER PRIMARY KEY AUTOINCREMENT, project_id TEXT NOT NULL REFERENCES projects(id),
  actor TEXT NOT NULL REFERENCES principals(id), kind TEXT NOT NULL, entity_id TEXT NOT NULL,
  payload TEXT NOT NULL, created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS events_project_seq ON events(project_id,seq);
CREATE TABLE IF NOT EXISTS event_checkpoints (
  project_id TEXT NOT NULL REFERENCES projects(id), principal_id TEXT NOT NULL REFERENCES principals(id),
  sequence INTEGER NOT NULL, updated_at INTEGER NOT NULL, PRIMARY KEY(project_id,principal_id)
);
CREATE TABLE IF NOT EXISTS event_retention (
  project_id TEXT PRIMARY KEY REFERENCES projects(id), pruned_through INTEGER NOT NULL,
  updated_at INTEGER NOT NULL, pruned_by TEXT NOT NULL REFERENCES principals(id)
);
CREATE TABLE IF NOT EXISTS legacy_imports (
  id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), source_hash TEXT NOT NULL,
  source_name TEXT NOT NULL, imported_by TEXT NOT NULL REFERENCES principals(id), imported_at INTEGER NOT NULL,
  source_manifest TEXT NOT NULL, UNIQUE(project_id,source_hash)
);
CREATE TABLE IF NOT EXISTS legacy_records (
  import_id TEXT NOT NULL REFERENCES legacy_imports(id), project_id TEXT NOT NULL REFERENCES projects(id),
  record_type TEXT NOT NULL, record_id TEXT NOT NULL, created_at INTEGER NOT NULL DEFAULT 0,
  digest TEXT NOT NULL, payload TEXT NOT NULL,
  PRIMARY KEY(import_id,record_type,record_id)
);
CREATE INDEX IF NOT EXISTS legacy_records_project_type ON legacy_records(project_id,record_type,record_id);
CREATE INDEX IF NOT EXISTS tasks_project_status ON tasks(project_id,status);
`

type identity struct{ ID, Role string }
type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type Credential struct {
	PrincipalID string `json:"principal_id"`
	Token       string `json:"token"`
}
type ReportContract struct {
	Formats              []string `json:"formats"`
	Questions            []string `json:"questions"`
	Coverage             []string `json:"coverage"`
	RequiredRole         string   `json:"required_role,omitempty"`
	RequiredCapabilities []string `json:"required_capabilities,omitempty"`
	ReviewType           string   `json:"review_type"`
	ReviewMode           string   `json:"review_mode"`
	SummaryTokenTarget   int      `json:"summary_token_target"`
}
type Task struct {
	ID            string         `json:"id"`
	ProjectID     string         `json:"project_id"`
	PlanRevision  int            `json:"plan_revision"`
	Title         string         `json:"title"`
	Description   string         `json:"description"`
	Kind          string         `json:"kind"`
	Contract      ReportContract `json:"report_contract"`
	Status        string         `json:"status"`
	Owner         string         `json:"owner,omitempty"`
	Attempt       int            `json:"attempt"`
	LeaseUntil    int64          `json:"lease_until"`
	AssignedTo    string         `json:"assigned_to,omitempty"`
	BlockerReason string         `json:"blocker_reason,omitempty"`
}
type Report struct {
	ID                    string `json:"id"`
	TaskID                string `json:"task_id"`
	Attempt               int    `json:"attempt"`
	Author                string `json:"author"`
	Body                  string `json:"body"`
	Digest                string `json:"digest"`
	Baseline              string `json:"baseline"`
	SourceCandidateID     string `json:"source_candidate_id,omitempty"`
	SourceCandidateDigest string `json:"source_candidate_digest,omitempty"`
	SourceCandidateBase   string `json:"source_candidate_baseline,omitempty"`
}
type UsageReport struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	Source       string `json:"source"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	CostMicros   int64  `json:"cost_micros"`
	Currency     string `json:"currency,omitempty"`
	TaskID       string `json:"task_id,omitempty"`
	Attempt      int    `json:"attempt,omitempty"`
	PrincipalID  string `json:"principal_id,omitempty"`
	RequestID    string `json:"request_id,omitempty"`
	RecordedAt   int64  `json:"recorded_at,omitempty"`
}
type Event struct {
	Sequence  int64           `json:"sequence"`
	Actor     string          `json:"actor"`
	Kind      string          `json:"kind"`
	EntityID  string          `json:"entity_id"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt int64           `json:"created_at"`
}

func randomID(prefix string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b), nil
}
func newToken() (string, []byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token := hex.EncodeToString(b)
	h := sha256.Sum256([]byte(token))
	return token, h[:], nil
}
func authenticate(ctx context.Context, tx *sql.Tx, projectID, token string, roles ...string) (identity, error) {
	var id identity
	h := sha256.Sum256([]byte(token))
	err := tx.QueryRowContext(ctx, `SELECT id, role FROM principals WHERE project_id=? AND token_hash=? AND revoked=0`, projectID, h[:]).Scan(&id.ID, &id.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return id, ErrUnauthorized
	}
	if err != nil {
		return id, err
	}
	for _, role := range roles {
		if id.Role == role {
			return id, nil
		}
	}
	return identity{}, ErrUnauthorized
}
func event(ctx context.Context, tx *sql.Tx, project, actor, kind, entity string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO events(project_id,actor,kind,entity_id,payload,created_at) VALUES(?,?,?,?,?,?)`,
		project, actor, kind, entity, string(data), time.Now().UnixMilli())
	return err
}

// CreateProject is a local administrative bootstrap operation. The returned
// token is shown only once; callers must protect it as a secret.
func (s *Store) CreateProject(ctx context.Context, name, ownerName string) (Project, Credential, error) {
	var p Project
	var c Credential
	if strings.TrimSpace(name) == "" || strings.TrimSpace(ownerName) == "" {
		return p, c, errors.New("project and owner names are required")
	}
	var err error
	if p.ID, err = randomID("proj_"); err != nil {
		return p, c, err
	}
	p.Name = name
	if c.PrincipalID, err = randomID("principal_"); err != nil {
		return p, c, err
	}
	var hash []byte
	if c.Token, hash, err = newToken(); err != nil {
		return p, Credential{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, Credential{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO projects(id,name) VALUES(?,?)`, p.ID, p.Name); err != nil {
		return Project{}, Credential{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO project_settings(project_id,revision,max_parallel,max_lease_seconds,summary_token_target,default_review_mode) VALUES(?,1,4,3600,1000,'report_first')`, p.ID); err != nil {
		return Project{}, Credential{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO principals(id,project_id,name,role,token_hash) VALUES(?,?,?,?,?)`, c.PrincipalID, p.ID, ownerName, "owner", hash); err != nil {
		return Project{}, Credential{}, err
	}
	if err = event(ctx, tx, p.ID, c.PrincipalID, "project.created", p.ID, map[string]string{"name": name}); err != nil {
		return Project{}, Credential{}, err
	}
	if err = tx.Commit(); err != nil {
		return Project{}, Credential{}, err
	}
	return p, c, nil
}

// CreateProjectWithWorkspace bootstraps a project from a local Git checkout in
// one transaction so setup cannot leave behind an empty project on failure.
func (s *Store) CreateProjectWithWorkspace(ctx context.Context, name, ownerName, repositoryName, node string, observation GitObservation) (Project, Credential, Repository, Workspace, error) {
	projectID, credential, err := PrepareCredential("")
	if err != nil {
		return Project{}, Credential{}, Repository{}, Workspace{}, err
	}
	return s.CreatePreparedProjectWithWorkspace(ctx, projectID, name, ownerName, repositoryName, node, observation, credential)
}

// ConfigureLocalBootstrapKey stores only a one-way digest. This operation is
// intentionally available through local database administration, never RPC.
func (s *Store) ConfigureLocalBootstrapKey(ctx context.Context, key string) error {
	key = strings.TrimSpace(key)
	if len(key) < 32 {
		return errors.New("bootstrap API key must contain at least 32 characters")
	}
	digest := sha256.Sum256([]byte(key))
	_, err := s.db.ExecContext(ctx, `INSERT INTO local_bootstrap_key(id,key_hash,updated_at) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET key_hash=excluded.key_hash,updated_at=excluded.updated_at`, digest[:], time.Now().UnixMilli())
	return err
}

func (s *Store) localBootstrapKeyAuthorized(ctx context.Context, key string) (bool, error) {
	if len(strings.TrimSpace(key)) < 32 {
		return false, nil
	}
	var expected []byte
	err := s.db.QueryRowContext(ctx, `SELECT key_hash FROM local_bootstrap_key WHERE id=1`).Scan(&expected)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	provided := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return len(expected) == len(provided) && subtle.ConstantTimeCompare(expected, provided[:]) == 1, nil
}

// CreatePreparedProjectWithWorkspaceUsingBootstrapKey is the local create-only
// bootstrap path. A configured key is required before it can create a project.
func (s *Store) CreatePreparedProjectWithWorkspaceUsingBootstrapKey(ctx context.Context, key, projectID, name, ownerName, repositoryName, node string, observation GitObservation, credential Credential) (Project, Credential, Repository, Workspace, error) {
	authorized, err := s.localBootstrapKeyAuthorized(ctx, key)
	if err != nil {
		return Project{}, Credential{}, Repository{}, Workspace{}, err
	}
	if !authorized {
		return Project{}, Credential{}, Repository{}, Workspace{}, ErrUnauthorized
	}
	return s.CreatePreparedProjectWithWorkspace(ctx, projectID, name, ownerName, repositoryName, node, observation, credential)
}

// CreatePreparedProjectWithWorkspace is retry-safe when a caller durably saves
// the prepared credential before sending the create request.
func (s *Store) CreatePreparedProjectWithWorkspace(ctx context.Context, projectID, name, ownerName, repositoryName, node string, observation GitObservation, c Credential) (Project, Credential, Repository, Workspace, error) {
	return s.CreatePreparedProjectWithWorkspaceAndCertificate(ctx, projectID, name, ownerName, repositoryName, node, observation, c, "")
}

// CreatePreparedProjectWithWorkspaceAndCertificate atomically creates the
// owner, initial checkout and optional verified bootstrap-client grant.
func (s *Store) CreatePreparedProjectWithWorkspaceAndCertificate(ctx context.Context, projectID, name, ownerName, repositoryName, node string, observation GitObservation, c Credential, fingerprint string) (Project, Credential, Repository, Workspace, error) {
	var p Project
	var repo Repository
	var workspace Workspace
	if !validPrepared(projectID, c) || strings.TrimSpace(name) == "" || strings.TrimSpace(ownerName) == "" || strings.TrimSpace(repositoryName) == "" || len(name) > 512 || len(ownerName) > 512 || len(repositoryName) > 256 || observation.Root == "" || len(observation.Root) > 4096 {
		return p, c, repo, workspace, errors.New("project, owner, repository and Git checkout are required")
	}
	if fingerprint != "" {
		var err error
		fingerprint, err = NormalizeCertificateFingerprint(fingerprint)
		if err != nil {
			return p, Credential{}, repo, workspace, err
		}
	}
	var err error
	p.ID = projectID
	p.Name = name
	tokenHash := sha256.Sum256([]byte(c.Token))
	if repo.ID, err = randomID("repo_"); err != nil {
		return p, Credential{}, repo, workspace, err
	}
	repo.Name = repositoryName
	if workspace.ID, err = randomID("workspace_"); err != nil {
		return p, Credential{}, repo, workspace, err
	}
	now := time.Now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return p, Credential{}, repo, workspace, err
	}
	defer tx.Rollback()
	var existingName, existingOwner, existingRole string
	var existingHash []byte
	var revoked int
	err = tx.QueryRowContext(ctx, `SELECT p.name,a.name,a.role,a.token_hash,a.revoked FROM projects p JOIN principals a ON a.project_id=p.id WHERE p.id=? AND a.id=?`, p.ID, c.PrincipalID).Scan(&existingName, &existingOwner, &existingRole, &existingHash, &revoked)
	if err == nil {
		if existingName != name || existingOwner != ownerName || existingRole != "owner" || revoked != 0 || !strings.EqualFold(hex.EncodeToString(existingHash), hex.EncodeToString(tokenHash[:])) {
			return Project{}, Credential{}, Repository{}, Workspace{}, ErrConflict
		}
		err = tx.QueryRowContext(ctx, `SELECT r.id,r.name,w.id,w.name,w.node_label,w.local_path,w.branch,w.head,w.dirty,w.updated_at FROM repositories r JOIN workspaces w ON w.repository_id=r.id AND w.project_id=r.project_id WHERE r.project_id=? AND w.owner=? AND w.local_path=? ORDER BY w.updated_at LIMIT 2`, p.ID, c.PrincipalID, observation.Root).Scan(&repo.ID, &repo.Name, &workspace.ID, &workspace.Name, &workspace.NodeLabel, &workspace.LocalPath, &workspace.Branch, &workspace.Head, &workspace.Dirty, &workspace.UpdatedAt)
		if err != nil || repo.Name != repositoryName || workspace.Name != repositoryName || workspace.NodeLabel != node {
			return Project{}, Credential{}, Repository{}, Workspace{}, ErrConflict
		}
		workspace.RepositoryID, workspace.Owner = repo.ID, c.PrincipalID
		rows, queryErr := tx.QueryContext(ctx, `SELECT remote_key FROM repository_remote_hints WHERE project_id=? AND repository_id=?`, p.ID, repo.ID)
		if queryErr != nil {
			return Project{}, Credential{}, Repository{}, Workspace{}, queryErr
		}
		for rows.Next() {
			var hint string
			if queryErr = rows.Scan(&hint); queryErr != nil {
				rows.Close()
				return Project{}, Credential{}, Repository{}, Workspace{}, queryErr
			}
			repo.RemoteHints = append(repo.RemoteHints, hint)
		}
		if queryErr = rows.Err(); queryErr != nil {
			rows.Close()
			return Project{}, Credential{}, Repository{}, Workspace{}, queryErr
		}
		rows.Close()
		want := make(map[string]bool, len(observation.RemoteHints))
		for _, hint := range observation.RemoteHints {
			want[hint] = true
		}
		if len(want) != len(repo.RemoteHints) {
			return Project{}, Credential{}, Repository{}, Workspace{}, ErrConflict
		}
		for _, hint := range repo.RemoteHints {
			if !want[hint] {
				return Project{}, Credential{}, Repository{}, Workspace{}, ErrConflict
			}
		}
		if fingerprint != "" {
			var granted int
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM certificate_grants WHERE project_id=? AND fingerprint=? AND granted_by=?`, p.ID, fingerprint, c.PrincipalID).Scan(&granted); err != nil {
				return Project{}, Credential{}, Repository{}, Workspace{}, err
			}
			if granted != 1 {
				return Project{}, Credential{}, Repository{}, Workspace{}, ErrConflict
			}
		}
		return p, c, repo, workspace, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Project{}, Credential{}, Repository{}, Workspace{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO projects(id,name) VALUES(?,?)`, p.ID, p.Name); err != nil {
		return p, Credential{}, repo, workspace, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO project_settings(project_id,revision,max_parallel,max_lease_seconds,summary_token_target,default_review_mode) VALUES(?,1,4,3600,1000,'report_first')`, p.ID); err != nil {
		return p, Credential{}, repo, workspace, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO principals(id,project_id,name,role,token_hash) VALUES(?,?,?,?,?)`, c.PrincipalID, p.ID, ownerName, "owner", tokenHash[:]); err != nil {
		return p, Credential{}, repo, workspace, err
	}
	if fingerprint != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO certificate_grants(project_id,fingerprint,granted_by,granted_at) VALUES(?,?,?,?)`, p.ID, fingerprint, c.PrincipalID, now); err != nil {
			return p, Credential{}, repo, workspace, err
		}
		if err = event(ctx, tx, p.ID, c.PrincipalID, "certificate.granted", fingerprint, map[string]any{"fingerprint": fingerprint, "expires_at": int64(0), "bootstrap": true}); err != nil {
			return p, Credential{}, repo, workspace, err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO repositories(id,project_id,name,created_at) VALUES(?,?,?,?)`, repo.ID, p.ID, repo.Name, now); err != nil {
		return p, Credential{}, repo, workspace, err
	}
	for _, hint := range observation.RemoteHints {
		if hint == "" || len(hint) > 2048 {
			return p, Credential{}, repo, workspace, errors.New("invalid inferred repository remote")
		}
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO repository_remote_hints(project_id,repository_id,remote_key,added_by,created_at) VALUES(?,?,?,?,?)`, p.ID, repo.ID, hint, c.PrincipalID, now); err != nil {
			return p, Credential{}, repo, workspace, err
		}
		repo.RemoteHints = append(repo.RemoteHints, hint)
	}
	workspace = Workspace{ID: workspace.ID, RepositoryID: repo.ID, Owner: c.PrincipalID, Name: repositoryName, NodeLabel: node, LocalPath: observation.Root, Branch: observation.Branch, Head: observation.Head, Dirty: observation.Dirty, UpdatedAt: now}
	if _, err = tx.ExecContext(ctx, `INSERT INTO workspaces(id,project_id,repository_id,owner,name,node_label,local_path,branch,head,dirty,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, workspace.ID, p.ID, repo.ID, c.PrincipalID, workspace.Name, node, workspace.LocalPath, workspace.Branch, workspace.Head, workspace.Dirty, now); err != nil {
		return p, Credential{}, repo, workspace, err
	}
	if err = event(ctx, tx, p.ID, c.PrincipalID, "project.created", p.ID, map[string]string{"name": name}); err != nil {
		return p, Credential{}, repo, workspace, err
	}
	if err = event(ctx, tx, p.ID, c.PrincipalID, "repository.created", repo.ID, map[string]string{"name": repo.Name}); err != nil {
		return p, Credential{}, repo, workspace, err
	}
	if err = event(ctx, tx, p.ID, c.PrincipalID, "workspace.registered", workspace.ID, map[string]string{"repository_id": repo.ID, "name": workspace.Name}); err != nil {
		return p, Credential{}, repo, workspace, err
	}
	if err = tx.Commit(); err != nil {
		return p, Credential{}, repo, workspace, err
	}
	return p, c, repo, workspace, nil
}

// AddPrincipal grants a credential for one project; only owners may enroll.
func (s *Store) AddPrincipal(ctx context.Context, projectID, token, name, role string) (Credential, error) {
	var c Credential
	if strings.TrimSpace(name) == "" || (role != "worker" && role != "reviewer" && role != "owner") {
		return c, errors.New("valid name and role required")
	}
	var err error
	if c.PrincipalID, err = randomID("principal_"); err != nil {
		return c, err
	}
	var hash []byte
	if c.Token, hash, err = newToken(); err != nil {
		return Credential{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Credential{}, err
	}
	defer tx.Rollback()
	actor, err := authorizePermission(ctx, tx, projectID, token, PermissionMembershipManage)
	if err != nil {
		return Credential{}, err
	}
	if role == "owner" && actor.Role != "owner" {
		return Credential{}, ErrUnauthorized
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO principals(id,project_id,name,role,token_hash) VALUES(?,?,?,?,?)`, c.PrincipalID, projectID, name, role, hash); err != nil {
		return Credential{}, err
	}
	if err = grantInitialPermissions(ctx, tx, projectID, actor.ID, c.PrincipalID, role); err != nil {
		return Credential{}, err
	}
	if err = event(ctx, tx, projectID, actor.ID, "principal.enrolled", c.PrincipalID, map[string]string{"name": name, "role": role}); err != nil {
		return Credential{}, err
	}
	if err = tx.Commit(); err != nil {
		return Credential{}, err
	}
	return c, nil
}

// PublishPlan atomically appends a plan revision using optimistic concurrency.
func (s *Store) PublishPlan(ctx context.Context, projectID, token, body string, expected int) (int, error) {
	if strings.TrimSpace(body) == "" || len(body) > 256*1024 {
		return 0, errors.New("plan body is empty or too large")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	actor, err := authorizePermission(ctx, tx, projectID, token, PermissionPlanEdit)
	if err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE projects SET plan_revision=plan_revision+1 WHERE id=? AND plan_revision=?`, projectID, expected)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n != 1 {
		return 0, ErrConflict
	}
	rev := expected + 1
	if _, err = tx.ExecContext(ctx, `INSERT INTO plans(project_id,revision,body,author,created_at) VALUES(?,?,?,?,?)`, projectID, rev, body, actor.ID, time.Now().UnixMilli()); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO plan_acknowledgments(project_id,principal_id,revision,acknowledged_at) VALUES(?,?,?,?) ON CONFLICT(project_id,principal_id) DO UPDATE SET revision=excluded.revision,acknowledged_at=excluded.acknowledged_at`, projectID, actor.ID, rev, time.Now().UnixMilli()); err != nil {
		return 0, err
	}
	// Existing work requires explicit reconciliation against the changed plan.
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='needs_replan', owner=NULL, lease_until=0 WHERE project_id=? AND status IN ('ready','blocked','running','submitted','accepted')`, projectID); err != nil {
		return 0, err
	}
	if err = event(ctx, tx, projectID, actor.ID, "plan.published", projectID, map[string]int{"revision": rev}); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return rev, nil
}
