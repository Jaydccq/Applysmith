package documents

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"applysmith/internal/llm"
	"applysmith/internal/store"
	"applysmith/internal/vault"
)

// reviewer scripts the review and the revision on top of the echo drafter. It
// captures the drafts the drafter wrote so a revision can start from them.
type reviewer struct {
	review func(prompt string) reviewReply
	revise func(r *ResumeDraft, l *LetterDraft)
	resume *ResumeDraft
	letter *LetterDraft
	fail   error
}

func cloneJSON[T any](v T) T {
	var out T
	b, _ := json.Marshal(v)
	_ = json.Unmarshal(b, &out)
	return out
}

func (rv *reviewer) model() *scriptModel {
	inner := echoModel(func(task string, v any) any {
		switch d := v.(type) {
		case ResumeDraft:
			c := cloneJSON(d)
			rv.resume = &c
		case LetterDraft:
			c := cloneJSON(d)
			rv.letter = &c
		}
		return v
	})
	return &scriptModel{fn: func(task, prompt string) (any, error) {
		switch {
		case task == "document-review":
			if rv.fail != nil {
				return nil, rv.fail
			}
			if rv.review == nil {
				return reviewReply{Factual: []reviewFinding{}, Narrative: []narrativeNote{}}, nil
			}
			return rv.review(prompt), nil
		case strings.HasPrefix(prompt, "Task: document-revision"):
			var out revisionReply
			if rv.resume != nil {
				c := cloneJSON(*rv.resume)
				out.Resume = &c
			}
			if rv.letter != nil {
				c := cloneJSON(*rv.letter)
				out.Letter = &c
			}
			if rv.revise != nil {
				rv.revise(out.Resume, out.Letter)
			}
			return out, nil
		}
		return inner.fn(task, prompt)
	}}
}

func (f *fixture) reviewRecord(res Result) ReviewRecord {
	f.t.Helper()
	d := f.svc.mustGet(f.t, res.Documents[0])
	var rec ReviewRecord
	must(f.t, json.Unmarshal([]byte(f.read(d.Path+"/document-review.json")), &rec))
	return rec
}

func (f *fixture) storedResume(res Result) string {
	d := f.svc.mustGet(f.t, res.Documents[0])
	return f.read(d.Path + "/resume.json")
}

var pair = Request{Action: ActionPair, Mode: ModeTailored}

func (f *fixture) runPair() Result {
	r := pair
	r.CandidacyID, r.Posting = f.cand, testPosting
	return f.run(r)
}

// A pair gets exactly one review call; a review with nothing to fix makes no
// revision call.
func TestReview_OneCallForThePairAndNoRevisionWhenSound(t *testing.T) {
	m := (&reviewer{}).model()
	f := newFixture(t, fixtureOpts{model: m})
	res := f.runPair()
	if !res.Published {
		t.Fatalf("result = %+v", res)
	}
	if got := strings.Join(m.tasks, ","); got != "resume-write,cover-letter,document-review" {
		t.Errorf("tasks = %s", got)
	}
	rec := f.reviewRecord(res)
	if rec.Status != ContentReviewed || rec.Revisions != 0 || rec.Calls.Review == "" || rec.Calls.Revision != "" {
		t.Errorf("record = %+v", rec)
	}
	if rec.Revision == nil || rec.Revision.Attempted || rec.Revision.Reason == "" {
		t.Errorf("revision = %+v; want a stated reason for making none", rec.Revision)
	}
}

