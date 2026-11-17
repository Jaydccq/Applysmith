package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newWorkspace(t *testing.T) *Workspace {
	t.Helper()
	root := filepath.Join(t.TempDir(), "ws")
	w := Open(root)
	if _, err := w.Init(); err != nil {
		t.Fatal(err)
	}
	return w
}

func mustRead(t *testing.T, w *Workspace, f File) Content {
	t.Helper()
	c, err := w.Read(f)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestInitSeedsFiveFilesAndDirectories(t *testing.T) {
	w := Open(filepath.Join(t.TempDir(), "ws"))
	res, err := w.Init()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range res {
		if !r.Created {
			t.Errorf("%s not created", r.Name)
		}
		names = append(names, r.Name)
	}
	if got := strings.Join(names, ","); got != "profile.yml,portals.yml,policy.yml,cv.md,narrative.md" {
		t.Fatalf("seeded %s", got)
	}
	for _, absent := range []string{"voice.md", "gmail-senders.yml", "prompts"} {
		if _, err := os.Stat(filepath.Join(w.Root, absent)); err == nil {
			t.Errorf("%s must not be created", absent)
		}
	}
	for _, dir := range []string{"resumes", "knowledge", "reports", "documents", ".applysmith", ".applysmith/backups", ".applysmith/logs", ".applysmith/snapshots", ".applysmith/downloads"} {
		info, err := os.Stat(filepath.Join(w.Root, dir))
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v %v", dir, info, err)
		}
	}
	for _, f := range Files {
		if !f.Seeded() {
			continue
		}
		info, err := os.Stat(w.Path(f))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", f, info, err)
		}
	}
}

func TestInitNeverOverwritesAndIsRepeatable(t *testing.T) {
	w := newWorkspace(t)
	custom := []byte("version: 1\ncandidate:\n  full_name: Alex Example\n")
	if err := os.WriteFile(w.Path(FileProfile), custom, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(w.Path(FileProfile), 0o640); err != nil {
		t.Fatal(err)
	}
	res, err := w.Init()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Created {
			t.Errorf("%s reported created on a repeat run", r.Name)
		}
	}
	if got, _ := os.ReadFile(w.Path(FileProfile)); !bytes.Equal(got, custom) {
		t.Fatal("profile.yml was modified")
	}
	if info, _ := os.Stat(w.Path(FileProfile)); info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v", info.Mode())
	}
}

func TestSeedsContainOnlyFictionalPlaceholders(t *testing.T) {
	for _, f := range Files {
		b := templateBytes(f)
		for _, bad := range []string{"@gmail.com", "@outlook", "linkedin.com/in/"} {
			if bytes.Contains(b, []byte(bad)) {
				t.Errorf("%s contains %q", f.Name(), bad)
			}
		}
	}
}

