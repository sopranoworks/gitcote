package npmgo

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// CachedPackage describes one package whose tarball lives in the cache but
// is not extracted to a node_modules directory.
type CachedPackage struct {
	Name        string // import name, e.g. "react" or "@scope/pkg"
	Version     string
	TarballPath string // path to the .tgz in the cache
	Integrity   string
}

// PackageIndex maps import names to their cached tarballs and lazily opens
// (and caches) a TarballReader per package for module resolution. It is safe
// for concurrent use.
type PackageIndex struct {
	Packages map[string]CachedPackage

	mu      sync.Mutex
	readers map[string]*TarballReader
}

// CachePackages parses the lock file and ensures every (non-skipped) package
// tarball is present in the cache, downloading and verifying as needed. It
// never extracts to disk and creates no node_modules directory. The returned
// PackageIndex maps each package's import name to its cached tarball.
//
// CachePackages forces CacheOnly behaviour regardless of opts.CacheOnly.
func CachePackages(opts Options) (*PackageIndex, error) {
	opts.CacheOnly = true
	opts.applyDefaults()
	if opts.CacheDir == "" {
		return nil, fmt.Errorf("CachePackages requires a cache directory")
	}

	pkgs, err := ParseLockfileFile(opts.LockfilePath)
	if err != nil {
		return nil, err
	}
	pkgs = dedupePackages(pkgs)

	c := newClient(RegistryConfig{URL: opts.Registry, Token: opts.Token}, opts.Timeout)
	ca := newCache(opts.CacheDir)

	var (
		sem        = make(chan struct{}, opts.Concurrency)
		wg         sync.WaitGroup
		mu         sync.Mutex
		firstErr   error
		cached     atomic.Int64
		downloaded atomic.Int64
		results    = make([]CachedPackage, len(pkgs))
		ok         = make([]bool, len(pkgs))
	)

	for i, p := range pkgs {
		if opts.Production && p.Dev {
			continue
		}
		mu.Lock()
		aborted := firstErr != nil
		mu.Unlock()
		if aborted {
			break
		}

		wg.Add(1)
		sem <- struct{}{}
		go func(i int, p Package) {
			defer wg.Done()
			defer func() { <-sem }()

			tpath, st, err := ensureCached(c, ca, p)
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
			results[i] = CachedPackage{Name: name, Version: p.Version, TarballPath: tpath, Integrity: p.Integrity}
			ok[i] = true
			if st == statusCached {
				cached.Add(1)
				opts.logf("  ✓ %s@%s (cached)", name, p.Version)
			} else {
				downloaded.Add(1)
				opts.logf("  ✓ %s@%s (downloaded)", name, p.Version)
			}
		}(i, p)
	}
	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}

	idx := &PackageIndex{
		Packages: make(map[string]CachedPackage),
		readers:  make(map[string]*TarballReader),
	}
	// dedupePackages already collapsed each import name to a single entry, so
	// names here are unique.
	for i := range pkgs {
		if !ok[i] {
			continue
		}
		idx.Packages[results[i].Name] = results[i]
	}

	opts.logf("Cached: %d packages (%d cached, %d downloaded)", len(idx.Packages), cached.Load(), downloaded.Load())
	return idx, nil
}

// dedupePackages collapses lockfile entries that share an import name down to
// the single best entry to download. The index is flat by import name, so at
// most one version of a name can be served; an entry that already carries a
// resolved tarball URL is preferred (no registry lookup, authoritative), and
// among equals the shallowest (hoisted) install path wins. The result is
// sorted by path for deterministic downloads and logs.
func dedupePackages(pkgs []Package) []Package {
	best := make(map[string]int, len(pkgs))
	for i := range pkgs {
		name := packageName(pkgs[i].Path)
		if j, seen := best[name]; !seen || betterPackage(pkgs[i], pkgs[j]) {
			best[name] = i
		}
	}
	out := make([]Package, 0, len(best))
	for _, i := range best {
		out = append(out, pkgs[i])
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Path < out[b].Path })
	return out
}

