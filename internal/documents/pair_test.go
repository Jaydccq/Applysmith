package documents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDraftOnlyPairWithoutProvider(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	res := f.run(Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting})
	if !res.Published || len(res.Documents) != 2 {
		t.Fatalf("result = %+v", res)
	}
	docs, err := f.svc.List(t.Context(), f.cand)
	must(t, err)
	if len(docs) != 2 {
		t.Fatalf("documents = %d", len(docs))
	}
	for _, d := range docs {
		if d.PDFState != PDFNone || d.Generation != string(GenDeterministic) || !d.Current || d.Ready {
			t.Errorf("%s: %+v", d.Kind, d)
		}
	}
	dir := docs[0].Path
	for _, n := range []string{"document-manifest.json", "posting.md", "success-profile.md", "resume.json", "resume.tex", "cover-letter.tex", "cover-letter.json", "evidence-manifest.json", "document-review.json"} {
		f.read(dir + "/" + n)
	}
	tex := f.read(dir + "/resume.tex")
	if !strings.Contains(tex, `Example Systems`) || !strings.Contains(tex, `Mar 2021 - Present`) || !strings.Contains(tex, `\&`) {
		t.Errorf("resume.tex:\n%s", tex)
	}
	e, _ := f.tr.Get(t.Context(), f.cand)
	if !e.Entry.CoverLetter || e.Entry.ResumePDF {
		t.Errorf("flags = resume %v cover %v", e.Entry.ResumePDF, e.Entry.CoverLetter)
	}
	t.Log(f.read(dir + "/cover-letter.md"))
	t.Log(f.read(dir + "/resume.md"))
	t.Log(res.Warnings)
}

func recordingModel(coverPrompts *[]string) *scriptModel {
	inner := echoModel(nil)
	return &scriptModel{fn: func(task, prompt string) (any, error) {
		if task == "cover-letter" {
			*coverPrompts = append(*coverPrompts, prompt)
		}
		return inner.fn(task, prompt)
	}}
}

// load reads one JSON file of a stored version.
func (f *fixture) load(t *testing.T, d Document, name string, v any) {
	t.Helper()
	must(t, json.Unmarshal([]byte(f.read(d.Path+"/"+name)), v))
}

func (f *fixture) resumeDoc(d Document) (rd ResumeDoc) {
	f.t.Helper()
	f.load(f.t, d, "resume.json", &rd)
	return rd
}

func (f *fixture) letterDoc(d Document) (ld LetterDoc) {
	f.t.Helper()
	f.load(f.t, d, "cover-letter.json", &ld)
	return ld
}

func byKind(docs []Document, k Kind) Document {
	for _, d := range docs {
		if d.Kind == k {
			return d
		}
	}
	return Document{}
}

// evidenceLines are the ids the evidence block of a prompt offers.
func evidenceLines(prompt string) map[string]bool {
	out := map[string]bool{}
	for _, m := range evLine.FindAllStringSubmatch(prompt, -1) {
		out[m[1]] = true
	}
	return out
}

func TestLetterOnlyRunPairsWithTheLatestStoredResume(t *testing.T) {
	var prompts []string
	f := newFixture(t, fixtureOpts{model: recordingModel(&prompts)})
	// With no stored résumé the letter draws on the manifest's selected entries.
	f.run(Request{Action: ActionCover, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting})
	if len(prompts) != 1 || len(evidenceLines(prompts[0])) == 0 {
		t.Fatalf("a letter-only run offered no evidence: %d prompts", len(prompts))
	}
	r := f.run(Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting})
	resume := f.svc.mustGet(t, r.Documents[0])
	used := usedEvidence(f.resumeDoc(resume))
	// voice.md is style, not evidence: an edit of it leaves the résumé the
	// letter pairs with.
	must(t, os.WriteFile(filepath.Join(f.ws.Root, "voice.md"), []byte("Short sentences."), 0o600))
	c := f.run(Request{Action: ActionCover, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting})
	if w := f.svc.mustGet(t, c.Documents[0]).Warnings; slices.ContainsFunc(w, func(s string) bool { return strings.Contains(s, "other sources") }) {
		t.Fatalf("the letter did not pair with the stored résumé: %v", w)
	}
	last := prompts[len(prompts)-1]
	offered := evidenceLines(last)
	// Besides the résumé's evidence the letter sees the lines of no entry
	// (summary, skills), which state the years and tools a fast drafting call
	// otherwise took from the posting ("five years" for a nine-year CV).
	for id := range offered {
		if !slices.Contains(used, id) && !strings.Contains(last, id+" [] ") {
			t.Errorf("the letter prompt offers %s, an entry line the stored résumé does not cite", id)
		}
	}
	for _, id := range used {
		if !offered[id] {
			t.Errorf("the letter prompt lacks %s of the stored résumé", id)
		}
	}
}

