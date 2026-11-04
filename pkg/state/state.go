package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DefaultDirName is the default directory relative to project root.
const DefaultDirName = ".local/gossip"

// Store manages thread-safe and process-safe persistence under .local/gossip.
type Store struct {
	dir     string
	lockMu  sync.Mutex // in-process mutex
	lockFd  *os.File   // inter-process lockfile
}

// Milestone represents an interim checkpoint under a high-level goal.
type Milestone struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Completed   bool     `json:"completed"`
	TaskIDs     []string `json:"task_ids,omitempty"`
}

// Goal represents a strategic objective for the multi-agent team.
type Goal struct {
	ID          string      `json:"id"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	Status      string      `json:"status"` // active, completed, archived
	Milestones  []Milestone `json:"milestones"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

// AgentSession tracks an active agent participating in the gossip mesh.
type AgentSession struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Role          string    `json:"role"`
	Capabilities  []string  `json:"capabilities"`
	CurrentTaskID string    `json:"current_task_id,omitempty"`
	Status        string    `json:"status"` // active, idle, offline
	RegisteredAt  time.Time `json:"registered_at"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
}

// ProjectState holds the top-level goals and agent registry.
type ProjectState struct {
	Version      string                  `json:"version"`
	ProjectName  string                  `json:"project_name"`
	Goals        []Goal                  `json:"goals"`
	Agents       map[string]AgentSession `json:"agents"`
	LastModified time.Time               `json:"last_modified"`
}

// NewStore initializes a store at the specified directory (or default .local/gossip).
func NewStore(customDir string) (*Store, error) {
	dir := customDir
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			dir = DefaultDirName
		} else {
			dir = filepath.Join(wd, DefaultDirName)
		}
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create gossip store dir %s: %w", dir, err)
	}

	return &Store{
		dir: dir,
	}, nil
}

// Dir returns the root storage directory.
func (s *Store) Dir() string {
	return s.dir
}

// Lock acquires both process-internal mutex and cross-process file lock.
func (s *Store) Lock() (func(), error) {
	s.lockMu.Lock()

	lockPath := filepath.Join(s.dir, ".lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0666)
	if err != nil {
		s.lockMu.Unlock()
		return nil, fmt.Errorf("failed to open lockfile: %w", err)
	}

	// Advisory file lock (cross-platform)
	if err := lockFile(f); err != nil {
		f.Close()
		s.lockMu.Unlock()
		return nil, fmt.Errorf("failed to acquire file lock: %w", err)
	}

	unlock := func() {
		_ = unlockFile(f)
		f.Close()
		s.lockMu.Unlock()
	}

	return unlock, nil
}

// AtomicWriteFile safely writes data to a temporary file in the same directory, then renames it.
func (s *Store) AtomicWriteFile(filename string, data []byte) error {
	destPath := filepath.Join(s.dir, filename)
	tmpPath := fmt.Sprintf("%s.tmp.%d.%d", destPath, os.Getpid(), time.Now().UnixNano())

	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write tempfile %s: %w", tmpPath, err)
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to atomic rename %s -> %s: %w", tmpPath, destPath, err)
	}

	return nil
}

// ReadFile reads the full contents of a file inside the store directory.
func (s *Store) ReadFile(filename string) ([]byte, error) {
	path := filepath.Join(s.dir, filename)
	return os.ReadFile(path)
}

// LoadProjectState reads state.json with defaults.
func (s *Store) LoadProjectState() (*ProjectState, error) {
	data, err := s.ReadFile("state.json")
	if err != nil {
		if os.IsNotExist(err) {
			wd, _ := os.Getwd()
			return &ProjectState{
				Version:      "1.0.0",
				ProjectName:  filepath.Base(wd),
				Goals:        []Goal{},
				Agents:       make(map[string]AgentSession),
				LastModified: time.Now(),
			}, nil
		}
		return nil, err
	}

	var state ProjectState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("corrupted state.json: %w", err)
	}
	if state.Agents == nil {
		state.Agents = make(map[string]AgentSession)
	}
	return &state, nil
}

// SaveProjectState atomically saves state.json.
func (s *Store) SaveProjectState(state *ProjectState) error {
	state.LastModified = time.Now()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return s.AtomicWriteFile("state.json", data)
}

// RegisterAgent registers or updates an agent in state.json.
func (s *Store) RegisterAgent(session AgentSession) error {
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer unlock()

	st, err := s.LoadProjectState()
	if err != nil {
		return err
	}

	now := time.Now()
	if session.RegisteredAt.IsZero() {
		session.RegisteredAt = now
	}
	session.LastHeartbeat = now
	if session.Status == "" {
		session.Status = "active"
	}

	st.Agents[session.ID] = session
	return s.SaveProjectState(st)
}

// Heartbeat refreshes the last seen timestamp of an agent.
func (s *Store) Heartbeat(agentID, currentTaskID, status string) error {
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer unlock()

	st, err := s.LoadProjectState()
	if err != nil {
		return err
	}

	sess, exists := st.Agents[agentID]
	if !exists {
		return fmt.Errorf("agent %s not found in registry", agentID)
	}

	sess.LastHeartbeat = time.Now()
	if status != "" {
		sess.Status = status
	}
	if currentTaskID != "" {
		sess.CurrentTaskID = currentTaskID
	}
	st.Agents[agentID] = sess

	return s.SaveProjectState(st)
}

// ListAgents retrieves all known agents with active status evaluated.
func (s *Store) ListAgents() ([]AgentSession, error) {
	unlock, err := s.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	return s.ListAgentsUnlocked()
}

// ListAgentsUnlocked retrieves all agents assuming lock is already held by caller.
func (s *Store) ListAgentsUnlocked() ([]AgentSession, error) {
	st, err := s.LoadProjectState()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	var list []AgentSession
	for _, a := range st.Agents {
		// Mark offline if no heartbeat for > 15 minutes
		if now.Sub(a.LastHeartbeat) > 15*time.Minute && a.Status == "active" {
			a.Status = "offline"
		}
		list = append(list, a)
	}
	return list, nil
}

// SetGoal creates or updates a project goal.
func (s *Store) SetGoal(title, description string, milestones []Milestone) (*Goal, error) {
	unlock, err := s.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	st, err := s.LoadProjectState()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	goalID := fmt.Sprintf("goal-%d", now.Unix())
	g := Goal{
		ID:          goalID,
		Title:       title,
		Description: description,
		Status:      "active",
		Milestones:  milestones,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	st.Goals = append(st.Goals, g)
	if err := s.SaveProjectState(st); err != nil {
		return nil, err
	}
	return &g, nil
}

// GetGoals retrieves all goals.
func (s *Store) GetGoals() ([]Goal, error) {
	unlock, err := s.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	return s.GetGoalsUnlocked()
}

// GetGoalsUnlocked retrieves goals assuming lock is already held.
func (s *Store) GetGoalsUnlocked() ([]Goal, error) {
	st, err := s.LoadProjectState()
	if err != nil {
		return nil, err
	}
	return st.Goals, nil
}
