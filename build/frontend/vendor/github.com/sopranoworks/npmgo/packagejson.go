package npmgo

import (
	"encoding/json"
	"strings"
)

// PackageJSON holds the subset of package.json fields needed for module
// resolution. Exports and Browser are kept raw because their shapes vary
// (string, condition map, or subpath map) and are parsed on demand.
type PackageJSON struct {
	Name    string          `json:"name"`
	Version string          `json:"version"`
	Main    string          `json:"main"`
	Module  string          `json:"module"`
	Exports json.RawMessage `json:"exports"`
	Imports json.RawMessage `json:"imports"`
	Browser json.RawMessage `json:"browser"`
}

// conditionPriority is the order in which export/condition keys are tried.
// It is biased toward browser ESM bundling: prefer browser and ESM entries,
// fall back to default/require (CommonJS).
var conditionPriority = []string{"browser", "import", "module", "default", "require", "node"}

// mainEntry returns the package's main entry file, applying the Node.js
// resolution order: exports["."] -> module -> main -> index.js.
func (pj *PackageJSON) mainEntry() string {
	if entry, ok := resolveExports(pj.Exports, "."); ok {
		return entry
	}
	if pj.Module != "" {
		return pj.Module
	}
	if pj.Main != "" {
		return pj.Main
	}
	return "index.js"
}

// subpathEntry resolves a subpath import (e.g. "jsx-runtime" for
// "react/jsx-runtime"). It consults the exports map first, then falls back
// to treating the subpath as a direct file path.
func (pj *PackageJSON) subpathEntry(subpath string) string {
	if entry, ok := resolveExports(pj.Exports, "./"+subpath); ok {
		return entry
	}
	return subpath
}

// importsEntry resolves a Node.js internal import specifier (one beginning
// with "#", looked up in the package's "imports" map) to a target file. The
// map values may be plain strings or condition maps, resolved with the same
// browser-biased condition priority as exports.
func (pj *PackageJSON) importsEntry(spec string) (string, bool) {
	if len(pj.Imports) == 0 {
		return "", false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(pj.Imports, &m) != nil {
		return "", false
	}
	v, ok := m[spec]
	if !ok {
		return "", false
	}
	return resolveConditions(v)
}

// resolveExports resolves a subpath ("." or "./name") against a raw exports
// value. It understands three shapes:
//   - string:            "./index.js"            (only valid for ".")
//   - condition map:     {"import": "./esm.js", "default": "./cjs.js"}
//   - subpath map:       {".": ..., "./client": ...}
//
// and condition maps nested within subpath entries.
func resolveExports(raw json.RawMessage, subpath string) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}

	// Try as a string first (sugar for the "." entry).
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if subpath == "." {
			return s, true
		}
		return "", false
	}

	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return "", false
	}

	if isSubpathMap(m) {
		if v, ok := m[subpath]; ok {
			return resolveConditions(v)
		}
		return "", false
	}

	// A bare condition map applies only to the "." subpath.
	if subpath == "." {
		return resolveConditions(raw)
	}
	return "", false
}

// resolveConditions resolves a value that is either a target string or a map
// of conditions to targets, picking the first condition in conditionPriority.
func resolveConditions(raw json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return "", false
	}
	for _, cond := range conditionPriority {
		if v, ok := m[cond]; ok {
			if r, ok := resolveConditions(v); ok {
				return r, true
			}
		}
	}
	return "", false
}

// isSubpathMap reports whether a map's keys are subpaths ("." / "./x") rather
// than condition names ("import", "default", ...).
func isSubpathMap(m map[string]json.RawMessage) bool {
	for k := range m {
		if k == "." || strings.HasPrefix(k, "./") {
			return true
		}
	}
	return false
}

// splitSpecifier splits a bare import specifier into its package name and
// subpath, handling scoped packages:
//
//	"react"                 -> ("react", "")
//	"react/jsx-runtime"     -> ("react", "jsx-runtime")
//	"@tanstack/react-query" -> ("@tanstack/react-query", "")
//	"@scope/pkg/sub/path"   -> ("@scope/pkg", "sub/path")
func splitSpecifier(spec string) (name, subpath string) {
	parts := strings.Split(spec, "/")
	if strings.HasPrefix(spec, "@") {
		if len(parts) < 2 {
			return spec, ""
		}
		name = parts[0] + "/" + parts[1]
		subpath = strings.Join(parts[2:], "/")
		return name, subpath
	}
	name = parts[0]
	subpath = strings.Join(parts[1:], "/")
	return name, subpath
}
