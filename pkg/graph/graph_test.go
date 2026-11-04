package graph_test

import (
	"os"
	"strings"
	"testing"

	"github.com/tasmirz/gossip/pkg/graph"
	"github.com/tasmirz/gossip/pkg/state"
)

func TestKnowledgeGraphLifecycle(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-graph-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := state.NewStore(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	gm := graph.NewManager(store)

	// Add Nodes
	n1, err := gm.AddNode(graph.Node{
		ID:        "concept:framing",
		Type:      graph.TypeConcept,
		Label:     "Length Prefixed Binary Framing",
		Summary:   "Frames have 4-byte big endian length prefix followed by payload and CRC32.",
		Tags:      []string{"network", "framing", "protocol"},
		CreatedBy: "net_1",
	})
	if err != nil {
		t.Fatalf("AddNode 1 failed: %v", err)
	}

	n2, err := gm.AddNode(graph.Node{
		ID:        "proof:crc32_polynomial",
		Type:      graph.TypeProof,
		Label:     "CRC-32-Castagnoli Polynomial Optimality",
		Summary:   "Castagnoli (0x82F63B78) provides Hamming distance of 6 for payloads up to 4096 bytes.",
		Tags:      []string{"math", "checksum", "crc32"},
		CreatedBy: "math_1",
	})
	if err != nil {
		t.Fatalf("AddNode 2 failed: %v", err)
	}

	n3, err := gm.AddNode(graph.Node{
		ID:        "file:pkg/protocol/framing.go",
		Type:      graph.TypeFile,
		Label:     "Framing Code Implementation",
		Summary:   "Go implementation of framing decoder and encoder.",
		Tags:      []string{"code", "go"},
		CreatedBy: "worker_1",
	})
	if err != nil {
		t.Fatalf("AddNode 3 failed: %v", err)
	}

	// Add Edges
	// n2 verifies n1
	_, err = gm.AddEdge(graph.Edge{
		Source:     n2.ID,
		Target:     n1.ID,
		Relation:   "verifies",
		Provenance: graph.ProvenanceInferred,
		Summary:    "Math specialist verified error detection capability",
		CreatedBy:  "math_1",
	})
	if err != nil {
		t.Fatalf("AddEdge 1 failed: %v", err)
	}

	// n3 implements n1
	_, err = gm.AddEdge(graph.Edge{
		Source:     n3.ID,
		Target:     n1.ID,
		Relation:   "implements",
		Provenance: graph.ProvenanceExtracted,
		Summary:    "Direct code implementation of length-prefixed protocol",
		CreatedBy:  "worker_1",
	})
	if err != nil {
		t.Fatalf("AddEdge 2 failed: %v", err)
	}

	// Test Query
	results, err := gm.Query("Castagnoli CRC32", "", "", 10)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("Expected query results, got 0")
	}
	if results[0].Node.ID != n2.ID {
		t.Fatalf("Expected top result to be %s, got %s", n2.ID, results[0].Node.ID)
	}

	// Test Tag Filter
	taggedResults, err := gm.Query("", "", "math", 10)
	if err != nil {
		t.Fatalf("Query tag failed: %v", err)
	}
	if len(taggedResults) != 1 || taggedResults[0].Node.ID != n2.ID {
		t.Fatalf("Tag filter mismatch: %+v", taggedResults)
	}

	// Test Neighborhood around n1 (center of both connections)
	nh, err := gm.GetNeighborhood(n1.ID, 1)
	if err != nil {
		t.Fatalf("GetNeighborhood failed: %v", err)
	}
	if len(nh.Nodes) != 3 {
		t.Fatalf("Expected 3 nodes in 1-hop neighborhood of n1, got %d", len(nh.Nodes))
	}
	if len(nh.Edges) != 2 {
		t.Fatalf("Expected 2 edges in 1-hop neighborhood, got %d", len(nh.Edges))
	}

	// Test FindPath between n3 (implementation) and n2 (proof) via n1 (concept)
	path, edges, err := gm.FindPath(n3.ID, n2.ID)
	if err != nil {
		t.Fatalf("FindPath failed: %v", err)
	}
	if len(path) != 3 {
		t.Fatalf("Expected path of length 3 (n3 -> n1 -> n2), got %v", path)
	}
	if len(edges) != 2 {
		t.Fatalf("Expected 2 edges traversed, got %d", len(edges))
	}

	// Test Report Generation
	rep, err := gm.GenerateReport()
	if err != nil {
		t.Fatalf("GenerateReport failed: %v", err)
	}
	if !strings.Contains(rep, "Gossip Shared Knowledge Graph Report") {
		t.Fatalf("Report header missing")
	}
	if !strings.Contains(rep, "EXTRACTED") || !strings.Contains(rep, "INFERRED") {
		t.Fatalf("Report missing provenance breakdown")
	}
	if !strings.Contains(rep, "CRC-32-Castagnoli Polynomial Optimality") {
		t.Fatalf("Report missing proof section")
	}

	// Verify GRAPH_REPORT.md was written to disk
	repBytes, err := store.ReadFile("GRAPH_REPORT.md")
	if err != nil || len(repBytes) == 0 {
		t.Fatalf("GRAPH_REPORT.md not written to disk: %v", err)
	}
}
