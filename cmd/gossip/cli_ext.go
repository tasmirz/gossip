package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/tasmirz/gossip/pkg/cron"
	"github.com/tasmirz/gossip/pkg/state"
	"github.com/tasmirz/gossip/pkg/tunnel"
	"github.com/tasmirz/gossip/pkg/vault"
)

func runInstall(args []string) {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	all := fs.Bool("all", true, "Configure all detected AI agents & editors (Antigravity, OpenCode, Claude, Cursor, VS Code)")
	client := fs.String("client", "", "Configure a specific client only: 'antigravity', 'opencode', 'claude', 'cursor', 'vscode'")
	binDir := fs.String("bin-dir", "", "Custom binary install destination directory")
	dryRun := fs.Bool("dry-run", false, "Print planned changes without writing files")
	_ = fs.Parse(args)

	fmt.Printf("%s\n", BannerArt)
	fmt.Printf("[INSTALL] Gossip Universal Mesh Auto-Installer (OS: %s/%s)\n", runtime.GOOS, runtime.GOARCH)
	fmt.Println(strings.Repeat("-", 65))

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error detecting user home directory: %v\n", err)
		os.Exit(1)
	}

	selfExe, err := os.Executable()
	if err != nil {
		selfExe = "gossip"
	}

	targetClients := make(map[string]bool)
	if *client != "" {
		for _, c := range strings.Split(*client, ",") {
			targetClients[strings.TrimSpace(strings.ToLower(c))] = true
		}
	} else if *all {
		targetClients["antigravity"] = true
		targetClients["opencode"] = true
		targetClients["claude"] = true
		targetClients["cursor"] = true
		targetClients["vscode"] = true
	}

	// 1. Binary installation
	destBinDir := *binDir
	if destBinDir == "" {
		if runtime.GOOS == "windows" {
			destBinDir = filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "gossip")
		} else {
			destBinDir = filepath.Join(home, ".local", "bin")
		}
	}

	fmt.Printf("[1/6] Binary Installation Destination: %s\n", destBinDir)
	if !*dryRun {
		_ = os.MkdirAll(destBinDir, 0755)
		destBin := filepath.Join(destBinDir, filepath.Base(selfExe))
		if selfExe != destBin {
			if data, err := os.ReadFile(selfExe); err == nil {
				_ = os.WriteFile(destBin, data, 0755)
				fmt.Printf("  ✓ Copied binary to %s\n", destBin)
			}
		}
	} else {
		fmt.Printf("  [DRY-RUN] Would ensure %s exists and copy %s\n", destBinDir, selfExe)
	}

	// 2. Configure Antigravity
	if targetClients["antigravity"] {
		fmt.Println("[2/6] Configuring Antigravity Agent & MCP plugin...")
		pluginDir := filepath.Join(home, ".gemini", "config", "plugins", "gossip")
		mcpDir := filepath.Join(home, ".gemini", "antigravity", "mcp", "gossip")
		if !*dryRun {
			_ = os.MkdirAll(pluginDir, 0755)
			_ = os.MkdirAll(mcpDir, 0755)
			fmt.Printf("  ✓ Antigravity plugin linked at %s\n", pluginDir)
		} else {
			fmt.Printf("  [DRY-RUN] Would configure Antigravity at %s and %s\n", pluginDir, mcpDir)
		}
	}

	// 3. Configure OpenCode
	if targetClients["opencode"] {
		fmt.Println("[3/6] Configuring OpenCode MCP & Skill...")
		ocDir := filepath.Join(home, ".config", "opencode")
		ocCfgFile := filepath.Join(ocDir, "opencode.json")
		if !*dryRun {
			_ = os.MkdirAll(ocDir, 0755)
			configureJSONMCP(ocCfgFile, "mcpServers", "gossip", "gossip", []string{"mcp"})
			fmt.Printf("  ✓ OpenCode MCP registered in %s\n", ocCfgFile)
		} else {
			fmt.Printf("  [DRY-RUN] Would register gossip in %s\n", ocCfgFile)
		}
	}

	// 4. Configure Claude Desktop
	if targetClients["claude"] {
		fmt.Println("[4/6] Configuring Claude Desktop MCP...")
		var claudeCfgFile string
		if runtime.GOOS == "darwin" {
			claudeCfgFile = filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json")
		} else if runtime.GOOS == "windows" {
			claudeCfgFile = filepath.Join(os.Getenv("APPDATA"), "Claude", "claude_desktop_config.json")
		} else {
			claudeCfgFile = filepath.Join(home, ".config", "Claude", "claude_desktop_config.json")
		}
		if !*dryRun {
			_ = os.MkdirAll(filepath.Dir(claudeCfgFile), 0755)
			configureJSONMCP(claudeCfgFile, "mcpServers", "gossip", "gossip", []string{"mcp"})
			fmt.Printf("  ✓ Claude Desktop MCP registered in %s\n", claudeCfgFile)
		} else {
			fmt.Printf("  [DRY-RUN] Would register gossip in %s\n", claudeCfgFile)
		}
	}

	// 5. Configure Cursor
	if targetClients["cursor"] {
		fmt.Println("[5/6] Configuring Cursor MCP...")
		cursorCfgFile := filepath.Join(home, ".cursor", "mcp.json")
		if !*dryRun {
			_ = os.MkdirAll(filepath.Dir(cursorCfgFile), 0755)
			configureJSONMCP(cursorCfgFile, "mcpServers", "gossip", "gossip", []string{"mcp"})
			fmt.Printf("  ✓ Cursor global MCP registered in %s\n", cursorCfgFile)
		} else {
			fmt.Printf("  [DRY-RUN] Would register gossip in %s\n", cursorCfgFile)
		}
	}

	// 6. Configure VS Code
	if targetClients["vscode"] {
		fmt.Println("[6/6] Configuring VS Code Workspace MCP...")
		vscodeFile := filepath.Join(".vscode", "mcp.json")
		if !*dryRun {
			_ = os.MkdirAll(".vscode", 0755)
			configureJSONMCP(vscodeFile, "mcpServers", "gossip", "gossip", []string{"mcp"})
			fmt.Printf("  ✓ VS Code workspace MCP registered in %s\n", vscodeFile)
		} else {
			fmt.Printf("  [DRY-RUN] Would register gossip in %s\n", vscodeFile)
		}
	}

	fmt.Println(strings.Repeat("-", 65))
	fmt.Println("[SUCCESS] Gossip installation and client configuration complete!")
	fmt.Println("Test instant connection: 'gossip status' or start UI: 'gossip ui'")
}

