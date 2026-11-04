package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/tasmirz/gossip/pkg/deliberation"
	"github.com/tasmirz/gossip/pkg/events"
	"github.com/tasmirz/gossip/pkg/graph"
	"github.com/tasmirz/gossip/pkg/guardrails"
	"github.com/tasmirz/gossip/pkg/messages"
	"github.com/tasmirz/gossip/pkg/roles"
	"github.com/tasmirz/gossip/pkg/security"
	"github.com/tasmirz/gossip/pkg/sop"
	"github.com/tasmirz/gossip/pkg/state"
	"github.com/tasmirz/gossip/pkg/tasks"
)

// Server encapsulates the Gossip MCP server and underlying subsystems.
type Server struct {
	mcpServer     *server.MCPServer
	store         *state.Store
	messages      *messages.Manager
	tasks         *tasks.Manager
	graph         *graph.Manager
	deliberations *deliberation.Manager
	sop           *sop.Manager
	guardrails    *guardrails.Manager
	events        *events.Manager
	roleProvider  roles.Provider
}

// NewServer instantiates the MCP server and registers all tools (SOLID Dependency Inversion).
func NewServer(store *state.Store, roleProv roles.Provider) *Server {
	if roleProv == nil {
		var err error
		roleProv, err = roles.NewService(store.Dir(), "")
		if err != nil {
			roleProv = roles.GetDefaultService()
		}
	}

	msgMgr := messages.NewManager(store)
	taskMgr := tasks.NewManager(store)
	delibMgr := deliberation.NewManager(store)
	sopMgr := sop.NewManager(store)
	evtMgr := events.NewManager(store, msgMgr, taskMgr, delibMgr, sopMgr)

	s := &Server{
		store:         store,
		messages:      msgMgr,
		tasks:         taskMgr,
		graph:         graph.NewManager(store),
		deliberations: delibMgr,
		sop:           sopMgr,
		guardrails:    guardrails.NewManager(store),
		events:        evtMgr,
		roleProvider:  roleProv,
	}

	mcpSrv := server.NewMCPServer(
		"Gossip Agent Mesh",
		"1.0.0",
		server.WithToolCapabilities(true),
		server.WithPromptCapabilities(true),
	)

	s.mcpServer = mcpSrv
	s.registerTools()
	s.registerPrompts()
	return s
}

// ServeStdio starts listening for JSON-RPC 2.0 messages over standard I/O.
func (s *Server) ServeStdio() error {
	return server.ServeStdio(s.mcpServer)
}

// MCPServer returns the underlying mark3labs MCPServer instance.
func (s *Server) MCPServer() *server.MCPServer {
	return s.mcpServer
}

func (s *Server) registerTools() {
	s.registerRoleTools()
	s.registerMessageTools()
	s.registerTaskTools()
	s.registerGraphTools()
	s.registerDeliberationTools()
	s.registerSOPTools()
	s.registerGuardrailTools()
	s.registerEventTools()
}

// --- Helper Functions for Extracting Tool Arguments ---

func getArgsMap(req mcp.CallToolRequest) map[string]any {
	if req.Params.Arguments == nil {
		return make(map[string]any)
	}
	if m, ok := req.Params.Arguments.(map[string]any); ok {
		return m
	}
	var m map[string]any
	data, err := json.Marshal(req.Params.Arguments)
	if err == nil {
		_ = json.Unmarshal(data, &m)
	}
	if m == nil {
		m = make(map[string]any)
	}
	return m
}

func getString(args map[string]any, key string) string {
	if v, ok := args[key]; ok {
		if str, ok := v.(string); ok {
			return strings.TrimSpace(str)
		}
	}
	return ""
}

func getBool(args map[string]any, key string) bool {
	if v, ok := args[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

func getInt(args map[string]any, key string, defaultVal int) int {
	if v, ok := args[key]; ok {
		switch num := v.(type) {
		case float64:
			return int(num)
		case int:
			return num
		case int64:
			return int(num)
		}
	}
	return defaultVal
}

func getFloat(args map[string]any, key string, defaultVal float64) float64 {
	if v, ok := args[key]; ok {
		switch num := v.(type) {
		case float64:
			return num
		case float32:
			return float64(num)
		case int:
			return float64(num)
		case int64:
			return float64(num)
		}
	}
	return defaultVal
}

func getStringSlice(args map[string]any, key string) []string {
	if v, ok := args[key]; ok {
		if arr, ok := v.([]any); ok {
			var res []string
			for _, item := range arr {
				if str, ok := item.(string); ok && str != "" {
					res = append(res, strings.TrimSpace(str))
				}
			}
			return res
		}
		if arr, ok := v.([]string); ok {
			return arr
		}
	}
	return nil
}

func toJSONText(v any) (*mcp.CallToolResult, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("serialization error: %v", err)), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func parseTimeFlexible(val string) (time.Time, error) {
	val = strings.TrimSpace(val)
	if val == "" {
		return time.Time{}, nil
	}

	cleanVal := strings.TrimSuffix(val, " ago")
	if d, err := time.ParseDuration(cleanVal); err == nil {
		return time.Now().Add(-d), nil
	}

	if t, err := time.Parse(time.RFC3339, val); err == nil {
		return t, nil
	}

	if t, err := time.Parse(time.RFC3339Nano, val); err == nil {
		return t, nil
	}

	if t, err := time.Parse("2006-01-02 15:04:05", val); err == nil {
		return t, nil
	}

	return time.Time{}, fmt.Errorf("unable to parse time '%s' (use RFC3339 or relative duration like '30s', '5m')", val)
}

// --- Role & Identity Tools ---

func (s *Server) registerRoleTools() {
	// gossip_register_agent
	s.mcpServer.AddTool(mcp.NewTool("gossip_register_agent",
		mcp.WithDescription("Register an agent session in the gossip mesh with its designated role and capabilities"),
		mcp.WithString("agent_id", mcp.Required(), mcp.Description("Unique identifier for this agent session (e.g. 'orch-1', 'neteng-1')")),
		mcp.WithString("role", mcp.Required(), mcp.Description("Role ID (orchestrator, worker, network_engineer, math_expert, systems_architect, security_specialist, qa_engineer, researcher)")),
		mcp.WithString("name", mcp.Description("Human-readable agent display name")),
		mcp.WithArray("capabilities", mcp.Description("List of capability tags or skills")),
		mcp.WithString("current_task_id", mcp.Description("Task ID currently being worked on")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		agentID := getString(args, "agent_id")
		role := getString(args, "role")
		name := getString(args, "name")
		if name == "" {
			name = agentID
		}
		caps := getStringSlice(args, "capabilities")
		currentTaskID := getString(args, "current_task_id")

		if agentID == "" || role == "" {
			return mcp.NewToolResultError("agent_id and role are required"), nil
		}

		session := state.AgentSession{
			ID:            agentID,
			Name:          name,
			Role:          role,
			Capabilities:  caps,
			CurrentTaskID: currentTaskID,
			Status:        "active",
		}

		if err := s.store.RegisterAgent(session); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to register agent: %v", err)), nil
		}

		return mcp.NewToolResultText(fmt.Sprintf("Agent '%s' successfully registered as role '%s'", agentID, role)), nil
	})

	// gossip_list_agents
	s.mcpServer.AddTool(mcp.NewTool("gossip_list_agents",
		mcp.WithDescription("List all registered agents, their current status, roles, tasks, and last heartbeats"),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		agents, err := s.store.ListAgents()
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to list agents: %v", err)), nil
		}
		return toJSONText(agents)
	})

	// gossip_heartbeat
	s.mcpServer.AddTool(mcp.NewTool("gossip_heartbeat",
		mcp.WithDescription("Send a heartbeat to keep agent session active and update current task or status"),
		mcp.WithString("agent_id", mcp.Required(), mcp.Description("Unique identifier for this agent")),
		mcp.WithString("current_task_id", mcp.Description("Current task ID being worked on")),
		mcp.WithString("status", mcp.Description("Session status: 'active', 'idle', or 'offline'")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		agentID := getString(args, "agent_id")
		currentTaskID := getString(args, "current_task_id")
		status := getString(args, "status")

		if agentID == "" {
			return mcp.NewToolResultError("agent_id is required"), nil
		}

		if err := s.store.Heartbeat(agentID, currentTaskID, status); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("heartbeat failed: %v", err)), nil
		}

		return mcp.NewToolResultText("Heartbeat recorded successfully"), nil
	})

	// gossip_get_role_definition
	s.mcpServer.AddTool(mcp.NewTool("gossip_get_role_definition",
		mcp.WithDescription("Retrieve system prompt, responsibilities, guidelines, and recommended tools for a specific role"),
		mcp.WithString("role_id", mcp.Required(), mcp.Description("Role ID (loaded dynamically from JSON)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		roleID := getString(getArgsMap(req), "role_id")
		r, ok := s.roleProvider.GetRole(roleID)
		if !ok {
			return mcp.NewToolResultError(fmt.Sprintf("unknown role '%s'. Call gossip_list_roles to see available roles loaded from JSON", roleID)), nil
		}
		return toJSONText(r)
	})

	// gossip_list_roles
	s.mcpServer.AddTool(mcp.NewTool("gossip_list_roles",
		mcp.WithDescription("List all available specialist roles loaded dynamically from JSON in Markdown"),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText(s.roleProvider.FormatRoleSummary()), nil
	})

	// gossip_register_custom_role
	s.mcpServer.AddTool(mcp.NewTool("gossip_register_custom_role",
		mcp.WithDescription("Dynamically define and register a new custom agent role at runtime (stored in JSON)"),
		mcp.WithString("id", mcp.Required(), mcp.Description("Unique role identifier (e.g. 'devops_engineer', 'quantum_expert')")),
		mcp.WithString("title", mcp.Required(), mcp.Description("Human-readable role title")),
		mcp.WithString("caller_role", mcp.Description("Role of the agent registering the custom role (e.g. 'orchestrator')")),
		mcp.WithString("category", mcp.Description("Category: 'management', 'engineering', 'specialist', 'assurance'")),
		mcp.WithString("description", mcp.Required(), mcp.Description("Detailed description of role responsibilities")),
		mcp.WithString("system_prompt", mcp.Required(), mcp.Description("System prompt and operational persona")),
		mcp.WithArray("directives", mcp.Description("List of behavioral rules/directives")),
		mcp.WithArray("recommended_tools", mcp.Description("List of recommended MCP tools")),
		mcp.WithArray("capability_tags", mcp.Description("List of capability tags")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		id := getString(args, "id")
		title := getString(args, "title")
		callerRole := getString(args, "caller_role")
		cat := getString(args, "category")
		desc := getString(args, "description")
		prompt := getString(args, "system_prompt")
		directives := getStringSlice(args, "directives")
		tools := getStringSlice(args, "recommended_tools")
		tags := getStringSlice(args, "capability_tags")

		if id == "" || title == "" || desc == "" || prompt == "" {
			return mcp.NewToolResultError("'id', 'title', 'description', and 'system_prompt' are required"), nil
		}

		if callerRole != "" {
			if err := security.AuthorizeAction(callerRole, "gossip_register_custom_role"); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
		}

		sanitizedID, err := security.SanitizeID(id, 64)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid role id: %v", err)), nil
		}

		if err := security.ValidatePayloadSize(desc+prompt, security.MaxMessageSizeBytes); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("role payload too large: %v", err)), nil
		}

		if cat == "" {
			cat = "specialist"
		}

		customRole := roles.RoleDefinition{
			ID:               sanitizedID,
			Title:            title,
			Category:         cat,
			Description:      desc,
			SystemPrompt:     prompt,
			Directives:       directives,
			RecommendedTools: tools,
			CapabilityTags:   tags,
		}

		if err := s.roleProvider.RegisterRole(customRole); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to register custom role: %v", err)), nil
		}

		// Save updated roles to project storage directory roles.json
		rolesPath := filepath.Join(s.store.Dir(), "roles.json")
		_ = s.roleProvider.SaveToFile(rolesPath)

		return mcp.NewToolResultText(fmt.Sprintf("Custom role '%s' successfully registered and saved to %s", id, rolesPath)), nil
	})
}

