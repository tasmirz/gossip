package coord

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RegistryMCPServer exposes the project tools once and resolves membership for
// every individual call. Resolvers should return a process-local adapter whose
// credential and transport are scoped to exactly one membership.
func RegistryMCPServer(defaultProfile string, projects func(context.Context) (any, error), resolve func(context.Context, string, string, string) (*server.MCPServer, error), inbox ...func(context.Context, string, string, string, string) (any, error)) *server.MCPServer {
	srv := server.NewMCPServer("Gossip coordination", "0.2.0", server.WithToolCapabilities(true))
	srv.AddTool(mcp.NewTool("gossip_projects",
		mcp.WithDescription("List authorized local Gossip projects and identities available to this process. Use the stable project_id and profile on calls when more than one connection exists.")),
		func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			v, err := projects(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			b, err := json.Marshal(v)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(b)), nil
		})
	base := MCPServer(nil, "", "")
	for name, definition := range base.ListTools() {
		tool := definition.Tool
		tool.Description += " Registry mode: project may be a project ID or an unambiguous name; profile selects a local identity. Omit these only when the configured profile and checkout mapping resolve uniquely."
		props := map[string]any{}
		for k, v := range tool.InputSchema.Properties {
			props[k] = v
		}
		props["project"] = map[string]any{"type": "string", "description": "Stable project ID or unambiguous configured project name"}
		props["profile"] = map[string]any{"type": "string", "description": "Local Gossip identity profile; defaults to the configured MCP profile"}
		props["checkout"] = map[string]any{"type": "string", "description": "Optional checkout path used to select a configured project mapping"}
		if name == "gossip_sync" && len(inbox) > 0 {
			tool.Description += " Use queued for the device inbox or dismiss_queued with item_id after handling."
			props["item_id"] = map[string]any{"type": "string"}
		}
		tool.InputSchema.Properties = props
		srv.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := request.GetArguments()
			project, _ := args["project"].(string)
			profile, _ := args["profile"].(string)
			checkout, _ := args["checkout"].(string)
			if profile == "" {
				profile = defaultProfile
			}
			operation, _ := args["operation"].(string)
			if name == "gossip_sync" && len(inbox) > 0 && (operation == "queued" || operation == "dismiss_queued") {
				id, _ := args["item_id"].(string)
				if operation == "dismiss_queued" && id == "" {
					return mcp.NewToolResultError("item_id is required"), nil
				}
				if operation == "queued" {
					id = ""
				}
				value, err := inbox[0](ctx, profile, project, checkout, id)
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				encoded, err := json.Marshal(value)
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				return mcp.NewToolResultText(string(encoded)), nil
			}
			backend, err := resolve(ctx, profile, project, checkout)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			selected, ok := backend.ListTools()[name]
			if !ok {
				return mcp.NewToolResultError(fmt.Sprintf("tool %q is unavailable for selected project", name)), nil
			}
			for _, k := range []string{"project", "profile", "checkout"} {
				delete(args, k)
			}
			request.Params.Arguments = args
			request.Params.RawArguments = nil
			return selected.Handler(ctx, request)
		})
	}
	return srv
}

// AddFileReservationTool installs the local registry reservation operations.
func AddFileReservationTool(srv *server.MCPServer, profile string, call func(context.Context, string, string, string, string, string, []string) ([]FileReservation, error)) {
	srv.AddTool(mcp.NewTool("gossip_file_locks", mcp.WithDescription("Reserve local worktree paths as a set, renew or release a reservation handle, or list active reservations."),
		mcp.WithString("operation", mcp.Required()), mcp.WithString("profile"), mcp.WithString("project"), mcp.WithString("checkout"),
		mcp.WithString("handle"), mcp.WithArray("paths", mcp.WithStringItems())),
		func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			reservations, err := call(ctx, request.GetString("profile", profile), request.GetString("project", ""), request.GetString("checkout", ""), request.GetString("operation", ""), request.GetString("handle", ""), request.GetStringSlice("paths", nil))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			payload, err := json.Marshal(reservations)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(payload)), nil
		})
}
