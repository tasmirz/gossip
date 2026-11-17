package coord_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/tasmirz/gossip/pkg/coord"
)

func TestProjectSyncFramesAreBoundedContinuousAndProjectScoped(t *testing.T) {
	s, _, project, owner, worker := setup(t)
	ctx := context.Background()
	create(t, s, project, owner, nil)
	if _, err := s.AddCertificateGrant(ctx, project.ID, owner.Token, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}

	first, err := s.SyncProject(ctx, project.ID, worker.Token, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.Plan == nil || first.Plan.Revision != 1 || len(first.Tasks.Tasks) != 1 || len(first.Events) != 1 || !first.EventsMayContinue || first.NextEventCursor != first.Events[0].Sequence || first.SnapshotSequence <= first.NextEventCursor {
		t.Fatalf("first bounded sync frame: %+v", first)
	}
	second, err := s.SyncProject(ctx, project.ID, worker.Token, first.NextEventCursor, 200)
	if err != nil {
		t.Fatal(err)
	}
	if second.SnapshotSequence != first.SnapshotSequence || second.EventsMayContinue || second.NextEventCursor != second.SnapshotSequence {
		t.Fatalf("sync did not advance to its snapshot watermark: first=%+v second=%+v", first, second)
	}
	for _, event := range append(first.Events, second.Events...) {
		if len(event.Kind) >= len("certificate.") && event.Kind[:len("certificate.")] == "certificate." {
			t.Fatalf("worker sync disclosed owner-only certificate event: %+v", event)
		}
	}
	if _, err = s.SyncProject(ctx, project.ID, owner.Token, -1, 200); err == nil {
		t.Fatal("accepted a negative sync cursor")
	}
	if _, err = s.SyncProject(ctx, project.ID, worker.Token, 0, 201); err == nil {
		t.Fatal("accepted an oversized sync page")
	}
	reset, err := s.SyncProject(ctx, project.ID, worker.Token, first.SnapshotSequence+100, 200)
	if err != nil || !reset.ResetRequired || reset.EventsAfter != 0 || reset.NextEventCursor != reset.SnapshotSequence {
		t.Fatalf("home sequence rollback did not request a cache reset: frame=%+v err=%v", reset, err)
	}
	other, otherOwner, err := s.CreateProject(ctx, "other", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SyncProject(ctx, other.ID, worker.Token, 0, 200); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("cross-project sync was not denied: %v", err)
	}
	if _, err = s.SyncProject(ctx, other.ID, otherOwner.Token, 0, 200); err != nil {
		t.Fatalf("authorized project snapshot: %v", err)
	}
}

func TestSyncInboxReturnsBoundedVisiblePreviewsAndDeltas(t *testing.T) {
	s, _, project, owner, worker := setup(t)
	ctx := context.Background()
	other, err := s.AddPrincipal(ctx, project.ID, owner.Token, "Other worker", "worker")
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("x", 300)
	if _, err = s.SendMessageWithID(ctx, project.ID, owner.Token, "", "", "shared", body, "room-message"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SendMessageWithID(ctx, project.ID, owner.Token, other.PrincipalID, "", "private", "not for worker", "private-message"); err != nil {
		t.Fatal(err)
	}
	first, err := s.SyncInbox(ctx, project.ID, worker.Token, 0, 0, 25)
	if err != nil {
		t.Fatal(err)
	}
	if first.Plan == nil || len(first.InboxMessages) != 1 || first.InboxHasMore {
		t.Fatalf("unexpected inbox snapshot: %+v", first)
	}
	if len([]rune(first.InboxMessages[0].Preview)) != 241 || first.InboxMessages[0].Recipient != "" {
		t.Fatalf("message preview was not bounded or was a private DM: %+v", first.InboxMessages[0])
	}
	second, err := s.SyncInbox(ctx, project.ID, worker.Token, first.NextEventCursor, first.NextInboxCursor, 25)
	if err != nil {
		t.Fatal(err)
	}
	if second.Plan != nil || len(second.Tasks.Tasks) != 0 || len(second.InboxMessages) != 0 {
		t.Fatalf("incremental inbox repeated full snapshot or old messages: %+v", second)
	}
}

func TestReplicaReceiptsBindProjectPrincipalAndCertificate(t *testing.T) {
	s, _, project, owner, worker := setup(t)
	ctx := context.Background()
	firstFingerprint := strings.Repeat("a", 64)
	secondFingerprint := strings.Repeat("b", 64)
	for _, fingerprint := range []string{firstFingerprint, secondFingerprint} {
		if _, err := s.AddCertificateGrant(ctx, project.ID, owner.Token, fingerprint); err != nil {
			t.Fatal(err)
		}
	}
	replicaID := "replica_0123456789abcdef0123456789abcdef"
	frame, err := s.SyncProjectFromReplica(ctx, project.ID, worker.Token, replicaID, firstFingerprint, 0, 20)
	if err != nil {
		t.Fatalf("authorized replica sync: %v", err)
	}
	if frame.HomeInstanceID == "" {
		t.Fatal("replica sync omitted the home identity")
	}
	replicas, err := s.ListProjectReplicas(ctx, project.ID, owner.Token)
	if err != nil || len(replicas) != 1 {
		t.Fatalf("owner replica receipt list: %+v %v", replicas, err)
	}
	receipt := replicas[0]
	if receipt.ReplicaID != replicaID || receipt.PrincipalID != worker.PrincipalID || receipt.CertificateFingerprint != firstFingerprint || receipt.FirstSeenAt <= 0 || receipt.LastSeenAt < receipt.FirstSeenAt || receipt.SnapshotSequence != frame.SnapshotSequence || receipt.LastEventCursor != frame.NextEventCursor {
		t.Fatalf("replica receipt does not match authorized sync: %+v frame=%+v", receipt, frame)
	}
	if _, err = s.ListProjectReplicas(ctx, project.ID, worker.Token); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("non-owner listed replica receipts: %v", err)
	}
	other, err := s.AddPrincipal(ctx, project.ID, owner.Token, "other worker", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SyncProjectFromReplica(ctx, project.ID, other.Token, replicaID, firstFingerprint, 0, 20); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("replica identity was rebound to another principal: %v", err)
	}
	if _, err = s.SyncProjectFromReplica(ctx, project.ID, worker.Token, replicaID, secondFingerprint, 0, 20); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("replica identity was rebound to another certificate: %v", err)
	}
	if _, err = s.SyncProjectFromReplica(ctx, project.ID, worker.Token, "replica_not-hex", firstFingerprint, 0, 20); err == nil {
		t.Fatal("accepted a malformed replica identity")
	}
	if err = s.RevokeCertificateGrant(ctx, project.ID, owner.Token, firstFingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SyncProjectFromReplica(ctx, project.ID, worker.Token, replicaID, firstFingerprint, 0, 20); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("revoked certificate continued syncing replica: %v", err)
	}
}

