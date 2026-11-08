package coord_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tasmirz/gossip/pkg/coord"
)

func TestExplicitTaskDelegationGrantIsScopedAndRevocable(t *testing.T) {
	s, _, project, owner, worker := setup(t)
	ctx := context.Background()
	other, err := s.AddPrincipal(ctx, project.ID, owner.Token, "other worker", "worker")
	if err != nil {
		t.Fatal(err)
	}
	task := create(t, s, project, owner, nil)
	if _, err := s.SetWorkRole(ctx, project.ID, owner.Token, worker.PrincipalID, "", "coordinator", "", 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTask(ctx, project.ID, worker.Token, 1, "Research", "Map the behavior", "research", coord.ReportContract{ReviewType: "full_code"}, nil); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("work role granted task creation authority: %v", err)
	}
	if _, err := s.AssignTask(ctx, project.ID, worker.Token, task.ID, other.PrincipalID); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("work role granted task assignment authority: %v", err)
	}
	if _, err := s.GrantPermission(ctx, project.ID, worker.Token, other.PrincipalID, coord.PermissionTaskDelegate); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("worker granted permission: %v", err)
	}
	if _, err := s.GrantPermission(ctx, project.ID, owner.Token, worker.PrincipalID, "membership.admin"); err == nil {
		t.Fatal("unsupported permission was granted")
	}
	grant, err := s.GrantPermission(ctx, project.ID, owner.Token, worker.PrincipalID, coord.PermissionTaskDelegate)
	if err != nil || grant.PrincipalID != worker.PrincipalID {
		t.Fatalf("grant: %+v %v", grant, err)
	}
	if repeated, err := s.GrantPermission(ctx, project.ID, owner.Token, worker.PrincipalID, coord.PermissionTaskDelegate); err != nil || repeated != grant {
		t.Fatalf("idempotent grant: %+v %v", repeated, err)
	}
	otherProject, otherOwner, err := s.CreateProject(ctx, "other project", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrantPermission(ctx, otherProject.ID, otherOwner.Token, worker.PrincipalID, coord.PermissionTaskDelegate); !errors.Is(err, coord.ErrNotFound) {
		t.Fatalf("permission crossed projects: %v", err)
	}
	created, err := s.CreateTask(ctx, project.ID, worker.Token, 1, "Parallel research", "Investigate an independent question", "research", coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil || created.ID == "" {
		t.Fatalf("delegated task creation: %+v %v", created, err)
	}
	assigned, err := s.AssignTask(ctx, project.ID, worker.Token, task.ID, other.PrincipalID)
	if err != nil || assigned.AssignedTo != other.PrincipalID {
		t.Fatalf("delegated task assignment: %+v %v", assigned, err)
	}
	if _, err := s.AddPrincipal(ctx, project.ID, worker.Token, "extra", "worker"); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("task delegate enrolled a principal: %v", err)
	}
	workerGrants, err := s.ListPermissions(ctx, project.ID, worker.Token)
	if err != nil || len(workerGrants) != 5 || !containsGrant(workerGrants, grant.Permission) {
		t.Fatalf("worker's effective grants: %+v %v", workerGrants, err)
	}
	otherGrants, err := s.ListPermissions(ctx, project.ID, other.Token)
	if err != nil || len(otherGrants) != 4 || containsGrant(otherGrants, coord.PermissionTaskDelegate) {
		t.Fatalf("other worker's starter grants or grant isolation failed: %+v %v", otherGrants, err)
	}
	if err := s.RevokePermission(ctx, project.ID, owner.Token, worker.PrincipalID, coord.PermissionTaskDelegate); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTask(ctx, project.ID, worker.Token, 1, "More research", "This should be denied", "research", coord.ReportContract{ReviewType: "full_code"}, nil); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("revoked delegate created task: %v", err)
	}
	if _, err := s.AssignTask(ctx, project.ID, worker.Token, created.ID, other.PrincipalID); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("revoked delegate assigned task: %v", err)
	}
}

func containsGrant(grants []coord.PermissionGrant, permission string) bool {
	for _, grant := range grants {
		if grant.Permission == permission {
			return true
		}
	}
	return false
}

