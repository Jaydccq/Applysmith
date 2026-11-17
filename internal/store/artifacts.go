package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
)

// ArtifactReady is the state of every artifact row: a row is inserted in the
// transaction that also holds the files, so a half-published artifact has no
// row.
const ArtifactReady = "ready"

// Artifact is one indexed report or document file.
type Artifact struct {
	Number      int64
	Kind        string
	Path        string // workspace-relative, slash-separated
	Status      string
	ContentHash string
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func tempSibling(abs string) string {
	return filepath.Join(filepath.Dir(abs), "."+filepath.Base(abs)+".pending")
}

// writeAndRename never replaces an existing file: bytes already at the target
// belong to someone else.
func writeAndRename(abs string, content []byte) error {
	tmp, err := stageFile(abs, content)
	if err != nil {
		return err
	}
	return linkNoReplace(tmp, abs)
}

// stageFile writes content to a flushed temporary sibling of abs and returns
// its path.
func stageFile(abs string, content []byte) (string, error) {
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return "", fmt.Errorf("%w: %w", ErrIO, err)
	}
	tmp := tempSibling(abs)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrIO, err)
	}
	_, werr := f.Write(content)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return "", diskError(werr)
	}
	return tmp, nil
}

// publishGap lets a test create the target between the flush and the link.
var publishGap func(abs string)

// linkNoReplace publishes tmp at abs atomically and never replaces bytes that
// already exist there (a rename would). The temp sibling is removed either way.
func linkNoReplace(tmp, abs string) error {
	if publishGap != nil {
		publishGap(abs)
	}
	err := os.Link(tmp, abs)
	_ = os.Remove(tmp)
	switch {
	case err == nil:
		// Without the directory flush a power loss could drop the new name
		// after the index already says ready.
		if err := syncParent(filepath.Dir(abs)); err != nil {
			return fmt.Errorf("%w: %w", ErrIO, err)
		}
		return nil
	case errors.Is(err, os.ErrExist):
		return fmt.Errorf("%w: %s already exists", ErrConflict, path.Base(abs))
	}
	return fmt.Errorf("%w: %w", ErrIO, err)
}

// syncParent flushes a directory's entries; tests replace it.
var syncParent = realSyncParent

func realSyncParent(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// RecoverArtifacts settles the journals of publishes a dead process left
// behind: the files of a publish that never committed are removed (see
// PublishArtifactTx). It never regenerates content, calls a provider or
// removes bytes it did not write.
//
// It runs under the writer lock: a publish of another process holds that lock
// from its journal until its commit, so recovery sees either the committed
// rows or the rolled-back publish, never a publish in flight.
func (s *Store) RecoverArtifacts(ctx context.Context, root string) error {
	return s.Write(ctx, func(tx *sql.Tx) error { return s.recoverPublishJournals(ctx, tx, root) })
}

// diskError keeps ENOSPC distinguishable from other i/o failures.
func diskError(err error) error {
	if errors.Is(err, errNoSpace) {
		return fmt.Errorf("%w: %w", ErrDiskFull, err)
	}
	return fmt.Errorf("%w: %w", ErrIO, err)
}
