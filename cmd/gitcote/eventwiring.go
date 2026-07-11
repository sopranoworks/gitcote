package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/sopranoworks/gitcote/internal/agent"
	"github.com/sopranoworks/gitcote/internal/git"
	"github.com/sopranoworks/gitcote/internal/integrity"
	"github.com/sopranoworks/gitcote/internal/pr"
	"github.com/sopranoworks/shoka/pkg/authz"
	"github.com/sopranoworks/shoka/pkg/oauthstore"
	"github.com/sopranoworks/shoka/pkg/uiws"
)

// agentActivity tracks MCP call timestamps per agent token for activity timeout.
var agentActivity sync.Map // token string → time.Time

// RecordAgentActivity records the current time for a given token.
func RecordAgentActivity(token string) {
	agentActivity.Store(token, time.Now())
}

// agentTokens tracks which tokens belong to spawned agents.
var agentTokens sync.Map // token string → bool

// RegisterAgentToken marks a token as belonging to an agent.
func RegisterAgentToken(token string) {
	agentTokens.Store(token, true)
	agentActivity.Store(token, time.Now())
}

// UnregisterAgentToken removes an agent token from tracking.
func UnregisterAgentToken(token string) {
	agentTokens.Delete(token)
	agentActivity.Delete(token)
}

// IsAgentToken returns true if the token belongs to a spawned agent.
func IsAgentToken(token string) bool {
	_, ok := agentTokens.Load(token)
	return ok
}

const agentTokenClientID = "gitcote-agent"

func agentTokenKey(namespace, project string, prNumber int) string {
	if prNumber == 0 {
		return fmt.Sprintf("seed:%s/%s", namespace, project)
	}
	return fmt.Sprintf("%s/%s#%d", namespace, project, prNumber)
}

// prAgentActive guards against concurrent agent-spawn attempts for the same
// PR, mirroring the seedPullActive/seedPushActive reentrancy pattern
// (commit 1f52fe2). Without this, two near-simultaneous retry/review calls
// for the same PR can both pass prRetryEligible's live-token check before
// either has actually registered a token — token issuance happens deep
// inside spawnAgentForPR/executeAgentForPR, well after the eligibility
// check returns, so the check alone is not enough to prevent a double
// spawn. Keyed the same way as agent tokens (agentTokenKey), held from the
// moment a retry/review call passes eligibility until its spawn attempt
// fully completes.
var prAgentActive sync.Map // key: agentTokenKey(ns, proj, prNumber) → bool

// acquirePRAgentLock returns true if the caller now holds the lock for key.
func acquirePRAgentLock(key string) bool {
	_, loaded := prAgentActive.LoadOrStore(key, true)
	return !loaded
}

// releasePRAgentLock releases a lock acquired by acquirePRAgentLock.
func releasePRAgentLock(key string) {
	prAgentActive.Delete(key)
}

func taskTypeForRole(role string) string {
	switch role {
	case "reviewer":
		return "pr_review"
	case "coder":
		return "pr_fix"
	case "merger":
		return "pr_merge"
	default:
		return role
	}
}

func ensureNoActiveToken(ec *eventContext, namespace, project string, prNumber int) {
	if ec.oauthStore == nil || ec.integrityHS == nil {
		return
	}
	key := agentTokenKey(namespace, project, prNumber)
	existing, err := ec.integrityHS.GetAgentToken(key)
	if err != nil || existing == nil {
		return
	}
	if err := ec.oauthStore.Revoke(existing.SeriesID); err != nil {
		ec.logger.Warn("failed to revoke stale agent token", "key", key, "error", err)
	}
	_ = ec.integrityHS.RemoveAgentToken(key)
	ec.logger.Warn("revoked stale agent token",
		"key", key, "old_agent", existing.AgentName, "series", existing.SeriesID)
}

func issueAgentToken(ec *eventContext, namespace, project string, prNumber int, sourceBranch, agentName, role string) (string, error) {
	if ec.oauthStore == nil {
		return "", nil
	}

	var scope string
	var ep map[string]any
	switch role {
	case "reviewer":
		scope = fmt.Sprintf("%s:%s:rw", namespace, project)
	case "coder", "merger":
		scope = fmt.Sprintf("git/%s:%s:rw", namespace, project)
		prefix := sourceBranch + "/"
		ep = map[string]any{"allowed_branches": []any{prefix}}
	default:
		scope = fmt.Sprintf("%s:%s:rw", namespace, project)
	}

	ttl := ec.agentCfg.TimeoutDuration()
	now := time.Now()
	name := fmt.Sprintf("%s-%s", role, agentTokenKey(namespace, project, prNumber))
	rec, err := ec.oauthStore.NewSeries(
		agentTokenClientID,
		oauthstore.Principal{Name: "agent-token", Email: "agent@gitcote.local"},
		"",
		scope,
		name,
		now,
		ttl,
		ttl,
		ep,
	)
	if err != nil {
		return "", fmt.Errorf("issue agent token: %w", err)
	}

	key := agentTokenKey(namespace, project, prNumber)
	tokenRec := integrity.AgentTokenRecord{
		SeriesID:  rec.SeriesID,
		Namespace: namespace,
		Project:   project,
		PRNumber:  prNumber,
		TaskType:  taskTypeForRole(role),
		AgentName: agentName,
		Role:      role,
		IssuedAt:  now.UTC().Format(time.RFC3339),
	}
	if err := ec.integrityHS.SetAgentToken(key, tokenRec); err != nil {
		_ = ec.oauthStore.Revoke(rec.SeriesID)
		return "", fmt.Errorf("store agent token record: %w", err)
	}

	ec.logger.Info("issued agent token",
		"key", key, "agent", agentName, "role", role, "scope", scope, "ttl", ttl)
	return rec.AccessToken, nil
}

func revokeAgentToken(ec *eventContext, namespace, project string, prNumber int, removeRecord bool) {
	if ec.oauthStore == nil || ec.integrityHS == nil {
		return
	}
	key := agentTokenKey(namespace, project, prNumber)
	existing, err := ec.integrityHS.GetAgentToken(key)
	if err != nil || existing == nil {
		return
	}
	if err := ec.oauthStore.Revoke(existing.SeriesID); err != nil {
		ec.logger.Warn("failed to revoke agent token", "key", key, "error", err)
	} else {
		ec.logger.Info("revoked agent token", "key", key, "series", existing.SeriesID)
	}
	if removeRecord {
		_ = ec.integrityHS.RemoveAgentToken(key)
	}
}

// eventContext holds dependencies for event hook processing.
type eventContext struct {
	gitStore    *git.Store
	integrityHS *integrity.Store
	oauthStore  *oauthstore.Store
	agentCfg    AgentSpawnConfig
	gitcoteURL  string
	httpURL     string
	seedCtx     *seedContext
	logger      *slog.Logger
}

func notify(method, message string, namespace, project string, prNumber uint32, logger *slog.Logger) {
	switch method {
	case "log":
		logger.Info("PR notification", "message", message, "namespace", namespace, "project", project, "pr", prNumber)
	default:
		logger.Info("PR notification (method not implemented)", "method", method, "message", message)
	}
}

func notifyInterrupt(ec *eventContext, method string, p *pr.PullRequest, reason, detail, agentName, agentRole string) {
	var msg strings.Builder
	fmt.Fprintf(&msg, "[GitCote] PR %s interrupted: %s/%s PR #%d", agentRole, p.RepoNamespace, p.RepoProject, p.Number)
	fmt.Fprintf(&msg, "\nReason: %s", reason)
	if detail != "" {
		fmt.Fprintf(&msg, "\nDetail: %s", detail)
	}
	if agentName != "" {
		fmt.Fprintf(&msg, "\nAgent: %s (%s)", agentName, agentRole)
	}
	if p.Title != "" {
		fmt.Fprintf(&msg, "\nTitle: %s", p.Title)
	}
	fmt.Fprintf(&msg, "\nTime: %s", time.Now().Format(time.RFC3339))
	if ec.gitcoteURL != "" {
		fmt.Fprintf(&msg, "\nLink: %s/p/%s/%s/prs?pr=%d",
			strings.TrimSuffix(ec.gitcoteURL, "/"), p.RepoNamespace, p.RepoProject, p.Number)
	}
	notify(method, msg.String(), p.RepoNamespace, p.RepoProject, p.Number, ec.logger)
}

