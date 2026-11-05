package cron

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tasmirz/gossip/pkg/messages"
	"github.com/tasmirz/gossip/pkg/state"
	"github.com/tasmirz/gossip/pkg/tasks"
	"github.com/tasmirz/gossip/pkg/tunnel"
)

// CheckResult represents a comprehensive snapshot produced by periodic health checks.
type CheckResult struct {
	Timestamp           time.Time `json:"timestamp"`
	AgentID             string    `json:"agent_id"`
	UnreadMessagesCount int       `json:"unread_messages_count"`
	ReadyTasksCount     int       `json:"ready_tasks_count"`
	ActiveAgentsCount   int       `json:"active_agents_count"`
	StaleAgentsCount    int       `json:"stale_agents_count"`
	TunnelActive        bool      `json:"tunnel_active"`
	TunnelEndpoint      string    `json:"tunnel_endpoint,omitempty"`
	AuthProtected       bool      `json:"auth_protected"`
	RemotePeersCount    int       `json:"remote_peers_count"`
	HasAlerts           bool      `json:"has_alerts"`
	Alerts              []string  `json:"alerts"`
	Summary             string    `json:"summary"`
}

// ParseInterval parses a duration string or common cron-like shorthand (@hourly, @every Xm).
func ParseInterval(expr string) (time.Duration, error) {
	expr = strings.TrimSpace(strings.ToLower(expr))
	if expr == "" {
		return 30 * time.Second, nil
	}
	switch expr {
	case "@minutely", "* * * * *":
		return 1 * time.Minute, nil
	case "@hourly", "0 * * * *":
		return 1 * time.Hour, nil
	case "@daily", "0 0 * * *":
		return 24 * time.Hour, nil
	}
	if strings.HasPrefix(expr, "@every ") {
		expr = strings.TrimPrefix(expr, "@every ")
	}
	if strings.HasPrefix(expr, "*/") && strings.HasSuffix(expr, " * * * *") {
		// e.g. "*/5 * * * *" -> 5 minutes
		minStr := strings.TrimSuffix(strings.TrimPrefix(expr, "*/"), " * * * *")
		return time.ParseDuration(minStr + "m")
	}
	return time.ParseDuration(expr)
}

