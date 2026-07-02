package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/sopranoworks/gitcote/internal/git"
	"github.com/sopranoworks/gitcote/internal/pr"
)

func TestEmptyRepoPRLifecycle(t *testing.T) {
	baseDir := t.TempDir()
	gitStore := git.NewStore(baseDir)
	ns, proj := "default", "lifecycle"

	if err := gitStore.CreateRepo(ns, proj); err != nil {
		t.Fatal(err)
	}
	repo, err := gitStore.OpenRepo(ns, proj)
	if err != nil {
		t.Fatal(err)
	}

	defaultBranch, err := git.ResolveDefaultBranch(repo)
	if err != nil {
		t.Fatalf("ResolveDefaultBranch: %v", err)
	}
	if defaultBranch != "main" {
		t.Fatalf("default branch = %q, want %q", defaultBranch, "main")
	}

	// Target branch (main) has no commits — ResolveBranch should return ZeroHash.
	targetHash, _ := git.ResolveBranch(repo, defaultBranch)
	if targetHash != plumbing.ZeroHash {
		t.Fatalf("expected ZeroHash for empty main, got %v", targetHash)
	}

	// Create a commit on feat-1 (orphan branch on empty repo).
	projDir := filepath.Join(baseDir, ns, proj)
	writeTestFile(t, projDir, "hello.txt", "hello world\n")
	feat1Hash := commitAllFiles(t, repo, projDir, "feat-1", "add hello.txt")

	// Restore HEAD to main (still empty — feat-1 is an orphan branch).
	repo.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.HEAD, plumbing.NewBranchReferenceName(defaultBranch)))

	// Open PR store.
	prStore, err := pr.Open(filepath.Join(projDir, "prs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer prStore.Close()

	// create_pull_request: target omitted → resolved from HEAD.
	mergeable := computeMergeableForRepo(gitStore, ns, proj, "feat-1", defaultBranch)
	if mergeable != pr.MergeableClean {
		t.Fatalf("mergeable = %q, want clean", mergeable)
	}

	now := time.Now()
	pr1 := &pr.PullRequest{
		RepoNamespace: ns,
		RepoProject:   proj,
		Title:         "feat-1",
		SourceBranch:  "feat-1",
		TargetBranch:  defaultBranch,
		Author:        "test",
		State:         pr.StateOpen,
		Mergeable:     mergeable,
		SourceCommit:  feat1Hash.String(),
		TargetCommit:  targetHash.String(),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	num1, err := prStore.Create(pr1)
	if err != nil {
		t.Fatal(err)
	}
	if num1 != 1 {
		t.Fatalf("PR number = %d, want 1", num1)
	}
	if pr1.TargetCommit != plumbing.ZeroHash.String() {
		t.Fatalf("target_commit = %q, want ZeroHash", pr1.TargetCommit)
	}

	// merge_pull_request PR #1: empty base → ref creation.
	pr1.State = pr.StateApproved
	_ = prStore.Update(pr1)

	sourceHash, err := git.ResolveBranch(repo, pr1.SourceBranch)
	if err != nil {
		t.Fatal(err)
	}
	mergeResult, err := git.ComputeMerge(repo, plumbing.ZeroHash, sourceHash)
	if err != nil {
		t.Fatal(err)
	}
	if !mergeResult.Clean {
		t.Fatal("expected clean merge on empty base")
	}

	if err := git.CreateBranchRef(repo, defaultBranch, sourceHash); err != nil {
		t.Fatalf("CreateBranchRef: %v", err)
	}

	// Verify default branch now has commits.
	mainHash, err := git.ResolveBranch(repo, defaultBranch)
	if err != nil {
		t.Fatalf("ResolveBranch after merge: %v", err)
	}
	if mainHash != feat1Hash {
		t.Fatalf("main HEAD = %v, want feat-1 HEAD %v (fast-forward)", mainHash, feat1Hash)
	}

	pr1.State = pr.StateMerged
	pr1.MergeCommit = sourceHash.String()
	mergedAt := time.Now()
	pr1.MergedAt = &mergedAt
	_ = prStore.Update(pr1)

	// Restore HEAD to main for the next checkout.
	repo.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.HEAD, plumbing.NewBranchReferenceName(defaultBranch)))

	// Second PR: feat-2 from main HEAD.
	createBranchAt(t, repo, "feat-2", mainHash)
	checkoutBranch(t, repo, "feat-2")
	writeTestFile(t, projDir, "world.txt", "hello again\n")
	feat2Hash := commitAllFiles(t, repo, projDir, "feat-2", "add world.txt")

	targetHash2, err := git.ResolveBranch(repo, defaultBranch)
	if err != nil {
		t.Fatalf("ResolveBranch main for PR2: %v", err)
	}
	if targetHash2 == plumbing.ZeroHash {
		t.Fatal("main should have commits after first merge")
	}

	mergeable2 := computeMergeableForRepo(gitStore, ns, proj, "feat-2", defaultBranch)
	if mergeable2 != pr.MergeableClean {
		t.Fatalf("PR2 mergeable = %q, want clean", mergeable2)
	}

	pr2 := &pr.PullRequest{
		RepoNamespace: ns,
		RepoProject:   proj,
		Title:         "feat-2",
		SourceBranch:  "feat-2",
		TargetBranch:  defaultBranch,
		Author:        "test",
		State:         pr.StateApproved,
		Mergeable:     mergeable2,
		SourceCommit:  feat2Hash.String(),
		TargetCommit:  targetHash2.String(),
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	num2, _ := prStore.Create(pr2)
	if num2 != 2 {
		t.Fatalf("PR2 number = %d, want 2", num2)
	}

	// merge_pull_request PR #2: normal merge (two parents).
	mergeResult2, err := git.ComputeMerge(repo, targetHash2, feat2Hash)
	if err != nil {
		t.Fatal(err)
	}
	if !mergeResult2.Clean {
		t.Fatal("expected clean merge for PR2")
	}

	msg := fmt.Sprintf("Merge PR #%d: %s", pr2.Number, pr2.Title)
	mergeHash, err := git.MergeCommitFromTree(repo, mergeResult2.TreeHash, targetHash2, feat2Hash, msg, "GitCote", "gitcote@localhost")
	if err != nil {
		t.Fatal(err)
	}
	if err := git.UpdateBranchRef(repo, defaultBranch, mergeHash, targetHash2); err != nil {
		t.Fatal(err)
	}

	// Verify merge commit has two parents.
	mergeCommit, _ := repo.CommitObject(mergeHash)
	if len(mergeCommit.ParentHashes) != 2 {
		t.Fatalf("merge commit parents = %d, want 2", len(mergeCommit.ParentHashes))
	}

	// main HEAD is the merge commit, not feat-2 HEAD.
	finalMain, _ := git.ResolveBranch(repo, defaultBranch)
	if finalMain == feat2Hash {
		t.Fatal("main HEAD should be merge commit, not feat-2 HEAD")
	}
	if finalMain != mergeHash {
		t.Fatalf("main HEAD = %v, want merge commit %v", finalMain, mergeHash)
	}
}

func TestEmptyRepoPRMergeableAllStates(t *testing.T) {
	baseDir := t.TempDir()
	gitStore := git.NewStore(baseDir)
	ns, proj := "default", "mergestate"

	if err := gitStore.CreateRepo(ns, proj); err != nil {
		t.Fatal(err)
	}
	repo, err := gitStore.OpenRepo(ns, proj)
	if err != nil {
		t.Fatal(err)
	}

	projDir := filepath.Join(baseDir, ns, proj)
	defaultBranch, _ := git.ResolveDefaultBranch(repo)

	writeTestFile(t, projDir, "hello.txt", "hello world\n")
	commitAllFiles(t, repo, projDir, "feat-1", "add hello.txt")

	repo.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.HEAD, plumbing.NewBranchReferenceName(defaultBranch)))

	prStore, err := pr.Open(filepath.Join(projDir, "prs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer prStore.Close()

	targetHash, _ := git.ResolveBranch(repo, defaultBranch)
	if targetHash != plumbing.ZeroHash {
		t.Fatalf("expected ZeroHash for empty main, got %v", targetHash)
	}

	for _, tc := range []struct {
		name  string
		state pr.PRState
	}{
		{"open", pr.StateOpen},
		{"approved", pr.StateApproved},
		{"rejected", pr.StateRejected},
		{"interrupted", pr.StateInterrupted},
		{"merge_conflict", pr.StateMergeConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &pr.PullRequest{
				RepoNamespace: ns,
				RepoProject:   proj,
				Title:         "feat-1 " + tc.name,
				SourceBranch:  "feat-1",
				TargetBranch:  defaultBranch,
				Author:        "test",
				State:         tc.state,
				Mergeable:     pr.MergeableClean,
				SourceCommit:  plumbing.ZeroHash.String(),
				TargetCommit:  plumbing.ZeroHash.String(),
				CreatedAt:     time.Now(),
				UpdatedAt:     time.Now(),
			}
			if _, err := prStore.Create(p); err != nil {
				t.Fatal(err)
			}

			result, err := computeMergeResult(gitStore, ns, proj, "feat-1", defaultBranch)
			if err != nil {
				t.Fatalf("computeMergeResult: %v", err)
			}
			if !result.Clean {
				t.Fatal("expected clean merge on empty base")
			}
		})
	}
}

