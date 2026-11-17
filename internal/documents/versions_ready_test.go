package documents

import (
	"testing"

	"applysmith/internal/documents/toolchain"
)

func TestDocuments_FailedPDFCannotReplaceReady(t *testing.T) {
	fail := false
	fb := &fakeBuilder{override: func(fakeCall) map[string]string {
		if fail {
			return map[string]string{toolPDF + "/text-layer": toolchain.Fail}
		}
		return nil
	}}
	f := newFixture(t, fixtureOpts{builder: func(*toolchain.Toolchain) pdfBuilder { return fb }})
	req := Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, PDF: true}
	first := f.run(req)
	if !first.Published {
		t.Fatalf("first run = %+v", first)
	}
	v1, err := f.svc.Get(t.Context(), first.Documents[0])
	must(t, err)
	if !v1.Ready || v1.PDFState != PDFReady || v1.PDFPath == "" {
		t.Fatalf("version 1 = %+v", v1)
	}
	pdfBefore := f.read(v1.PDFPath)

	f.clk.mu.Lock()
	f.clk.t = f.clk.t.AddDate(0, 0, 1)
	f.clk.mu.Unlock()
	fail = true
	second := f.run(req)
	docs, err := f.svc.List(t.Context(), f.cand)
	must(t, err)
	var newest, ready []Document
	for _, d := range docs {
		if d.Current {
			newest = append(newest, d)
		}
		if d.Ready {
			ready = append(ready, d)
		}
	}
	if len(newest) != 1 || newest[0].ID == v1.ID || newest[0].Ready || newest[0].PDFState == PDFReady {
		t.Fatalf("the failed regeneration is the current draft and is not ready: %+v (result %+v)", newest, second)
	}
	if len(ready) != 1 || ready[0].ID != v1.ID {
		t.Fatalf("the attachment-ready pointer must stay on version 1: %+v", ready)
	}
	if f.read(v1.PDFPath) != pdfBefore || newest[0].PDFPath == v1.PDFPath {
		t.Error("the ready PDF was touched by the later run")
	}
}
