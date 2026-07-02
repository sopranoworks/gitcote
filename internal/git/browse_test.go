package git_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sopranoworks/gitcote/internal/git"
)

func setupBrowseRepo(t *testing.T) (*git.Store, string) {
	t.Helper()
	baseDir := t.TempDir()
	store := git.NewStore(baseDir)
	if err := store.CreateRepo("ns", "browse"); err != nil {
		t.Fatal(err)
	}
	projPath, _ := store.ProjectPath("ns", "browse")

	os.MkdirAll(filepath.Join(projPath, "docs"), 0o755)
	os.WriteFile(filepath.Join(projPath, "README.md"), []byte("# Hello\n"), 0o644)
	os.WriteFile(filepath.Join(projPath, "docs/guide.txt"), []byte("guide content\n"), 0o644)
	runGit(t, projPath, "add", ".")
	runGit(t, projPath, "commit", "-m", "initial commit")

	os.WriteFile(filepath.Join(projPath, "README.md"), []byte("# Hello World\n"), 0o644)
	runGit(t, projPath, "add", "README.md")
	runGit(t, projPath, "commit", "-m", "update readme")

	runGit(t, projPath, "checkout", "-b", "feature")
	os.WriteFile(filepath.Join(projPath, "feature.txt"), []byte("feature\n"), 0o644)
	runGit(t, projPath, "add", "feature.txt")
	runGit(t, projPath, "commit", "-m", "add feature")

	runGit(t, projPath, "checkout", "main")

	return store, projPath
}

func TestResolveRef(t *testing.T) {
	store, _ := setupBrowseRepo(t)
	repo, err := store.OpenRepo("ns", "browse")
	if err != nil {
		t.Fatal(err)
	}

	// HEAD
	h, err := git.ResolveRef(repo, "")
	if err != nil {
		t.Fatalf("resolve HEAD: %v", err)
	}
	if h.IsZero() {
		t.Error("HEAD resolved to zero hash")
	}

	// Branch name
	h2, err := git.ResolveRef(repo, "main")
	if err != nil {
		t.Fatalf("resolve main: %v", err)
	}
	if h != h2 {
		t.Error("HEAD and main should be the same")
	}

	// Feature branch
	hf, err := git.ResolveRef(repo, "feature")
	if err != nil {
		t.Fatalf("resolve feature: %v", err)
	}
	if hf == h {
		t.Error("feature and main should differ")
	}

	// Full SHA
	h3, err := git.ResolveRef(repo, h.String())
	if err != nil {
		t.Fatalf("resolve SHA: %v", err)
	}
	if h3 != h {
		t.Error("SHA resolution mismatch")
	}

	// Non-existent ref
	_, err = git.ResolveRef(repo, "nonexistent")
	if err == nil {
		t.Error("expected error for non-existent ref")
	}
}

