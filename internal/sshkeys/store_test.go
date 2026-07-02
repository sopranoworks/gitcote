package sshkeys_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"path/filepath"
	"testing"

	gossh "golang.org/x/crypto/ssh"

	"github.com/sopranoworks/gitcote/internal/sshkeys"
)

func TestAddAndLookup(t *testing.T) {
	s := openTestStore(t)

	pubKey, _ := generateTestKey(t)
	authorizedKey := string(gossh.MarshalAuthorizedKey(pubKey))

	fp, err := s.Add("user@test.com", authorizedKey, "my laptop")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if fp == "" {
		t.Fatal("fingerprint is empty")
	}

	email, found := s.LookupByKey(pubKey)
	if !found {
		t.Fatal("key not found after Add")
	}
	if email != "user@test.com" {
		t.Errorf("email = %q, want user@test.com", email)
	}
}

func TestLookupUnknownKey(t *testing.T) {
	s := openTestStore(t)

	pubKey, _ := generateTestKey(t)
	_, found := s.LookupByKey(pubKey)
	if found {
		t.Fatal("expected unknown key to not be found")
	}
}

func TestDuplicateAdd(t *testing.T) {
	s := openTestStore(t)

	pubKey, _ := generateTestKey(t)
	authorizedKey := string(gossh.MarshalAuthorizedKey(pubKey))

	if _, err := s.Add("user@test.com", authorizedKey, "key1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("user@test.com", authorizedKey, "key2"); err == nil {
		t.Fatal("expected duplicate add to fail")
	}
}

func TestListByUser(t *testing.T) {
	s := openTestStore(t)

	pub1, _ := generateTestKey(t)
	pub2, _ := generateTestKey(t)
	pub3, _ := generateTestKey(t)

	s.Add("alice@test.com", string(gossh.MarshalAuthorizedKey(pub1)), "key1")
	s.Add("alice@test.com", string(gossh.MarshalAuthorizedKey(pub2)), "key2")
	s.Add("bob@test.com", string(gossh.MarshalAuthorizedKey(pub3)), "key3")

	aliceKeys, err := s.ListByUser("alice@test.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(aliceKeys) != 2 {
		t.Errorf("alice has %d keys, want 2", len(aliceKeys))
	}

	bobKeys, _ := s.ListByUser("bob@test.com")
	if len(bobKeys) != 1 {
		t.Errorf("bob has %d keys, want 1", len(bobKeys))
	}
}

func TestDelete(t *testing.T) {
	s := openTestStore(t)

	pubKey, _ := generateTestKey(t)
	authorizedKey := string(gossh.MarshalAuthorizedKey(pubKey))

	fp, _ := s.Add("user@test.com", authorizedKey, "test")

	if err := s.Delete("other@test.com", fp); err == nil {
		t.Fatal("expected delete by wrong user to fail")
	}

	if err := s.Delete("user@test.com", fp); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, found := s.LookupByKey(pubKey)
	if found {
		t.Fatal("key should not be found after delete")
	}
}

func TestInvalidPublicKey(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.Add("user@test.com", "not a valid key", "test"); err == nil {
		t.Fatal("expected error for invalid key")
	}
}

func openTestStore(t *testing.T) *sshkeys.Store {
	t.Helper()
	s, err := sshkeys.Open(filepath.Join(t.TempDir(), "keys.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func generateTestKey(t *testing.T) (gossh.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return sshPub, priv
}
