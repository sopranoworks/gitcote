package main

import (
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

// TestOnPRMergeConflict_NoAgentConfigured_StaysInConflictNoCrash mirrors
// TestOnPRCreated_NoAgentConfigured_StaysOpenNoCrash for the merger role: a
// merge conflict occurs on a project with no merger agent configured at
// all (fresh project, OnMergeConflict has no agent action). onPRMergeConflict
// should no-op cleanly — no panic, no spurious state transition, no
// InterruptInfo fabricated — leaving the PR in StateMergeConflict, still
// holding its queue slot, exactly mirroring how a never-configured reviewer
// leaves a PR sitting in StateOpen.
func TestOnPRMergeConflict_NoAgentConfigured_StaysInConflictNoCrash(t *testing.T) {
	ns, proj := "noagent", "mergeconflict"
	_, hs, prStore, ec := setupQueueTest(t, ns, proj)

	p := createTestPR(t, prStore, ns, proj, 1, "feat/conflict")
	isActive, err := hs.EnqueuePR(ns, proj, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !isActive {
		t.Fatal("first PR should be the active queue entry")
	}
	p.State = pr.StateMergeConflict
	p.Mergeable = pr.MergeableConflict
	if err := prStore.Update(p); err != nil {
		t.Fatal(err)
	}

	// No SeedEventSettings/PREventSettings configured anywhere —
	// ResolveEventAction(nil, nil) defaults AgentEnabled=false. This is the
	// merger-role equivalent of "no reviewer configured at PR arrival".
	onPRMergeConflict(ec, p)

	got, err := prStore.Get(1)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != pr.StateMergeConflict {
		t.Fatalf("expected PR to remain in merge_conflict with no merger configured, got state=%q", got.State)
	}
	if got.InterruptInfo != nil {
		t.Fatalf("expected no interrupt info (no merger was ever spawned to fail), got %+v", got.InterruptInfo)
	}
	q, _ := hs.GetPRQueue(ns, proj)
	if q.ActivePR != 1 {
		t.Fatalf("expected PR #1 to remain the active queue entry, got %d", q.ActivePR)
	}
	t.Log("PASS: merge conflict with no merger agent configured stays in merge_conflict, no crash, no fabricated interrupt")
}

// TestOnPRMergeConflict_NotifiesIndependentlyOfAgentEnabled confirms
// question 2 from the directive: a merge conflict with no merger
// configured is NOT silent as long as NotifyEnabled is set — mirroring
// onPRCreated's identical NotifyEnabled-independent-of-AgentEnabled
// structure. AgentEnabled=false must not suppress the notification.
func TestOnPRMergeConflict_NotifiesIndependentlyOfAgentEnabled(t *testing.T) {
	ns, proj := "noagent", "mergenotify"
	_, hs, prStore, ec := setupQueueTest(t, ns, proj)

	var buf strings.Builder
	ec.logger = slog.New(slog.NewTextHandler(&buf, nil))

	agentEnabled := false
	notifyEnabled := true
	if err := hs.SetGlobalPREventSettings(&integrity.PREventSettings{
		OnMergeConflict: &integrity.EventAction{
			AgentEnabled:  &agentEnabled,
			NotifyEnabled: &notifyEnabled,
			NotifyMethod:  "log",
		},
	}); err != nil {
		t.Fatal(err)
	}

	p := createTestPR(t, prStore, ns, proj, 1, "feat/notify-conflict")
	if _, err := hs.EnqueuePR(ns, proj, 1); err != nil {
		t.Fatal(err)
	}
	p.State = pr.StateMergeConflict
	if err := prStore.Update(p); err != nil {
		t.Fatal(err)
	}

	onPRMergeConflict(ec, p)
	// notify() fires from a goroutine inside onPRMergeConflict.
	deadline := time.Now().Add(2 * time.Second)
	for buf.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if !strings.Contains(buf.String(), "PR notification") {
		t.Fatalf("expected a notification to fire even though no merger is configured (AgentEnabled=false), got: %s", buf.String())
	}
	t.Log("PASS: merge conflict with no merger configured still notifies when NotifyEnabled is set — not silent")
}

// TestPRRetryEligible_MergeConflictNeverAttempted mirrors
// TestPRRetryEligible_Cases's StateOpen coverage for StateMergeConflict:
// before the fix, prRetryEligible only accepted StateInterrupted or
// StateOpen, so a never-attempted StateMergeConflict PR (no InterruptInfo)
// was permanently ineligible for retry_pr_agent — there was no way to
// retroactively resume it even after configuring a merger.
func TestPRRetryEligible_MergeConflictNeverAttempted(t *testing.T) {
	ns, proj := "noagent", "mergeeligible"
	_, hs, prStore, ec := setupQueueTest(t, ns, proj)

	p1 := createTestPR(t, prStore, ns, proj, 1, "feat/one")
	if _, err := hs.EnqueuePR(ns, proj, 1); err != nil {
		t.Fatal(err)
	}
	p1.State = pr.StateMergeConflict
	if err := prStore.Update(p1); err != nil {
		t.Fatal(err)
	}
	p2 := createTestPR(t, prStore, ns, proj, 2, "feat/two")
	if _, err := hs.EnqueuePR(ns, proj, 2); err != nil {
		t.Fatal(err)
	}
	p2.State = pr.StateMergeConflict
	if err := prStore.Update(p2); err != nil {
		t.Fatal(err)
	}

	// Case 1: StateMergeConflict, is the active queue entry, no agent
	// token — must be eligible (this is exactly the case the old code
	// rejected outright with "must be interrupted, or open...").
	if ok, reason := prRetryEligible(ec, p1); !ok {
		t.Fatalf("expected merge-conflict+active+no-token PR to be eligible, got rejected: %s", reason)
	}
	t.Log("PASS: merge-conflict PR that is the active queue entry with no prior agent history is eligible")

	// Case 2: StateMergeConflict, NOT the active queue entry — rejected,
	// same FIFO protection as the StateOpen case.
	if ok, reason := prRetryEligible(ec, p2); ok {
		t.Fatal("expected queued (non-active) merge-conflict PR to be rejected")
	} else if !strings.Contains(reason, "not the active queue entry") {
		t.Fatalf("expected rejection reason about queue order, got: %s", reason)
	}
	t.Log("PASS: merge-conflict PR that is not yet the active queue entry is rejected (no jumping the FIFO)")

	// Case 3: StateMergeConflict, active, but an agent token already
	// exists (a merger is currently running) — rejected.
	key := agentTokenKey(ns, proj, 1)
	if err := hs.SetAgentToken(key, integrity.AgentTokenRecord{
		SeriesID: "s1", Namespace: ns, Project: proj, PRNumber: 1,
		TaskType: "pr_merge", AgentName: "mock_merger", Role: "merger",
	}); err != nil {
		t.Fatal(err)
	}
	if ok, reason := prRetryEligible(ec, p1); ok {
		t.Fatal("expected merge-conflict PR with a live agent token to be rejected")
	} else if !strings.Contains(reason, "already has an agent running") {
		t.Fatalf("expected rejection reason about an active agent, got: %s", reason)
	}
	t.Log("PASS: merge-conflict PR with a live agent token is rejected (no double-spawn)")
}

// TestDefaultRetryRole_ResolvesMergerForMergeConflict proves the role a
// never-attempted retry resolves to actually depends on PR state, not a
// hardcoded "reviewer" default — the second half of the bug (eligibility
// alone isn't enough; even an eligible merge-conflict PR would previously
// have resolved to the wrong role and consulted OnCreated instead of
// OnMergeConflict).
func TestDefaultRetryRole_ResolvesMergerForMergeConflict(t *testing.T) {
	openPR := &pr.PullRequest{State: pr.StateOpen}
	if role := defaultRetryRole(openPR); role != "reviewer" {
		t.Fatalf("expected open PR to default to reviewer, got %q", role)
	}
	conflictPR := &pr.PullRequest{State: pr.StateMergeConflict}
	if role := defaultRetryRole(conflictPR); role != "merger" {
		t.Fatalf("expected merge-conflict PR to default to merger, got %q", role)
	}
}

// TestRetryPRAgent_MCP_RetroactiveOnNeverAttemptedMergeConflict is the core
// end-to-end proof, through the real MCP tool, that the unified
// retry_pr_agent action actually works for the merger role: a PR sits in
// merge_conflict because no merger was ever configured. It must be
// rejected with a clear message (not silently accepted, and not silently
// resolved against the wrong role's config) until a merger is configured,
// at which point the exact same call must succeed and consult
// OnMergeConflict specifically — proven by configuring a DIFFERENT,
// enabled reviewer action first and confirming that alone does not make
// the call succeed (which it would if role incorrectly defaulted to
// "reviewer" and consulted OnCreated instead of OnMergeConflict).
func TestRetryPRAgent_MCP_RetroactiveOnNeverAttemptedMergeConflict(t *testing.T) {
	ns, proj := "noagent", "mcpmergeretry"
	gitStore, hs, prStore, ec := setupQueueTest(t, ns, proj)

	disabled := false
	ec.agentCfg = AgentSpawnConfig{Enabled: &disabled}

	p := createTestPR(t, prStore, ns, proj, 1, "feat/mcp-merge-retry")
	isActive, err := hs.EnqueuePR(ns, proj, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !isActive {
		t.Fatal("PR should be the active queue entry")
	}
	p.State = pr.StateMergeConflict
	p.Mergeable = pr.MergeableConflict
	if err := prStore.Update(p); err != nil {
		t.Fatal(err)
	}

	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "gitcote-test", Version: "0.0.0-test"}, nil)
	registerPRTools(mcpServer, gitStore, &seedContext{gitStore: gitStore}, ec)

	authenticator := auth.New(auth.Config{
		ValidateToken: func(tok string) (auth.Principal, auth.RejectReason, bool) {
			return auth.Principal{Name: "admin", Email: "a@t.com", Scope: "*"}, "", true
		},
	})
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, nil)
	mux := http.NewServeMux()
	mux.Handle("/mcp", authenticator.Middleware(mcpHandler))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	ctx := context.Background()
	session, err := mcpClient.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             ts.URL + "/mcp",
		HTTPClient:           &http.Client{Transport: &bearerTransport{token: "t"}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	callRetry := func() (*mcp.CallToolResult, string) {
		t.Helper()
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
		return result, extractText(result)
	}

	// No merger configured yet — must be rejected, not silently spawn a
	// builtin, and not rejected for the wrong reason (e.g. "must be
	// interrupted or open" — that would prove the eligibility gap, not
	// this rejection).
	result, text := callRetry()
	if !result.IsError {
		t.Fatalf("expected retry_pr_agent to be rejected with no merger configured, got success: %s", text)
	}
	if !strings.Contains(text, "no merger agent configured") {
		t.Fatalf("expected a clear 'no merger agent configured' message, got: %s", text)
	}
	got, _ := prStore.Get(1)
	if got.State != pr.StateMergeConflict {
		t.Fatalf("expected PR to remain in merge_conflict after the rejected call, got %q", got.State)
	}
	t.Log("PASS: retry_pr_agent rejects a never-attempted merge-conflict PR when no merger is configured")

	// Configure a REVIEWER (OnCreated), deliberately leaving OnMergeConflict
	// unset/disabled. If role resolution incorrectly defaulted to
	// "reviewer" for a merge-conflict PR, this alone would make the next
	// call succeed against the wrong config — it must not.
	reviewerEnabled := true
	if err := hs.SetGlobalPREventSettings(&integrity.PREventSettings{
		OnCreated: &integrity.EventAction{AgentEnabled: &reviewerEnabled, AgentName: "mock_reviewer"},
	}); err != nil {
		t.Fatal(err)
	}
	result, text = callRetry()
	if !result.IsError {
		t.Fatalf("expected retry_pr_agent to still be rejected (only OnCreated is configured, not OnMergeConflict), got success: %s", text)
	}
	if !strings.Contains(text, "no merger agent configured") {
		t.Fatalf("expected rejection to still name the merger role, got: %s", text)
	}
	t.Log("PASS: configuring a reviewer alone does not retroactively spawn a merger for a merge-conflict PR (role resolution is not defaulting to reviewer)")

	// Now configure the merger (OnMergeConflict) — the same call must
	// succeed.
	mergerEnabled := true
	if err := hs.SetGlobalPREventSettings(&integrity.PREventSettings{
		OnCreated:       &integrity.EventAction{AgentEnabled: &reviewerEnabled, AgentName: "mock_reviewer"},
		OnMergeConflict: &integrity.EventAction{AgentEnabled: &mergerEnabled, AgentName: "mock_merger"},
	}); err != nil {
		t.Fatal(err)
	}
	result, text = callRetry()
	if result.IsError {
		t.Fatalf("retry_pr_agent on never-attempted merge-conflict PR should succeed once a merger is configured and it's the active queue entry, got error: %s", text)
	}
	if !strings.Contains(text, "agent spawned") {
		t.Fatalf("expected 'agent spawned' confirmation (never attempted before, not a re-spawn), got: %s", text)
	}

	got, _ = prStore.Get(1)
	if got.State != pr.StateMergeConflict {
		t.Fatalf("expected PR to remain in merge_conflict (no restore needed, this isn't an interrupted-PR recovery), got %q", got.State)
	}
	q, _ := hs.GetPRQueue(ns, proj)
	if q.ActivePR != 1 {
		t.Fatalf("expected PR #1 to remain the active queue entry, got %d", q.ActivePR)
	}
	t.Log("PASS: retry_pr_agent retroactively triggers a merger for a merge-conflict PR that never had one, and resolves the merger role correctly (not reviewer)")
}

// TestReconcileExternalMerges_NeverSpawnedMerger proves reconcileExternalMerges
// (triggered via the real handlePostReceive path, mirroring
// TestManualRecovery_ExternalMergeDetected) correctly detects and closes
// out a PR that reached StateMergeConflict with NO merger ever spawned —
// no InterruptInfo, no markInterrupted call — not just the already-covered
// "merger was spawned, got interrupted, operator resolved manually" case.
func TestReconcileExternalMerges_NeverSpawnedMerger(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	baseDir := t.TempDir()
	gitStore := git.NewStore(baseDir)
	ns, proj := "e2e", "nevermerger"
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

	gitHTTP := git.NewHandler(gitStore, logger)
	gitHTTP.PostReceive = func(namespace, project string, principal auth.Principal, pushOpts []string, refUpdates []git.RefUpdate) {
		handlePostReceive(gitStore, logger, namespace, project, principal, pushOpts, refUpdates, ec)
	}

	authenticator := auth.New(auth.Config{
		ValidateToken: func(tok string) (auth.Principal, auth.RejectReason, bool) {
			return auth.Principal{Name: "admin", Email: "admin@test.com", Scope: "*"}, "", true
		},
	})
	httpMux := http.NewServeMux()
	httpMux.Handle("/", authenticator.Middleware(gitHTTP))
	ts := httptest.NewServer(httpMux)
	defer ts.Close()

	cloneDir := t.TempDir()
	runGit2Stage(t, cloneDir, "clone", ts.URL+"/"+ns+"/"+proj+".git", "repo")
	repoDir := filepath.Join(cloneDir, "repo")
	writeTestFile(t, repoDir, "README.md", "# Never-spawned merger test\n")
	runGit2Stage(t, repoDir, "add", "README.md")
	runGit2Stage(t, repoDir, "commit", "-m", "initial commit")
	runGit2Stage(t, repoDir, "push", "-u", "origin", "HEAD:refs/heads/main")

	runGit2Stage(t, repoDir, "checkout", "-b", "feat/never-merger")
	writeTestFile(t, repoDir, "feature.go", "package main\nfunc Feature() {}\n")
	runGit2Stage(t, repoDir, "add", "feature.go")
	runGit2Stage(t, repoDir, "commit", "-m", "add feature")
	runGit2Stage(t, repoDir, "push", "-u", "origin", "feat/never-merger")

	prStore, err := getPRStore(baseDir, ns, proj)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	thePR := &pr.PullRequest{
		RepoNamespace: ns,
		RepoProject:   proj,
		Title:         "Never-spawned merger test",
		SourceBranch:  "feat/never-merger",
		TargetBranch:  "main",
		Author:        "test",
		// StateMergeConflict directly, with NO InterruptInfo and no
		// markInterrupted call — this PR never had a merger spawned for
		// it at all (e.g. no merger agent was configured when the
		// conflict was first detected).
		State:       pr.StateMergeConflict,
		Mergeable:   pr.MergeableConflict,
		CreatedAt:   now,
		UpdatedAt:   now,
		OrderFiles:  []string{},
		ResultFiles: []string{},
	}
	prNum, err := prStore.Create(thePR)
	if err != nil {
		t.Fatal(err)
	}
	integrityStore.EnqueuePR(ns, proj, int(prNum))

	q, _ := integrityStore.GetPRQueue(ns, proj)
	if q.ActivePR != int(prNum) {
		t.Fatalf("setup: active = %d, want %d", q.ActivePR, prNum)
	}

	// Operator manually merges feat/never-merger into main and pushes —
	// exactly as if they resolved the conflict by hand, with no merger
	// agent ever having been spawned for this PR.
	runGit2Stage(t, repoDir, "checkout", "main")
	runGit2Stage(t, repoDir, "merge", "feat/never-merger", "-m", "manual merge, no merger ever configured")
	runGit2Stage(t, repoDir, "push", "origin", "main")

	// The push fires handlePostReceive -> reconcileExternalMerges.
	merged, err := prStore.Get(prNum)
	if err != nil {
		t.Fatal(err)
	}
	if merged.State != pr.StateMerged {
		t.Fatalf("after external push (never-spawned merger): state = %q, want merged", merged.State)
	}
	if merged.InterruptInfo != nil {
		t.Fatal("after external merge: interrupt_info should be nil")
	}
	if merged.MergeCommit == "" {
		t.Fatal("after external merge: merge_commit should be set")
	}

	time.Sleep(50 * time.Millisecond)
	q, _ = integrityStore.GetPRQueue(ns, proj)
	if q.ActivePR != 0 {
		t.Errorf("after external merge: active = %d, want 0 (idle)", q.ActivePR)
	}
	t.Log("PASS: reconcileExternalMerges detects and closes out a merge conflict where no merger was ever spawned, not just an interrupted one")
}
