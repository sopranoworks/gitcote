package npmgo

import (
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// cache is a content-addressed tarball store. Tarballs are keyed by their
// Subresource Integrity hash and laid out as:
//
//	<dir>/<algo>/<hex-hash>.tgz   e.g. <dir>/sha512/ab12...ef.tgz
//
// Because the filename is the content hash, identical content maps to one
// file. The cache grows monotonically; there is no eviction.
type cache struct {
	dir string // base directory; empty disables the cache
}

func newCache(dir string) *cache {
	return &cache{dir: dir}
}

func (c *cache) enabled() bool { return c != nil && c.dir != "" }

// path returns the on-disk cache location for an integrity string, and
// whether caching applies (a supported, decodable hash is present).
func (c *cache) path(integrity string) (string, bool) {
	if !c.enabled() {
		return "", false
	}
	for _, entry := range strings.Fields(integrity) {
		algo, b64, ok := strings.Cut(entry, "-")
		if !ok {
			continue
		}
		switch algo {
		case "sha512", "sha256", "sha1":
		default:
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			continue
		}
		return filepath.Join(c.dir, algo, hex.EncodeToString(raw)+".tgz"), true
	}
	return "", false
}

// get returns the cached tarball for integrity if present and still valid.
// A cached file that fails integrity verification is removed and reported
// as a miss, so the caller re-downloads a fresh copy.
func (c *cache) get(integrity string) ([]byte, bool) {
	p, ok := c.path(integrity)
	if !ok {
		return nil, false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	if err := verifyIntegrity(data, integrity); err != nil {
		os.Remove(p) // corrupt entry: drop it
		return nil, false
	}
	return data, true
}

// put atomically stores data under the integrity-derived path. A temp file
// is written and renamed into place so readers never observe a partial
// tarball. Caching not applying (no usable hash) is not an error.
func (c *cache) put(integrity string, data []byte) error {
	p, ok := c.path(integrity)
	if !ok {
		return nil
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, p); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}
