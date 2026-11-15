package coord

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestEnrollmentClientOverTLS(t *testing.T) {
	ctx := context.Background()
	home, err := Open(filepath.Join(t.TempDir(), "home.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer home.Close()
	project, owner, err := home.CreateProject(ctx, "Home", "Owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := home.SetRemoteAccessEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	peer := httptest.NewUnstartedServer(EnrollmentHandler(home, RPCHandlerWithCertificateGrants(home), ca, key))
	peer.TLS = &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: pool}
	peer.StartTLS()
	defer peer.Close()
	invite, code, err := home.CreateRemoteInvitation(ctx, project.ID, owner.Token, "Device", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	bundle := EnrollmentBundle{Invitation: invite, Endpoint: peer.URL, HomeID: home.InstanceID(), ServerCA: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: peer.Certificate().Raw}))}
	dir := t.TempDir()
	recipient, err := OpenDeviceRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { recipient.Close() }()
	pending, err := recipient.AcceptEnrollment(ctx, "default", bundle, code)
	if err != nil || pending.State != "pending" {
		t.Fatalf("accept state %q: %v", pending.State, err)
	}
	if entries, err := recipient.List(ctx); err != nil || len(entries) != 0 {
		t.Fatalf("pending connection count %d: %v", len(entries), err)
	}
	other, err := OpenDeviceRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.AcceptEnrollment(ctx, "default", bundle, code); err == nil {
		t.Fatal("second device reused the code")
	}
	if err := home.DecideRemoteInvitation(ctx, project.ID, owner.Token, invite.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	if err := recipient.Close(); err != nil {
		t.Fatal(err)
	}
	recipient, err = OpenDeviceRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	enrolled, err := recipient.AcceptEnrollment(ctx, "default", bundle, code)
	if err != nil || enrolled.State != "enrolled" || enrolled.Fingerprint != pending.Fingerprint {
		t.Fatalf("resume state %q: %v", enrolled.State, err)
	}
	c, token, err := recipient.Resolve(ctx, "default", project.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	peerClient, err := NewRemote(c.Endpoint, c.CAFile, c.CertFile, c.KeyFile, "")
	if err != nil {
		t.Fatal(err)
	}
	defer peerClient.CloseIdleConnections()
	who, err := peerClient.WhoAmI(ctx, project.ID, token)
	if err != nil || who.ID != c.PrincipalID || who.ProjectRole != "viewer" {
		t.Fatalf("peer read: %v", err)
	}
	if _, err := peerClient.SendMessage(ctx, project.ID, token, "", "", "Viewer", "Denied"); err == nil {
		t.Fatal("viewer could send")
	}
	if err := home.RevokePrincipal(ctx, project.ID, owner.Token, c.PrincipalID); err != nil {
		t.Fatal(err)
	}
	if _, err := recipient.AcceptEnrollment(ctx, "default", bundle, code); err == nil {
		t.Fatal("retry restored revoked access")
	}
}
