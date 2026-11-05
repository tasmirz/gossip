package tunnel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tasmirz/gossip/pkg/graph"
	"github.com/tasmirz/gossip/pkg/state"
)

// FederatedQueryRequest represents a cross-node query sent across the tunnel.
type FederatedQueryRequest struct {
	QueryID     string `json:"query_id"`
	SourceNode  string `json:"source_node"`
	QueryType   string `json:"query_type"` // "graph", "tasks", "status", "repomap"
	QueryText   string `json:"query_text"`
	Depth       int    `json:"depth,omitempty"`
	AuthUser    string `json:"auth_user,omitempty"`
	AuthPass    string `json:"auth_pass,omitempty"`
}

// FederatedQueryResult holds the response from a remote node's independent worktree.
type FederatedQueryResult struct {
	QueryID          string      `json:"query_id"`
	TargetNodeID     string      `json:"target_node_id"`
	RemoteWorktree   string      `json:"remote_worktree"`
	QueryType        string      `json:"query_type"`
	Timestamp        time.Time   `json:"timestamp"`
	Data             interface{} `json:"data"`
	DisclosedPeers   int         `json:"disclosed_peers"`
	ExecutionTimeMs  int64       `json:"execution_time_ms"`
}

// NodeIdentity represents the local node in the federated multi-worktree mesh.
type NodeIdentity struct {
	NodeID       string   `json:"node_id"`
	WorktreeRoot string   `json:"worktree_root"`
	Endpoint     string   `json:"endpoint"`
	Role         string   `json:"role"`
	Capabilities []string `json:"capabilities"`
}

// GetLocalNodeIdentity detects or constructs this node's identity.
func GetLocalNodeIdentity(store *state.Store) NodeIdentity {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "local-node"
	}
	cwd, _ := os.Getwd()

	nodeID := fmt.Sprintf("node-%s", hostname)
	endpoint := "local://ipc"

	if store != nil {
		if tState, err := LoadTunnelState(store.Dir()); err == nil && tState.Active {
			if tState.RemoteEndpoint != "" {
				endpoint = tState.RemoteEndpoint
			}
		}
	}

	return NodeIdentity{
		NodeID:       nodeID,
		WorktreeRoot: cwd,
		Endpoint:     endpoint,
		Capabilities: []string{"graph", "tasks", "moe", "vault"},
	}
}

// ExecuteLocalFederatedQuery processes an incoming federated query against local subsystem state.
func ExecuteLocalFederatedQuery(store *state.Store, req FederatedQueryRequest) (*FederatedQueryResult, error) {
	start := time.Now()
	node := GetLocalNodeIdentity(store)

	result := &FederatedQueryResult{
		QueryID:        req.QueryID,
		TargetNodeID:   node.NodeID,
		RemoteWorktree: node.WorktreeRoot,
		QueryType:      req.QueryType,
		Timestamp:      time.Now(),
	}

	switch req.QueryType {
	case "graph":
		// Query local graph index
		gm := graph.NewManager(store)
		neighborhood, err := gm.GetNeighborhood(req.QueryText, 2)
		if err != nil {
			return nil, fmt.Errorf("local graph query failed: %w", err)
		}
		result.Data = map[string]interface{}{
			"query":        req.QueryText,
			"neighborhood": neighborhood,
		}

	case "repomap":
		gm := graph.NewManager(store)
		repoMap, err := gm.GenerateRepoMap(4000)
		if err != nil {
			return nil, fmt.Errorf("local repomap failed: %w", err)
		}
		result.Data = map[string]interface{}{
			"repomap":  repoMap,
			"worktree": node.WorktreeRoot,
		}

	case "tasks":
		tasksPath := filepath.Join(store.Dir(), "tasks.json")
		data, err := os.ReadFile(tasksPath)
		if err != nil {
			if os.IsNotExist(err) {
				result.Data = map[string]interface{}{"tasks": []interface{}{}}
			} else {
				return nil, fmt.Errorf("local tasks query failed: %w", err)
			}
		} else {
			var raw map[string]interface{}
			if err := json.Unmarshal(data, &raw); err != nil {
				return nil, fmt.Errorf("corrupted tasks.json: %w", err)
			}
			result.Data = raw["tasks"]
		}

	case "status":
		disc := ComputeDisclosure(store)
		agents, _ := store.ListAgents()
		result.Data = map[string]interface{}{
			"node_id":           node.NodeID,
			"worktree":          node.WorktreeRoot,
			"active_agents":     len(agents),
			"remote_disclosure": disc,
		}

	default:
		return nil, fmt.Errorf("unsupported federated query type: %s", req.QueryType)
	}

	disc := ComputeDisclosure(store)
	result.DisclosedPeers = disc.TotalRemoteCount
	result.ExecutionTimeMs = time.Since(start).Milliseconds()

	return result, nil
}

// QueryRemoteNode sends a federated query across the tunnel to a remote node's /api/mesh/query endpoint.
func QueryRemoteNode(remoteEndpoint, authUser, authPass string, req FederatedQueryRequest) (*FederatedQueryResult, error) {
	if !strings.HasPrefix(remoteEndpoint, "http://") && !strings.HasPrefix(remoteEndpoint, "https://") {
		return nil, fmt.Errorf("remote endpoint must be http or https URL: %s", remoteEndpoint)
	}

	url := fmt.Sprintf("%s/api/mesh/query", strings.TrimRight(remoteEndpoint, "/"))
	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal query request: %w", err)
	}

	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if authUser != "" || authPass != "" {
		httpReq.SetBasicAuth(authUser, authPass)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("query to remote node %s failed: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("remote node responded with HTTP %d: %s", resp.StatusCode, string(respBytes))
	}

	var qResult FederatedQueryResult
	if err := json.NewDecoder(resp.Body).Decode(&qResult); err != nil {
		return nil, fmt.Errorf("failed to decode remote response: %w", err)
	}

	return &qResult, nil
}
