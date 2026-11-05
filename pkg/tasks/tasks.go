package tasks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tasmirz/gossip/pkg/state"
	"github.com/tasmirz/gossip/pkg/tunnel"
)

// TaskStatus constants
const (
	StatusBacklog    = "backlog"
	StatusTodo       = "todo"
	StatusInProgress = "in_progress"
	StatusBlocked    = "blocked"
	StatusInReview   = "in_review"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
)

// Priority constants
const (
	PriorityLow      = "low"
	PriorityNormal   = "normal"
	PriorityHigh     = "high"
	PriorityCritical = "critical"
)

// TaskNote records progress reports or review remarks.
type TaskNote struct {
	Author    string    `json:"author"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// Task models an actionable unit of work in the shared DAG.
type Task struct {
	ID                 string     `json:"id"`
	Title              string     `json:"title"`
	Description        string     `json:"description"`
	Role               string     `json:"role"`
	AssignedTo         string     `json:"assigned_to,omitempty"`
	Status             string     `json:"status"`
	Priority           string     `json:"priority"`
	BlockedBy          []string   `json:"blocked_by,omitempty"`
	Blocks             []string   `json:"blocks,omitempty"`
	AcceptanceCriteria []string   `json:"acceptance_criteria,omitempty"`
	ResultArtifacts    []string   `json:"result_artifacts,omitempty"`
	Notes              []TaskNote `json:"notes,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// TasksContainer holds the serialized task list in tasks.json.
type TasksContainer struct {
	Version   string          `json:"version"`
	NextSeq   int             `json:"next_seq"`
	Tasks     map[string]Task `json:"tasks"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// Manager coordinates task state, DAG validation, and Kanban updates.
type Manager struct {
	store *state.Store
}

// NewManager creates a task manager backed by state.Store.
func NewManager(store *state.Store) *Manager {
	return &Manager{store: store}
}

func (m *Manager) tasksFilePath() string {
	return filepath.Join(m.store.Dir(), "tasks.json")
}

func (m *Manager) boardFilePath() string {
	return filepath.Join(m.store.Dir(), "BOARD.md")
}

// LoadContainer loads tasks.json or initializes a fresh container.
func (m *Manager) LoadContainer() (*TasksContainer, error) {
	data, err := os.ReadFile(m.tasksFilePath())
	if err != nil {
		if os.IsNotExist(err) {
			return &TasksContainer{
				Version:   "1.0.0",
				NextSeq:   1,
				Tasks:     make(map[string]Task),
				UpdatedAt: time.Now(),
			}, nil
		}
		return nil, err
	}

	var c TasksContainer
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("corrupted tasks.json: %w", err)
	}
	if c.Tasks == nil {
		c.Tasks = make(map[string]Task)
	}
	return &c, nil
}

// SaveContainer saves tasks and regenerates BOARD.md.
func (m *Manager) SaveContainer(c *TasksContainer) error {
	c.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	if err := m.store.AtomicWriteFile("tasks.json", data); err != nil {
		return err
	}

	// Render updated BOARD.md
	boardContent, _ := m.renderBoardContent(c)
	_ = m.store.AtomicWriteFile("BOARD.md", []byte(boardContent))

	return nil
}

// CreateTask instantiates and registers a new task.
func (m *Manager) CreateTask(title, description, role, priority string, blockedBy, criteria []string) (*Task, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	c, err := m.LoadContainer()
	if err != nil {
		return nil, err
	}

	if role == "" {
		role = "worker"
	}
	if priority == "" {
		priority = PriorityNormal
	}

	status := StatusTodo
	// If it has dependencies that are not completed, mark as blocked
	for _, depID := range blockedBy {
		if dep, exists := c.Tasks[depID]; exists && dep.Status != StatusCompleted {
			status = StatusBlocked
			break
		}
	}

	taskID := fmt.Sprintf("task-%d", c.NextSeq)
	c.NextSeq++

	now := time.Now()
	task := Task{
		ID:                 taskID,
		Title:              title,
		Description:        description,
		Role:               role,
		Status:             status,
		Priority:           priority,
		BlockedBy:          blockedBy,
		Blocks:             []string{},
		AcceptanceCriteria: criteria,
		ResultArtifacts:    []string{},
		Notes:              []TaskNote{},
		CreatedAt:          now,
		UpdatedAt:          now,
	}

	// Update inverse dependencies ('blocks') on prerequisite tasks
	for _, depID := range blockedBy {
		if dep, exists := c.Tasks[depID]; exists {
			dep.Blocks = append(dep.Blocks, taskID)
			c.Tasks[depID] = dep
		}
	}

	c.Tasks[taskID] = task

	// Validate DAG for cycles
	if err := m.validateNoCycles(c.Tasks); err != nil {
		return nil, fmt.Errorf("task creation violates DAG invariants: %w", err)
	}

	if err := m.SaveContainer(c); err != nil {
		return nil, err
	}

	return &task, nil
}

// TaskModifications specifies fields to update on an existing task.
type TaskModifications struct {
	Title              *string
	Description        *string
	Role               *string
	Priority           *string
	Status             *string
	AssignedTo         *string
	BlockedBy          *[]string
	AcceptanceCriteria *[]string
	ProgressNote       string
	Artifact           string
}

// ModifyTask updates task details, maintaining DAG consistency and invariants.
func (m *Manager) ModifyTask(taskID string, mods TaskModifications) (*Task, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	c, err := m.LoadContainer()
	if err != nil {
		return nil, err
	}

	task, exists := c.Tasks[taskID]
	if !exists {
		return nil, fmt.Errorf("task %s not found", taskID)
	}

	now := time.Now()

	if mods.Title != nil && *mods.Title != "" {
		task.Title = *mods.Title
	}
	if mods.Description != nil {
		task.Description = *mods.Description
	}
	if mods.Role != nil && *mods.Role != "" {
		task.Role = *mods.Role
	}
	if mods.Priority != nil && *mods.Priority != "" {
		task.Priority = *mods.Priority
	}
	if mods.AssignedTo != nil {
		task.AssignedTo = *mods.AssignedTo
	}
	if mods.AcceptanceCriteria != nil {
		task.AcceptanceCriteria = *mods.AcceptanceCriteria
	}

	// If BlockedBy is modified, adjust inverse references and check for cycles
	if mods.BlockedBy != nil {
		newBlockedBy := *mods.BlockedBy
		// Remove taskID from old dependencies' Blocks lists
		for _, oldDepID := range task.BlockedBy {
			if oldDep, ok := c.Tasks[oldDepID]; ok {
				var updatedBlocks []string
				for _, bID := range oldDep.Blocks {
					if bID != taskID {
						updatedBlocks = append(updatedBlocks, bID)
					}
				}
				oldDep.Blocks = updatedBlocks
				c.Tasks[oldDepID] = oldDep
			}
		}

		// Verify new dependencies exist and no self-blocker
		for _, depID := range newBlockedBy {
			if depID == taskID {
				return nil, fmt.Errorf("task %s cannot block itself", taskID)
			}
			if _, exists := c.Tasks[depID]; !exists {
				return nil, fmt.Errorf("prerequisite task %s does not exist", depID)
			}
		}

		// Add taskID to new dependencies' Blocks lists
		for _, depID := range newBlockedBy {
			dep := c.Tasks[depID]
			found := false
			for _, bID := range dep.Blocks {
				if bID == taskID {
					found = true
					break
				}
			}
			if !found {
				dep.Blocks = append(dep.Blocks, taskID)
				c.Tasks[depID] = dep
			}
		}

		task.BlockedBy = newBlockedBy

		// Cycle check
		c.Tasks[taskID] = task
		if err := m.validateNoCycles(c.Tasks); err != nil {
			return nil, fmt.Errorf("modifying dependencies creates cycle: %w", err)
		}
	}

	// Status transitions
	if mods.Status != nil && *mods.Status != "" {
		newStatus := *mods.Status
		if newStatus == StatusInProgress || newStatus == StatusCompleted {
			for _, depID := range task.BlockedBy {
				if dep, ok := c.Tasks[depID]; ok && dep.Status != StatusCompleted {
					return nil, fmt.Errorf("cannot advance %s: still blocked by incomplete task %s (%s)", taskID, depID, dep.Title)
				}
			}
		}
		task.Status = newStatus
	}

	if mods.ProgressNote != "" {
		author := task.AssignedTo
		if author == "" {
			author = "user"
		}
		task.Notes = append(task.Notes, TaskNote{
			Author:    author,
			Content:   mods.ProgressNote,
			Timestamp: now,
		})
	}

	if mods.Artifact != "" {
		task.ResultArtifacts = append(task.ResultArtifacts, mods.Artifact)
	}

	task.UpdatedAt = now
	c.Tasks[taskID] = task

	// If task is completed, check if downstream tasks can be unblocked
	if task.Status == StatusCompleted {
		for _, downID := range task.Blocks {
			if down, ok := c.Tasks[downID]; ok && down.Status == StatusBlocked {
				allDone := true
				for _, depID := range down.BlockedBy {
					if d, exists := c.Tasks[depID]; !exists || d.Status != StatusCompleted {
						allDone = false
						break
					}
				}
				if allDone {
					down.Status = StatusTodo
					down.UpdatedAt = now
					c.Tasks[downID] = down
				}
			}
		}
	}

	if err := m.SaveContainer(c); err != nil {
		return nil, err
	}

	return &task, nil
}

// UpdateTask transitions task status and updates notes or assignees.
func (m *Manager) UpdateTask(taskID, status, assignedTo, progressNote, artifact string) (*Task, error) {
	mods := TaskModifications{
		ProgressNote: progressNote,
		Artifact:     artifact,
	}
	if status != "" {
		mods.Status = &status
	}
	if assignedTo != "" {
		mods.AssignedTo = &assignedTo
	}
	return m.ModifyTask(taskID, mods)
}

// GetTask returns a single task by ID.
func (m *Manager) GetTask(taskID string) (*Task, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	c, err := m.LoadContainer()
	if err != nil {
		return nil, err
	}

	task, exists := c.Tasks[taskID]
	if !exists {
		return nil, fmt.Errorf("task %s not found", taskID)
	}
	return &task, nil
}

// ListTasks returns tasks with optional filters.
func (m *Manager) ListTasks(statusFilter, roleFilter, assigneeFilter string) ([]Task, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	c, err := m.LoadContainer()
	if err != nil {
		return nil, err
	}

	var result []Task
	for _, t := range c.Tasks {
		if statusFilter != "" && !strings.EqualFold(t.Status, statusFilter) {
			continue
		}
		if roleFilter != "" && !strings.EqualFold(t.Role, roleFilter) {
			continue
		}
		if assigneeFilter != "" && !strings.EqualFold(t.AssignedTo, assigneeFilter) {
			continue
		}
		result = append(result, t)
	}
	return result, nil
}

// validateNoCycles runs topological sort DFS to detect circular dependencies.
func (m *Manager) validateNoCycles(tasks map[string]Task) error {
	visited := make(map[string]int) // 0: unvisited, 1: visiting, 2: visited

	var dfs func(id string) error
	dfs = func(id string) error {
		visited[id] = 1
		for _, depID := range tasks[id].BlockedBy {
			if _, exists := tasks[depID]; !exists {
				continue
			}
			if visited[depID] == 1 {
				return fmt.Errorf("cycle detected involving %s -> %s", id, depID)
			}
			if visited[depID] == 0 {
				if err := dfs(depID); err != nil {
					return err
				}
			}
		}
		visited[id] = 2
		return nil
	}

	for id := range tasks {
		if visited[id] == 0 {
			if err := dfs(id); err != nil {
				return err
			}
		}
	}
	return nil
}

// RenderBoard force re-renders and returns the Markdown Kanban board.
func (m *Manager) RenderBoard() (string, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return "", err
	}
	defer unlock()

	c, err := m.LoadContainer()
	if err != nil {
		return "", err
	}

	return m.renderBoardContent(c)
}

func (m *Manager) renderBoardContent(c *TasksContainer) (string, error) {
	goals, _ := m.store.GetGoalsUnlocked()
	agents, _ := m.store.ListAgentsUnlocked()

	var b strings.Builder
	b.WriteString("# [GOSSIP] Gossip Multi-Agent Task Board\n\n")
	b.WriteString(fmt.Sprintf("*Last Synchronized: %s*\n\n", time.Now().Format(time.RFC1123)))

	// Mandatory Remote Agent & Tunnel Disclosure
	disc := tunnel.ComputeDisclosure(m.store)
	b.WriteString(disc.MarkdownBanner())

	// Goals Section
	b.WriteString("## [GOALS] Project Goals & Milestones\n\n")
	if len(goals) == 0 {
		b.WriteString("_No active goals specified yet. Orchestrator can define goals via `gossip_set_goal`._\n\n")
	} else {
		for _, g := range goals {
			b.WriteString(fmt.Sprintf("### %s [%s]\n", g.Title, strings.ToUpper(g.Status)))
			b.WriteString(fmt.Sprintf("%s\n\n", g.Description))
			if len(g.Milestones) > 0 {
				b.WriteString("**Milestones:**\n")
				for _, ms := range g.Milestones {
					check := " "
					if ms.Completed {
						check = "x"
					}
					b.WriteString(fmt.Sprintf("- [%s] **%s**: %s\n", check, ms.Title, ms.Description))
				}
				b.WriteString("\n")
			}
		}
	}

	// Kanban Columns
	groups := map[string][]Task{
		StatusInProgress: {},
		StatusBlocked:    {},
		StatusTodo:       {},
		StatusInReview:   {},
		StatusCompleted:  {},
		StatusBacklog:    {},
	}

	for _, t := range c.Tasks {
		groups[t.Status] = append(groups[t.Status], t)
	}

	renderColumn := func(title, emoji, status string) {
		items := groups[status]
		b.WriteString(fmt.Sprintf("## %s %s (%d)\n\n", emoji, title, len(items)))
		if len(items) == 0 {
			b.WriteString("_No tasks in this lane._\n\n")
			return
		}
		for _, t := range items {
			assigned := t.AssignedTo
			if assigned == "" {
				assigned = fmt.Sprintf("Role: %s", t.Role)
			}
			b.WriteString(fmt.Sprintf("### `[%s]` %s (Priority: %s | Assignee: %s)\n", t.ID, t.Title, strings.ToUpper(t.Priority), assigned))
			b.WriteString(fmt.Sprintf("%s\n\n", t.Description))
			if len(t.BlockedBy) > 0 {
				b.WriteString(fmt.Sprintf("- **Blocked by:** `%s`\n", strings.Join(t.BlockedBy, "`, `")))
			}
			if len(t.AcceptanceCriteria) > 0 {
				b.WriteString("- **Acceptance Criteria:**\n")
				for _, crit := range t.AcceptanceCriteria {
					b.WriteString(fmt.Sprintf("  - %s\n", crit))
				}
			}
			if len(t.ResultArtifacts) > 0 {
				b.WriteString(fmt.Sprintf("- **Artifacts:** `%s`\n", strings.Join(t.ResultArtifacts, "`, `")))
			}
			b.WriteString("\n")
		}
	}

	renderColumn("In Progress", "[>>]", StatusInProgress)
	renderColumn("Blocked", "[!]", StatusBlocked)
	renderColumn("Ready / Todo", "[ ]", StatusTodo)
	renderColumn("In Review", "[?]", StatusInReview)
	renderColumn("Completed", "[OK]", StatusCompleted)
	renderColumn("Backlog", "[-]", StatusBacklog)

	// Agent Workload Section
	b.WriteString("## [AGENTS] Active Agents & Workload\n\n")
	if len(agents) == 0 {
		b.WriteString("_No active agents currently registered._\n\n")
	} else {
		b.WriteString("| Agent ID | Role | Status | Scope | Current Task | Last Heartbeat |\n")
		b.WriteString("| :--- | :--- | :--- | :--- | :--- | :--- |\n")
		for _, a := range agents {
			curTask := a.CurrentTaskID
			if curTask == "" {
				curTask = "-"
			}
			scope := "LOCAL"
			if a.IsRemote || a.RemoteEndpoint != "" {
				if a.RemoteEndpoint != "" {
					scope = fmt.Sprintf("REMOTE (%s)", a.RemoteEndpoint)
				} else {
					scope = "REMOTE"
				}
			}
			b.WriteString(fmt.Sprintf("| `%s` | `%s` | **%s** | `%s` | `%s` | %s |\n",
				a.ID, a.Role, a.Status, scope, curTask, a.LastHeartbeat.Format("15:04:05")))
		}
		b.WriteString("\n")
	}

	// Deliberations, Debates & Red Team Probes Section
	delibPath := filepath.Join(m.store.Dir(), "deliberations.json")
	if delibBytes, err := os.ReadFile(delibPath); err == nil {
		var ds struct {
			Debates map[string]struct {
				ID       string         `json:"id"`
				Title    string         `json:"title"`
				Proposer string         `json:"proposer"`
				Role     string         `json:"role"`
				Status   string         `json:"status"`
				Votes    map[string]any `json:"votes"`
			} `json:"debates"`
			Reviews map[string]struct {
				ID         string `json:"id"`
				TargetType string `json:"target_type"`
				TargetID   string `json:"target_id"`
				ReviewerID string `json:"reviewer_id"`
				Status     string `json:"status"`
			} `json:"reviews"`
			Probes map[string]struct {
				ID             string `json:"id"`
				Target         string `json:"target"`
				ThreatCategory string `json:"threat_category"`
				AttackerID     string `json:"attacker_id"`
				Status         string `json:"status"`
			} `json:"probes"`
		}
		if json.Unmarshal(delibBytes, &ds) == nil {
			if len(ds.Debates) > 0 {
				b.WriteString("## [DEBATES] Active Debates & Voting Sessions\n\n")
				b.WriteString("| Debate ID | Title | Proposer | Status | Votes Cast |\n")
				b.WriteString("| :--- | :--- | :--- | :--- | :--- |\n")
				for _, d := range ds.Debates {
					b.WriteString(fmt.Sprintf("| `%s` | %s | `%s` (%s) | **%s** | %d |\n",
						d.ID, d.Title, d.Proposer, d.Role, strings.ToUpper(d.Status), len(d.Votes)))
				}
				b.WriteString("\n")
			}
			if len(ds.Probes) > 0 {
				b.WriteString("## [RED TEAM] Security & Stress Probes\n\n")
				b.WriteString("| Probe ID | Target | Threat Vector | Attacker | Status |\n")
				b.WriteString("| :--- | :--- | :--- | :--- | :--- |\n")
				for _, p := range ds.Probes {
					b.WriteString(fmt.Sprintf("| `%s` | `%s` | `%s` | `%s` | **%s** |\n",
						p.ID, p.Target, p.ThreatCategory, p.AttackerID, strings.ToUpper(p.Status)))
				}
				b.WriteString("\n")
			}
		}
	}

	return b.String(), nil
}
