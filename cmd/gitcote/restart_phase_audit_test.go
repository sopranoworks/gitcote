package main

import (
	"os/exec"
	"testing"
	"time"

	"github.com/sopranoworks/gitcote/internal/git"
	"github.com/sopranoworks/gitcote/internal/integrity"
	"github.com/sopranoworks/gitcote/internal/pr"
)

// TestReconcileOrphanedTokens_PRToken_NoWorkdirRecord_TransitionsToInterrupted
// reproduces the "spawn-starting" restart gap identified by the
// comprehensive-restart-phase-audit directive: issueAgentToken durably
// writes an AgentTokenRecord *before* AddAgentWorkdir is ever called
// (agent.PrepareWorkDir/WriteMCPConfig disk I/O sits in between). A crash in
// that exact window leaves a token with no matching "running" workdir
// record — invisible to reconcileOrphanedAgents's original workdir-only
// sweep, which would otherwise leave prRetryEligible permanently refusing
// Retry with no diagnostic trail at all.
func TestReconcileOrphanedTokens_PRToken_NoWorkdirRecord_TransitionsToInterrupted(t *testing.T) {
	ns, proj := "default", "spawnstarting"
	_, hs, prStore, ec := setupQueueTest(t, ns, proj)

	p := createTestPR(t, prStore, ns, proj, 1, "feat-1")
	// No AddAgentWorkdir call at all — simulating a crash between
	// issueAgentToken succeeding and AddAgentWorkdir ever being reached.
	tokenKey := agentTokenKey(ns, proj, 1)
	if err := hs.SetAgentToken(tokenKey, integrity.AgentTokenRecord{
		SeriesID:  "stale-spawn-starting-series",
		Namespace: ns,
		Project:   proj,
		PRNumber:  1,
		AgentName: "mock_reviewer",
		Role:      "reviewer",
		IssuedAt:  time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}

	reconcileOrphanedAgents(ec, ec.logger)

	got, err := prStore.Get(1)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != pr.StateInterrupted {
		t.Fatalf("state = %q, want interrupted", got.State)
	}
	if got.InterruptInfo == nil || got.InterruptInfo.Reason != "server_restarted" {
		t.Fatalf("unexpected interrupt info: %+v", got.InterruptInfo)
	}
	if tok, terr := hs.GetAgentToken(tokenKey); terr != nil || tok != nil {
		t.Fatalf("expected stale token to be cleared, got %+v (err=%v)", tok, terr)
	}
	eligible, reason := prRetryEligible(ec, got)
	if !eligible {
		t.Fatalf("expected PR retry-eligible after reconciliation, got: %s", reason)
	}
	if p.RepoNamespace != ns {
		t.Fatal("sanity: PR namespace mismatch")
	}
	t.Log("PASS: a token orphaned before any workdir record was ever written is still reconciled")
}

// TestReconcileOrphanedTokens_SeedSyncToken_NoWorkdirRecord mirrors the PR
// case for seed sync (PRNumber 0), confirming Direction is preserved from
// whatever SyncStatus already recorded before the crash.
func TestReconcileOrphanedTokens_SeedSyncToken_NoWorkdirRecord(t *testing.T) {
	ns, proj := "spawnstartseedns", "demo"
	sc, ec, hs := setupSeedSyncTest(t, ns, proj)

	updateSeedSyncStateDetail(sc.gitStore, ns, proj, "conflict", "push", "push_conflict", "manual merge required")

	tokenKey := agentTokenKey(ns, proj, 0)
	if err := hs.SetAgentToken(tokenKey, integrity.AgentTokenRecord{
		SeriesID:  "stale-seed-spawn-starting-series",
		Namespace: ns,
		Project:   proj,
		AgentName: "mock_merger",
		Role:      "merger",
		IssuedAt:  time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}

	reconcileOrphanedAgents(ec, ec.logger)

	projPath, err := sc.gitStore.ProjectPath(ns, proj)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := git.LoadSeedConfig(projPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SyncStatus == nil || cfg.SyncStatus.State != "interrupted" || cfg.SyncStatus.Reason != "server_restarted" {
		t.Fatalf("unexpected sync status: %+v", cfg.SyncStatus)
	}
	if cfg.SyncStatus.Direction != "push" {
		t.Fatalf("direction = %q, want push (preserved)", cfg.SyncStatus.Direction)
	}
	if tok, terr := hs.GetAgentToken(tokenKey); terr != nil || tok != nil {
		t.Fatalf("expected stale seed token to be cleared, got %+v (err=%v)", tok, terr)
	}
}

// TestReconcileExternalMerges_ApprovedPR_AlreadyMergedInGit reproduces the
// most severe finding of the comprehensive-restart-phase-audit directive:
// handlePRMerge/autoMergePR/reattemptMerge all write the git merge ref
// BEFORE the PR's own prStore.Update(StateMerged) — a crash in between
// leaves the merge already landed in git while PR.State is still
// StateApproved. Before this fix, reconcileExternalMerges's state gate
// excluded StateApproved entirely, so this PR would never be reconciled by
// ANYTHING — not at startup, not even by a future git push — a permanent
// dead end. This test simulates exactly that crash point and confirms
// startup reconciliation (reusing reconcileExternalMerges, now widened to
// include StateApproved) closes it.
func TestReconcileExternalMerges_ApprovedPR_AlreadyMergedInGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
	ns, proj := "default", "approvedmerged"
	gitStore, hs, prStore, ec := setupQueueTest(t, ns, proj)

	projPath, err := gitStore.ProjectPath(ns, proj)
	if err != nil {
		t.Fatal(err)
	}

	writeTestFile(t, projPath, "README.md", "# base\n")
	runGit2Stage(t, projPath, "add", ".")
	runGit2Stage(t, projPath, "commit", "-m", "initial commit")
	runGit2Stage(t, projPath, "branch", "-M", "main")

	runGit2Stage(t, projPath, "checkout", "-b", "feat/approved")
	writeTestFile(t, projPath, "feature.go", "package main\n")
	runGit2Stage(t, projPath, "add", ".")
	runGit2Stage(t, projPath, "commit", "-m", "add feature")

	p := createTestPR(t, prStore, ns, proj, 1, "feat/approved")
	p.TargetBranch = "main"
	p.State = pr.StateApproved
	if err := prStore.Update(p); err != nil {
		t.Fatal(err)
	}
	isActive, err := hs.EnqueuePR(ns, proj, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !isActive {
		t.Fatal("PR should be the active queue entry")
	}

	// The exact crash window: the target ref write has already landed
	// (handlePRMerge/autoMergePR write it before marking StateMerged), but
	// the process died before that second write ever happened.
	runGit2Stage(t, projPath, "checkout", "main")
	runGit2Stage(t, projPath, "merge", "feat/approved", "-m", "merge (simulating a crash right after this ref write)")

	reconcileExternalMerges(gitStore, ec, ns, proj, ec.logger)

	got, err := prStore.Get(1)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != pr.StateMerged {
		t.Fatalf("state = %q, want merged (git already contains the merge)", got.State)
	}
	if got.MergeCommit == "" {
		t.Fatal("expected merge_commit to be set")
	}
	q, _ := hs.GetPRQueue(ns, proj)
	if q.ActivePR != 0 {
		t.Fatalf("expected queue slot released, active = %d", q.ActivePR)
	}
	t.Log("PASS: an Approved PR whose merge already landed in git before a crash is reconciled, not left stuck forever")
}

// TestReconcileExternalMerges_ApprovedPR_NotYetMerged_LeftAlone confirms the
// widened state filter doesn't misfire for a completely normal, legitimately
// pending Approved PR (source genuinely not yet merged into target).
func TestReconcileExternalMerges_ApprovedPR_NotYetMerged_LeftAlone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
	ns, proj := "default", "approvedpending"
	gitStore, hs, prStore, ec := setupQueueTest(t, ns, proj)

	projPath, err := gitStore.ProjectPath(ns, proj)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, projPath, "README.md", "# base\n")
	runGit2Stage(t, projPath, "add", ".")
	runGit2Stage(t, projPath, "commit", "-m", "initial commit")
	runGit2Stage(t, projPath, "branch", "-M", "main")

	runGit2Stage(t, projPath, "checkout", "-b", "feat/pending")
	writeTestFile(t, projPath, "feature.go", "package main\n")
	runGit2Stage(t, projPath, "add", ".")
	runGit2Stage(t, projPath, "commit", "-m", "add feature")
	runGit2Stage(t, projPath, "checkout", "main")

	p := createTestPR(t, prStore, ns, proj, 1, "feat/pending")
	p.TargetBranch = "main"
	p.State = pr.StateApproved
	if err := prStore.Update(p); err != nil {
		t.Fatal(err)
	}
	if _, err := hs.EnqueuePR(ns, proj, 1); err != nil {
		t.Fatal(err)
	}

	reconcileExternalMerges(gitStore, ec, ns, proj, ec.logger)

	got, err := prStore.Get(1)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != pr.StateApproved {
		t.Fatalf("state = %q, want approved (unmerged PR must not be touched)", got.State)
	}
	q, _ := hs.GetPRQueue(ns, proj)
	if q.ActivePR != 1 {
		t.Fatalf("expected queue slot still held by PR #1, got active=%d", q.ActivePR)
	}
}

// TestReconcileExternalSeedSync_ErrorState_RecognizedByGuard reproduces the
// push-path-specific gap: doSeedPush's own interior write sets
// SyncStatus.State to git.SeedStateError ("error") before the caller
// overwrites it with the correct "conflict"/"interrupted" value. A crash in
// that window previously left a State value reconcileExternalSeedSync's
// guard didn't recognize at all — defeating even a FUTURE push's ability to
// self-heal it, since the function returned before ever checking git-ref
// ancestry.
func TestReconcileExternalSeedSync_ErrorState_RecognizedByGuard(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
	ns, proj := "errstateseedns", "demo"
	sc, ec, hs := setupSeedSyncTest(t, ns, proj)

	projPath, err := sc.gitStore.ProjectPath(ns, proj)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := git.LoadSeedConfig(projPath)
	if err != nil {
		t.Fatal(err)
	}

	// Populate refs/remotes/seed/main directly, exactly as doSeedPull/
	// doSeedPush's internal fetch would — seed and main start identical
	// (per setupSeedSyncTest's own setup), so seed is trivially an ancestor
	// once this ref exists.
	runGit2Stage(t, projPath, "fetch", cfg.SeedURL, "main:refs/remotes/seed/main")

	isActive, err := hs.EnqueuePriority(ns, proj, integrity.SeedSyncSentinel)
	if err != nil {
		t.Fatal(err)
	}
	if !isActive {
		t.Fatal("seed sync should be the active queue entry")
	}

	// The exact stale value doSeedPush's own interior write can leave
	// behind if a crash lands before the caller's follow-up
	// updateSeedSyncStateDetail overwrites it.
	if err := git.UpdateSeedStatus(projPath, &git.SeedSyncStatus{
		State:      git.SeedStateError,
		Direction:  "pull",
		LastResult: "conflict",
	}); err != nil {
		t.Fatal(err)
	}

	reconcileExternalSeedSync(sc.gitStore, ec, ns, proj, ec.logger)

	cfgAfter, err := git.LoadSeedConfig(projPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfgAfter.SyncStatus == nil || cfgAfter.SyncStatus.State != "idle" {
		state := "nil"
		if cfgAfter.SyncStatus != nil {
			state = cfgAfter.SyncStatus.State
		}
		t.Fatalf("state = %q, want idle (guard should now recognize SeedStateError and act on the ancestry match)", state)
	}
	q, _ := hs.GetPRQueue(ns, proj)
	if q.ActivePR != 0 {
		t.Fatalf("expected seed sync slot released, active = %d", q.ActivePR)
	}
}

// TestReconcileIdleSeedSyncSlot_ReleasesStuckSlot reproduces the narrow
// "mid-reconciliation-itself" gap explicitly called out in §1 phase 12 of
// the audit directive: updateSeedSyncState("idle") and releaseSeedSyncSlot
// are two separate bbolt writes; a crash between them (in
// verifySeedSyncAfterAgent or reconcileExternalSeedSync/
// reconcileExternalPushSync alike) leaves SyncStatus correctly "idle" but
// the seed-sync queue slot still held — permanently, since
// reconcileExternalSeedSync's own gate only proceeds for
// interrupted/conflict/error, never idle.
func TestReconcileIdleSeedSyncSlot_ReleasesStuckSlot(t *testing.T) {
	ns, proj := "idleseedns", "demo"
	sc, ec, hs := setupSeedSyncTest(t, ns, proj)

	isActive, err := hs.EnqueuePriority(ns, proj, integrity.SeedSyncSentinel)
	if err != nil {
		t.Fatal(err)
	}
	if !isActive {
		t.Fatal("seed sync should be the active queue entry")
	}
	updateSeedSyncState(sc.gitStore, ns, proj, "idle")

	reconcileIdleSeedSyncSlot(ec, ns, proj, ec.logger)

	q, _ := hs.GetPRQueue(ns, proj)
	if q.ActivePR != 0 {
		t.Fatalf("expected stuck idle slot to be released, active = %d", q.ActivePR)
	}
}

// TestReconcileIdleSeedSyncSlot_LeavesGenuineConflictAlone confirms the
// idle-only check never touches a slot that's legitimately still held for
// an unresolved conflict/interrupt.
func TestReconcileIdleSeedSyncSlot_LeavesGenuineConflictAlone(t *testing.T) {
	ns, proj := "idleseedns2", "demo"
	sc, ec, hs := setupSeedSyncTest(t, ns, proj)

	isActive, err := hs.EnqueuePriority(ns, proj, integrity.SeedSyncSentinel)
	if err != nil {
		t.Fatal(err)
	}
	if !isActive {
		t.Fatal("seed sync should be the active queue entry")
	}
	updateSeedSyncStateDetail(sc.gitStore, ns, proj, "conflict", "pull", "pull_conflict", "manual merge required")

	reconcileIdleSeedSyncSlot(ec, ns, proj, ec.logger)

	q, _ := hs.GetPRQueue(ns, proj)
	if q.ActivePR != integrity.SeedSyncSentinel {
		t.Fatalf("expected genuinely-conflicted slot to remain held, active = %d", q.ActivePR)
	}
}

// TestReconcileQueueMembership_EnqueuesMissingPR reproduces the "PR created
// but never enqueued" gap: prStore.Create and integrityHS.EnqueuePR are two
// writes to two separate bbolt databases, not atomic with each other. A
// crash between them leaves a durable PR with no queue entry at all — no
// reviewer is ever spawned, and since the PR row already exists, even
// re-pushing the same branch pair is refused, so there's no natural retry
// path either.
func TestReconcileQueueMembership_EnqueuesMissingPR(t *testing.T) {
	ns, proj := "default", "neverqueued"
	_, hs, prStore, ec := setupQueueTest(t, ns, proj)

	// Simulate the crash: the PR row exists, EnqueuePR was never called.
	createTestPR(t, prStore, ns, proj, 1, "feat-1")

	qBefore, _ := hs.GetPRQueue(ns, proj)
	if qBefore.ActivePR != 0 || len(qBefore.Waiting) != 0 {
		t.Fatalf("sanity: expected empty queue before reconciliation, got %+v", qBefore)
	}

	reconcileQueueMembership(ec, ns, proj, ec.logger)

	qAfter, _ := hs.GetPRQueue(ns, proj)
	if qAfter.ActivePR != 1 {
		t.Fatalf("expected PR #1 to become the active queue entry, got %+v", qAfter)
	}
}

// TestReconcileQueueMembership_IdempotentForAlreadyQueuedPRs confirms the
// sweep never duplicates an already-correctly-queued PR (EnqueuePriority's
// own duplicate check makes this safe to call unconditionally).
func TestReconcileQueueMembership_IdempotentForAlreadyQueuedPRs(t *testing.T) {
	ns, proj := "default", "alreadyqueued"
	_, hs, prStore, ec := setupQueueTest(t, ns, proj)

	createTestPR(t, prStore, ns, proj, 1, "feat-1")
	if _, err := hs.EnqueuePR(ns, proj, 1); err != nil {
		t.Fatal(err)
	}
	createTestPR(t, prStore, ns, proj, 2, "feat-2")
	if _, err := hs.EnqueuePR(ns, proj, 2); err != nil {
		t.Fatal(err)
	}

	reconcileQueueMembership(ec, ns, proj, ec.logger)
	reconcileQueueMembership(ec, ns, proj, ec.logger)

	q, _ := hs.GetPRQueue(ns, proj)
	if q.ActivePR != 1 {
		t.Fatalf("expected PR #1 to remain active, got %d", q.ActivePR)
	}
	if len(q.Waiting) != 1 || q.Waiting[0] != 2 {
		t.Fatalf("expected exactly one waiting entry (PR #2), no duplicates, got %+v", q.Waiting)
	}
}

// TestReconcileQueueMembership_SkipsTerminalPRs confirms merged/rejected/
// closed PRs are never re-enqueued.
func TestReconcileQueueMembership_SkipsTerminalPRs(t *testing.T) {
	ns, proj := "default", "terminalskip"
	_, hs, prStore, ec := setupQueueTest(t, ns, proj)

	p := createTestPR(t, prStore, ns, proj, 1, "feat-1")
	p.State = pr.StateMerged
	if err := prStore.Update(p); err != nil {
		t.Fatal(err)
	}

	reconcileQueueMembership(ec, ns, proj, ec.logger)

	q, _ := hs.GetPRQueue(ns, proj)
	if q.ActivePR != 0 || len(q.Waiting) != 0 {
		t.Fatalf("expected a merged PR to stay unqueued, got %+v", q)
	}
}
