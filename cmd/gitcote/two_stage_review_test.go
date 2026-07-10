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

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sopranoworks/gitcote/internal/git"
	"github.com/sopranoworks/gitcote/internal/integrity"
	"github.com/sopranoworks/gitcote/internal/pr"
	"github.com/sopranoworks/shoka/pkg/auth"
	"github.com/sopranoworks/shoka/pkg/oauthstore"
)

func setup2StageTest(t *testing.T, ns, proj string) (*git.Store, *integrity.Store, *pr.Store, *eventContext) {
	t.Helper()
	baseDir := t.TempDir()
	gitStore := git.NewStore(baseDir)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if err := gitStore.CreateRepo(ns, proj); err != nil {
		t.Fatal(err)
	}

	integrityStore, err := integrity.Open(filepath.Join(baseDir, "repo_heads.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { integrityStore.Close() })
	headStore = integrityStore

	oauthSt, err := oauthstore.Open(filepath.Join(baseDir, "oauth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { oauthSt.Close() })

	ec := &eventContext{
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

	return gitStore, integrityStore, prStore, ec
}

func create2StagePR(t *testing.T, prStore *pr.Store, ns, proj string, num int, source string) *pr.PullRequest {
	t.Helper()
	now := time.Now()
	p := &pr.PullRequest{
		RepoNamespace: ns,
		RepoProject:   proj,
		Title:         source,
		SourceBranch:  source,
		TargetBranch:  "main",
		Author:        "test",
		State:         pr.StateOpen,
		Mergeable:     pr.MergeableClean,
		CreatedAt:     now,
		UpdatedAt:     now,
		OrderFiles:    []string{"/test/orders/directive.md"},
		ResultFiles:   []string{"/test/results/report.md"},
		ReviewFiles:   []string{},
	}
	n, err := prStore.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if int(n) != num {
		t.Fatalf("expected PR #%d, got #%d", num, n)
	}
	return p
}

func TestOperatorReject(t *testing.T) {
	ns, proj := "default", "opreject"
	gitStore, hs, prStore, ec := setup2StageTest(t, ns, proj)

	pr1 := create2StagePR(t, prStore, ns, proj, 1, "feat-1")
	hs.EnqueuePR(ns, proj, 1)

	create2StagePR(t, prStore, ns, proj, 2, "feat-2")
	hs.EnqueuePR(ns, proj, 2)

	// Approve PR #1 (simulate reviewer approval)
	now := time.Now()
	pr1.State = pr.StateApproved
	pr1.ApprovedBy = "reviewer-agent"
	pr1.ApprovedAt = &now
	pr1.ReviewFiles = []string{"/test/reviews/review-1.md"}
	pr1.UpdatedAt = now
	if err := prStore.Update(pr1); err != nil {
		t.Fatal(err)
	}

	// Operator rejects the approved PR
	_, err := operatorRejectPR(gitStore, ec, ns, proj, 1, "needs architectural rework")
	if err != nil {
		t.Fatalf("operator reject failed: %v", err)
	}

	// Verify PR state
	p, err := prStore.Get(1)
	if err != nil {
		t.Fatal(err)
	}
	if p.State != pr.StateRejected {
		t.Fatalf("PR #1 state = %q, want rejected", p.State)
	}

	// Verify queue slot released and next PR dequeued
	time.Sleep(50 * time.Millisecond)
	q, _ := hs.GetPRQueue(ns, proj)
	if q.ActivePR != 2 {
		t.Errorf("after operator reject: active = %d, want 2", q.ActivePR)
	}
	if len(q.Waiting) != 0 {
		t.Errorf("after operator reject: waiting = %v, want []", q.Waiting)
	}
	t.Log("PASS: operator reject → rejected state, queue slot released, next PR dequeued")
}

func TestOperatorReject_FromOpenAndApproved(t *testing.T) {
	ns, proj := "default", "oprejectonly"
	gitStore, _, prStore, ec := setup2StageTest(t, ns, proj)

	// Reject from open state → should succeed
	create2StagePR(t, prStore, ns, proj, 1, "feat-1")
	rejected, err := operatorRejectPR(gitStore, ec, ns, proj, 1, "not needed")
	if err != nil {
		t.Fatalf("reject from open: unexpected error: %v", err)
	}
	if rejected.State != pr.StateRejected {
		t.Fatalf("PR #1 state = %q, want rejected", rejected.State)
	}
	t.Log("PASS: operator reject from open succeeds")

	// Reject from merged state → should fail
	create2StagePR(t, prStore, ns, proj, 2, "feat-2")
	p2, _ := prStore.Get(2)
	p2.State = pr.StateMerged
	if err := prStore.Update(p2); err != nil {
		t.Fatal(err)
	}
	_, err = operatorRejectPR(gitStore, ec, ns, proj, 2, "test")
	if err == nil {
		t.Fatal("expected error when rejecting merged PR")
	}
	t.Log("PASS: operator reject from merged correctly returns error")
}

func TestOperatorConfirmMerge(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	baseDir := t.TempDir()
	gitStore := git.NewStore(baseDir)
	ns, proj := "e2e", "confirmmerge"
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

	ec := &eventContext{
		gitStore:    gitStore,
		integrityHS: integrityStore,
		oauthStore:  oauthSt,
		agentCfg:    AgentSpawnConfig{},
		logger:      logger,
	}

	// auto_confirm = false
	autoConfirm := false
	if err := integrityStore.SetGlobalPREventSettings(&integrity.PREventSettings{
		OnConfirmed: &integrity.ConfirmAction{AutoConfirm: &autoConfirm},
	}); err != nil {
		t.Fatal(err)
	}

	gitHTTP := git.NewHandler(gitStore, logger)
	gitHTTP.PostReceive = func(namespace, project string, principal auth.Principal, pushOpts []string) {
		handlePostReceive(gitStore, logger, namespace, project, principal, pushOpts, ec)
	}

	mcpServer := mcp.NewServer(
		&mcp.Implementation{Name: "gitcote-test", Version: "0.0.0-test"},
		nil,
	)
	registerPRTools(mcpServer, gitStore, &seedContext{gitStore: gitStore}, ec)

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

	// Initial commit
	cloneDir := t.TempDir()
	runGit2Stage(t, cloneDir, "clone", ts.URL+"/"+ns+"/"+proj+".git", "repo")
	repoDir := filepath.Join(cloneDir, "repo")
	writeTestFile(t, repoDir, "README.md", "# Confirm Merge Test\n")
	runGit2Stage(t, repoDir, "add", "README.md")
	runGit2Stage(t, repoDir, "commit", "-m", "initial commit")
	runGit2Stage(t, repoDir, "push", "-u", "origin", "HEAD:refs/heads/main")

	// Feature branch
	runGit2Stage(t, repoDir, "checkout", "-b", "feat/confirm")
	writeTestFile(t, repoDir, "confirm.go", "package main\n")
	runGit2Stage(t, repoDir, "add", "confirm.go")
	runGit2Stage(t, repoDir, "commit", "-m", "confirm feature")
	runGit2Stage(t, repoDir, "push", "-u", "origin", "feat/confirm",
		"-o", "pull_request.create",
		"-o", "pull_request.title=Confirm test",
	)

	prStore, err := getPRStore(baseDir, ns, proj)
	if err != nil {
		t.Fatal(err)
	}

	// Approve via MCP
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

	prs, _ := prStore.List(pr.StateOpen)
	if len(prs) == 0 {
		t.Fatal("no open PR")
	}
	thePR := prs[0]

	_, err = session.CallTool(ctx, &mcp.CallToolParams{
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

	// Wait a bit and verify NOT auto-merged
	time.Sleep(200 * time.Millisecond)
	p, _ := prStore.Get(thePR.Number)
	if p.State != pr.StateApproved {
		t.Fatalf("PR state = %q, want approved (auto_confirm=false)", p.State)
	}

	// Operator confirms via autoMergePR (same logic as PR_MERGE handler)
	if err := autoMergePR(ec, p); err != nil {
		t.Fatalf("manual merge failed: %v", err)
	}

	merged, _ := prStore.Get(thePR.Number)
	if merged.State != pr.StateMerged {
		t.Fatalf("PR state = %q, want merged", merged.State)
	}

	// Verify queue slot released
	q, _ := integrityStore.GetPRQueue(ns, proj)
	if q.ActivePR != 0 {
		t.Errorf("queue active = %d, want 0 (idle)", q.ActivePR)
	}
	t.Logf("PASS: auto_confirm=false → approve → operator confirm → merged")
}

func TestListPRsByStatus(t *testing.T) {
	ns, proj := "default", "liststatus"
	_, _, prStore, ec := setup2StageTest(t, ns, proj)

	// Create 3 PRs with different states
	pr1 := create2StagePR(t, prStore, ns, proj, 1, "feat-1")

	pr2 := create2StagePR(t, prStore, ns, proj, 2, "feat-2")
	now := time.Now()
	pr2.State = pr.StateApproved
	pr2.ApprovedBy = "agent"
	pr2.ApprovedAt = &now
	pr2.UpdatedAt = now
	prStore.Update(pr2)

	pr3 := create2StagePR(t, prStore, ns, proj, 3, "feat-3")
	pr3.State = pr.StateRejected
	pr3.UpdatedAt = time.Now()
	prStore.Update(pr3)

	// MCP list with status filter
	mcpServer := mcp.NewServer(
		&mcp.Implementation{Name: "gitcote-test", Version: "0.0.0-test"},
		nil,
	)
	registerPRTools(mcpServer, ec.gitStore, &seedContext{gitStore: ec.gitStore}, ec)

	authenticator := auth.New(auth.Config{
		ValidateToken: func(tok string) (auth.Principal, auth.RejectReason, bool) {
			return auth.Principal{Name: "test", Email: "t@t.com", Scope: "*"}, "", true
		},
	})
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return mcpServer },
		nil,
	)
	mux := http.NewServeMux()
	mux.Handle("/mcp", authenticator.Middleware(mcpHandler))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	mcpClient := mcp.NewClient(
		&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	ctx := context.Background()
	session, err := mcpClient.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             ts.URL + "/mcp",
		HTTPClient:           &http.Client{Transport: &bearerTransport{token: "t"}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Filter by rejected
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "list_pull_requests",
		Arguments: map[string]any{
			"namespace":    ns,
			"project_name": proj,
			"state":        "rejected",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := extractText(result)
	if !strings.Contains(text, "feat-3") {
		t.Fatalf("rejected filter missing feat-3: %s", text)
	}
	if strings.Contains(text, "feat-1") || strings.Contains(text, "feat-2") {
		t.Fatalf("rejected filter contains non-rejected PRs: %s", text)
	}

	// Filter by open
	result2, _ := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "list_pull_requests",
		Arguments: map[string]any{
			"namespace":    ns,
			"project_name": proj,
			"state":        "open",
		},
	})
	text2 := extractText(result2)
	if !strings.Contains(text2, "feat-1") {
		t.Fatalf("open filter missing feat-1: %s", text2)
	}
	if strings.Contains(text2, "feat-2") || strings.Contains(text2, "feat-3") {
		t.Fatalf("open filter contains non-open PRs: %s", text2)
	}

	// No filter → all PRs
	result3, _ := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "list_pull_requests",
		Arguments: map[string]any{
			"namespace":    ns,
			"project_name": proj,
		},
	})
	text3 := extractText(result3)
	if !strings.Contains(text3, "feat-1") || !strings.Contains(text3, "feat-2") || !strings.Contains(text3, "feat-3") {
		t.Fatalf("no filter missing PRs: %s", text3)
	}

	_ = pr1
	t.Log("PASS: list_pull_requests status filter works correctly")
}

func TestCloseRejectedPR(t *testing.T) {
	ns, proj := "default", "closereject"
	gitStore, hs, prStore, ec := setup2StageTest(t, ns, proj)

	pr1 := create2StagePR(t, prStore, ns, proj, 1, "feat-1")
	hs.EnqueuePR(ns, proj, 1)

	// Approve
	now := time.Now()
	pr1.State = pr.StateApproved
	pr1.ApprovedBy = "agent"
	pr1.ApprovedAt = &now
	pr1.ReviewFiles = []string{"/test/review.md"}
	pr1.UpdatedAt = now
	prStore.Update(pr1)

	// Operator reject
	_, err := operatorRejectPR(gitStore, ec, ns, proj, 1, "needs work")
	if err != nil {
		t.Fatalf("operator reject failed: %v", err)
	}

	p, _ := prStore.Get(1)
	if p.State != pr.StateRejected {
		t.Fatalf("after operator reject: state = %q, want rejected", p.State)
	}

	// Close the rejected PR
	_, err = closePR(gitStore, ec, ns, proj, 1)
	if err != nil {
		t.Fatalf("close rejected PR failed: %v", err)
	}

	closed, _ := prStore.Get(1)
	if closed.State != pr.StateClosed {
		t.Fatalf("after close: state = %q, want closed", closed.State)
	}

	// list by rejected should NOT include it
	rejected, _ := prStore.List(pr.StateRejected)
	for _, r := range rejected {
		if r.Number == 1 {
			t.Fatal("closed PR #1 still appears in rejected list")
		}
	}
	t.Log("PASS: rejected PR → closed → no longer in rejected list")
}

func TestCloseValidStates(t *testing.T) {
	ns, proj := "default", "closevalid"
	gitStore, _, prStore, ec := setup2StageTest(t, ns, proj)

	tests := []struct {
		name    string
		state   pr.PRState
		wantErr bool
	}{
		{"open", pr.StateOpen, false},
		{"approved", pr.StateApproved, false},
		{"rejected", pr.StateRejected, false},
		{"interrupted", pr.StateInterrupted, false},
		{"merged", pr.StateMerged, true},
		{"closed", pr.StateClosed, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Now()
			p := &pr.PullRequest{
				RepoNamespace: ns,
				RepoProject:   proj,
				Title:         tt.name,
				SourceBranch:  tt.name,
				TargetBranch:  "main",
				Author:        "test",
				State:         tt.state,
				Mergeable:     pr.MergeableClean,
				CreatedAt:     now,
				UpdatedAt:     now,
				OrderFiles:    []string{},
				ResultFiles:   []string{},
			}
			if tt.state == pr.StateMerged {
				p.MergeCommit = "abc123"
				merged := now
				p.MergedAt = &merged
			}
			if tt.state == pr.StateClosed {
				closed := now
				p.ClosedAt = &closed
			}
			num, err := prStore.Create(p)
			if err != nil {
				t.Fatal(err)
			}

			_, closeErr := closePR(gitStore, ec, ns, proj, num)

			if tt.wantErr {
				if closeErr == nil {
					t.Errorf("state %q: expected error, got success", tt.state)
				}
			} else {
				if closeErr != nil {
					t.Errorf("state %q: unexpected error: %v", tt.state, closeErr)
				}
				p, _ := prStore.Get(num)
				if p.State != pr.StateClosed {
					t.Errorf("state %q: after close, state = %q, want closed", tt.state, p.State)
				}
			}
		})
	}
}

func TestFullE2E_2StageReview(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	baseDir := t.TempDir()
	gitStore := git.NewStore(baseDir)
	ns, proj := "e2e", "twostage"
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

	ec := &eventContext{
		gitStore:    gitStore,
		integrityHS: integrityStore,
		oauthStore:  oauthSt,
		agentCfg:    AgentSpawnConfig{},
		logger:      logger,
	}

	// auto_confirm = false for 2-stage review
	autoConfirm := false
	if err := integrityStore.SetGlobalPREventSettings(&integrity.PREventSettings{
		OnConfirmed: &integrity.ConfirmAction{AutoConfirm: &autoConfirm},
	}); err != nil {
		t.Fatal(err)
	}

	gitHTTP := git.NewHandler(gitStore, logger)
	gitHTTP.PostReceive = func(namespace, project string, principal auth.Principal, pushOpts []string) {
		handlePostReceive(gitStore, logger, namespace, project, principal, pushOpts, ec)
	}

	mcpServer := mcp.NewServer(
		&mcp.Implementation{Name: "gitcote-test", Version: "0.0.0-test"},
		nil,
	)
	registerPRTools(mcpServer, gitStore, &seedContext{gitStore: gitStore}, ec)

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

	// Initial commit
	cloneDir := t.TempDir()
	runGit2Stage(t, cloneDir, "clone", ts.URL+"/"+ns+"/"+proj+".git", "repo")
	repoDir := filepath.Join(cloneDir, "repo")
	writeTestFile(t, repoDir, "README.md", "# 2-Stage Test\n")
	runGit2Stage(t, repoDir, "add", "README.md")
	runGit2Stage(t, repoDir, "commit", "-m", "initial")
	runGit2Stage(t, repoDir, "push", "-u", "origin", "HEAD:refs/heads/main")

	// Feature branch with order/result files
	runGit2Stage(t, repoDir, "checkout", "-b", "feat/2stage")
	writeTestFile(t, repoDir, "feature.go", "package main\n")
	runGit2Stage(t, repoDir, "add", "feature.go")
	runGit2Stage(t, repoDir, "commit", "-m", "2stage feature")
	runGit2Stage(t, repoDir, "push", "-u", "origin", "feat/2stage",
		"-o", "pull_request.create",
		"-o", "pull_request.title=2-stage review test",
		"-o", "pull_request.order_files=/test/orders/directive.md",
		"-o", "pull_request.result_files=/test/results/report.md",
	)

	prStore, err := getPRStore(baseDir, ns, proj)
	if err != nil {
		t.Fatal(err)
	}

	mcpClient := mcp.NewClient(
		&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	ctx := context.Background()
	session, err := mcpClient.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             ts.URL + "/mcp",
		HTTPClient:           &http.Client{Transport: &bearerTransport{token: "t"}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	prs, _ := prStore.List(pr.StateOpen)
	if len(prs) == 0 {
		t.Fatal("no open PR")
	}
	thePR := prs[0]

	// Step 1: Reviewer approves with review_files
	reviewFiles := []any{"/test/reviews/review.md"}
	_, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name: "approve_pull_request",
		Arguments: map[string]any{
			"namespace":    ns,
			"project_name": proj,
			"number":       float64(thePR.Number),
			"review_files": reviewFiles,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Step 2: Verify NOT auto-merged (auto_confirm=false)
	time.Sleep(200 * time.Millisecond)
	p, _ := prStore.Get(thePR.Number)
	if p.State != pr.StateApproved {
		t.Fatalf("step 2: state = %q, want approved", p.State)
	}
	t.Log("Step 2 PASS: auto_confirm=false → PR stays approved")

	// Step 3: Operator rejects
	_, err = operatorRejectPR(gitStore, ec, ns, proj, thePR.Number, "need to rethink approach")
	if err != nil {
		t.Fatalf("step 3: operator reject failed: %v", err)
	}
	p, _ = prStore.Get(thePR.Number)
	if p.State != pr.StateRejected {
		t.Fatalf("step 3: state = %q, want rejected", p.State)
	}
	t.Log("Step 3 PASS: operator reject → rejected")

	// Step 4: Close the rejected PR
	_, err = closePR(gitStore, ec, ns, proj, thePR.Number)
	if err != nil {
		t.Fatalf("step 4: close failed: %v", err)
	}
	p, _ = prStore.Get(thePR.Number)
	if p.State != pr.StateClosed {
		t.Fatalf("step 4: state = %q, want closed", p.State)
	}
	t.Log("Step 4 PASS: rejected → closed")

	// Step 5: Create another PR, approve, operator confirms (merge)
	runGit2Stage(t, repoDir, "checkout", "main")
	runGit2Stage(t, repoDir, "pull", "origin", "main")
	runGit2Stage(t, repoDir, "checkout", "-b", "feat/confirm-path")
	writeTestFile(t, repoDir, "confirm.go", "package main\nfunc confirm() {}\n")
	runGit2Stage(t, repoDir, "add", "confirm.go")
	runGit2Stage(t, repoDir, "commit", "-m", "confirm path feature")
	runGit2Stage(t, repoDir, "push", "-u", "origin", "feat/confirm-path",
		"-o", "pull_request.create",
		"-o", "pull_request.title=Confirm path test",
	)

	prs2, _ := prStore.List(pr.StateOpen)
	if len(prs2) == 0 {
		t.Fatal("step 5: no open PR")
	}
	confirmPR := prs2[0]

	_, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name: "approve_pull_request",
		Arguments: map[string]any{
			"namespace":    ns,
			"project_name": proj,
			"number":       float64(confirmPR.Number),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(200 * time.Millisecond)
	p, _ = prStore.Get(confirmPR.Number)
	if p.State != pr.StateApproved {
		t.Fatalf("step 5: state = %q, want approved", p.State)
	}

	// Operator confirms via merge
	if err := autoMergePR(ec, p); err != nil {
		t.Fatalf("step 5: merge failed: %v", err)
	}
	p, _ = prStore.Get(confirmPR.Number)
	if p.State != pr.StateMerged {
		t.Fatalf("step 5: state = %q, want merged", p.State)
	}
	t.Log("Step 5 PASS: approve → operator confirm → merged")
}

func extractText(result *mcp.CallToolResult) string {
	for _, c := range result.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

func TestReviewIncomplete_RetryViaMCP(t *testing.T) {
	ns, proj := "default", "retryinc"
	_, hs, prStore, ec := setup2StageTest(t, ns, proj)

	pr1 := create2StagePR(t, prStore, ns, proj, 1, "feat-retry")
	hs.EnqueuePR(ns, proj, 1)

	create2StagePR(t, prStore, ns, proj, 2, "feat-queued")
	hs.EnqueuePR(ns, proj, 2)

	// Simulate review_incomplete interrupt (slot retained)
	markInterrupted(prStore, pr1, "review_incomplete",
		"agent exited successfully but did not approve or reject",
		"default_claude_reviewer", "reviewer", ec.logger)

	q, _ := hs.GetPRQueue(ns, proj)
	if q.ActivePR != 1 {
		t.Fatalf("after interrupt: active = %d, want 1 (slot retained)", q.ActivePR)
	}

	// Set up MCP server and call retry_pr_agent
	mcpServer := mcp.NewServer(
		&mcp.Implementation{Name: "gitcote-test", Version: "0.0.0-test"},
		nil,
	)
	registerPRTools(mcpServer, ec.gitStore, &seedContext{gitStore: ec.gitStore}, ec)

	authenticator := auth.New(auth.Config{
		ValidateToken: func(tok string) (auth.Principal, auth.RejectReason, bool) {
			return auth.Principal{Name: "admin", Email: "a@t.com", Scope: "*"}, "", true
		},
	})
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return mcpServer },
		nil,
	)
	mux := http.NewServeMux()
	mux.Handle("/mcp", authenticator.Middleware(mcpHandler))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	mcpClient := mcp.NewClient(
		&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	ctx := context.Background()
	session, err := mcpClient.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             ts.URL + "/mcp",
		HTTPClient:           &http.Client{Transport: &bearerTransport{token: "t"}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "retry_pr_agent",
		Arguments: map[string]any{
			"namespace":    ns,
			"project_name": proj,
			"number":       float64(1),
		},
	})
	if err != nil {
		t.Fatalf("retry_pr_agent: %v", err)
	}
	if result.IsError {
		t.Fatalf("retry_pr_agent returned error: %s", extractText(result))
	}

	// PR restored to open, still holds queue slot
	retried, _ := prStore.Get(1)
	if retried.State != pr.StateOpen {
		t.Fatalf("after retry: state = %q, want open", retried.State)
	}
	if retried.InterruptInfo != nil {
		t.Fatal("after retry: interrupt_info should be nil")
	}

	q, _ = hs.GetPRQueue(ns, proj)
	if q.ActivePR != 1 {
		t.Fatalf("after retry: active = %d, want 1 (same slot)", q.ActivePR)
	}
	if len(q.Waiting) != 1 || q.Waiting[0] != 2 {
		t.Fatalf("after retry: waiting = %v, want [2]", q.Waiting)
	}

	text := extractText(result)
	if !strings.Contains(text, "re-spawned") {
		t.Fatalf("retry response missing 're-spawned': %s", text)
	}
	t.Log("PASS: retry within same queue slot, PR #2 still waiting")
}

func TestReviewIncomplete_DismissViaMCP(t *testing.T) {
	ns, proj := "default", "dismissinc"
	_, hs, prStore, ec := setup2StageTest(t, ns, proj)

	pr1 := create2StagePR(t, prStore, ns, proj, 1, "feat-dismiss")
	hs.EnqueuePR(ns, proj, 1)

	create2StagePR(t, prStore, ns, proj, 2, "feat-queued")
	hs.EnqueuePR(ns, proj, 2)

	// Simulate review_incomplete interrupt (slot retained)
	markInterrupted(prStore, pr1, "review_incomplete",
		"agent exited successfully but did not approve or reject",
		"default_claude_reviewer", "reviewer", ec.logger)

	q, _ := hs.GetPRQueue(ns, proj)
	if q.ActivePR != 1 {
		t.Fatalf("after interrupt: active = %d, want 1", q.ActivePR)
	}

	// Set up MCP and call dismiss_pr_interrupt
	mcpServer := mcp.NewServer(
		&mcp.Implementation{Name: "gitcote-test", Version: "0.0.0-test"},
		nil,
	)
	registerPRTools(mcpServer, ec.gitStore, &seedContext{gitStore: ec.gitStore}, ec)

	authenticator := auth.New(auth.Config{
		ValidateToken: func(tok string) (auth.Principal, auth.RejectReason, bool) {
			return auth.Principal{Name: "admin", Email: "a@t.com", Scope: "*"}, "", true
		},
	})
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return mcpServer },
		nil,
	)
	mux := http.NewServeMux()
	mux.Handle("/mcp", authenticator.Middleware(mcpHandler))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	mcpClient := mcp.NewClient(
		&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	ctx := context.Background()
	session, err := mcpClient.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             ts.URL + "/mcp",
		HTTPClient:           &http.Client{Transport: &bearerTransport{token: "t"}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "dismiss_pr_interrupt",
		Arguments: map[string]any{
			"namespace":    ns,
			"project_name": proj,
			"number":       float64(1),
		},
	})
	if err != nil {
		t.Fatalf("dismiss_pr_interrupt: %v", err)
	}
	if result.IsError {
		t.Fatalf("dismiss_pr_interrupt returned error: %s", extractText(result))
	}

	// After dismiss: PR restored to open, queue slot released
	dismissed, _ := prStore.Get(1)
	if dismissed.State != pr.StateOpen {
		t.Fatalf("after dismiss: state = %q, want open", dismissed.State)
	}
	if dismissed.InterruptInfo != nil {
		t.Fatal("after dismiss: interrupt_info should be nil")
	}

	// Queue slot should be released, PR #2 becomes active
	time.Sleep(50 * time.Millisecond)
	q, _ = hs.GetPRQueue(ns, proj)
	if q.ActivePR != 2 {
		t.Fatalf("after dismiss: active = %d, want 2 (slot released)", q.ActivePR)
	}

	text := extractText(result)
	if !strings.Contains(text, "queue slot released") {
		t.Fatalf("dismiss response missing 'queue slot released': %s", text)
	}
	t.Log("PASS: dismiss releases queue slot, PR #2 becomes active")
}

func TestReviewIncomplete_RepeatedInterrupt(t *testing.T) {
	ns, proj := "default", "repeatinc"
	_, hs, prStore, ec := setup2StageTest(t, ns, proj)

	pr1 := create2StagePR(t, prStore, ns, proj, 1, "feat-repeat")
	hs.EnqueuePR(ns, proj, 1)

	create2StagePR(t, prStore, ns, proj, 2, "feat-queued")
	hs.EnqueuePR(ns, proj, 2)

	for cycle := 1; cycle <= 3; cycle++ {
		current, _ := prStore.Get(pr1.Number)
		markInterrupted(prStore, current, "review_incomplete",
			"agent exited successfully but did not approve or reject",
			"default_claude_reviewer", "reviewer", ec.logger)

		p, _ := prStore.Get(1)
		if p.State != pr.StateInterrupted {
			t.Fatalf("cycle %d: state = %q, want interrupted", cycle, p.State)
		}
		if p.InterruptInfo == nil || p.InterruptInfo.Reason != "review_incomplete" {
			t.Fatalf("cycle %d: unexpected interrupt_info", cycle)
		}

		// Queue slot must be retained throughout
		q, _ := hs.GetPRQueue(ns, proj)
		if q.ActivePR != 1 {
			t.Fatalf("cycle %d: active = %d, want 1 (slot retained)", cycle, q.ActivePR)
		}
		if len(q.Waiting) != 1 || q.Waiting[0] != 2 {
			t.Fatalf("cycle %d: waiting = %v, want [2]", cycle, q.Waiting)
		}

		// Simulate retry
		p.State = p.PreviousState
		p.PreviousState = ""
		p.InterruptInfo = nil
		p.UpdatedAt = time.Now()
		if err := prStore.Update(p); err != nil {
			t.Fatal(err)
		}
	}
	t.Log("PASS: 3 cycles with queue slot retained, PR #2 never jumped ahead")
}

func runGit2Stage(t *testing.T, dir string, args ...string) {
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
