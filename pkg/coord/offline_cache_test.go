package coord_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/tasmirz/gossip/pkg/coord"
)

type offlineBackend struct {
	coord.Backend
	offline atomic.Bool
}

func (b *offlineBackend) CurrentPlan(ctx context.Context, p, t string) (coord.Plan, error) {
	if b.offline.Load() {
		return coord.Plan{}, errors.New("offline")
	}
	return b.Backend.CurrentPlan(ctx, p, t)
}
func (b *offlineBackend) ListTasks(ctx context.Context, p, t string, limit int) ([]coord.Task, error) {
	if b.offline.Load() {
		return nil, errors.New("offline")
	}
	return b.Backend.ListTasks(ctx, p, t, limit)
}
func (b *offlineBackend) TaskCounts(ctx context.Context, p, t string) (map[string]int, error) {
	if b.offline.Load() {
		return nil, errors.New("offline")
	}
	return b.Backend.TaskCounts(ctx, p, t)
}
func (b *offlineBackend) ReadEvents(ctx context.Context, p, t string, after int64, limit int) ([]coord.Event, error) {
	if b.offline.Load() {
		return nil, errors.New("offline")
	}
	return b.Backend.ReadEvents(ctx, p, t, after, limit)
}
func (b *offlineBackend) SyncProject(ctx context.Context, p, t string, after int64, limit int) (coord.ProjectSyncFrame, error) {
	if b.offline.Load() {
		return coord.ProjectSyncFrame{}, errors.New("offline")
	}
	return b.Backend.SyncProject(ctx, p, t, after, limit)
}
func TestOfflineSnapshotsSurviveRestartAndStayExplicit(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	task := create(t, s, p, owner, nil)
	b := &offlineBackend{Backend: s}
	path := filepath.Join(t.TempDir(), "outbox.db")
	open := func() *coord.MCPAdapter {
		o, err := coord.OpenOutbox(path, "https://home", p.ID, worker.PrincipalID, worker.Token, b)
		if err != nil {
			t.Fatal(err)
		}
		return coord.NewMCPAdapterWithOutbox(b, p.ID, worker.Token, o)
	}
	call := func(a *coord.MCPAdapter, name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		r, err := a.Server.GetTool(name).Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: args}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	a := open()
	r := call(a, "gossip_sync", map[string]any{"operation": "refresh_cache"})
	if r.IsError {
		t.Fatalf("refresh project cache: %+v", r)
	}
	var syncStatus coord.CacheSyncStatus
	if err := json.Unmarshal([]byte(r.Content[0].(mcp.TextContent).Text), &syncStatus); err != nil || syncStatus.ObservedAt <= 0 || !syncStatus.PlanAvailable || syncStatus.PlanRevision != 1 || syncStatus.TasksCached != 1 || syncStatus.EventsCached == 0 || syncStatus.SnapshotSequence == 0 || syncStatus.NextEventCursor != syncStatus.SnapshotSequence || !syncStatus.ResetRequired {
		t.Fatalf("cache sync status: %+v %v", syncStatus, err)
	}
	if r := call(a, "gossip_plan", map[string]any{"operation": "read"}); r.IsError {
		t.Fatalf("plan: %+v", r)
	}
	if r := call(a, "gossip_task", map[string]any{"operation": "list"}); r.IsError {
		t.Fatalf("tasks: %+v", r)
	}
	a.Close()
	b.offline.Store(true)
	a = open()
	defer a.Close()
	if r := call(a, "gossip_plan", map[string]any{"operation": "read"}); !r.IsError {
		t.Fatal("live read succeeded offline")
	}
	if r := call(a, "gossip_sync", map[string]any{"operation": "refresh_cache"}); !r.IsError {
		t.Fatal("cache refresh succeeded offline")
	}
	r = call(a, "gossip_plan", map[string]any{"operation": "cached"})
	if r.IsError {
		t.Fatal(r)
	}
	var snapshot coord.CachedSnapshot
	if err := json.Unmarshal([]byte(r.Content[0].(mcp.TextContent).Text), &snapshot); err != nil || snapshot.ObservedAt <= 0 || !strings.Contains(snapshot.Source, "cached") || !strings.Contains(string(snapshot.Value), "plan") {
		t.Fatalf("snapshot: %+v %v", snapshot, err)
	}
	r = call(a, "gossip_task", map[string]any{"operation": "cached"})
	if r.IsError || !strings.Contains(r.Content[0].(mcp.TextContent).Text, task.ID) {
		t.Fatalf("cached tasks: %+v", r)
	}
	r = call(a, "gossip_sync", map[string]any{"operation": "cached", "after": 0, "limit": 100})
	if r.IsError {
		t.Fatalf("cached events after restart: %+v", r)
	}
	var cachedEvents coord.CachedEventPage
	if err := json.Unmarshal([]byte(r.Content[0].(mcp.TextContent).Text), &cachedEvents); err != nil || len(cachedEvents.Events) == 0 || cachedEvents.ObservedAt <= 0 || !strings.Contains(cachedEvents.Source, "stale") {
		t.Fatalf("cached events: %+v %v", cachedEvents, err)
	}
}

