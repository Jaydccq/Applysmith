package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func (te *testEnv) pdf(name string, data []byte) string {
	te.t.Helper()
	p := filepath.Join(te.t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		te.t.Fatal(err)
	}
	return p
}

func (te *testEnv) stored(name string) bool {
	_, err := os.Stat(filepath.Join(te.ws, "resumes", name))
	return err == nil
}

func TestResumes_Add_ValidatesPDFAndRollsBack(t *testing.T) {
	ctx := context.Background()
	te := newInitTestEnv(t)
	add := func(file string, extra ...string) output {
		return te.run(ctx, append([]string{"resumes", "add", file, "--workspace", te.ws}, extra...)...)
	}

	for name, data := range map[string][]byte{
		"notes.pdf":  []byte("plain text, not a PDF"),
		"empty.pdf":  nil,
		"bigger.pdf": append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte{' '}, 10<<20)...),
	} {
		if o := add(te.pdf(name, data)); o.code == 0 {
			t.Errorf("%s was accepted: %+v", name, o)
		}
		if te.stored(name) {
			t.Errorf("%s was left in resumes/", name)
		}
	}

	good := te.pdf("backend.pdf", []byte("%PDF-1.4\n%%EOF\n"))
	if o := add(good, "--label", "Backend", "--tags", "go, sql"); o.code != 0 {
		t.Fatalf("add: %+v", o)
	}
	list := te.run(ctx, "resumes", "list", "--workspace", te.ws)
	if list.code != 0 || !strings.Contains(list.stdout, "backend (default)") || !strings.Contains(list.stdout, "go,sql") {
		t.Fatalf("the first résumé is the default and keeps its tags:\n%+v", list)
	}
	if o := add(te.pdf("other.pdf", []byte("%PDF-1.4\nsecond")), "--id", "backend"); o.code == 0 {
		t.Errorf("an id already in use was accepted: %+v", o)
	}
	if b, _ := os.ReadFile(filepath.Join(te.ws, "resumes", "backend.pdf")); string(b) != "%PDF-1.4\n%%EOF\n" {
		t.Errorf("the stored file was replaced: %q", b)
	}

	// A profile that no longer validates refuses the write, and the file the
	// command had just stored goes again.
	profile := filepath.Join(te.ws, "profile.yml")
	src, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(src), "version: 1", "version: 99", 1)
	if broken == string(src) {
		t.Fatalf("the profile has no version line:\n%s", src)
	}
	if err := os.WriteFile(profile, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	if o := add(te.pdf("late.pdf", []byte("%PDF-1.4\nlate"))); o.code == 0 {
		t.Fatalf("an add against an invalid profile was accepted: %+v", o)
	}
	if te.stored("late.pdf") {
		t.Error("the refused add left its file in resumes/")
	}
}

func TestResumes_RemoveDefaultRefused(t *testing.T) {
	ctx := context.Background()
	te := newInitTestEnv(t)
	run := func(args ...string) output {
		return te.run(ctx, append([]string{"resumes"}, append(args, "--workspace", te.ws)...)...)
	}
	for _, id := range []string{"alpha", "beta"} {
		if o := run("add", te.pdf(id+".pdf", []byte("%PDF-1.4\n"+id)), "--label", id); o.code != 0 {
			t.Fatalf("add %s: %+v", id, o)
		}
	}
	if o := run("remove", "alpha"); o.code == 0 || !strings.Contains(o.stderr, "default") {
		t.Fatalf("removing the default while another remains must be refused: %+v", o)
	}
	if !te.stored("alpha.pdf") || !strings.Contains(run("list").stdout, "alpha (default)") {
		t.Fatal("a refused removal changed the roster or deleted the file")
	}
	if o := run("set-default", "nobody"); o.code == 0 {
		t.Errorf("an unknown id was made the default: %+v", o)
	}
	if o := run("set-default", "beta"); o.code != 0 {
		t.Fatalf("set-default: %+v", o)
	}
	if o := run("remove", "alpha"); o.code != 0 {
		t.Fatalf("remove: %+v", o)
	}
	if te.stored("alpha.pdf") || !te.stored("beta.pdf") {
		t.Error("removing an entry deletes its file and no other")
	}
	if o := run("remove", "alpha"); o.code == 0 {
		t.Errorf("a second removal found the entry: %+v", o)
	}
	if o := run("remove", "beta"); o.code != 0 || te.stored("beta.pdf") {
		t.Fatalf("the last entry can go: %+v", o)
	}
	if got := run("list"); got.code != 0 || strings.Contains(got.stdout, "beta") {
		t.Fatalf("the roster is empty: %+v", got)
	}
}
