package mcp_test

import (
	"context"
	"os"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	gossipmcp "github.com/tasmirz/gossip/pkg/mcp"
	"github.com/tasmirz/gossip/pkg/messages"
	"github.com/tasmirz/gossip/pkg/roles"
	"github.com/tasmirz/gossip/pkg/state"
	"github.com/tasmirz/gossip/pkg/tasks"
)

func TestMCPServerInitializationAndDynamicRoles(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-mcp-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := state.NewStore(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	roleSrv, err := roles.NewService(tmpDir, "")
	if err != nil {
		t.Fatal(err)
	}

	srv := gossipmcp.NewServer(store, roleSrv)
	if srv == nil {
		t.Fatal("Expected non-nil MCP server")
	}

	// Verify standard roles loaded dynamically from JSON
	r, ok := roleSrv.GetRole("network_engineer")
	if !ok || r.Title == "" {
		t.Fatalf("Role network_engineer failed to load from JSON")
	}

	// Dynamically register custom role
	custom := roles.RoleDefinition{
		ID:           "devops_specialist",
		Title:        "Kubernetes & CI/CD Engineer",
		Category:     "engineering",
		Description:  "Automates deployments and cluster manifests",
		SystemPrompt: "You are a DevOps Specialist.",
		Directives:   []string{"Always validate YAML syntax"},
	}

	if err := roleSrv.RegisterRole(custom); err != nil {
		t.Fatalf("Failed to register custom role: %v", err)
	}

	registered, ok := roleSrv.GetRole("devops_specialist")
	if !ok || registered.Title != "Kubernetes & CI/CD Engineer" {
		t.Fatalf("Failed to retrieve dynamically registered role")
	}

	// Verify that the MCP server initialized with expected tools
	mcpServer := srv.MCPServer()
	if mcpServer == nil {
		t.Fatal("Expected non-nil MCPServer instance")
	}
}

func TestMCPConfiguredIdentityCannotBeSpoofedByToolArguments(t *testing.T) {
	store, err := state.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := gossipmcp.NewServerForAgent(store, nil, gossipmcp.AgentIdentity{ID: "worker-1", Role: "worker", Name: "Trusted Worker"})

	register := srv.MCPServer().GetTool("gossip_register_agent")
	result, err := register.Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_register_agent", Arguments: map[string]any{
		"agent_id": "orchestrator", "role": "orchestrator", "name": "Spoofed Owner",
	}}})
	if err != nil || result.IsError {
		t.Fatalf("configured identity registration failed: result=%+v err=%v", result, err)
	}
	if agent, found, err := store.GetAgent("worker-1"); err != nil || !found || agent.Role != "worker" || agent.Name != "Trusted Worker" {
		t.Fatalf("trusted server identity was not stored: agent=%+v found=%v err=%v", agent, found, err)
	}
	if _, found, err := store.GetAgent("orchestrator"); err != nil || found {
		t.Fatalf("model-supplied identity was registered: found=%v err=%v", found, err)
	}

	send := srv.MCPServer().GetTool("gossip_send_message")
	result, err = send.Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_send_message", Arguments: map[string]any{
		"from": "orchestrator", "role": "orchestrator", "to": "all", "content": "identity binding check",
	}}})
	if err != nil || result.IsError {
		t.Fatalf("bound message send failed: result=%+v err=%v", result, err)
	}
	entries, err := messages.NewManager(store).LoadAllMessages()
	if err != nil || len(entries) != 1 || entries[0].From != "worker-1" || entries[0].Role != "worker" {
		t.Fatalf("message sender identity was spoofable: entries=%+v err=%v", entries, err)
	}

	setGoal := srv.MCPServer().GetTool("gossip_set_goal")
	result, err = setGoal.Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_set_goal", Arguments: map[string]any{
		"title": "Must be denied", "description": "The model claims orchestrator", "caller_role": "orchestrator",
	}}})
	if err != nil || !result.IsError {
		t.Fatalf("model-supplied role escalated goal permission: result=%+v err=%v", result, err)
	}
	createTask := srv.MCPServer().GetTool("gossip_create_task")
	result, err = createTask.Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_create_task", Arguments: map[string]any{
		"title": "Must be denied", "description": "Worker claims orchestrator through target role", "role": "orchestrator",
	}}})
	if err != nil || !result.IsError {
		t.Fatalf("worker escalated task delegation: result=%+v err=%v", result, err)
	}

	otherTask, err := tasks.NewManager(store).CreateTask("Other agent's task", "Do not overwrite this task", "worker", "normal", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	updateTask := srv.MCPServer().GetTool("gossip_update_task")
	result, err = updateTask.Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_update_task", Arguments: map[string]any{
		"task_id": otherTask.ID, "status": tasks.StatusCompleted, "assigned_to": "worker-1",
	}}})
	if err != nil || !result.IsError {
		t.Fatalf("worker changed an unassigned task: result=%+v err=%v", result, err)
	}
}

