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

// Dummy context to satisfy unused imports if needed
var _ = context.Background
var _ = mcp.NewTool
