package cron

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tasmirz/gossip/pkg/state"
)

func TestParseInterval(t *testing.T) {
	cases := []struct {
		input    string
		expected time.Duration
	}{
		{"", 30 * time.Second},
		{"@minutely", 1 * time.Minute},
		{"* * * * *", 1 * time.Minute},
		{"@hourly", 1 * time.Hour},
		{"@daily", 24 * time.Hour},
		{"@every 45s", 45 * time.Second},
		{"*/5 * * * *", 5 * time.Minute},
		{"10s", 10 * time.Second},
	}

	for _, c := range cases {
		dur, err := ParseInterval(c.input)
		if err != nil {
			t.Errorf("ParseInterval(%q) error: %v", c.input, err)
			continue
		}
		if dur != c.expected {
			t.Errorf("ParseInterval(%q) = %v, expected %v", c.input, dur, c.expected)
		}
	}
}

func TestRunCheck(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-cron-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := state.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	// Register an active agent
	err = store.RegisterAgent(state.AgentSession{
		ID:            "agent-worker-1",
		Role:          "worker",
		Status:        "active",
		LastHeartbeat: time.Now(),
	})
	if err != nil {
		t.Fatalf("RegisterAgent failed: %v", err)
	}

	res, err := RunCheck(store, "agent-worker-1")
	if err != nil {
		t.Fatalf("RunCheck failed: %v", err)
	}

	if res.ActiveAgentsCount != 1 {
		t.Errorf("expected 1 active agent, got %d", res.ActiveAgentsCount)
	}
	if !strings.Contains(res.Summary, "Tunnel: LOCAL ONLY") {
		t.Errorf("expected LOCAL ONLY in summary: %s", res.Summary)
	}

	// Test recording log
	if err := RecordCronLog(tmpDir, res); err != nil {
		t.Fatalf("RecordCronLog failed: %v", err)
	}
	logBytes, err := os.ReadFile(filepath.Join(tmpDir, "cron.log"))
	if err != nil || len(logBytes) == 0 {
		t.Fatalf("failed to read cron.log: %v", err)
	}
}

func TestRunnerLifecycle(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-runner-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := state.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	runner := NewRunner(store, "test-agent", 50*time.Millisecond)

	count := 0
	checked := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runner.Start(ctx, func(res *CheckResult) {
		count++
		select {
		case checked <- struct{}{}:
		default:
		}
	})

	select {
	case <-checked:
	case <-time.After(time.Second):
		t.Fatal("runner did not execute its initial check")
	}
	runner.Stop()

	if count == 0 {
		t.Error("expected at least 1 check executed by runner")
	}
	if runner.LastResult() == nil {
		t.Error("expected non-nil LastResult")
	}
}
