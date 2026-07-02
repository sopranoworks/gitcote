package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sopranoworks/gitcote/internal/agent"
	"github.com/sopranoworks/gitcote/internal/git"
	"github.com/sopranoworks/gitcote/internal/integrity"
	"github.com/sopranoworks/gitcote/internal/pr"
	"github.com/sopranoworks/shoka/pkg/auth"
	"github.com/sopranoworks/shoka/pkg/authz"
	"github.com/sopranoworks/shoka/pkg/uiws"
)

// prStoreCache caches open PR stores per project to avoid re-opening the bbolt db
// on every push. The stores are opened lazily and kept open for the process lifetime.
var (
	prStores   = map[string]*pr.Store{}
	prStoresMu sync.Mutex
)

func getPRStore(baseDir, namespace, project string) (*pr.Store, error) {
	key := namespace + "/" + project
	prStoresMu.Lock()
	defer prStoresMu.Unlock()
	if s, ok := prStores[key]; ok {
		return s, nil
	}
	projPath := filepath.Join(baseDir, namespace, project)
	s, err := pr.Open(filepath.Join(projPath, "prs.db"))
	if err != nil {
		return nil, err
	}
	prStores[key] = s
	return s, nil
}

// handlePostReceive processes push options after a successful receive-pack.
// If "pull_request.create" is present, creates or updates a PR.
func handlePostReceive(store *git.Store, logger *slog.Logger, namespace, project string, principal auth.Principal, pushOpts []string, ec *eventContext) {
	opts := parsePushOpts(pushOpts)
	createPR := false
	target := ""
	title := ""
	var orderFiles []string
	var resultFiles []string
	var sourceBranch string

	if _, ok := opts["pull_request.create"]; ok {
		createPR = true
	}
	if vals := opts["pull_request.target"]; len(vals) > 0 {
		if v := vals[len(vals)-1]; v != "" {
			target = v
		}
	}
	if vals := opts["pull_request.title"]; len(vals) > 0 {
		title = vals[len(vals)-1]
	}
	for _, v := range opts["pull_request.order_files"] {
		for _, f := range strings.Split(v, ",") {
			if f = strings.TrimSpace(f); f != "" {
				orderFiles = append(orderFiles, f)
			}
		}
	}
	for _, v := range opts["pull_request.result_files"] {
		for _, f := range strings.Split(v, ",") {
			if f = strings.TrimSpace(f); f != "" {
				resultFiles = append(resultFiles, f)
			}
		}
	}

	recordHeadHash(store, namespace, project)

	if !createPR {
		invalidateApprovalsForPush(store, logger, namespace, project)
		return
	}

	repo, err := store.OpenRepo(namespace, project)
	if err != nil {
		logger.Error("open repo for PR creation", "error", err)
		return
	}

	if target == "" {
		resolved, err := git.ResolveDefaultBranch(repo)
		if err != nil {
			logger.Error("resolve default branch", "error", err)
			return
		}
		target = resolved
	}
	branches, err := git.ListBranches(repo)
	if err != nil {
		logger.Error("list branches for PR creation", "error", err)
		return
	}
	for _, b := range branches {
		if b != target && sourceBranch == "" {
			sourceBranch = b
		}
	}

	if sourceBranch == "" {
		logger.Warn("no source branch found for PR creation")
		return
	}

	if title == "" {
		title = sourceBranch
	}

	author := principal.Email
	if author == "" {
		author = principal.Name
	}
	if author == "" {
		author = "anonymous"
	}

	prStore, err := getPRStore(store.BaseDir(), namespace, project)
	if err != nil {
		logger.Error("open PR store", "error", err)
		return
	}

	sourceHash, err := git.ResolveBranch(repo, sourceBranch)
	if err != nil {
		logger.Error("resolve source branch", "error", err)
		return
	}
	targetHash, _ := git.ResolveBranch(repo, target)

	// Check for existing open PR on same source→target.
	existing, _ := prStore.FindByBranches(sourceBranch, target)
	if existing != nil {
		// Update the existing PR: new source commit, recompute mergeable.
		existing.SourceCommit = sourceHash.String()
		existing.UpdatedAt = time.Now()
		// Invalidate approval if source changed.
		if existing.State == pr.StateApproved {
			existing.State = pr.StateOpen
			existing.ApprovedBy = ""
			existing.ApprovedAt = nil
		}
		existing.Mergeable = computeMergeableForRepo(store, namespace, project, existing.SourceBranch, existing.TargetBranch)
		if err := prStore.Update(existing); err != nil {
			logger.Error("update existing PR", "error", err)
		}
		logger.Info("pr updated", "number", existing.Number, "source", sourceBranch, "target", target)
		return
	}

	// Create new PR.
	mergeable := computeMergeableForRepo(store, namespace, project, sourceBranch, target)
	now := time.Now()
	if orderFiles == nil {
		orderFiles = []string{}
	}
	if resultFiles == nil {
		resultFiles = []string{}
	}
	newPR := &pr.PullRequest{
		RepoNamespace: namespace,
		RepoProject:   project,
		Title:         title,
		SourceBranch:  sourceBranch,
		TargetBranch:  target,
		Author:        author,
		State:         pr.StateOpen,
		Mergeable:     mergeable,
		SourceCommit:  sourceHash.String(),
		TargetCommit:  targetHash.String(),
		CreatedAt:     now,
		UpdatedAt:     now,
		OrderFiles:    orderFiles,
		ResultFiles:   resultFiles,
	}
	num, err := prStore.Create(newPR)
	if err != nil {
		logger.Error("create PR", "error", err)
		return
	}
	logger.Info("pr created", "number", num, "source", sourceBranch, "target", target, "mergeable", mergeable)
	if ec != nil && ec.integrityHS != nil {
		isActive, qerr := ec.integrityHS.EnqueuePR(namespace, project, int(num))
		if qerr != nil {
			logger.Error("enqueue PR", "error", qerr)
		}
		if isActive {
			go onPRCreated(ec, newPR)
		} else {
			logger.Info("PR queued, waiting for active PR to complete", "pr", num, "namespace", namespace, "project", project)
		}
	} else if ec != nil {
		go onPRCreated(ec, newPR)
	}
}

