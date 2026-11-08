package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/tasmirz/gossip/pkg/coord"
)

type preparedPairing struct {
	ProjectID           string `json:"project_id"`
	InvitationID        string `json:"invitation_id"`
	Token               string `json:"token"`
	ExpectedFingerprint string `json:"expected_fingerprint,omitempty"`
	ExpiresAt           int64  `json:"expires_at"`
}

func readPreparedPairing(path string) (preparedPairing, error) {
	var pairing preparedPairing
	if path == "" {
		return pairing, errors.New("--invite-file is required")
	}
	f, err := os.Open(path)
	if err != nil {
		return pairing, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return pairing, err
	}
	if err = validatePrivateCredentialInfo(info); err != nil {
		return pairing, err
	}
	if info.Size() > 8192 {
		return pairing, errors.New("invitation file is too large")
	}
	data, err := io.ReadAll(io.LimitReader(f, 8193))
	if err != nil {
		return pairing, err
	}
	if len(data) > 8192 {
		return pairing, errors.New("invitation file is too large")
	}
	if err = json.Unmarshal(data, &pairing); err != nil {
		return pairing, err
	}
	if pairing.ProjectID == "" || pairing.InvitationID == "" || pairing.Token == "" || pairing.ExpiresAt == 0 {
		return preparedPairing{}, errors.New("invalid invitation file")
	}
	return pairing, nil
}

func writePreparedPairing(path string, pairing preparedPairing) error {
	f, err := credentialOutput(path)
	if err != nil {
		return err
	}
	err = json.NewEncoder(f).Encode(pairing)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(path)
		if err != nil {
			return err
		}
		return closeErr
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("invitation retained at %s; directory sync failed: %w", path, err)
	}
	syncErr := dir.Sync()
	closeErr = dir.Close()
	if syncErr != nil {
		return fmt.Errorf("invitation retained at %s; directory sync failed: %w", path, syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("invitation retained at %s; directory close failed: %w", path, closeErr)
	}
	return nil
}

func runCoordPair(args []string) error {
	fs := flag.NewFlagSet("coord-pair", flag.ContinueOnError)
	action := fs.String("action", "create", "create, redeem, list, or revoke")
	db := fs.String("db", coordDefaultDB(), "local coordination database")
	socket := fs.String("socket", "", "private local coordination daemon socket")
	remoteURL := fs.String("remote", "", "HTTPS home endpoint")
	tlsCA := fs.String("tls-ca", "", "trusted home server CA certificate")
	tlsCert := fs.String("tls-cert", "", "client certificate")
	tlsKey := fs.String("tls-key", "", "client private key")
	tlsServerName := fs.String("tls-server-name", "", "optional server certificate name")
	ownerFile := fs.String("owner-credential", "", "owner credential for create, list, or revoke")
	inviteFile := fs.String("invite-file", "", "private invitation file for create or redeem")
	resume := fs.Bool("resume", false, "retry create with an existing prepared invitation file")
	expiresIn := fs.Duration("expires-in", 10*time.Minute, "invitation lifetime, at most 24h")
	fingerprint := fs.String("fingerprint", "", "optional expected client certificate SHA-256 fingerprint")
	certificate := fs.String("certificate", "", "optional expected client certificate PEM")
	id := fs.String("id", "", "invitation ID for revoke")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *action != "create" && *action != "redeem" && *action != "list" && *action != "revoke" {
		return errors.New("action must be create, redeem, list, or revoke")
	}
	if *socket != "" && *remoteURL != "" {
		return errors.New("choose either --socket or --remote")
	}
	if *action == "redeem" {
		if *remoteURL == "" || *socket != "" || *ownerFile != "" {
			return errors.New("redeem requires --remote and a client certificate, without an owner credential")
		}
		pairing, err := readPreparedPairing(*inviteFile)
		if err != nil {
			return err
		}
		remote, err := coord.NewRemote(*remoteURL, *tlsCA, *tlsCert, *tlsKey, *tlsServerName)
		if err != nil {
			return err
		}
		grant, err := remote.RedeemCertificateInvitation(context.Background(), pairing.ProjectID, pairing.InvitationID, pairing.Token)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(grant)
	}
	owner, err := readCoordCredential(*ownerFile)
	if err != nil {
		return err
	}
	var backend interface {
		AddPreparedCertificateInvitation(context.Context, string, string, string, string, string, int64) (coord.CertificateInvitation, error)
		ListCertificateInvitations(context.Context, string, string) ([]coord.CertificateInvitation, error)
		RevokeCertificateInvitation(context.Context, string, string, string) error
	}
	if *socket != "" {
		backend, err = coord.NewLocalRemote(*socket)
	} else if *remoteURL != "" {
		backend, err = coord.NewRemote(*remoteURL, *tlsCA, *tlsCert, *tlsKey, *tlsServerName)
	} else {
		var store *coord.Store
		store, err = coord.Open(*db)
		if err == nil {
			defer store.Close()
			backend = store
		}
	}
	if err != nil {
		return err
	}
	ctx := context.Background()
	switch *action {
	case "list":
		invitations, err := backend.ListCertificateInvitations(ctx, owner.ProjectID, owner.Token)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(invitations)
	case "revoke":
		if *id == "" {
			return errors.New("revoke requires --id")
		}
		if err := backend.RevokeCertificateInvitation(ctx, owner.ProjectID, owner.Token, *id); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "Certificate invitation revoked")
		return nil
	case "create":
		if *inviteFile == "" {
			return errors.New("create requires --invite-file")
		}
		if *fingerprint != "" && *certificate != "" {
			return errors.New("provide either --fingerprint or --certificate")
		}
		if *expiresIn <= 0 || *expiresIn > 24*time.Hour {
			return errors.New("--expires-in must be positive and no more than 24h")
		}
		expected := *fingerprint
		if *certificate != "" {
			expected, err = certificateFingerprint(*certificate)
			if err != nil {
				return err
			}
		}
		if expected != "" {
			expected, err = coord.NormalizeCertificateFingerprint(expected)
			if err != nil {
				return err
			}
		}
		var pairing preparedPairing
		if *resume {
			pairing, err = readPreparedPairing(*inviteFile)
			if err != nil {
				return err
			}
			if pairing.ProjectID != owner.ProjectID || expected != "" && expected != pairing.ExpectedFingerprint {
				return errors.New("prepared invitation does not match this project or certificate")
			}
		} else {
			inviteID, token, err := coord.PrepareCertificateInvitation()
			if err != nil {
				return err
			}
			pairing = preparedPairing{ProjectID: owner.ProjectID, InvitationID: inviteID, Token: token, ExpectedFingerprint: expected, ExpiresAt: time.Now().Add(*expiresIn).UnixMilli()}
			if err = writePreparedPairing(*inviteFile, pairing); err != nil {
				return err
			}
		}
		invite, err := backend.AddPreparedCertificateInvitation(ctx, owner.ProjectID, owner.Token, pairing.InvitationID, pairing.Token, pairing.ExpectedFingerprint, pairing.ExpiresAt)
		if err != nil {
			return fmt.Errorf("invitation retained at %s; retry with --resume after checking the error: %w", *inviteFile, err)
		}
		return json.NewEncoder(os.Stdout).Encode(invite)
	}
	return nil
}
