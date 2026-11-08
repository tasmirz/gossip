package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var ErrRemoteSecretTransferDisabled = errors.New("legacy remote secret transfer is disabled; deliver credentials through a separate secure channel")

// TransferReceipt summarizes a secure file transfer without revealing secret contents to agent context.
type TransferReceipt struct {
	TransferID          string    `json:"transfer_id"`
	SourcePath          string    `json:"source_path"`
	DestinationEndpoint string    `json:"destination_endpoint"`
	DestinationPath     string    `json:"destination_path"`
	SizeBytes           int64     `json:"size_bytes"`
	SHA256              string    `json:"sha256"`
	Encrypted           bool      `json:"encrypted"`
	Status              string    `json:"status"` // "COMPLETED", "STAGED", "FAILED"
	Timestamp           time.Time `json:"timestamp"`
	Note                string    `json:"note"`
}

// VaultEntry represents a staged secret file stored in the local vault with 0600 permissions.
type VaultEntry struct {
	ID          string    `json:"id"`
	Filename    string    `json:"filename"`
	RelPath     string    `json:"rel_path"`
	SizeBytes   int64     `json:"size_bytes"`
	SHA256      string    `json:"sha256"`
	CreatedAt   time.Time `json:"created_at"`
	Description string    `json:"description,omitempty"`
}

// VaultIndex maintains an inventory of staged secret files.
type VaultIndex struct {
	Entries      map[string]VaultEntry `json:"entries"`
	LastModified time.Time             `json:"last_modified"`
}

// Manager coordinates zero-exposure secret file staging and transfer.
type Manager struct {
	storageDir string
	vaultDir   string
	mu         sync.RWMutex
}

// NewManager creates a Vault Manager scoped to a project storage directory.
func NewManager(storageDir string) (*Manager, error) {
	if storageDir == "" {
		storageDir = ".local/gossip"
	}
	vDir := filepath.Join(storageDir, "vault")
	if err := os.MkdirAll(vDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create vault directory: %w", err)
	}

	return &Manager{
		storageDir: storageDir,
		vaultDir:   vDir,
	}, nil
}

// VaultDir returns the absolute path to the local vault.
func (m *Manager) VaultDir() string {
	return m.vaultDir
}

// deriveKey derives a 32-byte AES key from passphrase using PBKDF2-HMAC-SHA256 with 10,000 iterations.
func deriveKey(passphrase string, salt []byte) []byte {
	prf := hmac.New(sha256.New, []byte(passphrase))
	prf.Write(salt)
	prf.Write([]byte{0, 0, 0, 1})
	u := prf.Sum(nil)
	t := append([]byte(nil), u...)
	for i := 1; i < 10000; i++ {
		prf.Reset()
		prf.Write(u)
		u = prf.Sum(nil)
		for j := range t {
			t[j] ^= u[j]
		}
	}
	return t[:32]
}

// EncryptPayload encrypts arbitrary binary data using AES-256-GCM.
// Output format: [16 bytes salt] + [12 bytes nonce] + [ciphertext + tag]
func EncryptPayload(plaintext []byte, passphrase string) ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}

	key := deriveKey(passphrase, salt)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)

	// Combine: salt + nonce + ciphertext
	out := make([]byte, 0, len(salt)+len(nonce)+len(ciphertext))
	out = append(out, salt...)
	out = append(out, nonce...)
	out = append(out, ciphertext...)
	return out, nil
}

// DecryptPayload decrypts data encrypted with EncryptPayload.
func DecryptPayload(encryptedData []byte, passphrase string) ([]byte, error) {
	if len(encryptedData) < 28 { // 16 salt + 12 nonce
		return nil, errors.New("encrypted payload too short")
	}

	salt := encryptedData[:16]
	nonce := encryptedData[16:28]
	ciphertext := encryptedData[28:]

	key := deriveKey(passphrase, salt)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	return gcm.Open(nil, nonce, ciphertext, nil)
}

