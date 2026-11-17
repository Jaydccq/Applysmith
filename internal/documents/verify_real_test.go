package documents

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"applysmith/internal/documents/assets"
	"applysmith/internal/documents/toolchain"
)

// The fixtures below compile the real templates from fictional data, change
// the TeX the way a template regression would, and run the same build, verify
// and final-check path as a product run. Each one names the check that must
// catch its defect.

func fixtureResume() assets.Resume {
	return assets.Resume{
		Paper: assets.PaperA4, Labels: assets.EnglishLabels, FirstName: "Alex", LastName: "Example",
		Address: "100 Example Street, Sampleville", Phone: "+1 555 0100", Email: "alex.example@example.com",
		Links:   []assets.Link{{Label: "Profile", URL: "https://www.example.com/in/alex-example"}},
		Profile: "Backend engineer who ships reliable data platforms for teams of every size and keeps them running.",
		Competencies: []assets.Competency{
			{Label: "Platforms", Text: "Go, SQL, PostgreSQL, Docker and Linux"},
			{Label: "Practices", Text: "Code review, design documents and incident follow-up"},
		},
		Experience: []assets.Entry{
			{Dates: "2021-03 - Present", Title: "Senior Engineer", Organization: "Example Systems", Location: "Sampleville",
				Bullets: []string{"Led the platform migration and cut incidents by 40% across twelve services",
					"Mentored four engineers in code review and design reviews"}},
			{Dates: "2018-2021", Title: "Engineer", Organization: "Sample Works", Location: "Exampleland",
				Bullets: []string{"Owned on-call for a 99.9% availability target during a period of growth",
					"Built the ingestion service used by twelve teams"}},
			{Dates: "2015-2018", Title: "Analyst", Organization: "Placeholder Labs", Location: "Exampleland",
				Bullets: []string{"Produced weekly forecasts for the planning group"}},
		},
		Education: []assets.Entry{{Dates: "2011-2015", Title: "BSc in Mathematics", Organization: "Example University", Location: "Exampleland"}},
		Languages: "English (native), Exampleish (fluent).",
	}
}

func fixtureCover() assets.Cover {
	return assets.Cover{
		Paper: assets.PaperA4, Name: "Alex Example", Date: "October 11, 2026", Salutation: "Dear Hiring Team,",
		Contact: []assets.Link{{Label: "alex.example@example.com", URL: "mailto:alex.example@example.com"}, {Label: "+1 555 0100"}, {Label: "Profile", URL: "https://www.example.com/in/alex-example"}},
		Blocks: []assets.Block{
			{Text: "I am writing about the Platform Engineer role at Example Systems, where the team runs data services for many customers."},
			{Text: "My most relevant work for the tasks in your posting:", Bullets: []string{
				"Migration: cut incidents by 40% across twelve services", "Mentoring: four engineers, 2021-Present", "Reporting: report time from 5 h to 20 min"}},
			{Text: "I look forward to hearing from you."},
		},
		Closing: "Kind regards,",
	}
}

// outcome is a fixture's build with every check keyed by tool and id.
type outcome struct {
	checks map[string]toolchain.Check
	state  string
	out    toolchain.BuildOutput
}

func (o outcome) failing() []string {
	var out []string
	for k, c := range o.checks {
		if c.Status == toolchain.Fail {
			out = append(out, k)
		}
	}
	return out
}

func (o outcome) status(key string) string { return o.checks[key].Status }

// compileFixture builds tex as a product run would and runs all checks.
func compileFixture(t *testing.T, open func(context.Context) (*toolchain.Toolchain, error), k Kind, tex []byte, exp Expectation, paper assets.Paper) outcome {
	t.Helper()
	o, err := tryFixture(t, open, k, tex, exp, paper)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return o
}

