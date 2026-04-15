// Package safepath provides a defensive [Join] that bounds a user-supplied
// path component to a fixed root directory. It is intended for handlers that
// derive filesystem paths from request input — URL fragments, multipart
// filenames, JSON-supplied capability fields — where filepath.Join alone is
// insufficient because it cleans `..` segments without ever leaving the
// caller a way to confirm the result is still inside the root.
//
// Go 1.24 introduces os.Root with stronger guarantees (resolves symlinks at
// open time, scoped to a root file descriptor). When this module bumps to
// 1.24+ the recommended migration is to replace Join with Root.OpenFile.
package safepath

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrEscapesRoot is returned by Join when the resolved path falls outside
// the supplied root. It wraps a descriptive message including both the
// requested name and the resolved root for easier debugging — the message is
// safe to log but should not be returned verbatim to untrusted callers.
var ErrEscapesRoot = errors.New("safepath: path escapes root")

// Join resolves `name` relative to `root` and guarantees the result lives
// inside `root`. It rejects:
//
//   - absolute `name` values (e.g. "/etc/passwd", "C:\\Windows")
//   - parent traversal sequences that escape root after Clean
//     (e.g. "../etc/passwd", "subdir/../../escape")
//   - empty `name` (callers should validate intent explicitly)
//
// The returned path is always cleaned. Both `root` and `name` are normalized
// with filepath.Clean before comparison, so trailing slashes and `.`
// segments do not affect the bounds check.
func Join(root, name string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("%w: empty root", ErrEscapesRoot)
	}
	if name == "" {
		return "", fmt.Errorf("%w: empty name", ErrEscapesRoot)
	}
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("%w: absolute path %q rejected", ErrEscapesRoot, name)
	}

	cleanRoot := filepath.Clean(root)
	candidate := filepath.Clean(filepath.Join(cleanRoot, name))

	// The cleanest invariant: candidate must equal cleanRoot or sit inside
	// it (cleanRoot + os.PathSeparator + ...). Comparing strings after
	// Clean handles redundant separators, embedded `.`, and resolved `..`.
	rootWithSep := cleanRoot + string(filepath.Separator)
	if candidate != cleanRoot && !strings.HasPrefix(candidate, rootWithSep) {
		return "", fmt.Errorf("%w: %q resolves to %q (outside %q)", ErrEscapesRoot, name, candidate, cleanRoot)
	}

	return candidate, nil
}
