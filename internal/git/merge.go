package git

import (
	"fmt"
	"sort"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/storer"
	"github.com/go-git/go-git/v6/utils/merkletrie"
)

// ConflictResult describes the result of a conflict check.
type ConflictResult struct {
	HasConflict     bool
	ConflictedFiles []string
	IsFastForward   bool
}

// CheckConflicts detects file-level conflicts between source and target branches.
// Returns ConflictResult with conflict status and conflicted file paths.
func CheckConflicts(repo *gogit.Repository, sourceRef, targetRef plumbing.Hash) (*ConflictResult, error) {
	sourceCommit, err := repo.CommitObject(sourceRef)
	if err != nil {
		return nil, fmt.Errorf("resolve source commit: %w", err)
	}
	targetCommit, err := repo.CommitObject(targetRef)
	if err != nil {
		return nil, fmt.Errorf("resolve target commit: %w", err)
	}

	// Fast-forward check: if target is an ancestor of source, no merge needed.
	isAncestor, err := targetCommit.IsAncestor(sourceCommit)
	if err != nil {
		return nil, fmt.Errorf("ancestry check: %w", err)
	}
	if isAncestor {
		return &ConflictResult{IsFastForward: true}, nil
	}

	bases, err := sourceCommit.MergeBase(targetCommit)
	if err != nil {
		return nil, fmt.Errorf("merge base: %w", err)
	}
	if len(bases) == 0 {
		return nil, fmt.Errorf("no common ancestor between source and target")
	}
	base := bases[0]

	baseTree, err := base.Tree()
	if err != nil {
		return nil, fmt.Errorf("base tree: %w", err)
	}
	sourceTree, err := sourceCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("source tree: %w", err)
	}
	targetTree, err := targetCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("target tree: %w", err)
	}

	// Diff base→source and base→target.
	sourceChanges, err := object.DiffTree(baseTree, sourceTree)
	if err != nil {
		return nil, fmt.Errorf("diff base→source: %w", err)
	}
	targetChanges, err := object.DiffTree(baseTree, targetTree)
	if err != nil {
		return nil, fmt.Errorf("diff base→target: %w", err)
	}

	// Build path sets from each side.
	sourcePaths := make(map[string]merkletrie.Action)
	for _, c := range sourceChanges {
		a, _ := c.Action()
		path := changePath(c)
		sourcePaths[path] = a
	}

	var conflicted []string
	for _, c := range targetChanges {
		path := changePath(c)
		if _, both := sourcePaths[path]; both {
			conflicted = append(conflicted, path)
		}
	}

	return &ConflictResult{
		HasConflict:     len(conflicted) > 0,
		ConflictedFiles: conflicted,
	}, nil
}

// MergeCommit creates a merge commit integrating source into target. Assumes
// CheckConflicts returned no conflicts. Returns the hash of the new merge commit.
//
// The merged tree is constructed by starting from the target tree (which has all
// target-side changes) and applying source-side changes on top. File-level conflict
// detection guarantees no path is modified by both sides.
func MergeCommit(repo *gogit.Repository, sourceRef, targetRef plumbing.Hash, msg, authorName, authorEmail string) (plumbing.Hash, error) {
	sourceCommit, err := repo.CommitObject(sourceRef)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("resolve source: %w", err)
	}
	targetCommit, err := repo.CommitObject(targetRef)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("resolve target: %w", err)
	}

	// Fast-forward: if target is ancestor of source, the source tree IS correct.
	isFF, err := targetCommit.IsAncestor(sourceCommit)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("ancestry check: %w", err)
	}

	var treeHash plumbing.Hash
	if isFF {
		sourceTree, err := sourceCommit.Tree()
		if err != nil {
			return plumbing.ZeroHash, err
		}
		treeHash = sourceTree.Hash
	} else {
		treeHash, err = buildMergedTree(repo, sourceCommit, targetCommit)
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("build merged tree: %w", err)
		}
	}

	now := time.Now()
	sig := object.Signature{Name: authorName, Email: authorEmail, When: now}
	commit := &object.Commit{
		Author:       sig,
		Committer:    sig,
		Message:      msg,
		TreeHash:     treeHash,
		ParentHashes: []plumbing.Hash{targetRef, sourceRef},
	}
	obj := repo.Storer.NewEncodedObject()
	if err := commit.Encode(obj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("encode commit: %w", err)
	}
	hash, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("store commit: %w", err)
	}
	return hash, nil
}

