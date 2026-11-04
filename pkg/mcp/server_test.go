package mcp_test

import (
	"os"
	"testing"

	gossipmcp "github.com/tasmirz/gossip/pkg/mcp"
	"github.com/tasmirz/gossip/pkg/state"
)

func TestMCPServerInitialization(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-mcp-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := state.NewStore(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	srv := gossipmcp.NewServer(store)
	if srv == nil {
		t.Fatal("Expected non-nil MCP server")
	}
}