// resolveSeedSyncAction resolves the configured event action for a seed
// sync direction ("pull" or "push"), merging project override over global.
func resolveSeedSyncAction(ec *eventContext, ns, proj, direction string) integrity.ResolvedEventAction {
	if ec == nil || ec.integrityHS == nil {
		return integrity.ResolveEventAction(nil, nil)
	}
	global, _ := ec.integrityHS.GetGlobalSeedEventSettings()
	project, _ := ec.integrityHS.GetProjectSeedEventSettings(ns, proj)

	var globalAction, projectAction *integrity.EventAction
	if direction == "push" {
		if global != nil {
			globalAction = global.OnPushConflict
		}
		if project != nil {
			projectAction = project.OnPushConflict
		}
	} else {
		if global != nil {
			globalAction = global.OnPullConflict
		}
		if project != nil {
			projectAction = project.OnPullConflict
		}
	}
	return integrity.ResolveEventAction(projectAction, globalAction)
}

// maybeNotifySeedSyncInterrupt fires notifySeedSyncInterrupt only if the
// resolved event action for this direction has notifications enabled.
// Used to alert on seed sync interruption regardless of whether a merger
// agent was spawned (conflict detection, non-conflict failures) — agent
// spawn/failure notifications remain handled inside spawnAgentForSeedSync.
func maybeNotifySeedSyncInterrupt(ec *eventContext, ns, proj, direction, reason, detail string) {
	action := resolveSeedSyncAction(ec, ns, proj, direction)
	if action.NotifyEnabled {
		go notifySeedSyncInterrupt(ec, action.NotifyMethod, ns, proj, reason, detail, "")
	}
}

func notifySeedSyncInterrupt(ec *eventContext, method, ns, proj, reason, detail, agentName string) {
	var msg strings.Builder
	fmt.Fprintf(&msg, "[GitCote] Seed sync interrupted: %s/%s", ns, proj)
	fmt.Fprintf(&msg, "\nReason: %s", reason)
	if detail != "" {
		fmt.Fprintf(&msg, "\nDetail: %s", detail)
	}
	if agentName != "" {
		fmt.Fprintf(&msg, "\nAgent: %s (merger)", agentName)
	}
	fmt.Fprintf(&msg, "\nTime: %s", time.Now().Format(time.RFC3339))
	if ec.gitcoteURL != "" {
		fmt.Fprintf(&msg, "\nLink: %s/p/%s/%s",
			strings.TrimSuffix(ec.gitcoteURL, "/"), ns, proj)
	}
	notify(method, msg.String(), ns, proj, 0, ec.logger)
}

// markInterrupted transitions a PR to interrupted state.
func markInterrupted(prStore *pr.Store, p *pr.PullRequest, reason, detail, agentName, agentRole string, logger *slog.Logger) {
	p.PreviousState = p.State
	p.State = pr.StateInterrupted
	p.InterruptInfo = &pr.InterruptInfo{
		Reason:    reason,
		Detail:    detail,
		AgentName: agentName,
		AgentRole: agentRole,
		At:        time.Now(),
	}
	p.UpdatedAt = time.Now()
	if err := prStore.Update(p); err != nil {
		logger.Error("failed to mark PR interrupted", "pr", p.Number, "error", err)
	}
}

// spawnAgentForPR finds the appropriate agent config and executes it for a PR.
func spawnAgentForPR(ec *eventContext, action integrity.ResolvedEventAction, p *pr.PullRequest, role string) {
	if !ec.agentCfg.IsEnabled() {
		return
	}

	agentsRoot := ec.agentCfg.EffectiveAgentsRoot()
	configs, err := agent.ScanAgentConfigs(agentsRoot)
	if err != nil {
		ec.logger.Error("scan agent configs for PR event", "error", err)
		detail := fmt.Sprintf("scan configs: %v", err)
		prStore, serr := getPRStore(ec.gitStore.BaseDir(), p.RepoNamespace, p.RepoProject)
		if serr == nil {
			markInterrupted(prStore, p, "agent_spawn_failed", detail, action.AgentName, role, ec.logger)
		}
		if action.NotifyEnabled {
			go notifyInterrupt(ec, action.NotifyMethod, p, "agent_spawn_failed", detail, action.AgentName, role)
		}
		return
	}

	var ac *agent.AgentConfig
	if action.AgentName != "" {
		ac = configs.FindByName(action.AgentName)
	}
	if ac == nil {
		byRole := configs.FindByRole(role)
		if len(byRole) > 0 {
			ac = byRole[0]
		}
	}
	if ac == nil {
		ec.logger.Warn("no agent config found", "role", role, "agent_name", action.AgentName)
		detail := "no agent config found for role: " + role
		prStore, serr := getPRStore(ec.gitStore.BaseDir(), p.RepoNamespace, p.RepoProject)
		if serr == nil {
			markInterrupted(prStore, p, "agent_spawn_failed", detail, action.AgentName, role, ec.logger)
		}
		if action.NotifyEnabled {
			go notifyInterrupt(ec, action.NotifyMethod, p, "agent_spawn_failed", detail, action.AgentName, role)
		}
		return
	}

	maxAttempts := 1
	if action.AutoRetry && action.MaxRetries > 0 {
		maxAttempts = action.MaxRetries + 1
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			ec.logger.Warn("agent retry", "role", role, "attempt", attempt+1, "pr", p.Number)
		}

		ensureNoActiveToken(ec, p.RepoNamespace, p.RepoProject, int(p.Number))

		result := executeAgentForPR(ec, ac, p, role)
		if result != nil && result.ExitCode == 0 {
			if role == "merger" {
				if !reattemptMerge(ec, p, ac.DirName, role) {
					if action.NotifyEnabled && p.InterruptInfo != nil {
						go notifyInterrupt(ec, action.NotifyMethod, p,
							p.InterruptInfo.Reason, p.InterruptInfo.Detail,
							ac.DirName, role)
					}
					return
				}
			}
			if role == "reviewer" {
				prStore, serr := getPRStore(ec.gitStore.BaseDir(), p.RepoNamespace, p.RepoProject)
				if serr == nil {
					current, gerr := prStore.Get(p.Number)
					if gerr == nil && current.State == pr.StateOpen {
						ec.logger.Warn("reviewer exited without verdict", "pr", p.Number, "agent", ac.DirName)
						incDetail := "agent exited successfully but did not approve or reject"
						markInterrupted(prStore, current, "review_incomplete",
							incDetail, ac.DirName, role, ec.logger)
						if action.NotifyEnabled {
							go notifyInterrupt(ec, action.NotifyMethod, p, "review_incomplete", incDetail, ac.DirName, role)
						}
						return
					}
				}
				if !resolveAutoConfirm(ec, p.RepoNamespace, p.RepoProject) {
					return
				}
			}
			releasePRSlotAndDequeue(ec, p.RepoNamespace, p.RepoProject, int(p.Number))
			return
		}

		if attempt < maxAttempts-1 {
			continue
		}

		detail := "agent failed"
		if result != nil {
			detail = fmt.Sprintf("exit code %d", result.ExitCode)
			if result.Killed {
				detail = result.KillReason
			}
		}
		if action.AutoRetry && action.MaxRetries > 0 {
			detail = fmt.Sprintf("agent failed after %d retries: %s", action.MaxRetries, detail)
		}

		prStore, serr := getPRStore(ec.gitStore.BaseDir(), p.RepoNamespace, p.RepoProject)
		if serr == nil {
			markInterrupted(prStore, p, "agent_spawn_failed", detail, ac.DirName, role, ec.logger)
		}
		if action.NotifyEnabled {
			go notifyInterrupt(ec, action.NotifyMethod, p, "agent_spawn_failed", detail, ac.DirName, role)
		}
	}
}

