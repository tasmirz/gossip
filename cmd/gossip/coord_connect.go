package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/tasmirz/gossip/pkg/coord"
)

type coordConnectConfig struct {
	InviteFile      string
	Passphrase      string
	Endpoint        string
	SSHTarget       string
	SSHPort         int
	SSHRemotePort   int
	SSHIdentityFile string
	ProjectID       string
	InvitationID    string
	Code            string
	Profile         string
	AcceptNew       bool
	NoWait          bool
	Timeout         time.Duration
}

func parseCoordConnectArgs(args []string) (coordConnectConfig, error) {
	fs := flag.NewFlagSet("coord-connect", flag.ContinueOnError)
	var cfg coordConnectConfig
	var pinFlag string
	fs.StringVar(&cfg.InviteFile, "invite-file", "", "path to encrypted .gossip invitation file")
	fs.StringVar(&pinFlag, "pin", "", "6-digit PIN (or passphrase) for encrypted .gossip file")
	fs.StringVar(&cfg.Passphrase, "passphrase", "", "passphrase or PIN for encrypted .gossip file")
	fs.StringVar(&cfg.Endpoint, "endpoint", "", fmt.Sprintf("peer HTTPS endpoint (e.g. https://host:%d)", coord.DefaultPeerListenPort))
	fs.StringVar(&cfg.SSHTarget, "ssh", "", "optional OpenSSH target ([user@]host) for local -L forward")
	fs.IntVar(&cfg.SSHPort, "ssh-port", 22, "OpenSSH server port")
	fs.IntVar(&cfg.SSHRemotePort, "remote-port", coord.DefaultPeerListenPort, "remote peer port forwarded over SSH")
	fs.StringVar(&cfg.SSHIdentityFile, "identity", "", "optional OpenSSH identity file (-i)")
	fs.StringVar(&cfg.SSHIdentityFile, "i", "", "shorthand for --identity")
	fs.StringVar(&cfg.ProjectID, "project", "", "project ID (when connecting without --invite-file)")
	fs.StringVar(&cfg.InvitationID, "invite", "", "invitation ID (when connecting without --invite-file)")
	fs.StringVar(&cfg.Code, "code", "", "pairing secret code (when connecting without --invite-file)")
	fs.StringVar(&cfg.Profile, "profile", "default", "device profile")
	fs.BoolVar(&cfg.AcceptNew, "accept-new", false, "strict TOFU: trust an unseen host key automatically (still rejects changed keys)")
	fs.BoolVar(&cfg.NoWait, "no-wait", false, "submit the verification request and exit without polling for approval")
	fs.DurationVar(&cfg.Timeout, "timeout", 10*time.Minute, "maximum wait duration for manual owner approval")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	if strings.TrimSpace(cfg.InviteFile) == "" && fs.NArg() > 0 {
		cfg.InviteFile = strings.TrimSpace(fs.Arg(0))
	}
	if strings.TrimSpace(cfg.Passphrase) == "" && strings.TrimSpace(pinFlag) != "" {
		cfg.Passphrase = strings.TrimSpace(pinFlag)
	}
	return cfg, nil
}

func runCoordConnect(args []string) error {
	cfg, err := parseCoordConnectArgs(args)
	if err != nil {
		return err
	}
	registry, err := coord.OpenDeviceRegistry("")
	if err != nil {
		return err
	}
	defer registry.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}
	return executeCoordConnect(ctx, registry, cfg, os.Stdin, os.Stdout)
}