// Factual and wording findings are kept apart from narrative suggestions, and
// every factual finding carries manifest evidence ids.
func TestReview_FactualAndNarrativeFindingsAreSeparateAndCiteEvidence(t *testing.T) {
	rv := &reviewer{review: func(string) reviewReply {
		return reviewReply{
			Factual: []reviewFinding{
				{Span: "resume.experience.X1.3", Category: "specificity", Issue: "Name what was reviewed.", Evidence: []string{"E5"}, Edit: "Mentored four engineers through code review and design reviews"},
				{Span: "resume.profile", Category: "fidelity", Issue: "Cites a line that does not exist.", Evidence: []string{"E99"}},
				{Span: "resume.experience.X9.1", Category: "coverage", Issue: "No such bullet.", Evidence: []string{"E3"}},
				{Span: "resume.profile", Category: "voice", Issue: "No evidence given.", Evidence: nil},
			},
			Narrative: []narrativeNote{{Category: "coverage", Suggestion: "Lead the letter with the mentoring requirement."}},
		}
	}}
	m := rv.model()
	f := newFixture(t, fixtureOpts{model: m})
	rec := f.reviewRecord(f.runPair())
	if len(rec.Factual) != 1 || rec.Factual[0].Span != "resume.experience.X1.3" || len(rec.Factual[0].Evidence) != 1 || rec.Factual[0].Evidence[0] != "E5" {
		t.Errorf("factual = %+v", rec.Factual)
	}
	if len(rec.Narrative) != 1 || !strings.Contains(rec.Narrative[0].Suggestion, "mentoring") {
		t.Errorf("narrative = %+v", rec.Narrative)
	}
	codes := map[string]bool{}
	for _, r := range rec.Rejected {
		codes[r.Code] = true
	}
	for _, want := range []string{"unknown-evidence", "unknown-span", "no-evidence"} {
		if !codes[want] {
			t.Errorf("rejected codes = %v; want %s", codes, want)
		}
	}
	// The narrative suggestion is advice for the user and never reaches the revision.
	for _, r := range m.requestsTo("resume-write") {
		if strings.Contains(r.Prompt, "mentoring requirement") {
			t.Error("a narrative suggestion was sent to the revision")
		}
	}
}

// Reviewer proposes an unsupported metric: the edit is rejected and the
// original grounded claim remains.
func TestReview_UnsupportedMetricIsRejectedAndTheGroundedClaimRemains(t *testing.T) {
	rv := &reviewer{review: func(string) reviewReply {
		return reviewReply{Factual: []reviewFinding{{Span: "resume.experience.X1.1", Category: "specificity", Issue: "Quantify more.", Evidence: []string{"E3"},
			Edit: "Led the platform migration and cut incidents by 60% across 12 services"}}, Narrative: []narrativeNote{}}
	}}
	m := rv.model()
	f := newFixture(t, fixtureOpts{model: m})
	res := f.runPair()
	rec := f.reviewRecord(res)
	if len(rec.Factual) != 0 || len(rec.Rejected) != 1 || rec.Rejected[0].Code != "unsupported-figure" {
		t.Fatalf("record = %+v", rec)
	}
	if m.callsTo("resume-write") != 1 {
		t.Errorf("resume-write calls = %d; a rejected edit makes no revision", m.callsTo("resume-write"))
	}
	stored := f.storedResume(res)
	if strings.Contains(stored, "60%") || !strings.Contains(stored, "cut incidents by 40% across 12 services") {
		t.Errorf("resume.json = %s", stored)
	}
}

