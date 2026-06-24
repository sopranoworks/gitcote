package git_test

import (
	"os"
	"path/filepath"
	"testing"

	gogit "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/sopranoworks/gityard/internal/git"
)

func TestMergeDivergentBranches(t *testing.T) {
	repo, dir := initTestRepo(t)

	// Base: a.txt=a, b.txt=b
	writeFile(t, dir, "a.txt", "a")
	writeFile(t, dir, "b.txt", "b")
	baseHash := commitAll(t, repo, dir, "base")

	// Target branch: modify a.txt
	createBranch(t, repo, "target", baseHash)
	checkout(t, repo, "target")
	writeFile(t, dir, "a.txt", "a-modified")
	targetHash := commitAll(t, repo, dir, "modify a on target")

	// Source branch: modify b.txt (from base, not target)
	createBranch(t, repo, "source", baseHash)
	checkout(t, repo, "source")
	writeFile(t, dir, "b.txt", "b-modified")
	sourceHash := commitAll(t, repo, dir, "modify b on source")

	// Conflict check: should be clean (different files).
	result, err := git.CheckConflicts(repo, sourceHash, targetHash)
	if err != nil {
		t.Fatalf("CheckConflicts: %v", err)
	}
	if result.HasConflict {
		t.Fatalf("expected no conflict, got conflicts: %v", result.ConflictedFiles)
	}

	// Merge.
	mergeHash, err := git.MergeCommit(repo, sourceHash, targetHash, "merge", "Test", "test@test.com")
	if err != nil {
		t.Fatalf("MergeCommit: %v", err)
	}

	// Verify the merged tree has BOTH changes.
	mergeCommit, _ := repo.CommitObject(mergeHash)
	mergeTree, _ := mergeCommit.Tree()

	assertFileContent(t, mergeTree, "a.txt", "a-modified")
	assertFileContent(t, mergeTree, "b.txt", "b-modified")
}

func TestMergeSourceAddsFileTargetModifies(t *testing.T) {
	repo, dir := initTestRepo(t)

	writeFile(t, dir, "a.txt", "a")
	baseHash := commitAll(t, repo, dir, "base")

	createBranch(t, repo, "target", baseHash)
	checkout(t, repo, "target")
	writeFile(t, dir, "a.txt", "a-modified")
	targetHash := commitAll(t, repo, dir, "modify a on target")

	createBranch(t, repo, "source", baseHash)
	checkout(t, repo, "source")
	writeFile(t, dir, "c.txt", "c")
	sourceHash := commitAll(t, repo, dir, "add c on source")

	mergeHash, err := git.MergeCommit(repo, sourceHash, targetHash, "merge", "Test", "test@test.com")
	if err != nil {
		t.Fatalf("MergeCommit: %v", err)
	}

	mergeCommit, _ := repo.CommitObject(mergeHash)
	mergeTree, _ := mergeCommit.Tree()

	assertFileContent(t, mergeTree, "a.txt", "a-modified")
	assertFileContent(t, mergeTree, "c.txt", "c")
}

func TestMergeSourceDeletesFileTargetAdds(t *testing.T) {
	repo, dir := initTestRepo(t)

	writeFile(t, dir, "a.txt", "a")
	baseHash := commitAll(t, repo, dir, "base")

	createBranch(t, repo, "target", baseHash)
	checkout(t, repo, "target")
	writeFile(t, dir, "b.txt", "b")
	targetHash := commitAll(t, repo, dir, "add b on target")

	createBranch(t, repo, "source", baseHash)
	checkout(t, repo, "source")
	os.Remove(filepath.Join(dir, "a.txt"))
	sourceHash := commitRemove(t, repo, "a.txt", "delete a on source")

	mergeHash, err := git.MergeCommit(repo, sourceHash, targetHash, "merge", "Test", "test@test.com")
	if err != nil {
		t.Fatalf("MergeCommit: %v", err)
	}

	mergeCommit, _ := repo.CommitObject(mergeHash)
	mergeTree, _ := mergeCommit.Tree()

	assertFileAbsent(t, mergeTree, "a.txt")
	assertFileContent(t, mergeTree, "b.txt", "b")
}