func TestEmptyRepoPRDiffAllAdditions(t *testing.T) {
	baseDir := t.TempDir()
	gitStore := git.NewStore(baseDir)
	ns, proj := "default", "difftest"

	if err := gitStore.CreateRepo(ns, proj); err != nil {
		t.Fatal(err)
	}
	repo, err := gitStore.OpenRepo(ns, proj)
	if err != nil {
		t.Fatal(err)
	}

	projDir := filepath.Join(baseDir, ns, proj)
	defaultBranch, _ := git.ResolveDefaultBranch(repo)

	os.MkdirAll(filepath.Join(projDir, "sub"), 0o755)
	writeTestFile(t, projDir, "a.txt", "aaa\n")
	writeTestFile(t, projDir, "b.txt", "bbb\n")
	writeTestFile(t, projDir, "sub/c.txt", "ccc\n")
	commitAllFiles(t, repo, projDir, "feat", "add 3 files")

	// Restore HEAD to main (empty).
	repo.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.HEAD, plumbing.NewBranchReferenceName(defaultBranch)))
	diff, files, err := git.PRDiff(repo, "feat", defaultBranch)
	if err != nil {
		t.Fatalf("PRDiff: %v", err)
	}

	if len(files) != 3 {
		t.Fatalf("expected 3 files, got %d: %v", len(files), files)
	}
	for _, f := range files {
		if f.Action != "added" {
			t.Errorf("file %q action = %q, want %q", f.Path, f.Action, "added")
		}
	}

	if diff == "" {
		t.Error("expected non-empty diff")
	}
}

