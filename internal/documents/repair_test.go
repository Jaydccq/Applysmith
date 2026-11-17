package documents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"applysmith/internal/documents/assets"
	"applysmith/internal/documents/toolchain"
)

// leanProfile is testProfile without employment, so the CV's headings make the
// entries.
var leanProfile = testProfile[:strings.Index(testProfile, "  employment:")] + testProfile[strings.Index(testProfile, "  education:"):]

// repairCV has n jobs of k bullets, p projects of two bullets, and bullets
// padded to chars characters when chars is set.
func repairCV(n, k, p, chars int) string {
	var b strings.Builder
	b.WriteString("# Alex Example\n\n## Summary\n\nBackend engineer focused on reliable data platforms.\n\n## Experience\n\n")
	topics := []string{"Go services", "PostgreSQL schemas", "SQL reporting", "Docker builds", "code review habits", "incident follow-up"}
	pad := func(s string) string {
		for len(s) < chars {
			s += " and reviewed the plan with the team, wrote the notes, and followed up on each open item before the next review"
		}
		if chars > 0 {
			s = s[:chars]
		}
		return s
	}
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "### Engineer %d, Employer%c Works (20%02d-01 to 20%02d-12)\n\n", i, 'A'+i-1, 10+i, 10+i)
		for j := 1; j <= k; j++ {
			b.WriteString("- " + pad(fmt.Sprintf("Delivered milestone %d of workstream %d covering %s for the platform group", j, i, topics[(i+j)%len(topics)])) + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("## Projects\n\n")
	for i := 1; i <= p; i++ {
		fmt.Fprintf(&b, "### Project %c\n\n- Built the %s tooling for survey files\n- Documented the %s tooling for new engineers\n\n", 'A'+i-1, topics[i%len(topics)], topics[(i+1)%len(topics)])
	}
	return b.String() + "## Skills\n\n- Languages: Go, SQL, Python\n- Tools: PostgreSQL, Docker\n"
}

type repairRun struct {
	f    *fixture
	res  Result
	docs map[Kind]Document
}

// runRepair runs a pair with PDFs on the scripted builder; the résumé limit is
// two pages and the default CV fills 9 entries with 26 bullets.
func runRepair(t *testing.T, fb *fakeBuilder, o fixtureOpts) *repairRun {
	t.Helper()
	if o.profile == "" {
		o.profile = leanProfile
	}
	if o.cv == "" {
		o.cv = repairCV(5, 4, 3, 0)
	}
	o.builder = func(*toolchain.Toolchain) pdfBuilder { return fb }
	f := newFixture(t, o)
	res := f.run(Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, PDF: true, Options: Options{Pages: 2}})
	r := &repairRun{f: f, res: res, docs: map[Kind]Document{}}
	docs, err := f.svc.List(t.Context(), f.cand)
	must(t, err)
	for _, d := range docs {
		r.docs[d.Kind] = d
	}
	return r
}

func (r *repairRun) record(t *testing.T) (rec RepairRecord) {
	r.f.load(t, r.docs[KindResume], repairFile, &rec)
	return
}

func (r *repairRun) resume(t *testing.T) (d ResumeDoc) {
	r.f.load(t, r.docs[KindResume], "resume.json", &d)
	return
}
func (r *repairRun) letter(t *testing.T) (d LetterDoc) {
	r.f.load(t, r.docs[KindCover], "cover-letter.json", &d)
	return
}
func (r *repairRun) manifest(t *testing.T) (m Manifest) {
	r.f.load(t, r.docs[KindResume], "evidence-manifest.json", &m)
	return
}

func bulletCount(d ResumeDoc) (n int) {
	for _, list := range [][]EntryDraft{d.Experience, d.Projects} {
		for _, e := range list {
			n += len(e.Bullets)
		}
	}
	return n
}

