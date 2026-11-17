package api

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"applysmith/internal/api/wire"
)

func (d docsHarness) source(id wire.SafeInt) (wire.DocumentSource, result) {
	d.t.Helper()
	r := d.get("/api/documents/source/" + itoa(int(id)))
	var env wire.Success[wire.DocumentSource]
	if r.StatusCode == http.StatusOK {
		strictInto(d.t, r.body, &env)
	}
	return env.Data, r
}

// edit posts an edit and waits for its run; it returns the accepted result and
// the run when one was accepted.
func (d docsHarness) edit(id wire.SafeInt, body any) (result, *wire.DocumentRunView) {
	d.t.Helper()
	r := d.start("/api/documents/"+itoa(int(id))+"/edit", body)
	if r.StatusCode != http.StatusAccepted {
		return r, nil
	}
	run := d.finish(r.data()["runId"].(string))
	return r, &run
}

func (d docsHarness) pick(docs []wire.DocumentView, kind string) wire.DocumentView {
	d.t.Helper()
	for _, doc := range docs {
		if doc.Kind == kind {
			return doc
		}
	}
	d.t.Fatalf("no %s in %+v", kind, docs)
	return wire.DocumentView{}
}

func TestDocumentSource_ReturnsTheStoredTextOfEachKind(t *testing.T) {
	d := newDocsHarness(t, nil)
	docs := d.generate()
	resume, _ := d.source(d.pick(docs, "resume").ID)
	if resume.Resume == nil || resume.Letter != nil || resume.Kind != "resume" || resume.Version != 1 || resume.Frozen {
		t.Fatalf("resume source = %+v", resume)
	}
	if !strings.Contains(resume.Text, "Led the platform migration") || resume.Resume.Experience[0].Bullets[0].Evidence[0] != "E3" {
		t.Errorf("resume source = %+v", resume)
	}
	// The cited id names the CV line the sentence was written from.
	if i := slices.IndexFunc(resume.Evidence, func(e wire.DocumentEvidence) bool { return e.ID == "E3" }); i < 0 || !strings.Contains(resume.Evidence[i].Text, "Led the platform migration") {
		t.Errorf("evidence = %+v", resume.Evidence)
	}
	letter, _ := d.source(d.pick(docs, "cover-letter").ID)
	if letter.Letter == nil || letter.Resume != nil || !strings.Contains(letter.Text, "Dear Hiring Team") || letter.Letter.CompanyFacts == nil {
		t.Fatalf("letter source = %+v", letter)
	}
	if _, r := d.source(9999); r.StatusCode != 404 || r.errCode() != "not_found" {
		t.Errorf("unknown document: %d %s", r.StatusCode, r.body)
	}
	if _, r := d.source(0); r.StatusCode != 422 {
		t.Errorf("bad id: %d %s", r.StatusCode, r.body)
	}
}

func TestDocumentEdit_StoresANewDraftVersionAndLeavesTheEditedOneUntouched(t *testing.T) {
	d := newDocsHarness(t, nil)
	before := d.generate()
	resume := d.pick(before, "resume")
	src, _ := d.source(resume.ID)
	src.Resume.Experience[0].Bullets[1].Text = "Mentored four engineers through code review and design"

	r, run := d.edit(resume.ID, map[string]any{"resume": src.Resume})
	if run == nil || run.Status != wire.RunSucceeded || len(run.Documents) != 2 {
		t.Fatalf("edit: %d %s run=%+v", r.StatusCode, r.body, run)
	}
	if r.Header.Get("Location") == "" {
		t.Error("the accepted edit names no run")
	}
	after := d.list()
	if len(after) != 4 {
		t.Fatalf("documents = %d, want the first pair and the new one", len(after))
	}
	var fresh, old wire.DocumentView
	for _, doc := range after {
		switch {
		case doc.Kind == "resume" && doc.Version == 2:
			fresh = doc
		case doc.ID == resume.ID:
			old = doc
		}
	}
	// The new revision is a draft with no PDF, so its checks are pending.
	if fresh.ID == 0 || fresh.PDFState != "none" || fresh.Verification != nil || fresh.Review != nil || fresh.Ready || !fresh.Current || fresh.Frozen {
		t.Fatalf("new revision = %+v", fresh)
	}
	if !strings.Contains(strings.Join(fresh.Warnings, "|"), "Edited from version 1") {
		t.Errorf("warnings = %v", fresh.Warnings)
	}
	if old.ContentHash != resume.ContentHash || old.Current {
		t.Errorf("the edited version changed: %+v", old)
	}
	if got, _ := d.source(old.ID); got.Resume.Experience[0].Bullets[1].Text != "Mentored four engineers in code review and design" {
		t.Errorf("the edited version's text changed: %+v", got.Resume)
	}
	got, _ := d.source(fresh.ID)
	if got.Resume.Experience[0].Bullets[1].Text != "Mentored four engineers through code review and design" || !strings.Contains(got.Text, "through code review") {
		t.Errorf("new text = %+v", got)
	}
	// The letter of the pair is carried along unchanged.
	letter := d.pick(before, "cover-letter")
	var carried wire.DocumentView
	for _, doc := range after {
		if doc.Kind == "cover-letter" && doc.Version == 2 {
			carried = doc
		}
	}
	if carried.ID == 0 || carried.ContentHash != letter.ContentHash {
		t.Errorf("letter = %+v, want version 2 with the hash of %+v", carried, letter)
	}
}

