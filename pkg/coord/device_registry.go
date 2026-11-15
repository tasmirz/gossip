package coord

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DeviceConnection records routing metadata for one project membership on this
// device. Secrets are stored separately in owner-only files so registry reads
// and project discovery never reveal credentials.
type DeviceConnection struct {
	Profile         string `json:"profile"`
	HomeID          string `json:"home_id"`
	ProjectID       string `json:"project_id"`
	PrincipalID     string `json:"principal_id"`
	Name            string `json:"name"`
	Endpoint        string `json:"endpoint,omitempty"`
	CAFile          string `json:"ca_file,omitempty"`
	CertFile        string `json:"cert_file,omitempty"`
	KeyFile         string `json:"key_file,omitempty"`
	Checkout        string `json:"checkout,omitempty"`
	HostFingerprint string `json:"host_fingerprint,omitempty"`
	SSHTarget       string `json:"ssh_target,omitempty"`
	SSHPort         int    `json:"ssh_port,omitempty"`
	SSHRemotePort   int    `json:"ssh_remote_port,omitempty"`
	SSHIdentityFile string `json:"ssh_identity_file,omitempty"`
	SecretID        string `json:"-"`
}

type DeviceRegistry struct {
	db        *sql.DB
	configDir string
	sshPool   *SSHTunnelPool
}

func DefaultGossipConfigDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "gossip"), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "gossip"), nil
}

