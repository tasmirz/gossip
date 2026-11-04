package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/tasmirz/gossip/pkg/graph"
	"github.com/tasmirz/gossip/pkg/messages"
	"github.com/tasmirz/gossip/pkg/roles"
	"github.com/tasmirz/gossip/pkg/state"
	"github.com/tasmirz/gossip/pkg/tasks"
)

// Server encapsulates the Gossip MCP server and underlying subsystems.
type Server struct {
	mcpServer    *server.MCPServer
	store        *state.Store
	messages     *messages.Manager
	tasks        *tasks.Manager
	graph        *graph.Manager
	roleProvider roles.Provider
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

	s := &Server{
		store:        store,
		messages:     messages.NewManager(store),
		tasks:        tasks.NewManager(store),
		graph:        graph.NewManager(store),
		roleProvider: roleProv,
	}

	mcpSrv := server.NewMCPServer(
		"Gossip Agent Mesh",
		"1.0.0",
		server.WithToolCapabilities(true),
	)

	s.mcpServer = mcpSrv
	s.registerTools()
	return s
}

// ServeStdio starts listening for JSON-RPC 2.0 messages over standard I/O.
func (s *Server) ServeStdio() error {
	return server.ServeStdio(s.mcpServer)
}

func (s *Server) registerTools() {
	s.registerRoleTools()
	s.registerMessageTools()
	s.registerTaskTools()
	s.registerGraphTools()
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
		cat := getString(args, "category")
		desc := getString(args, "description")
		prompt := getString(args, "system_prompt")
		directives := getStringSlice(args, "directives")
		tools := getStringSlice(args, "recommended_tools")
		tags := getStringSlice(args, "capability_tags")

		if id == "" || title == "" || desc == "" || prompt == "" {
			return mcp.NewToolResultError("'id', 'title', 'description', and 'system_prompt' are required"), nil
		}
		if cat == "" {
			cat = "specialist"
		}

		customRole := roles.RoleDefinition{
			ID:               id,
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
		mcp.WithArray("milestone_titles", mcp.Description("List of milestone checkpoint titles")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := getArgsMap(req)
		title := getString(args, "title")
		desc := getString(args, "description")
		mTitles := getStringSlice(args, "milestone_titles")

		if title == "" || desc == "" {
			return mcp.NewToolResultError("title and description are required"), nil
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
}
