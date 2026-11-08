package moe_test

import (
	"os"
	"testing"

	"github.com/tasmirz/gossip/pkg/moe"
	"github.com/tasmirz/gossip/pkg/roles"
	"github.com/tasmirz/gossip/pkg/state"
)

func TestMoEPanelLifecycleAndParallelDispatch(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-moe-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := state.NewStore(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	roleProv := roles.GetDefaultService()
	mgr := moe.NewManager(store, roleProv)

	// 1. Convene Panel with automatic gating
	panel, err := mgr.ConvenePanel(
		"Protocol Binary Framing vs JSON",
		"Evaluate network latency, CRC checksums, and memory allocation races for our event pipeline",
		"networking, crypto, concurrency",
		"orchestrator-1",
		nil,
		4,
	)
	if err != nil {
		t.Fatalf("Failed to convene panel: %v", err)
	}
	if len(panel.SelectedExperts) == 0 {
		t.Fatalf("Expected gating function to select experts")
	}

	// 2. Post Expert Arguments
	_, err = mgr.PostArgument(
		panel.ID,
		"neteng-1",
		"network_engineer",
		"propose",
		"Binary framing with 4-byte length prefix reduces network overhead by 65% compared to JSON framing.",
		[]string{"Lower serialization cost", "Zero delimiter escaping"},
	)
	if err != nil {
		t.Fatalf("PostArgument failed: %v", err)
	}

	_, err = mgr.PostArgument(
		panel.ID,
		"sysarch-1",
		"systems_architect",
		"caution",
		"Binary framing requires strict buffer length bounds to prevent buffer overruns under high concurrency.",
		[]string{"Enforce 1MB payload ceiling", "Use atomic read operations"},
	)
	if err != nil {
		t.Fatalf("PostArgument failed: %v", err)
	}

	// 3. Synthesize Decision with Parallel Work Packages
	packages := []moe.WorkPackage{
		{
			PackageID:        "pkg-1",
			Title:            "Implement Binary Framing Encoder",
			Description:      "Author framing encoder with CRC32 checksum",
			AssignedRole:     "worker",
			CanRunInParallel: true,
		},
		{
			PackageID:        "pkg-2",
			Title:            "Fuzz Test Frame Parser",
			Description:      "Author adversarial fuzzer for malformed frame lengths",
			AssignedRole:     "red_team",
			CanRunInParallel: true,
		},
	}

	synthesis, err := mgr.Synthesize(
		panel.ID,
		"orchestrator-1",
		"Adopt length-prefixed binary framing with Castagnoli CRC32 and strict 1MB sanity check.",
		[]string{"Requires custom parser vs standard JSON parser", "Higher engineering complexity for 3x throughput"},
		[]string{"Implement encoder", "Fuzz parser in parallel"},
		packages,
	)
	if err != nil {
		t.Fatalf("Synthesize failed: %v", err)
	}
	if len(synthesis.ParallelWorkPackages) != 2 {
		t.Fatalf("Expected 2 work packages, got %d", len(synthesis.ParallelWorkPackages))
	}

	// 4. Dispatch Parallel Subagent Tracks
	tracks, err := mgr.DispatchSubagents(panel.ID, "orchestrator-1")
	if err != nil {
		t.Fatalf("DispatchSubagents failed: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("Expected 2 subagent tracks, got %d", len(tracks))
	}

	// 5. Verify Persistence & Retrieval
	fetched, err := mgr.GetPanel(panel.ID)
	if err != nil {
		t.Fatalf("GetPanel failed: %v", err)
	}
	if fetched.Status != "dispatched" {
		t.Fatalf("Expected status 'dispatched', got %s", fetched.Status)
	}
	if len(fetched.SubagentTracks) != 2 {
		t.Fatalf("Expected 2 persisted subagent tracks, got %d", len(fetched.SubagentTracks))
	}

	md := fetched.RenderMarkdown()
	if len(md) == 0 {
		t.Fatalf("RenderMarkdown returned empty string")
	}
}
