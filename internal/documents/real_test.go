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

// envRealTools opts in to the tests that run the installed LaTeX, Python and
// Poppler tools (make verify-documents).
const envRealTools = "APPLYSMITH_TEST_DOCUMENT_TOOLS"

var (
	realOnce sync.Once
	realCfg  toolchain.Config
	realErr  error
	realTmp  string
)

func TestMain(m *testing.M) {
	code := m.Run()
	if realTmp != "" {
		os.RemoveAll(realTmp)
	}
	os.Exit(code)
}

// realToolchain prepares one private toolchain root for the whole package
// run and returns the opener the service takes. APPLYSMITH_TEST_DOCUMENT_ROOT
// reuses an installed root instead of downloading the locked wheel again.
func realToolchain(t *testing.T) func(context.Context) (*toolchain.Toolchain, error) {
	t.Helper()
	if os.Getenv(envRealTools) != "1" {
		t.Skipf("%s=1 runs the installed LaTeX, Python and Poppler tools (make verify-documents)", envRealTools)
	}
	realOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		root := toolchain.Root(os.Getenv("APPLYSMITH_TEST_DOCUMENT_ROOT"))
		if root == "" {
			var err error
			if realTmp, err = os.MkdirTemp("", "applysmith-docs-real-"); err != nil {
				realErr = err
				return
			}
			root = toolchain.Root(realTmp)
		}
		realCfg = toolchain.Config{Root: root, SearchDirs: toolchain.SearchDirs(os.Getenv)}
		if err := toolchain.Setup(ctx, realCfg); err != nil {
			realErr = err
			return
		}
		tc, err := toolchain.OpenForSmoke(ctx, realCfg)
		if err != nil {
			realErr = err
			return
		}
		_, realErr = tc.Smoke(ctx)
	})
	if realErr != nil {
		t.Fatalf("toolchain setup: %v", realErr)
	}
	return func(ctx context.Context) (*toolchain.Toolchain, error) { return toolchain.Open(ctx, realCfg) }
}

// specialsCV holds bullets with every character TeX treats specially and
// accented letters, plus TeX and shell payloads.
func specialsCV() string {
	return strings.Replace(testCV, "- Mentored four engineers in code review and design",
		"- Mentored four engineers in code review and design\n"+
			"- Handled backend services in Go with PostgreSQL on C:\\temp paths & 100% of {braces}, ~tildes, ^carets and #hashes for Gen\u00e8ve sites at $5M scale\n"+
			"- Reviewed backend services in Go with PostgreSQL, \\input{/etc/hosts} and $(touch pwned) and \\write18{touch pwned} as plain text", 1)
}

func pdfBytes(t *testing.T, f *fixture, rel string) []byte {
	t.Helper()
	b := []byte(f.read(rel))
	if !strings.HasPrefix(string(b[:min(8, len(b))]), "%PDF-") {
		t.Fatalf("%s is not a PDF", rel)
	}
	return b
}

