package coord

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestSharedWorktreeGuardedEdit(t *testing.T) {
	ctx := context.Background()
	checkout := t.TempDir()
	if output, err := exec.Command("git", "init", checkout).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	dir := t.TempDir()
	first, err := OpenDeviceRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenDeviceRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	a := DeviceConnection{Profile: "first", HomeID: "home", ProjectID: "one", PrincipalID: "a", Checkout: checkout}
	b := DeviceConnection{Profile: "second", HomeID: "home", ProjectID: "two", PrincipalID: "b", Checkout: checkout}
	target := filepath.Join(checkout, "file.txt")
	original := []byte("original")
	if err := os.WriteFile(target, original, 0600); err != nil {
		t.Fatal(err)
	}
	lease, err := first.FileReservations(ctx, a, "reserve", "", []string{"file.txt"})
	if err != nil || len(lease) != 1 {
		t.Fatalf("reserve: %v %v", lease, err)
	}
	if _, err := second.FileReservations(ctx, b, "reserve", "", []string{"different.txt", "FILE.txt"}); err == nil {
		t.Fatal("alias did not conflict")
	}
	other, err := second.FileReservations(ctx, b, "reserve", "", []string{"different.txt"})
	if err != nil || len(other) != 1 {
		t.Fatalf("failed reservation left a partial lease: %v %v", other, err)
	}
	handle := lease[0].Handle
	hash := fmt.Sprintf("%x", sha256.Sum256(original))
	if err := second.GuardedFileEdit(ctx, b, handle, "file.txt", hash, false, []byte("foreign")); err == nil {
		t.Fatal("foreign handle authorized write")
	}
	if err := first.GuardedFileEdit(ctx, a, handle, "file.txt", "absent", false, []byte("stale")); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale expectation: %v", err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != string(original) {
		t.Fatalf("failed edit changed file: %q %v", got, err)
	}
	if err := first.GuardedFileEdit(ctx, a, handle, "file.txt", hash, false, []byte("updated")); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "updated" {
		t.Fatalf("edit did not update file: %q %v", got, err)
	}
	if _, err := first.FileReservations(ctx, a, "release", handle, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := second.FileReservations(ctx, b, "reserve", "", []string{"file.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := first.GuardedFileEdit(ctx, a, handle, "file.txt", hash, true, nil); err == nil {
		t.Fatal("released handle authorized delete")
	}
}

func TestReservationProcessHelper(t *testing.T) {
	dir := os.Getenv("GOSSIP_TEST_RESERVATION_REGISTRY")
	if dir == "" {
		return
	}
	r, err := OpenDeviceRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	c := DeviceConnection{Profile: "child", Checkout: os.Getenv("GOSSIP_TEST_RESERVATION_CHECKOUT")}
	if _, err := r.FileReservations(context.Background(), c, "reserve", "", []string{"shared.txt"}); err != nil {
		t.Fatal(err)
	}
	root, err := r.reservationRoot(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := r.lockWorktree(root)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	fmt.Println("lock held")
	time.Sleep(time.Minute)
}

func TestReservationSurvivesProcessCrashUntilExpiry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dir, checkout := t.TempDir(), t.TempDir()
	if output, err := exec.CommandContext(ctx, "git", "init", checkout).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReservationProcessHelper$")
	child.Env = append(os.Environ(), "GOSSIP_TEST_RESERVATION_REGISTRY="+dir, "GOSSIP_TEST_RESERVATION_CHECKOUT="+checkout)
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "lock held\n" {
		t.Fatalf("child did not acquire lock: %q %v", line, err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err == nil {
		t.Fatal("expected killed child")
	}
	r, err := OpenDeviceRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	c := DeviceConnection{Profile: "parent", Checkout: checkout}
	if _, err := r.FileReservations(ctx, c, "reserve", "", []string{"shared.txt"}); err == nil {
		t.Fatal("reservation was lost on process exit")
	}
	if _, err := r.FileReservations(ctx, c, "reserve", "", []string{"independent.txt"}); err != nil {
		t.Fatalf("worktree lock remained held: %v", err)
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE file_reservations SET expires_at=0 WHERE path='shared.txt'`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.FileReservations(ctx, c, "reserve", "", []string{"shared.txt"}); err != nil {
		t.Fatalf("cannot reclaim expired reservation: %v", err)
	}
}

func TestLinkedWorktreesHaveIndependentReservations(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	root := filepath.Join(base, "original")
	linked := filepath.Join(base, "linked")
	runGit := func(args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	runGit("init", root)
	runGit("-C", root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "initial")
	runGit("-C", root, "worktree", "add", "--detach", linked)
	registry, err := OpenDeviceRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	for _, checkout := range []string{root, linked} {
		c := DeviceConnection{Profile: "agent", Checkout: checkout}
		leases, err := registry.FileReservations(ctx, c, "reserve", "", []string{"same.txt"})
		if err != nil || len(leases) != 1 {
			t.Fatalf("reserve in %s: %v %v", checkout, leases, err)
		}
		if err := registry.GuardedFileEdit(ctx, c, leases[0].Handle, "same.txt", "absent", false, []byte(checkout)); err != nil {
			t.Fatal(err)
		}
	}
	for _, checkout := range []string{root, linked} {
		body, err := os.ReadFile(filepath.Join(checkout, "same.txt"))
		if err != nil || string(body) != checkout {
			t.Fatalf("edit escaped its worktree: %q %v", body, err)
		}
	}
}
