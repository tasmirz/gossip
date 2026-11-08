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
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// IntegrationCheck records a check run against the exact combined revision.
// Results are reported by the integration host; Gossip does not execute commands.
type IntegrationCheck struct {
	Name     string `json:"name"`
	Command  string `json:"command"`
	Passed   bool   `json:"passed"`
	Revision string `json:"revision"`
	Evidence string `json:"evidence,omitempty"`
}

// IntegrationRecord binds source candidates and host-reported checks to the
// clean combined revision observed in the integration workspace.
type IntegrationRecord struct {
	ID               string             `json:"id"`
	TaskID           string             `json:"task_id"`
	Attempt          int                `json:"attempt"`
	Author           string             `json:"author"`
	RepositoryID     string             `json:"repository_id"`
	WorkspaceID      string             `json:"workspace_id"`
	BaseRevision     string             `json:"base_revision"`
	CombinedRevision string             `json:"combined_revision"`
	CandidateIDs     []string           `json:"candidate_ids"`
	Checks           []IntegrationCheck `json:"checks"`
	Passed           bool               `json:"passed"`
	Summary          string             `json:"summary"`
	Digest           string             `json:"digest"`
	CreatedAt        int64              `json:"created_at"`
	EvidenceTrust    string             `json:"evidence_trust"`
}

// IntegrationSubmission is the immutable intent queued and replayed by a
// remote adapter. RequestID remains stable across retries and restarts.
type IntegrationSubmission struct {
	TaskID           string             `json:"task_id"`
	Attempt          int                `json:"attempt"`
	WorkspaceID      string             `json:"workspace_id"`
	BaseRevision     string             `json:"base_revision"`
	CombinedRevision string             `json:"combined_revision"`
	Observation      GitObservation     `json:"observation"`
	CandidateIDs     []string           `json:"candidate_ids"`
	Checks           []IntegrationCheck `json:"checks"`
	Summary          string             `json:"summary"`
	RequestID        string             `json:"request_id"`
}

