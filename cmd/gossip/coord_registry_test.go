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

func TestLocalProjectsDiscoveryAndPermissionGatedJoin(t *testing.T) {
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	dbPath := filepath.Join(config, "gossip", "coord.db")
	store, err := coord.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	pGesture, _, err := store.CreateProject(ctx, "gesture", "owner")
	if err != nil {
		t.Fatal(err)
	}
	pGossip, _, err := store.CreateProject(ctx, "gossip", "owner")
	if err != nil {
		t.Fatal(err)
	}

	registry, err := coord.OpenDeviceRegistry(filepath.Join(config, "gossip"))
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()

	// Seed a local "gesture" profile connected to gesture, plus a remote LAN/FRP profile "lan-peer".
	credGesture, err := store.EnrollLocalWorker(ctx, pGesture.ID, "antigravity", "initial")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Put(ctx, coord.DeviceConnection{
		Profile:     "gesture",
		HomeID:      store.InstanceID(),
		ProjectID:   pGesture.ID,
		PrincipalID: credGesture.PrincipalID,
		Name:        "gesture",
	}, credGesture.Token); err != nil {
		t.Fatal(err)
	}
	if err := registry.Put(ctx, coord.DeviceConnection{
		Profile:     "lan-peer",
		HomeID:      "node_remote_home",
		ProjectID:   "proj_remote_only",
		PrincipalID: "principal_remote",
		Name:        "remote-proj",
		Endpoint:    "https://192.168.1.50:47282",
	}, "tok_remote"); err != nil {
		t.Fatal(err)
	}

	// 1. Local profile sees unjoined local project "gossip".
	snapRaw, err := listRegistryProjectsSnapshot(ctx, registry, store, "gesture", func(coord.DeviceConnection, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	snapBytes, _ := json.Marshal(snapRaw)
	var snap struct {
		LocalDiscoveryAllowed bool `json:"local_discovery_allowed"`
		LocalAvailable        []struct {
			ProjectID string `json:"project_id"`
			Name      string `json:"name"`
		} `json:"local_available"`
	}
	if err := json.Unmarshal(snapBytes, &snap); err != nil {
		t.Fatal(err)
	}
	if !snap.LocalDiscoveryAllowed || len(snap.LocalAvailable) != 1 || snap.LocalAvailable[0].ProjectID != pGossip.ID {
		t.Fatalf("expected gossip in local_available, got %s", snapBytes)
	}

	// 2. Local join MUST fail when user_permission is false.
	if _, err := joinLocalRegistryProject(ctx, registry, store, "gesture", "", "gossip", "antigravity", "", false); err == nil {
		t.Fatal("expected error when user_permission is false")
	}

	// 3. Local join succeeds when user_permission is true and stores a valid 0600 credential without returning any token.
	joinedRaw, err := joinLocalRegistryProject(ctx, registry, store, "gesture", "", "gossip", "antigravity", "user approved", true)
	if err != nil {
		t.Fatalf("joinLocalRegistryProject: %v", err)
	}
	joinedBytes, _ := json.Marshal(joinedRaw)
	var joined map[string]any
	_ = json.Unmarshal(joinedBytes, &joined)
	if joined["status"] != "joined" || joined["project_id"] != pGossip.ID || joined["role"] != "worker" {
		t.Fatalf("unexpected join result: %s", joinedBytes)
	}
	if _, hasToken := joined["token"]; hasToken {
		t.Fatalf("join response must never leak token: %s", joinedBytes)
	}
	if _, _, err := registry.Resolve(ctx, "gesture", pGossip.ID, ""); err != nil {
		t.Fatalf("expected gesture profile to resolve newly joined project gossip: %v", err)
	}

	// 4. Remote LAN/FRP profile MUST NOT see local_available or join any local/remote project.
	remoteSnapRaw, err := listRegistryProjectsSnapshot(ctx, registry, store, "lan-peer", func(coord.DeviceConnection, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	remoteBytes, _ := json.Marshal(remoteSnapRaw)
	var remoteSnap struct {
		LocalDiscoveryAllowed bool  `json:"local_discovery_allowed"`
		LocalAvailable        []any `json:"local_available"`
	}
	_ = json.Unmarshal(remoteBytes, &remoteSnap)
	if remoteSnap.LocalDiscoveryAllowed || len(remoteSnap.LocalAvailable) != 0 {
		t.Fatalf("remote LAN/FRP profile must never discover local projects: %s", remoteBytes)
	}
	if _, err := joinLocalRegistryProject(ctx, registry, store, "lan-peer", "lan-peer", "gossip", "remote", "nope", true); err == nil {
		t.Fatal("expected remote LAN/FRP profile to be blocked from joining local projects")
	}
	if _, err := joinLocalRegistryProject(ctx, registry, store, "gesture", "gesture", "remote-proj", "antigravity", "nope", true); err == nil {
		t.Fatal("expected joining remote LAN/FRP project to be blocked")
	}
}
