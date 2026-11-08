package coord_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tasmirz/gossip/pkg/coord"
)

func TestWorkRolePresetsAreVersionedAndIncludeHandoffBoundaries(t *testing.T) {
	presets := coord.WorkRolePresets()
	want := []string{"coordinator", "researcher", "implementer", "reviewer", "verifier", "integrator"}
	if len(presets) != len(want) {
		t.Fatalf("preset count = %d, want %d", len(presets), len(want))
	}
	for i, preset := range presets {
		if preset.Name != want[i] || preset.Version == "" || preset.Provenance == "" || len(preset.Sources) == 0 || preset.Duties == "" || preset.ExpectedOutput == "" || preset.HandoffFormat == "" || preset.Escalation == "" {
			t.Fatalf("incomplete or unordered preset %d: %+v", i, preset)
		}
		for _, source := range preset.Sources {
			if !strings.HasPrefix(source, "https://") {
				t.Fatalf("preset %q has an untrusted or non-HTTPS source: %q", preset.Name, source)
			}
			parsed, err := url.Parse(source)
			if err != nil {
				t.Fatalf("preset %q has invalid URL %q: %v", preset.Name, source, err)
			}
			if parsed.Hostname() != "developers.openai.com" && parsed.Hostname() != "openai.github.io" {
				t.Fatalf("preset %q has unapproved source domain %q", preset.Name, parsed.Hostname())
			}
		}
	}
}

func TestWorkRoleChangesDutiesWithoutGrantingPermissions(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()
	task := create(t, s, p, owner, nil)
	role, err := s.SetWorkRole(ctx, p.ID, owner.Token, worker.PrincipalID, "", "coordinator", "", 0, 0)
	if err != nil || role.Duties == "" || !role.Active || !strings.Contains(role.Duties, "Preset version: 1.0.0") || !strings.Contains(role.Duties, "Handoff format:") || !strings.Contains(role.Duties, "Escalation:") {
		t.Fatalf("preset: %+v %v", role, err)
	}
	if _, err = s.CreateTask(ctx, p.ID, worker.Token, 1, "unauthorized", "scope", "research", coord.ReportContract{ReviewType: "full_code"}, nil); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("role granted permission: %v", err)
	}
	if _, err = s.SetWorkRole(ctx, p.ID, worker.Token, worker.PrincipalID, "", "implementer", "", 0, role.Revision); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("worker changed role: %v", err)
	}
	if _, err = s.SetWorkRole(ctx, p.ID, owner.Token, worker.PrincipalID, "", "researcher", "", 0, 0); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	scoped, err := s.SetWorkRole(ctx, p.ID, owner.Token, worker.PrincipalID, task.ID, "specialist", "Map failure paths", time.Now().Add(time.Minute).UnixMilli(), 0)
	if err != nil || !scoped.Active {
		t.Fatalf("scoped: %+v %v", scoped, err)
	}
	roles, err := s.ListWorkRoles(ctx, p.ID, worker.Token)
	if err != nil || len(roles) != 2 {
		t.Fatalf("list: %+v %v", roles, err)
	}
	cleared, err := s.SetWorkRole(ctx, p.ID, owner.Token, worker.PrincipalID, task.ID, "", "", 0, scoped.Revision)
	if err != nil || cleared.Active || cleared.Revision != 2 {
		t.Fatalf("clear: %+v %v", cleared, err)
	}
	other, otherOwner, err := s.CreateProject(ctx, "other", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetWorkRole(ctx, other.ID, otherOwner.Token, worker.PrincipalID, "", "researcher", "", 0, 0); !errors.Is(err, coord.ErrNotFound) {
		t.Fatalf("cross project principal: %v", err)
	}
	caseRole, err := s.SetWorkRole(ctx, p.ID, owner.Token, worker.PrincipalID, "", "Coordinator", "", 0, role.Revision)
	if err != nil || caseRole.Name != "coordinator" {
		t.Fatalf("case-insensitive preset name failed or did not canonicalize: %+v %v", caseRole, err)
	}
	roleTask, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Role Task", "Requires coordinator", "review", coord.ReportContract{ReviewType: "full_code", RequiredRole: "coordinator"}, nil)
	if err != nil {
		t.Fatalf("create role task: %v", err)
	}
	if err := s.RevokePrincipal(ctx, p.ID, owner.Token, worker.PrincipalID); err != nil {
		t.Fatalf("revoke principal: %v", err)
	}
	if _, err := s.AssignTask(ctx, p.ID, owner.Token, roleTask.ID, worker.PrincipalID); err == nil {
		t.Fatal("expected assign task to fail for revoked principal despite work_role row")
	}
}
