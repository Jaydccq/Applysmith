package cli

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"applysmith/internal/workspace"
)

// snapshot records every path below root with its mode and bytes.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	got := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		content := ""
		if info.Mode().IsRegular() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			content = string(b)
		}
		got[rel] = info.Mode().String() + "|" + content
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestInitSeedsAnEmptyWorkspaceAndReportsEachFile(t *testing.T) {
	te := newTestEnv(t)
	o := te.run(context.Background(), "init", "--workspace", te.ws)
	if o.code != 0 {
		t.Fatalf("%+v", o)
	}
	for _, name := range []string{"profile.yml", "answer-bank.yml", "portals.yml", "policy.yml", "cv.md", "narrative.md"} {
		if !strings.Contains(o.stdout, "created "+name+"\n") {
			t.Errorf("no created line for %s in %q", name, o.stdout)
		}
		info, err := os.Stat(filepath.Join(te.ws, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", name, info, err)
		}
	}
	for _, name := range []string{"voice.md", "gmail-senders.yml"} {
		if _, err := os.Stat(filepath.Join(te.ws, name)); err == nil {
			t.Errorf("%s was seeded", name)
		}
	}
	for _, dir := range []string{"resumes", "knowledge", "reports", "documents", ".applysmith"} {
		if info, err := os.Stat(filepath.Join(te.ws, dir)); err != nil || !info.IsDir() {
			t.Errorf("directory %s: %v", dir, err)
		}
	}
	st, err := workspace.Open(te.ws).Status()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range st.Files {
		if f.File.Seeded() && f.Status != workspace.StatusTemplate {
			t.Errorf("%s is %s after init, want template", f.Name, f.Status)
		}
	}
}

func TestInitRefusesAnInvalidWorkspaceAndExtraArguments(t *testing.T) {
	te := newTestEnv(t)
	if o := te.run(context.Background(), "init", "--workspace", "relative/dir"); o.code != 2 || !strings.Contains(o.stderr, "workspace_invalid") {
		t.Errorf("relative path: %+v", o)
	}
	if o := te.run(context.Background(), "init", "extra"); o.code != 2 {
		t.Errorf("extra argument: %+v", o)
	}
}

func TestDoctorLeavesTheWorkspaceUntouched(t *testing.T) {
	cases := map[string]func(base string) string{
		"non-existent": func(base string) string { return filepath.Join(base, "not", "yet") },
		"empty":        func(base string) string { return base },
		"seeded": func(base string) string {
			dir := filepath.Join(base, "ws")
			if _, err := workspace.Open(dir).Init(); err != nil {
				t.Fatal(err)
			}
			return dir
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			ws := mk(base)
			before := snapshot(t, base)
			o := newTestEnv(t).run(context.Background(), "doctor", "--workspace", ws, "--port", "0")
			if o.code == 4 || o.code == 2 {
				t.Fatalf("doctor did not run: %+v", o)
			}
			after := snapshot(t, base)
			for p, v := range after {
				if before[p] != v {
					t.Errorf("doctor created or changed %q", p)
				}
			}
			if len(after) != len(before) {
				t.Errorf("tree has %d paths after doctor, %d before", len(after), len(before))
			}
		})
	}
}