func jsonString(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestRepair_ShortTruthfulResumeIsNotPaddedAndNeedsNoRound(t *testing.T) {
	fb := &fakeBuilder{}
	r := runRepair(t, fb, fixtureOpts{cv: testCV, profile: testProfile})
	d := r.docs[KindResume]
	if d.PDFState != PDFReady || !d.Ready || d.Pages != 1 || len(fb.callsFor(KindResume)) != 1 {
		t.Fatalf("document = %+v, builds = %d", d, len(fb.callsFor(KindResume)))
	}
	if _, err := readRegular(r.f.ws.Root+"/"+d.Path+"/"+repairFile, 1<<20); err == nil {
		t.Error("a document that needed no repair carries a repair record")
	}
	if want := deterministicResume(r.manifest(t), "en"); jsonString(r.resume(t)) != jsonString(want) {
		t.Error("the stored résumé differs from the draft")
	}
}

func TestRepair_CutsTheLowestRankedFirstAndRecordsEveryCut(t *testing.T) {
	r := runRepair(t, &fakeBuilder{resumeCap: 380}, fixtureOpts{})
	d, rec, m := r.docs[KindResume], r.record(t), r.manifest(t)
	if d.PDFState != PDFReady || rec.Outcome != RepairRepaired || len(rec.Rounds) != 1 || len(rec.Rounds[0].Cuts) == 0 {
		t.Fatalf("document = %+v, record = %+v", d, rec)
	}
	cuts, competencies := rec.Rounds[0].Cuts, 0
	for i, c := range cuts {
		if c.Group == groupCompetency {
			competencies++
		}
		if c.Text == "" || !strings.Contains(c.Reason, "lowest-ranked") || !strings.Contains(c.Reason, "max-pages") {
			t.Errorf("cut %d is not fully recorded: %+v", i, c)
		}
		if got := requirementsSupported(m, c.Evidence); got != c.Rank.Requirements {
			t.Errorf("cut %d records %d requirements, its evidence supports %d", i, c.Rank.Requirements, got)
		}
	}
	if got := bulletCount(r.resume(t)); got != 26-len(cuts)+competencies {
		t.Errorf("bullets after %d cuts = %d", len(cuts), got)
	}
	if cuts[0].Rank.Requirements > cuts[len(cuts)-1].Rank.Requirements {
		t.Errorf("the first cut %+v outranks the last %+v", cuts[0].Rank, cuts[len(cuts)-1].Rank)
	}
	// Coverage is updated and followed by the strategy; the gate and the final
	// checks ran again on what was stored.
	if len(rec.Coverage.Before) == 0 || len(rec.Coverage.Before) != len(rec.Coverage.After) || rec.Rounds[0].Gates[KindResume] != "passed" {
		t.Errorf("coverage = %+v, gates = %v", rec.Coverage, rec.Rounds[0].Gates)
	}
	strategy := r.f.read(d.Path + "/application-strategy.md")
	for _, c := range rec.Coverage.After {
		if len(c.Evidence) > 0 && !strings.Contains(strategy, c.Requirement+" is supported by "+strings.Join(c.Evidence, ", ")) {
			t.Errorf("application-strategy.md does not follow the repaired coverage for %s", c.Requirement)
		}
	}
	if g := checkResume(m, r.resume(t)); !g.OK() {
		t.Errorf("grounding gate: %v", g.Errors)
	}
	v, err := r.f.svc.Verification(t.Context(), d)
	must(t, err)
	finals := 0
	for _, c := range v.Checks {
		if c.Tool == toolFinal {
			finals++
		}
		if c.Status != toolchain.Pass {
			t.Errorf("%s %s = %s", c.Tool, c.ID, c.Status)
		}
	}
	if v.Stale != "" || v.State != PDFReady || finals != len(finalCheckIDs) {
		t.Errorf("verification = %+v, %d final checks", v, finals)
	}
}

// Each cut saves half of what a round counts on, so two rounds fall short while
// lines are still left to cut: the bound, not the content, ends the repair.
func TestRepair_AtMostTwoRoundsThenAFailedDraftThatIsNotAttachable(t *testing.T) {
	fb := &fakeBuilder{resumeCap: 250, itemPt: 6}
	r := runRepair(t, fb, fixtureOpts{})
	d, rec, m := r.docs[KindResume], r.record(t), r.manifest(t)
	if len(rec.Rounds) != MaxRepairRounds || rec.Outcome != RepairUnresolved || len(fb.callsFor(KindResume)) != 1+MaxRepairRounds {
		t.Fatalf("record = %+v, builds = %d", rec, len(fb.callsFor(KindResume)))
	}
	if d.PDFState != PDFFailed || d.Ready || d.PDFPath == "" || !strings.Contains(strings.Join(r.res.Warnings, "|"), "unresolved") {
		t.Errorf("document = %+v, want a failed preview that is not ready", d)
	}
	if g := checkResume(m, r.resume(t)); !g.OK() {
		t.Errorf("gate: %v", g.Errors)
	}
	// A requirement the cuts left uncovered is named by the cut and in the strategy.
	strategy := r.f.read(d.Path + "/application-strategy.md")
	for _, req := range rec.lostRequirements() {
		named := false
		for _, rd := range rec.Rounds {
			for _, c := range rd.Cuts {
				named = named || hasID(c.Uncovered, req)
			}
		}
		if !named || !strings.Contains(strategy, req+": layout repair removed the last line") {
			t.Errorf("requirement %s lost its cover unrecorded (cut named: %v)", req, named)
		}
	}
	// The letter still rests on what the résumé shows.
	l := r.letter(t)
	for i, b := range l.Bullets {
		if !overlaps(b.Evidence, usedEvidence(r.resume(t))) {
			t.Errorf("letter bullet %d rests on evidence the résumé no longer shows", i+1)
		}
	}
	if g := checkLetter(m, l); !g.OK() {
		t.Errorf("letter gate: %v", g.Errors)
	}
}

func TestRepair_LetterStillTwoPagesAfterTwoRoundsStaysAFailedPreview(t *testing.T) {
	fb := &fakeBuilder{resumeCap: 600, coverCap: 150}
	r := runRepair(t, fb, fixtureOpts{})
	d, rec, l := r.docs[KindCover], r.record(t), r.letter(t)
	if len(rec.Rounds) == 0 || len(rec.Rounds) > MaxRepairRounds || rec.Outcome != RepairUnresolved || len(fb.callsFor(KindCover)) > 1+MaxRepairRounds {
		t.Fatalf("record = %+v", rec)
	}
	if d.PDFState != PDFFailed || d.Ready || d.Pages < 2 || l.Opening == "" || l.Closing == "" || len(l.Bullets) == 0 {
		t.Errorf("letter = %+v, content = %+v", d, l)
	}
	for _, rd := range rec.Rounds {
		for _, c := range rd.Cuts {
			if c.Document != KindCover || (c.Group != groupLetterBullet && c.Group != groupLetterSentence) {
				t.Errorf("a letter repair cut %+v", c)
			}
			if c.Group == groupLetterSentence && strings.Contains(c.Reason, "requirement(s) supported") {
				t.Errorf("a sentence cut reports a rank it does not have: %q", c.Reason)
			}
		}
	}
	if r.docs[KindResume].PDFState != PDFReady || len(fb.callsFor(KindResume)) != 1 {
		t.Error("the résumé, which was fine, was rebuilt or failed")
	}
}

// Defects that removing text cannot fix are reported, not repaired: clipping,
// overlap, a failed content check and an unavailable verifier start no round.
func TestRepair_UnrepairableDefectsStartNoRound(t *testing.T) {
	unverified := func(fakeCall) map[string]string {
		m := map[string]string{}
		for _, id := range []string{"page-text", "page-size", "clipping", "text-overlap", "internal-hole", "early-page-end", "footer-collision", "final-page-thin", "orphaned-entry", "entry-gap"} {
			m[toolLayout+"/"+id] = toolchain.Unverified
		}
		return m
	}
	for _, c := range []struct {
		name  string
		fb    *fakeBuilder
		state string
	}{
		{"clipping", &fakeBuilder{override: func(fakeCall) map[string]string { return map[string]string{toolLayout + "/clipping": toolchain.Fail} }}, PDFFailed},
		{"overlap", &fakeBuilder{override: func(fakeCall) map[string]string {
			return map[string]string{toolLayout + "/text-overlap": toolchain.Fail}
		}}, PDFFailed},
		{"content check", &fakeBuilder{dropText: "alex.example@example.com"}, PDFFailed},
		{"verifier unavailable", &fakeBuilder{override: unverified}, PDFUnverified},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := runRepair(t, c.fb, fixtureOpts{cv: testCV, profile: testProfile})
			if d := r.docs[KindResume]; len(c.fb.callsFor(KindResume)) != 1 || d.PDFState != c.state || d.Ready {
				t.Errorf("document = %+v, builds = %d", d, len(c.fb.callsFor(KindResume)))
			}
			if _, err := readRegular(r.f.ws.Root+"/"+r.docs[KindResume].Path+"/"+repairFile, 1<<20); err == nil {
				t.Error("a repair record was written for a defect no round can fix")
			}
		})
	}
}

