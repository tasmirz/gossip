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
	"strings"
	"time"
	"unicode/utf8"
)

const taskColumns = `id,project_id,plan_revision,title,description,kind,report_contract,status,COALESCE(owner,''),attempt,lease_until,COALESCE((SELECT principal_id FROM task_assignments WHERE task_id=tasks.id),''),COALESCE((SELECT reason FROM task_blockers WHERE task_id=tasks.id),'')`

type scanner interface{ Scan(...any) error }

func scanTask(row scanner) (Task, error) {
	var t Task
	var contract string
	err := row.Scan(&t.ID, &t.ProjectID, &t.PlanRevision, &t.Title, &t.Description, &t.Kind, &contract, &t.Status, &t.Owner, &t.Attempt, &t.LeaseUntil, &t.AssignedTo, &t.BlockerReason)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	err = json.Unmarshal([]byte(contract), &t.Contract)
	return t, err
}

// CreateTask records a work order and its prerequisites in one transaction.
func (s *Store) CreateTask(ctx context.Context, project, token string, revision int, title, description, kind string, contract ReportContract, dependencies []string) (Task, error) {
	return s.createTask(ctx, project, token, "", revision, title, description, kind, contract, dependencies)
}

// CreateTaskWithID makes remote retries safe after the home commits a task but
// the response is lost. IDs are scoped to the authenticated principal/project.
func (s *Store) CreateTaskWithID(ctx context.Context, project, token, requestID string, revision int, title, description, kind string, contract ReportContract, dependencies []string) (Task, error) {
	if strings.TrimSpace(requestID) == "" || len(requestID) > 128 || !utf8.ValidString(requestID) {
		return Task{}, errors.New("stable request_id required (maximum 128 UTF-8 bytes)")
	}
	return s.createTask(ctx, project, token, requestID, revision, title, description, kind, contract, dependencies)
}