func OpenDeviceRegistry(dir string) (*DeviceRegistry, error) {
	if dir == "" {
		var err error
		dir, err = DefaultGossipConfigDir()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dir, 0700); err != nil {
			return nil, err
		}
	}
	path := filepath.Join(dir, "registry.db")
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS device_connections (
		profile TEXT NOT NULL, home_id TEXT NOT NULL, project_id TEXT NOT NULL,
		principal_id TEXT NOT NULL, name TEXT NOT NULL, endpoint TEXT NOT NULL DEFAULT '',
		ca_file TEXT NOT NULL DEFAULT '', cert_file TEXT NOT NULL DEFAULT '', key_file TEXT NOT NULL DEFAULT '',
		checkout TEXT NOT NULL DEFAULT '', secret_id TEXT NOT NULL,
		PRIMARY KEY(profile, home_id, project_id, principal_id));
    CREATE TABLE IF NOT EXISTS enrollment_devices(id TEXT PRIMARY KEY,payload BLOB NOT NULL);
    CREATE TABLE IF NOT EXISTS file_reservations(root TEXT, path_key TEXT, path TEXT, holder TEXT, handle TEXT, expires_at INTEGER, PRIMARY KEY(root,path_key));
    CREATE TABLE IF NOT EXISTS service_inbox(profile TEXT, home TEXT, project TEXT, principal TEXT, item TEXT, payload BLOB, events INTEGER DEFAULT 0, messages INTEGER DEFAULT 0, polled_at INTEGER DEFAULT 0, PRIMARY KEY(profile,home,project,principal,item));
    CREATE TABLE IF NOT EXISTS service_wake_batches(profile TEXT, home TEXT, project TEXT, principal TEXT, payload BLOB NOT NULL, PRIMARY KEY(profile,home,project,principal));
    CREATE TRIGGER IF NOT EXISTS clear_service_wake_batch AFTER DELETE ON service_inbox WHEN OLD.item='' BEGIN DELETE FROM service_wake_batches WHERE profile=OLD.profile AND home=OLD.home AND project=OLD.project AND principal=OLD.principal; END;
    CREATE TABLE IF NOT EXISTS service_inbox_wakes(profile TEXT, home TEXT, project TEXT, principal TEXT, item TEXT, PRIMARY KEY(profile,home,project,principal,item));
    CREATE TRIGGER IF NOT EXISTS clear_deleted_inbox_wake AFTER DELETE ON service_inbox BEGIN DELETE FROM service_inbox_wakes WHERE profile=OLD.profile AND home=OLD.home AND project=OLD.project AND principal=OLD.principal AND item=OLD.item; END;
    CREATE TRIGGER IF NOT EXISTS clear_removed_service_inbox AFTER DELETE ON device_connections BEGIN DELETE FROM service_inbox WHERE profile=OLD.profile AND home=OLD.home_id AND project=OLD.project_id AND principal=OLD.principal_id; END;
	CREATE TABLE IF NOT EXISTS local_setup_requests(request_id TEXT PRIMARY KEY,payload_digest TEXT NOT NULL,project_id TEXT NOT NULL,principal_id TEXT NOT NULL,secret_id TEXT NOT NULL,created_at INTEGER NOT NULL);
	CREATE TABLE IF NOT EXISTS known_hosts(host_key TEXT PRIMARY KEY,home_id TEXT NOT NULL DEFAULT '',fingerprint TEXT NOT NULL,server_ca TEXT NOT NULL,first_seen INTEGER NOT NULL,last_seen INTEGER NOT NULL)`); err != nil {
		db.Close()
		return nil, err
	}
	if err = ensureDeviceRegistryColumns(db); err != nil {
		db.Close()
		return nil, err
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(path, 0600)
	}
	return &DeviceRegistry{db: db, configDir: dir, sshPool: NewSSHTunnelPool()}, nil
}

func ensureDeviceRegistryColumns(db *sql.DB) error {
	cols := map[string]bool{}
	rows, err := db.Query(`PRAGMA table_info(device_connections)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			_ = rows.Close()
			return err
		}
		cols[name] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	alters := []struct {
		name string
		ddl  string
	}{
		{"host_fingerprint", `ALTER TABLE device_connections ADD COLUMN host_fingerprint TEXT NOT NULL DEFAULT ''`},
		{"ssh_target", `ALTER TABLE device_connections ADD COLUMN ssh_target TEXT NOT NULL DEFAULT ''`},
		{"ssh_port", `ALTER TABLE device_connections ADD COLUMN ssh_port INTEGER NOT NULL DEFAULT 22`},
		{"ssh_remote_port", `ALTER TABLE device_connections ADD COLUMN ssh_remote_port INTEGER NOT NULL DEFAULT 0`},
		{"ssh_identity_file", `ALTER TABLE device_connections ADD COLUMN ssh_identity_file TEXT NOT NULL DEFAULT ''`},
	}
	for _, a := range alters {
		if !cols[a.name] {
			if _, err := db.Exec(a.ddl); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *DeviceRegistry) Close() error {
	if r.sshPool != nil {
		_ = r.sshPool.Close()
	}
	return r.db.Close()
}

func (r *DeviceRegistry) Put(ctx context.Context, c DeviceConnection, token string) error {
	if c.Profile == "" {
		c.Profile = "default"
	}
	for _, v := range []string{c.HomeID, c.ProjectID, c.PrincipalID, c.Name} {
		if strings.TrimSpace(v) == "" {
			return errors.New("profile, home, project, principal, and name are required")
		}
	}
	if token == "" {
		return errors.New("project credential is required")
	}
	oldSecret := ""
	_ = r.db.QueryRowContext(ctx, `SELECT secret_id FROM device_connections WHERE profile=? AND home_id=? AND project_id=? AND principal_id=?`, c.Profile, c.HomeID, c.ProjectID, c.PrincipalID).Scan(&oldSecret)
	if c.SecretID == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		c.SecretID = hex.EncodeToString(b)
	}
	if err := r.putSecret(c.SecretID, []byte(token)); err != nil {
		return err
	}
	sshPort := c.SSHPort
	if sshPort <= 0 {
		sshPort = defaultSSHPort
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO device_connections(profile,home_id,project_id,principal_id,name,endpoint,ca_file,cert_file,key_file,checkout,host_fingerprint,ssh_target,ssh_port,ssh_remote_port,ssh_identity_file,secret_id)
	VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(profile,home_id,project_id,principal_id) DO UPDATE SET name=excluded.name,endpoint=excluded.endpoint,ca_file=excluded.ca_file,cert_file=excluded.cert_file,key_file=excluded.key_file,checkout=excluded.checkout,host_fingerprint=excluded.host_fingerprint,ssh_target=excluded.ssh_target,ssh_port=excluded.ssh_port,ssh_remote_port=excluded.ssh_remote_port,ssh_identity_file=excluded.ssh_identity_file,secret_id=excluded.secret_id`,
		c.Profile, c.HomeID, c.ProjectID, c.PrincipalID, c.Name, c.Endpoint, c.CAFile, c.CertFile, c.KeyFile, c.Checkout, c.HostFingerprint, c.SSHTarget, sshPort, c.SSHRemotePort, c.SSHIdentityFile, c.SecretID)
	if err != nil {
		_ = os.Remove(filepath.Join(r.configDir, "secrets", c.SecretID))
		return err
	}
	if oldSecret != "" && oldSecret != c.SecretID {
		_ = os.Remove(filepath.Join(r.configDir, "secrets", oldSecret))
	}
	return err
}

func (r *DeviceRegistry) PrepareLocalProject(ctx context.Context, requestID string, payload any) (string, Credential, string, error) {
	if requestID == "" || len(requestID) > 128 {
		return "", Credential{}, "", errors.New("a stable setup request ID is required")
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", Credential{}, "", err
	}
	sum := sha256.Sum256(b)
	digest := hex.EncodeToString(sum[:])
	var oldDigest, projectID, principalID, secretID string
	err = r.db.QueryRowContext(ctx, `SELECT payload_digest,project_id,principal_id,secret_id FROM local_setup_requests WHERE request_id=?`, requestID).Scan(&oldDigest, &projectID, &principalID, &secretID)
	if err == nil {
		if oldDigest != digest {
			return "", Credential{}, "", ErrConflict
		}
		token, e := r.Token(DeviceConnection{SecretID: secretID})
		if e != nil {
			return "", Credential{}, "", e
		}
		return projectID, Credential{PrincipalID: principalID, Token: token}, secretID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", Credential{}, "", err
	}
	requestHash := sha256.Sum256([]byte(requestID))
	proposedID := "proj_" + hex.EncodeToString(requestHash[:16])
	projectID, credential, err := PrepareCredential(proposedID)
	if err != nil {
		return "", Credential{}, "", err
	}
	secretBytes := make([]byte, 16)
	if _, err = rand.Read(secretBytes); err != nil {
		return "", Credential{}, "", err
	}
	secretID = hex.EncodeToString(secretBytes)
	if err = r.putSecret(secretID, []byte(credential.Token)); err != nil {
		return "", Credential{}, "", err
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO local_setup_requests(request_id,payload_digest,project_id,principal_id,secret_id,created_at) VALUES(?,?,?,?,?,?)`, requestID, digest, projectID, credential.PrincipalID, secretID, time.Now().UnixMilli())
	if err != nil {
		_ = os.Remove(filepath.Join(r.configDir, "secrets", secretID))
		return "", Credential{}, "", err
	}
	return projectID, credential, secretID, nil
}

