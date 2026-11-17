package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Binding errors carry a stable code as their text: the dashboard reads the
// message of the rejected call.
var (
	errOutsideWorkspace = errors.New("path_outside_workspace")
	errPathNotFound     = errors.New("path_not_found")
)

// insideWorkspace resolves p, absolute or relative to the workspace root, and
// returns it only when it names an existing file or folder in the workspace.
// The test runs twice, on the written path and on the path with symbolic links
// resolved, so a link that leaves the workspace does not lead out of it.
func insideWorkspace(root, p string) (string, error) {
	if root == "" || p == "" {
		return "", errOutsideWorkspace
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	p = filepath.Clean(p)
	if !within(root, p) {
		return "", errOutsideWorkspace
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", errOutsideWorkspace
	}
	real, err := filepath.EvalSymlinks(p)
	if errors.Is(err, os.ErrNotExist) {
		return "", errPathNotFound
	}
	if err != nil || !within(realRoot, real) {
		return "", errOutsideWorkspace
	}
	return real, nil
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