// --- Messaging Tools ---

func (s *Server) registerMessageTools() {
	// gossip_send_message
	s.mcpServer.AddTool(mcp.NewTool("gossip_send_message",
		mcp.WithDescription("Send a message to a specific agent, a role channel, or broadcast to all"),
		mcp.WithString("from", mcp.Required(), mcp.Description("Sender identifier (e.g. 'orch-1', 'math-1')")),
		mcp.WithString("role", mcp.Required(), mcp.Description("Sender's role (e.g. 'orchestrator', 'math_expert')")),
		mcp.WithString("to", mcp.Required(), mcp.Description("Target: 'agent:<id>', 'role:<role_id>', or 'all'")),
		mcp.WithString("content", mcp.Required(), mcp.Description("Message payload")),
		mcp.WithString("subject", mcp.Description("Short subject line")),
		mcp.WithString("thread_id", mcp.Description("Optional thread identifier to group replies")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		from := getString(args, "from")
		role := getString(args, "role")
		to := getString(args, "to")
		content := getString(args, "content")
		subject := getString(args, "subject")
		threadID := getString(args, "thread_id")

		if from == "" || role == "" || to == "" || content == "" {
			return mcp.NewToolResultError("'from', 'role', 'to', and 'content' are required"), nil
		}

		if err := security.ValidatePayloadSize(content, security.MaxMessageSizeBytes); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("message payload error: %v", err)), nil
		}

		msg, err := s.messages.SendMessage(from, role, to, threadID, subject, content)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to send message: %v", err)), nil
		}

		return toJSONText(msg)
	})

	// gossip_broadcast_message
	s.mcpServer.AddTool(mcp.NewTool("gossip_broadcast_message",
		mcp.WithDescription("Broadcast a high-priority message or announcement to all active agents in the project"),
		mcp.WithString("from", mcp.Required(), mcp.Description("Sender identifier")),
		mcp.WithString("role", mcp.Required(), mcp.Description("Sender role")),
		mcp.WithString("content", mcp.Required(), mcp.Description("Announcement content")),
		mcp.WithString("subject", mcp.Description("Announcement subject")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		from := getString(args, "from")
		role := getString(args, "role")
		content := getString(args, "content")
		subject := getString(args, "subject")

		if from == "" || role == "" || content == "" {
			return mcp.NewToolResultError("'from', 'role', and 'content' are required"), nil
		}

		if err := security.ValidatePayloadSize(content, security.MaxMessageSizeBytes); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("message payload error: %v", err)), nil
		}

		msg, err := s.messages.SendMessage(from, role, "all", "", subject, content)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to broadcast: %v", err)), nil
		}

		return toJSONText(msg)
	})

	// gossip_read_inbox
	s.mcpServer.AddTool(mcp.NewTool("gossip_read_inbox",
		mcp.WithDescription("Read messages sent to this agent or this agent's role"),
		mcp.WithString("agent_id", mcp.Required(), mcp.Description("Your agent ID")),
		mcp.WithString("role", mcp.Required(), mcp.Description("Your agent role")),
		mcp.WithBoolean("only_unread", mcp.Description("If true, return only unread messages (default: false)")),
		mcp.WithString("thread_id", mcp.Description("Filter by conversation thread ID")),
		mcp.WithInteger("limit", mcp.Description("Max messages to return (default: 20)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		agentID := getString(args, "agent_id")
		role := getString(args, "role")
		onlyUnread := getBool(args, "only_unread")
		threadID := getString(args, "thread_id")
		limit := getInt(args, "limit", 20)

		if agentID == "" || role == "" {
			return mcp.NewToolResultError("agent_id and role are required"), nil
		}

		msgs, err := s.messages.ReadInbox(agentID, role, onlyUnread, threadID, limit)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to read inbox: %v", err)), nil
		}

		return toJSONText(msgs)
	})

	// gossip_mark_messages_read
	s.mcpServer.AddTool(mcp.NewTool("gossip_mark_messages_read",
		mcp.WithDescription("Mark messages as read for this agent"),
		mcp.WithString("agent_id", mcp.Required(), mcp.Description("Agent ID marking the messages as read")),
		mcp.WithArray("message_ids", mcp.Required(), mcp.Description("List of message IDs to mark read")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		agentID := getString(args, "agent_id")
		msgIDs := getStringSlice(args, "message_ids")

		if agentID == "" || len(msgIDs) == 0 {
			return mcp.NewToolResultError("agent_id and message_ids are required"), nil
		}

		if err := s.messages.MarkRead(agentID, msgIDs); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to mark messages read: %v", err)), nil
		}

		return mcp.NewToolResultText("Messages successfully marked as read"), nil
	})

	// gossip_list_threads
	s.mcpServer.AddTool(mcp.NewTool("gossip_list_threads",
		mcp.WithDescription("List active discussion threads and participant summaries"),
		mcp.WithInteger("limit", mcp.Description("Max threads to return (default: 15)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		limit := getInt(getArgsMap(req), "limit", 15)
		threads, err := s.messages.ListThreads(limit)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to list threads: %v", err)), nil
		}
		return toJSONText(threads)
	})
}

// --- Task & Goal Tools ---

func (s *Server) registerTaskTools() {
	// gossip_set_goal
	s.mcpServer.AddTool(mcp.NewTool("gossip_set_goal",
		mcp.WithDescription("Set or update high-level strategic goals and milestone checkpoints"),
		mcp.WithString("title", mcp.Required(), mcp.Description("Goal title")),
		mcp.WithString("description", mcp.Required(), mcp.Description("Detailed description of the goal")),
		mcp.WithString("caller_role", mcp.Description("Role of the agent setting the goal (e.g. 'orchestrator')")),
		mcp.WithArray("milestone_titles", mcp.Description("List of milestone checkpoint titles")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		title := getString(args, "title")
		desc := getString(args, "description")
		callerRole := getString(args, "caller_role")
		if callerRole == "" {
			callerRole = getString(args, "role")
		}
		mTitles := getStringSlice(args, "milestone_titles")

		if title == "" || desc == "" {
			return mcp.NewToolResultError("title and description are required"), nil
		}

		if callerRole != "" {
			if err := security.AuthorizeAction(callerRole, "gossip_set_goal"); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
		}

		if err := security.ValidatePayloadSize(desc, security.MaxMessageSizeBytes); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("goal description too large: %v", err)), nil
		}

		var milestones []state.Milestone
		for i, mt := range mTitles {
			milestones = append(milestones, state.Milestone{
				ID:        fmt.Sprintf("ms-%d", i+1),
				Title:     mt,
				Completed: false,
			})
		}

		goal, err := s.store.SetGoal(title, desc, milestones)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to set goal: %v", err)), nil
		}

		return toJSONText(goal)
	})

	// gossip_get_goals
	s.mcpServer.AddTool(mcp.NewTool("gossip_get_goals",
		mcp.WithDescription("Get active project goals, milestones, and status"),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		goals, err := s.store.GetGoals()
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to get goals: %v", err)), nil
		}
		return toJSONText(goals)
	})

	// gossip_create_task
	s.mcpServer.AddTool(mcp.NewTool("gossip_create_task",
		mcp.WithDescription("Create a new task in the shared DAG with role target, priority, dependencies, and acceptance criteria"),
		mcp.WithString("title", mcp.Required(), mcp.Description("Task title")),
		mcp.WithString("description", mcp.Required(), mcp.Description("Detailed task specification and requirements")),
		mcp.WithString("role", mcp.Description("Target specialist role (e.g. 'network_engineer', 'math_expert', 'worker')")),
		mcp.WithString("priority", mcp.Description("Priority: 'low', 'normal', 'high', 'critical' (default: 'normal')")),
		mcp.WithArray("blocked_by", mcp.Description("List of prerequisite task IDs that must complete first")),
		mcp.WithArray("acceptance_criteria", mcp.Description("List of acceptance criteria that must be verified")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		title := getString(args, "title")
		desc := getString(args, "description")
		role := getString(args, "role")
		priority := getString(args, "priority")
		blockedBy := getStringSlice(args, "blocked_by")
		criteria := getStringSlice(args, "acceptance_criteria")

		if title == "" || desc == "" {
			return mcp.NewToolResultError("title and description are required"), nil
		}

		if err := security.ValidatePayloadSize(desc, security.MaxMessageSizeBytes); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("task description too large: %v", err)), nil
		}

		task, err := s.tasks.CreateTask(title, desc, role, priority, blockedBy, criteria)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to create task: %v", err)), nil
		}

		return toJSONText(task)
	})

	// gossip_update_task
	s.mcpServer.AddTool(mcp.NewTool("gossip_update_task",
		mcp.WithDescription("Update task status, assign an agent, append progress notes, or register result artifacts"),
		mcp.WithString("task_id", mcp.Required(), mcp.Description("Task ID to update")),
		mcp.WithString("status", mcp.Description("New status: 'todo', 'in_progress', 'blocked', 'in_review', 'completed', 'failed'")),
		mcp.WithString("assigned_to", mcp.Description("Agent ID claiming or assigned to this task")),
		mcp.WithString("progress_note", mcp.Description("Progress report, notes, or findings to append")),
		mcp.WithString("artifact", mcp.Description("File path, PR, or deliverable produced")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		taskID := getString(args, "task_id")
		status := getString(args, "status")
		assignedTo := getString(args, "assigned_to")
		progressNote := getString(args, "progress_note")
		artifact := getString(args, "artifact")

		if taskID == "" {
			return mcp.NewToolResultError("task_id is required"), nil
		}

		if progressNote != "" {
			if err := security.ValidatePayloadSize(progressNote, security.MaxMessageSizeBytes); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("progress note too large: %v", err)), nil
			}
		}

		if artifact != "" {
			if _, err := security.ValidateSafePath(s.store.GetBaseDir(), artifact); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("unsafe artifact path: %v", err)), nil
			}
		}

		task, err := s.tasks.UpdateTask(taskID, status, assignedTo, progressNote, artifact)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to update task: %v", err)), nil
		}

		return toJSONText(task)
	})

	// gossip_list_tasks
	s.mcpServer.AddTool(mcp.NewTool("gossip_list_tasks",
		mcp.WithDescription("List tasks in the shared DAG with optional filtering by status, role, or assignee"),
		mcp.WithString("status", mcp.Description("Filter by status ('todo', 'in_progress', 'blocked', 'in_review', 'completed')")),
		mcp.WithString("role", mcp.Description("Filter by target role")),
		mcp.WithString("assigned_to", mcp.Description("Filter by assigned agent ID")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		status := getString(args, "status")
		role := getString(args, "role")
		assignee := getString(args, "assigned_to")

		tasksList, err := s.tasks.ListTasks(status, role, assignee)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to list tasks: %v", err)), nil
		}

		return toJSONText(tasksList)
	})

	// gossip_get_task
	s.mcpServer.AddTool(mcp.NewTool("gossip_get_task",
		mcp.WithDescription("Retrieve full details, notes, blockers, and artifacts for a single task"),
		mcp.WithString("task_id", mcp.Required(), mcp.Description("Task ID")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		taskID := getString(getArgsMap(req), "task_id")
		task, err := s.tasks.GetTask(taskID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("task error: %v", err)), nil
		}
		return toJSONText(task)
	})

	// gossip_render_board
	s.mcpServer.AddTool(mcp.NewTool("gossip_render_board",
		mcp.WithDescription("Render and return the live Markdown Kanban board (BOARD.md) representing current multi-agent work state"),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		board, err := s.tasks.RenderBoard()
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to render board: %v", err)), nil
		}
		return mcp.NewToolResultText(board), nil
	})

	// gossip_personal_todo_add
	s.mcpServer.AddTool(mcp.NewTool("gossip_personal_todo_add",
		mcp.WithDescription("Add a personal checklist item or scratchpad task for an agent to track internal sub-steps"),
		mcp.WithString("agent_id", mcp.Required(), mcp.Description("Agent ID")),
		mcp.WithString("title", mcp.Required(), mcp.Description("Personal to-do item title")),
		mcp.WithString("priority", mcp.Description("Priority: 'low', 'normal', 'high' (default: 'normal')")),
		mcp.WithString("context_task_id", mcp.Description("Parent global DAG task ID if applicable")),
		mcp.WithString("notes", mcp.Description("Working notes, scratchpad remarks, or implementation thoughts")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		agentID := getString(args, "agent_id")
		title := getString(args, "title")
		priority := getString(args, "priority")
		contextTaskID := getString(args, "context_task_id")
		notes := getString(args, "notes")

		if agentID == "" || title == "" {
			return mcp.NewToolResultError("'agent_id' and 'title' are required"), nil
		}

		item, err := s.tasks.AddPersonalTodo(agentID, title, priority, contextTaskID, notes)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to add personal todo: %v", err)), nil
		}
		return toJSONText(item)
	})

	// gossip_personal_todo_list
	s.mcpServer.AddTool(mcp.NewTool("gossip_personal_todo_list",
		mcp.WithDescription("List an agent's personal to-do scratchpad items and working checklist"),
		mcp.WithString("agent_id", mcp.Required(), mcp.Description("Agent ID")),
		mcp.WithString("status", mcp.Description("Filter by status: 'pending', 'in_progress', 'done'")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		agentID := getString(args, "agent_id")
		status := getString(args, "status")

		if agentID == "" {
			return mcp.NewToolResultError("'agent_id' is required"), nil
		}

		items, err := s.tasks.ListPersonalTodos(agentID, status)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to list personal todos: %v", err)), nil
		}
		return toJSONText(items)
	})

	// gossip_personal_todo_update
	s.mcpServer.AddTool(mcp.NewTool("gossip_personal_todo_update",
		mcp.WithDescription("Update status or notes for an agent's personal checklist item"),
		mcp.WithString("agent_id", mcp.Required(), mcp.Description("Agent ID")),
		mcp.WithString("item_id", mcp.Required(), mcp.Description("Personal to-do item ID")),
		mcp.WithString("status", mcp.Description("New status: 'pending', 'in_progress', 'done'")),
		mcp.WithString("notes", mcp.Description("Updated scratchpad notes")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		agentID := getString(args, "agent_id")
		itemID := getString(args, "item_id")
		status := getString(args, "status")
		notes := getString(args, "notes")

		if agentID == "" || itemID == "" {
			return mcp.NewToolResultError("'agent_id' and 'item_id' are required"), nil
		}

		updated, err := s.tasks.UpdatePersonalTodo(agentID, itemID, status, notes)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to update personal todo: %v", err)), nil
		}
		return toJSONText(updated)
	})

	// gossip_personal_todo_delete
	s.mcpServer.AddTool(mcp.NewTool("gossip_personal_todo_delete",
		mcp.WithDescription("Delete an item from an agent's personal checklist"),
		mcp.WithString("agent_id", mcp.Required(), mcp.Description("Agent ID")),
		mcp.WithString("item_id", mcp.Required(), mcp.Description("Personal to-do item ID")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		agentID := getString(args, "agent_id")
		itemID := getString(args, "item_id")

		if agentID == "" || itemID == "" {
			return mcp.NewToolResultError("'agent_id' and 'item_id' are required"), nil
		}

		if err := s.tasks.DeletePersonalTodo(agentID, itemID); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to delete personal todo: %v", err)), nil
		}
		return toJSONText(map[string]string{"status": "deleted", "id": itemID})
	})

	// gossip_system_todo_list
	s.mcpServer.AddTool(mcp.NewTool("gossip_system_todo_list",
		mcp.WithDescription("Retrieve unified system-wide backlog aggregating unassigned ready DAG tasks, incomplete milestones, pending SOP phase gates, and open security vulnerabilities"),
		mcp.WithString("format", mcp.Description("Output format: 'json' or 'markdown' (default: 'json')")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		format := getString(args, "format")

		sysList, err := s.tasks.GetSystemTodoList()
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to get system todos: %v", err)), nil
		}

		if format == "markdown" || format == "md" {
			return mcp.NewToolResultText(sysList.RenderMarkdown()), nil
		}
		return toJSONText(sysList)
	})

	// gossip_orchestrator_assignments
	s.mcpServer.AddTool(mcp.NewTool("gossip_orchestrator_assignments",
		mcp.WithDescription("Orchestrator central assignment matrix: active tasks per agent, workload distribution, bottleneck tasks, and idle-worker delegation suggestions"),
		mcp.WithString("format", mcp.Description("Output format: 'json' or 'markdown' (default: 'json')")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		format := getString(args, "format")

		matrix, err := s.tasks.GetOrchestratorAssignments()
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to get orchestrator assignments: %v", err)), nil
		}

		if format == "markdown" || format == "md" {
			return mcp.NewToolResultText(matrix.RenderMarkdown()), nil
		}
		return toJSONText(matrix)
	})
}

