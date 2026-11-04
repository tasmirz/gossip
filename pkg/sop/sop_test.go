package sop_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tasmirz/gossip/pkg/sop"
	"github.com/tasmirz/gossip/pkg/state"
)

func TestSOPWorkflowLifecycle(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "gossip-sop-test")
	_ = os.RemoveAll(tmpDir)
	defer os.RemoveAll(tmpDir)

	store, err := state.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	mgr := sop.NewManager(store)

	// 1. Start Feature Delivery Workflow
	wf, err := mgr.StartWorkflow("feature_delivery", "User Authentication Gateway", "Deliver robust JWT auth service", "agent:pm")
	if err != nil {
		t.Fatalf("failed to start workflow: %v", err)
	}

	if wf.CurrentStageIndex != 0 {
		t.Fatalf("expected stage index 0, got %d", wf.CurrentStageIndex)
	}
	if wf.Stages[0].ID != "requirements" {
		t.Fatalf("expected first stage 'requirements', got %s", wf.Stages[0].ID)
	}

	// 2. Attempt to advance without PRD artifact (should fail)
	_, err = mgr.AdvanceStage(wf.ID, "agent:pm", "orchestrator")
	if err == nil {
		t.Fatalf("expected failure when advancing without required artifact, got nil")
	}
	if !strings.Contains(err.Error(), "missing required artifact") {
		t.Fatalf("expected missing artifact error, got: %v", err)
	}

	// 3. Save PRD Artifact
	prdContent := `## Goal
Provide secure OAuth2 and JWT token validation for all incoming microservice requests.

## User Stories
- As a user, I can log in with username/password and receive a signed JWT.
- As an internal service, I can verify the JWT signature offline using RSA public keys.
`
	art, err := mgr.SaveArtifact(wf.ID, "requirements", sop.ArtifactPRD, "User Authentication PRD", prdContent, "agent:pm", nil)
	if err != nil {
		t.Fatalf("failed to save PRD artifact: %v", err)
	}

	if art.FilePath == "" {
		t.Fatalf("expected non-empty artifact file path")
	}

	// Verify file was written to disk
	absPath := filepath.Join(tmpDir, "artifacts", filepath.Base(art.FilePath))
	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		t.Fatalf("expected artifact file to exist at %s", absPath)
	}

	// 4. Advance Stage to Architecture
	wf, err = mgr.AdvanceStage(wf.ID, "agent:pm", "orchestrator")
	if err != nil {
		t.Fatalf("failed to advance stage after PRD: %v", err)
	}

	if wf.CurrentStageIndex != 1 {
		t.Fatalf("expected stage index 1 (architecture), got %d", wf.CurrentStageIndex)
	}
	if wf.Stages[1].ID != "architecture" {
		t.Fatalf("expected stage 'architecture', got %s", wf.Stages[1].ID)
	}
	if wf.Stages[0].Status != sop.StageStatusCompleted {
		t.Fatalf("expected first stage to be completed, got %s", wf.Stages[0].Status)
	}

	// 5. Verify SOP_PIPELINE.md was generated
	pipelineFile := filepath.Join(tmpDir, "SOP_PIPELINE.md")
	content, err := os.ReadFile(pipelineFile)
	if err != nil {
		t.Fatalf("failed to read SOP_PIPELINE.md: %v", err)
	}

	if !strings.Contains(string(content), "User Authentication Gateway") {
		t.Fatalf("expected SOP_PIPELINE.md to contain workflow title")
	}
	t.Logf("SOP Pipeline excerpt:\n%s", string(content)[:400])
}
