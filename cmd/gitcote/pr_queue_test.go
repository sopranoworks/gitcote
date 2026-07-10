package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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

func TestPRQueue_InterruptRetainsSlot(t *testing.T) {
	ns, proj := "default", "intq"
	_, hs, prStore, ec := setupQueueTest(t, ns, proj)

	pr1 := createTestPR(t, prStore, ns, proj, 1, "feat-1")
	hs.EnqueuePR(ns, proj, 1)

	createTestPR(t, prStore, ns, proj, 2, "feat-2")
	hs.EnqueuePR(ns, proj, 2)

	// Interrupt PR #1 — slot should be RETAINED
	markInterrupted(prStore, pr1, "agent_failed", "exit code 1", "test-agent", "reviewer", ec.logger)

	q, _ := hs.GetPRQueue(ns, proj)
	if q.ActivePR != 1 {
		t.Errorf("after interrupt: active = %d, want 1 (slot retained)", q.ActivePR)
	}
	if len(q.Waiting) != 1 || q.Waiting[0] != 2 {
		t.Errorf("after interrupt: waiting = %v, want [2]", q.Waiting)
	}

	p, _ := prStore.Get(1)
	if p.State != pr.StateInterrupted {
		t.Errorf("PR #1 state = %q, want interrupted", p.State)
	}

	// Dismiss releases the slot → PR #2 becomes active
	p.State = p.PreviousState
	p.PreviousState = ""
	p.InterruptInfo = nil
	p.UpdatedAt = time.Now()
	_ = prStore.Update(p)
	releasePRSlotAndDequeue(ec, ns, proj, 1)
	time.Sleep(50 * time.Millisecond)

	q, _ = hs.GetPRQueue(ns, proj)
	if q.ActivePR != 2 {
		t.Errorf("after dismiss: active = %d, want 2", q.ActivePR)
	}
}

func TestPRQueue_ReviewIncompleteRetainsSlot(t *testing.T) {
	ns, proj := "default", "reviewinc"
	_, hs, prStore, _ := setupQueueTest(t, ns, proj)

	pr1 := createTestPR(t, prStore, ns, proj, 1, "feat-1")
	hs.EnqueuePR(ns, proj, 1)

	createTestPR(t, prStore, ns, proj, 2, "feat-2")
	hs.EnqueuePR(ns, proj, 2)

	current, _ := prStore.Get(pr1.Number)
	markInterrupted(prStore, current, "review_incomplete",
		"agent exited successfully but did not approve or reject",
		"test-agent", "reviewer", slog.Default())

	// Slot should be retained — PR #2 must NOT become active
	q, _ := hs.GetPRQueue(ns, proj)
	if q.ActivePR != 1 {
		t.Errorf("after review_incomplete: active = %d, want 1 (slot retained)", q.ActivePR)
	}
	if len(q.Waiting) != 1 || q.Waiting[0] != 2 {
		t.Errorf("after review_incomplete: waiting = %v, want [2]", q.Waiting)
	}

	p, _ := prStore.Get(1)
	if p.State != pr.StateInterrupted {
		t.Errorf("PR #1 state = %q, want interrupted", p.State)
	}
	if p.InterruptInfo == nil || p.InterruptInfo.Reason != "review_incomplete" {
		t.Errorf("PR #1 interrupt reason = %v, want review_incomplete", p.InterruptInfo)
	}
}

