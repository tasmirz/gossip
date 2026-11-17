package coord

import (
	"context"
	"path/filepath"
	"strings"
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

func TestRegistryMCPProjectsJoinRequiresHandlerAndForwardsPermission(t *testing.T) {
	noopResolve := func(context.Context, string, string, string) (*server.MCPServer, error) {
		return server.NewMCPServer("unused", "1"), nil
	}
	noopList := func(context.Context) (any, error) {
		return map[string]any{"connections": []any{}}, nil
	}

	// Without JoinLocal handler, operation=join must be refused.
	remoteOnly := RegistryMCPServer("remote-prof", noopList, noopResolve)
	res, err := remoteOnly.ListTools()["gossip_projects"].Handler(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: map[string]any{"operation": "join", "project": "p1", "user_permission": true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].(mcp.TextContent).Text, "LAN and FRP") {
		t.Fatalf("expected LAN/FRP rejection without JoinLocal, got %#v", res.Content)
	}

	var gotProfile, gotProject, gotAgent, gotNote string
	var gotPerm bool
	localSrv := RegistryMCPServerWithOptions(RegistryMCPOptions{
		DefaultProfile: "gesture",
		Projects:       noopList,
		Resolve:        noopResolve,
		JoinLocal: func(_ context.Context, profile, project, agentName, permissionNote string, userPermission bool) (any, error) {
			gotProfile, gotProject, gotAgent, gotNote, gotPerm = profile, project, agentName, permissionNote, userPermission
			return map[string]string{"status": "joined", "project_id": project}, nil
		},
	})
	res, err = localSrv.ListTools()["gossip_projects"].Handler(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: map[string]any{
			"operation":       "join",
			"project":         "gossip",
			"agent_name":      "antigravity",
			"user_permission": true,
			"permission_note": "User approved",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || gotProfile != "gesture" || gotProject != "gossip" || gotAgent != "antigravity" || gotNote != "User approved" || !gotPerm {
		t.Fatalf("unexpected join forwarding: isErr=%v profile=%q project=%q agent=%q note=%q perm=%v content=%#v", res.IsError, gotProfile, gotProject, gotAgent, gotNote, gotPerm, res.Content)
	}
}

func TestStoreEnrollLocalWorkerRequiresLocalAuthorityAndEnrollsWorker(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "coord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	proj, ownerCred, err := store.CreateProject(context.Background(), "gesture", "owner")
	if err != nil {
		t.Fatal(err)
	}
	// Mismatched home authority context must be rejected.
	badCtx := withLocalOwnerAuthority(context.Background(), "node_foreign")
	if _, err := store.EnrollLocalWorker(badCtx, proj.ID, "antigravity", "ok"); err != ErrUnauthorized {
		t.Fatalf("expected ErrUnauthorized for non-local context, got %v", err)
	}

	cred, err := store.EnrollLocalWorker(context.Background(), proj.ID, "antigravity", "User approved join")
	if err != nil {
		t.Fatalf("EnrollLocalWorker: %v", err)
	}
	if cred.PrincipalID == "" || cred.Token == "" {
		t.Fatalf("expected non-empty worker credential, got %+v", cred)
	}
	actor, err := store.WhoAmI(context.Background(), proj.ID, cred.Token)
	if err != nil {
		t.Fatalf("WhoAmI: %v", err)
	}
	if actor.Role != "worker" || actor.Name != "antigravity" {
		t.Fatalf("expected worker role, got %+v", actor)
	}
	// Worker cannot manage membership (must remain worker, not owner).
	if _, err := store.AddPrincipal(context.Background(), proj.ID, cred.Token, "extra", "worker"); err != ErrUnauthorized {
		t.Fatalf("worker must not gain membership.manage/owner rights, got %v", err)
	}
	// Owner remains intact.
	if valid, err := store.CredentialValid(context.Background(), proj.ID, ownerCred.Token); err != nil || !valid {
		t.Fatalf("owner credential broke: valid=%v err=%v", valid, err)
	}
}
