package documents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"applysmith/internal/documents/toolchain"
)

// storedVersion writes a version directory holding one PDF and a verdict for it
// and returns the document row that points at it.
func storedVersion(t *testing.T, f *fixture, mutate func(*Verification)) Document {
	t.Helper()
	dir := "documents/example-corp-backend-engineer-2026-10-11/1"
	pdf := []byte("%PDF-1.7 synthetic")
	pages := 1
	text := "Alex Example"
	textSHA := sha([]byte(text))
	out := toolchain.BuildOutput{PDF: pdf, PDFSHA256: sha(pdf), TeXSHA256: sha([]byte("tex")), Pages: 1, Text: text,
		Verify:   toolchain.PDFResult{ScriptVersion: "2", Pages: &pages, TextSHA256: &textSHA, Checks: []toolchain.Check{{ID: "pdf-readable", Status: toolchain.Pass}}},
		Layout:   toolchain.LayoutResult{ScriptVersion: "2", Checks: []toolchain.Check{{ID: "page-text", Status: toolchain.Pass}}},
		Previews: []toolchain.Preview{{Name: "resume-page-1.png", SHA256: sha([]byte("png"))}}}
	b := &built{out: out, state: PDFReady, finals: []toolchain.Check{{ID: CheckContact, Status: toolchain.Pass}}}
	layout := toolchain.LayoutExpect{Template: toolchain.TemplateResume, Paper: "a4"}
	v := newVerification(KindResume, b, sha([]byte("source")), "manifest-hash", layout)
	if mutate != nil {
		mutate(&v)
	}
	body := verificationFileBody{SchemaVersion: verificationSchema, Verifications: map[Kind]Verification{KindResume: v}}
	abs := filepath.Join(f.ws.Root, filepath.FromSlash(dir))
	must(t, os.MkdirAll(abs, 0o700))
	must(t, os.WriteFile(filepath.Join(abs, "resume.pdf"), pdf, 0o600))
	must(t, os.WriteFile(filepath.Join(abs, verificationFile), jsonBytes(body), 0o600))
	return Document{Kind: KindResume, Path: dir, PDFPath: dir + "/resume.pdf", PDFState: PDFReady, PDFSHA256: out.PDFSHA256,
		ContentHash: sha([]byte("source")), Manifest: "manifest-hash"}
}

func TestVerify_StoredVerdictBindsPDFAndSource(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	d := storedVersion(t, f, nil)
	v, err := f.svc.Verification(t.Context(), d)
	if err != nil || v == nil || v.Stale != "" || v.State != PDFReady || len(v.Checks) != 3 {
		t.Fatalf("verification = %+v, err %v", v, err)
	}
	for name, tc := range map[string]func(*Document){
		"another source revision": func(d *Document) { d.ContentHash = sha([]byte("edited")) },
		"another manifest":        func(d *Document) { d.Manifest = "other" },
		"another PDF hash":        func(d *Document) { d.PDFSHA256 = sha([]byte("other.pdf")) },
		"another stored state":    func(d *Document) { d.PDFState = PDFFailed },
	} {
		t.Run(name, func(t *testing.T) {
			edited := d
			tc(&edited)
			v, err := f.svc.Verification(t.Context(), edited)
			if err != nil || v == nil || v.Stale == "" || v.State != PDFUnverified || len(v.Checks) != 0 {
				t.Errorf("the old verdict still applies: %+v, err %v", v, err)
			}
		})
	}
	t.Run("PDF bytes replaced on disk", func(t *testing.T) {
		must(t, os.WriteFile(filepath.Join(f.ws.Root, filepath.FromSlash(d.PDFPath)), []byte("%PDF-1.7 other"), 0o600))
		if v, _ := f.svc.Verification(t.Context(), d); v == nil || v.Stale == "" || v.State != PDFUnverified {
			t.Errorf("verification = %+v", v)
		}
	})
}

func TestVerify_NoRecordOrAnUnreadableOneIsUnverified(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	d := storedVersion(t, f, nil)
	rec := filepath.Join(f.ws.Root, filepath.FromSlash(d.Path), verificationFile)
	for name, content := range map[string]string{"garbage": "not json", "other schema": `{"schemaVersion":9,"verifications":{}}`, "no verdict for the kind": `{"schemaVersion":1,"verifications":{}}`} {
		must(t, os.WriteFile(rec, []byte(content), 0o600))
		if v, err := f.svc.Verification(t.Context(), d); err != nil || v == nil || v.Stale == "" || v.State != PDFUnverified {
			t.Errorf("%s: %+v, err %v", name, v, err)
		}
	}
	must(t, os.Remove(rec))
	if v, err := f.svc.Verification(t.Context(), d); err != nil || v == nil || !strings.Contains(v.Stale, "no verification record") {
		t.Errorf("missing record: %+v, err %v", v, err)
	}
	if v, err := f.svc.Verification(t.Context(), Document{Kind: KindResume, Path: d.Path}); err != nil || v != nil {
		t.Errorf("a document without a PDF has no verification: %+v, err %v", v, err)
	}
}

// Mechanical checks never complete a visual review, and a review of another
// file does not carry over.
func TestVerify_VisualReviewSeparate(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	d := storedVersion(t, f, nil)
	v, _ := f.svc.Verification(t.Context(), d)
	if v.VisualReview.Status != ReviewPending {
		t.Fatalf("a fresh verdict must leave the visual review pending: %+v", v.VisualReview)
	}
	for _, c := range v.Checks {
		if strings.Contains(c.ID, "visual") {
			t.Errorf("a mechanical check named %q claims a visual review", c.ID)
		}
	}
	other := storedVersion(t, f, func(v *Verification) {
		v.VisualReview = VisualReview{Status: ReviewPerformed, PDFSHA256: sha([]byte("another.pdf"))}
	})
	if v, _ := f.svc.Verification(t.Context(), other); v.VisualReview.Status != ReviewPending {
		t.Errorf("a review of another PDF carried over: %+v", v.VisualReview)
	}
	same := storedVersion(t, f, func(v *Verification) { v.VisualReview = VisualReview{Status: ReviewPerformed, PDFSHA256: v.PDFSHA256} })
	if v, _ := f.svc.Verification(t.Context(), same); v.VisualReview.Status != ReviewPerformed {
		t.Errorf("a review of this exact PDF was dropped: %+v", v.VisualReview)
	}
}

func TestReadPDF_ServesOnlyTheBytesTheVersionNames(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	d := storedVersion(t, f, nil)
	got, err := f.svc.ReadPDF(t.Context(), d)
	if err != nil || !strings.HasPrefix(string(got), "%PDF-") {
		t.Fatalf("pdf %q err %v", got, err)
	}
	must(t, os.WriteFile(filepath.Join(f.ws.Root, filepath.FromSlash(d.PDFPath)), []byte("%PDF-1.7 other"), 0o600))
	if _, err := f.svc.ReadPDF(t.Context(), d); err == nil {
		t.Fatal("a replaced file was served")
	}
	if _, err := f.svc.ReadPDF(t.Context(), Document{Kind: KindResume}); err == nil {
		t.Fatal("a version with no PDF was served")
	}
}
