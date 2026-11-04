package graph_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tasmirz/gossip/pkg/graph"
	"github.com/tasmirz/gossip/pkg/state"
)

func TestIndexCodebase(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-index-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := state.NewStore(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	// Create dummy Go package inside tmpDir
	pkgDir := filepath.Join(tmpDir, "pkg", "sample")
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatal(err)
	}

	sampleFile := filepath.Join(pkgDir, "sample.go")
	sampleCode := `package sample

// Engine performs high speed calculations.
type Engine struct {
	Count int
}

// Compute processes an input value.
func Compute(val int) int {
	return val * 2
}
`
	if err := os.WriteFile(sampleFile, []byte(sampleCode), 0644); err != nil {
		t.Fatal(err)
	}

	mgr := graph.NewManager(store)
	summary, err := mgr.IndexCodebase(tmpDir)
	if err != nil {
		t.Fatalf("IndexCodebase failed: %v", err)
	}

	if summary.FilesScanned == 0 {
		t.Fatalf("Expected files scanned > 0, got 0")
	}
	if summary.PackagesFound == 0 {
		t.Fatalf("Expected packages found > 0, got 0")
	}
	if summary.SymbolsExtracted == 0 {
		t.Fatalf("Expected symbols extracted > 0, got 0")
	}

	// Verify symbol node exists in graph
	g, err := mgr.LoadGraph()
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := g.Nodes["symbol:sample.Engine"]; !ok {
		t.Fatalf("Expected symbol:sample.Engine node in graph")
	}
	if _, ok := g.Nodes["decision:adr-005"]; !ok {
		t.Fatalf("Expected decision:adr-005 node in graph")
	}
	if _, ok := g.Nodes["invariant:cwe22-path-traversal-defense"]; !ok {
		t.Fatalf("Expected invariant:cwe22-path-traversal-defense node in graph")
	}

	// Check GRAPH_REPORT.md was generated
	reportPath := filepath.Join(store.Dir(), "GRAPH_REPORT.md")
	if _, err := os.Stat(reportPath); os.IsNotExist(err) {
		t.Fatalf("Expected GRAPH_REPORT.md to be generated")
	}
}
