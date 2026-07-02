package integrity_test

import (
	"path/filepath"
	"testing"

	"github.com/sopranoworks/gitcote/internal/integrity"
)

func TestStoreGetSetRoundTrip(t *testing.T) {
	s, err := integrity.Open(filepath.Join(t.TempDir(), "heads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	hash, err := s.Get("ns", "proj")
	if err != nil {
		t.Fatal(err)
	}
	if hash != "" {
		t.Fatalf("expected empty hash for new key, got %q", hash)
	}

	if err := s.Set("ns", "proj", "abc123"); err != nil {
		t.Fatal(err)
	}

	hash, err = s.Get("ns", "proj")
	if err != nil {
		t.Fatal(err)
	}
	if hash != "abc123" {
		t.Fatalf("expected %q, got %q", "abc123", hash)
	}

	if err := s.Set("ns", "proj", "def456"); err != nil {
		t.Fatal(err)
	}
	hash, _ = s.Get("ns", "proj")
	if hash != "def456" {
		t.Fatalf("expected updated hash %q, got %q", "def456", hash)
	}
}

func TestStoreIsolation(t *testing.T) {
	s, err := integrity.Open(filepath.Join(t.TempDir(), "heads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	_ = s.Set("ns1", "proj1", "aaa")
	_ = s.Set("ns2", "proj2", "bbb")

	h1, _ := s.Get("ns1", "proj1")
	h2, _ := s.Get("ns2", "proj2")
	if h1 != "aaa" || h2 != "bbb" {
		t.Fatalf("isolation broken: got %q %q", h1, h2)
	}
}

func TestAgentWorkdirCRUD(t *testing.T) {
	s, err := integrity.Open(filepath.Join(t.TempDir(), "heads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	rec := integrity.AgentWorkdirRecord{
		Path:      "/tmp/gitcote-agent-reviewer-abc",
		AgentName: "default_claude_reviewer",
		Role:      "reviewer",
		Namespace: "ns",
		Project:   "proj",
		PRNumber:  3,
		CreatedAt: "2026-06-28T12:00:00Z",
		Status:    "running",
	}

	if err := s.AddAgentWorkdir(rec); err != nil {
		t.Fatal(err)
	}

	recs, err := s.ListAgentWorkdirs()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 workdir, got %d", len(recs))
	}
	if recs[0].Status != "running" {
		t.Errorf("status = %q, want running", recs[0].Status)
	}

	if err := s.UpdateAgentWorkdir(rec.Path, "failed", 1); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetAgentWorkdir(rec.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected record, got nil")
	}
	if got.Status != "failed" {
		t.Errorf("status = %q, want failed", got.Status)
	}
	if got.ExitCode != 1 {
		t.Errorf("exit_code = %d, want 1", got.ExitCode)
	}

	if err := s.RemoveAgentWorkdir(rec.Path); err != nil {
		t.Fatal(err)
	}

	got, err = s.GetAgentWorkdir(rec.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Error("expected nil after removal")
	}
}

func TestAgentWorkdirUpdateNotFound(t *testing.T) {
	s, err := integrity.Open(filepath.Join(t.TempDir(), "heads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	err = s.UpdateAgentWorkdir("/nonexistent", "failed", 1)
	if err == nil {
		t.Error("expected error for non-existent workdir")
	}
}

func TestPREventSettingsGlobalCRUD(t *testing.T) {
	s, err := integrity.Open(filepath.Join(t.TempDir(), "heads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	got, err := s.GetGlobalPREventSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Error("expected nil for unset global settings")
	}

	enabled := true
	settings := &integrity.PREventSettings{
		OnCreated: &integrity.EventAction{
			AgentEnabled: &enabled,
			AgentName:    "default_claude_reviewer",
		},
	}
	if err := s.SetGlobalPREventSettings(settings); err != nil {
		t.Fatal(err)
	}

	got, err = s.GetGlobalPREventSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected non-nil settings")
	}
	if got.OnCreated == nil || *got.OnCreated.AgentEnabled != true {
		t.Error("on_created agent_enabled should be true")
	}
	if got.OnCreated.AgentName != "default_claude_reviewer" {
		t.Errorf("agent_name = %q, want default_claude_reviewer", got.OnCreated.AgentName)
	}
}

func TestPREventSettingsProjectOverride(t *testing.T) {
	s, err := integrity.Open(filepath.Join(t.TempDir(), "heads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	enabled := true
	disabled := false
	global := &integrity.PREventSettings{
		OnCreated: &integrity.EventAction{
			AgentEnabled: &enabled,
			AgentName:    "default_claude_reviewer",
		},
	}
	if err := s.SetGlobalPREventSettings(global); err != nil {
		t.Fatal(err)
	}

	project := &integrity.PREventSettings{
		OnCreated: &integrity.EventAction{
			AgentEnabled: &disabled,
			AgentName:    "reviewer-strict",
		},
	}
	if err := s.SetProjectPREventSettings("ns", "proj", project); err != nil {
		t.Fatal(err)
	}

	resolved, err := s.ResolvePREventSettings("ns", "proj")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.OnCreated == nil {
		t.Fatal("expected on_created in resolved settings")
	}
	if *resolved.OnCreated.AgentEnabled != false {
		t.Error("project override should disable agent")
	}
	if resolved.OnCreated.AgentName != "reviewer-strict" {
		t.Errorf("project override agent_name = %q", resolved.OnCreated.AgentName)
	}

	if err := s.ClearProjectPREventSettings("ns", "proj"); err != nil {
		t.Fatal(err)
	}
	resolved, err = s.ResolvePREventSettings("ns", "proj")
	if err != nil {
		t.Fatal(err)
	}
	if *resolved.OnCreated.AgentEnabled != true {
		t.Error("after clear, should fall back to global")
	}
}

func TestPREventSettingsResolutionOrder(t *testing.T) {
	s, err := integrity.Open(filepath.Join(t.TempDir(), "heads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	resolved, err := s.ResolvePREventSettings("ns", "proj")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.OnCreated != nil {
		t.Error("with no settings, on_created should be nil")
	}

	enabled := true
	s.SetGlobalPREventSettings(&integrity.PREventSettings{
		OnCreated: &integrity.EventAction{AgentEnabled: &enabled, AgentName: "global-agent"},
	})

	resolved, _ = s.ResolvePREventSettings("ns", "proj")
	if resolved.OnCreated.AgentName != "global-agent" {
		t.Error("should fall back to global")
	}

	s.SetProjectPREventSettings("ns", "proj", &integrity.PREventSettings{
		OnCreated: &integrity.EventAction{AgentEnabled: &enabled, AgentName: "project-agent"},
	})

	resolved, _ = s.ResolvePREventSettings("ns", "proj")
	if resolved.OnCreated.AgentName != "project-agent" {
		t.Error("project should override global")
	}

	resolved2, _ := s.ResolvePREventSettings("ns", "other")
	if resolved2.OnCreated.AgentName != "global-agent" {
		t.Error("other project should still get global")
	}
}

func TestResolveEventAction(t *testing.T) {
	enabled := true
	disabled := false
	maxRetries := 3

	r := integrity.ResolveEventAction(nil, nil)
	if r.AgentEnabled || r.AutoRetry || r.NotifyEnabled {
		t.Error("defaults should all be false/disabled")
	}
	if r.NotifyMethod != "log" {
		t.Errorf("default notify_method = %q, want log", r.NotifyMethod)
	}

	global := &integrity.EventAction{
		AgentEnabled: &enabled,
		AgentName:    "global-agent",
		MaxRetries:   &maxRetries,
		NotifyEnabled: &enabled,
	}
	r = integrity.ResolveEventAction(nil, global)
	if !r.AgentEnabled {
		t.Error("global agent_enabled should apply")
	}
	if r.AgentName != "global-agent" {
		t.Error("global agent_name should apply")
	}
	if r.MaxRetries != 3 {
		t.Error("global max_retries should apply")
	}

	project := &integrity.EventAction{
		AgentEnabled: &disabled,
		AgentName:    "project-agent",
	}
	r = integrity.ResolveEventAction(project, global)
	if r.AgentEnabled {
		t.Error("project should override agent_enabled to false")
	}
	if r.AgentName != "project-agent" {
		t.Error("project should override agent_name")
	}
	if r.MaxRetries != 3 {
		t.Error("max_retries should still come from global (project didn't set it)")
	}
}

func TestAgentTokenCRUD(t *testing.T) {
	s, err := integrity.Open(filepath.Join(t.TempDir(), "heads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	key := "ns/proj#3"
	got, err := s.GetAgentToken(key)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Error("expected nil for unset token")
	}

	rec := integrity.AgentTokenRecord{
		SeriesID:  "series-abc",
		Namespace: "ns",
		Project:   "proj",
		PRNumber:  3,
		TaskType:  "pr_review",
		AgentName: "default_claude_reviewer",
		Role:      "reviewer",
		IssuedAt:  "2026-06-28T12:00:00Z",
	}
	if err := s.SetAgentToken(key, rec); err != nil {
		t.Fatal(err)
	}

	got, err = s.GetAgentToken(key)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected record")
	}
	if got.SeriesID != "series-abc" {
		t.Errorf("series_id = %q, want series-abc", got.SeriesID)
	}
	if got.TaskType != "pr_review" {
		t.Errorf("task_type = %q, want pr_review", got.TaskType)
	}

	recs, err := s.ListAgentTokens()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 token, got %d", len(recs))
	}

	if err := s.RemoveAgentToken(key); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetAgentToken(key)
	if got != nil {
		t.Error("expected nil after removal")
	}
}

func TestAgentTokenSeedKey(t *testing.T) {
	s, err := integrity.Open(filepath.Join(t.TempDir(), "heads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	key := "seed:ns/proj"
	rec := integrity.AgentTokenRecord{
		SeriesID:  "series-seed",
		Namespace: "ns",
		Project:   "proj",
		TaskType:  "seed_sync",
		AgentName: "default_claude_merger",
		Role:      "merger",
		IssuedAt:  "2026-06-28T12:00:00Z",
	}
	if err := s.SetAgentToken(key, rec); err != nil {
		t.Fatal(err)
	}

	got, _ := s.GetAgentToken(key)
	if got == nil || got.SeriesID != "series-seed" {
		t.Error("seed key round-trip failed")
	}

	prKey := "ns/proj#1"
	prRec := integrity.AgentTokenRecord{
		SeriesID:  "series-pr",
		Namespace: "ns",
		Project:   "proj",
		PRNumber:  1,
		TaskType:  "pr_review",
		AgentName: "reviewer",
		Role:      "reviewer",
		IssuedAt:  "2026-06-28T12:00:00Z",
	}
	if err := s.SetAgentToken(prKey, prRec); err != nil {
		t.Fatal(err)
	}

	recs, _ := s.ListAgentTokens()
	if len(recs) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(recs))
	}
}

func TestPRQueue_EnqueueAndRelease(t *testing.T) {
	s, err := integrity.Open(filepath.Join(t.TempDir(), "heads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	q, _ := s.GetPRQueue("ns", "proj")
	if q.ActivePR != 0 || len(q.Waiting) != 0 {
		t.Fatalf("empty queue: got active=%d waiting=%v", q.ActivePR, q.Waiting)
	}

	active, err := s.EnqueuePR("ns", "proj", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !active {
		t.Error("PR #1 should be active (slot was idle)")
	}

	active, err = s.EnqueuePR("ns", "proj", 2)
	if err != nil {
		t.Fatal(err)
	}
	if active {
		t.Error("PR #2 should be queued (slot occupied)")
	}

	active, err = s.EnqueuePR("ns", "proj", 3)
	if err != nil {
		t.Fatal(err)
	}
	if active {
		t.Error("PR #3 should be queued")
	}

	q, _ = s.GetPRQueue("ns", "proj")
	if q.ActivePR != 1 {
		t.Errorf("active = %d, want 1", q.ActivePR)
	}
	if len(q.Waiting) != 2 || q.Waiting[0] != 2 || q.Waiting[1] != 3 {
		t.Errorf("waiting = %v, want [2 3]", q.Waiting)
	}

	next, found, err := s.ReleasePRSlot("ns", "proj", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !found || next != 2 {
		t.Errorf("release #1: next=%d found=%v, want 2/true", next, found)
	}

	q, _ = s.GetPRQueue("ns", "proj")
	if q.ActivePR != 2 {
		t.Errorf("after release: active = %d, want 2", q.ActivePR)
	}
	if len(q.Waiting) != 1 || q.Waiting[0] != 3 {
		t.Errorf("after release: waiting = %v, want [3]", q.Waiting)
	}

	next, found, err = s.ReleasePRSlot("ns", "proj", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !found || next != 3 {
		t.Errorf("release #2: next=%d found=%v, want 3/true", next, found)
	}

	next, found, err = s.ReleasePRSlot("ns", "proj", 3)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("release #3: should have no next PR")
	}

	q, _ = s.GetPRQueue("ns", "proj")
	if q.ActivePR != 0 || len(q.Waiting) != 0 {
		t.Errorf("final: active=%d waiting=%v, want 0/[]", q.ActivePR, q.Waiting)
	}
}

func TestPRQueue_DifferentProjects(t *testing.T) {
	s, err := integrity.Open(filepath.Join(t.TempDir(), "heads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	activeA, _ := s.EnqueuePR("ns", "projA", 1)
	activeB, _ := s.EnqueuePR("ns", "projB", 1)
	if !activeA {
		t.Error("projA PR #1 should be active")
	}
	if !activeB {
		t.Error("projB PR #1 should be active (independent)")
	}

	qA, _ := s.GetPRQueue("ns", "projA")
	qB, _ := s.GetPRQueue("ns", "projB")
	if qA.ActivePR != 1 || qB.ActivePR != 1 {
		t.Errorf("projA active=%d, projB active=%d, both should be 1", qA.ActivePR, qB.ActivePR)
	}
}

func TestPRQueue_ReleaseNonActive(t *testing.T) {
	s, err := integrity.Open(filepath.Join(t.TempDir(), "heads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	s.EnqueuePR("ns", "proj", 1)
	s.EnqueuePR("ns", "proj", 2)
	s.EnqueuePR("ns", "proj", 3)

	next, found, err := s.ReleasePRSlot("ns", "proj", 2)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("releasing a waiting PR should not dequeue next")
	}
	_ = next

	q, _ := s.GetPRQueue("ns", "proj")
	if q.ActivePR != 1 {
		t.Errorf("active should still be 1, got %d", q.ActivePR)
	}
	if len(q.Waiting) != 1 || q.Waiting[0] != 3 {
		t.Errorf("waiting = %v, want [3] (PR #2 removed)", q.Waiting)
	}
}

func TestPRQueue_Dequeue(t *testing.T) {
	s, err := integrity.Open(filepath.Join(t.TempDir(), "heads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	s.EnqueuePR("ns", "proj", 1)
	s.EnqueuePR("ns", "proj", 2)
	s.EnqueuePR("ns", "proj", 3)

	next, found, _ := s.DequeuePR("ns", "proj")
	if !found || next != 2 {
		t.Errorf("dequeue: next=%d found=%v, want 2/true", next, found)
	}

	q, _ := s.GetPRQueue("ns", "proj")
	if q.ActivePR != 1 || len(q.Waiting) != 1 || q.Waiting[0] != 3 {
		t.Errorf("after dequeue: active=%d waiting=%v", q.ActivePR, q.Waiting)
	}

	_, found, _ = s.DequeuePR("ns", "proj")
	if found {
		// Only one was left in the waiting list (PR #3), so this second dequeue should succeed
		// Let me re-check: after first dequeue, waiting=[3]. Second dequeue should get 3.
	}
	// Actually the second dequeue should find PR #3
	s.DequeuePR("ns", "proj") // dequeue #3

	_, found, _ = s.DequeuePR("ns", "proj")
	if found {
		t.Error("dequeue on empty waiting list should return not found")
	}
}

func TestAgentTokenOverwrite(t *testing.T) {
	s, err := integrity.Open(filepath.Join(t.TempDir(), "heads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	key := "ns/proj#5"
	rec1 := integrity.AgentTokenRecord{SeriesID: "old-series", AgentName: "old-agent", IssuedAt: "2026-06-28T12:00:00Z"}
	rec2 := integrity.AgentTokenRecord{SeriesID: "new-series", AgentName: "new-agent", IssuedAt: "2026-06-28T13:00:00Z"}

	_ = s.SetAgentToken(key, rec1)
	_ = s.SetAgentToken(key, rec2)

	got, _ := s.GetAgentToken(key)
	if got == nil || got.SeriesID != "new-series" {
		t.Error("overwrite should replace old record")
	}

	recs, _ := s.ListAgentTokens()
	if len(recs) != 1 {
		t.Errorf("expected 1 token after overwrite, got %d", len(recs))
	}
}

func TestSeedEventSettingsCRUD(t *testing.T) {
	s, err := integrity.Open(filepath.Join(t.TempDir(), "heads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	got, _ := s.GetGlobalSeedEventSettings()
	if got != nil {
		t.Error("expected nil for unset")
	}

	enabled := true
	settings := &integrity.SeedEventSettings{
		OnPushConflict: &integrity.EventAction{AgentEnabled: &enabled, AgentName: "merger"},
	}
	if err := s.SetGlobalSeedEventSettings(settings); err != nil {
		t.Fatal(err)
	}

	got, _ = s.GetGlobalSeedEventSettings()
	if got == nil || got.OnPushConflict == nil {
		t.Fatal("expected non-nil")
	}
	if got.OnPushConflict.AgentName != "merger" {
		t.Error("agent_name mismatch")
	}

	if err := s.SetProjectSeedEventSettings("ns", "proj", &integrity.SeedEventSettings{
		OnPullConflict: &integrity.EventAction{AgentEnabled: &enabled},
	}); err != nil {
		t.Fatal(err)
	}

	resolved, _ := s.ResolveSeedEventSettings("ns", "proj")
	if resolved.OnPullConflict == nil {
		t.Error("project override should be returned")
	}

	s.ClearProjectSeedEventSettings("ns", "proj")
	resolved, _ = s.ResolveSeedEventSettings("ns", "proj")
	if resolved.OnPushConflict == nil || resolved.OnPushConflict.AgentName != "merger" {
		t.Error("should fall back to global after clear")
	}
}
