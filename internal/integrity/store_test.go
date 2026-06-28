package integrity_test

import (
	"path/filepath"
	"testing"

	"github.com/sopranoworks/gityard/internal/integrity"
)

func TestStoreGetSetRoundTrip(t *testing.T) {
	s, err := integrity.Open(filepath.Join(t.TempDir(), "heads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	hash, err := s.Get("ns", "proj")
	if err != nil {
		t.Fatal(err)
	}
	if hash != "" {
		t.Fatalf("expected empty hash for new key, got %q", hash)
	}

	if err := s.Set("ns", "proj", "abc123"); err != nil {
		t.Fatal(err)
	}

	hash, err = s.Get("ns", "proj")
	if err != nil {
		t.Fatal(err)
	}
	if hash != "abc123" {
		t.Fatalf("expected %q, got %q", "abc123", hash)
	}

	if err := s.Set("ns", "proj", "def456"); err != nil {
		t.Fatal(err)
	}
	hash, _ = s.Get("ns", "proj")
	if hash != "def456" {
		t.Fatalf("expected updated hash %q, got %q", "def456", hash)
	}
}

func TestStoreIsolation(t *testing.T) {
	s, err := integrity.Open(filepath.Join(t.TempDir(), "heads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	_ = s.Set("ns1", "proj1", "aaa")
	_ = s.Set("ns2", "proj2", "bbb")

	h1, _ := s.Get("ns1", "proj1")
	h2, _ := s.Get("ns2", "proj2")
	if h1 != "aaa" || h2 != "bbb" {
		t.Fatalf("isolation broken: got %q %q", h1, h2)
	}
}
