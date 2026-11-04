package sop

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tasmirz/gossip/pkg/security"
	"github.com/tasmirz/gossip/pkg/state"
)

// Artifact type constants (MetaGPT-inspired standard operating artifacts)
const (
	ArtifactPRD           = "prd"
	ArtifactSystemDesign  = "system_design"
	ArtifactTaskPlan      = "task_plan"
	ArtifactCodeManifest  = "code_manifest"
	ArtifactPeerReview    = "peer_review"
	ArtifactSecurityAudit = "security_audit"
	ArtifactReleaseNotes  = "release_notes"
)

// Workflow status constants
const (
	WorkflowStatusActive    = "active"
	WorkflowStatusBlocked   = "blocked"
	WorkflowStatusCompleted = "completed"
)

// Stage status constants
const (
	StageStatusPending    = "pending"
	StageStatusInProgress = "in_progress"
	StageStatusCompleted  = "completed"
	StageStatusBlocked    = "blocked"
)

// SOPArtifact represents a formal, versioned handoff document in an engineering SOP.
type SOPArtifact struct {
	ID         string            `json:"id"`
	WorkflowID string            `json:"workflow_id"`
	StageID    string            `json:"stage_id"`
	Type       string            `json:"type"` // prd, system_design, task_plan, code_manifest, peer_review, security_audit, release_notes
	Title      string            `json:"title"`
	Content    string            `json:"content"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	CreatedBy  string            `json:"created_by"`
	CreatedAt  time.Time         `json:"created_at"`
	FilePath   string            `json:"file_path"`
}

// SOPStage represents an individual gate or phase in a Standard Operating Procedure.
type SOPStage struct {
	ID                    string   `json:"id"`
	Name                  string   `json:"name"`
	Description           string   `json:"description"`
	RequiredRole          string   `json:"required_role"`
	RequiredArtifactTypes []string `json:"required_artifact_types"`
	Status                string   `json:"status"` // pending, in_progress, completed, blocked
	CompletedAt           *time.Time `json:"completed_at,omitempty"`
}

// SOPWorkflow models an active end-to-end multi-agent engineering lifecycle.
type SOPWorkflow struct {
	ID                string                 `json:"id"`
	Template          string                 `json:"template"`
	Title             string                 `json:"title"`
	Description       string                 `json:"description"`
	Initiator         string                 `json:"initiator"`
	Status            string                 `json:"status"` // active, blocked, completed
	CurrentStageIndex int                    `json:"current_stage_index"`
	Stages            []SOPStage             `json:"stages"`
	Artifacts         map[string]SOPArtifact `json:"artifacts"`
	CreatedAt         time.Time              `json:"created_at"`
	UpdatedAt         time.Time              `json:"updated_at"`
}

// SOPStore serializes all workflows in sop.json.
type SOPStore struct {
	Version   string                 `json:"version"`
	Workflows map[string]SOPWorkflow `json:"workflows"`
	UpdatedAt time.Time              `json:"updated_at"`
}

// Manager coordinates SOP execution, phase gate transitions, and artifact storage.
type Manager struct {
	store *state.Store
}

// NewManager creates an SOP manager backed by state.Store.
func NewManager(store *state.Store) *Manager {
	return &Manager{store: store}
}

func (m *Manager) sopFilePath() string {
	return filepath.Join(m.store.Dir(), "sop.json")
}

func (m *Manager) artifactsDir() string {
	return filepath.Join(m.store.Dir(), "artifacts")
}

func (m *Manager) pipelineFilePath() string {
	return filepath.Join(m.store.Dir(), "SOP_PIPELINE.md")
}

// Load loads sop.json or initializes an empty container.
func (m *Manager) Load() (*SOPStore, error) {
	data, err := os.ReadFile(m.sopFilePath())
	if err != nil {
		if os.IsNotExist(err) {
			return &SOPStore{
				Version:   "1.0.0",
				Workflows: make(map[string]SOPWorkflow),
				UpdatedAt: time.Now(),
			}, nil
		}
		return nil, err
	}

	var s SOPStore
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("corrupted sop.json: %w", err)
	}
	if s.Workflows == nil {
		s.Workflows = make(map[string]SOPWorkflow)
	}
	return &s, nil
}

// Save atomically writes sop.json and updates SOP_PIPELINE.md.
func (m *Manager) Save(s *SOPStore) error {
	s.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	if err := m.store.AtomicWriteFile("sop.json", data); err != nil {
		return err
	}

	// Regenerate SOP_PIPELINE.md
	pipeline := m.buildPipelineReport(s)
	_ = m.store.AtomicWriteFile("SOP_PIPELINE.md", []byte(pipeline))

	return nil
}

// GetWorkflowTemplates returns the available SOP workflows.
func GetWorkflowTemplates() map[string][]SOPStage {
	return map[string][]SOPStage{
		"feature_delivery": {
			{
				ID:                    "requirements",
				Name:                  "1. Requirements & PRD",
				Description:           "Product Requirements Document outlining user stories, scope, and acceptance criteria.",
				RequiredRole:          "orchestrator",
				RequiredArtifactTypes: []string{ArtifactPRD},
				Status:                StageStatusInProgress,
			},
			{
				ID:                    "architecture",
				Name:                  "2. System Architecture & API Design",
				Description:           "System decomposition, data models, interface definitions, and dependency graph.",
				RequiredRole:          "systems_architect",
				RequiredArtifactTypes: []string{ArtifactSystemDesign},
				Status:                StageStatusPending,
			},
			{
				ID:                    "planning",
				Name:                  "3. Work Breakdown & Task DAG",
				Description:           "Scheduling tasks, dependencies, milestone targets, and role assignments.",
				RequiredRole:          "orchestrator",
				RequiredArtifactTypes: []string{ArtifactTaskPlan},
				Status:                StageStatusPending,
			},
			{
				ID:                    "implementation",
				Name:                  "4. Code Implementation",
				Description:           "Executing tasks, writing production code, and delivering runnable modules.",
				RequiredRole:          "worker",
				RequiredArtifactTypes: []string{ArtifactCodeManifest},
				Status:                StageStatusPending,
			},
			{
				ID:                    "qa_review",
				Name:                  "5. Peer Review & QA Verification",
				Description:           "Rigorous code inspection, unit/integration verification, and logic critique.",
				RequiredRole:          "qa_engineer",
				RequiredArtifactTypes: []string{ArtifactPeerReview},
				Status:                StageStatusPending,
			},
			{
				ID:                    "security_audit",
				Name:                  "6. Red-Team STRIDE Security Audit",
				Description:           "Adversarial testing against spoofing, tampering, information disclosure, and race conditions.",
				RequiredRole:          "security_specialist",
				RequiredArtifactTypes: []string{ArtifactSecurityAudit},
				Status:                StageStatusPending,
			},
			{
				ID:                    "signoff",
				Name:                  "7. Release Signoff & Documentation",
				Description:           "Final gate approval, changelog consolidation, and release delivery.",
				RequiredRole:          "orchestrator",
				RequiredArtifactTypes: []string{ArtifactReleaseNotes},
				Status:                StageStatusPending,
			},
		},
		"bug_investigation": {
			{
				ID:                    "incident_triage",
				Name:                  "1. Incident Triage & Reproduction",
				Description:           "Clarifying defect behavior, reproduction steps, and impact assessment.",
				RequiredRole:          "orchestrator",
				RequiredArtifactTypes: []string{ArtifactPRD},
				Status:                StageStatusInProgress,
			},
			{
				ID:                    "blast_radius",
				Name:                  "2. Root Cause & Blast Radius",
				Description:           "Investigating dependency call graph, affected nodes, and failure invariants.",
				RequiredRole:          "systems_architect",
				RequiredArtifactTypes: []string{ArtifactSystemDesign},
				Status:                StageStatusPending,
			},
			{
				ID:                    "fix_patch",
				Name:                  "3. Fix Implementation",
				Description:           "Applying minimal, robust code patch to address root cause.",
				RequiredRole:          "worker",
				RequiredArtifactTypes: []string{ArtifactCodeManifest},
				Status:                StageStatusPending,
			},
			{
				ID:                    "regression_test",
				Name:                  "4. QA Regression Verification",
				Description:           "Executing regression suites on all affected components.",
				RequiredRole:          "qa_engineer",
				RequiredArtifactTypes: []string{ArtifactPeerReview},
				Status:                StageStatusPending,
			},
			{
				ID:                    "resolution_signoff",
				Name:                  "5. Resolution Signoff",
				Description:           "Confirming fix resolution and archiving post-mortem notes.",
				RequiredRole:          "orchestrator",
				RequiredArtifactTypes: []string{ArtifactReleaseNotes},
				Status:                StageStatusPending,
			},
		},
		"security_hardening": {
			{
				ID:                    "threat_modeling",
				Name:                  "1. STRIDE Threat Modeling",
				Description:           "Mapping attack surfaces and defining security invariants.",
				RequiredRole:          "security_specialist",
				RequiredArtifactTypes: []string{ArtifactSystemDesign},
				Status:                StageStatusInProgress,
			},
			{
				ID:                    "red_team_probing",
				Name:                  "2. Adversarial Penetration Probing",
				Description:           "Executing fuzzing, race injection, privilege escalation, and payload testing.",
				RequiredRole:          "security_specialist",
				RequiredArtifactTypes: []string{ArtifactSecurityAudit},
				Status:                StageStatusPending,
			},
			{
				ID:                    "remediation_patching",
				Name:                  "3. Defensive Hardening & Patching",
				Description:           "Implementing defensive barriers, sanitization, and locking controls.",
				RequiredRole:          "worker",
				RequiredArtifactTypes: []string{ArtifactCodeManifest},
				Status:                StageStatusPending,
			},
			{
				ID:                    "security_signoff",
				Name:                  "4. Security Clearance Signoff",
				Description:           "Re-probing targets and certifying 100% mitigation of identified vulnerabilities.",
				RequiredRole:          "security_specialist",
				RequiredArtifactTypes: []string{ArtifactReleaseNotes},
				Status:                StageStatusPending,
			},
		},
	}
}

// StartWorkflow initiates a new Standard Operating Procedure instance.
func (m *Manager) StartWorkflow(templateName, title, description, initiator string) (*SOPWorkflow, error) {
	templates := GetWorkflowTemplates()
	stages, exists := templates[templateName]
	if !exists {
		return nil, fmt.Errorf("unknown SOP template %q; available templates: feature_delivery, bug_investigation, security_hardening", templateName)
	}

	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	s, err := m.Load()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	wfID := fmt.Sprintf("sop-%s-%d", templateName, now.Unix())

	// Deep copy stages
	stagesCopy := make([]SOPStage, len(stages))
	copy(stagesCopy, stages)

	wf := SOPWorkflow{
		ID:                wfID,
		Template:          templateName,
		Title:             title,
		Description:       description,
		Initiator:         initiator,
		Status:            WorkflowStatusActive,
		CurrentStageIndex: 0,
		Stages:            stagesCopy,
		Artifacts:         make(map[string]SOPArtifact),
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	s.Workflows[wfID] = wf
	if err := m.Save(s); err != nil {
		return nil, err
	}

	return &wf, nil
}

// SaveArtifact saves a formal SOP deliverable document, ensuring directory structure exists.
func (m *Manager) SaveArtifact(workflowID, stageID, artifactType, title, content, createdBy string, metadata map[string]string) (*SOPArtifact, error) {
	// Security bounds check
	if err := security.ValidatePayloadSize(content, security.MaxArtifactSizeBytes); err != nil {
		return nil, fmt.Errorf("artifact size violation: %w", err)
	}

	sanitizedWfID, err := security.SanitizeID(workflowID, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid workflow_id: %w", err)
	}
	sanitizedStageID, err := security.SanitizeID(stageID, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid stage_id: %w", err)
	}
	sanitizedArtType, err := security.SanitizeID(artifactType, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid artifact_type: %w", err)
	}

	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	s, err := m.Load()
	if err != nil {
		return nil, err
	}

	wf, exists := s.Workflows[sanitizedWfID]
	if !exists {
		return nil, fmt.Errorf("workflow %q not found", sanitizedWfID)
	}

	// Ensure artifacts dir exists
	artDir := m.artifactsDir()
	if err := os.MkdirAll(artDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create artifacts dir: %w", err)
	}

	now := time.Now()
	artID := fmt.Sprintf("%s_%s_%s", sanitizedWfID, sanitizedStageID, sanitizedArtType)
	fileName := fmt.Sprintf("%s.md", artID)
	fullPath, err := security.ValidateSafePath(artDir, fileName)
	if err != nil {
		return nil, fmt.Errorf("artifact path traversal rejected: %w", err)
	}

	// Format artifact with metadata header
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# [SOP ARTIFACT] %s\n\n", title))
	b.WriteString(fmt.Sprintf("- **Workflow:** `%s` (%s)\n", wf.ID, wf.Title))
	b.WriteString(fmt.Sprintf("- **Stage:** `%s`\n", stageID))
	b.WriteString(fmt.Sprintf("- **Type:** `%s`\n", artifactType))
	b.WriteString(fmt.Sprintf("- **Author:** `%s`\n", createdBy))
	b.WriteString(fmt.Sprintf("- **Timestamp:** %s\n\n", now.Format(time.RFC3339)))
	b.WriteString("---\n\n")
	b.WriteString(content)

	if err := os.WriteFile(fullPath, []byte(b.String()), 0644); err != nil {
		return nil, fmt.Errorf("failed to write artifact file: %w", err)
	}

	relPath := filepath.Join(".local", "gossip", "artifacts", fileName)
	art := SOPArtifact{
		ID:         artID,
		WorkflowID: workflowID,
		StageID:    stageID,
		Type:       artifactType,
		Title:      title,
		Content:    content,
		Metadata:   metadata,
		CreatedBy:  createdBy,
		CreatedAt:  now,
		FilePath:   relPath,
	}

	wf.Artifacts[artID] = art
	wf.UpdatedAt = now
	s.Workflows[workflowID] = wf

	if err := m.Save(s); err != nil {
		return nil, err
	}

	return &art, nil
}

// AdvanceStage validates current stage deliverables and transitions workflow to the next gate.
func (m *Manager) AdvanceStage(workflowID, agentID, role string) (*SOPWorkflow, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	s, err := m.Load()
	if err != nil {
		return nil, err
	}

	wf, exists := s.Workflows[workflowID]
	if !exists {
		return nil, fmt.Errorf("workflow %q not found", workflowID)
	}

	if wf.Status == WorkflowStatusCompleted {
		return nil, fmt.Errorf("workflow %q is already completed", workflowID)
	}

	currIdx := wf.CurrentStageIndex
	if currIdx >= len(wf.Stages) {
		wf.Status = WorkflowStatusCompleted
		s.Workflows[workflowID] = wf
		_ = m.Save(s)
		return &wf, nil
	}

	currStage := &wf.Stages[currIdx]

	// Role check (allow orchestrator override)
	if role != "orchestrator" && currStage.RequiredRole != "" && !strings.EqualFold(role, currStage.RequiredRole) {
		return nil, fmt.Errorf("stage %q requires role %q (caller role is %q)", currStage.Name, currStage.RequiredRole, role)
	}

	// Artifact check: verify all required artifact types have been submitted for this stage
	submittedTypes := make(map[string]bool)
	for _, art := range wf.Artifacts {
		if art.StageID == currStage.ID {
			submittedTypes[art.Type] = true
		}
	}

	var missingTypes []string
	for _, reqType := range currStage.RequiredArtifactTypes {
		if !submittedTypes[reqType] {
			missingTypes = append(missingTypes, reqType)
		}
	}

	if len(missingTypes) > 0 {
		return nil, fmt.Errorf("cannot advance stage %q: missing required artifact(s): [%s]",
			currStage.Name, strings.Join(missingTypes, ", "))
	}

	now := time.Now()
	currStage.Status = StageStatusCompleted
	currStage.CompletedAt = &now

	// Advance to next stage or complete workflow
	if currIdx+1 < len(wf.Stages) {
		wf.CurrentStageIndex++
		wf.Stages[wf.CurrentStageIndex].Status = StageStatusInProgress
	} else {
		wf.Status = WorkflowStatusCompleted
	}

	wf.UpdatedAt = now
	s.Workflows[workflowID] = wf

	if err := m.Save(s); err != nil {
		return nil, err
	}

	return &wf, nil
}

// GetWorkflow retrieves an active workflow by ID.
func (m *Manager) GetWorkflow(workflowID string) (*SOPWorkflow, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	s, err := m.Load()
	if err != nil {
		return nil, err
	}

	wf, exists := s.Workflows[workflowID]
	if !exists {
		return nil, fmt.Errorf("workflow %q not found", workflowID)
	}
	return &wf, nil
}

// ListWorkflows returns all registered workflows.
func (m *Manager) ListWorkflows() ([]SOPWorkflow, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	s, err := m.Load()
	if err != nil {
		return nil, err
	}

	list := make([]SOPWorkflow, 0, len(s.Workflows))
	for _, wf := range s.Workflows {
		list = append(list, wf)
	}
	return list, nil
}

func (m *Manager) buildPipelineReport(s *SOPStore) string {
	var b strings.Builder
	b.WriteString("# [SOP] Gossip Standard Operating Procedures Pipeline\n\n")
	b.WriteString(fmt.Sprintf("*Synchronized: %s | Active Workflows: %d*\n\n",
		time.Now().Format(time.RFC1123), len(s.Workflows)))

	if len(s.Workflows) == 0 {
		b.WriteString("_No active SOP workflows running. Start one using `gossip_sop_start`._\n")
		return b.String()
	}

	for _, wf := range s.Workflows {
		b.WriteString(fmt.Sprintf("## [WORKFLOW] %s (`%s`)\n", wf.Title, wf.ID))
		b.WriteString(fmt.Sprintf("**Template:** `%s` | **Status:** **%s** | **Initiator:** `%s`\n\n",
			wf.Template, strings.ToUpper(wf.Status), wf.Initiator))

		b.WriteString("| Step | Phase Gate | Required Role | Status | Completed At |\n")
		b.WriteString("| :---: | :--- | :--- | :--- | :--- |\n")

		for idx, st := range wf.Stages {
			statusIcon := "[PENDING]"
			switch st.Status {
			case StageStatusInProgress:
				statusIcon = "[IN_PROGRESS]"
			case StageStatusCompleted:
				statusIcon = "[COMPLETED]"
			case StageStatusBlocked:
				statusIcon = "[BLOCKED]"
			}

			completedStr := "-"
			if st.CompletedAt != nil {
				completedStr = st.CompletedAt.Format("15:04:05 MST")
			}

			b.WriteString(fmt.Sprintf("| %d | **%s** | `%s` | %s | %s |\n",
				idx+1, st.Name, st.RequiredRole, statusIcon, completedStr))
		}
		b.WriteString("\n")

		// Artifacts in this workflow
		b.WriteString("### [ARTIFACTS] Associated Artifacts\n\n")
		if len(wf.Artifacts) == 0 {
			b.WriteString("_No artifacts filed yet for this workflow._\n\n")
		} else {
			b.WriteString("| Artifact Type | Title | Author | Path |\n")
			b.WriteString("| :--- | :--- | :--- | :--- |\n")
			for _, art := range wf.Artifacts {
				b.WriteString(fmt.Sprintf("| `%s` | %s | `%s` | [`%s`](%s) |\n",
					art.Type, art.Title, art.CreatedBy, art.FilePath, art.FilePath))
			}
			b.WriteString("\n")
		}
		b.WriteString("---\n\n")
	}

	return b.String()
}
