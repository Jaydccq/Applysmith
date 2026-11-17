package documents

import (
	"errors"
	"path/filepath"
	"testing"

	"applysmith/internal/documents/toolchain"
)

func TestEditPublishesANewDraftAndLeavesTheEditedVersionByteIdentical(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	res := f.run(Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting})
	docs, _ := f.svc.List(t.Context(), f.cand)
	var resume, letter Document
	for _, d := range docs {
		if d.Kind == KindResume {
			resume = d
		} else {
			letter = d
		}
	}
	before := treeHash(t, filepath.Join(f.ws.Root, filepath.FromSlash(resume.Path)))
	src, err := f.svc.Source(t.Context(), resume.ID)
	must(t, err)
	draft := *src.Resume
	draft.Profile = "Backend engineer focused on dependable data platforms."

	out := f.run(Request{Action: ActionEdit, DocumentID: resume.ID, Resume: &draft})
	if !out.Published || len(out.Documents) != 2 {
		t.Fatalf("result = %+v (first run %+v)", out, res)
	}
	for p, h := range treeHash(t, filepath.Join(f.ws.Root, filepath.FromSlash(resume.Path))) {
		if before[p] != h {
			t.Errorf("%s of the edited version changed", p)
		}
	}
	for _, id := range out.Documents {
		d, err := f.svc.Get(t.Context(), id)
		must(t, err)
		if d.Version != 2 || d.PDFState != PDFNone || d.PDFPath != "" {
			t.Errorf("new document = %+v", d)
		}
		if v, err := f.svc.Verification(t.Context(), d); err != nil || v != nil {
			t.Errorf("the new revision must have no verdict yet: %+v %v", v, err)
		}
		if rv, _ := f.svc.Review(t.Context(), d); rv != nil {
			t.Errorf("a review of the earlier text was carried over: %+v", rv)
		}
		if d.Kind == KindCover && d.ContentHash != letter.ContentHash {
			t.Error("the letter of the pair must be carried over unchanged")
		}
		if d.Kind == KindResume && d.ContentHash == resume.ContentHash {
			t.Error("the edited résumé must have new content")
		}
	}
	// The edited version can be read back and gated again, which is what a PDF
	// run of it does first.
	fresh, _ := f.svc.Get(t.Context(), out.Documents[0])
	if _, _, err := f.svc.loadVersion(t.Context(), fresh, Request{}); err != nil {
		t.Errorf("the new version does not load: %v", err)
	}
	if err := f.svc.admit(t.Context(), &Request{Action: ActionEdit, DocumentID: resume.ID, Resume: &draft}); !errors.Is(err, ErrConflict) {
		t.Errorf("edit of a replaced version = %v, want ErrConflict", err)
	}
}

func TestEditRefusesASubmittedVersionAnUnchangedTextAndAnUngroundedFigure(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	f.run(Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting})
	docs, _ := f.svc.List(t.Context(), f.cand)
	var resume Document
	for _, d := range docs {
		if d.Kind == KindResume {
			resume = d
		}
	}
	src, err := f.svc.Source(t.Context(), resume.ID)
	must(t, err)

	same := Request{Action: ActionEdit, DocumentID: resume.ID, Resume: src.Resume}
	if err := f.svc.admit(t.Context(), &same); !errors.Is(err, ErrInvalid) {
		t.Errorf("unchanged text = %v, want ErrInvalid", err)
	}
	bad := *src.Resume
	bad.Experience = append([]EntryDraft(nil), bad.Experience...)
	bad.Experience[0].Bullets = []Bullet{{Text: "Cut incidents by 95% across 40 services", Evidence: []string{"E3"}}}
	if err := f.svc.admit(t.Context(), &Request{Action: ActionEdit, DocumentID: resume.ID, Resume: &bad}); !errors.Is(err, ErrNoValidDraft) {
		t.Errorf("ungrounded figure = %v, want ErrNoValidDraft", err)
	}
	must(t, f.svc.Freeze(t.Context(), f.cand, resume.ID))
	if err := f.svc.admit(t.Context(), &Request{Action: ActionEdit, DocumentID: resume.ID, Resume: &bad}); !errors.Is(err, ErrFrozen) {
		t.Errorf("submitted version = %v, want ErrFrozen", err)
	}
	if _, err := f.svc.normalize(t.Context(), Request{Action: ActionEdit, DocumentID: resume.ID}); !errors.Is(err, ErrInvalid) {
		t.Errorf("no text = %v, want ErrInvalid", err)
	}
}

