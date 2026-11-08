package coord

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxCapturedDiffBytes = 1024 * 1024
	maxCapturedDiffFiles = 1000
)

var gitObjectID = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)

// GitObservation is a point-in-time local observation, not a guarantee that
// the workspace stays unchanged after inspection.
type GitObservation struct {
	Root                 string   `json:"root"`
	CommonDirFingerprint string   `json:"common_dir_fingerprint"`
	Branch               string   `json:"branch"`
	Head                 string   `json:"head"`
	Dirty                bool     `json:"dirty"`
	RemoteHints          []string `json:"remote_hints,omitempty"`
}

type boundedOutput struct {
	data      []byte
	limit     int
	truncated bool
}

func (w *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := w.limit - len(w.data)
	if remaining > 0 {
		if len(p) > remaining {
			w.data = append(w.data, p[:remaining]...)
		} else {
			w.data = append(w.data, p...)
		}
	}
	if n > remaining {
		w.truncated = true
	}
	return n, nil
}

// InspectGitWorkspace inspects only the supplied directory. The common-dir
// fingerprint is a local matching hint and is not a project identity.
func InspectGitWorkspace(ctx context.Context, path string) (GitObservation, error) {
	var result GitObservation
	if path == "" {
		path = "."
	}
	if len(path) > 4096 {
		return result, errors.New("workspace path too long")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	run := func(limit int, args ...string) (string, bool, error) {
		base := []string{"-C", abs, "-c", "core.fsmonitor=false", "-c", "submodule.recurse=false"}
		cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
		env := make([]string, 0, len(os.Environ())+1)
		for _, entry := range os.Environ() {
			if strings.HasPrefix(strings.ToUpper(entry), "GIT_") {
				continue
			}
			env = append(env, entry)
		}
		cmd.Env = append(env, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
		out := &boundedOutput{limit: limit}
		stderr := &boundedOutput{limit: 4096}
		cmd.Stdout = out
		cmd.Stderr = stderr
		err := cmd.Run()
		if ctx.Err() != nil {
			return "", false, ctx.Err()
		}
		return strings.TrimSuffix(strings.TrimSuffix(string(out.data), "\n"), "\r"), out.truncated, err
	}
	root, over, err := run(8192, "rev-parse", "--show-toplevel")
	if err != nil || over || root == "" {
		return result, fmt.Errorf("cannot inspect Git worktree: %w", errors.New("git rev-parse failed"))
	}
	common, over, err := run(8192, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || over || common == "" {
		return result, errors.New("cannot determine Git common directory")
	}
	branch, over, err := run(1024, "symbolic-ref", "--quiet", "--short", "HEAD")
	if over {
		return result, errors.New("branch name too long")
	}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return result, errors.New("cannot determine Git branch")
		}
		branch = ""
	}
	head, over, err := run(1024, "rev-parse", "--verify", "--quiet", "HEAD")
	if over {
		return result, errors.New("Git head too long")
	}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return result, errors.New("cannot determine Git head")
		}
		head = ""
	}
	status, overflow, err := run(1, "status", "--porcelain=v1", "-z", "--untracked-files=normal", "--ignore-submodules=none")
	if err != nil {
		return result, errors.New("cannot determine workspace status")
	}
	remoteNames, over, err := run(4096, "remote")
	if err != nil || over {
		return result, errors.New("cannot inspect Git remotes")
	}
	hints := []string{}
	seen := make(map[string]bool)
	if remoteNames != "" {
		names := strings.Split(remoteNames, "\n")
		if len(names) > 16 {
			return result, errors.New("too many Git remotes")
		}
		for _, name := range names {
			urls, over, err := run(8192, "remote", "get-url", "--all", name)
			if err != nil || over {
				return result, errors.New("cannot inspect Git remote URLs")
			}
			for _, raw := range strings.Split(urls, "\n") {
				if raw == "" {
					continue
				}
				key, err := NormalizeRepositoryRemote(raw)
				if err != nil || seen[key] {
					continue
				}
				if len(hints) == 32 {
					return result, errors.New("too many Git remote URLs")
				}
				seen[key] = true
				hints = append(hints, key)
			}
		}
	}
	hash := sha256.Sum256([]byte(filepath.Clean(common)))
	return GitObservation{Root: root, CommonDirFingerprint: hex.EncodeToString(hash[:]), Branch: branch, Head: head, Dirty: overflow || status != "", RemoteHints: hints}, nil
}

