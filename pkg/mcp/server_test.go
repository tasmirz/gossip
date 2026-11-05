package mcp_test

import (
	"context"
	"os"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	gossipmcp "github.com/tasmirz/gossip/pkg/mcp"
	"github.com/tasmirz/gossip/pkg/roles"
	"github.com/tasmirz/gossip/pkg/state"
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

	// 3. Test gossip_introduce_remote_peer
	peerTool := srv.MCPServer().GetTool("gossip_introduce_remote_peer")
	if peerTool == nil {
		t.Fatal("Expected tool gossip_introduce_remote_peer to be registered")
	}

	res, err = peerTool.Handler(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "gossip_introduce_remote_peer",
			Arguments: map[string]any{
				"peer_agent_id": "remote-bob",
				"peer_role":     "security_specialist",
				"peer_endpoint": "frp://remote.cloud:7000",
				"peer_worktree": "/home/bob/backend",
			},
		},
	})
	if err != nil || res.IsError {
		t.Fatalf("gossip_introduce_remote_peer failed: %v, res: %+v", err, res)
	}

	remoteSess, found, _ := store.GetAgent("remote-bob")
	if !found || !remoteSess.IsRemote || remoteSess.RemoteEndpoint != "frp://remote.cloud:7000" {
		t.Fatalf("Remote peer not properly recorded: %+v", remoteSess)
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

	// 6. Test gossip_secret_transfer (local staging)
	dummyFile := tmpDir + "/secret.env"
	_ = os.WriteFile(dummyFile, []byte("SECRET_KEY=12345"), 0600)
	xfrTool := srv.MCPServer().GetTool("gossip_secret_transfer")
	if xfrTool == nil {
		t.Fatal("Expected tool gossip_secret_transfer to be registered")
	}
	res, err = xfrTool.Handler(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "gossip_secret_transfer",
			Arguments: map[string]any{
				"source_path": dummyFile,
				"description": "API credentials",
			},
		},
	})
	if err != nil || res.IsError {
		t.Fatalf("gossip_secret_transfer failed: %v, res: %+v", err, res)
	}
}

// Dummy context to satisfy unused imports if needed
var _ = context.Background
var _ = mcp.NewTool
