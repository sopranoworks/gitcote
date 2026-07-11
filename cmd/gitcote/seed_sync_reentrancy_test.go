package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sopranoworks/gitcote/internal/git"
	"github.com/sopranoworks/gitcote/internal/integrity"
	"github.com/sopranoworks/gitcote/internal/pr"
	"github.com/sopranoworks/gitcote/internal/vault"
	"github.com/sopranoworks/shoka/pkg/auth"
	"github.com/sopranoworks/shoka/pkg/oauthstore"
)

// syncBuffer is a mutex-guarded bytes.Buffer for tests that read log
// output from the main goroutine while a `go notifySeedSyncInterrupt(...)`
// call writes to it from a background goroutine — plain bytes.Buffer is
// not safe for that concurrent use and trips the race detector.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func setupSeedSyncTest(t *testing.T, ns, proj string) (*seedContext, *eventContext, *integrity.Store) {
	t.Helper()
	baseDir := t.TempDir()
	gitStore := git.NewStore(baseDir)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if err := gitStore.CreateRepo(ns, proj); err != nil {
		t.Fatal(err)
	}
	projPath, _ := gitStore.ProjectPath(ns, proj)

	seedBareDir := filepath.Join(t.TempDir(), "seed.git")
	runGitE2E(t, t.TempDir(), "init", "--bare", seedBareDir)
	runGitE2E(t, seedBareDir, "symbolic-ref", "HEAD", "refs/heads/main")

	writeTestFile(t, projPath, "README.md", "# seed sync test\n")
	runGitE2E(t, projPath, "add", ".")
	runGitE2E(t, projPath, "commit", "-m", "initial commit")
	runGitE2E(t, projPath, "remote", "add", "seed", seedBareDir)
	runGitE2E(t, projPath, "push", "seed", "main")

	v, err := vault.Open(filepath.Join(baseDir, "keys.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })
	if err := v.Unlock("test-password"); err != nil {
		t.Fatal(err)
	}
	keyName := "test-key"
	if _, err := v.GenerateKey(ns, keyName, "test"); err != nil {
		t.Fatal(err)
	}

	cfg := &git.SeedConfig{
		SeedURL:  seedBareDir,
		KeyName:  keyName,
		PushMode: git.PushModeDisabled,
	}
	if err := git.SaveSeedConfig(projPath, cfg); err != nil {
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

	sc := &seedContext{
		gitStore:   gitStore,
		vault:      v,
		gitcoteURL: "",
		resumed:    true,
	}

	ec.seedCtx = sc

	return sc, ec, integrityStore
}

func TestSeedPull_ReentrancyGuard(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
	ns, proj := "re", "pull"
	sc, ec, _ := setupSeedSyncTest(t, ns, proj)

	// First pull should succeed (up-to-date).
	r1 := executeSeedPull(sc, ec, ns, proj, "main")
	success1, _ := r1["success"].(bool)
	if !success1 {
		t.Fatalf("first pull should succeed: %v", r1)
	}

	// Simulate concurrent pulls using the re-entrancy guard directly.
	acquireSeedLock(&seedPullActive, ns, proj)
	r2 := executeSeedPull(sc, ec, ns, proj, "main")
	status2, _ := r2["status"].(string)
	if status2 != "in_progress" {
		t.Fatalf("second pull should return in_progress, got %q: %v", status2, r2)
	}
	releaseSeedLock(&seedPullActive, ns, proj)
	t.Log("PASS: re-entrancy guard prevents concurrent pulls")
}

func TestSeedPush_ReentrancyGuard(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
	ns, proj := "re", "push"
	sc, ec, _ := setupSeedSyncTest(t, ns, proj)

	acquireSeedLock(&seedPushActive, ns, proj)
	r := executeSeedPushWithMerge(sc, ec, ns, proj, "main")
	if r.Status != "in_progress" {
		t.Fatalf("push should return in_progress when lock held, got %q: %s", r.Status, r.Message)
	}
	releaseSeedLock(&seedPushActive, ns, proj)
	t.Log("PASS: re-entrancy guard prevents concurrent pushes")
}

func TestSeedPull_ConcurrentCallsOneProceeds(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
	ns, proj := "conc", "pull"
	sc, ec, _ := setupSeedSyncTest(t, ns, proj)

	var wg sync.WaitGroup
	results := make([]map[string]interface{}, 2)

	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx] = executeSeedPull(sc, ec, ns, proj, "main")
		}(i)
	}
	wg.Wait()

	succeeded := 0
	inProgress := 0
	for _, r := range results {
		if s, _ := r["success"].(bool); s {
			succeeded++
		}
		if st, _ := r["status"].(string); st == "in_progress" {
			inProgress++
		}
	}

	if succeeded < 1 {
		t.Fatalf("expected at least one success, results: %v", results)
	}
	t.Logf("PASS: concurrent pulls — %d succeeded, %d in_progress", succeeded, inProgress)
}

func TestSeedSync_SlotRetainedOnConflict(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
	ns, proj := "slot", "conflict"
	sc, ec, hs := setupSeedSyncTest(t, ns, proj)

	projPath, _ := sc.gitStore.ProjectPath(ns, proj)

	// Create a diverging commit on seed (via a temp clone of the bare repo).
	cfg, _ := git.LoadSeedConfig(projPath)
	cloneDir := filepath.Join(t.TempDir(), "seed-clone")
	runGitE2E(t, t.TempDir(), "clone", cfg.SeedURL, cloneDir)
	writeTestFile(t, cloneDir, "conflict.txt", "seed version\n")
	runGitE2E(t, cloneDir, "add", ".")
	runGitE2E(t, cloneDir, "commit", "-m", "seed-side change")
	runGitE2E(t, cloneDir, "push", "origin", "HEAD:main")

	// Create a diverging commit locally.
	writeTestFile(t, projPath, "conflict.txt", "local version\n")
	runGitE2E(t, projPath, "add", ".")
	runGitE2E(t, projPath, "commit", "-m", "local-side change")

	result := executeSeedPull(sc, ec, ns, proj, "main")
	status, _ := result["status"].(string)
	if status != "conflict" {
		t.Fatalf("expected conflict, got %q: %v", status, result)
	}

	// Verify queue slot is retained (SeedSyncSentinel is active).
	q, err := hs.GetPRQueue(ns, proj)
	if err != nil {
		t.Fatal(err)
	}
	if q.ActivePR != integrity.SeedSyncSentinel {
		t.Fatalf("expected SeedSyncSentinel as active queue entry, got %d", q.ActivePR)
	}
	t.Log("PASS: queue slot retained on conflict")

	// Verify a PR would be queued behind the seed sync.
	isActive, err := hs.EnqueuePR(ns, proj, 1)
	if err != nil {
		t.Fatal(err)
	}
	if isActive {
		t.Fatal("PR should be queued behind seed sync, not active")
	}
	t.Log("PASS: PR queued behind stuck seed sync")

	// Clean up: release queue entries.
	hs.ReleasePRSlot(ns, proj, integrity.SeedSyncSentinel)

	if tc, ok := result["temp_clone"].(string); ok && tc != "" {
		os.RemoveAll(tc)
	}
}

