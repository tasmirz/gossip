package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/server"
	"github.com/tasmirz/gossip/pkg/coord"
	"github.com/tasmirz/gossip/pkg/tunnel"
)

type coordCredential struct {
	ProjectID   string `json:"project_id"`
	PrincipalID string `json:"principal_id"`
	Token       string `json:"token"`
}

func coordDefaultDB() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return filepath.Join(".local", "gossip", "coord.db")
	}
	return filepath.Join(dir, "gossip", "coord.db")
}

func coordDefaultOutboxPath(credential coordCredential) string {
	digest := sha256.Sum256([]byte(credential.ProjectID + "\x00" + credential.PrincipalID + "\x00" + credential.Token))
	return filepath.Join(filepath.Dir(coordDefaultDB()), "outbox", hex.EncodeToString(digest[:])+".db")
}

func readCoordCredential(path string) (coordCredential, error) {
	var c coordCredential
	if path == "" {
		return c, errors.New("credential file is required")
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return c, err
	}
	if !pathInfo.Mode().IsRegular() {
		return c, errors.New("credential path must be a private regular file, not a symlink or special file")
	}
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return c, err
	}
	if !os.SameFile(pathInfo, info) {
		return c, errors.New("credential file changed while it was being opened")
	}
	if err = validatePrivateCredentialInfo(info); err != nil {
		return c, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	if c.ProjectID == "" || c.PrincipalID == "" || c.Token == "" {
		return c, errors.New("invalid coordination credential")
	}
	return c, nil
}

func validatePrivateCredentialInfo(info os.FileInfo) error {
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return errors.New("credential file must be a private regular file")
	}
	return nil
}