// betterPackage reports whether a is a better download source than b for the
// same import name: a directly-resolved entry beats one needing a registry
// lookup, then a shallower install path beats a more deeply nested one.
func betterPackage(a, b Package) bool {
	if (a.Resolved != "") != (b.Resolved != "") {
		return a.Resolved != ""
	}
	return pkgDepth(a.Path) < pkgDepth(b.Path)
}

// ensureCached makes sure a package's tarball is in the content-addressed
// cache, downloading and verifying it on a miss, and returns its cache path.
func ensureCached(c *client, ca *cache, p Package) (string, pkgStatus, error) {
	// Dependencies installed straight from a GitHub commit carry a git+ URL and
	// no registry integrity; they are downloaded from codeload and repacked as
	// a standard npm tarball rather than looked up on the registry.
	if isGitURL(p.Resolved) {
		return ensureCachedGit(c, ca, p)
	}
	// Workspace dependencies are often recorded without a resolved URL or
	// integrity; look them up from the registry so they can be cached like
	// any other package rather than being silently skipped.
	if p.Resolved == "" || p.Integrity == "" {
		// npm aliases install under one name but publish under another; the
		// registry must be queried by the published name (p.Name), falling
		// back to the install name for ordinary packages.
		regName := p.Name
		if regName == "" {
			regName = packageName(p.Path)
		}
		tarball, integrity, err := c.resolveDist(regName, p.Version)
		if err != nil {
			return "", 0, err
		}
		if p.Resolved == "" {
			p.Resolved = tarball
		}
		if p.Integrity == "" {
			p.Integrity = integrity
		}
	}

	tpath, addressable := ca.path(p.Integrity)
	if !addressable {
		return "", 0, fmt.Errorf("no usable integrity hash; cannot cache %q", p.Resolved)
	}
	if _, err := os.Stat(tpath); err == nil {
		return tpath, statusCached, nil
	}
	data, err := c.downloadTarball(p.Resolved, p.Integrity)
	if err != nil {
		return "", 0, err
	}
	if err := ca.put(p.Integrity, data); err != nil {
		return "", 0, fmt.Errorf("cache write: %w", err)
	}
	return tpath, statusDownloaded, nil
}

// ensureCachedGit resolves a git+ dependency: it downloads the pinned GitHub
// archive from codeload, extracts the package's subtree (matching the lockfile
// name against each package.json for monorepos), repacks it as a standard npm
// tarball, and stores it in the content-addressed cache. The cache key is
// derived from the commit and package name, so a second run is a plain cache
// hit with no download.
func ensureCachedGit(c *client, ca *cache, p Package) (string, pkgStatus, error) {
	ref, err := parseGitURL(p.Resolved)
	if err != nil {
		return "", 0, err
	}
	name := p.Name
	if name == "" {
		name = packageName(p.Path)
	}

	key := gitCacheKey(ref.commit, name)
	tpath, ok := ca.path(key)
	if !ok {
		return "", 0, fmt.Errorf("cache unavailable; cannot cache git dependency %q", p.Resolved)
	}
	if _, err := os.Stat(tpath); err == nil {
		return tpath, statusCached, nil
	}

	url := ref.codeloadURL(c.codeloadBase)
	repoTarball, err := c.fetchRetry(url)
	if err != nil {
		return "", 0, fmt.Errorf("download git dependency %s: %w", url, err)
	}
	npmTarball, err := repackGitSubdir(repoTarball, name)
	if err != nil {
		return "", 0, fmt.Errorf("repack git dependency %q: %w", p.Resolved, err)
	}
	if err := ca.put(key, npmTarball); err != nil {
		return "", 0, fmt.Errorf("cache write: %w", err)
	}
	return tpath, statusDownloaded, nil
}

// Reader returns the (cached) TarballReader for a package by import name.
func (idx *PackageIndex) Reader(name string) (*TarballReader, error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if r, ok := idx.readers[name]; ok {
		return r, nil
	}
	cp, ok := idx.Packages[name]
	if !ok {
		return nil, fmt.Errorf("package not in index: %s", name)
	}
	r, err := OpenTarball(cp.TarballPath)
	if err != nil {
		return nil, err
	}
	idx.readers[name] = r
	return r, nil
}