// RunCheck executes a full periodic check against local state, messages, tasks, and tunnels.
func RunCheck(store *state.Store, agentID string) (*CheckResult, error) {
	if store == nil {
		return nil, fmt.Errorf("state store is uninitialized")
	}

	now := time.Now()
	res := &CheckResult{
		Timestamp: now,
		AgentID:   agentID,
		Alerts:    make([]string, 0),
	}

	// 1. Check unread messages for agentID
	if agentID != "" {
		msgMgr := messages.NewManager(store)
		if unread, err := msgMgr.ReadInbox(agentID, "", true, "", 0); err == nil {
			res.UnreadMessagesCount = len(unread)
			if len(unread) > 0 {
				res.Alerts = append(res.Alerts, fmt.Sprintf("%d unread message(s) awaiting review in inbox", len(unread)))
			}
		}
	}

	// 2. Check ready tasks in DAG
	tm := tasks.NewManager(store)
	if ready, err := tm.ListTasks(tasks.StatusTodo, "", ""); err == nil {
		res.ReadyTasksCount = len(ready)
		if len(ready) > 0 {
			res.Alerts = append(res.Alerts, fmt.Sprintf("%d task(s) in 'todo' status ready for execution", len(ready)))
		}
	}

	// 3. Inspect active and stale agents
	st, err := store.LoadProjectState()
	if err == nil && st != nil && st.Agents != nil {
		for _, a := range st.Agents {
			if a.Status == "active" {
				if time.Since(a.LastHeartbeat) > 15*time.Minute {
					res.StaleAgentsCount++
					res.Alerts = append(res.Alerts, fmt.Sprintf("Agent '%s' (%s) heartbeat stale (>15m)", a.ID, a.Role))
				} else {
					res.ActiveAgentsCount++
				}
			}
		}
	}

	// 4. Inspect tunnel state & remote disclosure
	disc := tunnel.ComputeDisclosure(store)
	res.TunnelActive = disc.TunnelActive
	res.TunnelEndpoint = disc.RemoteEndpoint
	res.AuthProtected = disc.AuthProtected
	res.RemotePeersCount = disc.TotalRemoteCount

	if res.TunnelActive && !res.AuthProtected {
		res.Alerts = append(res.Alerts, "CRITICAL: Active tunnel is UNPROTECTED! BasicAuth credentials recommended.")
	}

	// 5. Evaluate overall alerts and formulate summary
	res.HasAlerts = len(res.Alerts) > 0
	var summaryParts []string
	summaryParts = append(summaryParts, fmt.Sprintf("Agents: %d active", res.ActiveAgentsCount))
	if res.StaleAgentsCount > 0 {
		summaryParts = append(summaryParts, fmt.Sprintf("%d stale", res.StaleAgentsCount))
	}
	summaryParts = append(summaryParts, fmt.Sprintf("Tasks: %d ready", res.ReadyTasksCount))
	if res.UnreadMessagesCount > 0 {
		summaryParts = append(summaryParts, fmt.Sprintf("Unread: %d msgs", res.UnreadMessagesCount))
	}
	if res.TunnelActive {
		prot := "PROTECTED"
		if !res.AuthProtected {
			prot = "UNPROTECTED"
		}
		summaryParts = append(summaryParts, fmt.Sprintf("Tunnel: %s (%s)", res.TunnelEndpoint, prot))
	} else {
		summaryParts = append(summaryParts, "Tunnel: LOCAL ONLY")
	}
	if res.RemotePeersCount > 0 {
		summaryParts = append(summaryParts, fmt.Sprintf("Remote Peers: %d", res.RemotePeersCount))
	}

	res.Summary = strings.Join(summaryParts, " // ")
	return res, nil
}

// Runner manages background periodic checks with ticker cancellation.
type Runner struct {
	store    *state.Store
	agentID  string
	interval time.Duration
	cancel   context.CancelFunc
	lastRes  *CheckResult
	mu       sync.RWMutex
}

// NewRunner creates a new periodic check runner.
func NewRunner(store *state.Store, agentID string, interval time.Duration) *Runner {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Runner{
		store:    store,
		agentID:  agentID,
		interval: interval,
	}
}

// Start initiates the periodic ticker in a background goroutine.
func (r *Runner) Start(ctx context.Context, callback func(*CheckResult)) {
	runCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel

	go func() {
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()

		// Initial check
		if res, err := RunCheck(r.store, r.agentID); err == nil {
			r.mu.Lock()
			r.lastRes = res
			r.mu.Unlock()
			if callback != nil {
				callback(res)
			}
		}

		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if res, err := RunCheck(r.store, r.agentID); err == nil {
					r.mu.Lock()
					r.lastRes = res
					r.mu.Unlock()
					if callback != nil {
						callback(res)
					}
				}
			}
		}
	}()
}

// Stop terminates the periodic runner.
func (r *Runner) Stop() {
	if r.cancel != nil {
		r.cancel()
	}
}

// LastResult returns the most recent check result.
func (r *Runner) LastResult() *CheckResult {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lastRes
}

// RecordCronLog appends a check result record to .local/gossip/cron.log.
func RecordCronLog(storageDir string, res *CheckResult) error {
	if storageDir == "" {
		storageDir = ".local/gossip"
	}
	logFile := filepath.Join(storageDir, "cron.log")
	line := fmt.Sprintf("[%s] %s (Alerts: %d)\n",
		res.Timestamp.Format("2006-01-02 15:04:05"), res.Summary, len(res.Alerts))
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line)
	return err
}