func TestDocuments_RealPairMatrix(t *testing.T) {
	open := realToolchain(t)
	for _, paper := range []assets.Paper{assets.PaperA4, assets.PaperLetter} {
		for _, pages := range []int{1, 2} {
			t.Run(string(paper)+"-"+itoa(pages)+"p", func(t *testing.T) {
				f := newFixture(t, fixtureOpts{toolchn: open, cv: specialsCV()})
				res := f.run(Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, PDF: true,
					Options: Options{Paper: paper, Pages: pages}})
				if !res.Published || len(res.Documents) != 2 {
					t.Fatalf("result = %+v", res)
				}
				docs, _ := f.svc.List(t.Context(), f.cand)
				for _, d := range docs {
					if d.PDFState != PDFReady || !d.Ready || d.Paper != string(paper) || d.Pages < 1 {
						t.Errorf("%s: %+v (warnings %v)", d.Kind, d, d.Warnings)
					}
					switch d.Kind {
					case KindCover:
						if d.Pages != 1 {
							t.Errorf("the letter has %d pages, want exactly 1", d.Pages)
						}
					case KindResume:
						if d.Pages > pages {
							t.Errorf("the résumé has %d pages over the limit %d", d.Pages, pages)
						}
					}
					pdfBytes(t, f, d.PDFPath)
					text := f.read(d.Path + "/" + string(d.Kind) + ".txt")
					for _, want := range []string{"Alex Example", "alex.example@example.com"} {
						if !strings.Contains(text, want) {
							t.Errorf("%s text layer lacks %q", d.Kind, want)
						}
					}
					if d.Kind == KindResume {
						for _, want := range []string{"100% of {braces}", "~tildes", "^carets", "#hashes", "Gen\u00e8ve", "\\input{/etc/hosts}", "$(touch pwned)", "paths & 100%"} {
							if !strings.Contains(text, want) {
								t.Errorf("résumé text layer lacks %q", want)
							}
						}
					}
					if _, err := os.Stat(filepath.Join(f.ws.Root, filepath.FromSlash(d.Path), string(d.Kind)+"-page-1.png")); err != nil {
						t.Errorf("%s has no page preview: %v", d.Kind, err)
					}
				}
				if _, err := os.Stat(filepath.Join(f.ws.Root, "pwned")); err == nil {
					t.Error("a payload created a file")
				}
				if _, err := os.Stat(filepath.Join(f.ws.Root, filepath.FromSlash(docs[0].Path), repairFile)); err == nil {
					t.Error("a document that fits was repaired")
				}
				e, _ := f.tr.Get(t.Context(), f.cand)
				if !e.Entry.ResumePDF || !e.Entry.CoverLetter {
					t.Errorf("flags = résumé %v letter %v", e.Entry.ResumePDF, e.Entry.CoverLetter)
				}
				if v := f.read(docs[0].Path + "/pdf-verification.json"); !strings.Contains(v, `"pending"`) {
					t.Error("visual review must be recorded as pending")
				}
			})
		}
	}
}

// longOrgs are the employers of the long CV and its profile.
var longOrgs = []string{"Example Systems", "Sample Works", "Placeholder Labs", "Sample Holdings", "Example Logistics", "Sample Rail"}

// longCV is a CV with six jobs of three bullets each; filler sets the length
// of a bullet and so how many pages the selected content fills.
func longCV(filler string) string {
	var b strings.Builder
	b.WriteString("# Alex Example\n\n## Summary\n\nBackend engineer focused on reliable data platforms and the teams that run them.\n\n## Experience\n\n")
	for i, org := range longOrgs {
		b.WriteString("### Engineer " + itoa(i+1) + ", " + org + " (" + itoa(2024-3*i) + "-01 to " + itoa(2024-3*i+2) + "-12)\n\n")
		for j := 0; j < 3+i/4; j++ {
			b.WriteString("- Delivered backend service work package " + itoa(j+1) + " for the " + org + " platform group" + filler + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("## Projects\n\n### Reef Survey Tool\n\n- Built a Go service that parsed survey files for the coastal agencies and wrote the weekly summary reports for them\n\n### Harbour Tide Notes\n\n- Wrote a Go command that read tide tables and published the daily harbour notes for the port authority\n\n### Canal Lock Planner\n\n- Built a SQL data model and a Go planner that scheduled lock use for the canal trust\n\n## Skills\n\n- Languages: Go, SQL, Python\n- Tools: PostgreSQL, Docker, Linux\n- Practices: code review, design documents, incident follow-up, mentoring\n- Domains: scheduling, logistics, reporting\n")
	return b.String()
}

const (
	longFiller  = ", covering design review, rollout planning, data modelling in SQL and PostgreSQL, incident follow-up and a written hand-over for the next team on call, with regular reviews of backend services in Go"
	shortFiller = " with backend services in Go"
)

func longProfile() string {
	var b strings.Builder
	b.WriteString(strings.Split(testProfile, "history:")[0] + "history:\n  employment:\n")
	for i, org := range longOrgs {
		b.WriteString("    - employer: " + org + "\n      title: Engineer " + itoa(i+1) + "\n      start: " + itoa(2024-3*i) + "-01\n      end: " + itoa(2024-3*i+2) + "-12\n")
	}
	b.WriteString("  education:\n    - school: Example University\n      degree: BSc\n      field: Mathematics\n      start: 2011-09\n      end: 2015-06\nskills:\n  - name: Go\n")
	return b.String()
}

func TestDocuments_RealTwoPageAndOnePageResume(t *testing.T) {
	open := realToolchain(t)
	f := newFixture(t, fixtureOpts{toolchn: open, cv: longCV(longFiller), profile: longProfile()})
	two := f.run(Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, PDF: true, Options: Options{Pages: 2}})
	short := newFixture(t, fixtureOpts{toolchn: open, cv: longCV(shortFiller), profile: longProfile()})
	one := short.run(Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: short.cand, Posting: testPosting, PDF: true, Options: Options{Pages: 1}})
	d2, _ := f.svc.Get(t.Context(), two.Documents[0])
	d1, _ := short.svc.Get(t.Context(), one.Documents[0])
	t.Logf("two-page limit: %d pages %s %v; one-page limit: %d pages %s %v", d2.Pages, d2.PDFState, d2.Warnings, d1.Pages, d1.PDFState, d1.Warnings)
	if d2.Pages != 2 || d2.PDFState != PDFReady {
		t.Errorf("two-page résumé = %d pages, %s", d2.Pages, d2.PDFState)
		t.Log(f.read(d2.Path + "/pdf-verification.json"))
	}
	if d1.Pages != 1 || d1.PDFState != PDFReady {
		t.Errorf("one-page résumé = %d pages, %s; the page budget should have trimmed the selection", d1.Pages, d1.PDFState)
	}
	if !d2.Ready || !d2.Current {
		t.Errorf("the two-page résumé: current %v ready %v", d2.Current, d2.Ready)
	}
}

// The name must extract as its two words. moderncv's banking style tied the
// names with a space of the body font, which the extractors read as no space
// where the letters meet with little side bearing ("n" then "A"): an
// application's résumé parse took "JordanAvery" for the first name.
func TestDocuments_RealNameExtractsAsTwoWords(t *testing.T) {
	open := realToolchain(t)
	f := newFixture(t, fixtureOpts{toolchn: open, cv: strings.Replace(testCV, "# Alex Example", "# Jordan Avery", 1),
		profile: strings.Replace(testProfile, "full_name: Alex Example", "full_name: Jordan Avery", 1)})
	res := f.run(Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, PDF: true, Options: Options{Pages: 1}})
	d, _ := f.svc.Get(t.Context(), res.Documents[0])
	text := f.read(d.Path + "/" + string(d.Kind) + ".txt")
	if !strings.Contains(text, "Jordan Avery") {
		head, _, _ := strings.Cut(text, "\n")
		t.Errorf("the text layer opens %q", head)
	}
	if d.PDFState != PDFReady {
		t.Errorf("the résumé is %s: %v", d.PDFState, d.Warnings)
	}
}

