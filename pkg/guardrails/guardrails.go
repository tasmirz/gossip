package guardrails

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tasmirz/gossip/pkg/state"
)

// Caution levels
const (
	LevelCritical = "critical"
	LevelWarning  = "warning"
	LevelInfo     = "info"
)

// Enforcement actions
const (
	EnforceBlock             = "block"
	EnforceRequirePeerReview = "require_peer_review"
	EnforceWarn              = "warn"
)

// RoleGuardrail defines permissions, forbidden actions, and cautions for a project role.
type RoleGuardrail struct {
	RoleID                string   `json:"role_id"`
	Title                 string   `json:"title"`
	Description           string   `json:"description"`
	SystemPrompt          string   `json:"system_prompt"`
	PermittedActions      []string `json:"permitted_actions"`
	ForbiddenActions      []string `json:"forbidden_actions"`
	CautionDirectives     []string `json:"caution_directives"`
	SensitivePathPatterns []string `json:"sensitive_path_patterns"`
}

// CautionRule represents an actionable safety guardrail or constraint.
type CautionRule struct {
	ID             string `json:"id"`
	Category       string `json:"category"` // security, concurrency, data_loss, regression
	Level          string `json:"level"`    // critical, warning, info
	TriggerPattern string `json:"trigger_pattern"`
	Directive      string `json:"directive"`
	Enforcement    string `json:"enforcement"` // block, require_peer_review, warn
}

// ProjectGuardrails contains the complete guardrail registry and database for a project.
type ProjectGuardrails struct {
	Version     string                   `json:"version"`
	ProjectName string                   `json:"project_name"`
	TechStack   []string                 `json:"tech_stack"`
	Invariants  []string                 `json:"invariants"`
	Roles       map[string]RoleGuardrail `json:"roles"`
	Cautions    []CautionRule            `json:"cautions"`
	CreatedAt   time.Time                `json:"created_at"`
	UpdatedAt   time.Time                `json:"updated_at"`
}

// ValidationResult represents the outcome of a pre-action guardrail inspection.
type ValidationResult struct {
	Allowed            bool     `json:"allowed"`
	Action             string   `json:"action"`
	Role               string   `json:"role"`
	Enforcement        string   `json:"enforcement"`
	ViolatedDirectives []string `json:"violated_directives,omitempty"`
	Reason             string   `json:"reason,omitempty"`
}

// Manager coordinates guardrail persistence, LLM derivation, validation, and AGENTS.md generation.
type Manager struct {
	store *state.Store
}

// NewManager creates a guardrail manager backed by state.Store.
func NewManager(store *state.Store) *Manager {
	return &Manager{store: store}
}

func (m *Manager) dbFilePath() string {
	return filepath.Join(m.store.Dir(), "guardrails.json")
}

// Load loads guardrails from storage or initializes empty project guardrails.
func (m *Manager) Load() (*ProjectGuardrails, error) {
	data, err := os.ReadFile(m.dbFilePath())
	if err != nil {
		if os.IsNotExist(err) {
			return &ProjectGuardrails{
				Version:     "1.0.0",
				ProjectName: "Project",
				TechStack:   []string{},
				Invariants:  []string{},
				Roles:       make(map[string]RoleGuardrail),
				Cautions:    []CautionRule{},
				CreatedAt:   time.Now(),
				UpdatedAt:   time.Now(),
			}, nil
		}
		return nil, err
	}

	var pg ProjectGuardrails
	if err := json.Unmarshal(data, &pg); err != nil {
		return nil, fmt.Errorf("corrupted guardrails.json: %w", err)
	}
	if pg.Roles == nil {
		pg.Roles = make(map[string]RoleGuardrail)
	}
	return &pg, nil
}

// Save atomically persists guardrails and re-renders AGENTS.md and RULES.md.
func (m *Manager) Save(pg *ProjectGuardrails) error {
	pg.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(pg, "", "  ")
	if err != nil {
		return err
	}

	if err := m.store.AtomicWriteFile("guardrails.json", data); err != nil {
		return err
	}

	// Re-render AGENTS.md and RULES.md
	agentsMD := m.RenderAgentsMD(pg)
	_ = m.store.AtomicWriteFile("AGENTS.md", []byte(agentsMD))

	rulesMD := m.RenderRulesMD(pg)
	_ = m.store.AtomicWriteFile("RULES.md", []byte(rulesMD))

	// Sync to project root AGENTS.md if writable
	projectRootDir := filepath.Dir(m.store.Dir())
	if filepath.Base(projectRootDir) == "gossip" && filepath.Base(filepath.Dir(projectRootDir)) == ".local" {
		projectRootDir = filepath.Dir(filepath.Dir(projectRootDir))
	}
	rootAgentsFile := filepath.Join(projectRootDir, "AGENTS.md")
	_ = os.WriteFile(rootAgentsFile, []byte(agentsMD), 0644)

	return nil
}

