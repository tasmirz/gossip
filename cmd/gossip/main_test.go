package main

import (
	"os"
	"testing"
)

func TestCLICommands(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-cli-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Test init
	runInit(tmpDir)

	// Test status
	runStatus(tmpDir)

	// Test board
	runBoard(tmpDir)

	// Test tasks
	runTasks(tmpDir)

	// Test debates
	runDebates(tmpDir)

	// Test probes
	runProbes(tmpDir)

	// Test report
	runReport(tmpDir)

	// Test PageRank
	runPageRank(tmpDir)

	// Test clusters
	runClusters(tmpDir)

	// Test repo-map
	runRepoMap(tmpDir)

	// Test red-team audit report
	runAudit(tmpDir)

	// Test SOP workflows
	runSOP(tmpDir)

	// Test Derive
	runDerive([]string{"--dir", tmpDir, "--name", "TestApp", "--stack", "Go,gRPC", "--goal", "Build fast API"})

	// Test Guardrails
	runGuardrails(tmpDir)

	// Test Check
	runCheck([]string{"--dir", tmpDir, "--role", "worker", "--action", "write_file", "--path", "pkg/api.go"})
	runCheck([]string{"--dir", tmpDir, "--role", "worker", "--action", "write_file", "--path", ".env"})

	// Test Agents MD
	runAgents(tmpDir)
}
