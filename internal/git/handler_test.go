package git_test

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"log/slog"

	"github.com/sopranoworks/gityard/internal/git"
	"github.com/sopranoworks/shoka/pkg/auth"
)

func TestSmartHTTPRoundTrip(t *testing.T) {
	baseDir := t.TempDir()
	store := git.NewStore(baseDir)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	var mu sync.Mutex
	var postReceiveCalls int
	handler := git.NewHandler(store, logger)
	handler.PostReceive = func(ns, proj string, p auth.Principal) {
		mu.Lock()
		defer mu.Unlock()
		postReceiveCalls++
	}

	ts := httptest.NewServer(http.StripPrefix("/git", handler))
	defer ts.Close()

	// Wrap with /git prefix to match the real URL pattern.
	mux := http.NewServeMux()
	mux.Handle("/git/", handler)
	ts2 := httptest.NewServer(mux)
	defer ts2.Close()

	if err := store.CreateRepo("test-ns", "test-proj"); err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	// Clone (empty repo).
	cloneDir := t.TempDir()
	runGit(t, cloneDir, "clone", ts2.URL+"/git/test-ns/test-proj.git", "repo")
	repoDir := filepath.Join(cloneDir, "repo")

	// Make a commit and push.
	if err := os.WriteFile(filepath.Join(repoDir, "hello.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "hello.txt")
	runGit(t, repoDir, "commit", "-m", "initial commit")
	runGit(t, repoDir, "push", "-u", "origin", "HEAD:refs/heads/main")

	// Clone again into a fresh directory and verify the pushed content.
	verifyDir := t.TempDir()
	runGit(t, verifyDir, "clone", ts2.URL+"/git/test-ns/test-proj.git", "verify")
	verifyRepoDir := filepath.Join(verifyDir, "verify")
	content, err := os.ReadFile(filepath.Join(verifyRepoDir, "hello.txt"))
	if err != nil {
		t.Fatalf("read hello.txt: %v", err)
	}
	if got := string(content); got != "hello world\n" {
		t.Errorf("content = %q, want %q", got, "hello world\n")
	}

	// PostReceive should have been called once.
	mu.Lock()
	calls := postReceiveCalls
	mu.Unlock()
	if calls != 1 {
		t.Errorf("PostReceive called %d times, want 1", calls)
	}
}

func TestSmartHTTPNonexistentRepo(t *testing.T) {
	baseDir := t.TempDir()
	store := git.NewStore(baseDir)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	handler := git.NewHandler(store, logger)
	mux := http.NewServeMux()
	mux.Handle("/git/", handler)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/git/no/such.git/info/refs?service=git-upload-pack")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestSmartHTTPBasicAuth(t *testing.T) {
	baseDir := t.TempDir()
	store := git.NewStore(baseDir)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	handler := git.NewHandler(store, logger)

	validate := func(token string) (auth.Principal, auth.RejectReason, bool) {
		if token == "valid-token" {
			return auth.Principal{Name: "testuser", Email: "test@example.com", Scope: "*"}, "", true
		}
		return auth.Principal{}, auth.ReasonInvalidToken, false
	}
	mux := http.NewServeMux()
	mux.Handle("/git/", git.BasicAuthMiddleware(validate)(handler))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	if err := store.CreateRepo("default", "auth-test"); err != nil {
		t.Fatal(err)
	}

	// No token → 401 (auth middleware requires credentials when validateToken is set).
	resp, err := http.Get(ts.URL + "/git/default/auth-test.git/info/refs?service=git-upload-pack")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-auth status = %d, want 401", resp.StatusCode)
	}

	// Bad token → 401.
	req, _ := http.NewRequest("GET", ts.URL+"/git/default/auth-test.git/info/refs?service=git-upload-pack", nil)
	req.SetBasicAuth("x-token", "bad-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad-token status = %d, want 401", resp.StatusCode)
	}

	// Valid token → 200.
	req, _ = http.NewRequest("GET", ts.URL+"/git/default/auth-test.git/info/refs?service=git-upload-pack", nil)
	req.SetBasicAuth("x-token", "valid-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("valid-token status = %d, want 200", resp.StatusCode)
	}
}

