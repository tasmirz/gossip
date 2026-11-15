package coord

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDeviceRegistryStoresCredentialPrivatelyAndResolvesByProfile(t *testing.T) {
	r, err := OpenDeviceRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	c := DeviceConnection{Profile: "antigravity", HomeID: "home_a", ProjectID: "project_a", PrincipalID: "agent_a", Name: "Alpha"}
	if err = r.Put(context.Background(), c, "secret-token"); err != nil {
		t.Fatal(err)
	}
	got, token, err := r.Resolve(context.Background(), "antigravity", "project_a", "")
	if err != nil {
		t.Fatal(err)
	}
	if token != "secret-token" || got.PrincipalID != "agent_a" {
		t.Fatalf("unexpected resolved connection %#v, token %q", got, token)
	}
	if _, _, err = r.Resolve(context.Background(), "codex", "project_a", ""); err == nil {
		t.Fatal("other profile resolved the connection")
	}
	info, err := os.Stat(filepath.Join(r.configDir, "secrets", got.SecretID))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Fatalf("credential mode is %o", info.Mode().Perm())
	}
	if err = r.Remove(context.Background(), got.Profile, got.HomeID, got.ProjectID, got.PrincipalID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(r.configDir, "secrets", got.SecretID)); !os.IsNotExist(err) {
		t.Fatalf("secret survived removal: %v", err)
	}
}

func TestPrepareLocalProjectIsRetrySafeAndRejectsChangedPayload(t *testing.T) {
	r, err := OpenDeviceRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	payload := struct {
		Name     string `json:"name"`
		Checkout string `json:"checkout"`
	}{"sample", "/work/sample"}
	id, first, secretID, err := r.PrepareLocalProject(context.Background(), "request-stable-1", payload)
	if err != nil {
		t.Fatal(err)
	}
	retriedID, retried, retriedSecret, err := r.PrepareLocalProject(context.Background(), "request-stable-1", payload)
	if err != nil {
		t.Fatal(err)
	}
	if retriedID != id || retried.PrincipalID != first.PrincipalID || retried.Token != first.Token || retriedSecret != secretID {
		t.Fatal("retry did not recover the same prepared project identity and credential")
	}
	payload.Checkout = "/work/other"
	if _, _, _, err = r.PrepareLocalProject(context.Background(), "request-stable-1", payload); err != ErrConflict {
		t.Fatalf("changed payload should conflict, got %v", err)
	}
}

func TestDeviceRegistryRejectsAmbiguousRouting(t *testing.T) {
	r, err := OpenDeviceRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, c := range []DeviceConnection{{Profile: "default", HomeID: "h1", ProjectID: "p1", PrincipalID: "a1", Name: "same"}, {Profile: "default", HomeID: "h2", ProjectID: "p2", PrincipalID: "a2", Name: "same"}} {
		if err = r.Put(context.Background(), c, "token"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err = r.Resolve(context.Background(), "default", "same", ""); err == nil {
		t.Fatal("ambiguous project name was selected")
	}
	if _, _, err = r.Resolve(context.Background(), "default", "p1", ""); err != nil {
		t.Fatalf("stable ID did not disambiguate: %v", err)
	}
}
