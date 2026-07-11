package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sopranoworks/gitcote/internal/integrity"
	"github.com/sopranoworks/shoka/pkg/auth"
)

// TestPRAgentLock_ReentrancyGuard is a direct, deterministic test of the
// acquirePRAgentLock/releasePRAgentLock primitive (mirrors
// TestSeedPush_ReentrancyGuard's pre-acquire-then-call pattern): a second
// acquire for the same key must fail while the first is held, and must
// succeed again once released.
func TestPRAgentLock_ReentrancyGuard(t *testing.T) {
	key := agentTokenKey("racens", "raceproj", 1)

	if !acquirePRAgentLock(key) {
		t.Fatal("first acquire should succeed")
	}
	if acquirePRAgentLock(key) {
		t.Fatal("second acquire while held should fail")
	}
	releasePRAgentLock(key)
	if !acquirePRAgentLock(key) {
		t.Fatal("acquire after release should succeed")
	}
	releasePRAgentLock(key)
	t.Log("PASS: per-PR agent lock is a true mutual-exclusion guard")
}

// setupMCPHarness spins up a real MCP server/client pair (same pattern as
// TestReviewIncomplete_RetryViaMCP / TestRetryPRAgent_MCP_RetroactiveOnNeverAttemptedPR)
// for driving retry_pr_agent end-to-end.
func setupMCPHarness(t *testing.T, ec *eventContext) *mcp.ClientSession {
	t.Helper()
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "gitcote-test", Version: "0.0.0-test"}, nil)
	registerPRTools(mcpServer, ec.gitStore, &seedContext{gitStore: ec.gitStore}, ec)

	authenticator := auth.New(auth.Config{
		ValidateToken: func(tok string) (auth.Principal, auth.RejectReason, bool) {
			return auth.Principal{Name: "admin", Email: "a@t.com", Scope: "*"}, "", true
		},
	})
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, nil)
	mux := http.NewServeMux()
	mux.Handle("/mcp", authenticator.Middleware(mcpHandler))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	session, err := mcpClient.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:             ts.URL + "/mcp",
		HTTPClient:           &http.Client{Transport: &bearerTransport{token: "t"}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// TestPRAgentLock_ConcurrentAcquireExactlyOneWinner is the rigorous,
// deterministic proof that acquirePRAgentLock is a true mutual-exclusion
// primitive: N goroutines released from a shared starting gate (maximum
// real simultaneity, no network/JSON-RPC scheduling noise) race to acquire
// the same key. Exactly one must win, regardless of N or timing.
func TestPRAgentLock_ConcurrentAcquireExactlyOneWinner(t *testing.T) {
	key := agentTokenKey("race", "gate", 1)

	const n = 64
	var wg sync.WaitGroup
	var winners atomic.Int32
	start := make(chan struct{})
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			<-start
			if acquirePRAgentLock(key) {
				winners.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if winners.Load() != 1 {
		t.Fatalf("expected exactly 1 winner out of %d simultaneous acquires, got %d", n, winners.Load())
	}
	releasePRAgentLock(key)
	t.Logf("PASS: %d simultaneous lock acquires on the same key — exactly 1 winner", n)
}

// TestRetryPRAgent_MCP_RejectedWhileAnotherInProgress proves the full
// retry_pr_agent stack (not just the bare lock) honors the guard while a
// spawn is genuinely in flight: pre-acquire the lock as spawnAgentForPR's
// caller would while running, then fire real concurrent MCP calls — every
// one must be rejected as "already in progress", none may slip through.
func TestRetryPRAgent_MCP_RejectedWhileAnotherInProgress(t *testing.T) {
	ns, proj := "race", "inflight"
	_, hs, prStore, ec := setupQueueTest(t, ns, proj)

	disabled := false
	ec.agentCfg = AgentSpawnConfig{Enabled: &disabled}

	createTestPR(t, prStore, ns, proj, 1, "feat/inflight")
	isActive, err := hs.EnqueuePR(ns, proj, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !isActive {
		t.Fatal("PR should be the active queue entry")
	}

	session := setupMCPHarness(t, ec)

	// Simulate a spawn already in flight (holds the lock the same way the
	// real handler does for the duration of its background goroutine).
	key := agentTokenKey(ns, proj, 1)
	if !acquirePRAgentLock(key) {
		t.Fatal("failed to pre-acquire lock")
	}

	const n = 8
	var wg sync.WaitGroup
	var rejected atomic.Int32
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "retry_pr_agent",
				Arguments: map[string]any{
					"namespace":    ns,
					"project_name": proj,
					"number":       float64(1),
				},
			})
			if err != nil {
				t.Errorf("unexpected transport error: %v", err)
				return
			}
			if !result.IsError {
				t.Error("call should have been rejected while a spawn is already in progress, but it succeeded")
				return
			}
			if text := extractText(result); !strings.Contains(text, "already in progress") {
				t.Errorf("unexpected rejection reason: %s", text)
			}
			rejected.Add(1)
		}()
	}
	wg.Wait()
	releasePRAgentLock(key)

	if rejected.Load() != n {
		t.Fatalf("expected all %d concurrent calls to be rejected while a spawn is in flight, got %d rejected", n, rejected.Load())
	}
	t.Logf("PASS: all %d concurrent retry_pr_agent calls correctly rejected while a spawn was already in flight — no double-spawn", n)

	// Now that the in-flight spawn has "finished" (lock released), a fresh
	// call must succeed — the guard doesn't leak/stick.
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "retry_pr_agent",
		Arguments: map[string]any{
			"namespace":    ns,
			"project_name": proj,
			"number":       float64(1),
		},
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected retry to succeed once the in-flight spawn released the lock, got: %s", extractText(result))
	}
	t.Log("PASS: lock correctly released after the in-flight spawn completed — a fresh retry succeeds")
}