func (r *DeviceRegistry) putSecret(id string, data []byte) error {
	if strings.ContainsAny(id, `/\\`) || id == "." || id == ".." {
		return errors.New("invalid secret reference")
	}
	dir := filepath.Join(r.configDir, "secrets")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, id)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	if werr != nil || serr != nil || cerr != nil {
		_ = os.Remove(tmp)
		return errors.New("could not save private project credential")
	}
	if err = os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (r *DeviceRegistry) Token(c DeviceConnection) (string, error) {
	if c.SecretID == "" {
		return "", errors.New("connection has no secret reference")
	}
	if strings.ContainsAny(c.SecretID, `/\\`) {
		return "", errors.New("invalid secret reference")
	}
	path := filepath.Join(r.configDir, "secrets", c.SecretID)
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return "", errors.New("stored credential is not a private regular file")
	}
	b, err := os.ReadFile(path)
	return strings.TrimSpace(string(b)), err
}

func (r *DeviceRegistry) List(ctx context.Context) ([]DeviceConnection, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT profile,home_id,project_id,principal_id,name,endpoint,ca_file,cert_file,key_file,checkout,host_fingerprint,ssh_target,ssh_port,ssh_remote_port,ssh_identity_file,secret_id FROM device_connections ORDER BY name,profile,principal_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeviceConnection{}
	for rows.Next() {
		var c DeviceConnection
		if err = rows.Scan(&c.Profile, &c.HomeID, &c.ProjectID, &c.PrincipalID, &c.Name, &c.Endpoint, &c.CAFile, &c.CertFile, &c.KeyFile, &c.Checkout, &c.HostFingerprint, &c.SSHTarget, &c.SSHPort, &c.SSHRemotePort, &c.SSHIdentityFile, &c.SecretID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// EnsureSSHClientForward resolves the effective HTTPS endpoint for a connection,
// opening a reusable OpenSSH local port forward when c.SSHTarget is configured.
func (r *DeviceRegistry) EnsureSSHClientForward(ctx context.Context, target, endpoint string, sshPort, remotePort int, identityFile string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return endpoint, nil
	}
	if r.sshPool == nil {
		return "", errors.New("SSH tunnel pool is unavailable")
	}
	if remotePort == 0 {
		remotePort = ParseEndpointPort(endpoint)
	}
	if remotePort == 0 {
		remotePort = defaultSSHRemotePort
	}
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://127.0.0.1:%d", remotePort)
	}
	localAddr, err := r.sshPool.EnsureForward(ctx, SSHTunnelSpec{
		Target:       target,
		SSHPort:      sshPort,
		RemoteHost:   "127.0.0.1",
		RemotePort:   remotePort,
		IdentityFile: identityFile,
	})
	if err != nil {
		return "", err
	}
	return RewriteEndpointHostPort(endpoint, localAddr)
}

// DialRemote opens a mutually authenticated Remote client for c, automatically
// bringing up its SSH local forward when c.SSHTarget is configured.
func (r *DeviceRegistry) DialRemote(ctx context.Context, c DeviceConnection) (*Remote, error) {
	endpoint := c.Endpoint
	if strings.TrimSpace(c.SSHTarget) != "" {
		var err error
		endpoint, err = r.EnsureSSHClientForward(ctx, c.SSHTarget, c.Endpoint, c.SSHPort, c.SSHRemotePort, c.SSHIdentityFile)
		if err != nil {
			return nil, err
		}
	}
	return NewRemote(endpoint, c.CAFile, c.CertFile, c.KeyFile, "")
}

func (r *DeviceRegistry) Resolve(ctx context.Context, profile, project, checkout string) (DeviceConnection, string, error) {
	all, err := r.List(ctx)
	if err != nil {
		return DeviceConnection{}, "", err
	}
	matches := []DeviceConnection{}
	for _, c := range all {
		if profile != "" && c.Profile != profile {
			continue
		}
		if project != "" && c.ProjectID != project && c.Name != project {
			continue
		}
		if checkout != "" && c.Checkout != checkout {
			continue
		}
		matches = append(matches, c)
	}
	if len(matches) == 0 {
		return DeviceConnection{}, "", fmt.Errorf("no authorized project connection matches; use gossip_projects to list available choices")
	}
	if len(matches) > 1 {
		choices := make([]map[string]string, 0, len(matches))
		for _, c := range matches {
			choices = append(choices, map[string]string{"project_id": c.ProjectID, "name": c.Name, "profile": c.Profile})
		}
		b, _ := json.Marshal(choices)
		return DeviceConnection{}, "", fmt.Errorf("project or identity is ambiguous; specify project and profile from authorized choices: %s", b)
	}
	token, err := r.Token(matches[0])
	return matches[0], token, err
}

func (r *DeviceRegistry) Remove(ctx context.Context, profile, homeID, projectID, principalID string) error {
	var secret string
	err := r.db.QueryRowContext(ctx, `SELECT secret_id FROM device_connections WHERE profile=? AND home_id=? AND project_id=? AND principal_id=?`, profile, homeID, projectID, principalID).Scan(&secret)
	if err != nil {
		return err
	}
	if _, err = r.db.ExecContext(ctx, `DELETE FROM device_connections WHERE profile=? AND home_id=? AND project_id=? AND principal_id=?`, profile, homeID, projectID, principalID); err != nil {
		return err
	}
	if secret != "" && !strings.ContainsAny(secret, `/\\`) {
		if err = os.Remove(filepath.Join(r.configDir, "secrets", secret)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
