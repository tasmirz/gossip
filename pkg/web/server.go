package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/tasmirz/gossip/pkg/deliberation"
	"github.com/tasmirz/gossip/pkg/guardrails"
	"github.com/tasmirz/gossip/pkg/messages"
	"github.com/tasmirz/gossip/pkg/moe"
	"github.com/tasmirz/gossip/pkg/roles"
	"github.com/tasmirz/gossip/pkg/sop"
	"github.com/tasmirz/gossip/pkg/state"
	"github.com/tasmirz/gossip/pkg/tasks"
)

// Server encapsulates the Gossip Local Web Dashboard.
type Server struct {
	port       int
	store      *state.Store
	messages   *messages.Manager
	tasks      *tasks.Manager
	delib      *deliberation.Manager
	sop        *sop.Manager
	guardrails *guardrails.Manager
	moe        *moe.Manager
	roleProv   roles.Provider
}

// NewServer initializes the Web Dashboard server.
func NewServer(port int, store *state.Store, roleProv roles.Provider) *Server {
	if roleProv == nil {
		roleProv = roles.GetDefaultService()
	}
	msgMgr := messages.NewManager(store)
	taskMgr := tasks.NewManager(store)
	delibMgr := deliberation.NewManager(store)
	sopMgr := sop.NewManager(store)
	guardMgr := guardrails.NewManager(store)
	moeMgr := moe.NewManager(store, roleProv)

	return &Server{
		port:       port,
		store:      store,
		messages:   msgMgr,
		tasks:      taskMgr,
		delib:      delibMgr,
		sop:        sopMgr,
		guardrails: guardMgr,
		moe:        moeMgr,
		roleProv:   roleProv,
	}
}

// Handler returns the HTTP handler with all web routes configured.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Frontend Single-Page App (Pure 2-Color Black & White, Monospace, Zero Border-Radius)
	mux.HandleFunc("/", s.handleIndex)

	// REST APIs
	mux.HandleFunc("/api/status", s.handleAPIStatus)
	mux.HandleFunc("/api/tasks", s.handleAPITasks)
	mux.HandleFunc("/api/tasks/update", s.handleAPITaskUpdate)
	mux.HandleFunc("/api/agents", s.handleAPIAgents)
	mux.HandleFunc("/api/messages", s.handleAPIMessages)
	mux.HandleFunc("/api/broadcast", s.handleAPIBroadcast)
	mux.HandleFunc("/api/moe", s.handleAPIMoE)
	mux.HandleFunc("/api/moe/convene", s.handleAPIMoEConvene)
	mux.HandleFunc("/api/moe/argument", s.handleAPIMoEArgument)
	mux.HandleFunc("/api/moe/synthesize", s.handleAPIMoESynthesize)
	mux.HandleFunc("/api/moe/dispatch", s.handleAPIMoEDispatch)
	mux.HandleFunc("/api/settings", s.handleAPISettings)

	return mux
}

// Start runs the HTTP web server.
func (s *Server) Start() error {
	addr := fmt.Sprintf(":%d", s.port)
	fmt.Printf("\n┌────────────────────────────────────────────────────────┐\n")
	fmt.Printf("│  🗣️ GOSSIP LOCAL WEB DASHBOARD (MONOCHROME B&W)        │\n")
	fmt.Printf("│  URL: http://localhost:%d                            │\n", s.port)
	fmt.Printf("│  Style: Pure 2-Color, Monospace, 0px Border Radius     │\n")
	fmt.Printf("└────────────────────────────────────────────────────────┘\n\n")
	return http.ListenAndServe(addr, s.Handler())
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(dashboardHTML))
}

func (s *Server) handleAPIStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.LoadProjectState()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	agents, _ := s.store.ListAgents()
	allTasks, _ := s.tasks.ListTasks("", "", "")
	panels, _ := s.moe.ListPanels("")

	resp := map[string]interface{}{
		"version":      "1.0.0",
		"project_name": st.ProjectName,
		"agent_count":  len(agents),
		"task_count":   len(allTasks),
		"moe_panels":   len(panels),
		"timestamp":    time.Now().Format(time.RFC3339),
	}
	jsonResponse(w, resp)
}

