package apply

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"applysmith/internal/browser"
	"applysmith/internal/documents"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
	"applysmith/internal/ledger"
	"applysmith/internal/tracker"
)

func trackerRig(t *testing.T) (*rig, *tracker.Tracker) {
	t.Helper()
	g := newRig(t, nil)
	tr, err := tracker.New(g.st, tracker.Options{Now: g.r.d.Now})
	if err != nil {
		t.Fatal(err)
	}
	g.r.d.Page.Tracker = tr
	return g, tr
}

func exec1(g *rig, in Input, o Outcome) *Exec {
	run := &Run{ID: "run_x", StartedAt: t0, Input: in, Outcome: o}
	return &Exec{r: g.r, Run: run, Ctx: context.Background()}
}

func TestApply_Record_ConfirmedPromotesTrackerUnconfirmedLeavesIt(t *testing.T) {
	g, tr := trackerRig(t)
	ent, err := tr.Add(context.Background(), tracker.AddRow{Company: "Example Corp", Role: "Platform Engineer", URL: pageURL, Status: tracker.Evaluated})
	if err != nil {
		t.Fatal(err)
	}
	in := Input{URL: pageURL, Caller: gates.CallerCLI}

	un := exec1(g, in, Outcome{Kind: OutcomeSubmitted})
	if d := g.r.trackerEffect(un); !strings.Contains(d, "unchanged") {
		t.Fatalf("detail %q", d)
	}
	if got, _ := tr.Get(context.Background(), ent.ID); got.Entry.Status != tracker.Evaluated {
		t.Fatalf("an unconfirmed submit changed the tracker to %s", got.Entry.Status)
	}

	ok := exec1(g, in, Outcome{Kind: OutcomeSubmitted, Confirmed: true, ConfirmSource: ConfirmATSResponse})
	if d := g.r.trackerEffect(ok); !strings.Contains(d, "applied by the agent") {
		t.Fatalf("detail %q", d)
	}
	got, _ := tr.Get(context.Background(), ent.ID)
	if got.Entry.Status != tracker.Applied || got.Entry.AppliedBy != tracker.ByAgent {
		t.Fatalf("row %+v", got.Entry)
	}
	var actor string
	_ = g.st.Read().QueryRow(`SELECT actor FROM tracker_events WHERE entry_id = ? AND kind = 'status_changed' ORDER BY id DESC LIMIT 1`, ent.ID).Scan(&actor)
	if actor != string(tracker.ActorApplyChain) {
		t.Fatalf("event actor %q", actor)
	}
}

func TestApply_Record_NoTrackerRowCreatedOnlyWhenCompanyAndRoleAreKnown(t *testing.T) {
	g, tr := trackerRig(t)
	conf := Outcome{Kind: OutcomeSubmitted, Confirmed: true, ConfirmSource: ConfirmPage}
	e := exec1(g, Input{URL: "https://careers.example.test/jobs/55", Caller: gates.CallerCLI}, conf)
	if d := g.r.trackerEffect(e); !strings.Contains(d, "not known") {
		t.Fatalf("detail %q", d)
	}
	e = exec1(g, Input{URL: "https://careers.example.test/jobs/56", Caller: gates.CallerCLI, Company: "Sample Inc", Role: "Engineer"}, conf)
	if d := g.r.trackerEffect(e); !strings.Contains(d, "applied by the agent") {
		t.Fatalf("detail %q", d)
	}
	page, err := tr.List(context.Background(), tracker.ListQuery{})
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Status != tracker.Applied {
		t.Fatalf("rows %+v err %v", page, err)
	}
}

func TestApply_Record_TrackerRefusalNeverFailsTheSubmission(t *testing.T) {
	g, tr := trackerRig(t)
	if _, err := tr.Add(context.Background(), tracker.AddRow{Company: "Example Corp", Role: "Platform Engineer", URL: pageURL, Status: tracker.Rejected}); err != nil {
		t.Fatal(err)
	}
	e := exec1(g, Input{URL: pageURL, Caller: gates.CallerCLI}, Outcome{Kind: OutcomeSubmitted, Confirmed: true, ConfirmSource: ConfirmPage})
	if d := g.r.trackerEffect(e); !strings.Contains(d, "not changed") {
		t.Fatalf("detail %q", d)
	}
}

// Owner decision (apply.md, "Skipped postings"): the user's own start of one
// posting may apply to a discarded row the sweep archived, and the submission
// it records takes the row out of the archive. A list row never does, and a run
// that stops before Submit leaves the archive as it is.
func TestApply_Record_OnlyTheUsersOwnSubmissionTakesARowOutOfTheArchive(t *testing.T) {
	g, tr := trackerRig(t)
	ctx := context.Background()
	ent, err := tr.Add(ctx, tracker.AddRow{Company: "Example Corp", Role: "Platform Engineer", URL: pageURL, Status: tracker.Discarded})
	if err == nil {
		_, err = tr.ArchiveRow(ctx, ent.ID, tracker.ActorUser)
	}
	if err != nil {
		t.Fatal(err)
	}
	single := Input{URL: pageURL, Caller: gates.CallerDashboard, Tool: ToolAutofill, Submit: true, ExplicitRequest: true, UserStarted: true}
	list := single
	list.Tool, list.BatchID = ToolFromList, "list_x"
	sent := Outcome{Kind: OutcomeSubmitted, Confirmed: true, ConfirmSource: ConfirmPage}
	for i, e := range []*Exec{exec1(g, single, Outcome{Kind: OutcomeStopped}), exec1(g, list, sent), exec1(g, single, sent)} {
		e.Run.ID = "run_" + strconv.Itoa(i)
		g.r.runRecord(e)
		got, err := tr.Get(ctx, ent.ID)
		if err != nil {
			t.Fatal(err)
		}
		if last := i == 2; (got.Entry.Status == tracker.Applied) != last || (got.Entry.ArchivedAt == "") != last {
			t.Fatalf("after run %d: %s, archived %q", i, got.Entry.Status, got.Entry.ArchivedAt)
		}
	}
}

