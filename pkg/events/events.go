package events

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tasmirz/gossip/pkg/deliberation"
	"github.com/tasmirz/gossip/pkg/messages"
	"github.com/tasmirz/gossip/pkg/security"
	"github.com/tasmirz/gossip/pkg/sop"
	"github.com/tasmirz/gossip/pkg/state"
	"github.com/tasmirz/gossip/pkg/tasks"
)

// EventType categories for structured polling.
const (
	TypeMessageReceived = "message_received"
	TypeTaskReady       = "task_ready"
	TypeTaskAssigned    = "task_assigned"
	TypeTaskUpdated     = "task_updated"
	TypeDebateActivity  = "debate_activity"
	TypeSOPAdvanced     = "sop_advanced"
	TypeAgentPresence   = "agent_presence"
	TypeGoalUpdated     = "goal_updated"
	TypeCustomEvent     = "custom_event"
)

// Event represents an actionable notification for an agent.
type Event struct {
	ID         string    `json:"id"`
	Type       string    `json:"type"`
	Timestamp  time.Time `json:"timestamp"`
	ActorID    string    `json:"actor_id,omitempty"`
	ActorRole  string    `json:"actor_role,omitempty"`
	TargetID   string    `json:"target_id,omitempty"`
	TargetRole string    `json:"target_role,omitempty"`
	Summary    string    `json:"summary"`
	Data       any       `json:"data,omitempty"`
}

// PollRequest defines parameters for continuous or long-polling sync.
type PollRequest struct {
	AgentID       string        `json:"agent_id"`
	Role          string        `json:"role"`
	SinceTime     time.Time     `json:"since_time"`
	Timeout       time.Duration `json:"timeout"`
	EventTypes    []string      `json:"event_types,omitempty"`
	AutoHeartbeat bool          `json:"auto_heartbeat"`
	CurrentTaskID string        `json:"current_task_id,omitempty"`
	Status        string        `json:"status,omitempty"`
}

// PollResponse encapsulates the result of an event synchronization call.
type PollResponse struct {
	ServerTime          time.Time `json:"server_time"`
	SinceTime           time.Time `json:"since_time"`
	HasUpdates          bool      `json:"has_updates"`
	Events              []Event   `json:"events"`
	TotalEvents         int       `json:"total_events"`
	UnreadMessagesCount int       `json:"unread_messages_count"`
	ReadyTasksCount     int       `json:"ready_tasks_count"`
	ActiveAgentsCount   int       `json:"active_agents_count"`
	PollDurationMs      int64     `json:"poll_duration_ms"`
}

// Manager coordinates event logging, aggregation, and long-polling synchronization.
type Manager struct {
	store    *state.Store
	messages *messages.Manager
	tasks    *tasks.Manager
	delib    *deliberation.Manager
	sop      *sop.Manager
	mu       sync.Mutex
}

// NewManager creates a new event manager backed by project state subsystems.
func NewManager(
	store *state.Store,
	msgMgr *messages.Manager,
	taskMgr *tasks.Manager,
	delibMgr *deliberation.Manager,
	sopMgr *sop.Manager,
) *Manager {
	return &Manager{
		store:    store,
		messages: msgMgr,
		tasks:    taskMgr,
		delib:    delibMgr,
		sop:      sopMgr,
	}
}

func (m *Manager) eventsFilePath() string {
	return filepath.Join(m.store.Dir(), "events.jsonl")
}

// LogEvent appends a structured event to events.jsonl with concurrency protection.
func (m *Manager) LogEvent(evt Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if evt.ID == "" {
		evt.ID = fmt.Sprintf("evt-%d", time.Now().UnixNano())
	}
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now()
	}

	sanitizedID, err := security.SanitizeID(evt.ID, 64)
	if err != nil {
		return fmt.Errorf("invalid event id: %w", err)
	}
	evt.ID = sanitizedID

	safePath, err := security.ValidateSafePath(m.store.Dir(), "events.jsonl")
	if err != nil {
		return err
	}

	data, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	f, err := os.OpenFile(safePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open events.jsonl: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("failed to append event: %w", err)
	}

	return nil
}