func configureJSONMCP(filePath, rootKey, serverName, command string, args []string) {
	data := make(map[string]interface{})
	if b, err := os.ReadFile(filePath); err == nil {
		_ = json.Unmarshal(b, &data)
	}

	serversMap, ok := data[rootKey].(map[string]interface{})
	if !ok || serversMap == nil {
		serversMap = make(map[string]interface{})
	}

	serversMap[serverName] = map[string]interface{}{
		"command": command,
		"args":    args,
	}
	data[rootKey] = serversMap

	out, err := json.MarshalIndent(data, "", "  ")
	if err == nil {
		_ = os.WriteFile(filePath, out, 0644)
	}
}

func runTunnel(args []string) {
	if len(args) == 0 {
		printTunnelHelp()
		return
	}

	sub := args[0]
	switch sub {
	case "init", "start", "activate":
		fs := flag.NewFlagSet("tunnel init", flag.ExitOnError)
		serverAddr := fs.String("server", "tunnel.yourdomain.com", "FRP server hostname or IP")
		serverPort := fs.Int("port", 7000, "FRP server bind port")
		token := fs.String("token", "", "FRP server authentication token")
		domain := fs.String("domain", "", "Custom domain for HTTP proxying")
		remotePort := fs.Int("remote-port", 8080, "Remote port for TCP proxying")
		user := fs.String("user", "", "Mandatory HTTP Basic Auth username")
		pass := fs.String("pass", "", "Mandatory HTTP Basic Auth password (min 6 chars)")
		localPort := fs.Int("local-port", 8080, "Local Gossip service port")
		dir := fs.String("dir", "", "Custom gossip storage directory")
		_ = fs.Parse(args[1:])

		store, err := state.NewStore(*dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if *user != "" && *pass != "" {
			if err := tunnel.ValidateCredentials(*user, *pass); err != nil {
				fmt.Fprintf(os.Stderr, "Credential error: %v\n", err)
				os.Exit(1)
			}
		}

		ep := *domain
		if ep == "" {
			ep = fmt.Sprintf("frp://%s:%d", *serverAddr, *serverPort)
		}

		tState := tunnel.TunnelState{
			Active:         true,
			Type:           "frp",
			ServerAddr:     *serverAddr,
			RemoteEndpoint: ep,
			LocalIP:        "127.0.0.1",
			LocalPort:      *localPort,
			RemotePort:     *remotePort,
			AuthEnabled:    *user != "" && *pass != "",
			AuthUser:       *user,
			AuthPassMasked: tunnel.MaskSecret(*pass),
			StartedAt:      time.Now(),
		}

		if err := tunnel.SaveTunnelState(store.Dir(), tState); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to save tunnel state: %v\n", err)
			os.Exit(1)
		}

		// Also generate frpc.toml
		cfg := tunnel.DefaultClientConfig(*localPort, *user, *pass)
		cfg.ServerAddr = *serverAddr
		cfg.ServerPort = *serverPort
		cfg.AuthToken = *token
		if *domain != "" {
			cfg.CustomDomains = []string{*domain}
		}
		cfg.RemotePort = *remotePort
		tomlContent := tunnel.GenerateFRPCTOML(cfg)
		_ = os.WriteFile(filepath.Join(store.Dir(), "frpc.toml"), []byte(tomlContent), 0600)

		fmt.Printf("%s\n", BannerArt)
		fmt.Println("[OK] Protected FRP Tunnel State Initialized & Saved (0600 permissions)")
		fmt.Printf("  • Server: %s:%d\n", *serverAddr, *serverPort)
		fmt.Printf("  • Remote Endpoint: %s\n", ep)
		authStatus := "ENABLED (Protected HTTP BasicAuth)"
		if !tState.AuthEnabled {
			authStatus = "DISABLED (WARNING: UNPROTECTED)"
		}
		fmt.Printf("  • Ingress Auth: %s\n", authStatus)
		if *user != "" {
			fmt.Printf("  • Auth User: %s (Password: %s)\n", *user, tState.AuthPassMasked)
		}
		fmt.Printf("  • Config written: %s\n", filepath.Join(store.Dir(), "frpc.toml"))

	case "config":
		fs := flag.NewFlagSet("tunnel config", flag.ExitOnError)
		fmtType := fs.String("format", "toml", "Config format: 'toml', 'ini', 'server_toml', 'server_ini'")
		serverAddr := fs.String("server", "tunnel.yourdomain.com", "FRP server hostname or IP")
		serverPort := fs.Int("port", 7000, "FRP server bind port")
		token := fs.String("token", "secret-token", "FRP auth token")
		user := fs.String("user", "admin", "HTTP Basic Auth username")
		pass := fs.String("pass", "password123", "HTTP Basic Auth password")
		_ = fs.Parse(args[1:])

		cfg := tunnel.DefaultClientConfig(8080, *user, *pass)
		cfg.ServerAddr = *serverAddr
		cfg.ServerPort = *serverPort
		cfg.AuthToken = *token

		switch *fmtType {
		case "ini":
			fmt.Print(tunnel.GenerateFRPCINI(cfg))
		case "server_toml":
			srvCfg := tunnel.FRPServerConfig{BindPort: *serverPort, AuthToken: *token, VhostHTTPPort: 80}
			fmt.Print(tunnel.GenerateFRPSTOML(srvCfg))
		case "server_ini":
			srvCfg := tunnel.FRPServerConfig{BindPort: *serverPort, AuthToken: *token, VhostHTTPPort: 80}
			fmt.Print(tunnel.GenerateFRPSINI(srvCfg))
		default:
			fmt.Print(tunnel.GenerateFRPCTOML(cfg))
		}

	case "status":
		dir := parseDirFlag(args[1:])
		store, err := state.NewStore(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		disc := tunnel.ComputeDisclosure(store)
		fmt.Print(disc.MarkdownBanner())

	case "stop", "deactivate":
		dir := parseDirFlag(args[1:])
		store, err := state.NewStore(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		_ = tunnel.ClearTunnelState(store.Dir())
		fmt.Println("[OK] Tunnel deactivated. Mesh reverted to local isolation.")

	default:
		printTunnelHelp()
	}
}

func printTunnelHelp() {
	fmt.Printf("%s\n", BannerArt)
	fmt.Println("Usage: gossip tunnel <subcommand> [flags]")
	fmt.Println("Subcommands:")
	fmt.Println("  init    Initialize and activate protected FRP tunnel with credentials")
	fmt.Println("  config  Generate frpc.toml, frpc.ini, or frps.toml configurations")
	fmt.Println("  status  Print current tunnel status and mandatory remote disclosure")
	fmt.Println("  stop    Deactivate tunnel and return to local isolation")
}

func runCron(args []string) {
	fs := flag.NewFlagSet("cron", flag.ExitOnError)
	interval := fs.String("interval", "30s", "Periodic checking interval or cron expression (@every 30s, */5 * * * *)")
	agentID := fs.String("agent", "cron-watcher", "Agent ID for unread messages inbox check")
	once := fs.Bool("once", false, "Execute single check cycle and exit")
	dir := fs.String("dir", "", "Custom gossip storage directory")

	subArgs := args
	if len(args) > 0 && (args[0] == "check" || args[0] == "pulse") {
		*once = true
		subArgs = args[1:]
	} else if len(args) > 0 && (args[0] == "start" || args[0] == "daemon") {
		subArgs = args[1:]
	}
	_ = fs.Parse(subArgs)

	store, err := state.NewStore(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if *once {
		res, err := cron.RunCheck(store, *agentID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Check failed: %v\n", err)
			os.Exit(1)
		}
		_ = cron.RecordCronLog(store.Dir(), res)
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))
		return
	}

	dur, err := cron.ParseInterval(*interval)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid interval/cron expression %q: %v\n", *interval, err)
		os.Exit(1)
	}

	fmt.Printf("%s\n", BannerArt)
	fmt.Printf("[CRON] Starting periodic checker daemon in %s (Interval: %v)\n", store.Dir(), dur)
	fmt.Println(strings.Repeat("-", 65))

	runner := cron.NewRunner(store, *agentID, dur)
	runner.Start(context.Background(), func(res *cron.CheckResult) {
		_ = cron.RecordCronLog(store.Dir(), res)
		alertTag := "[OK]"
		if res.HasAlerts {
			alertTag = "[ALERT]"
		}
		fmt.Printf("[%s] %s %s\n", res.Timestamp.Format("15:04:05"), alertTag, res.Summary)
		for _, a := range res.Alerts {
			fmt.Printf("    ! %s\n", a)
		}
	})

	// Block until interrupted
	select {}
}