func invalidateApprovalsForPush(store *git.Store, logger *slog.Logger, namespace, project string) {
	prStore, err := getPRStore(store.BaseDir(), namespace, project)
	if err != nil {
		return
	}
	repo, err := store.OpenRepo(namespace, project)
	if err != nil {
		return
	}

	prs, _ := prStore.List("")
	for i := range prs {
		p := &prs[i]
		if p.State != pr.StateOpen && p.State != pr.StateApproved {
			continue
		}
		currentSource, _ := git.ResolveBranch(repo, p.SourceBranch)
		currentTarget, _ := git.ResolveBranch(repo, p.TargetBranch)
		changed := false
		if currentSource.String() != p.SourceCommit {
			p.SourceCommit = currentSource.String()
			changed = true
			if p.State == pr.StateApproved {
				p.State = pr.StateOpen
				p.ApprovedBy = ""
				p.ApprovedAt = nil
			}
		}
		currentTargetStr := currentTarget.String()
		if currentTargetStr != p.TargetCommit {
			p.TargetCommit = currentTargetStr
			changed = true
			p.Mergeable = pr.MergeableUnknown
		}
		if changed {
			p.Mergeable = computeMergeableForRepo(store, namespace, project, p.SourceBranch, p.TargetBranch)
			p.UpdatedAt = time.Now()
			_ = prStore.Update(p)
		}
	}
}

func computeMergeableForRepo(store *git.Store, namespace, project, sourceBranch, targetBranch string) pr.Mergeable {
	repo, err := store.OpenRepo(namespace, project)
	if err != nil {
		return pr.MergeableUnknown
	}
	sourceHash, err := git.ResolveBranch(repo, sourceBranch)
	if err != nil {
		return pr.MergeableUnknown
	}
	targetHash, _ := git.ResolveBranch(repo, targetBranch)
	result, err := git.CheckConflicts(repo, sourceHash, targetHash)
	if err != nil {
		return pr.MergeableUnknown
	}
	if result.HasConflict {
		return pr.MergeableConflict
	}
	return pr.MergeableClean
}

// parsePushOpts converts a []string of "key=value" or bare "key" into a multi-value map.
// Multiple -o flags with the same key accumulate values.
func parsePushOpts(opts []string) map[string][]string {
	m := make(map[string][]string, len(opts))
	for _, o := range opts {
		if i := strings.IndexByte(o, '='); i >= 0 {
			key := o[:i]
			m[key] = append(m[key], o[i+1:])
		} else {
			m[o] = append(m[o], "")
		}
	}
	return m
}

func authorizePR(ctx context.Context, namespace, project string, level authz.Level) error {
	principal, hasPrincipal := auth.PrincipalFrom(ctx)
	if !hasPrincipal {
		return nil
	}
	return authz.Authorize(principal.Scope, namespace, project, level)
}

