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
}
