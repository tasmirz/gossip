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

// Dummy context to satisfy unused imports if needed
var _ = context.Background
var _ = mcp.NewTool