func TestApply_Record_UnconfirmedSubmitWritesLedgerRowAndBlocksALaterRun(t *testing.T) {
	g := newRig(t, nil)
	ctx := context.Background()
	in := Input{URL: pageURL, Caller: gates.CallerCLI, Submit: true, ExplicitRequest: true}
	e := exec1(g, in, Outcome{Kind: outcomeRunning})
	e.Run.Identity = "x"
	cl, err := g.ledger.Claim(ctx, e.claimRequest())
	if err != nil {
		t.Fatal(err)
	}
	if st := g.r.settle(e, cl.ID, ledger.Verdict{Landed: true, PressDispatched: true}); st != gates.StateSubmitted {
		t.Fatalf("state %s", st)
	}
	if g.count(`SELECT count(*) FROM submissions WHERE confirmed = 0 AND state = 'submitted' AND source = 'apply-chain'`) != 1 {
		t.Fatal("no unconfirmed submission row")
	}
	_, gerr := g.ledger.Guard(ctx, e.claimRequest())
	if d, ok := ledger.DecisionOf(gerr); !ok || d.Code != gates.CodeUnconfirmedSubmission {
		t.Fatalf("guard: %v", gerr)
	}
}

type fakeDocs struct {
	mu       sync.Mutex
	list     []documents.Document
	verify   map[int64]*documents.Verification
	frozen   []int64
	freezeAt int64
	err      error
}

func (f *fakeDocs) List(context.Context, int64) ([]documents.Document, error) { return f.list, f.err }
func (f *fakeDocs) Verification(_ context.Context, d documents.Document) (*documents.Verification, error) {
	return f.verify[d.ID], nil
}
func (f *fakeDocs) Freeze(_ context.Context, c int64, ids ...int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.freezeAt = c
	f.frozen = append(f.frozen, ids...)
	return f.err
}
func (f *fakeDocs) ReadPDF(context.Context, documents.Document) ([]byte, error) {
	return []byte("%PDF-1.4 fixture"), nil
}

const fixtureLetter = "Dear Hiring Manager,\n\nI build reliable platforms.\n\nThank you."

func (f *fakeDocs) Source(context.Context, int64) (documents.Source, error) {
	return documents.Source{Text: fixtureLetter}, nil
}

func TestApply_Documents_AttachOnlyAVerifiedFreshVersionAndFreezeExactlyThose(t *testing.T) {
	good := documents.Document{ID: 7, Kind: documents.KindResume, Ready: true, PDFPath: "r/7.pdf"}
	stale := documents.Document{ID: 9, Kind: documents.KindResume, Ready: true, PDFPath: "r/9.pdf"}
	fd := &fakeDocs{list: []documents.Document{stale, good}, verify: map[int64]*documents.Verification{
		9: {State: documents.PDFReady, Stale: "the PDF is not the one the verdict describes"},
		7: {State: documents.PDFReady},
	}}
	g := newRig(t, func(d *Deps) { d.Page.Documents = fd })
	e := exec1(g, Input{URL: pageURL, Caller: gates.CallerCLI}, Outcome{Kind: OutcomeSubmitted})
	e.Run.Report = &ReportBinding{Number: 3, TrackerID: 21}
	dr := newDocResumes(g.r, e)
	pl, err := dr.Resume(context.Background(), engine.ResumeRequest{RunID: "r", Document: "resume"})
	if err != nil || pl == nil {
		t.Fatalf("payload %v err %v", pl, err)
	}
	if ids := dr.attachedIDs(); len(ids) != 1 || ids[0] != 7 {
		t.Fatalf("attached %v: a stored ready row is not enough, a stale verdict must not attach", ids)
	}
	if cover, _ := dr.Resume(context.Background(), engine.ResumeRequest{Document: "cover-letter"}); cover != nil {
		t.Fatal("a cover letter was served though none exists")
	}
	s := &session{r: g.r, e: e, docs: dr}
	g.r.freeze(context.Background(), e, s)
	if len(fd.frozen) != 1 || fd.frozen[0] != 7 || fd.freezeAt != 21 {
		t.Fatalf("frozen %v for candidacy %d", fd.frozen, fd.freezeAt)
	}
	// Nothing verified: nothing attached and nothing frozen.
	fd2 := &fakeDocs{list: []documents.Document{stale}, verify: map[int64]*documents.Verification{9: {State: documents.PDFUnverified}}}
	g.r.d.Page.Documents = fd2
	dr2 := newDocResumes(g.r, e)
	if pl, _ := dr2.Resume(context.Background(), engine.ResumeRequest{Document: "resume"}); pl != nil || len(dr2.attachedIDs()) != 0 {
		t.Fatal("an unverified version was attached")
	}
	g.r.freeze(context.Background(), e, &session{r: g.r, e: e, docs: dr2})
	if len(fd2.frozen) != 0 {
		t.Fatal("froze with nothing attached")
	}
}

func TestApply_Documents_NoCandidacyMeansNothingFrozen(t *testing.T) {
	fd := &fakeDocs{list: []documents.Document{{ID: 1, Kind: documents.KindResume, Ready: true, PDFPath: "p"}}, verify: map[int64]*documents.Verification{1: {State: documents.PDFReady}}}
	g := newRig(t, func(d *Deps) { d.Page.Documents = fd })
	e := exec1(g, Input{URL: pageURL}, Outcome{})
	dr := newDocResumes(g.r, e)
	dr.attached = []documents.Document{{ID: 1, Kind: documents.KindResume}}
	g.r.freeze(context.Background(), e, &session{r: g.r, e: e, docs: dr})
	if len(fd.frozen) != 0 || len(e.Run.Warnings) == 0 {
		t.Fatalf("frozen %v warnings %v", fd.frozen, e.Run.Warnings)
	}
}