func certificateFingerprint(path string) (string, error) {
	if path == "" {
		return "", errors.New("client certificate file is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return "", errors.New("invalid client certificate PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:]), nil
}
func credentialOutput(path string) (*os.File, error) {
	if path == "" {
		return nil, errors.New("credential file is required")
	}
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
}
func writeCoordCredential(f *os.File, project string, c coord.Credential) error {
	err := json.NewEncoder(f).Encode(coordCredential{ProjectID: project, PrincipalID: c.PrincipalID, Token: c.Token})
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func expiryTimestamp(lifetime time.Duration) int64 {
	if lifetime == 0 {
		return 0
	}
	return time.Now().Add(lifetime).UnixMilli()
}

// preparedFile writes the private credential durably before attempting mutation.
func preparedFile(path, project string, resume bool) (coordCredential, error) {
	if resume {
		c, err := readCoordCredential(path)
		if err != nil {
			return c, err
		}
		if project != "" && c.ProjectID != project {
			return c, errors.New("credential belongs to another project")
		}
		return c, nil
	}
	id, credential, err := coord.PrepareCredential(project)
	if err != nil {
		return coordCredential{}, err
	}
	f, err := credentialOutput(path)
	if err != nil {
		return coordCredential{}, err
	}
	if err = writeCoordCredential(f, id, credential); err != nil {
		os.Remove(path)
		return coordCredential{}, err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return coordCredential{}, fmt.Errorf("credential retained at %s; directory sync failed: %w", path, err)
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil {
		return coordCredential{}, fmt.Errorf("credential retained at %s; directory sync failed: %w", path, syncErr)
	}
	if closeErr != nil {
		return coordCredential{}, fmt.Errorf("credential retained at %s; directory close failed: %w", path, closeErr)
	}
	return coordCredential{ProjectID: id, PrincipalID: credential.PrincipalID, Token: credential.Token}, nil
}
func runCoordInit(args []string) error {
	fs := flag.NewFlagSet("coord-init", flag.ContinueOnError)
	db := fs.String("db", coordDefaultDB(), "local coordination database")
	socket := fs.String("socket", "", "private local coordination daemon socket")
	name := fs.String("name", "", "project name")
	owner := fs.String("owner", "", "owner name")
	file := fs.String("credential-file", "", "private credential file")
	resume := fs.Bool("resume", false, "retry with existing prepared credential")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" || *owner == "" {
		return errors.New("project name and owner are required")
	}
	c, err := preparedFile(*file, "", *resume)
	if err != nil {
		return err
	}
	var backend interface {
		CreatePreparedProject(context.Context, string, string, string, coord.Credential) (coord.Project, error)
	}
	if *socket != "" {
		backend, err = coord.NewLocalRemote(*socket)
	} else {
		var store *coord.Store
		store, err = coord.Open(*db)
		if err == nil {
			defer store.Close()
			backend = store
		}
	}
	if err != nil {
		return fmt.Errorf("credential retained at %s; retry with --resume: %w", *file, err)
	}
	p, err := backend.CreatePreparedProject(context.Background(), c.ProjectID, *name, *owner, coord.Credential{PrincipalID: c.PrincipalID, Token: c.Token})
	if err != nil {
		return fmt.Errorf("credential retained at %s; retry with --resume and the same names: %w", *file, err)
	}
	fmt.Printf("Created project %s; credential saved to %s\n", p.ID, *file)
	return nil
}
func runCoordAdd(args []string) error {
	fs := flag.NewFlagSet("coord-add", flag.ContinueOnError)
	db := fs.String("db", coordDefaultDB(), "local coordination database")
	socket := fs.String("socket", "", "private local coordination daemon socket")
	remoteURL := fs.String("remote", "", "HTTPS home endpoint")
	tlsCA := fs.String("tls-ca", "", "trusted home server CA certificate")
	tlsCert := fs.String("tls-cert", "", "enrolled client certificate")
	tlsKey := fs.String("tls-key", "", "client private key")
	tlsServerName := fs.String("tls-server-name", "", "optional server certificate name")
	ownerFile := fs.String("owner-credential", "", "owner credential file")
	name := fs.String("name", "", "principal name")
	role := fs.String("role", "worker", "owner, worker, or reviewer")
	clientCertificate := fs.String("client-certificate", "", "optional remote client certificate to grant this project")
	accessExpiresIn := fs.Duration("access-expires-in", 0, "optional lifetime for the certificate project grant (for example 24h)")
	file := fs.String("credential-file", "", "private credential file")
	resume := fs.Bool("resume", false, "retry with existing prepared credential")
	if err := fs.Parse(args); err != nil {
		return err
	}
	owner, err := readCoordCredential(*ownerFile)
	if err != nil {
		return err
	}
	c, err := preparedFile(*file, owner.ProjectID, *resume)
	if err != nil {
		return err
	}
	var backend interface {
		AddPreparedPrincipal(context.Context, string, string, string, string, coord.Credential) (coord.Credential, error)
		AddPreparedRemotePrincipal(context.Context, string, string, string, string, coord.Credential, string, int64) (coord.Credential, error)
	}
	if *socket != "" {
		if *remoteURL != "" {
			return errors.New("choose either --socket or --remote")
		}
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
		return fmt.Errorf("credential retained at %s; retry with --resume: %w", *file, err)
	}
	prepared := coord.Credential{PrincipalID: c.PrincipalID, Token: c.Token}
	if *clientCertificate != "" {
		if *accessExpiresIn < 0 {
			return errors.New("access expiry must not be negative")
		}
		fingerprint, fpErr := certificateFingerprint(*clientCertificate)
		if fpErr != nil {
			return fpErr
		}
		_, err = backend.AddPreparedRemotePrincipal(context.Background(), owner.ProjectID, owner.Token, *name, *role, prepared, fingerprint, expiryTimestamp(*accessExpiresIn))
	} else {
		_, err = backend.AddPreparedPrincipal(context.Background(), owner.ProjectID, owner.Token, *name, *role, prepared)
	}
	if err != nil {
		return fmt.Errorf("credential retained at %s; retry with --resume and the same name and role: %w", *file, err)
	}
	fmt.Printf("Created principal %s; credential saved to %s\n", c.PrincipalID, *file)
	return nil
}
func runCoordMCP(args []string) error {
	fs := flag.NewFlagSet("coord-mcp", flag.ContinueOnError)
	db := fs.String("db", coordDefaultDB(), "local coordination database")
	file := fs.String("credential-file", "", "project credential file")
	remote := fs.String("remote", "", "HTTPS origin for a mutually authenticated home node")
	socket := fs.String("socket", "", "private local coordination daemon socket")
	ca := fs.String("tls-ca", "", "trusted server CA certificate")
	cert := fs.String("tls-cert", "", "client certificate")
	key := fs.String("tls-key", "", "client private key")
	serverName := fs.String("tls-server-name", "", "optional server certificate name")
	outboxPath := fs.String("outbox", "", "private local SQLite queue for remote messages (defaults to a per-credential path)")
	hostCapabilities := fs.String("host-capabilities", "", "comma-separated configured host reports: notification,wake,launch,interrupt,isolated_workspace,usage_reporting,model_selection")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var hostCapabilityNames []string
	for _, name := range strings.Split(*hostCapabilities, ",") {
		if strings.TrimSpace(name) != "" {
			hostCapabilityNames = append(hostCapabilityNames, strings.TrimSpace(name))
		}
	}
	if _, err := coord.HostReportedCapabilities(hostCapabilityNames); err != nil {
		return err
	}
	c, err := readCoordCredential(*file)
	if err != nil {
		return err
	}
	if *remote != "" && *socket != "" {
		return errors.New("choose either remote or socket")
	}
	if *socket != "" {
		if *outboxPath != "" {
			return errors.New("outbox requires a remote home")
		}
		backend, err := coord.NewLocalRemote(*socket)
		if err != nil {
			return err
		}
		adapter, err := coord.NewMCPAdapterWithHostCapabilities(backend, c.ProjectID, c.Token, nil, hostCapabilityNames)
		if err != nil {
			return err
		}
		defer adapter.Close()
		return server.ServeStdio(adapter.Server)
	}
	if *remote != "" {
		backend, err := coord.NewRemote(*remote, *ca, *cert, *key, *serverName)
		if err != nil {
			return err
		}
		if *outboxPath == "" {
			*outboxPath = coordDefaultOutboxPath(c)
		}
		var adapter *coord.MCPAdapter
		outbox, err := coord.OpenOutbox(*outboxPath, *remote, c.ProjectID, c.PrincipalID, c.Token, backend)
		if err != nil {
			return err
		}
		adapter, err = coord.NewMCPAdapterWithHostCapabilities(backend, c.ProjectID, c.Token, outbox, hostCapabilityNames)
		if err != nil {
			outbox.Close()
			return err
		}
		defer adapter.Close()
		return server.ServeStdio(adapter.Server)
	}
	if *outboxPath != "" {
		return errors.New("outbox requires a remote home")
	}
	s, err := coord.Open(*db)
	if err != nil {
		return err
	}
	defer s.Close()
	adapter, err := coord.NewMCPAdapterWithHostCapabilities(s, c.ProjectID, c.Token, nil, hostCapabilityNames)
	if err != nil {
		return err
	}
	defer adapter.Close()
	return server.ServeStdio(adapter.Server)
}

func runCoordBootstrapKey(args []string) error {
	fs := flag.NewFlagSet("coord-bootstrap-key", flag.ContinueOnError)
	file := fs.String("key-file", "", "private create-only project bootstrap API key file")
	db := fs.String("db", "", "local coordination database to authorize this key for project creation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return errors.New("--key-file is required")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	output, err := os.OpenFile(*file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := output.WriteString(base64.RawURLEncoding.EncodeToString(key) + "\n")
	syncErr := output.Sync()
	closeErr := output.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(*file)
		return errors.New("could not save bootstrap API key securely")
	}
	if *db != "" {
		store, openErr := coord.Open(*db)
		if openErr == nil {
			openErr = store.ConfigureLocalBootstrapKey(context.Background(), base64.RawURLEncoding.EncodeToString(key))
			closeErr = store.Close()
		}
		if openErr != nil || closeErr != nil {
			_ = os.Remove(*file)
			if openErr != nil {
				return fmt.Errorf("configure local bootstrap key: %w", openErr)
			}
			return fmt.Errorf("close coordination database after key setup: %w", closeErr)
		}
	}
	fmt.Printf("Create-only bootstrap API key saved to %s\n", *file)
	return nil
}

func runCoordMCPBootstrap(args []string) error {
	fs := flag.NewFlagSet("coord-mcp-bootstrap", flag.ContinueOnError)
	db := fs.String("db", coordDefaultDB(), "local coordination database")
	socket := fs.String("socket", "", "private local coordination daemon socket")
	remoteURL := fs.String("remote", "", "HTTPS project home for remote bootstrap")
	tlsCA := fs.String("tls-ca", "", "trusted home server CA certificate (required with --remote)")
	tlsCert := fs.String("tls-cert", "", "client certificate issued by the home CA")
	tlsKey := fs.String("tls-key", "", "client private key")
	tlsServerName := fs.String("tls-server-name", "", "optional server certificate name")
	keyFile := fs.String("bootstrap-api-key-file", "", "private create-only bootstrap API key configured for this MCP server")
	credentialDir := fs.String("credential-dir", filepath.Join(filepath.Dir(coordDefaultDB()), "credentials"), "directory for private project owner credentials")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *keyFile == "" {
		return errors.New("--bootstrap-api-key-file is required; create one with gossip coord-bootstrap-key")
	}
	if *remoteURL != "" && *socket != "" {
		return errors.New("choose either --remote or --socket")
	}
	info, err := os.Lstat(*keyFile)
	if err != nil {
		return err
	}
	if err = validatePrivateCredentialInfo(info); err != nil {
		return fmt.Errorf("bootstrap API key: %w", err)
	}
	key, err := os.ReadFile(*keyFile)
	if err != nil {
		return err
	}
	apiKey := strings.TrimSpace(string(key))
	if len(apiKey) < 32 {
		return errors.New("bootstrap API key must contain at least 32 characters")
	}
	var creator interface {
		CreatePreparedProjectWithWorkspaceUsingBootstrapKey(context.Context, string, string, string, string, string, string, coord.GitObservation, coord.Credential) (coord.Project, coord.Credential, coord.Repository, coord.Workspace, error)
	}
	var store *coord.Store
	if *remoteURL != "" {
		creator, err = coord.NewRemoteBootstrap(*remoteURL, *tlsCA, *tlsCert, *tlsKey, *tlsServerName, apiKey)
	} else if *socket != "" {
		creator, err = coord.NewLocalRemote(*socket)
	} else {
		store, err = coord.Open(*db)
		creator = store
	}
	if err != nil {
		return err
	}
	if store != nil {
		defer store.Close()
	}
	return server.ServeStdio(coord.ProjectBootstrapMCPServer(creator, *credentialDir, apiKey))
}

func runCoordAccess(args []string) error {
	fs := flag.NewFlagSet("coord-access", flag.ContinueOnError)
	db := fs.String("db", coordDefaultDB(), "local coordination database")
	socket := fs.String("socket", "", "private local coordination daemon socket")
	remoteURL := fs.String("remote", "", "HTTPS home endpoint")
	tlsCA := fs.String("tls-ca", "", "trusted home server CA certificate")
	tlsCert := fs.String("tls-cert", "", "enrolled client certificate")
	tlsKey := fs.String("tls-key", "", "client private key")
	tlsServerName := fs.String("tls-server-name", "", "optional server certificate name")
	credentialFile := fs.String("credential-file", "", "owner credential for the project")
	action := fs.String("action", "list", "list, grant, or revoke")
	fingerprint := fs.String("fingerprint", "", "client certificate SHA-256 fingerprint")
	certificate := fs.String("certificate", "", "client certificate PEM; fingerprint is calculated locally")
	expiresIn := fs.Duration("expires-in", 0, "optional grant lifetime, for example 24h; zero is indefinite")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := readCoordCredential(*credentialFile)
	if err != nil {
		return err
	}
	if *fingerprint != "" && *certificate != "" {
		return errors.New("provide either --fingerprint or --certificate")
	}
	fp := *fingerprint
	if *certificate != "" {
		fp, err = certificateFingerprint(*certificate)
		if err != nil {
			return err
		}
	}
	if *action != "list" && *action != "grant" && *action != "revoke" {
		return errors.New("action must be list, grant, or revoke")
	}
	if *action != "list" && fp == "" {
		return errors.New("grant and revoke require --fingerprint or --certificate")
	}
	if *expiresIn < 0 || (*action != "grant" && *expiresIn != 0) {
		return errors.New("--expires-in must be nonnegative and applies only to grant")
	}
	var backend interface {
		AddCertificateGrantUntil(context.Context, string, string, string, int64) (coord.CertificateGrant, error)
		ListCertificateGrants(context.Context, string, string) ([]coord.CertificateGrant, error)
		RevokeCertificateGrant(context.Context, string, string, string) error
	}
	if *socket != "" {
		if *remoteURL != "" {
			return errors.New("choose either --socket or --remote")
		}
		backend, err = coord.NewLocalRemote(*socket)
		if err != nil {
			return err
		}
	} else if *remoteURL != "" {
		backend, err = coord.NewRemote(*remoteURL, *tlsCA, *tlsCert, *tlsKey, *tlsServerName)
		if err != nil {
			return err
		}
	} else {
		backend, err = coord.Open(*db)
		if err != nil {
			return err
		}
		defer backend.(*coord.Store).Close()
	}
	ctx := context.Background()
	switch *action {
	case "list":
		grants, e := backend.ListCertificateGrants(ctx, c.ProjectID, c.Token)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(grants)
	case "grant":
		var expiresAt int64
		if *expiresIn > 0 {
			expiresAt = time.Now().Add(*expiresIn).UnixMilli()
		}
		grant, e := backend.AddCertificateGrantUntil(ctx, c.ProjectID, c.Token, fp, expiresAt)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(grant)
	case "revoke":
		if err = backend.RevokeCertificateGrant(ctx, c.ProjectID, c.Token, fp); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "Client certificate access revoked")
		return nil
	}
	return nil
}

