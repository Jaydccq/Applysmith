package workspace

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// TranscriptDir holds the academic transcript: one PDF, whose stored name is
// the name an application receives.
const TranscriptDir = "transcript"

// transcriptFiles lists the PDFs in dir in name order. Hidden files and
// anything that is not a regular file, a link included, are not transcripts.
func transcriptFiles(dir string) ([]fs.DirEntry, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []fs.DirEntry
	for _, e := range entries {
		if e.Type().IsRegular() && !strings.HasPrefix(e.Name(), ".") && strings.HasSuffix(strings.ToLower(e.Name()), ".pdf") {
			out = append(out, e)
		}
	}
	return out, nil
}

// Transcript returns the file name of the stored transcript, empty when there
// is none. A folder that holds more than one PDF (copied in by hand) has the
// first by name as its transcript.
func (w *Workspace) Transcript() (string, error) {
	dir, err := w.Confine(TranscriptDir)
	if err != nil {
		return "", err
	}
	files, err := transcriptFiles(dir)
	if err != nil || len(files) == 0 {
		return "", err
	}
	return files[0].Name(), nil
}

// ReadTranscript returns the stored transcript's file name and bytes, held to
// the résumé roster's file rules; an empty name and no data when there is none.
func (w *Workspace) ReadTranscript() (string, []byte, error) {
	name, err := w.Transcript()
	if err != nil || name == "" {
		return "", nil, err
	}
	data, err := w.readPDF(TranscriptDir + "/" + name)
	return name, data, err
}

// StoreTranscript writes an uploaded PDF as transcript/<name>.pdf and deletes
// every other PDF there, so a new upload replaces the transcript. It refuses a
// name that is not 1 to 40 lowercase letters, digits and hyphens, and content
// that is not a PDF or is larger than 10 MiB, as a résumé upload does. It
// returns the stored file name.
func (w *Workspace) StoreTranscript(name string, data []byte) (string, error) {
	rel := TranscriptDir + "/" + name + ".pdf"
	switch {
	case !idRe.MatchString(name):
		return "", newError(CodeInvalidFormat, rel, "the name must be 1 to 40 lowercase letters, digits and hyphens")
	case len(data) > maxResumeBytes:
		return "", newError(CodePayloadTooLarge, rel, "a transcript PDF is limited to 10 MiB")
	case !bytes.HasPrefix(data, []byte("%PDF-")):
		return "", newError(CodeInvalidFormat, rel, "the file is not a PDF")
	}
	dir, err := w.Confine(TranscriptDir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	defer w.lock(dir)()
	if err := w.writeAtomic(filepath.Join(dir, name+".pdf"), data, 0o600); err != nil {
		return "", err
	}
	if err := removeTranscripts(dir, name+".pdf"); err != nil {
		return "", err
	}
	return name + ".pdf", nil
}

// RemoveTranscript deletes the stored transcript; with none it does nothing.
func (w *Workspace) RemoveTranscript() error {
	dir, err := w.Confine(TranscriptDir)
	if err != nil {
		return err
	}
	defer w.lock(dir)()
	return removeTranscripts(dir, "")
}

// removeTranscripts deletes the PDFs in dir except keep.
func removeTranscripts(dir, keep string) error {
	files, err := transcriptFiles(dir)
	if err != nil {
		return err
	}
	for _, f := range files {
		if f.Name() == keep {
			continue
		}
		if err := os.Remove(filepath.Join(dir, f.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}
