package security

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Standard payload size limits to prevent memory exhaustion and DoS attacks.
const (
	MaxMessageSizeBytes  = 512 * 1024       // 512 KB
	MaxArtifactSizeBytes = 4 * 1024 * 1024  // 4 MB
	MaxGraphSummaryBytes = 64 * 1024        // 64 KB
	MaxStringParamBytes  = 256 * 1024       // 256 KB
	MaxIDLength          = 128
)

// Allowed identifier pattern: alphanumeric, dash, underscore, colon, dot, @
var validIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_\-\.:#@]+$`)

// Dangerous shell characters that should never appear in identifiers or file names
var dangerousCharPattern = regexp.MustCompile(`[;&|<>$` + "`" + `\\]|\x00`)

// SanitizeID validates and sanitizes entity IDs (tasks, nodes, agents, workflows, etc.).
func SanitizeID(id string, maxLen int) (string, error) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return "", fmt.Errorf("identifier cannot be empty")
	}
	if maxLen <= 0 {
		maxLen = MaxIDLength
	}
	if len(trimmed) > maxLen {
		return "", fmt.Errorf("identifier exceeds maximum length of %d characters", maxLen)
	}

	if dangerousCharPattern.MatchString(trimmed) {
		return "", fmt.Errorf("identifier contains prohibited control or shell characters")
	}

	if strings.Contains(trimmed, "..") || strings.Contains(trimmed, "/") || strings.Contains(trimmed, "\\") {
		return "", fmt.Errorf("identifier contains path traversal sequences")
	}

	if !validIDPattern.MatchString(trimmed) {
		return "", fmt.Errorf("identifier %q contains invalid characters (allowed: [a-zA-Z0-9_\\-\\.:#@])", trimmed)
	}

	return trimmed, nil
}

// ValidateSafePath verifies that a target path strictly resides within the base sandbox directory.
// Defends against Path Traversal (CWE-22), symlink escapes (CVE-2025-53109), and null byte injections.
func ValidateSafePath(baseDir, userInput string) (string, error) {
	if strings.Contains(userInput, "\x00") {
		return "", fmt.Errorf("path contains null byte injection")
	}

	// Canonicalize base directory
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return "", fmt.Errorf("failed to resolve base directory: %w", err)
	}
	if evalBase, err := filepath.EvalSymlinks(absBase); err == nil {
		absBase = evalBase
	}
	absBase = filepath.Clean(absBase)

	// Combine or resolve user input
	cleanedInput := filepath.Clean(strings.TrimSpace(userInput))
	var targetPath string
	if filepath.IsAbs(cleanedInput) {
		targetPath = cleanedInput
	} else {
		targetPath = filepath.Join(absBase, cleanedInput)
	}
	targetPath = filepath.Clean(targetPath)

	// Evaluate symlinks if target or its existing ancestor exists
	evalTarget := targetPath
	cur := targetPath
	for {
		if _, err := os.Lstat(cur); err == nil {
			if resolved, err := filepath.EvalSymlinks(cur); err == nil {
				suffix, _ := filepath.Rel(cur, targetPath)
				if suffix == "." {
					evalTarget = resolved
				} else {
					evalTarget = filepath.Join(resolved, suffix)
				}
			}
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur || parent == "." {
			break
		}
		cur = parent
	}
	evalTarget = filepath.Clean(evalTarget)

	// Strict prefix boundary check
	rel, err := filepath.Rel(absBase, evalTarget)
	if err != nil {
		return "", fmt.Errorf("failed to evaluate relative path: %w", err)
	}

	if strings.HasPrefix(rel, "..") || rel == ".." {
		return "", fmt.Errorf("path traversal violation: %q resolves outside sandbox root %q", userInput, absBase)
	}

	return evalTarget, nil
}

// ValidatePayloadSize ensures incoming content does not exceed resource bounds (DoS protection).
func ValidatePayloadSize(content string, maxBytes int) error {
	if maxBytes <= 0 {
		maxBytes = MaxArtifactSizeBytes
	}
	if len(content) > maxBytes {
		return fmt.Errorf("payload size (%d bytes) exceeds allowed limit of %d bytes", len(content), maxBytes)
	}
	return nil
}

// AuthorizeAction checks role permission gates for privileged operations.
func AuthorizeAction(callerRole, actionName string) error {
	callerRole = strings.ToLower(strings.TrimSpace(callerRole))

	switch actionName {
	case "gossip_set_goal", "gossip_register_custom_role":
		if callerRole != "orchestrator" && callerRole != "admin" && callerRole != "" {
			return fmt.Errorf("action %q requires role 'orchestrator' (caller role is %q)", actionName, callerRole)
		}
	case "gossip_conclude_debate":
		if callerRole != "orchestrator" && callerRole != "critic" && callerRole != "" {
			return fmt.Errorf("concluding a debate requires role 'orchestrator' or 'critic' (caller role is %q)", callerRole)
		}
	}
	return nil
}