// ComputeSHA256 returns the hex-encoded SHA-256 checksum of data.
func ComputeSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// StageSecretFile imports a local file into the vault with 0600 permissions,
// returning only metadata so that secret contents never enter agent context.
func (m *Manager) StageSecretFile(srcPath, description string) (*TransferReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(srcPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read secret source file: %w", err)
	}

	size := int64(len(data))
	checksum := ComputeSHA256(data)
	fileName := filepath.Base(srcPath)

	id := fmt.Sprintf("sec-%x", time.Now().UnixNano()%100000000)
	destPath := filepath.Join(m.vaultDir, fileName)

	// Write with 0600 (owner read/write only)
	if err := os.WriteFile(destPath, data, 0600); err != nil {
		return nil, fmt.Errorf("failed to stage secret file in vault: %w", err)
	}

	// Update index
	idx, _ := m.loadIndex()
	idx.Entries[id] = VaultEntry{
		ID:          id,
		Filename:    fileName,
		RelPath:     filepath.Join("vault", fileName),
		SizeBytes:   size,
		SHA256:      checksum,
		CreatedAt:   time.Now(),
		Description: description,
	}
	_ = m.saveIndex(idx)

	return &TransferReceipt{
		TransferID:          id,
		SourcePath:          srcPath,
		DestinationEndpoint: "local://vault",
		DestinationPath:     destPath,
		SizeBytes:           size,
		SHA256:              checksum,
		Encrypted:           false,
		Status:              "STAGED",
		Timestamp:           time.Now(),
		Note:                "Secret file safely staged in protected local vault (0600 permissions). Contents withheld from agent context.",
	}, nil
}

// ListStagedFiles returns an inventory of staged files without revealing their contents.
func (m *Manager) ListStagedFiles() ([]VaultEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	idx, err := m.loadIndex()
	if err != nil {
		return nil, err
	}

	var list []VaultEntry
	for _, entry := range idx.Entries {
		list = append(list, entry)
	}
	return list, nil
}

// TransferSecretToRemote is retained for source compatibility, but remote
// secret transfer is disabled. Project credentials and transport certificates
// must be delivered through a separate secure channel.
func (m *Manager) TransferSecretToRemote(srcPath, remoteEndpoint, authToken, destRelPath string) (*TransferReceipt, error) {
	return nil, ErrRemoteSecretTransferDisabled
}

// ReceiveSecretPayload receives, decrypts, and saves a secret file with 0600 permissions.
func (m *Manager) ReceiveSecretPayload(destRelPath string, encData []byte, authToken, expectedSHA string) (*TransferReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	decrypted, err := DecryptPayload(encData, authToken)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt payload: %w", err)
	}

	actualSHA := ComputeSHA256(decrypted)
	if expectedSHA != "" && actualSHA != expectedSHA {
		return nil, fmt.Errorf("SHA-256 checksum mismatch (expected %s, got %s)", expectedSHA, actualSHA)
	}

	cleanRel := filepath.Clean(destRelPath)
	if strings.HasPrefix(cleanRel, "..") {
		return nil, errors.New("invalid destination path: directory traversal disallowed")
	}

	destPath := filepath.Join(m.vaultDir, cleanRel)
	if err := os.MkdirAll(filepath.Dir(destPath), 0700); err != nil {
		return nil, err
	}

	if err := os.WriteFile(destPath, decrypted, 0600); err != nil {
		return nil, fmt.Errorf("failed to save decrypted file: %w", err)
	}

	id := fmt.Sprintf("rcv-%x", time.Now().UnixNano()%100000000)
	size := int64(len(decrypted))

	idx, _ := m.loadIndex()
	idx.Entries[id] = VaultEntry{
		ID:          id,
		Filename:    filepath.Base(destRelPath),
		RelPath:     filepath.Join("vault", cleanRel),
		SizeBytes:   size,
		SHA256:      actualSHA,
		CreatedAt:   time.Now(),
		Description: "Received via encrypted mesh tunnel",
	}
	_ = m.saveIndex(idx)

	return &TransferReceipt{
		TransferID:          id,
		SourcePath:          "remote://encrypted-payload",
		DestinationEndpoint: "local://vault",
		DestinationPath:     destPath,
		SizeBytes:           size,
		SHA256:              actualSHA,
		Encrypted:           true,
		Status:              "COMPLETED",
		Timestamp:           time.Now(),
		Note:                "Received and decrypted payload successfully into protected vault (0600 permissions). Zero agent exposure.",
	}, nil
}

func (m *Manager) loadIndex() (*VaultIndex, error) {
	indexPath := filepath.Join(m.vaultDir, "index.json")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &VaultIndex{
				Entries:      make(map[string]VaultEntry),
				LastModified: time.Now(),
			}, nil
		}
		return nil, err
	}
	var idx VaultIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, err
	}
	if idx.Entries == nil {
		idx.Entries = make(map[string]VaultEntry)
	}
	return &idx, nil
}

func (m *Manager) saveIndex(idx *VaultIndex) error {
	idx.LastModified = time.Now()
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(m.vaultDir, "index.json"), data, 0600)
}
