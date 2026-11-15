package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/tasmirz/gossip/skills"
)

type installOptions struct {
	home       string
	cwd        string
	xdgConfig  string
	codexHome  string
	geminiCfg  string
	appData    string
	binPath    string
	binDir     string
	profile    string
	clients    []string
	all        bool
	skillOnly  bool
	dryRun     bool
	lookPath   func(string) (string, error)
	runCmd     func(string, ...string) error
	out        io.Writer
	errOut     io.Writer
}

func runInstallCommand(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	all := fs.Bool("all", true, "Auto-configure all detected coding agents and editors")
	client := fs.String("client", "", "Comma-separated clients: codex,claude,antigravity,opencode,gemini,cursor,claude-desktop,vscode")
	profile := fs.String("profile", "default", "Local Gossip identity profile passed to coord-mcp")
	binDir := fs.String("bin-dir", "", "Custom PATH destination directory for the gossip binary")
	binPath := fs.String("bin-path", "", "Override binary path written into agent MCP configs")
	skillOnly := fs.Bool("skill-only", false, "Install SKILL.md files only without modifying MCP configs")
	dryRun := fs.Bool("dry-run", false, "Preview planned skill and MCP changes without writing files")
	if err := fs.Parse(args); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("detect user home directory: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	selfExe, err := os.Executable()
	if err != nil {
		selfExe = "gossip"
	} else if resolved, rerr := filepath.EvalSymlinks(selfExe); rerr == nil && !strings.HasSuffix(resolved, ".test") {
		selfExe = resolved
	}
	var explicit []string
	if strings.TrimSpace(*client) != "" {
		for _, item := range strings.Split(*client, ",") {
			if norm := normalizeInstallClient(item); norm != "" {
				explicit = append(explicit, norm)
			} else if strings.TrimSpace(item) != "" {
				return fmt.Errorf("unknown client %q (valid: codex, claude, antigravity, opencode, gemini, cursor, claude-desktop, vscode)", strings.TrimSpace(item))
			}
		}
	}
	if strings.TrimSpace(*profile) == "" {
		return errors.New("--profile must not be empty")
	}
	opts := installOptions{
		home:      home,
		cwd:       cwd,
		xdgConfig: firstNonEmpty(os.Getenv("XDG_CONFIG_HOME"), filepath.Join(home, ".config")),
		codexHome: firstNonEmpty(os.Getenv("CODEX_HOME"), filepath.Join(home, ".codex")),
		geminiCfg: firstNonEmpty(os.Getenv("GEMINI_CONFIG_DIR"), filepath.Join(home, ".gemini", "config")),
		appData:   os.Getenv("APPDATA"),
		binPath:   firstNonEmpty(strings.TrimSpace(*binPath), selfExe),
		binDir:    strings.TrimSpace(*binDir),
		profile:   strings.TrimSpace(*profile),
		clients:   explicit,
		all:       *all && len(explicit) == 0,
		skillOnly: *skillOnly,
		dryRun:    *dryRun,
		lookPath:  exec.LookPath,
		runCmd: func(name string, cmdArgs ...string) error {
			cmd := exec.Command(name, cmdArgs...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				msg := strings.TrimSpace(string(out))
				if msg != "" {
					return fmt.Errorf("%s: %s", err, msg)
				}
				return err
			}
			return nil
		},
		out:    os.Stdout,
		errOut: os.Stderr,
	}
	return executeInstall(opts)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func normalizeInstallClient(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "codex", "codex-cli":
		return "codex"
	case "claude", "claude-code":
		return "claude"
	case "claude-desktop":
		return "claude-desktop"
	case "antigravity", "agy":
		return "antigravity"
	case "opencode":
		return "opencode"
	case "gemini", "gemini-cli":
		return "gemini"
	case "cursor":
		return "cursor"
	case "vscode", "vs-code":
		return "vscode"
	default:
		return ""
	}
}

func defaultInstallBinDir(home string) string {
	if runtime.GOOS == "windows" {
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			return filepath.Join(local, "Programs", "gossip")
		}
		return filepath.Join(home, "AppData", "Local", "Programs", "gossip")
	}
	return filepath.Join(home, ".local", "bin")
}