func TestPRQueue_SlotRetainedThroughRetryCycle(t *testing.T) {
	ns, proj := "default", "retrycycle"
	_, hs, prStore, ec := setupQueueTest(t, ns, proj)

	pr1 := createTestPR(t, prStore, ns, proj, 1, "feat-1")
	hs.EnqueuePR(ns, proj, 1)

	createTestPR(t, prStore, ns, proj, 2, "feat-2")
	hs.EnqueuePR(ns, proj, 2)

	for cycle := 1; cycle <= 3; cycle++ {
		// Interrupt
		current, _ := prStore.Get(pr1.Number)
		markInterrupted(prStore, current, "review_incomplete",
			"agent exited without verdict", "test-agent", "reviewer", ec.logger)

		q, _ := hs.GetPRQueue(ns, proj)
		if q.ActivePR != 1 {
			t.Fatalf("cycle %d after interrupt: active = %d, want 1", cycle, q.ActivePR)
		}

		// Retry (restore state, agent would re-spawn in same slot)
		p, _ := prStore.Get(1)
		p.State = p.PreviousState
		p.PreviousState = ""
		p.InterruptInfo = nil
		p.UpdatedAt = time.Now()
		_ = prStore.Update(p)

		q, _ = hs.GetPRQueue(ns, proj)
		if q.ActivePR != 1 {
			t.Fatalf("cycle %d after retry: active = %d, want 1", cycle, q.ActivePR)
		}
	}

	// Terminal outcome (approve+merge) releases the slot
	p, _ := prStore.Get(1)
	now := time.Now()
	p.State = pr.StateMerged
	p.MergedAt = &now
	p.UpdatedAt = now
	_ = prStore.Update(p)

	releasePRSlotAndDequeue(ec, ns, proj, 1)
	time.Sleep(50 * time.Millisecond)

	q, _ := hs.GetPRQueue(ns, proj)
	if q.ActivePR != 2 {
		t.Errorf("after merge: active = %d, want 2", q.ActivePR)
	}
}

func TestPRQueue_DismissReleasesSlot(t *testing.T) {
	ns, proj := "default", "dismissq"
	_, hs, prStore, ec := setupQueueTest(t, ns, proj)

	pr1 := createTestPR(t, prStore, ns, proj, 1, "feat-1")
	hs.EnqueuePR(ns, proj, 1)

	createTestPR(t, prStore, ns, proj, 2, "feat-2")
	hs.EnqueuePR(ns, proj, 2)

	// Interrupt retains slot
	markInterrupted(prStore, pr1, "agent_failed", "exit code 1", "test-agent", "reviewer", ec.logger)

	q, _ := hs.GetPRQueue(ns, proj)
	if q.ActivePR != 1 {
		t.Fatalf("after interrupt: active = %d, want 1", q.ActivePR)
	}

	// Dismiss: restore state + release slot
	p, _ := prStore.Get(1)
	p.State = p.PreviousState
	p.PreviousState = ""
	p.InterruptInfo = nil
	p.UpdatedAt = time.Now()
	_ = prStore.Update(p)
	releasePRSlotAndDequeue(ec, ns, proj, 1)
	time.Sleep(50 * time.Millisecond)

	q, _ = hs.GetPRQueue(ns, proj)
	if q.ActivePR != 2 {
		t.Errorf("after dismiss: active = %d, want 2", q.ActivePR)
	}
	if len(q.Waiting) != 0 {
		t.Errorf("after dismiss: waiting = %v, want []", q.Waiting)
	}

	dismissed, _ := prStore.Get(1)
	if dismissed.State != pr.StateOpen {
		t.Errorf("dismissed PR state = %q, want open", dismissed.State)
	}
}

