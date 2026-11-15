package coord

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultSSHPort       = 22
	defaultSSHRemotePort = 19443
)

// SSHTunnelSpec describes an OpenSSH port-forwarding endpoint.
type SSHTunnelSpec struct {
	Target       string `json:"target"`
	SSHPort      int    `json:"ssh_port,omitempty"`
	RemoteHost   string `json:"remote_host,omitempty"`
	RemotePort   int    `json:"remote_port,omitempty"`
	IdentityFile string `json:"identity_file,omitempty"`
}

// FindSSHBinary returns the OpenSSH client binary path (honoring GOSSIP_SSH_BIN).
func FindSSHBinary() (string, error) {
	if override := strings.TrimSpace(os.Getenv("GOSSIP_SSH_BIN")); override != "" {
		info, err := os.Stat(override)
		if err == nil && !info.IsDir() {
			return override, nil
		}
		return "", fmt.Errorf("configured GOSSIP_SSH_BIN (%s) is unavailable", override)
	}
	path, err := exec.LookPath("ssh")
	if err != nil {
		return "", errors.New("OpenSSH `ssh` client binary not found in PATH")
	}
	return path, nil
}

// ValidateSSHTarget validates a user@host or host destination against SSH option injection.
func ValidateSSHTarget(target string) (string, error) {
	if strings.ContainsAny(target, "\r\n\x00") {
		return "", errors.New("SSH target contains invalid characters")
	}
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return "", errors.New("SSH target (e.g. user@host) is required")
	}
	if strings.HasPrefix(trimmed, "-") {
		return "", errors.New("SSH target cannot start with '-'")
	}
	if strings.ContainsAny(trimmed, " \t\r\n\"'`;|&$<>()\\") {
		return "", errors.New("SSH target contains invalid characters")
	}
	if len(trimmed) > 256 {
		return "", errors.New("SSH target is too long")
	}
	return trimmed, nil
}

// ValidateSSHIdentityFile validates an optional private key path passed via -i.
func ValidateSSHIdentityFile(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", nil
	}
	if strings.HasPrefix(trimmed, "-") || strings.ContainsAny(trimmed, "\r\n\x00") {
		return "", errors.New("invalid SSH identity file path")
	}
	info, err := os.Stat(trimmed)
	if err != nil {
		return "", fmt.Errorf("SSH identity file unavailable: %w", err)
	}
	if info.IsDir() {
		return "", errors.New("SSH identity path is a directory")
	}
	return trimmed, nil
}

func normalizeSSHTunnelSpec(spec SSHTunnelSpec, requireRemotePort bool) (SSHTunnelSpec, error) {
	target, err := ValidateSSHTarget(spec.Target)
	if err != nil {
		return spec, err
	}
	ident, err := ValidateSSHIdentityFile(spec.IdentityFile)
	if err != nil {
		return spec, err
	}
	sshPort := spec.SSHPort
	if sshPort == 0 {
		sshPort = defaultSSHPort
	}
	if sshPort < 1 || sshPort > 65535 {
		return spec, errors.New("SSH port must be between 1 and 65535")
	}
	remHost := strings.TrimSpace(spec.RemoteHost)
	if remHost == "" {
		remHost = "127.0.0.1"
	}
	if strings.HasPrefix(remHost, "-") || strings.ContainsAny(remHost, " \t\r\n\"'`;|&$<>()\\") {
		return spec, errors.New("SSH remote bind host contains invalid characters")
	}
	remPort := spec.RemotePort
	if remPort == 0 && requireRemotePort {
		remPort = defaultSSHRemotePort
	}
	if remPort < 1 || remPort > 65535 {
		return spec, errors.New("SSH forwarded port must be between 1 and 65535")
	}
	return SSHTunnelSpec{
		Target:       target,
		SSHPort:      sshPort,
		RemoteHost:   remHost,
		RemotePort:   remPort,
		IdentityFile: ident,
	}, nil
}

func buildBaseSSHForwardArgs(spec SSHTunnelSpec) []string {
	args := []string{
		"-N", "-T",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "BatchMode=yes",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-p", strconv.Itoa(spec.SSHPort),
	}
	if spec.IdentityFile != "" {
		args = append(args, "-i", spec.IdentityFile)
	}
	return args
}

// BuildReverseSSHArgs constructs hardened ssh -R arguments for host-side relay forwarding.
func BuildReverseSSHArgs(spec SSHTunnelSpec, localPort int) ([]string, error) {
	if localPort < 1 || localPort > 65535 {
		return nil, errors.New("local loopback port must be between 1 and 65535")
	}
	norm, err := normalizeSSHTunnelSpec(spec, true)
	if err != nil {
		return nil, err
	}
	args := buildBaseSSHForwardArgs(norm)
	args = append(args, "-R", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", norm.RemotePort, localPort), "--", norm.Target)
	return args, nil
}

// BuildLocalSSHArgs constructs hardened ssh -L arguments for client-side forwarding.
func BuildLocalSSHArgs(spec SSHTunnelSpec, localPort int) ([]string, error) {
	if localPort < 1 || localPort > 65535 {
		return nil, errors.New("local forward port must be between 1 and 65535")
	}
	norm, err := normalizeSSHTunnelSpec(spec, true)
	if err != nil {
		return nil, err
	}
	args := buildBaseSSHForwardArgs(norm)
	args = append(args, "-L", fmt.Sprintf("127.0.0.1:%d:%s:%d", localPort, norm.RemoteHost, norm.RemotePort), "--", norm.Target)
	return args, nil
}

// RewriteEndpointHostPort swaps the host:port of an https URL with a local forward address.
func RewriteEndpointHostPort(endpoint, newAddr string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || u.Scheme != "https" || newAddr == "" {
		return "", errors.New("invalid HTTPS endpoint for SSH tunnel")
	}
	u.Host = newAddr
	return u.String(), nil
}

// ParseEndpointPort extracts the integer port from an https URL (defaulting to 443).
func ParseEndpointPort(endpoint string) int {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return 0
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 && n <= 65535 {
			return n
		}
	}
	if u.Scheme == "https" {
		return 443
	}
	return 0
}

