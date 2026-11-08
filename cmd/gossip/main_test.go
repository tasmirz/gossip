package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPClientConfigsExposeCreateOnlyBootstrapByPrivateKeyPath(t *testing.T) {
	type server struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	type config struct {
		MCPServers map[string]server `json:"mcpServers"`
		Servers    map[string]server `json:"servers"`
	}
	for _, name := range []string{"mcp.json", "mcp_config.json", "claude_desktop_config.json", filepath.Join(".cursor", "mcp.json"), filepath.Join(".vscode", "mcp.json")} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", name))
			if err != nil {
				t.Fatal(err)
			}
			var cfg config
			if err = json.Unmarshal(data, &cfg); err != nil {
				t.Fatal(err)
			}
			servers := cfg.MCPServers
			if len(servers) == 0 {
				servers = cfg.Servers
			}
			bootstrap, ok := servers["gossip-project-bootstrap"]
			if !ok || len(bootstrap.Args) == 0 || bootstrap.Args[0] != "coord-mcp-bootstrap" {
				t.Fatal("configuration is missing the create-only project bootstrap server")
			}
			keyFlag, keyPath := false, ""
			credentialDir := false
			for i, arg := range bootstrap.Args {
				if arg == "--bootstrap-api-key-file" && i+1 < len(bootstrap.Args) {
					keyFlag, keyPath = true, bootstrap.Args[i+1]
				}
				if arg == "--credential-dir" && i+1 < len(bootstrap.Args) {
					credentialDir = bootstrap.Args[i+1] != ""
				}
			}
			if !keyFlag || keyPath == "" || !credentialDir {
				t.Fatal("bootstrap config must reference a private API key file and credential output directory")
			}
		})
	}
}

func TestLegacyMCPEntryPointsAreRetiredRegardlessOfOptIn(t *testing.T) {
	if os.Getenv("GOSSIP_TEST_RUN_RETIRED_MCP") == "1" {
		switch os.Getenv("GOSSIP_TEST_RETIRED_MCP_ENTRY") {
		case "none":
			os.Args = []string{"gossip"}
		case "mcp":
			os.Args = []string{"gossip", "mcp"}
		case "serve":
			os.Args = []string{"gossip", "serve"}
		case "flags":
			os.Args = []string{"gossip", "--dir", "/tmp/legacy-mcp-must-not-start"}
		case "export":
			os.Args = []string{"gossip", "export-mcp", "--out", "/tmp/legacy-mcp-schema-must-not-be-created"}
		case "export-alias":
			os.Args = []string{"gossip", "mcp-export", "--out", "/tmp/legacy-mcp-schema-must-not-be-created"}
		default:
			t.Fatal("invalid child entry point")
		}
		main()
		return
	}
	t.Setenv("GOSSIP_TEST_RUN_RETIRED_MCP", "1")
	t.Setenv("GOSSIP_ENABLE_LEGACY_MCP", "1")
	for _, entry := range []string{"none", "mcp", "serve", "flags", "export", "export-alias"} {
		t.Run(entry, func(t *testing.T) {
			t.Setenv("GOSSIP_TEST_RETIRED_MCP_ENTRY", entry)
			cmd := exec.Command(os.Args[0], "-test.run=^TestLegacyMCPEntryPointsAreRetiredRegardlessOfOptIn$")
			cmd.Env = os.Environ()
			output, err := cmd.CombinedOutput()
			exitError, ok := err.(*exec.ExitError)
			if !ok || exitError.ExitCode() != 2 || !strings.Contains(string(output), "legacy JSON MCP interface is retired") || !strings.Contains(string(output), "coord-mcp") {
				t.Fatalf("legacy MCP entry point was not rejected with guidance: err=%v output=%s", err, output)
			}
		})
	}
}

func TestLegacyHTTPFRPCommandsAreRejected(t *testing.T) {
	if err := legacyUIExposureError("frps.example"); err == nil || !strings.Contains(err.Error(), "coord-serve") {
		t.Fatalf("legacy UI FRP exposure was not rejected with secure guidance: %v", err)
	}
	if err := legacyUIExposureError(""); err != nil {
		t.Fatalf("loopback UI was rejected: %v", err)
	}
	for _, command := range []string{"init", "start", "activate", "config"} {
		err := legacyTunnelCommandError([]string{command})
		if err == nil || !strings.Contains(err.Error(), "coord-serve") {
			t.Fatalf("legacy tunnel command %q was not rejected with secure guidance: %v", command, err)
		}
	}
	for _, command := range []string{"status", "stop", "deactivate"} {
		if err := legacyTunnelCommandError([]string{command}); err != nil {
			t.Fatalf("safe local tunnel command %q was rejected: %v", command, err)
		}
	}
}

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

	// Test Poll
	runPoll([]string{"--dir", tmpDir, "--agent", "worker-1", "--role", "worker", "--since", "5m", "--timeout", "0"})

	// Test Index
	runIndex([]string{"--dir", tmpDir, "--root", tmpDir})

	// Test Personal Todo
	runPersonalTodo([]string{"add", "--dir", tmpDir, "--agent", "worker-1", "--title", "Fix buffer", "--priority", "high"})
	runPersonalTodo([]string{"--dir", tmpDir, "--agent", "worker-1"})

	// Test System Todos
	runSystemTodos(tmpDir)

	// Test Orchestrator Assignments
	runAssignments(tmpDir)
}