func (s *Server) handleAPITasks(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		tasksList, err := s.tasks.ListTasks("", "", "")
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		jsonResponse(w, tasksList)
		return
	}

	if r.Method == http.MethodPost {
		var req struct {
			Title       string   `json:"title"`
			Description string   `json:"description"`
			Role        string   `json:"role"`
			Priority    string   `json:"priority"`
			BlockedBy   []string `json:"blocked_by"`
			Criteria    []string `json:"criteria"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		task, err := s.tasks.CreateTask(req.Title, req.Description, req.Role, req.Priority, req.BlockedBy, req.Criteria)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		jsonResponse(w, task)
		return
	}
	http.Error(w, "Method not allowed", 405)
}

func (s *Server) handleAPITaskUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", 405)
		return
	}
	var req struct {
		TaskID       string `json:"task_id"`
		Status       string `json:"status"`
		AssignedTo   string `json:"assigned_to"`
		ProgressNote string `json:"progress_note"`
		Artifact     string `json:"artifact"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	task, err := s.tasks.UpdateTask(req.TaskID, req.Status, req.AssignedTo, req.ProgressNote, req.Artifact)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	jsonResponse(w, task)
}

func (s *Server) handleAPIAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		agents, err := s.store.ListAgents()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		jsonResponse(w, agents)
		return
	}
	if r.Method == http.MethodPost {
		var sess state.AgentSession
		if err := json.NewDecoder(r.Body).Decode(&sess); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if sess.ID == "" || sess.Role == "" {
			http.Error(w, "id and role required", 400)
			return
		}
		if err := s.store.RegisterAgent(sess); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		jsonResponse(w, map[string]string{"status": "registered"})
		return
	}
	http.Error(w, "Method not allowed", 405)
}

func (s *Server) handleAPIMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		msgs, err := s.messages.LoadAllMessages()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		jsonResponse(w, msgs)
		return
	}
	if r.Method == http.MethodPost {
		var req struct {
			From    string `json:"from"`
			Role    string `json:"role"`
			To      string `json:"to"`
			Subject string `json:"subject"`
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		msg, err := s.messages.SendMessage(req.From, req.Role, req.To, "", req.Subject, req.Content)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		jsonResponse(w, msg)
		return
	}
	http.Error(w, "Method not allowed", 405)
}

func (s *Server) handleAPIBroadcast(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", 405)
		return
	}
	var req struct {
		From    string `json:"from"`
		Role    string `json:"role"`
		Subject string `json:"subject"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	msg, err := s.messages.SendMessage(req.From, req.Role, "all", "", req.Subject, req.Content)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	jsonResponse(w, msg)
}

func (s *Server) handleAPIMoE(w http.ResponseWriter, r *http.Request) {
	panels, err := s.moe.ListPanels("")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	jsonResponse(w, panels)
}

func (s *Server) handleAPIMoEConvene(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", 405)
		return
	}
	var req struct {
		Title       string   `json:"title"`
		Query       string   `json:"query"`
		Domain      string   `json:"domain"`
		Initiator   string   `json:"initiator"`
		Roles       []string `json:"roles"`
		MaxParallel int      `json:"max_parallel"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	panel, err := s.moe.ConvenePanel(req.Title, req.Query, req.Domain, req.Initiator, req.Roles, req.MaxParallel)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	jsonResponse(w, panel)
}

func (s *Server) handleAPIMoEArgument(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", 405)
		return
	}
	var req struct {
		PanelID     string   `json:"panel_id"`
		AgentID     string   `json:"agent_id"`
		Role        string   `json:"role"`
		Stance      string   `json:"stance"`
		Perspective string   `json:"perspective"`
		KeyPoints   []string `json:"key_points"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	arg, err := s.moe.PostArgument(req.PanelID, req.AgentID, req.Role, req.Stance, req.Perspective, req.KeyPoints)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	jsonResponse(w, arg)
}

func (s *Server) handleAPIMoESynthesize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", 405)
		return
	}
	var req struct {
		PanelID            string            `json:"panel_id"`
		AgentID            string            `json:"agent_id"`
		ConsensusDecision  string            `json:"consensus_decision"`
		TradeOffs          []string          `json:"trade_offs"`
		RecommendedActions []string          `json:"recommended_actions"`
		Packages           []moe.WorkPackage `json:"packages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	synth, err := s.moe.Synthesize(req.PanelID, req.AgentID, req.ConsensusDecision, req.TradeOffs, req.RecommendedActions, req.Packages)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	jsonResponse(w, synth)
}

