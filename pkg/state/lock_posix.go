//go:build !windows

package state

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

func lockFile(f *os.File) error {
	deadline := time.Now().Add(10 * time.Second)
	backoff := 5 * time.Millisecond

	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}

		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return fmt.Errorf("unexpected flock error: %w", err)
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("lock acquisition timed out after 10s (possible contention or stale lock on %s)", f.Name())
		}

		time.Sleep(backoff)
		if backoff < 100*time.Millisecond {
			backoff *= 2
		}
	}
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
