package git

import (
	"fmt"
	"io"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
)

type TreeEntryInfo struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size,omitempty"`
	Mode string `json:"mode"`
}

type LogEntry struct {
	SHA     string `json:"sha"`
	Author  string `json:"author"`
	Date    string `json:"date"`
	Message string `json:"message"`
}

type CommitDetail struct {
	SHA       string    `json:"sha"`
	Author    Signature `json:"author"`
	Committer Signature `json:"committer"`
	Message   string    `json:"message"`
	Parents   []string  `json:"parents"`
	Stats     DiffStats `json:"stats"`
}

type Signature struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type DiffStats struct {
	FilesChanged int            `json:"files_changed"`
	Insertions   int            `json:"insertions"`
	Deletions    int            `json:"deletions"`
	Files        []DiffFileStat `json:"files"`
}

type DiffFileStat struct {
	Path       string `json:"path"`
	Insertions int    `json:"insertions"`
	Deletions  int    `json:"deletions"`
}

type BranchInfo struct {
	Name      string `json:"name"`
	HeadSHA   string `json:"head_sha"`
	IsDefault bool   `json:"is_default"`
}

// ResolveRef resolves a ref string to a commit hash.
// Accepts: empty (HEAD), branch name, full ref path, tag name, or commit SHA.
func ResolveRef(repo *gogit.Repository, ref string) (plumbing.Hash, error) {
	if ref == "" {
		head, err := repo.Head()
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("resolve HEAD: %w", err)
		}
		return head.Hash(), nil
	}

	if len(ref) == 40 && isHex(ref) {
		h := plumbing.NewHash(ref)
		if _, err := repo.CommitObject(h); err == nil {
			return h, nil
		}
	}

	if h, err := ResolveBranch(repo, ref); err == nil {
		return h, nil
	}

	if r, err := repo.Reference(plumbing.ReferenceName(ref), true); err == nil {
		return r.Hash(), nil
	}

	if r, err := repo.Reference(plumbing.NewTagReferenceName(ref), true); err == nil {
		return r.Hash(), nil
	}

	return plumbing.ZeroHash, fmt.Errorf("cannot resolve ref %q", ref)
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// ListTreeEntries lists immediate children at a path in the given commit's tree.
func ListTreeEntries(repo *gogit.Repository, commitHash plumbing.Hash, path string) ([]TreeEntryInfo, error) {
	commit, err := repo.CommitObject(commitHash)
	if err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("tree: %w", err)
	}

	path = strings.Trim(path, "/")
	if path != "" {
		tree, err = tree.Tree(path)
		if err != nil {
			return nil, fmt.Errorf("path %q not found", path)
		}
	}

	var result []TreeEntryInfo
	for _, e := range tree.Entries {
		info := TreeEntryInfo{
			Name: e.Name,
			Mode: e.Mode.String(),
		}
		if e.Mode == filemode.Dir {
			info.Type = "directory"
		} else {
			info.Type = "file"
			obj, oerr := repo.Storer.EncodedObject(plumbing.BlobObject, e.Hash)
			if oerr == nil {
				info.Size = obj.Size()
			}
		}
		result = append(result, info)
	}
	return result, nil
}

// ReadFileContent reads a file's content from the given commit's tree.
// Returns (content, isBinary, error). Binary files return a descriptive message.
func ReadFileContent(repo *gogit.Repository, commitHash plumbing.Hash, path string) (string, bool, error) {
	commit, err := repo.CommitObject(commitHash)
	if err != nil {
		return "", false, fmt.Errorf("commit: %w", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return "", false, fmt.Errorf("tree: %w", err)
	}

	file, err := tree.File(path)
	if err != nil {
		return "", false, fmt.Errorf("file %q not found", path)
	}

	reader, err := file.Reader()
	if err != nil {
		return "", false, fmt.Errorf("read: %w", err)
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		return "", false, fmt.Errorf("read: %w", err)
	}

	if isBinary(data) {
		return fmt.Sprintf("Binary file (%d bytes)", len(data)), true, nil
	}
	return string(data), false, nil
}

func isBinary(data []byte) bool {
	n := 512
	if len(data) < n {
		n = len(data)
	}
	for _, b := range data[:n] {
		if b == 0 {
			return true
		}
	}
	return false
}

// GetBranches returns all branches with their head SHAs.
func GetBranches(repo *gogit.Repository) ([]BranchInfo, error) {
	head, _ := repo.Head()
	var defaultBranch string
	if head != nil && head.Name().IsBranch() {
		defaultBranch = head.Name().Short()
	}

	refs, err := repo.References()
	if err != nil {
		return nil, err
	}
	var branches []BranchInfo
	_ = refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Name().IsBranch() {
			branches = append(branches, BranchInfo{
				Name:      ref.Name().Short(),
				HeadSHA:   ref.Hash().String(),
				IsDefault: ref.Name().Short() == defaultBranch,
			})
		}
		return nil
	})
	return branches, nil
}

