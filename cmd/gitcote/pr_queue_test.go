package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sopranoworks/gitcote/internal/git"
	"github.com/sopranoworks/gitcote/internal/integrity"
	"github.com/sopranoworks/gitcote/internal/pr"
	"github.com/sopranoworks/shoka/pkg/oauthstore"
)

func setupQueueTest(t *testing.T, ns, proj string) (*git.Store, *integrity.Store, *pr.Store, *eventContext) {
	t.Helper()
	baseDir := t.TempDir()
	gitStore := git.NewStore(baseDir)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if err := gitStore.CreateRepo(ns, proj); err != nil {
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

	prStore, err := getPRStore(baseDir, ns, proj)
	if err != nil {
		t.Fatal(err)
	}

	return gitStore, integrityStore, prStore, ec
}

func createTestPR(t *testing.T, prStore *pr.Store, ns, proj string, num int, source string) *pr.PullRequest {
	t.Helper()
	now := time.Now()
	p := &pr.PullRequest{
		RepoNamespace: ns,
		RepoProject:   proj,
		Title:         source,
		SourceBranch:  source,
		TargetBranch:  "main",
		Author:        "test",
		State:         pr.StateOpen,
		Mergeable:     pr.MergeableClean,
		CreatedAt:     now,
		UpdatedAt:     now,
		OrderFiles:    []string{},
		ResultFiles:   []string{},
	}
	n, err := prStore.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if int(n) != num {
		t.Fatalf("expected PR #%d, got #%d", num, n)
	}
	return p
}

func TestPRQueue_BasicFIFO(t *testing.T) {
	ns, proj := "default", "fifo"
	_, hs, prStore, ec := setupQueueTest(t, ns, proj)

	// Create 3 PRs and enqueue them
	pr1 := createTestPR(t, prStore, ns, proj, 1, "feat-1")
	isActive, err := hs.EnqueuePR(ns, proj, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !isActive {
		t.Error("PR #1 should be active")
	}

	pr2 := createTestPR(t, prStore, ns, proj, 2, "feat-2")
	isActive, _ = hs.EnqueuePR(ns, proj, 2)
	if isActive {
		t.Error("PR #2 should be queued")
	}

	_ = createTestPR(t, prStore, ns, proj, 3, "feat-3")
	isActive, _ = hs.EnqueuePR(ns, proj, 3)
	if isActive {
		t.Error("PR #3 should be queued")
	}

	// Verify queue state
	q, _ := hs.GetPRQueue(ns, proj)
	if q.ActivePR != 1 {
		t.Errorf("active = %d, want 1", q.ActivePR)
	}
	if len(q.Waiting) != 2 || q.Waiting[0] != 2 || q.Waiting[1] != 3 {
		t.Errorf("waiting = %v, want [2 3]", q.Waiting)
	}

	// PR #1 merged → PR #2 becomes active
	now := time.Now()
	pr1.State = pr.StateMerged
	pr1.MergedAt = &now
	pr1.UpdatedAt = now
	_ = prStore.Update(pr1)

	releasePRSlotAndDequeue(ec, ns, proj, 1)

	// Give the goroutine a moment (onPRCreated is called in a goroutine)
	time.Sleep(50 * time.Millisecond)

	q, _ = hs.GetPRQueue(ns, proj)
	if q.ActivePR != 2 {
		t.Errorf("after merge #1: active = %d, want 2", q.ActivePR)
	}
	if len(q.Waiting) != 1 || q.Waiting[0] != 3 {
		t.Errorf("after merge #1: waiting = %v, want [3]", q.Waiting)
	}

	// PR #2 rejected → PR #3 becomes active
	pr2.State = pr.StateRejected
	pr2.UpdatedAt = time.Now()
	_ = prStore.Update(pr2)

	releasePRSlotAndDequeue(ec, ns, proj, 2)
	time.Sleep(50 * time.Millisecond)

	q, _ = hs.GetPRQueue(ns, proj)
	if q.ActivePR != 3 {
		t.Errorf("after reject #2: active = %d, want 3", q.ActivePR)
	}
	if len(q.Waiting) != 0 {
		t.Errorf("after reject #2: waiting = %v, want []", q.Waiting)
	}
}

func TestPRQueue_DifferentProjects(t *testing.T) {
	baseDir := t.TempDir()
	gitStore := git.NewStore(baseDir)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	for _, proj := range []string{"projA", "projB"} {
		if err := gitStore.CreateRepo("ns", proj); err != nil {
			t.Fatal(err)
		}
	}

	hs, err := integrity.Open(filepath.Join(baseDir, "repo_heads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer hs.Close()

	// Both projects get an active PR simultaneously
	activeA, _ := hs.EnqueuePR("ns", "projA", 1)
	activeB, _ := hs.EnqueuePR("ns", "projB", 1)

	if !activeA {
		t.Error("projA PR #1 should be active")
	}
	if !activeB {
		t.Error("projB PR #1 should be active (independent)")
	}

	// Enqueue a second PR in projA — projB is not affected
	activeA2, _ := hs.EnqueuePR("ns", "projA", 2)
	if activeA2 {
		t.Error("projA PR #2 should be queued")
	}

	qA, _ := hs.GetPRQueue("ns", "projA")
	qB, _ := hs.GetPRQueue("ns", "projB")
	if qA.ActivePR != 1 || len(qA.Waiting) != 1 {
		t.Errorf("projA: active=%d waiting=%v", qA.ActivePR, qA.Waiting)
	}
	if qB.ActivePR != 1 || len(qB.Waiting) != 0 {
		t.Errorf("projB: active=%d waiting=%v (should be independent)", qB.ActivePR, qB.Waiting)
	}

	_ = logger
}

func TestPRQueue_ReleaseOnClose(t *testing.T) {
	ns, proj := "default", "closeq"
	_, hs, prStore, ec := setupQueueTest(t, ns, proj)

	pr1 := createTestPR(t, prStore, ns, proj, 1, "feat-1")
	hs.EnqueuePR(ns, proj, 1)

	createTestPR(t, prStore, ns, proj, 2, "feat-2")
	hs.EnqueuePR(ns, proj, 2)

	// Close PR #1 → PR #2 becomes active
	now := time.Now()
	pr1.State = pr.StateClosed
	pr1.ClosedAt = &now
	pr1.UpdatedAt = now
	_ = prStore.Update(pr1)

	releasePRSlotAndDequeue(ec, ns, proj, 1)
	time.Sleep(50 * time.Millisecond)

	q, _ := hs.GetPRQueue(ns, proj)
	if q.ActivePR != 2 {
		t.Errorf("after close #1: active = %d, want 2", q.ActivePR)
	}
	if len(q.Waiting) != 0 {
		t.Errorf("after close #1: waiting = %v, want []", q.Waiting)
	}
}

func TestPRQueue_ReleaseOnInterrupt(t *testing.T) {
	ns, proj := "default", "intq"
	_, hs, prStore, ec := setupQueueTest(t, ns, proj)

	pr1 := createTestPR(t, prStore, ns, proj, 1, "feat-1")
	hs.EnqueuePR(ns, proj, 1)

	createTestPR(t, prStore, ns, proj, 2, "feat-2")
	hs.EnqueuePR(ns, proj, 2)

	// Interrupt PR #1 → PR #2 becomes active
	markInterrupted(prStore, pr1, "agent_failed", "exit code 1", "test-agent", "reviewer", ec.logger)

	releasePRSlotAndDequeue(ec, ns, proj, 1)
	time.Sleep(50 * time.Millisecond)

	q, _ := hs.GetPRQueue(ns, proj)
	if q.ActivePR != 2 {
		t.Errorf("after interrupt #1: active = %d, want 2", q.ActivePR)
	}
	if len(q.Waiting) != 0 {
		t.Errorf("after interrupt #1: waiting = %v, want []", q.Waiting)
	}

	// Verify PR #1 is in interrupted state
	p, _ := prStore.Get(1)
	if p.State != pr.StateInterrupted {
		t.Errorf("PR #1 state = %q, want interrupted", p.State)
	}
}

func TestPRQueue_ReviewIncomplete(t *testing.T) {
	ns, proj := "default", "reviewinc"
	_, hs, prStore, ec := setupQueueTest(t, ns, proj)

	pr1 := createTestPR(t, prStore, ns, proj, 1, "feat-1")
	hs.EnqueuePR(ns, proj, 1)

	createTestPR(t, prStore, ns, proj, 2, "feat-2")
	hs.EnqueuePR(ns, proj, 2)

	// Simulate reviewer exiting 0 without calling approve/reject:
	// PR is still open → detect and mark interrupted with review_incomplete
	current, err := prStore.Get(pr1.Number)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != pr.StateOpen {
		t.Fatalf("PR #1 state = %q, want open", current.State)
	}

	markInterrupted(prStore, current, "review_incomplete",
		"agent exited successfully but did not approve or reject",
		"test-agent", "reviewer", ec.logger)
	releasePRSlotAndDequeue(ec, ns, proj, 1)
	time.Sleep(50 * time.Millisecond)

	// Queue should advance
	q, _ := hs.GetPRQueue(ns, proj)
	if q.ActivePR != 2 {
		t.Errorf("after review_incomplete #1: active = %d, want 2", q.ActivePR)
	}
	if len(q.Waiting) != 0 {
		t.Errorf("after review_incomplete #1: waiting = %v, want []", q.Waiting)
	}

	// PR #1 should be interrupted with review_incomplete reason
	p, _ := prStore.Get(1)
	if p.State != pr.StateInterrupted {
		t.Errorf("PR #1 state = %q, want interrupted", p.State)
	}
	if p.InterruptInfo == nil {
		t.Fatal("PR #1 interrupt_info is nil")
	}
	if p.InterruptInfo.Reason != "review_incomplete" {
		t.Errorf("PR #1 interrupt reason = %q, want review_incomplete", p.InterruptInfo.Reason)
	}
	if p.PreviousState != pr.StateOpen {
		t.Errorf("PR #1 previous_state = %q, want open", p.PreviousState)
	}
}