func TestRepair_RefusesContentThatChangedAfterItWasRendered(t *testing.T) {
	fb := &fakeBuilder{resumeCap: 200}
	f := newFixture(t, fixtureOpts{profile: leanProfile, cv: repairCV(5, 4, 3, 0), builder: func(*toolchain.Toolchain) pdfBuilder { return fb }})
	draft := f.run(Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting})
	d := f.svc.mustGet(t, draft.Documents[0])
	var m Manifest
	must(t, json.Unmarshal([]byte(f.read(d.Path+"/evidence-manifest.json")), &m))
	rd := deterministicResume(m, "en")
	req := Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, PDF: true}
	j := &job{s: f.svc, runID: draft.RunID, req: req, partial: map[string][]byte{}, gen: GenDeterministic}
	v := &version{m: m, req: req, lang: "en", paper: assets.PaperA4, pages: 2, resume: &rd, resumeGate: checkResume(m, rd)}
	ts, now := &texSet{tex: map[Kind][]byte{}, expect: map[Kind]Expectation{}}, f.clk.now()
	must(t, render(v, ts, KindResume, now))
	bt, err := j.buildOne(t.Context(), t.Context(), fb, v, ts, KindResume)
	must(t, err)
	if !fb.callsFor(KindResume)[0].Hashed || !defectsOf(bt, 2, false).overflow {
		t.Fatal("the build was not given its TeX hash, or the fixture has no overflow")
	}
	edited := cloneResume(rd)
	edited.Profile += " Edited."
	v.resume = &edited
	err = j.repair(t.Context(), t.Context(), fb, v, ts, map[Kind]*built{KindResume: bt}, now)
	if !errors.Is(err, ErrStaleSource) || !errors.Is(err, ErrConflict) || len(fb.callsFor(KindResume)) != 1 {
		t.Fatalf("err = %v, builds = %d; want a refusal before any round", err, len(fb.callsFor(KindResume)))
	}
	// One level down, TeX that no longer matches the caller's hash is not built.
	if _, err = fb.Build(t.Context(), toolchain.BuildInput{Name: "resume", TeX: []byte("x"), TeXSHA256: sha([]byte("y"))}); !errors.Is(err, toolchain.ErrStaleSource) {
		t.Errorf("err = %v", err)
	}
}

