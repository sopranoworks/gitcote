package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sopranoworks/gityard/internal/git"
	"github.com/sopranoworks/gityard/internal/integrity"
	"github.com/sopranoworks/gityard/internal/pr"
	"github.com/sopranoworks/shoka/pkg/auth"
	"github.com/sopranoworks/shoka/pkg/oauthstore"
)

func TestFullE2E_PushToMerge(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	baseDir := t.TempDir()
	gitStore := git.NewStore(baseDir)
	ns, proj := "e2e", "pushmerge"
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

	evtCtx := &eventContext{
		gitStore:    gitStore,
		integrityHS: integrityStore,
		oauthStore:  oauthSt,
		agentCfg:    AgentSpawnConfig{},
		logger:      logger,
	}

	autoConfirm := true
	if err := integrityStore.SetGlobalPREventSettings(&integrity.PREventSettings{
		OnConfirmed: &integrity.ConfirmAction{AutoConfirm: &autoConfirm},
	}); err != nil {
		t.Fatal(err)
	}

	gitHTTP := git.NewHandler(gitStore, logger)
	gitHTTP.PostReceive = func(namespace, project string, principal auth.Principal, pushOpts []string) {
		handlePostReceive(gitStore, logger, namespace, project, principal, pushOpts, evtCtx)
	}

	mcpServer := mcp.NewServer(
		&mcp.Implementation{Name: "gityard-test", Version: "0.0.0-test"},
		nil,
	)
	registerPRTools(mcpServer, gitStore, &seedContext{gitStore: gitStore}, evtCtx)

	authenticator := auth.New(auth.Config{
		ValidateToken: func(tok string) (auth.Principal, auth.RejectReason, bool) {
			if tok == "" {
				return auth.Principal{}, auth.ReasonMissingBearer, false
			}
			return auth.Principal{
				Name:  "admin",
				Email: "admin@test.com",
				Scope: "*",
			}, "", true
		},
	})

	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return mcpServer },
		nil,
	)
	httpMux := http.NewServeMux()
	httpMux.Handle("/mcp", authenticator.Middleware(mcpHandler))
	httpMux.Handle("/", gitHTTP)
	ts := httptest.NewServer(httpMux)
	defer ts.Close()

	// --- Step 1: Initial commit on main ---
	cloneDir := t.TempDir()
	runGitE2E(t, cloneDir, "clone", ts.URL+"/"+ns+"/"+proj+".git", "repo")
	repoDir := filepath.Join(cloneDir, "repo")

	writeTestFile(t, repoDir, "README.md", "# Test Project\n")
	runGitE2E(t, repoDir, "add", "README.md")
	runGitE2E(t, repoDir, "commit", "-m", "initial commit")
	runGitE2E(t, repoDir, "push", "-u", "origin", "HEAD:refs/heads/main")

	// --- Step 2: Push feature branch with push options ---
	runGitE2E(t, repoDir, "checkout", "-b", "feat/hello")
	writeTestFile(t, repoDir, "feature.go", "package main\n\nfunc hello() {}\n")
	runGitE2E(t, repoDir, "add", "feature.go")
	runGitE2E(t, repoDir, "commit", "-m", "add feature")
	runGitE2E(t, repoDir, "push", "-u", "origin", "feat/hello",
		"-o", "pull_request.create",
		"-o", "pull_request.title=Implement hello",
		"-o", "pull_request.order_files=/test/prtest/directives/xxx.md",
		"-o", "pull_request.result_files=/test/prtest/reports/xxx.md,/test/prtest/reports/yyy.md",
	)

	// --- Step 3: Verify PR has order_files and result_files ---
	prStore, err := getPRStore(baseDir, ns, proj)
	if err != nil {
		t.Fatal(err)
	}
	prs, err := prStore.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR, got %d", len(prs))
	}
	thePR := prs[0]
	if thePR.SourceBranch != "feat/hello" {
		t.Fatalf("PR source = %q, want feat/hello", thePR.SourceBranch)
	}
	if thePR.Title != "Implement hello" {
		t.Fatalf("PR title = %q, want %q", thePR.Title, "Implement hello")
	}
	if len(thePR.OrderFiles) != 1 || thePR.OrderFiles[0] != "/test/prtest/directives/xxx.md" {
		t.Fatalf("PR order_files = %v, want [/test/prtest/directives/xxx.md]", thePR.OrderFiles)
	}
	if len(thePR.ResultFiles) != 2 {
		t.Fatalf("PR result_files = %v, want 2 entries", thePR.ResultFiles)
	}
	if thePR.ResultFiles[0] != "/test/prtest/reports/xxx.md" || thePR.ResultFiles[1] != "/test/prtest/reports/yyy.md" {
		t.Fatalf("PR result_files = %v, want [/test/prtest/reports/xxx.md /test/prtest/reports/yyy.md]", thePR.ResultFiles)
	}
	t.Logf("Step 3 PASS: PR #%d created with order_files=%v result_files=%v",
		thePR.Number, thePR.OrderFiles, thePR.ResultFiles)

	// --- Step 4: Approve via MCP → auto-merge ---
	mcpClient := mcp.NewClient(
		&mcp.Implementation{Name: "test-client", Version: "1.0"},
		nil,
	)
	ctx := context.Background()

	mcpTransport := &mcp.StreamableClientTransport{
		Endpoint:             ts.URL + "/mcp",
		HTTPClient:           &http.Client{Transport: &bearerTransport{token: "test-admin-token"}},
		DisableStandaloneSSE: true,
	}

	session, err := mcpClient.Connect(ctx, mcpTransport, nil)
	if err != nil {
		t.Fatalf("MCP connect: %v", err)
	}

	approveResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "approve_pull_request",
		Arguments: map[string]any{
			"namespace":    ns,
			"project_name": proj,
			"number":       float64(thePR.Number),
		},
	})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if approveResult.IsError {
		t.Fatal("approve_pull_request returned error")
	}

	// Wait for auto-merge goroutine
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		merged, _ := prStore.Get(thePR.Number)
		if merged.State == pr.StateMerged {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	merged, err := prStore.Get(thePR.Number)
	if err != nil {
		t.Fatal(err)
	}
	if merged.State != pr.StateMerged {
		t.Fatalf("PR state = %q, want merged", merged.State)
	}
	if merged.MergeCommit == "" {
		t.Fatal("PR merge_commit is empty")
	}

	repo, err := gitStore.OpenRepo(ns, proj)
	if err != nil {
		t.Fatal(err)
	}
	mainHash, err := git.ResolveBranch(repo, "main")
	if err != nil {
		t.Fatalf("resolve main: %v", err)
	}
	if mainHash == plumbing.ZeroHash {
		t.Fatal("main is still empty after merge")
	}
	t.Logf("Step 4 PASS: PR #%d auto-merged, main=%s", thePR.Number, mainHash.String()[:8])

	// --- Step 5: Close PR ---
	runGitE2E(t, repoDir, "checkout", "main")
	runGitE2E(t, repoDir, "pull", "origin", "main")
	runGitE2E(t, repoDir, "checkout", "-b", "feat/stale")
	writeTestFile(t, repoDir, "stale.go", "package main\n")
	runGitE2E(t, repoDir, "add", "stale.go")
	runGitE2E(t, repoDir, "commit", "-m", "stale change")
	runGitE2E(t, repoDir, "push", "-u", "origin", "feat/stale",
		"-o", "pull_request.create",
		"-o", "pull_request.title=Stale PR",
	)

	openPRs, _ := prStore.List(pr.StateOpen)
	if len(openPRs) == 0 {
		t.Fatal("no open PR after second push")
	}
	stalePR := openPRs[0]

	now := time.Now()
	stalePR.State = pr.StateClosed
	stalePR.ClosedAt = &now
	stalePR.UpdatedAt = now
	if err := prStore.Update(&stalePR); err != nil {
		t.Fatal(err)
	}

	closed, _ := prStore.Get(stalePR.Number)
	if closed.State != pr.StateClosed {
		t.Fatalf("closed PR state = %q, want closed", closed.State)
	}
	t.Logf("Step 5 PASS: PR #%d closed", stalePR.Number)
}