// DeriveProjectRoles synthesizes tailored roles, guardrails, cautions, and invariants from project context.
func (m *Manager) DeriveProjectRoles(projectName string, techStack []string, projectGoal string) (*ProjectGuardrails, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	pg, err := m.Load()
	if err != nil {
		return nil, err
	}

	pg.ProjectName = projectName
	pg.TechStack = techStack

	stackLower := strings.ToLower(strings.Join(techStack, " "))

	// Core baseline invariants
	pg.Invariants = []string{
		"Never commit raw API keys, secrets, or credentials in code or messages.",
		"All changes touching public interfaces require unit/integration test coverage.",
		"Concurrent state writes must acquire flock with timeout; no unbounded loops.",
		"Destructive operations (e.g. drop tables, force pushes, file deletions) require Orchestrator signoff.",
	}

	// Baseline caution rules
	pg.Cautions = []CautionRule{
		{
			ID:             "caution-secrets",
			Category:       "security_leak",
			Level:          LevelCritical,
			TriggerPattern: "*(.env*|*secret*|*credential*|*.pem|*id_rsa*)",
			Directive:      "Direct editing or reading of private environment variables and key files is prohibited without sandbox masking.",
			Enforcement:    EnforceBlock,
		},
		{
			ID:             "caution-breaking-schema",
			Category:       "data_loss",
			Level:          LevelCritical,
			TriggerPattern: "*(migrations/*|*schema*|*.sql)",
			Directive:      "Database and schema modifications require dual peer reviews and backward compatibility verification.",
			Enforcement:    EnforceRequirePeerReview,
		},
		{
			ID:             "caution-high-blast-radius",
			Category:       "regression",
			Level:          LevelWarning,
			TriggerPattern: "*core*",
			Directive:      "Modifying core shared utilities requires running blast-radius impact analysis prior to merge.",
			Enforcement:    EnforceRequirePeerReview,
		},
	}

	// Always ensure Lead Orchestrator exists
	pg.Roles["orchestrator"] = RoleGuardrail{
		RoleID:            "orchestrator",
		Title:             "Lead System Orchestrator",
		Description:       "Decomposes goals, sets DAG tasks, advances SOP phase gates, and synthesizes project delivery.",
		SystemPrompt:      fmt.Sprintf("You are the Lead System Orchestrator for %s. Ensure all specialist roles adhere to SOP phase gates and caution rules.", projectName),
		PermittedActions:  []string{"*"},
		ForbiddenActions:  []string{},
		CautionDirectives: []string{"Do not advance phase gates without validating required artifact deliverables."},
	}

	// Derive tech-stack-specific specialist roles
	if strings.Contains(stackLower, "go") || strings.Contains(stackLower, "golang") {
		pg.Roles["go_systems_engineer"] = RoleGuardrail{
			RoleID:            "go_systems_engineer",
			Title:             "Go Systems & Concurrency Engineer",
			Description:       "Specializes in goroutines, channel synchronization, race detection, and atomic OS primitives.",
			SystemPrompt:      "You are a Go Systems Engineer. Always write race-free, idiomatic Go code with proper error wrapping.",
			PermittedActions:  []string{"write_code", "run_tests", "inspect_graph", "post_argument"},
			ForbiddenActions:  []string{"bypass_race_detector", "suppress_flock_errors"},
			CautionDirectives: []string{"Always run 'go test -v -race ./...' on code modifications.", "Use sync.Mutex with defer unlock carefully to prevent deadlocks."},
			SensitivePathPatterns: []string{"pkg/state/lock*"},
		}
	}

	if strings.Contains(stackLower, "network") || strings.Contains(stackLower, "socket") || strings.Contains(stackLower, "mcp") || strings.Contains(stackLower, "protocol") {
		pg.Roles["network_architect"] = RoleGuardrail{
			RoleID:            "network_architect",
			Title:             "Network & Protocol Architect",
			Description:       "Designs framing, socket multiplexing, serialization protocols, and latency budgets.",
			SystemPrompt:      "You are a Network Protocol Architect. Ensure all protocol frames have explicit bounds and length prefixes.",
			PermittedActions:  []string{"design_protocol", "write_framing", "benchmark_latency"},
			ForbiddenActions:  []string{"unbounded_buffer_allocation"},
			CautionDirectives: []string{"Avoid unbounded buffers on untrusted network streams to prevent DoS."},
		}
	}

	if strings.Contains(stackLower, "react") || strings.Contains(stackLower, "node") || strings.Contains(stackLower, "typescript") {
		pg.Roles["frontend_architect"] = RoleGuardrail{
			RoleID:            "frontend_architect",
			Title:             "TypeScript & Frontend Architect",
			Description:       "Designs responsive UI components, state management, and type contracts.",
			SystemPrompt:      "You are a TypeScript Frontend Architect. Enforce strict type safety and zero 'any' casts.",
			PermittedActions:  []string{"author_ui", "type_check", "render_preview"},
			ForbiddenActions:  []string{"suppress_eslint", "use_ts_ignore"},
			CautionDirectives: []string{"Never disable strict typechecking without architect approval."},
		}
	}

	// Add QA Specialist
	pg.Roles["qa_engineer"] = RoleGuardrail{
		RoleID:            "qa_engineer",
		Title:             "Verification & QA Specialist",
		Description:       "Authors deterministic test harnesses, regression suites, and invariant assertions.",
		SystemPrompt:      "You are a QA Verification Specialist. Hunt for boundary overflows, nil dereferences, and flaky tests.",
		PermittedActions:  []string{"submit_review", "run_tests", "log_finding"},
		ForbiddenActions:  []string{"bypass_failing_tests"},
		CautionDirectives: []string{"Verify both positive and negative error cases on all public functions."},
	}

	// Add Security Specialist & Red Team
	pg.Roles["security_specialist"] = RoleGuardrail{
		RoleID:            "security_specialist",
		Title:             "Security Auditor & Red Team Lead",
		Description:       "Performs STRIDE threat modeling, fuzzing, and penetration probing against boundaries.",
		SystemPrompt:      "You are an Adversarial Security Specialist. Challenge architectural assumptions and test attack surfaces.",
		PermittedActions:  []string{"log_red_team_probe", "generate_stride", "audit_report"},
		ForbiddenActions:  []string{"clear_audit_trail"},
		CautionDirectives: []string{"Flag all unsanitized user inputs, path parameters, and shell commands."},
	}

	if err := m.Save(pg); err != nil {
		return nil, err
	}

	return pg, nil
}

