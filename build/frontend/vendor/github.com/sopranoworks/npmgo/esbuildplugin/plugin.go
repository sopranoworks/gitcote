// Package esbuildplugin provides an esbuild plugin that resolves bare npm
// imports directly from an npmgo tarball cache, so a frontend can be bundled
// without ever creating a node_modules directory.
//
// It lives in its own Go module so the core npmgo installer stays free of
// external dependencies; only this package pulls in esbuild.
package esbuildplugin

import (
	"path"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/sopranoworks/npmgo"
)

// namespace tags virtual modules that are served from the tarball cache.
const namespace = "npmgo"

// New returns an esbuild plugin backed by the given PackageIndex. Bare
// imports (e.g. "react", "react/jsx-runtime", "@scope/pkg") and relative
// imports within a cached package are resolved from the index; everything
// else is left to esbuild's default resolver.
func New(index *npmgo.PackageIndex) api.Plugin {
	return api.Plugin{
		Name: "npmgo",
		Setup: func(build api.PluginBuild) {
			build.OnResolve(api.OnResolveOptions{Filter: ".*"}, func(args api.OnResolveArgs) (api.OnResolveResult, error) {
				// Relative import from a module we are already serving:
				// resolve it within the importer's package.
				if args.Namespace == namespace && isRelative(args.Path) {
					pkg, file := parsePath(args.Importer)
					p, f, ok := index.ResolveImport(args.Path, pkg, path.Dir(file))
					if !ok {
						return api.OnResolveResult{}, nil
					}
					return api.OnResolveResult{Path: encodePath(p, f), Namespace: namespace}, nil
				}

				// Internal "#" import (Node "imports" field): resolve against
				// the importer package's own package.json.
				if args.Namespace == namespace && strings.HasPrefix(args.Path, "#") {
					pkg, _ := parsePath(args.Importer)
					p, f, ok := index.ResolvePackageImport(args.Path, pkg)
					if !ok {
						return api.OnResolveResult{}, nil
					}
					return api.OnResolveResult{Path: encodePath(p, f), Namespace: namespace}, nil
				}

				// Bare import (from the entry point or from a cached module):
				// resolve the package entry / subpath from the index.
				if isBare(args.Path) {
					p, f, ok := index.ResolveImport(args.Path, "", "")
					if !ok {
						return api.OnResolveResult{}, nil // let esbuild try
					}
					return api.OnResolveResult{Path: encodePath(p, f), Namespace: namespace}, nil
				}

				return api.OnResolveResult{}, nil
			})

			build.OnLoad(api.OnLoadOptions{Filter: ".*", Namespace: namespace}, func(args api.OnLoadArgs) (api.OnLoadResult, error) {
				pkg, file := parsePath(args.Path)
				r, err := index.Reader(pkg)
				if err != nil {
					return api.OnLoadResult{}, err
				}
				data, err := r.ReadFile(file)
				if err != nil {
					return api.OnLoadResult{}, err
				}
				contents := string(data)
				loader := loaderFor(file)
				return api.OnLoadResult{Contents: &contents, Loader: loader}, nil
			})
		},
	}
}

// encodePath joins a package name and file into a namespace path. Scoped
// names keep their "@scope/name" prefix; parsePath reverses this.
func encodePath(pkg, file string) string {
	return pkg + "/" + file
}

// parsePath splits a namespace path back into (package, file).
func parsePath(p string) (pkg, file string) {
	parts := strings.Split(p, "/")
	if strings.HasPrefix(p, "@") {
		if len(parts) < 3 {
			return p, ""
		}
		return parts[0] + "/" + parts[1], strings.Join(parts[2:], "/")
	}
	if len(parts) < 2 {
		return p, ""
	}
	return parts[0], strings.Join(parts[1:], "/")
}

func isRelative(p string) bool {
	return strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../")
}

// isBare reports whether a specifier is a bare module import (a package name)
// rather than a relative or absolute path.
func isBare(p string) bool {
	if p == "" {
		return false
	}
	if isRelative(p) || strings.HasPrefix(p, "/") {
		return false
	}
	// Windows-style absolute paths and URLs are not bare specifiers.
	if strings.Contains(p, "://") || (len(p) > 1 && p[1] == ':') {
		return false
	}
	return true
}

func loaderFor(file string) api.Loader {
	switch {
	case strings.HasSuffix(file, ".jsx"):
		return api.LoaderJSX
	case strings.HasSuffix(file, ".ts"):
		return api.LoaderTS
	case strings.HasSuffix(file, ".tsx"):
		return api.LoaderTSX
	case strings.HasSuffix(file, ".json"):
		return api.LoaderJSON
	case strings.HasSuffix(file, ".module.css"):
		// CSS Modules: locally-scoped class names. Checked before the
		// plain ".css" case since a module file also ends in ".css".
		return api.LoaderLocalCSS
	case strings.HasSuffix(file, ".css"):
		return api.LoaderCSS
	default:
		// .js, .cjs, .mjs and anything else: treat as JavaScript.
		return api.LoaderJS
	}
}
