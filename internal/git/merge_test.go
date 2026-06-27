package git_test

import (
	"errors"
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

func TestComputeMergeBothModifySameFile(t *testing.T) {
	repo, dir := initTestRepo(t)

	writeFile(t, dir, "a.txt", "base content")
	baseHash := commitAll(t, repo, dir, "base")

	createBranch(t, repo, "target", baseHash)
	checkout(t, repo, "target")
	writeFile(t, dir, "a.txt", "target version")
	targetHash := commitAll(t, repo, dir, "modify a on target")

	createBranch(t, repo, "source", baseHash)
	checkout(t, repo, "source")
	writeFile(t, dir, "a.txt", "source version")
	sourceHash := commitAll(t, repo, dir, "modify a on source")

	result, err := git.ComputeMerge(repo, targetHash, sourceHash)
	if err != nil {
		t.Fatalf("ComputeMerge: %v", err)
	}
	if result.Clean {
		t.Fatal("expected conflict, got clean merge")
	}
	if len(result.Conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(result.Conflicts))
	}
	if result.Conflicts[0].Path != "a.txt" {
		t.Errorf("conflict path = %q, want %q", result.Conflicts[0].Path, "a.txt")
	}
	if result.Conflicts[0].Type != "content" {
		t.Errorf("conflict type = %q, want %q", result.Conflicts[0].Type, "content")
	}
}

func TestComputeMergeBothModifySameContent(t *testing.T) {
	repo, dir := initTestRepo(t)

	writeFile(t, dir, "a.txt", "base")
	baseHash := commitAll(t, repo, dir, "base")

	createBranch(t, repo, "target", baseHash)
	checkout(t, repo, "target")
	writeFile(t, dir, "a.txt", "same new content")
	targetHash := commitAll(t, repo, dir, "modify a on target")

	createBranch(t, repo, "source", baseHash)
	checkout(t, repo, "source")
	writeFile(t, dir, "a.txt", "same new content")
	sourceHash := commitAll(t, repo, dir, "modify a on source")

	result, err := git.ComputeMerge(repo, targetHash, sourceHash)
	if err != nil {
		t.Fatalf("ComputeMerge: %v", err)
	}
	if !result.Clean {
		t.Fatalf("expected clean merge, got conflicts: %v", result.Conflicts)
	}

	mergeCommit, _ := repo.CommitObject(commitFromTree(t, repo, result.TreeHash, targetHash, sourceHash))
	mergeTree, _ := mergeCommit.Tree()
	assertFileContent(t, mergeTree, "a.txt", "same new content")
}

func TestComputeMergeModifyDeleteConflict(t *testing.T) {
	repo, dir := initTestRepo(t)

	writeFile(t, dir, "a.txt", "base")
	baseHash := commitAll(t, repo, dir, "base")

	// Target deletes a.txt.
	createBranch(t, repo, "target", baseHash)
	checkout(t, repo, "target")
	os.Remove(filepath.Join(dir, "a.txt"))
	targetHash := commitRemove(t, repo, "a.txt", "delete a on target")

	// Source modifies a.txt.
	createBranch(t, repo, "source", baseHash)
	checkout(t, repo, "source")
	writeFile(t, dir, "a.txt", "modified on source")
	sourceHash := commitAll(t, repo, dir, "modify a on source")

	result, err := git.ComputeMerge(repo, targetHash, sourceHash)
	if err != nil {
		t.Fatalf("ComputeMerge: %v", err)
	}
	if result.Clean {
		t.Fatal("expected conflict, got clean merge")
	}
	if len(result.Conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(result.Conflicts))
	}
	if result.Conflicts[0].Type != "modify-delete" {
		t.Errorf("conflict type = %q, want %q", result.Conflicts[0].Type, "modify-delete")
	}
}

func TestComputeMergeDeleteModifyConflict(t *testing.T) {
	repo, dir := initTestRepo(t)

	writeFile(t, dir, "a.txt", "base")
	baseHash := commitAll(t, repo, dir, "base")

	// Target modifies a.txt.
	createBranch(t, repo, "target", baseHash)
	checkout(t, repo, "target")
	writeFile(t, dir, "a.txt", "modified on target")
	targetHash := commitAll(t, repo, dir, "modify a on target")

	// Source deletes a.txt.
	createBranch(t, repo, "source", baseHash)
	checkout(t, repo, "source")
	os.Remove(filepath.Join(dir, "a.txt"))
	sourceHash := commitRemove(t, repo, "a.txt", "delete a on source")

	result, err := git.ComputeMerge(repo, targetHash, sourceHash)
	if err != nil {
		t.Fatalf("ComputeMerge: %v", err)
	}
	if result.Clean {
		t.Fatal("expected conflict, got clean merge")
	}
	if len(result.Conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(result.Conflicts))
	}
	if result.Conflicts[0].Type != "modify-delete" {
		t.Errorf("conflict type = %q, want %q", result.Conflicts[0].Type, "modify-delete")
	}
}