func TestProjectReadMessagingClaimAndSubmitAreSeparateGrants(t *testing.T) {
	s, _, project, owner, worker := setup(t)
	ctx := context.Background()
	reviewer, err := s.AddPrincipal(ctx, project.ID, owner.Token, "reader", "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	task := create(t, s, project, owner, nil)
	if err = s.AcknowledgePlan(ctx, project.ID, reviewer.Token, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetTask(ctx, project.ID, reviewer.Token, task.ID); err != nil {
		t.Fatalf("project.read starter grant failed: %v", err)
	}
	if _, err = s.ClaimTask(ctx, project.ID, reviewer.Token, task.ID, time.Minute); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("reviewer starter profile gained task.claim: %v", err)
	}
	if _, err = s.SendMessage(ctx, project.ID, reviewer.Token, "", "", "", "A project note"); err != nil {
		t.Fatalf("message.send starter grant failed: %v", err)
	}
	if err = s.RevokePermission(ctx, project.ID, owner.Token, reviewer.PrincipalID, coord.PermissionProjectRead); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetTask(ctx, project.ID, reviewer.Token, task.ID); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("revoked project.read remained effective: %v", err)
	}
	if _, err = s.ListPermissions(ctx, project.ID, reviewer.Token); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("revoked project.read still exposed permission metadata: %v", err)
	}
	if _, err = s.SendMessage(ctx, project.ID, reviewer.Token, "", "", "", "This should be denied"); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("action grant bypassed revoked project.read: %v", err)
	}
	if _, err = s.GrantPermission(ctx, project.ID, owner.Token, reviewer.PrincipalID, coord.PermissionProjectRead); err != nil {
		t.Fatal(err)
	}
	if err = s.RevokePermission(ctx, project.ID, owner.Token, reviewer.PrincipalID, coord.PermissionMessageSend); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SendMessage(ctx, project.ID, reviewer.Token, "", "", "", "This should be denied"); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("revoked message.send remained effective: %v", err)
	}

	grants, err := s.ListPermissions(ctx, project.ID, worker.Token)
	if err != nil || len(grants) != 4 || !containsGrant(grants, coord.PermissionTaskClaim) || !containsGrant(grants, coord.PermissionResultSubmit) {
		t.Fatalf("worker enrollment starter profile: %+v %v", grants, err)
	}
	if _, err = s.SetWorkRole(ctx, project.ID, owner.Token, worker.PrincipalID, "", "reviewer", "Custom duties; no policy change", 0, 0); err != nil {
		t.Fatal(err)
	}
	grants, err = s.ListPermissions(ctx, project.ID, worker.Token)
	if err != nil || len(grants) != 4 || !containsGrant(grants, coord.PermissionTaskClaim) || !containsGrant(grants, coord.PermissionResultSubmit) {
		t.Fatalf("changing work duties changed action grants: %+v %v", grants, err)
	}
	claim, err := s.ClaimTask(ctx, project.ID, worker.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RevokePermission(ctx, project.ID, owner.Token, worker.PrincipalID, coord.PermissionResultSubmit); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SubmitReport(ctx, project.ID, worker.Token, task.ID, claim.Attempt, "commit:permission", "No result permission"); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("revoked result.submit remained effective: %v", err)
	}
}

