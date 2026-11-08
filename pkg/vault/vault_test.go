package vault

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncryptDecryptPayload(t *testing.T) {
	passphrase := "my-ultra-secure-passphrase-2026"
	plaintext := []byte("DATABASE_URL=postgres://user:secret@localhost:5432/production\nAPI_KEY=sk_live_9988776655")

	encrypted, err := EncryptPayload(plaintext, passphrase)
	if err != nil {
		t.Fatalf("EncryptPayload failed: %v", err)
	}

	if len(encrypted) <= len(plaintext) {
		t.Errorf("encrypted length (%d) should exceed plaintext (%d)", len(encrypted), len(plaintext))
	}

	decrypted, err := DecryptPayload(encrypted, passphrase)
	if err != nil {
		t.Fatalf("DecryptPayload failed: %v", err)
	}

	if string(decrypted) != string(plaintext) {
		t.Errorf("expected %s, got %s", string(plaintext), string(decrypted))
	}

	// Test with wrong passphrase
	_, err = DecryptPayload(encrypted, "wrong-passphrase")
	if err == nil {
		t.Error("expected decryption to fail with wrong passphrase")
	}

	// Test with corrupted data
	corrupted := append([]byte(nil), encrypted...)
	corrupted[len(corrupted)-1] ^= 0xFF
	_, err = DecryptPayload(corrupted, passphrase)
	if err == nil {
		t.Error("expected decryption to fail with corrupted ciphertext")
	}
}

func TestStageSecretFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-vault-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	vm, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	// Create a dummy secret file outside the vault
	srcFile := filepath.Join(tmpDir, ".env.production")
	secretContent := []byte("SECRET_TOKEN=supersecrettoken123\n")
	if err := os.WriteFile(srcFile, secretContent, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	receipt, err := vm.StageSecretFile(srcFile, "Production environment variables")
	if err != nil {
		t.Fatalf("StageSecretFile failed: %v", err)
	}

	if receipt.SizeBytes != int64(len(secretContent)) {
		t.Errorf("expected size %d, got %d", len(secretContent), receipt.SizeBytes)
	}
	if receipt.SHA256 != ComputeSHA256(secretContent) {
		t.Errorf("expected sha256 %s, got %s", ComputeSHA256(secretContent), receipt.SHA256)
	}
	if receipt.Status != "STAGED" {
		t.Errorf("expected status STAGED, got %s", receipt.Status)
	}

	// Verify permissions on the staged file
	stagedPath := receipt.DestinationPath
	fi, err := os.Stat(stagedPath)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Logf("Notice: staged file perm is %v (expected 0600 on POSIX)", fi.Mode().Perm())
	}

	// List staged files
	list, err := vm.ListStagedFiles()
	if err != nil {
		t.Fatalf("ListStagedFiles failed: %v", err)
	}
	if len(list) != 1 || list[0].Filename != ".env.production" {
		t.Errorf("unexpected staged file list: %+v", list)
	}
}

func TestRemoteSecretTransferFailsClosedWithoutReadingSource(t *testing.T) {
	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.TransferSecretToRemote(filepath.Join(t.TempDir(), "missing-secret"), "http://127.0.0.1:1", "test-passphrase", "secret.txt")
	if !errors.Is(err, ErrRemoteSecretTransferDisabled) {
		t.Fatalf("legacy transfer should be disabled before reading or sending a secret; got %v", err)
	}
}

func TestReceiveSecretPayload(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gossip-vault-rcv-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	vm, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	passphrase := "tunnel-token-secret"
	secretBytes := []byte("CERTIFICATE_PRIVATE_KEY_BYTES_HERE")
	sha := ComputeSHA256(secretBytes)

	enc, err := EncryptPayload(secretBytes, passphrase)
	if err != nil {
		t.Fatalf("EncryptPayload failed: %v", err)
	}

	receipt, err := vm.ReceiveSecretPayload("certs/server.key", enc, passphrase, sha)
	if err != nil {
		t.Fatalf("ReceiveSecretPayload failed: %v", err)
	}

	if receipt.Status != "COMPLETED" || receipt.SHA256 != sha {
		t.Errorf("bad receipt: %+v", receipt)
	}

	// Read decrypted file from disk to verify
	saved, err := os.ReadFile(receipt.DestinationPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(saved) != string(secretBytes) {
		t.Errorf("expected %s, got %s", string(secretBytes), string(saved))
	}

	// Test directory traversal prevention
	_, err = vm.ReceiveSecretPayload("../../../etc/shadow", enc, passphrase, sha)
	if err == nil || !strings.Contains(err.Error(), "traversal disallowed") {
		t.Errorf("expected directory traversal error, got %v", err)
	}
}
