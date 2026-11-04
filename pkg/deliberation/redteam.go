package deliberation

import (
	"fmt"
	"strings"
	"time"
)

// STRIDE threat modeling categories
const (
	ThreatSpoofing        = "SPOOFING"
	ThreatTampering       = "TAMPERING"
	ThreatRepudiation     = "REPUDIATION"
	ThreatInfoDisclosure  = "INFO_DISCLOSURE"
	ThreatDenialOfService = "DENIAL_OF_SERVICE"
	ThreatElevationOfPriv = "ELEVATION_OF_PRIVILEGE"
)

// GenerateSTRIDEProbes generates structured red-team attack scenarios against a target component across all 6 STRIDE dimensions.
func GenerateSTRIDEProbes(targetComponent, contextDescription, attackerID string) []RedTeamProbe {
	if attackerID == "" {
		attackerID = "agent:security_red_team"
	}
	now := time.Now()

	scenarios := []struct {
		Category string
		Vector   string
		Payload  string
	}{
		{
			Category: ThreatSpoofing,
			Vector:   fmt.Sprintf("Attacker assumes unauthorized Agent ID to execute privileged RPCs on %s without mutual authentication.", targetComponent),
			Payload:  `{"spoofed_agent_id": "agent:root_orchestrator", "bypass_header": "X-Internal-Token: null"}`,
		},
		{
			Category: ThreatTampering,
			Vector:   fmt.Sprintf("Concurrent state mutation injecting race conditions or corrupting disk storage for %s during atomic file commits.", targetComponent),
			Payload:  `{"concurrent_writers": 20, "payload": "CORRUPT_JSON_NON_ATOMIC_APPEND", "target_file": "state.json"}`,
		},
		{
			Category: ThreatRepudiation,
			Vector:   fmt.Sprintf("Deletion of action logs, un-receipted message dispatch, or tampering with task history on %s without audit records.", targetComponent),
			Payload:  `{"action": "drop_audit_trail", "silence_receipts": true, "tamper_target": "messages.jsonl"}`,
		},
		{
			Category: ThreatInfoDisclosure,
			Vector:   fmt.Sprintf("Eliciting raw stack traces, API credentials, or private workspace memory from %s via malformed error generation.", targetComponent),
			Payload:  `{"input": "../../.env\x00", "cause_panic": true, "extract_env_vars": ["API_KEY", "SECRET_KEY"]}`,
		},
		{
			Category: ThreatDenialOfService,
			Vector:   fmt.Sprintf("Resource exhaustion attack via cyclic DAG tasks, infinite graph recursion, or recursive mutex acquisition on %s.", targetComponent),
			Payload:  `{"cyclic_dependencies": ["task-1->task-2->task-1"], "memory_bomb_bytes": 1073741824}`,
		},
		{
			Category: ThreatElevationOfPriv,
			Vector:   fmt.Sprintf("Restricted worker agent attempting to unilaterally reassign project goals or dynamically register unauthorized root roles on %s.", targetComponent),
			Payload:  `{"agent_role": "worker", "attempted_action": "gossip_set_goal", "override_role": "superadmin"}`,
		},
	}

	probes := make([]RedTeamProbe, 0, len(scenarios))
	for i, sc := range scenarios {
		probes = append(probes, RedTeamProbe{
			ID:             fmt.Sprintf("probe-stride-%s-%d", strings.ToLower(sc.Category), now.Unix()+int64(i)),
			Target:         targetComponent,
			ThreatCategory: sc.Category,
			AttackerID:     attackerID,
			AttackVector:   sc.Vector,
			PoCPayload:     sc.Payload,
			Status:         ProbeStatusVulnerable, // logged as open vulnerability until verified/mitigated
			CreatedAt:      now,
			UpdatedAt:      now,
		})
	}

	return probes
}

// LogBatchProbes adds a slice of probes to the store atomically.
func (m *Manager) LogBatchProbes(probes []RedTeamProbe) error {
	unlock, err := m.store.Lock()
	if err != nil {
		return err
	}
	defer unlock()

	ds, err := m.Load()
	if err != nil {
		return err
	}

	for _, p := range probes {
		ds.Probes[p.ID] = p
	}

	if err := m.Save(ds); err != nil {
		return err
	}

	// Auto-regenerate AUDIT_REPORT.md
	report := m.buildAuditReport(ds)
	_ = m.store.AtomicWriteFile("AUDIT_REPORT.md", []byte(report))

	return nil
}