// ResolveImport resolves an import specifier to a concrete file inside a
// cached package. For a bare specifier ("react", "react/jsx-runtime",
// "@scope/pkg") the entry is resolved from the target package's package.json.
// For a relative specifier ("./x", "../y") it is resolved within the
// importer's package (importerPkg / importerDir).
//
// It returns the resolved package import name, the package-relative file
// path, and whether resolution succeeded. A miss (e.g. an unknown package)
// returns ok=false so the caller can fall back to default resolution.
func (idx *PackageIndex) ResolveImport(spec, importerPkg, importerDir string) (pkg, file string, ok bool) {
	if isRelative(spec) {
		if importerPkg == "" {
			return "", "", false
		}
		r, err := idx.Reader(importerPkg)
		if err != nil {
			return "", "", false
		}
		candidate := path.Join(importerDir, spec)
		resolved, ok := resolveFile(r, candidate)
		return importerPkg, resolved, ok
	}

	name, subpath := splitSpecifier(spec)
	r, err := idx.Reader(name)
	if err != nil {
		return "", "", false
	}
	pj, err := r.ReadPackageJSON()
	if err != nil {
		return "", "", false
	}
	var entry string
	if subpath == "" {
		entry = pj.mainEntry()
	} else {
		entry = pj.subpathEntry(subpath)
	}
	resolved, ok := resolveFile(r, entry)
	return name, resolved, ok
}

// ResolvePackageImport resolves a Node.js internal import specifier (one
// starting with "#") against the importing package's package.json "imports"
// map, returning a file within that same package. A miss returns ok=false.
func (idx *PackageIndex) ResolvePackageImport(spec, importerPkg string) (pkg, file string, ok bool) {
	if importerPkg == "" {
		return "", "", false
	}
	r, err := idx.Reader(importerPkg)
	if err != nil {
		return "", "", false
	}
	pj, err := r.ReadPackageJSON()
	if err != nil {
		return "", "", false
	}
	entry, ok := pj.importsEntry(spec)
	if !ok {
		return "", "", false
	}
	resolved, ok := resolveFile(r, entry)
	return importerPkg, resolved, ok
}

// resolveFile turns a resolution candidate into an existing file in the
// tarball, mirroring Node's file/directory resolution: the path as-is, then
// common extensions, then — when the candidate is a directory — a nested
// package.json's entry, and finally an index file.
func resolveFile(r *TarballReader, candidate string) (string, bool) {
	candidate = normalizeTarPath(candidate)
	if candidate == "" {
		candidate = "index.js"
	}
	if resolved, ok := resolveAsFile(r, candidate); ok {
		return resolved, true
	}
	// Directory resolution: honor a nested package.json's main entry (used by
	// packages that expose a subpath via a redirect dir, e.g. "pkg/constants"
	// -> "constants/package.json" -> "../dist/constants.js"), then index.*.
	if data, err := r.ReadFile(path.Join(candidate, "package.json")); err == nil {
		var pj PackageJSON
		if json.Unmarshal(data, &pj) == nil {
			target := path.Join(candidate, pj.mainEntry())
			if resolved, ok := resolveAsFile(r, target); ok {
				return resolved, true
			}
		}
	}
	return resolveAsFile(r, path.Join(candidate, "index"))
}

// resolveAsFile tries a candidate as a concrete file: as-is first, then with
// each of Node's implicit extensions.
func resolveAsFile(r *TarballReader, candidate string) (string, bool) {
	candidate = normalizeTarPath(candidate)
	for _, ext := range []string{"", ".js", ".cjs", ".mjs", ".json", ".ts", ".tsx"} {
		if r.has(candidate + ext) {
			return normalizeTarPath(candidate + ext), true
		}
	}
	return "", false
}

func isRelative(spec string) bool {
	return strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../")
}

// pkgDepth counts how deeply a lockfile path is nested (number of
// node_modules segments): "node_modules/react" -> 1,
// "node_modules/a/node_modules/b" -> 2.
func pkgDepth(lockPath string) int {
	return strings.Count(lockPath, "node_modules/")
}