func TestAutoMergePR_EmptyRepo(t *testing.T) {
	baseDir := t.TempDir()
	gitStore := git.NewStore(baseDir)
	ns, proj := "default", "emptymrg"
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

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
	featHash := commitAllFiles(t, repo, projDir, "feat-1", "add hello.txt")

	repo.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.HEAD, plumbing.NewBranchReferenceName(defaultBranch)))

	targetHash, _ := git.ResolveBranch(repo, defaultBranch)
	if targetHash != plumbing.ZeroHash {
		t.Fatalf("expected ZeroHash for empty main, got %v", targetHash)
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

	evtCtx := &eventContext{
		gitStore:    gitStore,
		integrityHS: integrityStore,
		oauthStore:  oauthSt,
		agentCfg:    AgentSpawnConfig{},
		logger:      logger,
	}

	prStore, err := getPRStore(baseDir, ns, proj)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	p := &pr.PullRequest{
		RepoNamespace: ns,
		RepoProject:   proj,
		Title:         "feat-1",
		SourceBranch:  "feat-1",
		TargetBranch:  defaultBranch,
		Author:        "test",
		State:         pr.StateApproved,
		Mergeable:     pr.MergeableClean,
		SourceCommit:  featHash.String(),
		TargetCommit:  plumbing.ZeroHash.String(),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	num, err := prStore.Create(p)
	if err != nil {
		t.Fatal(err)
	}

	if err := autoMergePR(evtCtx, p); err != nil {
		t.Fatalf("autoMergePR on empty repo: %v", err)
	}

	merged, err := prStore.Get(num)
	if err != nil {
		t.Fatal(err)
	}
	if merged.State != pr.StateMerged {
		t.Fatalf("PR state = %q, want merged", merged.State)
	}

	mainHash, err := git.ResolveBranch(repo, defaultBranch)
	if err != nil {
		t.Fatalf("resolve main: %v", err)
	}
	if mainHash != featHash {
		t.Fatalf("main HEAD = %v, want feat-1 HEAD %v (fast-forward)", mainHash, featHash)
	}
	t.Logf("PASS: autoMergePR on empty repo succeeded, main=%s", mainHash.String()[:8])
}

func runGitE2E(t *testing.T, dir string, args ...string) {
	t.Helper()
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
		t.Fatalf("git %s failed: %v\nstderr: %s\nstdout: %s",
			strings.Join(args, " "), err, stderr.String(), stdout.String())
	}
}
