package documents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/documents/assets"
	"applysmith/internal/documents/toolchain"
)

func TestResumeChain_ContactBlockFromProfile(t *testing.T) {
	const email, phone = "alex.example@example.com", "+1 555 0100"
	m := echoModel(nil)
	f := newFixture(t, fixtureOpts{model: m})
	res := f.run(Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting})
	if !res.Published || res.Generation != GenModel {
		t.Fatalf("result = %+v", res)
	}
	for _, req := range m.requestsTo("resume-write") {
		for _, secret := range []string{email, phone, "Alex Example"} {
			if strings.Contains(req.Prompt, secret) {
				t.Errorf("the contact value %q reached the model", secret)
			}
		}
		if !strings.Contains(req.Prompt, "Do not write names, contact details, locations or a relocation statement") {
			t.Error("the prompt does not forbid a model-written header")
		}
	}
	d, err := f.svc.Get(t.Context(), res.Documents[0])
	must(t, err)
	tex := f.read(d.Path + "/resume.tex")
	for _, want := range []string{"Alex", "Example", email, "Sampleville, Exampleland", "open to relocation"} {
		if !strings.Contains(tex, want) {
			t.Errorf("resume.tex lacks the profile value %q", want)
		}
	}

	// A profile that is closed to relocation, in another city: the header
	// follows the profile, the phrase is gone.
	closed := strings.NewReplacer("default: open", "default: closed", "city: Sampleville", "city: Otherton").Replace(testProfile)
	g := newFixture(t, fixtureOpts{profile: closed})
	r2 := g.run(Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: g.cand, Posting: testPosting})
	d2, err := g.svc.Get(t.Context(), r2.Documents[0])
	must(t, err)
	tex2 := g.read(d2.Path + "/resume.tex")
	if !strings.Contains(tex2, "Otherton, Exampleland") || strings.Contains(tex2, "relocation") {
		t.Errorf("resume.tex does not follow the profile:\n%.800s", tex2)
	}
}

func TestResumeChain_NoValidDraft_FailsClosed(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	req := Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting}
	first := f.run(req)
	if !first.Published {
		t.Fatalf("first = %+v", first)
	}
	before, err := f.svc.List(t.Context(), f.cand)
	must(t, err)
	tree := treeHash(t, filepath.Join(f.ws.Root, "documents"))

	// Nothing in cv.md or the profile can be written from: no draft passes.
	must(t, os.WriteFile(filepath.Join(f.ws.Root, "cv.md"), []byte("# Alex Example\n"), 0o600))
	bare := strings.Replace(testProfile, "skills:\n  - name: Go\n  - name: SQL\n", "", 1)
	bare = bare[:strings.Index(bare, "history:")]
	must(t, os.WriteFile(filepath.Join(f.ws.Root, "profile.yml"), []byte(bare), 0o600))
	f.clk.mu.Lock()
	f.clk.t = f.clk.t.AddDate(0, 0, 1)
	f.clk.mu.Unlock()
	second := f.run(req)
	if second.Published || second.Reason == "" {
		t.Fatalf("a run with nothing to write from published: %+v", second)
	}
	after, err := f.svc.List(t.Context(), f.cand)
	must(t, err)
	if len(after) != len(before) {
		t.Errorf("documents went from %d to %d", len(before), len(after))
	}
	now := treeHash(t, filepath.Join(f.ws.Root, "documents"))
	if len(now) != len(tree) {
		t.Errorf("the failed run left files: %d then %d", len(tree), len(now))
	}
	for p, h := range tree {
		if now[p] != h {
			t.Errorf("%s changed", p)
		}
	}
}

