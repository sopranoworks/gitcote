package git

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const projectConfigFile = "gityard.json"

type ProjectConfig struct {
	ProtectedBranches []string `json:"protected_branches"`
	ForcePush         string   `json:"force_push"`
}

func DefaultProjectConfig() *ProjectConfig {
	return &ProjectConfig{
		ProtectedBranches: []string{"main"},
		ForcePush:         "deny",
	}
}

func (c *ProjectConfig) IsProtected(branch string) bool {
	for _, p := range c.ProtectedBranches {
		if p == branch {
			return true
		}
	}
	return false
}

func LoadProjectConfig(projectPath string) (*ProjectConfig, error) {
	data, err := os.ReadFile(filepath.Join(projectPath, projectConfigFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DefaultProjectConfig(), nil
		}
		return nil, err
	}
	var cfg ProjectConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if len(cfg.ProtectedBranches) == 0 {
		cfg.ProtectedBranches = []string{"main"}
	}
	if cfg.ForcePush == "" {
		cfg.ForcePush = "deny"
	}
	return &cfg, nil
}

func SaveProjectConfig(projectPath string, cfg *ProjectConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(projectPath, projectConfigFile), data, 0o644)
}
