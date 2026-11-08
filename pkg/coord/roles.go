package coord

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

func requireAssignedWorkRole(ctx context.Context, tx *sql.Tx, project, principal, task, requiredRole string, now int64) error {
	if requiredRole == "" {
		return nil
	}
	var assignedRole string
	err := tx.QueryRowContext(ctx, `SELECT name FROM work_roles
		WHERE project_id=? AND principal_id=? AND task_id IN ('',?) AND name<>'' AND (expires_at=0 OR expires_at>?)
		ORDER BY CASE WHEN task_id=? THEN 0 ELSE 1 END LIMIT 1`, project, principal, task, now, task).Scan(&assignedRole)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !strings.EqualFold(strings.TrimSpace(assignedRole), requiredRole)) {
		return fmt.Errorf("task requires the active %q role assignment: %w", requiredRole, ErrUnauthorized)
	}
	return err
}

type RolePreset struct {
	Name           string   `json:"name"`
	Version        string   `json:"version"`
	Provenance     string   `json:"provenance"`
	Sources        []string `json:"sources"`
	Duties         string   `json:"duties"`
	ExpectedOutput string   `json:"expected_output"`
	HandoffFormat  string   `json:"handoff_format"`
	Escalation     string   `json:"escalation"`
}

// These Gossip-authored, versioned guidance presets are not permissions or
// imported third-party skills. Their scope and handoffs are informed by the
// project plan and docs/role-skill-research.md.
func WorkRolePresets() []RolePreset {
	const version = "1.0.0"
	const provenance = "Gossip-authored; guidance informed by first-party agent-skill and orchestration references."
	return []RolePreset{
		{Name: "coordinator", Version: version, Provenance: provenance, Sources: []string{"https://openai.github.io/openai-agents-python/multi_agent/", "https://developers.openai.com/plugins/concepts/skills"}, Duties: "Maintain the user-approved goal and plan. Split work into bounded tasks; parallelize only independent questions or changes; reconcile findings and keep task scope aligned with the current plan.", ExpectedOutput: "Current decision and plan revision; task owners, dependencies and deliverables; compact synthesis with evidence and unresolved questions.", HandoffFormat: "Plan revision; decision; assignments; dependencies; evidence links; open questions.", Escalation: "Ask the user when goals, acceptance criteria, project access, cost limits, or conflicting evidence require a decision."},
		{Name: "researcher", Version: version, Provenance: provenance, Sources: []string{"https://developers.openai.com/plugins/concepts/skills", "https://developers.openai.com/blog/skills-shell-tips"}, Duties: "Answer only the assigned questions. Prefer primary sources; distinguish observed facts, inference, and uncertainty. Do not change the plan or modify code unless the task explicitly includes implementation.", ExpectedOutput: "Question-by-question findings with source paths or URLs, evidence, limitations, and a recommendation when requested.", HandoffFormat: "Summary; findings; evidence index; uncertainties; next questions.", Escalation: "Report missing access, insufficient evidence, conflicting sources, or a question that cannot be resolved within the assigned scope."},
		{Name: "implementer", Version: version, Provenance: provenance, Sources: []string{"https://developers.openai.com/blog/skills-agents-sdk", "https://developers.openai.com/plugins/concepts/skills"}, Duties: "Acknowledge the current plan and attempt baseline. Implement only the assigned scope in the selected workspace, preserve agreed interfaces, and run checks required by the task contract.", ExpectedOutput: "Changed files and behavior, source baseline, checks and results, and remaining risks.", HandoffFormat: "Outcome; files; baseline; checks; evidence; risks; candidate or report reference.", Escalation: "Stop and report scope changes, incompatible interfaces, unexpected shared-worktree edits, or a baseline that cannot be verified."},
		{Name: "reviewer", Version: version, Provenance: provenance, Sources: []string{"https://developers.openai.com/blog/eval-skills", "https://developers.openai.com/plugins/concepts/skills"}, Duties: "Review the exact revision and requested review type and depth. Report actionable findings with severity and precise references; separate verified findings from questions and uncovered scope.", ExpectedOutput: "Verdict, prioritized findings, exact revision or artifact, coverage, limitations, and any targeted evidence request.", HandoffFormat: "Reviewed revision; verdict; findings by severity; coverage; limitations; follow-up evidence.", Escalation: "Request more evidence when the report omits required paths or the source baseline is stale; disclose conflicts of interest or insufficient review access."},
		{Name: "verifier", Version: version, Provenance: provenance, Sources: []string{"https://developers.openai.com/blog/eval-skills", "https://developers.openai.com/blog/skills-agents-sdk"}, Duties: "Check each acceptance criterion against reproducible evidence at the recorded revision. Run only authorized and relevant checks; label unavailable or unrun checks explicitly.", ExpectedOutput: "Criterion-to-evidence matrix with commands, outcomes, environment, revision, and gaps.", HandoffFormat: "Criterion; evidence; command/result; revision; pass/fail/not run; gap.", Escalation: "Escalate failed checks, ambiguous criteria, missing fixtures, or any result that cannot be reproduced."},
		{Name: "integrator", Version: version, Provenance: provenance, Sources: []string{"https://openai.github.io/openai-agents-python/multi_agent/", "https://developers.openai.com/plugins/concepts/skills"}, Duties: "Integrate only approved work in the designated integration workspace. Preserve the source and baseline of each input, resolve conflicts explicitly, and verify the combined revision.", ExpectedOutput: "Combined revision, included inputs, conflict decisions, integration checks, and residual risks.", HandoffFormat: "Combined revision; input revisions; conflicts and resolutions; checks; remaining risks.", Escalation: "Pause when changes overlap unexpectedly, required inputs are unreviewed, provenance is unclear, or integration would silently discard work."},
	}
}