func TestEmptyRepoConcurrentPRsNoConflict(t *testing.T) {
	baseDir := t.TempDir()
	gitStore := git.NewStore(baseDir)
	ns, proj := "default", "concurrent"

	if err := gitStore.CreateRepo(ns, proj); err != nil {
		t.Fatal(err)
	}
	repo, err := gitStore.OpenRepo(ns, proj)
	if err != nil {
		t.Fatal(err)
	}
	projDir := filepath.Join(baseDir, ns, proj)
	defaultBranch, _ := git.ResolveDefaultBranch(repo)

	prStore, err := pr.Open(filepath.Join(projDir, "prs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer prStore.Close()

	// feat-a: file A only (orphan branch on empty repo).
	writeTestFile(t, projDir, "fileA.txt", "content A\n")
	featAHash := commitAllFiles(t, repo, projDir, "feat-a", "add fileA")

	// Clean worktree for feat-b (independent orphan).
	os.Remove(filepath.Join(projDir, "fileA.txt"))
	w, _ := repo.Worktree()
	w.Remove("fileA.txt")

	writeTestFile(t, projDir, "fileB.txt", "content B\n")
	featBHash := commitAllFiles(t, repo, projDir, "feat-b", "add fileB")

	// Restore HEAD to main (empty).
	repo.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.HEAD, plumbing.NewBranchReferenceName(defaultBranch)))

	// Both PRs target the empty default branch.
	targetHash, _ := git.ResolveBranch(repo, defaultBranch)
	if targetHash != plumbing.ZeroHash {
		t.Fatal("main should be empty")
	}

	now := time.Now()
	prA := &pr.PullRequest{
		RepoNamespace: ns, RepoProject: proj,
		Title: "feat-a", SourceBranch: "feat-a", TargetBranch: defaultBranch,
		Author: "test", State: pr.StateApproved,
		Mergeable:    pr.MergeableClean,
		SourceCommit: featAHash.String(), TargetCommit: targetHash.String(),
		CreatedAt: now, UpdatedAt: now,
	}
	numA, _ := prStore.Create(prA)

	prB := &pr.PullRequest{
		RepoNamespace: ns, RepoProject: proj,
		Title: "feat-b", SourceBranch: "feat-b", TargetBranch: defaultBranch,
		Author: "test", State: pr.StateApproved,
		Mergeable:    pr.MergeableClean,
		SourceCommit: featBHash.String(), TargetCommit: targetHash.String(),
		CreatedAt: now, UpdatedAt: now,
	}
	numB, _ := prStore.Create(prB)

	// Both should be mergeable.
	mergeableA := computeMergeableForRepo(gitStore, ns, proj, "feat-a", defaultBranch)
	mergeableB := computeMergeableForRepo(gitStore, ns, proj, "feat-b", defaultBranch)
	if mergeableA != pr.MergeableClean {
		t.Fatalf("PR-A mergeable = %q, want clean", mergeableA)
	}
	if mergeableB != pr.MergeableClean {
		t.Fatalf("PR-B mergeable = %q, want clean", mergeableB)
	}

	// Merge PR-A: empty base → ref creation.
	if err := git.CreateBranchRef(repo, defaultBranch, featAHash); err != nil {
		t.Fatal(err)
	}
	prA.State = pr.StateMerged
	prA.MergeCommit = featAHash.String()
	_ = prStore.Update(prA)

	// After merge, main has commits. PR-B should still be mergeable (different files).
	mergeableBAfter := computeMergeableForRepo(gitStore, ns, proj, "feat-b", defaultBranch)
	if mergeableBAfter != pr.MergeableClean {
		t.Fatalf("PR-B after PR-A merge: mergeable = %q, want clean", mergeableBAfter)
	}

	// Merge PR-B: normal merge (main now has commits).
	mainHash, _ := git.ResolveBranch(repo, defaultBranch)
	mergeResult, err := git.ComputeMerge(repo, mainHash, featBHash)
	if err != nil {
		t.Fatal(err)
	}
	if !mergeResult.Clean {
		t.Fatal("PR-B merge should be clean (different files)")
	}

	mergeHash, err := git.MergeCommitFromTree(repo, mergeResult.TreeHash, mainHash, featBHash,
		fmt.Sprintf("Merge PR #%d", numB), "GitCote", "gitcote@localhost")
	if err != nil {
		t.Fatal(err)
	}
	if err := git.UpdateBranchRef(repo, defaultBranch, mergeHash, mainHash); err != nil {
		t.Fatal(err)
	}

	// Verify merge commit.
	mergeCommit, _ := repo.CommitObject(mergeHash)
	if len(mergeCommit.ParentHashes) != 2 {
		t.Fatalf("merge commit parents = %d, want 2", len(mergeCommit.ParentHashes))
	}

	// Verify merged tree has both files.
	mergeTree, _ := mergeCommit.Tree()
	for _, name := range []string{"fileA.txt", "fileB.txt"} {
		if _, err := mergeTree.File(name); err != nil {
			t.Errorf("merged tree missing %q", name)
		}
	}

	_ = numA // suppress unused
}

