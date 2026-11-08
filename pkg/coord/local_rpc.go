package coord

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// validateLocalSocketDirectory requires a private, non-symlink parent directory.
func validateLocalSocketDirectory(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("socket path must be absolute")
	}
	parent := filepath.Clean(filepath.Dir(path))
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return err
	}
	if resolved != parent {
		return errors.New("socket path must not contain symlinked directories")
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("socket parent must be a private directory (mode 0700 or stricter)")
	}
	return nil
}

// NewLocalRemote connects to a daemon using the same project-scoped API as TLS.
func NewLocalRemote(path string) (*Remote, error) {
	if err := validateLocalSocketDirectory(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("expected a private Unix socket")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", path)
	}}
	return &Remote{local: true, endpoint: "http://unix/v1/call", client: &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// ServeLocal exposes project-authenticated RPC exclusively through a private
// Unix socket. It does not remove an existing socket or file on startup.
func ServeLocal(ctx context.Context, s *Store, path string) error {
	if err := validateLocalSocketDirectory(path); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return errors.New("socket path already exists; inspect it before removal")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if u, ok := l.(*net.UnixListener); ok {
		u.SetUnlinkOnClose(false)
	}
	defer l.Close()
	if err = os.Chmod(path, 0600); err != nil {
		os.Remove(path)
		return err
	}
	original, err := os.Lstat(path)
	if err != nil {
		return err
	}
	defer func() {
		if current, e := os.Lstat(path); e == nil && os.SameFile(original, current) {
			_ = os.Remove(path)
		}
	}()
	srv := &http.Server{Handler: rpcHandler(s, false, nil, false, ""), ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(l) }()
	select {
	case err = <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("local daemon: %w", err)
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = srv.Shutdown(shutdown)
		if err != nil {
			_ = srv.Close()
		}
		serveErr := <-done
		if err != nil {
			return err
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return nil
	}
}