// A narrative note is advice and never applied, but it is not shown when it
// carries a figure or a name that no evidence holds.
func TestReview_NarrativeNoteWithUnsupportedClaimIsRejected(t *testing.T) {
	rv := &reviewer{review: func(string) reviewReply {
		return reviewReply{Narrative: []narrativeNote{
			{Category: "specificity", Suggestion: "Say that you cut incidents by 75% across the whole fleet."},
			{Category: "coverage", Suggestion: "Mention the certification you earned at Zorblat Labs."},
			{Category: "coverage", Suggestion: "Lead the letter with the mentoring requirement."},
		}}
	}}
	f := newFixture(t, fixtureOpts{model: rv.model()})
	rec := f.reviewRecord(f.runPair())
	if len(rec.Narrative) != 1 || !strings.Contains(rec.Narrative[0].Suggestion, "mentoring") {
		t.Fatalf("narrative = %+v", rec.Narrative)
	}
	if len(rec.Rejected) != 2 {
		t.Fatalf("rejected = %+v", rec.Rejected)
	}
	for i, code := range []string{"unsupported-figure", "unsupported-name"} {
		r := rec.Rejected[i]
		if r.Kind != "narrative" || r.Code != code || r.Detail == "" {
			t.Errorf("rejected[%d] = %+v, want a narrative %s", i, r, code)
		}
	}
	// A figure the draft or the evidence already holds is fine to discuss.
	rv2 := &reviewer{review: func(string) reviewReply {
		return reviewReply{Narrative: []narrativeNote{{Category: "specificity", Suggestion: "The 40% incident reduction deserves a stronger verb."}}}
	}}
	f2 := newFixture(t, fixtureOpts{model: rv2.model()})
	if rec := f2.reviewRecord(f2.runPair()); len(rec.Narrative) != 1 || len(rec.Rejected) != 0 {
		t.Errorf("a grounded figure was rejected: %+v", rec)
	}
	// A note may cite the ids and span addresses the reviewer was shown; an
	// unbacked name beside them still fails.
	rv3 := &reviewer{review: func(string) reviewReply {
		return reviewReply{Narrative: []narrativeNote{
			{Category: "coverage", Suggestion: "R3 is covered by E1; lead letter.opening with it, as resume.experience.X1.1 does."},
			{Category: "coverage", Suggestion: "R3 is covered by E1; add Zorblat to letter.opening."},
		}}
	}}
	f3 := newFixture(t, fixtureOpts{model: rv3.model()})
	if rec := f3.reviewRecord(f3.runPair()); len(rec.Narrative) != 1 || len(rec.Rejected) != 1 || !strings.Contains(rec.Rejected[0].Detail, "Zorblat") {
		t.Errorf("ids in a note: %+v", rec)
	}
}

func TestReview_PersonalityInferenceIsRejected(t *testing.T) {
	rv := &reviewer{review: func(string) reviewReply {
		return reviewReply{
			Factual:   []reviewFinding{{Span: "resume.profile", Category: "voice", Issue: "The writer sounds introverted; warm it up.", Evidence: []string{"E2"}}},
			Narrative: []narrativeNote{{Category: "voice", Suggestion: "Reflect an extroverted personality."}},
		}
	}}
	f := newFixture(t, fixtureOpts{model: rv.model()})
	rec := f.reviewRecord(f.runPair())
	if len(rec.Factual) != 0 || len(rec.Narrative) != 0 || len(rec.Rejected) != 2 || rec.Rejected[0].Code != "trait-inference" {
		t.Errorf("record = %+v", rec)
	}
}

// A grounded revision replaces the draft; the artifacts are built from it and
// the grounding gate stored with it is the revised draft's.
func TestReview_GroundedRevisionIsAppliedAndRebuilt(t *testing.T) {
	const wording = "Mentored four engineers through code review and design reviews"
	rv := &reviewer{
		review: func(string) reviewReply {
			return reviewReply{Factual: []reviewFinding{{Span: "resume.experience.X1.3", Category: "specificity", Issue: "Name what was reviewed.", Evidence: []string{"E5"}, Edit: wording}},
				Narrative: []narrativeNote{}}
		},
		revise: func(r *ResumeDraft, l *LetterDraft) { r.Experience[0].Bullets[2].Text = wording },
	}
	m := rv.model()
	f := newFixture(t, fixtureOpts{model: m})
	res := f.runPair()
	if got := strings.Join(m.tasks, ","); got != "resume-write,cover-letter,document-review,resume-write" {
		t.Errorf("tasks = %s", got)
	}
	rec := f.reviewRecord(res)
	if rec.Status != ContentRevised || rec.Revisions != 1 || rec.Revision == nil || !rec.Revision.Applied || rec.Revision.ResumeGate == nil || !rec.Revision.ResumeGate.OK() {
		t.Fatalf("record = %+v revision = %+v", rec, rec.Revision)
	}
	if rec.Calls.Review == "" || rec.Calls.Revision == "" || rec.Calls.Review == rec.Calls.Revision {
		t.Errorf("calls = %+v", rec.Calls)
	}
	d := f.svc.mustGet(t, res.Documents[0])
	for _, name := range []string{"resume.json", "resume.md", "resume.tex"} {
		if body := f.read(d.Path + "/" + name); !strings.Contains(body, wording) {
			t.Errorf("%s was not built from the revised draft", name)
		}
	}
	if gate := f.read(d.Path + "/resume-quality-gate.md"); strings.Contains(gate, "unsupported") {
		t.Errorf("quality gate = %s", gate)
	}
	// The letter was not revised and keeps its first draft.
	cover := f.svc.mustGet(t, res.Documents[1])
	if cover.Kind != KindCover {
		t.Fatalf("second document = %+v", cover)
	}
	if got := f.letterDoc(cover).LetterDraft; jsonString(got) != jsonString(*rv.letter) {
		t.Errorf("the letter changed from its first draft:\n%s\nwant\n%s", jsonString(got), jsonString(*rv.letter))
	}
}

