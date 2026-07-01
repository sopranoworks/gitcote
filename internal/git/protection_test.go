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
	"github.com/go-git/go-git/v6/storage"
	"github.com/sopranoworks/gityard/internal/git"
	"github.com/sopranoworks/shoka/pkg/auth"
	"github.com/sopranoworks/shoka/pkg/authz"
)

func TestParseRefUpdates(t *testing.T) {
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

func TestIsDefaultBranch(t *testing.T) {
	baseDir := t.TempDir()
	store := git.NewStore(baseDir)
	if err := store.CreateRepo("ns", "proj"); err != nil {
		t.Fatal(err)
	}

	// Empty repo with no HEAD target → falls back to "main".
	repo, _ := store.OpenRepo("ns", "proj")
	if !git.IsDefaultBranch(repo, "main") {
		t.Error("expected main to be default branch (fallback)")
	}
	if git.IsDefaultBranch(repo, "feature") {
		t.Error("expected feature NOT to be default branch")
	}
}

func TestCheckBranchProtection_RBlockedOnDefault(t *testing.T) {
	baseDir := t.TempDir()
	store := git.NewStore(baseDir)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	handler := git.NewHandler(store, logger)
	mux := http.NewServeMux()
	mux.Handle("/", handler)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	if err := store.CreateRepo("ns", "proj"); err != nil {
		t.Fatal(err)
	}

	// Push initial commit to set up HEAD → main.
	cloneDir := t.TempDir()
	runGit(t, cloneDir, "clone", ts.URL+"/ns/proj.git", "repo")
	repoDir := filepath.Join(cloneDir, "repo")
	os.WriteFile(filepath.Join(repoDir, "init.txt"), []byte("init"), 0o644)
	runGit(t, repoDir, "add", "init.txt")
	runGit(t, repoDir, "commit", "-m", "init")
	runGit(t, repoDir, "push", "-u", "origin", "HEAD:refs/heads/main")

	// Now check against the repo with a real HEAD.
	repo, _ := store.OpenRepo("ns", "proj")

	updates := []git.RefUpdate{{
		OldHash: testZeroHash(),
		NewHash: testFakeHash("aa"),
		RefName: "refs/heads/main",
	}}
	err := git.CheckBranchProtection(repo, updates, authz.LevelRead, nil)
	if err == nil {
		t.Fatal("expected error for R-level push to default branch")
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

	updates := []git.RefUpdate{{
		OldHash: testZeroHash(),
		NewHash: testFakeHash("bb"),
		RefName: "refs/heads/feature-branch",
	}}
	if err := git.CheckBranchProtection(repo, updates, authz.LevelRead, nil); err != nil {
		t.Fatalf("R-level push to feature branch should be allowed: %v", err)
	}
}

func TestCheckBranchProtection_DeleteDefault(t *testing.T) {
	baseDir := t.TempDir()
	store := git.NewStore(baseDir)
	if err := store.CreateRepo("ns", "proj"); err != nil {
		t.Fatal(err)
	}
	repo, _ := store.OpenRepo("ns", "proj")

	updates := []git.RefUpdate{{
		OldHash: testFakeHash("aa"),
		NewHash: testZeroHash(),
		RefName: "refs/heads/main",
	}}
	err := git.CheckBranchProtection(repo, updates, authz.LevelAdmin, nil)
	if err == nil {
		t.Fatal("expected error for delete of default branch")
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

	allowed := []string{"task-42/"}

	updates := []git.RefUpdate{{
		OldHash: testZeroHash(),
		NewHash: testFakeHash("cc"),
		RefName: "refs/heads/task-42/impl",
	}}
	if err := git.CheckBranchProtection(repo, updates, authz.LevelRead, allowed); err != nil {
		t.Fatalf("push to allowed branch should succeed: %v", err)
	}

	updates = []git.RefUpdate{{
		OldHash: testZeroHash(),
		NewHash: testFakeHash("dd"),
		RefName: "refs/heads/other-branch",
	}}
	err := git.CheckBranchProtection(repo, updates, authz.LevelRead, allowed)
	if err == nil {
		t.Fatal("expected error for push to disallowed branch")
	}
	if !strings.Contains(err.Error(), "token restricted") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestMatchesAllowedBranches(t *testing.T) {
	if !git.MatchesAllowedBranches("task-42/impl", []string{"task-42/"}) {
		t.Error("task-42/impl should match prefix task-42/")
	}
	if !git.MatchesAllowedBranches("task-42/fix", []string{"task-42/"}) {
		t.Error("task-42/fix should match prefix task-42/")
	}
	if git.MatchesAllowedBranches("main", []string{"task-42/"}) {
		t.Error("main should NOT match prefix task-42/")
	}
	if git.MatchesAllowedBranches("other", []string{"task-42/"}) {
		t.Error("other should NOT match prefix task-42/")
	}
}

func TestAllowedBranchesFromExtra(t *testing.T) {
	if got := git.AllowedBranchesFromExtra(nil); got != nil {
		t.Errorf("nil map: got %v, want nil", got)
	}
	if got := git.AllowedBranchesFromExtra(map[string]any{}); got != nil {
		t.Errorf("empty map: got %v, want nil", got)
	}
	extra := map[string]any{
		"allowed_branches": []any{"task-42/", "fix-"},
	}
	got := git.AllowedBranchesFromExtra(extra)
	if len(got) != 2 || got[0] != "task-42/" || got[1] != "fix-" {
		t.Errorf("got %v, want [task-42/ fix-]", got)
	}
}

func TestDeleteBranchRef(t *testing.T) {
	baseDir := t.TempDir()
	store := git.NewStore(baseDir)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	handler := git.NewHandler(store, logger)
	mux := http.NewServeMux()
	mux.Handle("/", handler)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	if err := store.CreateRepo("ns", "proj"); err != nil {
		t.Fatal(err)
	}

	// Push main and feature branch.
	cloneDir := t.TempDir()
	runGit(t, cloneDir, "clone", ts.URL+"/ns/proj.git", "repo")
	repoDir := filepath.Join(cloneDir, "repo")
	os.WriteFile(filepath.Join(repoDir, "init.txt"), []byte("init"), 0o644)
	runGit(t, repoDir, "add", "init.txt")
	runGit(t, repoDir, "commit", "-m", "init")
	runGit(t, repoDir, "push", "-u", "origin", "HEAD:refs/heads/main")
	runGit(t, repoDir, "checkout", "-b", "feature")
	os.WriteFile(filepath.Join(repoDir, "f.txt"), []byte("feature"), 0o644)
	runGit(t, repoDir, "add", "f.txt")
	runGit(t, repoDir, "commit", "-m", "feature")
	runGit(t, repoDir, "push", "-u", "origin", "HEAD:refs/heads/feature")

	repo, _ := store.OpenRepo("ns", "proj")
	branches, _ := git.ListBranches(repo)
	if len(branches) != 2 {
		t.Fatalf("expected 2 branches, got %d: %v", len(branches), branches)
	}

	if err := git.DeleteBranchRef(repo, "feature"); err != nil {
		t.Fatalf("DeleteBranchRef: %v", err)
	}

	branches, _ = git.ListBranches(repo)
	if len(branches) != 1 {
		t.Fatalf("expected 1 branch after delete, got %d: %v", len(branches), branches)
	}
	if branches[0] != "main" {
		t.Errorf("remaining branch = %q, want main", branches[0])
	}
}

func TestBranchProtection_E2E_RLevel(t *testing.T) {
	baseDir := t.TempDir()
	store := git.NewStore(baseDir)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	handler := git.NewHandler(store, logger)
	handler.PreReceive = func(namespace, project string, principal auth.Principal, refUpdates []git.RefUpdate) error {
		effectiveLevel := git.EffectiveGitLevel(principal.Scope, namespace, project)
		allowed := git.AllowedBranchesFromExtra(principal.ExtraPermissions)
		repo, err := store.OpenRepo(namespace, project)
		if err != nil {
			return err
		}
		return git.CheckBranchProtection(repo, refUpdates, effectiveLevel, allowed)
	}
	handler.ProtectStorer = func(namespace, project string, principal auth.Principal, st storage.Storer) storage.Storer {
		repo, err := store.OpenRepo(namespace, project)
		if err != nil {
			return st
		}
		return &git.ProtectedStorer{
			Storer:  st,
			Repo:    repo,
			Level:   git.EffectiveGitLevel(principal.Scope, namespace, project),
			Allowed: git.AllowedBranchesFromExtra(principal.ExtraPermissions),
		}
	}

	validate := func(token string) (auth.Principal, auth.RejectReason, bool) {
		if token == "r-token" {
			return auth.Principal{
				Name:  "coder",
				Email: "coder@test.com",
				Scope: "git/ns:proj:r",
			}, "", true
		}
		if token == "w-token" {
			return auth.Principal{
				Name:  "dev",
				Email: "dev@test.com",
				Scope: "git/ns:proj:rw",
			}, "", true
		}
		if token == "branch-token" {
			return auth.Principal{
				Name:  "agent",
				Email: "agent@test.com",
				Scope: "git/ns:proj:rw",
				ExtraPermissions: map[string]any{
					"allowed_branches": []any{"task-42/"},
				},
			}, "", true
		}
		if token == "mcp-only-token" {
			return auth.Principal{
				Name:  "reviewer",
				Email: "reviewer@test.com",
				Scope: "ns:proj:rw",
			}, "", true
		}
		return auth.Principal{}, auth.ReasonInvalidToken, false
	}

	mux := http.NewServeMux()
	mux.Handle("/", git.BasicAuthMiddleware(validate)(handler))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	if err := store.CreateRepo("ns", "proj"); err != nil {
		t.Fatal(err)
	}

	// RW git-zone: push to main → succeeds.
	cloneDir := t.TempDir()
	cloneURL := fmt.Sprintf("http://x-token:w-token@%s/ns/proj.git", ts.Listener.Addr().String())
	runGit(t, cloneDir, "clone", cloneURL, "w-repo")
	wRepoDir := filepath.Join(cloneDir, "w-repo")
	os.WriteFile(filepath.Join(wRepoDir, "init.txt"), []byte("init"), 0o644)
	runGit(t, wRepoDir, "add", "init.txt")
	runGit(t, wRepoDir, "commit", "-m", "init")
	runGit(t, wRepoDir, "push", "-u", "origin", "HEAD:refs/heads/main")

	// R git-zone: push to feature branch → rejected (r-scoped tokens cannot push).
	rDir := t.TempDir()
	rCloneURL := fmt.Sprintf("http://x-token:r-token@%s/ns/proj.git", ts.Listener.Addr().String())
	runGit(t, rDir, "clone", rCloneURL, "r-repo")
	rRepoDir := filepath.Join(rDir, "r-repo")
	runGit(t, rRepoDir, "checkout", "-b", "feature-branch")
	os.WriteFile(filepath.Join(rRepoDir, "feature.txt"), []byte("feature"), 0o644)
	runGit(t, rRepoDir, "add", "feature.txt")
	runGit(t, rRepoDir, "commit", "-m", "feature commit")
	err := runGitResult(rRepoDir, "push", "-u", "origin", "HEAD:refs/heads/feature-branch")
	if err == nil {
		t.Fatal("R-level push to feature branch should be rejected")
	}

	// R git-zone: push to main → rejected.
	err = runGitResult(rRepoDir, "push", "origin", "HEAD:refs/heads/main")
	if err == nil {
		t.Fatal("R-level push to main should be rejected")
	}

	// MCP-only token: clone → rejected (no git/ zone).
	mcpDir := t.TempDir()
	mcpCloneURL := fmt.Sprintf("http://x-token:mcp-only-token@%s/ns/proj.git", ts.Listener.Addr().String())
	err = runGitResult(mcpDir, "clone", mcpCloneURL, "mcp-repo")
	if err == nil {
		t.Fatal("MCP-only (unzoned) token should be denied git clone")
	}

	// RW git-zone: force push to main → rejected (403).
	os.WriteFile(filepath.Join(wRepoDir, "amend.txt"), []byte("amend"), 0o644)
	runGit(t, wRepoDir, "add", "amend.txt")
	runGit(t, wRepoDir, "commit", "--amend", "-m", "amended")
	err = runGitResult(wRepoDir, "push", "--force", "origin", "HEAD:refs/heads/main")
	if err == nil {
		t.Fatal("force push to main should be rejected")
	}

	// RW git-zone: delete main → rejected (403).
	err = runGitResult(wRepoDir, "push", "origin", "--delete", "main")
	if err == nil {
		t.Fatal("delete main should be rejected")
	}

	// Branch-restricted git-zone token (rw): push to task-42/impl → succeeds.
	bDir := t.TempDir()
	bCloneURL := fmt.Sprintf("http://x-token:branch-token@%s/ns/proj.git", ts.Listener.Addr().String())
	runGit(t, bDir, "clone", bCloneURL, "b-repo")
	bRepoDir := filepath.Join(bDir, "b-repo")
	runGit(t, bRepoDir, "checkout", "-b", "task-42/impl")
	os.WriteFile(filepath.Join(bRepoDir, "task.txt"), []byte("task"), 0o644)
	runGit(t, bRepoDir, "add", "task.txt")
	runGit(t, bRepoDir, "commit", "-m", "task commit")
	runGit(t, bRepoDir, "push", "-u", "origin", "HEAD:refs/heads/task-42/impl")

	// Branch-restricted git-zone token (rw): push to other-branch → rejected (403).
	runGit(t, bRepoDir, "checkout", "-b", "other-branch")
	os.WriteFile(filepath.Join(bRepoDir, "other.txt"), []byte("other"), 0o644)
	runGit(t, bRepoDir, "add", "other.txt")
	runGit(t, bRepoDir, "commit", "-m", "other commit")
	err = runGitResult(bRepoDir, "push", "origin", "HEAD:refs/heads/other-branch")
	if err == nil {
		t.Fatal("push to disallowed branch should be rejected")
	}
}

func TestBranchProtection_PRPushOptions(t *testing.T) {
	baseDir := t.TempDir()
	store := git.NewStore(baseDir)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	var capturedPushOpts []string
	handler := git.NewHandler(store, logger)
	handler.PreReceive = func(namespace, project string, principal auth.Principal, refUpdates []git.RefUpdate) error {
		effectiveLevel := git.EffectiveGitLevel(principal.Scope, namespace, project)
		repo, err := store.OpenRepo(namespace, project)
		if err != nil {
			return err
		}
		return git.CheckBranchProtection(repo, refUpdates, effectiveLevel, nil)
	}
	handler.ProtectStorer = func(namespace, project string, principal auth.Principal, st storage.Storer) storage.Storer {
		repo, err := store.OpenRepo(namespace, project)
		if err != nil {
			return st
		}
		return &git.ProtectedStorer{
			Storer:  st,
			Repo:    repo,
			Level:   git.EffectiveGitLevel(principal.Scope, namespace, project),
			Allowed: git.AllowedBranchesFromExtra(principal.ExtraPermissions),
		}
	}
	handler.PostReceive = func(ns, proj string, p auth.Principal, pushOpts []string) {
		capturedPushOpts = pushOpts
	}

	validate := func(token string) (auth.Principal, auth.RejectReason, bool) {
		if token == "rw-token" {
			return auth.Principal{
				Name:  "coder",
				Email: "coder@test.com",
				Scope: "git/ns:proj:rw",
			}, "", true
		}
		if token == "admin-token" {
			return auth.Principal{
				Name:  "dev",
				Email: "dev@test.com",
				Scope: "*",
			}, "", true
		}
		return auth.Principal{}, auth.ReasonInvalidToken, false
	}

	mux := http.NewServeMux()
	mux.Handle("/", git.BasicAuthMiddleware(validate)(handler))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	if err := store.CreateRepo("ns", "proj"); err != nil {
		t.Fatal(err)
	}

	wDir := t.TempDir()
	wURL := fmt.Sprintf("http://x-token:admin-token@%s/ns/proj.git", ts.Listener.Addr().String())
	runGit(t, wDir, "clone", wURL, "w-repo")
	wRepoDir := filepath.Join(wDir, "w-repo")
	os.WriteFile(filepath.Join(wRepoDir, "init.txt"), []byte("init"), 0o644)
	runGit(t, wRepoDir, "add", "init.txt")
	runGit(t, wRepoDir, "commit", "-m", "init")
	runGit(t, wRepoDir, "push", "-u", "origin", "HEAD:refs/heads/main")

	rwDir := t.TempDir()
	rwURL := fmt.Sprintf("http://x-token:rw-token@%s/ns/proj.git", ts.Listener.Addr().String())
	runGit(t, rwDir, "clone", rwURL, "rw-repo")
	rwRepoDir := filepath.Join(rwDir, "rw-repo")
	runGit(t, rwRepoDir, "checkout", "-b", "feature-pr")
	os.WriteFile(filepath.Join(rwRepoDir, "pr.txt"), []byte("pr content"), 0o644)
	runGit(t, rwRepoDir, "add", "pr.txt")
	runGit(t, rwRepoDir, "commit", "-m", "pr commit")
	runGit(t, rwRepoDir, "push", "-u", "origin", "HEAD:refs/heads/feature-pr",
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

func TestBranchRestriction_DirectiveVerification(t *testing.T) {
	baseDir := t.TempDir()
	store := git.NewStore(baseDir)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	handler := git.NewHandler(store, logger)
	handler.PreReceive = func(namespace, project string, principal auth.Principal, refUpdates []git.RefUpdate) error {
		effectiveLevel := git.EffectiveGitLevel(principal.Scope, namespace, project)
		allowed := git.AllowedBranchesFromExtra(principal.ExtraPermissions)
		repo, err := store.OpenRepo(namespace, project)
		if err != nil {
			return err
		}
		return git.CheckBranchProtection(repo, refUpdates, effectiveLevel, allowed)
	}
	handler.ProtectStorer = func(namespace, project string, principal auth.Principal, st storage.Storer) storage.Storer {
		repo, err := store.OpenRepo(namespace, project)
		if err != nil {
			return st
		}
		return &git.ProtectedStorer{
			Storer:  st,
			Repo:    repo,
			Level:   git.EffectiveGitLevel(principal.Scope, namespace, project),
			Allowed: git.AllowedBranchesFromExtra(principal.ExtraPermissions),
		}
	}

	validate := func(token string) (auth.Principal, auth.RejectReason, bool) {
		switch token {
		case "admin-token":
			return auth.Principal{
				Name: "admin", Email: "admin@test.com",
				Scope: "*",
			}, "", true
		case "rw-feat-token":
			return auth.Principal{
				Name: "agent", Email: "agent@test.com",
				Scope:            "git/test:prtest:rw",
				ExtraPermissions: map[string]any{"allowed_branches": []any{"feat/"}},
			}, "", true
		case "rw-task1-token":
			return auth.Principal{
				Name: "agent", Email: "agent@test.com",
				Scope:            "git/test:prtest:rw",
				ExtraPermissions: map[string]any{"allowed_branches": []any{"task-1/"}},
			}, "", true
		case "rw-noprefix-token":
			return auth.Principal{
				Name: "agent", Email: "agent@test.com",
				Scope: "git/test:prtest:rw",
			}, "", true
		case "r-feat-token":
			return auth.Principal{
				Name: "agent", Email: "agent@test.com",
				Scope:            "git/test:prtest:r",
				ExtraPermissions: map[string]any{"allowed_branches": []any{"feat/"}},
			}, "", true
		case "mcp-only-token":
			return auth.Principal{
				Name: "reviewer", Email: "reviewer@test.com",
				Scope: "test:prtest:rw",
			}, "", true
		}
		return auth.Principal{}, auth.ReasonInvalidToken, false
	}

	mux := http.NewServeMux()
	mux.Handle("/", git.BasicAuthMiddleware(validate)(handler))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	if err := store.CreateRepo("test", "prtest"); err != nil {
		t.Fatal(err)
	}

	addr := ts.Listener.Addr().String()
	cloneURL := func(token string) string {
		return fmt.Sprintf("http://x-token:%s@%s/test/prtest.git", token, addr)
	}

	// Set up main branch with admin token.
	setupDir := t.TempDir()
	runGit(t, setupDir, "clone", cloneURL("admin-token"), "setup")
	setupRepo := filepath.Join(setupDir, "setup")
	os.WriteFile(filepath.Join(setupRepo, "init.txt"), []byte("init"), 0o644)
	runGit(t, setupRepo, "add", "init.txt")
	runGit(t, setupRepo, "commit", "-m", "init")
	runGit(t, setupRepo, "push", "-u", "origin", "HEAD:refs/heads/main")

	preparePush := func(t *testing.T, token, branch, filename string) string {
		t.Helper()
		dir := t.TempDir()
		runGit(t, dir, "clone", cloneURL(token), "repo")
		repoDir := filepath.Join(dir, "repo")
		runGit(t, repoDir, "checkout", "-b", branch)
		os.WriteFile(filepath.Join(repoDir, filename), []byte(filename), 0o644)
		runGit(t, repoDir, "add", filename)
		runGit(t, repoDir, "commit", "-m", "commit "+filename)
		return repoDir
	}

	// === Case 1: rw token with allowed_branches: ["feat/"] ===
	t.Run("case1_feat_prefix", func(t *testing.T) {
		// Push to feat/hello → allowed
		dir := preparePush(t, "rw-feat-token", "feat/hello", "feat.txt")
		runGit(t, dir, "push", "-u", "origin", "HEAD:refs/heads/feat/hello")

		// Push to main → denied (default branch protection)
		dir2 := preparePush(t, "rw-feat-token", "try-main", "main.txt")
		err := runGitResult(dir2, "push", "origin", "HEAD:refs/heads/main")
		if err == nil {
			t.Fatal("push to main should be denied for branch-restricted token")
		}

		// Push to bugfix/x → denied (not in allowed_branches)
		dir3 := preparePush(t, "rw-feat-token", "bugfix/x", "bugfix.txt")
		err = runGitResult(dir3, "push", "origin", "HEAD:refs/heads/bugfix/x")
		if err == nil {
			t.Fatal("push to bugfix/x should be denied for feat/-restricted token")
		}
	})

	// === Case 2: rw token with allowed_branches: ["task-1/"] ===
	t.Run("case2_task1_prefix", func(t *testing.T) {
		// Push to task-1/impl → allowed
		dir := preparePush(t, "rw-task1-token", "task-1/impl", "task1.txt")
		runGit(t, dir, "push", "-u", "origin", "HEAD:refs/heads/task-1/impl")

		// Push to task-2/impl → denied
		dir2 := preparePush(t, "rw-task1-token", "task-2/impl", "task2.txt")
		err := runGitResult(dir2, "push", "origin", "HEAD:refs/heads/task-2/impl")
		if err == nil {
			t.Fatal("push to task-2/impl should be denied for task-1/-restricted token")
		}
	})

	// === Case 3: rw token with no allowed_branches ===
	t.Run("case3_no_restriction", func(t *testing.T) {
		// Push to any non-default branch → allowed (no branch restriction)
		dir := preparePush(t, "rw-noprefix-token", "anything/goes", "any.txt")
		runGit(t, dir, "push", "-u", "origin", "HEAD:refs/heads/anything/goes")

		// Fast-forward push to main → allowed (rw has write access)
		dir2 := preparePush(t, "rw-noprefix-token", "try-main2", "main2.txt")
		runGit(t, dir2, "push", "origin", "HEAD:refs/heads/main")

		// Force push to main → denied (non-fast-forward)
		runGit(t, dir2, "commit", "--allow-empty", "--amend", "-m", "amended")
		err := runGitResult(dir2, "push", "--force", "origin", "HEAD:refs/heads/main")
		if err == nil {
			t.Fatal("force push to main should be denied for rw token")
		}
	})

	// === Case 4: r scope + allowed_branches → denied regardless ===
	t.Run("case4_r_scope_with_branches", func(t *testing.T) {
		// Even though allowed_branches includes "feat/", r-scoped tokens cannot push at all.
		dir := preparePush(t, "r-feat-token", "feat/should-fail", "rfeat.txt")
		err := runGitResult(dir, "push", "-u", "origin", "HEAD:refs/heads/feat/should-fail")
		if err == nil {
			t.Fatal("r-scoped token should not be able to push, even to allowed branch")
		}
	})

	// === Case 5: rw scope + allowed_branches ===
	t.Run("case5_rw_scope_with_branches", func(t *testing.T) {
		// Push to allowed branch → allowed
		dir := preparePush(t, "rw-feat-token", "feat/rw-ok", "rwfeat.txt")
		runGit(t, dir, "push", "-u", "origin", "HEAD:refs/heads/feat/rw-ok")

		// Push to disallowed branch → denied
		dir2 := preparePush(t, "rw-feat-token", "other/rw-fail", "rwother.txt")
		err := runGitResult(dir2, "push", "origin", "HEAD:refs/heads/other/rw-fail")
		if err == nil {
			t.Fatal("rw-scoped token should be denied push to branch outside allowed_branches")
		}
	})

	// === Zone isolation: MCP-only token denied git ===
	t.Run("zone_mcp_only_denied_git", func(t *testing.T) {
		dir := t.TempDir()
		mcpURL := fmt.Sprintf("http://x-token:mcp-only-token@%s/test/prtest.git", addr)
		err := runGitResult(dir, "clone", mcpURL, "mcp-repo")
		if err == nil {
			t.Fatal("unzoned (MCP-only) token should be denied git clone")
		}
	})

	// === Zone isolation: super-user (*) allowed git ===
	t.Run("zone_superuser_allowed_git", func(t *testing.T) {
		dir := preparePush(t, "admin-token", "admin-push", "admin.txt")
		runGit(t, dir, "push", "-u", "origin", "HEAD:refs/heads/admin-push")
	})
}

func TestProtectedStorer_FastForwardPush(t *testing.T) {
	baseDir := t.TempDir()
	store := git.NewStore(baseDir)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	handler := git.NewHandler(store, logger)
	handler.PreReceive = func(namespace, project string, principal auth.Principal, refUpdates []git.RefUpdate) error {
		effectiveLevel := git.EffectiveGitLevel(principal.Scope, namespace, project)
		allowed := git.AllowedBranchesFromExtra(principal.ExtraPermissions)
		repo, err := store.OpenRepo(namespace, project)
		if err != nil {
			return err
		}
		return git.CheckBranchProtection(repo, refUpdates, effectiveLevel, allowed)
	}
	handler.ProtectStorer = func(namespace, project string, principal auth.Principal, st storage.Storer) storage.Storer {
		repo, err := store.OpenRepo(namespace, project)
		if err != nil {
			return st
		}
		return &git.ProtectedStorer{
			Storer:  st,
			Repo:    repo,
			Level:   git.EffectiveGitLevel(principal.Scope, namespace, project),
			Allowed: git.AllowedBranchesFromExtra(principal.ExtraPermissions),
		}
	}

	validate := func(token string) (auth.Principal, auth.RejectReason, bool) {
		if token == "w-token" {
			return auth.Principal{
				Name:  "dev",
				Email: "dev@test.com",
				Scope: "git/ns:proj:rw",
			}, "", true
		}
		return auth.Principal{}, auth.ReasonInvalidToken, false
	}

	mux := http.NewServeMux()
	mux.Handle("/", git.BasicAuthMiddleware(validate)(handler))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	if err := store.CreateRepo("ns", "proj"); err != nil {
		t.Fatal(err)
	}

	cloneURL := fmt.Sprintf("http://x-token:w-token@%s/ns/proj.git", ts.Listener.Addr().String())

	// 1. Push initial commit to set up main.
	cloneDir := t.TempDir()
	runGit(t, cloneDir, "clone", cloneURL, "repo")
	repoDir := filepath.Join(cloneDir, "repo")
	os.WriteFile(filepath.Join(repoDir, "init.txt"), []byte("init"), 0o644)
	runGit(t, repoDir, "add", "init.txt")
	runGit(t, repoDir, "commit", "-m", "init")
	runGit(t, repoDir, "push", "-u", "origin", "HEAD:refs/heads/main")

	// 2. Push a valid fast-forward commit to main — must succeed.
	os.WriteFile(filepath.Join(repoDir, "second.txt"), []byte("second"), 0o644)
	runGit(t, repoDir, "add", "second.txt")
	runGit(t, repoDir, "commit", "-m", "second commit")
	runGit(t, repoDir, "push", "origin", "HEAD:refs/heads/main")

	// 3. Push another fast-forward commit — must still succeed.
	os.WriteFile(filepath.Join(repoDir, "third.txt"), []byte("third"), 0o644)
	runGit(t, repoDir, "add", "third.txt")
	runGit(t, repoDir, "commit", "-m", "third commit")
	runGit(t, repoDir, "push", "origin", "HEAD:refs/heads/main")

	// 4. Force push (non-fast-forward) — must be rejected.
	os.WriteFile(filepath.Join(repoDir, "amend.txt"), []byte("amend"), 0o644)
	runGit(t, repoDir, "add", "amend.txt")
	runGit(t, repoDir, "commit", "--amend", "-m", "amended")
	err := runGitResult(repoDir, "push", "--force", "origin", "HEAD:refs/heads/main")
	if err == nil {
		t.Fatal("force push to main should be rejected")
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