func TestDocuments_RealOverflowIsAFailedDraftNotAttachable(t *testing.T) {
	open := realToolchain(t)
	// A model letter far beyond one page passes the content gate (it is
	// grounded) but fails the page-count check of the compiled PDF. It is one
	// sentence, so no repair round can shorten it without rewriting it.
	long := strings.Repeat("I have run backend services in Go and mentored engineers in code review and design, ", 40) + "and I enjoy it."
	m := echoModel(func(task string, v any) any {
		if d, ok := v.(LetterDraft); ok {
			d.Body = strings.TrimSpace(long)
			d.Bullets = d.Bullets[:0]
			return d
		}
		return v
	})
	f := newFixture(t, fixtureOpts{toolchn: open, model: m})
	good := f.run(Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, PDF: true})
	if !good.Published {
		t.Fatalf("résumé: %+v", good)
	}
	res := f.run(Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, PDF: true})
	if !res.Published {
		t.Fatalf("result = %+v", res)
	}
	docs, _ := f.svc.List(t.Context(), f.cand)
	var letter Document
	for _, d := range docs {
		if d.Kind == KindCover {
			letter = d
		}
	}
	if letter.PDFState != PDFFailed || letter.Ready || letter.Pages < 2 || letter.PDFPath == "" {
		t.Fatalf("letter = %+v; want a retained failed preview that is not attachable", letter)
	}
	if !strings.Contains(strings.Join(letter.Warnings, "|"), "page-count") {
		t.Errorf("warnings = %v", letter.Warnings)
	}
}