func runCoordServe(args []string) error {
	fs := flag.NewFlagSet("coord-serve", flag.ContinueOnError)
	db := fs.String("db", coordDefaultDB(), "local coordination database")
	listen := fs.String("listen", "127.0.0.1:8443", "TLS listening address")
	certFile := fs.String("tls-cert", "", "server certificate")
	keyFile := fs.String("tls-key", "", "server private key")
	clientCA := fs.String("client-ca", "", "trusted client CA certificate")
	bootstrapKeyFile := fs.String("bootstrap-api-key-file", "", "optional private create-only API key for remote project creation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cert, err := tls.LoadX509KeyPair(*certFile, *keyFile)
	if err != nil {
		return err
	}
	caBytes, err := os.ReadFile(*clientCA)
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caBytes) {
		return errors.New("invalid client CA certificate")
	}
	s, err := coord.Open(*db)
	if err != nil {
		return err
	}
	defer s.Close()
	config := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, ClientCAs: pool, ClientAuth: tls.RequireAndVerifyClientCert}
	var handler http.Handler
	if *bootstrapKeyFile == "" {
		handler = coord.RPCHandlerWithCertificateGrants(s)
	} else {
		bootstrapInfo, statErr := os.Lstat(*bootstrapKeyFile)
		if statErr != nil {
			return statErr
		}
		if err = validatePrivateCredentialInfo(bootstrapInfo); err != nil {
			return fmt.Errorf("bootstrap API key: %w", err)
		}
		bootstrapBytes, readErr := os.ReadFile(*bootstrapKeyFile)
		if readErr != nil {
			return readErr
		}
		handler, err = coord.RPCHandlerWithBootstrapKey(s, strings.TrimSpace(string(bootstrapBytes)))
		if err != nil {
			return err
		}
	}
	listener, err := tls.Listen("tcp", *listen, config)
	if err != nil {
		return err
	}
	defer listener.Close()
	fmt.Fprintf(os.Stderr, "Coordination TLS endpoint: %s\n", listener.Addr())
	web := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	return web.Serve(listener)
}

