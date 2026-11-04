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
	if !bytes.Contains([]byte(body), []byte("GOSSIP MESH // CONTROL PANEL")) {
		t.Fatalf("Expected HTML to contain title")
	}
	if !bytes.Contains([]byte(body), []byte("border-radius: 0 !important")) {
		t.Fatalf("Expected zero border radius style constraint")
	}

	// 2. Test GET /api/status
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
}
