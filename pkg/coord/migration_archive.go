package coord

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	legacyMessages "github.com/tasmirz/gossip/pkg/messages"
	legacySOP "github.com/tasmirz/gossip/pkg/sop"
	legacyState "github.com/tasmirz/gossip/pkg/state"
	legacyTasks "github.com/tasmirz/gossip/pkg/tasks"
)

type LegacyImport struct {
	ID         string         `json:"id"`
	ProjectID  string         `json:"project_id"`
	SourceHash string         `json:"source_hash"`
	SourceName string         `json:"source_name"`
	ImportedBy string         `json:"imported_by"`
	ImportedAt int64          `json:"imported_at"`
	Counts     map[string]int `json:"counts"`
	Existing   bool           `json:"existing,omitempty"`
}

type LegacyRecord struct {
	ImportID     string `json:"import_id"`
	Type         string `json:"type"`
	ID           string `json:"id"`
	CreatedAt    int64  `json:"created_at,omitempty"`
	Digest       string `json:"digest"`
	PayloadBytes int    `json:"payload_bytes"`
}

type LegacyRecordChunk struct {
	ImportID   string `json:"import_id"`
	Type       string `json:"type"`
	ID         string `json:"id"`
	Digest     string `json:"digest"`
	Offset     int    `json:"offset"`
	NextOffset int    `json:"next_offset"`
	Done       bool   `json:"done"`
	Content    string `json:"content"`
}

const maxLegacyRecordChunkChars = 262144

type legacyArchiveManifest struct {
	Files  []MigrationFile  `json:"files"`
	Counts map[string]int   `json:"counts"`
	Issues []MigrationIssue `json:"issues"`
}

type legacyActorRecord struct {
	MapID          string    `json:"map_id"`
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	HistoricalRole string    `json:"historical_role,omitempty"`
	Capabilities   []string  `json:"capabilities,omitempty"`
	CurrentTaskID  string    `json:"current_task_id,omitempty"`
	Status         string    `json:"status,omitempty"`
	RegisteredAt   time.Time `json:"registered_at,omitempty"`
	LastHeartbeat  time.Time `json:"last_heartbeat,omitempty"`
	SessionCount   int       `json:"session_count,omitempty"`
	IsRemote       bool      `json:"is_remote,omitempty"`
	EndpointType   string    `json:"endpoint_type,omitempty"`
	IdentityStatus string    `json:"identity_status"`
}

type legacyArtifactRecord struct {
	Path        string `json:"path"`
	SHA256      string `json:"sha256"`
	ContentType string `json:"content_type"`
	Text        string `json:"text,omitempty"`
	Base64      string `json:"base64,omitempty"`
}

