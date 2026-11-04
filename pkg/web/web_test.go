package web_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/tasmirz/gossip/pkg/roles"
	"github.com/tasmirz/gossip/pkg/state"
	"github.com/tasmirz/gossip/pkg/web"
)

func TestWebDashboardEndpoints(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-web-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := state.NewStore(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	roleProv := roles.GetDefaultService()
	server := web.NewServer(8080, store, roleProv)
	handler := server.Handler()

	// 1. Test GET / (HTML Index)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200 for /, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !bytes.Contains([]byte(body), []byte("GOSSIP MESH // MULTI-PROJECT CONTROL PANEL")) {
		t.Fatalf("Expected HTML to contain title")
	}
	if !bytes.Contains([]byte(body), []byte("border-radius: 0 !important")) {
		t.Fatalf("Expected zero border radius style constraint")
	}
	if !bytes.Contains([]byte(body), []byte("body.light-theme")) {
		t.Fatalf("Expected light theme support in stylesheet")
	}

	// 2. Test GET /api/projects
	req = httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200 for /api/projects, got %d", rec.Code)
	}

	// 3. Test GET /api/status
	req = httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200 for /api/status, got %d", rec.Code)
	}

	// 3. Test POST /api/tasks (Create Task)
	taskPayload := map[string]interface{}{
		"title":       "Test Web Task",
		"description": "Task created from web dashboard",
		"role":        "worker",
		"priority":    "high",
	}
	data, _ := json.Marshal(taskPayload)
	req = httptest.NewRequest(http.MethodPost, "/api/tasks", bytes.NewBuffer(data))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200 for POST /api/tasks, got %d", rec.Code)
	}

	// 4. Test GET /api/tasks (List Tasks)
	req = httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200 for GET /api/tasks, got %d", rec.Code)
	}

	// 5. Test POST /api/messages (Send Broadcast)
	msgPayload := map[string]interface{}{
		"from":    "web-tester",
		"role":    "orchestrator",
		"subject": "Sprint Notice",
		"content": "All agents resume work",
	}
	data, _ = json.Marshal(msgPayload)
	req = httptest.NewRequest(http.MethodPost, "/api/broadcast", bytes.NewBuffer(data))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200 for POST /api/broadcast, got %d", rec.Code)
	}

	// 6. Test POST /api/moe/convene (Convene MoE Council)
	moePayload := map[string]interface{}{
		"title":        "Evaluate In-Memory vs WAL",
		"query":        "Evaluate crash consistency and lock contention",
		"domain":       "concurrency, storage",
		"initiator":    "test-user",
		"max_parallel": 4,
	}
	data, _ = json.Marshal(moePayload)
	req = httptest.NewRequest(http.MethodPost, "/api/moe/convene", bytes.NewBuffer(data))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200 for POST /api/moe/convene, got %d", rec.Code)
	}

	// 7. Test POST /api/tasks/modify (Modify Task Attributes)
	var createdTask struct {
		ID string `json:"id"`
	}
	// Extract task ID from previous list
	req = httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var taskList []struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &taskList)
	if len(taskList) == 0 {
		t.Fatalf("Expected at least one task in list")
	}
	createdTask.ID = taskList[0].ID

	newTitle := "Updated Task Title"
	newRole := "systems_architect"
	newPriority := "high"
	newStatus := "in_progress"
	newAssignee := "worker-1"
	criteria := []string{"Criterion 1", "Criterion 2"}
	modPayload := map[string]interface{}{
		"task_id":       createdTask.ID,
		"title":         newTitle,
		"role":          newRole,
		"priority":      newPriority,
		"status":        newStatus,
		"assigned_to":   newAssignee,
		"criteria":      criteria,
		"progress_note": "Starting execution",
	}
	data, _ = json.Marshal(modPayload)
	req = httptest.NewRequest(http.MethodPost, "/api/tasks/modify", bytes.NewBuffer(data))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200 for POST /api/tasks/modify, got %d: %s", rec.Code, rec.Body.String())
	}
	var modifiedRes struct {
		Title      string `json:"title"`
		Role       string `json:"role"`
		Status     string `json:"status"`
		AssignedTo string `json:"assignee"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &modifiedRes)
	if modifiedRes.Title != newTitle || modifiedRes.Role != newRole || modifiedRes.Status != newStatus {
		t.Fatalf("ModifyTask did not update fields as expected: %+v", modifiedRes)
	}

	// 8. Test POST /api/artifacts/create (Create New Plan Artifact)
	planPayload := map[string]interface{}{
		"name":     "DEPLOYMENT_PLAN.md",
		"location": "artifacts",
		"content":  "# High Availability Deployment Plan\n\nPhase 1: Rollout...",
	}
	data, _ = json.Marshal(planPayload)
	req = httptest.NewRequest(http.MethodPost, "/api/artifacts/create", bytes.NewBuffer(data))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200 for POST /api/artifacts/create, got %d: %s", rec.Code, rec.Body.String())
	}

	// 9. Test GET /api/artifacts (List Plans & Artifacts)
	req = httptest.NewRequest(http.MethodGet, "/api/artifacts", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200 for GET /api/artifacts, got %d", rec.Code)
	}
	var artifacts []struct {
		Name    string `json:"name"`
		RelPath string `json:"rel_path"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &artifacts)
	foundPlan := false
	for _, a := range artifacts {
		if a.Name == "DEPLOYMENT_PLAN.md" {
			foundPlan = true
			break
		}
	}
	if !foundPlan {
		t.Fatalf("Expected to find DEPLOYMENT_PLAN.md in artifacts list, got %+v", artifacts)
	}

	// 10. Test GET /api/artifacts/content
	req = httptest.NewRequest(http.MethodGet, "/api/artifacts/content?file=.local/gossip/artifacts/DEPLOYMENT_PLAN.md", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200 for GET /api/artifacts/content, got %d: %s", rec.Code, rec.Body.String())
	}
	var contentRes struct {
		Content string `json:"content"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &contentRes)
	if !bytes.Contains([]byte(contentRes.Content), []byte("High Availability Deployment Plan")) {
		t.Fatalf("Artifact content mismatch: %s", contentRes.Content)
	}

	// 11. Test POST /api/artifacts/save (Save Modified Plan)
	savePayload := map[string]interface{}{
		"file":    ".local/gossip/artifacts/DEPLOYMENT_PLAN.md",
		"content": "# Updated Plan\n\nPhase 2 Complete.",
	}
	data, _ = json.Marshal(savePayload)
	req = httptest.NewRequest(http.MethodPost, "/api/artifacts/save", bytes.NewBuffer(data))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200 for POST /api/artifacts/save, got %d: %s", rec.Code, rec.Body.String())
	}

	// 12. Security Test: Path Traversal on Artifacts Must Be Blocked
	req = httptest.NewRequest(http.MethodGet, "/api/artifacts/content?file=../../../../etc/passwd", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("Path traversal attempt should have been rejected, got 200")
	}

	// 13. Test POST /api/messages (Social Media / DM message)
	dmPayload := map[string]interface{}{
		"from":    "user",
		"role":    "operator",
		"to":      "agent:worker-1",
		"subject": "Direct Query",
		"content": "Can you check node 3 metrics?",
	}
	data, _ = json.Marshal(dmPayload)
	req = httptest.NewRequest(http.MethodPost, "/api/messages", bytes.NewBuffer(data))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Expected 200 for POST /api/messages, got %d: %s", rec.Code, rec.Body.String())
	}
}
