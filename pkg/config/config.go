package config

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed default_config.json
var embeddedDefaultConfig []byte

// StorageConfig holds filesystem and locking parameters.
type StorageConfig struct {
	DefaultDir string `json:"default_dir"`
	LockFile   string `json:"lock_file"`
}

// RolesConfig controls role loading and dynamic registration.
type RolesConfig struct {
	ConfigFile                string `json:"config_file"`
	AllowDynamicRegistration bool   `json:"allow_dynamic_registration"`
}

// MessagesConfig controls messaging buffers and retention.
type MessagesConfig struct {
	LogFile        string `json:"log_file"`
	MaxInboxLimit  int    `json:"max_inbox_limit"`
	MaxBufferBytes int    `json:"max_buffer_bytes"`
}

// TasksConfig configures task DAG behavior.
type TasksConfig struct {
	DataFile    string `json:"data_file"`
	BoardFile   string `json:"board_file"`
	AutoUnblock bool   `json:"auto_unblock"`
}

// GraphConfig configures knowledge graph RAG parameters.
type GraphConfig struct {
	DataFile                string `json:"data_file"`
	ReportFile              string `json:"report_file"`
	DefaultNeighborhoodDepth int    `json:"default_neighborhood_depth"`
	MaxNeighborhoodDepth     int    `json:"max_neighborhood_depth"`
}

// AgentsConfig configures session heartbeat timeouts.
type AgentsConfig struct {
	HeartbeatTimeoutSeconds int `json:"heartbeat_timeout_seconds"`
}

// Config is the top-level configuration loaded from JSON.
type Config struct {
	Version  string         `json:"version"`
	Storage  StorageConfig  `json:"storage"`
	Roles    RolesConfig    `json:"roles"`
	Messages MessagesConfig `json:"messages"`
	Tasks    TasksConfig    `json:"tasks"`
	Graph    GraphConfig    `json:"graph"`
	Agents   AgentsConfig   `json:"agents"`
}

// DefaultConfig returns a fresh copy of the default configuration parsed from JSON.
func DefaultConfig() (*Config, error) {
	var cfg Config
	if err := json.Unmarshal(embeddedDefaultConfig, &cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal default config JSON: %w", err)
	}
	return &cfg, nil
}

// LoadConfig loads configuration with fallbacks:
// 1. customPath if non-empty and exists
// 2. <storageDir>/config.json
// 3. config/config.json in project root
// 4. embedded default JSON
func LoadConfig(storageDir, customPath string) (*Config, error) {
	cfg, err := DefaultConfig()
	if err != nil {
		return nil, err
	}

	candidatePaths := []string{}
	if customPath != "" {
		candidatePaths = append(candidatePaths, customPath)
	}
	if storageDir != "" {
		candidatePaths = append(candidatePaths, filepath.Join(storageDir, "config.json"))
	}
	candidatePaths = append(candidatePaths, filepath.Join("config", "config.json"))

	for _, p := range candidatePaths {
		data, err := os.ReadFile(p)
		if err == nil {
			var override Config
			if err := json.Unmarshal(data, &override); err == nil {
				return &override, nil
			}
		}
	}

	return cfg, nil
}