// A version holds the pair, so an edit of one document writes both. The one
// left byte-identical keeps its passed PDF and verdict, and a PDF run builds
// only the other; one whose text changed has no PDF until a PDF run makes it,
// and the ready pointer stays behind.
func TestEditKeepsThePassedPDFOfTheUntouchedDocumentOnly(t *testing.T) {
	fb := &fakeBuilder{}
	f := newFixture(t, fixtureOpts{builder: func(*toolchain.Toolchain) pdfBuilder { return fb }})
	f.run(Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, PDF: true})
	current := func() map[Kind]Document {
		docs, err := f.svc.List(t.Context(), f.cand)
		must(t, err)
		out := map[Kind]Document{}
		for _, d := range docs {
			if d.Current {
				out[d.Kind] = d
			}
		}
		return out
	}
	v1 := current()
	if !v1[KindResume].Ready || !v1[KindCover].Ready {
		t.Fatalf("the first pair is not ready: %+v", v1)
	}
	pdf := f.read(v1[KindResume].PDFPath)

	src, err := f.svc.Source(t.Context(), v1[KindCover].ID)
	must(t, err)
	letter := *src.Letter
	letter.Closing = "I would welcome a conversation about the role."
	f.run(Request{Action: ActionEdit, DocumentID: v1[KindCover].ID, Letter: &letter})
	v2 := current()
	if r := v2[KindResume]; r.Version != 2 || !r.Ready || r.PDFPath == v1[KindResume].PDFPath || f.read(r.PDFPath) != pdf {
		t.Fatalf("the untouched résumé must be ready in version 2 with the same PDF bytes: %+v", r)
	}
	if v, err := f.svc.Verification(t.Context(), v2[KindResume]); err != nil || v == nil || v.Stale != "" || v.State != PDFReady {
		t.Errorf("the kept verdict must still describe the files: %+v %v", v, err)
	}
	if c := v2[KindCover]; c.Ready || c.PDFState != PDFNone {
		t.Errorf("the edited letter must have no PDF yet: %+v", c)
	}

	built := len(fb.calls)
	f.run(Request{Action: ActionPDF, DocumentID: v2[KindCover].ID})
	v3 := current()
	if !v3[KindResume].Ready || !v3[KindCover].Ready || f.read(v3[KindResume].PDFPath) != pdf {
		t.Fatalf("the PDF run must leave both ready with the résumé unchanged: %+v", v3)
	}
	for _, c := range fb.calls[built:] {
		if c.Kind == KindResume {
			t.Error("the PDF run rebuilt the résumé that had passed")
		}
	}

	rsrc, err := f.svc.Source(t.Context(), v3[KindResume].ID)
	must(t, err)
	resume := *rsrc.Resume
	resume.Profile = "Backend engineer focused on dependable data platforms."
	f.run(Request{Action: ActionEdit, DocumentID: v3[KindResume].ID, Resume: &resume})
	v4 := current()
	if r := v4[KindResume]; r.Version != 4 || r.Ready || r.PDFState != PDFNone || r.PDFPath != "" {
		t.Errorf("a résumé whose text changed must not keep the PDF: %+v", r)
	}
	if !v4[KindCover].Ready {
		t.Errorf("the untouched letter must keep its PDF: %+v", v4[KindCover])
	}
	docs, err := f.svc.List(t.Context(), f.cand)
	must(t, err)
	for _, d := range docs {
		if d.Kind == KindResume && d.Ready && d.ID != v3[KindResume].ID {
			t.Errorf("the ready résumé must stay version 3, got version %d", d.Version)
		}
	}
}

// After a one-document run, the other document's current version sits in an
// older pair. Editing it must not bring that pair's replaced partner back as
// current, and a PDF run of a replaced version is refused for the same reason.
func TestEditAfterALetterOnlyRunKeepsTheNewerLetter(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	f.run(Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting})
	f.run(Request{Action: ActionCover, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting})
	docs, err := f.svc.List(t.Context(), f.cand)
	must(t, err)
	var resume, letter, oldLetter Document
	for _, d := range docs {
		switch {
		case d.Kind == KindResume && d.Current:
			resume = d
		case d.Kind == KindCover && d.Current:
			letter = d
		case d.Kind == KindCover:
			oldLetter = d
		}
	}
	if resume.Version != 1 || letter.Version != 2 || oldLetter.Version != 1 {
		t.Fatalf("setup: résumé v%d, letter v%d, old letter v%d", resume.Version, letter.Version, oldLetter.Version)
	}
	src, err := f.svc.Source(t.Context(), resume.ID)
	must(t, err)
	draft := *src.Resume
	draft.Profile = "Backend engineer focused on dependable data platforms."
	if out := f.run(Request{Action: ActionEdit, DocumentID: resume.ID, Resume: &draft}); !out.Published {
		t.Fatalf("edit = %+v", out)
	}
	docs, err = f.svc.List(t.Context(), f.cand)
	must(t, err)
	for _, d := range docs {
		if d.Kind == KindCover && d.Current && d.ID != letter.ID {
			t.Errorf("the replaced letter of version 1 came back as current in version %d", d.Version)
		}
	}
	if err := f.svc.admit(t.Context(), &Request{Action: ActionPDF, DocumentID: oldLetter.ID}); !errors.Is(err, ErrConflict) {
		t.Errorf("PDF of a replaced version = %v, want ErrConflict", err)
	}
}