func (p RolePreset) dutyText() string {
	return "Preset version: " + p.Version + "\nProvenance: " + p.Provenance + "\nDuties: " + p.Duties + "\nExpected output: " + p.ExpectedOutput + "\nHandoff format: " + p.HandoffFormat + "\nEscalation: " + p.Escalation
}

type WorkRole struct {
	PrincipalID string `json:"principal_id"`
	TaskID      string `json:"task_id,omitempty"`
	Revision    int    `json:"revision"`
	Name        string `json:"name"`
	Duties      string `json:"duties"`
	ExpiresAt   int64  `json:"expires_at,omitempty"`
	UpdatedAt   int64  `json:"updated_at"`
	UpdatedBy   string `json:"updated_by"`
	Active      bool   `json:"active"`
}

func (s *Store) ListWorkRoles(ctx context.Context, project, token string) ([]WorkRole, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = authorizeProjectRead(ctx, tx, project, token); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT r.principal_id,r.task_id,r.revision,r.name,r.duties,r.expires_at,r.updated_at,r.updated_by FROM work_roles r JOIN principals p ON p.id=r.principal_id WHERE r.project_id=? AND p.revoked=0 ORDER BY r.principal_id,r.task_id`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WorkRole{}
	now := time.Now().UnixMilli()
	for rows.Next() {
		var r WorkRole
		if err = rows.Scan(&r.PrincipalID, &r.TaskID, &r.Revision, &r.Name, &r.Duties, &r.ExpiresAt, &r.UpdatedAt, &r.UpdatedBy); err != nil {
			return nil, err
		}
		r.Active = r.Name != "" && (r.ExpiresAt == 0 || r.ExpiresAt > now)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetWorkRole changes guidance only; explicit project permission grants remain independent.
func (s *Store) SetWorkRole(ctx context.Context, project, token, principal, task, name, duties string, expires int64, revision int) (WorkRole, error) {
	var r WorkRole
	if principal == "" || len(principal) > 256 || len(task) > 256 || len(name) > 128 || len(duties) > 64*1024 || revision < 0 || expires < 0 || !utf8.ValidString(name) || !utf8.ValidString(duties) || !utf8.ValidString(task) || !utf8.ValidString(principal) {
		return r, errors.New("invalid role assignment")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		if duties != "" || expires != 0 {
			return r, errors.New("cleared role cannot have duties or expiration")
		}
	} else if strings.TrimSpace(duties) == "" {
		for _, preset := range WorkRolePresets() {
			if preset.Name == name {
				duties = preset.dutyText()
				break
			}
		}
		if strings.TrimSpace(duties) == "" {
			return r, errors.New("custom roles require duties")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	actor, err := authenticate(ctx, tx, project, token, "owner")
	if err != nil {
		return r, err
	}
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM principals WHERE id=? AND project_id=? AND revoked=0`, principal, project).Scan(&n); err != nil {
		return r, err
	}
	if n != 1 {
		return r, ErrNotFound
	}
	if task != "" {
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE id=? AND project_id=?`, task, project).Scan(&n); err != nil {
			return r, err
		}
		if n != 1 {
			return r, ErrNotFound
		}
	}
	var current int
	err = tx.QueryRowContext(ctx, `SELECT revision FROM work_roles WHERE project_id=? AND principal_id=? AND task_id=?`, project, principal, task).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return r, err
	}
	if current != revision {
		return r, ErrConflict
	}
	now := time.Now().UnixMilli()
	if expires != 0 && expires <= now {
		return r, errors.New("expiration must be in the future")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO work_roles(project_id,principal_id,task_id,revision,name,duties,expires_at,updated_at,updated_by) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(project_id,principal_id,task_id) DO UPDATE SET revision=excluded.revision,name=excluded.name,duties=excluded.duties,expires_at=excluded.expires_at,updated_at=excluded.updated_at,updated_by=excluded.updated_by`, project, principal, task, revision+1, name, duties, expires, now, actor.ID)
	if err != nil {
		return r, err
	}
	if err = event(ctx, tx, project, actor.ID, "role.assigned", principal, map[string]any{"task_id": task, "name": name, "revision": revision + 1, "expires_at": expires}); err != nil {
		return r, err
	}
	if err = tx.Commit(); err != nil {
		return r, err
	}
	return WorkRole{PrincipalID: principal, TaskID: task, Revision: revision + 1, Name: name, Duties: duties, ExpiresAt: expires, UpdatedAt: now, UpdatedBy: actor.ID, Active: name != ""}, nil
}
