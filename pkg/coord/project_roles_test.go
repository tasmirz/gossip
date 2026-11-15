package coord

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestProjectRoleChangesAndOverrides(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "coord.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	p, owner, err := s.CreateProject(ctx, "Project", "Owner")
	if err != nil {
		t.Fatal(err)
	}
	worker, err := s.AddPrincipal(ctx, p.ID, owner.Token, "Worker", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrantPermission(ctx, p.ID, owner.Token, worker.PrincipalID, PermissionPlanEdit); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokePermission(ctx, p.ID, owner.Token, worker.PrincipalID, PermissionMessageSend); err != nil {
		t.Fatal(err)
	}
	role, err := s.SaveProjectRole(ctx, p.ID, owner.Token, ProjectRole{Name: "Communicator", Permissions: []string{PermissionProjectRead, PermissionMessageSend}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AssignProjectRole(ctx, p.ID, owner.Token, worker.PrincipalID, role.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishContext(ctx, p.ID, worker.Token, ContextRecord{Kind: "decision", Title: "Keep history", Body: "Retain revisions", Status: "active"}); err != nil {
		t.Fatalf("existing grant lost: %v", err)
	}
	send := func() error { _, err := s.SendMessage(ctx, p.ID, worker.Token, "", "", "hello", "message"); return err }
	if err := send(); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("existing revocation lost: %v", err)
	}
	if err := s.ResetPermissionOverride(ctx, p.ID, owner.Token, worker.PrincipalID, PermissionMessageSend); err != nil {
		t.Fatal(err)
	}
	if err := send(); err != nil {
		t.Fatalf("role baseline not restored: %v", err)
	}
	role.Permissions = []string{PermissionProjectRead}
	if _, err := s.SaveProjectRole(ctx, p.ID, owner.Token, role); err != nil {
		t.Fatal(err)
	}
	if err := send(); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("stale role allowed write: %v", err)
	}
	if _, err := s.GrantPermission(ctx, p.ID, owner.Token, worker.PrincipalID, PermissionMessageSend); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := send(); err != nil {
		t.Fatalf("override lost at restart: %v", err)
	}
	if _, err := s.SaveProjectRole(ctx, p.ID, worker.Token, ProjectRole{Name: "Unauthorized", Permissions: []string{PermissionProjectRead}}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("worker could administer roles: %v", err)
	}
}
