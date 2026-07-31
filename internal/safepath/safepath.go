// Package safepath guards against writing outside a destination directory when
// the relative path comes from an untrusted source - object keys returned by a
// bucket listing, entry names in an archive, and the like.
package safepath

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Join cleans rel and joins it onto root, returning an error if the result
// would land outside root. It does not ban ".." outright: a path that uses ".."
// but stays within root (e.g. "a/../b") is allowed; only one that escapes root
// is rejected. root is assumed to be a trusted, caller-controlled directory.
func Join(root, rel string) (string, error) {
	joined := filepath.Join(root, filepath.FromSlash(rel))
	cleanRoot := filepath.Clean(root)
	if joined != cleanRoot && !strings.HasPrefix(joined, cleanRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes the destination directory", rel)
	}
	return joined, nil
}