// Poll performs an event sync check. If Timeout > 0 and no immediate events exist,
// it long-polls the state until new events occur or the timeout expires.
func (m *Manager) Poll(ctx context.Context, req PollRequest) (*PollResponse, error) {
	start := time.Now()

	// Max timeout safety cap (60s)
	if req.Timeout > 60*time.Second {
		req.Timeout = 60 * time.Second
	}

	// Update heartbeat if auto_heartbeat requested
	if req.AutoHeartbeat && req.AgentID != "" {
		status := req.Status
		if status == "" {
			status = "active"
		}
		_ = m.store.Heartbeat(req.AgentID, req.CurrentTaskID, status)
	}

	// Default SinceTime to 1 minute ago if uninitialized
	if req.SinceTime.IsZero() {
		req.SinceTime = time.Now().Add(-1 * time.Minute)
	}

	deadline := start.Add(req.Timeout)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()

	for {
		events, unreadMsgs, readyTasks, activeAgents := m.gatherEvents(req)

		if len(events) > 0 || req.Timeout == 0 || time.Now().After(deadline) || ctx.Err() != nil {
			durationMs := time.Since(start).Milliseconds()
			return &PollResponse{
				ServerTime:          time.Now(),
				SinceTime:           req.SinceTime,
				HasUpdates:          len(events) > 0,
				Events:              events,
				TotalEvents:         len(events),
				UnreadMessagesCount: unreadMsgs,
				ReadyTasksCount:     readyTasks,
				ActiveAgentsCount:   activeAgents,
				PollDurationMs:      durationMs,
			}, nil
		}

		select {
		case <-ctx.Done():
			durationMs := time.Since(start).Milliseconds()
			return &PollResponse{
				ServerTime:          time.Now(),
				SinceTime:           req.SinceTime,
				HasUpdates:          false,
				Events:              nil,
				TotalEvents:         0,
				UnreadMessagesCount: unreadMsgs,
				ReadyTasksCount:     readyTasks,
				ActiveAgentsCount:   activeAgents,
				PollDurationMs:      durationMs,
			}, ctx.Err()
		case <-ticker.C:
			// Continue to next check iteration
		}
	}
}