func TestSecretTransferToolsAreNotExposedToAgents(t *testing.T) {
	store, err := state.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := gossipmcp.NewServer(store, nil)
	for _, name := range []string{"gossip_secret_transfer", "gsp_vault_transfer"} {
		if tool := srv.MCPServer().GetTool(name); tool != nil {
			t.Fatalf("unsafe legacy tool %s is still exposed", name)
		}
	}
}

func TestArbitraryRemoteQueryToolsAreNotExposedToAgents(t *testing.T) {
	store, err := state.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := gossipmcp.NewServer(store, nil)
	for _, name := range []string{"gossip_remote_query", "gsp_remote_query"} {
		if tool := srv.MCPServer().GetTool(name); tool != nil {
			t.Fatalf("arbitrary-endpoint tool %s is still exposed", name)
		}
	}
}

func TestLegacyHTTPFRPTunnelingDisabled(t *testing.T) {
	store, err := state.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := gossipmcp.NewServer(store, nil).MCPServer()
	before, err := store.LoadProjectState()
	if err != nil {
		t.Fatal(err)
	}
	setup := srv.GetTool("gossip_setup")
	res, err := setup.Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_setup", Arguments: map[string]any{"project_name": "should-not-initialize", "configure_tunnel": true}}})
	if err != nil || !res.IsError {
		t.Fatalf("legacy setup enabled HTTP FRP: %+v %v", res, err)
	}
	stored, err := store.LoadProjectState()
	if err != nil || stored.ProjectName != before.ProjectName || len(stored.Goals) != len(before.Goals) {
		t.Fatalf("rejected legacy tunnel setup mutated project state: %+v %v", stored, err)
	}
	for _, name := range []string{"gossip_setup_tunnel", "gsp_setup_tunnel"} {
		if tool := srv.GetTool(name); tool != nil {
			t.Fatalf("retired legacy tunnel tool %s is still exposed", name)
		}
	}
}

func TestMCPReidentifyAgent(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-mcp-reident-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := state.NewStore(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	roleSrv, _ := roles.NewService(tmpDir, "")
	srv := gossipmcp.NewServer(store, roleSrv)

	// Initial registration
	session := state.AgentSession{
		ID:   "reident-worker-1",
		Role: "worker",
	}
	if err := store.RegisterAgent(session); err != nil {
		t.Fatal(err)
	}

	tool := srv.MCPServer().GetTool("gossip_reidentify_agent")
	if tool == nil {
		t.Fatal("Expected tool gossip_reidentify_agent to be registered")
	}

	res, err := tool.Handler(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "gossip_reidentify_agent",
			Arguments: map[string]any{
				"agent_id": "reident-worker-1",
				"format":   "json",
			},
		},
	})
	if err != nil {
		t.Fatalf("Reidentify tool call error: %v", err)
	}
	if res.IsError {
		t.Fatalf("Reidentify tool call returned error: %+v", res)
	}

	agent, found, err := store.GetAgent("reident-worker-1")
	if err != nil || !found {
		t.Fatalf("Expected agent found in store")
	}
	if agent.SessionCount != 2 {
		t.Fatalf("Expected SessionCount=2, got %d", agent.SessionCount)
	}
}

