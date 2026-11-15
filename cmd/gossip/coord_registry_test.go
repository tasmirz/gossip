package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tasmirz/gossip/pkg/coord"
)

func TestCoordConnectionImportsAndVerifiesCredential(t *testing.T) {
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	dbPath := filepath.Join(config, "gossip", "coord.db")
	store, err := coord.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	project, owner, err := store.CreateProject(context.Background(), "registry test", "owner")
	if err != nil {
		t.Fatal(err)
	}
	homeID := store.InstanceID()
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(t.TempDir(), "owner.json")
	data, _ := json.Marshal(coordCredential{ProjectID: project.ID, PrincipalID: owner.PrincipalID, Token: owner.Token})
	if err = os.WriteFile(credentialPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = runCoordConnection([]string{"add", "--db", dbPath, "--credential-file", credentialPath, "--profile", "codex", "--name", "registry test"}); err != nil {
		t.Fatal(err)
	}
	r, err := coord.OpenDeviceRegistry(filepath.Join(config, "gossip"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	c, token, err := r.Resolve(context.Background(), "codex", project.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.HomeID != homeID || token != owner.Token {
		t.Fatalf("unexpected imported connection %#v", c)
	}
	if _, err = os.Stat(credentialPath); err != nil {
		t.Fatalf("source credential was removed: %v", err)
	}
}
