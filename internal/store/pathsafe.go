package store

import (
	"fmt"
	"path"
	"strings"
)

// checkRelPath accepts only clean, slash-separated, workspace-relative paths
// that stay out of the state directory.
func checkRelPath(p string) error {
	switch {
	case p == "", strings.ContainsAny(p, "\\\x00"), strings.HasPrefix(p, "/"), (len(p) > 1 && p[1] == ':'):
		return fmt.Errorf("%w: %q", ErrUnsafePath, p)
	case path.Clean(p) != p, p == ".", p == "..", strings.HasPrefix(p, "../"):
		return fmt.Errorf("%w: %q", ErrUnsafePath, p)
	case p == StateDir, strings.HasPrefix(p, StateDir+"/"):
		return fmt.Errorf("%w: %q is runtime state", ErrUnsafePath, p)
	}
	return nil
}
