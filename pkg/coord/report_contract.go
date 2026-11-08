package coord

import (
	"errors"
	"regexp"
	"strings"
)

var (
	mermaidClassDecl       = regexp.MustCompile(`^class\s+([[:alnum:]_.]+)(?:\s|$)`)
	mermaidClassEdge       = regexp.MustCompile(`^([[:alnum:]_.]+)\s*(<\|--|<\.\.|--\|>|-->|<--|\*--|--\*|o--|--o|\.\.>|--|\.\.)\s*([[:alnum:]_.]+)(?:\s*:.*)?$`)
	mermaidSequenceMessage = regexp.MustCompile(`^([[:alnum:]_.]+)\s*(-->>|->>|-->|->|--x|->x|--\)|-\))\s*([[:alnum:]_.]+)\s*:\s*\S.*$`)
	mermaidParticipant     = regexp.MustCompile(`^(?:participant|actor)\s+([[:alnum:]_.]+)(?:\s+as\s+.+)?$`)
)

func validateReportBody(contract ReportContract, body string) error {
	if contract.ReviewType != "diagram_report" {
		return nil
	}
	for _, title := range []string{"Review summary", "Scope and evidence", "Findings", "Coverage and limitations"} {
		section, ok := markdownSection(body, title)
		if !ok || !substantiveNarrative(markdownProse(section)) {
			return errors.New("diagram_report requires a populated Markdown '" + title + "' section")
		}
	}
	classSection, hasClassSection := markdownSection(body, "UML class diagram")
	sequenceSection, hasSequenceSection := markdownSection(body, "Sequence diagram")
	if !hasClassSection || !hasMermaidDiagram(classSection, "classDiagram") || !hasSequenceSection || !hasMermaidDiagram(sequenceSection, "sequenceDiagram") {
		return errors.New("diagram_report requires populated UML class diagram and sequence diagram sections")
	}
	prose := markdownProse(body)
	if prose == "" {
		return errors.New("diagram_report requires explanatory Markdown outside diagram and code blocks")
	}
	for _, coverage := range contract.Coverage {
		coverage = strings.TrimSpace(coverage)
		if coverage == "" {
			continue
		}
		if !strings.Contains(strings.ToLower(prose), strings.ToLower(coverage)) {
			return errors.New("diagram_report must explicitly address every planned coverage item; missing: " + coverage)
		}
	}
	return nil
}

func substantiveNarrative(value string) bool {
	return len(strings.TrimSpace(value)) >= 24 && len(strings.Fields(value)) >= 4
}

func markdownSection(markdown, title string) (string, bool) {
	lines := strings.Split(markdown, "\n")
	var content []string
	found := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if heading, ok := markdownHeading(trimmed); ok {
			if found {
				break
			}
			if strings.EqualFold(heading, title) {
				found = true
			}
			continue
		}
		if found {
			content = append(content, line)
		}
	}
	return strings.TrimSpace(strings.Join(content, "\n")), found
}

func markdownHeading(line string) (string, bool) {
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level == len(line) || line[level] != ' ' {
		return "", false
	}
	return strings.TrimSpace(line[level:]), true
}

func markdownProse(markdown string) string {
	var prose []string
	inFence := false
	for _, line := range strings.Split(markdown, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			prose = append(prose, line)
		}
	}
	return strings.TrimSpace(strings.Join(prose, "\n"))
}

func hasMermaidDiagram(markdown, diagram string) bool {
	for _, tail := range strings.Split(markdown, "```mermaid") {
		if tail == markdown {
			continue
		}
		end := strings.Index(tail, "```")
		if end < 0 {
			continue
		}
		lines := strings.Split(strings.TrimSpace(tail[:end]), "\n")
		if len(lines) < 2 || strings.TrimSpace(strings.TrimSuffix(lines[0], "\r")) != diagram {
			continue
		}
		if diagramHasStructure(diagram, lines[1:]) {
			return true
		}
	}
	return false
}

func diagramHasStructure(kind string, lines []string) bool {
	switch kind {
	case "classDiagram":
		classes := map[string]bool{}
		relationships := 0
		for _, raw := range lines {
			line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
			if line == "" || strings.HasPrefix(line, "%%") {
				continue
			}
			if match := mermaidClassDecl.FindStringSubmatch(line); match != nil {
				classes[match[1]] = true
				continue
			}
			if match := mermaidClassEdge.FindStringSubmatch(line); match != nil {
				classes[match[1]] = true
				classes[match[3]] = true
				if match[1] != match[3] {
					relationships++
				}
			}
		}
		return len(classes) > 1 && relationships > 0
	case "sequenceDiagram":
		participants := map[string]bool{}
		messages := 0
		for _, raw := range lines {
			line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
			if line == "" || strings.HasPrefix(line, "%%") {
				continue
			}
			if match := mermaidParticipant.FindStringSubmatch(line); match != nil {
				participants[match[1]] = true
				continue
			}
			if match := mermaidSequenceMessage.FindStringSubmatch(line); match != nil {
				participants[match[1]] = true
				participants[match[3]] = true
				messages++
			}
		}
		return messages > 0 && len(participants) > 1
	}
	return false
}
