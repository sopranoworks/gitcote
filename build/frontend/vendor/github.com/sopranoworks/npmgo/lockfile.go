package npmgo

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Package is a single resolved dependency that must be downloaded and
// extracted. Path is the install location relative to the project root,
// e.g. "node_modules/react" or "node_modules/a/node_modules/b".
type Package struct {
	Path         string            // install path, e.g. "node_modules/react"
	Name         string            // published registry name; differs from the install path basename for npm aliases (install "string-width-cjs" -> published "string-width"). Empty when it matches.
	Version      string            // resolved version
	Resolved     string            // tarball URL
	Integrity    string            // subresource integrity, e.g. "sha512-..."
	Dependencies map[string]string // declared dependencies (informational)
	Dev          bool              // development-only dependency
	Optional     bool              // optional dependency (failure is non-fatal)
}

// rawLockfile mirrors the on-disk package-lock.json structure for
// lockfileVersion 1, 2 and 3.
type rawLockfile struct {
	LockfileVersion int                       `json:"lockfileVersion"`
	Packages        map[string]rawLockPackage `json:"packages"`
	Dependencies    map[string]rawLockDep     `json:"dependencies"`
}

// rawLockPackage is an entry in the v2/v3 "packages" map. The map key is
// the install path ("" for the root project).
type rawLockPackage struct {
	Name                 string            `json:"name"`
	Version              string            `json:"version"`
	Resolved             string            `json:"resolved"`
	Integrity            string            `json:"integrity"`
	Dependencies         map[string]string `json:"dependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	Dev                  bool              `json:"dev"`
	Optional             bool              `json:"optional"`
	Link                 bool              `json:"link"`
}

// rawLockDep is an entry in the legacy v1 "dependencies" tree.
type rawLockDep struct {
	Version      string                `json:"version"`
	Resolved     string                `json:"resolved"`
	Integrity    string                `json:"integrity"`
	Dev          bool                  `json:"dev"`
	Optional     bool                  `json:"optional"`
	Requires     map[string]string     `json:"requires"`
	Dependencies map[string]rawLockDep `json:"dependencies"`
}

// ParseLockfileFile reads and parses a package-lock.json from disk.
func ParseLockfileFile(path string) ([]Package, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read lockfile: %w", err)
	}
	return ParseLockfile(data)
}

// ParseLockfile parses package-lock.json bytes into the set of packages
// that must be downloaded. It supports lockfileVersion 1, 2 and 3.
//
// The v2/v3 "packages" map is preferred when present; otherwise the
// legacy v1 "dependencies" tree is walked. The root entry, and any
// file:/link: local references, are skipped.
func ParseLockfile(data []byte) ([]Package, error) {
	var lf rawLockfile
	if err := json.Unmarshal(data, &lf); err != nil {
		return nil, fmt.Errorf("parse lockfile: %w", err)
	}

	var pkgs []Package
	switch {
	case len(lf.Packages) > 0:
		for path, p := range lf.Packages {
			if path == "" {
				continue // the root project itself
			}
			if p.Link || isLocalRef(p.Resolved) {
				continue
			}
			// Only entries installed under a node_modules/ directory are
			// downloadable registry dependencies; workspace source dirs
			// (e.g. "web", "packages/foo") have a version but live in the
			// repo and must not be fetched.
			if !strings.Contains(path, "node_modules/") {
				continue
			}
			// A registry dependency needs at least a version so its tarball
			// can be located (directly via Resolved, or from the registry
			// when the lockfile omits it, as npm workspace entries do).
			if p.Resolved == "" && p.Version == "" {
				continue
			}
			pkgs = append(pkgs, Package{
				Path:         path,
				Name:         p.Name,
				Version:      p.Version,
				Resolved:     p.Resolved,
				Integrity:    p.Integrity,
				Dependencies: mergeDeps(p.Dependencies, p.OptionalDependencies),
				Dev:          p.Dev,
				Optional:     p.Optional,
			})
		}
	case len(lf.Dependencies) > 0:
		collectLegacyDeps(lf.Dependencies, "", &pkgs)
	default:
		return nil, fmt.Errorf("lockfile contains no packages")
	}

	// Deterministic order makes installs and tests reproducible.
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].Path < pkgs[j].Path })
	return pkgs, nil
}

// collectLegacyDeps walks the recursive v1 "dependencies" tree, building
// the nested node_modules install paths.
func collectLegacyDeps(deps map[string]rawLockDep, prefix string, out *[]Package) {
	for name, d := range deps {
		path := prefix + "node_modules/" + name
		if d.Resolved != "" && !isLocalRef(d.Resolved) {
			*out = append(*out, Package{
				Path:         path,
				Version:      d.Version,
				Resolved:     d.Resolved,
				Integrity:    d.Integrity,
				Dependencies: d.Requires,
				Dev:          d.Dev,
				Optional:     d.Optional,
			})
		}
		if len(d.Dependencies) > 0 {
			collectLegacyDeps(d.Dependencies, path+"/", out)
		}
	}
}

// isLocalRef reports whether a resolved URL points at a local path
// (file:/link:) rather than a downloadable tarball.
func isLocalRef(resolved string) bool {
	return strings.HasPrefix(resolved, "file:") || strings.HasPrefix(resolved, "link:")
}

func mergeDeps(a, b map[string]string) map[string]string {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