func TestComputeMergeBothDelete(t *testing.T) {
	repo, dir := initTestRepo(t)

	writeFile(t, dir, "a.txt", "base")
	writeFile(t, dir, "b.txt", "keep")
	baseHash := commitAll(t, repo, dir, "base")

	createBranch(t, repo, "target", baseHash)
	checkout(t, repo, "target")
	os.Remove(filepath.Join(dir, "a.txt"))
	targetHash := commitRemove(t, repo, "a.txt", "delete a on target")

	createBranch(t, repo, "source", baseHash)
	checkout(t, repo, "source")
	os.Remove(filepath.Join(dir, "a.txt"))
	sourceHash := commitRemove(t, repo, "a.txt", "delete a on source")

	result, err := git.ComputeMerge(repo, targetHash, sourceHash)
	if err != nil {
		t.Fatalf("ComputeMerge: %v", err)
	}
	if !result.Clean {
		t.Fatalf("expected clean merge (both delete), got conflicts: %v", result.Conflicts)
	}

	mergeCommit, _ := repo.CommitObject(commitFromTree(t, repo, result.TreeHash, targetHash, sourceHash))
	mergeTree, _ := mergeCommit.Tree()
	assertFileAbsent(t, mergeTree, "a.txt")
	assertFileContent(t, mergeTree, "b.txt", "keep")
}

func TestMergeCommitRejectsConflicts(t *testing.T) {
	repo, dir := initTestRepo(t)

	writeFile(t, dir, "a.txt", "base")
	baseHash := commitAll(t, repo, dir, "base")

	createBranch(t, repo, "target", baseHash)
	checkout(t, repo, "target")
	writeFile(t, dir, "a.txt", "target version")
	targetHash := commitAll(t, repo, dir, "modify a on target")

	createBranch(t, repo, "source", baseHash)
	checkout(t, repo, "source")
	writeFile(t, dir, "a.txt", "source version")
	sourceHash := commitAll(t, repo, dir, "modify a on source")

	_, err := git.MergeCommit(repo, sourceHash, targetHash, "merge", "Test", "test@test.com")
	if err == nil {
		t.Fatal("expected error from MergeCommit, got nil")
	}
	var mergeErr *git.MergeConflictError
	if !errors.As(err, &mergeErr) {
		t.Fatalf("expected MergeConflictError, got %T: %v", err, err)
	}
	if len(mergeErr.Conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(mergeErr.Conflicts))
	}
}

func TestComputeMergeMultipleConflicts(t *testing.T) {
	repo, dir := initTestRepo(t)

	writeFile(t, dir, "a.txt", "a-base")
	writeFile(t, dir, "b.txt", "b-base")
	writeFile(t, dir, "c.txt", "c-base")
	baseHash := commitAll(t, repo, dir, "base")

	createBranch(t, repo, "target", baseHash)
	checkout(t, repo, "target")
	writeFile(t, dir, "a.txt", "a-target")
	writeFile(t, dir, "b.txt", "b-target")
	writeFile(t, dir, "c.txt", "c-target")
	targetHash := commitAll(t, repo, dir, "modify all on target")

	createBranch(t, repo, "source", baseHash)
	checkout(t, repo, "source")
	writeFile(t, dir, "a.txt", "a-source")
	writeFile(t, dir, "b.txt", "b-source")
	sourceHash := commitAll(t, repo, dir, "modify a,b on source")

	result, err := git.ComputeMerge(repo, targetHash, sourceHash)
	if err != nil {
		t.Fatalf("ComputeMerge: %v", err)
	}
	if result.Clean {
		t.Fatal("expected conflicts")
	}
	// a.txt and b.txt conflict; c.txt only changed on target → no conflict.
	if len(result.Conflicts) != 2 {
		t.Fatalf("expected 2 conflicts, got %d: %v", len(result.Conflicts), result.Conflicts)
	}
	// Conflicts should be sorted by path.
	if result.Conflicts[0].Path != "a.txt" || result.Conflicts[1].Path != "b.txt" {
		t.Errorf("conflicts = %v, want a.txt and b.txt", result.Conflicts)
	}
}

func TestComputeMergeCleanDivergent(t *testing.T) {
	repo, dir := initTestRepo(t)

	writeFile(t, dir, "a.txt", "a")
	writeFile(t, dir, "b.txt", "b")
	baseHash := commitAll(t, repo, dir, "base")

	createBranch(t, repo, "target", baseHash)
	checkout(t, repo, "target")
	writeFile(t, dir, "a.txt", "a-modified")
	targetHash := commitAll(t, repo, dir, "modify a on target")

	createBranch(t, repo, "source", baseHash)
	checkout(t, repo, "source")
	writeFile(t, dir, "b.txt", "b-modified")
	sourceHash := commitAll(t, repo, dir, "modify b on source")

	result, err := git.ComputeMerge(repo, targetHash, sourceHash)
	if err != nil {
		t.Fatalf("ComputeMerge: %v", err)
	}
	if !result.Clean {
		t.Fatalf("expected clean merge, got conflicts: %v", result.Conflicts)
	}
	if result.FastForward {
		t.Error("should not be fast-forward for divergent branches")
	}

	mergeCommit, _ := repo.CommitObject(commitFromTree(t, repo, result.TreeHash, targetHash, sourceHash))
	mergeTree, _ := mergeCommit.Tree()
	assertFileContent(t, mergeTree, "a.txt", "a-modified")
	assertFileContent(t, mergeTree, "b.txt", "b-modified")
}

func commitFromTree(t *testing.T, repo *gogit.Repository, treeHash, parent1, parent2 plumbing.Hash) plumbing.Hash {
	t.Helper()
	hash, err := git.MergeCommitFromTree(repo, treeHash, parent1, parent2, "test merge", "Test", "test@test.com")
	if err != nil {
		t.Fatal(err)
	}
	return hash
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