func tryFixture(t *testing.T, open func(context.Context) (*toolchain.Toolchain, error), k Kind, tex []byte, exp Expectation, paper assets.Paper) (outcome, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	tc, err := open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	in := toolchain.BuildInput{Name: string(k), TeX: tex}
	if k == KindResume {
		in.Engine, in.Expect = toolchain.EngineLuaLaTeX, toolchain.PDFExpect{MaxPages: 2, MinChars: 150, ASCIIDates: true}
		in.Layout = toolchain.LayoutExpect{Template: toolchain.TemplateResume, Paper: paper}
	} else {
		in.Engine, in.Cover, in.Expect = toolchain.EngineXeLaTeX, true, toolchain.PDFExpect{Pages: 1, MinChars: 150, ASCIIDates: true}
		in.Layout = toolchain.LayoutExpect{Template: toolchain.TemplateCover, Paper: paper}
	}
	o, err := tc.Build(ctx, in)
	if err != nil {
		return outcome{}, err
	}
	finals := FinalChecks(exp, o)
	state, _ := pdfState(o, finals)
	res := outcome{checks: map[string]toolchain.Check{}, state: state, out: o}
	for _, c := range recordedChecks(o, finals) {
		res.checks[c.Tool+"/"+c.ID] = toolchain.Check{ID: c.ID, Status: c.Status, Reason: c.Reason}
	}
	return res, nil
}