// --- Knowledge Graph RAG Tools ---

func (s *Server) registerGraphTools() {
	// gossip_rag_add_node
	s.mcpServer.AddTool(mcp.NewTool("gossip_rag_add_node",
		mcp.WithDescription("Add a concept, file, code symbol, architectural decision, constraint, or formal proof to the shared knowledge graph"),
		mcp.WithString("id", mcp.Description("Unique node ID (e.g. 'decision:adr-001', 'concept:crc32', 'file:pkg/mcp/server.go')")),
		mcp.WithString("type", mcp.Required(), mcp.Description("Node type: 'concept', 'file', 'symbol', 'decision', 'constraint', 'proof', 'architecture'")),
		mcp.WithString("label", mcp.Required(), mcp.Description("Human-readable label or entity name")),
		mcp.WithString("summary", mcp.Required(), mcp.Description("Detailed description, specification, mathematical proof, or rationale")),
		mcp.WithArray("tags", mcp.Description("Categorical tags for filtering")),
		mcp.WithString("source_path", mcp.Description("Relevant source file path")),
		mcp.WithString("created_by", mcp.Description("Author agent ID")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		id := getString(args, "id")
		nodeType := getString(args, "type")
		label := getString(args, "label")
		summary := getString(args, "summary")
		tags := getStringSlice(args, "tags")
		sourcePath := getString(args, "source_path")
		createdBy := getString(args, "created_by")

		if nodeType == "" || label == "" || summary == "" {
			return mcp.NewToolResultError("'type', 'label', and 'summary' are required"), nil
		}

		if err := security.ValidatePayloadSize(summary, security.MaxMessageSizeBytes); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("node summary too large: %v", err)), nil
		}

		if id != "" {
			if _, err := security.SanitizeID(id, 128); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid node id: %v", err)), nil
			}
		}

		if sourcePath != "" {
			if _, err := security.ValidateSafePath(s.store.GetBaseDir(), sourcePath); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("unsafe source path: %v", err)), nil
			}
		}

		node := graph.Node{
			ID:         id,
			Type:       nodeType,
			Label:      label,
			Summary:    summary,
			Tags:       tags,
			SourcePath: sourcePath,
			CreatedBy:  createdBy,
		}

		saved, err := s.graph.AddNode(node)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to add node: %v", err)), nil
		}

		return toJSONText(saved)
	})

	// gossip_rag_add_edge
	s.mcpServer.AddTool(mcp.NewTool("gossip_rag_add_edge",
		mcp.WithDescription("Add a directed, typed relationship between two nodes in the shared knowledge graph with provenance tracking"),
		mcp.WithString("source", mcp.Required(), mcp.Description("Source node ID")),
		mcp.WithString("target", mcp.Required(), mcp.Description("Target node ID")),
		mcp.WithString("relation", mcp.Required(), mcp.Description("Relation: 'depends_on', 'implements', 'verifies', 'calls', 'solves', 'conflicts_with', 'references'")),
		mcp.WithString("provenance", mcp.Description("Provenance: 'EXTRACTED' (explicit code/file structural link) or 'INFERRED' (deduced by specialist agent)")),
		mcp.WithString("summary", mcp.Description("Brief reason or explanation for this connection")),
		mcp.WithString("created_by", mcp.Description("Author agent ID")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		src := getString(args, "source")
		tgt := getString(args, "target")
		rel := getString(args, "relation")
		prov := getString(args, "provenance")
		summary := getString(args, "summary")
		createdBy := getString(args, "created_by")

		if src == "" || tgt == "" || rel == "" {
			return mcp.NewToolResultError("'source', 'target', and 'relation' are required"), nil
		}

		edge := graph.Edge{
			Source:     src,
			Target:     tgt,
			Relation:   rel,
			Provenance: prov,
			Summary:    summary,
			CreatedBy:  createdBy,
		}

		saved, err := s.graph.AddEdge(edge)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to add edge: %v", err)), nil
		}

		return toJSONText(saved)
	})

	// gossip_rag_query
	s.mcpServer.AddTool(mcp.NewTool("gossip_rag_query",
		mcp.WithDescription("Search the shared knowledge graph using keywords, type filter, and tag filter with relevance ranking"),
		mcp.WithString("query", mcp.Description("Search terms")),
		mcp.WithString("type", mcp.Description("Filter by node type ('concept', 'file', 'symbol', 'decision', 'constraint', 'proof')")),
		mcp.WithString("tag", mcp.Description("Filter by tag")),
		mcp.WithInteger("limit", mcp.Description("Max results to return (default: 10)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		queryStr := getString(args, "query")
		nodeType := getString(args, "type")
		tag := getString(args, "tag")
		limit := getInt(args, "limit", 10)

		results, err := s.graph.Query(queryStr, nodeType, tag, limit)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("query error: %v", err)), nil
		}

		return toJSONText(results)
	})

	// gossip_rag_get_neighborhood
	s.mcpServer.AddTool(mcp.NewTool("gossip_rag_get_neighborhood",
		mcp.WithDescription("Extract connected subgraphs around a node within 1 to 3 hops to understand dependencies and impact"),
		mcp.WithString("node_id", mcp.Required(), mcp.Description("Center node ID")),
		mcp.WithInteger("depth", mcp.Description("Radius in hops: 1 or 2 (default: 1)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		nodeID := getString(args, "node_id")
		depth := getInt(args, "depth", 1)

		if nodeID == "" {
			return mcp.NewToolResultError("node_id is required"), nil
		}

		nh, err := s.graph.GetNeighborhood(nodeID, depth)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("neighborhood error: %v", err)), nil
		}

		return toJSONText(nh)
	})

	// gossip_rag_find_path
	s.mcpServer.AddTool(mcp.NewTool("gossip_rag_find_path",
		mcp.WithDescription("Find the shortest relationship path between two concepts/nodes to trace transitive dependencies or conflicts"),
		mcp.WithString("source", mcp.Required(), mcp.Description("Source node ID")),
		mcp.WithString("target", mcp.Required(), mcp.Description("Target node ID")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		src := getString(args, "source")
		tgt := getString(args, "target")

		if src == "" || tgt == "" {
			return mcp.NewToolResultError("source and target are required"), nil
		}

		path, edges, err := s.graph.FindPath(src, tgt)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("path error: %v", err)), nil
		}

		resp := map[string]any{
			"source":     src,
			"target":     tgt,
			"path_nodes": path,
			"path_edges": edges,
			"hop_count":  len(edges),
		}
		return toJSONText(resp)
	})

	// gossip_rag_export_report
	s.mcpServer.AddTool(mcp.NewTool("gossip_rag_export_report",
		mcp.WithDescription("Generate and return the comprehensive GRAPH_REPORT.md summarizing knowledge hubs, provenance, decisions, and proofs"),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		report, err := s.graph.GenerateReport()
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to generate report: %v", err)), nil
		}
		return mcp.NewToolResultText(report), nil
	})

	// gossip_rag_pagerank
	s.mcpServer.AddTool(mcp.NewTool("gossip_rag_pagerank",
		mcp.WithDescription("Compute PageRank centrality scores across all knowledge nodes to identify architecturally critical components (inspired by Aider repo-map)"),
		mcp.WithNumber("damping", mcp.Description("PageRank damping factor (default: 0.85)")),
		mcp.WithInteger("max_iterations", mcp.Description("Max iterations for power-method convergence (default: 100)")),
		mcp.WithNumber("tolerance", mcp.Description("Convergence tolerance threshold (default: 0.00001)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		damping := getFloat(args, "damping", 0.85)
		maxIter := getInt(args, "max_iterations", 100)
		tol := getFloat(args, "tolerance", 0.00001)

		scores, err := s.graph.ComputePageRank(damping, maxIter, tol)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("pagerank error: %v", err)), nil
		}
		return toJSONText(scores)
	})

	// gossip_rag_impact_analysis
	s.mcpServer.AddTool(mcp.NewTool("gossip_rag_impact_analysis",
		mcp.WithDescription("Perform blast-radius impact analysis when modifying a node/symbol/file, discovering all direct and indirect dependents (inspired by Graphify affected.py)"),
		mcp.WithString("seed_node_id", mcp.Required(), mcp.Description("Node, symbol, or file ID to analyze")),
		mcp.WithInteger("depth", mcp.Description("Max reverse traversal depth (default: 2)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		seedID := getString(args, "seed_node_id")
		depth := getInt(args, "depth", 2)

		if seedID == "" {
			return mcp.NewToolResultError("seed_node_id is required"), nil
		}

		report, err := s.graph.AffectedNodes(seedID, depth)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("impact analysis error: %v", err)), nil
		}
		return toJSONText(report)
	})

	// gossip_rag_clusters
	s.mcpServer.AddTool(mcp.NewTool("gossip_rag_clusters",
		mcp.WithDescription("Detect modular architectural clusters and submodules in the knowledge graph (inspired by Graphify cluster.py)"),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		clusters, err := s.graph.DetectClusters()
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("cluster error: %v", err)), nil
		}
		return toJSONText(clusters)
	})

	// gossip_rag_repo_map
	s.mcpServer.AddTool(mcp.NewTool("gossip_rag_repo_map",
		mcp.WithDescription("Generate a compact, token-budgeted architectural map ranked by PageRank centrality (inspired by Aider repo-map)"),
		mcp.WithInteger("max_tokens", mcp.Description("Token budget ceiling for the map (default: 1000)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		maxTokens := getInt(args, "max_tokens", 1000)

		repoMap, err := s.graph.GenerateRepoMap(maxTokens)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("repo-map error: %v", err)), nil
		}
		return mcp.NewToolResultText(repoMap), nil
	})

	// gossip_rag_index_codebase
	s.mcpServer.AddTool(mcp.NewTool("gossip_rag_index_codebase",
		mcp.WithDescription("Automatically index the codebase (files, packages, AST symbols, decisions, invariants) into the shared RAG knowledge graph"),
		mcp.WithString("root_path", mcp.Description("Root directory of the project to index (default: project root)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		rootPath := getString(args, "root_path")
		if rootPath == "" {
			rootPath = filepath.Dir(s.store.GetBaseDir())
		}

		summary, err := s.graph.IndexCodebase(rootPath)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("indexing error: %v", err)), nil
		}

		return toJSONText(summary)
	})

	s.mcpServer.AddTool(mcp.NewTool("gsp_index",
		mcp.WithDescription("Alias for gossip_rag_index_codebase: parse AST and index codebase into RAG space"),
		mcp.WithString("root_path", mcp.Description("Root directory of the project to index")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		rootPath := getString(args, "root_path")
		if rootPath == "" {
			rootPath = filepath.Dir(s.store.GetBaseDir())
		}

		summary, err := s.graph.IndexCodebase(rootPath)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("indexing error: %v", err)), nil
		}

		return toJSONText(summary)
	})
}

