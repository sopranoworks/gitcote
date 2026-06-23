package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	yaml "go.yaml.in/yaml/v4"
)

// Config is GitYard's own configuration. It is deliberately INDEPENDENT of Shoka's
// internal/config: GitYard owns this struct and translates it into the per-package
// Config types of the inherited Shoka core (the same pattern cmd/shoka uses — a
// unified file decoded here, then mapped onto pkg/auth, pkg/authapi, pkg/oauth, …).
// Step 1 carries only the fields needed to boot the core; Git-hosting / PR fields
// are added by later steps.
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Storage  StorageConfig  `yaml:"storage"`
	Identity IdentityConfig `yaml:"identity"`
	OAuth    OAuthConfig    `yaml:"oauth"`
}

type ServerConfig struct {
	HTTP HTTPConfig `yaml:"http"`
	MCP  MCPConfig  `yaml:"mcp"`
	Auth AuthConfig `yaml:"auth"`
}

type HTTPConfig struct {
	Listen      string `yaml:"listen"`
	ExternalURL string `yaml:"external_url"`
}

type MCPConfig struct {
	Plain MCPPlainConfig `yaml:"plain"`
	OAuth MCPOAuthConfig `yaml:"oauth"`
}

// MCPPlainConfig is the plain (internal) MCP transport. bearer_auth off means
// unauthenticated — loopback/internal use only.
type MCPPlainConfig struct {
	Listen      string `yaml:"listen"`
	ExternalURL string `yaml:"external_url"`
	BearerAuth  bool   `yaml:"bearer_auth"`
}

// MCPOAuthConfig is the OAuth (external) MCP transport. Its PRESENCE (a non-empty
// listen) is what enables the OAuth authorization server, mirroring Shoka's B-50.
type MCPOAuthConfig struct {
	Listen      string `yaml:"listen"`
	ExternalURL string `yaml:"external_url"`
}

type AuthConfig struct {
	// Enabled + Tokens are the static-bearer policy for the Web routes (the gate the
	// Web UI was always built on); AllowedOrigins is the WebSocket origin allowlist.
	Enabled        bool        `yaml:"enabled"`
	Tokens         []string    `yaml:"tokens"`
	AllowedOrigins []string    `yaml:"allowed_origins"`
	Users          UsersConfig `yaml:"users"`
}

type UsersConfig struct {
	SessionTTL         Duration `yaml:"session_ttl"`
	AllowFirstRunAdmin *bool    `yaml:"allow_first_run_admin"`
	// TOTPEncryptionKey is an optional base64 32-byte key. When empty, a key is
	// generated and persisted under storage.base_dir/userstore.key on first run.
	TOTPEncryptionKey string `yaml:"totp_encryption_key"`
}

func (u UsersConfig) FirstRunAdminAllowed() bool {
	// Default: allow first-run admin bootstrap (so an empty deployment is usable).
	return u.AllowFirstRunAdmin == nil || *u.AllowFirstRunAdmin
}

type StorageConfig struct {
	BaseDir string `yaml:"base_dir"`
}

// IdentityConfig is the single operator principal bound onto issued OAuth tokens
// (the same provisional single-user identity Shoka stamps).
type IdentityConfig struct {
	User UserIdentity `yaml:"user"`
}

type UserIdentity struct {
	Name  string `yaml:"name"`
	Email string `yaml:"email"`
}

// OAuthConfig holds the token lifetimes for the authorization server. Only consulted
// when the OAuth transport is enabled (server.mcp.oauth.listen set).
type OAuthConfig struct {
	AccessTokenTTL       Duration `yaml:"access_token_ttl"`
	RefreshTokenTTL      Duration `yaml:"refresh_token_ttl"`
	AuthorizationCodeTTL Duration `yaml:"authorization_code_ttl"`
}

// Duration is a YAML-friendly time.Duration that accepts Go duration strings
// ("1h", "720h", "5m").
type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return err
	}
	if s == "" {
		*d = 0
		return nil
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// Std returns the value as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// Or returns the duration, or fallback when it is zero/unset.
func (d Duration) Or(fallback time.Duration) time.Duration {
	if d == 0 {
		return fallback
	}
	return time.Duration(d)
}

// OAuthEnabled reports whether the OAuth MCP transport (and thus the authorization
// server) is active — keyed on the presence of its listen address, as in Shoka.
func (c *Config) OAuthEnabled() bool { return c.Server.MCP.OAuth.Listen != "" }

// Load reads and validates the YAML config at path.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true) // strict: reject unknown/misplaced keys (Shoka's stance)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) normalize() error {
	base, err := expandTilde(c.Storage.BaseDir)
	if err != nil {
		return err
	}
	c.Storage.BaseDir = base
	if c.Storage.BaseDir == "" {
		return fmt.Errorf("storage.base_dir is required")
	}
	if c.Server.HTTP.Listen == "" {
		return fmt.Errorf("server.http.listen is required")
	}
	if c.Server.MCP.Plain.Listen == "" && c.Server.MCP.OAuth.Listen == "" {
		return fmt.Errorf("at least one MCP transport must be configured (server.mcp.plain.listen or server.mcp.oauth.listen)")
	}
	return nil
}

// expandTilde expands a leading ~/ to the user's home directory.
func expandTilde(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot resolve home directory for %q: %w", p, err)
		}
		if p == "~" {
			return home, nil
		}
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}