func TestProjectSyncHomeIdentitySurvivesStoreRestart(t *testing.T) {
	s, path, project, _, worker := setup(t)
	first, err := s.SyncProject(context.Background(), project.ID, worker.Token, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	if first.HomeInstanceID == "" {
		t.Fatal("sync omitted the home identity")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := coord.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	second, err := restarted.SyncProject(context.Background(), project.ID, worker.Token, first.NextEventCursor, 200)
	if err != nil {
		t.Fatal(err)
	}
	if second.HomeInstanceID != first.HomeInstanceID {
		t.Fatalf("home identity changed across restart: before=%q after=%q", first.HomeInstanceID, second.HomeInstanceID)
	}
}

func TestProjectSyncSnapshotMatchesEventWatermarkDuringWrites(t *testing.T) {
	s, _, project, owner, worker := setup(t)
	ctx := context.Background()
	var writers sync.WaitGroup
	writeErr := make(chan error, 1)
	writers.Add(1)
	go func() {
		defer writers.Done()
		for revision := 1; revision <= 20; revision++ {
			if _, err := s.PublishPlan(ctx, project.ID, owner.Token, "revision", revision); err != nil {
				writeErr <- err
				return
			}
		}
	}()
	for i := 0; i < 40; i++ {
		frame, err := s.SyncProject(ctx, project.ID, worker.Token, 0, 200)
		if err != nil {
			t.Fatal(err)
		}
		latestPlanEvent := 0
		for _, event := range frame.Events {
			if event.Kind != "plan.published" {
				continue
			}
			var payload struct {
				Revision int `json:"revision"`
			}
			if err = json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			latestPlanEvent = payload.Revision
		}
		if frame.Plan == nil || frame.Plan.Revision != latestPlanEvent {
			t.Fatalf("mixed-time plan and event state: plan=%+v latest plan event=%d snapshot=%d", frame.Plan, latestPlanEvent, frame.SnapshotSequence)
		}
	}
	writers.Wait()
	select {
	case err := <-writeErr:
		t.Fatal(err)
	default:
	}
}

func TestRecommendedPollIntervalBackoffAndReset(t *testing.T) {
	expected := []int{30, 30, 60, 120, 240, 480, 900, 900}
	idle := 0
	for step, want := range expected {
		var got int
		idle, got = coord.RecommendedPollInterval(idle, false)
		if got != want {
			t.Fatalf("step %d: got %ds (idle=%d), want %ds", step+1, got, idle, want)
		}
	}
	if idle, got := coord.RecommendedPollInterval(idle, true); idle != 0 || got != 30 {
		t.Fatalf("activity reset failed: idle=%d got=%d", idle, got)
	}

	// Verify gossip_sync(operation="inbox") dynamically backs off on idle polls
	// even when 1 previous anchor message is returned via last_read_hash.
	s, _, project, owner, worker := setup(t)
	ctx := context.Background()
	m1, err := s.SendMessageWithID(ctx, project.ID, owner.Token, "", "", "ping", "initial message", "adapt-1")
	if err != nil {
		t.Fatal(err)
	}
	workerMCP := coord.MCPServer(s, project.ID, worker.Token)
	syncTool := workerMCP.GetTool("gossip_sync")

	// First check sees initial activity -> resets to idle_polls=0, 30s.
	res, err := syncTool.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "gossip_sync",
		Arguments: map[string]any{"operation": "inbox"},
	}})
	if err != nil || res.IsError {
		t.Fatalf("baseline inbox: err=%v res=%+v", err, res)
	}
	var f1 coord.ProjectSyncFrame
	_ = json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &f1)
	if f1.NextIdlePolls != 0 || f1.RecommendedPollSeconds != 30 || f1.LatestMessageHash != m1.Hash {
		t.Fatalf("unexpected baseline backoff frame: %+v", f1)
	}

	// Simulate 3 consecutive no-ack/empty polls anchored at last_read_hash=m1.Hash.
	after := f1.NextEventCursor
	idlePolls := f1.NextIdlePolls
	var seconds int
	for i := 0; i < 3; i++ {
		res, err = syncTool.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
			Name:      "gossip_sync",
			Arguments: map[string]any{"operation": "inbox", "after": after, "last_read_hash": m1.Hash, "idle_polls": idlePolls},
		}})
		if err != nil || res.IsError {
			t.Fatalf("idle poll %d: err=%v res=%+v", i+1, err, res)
		}
		var fi coord.ProjectSyncFrame
		_ = json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &fi)
		after = fi.NextEventCursor
		idlePolls = fi.NextIdlePolls
		seconds = fi.RecommendedPollSeconds
	}
	if idlePolls != 3 || seconds != 60 {
		t.Fatalf("expected backoff to 60s on 3rd idle poll, got idle=%d seconds=%d", idlePolls, seconds)
	}

	// New message resets immediately back to 30s.
	if _, err := s.SendMessageWithID(ctx, project.ID, owner.Token, "", "", "ping2", "wake up", "adapt-2"); err != nil {
		t.Fatal(err)
	}
	res, err = syncTool.Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "gossip_sync",
		Arguments: map[string]any{"operation": "inbox", "after": after, "last_read_hash": m1.Hash, "idle_polls": idlePolls},
	}})
	if err != nil || res.IsError {
		t.Fatalf("active poll: err=%v res=%+v", err, res)
	}
	var fReset coord.ProjectSyncFrame
	_ = json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &fReset)
	if fReset.NextIdlePolls != 0 || fReset.RecommendedPollSeconds != 30 {
		t.Fatalf("expected immediate reset to 30s on new message, got idle=%d seconds=%d", fReset.NextIdlePolls, fReset.RecommendedPollSeconds)
	}
}

