// Package git provides Git repository management and Smart HTTP transport
// for GitYard using go-git v6 (pure Go, no external binary).
// Repositories use a non-bare layout (<base_dir>/<namespace>/<project>/.git/)
// matching Shoka's storage conventions.
package git

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	gogit "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
)

// Store manages Git repositories under a base directory using Shoka's
// namespace/project filesystem layout.
type Store struct {
	baseDir string
}

// NewStore returns a Store rooted at baseDir.
func NewStore(baseDir string) *Store {
	return &Store{baseDir: baseDir}
}

// BaseDir returns the store's base directory.
func (s *Store) BaseDir() string { return s.baseDir }

const DefaultNamespace = "default"

// ProjectPath returns the absolute path of a project directory, validating
// the namespace and project names. It does not require the project to exist.
func (s *Store) ProjectPath(namespace, project string) (string, error) {
	if namespace == "" {
		namespace = DefaultNamespace
	}
	if !IsValidName(namespace) {
		return "", fmt.Errorf("invalid namespace: %q", namespace)
	}
	if !IsValidName(project) {
		return "", fmt.Errorf("invalid project name: %q", project)
	}
	return filepath.Join(s.baseDir, namespace, project), nil
}

// GitDir returns the .git directory path for a project.
func (s *Store) GitDir(namespace, project string) (string, error) {
	p, err := s.ProjectPath(namespace, project)
	if err != nil {
		return "", err
	}
	return filepath.Join(p, ".git"), nil
}

// IsValidName checks if a name (namespace or project) contains only
// [a-zA-Z0-9_-]. Mirrors Shoka's internal/utils.IsValidName.
func IsValidName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// CreateRepo initializes a new Git repository at <base>/<namespace>/<project>
// with a non-bare layout (.git/ subdirectory). HEAD defaults to refs/heads/main.
func (s *Store) CreateRepo(namespace, project string) error {
	projPath, err := s.ProjectPath(namespace, project)
	if err != nil {
		return err
	}
	gitDir := filepath.Join(projPath, ".git")
	if _, err := os.Stat(filepath.Join(gitDir, "HEAD")); err == nil {
		return fmt.Errorf("repository %s/%s already exists", namespace, project)
	}
	if err := os.MkdirAll(projPath, 0o755); err != nil {
		return fmt.Errorf("create project directory: %w", err)
	}

	repo, err := gogit.PlainInit(projPath, false)
	if err != nil {
		return fmt.Errorf("git init: %w", err)
	}

	// Set HEAD to refs/heads/main (the modern default).
	if err := repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("main"))); err != nil {
		return fmt.Errorf("set HEAD to main: %w", err)
	}

	// Set receive.denyCurrentBranch=updateInstead so pushes to the checked-out
	// branch update the working tree (non-bare repos normally reject this).
	cfg, err := repo.Config()
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	cfg.Raw.SetOption("receive", "", "denyCurrentBranch", "updateInstead")
	if err := repo.SetConfig(cfg); err != nil {
		return fmt.Errorf("set config: %w", err)
	}

	return nil
}

// CloneRepo clones a remote URL into a new project at <base>/<namespace>/<project>.
func (s *Store) CloneRepo(namespace, project, cloneURL string) error {
	projPath, err := s.ProjectPath(namespace, project)
	if err != nil {
		return err
	}
	gitDir := filepath.Join(projPath, ".git")
	if _, err := os.Stat(filepath.Join(gitDir, "HEAD")); err == nil {
		return fmt.Errorf("repository %s/%s already exists", namespace, project)
	}
	if err := os.MkdirAll(filepath.Dir(projPath), 0o755); err != nil {
		return fmt.Errorf("create namespace directory: %w", err)
	}

	_, err = gogit.PlainClone(projPath, &gogit.CloneOptions{
		URL: cloneURL,
	})
	if err != nil {
		return fmt.Errorf("git clone: %w", err)
	}
	return nil
}

// RepoExists reports whether a repo exists at <base>/<namespace>/<project>.
func (s *Store) RepoExists(namespace, project string) (bool, error) {
	gitDir, err := s.GitDir(namespace, project)
	if err != nil {
		return false, err
	}
	_, serr := os.Stat(filepath.Join(gitDir, "HEAD"))
	if serr == nil {
		return true, nil
	}
	if errors.Is(serr, os.ErrNotExist) {
		return false, nil
	}
	return false, serr
}

// OpenRepo opens an existing repository.
func (s *Store) OpenRepo(namespace, project string) (*gogit.Repository, error) {
	projPath, err := s.ProjectPath(namespace, project)
	if err != nil {
		return nil, err
	}
	return gogit.PlainOpen(projPath)
}

// ListProjects returns all namespace/project pairs under the base directory.
func (s *Store) ListProjects(namespace string) ([]ProjectInfo, error) {
	var results []ProjectInfo
	if namespace != "" {
		return s.listNamespace(namespace)
	}
	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		ns := e.Name()
		if !IsValidName(ns) {
			continue
		}
		projs, err := s.listNamespace(ns)
		if err != nil {
			continue
		}
		results = append(results, projs...)
	}
	return results, nil
}

func (s *Store) listNamespace(ns string) ([]ProjectInfo, error) {
	nsDir := filepath.Join(s.baseDir, ns)
	entries, err := os.ReadDir(nsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var results []ProjectInfo
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		head := filepath.Join(nsDir, e.Name(), ".git", "HEAD")
		if _, err := os.Stat(head); err != nil {
			continue
		}
		results = append(results, ProjectInfo{Namespace: ns, Project: e.Name()})
	}
	return results, nil
}

// ProjectInfo identifies a namespace/project pair.
type ProjectInfo struct {
	Namespace string `json:"namespace"`
	Project   string `json:"project"`
}

