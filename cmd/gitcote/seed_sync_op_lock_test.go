package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sopranoworks/gitcote/internal/integrity"
	"github.com/sopranoworks/shoka/pkg/auth"
)

// TestSeedSyncOpLock_ReentrancyGuard is the seed-sync counterpart to
// TestPRAgentLock_ReentrancyGuard: a direct, deterministic test of the
// acquireSeedSyncOpLock/releaseSeedSyncOpLock primitive added to close the
// TOCTOU gap in handleSeedSyncRetryWS/handleSeedSyncDismissWS and their MCP
// equivalents — the check ("is seed sync the active queue entry") and the
// mutation (release the slot, update state) were not atomic, so two
// concurrent calls could both pass the check and both get a false "status:
// ok" response even though only one underlying push/pull actually ran.
func TestSeedSyncOpLock_ReentrancyGuard(t *testing.T) {
	key := seedSyncOpKey("racens", "raceproj")

	if !acquireSeedSyncOpLock(key) {
		t.Fatal("first acquire should succeed")
	}
	if acquireSeedSyncOpLock(key) {
		t.Fatal("second acquire while held should fail")
	}
	releaseSeedSyncOpLock(key)
	if !acquireSeedSyncOpLock(key) {
		t.Fatal("acquire after release should succeed")
	}
	releaseSeedSyncOpLock(key)
	t.Log("PASS: seed sync op lock is a true mutual-exclusion guard")
}

// TestSeedSyncOpLock_ConcurrentAcquireExactlyOneWinner mirrors
// TestPRAgentLock_ConcurrentAcquireExactlyOneWinner: N goroutines released
// from a shared starting gate race to acquire the same key. Exactly one
// must win, regardless of N or timing.
func TestSeedSyncOpLock_ConcurrentAcquireExactlyOneWinner(t *testing.T) {
	key := seedSyncOpKey("race", "gate")

	const n = 64
	var wg sync.WaitGroup
	var winners atomic.Int32
	start := make(chan struct{})
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			<-start
			if acquireSeedSyncOpLock(key) {
				winners.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if winners.Load() != 1 {
		t.Fatalf("expected exactly 1 winner out of %d simultaneous acquires, got %d", n, winners.Load())
	}
	releaseSeedSyncOpLock(key)
	t.Logf("PASS: %d simultaneous lock acquires on the same key — exactly 1 winner", n)
}

// setupSeedMCPHarness spins up a real MCP server/client pair for driving
// retry_seed_sync/dismiss_seed_sync end-to-end, mirroring setupMCPHarness
// (pr_agent_race_test.go) for the PR side.
func setupSeedMCPHarness(t *testing.T, sc *seedContext, ec *eventContext) *mcp.ClientSession {
	t.Helper()
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "gitcote-test", Version: "0.0.0-test"}, nil)
	registerSeedTools(mcpServer, sc.gitStore, sc.vault, sc.gitcoteURL, ec)

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

// TestRetrySeedSync_MCP_RejectedWhileAnotherInProgress is the seed-sync
// counterpart to TestRetryPRAgent_MCP_RejectedWhileAnotherInProgress: with
// the op lock pre-acquired (simulating a retry already in flight), every
// concurrent retry_seed_sync call must be cleanly rejected as "already in
// progress" — never a false "status: ok" — and a fresh call must succeed
// once the lock is released.
func TestRetrySeedSync_MCP_RejectedWhileAnotherInProgress(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
	ns, proj := "race", "seedinflight"
	sc, ec, hs := setupSeedSyncTest(t, ns, proj)

	// Put seed sync into a stuck, retry-eligible state (as if a prior pull
	// conflicted and is now waiting on the operator).
	isActive, err := hs.EnqueuePriority(ns, proj, integrity.SeedSyncSentinel)
	if err != nil {
		t.Fatal(err)
	}
	if !isActive {
		t.Fatal("seed sync should be the active queue entry")
	}
	updateSeedSyncStateDetail(sc.gitStore, ns, proj, "conflict", "pull", "pull_conflict", "manual merge required")

	session := setupSeedMCPHarness(t, sc, ec)

	key := seedSyncOpKey(ns, proj)
	if !acquireSeedSyncOpLock(key) {
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
				Name: "retry_seed_sync",
				Arguments: map[string]any{
					"namespace":    ns,
					"project_name": proj,
				},
			})
			if err != nil {
				t.Errorf("unexpected transport error: %v", err)
				return
			}
			if !result.IsError {
				t.Error("call should have been rejected while a retry is already in progress, but it succeeded")
				return
			}
			if text := extractText(result); !strings.Contains(text, "already in progress") {
				t.Errorf("unexpected rejection reason: %s", text)
			}
			rejected.Add(1)
		}()
	}
	wg.Wait()
	releaseSeedSyncOpLock(key)

	if rejected.Load() != n {
		t.Fatalf("expected all %d concurrent calls to be rejected while a retry is in flight, got %d rejected", n, rejected.Load())
	}
	t.Logf("PASS: all %d concurrent retry_seed_sync calls correctly rejected while another was in flight — no false success", n)

	// Lock released — but the FIRST call's (simulated) mutation never
	// actually ran (we only held the bare lock, no real retry occurred), so
	// the slot and state are exactly as they were: still eligible. A fresh
	// call must now succeed rather than being permanently stuck.
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "retry_seed_sync",
		Arguments: map[string]any{
			"namespace":    ns,
			"project_name": proj,
		},
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected retry to succeed once the in-flight lock released, got: %s", extractText(result))
	}
	t.Log("PASS: lock correctly released after the in-flight call completed — a fresh retry succeeds")

	// The successful call above triggers a real pull in a background
	// goroutine; give it a moment to finish before t.TempDir() cleanup
	// tears down the repo out from under it.
	time.Sleep(100 * time.Millisecond)
}