// registerPRTools registers the PR MCP tools.
func registerPRTools(mcpServer *mcp.Server, gitStore *git.Store, sc *seedContext, ec *eventContext) {
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "create_pull_request",
		Description: "Create a pull request. Optionally attach order_files (what the coder was told to implement) and result_files (what the coder produced) as opaque B-47 absolute paths.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createPRInput) (*mcp.CallToolResult, createPROutput, error) {
		if err := authorizePR(ctx, in.Namespace, in.ProjectName, authz.LevelWrite); err != nil {
			return nil, createPROutput{}, fmt.Errorf("access denied")
		}
		prStore, err := getPRStore(gitStore.BaseDir(), in.Namespace, in.ProjectName)
		if err != nil {
			return nil, createPROutput{}, err
		}
		repo, err := gitStore.OpenRepo(in.Namespace, in.ProjectName)
		if err != nil {
			return nil, createPROutput{}, fmt.Errorf("open repo: %w", err)
		}

		if in.TargetBranch == "" {
			resolved, err := git.ResolveDefaultBranch(repo)
			if err != nil {
				return nil, createPROutput{}, fmt.Errorf("resolve default branch: %w", err)
			}
			in.TargetBranch = resolved
		}

		sourceHash, err := git.ResolveBranch(repo, in.SourceBranch)
		if err != nil {
			return nil, createPROutput{}, fmt.Errorf("resolve source branch %q: %w", in.SourceBranch, err)
		}
		targetHash, _ := git.ResolveBranch(repo, in.TargetBranch)

		existing, _ := prStore.FindByBranches(in.SourceBranch, in.TargetBranch)
		if existing != nil {
			return nil, createPROutput{}, fmt.Errorf("PR already exists for %s → %s: #%d", in.SourceBranch, in.TargetBranch, existing.Number)
		}

		principal, _ := auth.PrincipalFrom(ctx)
		author := principal.Email
		if author == "" {
			author = principal.Name
		}
		if author == "" {
			author = "anonymous"
		}

		orderFiles := in.OrderFiles
		if orderFiles == nil {
			orderFiles = []string{}
		}
		resultFiles := in.ResultFiles
		if resultFiles == nil {
			resultFiles = []string{}
		}

		mergeable := computeMergeableForRepo(gitStore, in.Namespace, in.ProjectName, in.SourceBranch, in.TargetBranch)
		now := time.Now()
		newPR := &pr.PullRequest{
			RepoNamespace: in.Namespace,
			RepoProject:   in.ProjectName,
			Title:         in.Title,
			Description:   in.Description,
			SourceBranch:  in.SourceBranch,
			TargetBranch:  in.TargetBranch,
			Author:        author,
			State:         pr.StateOpen,
			Mergeable:     mergeable,
			SourceCommit:  sourceHash.String(),
			TargetCommit:  targetHash.String(),
			CreatedAt:     now,
			UpdatedAt:     now,
			OrderFiles:    orderFiles,
			ResultFiles:   resultFiles,
		}
		num, err := prStore.Create(newPR)
		if err != nil {
			return nil, createPROutput{}, err
		}
		if ec != nil && ec.integrityHS != nil {
			isActive, qerr := ec.integrityHS.EnqueuePR(in.Namespace, in.ProjectName, int(num))
			if qerr != nil {
				slog.Default().Error("enqueue PR", "error", qerr)
			}
			if isActive {
				go onPRCreated(ec, newPR)
			}
		} else if ec != nil {
			go onPRCreated(ec, newPR)
		}
		return nil, createPROutput{Number: num, State: string(pr.StateOpen)}, nil
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "list_pull_requests",
		Description: "List pull requests for a repository, optionally filtered by state (open/approved/merged/closed).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listPRsInput) (*mcp.CallToolResult, listPRsOutput, error) {
		if err := authorizePR(ctx, in.Namespace, in.ProjectName, authz.LevelRead); err != nil {
			return nil, listPRsOutput{}, fmt.Errorf("access denied")
		}
		prStore, err := getPRStore(gitStore.BaseDir(), in.Namespace, in.ProjectName)
		if err != nil {
			return nil, listPRsOutput{}, err
		}
		prs, err := prStore.List(pr.PRState(in.State))
		if err != nil {
			return nil, listPRsOutput{}, err
		}
		return nil, listPRsOutput{PullRequests: prs}, nil
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "get_pull_request",
		Description: "Get a single pull request by number. Includes computed mergeable status and conflict details.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getPRInput) (*mcp.CallToolResult, getPROutput, error) {
		if err := authorizePR(ctx, in.Namespace, in.ProjectName, authz.LevelRead); err != nil {
			return nil, getPROutput{}, fmt.Errorf("access denied")
		}
		prStore, err := getPRStore(gitStore.BaseDir(), in.Namespace, in.ProjectName)
		if err != nil {
			return nil, getPROutput{}, err
		}
		p, err := prStore.Get(in.Number)
		if err != nil {
			return nil, getPROutput{}, err
		}
		if p.OrderFiles == nil {
			p.OrderFiles = []string{}
		}
		if p.ResultFiles == nil {
			p.ResultFiles = []string{}
		}
		if p.ReviewFiles == nil {
			p.ReviewFiles = []string{}
		}
		out := getPROutput{PullRequest: p}
		if p.State == pr.StateInterrupted {
			out.InterruptedPreviousStatus = string(p.PreviousState)
		}
		switch p.State {
		case pr.StateMerged:
			out.IsMergeable = true
		case pr.StateClosed:
			// leave IsMergeable as false
		default:
			result, mergeErr := computeMergeResult(gitStore, in.Namespace, in.ProjectName, p.SourceBranch, p.TargetBranch)
			if mergeErr == nil {
				out.IsMergeable = result.Clean
				for _, c := range result.Conflicts {
					out.Conflicts = append(out.Conflicts, conflictInfoWire{Path: c.Path, Type: c.Type})
				}
			}
		}
		return nil, out, nil
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "approve_pull_request",
		Description: "Approve an open pull request. Fails if the PR has conflicts or is not in 'open' state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in approvePRInput) (*mcp.CallToolResult, approvePROutput, error) {
		if err := authorizePR(ctx, in.Namespace, in.ProjectName, authz.LevelWrite); err != nil {
			return nil, approvePROutput{}, fmt.Errorf("access denied")
		}
		prStore, err := getPRStore(gitStore.BaseDir(), in.Namespace, in.ProjectName)
		if err != nil {
			return nil, approvePROutput{}, err
		}
		p, err := prStore.Get(in.Number)
		if err != nil {
			return nil, approvePROutput{}, err
		}
		if p.State != pr.StateOpen {
			return nil, approvePROutput{}, fmt.Errorf("PR #%d is in state %q, not open", in.Number, p.State)
		}
		if p.Mergeable == pr.MergeableConflict {
			return nil, approvePROutput{}, fmt.Errorf("PR #%d has merge conflicts", in.Number)
		}

		principal, _ := auth.PrincipalFrom(ctx)
		allowed := git.AllowedBranchesFromExtra(principal.ExtraPermissions)
		if len(allowed) > 0 && !git.MatchesAllowedBranches(p.TargetBranch, allowed) {
			return nil, approvePROutput{}, fmt.Errorf("token not permitted to approve PRs targeting %q", p.TargetBranch)
		}
		approver := principal.Email
		if approver == "" {
			approver = principal.Name
		}
		if approver == "" {
			approver = "anonymous"
		}

		now := time.Now()
		p.State = pr.StateApproved
		p.ApprovedBy = approver
		p.ApprovedAt = &now
		p.UpdatedAt = now
		if len(in.ReviewFiles) > 0 {
			p.ReviewFiles = in.ReviewFiles
		}
		if err := prStore.Update(p); err != nil {
			return nil, approvePROutput{}, err
		}
		go onPRApproved(ec, p)
		return nil, approvePROutput{Number: p.Number, State: string(p.State), ApprovedBy: approver}, nil
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "get_pull_request_diff",
		Description: "Get the unified diff for a pull request (source vs target).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getPRInput) (*mcp.CallToolResult, prDiffOutput, error) {
		if err := authorizePR(ctx, in.Namespace, in.ProjectName, authz.LevelRead); err != nil {
			return nil, prDiffOutput{}, fmt.Errorf("access denied")
		}
		prStore, err := getPRStore(gitStore.BaseDir(), in.Namespace, in.ProjectName)
		if err != nil {
			return nil, prDiffOutput{}, err
		}
		p, err := prStore.Get(in.Number)
		if err != nil {
			return nil, prDiffOutput{}, err
		}

		repo, err := gitStore.OpenRepo(in.Namespace, in.ProjectName)
		if err != nil {
			return nil, prDiffOutput{}, err
		}

		diff, files, err := git.PRDiff(repo, p.SourceBranch, p.TargetBranch)
		if err != nil {
			return nil, prDiffOutput{}, err
		}
		return nil, prDiffOutput{Diff: diff, Files: files}, nil
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "get_pull_request_files",
		Description: "List the changed files in a pull request.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getPRInput) (*mcp.CallToolResult, prFilesOutput, error) {
		if err := authorizePR(ctx, in.Namespace, in.ProjectName, authz.LevelRead); err != nil {
			return nil, prFilesOutput{}, fmt.Errorf("access denied")
		}
		prStore, err := getPRStore(gitStore.BaseDir(), in.Namespace, in.ProjectName)
		if err != nil {
			return nil, prFilesOutput{}, err
		}
		p, err := prStore.Get(in.Number)
		if err != nil {
			return nil, prFilesOutput{}, err
		}

		repo, err := gitStore.OpenRepo(in.Namespace, in.ProjectName)
		if err != nil {
			return nil, prFilesOutput{}, err
		}

		_, files, err := git.PRDiff(repo, p.SourceBranch, p.TargetBranch)
		if err != nil {
			return nil, prFilesOutput{}, err
		}
		return nil, prFilesOutput{Files: files}, nil
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "reject_pull_request",
		Description: "Reject an open pull request with a reason. Fires on_rejected event hook if configured.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in rejectPRInput) (*mcp.CallToolResult, rejectPROutput, error) {
		if err := authorizePR(ctx, in.Namespace, in.ProjectName, authz.LevelWrite); err != nil {
			return nil, rejectPROutput{}, fmt.Errorf("access denied")
		}
		prStore, err := getPRStore(gitStore.BaseDir(), in.Namespace, in.ProjectName)
		if err != nil {
			return nil, rejectPROutput{}, err
		}
		p, err := prStore.Get(in.Number)
		if err != nil {
			return nil, rejectPROutput{}, err
		}
		if p.State != pr.StateOpen {
			return nil, rejectPROutput{}, fmt.Errorf("PR #%d is in state %q, not open", in.Number, p.State)
		}
		p.State = pr.StateRejected
		p.UpdatedAt = time.Now()
		p.RejectionReason = in.Reason
		if len(in.ReviewFiles) > 0 {
			p.ReviewFiles = in.ReviewFiles
		}
		if err := prStore.Update(p); err != nil {
			return nil, rejectPROutput{}, err
		}
		releasePRSlotAndDequeue(ec, in.Namespace, in.ProjectName, int(p.Number))
		go onPRRejected(ec, p)
		return nil, rejectPROutput{Number: p.Number, State: string(p.State)}, nil
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "retry_pr_agent",
		Description: "Re-spawn the agent that failed on an interrupted PR. Clears interrupted state, restores previous status, then re-spawns. Admin only.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in retryPRAgentInput) (*mcp.CallToolResult, retryPRAgentOutput, error) {
		principal, hasPrincipal := auth.PrincipalFrom(ctx)
		if hasPrincipal {
			if err := authz.Authorize(principal.Scope, in.Namespace, "", authz.LevelAdmin); err != nil {
				return nil, retryPRAgentOutput{}, fmt.Errorf("admin access required")
			}
		}

		prStore, err := getPRStore(gitStore.BaseDir(), in.Namespace, in.ProjectName)
		if err != nil {
			return nil, retryPRAgentOutput{}, err
		}
		p, err := prStore.Get(in.Number)
		if err != nil {
			return nil, retryPRAgentOutput{}, err
		}
		if p.State != pr.StateInterrupted {
			return nil, retryPRAgentOutput{}, fmt.Errorf("PR #%d is not interrupted (state: %q)", in.Number, p.State)
		}

		interruptInfo := p.InterruptInfo
		previousState := p.PreviousState

		ensureNoActiveToken(ec, in.Namespace, in.ProjectName, int(in.Number))

		p.State = previousState
		p.PreviousState = ""
		p.InterruptInfo = nil
		p.UpdatedAt = time.Now()
		if err := prStore.Update(p); err != nil {
			return nil, retryPRAgentOutput{}, err
		}

		role := ""
		if interruptInfo != nil {
			role = interruptInfo.AgentRole
		}
		if role == "" {
			role = "reviewer"
		}

		global, _ := ec.integrityHS.GetGlobalPREventSettings()
		project, _ := ec.integrityHS.GetProjectPREventSettings(in.Namespace, in.ProjectName)

		var globalAction, projectAction *integrity.EventAction
		switch role {
		case "reviewer":
			if global != nil {
				globalAction = global.OnCreated
			}
			if project != nil {
				projectAction = project.OnCreated
			}
		case "coder":
			if global != nil {
				globalAction = global.OnRejected
			}
			if project != nil {
				projectAction = project.OnRejected
			}
		case "merger":
			if global != nil {
				globalAction = global.OnMergeConflict
			}
			if project != nil {
				projectAction = project.OnMergeConflict
			}
		}

		action := integrity.ResolveEventAction(projectAction, globalAction)
		if interruptInfo != nil && interruptInfo.AgentName != "" {
			action.AgentName = interruptInfo.AgentName
		}
		action.AgentEnabled = true

		go spawnAgentForPR(ec, action, p, role)

		return nil, retryPRAgentOutput{Number: p.Number, State: string(p.State), Message: "agent re-spawned"}, nil
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "dismiss_pr_interrupt",
		Description: "Clear interrupted state on a PR without re-spawning the agent. Restores previous status for manual handling. Admin only.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in dismissPRInterruptInput) (*mcp.CallToolResult, dismissPRInterruptOutput, error) {
		principal, hasPrincipal := auth.PrincipalFrom(ctx)
		if hasPrincipal {
			if err := authz.Authorize(principal.Scope, in.Namespace, "", authz.LevelAdmin); err != nil {
				return nil, dismissPRInterruptOutput{}, fmt.Errorf("admin access required")
			}
		}

		prStore, err := getPRStore(gitStore.BaseDir(), in.Namespace, in.ProjectName)
		if err != nil {
			return nil, dismissPRInterruptOutput{}, err
		}
		p, err := prStore.Get(in.Number)
		if err != nil {
			return nil, dismissPRInterruptOutput{}, err
		}
		if p.State != pr.StateInterrupted {
			return nil, dismissPRInterruptOutput{}, fmt.Errorf("PR #%d is not interrupted (state: %q)", in.Number, p.State)
		}

		ensureNoActiveToken(ec, in.Namespace, in.ProjectName, int(in.Number))

		p.State = p.PreviousState
		p.PreviousState = ""
		p.InterruptInfo = nil
		p.UpdatedAt = time.Now()
		if err := prStore.Update(p); err != nil {
			return nil, dismissPRInterruptOutput{}, err
		}

		return nil, dismissPRInterruptOutput{Number: p.Number, State: string(p.State), Message: "interrupt dismissed"}, nil
	})
}

