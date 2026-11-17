package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

// The demo marker cannot be cleared by anything the workspace offers: no
// writer of the edited files touches it, init and a repeated mark keep it,
// and rewriting its content does not disarm it.
func TestDemoMarker_NothingTheWorkspaceOffersClearsIt(t *testing.T) {
	ws := Open(filepath.Join(t.TempDir(), "Applysmith-Demo"))
	if ws.IsDemo() || ws.Holds() {
		t.Fatal("an empty folder reads as a workspace")
	}
	if err := ws.MarkDemo(); err != nil {
		t.Fatal(err)
	}
	if err := ws.MarkDemo(); err != nil {
		t.Fatalf("a second mark failed: %v", err)
	}
	if _, err := ws.Init(); err != nil {
		t.Fatal(err)
	}
	for _, f := range []File{FileCV, FileNarrative, FileVoice} {
		c, err := ws.Read(f)
		if err != nil {
			t.Fatal(err)
		}
		if saved, err := ws.Write(f, append(c.Data, "\nOne more line.\n"...), c.Revision); err != nil || !saved.Changed {
			t.Fatalf("%s: %+v %v", f.Name(), saved, err)
		}
	}
	for f, op := range map[File]Op{
		FileProfile: {Kind: OpSet, Path: "candidate.full_name", Value: "Alex Example"},
		FilePolicy:  {Kind: OpSet, Path: "captcha_solver", Value: "off"},
		FilePortals: {Kind: OpAppend, Path: "companies", Value: "{name: Example Corp, ats: greenhouse, slug: example-corp}"},
	} {
		if saved, err := ws.Edit(f, []Op{op}, mustRead(t, ws, f).Revision); err != nil || !saved.Changed {
			t.Fatalf("%s: %+v %v", f.Name(), saved, err)
		}
	}
	if !ws.IsDemo() {
		t.Fatal("a write cleared the demo marker")
	}
	if err := os.WriteFile(filepath.Join(ws.StateDir(), DemoMarkerName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !ws.IsDemo() || !ws.Holds() {
		t.Fatal("the demo marker was cleared")
	}
	if !DemoMarked(ws.StateDir()) {
		t.Fatal("DemoMarked disagrees with IsDemo")
	}
}