// A revision that fails the grounding gate leaves the pre-revision draft.
func TestReview_RevisionThatFailsGroundingKeepsThePreRevisionDraft(t *testing.T) {
	rv := &reviewer{
		review: func(string) reviewReply {
			return reviewReply{Factual: []reviewFinding{{Span: "resume.experience.X1.1", Category: "specificity", Issue: "Be more specific.", Evidence: []string{"E3"}}},
				Narrative: []narrativeNote{}}
		},
		revise: func(r *ResumeDraft, l *LetterDraft) {
			r.Experience[0].Bullets[0].Text = "Led the platform migration and cut incidents by 28% across 12 services"
		},
	}
	m := rv.model()
	f := newFixture(t, fixtureOpts{model: m})
	res := f.runPair()
	if !res.Published {
		t.Fatalf("result = %+v", res)
	}
	if m.callsTo("resume-write") != 2 || m.callsTo("document-review") != 1 {
		t.Errorf("tasks = %v", m.tasks)
	}
	rec := f.reviewRecord(res)
	if rec.Status != ContentReviewed || rec.Revisions != 0 || rec.Revision == nil || !rec.Revision.Attempted || rec.Revision.Applied ||
		rec.Revision.ResumeGate == nil || rec.Revision.ResumeGate.OK() || !strings.Contains(rec.Revision.Reason, "unsupported-figure") {
		t.Fatalf("record = %+v revision = %+v", rec, rec.Revision)
	}
	if stored := f.storedResume(res); strings.Contains(stored, "28%") || !strings.Contains(stored, "40%") {
		t.Errorf("resume.json = %s", stored)
	}
	if !strings.Contains(strings.Join(res.Warnings, "|"), "review revision was not applied") {
		t.Errorf("warnings = %v", res.Warnings)
	}
}

// A revised letter that names a company the input does not hold is refused,
// and the résumé change that came with it is not applied either.
func TestReview_RevisedLetterWithAnUnsupportedCompanyClaimIsRefusedAsAPair(t *testing.T) {
	const wording = "Mentored four engineers through code review and design reviews"
	rv := &reviewer{
		review: func(string) reviewReply {
			return reviewReply{Factual: []reviewFinding{{Span: "letter.closing", Category: "specificity", Issue: "The closing is generic.", Evidence: []string{"E3"}}}, Narrative: []narrativeNote{}}
		},
		revise: func(r *ResumeDraft, l *LetterDraft) {
			r.Experience[0].Bullets[2].Text = wording
			l.Closing = "I would be glad to discuss how this experience can help Example Corp after its acquisition of Zephyr Labs. Thank you for reading."
		},
	}
	f := newFixture(t, fixtureOpts{model: rv.model()})
	res := f.runPair()
	rec := f.reviewRecord(res)
	if rec.Revision == nil || rec.Revision.Applied || rec.Revision.LetterGate == nil || !strings.Contains(rec.Revision.Reason, "unsupported-name") {
		t.Fatalf("revision = %+v", rec.Revision)
	}
	if strings.Contains(f.storedResume(res), wording) {
		t.Error("half of a rejected revision was applied")
	}
}

