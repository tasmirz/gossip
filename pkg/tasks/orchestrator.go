package tasks

import (
	"fmt"
	"strings"
	"time"
)

// AgentWorkload models an individual agent's active load and progress.
type AgentWorkload struct {
	AgentID        string    `json:"agent_id"`
	Role           string    `json:"role"`
	Status         string    `json:"status"` // active, idle, blocked
	LastHeartbeat  time.Time `json:"last_heartbeat"`
	ActiveTasks    []Task    `json:"active_tasks"`
	BlockedTasks   []Task    `json:"blocked_tasks"`
	InReviewTasks  []Task    `json:"in_review_tasks"`
	CompletedCount int       `json:"completed_count"`
	TotalAssigned  int       `json:"total_assigned"`
}

// OrchestratorAssignmentMatrix provides the complete delegation and workload overview.
type OrchestratorAssignmentMatrix struct {
	GeneratedAt           time.Time       `json:"generated_at"`
	TotalAgents           int             `json:"total_agents"`
	ActiveAgents          int             `json:"active_agents"`
	IdleAgents            int             `json:"idle_agents"`
	TotalTasks            int             `json:"total_tasks"`
	AssignedTasksCount    int             `json:"assigned_tasks_count"`
	UnassignedTasksCount  int             `json:"unassigned_tasks_count"`
	AgentWorkloads        []AgentWorkload `json:"agent_workloads"`
	UnassignedTasks       []Task          `json:"unassigned_tasks"`
	CriticalBottlenecks   []string        `json:"critical_bottlenecks"` // tasks blocking the most downstream tasks
	DelegationSuggestions []string        `json:"delegation_suggestions"`
}

// GetOrchestratorAssignments compiles the assignment matrix across registered agents and the task DAG.
func (m *Manager) GetOrchestratorAssignments() (*OrchestratorAssignmentMatrix, error) {
	container, err := m.LoadContainer()
	if err != nil {
		return nil, err
	}

	agents, err := m.store.ListAgents()
	if err != nil {
		agents = nil
	}

	matrix := &OrchestratorAssignmentMatrix{
		GeneratedAt: time.Now(),
		TotalTasks:  len(container.Tasks),
	}

	// Map tasks by assignee
	assignedTasks := make(map[string][]Task)
	blockingCount := make(map[string]int)

	for _, t := range container.Tasks {
		for _, b := range t.BlockedBy {
			blockingCount[b]++
		}

		if t.AssignedTo == "" {
			matrix.UnassignedTasks = append(matrix.UnassignedTasks, t)
			matrix.UnassignedTasksCount++
		} else {
			matrix.AssignedTasksCount++
			assignedTasks[t.AssignedTo] = append(assignedTasks[t.AssignedTo], t)
		}
	}

	// Build AgentWorkloads
	agentMap := make(map[string]bool)
	cutoff := time.Now().Add(-5 * time.Minute)

	for _, a := range agents {
		agentMap[a.ID] = true
		matrix.TotalAgents++

		workload := AgentWorkload{
			AgentID:       a.ID,
			Role:          a.Role,
			Status:        a.Status,
			LastHeartbeat: a.LastHeartbeat,
		}

		if a.LastHeartbeat.After(cutoff) {
			matrix.ActiveAgents++
		}

		userTasks := assignedTasks[a.ID]
		for _, t := range userTasks {
			switch t.Status {
			case StatusInProgress:
				workload.ActiveTasks = append(workload.ActiveTasks, t)
			case StatusBlocked:
				workload.BlockedTasks = append(workload.BlockedTasks, t)
			case StatusInReview:
				workload.InReviewTasks = append(workload.InReviewTasks, t)
			case StatusCompleted:
				workload.CompletedCount++
			}
		}

		workload.TotalAssigned = len(workload.ActiveTasks) + len(workload.BlockedTasks) + len(workload.InReviewTasks)
		if workload.TotalAssigned == 0 && a.LastHeartbeat.After(cutoff) {
			matrix.IdleAgents++
		}

		matrix.AgentWorkloads = append(matrix.AgentWorkloads, workload)
	}

	// Also include any assignees present in tasks who might not have active sessions
	for assignee, tList := range assignedTasks {
		if !agentMap[assignee] {
			workload := AgentWorkload{
				AgentID: assignee,
				Role:    "worker",
				Status:  "assigned_in_tasks",
			}
			for _, t := range tList {
				switch t.Status {
				case StatusInProgress:
					workload.ActiveTasks = append(workload.ActiveTasks, t)
				case StatusBlocked:
					workload.BlockedTasks = append(workload.BlockedTasks, t)
				case StatusInReview:
					workload.InReviewTasks = append(workload.InReviewTasks, t)
				case StatusCompleted:
					workload.CompletedCount++
				}
			}
			workload.TotalAssigned = len(workload.ActiveTasks) + len(workload.BlockedTasks) + len(workload.InReviewTasks)
			matrix.AgentWorkloads = append(matrix.AgentWorkloads, workload)
		}
	}

	// Identify Critical Bottlenecks (tasks blocking $\ge 2$ downstream tasks)
	for tID, count := range blockingCount {
		t, ok := container.Tasks[tID]
		if ok && t.Status != StatusCompleted && count >= 2 {
			matrix.CriticalBottlenecks = append(matrix.CriticalBottlenecks,
				fmt.Sprintf("Task '%s' (%s) is blocking %d downstream tasks (Status: %s, Assigned: %s)",
					t.Title, t.ID, count, t.Status, t.AssignedTo))
		}
	}

	// Suggest Delegations (match unassigned tasks to idle agents by role)
	for _, unassigned := range matrix.UnassignedTasks {
		if unassigned.Status == StatusTodo || unassigned.Status == "ready" {
			for _, wl := range matrix.AgentWorkloads {
				if (wl.Role == unassigned.Role || unassigned.Role == "" || unassigned.Role == "any") && wl.TotalAssigned == 0 {
					matrix.DelegationSuggestions = append(matrix.DelegationSuggestions,
						fmt.Sprintf("Assign unassigned task '%s' (%s, Priority: %s) to idle agent '%s' (%s)",
							unassigned.Title, unassigned.ID, unassigned.Priority, wl.AgentID, wl.Role))
					break
				}
			}
		}
	}

	return matrix, nil
}

