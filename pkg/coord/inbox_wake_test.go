//go:build !windows

package coord

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestInboxWakeRetriesAcrossRegistryRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	r, err := OpenDeviceRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	c := DeviceConnection{Profile: "worker", HomeID: "home", ProjectID: "project", PrincipalID: "worker", Name: "Project"}
	if err := r.Put(ctx, c, "secret"); err != nil {
		t.Fatal(err)
	}
	c, _, err = r.Resolve(ctx, c.Profile, c.ProjectID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.saveInboxPage(ctx, c, ProjectSyncFrame{InboxMessages: []InboxMessage{{ID: "message", Sender: "owner", Preview: "private preview"}}}, false); err != nil {
		t.Fatal(err)
	}
	if err := r.deliverInboxWake(ctx, c); err != nil {
		t.Fatal(err)
	}
	wake := filepath.Join(dir, "wake")
	if err := os.Mkdir(wake, 0700); err != nil {
		t.Fatal(err)
	}
	adapter := filepath.Join(wake, "adapter")
	script := "#!/bin/sh\nset -eu\ncat >> \"$0.calls\"\nprintf '\\n' >> \"$0.calls\"\ntest -e \"$0.success\"\n"
	if err := os.WriteFile(adapter, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(adapter)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wake, fmt.Sprintf("%x.json", sha256.Sum256([]byte(c.Profile)))), config, 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.deliverInboxWake(ctx, c); err == nil {
		t.Fatal("expected failed delivery")
	}
	if err := r.saveInboxPage(ctx, c, ProjectSyncFrame{InboxMessages: []InboxMessage{{ID: "another", Sender: "owner"}}}, false); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r, err = OpenDeviceRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(adapter+".success", nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.deliverInboxWake(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := r.deliverInboxWake(ctx, c); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(adapter + ".calls")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	var first, second, third json.RawMessage
	if err := dec.Decode(&first); err != nil {
		t.Fatal(err)
	}
	if err := dec.Decode(&second); err != nil {
		t.Fatal(err)
	}
	if err := dec.Decode(&third); err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) || string(third) == string(first) || dec.More() {
		t.Fatalf("unexpected wake attempts: %s / %s", first, second)
	}
	status, err := r.InboxStatus(ctx, c)
	if err != nil || status.QueueCount != 2 {
		t.Fatalf("wake removed a queued item: %+v %v", status, err)
	}
}
