package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/tasmirz/gossip/pkg/coord"
	"path/filepath"
	"testing"
)

func TestCoordDefaultOutboxPathIsStableAndCredentialScoped(t *testing.T) {
	credential := coordCredential{ProjectID: "project-1", PrincipalID: "member-1", Token: "private-token-value"}
	first := coordDefaultOutboxPath(credential)
	if first == "" || filepath.Clean(first) != first || strings.Contains(first, credential.Token) {
		t.Fatalf("default outbox path is unsafe or exposes credential material: %q", first)
	}
	if again := coordDefaultOutboxPath(credential); again != first {
		t.Fatalf("default outbox path is not stable: %q != %q", first, again)
	}
	otherProject := credential
	otherProject.ProjectID = "project-2"
	if other := coordDefaultOutboxPath(otherProject); other == first {
		t.Fatal("different project credentials share a default outbox")
	}
	otherMember := credential
	otherMember.PrincipalID = "member-2"
	if other := coordDefaultOutboxPath(otherMember); other == first {
		t.Fatal("different member credentials share a default outbox")
	}
}

func TestCoordBootstrapKeyWritesPrivateOneTimeKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bootstrap.key")
	if err := runCoordBootstrapKey([]string{"--key-file", path}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("bootstrap key permissions are %o", info.Mode().Perm())
	}
	key, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.TrimSpace(string(key))) < 32 {
		t.Fatal("bootstrap key is too short")
	}
	if err = runCoordBootstrapKey([]string{"--key-file", path}); err == nil {
		t.Fatal("bootstrap key command overwrote an existing key")
	}
}

