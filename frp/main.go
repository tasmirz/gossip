package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	frpclient "github.com/fatedier/frp/client"
	"github.com/fatedier/frp/pkg/config"
	"github.com/fatedier/frp/pkg/config/source"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/fatedier/frp/pkg/config/v1/validation"
	"github.com/fatedier/frp/pkg/policy/security"
	"github.com/fatedier/frp/pkg/util/log"
	"github.com/fatedier/frp/pkg/util/system"
	"github.com/fatedier/frp/pkg/util/version"
	frpserver "github.com/fatedier/frp/server"
)

func main() {
	system.EnableCompatibilityMode()
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "gossip-frp: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}
	switch args[0] {
	case "client", "frpc":
		return runClientCmd(args[1:])
	case "server", "frps":
		return runServerCmd(args[1:])
	case "-v", "--version", "version":
		fmt.Printf("gossip-frp (frp %s)\n", version.Full())
		return nil
	case "-h", "--help", "help":
		printUsage()
		return nil
	default:
		if strings.HasPrefix(args[0], "-") {
			return runClientCmd(args)
		}
		printUsage()
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func printUsage() {
	fmt.Println(`gossip-frp - Companion FRP tunnel binary for Gossip

Usage:
  gossip-frp client -c <frpc.toml>
  gossip-frp server [-c <frps.toml> | --port 7000 --token <secret>]
  gossip-frp version`)
}

func runClientCmd(args []string) error {
	fs := flag.NewFlagSet("client", flag.ContinueOnError)
	cfgPath := fs.String("c", "", "path to FRP client config (TOML)")
	cfgLong := fs.String("config", "", "path to FRP client config (TOML)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path := *cfgPath
	if path == "" {
		path = *cfgLong
	}
	if path == "" {
		return fmt.Errorf("client config file (-c) is required")
	}
	unsafe := security.NewUnsafeFeatures(nil)
	result, err := config.LoadClientConfigResult(path, true)
	if err != nil {
		return err
	}
	if result.IsLegacyFormat {
		return fmt.Errorf("legacy INI FRP configs are not supported; use TOML")
	}
	configSource := source.NewConfigSource()
	if err := configSource.ReplaceAll(result.Proxies, result.Visitors); err != nil {
		return fmt.Errorf("set config source: %w", err)
	}
	aggregator := source.NewAggregator(configSource)
	proxyCfgs, visitorCfgs, err := aggregator.Load()
	if err != nil {
		return fmt.Errorf("load config from source: %w", err)
	}
	proxyCfgs, visitorCfgs = config.FilterClientConfigurers(result.Common, proxyCfgs, visitorCfgs)
	proxyCfgs = config.CompleteProxyConfigurers(proxyCfgs)
	visitorCfgs = config.CompleteVisitorConfigurers(visitorCfgs)
	if _, err := validation.ValidateAllClientConfig(result.Common, proxyCfgs, visitorCfgs, unsafe); err != nil {
		return err
	}
	log.InitLogger(result.Common.Log.To, result.Common.Log.Level, int(result.Common.Log.MaxDays), result.Common.Log.DisablePrintColor)
	svc, err := frpclient.NewService(frpclient.ServiceOptions{
		Common:                 result.Common,
		ConfigSourceAggregator: aggregator,
		UnsafeFeatures:         unsafe,
		ConfigFilePath:         path,
	})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		svc.GracefulClose(500 * time.Millisecond)
	}()
	return svc.Run(ctx)
}

func runServerCmd(args []string) error {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	cfgPath := fs.String("c", "", "path to FRP server config (TOML)")
	cfgLong := fs.String("config", "", "path to FRP server config (TOML)")
	addr := fs.String("addr", "0.0.0.0", "bind address when not using -c")
	port := fs.Int("port", 7000, "bind port when not using -c")
	token := fs.String("token", "", "auth token when not using -c")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path := *cfgPath
	if path == "" {
		path = *cfgLong
	}
	unsafe := security.NewUnsafeFeatures(nil)
	var svrCfg *v1.ServerConfig
	if path != "" {
		loaded, isLegacy, err := config.LoadServerConfig(path, true)
		if err != nil {
			return err
		}
		if isLegacy {
			return fmt.Errorf("legacy INI FRP configs are not supported; use TOML")
		}
		svrCfg = loaded
	} else {
		if *token == "" {
			return fmt.Errorf("--token is required when running without -c")
		}
		svrCfg = &v1.ServerConfig{
			BindAddr: *addr,
			BindPort: *port,
		}
		svrCfg.Auth.Method = "token"
		svrCfg.Auth.Token = *token
		if err := svrCfg.Complete(); err != nil {
			return err
		}
	}
	if _, err := validation.NewConfigValidator(unsafe).ValidateServerConfig(svrCfg); err != nil {
		return err
	}
	log.InitLogger(svrCfg.Log.To, svrCfg.Log.Level, int(svrCfg.Log.MaxDays), svrCfg.Log.DisablePrintColor)
	svc, err := frpserver.NewService(svrCfg)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = svc.Close()
	}()
	svc.Run(ctx)
	return nil
}
