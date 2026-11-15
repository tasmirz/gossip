package coord

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// ErrHostKeyChanged is returned when a known peer endpoint presents a different TLS CA key fingerprint.
var ErrHostKeyChanged = errors.New("WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED")

// KnownHost records a pinned host key fingerprint (SSH known_hosts style TOFU).
type KnownHost struct {
	HostKey     string `json:"host_key"`
	HomeID      string `json:"home_id,omitempty"`
	Fingerprint string `json:"fingerprint"`
	ServerCA    string `json:"server_ca"`
	FirstSeen   int64  `json:"first_seen"`
	LastSeen    int64  `json:"last_seen"`
}

// KnownHostCheck describes the result of checking a host key against known_hosts.
type KnownHostCheck struct {
	Status              string `json:"status"` // "trusted", "untrusted", "changed"
	HostKey             string `json:"host_key"`
	HomeID              string `json:"home_id,omitempty"`
	Fingerprint         string `json:"fingerprint"`
	ExpectedFingerprint string `json:"expected_fingerprint,omitempty"`
	ServerCA            string `json:"server_ca,omitempty"`
}

// PeerProbeResult holds the verified server certificate chain extracted from a peer endpoint.
type PeerProbeResult struct {
	Endpoint        string `json:"endpoint"`
	ServerCA        string `json:"server_ca"`
	HostFingerprint string `json:"host_fingerprint"`
}

// NormalizeKnownHostKey builds a canonical key for known_hosts (endpoint URL or ssh://target:port).
func NormalizeKnownHostKey(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if u, err := url.Parse(s); err == nil && u.Scheme != "" && u.Host != "" {
		return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
	}
	return strings.ToLower(s)
}

// ProbePeerEndpoint dials endpoint over TLS 1.3 and extracts its root CA certificate and SHA256 fingerprint.
func ProbePeerEndpoint(ctx context.Context, endpoint string) (PeerProbeResult, error) {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return PeerProbeResult{}, errors.New("enter a valid https:// peer host endpoint")
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	addr := net.JoinHostPort(u.Hostname(), port)
	dialer := &net.Dialer{Timeout: 6 * time.Second}
	rawConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return PeerProbeResult{}, fmt.Errorf("connect to %s: %w", addr, err)
	}
	tlsConn := tls.Client(rawConn, &tls.Config{
		MinVersion:         tls.VersionTLS13,
		ServerName:         u.Hostname(),
		InsecureSkipVerify: true, // Verified explicitly against the presented root chain below (TOFU).
	})
	defer tlsConn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = tlsConn.SetDeadline(deadline)
	} else {
		_ = tlsConn.SetDeadline(time.Now().Add(6 * time.Second))
	}
	if err = tlsConn.HandshakeContext(ctx); err != nil {
		return PeerProbeResult{}, fmt.Errorf("TLS handshake with %s: %w", addr, err)
	}
	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return PeerProbeResult{}, errors.New("peer host did not present a server certificate")
	}
	leaf := state.PeerCertificates[0]
	root := state.PeerCertificates[len(state.PeerCertificates)-1]
	roots := x509.NewCertPool()
	roots.AddCert(root)
	intermediates := x509.NewCertPool()
	for i := 1; i < len(state.PeerCertificates)-1; i++ {
		intermediates.AddCert(state.PeerCertificates[i])
	}
	if _, err = leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		DNSName:       u.Hostname(),
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		return PeerProbeResult{}, fmt.Errorf("peer server certificate validation failed: %w", err)
	}
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root.Raw}))
	return PeerProbeResult{
		Endpoint:        NormalizeKnownHostKey(endpoint),
		ServerCA:        caPEM,
		HostFingerprint: SSHFingerprintSHA256(root.Raw),
	}, nil
}

