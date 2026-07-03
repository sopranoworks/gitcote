package npmgo

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// extractPackage extracts an npm tarball into targetDir. npm tarballs wrap
// every entry under a leading directory (conventionally "package/"); that
// first path component is stripped so files land directly in targetDir.
//
// File modes are preserved (so bin/ scripts stay executable) and symlinks
// are recreated. Entries that would escape targetDir are rejected.
func extractPackage(tarballData []byte, targetDir string) error {
	gz, err := gzip.NewReader(bytes.NewReader(tarballData))
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}
	// Clean once so the zip-slip guard compares against a normalized root.
	root := filepath.Clean(targetDir)

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}

		rel := stripPackagePrefix(hdr.Name)
		if rel == "" {
			continue
		}

		target := filepath.Join(root, rel)
		if !withinRoot(root, target) {
			return fmt.Errorf("tar entry %q escapes target directory", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := writeFile(target, tr, fileMode(hdr.Mode)); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := writeSymlink(target, hdr.Linkname); err != nil {
				return err
			}
		case tar.TypeLink:
			// Hard links are rare in npm tarballs; resolve relative to root.
			linkTarget := filepath.Join(root, stripPackagePrefix(hdr.Linkname))
			if !withinRoot(root, linkTarget) {
				return fmt.Errorf("hardlink %q escapes target directory", hdr.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			os.Remove(target)
			if err := os.Link(linkTarget, target); err != nil {
				return err
			}
		default:
			// Skip char/block devices, fifos, etc.
		}
	}
	return nil
}

func writeFile(target string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func writeSymlink(target, linkname string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	os.Remove(target) // overwrite any existing entry
	return os.Symlink(linkname, target)
}

// stripPackagePrefix removes the leading path component (e.g. "package/")
// from a tar entry name and normalizes separators.
func stripPackagePrefix(name string) string {
	name = strings.TrimPrefix(filepath.ToSlash(name), "./")
	idx := strings.IndexByte(name, '/')
	if idx < 0 {
		return "" // top-level entry with no inner path (the wrapper dir)
	}
	return strings.Trim(name[idx+1:], "/")
}

// fileMode derives a sane file permission from the tar header mode,
// defaulting to 0644 when the archive records no usable bits.
func fileMode(mode int64) os.FileMode {
	m := os.FileMode(mode).Perm()
	if m == 0 {
		return 0o644
	}
	return m
}

// withinRoot reports whether target is inside root (after cleaning),
// guarding against path-traversal entries.
func withinRoot(root, target string) bool {
	if target == root {
		return true
	}
	return strings.HasPrefix(target, root+string(os.PathSeparator))
}
