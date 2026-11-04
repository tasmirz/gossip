package roles_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tasmirz/gossip/pkg/roles"
)

func TestJSONRoleServiceLoading(t *testing.T) {
	srv := roles.GetDefaultService()

	// Verify all 8 specialist roles were parsed from JSON
	tests := []string{
		"orchestrator",
		"worker",
		"network_engineer",
		"network-engineer",
		"math_expert",
		"math-expert",
		"systems_architect",
		"security_specialist",
		"qa_engineer",
		"researcher",
	}

	for _, id := range tests {
		r, ok := srv.GetRole(id)
		if !ok {
			t.Fatalf("Role %s not loaded from JSON", id)
		}
		if r.Title == "" || r.SystemPrompt == "" {
			t.Fatalf("Role %s has empty title or system prompt", id)
		}
	}
}

func TestDynamicRoleRegistrationFromJSON(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-roles-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create custom roles.json
	customJSON := `{
		"version": "1.0.0",
		"roles": [
			{
				"id": "quantum_physicist",
				"title": "Quantum Algorithm Engineer",
				"category": "specialist",
				"description": "Designs quantum circuits and Hamiltonian simulations.",
				"system_prompt": "You are a Quantum Computing Specialist.",
				"directives": ["Verify decoherence times."],
				"recommended_tools": ["gossip_rag_add_node"],
				"capability_tags": ["quantum", "qasm", "circuits"]
			}
		]
	}`

	customFile := filepath.Join(tmpDir, "custom_roles.json")
	if err := os.WriteFile(customFile, []byte(customJSON), 0644); err != nil {
		t.Fatal(err)
	}

	srv, err := roles.NewService(tmpDir, customFile)
	if err != nil {
		t.Fatalf("Failed to create role service: %v", err)
	}

	// Verify custom role from JSON is loaded
	qr, ok := srv.GetRole("quantum_physicist")
	if !ok {
		t.Fatalf("Expected quantum_physicist role to be loaded from custom JSON")
	}
	if qr.Title != "Quantum Algorithm Engineer" {
		t.Fatalf("Title mismatch: %s", qr.Title)
	}

	// Verify dynamic registration at runtime (Open/Closed Principle)
	newRole := roles.RoleDefinition{
		ID:           "database_tuner",
		Title:        "PostgreSQL Performance Specialist",
		Category:     "engineering",
		Description:  "Analyzes EXPLAIN ANALYZE and buffer cache hits.",
		SystemPrompt: "You are a Database Performance Specialist.",
		Directives:   []string{"Never allow sequential scans on tables > 100k rows."},
	}

	if err := srv.RegisterRole(newRole); err != nil {
		t.Fatalf("RegisterRole failed: %v", err)
	}

	dbr, ok := srv.GetRole("database_tuner")
	if !ok || dbr.Title != "PostgreSQL Performance Specialist" {
		t.Fatalf("Dynamic role registration failed")
	}

	// Test saving to JSON
	savePath := filepath.Join(tmpDir, "exported_roles.json")
	if err := srv.SaveToFile(savePath); err != nil {
		t.Fatalf("SaveToFile failed: %v", err)
	}

	data, err := os.ReadFile(savePath)
	if err != nil || !strings.Contains(string(data), "database_tuner") {
		t.Fatalf("Exported JSON missing dynamic role: %s", string(data))
	}
}

func TestFormatRoleSummary(t *testing.T) {
	summary := roles.FormatRoleSummary()
	if !strings.Contains(summary, "JSON-Driven") {
		t.Errorf("Summary header mismatch")
	}
	if !strings.Contains(summary, "network_engineer") || !strings.Contains(summary, "math_expert") {
		t.Errorf("Summary missing key roles")
	}
}