type createPRInput struct {
	Namespace    string   `json:"namespace" jsonschema:"the namespace"`
	ProjectName  string   `json:"project_name" jsonschema:"required,the project name"`
	SourceBranch string   `json:"source_branch" jsonschema:"required,the source branch"`
	TargetBranch string   `json:"target_branch,omitempty" jsonschema:"the target branch (defaults to HEAD / default branch)"`
	Title        string   `json:"title" jsonschema:"required,the PR title"`
	Description  string   `json:"description,omitempty" jsonschema:"optional description"`
	OrderFiles   []string `json:"order_files,omitempty" jsonschema:"optional B-47 absolute paths for order/instruction files"`
	ResultFiles  []string `json:"result_files,omitempty" jsonschema:"optional B-47 absolute paths for result/report files"`
}

type createPROutput struct {
	Number uint32 `json:"number"`
	State  string `json:"state"`
}

type listPRsInput struct {
	Namespace   string `json:"namespace" jsonschema:"the namespace"`
	ProjectName string `json:"project_name" jsonschema:"required,the project name"`
	State       string `json:"state,omitempty" jsonschema:"filter by state (open/approved/rejected/merged/closed/interrupted); empty=all"`
}

type listPRsOutput struct {
	PullRequests []pr.PullRequest `json:"pull_requests"`
}