// GenerateAuditReport creates and writes an executive AUDIT_REPORT.md summarizing all red-team evaluations.
func (m *Manager) GenerateAuditReport() (string, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return "", err
	}
	defer unlock()

	ds, err := m.Load()
	if err != nil {
		return "", err
	}

	report := m.buildAuditReport(ds)
	if err := m.store.AtomicWriteFile("AUDIT_REPORT.md", []byte(report)); err != nil {
		return "", err
	}

	return report, nil
}

func (m *Manager) buildAuditReport(ds *DeliberationStore) string {
	var b strings.Builder
	b.WriteString("# [RED-TEAM] Security Assessment & STRIDE Audit\n\n")
	b.WriteString(fmt.Sprintf("*Generated at: %s | Total Probes Logged: %d*\n\n",
		time.Now().Format(time.RFC1123), len(ds.Probes)))

	// Metrics breakdown
	vulnerableCount := 0
	mitigatedCount := 0
	falsePositiveCount := 0
	strideCounts := make(map[string]int)

	for _, p := range ds.Probes {
		switch p.Status {
		case ProbeStatusVulnerable:
			vulnerableCount++
		case ProbeStatusMitigated:
			mitigatedCount++
		case ProbeStatusFalsePositive:
			falsePositiveCount++
		}
		cat := strings.ToUpper(p.ThreatCategory)
		if cat != "" {
			strideCounts[cat]++
		}
	}

	statusClass := "[ACTION REQUIRED]"
	if vulnerableCount == 0 && len(ds.Probes) > 0 {
		statusClass = "[SECURE] ALL PROBES MITIGATED"
	} else if vulnerableCount > 3 {
		statusClass = "[CRITICAL RISK] UNMITIGATED VULNERABILITIES"
	}

	b.WriteString(fmt.Sprintf("## Overall Security Status: **%s**\n\n", statusClass))
	b.WriteString("| Metric | Value |\n")
	b.WriteString("| :--- | :--- |\n")
	b.WriteString(fmt.Sprintf("| Total Probed Scenarios | %d |\n", len(ds.Probes)))
	b.WriteString(fmt.Sprintf("| Open Vulnerabilities | **%d** |\n", vulnerableCount))
	b.WriteString(fmt.Sprintf("| Successfully Mitigated | %d |\n", mitigatedCount))
	b.WriteString(fmt.Sprintf("| False Positives Marked | %d |\n\n", falsePositiveCount))

	// STRIDE Threat Matrix
	b.WriteString("### [STRIDE] Coverage Matrix\n\n")
	b.WriteString("| STRIDE Category | Probes Tested |\n")
	b.WriteString("| :--- | :--- |\n")
	strideList := []string{
		ThreatSpoofing,
		ThreatTampering,
		ThreatRepudiation,
		ThreatInfoDisclosure,
		ThreatDenialOfService,
		ThreatElevationOfPriv,
	}
	for _, cat := range strideList {
		count := strideCounts[cat]
		b.WriteString(fmt.Sprintf("| `%s` | %d |\n", cat, count))
	}
	b.WriteString("\n")

	// Vulnerable items table
	b.WriteString("### [VULNERABILITIES] Active Unmitigated Vulnerabilities\n\n")
	if vulnerableCount == 0 {
		b.WriteString("_No active vulnerabilities logged._\n\n")
	} else {
		b.WriteString("| Probe ID | Target | Threat Category | Attack Vector | Remediation Task |\n")
		b.WriteString("| :--- | :--- | :--- | :--- | :--- |\n")
		for _, p := range ds.Probes {
			if p.Status == ProbeStatusVulnerable {
				remTask := p.RemediationTaskID
				if remTask == "" {
					remTask = "_Not assigned_"
				} else {
					remTask = fmt.Sprintf("`%s`", remTask)
				}
				b.WriteString(fmt.Sprintf("| `%s` | `%s` | `%s` | %s | %s |\n",
					p.ID, p.Target, p.ThreatCategory, p.AttackVector, remTask))
			}
		}
		b.WriteString("\n")
	}

	// Mitigated items table
	if mitigatedCount > 0 {
		b.WriteString("### [MITIGATED] Verified Defenses\n\n")
		b.WriteString("| Probe ID | Target | Category | Remediation Task |\n")
		b.WriteString("| :--- | :--- | :--- | :--- |\n")
		for _, p := range ds.Probes {
			if p.Status == ProbeStatusMitigated {
				b.WriteString(fmt.Sprintf("| `%s` | `%s` | `%s` | `%s` |\n",
					p.ID, p.Target, p.ThreatCategory, p.RemediationTaskID))
			}
		}
		b.WriteString("\n")
	}

	return b.String()
}
