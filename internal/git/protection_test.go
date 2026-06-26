package git_test

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"log/slog"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/sopranoworks/gityard/internal/git"
	"github.com/sopranoworks/shoka/pkg/auth"
	"github.com/sopranoworks/shoka/pkg/authz"
)

func TestParseRefUpdates(t *testing.T) {
	// Build a minimal receive-pack pkt-line stream with two ref updates.
	var buf bytes.Buffer
	line1 := fmt.Sprintf("%s %s refs/heads/main\x00report-status push-options\n",
		"0000000000000000000000000000000000000000",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	writePktLine(&buf, []byte(line1))
	line2 := fmt.Sprintf("%s %s refs/heads/feature\n",
		"0000000000000000000000000000000000000000",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	writePktLine(&buf, []byte(line2))
	writeFlush(&buf)

	updates := git.ParseRefUpdates(buf.Bytes())
	if len(updates) != 2 {
		t.Fatalf("got %d updates, want 2", len(updates))
	}
	if updates[0].RefName != "refs/heads/main" {
		t.Errorf("update[0].RefName = %q, want refs/heads/main", updates[0].RefName)
	}
	if updates[1].RefName != "refs/heads/feature" {
		t.Errorf("update[1].RefName = %q, want refs/heads/feature", updates[1].RefName)
	}
}

func TestCheckBranchProtection_RBlockedOnProtected(t *testing.T) {
	baseDir := t.TempDir()
	store := git.NewStore(baseDir)
	if err := store.CreateRepo("ns", "proj"); err != nil {
		t.Fatal(err)
	}
	repo, _ := store.OpenRepo("ns", "proj")

	cfg := &git.ProjectConfig{
		ProtectedBranches: []string{"main", "master"},
		ForcePush:         "deny",
	}

	// R-level push to protected branch should be rejected.
	updates := []git.RefUpdate{{
		OldHash: testZeroHash(),
		NewHash: testFakeHash("aa"),
		RefName: "refs/heads/main",
	}}
	err := git.CheckBranchProtection(repo, updates, cfg, authz.LevelRead, nil)
	if err == nil {
		t.Fatal("expected error for R-level push to protected branch")
	}
	if !strings.Contains(err.Error(), "protected branch") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCheckBranchProtection_RAllowedOnFeature(t *testing.T) {
	baseDir := t.TempDir()
	store := git.NewStore(baseDir)
	if err := store.CreateRepo("ns", "proj"); err != nil {
		t.Fatal(err)
	}
	repo, _ := store.OpenRepo("ns", "proj")

	cfg := git.DefaultProjectConfig()

	// R-level push to feature branch should succeed.
	updates := []git.RefUpdate{{
		OldHash: testZeroHash(),
		NewHash: testFakeHash("bb"),
		RefName: "refs/heads/feature-branch",
	}}
	if err := git.CheckBranchProtection(repo, updates, cfg, authz.LevelRead, nil); err != nil {
		t.Fatalf("R-level push to feature branch should be allowed: %v", err)
	}
}

func TestCheckBranchProtection_DeleteProtected(t *testing.T) {
	baseDir := t.TempDir()
	store := git.NewStore(baseDir)
	if err := store.CreateRepo("ns", "proj"); err != nil {
		t.Fatal(err)
	}
	repo, _ := store.OpenRepo("ns", "proj")

	cfg := git.DefaultProjectConfig()

	// Delete of protected branch should be rejected (even for admin).
	updates := []git.RefUpdate{{
		OldHash: testFakeHash("aa"),
		NewHash: testZeroHash(),
		RefName: "refs/heads/main",
	}}
	err := git.CheckBranchProtection(repo, updates, cfg, authz.LevelAdmin, nil)
	if err == nil {
		t.Fatal("expected error for delete of protected branch")
	}
	if !strings.Contains(err.Error(), "cannot delete") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCheckBranchProtection_AllowedBranches(t *testing.T) {
	baseDir := t.TempDir()
	store := git.NewStore(baseDir)
	if err := store.CreateRepo("ns", "proj"); err != nil {
		t.Fatal(err)
	}
	repo, _ := store.OpenRepo("ns", "proj")

	cfg := git.DefaultProjectConfig()
	allowed := []string{"task-42/"}

	// Push to matching prefix should succeed.
	updates := []git.RefUpdate{{
		OldHash: testZeroHash(),
		NewHash: testFakeHash("cc"),
		RefName: "refs/heads/task-42/impl",
	}}
	if err := git.CheckBranchProtection(repo, updates, cfg, authz.LevelRead, allowed); err != nil {
		t.Fatalf("push to allowed branch should succeed: %v", err)
	}

	// Push to non-matching prefix should fail.
	updates = []git.RefUpdate{{
		OldHash: testZeroHash(),
		NewHash: testFakeHash("dd"),
		RefName: "refs/heads/other-branch",
	}}
	err := git.CheckBranchProtection(repo, updates, cfg, authz.LevelRead, allowed)
	if err == nil {
		t.Fatal("expected error for push to disallowed branch")
	}
	if !strings.Contains(err.Error(), "token restricted") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAllowedBranchesFromExtra(t *testing.T) {
	// Nil map → nil.
	if got := git.AllowedBranchesFromExtra(nil); got != nil {
		t.Errorf("nil map: got %v, want nil", got)
	}

	// Empty key → nil.
	if got := git.AllowedBranchesFromExtra(map[string]any{}); got != nil {
		t.Errorf("empty map: got %v, want nil", got)
	}

	// Valid list.
	extra := map[string]any{
		"allowed_branches": []any{"task-42/", "fix-"},
	}
	got := git.AllowedBranchesFromExtra(extra)
	if len(got) != 2 || got[0] != "task-42/" || got[1] != "fix-" {
		t.Errorf("got %v, want [task-42/ fix-]", got)
	}
}

func TestProjectConfig_DefaultAndRoundTrip(t *testing.T) {
	dir := t.TempDir()

	// Default when no file exists.
	cfg, err := git.LoadProjectConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ProtectedBranches) != 1 || cfg.ProtectedBranches[0] != "main" {
		t.Errorf("default protected_branches = %v, want [main]", cfg.ProtectedBranches)
	}
	if cfg.ForcePush != "deny" {
		t.Errorf("default force_push = %q, want deny", cfg.ForcePush)
	}

	// Save and reload.
	cfg.ProtectedBranches = []string{"main", "release"}
	if err := git.SaveProjectConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := git.LoadProjectConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.ProtectedBranches) != 2 || loaded.ProtectedBranches[1] != "release" {
		t.Errorf("after roundtrip: %v", loaded.ProtectedBranches)
	}
}

func TestBranchProtection_E2E_RLevel(t *testing.T) {
	baseDir := t.TempDir()
	store := git.NewStore(baseDir)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	handler := git.NewHandler(store, logger)
	handler.PreReceive = func(namespace, project string, principal auth.Principal, refUpdates []git.RefUpdate) error {
		projPath, _ := store.ProjectPath(namespace, project)
		cfg, _ := git.LoadProjectConfig(projPath)
		effectiveLevel := authz.EffectiveLevel(authz.ParseScope(principal.Scope), namespace, project)
		allowed := git.AllowedBranchesFromExtra(principal.ExtraPermissions)
		repo, err := store.OpenRepo(namespace, project)
		if err != nil {
			return err
		}
		return git.CheckBranchProtection(repo, refUpdates, cfg, effectiveLevel, allowed)
	}

	rLevelValidate := func(token string) (auth.Principal, auth.RejectReason, bool) {
		if token == "r-token" {
			return auth.Principal{
				Name:  "coder",
				Email: "coder@test.com",
				Scope: "namespace:ns/proj:r",
			}, "", true
		}
		if token == "w-token" {
			return auth.Principal{
				Name:  "dev",
				Email: "dev@test.com",
				Scope: "namespace:ns/proj:rw",
			}, "", true
		}
		if token == "branch-token" {
			return auth.Principal{
				Name:  "agent",
				Email: "agent@test.com",
				Scope: "namespace:ns/proj:r",
				ExtraPermissions: map[string]any{
					"allowed_branches": []any{"task-42/"},
				},
			}, "", true
		}
		return auth.Principal{}, auth.ReasonInvalidToken, false
	}

	mux := http.NewServeMux()
	mux.Handle("/", git.BasicAuthMiddleware(rLevelValidate)(handler))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	if err := store.CreateRepo("ns", "proj"); err != nil {
		t.Fatal(err)
	}

	// W-level: push to main → succeeds.
	cloneDir := t.TempDir()
	cloneURL := fmt.Sprintf("http://x-token:w-token@%s/ns/proj.git", ts.Listener.Addr().String())
	runGit(t, cloneDir, "clone", cloneURL, "w-repo")
	wRepoDir := filepath.Join(cloneDir, "w-repo")
	os.WriteFile(filepath.Join(wRepoDir, "init.txt"), []byte("init"), 0o644)
	runGit(t, wRepoDir, "add", "init.txt")
	runGit(t, wRepoDir, "commit", "-m", "init")
	runGit(t, wRepoDir, "push", "-u", "origin", "HEAD:refs/heads/main")

	// R-level: push to feature branch → succeeds.
	rDir := t.TempDir()
	rCloneURL := fmt.Sprintf("http://x-token:r-token@%s/ns/proj.git", ts.Listener.Addr().String())
	runGit(t, rDir, "clone", rCloneURL, "r-repo")
	rRepoDir := filepath.Join(rDir, "r-repo")
	runGit(t, rRepoDir, "checkout", "-b", "feature-branch")
	os.WriteFile(filepath.Join(rRepoDir, "feature.txt"), []byte("feature"), 0o644)
	runGit(t, rRepoDir, "add", "feature.txt")
	runGit(t, rRepoDir, "commit", "-m", "feature commit")
	runGit(t, rRepoDir, "push", "-u", "origin", "HEAD:refs/heads/feature-branch")

	// R-level: push to main → rejected (403).
	err := runGitResult(rRepoDir, "push", "origin", "HEAD:refs/heads/main")
	if err == nil {
		t.Fatal("R-level push to main should be rejected")
	}

	// W-level: force push to main → rejected (403).
	os.WriteFile(filepath.Join(wRepoDir, "amend.txt"), []byte("amend"), 0o644)
	runGit(t, wRepoDir, "add", "amend.txt")
	runGit(t, wRepoDir, "commit", "--amend", "-m", "amended")
	err = runGitResult(wRepoDir, "push", "--force", "origin", "HEAD:refs/heads/main")
	if err == nil {
		t.Fatal("force push to main should be rejected")
	}

	// W-level: delete main → rejected (403).
	err = runGitResult(wRepoDir, "push", "origin", "--delete", "main")
	if err == nil {
		t.Fatal("delete main should be rejected")
	}

	// Branch-restricted token: push to task-42/impl → succeeds.
	bDir := t.TempDir()
	bCloneURL := fmt.Sprintf("http://x-token:branch-token@%s/ns/proj.git", ts.Listener.Addr().String())
	runGit(t, bDir, "clone", bCloneURL, "b-repo")
	bRepoDir := filepath.Join(bDir, "b-repo")
	runGit(t, bRepoDir, "checkout", "-b", "task-42/impl")
	os.WriteFile(filepath.Join(bRepoDir, "task.txt"), []byte("task"), 0o644)
	runGit(t, bRepoDir, "add", "task.txt")
	runGit(t, bRepoDir, "commit", "-m", "task commit")
	runGit(t, bRepoDir, "push", "-u", "origin", "HEAD:refs/heads/task-42/impl")

	// Branch-restricted token: push to other-branch → rejected (403).
	runGit(t, bRepoDir, "checkout", "-b", "other-branch")
	os.WriteFile(filepath.Join(bRepoDir, "other.txt"), []byte("other"), 0o644)
	runGit(t, bRepoDir, "add", "other.txt")
	runGit(t, bRepoDir, "commit", "-m", "other commit")
	err = runGitResult(bRepoDir, "push", "origin", "HEAD:refs/heads/other-branch")
	if err == nil {
		t.Fatal("push to disallowed branch should be rejected")
	}
}

func TestBranchProtection_PRFromRLevel(t *testing.T) {
	baseDir := t.TempDir()
	store := git.NewStore(baseDir)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	var capturedPushOpts []string
	handler := git.NewHandler(store, logger)
	handler.PreReceive = func(namespace, project string, principal auth.Principal, refUpdates []git.RefUpdate) error {
		projPath, _ := store.ProjectPath(namespace, project)
		cfg, _ := git.LoadProjectConfig(projPath)
		effectiveLevel := authz.EffectiveLevel(authz.ParseScope(principal.Scope), namespace, project)
		repo, err := store.OpenRepo(namespace, project)
		if err != nil {
			return err
		}
		return git.CheckBranchProtection(repo, refUpdates, cfg, effectiveLevel, nil)
	}
	handler.PostReceive = func(ns, proj string, p auth.Principal, pushOpts []string) {
		capturedPushOpts = pushOpts
	}

	rLevelValidate := func(token string) (auth.Principal, auth.RejectReason, bool) {
		if token == "r-token" {
			return auth.Principal{
				Name:  "coder",
				Email: "coder@test.com",
				Scope: "namespace:ns/proj:r",
			}, "", true
		}
		if token == "w-token" {
			return auth.Principal{
				Name:  "dev",
				Email: "dev@test.com",
				Scope: "*",
			}, "", true
		}
		return auth.Principal{}, auth.ReasonInvalidToken, false
	}

	mux := http.NewServeMux()
	mux.Handle("/", git.BasicAuthMiddleware(rLevelValidate)(handler))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	if err := store.CreateRepo("ns", "proj"); err != nil {
		t.Fatal(err)
	}

	// Setup: W-level push initial commit to main.
	wDir := t.TempDir()
	wURL := fmt.Sprintf("http://x-token:w-token@%s/ns/proj.git", ts.Listener.Addr().String())
	runGit(t, wDir, "clone", wURL, "w-repo")
	wRepoDir := filepath.Join(wDir, "w-repo")
	os.WriteFile(filepath.Join(wRepoDir, "init.txt"), []byte("init"), 0o644)
	runGit(t, wRepoDir, "add", "init.txt")
	runGit(t, wRepoDir, "commit", "-m", "init")
	runGit(t, wRepoDir, "push", "-u", "origin", "HEAD:refs/heads/main")

	// R-level: push to feature branch with PR push options → succeeds.
	rDir := t.TempDir()
	rURL := fmt.Sprintf("http://x-token:r-token@%s/ns/proj.git", ts.Listener.Addr().String())
	runGit(t, rDir, "clone", rURL, "r-repo")
	rRepoDir := filepath.Join(rDir, "r-repo")
	runGit(t, rRepoDir, "checkout", "-b", "feature-pr")
	os.WriteFile(filepath.Join(rRepoDir, "pr.txt"), []byte("pr content"), 0o644)
	runGit(t, rRepoDir, "add", "pr.txt")
	runGit(t, rRepoDir, "commit", "-m", "pr commit")
	runGit(t, rRepoDir, "push", "-u", "origin", "HEAD:refs/heads/feature-pr",
		"-o", "pull_request.create", "-o", "pull_request.target=main")

	if len(capturedPushOpts) == 0 {
		t.Fatal("expected push options to be captured")
	}
	found := false
	for _, o := range capturedPushOpts {
		if o == "pull_request.create" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected pull_request.create in push options, got: %v", capturedPushOpts)
	}
}

// Helpers

func testZeroHash() plumbing.Hash { return plumbing.ZeroHash }

func testFakeHash(hex string) plumbing.Hash {
	return plumbing.NewHash(strings.Repeat(hex, 20))
}

func writePktLine(buf *bytes.Buffer, data []byte) {
	l := len(data) + 4
	buf.WriteString(fmt.Sprintf("%04x", l))
	buf.Write(data)
}

func writeFlush(buf *bytes.Buffer) {
	buf.WriteString("0000")
}