func (s *Server) handleAPIMoEDispatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", 405)
		return
	}
	var req struct {
		PanelID string `json:"panel_id"`
		AgentID string `json:"agent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	tracks, err := s.moe.DispatchSubagents(req.PanelID, req.AgentID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	jsonResponse(w, tracks)
}

func (s *Server) handleAPISettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		st, _ := s.store.LoadProjectState()
		gr, _ := s.guardrails.Load()
		resp := map[string]interface{}{
			"project_name": st.ProjectName,
			"goals":        st.Goals,
			"guardrails":   gr,
		}
		jsonResponse(w, resp)
		return
	}

	if r.Method == http.MethodPost {
		var req struct {
			ProjectName string `json:"project_name"`
			GoalTitle   string `json:"goal_title"`
			GoalDesc    string `json:"goal_desc"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		st, err := s.store.LoadProjectState()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if req.ProjectName != "" {
			st.ProjectName = req.ProjectName
		}
		if req.GoalTitle != "" {
			st.Goals = append(st.Goals, state.Goal{
				ID:          fmt.Sprintf("goal-%d", time.Now().Unix()),
				Title:       req.GoalTitle,
				Description: req.GoalDesc,
				Status:      "active",
				CreatedAt:   time.Now(),
			})
		}
		_ = s.store.SaveProjectState(st)
		jsonResponse(w, map[string]string{"status": "updated"})
		return
	}
	http.Error(w, "Method not allowed", 405)
}

func jsonResponse(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(data)
}

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>GOSSIP MESH // CONTROL PANEL</title>
<style>
/* PURE TWO-COLOR BLACK AND WHITE THEME // ZERO BORDER-RADIUS // MONOSPACE */
* {
  box-sizing: border-box;
  margin: 0;
  padding: 0;
  border-radius: 0 !important;
  font-family: 'Courier New', Courier, monospace, ui-monospace !important;
}

body {
  background-color: #000000;
  color: #ffffff;
  padding: 16px;
  line-height: 1.4;
}

header {
  border: 1px solid #ffffff;
  padding: 12px 16px;
  margin-bottom: 16px;
  display: flex;
  justify-content: space-between;
  align-items: center;
}

h1 {
  font-size: 18px;
  letter-spacing: 2px;
}

.status-badge {
  border: 1px solid #ffffff;
  padding: 4px 8px;
  font-size: 12px;
}

nav {
  display: flex;
  gap: 8px;
  margin-bottom: 16px;
  border-bottom: 1px solid #ffffff;
  padding-bottom: 8px;
}

.tab-btn {
  background: #000000;
  color: #ffffff;
  border: 1px solid #ffffff;
  padding: 8px 16px;
  cursor: pointer;
  font-weight: bold;
}

.tab-btn:hover, .tab-btn.active {
  background: #ffffff;
  color: #000000;
}

.panel {
  display: none;
}

.panel.active {
  display: block;
}

.box {
  border: 1px solid #ffffff;
  padding: 16px;
  margin-bottom: 16px;
}

.box-title {
  font-size: 14px;
  font-weight: bold;
  border-bottom: 1px solid #ffffff;
  padding-bottom: 8px;
  margin-bottom: 12px;
  letter-spacing: 1px;
}

table {
  width: 100%;
  border-collapse: collapse;
  margin-top: 8px;
}

th, td {
  border: 1px solid #ffffff;
  padding: 8px 10px;
  text-align: left;
  font-size: 13px;
}

th {
  background-color: #ffffff;
  color: #000000;
  font-weight: bold;
}