// SetCautionRule adds or updates a caution rule.
func (m *Manager) SetCautionRule(rule CautionRule) error {
	unlock, err := m.store.Lock()
	if err != nil {
		return err
	}
	defer unlock()

	pg, err := m.Load()
	if err != nil {
		return err
	}

	if rule.ID == "" {
		rule.ID = fmt.Sprintf("caution-%d", time.Now().UnixNano()/1000000)
	}

	found := false
	for i, c := range pg.Cautions {
		if c.ID == rule.ID {
			pg.Cautions[i] = rule
			found = true
			break
		}
	}
	if !found {
		pg.Cautions = append(pg.Cautions, rule)
	}

	return m.Save(pg)
}

// ValidateProposedAction checks whether a role and proposed action adhere to project guardrails.
func (m *Manager) ValidateProposedAction(role, action, targetPath, payload string) (*ValidationResult, error) {
	pg, err := m.Load()
	if err != nil {
		return nil, err
	}

	roleLower := strings.ToLower(strings.TrimSpace(role))
	actionLower := strings.ToLower(strings.TrimSpace(action))
	targetClean := strings.ToLower(filepath.Clean(targetPath))

	// Check Role Guardrails
	if rg, exists := pg.Roles[roleLower]; exists {
		// Check forbidden actions
		for _, fa := range rg.ForbiddenActions {
			if strings.EqualFold(fa, actionLower) {
				return &ValidationResult{
					Allowed:     false,
					Action:      action,
					Role:        role,
					Enforcement: EnforceBlock,
					Reason:      fmt.Sprintf("Action %q is explicitly forbidden for role %q", action, role),
				}, nil
			}
		}

		// Check sensitive path patterns
		for _, pat := range rg.SensitivePathPatterns {
			matched, _ := filepath.Match(strings.ToLower(pat), targetClean)
			if matched || strings.Contains(targetClean, strings.Trim(pat, "*")) {
				return &ValidationResult{
					Allowed:     false,
					Action:      action,
					Role:        role,
					Enforcement: EnforceRequirePeerReview,
					Reason:      fmt.Sprintf("Target path %q matches sensitive pattern %q assigned to role %q", targetPath, pat, role),
				}, nil
			}
		}
	}

	// Check Global Cautions
	for _, c := range pg.Cautions {
		targetMatch, _ := filepath.Match(strings.ToLower(c.TriggerPattern), targetClean)
		payloadMatch := strings.Contains(strings.ToLower(payload), strings.ToLower(c.TriggerPattern))

		if targetMatch || payloadMatch || (strings.Contains(targetClean, ".env") && strings.Contains(c.TriggerPattern, ".env")) {
			if c.Enforcement == EnforceBlock {
				return &ValidationResult{
					Allowed:            false,
					Action:             action,
					Role:               role,
					Enforcement:        c.Enforcement,
					ViolatedDirectives: []string{c.Directive},
					Reason:             fmt.Sprintf("Blocked by caution rule %s (%s): %s", c.ID, c.Category, c.Directive),
				}, nil
			} else if c.Enforcement == EnforceRequirePeerReview {
				return &ValidationResult{
					Allowed:            true,
					Action:             action,
					Role:               role,
					Enforcement:        c.Enforcement,
					ViolatedDirectives: []string{c.Directive},
					Reason:             fmt.Sprintf("Flagged by caution rule %s (%s): %s", c.ID, c.Category, c.Directive),
				}, nil
			}
		}
	}

	return &ValidationResult{
		Allowed:     true,
		Action:      action,
		Role:        role,
		Enforcement: "allow",
	}, nil
}

