package tasks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tasmirz/gossip/pkg/security"
)

// PersonalTodoStatus values
const (
	PersonalTodoPending    = "pending"
	PersonalTodoInProgress = "in_progress"
	PersonalTodoDone       = "done"
)

// PersonalTodoItem represents an agent's personal scratchpad task or checklist item.
type PersonalTodoItem struct {
	ID            string    `json:"id"`
	AgentID       string    `json:"agent_id"`
	Title         string    `json:"title"`
	Status        string    `json:"status"` // pending, in_progress, done
	Priority      string    `json:"priority"` // low, normal, high
	ContextTaskID string    `json:"context_task_id,omitempty"` // Parent global DAG task
	Notes         string    `json:"notes,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// PersonalTodosContainer stores personal items per agent.
type PersonalTodosContainer struct {
	Version   string                        `json:"version"`
	Todos     map[string][]PersonalTodoItem `json:"todos"` // keyed by AgentID
	UpdatedAt time.Time                     `json:"updated_at"`
}

var personalMu sync.Mutex

func (m *Manager) personalTodosFilePath() string {
	return filepath.Join(m.store.Dir(), "personal_todos.json")
}

// LoadPersonalTodosContainer loads personal_todos.json or initializes a new one.
func (m *Manager) LoadPersonalTodosContainer() (*PersonalTodosContainer, error) {
	personalMu.Lock()
	defer personalMu.Unlock()

	fPath := m.personalTodosFilePath()
	data, err := os.ReadFile(fPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &PersonalTodosContainer{
				Version:   "1.0.0",
				Todos:     make(map[string][]PersonalTodoItem),
				UpdatedAt: time.Now(),
			}, nil
		}
		return nil, err
	}

	var c PersonalTodosContainer
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("failed to parse personal_todos.json: %w", err)
	}
	if c.Todos == nil {
		c.Todos = make(map[string][]PersonalTodoItem)
	}
	return &c, nil
}

// SavePersonalTodosContainer saves personal_todos.json with atomic safety.
func (m *Manager) SavePersonalTodosContainer(c *PersonalTodosContainer) error {
	personalMu.Lock()
	defer personalMu.Unlock()

	c.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return m.store.AtomicWriteFile("personal_todos.json", data)
}

// AddPersonalTodo adds a checklist item for a specific agent.
func (m *Manager) AddPersonalTodo(agentID, title, priority, contextTaskID, notes string) (*PersonalTodoItem, error) {
	if agentID == "" || title == "" {
		return nil, fmt.Errorf("agent_id and title are required")
	}

	sanitizedAgent, err := security.SanitizeID(agentID, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid agent_id: %w", err)
	}

	if priority == "" {
		priority = PriorityNormal
	}

	c, err := m.LoadPersonalTodosContainer()
	if err != nil {
		return nil, err
	}

	item := PersonalTodoItem{
		ID:            fmt.Sprintf("ptodo-%d", time.Now().UnixNano()),
		AgentID:       sanitizedAgent,
		Title:         strings.TrimSpace(title),
		Status:        PersonalTodoPending,
		Priority:      priority,
		ContextTaskID: strings.TrimSpace(contextTaskID),
		Notes:         strings.TrimSpace(notes),
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	c.Todos[sanitizedAgent] = append(c.Todos[sanitizedAgent], item)
	if err := m.SavePersonalTodosContainer(c); err != nil {
		return nil, err
	}

	return &item, nil
}

// UpdatePersonalTodo updates status or notes for an agent's personal to-do.
func (m *Manager) UpdatePersonalTodo(agentID, itemID, status, notes string) (*PersonalTodoItem, error) {
	c, err := m.LoadPersonalTodosContainer()
	if err != nil {
		return nil, err
	}

	items, ok := c.Todos[agentID]
	if !ok || len(items) == 0 {
		return nil, fmt.Errorf("no personal to-dos found for agent '%s'", agentID)
	}

	var updated *PersonalTodoItem
	for i := range items {
		if items[i].ID == itemID {
			if status != "" {
				items[i].Status = status
			}
			if notes != "" {
				items[i].Notes = notes
			}
			items[i].UpdatedAt = time.Now()
			updated = &items[i]
			break
		}
	}

	if updated == nil {
		return nil, fmt.Errorf("personal to-do item '%s' not found", itemID)
	}

	c.Todos[agentID] = items
	if err := m.SavePersonalTodosContainer(c); err != nil {
		return nil, err
	}

	return updated, nil
}

// ListPersonalTodos returns all personal to-do items for an agent, optionally filtered by status.
func (m *Manager) ListPersonalTodos(agentID, statusFilter string) ([]PersonalTodoItem, error) {
	c, err := m.LoadPersonalTodosContainer()
	if err != nil {
		return nil, err
	}

	items := c.Todos[agentID]
	var res []PersonalTodoItem
	for _, it := range items {
		if statusFilter == "" || it.Status == statusFilter {
			res = append(res, it)
		}
	}

	sort.Slice(res, func(i, j int) bool {
		return res[i].CreatedAt.Before(res[j].CreatedAt)
	})

	return res, nil
}

// DeletePersonalTodo removes a personal to-do item.
func (m *Manager) DeletePersonalTodo(agentID, itemID string) error {
	c, err := m.LoadPersonalTodosContainer()
	if err != nil {
		return err
	}

	items := c.Todos[agentID]
	var remaining []PersonalTodoItem
	found := false
	for _, it := range items {
		if it.ID == itemID {
			found = true
			continue
		}
		remaining = append(remaining, it)
	}

	if !found {
		return fmt.Errorf("personal to-do item '%s' not found", itemID)
	}

	c.Todos[agentID] = remaining
	return m.SavePersonalTodosContainer(c)
}