input, select, textarea, button {
  background: #000000;
  color: #ffffff;
  border: 1px solid #ffffff;
  padding: 6px 10px;
  font-size: 13px;
  outline: none;
}

button {
  cursor: pointer;
  font-weight: bold;
}

button:hover {
  background: #ffffff;
  color: #000000;
}

.grid-2 {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 16px;
}

.chat-box {
  height: 380px;
  overflow-y: scroll;
  border: 1px solid #ffffff;
  padding: 12px;
  background: #000000;
  margin-bottom: 12px;
}

.chat-line {
  margin-bottom: 8px;
  font-size: 13px;
  border-bottom: 1px dashed #ffffff;
  padding-bottom: 4px;
}

.chat-meta {
  font-size: 11px;
}

.chat-content {
  margin-top: 2px;
}

.flex-row {
  display: flex;
  gap: 8px;
}

.flex-1 {
  flex: 1;
}

.badge {
  display: inline-block;
  padding: 2px 6px;
  border: 1px solid #ffffff;
  font-size: 11px;
}
</style>
</head>
<body>

<header>
  <div>
    <h1>🗣️ GOSSIP MESH // CONTROL PANEL</h1>
    <div style="font-size: 11px; margin-top: 4px;">PROJECT: <span id="hdr-proj">Gossip</span> // AGENTS ONLINE: <span id="hdr-agents">0</span> // ACTIVE TASKS: <span id="hdr-tasks">0</span></div>
  </div>
  <div class="status-badge" id="live-indicator">● SYNC: OK</div>
</header>

<nav>
  <button class="tab-btn active" onclick="switchTab('tasks')">[1] TASKS & DAG</button>
  <button class="tab-btn" onclick="switchTab('agents')">[2] LIVE AGENTS</button>
  <button class="tab-btn" onclick="switchTab('chats')">[3] CHATS & BROADCAST</button>
  <button class="tab-btn" onclick="switchTab('moe')">[4] MOE COUNCIL</button>
  <button class="tab-btn" onclick="switchTab('settings')">[5] SETTINGS</button>
</nav>

<!-- PANEL 1: TASKS & DAG -->
<div id="panel-tasks" class="panel active">
  <div class="box">
    <div class="box-title">➕ CREATE TASK IN GLOBAL DAG</div>
    <div class="flex-row" style="margin-bottom: 8px;">
      <input type="text" id="task-title" placeholder="Task Title" class="flex-1">
      <select id="task-role">
        <option value="worker">Role: worker</option>
        <option value="orchestrator">Role: orchestrator</option>
        <option value="network_engineer">Role: network_engineer</option>
        <option value="math_expert">Role: math_expert</option>
        <option value="systems_architect">Role: systems_architect</option>
        <option value="security_specialist">Role: security_specialist</option>
        <option value="qa_engineer">Role: qa_engineer</option>
        <option value="researcher">Role: researcher</option>
        <option value="red_team">Role: red_team</option>
      </select>
      <select id="task-priority">
        <option value="high">Priority: high</option>
        <option value="medium" selected>Priority: medium</option>
        <option value="low">Priority: low</option>
      </select>
    </div>
    <div class="flex-row">
      <input type="text" id="task-desc" placeholder="Task description and acceptance requirements" class="flex-1">
      <input type="text" id="task-blocked-by" placeholder="Blocked by IDs (comma separated)" style="width: 260px;">
      <button onclick="createTask()">+ ADD TASK</button>
    </div>
  </div>

  <div class="box">
    <div class="box-title">📋 SHARED TASK MATRIX (DAG)</div>
    <table>
      <thead>
        <tr>
          <th>ID</th>
          <th>TITLE</th>
          <th>ROLE</th>
          <th>PRIORITY</th>
          <th>STATUS</th>
          <th>ASSIGNEE</th>
          <th>BLOCKERS</th>
          <th>ACTION</th>
        </tr>
      </thead>
      <tbody id="tasks-table-body">
        <tr><td colspan="8">Loading tasks...</td></tr>
      </tbody>
    </table>
  </div>