func TestStatusReportsTemplateMissingInvalidReady(t *testing.T) {
	w := newWorkspace(t)
	if err := os.WriteFile(w.Path(FilePortals), []byte("version: 1\nbogus: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.Path(FileNarrative), []byte("# My story\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := w.Status()
	if err != nil {
		t.Fatal(err)
	}
	got := map[File]string{}
	for _, f := range st.Files {
		got[f.File] = f.Status
	}
	want := map[File]string{
		FileProfile: StatusTemplate, FileAnswerBank: StatusMissing, FilePortals: StatusInvalid,
		FilePolicy: StatusTemplate, FileCV: StatusTemplate, FileNarrative: StatusReady, FileVoice: StatusMissing,
	}
	for f, s := range want {
		if got[f] != s {
			t.Errorf("%s = %s, want %s", f, got[f], s)
		}
	}
	for _, f := range st.Files {
		if f.File == FilePortals && (len(f.Report.Issues) != 1 || f.Report.Issues[0].Path != "bogus") {
			t.Errorf("portals report = %s", f.Report)
		}
		if f.File == FileVoice && (!f.Optional || !f.Report.OK()) {
			t.Errorf("voice must be optional and cause no issue: %+v", f)
		}
	}
}

// Status keeps the last report per file by revision; an edit is checked
// afresh, and the profile, whose check reads the résumé files, every time.
func TestStatusChecksAnEditedFileAgain(t *testing.T) {
	w := newWorkspace(t)
	status := func(f File) string {
		t.Helper()
		st, err := w.Status()
		if err != nil {
			t.Fatal(err)
		}
		for _, fs := range st.Files {
			if fs.File == f {
				return fs.Status
			}
		}
		return ""
	}
	write := func(f File, body string) {
		t.Helper()
		if err := os.WriteFile(w.Path(f), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(FilePortals, "version: 1\n")
	for _, want := range []string{StatusReady, StatusReady} {
		if got := status(FilePortals); got != want {
			t.Fatalf("portals = %s, want %s", got, want)
		}
	}
	write(FilePortals, "version: 1\nbogus: 1\n")
	if got := status(FilePortals); got != StatusInvalid {
		t.Fatalf("edited portals = %s, want invalid", got)
	}
	pdf := filepath.Join(w.Root, "resumes", "a.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-1.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	write(FileProfile, "version: 1\nresumes:\n  - id: a\n    file: resumes/a.pdf\n    default: true\n")
	if got := status(FileProfile); got != StatusReady {
		t.Fatalf("profile = %s, want ready", got)
	}
	if err := os.Remove(pdf); err != nil {
		t.Fatal(err)
	}
	if got := status(FileProfile); got != StatusInvalid {
		t.Fatalf("profile naming a removed résumé = %s, want invalid", got)
	}
}

func TestStaleRevisionIsAConflictWithTheLatestContent(t *testing.T) {
	w := newWorkspace(t)
	first := mustRead(t, w, FileCV)
	external := []byte("# Edited elsewhere\n")
	if err := os.WriteFile(w.Path(FileCV), external, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := w.Write(FileCV, []byte("# Mine\n"), first.Revision)
	if !IsCode(err, CodeRevisionConflict) {
		t.Fatalf("err = %v", err)
	}
	var ce *ConflictError
	if !errors.As(err, &ce) || !bytes.Equal(ce.Content, external) || ce.Revision != Revision(external) {
		t.Fatalf("conflict = %+v", ce)
	}
	if got, _ := os.ReadFile(w.Path(FileCV)); !bytes.Equal(got, external) {
		t.Fatal("file modified despite conflict")
	}
}

func TestCreateWithExpectedAbsentConflictsWhenFileExists(t *testing.T) {
	w := newWorkspace(t)
	_, err := w.Write(FileCV, []byte("# x\n"), RevisionAbsent)
	if !IsCode(err, CodeRevisionConflict) {
		t.Fatalf("err = %v", err)
	}
}

func TestResavingUnchangedBytesIsNotAConflict(t *testing.T) {
	w := newWorkspace(t)
	cur := mustRead(t, w, FileCV)
	saved, err := w.Write(FileCV, cur.Data, "0000stale")
	if err != nil || saved.Changed || saved.Revision != cur.Revision {
		t.Fatalf("%v %+v", err, saved)
	}
	entries, _ := os.ReadDir(w.BackupsDir())
	if len(entries) != 0 {
		t.Fatal("a no-op save created a backup")
	}
}

func TestInvalidWriteLeavesTheFileUnchanged(t *testing.T) {
	w := newWorkspace(t)
	before := mustRead(t, w, FileProfile)
	_, err := w.Write(FileProfile, []byte("version: 1\ntargets:\n  compensaton: {}\n"), before.Revision)
	var ie *InvalidError
	if !errors.As(err, &ie) || !IsCode(err, CodeConfigInvalid) {
		t.Fatalf("err = %v", err)
	}
	if ie.Report.Issues[0].Path != "targets.compensaton" {
		t.Fatalf("report = %s", ie.Report)
	}
	if after := mustRead(t, w, FileProfile); after.Revision != before.Revision {
		t.Fatal("file changed")
	}
}

func TestSaveKeepsExistingPermissions(t *testing.T) {
	w := newWorkspace(t)
	if err := os.Chmod(w.Path(FileProfile), 0o640); err != nil {
		t.Fatal(err)
	}
	cur := mustRead(t, w, FileProfile)
	if _, err := w.Write(FileProfile, []byte("version: 1\n"), cur.Revision); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(w.Path(FileProfile))
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v", info.Mode())
	}
}

func TestBackupRetentionKeepsTenNewest(t *testing.T) {
	w := newWorkspace(t)
	clock := time.Unix(1_700_000_000, 0)
	w.now = func() time.Time { return clock }
	rev := mustRead(t, w, FileCV).Revision
	for i := 1; i <= 12; i++ {
		body := []byte(fmt.Sprintf("# version %d\n", i))
		saved, err := w.Write(FileCV, body, rev) // same wall-clock second every time
		if err != nil {
			t.Fatal(err)
		}
		rev = saved.Revision
	}
	stamps, err := backupStamps(w.BackupsDir(), "cv.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(stamps) != 10 {
		t.Fatalf("%d backups", len(stamps))
	}
	// The ten newest previous versions are saves 2..11 (the 12th is on disk).
	oldest, _ := os.ReadFile(filepath.Join(w.BackupsDir(), fmt.Sprintf("cv.md.%d", stamps[0])))
	newest, _ := os.ReadFile(filepath.Join(w.BackupsDir(), fmt.Sprintf("cv.md.%d", stamps[9])))
	if string(oldest) != "# version 2\n" || string(newest) != "# version 11\n" {
		t.Fatalf("oldest %q newest %q", oldest, newest)
	}
	if info, _ := os.Stat(w.BackupsDir()); info.Mode().Perm() != 0o700 {
		t.Fatalf("backups dir mode %v", info.Mode())
	}
}

func TestBackupsOfDifferentFilesDoNotShareRetention(t *testing.T) {
	w := newWorkspace(t)
	for _, f := range []File{FileCV, FileNarrative} {
		rev := mustRead(t, w, f).Revision
		for i := 0; i < 3; i++ {
			saved, err := w.Write(f, []byte(fmt.Sprintf("# %s %d\n", f, i)), rev)
			if err != nil {
				t.Fatal(err)
			}
			rev = saved.Revision
		}
	}
	for _, name := range []string{"cv.md", "narrative.md"} {
		if s, _ := backupStamps(w.BackupsDir(), name); len(s) != 3 {
			t.Errorf("%s: %d backups", name, len(s))
		}
	}
}

func TestInterruptedWriteKeepsThePreviousContent(t *testing.T) {
	w := newWorkspace(t)
	cur := mustRead(t, w, FileCV)
	w.beforeRename = func() error { return errors.New("process stopped") }
	if _, err := w.Write(FileCV, []byte("# new\n"), cur.Revision); err == nil {
		t.Fatal("expected failure")
	}
	if got := mustRead(t, w, FileCV); got.Revision != cur.Revision {
		t.Fatal("target changed")
	}
	entries, _ := os.ReadDir(w.Root)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestMarkdownDocumentRules(t *testing.T) {
	w := newWorkspace(t)
	rev := mustRead(t, w, FileCV).Revision
	big := bytes.Repeat([]byte("a"), 300<<10)
	if _, err := w.Write(FileCV, big, rev); !IsCode(err, CodePayloadTooLarge) {
		t.Fatalf("oversized: %v", err)
	}
	// One placed by hand is reported, not ready, and saving it back is refused
	// the same way.
	if err := os.WriteFile(w.Path(FileCV), big, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := w.Status()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range st.Files {
		if f.File == FileCV && (f.Status != StatusInvalid || len(f.Report.Issues) != 1 || f.Report.Issues[0].Code != CodePayloadTooLarge) {
			t.Fatalf("oversized cv.md on disk: %s %s", f.Status, f.Report)
		}
	}
	if _, err := w.Write(FileCV, big, Revision(big)); !IsCode(err, CodePayloadTooLarge) {
		t.Fatalf("saving the oversized file back: %v", err)
	}
	rev = Revision(big)
	if _, err := w.Write(FileCV, []byte{0xff, 0xfe, 'x'}, rev); !IsCode(err, CodeConfigInvalid) {
		t.Fatalf("non-UTF-8: %v", err)
	}
	saved, err := w.Write(FileCV, append([]byte("\xEF\xBB\xBF"), "# CV\r\nline two\r\n"...), rev)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(w.Path(FileCV))
	if string(got) != "# CV\r\nline two\r\n" || saved.Revision != Revision(got) {
		t.Fatalf("stored %q", got)
	}
	exact := bytes.Repeat([]byte("a"), 256<<10)
	if _, err := w.Write(FileCV, exact, saved.Revision); err != nil {
		t.Fatalf("256 KiB must be accepted: %v", err)
	}
}

func TestEditThroughTheRevisionedPath(t *testing.T) {
	w := newWorkspace(t)
	cur := mustRead(t, w, FilePolicy)
	saved, err := w.Edit(FilePolicy, []Op{{Kind: OpSet, Path: "captcha_solver", Value: "off"}}, cur.Revision)
	if err != nil || !saved.Changed {
		t.Fatalf("%v %+v", err, saved)
	}
	after := mustRead(t, w, FilePolicy)
	if !strings.Contains(string(after.Data), "captcha_solver: off") || !strings.Contains(string(after.Data), "# Hours to back off") {
		t.Fatalf("policy:\n%s", after.Data)
	}
	backups, _ := backupStamps(w.BackupsDir(), "policy.yml")
	if len(backups) != 1 {
		t.Fatal("previous content not backed up")
	}
	p, _ := w.LoadPolicy()
	if p.CaptchaSolver != "off" {
		t.Fatal("LoadPolicy missed the edit")
	}
}

func TestListAppendKeepsOtherLinesByteIdentical(t *testing.T) {
	w := newWorkspace(t)
	body := "version: 1\n\n# keep\nbatch_skip_companies:\n  - Tesla\n\nscan:\n  defaults: {}\n"
	rev := mustRead(t, w, FileProfile).Revision
	saved, err := w.Write(FileProfile, []byte(body), rev)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Edit(FileProfile, []Op{{Kind: OpAppend, Path: "batch_skip_companies", Value: "Example Corp"}}, saved.Revision); err != nil {
		t.Fatal(err)
	}
	got := mustRead(t, w, FileProfile).Data
	want := "version: 1\n\n# keep\nbatch_skip_companies:\n  - Tesla\n  - Example Corp\n\nscan:\n  defaults: {}\n"
	if string(got) != want {
		t.Fatalf("got %q", got)
	}
}

func TestLoadPolicyIsRestrictiveWhenMissingOrInvalid(t *testing.T) {
	w := newWorkspace(t)
	if p, rep := w.LoadPolicy(); !rep.OK() || p.CaptchaSolver != "on" {
		t.Fatalf("seeded policy: %+v %s", p, rep)
	}
	if err := os.WriteFile(w.Path(FilePolicy), []byte("version: 1\ncaptcha_solver: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	p, rep := w.LoadPolicy()
	if rep.OK() || p.CaptchaSolver != "off" || p.RemoteAccess.Mode != "off" {
		t.Fatalf("invalid policy: %+v", p)
	}
	if err := os.Remove(w.Path(FilePolicy)); err != nil {
		t.Fatal(err)
	}
	p, rep = w.LoadPolicy()
	if rep.OK() || p.CaptchaSolver != "off" {
		t.Fatalf("missing policy: %+v", p)
	}
}

// Two Open calls on one directory are two handles; serialisation must span them.
func TestSeparateHandlesAreSerialised(t *testing.T) {
	w := newWorkspace(t)
	rev := mustRead(t, w, FileCV).Revision
	const n = 8
	results := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			h := Open(w.Root)
			_, err := h.Write(FileCV, []byte(fmt.Sprintf("# handle %d\n", i)), rev)
			results <- err
		}(i)
	}
	ok, conflicts := 0, 0
	for i := 0; i < n; i++ {
		switch err := <-results; {
		case err == nil:
			ok++
		case IsCode(err, CodeRevisionConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if ok != 1 || conflicts != n-1 {
		t.Fatalf("ok=%d conflicts=%d", ok, conflicts)
	}
}

func TestWriteRefusesASymbolicLinkedFile(t *testing.T) {
	w := newWorkspace(t)
	target := filepath.Join(t.TempDir(), "shared-profile.yml")
	original := mustRead(t, w, FileProfile)
	if err := os.WriteFile(target, original.Data, 0o600); err != nil {
		t.Fatal(err)
	}
	link := w.Path(FileProfile)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, err := w.Write(FileProfile, append(original.Data, []byte("\n# edited\n")...), original.Revision)
	if !IsCode(err, CodeNotAllowed) {
		t.Fatalf("err = %v", err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced: %v %v", info, err)
	}
	if b, _ := os.ReadFile(target); !bytes.Equal(b, original.Data) {
		t.Fatal("the link target changed")
	}
}
