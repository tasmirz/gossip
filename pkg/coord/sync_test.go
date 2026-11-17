package coord_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

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