// GetLog returns commit log entries starting from the given commit.
func GetLog(repo *gogit.Repository, commitHash plumbing.Hash, pathFilter string, limit int) ([]LogEntry, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	iter, err := repo.Log(&gogit.LogOptions{From: commitHash})
	if err != nil {
		return nil, fmt.Errorf("log: %w", err)
	}

	var entries []LogEntry
	for {
		c, err := iter.Next()
		if err != nil {
			break
		}
		if pathFilter != "" && !commitTouchesPath(repo, c, pathFilter) {
			continue
		}
		entries = append(entries, LogEntry{
			SHA:     c.Hash.String(),
			Author:  fmt.Sprintf("%s <%s>", c.Author.Name, c.Author.Email),
			Date:    c.Author.When.UTC().Format(time.RFC3339),
			Message: firstLine(c.Message),
		})
		if len(entries) >= limit {
			break
		}
	}
	return entries, nil
}

func commitTouchesPath(repo *gogit.Repository, c *object.Commit, path string) bool {
	tree, err := c.Tree()
	if err != nil {
		return false
	}

	thisFile, thisErr := tree.File(path)

	if len(c.ParentHashes) == 0 {
		return thisErr == nil
	}

	parent, err := repo.CommitObject(c.ParentHashes[0])
	if err != nil {
		return thisErr == nil
	}
	parentTree, err := parent.Tree()
	if err != nil {
		return thisErr == nil
	}

	parentFile, parentErr := parentTree.File(path)

	if thisErr != nil && parentErr != nil {
		return false
	}
	if thisErr != nil || parentErr != nil {
		return true
	}
	return thisFile.Hash != parentFile.Hash
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// GetCommitDetail returns full details of a commit including diff stats.
func GetCommitDetail(repo *gogit.Repository, sha plumbing.Hash) (*CommitDetail, error) {
	commit, err := repo.CommitObject(sha)
	if err != nil {
		return nil, fmt.Errorf("commit %s: %w", sha, err)
	}

	var parents []string
	for _, p := range commit.ParentHashes {
		parents = append(parents, p.String())
	}

	commitTree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("tree: %w", err)
	}

	var parentTree *object.Tree
	if len(commit.ParentHashes) > 0 {
		p, perr := repo.CommitObject(commit.ParentHashes[0])
		if perr == nil {
			parentTree, _ = p.Tree()
		}
	}

	stats := computeDiffStats(parentTree, commitTree)

	return &CommitDetail{
		SHA:       commit.Hash.String(),
		Author:    Signature{Name: commit.Author.Name, Email: commit.Author.Email},
		Committer: Signature{Name: commit.Committer.Name, Email: commit.Committer.Email},
		Message:   commit.Message,
		Parents:   parents,
		Stats:     stats,
	}, nil
}

func computeDiffStats(from, to *object.Tree) DiffStats {
	changes, err := object.DiffTree(from, to)
	if err != nil {
		return DiffStats{}
	}

	patch, err := changes.Patch()
	if err != nil {
		return DiffStats{}
	}

	var stats DiffStats
	for _, fs := range patch.Stats() {
		stats.Files = append(stats.Files, DiffFileStat{
			Path:       fs.Name,
			Insertions: fs.Addition,
			Deletions:  fs.Deletion,
		})
		stats.Insertions += fs.Addition
		stats.Deletions += fs.Deletion
	}
	stats.FilesChanged = len(stats.Files)
	return stats
}
