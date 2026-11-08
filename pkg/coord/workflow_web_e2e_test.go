package coord_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/tasmirz/gossip/pkg/coord"
)

func TestParallelMCPFullCodeCandidatesReviewedThroughDashboardAPI(t *testing.T) {
	store, _, project, owner, workerA := setup(t)
	ctx := context.Background()
	workerB, err := store.AddPrincipal(ctx, project.ID, owner.Token, "Worker B", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.AcknowledgePlan(ctx, project.ID, workerB.Token, 1); err != nil {
		t.Fatal(err)
	}
	repository, err := store.CreateRepository(ctx, project.ID, owner.Token, "parallel repository")
	if err != nil {
		t.Fatal(err)
	}
	workspaceA, err := store.RegisterWorkspace(ctx, project.ID, workerA.Token, repository.ID, "worker A", "node-a", "/private/worktree-a", "feature/a", "base-a", false)
	if err != nil {
		t.Fatal(err)
	}
	workspaceB, err := store.RegisterWorkspace(ctx, project.ID, workerB.Token, repository.ID, "worker B", "node-b", "/private/worktree-b", "feature/b", "base-b", false)
	if err != nil {
		t.Fatal(err)
	}
	web := coord.NewWebHandler(store)
	request := func(method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		var payload []byte
		if body != nil {
			payload, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest(method, "http://localhost:8081"+path, bytes.NewReader(payload))
		req.RemoteAddr = "127.0.0.1:12345"
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "http://localhost:8081")
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		web.ServeHTTP(response, req)
		return response
	}
	login := request(http.MethodPost, "/api/login", map[string]string{"project_id": project.ID, "token": owner.Token}, nil)
	if login.Code != http.StatusOK || len(login.Result().Cookies()) == 0 {
		t.Fatalf("owner login failed: %d %s", login.Code, login.Body.String())
	}
	ownerCookie := login.Result().Cookies()[0]
	workers := []struct {
		credential coord.Credential
		workspace  coord.Workspace
		name       string
		diff       string
	}{
		{workerA, workspaceA, "API branch", "diff --git a/api.go b/api.go\n--- a/api.go\n+++ b/api.go\n@@ -1 +1 @@\n-old api\n+new api\n"},
		{workerB, workspaceB, "UI branch", "diff --git a/ui.go b/ui.go\n--- a/ui.go\n+++ b/ui.go\n@@ -1 +1 @@\n-old ui\n+new ui\n"},
	}
	tasks := make([]coord.Task, len(workers))
	candidates := make([]coord.CandidateReport, len(workers))
	reports := make([]coord.Report, len(workers))
	for index, worker := range workers {
		created := request(http.MethodPost, "/api/projects/"+project.ID+"/tasks", map[string]any{
			"operation": "create", "request_id": "parallel-review-" + worker.name, "revision": 1,
			"title": "Review " + worker.name, "description": "Prepare a complete independent source change",
			"kind": "implementation", "contract": map[string]any{"review_type": "full_code", "formats": []string{"markdown"}},
		}, ownerCookie)
		if created.Code != http.StatusOK {
			t.Fatalf("dashboard task planning failed: %d %s", created.Code, created.Body.String())
		}
		if err = json.Unmarshal(created.Body.Bytes(), &tasks[index]); err != nil {
			t.Fatal(err)
		}
		assigned := request(http.MethodPost, "/api/projects/"+project.ID+"/tasks", map[string]any{"operation": "assign", "id": tasks[index].ID, "recipient": worker.credential.PrincipalID}, ownerCookie)
		if assigned.Code != http.StatusOK {
			t.Fatalf("dashboard assignment failed: %d %s", assigned.Code, assigned.Body.String())
		}
		server := coord.MCPServer(store, project.ID, worker.credential.Token)
		call := func(tool string, args map[string]any) *mcp.CallToolResult {
			t.Helper()
			toolHandler := server.GetTool(tool)
			if toolHandler == nil {
				t.Fatalf("missing worker tool %s", tool)
			}
			result, callErr := toolHandler.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: tool, Arguments: args}})
			if callErr != nil || result.IsError {
				t.Fatalf("worker %s calling %s: result=%+v err=%v", worker.name, tool, result, callErr)
			}
			return result
		}
		claimed := call("gossip_task", map[string]any{"operation": "claim", "task_id": tasks[index].ID, "workspace_id": worker.workspace.ID, "refresh_git": false, "auto_renew": false})
		var attempt coord.Task
		if err = json.Unmarshal([]byte(claimed.Content[0].(mcp.TextContent).Text), &attempt); err != nil || attempt.Attempt == 0 {
			t.Fatalf("worker did not claim planned task: %+v %v", attempt, err)
		}
		candidateResult := call("gossip_submit", map[string]any{"operation": "save_candidate", "task_id": tasks[index].ID, "attempt": attempt.Attempt, "baseline": worker.workspace.Head, "body": worker.diff})
		if err = json.Unmarshal([]byte(candidateResult.Content[0].(mcp.TextContent).Text), &candidates[index]); err != nil || candidates[index].Digest == "" {
			t.Fatalf("worker candidate omitted its digest: %+v %v", candidates[index], err)
		}
		requested := call("gossip_task", map[string]any{"operation": "request_review", "task_id": tasks[index].ID, "attempt": attempt.Attempt})
		if !strings.Contains(requested.Content[0].(mcp.TextContent).Text, `"requested":true`) {
			t.Fatalf("worker review request was not acknowledged: %s", requested.Content[0].(mcp.TextContent).Text)
		}
		submitted := call("gossip_submit", map[string]any{"task_id": tasks[index].ID, "attempt": attempt.Attempt, "baseline": worker.workspace.Head, "body": "Reviewed the complete " + worker.name + " candidate; no blocking issue found."})
		if err = json.Unmarshal([]byte(submitted.Content[0].(mcp.TextContent).Text), &reports[index]); err != nil || reports[index].ID == "" {
			t.Fatalf("worker report submission failed: %+v %v", reports[index], err)
		}
	}
	eventsResponse := request(http.MethodGet, "/api/projects/"+project.ID+"/events?recent=1", nil, ownerCookie)
	var events []coord.Event
	if err = json.Unmarshal(eventsResponse.Body.Bytes(), &events); eventsResponse.Code != http.StatusOK || err != nil {
		t.Fatalf("owner dashboard could not load review activity: status=%d err=%v body=%s", eventsResponse.Code, err, eventsResponse.Body.String())
	}
	requestedCandidates := map[string]bool{}
	for _, event := range events {
		if event.Kind != "review.requested" {
			continue
		}
		var payload struct {
			CandidateID     string `json:"candidate_id"`
			CandidateDigest string `json:"candidate_digest"`
		}
		if err = json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		for index := range candidates {
			if event.EntityID == tasks[index].ID && payload.CandidateID == candidates[index].ID && payload.CandidateDigest == candidates[index].Digest {
				requestedCandidates[candidates[index].ID] = true
			}
		}
	}
	for index, candidate := range candidates {
		if !requestedCandidates[candidate.ID] {
			t.Fatalf("owner dashboard did not receive worker %d's pinned candidate review request", index)
		}
		page := request(http.MethodGet, "/api/projects/"+project.ID+"/candidate-page?id="+candidate.ID+"&offset=0&limit=4096", nil, ownerCookie)
		var candidatePage coord.CandidatePage
		if err = json.Unmarshal(page.Body.Bytes(), &candidatePage); page.Code != http.StatusOK || err != nil || candidatePage.Digest != candidate.Digest || !strings.Contains(candidatePage.Excerpt, workers[index].diff) {
			t.Fatalf("owner could not inspect exact candidate %s: status=%d err=%v body=%s", candidate.ID, page.Code, err, page.Body.String())
		}
		reportResponse := request(http.MethodGet, "/api/projects/"+project.ID+"/report?id="+reports[index].ID, nil, ownerCookie)
		var ownerReport coord.Report
		if err = json.Unmarshal(reportResponse.Body.Bytes(), &ownerReport); reportResponse.Code != http.StatusOK || err != nil || ownerReport.Digest != reports[index].Digest || !strings.Contains(ownerReport.Body, workers[index].name) || ownerReport.SourceCandidateID != candidate.ID {
			t.Fatalf("owner could not inspect matching report %s: status=%d err=%v body=%s", reports[index].ID, reportResponse.Code, err, reportResponse.Body.String())
		}
		accepted := request(http.MethodPost, "/api/projects/"+project.ID+"/review", map[string]any{"operation": "accept", "id": reports[index].ID, "digest": reports[index].Digest}, ownerCookie)
		if accepted.Code != http.StatusOK {
			t.Fatalf("owner could not accept exact worker report: %d %s", accepted.Code, accepted.Body.String())
		}
		var outcome map[string]bool
		if err = json.Unmarshal(accepted.Body.Bytes(), &outcome); err != nil || !outcome["accepted"] {
			t.Fatalf("dashboard did not confirm report acceptance: %s %v", accepted.Body.String(), err)
		}
	}
	for index := range tasks {
		finalTask, getErr := store.GetTask(ctx, project.ID, owner.Token, tasks[index].ID)
		if getErr != nil || finalTask.Status != "accepted" {
			t.Fatalf("parallel review task %d did not reach accepted: %+v %v", index, finalTask, getErr)
		}
		if reports[index].SourceCandidateID != candidates[index].ID || reports[index].SourceCandidateDigest != candidates[index].Digest {
			t.Fatalf("accepted report lost its exact source candidate: report=%+v candidate=%+v", reports[index], candidates[index])
		}
	}
}
