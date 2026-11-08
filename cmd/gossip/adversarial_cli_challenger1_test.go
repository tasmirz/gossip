package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/tasmirz/gossip/pkg/coord"
	_ "modernc.org/sqlite"
)

func TestAdversarialCLI_CoordRemoteScenarios(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "cli_adv.db")
	s, err := coord.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// 1. Recovery via CLI when row 1 is missing
	t.Run("CLIRecoversMissingRowViaDirectDB", func(t *testing.T) {
		rawDB, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer rawDB.Close()

		if _, err := rawDB.Exec(`DELETE FROM remote_access_control WHERE id=1`); err != nil {
			t.Fatal(err)
		}

		// Run CLI enable on DB with missing row
		if err := runCoordRemote([]string{"--db", dbPath, "--action", "enable"}); err != nil {
			t.Fatalf("runCoordRemote enable failed on deleted row 1: %v", err)
		}

		enabled, err := s.RemoteAccessEnabled(ctx)
		if err != nil || !enabled {
			t.Fatalf("expected enabled=true after CLI UPSERT recovery: %v %v", enabled, err)
		}
	})

	// 2. Error handling for bad inputs
	t.Run("CLIBadInputsRejected", func(t *testing.T) {
		// Invalid action
		if err := runCoordRemote([]string{"--db", dbPath, "--action", "drop_tables"}); err == nil {
			t.Fatal("expected error for invalid action drop_tables, got nil")
		}

		// Missing required flag argument
		if err := runCoordRemote([]string{"--db"}); err == nil {
			t.Fatal("expected error for missing --db argument, got nil")
		}

		// Nonexistent Unix socket
		nonExistentSock := filepath.Join(dir, "no_such.sock")
		if err := runCoordRemote([]string{"--socket", nonExistentSock, "--action", "status"}); err == nil {
			t.Fatal("expected error connecting to nonexistent socket, got nil")
		}
	})

	// 3. Concurrent CLI invocations on same DB
	t.Run("ConcurrentCLIInvocations", func(t *testing.T) {
		var wg sync.WaitGroup
		errs := make(chan error, 20)

		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				act := "disable"
				if idx%2 == 0 {
					act = "enable"
				}
				if err := runCoordRemote([]string{"--db", dbPath, "--action", act}); err != nil {
					errs <- fmt.Errorf("concurrent CLI [%d]: %w", idx, err)
				}
			}(i)
		}
		wg.Wait()
		close(errs)

		for e := range errs {
			t.Fatalf("concurrent CLI execution failed: %v", e)
		}
	})
}
