package vault_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sopranoworks/gityard/internal/vault"
)

func openTestVault(t *testing.T) *vault.Vault {
	t.Helper()
	v, err := vault.Open(filepath.Join(t.TempDir(), "keys.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })
	return v
}

func TestVaultLockUnlock(t *testing.T) {
	v := openTestVault(t)

	if v.State() != vault.VaultLocked {
		t.Error("initial state should be locked")
	}

	if err := v.Unlock("testpassword"); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if v.State() != vault.VaultUnlocked {
		t.Error("state should be unlocked after Unlock")
	}

	v.Lock()
	if v.State() != vault.VaultLocked {
		t.Error("state should be locked after Lock")
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	v := openTestVault(t)
	v.Unlock("testpassword")

	plaintext := []byte("secret private key data")
	// Use GenerateKey + DecryptPrivateKey for round-trip since encrypt/decrypt are unexported.
	// Instead, test via key generation.
	pubKey, err := v.GenerateKey("ns", "test-key", "test@example.com")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.HasPrefix(pubKey, "ssh-ed25519 ") {
		t.Errorf("public key format: %q", pubKey)
	}

	pem, err := v.DecryptPrivateKey("ns", "test-key")
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if len(pem) == 0 {
		t.Error("decrypted PEM should not be empty")
	}
	if !strings.Contains(string(pem), "PRIVATE KEY") {
		t.Error("decrypted data should contain PRIVATE KEY")
	}
	_ = plaintext
}

func TestDecryptWhileLocked(t *testing.T) {
	v := openTestVault(t)
	v.Unlock("testpassword")

	v.GenerateKey("ns", "locked-key", "test@example.com")
	v.Lock()

	_, err := v.DecryptPrivateKey("ns", "locked-key")
	if err == nil {
		t.Error("expected error decrypting while locked")
	}
}

func TestGenerateWhileLocked(t *testing.T) {
	v := openTestVault(t)

	_, err := v.GenerateKey("ns", "fail-key", "test@example.com")
	if err == nil {
		t.Error("expected error generating while locked")
	}
}

func TestVaultSaltPersistence(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "keys.db")

	v1, _ := vault.Open(dbPath)
	v1.Unlock("password1")
	v1.GenerateKey("ns", "k1", "test@example.com")

	pem1, _ := v1.DecryptPrivateKey("ns", "k1")
	v1.Close()

	// Reopen and unlock with the same password — should decrypt the same key.
	v2, _ := vault.Open(dbPath)
	defer v2.Close()
	v2.Unlock("password1")

	pem2, err := v2.DecryptPrivateKey("ns", "k1")
	if err != nil {
		t.Fatalf("decrypt after reopen: %v", err)
	}
	if string(pem1) != string(pem2) {
		t.Error("decrypted key should be identical after reopen with same password")
	}
}

func TestWrongPasswordAfterReopen(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "keys.db")

	v1, _ := vault.Open(dbPath)
	v1.Unlock("correct-password")
	v1.GenerateKey("ns", "k1", "test@example.com")
	v1.Close()

	v2, _ := vault.Open(dbPath)
	defer v2.Close()
	v2.Unlock("wrong-password")

	_, err := v2.DecryptPrivateKey("ns", "k1")
	if err == nil {
		t.Error("expected error with wrong password")
	}
}

func TestGenerateKey(t *testing.T) {
	v := openTestVault(t)
	v.Unlock("testpassword")

	pubKey, err := v.GenerateKey("myns", "deploy-1", "admin@example.com")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.HasPrefix(pubKey, "ssh-ed25519 ") {
		t.Errorf("public key format: got %q", pubKey)
	}

	// Duplicate name should fail.
	_, err = v.GenerateKey("myns", "deploy-1", "admin@example.com")
	if err == nil {
		t.Error("duplicate key name should fail")
	}

	// Different namespace, same name is ok.
	_, err = v.GenerateKey("other-ns", "deploy-1", "admin@example.com")
	if err != nil {
		t.Errorf("same name in different namespace should work: %v", err)
	}
}

func TestListKeys(t *testing.T) {
	v := openTestVault(t)
	v.Unlock("testpassword")

	v.GenerateKey("ns", "key-a", "admin@example.com")
	v.GenerateKey("ns", "key-b", "admin@example.com")
	v.GenerateKey("other", "key-c", "admin@example.com")

	keys, err := v.ListKeys("ns")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Errorf("list ns: got %d keys, want 2", len(keys))
	}

	names := map[string]bool{}
	for _, k := range keys {
		names[k.Name] = true
		if k.Fingerprint == "" {
			t.Errorf("key %q has empty fingerprint", k.Name)
		}
		if !strings.HasPrefix(k.Fingerprint, "SHA256:") {
			t.Errorf("key %q fingerprint format: %q", k.Name, k.Fingerprint)
		}
	}
	if !names["key-a"] || !names["key-b"] {
		t.Error("expected key-a and key-b in listing")
	}

	// Empty namespace.
	keys, _ = v.ListKeys("nonexistent")
	if len(keys) != 0 {
		t.Errorf("nonexistent ns: got %d keys, want 0", len(keys))
	}
}

func TestListKeysWhileLocked(t *testing.T) {
	v := openTestVault(t)
	v.Unlock("testpassword")
	v.GenerateKey("ns", "k1", "admin@example.com")
	v.Lock()

	keys, err := v.ListKeys("ns")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 {
		t.Errorf("list while locked: got %d keys, want 1", len(keys))
	}
}

func TestDeleteKey(t *testing.T) {
	v := openTestVault(t)
	v.Unlock("testpassword")

	v.GenerateKey("ns", "del-me", "admin@example.com")
	if err := v.DeleteKey("ns", "del-me"); err != nil {
		t.Fatal(err)
	}

	keys, _ := v.ListKeys("ns")
	if len(keys) != 0 {
		t.Error("key should be deleted")
	}

	// Delete non-existent should error.
	if err := v.DeleteKey("ns", "nope"); err == nil {
		t.Error("expected error deleting non-existent key")
	}
}

func TestGetKey(t *testing.T) {
	v := openTestVault(t)
	v.Unlock("testpassword")

	v.GenerateKey("ns", "get-me", "admin@example.com")

	info, err := v.GetKey("ns", "get-me")
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "get-me" {
		t.Errorf("name = %q", info.Name)
	}
	if info.Algorithm != "ed25519" {
		t.Errorf("algorithm = %q", info.Algorithm)
	}

	_, err = v.GetKey("ns", "nope")
	if err == nil {
		t.Error("expected error for non-existent key")
	}
}
