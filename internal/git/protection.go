package git

import (
	"bytes"
	"fmt"
	"strings"

	gogit "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/format/pktline"
	"github.com/sopranoworks/shoka/pkg/authz"
)

// RefUpdate represents a single ref update from a receive-pack request.
type RefUpdate struct {
	OldHash plumbing.Hash
	NewHash plumbing.Hash
	RefName string
}

// ParseRefUpdates extracts ref updates from a receive-pack request body.
func ParseRefUpdates(data []byte) []RefUpdate {
	rd := bytes.NewReader(data)
	var updates []RefUpdate
	for {
		lineLen, payload, err := pktline.ReadLine(rd)
		if err != nil || lineLen == pktline.Flush {
			break
		}
		if len(payload) == 0 {
			continue
		}
		// Strip capabilities after NUL byte
		if idx := bytes.IndexByte(payload, 0); idx >= 0 {
			payload = payload[:idx]
		}
		// Strip trailing newline
		payload = bytes.TrimRight(payload, "\n")
		// Format: <old-hash> SP <new-hash> SP <ref-name>
		parts := bytes.SplitN(payload, []byte(" "), 3)
		if len(parts) != 3 {
			continue
		}
		updates = append(updates, RefUpdate{
			OldHash: plumbing.NewHash(string(parts[0])),
			NewHash: plumbing.NewHash(string(parts[1])),
			RefName: string(parts[2]),
		})
	}
	return updates
}

// CheckBranchProtection validates ref updates against branch protection rules
// and token branch prefix restrictions. It returns an error describing the
// first violation, or nil if all updates are allowed.
func CheckBranchProtection(
	repo *gogit.Repository,
	updates []RefUpdate,
	config *ProjectConfig,
	effectiveLevel authz.Level,
	allowedBranches []string,
) error {
	for _, u := range updates {
		branch := strings.TrimPrefix(u.RefName, "refs/heads/")
		if branch == u.RefName {
			continue
		}

		if config.IsProtected(branch) {
			if u.NewHash == plumbing.ZeroHash {
				return fmt.Errorf("cannot delete protected branch %q", branch)
			}

			if effectiveLevel < authz.LevelWrite {
				return fmt.Errorf("protected branch %q requires write access; use PR workflow", branch)
			}

			if u.OldHash != plumbing.ZeroHash {
				ff, err := isFastForward(repo, u.OldHash, u.NewHash)
				if err != nil || !ff {
					return fmt.Errorf("force push denied on protected branch %q", branch)
				}
			}
		}

		if len(allowedBranches) > 0 {
			matched := false
			for _, prefix := range allowedBranches {
				if strings.HasPrefix(branch, prefix) {
					matched = true
					break
				}
			}
			if !matched {
				return fmt.Errorf("token restricted to branches: %v", allowedBranches)
			}
		}
	}
	return nil
}

func isFastForward(repo *gogit.Repository, oldHash, newHash plumbing.Hash) (bool, error) {
	oldCommit, err := repo.CommitObject(oldHash)
	if err != nil {
		return false, err
	}
	newCommit, err := repo.CommitObject(newHash)
	if err != nil {
		return false, err
	}
	return oldCommit.IsAncestor(newCommit)
}

// AllowedBranchesFromExtra extracts the allowed_branches list from a
// principal's ExtraPermissions map. Returns nil if absent or empty.
func AllowedBranchesFromExtra(extra map[string]any) []string {
	if extra == nil {
		return nil
	}
	raw, ok := extra["allowed_branches"]
	if !ok || raw == nil {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, v := range arr {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}
