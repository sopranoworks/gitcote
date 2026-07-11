package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sopranoworks/gitcote/internal/git"
	"github.com/sopranoworks/gitcote/internal/integrity"
	"github.com/sopranoworks/shoka/pkg/auth"
	"github.com/sopranoworks/shoka/pkg/oauthstore"
)

// TestPostReceive_SourceBranchResolution_TwoSimultaneousOpenPRs reproduces
// the bug documented in commit a8922455 / gitcote/development report
// 2026-07-11-guard-review-and-retry-race-safety: handlePostReceive resolved
// the pushed PR's source branch by taking "the first non-target branch"
// from git.ListBranches, whose iteration order is NOT guaranteed once more
// than one non-target branch exists in the repo.
//
// Confirmed empirically (see the directive's report) that git.ListBranches'
// order depends on whether refs are loose or packed: right after a push,
// loose refs iterate most-recent-first (which accidentally makes the old
// heuristic look correct for simple sequential single-branch pushes) — but
// once a repo's refs get packed (a normal, expected event: explicit gc, or
// automatic gc once enough loose refs/objects accumulate — not a contrived
// edge case), packed refs iterate ALPHABETICALLY instead. With two branches
// "feat/active" (an already-open PR) and "feat/queued" (about to be pushed)
// both packed, "feat/active" sorts first — so pushing feat/queued with
// pull_request.create resolves sourceBranch back to "feat/active", matches
// PR #1's existing FindByBranches lookup, and silently UPDATES PR #1
// instead of creating PR #2.
//
// This test forces that exact repo state (both branches packed) before the
// triggering push, so it reproduces deterministically instead of depending
// on incidental loose-ref iteration timing.
func TestPostReceive_SourceBranchResolution_TwoSimultaneousOpenPRs(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	baseDir := t.TempDir()
	gitStore := git.NewStore(baseDir)
	ns, proj := "e2e", "srcbranch"
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if err := gitStore.CreateRepo(ns, proj); err != nil {
		t.Fatal(err)
	}

	integrityStore, err := integrity.Open(filepath.Join(baseDir, "repo_heads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer integrityStore.Close()
	headStore = integrityStore

	oauthSt, err := oauthstore.Open(filepath.Join(baseDir, "oauth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer oauthSt.Close()

	disabled := false
	evtCtx := &eventContext{
		gitStore:    gitStore,
		integrityHS: integrityStore,
		oauthStore:  oauthSt,
		agentCfg:    AgentSpawnConfig{Enabled: &disabled},
		logger:      logger,
	}

	gitHTTP := git.NewHandler(gitStore, logger)
	gitHTTP.PostReceive = func(namespace, project string, principal auth.Principal, pushOpts []string, refUpdates []git.RefUpdate) {
		handlePostReceive(gitStore, logger, namespace, project, principal, pushOpts, refUpdates, evtCtx)
	}

	httpMux := http.NewServeMux()
	httpMux.Handle("/", gitHTTP)
	ts := httptest.NewServer(httpMux)
	defer ts.Close()

	cloneDir := t.TempDir()
	runGitE2E(t, cloneDir, "clone", ts.URL+"/"+ns+"/"+proj+".git", "repo")
	repoDir := filepath.Join(cloneDir, "repo")

	writeTestFile(t, repoDir, "README.md", "# Source Branch Resolution Test\n")
	runGitE2E(t, repoDir, "add", "README.md")
	runGitE2E(t, repoDir, "commit", "-m", "initial commit")
	runGitE2E(t, repoDir, "push", "-u", "origin", "HEAD:refs/heads/main")

	// Push PR #1's branch normally — this creates PR #1 correctly and
	// leaves feat/active present on the server for the rest of the test.
	runGitE2E(t, repoDir, "checkout", "-b", "feat/active")
	writeTestFile(t, repoDir, "active.txt", "active\n")
	runGitE2E(t, repoDir, "add", "active.txt")
	runGitE2E(t, repoDir, "commit", "-m", "active PR change")
	runGitE2E(t, repoDir, "push", "-u", "origin", "feat/active",
		"-o", "pull_request.create",
		"-o", "pull_request.title=Active PR",
	)

	prStore, err := getPRStore(baseDir, ns, proj)
	if err != nil {
		t.Fatal(err)
	}
	prsAfterFirst, err := prStore.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(prsAfterFirst) != 1 {
		t.Fatalf("expected 1 PR after first push, got %d", len(prsAfterFirst))
	}
	if prsAfterFirst[0].SourceBranch != "feat/active" || prsAfterFirst[0].Title != "Active PR" {
		t.Fatalf("PR #1 = %+v, want source=feat/active title=%q", prsAfterFirst[0], "Active PR")
	}

	// Push feat/queued's content WITHOUT pull_request.create yet — this is
	// a completely ordinary occurrence (e.g. an operator pushes a branch,
	// then decides moments later to open a PR for it with a follow-up
	// push-option-bearing push; or the branch already existed for other
	// reasons). This creates the ref on the server without exercising the
	// PR-creation code path at all.
	runGitE2E(t, repoDir, "checkout", "main")
	runGitE2E(t, repoDir, "checkout", "-b", "feat/queued")
	writeTestFile(t, repoDir, "queued.txt", "queued\n")
	runGitE2E(t, repoDir, "add", "queued.txt")
	runGitE2E(t, repoDir, "commit", "-m", "queued PR change")
	runGitE2E(t, repoDir, "push", "-u", "origin", "feat/queued")

	// Force the server's bare repo to pack its refs — a normal, expected
	// event in any git repository's lifecycle (explicit gc, or automatic
	// gc once enough loose refs/objects accumulate). git.ListBranches
	// iterates repo.References(), and loose vs. packed refs are NOT
	// iterated in the same order: loose refs (the state right after a
	// push) happen to iterate in most-recent-first order on this backend,
	// which coincidentally makes the old "first non-target branch"
	// heuristic look correct — but once BOTH branches are packed, they
	// iterate alphabetically instead, and "feat/active" < "feat/queued".
	projPath, err := gitStore.ProjectPath(ns, proj)
	if err != nil {
		t.Fatal(err)
	}
	packCmd := exec.Command("git", "pack-refs", "--all")
	packCmd.Dir = projPath
	if out, err := packCmd.CombinedOutput(); err != nil {
		t.Fatalf("git pack-refs: %v: %s", err, out)
	}

	// Now the operator actually requests a PR for feat/queued. Since no
	// new commit is needed (the content is already there), this is
	// invoked the same way the git hook would fire it: directly, with the
	// push-options that signal PR creation, AND the ref-update list a real
	// push's post-receive hook would supply (this is what production
	// always provides — HTTP from the pkt-line command list, SSH from
	// ProtectedStorer — so passing it explicitly here matches the real
	// call shape, not a degraded/fallback one). handlePostReceive re-reads
	// repo state fresh from the store on every call, so invoking it
	// directly here exercises exactly the same logic a real push-triggered
	// call would, against the exact repo state constructed above.
	repoForHash, err := gitStore.OpenRepo(ns, proj)
	if err != nil {
		t.Fatal(err)
	}
	queuedHash, err := git.ResolveBranch(repoForHash, "feat/queued")
	if err != nil {
		t.Fatal(err)
	}
	refUpdates := []git.RefUpdate{
		{RefName: "refs/heads/feat/queued", OldHash: queuedHash, NewHash: queuedHash},
	}

	principal := auth.Principal{Name: "admin", Email: "admin@test.com", Scope: "*"}
	handlePostReceive(gitStore, logger, ns, proj, principal,
		[]string{"pull_request.create", "pull_request.title=Queued PR"}, refUpdates, evtCtx)

	prsAfterSecond, err := prStore.List("")
	if err != nil {
		t.Fatal(err)
	}

	if len(prsAfterSecond) != 2 {
		t.Fatalf("expected 2 PRs after the feat/queued PR-creation trigger (feat/active's PR must be "+
			"untouched and a new PR created for feat/queued), got %d: %+v", len(prsAfterSecond), prsAfterSecond)
	}

	foundActive, foundQueued := false, false
	for i := range prsAfterSecond {
		p := &prsAfterSecond[i]
		switch p.SourceBranch {
		case "feat/active":
			foundActive = true
			if p.Title != "Active PR" {
				t.Fatalf("PR for feat/active was corrupted by the second trigger: title = %q, want %q "+
					"(this is the bug — the second PR-creation trigger updated the WRONG existing PR "+
					"instead of creating a new one)", p.Title, "Active PR")
			}
		case "feat/queued":
			foundQueued = true
			if p.Title != "Queued PR" {
				t.Fatalf("PR for feat/queued has wrong title: %q, want %q", p.Title, "Queued PR")
			}
		}
	}
	if !foundActive {
		t.Fatal("PR for feat/active is missing entirely after the second trigger")
	}
	if !foundQueued {
		t.Fatal("no PR was created for feat/queued — the source-branch resolution incorrectly matched " +
			"an existing PR (feat/active) instead of creating a new one for the branch that was " +
			"actually the target of pull_request.create")
	}

	t.Log("PASS: two simultaneously-open PRs on distinct (packed) branches both resolved correctly")
}
