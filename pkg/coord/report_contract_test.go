package coord

import (
	"strings"
	"testing"
)

func validDiagramReport() string {
	return strings.Join([]string{
		"# Architecture review",
		"",
		"## Review summary",
		"The reviewed architecture enforces project-scoped access before processing a worker review request.",
		"",
		"## Scope and evidence",
		"This report traces the coordination service at the recorded baseline and compares the flow with the project access criteria.",
		"",
		"## UML class diagram",
		"The classes and relationship below show which service objects enforce this boundary.",
		"",
		"```mermaid",
		"classDiagram",
		"  Coordinator --> Worker",
		"```",
		"",
		"## Sequence diagram",
		"This sequence records the request path from the coordinator through authorization to the worker.",
		"",
		"```mermaid",
		"sequenceDiagram",
		"  Coordinator->>Worker: request review",
		"```",
		"",
		"## Findings",
		"The coordinator delegates only after checking project access; the request preserves the selected review contract.",
		"",
		"## Coverage and limitations",
		"Project access and review handoff are covered; runtime behavior was not traced and should be checked separately.",
	}, "\n")
}

func TestDiagramReportRequiresNarrativeAndPlannedCoverage(t *testing.T) {
	contract := ReportContract{ReviewType: "diagram_report", Coverage: []string{"project access", "review handoff"}}
	if err := validateReportBody(contract, validDiagramReport()); err != nil {
		t.Fatalf("complete report rejected: %v", err)
	}
	withoutNarrative := "# Review\n\n```mermaid\nclassDiagram\n  Coordinator --> Worker\n```\n```mermaid\nsequenceDiagram\n  Coordinator->>Worker: request review\n```"
	if err := validateReportBody(ReportContract{ReviewType: "diagram_report"}, withoutNarrative); err == nil || !strings.Contains(err.Error(), "populated Markdown") {
		t.Fatalf("diagram-only report accepted: %v", err)
	}
	withoutCoverage := strings.Replace(validDiagramReport(), "review handoff", "review flow", 1)
	if err := validateReportBody(contract, withoutCoverage); err == nil || !strings.Contains(err.Error(), "review handoff") {
		t.Fatalf("report omitting planned coverage accepted: %v", err)
	}
	if err := validateReportBody(ReportContract{ReviewType: "full_code", Coverage: contract.Coverage}, withoutNarrative); err != nil {
		t.Fatalf("diagram-specific requirements applied to full-code report: %v", err)
	}
	wrongSections := strings.Replace(validDiagramReport(), "## UML class diagram", "## Class diagram", 1)
	if err := validateReportBody(contract, wrongSections); err == nil || !strings.Contains(err.Error(), "diagram sections") {
		t.Fatalf("diagrams outside the required report sections were accepted: %v", err)
	}
	emptyFindings := strings.Replace(validDiagramReport(), "The coordinator delegates only after checking project access; the request preserves the selected review contract.", "", 1)
	if err := validateReportBody(contract, emptyFindings); err == nil || !strings.Contains(err.Error(), "Findings") {
		t.Fatalf("empty findings section was accepted: %v", err)
	}
}
