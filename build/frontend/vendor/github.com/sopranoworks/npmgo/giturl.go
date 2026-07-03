package npmgo

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path"
	"sort"
	"strings"
)

// defaultCodeloadURL is GitHub's tarball endpoint. A commit-pinned archive is
// served at <base>/<owner>/<repo>/tar.gz/<commit>. It is overridable on the
// client for testing.
const defaultCodeloadURL = "https://codeload.github.com"

// isGitURL reports whether a lockfile "resolved" value is a git reference
// rather than an npm registry tarball URL. npm records these for dependencies
// installed straight from a GitHub commit; they carry no registry integrity
// and 404 if looked up on the registry.
func isGitURL(s string) bool {
	return strings.HasPrefix(s, "git+ssh://") ||
		strings.HasPrefix(s, "git+https://") ||
		strings.HasPrefix(s, "git+http://")
}

// gitRef is the GitHub coordinate extracted from a git+ URL.
type gitRef struct {
	owner  string
	repo   string
	commit string
}

// parseGitURL extracts owner/repo/commit from a git+ssh:// or git+https:// URL.
// Both "git+ssh://git@github.com/owner/repo.git#<commit>" and
// "git+https://github.com/owner/repo.git#<commit>" forms are accepted; the
// commit is the required "#" fragment. Only github.com is supported, matching
// the codeload endpoint used for downloads.
func parseGitURL(raw string) (gitRef, error) {
	if !isGitURL(raw) {
		return gitRef{}, fmt.Errorf("not a git URL: %q", raw)
	}
	s := strings.TrimPrefix(raw, "git+")

	// The commit lives in the "#" fragment; url.Parse would keep it in u.Fragment
	// but splitting first keeps the SSH/scp handling below simpler.
	body, commit, ok := strings.Cut(s, "#")
	if !ok || commit == "" {
		return gitRef{}, fmt.Errorf("git URL %q has no commit fragment", raw)
	}

	u, err := url.Parse(body)
	if err != nil {
		return gitRef{}, fmt.Errorf("parse git URL %q: %w", raw, err)
	}
	if !strings.EqualFold(u.Host, "github.com") {
		return gitRef{}, fmt.Errorf("git URL %q: only github.com is supported", raw)
	}

	p := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
	owner, repo, ok := strings.Cut(p, "/")
	if !ok || owner == "" || repo == "" {
		return gitRef{}, fmt.Errorf("git URL %q: cannot extract owner/repo from path %q", raw, u.Path)
	}
	return gitRef{owner: owner, repo: repo, commit: commit}, nil
}

// codeloadURL builds the GitHub tarball URL for this ref against a base
// endpoint (defaultCodeloadURL in production).
func (g gitRef) codeloadURL(base string) string {
	return fmt.Sprintf("%s/%s/%s/tar.gz/%s", strings.TrimSuffix(base, "/"), g.owner, g.repo, g.commit)
}

// gitCacheKey derives a stable, content-addressed cache key for a git
// dependency without downloading it. The commit hash pins the tree and the
// package name disambiguates the subpackages of a monorepo (which all share a
// commit but repack to different tarballs). The result is a valid SRI string
// so it slots into the normal integrity-keyed cache.
func gitCacheKey(commit, name string) string {
	sum := sha256.Sum256([]byte(commit + "\x00" + name))
	return "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
}

// repackGitSubdir turns a GitHub repository tarball into a standard npm
// tarball for a single package. GitHub archives wrap everything in a
// "<repo>-<commit>/" directory; this strips that wrapper, locates the
// subdirectory whose package.json "name" matches wantName (the whole repo for
// a single-package repo), and repacks that subtree under the conventional
// "package/" prefix so a TarballReader can consume it.
func repackGitSubdir(repoTarball []byte, wantName string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(repoTarball))
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	// Index the archive by repo-relative path (wrapper directory stripped).
	files := make(map[string][]byte)
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		rel := stripPackagePrefix(hdr.Name)
		if rel == "" {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", rel, err)
		}
		files[rel] = data
	}

	subdir, err := findPackageDir(files, wantName)
	if err != nil {
		return nil, err
	}

	// Collect every file under the chosen subdirectory, re-rooting each path so
	// the package's own package.json sits at the tarball root.
	prefix := ""
	if subdir != "." {
		prefix = subdir + "/"
	}
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for _, rel := range sortedKeys(files) {
		if prefix != "" && !strings.HasPrefix(rel, prefix) {
			continue
		}
		inner := strings.TrimPrefix(rel, prefix)
		body := files[rel]
		hdr := &tar.Header{
			Name:     "package/" + inner,
			Typeflag: tar.TypeReg,
			Mode:     0o644,
			Size:     int64(len(body)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, fmt.Errorf("write header %s: %w", inner, err)
		}
		if _, err := tw.Write(body); err != nil {
			return nil, fmt.Errorf("write body %s: %w", inner, err)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close tar: %w", err)
	}
	if err := gw.Close(); err != nil {
		return nil, fmt.Errorf("close gzip: %w", err)
	}
	return buf.Bytes(), nil
}

// findPackageDir returns the repo-relative directory (".", or a nested path)
// of the package.json whose "name" matches wantName. When nothing matches but
// a root package.json exists, the root is used — covering single-package repos
// whose lockfile entry omits an explicit name.
func findPackageDir(files map[string][]byte, wantName string) (string, error) {
	rootExists := false
	for _, rel := range sortedKeys(files) {
		if path.Base(rel) != "package.json" {
			continue
		}
		dir := path.Dir(rel)
		if dir == "." {
			rootExists = true
		}
		var pj struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(files[rel], &pj) != nil {
			continue
		}
		if wantName != "" && pj.Name == wantName {
			return dir, nil
		}
	}
	if rootExists {
		return ".", nil
	}
	return "", fmt.Errorf("no package.json matching name %q found in repository tarball", wantName)
}

// sortedKeys returns the map keys sorted, for deterministic tar output.
func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
