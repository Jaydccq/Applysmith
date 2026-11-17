package workspace

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// RevisionAbsent is the revision of a file that does not exist.
	RevisionAbsent = "absent"
	backupsKept    = 10
)

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Revision is the lowercase hexadecimal SHA-256 of the exact bytes.
func Revision(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Content is a file's bytes with its revision.
type Content struct {
	Data     []byte
	Revision string
}

// Saved reports the outcome of a write.
type Saved struct {
	Revision string
	Changed  bool
}

// Read returns the file's bytes and revision; a missing file has the revision
// "absent" and no bytes.
func (w *Workspace) Read(f File) (Content, error) {
	data, err := os.ReadFile(w.Path(f))
	if errors.Is(err, fs.ErrNotExist) {
		return Content{Revision: RevisionAbsent}, nil
	}
	if err != nil {
		return Content{}, err
	}
	return Content{Data: data, Revision: Revision(data)}, nil
}

func validateDocument(f File, data []byte) Report {
	if len(bytes.TrimPrefix(data, utf8BOM)) > maxDocumentBytes {
		return Report{Issues: []Issue{{File: f.Name(), Code: CodePayloadTooLarge, Message: fmt.Sprintf("documents are limited to %d KiB", maxDocumentBytes>>10)}}}
	}
	if !utf8.Valid(data) {
		return Report{Issues: []Issue{{File: f.Name(), Code: CodeInvalidFormat, Message: "content is not valid UTF-8"}}}
	}
	return Report{}
}

// Validate checks content for f, including the checks that need the workspace
// (the résumé roster names files that must exist).
func (w *Workspace) Validate(f File, data []byte) Report {
	if f != FileProfile {
		return Validate(f, data)
	}
	p, rep := ParseProfile(data)
	if !rep.OK() {
		return rep
	}
	return w.checkRosterFiles(p, data)
}

// Write validates data and replaces the file when expected matches its current
// revision. Saving the bytes already on disk is not a conflict and writes
// nothing. The previous content is copied to .applysmith/backups first.
func (w *Workspace) Write(f File, data []byte, expected string) (Saved, error) {
	if !f.IsYAML() {
		data = bytes.TrimPrefix(data, utf8BOM)
	}
	if rep := w.Validate(f, data); !rep.OK() {
		if i := rep.Issues[0]; i.Code == CodePayloadTooLarge {
			return Saved{}, newError(CodePayloadTooLarge, f.Name(), "%s", i.Message)
		}
		return Saved{}, &InvalidError{Report: rep}
	}
	path := w.Path(f)
	defer w.lock(path)()
	// The atomic replace would swap a link for a regular file and detach the
	// file it pointed at, so a linked file is refused rather than followed.
	if info, err := os.Lstat(path); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return Saved{}, newError(CodeNotAllowed, f.Name(), "the file is a symbolic link; replace it with a regular file to edit it here")
	}
	cur, err := w.Read(f)
	if err != nil {
		return Saved{}, err
	}
	if cur.Revision == Revision(data) {
		return Saved{Revision: cur.Revision}, nil
	}
	if cur.Revision != expected {
		return Saved{}, &ConflictError{File: f.Name(), Revision: cur.Revision, Content: cur.Data}
	}
	mode := fs.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
		if err := w.backup(f, cur.Data); err != nil {
			return Saved{}, err
		}
	}
	if err := w.writeAtomic(path, data, mode); err != nil {
		return Saved{}, err
	}
	return Saved{Revision: Revision(data), Changed: true}, nil
}

// writeAtomic writes a temporary sibling, flushes it, renames it over the
// target and flushes the directory.
func (w *Workspace) writeAtomic(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(mode); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	if w.beforeRename != nil {
		if err := w.beforeRename(); err != nil {
			return fail(err)
		}
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fail(err)
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// backup copies the previous content to <name>.<unix-seconds>, moving the
// stamp forward when two saves land in the same second, and prunes all but the
// newest backupsKept copies of that file.
func (w *Workspace) backup(f File, prev []byte) error {
	dir := w.BackupsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	stamps, err := backupStamps(dir, f.Name())
	if err != nil {
		return err
	}
	stamp := w.now().Unix()
	if n := len(stamps); n > 0 && stamps[n-1] >= stamp {
		stamp = stamps[n-1] + 1
	}
	path := filepath.Join(dir, fmt.Sprintf("%s.%d", f.Name(), stamp))
	if err := os.WriteFile(path, prev, 0o600); err != nil {
		return err
	}
	stamps = append(stamps, stamp)
	for len(stamps) > backupsKept {
		_ = os.Remove(filepath.Join(dir, fmt.Sprintf("%s.%d", f.Name(), stamps[0])))
		stamps = stamps[1:]
	}
	return nil
}

// Backup is an earlier content of a file that Write kept: what the file held
// until the save at SavedAt replaced it.
type Backup struct {
	SavedAt time.Time
	Data    []byte
}

// Backups lists the kept earlier contents of f, newest first.
func (w *Workspace) Backups(f File) ([]Backup, error) {
	dir := w.BackupsDir()
	stamps, err := backupStamps(dir, f.Name())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]Backup, 0, len(stamps))
	for i := len(stamps) - 1; i >= 0; i-- {
		data, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("%s.%d", f.Name(), stamps[i])))
		if err != nil {
			return nil, err
		}
		out = append(out, Backup{SavedAt: time.Unix(stamps[i], 0), Data: data})
	}
	return out, nil
}

// backupStamps lists the stamps of a file's backups, oldest first.
func backupStamps(dir, name string) ([]int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var stamps []int64
	for _, e := range entries {
		rest, ok := strings.CutPrefix(e.Name(), name+".")
		if !ok {
			continue
		}
		if n, err := strconv.ParseInt(rest, 10, 64); err == nil {
			stamps = append(stamps, n)
		}
	}
	sort.Slice(stamps, func(i, j int) bool { return stamps[i] < stamps[j] })
	return stamps, nil
}

// Edit applies structured operations to a YAML file and writes the result
// through the same validation, revision check and backup as Write.
func (w *Workspace) Edit(f File, ops []Op, expected string) (Saved, error) {
	if !f.IsYAML() {
		return Saved{}, newError(CodeEditPath, f.Name(), "structured edits apply to YAML files only")
	}
	cur, err := w.Read(f)
	if err != nil {
		return Saved{}, err
	}
	if cur.Revision == RevisionAbsent {
		return Saved{}, newError(CodeFileMissing, f.Name(), "file does not exist")
	}
	if cur.Revision != expected {
		return Saved{}, &ConflictError{File: f.Name(), Revision: cur.Revision, Content: cur.Data}
	}
	out, err := ApplyOps(cur.Data, ops)
	if err != nil {
		return Saved{}, err
	}
	return w.Write(f, out, expected)
}
