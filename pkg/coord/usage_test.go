package coord_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/tasmirz/gossip/pkg/coord"
)

func TestAttemptUsageIsOwnedIdempotentScopedAndVisible(t *testing.T) {
	s, _, project, owner, worker := setup(t)
	ctx := context.Background()
	task := create(t, s, project, owner, nil)
	claim, err := s.ClaimTask(ctx, project.ID, worker.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	usage := coord.UsageReport{Provider: "ExampleAI", Model: "economy-code", Source: "provider_reported", InputTokens: 1250, OutputTokens: 340, CostMicros: 2750, Currency: "usd"}
	first, err := s.RecordAttemptUsage(ctx, project.ID, worker.Token, task.ID, claim.Attempt, "usage-attempt-1", usage)
	if err != nil {
		t.Fatal(err)
	}
	if first.TaskID != task.ID || first.Attempt != claim.Attempt || first.PrincipalID != worker.PrincipalID || first.Currency != "USD" || first.RecordedAt == 0 {
		t.Fatalf("unexpected recorded usage: %+v", first)
	}
	retry, err := s.RecordAttemptUsage(ctx, project.ID, worker.Token, task.ID, claim.Attempt, "usage-attempt-1", usage)
	if err != nil || retry.RecordedAt != first.RecordedAt || retry.InputTokens != first.InputTokens {
		t.Fatalf("stable usage retry changed the record: %+v %v", retry, err)
	}
	changed := usage
	changed.OutputTokens++
	if _, err = s.RecordAttemptUsage(ctx, project.ID, worker.Token, task.ID, claim.Attempt, "usage-attempt-1", changed); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("changed retry payload = %v", err)
	}
	otherWorker, err := s.AddPrincipal(ctx, project.ID, owner.Token, "Other worker", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecordAttemptUsage(ctx, project.ID, otherWorker.Token, task.ID, claim.Attempt, "usage-other", usage); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("non-owner agent recorded attempt usage: %v", err)
	}
	if _, err = s.RecordAttemptUsage(ctx, project.ID, worker.Token, task.ID, claim.Attempt, "usage-negative", coord.UsageReport{Provider: "ExampleAI", Model: "economy-code", Source: "estimated", InputTokens: -1}); err == nil {
		t.Fatal("negative token count was accepted")
	}
	budget, err := s.GetBudget(ctx, project.ID, owner.Token)
	if err != nil {
		t.Fatal(err)
	}
	if budget.ReportedUsage.Records != 1 || budget.ReportedUsage.InputTokens != 1250 || budget.ReportedUsage.OutputTokens != 340 || budget.ReportedUsage.TotalTokens != 1590 || budget.ReportedUsage.Sources["provider_reported"] != 1 || len(budget.ReportedUsage.Costs) != 1 || budget.ReportedUsage.Costs[0].Currency != "USD" || budget.ReportedUsage.Costs[0].CostMicros != 2750 {
		t.Fatalf("project budget summary omitted or misreported usage: %+v", budget.ReportedUsage)
	}
	otherProject, otherOwner, err := s.CreateProject(ctx, "usage isolation", "other owner")
	if err != nil {
		t.Fatal(err)
	}
	otherBudget, err := s.GetBudget(ctx, otherProject.ID, otherOwner.Token)
	if err != nil || otherBudget.ReportedUsage.Records != 0 || otherBudget.ReportedUsage.InputTokens != 0 {
		t.Fatalf("usage crossed project boundary: %+v %v", otherBudget.ReportedUsage, err)
	}
}

func TestMCPUsageToolRecordsAttemptSummary(t *testing.T) {
	s, _, project, owner, worker := setup(t)
	task := create(t, s, project, owner, nil)
	claim, err := s.ClaimTask(context.Background(), project.ID, worker.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	tool := coord.MCPServer(s, project.ID, worker.Token).GetTool("gossip_usage")
	result, err := tool.Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_usage", Arguments: map[string]any{"task_id": task.ID, "attempt": claim.Attempt, "request_id": "mcp-usage-1", "provider": "ExampleAI", "model": "fast", "source": "host_reported", "input_tokens": 100, "output_tokens": 25, "cost_micros": 0}}})
	if err != nil || result.IsError {
		t.Fatalf("gossip_usage: %+v %v", result, err)
	}
	var got coord.UsageReport
	if err = json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &got); err != nil {
		t.Fatal(err)
	}
	if got.Source != "host_reported" || got.InputTokens != 100 || got.OutputTokens != 25 || got.PrincipalID != worker.PrincipalID {
		t.Fatalf("MCP usage result: %+v", got)
	}
}

func TestBudgetAPIAndDashboardExposeUsageProvenance(t *testing.T) {
	s, _, project, owner, worker := setup(t)
	ctx := context.Background()
	task := create(t, s, project, owner, nil)
	claim, err := s.ClaimTask(ctx, project.ID, worker.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecordAttemptUsage(ctx, project.ID, worker.Token, task.ID, claim.Attempt, "ui-usage-1", coord.UsageReport{Provider: "ExampleAI", Model: "fast", Source: "estimated", InputTokens: 20, OutputTokens: 7, CostMicros: 12345, Currency: "EUR"}); err != nil {
		t.Fatal(err)
	}
	h := coord.NewWebHandler(s)
	login := httptest.NewRequest(http.MethodPost, "http://localhost:8081/api/login", strings.NewReader(`{"project_id":"`+project.ID+`","token":"`+owner.Token+`"}`))
	login.RemoteAddr = "127.0.0.1:12345"
	login.Header.Set("Origin", "http://localhost:8081")
	login.Header.Set("Content-Type", "application/json")
	loginResult := httptest.NewRecorder()
	h.ServeHTTP(loginResult, login)
	if loginResult.Code != http.StatusOK || len(loginResult.Result().Cookies()) == 0 {
		t.Fatalf("owner dashboard login: %d %s", loginResult.Code, loginResult.Body.String())
	}
	request := httptest.NewRequest(http.MethodGet, "http://localhost:8081/api/projects/"+project.ID+"/budget", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.AddCookie(loginResult.Result().Cookies()[0])
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	var budget coord.Budget
	if err = json.Unmarshal(response.Body.Bytes(), &budget); response.Code != http.StatusOK || err != nil || budget.ReportedUsage.TotalTokens != 27 || budget.ReportedUsage.Sources["estimated"] != 1 {
		t.Fatalf("budget API usage payload: status=%d body=%s err=%v", response.Code, response.Body.String(), err)
	}
	asset := httptest.NewRecorder()
	assetRequest := httptest.NewRequest(http.MethodGet, "http://localhost:8081/app.js", nil)
	assetRequest.RemoteAddr = "127.0.0.1:12345"
	h.ServeHTTP(asset, assetRequest)
	if asset.Code != http.StatusOK || !strings.Contains(asset.Body.String(), "reported_usage") || !strings.Contains(asset.Body.String(), "provider billing is unavailable") {
		t.Fatal("dashboard asset does not display usage totals and their limits")
	}
}
