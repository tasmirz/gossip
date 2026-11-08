package coord_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tasmirz/gossip/pkg/coord"
)

func TestLocalDaemonCredentialIsolationAndConcurrentClaims(t *testing.T) {
	store, _, p, owner, worker := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "gossip.sock")
	done := make(chan error, 1)
	go func() { done <- coord.ServeLocal(ctx, store, path) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("daemon did not stop")
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSocket != 0 && info.Mode().Perm()&0077 == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	a, err := coord.NewLocalRemote(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := coord.NewLocalRemote(path)
	if err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(t.TempDir(), "local-project")
	if err = os.MkdirAll(checkout, 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"remote", "add", "origin", "https://github.com/acme/local-project.git"}} {
		cmd := exec.Command("git", append([]string{"-C", checkout}, args...)...)
		if output, gitErr := cmd.CombinedOutput(); gitErr != nil {
			t.Fatalf("git %v: %v: %s", args, gitErr, output)
		}
	}
	observation, err := coord.InspectGitWorkspace(ctx, checkout)
	if err != nil {
		t.Fatal(err)
	}
	newProject, _, repository, workspace, err := a.CreateProjectWithWorkspace(ctx, "local project", "local owner", "local-project", "test-host", observation)
	if err != nil || newProject.ID == "" || repository.ID == "" || workspace.RepositoryID != repository.ID || workspace.LocalPath != checkout {
		t.Fatalf("local socket project setup: project=%+v repository=%+v workspace=%+v err=%v", newProject, repository, workspace, err)
	}
	if _, err = a.CurrentPlan(ctx, p.ID, "invalid"); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("invalid credential: %v", err)
	}
	task := create(t, store, p, owner, nil)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, c := range []*coord.Remote{a, b} {
		wg.Add(1)
		go func(c *coord.Remote) {
			defer wg.Done()
			_, err := c.ClaimTaskInWorkspace(ctx, p.ID, worker.Token, task.ID, "", time.Minute)
			results <- err
		}(c)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, coord.ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("claims success=%d conflict=%d", success, conflict)
	}
	other, otherOwner, err := store.CreateProject(ctx, "other", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.GetTask(ctx, other.ID, otherOwner.Token, task.ID); !errors.Is(err, coord.ErrNotFound) {
		t.Fatalf("cross project: %v", err)
	}
}

func TestLocalRemoteSwitchDefaultsOffAndIsControllableOverSocket(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := coord.Open(filepath.Join(dir, "home.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	socket := filepath.Join(dir, "coord.sock")
	done := make(chan error, 1)
	go func() { done <- coord.ServeLocal(ctx, s, socket) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("daemon did not stop")
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err = os.Stat(socket); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	r, err := coord.NewLocalRemote(socket)
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := r.LocalRemoteAccessStatus(context.Background())
	if err != nil || enabled {
		t.Fatalf("remote access must default off: enabled=%v err=%v", enabled, err)
	}
	if err = r.LocalSetRemoteAccessEnabled(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	enabled, err = r.LocalRemoteAccessStatus(context.Background())
	if err != nil || !enabled {
		t.Fatalf("local socket could not enable remote access: enabled=%v err=%v", enabled, err)
	}
	if err = r.LocalSetRemoteAccessEnabled(context.Background(), false); err != nil {
		t.Fatal(err)
	}
}
func TestLocalDaemonRejectsUnsafeSocketPaths(t *testing.T) {
	s, _, _, _, _ := setup(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "existing")
	if err := os.WriteFile(path, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := coord.ServeLocal(context.Background(), s, path); err == nil {
		t.Fatal("accepted existing file")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "preserve" {
		t.Fatalf("file changed: %q %v", data, err)
	}
	unsafe := filepath.Join(dir, "public")
	if err = os.Mkdir(unsafe, 0755); err != nil {
		t.Fatal(err)
	}
	if err = coord.ServeLocal(context.Background(), s, filepath.Join(unsafe, "sock")); err == nil {
		t.Fatal("accepted public directory")
	}
	private := filepath.Join(dir, "private")
	if err = os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err = os.Symlink(private, alias); err != nil {
		t.Fatal(err)
	}
	symlinkedPath := filepath.Join(alias, "sock")
	if err = coord.ServeLocal(context.Background(), s, symlinkedPath); err == nil {
		t.Fatal("accepted symlinked socket directory")
	}
	if _, err = coord.NewLocalRemote(symlinkedPath); err == nil {
		t.Fatal("client accepted symlinked socket directory")
	}
}

func TestLocalDaemonEventRetentionRoundTrip(t *testing.T) {
	store, _, project, owner, worker := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "retention.sock")
	done := make(chan error, 1)
	go func() { done <- coord.ServeLocal(ctx, store, path) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	var remote *coord.Remote
	var err error
	for time.Now().Before(deadline) {
		remote, err = coord.NewLocalRemote(path)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err = remote.SendMessage(ctx, project.ID, owner.Token, worker.PrincipalID, "", "retention", "test"); err != nil {
		t.Fatal(err)
	}
	status, err := remote.EventRetentionStatus(ctx, project.ID, owner.Token)
	if err != nil || status.SnapshotSequence == 0 {
		t.Fatalf("remote retention status failed: %+v %v", status, err)
	}
	if _, err = remote.PruneProjectEvents(ctx, project.ID, owner.Token, status.SnapshotSequence); err != nil {
		t.Fatalf("remote owner could not prune event history: %v", err)
	}
	if _, err = remote.PruneProjectEvents(ctx, project.ID, worker.Token, status.SnapshotSequence+1); !errors.Is(err, coord.ErrUnauthorized) {
		t.Fatalf("remote worker was allowed to prune event history: %v", err)
	}
}

func TestDashboardUsesLocalDaemonAndHonorsRevocation(t *testing.T) {
	store, _, p, owner, worker := setup(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "daemon.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- coord.ServeLocal(ctx, store, path) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	var backend *coord.Remote
	var err error
	for time.Now().Before(deadline) {
		backend, err = coord.NewLocalRemote(path)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	h := coord.NewWebHandler(backend)
	loginBody, _ := json.Marshal(map[string]string{"project_id": p.ID, "token": worker.Token})
	req := httptest.NewRequest("POST", "http://localhost:8081/api/login", bytes.NewReader(loginBody))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Origin", "http://localhost:8081")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	cookie := rec.Result().Cookies()[0]
	for _, endpoint := range []string{"task-counts", "events?recent=1", "messages?recent=1", "roles", "budget"} {
		r := httptest.NewRequest("GET", "http://localhost:8081/api/projects/"+p.ID+"/"+endpoint, nil)
		r.RemoteAddr = "127.0.0.1:12345"
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", endpoint, w.Code, w.Body.String())
		}
	}
	if err = store.RevokePrincipal(ctx, p.ID, owner.Token, worker.PrincipalID); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "http://localhost:8081/api/projects/"+p.ID+"/tasks", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked dashboard: %d %s", w.Code, w.Body.String())
	}
}