</div>

<!-- PANEL 2: LIVE AGENTS -->
<div id="panel-agents" class="panel">
  <div class="box">
    <div class="box-title">➕ REGISTER / REIDENTIFY AGENT</div>
    <div class="flex-row">
      <input type="text" id="agent-reg-id" placeholder="Agent ID (e.g. worker-1)" class="flex-1">
      <select id="agent-reg-role">
        <option value="worker">worker</option>
        <option value="orchestrator">orchestrator</option>
        <option value="network_engineer">network_engineer</option>
        <option value="math_expert">math_expert</option>
        <option value="systems_architect">systems_architect</option>
        <option value="security_specialist">security_specialist</option>
        <option value="qa_engineer">qa_engineer</option>
        <option value="researcher">researcher</option>
        <option value="red_team">red_team</option>
        <option value="critic">critic</option>
      </select>
      <input type="text" id="agent-reg-task" placeholder="Current Task ID (optional)" style="width: 220px;">
      <button onclick="registerAgent()">REGISTER AGENT</button>
    </div>
  </div>

  <div class="box">
    <div class="box-title">🤖 ACTIVE AGENT REGISTRY & PRESENCE</div>
    <table>
      <thead>
        <tr>
          <th>AGENT ID</th>
          <th>ROLE</th>
          <th>STATUS</th>
          <th>ACTIVE TASK</th>
          <th>SESSIONS</th>
          <th>LAST HEARTBEAT</th>
          <th>ACTIONS</th>
        </tr>
      </thead>
      <tbody id="agents-table-body">
        <tr><td colspan="7">Loading agents...</td></tr>
      </tbody>
    </table>
  </div>
</div>

<!-- PANEL 3: CHATS & BROADCAST -->
<div id="panel-chats" class="panel">
  <div class="box">
    <div class="box-title">💬 LIVE MESH TRANSCRIPT (DIRECT & BROADCAST MESSAGES)</div>
    <div class="chat-box" id="chat-stream">
      <div class="chat-line">Connecting to live message mesh...</div>
    </div>
    
    <div class="flex-row" style="margin-bottom: 8px;">
      <input type="text" id="chat-from" placeholder="From (e.g. user, orch-1)" style="width: 180px;">
      <input type="text" id="chat-to" placeholder="To (e.g. 'all', 'agent:worker-1', 'role:math_expert')" style="width: 260px;">
      <input type="text" id="chat-subject" placeholder="Subject / Topic" class="flex-1">
    </div>
    <div class="flex-row">
      <input type="text" id="chat-content" placeholder="Type message or broadcast directive..." class="flex-1">
      <button onclick="sendMessage()">SEND / BROADCAST</button>
    </div>
  </div>
</div>

<!-- PANEL 4: MOE COUNCIL -->
<div id="panel-moe" class="panel">
  <div class="box">
    <div class="box-title">🧠 CONVENE MIXTURE OF EXPERTS (MOE) COUNCIL</div>
    <div class="flex-row" style="margin-bottom: 8px;">
      <input type="text" id="moe-title" placeholder="Panel Topic / Title" class="flex-1">
      <input type="text" id="moe-domain" placeholder="Domain (e.g. networking, crypto, locks)" style="width: 280px;">
      <input type="number" id="moe-parallel" value="4" style="width: 140px;" placeholder="Max Parallel">
    </div>
    <div class="flex-row">
      <textarea id="moe-query" placeholder="Complex technical dilemma, architectural choice, or algorithmic trade-off statement" style="width: 100%; height: 60px;"></textarea>
    </div>
    <div style="margin-top: 8px;">
      <button onclick="conveneMoE()">CONVENE EXPERT COUNCIL</button>
    </div>
  </div>

  <div class="box">
    <div class="box-title">⚖️ ACTIVE MOE PANELS & SUBAGENT PARALLEL TRACKS</div>
    <div id="moe-panels-list">Loading MoE Councils...</div>
  </div>
</div>