type getPRInput struct {
	Namespace   string `json:"namespace" jsonschema:"the namespace"`
	ProjectName string `json:"project_name" jsonschema:"required,the project name"`
	Number      uint32 `json:"number" jsonschema:"required,the PR number"`
}

type approvePRInput struct {
	Namespace   string   `json:"namespace" jsonschema:"the namespace"`
	ProjectName string   `json:"project_name" jsonschema:"required,the project name"`
	Number      uint32   `json:"number" jsonschema:"required,the PR number"`
	ReviewFiles []string `json:"review_files,omitempty" jsonschema:"optional B-47 absolute paths for review files"`
}

type approvePROutput struct {
	Number     uint32 `json:"number"`
	State      string `json:"state"`
	ApprovedBy string `json:"approved_by"`
}

type getPROutput struct {
	*pr.PullRequest
	IsMergeable              bool               `json:"mergeable"`
	Conflicts                []conflictInfoWire `json:"conflicts,omitempty"`
	InterruptedPreviousStatus string            `json:"interrupted_previous_status,omitempty"`
}

type rejectPRInput struct {
	Namespace   string   `json:"namespace" jsonschema:"the namespace"`
	ProjectName string   `json:"project_name" jsonschema:"required,the project name"`
	Number      uint32   `json:"number" jsonschema:"required,the PR number"`
	Reason      string   `json:"reason,omitempty" jsonschema:"optional rejection reason"`
	ReviewFiles []string `json:"review_files,omitempty" jsonschema:"optional B-47 absolute paths for review files"`
}

type rejectPROutput struct {
	Number uint32 `json:"number"`
	State  string `json:"state"`
}

type retryPRAgentInput struct {
	Namespace   string `json:"namespace" jsonschema:"the namespace"`
	ProjectName string `json:"project_name" jsonschema:"required,the project name"`
	Number      uint32 `json:"number" jsonschema:"required,the PR number"`
}

type retryPRAgentOutput struct {
	Number  uint32 `json:"number"`
	State   string `json:"state"`
	Message string `json:"message"`
}

type dismissPRInterruptInput struct {
	Namespace   string `json:"namespace" jsonschema:"the namespace"`
	ProjectName string `json:"project_name" jsonschema:"required,the project name"`
	Number      uint32 `json:"number" jsonschema:"required,the PR number"`
}

type dismissPRInterruptOutput struct {
	Number  uint32 `json:"number"`
	State   string `json:"state"`
	Message string `json:"message"`
}

type conflictInfoWire struct {
	Path string `json:"path"`
	Type string `json:"type"`
}

type prDiffOutput struct {
	Diff  string         `json:"diff"`
	Files []git.FileChange `json:"files"`
}

type prFilesOutput struct {
	Files []git.FileChange `json:"files"`
}

func computeMergeResult(gitStore *git.Store, namespace, project, sourceBranch, targetBranch string) (*git.MergeResult, error) {
	repo, err := gitStore.OpenRepo(namespace, project)
	if err != nil {
		return nil, err
	}
	sourceHash, err := git.ResolveBranch(repo, sourceBranch)
	if err != nil {
		return nil, err
	}
	targetHash, _ := git.ResolveBranch(repo, targetBranch)
	return git.ComputeMerge(repo, targetHash, sourceHash)
}

// --- PR WebSocket handler ---

const (
	MsgPRMergeable       uiws.MessageType = "PR_MERGEABLE"
	MsgPRList            uiws.MessageType = "PR_LIST"
	MsgPRGet             uiws.MessageType = "PR_GET"
	MsgPRMerge           uiws.MessageType = "PR_MERGE"
	MsgPRClose           uiws.MessageType = "PR_CLOSE"
	MsgPRRetryAgent      uiws.MessageType = "PR_RETRY_AGENT"
	MsgPRDismissInterrupt uiws.MessageType = "PR_DISMISS_INTERRUPT"
	MsgPROperatorReject  uiws.MessageType = "PR_OPERATOR_REJECT"
	MsgAgentList         uiws.MessageType = "AGENT_LIST"
	MsgPRQueueGet        uiws.MessageType = "PR_QUEUE_GET"
	MsgPRReview          uiws.MessageType = "PR_REVIEW"
)

var PRLevels = map[uiws.MessageType]uiws.Op{
	MsgPRMergeable:        {Level: authz.LevelRead, Global: false},
	MsgPRList:             {Level: authz.LevelRead, Global: false},
	MsgPRGet:              {Level: authz.LevelRead, Global: false},
	MsgPRMerge:            {Level: authz.LevelAdmin, Global: false},
	MsgPRClose:            {Level: authz.LevelAdmin, Global: false},
	MsgPRRetryAgent:       {Level: authz.LevelAdmin, Global: false},
	MsgPRDismissInterrupt: {Level: authz.LevelAdmin, Global: false},
	MsgPROperatorReject:   {Level: authz.LevelAdmin, Global: false},
	MsgAgentList:          {Level: authz.LevelRead, Global: true},
	MsgPRQueueGet:         {Level: authz.LevelRead, Global: false},
	MsgPRReview:           {Level: authz.LevelAdmin, Global: false},
}

func prDispatch(c *uiws.Client, gitStore *git.Store, ec *eventContext, msgType uiws.MessageType, payload json.RawMessage) bool {
	switch msgType {
	case MsgPRMergeable:
		handlePRMergeable(c, gitStore, payload)
	case MsgPRList:
		handlePRList(c, gitStore, payload)
	case MsgPRGet:
		handlePRGet(c, gitStore, payload)
	case MsgPRMerge:
		handlePRMerge(c, gitStore, ec, payload)
	case MsgPRClose:
		handlePRClose(c, gitStore, ec, payload)
	case MsgPRRetryAgent:
		handlePRRetryAgent(c, gitStore, ec, payload)
	case MsgPRDismissInterrupt:
		handlePRDismissInterrupt(c, gitStore, ec, payload)
	case MsgPROperatorReject:
		handlePROperatorReject(c, gitStore, ec, payload)
	case MsgAgentList:
		handleAgentList(c, ec)
	case MsgPRQueueGet:
		handlePRQueueGet(c, ec, payload)
	case MsgPRReview:
		handlePRReview(c, gitStore, ec, payload)
	default:
		return false
	}
	return true
}

