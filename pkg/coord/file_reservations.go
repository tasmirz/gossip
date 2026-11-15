package coord

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tasmirz/gossip/pkg/state"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

type FileReservation struct {
	Path      string `json:"path"`
	Holder    string `json:"holder"`
	Handle    string `json:"handle,omitempty"`
	ExpiresAt int64  `json:"expires_at"`
}

func (r *DeviceRegistry) reservationRoot(ctx context.Context, c DeviceConnection) (string, error) {
	if c.Checkout == "" {
		return "", errors.New("configure a local checkout for this identity")
	}
	observation, err := InspectGitWorkspace(ctx, c.Checkout)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(observation.Root)
}

func reservationPath(root, path string) (string, string, error) {
	if !filepath.IsLocal(path) || path == "." || strings.ContainsAny(path, "\\:") {
		return "", "", errors.New("a worktree-relative file path is required")
	}
	parts := strings.Split(path, "/")
	current := root
	for i, part := range parts {
		if part == "" || part == "." || part == ".." || strings.EqualFold(strings.TrimRight(part, " ."), ".git") || strings.EqualFold(part, ".git") || strings.TrimRight(part, " .") != part {
			return "", "", errors.New("invalid worktree path component")
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) && i == len(parts)-1 {
			break
		}
		if err != nil {
			return "", "", err
		}
		if !ordinaryPath(current, info) || (i == len(parts)-1 && !info.Mode().IsRegular()) || (i < len(parts)-1 && !info.IsDir()) {
			return "", "", errors.New("path must refer to a regular file inside ordinary directories")
		}
	}
	return current, norm.NFC.String(cases.Fold().String(path)), nil
}

func (r *DeviceRegistry) lockWorktree(root string) (func(), error) {
	hash := sha256.Sum256([]byte(norm.NFC.String(cases.Fold().String(root))))
	locks, err := state.NewStore(filepath.Join(r.configDir, "file-locks", fmt.Sprintf("%x", hash)))
	if err != nil {
		return nil, err
	}
	return locks.Lock()
}

func (r *DeviceRegistry) FileReservations(ctx context.Context, c DeviceConnection, operation, handle string, paths []string) ([]FileReservation, error) {
	root, err := r.reservationRoot(ctx, c)
	if err != nil {
		return nil, err
	}
	unlock, err := r.lockWorktree(root)
	if err != nil {
		return nil, err
	}
	defer unlock()
	physicalRoot := root
	root = norm.NFC.String(cases.Fold().String(root))
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	expiry := now + (10 * time.Minute).Milliseconds()
	holder := c.Profile + "/" + c.HomeID + "/" + c.ProjectID + "/" + c.PrincipalID
	var out = []FileReservation{}
	switch operation {
	case "reserve":
		if len(paths) == 0 || len(paths) > 100 {
			return nil, errors.New("reserve requires 1 to 100 paths")
		}
		handle, err = randomID("lock_")
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, path := range paths {
			_, key, err := reservationPath(physicalRoot, path)
			if err != nil {
				return nil, err
			}
			if seen[key] {
				return nil, fmt.Errorf("duplicate path: %s", path)
			}
			seen[key] = true
			var existing FileReservation
			err = tx.QueryRowContext(ctx, `SELECT holder,expires_at FROM file_reservations WHERE root=? AND path_key=? AND expires_at>?`, root, key, now).Scan(&existing.Holder, &existing.ExpiresAt)
			if err == nil {
				return nil, fmt.Errorf("%s is reserved by %s until %s", path, existing.Holder, time.UnixMilli(existing.ExpiresAt).UTC().Format(time.RFC3339))
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO file_reservations VALUES(?,?,?,?,?,?)`, root, key, path, holder, handle, expiry); err != nil {
				return nil, err
			}
			out = append(out, FileReservation{path, holder, handle, expiry})
		}
	case "renew", "release":
		var result sql.Result
		if operation == "renew" {
			result, err = tx.ExecContext(ctx, `UPDATE file_reservations SET expires_at=? WHERE root=? AND holder=? AND handle=? AND expires_at>?`, expiry, root, holder, handle, now)
		} else {
			result, err = tx.ExecContext(ctx, `DELETE FROM file_reservations WHERE root=? AND holder=? AND handle=? AND expires_at>?`, root, holder, handle, now)
		}
		if err != nil {
			return nil, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, errors.New("no active reservation for this handle and identity")
		}
	case "list":
		rows, err := tx.QueryContext(ctx, `SELECT path,holder,CASE WHEN holder=? THEN handle ELSE '' END,expires_at FROM file_reservations WHERE root=? AND expires_at>? ORDER BY path_key`, holder, root, now)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var entry FileReservation
			if err := rows.Scan(&entry.Path, &entry.Holder, &entry.Handle, &entry.ExpiresAt); err != nil {
				return nil, err
			}
			out = append(out, entry)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("unknown file reservation operation")
	}
	return out, tx.Commit()
}

// GuardedFileEdit checks the lease and expected contents under the worktree lock.
func (r *DeviceRegistry) GuardedFileEdit(ctx context.Context, c DeviceConnection, handle, path, expected string, remove bool, content []byte) error {
	root, err := r.reservationRoot(ctx, c)
	if err != nil {
		return err
	}
	unlock, err := r.lockWorktree(root)
	if err != nil {
		return err
	}
	defer unlock()
	target, key, err := reservationPath(root, path)
	if err != nil {
		return err
	}
	var reservedPath string
	err = r.db.QueryRowContext(ctx, `SELECT path FROM file_reservations WHERE root=? AND path_key=? AND holder=? AND handle=? AND expires_at>?`, norm.NFC.String(cases.Fold().String(root)), key, c.Profile+"/"+c.HomeID+"/"+c.ProjectID+"/"+c.PrincipalID, handle, time.Now().UnixMilli()).Scan(&reservedPath)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("no active reservation for this path and identity")
	}
	if err != nil {
		return err
	}
	if reservedPath != path {
		return errors.New("use the path spelling recorded in the reservation")
	}
	mode := os.FileMode(0644)
	info, err := os.Lstat(target)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if expected != "absent" || remove {
			return ErrConflict
		}
	case err != nil:
		return err
	default:
		if expected == "absent" {
			return ErrConflict
		}
		mode = info.Mode().Perm()
		old, err := os.Open(target)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, readErr := io.Copy(hash, old)
		if err := errors.Join(readErr, old.Close()); err != nil {
			return err
		}
		if fmt.Sprintf("%x", hash.Sum(nil)) != expected {
			return ErrConflict
		}
	}
	if remove {
		return os.Remove(target)
	}
	file, err := os.CreateTemp(filepath.Dir(target), ".gossip-edit-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(content)
	if err := errors.Join(writeErr, file.Chmod(mode), file.Sync(), file.Close()); err != nil {
		return err
	}
	return replaceGuardedFile(file.Name(), target)
}