func TestPRQueue_MergerInterruptRetainsSlot(t *testing.T) {
	ns, proj := "default", "mergerint"
	_, hs, prStore, _ := setupQueueTest(t, ns, proj)

	pr1 := createTestPR(t, prStore, ns, proj, 1, "feat-1")
	pr1.State = pr.StateMergeConflict
	pr1.Mergeable = pr.MergeableConflict
	pr1.UpdatedAt = time.Now()
	if err := prStore.Update(pr1); err != nil {
		t.Fatal(err)
	}
	hs.EnqueuePR(ns, proj, 1)

	createTestPR(t, prStore, ns, proj, 2, "feat-2")
	hs.EnqueuePR(ns, proj, 2)

	current, _ := prStore.Get(pr1.Number)
	markInterrupted(prStore, current, "merge_still_conflicting",
		"merger agent succeeded but conflicts persist",
		"test-merger", "merger", slog.Default())

	q, _ := hs.GetPRQueue(ns, proj)
	if q.ActivePR != 1 {
		t.Errorf("after merge_still_conflicting: active = %d, want 1 (slot retained)", q.ActivePR)
	}
	if len(q.Waiting) != 1 || q.Waiting[0] != 2 {
		t.Errorf("after merge_still_conflicting: waiting = %v, want [2]", q.Waiting)
	}

	p, _ := prStore.Get(1)
	if p.State != pr.StateInterrupted {
		t.Errorf("PR #1 state = %q, want interrupted", p.State)
	}
	if p.PreviousState != pr.StateMergeConflict {
		t.Errorf("PR #1 previous_state = %q, want merge_conflict", p.PreviousState)
	}
	if p.InterruptInfo == nil || p.InterruptInfo.Reason != "merge_still_conflicting" {
		t.Errorf("PR #1 interrupt reason = %v, want merge_still_conflicting", p.InterruptInfo)
	}
	if p.InterruptInfo.AgentRole != "merger" {
		t.Errorf("PR #1 interrupt agent_role = %q, want merger", p.InterruptInfo.AgentRole)
	}
}

func TestPRQueue_MergeIncompleteRetainsSlot(t *testing.T) {
	ns, proj := "default", "mergeinc"
	_, hs, prStore, _ := setupQueueTest(t, ns, proj)

	pr1 := createTestPR(t, prStore, ns, proj, 1, "feat-1")
	pr1.State = pr.StateMergeConflict
	pr1.UpdatedAt = time.Now()
	if err := prStore.Update(pr1); err != nil {
		t.Fatal(err)
	}
	hs.EnqueuePR(ns, proj, 1)

	createTestPR(t, prStore, ns, proj, 2, "feat-2")
	hs.EnqueuePR(ns, proj, 2)

	current, _ := prStore.Get(pr1.Number)
	markInterrupted(prStore, current, "merge_incomplete",
		"open repo: repository not found",
		"test-merger", "merger", slog.Default())

	q, _ := hs.GetPRQueue(ns, proj)
	if q.ActivePR != 1 {
		t.Errorf("after merge_incomplete: active = %d, want 1 (slot retained)", q.ActivePR)
	}
	if len(q.Waiting) != 1 || q.Waiting[0] != 2 {
		t.Errorf("after merge_incomplete: waiting = %v, want [2]", q.Waiting)
	}

	p, _ := prStore.Get(1)
	if p.State != pr.StateInterrupted {
		t.Errorf("PR #1 state = %q, want interrupted", p.State)
	}
	if p.InterruptInfo == nil || p.InterruptInfo.Reason != "merge_incomplete" {
		t.Errorf("PR #1 interrupt reason = %v, want merge_incomplete", p.InterruptInfo)
	}
}

func TestPRQueue_MergerSlotRetainedThroughRetryCycle(t *testing.T) {
	ns, proj := "default", "mergerretry"
	_, hs, prStore, ec := setupQueueTest(t, ns, proj)

	pr1 := createTestPR(t, prStore, ns, proj, 1, "feat-1")
	pr1.State = pr.StateMergeConflict
	pr1.UpdatedAt = time.Now()
	if err := prStore.Update(pr1); err != nil {
		t.Fatal(err)
	}
	hs.EnqueuePR(ns, proj, 1)

	createTestPR(t, prStore, ns, proj, 2, "feat-2")
	hs.EnqueuePR(ns, proj, 2)

	for cycle := 1; cycle <= 3; cycle++ {
		current, _ := prStore.Get(pr1.Number)
		markInterrupted(prStore, current, "merge_still_conflicting",
			"merger agent succeeded but conflicts persist",
			"test-merger", "merger", ec.logger)

		q, _ := hs.GetPRQueue(ns, proj)
		if q.ActivePR != 1 {
			t.Fatalf("cycle %d after interrupt: active = %d, want 1", cycle, q.ActivePR)
		}
		if len(q.Waiting) != 1 || q.Waiting[0] != 2 {
			t.Fatalf("cycle %d after interrupt: waiting = %v, want [2]", cycle, q.Waiting)
		}

		p, _ := prStore.Get(1)
		p.State = p.PreviousState
		p.PreviousState = ""
		p.InterruptInfo = nil
		p.UpdatedAt = time.Now()
		_ = prStore.Update(p)

		q, _ = hs.GetPRQueue(ns, proj)
		if q.ActivePR != 1 {
			t.Fatalf("cycle %d after retry: active = %d, want 1", cycle, q.ActivePR)
		}
	}

	// Terminal outcome (merge succeeds) releases the slot
	p, _ := prStore.Get(1)
	now := time.Now()
	p.State = pr.StateMerged
	p.MergedAt = &now
	p.UpdatedAt = now
	_ = prStore.Update(p)

	releasePRSlotAndDequeue(ec, ns, proj, 1)
	time.Sleep(50 * time.Millisecond)

	q, _ := hs.GetPRQueue(ns, proj)
	if q.ActivePR != 2 {
		t.Errorf("after merge: active = %d, want 2", q.ActivePR)
	}
}

