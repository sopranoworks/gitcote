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

// ConflictResult describes the result of a conflict check (legacy API).
type ConflictResult struct {
	HasConflict     bool
	ConflictedFiles []string
	IsFastForward   bool
}

// MergeResult is the result of an in-memory 3-way merge computation.
type MergeResult struct {
	Clean       bool
	FastForward bool
	TreeHash    plumbing.Hash
	Conflicts   []ConflictEntry
}

// ConflictEntry describes a single conflicting file in a merge.
type ConflictEntry struct {
	Path   string        `json:"path"`
	Type   string        `json:"type"`
	Base   plumbing.Hash `json:"base"`
	Ours   plumbing.Hash `json:"ours"`
	Theirs plumbing.Hash `json:"theirs"`
}

// MergeConflictError is returned by MergeCommit when conflicts are detected.
type MergeConflictError struct {
	Conflicts []ConflictEntry
}

func (e *MergeConflictError) Error() string {
	paths := make([]string, len(e.Conflicts))
	for i, c := range e.Conflicts {
		paths[i] = c.Path
	}
	return fmt.Sprintf("merge conflicts: %s", strings.Join(paths, ", "))
}

// ResolveDefaultBranch returns the default branch name from HEAD's symbolic
// target. Works even in empty repos where the branch ref doesn't exist yet.
func ResolveDefaultBranch(repo *gogit.Repository) (string, error) {
	head, err := repo.Reference(plumbing.HEAD, false)
	if err != nil {
		return "", fmt.Errorf("read HEAD: %w", err)
	}
	if head.Type() == plumbing.SymbolicReference {
		return head.Target().Short(), nil
	}
	resolved, err := repo.Head()
	if err != nil {
		return "", fmt.Errorf("resolve HEAD: %w", err)
	}
	return resolved.Name().Short(), nil
}

// CreateBranchRef creates a new branch reference pointing to the given hash.
// Unlike UpdateBranchRef, it does not require the ref to exist beforehand.
func CreateBranchRef(repo *gogit.Repository, branch string, hash plumbing.Hash) error {
	refName := plumbing.NewBranchReferenceName(branch)
	return repo.Storer.SetReference(plumbing.NewHashReference(refName, hash))
}

