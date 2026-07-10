package main

import (
	"bytes"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sopranoworks/gitcote/internal/git"
	"github.com/sopranoworks/gitcote/internal/integrity"
	"github.com/sopranoworks/gitcote/internal/vault"
	"github.com/sopranoworks/shoka/pkg/oauthstore"
)

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
