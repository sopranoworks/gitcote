// Package npmgo installs npm dependencies from a package-lock.json into a
// node_modules directory. It is pure Go with no external dependencies:
// parse the lock file, download tarballs from the registry (or a local
// content-addressed cache), verify their SHA-512 integrity, and extract
// them to disk.
package npmgo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Options configures an Install.
type Options struct {
	LockfilePath string // default: "package-lock.json"
	TargetDir    string // default: "node_modules"
	CacheDir     string // default: "<user cache>/npmgo"; cache is keyed by integrity hash
	Registry     string // default: "https://registry.npmjs.org"
	Token        string // optional Bearer token for private registries
	Concurrency  int    // default: 8
	Production   bool   // skip devDependencies when true
	CacheOnly    bool   // download tarballs to cache without extracting (see CachePackages)
	Timeout      time.Duration

	// Logf, when set, receives human-readable progress lines, including
	// per-package cache/download/skip status. It may be called
	// concurrently and must be safe for concurrent use.
	Logf func(format string, args ...any)
}

func (o *Options) applyDefaults() {
	if o.LockfilePath == "" {
		o.LockfilePath = "package-lock.json"
	}
	if o.TargetDir == "" {
		o.TargetDir = "node_modules"
	}
	if o.Registry == "" {
		o.Registry = defaultRegistry
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 8
	}
	if o.CacheDir == "" {
		if d, err := os.UserCacheDir(); err == nil {
			o.CacheDir = filepath.Join(d, "npmgo")
		}
	}
}

func (o *Options) logf(format string, args ...any) {
	if o.Logf != nil {
		o.Logf(format, args...)
	}
}

// pkgStatus records how a package was satisfied during an install.
type pkgStatus int

const (
	statusSkipped    pkgStatus = iota // already installed at the wanted version
	statusCached                      // tarball read from the local cache
	statusDownloaded                  // tarball fetched over HTTP
)

// Install reads the lock file referenced by opts, then resolves every
// (non-skipped) package: packages already installed at the correct version
// are skipped, otherwise the tarball is taken from the cache or downloaded,
// verified, cached, and extracted into opts.TargetDir.
//
// Optional-dependency failures are logged and tolerated; any other package
// failure aborts the install and is returned.
func Install(opts Options) error {
	opts.applyDefaults()

	pkgs, err := ParseLockfileFile(opts.LockfilePath)
	if err != nil {
		return err
	}

	work := make([]Package, 0, len(pkgs))
	for _, p := range pkgs {
		if opts.Production && p.Dev {
			continue
		}
		work = append(work, p)
	}
	total := len(work)
	opts.logf("npmgo install — %d package(s)", total)

	c := newClient(RegistryConfig{URL: opts.Registry, Token: opts.Token}, opts.Timeout)
	ca := newCache(opts.CacheDir)

	var (
		sem        = make(chan struct{}, opts.Concurrency)
		wg         sync.WaitGroup
		mu         sync.Mutex
		firstErr   error
		cached     atomic.Int64
		downloaded atomic.Int64
		skipped    atomic.Int64
	)

	for _, p := range work {
		mu.Lock()
		aborted := firstErr != nil
		mu.Unlock()
		if aborted {
			break
		}

		wg.Add(1)
		sem <- struct{}{}
		go func(p Package) {
			defer wg.Done()
			defer func() { <-sem }()

			st, err := installOne(c, ca, p, opts)
			name := packageName(p.Path)
			if err != nil {
				if p.Optional {
					opts.logf("  ⚠ %s@%s skipped (optional): %v", name, p.Version, err)
					return
				}
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("%s: %w", p.Path, err)
				}
				mu.Unlock()
				return
			}
			switch st {
			case statusSkipped:
				skipped.Add(1)
				opts.logf("  ✓ %s@%s (already installed)", name, p.Version)
			case statusCached:
				cached.Add(1)
				opts.logf("  ✓ %s@%s (cached)", name, p.Version)
			case statusDownloaded:
				downloaded.Add(1)
				opts.logf("  ✓ %s@%s (installed)", name, p.Version)
			}
		}(p)
	}
	wg.Wait()

	if firstErr != nil {
		return firstErr
	}
	opts.logf("Done: %d packages (%d cached, %d downloaded, %d already installed)",
		total, cached.Load(), downloaded.Load(), skipped.Load())
	return nil
}

// installOne resolves a single package: skip if already installed at the
// wanted version, else obtain the tarball (cache hit or download), cache a
// freshly downloaded tarball, and extract.
func installOne(c *client, ca *cache, p Package, opts Options) (pkgStatus, error) {
	target := installTarget(opts.TargetDir, p.Path)

	if alreadyInstalled(target, p.Version) {
		return statusSkipped, nil
	}

	if data, ok := ca.get(p.Integrity); ok {
		if err := extractPackage(data, target); err != nil {
			return statusCached, fmt.Errorf("extract: %w", err)
		}
		return statusCached, nil
	}

	opts.logf("  ↓ %s@%s (downloading)", packageName(p.Path), p.Version)
	data, err := c.downloadTarball(p.Resolved, p.Integrity)
	if err != nil {
		return statusDownloaded, err
	}
	if err := ca.put(p.Integrity, data); err != nil {
		opts.logf("  ⚠ cache write %s: %v", p.Path, err) // non-fatal
	}
	if err := extractPackage(data, target); err != nil {
		return statusDownloaded, fmt.Errorf("extract: %w", err)
	}
	return statusDownloaded, nil
}

// alreadyInstalled reports whether targetPath holds a package.json whose
// version matches wantVersion. An empty wantVersion (no recorded version)
// is never considered already installed, so it is always (re)extracted.
func alreadyInstalled(targetPath, wantVersion string) bool {
	if wantVersion == "" {
		return false
	}
	data, err := os.ReadFile(filepath.Join(targetPath, "package.json"))
	if err != nil {
		return false
	}
	var pj struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &pj); err != nil {
		return false
	}
	return pj.Version == wantVersion
}

// installTarget maps a lockfile path ("node_modules/react" or a nested
// "node_modules/a/node_modules/b") onto opts.TargetDir by replacing the
// leading "node_modules/" segment with TargetDir.
func installTarget(targetDir, lockPath string) string {
	rel := strings.TrimPrefix(lockPath, "node_modules/")
	return filepath.Join(targetDir, filepath.FromSlash(rel))
}

// packageName extracts the package name from a lockfile install path,
// e.g. "node_modules/a/node_modules/@scope/b" -> "@scope/b".
func packageName(lockPath string) string {
	idx := strings.LastIndex(lockPath, "node_modules/")
	if idx < 0 {
		return lockPath
	}
	return lockPath[idx+len("node_modules/"):]
}
