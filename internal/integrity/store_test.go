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

func TestAgentWorkdirCRUD(t *testing.T) {
	s, err := integrity.Open(filepath.Join(t.TempDir(), "heads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	rec := integrity.AgentWorkdirRecord{
		Path:      "/tmp/gityard-agent-reviewer-abc",
		AgentName: "default_claude_reviewer",
		Role:      "reviewer",
		Namespace: "ns",
		Project:   "proj",
		PRNumber:  3,
		CreatedAt: "2026-06-28T12:00:00Z",
		Status:    "running",
	}

	if err := s.AddAgentWorkdir(rec); err != nil {
		t.Fatal(err)
	}

	recs, err := s.ListAgentWorkdirs()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 workdir, got %d", len(recs))
	}
	if recs[0].Status != "running" {
		t.Errorf("status = %q, want running", recs[0].Status)
	}

	if err := s.UpdateAgentWorkdir(rec.Path, "failed", 1); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetAgentWorkdir(rec.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected record, got nil")
	}
	if got.Status != "failed" {
		t.Errorf("status = %q, want failed", got.Status)
	}
	if got.ExitCode != 1 {
		t.Errorf("exit_code = %d, want 1", got.ExitCode)
	}

	if err := s.RemoveAgentWorkdir(rec.Path); err != nil {
		t.Fatal(err)
	}

	got, err = s.GetAgentWorkdir(rec.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Error("expected nil after removal")
	}
}

func TestAgentWorkdirUpdateNotFound(t *testing.T) {
	s, err := integrity.Open(filepath.Join(t.TempDir(), "heads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	err = s.UpdateAgentWorkdir("/nonexistent", "failed", 1)
	if err == nil {
		t.Error("expected error for non-existent workdir")
	}
}