func executeAgentForPR(ec *eventContext, ac *agent.AgentConfig, p *pr.PullRequest, role string) *agent.SpawnResult {
	spawnCtx := &agent.SpawnContext{
		PRId:            fmt.Sprintf("%s/%s#%d", p.RepoNamespace, p.RepoProject, p.Number),
		PRNumber:        int(p.Number),
		Namespace:       p.RepoNamespace,
		Project:         p.RepoProject,
		SourceBranch:    p.SourceBranch,
		TargetBranch:    p.TargetBranch,
		OrderFiles:      strings.Join(p.OrderFiles, ","),
		ResultFiles:     strings.Join(p.ResultFiles, ","),
		ReviewFiles:     strings.Join(p.ReviewFiles, ","),
		RejectionReason: p.RejectionReason,
	}

	if role == "merger" {
		if ec.httpURL != "" {
			spawnCtx.GitURL = strings.TrimSuffix(ec.httpURL, "/") + "/" + p.RepoNamespace + "/" + p.RepoProject + ".git"
		}
		repo, err := ec.gitStore.OpenRepo(p.RepoNamespace, p.RepoProject)
		if err == nil {
			sourceHash, serr := git.ResolveBranch(repo, p.SourceBranch)
			targetHash, _ := git.ResolveBranch(repo, p.TargetBranch)
			if serr == nil {
				mergeResult, merr := git.ComputeMerge(repo, targetHash, sourceHash)
				if merr == nil && !mergeResult.Clean {
					var paths []string
					for _, c := range mergeResult.Conflicts {
						paths = append(paths, c.Path)
					}
					spawnCtx.ConflictFiles = strings.Join(paths, ",")
				}
			}
		}
	}

	token, terr := issueAgentToken(ec, p.RepoNamespace, p.RepoProject, int(p.Number), p.SourceBranch, ac.DirName, role)
	if terr != nil {
		ec.logger.Error("issue agent token", "error", terr, "pr", p.Number, "role", role)
	}
	if token != "" {
		spawnCtx.Token = token
	}

	workDir, cleanup, err := agent.PrepareWorkDir(ac, spawnCtx)
	if err != nil {
		ec.logger.Error("prepare workdir for PR agent", "error", err, "pr", p.Number, "role", role)
		revokeAgentToken(ec, p.RepoNamespace, p.RepoProject, int(p.Number), true)
		return nil
	}

	if ec.gitcoteURL != "" {
		mcpURL := strings.TrimSuffix(ec.gitcoteURL, "/") + "/mcp"
		entry := agent.MCPServerEntry{Type: "http", URL: mcpURL}
		if token != "" {
			entry.Headers = map[string]string{"Authorization": "Bearer " + token}
		}
		if werr := agent.WriteMCPConfig(workDir, map[string]agent.MCPServerEntry{
			"gitcote": entry,
		}); werr != nil {
			ec.logger.Error("write mcp config", "error", werr, "pr", p.Number, "role", role)
		}
	}

	if ec.integrityHS != nil {
		_ = ec.integrityHS.AddAgentWorkdir(integrity.AgentWorkdirRecord{
			Path:      workDir,
			AgentName: ac.DirName,
			Role:      ac.Role,
			Namespace: p.RepoNamespace,
			Project:   p.RepoProject,
			PRNumber:  int(p.Number),
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
			Status:    "running",
		})
	}

	if spawnCtx.Token != "" {
		RegisterAgentToken(spawnCtx.Token)
	}

	result := executeWithActivityTimeout(ec, ac, spawnCtx, workDir)

	if spawnCtx.Token != "" {
		UnregisterAgentToken(spawnCtx.Token)
	}

	status := "completed"
	if result.ExitCode != 0 {
		status = "failed"
	}
	if result.Killed {
		status = "killed"
	}

	if result.ExitCode == 0 {
		revokeAgentToken(ec, p.RepoNamespace, p.RepoProject, int(p.Number), true)
		if !ec.agentCfg.RetainWorkdir {
			cleanup()
			if ec.integrityHS != nil {
				_ = ec.integrityHS.RemoveAgentWorkdir(workDir)
			}
		}
	} else {
		revokeAgentToken(ec, p.RepoNamespace, p.RepoProject, int(p.Number), false)
		if ec.integrityHS != nil {
			_ = ec.integrityHS.UpdateAgentWorkdir(workDir, status, result.ExitCode)
		}
	}

	return result
}

func executeWithActivityTimeout(ec *eventContext, ac *agent.AgentConfig, spawnCtx *agent.SpawnContext, workDir string) *agent.SpawnResult {
	type resultMsg struct {
		result *agent.SpawnResult
		err    error
	}
	ch := make(chan resultMsg, 1)

	go func() {
		r, err := agent.ExecuteAgent(ac, spawnCtx, workDir, ec.agentCfg.TimeoutDuration(), ec.logger)
		ch <- resultMsg{r, err}
	}()

	if spawnCtx.Token == "" {
		msg := <-ch
		if msg.err != nil {
			ec.logger.Error("agent execution error", "error", msg.err, "role", ac.Role)
			return &agent.SpawnResult{ExitCode: -1, StartedAt: time.Now(), FinishedAt: time.Now()}
		}
		return msg.result
	}

	activityTimeout := ec.agentCfg.ActivityTimeoutDuration()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case msg := <-ch:
			if msg.err != nil {
				ec.logger.Error("agent execution error", "error", msg.err, "role", ac.Role)
				return &agent.SpawnResult{ExitCode: -1, StartedAt: time.Now(), FinishedAt: time.Now()}
			}
			return msg.result
		case <-ticker.C:
			last, ok := agentActivity.Load(spawnCtx.Token)
			if ok {
				if time.Since(last.(time.Time)) > activityTimeout {
					ec.logger.Warn("agent stalled (no MCP activity), revoking token before kill",
						"role", ac.Role, "timeout", activityTimeout)
					revokeAgentToken(ec, spawnCtx.Namespace, spawnCtx.Project, spawnCtx.PRNumber, false)
					return &agent.SpawnResult{
						ExitCode:   -1,
						StartedAt:  time.Now(),
						FinishedAt: time.Now(),
						Killed:     true,
						KillReason: fmt.Sprintf("no MCP activity for %s", activityTimeout),
					}
				}
			}
		}
	}
}

