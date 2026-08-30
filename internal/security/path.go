// Package security holds protections shared by every protocol server:
// path-traversal-safe file resolution and IP allow/deny list matching.
package security

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ErrPathEscapesRoot is returned by SafeJoin when the requested path would
// resolve outside of the configured root directory.
var ErrPathEscapesRoot = errors.New("security: path escapes root directory")

// SafeJoin resolves an untrusted client-supplied path (as sent over FTP or
// TFTP) against root. A harmless leading slash is normalized into root
// (filepath.Join never lets an embedded "/" reroot the result), but any
// path that lexically climbs above root via ".." — however it's disguised —
// is rejected outright with ErrPathEscapesRoot rather than being silently
// clamped back inside root: callers use that error to log the attempt as a
// path-traversal probe (see internal/tftp/session.go), so silently
// containing it would erase the audit trail docs/PLAN.md §8.1 calls for.
func SafeJoin(root, userPath string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absRoot = filepath.Clean(absRoot)

	candidate := filepath.Join(absRoot, filepath.Clean(userPath))

	if !isWithinRoot(absRoot, candidate) {
		return "", ErrPathEscapesRoot
	}

	// Resolve symlinks on the parts of the path that already exist, so a
	// symlink planted inside root cannot point outside of it.
	resolved, err := resolveExisting(candidate)
	if err != nil {
		return "", err
	}
	if !isWithinRoot(absRoot, resolved) {
		return "", ErrPathEscapesRoot
	}

	return candidate, nil
}

func isWithinRoot(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveExisting walks up from path until it finds a segment that exists,
// resolves symlinks for that existing prefix, and re-appends the remaining
// (not-yet-created) segments unchanged.
func resolveExisting(path string) (string, error) {
	segments := []string{}
	cur := path
	for {
		if _, err := os.Lstat(cur); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			// Reached filesystem root without finding an existing segment.
			return path, nil
		}
		segments = append([]string{filepath.Base(cur)}, segments...)
		cur = parent
	}

	real, err := filepath.EvalSymlinks(cur)
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{real}, segments...)...), nil
}
