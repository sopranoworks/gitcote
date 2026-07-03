package main

import (
	"fmt"
	"os"
	"path/filepath"

	yaml "go.yaml.in/yaml/v4"
)

type Config struct {
	GitCote GitCoteConfig `yaml:"gitcote"`
}

type GitCoteConfig struct {
	MCPURL     string `yaml:"mcp_url"`
	OAuthToken string `yaml:"oauth_token"`
}

func LoadConfig(path string) (*Config, error) {
	paths := configSearchPaths(path)

	var cfg Config
	var loaded bool
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read config %s: %w", p, err)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", p, err)
		}
		loaded = true
		break
	}

	applyEnvOverrides(&cfg)

	if !loaded && cfg.GitCote.MCPURL == "" {
		return nil, fmt.Errorf("no config found; create dovefeeder.yaml or ~/.config/dovefeeder/config.yaml with:\n\ngitcote:\n  mcp_url: \"https://gitcote.example.com/mcp\"\n  oauth_token: \"tok_...\"")
	}

	return &cfg, nil
}

func configSearchPaths(explicit string) []string {
	if explicit != "" {
		return []string{explicit}
	}
	var paths []string
	paths = append(paths, "dovefeeder.yaml")
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "dovefeeder", "config.yaml"))
	}
	return paths
}

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("GITCOTE_MCP_URL"); v != "" {
		cfg.GitCote.MCPURL = v
	}
	if v := os.Getenv("GITCOTE_OAUTH_TOKEN"); v != "" {
		cfg.GitCote.OAuthToken = v
	}
}
