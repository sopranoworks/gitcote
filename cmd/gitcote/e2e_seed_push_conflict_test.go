package main

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sopranoworks/gitcote/internal/git"
	"github.com/sopranoworks/gitcote/internal/integrity"
	"github.com/sopranoworks/gitcote/internal/vault"
	"github.com/sopranoworks/shoka/pkg/oauthstore"
)

func TestSeedPushConflict_QueueAndAgentWiring(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	baseDir := t.TempDir()
	gitStore := git.NewStore(baseDir)
	ns, proj := "e2e", "seedpush"
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if err := gitStore.CreateRepo(ns, proj); err != nil {
		t.Fatal(err)
	}
	projPath, _ := gitStore.ProjectPath(ns, proj)

	// Create a local bare repo to act as the seed (no SSH needed).
	seedBareDir := filepath.Join(t.TempDir(), "seed.git")
	runGitE2E(t, t.TempDir(), "init", "--bare", seedBareDir)
	runGitE2E(t, seedBareDir, "symbolic-ref", "HEAD", "refs/heads/main")

	// Write an initial commit to the gitcote repo and push to the bare seed.
	writeTestFile(t, projPath, "README.md", "# seed push test\n")
	runGitE2E(t, projPath, "add", ".")
	runGitE2E(t, projPath, "commit", "-m", "initial commit")
	runGitE2E(t, projPath, "remote", "add", "seed", seedBareDir)
	runGitE2E(t, projPath, "push", "seed", "main")

	// Create a diverging commit on the seed via a temp clone.
	cloneDir := filepath.Join(t.TempDir(), "seed-clone")
	runGitE2E(t, t.TempDir(), "clone", seedBareDir, cloneDir)
	writeTestFile(t, cloneDir, "conflict.txt", "seed version\n")
	runGitE2E(t, cloneDir, "add", ".")
	runGitE2E(t, cloneDir, "commit", "-m", "seed-side change")
	runGitE2E(t, cloneDir, "push", "origin", "HEAD:main")

	// Create a diverging commit on the gitcote repo (same file, different content).
	writeTestFile(t, projPath, "conflict.txt", "local version\n")
	runGitE2E(t, projPath, "add", ".")
	runGitE2E(t, projPath, "commit", "-m", "local-side change")

	// Set up vault (seed config needs a key, but local push doesn't use SSH).
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

	// Save seed config pointing to local bare repo.
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
	defer integrityStore.Close()
	headStore = integrityStore

	oauthSt, err := oauthstore.Open(filepath.Join(baseDir, "oauth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer oauthSt.Close()

	// Configure OnPushConflict agent settings.
	agentEnabled := true
	seedSettings := &integrity.SeedEventSettings{
		OnPushConflict: &integrity.EventAction{
			AgentEnabled: &agentEnabled,
			AgentName:    "test-merger",
		},
	}
	if err := integrityStore.SetGlobalSeedEventSettings(seedSettings); err != nil {
		t.Fatal(err)
	}

	agentDisabled := false
	evtCtx := &eventContext{
		gitStore:    gitStore,
		integrityHS: integrityStore,
		oauthStore:  oauthSt,
		agentCfg:    AgentSpawnConfig{Enabled: &agentDisabled},
		logger:      logger,
	}

	sc := &seedContext{
		gitStore:   gitStore,
		vault:      v,
		gitcoteURL: "",
		resumed:    true,
	}

	// --- Test 1: Push with conflict goes through queue and returns conflict ---
	result := executeSeedPushWithMerge(sc, evtCtx, ns, proj, "main")
	if result.Success {
		t.Fatal("expected push to fail with conflict, but it succeeded")
	}
	if result.Status != "conflict" {
		t.Fatalf("expected status=conflict, got %q (msg: %s)", result.Status, result.Message)
	}
	if len(result.Conflicts) == 0 {
		t.Fatal("expected at least one conflict entry")
	}
	t.Logf("PASS: push conflict detected with %d conflict(s)", len(result.Conflicts))

	// Verify temp clone was created.
	if result.TempCloneDir == "" {
		t.Fatal("expected temp clone dir to be set on conflict")
	}
	if _, err := os.Stat(result.TempCloneDir); os.IsNotExist(err) {
		t.Fatalf("temp clone dir does not exist: %s", result.TempCloneDir)
	}
	t.Logf("PASS: temp clone created at %s", result.TempCloneDir)

	// Verify seed sync state was set to error/conflict.
	cfgAfter, err := git.LoadSeedConfig(projPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfgAfter.SyncStatus == nil {
		t.Fatal("expected sync status to be set after conflict")
	}
	if cfgAfter.SyncStatus.State != "conflict" {
		t.Fatalf("sync status state = %q, want conflict", cfgAfter.SyncStatus.State)
	}
	if cfgAfter.SyncStatus.Reason != "push_conflict" {
		t.Fatalf("sync status reason = %q, want push_conflict", cfgAfter.SyncStatus.Reason)
	}
	if cfgAfter.SyncStatus.LastResult == "" {
		t.Fatal("expected sync status last_result to carry failure detail, got empty")
	}
	t.Logf("PASS: sync status updated (state=%s, reason=%s, result=%s)", cfgAfter.SyncStatus.State, cfgAfter.SyncStatus.Reason, cfgAfter.SyncStatus.LastResult)

	// Verify onSeedPushConflict would check the right settings.
	if !seedPushConflictAgentEnabled(evtCtx, ns, proj) {
		t.Fatal("seedPushConflictAgentEnabled should return true with OnPushConflict configured")
	}
	t.Logf("PASS: seedPushConflictAgentEnabled returns true")

	// --- Test 2: Queue slot is retained on conflict (slot retention pattern) ---
	// Wait briefly for the async onSeedPushConflict goroutine to start.
	time.Sleep(100 * time.Millisecond)

	// The queue slot should be held. Conflict retains the slot so
	// PR auto-merge is suspended until operator resolves (retry or dismiss).
	q, qerr := integrityStore.GetPRQueue(ns, proj)
	if qerr != nil {
		t.Fatalf("get queue: %v", qerr)
	}
	if q.ActivePR != integrity.SeedSyncSentinel {
		t.Fatalf("expected SeedSyncSentinel retained, got ActivePR=%d", q.ActivePR)
	}
	t.Logf("PASS: queue slot retained on conflict (slot retention)")

	// Release for cleanup.
	integrityStore.ReleasePRSlot(ns, proj, integrity.SeedSyncSentinel)

	// --- Test 3: Verify seedPushConflictAgentEnabled returns false when not configured ---
	integrityStore.SetGlobalSeedEventSettings(&integrity.SeedEventSettings{})
	if seedPushConflictAgentEnabled(evtCtx, ns, proj) {
		t.Fatal("seedPushConflictAgentEnabled should return false without OnPushConflict")
	}
	t.Logf("PASS: seedPushConflictAgentEnabled returns false when not configured")

	// Cleanup temp clone.
	os.RemoveAll(result.TempCloneDir)
}

func TestSeedPush_QueuedWhenPRActive(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	baseDir := t.TempDir()
	gitStore := git.NewStore(baseDir)
	ns, proj := "e2e", "seedpushq"
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if err := gitStore.CreateRepo(ns, proj); err != nil {
		t.Fatal(err)
	}
	projPath, _ := gitStore.ProjectPath(ns, proj)

	seedBareDir := filepath.Join(t.TempDir(), "seed.git")
	runGitE2E(t, t.TempDir(), "init", "--bare", seedBareDir)

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

	sc := &seedContext{
		gitStore:   gitStore,
		vault:      v,
		gitcoteURL: "",
		resumed:    true,
	}

	// Simulate an active PR by occupying the queue slot.
	prNumber := 42
	isActive, err := integrityStore.EnqueuePriority(ns, proj, prNumber)
	if err != nil {
		t.Fatal(err)
	}
	if !isActive {
		t.Fatal("PR should be active (first enqueue)")
	}

	// Attempt seed push — should be queued.
	result := executeSeedPushWithMerge(sc, evtCtx, ns, proj, "main")
	if result.Success {
		t.Fatal("expected push to be queued, but it succeeded")
	}
	if result.Status != "queued" {
		t.Fatalf("expected status=queued, got %q (msg: %s)", result.Status, result.Message)
	}
	t.Logf("PASS: seed push correctly queued when PR is active (msg: %s)", result.Message)

	// Release the PR slot — this should dequeue the seed sync.
	integrityStore.ReleasePRSlot(ns, proj, prNumber)
}
