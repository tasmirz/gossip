package coord

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func TestRegistryMCPResolvesEachRequestAndExposesDiscovery(t *testing.T) {
	selected := server.NewMCPServer("selected", "1", server.WithToolCapabilities(true))
	selected.AddTool(mcp.NewTool("gossip_plan", mcp.WithString("operation")), func(_ context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if _, ok := r.GetArguments()["project"]; ok {
			t.Fatal("routing field leaked into project-scoped handler")
		}
		return mcp.NewToolResultText("selected:" + r.GetArguments()["operation"].(string)), nil
	})
	router := RegistryMCPServer("default", func(context.Context) (any, error) {
		return []map[string]string{{"project_id": "p1", "profile": "default"}}, nil
	}, func(_ context.Context, profile, project, checkout string) (*server.MCPServer, error) {
		if profile != "default" || project != "p1" || checkout != "" {
			t.Fatalf("unexpected route %q %q %q", profile, project, checkout)
		}
		return selected, nil
	})
	tools := router.ListTools()
	if _, ok := tools["gossip_projects"]; !ok {
		t.Fatal("project discovery tool missing")
	}
	if _, ok := tools["gossip_plan"].Tool.InputSchema.Properties["project"]; !ok {
		t.Fatal("project selector missing from schema")
	}
	result, err := tools["gossip_plan"].Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"operation": "read", "project": "p1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 || result.Content[0].(mcp.TextContent).Text != "selected:read" {
		t.Fatalf("unexpected result %#v", result.Content)
	}
}
