package npmgo

import (
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"
)

// verifyIntegrity checks data against an npm Subresource Integrity (SRI)
// string of the form "sha512-<base64>". The integrity field may contain
// several space-separated hashes; data is considered valid if it matches
// any one of the supported entries.
//
// An empty integrity string is treated as "nothing to verify" and passes,
// matching npm's behaviour for packages without recorded hashes.
func verifyIntegrity(data []byte, integrity string) error {
	if strings.TrimSpace(integrity) == "" {
		return nil
	}

	supported := false
	for _, entry := range strings.Fields(integrity) {
		algo, b64, ok := strings.Cut(entry, "-")
		if !ok {
			continue
		}

		var sum []byte
		switch algo {
		case "sha512":
			h := sha512.Sum512(data)
			sum = h[:]
		case "sha256":
			h := sha256.Sum256(data)
			sum = h[:]
		case "sha1":
			h := sha1.Sum(data)
			sum = h[:]
		default:
			continue
		}
		supported = true

		expected, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return fmt.Errorf("integrity: invalid base64 in %q: %w", entry, err)
		}
		if subtle.ConstantTimeCompare(sum, expected) == 1 {
			return nil
		}
	}

	if !supported {
		return fmt.Errorf("integrity: no supported algorithm in %q", integrity)
	}
	return fmt.Errorf("integrity mismatch: data does not match %q", integrity)
}