func ensureBinaryInBinDir(opts installOptions) (string, error) {
	destDir := opts.binDir
	if destDir == "" {
		destDir = defaultInstallBinDir(opts.home)
	}
	base := "gossip"
	if runtime.GOOS == "windows" {
		base = "gossip.exe"
	}
	destBin := filepath.Join(destDir, base)
	src := opts.binPath
	// Do not copy go test binaries or non-existent synthetic test overrides.
	if src == "" || src == "gossip" || strings.HasSuffix(src, ".test") {
		return src, nil
	}
	srcInfo, err := os.Stat(src)
	if err != nil {
		return src, nil
	}
	if destInfo, err := os.Stat(destBin); err == nil && os.SameFile(srcInfo, destInfo) {
		return destBin, nil
	}
	if opts.dryRun {
		fmt.Fprintf(opts.out, "  [DRY-RUN] Would install binary at %s\n", destBin)
		return destBin, nil
	}
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return src, nil
	}
	tmp, err := os.CreateTemp(destDir, ".gossip-bin-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Chmod(0755); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpPath, destBin); err != nil {
		return "", err
	}
	cleanup = false
	fmt.Fprintf(opts.out, "  [OK] Installed binary: %s\n", destBin)
	return destBin, nil
}

func executeInstall(opts installOptions) error {
	fmt.Fprintf(opts.out, "[INSTALL] Gossip coding-agent setup (profile=%q, os=%s/%s)\n", opts.profile, runtime.GOOS, runtime.GOARCH)
	binForMCP, err := ensureBinaryInBinDir(opts)
	if err != nil {
		return fmt.Errorf("install binary: %w", err)
	}
	if binForMCP == "" {
		binForMCP = "gossip"
	}

	order := []string{"codex", "claude", "antigravity", "opencode", "gemini", "cursor", "claude-desktop", "vscode"}
	targets := opts.clients
	autoDetect := len(targets) == 0 && opts.all
	if autoDetect {
		targets = order
	}

	configured := 0
	for _, client := range targets {
		detected := !autoDetect || isClientDetected(opts, client)
		if !detected {
			continue
		}
		if err := installForClient(opts, client, binForMCP, !autoDetect); err != nil {
			return err
		}
		configured++
	}

	if configured == 0 {
		fmt.Fprintln(opts.out, "  [INFO] No supported coding agents were auto-detected.")
		fmt.Fprintln(opts.out, "  Target one explicitly with: gossip install --client claude,codex,antigravity,opencode,gemini,cursor,vscode")
	} else {
		fmt.Fprintln(opts.out, "[DONE] Restart your configured coding agent(s), then run `gossip coord-ui` (http://127.0.0.1:47281) to connect a project.")
	}
	return nil
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func isClientDetected(opts installOptions, client string) bool {
	hasCLI := func(name string) bool {
		if opts.lookPath == nil {
			return false
		}
		_, err := opts.lookPath(name)
		return err == nil
	}
	switch client {
	case "codex":
		return hasCLI("codex") || dirExists(opts.codexHome)
	case "claude":
		return hasCLI("claude") || dirExists(filepath.Join(opts.home, ".claude"))
	case "antigravity":
		return dirExists(opts.geminiCfg) || fileExists(filepath.Join(opts.geminiCfg, "mcp_config.json"))
	case "opencode":
		return hasCLI("opencode") || dirExists(filepath.Join(opts.xdgConfig, "opencode"))
	case "gemini":
		return hasCLI("gemini") || dirExists(filepath.Join(opts.home, ".gemini"))
	case "cursor":
		return dirExists(filepath.Join(opts.home, ".cursor"))
	case "claude-desktop":
		return fileExists(claudeDesktopConfigPath(opts))
	case "vscode":
		return dirExists(filepath.Join(opts.cwd, ".vscode"))
	default:
		return false
	}
}

func claudeDesktopConfigPath(opts installOptions) string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(opts.home, "Library", "Application Support", "Claude", "claude_desktop_config.json")
	case "windows":
		base := opts.appData
		if base == "" {
			base = filepath.Join(opts.home, "AppData", "Roaming")
		}
		return filepath.Join(base, "Claude", "claude_desktop_config.json")
	default:
		return filepath.Join(opts.xdgConfig, "Claude", "claude_desktop_config.json")
	}
}