// RecordIntegration records a clean workspace observation from the adapter,
// exact candidate inputs, and checks tied to one integration attempt. It does
// not merge source or execute checks; those actions stay on the agent host.
func (s *Store) RecordIntegration(ctx context.Context, project, token string, submission IntegrationSubmission) (IntegrationRecord, error) {
	var record IntegrationRecord
	taskID, attempt, workspaceID := submission.TaskID, submission.Attempt, submission.WorkspaceID
	baseRevision, combinedRevision, observation := submission.BaseRevision, submission.CombinedRevision, submission.Observation
	candidateIDs, checks, summary, requestID := submission.CandidateIDs, submission.Checks, submission.Summary, submission.RequestID
	if requestID == "" {
		requestID = stableIntegrationRequestID(taskID, attempt)
		submission.RequestID = requestID
	}
	if attempt < 1 || len(candidateIDs) < 2 || len(candidateIDs) > 50 || len(checks) == 0 || len(checks) > 100 || strings.TrimSpace(summary) == "" || len(summary) > 64*1024 || len(requestID) > 128 || !utf8.ValidString(requestID) {
		return record, errors.New("integration requires an attempt, 2-50 candidates, checks, and a bounded summary")
	}
	if !validRevision(baseRevision) || !validRevision(combinedRevision) || observation.Head != combinedRevision || observation.Dirty || strings.TrimSpace(observation.Branch) == "" || len(observation.Branch) > 512 {
		return record, errors.New("integration workspace must be clean and observed at the combined revision")
	}
	seenCandidates := map[string]bool{}
	seenTasks := map[string]bool{}
	for _, id := range candidateIDs {
		if id == "" || len(id) > 256 || seenCandidates[id] {
			return record, errors.New("candidate ids must be unique and non-empty")
		}
		seenCandidates[id] = true
	}
	allPassed := true
	for i := range checks {
		c := &checks[i]
		if strings.TrimSpace(c.Name) == "" || len(c.Name) > 256 || strings.TrimSpace(c.Command) == "" || len(c.Command) > 2048 || c.Revision != combinedRevision || len(c.Evidence) > 16*1024 {
			return record, fmt.Errorf("check %d must name a command and evidence for the combined revision", i+1)
		}
		if !c.Passed {
			allPassed = false
		}
	}
	if len(baseRevision) > 4096 || baseRevision == "" || len(summary) > 64*1024 {
		return record, errors.New("integration baseline and summary are required within size limits")
	}
	id, err := randomID("integration_")
	if err != nil {
		return record, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return record, err
	}
	defer tx.Rollback()
	actor, err := authorizePermission(ctx, tx, project, token, PermissionIntegration)
	if err != nil {
		return record, err
	}
	payload, err := json.Marshal([]any{taskID, attempt, workspaceID, baseRevision, combinedRevision, observation, candidateIDs, checks, summary})
	if err != nil {
		return record, err
	}
	payloadDigest := sha256.Sum256(payload)
	if requestID != "" {
		var prior []byte
		err = tx.QueryRowContext(ctx, `SELECT payload_digest,integration_id FROM integration_requests WHERE project_id=? AND principal_id=? AND request_id=?`, project, actor.ID, requestID).Scan(&prior, &record.ID)
		if err == nil {
			if !bytes.Equal(prior, payloadDigest[:]) {
				return IntegrationRecord{}, ErrConflict
			}
			return getIntegration(ctx, tx, project, record.ID)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return record, err
		}
	}
	var repoID, workspaceOwner string
	if err = tx.QueryRowContext(ctx, `SELECT w.repository_id,w.owner FROM workspaces w WHERE w.project_id=? AND w.id=?`, project, workspaceID).Scan(&repoID, &workspaceOwner); errors.Is(err, sql.ErrNoRows) {
		return record, ErrNotFound
	} else if err != nil {
		return record, err
	}
	if workspaceOwner != actor.ID || workspaceID == "" || observation.Branch == "" || observation.Dirty {
		return record, errors.New("integration must use the actor's registered, clean workspace at the observed combined revision")
	}
	var owner, status, kind, attemptOwner, attemptWorkspace, attemptHead string
	var leaseUntil int64
	err = tx.QueryRowContext(ctx, `SELECT t.owner,t.status,t.kind,t.lease_until,a.principal_id,COALESCE(a.workspace_id,''),a.workspace_head FROM tasks t JOIN attempts a ON a.task_id=t.id AND a.number=t.attempt WHERE t.project_id=? AND t.id=? AND t.attempt=?`, project, taskID, attempt).Scan(&owner, &status, &kind, &leaseUntil, &attemptOwner, &attemptWorkspace, &attemptHead)
	if errors.Is(err, sql.ErrNoRows) {
		return record, ErrNotFound
	}
	if err != nil {
		return record, err
	}
	if kind != "integration" || status != "running" || leaseUntil <= time.Now().UnixMilli() || owner != actor.ID || attemptOwner != actor.ID || attemptWorkspace != workspaceID || attemptHead != baseRevision {
		return record, errors.New("integration requires the actor's active integration attempt and its recorded base revision")
	}
	for _, candidateID := range candidateIDs {
		var sourceTask, sourceRepo, sourceRevision, candidateBaseline string
		err = tx.QueryRowContext(ctx, `SELECT c.task_id,w.repository_id,a.workspace_head,c.baseline FROM candidate_reports c JOIN attempts a ON a.task_id=c.task_id AND a.number=c.attempt JOIN workspaces w ON w.id=a.workspace_id WHERE c.id=? AND c.task_id IN (SELECT id FROM tasks WHERE project_id=?)`, candidateID, project).Scan(&sourceTask, &sourceRepo, &sourceRevision, &candidateBaseline)
		if errors.Is(err, sql.ErrNoRows) {
			return record, fmt.Errorf("candidate %s is unavailable or has no registered source workspace", candidateID)
		}
		if err != nil {
			return record, err
		}
		if sourceRepo != repoID || sourceTask == taskID || seenTasks[sourceTask] || !validRevision(sourceRevision) || candidateBaseline != sourceRevision {
			return record, errors.New("integration candidates must come from distinct tasks in the same repository")
		}
		seenTasks[sourceTask] = true
	}
	checksJSON, err := json.Marshal(checks)
	if err != nil {
		return record, err
	}
	candidateJSON, err := json.Marshal(candidateIDs)
	if err != nil {
		return record, err
	}
	passed := allPassed
	canonical, _ := json.Marshal([]any{taskID, attempt, actor.ID, repoID, workspaceID, baseRevision, combinedRevision, candidateIDs, checks, summary, passed})
	digest := sha256.Sum256(canonical)
	record = IntegrationRecord{ID: id, TaskID: taskID, Attempt: attempt, Author: actor.ID, RepositoryID: repoID, WorkspaceID: workspaceID, BaseRevision: baseRevision, CombinedRevision: combinedRevision, CandidateIDs: candidateIDs, Checks: checks, Passed: passed, Summary: summary, Digest: hex.EncodeToString(digest[:]), CreatedAt: time.Now().UnixMilli(), EvidenceTrust: "host_reported"}
	if _, err = tx.ExecContext(ctx, `UPDATE workspaces SET branch=?,head=?,dirty=0,updated_at=? WHERE project_id=? AND id=? AND owner=?`, observation.Branch, combinedRevision, record.CreatedAt, project, workspaceID, actor.ID); err != nil {
		return IntegrationRecord{}, err
	}
	if err = event(ctx, tx, project, actor.ID, "workspace.updated", workspaceID, map[string]string{"branch": observation.Branch, "head": combinedRevision}); err != nil {
		return IntegrationRecord{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO integration_records(id,project_id,task_id,attempt,author,repository_id,workspace_id,base_revision,combined_revision,candidate_ids,checks_json,summary,digest,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, project, taskID, attempt, actor.ID, repoID, workspaceID, baseRevision, combinedRevision, string(candidateJSON), string(checksJSON), summary, record.Digest, record.CreatedAt); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return IntegrationRecord{}, ErrConflict
		}
		return IntegrationRecord{}, err
	}
	if requestID != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO integration_requests(project_id,principal_id,request_id,payload_digest,integration_id) VALUES(?,?,?,?,?)`, project, actor.ID, requestID, payloadDigest[:], id); err != nil {
			return IntegrationRecord{}, err
		}
	}
	if err = event(ctx, tx, project, actor.ID, "integration.recorded", id, map[string]any{"task_id": taskID, "attempt": attempt, "combined_revision": combinedRevision, "passed": passed, "candidate_ids": candidateIDs, "evidence_trust": record.EvidenceTrust}); err != nil {
		return IntegrationRecord{}, err
	}
	if err = tx.Commit(); err != nil {
		return IntegrationRecord{}, err
	}
	return record, nil
}

func validRevision(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func stableIntegrationRequestID(taskID string, attempt int) string {
	digest := sha256.Sum256([]byte(taskID + "\x00" + strconv.Itoa(attempt)))
	return "integration_" + hex.EncodeToString(digest[:16])
}

func (s *Store) ListIntegrations(ctx context.Context, project, token string, limit int) ([]IntegrationRecord, error) {
	if limit < 1 || limit > 200 {
		return nil, errors.New("invalid limit")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = authorizeProjectRead(ctx, tx, project, token); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM integration_records WHERE project_id=? ORDER BY created_at DESC,id DESC LIMIT ?`, project, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IntegrationRecord{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		r, readErr := getIntegration(ctx, tx, project, id)
		if readErr != nil {
			return nil, readErr
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func getIntegration(ctx context.Context, tx *sql.Tx, project, id string) (IntegrationRecord, error) {
	var r IntegrationRecord
	var candidateJSON, checksJSON string
	err := tx.QueryRowContext(ctx, `SELECT id,task_id,attempt,author,repository_id,workspace_id,base_revision,combined_revision,candidate_ids,checks_json,summary,digest,created_at FROM integration_records WHERE project_id=? AND id=?`, project, id).Scan(&r.ID, &r.TaskID, &r.Attempt, &r.Author, &r.RepositoryID, &r.WorkspaceID, &r.BaseRevision, &r.CombinedRevision, &candidateJSON, &checksJSON, &r.Summary, &r.Digest, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal([]byte(candidateJSON), &r.CandidateIDs); err != nil {
		return r, err
	}
	if err = json.Unmarshal([]byte(checksJSON), &r.Checks); err != nil {
		return r, err
	}
	r.Passed = true
	for _, check := range r.Checks {
		if !check.Passed {
			r.Passed = false
			break
		}
	}
	r.EvidenceTrust = "host_reported"
	return r, nil
}