// reattemptMerge re-attempts the merge after a successful merger agent.
// Unlike autoMergePR, it does not fire onPRMergeConflict if conflicts persist
// (to prevent infinite re-spawns). Instead it marks the PR as interrupted.
// Returns true if the merge succeeded (PR is now StateMerged), false on any
// failure (PR is marked interrupted with the appropriate reason).
func reattemptMerge(ec *eventContext, p *pr.PullRequest, agentName, role string) bool {
	prStore, err := getPRStore(ec.gitStore.BaseDir(), p.RepoNamespace, p.RepoProject)
	if err != nil {
		ec.logger.Warn("reattempt merge: open PR store", "error", err, "pr", p.Number)
		return false
	}

	repo, err := ec.gitStore.OpenRepo(p.RepoNamespace, p.RepoProject)
	if err != nil {
		ec.logger.Warn("reattempt merge: open repo", "error", err, "pr", p.Number)
		markInterrupted(prStore, p, "merge_incomplete",
			fmt.Sprintf("open repo: %v", err), agentName, role, ec.logger)
		return false
	}
	sourceHash, err := git.ResolveBranch(repo, p.SourceBranch)
	if err != nil {
		ec.logger.Warn("reattempt merge: resolve source", "error", err, "pr", p.Number)
		markInterrupted(prStore, p, "merge_incomplete",
			fmt.Sprintf("resolve source branch: %v", err), agentName, role, ec.logger)
		return false
	}
	targetHash, _ := git.ResolveBranch(repo, p.TargetBranch)

	mergeResult, err := git.ComputeMerge(repo, targetHash, sourceHash)
	if err != nil {
		ec.logger.Warn("reattempt merge: compute", "error", err, "pr", p.Number)
		markInterrupted(prStore, p, "merge_incomplete",
			fmt.Sprintf("compute merge: %v", err), agentName, role, ec.logger)
		return false
	}

	if !mergeResult.Clean {
		ec.logger.Warn("reattempt merge: still conflicting after merger", "pr", p.Number)
		markInterrupted(prStore, p, "merge_still_conflicting",
			"merger agent succeeded but conflicts persist", agentName, role, ec.logger)
		return false
	}

	var mergeHash plumbing.Hash
	if targetHash == plumbing.ZeroHash {
		mergeHash = sourceHash
		if err := git.CreateBranchRef(repo, p.TargetBranch, sourceHash); err != nil {
			ec.logger.Warn("reattempt merge: create target ref", "error", err, "pr", p.Number)
			markInterrupted(prStore, p, "merge_incomplete",
				fmt.Sprintf("create target ref: %v", err), agentName, role, ec.logger)
			return false
		}
	} else {
		msg := fmt.Sprintf("Merge pull request #%d: %s\n\nMerge %s into %s", p.Number, p.Title, p.SourceBranch, p.TargetBranch)
		mergeHash, err = git.MergeCommitFromTree(repo, mergeResult.TreeHash, targetHash, sourceHash, msg, "GitCote", "gitcote@localhost")
		if err != nil {
			ec.logger.Warn("reattempt merge: create commit", "error", err, "pr", p.Number)
			markInterrupted(prStore, p, "merge_incomplete",
				fmt.Sprintf("create merge commit: %v", err), agentName, role, ec.logger)
			return false
		}
		if err := git.UpdateBranchRef(repo, p.TargetBranch, mergeHash, targetHash); err != nil {
			ec.logger.Warn("reattempt merge: update ref", "error", err, "pr", p.Number)
			markInterrupted(prStore, p, "merge_incomplete",
				fmt.Sprintf("update target ref: %v", err), agentName, role, ec.logger)
			return false
		}
	}

	recordHeadHash(ec.gitStore, p.RepoNamespace, p.RepoProject)

	now := time.Now()
	p.State = pr.StateMerged
	p.MergeCommit = mergeHash.String()
	p.MergedAt = &now
	p.UpdatedAt = now
	_ = prStore.Update(p)

	if delErr := git.DeleteBranchRef(repo, p.SourceBranch); delErr == nil {
		p.SourceBranchDeleted = true
		_ = prStore.Update(p)
	}

	ec.logger.Info("reattempt merge: PR merged after conflict resolution",
		"pr", p.Number, "merge_commit", mergeHash.String()[:8])
	return true
}

// releasePRSlotAndDequeue releases the queue slot for a completed PR and
// spawns the reviewer for the next queued PR if one exists.
func releasePRSlotAndDequeue(ec *eventContext, ns, proj string, prNumber int) {
	if ec.integrityHS == nil {
		return
	}
	nextPR, found, err := ec.integrityHS.ReleasePRSlot(ns, proj, prNumber)
	if err != nil {
		ec.logger.Error("release PR slot", "error", err, "pr", prNumber)
		return
	}
	if !found {
		return
	}
	if nextPR == integrity.SeedSyncSentinel {
		ec.logger.Info("seed sync dequeued from PR queue", "namespace", ns, "project", proj)
		go executeSeedPullFromQueue(ec, ns, proj)
		return
	}
	prStore, err := getPRStore(ec.gitStore.BaseDir(), ns, proj)
	if err != nil {
		ec.logger.Error("get PR store for dequeue", "error", err)
		return
	}
	p, err := prStore.Get(uint32(nextPR))
	if err != nil {
		ec.logger.Error("get dequeued PR", "error", err, "pr", nextPR)
		return
	}
	ec.logger.Info("PR dequeued, spawning reviewer", "pr", nextPR, "namespace", ns, "project", proj)
	go onPRCreated(ec, p)
}

func resolveAutoConfirm(ec *eventContext, ns, proj string) bool {
	if ec.integrityHS == nil {
		return false
	}
	global, _ := ec.integrityHS.GetGlobalPREventSettings()
	project, _ := ec.integrityHS.GetProjectPREventSettings(ns, proj)
	var globalAction, projectAction *integrity.ConfirmAction
	if global != nil {
		globalAction = global.OnConfirmed
	}
	if project != nil {
		projectAction = project.OnConfirmed
	}
	return integrity.ResolveConfirmAction(projectAction, globalAction).AutoConfirm
}

// --- PR Event Hooks ---

// onPRCreated fires when a new PR is created.
func onPRCreated(ec *eventContext, p *pr.PullRequest) {
	if ec.integrityHS == nil {
		return
	}
	settings, err := ec.integrityHS.ResolvePREventSettings(p.RepoNamespace, p.RepoProject)
	if err != nil {
		ec.logger.Error("resolve PR event settings", "error", err)
		return
	}
	global, _ := ec.integrityHS.GetGlobalPREventSettings()
	project, _ := ec.integrityHS.GetProjectPREventSettings(p.RepoNamespace, p.RepoProject)

	var globalAction, projectAction *integrity.EventAction
	if global != nil {
		globalAction = global.OnCreated
	}
	if project != nil {
		projectAction = project.OnCreated
	}
	_ = settings

	action := integrity.ResolveEventAction(projectAction, globalAction)
	if action.AgentEnabled {
		go spawnAgentForPR(ec, action, p, "reviewer")
	}
	if action.NotifyEnabled {
		go notify(action.NotifyMethod, fmt.Sprintf("PR #%d created: %s", p.Number, p.Title), p.RepoNamespace, p.RepoProject, p.Number, ec.logger)
	}
}

// onPRApproved fires when a PR is approved.
func onPRApproved(ec *eventContext, p *pr.PullRequest) {
	if ec.integrityHS == nil {
		return
	}
	global, _ := ec.integrityHS.GetGlobalPREventSettings()
	project, _ := ec.integrityHS.GetProjectPREventSettings(p.RepoNamespace, p.RepoProject)

	var globalAction, projectAction *integrity.ConfirmAction
	if global != nil {
		globalAction = global.OnConfirmed
	}
	if project != nil {
		projectAction = project.OnConfirmed
	}

	action := integrity.ResolveConfirmAction(projectAction, globalAction)
	if action.AutoConfirm {
		go func() {
			ec.logger.Info("auto-confirming PR", "pr", p.Number)
			if err := autoMergePR(ec, p); err != nil {
				ec.logger.Warn("auto-merge failed", "pr", p.Number, "error", err)
			}
		}()
	} else if action.NotifyEnabled {
		go notify(action.NotifyMethod, fmt.Sprintf("PR #%d approved, awaiting manual confirm", p.Number), p.RepoNamespace, p.RepoProject, p.Number, ec.logger)
	}
}

