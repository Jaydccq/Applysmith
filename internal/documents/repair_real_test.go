package documents

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"applysmith/internal/documents/assets"
	"applysmith/internal/documents/toolchain"
)

// realRun runs a request with PDFs on the installed tools and returns the
// stored documents by kind.
func realRun(t *testing.T, o fixtureOpts, req Request) (*fixture, map[Kind]Document) {
	t.Helper()
	o.toolchn = realToolchain(t)
	f := newFixture(t, o)
	req.Mode, req.CandidacyID, req.Posting, req.PDF = ModeTailored, f.cand, testPosting, true
	if res := f.run(req); !res.Published {
		t.Fatalf("result = %+v", res)
	}
	docs, err := f.svc.List(t.Context(), f.cand)
	must(t, err)
	out := map[Kind]Document{}
	for _, d := range docs {
		out[d.Kind] = d
	}
	return f, out
}

func repairOf(t *testing.T, f *fixture, d Document) (rec RepairRecord) {
	f.load(t, d, repairFile, &rec)
	return
}

// A résumé over its one-page limit is repaired inside two rounds, and the PDF
// that results passes every check of the real verifiers.
func TestDocuments_RealOverflowIsRepairedWithinTwoRounds(t *testing.T) {
	f, docs := realRun(t, fixtureOpts{profile: leanProfile, cv: repairCV(3, 3, 0, 430)}, Request{Action: ActionPair, Options: Options{Pages: 1}})
	d := docs[KindResume]
	rec := repairOf(t, f, d)
	if len(rec.Rounds) == 0 || len(rec.Rounds) > MaxRepairRounds || d.PDFState != PDFReady || !d.Ready || d.Pages != 1 || rec.Outcome != RepairRepaired {
		t.Fatalf("résumé = %+v, record = %+v", d, rec)
	}
	text, cuts := f.read(d.Path+"/resume.txt"), 0
	for _, rd := range rec.Rounds {
		for _, c := range rd.Cuts {
			cuts++
			if c.Reason == "" || strings.Contains(skeleton(text), skeleton(c.Text)) {
				t.Errorf("cut %.50q has no reason or is still in the PDF", c.Text)
			}
		}
	}
	if cuts == 0 || !strings.Contains(text, "alex.example@example.com") {
		t.Errorf("cuts = %d, contact in text = %v", cuts, strings.Contains(text, "alex.example@example.com"))
	}
	v, err := f.svc.Verification(t.Context(), d)
	must(t, err)
	if v == nil || v.Stale != "" || v.State != PDFReady {
		t.Fatalf("verification = %+v", v)
	}
	for _, c := range v.Checks {
		if c.Status != toolchain.Pass {
			t.Errorf("%s %s = %s: %s", c.Tool, c.ID, c.Status, c.Reason)
		}
	}
	var m Manifest
	var stored ResumeDoc
	f.load(t, d, "evidence-manifest.json", &m)
	f.load(t, d, "resume.json", &stored)
	if g := checkResume(m, stored); !g.OK() || docs[KindCover].PDFState != PDFReady {
		t.Errorf("gate after the repair: %v; letter %s", g.Errors, docs[KindCover].PDFState)
	}
}

// When even the protected lines do not fit, the draft stays a failed preview
// and cannot be attached.
func TestDocuments_RealUnresolvedOverflowFailsThePageLimit(t *testing.T) {
	f, docs := realRun(t, fixtureOpts{profile: leanProfile, cv: repairCV(3, 3, 0, 1400)}, Request{Action: ActionPair, Options: Options{Pages: 1}})
	d := docs[KindResume]
	rec := repairOf(t, f, d)
	if len(rec.Rounds) == 0 || len(rec.Rounds) > MaxRepairRounds || rec.Outcome != RepairUnresolved || (len(rec.Rounds) < MaxRepairRounds && rec.Stopped == "") {
		t.Fatalf("record = %+v", rec)
	}
	if d.PDFState != PDFFailed || d.Ready || d.Pages < 2 || d.PDFPath == "" || !strings.Contains(strings.Join(d.Warnings, "|"), "max-pages") {
		t.Fatalf("résumé = %+v; want a retained failed preview over the limit", d)
	}
	var stored ResumeDoc
	f.load(t, d, "resume.json", &stored)
	for _, e := range stored.Experience {
		if len(e.Bullets) != 1 {
			t.Errorf("entry %s keeps %d bullets, want its last one", e.Entry, len(e.Bullets))
		}
	}
}