type prMergeablePayload struct {
	Namespace   string `json:"namespace"`
	ProjectName string `json:"projectName"`
	PRNumber    uint32 `json:"prNumber"`
}

func handlePRMergeable(c *uiws.Client, gitStore *git.Store, payload json.RawMessage) {
	var p prMergeablePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	prStore, err := getPRStore(gitStore.BaseDir(), p.Namespace, p.ProjectName)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	pullReq, err := prStore.Get(p.PRNumber)
	if err != nil {
		c.SendError(err.Error())
		return
	}

	if pullReq.State == pr.StateMerged {
		c.SendResponse(MsgPRMergeable, map[string]interface{}{"mergeable": true, "conflicts": []conflictInfoWire{}})
		return
	}
	if pullReq.State == pr.StateClosed {
		c.SendResponse(MsgPRMergeable, map[string]interface{}{"mergeable": false, "conflicts": []conflictInfoWire{}})
		return
	}

	result, err := computeMergeResult(gitStore, p.Namespace, p.ProjectName, pullReq.SourceBranch, pullReq.TargetBranch)
	if err != nil {
		c.SendError(err.Error())
		return
	}

	var conflicts []conflictInfoWire
	for _, cf := range result.Conflicts {
		conflicts = append(conflicts, conflictInfoWire{Path: cf.Path, Type: cf.Type})
	}
	if conflicts == nil {
		conflicts = []conflictInfoWire{}
	}

	c.SendResponse(MsgPRMergeable, map[string]interface{}{
		"mergeable": result.Clean,
		"conflicts": conflicts,
	})
}

// --- PR_LIST ---

type prListPayload struct {
	Namespace   string `json:"namespace"`
	ProjectName string `json:"projectName"`
	State       string `json:"state,omitempty"`
}

func handlePRList(c *uiws.Client, gitStore *git.Store, payload json.RawMessage) {
	var p prListPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	prStore, err := getPRStore(gitStore.BaseDir(), p.Namespace, p.ProjectName)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	prs, err := prStore.List(pr.PRState(p.State))
	if err != nil {
		c.SendError(err.Error())
		return
	}
	if prs == nil {
		prs = []pr.PullRequest{}
	}
	c.SendResponse(MsgPRList, map[string]interface{}{"pull_requests": prs})
}

// --- PR_GET ---

type prGetPayload struct {
	Namespace   string `json:"namespace"`
	ProjectName string `json:"projectName"`
	Number      uint32 `json:"number"`
}

func handlePRGet(c *uiws.Client, gitStore *git.Store, payload json.RawMessage) {
	var p prGetPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	prStore, err := getPRStore(gitStore.BaseDir(), p.Namespace, p.ProjectName)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	pullReq, err := prStore.Get(p.Number)
	if err != nil {
		c.SendError(err.Error())
		return
	}

	if pullReq.OrderFiles == nil {
		pullReq.OrderFiles = []string{}
	}
	if pullReq.ResultFiles == nil {
		pullReq.ResultFiles = []string{}
	}
	if pullReq.ReviewFiles == nil {
		pullReq.ReviewFiles = []string{}
	}

	resp := map[string]interface{}{
		"pull_request": pullReq,
	}

	if pullReq.State == pr.StateInterrupted {
		resp["interrupted_previous_status"] = string(pullReq.PreviousState)
	}

	switch pullReq.State {
	case pr.StateMerged:
		resp["mergeable"] = true
		resp["conflicts"] = []conflictInfoWire{}
	case pr.StateClosed:
		// leave mergeable unset
	default:
		result, mergeErr := computeMergeResult(gitStore, p.Namespace, p.ProjectName, pullReq.SourceBranch, pullReq.TargetBranch)
		if mergeErr == nil {
			resp["mergeable"] = result.Clean
			conflicts := make([]conflictInfoWire, 0, len(result.Conflicts))
			for _, cf := range result.Conflicts {
				conflicts = append(conflicts, conflictInfoWire{Path: cf.Path, Type: cf.Type})
			}
			resp["conflicts"] = conflicts
		}
	}

	c.SendResponse(MsgPRGet, resp)
}

// --- PR_MERGE ---

type prMergePayload struct {
	Namespace   string `json:"namespace"`
	ProjectName string `json:"projectName"`
	Number      uint32 `json:"number"`
}

