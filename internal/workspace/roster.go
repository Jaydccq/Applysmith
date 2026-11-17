package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const maxResumeBytes = 10 << 20

// checkRosterFiles verifies that every résumé roster entry names an existing
// PDF of at most 10 MiB that resolves inside the workspace.
func (w *Workspace) checkRosterFiles(p Profile, src []byte) Report {
	var doc yaml.Node
	if yaml.Unmarshal(src, &doc) != nil || len(doc.Content) == 0 {
		return Report{}
	}
	roster := valueNode(deref(doc.Content[0]), "resumes")
	var issues []Issue
	for i, r := range p.Resumes {
		if r.File == "" || roster == nil || i >= len(roster.Content) {
			continue
		}
		at := valueNode(roster.Content[i], "file")
		add := func(code, msg string) {
			issue := Issue{File: FileProfile.Name(), Path: fmt.Sprintf("resumes[%d].file", i), Code: code, Message: msg}
			if at != nil {
				issue.Line, issue.Column = at.Line, at.Column
			}
			issues = append(issues, issue)
		}
		if msg := w.checkPDF(r.File); msg != nil {
			add(msg.Code, msg.Message)
		}
	}
	return finishReport(issues)
}

func (w *Workspace) checkPDF(rel string) *Error {
	full, err := w.Confine(rel)
	if err != nil {
		var te *Error
		if errors.As(err, &te) {
			return te
		}
		return newError(CodeFileMissing, rel, "cannot be inspected")
	}
	info, err := os.Stat(full)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return newError(CodeFileMissing, rel, "file %s does not exist", rel)
	case err != nil:
		return newError(CodeFileMissing, rel, "file %s cannot be read", rel)
	case !info.Mode().IsRegular():
		return newError(CodeInvalidFormat, rel, "%s is not a regular file", rel)
	case info.Size() > maxResumeBytes:
		return newError(CodeOutOfRange, rel, "%s is larger than 10 MiB", rel)
	}
	fh, err := os.Open(full)
	if err != nil {
		return newError(CodeFileMissing, rel, "file %s cannot be read", rel)
	}
	defer fh.Close()
	head := make([]byte, 5)
	if _, err := io.ReadFull(fh, head); err != nil || string(head) != "%PDF-" {
		return newError(CodeInvalidFormat, rel, "%s is not a PDF", rel)
	}
	return nil
}

// StoreResume writes an uploaded PDF as resumes/<name>.pdf and returns the
// workspace-relative path. It refuses a name that is not a roster id, content
// that is not a PDF or larger than 10 MiB, and a name already in use, so an
// upload never replaces a file a roster entry may point to.
func (w *Workspace) StoreResume(name string, data []byte) (string, error) {
	rel, err := w.CheckResume(name, data)
	if err != nil {
		return "", err
	}
	dir, err := w.Confine("resumes")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	full := filepath.Join(dir, name+".pdf")
	defer w.lock(full)()
	if _, err := os.Lstat(full); err == nil {
		return "", newError(CodeFileExists, rel, "%s already exists", rel)
	}
	return rel, w.writeAtomic(full, data, 0o600)
}

// CheckResume runs StoreResume's checks without writing, for a dry run, and
// returns the workspace-relative path the PDF would be stored at.
func (w *Workspace) CheckResume(name string, data []byte) (string, error) {
	rel := "resumes/" + name + ".pdf"
	switch {
	case !idRe.MatchString(name):
		return "", newError(CodeInvalidFormat, rel, "the name must be 1 to 40 lowercase letters, digits and hyphens, not starting with a hyphen")
	case len(data) > maxResumeBytes:
		return "", newError(CodePayloadTooLarge, rel, "a résumé PDF is limited to 10 MiB")
	case !bytes.HasPrefix(data, []byte("%PDF-")):
		return "", newError(CodeInvalidFormat, rel, "the file is not a PDF")
	}
	if _, err := os.Lstat(filepath.Join(w.Root, "resumes", name+".pdf")); err == nil {
		return "", newError(CodeFileExists, rel, "%s already exists", rel)
	}
	return rel, nil
}

// RemoveResume deletes the PDF a roster entry named. It only ever deletes a
// regular file directly inside resumes/; any other path is refused, so a
// roster can never make it delete a file outside the managed directory.
func (w *Workspace) RemoveResume(rel string) error {
	clean := path.Clean(rel)
	dir, name := path.Split(clean)
	if dir != "resumes/" || name == "" || name == "." || name == ".." {
		return newError(CodePathOutside, rel, "only a file directly inside resumes/ can be deleted")
	}
	dir, err := w.Confine("resumes")
	if err != nil {
		return err
	}
	full := filepath.Join(dir, name)
	defer w.lock(full)()
	info, err := os.Lstat(full)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case !info.Mode().IsRegular():
		return newError(CodeInvalidFormat, rel, "%s is not a regular file", rel)
	}
	return os.Remove(full)
}