func autoMergePR(ec *eventContext, p *pr.PullRequest) error {
	repo, err := ec.gitStore.OpenRepo(p.RepoNamespace, p.RepoProject)
	if err != nil {
		return fmt.Errorf("open repo: %w", err)
	}

	sourceHash, err := git.ResolveBranch(repo, p.SourceBranch)
	if err != nil {
		return fmt.Errorf("resolve source: %w", err)
	}
	targetHash, _ := git.ResolveBranch(repo, p.TargetBranch)

	mergeResult, err := git.ComputeMerge(repo, targetHash, sourceHash)
	if err != nil {
		return fmt.Errorf("compute merge: %w", err)
	}

	prStore, err := getPRStore(ec.gitStore.BaseDir(), p.RepoNamespace, p.RepoProject)
	if err != nil {
		return err
	}

	if !mergeResult.Clean {
		p.State = pr.StateMergeConflict
		p.Mergeable = pr.MergeableConflict
		p.UpdatedAt = time.Now()
		_ = prStore.Update(p)

		onPRMergeConflict(ec, p)
		return fmt.Errorf("merge conflicts detected")
	}

	var mergeHash plumbing.Hash
	if targetHash == plumbing.ZeroHash {
		mergeHash = sourceHash
		if err := git.CreateBranchRef(repo, p.TargetBranch, sourceHash); err != nil {
			return fmt.Errorf("create target ref: %w", err)
		}
	} else {
		msg := fmt.Sprintf("Merge pull request #%d: %s\n\nMerge %s into %s", p.Number, p.Title, p.SourceBranch, p.TargetBranch)
		var mergeErr error
		mergeHash, mergeErr = git.MergeCommitFromTree(repo, mergeResult.TreeHash, targetHash, sourceHash, msg, "GitCote", "gitcote@localhost")
		if mergeErr != nil {
			return fmt.Errorf("create merge commit: %w", mergeErr)
		}
		if err := git.UpdateBranchRef(repo, p.TargetBranch, mergeHash, targetHash); err != nil {
			return fmt.Errorf("update target ref: %w", err)
		}
	}

	recordHeadHash(ec.gitStore, p.RepoNamespace, p.RepoProject)

	now := time.Now()
	p.State = pr.StateMerged
	p.MergeCommit = mergeHash.String()
	p.MergedAt = &now
	p.UpdatedAt = now
	_ = prStore.Update(p)

	if delErr := git.DeleteBranchRef(repo, p.SourceBranch); delErr == nil {
		p.SourceBranchDeleted = true
		_ = prStore.Update(p)
	}

	invalidateApprovalsForPush(ec.gitStore, ec.logger, p.RepoNamespace, p.RepoProject)

	releasePRSlotAndDequeue(ec, p.RepoNamespace, p.RepoProject, int(p.Number))

	return nil
}

// onPRRejected fires when a PR is rejected.
func onPRRejected(ec *eventContext, p *pr.PullRequest) {
	if ec.integrityHS == nil {
		return
	}
	global, _ := ec.integrityHS.GetGlobalPREventSettings()
	project, _ := ec.integrityHS.GetProjectPREventSettings(p.RepoNamespace, p.RepoProject)

	var globalAction, projectAction *integrity.EventAction
	if global != nil {
		globalAction = global.OnRejected
	}
	if project != nil {
		projectAction = project.OnRejected
	}

	action := integrity.ResolveEventAction(projectAction, globalAction)
	if action.AgentEnabled {
		go spawnAgentForPR(ec, action, p, "coder")
	}
	if action.NotifyEnabled {
		go notify(action.NotifyMethod, fmt.Sprintf("PR #%d rejected", p.Number), p.RepoNamespace, p.RepoProject, p.Number, ec.logger)
	}
}

// onPRMergeConflict fires when a merge attempt detects conflicts.
func onPRMergeConflict(ec *eventContext, p *pr.PullRequest) {
	if ec.integrityHS == nil {
		return
	}
	global, _ := ec.integrityHS.GetGlobalPREventSettings()
	project, _ := ec.integrityHS.GetProjectPREventSettings(p.RepoNamespace, p.RepoProject)

	var globalAction, projectAction *integrity.EventAction
	if global != nil {
		globalAction = global.OnMergeConflict
	}
	if project != nil {
		projectAction = project.OnMergeConflict
	}

	action := integrity.ResolveEventAction(projectAction, globalAction)
	if action.AgentEnabled {
		go spawnAgentForPR(ec, action, p, "merger")
	}
	if action.NotifyEnabled {
		go notify(action.NotifyMethod, fmt.Sprintf("PR #%d has merge conflicts", p.Number), p.RepoNamespace, p.RepoProject, p.Number, ec.logger)
	}
}

// --- Seed Sync Agent Spawn ---

// onSeedPullConflict fires when a seed pull detects conflicts.
func onSeedPullConflict(ec *eventContext, ns, proj, tempDir string, conflictFiles []string) {
	if ec.integrityHS == nil {
		return
	}
	global, _ := ec.integrityHS.GetGlobalSeedEventSettings()
	project, _ := ec.integrityHS.GetProjectSeedEventSettings(ns, proj)

	var globalAction, projectAction *integrity.EventAction
	if global != nil {
		globalAction = global.OnPullConflict
	}
	if project != nil {
		projectAction = project.OnPullConflict
	}

	action := integrity.ResolveEventAction(projectAction, globalAction)
	if action.AgentEnabled {
		go spawnAgentForSeedSync(ec, action, ns, proj, tempDir, conflictFiles)
	}
}

// onSeedPushConflict fires when a seed push detects conflicts.
func onSeedPushConflict(ec *eventContext, ns, proj, tempDir string, conflictFiles []string) {
	if ec.integrityHS == nil {
		return
	}
	global, _ := ec.integrityHS.GetGlobalSeedEventSettings()
	project, _ := ec.integrityHS.GetProjectSeedEventSettings(ns, proj)

	var globalAction, projectAction *integrity.EventAction
	if global != nil {
		globalAction = global.OnPushConflict
	}
	if project != nil {
		projectAction = project.OnPushConflict
	}

	action := integrity.ResolveEventAction(projectAction, globalAction)
	if action.AgentEnabled {
		go spawnAgentForSeedSync(ec, action, ns, proj, tempDir, conflictFiles)
	}
}

// spawnAgentForSeedSync finds the appropriate merger agent and executes it for seed sync conflict resolution.
// On failure, the queue slot is retained (matching PR interrupt behavior) so PR
// auto-merge is suspended until the operator retries or dismisses.
func spawnAgentForSeedSync(ec *eventContext, action integrity.ResolvedEventAction, ns, proj, tempDir string, conflictFiles []string) {
	if !ec.agentCfg.IsEnabled() {
		return
	}

	agentsRoot := ec.agentCfg.EffectiveAgentsRoot()
	configs, err := agent.ScanAgentConfigs(agentsRoot)
	if err != nil {
		ec.logger.Error("scan agent configs for seed sync", "error", err)
		detail := fmt.Sprintf("scan configs: %v", err)
		updateSeedSyncState(ec.gitStore, ns, proj, "interrupted")
		if action.NotifyEnabled {
			go notifySeedSyncInterrupt(ec, action.NotifyMethod, ns, proj, "agent_spawn_failed", detail, action.AgentName)
		}
		return
	}

	var ac *agent.AgentConfig
	if action.AgentName != "" {
		ac = configs.FindByName(action.AgentName)
	}
	if ac == nil {
		byRole := configs.FindByRole("merger")
		if len(byRole) > 0 {
			ac = byRole[0]
		}
	}
	if ac == nil {
		detail := "no agent config found for role: merger"
		ec.logger.Warn("no agent config found for seed sync merger", "agent_name", action.AgentName)
		updateSeedSyncState(ec.gitStore, ns, proj, "interrupted")
		if action.NotifyEnabled {
			go notifySeedSyncInterrupt(ec, action.NotifyMethod, ns, proj, "agent_spawn_failed", detail, action.AgentName)
		}
		return
	}

	maxAttempts := 1
	if action.AutoRetry && action.MaxRetries > 0 {
		maxAttempts = action.MaxRetries + 1
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			ec.logger.Warn("seed sync agent retry", "attempt", attempt+1, "ns", ns, "proj", proj)
		}

		ensureNoActiveToken(ec, ns, proj, 0)

		result := executeAgentForSeedSync(ec, ac, ns, proj, tempDir, conflictFiles)
		if result != nil && result.ExitCode == 0 {
			verifySeedSyncAfterAgent(ec, ns, proj, tempDir)
			releaseSeedSyncSlot(ec, ns, proj)
			return
		}

		if attempt < maxAttempts-1 {
			continue
		}

		detail := "agent failed"
		if result != nil {
			detail = fmt.Sprintf("exit code %d", result.ExitCode)
			if result.Killed {
				detail = result.KillReason
			}
		}
		if action.AutoRetry && action.MaxRetries > 0 {
			detail = fmt.Sprintf("seed sync agent failed after %d retries: %s", action.MaxRetries, detail)
		}

		ec.logger.Error("seed sync agent failed", "ns", ns, "proj", proj, "detail", detail)
		updateSeedSyncState(ec.gitStore, ns, proj, "interrupted")
		if action.NotifyEnabled {
			go notifySeedSyncInterrupt(ec, action.NotifyMethod, ns, proj, "seed_sync_agent_failed", detail, ac.DirName)
		}
	}
}

