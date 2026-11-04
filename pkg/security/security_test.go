package security_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tasmirz/gossip/pkg/security"
)

func TestSecuritySanitizeID(t *testing.T) {
	validIDs := []string{
		"agent-1",
		"task_42",
		"concept:crc32",
		"decision:adr-001",
		"user@domain.com",
		"v1.0.0",
	}

	for _, id := range validIDs {
		sanitized, err := security.SanitizeID(id, 64)
		if err != nil {
			t.Errorf("expected %q to be valid, got error: %v", id, err)
		}
		if sanitized != id {
			t.Errorf("expected %q, got %q", id, sanitized)
		}
	}

	invalidIDs := []string{
		"",
		"   ",
		"../../etc/passwd",
		"foo/bar",
		"foo\\bar",
		"cmd; rm -rf /",
		"cat `whoami`",
		"param\x00inject",
		"id_with_$dollar",
	}

	for _, id := range invalidIDs {
		_, err := security.SanitizeID(id, 64)
		if err == nil {
			t.Errorf("expected %q to fail validation, but it succeeded", id)
		}
	}
}

func TestSecurityValidateSafePath(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-security-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Safe paths inside tmpDir
	safePath, err := security.ValidateSafePath(tmpDir, "subfolder/artifact.md")
	if err != nil {
		t.Fatalf("expected safe path, got error: %v", err)
	}
	expected := filepath.Join(tmpDir, "subfolder", "artifact.md")
	if safePath != expected {
		t.Fatalf("expected %q, got %q", expected, safePath)
	}

	// Path traversal attacks
	malicious := []string{
		"../../etc/passwd",
		"../sibling_dir",
		"/etc/shadow",
		"sub/../../../../tmp/secret",
		"file\x00.md",
	}

	for _, mal := range malicious {
		_, err := security.ValidateSafePath(tmpDir, mal)
		if err == nil {
			t.Errorf("expected path traversal attack %q to be rejected, but it was accepted", mal)
		}
	}
}

func TestSecurityValidatePayloadSize(t *testing.T) {
	small := "hello world"
	if err := security.ValidatePayloadSize(small, 100); err != nil {
		t.Errorf("expected small payload to pass: %v", err)
	}

	large := string(make([]byte, 1000))
	if err := security.ValidatePayloadSize(large, 500); err == nil {
		t.Errorf("expected large payload to fail size check")
	}
}

func TestSecurityAuthorizeAction(t *testing.T) {
	// Orchestrator can set goals
	if err := security.AuthorizeAction("orchestrator", "gossip_set_goal"); err != nil {
		t.Errorf("expected orchestrator to be allowed: %v", err)
	}

	// Worker cannot set goals
	if err := security.AuthorizeAction("worker", "gossip_set_goal"); err == nil {
		t.Errorf("expected worker to be rejected for gossip_set_goal")
	}

	// General action allowed
	if err := security.AuthorizeAction("worker", "gossip_send_message"); err != nil {
		t.Errorf("expected general action to be allowed: %v", err)
	}
}
