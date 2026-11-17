package coord

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RegistryMCPOptions configures a project-routing MCP server over a local device registry.
type RegistryMCPOptions struct {
	DefaultProfile string
	Projects       func(context.Context) (any, error)
	Resolve        func(context.Context, string, string, string) (*server.MCPServer, error)
	Inbox          func(context.Context, string, string, string, string) (any, error)
	JoinLocal      func(ctx context.Context, profile, project, agentName, permissionNote string, userPermission bool) (any, error)
}

// RegistryMCPServer exposes the project tools once and resolves membership for
// every individual call. Resolvers should return a process-local adapter whose
// credential and transport are scoped to exactly one membership.
func RegistryMCPServer(defaultProfile string, projects func(context.Context) (any, error), resolve func(context.Context, string, string, string) (*server.MCPServer, error), inbox ...func(context.Context, string, string, string, string) (any, error)) *server.MCPServer {
	var inboxFn func(context.Context, string, string, string, string) (any, error)
	if len(inbox) > 0 {
		inboxFn = inbox[0]
	}
	return RegistryMCPServerWithOptions(RegistryMCPOptions{
		DefaultProfile: defaultProfile,
		Projects:       projects,
		Resolve:        resolve,
		Inbox:          inboxFn,
	})
}

// RegistryMCPServerWithOptions exposes project discovery, optional permission-gated
// local project enrollment, and project-scoped tool routing.
func RegistryMCPServerWithOptions(opts RegistryMCPOptions) *server.MCPServer {
	defaultProfile := opts.DefaultProfile
	projects := opts.Projects
	resolve := opts.Resolve
	srv := server.NewMCPServer("Gossip coordination", "0.2.0", server.WithToolCapabilities(true))
	srv.AddTool(mcp.NewTool("gossip_projects",
		mcp.WithDescription("List authorized Gossip connections (and local-only discoverable projects on this machine), or join an explicitly user-approved local project. Never call operation=join without the user's explicit confirmation; LAN/FRP projects cannot be discovered or joined here."),
		mcp.WithString("operation", mcp.Description("list (default) or join (local machine only, requires user_permission=true)")),
		mcp.WithString("project", mcp.Description("For operation=join: local project_id or unambiguous local project name")),
		mcp.WithString("profile", mcp.Description("Local Gossip identity profile; defaults to the configured MCP profile")),
		mcp.WithString("agent_name", mcp.Description("For operation=join: display name for the local worker identity")),
		mcp.WithBoolean("user_permission", mcp.Description("For operation=join: must be true ONLY after the human user explicitly granted permission to join this local project")),
		mcp.WithString("permission_note", mcp.Description("For operation=join: brief summary of the user's confirmation"))),
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			op := r.GetString("operation", "list")
			var (
				v   any
				err error
			)
			switch op {
			case "", "list":
				v, err = projects(ctx)
			case "join", "join_local":
				if opts.JoinLocal == nil {
					return mcp.NewToolResultError("joining local projects is unavailable in this mode; LAN and FRP connections require explicit owner pairing or a project credential"), nil
				}
				args := r.GetArguments()
				permitted, _ := args["user_permission"].(bool)
				profile := r.GetString("profile", defaultProfile)
				if profile == "" {
					profile = defaultProfile
				}
				v, err = opts.JoinLocal(ctx, profile, r.GetString("project", ""), r.GetString("agent_name", ""), r.GetString("permission_note", ""), permitted)
			default:
				return mcp.NewToolResultError("unknown operation; use list or join"), nil
			}
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
		if name == "gossip_sync" && opts.Inbox != nil {
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
			if name == "gossip_sync" && opts.Inbox != nil && (operation == "queued" || operation == "dismiss_queued") {
				id, _ := args["item_id"].(string)
				if operation == "dismiss_queued" && id == "" {
					return mcp.NewToolResultError("item_id is required"), nil
				}
				if operation == "queued" {
					id = ""
				}
				value, err := opts.Inbox(ctx, profile, project, checkout, id)
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
