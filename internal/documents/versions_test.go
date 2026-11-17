package documents

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"applysmith/internal/documents/assets"
	"applysmith/internal/documents/toolchain"
)

func treeHash(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		sum := sha256.Sum256(b)
		out[p] = hex.EncodeToString(sum[:])
		return err
	})
	must(t, err)
	return out
}

func TestRegenerationKeepsEarlierVersionsImmutable(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	req := Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting}
	f.run(req)
	first, _ := f.svc.List(t.Context(), f.cand)
	before := treeHash(t, filepath.Join(f.ws.Root, filepath.FromSlash(first[0].Path)))
	f.clk.mu.Lock()
	f.clk.t = f.clk.t.AddDate(0, 0, 1)
	f.clk.mu.Unlock()
	f.run(req)
	docs, _ := f.svc.List(t.Context(), f.cand)
	if len(docs) != 4 {
		t.Fatalf("documents = %d, want 4 (two versions of two kinds)", len(docs))
	}
	var cur, old int
	for _, d := range docs {
		switch {
		case d.Current:
			cur++
			if d.Version != 2 && d.Bundle == first[0].Bundle {
				t.Errorf("current version = %d", d.Version)
			}
		default:
			old++
		}
	}
	if cur != 2 || old != 2 {
		t.Errorf("current = %d, older = %d", cur, old)
	}
	after := treeHash(t, filepath.Join(f.ws.Root, filepath.FromSlash(first[0].Path)))
	if len(before) != len(after) {
		t.Fatalf("files of version 1 changed: %d then %d", len(before), len(after))
	}
	for p, h := range before {
		if after[p] != h {
			t.Errorf("%s changed after regeneration", p)
		}
	}
	// A recorded version cannot be rewritten even by SQL.
	err := f.st.Write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(), `UPDATE documents SET content_hash = 'x' WHERE id = ?`, docs[0].ID)
		return err
	})
	if err == nil {
		t.Error("the write-once trigger must refuse a changed version")
	}
}

func TestSubmittedVersionIsRetainedUntilExplicitDelete(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	req := Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting}
	res := f.run(req)
	must(t, f.svc.Freeze(t.Context(), f.cand, res.Documents...))
	f.clk.mu.Lock()
	f.clk.t = f.clk.t.AddDate(0, 0, 1)
	f.clk.mu.Unlock()
	f.run(req) // the user regenerates after submitting
	frozen, err := f.svc.Get(t.Context(), res.Documents[0])
	must(t, err)
	if frozen.FrozenAt == "" || !strings.HasSuffix(frozen.Path, "/1") {
		t.Fatalf("frozen = %+v", frozen)
	}
	if err := f.svc.Delete(t.Context(), frozen.ID, false); !errors.Is(err, ErrFrozen) {
		t.Fatalf("delete of a submitted version = %v, want ErrFrozen", err)
	}
	// The frozen stamp cannot be moved or cleared.
	err = f.st.Write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(), `UPDATE documents SET frozen_at = NULL WHERE id = ?`, frozen.ID)
		return err
	})
	if err == nil {
		t.Error("the freeze stamp must not be clearable")
	}
	if _, err := os.Stat(filepath.Join(f.ws.Root, filepath.FromSlash(frozen.SourcePath))); err != nil {
		t.Errorf("the submitted source is gone: %v", err)
	}
	must(t, f.svc.Delete(t.Context(), frozen.ID, true))
	if _, err := f.svc.Get(t.Context(), frozen.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("after the explicit delete: %v", err)
	}
	e, _ := f.tr.Get(t.Context(), f.cand)
	if !e.Entry.CoverLetter {
		t.Error("deleting a document must not clear the sticky flag or the tracker row")
	}
}

