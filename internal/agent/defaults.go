package agent

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed agents
var defaultAgentsFS embed.FS

func EnsureDefaultAgents(configRoot string) error {
	if err := os.MkdirAll(configRoot, 0o755); err != nil {
		return err
	}

	entries, err := fs.ReadDir(defaultAgentsFS, "agents")
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		target := filepath.Join(configRoot, entry.Name())
		if _, err := os.Stat(target); err == nil {
			continue
		}
		if err := copyEmbeddedDir(defaultAgentsFS, filepath.Join("agents", entry.Name()), target); err != nil {
			return err
		}
	}
	return nil
}

func copyEmbeddedDir(fsys embed.FS, src, dst string) error {
	return fs.WalkDir(fsys, src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)

		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		data, err := fsys.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
