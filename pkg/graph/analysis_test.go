package graph_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tasmirz/gossip/pkg/graph"
	"github.com/tasmirz/gossip/pkg/state"
)

func TestAnalysisPageRankAndImpact(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "gossip-analysis-test")
	_ = os.RemoveAll(tmpDir)
	defer os.RemoveAll(tmpDir)

	store, err := state.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	mgr := graph.NewManager(store)

	// Create nodes: Core Utility (C), Service A (A), Service B (B), Gateway (G)
	// G -> A -> C
	// G -> B -> C
	nodes := []graph.Node{
		{ID: "util:core", Type: graph.TypeSymbol, Label: "CoreUtility", Summary: "Foundational atomic math and data utils"},
		{ID: "svc:a", Type: graph.TypeSymbol, Label: "ServiceA", Summary: "User auth and profile management"},
		{ID: "svc:b", Type: graph.TypeSymbol, Label: "ServiceB", Summary: "Payment and billing service"},
		{ID: "gateway", Type: graph.TypeSymbol, Label: "APIGateway", Summary: "Public ingress HTTP proxy"},
	}

	for _, n := range nodes {
		if _, err := mgr.AddNode(n); err != nil {
			t.Fatalf("failed to add node %s: %v", n.ID, err)
		}
	}

	edges := []graph.Edge{
		{Source: "svc:a", Target: "util:core", Relation: "imports"},
		{Source: "svc:b", Target: "util:core", Relation: "calls"},
		{Source: "gateway", Target: "svc:a", Relation: "calls"},
		{Source: "gateway", Target: "svc:b", Relation: "calls"},
	}

	for _, e := range edges {
		if _, err := mgr.AddEdge(e); err != nil {
			t.Fatalf("failed to add edge: %v", err)
		}
	}

	// 1. Test PageRank
	scores, err := mgr.ComputePageRank(0.85, 100, 1e-5)
	if err != nil {
		t.Fatalf("failed to compute PageRank: %v", err)
	}
	if len(scores) != 4 {
		t.Fatalf("expected 4 node scores, got %d", len(scores))
	}

	// util:core has the most incoming edges, so it should rank highest or second highest
	if scores[0].NodeID != "util:core" {
		t.Logf("Top node is %s (score: %.4f)", scores[0].NodeID, scores[0].PageRank)
	}

	// 2. Test Impact Analysis (Blast Radius of changing util:core)
	impact, err := mgr.AffectedNodes("util:core", 3)
	if err != nil {
		t.Fatalf("failed to run impact analysis on util:core: %v", err)
	}

	if impact.TotalAffectedCount != 3 {
		t.Fatalf("expected 3 affected nodes (svc:a, svc:b, gateway), got %d", impact.TotalAffectedCount)
	}
	if len(impact.DirectHits) != 2 {
		t.Fatalf("expected 2 direct hits (svc:a, svc:b), got %d", len(impact.DirectHits))
	}
	if len(impact.IndirectHits) != 1 {
		t.Fatalf("expected 1 indirect hit (gateway), got %d", len(impact.IndirectHits))
	}
	if impact.BlastRadiusScore <= 0 {
		t.Fatalf("expected positive blast radius score, got %.2f", impact.BlastRadiusScore)
	}
	if len(impact.RecommendedVerifications) == 0 {
		t.Fatalf("expected recommended verifications for high impact change")
	}

	// 3. Test Cluster Detection
	clusters, err := mgr.DetectClusters()
	if err != nil {
		t.Fatalf("failed to detect clusters: %v", err)
	}
	if len(clusters) != 1 {
		t.Fatalf("expected 1 connected cluster, got %d", len(clusters))
	}
	if clusters[0].Size != 4 {
		t.Fatalf("expected cluster size 4, got %d", clusters[0].Size)
	}

	// 4. Test Repo Map Generation
	repoMap, err := mgr.GenerateRepoMap(500)
	if err != nil {
		t.Fatalf("failed to generate repo map: %v", err)
	}
	if repoMap == "" {
		t.Fatalf("expected non-empty repo map")
	}
	t.Logf("Generated Repo-Map:\n%s", repoMap)
}