// Deleting the only version of a bundle frees its number: the same request
// made again the same day publishes, rather than colliding with what the
// deleted version left behind.
func TestDeletedVersionCanBeMadeAgain(t *testing.T) {
	f := newFixture(t, fixtureOpts{noReport: true})
	req := Request{Action: ActionResume, Mode: ModeBaseline}
	first := f.run(req)
	if !first.Published {
		t.Fatalf("first = %+v", first)
	}
	must(t, f.svc.Delete(t.Context(), first.Documents[0], false))
	if again := f.run(req); !again.Published {
		t.Fatalf("after the delete = %+v", again)
	}
	// A failure the run cannot classify says what went wrong: an artifact row
	// at the next version's path (left by a delete made before versions released
	// theirs) is named, not reported as a bare "error".
	err := f.st.Write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO report_artifacts (kind, path, status, content_hash, created_at, updated_at)
			VALUES ('document', 'documents/baseline-2026-10-11/2/document-manifest.json', 'ready', 'h', 'x', 'x')`)
		return err
	})
	must(t, err)
	if res := f.run(req); res.Published || !strings.Contains(res.Reason, "report_artifacts.path") {
		t.Errorf("an orphan artifact row = %+v, want the constraint named", res)
	}
}

func TestPublicationIsAtomic(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	// A file already at one target path makes the publish conflict after other
	// files may have been linked; nothing of the version may remain.
	bundle := "example-corp-backend-engineer-2026-10-11"
	blocker := filepath.Join(f.ws.Root, "documents", bundle, "1", "resume.json")
	must(t, os.MkdirAll(filepath.Dir(blocker), 0o700))
	must(t, os.WriteFile(blocker, []byte("someone else's bytes"), 0o600))
	h, err := f.svc.Submit(t.Context(), Claim{Principal: "test", Key: "atomic-key-1"}, Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting})
	must(t, err)
	res, err := f.svc.Wait(t.Context(), h.RunID)
	must(t, err)
	if res.Published || res.Reason != ReasonWrite {
		t.Fatalf("result = %+v", res)
	}
	files := treeHash(t, filepath.Join(f.ws.Root, "documents"))
	if len(files) != 1 {
		t.Errorf("files left after the failed publish: %v", files)
	}
	if b, _ := os.ReadFile(blocker); string(b) != "someone else's bytes" {
		t.Error("the foreign file was overwritten")
	}
	docs, _ := f.svc.List(t.Context(), f.cand)
	e, _ := f.tr.Get(t.Context(), f.cand)
	if len(docs) != 0 || e.Entry.CoverLetter {
		t.Errorf("rows or flags committed without files: %d documents, cover flag %v", len(docs), e.Entry.CoverLetter)
	}
	var n int
	if err := f.st.Read().QueryRow(`SELECT count(*) FROM report_artifacts WHERE kind = ?`, artifactKind).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d artifact rows committed, %v", n, err)
	}
}

func TestBelowFloorRefusesPDFNotDrafts(t *testing.T) {
	f := newFixture(t, fixtureOpts{score: true, toolchn: func(context.Context) (*toolchain.Toolchain, error) { return &toolchain.Toolchain{}, nil }})
	_, err := f.svc.Submit(t.Context(), Claim{Principal: "test", Key: "floor-key-001"}, Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, PDF: true})
	var floor *BelowFloorError
	if !errors.As(err, &floor) || !strings.Contains(err.Error(), "4.5") || !strings.Contains(err.Error(), "override") {
		t.Fatalf("err = %v, want the floor, the score and the override instruction", err)
	}
	if res := f.run(Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting}); !res.Published {
		t.Errorf("a draft below the floor must still be allowed: %+v", res)
	}
}

// A cover letter an application form asks for is written whatever the posting
// scored, the owner's default; "eligible" keeps the floor for it, and a letter
// nobody asked for and a résumé keep it either way.
func TestACoverLetterAFormAsksForClearsTheFloorByDefault(t *testing.T) {
	f := newFixture(t, fixtureOpts{score: true, toolchn: func(context.Context) (*toolchain.Toolchain, error) { return &toolchain.Toolchain{}, nil }})
	ctx := t.Context()
	var floor *BelowFloorError
	submit := func(key string, r Request) error {
		_, err := f.svc.Submit(ctx, Claim{Principal: "test", Key: key}, r)
		return err
	}
	asked := Request{Action: ActionCover, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, PDF: true, Asked: true}
	if err := submit("asked-cover-001", asked); err != nil {
		t.Fatalf("an asked-for letter: %v", err)
	}
	unasked := asked
	unasked.Asked = false
	if err := submit("unasked-cover-1", unasked); !errors.As(err, &floor) {
		t.Errorf("a letter nobody asked for: %v", err)
	}
	resume := asked
	resume.Action = ActionResume
	if err := submit("asked-resume-01", resume); !errors.As(err, &floor) {
		t.Errorf("a résumé: %v", err)
	}
	p, err := f.svc.Preferences(ctx)
	must(t, err)
	eligible := CoverEligible
	_, err = f.svc.SetPreferences(ctx, PreferencePatch{CoverWhenAsked: &eligible, Revision: p.Revision})
	must(t, err)
	if err := submit("asked-cover-002", asked); !errors.As(err, &floor) {
		t.Errorf("under eligible: %v", err)
	}
}

func TestBaselineHasNoCandidacyAndNoLetter(t *testing.T) {
	f := newFixture(t, fixtureOpts{noReport: true})
	res := f.run(Request{Action: ActionResume, Mode: ModeBaseline})
	if !res.Published {
		t.Fatalf("result = %+v", res)
	}
	docs, err := f.svc.List(t.Context(), 0)
	must(t, err)
	if len(docs) != 1 || docs[0].CandidacyID != nil || docs[0].Mode != ModeBaseline || !docs[0].Limited || docs[0].Bundle != "baseline-2026-10-11" {
		t.Fatalf("documents = %+v", docs)
	}
	e, _ := f.tr.Get(t.Context(), f.cand)
	if e.Entry.ResumePDF || e.Entry.CoverLetter {
		t.Error("a baseline résumé changes no tracker flags")
	}
	_, err = f.svc.Submit(t.Context(), Claim{Principal: "test", Key: "baseline-cover-1"}, Request{Action: ActionCover, Mode: ModeBaseline})
	if !errors.Is(err, ErrBaselineCover) {
		t.Errorf("baseline letter: %v", err)
	}
	if !strings.Contains(f.read(docs[0].Path+"/success-profile.md"), "Role:") {
		t.Error("the bundle lacks the success profile")
	}
}

func TestPreferencesAreRevisionedAndValidated(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	ctx := t.Context()
	p, err := f.svc.Preferences(ctx)
	must(t, err)
	if p.Language != "en" || p.Paper != assets.PaperA4 || p.ResumePages != 2 || p.CoverWhenAsked != CoverAlways || p.Revision != 1 {
		t.Fatalf("defaults = %+v", p)
	}
	letter, pages := "letter", 1
	p2, err := f.svc.SetPreferences(ctx, PreferencePatch{Paper: &letter, ResumePages: &pages, Revision: 1})
	must(t, err)
	if p2.Revision != 2 || p2.Paper != assets.PaperLetter || p2.ResumePages != 1 {
		t.Errorf("after patch = %+v", p2)
	}
	if _, err := f.svc.SetPreferences(ctx, PreferencePatch{Paper: &letter, Revision: 1}); !errors.Is(err, ErrConflict) {
		t.Errorf("stale revision: %v", err)
	}
	bad, three := "klingon", 3
	if _, err := f.svc.SetPreferences(ctx, PreferencePatch{Language: &bad, Revision: 2}); !errors.Is(err, ErrInvalid) {
		t.Errorf("language: %v", err)
	}
	if _, err := f.svc.SetPreferences(ctx, PreferencePatch{ResumePages: &three, Revision: 2}); !errors.Is(err, ErrInvalid) {
		t.Errorf("pages: %v", err)
	}
	sometimes := "sometimes"
	if _, err := f.svc.SetPreferences(ctx, PreferencePatch{CoverWhenAsked: &sometimes, Revision: 2}); !errors.Is(err, ErrInvalid) {
		t.Errorf("cover when asked: %v", err)
	}
	// Request options override; the stored preference is the default.
	res := f.run(Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting})
	d, _ := f.svc.Get(ctx, res.Documents[0])
	if d.Paper != "letter" || d.PageLimit != 1 {
		t.Errorf("preferences did not apply: %+v", d)
	}
	res = f.run(Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, Options: Options{Paper: assets.PaperA4, Pages: 2, Language: "de"}})
	d, _ = f.svc.Get(ctx, res.Documents[0])
	if d.Paper != "a4" || d.PageLimit != 2 || d.Language != "de" {
		t.Errorf("request options did not override: %+v", d)
	}
	if !strings.Contains(f.read(d.Path+"/resume.tex"), `Berufserfahrung`) {
		t.Error("the German labels were not used")
	}
}

func TestMissingToolsDegradeOnlyPDFGeneration(t *testing.T) {
	missing := &toolchain.UnavailableError{Report: toolchain.Report{State: toolchain.StateUnavailable, Missing: []string{"lualatex", "pdftotext"}}}
	f := newFixture(t, fixtureOpts{toolchn: func(context.Context) (*toolchain.Toolchain, error) { return nil, missing }})
	_, err := f.svc.Submit(t.Context(), Claim{Principal: "test", Key: "no-tools-0001"}, Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, PDF: true})
	var ue *toolchain.UnavailableError
	if !errors.As(err, &ue) || ue.Code() != "document-toolchain-unavailable" || len(ue.Report.Missing) != 2 {
		t.Fatalf("PDF request without tools: %v", err)
	}
	res := f.run(Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting})
	if !res.Published {
		t.Fatalf("drafting must not need the tools: %+v", res)
	}
}

func TestToolsVanishingMidRunKeepsTheDraft(t *testing.T) {
	calls := 0
	f := newFixture(t, fixtureOpts{toolchn: func(context.Context) (*toolchain.Toolchain, error) {
		calls++
		if calls == 1 {
			return &toolchain.Toolchain{}, nil
		}
		return nil, &toolchain.UnavailableError{Report: toolchain.Report{Missing: []string{"python-venv"}}}
	}})
	res := f.run(Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, PDF: true})
	if !res.Published {
		t.Fatalf("result = %+v", res)
	}
	d, _ := f.svc.Get(t.Context(), res.Documents[0])
	if d.PDFState != PDFUnavailable || d.Ready || d.PDFPath != "" {
		t.Errorf("document = %+v", d)
	}
	e, _ := f.tr.Get(t.Context(), f.cand)
	if e.Entry.ResumePDF {
		t.Error("no PDF was made, so the flag must stay false")
	}
}