// --- Multi-Agent Debate, Review & Red Teaming Tools ---

func (s *Server) registerDeliberationTools() {
	// gossip_start_debate
	s.mcpServer.AddTool(mcp.NewTool("gossip_start_debate",
		mcp.WithDescription("Open a multi-agent debate session on an architectural choice, controversial design, or proposal"),
		mcp.WithString("title", mcp.Required(), mcp.Description("Debate title or motion")),
		mcp.WithString("description", mcp.Required(), mcp.Description("Detailed problem statement and proposed approach")),
		mcp.WithString("proposer", mcp.Required(), mcp.Description("Agent ID proposing the motion")),
		mcp.WithString("role", mcp.Required(), mcp.Description("Proposer's role")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		title := getString(args, "title")
		desc := getString(args, "description")
		proposer := getString(args, "proposer")
		role := getString(args, "role")

		if title == "" || desc == "" || proposer == "" || role == "" {
			return mcp.NewToolResultError("'title', 'description', 'proposer', and 'role' are required"), nil
		}

		deb, err := s.deliberations.StartDebate(title, desc, proposer, role)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to start debate: %v", err)), nil
		}

		return toJSONText(deb)
	})

	// gossip_post_argument
	s.mcpServer.AddTool(mcp.NewTool("gossip_post_argument",
		mcp.WithDescription("Post an argument in an active multi-agent debate (pro, con, critique, rebuttal, or neutral)"),
		mcp.WithString("debate_id", mcp.Required(), mcp.Description("ID of the debate")),
		mcp.WithString("agent_id", mcp.Required(), mcp.Description("Agent ID posting the argument")),
		mcp.WithString("role", mcp.Required(), mcp.Description("Agent's role")),
		mcp.WithString("stance", mcp.Required(), mcp.Description("Stance: 'pro', 'con', 'critique', 'rebuttal', 'neutral'")),
		mcp.WithString("content", mcp.Required(), mcp.Description("Detailed argument, evidence, or algorithmic justification")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		debateID := getString(args, "debate_id")
		agentID := getString(args, "agent_id")
		role := getString(args, "role")
		stance := getString(args, "stance")
		content := getString(args, "content")

		if debateID == "" || agentID == "" || role == "" || stance == "" || content == "" {
			return mcp.NewToolResultError("debate_id, agent_id, role, stance, and content are required"), nil
		}

		arg, err := s.deliberations.PostArgument(debateID, agentID, role, stance, content)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to post argument: %v", err)), nil
		}

		return toJSONText(arg)
	})

	// gossip_cast_vote
	s.mcpServer.AddTool(mcp.NewTool("gossip_cast_vote",
		mcp.WithDescription("Cast a formal vote on an active debate or proposal"),
		mcp.WithString("debate_id", mcp.Required(), mcp.Description("ID of the debate")),
		mcp.WithString("agent_id", mcp.Required(), mcp.Description("Agent ID voting")),
		mcp.WithString("role", mcp.Required(), mcp.Description("Voter's role")),
		mcp.WithString("choice", mcp.Required(), mcp.Description("Vote choice: 'accept', 'reject', or 'abstain'")),
		mcp.WithInteger("confidence", mcp.Description("Confidence level from 1 to 5 (default: 3)")),
		mcp.WithString("reasoning", mcp.Description("Rationale for this vote")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		debateID := getString(args, "debate_id")
		agentID := getString(args, "agent_id")
		role := getString(args, "role")
		choice := getString(args, "choice")
		confidence := getInt(args, "confidence", 3)
		reasoning := getString(args, "reasoning")

		if debateID == "" || agentID == "" || role == "" || choice == "" {
			return mcp.NewToolResultError("debate_id, agent_id, role, and choice are required"), nil
		}

		vote, err := s.deliberations.CastVote(debateID, agentID, role, choice, confidence, reasoning)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to cast vote: %v", err)), nil
		}

		return toJSONText(vote)
	})

	// gossip_conclude_debate
	s.mcpServer.AddTool(mcp.NewTool("gossip_conclude_debate",
		mcp.WithDescription("Conclude a debate, tally votes, establish consensus, and record the final resolution"),
		mcp.WithString("debate_id", mcp.Required(), mcp.Description("ID of the debate to conclude")),
		mcp.WithString("resolved_by", mcp.Required(), mcp.Description("Arbiter/Critic agent ID concluding the debate")),
		mcp.WithString("summary", mcp.Required(), mcp.Description("Executive summary of deliberation and consensus")),
		mcp.WithString("adopted_decision", mcp.Description("Adopted architectural decision or policy to register")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		debateID := getString(args, "debate_id")
		resolvedBy := getString(args, "resolved_by")
		summary := getString(args, "summary")
		adoptedDecision := getString(args, "adopted_decision")

		if debateID == "" || resolvedBy == "" || summary == "" {
			return mcp.NewToolResultError("debate_id, resolved_by, and summary are required"), nil
		}

		res, err := s.deliberations.ConcludeDebate(debateID, resolvedBy, summary, adoptedDecision)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to conclude debate: %v", err)), nil
		}

		// If an adopted decision was formulated, register it automatically in the knowledge graph!
		if adoptedDecision != "" {
			_, _ = s.graph.AddNode(graph.Node{
				ID:        fmt.Sprintf("decision:%s", debateID),
				Type:      graph.TypeDecision,
				Label:     fmt.Sprintf("Consensus Decision (%s)", debateID),
				Summary:   fmt.Sprintf("%s\n\nAdopted: %s", summary, adoptedDecision),
				Tags:      []string{"consensus", "debate", "decision"},
				CreatedBy: resolvedBy,
			})
		}

		return toJSONText(res)
	})

	// gossip_request_review
	s.mcpServer.AddTool(mcp.NewTool("gossip_request_review",
		mcp.WithDescription("Request a formal peer review or critique cycle on a task, artifact, or code file"),
		mcp.WithString("target_type", mcp.Required(), mcp.Description("Target: 'task', 'file', 'artifact', 'decision'")),
		mcp.WithString("target_id", mcp.Required(), mcp.Description("Identifier of target (e.g. 'task-1', 'pkg/tasks/tasks.go')")),
		mcp.WithString("author_id", mcp.Required(), mcp.Description("Author agent ID")),
		mcp.WithString("reviewer_id", mcp.Required(), mcp.Description("Reviewer agent ID (or role:qa_engineer / role:red_team)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		targetType := getString(args, "target_type")
		targetID := getString(args, "target_id")
		authorID := getString(args, "author_id")
		reviewerID := getString(args, "reviewer_id")

		if targetType == "" || targetID == "" || authorID == "" || reviewerID == "" {
			return mcp.NewToolResultError("target_type, target_id, author_id, and reviewer_id are required"), nil
		}

		rev, err := s.deliberations.RequestReview(targetType, targetID, authorID, reviewerID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to request review: %v", err)), nil
		}

		return toJSONText(rev)
	})

	// gossip_submit_review
	s.mcpServer.AddTool(mcp.NewTool("gossip_submit_review",
		mcp.WithDescription("Submit structured review findings, critique, and formal verdict on a pending review"),
		mcp.WithString("review_id", mcp.Required(), mcp.Description("ID of the review")),
		mcp.WithString("reviewer_id", mcp.Required(), mcp.Description("Reviewer agent ID")),
		mcp.WithString("status", mcp.Required(), mcp.Description("Verdict status: 'approved', 'changes_requested', 'rejected'")),
		mcp.WithString("verdict", mcp.Required(), mcp.Description("Review summary evaluation")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		reviewID := getString(args, "review_id")
		reviewerID := getString(args, "reviewer_id")
		status := getString(args, "status")
		verdict := getString(args, "verdict")

		if reviewID == "" || reviewerID == "" || status == "" || verdict == "" {
			return mcp.NewToolResultError("review_id, reviewer_id, status, and verdict are required"), nil
		}

		rev, err := s.deliberations.SubmitReview(reviewID, reviewerID, status, verdict, nil)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to submit review: %v", err)), nil
		}

		return toJSONText(rev)
	})

	// gossip_log_red_team_probe
	s.mcpServer.AddTool(mcp.NewTool("gossip_log_red_team_probe",
		mcp.WithDescription("Log an adversarial red-team penetration, fuzzing vector, concurrency race, or boundary exploit"),
		mcp.WithString("target", mcp.Required(), mcp.Description("Component, protocol, or file under test")),
		mcp.WithString("threat_category", mcp.Required(), mcp.Description("Threat category: 'concurrency_race', 'dos', 'boundary_overflow', 'tampering', 'spoofing', 'logic_bypass'")),
		mcp.WithString("attacker_id", mcp.Required(), mcp.Description("Red team agent ID")),
		mcp.WithString("attack_vector", mcp.Required(), mcp.Description("Description of exploit or edge-case break scenario")),
		mcp.WithString("poc_payload", mcp.Description("Minimal reproduction payload or reproduction command")),
		mcp.WithString("status", mcp.Description("Status: 'vulnerable', 'mitigated', 'false_positive' (default: 'vulnerable')")),
		mcp.WithString("remediation_task_id", mcp.Description("Linked task ID to remediate this vulnerability")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		target := getString(args, "target")
		threatCat := getString(args, "threat_category")
		attackerID := getString(args, "attacker_id")
		attackVec := getString(args, "attack_vector")
		poc := getString(args, "poc_payload")
		status := getString(args, "status")
		remediationTask := getString(args, "remediation_task_id")

		if target == "" || threatCat == "" || attackerID == "" || attackVec == "" {
			return mcp.NewToolResultError("target, threat_category, attacker_id, and attack_vector are required"), nil
		}

		probe, err := s.deliberations.LogRedTeamProbe(target, threatCat, attackerID, attackVec, poc, status, remediationTask)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to log red team probe: %v", err)), nil
		}

		return toJSONText(probe)
	})

	// gossip_list_deliberations
	s.mcpServer.AddTool(mcp.NewTool("gossip_list_deliberations",
		mcp.WithDescription("List debates, peer reviews, and red team probes with optional filtering"),
		mcp.WithString("type", mcp.Description("Filter by type: 'debates', 'reviews', 'probes', or 'all' (default: 'all')")),
		mcp.WithString("status", mcp.Description("Optional status filter")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		delibType := getString(args, "type")
		status := getString(args, "status")

		resp := make(map[string]any)

		if delibType == "" || delibType == "all" || delibType == "debates" {
			debates, err := s.deliberations.ListDebates(status)
			if err == nil {
				resp["debates"] = debates
			}
		}

		if delibType == "" || delibType == "all" || delibType == "reviews" {
			reviews, err := s.deliberations.ListReviews(status)
			if err == nil {
				resp["reviews"] = reviews
			}
		}

		if delibType == "" || delibType == "all" || delibType == "probes" {
			probes, err := s.deliberations.ListProbes(status)
			if err == nil {
				resp["probes"] = probes
			}
		}

		return toJSONText(resp)
	})

	// gossip_advance_debate_round
	s.mcpServer.AddTool(mcp.NewTool("gossip_advance_debate_round",
		mcp.WithDescription("Close the current round of an active debate and open the next round for critique and rebuttal"),
		mcp.WithString("debate_id", mcp.Required(), mcp.Description("ID of the debate to advance")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		debateID := getString(args, "debate_id")
		if debateID == "" {
			return mcp.NewToolResultError("debate_id is required"), nil
		}

		if err := s.deliberations.AdvanceRound(debateID); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to advance round: %v", err)), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Successfully advanced debate %s to next round", debateID)), nil
	})

	// gossip_debate_ranked_vote
	s.mcpServer.AddTool(mcp.NewTool("gossip_debate_ranked_vote",
		mcp.WithDescription("Cast a ranked-choice ballot ordering multiple alternative candidates (Borda Count tallying)"),
		mcp.WithString("debate_id", mcp.Required(), mcp.Description("ID of the debate")),
		mcp.WithString("agent_id", mcp.Required(), mcp.Description("Voter Agent ID")),
		mcp.WithString("role", mcp.Required(), mcp.Description("Voter Role")),
		mcp.WithArray("ranked_choices", mcp.Required(), mcp.Description("Ordered list of candidate options from most preferred (1st) to least preferred")),
		mcp.WithInteger("confidence", mcp.Description("Confidence level from 1 to 5 (default: 3)")),
		mcp.WithString("reasoning", mcp.Description("Rationale for this candidate ranking")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		debateID := getString(args, "debate_id")
		agentID := getString(args, "agent_id")
		role := getString(args, "role")
		choices := getStringSlice(args, "ranked_choices")
		confidence := getInt(args, "confidence", 3)
		reasoning := getString(args, "reasoning")

		if debateID == "" || agentID == "" || role == "" || len(choices) == 0 {
			return mcp.NewToolResultError("debate_id, agent_id, role, and ranked_choices are required"), nil
		}

		err := s.deliberations.CastRankedVote(debateID, agentID, role, choices, confidence, reasoning)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to cast ranked vote: %v", err)), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Ranked vote recorded for %s on debate %s", agentID, debateID)), nil
	})

	// gossip_debate_tally_ranked_votes
	s.mcpServer.AddTool(mcp.NewTool("gossip_debate_tally_ranked_votes",
		mcp.WithDescription("Tally ranked-choice votes using confidence-weighted Borda Count to identify the consensus winner"),
		mcp.WithString("debate_id", mcp.Required(), mcp.Description("ID of the debate")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		debateID := getString(args, "debate_id")
		if debateID == "" {
			return mcp.NewToolResultError("debate_id is required"), nil
		}

		result, err := s.deliberations.TallyRankedVotes(debateID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("tally error: %v", err)), nil
		}
		return toJSONText(result)
	})

	// gossip_debate_check_convergence
	s.mcpServer.AddTool(mcp.NewTool("gossip_debate_check_convergence",
		mcp.WithDescription("Evaluate whether a multi-round debate has converged towards consensus or if further rounds are needed"),
		mcp.WithString("debate_id", mcp.Required(), mcp.Description("ID of the debate")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		debateID := getString(args, "debate_id")
		if debateID == "" {
			return mcp.NewToolResultError("debate_id is required"), nil
		}

		report, err := s.deliberations.CheckConvergence(debateID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("convergence check error: %v", err)), nil
		}
		return toJSONText(report)
	})

	// gossip_redteam_generate_stride_probes
	s.mcpServer.AddTool(mcp.NewTool("gossip_redteam_generate_stride_probes",
		mcp.WithDescription("Generate structured adversarial STRIDE threat probes (Spoofing, Tampering, Repudiation, Info Disclosure, DoS, Elevation of Privilege)"),
		mcp.WithString("target", mcp.Required(), mcp.Description("Target component, interface, or file to probe")),
		mcp.WithString("context", mcp.Description("Context description or architectural invariants")),
		mcp.WithString("attacker_id", mcp.Description("Attacker agent ID (default: 'agent:red_team')")),
		mcp.WithBoolean("auto_log", mcp.Description("Automatically record generated probes in deliberations.json (default: true)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		target := getString(args, "target")
		contextDesc := getString(args, "context")
		attackerID := getString(args, "attacker_id")
		autoLog := getBool(args, "auto_log")
		// default to true if key not provided
		if _, ok := args["auto_log"]; !ok {
			autoLog = true
		}

		if target == "" {
			return mcp.NewToolResultError("target is required"), nil
		}

		probes := deliberation.GenerateSTRIDEProbes(target, contextDesc, attackerID)
		if autoLog {
			_ = s.deliberations.LogBatchProbes(probes)
		}
		return toJSONText(probes)
	})

	// gossip_redteam_generate_audit_report
	s.mcpServer.AddTool(mcp.NewTool("gossip_redteam_generate_audit_report",
		mcp.WithDescription("Generate and save the executive AUDIT_REPORT.md summarizing all red-team security evaluations"),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		report, err := s.deliberations.GenerateAuditReport()
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to generate audit report: %v", err)), nil
		}
		return mcp.NewToolResultText(report), nil
	})
}

// --- Standard Operating Procedures (SOP) Tools ---

func (s *Server) registerSOPTools() {
	// gossip_sop_list_workflows
	s.mcpServer.AddTool(mcp.NewTool("gossip_sop_list_workflows",
		mcp.WithDescription("List active Standard Operating Procedure (SOP) workflows and available templates (MetaGPT-inspired)"),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		workflows, err := s.sop.ListWorkflows()
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to list workflows: %v", err)), nil
		}

		templates := sop.GetWorkflowTemplates()
		templateNames := make([]string, 0, len(templates))
		for name := range templates {
			templateNames = append(templateNames, name)
		}

		resp := map[string]any{
			"active_workflows":    workflows,
			"available_templates": templateNames,
		}
		return toJSONText(resp)
	})

	// gossip_sop_start
	s.mcpServer.AddTool(mcp.NewTool("gossip_sop_start",
		mcp.WithDescription("Start a structured SOP workflow (templates: 'feature_delivery', 'bug_investigation', 'security_hardening')"),
		mcp.WithString("template", mcp.Required(), mcp.Description("Template name: 'feature_delivery', 'bug_investigation', or 'security_hardening'")),
		mcp.WithString("title", mcp.Required(), mcp.Description("Workflow title")),
		mcp.WithString("description", mcp.Required(), mcp.Description("Project scope or objective")),
		mcp.WithString("initiator", mcp.Required(), mcp.Description("Initiator agent ID")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		templateName := getString(args, "template")
		title := getString(args, "title")
		desc := getString(args, "description")
		initiator := getString(args, "initiator")

		if templateName == "" || title == "" || desc == "" || initiator == "" {
			return mcp.NewToolResultError("template, title, description, and initiator are required"), nil
		}

		wf, err := s.sop.StartWorkflow(templateName, title, desc, initiator)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to start SOP workflow: %v", err)), nil
		}
		return toJSONText(wf)
	})

	// gossip_sop_save_artifact
	s.mcpServer.AddTool(mcp.NewTool("gossip_sop_save_artifact",
		mcp.WithDescription("Save a versioned SOP artifact deliverable (PRD, System Design, Task Plan, Review, Security Audit)"),
		mcp.WithString("workflow_id", mcp.Required(), mcp.Description("Active SOP workflow ID")),
		mcp.WithString("stage_id", mcp.Required(), mcp.Description("Stage ID (e.g. 'requirements', 'architecture', 'planning', 'implementation', 'qa_review', 'security_audit')")),
		mcp.WithString("artifact_type", mcp.Required(), mcp.Description("Type: 'prd', 'system_design', 'task_plan', 'code_manifest', 'peer_review', 'security_audit', 'release_notes'")),
		mcp.WithString("title", mcp.Required(), mcp.Description("Artifact title")),
		mcp.WithString("content", mcp.Required(), mcp.Description("Full Markdown artifact document content")),
		mcp.WithString("created_by", mcp.Required(), mcp.Description("Author Agent ID")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		wfID := getString(args, "workflow_id")
		stageID := getString(args, "stage_id")
		artType := getString(args, "artifact_type")
		title := getString(args, "title")
		content := getString(args, "content")
		createdBy := getString(args, "created_by")

		if wfID == "" || stageID == "" || artType == "" || title == "" || content == "" || createdBy == "" {
			return mcp.NewToolResultError("workflow_id, stage_id, artifact_type, title, content, and created_by are required"), nil
		}

		art, err := s.sop.SaveArtifact(wfID, stageID, artType, title, content, createdBy, nil)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to save artifact: %v", err)), nil
		}
		return toJSONText(art)
	})

	// gossip_sop_advance
	s.mcpServer.AddTool(mcp.NewTool("gossip_sop_advance",
		mcp.WithDescription("Advance an SOP workflow to the next phase gate, verifying that required deliverables and roles match"),
		mcp.WithString("workflow_id", mcp.Required(), mcp.Description("Active SOP workflow ID")),
		mcp.WithString("agent_id", mcp.Required(), mcp.Description("Caller Agent ID")),
		mcp.WithString("role", mcp.Required(), mcp.Description("Caller Role (must match stage required role or orchestrator)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		wfID := getString(args, "workflow_id")
		agentID := getString(args, "agent_id")
		role := getString(args, "role")

		if wfID == "" || agentID == "" || role == "" {
			return mcp.NewToolResultError("workflow_id, agent_id, and role are required"), nil
		}

		wf, err := s.sop.AdvanceStage(wfID, agentID, role)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to advance stage: %v", err)), nil
		}
		return toJSONText(wf)
	})

	// gossip_sop_get_status
	s.mcpServer.AddTool(mcp.NewTool("gossip_sop_get_status",
		mcp.WithDescription("Inspect the current stage, completed gates, and deliverables for an SOP workflow"),
		mcp.WithString("workflow_id", mcp.Required(), mcp.Description("Active SOP workflow ID")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		wfID := getString(args, "workflow_id")
		if wfID == "" {
			return mcp.NewToolResultError("workflow_id is required"), nil
		}

		wf, err := s.sop.GetWorkflow(wfID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("workflow error: %v", err)), nil
		}
		return toJSONText(wf)
	})
}

// --- Guardrail, Role Derivation & Project Safety Tools ---

func (s *Server) registerGuardrailTools() {
	// gossip_derive_project_roles (and alias gsp_derive_roles)
	deriveHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		name := getString(args, "project_name")
		stack := getStringSlice(args, "tech_stack")
		goal := getString(args, "project_goal")

		if name == "" {
			name = "Current Project"
		}

		pg, err := s.guardrails.DeriveProjectRoles(name, stack, goal)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to derive roles: %v", err)), nil
		}

		// Also register derived roles into role provider dynamically
		for _, r := range pg.Roles {
			_ = s.roleProvider.RegisterRole(roles.RoleDefinition{
				ID:           r.RoleID,
				Title:        r.Title,
				Category:     "derived",
				Description:  r.Description,
				SystemPrompt: r.SystemPrompt,
				Directives:   append(r.CautionDirectives, r.ForbiddenActions...),
			})
		}

		return toJSONText(pg)
	}

	s.mcpServer.AddTool(mcp.NewTool("gossip_derive_project_roles",
		mcp.WithDescription("Synthesize LLM-derived specialist roles, guardrails, cautions, and AGENTS.md customized for this project"),
		mcp.WithString("project_name", mcp.Description("Name of the project")),
		mcp.WithArray("tech_stack", mcp.Description("List of core languages and frameworks (e.g. ['Go', 'React', 'gRPC'])")),
		mcp.WithString("project_goal", mcp.Description("High-level architectural goal or purpose")),
	), deriveHandler)

	s.mcpServer.AddTool(mcp.NewTool("gsp_derive_roles",
		mcp.WithDescription("Alias for gossip_derive_project_roles: derive roles, guardrails, cautions, and AGENTS.md"),
		mcp.WithString("project_name", mcp.Description("Name of the project")),
		mcp.WithArray("tech_stack", mcp.Description("List of core languages and frameworks")),
		mcp.WithString("project_goal", mcp.Description("High-level architectural goal or purpose")),
	), deriveHandler)

	// gossip_guardrails_validate_action (and alias gsp_guardrail_check)
	validateHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		role := getString(args, "role")
		action := getString(args, "action")
		targetPath := getString(args, "target_path")
		payload := getString(args, "payload")

		if role == "" || action == "" {
			return mcp.NewToolResultError("role and action are required"), nil
		}

		res, err := s.guardrails.ValidateProposedAction(role, action, targetPath, payload)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("validation error: %v", err)), nil
		}
		return toJSONText(res)
	}

	s.mcpServer.AddTool(mcp.NewTool("gossip_guardrails_validate_action",
		mcp.WithDescription("Pre-flight inspection checking whether an action or file modification violates project cautions or forbidden rules"),
		mcp.WithString("role", mcp.Required(), mcp.Description("Executing agent role")),
		mcp.WithString("action", mcp.Required(), mcp.Description("Action name (e.g. 'write_file', 'bypass_race_detector', 'delete_table')")),
		mcp.WithString("target_path", mcp.Description("Target file or resource path")),
		mcp.WithString("payload", mcp.Description("Optional payload or diff snippet to scan")),
	), validateHandler)

	s.mcpServer.AddTool(mcp.NewTool("gsp_guardrail_check",
		mcp.WithDescription("Alias for gossip_guardrails_validate_action: pre-flight check against active cautions"),
		mcp.WithString("role", mcp.Required(), mcp.Description("Executing agent role")),
		mcp.WithString("action", mcp.Required(), mcp.Description("Action name")),
		mcp.WithString("target_path", mcp.Description("Target file path")),
		mcp.WithString("payload", mcp.Description("Payload snippet to scan")),
	), validateHandler)

	// gossip_set_caution_rule
	s.mcpServer.AddTool(mcp.NewTool("gossip_set_caution_rule",
		mcp.WithDescription("Add or update a safety caution rule, sensitive path pattern, or invariant in the project guardrail database"),
		mcp.WithString("id", mcp.Description("Unique rule ID (auto-generated if omitted)")),
		mcp.WithString("category", mcp.Required(), mcp.Description("Category: 'security_leak', 'data_loss', 'regression', 'concurrency'")),
		mcp.WithString("level", mcp.Required(), mcp.Description("Severity level: 'critical', 'warning', 'info'")),
		mcp.WithString("trigger_pattern", mcp.Required(), mcp.Description("Filename pattern or keyword regex triggering this caution")),
		mcp.WithString("directive", mcp.Required(), mcp.Description("Human/agent instruction to follow when triggered")),
		mcp.WithString("enforcement", mcp.Required(), mcp.Description("Enforcement mode: 'block', 'require_peer_review', 'warn'")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		rule := guardrails.CautionRule{
			ID:             getString(args, "id"),
			Category:       getString(args, "category"),
			Level:          getString(args, "level"),
			TriggerPattern: getString(args, "trigger_pattern"),
			Directive:      getString(args, "directive"),
			Enforcement:    getString(args, "enforcement"),
		}

		if rule.Category == "" || rule.Level == "" || rule.TriggerPattern == "" || rule.Directive == "" {
			return mcp.NewToolResultError("category, level, trigger_pattern, and directive are required"), nil
		}

		if err := s.guardrails.SetCautionRule(rule); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to set caution rule: %v", err)), nil
		}

		return mcp.NewToolResultText(fmt.Sprintf("Successfully registered caution rule %q in guardrails database", rule.ID)), nil
	})

	// gossip_get_guardrails
	s.mcpServer.AddTool(mcp.NewTool("gossip_get_guardrails",
		mcp.WithDescription("Retrieve all project guardrails, active roles, forbidden actions, cautions, and invariants"),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		pg, err := s.guardrails.Load()
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to load guardrails: %v", err)), nil
		}
		return toJSONText(pg)
	})

	// gossip_export_agents_md
	s.mcpServer.AddTool(mcp.NewTool("gossip_export_agents_md",
		mcp.WithDescription("Re-render and write AGENTS.md and RULES.md in the project directory"),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		pg, err := s.guardrails.Load()
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to load guardrails: %v", err)), nil
		}
		if err := s.guardrails.Save(pg); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to save: %v", err)), nil
		}
		agentsMD := s.guardrails.RenderAgentsMD(pg)
		return mcp.NewToolResultText(agentsMD), nil
	})
}