func executeCoordConnect(ctx context.Context, registry *coord.DeviceRegistry, cfg coordConnectConfig, in io.Reader, out io.Writer) error {
	if registry == nil {
		return errors.New("device registry is required")
	}
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}
	if out == nil {
		out = io.Discard
	}
	reader := bufio.NewReader(in)
	if cfg.Profile == "" {
		cfg.Profile = "default"
	}

	var bundle coord.EnrollmentBundle
	code := strings.TrimSpace(cfg.Code)
	sshTarget := strings.TrimSpace(cfg.SSHTarget)
	sshPort := cfg.SSHPort
	sshRemotePort := cfg.SSHRemotePort

	if strings.TrimSpace(cfg.InviteFile) != "" {
		raw, err := os.ReadFile(strings.TrimSpace(cfg.InviteFile))
		if err != nil {
			return err
		}
		if len(raw) > 65536 {
			return errors.New("encrypted invitation file exceeds 64 KiB limit")
		}
		pass := cfg.Passphrase
		if pass == "" {
			fmt.Fprint(out, "Invite PIN / passphrase: ")
			line, err := reader.ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			pass = strings.TrimSpace(line)
		}
		payload, err := coord.DecryptEnrollmentBundle(raw, pass)
		if err != nil {
			return err
		}
		bundle = coord.EnrollmentBundle{
			Invitation: payload.Invitation,
			Endpoint:   payload.Endpoint,
			ServerCA:   payload.ServerCA,
			HomeID:     payload.HomeID,
		}
		code = payload.Code
		if sshTarget == "" {
			sshTarget = payload.SSHTarget
		}
		if sshPort <= 0 {
			sshPort = payload.SSHPort
		}
		if sshRemotePort <= 0 {
			sshRemotePort = payload.SSHRemotePort
		}
	} else {
		endpoint := strings.TrimSpace(cfg.Endpoint)
		if endpoint == "" && sshTarget != "" {
			remPort := sshRemotePort
			if remPort <= 0 {
				remPort = coord.DefaultPeerListenPort
			}
			endpoint = fmt.Sprintf("https://127.0.0.1:%d", remPort)
		}
		if endpoint != "" && !strings.Contains(endpoint, "://") {
			endpoint = "https://" + endpoint
		}
		if endpoint == "" {
			return errors.New("--invite-file, --endpoint, or --ssh is required")
		}
		if strings.TrimSpace(cfg.ProjectID) == "" || strings.TrimSpace(cfg.InvitationID) == "" || len(code) != 64 {
			return errors.New("--project, --invite, and 64-char --code are required without --invite-file")
		}
		bundle = coord.EnrollmentBundle{
			Endpoint: endpoint,
			Invitation: coord.RemoteInvitation{
				ID:      strings.TrimSpace(cfg.InvitationID),
				Project: strings.TrimSpace(cfg.ProjectID),
			},
		}
	}

	if bundle.Endpoint != "" && !strings.Contains(bundle.Endpoint, "://") {
		bundle.Endpoint = "https://" + bundle.Endpoint
	}
	dialEndpoint := bundle.Endpoint
	if sshTarget != "" {
		var err error
		dialEndpoint, err = registry.EnsureSSHClientForward(ctx, sshTarget, bundle.Endpoint, sshPort, sshRemotePort, cfg.SSHIdentityFile)
		if err != nil {
			return err
		}
	}
	if strings.TrimSpace(bundle.ServerCA) == "" {
		probe, err := coord.ProbePeerEndpoint(ctx, dialEndpoint)
		if err != nil {
			return err
		}
		bundle.ServerCA = probe.ServerCA
	}

	hostKey := coord.KnownHostIdentity(bundle.Endpoint, sshTarget, sshRemotePort)
	check, err := registry.VerifyOrRecordKnownHost(ctx, hostKey, bundle.HomeID, bundle.ServerCA, false)
	if err != nil {
		if errors.Is(err, coord.ErrHostKeyChanged) {
			fmt.Fprintf(out, "WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!\nHost: %s\nExpected: %s\nReceived: %s\n", hostKey, check.ExpectedFingerprint, check.Fingerprint)
		}
		return err
	}
	if check.Status == "untrusted" {
		fmt.Fprintf(out, "The authenticity of host '%s' can't be established.\n", hostKey)
		fmt.Fprintf(out, "Peer CA key fingerprint is %s.\n", check.Fingerprint)
		if !cfg.AcceptNew {
			fmt.Fprint(out, "Are you sure you want to continue connecting (yes/no)? ")
			answer, readErr := reader.ReadString('\n')
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return readErr
			}
			answer = strings.ToLower(strings.TrimSpace(answer))
			if answer != "yes" && answer != "y" {
				return errors.New("host key verification failed")
			}
		}
		if err = registry.TrustKnownHost(ctx, hostKey, bundle.HomeID, check.Fingerprint, bundle.ServerCA); err != nil {
			return err
		}
		fmt.Fprintf(out, "Warning: Permanently added '%s' to known_hosts.\n", hostKey)
	}

	opts := coord.EnrollmentAcceptOptions{
		Bundle:          bundle,
		Code:            code,
		SSHTarget:       sshTarget,
		SSHPort:         sshPort,
		SSHRemotePort:   sshRemotePort,
		SSHIdentityFile: cfg.SSHIdentityFile,
		TrustHostKey:    false,
	}
	accepted, err := registry.AcceptEnrollmentWithOptions(ctx, cfg.Profile, opts)
	if err != nil {
		return err
	}
	hostFP := coord.HostCertSSHFingerprint(bundle.ServerCA)
	fmt.Fprintf(out, "Invitation state: %s\n", accepted.State)
	fmt.Fprintf(out, "Verification code: %s\n", accepted.VerificationCode)
	fmt.Fprintf(out, "Device key fingerprint: %s\n", accepted.SSHFingerprint)
	fmt.Fprintf(out, "Host key fingerprint: %s\n", hostFP)
	if accepted.State == "enrolled" {
		fmt.Fprintf(out, "Enrolled project %s (%s).\n", accepted.Project, accepted.Name)
		return nil
	}
	if cfg.NoWait {
		return nil
	}
	fmt.Fprintln(out, "Waiting for the project owner on the host to verify this code and click Accept...")

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			accepted, err = registry.AcceptEnrollmentWithOptions(ctx, cfg.Profile, opts)
			if err != nil {
				return err
			}
			if accepted.State == "enrolled" {
				fmt.Fprintf(out, "Enrolled project %s (%s).\n", accepted.Project, accepted.Name)
				return nil
			}
		}
	}
}
