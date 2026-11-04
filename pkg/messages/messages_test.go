package messages_test

import (
	"os"
	"testing"

	"github.com/tasmirz/gossip/pkg/messages"
	"github.com/tasmirz/gossip/pkg/state"
)

func TestMessagesLifecycle(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-msg-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := state.NewStore(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	mgr := messages.NewManager(store)

	// Send direct message
	m1, err := mgr.SendMessage("orch_1", "orchestrator", "agent:math_1", "th-1", "CRC Question", "What is the generator polynomial?")
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	if m1.ID == "" {
		t.Fatalf("Expected non-empty message ID")
	}

	// Send role broadcast message
	m2, err := mgr.SendMessage("math_1", "math_expert", "role:network_engineer", "th-1", "CRC Answer", "Use 0x82F63B78 for Castagnoli.")
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	// Send global broadcast
	m3, err := mgr.SendMessage("orch_1", "orchestrator", "all", "", "Announcement", "Milestone 1 reached!")
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	// Read inbox for math_1
	inboxMath, err := mgr.ReadInbox("math_1", "math_expert", false, "", 10)
	if err != nil {
		t.Fatalf("ReadInbox failed: %v", err)
	}
	// math_1 should receive m1 (addressed to agent:math_1) and m3 (broadcast to all)
	if len(inboxMath) != 2 {
		t.Fatalf("Expected 2 messages for math_1, got %d", len(inboxMath))
	}

	// Read inbox for net_1 with role network_engineer
	inboxNet, err := mgr.ReadInbox("net_1", "network_engineer", false, "", 10)
	if err != nil {
		t.Fatalf("ReadInbox failed: %v", err)
	}
	// net_1 should receive m2 (addressed to role:network_engineer) and m3 (all)
	if len(inboxNet) != 2 {
		t.Fatalf("Expected 2 messages for net_1, got %d", len(inboxNet))
	}

	// Test unread filter and marking read
	unreadNet, err := mgr.ReadInbox("net_1", "network_engineer", true, "", 10)
	if err != nil {
		t.Fatalf("ReadInbox unread failed: %v", err)
	}
	if len(unreadNet) != 2 {
		t.Fatalf("Expected 2 unread for net_1, got %d", len(unreadNet))
	}

	// Mark m2 as read
	if err := mgr.MarkRead("net_1", []string{m2.ID}); err != nil {
		t.Fatalf("MarkRead failed: %v", err)
	}

	unreadNetAfter, err := mgr.ReadInbox("net_1", "network_engineer", true, "", 10)
	if err != nil {
		t.Fatalf("ReadInbox unread after mark failed: %v", err)
	}
	if len(unreadNetAfter) != 1 || unreadNetAfter[0].ID != m3.ID {
		t.Fatalf("Expected 1 unread message (m3), got %d", len(unreadNetAfter))
	}

	// Test list threads
	threads, err := mgr.ListThreads(10)
	if err != nil {
		t.Fatalf("ListThreads failed: %v", err)
	}
	if len(threads) < 1 {
		t.Fatalf("Expected at least 1 thread, got %d", len(threads))
	}
}