// A PDF run over a stored version whose source changed is refused.
func TestRepair_PDFRunOverChangedSourceIsRefused(t *testing.T) {
	fb := &fakeBuilder{resumeCap: 380}
	f := newFixture(t, fixtureOpts{profile: leanProfile, cv: repairCV(5, 4, 3, 0), builder: func(*toolchain.Toolchain) pdfBuilder { return fb }})
	draft := f.run(Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, Options: Options{Pages: 2}})
	d := f.svc.mustGet(t, draft.Documents[0])
	must(t, os.WriteFile(f.ws.Root+"/"+d.Path+"/resume.json", []byte(f.read(d.Path+"/resume.json")+" "), 0o600))
	if res := f.run(Request{Action: ActionPDF, DocumentID: d.ID}); res.Published || len(fb.calls) != 0 {
		t.Fatalf("result = %+v, builds = %d", res, len(fb.calls))
	}
}

// An explicit PDF action repairs into a new version; the draft stays as it was.
func TestRepair_ExplicitPDFActionRepairsIntoANewVersion(t *testing.T) {
	fb := &fakeBuilder{resumeCap: 380}
	f := newFixture(t, fixtureOpts{profile: leanProfile, cv: repairCV(5, 4, 3, 0), builder: func(*toolchain.Toolchain) pdfBuilder { return fb }})
	draft := f.run(Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, Options: Options{Pages: 2}})
	first := f.svc.mustGet(t, draft.Documents[0])
	before := f.read(first.Path + "/resume.json")
	if res := f.run(Request{Action: ActionPDF, DocumentID: first.ID}); !res.Published {
		t.Fatalf("result = %+v", res)
	}
	var second Document
	docs, _ := f.svc.List(t.Context(), f.cand)
	for _, d := range docs {
		if d.Kind == KindResume && d.Version > first.Version {
			second = d
		}
	}
	after := f.read(second.Path + "/resume.json")
	if second.PDFState != PDFReady || f.read(first.Path+"/resume.json") != before || after == before || second.ContentHash != sha([]byte(after)) {
		t.Fatalf("new version = %+v", second)
	}
	if v, err := f.svc.Verification(t.Context(), second); err != nil || v == nil || v.Stale != "" {
		t.Errorf("verification = %+v, %v", v, err)
	}
	var rec RepairRecord
	must(t, json.Unmarshal([]byte(f.read(second.Path+"/"+repairFile)), &rec))
	if len(rec.Rounds) != 1 {
		t.Errorf("record = %+v", rec)
	}
}