// A transcript slot gets the Answer Bank's transcript and nothing else: with
// none stored it gets no document, though a verified résumé and a roster
// résumé are there to attach, and a served transcript freezes no version.
func TestApply_Documents_ATranscriptSlotGetsOnlyTheStoredTranscript(t *testing.T) {
	transcript := &engine.ResumePayload{FileName: "unofficial-transcript.pdf", MediaType: "application/pdf", ContentBase64: "JVBERi0xLjQ="}
	var stored *engine.ResumePayload
	fd := &fakeDocs{list: []documents.Document{{ID: 7, Kind: documents.KindResume, Ready: true, PDFPath: "r/7.pdf"}}, verify: map[int64]*documents.Verification{7: {State: documents.PDFReady}}}
	roster := func(context.Context, *ReportBinding) (*engine.ResumePayload, string, error) {
		return &engine.ResumePayload{FileName: "backend.pdf", MediaType: "application/pdf", ContentBase64: "JVBERi0xLjQ="}, "the default", nil
	}
	g := newRig(t, func(d *Deps) {
		d.Page.Documents, d.Page.ProfileResume = fd, roster
		d.Page.Transcript = func(context.Context) (*engine.ResumePayload, error) { return stored, nil }
	})
	e := exec1(g, Input{URL: pageURL, Caller: gates.CallerCLI}, Outcome{})
	e.Run.Report = &ReportBinding{Number: 3, TrackerID: 21}
	dr := newDocResumes(g.r, e)
	ctx := context.Background()
	if pl, err := dr.Resume(ctx, engine.ResumeRequest{Document: "transcript"}); err != nil || pl != nil {
		t.Fatalf("with no transcript stored the slot got %+v %v", pl, err)
	}
	stored = transcript
	if pl, err := dr.Resume(ctx, engine.ResumeRequest{Document: "transcript"}); err != nil || pl != transcript {
		t.Fatalf("payload %+v err %v", pl, err)
	}
	if ids := dr.attachedIDs(); len(ids) != 0 || dr.resumeLine() != "" {
		t.Fatalf("the transcript counted as a document version %v or a résumé", ids)
	}
}

// resumeFill is a page whose fill asks the résumé binding for its upload, as
// the engine does.
type resumeFill struct {
	*fakePage
	docs *docResumes
}

func (p resumeFill) Fill(ctx context.Context, o engine.FillOptions) (*engine.PageResult, error) {
	if _, err := p.docs.Resume(ctx, engine.ResumeRequest{Document: "resume"}); err != nil {
		return nil, err
	}
	return p.fakePage.Fill(ctx, o)
}

type fakePoster struct {
	mu     sync.Mutex
	limit  int
	got    [][]LearnEvent
	calls  int
	reject map[string]bool
}

func (p *fakePoster) PostEvents(_ context.Context, ev []LearnEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	for _, e := range ev {
		if p.reject[e.EventID] {
			return ErrTooLarge
		}
	}
	if len(ev) > p.limit {
		return ErrTooLarge
	}
	p.got = append(p.got, ev)
	return nil
}

func evs(n int) []LearnEvent {
	var out []LearnEvent
	for i := 0; i < n; i++ {
		out = append(out, LearnEvent{LearningEvent: engine.LearningEvent{EventID: strings.Repeat("e", 1) + string(rune('a'+i%26)) + string(rune('a'+i/26))}, RunID: "r"})
	}
	return out
}

func TestApply_Learn_ProvenanceNotCorrectionBatchSplitting(t *testing.T) {
	p := &fakePoster{limit: 4}
	posted, failed, err := PostBatches(context.Background(), p, evs(10))
	if err != nil || posted != 10 || len(failed) != 0 {
		t.Fatalf("posted %d failed %d err %v", posted, len(failed), err)
	}
	for _, b := range p.got {
		if len(b) > 4 {
			t.Fatalf("a batch of %d went through", len(b))
		}
	}
	// A single event the receiver always rejects fails alone.
	bad := evs(3)
	p2 := &fakePoster{limit: 100, reject: map[string]bool{bad[1].EventID: true}}
	posted, failed, err = PostBatches(context.Background(), p2, bad)
	if err != nil || posted != 2 || len(failed) != 1 || failed[0].EventID != bad[1].EventID {
		t.Fatalf("posted %d failed %v err %v", posted, failed, err)
	}
	// Batches are at most 200.
	p3 := &fakePoster{limit: 1000}
	if _, _, err := PostBatches(context.Background(), p3, evs(450)); err != nil || len(p3.got) != 3 || len(p3.got[0]) != 200 {
		t.Fatalf("batches %d", len(p3.got))
	}
}

// loggingPage is a fake page that also reads its engine log, by document.
type loggingPage struct {
	*fakePage
	lines  []string
	frames map[string][]string
	read   []string // the documents whose log was read
}

func (p *loggingPage) LogIn(_ context.Context, frame string) ([]string, error) {
	p.read = append(p.read, frame)
	if frame != "" {
		return p.frames[frame], nil
	}
	return p.lines, nil
}

