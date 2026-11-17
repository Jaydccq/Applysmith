package kb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"applysmith/internal/lazyregexp"
)

const (
	dirMode  = 0o700
	fileMode = 0o600
	// maxFileBytes bounds one knowledge file read into the index.
	maxFileBytes = 1 << 20
)

// writeFileAtomic writes data beside path and renames it into place, so a
// reader sees the old or the new file and never half of one.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".kb-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(fileMode); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(name, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// writeRecorded replaces the file at abs with data as the last step of the
// transaction that records the change. A record that fails, or a writer that
// is busy, leaves the file alone; a transaction that fails after the file was
// written gets the file back as it was: prev, or no file when had is false.
func (s *Service) writeRecorded(ctx context.Context, abs string, data, prev []byte, had bool, record func(tx *sql.Tx) error) error {
	wrote := false
	err := s.st.Write(ctx, func(tx *sql.Tx) error {
		if err := record(tx); err != nil {
			return err
		}
		if err := writeFileAtomic(abs, data); err != nil {
			return err
		}
		wrote = true
		return nil
	})
	switch {
	case err == nil || !wrote:
		return err
	case had:
		return errors.Join(err, writeFileAtomic(abs, prev))
	}
	return errors.Join(err, os.Remove(abs))
}

var slugRE = lazyregexp.New(`[^a-z0-9]+`)

// Slug turns an id or file name into a safe file name stem: lowercase letters
// and digits separated by single hyphens.
func Slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRE.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 80 {
		s = strings.Trim(s[:80], "-")
	}
	return s
}

func (s *Service) knowledgeRoot() string { return filepath.Join(s.ws.Root, KnowledgeDir) }

func (s *Service) absPath(rel string) string {
	return filepath.Join(s.ws.Root, filepath.FromSlash(rel))
}

// readRegular reads a file only when its final component is a regular file, so
// a symlink swapped in beside the index is never followed out of the workspace.
func readRegular(abs string) ([]byte, error) {
	fi, err := os.Lstat(abs)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrInvalid, filepath.Base(abs))
	}
	return os.ReadFile(abs)
}
