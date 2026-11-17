package learn

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"applysmith/internal/answerbank/match"
)

func selectQuestion(text string, labels ...string) match.Question {
	q := match.Question{Selector: "q", Text: text, Kind: match.KindSelect, SectionPath: []string{"Application", "Questions"}}
	for _, l := range labels {
		q.Options = append(q.Options, match.Option{Value: l, Label: l})
	}
	return q
}

func TestAnswerBank_Telemetry_OneEventPerQuestion(t *testing.T) {
	const runID = "run_0123456789abcdef01234567"
	q := selectQuestion("How did you hear about us?", "Referral", "Job board")
	for _, c := range []struct {
		name string
		r    match.Result
		kind string
		lane string
	}{
		{"filled", match.Result{Outcome: match.OutcomeFilled, EntryID: "heard", AnswerKey: "heard", Answer: "Referral", ChosenLabel: "Referral", Confidence: 0.95, Source: match.SourceEntry}, KindMatch, LaneAnswerBank},
		{"review", match.Result{Outcome: match.OutcomeFilledReview, EntryID: "heard", Answer: "Referral", Confidence: 0.8, Source: match.SourceEntry}, KindMatch, LaneAnswerBank},
		{"model option", match.Result{Outcome: match.OutcomeFilledModel, Answer: "Referral", Confidence: 0.75, Source: match.SourceModelOption}, KindMatch, LaneModelOption},
		// A recognised entry keeps its entry id on the model lane, so the
		// candidates flow can propose the question as that entry's example.
		{"recognised entry", match.Result{Outcome: match.OutcomeFilledReview, EntryID: "heard", Answer: "Referral", ChosenLabel: "Referral", Confidence: 0.8, Source: match.SourceModelEntry}, KindMatch, LaneModelOption},
		{"composed", match.Result{Outcome: match.OutcomeFilledModel, Answer: "A drafted answer", Source: match.SourceComposed}, KindMatch, LaneComposed},
		{"abstained", match.Result{Outcome: match.OutcomeAbstained, Reason: match.ReasonNoEntry}, KindAbstained, LaneAnswerBank},
		{"abstained with entry", match.Result{Outcome: match.OutcomeAbstained, Reason: match.ReasonLowConfidence, EntryID: "heard", Confidence: 0.4}, KindAbstained, LaneAnswerBank},
	} {
		evs := EventsFor("jobs.example.com", q, c.r, baseTime, runID, 7)
		if len(evs) != 1 {
			t.Errorf("%s: %d events, want one per question", c.name, len(evs))
			continue
		}
		ev := evs[0]
		if ev.Kind != c.kind || ev.Lane != c.lane || ev.EntryID != c.r.EntryID || ev.Outcome != string(c.r.Outcome) || ev.Question != q.Text || ev.RunID != runID ||
			ev.ReportNumber != 7 || ev.ControlKind != "select" || len(ev.Options) != 2 || ev.Host != "jobs.example.com" {
			t.Errorf("%s: %+v", c.name, ev)
		}
	}
	e := newEnv(t)
	e.s.Record(context.Background(), EventsFor("jobs.example.com", q, match.Result{Outcome: match.OutcomeFilled, EntryID: "heard", AnswerKey: "heard", Confidence: 0.95, Answer: "Referral"}, baseTime, runID, 7)...)
	e.s.Flush()
	var section, key, run string
	var conf float64
	var rep int
	if err := e.st.Read().QueryRow(`SELECT section_path, answer_key, run_id, confidence, report_number FROM answer_events`).Scan(&section, &key, &run, &conf, &rep); err != nil {
		t.Fatal(err)
	}
	if section != "Application > Questions" || key != "heard" || run != runID || conf != 0.95 || rep != 7 {
		t.Fatal(section, key, run, conf, rep)
	}
}

func TestAnswerBank_Telemetry_WriteFailureDoesNotBlockMatch(t *testing.T) {
	e := newEnv(t)
	e.writeBank(fixedEntry("years-go", "3", []string{yearsQ}))
	// The event table is unusable during a match request.
	e.exec(`DROP TABLE answer_candidate_evidence`)
	e.exec(`DROP TABLE answer_events`)
	r := e.run("jobs.example.com", yearsQ)
	e.s.Record(context.Background(), EventsFor("jobs.example.com", match.Question{Selector: "q", Text: yearsQ, Kind: match.KindText}, r, baseTime, "", 0)...)
	if r.Outcome != match.OutcomeFilled || r.Answer != "3" {
		t.Fatalf("the match response is returned normally: %+v", r)
	}
	e.s.Flush()
	if !strings.Contains(e.logged(), "telemetry write failed") {
		t.Fatalf("a warning is logged: %q", e.logged())
	}
	// A full queue drops events with a warning instead of blocking the caller.
	e2 := newEnv(t)
	e2.exec(`DROP TABLE answer_candidate_evidence`)
	e2.exec(`DROP TABLE answer_events`)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 3*eventQueue; i++ {
			e2.s.Record(context.Background(), Event{Kind: KindMatch, Host: "jobs.example.com", Question: fmt.Sprintf("Question %d", i), ControlKind: "text"})
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Record blocked its caller")
	}
	e2.s.Flush()
}