// ImportLegacyArchive preserves legacy records as project-scoped history. It
// does not turn historical actors, role labels, assignments, or task states
// into active principals, permissions, claims, or accepted reports.
func (s *Store) ImportLegacyArchive(ctx context.Context, projectID, ownerToken, source string, acceptWarnings bool) (LegacyImport, MigrationAudit, error) {
	var result LegacyImport
	principal, err := s.WhoAmI(ctx, projectID, ownerToken)
	if err != nil {
		return result, MigrationAudit{}, err
	}
	if principal.Role != "owner" {
		return result, MigrationAudit{}, ErrUnauthorized
	}
	absolute, err := filepath.Abs(source)
	if err != nil {
		return result, MigrationAudit{}, err
	}
	rootInfo, err := os.Lstat(absolute)
	if err != nil {
		return result, MigrationAudit{}, err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return result, MigrationAudit{}, errors.New("legacy source must be a real directory, not a symlink")
	}
	store, err := legacyState.NewStore(absolute)
	if err != nil {
		return result, MigrationAudit{}, err
	}
	unlock, err := store.Lock()
	if err != nil {
		return result, MigrationAudit{}, err
	}
	defer unlock()

	audit, err := AuditLegacyStore(source)
	if err != nil {
		return result, audit, err
	}
	if !audit.Ready {
		return result, audit, errors.New("legacy audit has structural errors; resolve them before import")
	}
	if countMigrationWarnings(audit.Issues) > 0 && !acceptWarnings {
		return result, audit, errors.New("legacy audit has warnings; review them and explicitly accept with --accept-warnings")
	}
	files := make(map[string]MigrationFile, len(audit.Files))
	for _, file := range audit.Files {
		files[file.Name] = file
	}
	manifest := legacyArchiveManifest{Files: audit.Files, Counts: audit.Counts, Issues: audit.Issues}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return result, audit, err
	}
	sourceHash := migrationSourceHash(audit.Files)
	importID, err := randomID("legacy_")
	if err != nil {
		return result, audit, err
	}
	now := time.Now().UnixNano()
	sourceName := filepath.Base(absolute)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, audit, err
	}
	defer tx.Rollback()
	actor, err := authenticate(ctx, tx, projectID, ownerToken, "owner")
	if err != nil {
		return result, audit, err
	}
	var existingID, existingName, existingBy string
	var existingAt int64
	var existingManifest string
	err = tx.QueryRowContext(ctx, `SELECT id,source_name,imported_by,imported_at,source_manifest FROM legacy_imports WHERE project_id=? AND source_hash=?`, projectID, sourceHash).Scan(&existingID, &existingName, &existingBy, &existingAt, &existingManifest)
	if err == nil {
		result, err = legacyImportFromRow(projectID, existingID, sourceHash, existingName, existingBy, existingAt, existingManifest)
		if err != nil {
			return LegacyImport{}, audit, err
		}
		result.Existing = true
		if err = tx.Commit(); err != nil {
			return LegacyImport{}, audit, err
		}
		return result, audit, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, audit, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO legacy_imports(id,project_id,source_hash,source_name,imported_by,imported_at,source_manifest) VALUES(?,?,?,?,?,?,?)`, importID, projectID, sourceHash, sourceName, actor.ID, now, string(manifestJSON)); err != nil {
		return result, audit, err
	}
	insert := func(recordType, recordID string, createdAt int64, value any) error {
		payload, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return marshalErr
		}
		digest := sha256.Sum256(payload)
		_, execErr := tx.ExecContext(ctx, `INSERT INTO legacy_records(import_id,project_id,record_type,record_id,created_at,digest,payload) VALUES(?,?,?,?,?,?,?)`, importID, projectID, recordType, recordID, createdAt, hex.EncodeToString(digest[:]), string(payload))
		return execErr
	}

	if file, ok := files["state.json"]; ok {
		data, readErr := readVerifiedMigrationFile(source, file, maxLegacyJSONBytes)
		if readErr != nil {
			return result, audit, readErr
		}
		var legacy legacyState.ProjectState
		if err = json.Unmarshal(data, &legacy); err != nil {
			return result, audit, fmt.Errorf("state.json changed or became invalid after audit: %w", err)
		}
		for _, goal := range legacy.Goals {
			if err = insert("goal", goal.ID, migrationTime(goal.CreatedAt), goal); err != nil {
				return result, audit, err
			}
		}
		for id, agent := range legacy.Agents {
			entry := legacyActorRecord{MapID: id, ID: agent.ID, Name: agent.Name, HistoricalRole: agent.Role, Capabilities: agent.Capabilities, CurrentTaskID: agent.CurrentTaskID, Status: agent.Status, RegisteredAt: agent.RegisteredAt, LastHeartbeat: agent.LastHeartbeat, SessionCount: agent.SessionCount, IsRemote: agent.IsRemote, EndpointType: agent.EndpointType, IdentityStatus: "unverified_historical_actor"}
			if err = insert("actor", id, migrationTime(agent.RegisteredAt), entry); err != nil {
				return result, audit, err
			}
		}
	}
	if file, ok := files["tasks.json"]; ok {
		data, readErr := readVerifiedMigrationFile(source, file, maxLegacyJSONBytes)
		if readErr != nil {
			return result, audit, readErr
		}
		var legacy legacyTasks.TasksContainer
		if err = json.Unmarshal(data, &legacy); err != nil {
			return result, audit, fmt.Errorf("tasks.json changed or became invalid after audit: %w", err)
		}
		for id, task := range legacy.Tasks {
			if err = insert("task", id, migrationTime(task.CreatedAt), task); err != nil {
				return result, audit, err
			}
		}
	}
	if file, ok := files["sop.json"]; ok {
		data, readErr := readVerifiedMigrationFile(source, file, maxLegacyJSONBytes)
		if readErr != nil {
			return result, audit, readErr
		}
		var legacy legacySOP.SOPStore
		if err = json.Unmarshal(data, &legacy); err != nil {
			return result, audit, fmt.Errorf("sop.json changed or became invalid after audit: %w", err)
		}
		for id, workflow := range legacy.Workflows {
			if err = insert("workflow", id, migrationTime(workflow.CreatedAt), workflow); err != nil {
				return result, audit, err
			}
			for artifactID, artifact := range workflow.Artifacts {
				if err = insert("sop_artifact", id+"/"+artifactID, migrationTime(artifact.CreatedAt), artifact); err != nil {
					return result, audit, err
				}
			}
		}
	}
	if file, ok := files["messages.jsonl"]; ok {
		if err = importLegacyMessages(ctx, source, file, insert); err != nil {
			return result, audit, err
		}
	}
	for name, file := range files {
		if !strings.HasPrefix(name, "artifacts/") {
			continue
		}
		data, readErr := readVerifiedMigrationFile(source, file, maxLegacyArtifactBytes)
		if readErr != nil {
			return result, audit, readErr
		}
		sum := sha256.Sum256(data)
		payload := legacyArtifactRecord{Path: name, SHA256: hex.EncodeToString(sum[:]), ContentType: "text/markdown"}
		if utf8.Valid(data) {
			payload.Text = string(data)
		} else {
			payload.ContentType = "application/octet-stream"
			payload.Base64 = base64.StdEncoding.EncodeToString(data)
		}
		if err = insert("artifact_file", name, 0, payload); err != nil {
			return result, audit, err
		}
	}
	if err = event(ctx, tx, projectID, actor.ID, "legacy.imported", importID, map[string]any{"source_hash": sourceHash, "counts": audit.Counts, "warnings": countMigrationWarnings(audit.Issues)}); err != nil {
		return result, audit, err
	}
	if err = tx.Commit(); err != nil {
		return result, audit, err
	}
	return LegacyImport{ID: importID, ProjectID: projectID, SourceHash: sourceHash, SourceName: sourceName, ImportedBy: actor.ID, ImportedAt: now, Counts: audit.Counts}, audit, nil
}

func migrationSourceHash(files []MigrationFile) string {
	h := sha256.New()
	for _, file := range files {
		_, _ = io.WriteString(h, file.Name)
		_, _ = h.Write([]byte{0})
		_, _ = io.WriteString(h, file.SHA256)
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func migrationTime(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixNano()
}

func readVerifiedMigrationFile(root string, expected MigrationFile, limit int64) ([]byte, error) {
	path := filepath.Join(root, filepath.FromSlash(expected.Name))
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > limit {
		return nil, fmt.Errorf("migration source %q changed type or exceeds the accepted size", expected.Name)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(f, limit+1))
	closeErr := f.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if int64(len(data)) != expected.Bytes {
		return nil, fmt.Errorf("migration source %q changed size after audit", expected.Name)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != expected.SHA256 {
		return nil, fmt.Errorf("migration source %q changed after audit", expected.Name)
	}
	return data, nil
}
func importLegacyMessages(ctx context.Context, source string, expected MigrationFile, insert func(string, string, int64, any) error) error {
	path := filepath.Join(source, filepath.FromSlash(expected.Name))
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxLegacyMessageBytes {
		return errors.New("messages.jsonl changed type or exceeds the accepted size")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	scanner := bufio.NewScanner(io.TeeReader(f, h))
	scanner.Buffer(make([]byte, 64*1024), maxLegacyMessageLine)
	for line := 1; scanner.Scan(); line++ {
		if err = ctx.Err(); err != nil {
			return err
		}
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var message legacyMessages.Message
		if err = json.Unmarshal(raw, &message); err != nil {
			return fmt.Errorf("messages.jsonl changed at line %d: %w", line, err)
		}
		if err = insert("message", message.ID, migrationTime(message.CreatedAt), message); err != nil {
			return err
		}
	}
	if err = scanner.Err(); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != expected.SHA256 {
		return errors.New("messages.jsonl changed after audit")
	}
	return nil
}

func countMigrationWarnings(issues []MigrationIssue) int {
	count := 0
	for _, issue := range issues {
		if issue.Severity == "warning" {
			count++
		}
	}
	return count
}

func legacyImportFromRow(projectID, id, sourceHash, sourceName, importedBy string, importedAt int64, manifestJSON string) (LegacyImport, error) {
	var manifest legacyArchiveManifest
	if err := json.Unmarshal([]byte(manifestJSON), &manifest); err != nil {
		return LegacyImport{}, err
	}
	return LegacyImport{ID: id, ProjectID: projectID, SourceHash: sourceHash, SourceName: sourceName, ImportedBy: importedBy, ImportedAt: importedAt, Counts: manifest.Counts}, nil
}

func (s *Store) ListLegacyImports(ctx context.Context, projectID, token string) ([]LegacyImport, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = authorizePermission(ctx, tx, projectID, token, PermissionProjectRead); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,source_hash,source_name,imported_by,imported_at,source_manifest FROM legacy_imports WHERE project_id=? ORDER BY imported_at,id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	imports := []LegacyImport{}
	for rows.Next() {
		var item LegacyImport
		var manifest string
		item.ProjectID = projectID
		if err = rows.Scan(&item.ID, &item.SourceHash, &item.SourceName, &item.ImportedBy, &item.ImportedAt, &manifest); err != nil {
			return nil, err
		}
		var summary legacyArchiveManifest
		if err = json.Unmarshal([]byte(manifest), &summary); err != nil {
			return nil, err
		}
		item.Counts = summary.Counts
		imports = append(imports, item)
	}
	return imports, rows.Err()
}

func (s *Store) ReadLegacyRecords(ctx context.Context, projectID, token, importID, recordType, after string, limit int) ([]LegacyRecord, error) {
	if recordType == "" || (recordType != "goal" && recordType != "actor" && recordType != "task" && recordType != "message" && recordType != "workflow" && recordType != "sop_artifact" && recordType != "artifact_file") {
		return nil, errors.New("a supported history record type is required")
	}
	if limit < 1 || limit > 100 {
		limit = 50
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = authorizePermission(ctx, tx, projectID, token, PermissionProjectRead); err != nil {
		return nil, err
	}
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT 1 FROM legacy_imports WHERE id=? AND project_id=?`, importID, projectID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT import_id,record_type,record_id,created_at,digest,length(payload) FROM legacy_records WHERE project_id=? AND import_id=? AND record_type=? AND record_id>? ORDER BY record_id LIMIT ?`, projectID, importID, recordType, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []LegacyRecord{}
	for rows.Next() {
		var item LegacyRecord
		if err = rows.Scan(&item.ImportID, &item.Type, &item.ID, &item.CreatedAt, &item.Digest, &item.PayloadBytes); err != nil {
			return nil, err
		}
		records = append(records, item)
	}
	return records, rows.Err()
}

func (s *Store) ReadLegacyRecordChunk(ctx context.Context, projectID, token, importID, recordType, recordID string, offset, maxChars int) (LegacyRecordChunk, error) {
	chunk := LegacyRecordChunk{ImportID: importID, Type: recordType, ID: recordID, Offset: offset}
	if recordType == "" || recordID == "" || offset < 0 {
		return chunk, errors.New("import ID, record type, record ID, and nonnegative offset are required")
	}
	if maxChars < 1 || maxChars > maxLegacyRecordChunkChars {
		maxChars = 16384
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return chunk, err
	}
	defer tx.Rollback()
	if _, err = authorizePermission(ctx, tx, projectID, token, PermissionProjectRead); err != nil {
		return chunk, err
	}
	var payload string
	err = tx.QueryRowContext(ctx, `SELECT r.payload,r.digest FROM legacy_records r JOIN legacy_imports i ON i.id=r.import_id AND i.project_id=r.project_id WHERE r.project_id=? AND r.import_id=? AND r.record_type=? AND r.record_id=?`, projectID, importID, recordType, recordID).Scan(&payload, &chunk.Digest)
	if errors.Is(err, sql.ErrNoRows) {
		return chunk, ErrNotFound
	}
	if err != nil {
		return chunk, err
	}
	digest := sha256.Sum256([]byte(payload))
	if hex.EncodeToString(digest[:]) != chunk.Digest {
		return chunk, fmt.Errorf("legacy record %s/%s failed its digest check", recordType, recordID)
	}
	runes := []rune(payload)
	if offset > len(runes) {
		return chunk, ErrConflict
	}
	end := offset + maxChars
	if end > len(runes) {
		end = len(runes)
	}
	chunk.Content = string(runes[offset:end])
	chunk.NextOffset = end
	chunk.Done = end == len(runes)
	return chunk, nil
}
