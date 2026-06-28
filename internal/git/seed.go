package git

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	gogit "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/transport/ssh"
	gossh "golang.org/x/crypto/ssh"
)

const (
	seedConfigFile = "seed.json"
	seedRemoteName = "seed"

	PushModeDisabled = "disabled"
	PushModeOnMerge  = "on-merge"
	PushModePeriodic = "periodic"

	SeedStateActive   = "active"
	SeedStatePending  = "pending"
	SeedStateDisabled = "disabled"
	SeedStateError    = "error"
)

type SeedConfig struct {
	SeedURL      string          `json:"seed_url"`
	KeyName      string          `json:"key_name"`
	PushMode     string          `json:"push_mode"`
	PushInterval string          `json:"push_interval,omitempty"`
	SyncStatus   *SeedSyncStatus `json:"sync_status,omitempty"`
}

type SeedSyncStatus struct {
	State       string     `json:"state"`
	LastPushAt  *time.Time `json:"last_push_at,omitempty"`
	LastResult  string     `json:"last_result,omitempty"`
	PausedSince *time.Time `json:"paused_since,omitempty"`
}

// SeedMergeResult describes the outcome of comparing local and seed branches.
type SeedMergeResult struct {
	Status     string        // "up-to-date", "fast-forward", "auto-merged", "conflict"
	MergedHash plumbing.Hash // valid for fast-forward and auto-merged
	Conflicts  []ConflictEntry
}

func LoadSeedConfig(projectPath string) (*SeedConfig, error) {
	data, err := os.ReadFile(filepath.Join(projectPath, seedConfigFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &SeedConfig{PushMode: PushModeDisabled}, nil
		}
		return nil, err
	}
	var cfg SeedConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func SaveSeedConfig(projectPath string, cfg *SeedConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(projectPath, seedConfigFile), data, 0o644)
}

func UpdateSeedStatus(projectPath string, status *SeedSyncStatus) error {
	cfg, err := LoadSeedConfig(projectPath)
	if err != nil {
		return err
	}
	cfg.SyncStatus = status
	return SaveSeedConfig(projectPath, cfg)
}

func sshAuthFromPEM(privateKeyPEM []byte) (*ssh.PublicKeys, error) {
	auth, err := ssh.NewPublicKeys("git", privateKeyPEM, "")
	if err != nil {
		return nil, fmt.Errorf("load SSH key: %w", err)
	}
	auth.HostKeyCallback = gossh.InsecureIgnoreHostKey()
	return auth, nil
}

func ensureSeedRemote(repo *gogit.Repository, seedURL string) (*gogit.Remote, error) {
	remote, err := repo.Remote(seedRemoteName)
	if err != nil {
		return repo.CreateRemote(&config.RemoteConfig{
			Name: seedRemoteName,
			URLs: []string{seedURL},
		})
	}
	if len(remote.Config().URLs) == 0 || remote.Config().URLs[0] != seedURL {
		_ = repo.DeleteRemote(seedRemoteName)
		return repo.CreateRemote(&config.RemoteConfig{
			Name: seedRemoteName,
			URLs: []string{seedURL},
		})
	}
	return remote, nil
}

// FetchSeedRef fetches the given branch from the seed and returns the remote HEAD hash.
func FetchSeedRef(repo *gogit.Repository, seedURL string, branch string, privateKeyPEM []byte) (plumbing.Hash, error) {
	if branch == "" {
		branch = "main"
	}
	auth, err := sshAuthFromPEM(privateKeyPEM)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if _, err := ensureSeedRemote(repo, seedURL); err != nil {
		return plumbing.ZeroHash, err
	}

	refSpec := config.RefSpec(fmt.Sprintf("+refs/heads/%s:refs/remotes/%s/%s", branch, seedRemoteName, branch))
	err = repo.Fetch(&gogit.FetchOptions{
		RemoteName:    seedRemoteName,
		RefSpecs:      []config.RefSpec{refSpec},
		ClientOptions: []client.Option{client.WithSSHAuth(auth)},
	})
	if err != nil && !errors.Is(err, gogit.NoErrAlreadyUpToDate) {
		return plumbing.ZeroHash, fmt.Errorf("fetch: %w", err)
	}

	ref, err := repo.Reference(plumbing.ReferenceName("refs/remotes/"+seedRemoteName+"/"+branch), true)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("resolve seed ref: %w", err)
	}
	return ref.Hash(), nil
}

// SeedMerge computes the merge result between a local and remote ref.
// It returns auto-merged (with a new merge commit) or conflict info.
func SeedMerge(repo *gogit.Repository, localRef, remoteRef plumbing.Hash) (*SeedMergeResult, error) {
	if localRef == remoteRef {
		return &SeedMergeResult{Status: "up-to-date", MergedHash: localRef}, nil
	}

	localCommit, err := repo.CommitObject(localRef)
	if err != nil {
		return nil, fmt.Errorf("resolve local commit: %w", err)
	}
	remoteCommit, err := repo.CommitObject(remoteRef)
	if err != nil {
		return nil, fmt.Errorf("resolve remote commit: %w", err)
	}

	isLocalAncestor, _ := localCommit.IsAncestor(remoteCommit)
	if isLocalAncestor {
		return &SeedMergeResult{Status: "fast-forward", MergedHash: remoteRef}, nil
	}

	isRemoteAncestor, _ := remoteCommit.IsAncestor(localCommit)
	if isRemoteAncestor {
		return &SeedMergeResult{Status: "up-to-date", MergedHash: localRef}, nil
	}

	result, err := ComputeMerge(repo, localRef, remoteRef)
	if err != nil {
		return nil, fmt.Errorf("compute merge: %w", err)
	}

	if !result.Clean {
		return &SeedMergeResult{Status: "conflict", Conflicts: result.Conflicts}, nil
	}

	mergeHash, err := MergeCommitFromTree(repo, result.TreeHash, localRef, remoteRef,
		"Auto-merge seed sync", "GitYard", "gityard@localhost")
	if err != nil {
		return nil, fmt.Errorf("create merge commit: %w", err)
	}
	return &SeedMergeResult{Status: "auto-merged", MergedHash: mergeHash}, nil
}

