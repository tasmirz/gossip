package guardrails_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tasmirz/gossip/pkg/guardrails"
	"github.com/tasmirz/gossip/pkg/state"
)

func TestGuardrailsLifecycle(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-guardrails-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := state.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	mgr := guardrails.NewManager(store)

	// 1. Derive Project Roles for a Go and Network project
	pg, err := mgr.DeriveProjectRoles("Gossip Mesh", []string{"Go", "Networking", "Distributed Systems"}, "High performance peer-to-peer agent mesh")
	if err != nil {
		t.Fatalf("failed to derive roles: %v", err)
	}

	if len(pg.Roles) < 3 {
		t.Fatalf("expected at least 3 derived roles, got %d", len(pg.Roles))
	}

	// Verify Go Systems Engineer was derived
	goEng, exists := pg.Roles["go_systems_engineer"]
	if !exists {
		t.Fatalf("expected role 'go_systems_engineer' to be derived for Go project")
	}
	if len(goEng.ForbiddenActions) == 0 {
		t.Fatalf("expected forbidden actions for go_systems_engineer")
	}

	// 2. Validate Actions
	// Test blocked action
	resBlocked, err := mgr.ValidateProposedAction("go_systems_engineer", "bypass_race_detector", "pkg/main.go", "")
	if err != nil {
		t.Fatalf("validate action failed: %v", err)
	}
	if resBlocked.Allowed {
		t.Fatalf("expected 'bypass_race_detector' to be blocked")
	}

	// Test sensitive path (.env caution)
	resSecret, err := mgr.ValidateProposedAction("go_systems_engineer", "read_file", ".env.local", "")
	if err != nil {
		t.Fatalf("validate action failed: %v", err)
	}
	if resSecret.Allowed {
		t.Fatalf("expected editing .env to be blocked by caution rule")
	}

	// Test normal safe action
	resAllowed, err := mgr.ValidateProposedAction("go_systems_engineer", "write_code", "pkg/protocol/framing.go", "package protocol")
	if err != nil {
		t.Fatalf("validate action failed: %v", err)
	}
	if !resAllowed.Allowed {
		t.Fatalf("expected normal write_code to be allowed")
	}

	// 3. Verify AGENTS.md generation
	agentsFile := filepath.Join(tmpDir, "AGENTS.md")
	content, err := os.ReadFile(agentsFile)
	if err != nil {
		t.Fatalf("failed to read AGENTS.md: %v", err)
	}
	if !strings.Contains(string(content), "Gossip Mesh") {
		t.Fatalf("expected AGENTS.md to contain project name")
	}
	if !strings.Contains(string(content), "go_systems_engineer") {
		t.Fatalf("expected AGENTS.md to list go_systems_engineer")
	}
	t.Logf("Generated AGENTS.md excerpt:\n%s", string(content)[:350])
}
