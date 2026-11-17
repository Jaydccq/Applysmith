package documents

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"applysmith/internal/documents/toolchain"
)

// A PDF run reads the manifest back from the version directory. A manifest
// that is internally consistent but is not the one the version's rows were
// written for is refused before anything is built.
func TestPDFRun_ManifestMustBeTheOneTheRowsRecord(t *testing.T) {
	fb := &fakeBuilder{resumeCap: 380}
	f := newFixture(t, fixtureOpts{profile: leanProfile, cv: repairCV(5, 4, 3, 0), builder: func(*toolchain.Toolchain) pdfBuilder { return fb }})
	draft := f.run(Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, Options: Options{Pages: 2}})
	d := f.svc.mustGet(t, draft.Documents[0])
	var m Manifest
	must(t, json.Unmarshal([]byte(f.read(d.Path+"/evidence-manifest.json")), &m))
	m.Warnings = append(m.Warnings, "another manifest")
	must(t, m.Freeze())
	raw, err := json.Marshal(m)
	must(t, err)
	must(t, os.WriteFile(filepath.Join(f.ws.Root, filepath.FromSlash(d.Path), "evidence-manifest.json"), raw, 0o600))
	if res := f.run(Request{Action: ActionPDF, DocumentID: d.ID}); res.Published || len(fb.calls) != 0 {
		t.Fatalf("result = %+v, builds = %d; want a refusal before any build", res, len(fb.calls))
	} else if res.Reason == ReasonNoDraft {
		t.Errorf("reason = %s: the gate refused the draft, not the manifest comparison", res.Reason)
	}
}

func TestReadRegular_RefusesWhatIsNotAPlainFileWithinTheBound(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain")
	must(t, os.WriteFile(plain, []byte("12345"), 0o600))
	if b, err := readRegular(plain, 5); err != nil || string(b) != "12345" {
		t.Fatalf("plain = %q, %v", b, err)
	}
	if _, err := readRegular(plain, 4); err == nil {
		t.Error("a file over the bound was read")
	}
	if _, err := readRegular(dir, 1<<20); err == nil {
		t.Error("a directory was read")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(plain, link); err != nil {
		t.Skip("symlinks are not available:", err)
	}
	if _, err := readRegular(link, 1<<20); err == nil || strings.Contains(err.Error(), "no such file") {
		t.Errorf("a symlink was read: %v", err)
	}
	if _, err := readRegular(filepath.Join(dir, "missing"), 1<<20); !os.IsNotExist(err) {
		t.Errorf("a missing file = %v, want not-exist", err)
	}
}

type failingBuilder struct{ msg string }

func (b failingBuilder) Build(context.Context, toolchain.BuildInput) (toolchain.BuildOutput, error) {
	return toolchain.BuildOutput{}, errors.New(b.msg)
}

// A build failure is stored as a warning and shown to the user: it names no
// absolute path of this machine.
func TestBuildFailureWarningCarriesNoAbsolutePath(t *testing.T) {
	root := t.TempDir()
	msg := "xelatex exited 1 (engine log kept at " + filepath.Join(root, "documents", "x", "1", "resume.log") + ") see " + filepath.Join(string(filepath.Separator), "private", "cache", "toolchain", "diagnostics", "resume.log")
	f := newFixture(t, fixtureOpts{root: root, builder: func(*toolchain.Toolchain) pdfBuilder { return failingBuilder{msg} }})
	res := f.run(Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, PDF: true})
	var got string
	for _, w := range res.Warnings {
		if strings.Contains(w, "no PDF could be verified") {
			got = w
		}
	}
	if got == "" {
		t.Fatalf("warnings = %v", res.Warnings)
	}
	if strings.Contains(got, root) || strings.Contains(got, "/private") || !strings.Contains(got, "documents/x/1/resume.log") {
		t.Errorf("warning = %q; want workspace-relative paths only", got)
	}
}
