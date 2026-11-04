package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tasmirz/gossip/pkg/config"
)

func TestDefaultConfig(t *testing.T) {
	cfg, err := config.DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig failed: %v", err)
	}

	if cfg.Storage.DefaultDir != ".local/gossip" {
		t.Fatalf("Default storage dir mismatch: %s", cfg.Storage.DefaultDir)
	}
	if cfg.Messages.MaxInboxLimit != 50 {
		t.Fatalf("Max inbox limit mismatch: %d", cfg.Messages.MaxInboxLimit)
	}
	if !cfg.Tasks.AutoUnblock {
		t.Fatalf("AutoUnblock should be true")
	}
}

func TestLoadConfigOverride(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-cfg-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	overrideJSON := `{
		"version": "2.0.0",
		"storage": {
			"default_dir": ".custom/gossip",
			"lock_file": ".custom_lock"
		},
		"tasks": {
			"data_file": "custom_tasks.json",
			"board_file": "CUSTOM_BOARD.md",
			"auto_unblock": false
		}
	}`

	cfgFile := filepath.Join(tmpDir, "config.json")
	if err := os.WriteFile(cfgFile, []byte(overrideJSON), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadConfig(tmpDir, cfgFile)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if cfg.Version != "2.0.0" {
		t.Fatalf("Expected version 2.0.0, got %s", cfg.Version)
	}
	if cfg.Storage.DefaultDir != ".custom/gossip" {
		t.Fatalf("Storage dir override mismatch: %s", cfg.Storage.DefaultDir)
	}
	if cfg.Tasks.AutoUnblock {
		t.Fatalf("Expected AutoUnblock to be false")
	}
}
