package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tasmirz/gossip/pkg/cron"
	"github.com/tasmirz/gossip/pkg/state"
	"github.com/tasmirz/gossip/pkg/tunnel"
	"github.com/tasmirz/gossip/pkg/vault"
)

func runInstall(args []string) {
	if err := runInstallCommand(args); err != nil {
		fmt.Fprintf(os.Stderr, "Install error: %v\n", err)
		os.Exit(1)
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
	fmt.Println("  init    Disabled for the legacy HTTP dashboard; use coord-serve with mutual TLS and FRP TCP")
	fmt.Println("  config  Disabled for the legacy HTTP dashboard; use coord-serve with mutual TLS and FRP TCP")
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
		fmt.Fprintln(os.Stderr, "Legacy remote secret transfer is disabled; use an independently secured transfer method")
		os.Exit(1)

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
	fmt.Println("  send    Disabled: legacy remote secret transfer is unsafe")
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
