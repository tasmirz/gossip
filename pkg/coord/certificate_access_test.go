package coord_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tasmirz/gossip/pkg/coord"
)

func TestProjectCertificateGrantsAreOwnerManagedAndImmediate(t *testing.T) {
	s, _, project, owner, worker := setup(t)
	ctx := context.Background()
	fingerprint := strings.Repeat("ab", 32)
	if _, err := s.AddCertificateGrant(ctx, project.ID, worker.Token, fingerprint); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("worker added certificate grant: %v", err)
	}
	grant, err := s.AddCertificateGrant(ctx, project.ID, owner.Token, strings.ToUpper(fingerprint[:2])+fingerprint[2:])
	if err != nil || grant.ProjectID != project.ID || grant.Fingerprint != fingerprint {
		t.Fatalf("owner grant: %+v %v", grant, err)
	}
	if _, err = s.AddCertificateGrant(ctx, project.ID, owner.Token, fingerprint); err != nil {
		t.Fatalf("idempotent grant: %v", err)
	}
	grants, err := s.ListCertificateGrants(ctx, project.ID, owner.Token)
	if err != nil || len(grants) != 1 || grants[0].Fingerprint != fingerprint {
		t.Fatalf("certificate grant list: %+v %v", grants, err)
	}
	if _, err = s.ListCertificateGrants(ctx, project.ID, worker.Token); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("worker listed certificate access: %v", err)
	}
	allowed, err := s.CertificateAuthorized(ctx, project.ID, fingerprint)
	if err != nil || !allowed {
		t.Fatalf("certificate authorization: %v %v", allowed, err)
	}
	other, otherOwner, err := s.CreateProject(ctx, "other", "other owner")
	if err != nil {
		t.Fatal(err)
	}
	allowed, err = s.CertificateAuthorized(ctx, other.ID, fingerprint)
	if err != nil || allowed {
		t.Fatalf("certificate grant crossed projects: %v %v", allowed, err)
	}
	if err = s.RevokeCertificateGrant(ctx, project.ID, worker.Token, fingerprint); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("worker revoked certificate grant: %v", err)
	}
	if err = s.RevokeCertificateGrant(ctx, project.ID, owner.Token, fingerprint); err != nil {
		t.Fatalf("owner revoked certificate grant: %v", err)
	}
	allowed, err = s.CertificateAuthorized(ctx, project.ID, fingerprint)
	if err != nil || allowed {
		t.Fatalf("revoked certificate remained authorized: %v %v", allowed, err)
	}
	_ = otherOwner
}

func TestCertificateGrantExpiresAndCanBeRenewed(t *testing.T) {
	s, _, project, owner, worker := setup(t)
	ctx := context.Background()
	fingerprint := strings.Repeat("cd", 32)
	if _, err := s.AddCertificateGrantUntil(ctx, project.ID, worker.Token, fingerprint, time.Now().Add(time.Hour).UnixMilli()); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("worker granted expiring access: %v", err)
	}
	if _, err := s.AddCertificateGrantUntil(ctx, project.ID, owner.Token, fingerprint, time.Now().Add(-time.Second).UnixMilli()); err == nil {
		t.Fatal("accepted an already expired grant")
	}
	expiresAt := time.Now().Add(300 * time.Millisecond).UnixMilli()
	grant, err := s.AddCertificateGrantUntil(ctx, project.ID, owner.Token, fingerprint, expiresAt)
	if err != nil || grant.ExpiresAt != expiresAt {
		t.Fatalf("expiring grant: %+v %v", grant, err)
	}
	if allowed, err := s.CertificateAuthorized(ctx, project.ID, fingerprint); err != nil || !allowed {
		t.Fatalf("grant did not authorize immediately: %v %v", allowed, err)
	}
	time.Sleep(time.Until(time.UnixMilli(expiresAt)) + 10*time.Millisecond)
	if allowed, err := s.CertificateAuthorized(ctx, project.ID, fingerprint); err != nil || allowed {
		t.Fatalf("expired grant still authorized: %v %v", allowed, err)
	}
	listed, err := s.ListCertificateGrants(ctx, project.ID, owner.Token)
	if err != nil || len(listed) != 1 || listed[0].ExpiresAt != expiresAt {
		t.Fatalf("expired grant not visible for renewal: %+v %v", listed, err)
	}
	renewed, err := s.AddCertificateGrantUntil(ctx, project.ID, owner.Token, fingerprint, time.Now().Add(time.Hour).UnixMilli())
	if err != nil || renewed.ExpiresAt <= expiresAt {
		t.Fatalf("grant renewal: %+v %v", renewed, err)
	}
	if allowed, err := s.CertificateAuthorized(ctx, project.ID, fingerprint); err != nil || !allowed {
		t.Fatalf("renewed grant denied: %v %v", allowed, err)
	}
}

func TestRemotePrincipalInviteGrantsOnlySelectedProject(t *testing.T) {
	s, _, project, owner, _ := setup(t)
	ctx := context.Background()
	other, _, err := s.CreateProject(ctx, "private project", "other owner")
	if err != nil {
		t.Fatal(err)
	}
	preparedProject, credential, err := coord.PrepareCredential(project.ID)
	if err != nil || preparedProject != project.ID {
		t.Fatalf("prepare principal: %v", err)
	}
	fingerprint := strings.Repeat("b7", 32)
	expiresAt := time.Now().Add(time.Hour).UnixMilli()
	enrolled, err := s.AddPreparedRemotePrincipal(ctx, project.ID, owner.Token, "Remote worker", "worker", credential, fingerprint, expiresAt)
	if err != nil || enrolled != credential {
		t.Fatalf("atomic remote invite: %+v %v", enrolled, err)
	}
	if _, err = s.WhoAmI(ctx, project.ID, enrolled.Token); err != nil {
		t.Fatalf("project credential was not enrolled: %v", err)
	}
	if allowed, err := s.CertificateAuthorized(ctx, project.ID, fingerprint); err != nil || !allowed {
		t.Fatalf("certificate was not granted to selected project: %v %v", allowed, err)
	}
	if allowed, err := s.CertificateAuthorized(ctx, other.ID, fingerprint); err != nil || allowed {
		t.Fatalf("certificate grant crossed into another project: %v %v", allowed, err)
	}
	if retry, err := s.AddPreparedRemotePrincipal(ctx, project.ID, owner.Token, "Remote worker", "worker", credential, fingerprint, expiresAt); err != nil || retry != credential {
		t.Fatalf("lost-response retry failed: %+v %v", retry, err)
	}
	if err = s.RevokeCertificateGrant(ctx, project.ID, owner.Token, fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AddPreparedRemotePrincipal(ctx, project.ID, owner.Token, "Remote worker", "worker", credential, fingerprint, expiresAt); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("retry restored revoked certificate access: %v", err)
	}
}