func TestProjectReadRevocationBlocksAllProjectReadSurfaces(t *testing.T) {
	s, _, project, owner, worker := setup(t)
	ctx := context.Background()
	task := create(t, s, project, owner, nil)
	repo, err := s.CreateRepository(ctx, project.ID, owner.Token, "access-audit")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RegisterWorkspace(ctx, project.ID, worker.Token, repo.ID, "worker checkout", "worker-host", "/workspace/repo", "main", "abc123", false); err != nil {
		t.Fatal(err)
	}
	message, err := s.SendMessage(ctx, project.ID, owner.Token, "", "", "", "private project update")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RevokePermission(ctx, project.ID, owner.Token, worker.PrincipalID, coord.PermissionProjectRead); err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name string
		read func() error
	}{
		{"identity", func() error { _, e := s.WhoAmI(ctx, project.ID, worker.Token); return e }},
		{"members", func() error { _, e := s.ListPrincipals(ctx, project.ID, worker.Token); return e }},
		{"permissions", func() error { _, e := s.ListPermissions(ctx, project.ID, worker.Token); return e }},
		{"plan", func() error { _, e := s.CurrentPlan(ctx, project.ID, worker.Token); return e }},
		{"tasks", func() error { _, e := s.ListTasks(ctx, project.ID, worker.Token, 20); return e }},
		{"task search", func() error {
			_, e := s.SearchTasks(ctx, project.ID, worker.Token, coord.TaskFilter{Limit: 20})
			return e
		}},
		{"task counts", func() error { _, e := s.TaskCounts(ctx, project.ID, worker.Token); return e }},
		{"task detail", func() error { _, e := s.GetTask(ctx, project.ID, worker.Token, task.ID); return e }},
		{"attempt snapshot", func() error { _, e := s.GetAttempt(ctx, project.ID, worker.Token, task.ID, 1); return e }},
		{"report", func() error { _, e := s.GetReport(ctx, project.ID, worker.Token, "missing-report"); return e }},
		{"report page", func() error {
			_, e := s.GetReportPage(ctx, project.ID, worker.Token, "missing-report", 0, 10)
			return e
		}},
		{"candidate", func() error { _, e := s.GetCandidate(ctx, project.ID, worker.Token, "missing-candidate"); return e }},
		{"candidate page", func() error {
			_, e := s.GetCandidatePage(ctx, project.ID, worker.Token, "missing-candidate", 0, 10)
			return e
		}},
		{"settings", func() error { _, e := s.GetSettings(ctx, project.ID, worker.Token); return e }},
		{"budget", func() error { _, e := s.GetBudget(ctx, project.ID, worker.Token); return e }},
		{"repositories", func() error { _, e := s.ListRepositories(ctx, project.ID, worker.Token); return e }},
		{"remote recognition", func() error {
			_, e := s.FindRepositoriesByRemote(ctx, project.ID, worker.Token, "https://example.com/org/repo.git")
			return e
		}},
		{"workspaces", func() error { _, e := s.ListWorkspaces(ctx, project.ID, worker.Token); return e }},
		{"roles", func() error { _, e := s.ListWorkRoles(ctx, project.ID, worker.Token); return e }},
		{"messages", func() error { _, e := s.ListMessages(ctx, project.ID, worker.Token, 0, 20); return e }},
		{"message acknowledgments", func() error { _, e := s.MessageAcknowledgments(ctx, project.ID, worker.Token, message.ID); return e }},
		{"recent messages", func() error { _, e := s.RecentMessages(ctx, project.ID, worker.Token, 20); return e }},
		{"events", func() error { _, e := s.ReadEvents(ctx, project.ID, worker.Token, 0, 20); return e }},
		{"recent events", func() error { _, e := s.RecentEvents(ctx, project.ID, worker.Token, 20); return e }},
		{"event checkpoint", func() error { _, e := s.EventCheckpoint(ctx, project.ID, worker.Token); return e }},
		{"agent presence", func() error { _, e := s.ListAgentPresence(ctx, project.ID, worker.Token); return e }},
		{"integrations", func() error { _, e := s.ListIntegrations(ctx, project.ID, worker.Token, 20); return e }},
		{"candidates", func() error { _, e := s.ListCandidates(ctx, project.ID, worker.Token, task.ID, 20); return e }},
		{"legacy archive", func() error { _, e := s.ListLegacyImports(ctx, project.ID, worker.Token); return e }},
		{"legacy records", func() error {
			_, e := s.ReadLegacyRecords(ctx, project.ID, worker.Token, "missing-import", "message", "", 20)
			return e
		}},
		{"legacy record chunk", func() error {
			_, e := s.ReadLegacyRecordChunk(ctx, project.ID, worker.Token, "missing-import", "message", "missing-record", 0, 10)
			return e
		}},
		{"replica sync", func() error { _, e := s.SyncProject(ctx, project.ID, worker.Token, 0, 20); return e }},
		{"replica receipt sync", func() error {
			_, e := s.SyncProjectFromReplica(ctx, project.ID, worker.Token, "replica_0123456789abcdef0123456789abcdef", strings.Repeat("0", 64), 0, 20)
			return e
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.read(); !errors.Is(err, coord.ErrUnauthorized) {
				t.Fatalf("read remained available after project.read revocation: %v", err)
			}
		})
	}
}

func TestMembershipManagerCanOnlyChangeMemberCredentialsInProject(t *testing.T) {
	s, _, project, owner, manager := setup(t)
	ctx := context.Background()
	if _, err := s.GrantPermission(ctx, project.ID, owner.Token, manager.PrincipalID, coord.PermissionMembershipManage); err != nil {
		t.Fatal(err)
	}
	member, err := s.AddPrincipal(ctx, project.ID, manager.Token, "new reviewer", "reviewer")
	if err != nil {
		t.Fatalf("membership manager could not enroll a member: %v", err)
	}
	if _, err = s.WhoAmI(ctx, project.ID, member.Token); err != nil {
		t.Fatalf("enrolled member credential is invalid: %v", err)
	}
	if _, err = s.AddPrincipal(ctx, project.ID, manager.Token, "new owner", "owner"); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("membership manager created an owner: %v", err)
	}
	if err = s.RevokePrincipal(ctx, project.ID, manager.Token, member.PrincipalID); err != nil {
		t.Fatalf("membership manager could not revoke a member: %v", err)
	}
	if _, err = s.WhoAmI(ctx, project.ID, member.Token); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("revoked member credential remained valid: %v", err)
	}
	if err = s.RevokePrincipal(ctx, project.ID, manager.Token, manager.PrincipalID); err == nil {
		t.Fatal("membership manager revoked their own credential")
	}
	if _, err = s.GrantPermission(ctx, project.ID, manager.Token, manager.PrincipalID, coord.PermissionPlanEdit); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("membership manager delegated permissions: %v", err)
	}
	preparedProject, prepared, err := coord.PrepareCredential(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AddPreparedRemotePrincipal(ctx, preparedProject, manager.Token, "remote member", "worker", prepared, strings.Repeat("a", 64), 0); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("membership manager granted remote certificate access: %v", err)
	}
}

