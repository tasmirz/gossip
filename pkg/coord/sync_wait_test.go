package coord_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/tasmirz/gossip/pkg/coord"
)

func TestSyncWaitReturnsWorkerReviewRequestWithoutAcknowledging(t *testing.T) {
	store, _, project, owner, worker := setup(t)
	ctx := context.Background()
	task, err := store.CreateTask(ctx, project.ID, owner.Token, 1, "Review design", "Create UML review", "design", coord.ReportContract{ReviewType: "diagram_report", Formats: []string{"markdown", "mermaid_class", "mermaid_sequence"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimTask(ctx, project.ID, worker.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := store.ReadEvents(ctx, project.ID, worker.Token, 0, 200)
	if err != nil || len(initial) == 0 {
		t.Fatalf("read initial events: %v", err)
	}
	after := initial[len(initial)-1].Sequence
	adapter := coord.NewMCPAdapter(store, project.ID, worker.Token)
	defer adapter.Close()
	time.AfterFunc(100*time.Millisecond, func() {
		_ = store.RequestReview(ctx, project.ID, worker.Token, task.ID, claim.Attempt)
	})
	result, err := adapter.Server.GetTool("gossip_sync").Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_sync", Arguments: map[string]any{"operation": "wait", "after": int(after), "limit": 100, "wait_seconds": 2}}})
	if err != nil || result.IsError {
		t.Fatalf("wait for event: %+v %v", result, err)
	}
	var page coord.EventWaitResult
	if err = json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &page); err != nil {
		t.Fatal(err)
	}
	if page.TimedOut || len(page.Events) == 0 || page.Events[0].Kind != "review.requested" || page.Events[0].EntityID != task.ID {
		t.Fatalf("wait did not return new event: %+v", page)
	}
	var payload map[string]any
	if err = json.Unmarshal(page.Events[0].Payload, &payload); err != nil || payload["review_type"] != "diagram_report" {
		t.Fatalf("wait omitted the planned review type: %+v %v", payload, err)
	}
	if checkpoint, checkpointErr := store.EventCheckpoint(ctx, project.ID, worker.Token); checkpointErr != nil || checkpoint != 0 {
		t.Fatalf("wait advanced the event checkpoint: %d %v", checkpoint, checkpointErr)
	}
}

func TestSyncWaitValidatesBoundedDuration(t *testing.T) {
	store, _, project, _, worker := setup(t)
	adapter := coord.NewMCPAdapter(store, project.ID, worker.Token)
	defer adapter.Close()
	result, err := adapter.Server.GetTool("gossip_sync").Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_sync", Arguments: map[string]any{"operation": "wait", "wait_seconds": 26}}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("wait longer than the bound was accepted")
	}
}