<!-- PANEL 5: SETTINGS -->
<div id="panel-settings" class="panel">
  <div class="grid-2">
    <div class="box">
      <div class="box-title">⚙️ PROJECT SETTINGS</div>
      <div style="margin-bottom: 12px;">
        <label>PROJECT NAME:</label>
        <input type="text" id="set-proj-name" style="width: 100%; margin-top: 4px;">
      </div>
      <div style="margin-bottom: 12px;">
        <label>NEW STRATEGIC MILESTONE / GOAL:</label>
        <input type="text" id="set-goal-title" placeholder="Goal title" style="width: 100%; margin-top: 4px;">
        <textarea id="set-goal-desc" placeholder="Goal criteria" style="width: 100%; height: 60px; margin-top: 4px;"></textarea>
      </div>
      <button onclick="updateSettings()">SAVE SETTINGS</button>
    </div>

    <div class="box">
      <div class="box-title">🛡️ GUARDRAILS & PROJECT INVARIANTS</div>
      <div id="settings-guardrails-content">Loading guardrails...</div>
    </div>
  </div>
</div>

<script>
let currentTab = 'tasks';

function switchTab(tab) {
  currentTab = tab;
  document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
  document.querySelectorAll('.panel').forEach(p => p.classList.remove('active'));
  
  event.target.classList.add('active');
  document.getElementById('panel-' + tab).classList.add('active');
  syncData();
}

async function syncData() {
  try {
    const stRes = await fetch('/api/status');
    const st = await stRes.json();
    document.getElementById('hdr-proj').innerText = st.project_name || 'Gossip';
    document.getElementById('hdr-agents').innerText = st.agent_count;
    document.getElementById('hdr-tasks').innerText = st.task_count;

    if (currentTab === 'tasks') await syncTasks();
    if (currentTab === 'agents') await syncAgents();
    if (currentTab === 'chats') await syncChats();
    if (currentTab === 'moe') await syncMoE();
    if (currentTab === 'settings') await syncSettings();
  } catch(e) {
    document.getElementById('live-indicator').innerText = '✕ SYNC: ERR';
  }
}

