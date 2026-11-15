package coord

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

type EnrollmentBundle struct {
	Invitation RemoteInvitation `json:"invitation"`
	Endpoint   string           `json:"endpoint"`
	ServerCA   string           `json:"server_ca"`
	HomeID     string           `json:"home_id"`
}

type enrollmentDevice struct {
	Key        []byte
	CSR        []byte
	Credential Credential
	Bundle     EnrollmentBundle
}

func (r *DeviceRegistry) AcceptEnrollment(ctx context.Context, profile string, bundle EnrollmentBundle, code string) (RemoteInvitation, error) {
	inv := bundle.Invitation
	if err := validateEnrollmentHome(bundle.Endpoint, bundle.ServerCA); err != nil {
		return inv, err
	}
	if (inv.ExpiresAt != 0 && inv.ExpiresAt <= time.Now().UnixMilli()) || inv.ID == "" || bundle.HomeID == "" || len(code) != 64 {
		return inv, errors.New("valid invitation and pairing code required")
	}
	fingerprint := sha256.Sum256([]byte(profile + "\x00" + bundle.HomeID + "\x00" + inv.Project + "\x00" + inv.ID))
	keyID := fmt.Sprintf("%x", fingerprint)
	device, err := r.loadEnrollmentDevice(ctx, keyID, bundle)
	if err != nil {
		return inv, err
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(bundle.ServerCA))
	config := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}
	transport := &http.Transport{TLSClientConfig: config}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	input := enrollmentRequest{Project: inv.Project, ID: inv.ID, Code: code, CSR: device.CSR}
	var accepted RemoteInvitation
	if err = enrollmentCall(ctx, client, bundle.Endpoint, "accept", input, &accepted); err != nil {
		return inv, err
	}
	if accepted.ID != inv.ID || accepted.Project != inv.Project || accepted.Fingerprint != deviceFingerprint(device.CSR) {
		return inv, ErrConflict
	}
	if accepted.State == "pending" {
		return accepted, nil
	}
	if accepted.State != "approved" && accepted.State != "enrolled" {
		return inv, ErrConflict
	}
	var issued struct {
		Certificate []byte `json:"certificate"`
	}
	if err = enrollmentCall(ctx, client, bundle.Endpoint, "certificate", input, &issued); err != nil {
		return inv, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: issued.Certificate})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: device.Key})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return inv, err
	}
	finalTransport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{pair}}}
	defer finalTransport.CloseIdleConnections()
	finalClient := &http.Client{Transport: finalTransport, Timeout: 10 * time.Second, CheckRedirect: client.CheckRedirect}
	if err = enrollmentCall(ctx, finalClient, bundle.Endpoint, "finalize", enrollmentRequest{Project: inv.Project, ID: inv.ID, Credential: device.Credential}, nil); err != nil {
		return inv, err
	}
	for suffix, data := range map[string][]byte{"_ca": []byte(bundle.ServerCA), "_cert": certPEM, "_key": keyPEM} {
		if err = r.putSecret(keyID+suffix, data); err != nil {
			return inv, err
		}
	}
	conn := DeviceConnection{Profile: profile, HomeID: bundle.HomeID, ProjectID: inv.Project, PrincipalID: device.Credential.PrincipalID, Name: accepted.Name, Endpoint: bundle.Endpoint, CAFile: filepath.Join(r.configDir, "secrets", keyID+"_ca"), CertFile: filepath.Join(r.configDir, "secrets", keyID+"_cert"), KeyFile: filepath.Join(r.configDir, "secrets", keyID+"_key")}
	remote, err := NewRemote(conn.Endpoint, conn.CAFile, conn.CertFile, conn.KeyFile, "")
	if err != nil {
		return inv, err
	}
	defer remote.CloseIdleConnections()
	frame, err := remote.SyncProject(ctx, conn.ProjectID, device.Credential.Token, 0, 1)
	if err != nil {
		return inv, err
	}
	if frame.HomeInstanceID != bundle.HomeID {
		return inv, ErrConflict
	}
	if err = r.Put(ctx, conn, device.Credential.Token); err != nil {
		return inv, err
	}
	accepted.State = "enrolled"
	return accepted, nil
}

func deviceFingerprint(der []byte) string {
	csr, err := x509.ParseCertificateRequest(der)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(csr.RawSubjectPublicKeyInfo))
}

func (r *DeviceRegistry) loadEnrollmentDevice(ctx context.Context, id string, bundle EnrollmentBundle) (enrollmentDevice, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return enrollmentDevice{}, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return enrollmentDevice{}, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		return enrollmentDevice{}, err
	}
	_, cred, err := PrepareCredential(bundle.Invitation.Project)
	if err != nil {
		return enrollmentDevice{}, err
	}
	payload, err := json.Marshal(enrollmentDevice{Key: der, CSR: csr, Credential: cred, Bundle: bundle})
	if err != nil {
		return enrollmentDevice{}, err
	}
	if _, err = r.db.ExecContext(ctx, `INSERT OR IGNORE INTO enrollment_devices VALUES(?,?)`, id, payload); err != nil {
		return enrollmentDevice{}, err
	}
	if err = r.db.QueryRowContext(ctx, `SELECT payload FROM enrollment_devices WHERE id=?`, id).Scan(&payload); err != nil {
		return enrollmentDevice{}, err
	}
	var device enrollmentDevice
	if err = json.Unmarshal(payload, &device); err != nil {
		return device, err
	}
	if device.Bundle.Endpoint != bundle.Endpoint || device.Bundle.ServerCA != bundle.ServerCA {
		return device, ErrConflict
	}
	return device, nil
}

func enrollmentCall(ctx context.Context, client *http.Client, endpoint, action string, input enrollmentRequest, out any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/v1/enrollment/"+action, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return errors.New("could not reach or verify the peer enrollment endpoint")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("peer rejected %s (HTTP %d)", action, response.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(out)
}
