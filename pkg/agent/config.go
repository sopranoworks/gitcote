package agent

import (
	"fmt"
	"os"
	"path/filepath"

	yaml "go.yaml.in/yaml/v4"
)

type AgentConfig struct {
	DirName     string
	Role        string
	DisplayName string
	Command     string
	Prompt      string
	EnvDir      string // path to environment_default/ directory (empty for builtin configs)
	IsBuiltin   bool
}

func (c *AgentConfig) HasEnvDir() bool {
	if c.IsBuiltin {
		return hasBuiltinEnvDefault(c.DirName)
	}
	return c.EnvDir != ""
}

type agentYAML struct {
	Agent struct {
		Role        string `yaml:"role"`
		DisplayName string `yaml:"display_name"`
		Command     string `yaml:"command"`
		Prompt      string `yaml:"prompt"`
	} `yaml:"agent"`
}

type AgentConfigs []AgentConfig

func (configs AgentConfigs) FindByName(name string) *AgentConfig {
	for i := range configs {
		if configs[i].DirName == name {
			return &configs[i]
		}
	}
	return nil
}

func (configs AgentConfigs) FindByRole(role string) []*AgentConfig {
	var result []*AgentConfig
	for i := range configs {
		if configs[i].Role == role {
			result = append(result, &configs[i])
		}
	}
	return result
}

func ScanAgentConfigs(configRoot string) (AgentConfigs, error) {
	var configs AgentConfigs

	names, err := builtinNames()
	if err != nil {
		return nil, fmt.Errorf("list builtin agents: %w", err)
	}
	for _, name := range names {
		cfg, err := loadBuiltinConfig(name)
		if err != nil {
			return nil, err
		}
		configs = append(configs, *cfg)
	}

	if configRoot == "" {
		return configs, nil
	}

	entries, err := os.ReadDir(configRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return configs, nil
		}
		return nil, fmt.Errorf("read agent config root %q: %w", configRoot, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		yamlPath := filepath.Join(configRoot, entry.Name(), "agent.yaml")
		data, err := os.ReadFile(yamlPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read %s: %w", yamlPath, err)
		}

		var parsed agentYAML
		if err := yaml.Unmarshal(data, &parsed); err != nil {
			return nil, fmt.Errorf("parse %s: %w", yamlPath, err)
		}

		if parsed.Agent.Role == "" {
			return nil, fmt.Errorf("%s: agent.role is required", yamlPath)
		}
		if parsed.Agent.Command == "" {
			return nil, fmt.Errorf("%s: agent.command is required", yamlPath)
		}
		if parsed.Agent.Prompt == "" {
			return nil, fmt.Errorf("%s: agent.prompt is required", yamlPath)
		}

		displayName := parsed.Agent.DisplayName
		if displayName == "" {
			displayName = entry.Name()
		}

		envDir := filepath.Join(configRoot, entry.Name(), "environment_default")
		if _, serr := os.Stat(envDir); serr != nil {
			envDir = ""
		}

		userCfg := AgentConfig{
			DirName:     entry.Name(),
			Role:        parsed.Agent.Role,
			DisplayName: displayName,
			Command:     parsed.Agent.Command,
			Prompt:      parsed.Agent.Prompt,
			EnvDir:      envDir,
		}

		overridden := false
		for i, c := range configs {
			if c.DirName == entry.Name() {
				configs[i] = userCfg
				overridden = true
				break
			}
		}
		if !overridden {
			configs = append(configs, userCfg)
		}
	}

	return configs, nil
}
