package workspace

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withRoster(t *testing.T, w *Workspace, block string) {
	t.Helper()
	cur, err := w.Read(FileProfile)
	if err != nil {
		t.Fatal(err)
	}
	src := string(cur.Data)
	from, to := strings.Index(src, "\nresumes:"), strings.Index(src, "\nhistory:")
	if from < 0 || to < from {
		t.Fatalf("no roster in the seeded profile")
	}
	if err := os.WriteFile(w.Path(FileProfile), []byte(src[:from+1]+block+src[to:]), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeResume(t *testing.T, w *Workspace, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(w.Root, "resumes", name), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRosterListsEntriesWithTheirFileSize(t *testing.T) {
	w := newWorkspace(t)
	writeResume(t, w, "backend.pdf", []byte("%PDF-1.4 body"))
	withRoster(t, w, "resumes:\n  - {id: backend, label: Backend, file: resumes/backend.pdf, tags: [go], default: true}\n")
	got, err := w.Roster()
	if err != nil || len(got) != 1 || got[0].ID != "backend" || got[0].FileName != "backend.pdf" || got[0].Size != 13 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestRosterOfAWorkspaceWithoutAProfileIsEmpty(t *testing.T) {
	w := Open(t.TempDir())
	got, err := w.Roster()
	if err != nil || len(got) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestRosterRefusesAnInvalidProfile(t *testing.T) {
	w := newWorkspace(t)
	if err := os.WriteFile(w.Path(FileProfile), []byte("version: 1\nresumes: not-a-list\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var invalid *InvalidError
	if _, err := w.Roster(); !errors.As(err, &invalid) {
		t.Fatalf("%v", err)
	}
}

func TestReadResumeServesOnlyPDFsInsideResumes(t *testing.T) {
	w := newWorkspace(t)
	writeResume(t, w, "backend.pdf", []byte("%PDF-1.4 body"))
	withRoster(t, w, "resumes:\n  - {id: backend, label: Backend, file: resumes/backend.pdf, default: true}\n")
	e, data, err := w.ReadResume("backend")
	if err != nil || e.ID != "backend" || string(data) != "%PDF-1.4 body" {
		t.Fatalf("%+v %q %v", e, data, err)
	}
	for _, id := range []string{"missing", "", "../backend", "backend/../backend"} {
		if _, _, err := w.ReadResume(id); !errors.Is(err, &Error{Code: CodeFileMissing}) {
			t.Errorf("id %q: %v", id, err)
		}
	}
}

func TestReadResumeRefusesWhatIsNotAPDF(t *testing.T) {
	w := newWorkspace(t)
	withRoster(t, w, "resumes:\n  - {id: backend, label: Backend, file: resumes/backend.pdf, default: true}\n")
	if _, _, err := w.ReadResume("backend"); !errors.Is(err, &Error{Code: CodeInvalidFormat}) && !errors.Is(err, &Error{Code: CodeFileMissing}) {
		t.Fatalf("missing file: %v", err)
	}
	// A roster that validates cannot name a missing file, so the file is added
	// to pass validation and then replaced behind the roster's back.
	writeResume(t, w, "backend.pdf", []byte("%PDF-1.4"))
	withRoster(t, w, "resumes:\n  - {id: backend, label: Backend, file: resumes/backend.pdf, default: true}\n")
	writeResume(t, w, "backend.pdf", []byte("<html>not a pdf</html>"))
	if _, _, err := w.ReadResume("backend"); !errors.Is(err, &Error{Code: CodeInvalidFormat}) {
		t.Fatalf("not a pdf: %v", err)
	}
	writeResume(t, w, "backend.pdf", append([]byte("%PDF-"), bytes.Repeat([]byte{0}, maxResumeBytes)...))
	if _, _, err := w.ReadResume("backend"); !errors.Is(err, &Error{Code: CodeOutOfRange}) {
		t.Fatalf("too large: %v", err)
	}
	if err := os.Remove(filepath.Join(w.Root, "resumes", "backend.pdf")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.ReadResume("backend"); !errors.Is(err, &Error{Code: CodeFileMissing}) {
		t.Fatalf("deleted: %v", err)
	}
}

func TestRemoveResumeDeletesOnlyAFileDirectlyInsideResumes(t *testing.T) {
	w := newWorkspace(t)
	writeResume(t, w, "backend.pdf", []byte("%PDF-1.4"))
	outside := filepath.Join(w.Root, "cv.md")
	if err := os.WriteFile(outside, []byte("# Alex Example"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(w.Root, "resumes", "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeResume(t, w, "sub/deep.pdf", []byte("%PDF-1.4"))
	for _, rel := range []string{"cv.md", "../cv.md", "resumes/../cv.md", "resumes/sub/deep.pdf", "resumes/", "resumes", "/etc/hosts", ""} {
		if err := w.RemoveResume(rel); !errors.Is(err, &Error{Code: CodePathOutside}) {
			t.Errorf("RemoveResume(%q) = %v", rel, err)
		}
	}
	for _, p := range []string{outside, filepath.Join(w.Root, "resumes", "sub", "deep.pdf")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was deleted", p)
		}
	}
	if err := w.RemoveResume("resumes/backend.pdf"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w.Root, "resumes", "backend.pdf")); err == nil {
		t.Error("the file stayed")
	}
	if err := w.RemoveResume("resumes/backend.pdf"); err != nil {
		t.Errorf("deleting a file that is already gone: %v", err)
	}
	link := filepath.Join(w.Root, "resumes", "link.pdf")
	if err := os.Symlink(outside, link); err == nil {
		if err := w.RemoveResume("resumes/link.pdf"); err == nil {
			t.Error("a symbolic link was deleted as if it were a résumé")
		}
		if _, err := os.Stat(outside); err != nil {
			t.Error("the link target was deleted")
		}
	}
}

// A resumes/ folder that is a link to a folder outside the workspace takes no
// upload and loses no file: both paths are confined as the roster check is.
func TestResumeWritesStayInsideWhenResumesIsALinkOut(t *testing.T) {
	w := newWorkspace(t)
	outside := t.TempDir()
	kept := filepath.Join(outside, "kept.pdf")
	if err := os.WriteFile(kept, []byte("%PDF-1.4"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(w.Root, "resumes")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(w.Root, "resumes")); err != nil {
		t.Skip("symbolic links are not available:", err)
	}
	if _, err := w.StoreResume("backend", []byte("%PDF-1.4")); !errors.Is(err, &Error{Code: CodePathOutside}) {
		t.Errorf("StoreResume = %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "backend.pdf")); err == nil {
		t.Error("the upload was written outside the workspace")
	}
	if err := w.RemoveResume("resumes/kept.pdf"); !errors.Is(err, &Error{Code: CodePathOutside}) {
		t.Errorf("RemoveResume = %v", err)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Error("a file outside the workspace was deleted")
	}
}
