package git

import (
	"fmt"
	"sync"

	gogit "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/storage"

	"github.com/sopranoworks/shoka/pkg/authz"
)

// ProtectedStorer wraps a storage.Storer with branch protection checks.
// go-git's ReceivePack calls SetReference/RemoveReference when applying ref
// updates; this wrapper intercepts those calls and enforces branch protection
// (including fast-forward checks) at the point where objects are already
// available in the store.
//
// It also records every successfully-applied SetReference as a RefUpdate in
// Updates. This is the SSH transport's only source of "which ref did this
// push actually touch" — unlike the HTTP path, SSH's receive-pack handling
// doesn't pre-parse the pkt-line command list, so callers that need the real
// pushed ref (e.g. PostReceive, to resolve a PR's source branch correctly
// instead of guessing) read Updates after ReceivePack completes.
type ProtectedStorer struct {
	storage.Storer
	Repo    *gogit.Repository
	Level   authz.Level
	Allowed []string

	mu      sync.Mutex
	Updates []RefUpdate
}

func (s *ProtectedStorer) SetReference(ref *plumbing.Reference) error {
	oldHash := plumbing.ZeroHash
	if existing, err := s.Storer.Reference(ref.Name()); err == nil {
		oldHash = existing.Hash()
	}
	if err := s.checkRef(ref.Name(), oldHash, ref.Hash()); err != nil {
		return err
	}
	if err := s.Storer.SetReference(ref); err != nil {
		return err
	}
	s.mu.Lock()
	s.Updates = append(s.Updates, RefUpdate{OldHash: oldHash, NewHash: ref.Hash(), RefName: ref.Name().String()})
	s.mu.Unlock()
	return nil
}

func (s *ProtectedStorer) RemoveReference(name plumbing.ReferenceName) error {
	if name.IsBranch() && IsDefaultBranch(s.Repo, name.Short()) {
		return fmt.Errorf("cannot delete protected branch %q", name.Short())
	}
	return s.Storer.RemoveReference(name)
}

func (s *ProtectedStorer) checkRef(name plumbing.ReferenceName, oldHash, newHash plumbing.Hash) error {
	if !name.IsBranch() {
		return nil
	}
	branch := name.Short()

	if IsDefaultBranch(s.Repo, branch) {
		if newHash == plumbing.ZeroHash {
			return fmt.Errorf("cannot delete protected branch %q", branch)
		}
		if s.Level < authz.LevelWrite {
			return fmt.Errorf("protected branch %q requires write access; use PR workflow", branch)
		}
		if oldHash != plumbing.ZeroHash {
			oldCommit, cerr := s.Repo.CommitObject(oldHash)
			if cerr != nil {
				return fmt.Errorf("force push denied on protected branch %q", branch)
			}
			newCommit, cerr := s.Repo.CommitObject(newHash)
			if cerr != nil {
				return fmt.Errorf("force push denied on protected branch %q", branch)
			}
			isFF, cerr := oldCommit.IsAncestor(newCommit)
			if cerr != nil || !isFF {
				return fmt.Errorf("force push denied on protected branch %q", branch)
			}
		}
	}

	if len(s.Allowed) > 0 && !MatchesAllowedBranches(branch, s.Allowed) {
		return fmt.Errorf("token restricted to branches: %v", s.Allowed)
	}
	return nil
}