// buildMergedTree constructs a tree that combines target-side and source-side
// changes. It starts from the target tree and applies source-side diffs (computed
// against the merge base). File-level conflict detection guarantees no overlap.
func buildMergedTree(repo *gogit.Repository, sourceCommit, targetCommit *object.Commit) (plumbing.Hash, error) {
	bases, err := sourceCommit.MergeBase(targetCommit)
	if err != nil || len(bases) == 0 {
		return plumbing.ZeroHash, fmt.Errorf("merge base: %w", err)
	}
	baseTree, err := bases[0].Tree()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	sourceTree, err := sourceCommit.Tree()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	targetTree, err := targetCommit.Tree()
	if err != nil {
		return plumbing.ZeroHash, err
	}

	// Diff base→source to find what source changed.
	sourceChanges, err := object.DiffTree(baseTree, sourceTree)
	if err != nil {
		return plumbing.ZeroHash, err
	}

	// Flatten target tree into a path→entry map.
	entries := make(map[string]object.TreeEntry)
	flattenTree(targetTree, "", entries)

	// Apply source changes on top of target.
	for _, c := range sourceChanges {
		action, _ := c.Action()
		switch action {
		case merkletrie.Insert, merkletrie.Modify:
			// Source added or modified a file: use source's blob.
			f, err := sourceTree.File(c.To.Name)
			if err != nil {
				return plumbing.ZeroHash, fmt.Errorf("source file %q: %w", c.To.Name, err)
			}
			entries[c.To.Name] = object.TreeEntry{
				Name: baseName(c.To.Name),
				Mode: f.Mode,
				Hash: f.Hash,
			}
		case merkletrie.Delete:
			delete(entries, c.From.Name)
		}
	}

	// Reconstruct nested tree structure from flat entries and store.
	return writeTreeFromFlat(repo.Storer, entries)
}

// flattenTree recursively reads a tree into a flat map of full-path → TreeEntry
// (with the entry's Hash pointing to the blob, not a subtree).
func flattenTree(tree *object.Tree, prefix string, out map[string]object.TreeEntry) {
	for _, entry := range tree.Entries {
		fullPath := entry.Name
		if prefix != "" {
			fullPath = prefix + "/" + entry.Name
		}
		if entry.Mode == filemode.Dir {
			subtree, err := tree.Tree(entry.Name)
			if err == nil {
				flattenTree(subtree, fullPath, out)
			}
		} else {
			out[fullPath] = object.TreeEntry{
				Name: baseName(fullPath),
				Mode: entry.Mode,
				Hash: entry.Hash,
			}
		}
	}
}

