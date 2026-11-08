package coord_test

import (
	"bytes"
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
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tasmirz/gossip/pkg/coord"
)

// multiWorktreeTLSFixture holds resources for multi-worktree mTLS acceptance tests.
type multiWorktreeTLSFixture struct {
	Store                 *coord.Store
	DBPath                string
	Project               coord.Project
	Owner                 coord.Credential
	Worker                coord.Credential
	Server                *httptest.Server
	URL                   string
	CAFile                string
	ServerCertFile        string
	ServerKeyFile         string
	ClientCertFile        string
	ClientKeyFile         string
	ClientCertFingerprint string
	OtherCertFile         string
	OtherKeyFile          string
	OtherCertFingerprint  string
	Remote                *coord.Remote
	OtherRemote           *coord.Remote
	CertPool              *x509.CertPool
}

func initMultiWorktreeGitRepos(t *testing.T) (string, string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable is not available")
	}
	tempDir := t.TempDir()
	mainRepo := filepath.Join(tempDir, "main-repo")
	worktreeA := filepath.Join(tempDir, "worktree-a")
	worktreeB := filepath.Join(tempDir, "worktree-b")

	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s failed: %s (%v)", args, dir, string(output), err)
		}
	}

	if err := os.MkdirAll(mainRepo, 0700); err != nil {
		t.Fatal(err)
	}
	runGit(mainRepo, "init", "--initial-branch=main")

	mainSource := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"Multi-worktree coordinator baseline\")\n}\n"
	if err := os.WriteFile(filepath.Join(mainRepo, "main.go"), []byte(mainSource), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(mainRepo, "add", "main.go")
	runGit(mainRepo, "-c", "user.name=Acceptance Tester", "-c", "user.email=tester@example.invalid", "commit", "-m", "initial commit")

	runGit(mainRepo, "worktree", "add", "--detach", worktreeA)
	runGit(mainRepo, "worktree", "add", "--detach", worktreeB)

	return mainRepo, worktreeA, worktreeB
}

func setupMultiWorktreeTLS(t *testing.T) *multiWorktreeTLSFixture {
	t.Helper()
	now := time.Now()
	keyGen := func() *ecdsa.PrivateKey {
		t.Helper()
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}

	caKey := keyGen()
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Multi-Worktree Acceptance CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	tempDir := t.TempDir()
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	caFile := filepath.Join(tempDir, "ca.crt")
	if err := os.WriteFile(caFile, caPEM, 0600); err != nil {
		t.Fatal(err)
	}

	issueCert := func(serial int64, usage x509.ExtKeyUsage, prefix string) (string, string, string, tls.Certificate) {
		t.Helper()
		k := keyGen()
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			Subject:      pkix.Name{CommonName: prefix},
			NotBefore:    now.Add(-time.Hour),
			NotAfter:     now.Add(24 * time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{usage},
			IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &k.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			t.Fatal(err)
		}
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

		certPath := filepath.Join(tempDir, prefix+".crt")
		keyPath := filepath.Join(tempDir, prefix+".key")
		if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
			t.Fatal(err)
		}
		tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			t.Fatal(err)
		}
		pin := sha256.Sum256(der)
		fingerprint := hex.EncodeToString(pin[:])
		return certPath, keyPath, fingerprint, tlsCert
	}

	serverCertFile, serverKeyFile, _, serverTLSCert := issueCert(2, x509.ExtKeyUsageServerAuth, "server")
	clientCertFile, clientKeyFile, clientFingerprint, _ := issueCert(3, x509.ExtKeyUsageClientAuth, "client")
	otherCertFile, otherKeyFile, otherCertFingerprint, _ := issueCert(4, x509.ExtKeyUsageClientAuth, "other")

	dbPath := filepath.Join(tempDir, "coord.db")
	s, err := coord.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	ctx := context.Background()
	p, owner, err := s.CreateProject(ctx, "acceptance-project", "owner")
	if err != nil {
		t.Fatal(err)
	}
	worker, err := s.AddPrincipal(ctx, p.ID, owner.Token, "worker", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PublishPlan(ctx, p.ID, owner.Token, "Initial plan", 0); err != nil {
		t.Fatal(err)
	}
	if err = s.AcknowledgePlan(ctx, p.ID, worker.Token, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AddCertificateGrant(ctx, p.ID, owner.Token, clientFingerprint); err != nil {
		t.Fatal(err)
	}
	if err = s.SetRemoteAccessEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}

	rpcH := coord.RPCHandler(s)
	ts := httptest.NewUnstartedServer(rpcH)
	ts.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{serverTLSCert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}
	ts.StartTLS()
	t.Cleanup(func() { ts.Close() })

	rem, err := coord.NewRemote(ts.URL, caFile, clientCertFile, clientKeyFile, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	otherRem, err := coord.NewRemote(ts.URL, caFile, otherCertFile, otherKeyFile, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}

	return &multiWorktreeTLSFixture{
		Store:                 s,
		DBPath:                dbPath,
		Project:               p,
		Owner:                 owner,
		Worker:                worker,
		Server:                ts,
		URL:                   ts.URL,
		CAFile:                caFile,
		ServerCertFile:        serverCertFile,
		ServerKeyFile:         serverKeyFile,
		ClientCertFile:        clientCertFile,
		ClientKeyFile:         clientKeyFile,
		ClientCertFingerprint: clientFingerprint,
		OtherCertFile:         otherCertFile,
		OtherKeyFile:          otherKeyFile,
		OtherCertFingerprint:  otherCertFingerprint,
		Remote:                rem,
		OtherRemote:           otherRem,
		CertPool:              pool,
	}
}

func makeRawTLSCall(t *testing.T, targetURL, caFile, certFile, keyFile, token, project, operation string, extra map[string]any) (int, string) {
	t.Helper()
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion:   tls.VersionTLS13,
			RootCAs:      pool,
			Certificates: []tls.Certificate{cert},
			ServerName:   "127.0.0.1",
		},
	}
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	payloadMap := map[string]any{
		"operation": operation,
		"project":   project,
	}
	for k, v := range extra {
		payloadMap[k] = v
	}
	bodyBytes, err := json.Marshal(payloadMap)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, targetURL+"/v1/call", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("TLS request failed: %v", err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, strings.TrimSpace(buf.String())
}