func TestCoverLetter_StructureAndLength(t *testing.T) {
	m := testManifest(t)
	word := strings.Repeat("backend ", 1)
	body := func(n int) string { return strings.Repeat(word, n) + "services." }
	good := "I am writing to apply for the Backend Engineer role at Example Corp, where backend services in Go are central to the work."
	letter := func(words int) LetterDoc {
		d := letterDoc(m, good, "I would welcome a conversation. Thank you for reading.")
		d.Body = body(words)
		return d
	}
	short := checkLetter(m, letter(40))
	if !short.OK() || !hasWarning(short, "length") {
		t.Errorf("a shorter grounded letter is allowed with a warning: errors %+v warnings %+v", short.Errors, short.Warnings)
	}
	in := letter(250)
	if g := checkLetter(m, in); hasWarning(g, "length") {
		t.Errorf("%d words are inside the target: %+v", m.words(in), g.Warnings)
	}
	if g := checkLetter(m, letter(400)); !hasWarning(g, "length") {
		t.Errorf("a letter over 300 words warns: %+v", g.Warnings)
	}

	// The letter is data first, paired with the same frozen manifest as the résumé.
	f := newFixture(t, fixtureOpts{})
	res := f.run(Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting})
	docs, err := f.svc.List(t.Context(), f.cand)
	must(t, err)
	_ = res
	ld, rd := f.letterDoc(byKind(docs, KindCover)), f.resumeDoc(byKind(docs, KindResume))
	if ld.ManifestHash == "" || ld.ManifestHash != rd.ManifestHash {
		t.Errorf("letter manifest %q, résumé manifest %q", ld.ManifestHash, rd.ManifestHash)
	}
	if ld.Opening == "" || ld.Closing == "" || ld.Salutation == "" {
		t.Errorf("the letter is not structured data: %+v", ld)
	}
}

// A model letter may leave the body empty and go straight to its bullets
// (measured on a paired run); the TeX then held "\lettercontent{}" and XeLaTeX
// stopped with no PDF.
func TestCoverLetter_EmptyBodyPrintsNoEmptyParagraph(t *testing.T) {
	m := factManifest(t)
	l := letterWith(m, slotApply, "", slotClosing)
	l = withBullet(t, m, l, "Led the platform migration and cut incidents by 40% across 12 services.")
	c, err := buildCover(m, l, assets.PaperA4, time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC))
	must(t, err)
	tex, err := assets.RenderCover(c)
	must(t, err)
	got := string(tex)
	if strings.Contains(got, `\lettercontent{}`) {
		t.Errorf("an empty paragraph is printed:\n%s", got)
	}
	if i, j := strings.Index(got, slotApply), strings.Index(got, `\item Led the platform migration`); i < 0 || j < i {
		t.Errorf("the bullets do not follow the opening:\n%s", got)
	}
}

func TestCoverLetter_RelocationSentenceOnce(t *testing.T) {
	printed := func(profile string) (md, tex string) {
		f := newFixture(t, fixtureOpts{profile: profile})
		res := f.run(Request{Action: ActionCover, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting})
		d, err := f.svc.Get(t.Context(), res.Documents[0])
		must(t, err)
		return f.read(d.Path + "/cover-letter.md"), f.read(d.Path + "/cover-letter.tex")
	}
	md, tex := printed(testProfile)
	for name, got := range map[string]string{"markdown": md, "TeX": tex} {
		if n := strings.Count(strings.ToLower(got), "open to relocation"); n != 1 {
			t.Errorf("%s says it %d times:\n%s", name, n, got)
		}
	}
	paragraphs := strings.Split(strings.TrimSpace(md), "\n\n")
	if last := paragraphs[len(paragraphs)-1]; !strings.HasSuffix(last, "I am open to relocation.") {
		t.Errorf("the sentence closes the letter: %q", last)
	}
	md, tex = printed(strings.Replace(testProfile, "default: open", "default: closed", 1))
	if strings.Contains(strings.ToLower(md+tex), "relocat") {
		t.Errorf("a closed profile says nothing about relocation:\n%s", md)
	}
}