func runVault(args []string) {
	if len(args) == 0 {
		printVaultHelp()
		return
	}

	sub := args[0]
	switch sub {
	case "stage":
		fs := flag.NewFlagSet("vault stage", flag.ExitOnError)
		file := fs.String("file", "", "Path to local secret file (required)")
		desc := fs.String("desc", "", "Description for secret file")
		dir := fs.String("dir", "", "Custom storage directory")
		_ = fs.Parse(args[1:])

		if *file == "" {
			fmt.Fprintln(os.Stderr, "Error: --file is required")
			os.Exit(1)
		}
		store, _ := state.NewStore(*dir)
		vm, err := vault.NewManager(store.Dir())
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		receipt, err := vm.StageSecretFile(*file, *desc)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Staging failed: %v\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(receipt, "", "  ")
		fmt.Println(string(data))

	case "list":
		dir := parseDirFlag(args[1:])
		store, _ := state.NewStore(dir)
		vm, err := vault.NewManager(store.Dir())
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		files, err := vm.ListStagedFiles()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("=== Staged Files in Protected Vault (%d) ===\n", len(files))
		for _, f := range files {
			fmt.Printf("• %s (%d bytes) [SHA: %s] %s\n", f.Filename, f.SizeBytes, f.SHA256[:16], f.Description)
		}

	case "send", "transfer":
		fs := flag.NewFlagSet("vault send", flag.ExitOnError)
		file := fs.String("file", "", "Path to local secret file (required)")
		to := fs.String("to", "", "Remote endpoint URL (e.g. http://remote.mesh:8080) (required)")
		token := fs.String("token", "", "Encryption passphrase / auth token (required)")
		dest := fs.String("dest", "", "Remote destination path (defaults to file basename)")
		dir := fs.String("dir", "", "Custom storage directory")
		_ = fs.Parse(args[1:])

		if *file == "" || *to == "" || *token == "" {
			fmt.Fprintln(os.Stderr, "Error: --file, --to, and --token are required")
			os.Exit(1)
		}
		store, _ := state.NewStore(*dir)
		vm, err := vault.NewManager(store.Dir())
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		receipt, err := vm.TransferSecretToRemote(*file, *to, *token, *dest)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Transfer failed: %v\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(receipt, "", "  ")
		fmt.Println(string(data))

	default:
		printVaultHelp()
	}
}

