package coord

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// LocalProjectStatus describes a local coordinator project and whether its
// explicit device-owner mapping resolves cleanly without picking an owner.
type LocalProjectStatus struct {
	ProjectID      string      `json:"project_id"`
	ProjectName    string      `json:"project_name"`
	Status         string      `json:"status"` // "ready", "ambiguous_owner", "missing_owner"
	Owner          *Principal  `json:"owner,omitempty"`
	Candidates     []Principal `json:"candidates,omitempty"`
	PlanRevision   int         `json:"plan_revision"`
	LastActivityAt int64       `json:"last_activity_at,omitempty"`
}

// LocalDashboardBackend provides explicit device-owner project enumeration,
// owner repair selection, and local checkout matching for the loopback UI.
type LocalDashboardBackend interface {
	ListLocalDashboardProjects(ctx context.Context) ([]LocalProjectStatus, error)
	SelectLocalDashboardOwner(ctx context.Context, projectID, principalID string) (Principal, error)
	FindLocalProjectsByCheckout(ctx context.Context, localPath string, remoteHints []string) ([]ProjectSuggestion, error)
}

// DashboardQueryBackend exposes read-only project queries (plan history,
// authorized text search, Graphify inspection, and personal checklists) for
// dashboard routes.
type DashboardQueryBackend interface {
	ListPlanHistory(ctx context.Context, project, token string) ([]Plan, error)
	ListPlanThreads(ctx context.Context, project, token string) ([]PlanThread, error)
	CreatePlan(ctx context.Context, project, token, title, body string, expectedCurrent int) (Plan, error)
	SearchProject(ctx context.Context, project, token, query string) (ProjectSearchResult, error)
	InspectGraphify(ctx context.Context, project, token, query string) (GraphifyInspection, error)
	ListPersonalChecklists(ctx context.Context, project, token string) (PersonalChecklistResponse, error)
}

type ProjectSearchResult struct {
	Context  []ContextHit `json:"context,omitempty"`
	Query    string       `json:"query"`
	Tasks    []Task       `json:"tasks"`
	Plans    []Plan       `json:"plans"`
	Messages []Message    `json:"messages"`
	Events   []Event      `json:"events"`
}

type GraphifyNodeHit struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Label      string `json:"label"`
	Summary    string `json:"summary"`
	SourcePath string `json:"source_path,omitempty"`
}

type GraphifyInspection struct {
	Status       string            `json:"status"` // "ready", "stale", "unavailable"
	Reason       string            `json:"reason,omitempty"`
	SourcePath   string            `json:"source_path,omitempty"`
	UpdatedAt    string            `json:"updated_at,omitempty"`
	NodeCount    int               `json:"node_count"`
	EdgeCount    int               `json:"edge_count"`
	MatchedNodes []GraphifyNodeHit `json:"matched_nodes"`
}