func (s *Service) mustGet(t *testing.T, id int64) Document {
	t.Helper()
	d, err := s.Get(t.Context(), id)
	must(t, err)
	return d
}

func TestDeterministicLetterDrawsOnTheResumeEvidenceOnly(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	f.run(Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting})
	docs, _ := f.svc.List(t.Context(), f.cand)
	var m Manifest
	must(t, json.Unmarshal([]byte(f.read(docs[0].Path+"/evidence-manifest.json")), &m))
	all, ok := deterministicLetter(m, ResumeDoc{}, "en")
	if !ok || len(all.Bullets) < 2 {
		t.Fatalf("letter without a résumé = %+v", all)
	}
	only := all.Bullets[1].Evidence[0]
	r := ResumeDoc{ResumeDraft: ResumeDraft{Experience: []EntryDraft{{Entry: "X1", Bullets: []Bullet{{Text: "x", Evidence: []string{only}}}}}}}
	got, _ := deterministicLetter(m, r, "en")
	if len(got.Bullets) != 1 || got.Bullets[0].Evidence[0] != only {
		t.Errorf("bullets = %+v; want only the evidence %s the résumé cites", got.Bullets, only)
	}
}

func TestPairWhoseLetterFailsPublishesTheResumeWithAWarning(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	// German needs a model for the letter; the deterministic résumé works.
	res := f.run(Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, Options: Options{Language: "de"}})
	if !res.Published || len(res.Documents) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if w := strings.Join(res.Warnings, "|"); !strings.Contains(w, "the cover letter was not written") || !strings.Contains(w, `language "de"`) {
		t.Errorf("warnings = %v; want the letter failure and its reason", res.Warnings)
	}
	docs, _ := f.svc.List(t.Context(), f.cand)
	if len(docs) != 1 || docs[0].Kind != KindResume {
		t.Fatalf("documents = %+v", docs)
	}
	if d := byKind(docs, KindCover); d.ID != 0 {
		t.Errorf("a letter was stored: %+v", d)
	}
	if strings.Contains(strings.Join(docs[0].Warnings, "|"), "the cover letter was not written") == false {
		t.Errorf("the stored résumé does not carry the warning: %v", docs[0].Warnings)
	}
	e, _ := f.tr.Get(t.Context(), f.cand)
	if e.Entry.CoverLetter {
		t.Error("the cover_letter flag was set without a letter")
	}
	out, err := f.svc.Outcome(t.Context(), res.RunID)
	must(t, err)
	if !out.Published || len(out.Documents) != 1 || !strings.Contains(strings.Join(out.Warnings, "|"), "cover letter was not written") {
		t.Errorf("stored outcome = %+v", out)
	}
}

func TestLetterOnlyRunFailsClosedWhenTheLetterFails(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	res := f.run(Request{Action: ActionCover, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, Options: Options{Language: "de"}})
	if res.Published || res.Reason != ReasonNoDraft || len(res.Documents) != 0 {
		t.Fatalf("result = %+v", res)
	}
	docs, _ := f.svc.List(t.Context(), f.cand)
	if len(docs) != 0 {
		t.Errorf("documents = %+v", docs)
	}
	e, _ := f.tr.Get(t.Context(), f.cand)
	if e.Entry.CoverLetter {
		t.Error("the cover_letter flag was set")
	}
}