func executeAgentForSeedSync(ec *eventContext, ac *agent.AgentConfig, ns, proj, tempDir string, conflictFiles []string) *agent.SpawnResult {
	spawnCtx := &agent.SpawnContext{
		Namespace:     ns,
		Project:       proj,
		TempCloneDir:  tempDir,
		ConflictFiles: strings.Join(conflictFiles, ","),
	}

	if ec.httpURL != "" {
		spawnCtx.GitURL = strings.TrimSuffix(ec.httpURL, "/") + "/" + ns + "/" + proj + ".git"
	}

	token, terr := issueAgentToken(ec, ns, proj, 0, "", ac.DirName, "merger")
	if terr != nil {
		ec.logger.Error("issue agent token for seed sync", "error", terr, "ns", ns, "proj", proj)
	}
	if token != "" {
		spawnCtx.Token = token
	}

	workDir, cleanup, err := agent.PrepareWorkDir(ac, spawnCtx)
	if err != nil {
		ec.logger.Error("prepare workdir for seed sync agent", "error", err, "ns", ns, "proj", proj)
		revokeAgentToken(ec, ns, proj, 0, true)
		return nil
	}

	if ec.gitcoteURL != "" {
		mcpURL := strings.TrimSuffix(ec.gitcoteURL, "/") + "/mcp"
		entry := agent.MCPServerEntry{Type: "http", URL: mcpURL}
		if token != "" {
			entry.Headers = map[string]string{"Authorization": "Bearer " + token}
		}
		if werr := agent.WriteMCPConfig(workDir, map[string]agent.MCPServerEntry{
			"gitcote": entry,
		}); werr != nil {
			ec.logger.Error("write mcp config for seed sync", "error", werr, "ns", ns, "proj", proj)
		}
	}

	if ec.integrityHS != nil {
		_ = ec.integrityHS.AddAgentWorkdir(integrity.AgentWorkdirRecord{
			Path:      workDir,
			AgentName: ac.DirName,
			Role:      ac.Role,
			Namespace: ns,
			Project:   proj,
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
			Status:    "running",
		})
	}

	if spawnCtx.Token != "" {
		RegisterAgentToken(spawnCtx.Token)
	}

	result := executeWithActivityTimeout(ec, ac, spawnCtx, workDir)

	if spawnCtx.Token != "" {
		UnregisterAgentToken(spawnCtx.Token)
	}

	status := "completed"
	if result.ExitCode != 0 {
		status = "failed"
	}
	if result.Killed {
		status = "killed"
	}

	if result.ExitCode == 0 {
		revokeAgentToken(ec, ns, proj, 0, true)
		if !ec.agentCfg.RetainWorkdir {
			cleanup()
			if ec.integrityHS != nil {
				_ = ec.integrityHS.RemoveAgentWorkdir(workDir)
			}
		}
	} else {
		revokeAgentToken(ec, ns, proj, 0, false)
		if ec.integrityHS != nil {
			_ = ec.integrityHS.UpdateAgentWorkdir(workDir, status, result.ExitCode)
		}
	}

	return result
}

// verifySeedSyncAfterAgent checks if the merger agent successfully updated main.
func verifySeedSyncAfterAgent(ec *eventContext, ns, proj, tempDir string) {
	repo, err := ec.gitStore.OpenRepo(ns, proj)
	if err != nil {
		ec.logger.Warn("verify seed sync: open repo", "error", err, "ns", ns, "proj", proj)
		updateSeedSyncState(ec.gitStore, ns, proj, "interrupted")
		return
	}

	mainHash, err := git.ResolveBranch(repo, "main")
	if err != nil {
		ec.logger.Warn("verify seed sync: resolve main", "error", err, "ns", ns, "proj", proj)
		updateSeedSyncState(ec.gitStore, ns, proj, "interrupted")
		return
	}

	seedRef, err := repo.Reference(plumbing.ReferenceName("refs/remotes/seed/main"), true)
	if err != nil {
		ec.logger.Info("verify seed sync: no seed remote ref, assuming agent pushed", "ns", ns, "proj", proj)
		cleanupTempCloneDir(ec, tempDir)
		updateSeedSyncState(ec.gitStore, ns, proj, "idle")
		ec.logger.Info("seed sync: agent resolved conflict successfully", "ns", ns, "proj", proj, "main", mainHash.String()[:8])
		return
	}

	seedHash := seedRef.Hash()
	seedCommit, err := repo.CommitObject(seedHash)
	if err != nil {
		ec.logger.Warn("verify seed sync: resolve seed commit", "error", err)
		cleanupTempCloneDir(ec, tempDir)
		updateSeedSyncState(ec.gitStore, ns, proj, "idle")
		return
	}

	mainCommit, err := repo.CommitObject(mainHash)
	if err != nil {
		ec.logger.Warn("verify seed sync: resolve main commit", "error", err)
		updateSeedSyncState(ec.gitStore, ns, proj, "interrupted")
		return
	}

	isSeedAncestor, _ := seedCommit.IsAncestor(mainCommit)
	if isSeedAncestor {
		cleanupTempCloneDir(ec, tempDir)
		updateSeedSyncState(ec.gitStore, ns, proj, "idle")
		ec.logger.Info("seed sync: agent resolved conflict, main contains seed changes",
			"ns", ns, "proj", proj, "main", mainHash.String()[:8])
	} else {
		ec.logger.Warn("seed sync: agent succeeded but main does not contain seed changes",
			"ns", ns, "proj", proj)
		updateSeedSyncState(ec.gitStore, ns, proj, "interrupted")
	}
}

func cleanupTempCloneDir(ec *eventContext, tempDir string) {
	if err := os.RemoveAll(tempDir); err != nil {
		ec.logger.Warn("cleanup temp clone", "error", err, "path", tempDir)
	}
	if ec.integrityHS != nil {
		_ = ec.integrityHS.RemoveTempClone(tempDir)
	}
}