// ============================================================================
// Scenario A: Lifecycle & Collaboration
// ============================================================================
// Validates plan acknowledgment fencing, task creation and claim across separate
// workspaces, private threaded messages, review deliverables (diagram_report and
// full_code Git diff capture), review acceptance, dependency DAG cascades,
// and lease renewal / expiration steal.
func TestMultiWorktree_ScenarioA_LifecycleAndCollaboration(t *testing.T) {
	mainRepo, worktreeA, worktreeB := initMultiWorktreeGitRepos(t)
	s, _, p, owner, workerA := setup(t)
	ctx := context.Background()

	workerB, err := s.AddPrincipal(ctx, p.ID, owner.Token, "Worker B", "worker")
	if err != nil {
		t.Fatal(err)
	}

	repo, err := s.CreateRepository(ctx, p.ID, owner.Token, "Shared Repository")
	if err != nil {
		t.Fatal(err)
	}

	obsA, err := coord.InspectGitWorkspace(ctx, worktreeA)
	if err != nil {
		t.Fatal(err)
	}
	obsB, err := coord.InspectGitWorkspace(ctx, worktreeB)
	if err != nil {
		t.Fatal(err)
	}

	wsA, err := s.RegisterWorkspace(ctx, p.ID, workerA.Token, repo.ID, "Worker A Worktree", "node-a", worktreeA, obsA.Branch, obsA.Head, obsA.Dirty)
	if err != nil {
		t.Fatal(err)
	}
	wsB, err := s.RegisterWorkspace(ctx, p.ID, workerB.Token, repo.ID, "Worker B Worktree", "node-b", worktreeB, obsB.Branch, obsB.Head, obsB.Dirty)
	if err != nil {
		t.Fatal(err)
	}

	// Verify workspace privacy: worker B cannot see worker A's local path.
	workspacesVisibleToB, err := s.ListWorkspaces(ctx, p.ID, workerB.Token)
	if err != nil {
		t.Fatal(err)
	}
	for _, ws := range workspacesVisibleToB {
		if ws.ID == wsA.ID && ws.LocalPath != "" {
			t.Fatalf("worker A local path leaked to worker B: %q", ws.LocalPath)
		}
		if ws.ID == wsB.ID && ws.LocalPath != worktreeB {
			t.Fatalf("worker B local path missing for self: %q", ws.LocalPath)
		}
	}

	// Task 1: Research task requiring diagram_report.
	task1, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Architecture Research", "Produce architectural diagrams", "research", coord.ReportContract{
		ReviewType: "diagram_report",
		Formats:    []string{"markdown", "mermaid_class", "mermaid_sequence"},
		Coverage:   []string{"coordination", "isolation"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Task 2: Implementation task requiring full_code, blocked on Task 1.
	task2, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Implementation Feature", "Implement feature in worktree B", "implementation", coord.ReportContract{
		ReviewType: "full_code",
		Formats:    []string{"markdown"},
	}, []string{task1.ID})
	if err != nil {
		t.Fatal(err)
	}

	// Verify Task 2 is initially blocked due to dependency DAG.
	reloadedTask2, err := s.GetTask(ctx, p.ID, owner.Token, task2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloadedTask2.Status != "blocked" {
		t.Fatalf("dependent task should be blocked, got: %s", reloadedTask2.Status)
	}

	// Plan acknowledgment fencing: worker B has not acknowledged plan 1 yet.
	if _, err = s.ClaimTaskInWorkspace(ctx, p.ID, workerB.Token, task1.ID, wsB.ID, time.Minute); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("worker B claimed task without acknowledging plan: %v", err)
	}

	// Worker B acknowledges plan 1.
	if err = s.AcknowledgePlan(ctx, p.ID, workerB.Token, 1); err != nil {
		t.Fatal(err)
	}

	// Private threaded messaging between Worker A and Worker B.
	msg, err := s.SendMessageWithID(ctx, p.ID, workerA.Token, workerB.PrincipalID, "thread-collab", "Work distribution", "I will complete the architecture diagram first.", "req-msg-1")
	if err != nil {
		t.Fatal(err)
	}

	// Verify message visibility: Worker B sees it; Owner does NOT see direct private messages.
	msgsB, err := s.RecentMessages(ctx, p.ID, workerB.Token, 10)
	if err != nil || len(msgsB) != 1 || msgsB[0].ID != msg.ID {
		t.Fatalf("Worker B did not receive private message: %+v (%v)", msgsB, err)
	}
	msgsOwner, err := s.RecentMessages(ctx, p.ID, owner.Token, 10)
	if err != nil || len(msgsOwner) != 0 {
		t.Fatalf("Direct peer message leaked to project owner: %+v", msgsOwner)
	}

	// Worker B acknowledges the message.
	if _, err = s.AcknowledgeMessage(ctx, p.ID, workerB.Token, msg.ID); err != nil {
		t.Fatal(err)
	}
	acks, err := s.MessageAcknowledgments(ctx, p.ID, workerA.Token, msg.ID)
	if err != nil || len(acks) != 1 || acks[0].PrincipalID != workerB.PrincipalID {
		t.Fatalf("Missing receipt acknowledgment for worker A: %+v", acks)
	}

	// Worker A claims Task 1 in worktree A.
	claim1, err := s.ClaimTaskInWorkspace(ctx, p.ID, workerA.Token, task1.ID, wsA.ID, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claim1.Status != "running" || claim1.Attempt != 1 {
		t.Fatalf("unexpected claim state: %+v", claim1)
	}

	// Lease renewal: Worker A renews lease on Task 1.
	renewed1, err := s.RenewLease(ctx, p.ID, workerA.Token, task1.ID, claim1.Attempt, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if renewed1.LeaseUntil <= claim1.LeaseUntil || renewed1.Attempt != claim1.Attempt {
		t.Fatalf("lease renewal failed: renewed=%+v claim=%+v", renewed1, claim1)
	}

	// Worker A submits diagram_report with all 6 required sections and both Mermaid blocks.
	diagramBody := `# Architecture Review

## Review summary
Multi-worktree coordinator design verified across isolated checkouts.

## Scope and evidence
Checked worktree registration, plan fencing, and dependency DAG cascade.

## UML class diagram
` + "```mermaid\nclassDiagram\n  Coordinator --> Workspace\n  Workspace --> GitCheckout\n```" + `

## Sequence diagram
` + "```mermaid\nsequenceDiagram\n  WorkerA->>Coordinator: ClaimTask\n  Coordinator-->>WorkerA: Running\n```" + `

## Findings
Workspaces are strictly isolated and private paths are masked.

## Coverage and limitations
Covers coordination and isolation across local worktrees and container namespaces; physical multi-host requires runbook.
`
	if err = s.RequestReview(ctx, p.ID, workerA.Token, task1.ID, claim1.Attempt); err != nil {
		t.Fatal(err)
	}
	rep1, err := s.SubmitReport(ctx, p.ID, workerA.Token, task1.ID, claim1.Attempt, obsA.Head, diagramBody)
	if err != nil {
		t.Fatal(err)
	}

	// Anti-self-review gate: Worker A cannot accept their own report.
	if err = s.AcceptReport(ctx, p.ID, workerA.Token, rep1.ID, rep1.Digest); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("worker approved their own report: %v", err)
	}

	// Owner accepts Task 1 report.
	if err = s.AcceptReport(ctx, p.ID, owner.Token, rep1.ID, rep1.Digest); err != nil {
		t.Fatal(err)
	}
	reloadedTask1, err := s.GetTask(ctx, p.ID, owner.Token, task1.ID)
	if err != nil || reloadedTask1.Status != "accepted" {
		t.Fatalf("Task 1 not accepted: %+v (%v)", reloadedTask1, err)
	}

	// Dependency DAG cascade: Task 2 must now transition from blocked to ready!
	reloadedTask2, err = s.GetTask(ctx, p.ID, owner.Token, task2.ID)
	if err != nil || reloadedTask2.Status != "ready" {
		t.Fatalf("Task 2 did not unblock to ready after prerequisite accepted: %+v (%v)", reloadedTask2, err)
	}

	// Full-code review deliverable in worktree B:
	// Worker B claims Task 2 in worktree B.
	claim2, err := s.ClaimTaskInWorkspace(ctx, p.ID, workerB.Token, task2.ID, wsB.ID, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	// Worker B writes changes to worktree B.
	featureFile := filepath.Join(worktreeB, "feature.go")
	if err = os.WriteFile(featureFile, []byte("package main\n\nconst FeatureActive = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Also modify main.go
	newMain := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"Multi-worktree coordinator baseline - Feature Active\")\n}\n"
	if err = os.WriteFile(filepath.Join(worktreeB, "main.go"), []byte(newMain), 0600); err != nil {
		t.Fatal(err)
	}

	// Capture real Git diff against attempt baseline.
	diff, err := coord.CaptureGitDiff(ctx, worktreeB, obsB.Head)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "FeatureActive") || !strings.Contains(diff, "diff --git") {
		t.Fatalf("captured diff did not contain expected changes: %s", diff)
	}

	// Save candidate diff.
	cand2, err := s.SaveCandidate(ctx, p.ID, workerB.Token, task2.ID, claim2.Attempt, obsB.Head, diff)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RequestReview(ctx, p.ID, workerB.Token, task2.ID, claim2.Attempt); err != nil {
		t.Fatal(err)
	}
	rep2, err := s.SubmitReport(ctx, p.ID, workerB.Token, task2.ID, claim2.Attempt, obsB.Head, "Implemented feature with verified full-code diff.")
	if err != nil {
		t.Fatal(err)
	}
	if rep2.SourceCandidateID != cand2.ID || rep2.SourceCandidateDigest != cand2.Digest {
		t.Fatalf("candidate not correctly pinned to report: %+v vs %+v", rep2, cand2)
	}

	// Owner accepts Task 2 report.
	if err = s.AcceptReport(ctx, p.ID, owner.Token, rep2.ID, rep2.Digest); err != nil {
		t.Fatal(err)
	}
	reloadedTask2, err = s.GetTask(ctx, p.ID, owner.Token, task2.ID)
	if err != nil || reloadedTask2.Status != "accepted" {
		t.Fatalf("Task 2 not accepted: %+v (%v)", reloadedTask2, err)
	}

	// Lease expiration steal:
	task3, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Quick Task", "Test lease steal", "research", coord.ReportContract{
		ReviewType: "full_code",
		Formats:    []string{"markdown"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Worker A claims Task 3 with a 1-second lease.
	claim3, err := s.ClaimTaskInWorkspace(ctx, p.ID, workerA.Token, task3.ID, wsA.ID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if claim3.Attempt != 1 {
		t.Fatalf("expected attempt 1, got %d", claim3.Attempt)
	}

	// Wait for lease to expire.
	time.Sleep(1100 * time.Millisecond)

	// Worker B steals the expired task.
	claim3Steal, err := s.ClaimTaskInWorkspace(ctx, p.ID, workerB.Token, task3.ID, wsB.ID, 5*time.Minute)
	if err != nil {
		t.Fatalf("Worker B could not steal expired task: %v", err)
	}
	if claim3Steal.Owner != workerB.PrincipalID || claim3Steal.Attempt != 2 {
		t.Fatalf("lease steal failed to increment attempt: %+v", claim3Steal)
	}

	// Stale Worker A attempts lease renewal on attempt 1 -> conflict!
	if _, err = s.RenewLease(ctx, p.ID, workerA.Token, task3.ID, claim3.Attempt, time.Minute); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("stale attempt renewed lease: %v", err)
	}

	_ = mainRepo
}

// ============================================================================
// Scenario B: Coordinator Restart Recovery
// ============================================================================
// Verifies active coordinator shutdown (s.Close()), re-opening the SQLite WAL
// database on disk, and confirming that task states, active leases, messages,
// and plan acknowledgments resume accurately without state corruption or loss.
func TestMultiWorktree_ScenarioB_CoordinatorRestartRecovery(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "restart_test.db")
	ctx := context.Background()

	s1, err := coord.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	p, owner, err := s1.CreateProject(ctx, "restart-project", "owner")
	if err != nil {
		t.Fatal(err)
	}
	workerA, err := s1.AddPrincipal(ctx, p.ID, owner.Token, "Worker A", "worker")
	if err != nil {
		t.Fatal(err)
	}
	workerB, err := s1.AddPrincipal(ctx, p.ID, owner.Token, "Worker B", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s1.PublishPlan(ctx, p.ID, owner.Token, "Plan Revision 1", 0); err != nil {
		t.Fatal(err)
	}
	if err = s1.AcknowledgePlan(ctx, p.ID, workerA.Token, 1); err != nil {
		t.Fatal(err)
	}
	if err = s1.AcknowledgePlan(ctx, p.ID, workerB.Token, 1); err != nil {
		t.Fatal(err)
	}

	repo, err := s1.CreateRepository(ctx, p.ID, owner.Token, "Repo")
	if err != nil {
		t.Fatal(err)
	}
	wsA, err := s1.RegisterWorkspace(ctx, p.ID, workerA.Token, repo.ID, "ws-a", "node-a", "/local/path/a", "main", "commit-1", false)
	if err != nil {
		t.Fatal(err)
	}
	wsB, err := s1.RegisterWorkspace(ctx, p.ID, workerB.Token, repo.ID, "ws-b", "node-b", "/local/path/b", "main", "commit-1", false)
	if err != nil {
		t.Fatal(err)
	}

	// Worker A sends message to Worker B.
	msg, err := s1.SendMessageWithID(ctx, p.ID, workerA.Token, workerB.PrincipalID, "restart-thread", "Pre-restart handoff", "Confirm message persists across restart.", "req-msg-restart")
	if err != nil {
		t.Fatal(err)
	}

	// Task 1: Claimed by Worker A with an active 30-minute lease.
	task1, err := s1.CreateTask(ctx, p.ID, owner.Token, 1, "In-Flight Task", "Active before restart", "implementation", coord.ReportContract{
		ReviewType: "full_code",
		Formats:    []string{"markdown"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	claim1, err := s1.ClaimTaskInWorkspace(ctx, p.ID, workerA.Token, task1.ID, wsA.ID, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claim1.Status != "running" || claim1.Attempt != 1 {
		t.Fatalf("unexpected claim: %+v", claim1)
	}

	// Task 2: Ready for claiming by Worker B after restart.
	task2, err := s1.CreateTask(ctx, p.ID, owner.Token, 1, "Post-Restart Task", "Unclaimed before restart", "research", coord.ReportContract{
		ReviewType: "diagram_report",
		Formats:    []string{"markdown", "mermaid_sequence", "mermaid_class"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Shutdown the coordinator cleanly.
	if err = s1.Close(); err != nil {
		t.Fatalf("failed to close store: %v", err)
	}

	// Re-open coordinator on the same SQLite WAL database.
	s2, err := coord.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to re-open store on disk: %v", err)
	}
	t.Cleanup(func() { s2.Close() })

	// Verify Task 1 active lease and running state survived restart without alteration.
	t1Reloaded, err := s2.GetTask(ctx, p.ID, owner.Token, task1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if t1Reloaded.Status != "running" || t1Reloaded.Owner != workerA.PrincipalID || t1Reloaded.Attempt != 1 || t1Reloaded.LeaseUntil != claim1.LeaseUntil {
		t.Fatalf("Task 1 state corrupted after restart: %+v vs %+v", t1Reloaded, claim1)
	}

	// Verify Worker A can continue renewing lease on the re-opened store.
	renewed, err := s2.RenewLease(ctx, p.ID, workerA.Token, task1.ID, claim1.Attempt, 45*time.Minute)
	if err != nil {
		t.Fatalf("Worker A could not renew lease after coordinator restart: %v", err)
	}
	if renewed.LeaseUntil <= claim1.LeaseUntil {
		t.Fatalf("lease not extended: renewed=%+v claim=%+v", renewed, claim1)
	}

	// Verify message persistence across restart.
	msgsB, err := s2.RecentMessages(ctx, p.ID, workerB.Token, 10)
	if err != nil || len(msgsB) != 1 || msgsB[0].ID != msg.ID || msgsB[0].Body != msg.Body {
		t.Fatalf("messages corrupted or lost across restart: %+v (%v)", msgsB, err)
	}

	// Verify plan acknowledgments survived: Worker B can claim Task 2 without re-acknowledging.
	claim2, err := s2.ClaimTaskInWorkspace(ctx, p.ID, workerB.Token, task2.ID, wsB.ID, 10*time.Minute)
	if err != nil {
		t.Fatalf("Worker B could not claim task after restart: %v", err)
	}
	if claim2.Status != "running" || claim2.Owner != workerB.PrincipalID {
		t.Fatalf("unexpected claim2 state: %+v", claim2)
	}

	// Verify workspace privacy remains enforced after restart.
	wsList, err := s2.ListWorkspaces(ctx, p.ID, workerB.Token)
	if err != nil {
		t.Fatal(err)
	}
	for _, ws := range wsList {
		if ws.ID == wsA.ID && ws.LocalPath != "" {
			t.Fatalf("workspace privacy failed after restart: %+v", ws)
		}
	}
}

// ============================================================================
// Scenario C: Mid-Attempt Principal Revocation
// ============================================================================
// Verifies that revoking a principal mid-attempt immediately resets their
// running task to ready (owner=NULL, lease_until=0), deletes assignments and
// permissions, rejects subsequent calls with ErrUnauthorized, and allows
// another worker in a second workspace to claim the task with incremented attempt.
func TestMultiWorktree_ScenarioC_MidAttemptPrincipalRevocation(t *testing.T) {
	s, _, p, owner, workerA := setup(t)
	ctx := context.Background()

	workerB, err := s.AddPrincipal(ctx, p.ID, owner.Token, "Worker B", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AcknowledgePlan(ctx, p.ID, workerB.Token, 1); err != nil {
		t.Fatal(err)
	}

	repo, err := s.CreateRepository(ctx, p.ID, owner.Token, "Repo")
	if err != nil {
		t.Fatal(err)
	}
	wsA, err := s.RegisterWorkspace(ctx, p.ID, workerA.Token, repo.ID, "ws-a", "node-a", "/tmp/ws-a", "feat-a", "commit-a", false)
	if err != nil {
		t.Fatal(err)
	}
	wsB, err := s.RegisterWorkspace(ctx, p.ID, workerB.Token, repo.ID, "ws-b", "node-b", "/tmp/ws-b", "feat-b", "commit-b", false)
	if err != nil {
		t.Fatal(err)
	}

	task, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Critical Implementation", "Will be revoked mid-flight", "implementation", coord.ReportContract{
		ReviewType: "full_code",
		Formats:    []string{"markdown"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Assign task to Worker A.
	if _, err = s.AssignTask(ctx, p.ID, owner.Token, task.ID, workerA.PrincipalID); err != nil {
		t.Fatal(err)
	}

	// Worker A claims the task.
	claimA, err := s.ClaimTaskInWorkspace(ctx, p.ID, workerA.Token, task.ID, wsA.ID, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimA.Status != "running" || claimA.Owner != workerA.PrincipalID || claimA.Attempt != 1 {
		t.Fatalf("unexpected claim state: %+v", claimA)
	}

	// Owner revokes Worker A mid-attempt.
	if err = s.RevokePrincipal(ctx, p.ID, owner.Token, workerA.PrincipalID); err != nil {
		t.Fatalf("RevokePrincipal failed: %v", err)
	}

	// Assertions on task state after revocation:
	// Task must be reset to status='ready', owner='', lease_until=0.
	taskReloaded, err := s.GetTask(ctx, p.ID, owner.Token, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if taskReloaded.Status != "ready" || taskReloaded.Owner != "" || taskReloaded.LeaseUntil != 0 {
		t.Fatalf("task was not reset to unowned ready after principal revocation: %+v", taskReloaded)
	}

	// Worker A's subsequent calls must all be rejected with ErrUnauthorized.
	if _, err = s.RenewLease(ctx, p.ID, workerA.Token, task.ID, claimA.Attempt, time.Minute); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("revoked worker renewed lease: %v", err)
	}
	if _, err = s.SaveCandidate(ctx, p.ID, workerA.Token, task.ID, claimA.Attempt, wsA.Head, "diff..."); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("revoked worker saved candidate: %v", err)
	}
	if _, err = s.SubmitReport(ctx, p.ID, workerA.Token, task.ID, claimA.Attempt, wsA.Head, "report..."); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("revoked worker submitted report: %v", err)
	}
	if _, err = s.SendMessage(ctx, p.ID, workerA.Token, workerB.PrincipalID, "thread", "subject", "body"); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("revoked worker sent message: %v", err)
	}
	if _, err = s.ClaimTaskInWorkspace(ctx, p.ID, workerA.Token, task.ID, wsA.ID, time.Minute); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("revoked worker claimed task: %v", err)
	}

	// Worker B in Workspace B claims the re-readied task.
	claimB, err := s.ClaimTaskInWorkspace(ctx, p.ID, workerB.Token, task.ID, wsB.ID, 10*time.Minute)
	if err != nil {
		t.Fatalf("Worker B could not claim task after Worker A was revoked: %v", err)
	}
	// Assert attempt counter is incremented to 2, fencing Worker A's attempt 1.
	if claimB.Attempt != 2 || claimB.Owner != workerB.PrincipalID || claimB.Status != "running" {
		t.Fatalf("Worker B claim unexpected: %+v", claimB)
	}

	// Worker B completes the task successfully.
	candB, err := s.SaveCandidate(ctx, p.ID, workerB.Token, task.ID, claimB.Attempt, wsB.Head, "diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1 +1 @@\n-old\n+new\n")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RequestReview(ctx, p.ID, workerB.Token, task.ID, claimB.Attempt); err != nil {
		t.Fatal(err)
	}
	repB, err := s.SubmitReport(ctx, p.ID, workerB.Token, task.ID, claimB.Attempt, wsB.Head, "Completed successfully by worker B")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AcceptReport(ctx, p.ID, owner.Token, repB.ID, repB.Digest); err != nil {
		t.Fatal(err)
	}

	finalTask, err := s.GetTask(ctx, p.ID, owner.Token, task.ID)
	if err != nil || finalTask.Status != "accepted" {
		t.Fatalf("final task state not accepted: %+v", finalTask)
	}
	_ = candB
}

// ============================================================================
// Scenario D: Certificate Revocation vs Principal Revocation
// ============================================================================
// Verifies that revoking a client certificate grant immediately cuts off TLS RPC
// calls with HTTP 403 Forbidden, while leaving the project credential valid locally.
// Contrasts with principal revocation which invalidates the token itself.
func TestMultiWorktree_ScenarioD_CertificateRevocationVsPrincipalRevocation(t *testing.T) {
	fixture := setupMultiWorktreeTLS(t)
	ctx := context.Background()

	// 1. Initial state: Remote worker connects over mTLS and reads current plan.
	plan, err := fixture.Remote.CurrentPlan(ctx, fixture.Project.ID, fixture.Worker.Token)
	if err != nil || plan.Revision != 1 {
		t.Fatalf("initial remote call failed: %+v (%v)", plan, err)
	}

	// Verify raw HTTP status is 200.
	status, _ := makeRawTLSCall(t, fixture.URL, fixture.CAFile, fixture.ClientCertFile, fixture.ClientKeyFile, fixture.Worker.Token, fixture.Project.ID, "plan.read", nil)
	if status != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", status)
	}

	// 2. Owner revokes ONLY the certificate grant.
	if err = fixture.Store.RevokeCertificateGrant(ctx, fixture.Project.ID, fixture.Owner.Token, fixture.ClientCertFingerprint); err != nil {
		t.Fatalf("RevokeCertificateGrant failed: %v", err)
	}

	// 3. Subsequent TLS RPC calls with that certificate must fail immediately with HTTP 403 Forbidden.
	statusRevoked, bodyRevoked := makeRawTLSCall(t, fixture.URL, fixture.CAFile, fixture.ClientCertFile, fixture.ClientKeyFile, fixture.Worker.Token, fixture.Project.ID, "plan.read", nil)
	if statusRevoked != http.StatusForbidden {
		t.Fatalf("expected HTTP 403 Forbidden for revoked certificate grant, got %d (body: %s)", statusRevoked, bodyRevoked)
	}
	if !strings.Contains(bodyRevoked, "certificate is not authorized") {
		t.Fatalf("unexpected 403 body: %s", bodyRevoked)
	}

	// 4. Principal token remains valid locally in SQLite and for local calls.
	valid, err := fixture.Store.CredentialValid(ctx, fixture.Project.ID, fixture.Worker.Token)
	if err != nil || !valid {
		t.Fatalf("worker principal token should remain valid in store after certificate revocation: %v (%v)", valid, err)
	}
	localPlan, err := fixture.Store.CurrentPlan(ctx, fixture.Project.ID, fixture.Worker.Token)
	if err != nil || localPlan.Revision != 1 {
		t.Fatalf("local store call with worker token failed after certificate revocation: %v", err)
	}

	// 5. Contrast with Principal Revocation: revoking principal invalidates the token itself.
	if err = fixture.Store.RevokePrincipal(ctx, fixture.Project.ID, fixture.Owner.Token, fixture.Worker.PrincipalID); err != nil {
		t.Fatalf("RevokePrincipal failed: %v", err)
	}
	validAfterPrincipalRevoke, err := fixture.Store.CredentialValid(ctx, fixture.Project.ID, fixture.Worker.Token)
	if validAfterPrincipalRevoke {
		t.Fatal("worker principal token should be invalid after RevokePrincipal")
	}
	if _, err = fixture.Store.CurrentPlan(ctx, fixture.Project.ID, fixture.Worker.Token); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized from store after principal revocation, got: %v", err)
	}
}

// ============================================================================
// Scenario E: Project Isolation Boundary
// ============================================================================
// Verifies that:
// 1. A token from Project A cannot access, read, or mutate Project B (HTTP 401).
// 2. A certificate registered only for Project A cannot call Project B over TLS RPC (HTTP 403).
// 3. Project B state remains completely unmodified.
func TestMultiWorktree_ScenarioE_ProjectIsolationBoundary(t *testing.T) {
	fixture := setupMultiWorktreeTLS(t)
	ctx := context.Background()

	// Create Project 2 in the same coordinator store.
	p2, owner2, err := fixture.Store.CreateProject(ctx, "project-two", "owner-two")
	if err != nil {
		t.Fatal(err)
	}
	worker2, err := fixture.Store.AddPrincipal(ctx, p2.ID, owner2.Token, "Worker 2", "worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.Store.PublishPlan(ctx, p2.ID, owner2.Token, "Project 2 Plan", 0); err != nil {
		t.Fatal(err)
	}

	// OtherCert is granted to Project 2.
	if _, err = fixture.Store.AddCertificateGrant(ctx, p2.ID, owner2.Token, fixture.OtherCertFingerprint); err != nil {
		t.Fatal(err)
	}

	// -------------------------------------------------------------------------
	// Boundary 1: ClientCert is granted ONLY to Project 1.
	// When calling Project 2 over TLS RPC with ClientCert, the certificate gate
	// must reject with HTTP 403 Forbidden, regardless of what token is presented.
	// -------------------------------------------------------------------------
	statusCertBlocked, bodyCertBlocked := makeRawTLSCall(t, fixture.URL, fixture.CAFile, fixture.ClientCertFile, fixture.ClientKeyFile, fixture.Worker.Token, p2.ID, "plan.read", nil)
	if statusCertBlocked != http.StatusForbidden {
		t.Fatalf("expected HTTP 403 when using Project 1 cert against Project 2, got %d (body: %s)", statusCertBlocked, bodyCertBlocked)
	}

	// -------------------------------------------------------------------------
	// Boundary 2: Token Isolation (HTTP 401 Unauthorized).
	// When using a certificate authorized for Project 2 (OtherCert), but supplying
	// a credential token from Project 1 (Worker 1 token):
	// The certificate gate passes, but the token verification fails with HTTP 401.
	// -------------------------------------------------------------------------
	statusTokenBlocked, bodyTokenBlocked := makeRawTLSCall(t, fixture.URL, fixture.CAFile, fixture.OtherCertFile, fixture.OtherKeyFile, fixture.Worker.Token, p2.ID, "plan.read", nil)
	if statusTokenBlocked != http.StatusUnauthorized {
		t.Fatalf("expected HTTP 401 when using Project 1 token against Project 2, got %d (body: %s)", statusTokenBlocked, bodyTokenBlocked)
	}

	// Verify cross-project mutation rejection over TLS RPC:
	// Attempt task creation in Project 2 with Project 1 token -> HTTP 401.
	statusMutate, _ := makeRawTLSCall(t, fixture.URL, fixture.CAFile, fixture.OtherCertFile, fixture.OtherKeyFile, fixture.Worker.Token, p2.ID, "task.create", map[string]any{
		"request_id":  "cross-proj-task",
		"revision":    1,
		"title":       "Injected Task",
		"description": "Should fail",
		"kind":        "implementation",
		"contract":    coord.ReportContract{ReviewType: "full_code", Formats: []string{"markdown"}},
	})
	if statusMutate != http.StatusUnauthorized {
		t.Fatalf("expected HTTP 401 on cross-project task creation, got %d", statusMutate)
	}

	// Attempt message send in Project 2 with Project 1 token -> HTTP 401.
	statusMsg, _ := makeRawTLSCall(t, fixture.URL, fixture.CAFile, fixture.OtherCertFile, fixture.OtherKeyFile, fixture.Worker.Token, p2.ID, "message.send", map[string]any{
		"recipient":  worker2.PrincipalID,
		"thread":     "cross-thread",
		"subject":    "Injected",
		"body":       "Injected",
		"request_id": "cross-msg",
	})
	if statusMsg != http.StatusUnauthorized {
		t.Fatalf("expected HTTP 401 on cross-project message send, got %d", statusMsg)
	}

	// Verify direct Store API isolation:
	if _, err = fixture.Store.CurrentPlan(ctx, p2.ID, fixture.Worker.Token); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("Store.CurrentPlan accepted cross-project token: %v", err)
	}
	if _, err = fixture.Store.CreateTask(ctx, p2.ID, fixture.Worker.Token, 1, "Cross", "Cross", "research", coord.ReportContract{ReviewType: "full_code", Formats: []string{"markdown"}}, nil); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("Store.CreateTask accepted cross-project token: %v", err)
	}
	if _, err = fixture.Store.SendMessage(ctx, p2.ID, fixture.Worker.Token, worker2.PrincipalID, "t", "s", "b"); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("Store.SendMessage accepted cross-project token: %v", err)
	}

	// Verify Web API project isolation:
	webH := coord.NewWebHandler(fixture.Store)
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8081/api/projects/"+p2.ID+"/tasks", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	webH.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("Web API allowed unauthenticated access to project 2: code=%d", rec.Code)
	}

	// Confirm Project 2 remains completely clean and unmutated:
	tasksP2, err := fixture.Store.ListTasks(ctx, p2.ID, owner2.Token, 10)
	if err != nil || len(tasksP2) != 0 {
		t.Fatalf("Project 2 state was polluted by cross-project requests: %+v", tasksP2)
	}
}

// ============================================================================
// Scenario F: Kill Switch Dynamic Cutoff & Local Resilience
// ============================================================================
// Verifies that toggling the remote kill switch off immediately rejects remote
// TLS RPC requests with HTTP 503 Service Unavailable, while local worktrees
// on loopback HTTP, direct Store calls, and private Unix socket daemon continue
// operating unhindered. Toggling back on immediately resumes remote traffic.
func TestMultiWorktree_ScenarioF_KillSwitchDynamicCutoffAndLocalResilience(t *testing.T) {
	fixture := setupMultiWorktreeTLS(t)
	ctx := context.Background()

	// Setup local loopback WebHandler
	webH := coord.NewWebHandler(fixture.Store)

	// Setup private Unix domain socket daemon
	sockDir := filepath.Join(t.TempDir(), "priv_socket")
	if err := os.Mkdir(sockDir, 0700); err != nil {
		t.Fatal(err)
	}
	sockPath := filepath.Join(sockDir, "coord.sock")

	daemonCtx, daemonCancel := context.WithCancel(context.Background())
	t.Cleanup(daemonCancel)
	go func() {
		_ = coord.ServeLocal(daemonCtx, fixture.Store, sockPath)
	}()

	// Wait for Unix socket to become ready
	var localRemote *coord.Remote
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(sockPath); err == nil {
			var errConnect error
			localRemote, errConnect = coord.NewLocalRemote(sockPath)
			if errConnect == nil {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if localRemote == nil {
		t.Fatal("failed to connect to local Unix socket daemon")
	}

	// 1. Initial State: Kill switch is enabled.
	// Verify remote TLS RPC, local socket daemon, loopback web, and direct store all succeed.
	remotePlan, err := fixture.Remote.CurrentPlan(ctx, fixture.Project.ID, fixture.Worker.Token)
	if err != nil || remotePlan.Revision != 1 {
		t.Fatalf("remote plan call failed initially: %+v (%v)", remotePlan, err)
	}
	socketPlan, err := localRemote.CurrentPlan(ctx, fixture.Project.ID, fixture.Worker.Token)
	if err != nil || socketPlan.Revision != 1 {
		t.Fatalf("local socket daemon plan call failed initially: %+v (%v)", socketPlan, err)
	}

	// 2. Toggle Kill Switch OFF (Disable remote coordination).
	if err = fixture.Store.SetRemoteAccessEnabled(ctx, false); err != nil {
		t.Fatalf("failed to disable remote access: %v", err)
	}

	// Remote TLS RPC must be IMMEDIATELY rejected with HTTP 503 / ErrUnavailable.
	_, err = fixture.Remote.CurrentPlan(ctx, fixture.Project.ID, fixture.Worker.Token)
	if !errors.Is(err, coord.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable on remote TLS RPC when kill switch disabled, got: %v", err)
	}
	rawStatus, rawBody := makeRawTLSCall(t, fixture.URL, fixture.CAFile, fixture.ClientCertFile, fixture.ClientKeyFile, fixture.Worker.Token, fixture.Project.ID, "plan.read", nil)
	if rawStatus != http.StatusServiceUnavailable {
		t.Fatalf("expected HTTP 503 Service Unavailable, got %d (body: %s)", rawStatus, rawBody)
	}
	if !strings.Contains(rawBody, "kill switch") {
		t.Fatalf("expected kill switch message in body, got: %s", rawBody)
	}

	// Verify remote callers cannot toggle the kill switch over TLS RPC:
	// While disabled, remote requests are pre-empted by the ingress gate with HTTP 503.
	statusRemoteToggleDisabled, _ := makeRawTLSCall(t, fixture.URL, fixture.CAFile, fixture.ClientCertFile, fixture.ClientKeyFile, fixture.Owner.Token, fixture.Project.ID, "remote.access.set", map[string]any{"enabled": true})
	if statusRemoteToggleDisabled != http.StatusServiceUnavailable {
		t.Fatalf("expected HTTP 503 when remote access is disabled, got: status=%d", statusRemoteToggleDisabled)
	}

	// 3. Local Resilience: Local mechanisms continue operating completely unhindered!
	// (a) Private Unix domain socket daemon continues to serve requests.
	socketPlanDuringKill, err := localRemote.CurrentPlan(ctx, fixture.Project.ID, fixture.Worker.Token)
	if err != nil || socketPlanDuringKill.Revision != 1 {
		t.Fatalf("local socket daemon failed during remote kill switch cutoff: %+v (%v)", socketPlanDuringKill, err)
	}

	// (b) Loopback WebHandler continues to operate.
	reqWeb := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8081/api/projects", nil)
	reqWeb.RemoteAddr = "127.0.0.1:12345"
	recWeb := httptest.NewRecorder()
	webH.ServeHTTP(recWeb, reqWeb)
	if recWeb.Code != http.StatusOK {
		t.Fatalf("local loopback web request was blocked during kill switch cutoff: code=%d", recWeb.Code)
	}

	// Also verify loopback login and authenticated endpoint succeed during kill switch:
	loginPayload, _ := json.Marshal(map[string]string{"project_id": fixture.Project.ID, "token": fixture.Owner.Token})
	reqLogin := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8081/api/login", bytes.NewReader(loginPayload))
	reqLogin.Header.Set("Content-Type", "application/json")
	reqLogin.Header.Set("Origin", "http://127.0.0.1:8081")
	reqLogin.RemoteAddr = "127.0.0.1:12345"
	recLogin := httptest.NewRecorder()
	webH.ServeHTTP(recLogin, reqLogin)
	if recLogin.Code != http.StatusOK {
		t.Fatalf("local loopback login failed during kill switch cutoff: code=%d", recLogin.Code)
	}
	cookie := recLogin.Result().Cookies()
	reqAuthed := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8081/api/projects/"+fixture.Project.ID+"/role-presets", nil)
	reqAuthed.RemoteAddr = "127.0.0.1:12345"
	for _, c := range cookie {
		reqAuthed.AddCookie(c)
	}
	recAuthed := httptest.NewRecorder()
	webH.ServeHTTP(recAuthed, reqAuthed)
	if recAuthed.Code != http.StatusOK {
		t.Fatalf("authenticated loopback web request was blocked during kill switch cutoff: code=%d", recAuthed.Code)
	}

	// (c) Direct Store calls continue unhindered.
	storePlan, err := fixture.Store.CurrentPlan(ctx, fixture.Project.ID, fixture.Worker.Token)
	if err != nil || storePlan.Revision != 1 {
		t.Fatalf("direct store call failed during kill switch cutoff: %+v (%v)", storePlan, err)
	}

	// 4. Toggle Kill Switch back ON.
	if err = fixture.Store.SetRemoteAccessEnabled(ctx, true); err != nil {
		t.Fatalf("failed to re-enable remote access: %v", err)
	}

	// Remote TLS RPC must immediately resume with HTTP 200.
	remotePlanResumed, err := fixture.Remote.CurrentPlan(ctx, fixture.Project.ID, fixture.Worker.Token)
	if err != nil || remotePlanResumed.Revision != 1 {
		t.Fatalf("remote plan call failed after re-enabling kill switch: %+v (%v)", remotePlanResumed, err)
	}
	rawStatusResumed, _ := makeRawTLSCall(t, fixture.URL, fixture.CAFile, fixture.ClientCertFile, fixture.ClientKeyFile, fixture.Worker.Token, fixture.Project.ID, "plan.read", nil)
	if rawStatusResumed != http.StatusOK {
		t.Fatalf("expected HTTP 200 after re-enabling kill switch, got %d", rawStatusResumed)
	}

	// Even when remote access is enabled, remote TLS RPC can NEVER mutate the kill switch (HTTP 401 Unauthorized):
	statusRemoteToggleEnabled, _ := makeRawTLSCall(t, fixture.URL, fixture.CAFile, fixture.ClientCertFile, fixture.ClientKeyFile, fixture.Owner.Token, fixture.Project.ID, "remote.access.set", map[string]any{"enabled": false})
	if statusRemoteToggleEnabled != http.StatusUnauthorized {
		t.Fatalf("remote TLS RPC was able to mutate kill switch when enabled: status=%d", statusRemoteToggleEnabled)
	}
}
