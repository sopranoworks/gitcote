package main

import (
	"testing"
	"time"

	"github.com/sopranoworks/gitcote/internal/git"
	"github.com/sopranoworks/gitcote/internal/integrity"
	"github.com/sopranoworks/gitcote/internal/pr"
)

// TestReconcileOrphanedAgents_PRAgent_TransitionsToInterruptedAndClearsToken
// reproduces the restart-persistence directive's core finding: an
// AgentWorkdirRecord written "running" right before spawnAgentForPR executes
// a merger agent is never revisited if the process crashes/restarts while
// that agent is still running — nothing else ever re-examines it
// (reconcileExternalMerges only fires on a future git push). Left alone,
// the stale AgentTokenRecord it also wrote permanently blocks Retry via
// prRetryEligible's "already has an agent running" check. This confirms
// reconcileOrphanedAgents (run once at startup) closes that gap: the PR is
// transitioned to interrupted with a clear reason, and the stale token is
// revoked so Retry works again.
func TestReconcileOrphanedAgents_PRAgent_TransitionsToInterruptedAndClearsToken(t *testing.T) {
	ns, proj := "default", "orphanpr"
	_, hs, prStore, ec := setupQueueTest(t, ns, proj)

	p := createTestPR(t, prStore, ns, proj, 1, "feat-1")
	p.State = pr.StateMergeConflict
	if err := prStore.Update(p); err != nil {
		t.Fatal(err)
	}

	workPath := t.TempDir()
	if err := hs.AddAgentWorkdir(integrity.AgentWorkdirRecord{
		Path:      workPath,
		AgentName: "mock_merger",
		Role:      "merger",
		Namespace: ns,
		Project:   proj,
		PRNumber:  1,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Status:    "running",
	}); err != nil {
		t.Fatal(err)
	}
	tokenKey := agentTokenKey(ns, proj, 1)
	if err := hs.SetAgentToken(tokenKey, integrity.AgentTokenRecord{
		SeriesID:  "stale-series",
		Namespace: ns,
		Project:   proj,
		PRNumber:  1,
		AgentName: "mock_merger",
		Role:      "merger",
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
	if got.InterruptInfo == nil {
		t.Fatal("expected InterruptInfo to be set")
	}
	if got.InterruptInfo.Reason != "server_restarted" {
		t.Fatalf("reason = %q, want server_restarted", got.InterruptInfo.Reason)
	}
	if got.InterruptInfo.AgentName != "mock_merger" || got.InterruptInfo.AgentRole != "merger" {
		t.Fatalf("unexpected interrupt agent info: %+v", got.InterruptInfo)
	}

	if tok, terr := hs.GetAgentToken(tokenKey); terr != nil || tok != nil {
		t.Fatalf("expected stale agent token to be cleared, got %+v (err=%v)", tok, terr)
	}

	rec, rerr := hs.GetAgentWorkdir(workPath)
	if rerr != nil || rec == nil {
		t.Fatalf("expected workdir record to still exist, got %+v (err=%v)", rec, rerr)
	}
	if rec.Status != "orphaned" {
		t.Fatalf("workdir status = %q, want orphaned", rec.Status)
	}

	// The now-cleared token means a fresh retry is no longer blocked by
	// prRetryEligible's live-token check — the real-world proof that the
	// zombie is gone, not just that bookkeeping fields changed.
	eligible, reason := prRetryEligible(ec, got)
	if !eligible {
		t.Fatalf("expected PR to be retry-eligible after reconciliation, got ineligible: %s", reason)
	}
}

// TestReconcileOrphanedAgents_PRAgent_LeavesTerminalStateAlone confirms
// reconciliation doesn't clobber a PR that had already reached a terminal
// state (e.g. merged via a manual push) before the crash — only the stale
// bookkeeping (token, workdir status) should be cleared.
func TestReconcileOrphanedAgents_PRAgent_LeavesTerminalStateAlone(t *testing.T) {
	ns, proj := "default", "orphanterminal"
	_, hs, prStore, ec := setupQueueTest(t, ns, proj)

	p := createTestPR(t, prStore, ns, proj, 1, "feat-1")
	p.State = pr.StateMerged
	if err := prStore.Update(p); err != nil {
		t.Fatal(err)
	}

	workPath := t.TempDir()
	if err := hs.AddAgentWorkdir(integrity.AgentWorkdirRecord{
		Path:      workPath,
		AgentName: "mock_merger",
		Role:      "merger",
		Namespace: ns,
		Project:   proj,
		PRNumber:  1,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Status:    "running",
	}); err != nil {
		t.Fatal(err)
	}

	reconcileOrphanedAgents(ec, ec.logger)

	got, err := prStore.Get(1)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != pr.StateMerged {
		t.Fatalf("state = %q, want merged (terminal state must not be overwritten)", got.State)
	}
	if got.InterruptInfo != nil {
		t.Fatalf("expected no InterruptInfo on a terminal-state PR, got %+v", got.InterruptInfo)
	}

	rec, rerr := hs.GetAgentWorkdir(workPath)
	if rerr != nil || rec == nil || rec.Status != "orphaned" {
		t.Fatalf("expected workdir record status orphaned regardless, got %+v (err=%v)", rec, rerr)
	}
}

// TestReconcileOrphanedAgents_IgnoresNonRunningRecords confirms
// reconciliation only touches records still marked "running" — a
// successfully completed/failed agent run must be left exactly as its own
// exit-path code left it.
func TestReconcileOrphanedAgents_IgnoresNonRunningRecords(t *testing.T) {
	ns, proj := "default", "orphanskip"
	_, hs, prStore, ec := setupQueueTest(t, ns, proj)
	createTestPR(t, prStore, ns, proj, 1, "feat-1")

	workPath := t.TempDir()
	if err := hs.AddAgentWorkdir(integrity.AgentWorkdirRecord{
		Path:      workPath,
		AgentName: "mock_reviewer",
		Role:      "reviewer",
		Namespace: ns,
		Project:   proj,
		PRNumber:  1,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Status:    "completed",
		ExitCode:  0,
	}); err != nil {
		t.Fatal(err)
	}

	reconcileOrphanedAgents(ec, ec.logger)

	got, err := prStore.Get(1)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != pr.StateOpen {
		t.Fatalf("state = %q, want open (untouched)", got.State)
	}
	if got.InterruptInfo != nil {
		t.Fatalf("expected no InterruptInfo, got %+v", got.InterruptInfo)
	}

	rec, rerr := hs.GetAgentWorkdir(workPath)
	if rerr != nil || rec == nil || rec.Status != "completed" {
		t.Fatalf("expected completed record left untouched, got %+v (err=%v)", rec, rerr)
	}
}

// TestReconcileOrphanedAgents_SeedSync_TransitionsAndPreservesDirection
// mirrors the PR-agent case for seed sync: an AgentWorkdirRecord for a
// merger agent resolving a seed-pull/push conflict (PRNumber 0) left
// "running" at crash time must be reconciled into an interrupted SyncStatus
// with a clear reason, preserving whichever direction (pull/push) was
// already recorded before the agent was spawned — reconciliation has no
// other way to know the direction, since AgentWorkdirRecord doesn't carry it.
func TestReconcileOrphanedAgents_SeedSync_TransitionsAndPreservesDirection(t *testing.T) {
	ns, proj := "orphanseedns", "demo"
	sc, ec, hs := setupSeedSyncTest(t, ns, proj)

	updateSeedSyncStateDetail(sc.gitStore, ns, proj, "conflict", "push", "push_conflict", "manual merge required")

	workPath := t.TempDir()
	if err := hs.AddAgentWorkdir(integrity.AgentWorkdirRecord{
		Path:      workPath,
		AgentName: "mock_merger",
		Role:      "merger",
		Namespace: ns,
		Project:   proj,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Status:    "running",
	}); err != nil {
		t.Fatal(err)
	}
	tokenKey := agentTokenKey(ns, proj, 0)
	if err := hs.SetAgentToken(tokenKey, integrity.AgentTokenRecord{
		SeriesID:  "stale-seed-series",
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
	if cfg.SyncStatus == nil {
		t.Fatal("expected SyncStatus to be set")
	}
	if cfg.SyncStatus.State != "interrupted" {
		t.Fatalf("state = %q, want interrupted", cfg.SyncStatus.State)
	}
	if cfg.SyncStatus.Reason != "server_restarted" {
		t.Fatalf("reason = %q, want server_restarted", cfg.SyncStatus.Reason)
	}
	if cfg.SyncStatus.Direction != "push" {
		t.Fatalf("direction = %q, want push (preserved from pre-crash state)", cfg.SyncStatus.Direction)
	}

	if tok, terr := hs.GetAgentToken(tokenKey); terr != nil || tok != nil {
		t.Fatalf("expected stale seed sync agent token to be cleared, got %+v (err=%v)", tok, terr)
	}

	rec, rerr := hs.GetAgentWorkdir(workPath)
	if rerr != nil || rec == nil || rec.Status != "orphaned" {
		t.Fatalf("expected workdir record status orphaned, got %+v (err=%v)", rec, rerr)
	}
}