func TestDocuments_RealDraftThenPDFLeavesTheDraftUntouched(t *testing.T) {
	open := realToolchain(t)
	f := newFixture(t, fixtureOpts{toolchn: open})
	draft := f.run(Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting})
	docs, _ := f.svc.List(t.Context(), f.cand)
	for _, d := range docs {
		if d.PDFState != PDFNone || d.PDFPath != "" {
			t.Fatalf("a draft-only run produced %+v", d)
		}
	}
	if matches, _ := filepath.Glob(filepath.Join(f.ws.Root, "documents", "*", "*", "*.pdf")); len(matches) != 0 {
		t.Fatalf("PDFs after a draft-only run: %v", matches)
	}
	before := treeHash(t, filepath.Join(f.ws.Root, filepath.FromSlash(docs[0].Path)))
	res := f.run(Request{Action: ActionPDF, DocumentID: draft.Documents[0]})
	if !res.Published || len(res.Documents) != 2 {
		t.Fatalf("pdf run = %+v", res)
	}
	after := treeHash(t, filepath.Join(f.ws.Root, filepath.FromSlash(docs[0].Path)))
	if len(after) != len(before) {
		t.Errorf("the draft version changed: %d files, then %d", len(before), len(after))
	}
	for p, h := range before {
		if after[p] != h {
			t.Errorf("%s changed", p)
		}
	}
	all, _ := f.svc.List(t.Context(), f.cand)
	var ready int
	for _, d := range all {
		if d.Ready {
			ready++
			if d.Version != 2 {
				t.Errorf("ready version = %d", d.Version)
			}
		}
	}
	if ready != 2 {
		t.Errorf("ready documents = %d, want the 2 of version 2", ready)
	}
}

// A revised draft is what gets compiled: the PDF text, the verdict and the
// content hash all belong to the revision, and the checks pass on it.
func TestDocuments_RealReviewedRevisionIsCompiledAndVerified(t *testing.T) {
	open := realToolchain(t)
	const wording = "Mentored four engineers through code review and design reviews"
	rv := &reviewer{
		review: func(string) reviewReply {
			return reviewReply{Factual: []reviewFinding{{Span: "resume.experience.X1.3", Category: "specificity", Issue: "Name what was reviewed.", Evidence: []string{"E5"}, Edit: wording}},
				Narrative: []narrativeNote{}}
		},
		revise: func(r *ResumeDraft, l *LetterDraft) { r.Experience[0].Bullets[2].Text = wording },
	}
	m := rv.model()
	f := newFixture(t, fixtureOpts{toolchn: open, model: m})
	res := f.run(Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, PDF: true})
	if !res.Published || len(res.Documents) != 2 {
		t.Fatalf("result = %+v", res)
	}
	if got := strings.Join(m.tasks, ","); got != "resume-write,cover-letter,document-review,resume-write" {
		t.Errorf("tasks = %s", got)
	}
	resume := f.svc.mustGet(t, res.Documents[0])
	if resume.PDFState != PDFReady || !resume.Ready {
		t.Fatalf("résumé = %+v warnings %v", resume, res.Warnings)
	}
	if text := f.read(resume.Path + "/resume.txt"); !strings.Contains(text, "design reviews") {
		t.Errorf("the PDF text does not carry the revision:\n%s", text)
	}
	ver, err := f.svc.Verification(t.Context(), resume)
	must(t, err)
	if ver == nil || ver.Stale != "" || ver.State != PDFReady || ver.SourceSHA256 != resume.ContentHash {
		t.Errorf("verification = %+v; want a current verdict bound to the revised source", ver)
	}
	if rec := f.reviewRecord(res); rec.Status != ContentRevised {
		t.Errorf("record = %+v", rec)
	}
}

// Provider fails during review: no retry conversation, and the deterministic
// artifact checks still run on the PDF.
func TestDocuments_RealReviewFailureStillRunsThePDFChecks(t *testing.T) {
	open := realToolchain(t)
	rv := &reviewer{fail: context.DeadlineExceeded}
	m := rv.model()
	f := newFixture(t, fixtureOpts{toolchn: open, model: m})
	res := f.run(Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, PDF: true})
	if !res.Published || m.callsTo("document-review") != 1 {
		t.Fatalf("result = %+v tasks = %v", res, m.tasks)
	}
	resume := f.svc.mustGet(t, res.Documents[0])
	ver, err := f.svc.Verification(t.Context(), resume)
	must(t, err)
	if resume.PDFState != PDFReady || ver == nil || ver.Stale != "" || len(ver.Checks) == 0 {
		t.Errorf("résumé = %+v verification = %+v", resume, ver)
	}
	if rec := f.reviewRecord(res); rec.Status != ContentUnavailable {
		t.Errorf("record = %+v", rec)
	}
}