func runCoordFRP(args []string) error {
	fs := flag.NewFlagSet("coord-frp", flag.ContinueOnError)
	server := fs.String("server", "", "FRP server host or IP")
	serverPort := fs.Int("server-port", 7000, "FRP control port")
	token := fs.String("token", "", "FRP server authentication token")
	localPort := fs.Int("local-port", 8443, "local mutual-TLS coordination port")
	remotePort := fs.Int("remote-port", 0, "public FRP TCP port assigned to this home node")
	configFile := fs.String("config-file", "", "private destination path for generated FRP config")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *server == "" || strings.ContainsAny(*server, "/\\ \t\r\n") || *token == "" || len(*token) > 4096 || *remotePort < 1 || *remotePort > 65535 || *localPort < 1 || *localPort > 65535 || *serverPort < 1 || *serverPort > 65535 || *configFile == "" {
		return errors.New("server, token, valid server/local/remote ports, and --config-file are required")
	}
	cfg := tunnel.FRPClientConfig{ServerAddr: *server, ServerPort: *serverPort, AuthToken: *token, ProxyName: "gossip-coordination", ProxyType: "tcp", LocalIP: "127.0.0.1", LocalPort: *localPort, RemotePort: *remotePort, UseEncryption: true, UseCompression: true}
	data := []byte(tunnel.GenerateFRPCTOML(cfg))
	f, err := os.OpenFile(*configFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(*configFile)
		}
	}()
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		dir, openErr := os.Open(filepath.Dir(*configFile))
		if openErr != nil {
			return openErr
		}
		err = dir.Sync()
		closeErr := dir.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	cleanup = false
	fmt.Printf("Private FRP TCP config written to %s; it forwards to the mutual-TLS coordination listener on %s:%d\n", *configFile, cfg.LocalIP, cfg.LocalPort)
	return nil
}