// The PDF of a version whose checks failed can be made again (the drawer's
// "Make the PDF again"): the same text, gated again, becomes a new version
// checked from scratch, and the failed version keeps its PDF and state.
func TestRepair_PDFOfAFailedVersionIsMadeAgainIntoANewVersion(t *testing.T) {
	failing := true
	fb := &fakeBuilder{override: func(fakeCall) map[string]string {
		if failing {
			return map[string]string{toolLayout + "/clipping": toolchain.Fail}
		}
		return nil
	}}
	r := runRepair(t, fb, fixtureOpts{cv: testCV, profile: testProfile})
	failed := r.docs[KindResume]
	if failed.PDFState != PDFFailed {
		t.Fatalf("first PDF = %+v", failed)
	}
	failing = false
	if res := r.f.run(Request{Action: ActionPDF, DocumentID: failed.ID}); !res.Published {
		t.Fatalf("result = %+v", res)
	}
	docs, _ := r.f.svc.List(t.Context(), r.f.cand)
	again := 0
	for _, d := range docs {
		switch {
		case d.ID == failed.ID && (d.PDFState != PDFFailed || d.PDFSHA256 != failed.PDFSHA256):
			t.Errorf("the failed version changed: %+v", d)
		case d.Kind == KindResume && d.Version > failed.Version:
			if again++; d.PDFState != PDFReady || d.ContentHash != failed.ContentHash {
				t.Errorf("new version = %+v", d)
			}
		}
	}
	if again != 1 {
		t.Errorf("%d new résumé versions, want 1", again)
	}
}