func TestDocumentEdit_Refusals(t *testing.T) {
	d := newDocsHarness(t, nil)
	docs := d.generate()
	resume, letter := d.pick(docs, "resume"), d.pick(docs, "cover-letter")
	rsrc, _ := d.source(resume.ID)
	lsrc, _ := d.source(letter.ID)
	edited := func(mod func(*wire.DocumentResumeContent)) map[string]any {
		s, _ := d.source(resume.ID)
		mod(s.Resume)
		return map[string]any{"resume": s.Resume}
	}
	cases := []struct {
		name string
		id   wire.SafeInt
		body any
		code int
		want string
	}{
		{"unchanged text", resume.ID, map[string]any{"resume": rsrc.Resume}, 422, "validation_failed"},
		{"neither content", resume.ID, map[string]any{}, 422, "validation_failed"},
		{"both contents", resume.ID, map[string]any{"resume": rsrc.Resume, "letter": lsrc.Letter}, 422, "validation_failed"},
		{"résumé text for a letter", letter.ID, map[string]any{"resume": rsrc.Resume}, 422, "validation_failed"},
		{"unknown document", 9999, map[string]any{"resume": rsrc.Resume}, 404, "not_found"},
		{"unknown field", resume.ID, map[string]any{"resume": rsrc.Resume, "pdf": true}, 422, "validation_failed"},
		{"a figure the sources do not hold", resume.ID, edited(func(c *wire.DocumentResumeContent) {
			c.Experience[0].Bullets[0].Text = "Led the platform migration and cut incidents by 90% across 12 services"
		}), 422, "validation_failed"},
	}
	runs := d.runCount()
	for _, c := range cases {
		r := d.start("/api/documents/"+itoa(int(c.id))+"/edit", c.body)
		if r.StatusCode != c.code || r.errCode() != c.want {
			t.Errorf("%s: %d %s", c.name, r.StatusCode, r.body)
		}
	}
	if d.runCount() != runs {
		t.Error("a refused edit created a run")
	}
	if after := d.list(); len(after) != len(docs) {
		t.Errorf("a refused edit stored a version: %d documents", len(after))
	}
}

func TestDocumentEdit_ASubmittedVersionIsNotEdited(t *testing.T) {
	d := newDocsHarness(t, nil)
	resume := d.pick(d.generate(), "resume")
	src, _ := d.source(resume.ID)
	src.Resume.Profile = "Backend engineer focused on dependable data platforms."
	if err := d.domainOf().docs.Freeze(context.Background(), int64(atoi(d.cand)), int64(resume.ID)); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.source(resume.ID); !got.Frozen {
		t.Error("the source does not say the version is frozen")
	}
	r := d.start("/api/documents/"+itoa(int(resume.ID))+"/edit", map[string]any{"resume": src.Resume})
	if r.StatusCode != 409 || r.errCode() != "conflict" {
		t.Errorf("%d %s", r.StatusCode, r.body)
	}
	if after := d.list(); len(after) != 2 {
		t.Errorf("a refused edit stored a version: %d documents", len(after))
	}
}

func TestDocumentPDF_ServesTheStoredBytesForPreview(t *testing.T) {
	d := newDocsHarness(t, nil)
	resume := d.pick(d.generate(), "resume")
	if r := d.get("/api/documents/pdf/" + itoa(int(resume.ID))); r.StatusCode != 404 || r.errCode() != "not_found" {
		t.Errorf("a draft has no PDF: %d %s", r.StatusCode, r.body)
	}
	file := d.storePDF(resume, "ready")
	var withPDF wire.DocumentView
	for _, doc := range d.list() {
		if doc.PDFState == "ready" {
			withPDF = doc
		}
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	r := d.get("/api/documents/pdf/" + itoa(int(withPDF.ID)))
	if r.StatusCode != 200 || !bytes.Equal(r.body, want) || r.Header.Get("Content-Type") != "application/pdf" ||
		!strings.HasPrefix(r.Header.Get("Content-Disposition"), "inline;") || r.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("%d %v", r.StatusCode, r.Header)
	}
	// Bytes that are not the ones the row names are never served.
	if err := os.WriteFile(file, []byte("%PDF-1.7 replaced"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := d.get("/api/documents/pdf/" + itoa(int(withPDF.ID))); r.StatusCode != 409 || r.errCode() != "conflict" {
		t.Errorf("a replaced PDF was served: %d %s", r.StatusCode, r.body)
	}
	if r := d.get("/api/documents/pdf/0"); r.StatusCode != 422 {
		t.Errorf("bad id: %d %s", r.StatusCode, r.body)
	}
}