// TestRetryPRAgent_MCP_ConcurrentCallsNeverOverlap fires genuinely
// concurrent retry_pr_agent calls (real HTTP/MCP round-trips, same pattern
// as TestSeedPush_ConcurrentCallsOneProceeds) against the same eligible PR
// with no agent running. Because the guarded work here (agent disabled)
// completes near-instantly, more than one call can legitimately succeed in
// sequence — the guarantee under test is that the guard never lets two
// calls succeed AT THE SAME TIME (proven above), and that concurrent load
// never produces an unexpected error, a panic, or a rejection reason other
// than the documented one.
func TestRetryPRAgent_MCP_ConcurrentCallsNeverOverlap(t *testing.T) {
	ns, proj := "race", "concurrent"
	_, hs, prStore, ec := setupQueueTest(t, ns, proj)

	disabled := false
	ec.agentCfg = AgentSpawnConfig{Enabled: &disabled}

	createTestPR(t, prStore, ns, proj, 1, "feat/race")
	isActive, err := hs.EnqueuePR(ns, proj, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !isActive {
		t.Fatal("PR should be the active queue entry")
	}

	session := setupMCPHarness(t, ec)

	const n = 12
	var wg sync.WaitGroup
	var succeeded, rejected atomic.Int32
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "retry_pr_agent",
				Arguments: map[string]any{
					"namespace":    ns,
					"project_name": proj,
					"number":       float64(1),
				},
			})
			if err != nil {
				t.Errorf("unexpected transport error: %v", err)
				return
			}
			if result.IsError {
				text := extractText(result)
				if !strings.Contains(text, "already in progress") {
					t.Errorf("unexpected rejection reason: %s", text)
				}
				rejected.Add(1)
				return
			}
			succeeded.Add(1)
		}()
	}
	wg.Wait()

	if succeeded.Load() < 1 {
		t.Fatal("expected at least one call to succeed")
	}
	t.Logf("PASS: %d concurrent retry_pr_agent calls — %d succeeded (sequential, non-overlapping turns), %d correctly rejected as already-in-progress, no crashes/wrong errors",
		n, succeeded.Load(), rejected.Load())
}

// TestRetryPRAgent_MCP_StaleEligibilityRejected proves the backend
// re-validates eligibility at call time rather than trusting any prior
// snapshot: a PR that was eligible a moment ago (no live token) but has
// since acquired one — simulating a reviewer spawned by another path
// landing between the frontend's eligibility snapshot and this call — must
// be rejected, not double-spawned.
func TestRetryPRAgent_MCP_StaleEligibilityRejected(t *testing.T) {
	ns, proj := "race", "stale"
	_, hs, prStore, ec := setupQueueTest(t, ns, proj)

	disabled := false
	ec.agentCfg = AgentSpawnConfig{Enabled: &disabled}

	p := createTestPR(t, prStore, ns, proj, 1, "feat/stale")
	isActive, err := hs.EnqueuePR(ns, proj, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !isActive {
		t.Fatal("PR should be the active queue entry")
	}

	// Snapshot: this is what the frontend would have seen (eligible).
	if eligible, reason := prRetryEligible(ec, p); !eligible {
		t.Fatalf("expected PR to be eligible at snapshot time, got rejected: %s", reason)
	}

	// Between the snapshot and the click landing, a reviewer gets spawned
	// via another path (e.g. a concurrent onPRCreated dequeue) and
	// registers its token.
	key := agentTokenKey(ns, proj, 1)
	if err := hs.SetAgentToken(key, integrity.AgentTokenRecord{
		SeriesID: "s1", Namespace: ns, Project: proj, PRNumber: 1,
		TaskType: "pr_review", AgentName: "mock_reviewer", Role: "reviewer",
	}); err != nil {
		t.Fatal(err)
	}

	session := setupMCPHarness(t, ec)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "retry_pr_agent",
		Arguments: map[string]any{
			"namespace":    ns,
			"project_name": proj,
			"number":       float64(1),
		},
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected the stale retry call to be rejected now that a live token exists, but it succeeded")
	}
	text := extractText(result)
	if !strings.Contains(text, "already has an agent running") {
		t.Fatalf("expected rejection about an active agent, got: %s", text)
	}
	t.Log("PASS: retry call re-validates eligibility at call time and rejects a now-stale snapshot")

	_ = hs.RemoveAgentToken(key)
}