// ComputeMerge performs an in-memory 3-way merge between target and source
// commits. It returns a clean merged tree hash or a list of conflicts.
// No refs or repository state are modified.
//
// When target is ZeroHash (empty base — no commits on target branch), the
// source tree IS the merged tree. No conflicts are possible.
//
// Uses the conservative approach: if both sides modify the same file with
// different blob hashes, it is reported as a conflict (no line-level merge).
func ComputeMerge(repo *gogit.Repository, target, source plumbing.Hash) (*MergeResult, error) {
	if target == plumbing.ZeroHash {
		sourceCommit, err := repo.CommitObject(source)
		if err != nil {
			return nil, fmt.Errorf("resolve source commit: %w", err)
		}
		sourceTree, err := sourceCommit.Tree()
		if err != nil {
			return nil, fmt.Errorf("source tree: %w", err)
		}
		return &MergeResult{Clean: true, TreeHash: sourceTree.Hash}, nil
	}

	targetCommit, err := repo.CommitObject(target)
	if err != nil {
		return nil, fmt.Errorf("resolve target commit: %w", err)
	}
	sourceCommit, err := repo.CommitObject(source)
	if err != nil {
		return nil, fmt.Errorf("resolve source commit: %w", err)
	}

	isFF, err := targetCommit.IsAncestor(sourceCommit)
	if err != nil {
		return nil, fmt.Errorf("ancestry check: %w", err)
	}
	if isFF {
		sourceTree, err := sourceCommit.Tree()
		if err != nil {
			return nil, fmt.Errorf("source tree: %w", err)
		}
		return &MergeResult{Clean: true, FastForward: true, TreeHash: sourceTree.Hash}, nil
	}

	bases, err := sourceCommit.MergeBase(targetCommit)
	if err != nil {
		return nil, fmt.Errorf("merge base: %w", err)
	}

	// No common ancestor: unrelated histories (e.g. orphan branches after
	// bootstrap merge on an empty repo). Use an empty tree as the base —
	// equivalent to git merge --allow-unrelated-histories.
	var baseTree *object.Tree
	if len(bases) == 0 {
		baseTree = &object.Tree{}
	} else {
		baseTree, err = bases[0].Tree()
		if err != nil {
			return nil, fmt.Errorf("base tree: %w", err)
		}
	}
	targetTree, err := targetCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("target tree: %w", err)
	}
	sourceTree, err := sourceCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("source tree: %w", err)
	}

	targetChanges, err := object.DiffTree(baseTree, targetTree)
	if err != nil {
		return nil, fmt.Errorf("diff base→target: %w", err)
	}
	sourceChanges, err := object.DiffTree(baseTree, sourceTree)
	if err != nil {
		return nil, fmt.Errorf("diff base→source: %w", err)
	}

	type changeInfo struct {
		action merkletrie.Action
	}

	targetMap := make(map[string]changeInfo)
	for _, c := range targetChanges {
		a, _ := c.Action()
		targetMap[changePath(c)] = changeInfo{action: a}
	}
	sourceMap := make(map[string]changeInfo)
	for _, c := range sourceChanges {
		a, _ := c.Action()
		sourceMap[changePath(c)] = changeInfo{action: a}
	}

	entries := make(map[string]object.TreeEntry)
	flattenTree(baseTree, "", entries)

	var conflicts []ConflictEntry

	allPaths := make(map[string]bool)
	for p := range targetMap {
		allPaths[p] = true
	}
	for p := range sourceMap {
		allPaths[p] = true
	}

	for path := range allPaths {
		tc, tChanged := targetMap[path]
		sc, sChanged := sourceMap[path]

		if tChanged && sChanged {
			tDel := tc.action == merkletrie.Delete
			sDel := sc.action == merkletrie.Delete

			if tDel && sDel {
				delete(entries, path)
				continue
			}
			if tDel {
				var baseHash plumbing.Hash
				if e, ok := entries[path]; ok {
					baseHash = e.Hash
				}
				sf, err := sourceTree.File(path)
				if err != nil {
					return nil, fmt.Errorf("source file %q: %w", path, err)
				}
				conflicts = append(conflicts, ConflictEntry{
					Path: path, Type: "modify-delete",
					Base: baseHash, Ours: plumbing.ZeroHash, Theirs: sf.Hash,
				})
				continue
			}
			if sDel {
				var baseHash plumbing.Hash
				if e, ok := entries[path]; ok {
					baseHash = e.Hash
				}
				tf, err := targetTree.File(path)
				if err != nil {
					return nil, fmt.Errorf("target file %q: %w", path, err)
				}
				conflicts = append(conflicts, ConflictEntry{
					Path: path, Type: "modify-delete",
					Base: baseHash, Ours: tf.Hash, Theirs: plumbing.ZeroHash,
				})
				continue
			}

			tf, err := targetTree.File(path)
			if err != nil {
				return nil, fmt.Errorf("target file %q: %w", path, err)
			}
			sf, err := sourceTree.File(path)
			if err != nil {
				return nil, fmt.Errorf("source file %q: %w", path, err)
			}
			if tf.Hash == sf.Hash {
				entries[path] = object.TreeEntry{Name: baseName(path), Mode: tf.Mode, Hash: tf.Hash}
			} else {
				// Attempt line-level 3-way merge.
				var baseContent string
				if bf, berr := baseTree.File(path); berr == nil {
					baseContent, _ = bf.Contents()
				}
				oursContent, _ := tf.Contents()
				theirsContent, _ := sf.Contents()

				merged, isConflict := mergeFileContent(baseContent, oursContent, theirsContent)
				if isConflict {
					var blobHash plumbing.Hash
					if e, ok := entries[path]; ok {
						blobHash = e.Hash
					}
					conflicts = append(conflicts, ConflictEntry{
						Path: path, Type: "content",
						Base: blobHash, Ours: tf.Hash, Theirs: sf.Hash,
					})
				} else {
					mergedHash, merr := storeBlob(repo.Storer, []byte(merged))
					if merr != nil {
						return nil, fmt.Errorf("store merged blob for %q: %w", path, merr)
					}
					entries[path] = object.TreeEntry{Name: baseName(path), Mode: tf.Mode, Hash: mergedHash}
				}
			}
		} else if tChanged {
			switch tc.action {
			case merkletrie.Insert, merkletrie.Modify:
				f, err := targetTree.File(path)
				if err != nil {
					return nil, fmt.Errorf("target file %q: %w", path, err)
				}
				entries[path] = object.TreeEntry{Name: baseName(path), Mode: f.Mode, Hash: f.Hash}
			case merkletrie.Delete:
				delete(entries, path)
			}
		} else {
			switch sc.action {
			case merkletrie.Insert, merkletrie.Modify:
				f, err := sourceTree.File(path)
				if err != nil {
					return nil, fmt.Errorf("source file %q: %w", path, err)
				}
				entries[path] = object.TreeEntry{Name: baseName(path), Mode: f.Mode, Hash: f.Hash}
			case merkletrie.Delete:
				delete(entries, path)
			}
		}
	}

	if len(conflicts) > 0 {
		sort.Slice(conflicts, func(i, j int) bool {
			return conflicts[i].Path < conflicts[j].Path
		})
		return &MergeResult{Conflicts: conflicts}, nil
	}

	treeHash, err := writeTreeFromFlat(repo.Storer, entries)
	if err != nil {
		return nil, fmt.Errorf("write merged tree: %w", err)
	}
	return &MergeResult{Clean: true, TreeHash: treeHash}, nil
}