type PersonalChecklistEntry struct {
	ID            string `json:"id"`
	AgentID       string `json:"agent_id"`
	Title         string `json:"title"`
	Status        string `json:"status"`
	Priority      string `json:"priority"`
	ContextTaskID string `json:"context_task_id,omitempty"`
	Notes         string `json:"notes,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
	Source        string `json:"source"`
}

type PersonalChecklistResponse struct {
	Available bool                     `json:"available"`
	Reason    string                   `json:"reason,omitempty"`
	Items     []PersonalChecklistEntry `json:"items"`
}

func (s *Store) verifyLocalAuthorityContext(ctx context.Context) error {
	if auth, ok := ctx.Value(localOwnerAuthorityKey{}).(localOwnerAuthority); ok && auth.homeID != "" && auth.homeID != s.instanceID {
		return ErrUnauthorized
	}
	return nil
}

// ListLocalDashboardProjects returns all coordinator projects ordered by
// recent activity along with their explicit local owner mapping or repair state.
func (s *Store) ListLocalDashboardProjects(ctx context.Context) ([]LocalProjectStatus, error) {
	if err := s.verifyLocalAuthorityContext(ctx); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		SELECT j.id, j.name, j.plan_revision,
		       COALESCE(l.principal_id, ''),
		       COALESCE(p.id, ''), COALESCE(p.name, ''), COALESCE(p.role, ''), COALESCE(p.revoked, 1),
		       COALESCE(
		         NULLIF((SELECT MAX(e.created_at) FROM events e WHERE e.project_id=j.id), 0),
		         l.updated_at,
		         0
		       ) AS last_activity_at
		FROM projects j
		LEFT JOIN local_dashboard_owners l ON l.project_id = j.id
		LEFT JOIN principals p ON p.project_id = j.id AND p.id = l.principal_id
		ORDER BY last_activity_at DESC, j.name ASC, j.id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []LocalProjectStatus
	var needsCandidates []int
	for rows.Next() {
		var item LocalProjectStatus
		var mappedID, pID, pName, pRole string
		var revoked int
		if err = rows.Scan(&item.ProjectID, &item.ProjectName, &item.PlanRevision, &mappedID, &pID, &pName, &pRole, &revoked, &item.LastActivityAt); err != nil {
			return nil, err
		}
		if mappedID != "" && pID != "" && pRole == "owner" && revoked == 0 {
			item.Status = "ready"
			item.Owner = &Principal{
				ID:          pID,
				Name:        pName,
				Role:        pRole,
				ProjectID:   item.ProjectID,
				ProjectName: item.ProjectName,
			}
		} else {
			needsCandidates = append(needsCandidates, len(out))
		}
		out = append(out, item)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}

	for _, idx := range needsCandidates {
		item := &out[idx]
		cRows, cErr := tx.QueryContext(ctx, `
			SELECT id, name, role
			FROM principals
			WHERE project_id=? AND role='owner' AND revoked=0
			ORDER BY name ASC, id ASC`, item.ProjectID)
		if cErr != nil {
			return nil, cErr
		}
		var candidates []Principal
		for cRows.Next() {
			var p Principal
			if scanErr := cRows.Scan(&p.ID, &p.Name, &p.Role); scanErr != nil {
				cRows.Close()
				return nil, scanErr
			}
			p.ProjectID = item.ProjectID
			p.ProjectName = item.ProjectName
			candidates = append(candidates, p)
		}
		if err = cRows.Err(); err != nil {
			cRows.Close()
			return nil, err
		}
		cRows.Close()
		if len(candidates) == 0 {
			item.Status = "missing_owner"
		} else {
			item.Status = "ambiguous_owner"
			item.Candidates = candidates
		}
	}
	if out == nil {
		out = []LocalProjectStatus{}
	}
	return out, nil
}

// SelectLocalDashboardOwner explicitly assigns the device-owner principal for
// a project that needs local administrative repair.
func (s *Store) SelectLocalDashboardOwner(ctx context.Context, projectID, principalID string) (Principal, error) {
	if err := s.verifyLocalAuthorityContext(ctx); err != nil {
		return Principal{}, err
	}
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(principalID) == "" {
		return Principal{}, errors.New("project_id and principal_id are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Principal{}, err
	}
	defer tx.Rollback()

	var p Principal
	err = tx.QueryRowContext(ctx, `
		SELECT p.id, p.name, p.role, j.id, j.name
		FROM principals p
		JOIN projects j ON j.id = p.project_id
		WHERE p.project_id=? AND p.id=? AND p.role='owner' AND p.revoked=0`, projectID, principalID).
		Scan(&p.ID, &p.Name, &p.Role, &p.ProjectID, &p.ProjectName)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, ErrUnauthorized
	}
	if err != nil {
		return Principal{}, err
	}
	now := time.Now().UnixMilli()
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO local_dashboard_owners(project_id, principal_id, updated_at)
		VALUES(?,?,?)
		ON CONFLICT(project_id) DO UPDATE SET principal_id=excluded.principal_id, updated_at=excluded.updated_at`,
		projectID, principalID, now); err != nil {
		return Principal{}, err
	}
	if err = event(ctx, tx, projectID, principalID, "local_owner.selected", principalID, map[string]string{"principal_id": principalID}); err != nil {
		return Principal{}, err
	}
	if err = tx.Commit(); err != nil {
		return Principal{}, err
	}
	return p, nil
}

