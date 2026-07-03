package npmgo

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultRegistry = "https://registry.npmjs.org"

// RegistryConfig describes how to reach a package registry. Token, when
// set, is sent as a Bearer credential for private-registry access.
type RegistryConfig struct {
	URL   string // base registry URL (used for relative-path fallbacks)
	Token string // optional Bearer token for private registries
}

// client downloads and verifies tarballs.
type client struct {
	http *http.Client
	cfg  RegistryConfig
	// codeloadBase is GitHub's tarball endpoint for git dependencies;
	// overridable in tests.
	codeloadBase string
}

func newClient(cfg RegistryConfig, timeout time.Duration) *client {
	if cfg.URL == "" {
		cfg.URL = defaultRegistry
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &client{
		http:         &http.Client{Timeout: timeout},
		cfg:          cfg,
		codeloadBase: defaultCodeloadURL,
	}
}

// downloadTarball fetches url, verifies it against integrity, and returns
// the tarball bytes. Transient HTTP failures are retried once. An
// integrity mismatch fails immediately and is never retried, since a
// mismatching body indicates tampering rather than a transient error.
func (c *client) downloadTarball(url, integrity string) ([]byte, error) {
	const attempts = 2
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		data, err := c.fetch(url)
		if err != nil {
			lastErr = err
			continue
		}
		if err := verifyIntegrity(data, integrity); err != nil {
			return nil, fmt.Errorf("%s: %w", url, err)
		}
		return data, nil
	}
	return nil, fmt.Errorf("download %s failed after %d attempts: %w", url, attempts, lastErr)
}

// versionManifest is the slice of a registry version document we need: the
// tarball URL and integrity recorded under "dist".
type versionManifest struct {
	Dist struct {
		Tarball   string `json:"tarball"`
		Integrity string `json:"integrity"`
	} `json:"dist"`
}

// resolveDist looks up a package version's tarball URL and integrity from the
// registry, for lockfile entries that omit them (npm workspace dependencies
// are commonly recorded under "<ws>/node_modules/<pkg>" with only a version).
// It fetches the per-version manifest at <registry>/<name>/<version>, which is
// far smaller than the full packument. Scoped names keep their "/" verbatim,
// which registry.npmjs.org accepts. A transient HTTP failure is retried once.
func (c *client) resolveDist(name, version string) (tarball, integrity string, err error) {
	if version == "" {
		return "", "", fmt.Errorf("cannot resolve %s: no version in lockfile", name)
	}
	url := strings.TrimSuffix(c.cfg.URL, "/") + "/" + name + "/" + version

	const attempts = 2
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		data, err := c.fetch(url)
		if err != nil {
			lastErr = err
			continue
		}
		var m versionManifest
		if err := json.Unmarshal(data, &m); err != nil {
			return "", "", fmt.Errorf("parse manifest %s: %w", url, err)
		}
		if m.Dist.Tarball == "" {
			return "", "", fmt.Errorf("manifest %s has no tarball URL", url)
		}
		return m.Dist.Tarball, m.Dist.Integrity, nil
	}
	return "", "", fmt.Errorf("resolve %s@%s failed after %d attempts: %w", name, version, attempts, lastErr)
}

// fetchRetry performs a GET with a single retry on transient failure, without
// integrity verification. It is used for GitHub codeload archives, whose bytes
// are not content-addressed by an npm integrity hash (the commit is the trust
// anchor).
func (c *client) fetchRetry(url string) ([]byte, error) {
	const attempts = 2
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		data, err := c.fetch(url)
		if err != nil {
			lastErr = err
			continue
		}
		return data, nil
	}
	return nil, fmt.Errorf("fetch %s failed after %d attempts: %w", url, attempts, lastErr)
}

// fetch performs a single GET. The default http.Client follows redirects.
func (c *client) fetch(url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Drain a little of the body for context, then discard.
		io.CopyN(io.Discard, resp.Body, 512)
		return nil, fmt.Errorf("unexpected status %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return data, nil
}
