package learn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// dbHolds reports whether any text in any table of the database contains the
// needle: the proof that a value was never stored anywhere.
func (e *env) dbHolds(needle string) bool {
	e.t.Helper()
	tables, err := e.st.Read().Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		e.t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var n string
		if err := tables.Scan(&n); err != nil {
			e.t.Fatal(err)
		}
		names = append(names, n)
	}
	tables.Close()
	for _, n := range names {
		rows, err := e.st.Read().Query("SELECT * FROM " + n)
		if err != nil {
			e.t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				e.t.Fatal(err)
			}
			for _, v := range vals {
				if strings.Contains(fmt.Sprint(v), needle) {
					rows.Close()
					return true
				}
				if b, ok := v.([]byte); ok && strings.Contains(string(b), needle) {
					rows.Close()
					return true
				}
			}
		}
		rows.Close()
	}
	return false
}

func (e *env) fillsList() []ManualFill {
	e.t.Helper()
	list, _, err := e.s.ManualFills(context.Background(), 1000, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	return list
}

func TestAnswerBank_Events_BatchIdempotentByEventID(t *testing.T) {
	e := newEnv(t)
	var ups []Upload
	for i := 0; i < 10; i++ {
		ups = append(ups, fillUpload("jobs.example.com", fmt.Sprintf("Question number %d", i), "Springfield"))
	}
	first := e.ingest(ups...)
	if first.Accepted != 10 || len(first.Duplicates) != 0 || len(first.Rejected) != 0 {
		t.Fatalf("first: %+v", first)
	}
	second := e.ingest(ups...)
	if second.Accepted != 10 || len(second.Duplicates) != 10 {
		t.Fatalf("second post must report all ten as already accepted: %+v", second)
	}
	if n := e.count("manual_fills"); n != 10 {
		t.Fatalf("%d fills exist, want 10", n)
	}
	if n := e.count("learning_uploads"); n != 10 {
		t.Fatalf("%d uploads exist, want 10", n)
	}
}

func TestAnswerBank_ManualFills_BatchBounds(t *testing.T) {
	e := newEnv(t)
	var ups []Upload
	for i := 0; i < MaxBatch+1; i++ {
		ups = append(ups, uncertainty("jobs.example.com", fmt.Sprintf("Question %d", i), "no-entry"))
	}
	if _, err := e.s.Ingest(context.Background(), ups); !errors.Is(err, ErrBatchSize) {
		t.Fatalf("201 events: %v", err)
	}
	if _, err := e.s.Ingest(context.Background(), nil); !errors.Is(err, ErrBatchSize) {
		t.Fatalf("no events: %v", err)
	}
	if n := e.count("learning_uploads") + e.count("answer_events"); n != 0 {
		t.Fatalf("an oversized batch stored %d rows", n)
	}
	if res := e.ingest(ups[:MaxBatch]...); res.Accepted != MaxBatch {
		t.Fatalf("200 events: %+v", res)
	}
}

func TestAnswerBank_ManualFills_RecordShape(t *testing.T) {
	e := newEnv(t)
	u := fillUpload("Jobs.Example.com", "Preferred pronouns for the team directory", "")
	run := e.issueRun()
	u.ControlKind, u.OptionLabel, u.After = "select", "Prefer not to say", "prefer-not"
	u.Before, u.EntryID, u.RunID, u.ReportNumber = "they/them", "pronouns", run, 12
	u.Options = []string{"he/him", "she/her", "they/them", "Prefer not to say"}
	e.ingest(u)
	fills := e.fillsList()
	if len(fills) != 1 {
		t.Fatalf("%d fills", len(fills))
	}
	f := fills[0]
	if f.ID == 0 || f.Host != "jobs.example.com" || f.Question != "self-id:pronouns" || f.ControlKind != "select" || f.EntryID != "pronouns" ||
		f.RunID != run || f.ReportNumber == nil || *f.ReportNumber != 12 || !f.At.Equal(u.At) || !f.ReceivedAt.Equal(baseTime) || !f.GroundTruth {
		t.Fatalf("record: %+v", f)
	}
	// Pronouns are a self-identification control: the edit is noted, no value.
	if f.Value != nil || f.OptionLabel != nil || f.PriorValue != nil || f.Withheld != WithheldSelfID {
		t.Fatalf("a self-identification record holds no value: %+v", f)
	}
	v := fillUpload("jobs.example.com", "Preferred start date notes", "Mid March")
	v.Before, v.EntryID = "Immediately", "address-street"
	e.ingest(v)
	for _, f := range e.fillsList() {
		if f.Question == v.Question && (f.Value == nil || *f.Value != "Mid March" || f.PriorValue == nil || *f.PriorValue != "Immediately" || f.EntryID != "address-street") {
			t.Fatalf("a non-sensitive record keeps the value, the prior value and the producing entry: %+v", f)
		}
	}
}

func TestAnswerBank_ManualFills_SystemWritesNotRecorded(t *testing.T) {
	e := newEnv(t)
	// The system fills a city field and the user does nothing: nothing is
	// uploaded, so no record exists.
	if n := e.count("manual_fills"); n != 0 {
		t.Fatal("no records before any upload")
	}
	// A client that posts the system's own value anyway is refused: the value did
	// not change.
	u := fillUpload("jobs.example.com", "City", "Springfield")
	u.Before, u.EntryID = "springfield ", "address-city"
	res := e.ingest(u)
	if len(res.Rejected) != 1 || res.Rejected[0].Reason != "not-an-edit" || res.Accepted != 0 {
		t.Fatalf("%+v", res)
	}
	if e.count("manual_fills")+e.count("answer_events")+e.count("learning_uploads") != 0 {
		t.Fatal("a refused event stores nothing")
	}
	// A correction from the apply chain's own check stage is not the user.
	c := Upload{EventID: eid("chain-1"), Kind: UploadCorrection, Host: "jobs.example.com", Question: "City", ControlKind: "text", Origin: "apply-chain",
		Before: "Springfield", After: "Shelbyville", EntryID: "address-city", At: baseTime}
	if res := e.ingest(c); len(res.Rejected) != 1 || res.Rejected[0].Reason != "origin-not-allowed" {
		t.Fatalf("%+v", res)
	}
	if e.count("answer_events") != 0 {
		t.Fatal("the chain's answer is not a user correction")
	}
}

// The system fills a city field and the user does nothing: whether nothing is
// uploaded or the engine's read-back of its own value is, no ground truth, no
// candidate and no correction results.
func TestAnswerBank_ManualFills_UserDoesNothing(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.generate()
	if _, err := e.s.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if e.count("manual_fills")+e.count("answer_events WHERE kind = 'user-corrected'")+e.count("answer_candidates") != 0 {
		t.Fatal("nothing happened, so nothing is recorded")
	}
	run := e.issueRun()
	if err := e.s.RecordSystemWrites(ctx, run, SystemWrite{"#city", "Springfield"}); err != nil {
		t.Fatal(err)
	}
	var ups []Upload
	for i := 0; i < 3; i++ {
		u := fillUpload("jobs.example.com", "City of residence", "Springfield")
		u.RunID, u.Selector, u.At = run, "#city", baseTime.Add(-time.Duration(i+1)*time.Hour)
		u.EntryID, u.Before = "address-city", ""
		ups = append(ups, u)
	}
	e.ingest(ups...)
	e.generate()
	if e.count("manual_fills WHERE ground_truth = 1")+e.count("answer_candidates")+e.count("answer_events WHERE counted = 1") != 0 {
		t.Fatal("the system's own value read back is no evidence of anything")
	}
}

// Deleting a manual fill forgets its upload record too, so the event id is
// accepted again if it is re-posted, and nothing of the fill stays behind.
func TestAnswerBank_ManualFills_DeleteForgetsTheUploadRecord(t *testing.T) {
	e := newEnv(t)
	u := fillUpload("jobs.example.com", "Your current employer "+sentinel, "Acme")
	if res := e.ingest(u); res.Accepted != 1 || len(res.Duplicates) != 0 {
		t.Fatalf("%+v", res)
	}
	if err := e.s.DeleteManualFill(context.Background(), e.fillsList()[0].ID); err != nil {
		t.Fatal(err)
	}
	if holdsSentinel(e) || e.count("learning_uploads") != 0 {
		t.Fatal("the deleted fill left its question behind")
	}
	if res := e.ingest(u); res.Accepted != 1 || len(res.Duplicates) != 0 || e.count("manual_fills") != 1 {
		t.Fatalf("the same event id is accepted again: %+v", res)
	}
}

func TestAnswerBank_ManualFills_ServerDropsSensitiveValue(t *testing.T) {
	e := newEnv(t)
	const secret = "TopSecretValue"
	type probe struct {
		name, question, inputType, auto string
		options                         []string
	}
	for i, p := range []probe{
		{"password", "Choose a password", "password", "", nil},
		{"password by input type alone", "Account entry", "password", "", nil},
		{"one-time code", "Your details", "", "one-time-code", nil},
		{"payment", "Your details", "", "cc-number", nil},
		{"government id", "Social Security Number", "", "", nil},
		{"self-id", "Gender", "", "", []string{"Female", "Male", "Decline to self-identify"}},
		{"date of birth", "Your details", "", "bday", nil},
		{"profile identity", "Your details", "email", "", nil},
		{"profile name", "Your details", "", "name", nil},
	} {
		u := fillUpload("jobs.example.com", p.question, secret+fmt.Sprint(i))
		u.InputType, u.Autocomplete, u.Options = p.inputType, p.auto, p.options
		u.OptionLabel, u.Before, u.EntryID = secret+"label"+fmt.Sprint(i), secret+"before"+fmt.Sprint(i), "phone-mobile"
		if res := e.ingest(u); res.Accepted != 1 {
			t.Fatalf("%s: %+v", p.name, res)
		}
	}
	for _, f := range e.fillsList() {
		if f.Value != nil || f.OptionLabel != nil || f.PriorValue != nil || f.Withheld == WithheldNone {
			t.Errorf("%q: a withheld control stored a value: %+v", f.Question, f)
		}
	}
	if e.dbHolds(secret) {
		t.Fatal("a sentinel value of a withheld control reached the database")
	}
	// The same payload with an unflagged control keeps the value, so the test
	// would notice a classifier that drops everything.
	e.ingest(fillUpload("jobs.example.com", "Preferred pizza topping", secret+"visible"))
	if !e.dbHolds(secret + "visible") {
		t.Fatal("an ordinary value is stored")
	}
}

func TestAnswerBank_ManualFills_CarryNoUrlQueryOrHash(t *testing.T) {
	e := newEnv(t)
	u := fillUpload("https://user:pw@jobs.example.com:8443/apply/42?token=SECRETTOKEN#frag", "Portfolio link, see https://example.org/p?session=SECRETSESSION#top", "https://example.com/me?utm=SECRETUTM#x")
	u.EntryID, u.Before = "linkedin-url", "https://example.com/old?k=SECRETK"
	e.ingest(u)
	for _, bad := range []string{"SECRETTOKEN", "SECRETSESSION", "SECRETUTM", "SECRETK", "#frag", "user:pw", "8443"} {
		if e.dbHolds(bad) {
			t.Errorf("%q reached the database", bad)
		}
	}
	f := e.fillsList()[0]
	if f.Host != "jobs.example.com" || f.Value == nil || *f.Value != "https://example.com/me" {
		t.Fatalf("%+v", f)
	}
}

func TestAnswerBank_ManualFills_ConfidentOverwriteBecomesCorrection(t *testing.T) {
	e := newEnv(t)
	// Scenario "Silent overwrite counts": the engine filled a city from entry
	// city and the user replaced it.
	u := fillUpload("jobs.example.com", "City", "Shelbyville")
	u.Before, u.EntryID = "Springfield", "address-city"
	e.ingest(u)
	counts, err := e.s.CountedCorrections(context.Background())
	if err != nil || counts["address-city"] != 1 {
		t.Fatalf("counts %v, %v", counts, err)
	}
	if n := e.count("answer_events WHERE kind = 'user-corrected' AND entry_id = 'address-city' AND origin = 'manual-fill'"); n != 1 {
		t.Fatalf("%d correction events", n)
	}
	// A fill over an empty field is not an overwrite.
	e.ingest(fillUpload("jobs.example.com", "State", "Example State"))
	// A fill that replaces a value no entry produced has no entry to blame.
	w := fillUpload("jobs.example.com", "Zip", "90210")
	w.Before = "12345"
	e.ingest(w)
	if n := e.count("answer_events WHERE kind = 'user-corrected'"); n != 1 {
		t.Fatalf("%d correction events, want only the attributed one", n)
	}
	// A value whose shape contradicts the label is stored but never counts.
	x := fillUpload("jobs.example.com", "First name", "https://example.com/not-a-name")
	x.Before, x.EntryID = "Alex", "preferred-name"
	e.ingest(x)
	if counts, _ := e.s.CountedCorrections(context.Background()); counts["preferred-name"] != 0 {
		t.Fatalf("a contradicting value counted: %v", counts)
	}
}

func TestAnswerBank_Events_CorrectionOriginRequired(t *testing.T) {
	e := newEnv(t)
	mk := func(id, origin string) Upload {
		return Upload{EventID: eid(id), Kind: UploadCorrection, Origin: origin, Host: "jobs.example.com", Question: "City", ControlKind: "text", Before: "a", After: "b", EntryID: "address-city", At: baseTime}
	}
	res := e.ingest(mk("ok-1", OriginUserEdit), mk("ok-2", OriginManualFill), mk("bad-1", ""), mk("bad-2", "apply-chain"), mk("bad-3", "model"))
	if res.Accepted != 2 || len(res.Rejected) != 3 {
		t.Fatalf("%+v", res)
	}
	for _, r := range res.Rejected {
		if r.Reason != "origin-not-allowed" {
			t.Errorf("%+v", r)
		}
	}
	// An origin on any other kind is refused too.
	u := fillUpload("jobs.example.com", "City", "x")
	u.Origin = OriginUserEdit
	if res := e.ingest(u); len(res.Rejected) != 1 {
		t.Fatalf("%+v", res)
	}
}

func TestAnswerBank_Events_UserReportInvariants(t *testing.T) {
	e := newEnv(t)
	rep := &Report{Note: "wrong field", Tag: "input", Role: "textbox"}
	mk := func(id string) Upload {
		return Upload{EventID: eid(id), Kind: UploadUserReported, Host: "jobs.example.com", Question: "Salary", ControlKind: "text", At: baseTime, Report: rep}
	}
	good := mk("r-ok")
	noReport := mk("r-none")
	noReport.Report = nil
	withCorrection := mk("r-correction")
	withCorrection.After = "100000"
	onWrongKind := fillUpload("jobs.example.com", "Salary", "100000")
	onWrongKind.Report = rep
	onUncertainty := uncertainty("jobs.example.com", "Salary", "no-entry")
	onUncertainty.Report = rep
	res := e.ingest(good, noReport, withCorrection, onWrongKind, onUncertainty)
	want := map[string]string{eid("r-none"): "report-required", eid("r-correction"): "correction-on-report", onWrongKind.EventID: "report-not-allowed", onUncertainty.EventID: "report-not-allowed"}
	if res.Accepted != 1 || len(res.Rejected) != 4 {
		t.Fatalf("%+v", res)
	}
	for _, r := range res.Rejected {
		if want[r.EventID] != r.Reason {
			t.Errorf("%+v, want %q", r, want[r.EventID])
		}
	}
	// A note is never ground truth: no fill and no correction came out of it.
	if e.count("manual_fills")+e.count("answer_events") != 0 {
		t.Fatal("a user report produced evidence")
	}
}

func TestAnswerBank_Events_UserReportSanitized(t *testing.T) {
	e := newEnv(t)
	u := Upload{EventID: eid("rep-1"), Kind: UploadUserReported, Host: "jobs.example.com", Question: "Preferred shift", ControlKind: "text", At: baseTime, Report: &Report{
		Note:       "It put alex@example.com here, call +1 415 555 0100 if unsure",
		Tag:        "input",
		NearbyText: "Contact alex@example.com or +1 415 555 0100",
		URL:        "https://user:pw@jobs.example.com/apply/42?token=abc#frag",
		FrameURL:   "https://frames.example.com/f?session=zzz",
		Snippet:    `<input id="q1" name="phone" type="text" placeholder="Phone">`,
	}}
	e.ingest(u)
	var js string
	if err := e.st.Read().QueryRow(`SELECT report_json FROM learning_uploads WHERE event_id = ?`, eid("rep-1")).Scan(&js); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"nearbyText":"Contact [email] or [phone]"`, `"url":"https://jobs.example.com/apply/42"`, `"frameUrl":"https://frames.example.com/f"`,
		`"note":"It put [email] here, call [phone] if unsure"`} {
		if !strings.Contains(js, want) {
			t.Errorf("stored report lacks %s:\n%s", want, js)
		}
	}
	for _, bad := range []string{"alex@example.com", "415 555 0100", "token=abc", "frag", "user:pw", "session=zzz"} {
		if e.dbHolds(bad) {
			t.Errorf("%q reached the database", bad)
		}
	}
	// Hostile snippets are rejected, not repaired, and nothing of them is stored.
	hostile := []string{`<img src=x onerror=alert(1)>`, `<input value="typed text">`, `<script>x</script>`, `<div style="a:b">x</div>`}
	var ups []Upload
	for i, s := range hostile {
		h := u
		h.EventID = eid(fmt.Sprintf("rep-bad-%d", i))
		r := *u.Report
		r.Snippet = s
		h.Report = &r
		ups = append(ups, h)
	}
	res := e.ingest(ups...)
	if res.Accepted != 0 || len(res.Rejected) != len(hostile) {
		t.Fatalf("%+v", res)
	}
	for _, r := range res.Rejected {
		if !strings.HasPrefix(r.Reason, "report-rejected") {
			t.Errorf("%+v", r)
		}
	}
	// Lengths are clamped.
	long := u
	long.EventID = eid("rep-long")
	r := *u.Report
	r.Note, r.NearbyText, r.Snippet = strings.Repeat("n", 5000), strings.Repeat("t", 5000), `<p>`+strings.Repeat("é", 4000)+`</p>`
	long.Report = &r
	e.ingest(long)
	var got Report
	var raw string
	if err := e.st.Read().QueryRow(`SELECT report_json FROM learning_uploads WHERE event_id = ?`, eid("rep-long")).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Note) != maxNote || len(got.NearbyText) != maxNearby || len(got.Snippet) > maxSnippet || !utf8.ValidString(got.Snippet) {
		t.Fatalf("lengths %d %d %d", len(got.Note), len(got.NearbyText), len(got.Snippet))
	}
}

func TestAnswerBank_Events_UnsafeControlCarriesNoValues(t *testing.T) {
	e := newEnv(t)
	ok := Upload{EventID: eid("u-ok"), Kind: UploadUnsafeControl, Host: "jobs.example.com", Question: "Sensitive widget", ControlKind: "custom-listbox", At: baseTime, Reason: "unsafe-click"}
	withAfter, withLabel, withBefore, withOptions := ok, ok, ok, ok
	withAfter.EventID, withAfter.After = eid("u-after"), "x"
	withLabel.EventID, withLabel.OptionLabel = eid("u-label"), "x"
	withBefore.EventID, withBefore.Before = eid("u-before"), "x"
	withOptions.EventID, withOptions.Options = eid("u-options"), []string{"a"}
	res := e.ingest(ok, withAfter, withLabel, withBefore, withOptions)
	if res.Accepted != 1 || len(res.Rejected) != 4 {
		t.Fatalf("%+v", res)
	}
	for _, r := range res.Rejected {
		if r.Reason != "unsafe-control-carries-values" {
			t.Errorf("%+v", r)
		}
	}
	if e.count("manual_fills")+e.count("answer_events") != 0 || e.count("learning_uploads") != 1 {
		t.Fatal("an unsafe-control event is only a record of the fact")
	}
}

// A frame the fill could not enter is a record of the fact: tagged iframe, its
// reason one of the three failure outcomes, no values and no options, and the
// iframe tag on any other kind is not a control kind.
func TestAnswerBank_Events_FrameUnreachableIsARecordOfTheFact(t *testing.T) {
	e := newEnv(t)
	ok := Upload{EventID: eid("f-ok"), Kind: UploadFrameUnreachable, Host: "jobs.example.com", Question: "Application form frame could not be reached: https://forms.example.com/apply",
		ControlKind: FrameControlKind, At: baseTime, Reason: "failed-inject"}
	mut := func(id string, f func(*Upload)) Upload { u := ok; u.EventID = eid(id); f(&u); return u }
	cases := []struct {
		u      Upload
		reason string
	}{
		{mut("f-kind", func(u *Upload) { u.ControlKind = "text" }), "invalid-control-kind"},
		{mut("f-reason", func(u *Upload) { u.Reason = "no-entry" }), "invalid-reason"},
		{mut("f-noreason", func(u *Upload) { u.Reason = "" }), "invalid-reason"},
		{mut("f-after", func(u *Upload) { u.After = "x" }), "frame-carries-values"},
		{mut("f-options", func(u *Upload) { u.Options = []string{"a"} }), "frame-carries-values"},
		{mut("f-other", func(u *Upload) { u.Kind, u.Reason = UploadUncertainty, "failed" }), "invalid-control-kind"},
		{mut("f-outcome", func(u *Upload) { u.Kind, u.ControlKind = UploadUncertainty, "text" }), "invalid-reason"},
	}
	ups := []Upload{ok}
	for _, c := range cases {
		ups = append(ups, c.u)
	}
	res := e.ingest(ups...)
	if res.Accepted != 1 || len(res.Rejected) != len(cases) {
		t.Fatalf("%+v", res)
	}
	for i, r := range res.Rejected {
		if r.Reason != cases[i].reason {
			t.Errorf("case %d: %+v, want %s", i, r, cases[i].reason)
		}
	}
	if e.count("manual_fills")+e.count("answer_events") != 0 || e.count("learning_uploads WHERE kind = 'frame-unreachable' AND control_kind = 'iframe' AND reason = 'failed-inject'") != 1 {
		t.Fatal("a frame-unreachable event is kept as an upload record only")
	}
}

// An already-satisfied record is observability only: the upload is held, but no
// abstain is recorded, so it never reaches triage or candidate generation.
func TestAnswerBank_Events_AlreadySatisfiedIsObserveOnly(t *testing.T) {
	e := newEnv(t)
	u := Upload{EventID: eid("sat-1"), Kind: UploadUncertainty, Host: "jobs.example.com", Question: "Years of experience with Go", ControlKind: "text", At: baseTime, Reason: ReasonAlreadySatisfied}
	if res := e.ingest(u); res.Accepted != 1 || len(res.Rejected) != 0 {
		t.Fatalf("%+v", res)
	}
	if e.count("learning_uploads") != 1 || e.count("answer_events")+e.count("manual_fills") != 0 {
		t.Fatal("an already-satisfied record must not become an abstain")
	}
}

func TestAnswerBank_Events_RejectsMalformedWithReason(t *testing.T) {
	e := newEnv(t)
	good := func(id string) Upload {
		return Upload{EventID: eid(id), Kind: UploadUncertainty, Host: "jobs.example.com", Question: "Q one", ControlKind: "text", Reason: "no-entry", At: baseTime}
	}
	mut := func(id string, f func(*Upload)) Upload { u := good(id); f(&u); return u }
	// A pick whose option label is longer than an option may be, as the engine
	// sends it: the options it carries were cut to that length.
	const longPick = "Yes, I am willing to relocate to the office this posting names within three months of an offer, and I understand that the relocation support " +
		"described above covers travel to the new city, temporary housing for the first month and the moving of my household goods"
	// An empty reason marks a shape a sender really emits, which is taken.
	cases := []struct {
		u      Upload
		reason string
	}{
		{mut("bad", func(u *Upload) { u.EventID = "bad id!" }), "invalid-event-id"},
		{mut("k1", func(u *Upload) { u.Kind = "applied" }), "unknown-kind"},
		{mut("h1", func(u *Upload) { u.Host = "not a host" }), "invalid-host"},
		{mut("q1", func(u *Upload) { u.Question = "  " }), "question-required"},
		{mut("q2", func(u *Upload) { u.Question = strings.Repeat("q", maxQuestionRunes+1) }), "question-too-long"},
		{mut("c1", func(u *Upload) { u.ControlKind = "file" }), "invalid-control-kind"},
		{mut("o1", func(u *Upload) { u.Options = make([]string, MaxOptions+1) }), "too-many-options"},
		{mut("o2", func(u *Upload) { u.Options = []string{strings.Repeat("o", maxOptionRunes+1)} }), "option-too-long"},
		{mut("t1", func(u *Upload) { u.At = time.Time{} }), "invalid-time"},
		{mut("t2", func(u *Upload) { u.At = baseTime.Add(48 * time.Hour) }), "time-in-future"},
		{mut("r1", func(u *Upload) { u.ReportNumber = -1 }), "invalid-report-number"},
		{mut("r2", func(u *Upload) { u.Reason = "Not A Reason" }), "invalid-reason"},
		{mut("r3", func(u *Upload) { u.Reason = "" }), "reason-required"},
		// A required control the page shows only after the fill (engine/src/corrections.ts).
		{mut("r4", func(u *Upload) { u.Reason = "revealed-after-run" }), ""},
		// A list without the answer's option or one that would not open
		// (engine/src/apply.ts), and a model draft the engine refused to type
		// (engine/src/answers.ts).
		{mut("r5", func(u *Upload) { u.Reason = "option-not-found" }), ""},
		{mut("r6", func(u *Upload) { u.Reason = "listbox-not-opened" }), ""},
		{mut("r7", func(u *Upload) { u.Reason = "unsafe-to-compose" }), ""},
		{mut("v1", func(u *Upload) { u.After = strings.Repeat("v", maxValueRunes+1) }), "value-too-long"},
		{mut("v2", func(u *Upload) { u.After = "x" }), "values-not-allowed"},
		{mut("l1", func(u *Upload) {
			u.Kind, u.Reason, u.ControlKind, u.TrustedInput, u.RunID, u.After, u.OptionLabel = UploadManualFill, "", "select", true, testRun, longPick, longPick
		}), ""},
		{mut("a1", func(u *Upload) { u.InputType = "te<xt" }), "invalid-control-attribute"},
	}
	var ups []Upload
	for _, c := range cases {
		ups = append(ups, c.u)
	}
	ups = append(ups, good("fine"))
	res := e.ingest(ups...)
	rejected := map[string]string{}
	for _, r := range res.Rejected {
		rejected[r.EventID] = r.Reason
	}
	taken := 1
	for i, c := range cases {
		if c.reason == "" {
			taken++
		}
		if got := rejected[c.u.EventID]; got != c.reason {
			t.Errorf("case %d: got %q, want %q", i, got, c.reason)
		}
	}
	if res.Accepted != taken || len(res.Rejected) != len(ups)-taken {
		t.Fatalf("%+v", res)
	}
	if n := e.count("manual_fills WHERE length(option_label) = ?", maxOptionRunes); n != 1 {
		t.Fatalf("%d fills hold the long pick cut to an option's length", n)
	}
}
