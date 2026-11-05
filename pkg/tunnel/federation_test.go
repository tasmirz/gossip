package tunnel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/tasmirz/gossip/pkg/state"
)

func TestLocalNodeIdentity(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-node-id-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := state.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	node := GetLocalNodeIdentity(store)
	if node.NodeID == "" || node.WorktreeRoot == "" {
		t.Errorf("invalid node identity: %+v", node)
	}
}

func TestExecuteLocalFederatedQuery(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-query-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := state.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	// 1. Status query
	res, err := ExecuteLocalFederatedQuery(store, FederatedQueryRequest{
		QueryID:    "q1",
		SourceNode: "peer-node-1",
		QueryType:  "status",
	})
	if err != nil {
		t.Fatalf("ExecuteLocalFederatedQuery status failed: %v", err)
	}
	if res.QueryType != "status" || res.TargetNodeID == "" {
		t.Errorf("unexpected query result: %+v", res)
	}

	// 2. Tasks query
	resTasks, err := ExecuteLocalFederatedQuery(store, FederatedQueryRequest{
		QueryID:    "q2",
		SourceNode: "peer-node-1",
		QueryType:  "tasks",
	})
	if err != nil {
		t.Fatalf("ExecuteLocalFederatedQuery tasks failed: %v", err)
	}
	if resTasks.QueryType != "tasks" {
		t.Errorf("unexpected tasks query result: %+v", resTasks)
	}
}

func TestQueryRemoteNodeHTTP(t *testing.T) {
	// Setup mock remote node
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "testuser" || pass != "testpass" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		resp := FederatedQueryResult{
			QueryID:        "test-q",
			TargetNodeID:   "remote-server-node",
			RemoteWorktree: "/var/projects/backend",
			QueryType:      "tasks",
			Data:           []string{"task-1", "task-2"},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	// 1. Successful authenticated query
	res, err := QueryRemoteNode(ts.URL, "testuser", "testpass", FederatedQueryRequest{
		QueryID:    "test-q",
		SourceNode: "local-client",
		QueryType:  "tasks",
	})
	if err != nil {
		t.Fatalf("QueryRemoteNode failed: %v", err)
	}
	if res.TargetNodeID != "remote-server-node" || res.RemoteWorktree != "/var/projects/backend" {
		t.Errorf("unexpected remote query response: %+v", res)
	}

	// 2. Unauthorized query
	_, err = QueryRemoteNode(ts.URL, "wronguser", "wrongpass", FederatedQueryRequest{
		QueryID:   "test-q",
		QueryType: "tasks",
	})
	if err == nil {
		t.Error("expected unauthorized error with bad credentials")
	}
}