async function syncTasks() {
  const res = await fetch('/api/tasks');
  const tasks = await res.json();
  const tbody = document.getElementById('tasks-table-body');
  if (!tasks || tasks.length === 0) {
    tbody.innerHTML = '<tr><td colspan="8">No tasks in global DAG. Create one above!</td></tr>';
    return;
  }
  tbody.innerHTML = tasks.map(t => ` + "`" + `
    <tr>
      <td><b>${t.id}</b></td>
      <td>${escapeHTML(t.title)}</td>
      <td><span class="badge">${t.role}</span></td>
      <td>${t.priority}</td>
      <td><span class="badge">${t.status}</span></td>
      <td>${t.assignee || '<i>unassigned</i>'}</td>
      <td>${(t.blocked_by || []).join(', ') || '-'}</td>
      <td>
        <button onclick="quickUpdateTask('${t.id}', 'in_progress')">START</button>
        <button onclick="quickUpdateTask('${t.id}', 'done')">DONE</button>
      </td>
    </tr>
  ` + "`" + `).join('');
}

async function syncAgents() {
  const res = await fetch('/api/agents');
  const agents = await res.json();
  const tbody = document.getElementById('agents-table-body');
  if (!agents || agents.length === 0) {
    tbody.innerHTML = '<tr><td colspan="7">No registered agents.</td></tr>';
    return;
  }
  tbody.innerHTML = agents.map(a => ` + "`" + `
    <tr>
      <td><b>${a.id}</b></td>
      <td><span class="badge">${a.role}</span></td>
      <td>${a.status === 'active' ? '● ACTIVE' : '○ ' + a.status}</td>
      <td>${a.current_task_id || '-'}</td>
      <td>#${a.session_count || 1}</td>
      <td>${new Date(a.last_heartbeat).toLocaleTimeString()}</td>
      <td>
        <button onclick="quickHeartbeat('${a.id}')">PULSE</button>
      </td>
    </tr>
  ` + "`" + `).join('');
}

async function syncChats() {
  const res = await fetch('/api/messages');
  const msgs = await res.json();
  const box = document.getElementById('chat-stream');
  if (!msgs || msgs.length === 0) {
    box.innerHTML = '<div class="chat-line">No messages in project transcript yet. Broadcast above!</div>';
    return;
  }
  box.innerHTML = msgs.map(m => ` + "`" + `
    <div class="chat-line">
      <div class="chat-meta">
        [${new Date(m.created_at).toLocaleTimeString()}]
        <b>${m.from}</b> (${m.role}) -> <b>${m.to}</b>
        ${m.subject ? ' | <i>' + escapeHTML(m.subject) + '</i>' : ''}
      </div>
      <div class="chat-content">${escapeHTML(m.content)}</div>
    </div>
  ` + "`" + `).join('');
  box.scrollTop = box.scrollHeight;
}

async function syncMoE() {
  const res = await fetch('/api/moe');
  const panels = await res.json();
  const box = document.getElementById('moe-panels-list');
  if (!panels || panels.length === 0) {
    box.innerHTML = '<div>No MoE Panels convened yet. Convene one above!</div>';
    return;
  }
  box.innerHTML = panels.map(p => ` + "`" + `
    <div style="border: 1px solid #ffffff; padding: 12px; margin-bottom: 12px;">
      <div style="display: flex; justify-content: space-between;">
        <b>${p.id}: ${escapeHTML(p.title)}</b>
        <span class="badge">${p.status.toUpperCase()}</span>
      </div>
      <div style="margin: 6px 0; font-size: 12px;">DOMAIN: ${p.domain} // PARALLEL WORKERS: ${p.max_parallel}</div>
      <div style="margin-bottom: 8px;">${escapeHTML(p.query)}</div>
      
      <div style="border-top: 1px dashed #ffffff; padding-top: 6px; margin-top: 6px;">
        <b>EXPERTS SUMMONED:</b> ${(p.selected_experts || []).map(e => e.role).join(', ')}
      </div>

      ${p.synthesis ? ` + "`" + `
        <div style="margin-top: 8px; border: 1px solid #ffffff; padding: 8px;">
          <b>CONSENSUS DECISION:</b> ${escapeHTML(p.synthesis.consensus_decision)}
          <div style="margin-top: 4px; font-size: 12px;">
            <b>PARALLEL WORK PACKAGES:</b> ${(p.synthesis.parallel_work_packages || []).length} items
          </div>
        </div>
      ` + "`" + ` : ` + "`" + `
        <div style="margin-top: 8px;">
          <button onclick="promptSynthesize('${p.id}')">SYNTHESIZE CONSENSUS</button>
        </div>
      ` + "`" + `}

      ${(p.subagent_tracks && p.subagent_tracks.length > 0) ? ` + "`" + `
        <div style="margin-top: 8px; font-size: 12px;">
          <b>⚡ PARALLEL SUBAGENTS RUNNING:</b> ${p.subagent_tracks.length} active tracks
        </div>
      ` + "`" + ` : (p.synthesis ? ` + "`" + `
        <div style="margin-top: 8px;">
          <button onclick="dispatchSubagents('${p.id}')">🚀 DISPATCH PARALLEL SUBAGENTS</button>
        </div>
      ` + "`" + ` : '')}
    </div>
  ` + "`" + `).join('');
}

async function syncSettings() {
  const res = await fetch('/api/settings');
  const data = await res.json();
  if (data.project_name && !document.getElementById('set-proj-name').value) {
    document.getElementById('set-proj-name').value = data.project_name;
  }
  const grBox = document.getElementById('settings-guardrails-content');
  if (data.guardrails) {
    const g = data.guardrails;
    grBox.innerHTML = ` + "`" + `
      <div style="margin-bottom: 8px;"><b>TECH STACK:</b> ${(g.tech_stack || []).join(', ') || 'General'}</div>
      <div style="margin-bottom: 8px;"><b>PROJECT INVARIANTS:</b> ${(g.invariants || []).join('; ') || 'None set'}</div>
      <div><b>CAUTION RULES:</b> ${(g.caution_rules || []).length} active security rules</div>
    ` + "`" + `;
  }
}

async function createTask() {
  const title = document.getElementById('task-title').value.trim();
  if (!title) return alert('Task title required');
  const role = document.getElementById('task-role').value;
  const priority = document.getElementById('task-priority').value;
  const description = document.getElementById('task-desc').value;
  const bbRaw = document.getElementById('task-blocked-by').value;
  const blocked_by = bbRaw ? bbRaw.split(',').map(s => s.trim()) : [];

  await fetch('/api/tasks', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({title, role, priority, description, blocked_by})
  });
  document.getElementById('task-title').value = '';
  document.getElementById('task-desc').value = '';
  document.getElementById('task-blocked-by').value = '';
  await syncTasks();
}

async function quickUpdateTask(task_id, status) {
  await fetch('/api/tasks/update', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({task_id, status})
  });
  await syncTasks();
}

async function registerAgent() {
  const id = document.getElementById('agent-reg-id').value.trim();
  if (!id) return alert('Agent ID required');
  const role = document.getElementById('agent-reg-role').value;
  const current_task_id = document.getElementById('agent-reg-task').value.trim();

  await fetch('/api/agents', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({id, role, current_task_id, status: 'active'})
  });
  document.getElementById('agent-reg-id').value = '';
  document.getElementById('agent-reg-task').value = '';
  await syncAgents();
}

async function quickHeartbeat(id) {
  await fetch('/api/agents', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({id, role: 'worker', status: 'active'})
  });
  await syncAgents();
}

async function sendMessage() {
  const from = document.getElementById('chat-from').value.trim() || 'user';
  const to = document.getElementById('chat-to').value.trim() || 'all';
  const subject = document.getElementById('chat-subject').value.trim();
  const content = document.getElementById('chat-content').value.trim();
  if (!content) return;

  await fetch('/api/messages', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({from, role: 'operator', to, subject, content})
  });
  document.getElementById('chat-content').value = '';
  await syncChats();
}

async function conveneMoE() {
  const title = document.getElementById('moe-title').value.trim();
  const domain = document.getElementById('moe-domain').value.trim();
  const max_parallel = parseInt(document.getElementById('moe-parallel').value) || 4;
  const query = document.getElementById('moe-query').value.trim();
  if (!title || !query) return alert('Title and Problem Statement required');

  await fetch('/api/moe/convene', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({title, query, domain, initiator: 'web-user', max_parallel})
  });
  document.getElementById('moe-title').value = '';
  document.getElementById('moe-domain').value = '';
  document.getElementById('moe-query').value = '';
  await syncMoE();
}

