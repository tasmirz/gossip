package tasks

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// SystemTodoItem models an actionable system-wide backlog item.
type SystemTodoItem struct {
	ID          string    `json:"id"`
	Category    string    `json:"category"` // "dag_task", "milestone", "sop_gate", "vulnerability"
	Title       string    `json:"title"`
	Priority    string    `json:"priority"` // critical, high, normal, low
	Status      string    `json:"status"`
	TargetRole  string    `json:"target_role,omitempty"`
	AssignedTo  string    `json:"assigned_to,omitempty"`
	ContextInfo string    `json:"context_info,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// SystemTodoList aggregates all actionable work across project subsystems.
type SystemTodoList struct {
	ProjectName              string           `json:"project_name"`
	TotalActionItems         int              `json:"total_action_items"`
	ReadyTasksCount          int              `json:"ready_tasks_count"`
	PendingMilestonesCount   int              `json:"pending_milestones_count"`
	PendingSOPGatesCount     int              `json:"pending_sop_gates_count"`
	OpenVulnerabilitiesCount int              `json:"open_vulnerabilities_count"`
	Items                    []SystemTodoItem `json:"items"`
	GeneratedAt              time.Time        `json:"generated_at"`
}

// GetSystemTodoList aggregates unassigned ready tasks, pending milestones,
// incomplete SOP phase gates, and open security vulnerabilities into a unified backlog.
func (m *Manager) GetSystemTodoList() (*SystemTodoList, error) {
	container, err := m.LoadContainer()
	if err != nil {
		return nil, err
	}

	var items []SystemTodoItem
	readyTasksCount := 0

	// 1. DAG Ready Tasks
	taskStatus := make(map[string]string)
	for _, t := range container.Tasks {
		taskStatus[t.ID] = t.Status
	}

	for _, t := range container.Tasks {
		if t.Status == StatusTodo || t.Status == "ready" {
			allBlockersMet := true
			for _, b := range t.BlockedBy {
				if taskStatus[b] != StatusCompleted {
					allBlockersMet = false
					break
				}
			}
			if allBlockersMet {
				readyTasksCount++
				items = append(items, SystemTodoItem{
					ID:          t.ID,
					Category:    "dag_task",
					Title:       t.Title,
					Priority:    t.Priority,
					Status:      t.Status,
					TargetRole:  t.Role,
					AssignedTo:  t.AssignedTo,
					ContextInfo: fmt.Sprintf("Prerequisites met. Ready for claim by role: %s", t.Role),
					CreatedAt:   t.CreatedAt,
				})
			}
		}
	}

	// 2. Pending Strategic Milestones
	pendingMilestonesCount := 0
	goals, _ := m.store.GetGoals()
	for _, g := range goals {
		for _, ms := range g.Milestones {
			if !ms.Completed {
				pendingMilestonesCount++
				items = append(items, SystemTodoItem{
					ID:          fmt.Sprintf("ms-%s", ms.ID),
					Category:    "milestone",
					Title:       fmt.Sprintf("Goal '%s': %s", g.Title, ms.Title),
					Priority:    PriorityHigh,
					Status:      "pending",
					TargetRole:  "orchestrator",
					ContextInfo: fmt.Sprintf("Parent Goal: %s", g.Title),
					CreatedAt:   g.CreatedAt,
				})
			}
		}
	}

	// 3. Pending SOP Phase Gates
	pendingSOPCount := 0
	sopData, err := m.store.ReadFile("sop.json")
	if err == nil {
		var sopStore struct {
			Workflows map[string]struct {
				ID                string `json:"id"`
				Title             string `json:"title"`
				Status            string `json:"status"`
				CurrentStageIndex int    `json:"current_stage_index"`
				Stages            []struct {
					ID           string `json:"id"`
					Name         string `json:"name"`
					RequiredRole string `json:"required_role"`
					ArtifactType string `json:"artifact_type"`
					Status       string `json:"status"`
				} `json:"stages"`
			} `json:"workflows"`
		}
		if json.Unmarshal(sopData, &sopStore) == nil {
			for _, wf := range sopStore.Workflows {
				if wf.Status == "active" && wf.CurrentStageIndex < len(wf.Stages) {
					stage := wf.Stages[wf.CurrentStageIndex]
					if stage.Status != "completed" {
						pendingSOPCount++
						items = append(items, SystemTodoItem{
							ID:          fmt.Sprintf("sop-%s-%s", wf.ID, stage.ID),
							Category:    "sop_gate",
							Title:       fmt.Sprintf("SOP '%s': Deliver %s", wf.Title, stage.ArtifactType),
							Priority:    PriorityCritical,
							Status:      "pending_artifact",
							TargetRole:  stage.RequiredRole,
							ContextInfo: fmt.Sprintf("Workflow %s, Stage: %s (%s)", wf.ID, stage.Name, stage.ArtifactType),
							CreatedAt:   time.Now(),
						})
					}
				}
			}
		}
	}

	// 4. Open STRIDE Security Vulnerabilities
	openVulnCount := 0
	delibData, err := m.store.ReadFile("deliberations.json")
	if err == nil {
		var delibStore struct {
			Probes []struct {
				ID             string    `json:"id"`
				Target         string    `json:"target"`
				ThreatCategory string    `json:"threat_category"`
				Status         string    `json:"status"`
				CreatedAt      time.Time `json:"created_at"`
			} `json:"probes"`
		}
		if json.Unmarshal(delibData, &delibStore) == nil {
			for _, p := range delibStore.Probes {
				if p.Status == "vulnerable" {
					openVulnCount++
					items = append(items, SystemTodoItem{
						ID:          fmt.Sprintf("vuln-%s", p.ID),
						Category:    "vulnerability",
						Title:       fmt.Sprintf("Mitigate %s in %s", p.ThreatCategory, p.Target),
						Priority:    PriorityCritical,
						Status:      "vulnerable",
						TargetRole:  "security_specialist",
						ContextInfo: fmt.Sprintf("STRIDE Threat: %s on target %s", p.ThreatCategory, p.Target),
						CreatedAt:   p.CreatedAt,
					})
				}
			}
		}
	}

	return &SystemTodoList{
		ProjectName:              filepath.Base(m.store.Dir()),
		TotalActionItems:         len(items),
		ReadyTasksCount:          readyTasksCount,
		PendingMilestonesCount:   pendingMilestonesCount,
		PendingSOPGatesCount:     pendingSOPCount,
		OpenVulnerabilitiesCount: openVulnCount,
		Items:                    items,
		GeneratedAt:              time.Now(),
	}, nil
}

// RenderSystemTodoListMarkdown formats the system backlog into a clean Markdown table.
func (s *SystemTodoList) RenderMarkdown() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# 📋 System To-Do Backlog: %s\n\n", s.ProjectName))
	b.WriteString(fmt.Sprintf("*Synchronized: %s | Total Action Items: %d*\n\n",
		s.GeneratedAt.Format(time.RFC1123), s.TotalActionItems))

	b.WriteString("| Metric | Count |\n| :--- | :---: |\n")
	b.WriteString(fmt.Sprintf("| 📋 Ready DAG Tasks | **%d** |\n", s.ReadyTasksCount))
	b.WriteString(fmt.Sprintf("| 🎯 Incomplete Milestones | **%d** |\n", s.PendingMilestonesCount))
	b.WriteString(fmt.Sprintf("| ⚙️ Pending SOP Phase Gates | **%d** |\n", s.PendingSOPGatesCount))
	b.WriteString(fmt.Sprintf("| 🛡️ Open Security Vulnerabilities | **%d** |\n\n", s.OpenVulnerabilitiesCount))

	if len(s.Items) == 0 {
		b.WriteString("_No pending action items found. All systems operational!_\n")
		return b.String()
	}

	b.WriteString("### 🚀 Pending Action Items\n\n")
	b.WriteString("| ID | Category | Priority | Target Role | Title | Context |\n")
	b.WriteString("| :--- | :---: | :---: | :--- | :--- | :--- |\n")

	for _, it := range s.Items {
		b.WriteString(fmt.Sprintf("| `%s` | `%s` | **%s** | `%s` | %s | %s |\n",
			it.ID, it.Category, strings.ToUpper(it.Priority), it.TargetRole, it.Title, it.ContextInfo))
	}
	b.WriteString("\n")

	return b.String()
}