func skillPathForClient(opts installOptions, client string) string {
	switch client {
	case "codex":
		return filepath.Join(opts.home, ".agents", "skills", "gossip", "SKILL.md")
	case "claude":
		return filepath.Join(opts.home, ".claude", "skills", "gossip", "SKILL.md")
	case "antigravity":
		return filepath.Join(opts.geminiCfg, "skills", "gossip", "SKILL.md")
	case "opencode":
		return filepath.Join(opts.xdgConfig, "opencode", "skills", "gossip", "SKILL.md")
	case "gemini":
		return filepath.Join(opts.home, ".gemini", "skills", "gossip", "SKILL.md")
	case "cursor":
		return filepath.Join(opts.home, ".cursor", "skills", "gossip", "SKILL.md")
	default:
		return ""
	}
}

func installSkillFile(opts installOptions, dest string) error {
	if dest == "" {
		return nil
	}
	if opts.dryRun {
		fmt.Fprintf(opts.out, "    - [DRY-RUN] Would write skill to %s\n", dest)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(dest, skills.GossipSkillMD, 0644); err != nil {
		return err
	}
	fmt.Fprintf(opts.out, "    - Skill installed: %s\n", dest)
	return nil
}

func installForClient(opts installOptions, client, binForMCP string, strictCLI bool) error {
	fmt.Fprintf(opts.out, "  * %s\n", client)
	if err := installSkillFile(opts, skillPathForClient(opts, client)); err != nil {
		return fmt.Errorf("%s skill: %w", client, err)
	}
	if opts.skillOnly {
		return nil
	}
	args := []string{"coord-mcp", "--profile", opts.profile}
	switch client {
	case "codex":
		if _, err := opts.lookPath("codex"); err != nil {
			if strictCLI {
				return errors.New("codex CLI not found on PATH; install Codex CLI or run with --skill-only")
			}
			fmt.Fprintln(opts.out, "    - MCP skipped (`codex` CLI not on PATH)")
			return nil
		}
		cfg := filepath.Join(opts.codexHome, "config.toml")
		if raw, err := os.ReadFile(cfg); err == nil && strings.Contains(string(raw), "[mcp_servers.gossip]") {
			if err := backupConfigFile(opts, cfg); err != nil {
				return err
			}
		}
		cmdArgs := append([]string{"mcp", "add", "gossip", "--", binForMCP}, args...)
		if opts.dryRun {
			fmt.Fprintf(opts.out, "    - [DRY-RUN] Would run: codex %s\n", strings.Join(cmdArgs, " "))
			return nil
		}
		if err := opts.runCmd("codex", cmdArgs...); err != nil {
			return fmt.Errorf("codex mcp add: %w", err)
		}
		fmt.Fprintln(opts.out, "    - MCP registered via `codex mcp add`")
	case "claude":
		if _, err := opts.lookPath("claude"); err != nil {
			if strictCLI {
				return errors.New("claude CLI not found on PATH; install Claude Code or run with --skill-only")
			}
			fmt.Fprintln(opts.out, "    - MCP skipped (`claude` CLI not on PATH)")
			return nil
		}
		cmdArgs := append([]string{"mcp", "add", "--scope", "user", "gossip", "--", binForMCP}, args...)
		if opts.dryRun {
			fmt.Fprintf(opts.out, "    - [DRY-RUN] Would run: claude %s\n", strings.Join(cmdArgs, " "))
			return nil
		}
		if err := opts.runCmd("claude", cmdArgs...); err != nil {
			return fmt.Errorf("claude mcp add: %w", err)
		}
		fmt.Fprintln(opts.out, "    - MCP registered via `claude mcp add --scope user`")
	case "gemini":
		if _, err := opts.lookPath("gemini"); err != nil {
			if strictCLI {
				return errors.New("gemini CLI not found on PATH; install Gemini CLI or run with --skill-only")
			}
			fmt.Fprintln(opts.out, "    - MCP skipped (`gemini` CLI not on PATH)")
			return nil
		}
		cmdArgs := append([]string{"mcp", "add", "--scope", "user", "gossip", binForMCP}, args...)
		if opts.dryRun {
			fmt.Fprintf(opts.out, "    - [DRY-RUN] Would run: gemini %s\n", strings.Join(cmdArgs, " "))
			return nil
		}
		if err := opts.runCmd("gemini", cmdArgs...); err != nil {
			return fmt.Errorf("gemini mcp add: %w", err)
		}
		fmt.Fprintln(opts.out, "    - MCP registered via `gemini mcp add --scope user`")
	case "antigravity":
		return upsertStandardJSONServer(opts, filepath.Join(opts.geminiCfg, "mcp_config.json"), "mcpServers", binForMCP, args)
	case "cursor":
		return upsertStandardJSONServer(opts, filepath.Join(opts.home, ".cursor", "mcp.json"), "mcpServers", binForMCP, args)
	case "claude-desktop":
		return upsertStandardJSONServer(opts, claudeDesktopConfigPath(opts), "mcpServers", binForMCP, args)
	case "vscode":
		return upsertStandardJSONServer(opts, filepath.Join(opts.cwd, ".vscode", "mcp.json"), "servers", binForMCP, args)
	case "opencode":
		return upsertOpenCodeServer(opts, filepath.Join(opts.xdgConfig, "opencode", "opencode.json"), binForMCP, args)
	}
	return nil
}

func backupConfigFile(opts installOptions, path string) error {
	if opts.dryRun || !fileExists(path) {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	backup := fmt.Sprintf("%s.backup-%s", path, time.Now().Format("20060102-150405"))
	if err := os.WriteFile(backup, raw, 0600); err != nil {
		return err
	}
	fmt.Fprintf(opts.out, "    - Backed up previous config: %s\n", backup)
	return nil
}

func readJSONObjectFile(path string, defaultObj map[string]any, stripComments bool) (map[string]any, os.FileMode, bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		out := map[string]any{}
		for k, v := range defaultObj {
			out[k] = v
		}
		return out, 0600, false, nil
	}
	if err != nil {
		return nil, 0600, false, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, 0600, true, err
	}
	text := string(raw)
	if stripComments {
		text = stripJSONCCommentsAndCommas(text)
	}
	if strings.TrimSpace(text) == "" {
		text = "{}"
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		return nil, info.Mode().Perm(), true, fmt.Errorf("parse %s: %w", path, err)
	}
	if doc == nil {
		return nil, info.Mode().Perm(), true, fmt.Errorf("%s must contain a JSON object", path)
	}
	return doc, info.Mode().Perm(), true, nil
}

func writeJSONAtomic(path string, doc map[string]any, mode os.FileMode) error {
	if mode == 0 {
		mode = 0600
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	tmp, err := os.CreateTemp(dir, ".gossip-cfg-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func upsertStandardJSONServer(opts installOptions, path, containerKey, command string, args []string) error {
	if opts.dryRun {
		fmt.Fprintf(opts.out, "    - [DRY-RUN] Would register %s.gossip in %s\n", containerKey, path)
		return nil
	}
	doc, mode, existed, err := readJSONObjectFile(path, nil, false)
	if err != nil {
		return err
	}
	var servers map[string]any
	if existing, ok := doc[containerKey]; ok {
		typed, ok := existing.(map[string]any)
		if !ok {
			return fmt.Errorf("%s %s must be a JSON object", path, containerKey)
		}
		servers = typed
	} else {
		servers = map[string]any{}
	}
	if _, replacing := servers["gossip"]; replacing && existed {
		if err := backupConfigFile(opts, path); err != nil {
			return err
		}
	}
	servers["gossip"] = map[string]any{
		"command": command,
		"args":    args,
	}
	doc[containerKey] = servers
	if err := writeJSONAtomic(path, doc, mode); err != nil {
		return err
	}
	fmt.Fprintf(opts.out, "    - MCP configured: %s\n", path)
	return nil
}

func upsertOpenCodeServer(opts installOptions, path, command string, args []string) error {
	if opts.dryRun {
		fmt.Fprintf(opts.out, "    - [DRY-RUN] Would register mcp.servers.gossip in %s\n", path)
		return nil
	}
	defaults := map[string]any{"$schema": "https://opencode.ai/config.json"}
	doc, mode, existed, err := readJSONObjectFile(path, defaults, true)
	if err != nil {
		return err
	}
	if _, ok := doc["$schema"]; !ok {
		doc["$schema"] = "https://opencode.ai/config.json"
	}
	mcpObj := map[string]any{}
	if rawMCP, ok := doc["mcp"]; ok {
		typed, ok := rawMCP.(map[string]any)
		if !ok {
			return fmt.Errorf("%s mcp must be a JSON object", path)
		}
		for k, v := range typed {
			mcpObj[k] = v
		}
	}
	servers := map[string]any{}
	if rawServers, ok := mcpObj["servers"]; ok {
		typed, ok := rawServers.(map[string]any)
		if !ok {
			return fmt.Errorf("%s mcp.servers must be a JSON object", path)
		}
		for k, v := range typed {
			servers[k] = v
		}
	}
	_, replacing := servers["gossip"]
	_, legacyGossip := mcpObj["gossip"]
	if (replacing || legacyGossip) && existed {
		if err := backupConfigFile(opts, path); err != nil {
			return err
		}
	}
	servers["gossip"] = map[string]any{
		"type":    "local",
		"command": append([]string{command}, args...),
	}
	mcpObj["servers"] = servers
	doc["mcp"] = mcpObj
	if err := writeJSONAtomic(path, doc, mode); err != nil {
		return err
	}
	fmt.Fprintf(opts.out, "    - MCP configured: %s\n", path)
	return nil
}

// stripJSONCCommentsAndCommas strips // line comments, /* block comments */,
// and trailing commas outside string literals so OpenCode JSONC files parse safely.
func stripJSONCCommentsAndCommas(src string) string {
	var clean bytes.Buffer
	inStr, esc := false, false
	for i := 0; i < len(src); i++ {
		ch := src[i]
		nxt := byte(0)
		if i+1 < len(src) {
			nxt = src[i+1]
		}
		if inStr {
			clean.WriteByte(ch)
			if esc {
				esc = false
			} else if ch == '\\' {
				esc = true
			} else if ch == '"' {
				inStr = false
			}
			continue
		}
		if ch == '"' {
			inStr = true
			clean.WriteByte(ch)
			continue
		}
		if ch == '/' && nxt == '/' {
			for i < len(src) && src[i] != '\n' && src[i] != '\r' {
				i++
			}
			if i < len(src) {
				clean.WriteByte(src[i])
			}
			continue
		}
		if ch == '/' && nxt == '*' {
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				if src[i] == '\n' || src[i] == '\r' {
					clean.WriteByte(src[i])
				}
				i++
			}
			i++
			continue
		}
		clean.WriteByte(ch)
	}
	noComments := clean.String()
	var out bytes.Buffer
	inStr, esc = false, false
	for i := 0; i < len(noComments); i++ {
		ch := noComments[i]
		if inStr {
			out.WriteByte(ch)
			if esc {
				esc = false
			} else if ch == '\\' {
				esc = true
			} else if ch == '"' {
				inStr = false
			}
			continue
		}
		if ch == '"' {
			inStr = true
			out.WriteByte(ch)
			continue
		}
		if ch == ',' {
			j := i + 1
			for j < len(noComments) && (noComments[j] == ' ' || noComments[j] == '\t' || noComments[j] == '\n' || noComments[j] == '\r') {
				j++
			}
			if j >= len(noComments) || noComments[j] == '}' || noComments[j] == ']' {
				continue
			}
		}
		out.WriteByte(ch)
	}
	return out.String()
}