func TestCoordRemoteAccessCommands(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := coord.Open(filepath.Join(dir, "home.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, owner, err := store.CreateProject(ctx, "remote access", "owner")
	if err != nil {
		t.Fatal(err)
	}
	key := func() *ecdsa.PrivateKey {
		t.Helper()
		k, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if e != nil {
			t.Fatal(e)
		}
		return k
	}
	now := time.Now()
	caKey := key()
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "coord test CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	issue := func(serial int64, name string, usage x509.ExtKeyUsage) ([]byte, []byte) {
		t.Helper()
		privateKey := key()
		template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
		if usage == x509.ExtKeyUsageServerAuth {
			template.DNSNames = []string{"localhost"}
			template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		}
		der, e := x509.CreateCertificate(rand.Reader, template, ca, &privateKey.PublicKey, caKey)
		if e != nil {
			t.Fatal(e)
		}
		keyDER, e := x509.MarshalPKCS8PrivateKey(privateKey)
		if e != nil {
			t.Fatal(e)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	}
	serverPEM, serverKey := issue(2, "coord server", x509.ExtKeyUsageServerAuth)
	adminPEM, adminKey := issue(3, "coord admin", x509.ExtKeyUsageClientAuth)
	newUserPEM, newUserKey := issue(4, "new user", x509.ExtKeyUsageClientAuth)
	serverCert, err := tls.X509KeyPair(serverPEM, serverKey)
	if err != nil {
		t.Fatal(err)
	}
	adminBlock, _ := pem.Decode(adminPEM)
	adminCert, err := x509.ParseCertificate(adminBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	adminPin := sha256.Sum256(adminCert.Raw)
	if _, err = store.AddCertificateGrant(ctx, project.ID, owner.Token, hex.EncodeToString(adminPin[:])); err != nil {
		t.Fatal(err)
	}
	if err = store.SetRemoteAccessEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	clientRoots := x509.NewCertPool()
	clientRoots.AddCert(ca)
	ts := httptest.NewUnstartedServer(coord.RPCHandlerWithCertificateGrants(store))
	ts.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{serverCert}, ClientCAs: clientRoots, ClientAuth: tls.RequireAndVerifyClientCert}
	ts.StartTLS()
	defer ts.Close()
	write := func(name string, data []byte) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if e := os.WriteFile(path, data, 0600); e != nil {
			t.Fatal(e)
		}
		return path
	}
	caFile := write("ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
	adminCertFile := write("admin.pem", adminPEM)
	adminKeyFile := write("admin-key.pem", adminKey)
	ownerFile := write("owner.json", mustJSON(t, coordCredential{ProjectID: project.ID, PrincipalID: owner.PrincipalID, Token: owner.Token}))
	common := []string{"--remote", ts.URL, "--tls-ca", caFile, "--tls-cert", adminCertFile, "--tls-key", adminKeyFile, "--owner-credential", ownerFile}
	newCredentialFile := filepath.Join(dir, "new-user.json")
	newUserCertFile := write("new-user.pem", newUserPEM)
	addArgs := append(append([]string{}, common...), "--name", "New user", "--role", "worker", "--credential-file", newCredentialFile, "--client-certificate", newUserCertFile, "--access-expires-in", "1h")
	if err = runCoordAdd(addArgs); err != nil {
		t.Fatalf("remote enrollment command: %v", err)
	}
	newCredential, err := readCoordCredential(newCredentialFile)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := store.WhoAmI(ctx, project.ID, newCredential.Token)
	if err != nil || principal.ID != newCredential.PrincipalID || principal.Role != "worker" {
		t.Fatalf("remote enrollment result: %+v %v", principal, err)
	}
	newUserBlock, _ := pem.Decode(newUserPEM)
	newUserCert, err := x509.ParseCertificate(newUserBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	newUserPin := sha256.Sum256(newUserCert.Raw)
	newUserFingerprint := hex.EncodeToString(newUserPin[:])
	if allowed, checkErr := store.CertificateAuthorized(ctx, project.ID, newUserFingerprint); checkErr != nil || !allowed {
		t.Fatalf("combined remote invite did not grant selected project: %t %v", allowed, checkErr)
	}
	otherProject, _, err := store.CreateProject(ctx, "isolated project", "other owner")
	if err != nil {
		t.Fatal(err)
	}
	if allowed, checkErr := store.CertificateAuthorized(ctx, otherProject.ID, newUserFingerprint); checkErr != nil || allowed {
		t.Fatalf("combined remote invite granted another project: %t %v", allowed, checkErr)
	}
	if _, checkErr := store.WhoAmI(ctx, otherProject.ID, newCredential.Token); !errors.Is(checkErr, coord.ErrUnauthorized) {
		t.Fatalf("project credential crossed projects: %v", checkErr)
	}
	accessCommon := []string{"--remote", ts.URL, "--tls-ca", caFile, "--tls-cert", adminCertFile, "--tls-key", adminKeyFile, "--credential-file", ownerFile}
	grantArgs := append(append([]string{}, accessCommon...), "--action", "grant", "--certificate", newUserCertFile)
	if err = runCoordAccess(grantArgs); err != nil {
		t.Fatalf("remote certificate grant command: %v", err)
	}
	if allowed, err := store.CertificateAuthorized(ctx, project.ID, newUserFingerprint); err != nil || !allowed {
		t.Fatalf("remote grant did not authorize client certificate: %t %v", allowed, err)
	}
	revokeArgs := append(append([]string{}, accessCommon...), "--action", "revoke", "--certificate", newUserCertFile)
	if err = runCoordAccess(revokeArgs); err != nil {
		t.Fatalf("remote certificate revoke command: %v", err)
	}
	if allowed, err := store.CertificateAuthorized(ctx, project.ID, newUserFingerprint); err != nil || allowed {
		t.Fatalf("remote revoke retained certificate access: %t %v", allowed, err)
	}
	inviteFile := filepath.Join(dir, "pairing.json")
	createPairing := []string{"--remote", ts.URL, "--tls-ca", caFile, "--tls-cert", adminCertFile, "--tls-key", adminKeyFile, "--owner-credential", ownerFile, "--action", "create", "--invite-file", inviteFile, "--certificate", newUserCertFile}
	if err = runCoordPair(createPairing); err != nil {
		t.Fatalf("remote certificate invitation: %v", err)
	}
	if info, err := os.Stat(inviteFile); err != nil || info.Mode().Perm()&0077 != 0 {
		t.Fatalf("invitation file permissions: %v %v", info, err)
	}
	if err = runCoordPair(append(createPairing, "--resume")); err != nil {
		t.Fatalf("invitation retry: %v", err)
	}
	newUserKeyFile := write("new-user-key.pem", newUserKey)
	redeemArgs := []string{"--action", "redeem", "--remote", ts.URL, "--tls-ca", caFile, "--tls-cert", newUserCertFile, "--tls-key", newUserKeyFile, "--invite-file", inviteFile}
	if err = runCoordPair(redeemArgs); err != nil {
		t.Fatalf("remote invitation redemption: %v", err)
	}
	if allowed, err := store.CertificateAuthorized(ctx, project.ID, newUserFingerprint); err != nil || !allowed {
		t.Fatalf("redemption did not grant certificate access: %t %v", allowed, err)
	}
	if err = runCoordAccess(revokeArgs); err != nil {
		t.Fatalf("revoke redeemed certificate: %v", err)
	}
	if err = runCoordPair(redeemArgs); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("invitation replay restored revoked access: %v", err)
	}
}

func TestCoordFRPConfigUsesPrivateTCPProxy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frpc.toml")
	if err := runCoordFRP([]string{"--server", "frps.example", "--server-port", "7000", "--token", "frp-secret", "--local-port", "8443", "--remote-port", "18443", "--config-file", path}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Fatalf("FRP config permissions %v", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"type = \"tcp\"", "localPort = 8443", "remotePort = 18443", "transport.useEncryption = true", "auth.token = \"frp-secret\""} {
		if !strings.Contains(string(data), expected) {
			t.Fatalf("FRP config missing %q: %s", expected, data)
		}
	}
	if err = runCoordFRP([]string{"--server", "frps.example", "--token", "frp-secret", "--remote-port", "18443", "--config-file", path}); err == nil {
		t.Fatal("overwrote an existing FRP config")
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCoordCredentialsArePrivateAndNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "coord.db")
	owner := filepath.Join(dir, "owner.json")
	worker := filepath.Join(dir, "worker.json")
	if err := runCoordInit([]string{"--db", db, "--name", "demo", "--owner", "human", "--credential-file", owner}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(owner)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Fatalf("credential permissions %v", info.Mode().Perm())
	}
	if err = runCoordAdd([]string{"--db", db, "--owner-credential", owner, "--name", "worker", "--credential-file", worker}); err != nil {
		t.Fatal(err)
	}
	oc, err := readCoordCredential(owner)
	if err != nil {
		t.Fatal(err)
	}
	wc, err := readCoordCredential(worker)
	if err != nil {
		t.Fatal(err)
	}
	if oc.ProjectID != wc.ProjectID || oc.Token == wc.Token {
		t.Fatal("credential identity mismatch")
	}
	if runtime.GOOS != "windows" {
		permissive := filepath.Join(dir, "permissive.json")
		if err = os.WriteFile(permissive, []byte(`{"project_id":"`+oc.ProjectID+`","principal_id":"`+oc.PrincipalID+`","token":"`+oc.Token+`"}`), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err = readCoordCredential(permissive); err == nil {
			t.Fatal("accepted a credential file readable by other local users")
		}
		linked := filepath.Join(dir, "credential-link.json")
		if err = os.Symlink(owner, linked); err != nil {
			t.Fatal(err)
		}
		if _, err = readCoordCredential(linked); err == nil {
			t.Fatal("accepted a credential through a symbolic link")
		}
	}
	if err = runCoordInit([]string{"--db", db, "--name", "other", "--owner", "human", "--credential-file", owner}); err == nil {
		t.Fatal("overwrote credential")
	}
}

func TestCoordMigrateRunsReadOnlyAuditByDefault(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"project_name":"demo","agents":{},"goals":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = write
	commandErr := runCoordMigrate([]string{"--legacy-dir", dir})
	_ = write.Close()
	os.Stdout = previous
	if commandErr != nil {
		t.Fatal(commandErr)
	}
	data, err := io.ReadAll(read)
	_ = read.Close()
	if err != nil {
		t.Fatal(err)
	}
	var report coord.MigrationAudit
	if err = json.Unmarshal(data, &report); err != nil {
		t.Fatalf("invalid migration audit output: %s: %v", data, err)
	}
	if !report.Ready || report.Counts["agents"] != 0 {
		t.Fatalf("unexpected migration audit: %+v", report)
	}
	if err = runCoordMigrate([]string{"--legacy-dir", dir, "--apply"}); err == nil {
		t.Fatal("migration apply accepted missing owner credential")
	}
}

func TestCoordMigrateCLIImportsHistoryAfterDryRun(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "legacy")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "state.json"), []byte(`{"project_name":"demo","agents":{},"goals":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(dir, "coord.db")
	owner := filepath.Join(dir, "owner.json")
	if err := runCoordInit([]string{"--db", db, "--name", "demo", "--owner", "owner", "--credential-file", owner}); err != nil {
		t.Fatal(err)
	}
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = write
	commandErr := runCoordMigrate([]string{"--legacy-dir", source, "--db", db, "--credential-file", owner, "--apply"})
	_ = write.Close()
	os.Stdout = previous
	if commandErr != nil {
		t.Fatal(commandErr)
	}
	data, err := io.ReadAll(read)
	_ = read.Close()
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Import coord.LegacyImport   `json:"import"`
		Audit  coord.MigrationAudit `json:"audit"`
	}
	if err = json.Unmarshal(data, &response); err != nil {
		t.Fatalf("invalid migration output: %s: %v", data, err)
	}
	if response.Import.ID == "" || !response.Audit.Ready || response.Import.Existing {
		t.Fatalf("unexpected import result: %+v", response)
	}
	store, err := coord.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	credential, err := readCoordCredential(owner)
	if err != nil {
		t.Fatal(err)
	}
	imports, err := store.ListLegacyImports(context.Background(), credential.ProjectID, credential.Token)
	if err != nil || len(imports) != 1 || imports[0].ID != response.Import.ID {
		t.Fatalf("CLI import was not persisted: %+v %v", imports, err)
	}
}

func TestCoordAccessCLIGrantsAndRevokesCertificate(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "coord.db")
	ownerFile := filepath.Join(dir, "owner.json")
	workerFile := filepath.Join(dir, "worker.json")
	if err := runCoordInit([]string{"--db", db, "--name", "demo", "--owner", "owner", "--credential-file", ownerFile}); err != nil {
		t.Fatal(err)
	}
	if err := runCoordAdd([]string{"--db", db, "--owner-credential", ownerFile, "--name", "worker", "--role", "worker", "--credential-file", workerFile}); err != nil {
		t.Fatal(err)
	}
	owner, err := readCoordCredential(ownerFile)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := strings.Repeat("cd", 32)
	if err = runCoordAccess([]string{"--db", db, "--credential-file", ownerFile, "--action", "grant", "--fingerprint", fingerprint}); err != nil {
		t.Fatal(err)
	}
	s, err := coord.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := s.CertificateAuthorized(context.Background(), owner.ProjectID, fingerprint)
	if err != nil || !allowed {
		t.Fatalf("CLI grant not stored: %v %v", allowed, err)
	}
	worker, err := readCoordCredential(workerFile)
	if err != nil {
		t.Fatal(err)
	}
	if err = runCoordAccess([]string{"--db", db, "--credential-file", workerFile, "--action", "revoke", "--fingerprint", fingerprint}); err == nil {
		t.Fatal("worker revoked certificate access")
	}
	if err = runCoordAccess([]string{"--db", db, "--credential-file", ownerFile, "--action", "revoke", "--fingerprint", fingerprint}); err != nil {
		t.Fatal(err)
	}
	allowed, err = s.CertificateAuthorized(context.Background(), worker.ProjectID, fingerprint)
	if err != nil || allowed {
		t.Fatalf("CLI revoke not applied: %v %v", allowed, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCoordAdminViaLocalDaemon(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(dir, "db")
	sock := filepath.Join(dir, "daemon.sock")
	s, err := coord.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- coord.ServeLocal(ctx, s, sock) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if info, e := os.Lstat(sock); e == nil && info.Mode().Perm()&0077 == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	owner := filepath.Join(dir, "owner.json")
	worker := filepath.Join(dir, "worker.json")
	if err = runCoordInit([]string{"--socket", sock, "--name", "demo", "--owner", "human", "--credential-file", owner}); err != nil {
		t.Fatal(err)
	}
	if err = runCoordAdd([]string{"--socket", sock, "--owner-credential", owner, "--name", "worker", "--credential-file", worker}); err != nil {
		t.Fatal(err)
	}
	wc, err := readCoordCredential(worker)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.WhoAmI(ctx, wc.ProjectID, wc.Token); err != nil {
		t.Fatal(err)
	}
	fingerprint := strings.Repeat("ef", 32)
	if err = runCoordAccess([]string{"--socket", sock, "--credential-file", owner, "--action", "grant", "--fingerprint", fingerprint}); err != nil {
		t.Fatal(err)
	}
	allowed, err := s.CertificateAuthorized(ctx, wc.ProjectID, fingerprint)
	if err != nil || !allowed {
		t.Fatalf("socket grant: %v %v", allowed, err)
	}
	if err = runCoordAccess([]string{"--socket", sock, "--credential-file", owner, "--action", "revoke", "--fingerprint", fingerprint}); err != nil {
		t.Fatal(err)
	}
	allowed, err = s.CertificateAuthorized(ctx, wc.ProjectID, fingerprint)
	if err != nil || allowed {
		t.Fatalf("socket revoke: %v %v", allowed, err)
	}
	unauthorized := filepath.Join(dir, "no.json")
	if err = runCoordAdd([]string{"--socket", sock, "--owner-credential", worker, "--name", "unauthorized", "--credential-file", unauthorized}); err == nil {
		t.Fatal("worker enrolled principal")
	}
	if info, err := os.Stat(unauthorized); err != nil || info.Mode().Perm()&0077 != 0 {
		t.Fatalf("missing private recovery credential: %v", err)
	}
	if err = runCoordAdd([]string{"--socket", sock, "--owner-credential", owner, "--name", "unauthorized", "--credential-file", unauthorized, "--resume"}); err != nil {
		t.Fatal(err)
	}
	if err = runCoordAdd([]string{"--socket", sock, "--owner-credential", owner, "--name", "unauthorized", "--credential-file", unauthorized, "--resume"}); err != nil {
		t.Fatal(err)
	}
	if err = runCoordInit([]string{"--socket", sock, "--name", "demo", "--owner", "human", "--credential-file", owner, "--resume"}); err != nil {
		t.Fatal(err)
	}
	if err = runCoordInit([]string{"--socket", sock, "--name", "different", "--owner", "human", "--credential-file", owner, "--resume"}); err == nil {
		t.Fatal("changed provisioning input accepted")
	}
	principals, err := s.ListPrincipals(ctx, wc.ProjectID, wc.Token)
	if err != nil || len(principals) != 3 {
		t.Fatalf("duplicate principal: %+v %v", principals, err)
	}
}
