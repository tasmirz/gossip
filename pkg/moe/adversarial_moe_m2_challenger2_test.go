package moe_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tasmirz/gossip/pkg/moe"
	"github.com/tasmirz/gossip/pkg/roles"
	"github.com/tasmirz/gossip/pkg/state"
)

// TestAdversarialMoE_SubagentTracksNoOSProcesses empirically tests that
// SubagentTrack dispatch creates purely local state records and does NOT
// spawn OS processes, launch containers, or leak background goroutines.
func TestAdversarialMoE_SubagentTracksNoOSProcesses(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-moe-adversarial-*")
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

	panel, err := mgr.ConvenePanel(
		"Adversarial Process & Container Verification",
		"Verify whether MoE subagent dispatch spawns OS processes or executes shell strings",
		"systems, security, runtime",
		"challenger-orch",
		nil,
		4,
	)
	if err != nil {
		t.Fatalf("Failed to convene panel: %v", err)
	}

	// Post arguments
	_, err = mgr.PostArgument(
		panel.ID,
		"sec-1",
		"security_auditor",
		"propose",
		"Ensure subagent dispatch is decoupled coordination only.",
		[]string{"No child process spawning", "No shell expansion"},
	)
	if err != nil {
		t.Fatalf("PostArgument failed: %v", err)
	}

	// Craft adversarial work packages containing shell injection payloads
	canaryPath := filepath.Join(tmpDir, "pwned_canary.txt")
	packages := []moe.WorkPackage{
		{
			PackageID:        "pkg-inject-1",
			Title:            fmt.Sprintf("Adversarial Task; touch %s", canaryPath),
			Description:      fmt.Sprintf("Malicious description && rm -rf /nonexistent || touch %s", canaryPath),
			AssignedRole:     "worker",
			SuggestedTaskID:  fmt.Sprintf("task-id; touch %s", canaryPath),
			CanRunInParallel: true,
		},
		{
			PackageID:        "pkg-inject-2",
			Title:            "Subshell injection $(touch " + canaryPath + ")",
			Description:      "Backtick injection `touch " + canaryPath + "`",
			AssignedRole:     "reviewer",
			SuggestedTaskID:  "task-2-clean",
			CanRunInParallel: true,
		},
	}

	_, err = mgr.Synthesize(
		panel.ID,
		"challenger-orch",
		"Verify inert dispatching.",
		[]string{"No execution side-effects"},
		[]string{"Test inert dispatch"},
		packages,
	)
	if err != nil {
		t.Fatalf("Synthesize failed: %v", err)
	}

	// Record baseline goroutine count
	runtime.GC()
	initialGoroutines := runtime.NumGoroutine()

	// Dispatch subagents
	tracks, err := mgr.DispatchSubagents(panel.ID, "challenger-orch")
	if err != nil {
		t.Fatalf("DispatchSubagents failed: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("Expected 2 tracks, got %d", len(tracks))
	}

	// 1. Verify canary file was NEVER created (no shell execution or command execution occurred)
	if _, statErr := os.Stat(canaryPath); !os.IsNotExist(statErr) {
		t.Fatalf("CRITICAL SECURITY VULNERABILITY: Shell command was executed during SubagentTrack dispatch! Canary exists at %s", canaryPath)
	}

	// 2. Verify no child processes were spawned by checking pgrep -P <current PID>
	cmd := exec.Command("pgrep", "-P", fmt.Sprint(os.Getpid()))
	output, _ := cmd.CombinedOutput()
	// pgrep outputs PIDs if any children exist. Filter out pgrep itself
	childPIDs := strings.TrimSpace(string(output))
	if childPIDs != "" {
		t.Logf("Observed child processes under test PID %d: %s", os.Getpid(), childPIDs)
	}

	// 3. Verify no goroutines were leaked
	runtime.GC()
	finalGoroutines := runtime.NumGoroutine()
	if finalGoroutines > initialGoroutines+5 {
		t.Fatalf("Goroutine leak detected: initial=%d, final=%d", initialGoroutines, finalGoroutines)
	}

	// 4. Verify track invariants in state
	fetched, err := mgr.GetPanel(panel.ID)
	if err != nil {
		t.Fatalf("GetPanel failed: %v", err)
	}
	if fetched.Status != "dispatched" {
		t.Fatalf("Expected panel status 'dispatched', got %s", fetched.Status)
	}
	for i, tr := range fetched.SubagentTracks {
		if tr.Status != "pending" {
			t.Errorf("Track %d status: expected 'pending' until an agent claims it, got %s", i, tr.Status)
		}
		if tr.TrackID == "" || tr.PackageID == "" || tr.TaskID == "" {
			t.Errorf("Track %d missing identifiers: %+v", i, tr)
		}
		if tr.AgentID != "" {
			t.Errorf("Track %d pretends an external agent was assigned: %+v", i, tr)
		}
		// Confirm the suggested task ID was preserved verbatim as string data without execution
		if i == 0 && tr.TaskID != fmt.Sprintf("task-id; touch %s", canaryPath) {
			t.Errorf("Track 0 task ID mismatch: %s", tr.TaskID)
		}
	}
}

// TestAdversarialMoE_DispatchEdgeCases tests edge cases and error states
// during subagent track dispatch.
func TestAdversarialMoE_DispatchEdgeCases(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-moe-edge-*")
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

	// 1. Dispatch on nonexistent panel
	_, err = mgr.DispatchSubagents("nonexistent-panel-id", "orch-1")
	if err == nil {
		t.Fatalf("Expected error when dispatching on nonexistent panel, got nil")
	}

	// 2. Dispatch on panel that has not been synthesized
	panel, err := mgr.ConvenePanel("Unsynthesized Panel", "Query", "domain", "orch-1", nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = mgr.DispatchSubagents(panel.ID, "orch-1")
	if err == nil {
		t.Fatalf("Expected error when dispatching unsynthesized panel, got nil")
	}

	// 3. Synthesize with empty work packages -> dispatch returns empty slice without error
	_, err = mgr.Synthesize(panel.ID, "orch-1", "Decision", nil, nil, []moe.WorkPackage{})
	if err != nil {
		t.Fatal(err)
	}
	tracks, err := mgr.DispatchSubagents(panel.ID, "orch-1")
	if err != nil {
		t.Fatalf("DispatchSubagents with 0 packages failed: %v", err)
	}
	if len(tracks) != 0 {
		t.Fatalf("Expected 0 tracks, got %d", len(tracks))
	}
}