// gatherEvents collects new events since req.SinceTime across all Gossip subsystems.
func (m *Manager) gatherEvents(req PollRequest) ([]Event, int, int, int) {
	typeFilter := make(map[string]bool)
	for _, t := range req.EventTypes {
		typeFilter[strings.ToLower(strings.TrimSpace(t))] = true
	}
	shouldInclude := func(t string) bool {
		if len(typeFilter) == 0 {
			return true
		}
		return typeFilter[t]
	}

	var events []Event
	seenIDs := make(map[string]bool)

	// 1. Gather explicit events from events.jsonl
	explicitEvents, _ := m.loadExplicitEventsSince(req.SinceTime)
	for _, e := range explicitEvents {
		if shouldInclude(e.Type) && !seenIDs[e.ID] {
			// Check target filter if set
			if e.TargetID == "" || e.TargetID == "all" || e.TargetID == req.AgentID || e.TargetRole == req.Role {
				events = append(events, e)
				seenIDs[e.ID] = true
			}
		}
	}

	// 2. Gather new messages
	unreadMsgs := 0
	if m.messages != nil {
		allMsgs, err := m.messages.LoadAllMessages()
		if err == nil {
			for _, msg := range allMsgs {
				isTargeted := msg.To == "all" ||
					msg.To == "*" ||
					msg.To == req.AgentID ||
					msg.To == req.Role ||
					msg.To == fmt.Sprintf("role:%s", req.Role)

				hasRead := false
				for _, reader := range msg.ReadBy {
					if reader == req.AgentID {
						hasRead = true
						break
					}
				}

				if isTargeted && !hasRead {
					unreadMsgs++
				}

				if msg.CreatedAt.After(req.SinceTime) && isTargeted {
					evtID := fmt.Sprintf("evt-msg-%s", msg.ID)
					if !seenIDs[evtID] && shouldInclude(TypeMessageReceived) {
						events = append(events, Event{
							ID:         evtID,
							Type:       TypeMessageReceived,
							Timestamp:  msg.CreatedAt,
							ActorID:    msg.From,
							ActorRole:  msg.Role,
							TargetID:   msg.To,
							TargetRole: req.Role,
							Summary:    fmt.Sprintf("Message from %s (%s): %s", msg.From, msg.Role, msg.Subject),
							Data:       msg,
						})
						seenIDs[evtID] = true
					}
				}
			}
		}
	}

	// 3. Gather task updates & ready tasks
	readyTasks := 0
	if m.tasks != nil {
		container, err := m.tasks.LoadContainer()
		if err == nil {
			// Build status map of tasks for dependency checking
			taskStatus := make(map[string]string)
			for _, t := range container.Tasks {
				taskStatus[t.ID] = t.Status
			}

			for _, t := range container.Tasks {
				// Check if task is unblocked and ready to work
				isRoleMatch := t.Role == "" || t.Role == "any" || t.Role == req.Role || t.AssignedTo == req.AgentID
				if (t.Status == "todo" || t.Status == "ready") && isRoleMatch {
					allBlockersMet := true
					for _, b := range t.BlockedBy {
						if taskStatus[b] != "completed" {
							allBlockersMet = false
							break
						}
					}
					if allBlockersMet {
						readyTasks++
						evtID := fmt.Sprintf("evt-task-ready-%s", t.ID)
						if !seenIDs[evtID] && t.UpdatedAt.After(req.SinceTime) && shouldInclude(TypeTaskReady) {
							events = append(events, Event{
								ID:         evtID,
								Type:       TypeTaskReady,
								Timestamp:  t.UpdatedAt,
								ActorID:    t.AssignedTo,
								TargetID:   t.ID,
								TargetRole: t.Role,
								Summary:    fmt.Sprintf("Task ready for work: '%s' (Priority: %s)", t.Title, t.Priority),
								Data:       t,
							})
							seenIDs[evtID] = true
						}
					}
				}

				// Check recently assigned or updated tasks
				if t.UpdatedAt.After(req.SinceTime) {
					if t.AssignedTo == req.AgentID && shouldInclude(TypeTaskAssigned) {
						evtID := fmt.Sprintf("evt-task-assign-%s", t.ID)
						if !seenIDs[evtID] {
							events = append(events, Event{
								ID:        evtID,
								Type:      TypeTaskAssigned,
								Timestamp: t.UpdatedAt,
								TargetID:  t.ID,
								Summary:   fmt.Sprintf("Task assigned to you: '%s'", t.Title),
								Data:      t,
							})
							seenIDs[evtID] = true
						}
					} else if shouldInclude(TypeTaskUpdated) {
						evtID := fmt.Sprintf("evt-task-update-%s", t.ID)
						if !seenIDs[evtID] {
							events = append(events, Event{
								ID:        evtID,
								Type:      TypeTaskUpdated,
								Timestamp: t.UpdatedAt,
								TargetID:  t.ID,
								Summary:   fmt.Sprintf("Task '%s' updated to status '%s'", t.Title, t.Status),
								Data:      t,
							})
							seenIDs[evtID] = true
						}
					}
				}
			}
		}
	}

	// 4. Gather debate updates
	if m.delib != nil && shouldInclude(TypeDebateActivity) {
		debates, err := m.delib.ListDebates("")
		if err == nil {
			for _, d := range debates {
				if d.UpdatedAt.After(req.SinceTime) {
					evtID := fmt.Sprintf("evt-delib-%s-%d", d.ID, len(d.Rounds))
					if !seenIDs[evtID] {
						events = append(events, Event{
							ID:        evtID,
							Type:      TypeDebateActivity,
							Timestamp: d.UpdatedAt,
							ActorID:   d.Proposer,
							TargetID:  d.ID,
							Summary:   fmt.Sprintf("Debate activity on '%s': Status=%s, Rounds=%d", d.Title, d.Status, len(d.Rounds)),
							Data:      d,
						})
						seenIDs[evtID] = true
					}
				}
			}
		}
	}

	// 5. Gather SOP workflow advancements
	if m.sop != nil && shouldInclude(TypeSOPAdvanced) {
		wfs, err := m.sop.ListWorkflows()
		if err == nil {
			for _, wf := range wfs {
				if wf.UpdatedAt.After(req.SinceTime) {
					stageName := wf.Status
					if wf.CurrentStageIndex < len(wf.Stages) {
						stageName = wf.Stages[wf.CurrentStageIndex].Name
					}
					evtID := fmt.Sprintf("evt-sop-%s-%s", wf.ID, stageName)
					if !seenIDs[evtID] {
						events = append(events, Event{
							ID:        evtID,
							Type:      TypeSOPAdvanced,
							Timestamp: wf.UpdatedAt,
							TargetID:  wf.ID,
							Summary:   fmt.Sprintf("SOP workflow '%s' advanced to stage '%s'", wf.Title, stageName),
							Data:      wf,
						})
						seenIDs[evtID] = true
					}
				}
			}
		}
	}

	// 6. Gather active agent presence
	activeAgents := 0
	if m.store != nil {
		agents, err := m.store.ListAgents()
		if err == nil {
			cutoff := time.Now().Add(-2 * time.Minute)
			for _, a := range agents {
				if a.LastHeartbeat.After(cutoff) {
					activeAgents++
				}
				if a.LastHeartbeat.After(req.SinceTime) && a.ID != req.AgentID && shouldInclude(TypeAgentPresence) {
					evtID := fmt.Sprintf("evt-agent-%s-%d", a.ID, a.LastHeartbeat.Unix())
					if !seenIDs[evtID] {
						events = append(events, Event{
							ID:        evtID,
							Type:      TypeAgentPresence,
							Timestamp: a.LastHeartbeat,
							ActorID:   a.ID,
							ActorRole: a.Role,
							Summary:   fmt.Sprintf("Agent presence: %s (%s) is %s", a.Name, a.Role, a.Status),
							Data:      a,
						})
						seenIDs[evtID] = true
					}
				}
			}
		}
	}

	// Sort events chronologically (oldest to newest)
	sort.Slice(events, func(i, j int) bool {
		return events[i].Timestamp.Before(events[j].Timestamp)
	})

	return events, unreadMsgs, readyTasks, activeAgents
}

// loadExplicitEventsSince reads events recorded in events.jsonl after sinceTime.
func (m *Manager) loadExplicitEventsSince(sinceTime time.Time) ([]Event, error) {
	fPath := m.eventsFilePath()
	if _, err := os.Stat(fPath); os.IsNotExist(err) {
		return nil, nil
	}

	f, err := os.Open(fPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var events []Event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var evt Event
		if err := json.Unmarshal([]byte(line), &evt); err == nil {
			if evt.Timestamp.After(sinceTime) {
				events = append(events, evt)
			}
		}
	}

	return events, scanner.Err()
}