// RenderAgentsMD renders a comprehensive AGENTS.md document.
func (m *Manager) RenderAgentsMD(pg *ProjectGuardrails) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# 👥 Project Agent Directives & Guardrails: %s\n\n", pg.ProjectName))
	b.WriteString(fmt.Sprintf("*Synchronized: %s | Active Roles: %d*\n\n",
		pg.UpdatedAt.Format(time.RFC1123), len(pg.Roles)))

	if len(pg.TechStack) > 0 {
		b.WriteString(fmt.Sprintf("**Tech Stack:** `%s`\n\n", strings.Join(pg.TechStack, "`, `")))
	}

	// Core Invariants
	b.WriteString("## 🛡️ Project Invariants & Mandatory Rules\n\n")
	for i, inv := range pg.Invariants {
		b.WriteString(fmt.Sprintf("%d. **%s**\n", i+1, inv))
	}
	b.WriteString("\n---\n\n")

	// Role Directory
	b.WriteString("## 🎭 Active Agent Role Specifications\n\n")
	for _, r := range pg.Roles {
		b.WriteString(fmt.Sprintf("### Role: `%s` — %s\n\n", r.RoleID, r.Title))
		b.WriteString(fmt.Sprintf("**Description:** %s\n\n", r.Description))
		b.WriteString(fmt.Sprintf("> **System Directive:**\n> %s\n\n", r.SystemPrompt))

		if len(r.ForbiddenActions) > 0 {
			b.WriteString(fmt.Sprintf("- 🛑 **Forbidden Actions:** `%s`\n", strings.Join(r.ForbiddenActions, "`, `")))
		}
		if len(r.CautionDirectives) > 0 {
			b.WriteString("- ⚠️ **Caution Directives:**\n")
			for _, cd := range r.CautionDirectives {
				b.WriteString(fmt.Sprintf("  - %s\n", cd))
			}
		}
		if len(r.SensitivePathPatterns) > 0 {
			b.WriteString(fmt.Sprintf("- 🔒 **Protected File Patterns:** `%s`\n", strings.Join(r.SensitivePathPatterns, "`, `")))
		}
		b.WriteString("\n")
	}

	return b.String()
}

// RenderRulesMD renders a dedicated RULES.md caution and constraint document.
func (m *Manager) RenderRulesMD(pg *ProjectGuardrails) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# 📜 Project Caution Rules & Enforcement Policy: %s\n\n", pg.ProjectName))
	b.WriteString(fmt.Sprintf("*Synchronized: %s*\n\n", pg.UpdatedAt.Format(time.RFC1123)))

	b.WriteString("| Rule ID | Category | Severity | Trigger Pattern | Directive | Enforcement |\n")
	b.WriteString("| :--- | :--- | :---: | :--- | :--- | :---: |\n")

	for _, c := range pg.Cautions {
		b.WriteString(fmt.Sprintf("| `%s` | `%s` | **%s** | `%s` | %s | `%s` |\n",
			c.ID, c.Category, strings.ToUpper(c.Level), c.TriggerPattern, c.Directive, c.Enforcement))
	}
	b.WriteString("\n")

	return b.String()
}

// GetGuardrails returns the current ProjectGuardrails configuration.
func (m *Manager) GetGuardrails() (*ProjectGuardrails, error) {
	return m.Load()
}

// ExportAgentsMD loads the guardrails database and exports formatted AGENTS.md.
func (m *Manager) ExportAgentsMD() (string, error) {
	pg, err := m.Load()
	if err != nil {
		return "", err
	}
	return m.RenderAgentsMD(pg), nil
}