func TestPlanAndSettingsAuthorityRequiresScopedActionGrants(t *testing.T) {
	s, _, project, owner, worker := setup(t)
	ctx := context.Background()
	if _, err := s.PublishPlan(ctx, project.ID, worker.Token, "unauthorized change", 1); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("worker changed the plan without a grant: %v", err)
	}
	if _, err := s.GrantPermission(ctx, project.ID, owner.Token, worker.PrincipalID, coord.PermissionPlanEdit); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPrincipal(ctx, project.ID, worker.Token, "unauthorized invite", "worker"); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("plan editor gained membership administration: %v", err)
	}
	if revision, err := s.PublishPlan(ctx, project.ID, worker.Token, "delegated plan edit", 1); err != nil || revision != 2 {
		t.Fatalf("granted plan edit failed: revision=%d err=%v", revision, err)
	}
	if err := s.RevokePermission(ctx, project.ID, owner.Token, worker.PrincipalID, coord.PermissionPlanEdit); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishPlan(ctx, project.ID, worker.Token, "revoked plan edit", 2); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("revoked plan editor changed the plan: %v", err)
	}

	settings, err := s.GetSettings(ctx, project.ID, owner.Token)
	if err != nil {
		t.Fatal(err)
	}
	settings.MaxParallel++
	if _, err = s.UpdateSettings(ctx, project.ID, worker.Token, settings); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("worker changed settings without a grant: %v", err)
	}
	if _, err = s.GrantPermission(ctx, project.ID, owner.Token, worker.PrincipalID, coord.PermissionSettingsEdit); err != nil {
		t.Fatal(err)
	}
	updated, err := s.UpdateSettings(ctx, project.ID, worker.Token, settings)
	if err != nil || updated.MaxParallel != settings.MaxParallel {
		t.Fatalf("granted settings edit failed: %+v err=%v", updated, err)
	}
	if _, err = s.CreateTask(ctx, project.ID, worker.Token, 2, "No delegation", "settings access must not grant task delegation", "research", coord.ReportContract{ReviewType: "full_code"}, nil); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("settings editor gained task delegation: %v", err)
	}
	if err = s.RevokePermission(ctx, project.ID, owner.Token, worker.PrincipalID, coord.PermissionSettingsEdit); err != nil {
		t.Fatal(err)
	}
	updated.MaxParallel++
	if _, err = s.UpdateSettings(ctx, project.ID, worker.Token, updated); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("revoked settings editor changed settings: %v", err)
	}

	other, otherOwner, err := s.CreateProject(ctx, "separate project", "separate owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.GrantPermission(ctx, other.ID, otherOwner.Token, worker.PrincipalID, coord.PermissionPlanEdit); !errors.Is(err, coord.ErrNotFound) {
		t.Fatalf("project permission was granted to a principal outside that project: %v", err)
	}
}