// A letter whose evidence paragraph runs over the page loses trailing sentences
// until it fits; the opening, the closing and the signature stay.
func TestDocuments_RealLetterOverflowIsRepairedBySentence(t *testing.T) {
	long := strings.TrimSpace(strings.Repeat("I have run backend services in Go and mentored engineers in code review and design. ", 60))
	m := echoModel(func(task string, v any) any {
		if d, ok := v.(LetterDraft); ok {
			d.Body, d.Bullets = long, d.Bullets[:0]
			return d
		}
		return v
	})
	f, docs := realRun(t, fixtureOpts{model: m}, Request{Action: ActionCover})
	d := docs[KindCover]
	rec := repairOf(t, f, d)
	if d.PDFState != PDFReady || d.Pages != 1 || rec.Outcome != RepairRepaired || len(rec.Rounds) == 0 || len(rec.Rounds) > MaxRepairRounds {
		t.Fatalf("letter = %+v, record = %+v", d, rec)
	}
	var l LetterDoc
	f.load(t, d, "cover-letter.json", &l)
	text := f.read(d.Path + "/cover-letter.txt")
	if !terminal(l.Body) || !strings.HasPrefix(l.Body, "I have run") || l.Opening == "" || l.Closing == "" || !strings.Contains(text, "Alex Example") {
		t.Errorf("letter after the repair: %q", l.Body)
	}
}

// proofCV lists three jobs, newest first. Only the oldest holds a bullet that
// supports a requirement of the posting; the rest is filler about other things.
func proofCV(proof string, chars int) string {
	var b strings.Builder
	b.WriteString("# Alex Example\n\n## Summary\n\nBackend engineer focused on reliable data platforms.\n\n## Experience\n\n")
	for i, year := range []int{2024, 2018, 2012} {
		fmt.Fprintf(&b, "### Planner %d, Employer%c Works (%d-01 to %d-12)\n\n", i+1, 'A'+i, year, year+3)
		for j := 1; j <= 3; j++ {
			text := fmt.Sprintf("Organized rota %d of year %d covering scheduling and forecasting for the planning group", j, year)
			if i == 2 && j == 3 {
				text = proof
			}
			for len(text) < chars {
				text += " and reviewed the plan with the team, wrote the notes, and followed up on each open item"
			}
			b.WriteString("- " + text[:chars] + "\n")
		}
		b.WriteString("\n")
	}
	return b.String() + "## Skills\n\n- Languages: Go, SQL\n"
}

// Age is not relevance: when the limit forces cuts, the oldest job's only proof
// of a requirement stays in the PDF while newer filler goes.
func TestDocuments_RealOlderRelevantProofSurvivesTheRepair(t *testing.T) {
	const proof = "Modelled SQL data in PostgreSQL for the reporting group"
	f, docs := realRun(t, fixtureOpts{profile: leanProfile, cv: proofCV(proof, 430)}, Request{Action: ActionPair, Options: Options{Pages: 1}})
	d := docs[KindResume]
	rec := repairOf(t, f, d)
	var m Manifest
	f.load(t, d, "evidence-manifest.json", &m)
	supported, cuts := false, 0
	for _, e := range m.Evidence {
		supported = supported || (strings.HasPrefix(e.Text, proof) && requirementsSupported(m, []string{e.ID}) >= 1)
	}
	for _, rd := range rec.Rounds {
		for _, c := range rd.Cuts {
			cuts++
			if strings.HasPrefix(c.Text, proof) {
				t.Errorf("the only proof of a requirement was cut: %+v", c.Rank)
			}
		}
	}
	if d.PDFState != PDFReady || d.Pages != 1 || !supported || cuts == 0 || !strings.Contains(skeleton(f.read(d.Path+"/resume.txt")), skeleton(proof)) {
		t.Fatalf("résumé = %+v, proof supports a requirement = %v, cuts = %d", d, supported, cuts)
	}
}

