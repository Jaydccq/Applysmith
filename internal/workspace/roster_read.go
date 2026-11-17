package workspace

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
)

// RosterEntry is a résumé roster entry with the facts of its file. Size is
// zero when the file cannot be read, so a listing still shows the entry.
type RosterEntry struct {
	Resume
	FileName string
	Size     int64
}

// Roster lists the résumé roster of profile.yml in roster order. A workspace
// without a profile has an empty roster; an invalid profile is refused rather
// than read partly.
func (w *Workspace) Roster() ([]RosterEntry, error) {
	cur, err := w.Read(FileProfile)
	if err != nil {
		return nil, err
	}
	if cur.Revision == RevisionAbsent {
		return nil, nil
	}
	p, rep := ParseProfile(cur.Data)
	if !rep.OK() {
		return nil, &InvalidError{Report: rep}
	}
	out := make([]RosterEntry, 0, len(p.Resumes))
	for _, r := range p.Resumes {
		e := RosterEntry{Resume: r, FileName: path.Base(r.File)}
		if full, err := w.Confine(r.File); err == nil {
			if info, err := os.Stat(full); err == nil && info.Mode().IsRegular() {
				e.Size = info.Size()
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// ReadResume returns the PDF a roster entry names. It serves only files that
// resolve inside resumes/ of the workspace, are regular files of at most
// 10 MiB and start with the PDF signature, so a hand-edited roster cannot turn
// this into a general file reader. An unknown id or a file that is missing is
// CodeFileMissing.
func (w *Workspace) ReadResume(id string) (RosterEntry, []byte, error) {
	roster, err := w.Roster()
	if err != nil {
		return RosterEntry{}, nil, err
	}
	for _, e := range roster {
		if e.ID != id {
			continue
		}
		data, err := w.readRosterFile(e.File)
		return e, data, err
	}
	return RosterEntry{}, nil, newError(CodeFileMissing, id, "no résumé has the id %s", id)
}

func (w *Workspace) readRosterFile(rel string) ([]byte, error) {
	if !strings.HasPrefix(path.Clean(rel), "resumes/") {
		return nil, newError(CodeConstraint, rel, "file must be inside resumes/")
	}
	return w.readPDF(rel)
}

// readPDF reads a regular file inside the workspace of at most 10 MiB that
// starts with the PDF signature.
func (w *Workspace) readPDF(rel string) ([]byte, error) {
	full, err := w.Confine(rel)
	if err != nil {
		return nil, err
	}
	fh, err := os.Open(full)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, newError(CodeFileMissing, rel, "file %s does not exist", rel)
	}
	if err != nil {
		return nil, newError(CodeFileMissing, rel, "file %s cannot be read", rel)
	}
	defer fh.Close()
	info, err := fh.Stat()
	switch {
	case err != nil || !info.Mode().IsRegular():
		return nil, newError(CodeInvalidFormat, rel, "%s is not a regular file", rel)
	case info.Size() > maxResumeBytes:
		return nil, newError(CodeOutOfRange, rel, "%s is larger than 10 MiB", rel)
	}
	data, err := io.ReadAll(io.LimitReader(fh, maxResumeBytes+1))
	switch {
	case err != nil:
		return nil, newError(CodeFileMissing, rel, "file %s cannot be read", rel)
	case len(data) > maxResumeBytes:
		return nil, newError(CodeOutOfRange, rel, "%s is larger than 10 MiB", rel)
	case !bytes.HasPrefix(data, []byte("%PDF-")):
		return nil, newError(CodeInvalidFormat, rel, "%s is not a PDF", rel)
	}
	return data, nil
}