func TestPRQueue_MergerDismissReleasesSlot(t *testing.T) {
	ns, proj := "default", "mergerdismiss"
	_, hs, prStore, ec := setupQueueTest(t, ns, proj)

	pr1 := createTestPR(t, prStore, ns, proj, 1, "feat-1")
	pr1.State = pr.StateMergeConflict
	pr1.UpdatedAt = time.Now()
	if err := prStore.Update(pr1); err != nil {
		t.Fatal(err)
	}
	hs.EnqueuePR(ns, proj, 1)

	createTestPR(t, prStore, ns, proj, 2, "feat-2")
	hs.EnqueuePR(ns, proj, 2)

	markInterrupted(prStore, pr1, "merge_still_conflicting",
		"merger agent succeeded but conflicts persist",
		"test-merger", "merger", ec.logger)

	q, _ := hs.GetPRQueue(ns, proj)
	if q.ActivePR != 1 {
		t.Fatalf("after interrupt: active = %d, want 1", q.ActivePR)
	}

	p, _ := prStore.Get(1)
	p.State = p.PreviousState
	p.PreviousState = ""
	p.InterruptInfo = nil
	p.UpdatedAt = time.Now()
	_ = prStore.Update(p)
	releasePRSlotAndDequeue(ec, ns, proj, 1)
	time.Sleep(50 * time.Millisecond)

	q, _ = hs.GetPRQueue(ns, proj)
	if q.ActivePR != 2 {
		t.Errorf("after dismiss: active = %d, want 2", q.ActivePR)
	}
	if len(q.Waiting) != 0 {
		t.Errorf("after dismiss: waiting = %v, want []", q.Waiting)
	}

	dismissed, _ := prStore.Get(1)
	if dismissed.State != pr.StateMergeConflict {
		t.Errorf("dismissed PR state = %q, want merge_conflict", dismissed.State)
	}
}

func TestNotifyInterrupt_FiresWhenEnabled(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	ec := &eventContext{
		logger:     logger,
		gitcoteURL: "http://localhost:9090",
	}

	p := &pr.PullRequest{
		Number:        1,
		RepoNamespace: "testns",
		RepoProject:   "testproj",
		Title:         "Add feature X",
	}

	notifyInterrupt(ec, "log", p, "review_incomplete",
		"agent exited successfully but did not approve or reject",
		"default_claude_reviewer", "reviewer")

	output := buf.String()
	for _, want := range []string{
		"PR notification",
		"PR reviewer interrupted",
		"review_incomplete",
		"agent exited successfully",
		"default_claude_reviewer",
		"reviewer",
		"testns",
		"testproj",
		"Add feature X",
		"http://localhost:9090/p/testns/testproj/prs?pr=1",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("notification log missing %q\nGot: %s", want, output)
		}
	}
}

