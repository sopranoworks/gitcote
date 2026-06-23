// Package git provides bare Git repository management and Smart HTTP transport
// for GitYard. Repositories are bare repos stored under <base_dir>/<namespace>/<project>.
package git

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Store manages bare Git repositories under a base directory, mirroring Shoka's
// namespace/project filesystem layout.
type Store struct {
	baseDir string
}

// NewStore returns a Store rooted at baseDir.
func NewStore(baseDir string) *Store {
	return &Store{baseDir: baseDir}
}

const DefaultNamespace = "default"

// RepoPath returns the absolute path of a bare repository, validating the
// namespace and project names. It does not require the repo to exist.
func (s *Store) RepoPath(namespace, project string) (string, error) {
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

// CreateRepo initializes a new bare Git repository at <base>/<namespace>/<project>.
// Returns an error if the repo already exists.
func (s *Store) CreateRepo(namespace, project string) error {
	repoPath, err := s.RepoPath(namespace, project)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(repoPath, "HEAD")); err == nil {
		return fmt.Errorf("repository %s/%s already exists", namespace, project)
	}
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		return fmt.Errorf("create repo directory: %w", err)
	}
	cmd := exec.Command("git", "init", "--bare", repoPath)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git init --bare: %w", err)
	}
	if err := configureRepo(repoPath); err != nil {
		return err
	}
	// Set HEAD to main (the modern default, vs git's "master").
	cmd = exec.Command("git", "-C", repoPath, "symbolic-ref", "HEAD", "refs/heads/main")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("set HEAD to main: %w", err)
	}
	return nil
}

// configureRepo sets git config flags needed for Smart HTTP serving and
// sets the default branch to "main".
func configureRepo(repoPath string) error {
	for _, kv := range [][2]string{
		{"http.receivepack", "true"},
		{"http.uploadpack", "true"},
		{"receive.advertisePushOptions", "true"},
	} {
		cmd := exec.Command("git", "-C", repoPath, "config", kv[0], kv[1])
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("git config %s: %w", kv[0], err)
		}
	}
	return nil
}

// CloneRepo clones a remote URL into a new bare repo at <base>/<namespace>/<project>.
func (s *Store) CloneRepo(namespace, project, cloneURL string) error {
	repoPath, err := s.RepoPath(namespace, project)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(repoPath, "HEAD")); err == nil {
		return fmt.Errorf("repository %s/%s already exists", namespace, project)
	}
	if err := os.MkdirAll(filepath.Dir(repoPath), 0o755); err != nil {
		return fmt.Errorf("create namespace directory: %w", err)
	}
	cmd := exec.Command("git", "clone", "--bare", cloneURL, repoPath)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone --bare: %w", err)
	}
	return configureRepo(repoPath)
}

// RepoExists reports whether a bare repo exists at <base>/<namespace>/<project>.
func (s *Store) RepoExists(namespace, project string) (bool, error) {
	repoPath, err := s.RepoPath(namespace, project)
	if err != nil {
		return false, err
	}
	_, serr := os.Stat(filepath.Join(repoPath, "HEAD"))
	if serr == nil {
		return true, nil
	}
	if errors.Is(serr, os.ErrNotExist) {
		return false, nil
	}
	return false, serr
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
		head := filepath.Join(nsDir, e.Name(), "HEAD")
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