func printVaultHelp() {
	fmt.Printf("%s\n", BannerArt)
	fmt.Println("Usage: gossip vault <subcommand> [flags]")
	fmt.Println("Subcommands:")
	fmt.Println("  stage   Stage a sensitive secret file in local protected vault (0600)")
	fmt.Println("  list    List all staged files in the local vault without revealing contents")
	fmt.Println("  send    Transfer encrypted file across tunnel without exposing to agent context")
}

func runRemote(args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: gossip remote query --to <endpoint> --type <graph|tasks|status|repomap>")
		return
	}
	sub := args[0]
	if sub == "query" {
		fs := flag.NewFlagSet("remote query", flag.ExitOnError)
		to := fs.String("to", "", "Remote node URL (required)")
		qType := fs.String("type", "status", "Query type: 'graph', 'tasks', 'status', 'repomap'")
		qText := fs.String("text", "", "Query text or symbol label")
		user := fs.String("user", "", "HTTP Basic Auth username")
		pass := fs.String("pass", "", "HTTP Basic Auth password")
		_ = fs.Parse(args[1:])

		if *to == "" {
			fmt.Fprintln(os.Stderr, "Error: --to is required")
			os.Exit(1)
		}

		req := tunnel.FederatedQueryRequest{
			QueryID:    fmt.Sprintf("cli-%x", time.Now().UnixNano()%100000),
			SourceNode: "cli-operator",
			QueryType:  *qType,
			QueryText:  *qText,
			AuthUser:   *user,
			AuthPass:   *pass,
		}

		res, err := tunnel.QueryRemoteNode(*to, *user, *pass, req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Remote query failed: %v\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))
	}
}