func TestCheckAfterHintsOnAckSendAndSync(t *testing.T) {
	s, _, project, owner, worker := setup(t)
	ctx := context.Background()

	ownerMCP := coord.MCPServer(s, project.ID, owner.Token)
	workerMCP := coord.MCPServer(s, project.ID, worker.Token)

	// Owner sends a task message to worker.
	res, err := ownerMCP.GetTool("gossip_send").Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "gossip_send",
		Arguments: map[string]any{"recipient": worker.PrincipalID, "body": "Please analyze module X", "request_id": "eta-req-1"},
	}})
	if err != nil || res.IsError {
		t.Fatalf("owner send: err=%v res=%+v", err, res)
	}
	var taskMsg coord.Message
	_ = json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &taskMsg)

	// Worker acknowledges with checkAfter="4m" (240s).
	res, err = workerMCP.GetTool("gossip_read").Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "gossip_read",
		Arguments: map[string]any{"operation": "acknowledge", "message_id": taskMsg.ID, "checkAfter": "4m"},
	}})
	if err != nil || res.IsError {
		t.Fatalf("worker ack with checkAfter: err=%v res=%+v", err, res)
	}
	var ack coord.MessageAcknowledgment
	_ = json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &ack)
	if ack.CheckAfterSeconds != 240 || ack.CheckAfterAt <= ack.AcknowledgedAt {
		t.Fatalf("expected 240s checkAfter on ack, got %+v", ack)
	}

	// Owner's inbox sync sees worker's active checkAfter ETA and recommends checking back near 240s (not 30s),
	// whereas worker's own inbox does not delay itself for its own ack.
	res, err = ownerMCP.GetTool("gossip_sync").Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "gossip_sync",
		Arguments: map[string]any{"operation": "inbox"},
	}})
	if err != nil || res.IsError {
		t.Fatalf("owner sync: err=%v res=%+v", err, res)
	}
	var ownerFrame coord.ProjectSyncFrame
	_ = json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &ownerFrame)
	if ownerFrame.CheckAfterSeconds < 230 || ownerFrame.CheckAfterSeconds > 240 || ownerFrame.RecommendedPollSeconds != ownerFrame.CheckAfterSeconds {
		t.Fatalf("expected owner recommended_poll_seconds near 240s from worker ack, got %+v", ownerFrame)
	}

	res, err = workerMCP.GetTool("gossip_sync").Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "gossip_sync",
		Arguments: map[string]any{"operation": "inbox"},
	}})
	if err != nil || res.IsError {
		t.Fatalf("worker sync: err=%v res=%+v", err, res)
	}
	var workerFrame coord.ProjectSyncFrame
	_ = json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &workerFrame)
	if workerFrame.CheckAfterSeconds != 0 || workerFrame.RecommendedPollSeconds != 30 {
		t.Fatalf("expected worker not to wait on its own ack, got %+v", workerFrame)
	}

	// Worker finishes early and sends completion message with no checkAfter -> clears ETA and resets owner poll back to 30s!
	res, err = workerMCP.GetTool("gossip_send").Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "gossip_send",
		Arguments: map[string]any{"recipient": owner.PrincipalID, "body": "Finished early!", "request_id": "eta-req-2"},
	}})
	if err != nil || res.IsError {
		t.Fatalf("worker early reply: err=%v res=%+v", err, res)
	}
	res, err = ownerMCP.GetTool("gossip_sync").Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "gossip_sync",
		Arguments: map[string]any{"operation": "inbox", "after": ownerFrame.NextEventCursor, "message_after": ownerFrame.NextInboxCursor},
	}})
	if err != nil || res.IsError {
		t.Fatalf("owner sync after completion: err=%v res=%+v", err, res)
	}
	var afterDone coord.ProjectSyncFrame
	_ = json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &afterDone)
	if afterDone.CheckAfterSeconds != 0 || afterDone.RecommendedPollSeconds != 30 {
		t.Fatalf("expected completion message to clear checkAfter ETA back to 30s, got %+v", afterDone)
	}

	// Also verify gossip_send supports checkAfter (e.g. 90s) directly on messages.
	res, err = workerMCP.GetTool("gossip_send").Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "gossip_send",
		Arguments: map[string]any{"body": "Starting phase 2, might take 90s", "checkAfter": 90, "request_id": "eta-req-3"},
	}})
	if err != nil || res.IsError {
		t.Fatalf("worker send with checkAfter=90: err=%v res=%+v", err, res)
	}
	var msgWithETA coord.Message
	_ = json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &msgWithETA)
	if msgWithETA.CheckAfterSeconds != 90 || msgWithETA.CheckAfterAt <= msgWithETA.CreatedAt {
		t.Fatalf("expected check_after_seconds=90 on sent message, got %+v", msgWithETA)
	}
}

