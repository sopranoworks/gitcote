package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/sopranoworks/gityard/internal/agent"
	"github.com/sopranoworks/gityard/internal/git"
	"github.com/sopranoworks/gityard/internal/integrity"
	"github.com/sopranoworks/gityard/internal/pr"
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

const agentTokenClientID = "gityard-agent"

func agentTokenKey(namespace, project string, prNumber int) string {
	if prNumber == 0 {
		return fmt.Sprintf("seed:%s/%s", namespace, project)
	}
	return fmt.Sprintf("%s/%s#%d", namespace, project, prNumber)
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
	rec, err := ec.oauthStore.NewSeries(
		agentTokenClientID,
		oauthstore.Principal{Name: "agent-token", Email: "agent@gityard.local"},
		"",
		scope,
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
	gityardURL  string
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
		prStore, serr := getPRStore(ec.gitStore.BaseDir(), p.RepoNamespace, p.RepoProject)
		if serr == nil {
			markInterrupted(prStore, p, "agent_spawn_failed", fmt.Sprintf("scan configs: %v", err), action.AgentName, role, ec.logger)
		}
		releasePRSlotAndDequeue(ec, p.RepoNamespace, p.RepoProject, int(p.Number))
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
		prStore, serr := getPRStore(ec.gitStore.BaseDir(), p.RepoNamespace, p.RepoProject)
		if serr == nil {
			markInterrupted(prStore, p, "agent_spawn_failed", "no agent config found for role: "+role, action.AgentName, role, ec.logger)
		}
		releasePRSlotAndDequeue(ec, p.RepoNamespace, p.RepoProject, int(p.Number))
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
		releasePRSlotAndDequeue(ec, p.RepoNamespace, p.RepoProject, int(p.Number))
	}
}

func executeAgentForPR(ec *eventContext, ac *agent.AgentConfig, p *pr.PullRequest, role string) *agent.SpawnResult {
	spawnCtx := &agent.SpawnContext{
		PRId:         fmt.Sprintf("%s/%s#%d", p.RepoNamespace, p.RepoProject, p.Number),
		PRNumber:     int(p.Number),
		Namespace:    p.RepoNamespace,
		Project:      p.RepoProject,
		SourceBranch: p.SourceBranch,
		TargetBranch: p.TargetBranch,
		OrderFiles:   strings.Join(p.OrderFiles, ","),
		ResultFiles:  strings.Join(p.ResultFiles, ","),
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

	if ec.gityardURL != "" {
		mcpURL := strings.TrimSuffix(ec.gityardURL, "/") + "/mcp"
		entry := agent.MCPServerEntry{Type: "http", URL: mcpURL}
		if token != "" {
			entry.Headers = map[string]string{"Authorization": "Bearer " + token}
		}
		if werr := agent.WriteMCPConfig(workDir, map[string]agent.MCPServerEntry{
			"gityard": entry,
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
		mergeHash, mergeErr = git.MergeCommitFromTree(repo, mergeResult.TreeHash, targetHash, sourceHash, msg, "GitYard", "gityard@localhost")
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