func TestMCPSetupAndIntroduce(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-mcp-setup-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := state.NewStore(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	roleSrv, _ := roles.NewService(tmpDir, "")
	srv := gossipmcp.NewServer(store, roleSrv)

	// 1. Test gossip_setup
	setupTool := srv.MCPServer().GetTool("gossip_setup")
	if setupTool == nil {
		t.Fatal("Expected tool gossip_setup to be registered")
	}

	res, err := setupTool.Handler(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "gossip_setup",
			Arguments: map[string]any{
				"project_name": "TestSetupProject",
				"goals":        []any{"Build Mesh Core", "Deliver Zero-Exposure Vault"},
				"tech_stack":   []any{"Go", "TypeScript"},
			},
		},
	})
	if err != nil || res.IsError {
		t.Fatalf("gossip_setup failed: %v, res: %+v", err, res)
	}

	st, _ := store.LoadProjectState()
	if st.ProjectName != "TestSetupProject" || len(st.Goals) != 2 {
		t.Fatalf("State mismatch after gossip_setup: %+v", st)
	}

	// 2. Test gossip_introduce_agent
	introTool := srv.MCPServer().GetTool("gossip_introduce_agent")
	if introTool == nil {
		t.Fatal("Expected tool gossip_introduce_agent to be registered")
	}

	res, err = introTool.Handler(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "gossip_introduce_agent",
			Arguments: map[string]any{
				"agent_id":     "agent-intro-1",
				"role":         "network_engineer",
				"name":         "Alice Network Lead",
				"capabilities": []any{"tcp", "frp", "protocols"},
				"greeting":     "Greetings team, I am ready to route sockets.",
			},
		},
	})
	if err != nil || res.IsError {
		t.Fatalf("gossip_introduce_agent failed: %v, res: %+v", err, res)
	}

	sess, found, _ := store.GetAgent("agent-intro-1")
	if !found || sess.Role != "network_engineer" {
		t.Fatalf("Introduced agent not found in store: %+v", sess)
	}

	// 3. Remote membership must be established by the project-scoped mTLS flow,
	// not by a model-supplied peer ID, role, and endpoint.
	for _, name := range []string{"gossip_introduce_remote_peer", "gossip_handshake", "gsp_introduce_peer"} {
		if srv.MCPServer().GetTool(name) != nil {
			t.Fatalf("unauthenticated remote peer registration tool %s is still exposed", name)
		}
	}
	if _, found, err := store.GetAgent("remote-bob"); err != nil || found {
		t.Fatalf("retired remote registration unexpectedly changed local agent state: found=%v err=%v", found, err)
	}

	// 4. Test gossip_status
	statusTool := srv.MCPServer().GetTool("gossip_status")
	if statusTool == nil {
		t.Fatal("Expected tool gossip_status to be registered")
	}
	res, err = statusTool.Handler(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "gossip_status",
			Arguments: map[string]any{"format": "markdown"},
		},
	})
	if err != nil || res.IsError {
		t.Fatalf("gossip_status failed: %v, res: %+v", err, res)
	}

	// 5. Test gossip_cron_check
	cronTool := srv.MCPServer().GetTool("gossip_cron_check")
	if cronTool == nil {
		t.Fatal("Expected tool gossip_cron_check to be registered")
	}
	res, err = cronTool.Handler(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "gossip_cron_check",
			Arguments: map[string]any{"agent_id": "agent-intro-1"},
		},
	})
	if err != nil || res.IsError {
		t.Fatalf("gossip_cron_check failed: %v, res: %+v", err, res)
	}

}

// Dummy context to satisfy unused imports if needed
var _ = context.Background
var _ = mcp.NewTool