// reconcileExternalSeedSync detects when an operator manually resolved a
// seed-pull conflict and pushed the result to main outside GitCote's agent
// flow. If refs/remotes/seed/main is an ancestor of local main, the conflict
// is resolved — auto-clear the interrupted/conflict state and release the
// queue slot. Mirrors reconcileExternalMerges for PRs.
func reconcileExternalSeedSync(store *git.Store, ec *eventContext, ns, proj string, logger *slog.Logger) {
	if ec == nil || ec.integrityHS == nil {
		return
	}

	q, err := ec.integrityHS.GetPRQueue(ns, proj)
	if err != nil || q.ActivePR != integrity.SeedSyncSentinel {
		return
	}

	projPath, err := store.ProjectPath(ns, proj)
	if err != nil {
		return
	}
	cfg, err := git.LoadSeedConfig(projPath)
	if err != nil || cfg.SyncStatus == nil {
		return
	}
	if cfg.SyncStatus.State != "interrupted" && cfg.SyncStatus.State != "conflict" {
		return
	}

	repo, err := store.OpenRepo(ns, proj)
	if err != nil {
		return
	}

	seedRef, err := repo.Reference(plumbing.ReferenceName("refs/remotes/seed/main"), true)
	if err != nil {
		return
	}
	mainHash, err := git.ResolveBranch(repo, "main")
	if err != nil {
		return
	}

	seedCommit, err := repo.CommitObject(seedRef.Hash())
	if err != nil {
		return
	}
	mainCommit, err := repo.CommitObject(mainHash)
	if err != nil {
		return
	}

	isSeedAncestor, _ := seedCommit.IsAncestor(mainCommit)
	if !isSeedAncestor {
		return
	}

	if cfg.SyncStatus.Direction == "push" {
		reconcileExternalPushSync(store, ec, ns, proj, logger)
		return
	}

	updateSeedSyncState(store, ns, proj, "idle")
	releaseSeedSyncSlot(ec, ns, proj)
	logger.Info("seed sync resolved externally (seed ancestor of main)",
		"namespace", ns, "project", proj)
}

// reconcileExternalPushSync completes an externally-resolved seed-PUSH
// conflict. Unlike pull, a manual push to gitcote's own main only proves the
// operator resolved the conflict LOCALLY — it does not deliver anything to
// the actual seed remote (the objective of a push sync). Since local main
// now contains the seed's tip as an ancestor (verified by the caller), a
// push to seed is a clean fast-forward from the seed's perspective — attempt
// it before declaring the sync complete. Leave the interrupted state in
// place if delivery still fails, so the operator is not misled.
func reconcileExternalPushSync(store *git.Store, ec *eventContext, ns, proj string, logger *slog.Logger) {
	if ec.seedCtx == nil {
		logger.Warn("seed sync (push) resolved locally but no seed context available to deliver",
			"namespace", ns, "project", proj)
		return
	}
	if !acquireSeedLock(&seedPushActive, ns, proj) {
		// A push is already running elsewhere (e.g. operator-triggered retry); let it finish naturally.
		return
	}
	defer releaseSeedLock(&seedPushActive, ns, proj)

	result := doSeedPush(ec.seedCtx, ec, ns, proj, "main")
	if !result.Success {
		logger.Warn("seed sync (push): local conflict resolved but delivery to seed failed",
			"namespace", ns, "project", proj, "message", result.Message)
		return
	}

	updateSeedSyncState(store, ns, proj, "idle")
	releaseSeedSyncSlot(ec, ns, proj)
	logger.Info("seed sync (push) resolved externally and delivered to seed",
		"namespace", ns, "project", proj)
}

func updateSeedSyncState(gitStore *git.Store, ns, proj, state string) {
	projPath, err := gitStore.ProjectPath(ns, proj)
	if err != nil {
		return
	}
	cfg, err := git.LoadSeedConfig(projPath)
	if err != nil {
		_ = git.UpdateSeedStatus(projPath, &git.SeedSyncStatus{State: state})
		return
	}
	if cfg.SyncStatus == nil {
		cfg.SyncStatus = &git.SeedSyncStatus{}
	}
	cfg.SyncStatus.State = state
	_ = git.SaveSeedConfig(projPath, cfg)
}

// updateSeedSyncStateDirection is like updateSeedSyncState but also records
// which flow ("pull" or "push") produced this state, so reconciliation logic
// can apply direction-appropriate completion semantics.
func updateSeedSyncStateDirection(gitStore *git.Store, ns, proj, state, direction string) {
	projPath, err := gitStore.ProjectPath(ns, proj)
	if err != nil {
		return
	}
	cfg, err := git.LoadSeedConfig(projPath)
	if err != nil {
		_ = git.UpdateSeedStatus(projPath, &git.SeedSyncStatus{State: state, Direction: direction})
		return
	}
	if cfg.SyncStatus == nil {
		cfg.SyncStatus = &git.SeedSyncStatus{}
	}
	cfg.SyncStatus.State = state
	cfg.SyncStatus.Direction = direction
	_ = git.SaveSeedConfig(projPath, cfg)
}

// executeSeedPullFromQueue runs seed pull when the queue slot becomes available.
// The queue slot is already held (SeedSyncSentinel is active).
// On failure or conflict, the slot is retained so PR auto-merge stays suspended.
func executeSeedPullFromQueue(ec *eventContext, ns, proj string) {
	if ec.seedCtx == nil {
		ec.logger.Error("seed sync from queue: no seed context")
		updateSeedSyncState(ec.gitStore, ns, proj, "interrupted")
		return
	}

	if !acquireSeedLock(&seedPullActive, ns, proj) {
		ec.logger.Warn("seed pull already in progress, skipping queue-triggered pull", "ns", ns, "proj", proj)
		return
	}
	defer releaseSeedLock(&seedPullActive, ns, proj)

	updateSeedSyncState(ec.gitStore, ns, proj, "syncing")
	result := doSeedPull(ec.seedCtx, ec, ns, proj, "main")
	success, _ := result["success"].(bool)
	status, _ := result["status"].(string)
	if success {
		updateSeedSyncState(ec.gitStore, ns, proj, "idle")
		releaseSeedSyncSlot(ec, ns, proj)
	} else if status == "conflict" {
		updateSeedSyncStateDirection(ec.gitStore, ns, proj, "conflict", "pull")
		maybeNotifySeedSyncInterrupt(ec, ns, proj, "pull", "pull_conflict", pullResultDetail(result))
		// Slot retained — agent (if enabled) or operator handles it.
	} else {
		ec.logger.Warn("seed sync from queue failed", "ns", ns, "proj", proj, "result", result)
		updateSeedSyncStateDirection(ec.gitStore, ns, proj, "interrupted", "pull")
		maybeNotifySeedSyncInterrupt(ec, ns, proj, "pull", "pull_failed", pullResultDetail(result))
		// Slot retained — operator uses retry_seed_sync or dismiss_seed_sync.
	}
}

func seedPullConflictAgentEnabled(ec *eventContext, ns, proj string) bool {
	if ec.integrityHS == nil {
		return false
	}
	global, _ := ec.integrityHS.GetGlobalSeedEventSettings()
	project, _ := ec.integrityHS.GetProjectSeedEventSettings(ns, proj)
	var globalAction, projectAction *integrity.EventAction
	if global != nil {
		globalAction = global.OnPullConflict
	}
	if project != nil {
		projectAction = project.OnPullConflict
	}
	action := integrity.ResolveEventAction(projectAction, globalAction)
	return action.AgentEnabled
}

func seedPushConflictAgentEnabled(ec *eventContext, ns, proj string) bool {
	if ec.integrityHS == nil {
		return false
	}
	global, _ := ec.integrityHS.GetGlobalSeedEventSettings()
	project, _ := ec.integrityHS.GetProjectSeedEventSettings(ns, proj)
	var globalAction, projectAction *integrity.EventAction
	if global != nil {
		globalAction = global.OnPushConflict
	}
	if project != nil {
		projectAction = project.OnPushConflict
	}
	action := integrity.ResolveEventAction(projectAction, globalAction)
	return action.AgentEnabled
}