func runCoordUI(args []string) error {
	fs := flag.NewFlagSet("coord-ui", flag.ContinueOnError)
	db := fs.String("db", coordDefaultDB(), "local coordination database")
	port := fs.Int("port", 8081, "dashboard port")
	listen := fs.String("listen", "127.0.0.1", "dashboard listen address")
	socket := fs.String("socket", "", "private local coordination daemon socket")
	tlsCert := fs.String("tls-cert", "", "HTTPS server certificate (required off loopback)")
	tlsKey := fs.String("tls-key", "", "HTTPS server private key")
	clientCA := fs.String("client-ca", "", "trusted human client CA for mutual TLS")
	var hosts stringListFlag
	fs.Var(&hosts, "host", "allowed HTTPS Host including port; repeat for each dashboard URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *port < 0 || *port > 65535 {
		return errors.New("invalid dashboard port")
	}
	var backend coord.WebBackend
	if *socket != "" {
		remote, err := coord.NewLocalRemote(*socket)
		if err != nil {
			return err
		}
		backend = remote
	} else {
		store, err := coord.Open(*db)
		if err != nil {
			return err
		}
		defer store.Close()
		backend = store
	}
	address := net.JoinHostPort(*listen, strconv.Itoa(*port))
	ip := net.ParseIP(*listen)
	loopback := strings.EqualFold(*listen, "localhost") || (ip != nil && ip.IsLoopback())
	secure := *tlsCert != "" || *tlsKey != "" || *clientCA != ""
	if !loopback && !secure {
		return errors.New("non-loopback dashboard requires HTTPS mutual TLS and enrolled client certificates")
	}
	if secure && (*tlsCert == "" || *tlsKey == "" || *clientCA == "" || len(hosts) == 0) {
		return errors.New("HTTPS dashboard requires server cert/key, client CA, and --host")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	var handler http.Handler = coord.NewWebHandler(backend)
	scheme := "http"
	if secure {
		cert, e := tls.LoadX509KeyPair(*tlsCert, *tlsKey)
		if e != nil {
			return e
		}
		ca, e := os.ReadFile(*clientCA)
		if e != nil {
			return e
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(ca) {
			return errors.New("invalid client CA")
		}
		config := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, ClientCAs: pool, ClientAuth: tls.RequireAndVerifyClientCert}
		listener = tls.NewListener(listener, config)
		handler = coord.NewWebHandlerWithCertificateGrants(backend, hosts)
		scheme = "https"
	} else if !loopback {
		return errors.New("dashboard must listen on loopback or use mutual TLS")
	}
	fmt.Fprintf(os.Stderr, "Coordination dashboard: %s://%s\n", scheme, listener.Addr())
	web := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	return web.Serve(listener)
}

func runCoordRemote(args []string) error {
	fs := flag.NewFlagSet("coord-remote", flag.ContinueOnError)
	action := fs.String("action", "status", "status, enable, or disable")
	db := fs.String("db", coordDefaultDB(), "local coordination database")
	socket := fs.String("socket", "", "private local coordination daemon socket")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *action != "status" && *action != "enable" && *action != "disable" {
		return errors.New("--action must be status, enable, or disable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var enabled bool
	if *socket != "" {
		remote, err := coord.NewLocalRemote(*socket)
		if err != nil {
			return err
		}
		defer remote.CloseIdleConnections()
		if *action == "status" {
			enabled, err = remote.LocalRemoteAccessStatus(ctx)
		} else {
			enabled = *action == "enable"
			err = remote.LocalSetRemoteAccessEnabled(ctx, enabled)
		}
		if err != nil {
			return err
		}
	} else {
		store, err := coord.Open(*db)
		if err != nil {
			return err
		}
		defer store.Close()
		if *action == "status" {
			enabled, err = store.RemoteAccessEnabled(ctx)
		} else {
			enabled = *action == "enable"
			err = store.SetRemoteAccessEnabled(ctx, enabled)
		}
		if err != nil {
			return err
		}
	}
	if enabled {
		fmt.Println("Remote coordination enabled")
	} else {
		fmt.Println("Remote coordination disabled (local kill switch active)")
	}
	return nil
}

func runCoordDaemon(args []string) error {
	fs := flag.NewFlagSet("coord-daemon", flag.ContinueOnError)
	db := fs.String("db", coordDefaultDB(), "local coordination database")
	socket := fs.String("socket", filepath.Join(filepath.Dir(coordDefaultDB()), "coord.sock"), "private Unix socket path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, err := coord.Open(*db)
	if err != nil {
		return err
	}
	defer s.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return coord.ServeLocal(ctx, s, *socket)
}

func runCoordMigrate(args []string) error {
	fs := flag.NewFlagSet("coord-migrate", flag.ContinueOnError)
	legacyDir := fs.String("legacy-dir", "", "legacy .local/gossip directory to inspect")
	dryRun := fs.Bool("dry-run", false, "inspect and report without changing data (the default)")
	apply := fs.Bool("apply", false, "import legacy records into a project-scoped historical archive")
	db := fs.String("db", coordDefaultDB(), "local coordination database for --apply")
	credentialFile := fs.String("credential-file", "", "private owner credential file for --apply")
	acceptWarnings := fs.Bool("accept-warnings", false, "proceed after reviewing all preflight warnings")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *legacyDir == "" {
		return errors.New("--legacy-dir is required")
	}
	if *dryRun && *apply {
		return errors.New("choose either --dry-run or --apply")
	}
	if !*apply {
		if *acceptWarnings {
			return errors.New("--accept-warnings applies only with --apply")
		}
		report, err := coord.AuditLegacyStore(*legacyDir)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(report)
	}
	if *credentialFile == "" {
		return errors.New("--credential-file is required with --apply")
	}
	source, err := filepath.Abs(*legacyDir)
	if err != nil {
		return err
	}
	database, err := filepath.Abs(*db)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(source, database)
	if err != nil {
		return err
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return errors.New("coordination database must be outside the legacy source directory")
	}
	credential, err := preparedFile(*credentialFile, "", true)
	if err != nil {
		return err
	}
	store, err := coord.Open(database)
	if err != nil {
		return err
	}
	defer store.Close()
	imported, audit, err := store.ImportLegacyArchive(context.Background(), credential.ProjectID, credential.Token, source, *acceptWarnings)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Import coord.LegacyImport   `json:"import"`
		Audit  coord.MigrationAudit `json:"audit"`
	}{Import: imported, Audit: audit})
}

type stringListFlag []string

func (v *stringListFlag) String() string         { return strings.Join(*v, ",") }
func (v *stringListFlag) Set(value string) error { *v = append(*v, value); return nil }
