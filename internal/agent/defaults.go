package agent

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	yaml "go.yaml.in/yaml/v4"
)

//go:embed agents
var defaultAgentsFS embed.FS

func builtinNames() ([]string, error) {
	entries, err := fs.ReadDir(defaultAgentsFS, "agents")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

func loadBuiltinConfig(name string) (*AgentConfig, error) {
	yamlPath := filepath.Join("agents", name, "agent.yaml")
	data, err := defaultAgentsFS.ReadFile(yamlPath)
	if err != nil {
		return nil, fmt.Errorf("read embedded %s: %w", yamlPath, err)
	}

	var parsed agentYAML
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("parse embedded %s: %w", yamlPath, err)
	}

	displayName := parsed.Agent.DisplayName
	if displayName == "" {
		displayName = name
	}

	return &AgentConfig{
		DirName:     name,
		Role:        parsed.Agent.Role,
		DisplayName: displayName,
		Command:     parsed.Agent.Command,
		Prompt:      parsed.Agent.Prompt,
		IsBuiltin:   true,
	}, nil
}

func hasBuiltinEnvDefault(name string) bool {
	_, err := fs.ReadDir(defaultAgentsFS, filepath.Join("agents", name, "environment_default"))
	return err == nil
}

func copyBuiltinEnvDefault(name, dst string, vars map[string]string) error {
	srcRoot := filepath.Join("agents", name, "environment_default")
	return fs.WalkDir(defaultAgentsFS, srcRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(srcRoot, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := defaultAgentsFS.ReadFile(path)
		if err != nil {
			return err
		}
		if isBinary(data) {
			return os.WriteFile(target, data, 0o644)
		}
		substituted := substituteVars(string(data), vars)
		return os.WriteFile(target, []byte(substituted), 0o644)
	})
}