// RenderMarkdown renders the assignment matrix into a human-readable Orchestrator Dashboard.
func (m *OrchestratorAssignmentMatrix) RenderMarkdown() string {
	var b strings.Builder
	b.WriteString("# 👑 Orchestrator Task Assignment & Delegation Matrix\n\n")
	b.WriteString(fmt.Sprintf("*Synchronized: %s | Total Tasks: %d | Assigned: %d | Unassigned: %d*\n\n",
		m.GeneratedAt.Format(time.RFC1123), m.TotalTasks, m.AssignedTasksCount, m.UnassignedTasksCount))

	b.WriteString("### 👥 Agent Workload Breakdown\n\n")
	b.WriteString("| Agent ID | Role | Status | Active Tasks | In Review | Blocked | Completed | Total Load |\n")
	b.WriteString("| :--- | :--- | :---: | :---: | :---: | :---: | :---: | :---: |\n")

	if len(m.AgentWorkloads) == 0 {
		b.WriteString("| _No agents registered_ | - | - | 0 | 0 | 0 | 0 | 0 |\n")
	} else {
		for _, wl := range m.AgentWorkloads {
			b.WriteString(fmt.Sprintf("| `%s` | `%s` | %s | %d | %d | %d | %d | **%d** |\n",
				wl.AgentID, wl.Role, strings.ToUpper(wl.Status),
				len(wl.ActiveTasks), len(wl.InReviewTasks), len(wl.BlockedTasks), wl.CompletedCount, wl.TotalAssigned))
		}
	}
	b.WriteString("\n")

	if len(m.CriticalBottlenecks) > 0 {
		b.WriteString("### ⚠️ Critical Dependency Bottlenecks\n\n")
		for _, c := range m.CriticalBottlenecks {
			b.WriteString(fmt.Sprintf("- 🛑 %s\n", c))
		}
		b.WriteString("\n")
	}

	if len(m.DelegationSuggestions) > 0 {
		b.WriteString("### 💡 Delegation Recommendations\n\n")
		for _, s := range m.DelegationSuggestions {
			b.WriteString(fmt.Sprintf("- 🎯 %s\n", s))
		}
		b.WriteString("\n")
	}

	if len(m.UnassignedTasks) > 0 {
		b.WriteString("### 📋 Unassigned Backlog Tasks\n\n")
		b.WriteString("| ID | Title | Role | Priority | Status |\n")
		b.WriteString("| :--- | :--- | :--- | :---: | :---: |\n")
		for _, t := range m.UnassignedTasks {
			b.WriteString(fmt.Sprintf("| `%s` | %s | `%s` | **%s** | %s |\n",
				t.ID, t.Title, t.Role, strings.ToUpper(t.Priority), t.Status))
		}
		b.WriteString("\n")
	}

	return b.String()
}
