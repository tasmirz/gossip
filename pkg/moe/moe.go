package moe

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/tasmirz/gossip/pkg/roles"
	"github.com/tasmirz/gossip/pkg/state"
)

// ExpertSelection describes a specialist role selected by the MoE gating mechanism.
type ExpertSelection struct {
	Role       string  `json:"role"`
	Rationale  string  `json:"rationale"`
	Confidence float64 `json:"confidence"`
}

// ExpertArgument holds a domain-specific contribution from a panel expert.
type ExpertArgument struct {
	ID          string    `json:"id"`
	Role        string    `json:"role"`
	AgentID     string    `json:"agent_id"`
	Stance      string    `json:"stance"` // propose, counter, caution, endorse
	Perspective string    `json:"perspective"`
	KeyPoints   []string  `json:"key_points"`
	CreatedAt   time.Time `json:"created_at"`
}

// WorkPackage represents a modular, parallelizable work item produced by the MoE panel.
type WorkPackage struct {
	PackageID        string   `json:"package_id"`
	Title            string   `json:"title"`
	Description      string   `json:"description"`
	AssignedRole     string   `json:"assigned_role"`
	Dependencies     []string `json:"dependencies"`
	CanRunInParallel bool     `json:"can_run_in_parallel"`
	SuggestedTaskID  string   `json:"suggested_task_id,omitempty"`
}

// MoESynthesis captures the unified decision and parallel execution plan.
type MoESynthesis struct {
	ConsensusDecision    string        `json:"consensus_decision"`
	TradeOffs            []string      `json:"trade_offs"`
	RecommendedActions   []string      `json:"recommended_actions"`
	ParallelWorkPackages []WorkPackage `json:"parallel_work_packages"`
	SynthesizedBy        string        `json:"synthesized_by"`
	SynthesizedAt        time.Time     `json:"synthesized_at"`
}

// SubagentTrack tracks concurrent background execution of an assigned work package.
type SubagentTrack struct {
	TrackID     string    `json:"track_id"`
	PackageID   string    `json:"package_id"`
	TaskID      string    `json:"task_id"`
	Role        string    `json:"role"`
	AgentID     string    `json:"agent_id"`
	Status      string    `json:"status"` // pending, running, completed, failed
	Artifact    string    `json:"artifact,omitempty"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
}

// MoEPanel represents a multi-expert discussion council and subagent dispatch coordinator.
type MoEPanel struct {
	ID              string            `json:"id"`
	Title           string            `json:"title"`
	Query           string            `json:"query"`
	Domain          string            `json:"domain"`
	Status          string            `json:"status"` // open, deliberating, synthesized, executing, completed
	Initiator       string            `json:"initiator"`
	SelectedExperts []ExpertSelection `json:"selected_experts"`
	Arguments       []ExpertArgument  `json:"arguments"`
	Synthesis       *MoESynthesis     `json:"synthesis,omitempty"`
	SubagentTracks  []SubagentTrack   `json:"subagent_tracks,omitempty"`
	MaxParallel     int               `json:"max_parallel"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

// MoEContainer serializes all discussion panels into moe_panels.json.
type MoEContainer struct {
	Panels map[string]MoEPanel `json:"panels"`
}

// Manager orchestrates Mixture of Experts panels and parallel subagent delegations.
type Manager struct {
	store    *state.Store
	roleProv roles.Provider
}

// NewManager creates an MoE Manager with dependency inversion.
func NewManager(store *state.Store, roleProv roles.Provider) *Manager {
	if roleProv == nil {
		roleProv = roles.GetDefaultService()
	}
	return &Manager{
		store:    store,
		roleProv: roleProv,
	}
}

func (m *Manager) filePath() string {
	return filepath.Join(m.store.Dir(), "moe_panels.json")
}

// Load reads all panels from storage.
func (m *Manager) Load() (*MoEContainer, error) {
	bytes, err := m.store.ReadFile("moe_panels.json")
	if err != nil {
		return &MoEContainer{Panels: make(map[string]MoEPanel)}, nil
	}
	var container MoEContainer
	if err := json.Unmarshal(bytes, &container); err != nil {
		return &MoEContainer{Panels: make(map[string]MoEPanel)}, nil
	}
	if container.Panels == nil {
		container.Panels = make(map[string]MoEPanel)
	}
	return &container, nil
}

// Save persists all panels with flock concurrency control.
func (m *Manager) Save(c *MoEContainer) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return m.store.AtomicWriteFile("moe_panels.json", data)
}