func TestPeerModesAndCommunicationTTLRTO(t *testing.T) {
	s, _, project, owner, worker := setup(t)
	ctx := context.Background()

	ownerMCP := coord.MCPServer(s, project.ID, owner.Token)
	workerMCP := coord.MCPServer(s, project.ID, worker.Token)

	// 1. Worker explicitly advertises working mode with checkAfter="3m".
	res, err := workerMCP.GetTool("gossip_agents").Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "gossip_agents",
		Arguments: map[string]any{"operation": "set_mode", "mode": "working", "checkAfter": "3m"},
	}})
	if err != nil || res.IsError {
		t.Fatalf("worker set_mode: err=%v res=%+v", err, res)
	}
	var pm coord.PeerMode
	_ = json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &pm)
	if pm.Mode != "working" || pm.CheckAfterSeconds != 180 || pm.PrincipalID != worker.PrincipalID {
		t.Fatalf("unexpected peer mode from set_mode: %+v", pm)
	}

	// Owner inspects advertised modes via gossip_agents(operation="modes").
	res, err = ownerMCP.GetTool("gossip_agents").Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "gossip_agents",
		Arguments: map[string]any{"operation": "modes"},
	}})
	if err != nil || res.IsError {
		t.Fatalf("owner list modes: err=%v res=%+v", err, res)
	}
	var modes []coord.PeerMode
	_ = json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &modes)
	if len(modes) != 1 || modes[0].Mode != "working" || modes[0].PrincipalID != worker.PrincipalID {
		t.Fatalf("expected 1 advertised working mode, got %+v", modes)
	}

	// 2. Owner sends a planning-mode message negotiating checkAfter="45s".
	res, err = ownerMCP.GetTool("gossip_send").Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name: "gossip_send",
		Arguments: map[string]any{
			"recipient":  worker.PrincipalID,
			"subject":    "Architecture check",
			"body":       "Let's align on schema",
			"mode":       "planning",
			"checkAfter": "45s",
			"request_id": "plan-msg-1",
		},
	}})
	if err != nil || res.IsError {
		t.Fatalf("owner planning send: err=%v res=%+v", err, res)
	}
	var planMsg coord.Message
	_ = json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &planMsg)
	if planMsg.Mode != "planning" || planMsg.CheckAfterSeconds != 45 {
		t.Fatalf("expected mode=planning checkAfter=45, got %+v", planMsg)
	}

	// Worker acknowledges in working mode with checkAfter="2m".
	res, err = workerMCP.GetTool("gossip_read").Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "gossip_read",
		Arguments: map[string]any{"operation": "acknowledge", "message_id": planMsg.ID, "mode": "working", "checkAfter": "2m"},
	}})
	if err != nil || res.IsError {
		t.Fatalf("worker ack mode=working: err=%v res=%+v", err, res)
	}
	var ack coord.MessageAcknowledgment
	_ = json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &ack)
	if ack.Mode != "working" || ack.CheckAfterSeconds != 120 {
		t.Fatalf("expected ack mode=working checkAfter=120, got %+v", ack)
	}

	// 3. Communication mode with TCP-like TTL + RTO:
	// Owner sends two communication messages:
	// - commMsg1: ttl=300s, rto=1s (should trigger RTODueMessages after 1.1s with doubled next_rto_seconds=2)
	// - commMsgExpired: ttl=1s, rto=1s (expires after 1.1s, so it must be marked expired=true and excluded from RTODueMessages)
	res, err = ownerMCP.GetTool("gossip_send").Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name: "gossip_send",
		Arguments: map[string]any{
			"recipient":  worker.PrincipalID,
			"subject":    "SYN ping",
			"body":       "Need quick ACK",
			"mode":       "communication",
			"ttl":        "5m",
			"rto":        1,
			"request_id": "comm-msg-1",
		},
	}})
	if err != nil || res.IsError {
		t.Fatalf("owner comm send 1: err=%v res=%+v", err, res)
	}
	var commMsg1 coord.Message
	_ = json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &commMsg1)
	if commMsg1.Mode != "communication" || commMsg1.TTLSeconds != 300 || commMsg1.RTOSeconds != 1 || commMsg1.ExpiresAt <= commMsg1.CreatedAt {
		t.Fatalf("unexpected commMsg1 fields: %+v", commMsg1)
	}

	res, err = ownerMCP.GetTool("gossip_send").Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name: "gossip_send",
		Arguments: map[string]any{
			"recipient":  worker.PrincipalID,
			"subject":    "Short-lived ping",
			"body":       "Expires in 1s",
			"mode":       "communication",
			"ttl":        1,
			"rto":        1,
			"request_id": "comm-msg-expired",
		},
	}})
	if err != nil || res.IsError {
		t.Fatalf("owner comm send expired: err=%v res=%+v", err, res)
	}
	var commMsgExpired coord.Message
	_ = json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &commMsgExpired)

	time.Sleep(1100 * time.Millisecond)

	// Owner syncs inbox: should see peer_modes (owner=communication, worker=working) AND rto_due_messages containing ONLY commMsg1!
	res, err = ownerMCP.GetTool("gossip_sync").Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "gossip_sync",
		Arguments: map[string]any{"operation": "inbox"},
	}})
	if err != nil || res.IsError {
		t.Fatalf("owner sync comm: err=%v res=%+v", err, res)
	}
	var ownerSync coord.ProjectSyncFrame
	_ = json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &ownerSync)
	if len(ownerSync.PeerModes) != 2 {
		t.Fatalf("expected 2 advertised peer_modes in inbox sync, got %+v", ownerSync.PeerModes)
	}
	if len(ownerSync.RTODueMessages) != 1 || ownerSync.RTODueMessages[0].ID != commMsg1.ID || ownerSync.RTODueMessages[0].NextRTOSeconds < 2 {
		t.Fatalf("expected commMsg1 in rto_due_messages with doubled next_rto_seconds>=2, got %+v", ownerSync.RTODueMessages)
	}

	// Check that commMsgExpired is flagged Expired=true in inbox_messages.
	foundExpired := false
	for _, im := range ownerSync.InboxMessages {
		if im.ID == commMsgExpired.ID {
			if !im.Expired {
				t.Fatalf("expected commMsgExpired to be marked expired=true in inbox, got %+v", im)
			}
			foundExpired = true
		}
	}
	if !foundExpired {
		t.Fatalf("expected commMsgExpired in inbox_messages")
	}

	// Worker acknowledges commMsg1 -> clears it from owner's rto_due_messages!
	res, err = workerMCP.GetTool("gossip_read").Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "gossip_read",
		Arguments: map[string]any{"operation": "acknowledge", "message_id": commMsg1.ID},
	}})
	if err != nil || res.IsError {
		t.Fatalf("worker ack commMsg1: err=%v res=%+v", err, res)
	}

	res, err = ownerMCP.GetTool("gossip_sync").Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "gossip_sync",
		Arguments: map[string]any{"operation": "inbox"},
	}})
	if err != nil || res.IsError {
		t.Fatalf("owner sync after comm ack: err=%v res=%+v", err, res)
	}
	var ownerSyncAfterAck coord.ProjectSyncFrame
	_ = json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &ownerSyncAfterAck)
	if len(ownerSyncAfterAck.RTODueMessages) != 0 {
		t.Fatalf("expected rto_due_messages to be empty after recipient ack, got %+v", ownerSyncAfterAck.RTODueMessages)
	}
}