// CaptureGitDiff returns a bounded unified diff from a recorded commit to the
// current worktree, including non-ignored untracked files. It disables
// external diff/textconv helpers and ignores inherited GIT_* overrides.
func CaptureGitDiff(ctx context.Context, path, baseline string) (string, error) {
	baseline = strings.TrimPrefix(strings.TrimSpace(baseline), "commit:")
	if !gitObjectID.MatchString(baseline) {
		return "", errors.New("full-code diff capture needs the attempt's recorded Git commit baseline")
	}
	observation, err := InspectGitWorkspace(ctx, path)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	root := observation.Root
	var diff boundedOutput
	diff.limit = maxCapturedDiffBytes
	appendCommand := func(allowDiffExit bool, args ...string) error {
		remaining := diff.limit - len(diff.data)
		if remaining <= 0 {
			return errors.New("captured source diff exceeds the 1 MiB limit")
		}
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root, "-c", "core.fsmonitor=false", "-c", "submodule.recurse=false"}, args...)...)
		cmd.Env = cleanGitEnvironment()
		cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
		out := &boundedOutput{limit: remaining}
		stderr := &boundedOutput{limit: 4096}
		cmd.Stdout, cmd.Stderr = out, stderr
		runErr := cmd.Run()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if out.truncated {
			return errors.New("captured source diff exceeds the 1 MiB limit")
		}
		var exit *exec.ExitError
		if runErr != nil && !(allowDiffExit && errors.As(runErr, &exit) && exit.ExitCode() == 1) {
			return fmt.Errorf("git diff failed: %s", strings.TrimSpace(string(stderr.data)))
		}
		diff.data = append(diff.data, out.data...)
		return nil
	}
	if err = appendCommand(false, "diff", "--binary", "--no-ext-diff", "--no-textconv", "--no-color", "--find-renames", baseline, "--"); err != nil {
		return "", err
	}
	remaining := maxCapturedDiffBytes - len(diff.data)
	untracked := &boundedOutput{limit: remaining}
	cmd := exec.CommandContext(ctx, "git", "-C", root, "-c", "core.fsmonitor=false", "-c", "submodule.recurse=false", "ls-files", "--others", "--exclude-standard", "-z")
	cmd.Env = append(cleanGitEnvironment(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	cmd.Stdout = untracked
	cmd.Stderr = &boundedOutput{limit: 4096}
	if err = cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("cannot enumerate untracked Git files")
	}
	if untracked.truncated {
		return "", errors.New("untracked file list exceeds the source diff limit")
	}
	untrackedPaths := strings.Split(strings.TrimSuffix(string(untracked.data), "\x00"), "\x00")
	if len(untrackedPaths) > maxCapturedDiffFiles {
		return "", errors.New("untracked file count exceeds the 1000-file capture limit")
	}
	for _, relative := range untrackedPaths {
		if relative == "" {
			continue
		}
		filePath := filepath.Join(root, filepath.FromSlash(relative))
		if err = appendCommand(true, "diff", "--no-index", "--binary", "--no-ext-diff", "--no-textconv", "--no-color", "--", "/dev/null", filePath); err != nil {
			return "", err
		}
	}
	if len(diff.data) == 0 {
		return "", errors.New("there are no source changes to review at the recorded baseline")
	}
	if !utf8.Valid(diff.data) {
		return "", errors.New("captured Git diff is not valid UTF-8; use a binary-safe review artifact")
	}
	return string(diff.data), nil
}

func cleanGitEnvironment() []string {
	env := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		if strings.HasPrefix(strings.ToUpper(entry), "GIT_") {
			continue
		}
		env = append(env, entry)
	}
	return env
}