func TestProjectCacheRefreshReadsAllTaskPages(t *testing.T) {
	s, _, project, owner, worker := setup(t)
	ctx := context.Background()
	const taskCount = 205
	for i := 0; i < taskCount; i++ {
		if _, err := s.CreateTask(ctx, project.ID, owner.Token, 1, "Paged task", "Include this task in the offline snapshot", "research", coord.ReportContract{ReviewType: "full_code"}, nil); err != nil {
			t.Fatalf("create task %d: %v", i, err)
		}
	}
	outbox, err := coord.OpenOutbox(filepath.Join(t.TempDir(), "outbox.db"), "https://home", project.ID, worker.PrincipalID, worker.Token, s)
	if err != nil {
		t.Fatal(err)
	}
	adapter := coord.NewMCPAdapterWithOutbox(s, project.ID, worker.Token, outbox)
	defer adapter.Close()
	result, err := adapter.Server.GetTool("gossip_sync").Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_sync", Arguments: map[string]any{"operation": "refresh_cache"}}})
	if err != nil || result.IsError {
		t.Fatalf("refresh cache: result=%+v err=%v", result, err)
	}
	var status coord.CacheSyncStatus
	if err = json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &status); err != nil {
		t.Fatal(err)
	}
	if status.TasksCached != taskCount || status.TaskNextCursor != "" {
		t.Fatalf("cache refresh stopped before the final task page: %+v", status)
	}
	if status.EventsCached <= 200 || status.EventsMayContinue || status.NextEventCursor != status.SnapshotSequence {
		t.Fatalf("cache refresh stopped before the final event page: %+v", status)
	}
	cachedResult, err := adapter.Server.GetTool("gossip_task").Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_task", Arguments: map[string]any{"operation": "cached"}}})
	if err != nil || cachedResult.IsError {
		t.Fatalf("read cached tasks: result=%+v err=%v", cachedResult, err)
	}
	var snapshot coord.CachedSnapshot
	if err = json.Unmarshal([]byte(cachedResult.Content[0].(mcp.TextContent).Text), &snapshot); err != nil {
		t.Fatal(err)
	}
	var tasks []coord.Task
	if err = json.Unmarshal(snapshot.Value, &tasks); err != nil || len(tasks) != taskCount {
		t.Fatalf("cached task pages were not aggregated: count=%d err=%v", len(tasks), err)
	}
}

func TestHomeInstanceRestartRetainsReplicaCursorAndDoesNotReplayEvents(t *testing.T) {
	s, dbPath, project, owner, worker := setup(t)
	task := create(t, s, project, owner, nil)
	ctx := context.Background()
	b := &offlineBackend{Backend: s}
	outbox, err := coord.OpenOutbox(filepath.Join(t.TempDir(), "outbox.db"), "https://home", project.ID, worker.PrincipalID, worker.Token, b)
	if err != nil {
		t.Fatal(err)
	}
	adapter := coord.NewMCPAdapterWithOutbox(b, project.ID, worker.Token, outbox)
	defer adapter.Close()
	call := func() coord.CacheSyncStatus {
		t.Helper()
		result, err := adapter.Server.GetTool("gossip_sync").Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_sync", Arguments: map[string]any{"operation": "refresh_cache"}}})
		if err != nil || result.IsError {
			t.Fatalf("refresh cache: result=%+v err=%v", result, err)
		}
		var status coord.CacheSyncStatus
		if err = json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &status); err != nil {
			t.Fatal(err)
		}
		return status
	}
	first := call()
	if !first.ResetRequired || first.EventsCached == 0 {
		t.Fatalf("initial sync did not build a full cache: %+v", first)
	}
	restarted, err := coord.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	b.Backend = restarted
	second := call()
	if second.ResetRequired || second.EventsCached != 0 || second.NextEventCursor != second.SnapshotSequence {
		t.Fatalf("routine home restart reset or replayed the replica cursor: %+v", second)
	}
	third := call()
	if third.ResetRequired || third.EventsCached != 0 || third.NextEventCursor != third.SnapshotSequence {
		t.Fatalf("stable home identity caused an unnecessary cache reset: %+v", third)
	}
	cached, err := adapter.Server.GetTool("gossip_task").Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_task", Arguments: map[string]any{"operation": "cached"}}})
	if err != nil {
		t.Fatal(err)
	}
	if cached.IsError || !strings.Contains(cached.Content[0].(mcp.TextContent).Text, task.ID) {
		t.Fatalf("restarted home snapshot missing task %s: %+v", task.ID, cached)
	}
}

func TestRevokedProjectAccessPurgesCachedSnapshotsAndEvents(t *testing.T) {
	s, _, project, owner, worker := setup(t)
	ctx := context.Background()
	create(t, s, project, owner, nil)
	b := &offlineBackend{Backend: s}
	outbox, err := coord.OpenOutbox(filepath.Join(t.TempDir(), "outbox.db"), "https://home", project.ID, worker.PrincipalID, worker.Token, b)
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	adapter := coord.NewMCPAdapterWithOutbox(b, project.ID, worker.Token, outbox)
	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		result, callErr := adapter.Server.GetTool(name).Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: args}})
		if callErr != nil {
			t.Fatal(callErr)
		}
		return result
	}
	if result := call("gossip_sync", map[string]any{"operation": "refresh_cache"}); result.IsError {
		t.Fatalf("initial cache refresh: %+v", result)
	}
	if err = s.RevokePrincipal(ctx, project.ID, owner.Token, worker.PrincipalID); err != nil {
		t.Fatal(err)
	}
	if result := call("gossip_sync", map[string]any{"operation": "refresh_cache"}); !result.IsError {
		t.Fatal("revoked principal refreshed the cache")
	}
	for _, nameAndArgs := range []struct {
		name string
		args map[string]any
	}{{"gossip_plan", map[string]any{"operation": "cached"}}, {"gossip_task", map[string]any{"operation": "cached"}}} {
		if result := call(nameAndArgs.name, nameAndArgs.args); !result.IsError {
			t.Fatalf("revoked principal retained %s snapshot: %+v", nameAndArgs.name, result)
		}
	}
	result := call("gossip_sync", map[string]any{"operation": "cached", "after": 0, "limit": 50})
	if !result.IsError || !strings.Contains(result.Content[0].(mcp.TextContent).Text, "not found") {
		t.Fatalf("revoked principal retained cached events: %+v", result)
	}
}