// GatingFunction analyzes a problem query and selects the top-K specialist roles.
func (m *Manager) GatingFunction(query, domain string, requestedRoles []string) []ExpertSelection {
	if len(requestedRoles) > 0 {
		var selected []ExpertSelection
		for _, r := range requestedRoles {
			selected = append(selected, ExpertSelection{
				Role:       r,
				Rationale:  "Explicitly summoned by orchestrator",
				Confidence: 1.0,
			})
		}
		return selected
	}

	q := strings.ToLower(query + " " + domain)
	candidates := make(map[string]ExpertSelection)

	// Keyword-based MoE router heuristics
	if strings.Contains(q, "network") || strings.Contains(q, "protocol") || strings.Contains(q, "socket") || strings.Contains(q, "latency") || strings.Contains(q, "http") || strings.Contains(q, "grpc") {
		candidates["network_engineer"] = ExpertSelection{
			Role:       "network_engineer",
			Rationale:  "High domain relevance to distributed networking and communication protocols",
			Confidence: 0.95,
		}
	}
	if strings.Contains(q, "math") || strings.Contains(q, "hash") || strings.Contains(q, "crypto") || strings.Contains(q, "crc") || strings.Contains(q, "complexity") || strings.Contains(q, "proof") || strings.Contains(q, "algorithm") {
		candidates["math_expert"] = ExpertSelection{
			Role:       "math_expert",
			Rationale:  "Relevant to algorithmic complexity, polynomial guarantees, and mathematical proofs",
			Confidence: 0.92,
		}
	}
	if strings.Contains(q, "concurrency") || strings.Contains(q, "lock") || strings.Contains(q, "race") || strings.Contains(q, "memory") || strings.Contains(q, "flock") || strings.Contains(q, "architecture") || strings.Contains(q, "deadlock") {
		candidates["systems_architect"] = ExpertSelection{
			Role:       "systems_architect",
			Rationale:  "Critical for concurrency invariants, memory models, and crash consistency",
			Confidence: 0.96,
		}
	}
	if strings.Contains(q, "security") || strings.Contains(q, "vulnerability") || strings.Contains(q, "sanitize") || strings.Contains(q, "injection") || strings.Contains(q, "stride") || strings.Contains(q, "threat") || strings.Contains(q, "attack") {
		candidates["security_specialist"] = ExpertSelection{
			Role:       "security_specialist",
			Rationale:  "Essential for STRIDE threat vector defense and sanitization invariants",
			Confidence: 0.94,
		}
	}
	if strings.Contains(q, "test") || strings.Contains(q, "qa") || strings.Contains(q, "boundary") || strings.Contains(q, "edge case") || strings.Contains(q, "benchmark") {
		candidates["qa_engineer"] = ExpertSelection{
			Role:       "qa_engineer",
			Rationale:  "Responsible for test harness design and regression coverage",
			Confidence: 0.88,
		}
	}
	if strings.Contains(q, "probe") || strings.Contains(q, "red team") || strings.Contains(q, "fuzz") || strings.Contains(q, "adversarial") {
		candidates["red_team"] = ExpertSelection{
			Role:       "red_team",
			Rationale:  "Provides adversarial critique and stress-tests assumptions",
			Confidence: 0.90,
		}
	}

	// Always ensure systems_architect and security_specialist are considered if too few
	if len(candidates) < 2 {
		candidates["systems_architect"] = ExpertSelection{
			Role:       "systems_architect",
			Rationale:  "Core systems baseline evaluation",
			Confidence: 0.80,
		}
		candidates["security_specialist"] = ExpertSelection{
			Role:       "security_specialist",
			Rationale:  "Security boundary baseline review",
			Confidence: 0.80,
		}
	}
	// Always include critic if high stakes
	candidates["critic"] = ExpertSelection{
		Role:       "critic",
		Rationale:  "Independent dialectic evaluation and balance arbiter",
		Confidence: 0.85,
	}

	var results []ExpertSelection
	for _, sel := range candidates {
		results = append(results, sel)
	}
	return results
}