type clientSSHForward struct {
	localAddr string
	cancel    context.CancelFunc
	done      chan struct{}
	mu        sync.Mutex
	exited    bool
	exitErr   string
}

// SSHTunnelPool manages reusable client-side OpenSSH local forwards (`ssh -L`).
type SSHTunnelPool struct {
	mu       sync.Mutex
	forwards map[string]*clientSSHForward
	closed   bool
}

func NewSSHTunnelPool() *SSHTunnelPool {
	return &SSHTunnelPool{
		forwards: make(map[string]*clientSSHForward),
	}
}

func (p *SSHTunnelPool) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	list := make([]*clientSSHForward, 0, len(p.forwards))
	for k, f := range p.forwards {
		list = append(list, f)
		delete(p.forwards, k)
	}
	p.mu.Unlock()
	for _, f := range list {
		stopClientSSHForward(f)
	}
	return nil
}

// EnsureForward ensures an active `ssh -L` tunnel for spec and returns 127.0.0.1:<localPort>.
func (p *SSHTunnelPool) EnsureForward(ctx context.Context, spec SSHTunnelSpec) (string, error) {
	norm, err := normalizeSSHTunnelSpec(spec, true)
	if err != nil {
		return "", err
	}
	key := fmt.Sprintf("%s|%d|%s|%d|%s", norm.Target, norm.SSHPort, norm.RemoteHost, norm.RemotePort, norm.IdentityFile)

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return "", errors.New("SSH tunnel pool is closed")
	}
	existing := p.forwards[key]
	if existing != nil {
		existing.mu.Lock()
		dead := existing.exited
		existing.mu.Unlock()
		if !dead && isTCPAccepting(existing.localAddr) {
			addr := existing.localAddr
			p.mu.Unlock()
			return addr, nil
		}
		delete(p.forwards, key)
	}
	p.mu.Unlock()

	if existing != nil {
		stopClientSSHForward(existing)
	}

	sshBin, err := FindSSHBinary()
	if err != nil {
		return "", err
	}
	localPort, err := reserveFreeLoopbackPort()
	if err != nil {
		return "", fmt.Errorf("allocate local SSH forward port: %w", err)
	}
	localAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort))
	args, err := BuildLocalSSHArgs(norm, localPort)
	if err != nil {
		return "", err
	}

	procCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, sshBin, args...)
	var stderr tailBuffer
	stderr.max = 2048
	cmd.Stdout = &stderr
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return "", fmt.Errorf("start SSH tunnel to %s: %w", norm.Target, err)
	}

	fwd := &clientSSHForward{
		localAddr: localAddr,
		cancel:    cancel,
		done:      make(chan struct{}),
	}
	go func() {
		defer close(fwd.done)
		waitErr := cmd.Wait()
		msg := strings.TrimSpace(lastNonEmptyLine(stderr.String()))
		if msg == "" && waitErr != nil {
			msg = waitErr.Error()
		}
		fwd.mu.Lock()
		fwd.exited = true
		fwd.exitErr = msg
		fwd.mu.Unlock()
	}()

	waitDeadline := time.Now().Add(10 * time.Second)
	for {
		if isTCPAccepting(localAddr) {
			break
		}
		fwd.mu.Lock()
		exited, errText := fwd.exited, fwd.exitErr
		fwd.mu.Unlock()
		if exited {
			stopClientSSHForward(fwd)
			if errText == "" {
				errText = "SSH tunnel exited before opening local port"
			}
			return "", fmt.Errorf("SSH tunnel (%s): %s", norm.Target, errText)
		}
		if time.Now().After(waitDeadline) {
			stopClientSSHForward(fwd)
			return "", fmt.Errorf("timed out waiting for SSH tunnel to %s (port %d)", norm.Target, norm.RemotePort)
		}
		select {
		case <-ctx.Done():
			stopClientSSHForward(fwd)
			return "", ctx.Err()
		case <-time.After(75 * time.Millisecond):
		}
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		stopClientSSHForward(fwd)
		return "", errors.New("SSH tunnel pool is closed")
	}
	if prev := p.forwards[key]; prev != nil {
		p.mu.Unlock()
		stopClientSSHForward(fwd)
		return prev.localAddr, nil
	}
	p.forwards[key] = fwd
	p.mu.Unlock()
	return localAddr, nil
}

func stopClientSSHForward(fwd *clientSSHForward) {
	if fwd == nil {
		return
	}
	if fwd.cancel != nil {
		fwd.cancel()
	}
	if fwd.done != nil {
		select {
		case <-fwd.done:
		case <-time.After(2 * time.Second):
		}
	}
}

func reserveFreeLoopbackPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	addr := ln.Addr().(*net.TCPAddr)
	port := addr.Port
	_ = ln.Close()
	return port, nil
}

func isTCPAccepting(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 150*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}