// --- MCP Prompts (Slash Commands like gsp:__) ---

func (s *Server) registerPrompts() {
	// gsp:derive_roles
	s.mcpServer.AddPrompt(mcp.NewPrompt("gsp:derive_roles",
		mcp.WithPromptDescription("Analyze codebase tech stack and derive custom agent roles, guardrails, cautions, and AGENTS.md"),
		mcp.WithArgument("project_name", mcp.ArgumentDescription("Project name")),
		mcp.WithArgument("tech_stack", mcp.ArgumentDescription("Comma-separated technologies (e.g. 'Go, WebSocket, React')")),
	), func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		projectName := req.Params.Arguments["project_name"]
		techStack := req.Params.Arguments["tech_stack"]
		promptText := fmt.Sprintf("Act as the Lead Orchestrator for %s.\n1. Inspect project requirements and tech stack: %s.\n2. Call `gossip_derive_project_roles` with the detected technologies.\n3. Verify generated roles and safety cautions in AGENTS.md.\n4. Register specialist agents in the mesh.", projectName, techStack)
		return mcp.NewGetPromptResult("Derive project roles and guardrails", []mcp.PromptMessage{
			mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(promptText)),
		}), nil
	})

	// gsp:blast_radius
	s.mcpServer.AddPrompt(mcp.NewPrompt("gsp:blast_radius",
		mcp.WithPromptDescription("Perform blast-radius impact analysis on a component using Graphify reverse dependency walk"),
		mcp.WithArgument("component_id", mcp.ArgumentDescription("Node, symbol, or file ID to analyze")),
	), func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		compID := req.Params.Arguments["component_id"]
		promptText := fmt.Sprintf("Before refactoring or modifying component `%s`:\n1. Call `gossip_rag_impact_analysis` with seed_node_id='%s' and depth=2.\n2. Identify direct and transitive dependents.\n3. Check blast radius score and critical bottlenecks.\n4. Run recommended regression suites.", compID, compID)
		return mcp.NewGetPromptResult("Blast radius analysis", []mcp.PromptMessage{
			mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(promptText)),
		}), nil
	})

	// gsp:sop_advance
	s.mcpServer.AddPrompt(mcp.NewPrompt("gsp:sop_advance",
		mcp.WithPromptDescription("Advance an active SOP workflow through its next phase gate with artifact verification"),
		mcp.WithArgument("workflow_id", mcp.ArgumentDescription("Active SOP workflow ID")),
	), func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		wfID := req.Params.Arguments["workflow_id"]
		promptText := fmt.Sprintf("For SOP Workflow `%s`:\n1. Call `gossip_sop_get_status` to inspect current stage deliverables.\n2. Ensure required artifact (PRD, System Design, Code Manifest, Peer Review, or Security Audit) is saved via `gossip_sop_save_artifact`.\n3. Call `gossip_sop_advance` to pass the phase gate.", wfID)
		return mcp.NewGetPromptResult("Advance SOP phase gate", []mcp.PromptMessage{
			mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(promptText)),
		}), nil
	})

	// gsp:debate
	s.mcpServer.AddPrompt(mcp.NewPrompt("gsp:debate",
		mcp.WithPromptDescription("Initiate an adversarial multi-agent debate with convergence tracking and Borda voting"),
		mcp.WithArgument("title", mcp.ArgumentDescription("Motion or trade-off to debate")),
	), func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		title := req.Params.Arguments["title"]
		promptText := fmt.Sprintf("Open a Multi-Agent Debate for `%s`:\n1. Call `gossip_start_debate`.\n2. Have specialists (network_engineer, math_expert, systems_architect, security_specialist) post arguments via `gossip_post_argument`.\n3. Check convergence via `gossip_debate_check_convergence`.\n4. Cast ranked votes via `gossip_debate_ranked_vote` and tally Borda consensus.", title)
		return mcp.NewGetPromptResult("Multi-agent debate", []mcp.PromptMessage{
			mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(promptText)),
		}), nil
	})

	// gsp:stride_audit
	s.mcpServer.AddPrompt(mcp.NewPrompt("gsp:stride_audit",
		mcp.WithPromptDescription("Execute automated STRIDE threat modeling and compile AUDIT_REPORT.md"),
		mcp.WithArgument("target", mcp.ArgumentDescription("Target component or interface to audit")),
	), func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		target := req.Params.Arguments["target"]
		promptText := fmt.Sprintf("Run security red-team audit for `%s`:\n1. Call `gossip_redteam_generate_stride_probes` for `%s`.\n2. Inspect each probe for Spoofing, Tampering, Repudiation, Info Disclosure, DoS, and Elevation of Privilege.\n3. Implement mitigations and call `gossip_redteam_generate_audit_report`.", target, target)
		return mcp.NewGetPromptResult("STRIDE security audit", []mcp.PromptMessage{
			mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(promptText)),
		}), nil
	})

	// gsp:review
	s.mcpServer.AddPrompt(mcp.NewPrompt("gsp:review",
		mcp.WithPromptDescription("Request and submit a formal peer review on a task deliverable"),
		mcp.WithArgument("task_id", mcp.ArgumentDescription("Task or artifact ID")),
	), func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		taskID := req.Params.Arguments["task_id"]
		promptText := fmt.Sprintf("Conduct Peer Review for `%s`:\n1. Call `gossip_request_review` targeting `%s`.\n2. Inspect implementation code, test coverage, and invariants.\n3. Submit verdict via `gossip_submit_review` with structured findings.", taskID, taskID)
		return mcp.NewGetPromptResult("Peer review", []mcp.PromptMessage{
			mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(promptText)),
		}), nil
	})

	// gsp:guardrail_check
	s.mcpServer.AddPrompt(mcp.NewPrompt("gsp:guardrail_check",
		mcp.WithPromptDescription("Validate an action or code edit against active caution rules and project invariants"),
		mcp.WithArgument("action", mcp.ArgumentDescription("Action to check (e.g. write_file, edit_config)")),
		mcp.WithArgument("path", mcp.ArgumentDescription("Target file path")),
	), func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		action := req.Params.Arguments["action"]
		path := req.Params.Arguments["path"]
		promptText := fmt.Sprintf("Pre-flight safety inspection:\n1. Call `gossip_guardrails_validate_action` with action='%s' and target_path='%s'.\n2. If blocked or caution flagged, follow the directive or seek peer review before executing.", action, path)
		return mcp.NewGetPromptResult("Guardrail inspection", []mcp.PromptMessage{
			mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(promptText)),
		}), nil
	})

	// gsp:repomap
	s.mcpServer.AddPrompt(mcp.NewPrompt("gsp:repomap",
		mcp.WithPromptDescription("Fetch PageRank-ranked architectural repo map for context injection"),
		mcp.WithArgument("max_tokens", mcp.ArgumentDescription("Token budget (default: 1000)")),
	), func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		tokens := req.Params.Arguments["max_tokens"]
		if tokens == "" {
			tokens = "1000"
		}
		promptText := fmt.Sprintf("Fetch compact repo map:\nCall `gossip_rag_repo_map` with max_tokens=%s and review architectural hubs before writing code.", tokens)
		return mcp.NewGetPromptResult("Architectural repo-map", []mcp.PromptMessage{
			mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(promptText)),
		}), nil
	})

	// gsp:poll
	s.mcpServer.AddPrompt(mcp.NewPrompt("gsp:poll",
		mcp.WithPromptDescription("Continuously poll or long-poll for incoming messages, unblocked tasks, and state events"),
		mcp.WithArgument("agent_id", mcp.ArgumentDescription("Calling agent identifier (e.g. 'worker-1')")),
		mcp.WithArgument("role", mcp.ArgumentDescription("Specialist role (e.g. 'worker', 'network_engineer')")),
		mcp.WithArgument("timeout", mcp.ArgumentDescription("Long-polling timeout in seconds (default: 20)")),
	), func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		agentID := req.Params.Arguments["agent_id"]
		role := req.Params.Arguments["role"]
		timeout := req.Params.Arguments["timeout"]
		if timeout == "" {
			timeout = "20"
		}
		promptText := fmt.Sprintf("Event Sync & Continuous Polling Loop:\nCall `gossip_poll_events` with agent_id='%s', role='%s', and timeout_seconds=%s.\nIf has_updates=true, process incoming messages or claim ready tasks; then poll again with returned server_time.", agentID, role, timeout)
		return mcp.NewGetPromptResult("Event polling loop", []mcp.PromptMessage{
			mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(promptText)),
		}), nil
	})

	// gsp:assignments
	s.mcpServer.AddPrompt(mcp.NewPrompt("gsp:assignments",
		mcp.WithPromptDescription("Inspect Orchestrator workload distribution, task assignments per agent, blockers, and delegation recommendations"),
	), func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		promptText := "Orchestrator Task Allocation & Delegation Review:\n1. Call `gossip_orchestrator_assignments` with format='markdown'.\n2. Review active task loads, bottleneck tasks, and delegation suggestions.\n3. Delegate ready unassigned tasks to idle agents via `gossip_update_task`."
		return mcp.NewGetPromptResult("Orchestrator assignments", []mcp.PromptMessage{
			mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(promptText)),
		}), nil
	})

	// gsp:my_todos
	s.mcpServer.AddPrompt(mcp.NewPrompt("gsp:my_todos",
		mcp.WithPromptDescription("Inspect personal scratchpad checklist items and sub-tasks for an agent"),
		mcp.WithArgument("agent_id", mcp.ArgumentDescription("Agent ID")),
	), func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		agentID := req.Params.Arguments["agent_id"]
		promptText := fmt.Sprintf("Personal To-Do Checklist for `%s`:\nCall `gossip_personal_todo_list` with agent_id='%s'.\nDeconstruct your assigned global task into personal checklist items via `gossip_personal_todo_add`.", agentID, agentID)
		return mcp.NewGetPromptResult("Personal todo list", []mcp.PromptMessage{
			mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(promptText)),
		}), nil
	})

	// gsp:system_todos
	s.mcpServer.AddPrompt(mcp.NewPrompt("gsp:system_todos",
		mcp.WithPromptDescription("Inspect unified project-wide action backlog across DAG, milestones, SOP gates, and vulnerabilities"),
	), func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		promptText := "System-Wide Backlog Review:\nCall `gossip_system_todo_list` with format='markdown' to inspect ready unassigned tasks, incomplete milestones, pending SOP phase gates, and open security vulnerabilities."
		return mcp.NewGetPromptResult("System todo list", []mcp.PromptMessage{
			mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(promptText)),
		}), nil
	})
}

