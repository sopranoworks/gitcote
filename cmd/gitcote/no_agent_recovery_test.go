package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sopranoworks/gitcote/internal/integrity"
	"github.com/sopranoworks/gitcote/internal/pr"
	"github.com/sopranoworks/shoka/pkg/auth"
)

// TestOnPRCreated_NoAgentConfigured_StaysOpenNoCrash covers the normal-path
// (not failure/interrupt) scenario: a PR arrives in a project with no
// reviewer agent configured at all (fresh project, OnCreated has no agent
// action). onPRCreated should no-op cleanly — no panic, no spurious state
// transition — leaving the PR open and waiting.
func TestOnPRCreated_NoAgentConfigured_StaysOpenNoCrash(t *testing.T) {
	ns, proj := "noagent", "created"
	_, hs, prStore, ec := setupQueueTest(t, ns, proj)

	p := createTestPR(t, prStore, ns, proj, 1, "feat/no-agent")
	isActive, err := hs.EnqueuePR(ns, proj, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !isActive {
		t.Fatal("first PR should be the active queue entry")
	}

	// No PREventSettings configured anywhere — ResolveEventAction(nil, nil)
	// defaults AgentEnabled=false. This is the exact "no agent configured"
	// scenario, not a failure path.
	onPRCreated(ec, p)

	got, err := prStore.Get(1)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != pr.StateOpen {
		t.Fatalf("expected PR to remain open with no agent configured, got state=%q", got.State)
	}
	if got.InterruptInfo != nil {
		t.Fatalf("expected no interrupt info (this is not a failure path), got %+v", got.InterruptInfo)
	}
	q, _ := hs.GetPRQueue(ns, proj)
	if q.ActivePR != 1 {
		t.Fatalf("expected PR #1 to remain the active queue entry, got %d", q.ActivePR)
	}
	t.Log("PASS: PR with no reviewer agent configured stays open, no crash, no interrupt")
}

// TestPRRetryEligible_Cases exercises prRetryEligible directly across the
// state matrix: the established StateInterrupted recovery path, the newly
// added StateOpen/never-attempted path, and the safety rails that prevent
// it from being abused (not the active queue entry, or an agent is already
// running for this PR).
func TestPRRetryEligible_Cases(t *testing.T) {
	ns, proj := "noagent", "eligible"
	_, hs, prStore, ec := setupQueueTest(t, ns, proj)

	p1 := createTestPR(t, prStore, ns, proj, 1, "feat/one")
	if _, err := hs.EnqueuePR(ns, proj, 1); err != nil {
		t.Fatal(err)
	}
	p2 := createTestPR(t, prStore, ns, proj, 2, "feat/two")
	if _, err := hs.EnqueuePR(ns, proj, 2); err != nil {
		t.Fatal(err)
	}

	// Case 1: StateOpen, is the active queue entry, no agent token — eligible.
	if ok, reason := prRetryEligible(ec, p1); !ok {
		t.Fatalf("expected open+active+no-token PR to be eligible, got rejected: %s", reason)
	}
	t.Log("PASS: open PR that is the active queue entry with no prior agent history is eligible")

	// Case 2: StateOpen, NOT the active queue entry (still waiting) — rejected.
	if ok, reason := prRetryEligible(ec, p2); ok {
		t.Fatal("expected queued (non-active) open PR to be rejected")
	} else if !strings.Contains(reason, "not the active queue entry") {
		t.Fatalf("expected rejection reason about queue order, got: %s", reason)
	}
	t.Log("PASS: open PR that is not yet the active queue entry is rejected (no jumping the FIFO)")

	// Case 3: StateOpen, active, but an agent token already exists (a
	// reviewer is currently running) — rejected, must not double-spawn.
	key := agentTokenKey(ns, proj, 1)
	if err := hs.SetAgentToken(key, integrity.AgentTokenRecord{
		SeriesID: "s1", Namespace: ns, Project: proj, PRNumber: 1,
		TaskType: "pr_review", AgentName: "mock_reviewer", Role: "reviewer",
	}); err != nil {
		t.Fatal(err)
	}
	if ok, reason := prRetryEligible(ec, p1); ok {
		t.Fatal("expected PR with a live agent token to be rejected")
	} else if !strings.Contains(reason, "already has an agent running") {
		t.Fatalf("expected rejection reason about an active agent, got: %s", reason)
	}
	t.Log("PASS: open PR with a live agent token is rejected (no double-spawn)")
	_ = hs.RemoveAgentToken(key)

	// Case 4: StateInterrupted — the established recovery path still works.
	markInterrupted(prStore, p1, "review_incomplete", "test", "mock_reviewer", "reviewer", ec.logger)
	if ok, reason := prRetryEligible(ec, p1); !ok {
		t.Fatalf("expected interrupted PR to remain eligible, got rejected: %s", reason)
	}
	t.Log("PASS: interrupted PR is still eligible (existing recovery path unaffected)")

	// Case 5: some other terminal state (e.g. rejected) — not eligible.
	p3 := createTestPR(t, prStore, ns, proj, 3, "feat/three")
	p3.State = pr.StateRejected
	if ok, _ := prRetryEligible(ec, p3); ok {
		t.Fatal("expected a rejected PR to be ineligible")
	}
	t.Log("PASS: terminal states other than interrupted/open are ineligible")
}

// TestRetryPRAgent_MCP_RetroactiveOnNeverAttemptedPR is the core directive
// scenario end-to-end through the real MCP tool: a PR sits open because no
// reviewer agent was ever configured for it. It's still the active queue
// entry, so retry_pr_agent must accept it and drive the same spawn path
// onPRCreated would have used had an agent existed at creation time.
func TestRetryPRAgent_MCP_RetroactiveOnNeverAttemptedPR(t *testing.T) {
	ns, proj := "noagent", "mcpretry"
	gitStore, hs, prStore, ec := setupQueueTest(t, ns, proj)

	// Keep agent execution disabled — this test verifies the eligibility
	// and wiring fix, not real subprocess spawning (covered by the Docker
	// E2E Playwright spec using the mock_reviewer fixture).
	disabled := false
	ec.agentCfg = AgentSpawnConfig{Enabled: &disabled}

	createTestPR(t, prStore, ns, proj, 1, "feat/mcp-retry")
	isActive, err := hs.EnqueuePR(ns, proj, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !isActive {
		t.Fatal("PR should be the active queue entry")
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
		t.Fatalf("retry_pr_agent on never-attempted open PR should succeed once it's the active queue entry, got error: %s", extractText(result))
	}
	text := extractText(result)
	if !strings.Contains(text, "re-spawned") {
		t.Fatalf("expected re-spawned confirmation, got: %s", text)
	}

	// PR must remain open (this isn't an interrupted-PR restore — nothing
	// to restore) and keep holding its queue slot.
	got, _ := prStore.Get(1)
	if got.State != pr.StateOpen {
		t.Fatalf("expected PR to remain open, got %q", got.State)
	}
	q, _ := hs.GetPRQueue(ns, proj)
	if q.ActivePR != 1 {
		t.Fatalf("expected PR #1 to remain the active queue entry, got %d", q.ActivePR)
	}
	t.Log("PASS: retry_pr_agent retroactively triggers a reviewer for a PR that never had one, once it becomes eligible")
}
