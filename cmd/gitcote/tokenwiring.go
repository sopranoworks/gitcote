package main

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sopranoworks/gitcote/internal/git"
	"github.com/sopranoworks/shoka/pkg/auth"
	"github.com/sopranoworks/shoka/pkg/authz"
	"github.com/sopranoworks/shoka/pkg/oauthstore"
)

const gitcoteIssuedClientID = "gitcote-issued"

func checkBranchProtection(gitStore *git.Store, namespace, project string, principal auth.Principal, refUpdates []git.RefUpdate) error {
	effectiveLevel := git.EffectiveGitLevel(principal.Scope, namespace, project)
	allowedBranches := git.AllowedBranchesFromExtra(principal.ExtraPermissions)

	repo, err := gitStore.OpenRepo(namespace, project)
	if err != nil {
		return fmt.Errorf("open repo: %w", err)
	}

	return git.CheckBranchProtection(repo, refUpdates, effectiveLevel, allowedBranches)
}

func registerTokenTools(mcpServer *mcp.Server, gitStore *git.Store, oauthStore *oauthstore.Store, httpExternalURL, httpListen string) {
	if oauthStore == nil {
		return
	}

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "issue_git_token",
		Description: "Issue a scoped, branch-restricted, short-lived CLI token for git access. Requires admin permission on the namespace.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in issueGitTokenInput) (*mcp.CallToolResult, issueGitTokenOutput, error) {
		principal, hasPrincipal := auth.PrincipalFrom(ctx)
		if !hasPrincipal {
			return nil, issueGitTokenOutput{}, fmt.Errorf("authentication required")
		}
		if err := authz.Authorize(principal.Scope, in.Namespace, "", authz.LevelAdmin); err != nil {
			return nil, issueGitTokenOutput{}, fmt.Errorf("admin access required on namespace %q", in.Namespace)
		}

		ttlDur, err := time.ParseDuration(in.TTL)
		if err != nil {
			return nil, issueGitTokenOutput{}, fmt.Errorf("invalid ttl %q: %w", in.TTL, err)
		}
		if ttlDur <= 0 {
			return nil, issueGitTokenOutput{}, fmt.Errorf("ttl must be positive")
		}

		// Reject if any allowed_branches prefix matches existing branches.
		if len(in.AllowedBranches) > 0 {
			repo, rerr := gitStore.OpenRepo(in.Namespace, in.ProjectName)
			if rerr != nil {
				return nil, issueGitTokenOutput{}, fmt.Errorf("open repo: %w", rerr)
			}
			branches, berr := git.ListBranches(repo)
			if berr != nil {
				return nil, issueGitTokenOutput{}, fmt.Errorf("list branches: %w", berr)
			}
			for _, prefix := range in.AllowedBranches {
				for _, b := range branches {
					if b == prefix || git.MatchesAllowedBranches(b, []string{prefix}) {
						return nil, issueGitTokenOutput{}, fmt.Errorf("branches matching prefix %q already exist (e.g. %q)", prefix, b)
					}
				}
			}
		}

		scopeLevel := "r"
		if in.Scope == "w" || in.Scope == "rw" {
			scopeLevel = "rw"
		}
		scope := fmt.Sprintf("git/%s:%s:%s", in.Namespace, in.ProjectName, scopeLevel)

		var ep map[string]any
		if len(in.AllowedBranches) > 0 {
			branches := make([]any, len(in.AllowedBranches))
			for i, b := range in.AllowedBranches {
				branches[i] = b
			}
			ep = map[string]any{"allowed_branches": branches}
		}

		now := time.Now()
		rec, err := oauthStore.NewSeries(
			gitcoteIssuedClientID,
			oauthstore.Principal{Name: "git-token", Email: principal.Email},
			"",
			scope,
			"",
			now,
			ttlDur,
			ttlDur,
			ep,
		)
		if err != nil {
			return nil, issueGitTokenOutput{}, fmt.Errorf("issue token: %w", err)
		}

		base := httpExternalURL
		if base == "" {
			base = "http://" + httpListen
		}
		cloneURL := fmt.Sprintf("%s/%s/%s.git", base, in.Namespace, in.ProjectName)

		return nil, issueGitTokenOutput{
			Token:           rec.AccessToken,
			Scope:           scope,
			AllowedBranches: in.AllowedBranches,
			ExpiresAt:       rec.AccessExpiry.Format(time.RFC3339),
			CloneURL:        cloneURL,
		}, nil
	})
}

type issueGitTokenInput struct {
	Namespace       string   `json:"namespace" jsonschema:"required,the namespace"`
	ProjectName     string   `json:"project_name" jsonschema:"required,the project name"`
	Scope           string   `json:"scope,omitempty" jsonschema:"permission level: r (read, default) or w (read-write)"`
	AllowedBranches []string `json:"allowed_branches,omitempty" jsonschema:"optional branch prefix restrictions"`
	TTL             string   `json:"ttl" jsonschema:"required,token lifetime as a Go duration string (e.g. 1h or 30m)"`
}

type issueGitTokenOutput struct {
	Token           string   `json:"token"`
	Scope           string   `json:"scope"`
	AllowedBranches []string `json:"allowed_branches,omitempty"`
	ExpiresAt       string   `json:"expires_at"`
	CloneURL        string   `json:"clone_url"`
}