func (s *Store) createTask(ctx context.Context, project, token, requestID string, revision int, title, description, kind string, contract ReportContract, dependencies []string) (Task, error) {
	var zero Task
	if strings.TrimSpace(title) == "" || len(title) > 512 || strings.TrimSpace(description) == "" || len(description) > 256*1024 {
		return zero, errors.New("invalid task title or description")
	}
	switch kind {
	case "research", "design", "implementation", "review", "verification", "integration":
	default:
		return zero, errors.New("invalid task kind")
	}
	switch contract.ReviewMode {
	case "", "summary", "report_first", "targeted_source", "full_source":
	default:
		return zero, errors.New("invalid review mode")
	}
	switch contract.ReviewType {
	case "full_code":
	case "diagram_report":
		if !contains(contract.Formats, "markdown") || !contains(contract.Formats, "mermaid_sequence") || !contains(contract.Formats, "mermaid_class") {
			return zero, errors.New("diagram_report requires markdown, mermaid_sequence, and mermaid_class formats")
		}
	case "":
		return zero, errors.New("review_type must be chosen during planning: full_code or diagram_report")
	default:
		return zero, errors.New("invalid review type")
	}
	if contract.SummaryTokenTarget < 0 || contract.SummaryTokenTarget > 100000 {
		return zero, errors.New("invalid summary token target")
	}
	contract.RequiredRole = strings.TrimSpace(contract.RequiredRole)
	if len(contract.RequiredRole) > 128 || !utf8.ValidString(contract.RequiredRole) {
		return zero, errors.New("invalid required role")
	}
	requiredCapabilities, capabilityErr := normalizeRequiredCapabilities(contract.RequiredCapabilities)
	if capabilityErr != nil {
		return zero, capabilityErr
	}
	contract.RequiredCapabilities = requiredCapabilities
	raw, err := json.Marshal(contract)
	if err != nil || len(raw) > 64*1024 {
		return zero, errors.New("invalid report contract")
	}
	requestDigest := [32]byte{}
	if requestID != "" {
		payload, marshalErr := json.Marshal(struct {
			Revision     int            `json:"revision"`
			Title        string         `json:"title"`
			Description  string         `json:"description"`
			Kind         string         `json:"kind"`
			Contract     ReportContract `json:"contract"`
			Dependencies []string       `json:"dependencies"`
		}{revision, title, description, kind, contract, dependencies})
		if marshalErr != nil {
			return zero, marshalErr
		}
		requestDigest = sha256.Sum256(payload)
	}
	id, err := randomID("task_")
	if err != nil {
		return zero, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	actor, err := authorizeTaskDelegate(ctx, tx, project, token)
	if err != nil {
		return zero, err
	}
	if requestID != "" {
		var previousDigest []byte
		var previousTask string
		err = tx.QueryRowContext(ctx, `SELECT payload_digest,task_id FROM task_requests WHERE project_id=? AND principal_id=? AND request_id=?`, project, actor.ID, requestID).Scan(&previousDigest, &previousTask)
		if err == nil {
			if !bytes.Equal(previousDigest, requestDigest[:]) {
				return zero, ErrConflict
			}
			return scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE project_id=? AND id=?`, project, previousTask))
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return zero, err
		}
	}
	settings, err := readSettings(ctx, tx, project)
	if err != nil {
		return zero, err
	}
	if contract.ReviewMode == "" {
		contract.ReviewMode = settings.DefaultReviewMode
	}
	if contract.SummaryTokenTarget == 0 {
		contract.SummaryTokenTarget = settings.SummaryTokenTarget
	}
	raw, err = json.Marshal(contract)
	if err != nil || len(raw) > 64*1024 {
		return zero, errors.New("invalid report contract")
	}
	var current int
	if err = tx.QueryRowContext(ctx, `SELECT plan_revision FROM projects WHERE id=?`, project).Scan(&current); err != nil {
		return zero, err
	}
	if revision < 1 || revision != current {
		return zero, ErrConflict
	}
	seen := map[string]bool{}
	status := "ready"
	for _, dep := range dependencies {
		if seen[dep] {
			return zero, errors.New("duplicate prerequisite")
		}
		seen[dep] = true
		var depStatus string
		err = tx.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id=? AND project_id=?`, dep, project).Scan(&depStatus)
		if errors.Is(err, sql.ErrNoRows) {
			return zero, fmt.Errorf("prerequisite %s: %w", dep, ErrNotFound)
		}
		if err != nil {
			return zero, err
		}
		if depStatus != "accepted" {
			status = "blocked"
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO tasks(id,project_id,plan_revision,title,description,kind,report_contract,status,created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		id, project, revision, title, description, kind, string(raw), status, time.Now().UnixMilli())
	if err != nil {
		return zero, err
	}
	if requestID != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO task_requests(project_id,principal_id,request_id,payload_digest,task_id,created_at) VALUES(?,?,?,?,?,?)`, project, actor.ID, requestID, requestDigest[:], id, time.Now().UnixMilli()); err != nil {
			return zero, err
		}
	}
	for _, dep := range dependencies {
		if _, err = tx.ExecContext(ctx, `INSERT INTO dependencies(task_id,prerequisite_id) VALUES(?,?)`, id, dep); err != nil {
			return zero, err
		}
	}
	if err = event(ctx, tx, project, actor.ID, "task.created", id, map[string]any{"kind": kind, "dependencies": dependencies}); err != nil {
		return zero, err
	}
	t, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id=?`, id))
	if err != nil {
		return zero, err
	}
	if err = tx.Commit(); err != nil {
		return zero, err
	}
	return t, nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// RequestReview sends the orchestrator a durable request for the review type
// selected when this task was planned. The worker must own a live attempt.
func (s *Store) RequestReview(ctx context.Context, project, token, taskID string, attempt int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := authorizePermission(ctx, tx, project, token, PermissionTaskClaim)
	if err != nil {
		return err
	}
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT r.review_type FROM task_review_requests r JOIN tasks t ON t.id=r.task_id WHERE r.task_id=? AND r.attempt=? AND r.principal_id=? AND t.project_id=?`, taskID, attempt, actor.ID, project).Scan(&existing)
	if err == nil {
		return tx.Commit() // A retried request must not create duplicate notifications.
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var rawContract string
	err = tx.QueryRowContext(ctx, `SELECT t.report_contract FROM tasks t WHERE t.id=? AND t.project_id=? AND t.status='running' AND t.owner=? AND t.attempt=? AND t.lease_until>? AND t.plan_revision=(SELECT plan_revision FROM projects WHERE id=?)`, taskID, project, actor.ID, attempt, time.Now().UnixMilli(), project).Scan(&rawContract)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	var contract ReportContract
	if err = json.Unmarshal([]byte(rawContract), &contract); err != nil {
		return err
	}
	reviewType := contract.ReviewType
	if reviewType == "" {
		return errors.New("task has no planned review_type; revise the task contract before requesting review")
	}
	var evidence struct {
		ID       string
		Digest   string
		Baseline string
	}
	if reviewType == "full_code" {
		err = tx.QueryRowContext(ctx, `SELECT id,digest,baseline FROM candidate_reports WHERE task_id=? AND attempt=? AND author=? ORDER BY created_at DESC,rowid DESC LIMIT 1`, taskID, attempt, actor.ID).Scan(&evidence.ID, &evidence.Digest, &evidence.Baseline)
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("full_code review requires a source diff saved as a candidate for this attempt first")
		}
		if err != nil {
			return err
		}
		var source string
		if err = tx.QueryRowContext(ctx, `SELECT body FROM candidate_reports WHERE id=?`, evidence.ID).Scan(&source); err != nil {
			return err
		}
		if err = validateSourceDiff(source); err != nil {
			return err
		}
	}
	now := time.Now().UnixMilli()
	res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO task_review_requests(task_id,attempt,principal_id,review_type,created_at) VALUES(?,?,?,?,?)`, taskID, attempt, actor.ID, reviewType, now)
	if err != nil {
		return err
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if inserted == 0 {
		return tx.Commit()
	}
	if evidence.ID != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO review_request_candidates(task_id,attempt,candidate_id,digest,baseline) VALUES(?,?,?,?,?)`, taskID, attempt, evidence.ID, evidence.Digest, evidence.Baseline); err != nil {
			return err
		}
	}
	payload := map[string]any{"task_id": taskID, "attempt": attempt, "review_type": reviewType}
	if evidence.ID != "" {
		payload["candidate_id"] = evidence.ID
		payload["candidate_digest"] = evidence.Digest
		payload["candidate_baseline"] = evidence.Baseline
	}
	if err = event(ctx, tx, project, actor.ID, "review.requested", taskID, payload); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetTask(ctx context.Context, project, token, id string) (Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	if _, err = authorizeProjectRead(ctx, tx, project, token); err != nil {
		return Task{}, err
	}
	return scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE project_id=? AND id=?`, project, id))
}

