package pr_test

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/sopranoworks/gitcote/internal/pr"
)

func TestStoreCreateAndGet(t *testing.T) {
	s := openTestStore(t)

	now := time.Now()
	p := &pr.PullRequest{
		RepoNamespace: "ns",
		RepoProject:   "proj",
		Title:         "Test PR",
		SourceBranch:  "feature",
		TargetBranch:  "main",
		Author:        "test@example.com",
		State:         pr.StateOpen,
		Mergeable:     pr.MergeableClean,
		SourceCommit:  "abc123",
		TargetCommit:  "def456",
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	num, err := s.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if num != 1 {
		t.Errorf("first PR number = %d, want 1", num)
	}

	got, err := s.Get(1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Test PR" {
		t.Errorf("title = %q", got.Title)
	}
	if got.Number != 1 {
		t.Errorf("number = %d", got.Number)
	}
}

func TestStoreSequentialNumbers(t *testing.T) {
	s := openTestStore(t)

	now := time.Now()
	for i := range 3 {
		p := &pr.PullRequest{
			Title:     fmt.Sprintf("PR %d", i+1),
			State:     pr.StateOpen,
			CreatedAt: now,
			UpdatedAt: now,
		}
		num, err := s.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		if num != uint32(i+1) {
			t.Errorf("PR %d number = %d, want %d", i, num, i+1)
		}
	}
}

func TestStoreListByState(t *testing.T) {
	s := openTestStore(t)
	now := time.Now()

	for _, state := range []pr.PRState{pr.StateOpen, pr.StateOpen, pr.StateApproved, pr.StateMerged} {
		p := &pr.PullRequest{State: state, CreatedAt: now, UpdatedAt: now}
		if _, err := s.Create(p); err != nil {
			t.Fatal(err)
		}
	}

	all, _ := s.List("")
	if len(all) != 4 {
		t.Errorf("all = %d, want 4", len(all))
	}
	open, _ := s.List(pr.StateOpen)
	if len(open) != 2 {
		t.Errorf("open = %d, want 2", len(open))
	}
	merged, _ := s.List(pr.StateMerged)
	if len(merged) != 1 {
		t.Errorf("merged = %d, want 1", len(merged))
	}
}

func TestStoreFindByBranches(t *testing.T) {
	s := openTestStore(t)
	now := time.Now()

	p := &pr.PullRequest{
		SourceBranch: "feature",
		TargetBranch: "main",
		State:        pr.StateOpen,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	s.Create(p)

	found, err := s.FindByBranches("feature", "main")
	if err != nil {
		t.Fatal(err)
	}
	if found == nil {
		t.Fatal("expected to find PR")
	}

	notFound, _ := s.FindByBranches("other", "main")
	if notFound != nil {
		t.Error("should not find PR for other→main")
	}
}

func TestStoreUpdate(t *testing.T) {
	s := openTestStore(t)
	now := time.Now()
	p := &pr.PullRequest{
		Title:     "Original",
		State:     pr.StateOpen,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.Create(p)

	got, _ := s.Get(1)
	got.State = pr.StateApproved
	got.ApprovedBy = "reviewer@example.com"
	approvedAt := time.Now()
	got.ApprovedAt = &approvedAt
	if err := s.Update(got); err != nil {
		t.Fatal(err)
	}

	got2, _ := s.Get(1)
	if got2.State != pr.StateApproved {
		t.Errorf("state = %q, want approved", got2.State)
	}
	if got2.ApprovedBy != "reviewer@example.com" {
		t.Errorf("approved_by = %q", got2.ApprovedBy)
	}
}

func TestStoreInterruptedState(t *testing.T) {
	s := openTestStore(t)
	now := time.Now()

	p := &pr.PullRequest{
		Title:        "Test PR",
		SourceBranch: "feature",
		TargetBranch: "main",
		State:        pr.StateOpen,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	s.Create(p)

	got, _ := s.Get(1)
	got.PreviousState = got.State
	got.State = pr.StateInterrupted
	got.InterruptInfo = &pr.InterruptInfo{
		Reason:    "agent_spawn_failed",
		Detail:    "exec: claude: not found",
		AgentName: "default_claude_reviewer",
		AgentRole: "reviewer",
		At:        now,
	}
	got.UpdatedAt = time.Now()
	if err := s.Update(got); err != nil {
		t.Fatal(err)
	}

	got2, _ := s.Get(1)
	if got2.State != pr.StateInterrupted {
		t.Errorf("state = %q, want interrupted", got2.State)
	}
	if got2.PreviousState != pr.StateOpen {
		t.Errorf("previous_state = %q, want open", got2.PreviousState)
	}
	if got2.InterruptInfo == nil {
		t.Fatal("expected interrupt_info")
	}
	if got2.InterruptInfo.Reason != "agent_spawn_failed" {
		t.Errorf("reason = %q", got2.InterruptInfo.Reason)
	}
	if got2.InterruptInfo.AgentName != "default_claude_reviewer" {
		t.Errorf("agent_name = %q", got2.InterruptInfo.AgentName)
	}

	got2.State = got2.PreviousState
	got2.PreviousState = ""
	got2.InterruptInfo = nil
	got2.UpdatedAt = time.Now()
	_ = s.Update(got2)

	got3, _ := s.Get(1)
	if got3.State != pr.StateOpen {
		t.Errorf("after dismiss, state = %q, want open", got3.State)
	}
	if got3.InterruptInfo != nil {
		t.Error("interrupt_info should be nil after dismiss")
	}
}

func TestStoreNewStates(t *testing.T) {
	s := openTestStore(t)
	now := time.Now()

	for _, state := range []pr.PRState{pr.StateOpen, pr.StateRejected, pr.StateMergeConflict, pr.StateInterrupted} {
		p := &pr.PullRequest{State: state, CreatedAt: now, UpdatedAt: now}
		if _, err := s.Create(p); err != nil {
			t.Fatal(err)
		}
	}

	all, _ := s.List("")
	if len(all) != 4 {
		t.Errorf("all = %d, want 4", len(all))
	}

	rejected, _ := s.List(pr.StateRejected)
	if len(rejected) != 1 {
		t.Errorf("rejected = %d, want 1", len(rejected))
	}

	mergeConflict, _ := s.List(pr.StateMergeConflict)
	if len(mergeConflict) != 1 {
		t.Errorf("merge_conflict = %d, want 1", len(mergeConflict))
	}
}

func TestStoreFindByBranchesIncludesNewStates(t *testing.T) {
	s := openTestStore(t)
	now := time.Now()

	p := &pr.PullRequest{
		SourceBranch: "feature",
		TargetBranch: "main",
		State:        pr.StateRejected,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	s.Create(p)

	found, _ := s.FindByBranches("feature", "main")
	if found == nil {
		t.Error("rejected PR should be found by FindByBranches")
	}

	p2 := &pr.PullRequest{
		SourceBranch: "hotfix",
		TargetBranch: "main",
		State:        pr.StateMergeConflict,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	s.Create(p2)

	found2, _ := s.FindByBranches("hotfix", "main")
	if found2 == nil {
		t.Error("merge_conflict PR should be found by FindByBranches")
	}
}

func TestStoreRejectionReason(t *testing.T) {
	s := openTestStore(t)
	now := time.Now()

	p := &pr.PullRequest{
		Title:        "Test PR",
		SourceBranch: "feature",
		TargetBranch: "main",
		State:        pr.StateOpen,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	s.Create(p)

	got, _ := s.Get(1)
	got.State = pr.StateRejected
	got.RejectionReason = "Code style issues: missing error handling in auth middleware"
	got.UpdatedAt = time.Now()
	if err := s.Update(got); err != nil {
		t.Fatal(err)
	}

	got2, _ := s.Get(1)
	if got2.State != pr.StateRejected {
		t.Errorf("state = %q, want rejected", got2.State)
	}
	if got2.RejectionReason != "Code style issues: missing error handling in auth middleware" {
		t.Errorf("rejection_reason = %q", got2.RejectionReason)
	}
}

func TestStoreRejectionReasonEmpty(t *testing.T) {
	s := openTestStore(t)
	now := time.Now()

	p := &pr.PullRequest{
		Title:        "Test PR",
		SourceBranch: "feature",
		TargetBranch: "main",
		State:        pr.StateOpen,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	s.Create(p)

	got, _ := s.Get(1)
	got.State = pr.StateRejected
	got.UpdatedAt = time.Now()
	if err := s.Update(got); err != nil {
		t.Fatal(err)
	}

	got2, _ := s.Get(1)
	if got2.State != pr.StateRejected {
		t.Errorf("state = %q, want rejected", got2.State)
	}
	if got2.RejectionReason != "" {
		t.Errorf("rejection_reason should be empty, got %q", got2.RejectionReason)
	}
}

func openTestStore(t *testing.T) *pr.Store {
	t.Helper()
	s, err := pr.Open(filepath.Join(t.TempDir(), "prs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
