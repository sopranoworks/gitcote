package git

import (
	"fmt"

	gogit "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/utils/merkletrie"
)

// FileChange describes a changed file in a PR diff.
type FileChange struct {
	Path   string `json:"path"`
	Action string `json:"action"` // "added", "modified", "deleted"
}

// PRDiff computes the unified diff and changed file list between two branches.
// When the target branch has no commits (empty base), all source files are shown as additions.
func PRDiff(repo *gogit.Repository, sourceBranch, targetBranch string) (string, []FileChange, error) {
	sourceHash, err := ResolveBranch(repo, sourceBranch)
	if err != nil {
		return "", nil, err
	}

	sourceCommit, err := repo.CommitObject(sourceHash)
	if err != nil {
		return "", nil, fmt.Errorf("source commit: %w", err)
	}
	sourceTree, err := sourceCommit.Tree()
	if err != nil {
		return "", nil, fmt.Errorf("source tree: %w", err)
	}

	targetHash, _ := ResolveBranch(repo, targetBranch)
	var targetTree *object.Tree
	if targetHash != plumbing.ZeroHash {
		targetCommit, err := repo.CommitObject(targetHash)
		if err != nil {
			return "", nil, fmt.Errorf("target commit: %w", err)
		}
		targetTree, err = targetCommit.Tree()
		if err != nil {
			return "", nil, fmt.Errorf("target tree: %w", err)
		}
	} else {
		targetTree = &object.Tree{}
	}

	changes, err := object.DiffTree(targetTree, sourceTree)
	if err != nil {
		return "", nil, fmt.Errorf("diff: %w", err)
	}

	var files []FileChange
	for _, c := range changes {
		action, _ := c.Action()
		fc := FileChange{Path: changePath(c)}
		switch action {
		case merkletrie.Insert:
			fc.Action = "added"
		case merkletrie.Delete:
			fc.Action = "deleted"
		case merkletrie.Modify:
			fc.Action = "modified"
		}
		files = append(files, fc)
	}

	patch, err := changes.Patch()
	if err != nil {
		return "", files, fmt.Errorf("patch: %w", err)
	}

	return patch.String(), files, nil
}
