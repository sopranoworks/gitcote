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

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sopranoworks/gityard/internal/git"
	"github.com/sopranoworks/gityard/internal/pr"
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
func handlePostReceive(store *git.Store, logger *slog.Logger, namespace, project string, principal auth.Principal, pushOpts []string) {
	opts := parsePushOpts(pushOpts)
	createPR := false
	target := "main"
	title := ""
	var sourceBranch string

	for k, v := range opts {
		switch k {
		case "pull_request.create":
			createPR = true
		case "pull_request.target":
			if v != "" {
				target = v
			}
		case "pull_request.title":
			title = v
		}
	}

	if !createPR {
		// Even without create, check if any existing PRs need approval invalidation
		// (source branch was updated).
		invalidateApprovalsForPush(store, logger, namespace, project)
		return
	}

	// Determine the source branch from repo refs.
	repo, err := store.OpenRepo(namespace, project)
	if err != nil {
		logger.Error("open repo for PR creation", "error", err)
		return
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
	targetHash, err := git.ResolveBranch(repo, target)
	if err != nil {
		logger.Error("resolve target branch", "error", err)
		return
	}

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
	}
	num, err := prStore.Create(newPR)
	if err != nil {
		logger.Error("create PR", "error", err)
		return
	}
	logger.Info("pr created", "number", num, "source", sourceBranch, "target", target, "mergeable", mergeable)
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
		// Check if source or target commit changed.
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
		if currentTarget.String() != p.TargetCommit {
			p.TargetCommit = currentTarget.String()
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
	targetHash, err := git.ResolveBranch(repo, targetBranch)
	if err != nil {
		return pr.MergeableUnknown
	}
	result, err := git.CheckConflicts(repo, sourceHash, targetHash)
	if err != nil {
		return pr.MergeableUnknown
	}
	if result.HasConflict {
		return pr.MergeableConflict
	}
	return pr.MergeableClean
}

// parsePushOpts converts a []string of "key=value" or bare "key" into a map.
func parsePushOpts(opts []string) map[string]string {
	m := make(map[string]string, len(opts))
	for _, o := range opts {
		if i := strings.IndexByte(o, '='); i >= 0 {
			m[o[:i]] = o[i+1:]
		} else {
			m[o] = ""
		}
	}
	return m
}

// registerPRTools registers the PR MCP tools.
func registerPRTools(mcpServer *mcp.Server, gitStore *git.Store, sc *seedContext) {
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "list_pull_requests",
		Description: "List pull requests for a repository, optionally filtered by state (open/approved/merged/closed).",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in listPRsInput) (*mcp.CallToolResult, listPRsOutput, error) {
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
	}, func(_ context.Context, _ *mcp.CallToolRequest, in getPRInput) (*mcp.CallToolResult, getPROutput, error) {
		prStore, err := getPRStore(gitStore.BaseDir(), in.Namespace, in.ProjectName)
		if err != nil {
			return nil, getPROutput{}, err
		}
		p, err := prStore.Get(in.Number)
		if err != nil {
			return nil, getPROutput{}, err
		}
		out := getPROutput{PullRequest: p}
		if p.State == pr.StateOpen || p.State == pr.StateApproved {
			result, mergeErr := computeMergeResult(gitStore, in.Namespace, in.ProjectName, p.SourceBranch, p.TargetBranch)
			if mergeErr == nil {
				out.IsMergeable = result.Clean
				for _, c := range result.Conflicts {
					out.Conflicts = append(out.Conflicts, conflictInfoWire{Path: c.Path, Type: c.Type})
				}
			}
		} else if p.State == pr.StateMerged {
			out.IsMergeable = true
		}
		return nil, out, nil
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "approve_pull_request",
		Description: "Approve an open pull request. Fails if the PR has conflicts or is not in 'open' state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in approvePRInput) (*mcp.CallToolResult, approvePROutput, error) {
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
		if err := prStore.Update(p); err != nil {
			return nil, approvePROutput{}, err
		}
		return nil, approvePROutput{Number: p.Number, State: string(p.State), ApprovedBy: approver}, nil
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "merge_pull_request",
		Description: "Merge an approved pull request. Re-computes merge status at execution time and rejects if conflicts exist.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mergePRInput) (*mcp.CallToolResult, mergePROutput, error) {
		prStore, err := getPRStore(gitStore.BaseDir(), in.Namespace, in.ProjectName)
		if err != nil {
			return nil, mergePROutput{}, err
		}
		p, err := prStore.Get(in.Number)
		if err != nil {
			return nil, mergePROutput{}, err
		}
		if p.State != pr.StateApproved {
			return nil, mergePROutput{}, fmt.Errorf("PR #%d is in state %q, not approved", in.Number, p.State)
		}

		principal, _ := auth.PrincipalFrom(ctx)
		allowed := git.AllowedBranchesFromExtra(principal.ExtraPermissions)
		if len(allowed) > 0 && !git.MatchesAllowedBranches(p.TargetBranch, allowed) {
			return nil, mergePROutput{}, fmt.Errorf("token not permitted to merge PRs targeting %q", p.TargetBranch)
		}

		repo, err := gitStore.OpenRepo(in.Namespace, in.ProjectName)
		if err != nil {
			return nil, mergePROutput{}, fmt.Errorf("open repo: %w", err)
		}

		sourceHash, err := git.ResolveBranch(repo, p.SourceBranch)
		if err != nil {
			return nil, mergePROutput{}, fmt.Errorf("resolve source: %w", err)
		}
		targetHash, err := git.ResolveBranch(repo, p.TargetBranch)
		if err != nil {
			return nil, mergePROutput{}, fmt.Errorf("resolve target: %w", err)
		}

		// Re-compute merge at execution time (never trust cached status).
		mergeResult, err := git.ComputeMerge(repo, targetHash, sourceHash)
		if err != nil {
			return nil, mergePROutput{}, fmt.Errorf("compute merge: %w", err)
		}
		if !mergeResult.Clean {
			paths := make([]string, len(mergeResult.Conflicts))
			for i, c := range mergeResult.Conflicts {
				paths[i] = c.Path
			}
			return nil, mergePROutput{}, fmt.Errorf("PR #%d has merge conflicts: %s", in.Number, strings.Join(paths, ", "))
		}

		msg := fmt.Sprintf("Merge pull request #%d: %s\n\nMerge %s into %s", p.Number, p.Title, p.SourceBranch, p.TargetBranch)
		mergeHash, err := git.MergeCommitFromTree(repo, mergeResult.TreeHash, targetHash, sourceHash, msg, "GitYard", "gityard@localhost")
		if err != nil {
			return nil, mergePROutput{}, fmt.Errorf("create merge commit: %w", err)
		}

		if err := git.UpdateBranchRef(repo, p.TargetBranch, mergeHash, targetHash); err != nil {
			return nil, mergePROutput{}, fmt.Errorf("update target ref: %w", err)
		}

		now := time.Now()
		p.State = pr.StateMerged
		p.MergeCommit = mergeHash.String()
		p.MergedAt = &now
		p.UpdatedAt = now
		if err := prStore.Update(p); err != nil {
			return nil, mergePROutput{}, err
		}

		// Delete source branch after merge.
		sourceBranchDeleted := false
		if delErr := git.DeleteBranchRef(repo, p.SourceBranch); delErr == nil {
			sourceBranchDeleted = true
		}
		p.SourceBranchDeleted = sourceBranchDeleted
		_ = prStore.Update(p)

		// Invalidate mergeable status on other PRs targeting the same branch.
		invalidateApprovalsForPush(gitStore, slog.Default(), in.Namespace, in.ProjectName)

		// On-merge push: if configured, push target branch to seed asynchronously.
		go triggerOnMergePush(sc, in.Namespace, in.ProjectName, p.TargetBranch)

		return nil, mergePROutput{Number: p.Number, State: string(p.State), MergeCommit: mergeHash.String(), SourceBranchDeleted: sourceBranchDeleted}, nil
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "get_pull_request_diff",
		Description: "Get the unified diff for a pull request (source vs target).",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in getPRInput) (*mcp.CallToolResult, prDiffOutput, error) {
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
	}, func(_ context.Context, _ *mcp.CallToolRequest, in getPRInput) (*mcp.CallToolResult, prFilesOutput, error) {
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
}

type listPRsInput struct {
	Namespace   string `json:"namespace" jsonschema:"the namespace"`
	ProjectName string `json:"project_name" jsonschema:"required,the project name"`
	State       string `json:"state,omitempty" jsonschema:"filter by state (open/approved/merged/closed); empty=all"`
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
	Namespace   string `json:"namespace" jsonschema:"the namespace"`
	ProjectName string `json:"project_name" jsonschema:"required,the project name"`
	Number      uint32 `json:"number" jsonschema:"required,the PR number"`
}

type approvePROutput struct {
	Number     uint32 `json:"number"`
	State      string `json:"state"`
	ApprovedBy string `json:"approved_by"`
}

type mergePRInput struct {
	Namespace   string `json:"namespace" jsonschema:"the namespace"`
	ProjectName string `json:"project_name" jsonschema:"required,the project name"`
	Number      uint32 `json:"number" jsonschema:"required,the PR number"`
}

type mergePROutput struct {
	Number              uint32 `json:"number"`
	State               string `json:"state"`
	MergeCommit         string `json:"merge_commit"`
	SourceBranchDeleted bool   `json:"source_branch_deleted"`
}

type getPROutput struct {
	*pr.PullRequest
	IsMergeable bool             `json:"mergeable"`
	Conflicts   []conflictInfoWire `json:"conflicts,omitempty"`
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
	targetHash, err := git.ResolveBranch(repo, targetBranch)
	if err != nil {
		return nil, err
	}
	return git.ComputeMerge(repo, targetHash, sourceHash)
}

// --- PR WebSocket handler ---

const MsgPRMergeable uiws.MessageType = "PR_MERGEABLE"

var PRLevels = map[uiws.MessageType]uiws.Op{
	MsgPRMergeable: {Level: authz.LevelRead, Global: false},
}

func prDispatch(c *uiws.Client, gitStore *git.Store, msgType uiws.MessageType, payload json.RawMessage) bool {
	switch msgType {
	case MsgPRMergeable:
		handlePRMergeable(c, gitStore, payload)
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