func TestAnswerBank_Telemetry_SelfIdValueOmitted(t *testing.T) {
	e := newEnv(t)
	q := selectQuestion("Do you identify as a protected veteran?", "I am a protected veteran", "I am not a protected veteran", "Decline to self-identify")
	r := match.Result{Outcome: match.OutcomeFilled, EntryID: "veteran-status", AnswerKey: "veteran_status", Answer: "Decline to self-identify", ChosenLabel: "Decline to self-identify",
		Confidence: 0.95, Source: match.SourceSelfIDDecline}
	e.s.Record(context.Background(), EventsFor("jobs.example.com", q, r, baseTime, "", 0)...)
	// A composed answer to an ordinary question keeps its text; the same lane on a
	// value-withheld control does not.
	e.s.Record(context.Background(),
		Event{Kind: KindMatch, Host: "jobs.example.com", Question: "Tell us a fun fact", ControlKind: "text", Lane: LaneComposed, Value: "Fun fact visible"},
		Event{Kind: KindMatch, Host: "jobs.example.com", Question: "Social Security Number", ControlKind: "text", Lane: LaneComposed, Value: "SSN secret value"},
		Event{Kind: KindMatch, Host: "jobs.example.com", Question: "Date of birth", ControlKind: "text", Lane: LaneComposed, Value: "DOB secret value"},
		Event{Kind: KindUserCorrected, Host: "jobs.example.com", Question: "Choose a password", ControlKind: "text", InputType: "password", EntryID: "pw", Value: "password secret value", Lane: LaneManual},
		Event{Kind: KindUserCorrected, Host: "jobs.example.com", Question: "One-time code", ControlKind: "text", Autocomplete: "one-time-code", EntryID: "otp", Value: "123456 secret", Lane: LaneManual},
		// A neutral label: only the control's own attributes withhold these.
		Event{Kind: KindUserCorrected, Host: "jobs.example.com", Question: "Your account", ControlKind: "text", InputType: "password", EntryID: "pw", Value: "typed-box secret", Lane: LaneManual},
		Event{Kind: KindUserCorrected, Host: "jobs.example.com", Question: "Your account", ControlKind: "text", Autocomplete: "one-time-code", EntryID: "otp", Value: "autocomplete secret", Lane: LaneManual},
	)
	e.s.Flush()
	if e.count("answer_events WHERE entry_id = 'veteran-status' AND outcome = 'filled' AND kind = 'match'") != 1 {
		t.Fatal("the event holds the entry id and the outcome")
	}
	for _, secret := range []string{"SSN secret value", "DOB secret value", "password secret value", "123456 secret", "typed-box secret", "autocomplete secret"} {
		if e.dbHolds(secret) {
			t.Errorf("%q was stored", secret)
		}
	}
	if e.count("answer_events WHERE entry_id = 'veteran-status' AND (value IS NOT NULL OR option_label IS NOT NULL)") != 0 {
		t.Fatal("the chosen self-identification label was stored")
	}
	// The row is there, and an ordinary composed value is kept for the cluster.
	if !e.dbHolds("Fun fact visible") {
		t.Fatal("the composed value of an ordinary question is the evidence of its cluster")
	}
	var v *string
	if err := e.st.Read().QueryRow(`SELECT value FROM answer_events WHERE entry_id = 'veteran-status'`).Scan(&v); err != nil || v != nil {
		t.Fatalf("%v %v", v, err)
	}
	// An applied or matched event of an ordinary question stores no value at all.
	e.s.Record(context.Background(), EventsFor("jobs.example.com", match.Question{Selector: "q", Text: "Favourite colour", Kind: match.KindText},
		match.Result{Outcome: match.OutcomeFilled, EntryID: "colour", Answer: "Green colour value"}, baseTime, "", 0)...)
	e.s.Flush()
	if e.dbHolds("Green colour value") {
		t.Fatal("a bank answer is not telemetry")
	}
	// A model's pick is kept: the bank cannot say what the run chose.
	e.s.Record(context.Background(), EventsFor("jobs.example.com", selectQuestion("What factors matter most to you?", "Career growth", "Mentorship"),
		match.Result{Outcome: match.OutcomeFilledModel, Answer: "Career growth", ChosenLabel: "Career growth", Source: match.SourceModelOption}, baseTime, "", 0)...)
	e.s.Flush()
	if e.count("answer_events WHERE lane = 'model-option' AND value = 'Career growth' AND option_label = 'Career growth'") != 1 {
		t.Fatal("the model's pick was not kept")
	}
}

