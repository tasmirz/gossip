package coord_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/tasmirz/gossip/pkg/coord"
)

func TestMCPClientPresenceColumnsMigrateExistingDatabase(t *testing.T) {
	store, path, _, _, _ := setup(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`ALTER TABLE agent_sessions DROP COLUMN client_version`, `ALTER TABLE agent_sessions DROP COLUMN client_name`} {
		if _, err = db.Exec(query); err != nil {
			db.Close()
			t.Fatalf("prepare existing schema: %v", err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := coord.Open(path)
	if err != nil {
		t.Fatalf("upgrade existing coordination database: %v", err)
	}
	defer migrated.Close()
}

func TestAgentPresenceIsProjectScopedAndSessionBound(t *testing.T) {
	store, _, project, owner, worker := setup(t)
	ctx := context.Background()
	initial, err := store.ListAgentPresence(ctx, project.ID, owner.Token)
	if err != nil || len(initial) != 2 {
		t.Fatalf("initial project roster: %+v %v", initial, err)
	}
	if err = store.HeartbeatAgent(ctx, project.ID, worker.Token, "worker-session"); err != nil {
		t.Fatal(err)
	}
	if err = store.HeartbeatAgent(ctx, project.ID, owner.Token, "worker-session"); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("another principal claimed the session ID: %v", err)
	}
	other, _, err := store.CreateProject(ctx, "private", "private owner")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.HeartbeatAgent(ctx, other.ID, worker.Token, "foreign-session"); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("heartbeat crossed project boundary: %v", err)
	}
	presence, err := store.ListAgentPresence(ctx, project.ID, owner.Token)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range presence {
		if item.PrincipalID == worker.PrincipalID && !item.Online {
			t.Fatalf("worker heartbeat not visible: %+v", item)
		}
		if item.PrincipalID == owner.PrincipalID && item.Online {
			t.Fatalf("owner incorrectly marked online: %+v", item)
		}
	}
	if err = store.EndAgentSession(ctx, project.ID, worker.Token, "worker-session"); err != nil {
		t.Fatal(err)
	}
	if err = store.EndAgentSession(ctx, project.ID, worker.Token, "worker-session"); err != nil {
		t.Fatalf("ending a session was not idempotent: %v", err)
	}
	presence, err = store.ListAgentPresence(ctx, project.ID, owner.Token)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range presence {
		if item.PrincipalID == worker.PrincipalID && item.Online {
			t.Fatalf("ended worker session remained online: %+v", item)
		}
	}
}

func TestAgentPresenceCapabilitiesAreAuthenticatedLabeledAndReplaceable(t *testing.T) {
	store, _, project, _, worker := setup(t)
	ctx := context.Background()
	initial := []coord.AgentCapability{{Name: "mcp_tools", Source: "adapter"}, {Name: "interrupt", Source: "host_reported"}}
	if err := store.HeartbeatAgentWithCapabilities(ctx, project.ID, worker.Token, "cap-session", initial); err != nil {
		t.Fatal(err)
	}
	find := func() coord.AgentPresence {
		t.Helper()
		presence, err := store.ListAgentPresence(ctx, project.ID, worker.Token)
		if err != nil {
			t.Fatal(err)
		}
		for _, agent := range presence {
			if agent.PrincipalID == worker.PrincipalID {
				return agent
			}
		}
		t.Fatal("worker presence missing")
		return coord.AgentPresence{}
	}
	agent := find()
	if len(agent.Capabilities) != 2 || agent.Capabilities[0].Source != "adapter" || agent.Capabilities[1].Source != "host_reported" {
		t.Fatalf("capability provenance was lost: %+v", agent.Capabilities)
	}
	if err := store.HeartbeatAgentWithCapabilities(ctx, project.ID, worker.Token, "cap-session", []coord.AgentCapability{{Name: "launch", Source: "host_reported"}}); err != nil {
		t.Fatal(err)
	}
	agent = find()
	if len(agent.Capabilities) != 1 || agent.Capabilities[0].Name != "launch" || agent.Capabilities[0].Source != "host_reported" {
		t.Fatalf("heartbeat did not replace stale capabilities: %+v", agent.Capabilities)
	}
	if err := store.HeartbeatAgentWithCapabilities(ctx, project.ID, worker.Token, "cap-session", []coord.AgentCapability{{Name: "membership.manage", Source: "host_reported"}}); err == nil {
		t.Fatal("accepted an unknown host capability")
	}
	if err := store.HeartbeatAgentWithCapabilities(ctx, project.ID, worker.Token, "cap-session", []coord.AgentCapability{{Name: "launch", Source: "adapter"}}); err == nil {
		t.Fatal("accepted host capability under the trusted adapter source")
	}
}

func TestMCPAdapterHeartbeatEndsOnClose(t *testing.T) {
	store, _, project, _, worker := setup(t)
	adapter, err := coord.NewMCPAdapterWithHostCapabilities(store, project.ID, worker.Token, nil, []string{"interrupt", "notification"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	clientConn, err := client.NewInProcessClient(adapter.Server)
	if err != nil {
		t.Fatal(err)
	}
	if err = clientConn.Start(ctx); err != nil {
		t.Fatal(err)
	}
	presence, err := store.ListAgentPresence(ctx, project.ID, worker.Token)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range presence {
		if item.PrincipalID == worker.PrincipalID && item.Online {
			t.Fatal("adapter reported online before MCP initialization")
		}
	}
	initialize := mcp.InitializeRequest{}
	initialize.Params.ProtocolVersion = "2025-11-25"
	initialize.Params.ClientInfo = mcp.Implementation{Name: "presence-test", Version: "1"}
	if _, err = clientConn.Initialize(ctx, initialize); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	foundOnline := false
	for time.Now().Before(deadline) {
		presence, err := store.ListAgentPresence(context.Background(), project.ID, worker.Token)
		if err != nil {
			t.Fatal(err)
		}
		if len(presence) == 2 && presence[1].Online {
			if len(presence[1].Capabilities) != 5 {
				t.Fatalf("adapter capabilities not reported: %+v", presence[1])
			}
			if presence[1].MCPClientName != "presence-test" || presence[1].MCPClientVersion != "1" {
				t.Fatalf("MCP client metadata not displayed: %+v", presence[1])
			}
			foundOnline = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !foundOnline {
		t.Fatal("adapter did not report online after successful MCP initialization")
	}
	adapter.Close()
	_ = clientConn.Close()
	presence, err = store.ListAgentPresence(context.Background(), project.ID, worker.Token)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range presence {
		if item.PrincipalID == worker.PrincipalID && item.Online {
			t.Fatalf("closed adapter still appears online: %+v", item)
		}
	}
}

func TestAgentPresenceTracksToolCallStatsAndReportedUsage(t *testing.T) {
	store, _, project, owner, worker := setup(t)
	ctx := context.Background()

	// Both direct MCPServer and RegistryMCPServer must record durable per-principal tool calls.
	workerSrv := coord.MCPServer(store, project.ID, worker.Token)
	callReq := mcp.CallToolRequest{}
	callReq.Params.Name = "gossip_join"
	callReq.Params.Arguments = map[string]any{}
	res, err := workerSrv.ListTools()["gossip_join"].Handler(ctx, callReq)
	if err != nil || res == nil || res.IsError {
		t.Fatalf("direct MCP call failed: res=%+v err=%v", res, err)
	}

	regSrv := coord.RegistryMCPServer("default", func(context.Context) (any, error) {
		return []any{}, nil
	}, func(_ context.Context, _ string, _ string, _ string) (*server.MCPServer, error) {
		return workerSrv, nil
	})
	errReq := mcp.CallToolRequest{}
	errReq.Params.Name = "gossip_read"
	errReq.Params.Arguments = map[string]any{"operation": "invalid_op", "project": project.ID}
	errRes, err := regSrv.ListTools()["gossip_read"].Handler(ctx, errReq)
	if err != nil || errRes == nil || !errRes.IsError {
		t.Fatalf("expected error tool result from invalid operation: res=%+v err=%v", errRes, err)
	}

	task := create(t, store, project, owner, nil)
	claim, err := store.ClaimTask(ctx, project.ID, worker.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.RecordAttemptUsage(ctx, project.ID, worker.Token, task.ID, claim.Attempt, "usage-req-1", coord.UsageReport{
		Provider: "anthropic", Model: "claude", Source: "provider_reported", InputTokens: 1200, OutputTokens: 300, CostMicros: 5000, Currency: "USD",
	}); err != nil {
		t.Fatal(err)
	}

	if err = store.RecordToolCall(ctx, project.ID, "invalid-token", "gossip_join", false); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("unauthorized RecordToolCall must fail: %v", err)
	}

	presence, err := store.ListAgentPresence(ctx, project.ID, owner.Token)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]coord.AgentPresence{}
	for _, item := range presence {
		byID[item.PrincipalID] = item
	}
	workerPresence := byID[worker.PrincipalID]
	if workerPresence.ToolStats.TotalCalls != 2 || workerPresence.ToolStats.ErrorCalls != 1 {
		t.Fatalf("unexpected worker tool stats: %+v", workerPresence.ToolStats)
	}
	if workerPresence.ToolStats.Tools["gossip_join"] != 1 || workerPresence.ToolStats.Tools["gossip_read"] != 1 {
		t.Fatalf("unexpected worker per-tool breakdown: %+v", workerPresence.ToolStats.Tools)
	}
	if workerPresence.ReportedUsage == nil || workerPresence.ReportedUsage.TotalTokens != 1500 || workerPresence.ReportedUsage.Records != 1 {
		t.Fatalf("unexpected worker reported usage: %+v", workerPresence.ReportedUsage)
	}
	ownerPresence := byID[owner.PrincipalID]
	if ownerPresence.ToolStats.TotalCalls != 0 || ownerPresence.ReportedUsage != nil {
		t.Fatalf("owner stats should remain zero without owner tool calls/usage: %+v", ownerPresence)
	}
}
