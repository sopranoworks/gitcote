package git_test

import (
	"os"
	"path/filepath"
	"testing"

	gogit "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/sopranoworks/gitcote/internal/git"
)

func TestSeedConfigPersistence(t *testing.T) {
	baseDir := t.TempDir()
	store := git.NewStore(baseDir)
	store.CreateRepo("ns", "cfg-test")
	projPath, _ := store.ProjectPath("ns", "cfg-test")

	cfg := &git.SeedConfig{
		SeedURL:      "git@github.com:org/repo.git",
		KeyName:      "github-deploy",
		PushMode:     git.PushModeOnMerge,
		PushInterval: "6h",
	}
	if err := git.SaveSeedConfig(projPath, cfg); err != nil {
		t.Fatal(err)
	}

	loaded, err := git.LoadSeedConfig(projPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SeedURL != cfg.SeedURL {
		t.Errorf("seed URL = %q, want %q", loaded.SeedURL, cfg.SeedURL)
	}
	if loaded.PushMode != cfg.PushMode {
		t.Errorf("push mode = %q, want %q", loaded.PushMode, cfg.PushMode)
	}
	if loaded.KeyName != cfg.KeyName {
		t.Errorf("key name = %q, want %q", loaded.KeyName, cfg.KeyName)
	}
	if loaded.PushInterval != cfg.PushInterval {
		t.Errorf("push interval = %q, want %q", loaded.PushInterval, cfg.PushInterval)
	}
}

func TestSeedConfigDefault(t *testing.T) {
	baseDir := t.TempDir()
	store := git.NewStore(baseDir)
	store.CreateRepo("ns", "default-test")
	projPath, _ := store.ProjectPath("ns", "default-test")

	cfg, err := git.LoadSeedConfig(projPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PushMode != git.PushModeDisabled {
		t.Errorf("default push mode = %q, want %q", cfg.PushMode, git.PushModeDisabled)
	}
}

func TestSeedSyncStatus(t *testing.T) {
	baseDir := t.TempDir()
	store := git.NewStore(baseDir)
	store.CreateRepo("ns", "status-test")
	projPath, _ := store.ProjectPath("ns", "status-test")

	git.SaveSeedConfig(projPath, &git.SeedConfig{
		SeedURL:  "git@github.com:org/repo.git",
		PushMode: git.PushModeOnMerge,
	})

	err := git.UpdateSeedStatus(projPath, &git.SeedSyncStatus{
		State:      git.SeedStateActive,
		LastResult: "ok",
	})
	if err != nil {
		t.Fatal(err)
	}

	cfg, _ := git.LoadSeedConfig(projPath)
	if cfg.SyncStatus == nil {
		t.Fatal("sync status should not be nil")
	}
	if cfg.SyncStatus.State != git.SeedStateActive {
		t.Errorf("state = %q, want %q", cfg.SyncStatus.State, git.SeedStateActive)
	}
}

func TestPushToSeedLocalBare(t *testing.T) {
	baseDir := t.TempDir()
	store := git.NewStore(baseDir)
	store.CreateRepo("ns", "push-src")
	srcPath, _ := store.ProjectPath("ns", "push-src")

	os.WriteFile(filepath.Join(srcPath, "hello.txt"), []byte("hello\n"), 0o644)
	runGit(t, srcPath, "add", ".")
	runGit(t, srcPath, "commit", "-m", "initial")

	seedDir := t.TempDir()
	seedPath := filepath.Join(seedDir, "seed.git")
	runGit(t, seedDir, "init", "--bare", seedPath)
	runGit(t, seedPath, "symbolic-ref", "HEAD", "refs/heads/main")

	repo, _ := store.OpenRepo("ns", "push-src")

	_, err := repo.CreateRemote(&config.RemoteConfig{
		Name: "seed",
		URLs: []string{seedPath},
	})
	if err != nil {
		t.Fatal(err)
	}

	err = repo.Push(&gogit.PushOptions{
		RemoteName: "seed",
		RefSpecs:   []config.RefSpec{"refs/heads/main:refs/heads/main"},
	})
	if err != nil {
		t.Fatalf("push to seed: %v", err)
	}

	verifyDir := t.TempDir()
	runGit(t, verifyDir, "clone", seedPath, "verify")
	content, err := os.ReadFile(filepath.Join(verifyDir, "verify", "hello.txt"))
	if err != nil {
		t.Fatalf("read from seed clone: %v", err)
	}
	if string(content) != "hello\n" {
		t.Errorf("seed content = %q, want %q", string(content), "hello\n")
	}
}