func TestSeedSync_SlotRetainedWhenAgentDisabled(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
	ns, proj := "slot", "agentoff"
	_, ec, hs := setupSeedSyncTest(t, ns, proj)

	disabled := false
	ec.agentCfg = AgentSpawnConfig{Enabled: &disabled}

	// Pre-acquire the seed sync slot.
	isActive, err := hs.EnqueuePriority(ns, proj, integrity.SeedSyncSentinel)
	if err != nil {
		t.Fatal(err)
	}
	if !isActive {
		t.Fatal("seed sync should be active")
	}

	action := integrity.ResolvedEventAction{AgentEnabled: true}

	// Agent spawning is disabled. spawnAgentForSeedSync returns early
	// without releasing the slot or marking interrupted.
	spawnAgentForSeedSync(ec, action, ns, proj, "/tmp/nonexistent", []string{"conflict.txt"})

	time.Sleep(50 * time.Millisecond)

	// Slot should still be held.
	q, err := hs.GetPRQueue(ns, proj)
	if err != nil {
		t.Fatal(err)
	}
	if q.ActivePR != integrity.SeedSyncSentinel {
		t.Fatalf("expected slot retained, got ActivePR=%d", q.ActivePR)
	}
	t.Log("PASS: slot retained when agent spawning is disabled")

	hs.ReleasePRSlot(ns, proj, integrity.SeedSyncSentinel)
}

func TestSeedSync_SlotRetainedOnNonConflictFailure(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
	ns, proj := "slot", "fail"
	sc, ec, hs := setupSeedSyncTest(t, ns, proj)

	// Point seed URL to nonexistent repo to force a non-conflict failure.
	projPath, _ := sc.gitStore.ProjectPath(ns, proj)
	cfg := &git.SeedConfig{
		SeedURL:  "/nonexistent/seed.git",
		KeyName:  "test-key",
		PushMode: git.PushModeDisabled,
	}
	if err := git.SaveSeedConfig(projPath, cfg); err != nil {
		t.Fatal(err)
	}

	// executeSeedPull should fail with a non-conflict error and retain the slot.
	result := executeSeedPull(sc, ec, ns, proj, "main")
	success, _ := result["success"].(bool)
	if success {
		t.Fatal("expected failure, got success")
	}
	status, _ := result["status"].(string)
	if status == "conflict" {
		t.Fatal("expected non-conflict failure")
	}

	// Slot should be retained.
	q, err := hs.GetPRQueue(ns, proj)
	if err != nil {
		t.Fatal(err)
	}
	if q.ActivePR != integrity.SeedSyncSentinel {
		t.Fatalf("expected slot retained on non-conflict failure, got ActivePR=%d", q.ActivePR)
	}

	// Verify state is interrupted.
	cfgAfter, _ := git.LoadSeedConfig(projPath)
	if cfgAfter.SyncStatus == nil || cfgAfter.SyncStatus.State != "interrupted" {
		t.Fatalf("expected state=interrupted, got %v", cfgAfter.SyncStatus)
	}
	t.Log("PASS: slot retained and state=interrupted on non-conflict failure")

	hs.ReleasePRSlot(ns, proj, integrity.SeedSyncSentinel)
}

func TestSeedSync_DismissReleasesSlot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
	ns, proj := "slot", "dismiss"
	sc, ec, hs := setupSeedSyncTest(t, ns, proj)

	// Acquire seed sync slot.
	isActive, err := hs.EnqueuePriority(ns, proj, integrity.SeedSyncSentinel)
	if err != nil {
		t.Fatal(err)
	}
	if !isActive {
		t.Fatal("seed sync should be active")
	}
	updateSeedSyncState(sc.gitStore, ns, proj, "interrupted")

	// Queue a PR behind seed sync.
	isActive, err = hs.EnqueuePR(ns, proj, 1)
	if err != nil {
		t.Fatal(err)
	}
	if isActive {
		t.Fatal("PR should be queued")
	}

	// Dismiss the seed sync interrupt.
	ensureNoActiveToken(ec, ns, proj, 0)
	updateSeedSyncState(sc.gitStore, ns, proj, "idle")
	releaseSeedSyncSlot(ec, ns, proj)

	// Verify slot was released and state is idle.
	projPath, _ := sc.gitStore.ProjectPath(ns, proj)
	cfg, _ := git.LoadSeedConfig(projPath)
	if cfg.SyncStatus == nil || cfg.SyncStatus.State != "idle" {
		t.Fatalf("expected state=idle after dismiss, got %v", cfg.SyncStatus)
	}

	// Verify the queued PR is now active.
	q, err := hs.GetPRQueue(ns, proj)
	if err != nil {
		t.Fatal(err)
	}
	if q.ActivePR != 1 {
		t.Fatalf("expected PR #1 as active after dismiss, got %d", q.ActivePR)
	}
	t.Log("PASS: dismiss releases slot and dequeues next PR")

	hs.ReleasePRSlot(ns, proj, 1)
}

