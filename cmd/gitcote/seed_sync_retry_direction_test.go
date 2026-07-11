package main

import (
	"context"
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
	"github.com/sopranoworks/gitcote/internal/vault"
	"github.com/sopranoworks/shoka/pkg/auth"
	"github.com/sopranoworks/shoka/pkg/oauthstore"
)

// TestSeedSyncRetryDirection_ResolvesFromStoredDirection is a fast unit test
// for seedSyncRetryDirection: it must default to "pull" (preserving prior
// behavior) whenever no seed config, no sync status, or a non-"push"
// direction is on record, and only report "push" when the last conflict/
// interrupt was actually recorded as Direction="push".
func TestSeedSyncRetryDirection_ResolvesFromStoredDirection(t *testing.T) {
	baseDir := t.TempDir()
	gitStore := git.NewStore(baseDir)
	ns, proj := "dirtest", "proj"
	if err := gitStore.CreateRepo(ns, proj); err != nil {
		t.Fatal(err)
	}
	projPath, _ := gitStore.ProjectPath(ns, proj)

	// No seed config saved at all yet.
	if got := seedSyncRetryDirection(gitStore, ns, proj); got != "pull" {
		t.Fatalf("expected pull with no seed config, got %q", got)
	}

	// Seed config with no SyncStatus.
	if err := git.SaveSeedConfig(projPath, &git.SeedConfig{SeedURL: "x"}); err != nil {
		t.Fatal(err)
	}
	if got := seedSyncRetryDirection(gitStore, ns, proj); got != "pull" {
		t.Fatalf("expected pull with no sync status, got %q", got)
	}

	// SyncStatus present with Direction="pull".
	if err := git.SaveSeedConfig(projPath, &git.SeedConfig{
		SeedURL:    "x",
		SyncStatus: &git.SeedSyncStatus{State: "conflict", Direction: "pull"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := seedSyncRetryDirection(gitStore, ns, proj); got != "pull" {
		t.Fatalf("expected pull with Direction=pull, got %q", got)
	}

	// SyncStatus present with Direction="push" — the case that was broken:
	// retry_seed_sync used to ignore this and always retry a pull.
	if err := git.SaveSeedConfig(projPath, &git.SeedConfig{
		SeedURL:    "x",
		SyncStatus: &git.SeedSyncStatus{State: "conflict", Direction: "push"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := seedSyncRetryDirection(gitStore, ns, proj); got != "push" {
		t.Fatalf("expected push with Direction=push, got %q", got)
	}
	t.Log("PASS: seedSyncRetryDirection resolves push only when actually recorded, defaults to pull otherwise")
}

// TestRetrySeedSync_MCP_PushConflict_RetriggersPushNotPull is the concrete,
// end-to-end proof for directive question 5: a push_to_seed conflict (not a
// pull conflict) gets stuck with Direction="push" on record. Before the fix,
// retry_seed_sync unconditionally called executeSeedPull, so retrying a
// stuck PUSH would silently perform the wrong operation (a pull) instead.
// This reproduces a real push conflict via a local bare-repo seed (no SSH
// needed, same harness as TestSeedPushConflict_QueueAndAgentWiring), calls
// the real retry_seed_sync MCP tool, and asserts the sync status still
// shows Direction="push" afterward — which only happens if executeSeedPush
// (not executeSeedPull) actually ran, since each flow re-stamps its own
// direction via updateSeedSyncStateDirection on conflict/interrupt.
func TestRetrySeedSync_MCP_PushConflict_RetriggersPushNotPull(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	baseDir := t.TempDir()
	gitStore := git.NewStore(baseDir)
	ns, proj := "e2e", "seedpushretry"

	if err := gitStore.CreateRepo(ns, proj); err != nil {
		t.Fatal(err)
	}
	projPath, _ := gitStore.ProjectPath(ns, proj)

	// Local bare repo acting as the seed — mirrors TestSeedPushConflict_QueueAndAgentWiring.
	seedBareDir := filepath.Join(t.TempDir(), "seed.git")
	runGitE2E(t, t.TempDir(), "init", "--bare", seedBareDir)
	runGitE2E(t, seedBareDir, "symbolic-ref", "HEAD", "refs/heads/main")

	writeTestFile(t, projPath, "README.md", "# seed push retry test\n")
	runGitE2E(t, projPath, "add", ".")
	runGitE2E(t, projPath, "commit", "-m", "initial commit")
	runGitE2E(t, projPath, "remote", "add", "seed", seedBareDir)
	runGitE2E(t, projPath, "push", "seed", "main")

	// Diverging commit on the seed side, via a temp clone.
	cloneDir := filepath.Join(t.TempDir(), "seed-clone")
	runGitE2E(t, t.TempDir(), "clone", seedBareDir, cloneDir)
	writeTestFile(t, cloneDir, "conflict.txt", "seed version\n")
	runGitE2E(t, cloneDir, "add", ".")
	runGitE2E(t, cloneDir, "commit", "-m", "seed-side change")
	runGitE2E(t, cloneDir, "push", "origin", "HEAD:main")

	// Diverging commit locally on the same file — guarantees both a push
	// AND a pull of this content would conflict, so Direction flipping to
	// "pull" (the bug) is observable and not masked by a clean auto-merge.
	writeTestFile(t, projPath, "conflict.txt", "local version\n")
	runGitE2E(t, projPath, "add", ".")
	runGitE2E(t, projPath, "commit", "-m", "local-side change")

	v, err := vault.Open(filepath.Join(baseDir, "keys.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if err := v.Unlock("test-password"); err != nil {
		t.Fatal(err)
	}
	keyName := "test-key"
	if _, err := v.GenerateKey(ns, keyName, "test"); err != nil {
		t.Fatal(err)
	}

	if err := git.SaveSeedConfig(projPath, &git.SeedConfig{
		SeedURL:  seedBareDir,
		KeyName:  keyName,
		PushMode: git.PushModeDisabled,
	}); err != nil {
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

	agentDisabled := false
	ec := &eventContext{
		gitStore:    gitStore,
		integrityHS: integrityStore,
		oauthStore:  oauthSt,
		agentCfg:    AgentSpawnConfig{Enabled: &agentDisabled},
		logger:      testLogger(),
	}

	sc := &seedContext{gitStore: gitStore, vault: v, gitcoteURL: "", resumed: true}

	// Real push conflict, exactly as push_to_seed would produce it. This
	// naturally records Direction="push" via executeSeedPushWithMerge and
	// retains the SeedSyncSentinel queue slot (slot retention on conflict).
	result := executeSeedPushWithMerge(sc, ec, ns, proj, "main")
	if result.Status != "conflict" {
		t.Fatalf("setup: expected initial push to conflict, got status=%q msg=%s", result.Status, result.Message)
	}
	defer os.RemoveAll(result.TempCloneDir)

	cfgBefore, err := git.LoadSeedConfig(projPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfgBefore.SyncStatus == nil || cfgBefore.SyncStatus.Direction != "push" {
		t.Fatalf("setup: expected Direction=push after a push conflict, got %+v", cfgBefore.SyncStatus)
	}

	q, qerr := integrityStore.GetPRQueue(ns, proj)
	if qerr != nil || q.ActivePR != integrity.SeedSyncSentinel {
		t.Fatalf("setup: expected seed sync to hold the queue slot after conflict, got %+v err=%v", q, qerr)
	}

	// Now call the real retry_seed_sync MCP tool — this is the action an
	// operator takes after e.g. configuring a merger for OnPushConflict.
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "gitcote-test", Version: "0.0.0-test"}, nil)
	registerSeedTools(mcpServer, gitStore, v, "", ec)

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

	callResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "retry_seed_sync",
		Arguments: map[string]any{
			"namespace":    ns,
			"project_name": proj,
		},
	})
	if err != nil {
		t.Fatalf("retry_seed_sync: %v", err)
	}
	if callResult.IsError {
		t.Fatalf("retry_seed_sync unexpectedly errored: %s", extractText(callResult))
	}
	if text := extractText(callResult); !strings.Contains(text, "push re-triggered") {
		t.Fatalf("expected the response to confirm a push (not pull) was re-triggered, got: %s", text)
	}

	// The retried push runs in a goroutine; wait for it to re-conflict and
	// re-stamp sync status (same seed/local content, so it conflicts again
	// deterministically — that's fine, we're only checking which flow ran).
	deadline := time.Now().Add(5 * time.Second)
	var cfgAfter *git.SeedConfig
	for time.Now().Before(deadline) {
		cfgAfter, err = git.LoadSeedConfig(projPath)
		if err == nil && cfgAfter.SyncStatus != nil && cfgAfter.SyncStatus.State != "retrying" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if cfgAfter == nil || cfgAfter.SyncStatus == nil {
		t.Fatal("timed out waiting for retried sync to update status")
	}
	if cfgAfter.SyncStatus.Direction != "push" {
		t.Fatalf("retry_seed_sync retried the wrong flow: expected Direction to remain %q (push retried), got %q — "+
			"this is exactly the bug where a stuck push conflict got silently retried as a pull",
			"push", cfgAfter.SyncStatus.Direction)
	}
	t.Log("PASS: retry_seed_sync on a push-conflict-stuck sync re-triggers the push, not a pull")
}