// CreateSeedTempClone clones the seed into a temporary directory and adds
// a "gityard" remote pointing to gityardURL (if non-empty).
func CreateSeedTempClone(seedURL string, sshKey []byte, gityardURL string) (string, error) {
	dir, err := os.MkdirTemp("", "gityard-seed-sync-")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}

	auth, err := sshAuthFromPEM(sshKey)
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}

	repo, err := gogit.PlainClone(dir, &gogit.CloneOptions{
		URL:           seedURL,
		ClientOptions: []client.Option{client.WithSSHAuth(auth)},
	})
	if err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("clone seed: %w", err)
	}

	if gityardURL != "" {
		_, _ = repo.CreateRemote(&config.RemoteConfig{
			Name: "gityard",
			URLs: []string{gityardURL},
		})
	}

	return dir, nil
}

// SetBranchRef sets a branch reference to the given hash (non-CAS).
func SetBranchRef(repo *gogit.Repository, branch string, hash plumbing.Hash) error {
	ref := plumbing.NewHashReference(plumbing.NewBranchReferenceName(branch), hash)
	return repo.Storer.SetReference(ref)
}

// PushToSeed pushes a branch to a seed repository using the provided private key PEM.
func PushToSeed(repo *gogit.Repository, seedURL string, branch string, privateKeyPEM []byte) error {
	if seedURL == "" {
		return fmt.Errorf("no seed URL configured")
	}
	if branch == "" {
		branch = "main"
	}

	auth, err := sshAuthFromPEM(privateKeyPEM)
	if err != nil {
		return err
	}

	if _, err := ensureSeedRemote(repo, seedURL); err != nil {
		return err
	}

	refSpec := config.RefSpec(fmt.Sprintf("refs/heads/%s:refs/heads/%s", branch, branch))
	err = repo.Push(&gogit.PushOptions{
		RemoteName:    seedRemoteName,
		RefSpecs:      []config.RefSpec{refSpec},
		ClientOptions: []client.Option{client.WithSSHAuth(auth)},
	})
	if err != nil && !errors.Is(err, gogit.NoErrAlreadyUpToDate) {
		return fmt.Errorf("push: %w", err)
	}
	return nil
}

// PullFromSeed fetches from the seed repository and fast-forwards the local branch.
func PullFromSeed(repo *gogit.Repository, seedURL string, branch string, privateKeyPEM []byte) error {
	if seedURL == "" {
		return fmt.Errorf("no seed URL configured")
	}
	if branch == "" {
		branch = "main"
	}

	auth, err := sshAuthFromPEM(privateKeyPEM)
	if err != nil {
		return err
	}

	if _, err := ensureSeedRemote(repo, seedURL); err != nil {
		return err
	}

	refSpec := config.RefSpec(fmt.Sprintf("+refs/heads/%s:refs/remotes/%s/%s", branch, seedRemoteName, branch))
	err = repo.Fetch(&gogit.FetchOptions{
		RemoteName:    seedRemoteName,
		RefSpecs:      []config.RefSpec{refSpec},
		ClientOptions: []client.Option{client.WithSSHAuth(auth)},
	})
	if err != nil && !errors.Is(err, gogit.NoErrAlreadyUpToDate) {
		return fmt.Errorf("fetch: %w", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("worktree: %w", err)
	}

	err = wt.Pull(&gogit.PullOptions{
		RemoteName:    seedRemoteName,
		ReferenceName: plumbing.ReferenceName("refs/heads/" + branch),
		ClientOptions: []client.Option{client.WithSSHAuth(auth)},
	})
	if err != nil && !errors.Is(err, gogit.NoErrAlreadyUpToDate) {
		return fmt.Errorf("pull: %w", err)
	}
	return nil
}

// TestSeedConnection verifies SSH connectivity to a seed repository.
func TestSeedConnection(seedURL string, privateKeyPEM []byte) error {
	if seedURL == "" {
		return fmt.Errorf("no seed URL configured")
	}

	auth, err := sshAuthFromPEM(privateKeyPEM)
	if err != nil {
		return err
	}

	remote := gogit.NewRemote(nil, &config.RemoteConfig{
		Name: seedRemoteName,
		URLs: []string{seedURL},
	})

	_, err = remote.ListContext(context.Background(), &gogit.ListOptions{
		ClientOptions: []client.Option{client.WithSSHAuth(auth)},
	})
	if err != nil {
		return fmt.Errorf("ls-remote: %w", err)
	}
	return nil
}