func TestAnswerBank_Telemetry_Retention180Days(t *testing.T) {
	e := newEnv(t)
	for _, days := range []int{181, 179, 1} {
		e.record(Event{Kind: KindAbstained, At: baseTime.AddDate(0, 0, -days), Host: "jobs.example.com", Question: fmt.Sprintf("Question aged %d", days), ControlKind: "text", Reason: "no-entry"})
	}
	// An upload record ages out with its events.
	u := fillUpload("jobs.example.com", "An old fill", "x")
	u.At = baseTime.AddDate(0, 0, -200)
	e.ingest(u)
	e.exec(`UPDATE learning_uploads SET received_at = ?`, stamp(baseTime.AddDate(0, 0, -181)))
	n, err := e.s.Prune(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if e.count("answer_events WHERE question = 'Question aged 181'") != 0 || e.count("answer_events WHERE question = 'Question aged 179'") != 1 || e.count("answer_events") != 2 {
		t.Fatal("181 days is deleted, 179 is kept")
	}
	if e.count("learning_uploads") != 0 {
		t.Fatal("old upload records are pruned")
	}
	// Pruning settles the support of a pending candidate whose rows went.
	e2 := newEnv(t)
	for i := 0; i < 3; i++ {
		e2.record(Event{Kind: KindAbstained, At: baseTime.Add(-time.Hour), Host: "jobs.example.com", Question: heardQ, ControlKind: "text", Reason: "no-entry"})
	}
	e2.generate()
	e2.exec(`UPDATE answer_events SET at = ?`, stamp(baseTime.AddDate(0, 0, -181)))
	if _, err := e2.s.Prune(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c := e2.only(StatePending); c.Support != 0 {
		t.Fatalf("%+v", c)
	}
}

func TestAnswerBank_Usage_CountersPerEntry(t *testing.T) {
	e := newEnv(t)
	e.writeBank(fixedEntry("a", "1", nil), fixedEntry("b", "2", nil))
	ev := func(kind, entry string, extra ...func(*Event)) {
		x := Event{Kind: kind, At: baseTime.Add(-time.Hour), Host: "jobs.example.com", Question: "Q " + kind + entry, ControlKind: "text", EntryID: entry, Lane: LaneAnswerBank}
		for _, f := range extra {
			f(&x)
		}
		e.record(x)
	}
	for i := 0; i < 3; i++ {
		ev(KindMatch, "a")
	}
	for i := 0; i < 2; i++ {
		ev(KindApplied, "a")
	}
	ev(KindAbstained, "a")
	ev(KindMatch, "b")
	ev(KindMatch, "")              // no entry: contributes to none
	ev(KindAbstained, "")          // an abstain with no entry blames none
	ev(KindMatch, "gone")          // not in the bank: omitted
	ev(KindCandidateApproved, "a") // decisions are not usage
	ev(KindAbstainReason, "a", func(x *Event) { x.Reason = "no-grounding" })
	e.correct("a", 2)
	u := e.usage()
	a := u["a"]
	// A correction never inflates the match count.
	if a.Matches != 3 || a.Applies != 2 || a.Abstains != 1 || a.Corrections != 2 {
		t.Fatalf("%+v", a)
	}
	if b := u["b"]; b.Matches != 1 || b.Applies+b.Abstains+b.Corrections != 0 {
		t.Fatalf("%+v", b)
	}
	if _, ok := u["gone"]; ok {
		t.Fatal("rows for ids no longer in the bank are omitted")
	}
	// A control failure is stored and counts for nobody.
	e.ingest(Upload{EventID: eid("cf"), Kind: UploadCorrection, Origin: OriginUserEdit, Host: "jobs.example.com", Question: yearsQ, ControlKind: "text", Before: "x", After: " X", EntryID: "b", At: baseTime})
	if e.usage()["b"].Corrections != 0 {
		t.Fatal("a control failure is not a correction")
	}
}

func (e *env) usage() map[string]Usage {
	e.t.Helper()
	rows, err := e.s.Usage(context.Background(), UsageWindow)
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]Usage{}
	for _, r := range rows {
		out[r.EntryID] = r
	}
	return out
}

func TestAnswerBank_Usage_TimestampsAndExamples(t *testing.T) {
	e := newEnv(t)
	e.writeBank(fixedEntry("a", "1", nil))
	t0 := baseTime.Add(-48 * time.Hour)
	at := func(h int) time.Time { return t0.Add(time.Duration(h) * time.Hour) }
	c1, c2 := 0.9, 0.7
	e.record(
		Event{Kind: KindMatch, At: at(1), Host: "jobs.example.com", Question: "Q1", ControlKind: "text", EntryID: "a", Confidence: &c1},
		Event{Kind: KindMatch, At: at(2), Host: "jobs.example.com", Question: "Q2", ControlKind: "text", EntryID: "a", Confidence: &c2},
		Event{Kind: KindApplied, At: at(3), Host: "jobs.example.com", Question: "Q3", ControlKind: "text", EntryID: "a"},
		Event{Kind: KindAbstained, At: at(4), Host: "jobs.example.com", Question: "Q4", ControlKind: "text", EntryID: "a"},
		Event{Kind: KindMatch, At: at(5), Host: "jobs.example.com", Question: "Q5", ControlKind: "text", EntryID: "a"},
		Event{Kind: KindMatch, At: at(6), Host: "jobs.example.com", Question: "Q6", ControlKind: "text", EntryID: "a"},
		Event{Kind: KindMatch, At: at(7), Host: "jobs.example.com", Question: "Q2", ControlKind: "text", EntryID: "a"}, // a repeat moves to the end
	)
	e.ingest(Upload{EventID: eid("late"), Kind: UploadCorrection, Origin: OriginUserEdit, Host: "jobs.example.com", Question: "Q7", ControlKind: "text", Before: "1", After: "2", EntryID: "a", At: at(9)})
	a := e.usage()["a"]
	f := func(t time.Time) string { return stamp(t) }
	if a.LastMatchedAt != f(at(7)) || a.LastAppliedAt != f(at(3)) || a.LastCorrectedAt != f(at(9)) || a.LastActivityAt != f(at(9)) {
		t.Fatalf("%+v", a)
	}
	if a.AverageConfidence == nil || *a.AverageConfidence < 0.799 || *a.AverageConfidence > 0.801 {
		t.Fatalf("the mean covers the events that carried a confidence: %v", a.AverageConfidence)
	}
	// The latest five distinct texts, oldest to newest; the repeated Q2 moved to the end.
	if got := strings.Join(a.ExampleQuestions, ","); got != "Q4,Q5,Q6,Q2,Q7" {
		t.Fatalf("%s", got)
	}
}

func TestAnswerBank_Usage_CountsCoverTheWindowAndLastUsedCoversEverything(t *testing.T) {
	e := newEnv(t)
	e.writeBank(fixedEntry("recent", "1", nil), fixedEntry("idle", "1", nil))
	day := 24 * time.Hour
	ev := func(entry string, ago time.Duration, conf float64) Event {
		return Event{Kind: KindMatch, At: baseTime.Add(-ago), Host: "jobs.example.com", Question: fmt.Sprintf("Q %s %d", entry, ago/day), ControlKind: "text", EntryID: entry, Confidence: &conf}
	}
	e.record(ev("recent", 40*day, 0.2), ev("recent", 29*day, 0.6), ev("recent", day, 1.0), ev("idle", 45*day, 0.9))
	rows, err := e.s.Usage(context.Background(), UsageWindow)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Usage{}
	for _, r := range rows {
		got[r.EntryID] = r
	}
	if r := got["recent"]; r.Matches != 2 || r.AverageConfidence == nil || *r.AverageConfidence < 0.799 || *r.AverageConfidence > 0.801 {
		t.Fatalf("an event older than 30 days is not counted: %+v", r)
	}
	if r := got["idle"]; r.Matches != 0 || r.AverageConfidence != nil || r.LastMatchedAt != stamp(baseTime.Add(-45*day)) || r.LastActivityAt != r.LastMatchedAt {
		t.Fatalf("an idle entry keeps its last-used time: %+v", r)
	}
	all, err := e.s.Usage(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range all {
		if r.EntryID == "recent" && r.Matches != 3 {
			t.Fatalf("a window of zero counts every retained event: %+v", r)
		}
	}
}