// A résumé that fills its final page needs no repair: the PDF is ready,
// attachable, and sets the tracker's résumé flag. The thin final page is
// TestDocuments_RealThinLastPageCutsOnlyWhatItNeeds.
func TestDocuments_RealFilledFinalPageIsReadyAndSetsTheTrackerFlag(t *testing.T) {
	open := realToolchain(t)
	f := newFixture(t, fixtureOpts{toolchn: open, cv: longCV(longFiller[:len(longFiller)-3]), profile: longProfile()})
	r := f.run(Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, PDF: true, Options: Options{Pages: 2}})
	if !r.Published {
		t.Fatalf("result = %+v", r)
	}
	d, err := f.svc.Get(t.Context(), r.Documents[0])
	must(t, err)
	if d.PDFState != PDFReady || !d.Ready || d.PDFPath == "" {
		t.Fatalf("document = state %s ready %v pdf %q; warnings %v", d.PDFState, d.Ready, d.PDFPath, d.Warnings)
	}
	if e, _ := f.tr.Get(t.Context(), f.cand); !e.Entry.ResumePDF {
		t.Error("a stored résumé PDF must set the tracker flag")
	}
}

// A thin last page is repaired in one round by the smallest run of
// lowest-ranked lines: putting the last cut line back makes two pages again.
// Before the sizing read the verifier's pages and text layer, the 180 case cut
// 15 lines where 11 do, and the 165 case (113-character bullets that print on
// one line) needed a second round.
func TestDocuments_RealThinLastPageCutsOnlyWhatItNeeds(t *testing.T) {
	open := realToolchain(t)
	for _, short := range []int{165, 180} {
		t.Run(itoa(short), func(t *testing.T) { thinLastPageCutsOnlyWhatItNeeds(t, open, short) })
	}
}

func thinLastPageCutsOnlyWhatItNeeds(t *testing.T, open func(context.Context) (*toolchain.Toolchain, error), short int) {
	f := newFixture(t, fixtureOpts{toolchn: open, cv: longCV(longFiller[:len(longFiller)-short]), profile: longProfile()})
	r := f.run(Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, PDF: true, Options: Options{Pages: 2}})
	d, err := f.svc.Get(t.Context(), r.Documents[0])
	must(t, err)
	rec := repairOf(t, f, d)
	if d.PDFState != PDFReady || d.Pages != 1 || rec.Outcome != RepairRepaired || len(rec.Rounds) != 1 || !strings.Contains(rec.Rounds[0].Trigger[0], "final-page-thin") {
		t.Fatalf("résumé = %+v, record = %+v", d, rec)
	}
	cuts := rec.Rounds[0].Cuts
	t.Logf("%d line(s) cut", len(cuts))
	if len(cuts) == 0 {
		return // the spacing flag alone was enough: nothing was cut
	}
	var m Manifest
	var stored ResumeDoc
	f.load(t, d, "evidence-manifest.json", &m)
	f.load(t, d, "resume.json", &stored)
	last := cuts[len(cuts)-1]
	if last.Group == groupCompetency {
		label, text, _ := strings.Cut(last.Text, ": ")
		stored.Competencies = append(stored.Competencies, Competency{Label: label, Text: text, Evidence: last.Evidence})
	} else {
		for i, e := range append(stored.Experience, stored.Projects...) {
			if e.Entry == last.Entry {
				b := Bullet{Text: last.Text, Evidence: last.Evidence}
				if i < len(stored.Experience) {
					stored.Experience[i].Bullets = append(stored.Experience[i].Bullets, b)
				} else {
					stored.Projects[i-len(stored.Experience)].Bullets = append(stored.Projects[i-len(stored.Experience)].Bullets, b)
				}
			}
		}
	}
	res, err := buildResume(m, stored, assets.PaperA4)
	must(t, err)
	res.Tight = true
	if o := compileFixture(t, open, KindResume, renderResume(t, res), expectResume(res), assets.PaperA4); o.out.Pages != 2 {
		t.Errorf("with %.50q back the résumé is %d page(s): the repair cut more than one page fewer needs (%d cuts)", last.Text, o.out.Pages, len(cuts))
	}
}
