package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig_FromFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "dovefeeder.yaml")
	os.WriteFile(cfgPath, []byte(`gitcote:
  mcp_url: "https://example.com/mcp"
  oauth_token: "tok_abc123"
`), 0o644)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitCote.MCPURL != "https://example.com/mcp" {
		t.Errorf("mcp_url = %q, want https://example.com/mcp", cfg.GitCote.MCPURL)
	}
	if cfg.GitCote.OAuthToken != "tok_abc123" {
		t.Errorf("oauth_token = %q, want tok_abc123", cfg.GitCote.OAuthToken)
	}
}

func TestLoadConfig_EnvOverride(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "dovefeeder.yaml")
	os.WriteFile(cfgPath, []byte(`gitcote:
  mcp_url: "https://file.example.com/mcp"
  oauth_token: "tok_file"
`), 0o644)

	t.Setenv("GITCOTE_MCP_URL", "https://env.example.com/mcp")
	t.Setenv("GITCOTE_OAUTH_TOKEN", "tok_env")

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitCote.MCPURL != "https://env.example.com/mcp" {
		t.Errorf("mcp_url = %q, want env override", cfg.GitCote.MCPURL)
	}
	if cfg.GitCote.OAuthToken != "tok_env" {
		t.Errorf("oauth_token = %q, want env override", cfg.GitCote.OAuthToken)
	}
}

func TestLoadConfig_EnvOnly(t *testing.T) {
	t.Setenv("GITCOTE_MCP_URL", "https://env-only.example.com/mcp")
	t.Setenv("GITCOTE_OAUTH_TOKEN", "tok_env_only")

	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "nonexistent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitCote.MCPURL != "https://env-only.example.com/mcp" {
		t.Errorf("mcp_url = %q, want env value", cfg.GitCote.MCPURL)
	}
}

func TestLoadConfig_Missing(t *testing.T) {
	t.Setenv("GITCOTE_MCP_URL", "")
	t.Setenv("GITCOTE_OAUTH_TOKEN", "")

	_, err := LoadConfig(filepath.Join(t.TempDir(), "nonexistent.yaml"))
	if err == nil {
		t.Fatal("expected error for missing config")
	}
}

func TestConfigSearchPaths_Explicit(t *testing.T) {
	paths := configSearchPaths("/some/path.yaml")
	if len(paths) != 1 || paths[0] != "/some/path.yaml" {
		t.Errorf("explicit path should return single entry, got %v", paths)
	}
}

func TestConfigSearchPaths_Default(t *testing.T) {
	paths := configSearchPaths("")
	if len(paths) < 1 {
		t.Fatal("expected at least one default path")
	}
	if paths[0] != "dovefeeder.yaml" {
		t.Errorf("first default path = %q, want dovefeeder.yaml", paths[0])
	}
}