func TestListTreeEntries(t *testing.T) {
	store, _ := setupBrowseRepo(t)
	repo, err := store.OpenRepo("ns", "browse")
	if err != nil {
		t.Fatal(err)
	}

	h, _ := git.ResolveRef(repo, "main")

	// Root listing
	entries, err := git.ListTreeEntries(repo, h, "")
	if err != nil {
		t.Fatalf("list root: %v", err)
	}
	names := map[string]string{}
	for _, e := range entries {
		names[e.Name] = e.Type
	}
	if names["README.md"] != "file" {
		t.Error("README.md missing or not file")
	}
	if names["docs"] != "directory" {
		t.Error("docs missing or not directory")
	}

	// Subdirectory listing
	entries, err = git.ListTreeEntries(repo, h, "docs")
	if err != nil {
		t.Fatalf("list docs: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "guide.txt" {
		t.Errorf("docs listing: got %v", entries)
	}
	if entries[0].Size <= 0 {
		t.Error("file size should be > 0")
	}

	// Feature branch has extra file
	hf, _ := git.ResolveRef(repo, "feature")
	entries, err = git.ListTreeEntries(repo, hf, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Name == "feature.txt" {
			found = true
		}
	}
	if !found {
		t.Error("feature.txt not found on feature branch")
	}

	// Non-existent path
	_, err = git.ListTreeEntries(repo, h, "nonexistent")
	if err == nil {
		t.Error("expected error for non-existent path")
	}
}

func TestReadFileContent(t *testing.T) {
	store, _ := setupBrowseRepo(t)
	repo, err := store.OpenRepo("ns", "browse")
	if err != nil {
		t.Fatal(err)
	}

	h, _ := git.ResolveRef(repo, "main")

	content, binary, err := git.ReadFileContent(repo, h, "README.md")
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	if binary {
		t.Error("README should not be binary")
	}
	if content != "# Hello World\n" {
		t.Errorf("content = %q, want %q", content, "# Hello World\n")
	}

	// Nested file
	content, _, err = git.ReadFileContent(repo, h, "docs/guide.txt")
	if err != nil {
		t.Fatalf("read guide.txt: %v", err)
	}
	if content != "guide content\n" {
		t.Errorf("guide content = %q", content)
	}

	// Non-existent file
	_, _, err = git.ReadFileContent(repo, h, "nope.txt")
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}

func TestReadFileContentBinary(t *testing.T) {
	baseDir := t.TempDir()
	store := git.NewStore(baseDir)
	store.CreateRepo("ns", "bin")
	projPath, _ := store.ProjectPath("ns", "bin")

	// Write a file with null bytes
	data := []byte("hello\x00world")
	os.WriteFile(filepath.Join(projPath, "binary.dat"), data, 0o644)
	runGit(t, projPath, "add", ".")
	runGit(t, projPath, "commit", "-m", "add binary")

	repo, _ := store.OpenRepo("ns", "bin")
	h, _ := git.ResolveRef(repo, "")

	content, binary, err := git.ReadFileContent(repo, h, "binary.dat")
	if err != nil {
		t.Fatal(err)
	}
	if !binary {
		t.Error("expected binary detection")
	}
	if !strings.Contains(content, "Binary file") {
		t.Errorf("expected binary message, got %q", content)
	}
}

func TestGetBranches(t *testing.T) {
	store, _ := setupBrowseRepo(t)
	repo, err := store.OpenRepo("ns", "browse")
	if err != nil {
		t.Fatal(err)
	}

	branches, err := git.GetBranches(repo)
	if err != nil {
		t.Fatal(err)
	}

	branchMap := map[string]git.BranchInfo{}
	for _, b := range branches {
		branchMap[b.Name] = b
	}

	if _, ok := branchMap["main"]; !ok {
		t.Error("main branch missing")
	}
	if _, ok := branchMap["feature"]; !ok {
		t.Error("feature branch missing")
	}
	if !branchMap["main"].IsDefault {
		t.Error("main should be marked as default")
	}
	if branchMap["feature"].IsDefault {
		t.Error("feature should not be default")
	}
	if branchMap["main"].HeadSHA == "" {
		t.Error("main head SHA should not be empty")
	}
}

func TestGetLog(t *testing.T) {
	store, _ := setupBrowseRepo(t)
	repo, err := store.OpenRepo("ns", "browse")
	if err != nil {
		t.Fatal(err)
	}

	h, _ := git.ResolveRef(repo, "main")

	// All commits on main
	entries, err := git.GetLog(repo, h, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("main log: got %d entries, want 2", len(entries))
	}
	if entries[0].Message != "update readme" {
		t.Errorf("latest commit message = %q", entries[0].Message)
	}

	// Path filter
	entries, err = git.GetLog(repo, h, "README.md", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("README log: got %d entries, want 2", len(entries))
	}

	entries, err = git.GetLog(repo, h, "docs/guide.txt", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("guide.txt log: got %d entries, want 1", len(entries))
	}

	// Limit
	entries, err = git.GetLog(repo, h, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("limited log: got %d entries, want 1", len(entries))
	}

	// Feature branch log
	hf, _ := git.ResolveRef(repo, "feature")
	entries, err = git.GetLog(repo, hf, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Errorf("feature log: got %d entries, want 3", len(entries))
	}
}

func TestGetCommitDetail(t *testing.T) {
	store, _ := setupBrowseRepo(t)
	repo, err := store.OpenRepo("ns", "browse")
	if err != nil {
		t.Fatal(err)
	}

	h, _ := git.ResolveRef(repo, "main")
	detail, err := git.GetCommitDetail(repo, h)
	if err != nil {
		t.Fatal(err)
	}

	if detail.SHA != h.String() {
		t.Error("SHA mismatch")
	}
	if detail.Message != "update readme\n" && detail.Message != "update readme" {
		t.Errorf("message = %q", detail.Message)
	}
	if detail.Author.Name != "Test" {
		t.Errorf("author name = %q", detail.Author.Name)
	}
	if len(detail.Parents) != 1 {
		t.Errorf("parents = %d, want 1", len(detail.Parents))
	}
	if detail.Stats.FilesChanged != 1 {
		t.Errorf("files changed = %d, want 1", detail.Stats.FilesChanged)
	}
	if detail.Stats.Insertions <= 0 {
		t.Error("expected insertions > 0")
	}
}

func TestGetCommitDetailInitial(t *testing.T) {
	baseDir := t.TempDir()
	store := git.NewStore(baseDir)
	store.CreateRepo("ns", "init")
	projPath, _ := store.ProjectPath("ns", "init")

	os.WriteFile(filepath.Join(projPath, "a.txt"), []byte("aaa\n"), 0o644)
	runGit(t, projPath, "add", ".")
	runGit(t, projPath, "commit", "-m", "first")

	repo, _ := store.OpenRepo("ns", "init")
	h, _ := git.ResolveRef(repo, "")

	detail, err := git.GetCommitDetail(repo, h)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Parents) != 0 {
		t.Errorf("initial commit should have 0 parents, got %d", len(detail.Parents))
	}
	if detail.Stats.FilesChanged != 1 {
		t.Errorf("files changed = %d, want 1", detail.Stats.FilesChanged)
	}
}

func TestReadFileAtPreviousCommit(t *testing.T) {
	store, _ := setupBrowseRepo(t)
	repo, err := store.OpenRepo("ns", "browse")
	if err != nil {
		t.Fatal(err)
	}

	// Get the initial commit (parent of HEAD)
	h, _ := git.ResolveRef(repo, "main")
	detail, _ := git.GetCommitDetail(repo, h)
	parentSHA := detail.Parents[0]

	parentHash, err := git.ResolveRef(repo, parentSHA)
	if err != nil {
		t.Fatal(err)
	}

	content, _, err := git.ReadFileContent(repo, parentHash, "README.md")
	if err != nil {
		t.Fatal(err)
	}
	if content != "# Hello\n" {
		t.Errorf("historical content = %q, want %q", content, "# Hello\n")
	}
}