// FindLocalProjectsByCheckout checks whether any local project already tracks
// the inspected checkout path or any of its normalized Git remotes.
func (s *Store) FindLocalProjectsByCheckout(ctx context.Context, localPath string, remoteHints []string) ([]ProjectSuggestion, error) {
	if err := s.verifyLocalAuthorityContext(ctx); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	seen := map[string]bool{}
	out := []ProjectSuggestion{}
	add := func(sg ProjectSuggestion) {
		key := sg.ProjectID + ":" + sg.RepositoryID
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, sg)
	}

	if clean := strings.TrimSpace(localPath); clean != "" {
		rows, err := tx.QueryContext(ctx, `
			SELECT DISTINCT j.id, j.name, r.id, r.name
			FROM workspaces w
			JOIN projects j ON j.id = w.project_id
			JOIN repositories r ON r.id = w.repository_id AND r.project_id = w.project_id
			WHERE w.local_path = ?
			ORDER BY j.name, r.name`, clean)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var sg ProjectSuggestion
			if err = rows.Scan(&sg.ProjectID, &sg.ProjectName, &sg.RepositoryID, &sg.RepositoryName); err != nil {
				rows.Close()
				return nil, err
			}
			add(sg)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}

	for _, hint := range remoteHints {
		rows, err := tx.QueryContext(ctx, `
			SELECT DISTINCT j.id, j.name, r.id, r.name
			FROM repository_remote_hints h
			JOIN projects j ON j.id = h.project_id
			JOIN repositories r ON r.id = h.repository_id AND r.project_id = h.project_id
			WHERE h.remote_key = ?
			ORDER BY j.name, r.name`, hint)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var sg ProjectSuggestion
			if err = rows.Scan(&sg.ProjectID, &sg.ProjectName, &sg.RepositoryID, &sg.RepositoryName); err != nil {
				rows.Close()
				return nil, err
			}
			add(sg)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// ListPlanHistory returns all published plan revisions for an authorized caller.
func (s *Store) ListPlanHistory(ctx context.Context, project, token string) ([]Plan, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	actor, err := authorizeProjectRead(ctx, tx, project, token)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT p.revision, p.body, p.author, p.created_at,
		       EXISTS(SELECT 1 FROM plan_acknowledgments a WHERE a.project_id=p.project_id AND a.principal_id=? AND a.revision=p.revision),
		       CASE WHEN EXISTS(SELECT 1 FROM plan_completions c WHERE c.project_id=p.project_id AND c.revision=p.revision) THEN 'done'
		            WHEN p.revision=(SELECT MAX(latest.revision) FROM plans latest WHERE latest.project_id=p.project_id AND latest.plan_id=p.plan_id) THEN 'active'
		            ELSE 'superseded' END,p.plan_id,COALESCE(t.title,'')
		FROM plans p JOIN projects j ON j.id=p.project_id LEFT JOIN plan_threads t ON t.id=p.plan_id
		WHERE p.project_id=?
		ORDER BY p.revision DESC
		LIMIT 50`, actor.ID, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Plan{}
	for rows.Next() {
		var p Plan
		if err = rows.Scan(&p.Revision, &p.Body, &p.Author, &p.CreatedAt, &p.Acknowledged, &p.Status, &p.PlanID, &p.PlanTitle); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SearchProject runs an authorized, participant-filtered text search across
// tasks, plan revisions, messages, and events inside one project.
func (s *Store) SearchProject(ctx context.Context, project, token, query string) (ProjectSearchResult, error) {
	q := strings.TrimSpace(query)
	res := ProjectSearchResult{
		Query:    q,
		Tasks:    []Task{},
		Plans:    []Plan{},
		Messages: []Message{},
		Events:   []Event{},
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	actor, err := authorizeProjectRead(ctx, tx, project, token)
	if err != nil {
		return res, err
	}
	if q == "" {
		return res, nil
	}
	lq := strings.ToLower(q)

	taskRows, err := tx.QueryContext(ctx, `
		SELECT t.id, t.project_id, t.plan_revision, t.title, t.description, t.kind, t.report_contract, t.status,
		       COALESCE(t.owner,''), t.attempt, t.lease_until, COALESCE(a.principal_id,''), COALESCE(b.reason,'')
		FROM tasks t
		LEFT JOIN task_assignments a ON a.task_id=t.id
		LEFT JOIN task_blockers b ON b.task_id=t.id
		WHERE t.project_id=? AND (
			INSTR(LOWER(t.id), ?) > 0 OR
			INSTR(LOWER(t.title), ?) > 0 OR
			INSTR(LOWER(t.description), ?) > 0
		)
		ORDER BY t.created_at DESC
		LIMIT 40`, project, lq, lq, lq)
	if err != nil {
		return res, err
	}
	for taskRows.Next() {
		var t Task
		var contractJSON string
		if err = taskRows.Scan(&t.ID, &t.ProjectID, &t.PlanRevision, &t.Title, &t.Description, &t.Kind, &contractJSON, &t.Status, &t.Owner, &t.Attempt, &t.LeaseUntil, &t.AssignedTo, &t.BlockerReason); err != nil {
			taskRows.Close()
			return res, err
		}
		_ = json.Unmarshal([]byte(contractJSON), &t.Contract)
		res.Tasks = append(res.Tasks, t)
	}
	if err = taskRows.Err(); err != nil {
		taskRows.Close()
		return res, err
	}
	taskRows.Close()

	planRows, err := tx.QueryContext(ctx, `
		SELECT p.revision, p.body, p.author, p.created_at,
		       EXISTS(SELECT 1 FROM plan_acknowledgments a WHERE a.project_id=p.project_id AND a.principal_id=? AND a.revision=p.revision),
		       CASE WHEN EXISTS(SELECT 1 FROM plan_completions c WHERE c.project_id=p.project_id AND c.revision=p.revision) THEN 'done'
		            WHEN p.revision=(SELECT MAX(latest.revision) FROM plans latest WHERE latest.project_id=p.project_id AND latest.plan_id=p.plan_id) THEN 'active'
		            ELSE 'superseded' END,p.plan_id,COALESCE(t.title,'')
		FROM plans p LEFT JOIN plan_threads t ON t.project_id=p.project_id AND t.id=p.plan_id
		WHERE p.project_id=? AND INSTR(LOWER(p.body), ?) > 0
		ORDER BY p.revision DESC
		LIMIT 20`, actor.ID, project, lq)
	if err != nil {
		return res, err
	}
	for planRows.Next() {
		var p Plan
		if err = planRows.Scan(&p.Revision, &p.Body, &p.Author, &p.CreatedAt, &p.Acknowledged, &p.Status, &p.PlanID, &p.PlanTitle); err != nil {
			planRows.Close()
			return res, err
		}
		res.Plans = append(res.Plans, p)
	}
	if err = planRows.Err(); err != nil {
		planRows.Close()
		return res, err
	}
	planRows.Close()

	msgRows, err := tx.QueryContext(ctx, `
		SELECT seq, id, sender, COALESCE(recipient,''), thread_id, subject, body, created_at
		FROM messages
		WHERE project_id=?
		  AND `+visibleMessageSQL("messages")+`
		  AND (INSTR(LOWER(body), ?) > 0 OR INSTR(LOWER(subject), ?) > 0)
		ORDER BY seq DESC
		LIMIT 40`, project, actor.Role, actor.ID, actor.ID, actor.ID, actor.ID, lq, lq)
	if err != nil {
		return res, err
	}
	for msgRows.Next() {
		var m Message
		if err = msgRows.Scan(&m.Sequence, &m.ID, &m.Sender, &m.Recipient, &m.ThreadID, &m.Subject, &m.Body, &m.CreatedAt); err != nil {
			msgRows.Close()
			return res, err
		}
		res.Messages = append(res.Messages, m)
	}
	if err = msgRows.Err(); err != nil {
		msgRows.Close()
		return res, err
	}
	msgRows.Close()

	evtRows, err := tx.QueryContext(ctx, `
		SELECT e.seq, e.actor, e.kind, e.entity_id, e.payload, e.created_at
		FROM events e
		WHERE e.project_id=?
		  AND (?='owner' OR (e.kind NOT LIKE 'certificate.%' AND e.kind<>'dm.inspected'))
		  AND (e.kind NOT IN ('message.sent','message.acknowledged') OR EXISTS (
			SELECT 1 FROM messages m WHERE m.id=e.entity_id AND m.project_id=e.project_id
			AND `+visibleMessageSQL("m")+`))
		  AND (INSTR(LOWER(e.kind), ?) > 0 OR INSTR(LOWER(e.entity_id), ?) > 0 OR INSTR(LOWER(e.payload), ?) > 0)
		ORDER BY e.seq DESC
		LIMIT 30`, project, actor.Role, actor.Role, actor.ID, actor.ID, actor.ID, actor.ID, lq, lq, lq)
	if err != nil {
		return res, err
	}
	for evtRows.Next() {
		var e Event
		var payload string
		if err = evtRows.Scan(&e.Sequence, &e.Actor, &e.Kind, &e.EntityID, &payload, &e.CreatedAt); err != nil {
			evtRows.Close()
			return res, err
		}
		e.Payload = json.RawMessage(payload)
		res.Events = append(res.Events, e)
	}
	if err = evtRows.Err(); err != nil {
		evtRows.Close()
		return res, err
	}
	evtRows.Close()
	return res, nil
}

// InspectGraphify inspects any persisted Graphify index in the project's
// registered local workspaces without synthesizing fake data when unavailable.
func (s *Store) InspectGraphify(ctx context.Context, project, token, query string) (GraphifyInspection, error) {
	out := GraphifyInspection{
		Status:       "unavailable",
		Reason:       "No Graphify index file was found in the project's local workspaces.",
		MatchedNodes: []GraphifyNodeHit{},
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err = authorizeProjectRead(ctx, tx, project, token); err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT local_path, dirty, updated_at FROM workspaces WHERE project_id=? AND local_path<>'' ORDER BY updated_at DESC`, project)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	type wsInfo struct {
		path      string
		dirty     int
		updatedAt int64
	}
	var workspaces []wsInfo
	for rows.Next() {
		var w wsInfo
		if err = rows.Scan(&w.path, &w.dirty, &w.updatedAt); err != nil {
			return out, err
		}
		workspaces = append(workspaces, w)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	rows.Close()

	candidates := []string{}
	for _, w := range workspaces {
		candidates = append(candidates,
			filepath.Join(w.path, ".gossip", "graph.json"),
			filepath.Join(w.path, "graphify-out", "graph.json"),
		)
	}
	for i, path := range candidates {
		info, statErr := os.Stat(path)
		if statErr != nil || info.IsDir() {
			continue
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			out.Status = "unavailable"
			out.SourcePath = path
			out.Reason = "Graphify index exists but could not be read: " + readErr.Error()
			return out, nil
		}
		var raw struct {
			Nodes map[string]struct {
				ID         string `json:"id"`
				Type       string `json:"type"`
				Label      string `json:"label"`
				Summary    string `json:"summary"`
				SourcePath string `json:"source_path"`
			} `json:"nodes"`
			Edges     map[string]json.RawMessage `json:"edges"`
			UpdatedAt string                     `json:"updated_at"`
		}
		if jsonErr := json.Unmarshal(data, &raw); jsonErr != nil {
			out.Status = "unavailable"
			out.SourcePath = path
			out.Reason = "Graphify index JSON is malformed: " + jsonErr.Error()
			return out, nil
		}
		ws := workspaces[i/2]
		out.SourcePath = path
		out.NodeCount = len(raw.Nodes)
		out.EdgeCount = len(raw.Edges)
		out.UpdatedAt = raw.UpdatedAt
		out.Status = "ready"
		if ws.dirty != 0 {
			out.Status = "stale"
			out.Reason = "Workspace has uncommitted changes since the Graphify index was updated."
		} else if parsed, pErr := time.Parse(time.RFC3339, raw.UpdatedAt); pErr == nil && ws.updatedAt > parsed.UnixMilli() {
			out.Status = "stale"
			out.Reason = "Workspace snapshot is newer than the Graphify index timestamp."
		}
		lq := strings.ToLower(strings.TrimSpace(query))
		for id, n := range raw.Nodes {
			if n.ID == "" {
				n.ID = id
			}
			if lq == "" || strings.Contains(strings.ToLower(n.Label), lq) || strings.Contains(strings.ToLower(n.Summary), lq) || strings.Contains(strings.ToLower(n.SourcePath), lq) {
				out.MatchedNodes = append(out.MatchedNodes, GraphifyNodeHit{
					ID:         n.ID,
					Type:       n.Type,
					Label:      n.Label,
					Summary:    n.Summary,
					SourcePath: n.SourcePath,
				})
			}
		}
		sort.Slice(out.MatchedNodes, func(a, b int) bool {
			return out.MatchedNodes[a].Label < out.MatchedNodes[b].Label
		})
		if len(out.MatchedNodes) > 40 {
			out.MatchedNodes = out.MatchedNodes[:40]
		}
		return out, nil
	}
	return out, nil
}

// ListPersonalChecklists loads any persisted per-agent personal checklist
// items from local workspaces (.gossip/personal_todos.json) or imported archives.
func (s *Store) ListPersonalChecklists(ctx context.Context, project, token string) (PersonalChecklistResponse, error) {
	out := PersonalChecklistResponse{
		Available: false,
		Reason:    "No local workspace personal checklist (.gossip/personal_todos.json) or imported checklist records were found for this project.",
		Items:     []PersonalChecklistEntry{},
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err = authorizeProjectRead(ctx, tx, project, token); err != nil {
		return out, err
	}

	wsRows, err := tx.QueryContext(ctx, `SELECT DISTINCT local_path FROM workspaces WHERE project_id=? AND local_path<>'' ORDER BY updated_at DESC`, project)
	if err != nil {
		return out, err
	}
	var paths []string
	for wsRows.Next() {
		var p string
		if err = wsRows.Scan(&p); err != nil {
			wsRows.Close()
			return out, err
		}
		paths = append(paths, p)
	}
	if err = wsRows.Err(); err != nil {
		wsRows.Close()
		return out, err
	}
	wsRows.Close()

	for _, root := range paths {
		todoPath := filepath.Join(root, ".gossip", "personal_todos.json")
		data, readErr := os.ReadFile(todoPath)
		if readErr != nil {
			continue
		}
		var container struct {
			Todos map[string][]struct {
				ID            string `json:"id"`
				AgentID       string `json:"agent_id"`
				Title         string `json:"title"`
				Status        string `json:"status"`
				Priority      string `json:"priority"`
				ContextTaskID string `json:"context_task_id"`
				Notes         string `json:"notes"`
				UpdatedAt     string `json:"updated_at"`
			} `json:"todos"`
		}
		if json.Unmarshal(data, &container) != nil {
			continue
		}
		out.Available = true
		out.Reason = ""
		for agentID, list := range container.Todos {
			for _, item := range list {
				if item.AgentID == "" {
					item.AgentID = agentID
				}
				out.Items = append(out.Items, PersonalChecklistEntry{
					ID:            item.ID,
					AgentID:       item.AgentID,
					Title:         item.Title,
					Status:        item.Status,
					Priority:      item.Priority,
					ContextTaskID: item.ContextTaskID,
					Notes:         item.Notes,
					UpdatedAt:     item.UpdatedAt,
					Source:        filepath.Base(root) + "/.gossip/personal_todos.json",
				})
			}
		}
	}

	legRows, err := tx.QueryContext(ctx, `SELECT record_id, payload FROM legacy_records WHERE project_id=? AND record_type='personal_todo' ORDER BY created_at DESC LIMIT 100`, project)
	if err == nil {
		for legRows.Next() {
			var rid, payload string
			if legRows.Scan(&rid, &payload) == nil {
				var item struct {
					ID            string `json:"id"`
					AgentID       string `json:"agent_id"`
					Title         string `json:"title"`
					Status        string `json:"status"`
					Priority      string `json:"priority"`
					ContextTaskID string `json:"context_task_id"`
					Notes         string `json:"notes"`
					UpdatedAt     string `json:"updated_at"`
				}
				if json.Unmarshal([]byte(payload), &item) == nil && item.Title != "" {
					if item.ID == "" {
						item.ID = rid
					}
					out.Available = true
					out.Reason = ""
					out.Items = append(out.Items, PersonalChecklistEntry{
						ID:            item.ID,
						AgentID:       item.AgentID,
						Title:         item.Title,
						Status:        item.Status,
						Priority:      item.Priority,
						ContextTaskID: item.ContextTaskID,
						Notes:         item.Notes,
						UpdatedAt:     item.UpdatedAt,
						Source:        "legacy_import",
					})
				}
			}
		}
		legRows.Close()
	}
	sort.Slice(out.Items, func(i, j int) bool {
		if out.Items[i].AgentID != out.Items[j].AgentID {
			return out.Items[i].AgentID < out.Items[j].AgentID
		}
		return out.Items[i].Title < out.Items[j].Title
	})
	return out, nil
}