// No fan-out: however many findings come back, there is one review call and
// at most one revision call, and the revision covers the whole pair.
func TestReview_AtMostOneRevisionCallForAPair(t *testing.T) {
	var many []reviewFinding
	for i := 0; i < 12; i++ {
		many = append(many, reviewFinding{Span: "resume.experience.X1.1", Category: "redundancy", Issue: "Tighten.", Evidence: []string{"E3"}})
	}
	many[11] = reviewFinding{Span: "letter.body", Category: "consistency", Issue: "Align with the résumé.", Evidence: []string{"E3"}}
	rv := &reviewer{
		review: func(string) reviewReply { return reviewReply{Factual: many, Narrative: []narrativeNote{}} },
		revise: func(r *ResumeDraft, l *LetterDraft) {
			r.Experience[0].Bullets[2].Text = "Mentored four engineers through code review and design reviews"
			l.Body = "Examples from my recent work are below, both from the Example Systems role."
		},
	}
	m := rv.model()
	f := newFixture(t, fixtureOpts{model: m})
	res := f.runPair()
	if m.callsTo("document-review") != 1 {
		t.Errorf("document-review calls = %d", m.callsTo("document-review"))
	}
	if m.callsTo("resume-write") != 2 || m.callsTo("cover-letter") != 1 {
		t.Errorf("tasks = %v; want one draft and one revision under resume-write and one letter draft", m.tasks)
	}
	rec := f.reviewRecord(res)
	if rec.Status != ContentRevised || rec.Revision == nil || rec.Revision.LetterGate == nil || rec.Revision.ResumeGate == nil {
		t.Fatalf("record = %+v", rec)
	}
	if body := f.read(f.svc.mustGet(t, res.Documents[1]).Path + "/cover-letter.json"); !strings.Contains(body, "both from the Example Systems role") {
		t.Errorf("the letter was not revised in the same call: %s", body)
	}
}

// Provider fails during review: the review is unavailable, no retry
// conversation starts, and the run still gates, renders and publishes.
func TestReview_ProviderFailureIsUnavailableNotApprovedAndRunsOn(t *testing.T) {
	rv := &reviewer{fail: context.DeadlineExceeded}
	m := rv.model()
	f := newFixture(t, fixtureOpts{model: m})
	res := f.runPair()
	if !res.Published || len(res.Documents) != 2 || res.Generation != GenModel {
		t.Fatalf("result = %+v", res)
	}
	if m.callsTo("document-review") != 1 || m.callsTo("resume-write") != 1 || m.callsTo("cover-letter") != 1 {
		t.Errorf("tasks = %v; the failed review must not be retried", m.tasks)
	}
	rec := f.reviewRecord(res)
	if rec.Status != ContentUnavailable || !strings.Contains(rec.Reason, "timeout") || rec.Revisions != 0 || rec.Revision != nil {
		t.Errorf("record = %+v", rec)
	}
	d := f.svc.mustGet(t, res.Documents[0])
	if !strings.Contains(f.read(d.Path+"/document-review.json"), `"unavailable"`) || strings.Contains(f.read(d.Path+"/document-review.json"), "approved") {
		t.Error("the record must say unavailable and never approved")
	}
	if !strings.Contains(f.read(d.Path+"/resume-quality-gate.md"), "Résumé quality gate") {
		t.Error("the grounding gate did not run")
	}
	if !strings.Contains(strings.Join(res.Warnings, "|"), "independent review is unavailable (timeout)") {
		t.Errorf("warnings = %v", res.Warnings)
	}
}

func TestReview_SkippedWhenTheRunDeadlineLeavesNoRoom(t *testing.T) {
	m := (&reviewer{}).model()
	f := newFixture(t, fixtureOpts{model: m})
	f.svc.runTimeout = time.Minute
	res := f.runPair()
	if !res.Published || m.callsTo("document-review") != 0 {
		t.Fatalf("result = %+v tasks = %v", res, m.tasks)
	}
	if rec := f.reviewRecord(res); rec.Status != ContentUnavailable || !strings.Contains(rec.Reason, "deadline") {
		t.Errorf("record = %+v", rec)
	}
}