// releaseSeedSyncSlot releases the queue slot held by seed sync.
func releaseSeedSyncSlot(ec *eventContext, ns, proj string) {
	if ec.integrityHS == nil {
		return
	}
	nextPR, found, err := ec.integrityHS.ReleasePRSlot(ns, proj, integrity.SeedSyncSentinel)
	if err != nil {
		ec.logger.Error("release seed sync slot", "error", err, "ns", ns, "proj", proj)
		return
	}
	if !found {
		return
	}
	prStore, err := getPRStore(ec.gitStore.BaseDir(), ns, proj)
	if err != nil {
		ec.logger.Error("get PR store after seed sync", "error", err)
		return
	}
	p, err := prStore.Get(uint32(nextPR))
	if err != nil {
		ec.logger.Error("get dequeued PR after seed sync", "error", err, "pr", nextPR)
		return
	}
	ec.logger.Info("PR dequeued after seed sync", "pr", nextPR, "namespace", ns, "project", proj)
	go onPRCreated(ec, p)
}

// --- Event Settings WebSocket Handlers ---

const (
	MsgPREventSettingsGet          uiws.MessageType = "PR_EVENT_SETTINGS_GET"
	MsgPREventSettingsSetGlobal    uiws.MessageType = "PR_EVENT_SETTINGS_SET_GLOBAL"
	MsgPREventSettingsSetProject   uiws.MessageType = "PR_EVENT_SETTINGS_SET_PROJECT"
	MsgPREventSettingsClearProject uiws.MessageType = "PR_EVENT_SETTINGS_CLEAR_PROJECT"

	MsgSeedEventSettingsGet          uiws.MessageType = "SEED_EVENT_SETTINGS_GET"
	MsgSeedEventSettingsSetGlobal    uiws.MessageType = "SEED_EVENT_SETTINGS_SET_GLOBAL"
	MsgSeedEventSettingsSetProject   uiws.MessageType = "SEED_EVENT_SETTINGS_SET_PROJECT"
	MsgSeedEventSettingsClearProject uiws.MessageType = "SEED_EVENT_SETTINGS_CLEAR_PROJECT"
)

var EventSettingsLevels = map[uiws.MessageType]uiws.Op{
	MsgPREventSettingsGet:          {Level: authz.LevelRead, Global: false},
	MsgPREventSettingsSetGlobal:    {Level: authz.LevelAdmin, Global: true},
	MsgPREventSettingsSetProject:   {Level: authz.LevelAdmin, Global: false},
	MsgPREventSettingsClearProject: {Level: authz.LevelAdmin, Global: false},

	MsgSeedEventSettingsGet:          {Level: authz.LevelRead, Global: false},
	MsgSeedEventSettingsSetGlobal:    {Level: authz.LevelAdmin, Global: true},
	MsgSeedEventSettingsSetProject:   {Level: authz.LevelAdmin, Global: false},
	MsgSeedEventSettingsClearProject: {Level: authz.LevelAdmin, Global: false},
}

func eventSettingsDispatch(c *uiws.Client, hs *integrity.Store, msgType uiws.MessageType, payload json.RawMessage) bool {
	if hs == nil {
		return false
	}
	switch msgType {
	case MsgPREventSettingsGet:
		handlePREventSettingsGet(c, hs, payload)
	case MsgPREventSettingsSetGlobal:
		handlePREventSettingsSetGlobal(c, hs, payload)
	case MsgPREventSettingsSetProject:
		handlePREventSettingsSetProject(c, hs, payload)
	case MsgPREventSettingsClearProject:
		handlePREventSettingsClearProject(c, hs, payload)
	case MsgSeedEventSettingsGet:
		handleSeedEventSettingsGet(c, hs, payload)
	case MsgSeedEventSettingsSetGlobal:
		handleSeedEventSettingsSetGlobal(c, hs, payload)
	case MsgSeedEventSettingsSetProject:
		handleSeedEventSettingsSetProject(c, hs, payload)
	case MsgSeedEventSettingsClearProject:
		handleSeedEventSettingsClearProject(c, hs, payload)
	default:
		return false
	}
	return true
}

type eventSettingsProjectPayload struct {
	Namespace   string `json:"namespace"`
	ProjectName string `json:"projectName"`
}

func handlePREventSettingsGet(c *uiws.Client, hs *integrity.Store, payload json.RawMessage) {
	var p eventSettingsProjectPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	global, _ := hs.GetGlobalPREventSettings()
	project, _ := hs.GetProjectPREventSettings(p.Namespace, p.ProjectName)

	resp := map[string]interface{}{
		"global":  global,
		"project": project,
	}
	c.SendResponse(MsgPREventSettingsGet, resp)
}

func handlePREventSettingsSetGlobal(c *uiws.Client, hs *integrity.Store, payload json.RawMessage) {
	var settings integrity.PREventSettings
	if err := json.Unmarshal(payload, &settings); err != nil {
		c.SendError("invalid payload")
		return
	}
	if err := hs.SetGlobalPREventSettings(&settings); err != nil {
		c.SendError(err.Error())
		return
	}
	c.SendResponse(MsgPREventSettingsSetGlobal, map[string]string{"status": "ok"})
}

type prEventSettingsSetProjectPayload struct {
	Namespace   string                   `json:"namespace"`
	ProjectName string                   `json:"projectName"`
	Settings    integrity.PREventSettings `json:"settings"`
}

func handlePREventSettingsSetProject(c *uiws.Client, hs *integrity.Store, payload json.RawMessage) {
	var p prEventSettingsSetProjectPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	if err := hs.SetProjectPREventSettings(p.Namespace, p.ProjectName, &p.Settings); err != nil {
		c.SendError(err.Error())
		return
	}
	c.SendResponse(MsgPREventSettingsSetProject, map[string]string{"status": "ok"})
}

func handlePREventSettingsClearProject(c *uiws.Client, hs *integrity.Store, payload json.RawMessage) {
	var p eventSettingsProjectPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	if err := hs.ClearProjectPREventSettings(p.Namespace, p.ProjectName); err != nil {
		c.SendError(err.Error())
		return
	}
	c.SendResponse(MsgPREventSettingsClearProject, map[string]string{"status": "ok"})
}

func handleSeedEventSettingsGet(c *uiws.Client, hs *integrity.Store, payload json.RawMessage) {
	var p eventSettingsProjectPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	global, _ := hs.GetGlobalSeedEventSettings()
	project, _ := hs.GetProjectSeedEventSettings(p.Namespace, p.ProjectName)

	resp := map[string]interface{}{
		"global":  global,
		"project": project,
	}
	c.SendResponse(MsgSeedEventSettingsGet, resp)
}

func handleSeedEventSettingsSetGlobal(c *uiws.Client, hs *integrity.Store, payload json.RawMessage) {
	var settings integrity.SeedEventSettings
	if err := json.Unmarshal(payload, &settings); err != nil {
		c.SendError("invalid payload")
		return
	}
	if err := hs.SetGlobalSeedEventSettings(&settings); err != nil {
		c.SendError(err.Error())
		return
	}
	c.SendResponse(MsgSeedEventSettingsSetGlobal, map[string]string{"status": "ok"})
}

type seedEventSettingsSetProjectPayload struct {
	Namespace   string                     `json:"namespace"`
	ProjectName string                     `json:"projectName"`
	Settings    integrity.SeedEventSettings `json:"settings"`
}

func handleSeedEventSettingsSetProject(c *uiws.Client, hs *integrity.Store, payload json.RawMessage) {
	var p seedEventSettingsSetProjectPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	if err := hs.SetProjectSeedEventSettings(p.Namespace, p.ProjectName, &p.Settings); err != nil {
		c.SendError(err.Error())
		return
	}
	c.SendResponse(MsgSeedEventSettingsSetProject, map[string]string{"status": "ok"})
}

func handleSeedEventSettingsClearProject(c *uiws.Client, hs *integrity.Store, payload json.RawMessage) {
	var p eventSettingsProjectPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	if err := hs.ClearProjectSeedEventSettings(p.Namespace, p.ProjectName); err != nil {
		c.SendError(err.Error())
		return
	}
	c.SendResponse(MsgSeedEventSettingsClearProject, map[string]string{"status": "ok"})
}