func TestNotifyInterrupt_SkippedWhenDisabled(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	ec := &eventContext{logger: logger}

	p := &pr.PullRequest{
		Number:        1,
		RepoNamespace: "ns",
		RepoProject:   "proj",
	}

	action := integrity.ResolvedEventAction{NotifyEnabled: false, NotifyMethod: "log"}

	// Simulate the guard used at each interrupt site
	if action.NotifyEnabled {
		notifyInterrupt(ec, action.NotifyMethod, p, "review_incomplete", "detail", "agent", "reviewer")
	}

	if buf.Len() > 0 {
		t.Errorf("expected no notification when disabled, got: %s", buf.String())
	}
}

func TestNotifyInterrupt_AllReasons(t *testing.T) {
	reasons := []struct {
		reason string
		detail string
	}{
		{"agent_spawn_failed", "exit code 1"},
		{"agent_spawn_failed", "no agent config found for role: reviewer"},
		{"agent_spawn_failed", "scan configs: no such directory"},
		{"review_incomplete", "agent exited successfully but did not approve or reject"},
		{"agent_spawn_failed", "hard_timeout"},
		{"agent_spawn_failed", "no MCP activity for 5m0s"},
	}

	for _, tc := range reasons {
		t.Run(tc.reason+"_"+tc.detail[:min(20, len(tc.detail))], func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, nil))
			ec := &eventContext{logger: logger}
			p := &pr.PullRequest{
				Number:        42,
				RepoNamespace: "ns",
				RepoProject:   "proj",
				Title:         "Test PR",
			}

			notifyInterrupt(ec, "log", p, tc.reason, tc.detail, "test-agent", "reviewer")

			output := buf.String()
			if !strings.Contains(output, tc.reason) {
				t.Errorf("missing reason %q in: %s", tc.reason, output)
			}
			if !strings.Contains(output, tc.detail) {
				t.Errorf("missing detail %q in: %s", tc.detail, output)
			}
		})
	}
}

func TestNotifyInterrupt_NoLinkWithoutURL(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	ec := &eventContext{logger: logger}
	p := &pr.PullRequest{Number: 1, RepoNamespace: "ns", RepoProject: "proj"}

	notifyInterrupt(ec, "log", p, "review_incomplete", "detail", "agent", "reviewer")

	if strings.Contains(buf.String(), "Link:") {
		t.Error("should not include Link when gitcoteURL is empty")
	}
}

func TestNotifyInterrupt_MergerReasons(t *testing.T) {
	reasons := []struct {
		reason string
		detail string
		role   string
	}{
		{"merge_still_conflicting", "merger agent succeeded but conflicts persist", "merger"},
		{"merge_incomplete", "open repo: repository not found", "merger"},
		{"merge_incomplete", "resolve source branch: reference not found", "merger"},
		{"merge_incomplete", "create merge commit: tree hash mismatch", "merger"},
		{"agent_spawn_failed", "exit code 1", "merger"},
		{"agent_spawn_failed", "no MCP activity for 5m0s", "merger"},
	}

	for _, tc := range reasons {
		t.Run(tc.reason+"_"+tc.detail[:min(20, len(tc.detail))], func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, nil))
			ec := &eventContext{logger: logger, gitcoteURL: "http://localhost:9090"}
			p := &pr.PullRequest{
				Number:        42,
				RepoNamespace: "ns",
				RepoProject:   "proj",
				Title:         "Fix conflicts",
			}

			notifyInterrupt(ec, "log", p, tc.reason, tc.detail, "test-merger", tc.role)

			output := buf.String()
			if !strings.Contains(output, tc.reason) {
				t.Errorf("missing reason %q in: %s", tc.reason, output)
			}
			if !strings.Contains(output, tc.detail) {
				t.Errorf("missing detail %q in: %s", tc.detail, output)
			}
			if !strings.Contains(output, "PR merger interrupted") {
				t.Errorf("missing role-aware header 'PR merger interrupted' in: %s", output)
			}
			if !strings.Contains(output, "http://localhost:9090/p/ns/proj/prs?pr=42") {
				t.Errorf("missing WebUI link in: %s", output)
			}
		})
	}
}