func TestMergeFastForward(t *testing.T) {
	repo, dir := initTestRepo(t)

	writeFile(t, dir, "a.txt", "a")
	baseHash := commitAll(t, repo, dir, "base")

	// Source adds a file on top of base; target IS base.
	createBranch(t, repo, "source", baseHash)
	checkout(t, repo, "source")
	writeFile(t, dir, "b.txt", "b")
	sourceHash := commitAll(t, repo, dir, "add b on source")

	result, err := git.CheckConflicts(repo, sourceHash, baseHash)
	if err != nil {
		t.Fatalf("CheckConflicts: %v", err)
	}
	if !result.IsFastForward {
		t.Error("expected fast-forward")
	}

	mergeHash, err := git.MergeCommit(repo, sourceHash, baseHash, "merge", "Test", "test@test.com")
	if err != nil {
		t.Fatalf("MergeCommit: %v", err)
	}

	mergeCommit, _ := repo.CommitObject(mergeHash)
	mergeTree, _ := mergeCommit.Tree()
	assertFileContent(t, mergeTree, "a.txt", "a")
	assertFileContent(t, mergeTree, "b.txt", "b")
}

func TestMergeNestedDirectory(t *testing.T) {
	repo, dir := initTestRepo(t)

	os.MkdirAll(filepath.Join(dir, "dir"), 0o755)
	writeFile(t, dir, "dir/x.txt", "x")
	baseHash := commitAll(t, repo, dir, "base")

	createBranch(t, repo, "target", baseHash)
	checkout(t, repo, "target")
	writeFile(t, dir, "dir/y.txt", "y")
	targetHash := commitAll(t, repo, dir, "add y on target")

	createBranch(t, repo, "source", baseHash)
	checkout(t, repo, "source")
	writeFile(t, dir, "dir/x.txt", "x-modified")
	sourceHash := commitAll(t, repo, dir, "modify x on source")

	mergeHash, err := git.MergeCommit(repo, sourceHash, targetHash, "merge", "Test", "test@test.com")
	if err != nil {
		t.Fatalf("MergeCommit: %v", err)
	}

	mergeCommit, _ := repo.CommitObject(mergeHash)
	mergeTree, _ := mergeCommit.Tree()

	assertFileContent(t, mergeTree, "dir/x.txt", "x-modified")
	assertFileContent(t, mergeTree, "dir/y.txt", "y")
}

// --- helpers ---

func initTestRepo(t *testing.T) (*gogit.Repository, string) {
	t.Helper()
	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	return repo, dir
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitAll(t *testing.T, repo *gogit.Repository, dir string, msg string) plumbing.Hash {
	t.Helper()
	w, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	// Add all files.
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
	return hash
}

func createBranch(t *testing.T, repo *gogit.Repository, name string, hash plumbing.Hash) {
	t.Helper()
	ref := plumbing.NewHashReference(plumbing.NewBranchReferenceName(name), hash)
	if err := repo.Storer.SetReference(ref); err != nil {
		t.Fatal(err)
	}
}

func checkout(t *testing.T, repo *gogit.Repository, branch string) {
	t.Helper()
	w, _ := repo.Worktree()
	if err := w.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(branch),
		Force:  true,
	}); err != nil {
		t.Fatal(err)
	}
}

func assertFileContent(t *testing.T, tree *object.Tree, path, expected string) {
	t.Helper()
	f, err := tree.File(path)
	if err != nil {
		t.Errorf("file %q not found in merged tree: %v", path, err)
		return
	}
	content, _ := f.Contents()
	if content != expected {
		t.Errorf("file %q = %q, want %q", path, content, expected)
	}
}

func commitRemove(t *testing.T, repo *gogit.Repository, path, msg string) plumbing.Hash {
	t.Helper()
	w, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Remove(path); err != nil {
		t.Fatal(err)
	}
	hash, err := w.Commit(msg, &gogit.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func assertFileAbsent(t *testing.T, tree *object.Tree, path string) {
	t.Helper()
	_, err := tree.File(path)
	if err == nil {
		t.Errorf("file %q should be absent but exists", path)
	}
}