func renderResume(t *testing.T, r assets.Resume) []byte {
	t.Helper()
	b, err := assets.RenderResume(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func renderCover(t *testing.T, c assets.Cover) []byte {
	t.Helper()
	b, err := assets.RenderCover(c)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// mutate changes the rendered TeX and fails the test when the template no
// longer holds the text the fixture depends on.
func mutate(t *testing.T, tex []byte, old, new string, n int) []byte {
	t.Helper()
	s := string(tex)
	if strings.Count(s, old) < 1 {
		t.Fatalf("the template no longer contains %q", old)
	}
	return []byte(strings.Replace(s, old, new, n))
}

// onlyFailing asserts that exactly the named checks failed. The fixtures are
// built so that no other check can see the defect, which shows the named check
// is the one that stops the PDF.
func onlyFailing(t *testing.T, o outcome, want ...string) {
	t.Helper()
	got := o.failing()
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("failing checks = %v, want %v", got, want)
		for k, c := range o.checks {
			if c.Status != toolchain.Pass {
				t.Logf("  %s %s %s", k, c.Status, c.Reason)
			}
		}
	}
	if o.state != PDFFailed {
		t.Errorf("state = %s, want failed", o.state)
	}
}

func TestDocuments_RealVerify_HealthyDocumentsAreReady(t *testing.T) {
	open := realToolchain(t)
	for _, paper := range []assets.Paper{assets.PaperA4, assets.PaperLetter} {
		r := fixtureResume()
		r.Paper = paper
		c := fixtureCover()
		c.Paper = paper
		res := compileFixture(t, open, KindResume, renderResume(t, r), expectResume(r), paper)
		cov := compileFixture(t, open, KindCover, renderCover(t, c), expectCover(c), paper)
		for name, o := range map[string]outcome{"résumé": res, "letter": cov} {
			if o.state != PDFReady || len(o.failing()) != 0 {
				t.Errorf("%s on %s: state %s, failing %v", name, paper, o.state, o.failing())
			}
			if o.out.Layout.Profile == "" || len(o.out.Previews) != o.out.Pages {
				t.Errorf("%s on %s: profile %q, %d previews for %d pages", name, paper, o.out.Layout.Profile, len(o.out.Previews), o.out.Pages)
			}
		}
	}
}

// A short, truthful one-page résumé has a lot of white space below its last
// line; that is not a layout failure.
func TestDocuments_RealVerify_ShortOnePageNotAFailure(t *testing.T) {
	open := realToolchain(t)
	r := fixtureResume()
	r.Experience = r.Experience[:1]
	r.Education = nil
	o := compileFixture(t, open, KindResume, renderResume(t, r), expectResume(r), assets.PaperA4)
	m := o.out.Layout.Measurements[0]
	if o.state != PDFReady || o.out.Pages != 1 || m.BottomShare == nil || *m.BottomShare < 0.5 {
		t.Fatalf("state %s, pages %d, measurement %+v, failing %v", o.state, o.out.Pages, m, o.failing())
	}
}

func TestDocuments_RealVerify_OrphanHeading(t *testing.T) {
	open := realToolchain(t)
	r := fixtureResume()
	tex := mutate(t, renderResume(t, r), `\section{Education}`, `\section{Education}\newpage`, 1)
	o := compileFixture(t, open, KindResume, tex, expectResume(r), assets.PaperA4)
	if o.status("verify_layout/orphaned-entry") != toolchain.Fail || !strings.Contains(o.checks["verify_layout/orphaned-entry"].Reason, "section heading") || o.state != PDFFailed {
		t.Fatalf("state %s, orphan check %+v", o.state, o.checks["verify_layout/orphaned-entry"])
	}
}

func TestDocuments_RealVerify_ClippedContent(t *testing.T) {
	open := realToolchain(t)
	wide := strings.Repeat("x", 125)
	t.Run("résumé", func(t *testing.T) {
		r := fixtureResume()
		tex := mutate(t, renderResume(t, r), "Built the ingestion service", `Built \mbox{`+wide+`} the ingestion service`, 1)
		onlyFailing(t, compileFixture(t, open, KindResume, tex, expectResume(r), assets.PaperA4), "verify_layout/clipping")
	})
	t.Run("letter", func(t *testing.T) {
		c := fixtureCover()
		tex := mutate(t, renderCover(t, c), "I look forward to hearing from you.", `I look forward to hearing from you. \mbox{`+wide+`}`, 1)
		onlyFailing(t, compileFixture(t, open, KindCover, tex, expectCover(c), assets.PaperA4), "verify_layout/clipping")
	})
}

// A 40pt gap between entries is far under the 100pt hole limit; only the
// template's own entry-gap limit sees it.
func TestDocuments_RealVerify_ModerncvEntryGap(t *testing.T) {
	open := realToolchain(t)
	r := fixtureResume()
	tex := mutate(t, renderResume(t, r), "\\vspace{3pt}\n", "\\vspace{40pt}\n", 2)
	o := compileFixture(t, open, KindResume, tex, expectResume(r), assets.PaperA4)
	onlyFailing(t, o, "verify_layout/entry-gap")
	if o.status("verify_layout/internal-hole") != toolchain.Pass {
		t.Error("the gap is meant to stay under the hole limit")
	}
}

func TestDocuments_RealVerify_FooterSignatureCollision(t *testing.T) {
	open := realToolchain(t)
	c := fixtureCover()
	t.Run("signature printed over the footer line", func(t *testing.T) {
		body := `\raggedright\fontspec[Path = fonts/raleway/]{Raleway-Medium}\fontsize{11pt}{13pt}\selectfont Page 1\par`
		tex := mutate(t, renderCover(t, c), `\end{flushright}`, `\vspace*{-13pt}`+body+`\end{flushright}`, 1)
		onlyFailing(t, compileFixture(t, open, KindCover, tex, expectCover(c), assets.PaperA4), "verify_layout/text-overlap")
	})
	t.Run("signature pushed into the footer band", func(t *testing.T) {
		tex := mutate(t, mutate(t, renderCover(t, c), `\thispagestyle{empty}`, `\thispagestyle{fancy}`, 1), `\begin{flushright}`, `\vspace*{372pt}\begin{flushright}`, 1)
		o := compileFixture(t, open, KindCover, tex, expectCover(c), assets.PaperA4)
		if o.status("verify_layout/footer-collision") != toolchain.Fail || o.state != PDFFailed {
			t.Fatalf("state %s, footer %+v", o.state, o.checks["verify_layout/footer-collision"])
		}
	})
}

// The list sits outside the class's paragraph macro, so it must carry the
// body font itself; without it the items fall back to the class's main font.
func TestDocuments_RealVerify_CoverListFont(t *testing.T) {
	open := realToolchain(t)
	c := fixtureCover()
	tex := renderCover(t, c)
	if o := compileFixture(t, open, KindCover, tex, expectCover(c), assets.PaperA4); o.state != PDFReady {
		t.Fatalf("the template's own list must pass: %s %v", o.state, o.failing())
	}
	broken := mutate(t, tex, "\\fontspec[Path = fonts/raleway/, Kerning=Off, Numbers=Lining]{Raleway-Medium}\\fontsize{11pt}{13pt}\\selectfont\n", "\n", 1)
	o := compileFixture(t, open, KindCover, broken, expectCover(c), assets.PaperA4)
	if o.status("final/fonts") != toolchain.Fail || o.state != PDFFailed {
		t.Fatalf("state %s, fonts %+v", o.state, o.checks["final/fonts"])
	}
	// The list font also changes the hyphen glyph, so the date in the first item breaks.
	if o.status("final/dates") != toolchain.Fail {
		t.Errorf("dates = %+v", o.checks["final/dates"])
	}
}

func TestDocuments_RealVerify_PercentAndDateEncoding(t *testing.T) {
	open := realToolchain(t)
	r := fixtureResume()
	tex := renderResume(t, r)
	t.Run("percent sign dropped", func(t *testing.T) {
		o := compileFixture(t, open, KindResume, mutate(t, tex, `40\%`, `40`, 1), expectResume(r), assets.PaperA4)
		onlyFailing(t, o, "final/percent")
	})
	// An unescaped percent comments out the rest of its line, including the
	// closing brace, so the engine stops and no PDF can reach a check at all.
	t.Run("percent sign comments out the line", func(t *testing.T) {
		if o, err := tryFixture(t, open, KindResume, mutate(t, tex, `99.9\%`, `99.9%`, 1), expectResume(r), assets.PaperA4); err == nil {
			t.Errorf("a PDF was produced: state %s", o.state)
		}
	})
	t.Run("double hyphen becomes an en dash", func(t *testing.T) {
		o := compileFixture(t, open, KindResume, mutate(t, tex, "2018-2021", "2018--2021", 1), expectResume(r), assets.PaperA4)
		onlyFailing(t, o, "final/dates", "verify_pdf/ascii-dates")
	})
}

// toolMissing opens the real toolchain and then loses one tool, the way an
// uninstalled Poppler or a broken venv looks to a run that already passed the
// readiness probe.
func toolMissing(open func(context.Context) (*toolchain.Toolchain, error), lose func(*toolchain.Tools)) func(context.Context) (*toolchain.Toolchain, error) {
	return func(ctx context.Context) (*toolchain.Toolchain, error) {
		tc, err := open(ctx)
		if err != nil {
			return nil, err
		}
		broken := *tc
		lose(&broken.Tools)
		return &broken, nil
	}
}

// A check that cannot run leaves the PDF unverified, kept as a preview, never
// ready and never attachable; a failed PDF is just as unattachable, and
// neither replaces an earlier ready version.
func TestDocuments_RealVerify_MissingToolLeavesThePDFUnverified(t *testing.T) {
	open := realToolchain(t)
	for name, tc := range map[string]struct {
		lose  func(*toolchain.Tools)
		blind []string // the tools whose checks cannot run
	}{
		"no pdftotext": {func(tl *toolchain.Tools) { tl.PDFToText = "/nonexistent/pdftotext" }, []string{toolLayout}},
		"no python":    {func(tl *toolchain.Tools) { tl.Python = "/nonexistent/python" }, []string{toolPDF, toolLayout, toolFinal}},
	} {
		lose := tc.lose
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, fixtureOpts{toolchn: open})
			good := f.run(Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, PDF: true})
			if !good.Published {
				t.Fatalf("first run: %+v", good)
			}
			f.svc.d.Toolchain = toolMissing(open, lose)
			res := f.run(Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, PDF: true})
			if !res.Published {
				t.Fatalf("second run: %+v", res)
			}
			docs, _ := f.svc.List(t.Context(), f.cand)
			var sawNew, sawOld int
			for _, d := range docs {
				switch d.Version {
				case 1:
					sawOld++
					if d.PDFState != PDFReady || !d.Ready {
						t.Errorf("the earlier version must stay the ready one: %+v", d)
					}
				case 2:
					sawNew++
					if d.PDFState != PDFUnverified || d.Ready || d.PDFPath == "" {
						t.Errorf("version 2 %s = state %s ready %v pdf %q (warnings %v), want a kept unverified preview", d.Kind, d.PDFState, d.Ready, d.PDFPath, d.Warnings)
					}
					v, err := f.svc.Verification(t.Context(), d)
					if err != nil || v == nil || v.Stale != "" || v.State != PDFUnverified {
						t.Fatalf("verification = %+v, err %v", v, err)
					}
					for _, c := range v.Checks {
						if slices.Contains(tc.blind, c.Tool) && c.Status != toolchain.Unverified {
							t.Errorf("%s/%s = %s although its tool could not run", c.Tool, c.ID, c.Status)
						}
						if c.Status == toolchain.Fail {
							t.Errorf("%s/%s failed: a missing tool is not a defect of the document", c.Tool, c.ID)
						}
					}
				}
			}
			if sawNew != 2 || sawOld != 2 {
				t.Errorf("documents: %d new, %d old", sawNew, sawOld)
			}
		})
	}
}

