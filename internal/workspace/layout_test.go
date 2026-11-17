package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestConfine(t *testing.T) {
	w := newWorkspace(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.pdf"), []byte("%PDF-1.4"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.pdf"), filepath.Join(w.Root, "resumes", "cv.pdf")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(w.Root, "knowledge", "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.Root, "resumes", "ok.pdf"), []byte("%PDF-1.4"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, rel string
		ok        bool
	}{
		{"inside", "resumes/ok.pdf", true},
		{"not yet created", "reports/new/report.md", true},
		{"parent traversal", "../secrets.pdf", false},
		{"nested traversal", "resumes/../../x", false},
		{"cleans to inside", "resumes/../reports/a.md", true},
		{"absolute", "/etc/passwd", false},
		{"empty", "", false},
		{"symlink file escape", "resumes/cv.pdf", false},
		{"symlink dir escape", "knowledge/link/file.md", false},
		{"symlink dir escape, new file", "knowledge/link/new.md", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			full, err := w.Confine(tc.rel)
			if tc.ok {
				if err != nil || !strings.HasPrefix(full, w.Root) {
					t.Fatalf("%q, %v", full, err)
				}
				return
			}
			if !IsCode(err, CodePathOutside) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestRosterFileValidationAgainstTheWorkspace(t *testing.T) {
	w := newWorkspace(t)
	outside := t.TempDir()
	_ = os.WriteFile(filepath.Join(outside, "x.pdf"), []byte("%PDF-1.4"), 0o600)
	_ = os.Symlink(filepath.Join(outside, "x.pdf"), filepath.Join(w.Root, "resumes", "link.pdf"))
	_ = os.WriteFile(filepath.Join(w.Root, "resumes", "good.pdf"), []byte("%PDF-1.7\n"), 0o600)
	_ = os.WriteFile(filepath.Join(w.Root, "resumes", "fake.pdf"), []byte("hello"), 0o600)
	_ = os.WriteFile(filepath.Join(w.Root, "resumes", "huge.pdf"), append([]byte("%PDF-"), make([]byte, 10<<20)...), 0o600)
	roster := func(files ...string) []byte {
		var b strings.Builder
		b.WriteString("version: 1\nresumes:\n")
		for i, f := range files {
			b.WriteString("  - id: r" + string(rune('a'+i)) + "\n    file: " + f + "\n")
			if i == 0 {
				b.WriteString("    default: true\n")
			}
		}
		return []byte(b.String())
	}
	tests := []struct {
		name  string
		files []string
		want  []string
	}{
		{"good", []string{"resumes/good.pdf"}, nil},
		{"missing", []string{"resumes/backend.pdf"}, []string{"resumes[0].file file_missing"}},
		{"symlink escape", []string{"resumes/link.pdf"}, []string{"resumes[0].file path_outside_workspace"}},
		{"not a pdf", []string{"resumes/fake.pdf"}, []string{"resumes[0].file invalid_format"}},
		{"too large", []string{"resumes/huge.pdf"}, []string{"resumes[0].file out_of_range"}},
		{"second entry bad", []string{"resumes/good.pdf", "resumes/nope.pdf"}, []string{"resumes[1].file file_missing"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wantIssues(t, w.Validate(FileProfile, roster(tc.files...)), tc.want...)
		})
	}
}

func TestMissingResumeBlocksTheSave(t *testing.T) {
	w := newWorkspace(t)
	rev := mustRead(t, w, FileProfile).Revision
	_, err := w.Write(FileProfile, []byte("version: 1\nresumes:\n  - id: a\n    file: resumes/backend.pdf\n    default: true\n"), rev)
	if !IsCode(err, CodeConfigInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestWorkspaceIDIsStableOpaqueAndPrivate(t *testing.T) {
	w := newWorkspace(t)
	a, err := w.ID()
	if err != nil || len(a) != 32 {
		t.Fatalf("%q %v", a, err)
	}
	if b, _ := w.ID(); b != a {
		t.Fatal("ID changed between calls")
	}
	if other, _ := newWorkspace(t).ID(); other == a {
		t.Fatal("two workspaces share an ID")
	}
	info, _ := os.Stat(filepath.Join(w.StateDir(), "workspace-id"))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode())
	}
	if strings.Contains(a, w.Root) {
		t.Fatal("ID derived from the path")
	}
}

func TestWorkspaceIDFileThatIsNotAnIdentifierFailsLoudly(t *testing.T) {
	for name, content := range map[string]string{
		"empty":     "",
		"newline":   "\n",
		"truncated": "0123abcd\n",
		"not hex":   strings.Repeat("z", 32) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorkspace(t)
			path := filepath.Join(w.StateDir(), idFileName)
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := w.ID(); !IsCode(err, CodeWorkspaceIDInvalid) {
				t.Fatalf("err = %v; want %s", err, CodeWorkspaceIDInvalid)
			}
			if b, _ := os.ReadFile(path); string(b) != content {
				t.Fatalf("the file was rewritten to %q", b)
			}
		})
	}
}

func TestWorkspaceIDConcurrentFirstUseAgrees(t *testing.T) {
	root := filepath.Join(t.TempDir(), "ws")
	const n = 16
	ids := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ids[i], errs[i] = Open(root).ID() // separate handles: no shared lock
		}()
	}
	close(start)
	wg.Wait()
	for i := range n {
		if errs[i] != nil || ids[i] != ids[0] || len(ids[i]) != 32 {
			t.Fatalf("creator %d got %q, %v; creator 0 got %q", i, ids[i], errs[i], ids[0])
		}
	}
}

func TestWorkspaceIDNeverAppearsHalfWritten(t *testing.T) {
	w := newWorkspace(t)
	path := filepath.Join(w.StateDir(), idFileName)
	boom := errors.New("injected crash after the flush")
	w.beforeRename = func() error {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("identifier file visible before it was complete: %v", err)
		}
		return boom
	}
	if _, err := w.ID(); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed creation left %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(w.StateDir(), ".workspace-id.*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
	w.beforeRename = nil
	id, err := w.ID()
	if err != nil || len(id) != 32 {
		t.Fatalf("%q %v", id, err)
	}
}

func TestCreateIDNeverReplacesAnIdentifier(t *testing.T) {
	w := newWorkspace(t)
	first, err := w.ID()
	if err != nil {
		t.Fatal(err)
	}
	if err := CreateID(w.StateDir(), NewID()); !errors.Is(err, os.ErrExist) {
		t.Fatalf("err = %v", err)
	}
	if again, _ := w.ID(); again != first {
		t.Fatal("identifier replaced")
	}
	if err := CreateID(t.TempDir(), "short"); !IsCode(err, CodeWorkspaceIDInvalid) {
		t.Fatalf("err = %v", err)
	}
}