// writeTreeFromFlat reconstructs nested git trees from a flat path→entry map
// and writes them to the object store, returning the root tree hash.
func writeTreeFromFlat(s storer.EncodedObjectStorer, flat map[string]object.TreeEntry) (plumbing.Hash, error) {
	// Group entries by directory, ensuring all ancestor directories exist.
	type dirEntry struct {
		name string
		mode filemode.FileMode
		hash plumbing.Hash
	}
	dirs := map[string][]dirEntry{} // dir path → entries in that dir
	dirs[""] = nil                  // root always exists
	for path, entry := range flat {
		dir, name := splitPath(path)
		dirs[dir] = append(dirs[dir], dirEntry{name: name, mode: entry.Mode, hash: entry.Hash})
		// Ensure all ancestor directories exist.
		for dir != "" {
			parent, _ := splitPath(dir)
			if _, ok := dirs[parent]; !ok {
				dirs[parent] = nil
			}
			dir = parent
		}
	}

	// Build trees bottom-up: sort directory paths by depth (deepest first).
	dirPaths := make([]string, 0, len(dirs))
	for d := range dirs {
		dirPaths = append(dirPaths, d)
	}
	sort.Slice(dirPaths, func(i, j int) bool {
		di := strings.Count(dirPaths[i], "/")
		dj := strings.Count(dirPaths[j], "/")
		if dirPaths[i] == "" {
			return false // root last
		}
		if dirPaths[j] == "" {
			return true // root last
		}
		if di != dj {
			return di > dj // deeper first
		}
		return dirPaths[i] < dirPaths[j]
	})

	treeHashes := map[string]plumbing.Hash{} // dir path → tree hash

	for _, dirPath := range dirPaths {
		entries := dirs[dirPath]

		// Also add subtree entries for child directories.
		for childDir, childHash := range treeHashes {
			childParent, childName := splitPath(childDir)
			if childParent == dirPath {
				entries = append(entries, dirEntry{name: childName, mode: filemode.Dir, hash: childHash})
			}
		}

		// Sort entries by name (git requirement).
		sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })

		tree := &object.Tree{}
		for _, e := range entries {
			tree.Entries = append(tree.Entries, object.TreeEntry{
				Name: e.name,
				Mode: e.mode,
				Hash: e.hash,
			})
		}

		obj := s.NewEncodedObject()
		if err := tree.Encode(obj); err != nil {
			return plumbing.ZeroHash, fmt.Errorf("encode tree %q: %w", dirPath, err)
		}
		hash, err := s.SetEncodedObject(obj)
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("store tree %q: %w", dirPath, err)
		}
		treeHashes[dirPath] = hash
	}

	root, ok := treeHashes[""]
	if !ok {
		return plumbing.ZeroHash, fmt.Errorf("no root tree produced")
	}
	return root, nil
}

// splitPath splits "dir/subdir/file.txt" into ("dir/subdir", "file.txt").
// A top-level file returns ("", "file.txt").
func splitPath(path string) (string, string) {
	i := strings.LastIndex(path, "/")
	if i < 0 {
		return "", path
	}
	return path[:i], path[i+1:]
}

// baseName returns just the filename from a full path (last component).
func baseName(path string) string {
	_, name := splitPath(path)
	return name
}

// UpdateBranchRef updates a branch reference to point to a new commit.
// Uses CheckAndSetReference for CAS (compare-and-swap) to handle concurrent updates.
func UpdateBranchRef(repo *gogit.Repository, branch string, newHash, expectedOldHash plumbing.Hash) error {
	refName := plumbing.NewBranchReferenceName(branch)
	newRef := plumbing.NewHashReference(refName, newHash)
	oldRef := plumbing.NewHashReference(refName, expectedOldHash)
	return repo.Storer.CheckAndSetReference(newRef, oldRef)
}

// ResolveBranch resolves a branch name to its commit hash.
func ResolveBranch(repo *gogit.Repository, branch string) (plumbing.Hash, error) {
	ref, err := repo.Reference(plumbing.NewBranchReferenceName(branch), true)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("resolve branch %q: %w", branch, err)
	}
	return ref.Hash(), nil
}

// ListBranches returns the names of all branches in the repository.
func ListBranches(repo *gogit.Repository) ([]string, error) {
	refs, err := repo.References()
	if err != nil {
		return nil, err
	}
	var branches []string
	_ = refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Name().IsBranch() {
			branches = append(branches, ref.Name().Short())
		}
		return nil
	})
	return branches, nil
}

func changePath(c *object.Change) string {
	if c.To.Name != "" {
		return c.To.Name
	}
	return c.From.Name
}
