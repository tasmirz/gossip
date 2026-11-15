package coord

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"fmt"
	"math/big"
	"path/filepath"
	"testing"
	"time"
)

func TestEnrollmentRequiresApprovalAndBoundDevice(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "home.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, owner, err := s.CreateProject(ctx, "Home", "Owner")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetRemoteAccessEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	inv, code, err := s.CreateRemoteInvitation(ctx, p.ID, owner.Token, "Device", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	newCSR := func() []byte {
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
		if err != nil {
			t.Fatal(err)
		}
		return der
	}
	csr := newCSR()
	pub, caKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, pub, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err = x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := s.AcceptRemoteInvitation(ctx, p.ID, inv.ID, code, csr)
	if err != nil || pending.State != "pending" {
		t.Fatalf("accept: %+v %v", pending, err)
	}
	if _, err = s.AcceptRemoteInvitation(ctx, p.ID, inv.ID, code, newCSR()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("second device reused invite: %v", err)
	}
	if _, err = s.IssueEnrollmentCertificate(ctx, p.ID, inv.ID, code, csr, ca, caKey); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("issued before approval: %v", err)
	}
	if err = s.DecideRemoteInvitation(ctx, p.ID, owner.Token, inv.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	cert, err := s.IssueEnrollmentCertificate(ctx, p.ID, inv.ID, code, csr, ca, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(cert)
	if err != nil {
		t.Fatal(err)
	}
	_, cred, err := PrepareCredential(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinalizeRemoteInvitation(ctx, p.ID, inv.ID, ca, cred); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong certificate finalized: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err = s.FinalizeRemoteInvitation(ctx, p.ID, inv.ID, leaf, cred); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.WhoAmI(ctx, p.ID, cred.Token); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SendMessage(ctx, p.ID, cred.Token, "", "", "subject", "body"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("viewer write: %v", err)
	}
	if err = s.RevokeCertificateGrant(ctx, p.ID, owner.Token, fmt.Sprintf("%x", sha256.Sum256(cert))); err != nil {
		t.Fatal(err)
	}
	if err = s.FinalizeRemoteInvitation(ctx, p.ID, inv.ID, leaf, cred); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("retry restored revoked access: %v", err)
	}
}