// ClaimTask atomically reserves eligible work. Each claim increments a fencing
// number; submission requires the active owner's unexpired attempt.
// AssignTask directs unclaimed work to an active project member. An empty
// assignee clears the assignment. Existing running work must be reconciled first.
func (s *Store) AssignTask(ctx context.Context, project, token, id, assignee string) (Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	actor, err := authorizeTaskDelegate(ctx, tx, project, token)
	if err != nil {
		return Task{}, err
	}
	var status, rawContract string
	err = tx.QueryRowContext(ctx, `SELECT status,report_contract FROM tasks WHERE id=? AND project_id=?`, id, project).Scan(&status, &rawContract)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, err
	}
	if status != "ready" && status != "blocked" && status != "needs_replan" {
		return Task{}, ErrConflict
	}
	var contract ReportContract
	if err = json.Unmarshal([]byte(rawContract), &contract); err != nil {
		return Task{}, err
	}
	if assignee != "" {
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM principals WHERE id=? AND project_id=? AND revoked=0 AND role IN ('owner','worker')`, assignee, project).Scan(&count); err != nil {
			return Task{}, err
		}
		if count != 1 {
			return Task{}, ErrNotFound
		}
		if err = requireAssignedWorkRole(ctx, tx, project, assignee, id, contract.RequiredRole, time.Now().UnixMilli()); err != nil {
			return Task{}, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO task_assignments(task_id,principal_id,assigned_by,assigned_at) VALUES(?,?,?,?) ON CONFLICT(task_id) DO UPDATE SET principal_id=excluded.principal_id,assigned_by=excluded.assigned_by,assigned_at=excluded.assigned_at`, id, assignee, actor.ID, time.Now().UnixMilli())
	} else {
		_, err = tx.ExecContext(ctx, `DELETE FROM task_assignments WHERE task_id=?`, id)
	}
	if err != nil {
		return Task{}, err
	}
	if err = event(ctx, tx, project, actor.ID, "task.assigned", id, map[string]string{"assignee": assignee}); err != nil {
		return Task{}, err
	}
	t, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id=? AND project_id=?`, id, project))
	if err != nil {
		return Task{}, err
	}
	if err = tx.Commit(); err != nil {
		return Task{}, err
	}
	return t, nil
}

func (s *Store) ClaimTask(ctx context.Context, project, token, id string, lease time.Duration) (Task, error) {
	return s.ClaimTaskInWorkspace(ctx, project, token, id, "", lease)
}

// ClaimTaskInWorkspace snapshots the caller-reported workspace head and dirty
// state alongside the claim. An empty workspace means the baseline is unknown.
func (s *Store) ClaimTaskInWorkspace(ctx context.Context, project, token, id, workspace string, lease time.Duration) (Task, error) {
	return s.claimTask(ctx, project, token, id, workspace, nil, lease)
}

// ClaimObservedWorkspace atomically records the caller's Git observation and
// snapshots it for the attempt. The caller inspects its own machine.
func (s *Store) ClaimObservedWorkspace(ctx context.Context, project, token, id, workspace string, observation GitObservation, lease time.Duration) (Task, error) {
	if workspace == "" || observation.Root == "" || len(observation.Root) > 4096 || len(observation.Branch) > 512 || len(observation.Head) > 512 {
		return Task{}, errors.New("invalid workspace observation")
	}
	return s.claimTask(ctx, project, token, id, workspace, &observation, lease)
}
func (s *Store) claimTask(ctx context.Context, project, token, id, workspace string, observation *GitObservation, lease time.Duration) (Task, error) {
	if lease < time.Second || lease > time.Hour {
		return Task{}, errors.New("lease must be between one second and one hour")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	actor, err := authorizePermission(ctx, tx, project, token, PermissionTaskClaim)
	if err != nil {
		return Task{}, err
	}
	var workspaceID any
	var head string
	var dirty bool
	var localPath string
	if workspace != "" {
		err = tx.QueryRowContext(ctx, `SELECT head,dirty,local_path FROM workspaces WHERE id=? AND project_id=? AND owner=?`, workspace, project, actor.ID).Scan(&head, &dirty, &localPath)
		if errors.Is(err, sql.ErrNoRows) {
			return Task{}, ErrNotFound
		}
		if err != nil {
			return Task{}, err
		}
		workspaceID = workspace
		if observation != nil {
			if localPath != observation.Root {
				return Task{}, errors.New("workspace root differs from registered path; register the Git root")
			}
			head, dirty = observation.Head, observation.Dirty
		}
	}
	now := time.Now().UnixMilli()
	var rawContract string
	if err = tx.QueryRowContext(ctx, `SELECT report_contract FROM tasks WHERE id=? AND project_id=?`, id, project).Scan(&rawContract); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Task{}, ErrNotFound
		}
		return Task{}, err
	}
	var contract ReportContract
	if err = json.Unmarshal([]byte(rawContract), &contract); err != nil {
		return Task{}, err
	}
	if err = requireAssignedWorkRole(ctx, tx, project, actor.ID, id, contract.RequiredRole, now); err != nil {
		return Task{}, err
	}
	if err = requireReportedCapabilities(ctx, tx, project, actor.ID, contract.RequiredCapabilities, now); err != nil {
		return Task{}, err
	}
	settings, err := readSettings(ctx, tx, project)
	if err != nil {
		return Task{}, err
	}
	if lease > time.Duration(settings.MaxLeaseSeconds)*time.Second {
		return Task{}, errors.New("lease exceeds project limit")
	}
	var budgetLimit, allocated int64
	budgetEnabled := false
	err = tx.QueryRowContext(ctx, `SELECT token_limit FROM budget_envelopes WHERE project_id=?`, project).Scan(&budgetLimit)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Task{}, err
	}
	if err == nil {
		budgetEnabled = true
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT amount FROM budget_reservations WHERE task_id=?),0)`, id).Scan(&allocated); err != nil {
			return Task{}, err
		}
		if allocated <= 0 {
			return Task{}, errors.New("task requires a budget reservation before claim")
		}
	}
	var running int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE project_id=? AND status='running' AND lease_until>?`, project, now).Scan(&running); err != nil {
		return Task{}, err
	}
	if running >= settings.MaxParallel {
		return Task{}, ErrConflict
	}
	res, err := tx.ExecContext(ctx, `UPDATE tasks SET status='running',owner=?,attempt=attempt+1,lease_until=?
		WHERE id=? AND project_id=? AND (status='ready' OR (status='running' AND lease_until<=?))
		AND plan_revision=(SELECT plan_revision FROM projects WHERE id=?)
		AND EXISTS (SELECT 1 FROM plan_acknowledgments a WHERE a.project_id=tasks.project_id AND a.principal_id=? AND a.revision=tasks.plan_revision)
		AND NOT EXISTS (SELECT 1 FROM task_assignments a WHERE a.task_id=tasks.id AND a.principal_id<>?)
		AND NOT EXISTS (SELECT 1 FROM dependencies d JOIN tasks p ON p.id=d.prerequisite_id WHERE d.task_id=tasks.id AND p.status<>'accepted')`,
		actor.ID, now+lease.Milliseconds(), id, project, now, project, actor.ID, actor.ID)
	if err != nil {
		return Task{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Task{}, err
	}
	if n != 1 {
		return Task{}, ErrConflict
	}
	if observation != nil {
		if _, err = tx.ExecContext(ctx, `UPDATE workspaces SET branch=?,head=?,dirty=?,updated_at=? WHERE id=? AND project_id=? AND owner=?`, observation.Branch, head, dirty, now, workspace, project, actor.ID); err != nil {
			return Task{}, err
		}
		if err = event(ctx, tx, project, actor.ID, "workspace.updated", workspace, map[string]string{"head": head}); err != nil {
			return Task{}, err
		}
	}
	t, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id=? AND project_id=?`, id, project))
	if err != nil {
		return Task{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO attempts(task_id,number,principal_id,workspace_id,workspace_head,workspace_dirty,started_at) VALUES(?,?,?,?,?,?,?)`, id, t.Attempt, actor.ID, workspaceID, head, dirty, now); err != nil {
		return Task{}, err
	}
	if budgetEnabled {
		budget, e := readBudget(ctx, tx, project)
		if e != nil {
			return Task{}, e
		}
		var prior int64
		for _, r := range budget.Reservations {
			if r.TaskID == id {
				prior = r.Held
				break
			}
		}
		// The first attempt consumes the already-reserved allocation. Every
		// overlapping or later attempt holds another allocation until release.
		extra := max(allocated, prior+allocated) - max(allocated, prior)
		if budget.Reserved+extra > budgetLimit {
			return Task{}, errors.New("insufficient budget to reserve another attempt")
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO budget_attempt_holds(task_id,attempt,amount) VALUES(?,?,?)`, id, t.Attempt, allocated); e != nil {
			return Task{}, e
		}
	}
	if err = event(ctx, tx, project, actor.ID, "task.claimed", id, map[string]any{"attempt": t.Attempt, "lease_until": t.LeaseUntil}); err != nil {
		return Task{}, err
	}
	if err = tx.Commit(); err != nil {
		return Task{}, err
	}
	return t, nil
}