// The verdict is stored with the hashes of the PDF and the source it
// describes, previews are listed with their own hashes, the visual review is
// pending, and a verdict is not read back for a file it does not describe.
func TestDocuments_RealVerify_RecordBindsTheArtifact(t *testing.T) {
	open := realToolchain(t)
	f := newFixture(t, fixtureOpts{toolchn: open})
	res := f.run(Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, PDF: true})
	if !res.Published {
		t.Fatalf("result = %+v", res)
	}
	docs, _ := f.svc.List(t.Context(), f.cand)
	for _, d := range docs {
		v, err := f.svc.Verification(t.Context(), d)
		if err != nil || v == nil || v.Stale != "" {
			t.Fatalf("%s verification = %+v, err %v", d.Kind, v, err)
		}
		if !d.Ready {
			t.Errorf("%s: a document with a current verdict is not ready: %+v", d.Kind, d)
		}
		tools := map[string]int{}
		for _, c := range v.Checks {
			tools[c.Tool]++
			if c.Status != toolchain.Pass {
				t.Errorf("%s: %s/%s = %s %s", d.Kind, c.Tool, c.ID, c.Status, c.Reason)
			}
		}
		if tools[toolPDF] != 4 || tools[toolFinal] != len(finalCheckIDs) || (tools[toolLayout] != 9 && tools[toolLayout] != 10) {
			t.Errorf("%s: checks per tool = %v", d.Kind, tools)
		}
		if v.State != PDFReady || v.PDFSHA256 != d.PDFSHA256 || v.SourceSHA256 != d.ContentHash || v.ManifestHash != d.Manifest {
			t.Errorf("%s: record %+v does not bind the row %+v", d.Kind, v, d)
		}
		if v.TeXSHA256 != sha([]byte(f.read(d.Path+"/"+string(d.Kind)+".tex"))) {
			t.Errorf("%s: the record names another TeX source", d.Kind)
		}
		if v.VisualReview.Status != ReviewPending || v.VisualReview.PDFSHA256 != "" {
			t.Errorf("%s: visual review = %+v, want pending", d.Kind, v.VisualReview)
		}
		if v.Verifiers.Rules != RulesVersion || v.Verifiers.PDFScript == "" || v.Verifiers.LayoutScript == "" || v.Verifiers.LayoutProfile == "" || v.Verifiers.Poppler == "" || v.Verifiers.Pypdf == "" {
			t.Errorf("%s: verifiers = %+v", d.Kind, v.Verifiers)
		}
		if len(v.Previews) != d.Pages || len(v.Previews) == 0 {
			t.Fatalf("%s: %d previews for %d pages", d.Kind, len(v.Previews), d.Pages)
		}
		for _, p := range v.Previews {
			if sha([]byte(f.read(d.Path+"/"+p.Name))) != p.SHA256 {
				t.Errorf("%s: preview %s does not match its recorded hash", d.Kind, p.Name)
			}
		}

		// Another source revision, or other PDF bytes, voids the verdict.
		edited := d
		edited.ContentHash = strings.Repeat("0", 64)
		if v2, _ := f.svc.Verification(t.Context(), edited); v2 == nil || v2.Stale == "" || v2.State != PDFUnverified || len(v2.Checks) != 0 {
			t.Errorf("%s: a verdict for another source revision still applies: %+v", d.Kind, v2)
		}
		pdf := filepath.Join(f.ws.Root, filepath.FromSlash(d.PDFPath))
		must(t, os.WriteFile(pdf, []byte("%PDF-1.7 replaced"), 0o600))
		if v3, _ := f.svc.Verification(t.Context(), d); v3 == nil || v3.Stale == "" || v3.State != PDFUnverified {
			t.Errorf("%s: a verdict for other PDF bytes still applies: %+v", d.Kind, v3)
		}
		// Ready follows the same check: a replaced PDF is stale for the API and
		// the CLI too, whatever the database row says.
		if got, err := f.svc.Get(t.Context(), d.ID); err != nil || got.Ready || got.PDFState != PDFReady {
			t.Errorf("%s: after the PDF was replaced Get = %+v, err %v; want not ready", d.Kind, got, err)
		}
	}
}