// TestRetrySeedSync_MCP_SecondCallWhileFirstStillPulling targets a deeper
// instance of the same false-success bug the op lock was built to close: an
// EARLIER version of the fix released seedSyncOpLock as soon as
// handleSeedSyncRetryWS's synchronous portion finished, not when the async
// executeSeedPull it kicked off actually completed. Since that goroutine
// re-acquires the SeedSyncSentinel queue slot itself, a second retry_seed_sync
// call arriving after the first call's handler returned — but while its pull
// was still genuinely running — would see the slot "active" again and the op
// lock free, and would proceed to release the slot out from under the
// in-flight pull and report its own false "status: ok", even though nothing
// new actually started.
//
// This was actually CAUGHT by the Docker E2E multi-session Playwright test
// (6 real concurrent WS connections, real network timing, all 6 got
// "ok: true"), not by this test — reproducing the exact race locally proved
// unreliable even with two independent MCP sessions fired from a shared
// start gate (local git ops against a tiny repo complete faster than the
// scheduling/round-trip jitter needed to land a second call inside the
// narrow "slot re-acquired, pull not yet finished" window). Kept anyway as
// a real, if weaker, concurrent-call check — it still exercises two
// independent sessions racing the real handler end-to-end and asserts the
// same invariant (never more than one success, only clean rejections) — but
// the Docker E2E test is the one that actually guards this regression.
func TestRetrySeedSync_MCP_SecondCallWhileFirstStillPulling(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
	ns, proj := "race", "seedstillpulling"
	sc, ec, hs := setupSeedSyncTest(t, ns, proj)

	isActive, err := hs.EnqueuePriority(ns, proj, integrity.SeedSyncSentinel)
	if err != nil {
		t.Fatal(err)
	}
	if !isActive {
		t.Fatal("seed sync should be the active queue entry")
	}
	updateSeedSyncStateDetail(sc.gitStore, ns, proj, "conflict", "pull", "pull_conflict", "manual merge required")

	// Two INDEPENDENT sessions (each its own MCP server/httptest instance,
	// both wired to the same underlying sc/ec — the shared op lock and
	// integrity store live at the package level, not on the session), the
	// same way two browser tabs would each hold their own connection. A
	// single shared session's client SDK can serialize concurrent
	// CallTool invocations on its own transport, which would mask the
	// race entirely — this setup rules that out.
	session1 := setupSeedMCPHarness(t, sc, ec)
	session2 := setupSeedMCPHarness(t, sc, ec)

	callRetry := func(session *mcp.ClientSession) (bool, string) {
		t.Helper()
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "retry_seed_sync",
			Arguments: map[string]any{
				"namespace":    ns,
				"project_name": proj,
			},
		})
		if err != nil {
			t.Fatalf("unexpected transport error: %v", err)
		}
		return result.IsError, extractText(result)
	}

	// Fire both calls from a shared starting gate for maximum real
	// simultaneity — the first call's handler returns (having kicked off
	// its pull in a goroutine) well before that goroutine finishes real
	// git work, so the second call reliably arrives while the first is
	// still genuinely in flight.
	var wg sync.WaitGroup
	results := make([]struct {
		isErr bool
		text  string
	}, 2)
	start := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		results[0].isErr, results[0].text = callRetry(session1)
	}()
	go func() {
		defer wg.Done()
		<-start
		results[1].isErr, results[1].text = callRetry(session2)
	}()
	close(start)
	wg.Wait()

	successes := 0
	for _, r := range results {
		if !r.isErr {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly 1 of 2 calls to succeed while the other is still pulling, got %d: %+v", successes, results)
	}
	for _, r := range results {
		if r.isErr && !strings.Contains(r.text, "already in progress") && !strings.Contains(r.text, "not the active queue entry") {
			t.Fatalf("unexpected rejection reason: %s", r.text)
		}
	}
	t.Log("PASS: a second retry_seed_sync call while the first is still actually pulling is rejected, never a false success")

	time.Sleep(150 * time.Millisecond)
}