func handlePRMerge(c *uiws.Client, gitStore *git.Store, ec *eventContext, payload json.RawMessage) {
	var p prMergePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	prStore, err := getPRStore(gitStore.BaseDir(), p.Namespace, p.ProjectName)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	pullReq, err := prStore.Get(p.Number)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	if pullReq.State != pr.StateApproved && pullReq.State != pr.StateMergeConflict {
		c.SendError(fmt.Sprintf("PR #%d is in state %q, not approved or merge_conflict", p.Number, pullReq.State))
		return
	}

	repo, err := gitStore.OpenRepo(p.Namespace, p.ProjectName)
	if err != nil {
		c.SendError(fmt.Sprintf("open repo: %v", err))
		return
	}

	sourceHash, err := git.ResolveBranch(repo, pullReq.SourceBranch)
	if err != nil {
		c.SendError(fmt.Sprintf("resolve source: %v", err))
		return
	}
	targetHash, _ := git.ResolveBranch(repo, pullReq.TargetBranch)

	mergeResult, err := git.ComputeMerge(repo, targetHash, sourceHash)
	if err != nil {
		c.SendError(fmt.Sprintf("compute merge: %v", err))
		return
	}
	if !mergeResult.Clean {
		pullReq.State = pr.StateMergeConflict
		pullReq.Mergeable = pr.MergeableConflict
		pullReq.UpdatedAt = time.Now()
		_ = prStore.Update(pullReq)
		go onPRMergeConflict(ec, pullReq)
		conflicts := make([]conflictInfoWire, 0, len(mergeResult.Conflicts))
		for _, cf := range mergeResult.Conflicts {
			conflicts = append(conflicts, conflictInfoWire{Path: cf.Path, Type: cf.Type})
		}
		c.SendResponse(MsgPRMerge, map[string]interface{}{
			"error":     fmt.Sprintf("PR #%d has merge conflicts", p.Number),
			"conflicts": conflicts,
		})
		return
	}

	var mergeHash plumbing.Hash
	if targetHash == plumbing.ZeroHash {
		mergeHash = sourceHash
		if err := git.CreateBranchRef(repo, pullReq.TargetBranch, sourceHash); err != nil {
			c.SendError(fmt.Sprintf("create target ref: %v", err))
			return
		}
	} else {
		msg := fmt.Sprintf("Merge pull request #%d: %s\n\nMerge %s into %s", pullReq.Number, pullReq.Title, pullReq.SourceBranch, pullReq.TargetBranch)
		mergeHash, err = git.MergeCommitFromTree(repo, mergeResult.TreeHash, targetHash, sourceHash, msg, "GitCote", "gitcote@localhost")
		if err != nil {
			c.SendError(fmt.Sprintf("create merge commit: %v", err))
			return
		}
		if err := git.UpdateBranchRef(repo, pullReq.TargetBranch, mergeHash, targetHash); err != nil {
			c.SendError(fmt.Sprintf("update target ref: %v", err))
			return
		}
	}

	recordHeadHash(gitStore, p.Namespace, p.ProjectName)

	now := time.Now()
	pullReq.State = pr.StateMerged
	pullReq.MergeCommit = mergeHash.String()
	pullReq.MergedAt = &now
	pullReq.UpdatedAt = now
	_ = prStore.Update(pullReq)

	sourceBranchDeleted := false
	if delErr := git.DeleteBranchRef(repo, pullReq.SourceBranch); delErr == nil {
		sourceBranchDeleted = true
	}
	pullReq.SourceBranchDeleted = sourceBranchDeleted
	_ = prStore.Update(pullReq)

	invalidateApprovalsForPush(gitStore, slog.Default(), p.Namespace, p.ProjectName)

	releasePRSlotAndDequeue(ec, p.Namespace, p.ProjectName, int(pullReq.Number))

	sc := ec.seedCtx
	if sc != nil {
		go triggerOnMergePush(sc, p.Namespace, p.ProjectName, pullReq.TargetBranch)
	}

	c.SendResponse(MsgPRMerge, map[string]interface{}{
		"number":                pullReq.Number,
		"state":                 string(pullReq.State),
		"merge_commit":          mergeHash.String(),
		"source_branch_deleted": sourceBranchDeleted,
	})
}

// --- PR_CLOSE ---

// closePR is the core logic for closing a PR.
func closePR(gitStore *git.Store, ec *eventContext, ns, proj string, number uint32) (*pr.PullRequest, error) {
	prStore, err := getPRStore(gitStore.BaseDir(), ns, proj)
	if err != nil {
		return nil, err
	}
	pullReq, err := prStore.Get(number)
	if err != nil {
		return nil, err
	}
	if pullReq.State == pr.StateMerged || pullReq.State == pr.StateClosed {
		return nil, fmt.Errorf("PR #%d is already %s", number, pullReq.State)
	}
	now := time.Now()
	pullReq.State = pr.StateClosed
	pullReq.ClosedAt = &now
	pullReq.UpdatedAt = now
	if err := prStore.Update(pullReq); err != nil {
		return nil, err
	}

	releasePRSlotAndDequeue(ec, ns, proj, int(pullReq.Number))
	return pullReq, nil
}

func handlePRClose(c *uiws.Client, gitStore *git.Store, ec *eventContext, payload json.RawMessage) {
	var p prMergePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	pullReq, err := closePR(gitStore, ec, p.Namespace, p.ProjectName, p.Number)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	c.SendResponse(MsgPRClose, map[string]interface{}{
		"number": pullReq.Number,
		"state":  string(pullReq.State),
	})
}

// --- PR_RETRY_AGENT ---

type prRetryPayload struct {
	Namespace   string `json:"namespace"`
	ProjectName string `json:"projectName"`
	Number      uint32 `json:"number"`
	AgentName   string `json:"agentName,omitempty"`
}

func handlePRRetryAgent(c *uiws.Client, gitStore *git.Store, ec *eventContext, payload json.RawMessage) {
	var p prRetryPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	prStore, err := getPRStore(gitStore.BaseDir(), p.Namespace, p.ProjectName)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	pullReq, err := prStore.Get(p.Number)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	if pullReq.State != pr.StateInterrupted {
		c.SendError(fmt.Sprintf("PR #%d is not interrupted (state: %q)", p.Number, pullReq.State))
		return
	}

	interruptInfo := pullReq.InterruptInfo
	previousState := pullReq.PreviousState

	ensureNoActiveToken(ec, p.Namespace, p.ProjectName, int(p.Number))

	pullReq.State = previousState
	pullReq.PreviousState = ""
	pullReq.InterruptInfo = nil
	pullReq.UpdatedAt = time.Now()
	if err := prStore.Update(pullReq); err != nil {
		c.SendError(err.Error())
		return
	}

	role := ""
	if interruptInfo != nil {
		role = interruptInfo.AgentRole
	}
	if role == "" {
		role = "reviewer"
	}

	global, _ := ec.integrityHS.GetGlobalPREventSettings()
	project, _ := ec.integrityHS.GetProjectPREventSettings(p.Namespace, p.ProjectName)

	var globalAction, projectAction *integrity.EventAction
	switch role {
	case "reviewer":
		if global != nil {
			globalAction = global.OnCreated
		}
		if project != nil {
			projectAction = project.OnCreated
		}
	case "coder":
		if global != nil {
			globalAction = global.OnRejected
		}
		if project != nil {
			projectAction = project.OnRejected
		}
	case "merger":
		if global != nil {
			globalAction = global.OnMergeConflict
		}
		if project != nil {
			projectAction = project.OnMergeConflict
		}
	}

	action := integrity.ResolveEventAction(projectAction, globalAction)
	if p.AgentName != "" {
		action.AgentName = p.AgentName
	} else if interruptInfo != nil && interruptInfo.AgentName != "" {
		action.AgentName = interruptInfo.AgentName
	}
	action.AgentEnabled = true

	go spawnAgentForPR(ec, action, pullReq, role)

	c.SendResponse(MsgPRRetryAgent, map[string]interface{}{
		"number":  pullReq.Number,
		"state":   string(pullReq.State),
		"message": "agent re-spawned",
	})
}

// --- PR_DISMISS_INTERRUPT ---

type prDismissPayload struct {
	Namespace   string `json:"namespace"`
	ProjectName string `json:"projectName"`
	Number      uint32 `json:"number"`
}