// SubmitReport stores an immutable result and advances only a live attempt.
func (s *Store) SubmitReport(ctx context.Context, project, token, taskID string, attempt int, baseline, body string) (Report, error) {
	return s.submitReport(ctx, project, token, taskID, attempt, baseline, body, "")
}

// SubmitReportWithID makes a report retry safe when the caller loses the
// response after the home node committed the submission.
func (s *Store) SubmitReportWithID(ctx context.Context, project, token, taskID string, attempt int, baseline, body, requestID string) (Report, error) {
	if requestID == "" || len(requestID) > 128 || !utf8.ValidString(requestID) {
		return Report{}, errors.New("a valid report request ID is required")
	}
	return s.submitReport(ctx, project, token, taskID, attempt, baseline, body, requestID)
}

func (s *Store) submitReport(ctx context.Context, project, token, taskID string, attempt int, baseline, body, requestID string) (Report, error) {
	var r Report
	if strings.TrimSpace(body) == "" || !utf8.ValidString(body) || len(body) > 1024*1024 || strings.TrimSpace(baseline) == "" || len(baseline) > 4096 {
		return r, errors.New("report body and baseline required within size limits")
	}
	id := ""
	var err error
	if requestID == "" {
		id, err = randomID("report_")
		if err != nil {
			return r, err
		}
	} else {
		id, err = randomID("report_")
		if err != nil {
			return r, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	actor, err := authorizePermission(ctx, tx, project, token, PermissionResultSubmit)
	if err != nil {
		return r, err
	}
	payloadBytes, err := json.Marshal(struct {
		TaskID   string `json:"task_id"`
		Attempt  int    `json:"attempt"`
		Baseline string `json:"baseline"`
		Body     string `json:"body"`
	}{TaskID: taskID, Attempt: attempt, Baseline: baseline, Body: body})
	if err != nil {
		return Report{}, err
	}
	payloadHash := sha256.Sum256(payloadBytes)
	payloadDigest := hex.EncodeToString(payloadHash[:])
	if requestID != "" {
		var previousDigest, previousReport string
		err = tx.QueryRowContext(ctx, `SELECT payload_digest,report_id FROM report_requests WHERE project_id=? AND principal_id=? AND request_id=?`, project, actor.ID, requestID).Scan(&previousDigest, &previousReport)
		if err == nil {
			if previousDigest != payloadDigest {
				return Report{}, ErrConflict
			}
			err = tx.QueryRowContext(ctx, `SELECT id,task_id,attempt,author,body,digest,baseline FROM reports WHERE id=?`, previousReport).Scan(&r.ID, &r.TaskID, &r.Attempt, &r.Author, &r.Body, &r.Digest, &r.Baseline)
			if err != nil {
				return Report{}, err
			}
			_ = tx.QueryRowContext(ctx, `SELECT candidate_id,digest,baseline FROM report_review_evidence WHERE report_id=?`, r.ID).Scan(&r.SourceCandidateID, &r.SourceCandidateDigest, &r.SourceCandidateBase)
			return r, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return Report{}, err
		}
	}
	now := time.Now().UnixMilli()
	var rawContract string
	err = tx.QueryRowContext(ctx, `SELECT report_contract FROM tasks WHERE id=? AND project_id=? AND status='running' AND owner=? AND attempt=? AND lease_until>? AND plan_revision=(SELECT plan_revision FROM projects WHERE id=?)`, taskID, project, actor.ID, attempt, now, project).Scan(&rawContract)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrConflict
	}
	if err != nil {
		return r, err
	}
	if err = validateAttemptBaseline(ctx, tx, project, taskID, attempt, actor.ID, baseline); err != nil {
		return r, err
	}
	var contract ReportContract
	if err = json.Unmarshal([]byte(rawContract), &contract); err != nil {
		return r, err
	}
	if err = validateReportBody(contract, body); err != nil {
		return r, err
	}
	var sourceCandidateID, sourceCandidateDigest, sourceCandidateBaseline string
	if contract.ReviewType == "full_code" {
		err = tx.QueryRowContext(ctx, `SELECT candidate_id,digest,baseline FROM review_request_candidates WHERE task_id=? AND attempt=?`, taskID, attempt).Scan(&sourceCandidateID, &sourceCandidateDigest, &sourceCandidateBaseline)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return r, err
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE tasks SET status='submitted',lease_until=0
		WHERE id=? AND project_id=? AND status='running' AND owner=? AND attempt=? AND lease_until>?
		AND plan_revision=(SELECT plan_revision FROM projects WHERE id=?)`, taskID, project, actor.ID, attempt, now, project)
	if err != nil {
		return r, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return r, err
	}
	if n != 1 {
		return r, ErrConflict
	}
	hash := sha256.Sum256([]byte(body))
	r = Report{ID: id, TaskID: taskID, Attempt: attempt, Author: actor.ID, Body: body, Digest: hex.EncodeToString(hash[:]), Baseline: baseline, SourceCandidateID: sourceCandidateID, SourceCandidateDigest: sourceCandidateDigest, SourceCandidateBase: sourceCandidateBaseline}
	if _, err = tx.ExecContext(ctx, `INSERT INTO reports(id,task_id,attempt,author,body,digest,baseline,created_at) VALUES(?,?,?,?,?,?,?,?)`, r.ID, taskID, attempt, actor.ID, body, r.Digest, baseline, now); err != nil {
		return Report{}, err
	}
	if sourceCandidateID != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO report_review_evidence(report_id,candidate_id,digest,baseline) VALUES(?,?,?,?)`, r.ID, sourceCandidateID, sourceCandidateDigest, sourceCandidateBaseline); err != nil {
			return Report{}, err
		}
	}
	payload := map[string]any{"task_id": taskID, "attempt": attempt, "digest": r.Digest}
	if sourceCandidateID != "" {
		payload["source_candidate_id"] = sourceCandidateID
		payload["source_candidate_digest"] = sourceCandidateDigest
	}
	if err = event(ctx, tx, project, actor.ID, "report.submitted", r.ID, payload); err != nil {
		return Report{}, err
	}
	if requestID != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO report_requests(project_id,principal_id,request_id,payload_digest,report_id,created_at) VALUES(?,?,?,?,?,?)`, project, actor.ID, requestID, payloadDigest, r.ID, now); err != nil {
			return Report{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return Report{}, err
	}
	return r, nil
}

// AcceptReport records an independent review of the exact submitted report.
// RequestRework returns a submitted task to the ready queue, preserving the report
// and recording the review feedback in the same transaction.
func (s *Store) RequestRework(ctx context.Context, project, token, reportID, digest, feedback string) error {
	if strings.TrimSpace(feedback) == "" || len(feedback) > 64*1024 {
		return errors.New("review feedback is required and must be within size limits")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := authorizePermission(ctx, tx, project, token, PermissionReviewDecide)
	if err != nil {
		return err
	}
	var taskID, author, storedDigest string
	var attempt int
	err = tx.QueryRowContext(ctx, `SELECT r.task_id,r.author,r.digest,r.attempt FROM reports r JOIN tasks t ON t.id=r.task_id WHERE r.id=? AND t.project_id=?`, reportID, project).Scan(&taskID, &author, &storedDigest, &attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if actor.ID == author {
		return ErrUnauthorized
	}
	if digest != storedDigest {
		return ErrConflict
	}
	res, err := tx.ExecContext(ctx, `UPDATE tasks SET status='ready',owner=NULL,lease_until=0 WHERE id=? AND project_id=? AND status='submitted' AND attempt=? AND plan_revision=(SELECT plan_revision FROM projects WHERE id=?)`, taskID, project, attempt, project)
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
	if err = event(ctx, tx, project, actor.ID, "report.rework_requested", reportID, map[string]any{"task_id": taskID, "digest": digest, "feedback": feedback}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AcceptReport(ctx context.Context, project, token, reportID, digest string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := authorizePermission(ctx, tx, project, token, PermissionReviewDecide)
	if err != nil {
		return err
	}
	var taskID, author, storedDigest, evidenceID, evidenceDigest string
	var attempt int
	err = tx.QueryRowContext(ctx, `SELECT r.task_id,r.author,r.digest,r.attempt,COALESCE(e.candidate_id,''),COALESCE(e.digest,'') FROM reports r JOIN tasks t ON t.id=r.task_id LEFT JOIN report_review_evidence e ON e.report_id=r.id WHERE r.id=? AND t.project_id=?`, reportID, project).Scan(&taskID, &author, &storedDigest, &attempt, &evidenceID, &evidenceDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if author == actor.ID {
		return ErrUnauthorized
	}
	if digest != storedDigest {
		return ErrConflict
	}
	res, err := tx.ExecContext(ctx, `UPDATE tasks SET status='accepted' WHERE id=? AND project_id=? AND status='submitted' AND attempt=? AND plan_revision=(SELECT plan_revision FROM projects WHERE id=?)`, taskID, project, attempt, project)
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
	payload := map[string]any{"task_id": taskID, "digest": digest}
	if evidenceID != "" {
		payload["source_candidate_id"] = evidenceID
		payload["source_candidate_digest"] = evidenceDigest
	}
	if err = event(ctx, tx, project, actor.ID, "report.accepted", reportID, payload); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `UPDATE tasks SET status='ready' WHERE project_id=? AND status='blocked' AND id IN (SELECT task_id FROM dependencies WHERE prerequisite_id=?) AND NOT EXISTS (SELECT 1 FROM task_blockers b WHERE b.task_id=tasks.id) AND NOT EXISTS (SELECT 1 FROM dependencies d JOIN tasks p ON p.id=d.prerequisite_id WHERE d.task_id=tasks.id AND p.status<>'accepted') RETURNING id`, project, taskID)
	if err != nil {
		return err
	}
	var ready []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ready = append(ready, id)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, id := range ready {
		if err = event(ctx, tx, project, actor.ID, "task.ready", id, map[string]string{"prerequisite": taskID}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ReadEvents(ctx context.Context, project, token string, after int64, limit int) ([]Event, error) {
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
	rows, err := tx.QueryContext(ctx, `SELECT e.seq,e.actor,e.kind,e.entity_id,e.payload,e.created_at FROM events e WHERE e.project_id=? AND e.seq>?
		AND (?='owner' OR e.kind NOT LIKE 'certificate.%')
		AND (e.kind NOT IN ('message.sent','message.acknowledged') OR EXISTS (
			SELECT 1 FROM messages m WHERE m.id=e.entity_id AND m.project_id=e.project_id
			AND (m.recipient IS NULL OR m.recipient=? OR m.sender=?)))
		ORDER BY e.seq LIMIT ?`, project, after, actor.Role, actor.ID, actor.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		var e Event
		var payload string
		if err = rows.Scan(&e.Sequence, &e.Actor, &e.Kind, &e.EntityID, &payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Payload = json.RawMessage(payload)
		events = append(events, e)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}