func TestRepoLayout(t *testing.T) {
	store := git.NewStore(t.TempDir())
	if err := store.CreateRepo("ns", "proj"); err != nil {
		t.Fatal(err)
	}

	projPath, _ := store.ProjectPath("ns", "proj")

	// .git/ subdirectory should exist.
	gitDir := filepath.Join(projPath, ".git")
	if _, err := os.Stat(filepath.Join(gitDir, "HEAD")); err != nil {
		t.Errorf(".git/HEAD not found: %v", err)
	}

	// Project directory should be clean for application data.
	entries, _ := os.ReadDir(projPath)
	for _, e := range entries {
		if e.Name() != ".git" {
			t.Errorf("unexpected entry in project dir: %s", e.Name())
		}
	}
}

func TestConcurrentPush(t *testing.T) {
	baseDir := t.TempDir()
	store := git.NewStore(baseDir)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	handler := git.NewHandler(store, logger)
	mux := http.NewServeMux()
	mux.Handle("/git/", handler)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	if err := store.CreateRepo("default", "conc"); err != nil {
		t.Fatal(err)
	}

	// Initial push to create main branch.
	workDir := t.TempDir()
	runGit(t, workDir, "clone", ts.URL+"/git/default/conc.git", "setup")
	setupDir := filepath.Join(workDir, "setup")
	os.WriteFile(filepath.Join(setupDir, "init.txt"), []byte("init"), 0o644)
	runGit(t, setupDir, "add", "init.txt")
	runGit(t, setupDir, "commit", "-m", "init")
	runGit(t, setupDir, "push", "origin", "HEAD:refs/heads/main")

	// Two parallel pushes to different branches.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			d := t.TempDir()
			runGitErr(t, d, "clone", ts.URL+"/git/default/conc.git", fmt.Sprintf("w%d", idx))
			wd := filepath.Join(d, fmt.Sprintf("w%d", idx))
			os.WriteFile(filepath.Join(wd, fmt.Sprintf("file%d.txt", idx)), []byte(fmt.Sprintf("data%d", idx)), 0o644)
			runGitErr(t, wd, "add", ".")
			runGitErr(t, wd, "commit", "-m", fmt.Sprintf("commit %d", idx))
			errs[idx] = runGitResult(wd, "push", "origin", fmt.Sprintf("HEAD:refs/heads/branch%d", idx))
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent push %d failed: %v", i, err)
		}
	}

	// Verify both branches exist by cloning and checking refs.
	verifyDir := t.TempDir()
	runGit(t, verifyDir, "clone", ts.URL+"/git/default/conc.git", "verify")
	vd := filepath.Join(verifyDir, "verify")
	runGit(t, vd, "branch", "-r")
}

func TestRepoCreateAndList(t *testing.T) {
	store := git.NewStore(t.TempDir())

	if err := store.CreateRepo("ns1", "proj-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRepo("ns1", "proj-b"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRepo("ns2", "proj-c"); err != nil {
		t.Fatal(err)
	}

	all, err := store.ListProjects("")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Errorf("list all: got %d, want 3", len(all))
	}

	ns1, err := store.ListProjects("ns1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ns1) != 2 {
		t.Errorf("list ns1: got %d, want 2", len(ns1))
	}

	ok, _ := store.RepoExists("ns1", "proj-a")
	if !ok {
		t.Error("proj-a should exist")
	}
	ok, _ = store.RepoExists("ns1", "nope")
	if ok {
		t.Error("nope should not exist")
	}

	err = store.CreateRepo("ns1", "proj-a")
	if err == nil {
		t.Error("duplicate create should fail")
	}
}

func TestIsValidName(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want bool
	}{
		{"hello", true},
		{"Hello_World-123", true},
		{"", false},
		{"has space", false},
		{"has/slash", false},
		{"has.dot", false},
		{".hidden", false},
	} {
		t.Run(fmt.Sprintf("%q", tt.in), func(t *testing.T) {
			if got := git.IsValidName(tt.in); got != tt.want {
				t.Errorf("IsValidName(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if err := runGitResult(dir, args...); err != nil {
		t.Fatalf("git %s failed: %v", strings.Join(args, " "), err)
	}
}

func runGitErr(t *testing.T, dir string, args ...string) {
	t.Helper()
	if err := runGitResult(dir, args...); err != nil {
		t.Errorf("git %s failed: %v", strings.Join(args, " "), err)
	}
}

func runGitResult(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	var stderr, stdout bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%v\nstderr: %s\nstdout: %s", err, stderr.String(), stdout.String())
	}
	return nil
}
