//go:build !windows

package coord

import (
	"os"
	"syscall"
)

func ordinaryPath(_ string, info os.FileInfo) bool {
	if info.IsDir() {
		return true
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && stat.Nlink == 1
}

func replaceGuardedFile(source, target string) error {
	return os.Rename(source, target)
}
