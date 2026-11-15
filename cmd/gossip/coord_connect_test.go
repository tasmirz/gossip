package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tasmirz/gossip/pkg/coord"
)

func TestCoordConnectTOFUAndManualApprovalPolling(t *testing.T) {
	ctx := context.Background()
	hostStore, err := coord.Open(filepath.Join(t.TempDir(), "host.db"))
	if err != nil {
		t.Fatalf("coord.Open: %v", err)
	}
	t.Cleanup(func() { _ = hostStore.Close() })

	proj, ownerCred, err := hostStore.CreateProject(ctx, "CLI Connect Project", "Owner")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	pm, err := coord.NewPeerRuntime(hostStore, t.TempDir())
	if err != nil {
		t.Fatalf("NewPeerRuntime: %v", err)
	}
	t.Cleanup(func() { _ = pm.Close() })

	trueVal := true
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate free loopback port: %v", err)
	}
	localPort := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	status, err := pm.Configure(ctx, coord.PeerAccessUpdate{
		ProjectID:    proj.ID,
		SSHEnabled:   &trueVal,
		SSHLocalPort: &localPort,
	})
	if err != nil {
		t.Fatalf("pm.Configure SSH loopback: %v", err)
	}

	invite, code, err := hostStore.CreateRemoteInvitationWithOptions(ctx, proj.ID, ownerCred.Token, "CLI-Peer", "worker", false)
	if err != nil {
		t.Fatalf("CreateRemoteInvitationWithOptions: %v", err)
	}
	pass, err := coord.GenerateInvitePassphrase()
	if err != nil {
		t.Fatalf("GenerateInvitePassphrase: %v", err)
	}
	sealed, err := coord.EncryptEnrollmentBundle(coord.EncryptedInvitePayload{
		Version:         1,
		ProjectName:     proj.Name,
		Mode:            "lan",
		Invitation:      invite,
		Endpoint:        status.SSHEndpoint,
		ServerCA:        status.ServerCA,
		HostFingerprint: status.HostFingerprint,
		HomeID:          hostStore.InstanceID(),
		Code:            code,
	}, pass)
	if err != nil {
		t.Fatalf("EncryptEnrollmentBundle: %v", err)
	}
	invitePath := filepath.Join(t.TempDir(), "cli.gossip")
	if err := os.WriteFile(invitePath, sealed, 0600); err != nil {
		t.Fatalf("WriteFile(.gossip): %v", err)
	}

	peerReg, err := coord.OpenDeviceRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("OpenDeviceRegistry: %v", err)
	}
	t.Cleanup(func() { _ = peerReg.Close() })

	// Answering "no" to TOFU prompt aborts cleanly without trusting the host.
	var rejectOut bytes.Buffer
	rejectErr := executeCoordConnect(ctx, peerReg, coordConnectConfig{
		InviteFile: invitePath,
		Passphrase: pass,
		Timeout:    5 * time.Second,
	}, strings.NewReader("no\n"), &rejectOut)
	if rejectErr == nil || !strings.Contains(rejectOut.String(), "The authenticity of host") {
		t.Fatalf("expected TOFU prompt rejection on 'no', got err=%v out=%s", rejectErr, rejectOut.String())
	}

	// Approve on host shortly after CLI submits pending verification request.
	approveCtx, cancelApprove := context.WithTimeout(ctx, 20*time.Second)
	defer cancelApprove()
	go func() {
		for approveCtx.Err() == nil {
			invs, err := hostStore.RemoteInvitations(approveCtx, proj.ID, ownerCred.Token)
			if err == nil {
				for _, hi := range invs {
					if hi.ID == invite.ID && hi.State == "pending" {
						_ = hostStore.DecideRemoteInvitation(approveCtx, proj.ID, ownerCred.Token, invite.ID, "approved")
						return
					}
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()

	var okOut bytes.Buffer
	err = executeCoordConnect(ctx, peerReg, coordConnectConfig{
		InviteFile: invitePath,
		Passphrase: pass,
		AcceptNew:  true,
		Timeout:    25 * time.Second,
	}, strings.NewReader(""), &okOut)
	if err != nil {
		t.Fatalf("executeCoordConnect with --accept-new failed: %v\nout:\n%s", err, okOut.String())
	}
	outStr := okOut.String()
	for _, want := range []string{"Verification code:", "Device key fingerprint: SHA256:", "Enrolled project " + proj.ID} {
		if !strings.Contains(outStr, want) {
			t.Fatalf("missing %q in coord-connect output:\n%s", want, outStr)
		}
	}

	// Ensure host known_hosts persisted the trusted fingerprint as valid JSON listable entry.
	hosts, err := peerReg.ListKnownHosts(ctx)
	if err != nil || len(hosts) != 1 || hosts[0].Fingerprint != status.HostFingerprint {
		raw, _ := json.Marshal(hosts)
		t.Fatalf("unexpected known_hosts entries: err=%v hosts=%s", err, raw)
	}
}

func TestCoordConnectPositionalFileAndPINFlag(t *testing.T) {
	cfg, err := parseCoordConnectArgs([]string{"--pin", "482917", "--accept-new", "/tmp/project-invite.gossip"})
	if err != nil {
		t.Fatalf("parseCoordConnectArgs: %v", err)
	}
	if cfg.InviteFile != "/tmp/project-invite.gossip" {
		t.Fatalf("expected positional InviteFile, got %q", cfg.InviteFile)
	}
	if cfg.Passphrase != "482917" {
		t.Fatalf("expected Passphrase from --pin, got %q", cfg.Passphrase)
	}
	if !cfg.AcceptNew {
		t.Fatal("expected AcceptNew=true")
	}
}