func TestRuns_StageProgressAndCancel(t *testing.T) {
	var mu sync.Mutex
	var stages []string
	fb := &fakeBuilder{}
	f := newFixture(t, fixtureOpts{model: echoModel(nil), builder: func(*toolchain.Toolchain) pdfBuilder { return fb },
		emit: func(ev Event) {
			mu.Lock()
			defer mu.Unlock()
			if !ev.Final && ev.Stage != "" && (len(stages) == 0 || stages[len(stages)-1] != ev.Stage) {
				stages = append(stages, ev.Stage)
			}
		}})
	res := f.run(Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, PDF: true})
	if !res.Published {
		t.Fatalf("result = %+v", res)
	}
	mu.Lock()
	got := strings.Join(stages, ",")
	mu.Unlock()
	want := []string{StageRetrieve, StageProfile, StageManifest, StageDraft, StageReview, StageRevise, StageCompile, StageVerify, StagePublish}
	if got != strings.Join(want, ",") {
		t.Errorf("stages = %s, want %s", got, strings.Join(want, ","))
	}

	// A cancelled run reports the real stage it was in and publishes nothing.
	started := make(chan struct{})
	var last string
	g := newFixture(t, fixtureOpts{emit: func(ev Event) {
		mu.Lock()
		defer mu.Unlock()
		if !ev.Final && ev.Stage != "" {
			last = ev.Stage
		}
	}})
	g.svc.d.Model = &blockingModel{started: started}
	h, err := g.svc.Submit(t.Context(), Claim{Principal: "test", Key: "stage-cancel-1"}, Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: g.cand, Posting: testPosting})
	must(t, err)
	<-started
	if !g.svc.Cancel(h.RunID) {
		t.Fatal("cancel refused")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cancelled, err := g.svc.Wait(ctx, h.RunID)
	must(t, err)
	if cancelled.Published || cancelled.Reason != ReasonCancelled {
		t.Errorf("result = %+v", cancelled)
	}
	mu.Lock()
	defer mu.Unlock()
	if last != StageDraft {
		t.Errorf("the cancelled run's last stage = %q, want %q", last, StageDraft)
	}
	if docs, _ := g.svc.List(t.Context(), g.cand); len(docs) != 0 {
		t.Errorf("documents after cancel = %d", len(docs))
	}
	if st, _ := g.st.GetRun(t.Context(), h.RunID); st.Status != "cancelled" {
		t.Errorf("run status = %s", st.Status)
	}
}

func TestRender_SchemaFailureWritesNoPDF(t *testing.T) {
	// A required candidate field missing: the run stops before any file or
	// row exists, however much of the PDF path was asked for.
	noEmail := strings.Replace(testProfile, "  email: alex.example@example.com\n", "", 1)
	fb := &fakeBuilder{}
	f := newFixture(t, fixtureOpts{profile: noEmail, builder: func(*toolchain.Toolchain) pdfBuilder { return fb }})
	res := f.run(Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, PDF: true})
	if res.Published || len(res.Documents) != 0 {
		t.Fatalf("a profile without an email published: %+v", res)
	}
	if n := len(fb.callsFor(KindResume)); n != 0 {
		t.Errorf("the toolchain was asked to build %d times", n)
	}
	if files := filesUnder(t, filepath.Join(f.ws.Root, "documents")); len(files) != 0 {
		t.Errorf("files were written: %v", files)
	}

	// A draft in a language the templates do not cover cannot be rendered.
	m := testManifest(t)
	d := docWith(m, "X1", "Led the platform migration", evID(t, m, "platform migration"))
	d.Language = "xx"
	if _, err := buildResume(m, d, "a4"); err == nil {
		t.Error("an unsupported language rendered")
	}
}

// Entry dates print as docs/architecture/document-verification.md ("Entry
// dates") defines them, from either source form: cv.md's heading range and
// profile.yml's start and end are the same strings.
func TestResumeDates_AsDocumented(t *testing.T) {
	for _, c := range []struct{ kind, lang, dates, want string }{
		{EntryExperience, "en", "2021-03 to present", "Mar 2021 - Present"},
		{EntryExperience, "en", "09/2019 - 05/2021", "Sep 2019 - May 2021"},
		{EntryExperience, "en", "Spring 2020 to 2021", "Spring 2020 - 2021"},
		{EntryExperience, "de", "2021-03 to present", "März 2021 - heute"},
		{EntryEducation, "en", "2013-09", "Sep 2013"},
	} {
		_, _, start, end, ok := headingEntry("Engineer, Example Systems (" + c.dates + ")")
		l, _ := lookupLanguage(c.lang)
		if got := dateRange(l, Entry{Kind: c.kind, Start: start, End: end}); !ok || got != c.want {
			t.Errorf("%s %q = %q, want %q", c.lang, c.dates, got, c.want)
		}
	}
}
