package documents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"applysmith/internal/documents/assets"
)

// envExitOut names a directory that receives the final résumé and letter PDFs
// of the phase exit check, for inspection of their previews.
const envExitOut = "APPLYSMITH_TEST_EXIT_OUT"

// exitFacts are two synthetic company facts, so the preview letter shows the
// company paragraph.
var exitFacts = briefRetriever{"Example Corp runs its event platform on Go services and PostgreSQL.", "Example Corp builds reliable tools for coastal agencies."}

const exitParagraph = "What draws me to Example Corp: Example Corp runs its event platform on Go services and PostgreSQL."

// The fixture of the exit preview quotes its company fact without a toolchain.
func TestDocuments_ExitPreviewFixtureHasACompanyParagraph(t *testing.T) {
	f := newFixture(t, fixtureOpts{cv: longCV(shortFiller), profile: longProfile(), retriever: exitFacts})
	res := f.run(Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, Options: Options{Paper: assets.PaperA4, Pages: 2}})
	docs, _ := f.svc.List(t.Context(), f.cand)
	if !res.Published || len(docs) != 2 {
		t.Fatalf("result = %+v, %d documents", res, len(docs))
	}
	for _, d := range docs {
		if d.Kind == KindCover && !strings.Contains(f.read(d.Path+"/cover-letter.md"), exitParagraph) {
			t.Errorf("the letter lacks the company paragraph:\n%s", f.read(d.Path+"/cover-letter.md"))
		}
	}
}

func TestDocuments_RealExitPreviewPDFs(t *testing.T) {
	open := realToolchain(t)
	f := newFixture(t, fixtureOpts{toolchn: open, cv: longCV(shortFiller), profile: longProfile(), retriever: exitFacts})
	res := f.run(Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, PDF: true,
		Options: Options{Paper: assets.PaperA4, Pages: 2}})
	docs, _ := f.svc.List(t.Context(), f.cand)
	if !res.Published || len(docs) != 2 {
		t.Fatalf("result = %+v, %d documents", res, len(docs))
	}
	out := os.Getenv(envExitOut)
	for _, d := range docs {
		if d.PDFState != PDFReady || !d.Ready {
			t.Errorf("%s: state %s, warnings %v", d.Kind, d.PDFState, d.Warnings)
		}
		if d.Kind == KindCover && !strings.Contains(f.read(d.Path+"/cover-letter.md"), exitParagraph) {
			t.Errorf("the letter lacks the company paragraph %q", exitParagraph)
		}
		if out == "" {
			continue
		}
		if err := os.MkdirAll(out, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, string(d.Kind)+".pdf"), pdfBytes(t, f, d.PDFPath), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