// NXP: a catalog search that found no option is diagnosed from the labels the
// engine's log of that page names; a page where every writer committed keeps
// no engine log.
func TestApply_Record_AWriterMissKeepsThePagesEngineLog(t *testing.T) {
	fl := &fillLog{}
	inner := &loggingPage{fakePage: &fakePage{fills: []*engine.PageResult{
		doneResult(engine.Row{Selector: "#major", Label: "Field of Study", Kind: "custom-listbox", Outcome: "abstained", Reason: "option-not-found"}),
		doneResult(engine.Row{Selector: "#city", Label: "City", Kind: "text", Outcome: "abstained", Reason: "no-answer"}),
	}}, lines: []string{"120 prompt-search selector=#major reason=option-not-found detail=stage=search labels=Mathematics-Statistics"}}
	page := &loggedFiller{Filler: inner, log: fl}
	for range 2 {
		if _, err := page.Fill(context.Background(), engine.FillOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	var engineLines []fillLine
	for _, l := range fl.lines {
		if l.Event == "engine" {
			engineLines = append(engineLines, l)
		}
	}
	if len(engineLines) != 1 || engineLines[0].Page != 1 || !strings.Contains(engineLines[0].Reason, "labels=Mathematics-Statistics") {
		t.Fatalf("engine lines %+v: only the page whose writer missed keeps its log", engineLines)
	}
}

// MongoDB: the Greenhouse frame's location search reported filled, then the
// check found it empty. No writer missed, so no engine line was kept; the stop
// snapshot reads every frame the fills reached, once each, under its name.
func TestApply_Record_TheStopSnapshotReadsEveryFilledFramesEngineLog(t *testing.T) {
	fl := &fillLog{}
	framed := func() *engine.PageResult {
		res := doneResult()
		res.Frames = []engine.FrameResult{
			{Frame: browser.Frame{ID: "GH"}, Status: &engine.Status{State: engine.StateDone, Summary: &engine.Summary{Rows: []engine.Row{
				{Selector: "#candidate-location", Label: "Location (City)", Kind: "custom-listbox", Outcome: "filled"},
			}}}},
			{Frame: browser.Frame{ID: "ads"}},
		}
		return res
	}
	inner := &loggingPage{fakePage: &fakePage{fills: []*engine.PageResult{framed(), framed()}}, lines: []string{"top line"},
		frames: map[string][]string{"GH": {"300 geo-pick selector=#candidate-location"}}}
	page := &loggedFiller{Filler: inner, log: fl}
	for range 2 {
		if _, err := page.Fill(context.Background(), engine.FillOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if len(inner.read) != 0 {
		t.Fatalf("a fill with no miss read logs %v", inner.read)
	}
	got := page.frameLogs(context.Background())
	if !slices.Equal(got, []string{"== frame GH", "300 geo-pick selector=#candidate-location"}) || !slices.Equal(inner.read, []string{"GH"}) {
		t.Fatalf("frame logs %q, read %v", got, inner.read)
	}
}

func TestApply_Record_StopSnapshotCarriesFillLog(t *testing.T) {
	dir := t.TempDir()
	g := newRig(t, func(d *Deps) { d.Page.SnapshotDir = dir })
	tab := newScriptTab(appPage())
	fl := &fillLog{}
	inner := &fakePage{fills: []*engine.PageResult{doneResult(
		engine.Row{Selector: "#first", Label: "First Name", Kind: "text", Outcome: "filled", Value: "Alex"},
		engine.Row{Selector: "#city", Label: "City", Kind: "text", Outcome: "abstained", Reason: "no-answer"},
	)}}
	page := &loggedFiller{Filler: inner, log: fl}
	run := &Run{ID: "run_log", Input: Input{URL: pageURL}}
	e := &Exec{r: g.r, Run: run, Ctx: context.Background()}
	s := &session{r: g.r, e: e, tab: tab, page: page, fl: fl}
	ctx := context.Background()
	if _, err := page.Fill(ctx, engine.FillOptions{}); err != nil {
		t.Fatal(err)
	}
	// A refill pass: one Answer call is one pass, and each write keeps its state.
	if _, err := page.Answer(ctx, "er1", []engine.Resolution{{Selector: "#city", Outcome: "filled", Value: "Springfield"}}); err != nil {
		t.Fatal(err)
	}
	s.snapshot(ctx, "stop")
	raw, err := os.ReadFile(filepath.Join(dir, "run_log-stop.fill-log.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var got []fillLine
	for _, ln := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var l fillLine
		if err := json.Unmarshal([]byte(ln), &l); err != nil {
			t.Fatalf("%q: %v", ln, err)
		}
		got = append(got, l)
	}
	want := []fillLine{
		{Event: "fill", Page: 1, Outcome: engine.StateDone},
		{Event: "row", Page: 1, Selector: "#first", Label: "First Name", Kind: "text", Outcome: "filled"},
		{Event: "row", Page: 1, Selector: "#city", Label: "City", Kind: "text", Outcome: "abstained", Reason: "no-answer"},
		{Event: "write", Page: 1, Pass: 1, Selector: "#city", Outcome: "filled"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fill log\n got %+v\nwant %+v", got, want)
	}
	if strings.Contains(string(raw), "Alex") || strings.Contains(string(raw), "Springfield") {
		t.Fatal("the fill log holds an answer value")
	}
	if fi, err := os.Stat(filepath.Join(dir, "run_log-stop.fill-log.jsonl")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", fi, err)
	}
	if run.Snapshot != filepath.Join(dir, "run_log-stop.html") {
		t.Fatalf("the run does not name its stop snapshot: %q", run.Snapshot)
	}
}

func TestApply_Learn_LaneByFailureClass(t *testing.T) {
	for _, c := range []struct {
		class Class
		lane  Lane
		note  string
	}{
		{ClassCredential, LaneUserAction, "lane user-action"},
		{ClassAnswerGap, LaneCandidate, "lane answer-bank-candidate"},
		{ClassDefect, LaneWorkItem, "snapshot /snap/run_x-stop.html"},
		{ClassEnvironment, LaneNone, "no learning lane"},
		{ClassUnavailable, LaneNone, "no learning lane"},
		{ClassPosting, LaneNone, "no learning lane"},
	} {
		t.Run(string(c.class), func(t *testing.T) {
			g := newRig(t, nil)
			e := exec1(g, Input{URL: pageURL}, Outcome{Kind: OutcomeStopped, Stage: StageFill, Class: c.class})
			e.Run.Snapshot = "/snap/run_x-stop.html"
			g.r.runLearn(e)
			line := stageOf(e.Run, StageLearn)
			if e.Run.LearningLane != c.lane || line.Status != StatusOK || !strings.Contains(line.Detail, c.note) {
				t.Fatalf("lane %q, stage %+v", e.Run.LearningLane, line)
			}
			if c.class != ClassDefect && strings.Contains(line.Detail, "/snap/") {
				t.Fatalf("only a defect's work item names the snapshot: %q", line.Detail)
			}
		})
	}
	g := newRig(t, nil)
	e := exec1(g, Input{URL: pageURL}, Outcome{Kind: OutcomeSubmitted})
	g.r.runLearn(e)
	if e.Run.LearningLane != LaneNone || strings.Contains(stageOf(e.Run, StageLearn).Detail, "lane") {
		t.Fatalf("a run that did not stop has no lane: %+v", stageOf(e.Run, StageLearn))
	}
}

// fakeLetters records what a run asked for, in order, and on Write makes the
// candidacy's letter ready in docs.
type fakeLetters struct {
	mu       sync.Mutex
	calls    []string
	report   *ReportBinding
	evalErr  error
	writeErr error
	docs     *fakeDocs
}

func (f *fakeLetters) Evaluate(_ context.Context, postingURL string) (*ReportBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "evaluate "+postingURL)
	return f.report, f.evalErr
}

func (f *fakeLetters) Write(_ context.Context, candidacy int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "write "+strconv.FormatInt(candidacy, 10))
	if f.writeErr != nil {
		return f.writeErr
	}
	if f.docs != nil {
		f.docs.mu.Lock()
		f.docs.list = append(f.docs.list, documents.Document{ID: 50, Kind: documents.KindCover, Ready: true, PDFPath: "c/50.pdf"})
		f.docs.verify[50] = &documents.Verification{State: documents.PDFReady}
		f.docs.mu.Unlock()
	}
	return nil
}

func (f *fakeLetters) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func coverPage(label string) *PageState {
	return &PageState{Inputs: []InputInfo{
		{Kind: "text", Type: "text", Label: "First name"},
		{Kind: "file", Type: "file", Label: "Resume/CV *"},
		{Kind: "file", Type: "file", Label: label, Required: true},
	}}
}

func TestApply_CoverLetter_PreparationOrderAndRefusals(t *testing.T) {
	full := &ReportBinding{Number: 12, Kind: ReportFinal, TrackerID: 21, Company: "Example Corp", Role: "Platform Engineer"}
	run := func(t *testing.T, report *ReportBinding, fl *fakeLetters, fd *fakeDocs, page *PageState) (string, bool, *Exec) {
		t.Helper()
		g := newRig(t, func(d *Deps) { d.Page.Documents, d.Page.Letters = fd, fl })
		e := exec1(g, Input{URL: pageURL, Caller: gates.CallerCLI}, Outcome{})
		e.Run.Report = report
		s := &session{r: g.r, e: e}
		note, made := s.prepareLetter(context.Background(), page)
		if again, _ := s.prepareLetter(context.Background(), page); again != "" {
			t.Fatalf("a second page tried again: %q", again)
		}
		return note, made, e
	}
	docs := func() *fakeDocs { return &fakeDocs{verify: map[int64]*documents.Verification{}} }

	t.Run("a full report on file: the letter is written from it, nothing is evaluated", func(t *testing.T) {
		fl := &fakeLetters{}
		note, made, _ := run(t, full, fl, docs(), coverPage("Cover Letter *"))
		if !made || strings.Join(fl.log(), ",") != "write 21" || !strings.Contains(note, "written from report #012") {
			t.Fatalf("note %q made %v calls %v", note, made, fl.log())
		}
	})
	t.Run("a ready letter on file: nothing is asked for", func(t *testing.T) {
		fd := docs()
		fd.list = []documents.Document{{ID: 4, Kind: documents.KindCover, Ready: true, PDFPath: "c/4.pdf"}}
		fl := &fakeLetters{}
		if note, _, _ := run(t, full, fl, fd, coverPage("Cover Letter")); note != "" || len(fl.log()) != 0 {
			t.Fatalf("note %q calls %v", note, fl.log())
		}
	})
	t.Run("only a quick screen on file: the posting is evaluated in full first, then the letter is written", func(t *testing.T) {
		fl := &fakeLetters{report: full}
		note, made, e := run(t, &ReportBinding{Number: 3, Kind: "screening", TrackerID: 21}, fl, docs(), coverPage("Cover Letter *"))
		if !made || strings.Join(fl.log(), ",") != "evaluate "+pageURL+",write 21" || !strings.Contains(note, "evaluated first (report #012)") {
			t.Fatalf("note %q calls %v", note, fl.log())
		}
		if e.Run.Report != full {
			t.Fatal("the run is not bound to the new full report")
		}
	})
	t.Run("an evaluation that gives only a screen leaves the slot, says which report, and writes nothing", func(t *testing.T) {
		fl := &fakeLetters{report: &ReportBinding{Number: 8, Kind: "screening", TrackerID: 21}}
		note, made, _ := run(t, nil, fl, docs(), coverPage("Cover Letter *"))
		if made || !strings.Contains(note, "only a quick screen (report #008)") || strings.Join(fl.log(), ",") != "evaluate "+pageURL {
			t.Fatalf("note %q calls %v", note, fl.log())
		}
	})
	t.Run("a failed or timed-out evaluation is named, and the letter is not written", func(t *testing.T) {
		fl := &fakeLetters{evalErr: errors.New("the posting text is 40 characters")}
		if note, made, _ := run(t, nil, fl, docs(), coverPage("Cover Letter")); made || !strings.Contains(note, "evaluation of this posting failed: the posting text is 40 characters") {
			t.Fatalf("note %q", note)
		}
		fl = &fakeLetters{evalErr: context.DeadlineExceeded}
		if note, _, _ := run(t, nil, fl, docs(), coverPage("Cover Letter")); !strings.Contains(note, "still running after 300 s") {
			t.Fatalf("note %q", note)
		}
	})
	t.Run("a letter the writer refuses is named", func(t *testing.T) {
		fl := &fakeLetters{writeErr: errors.New("the score is below the floor")}
		if note, made, _ := run(t, full, fl, docs(), coverPage("Cover Letter")); made || !strings.Contains(note, "could not be written: the score is below the floor") {
			t.Fatalf("note %q", note)
		}
	})
	t.Run("Greenhouse's slot labelled only Attach is the letter by its own id", func(t *testing.T) {
		for id, want := range map[string]bool{"cover_letter": true, "resume": false, "": false} {
			fl := &fakeLetters{}
			page := &PageState{Inputs: []InputInfo{{Kind: "file", Type: "file", Label: "Attach", ID: id, Required: true}}}
			if _, made, _ := run(t, full, fl, docs(), page); made != want {
				t.Fatalf("id %q: letter written %v, calls %v", id, made, fl.log())
			}
		}
	})
	t.Run("no cover-letter slot, or one that also names the résumé, asks for nothing", func(t *testing.T) {
		for _, label := range []string{"Portfolio", "Resume/Cover Letter"} {
			fl := &fakeLetters{report: full}
			if note, _, _ := run(t, nil, fl, docs(), coverPage(label)); note != "" || len(fl.log()) != 0 {
				t.Fatalf("%q: note %q calls %v", label, note, fl.log())
			}
		}
	})
	t.Run("a text box labelled as the letter asks for one; a question about a letter does not", func(t *testing.T) {
		for label, want := range map[string]bool{"Cover letter*Required": true, "Paste your cover letter here": true, "Motivation letter (optional)": true,
			"Why do you want to work here?": false, "Did you submit a cover letter?": false, "Anything to add to your cover letter?": false, "Resume/Cover letter": false} {
			fl := &fakeLetters{}
			if _, made, _ := run(t, full, fl, docs(), &PageState{Inputs: []InputInfo{{Kind: "textarea", Label: label}}}); made != want {
				t.Fatalf("%q: letter written %v, calls %v", label, made, fl.log())
			}
		}
	})
}

// A letter box gets the text of the version a slot would get, as attached;
// never a stale one, and never one longer than the box.
func TestApply_CoverLetter_TextIsTheAttachableLetterWithinTheBoxLimit(t *testing.T) {
	cover := documents.Document{ID: 50, Kind: documents.KindCover, Ready: true, PDFPath: "c"}
	stale := documents.Document{ID: 51, Kind: documents.KindCover, Ready: true, PDFPath: "c"}
	for i, c := range []struct {
		max  int
		list []documents.Document
		want string
	}{{0, []documents.Document{stale, cover}, fixtureLetter}, {len(fixtureLetter) - 1, []documents.Document{cover}, ""}, {0, []documents.Document{stale}, ""}} {
		g := newRig(t, func(d *Deps) {
			d.Page.Documents = &fakeDocs{list: c.list, verify: map[int64]*documents.Verification{50: {State: documents.PDFReady}, 51: {State: documents.PDFReady, Stale: "x"}}}
		})
		e := exec1(g, Input{URL: pageURL, Caller: gates.CallerCLI}, Outcome{})
		e.Run.Report = &ReportBinding{Number: 3, TrackerID: 21}
		dr := newDocResumes(g.r, e)
		pl, _ := dr.Resume(context.Background(), engine.ResumeRequest{Document: "cover-letter", Format: "text", MaxLength: c.max})
		var got []byte
		if pl != nil {
			got, _ = base64.StdEncoding.DecodeString(pl.ContentBase64)
		}
		if string(got) != c.want || dr.observed(documents.KindCover) != (c.want != "") || (i == 1) != strings.Contains(strings.Join(dr.skips(), ""), "is 61 characters and the page's box takes at most 60") {
			t.Fatalf("case %d: text %q, warnings %v", i, got, dr.skips())
		}
	}
}

// A posting named only by its query (ADP's jobId) still reaches the matcher
// with the location its tracker row holds: the match call carries the address
// without the query, so the run reads the row by its whole address.
func TestApply_Fill_PostingLocationComesFromTheTrackerRowOfTheWholeAddress(t *testing.T) {
	g, tr := trackerRig(t)
	const adpURL = "https://workforcenow.adp.com/mascsr/default/mdf/recruitment/recruitment.html?cid=c0ffee&jobId=598183"
	if _, err := tr.Add(context.Background(), tracker.AddRow{Company: "Example Corp", Role: "Platform Engineer", URL: adpURL, Location: "Durham, NC, US"}); err != nil {
		t.Fatal(err)
	}
	s := &session{r: g.r, e: exec1(g, Input{URL: adpURL, Caller: gates.CallerCLI}, Outcome{})}
	if got := s.fillOptions(nil).Posting; got.Location != "Durham, NC, US" || strings.Contains(got.URL, "?") {
		t.Fatalf("posting %+v", got)
	}
	other := &session{r: g.r, e: exec1(g, Input{URL: "https://workforcenow.adp.com/mascsr/default/mdf/recruitment/recruitment.html?cid=c0ffee&jobId=1", Caller: gates.CallerCLI}, Outcome{})}
	if got := other.fillOptions(nil).Posting.Location; got != "" {
		t.Fatalf("another posting of the site took the location %q", got)
	}
}

// P&G's Workday names no employer on its pages: a run given only the address
// takes the company the posting's tracker row names, for the fill and for the
// matcher ("worked for this organization"); a named company still wins.
func TestApply_Fill_PostingCompanyComesFromTheTrackerRow(t *testing.T) {
	g, tr := trackerRig(t)
	const pgURL = "https://pg.wd5.myworkdayjobs.com/1000/job/CINCINNATI-GENERAL-OFFICES/Data---AI-Engineering--2027-Grads-_R000157913"
	if _, err := tr.Add(context.Background(), tracker.AddRow{Company: "Procter & Gamble", Role: "Data & AI Engineering", URL: pgURL, Location: "Cincinnati, OH"}); err != nil {
		t.Fatal(err)
	}
	s := &session{r: g.r, e: exec1(g, Input{URL: pgURL, Caller: gates.CallerCLI}, Outcome{})}
	s.e.Run.ATS, s.e.Run.Tenant = ATSWorkday, "pg"
	if got := s.fillOptions(nil).Posting.Company; got != "Procter & Gamble" {
		t.Fatalf("fill company %q", got)
	}
	if got := s.matchRequest("run_x", nil).Posting.Company; got != "Procter & Gamble" {
		t.Fatalf("match company %q", got)
	}
	named := &session{r: g.r, e: exec1(g, Input{URL: pgURL, Caller: gates.CallerCLI, Company: "P&G"}, Outcome{})}
	if got := named.fillOptions(nil).Posting.Company; got != "P&G" {
		t.Fatalf("a named company wins: %q", got)
	}
}

// A run with no full report that meets a cover-letter slot is evaluated first,
// and the evaluation makes the posting's tracker row, with its place, before
// that page's fill: the page and every step after it are placed by the row.
func TestApply_Fill_TheTrackerRowIsReadAgainOnEachPage(t *testing.T) {
	a := newAcct(t, Input{URL: pageURL, Caller: gates.CallerCLI}, ATSGeneric, "careers.example.test", nil)
	tr, err := tracker.New(a.g.st, tracker.Options{Now: a.g.r.d.Now})
	if err != nil {
		t.Fatal(err)
	}
	a.g.r.d.Page.Tracker = tr
	a.s.page, a.s.tab = &fakePage{}, a.tab
	a.tab.set(appPage())
	_ = a.s.pageWork(context.Background(), &walker{r: a.g.r, e: a.e, s: a.s}, appPage())
	if _, err := tr.Add(context.Background(), tracker.AddRow{Company: "Northwind Example", Role: "Platform Engineer", URL: pageURL, Location: "Toronto, ON, Canada"}); err != nil {
		t.Fatal(err)
	}
	_ = a.s.pageWork(context.Background(), &walker{r: a.g.r, e: a.e, s: a.s}, appPage())
	if got := a.s.postingLoc(); got != "Toronto, ON, Canada" {
		t.Fatalf("the second page is placed at %q", got)
	}
}

func TestApply_Fill_PostingCompanyOfADirectBoardAddressIsTheBoardsEmployer(t *testing.T) {
	g, _ := trackerRig(t)
	s := &session{r: g.r, e: exec1(g, Input{URL: "https://job-boards.greenhouse.io/affirm/jobs/7799631003", Caller: gates.CallerCLI}, Outcome{})}
	s.e.Run.ATS, s.e.Run.Tenant = ATSGreenhouse, "affirm"
	if got := s.fillOptions(nil).Posting.Company; got != "affirm" {
		t.Fatalf("company %q", got)
	}
	s.e.Run.Input.Company = "Affirm Holdings"
	if got := s.fillOptions(nil).Posting.Company; got != "Affirm Holdings" {
		t.Fatalf("a named company wins: %q", got)
	}
	s.e.Run.Input.Company, s.e.Run.ATS, s.e.Run.Tenant = "", ATSWorkday, "affirm"
	if got := s.fillOptions(nil).Posting.Company; got != "" {
		t.Fatalf("a Workday tenant is not a company name: %q", got)
	}
	if got := boardCompany(ATSAshby, "1password-eu"); got != "1password eu" {
		t.Fatalf("slug words: %q", got)
	}
	for u, want := range map[string]string{"https://apply.workable.com/exampleco/j/1A2B3C/": "exampleco", "https://apply.workable.com/j/1A2B3C4D5E/apply": ""} {
		if got := tenantSlug(u); got != want {
			t.Errorf("tenant slug of %s: %q, want %q", u, got, want)
		}
	}
}

// TixTrack on Teamtailor from a bare address: the employer is what the page
// states, else the tenant's slug; a named company still wins.
func TestApply_Fill_PostingCompanyOfABareAddressIsWhatThePageStates(t *testing.T) {
	for _, c := range []struct{ stated, named, want string }{{"TixTrack", "", "TixTrack"}, {"", "", "tixtrack"}, {"TixTrack", "TixTrack Inc", "TixTrack Inc"}} {
		w := newWalk(t, Input{URL: "https://tixtrack.teamtailor.com/jobs/8514114-software-engineer", Caller: gates.CallerCLI, Company: c.named}, wizardStep(1, 1))
		w.tab.company = c.stated
		if _, stp := w.walk(); stp != nil || w.s.fillOptions(nil).Posting.Company != c.want {
			t.Fatalf("%+v: stop %+v company %q", c, stp, w.s.fillOptions(nil).Posting.Company)
		}
	}
}

// Northwood Space on Ashby from a bare address: the board's slug squashes the
// name ("northwoodspace"), so "sponsorship from Northwood" named no employer the
// matcher knew; the name the page's JobPosting data states comes first, and the
// slug stands in when the page states none.
func TestApply_Fill_PostingCompanyOnABoardIsThePagesJobPostingName(t *testing.T) {
	g, _ := trackerRig(t)
	for _, c := range []struct{ stated, want string }{{"Northwood Space", "Northwood Space"}, {"", "northwoodspace"}} {
		s := &session{r: g.r, e: exec1(g, Input{URL: "https://jobs.ashbyhq.com/northwoodspace/ad3d19b2-114e-4096-810e-64f289c862db/application", Caller: gates.CallerCLI}, Outcome{})}
		s.e.Run.ATS, s.e.Run.Tenant = ATSAshby, "northwoodspace"
		s.e.Run.Stated = Stated{Company: c.stated}
		if got := s.statedCompany(); got != c.want {
			t.Errorf("stated %q: company %q, want %q", c.stated, got, c.want)
		}
	}
}

// A Lever /apply or Ashby /application page holds only the form, so the
// posting's words come from its board, read once; a Greenhouse page shows the
// posting and keeps its own text, with the board's location, as do the generic
// SmartRecruiters and Workable pages, whose addresses name their board; a
// board that cannot be read leaves the page's text and says so.
func TestApply_Fill_PostingTextOfAFormOnlyPageComesFromItsBoard(t *testing.T) {
	g, _ := trackerRig(t)
	var calls []string
	fail := false
	boardLoc := "Austin, TX"
	g.r.d.Page.Posting = func(_ context.Context, u string) (PostingRead, error) {
		calls = append(calls, u)
		if fail {
			return PostingRead{}, errors.New("the ATS API answered 404")
		}
		return PostingRead{Title: "Platform Engineer", Location: boardLoc, Text: "Run the platform.\nWhat we require\n- Five years of Go"}, nil
	}
	form := &PageState{Text: "Platform Engineer Submit your application Full name Email"}
	session := func(url, ats string) *session {
		s := &session{r: g.r, e: exec1(g, Input{URL: url, Caller: gates.CallerCLI}, Outcome{})}
		s.e.Run.ATS = ats
		return s
	}
	const lever = "https://jobs.lever.co/exampleco/0a1b2c3d-1111-2222-3333-444455556666/apply"
	s := session(lever, ATSLever)
	text, note := s.postingText(context.Background(), form, ATSLever)
	if text != "Platform Engineer\nLocation: Austin, TX\n\nRun the platform.\nWhat we require\n- Five years of Go" || !strings.Contains(note, "read from its board") {
		t.Fatalf("lever: %q (%s)", text, note)
	}
	if got := s.fillOptions(form).Posting.Location; got != "Austin, TX" || s.postingLoc() != "Austin, TX" {
		t.Fatalf("the board's location reaches the matcher: %q / %q", got, s.postingLoc())
	}
	if again, note := s.postingText(context.Background(), form, ATSLever); again != form.Text || note != "" || len(calls) != 1 || calls[0] != lever {
		t.Fatalf("one request per run: %v (%q, %q)", calls, again, note)
	}

	gh := session("https://job-boards.greenhouse.io/exampleco/jobs/4012345", ATSGreenhouse)
	page := &PageState{Text: "Platform Engineer San Francisco, CA We build things. First Name"}
	if text, note := gh.postingText(context.Background(), page, ATSGreenhouse); text != page.Text || note != "" || gh.fillOptions(page).Posting.Location != "Austin, TX" {
		t.Fatalf("greenhouse keeps the page and takes the location: %q (%s) %+v", text, note, gh.fillOptions(page).Posting)
	}

	// A Greenhouse form embedded in the company's page (Pinterest, gh_jid): the
	// frame names the board, so the posting's location is read from it.
	embed := session("https://careers.example.test/jobs/?gh_jid=4012345", ATSGeneric)
	framed := &PageState{Text: page.Text, Frames: []FrameInfo{{Src: "https://job-boards.greenhouse.io/embed/job_app?for=exampleco&token=4012345"}}}
	if text, _ := embed.postingText(context.Background(), framed, ATSGeneric); text != page.Text || embed.postingLoc() != "Austin, TX" || calls[len(calls)-1] != "https://job-boards.greenhouse.io/exampleco/jobs/4012345" {
		t.Fatalf("embedded greenhouse: %q %q %v", text, embed.postingLoc(), calls)
	}
	// Datadog: the run was handed off to the embed itself, so the page's own
	// address names the board.
	handed := session("https://careers.example.test/detail/4012345/?gh_jid=4012345", ATSGreenhouse)
	top := &PageState{URL: "https://job-boards.greenhouse.io/embed/job_app?for=exampleco&token=4012345", Text: page.Text}
	handed.postingText(context.Background(), top, ATSGreenhouse)
	if handed.postingLoc() != "Austin, TX" || calls[len(calls)-1] != "https://job-boards.greenhouse.io/exampleco/jobs/4012345" {
		t.Fatalf("handed-off greenhouse embed: %q %v", handed.postingLoc(), calls)
	}

	// The chain fills SmartRecruiters and Workable as generic sites; their
	// addresses still ask the board, for a place no page of a Workable walk
	// states. A place outside the United States is one nothing else gives. A
	// Workable short link names no account: Workable answers it with a 301 to
	// the account's address, which the page then shows; an address that names
	// the board is asked before the page's (SmartRecruiters' one-click form).
	boardLoc = "Toronto, ON, Canada"
	const sr, workable = "https://jobs.smartrecruiters.com/NorthwindExample/744000100000001-platform-engineer", "https://apply.workable.com/northwind-example/j/1A2B3C4D5E/apply/"
	for _, c := range []struct{ run, page, ask string }{
		{sr, "https://jobs.smartrecruiters.com/oneclick-ui/company/NorthwindExample/publication/0a1b2c3d-1111-2222-3333-444455556666", sr},
		{workable, workable, workable},
		{"https://apply.workable.com/j/1A2B3C4D5E/apply", workable, workable},
	} {
		gs, at, before := session(c.run, ATSGeneric), &PageState{URL: c.page, Text: page.Text}, len(calls)
		if text, note := gs.postingText(context.Background(), at, ATSGeneric); text != page.Text || note != "" || len(calls) != before+1 ||
			calls[before] != c.ask || gs.postingLoc() != boardLoc || gs.fillOptions(at).Posting.Location != boardLoc {
			t.Fatalf("%s: %q (%s) %v, placed %q", c.run, text, note, calls, gs.postingLoc())
		}
	}

	fail = true
	if text, note := session(lever, ATSLever).postingText(context.Background(), form, ATSLever); text != form.Text || !strings.Contains(note, "could not be read") {
		t.Fatalf("an unreadable board: %q (%s)", text, note)
	}
	before := len(calls)
	if text, _ := session("https://careers.example.test/job/1", ATSGeneric).postingText(context.Background(), form, ATSGeneric); text != form.Text || len(calls) != before {
		t.Fatalf("another system asks no board: %q %v", text, calls)
	}

	// Workable's /apply/ page holds only the form: the overview the walk read
	// before it gives the description, first, so the cap keeps it, and a board
	// that cannot be read leaves that note standing.
	desc := "Platform Engineer\nAbout the Role\nStart your first answer with the words small sign, so we know you read this far; we build the platform every team ships on."
	wk := session("https://apply.workable.com/exampleco/j/1A2B3C/", ATSGeneric)
	wk.lead = &PageState{Text: desc}
	if text, note := wk.postingText(context.Background(), form, ATSGeneric); !strings.HasPrefix(text, desc) || !strings.HasSuffix(text, form.Text) ||
		!strings.Contains(note, "page before it") || !strings.Contains(note, "could not be read") {
		t.Fatalf("two-page form: %q (%s)", text, note)
	}
	// One page that shows both keeps its own text, as does a page before it
	// without a paragraph (a sign-in page).
	wk.lead = &PageState{Text: desc}
	same := &PageState{Text: desc + "\nFirst Name\nEmail"}
	if text, note := wk.postingText(context.Background(), same, ATSGeneric); text != same.Text || note != "" {
		t.Fatalf("single page: %q (%s)", text, note)
	}
	wk.lead = &PageState{Text: "Sign in\nEmail\nPassword"}
	if text, _ := wk.postingText(context.Background(), form, ATSGeneric); text != form.Text {
		t.Fatalf("sign-in entry: %q", text)
	}
}

// Where a posting is: the location its tracker row holds, else its board's,
// else the place the first page's JobPosting data states, else the "Location:"
// line of its text (flow rounds 4 and 10). The rungs' matcher requests, the
// check's answers, the office list, the readiness gate and the review read
// postingLoc; the fill takes the first three and leaves the text's line to the
// matcher, after the engine's look at the form's own JobPosting data. A page
// that states nothing is then answered for the tracker's country at every
// step, never presumed in the United States by a later rung.
func TestApply_Fill_PostingLocationIsTheTrackersThenTheBoardsThenTheFirstPagesThenTheTextLine(t *testing.T) {
	g, tr := trackerRig(t)
	for i, c := range []struct{ name, tracker, board, stated, jd, want, fill string }{
		{"the tracker's", "London, United Kingdom", "Austin, TX", "Durham, NC, US", "Location: Boston, MA", "London, United Kingdom", "London, United Kingdom"},
		{"the tracker's, on a page that states nothing", "Toronto, ON, Canada", "", "", "Platform Engineer", "Toronto, ON, Canada", "Toronto, ON, Canada"},
		{"the board's", "", "Austin, TX", "Durham, NC, US", "Location: Boston, MA", "Austin, TX", "Austin, TX"},
		{"the board's, beside a tracker place that settles no country", "NIH-Bethesda", "Bethesda, Maryland, United States", "", "", "Bethesda, Maryland, United States", "Bethesda, Maryland, United States"},
		{"the first page's, with no board", "", "", "Durham, NC, US", "Location: Boston, MA", "Durham, NC, US", "Durham, NC, US"},
		{"the text's line, with neither", "", "", "", "Platform Engineer\nLocation: Boston, MA", "Boston, MA", ""},
	} {
		u := "https://careers.example.test/jobs/" + strconv.Itoa(i+1)
		if c.tracker != "" {
			if _, err := tr.Add(context.Background(), tracker.AddRow{Company: "Northwind Example", Role: "Platform Engineer", URL: u, Location: c.tracker}); err != nil {
				t.Fatal(err)
			}
		}
		s := &session{r: g.r, e: exec1(g, Input{URL: u, Caller: gates.CallerCLI}, Outcome{}), jd: c.jd}
		s.board.Location, s.e.Run.Stated.Location = c.board, c.stated
		got, fill, asked := s.postingLoc(), s.fillOptions(nil).Posting.Location, s.matchRequest("er1", nil).Posting.Location
		if got != c.want || fill != c.fill || asked != c.want {
			t.Errorf("%s: postingLoc %q, fill %q, matcher %q; want %q, %q", c.name, got, fill, asked, c.want, c.fill)
		}
	}
}
