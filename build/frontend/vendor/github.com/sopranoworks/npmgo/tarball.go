package npmgo

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
)

// TarballReader provides random read access to the files inside a cached
// npm .tgz without extracting it to disk. The archive is decompressed once
// on Open and indexed by package-relative path (the leading "package/"
// directory is stripped); subsequent ReadFile calls are in-memory lookups.
//
// A TarballReader is safe for concurrent reads after Open returns: its
// contents are immutable.
type TarballReader struct {
	files map[string][]byte
	paths []string // sorted, for stable List()
}

// OpenTarball reads and indexes the gzip-compressed tar at path.
func OpenTarball(path string) (*TarballReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("gzip %s: %w", path, err)
	}
	defer gz.Close()

	files := make(map[string][]byte)
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar %s: %w", path, err)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		name := stripPackagePrefix(hdr.Name)
		if name == "" {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("read %s in %s: %w", name, path, err)
		}
		files[name] = data
	}

	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return &TarballReader{files: files, paths: paths}, nil
}

// ReadFile returns the contents of a single package-relative file. Leading
// "./" and "/" are normalized away, so "./index.js", "index.js" and
// "/index.js" are equivalent.
func (r *TarballReader) ReadFile(name string) ([]byte, error) {
	key := normalizeTarPath(name)
	data, ok := r.files[key]
	if !ok {
		return nil, fmt.Errorf("file not found in tarball: %s", name)
	}
	return data, nil
}

// has reports whether a package-relative file exists.
func (r *TarballReader) has(name string) bool {
	_, ok := r.files[normalizeTarPath(name)]
	return ok
}

// List returns all package-relative file paths, sorted.
func (r *TarballReader) List() []string {
	out := make([]string, len(r.paths))
	copy(out, r.paths)
	return out
}

// ReadPackageJSON reads and parses the package's package.json.
func (r *TarballReader) ReadPackageJSON() (*PackageJSON, error) {
	data, err := r.ReadFile("package.json")
	if err != nil {
		return nil, err
	}
	var pj PackageJSON
	if err := json.Unmarshal(data, &pj); err != nil {
		return nil, fmt.Errorf("parse package.json: %w", err)
	}
	return &pj, nil
}

// normalizeTarPath cleans a path to the form used as the file-index key.
func normalizeTarPath(name string) string {
	name = strings.TrimPrefix(name, "./")
	name = path.Clean("/" + name) // collapse .. and . segments
	return strings.TrimPrefix(name, "/")
}