// Cutting to exhaustion keeps the letter grounded in what the résumé still
// shows: a letter bullet resting only on removed evidence goes with it, the
// opening's reference is never stripped, and the last letter bullet stays.
func TestRepairResume_LetterClaimsFollowTheCutEvidence(t *testing.T) {
	m := rankManifest(t)
	d := rankResume(m)
	l := rankLetter(m, []string{"E4"}, bulletOf(m, "E3"), bulletOf(m, "E5"))
	v := &version{m: m, resume: &d, letter: l, tight: true, pages: 1}
	rr := RepairRound{Cuts: []Cut{}, Gates: map[Kind]string{}}
	resume, letter := (&job{}).repairResume(v, defect{overflow: true, failed: []string{"verify_pdf max-pages"}, excessPt: 5000}, &rr)
	if !resume || !letter || len(v.letter.Bullets) != 1 || v.letter.Bullets[0].Evidence[0] != "E3" {
		t.Fatalf("resume %v, letter %v, bullets %+v", resume, letter, v.letter.Bullets)
	}
	noted := false
	for _, c := range rr.Cuts {
		noted = noted || strings.Contains(strings.Join(c.Letter, "|"), "was removed")
		if c.Text == bulletOf(m, "E4").Text || c.Text == bulletOf(m, "E3").Text {
			t.Errorf("protected line cut: %+v", c)
		}
	}
	cited := citedIDs(*v.resume)
	for _, claim := range letterClaims(v.letter) {
		backed := false
		for _, id := range claim {
			backed = backed || cited[id] > 0
		}
		if len(claim) > 0 && !backed {
			t.Errorf("letter claim %v is no longer backed by the résumé", claim)
		}
	}
	if !noted || !v.resumeGate.OK() || !v.letterGate.OK() || rr.Gates[KindResume] != "passed" || rr.Gates[KindCover] != "passed" {
		t.Errorf("noted %v, gates: resume %v, letter %v, round %v", noted, v.resumeGate.Errors, v.letterGate.Errors, rr.Gates)
	}
}

// Cancelling during the second round publishes nothing and leaves the earlier
// version exactly as it was.
func TestRepair_CancellationBetweenRoundsLeavesNoNewVersion(t *testing.T) {
	var svc *Service
	runID := make(chan string, 1)
	fb := &fakeBuilder{resumeCap: 331, itemPt: 9}
	fb.hook = func(ctx context.Context, c fakeCall) error {
		if c.Kind == KindResume && c.N == 4 { // the second round's rebuild
			select {
			case id := <-runID:
				svc.Cancel(id)
			case <-time.After(10 * time.Second):
				return errors.New("the test never gave the run id")
			}
		}
		return nil
	}
	f := newFixture(t, fixtureOpts{profile: leanProfile, cv: repairCV(5, 4, 3, 0), builder: func(*toolchain.Toolchain) pdfBuilder { return fb }})
	svc = f.svc
	earlier := f.svc.mustGet(t, f.run(Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, Options: Options{Pages: 2}}).Documents[0])
	count := func() (n int) {
		must(t, f.st.Read().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM documents`).Scan(&n))
		return n
	}
	before := count()
	h, err := f.svc.Submit(t.Context(), Claim{Principal: "test", Key: "key-cancel"}, Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, PDF: true, Options: Options{Pages: 2}})
	must(t, err)
	runID <- h.RunID
	res, err := f.svc.Wait(t.Context(), h.RunID)
	must(t, err)
	if res.Published || res.Reason != ReasonCancelled || count() != before || len(versionDirs(t, f.ws.Root)) != 1 || f.svc.mustGet(t, earlier.ID).PDFState != earlier.PDFState {
		t.Fatalf("result = %+v, rows %d (was %d), directories %v", res, count(), before, versionDirs(t, f.ws.Root))
	}
}