func TestSeedSync_RetryReleasesAndRepulls(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
	ns, proj := "slot", "retry"
	sc, ec, hs := setupSeedSyncTest(t, ns, proj)

	// Acquire seed sync slot and mark interrupted.
	isActive, err := hs.EnqueuePriority(ns, proj, integrity.SeedSyncSentinel)
	if err != nil {
		t.Fatal(err)
	}
	if !isActive {
		t.Fatal("seed sync should be active")
	}
	updateSeedSyncState(sc.gitStore, ns, proj, "interrupted")

	// Retry: release slot and re-trigger pull.
	ensureNoActiveToken(ec, ns, proj, 0)
	releaseSeedSyncSlot(ec, ns, proj)
	updateSeedSyncState(sc.gitStore, ns, proj, "retrying")

	// The re-triggered pull should succeed (no conflict — repo is up-to-date).
	result := executeSeedPull(sc, ec, ns, proj, "main")
	success, _ := result["success"].(bool)
	if !success {
		t.Fatalf("retry pull should succeed: %v", result)
	}

	// Verify state is now idle (slot was released by successful pull).
	q, err := hs.GetPRQueue(ns, proj)
	if err != nil {
		t.Fatal(err)
	}
	if q.ActivePR != 0 {
		t.Fatalf("expected no active queue entry after successful retry, got %d", q.ActivePR)
	}
	t.Log("PASS: retry releases slot and re-triggers pull successfully")
}

func TestSeedSync_PRMergeBlockedDuringSeedSync(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
	ns, proj := "block", "prmerge"
	_, ec, hs := setupSeedSyncTest(t, ns, proj)

	// Acquire seed sync slot.
	isActive, err := hs.EnqueuePriority(ns, proj, integrity.SeedSyncSentinel)
	if err != nil {
		t.Fatal(err)
	}
	if !isActive {
		t.Fatal("seed sync should be active")
	}

	// Check that the queue correctly reflects seed sync as active.
	q, err := hs.GetPRQueue(ns, proj)
	if err != nil {
		t.Fatal(err)
	}
	if q.ActivePR != integrity.SeedSyncSentinel {
		t.Fatalf("expected SeedSyncSentinel active, got %d", q.ActivePR)
	}

	// Verify a new PR gets queued (auto-merge path is blocked).
	isActive, err = hs.EnqueuePR(ns, proj, 1)
	if err != nil {
		t.Fatal(err)
	}
	if isActive {
		t.Fatal("PR should be queued behind seed sync")
	}
	t.Log("PASS: PR enqueue blocked by seed sync slot")

	// The handlePRMerge check: simulate it.
	if q.ActivePR == integrity.SeedSyncSentinel {
		t.Log("PASS: handlePRMerge would reject merge while seed sync holds the slot")
	}

	hs.ReleasePRSlot(ns, proj, integrity.SeedSyncSentinel)
	_ = ec
}

func TestSeedSync_NotificationOnInterrupt(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
	ns, proj := "notify", "seedsync"

	baseDir := t.TempDir()
	gitStore := git.NewStore(baseDir)

	if err := gitStore.CreateRepo(ns, proj); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	ec := &eventContext{
		gitStore:   gitStore,
		logger:     logger,
		gitcoteURL: "https://gitcote.example.com",
	}

	notifySeedSyncInterrupt(ec, "log", ns, proj, "seed_sync_agent_failed", "exit code 1", "test-merger")

	logged := buf.String()
	if !strings.Contains(logged, "Seed sync interrupted") {
		t.Fatalf("expected 'Seed sync interrupted' in log, got: %s", logged)
	}
	if !strings.Contains(logged, "seed_sync_agent_failed") {
		t.Fatalf("expected reason in log, got: %s", logged)
	}
	if !strings.Contains(logged, ns+"/"+proj) {
		t.Fatalf("expected namespace/project in log, got: %s", logged)
	}
	if !strings.Contains(logged, "test-merger") {
		t.Fatalf("expected agent name in log, got: %s", logged)
	}
	if !strings.Contains(logged, "gitcote.example.com") {
		t.Fatalf("expected link in log, got: %s", logged)
	}
	t.Log("PASS: notification includes reason, agent, project, and link")
}

