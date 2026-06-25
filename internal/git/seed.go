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

	remote, err := repo.Remote(seedRemoteName)
	if err != nil {
		remote, err = repo.CreateRemote(&config.RemoteConfig{
			Name: seedRemoteName,
			URLs: []string{seedURL},
		})
		if err != nil {
			return fmt.Errorf("create remote: %w", err)
		}
	} else if len(remote.Config().URLs) == 0 || remote.Config().URLs[0] != seedURL {
		_ = repo.DeleteRemote(seedRemoteName)
		remote, err = repo.CreateRemote(&config.RemoteConfig{
			Name: seedRemoteName,
			URLs: []string{seedURL},
		})
		if err != nil {
			return fmt.Errorf("recreate remote: %w", err)
		}
	}

	refSpec := config.RefSpec(fmt.Sprintf("refs/heads/%s:refs/heads/%s", branch, branch))
	err = remote.Push(&gogit.PushOptions{
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

	remote, err := repo.Remote(seedRemoteName)
	if err != nil {
		remote, err = repo.CreateRemote(&config.RemoteConfig{
			Name: seedRemoteName,
			URLs: []string{seedURL},
		})
		if err != nil {
			return fmt.Errorf("create remote: %w", err)
		}
	} else if len(remote.Config().URLs) == 0 || remote.Config().URLs[0] != seedURL {
		_ = repo.DeleteRemote(seedRemoteName)
		remote, err = repo.CreateRemote(&config.RemoteConfig{
			Name: seedRemoteName,
			URLs: []string{seedURL},
		})
		if err != nil {
			return fmt.Errorf("recreate remote: %w", err)
		}
	}

	refSpec := config.RefSpec(fmt.Sprintf("+refs/heads/%s:refs/remotes/%s/%s", branch, seedRemoteName, branch))
	err = remote.Fetch(&gogit.FetchOptions{
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