// ConvenePanel convenes a new Mixture of Experts discussion council.
func (m *Manager) ConvenePanel(title, query, domain, initiator string, requestedRoles []string, maxParallel int) (*MoEPanel, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	c, err := m.Load()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	panelID := fmt.Sprintf("moe-%d", now.UnixNano())
	if maxParallel <= 0 {
		maxParallel = 4 // default 4 concurrent parallel subagents
	}

	experts := m.GatingFunction(query, domain, requestedRoles)

	panel := MoEPanel{
		ID:              panelID,
		Title:           title,
		Query:           query,
		Domain:          domain,
		Status:          "deliberating",
		Initiator:       initiator,
		SelectedExperts: experts,
		Arguments:       []ExpertArgument{},
		MaxParallel:     maxParallel,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	c.Panels[panelID] = panel
	if err := m.Save(c); err != nil {
		return nil, err
	}

	return &panel, nil
}

// PostArgument records an expert domain perspective on an active panel.
func (m *Manager) PostArgument(panelID, agentID, role, stance, perspective string, keyPoints []string) (*ExpertArgument, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	c, err := m.Load()
	if err != nil {
		return nil, err
	}

	panel, exists := c.Panels[panelID]
	if !exists {
		return nil, fmt.Errorf("panel %s not found", panelID)
	}

	now := time.Now()
	arg := ExpertArgument{
		ID:          fmt.Sprintf("arg-%d", now.UnixNano()),
		Role:        role,
		AgentID:     agentID,
		Stance:      stance,
		Perspective: perspective,
		KeyPoints:   keyPoints,
		CreatedAt:   now,
	}

	panel.Arguments = append(panel.Arguments, arg)
	panel.UpdatedAt = now
	c.Panels[panelID] = panel

	if err := m.Save(c); err != nil {
		return nil, err
	}

	return &arg, nil
}

// Synthesize creates the unified consensus synthesis and extracts parallel work packages.
func (m *Manager) Synthesize(panelID, agentID, consensusDecision string, tradeOffs, recommendedActions []string, packages []WorkPackage) (*MoESynthesis, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	c, err := m.Load()
	if err != nil {
		return nil, err
	}

	panel, exists := c.Panels[panelID]
	if !exists {
		return nil, fmt.Errorf("panel %s not found", panelID)
	}

	now := time.Now()
	synthesis := MoESynthesis{
		ConsensusDecision:    consensusDecision,
		TradeOffs:            tradeOffs,
		RecommendedActions:   recommendedActions,
		ParallelWorkPackages: packages,
		SynthesizedBy:        agentID,
		SynthesizedAt:        now,
	}

	panel.Synthesis = &synthesis
	panel.Status = "synthesized"
	panel.UpdatedAt = now
	c.Panels[panelID] = panel

	if err := m.Save(c); err != nil {
		return nil, err
	}

	return &synthesis, nil
}

// DispatchSubagents converts parallel work packages into active subagent execution tracks.
func (m *Manager) DispatchSubagents(panelID, agentID string) ([]SubagentTrack, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	c, err := m.Load()
	if err != nil {
		return nil, err
	}

	panel, exists := c.Panels[panelID]
	if !exists {
		return nil, fmt.Errorf("panel %s not found", panelID)
	}

	if panel.Synthesis == nil {
		return nil, fmt.Errorf("panel %s must be synthesized before dispatching subagents", panelID)
	}

	now := time.Now()
	var newTracks []SubagentTrack

	for i, pkg := range panel.Synthesis.ParallelWorkPackages {
		trackID := fmt.Sprintf("track-%s-%d", panel.ID, i+1)
		track := SubagentTrack{
			TrackID:   trackID,
			PackageID: pkg.PackageID,
			TaskID:    pkg.SuggestedTaskID,
			Role:      pkg.AssignedRole,
			AgentID:   fmt.Sprintf("subagent-%s-%d", pkg.AssignedRole, i+1),
			Status:    "running",
			StartedAt: now,
		}
		newTracks = append(newTracks, track)
	}

	panel.SubagentTracks = append(panel.SubagentTracks, newTracks...)
	panel.Status = "executing"
	panel.UpdatedAt = now
	c.Panels[panelID] = panel

	if err := m.Save(c); err != nil {
		return nil, err
	}

	return newTracks, nil
}

// GetPanel retrieves a specific panel by ID.
func (m *Manager) GetPanel(panelID string) (*MoEPanel, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	c, err := m.Load()
	if err != nil {
		return nil, err
	}

	panel, exists := c.Panels[panelID]
	if !exists {
		return nil, fmt.Errorf("panel %s not found", panelID)
	}
	return &panel, nil
}

// ListPanels retrieves all discussion panels, optionally filtered by status.
func (m *Manager) ListPanels(statusFilter string) ([]MoEPanel, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	c, err := m.Load()
	if err != nil {
		return nil, err
	}

	var list []MoEPanel
	for _, p := range c.Panels {
		if statusFilter == "" || p.Status == statusFilter {
			list = append(list, p)
		}
	}
	return list, nil
}

// RenderMarkdown creates a formatted markdown view of an MoE panel.
func (p *MoEPanel) RenderMarkdown() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## 🧠 Mixture of Experts Council: %s (`%s`)\n\n", p.Title, p.ID))
	sb.WriteString(fmt.Sprintf("- **Domain**: `%s` | **Status**: `%s` | **Max Parallel Subagents**: %d\n", p.Domain, p.Status, p.MaxParallel))
	sb.WriteString(fmt.Sprintf("- **Problem Statement**: %s\n\n", p.Query))

	sb.WriteString("### 👥 Selected Expert Panel\n")
	for _, e := range p.SelectedExperts {
		sb.WriteString(fmt.Sprintf("- **`%s`** (Confidence: %.0f%%): %s\n", e.Role, e.Confidence*100, e.Rationale))
	}
	sb.WriteString("\n")

	sb.WriteString("### 💬 Expert Perspectives & Deliberation\n")
	if len(p.Arguments) == 0 {
		sb.WriteString("*Awaiting expert contributions...*\n\n")
	} else {
		for _, arg := range p.Arguments {
			sb.WriteString(fmt.Sprintf("#### [%s] %s (`%s`)\n", strings.ToUpper(arg.Stance), arg.Role, arg.AgentID))
			sb.WriteString(fmt.Sprintf("%s\n\n", arg.Perspective))
			if len(arg.KeyPoints) > 0 {
				sb.WriteString("**Key Points:**\n")
				for _, kp := range arg.KeyPoints {
					sb.WriteString(fmt.Sprintf("- %s\n", kp))
				}
				sb.WriteString("\n")
			}
		}
	}

	if p.Synthesis != nil {
		sb.WriteString("### ⚖️ MoE Council Synthesis & Decision\n")
		sb.WriteString(fmt.Sprintf("**Consensus**: %s\n\n", p.Synthesis.ConsensusDecision))

		if len(p.Synthesis.TradeOffs) > 0 {
			sb.WriteString("**Architectural Trade-offs Identified:**\n")
			for _, to := range p.Synthesis.TradeOffs {
				sb.WriteString(fmt.Sprintf("- %s\n", to))
			}
			sb.WriteString("\n")
		}

		if len(p.Synthesis.ParallelWorkPackages) > 0 {
			sb.WriteString("### ⚡ Parallel Subagent Work Packages\n")
			for _, pkg := range p.Synthesis.ParallelWorkPackages {
				parallelStr := "Sequential"
				if pkg.CanRunInParallel {
					parallelStr = "⚡ Concurrent Parallel"
				}
				sb.WriteString(fmt.Sprintf("- **`[%s]` %s** (Role: `%s`, Mode: %s)\n", pkg.PackageID, pkg.Title, pkg.AssignedRole, parallelStr))
				sb.WriteString(fmt.Sprintf("  - %s\n", pkg.Description))
			}
			sb.WriteString("\n")
		}
	}

	if len(p.SubagentTracks) > 0 {
		sb.WriteString("### 🚀 Active Subagent Parallel Execution Tracks\n")
		for _, track := range p.SubagentTracks {
			sb.WriteString(fmt.Sprintf("- Track `%s` -> Agent `%s` (%s) [%s]\n", track.TrackID, track.AgentID, track.Role, track.Status))
		}
	}

	return sb.String()
}
