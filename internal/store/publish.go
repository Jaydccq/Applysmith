package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PublishFile is one file of an atomic publish.
type PublishFile struct {
	Path    string // workspace-relative, slash-separated
	Content []byte
}

// publishDir holds the journals of publishes in flight, below the state dir.
const publishDir = "publish"

type journalFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// PublishArtifactTx allocates a report number, writes its files and commits
// the caller's rows in one transaction. plan receives the number and returns
// the files (the first is the artifact the number belongs to). The files are
// staged and flushed, then linked without replacing anything, with a journal
// written first. commit runs in the same transaction after the artifact row is
// inserted. If anything fails, the transaction rolls back (the number is not
// consumed) and every file this call created is removed. A crash before the
// commit leaves a journal that RecoverArtifacts uses to remove the orphan
// files.
func (s *Store) PublishArtifactTx(ctx context.Context, root, kind string, plan func(number int64) ([]PublishFile, error), commit func(tx *sql.Tx, a Artifact) error) (Artifact, error) {
	var a Artifact
	var staged, created []string
	journal := ""
	cleanup := func() {
		for _, p := range created {
			_ = os.Remove(p)
		}
		for _, p := range staged {
			_ = os.Remove(p)
		}
		if journal != "" {
			_ = os.Remove(journal)
		}
	}
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'report_artifacts'), 0) + 1`).Scan(&a.Number); err != nil {
			return err
		}
		files, err := plan(a.Number)
		if err != nil {
			return err
		}
		if len(files) == 0 {
			return errors.New("store: a publish needs at least one file")
		}
		a.Kind, a.Path = kind, files[0].Path
		var abss, tmps []string
		var entries []journalFile
		for _, f := range files {
			if err := checkRelPath(f.Path); err != nil {
				return err
			}
			abs := filepath.Join(root, filepath.FromSlash(f.Path))
			tmp, err := stageFile(abs, f.Content)
			if err != nil {
				return err
			}
			staged = append(staged, tmp)
			abss, tmps = append(abss, abs), append(tmps, tmp)
			entries = append(entries, journalFile{f.Path, sha256Hex(f.Content)})
		}
		if journal, err = writeJournal(root, a.Number, entries); err != nil {
			return err
		}
		for i := range abss {
			err := linkNoReplace(tmps[i], abss[i])
			// A failed directory flush leaves the name linked; only a conflict
			// means the bytes belong to someone else.
			if !errors.Is(err, ErrConflict) {
				created = append(created, abss[i])
			}
			if err != nil {
				return err
			}
		}
		now := s.stamp()
		a.Status, a.ContentHash = ArtifactReady, entries[0].SHA256
		if _, err := tx.ExecContext(ctx, `INSERT INTO report_artifacts (report_number, kind, path, status, content_hash, created_at, updated_at) VALUES (?, ?, ?, 'ready', ?, ?, ?)`,
			a.Number, kind, a.Path, a.ContentHash, now, now); err != nil {
			return err
		}
		return commit(tx, a)
	})
	if err != nil {
		cleanup()
		return Artifact{}, err
	}
	if journal != "" {
		_ = os.Remove(journal)
	}
	return a, nil
}

func writeJournal(root string, number int64, files []journalFile) (string, error) {
	b, err := json.Marshal(files)
	if err != nil {
		return "", err
	}
	abs := filepath.Join(root, StateDir, publishDir, fmt.Sprintf("%d-%s.json", number, NewID("j")))
	if err := writeAndRename(abs, b); err != nil {
		return "", err
	}
	return abs, nil
}

// recoverPublishJournals settles the journals of publishes that did not reach
// their commit. A journal whose artifact row is committed is just removed;
// otherwise the files it names are orphans of a rolled-back publish and are
// removed too, when their bytes are the ones the journal recorded.
func (s *Store) recoverPublishJournals(ctx context.Context, tx *sql.Tx, root string) error {
	dir := filepath.Join(root, StateDir, publishDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrIO, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		jp := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(jp)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrIO, err)
		}
		var files []journalFile
		if json.Unmarshal(raw, &files) != nil || len(files) == 0 {
			_ = os.Remove(jp) // an unreadable journal names nothing to clean
			continue
		}
		var committed int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM report_artifacts WHERE path = ? AND content_hash = ? AND status = 'ready'`,
			files[0].Path, files[0].SHA256).Scan(&committed); err != nil {
			return typed(err)
		}
		if committed == 0 {
			for _, f := range files {
				if checkRelPath(f.Path) != nil {
					continue
				}
				abs := filepath.Join(root, filepath.FromSlash(f.Path))
				if b, err := os.ReadFile(abs); err == nil && sha256Hex(b) == f.SHA256 {
					_ = os.Remove(abs)
				}
				_ = os.Remove(tempSibling(abs))
			}
		}
		_ = os.Remove(jp)
	}
	return nil
}