// VerifyOrRecordKnownHost checks hostKey against SQLite known_hosts.
// If trustIfNew is true and hostKey is unseen, it records the key in known_hosts (SSH TOFU).
// If hostKey was seen previously with a different fingerprint, it returns ErrHostKeyChanged.
func (r *DeviceRegistry) VerifyOrRecordKnownHost(ctx context.Context, hostKey, homeID, serverCA string, trustIfNew bool) (KnownHostCheck, error) {
	normKey := NormalizeKnownHostKey(hostKey)
	fp := HostCertSSHFingerprint(serverCA)
	if normKey == "" || fp == "" {
		return KnownHostCheck{}, errors.New("host key and server CA certificate required")
	}
	check := KnownHostCheck{
		HostKey:     normKey,
		HomeID:      strings.TrimSpace(homeID),
		Fingerprint: fp,
		ServerCA:    serverCA,
	}
	var existingFP, existingHome string
	err := r.db.QueryRowContext(ctx, `SELECT fingerprint,home_id FROM known_hosts WHERE host_key=?`, normKey).Scan(&existingFP, &existingHome)
	if errors.Is(err, sql.ErrNoRows) {
		if !trustIfNew {
			check.Status = "untrusted"
			return check, nil
		}
		if err = r.TrustKnownHost(ctx, normKey, check.HomeID, fp, serverCA); err != nil {
			return check, err
		}
		check.Status = "trusted"
		return check, nil
	}
	if err != nil {
		return check, err
	}
	if existingFP != fp {
		check.Status = "changed"
		check.ExpectedFingerprint = existingFP
		return check, fmt.Errorf("%w: host %s presented %s, expected %s", ErrHostKeyChanged, normKey, fp, existingFP)
	}
	now := time.Now().UnixMilli()
	if check.HomeID == "" {
		check.HomeID = existingHome
	}
	_, _ = r.db.ExecContext(ctx, `UPDATE known_hosts SET last_seen=?, home_id=CASE WHEN ?<>'' THEN ? ELSE home_id END WHERE host_key=?`, now, check.HomeID, check.HomeID, normKey)
	check.Status = "trusted"
	return check, nil
}

// TrustKnownHost explicitly saves or updates an entry in known_hosts.
func (r *DeviceRegistry) TrustKnownHost(ctx context.Context, hostKey, homeID, fingerprint, serverCA string) error {
	normKey := NormalizeKnownHostKey(hostKey)
	fp := strings.TrimSpace(fingerprint)
	if fp == "" {
		fp = HostCertSSHFingerprint(serverCA)
	}
	if normKey == "" || fp == "" || strings.TrimSpace(serverCA) == "" {
		return errors.New("host key, fingerprint, and server CA are required")
	}
	now := time.Now().UnixMilli()
	_, err := r.db.ExecContext(ctx, `INSERT INTO known_hosts(host_key,home_id,fingerprint,server_ca,first_seen,last_seen)
		VALUES(?,?,?,?,?,?) ON CONFLICT(host_key) DO UPDATE SET
		home_id=CASE WHEN excluded.home_id<>'' THEN excluded.home_id ELSE known_hosts.home_id END,
		fingerprint=excluded.fingerprint, server_ca=excluded.server_ca, last_seen=excluded.last_seen`,
		normKey, strings.TrimSpace(homeID), fp, serverCA, now, now)
	return err
}

// ListKnownHosts returns all recorded peer host keys sorted most-recently-seen first.
func (r *DeviceRegistry) ListKnownHosts(ctx context.Context) ([]KnownHost, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT host_key,home_id,fingerprint,server_ca,first_seen,last_seen FROM known_hosts ORDER BY last_seen DESC, host_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []KnownHost{}
	for rows.Next() {
		var kh KnownHost
		if err := rows.Scan(&kh.HostKey, &kh.HomeID, &kh.Fingerprint, &kh.ServerCA, &kh.FirstSeen, &kh.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, kh)
	}
	return out, rows.Err()
}

// RemoveKnownHost deletes a host entry from known_hosts so a rotated key can be re-verified.
func (r *DeviceRegistry) RemoveKnownHost(ctx context.Context, hostKey string) error {
	normKey := NormalizeKnownHostKey(hostKey)
	if normKey == "" {
		return errors.New("host_key is required")
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM known_hosts WHERE host_key=?`, normKey)
	return err
}
