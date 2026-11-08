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
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/tasmirz/gossip/pkg/coord"
)

func TestRemoteMutualTLSResearchHandoff(t *testing.T) {
	s, _, p, owner, worker := setup(t)
	ctx := context.Background()
	now := time.Now()
	key := func() *ecdsa.PrivateKey {
		t.Helper()
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	caKey := key()
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	issue := func(serial int64, usage x509.ExtKeyUsage) ([]byte, []byte) {
		t.Helper()
		k := key()
		template := &x509.Certificate{SerialNumber: big.NewInt(serial), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
		der, err := x509.CreateCertificate(rand.Reader, template, ca, &k.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			t.Fatal(err)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	}
	serverPEM, serverKey := issue(2, x509.ExtKeyUsageServerAuth)
	clientPEM, clientKey := issue(3, x509.ExtKeyUsageClientAuth)
	otherPEM, otherKey := issue(4, x509.ExtKeyUsageClientAuth)
	cert, err := tls.X509KeyPair(serverPEM, serverKey)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	clientCertParsed, err := x509.ParseCertificate(func() []byte { b, _ := pem.Decode(clientPEM); return b.Bytes }())
	if err != nil {
		t.Fatal(err)
	}
	pin := sha256.Sum256(clientCertParsed.Raw)
	fingerprint := hex.EncodeToString(pin[:])
	if _, err = s.AddCertificateGrant(ctx, p.ID, owner.Token, fingerprint); err != nil {
		t.Fatal(err)
	}
	bootstrapAPIKey := strings.Repeat("bootstrap-secret-", 4)
	if err := s.SetRemoteAccessEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	handler, err := coord.RPCHandlerWithBootstrapKey(s, bootstrapAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(handler)
	ts.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, ClientCAs: pool, ClientAuth: tls.RequireAndVerifyClientCert}
	ts.StartTLS()
	defer ts.Close()
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	caFile := write("ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
	certFile := write("client.pem", clientPEM)
	keyFile := write("key.pem", clientKey)
	otherCertFile := write("other-client.pem", otherPEM)
	otherKeyFile := write("other-key.pem", otherKey)
	unlisted, err := coord.NewRemote(ts.URL, caFile, otherCertFile, otherKeyFile, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = unlisted.CurrentPlan(ctx, p.ID, owner.Token); err == nil {
		t.Fatal("unlisted CA-issued certificate was accepted")
	}
	bootstrapRemote, err := coord.NewRemoteBootstrap(ts.URL, caFile, otherCertFile, otherKeyFile, "", bootstrapAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	bootstrapCheckout := filepath.Join(t.TempDir(), "bootstrap-checkout")
	if err = os.MkdirAll(bootstrapCheckout, 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"remote", "add", "origin", "https://github.com/acme/bootstrapped.git"}} {
		cmd := exec.Command("git", append([]string{"-C", bootstrapCheckout}, args...)...)
		if output, gitErr := cmd.CombinedOutput(); gitErr != nil {
			t.Fatalf("git %v: %v: %s", args, gitErr, output)
		}
	}
	observation, err := coord.InspectGitWorkspace(ctx, bootstrapCheckout)
	if err != nil {
		t.Fatal(err)
	}
	preparedProjectID, preparedOwner, err := coord.PrepareCredential("")
	if err != nil {
		t.Fatal(err)
	}
	wrongBootstrap, err := coord.NewRemoteBootstrap(ts.URL, caFile, otherCertFile, otherKeyFile, "", strings.Repeat("wrong-secret-", 5))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err = wrongBootstrap.CreatePreparedProjectWithWorkspace(ctx, preparedProjectID, "bootstrapped", "owner", "bootstrapped", "worker-host", observation, preparedOwner); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("wrong bootstrap API key was accepted: %v", err)
	}
	createdProject, createdOwner, createdRepo, createdWorkspace, err := bootstrapRemote.CreatePreparedProjectWithWorkspace(ctx, preparedProjectID, "bootstrapped", "owner", "bootstrapped", "worker-host", observation, preparedOwner)
	if err != nil || createdProject.Name != "bootstrapped" || createdOwner.Token == "" || createdRepo.Name != "bootstrapped" || createdWorkspace.RepositoryID != createdRepo.ID {
		t.Fatalf("authorized remote bootstrap failed: project=%+v repo=%+v workspace=%+v err=%v", createdProject, createdRepo, createdWorkspace, err)
	}
	retriedProject, _, retriedRepo, retriedWorkspace, err := bootstrapRemote.CreatePreparedProjectWithWorkspace(ctx, preparedProjectID, "bootstrapped", "owner", "bootstrapped", "worker-host", observation, preparedOwner)
	if err != nil || retriedProject.ID != createdProject.ID || retriedRepo.ID != createdRepo.ID || retriedWorkspace.ID != createdWorkspace.ID {
		t.Fatalf("prepared remote bootstrap retry was not idempotent: project=%+v repo=%+v workspace=%+v err=%v", retriedProject, retriedRepo, retriedWorkspace, err)
	}
	if _, err = bootstrapRemote.CurrentPlan(ctx, createdProject.ID, bootstrapAPIKey); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("create-only bootstrap key accessed project work: %v", err)
	}
	newOwnerRemote, err := coord.NewRemote(ts.URL, caFile, otherCertFile, otherKeyFile, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = newOwnerRemote.WhoAmI(ctx, createdProject.ID, createdOwner.Token); err != nil {
		t.Fatalf("bootstrap client's certificate was not granted to the newly created project: %v", err)
	}
	mcpCheckout := filepath.Join(t.TempDir(), "mcp-bootstrap-checkout")
	if err = os.MkdirAll(mcpCheckout, 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"remote", "add", "origin", "https://github.com/acme/mcp-bootstrap.git"}} {
		cmd := exec.Command("git", append([]string{"-C", mcpCheckout}, args...)...)
		if output, gitErr := cmd.CombinedOutput(); gitErr != nil {
			t.Fatalf("git %v: %v: %s", args, gitErr, output)
		}
	}
	credentialDir := filepath.Join(t.TempDir(), "mcp-credentials")
	bootstrapMCP := coord.ProjectBootstrapMCPServer(bootstrapRemote, credentialDir)
	createdByMCP, callErr := bootstrapMCP.GetTool("gossip_create_project").Handler(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "gossip_create_project", Arguments: map[string]any{"local_path": mcpCheckout, "request_id": "remote-mcp-project-create"}}})
	if callErr != nil || createdByMCP.IsError {
		t.Fatalf("remote MCP project creation failed: %+v %v", createdByMCP, callErr)
	}
	var mcpCreation struct {
		Project                  coord.Project `json:"project"`
		CredentialFile           string        `json:"owner_credential_file"`
		ClientCertificateGranted bool          `json:"client_certificate_granted"`
	}
	if err = json.Unmarshal([]byte(createdByMCP.Content[0].(mcp.TextContent).Text), &mcpCreation); err != nil || mcpCreation.Project.Name != "mcp-bootstrap" || !mcpCreation.ClientCertificateGranted || mcpCreation.CredentialFile == "" {
		t.Fatalf("remote MCP bootstrap response was incomplete or inaccurate: %+v err=%v", mcpCreation, err)
	}
	remote, err := coord.NewRemote(ts.URL, caFile, certFile, keyFile, "")
	if err != nil {
		t.Fatal(err)
	}
	credentialValid, err := remote.CredentialValid(ctx, p.ID, worker.Token)
	if err != nil || !credentialValid {
		t.Fatalf("TLS credential validation did not recognize an active project member: valid=%v err=%v", credentialValid, err)
	}
	credentialValid, err = remote.CredentialValid(ctx, p.ID, "invalid-credential")
	if err != nil || credentialValid {
		t.Fatalf("TLS credential validation accepted an invalid project credential: valid=%v err=%v", credentialValid, err)
	}
	remoteCapabilities := []coord.AgentCapability{{Name: "mcp_tools", Source: "adapter"}, {Name: "model_selection", Source: "host_reported"}}
	if err = remote.HeartbeatAgentWithMetadata(ctx, p.ID, worker.Token, "remote-capability-session", remoteCapabilities, coord.MCPClientInfo{Name: "remote-agent-host", Version: "2.4"}); err != nil {
		t.Fatalf("remote capability heartbeat: %v", err)
	}
	remotePresence, err := remote.ListAgentPresence(ctx, p.ID, owner.Token)
	if err != nil {
		t.Fatal(err)
	}
	foundRemoteCapabilities := false
	for _, agent := range remotePresence {
		if agent.PrincipalID == worker.PrincipalID && len(agent.Capabilities) == 2 && agent.Capabilities[0].Source == "adapter" && agent.Capabilities[1].Source == "host_reported" && agent.MCPClientName == "remote-agent-host" && agent.MCPClientVersion == "2.4" {
			foundRemoteCapabilities = true
		}
	}
	if !foundRemoteCapabilities {
		t.Fatalf("remote capability provenance did not survive TLS RPC: %+v", remotePresence)
	}
	syncFrame, err := remote.SyncProject(ctx, p.ID, worker.Token, 0, 1)
	if err != nil || syncFrame.Plan == nil || syncFrame.Plan.Revision != 1 || len(syncFrame.Events) != 1 || !syncFrame.EventsMayContinue {
		t.Fatalf("remote consistent project sync: frame=%+v err=%v", syncFrame, err)
	}

	// Exercise the worker-to-orchestrator review request through the same
	// authenticated remote boundary used by a LAN or FRP-connected agent.
	reviewTask, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Review remote architecture", "Return a Markdown report with UML diagrams", "research", coord.ReportContract{ReviewType: "diagram_report", Formats: []string{"markdown", "mermaid_sequence", "mermaid_class"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	reviewAttempt, err := remote.ClaimTaskInWorkspace(ctx, p.ID, worker.Token, reviewTask.ID, "", time.Minute)
	if err != nil {
		t.Fatalf("remote worker could not claim planned review: %v", err)
	}
	if err = remote.RequestReview(ctx, p.ID, worker.Token, reviewTask.ID, reviewAttempt.Attempt); err != nil {
		t.Fatalf("remote worker could not request orchestrator review: %v", err)
	}
	reportedUsage, err := remote.RecordAttemptUsage(ctx, p.ID, worker.Token, reviewTask.ID, reviewAttempt.Attempt, "remote-usage-1", coord.UsageReport{Provider: "ExampleAI", Model: "fast-coder", Source: "provider_reported", InputTokens: 2300, OutputTokens: 450, CostMicros: 8200, Currency: "USD"})
	if err != nil || reportedUsage.Source != "provider_reported" || reportedUsage.PrincipalID != worker.PrincipalID {
		t.Fatalf("remote agent usage was not recorded: %+v %v", reportedUsage, err)
	}
	retriedUsage, err := remote.RecordAttemptUsage(ctx, p.ID, worker.Token, reviewTask.ID, reviewAttempt.Attempt, "remote-usage-1", coord.UsageReport{Provider: "ExampleAI", Model: "fast-coder", Source: "provider_reported", InputTokens: 2300, OutputTokens: 450, CostMicros: 8200, Currency: "usd"})
	if err != nil || retriedUsage.RecordedAt != reportedUsage.RecordedAt {
		t.Fatalf("remote usage retry was not idempotent: %+v %v", retriedUsage, err)
	}
	usageBudget, err := remote.GetBudget(ctx, p.ID, owner.Token)
	if err != nil || usageBudget.ReportedUsage.Records != 1 || usageBudget.ReportedUsage.InputTokens != 2300 || len(usageBudget.ReportedUsage.Costs) != 1 || usageBudget.ReportedUsage.Costs[0].CostMicros != 8200 {
		t.Fatalf("orchestrator could not read project usage totals: %+v %v", usageBudget.ReportedUsage, err)
	}
	remoteEvents, err := remote.ReadEvents(ctx, p.ID, owner.Token, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	foundReviewRequest := false
	for _, item := range remoteEvents {
		if item.Kind != "review.requested" || item.EntityID != reviewTask.ID {
			continue
		}
		var payload struct {
			Attempt         int    `json:"attempt"`
			ReviewType      string `json:"review_type"`
			CandidateID     string `json:"candidate_id"`
			CandidateDigest string `json:"candidate_digest"`
		}
		if err = json.Unmarshal(item.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		foundReviewRequest = payload.Attempt == reviewAttempt.Attempt && payload.ReviewType == "diagram_report"
	}
	if !foundReviewRequest {
		t.Fatal("orchestrator did not receive the planned diagram review request over mutual TLS")
	}
	reviewBody := "# Architecture review\n\n## Review summary\nThe coordinator verifies project access before delegating the requested review.\n\n## Scope and evidence\nThis report traces the task request and authorization boundary at the recorded source baseline.\n\n## UML class diagram\nThe relationship below shows the coordinator assigning work to a worker.\n\n```mermaid\nclassDiagram\n  Coordinator --> Worker\n```\n\n## Sequence diagram\nThis sequence shows the review request crossing the coordinator and worker boundary.\n\n```mermaid\nsequenceDiagram\n  Coordinator->>Worker: request review\n```\n\n## Findings\nThe request preserves its planned review type after the worker claims the task.\n\n## Coverage and limitations\nThe task handoff is covered; this report does not claim a live runtime trace."
	diagramReport, err := remote.SubmitReport(ctx, p.ID, worker.Token, reviewTask.ID, reviewAttempt.Attempt, "commit:remote-review", reviewBody)
	if err != nil {
		t.Fatalf("remote UML report submission failed: %v", err)
	}
	retriedDiagramReport, retryErr := remote.SubmitReport(ctx, p.ID, worker.Token, reviewTask.ID, reviewAttempt.Attempt, "commit:remote-review", reviewBody)
	if retryErr != nil || retriedDiagramReport.ID != diagramReport.ID || retriedDiagramReport.Digest != diagramReport.Digest {
		t.Fatalf("remote report retry was not idempotent: first=%+v retry=%+v err=%v", diagramReport, retriedDiagramReport, retryErr)
	}
	visibleReport, err := remote.GetReport(ctx, p.ID, owner.Token, diagramReport.ID)
	if err != nil || visibleReport.Digest != diagramReport.Digest || visibleReport.Body != reviewBody {
		t.Fatalf("orchestrator could not retrieve exact remote report: %+v %v", visibleReport, err)
	}
	if err = remote.AcceptReport(ctx, p.ID, owner.Token, visibleReport.ID, visibleReport.Digest); err != nil {
		t.Fatalf("orchestrator could not accept the exact diagram report: %v", err)
	}
	acceptedDiagramTask, err := remote.GetTask(ctx, p.ID, owner.Token, reviewTask.ID)
	if err != nil || acceptedDiagramTask.Status != "accepted" {
		t.Fatalf("diagram review was not accepted: %+v %v", acceptedDiagramTask, err)
	}
	fullCodeTask, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Review remote implementation", "Inspect the complete source change", "review", coord.ReportContract{ReviewType: "full_code", Formats: []string{"markdown"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fullCodeAttempt, err := remote.ClaimTaskInWorkspace(ctx, p.ID, worker.Token, fullCodeTask.ID, "", time.Minute)
	if err != nil {
		t.Fatalf("remote worker could not claim full-code review: %v", err)
	}
	fullCodeDiff := "diff --git a/pkg/remote.go b/pkg/remote.go\n--- a/pkg/remote.go\n+++ b/pkg/remote.go\n@@ -1 +1 @@\n-old\n+new"
	fullCodeCandidate, err := remote.SaveCandidate(ctx, p.ID, worker.Token, fullCodeTask.ID, fullCodeAttempt.Attempt, "commit:remote-full-review", fullCodeDiff)
	if err != nil {
		t.Fatalf("remote worker could not attach full-code evidence: %v", err)
	}
	if err = remote.RequestReview(ctx, p.ID, worker.Token, fullCodeTask.ID, fullCodeAttempt.Attempt); err != nil {
		t.Fatalf("remote worker could not request full-code review: %v", err)
	}
	remoteEvents, err = remote.ReadEvents(ctx, p.ID, owner.Token, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	foundFullCodeRequest := false
	for _, item := range remoteEvents {
		if item.Kind != "review.requested" || item.EntityID != fullCodeTask.ID {
			continue
		}
		var payload struct {
			Attempt         int    `json:"attempt"`
			ReviewType      string `json:"review_type"`
			CandidateID     string `json:"candidate_id"`
			CandidateDigest string `json:"candidate_digest"`
		}
		if err = json.Unmarshal(item.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		foundFullCodeRequest = payload.Attempt == fullCodeAttempt.Attempt && payload.ReviewType == "full_code" && payload.CandidateID == fullCodeCandidate.ID && payload.CandidateDigest == fullCodeCandidate.Digest
	}
	if !foundFullCodeRequest {
		t.Fatal("orchestrator did not receive the planned full-code review and exact evidence reference over mutual TLS")
	}
	fullCodeRead := strings.Builder{}
	for offset := 0; ; {
		fullCodePage, pageErr := remote.GetCandidatePage(ctx, p.ID, owner.Token, fullCodeCandidate.ID, offset, 32)
		if pageErr != nil || fullCodePage.Digest != fullCodeCandidate.Digest {
			t.Fatalf("orchestrator could not retrieve exact remote source evidence: %+v %v", fullCodePage, pageErr)
		}
		fullCodeRead.WriteString(fullCodePage.Excerpt)
		if fullCodePage.NextOffset == nil {
			break
		}
		offset = *fullCodePage.NextOffset
	}
	if fullCodeRead.String() != fullCodeDiff {
		t.Fatalf("paged source evidence changed: %q", fullCodeRead.String())
	}
	fullCodeReport, err := remote.SubmitReport(ctx, p.ID, worker.Token, fullCodeTask.ID, fullCodeAttempt.Attempt, "commit:remote-full-review", "Full code review findings and evidence")
	if err != nil {
		t.Fatalf("remote full-code report submission failed: %v", err)
	}
	visibleFullCodeReport, err := remote.GetReport(ctx, p.ID, owner.Token, fullCodeReport.ID)
	if err != nil || visibleFullCodeReport.Digest != fullCodeReport.Digest || visibleFullCodeReport.SourceCandidateID != fullCodeCandidate.ID || visibleFullCodeReport.SourceCandidateDigest != fullCodeCandidate.Digest {
		t.Fatalf("orchestrator could not retrieve exact full-code report: %+v %v", visibleFullCodeReport, err)
	}
	if err = remote.AcceptReport(ctx, p.ID, owner.Token, visibleFullCodeReport.ID, visibleFullCodeReport.Digest); err != nil {
		t.Fatalf("orchestrator could not accept the exact full-code report: %v", err)
	}
	acceptedFullCodeTask, err := remote.GetTask(ctx, p.ID, owner.Token, fullCodeTask.ID)
	if err != nil || acceptedFullCodeTask.Status != "accepted" {
		t.Fatalf("full-code review was not accepted: %+v %v", acceptedFullCodeTask, err)
	}

	repository, err := s.CreateRepository(ctx, p.ID, owner.Token, "integration transport")
	if err != nil {
		t.Fatal(err)
	}
	base := strings.Repeat("a", 40)
	workerWorkspace, err := s.RegisterWorkspace(ctx, p.ID, worker.Token, repository.ID, "worker checkout", "node", "/worker/repo", "feature", base, false)
	if err != nil {
		t.Fatal(err)
	}
	ownerWorkspace, err := s.RegisterWorkspace(ctx, p.ID, owner.Token, repository.ID, "integrator checkout", "node", "/owner/repo", "main", base, false)
	if err != nil {
		t.Fatal(err)
	}
	var candidateIDs []string
	for i := 0; i < 2; i++ {
		sourceTask, createErr := s.CreateTask(ctx, p.ID, owner.Token, 1, "Remote source", "Candidate for integration", "implementation", coord.ReportContract{ReviewType: "full_code"}, nil)
		if createErr != nil {
			t.Fatal(createErr)
		}
		sourceAttempt, claimErr := s.ClaimTaskInWorkspace(ctx, p.ID, worker.Token, sourceTask.ID, workerWorkspace.ID, time.Minute)
		if claimErr != nil {
			t.Fatal(claimErr)
		}
		candidate, saveErr := s.SaveCandidate(ctx, p.ID, worker.Token, sourceTask.ID, sourceAttempt.Attempt, base, "candidate")
		if saveErr != nil {
			t.Fatal(saveErr)
		}
		candidateIDs = append(candidateIDs, candidate.ID)
		if _, saveErr = s.SubmitReport(ctx, p.ID, worker.Token, sourceTask.ID, sourceAttempt.Attempt, base, "source candidate is ready"); saveErr != nil {
			t.Fatal(saveErr)
		}
	}
	integrationTask, err := s.CreateTask(ctx, p.ID, owner.Token, 1, "Remote integration", "Record the combined revision", "integration", coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	integrationAttempt, err := remote.ClaimTaskInWorkspace(ctx, p.ID, owner.Token, integrationTask.ID, ownerWorkspace.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	submission := coord.IntegrationSubmission{TaskID: integrationTask.ID, Attempt: integrationAttempt.Attempt, WorkspaceID: ownerWorkspace.ID, BaseRevision: base, CombinedRevision: base, Observation: coord.GitObservation{Branch: "main", Head: base}, CandidateIDs: candidateIDs, Checks: []coord.IntegrationCheck{{Name: "transport", Command: "check", Passed: true, Revision: base}}, Summary: "MCP over mutual TLS stored the combined revision."}
	remoteRecord, err := remote.RecordIntegration(ctx, p.ID, owner.Token, submission)
	if err != nil || remoteRecord.EvidenceTrust != "host_reported" {
		t.Fatalf("remote integration record: %+v %v", remoteRecord, err)
	}
	remoteRecords, err := remote.ListIntegrations(ctx, p.ID, owner.Token, 10)
	if err != nil || len(remoteRecords) != 1 || remoteRecords[0].ID != remoteRecord.ID {
		t.Fatalf("remote integration history: %+v %v", remoteRecords, err)
	}
	if _, err = remote.TransitionTask(ctx, p.ID, owner.Token, integrationTask.ID, "cancel", "integration transport fixture complete", integrationAttempt.Attempt); err != nil {
		t.Fatalf("remote integration fixture cleanup: %v", err)
	}
	otherBlock, _ := pem.Decode(otherPEM)
	otherParsed, err := x509.ParseCertificate(otherBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	otherDigest := sha256.Sum256(otherParsed.Raw)
	otherFingerprint := hex.EncodeToString(otherDigest[:])
	expiresAt := time.Now().Add(350 * time.Millisecond).UnixMilli()
	temporaryGrant, err := remote.AddCertificateGrantUntil(ctx, p.ID, owner.Token, otherFingerprint, expiresAt)
	if err != nil || temporaryGrant.ExpiresAt != expiresAt {
		t.Fatalf("remote temporary certificate grant: %+v %v", temporaryGrant, err)
	}
	if _, err = unlisted.WhoAmI(ctx, p.ID, owner.Token); err != nil {
		t.Fatalf("temporary certificate was not authorized: %v", err)
	}
	time.Sleep(time.Until(time.UnixMilli(expiresAt)) + 10*time.Millisecond)
	if _, err = unlisted.WhoAmI(ctx, p.ID, owner.Token); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("expired remote certificate still authorized: %v", err)
	}
	if err = remote.RevokeCertificateGrant(ctx, p.ID, owner.Token, otherFingerprint); err != nil {
		t.Fatal(err)
	}
	inviteID, inviteSecret, err := coord.PrepareCertificateInvitation()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = remote.AddPreparedCertificateInvitation(ctx, p.ID, owner.Token, inviteID, inviteSecret, otherFingerprint, time.Now().Add(time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err = unlisted.RedeemCertificateInvitation(ctx, p.ID, inviteID, inviteSecret); err != nil {
		t.Fatalf("CA-verified client could not redeem project invitation: %v", err)
	}
	if _, err = unlisted.WhoAmI(ctx, p.ID, owner.Token); err != nil {
		t.Fatalf("redeemed certificate lacks project access: %v", err)
	}
	if _, err = unlisted.RedeemCertificateInvitation(ctx, p.ID, inviteID, inviteSecret); err != nil {
		t.Fatalf("lost-response retry failed: %v", err)
	}
	if err = remote.RevokeCertificateGrant(ctx, p.ID, owner.Token, otherFingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err = unlisted.RedeemCertificateInvitation(ctx, p.ID, inviteID, inviteSecret); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("redeemed invitation restored revoked certificate: %v", err)
	}
	otherProject, otherOwner, err := s.CreateProject(ctx, "other project", "other owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = remote.CurrentPlan(ctx, otherProject.ID, otherOwner.Token); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("certificate crossed its project grant: %v", err)
	}
	if _, err = remote.CurrentPlan(ctx, p.ID, "wrong"); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("invalid token: %v", err)
	}
	preparedProject, prepared, err := coord.PrepareCredential(p.ID)
	if err != nil || preparedProject != p.ID {
		t.Fatalf("prepare remote principal credential: %v", err)
	}
	enrolled, err := remote.AddPreparedPrincipal(ctx, p.ID, owner.Token, "Remote reviewer", "reviewer", prepared)
	if err != nil || enrolled != prepared {
		t.Fatalf("owner could not enroll over mTLS: %+v %v", enrolled, err)
	}
	principal, err := remote.WhoAmI(ctx, p.ID, enrolled.Token)
	if err != nil || principal.ID != enrolled.PrincipalID || principal.Role != "reviewer" {
		t.Fatalf("remotely enrolled credential did not authenticate: %+v %v", principal, err)
	}
	_, unauthorized, err := coord.PrepareCredential(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = remote.AddPreparedPrincipal(ctx, p.ID, worker.Token, "Unauthorized", "worker", unauthorized); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("non-owner enrolled a principal remotely: %v", err)
	}
	legacyDir := t.TempDir()
	if err = os.WriteFile(filepath.Join(legacyDir, "state.json"), []byte(`{"project_name":"old","agents":{},"goals":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	archived, _, err := s.ImportLegacyArchive(ctx, p.ID, owner.Token, legacyDir, false)
	if err != nil {
		t.Fatalf("prepare history for remote read: %v", err)
	}
	remoteImports, err := remote.ListLegacyImports(ctx, p.ID, owner.Token)
	if err != nil || len(remoteImports) != 1 || remoteImports[0].ID != archived.ID {
		t.Fatalf("remote history list: %+v %v", remoteImports, err)
	}
	remoteGoals, err := remote.ReadLegacyRecords(ctx, p.ID, owner.Token, archived.ID, "goal", "", 10)
	if err != nil || len(remoteGoals) != 0 {
		t.Fatalf("remote history read: %+v %v", remoteGoals, err)
	}
	outbox, err := coord.OpenOutbox(filepath.Join(dir, "remote-outbox.db"), ts.URL, p.ID, worker.PrincipalID, worker.Token, remote)
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	if _, err = remote.SyncProject(ctx, p.ID, worker.Token, 0, 50); err != nil {
		t.Fatalf("remote replica sync: %v", err)
	}
	replicas, err := remote.ListProjectReplicas(ctx, p.ID, owner.Token)
	if err != nil || len(replicas) != 1 || replicas[0].ReplicaID != outbox.ReplicaID() || replicas[0].PrincipalID != worker.PrincipalID || len(replicas[0].CertificateFingerprint) != 64 {
		t.Fatalf("owner could not inspect project-scoped remote sync receipt: %+v %v", replicas, err)
	}
	if _, err = remote.ListProjectReplicas(ctx, p.ID, worker.Token); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("remote worker listed owner-only replica receipts: %v", err)
	}
	if _, err = outbox.Enqueue(ctx, owner.PrincipalID, "", "offline queue", "queued over TLS", "remote-queue-id"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		queued, e := outbox.List(ctx, 10)
		if e != nil {
			t.Fatal(e)
		}
		if len(queued) == 1 && queued[0].Status == "delivered" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	queued, err := outbox.List(ctx, 10)
	if err != nil || len(queued) != 1 || queued[0].Status != "delivered" {
		t.Fatalf("remote outbox: %+v %v", queued, err)
	}
	workRole, err := remote.SetWorkRole(ctx, p.ID, owner.Token, worker.PrincipalID, "", "researcher", "", 0, 0)
	if err != nil || workRole.Duties == "" {
		t.Fatalf("remote role: %+v %v", workRole, err)
	}
	workRoles, err := remote.ListWorkRoles(ctx, p.ID, worker.Token)
	if err != nil || len(workRoles) != 1 || workRoles[0].Name != "researcher" {
		t.Fatalf("remote roles: %+v %v", workRoles, err)
	}
	events, err := remote.ReadEvents(ctx, p.ID, worker.Token, 0, 50)
	if err != nil || len(events) == 0 {
		t.Fatalf("remote events: %v %v", events, err)
	}
	lastEvent := events[len(events)-1].Sequence
	if got, err := remote.AcknowledgeEvents(ctx, p.ID, worker.Token, lastEvent); err != nil || got != lastEvent {
		t.Fatalf("remote event acknowledgment: %d %v", got, err)
	}
	if got, err := remote.EventCheckpoint(ctx, p.ID, worker.Token); err != nil || got != lastEvent {
		t.Fatalf("remote checkpoint: %d %v", got, err)
	}
	settings, err := remote.GetSettings(ctx, p.ID, owner.Token)
	if err != nil {
		t.Fatal(err)
	}
	settings.MaxParallel = 3
	if _, err = remote.UpdateSettings(ctx, p.ID, owner.Token, settings); err != nil {
		t.Fatal(err)
	}
	remoteTaskContract := coord.ReportContract{ReviewType: "full_code", Formats: []string{"mermaid_sequence"}}
	task, err := remote.CreateTaskWithID(ctx, p.ID, owner.Token, "remote-research-1", 1, "Remote research", "Find evidence", "research", remoteTaskContract, nil)
	if err != nil {
		t.Fatal(err)
	}
	retriedTask, err := remote.CreateTaskWithID(ctx, p.ID, owner.Token, "remote-research-1", 1, "Remote research", "Find evidence", "research", remoteTaskContract, nil)
	if err != nil || retriedTask.ID != task.ID {
		t.Fatalf("remote task retry duplicated a committed request: original=%s retry=%s err=%v", task.ID, retriedTask.ID, err)
	}
	if _, err = remote.CreateTaskWithID(ctx, p.ID, owner.Token, "remote-research-1", 1, "Changed task", "Different payload", "research", remoteTaskContract, nil); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("remote request ID accepted changed task payload: %v", err)
	}
	if _, err = remote.CreateTaskWithID(ctx, p.ID, worker.Token, "denied-delegation", 1, "Denied delegation", "No grant yet", "research", coord.ReportContract{ReviewType: "full_code"}, nil); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("remote worker delegated without grant: %v", err)
	}
	if _, err = remote.GrantPermission(ctx, p.ID, owner.Token, worker.PrincipalID, coord.PermissionTaskDelegate); err != nil {
		t.Fatalf("remote permission grant: %v", err)
	}
	delegatedTask, createErr := remote.CreateTaskWithID(ctx, p.ID, worker.Token, "delegated-verification", 1, "Remote delegated verification", "Verify the planned sequence", "verification", coord.ReportContract{ReviewType: "full_code"}, nil)
	if createErr != nil {
		t.Fatalf("remote delegated task creation: %v", createErr)
	}
	claimed, claimErr := remote.ClaimTaskInWorkspace(ctx, p.ID, worker.Token, delegatedTask.ID, "", time.Minute)
	if claimErr != nil {
		t.Fatalf("remote delegated task claim: %v", claimErr)
	}
	if _, err = remote.SaveCandidate(ctx, p.ID, worker.Token, delegatedTask.ID, claimed.Attempt, "commit:remote-review", "diff --git a/review.go b/review.go\n--- a/review.go\n+++ b/review.go\n@@ -1 +1 @@\n-old\n+new"); err != nil {
		t.Fatalf("remote delegated source evidence: %v", err)
	}
	if err = remote.RequestReview(ctx, p.ID, worker.Token, delegatedTask.ID, claimed.Attempt); err != nil {
		t.Fatalf("remote review request: %v", err)
	}
	reviewReport, submitErr := remote.SubmitReport(ctx, p.ID, worker.Token, delegatedTask.ID, claimed.Attempt, "commit:remote-review", "Verification and UML review request complete")
	if submitErr != nil {
		t.Fatalf("remote report submission: %v", submitErr)
	}
	if err = remote.AcceptReport(ctx, p.ID, owner.Token, reviewReport.ID, reviewReport.Digest); err != nil {
		t.Fatalf("remote report acceptance: %v", err)
	}
	if err = remote.RevokePermission(ctx, p.ID, owner.Token, worker.PrincipalID, coord.PermissionTaskDelegate); err != nil {
		t.Fatalf("remote permission revoke: %v", err)
	}
	if _, err = remote.CreateTaskWithID(ctx, p.ID, worker.Token, "denied-after-revoke", 1, "Denied again", "Grant revoked", "research", coord.ReportContract{ReviewType: "full_code"}, nil); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("remote worker delegated after revoke: %v", err)
	}
	principals, err := remote.ListPrincipals(ctx, p.ID, owner.Token)
	if err != nil || len(principals) != 3 {
		t.Fatalf("remote principals: %+v %v", principals, err)
	}
	assigned, err := remote.AssignTask(ctx, p.ID, owner.Token, task.ID, worker.PrincipalID)
	if err != nil || assigned.AssignedTo != worker.PrincipalID {
		t.Fatalf("remote assignment: %+v %v", assigned, err)
	}
	page, err := remote.SearchTasks(ctx, p.ID, worker.Token, coord.TaskFilter{Status: "ready", Assignee: worker.PrincipalID, Kind: "research", Limit: 10})
	if err != nil || len(page.Tasks) != 1 || page.Tasks[0].ID != task.ID {
		t.Fatalf("remote filtered search: %+v %v", page, err)
	}
	if _, err = remote.ClaimTaskInWorkspace(ctx, p.ID, owner.Token, task.ID, "", time.Minute); !errors.Is(err, coord.ErrConflict) {
		t.Fatalf("unassigned principal claimed: %v", err)
	}
	repo, err := remote.CreateRepository(ctx, p.ID, owner.Token, "remote repository")
	if err != nil {
		t.Fatal(err)
	}
	hinted, err := remote.AddRepositoryRemoteHint(ctx, p.ID, owner.Token, repo.ID, "https://user:secret@GitHub.com/org/remote.git?token=private")
	if err != nil || len(hinted.RemoteHints) != 1 || hinted.RemoteHints[0] != "github.com/org/remote" {
		t.Fatalf("remote repository hint was not sanitized: %+v %v", hinted, err)
	}
	listedRepositories, err := remote.ListRepositories(ctx, p.ID, worker.Token)
	listedHint := false
	for _, listed := range listedRepositories {
		if listed.ID == repo.ID && len(listed.RemoteHints) == 1 && listed.RemoteHints[0] == "github.com/org/remote" {
			listedHint = true
		}
	}
	if err != nil || !listedHint {
		t.Fatalf("remote repository hint could not be read within project: %+v %v", listedRepositories, err)
	}
	matchedRepositories, err := remote.FindRepositoriesByRemote(ctx, p.ID, worker.Token, "git@github.com:org/remote.git")
	if err != nil || len(matchedRepositories) != 1 || matchedRepositories[0].ID != repo.ID {
		t.Fatalf("remote project recognition failed: %+v %v", matchedRepositories, err)
	}
	if _, err = remote.AddRepositoryRemoteHint(ctx, p.ID, worker.Token, repo.ID, "https://github.com/org/other"); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("non-owner added a remote recognition hint over mTLS: %v", err)
	}
	workspace, err := remote.RegisterWorkspace(ctx, p.ID, worker.Token, repo.ID, "remote workspace", "node", "/remote/repo", "main", "old", true)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := remote.ClaimObservedWorkspace(ctx, p.ID, worker.Token, task.ID, workspace.ID, coord.GitObservation{Root: "/remote/repo", Branch: "main", Head: "fresh", Dirty: false}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := remote.GetAttempt(ctx, p.ID, owner.Token, task.ID, claim.Attempt)
	if err != nil || observed.WorkspaceHead != "fresh" || observed.WorkspaceDirty {
		t.Fatalf("remote observed baseline: %+v %v", observed, err)
	}
	candidate, err := remote.SaveCandidate(ctx, p.ID, worker.Token, task.ID, claim.Attempt, "commit:fresh", "candidate findings")
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := remote.ListCandidates(ctx, p.ID, owner.Token, task.ID, 50)
	if err != nil || len(candidates) != 1 || candidates[0].ID != candidate.ID || candidates[0].Body != "" {
		t.Fatalf("remote candidates: %+v %v", candidates, err)
	}
	candidate, err = remote.GetCandidate(ctx, p.ID, owner.Token, candidate.ID)
	if err != nil || candidate.Body != "candidate findings" {
		t.Fatalf("remote candidate: %+v %v", candidate, err)
	}
	queuedCandidate, err := remote.SaveCandidateWithID(ctx, p.ID, worker.Token, task.ID, claim.Attempt, "commit:fresh", "queued finding", "candidate-id")
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := remote.SaveCandidateWithID(ctx, p.ID, worker.Token, task.ID, claim.Attempt, "commit:fresh", "queued finding", "candidate-id")
	if err != nil || duplicate.ID != queuedCandidate.ID {
		t.Fatalf("remote candidate retry: %+v %v", duplicate, err)
	}
	report, err := remote.SubmitReport(ctx, p.ID, worker.Token, task.ID, claim.Attempt, "commit:fresh", "remote findings")
	if err != nil {
		t.Fatal(err)
	}
	if report.Body != "remote findings" {
		t.Fatal("remote adapter lost submitted content")
	}
	reportPage, err := remote.GetReportPage(ctx, p.ID, owner.Token, report.ID, 0, 6)
	if err != nil || reportPage.Excerpt != "remote" || reportPage.NextOffset == nil {
		t.Fatalf("remote report page: %+v %v", reportPage, err)
	}
	if err = remote.AcceptReport(ctx, p.ID, owner.Token, report.ID, report.Digest); err != nil {
		t.Fatal(err)
	}
	message, err := remote.SendMessage(ctx, p.ID, owner.Token, worker.PrincipalID, "", "handoff", "review complete")
	if err != nil {
		t.Fatal(err)
	}
	idempotent, err := remote.SendMessageWithID(ctx, p.ID, owner.Token, worker.PrincipalID, "", "handoff", "retry safely", "remote-retry-1")
	if err != nil {
		t.Fatal(err)
	}
	retry, err := remote.SendMessageWithID(ctx, p.ID, owner.Token, worker.PrincipalID, "", "handoff", "retry safely", "remote-retry-1")
	if err != nil || retry.ID != idempotent.ID {
		t.Fatalf("remote retry: %+v %v", retry, err)
	}
	if _, err = remote.AcknowledgeMessage(ctx, p.ID, worker.Token, message.ID); err != nil {
		t.Fatal(err)
	}
	receipts, err := remote.MessageAcknowledgments(ctx, p.ID, owner.Token, message.ID)
	if err != nil || len(receipts) != 1 || receipts[0].PrincipalID != worker.PrincipalID {
		t.Fatalf("remote receipt: %+v %v", receipts, err)
	}
	remote2, err := coord.NewRemote(ts.URL, caFile, certFile, keyFile, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := remote2.GetTask(ctx, p.ID, worker.Token, task.ID)
	if err != nil || got.Status != "accepted" {
		t.Fatalf("reconnect: %+v %v", got, err)
	}
	blockedTask, err := remote.CreateTaskWithID(ctx, p.ID, owner.Token, "blocked-research", 1, "Blocked research", "Explore blocker", "research", coord.ReportContract{ReviewType: "full_code"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	blockedClaim, err := remote.ClaimTaskInWorkspace(ctx, p.ID, worker.Token, blockedTask.ID, "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	blockedTask, err = remote.TransitionTask(ctx, p.ID, worker.Token, blockedTask.ID, "block", "Need clarification", blockedClaim.Attempt)
	if err != nil || blockedTask.Status != "blocked" {
		t.Fatalf("remote block: %+v %v", blockedTask, err)
	}
	blockedTask, err = remote.TransitionTask(ctx, p.ID, owner.Token, blockedTask.ID, "resolve", "", 0)
	if err != nil || blockedTask.Status != "ready" {
		t.Fatalf("remote resolve: %+v %v", blockedTask, err)
	}
	budget, err := remote.SetBudget(ctx, p.ID, owner.Token, 0, 1000)
	if err != nil || !budget.Enabled {
		t.Fatalf("remote budget: %+v %v", budget, err)
	}
	budget, err = remote.ReserveBudget(ctx, p.ID, owner.Token, blockedTask.ID, 400)
	if err != nil || budget.Reserved != 400 {
		t.Fatalf("remote reservation: %+v %v", budget, err)
	}
	budget, err = remote.GetBudget(ctx, p.ID, worker.Token)
	if err != nil || budget.Reserved != 400 {
		t.Fatalf("remote budget read: %+v %v", budget, err)
	}
	clientCert, err := tls.X509KeyPair(clientPEM, clientKey)
	if err != nil {
		t.Fatal(err)
	}
	remoteClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{clientCert}, MinVersion: tls.VersionTLS13}}}
	request, err := http.NewRequest("POST", ts.URL+"/v1/call", bytes.NewBufferString(`{"operation":"project.create_prepared_local","name":"forbidden","description":"owner"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+owner.Token)
	request.Header.Set("Content-Type", "application/json")
	response, err := remoteClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("remote bootstrap status: %d", response.StatusCode)
	}
	unauth := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}}}
	req, _ := http.NewRequest("POST", ts.URL+"/v1/call", nil)
	if resp, err := unauth.Do(req); err == nil {
		resp.Body.Close()
		t.Fatal("server accepted a client without a certificate")
	}
	if frpc, frps := os.Getenv("GOSSIP_TEST_FRPC"), os.Getenv("GOSSIP_TEST_FRPS"); frpc != "" && frps != "" {
		t.Run("FRP", func(t *testing.T) {
			freePort := func() int {
				t.Helper()
				l, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				port := l.Addr().(*net.TCPAddr).Port
				l.Close()
				return port
			}
			serverPort, proxyPort := freePort(), freePort()
			for proxyPort == serverPort {
				proxyPort = freePort()
			}
			_, localPortText, err := net.SplitHostPort(ts.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			localPort, err := strconv.Atoi(localPortText)
			if err != nil {
				t.Fatal(err)
			}
			serverConfig := write("frps.toml", []byte(fmt.Sprintf("bindAddr = \"127.0.0.1\"\nbindPort = %d\nauth.method = \"token\"\nauth.token = \"test-only-token\"\n", serverPort)))
			clientConfig := write("frpc.toml", []byte(fmt.Sprintf("serverAddr = \"127.0.0.1\"\nserverPort = %d\nauth.method = \"token\"\nauth.token = \"test-only-token\"\n[[proxies]]\nname = \"gossip-test\"\ntype = \"tcp\"\nlocalIP = \"127.0.0.1\"\nlocalPort = %d\nremotePort = %d\n", serverPort, localPort, proxyPort)))
			start := func(bin, cfg string) *exec.Cmd {
				t.Helper()
				cmd := exec.Command(bin, "-c", cfg)
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				return cmd
			}
			stop := func(cmd *exec.Cmd) {
				if cmd != nil && cmd.Process != nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}
			waitPort := func(port int) {
				t.Helper()
				deadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
					if err == nil {
						c.Close()
						return
					}
					time.Sleep(100 * time.Millisecond)
				}
				t.Fatalf("FRP port %d did not become ready", port)
			}
			serverProcess := start(frps, serverConfig)
			defer stop(serverProcess)
			waitPort(serverPort)
			clientProcess := start(frpc, clientConfig)
			defer func() { stop(clientProcess) }()
			waitPort(proxyPort)
			throughTunnel, err := coord.NewRemote(fmt.Sprintf("https://127.0.0.1:%d", proxyPort), caFile, certFile, keyFile, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = throughTunnel.SendMessage(ctx, p.ID, worker.Token, "", "", "", "via frp"); err != nil {
				t.Fatal(err)
			}
			stop(clientProcess)
			clientProcess = start(frpc, clientConfig)
			waitPort(proxyPort)
			deadline := time.Now().Add(10 * time.Second)
			for {
				got, err := throughTunnel.GetTask(ctx, p.ID, worker.Token, task.ID)
				if err == nil && got.Status == "accepted" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("FRP reconnect: %+v %v", got, err)
				}
				time.Sleep(100 * time.Millisecond)
			}
			otherBlock, _ := pem.Decode(otherPEM)
			otherLeaf, err := x509.ParseCertificate(otherBlock.Bytes)
			if err != nil {
				t.Fatal(err)
			}
			otherDigest := sha256.Sum256(otherLeaf.Raw)
			otherFingerprint := hex.EncodeToString(otherDigest[:])
			throughTunnelOther, err := coord.NewRemote(fmt.Sprintf("https://127.0.0.1:%d", proxyPort), caFile, otherCertFile, otherKeyFile, "")
			if err != nil {
				t.Fatal(err)
			}
			preparedProject, preparedCredential, err := coord.PrepareCredential(p.ID)
			if err != nil || preparedProject != p.ID {
				t.Fatalf("prepare FRP project member: %v", err)
			}
			if _, err = throughTunnel.AddPreparedRemotePrincipal(ctx, p.ID, owner.Token, "FRP worker", "worker", preparedCredential, otherFingerprint, time.Now().Add(time.Hour).UnixMilli()); err != nil {
				t.Fatalf("combined project invite over FRP: %v", err)
			}
			if _, err = throughTunnelOther.WhoAmI(ctx, p.ID, preparedCredential.Token); err != nil {
				t.Fatalf("combined invite was not authorized over FRP: %v", err)
			}
			if err = throughTunnel.RevokeCertificateGrant(ctx, p.ID, owner.Token, otherFingerprint); err != nil {
				t.Fatalf("certificate revoke over FRP: %v", err)
			}
			if _, err = throughTunnelOther.WhoAmI(ctx, p.ID, preparedCredential.Token); !errors.Is(err, coord.ErrUnauthorized) {
				t.Fatalf("revoked certificate remained authorized through FRP: %v", err)
			}
			pairID, pairToken, err := coord.PrepareCertificateInvitation()
			if err != nil {
				t.Fatal(err)
			}
			if _, err = throughTunnel.AddPreparedCertificateInvitation(ctx, p.ID, owner.Token, pairID, pairToken, otherFingerprint, time.Now().Add(time.Hour).UnixMilli()); err != nil {
				t.Fatalf("FRP invitation creation: %v", err)
			}
			if _, err = throughTunnelOther.RedeemCertificateInvitation(ctx, p.ID, pairID, pairToken); err != nil {
				t.Fatalf("FRP invitation redemption: %v", err)
			}
			if _, err = throughTunnelOther.CurrentPlan(ctx, p.ID, owner.Token); err != nil {
				t.Fatalf("redeemed certificate failed through FRP: %v", err)
			}
			if err = throughTunnel.RevokeCertificateGrant(ctx, p.ID, owner.Token, otherFingerprint); err != nil {
				t.Fatalf("revoke paired certificate through FRP: %v", err)
			}
			if _, err = throughTunnelOther.RedeemCertificateInvitation(ctx, p.ID, pairID, pairToken); !errors.Is(err, coord.ErrUnauthorized) {
				t.Fatalf("FRP invitation replay restored revoked certificate: %v", err)
			}
		})
	}
	if err = remote.RevokePrincipal(ctx, p.ID, owner.Token, worker.PrincipalID); err != nil {
		t.Fatalf("remote owner revocation: %v", err)
	}
	if _, err = remote.CurrentPlan(ctx, p.ID, worker.Token); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("remote revoked credential remained valid: %v", err)
	}
	if err = remote.RevokeCertificateGrant(ctx, p.ID, owner.Token, fingerprint); err != nil {
		t.Fatalf("remote certificate revocation: %v", err)
	}
	if _, err = remote.CurrentPlan(ctx, p.ID, owner.Token); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("revoked certificate retained project access: %v", err)
	}
	if err = remote.LocalSetRemoteAccessEnabled(ctx, true); err == nil {
		t.Fatal("network client could control the local remote-access switch")
	}
	if err = s.SetRemoteAccessEnabled(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err = remote.CurrentPlan(ctx, p.ID, owner.Token); !errors.Is(err, coord.ErrUnavailable) {
		t.Fatalf("active remote listener did not immediately honor local kill switch: %v", err)
	}
}
