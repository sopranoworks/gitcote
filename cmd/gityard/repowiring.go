package main

import (
	"context"
	"fmt"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sopranoworks/gityard/internal/git"
)

func registerRepoTools(mcpServer *mcp.Server, gitStore *git.Store) {
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "list_files",
		Description: "List files and directories at a path in a repository.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in listFilesInput) (*mcp.CallToolResult, listFilesOutput, error) {
		repo, err := gitStore.OpenRepo(in.Namespace, in.ProjectName)
		if err != nil {
			return nil, listFilesOutput{}, fmt.Errorf("open repo: %w", err)
		}
		hash, err := git.ResolveRef(repo, in.Ref)
		if err != nil {
			return nil, listFilesOutput{}, err
		}
		entries, err := git.ListTreeEntries(repo, hash, in.Path)
		if err != nil {
			return nil, listFilesOutput{}, err
		}
		return nil, listFilesOutput{Entries: entries}, nil
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "read_file",
		Description: "Read the contents of a file in a repository.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in readFileInput) (*mcp.CallToolResult, readFileOutput, error) {
		repo, err := gitStore.OpenRepo(in.Namespace, in.ProjectName)
		if err != nil {
			return nil, readFileOutput{}, fmt.Errorf("open repo: %w", err)
		}
		hash, err := git.ResolveRef(repo, in.Ref)
		if err != nil {
			return nil, readFileOutput{}, err
		}
		content, binary, err := git.ReadFileContent(repo, hash, in.Path)
		if err != nil {
			return nil, readFileOutput{}, err
		}
		return nil, readFileOutput{Content: content, IsBinary: binary}, nil
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "list_branches",
		Description: "List branches in a repository.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in listBranchesInput) (*mcp.CallToolResult, listBranchesOutput, error) {
		repo, err := gitStore.OpenRepo(in.Namespace, in.ProjectName)
		if err != nil {
			return nil, listBranchesOutput{}, fmt.Errorf("open repo: %w", err)
		}
		branches, err := git.GetBranches(repo)
		if err != nil {
			return nil, listBranchesOutput{}, err
		}
		return nil, listBranchesOutput{Branches: branches}, nil
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "get_log",
		Description: "View commit history of a repository.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in getLogInput) (*mcp.CallToolResult, getLogOutput, error) {
		repo, err := gitStore.OpenRepo(in.Namespace, in.ProjectName)
		if err != nil {
			return nil, getLogOutput{}, fmt.Errorf("open repo: %w", err)
		}
		hash, err := git.ResolveRef(repo, in.Ref)
		if err != nil {
			return nil, getLogOutput{}, err
		}
		entries, err := git.GetLog(repo, hash, in.Path, in.Limit)
		if err != nil {
			return nil, getLogOutput{}, err
		}
		return nil, getLogOutput{Commits: entries}, nil
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "get_commit",
		Description: "Get details of a specific commit.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in getCommitInput) (*mcp.CallToolResult, *git.CommitDetail, error) {
		repo, err := gitStore.OpenRepo(in.Namespace, in.ProjectName)
		if err != nil {
			return nil, nil, fmt.Errorf("open repo: %w", err)
		}
		hash := plumbing.NewHash(in.SHA)
		detail, err := git.GetCommitDetail(repo, hash)
		if err != nil {
			return nil, nil, err
		}
		return nil, detail, nil
	})
}

type listFilesInput struct {
	Namespace   string `json:"namespace" jsonschema:"required,the namespace"`
	ProjectName string `json:"project_name" jsonschema:"required,the project name"`
	Path        string `json:"path,omitempty" jsonschema:"path within the repository (default: root)"`
	Ref         string `json:"ref,omitempty" jsonschema:"branch name, tag, or commit SHA (default: HEAD)"`
}

type listFilesOutput struct {
	Entries []git.TreeEntryInfo `json:"entries"`
}

type readFileInput struct {
	Namespace   string `json:"namespace" jsonschema:"required,the namespace"`
	ProjectName string `json:"project_name" jsonschema:"required,the project name"`
	Path        string `json:"path" jsonschema:"required,file path within the repository"`
	Ref         string `json:"ref,omitempty" jsonschema:"branch name, tag, or commit SHA (default: HEAD)"`
}

type readFileOutput struct {
	Content  string `json:"content"`
	IsBinary bool   `json:"is_binary"`
}

type listBranchesInput struct {
	Namespace   string `json:"namespace" jsonschema:"required,the namespace"`
	ProjectName string `json:"project_name" jsonschema:"required,the project name"`
}

type listBranchesOutput struct {
	Branches []git.BranchInfo `json:"branches"`
}

type getLogInput struct {
	Namespace   string `json:"namespace" jsonschema:"required,the namespace"`
	ProjectName string `json:"project_name" jsonschema:"required,the project name"`
	Ref         string `json:"ref,omitempty" jsonschema:"branch name, tag, or commit SHA (default: HEAD)"`
	Path        string `json:"path,omitempty" jsonschema:"filter to commits touching this path"`
	Limit       int    `json:"limit,omitempty" jsonschema:"max commits to return (default: 20, max: 100)"`
}

type getLogOutput struct {
	Commits []git.LogEntry `json:"commits"`
}

type getCommitInput struct {
	Namespace   string `json:"namespace" jsonschema:"required,the namespace"`
	ProjectName string `json:"project_name" jsonschema:"required,the project name"`
	SHA         string `json:"sha" jsonschema:"required,the commit SHA"`
}