// --- Event Synchronization & Long-Polling Tools ---

func (s *Server) registerEventTools() {
	// gossip_poll_events (alias gsp_poll, gossip_sync)
	pollHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		agentID := getString(args, "agent_id")
		role := getString(args, "role")
		sinceTimeStr := getString(args, "since_time")
		timeoutSec := getInt(args, "timeout_seconds", 0)
		eventTypes := getStringSlice(args, "event_types")
		autoHeartbeat := getBool(args, "auto_heartbeat")
		currentTaskID := getString(args, "current_task_id")
		status := getString(args, "status")

		if agentID == "" || role == "" {
			return mcp.NewToolResultError("'agent_id' and 'role' are required"), nil
		}

		var sinceTime time.Time
		if sinceTimeStr != "" {
			var err error
			sinceTime, err = parseTimeFlexible(sinceTimeStr)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid since_time: %v", err)), nil
			}
		}

		// Default auto_heartbeat to true if omitted or not explicitly false
		if _, ok := args["auto_heartbeat"]; !ok {
			autoHeartbeat = true
		}

		pollReq := events.PollRequest{
			AgentID:       agentID,
			Role:          role,
			SinceTime:     sinceTime,
			Timeout:       time.Duration(timeoutSec) * time.Second,
			EventTypes:    eventTypes,
			AutoHeartbeat: autoHeartbeat,
			CurrentTaskID: currentTaskID,
			Status:        status,
		}

		resp, err := s.events.Poll(ctx, pollReq)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("poll error: %v", err)), nil
		}

		return toJSONText(resp)
	}

	s.mcpServer.AddTool(mcp.NewTool("gossip_poll_events",
		mcp.WithDescription("Continuously poll or long-poll for updates, new messages, newly unblocked ready tasks, debate rounds, and peer activity with timeout"),
		mcp.WithString("agent_id", mcp.Required(), mcp.Description("Unique identifier of the calling agent")),
		mcp.WithString("role", mcp.Required(), mcp.Description("Specialist role of the calling agent")),
		mcp.WithString("since_time", mcp.Description("RFC3339 timestamp or relative duration (e.g. '2026-11-04T12:00:00Z', '30s', '5m')")),
		mcp.WithInteger("timeout_seconds", mcp.Description("Long-polling timeout in seconds (0 to 60; 0 returns immediately, >0 waits for events)")),
		mcp.WithArray("event_types", mcp.Description("Filter event categories: ['message_received', 'task_ready', 'task_assigned', 'task_updated', 'debate_activity', 'sop_advanced', 'agent_presence']")),
		mcp.WithBoolean("auto_heartbeat", mcp.Description("Automatically update agent's online heartbeat during poll (default: true)")),
		mcp.WithString("current_task_id", mcp.Description("Active task ID currently being worked on")),
		mcp.WithString("status", mcp.Description("Agent working status: 'active', 'idle', 'blocked'")),
	), pollHandler)

	s.mcpServer.AddTool(mcp.NewTool("gsp_poll",
		mcp.WithDescription("Alias for gossip_poll_events: time-based continuous event polling and long-polling sync"),
		mcp.WithString("agent_id", mcp.Required(), mcp.Description("Unique identifier of the calling agent")),
		mcp.WithString("role", mcp.Required(), mcp.Description("Specialist role of the calling agent")),
		mcp.WithString("since_time", mcp.Description("RFC3339 timestamp or relative duration")),
		mcp.WithInteger("timeout_seconds", mcp.Description("Long-polling timeout in seconds (0 to 60)")),
		mcp.WithArray("event_types", mcp.Description("Filter event categories")),
		mcp.WithBoolean("auto_heartbeat", mcp.Description("Automatically update agent's online heartbeat")),
		mcp.WithString("current_task_id", mcp.Description("Active task ID")),
		mcp.WithString("status", mcp.Description("Agent working status")),
	), pollHandler)

	s.mcpServer.AddTool(mcp.NewTool("gossip_sync",
		mcp.WithDescription("Alias for gossip_poll_events: synchronize state, heartbeat, and fetch new events"),
		mcp.WithString("agent_id", mcp.Required(), mcp.Description("Unique identifier of the calling agent")),
		mcp.WithString("role", mcp.Required(), mcp.Description("Specialist role of the calling agent")),
		mcp.WithString("since_time", mcp.Description("RFC3339 timestamp or relative duration")),
		mcp.WithInteger("timeout_seconds", mcp.Description("Long-polling timeout in seconds (0 to 60)")),
	), pollHandler)

	// gossip_log_event
	s.mcpServer.AddTool(mcp.NewTool("gossip_log_event",
		mcp.WithDescription("Append an explicit custom event or checkpoint to the shared event ledger (events.jsonl)"),
		mcp.WithString("type", mcp.Required(), mcp.Description("Event category or action (e.g. 'build_finished', 'migration_complete')")),
		mcp.WithString("summary", mcp.Required(), mcp.Description("Human-readable event summary")),
		mcp.WithString("actor_id", mcp.Description("Agent or system ID initiating the event")),
		mcp.WithString("actor_role", mcp.Description("Role of the actor")),
		mcp.WithString("target_id", mcp.Description("Target entity, task ID, or recipient ID")),
		mcp.WithString("target_role", mcp.Description("Target role")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		evtType := getString(args, "type")
		summary := getString(args, "summary")
		actorID := getString(args, "actor_id")
		actorRole := getString(args, "actor_role")
		targetID := getString(args, "target_id")
		targetRole := getString(args, "target_role")

		if evtType == "" || summary == "" {
			return mcp.NewToolResultError("'type' and 'summary' are required"), nil
		}

		if err := security.ValidatePayloadSize(summary, security.MaxMessageSizeBytes); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("event summary too large: %v", err)), nil
		}

		evt := events.Event{
			Type:       evtType,
			Timestamp:  time.Now(),
			ActorID:    actorID,
			ActorRole:  actorRole,
			TargetID:   targetID,
			TargetRole: targetRole,
			Summary:    summary,
		}

		if err := s.events.LogEvent(evt); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to log event: %v", err)), nil
		}

		return toJSONText(map[string]any{
			"status": "logged",
			"event":  evt,
		})
	})
}