// CheckConflicts detects file-level conflicts between source and target branches.
func CheckConflicts(repo *gogit.Repository, sourceRef, targetRef plumbing.Hash) (*ConflictResult, error) {
	result, err := ComputeMerge(repo, targetRef, sourceRef)
	if err != nil {
		return nil, err
	}
	if result.FastForward {
		return &ConflictResult{IsFastForward: true}, nil
	}
	if result.Clean {
		return &ConflictResult{}, nil
	}
	files := make([]string, len(result.Conflicts))
	for i, c := range result.Conflicts {
		files[i] = c.Path
	}
	return &ConflictResult{HasConflict: true, ConflictedFiles: files}, nil
}

// MergeCommit creates a merge commit integrating source into target.
// Calls ComputeMerge first and rejects with MergeConflictError if conflicts exist.
func MergeCommit(repo *gogit.Repository, sourceRef, targetRef plumbing.Hash, msg, authorName, authorEmail string) (plumbing.Hash, error) {
	result, err := ComputeMerge(repo, targetRef, sourceRef)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("compute merge: %w", err)
	}
	if !result.Clean {
		return plumbing.ZeroHash, &MergeConflictError{Conflicts: result.Conflicts}
	}
	return MergeCommitFromTree(repo, result.TreeHash, targetRef, sourceRef, msg, authorName, authorEmail)
}

// MergeCommitFromTree creates a merge commit from a pre-computed tree hash.
func MergeCommitFromTree(repo *gogit.Repository, treeHash, targetRef, sourceRef plumbing.Hash, msg, authorName, authorEmail string) (plumbing.Hash, error) {
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

func writeTreeFromFlat(s storer.EncodedObjectStorer, flat map[string]object.TreeEntry) (plumbing.Hash, error) {
	type dirEntry struct {
		name string
		mode filemode.FileMode
		hash plumbing.Hash
	}
	dirs := map[string][]dirEntry{}
	dirs[""] = nil
	for path, entry := range flat {
		dir, name := splitPath(path)
		dirs[dir] = append(dirs[dir], dirEntry{name: name, mode: entry.Mode, hash: entry.Hash})
		for dir != "" {
			parent, _ := splitPath(dir)
			if _, ok := dirs[parent]; !ok {
				dirs[parent] = nil
			}
			dir = parent
		}
	}

	dirPaths := make([]string, 0, len(dirs))
	for d := range dirs {
		dirPaths = append(dirPaths, d)
	}
	sort.Slice(dirPaths, func(i, j int) bool {
		di := strings.Count(dirPaths[i], "/")
		dj := strings.Count(dirPaths[j], "/")
		if dirPaths[i] == "" {
			return false
		}
		if dirPaths[j] == "" {
			return true
		}
		if di != dj {
			return di > dj
		}
		return dirPaths[i] < dirPaths[j]
	})

	treeHashes := map[string]plumbing.Hash{}

	for _, dirPath := range dirPaths {
		entries := dirs[dirPath]

		for childDir, childHash := range treeHashes {
			childParent, childName := splitPath(childDir)
			if childParent == dirPath {
				entries = append(entries, dirEntry{name: childName, mode: filemode.Dir, hash: childHash})
			}
		}

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

func splitPath(path string) (string, string) {
	i := strings.LastIndex(path, "/")
	if i < 0 {
		return "", path
	}
	return path[:i], path[i+1:]
}

func baseName(path string) string {
	_, name := splitPath(path)
	return name
}

// UpdateBranchRef updates a branch reference to point to a new commit.
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