func TestReviewDecisionGrantIsIndependentAndRevocable(t *testing.T) {
	s, _, project, owner, worker := setup(t)
	ctx := context.Background()
	reviewer, err := s.AddPrincipal(ctx, project.ID, owner.Token, "reviewer", "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	decider, err := s.AddPrincipal(ctx, project.ID, owner.Token, "delegated reviewer", "worker")
	if err != nil {
		t.Fatal(err)
	}
	task := create(t, s, project, owner, nil)
	claim, err := s.ClaimTask(ctx, project.ID, worker.Token, task.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	report, err := s.SubmitReport(ctx, project.ID, worker.Token, task.ID, claim.Attempt, "commit:review", "Findings")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AcceptReport(ctx, project.ID, worker.Token, report.ID, report.Digest); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("worker reviewed their own report: %v", err)
	}
	if err = s.AcceptReport(ctx, project.ID, reviewer.Token, report.ID, report.Digest); err != nil {
		t.Fatalf("reviewer baseline role lost review authority: %v", err)
	}

	second := create(t, s, project, owner, nil)
	secondClaim, err := s.ClaimTask(ctx, project.ID, worker.Token, second.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	secondReport, err := s.SubmitReport(ctx, project.ID, worker.Token, second.ID, secondClaim.Attempt, "commit:review-2", "More findings")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RequestRework(ctx, project.ID, reviewer.Token, secondReport.ID, secondReport.Digest, "Clarify evidence"); err != nil {
		t.Fatalf("reviewer baseline role lost rework authority: %v", err)
	}
	secondClaim, err = s.ClaimTask(ctx, project.ID, worker.Token, second.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	secondReport, err = s.SubmitReport(ctx, project.ID, worker.Token, second.ID, secondClaim.Attempt, "commit:review-3", "Clarified findings")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AcceptReport(ctx, project.ID, decider.Token, secondReport.ID, secondReport.Digest); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("member gained report authority without grant: %v", err)
	}
	if _, err = s.GrantPermission(ctx, project.ID, owner.Token, decider.PrincipalID, coord.PermissionReviewDecide); err != nil {
		t.Fatal(err)
	}
	if err = s.AcceptReport(ctx, project.ID, decider.Token, secondReport.ID, secondReport.Digest); err != nil {
		t.Fatalf("granted review decision failed: %v", err)
	}
	if _, err = s.CreateTask(ctx, project.ID, decider.Token, 1, "No delegation", "Review authority does not delegate", "research", coord.ReportContract{ReviewType: "full_code"}, nil); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("review decision grant gained task delegation: %v", err)
	}
	if err = s.RevokePermission(ctx, project.ID, owner.Token, decider.PrincipalID, coord.PermissionReviewDecide); err != nil {
		t.Fatal(err)
	}
	third := create(t, s, project, owner, nil)
	thirdClaim, err := s.ClaimTask(ctx, project.ID, worker.Token, third.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	thirdReport, err := s.SubmitReport(ctx, project.ID, worker.Token, third.ID, thirdClaim.Attempt, "commit:review-4", "Final findings")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AcceptReport(ctx, project.ID, decider.Token, thirdReport.ID, thirdReport.Digest); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("revoked review decision remained effective: %v", err)
	}
}

func TestPermissionCatalogMigrationPreservesExistingDelegation(t *testing.T) {
	s, path, project, owner, worker := setup(t)
	ctx := context.Background()
	if _, err := s.GrantPermission(ctx, project.ID, owner.Token, worker.PrincipalID, coord.PermissionTaskDelegate); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`BEGIN`,
		`ALTER TABLE principal_permissions RENAME TO principal_permissions_expanded`,
		`CREATE TABLE principal_permissions (
  project_id TEXT NOT NULL REFERENCES projects(id), principal_id TEXT NOT NULL REFERENCES principals(id),
  permission TEXT NOT NULL CHECK(permission IN ('task.delegate')),
  granted_by TEXT NOT NULL REFERENCES principals(id), granted_at INTEGER NOT NULL,
  PRIMARY KEY(project_id,principal_id,permission)
)`,
		`INSERT INTO principal_permissions SELECT * FROM principal_permissions_expanded WHERE permission='task.delegate'`,
		`DROP TABLE principal_permissions_expanded`,
		`DELETE FROM coord_migrations WHERE name='permission_catalog_v1'`,
		`DELETE FROM coord_migrations WHERE name='permission_review_decide_v2'`,
		`DELETE FROM coord_migrations WHERE name='project_action_permissions_v3'`,
		`DELETE FROM coord_migrations WHERE name='membership_manage_v4'`,
		`DELETE FROM coord_migrations WHERE name='integration_permission_v5'`,
		`COMMIT`,
	} {
		if _, err = db.Exec(query); err != nil {
			db.Close()
			t.Fatalf("prepare legacy permission schema: %v", err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := coord.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	grants, err := migrated.ListPermissions(ctx, project.ID, worker.Token)
	if err != nil || len(grants) != 5 || !containsGrant(grants, coord.PermissionTaskDelegate) || !containsGrant(grants, coord.PermissionProjectRead) || !containsGrant(grants, coord.PermissionResultSubmit) {
		t.Fatalf("legacy delegation grant was not preserved: %+v err=%v", grants, err)
	}
	for _, permission := range []string{coord.PermissionPlanEdit, coord.PermissionSettingsEdit, coord.PermissionReviewDecide, coord.PermissionIntegration} {
		if _, err = migrated.GrantPermission(ctx, project.ID, owner.Token, worker.PrincipalID, permission); err != nil {
			t.Fatalf("upgraded schema rejected %s: %v", permission, err)
		}
	}
}
