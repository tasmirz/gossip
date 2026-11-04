package deliberation_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tasmirz/gossip/pkg/deliberation"
	"github.com/tasmirz/gossip/pkg/state"
)

func TestMADRankedVotingAndConvergence(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "gossip-mad-test")
	_ = os.RemoveAll(tmpDir)
	defer os.RemoveAll(tmpDir)

	store, err := state.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	mgr := deliberation.NewManager(store)

	// 1. Start Debate
	deb, err := mgr.StartDebate("Transport Protocol", "Choose communication protocol for real-time mesh", "agent:arch", "systems_architect")
	if err != nil {
		t.Fatalf("failed to start debate: %v", err)
	}

	// Add arguments across 2 rounds to test convergence
	// Round 1
	_, _ = mgr.PostArgument(deb.ID, "agent:arch", "systems_architect", deliberation.StancePro, "Option B (WebSocket) provides bidirectional framing with lower latency.")
	_, _ = mgr.PostArgument(deb.ID, "agent:net", "network_engineer", deliberation.StancePro, "Agreed, WebSocket avoids HTTP/1.1 polling overhead.")
	_, _ = mgr.PostArgument(deb.ID, "agent:qa", "qa_engineer", deliberation.StanceCritique, "WebSocket connection drop handling requires reconnect state machines.")

	// Advance round
	if err := mgr.AdvanceRound(deb.ID); err != nil {
		t.Fatalf("failed to advance round: %v", err)
	}

	// Round 2
	_, _ = mgr.PostArgument(deb.ID, "agent:arch", "systems_architect", deliberation.StancePro, "We can add exponential backoff reconnect logic in pkg/transport.")
	_, _ = mgr.PostArgument(deb.ID, "agent:net", "network_engineer", deliberation.StancePro, "Confirmed, network stability tests show backoff solves connection flapping.")
	_, _ = mgr.PostArgument(deb.ID, "agent:qa", "qa_engineer", deliberation.StancePro, "Acceptable, with automated reconnect integration tests.")

	// Check Convergence
	conv, err := mgr.CheckConvergence(deb.ID)
	if err != nil {
		t.Fatalf("failed to check convergence: %v", err)
	}

	if !conv.HasConverged {
		t.Fatalf("expected debate to converge in round 2, got false (agreement: %.2f)", conv.AgreementRatio)
	}
	if conv.DominantStance != deliberation.StancePro {
		t.Fatalf("expected dominant stance 'pro', got %s", conv.DominantStance)
	}
	if conv.Recommendation != "proceed_to_vote" {
		t.Fatalf("expected recommendation 'proceed_to_vote', got %s", conv.Recommendation)
	}

	// 2. Cast Ranked Votes (Borda Count)
	// Candidates: Option A (gRPC), Option B (WebSocket), Option C (SSE)
	err = mgr.CastRankedVote(deb.ID, "agent:arch", "systems_architect", []string{"Option B (WebSocket)", "Option A (gRPC)", "Option C (SSE)"}, 5, "Best latency and browser support")
	if err != nil {
		t.Fatalf("failed to cast ranked vote 1: %v", err)
	}

	err = mgr.CastRankedVote(deb.ID, "agent:net", "network_engineer", []string{"Option B (WebSocket)", "Option C (SSE)", "Option A (gRPC)"}, 4, "Clean full duplex multiplexing")
	if err != nil {
		t.Fatalf("failed to cast ranked vote 2: %v", err)
	}

	err = mgr.CastRankedVote(deb.ID, "agent:qa", "qa_engineer", []string{"Option A (gRPC)", "Option B (WebSocket)", "Option C (SSE)"}, 3, "Strict protobuf schema enforcement")
	if err != nil {
		t.Fatalf("failed to cast ranked vote 3: %v", err)
	}

	// Tally
	tally, err := mgr.TallyRankedVotes(deb.ID)
	if err != nil {
		t.Fatalf("failed to tally ranked votes: %v", err)
	}

	if tally.TotalVoters != 3 {
		t.Fatalf("expected 3 voters, got %d", tally.TotalVoters)
	}
	if tally.Winner != "Option B (WebSocket)" {
		t.Fatalf("expected Option B (WebSocket) to win, got %s", tally.Winner)
	}
	t.Logf("Tally results:\n%s", tally.Details)

	// 3. Test STRIDE Probes & Audit Report
	probes := deliberation.GenerateSTRIDEProbes("pkg/transport/websocket", "WebSocket binary framing transport", "agent:red_team")
	if len(probes) != 6 {
		t.Fatalf("expected 6 STRIDE probes, got %d", len(probes))
	}

	if err := mgr.LogBatchProbes(probes); err != nil {
		t.Fatalf("failed to log batch probes: %v", err)
	}

	auditReport, err := mgr.GenerateAuditReport()
	if err != nil {
		t.Fatalf("failed to generate audit report: %v", err)
	}
	if !strings.Contains(auditReport, "STRIDE") {
		t.Fatalf("expected audit report to contain STRIDE breakdown")
	}
	t.Logf("Generated Audit Report excerpt:\n%s", auditReport[:300])
}