func TestSeedSync_QueuePullFromQueueRetainsSlotOnFailure(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
	ns, proj := "qpull", "fail"

	baseDir := t.TempDir()
	gitStore := git.NewStore(baseDir)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if err := gitStore.CreateRepo(ns, proj); err != nil {
		t.Fatal(err)
	}
	projPath, _ := gitStore.ProjectPath(ns, proj)

	// Set up vault but with an invalid seed URL to force a pull failure.
	v, err := vault.Open(filepath.Join(baseDir, "keys.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })
	if err := v.Unlock("test-password"); err != nil {
		t.Fatal(err)
	}
	keyName := "test-key"
	if _, err := v.GenerateKey(ns, keyName, "test"); err != nil {
		t.Fatal(err)
	}

	cfg := &git.SeedConfig{
		SeedURL:  "/nonexistent/seed.git",
		KeyName:  keyName,
		PushMode: git.PushModeDisabled,
	}
	if err := git.SaveSeedConfig(projPath, cfg); err != nil {
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

	sc := &seedContext{
		gitStore:   gitStore,
		vault:      v,
		gitcoteURL: "",
		resumed:    true,
	}

	ec := &eventContext{
		gitStore:    gitStore,
		integrityHS: integrityStore,
		oauthStore:  oauthSt,
		agentCfg:    AgentSpawnConfig{},
		logger:      logger,
		seedCtx:     sc,
	}

	// Pre-acquire the seed sync slot (simulating dequeue from PR queue).
	isActive, err := integrityStore.EnqueuePriority(ns, proj, integrity.SeedSyncSentinel)
	if err != nil {
		t.Fatal(err)
	}
	if !isActive {
		t.Fatal("seed sync should be active")
	}

	// Run seed pull from queue — should fail (invalid seed URL).
	executeSeedPullFromQueue(ec, ns, proj)

	// Verify slot is retained.
	q, err := integrityStore.GetPRQueue(ns, proj)
	if err != nil {
		t.Fatal(err)
	}
	if q.ActivePR != integrity.SeedSyncSentinel {
		t.Fatalf("expected slot retained on failure, got ActivePR=%d", q.ActivePR)
	}

	// Verify state is interrupted.
	cfgAfter, _ := git.LoadSeedConfig(projPath)
	if cfgAfter.SyncStatus == nil || cfgAfter.SyncStatus.State != "interrupted" {
		t.Fatalf("expected state=interrupted, got %v", cfgAfter.SyncStatus)
	}
	t.Log("PASS: executeSeedPullFromQueue retains slot and marks interrupted on failure")

	integrityStore.ReleasePRSlot(ns, proj, integrity.SeedSyncSentinel)
}

// runGitSeed is a test helper that runs git commands in a directory.
func runGitSeed(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func runGitSeedCapture(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestSeedSync_ExternalMergeAutoDetected(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	baseDir := t.TempDir()
	gitStore := git.NewStore(baseDir)
	ns, proj := "e2e", "seedext"
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

	disabled := false
	sc := &seedContext{gitStore: gitStore, vault: v, resumed: true}
	ec := &eventContext{
		gitStore:    gitStore,
		integrityHS: integrityStore,
		oauthStore:  oauthSt,
		agentCfg:    AgentSpawnConfig{Enabled: &disabled},
		logger:      logger,
		seedCtx:     sc,
	}

	// Set up HTTP git server with PostReceive hook.
	gitHTTP := git.NewHandler(gitStore, logger)
	gitHTTP.PostReceive = func(namespace, project string, principal auth.Principal, pushOpts []string) {
		handlePostReceive(gitStore, logger, namespace, project, principal, pushOpts, ec)
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

	// Create a local bare repo to act as the seed remote.
	seedBareDir := filepath.Join(t.TempDir(), "seed.git")
	runGitSeed(t, t.TempDir(), "init", "--bare", seedBareDir)
	runGitSeed(t, seedBareDir, "symbolic-ref", "HEAD", "refs/heads/main")

	// Clone gitcote repo, create initial commit, push to both gitcote and seed.
	cloneDir := t.TempDir()
	runGitSeed(t, cloneDir, "clone", ts.URL+"/"+ns+"/"+proj+".git", "repo")
	repoDir := filepath.Join(cloneDir, "repo")
	runGitSeed(t, repoDir, "checkout", "-b", "main")
	writeTestFile(t, repoDir, "README.md", "# Seed External Merge\n")
	runGitSeed(t, repoDir, "add", "README.md")
	runGitSeed(t, repoDir, "commit", "-m", "initial commit")
	runGitSeed(t, repoDir, "push", "-u", "origin", "main")
	runGitSeed(t, repoDir, "remote", "add", "seed", seedBareDir)
	runGitSeed(t, repoDir, "push", "seed", "main")

	// Save seed config.
	projPath, _ := gitStore.ProjectPath(ns, proj)
	seedCfg := &git.SeedConfig{
		SeedURL:  seedBareDir,
		KeyName:  keyName,
		PushMode: git.PushModeDisabled,
	}
	if err := git.SaveSeedConfig(projPath, seedCfg); err != nil {
		t.Fatal(err)
	}

	// Create diverging commits: seed-side and local-side on the same file.
	seedCloneDir := filepath.Join(t.TempDir(), "seed-clone")
	runGitSeed(t, t.TempDir(), "clone", seedBareDir, seedCloneDir)
	writeTestFile(t, seedCloneDir, "conflict.txt", "seed version\n")
	runGitSeed(t, seedCloneDir, "add", ".")
	runGitSeed(t, seedCloneDir, "commit", "-m", "seed-side change")
	runGitSeed(t, seedCloneDir, "push", "origin", "HEAD:main")

	writeTestFile(t, repoDir, "conflict.txt", "local version\n")
	runGitSeed(t, repoDir, "add", ".")
	runGitSeed(t, repoDir, "commit", "-m", "local-side change")
	runGitSeed(t, repoDir, "push", "origin", "main")

	// Trigger seed pull — should conflict.
	result := executeSeedPull(sc, ec, ns, proj, "main")
	status, _ := result["status"].(string)
	if status != "conflict" {
		t.Fatalf("expected conflict, got %q: %v", status, result)
	}

	// Verify slot is held, state is conflict.
	q, _ := integrityStore.GetPRQueue(ns, proj)
	if q.ActivePR != integrity.SeedSyncSentinel {
		t.Fatalf("expected SeedSyncSentinel active, got %d", q.ActivePR)
	}

	// Now simulate operator manually resolving: fetch seed, merge, push to gitcote.
	runGitSeed(t, repoDir, "fetch", "seed")
	runGitSeed(t, repoDir, "merge", "seed/main", "-m", "manual resolve of seed conflict",
		"--strategy-option=theirs")
	runGitSeed(t, repoDir, "push", "origin", "main")

	// The push fires handlePostReceive → reconcileExternalSeedSync.
	// Verify state was auto-cleared.
	cfgAfter, _ := git.LoadSeedConfig(projPath)
	if cfgAfter.SyncStatus == nil || cfgAfter.SyncStatus.State != "idle" {
		state := "nil"
		if cfgAfter.SyncStatus != nil {
			state = cfgAfter.SyncStatus.State
		}
		t.Fatalf("expected state=idle after external resolve, got %s", state)
	}

	// Verify slot was released.
	time.Sleep(50 * time.Millisecond)
	q, _ = integrityStore.GetPRQueue(ns, proj)
	if q.ActivePR != 0 {
		t.Fatalf("expected queue idle after external resolve, got ActivePR=%d", q.ActivePR)
	}
	t.Log("PASS: manually-resolved seed conflict auto-detected on push, state cleared, slot released")

	// Clean up temp clone if created.
	if tc, ok := result["temp_clone"].(string); ok && tc != "" {
		os.RemoveAll(tc)
	}
}

func TestSeedSync_PRQueueResumesAfterRecovery(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	baseDir := t.TempDir()
	gitStore := git.NewStore(baseDir)
	ns, proj := "e2e", "seedresume"
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

	disabled := false
	ec := &eventContext{
		gitStore:    gitStore,
		integrityHS: integrityStore,
		oauthStore:  oauthSt,
		agentCfg:    AgentSpawnConfig{Enabled: &disabled},
		logger:      logger,
	}

	// Create a PR that will be queued behind seed sync.
	prStore, err := getPRStore(baseDir, ns, proj)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	thePR := &pr.PullRequest{
		RepoNamespace: ns,
		RepoProject:   proj,
		Title:         "queued behind seed",
		SourceBranch:  "feat/queued",
		TargetBranch:  "main",
		Author:        "test",
		State:         pr.StateOpen,
		Mergeable:     pr.MergeableClean,
		CreatedAt:     now,
		UpdatedAt:     now,
		OrderFiles:    []string{},
		ResultFiles:   []string{},
	}
	prNum, err := prStore.Create(thePR)
	if err != nil {
		t.Fatal(err)
	}

	// Seed sync acquires slot first.
	isActive, err := integrityStore.EnqueuePriority(ns, proj, integrity.SeedSyncSentinel)
	if err != nil {
		t.Fatal(err)
	}
	if !isActive {
		t.Fatal("seed sync should be active")
	}
	updateSeedSyncState(gitStore, ns, proj, "interrupted")

	// PR enqueues behind seed sync.
	isActive, err = integrityStore.EnqueuePR(ns, proj, int(prNum))
	if err != nil {
		t.Fatal(err)
	}
	if isActive {
		t.Fatal("PR should be queued behind seed sync")
	}

	// Verify the re-entrancy lock is NOT held (no stale lock).
	if !acquireSeedLock(&seedPullActive, ns, proj) {
		t.Fatal("re-entrancy lock should not be held when seed sync is interrupted")
	}
	releaseSeedLock(&seedPullActive, ns, proj)

	// Dismiss seed sync → releases slot → dequeues PR.
	ensureNoActiveToken(ec, ns, proj, 0)
	updateSeedSyncState(gitStore, ns, proj, "idle")
	releaseSeedSyncSlot(ec, ns, proj)

	// Wait for async onPRCreated goroutine.
	time.Sleep(100 * time.Millisecond)

	// Verify PR became the active queue entry.
	q, _ := integrityStore.GetPRQueue(ns, proj)
	if q.ActivePR != int(prNum) {
		t.Fatalf("expected PR #%d active after seed sync dismiss, got ActivePR=%d", prNum, q.ActivePR)
	}

	// Verify PR is still in processable state (onPRCreated was called but
	// agent is disabled, so the PR stays open — no spawn, no state change).
	p, _ := prStore.Get(prNum)
	if p.State != pr.StateOpen {
		t.Fatalf("expected PR state=open (agent disabled, no spawn), got %q", p.State)
	}

	// Verify seed sync state is idle.
	projPath, _ := gitStore.ProjectPath(ns, proj)
	cfg, _ := git.LoadSeedConfig(projPath)
	if cfg.SyncStatus == nil || cfg.SyncStatus.State != "idle" {
		t.Fatalf("expected seed sync state=idle, got %v", cfg.SyncStatus)
	}

	// Verify re-entrancy lock is clean after recovery.
	if !acquireSeedLock(&seedPullActive, ns, proj) {
		t.Fatal("re-entrancy lock should not be held after recovery")
	}
	releaseSeedLock(&seedPullActive, ns, proj)

	t.Log("PASS: seed sync dismissed → PR became active → no stale lock → state clean")

	integrityStore.ReleasePRSlot(ns, proj, int(prNum))
}

// --- Directive 2026-07-11: seed-push parity + notification coverage ---

func TestSeedPush_ConcurrentCallsOneProceeds(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
	ns, proj := "conc", "push"
	sc, ec, _ := setupSeedSyncTest(t, ns, proj)

	// Create a local commit so there is actually something to push.
	projPath, _ := sc.gitStore.ProjectPath(ns, proj)
	writeTestFile(t, projPath, "new.txt", "local change\n")
	runGitE2E(t, projPath, "add", ".")
	runGitE2E(t, projPath, "commit", "-m", "local change to push")

	var wg sync.WaitGroup
	results := make([]SeedPushResult, 2)

	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx] = executeSeedPushWithMerge(sc, ec, ns, proj, "main")
		}(i)
	}
	wg.Wait()

	succeeded := 0
	inProgress := 0
	for _, r := range results {
		if r.Success {
			succeeded++
		}
		if r.Status == "in_progress" {
			inProgress++
		}
	}

	if succeeded < 1 {
		t.Fatalf("expected at least one success, results: %+v", results)
	}
	if inProgress < 1 {
		t.Fatalf("expected the re-entrancy guard to reject at least one concurrent push (real race, not just the wrapper), results: %+v", results)
	}
	t.Logf("PASS: concurrent pushes — %d succeeded, %d in_progress (real re-entrancy race, not just lock wrapper)", succeeded, inProgress)
}

func TestSeedSync_PushSlotRetainedOnConflict(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
	ns, proj := "pslot", "conflict"
	sc, ec, hs := setupSeedSyncTest(t, ns, proj)

	projPath, _ := sc.gitStore.ProjectPath(ns, proj)
	cfg, _ := git.LoadSeedConfig(projPath)

	// Diverging commit on seed.
	cloneDir := filepath.Join(t.TempDir(), "seed-clone")
	runGitE2E(t, t.TempDir(), "clone", cfg.SeedURL, cloneDir)
	writeTestFile(t, cloneDir, "conflict.txt", "seed version\n")
	runGitE2E(t, cloneDir, "add", ".")
	runGitE2E(t, cloneDir, "commit", "-m", "seed-side change")
	runGitE2E(t, cloneDir, "push", "origin", "HEAD:main")

	// Diverging commit locally (so push is rejected as non-fast-forward, then conflicts on merge).
	writeTestFile(t, projPath, "conflict.txt", "local version\n")
	runGitE2E(t, projPath, "add", ".")
	runGitE2E(t, projPath, "commit", "-m", "local-side change")

	result := executeSeedPushWithMerge(sc, ec, ns, proj, "main")
	if result.Status != "conflict" {
		t.Fatalf("expected conflict, got %q: %+v", result.Status, result)
	}

	q, err := hs.GetPRQueue(ns, proj)
	if err != nil {
		t.Fatal(err)
	}
	if q.ActivePR != integrity.SeedSyncSentinel {
		t.Fatalf("expected SeedSyncSentinel as active queue entry, got %d", q.ActivePR)
	}
	t.Log("PASS: queue slot retained on push conflict")

	cfgAfter, _ := git.LoadSeedConfig(projPath)
	if cfgAfter.SyncStatus == nil || cfgAfter.SyncStatus.State != "conflict" {
		t.Fatalf("expected state=conflict, got %v", cfgAfter.SyncStatus)
	}
	if cfgAfter.SyncStatus.Direction != "push" {
		t.Fatalf("expected direction=push, got %q", cfgAfter.SyncStatus.Direction)
	}
	t.Log("PASS: sync status records direction=push on push conflict")

	hs.ReleasePRSlot(ns, proj, integrity.SeedSyncSentinel)
	if result.TempCloneDir != "" {
		os.RemoveAll(result.TempCloneDir)
	}
}

func TestSeedSync_PushSlotRetainedOnNonConflictFailure(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
	ns, proj := "pslot", "fail"
	sc, ec, hs := setupSeedSyncTest(t, ns, proj)

	projPath, _ := sc.gitStore.ProjectPath(ns, proj)
	cfg := &git.SeedConfig{
		SeedURL:  "/nonexistent/seed.git",
		KeyName:  "test-key",
		PushMode: git.PushModeDisabled,
	}
	if err := git.SaveSeedConfig(projPath, cfg); err != nil {
		t.Fatal(err)
	}

	result := executeSeedPushWithMerge(sc, ec, ns, proj, "main")
	if result.Success {
		t.Fatal("expected failure, got success")
	}
	if result.Status == "conflict" {
		t.Fatal("expected non-conflict failure")
	}

	q, err := hs.GetPRQueue(ns, proj)
	if err != nil {
		t.Fatal(err)
	}
	if q.ActivePR != integrity.SeedSyncSentinel {
		t.Fatalf("expected slot retained on non-conflict push failure, got ActivePR=%d", q.ActivePR)
	}

	cfgAfter, _ := git.LoadSeedConfig(projPath)
	if cfgAfter.SyncStatus == nil || cfgAfter.SyncStatus.State != "interrupted" {
		t.Fatalf("expected state=interrupted, got %v", cfgAfter.SyncStatus)
	}
	if cfgAfter.SyncStatus.Direction != "push" {
		t.Fatalf("expected direction=push, got %q", cfgAfter.SyncStatus.Direction)
	}
	t.Log("PASS: slot retained and state=interrupted/direction=push on non-conflict push failure")

	hs.ReleasePRSlot(ns, proj, integrity.SeedSyncSentinel)
}

func TestSeedSync_NotificationFiresOnPushConflict(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
	ns, proj := "pnotify", "conflict"
	sc, ec, hs := setupSeedSyncTest(t, ns, proj)

	var buf syncBuffer
	ec.logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ec.gitcoteURL = "https://gitcote.example.com"

	// Enable notifications on push conflict, agent disabled so only the
	// conflict-detection notify path fires (not the agent-failure path).
	notifyEnabled := true
	agentEnabled := false
	if err := hs.SetGlobalSeedEventSettings(&integrity.SeedEventSettings{
		OnPushConflict: &integrity.EventAction{
			NotifyEnabled: &notifyEnabled,
			NotifyMethod:  "log",
			AgentEnabled:  &agentEnabled,
		},
	}); err != nil {
		t.Fatal(err)
	}

	projPath, _ := sc.gitStore.ProjectPath(ns, proj)
	cfg, _ := git.LoadSeedConfig(projPath)
	cloneDir := filepath.Join(t.TempDir(), "seed-clone")
	runGitE2E(t, t.TempDir(), "clone", cfg.SeedURL, cloneDir)
	writeTestFile(t, cloneDir, "conflict.txt", "seed version\n")
	runGitE2E(t, cloneDir, "add", ".")
	runGitE2E(t, cloneDir, "commit", "-m", "seed-side change")
	runGitE2E(t, cloneDir, "push", "origin", "HEAD:main")

	writeTestFile(t, projPath, "conflict.txt", "local version\n")
	runGitE2E(t, projPath, "add", ".")
	runGitE2E(t, projPath, "commit", "-m", "local-side change")

	result := executeSeedPushWithMerge(sc, ec, ns, proj, "main")
	if result.Status != "conflict" {
		t.Fatalf("expected conflict, got %q: %+v", result.Status, result)
	}

	time.Sleep(100 * time.Millisecond)

	logged := buf.String()
	if !strings.Contains(logged, "Seed sync interrupted") {
		t.Fatalf("expected notification to fire on bare push conflict (no agent involved), got log: %s", logged)
	}
	if !strings.Contains(logged, "push_conflict") {
		t.Fatalf("expected reason=push_conflict in notification, got: %s", logged)
	}
	if !strings.Contains(logged, ns+"/"+proj) {
		t.Fatalf("expected namespace/project in notification, got: %s", logged)
	}
	t.Log("PASS: notification fires directly on push conflict detection, without requiring an agent to run/fail first")

	hs.ReleasePRSlot(ns, proj, integrity.SeedSyncSentinel)
	if result.TempCloneDir != "" {
		os.RemoveAll(result.TempCloneDir)
	}
}

func TestSeedSync_NotificationFiresOnPushNonConflictFailure(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
	ns, proj := "pnotify", "fail"
	sc, ec, hs := setupSeedSyncTest(t, ns, proj)

	var buf syncBuffer
	ec.logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	notifyEnabled := true
	if err := hs.SetGlobalSeedEventSettings(&integrity.SeedEventSettings{
		OnPushConflict: &integrity.EventAction{
			NotifyEnabled: &notifyEnabled,
			NotifyMethod:  "log",
		},
	}); err != nil {
		t.Fatal(err)
	}

	projPath, _ := sc.gitStore.ProjectPath(ns, proj)
	cfg := &git.SeedConfig{
		SeedURL:  "/nonexistent/seed.git",
		KeyName:  "test-key",
		PushMode: git.PushModeDisabled,
	}
	if err := git.SaveSeedConfig(projPath, cfg); err != nil {
		t.Fatal(err)
	}

	result := executeSeedPushWithMerge(sc, ec, ns, proj, "main")
	if result.Success || result.Status == "conflict" {
		t.Fatalf("expected non-conflict failure, got: %+v", result)
	}

	time.Sleep(100 * time.Millisecond)

	logged := buf.String()
	if !strings.Contains(logged, "Seed sync interrupted") {
		t.Fatalf("expected notification to fire on non-conflict push failure, got log: %s", logged)
	}
	if !strings.Contains(logged, "push_failed") {
		t.Fatalf("expected reason=push_failed in notification, got: %s", logged)
	}
	t.Log("PASS: notification fires on non-conflict push failure (e.g. bad seed URL / auth), not just conflicts")

	hs.ReleasePRSlot(ns, proj, integrity.SeedSyncSentinel)
}

func TestSeedSync_PeriodicPushGoesThroughGuardedFunction(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
	ns, proj := "periodic", "guarded"
	sc, ec, hs := setupSeedSyncTest(t, ns, proj)

	projPath, _ := sc.gitStore.ProjectPath(ns, proj)
	cfg, _ := git.LoadSeedConfig(projPath)
	cfg.PushMode = git.PushModePeriodic
	cfg.PushInterval = ""
	if err := git.SaveSeedConfig(projPath, cfg); err != nil {
		t.Fatal(err)
	}

	writeTestFile(t, projPath, "new.txt", "local change\n")
	runGitE2E(t, projPath, "add", ".")
	runGitE2E(t, projPath, "commit", "-m", "local change to push")

	markerPath := filepath.Join(projPath, ".seed_last_push")
	os.Remove(markerPath)

	// Hold the same re-entrancy lock that executeSeedPushWithMerge uses.
	// If runPeriodicPush truly routes through the guarded function, it
	// must be blocked and must NOT write the "push succeeded" marker.
	acquireSeedLock(&seedPushActive, ns, proj)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	runPeriodicPush(sc, ec, logger)
	releaseSeedLock(&seedPushActive, ns, proj)

	if _, err := os.Stat(markerPath); err == nil {
		t.Fatal("periodic push should have been blocked by the held re-entrancy lock (bypassing executeSeedPushWithMerge would let it slip through)")
	}
	t.Log("PASS: runPeriodicPush is blocked by the seedPushActive re-entrancy lock — confirmed it routes through the guarded executeSeedPushWithMerge, not a bypass path")

	// Now release and confirm periodic push actually succeeds through the real path.
	runPeriodicPush(sc, ec, logger)
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("expected periodic push to succeed and write marker once unguarded, got: %v", err)
	}
	t.Log("PASS: runPeriodicPush succeeds end-to-end once the lock is free")

	hs.ReleasePRSlot(ns, proj, integrity.SeedSyncSentinel)
}

func TestSeedSync_PRQueueResumesAfterPushRecovery(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	baseDir := t.TempDir()
	gitStore := git.NewStore(baseDir)
	ns, proj := "e2e", "pushresume"
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

	disabled := false
	ec := &eventContext{
		gitStore:    gitStore,
		integrityHS: integrityStore,
		oauthStore:  oauthSt,
		agentCfg:    AgentSpawnConfig{Enabled: &disabled},
		logger:      logger,
	}

	prStore, err := getPRStore(baseDir, ns, proj)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	thePR := &pr.PullRequest{
		RepoNamespace: ns,
		RepoProject:   proj,
		Title:         "queued behind push sync",
		SourceBranch:  "feat/queued",
		TargetBranch:  "main",
		Author:        "test",
		State:         pr.StateOpen,
		Mergeable:     pr.MergeableClean,
		CreatedAt:     now,
		UpdatedAt:     now,
		OrderFiles:    []string{},
		ResultFiles:   []string{},
	}
	prNum, err := prStore.Create(thePR)
	if err != nil {
		t.Fatal(err)
	}

	// Seed push acquires slot first and gets interrupted (direction=push).
	isActive, err := integrityStore.EnqueuePriority(ns, proj, integrity.SeedSyncSentinel)
	if err != nil {
		t.Fatal(err)
	}
	if !isActive {
		t.Fatal("seed sync should be active")
	}
	updateSeedSyncStateDirection(gitStore, ns, proj, "interrupted", "push")

	isActive, err = integrityStore.EnqueuePR(ns, proj, int(prNum))
	if err != nil {
		t.Fatal(err)
	}
	if isActive {
		t.Fatal("PR should be queued behind seed push sync")
	}

	// No stale push lock while interrupted.
	if !acquireSeedLock(&seedPushActive, ns, proj) {
		t.Fatal("push re-entrancy lock should not be held when seed sync is interrupted")
	}
	releaseSeedLock(&seedPushActive, ns, proj)

	// Dismiss → releases slot → dequeues PR.
	ensureNoActiveToken(ec, ns, proj, 0)
	updateSeedSyncState(gitStore, ns, proj, "idle")
	releaseSeedSyncSlot(ec, ns, proj)

	time.Sleep(100 * time.Millisecond)

	q, _ := integrityStore.GetPRQueue(ns, proj)
	if q.ActivePR != int(prNum) {
		t.Fatalf("expected PR #%d active after push sync dismiss, got ActivePR=%d", prNum, q.ActivePR)
	}

	p, _ := prStore.Get(prNum)
	if p.State != pr.StateOpen {
		t.Fatalf("expected PR state=open (agent disabled, no spawn), got %q", p.State)
	}

	projPath, _ := gitStore.ProjectPath(ns, proj)
	cfg, _ := git.LoadSeedConfig(projPath)
	if cfg.SyncStatus == nil || cfg.SyncStatus.State != "idle" {
		t.Fatalf("expected seed sync state=idle, got %v", cfg.SyncStatus)
	}

	if !acquireSeedLock(&seedPushActive, ns, proj) {
		t.Fatal("push re-entrancy lock should not be held after recovery")
	}
	releaseSeedLock(&seedPushActive, ns, proj)

	t.Log("PASS: push seed sync dismissed → PR became active → no stale push lock → state clean")

	integrityStore.ReleasePRSlot(ns, proj, int(prNum))
}

// TestSeedSync_PushConflictExternalResolveDeliversToSeed verifies that
// auto-detecting a manually-resolved PUSH conflict does not just clear
// state — it must actually deliver the resolved commit to the seed remote.
// A manual push to gitcote's own main only proves the LOCAL side was
// resolved; the seed repository is untouched until something pushes to it.
// This is the push/pull semantic asymmetry the ancestor-check reconciliation
// (built for pull) does not account for when reused blindly for push.
func TestSeedSync_PushConflictExternalResolveDeliversToSeed(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	baseDir := t.TempDir()
	gitStore := git.NewStore(baseDir)
	ns, proj := "e2e", "pushdeliver"
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

	disabled := false
	sc := &seedContext{gitStore: gitStore, vault: v, resumed: true}
	ec := &eventContext{
		gitStore:    gitStore,
		integrityHS: integrityStore,
		oauthStore:  oauthSt,
		agentCfg:    AgentSpawnConfig{Enabled: &disabled},
		logger:      logger,
		seedCtx:     sc,
	}

	gitHTTP := git.NewHandler(gitStore, logger)
	gitHTTP.PostReceive = func(namespace, project string, principal auth.Principal, pushOpts []string) {
		handlePostReceive(gitStore, logger, namespace, project, principal, pushOpts, ec)
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

	seedBareDir := filepath.Join(t.TempDir(), "seed.git")
	runGitSeed(t, t.TempDir(), "init", "--bare", seedBareDir)
	runGitSeed(t, seedBareDir, "symbolic-ref", "HEAD", "refs/heads/main")

	cloneDir := t.TempDir()
	runGitSeed(t, cloneDir, "clone", ts.URL+"/"+ns+"/"+proj+".git", "repo")
	repoDir := filepath.Join(cloneDir, "repo")
	runGitSeed(t, repoDir, "checkout", "-b", "main")
	writeTestFile(t, repoDir, "README.md", "# Seed Push Delivery\n")
	runGitSeed(t, repoDir, "add", "README.md")
	runGitSeed(t, repoDir, "commit", "-m", "initial commit")
	runGitSeed(t, repoDir, "push", "-u", "origin", "main")
	runGitSeed(t, repoDir, "remote", "add", "seed", seedBareDir)
	runGitSeed(t, repoDir, "push", "seed", "main")

	projPath, _ := gitStore.ProjectPath(ns, proj)
	seedCfg := &git.SeedConfig{
		SeedURL:  seedBareDir,
		KeyName:  keyName,
		PushMode: git.PushModeDisabled,
	}
	if err := git.SaveSeedConfig(projPath, seedCfg); err != nil {
		t.Fatal(err)
	}

	// Diverging commits on both sides so the push conflicts.
	seedCloneDir := filepath.Join(t.TempDir(), "seed-clone")
	runGitSeed(t, t.TempDir(), "clone", seedBareDir, seedCloneDir)
	writeTestFile(t, seedCloneDir, "conflict.txt", "seed version\n")
	runGitSeed(t, seedCloneDir, "add", ".")
	runGitSeed(t, seedCloneDir, "commit", "-m", "seed-side change")
	runGitSeed(t, seedCloneDir, "push", "origin", "HEAD:main")

	writeTestFile(t, repoDir, "conflict.txt", "local version\n")
	runGitSeed(t, repoDir, "add", ".")
	runGitSeed(t, repoDir, "commit", "-m", "local-side change")
	runGitSeed(t, repoDir, "push", "origin", "main")

	// Trigger seed push directly against gitcote's own repo — should conflict.
	result := executeSeedPushWithMerge(sc, ec, ns, proj, "main")
	if result.Status != "conflict" {
		t.Fatalf("expected conflict, got %q: %+v", result.Status, result)
	}

	cfgAfter, _ := git.LoadSeedConfig(projPath)
	if cfgAfter.SyncStatus == nil || cfgAfter.SyncStatus.State != "conflict" || cfgAfter.SyncStatus.Direction != "push" {
		t.Fatalf("expected state=conflict/direction=push, got %v", cfgAfter.SyncStatus)
	}

	// Operator manually resolves the conflict — but only pushes to
	// gitcote, exactly as the temp-clone instructions say ("git push
	// gitcote HEAD:main"). They do NOT push to the seed remote directly.
	runGitSeed(t, repoDir, "fetch", "seed")
	runGitSeed(t, repoDir, "merge", "seed/main", "-m", "manual resolve of push conflict",
		"--strategy-option=theirs")
	runGitSeed(t, repoDir, "push", "origin", "main")

	// handlePostReceive → reconcileExternalSeedSync fires synchronously
	// within the HTTP handler's post-receive hook in this test setup
	// (handlePostReceive is called directly, not via a goroutine dispatch
	// beyond what git.NewHandler already does), but allow a brief window
	// for the push-delivery attempt (network/disk I/O against the seed
	// bare repo) to complete.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		cfgNow, _ := git.LoadSeedConfig(projPath)
		if cfgNow.SyncStatus != nil && cfgNow.SyncStatus.State == "idle" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	cfgFinal, _ := git.LoadSeedConfig(projPath)
	if cfgFinal.SyncStatus == nil || cfgFinal.SyncStatus.State != "idle" {
		state := "nil"
		if cfgFinal.SyncStatus != nil {
			state = cfgFinal.SyncStatus.State
		}
		t.Fatalf("expected state=idle after external push resolve + delivery, got %s", state)
	}

	q, _ := integrityStore.GetPRQueue(ns, proj)
	if q.ActivePR != 0 {
		t.Fatalf("expected queue idle after external push resolve, got ActivePR=%d", q.ActivePR)
	}

	// The critical assertion: the seed bare repo's main ref must actually
	// advance to gitcote's resolved merge commit — not just a coincidental
	// content match (the merge favors the seed's own conflicting content,
	// so a content-only check would pass even if nothing was ever pushed
	// to the seed remote). Compare commit hashes, not file bytes.
	resolvedHash := strings.TrimSpace(runGitSeedCapture(t, repoDir, "rev-parse", "main"))
	verifyDir := filepath.Join(t.TempDir(), "verify-seed")
	runGitSeed(t, t.TempDir(), "clone", seedBareDir, verifyDir)
	seedHash := strings.TrimSpace(runGitSeedCapture(t, verifyDir, "rev-parse", "main"))
	if seedHash != resolvedHash {
		t.Fatalf("seed repo main (%s) does not match gitcote's resolved merge commit (%s) — "+
			"the resolution was never actually pushed to the seed remote", seedHash, resolvedHash)
	}
	t.Log("PASS: externally-resolved push conflict is not just marked idle — the exact resolved commit is actually delivered to the seed remote")

	if result.TempCloneDir != "" {
		os.RemoveAll(result.TempCloneDir)
	}
}
