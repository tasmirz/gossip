package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecuteInstallMergesMCPConfigsPreservingUnrelatedServers(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	out := &bytes.Buffer{}

	// Seed Antigravity config with an existing unrelated server and a prior gossip entry.
	agyDir := filepath.Join(home, ".gemini", "config")
	if err := os.MkdirAll(agyDir, 0700); err != nil {
		t.Fatal(err)
	}
	agyCfgPath := filepath.Join(agyDir, "mcp_config.json")
	if err := os.WriteFile(agyCfgPath, []byte(`{"mcpServers":{"other":{"command":"other-mcp"},"gossip":{"command":"old"}}}`), 0600); err != nil {
		t.Fatal(err)
	}

	// Seed OpenCode JSONC config with comments, trailing commas, and another MCP entry.
	ocDir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(ocDir, 0700); err != nil {
		t.Fatal(err)
	}
	ocCfgPath := filepath.Join(ocDir, "opencode.json")
	ocSeed := `{
  // existing opencode config
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "servers": {
      "existing": { "type": "local", "command": ["echo", "hi"], },
    },
  },
}`
	if err := os.WriteFile(ocCfgPath, []byte(ocSeed), 0600); err != nil {
		t.Fatal(err)
	}

	opts := installOptions{
		home:      home,
		cwd:       cwd,
		xdgConfig: filepath.Join(home, ".config"),
		codexHome: filepath.Join(home, ".codex"),
		geminiCfg: agyDir,
		binPath:   "gossip",
		profile:   "reviewer",
		clients:   []string{"antigravity", "opencode", "cursor", "vscode"},
		lookPath:  func(string) (string, error) { return "", os.ErrNotExist },
		out:       out,
		errOut:    out,
	}
	if err := executeInstall(opts); err != nil {
		t.Fatalf("executeInstall: %v\n%s", err, out.String())
	}

	// Verify skill files exist for skill-capable editors.
	for _, skillPath := range []string{
		filepath.Join(agyDir, "skills", "gossip", "SKILL.md"),
		filepath.Join(ocDir, "skills", "gossip", "SKILL.md"),
		filepath.Join(home, ".cursor", "skills", "gossip", "SKILL.md"),
	} {
		data, err := os.ReadFile(skillPath)
		if err != nil || !strings.Contains(string(data), "name: gossip") {
			t.Fatalf("missing valid SKILL.md at %s: err=%v", skillPath, err)
		}
	}

	// Verify Antigravity preserved "other", updated "gossip", and created backup.
	var agyDoc struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	agyRaw, err := os.ReadFile(agyCfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(agyRaw, &agyDoc); err != nil {
		t.Fatal(err)
	}
	if agyDoc.MCPServers["other"].Command != "other-mcp" {
		t.Fatalf("lost unrelated MCP server: %+v", agyDoc)
	}
	if g := agyDoc.MCPServers["gossip"]; g.Command != "gossip" || len(g.Args) != 3 || g.Args[0] != "coord-mcp" || g.Args[2] != "reviewer" {
		t.Fatalf("unexpected antigravity gossip entry: %+v", g)
	}
	backups, err := filepath.Glob(agyCfgPath + ".backup-*")
	if err != nil || len(backups) == 0 {
		t.Fatalf("expected backup for overwritten gossip entry, got %v err=%v", backups, err)
	}
	if info, err := os.Stat(agyCfgPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("expected 0600 permissions on %s, got %v err=%v", agyCfgPath, info.Mode().Perm(), err)
	}

	// Verify OpenCode preserved "existing" and configured local coord-mcp command array.
	var ocDoc struct {
		MCP struct {
			Servers map[string]struct {
				Type    string   `json:"type"`
				Command []string `json:"command"`
			} `json:"servers"`
		} `json:"mcp"`
	}
	ocRaw, err := os.ReadFile(ocCfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(ocRaw, &ocDoc); err != nil {
		t.Fatal(err)
	}
	if _, ok := ocDoc.MCP.Servers["existing"]; !ok {
		t.Fatalf("lost unrelated opencode server: %+v", ocDoc)
	}
	if g := ocDoc.MCP.Servers["gossip"]; g.Type != "local" || len(g.Command) != 4 || g.Command[1] != "coord-mcp" || g.Command[3] != "reviewer" {
		t.Fatalf("unexpected opencode gossip entry: %+v", g)
	}

	// Verify VS Code workspace config uses "servers.gossip".
	var vsDoc struct {
		Servers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"servers"`
	}
	vsRaw, err := os.ReadFile(filepath.Join(cwd, ".vscode", "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(vsRaw, &vsDoc); err != nil {
		t.Fatal(err)
	}
	if g := vsDoc.Servers["gossip"]; g.Command != "gossip" || len(g.Args) != 3 || g.Args[0] != "coord-mcp" {
		t.Fatalf("unexpected vscode gossip entry: %+v", g)
	}
}

func TestExecuteInstallAutoDetectsOnlyPresentCLIsAndRespectsDryRun(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	var ran [][]string
	out := &bytes.Buffer{}

	opts := installOptions{
		home:      home,
		cwd:       cwd,
		xdgConfig: filepath.Join(home, ".config"),
		codexHome: filepath.Join(home, ".codex"),
		geminiCfg: filepath.Join(home, ".gemini", "config"),
		binPath:   "gossip",
		profile:   "default",
		all:       true,
		dryRun:    true,
		lookPath: func(name string) (string, error) {
			if name == "claude" {
				return "/usr/local/bin/claude", nil
			}
			return "", os.ErrNotExist
		},
		runCmd: func(name string, args ...string) error {
			ran = append(ran, append([]string{name}, args...))
			return nil
		},
		out:    out,
		errOut: out,
	}
	if err := executeInstall(opts); err != nil {
		t.Fatalf("executeInstall dryRun: %v", err)
	}
	if len(ran) != 0 {
		t.Fatalf("dryRun executed external commands: %v", ran)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "gossip", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("dryRun wrote SKILL.md on disk")
	}

	opts.dryRun = false
	if err := executeInstall(opts); err != nil {
		t.Fatalf("executeInstall auto-detect: %v", err)
	}
	if len(ran) != 1 || ran[0][0] != "claude" || ran[0][1] != "mcp" || ran[0][2] != "add" {
		t.Fatalf("expected claude mcp add invocation only, got %v", ran)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "gossip", "SKILL.md")); err != nil {
		t.Fatalf("expected claude SKILL.md written: %v", err)
	}
}