async function promptSynthesize(panel_id) {
  const decision = prompt('Enter MoE Council Consensus Decision:');
  if (!decision) return;
  await fetch('/api/moe/synthesize', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({
      panel_id,
      agent_id: 'web-orchestrator',
      consensus_decision: decision,
      trade_offs: ['Parallel speedup vs branch merge complexity'],
      recommended_actions: ['Execute parallel work packages'],
      packages: [
        {package_id: 'p1', title: 'Implementation Track Alpha', assigned_role: 'worker', can_run_in_parallel: true},
        {package_id: 'p2', title: 'Adversarial Verification Beta', assigned_role: 'red_team', can_run_in_parallel: true}
      ]
    })
  });
  await syncMoE();
}

async function dispatchSubagents(panel_id) {
  await fetch('/api/moe/dispatch', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({panel_id, agent_id: 'web-orchestrator'})
  });
  await syncMoE();
}

async function updateSettings() {
  const project_name = document.getElementById('set-proj-name').value.trim();
  const goal_title = document.getElementById('set-goal-title').value.trim();
  const goal_desc = document.getElementById('set-goal-desc').value.trim();

  await fetch('/api/settings', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({project_name, goal_title, goal_desc})
  });
  alert('Settings saved!');
  await syncSettings();
}

function escapeHTML(str) {
  if (!str) return '';
  return str.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

// Auto-sync polling every 2.5 seconds
setInterval(syncData, 2500);
syncData();
</script>

</body>
</html>
`
