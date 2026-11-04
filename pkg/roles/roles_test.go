package roles_test

import (
	"strings"
	"testing"

	"github.com/tasmirz/gossip/pkg/roles"
)

func TestGetRole(t *testing.T) {
	tests := []struct {
		roleID      string
		expectFound bool
	}{
		{"orchestrator", true},
		{"worker", true},
		{"network_engineer", true},
		{"network-engineer", true},
		{"math_expert", true},
		{"math-expert", true},
		{"systems_architect", true},
		{"security_specialist", true},
		{"qa_engineer", true},
		{"researcher", true},
		{"unknown_role", false},
	}

	for _, tt := range tests {
		role, ok := roles.GetRole(tt.roleID)
		if ok != tt.expectFound {
			t.Errorf("GetRole(%q) = %v; expected found = %v", tt.roleID, ok, tt.expectFound)
		}
		if ok {
			if role.Title == "" || role.SystemPrompt == "" || len(role.Directives) == 0 {
				t.Errorf("Role %s has incomplete specification", tt.roleID)
			}
		}
	}
}

func TestListRoles(t *testing.T) {
	all := roles.ListRoles()
	if len(all) < 8 {
		t.Fatalf("Expected at least 8 roles, got %d", len(all))
	}
}

func TestFormatRoleSummary(t *testing.T) {
	summary := roles.FormatRoleSummary()
	if !strings.Contains(summary, "Lead System Orchestrator") {
		t.Errorf("Summary missing Orchestrator")
	}
	if !strings.Contains(summary, "network_engineer") {
		t.Errorf("Summary missing network_engineer")
	}
	if !strings.Contains(summary, "math_expert") {
		t.Errorf("Summary missing math_expert")
	}
}