func TestEmptyRepoConcurrentPRsWithConflict(t *testing.T) {
	baseDir := t.TempDir()
	gitStore := git.NewStore(baseDir)
	ns, proj := "default", "conflict"

	if err := gitStore.CreateRepo(ns, proj); err != nil {
		t.Fatal(err)
	}
	repo, err := gitStore.OpenRepo(ns, proj)
	if err != nil {
		t.Fatal(err)
	}
	projDir := filepath.Join(baseDir, ns, proj)
	defaultBranch, _ := git.ResolveDefaultBranch(repo)

	prStore, err := pr.Open(filepath.Join(projDir, "prs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer prStore.Close()

	// feat-a: file X with content A (orphan branch).
	writeTestFile(t, projDir, "X.txt", "content A\n")
	featAHash := commitAllFiles(t, repo, projDir, "feat-a", "add X with content A")

	// feat-b: file X with content B (independent orphan).
	w, _ := repo.Worktree()
	w.Remove("X.txt")
	writeTestFile(t, projDir, "X.txt", "content B\n")
	featBHash := commitAllFiles(t, repo, projDir, "feat-b", "add X with content B")

	// Restore HEAD to main (empty).
	repo.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.HEAD, plumbing.NewBranchReferenceName(defaultBranch)))

	targetHash, _ := git.ResolveBranch(repo, defaultBranch)

	now := time.Now()
	prA := &pr.PullRequest{
		RepoNamespace: ns, RepoProject: proj,
		Title: "feat-a", SourceBranch: "feat-a", TargetBranch: defaultBranch,
		Author: "test", State: pr.StateApproved,
		Mergeable:    pr.MergeableClean,
		SourceCommit: featAHash.String(), TargetCommit: targetHash.String(),
		CreatedAt: now, UpdatedAt: now,
	}
	prStore.Create(prA)

	prB := &pr.PullRequest{
		RepoNamespace: ns, RepoProject: proj,
		Title: "feat-b", SourceBranch: "feat-b", TargetBranch: defaultBranch,
		Author: "test", State: pr.StateApproved,
		Mergeable:    pr.MergeableClean,
		SourceCommit: featBHash.String(), TargetCommit: targetHash.String(),
		CreatedAt: now, UpdatedAt: now,
	}
	prStore.Create(prB)

	// Merge PR-A: empty base → ref creation.
	if err := git.CreateBranchRef(repo, defaultBranch, featAHash); err != nil {
		t.Fatal(err)
	}
	prA.State = pr.StateMerged
	_ = prStore.Update(prA)

	// PR-B should now have a conflict (same file X, different content).
	mainHash, _ := git.ResolveBranch(repo, defaultBranch)
	mergeResult, err := git.ComputeMerge(repo, mainHash, featBHash)
	if err != nil {
		t.Fatal(err)
	}
	if mergeResult.Clean {
		t.Fatal("expected conflict for PR-B (same file, different content)")
	}
	if len(mergeResult.Conflicts) == 0 {
		t.Fatal("expected at least one conflict entry")
	}

	foundX := false
	for _, c := range mergeResult.Conflicts {
		if c.Path == "X.txt" {
			foundX = true
		}
	}
	if !foundX {
		t.Errorf("expected conflict on X.txt, got: %v", mergeResult.Conflicts)
	}

	mergeableB := computeMergeableForRepo(gitStore, ns, proj, "feat-b", defaultBranch)
	if mergeableB != pr.MergeableConflict {
		t.Fatalf("PR-B after PR-A merge: mergeable = %q, want conflict", mergeableB)
	}
}

// --- test helpers ---

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitAllFiles(t *testing.T, repo *gogit.Repository, dir, branch, msg string) plumbing.Hash {
	t.Helper()

	// Point HEAD at the target branch so the commit lands there (not on main).
	repo.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.HEAD, plumbing.NewBranchReferenceName(branch)))

	w, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() == ".git" {
			continue
		}
		if _, err := w.Add(e.Name()); err != nil {
			t.Fatal(err)
		}
	}

	hash, err := w.Commit(msg, &gogit.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com"},
	})
	if err != nil {
		t.Fatal(err)
	}

	createBranchAt(t, repo, branch, hash)
	return hash
}

func createBranchAt(t *testing.T, repo *gogit.Repository, name string, hash plumbing.Hash) {
	t.Helper()
	ref := plumbing.NewHashReference(plumbing.NewBranchReferenceName(name), hash)
	if err := repo.Storer.SetReference(ref); err != nil {
		t.Fatal(err)
	}
}

func checkoutBranch(t *testing.T, repo *gogit.Repository, branch string) {
	t.Helper()
	w, _ := repo.Worktree()
	if err := w.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(branch),
		Force:  true,
	}); err != nil {
		t.Fatal(err)
	}
}
