package deliberation_test

import (
	"os"
	"testing"

	"github.com/tasmirz/gossip/pkg/deliberation"
	"github.com/tasmirz/gossip/pkg/state"
)

func TestDebateLifecycle(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-delib-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := state.NewStore(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	dm := deliberation.NewManager(store)

	// 1. Propose Debate
	deb, err := dm.StartDebate(
		"Adopt FlatBuffers vs Protobuf for Framing",
		"We need zero-copy deserialization for high-throughput messaging.",
		"orch-1",
		"orchestrator",
	)
	if err != nil {
		t.Fatalf("StartDebate failed: %v", err)
	}
	if deb.Status != deliberation.DebateStatusOpen {
		t.Fatalf("Expected status open, got %s", deb.Status)
	}

	// 2. Post Pro Argument
	_, err = dm.PostArgument(deb.ID, "net-1", "network_engineer", deliberation.StancePro, "FlatBuffers eliminates memory copies during packet traversal.")
	if err != nil {
		t.Fatalf("PostArgument pro failed: %v", err)
	}

	// 3. Post Critique Argument
	_, err = dm.PostArgument(deb.ID, "worker-1", "worker", deliberation.StanceCritique, "FlatBuffers schema compilation adds CI build overhead and tooling complexity.")
	if err != nil {
		t.Fatalf("PostArgument critique failed: %v", err)
	}

	// 4. Cast Votes
	_, err = dm.CastVote(deb.ID, "net-1", "network_engineer", "accept", 5, "Benchmarks demonstrate 4x latency reduction.")
	if err != nil {
		t.Fatalf("CastVote 1 failed: %v", err)
	}

	_, err = dm.CastVote(deb.ID, "math-1", "math_expert", "accept", 4, "Zero-copy aligns with memory complexity budget.")
	if err != nil {
		t.Fatalf("CastVote 2 failed: %v", err)
	}

	_, err = dm.CastVote(deb.ID, "worker-1", "worker", "reject", 3, "Maintenance cost is non-trivial.")
	if err != nil {
		t.Fatalf("CastVote 3 failed: %v", err)
	}

	// 5. Conclude Debate
	res, err := dm.ConcludeDebate(deb.ID, "critic-1", "Consensus accepts FlatBuffers due to strict latency requirements.", "ADR-004: FlatBuffers Adopted")
	if err != nil {
		t.Fatalf("ConcludeDebate failed: %v", err)
	}

	if res.Outcome != "accepted" {
		t.Fatalf("Expected outcome accepted, got %s", res.Outcome)
	}

	debates, err := dm.ListDebates(deliberation.DebateStatusResolved)
	if err != nil || len(debates) != 1 {
		t.Fatalf("Expected 1 resolved debate, got %d", len(debates))
	}
}

func TestPeerReviewLifecycle(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-rev-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := state.NewStore(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	dm := deliberation.NewManager(store)

	// 1. Request Review
	rev, err := dm.RequestReview("task", "task-5", "worker-1", "qa-1")
	if err != nil {
		t.Fatalf("RequestReview failed: %v", err)
	}
	if rev.Status != deliberation.ReviewStatusPending {
		t.Fatalf("Expected pending review, got %s", rev.Status)
	}

	// 2. Submit Review with Findings
	findings := []deliberation.Finding{
		{
			ID:             "f-1",
			Severity:       "major",
			Category:       "concurrency",
			Location:       "pkg/state/state.go:102",
			Description:    "Lock was acquired non-reentrantly causing deadlock during board render.",
			Recommendation: "Use unlocked helper methods when calling within lock boundary.",
		},
	}

	completedRev, err := dm.SubmitReview(rev.ID, "qa-1", deliberation.ReviewStatusChangesRequested, "Deadlock scenario must be addressed before merge.", findings)
	if err != nil {
		t.Fatalf("SubmitReview failed: %v", err)
	}

	if completedRev.Status != deliberation.ReviewStatusChangesRequested {
		t.Fatalf("Review status mismatch: %s", completedRev.Status)
	}
	if len(completedRev.Findings) != 1 {
		t.Fatalf("Expected 1 finding, got %d", len(completedRev.Findings))
	}
}

func TestRedTeamProbeLifecycle(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-probe-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := state.NewStore(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	dm := deliberation.NewManager(store)

	// Log Red Team Attack Probe
	probe, err := dm.LogRedTeamProbe(
		"pkg/messages/messages.go:ReadInbox",
		"concurrency_race",
		"red-team-1",
		"Concurrent reads and writes to messages.jsonl cause partial JSON unmarshal panic.",
		"go test -race -run TestConcurrentMessages",
		deliberation.ProbeStatusVulnerable,
		"task-9",
	)
	if err != nil {
		t.Fatalf("LogRedTeamProbe failed: %v", err)
	}

	if probe.ThreatCategory != "concurrency_race" || probe.Status != deliberation.ProbeStatusVulnerable {
		t.Fatalf("Probe state mismatch: %+v", probe)
	}

	probes, err := dm.ListProbes(deliberation.ProbeStatusVulnerable)
	if err != nil || len(probes) != 1 {
		t.Fatalf("Expected 1 vulnerable probe, got %d", len(probes))
	}
}
