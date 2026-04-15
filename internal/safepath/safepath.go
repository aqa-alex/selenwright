package safepath

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

var ErrEscapesRoot = errors.New("safepath: path escapes root")

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

	rootWithSep := cleanRoot + string(filepath.Separator)
	if candidate != cleanRoot && !strings.HasPrefix(candidate, rootWithSep) {
		return "", fmt.Errorf("%w: %q resolves to %q (outside %q)", ErrEscapesRoot, name, candidate, cleanRoot)
	}

	return candidate, nil
}
