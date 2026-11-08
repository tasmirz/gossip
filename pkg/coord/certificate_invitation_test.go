package coord_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tasmirz/gossip/pkg/coord"
)

func TestCertificateInvitationIsBoundSingleUseAndProjectScoped(t *testing.T) {
	s, _, project, owner, worker := setup(t)
	ctx := context.Background()
	id, secret, err := coord.PrepareCertificateInvitation()
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := strings.Repeat("ab", 32)
	otherFingerprint := strings.Repeat("cd", 32)
	expiresAt := time.Now().Add(time.Hour).UnixMilli()
	if _, err := s.AddPreparedCertificateInvitation(ctx, project.ID, worker.Token, id, secret, fingerprint, expiresAt); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("worker created invitation: %v", err)
	}
	invite, err := s.AddPreparedCertificateInvitation(ctx, project.ID, owner.Token, id, secret, fingerprint, expiresAt)
	if err != nil || invite.ID != id || invite.ExpectedFingerprint != fingerprint {
		t.Fatalf("owner invitation: %+v %v", invite, err)
	}
	if repeated, err := s.AddPreparedCertificateInvitation(ctx, project.ID, owner.Token, id, secret, fingerprint, expiresAt); err != nil || repeated != invite {
		t.Fatalf("idempotent creation: %+v %v", repeated, err)
	}
	if _, err := s.AddPreparedCertificateInvitation(ctx, project.ID, owner.Token, id, secret, otherFingerprint, expiresAt); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("changed invitation accepted: %v", err)
	}
	listed, err := s.ListCertificateInvitations(ctx, project.ID, owner.Token)
	if err != nil || len(listed) != 1 {
		t.Fatalf("owner list: %+v %v", listed, err)
	}
	encoded, _ := json.Marshal(listed)
	if strings.Contains(string(encoded), secret) {
		t.Fatal("invitation secret leaked through list")
	}
	if _, err := s.ListCertificateInvitations(ctx, project.ID, worker.Token); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("worker listed invitations: %v", err)
	}
	ownerEvents, err := s.ReadEvents(ctx, project.ID, owner.Token, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range ownerEvents {
		if event.Kind == "certificate.invitation_created" && event.EntityID == id {
			if _, err := s.AcknowledgeEvents(ctx, project.ID, worker.Token, event.Sequence); !errors.Is(err, coord.ErrNotFound) {
				t.Fatalf("worker acknowledged hidden certificate audit event: %v", err)
			}
		}
	}
	workerEvents, err := s.RecentEvents(ctx, project.ID, worker.Token, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range workerEvents {
		if strings.HasPrefix(event.Kind, "certificate.") {
			t.Fatalf("certificate access audit leaked to worker: %+v", event)
		}
	}
	if _, err := s.RedeemCertificateInvitation(ctx, project.ID, id, strings.Repeat("ef", 32), fingerprint); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("wrong secret redeemed invitation: %v", err)
	}
	if _, err := s.RedeemCertificateInvitation(ctx, project.ID, id, secret, otherFingerprint); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("wrong certificate redeemed invitation: %v", err)
	}
	otherProject, _, err := s.CreateProject(ctx, "other", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemCertificateInvitation(ctx, otherProject.ID, id, secret, fingerprint); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("invitation crossed project: %v", err)
	}
	first, err := s.RedeemCertificateInvitation(ctx, project.ID, id, secret, fingerprint)
	if err != nil || first.ProjectID != project.ID || first.Fingerprint != fingerprint {
		t.Fatalf("redemption: %+v %v", first, err)
	}
	if repeated, err := s.RedeemCertificateInvitation(ctx, project.ID, id, secret, fingerprint); err != nil || repeated != first {
		t.Fatalf("same certificate retry: %+v %v", repeated, err)
	}
	if _, err := s.RedeemCertificateInvitation(ctx, project.ID, id, secret, otherFingerprint); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("second certificate reused invitation: %v", err)
	}
	if err := s.RevokeCertificateGrant(ctx, project.ID, owner.Token, fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemCertificateInvitation(ctx, project.ID, id, secret, fingerprint); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("retry restored revoked grant: %v", err)
	}
}

func TestCertificateInvitationExpiryAndRevocation(t *testing.T) {
	s, _, project, owner, _ := setup(t)
	ctx := context.Background()
	id, secret, err := coord.PrepareCertificateInvitation()
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := strings.Repeat("ab", 32)
	expiresAt := time.Now().Add(200 * time.Millisecond).UnixMilli()
	if _, err := s.AddPreparedCertificateInvitation(ctx, project.ID, owner.Token, id, secret, "", expiresAt); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(time.UnixMilli(expiresAt)) + 10*time.Millisecond)
	if _, err := s.RedeemCertificateInvitation(ctx, project.ID, id, secret, fingerprint); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("expired invitation redeemed: %v", err)
	}
	id2, secret2, err := coord.PrepareCertificateInvitation()
	if err != nil {
		t.Fatal(err)
	}
	expiry2 := time.Now().Add(time.Hour).UnixMilli()
	if _, err := s.AddPreparedCertificateInvitation(ctx, project.ID, owner.Token, id2, secret2, "", expiry2); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeCertificateInvitation(ctx, project.ID, owner.Token, id2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPreparedCertificateInvitation(ctx, project.ID, owner.Token, id2, secret2, "", expiry2); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("revoked invitation was recreated: %v", err)
	}
	if _, err := s.RedeemCertificateInvitation(ctx, project.ID, id2, secret2, fingerprint); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("revoked invitation redeemed: %v", err)
	}
	secondOwner, err := s.AddPrincipal(ctx, project.ID, owner.Token, "second owner", "owner")
	if err != nil {
		t.Fatal(err)
	}
	id3, secret3, err := coord.PrepareCertificateInvitation()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPreparedCertificateInvitation(ctx, project.ID, owner.Token, id3, secret3, "", time.Now().Add(time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokePrincipal(ctx, project.ID, secondOwner.Token, owner.PrincipalID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemCertificateInvitation(ctx, project.ID, id3, secret3, fingerprint); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("revoked inviter's invitation redeemed: %v", err)
	}
}