func handlePRDismissInterrupt(c *uiws.Client, gitStore *git.Store, ec *eventContext, payload json.RawMessage) {
	var p prDismissPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	prStore, err := getPRStore(gitStore.BaseDir(), p.Namespace, p.ProjectName)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	pullReq, err := prStore.Get(p.Number)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	if pullReq.State != pr.StateInterrupted {
		c.SendError(fmt.Sprintf("PR #%d is not interrupted (state: %q)", p.Number, pullReq.State))
		return
	}

	ensureNoActiveToken(ec, p.Namespace, p.ProjectName, int(p.Number))

	pullReq.State = pullReq.PreviousState
	pullReq.PreviousState = ""
	pullReq.InterruptInfo = nil
	pullReq.UpdatedAt = time.Now()
	if err := prStore.Update(pullReq); err != nil {
		c.SendError(err.Error())
		return
	}

	c.SendResponse(MsgPRDismissInterrupt, map[string]interface{}{
		"number":  pullReq.Number,
		"state":   string(pullReq.State),
		"message": "interrupt dismissed",
	})
}

// --- PR_OPERATOR_REJECT ---

type prOperatorRejectPayload struct {
	Namespace   string `json:"namespace"`
	ProjectName string `json:"projectName"`
	PRNumber    uint32 `json:"prNumber"`
	Reason      string `json:"reason"`
}

// operatorRejectPR is the core logic for operator-initiated PR rejection (2nd stage).
// Returns the rejected PR on success.
func operatorRejectPR(gitStore *git.Store, ec *eventContext, ns, proj string, prNumber uint32, reason string) (*pr.PullRequest, error) {
	prStore, err := getPRStore(gitStore.BaseDir(), ns, proj)
	if err != nil {
		return nil, err
	}
	pullReq, err := prStore.Get(prNumber)
	if err != nil {
		return nil, err
	}
	if pullReq.State != pr.StateOpen && pullReq.State != pr.StateApproved {
		return nil, fmt.Errorf("PR #%d is in state %q (can only reject open or approved PRs)", prNumber, pullReq.State)
	}

	pullReq.State = pr.StateRejected
	pullReq.UpdatedAt = time.Now()
	pullReq.RejectionReason = reason
	if err := prStore.Update(pullReq); err != nil {
		return nil, err
	}

	releasePRSlotAndDequeue(ec, ns, proj, int(pullReq.Number))

	var msg strings.Builder
	fmt.Fprintf(&msg, "[GitCote] PR rejected by operator: %s/%s PR #%d", ns, proj, pullReq.Number)
	if reason != "" {
		fmt.Fprintf(&msg, "\nReason: %s", reason)
	}
	if len(pullReq.ReviewFiles) > 0 {
		fmt.Fprintf(&msg, "\nReview files: %s", strings.Join(pullReq.ReviewFiles, ", "))
	}
	if len(pullReq.OrderFiles) > 0 {
		fmt.Fprintf(&msg, "\nOrder files: %s", strings.Join(pullReq.OrderFiles, ", "))
	}
	if len(pullReq.ResultFiles) > 0 {
		fmt.Fprintf(&msg, "\nResult files: %s", strings.Join(pullReq.ResultFiles, ", "))
	}
	notify("log", msg.String(), ns, proj, pullReq.Number, ec.logger)

	return pullReq, nil
}

func handlePROperatorReject(c *uiws.Client, gitStore *git.Store, ec *eventContext, payload json.RawMessage) {
	var p prOperatorRejectPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	pullReq, err := operatorRejectPR(gitStore, ec, p.Namespace, p.ProjectName, p.PRNumber, p.Reason)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	c.SendResponse(MsgPROperatorReject, map[string]interface{}{
		"number": pullReq.Number,
		"state":  string(pullReq.State),
	})
}

// --- PR_QUEUE_GET ---

type prQueueGetPayload struct {
	Namespace   string `json:"namespace"`
	ProjectName string `json:"projectName"`
}

func handlePRQueueGet(c *uiws.Client, ec *eventContext, payload json.RawMessage) {
	var p prQueueGetPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}
	if ec.integrityHS == nil {
		c.SendResponse(MsgPRQueueGet, map[string]interface{}{
			"active_pr": 0,
			"waiting":   []int{},
		})
		return
	}
	q, err := ec.integrityHS.GetPRQueue(p.Namespace, p.ProjectName)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	c.SendResponse(MsgPRQueueGet, map[string]interface{}{
		"active_pr": q.ActivePR,
		"waiting":   q.Waiting,
	})
}

// --- AGENT_LIST ---

func handleAgentList(c *uiws.Client, ec *eventContext) {
	if !ec.agentCfg.IsEnabled() {
		c.SendResponse(MsgAgentList, map[string]interface{}{"agents": []interface{}{}})
		return
	}
	agentsRoot := ec.agentCfg.EffectiveAgentsRoot()
	configs, err := agent.ScanAgentConfigs(agentsRoot)
	if err != nil {
		c.SendError(fmt.Sprintf("scan agent configs: %v", err))
		return
	}
	agents := make([]map[string]interface{}, 0, len(configs))
	for _, cfg := range configs {
		agents = append(agents, map[string]interface{}{
			"name":         cfg.DirName,
			"role":         cfg.Role,
			"display_name": cfg.DisplayName,
		})
	}
	c.SendResponse(MsgAgentList, map[string]interface{}{"agents": agents})
}

// --- PR_REVIEW ---

type prReviewPayload struct {
	Namespace   string `json:"namespace"`
	ProjectName string `json:"projectName"`
	Number      uint32 `json:"number"`
}

func handlePRReview(c *uiws.Client, gitStore *git.Store, ec *eventContext, payload json.RawMessage) {
	var p prReviewPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		c.SendError("invalid payload")
		return
	}

	prStore, err := getPRStore(gitStore.BaseDir(), p.Namespace, p.ProjectName)
	if err != nil {
		c.SendError(err.Error())
		return
	}
	prObj, err := prStore.Get(p.Number)
	if err != nil {
		c.SendError(fmt.Sprintf("PR #%d not found", p.Number))
		return
	}
	if prObj.State != pr.StateOpen {
		c.SendError(fmt.Sprintf("PR #%d is not open (state: %s)", p.Number, prObj.State))
		return
	}

	go onPRCreated(ec, prObj)

	c.SendResponse(MsgPRReview, map[string]interface{}{
		"number":  prObj.Number,
		"message": "review triggered",
	})
}