// Review and revision calls go through the routing capability: each appears in
// the call ledger under its task and run, and the record names those calls.
func TestReview_CallsAreInTheLedgerUnderTheRunAndPassTheRouterSchemas(t *testing.T) {
	const wording = "Mentored four engineers through code review and design reviews"
	rv := &reviewer{
		review: func(string) reviewReply {
			return reviewReply{Factual: []reviewFinding{{Span: "resume.experience.X1.3", Category: "specificity", Issue: "Name what was reviewed.", Evidence: []string{"E5"}, Edit: wording}},
				Narrative: []narrativeNote{{Category: "coverage", Suggestion: "Lead with mentoring."}}}
		},
		revise: func(r *ResumeDraft, l *LetterDraft) { r.Experience[0].Bullets[2].Text = wording },
	}
	script := rv.model()
	fake := llm.NewFake()
	fake.SetGenerator(func(task, prompt string) (string, bool) {
		v, err := script.fn(task, prompt)
		if err != nil {
			return "", false
		}
		b, _ := json.Marshal(v)
		return string(b), true
	})
	router := func(st *store.Store) Model {
		v, err := vault.New("0123456789abcdef0123456789abcdef", vault.NewMemory(), vault.WithGetenv(func(string) string { return "" }))
		must(t, err)
		return llm.New(st, v, llm.WithGetenv(func(string) string { return "" }), llm.WithFake(fake), llm.WithOfflineSimulation())
	}
	f := newFixture(t, fixtureOpts{router: router})
	res := f.runPair()
	if !res.Published {
		t.Fatalf("result = %+v", res)
	}
	rows, err := f.st.Read().QueryContext(t.Context(), `SELECT task, call_id FROM llm_ledger WHERE run_id = ? ORDER BY id`, res.RunID)
	must(t, err)
	defer rows.Close()
	var tasks []string
	ids := map[string][]string{}
	for rows.Next() {
		var task, id string
		must(t, rows.Scan(&task, &id))
		tasks = append(tasks, task)
		ids[task] = append(ids[task], id)
	}
	if got := strings.Join(tasks, ","); got != "resume-write,cover-letter,document-review,resume-write" {
		t.Fatalf("ledger tasks = %s", got)
	}
	rec := f.reviewRecord(res)
	if rec.Status != ContentRevised || rec.Calls.Review != ids["document-review"][0] || rec.Calls.Revision != ids["resume-write"][1] {
		t.Errorf("record = %+v calls; ledger = %v", rec, ids)
	}
}

// A review note may open with an imperative, count the draft's parts and name
// the posting's terms in a gap; asking for an unheld name or figure is refused.
func TestReview_NarrativeNotesNameGapsNotClaims(t *testing.T) {
	m := demoManifest(t, "TixTrack", "Software Engineer")
	m.Requirements = append(m.Requirements, Requirement{ID: "R8", Text: "Comfort using Git."}, Requirement{ID: "R9", Text: "SQL for Level 2."},
		Requirement{ID: "R1", Text: "Eligible to work in the United States"})
	m.NiceToHave = append(m.NiceToHave, "Payments such as Stripe")
	for _, c := range []struct {
		note string
		kept bool
	}{
		{"R8 (Git) is not covered, and the draft rightly does not claim it.", true},
		{"Bullet 3 pairs two separate bodies of work. Consider giving the strongest one its own line on one measurable outcome.", true},
		{"R1 (US work eligibility) is not addressed.", true},
		{"Git is not covered; add it to the summary.", false},
		{"R8 (Git) is not covered, and neither is Relativity Space.", false},
		{"Mention that you mentored ten engineers.", false},
	} {
		_, kept, rej := screenReply(m, nil, reviewReply{Narrative: []narrativeNote{{Category: "coverage", Suggestion: c.note}}})
		if len(kept) == 1 != c.kept {
			t.Errorf("%q: kept = %v (%+v)", c.note, !c.kept, rej)
		}
	}
}
