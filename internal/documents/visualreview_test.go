package documents

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"applysmith/internal/documents/assets"
)

func reviewedFixture(t *testing.T) (*repairRun, Document) {
	t.Helper()
	r := runRepair(t, &fakeBuilder{}, fixtureOpts{cv: testCV, profile: testProfile})
	d := r.docs[KindResume]
	if d.PDFState != PDFReady || d.PDFSHA256 == "" {
		t.Fatalf("document = %+v", d)
	}
	return r, d
}

func TestVisualReview_RecordedAgainstTheExactPDFHash(t *testing.T) {
	r, d := reviewedFixture(t)
	svc := r.f.svc
	if v, _ := svc.Verification(t.Context(), d); v.VisualReview.Status != ReviewPending {
		t.Fatalf("a fresh PDF must read as pending: %+v", v.VisualReview)
	}
	got, err := svc.RecordVisualReview(t.Context(), d.ID, d.PDFSHA256)
	if err != nil || got.VisualReview.Status != ReviewPerformed || got.VisualReview.PDFSHA256 != d.PDFSHA256 {
		t.Fatalf("record = %+v, err %v", got, err)
	}
	v, _ := svc.Verification(t.Context(), d)
	if v.VisualReview.Status != ReviewPerformed || v.VisualReview.PDFSHA256 != d.PDFSHA256 || v.Stale != "" {
		t.Errorf("stored review = %+v (stale %q)", v.VisualReview, v.Stale)
	}
	if other, _ := svc.Verification(t.Context(), r.docs[KindCover]); other.VisualReview.Status != ReviewPending {
		t.Errorf("reviewing the résumé marked the letter: %+v", other.VisualReview)
	}
}

func TestVisualReview_WrongHashAndMissingDocumentAreRefused(t *testing.T) {
	r, d := reviewedFixture(t)
	for name, tc := range map[string]struct {
		id   int64
		hash string
	}{
		"another PDF":    {d.ID, sha([]byte("another.pdf"))},
		"an empty hash":  {d.ID, ""},
		"no such record": {d.ID + 1000, d.PDFSHA256},
	} {
		if _, err := r.f.svc.RecordVisualReview(t.Context(), tc.id, tc.hash); err == nil {
			t.Errorf("%s: the review was recorded", name)
		}
	}
	if _, err := r.f.svc.RecordVisualReview(t.Context(), d.ID, sha([]byte("x"))); !errors.Is(err, ErrInvalid) {
		t.Errorf("a hash that is not the stored PDF's: err = %v, want ErrInvalid", err)
	}
	if v, _ := r.f.svc.Verification(t.Context(), d); v.VisualReview.Status != ReviewPending {
		t.Errorf("a refused review changed the record: %+v", v.VisualReview)
	}
}

// A regenerated PDF is a new file: the earlier look does not carry over, and
// the old hash cannot be recorded for it.
func TestVisualReview_RegeneratedPDFReadsAsUnreviewed(t *testing.T) {
	r, d := reviewedFixture(t)
	f := r.f
	if _, err := f.svc.RecordVisualReview(t.Context(), d.ID, d.PDFSHA256); err != nil {
		t.Fatal(err)
	}
	f.run(Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, PDF: true, Options: Options{Paper: assets.PaperLetter}})
	docs, _ := f.svc.List(t.Context(), f.cand)
	var next Document
	for _, x := range docs {
		if x.Kind == KindResume && x.Current {
			next = x
		}
	}
	if next.ID == d.ID || next.PDFSHA256 == d.PDFSHA256 {
		t.Fatalf("the second run did not make a new PDF: %+v", next)
	}
	if v, _ := f.svc.Verification(t.Context(), next); v.VisualReview.Status != ReviewPending {
		t.Errorf("a regenerated PDF reads as reviewed: %+v", v.VisualReview)
	}
	if _, err := f.svc.RecordVisualReview(t.Context(), next.ID, d.PDFSHA256); !errors.Is(err, ErrInvalid) {
		t.Errorf("the old hash was accepted for the new PDF: %v", err)
	}
}

func TestVisualReview_PDFChangedOnDiskOrRecordUnreadableReadsAsPending(t *testing.T) {
	r, d := reviewedFixture(t)
	svc := r.f.svc
	if _, err := svc.RecordVisualReview(t.Context(), d.ID, d.PDFSHA256); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(r.f.ws.Root, filepath.FromSlash(d.Path))
	if err := os.WriteFile(filepath.Join(dir, visualReviewFile), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if v, _ := svc.Verification(t.Context(), d); v.VisualReview.Status != ReviewPending {
		t.Errorf("an unreadable review record counts as a review: %+v", v.VisualReview)
	}
	if _, err := svc.RecordVisualReview(t.Context(), d.ID, d.PDFSHA256); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.f.ws.Root, filepath.FromSlash(d.PDFPath)), []byte("%PDF-1.7 replaced"), 0o600); err != nil {
		t.Fatal(err)
	}
	v, _ := svc.Verification(t.Context(), d)
	if v.VisualReview.Status != ReviewPending || v.Stale == "" {
		t.Errorf("a replaced PDF kept its review: %+v (stale %q)", v.VisualReview, v.Stale)
	}
	if _, err := svc.RecordVisualReview(t.Context(), d.ID, d.PDFSHA256); !errors.Is(err, ErrInvalid) {
		t.Errorf("a stale record was reviewable: %v", err)
	}
}
