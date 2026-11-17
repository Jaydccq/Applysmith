package learn

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/workspace"
)

const yearsQ = "How many years of Go experience do you have?"

// run matches one question against the stamped bank with the real matcher.
func (e *env) run(host, question string, options ...string) match.Result {
	e.t.Helper()
	l, err := e.bank.Bank()
	if err != nil {
		e.t.Fatal(err)
	}
	q := match.Question{Selector: "q", Text: question, Kind: match.KindText}
	if len(options) > 0 {
		q.Kind = match.KindSelect
		for _, o := range options {
			q.Options = append(q.Options, match.Option{Value: o, Label: o})
		}
	}
	resp, err := match.New(l.Bank, match.DefaultStages()).Match(match.Request{Host: host, Today: match.Date{Year: 2031, Month: 3, Day: 4}, Questions: []match.Question{q}})
	if err != nil {
		e.t.Fatal(err)
	}
	return resp.Results[0]
}

func (e *env) correct(entry string, n int) {
	e.t.Helper()
	var ups []Upload
	for i := 0; i < n; i++ {
		uploadSeq++
		ups = append(ups, Upload{EventID: eid(fmt.Sprintf("corr-%d", uploadSeq)), Kind: UploadCorrection, Origin: OriginUserEdit, Host: "jobs.example.com", Question: yearsQ,
			ControlKind: "text", Before: "3", After: fmt.Sprint(4 + i), EntryID: entry, At: baseTime})
	}
	if res := e.ingest(ups...); len(res.Rejected) > 0 {
		e.t.Fatalf("%+v", res.Rejected)
	}
}

func (e *env) evolve(apply bool) []string {
	e.t.Helper()
	ids, err := e.s.Evolve(context.Background(), apply)
	if err != nil {
		e.t.Fatal(err)
	}
	return ids
}

func TestAnswerBank_Lifecycle_ThirdCorrectionMarksStale(t *testing.T) {
	e := newEnv(t)
	e.writeBank(fixedEntry("years-go", "3", []string{yearsQ}))
	e.correct("years-go", 2)
	if ids := e.evolve(true); len(ids) != 0 || e.entry("years-go").State != "" {
		t.Fatalf("two corrections change nothing: %v", ids)
	}
	e.correct("years-go", 1)
	if ids := e.evolve(true); len(ids) != 1 || ids[0] != "years-go" {
		t.Fatalf("%v", ids)
	}
	en := e.entry("years-go")
	if en.State != match.StateStale || en.StaleCorrectionCount != 3 || en.StaleAt == "" {
		t.Fatalf("%+v", en)
	}
	if !strings.Contains(e.bankText(), "stale_correction_count: 3") {
		t.Fatalf("the transition is in the file:\n%s", e.bankText())
	}
}

func TestAnswerBank_Lifecycle_StaleIsOneWay(t *testing.T) {
	e := newEnv(t)
	e.writeBank(
		fixedEntry("years-go", "3", []string{yearsQ}),
		fixedEntry("archived-one", "3", []string{"Archived question?"}, "state: archived"),
	)
	e.correct("years-go", 3)
	e.correct("archived-one", 5)
	e.evolve(true)
	first := e.entry("years-go")
	e.clk.advance(24 * time.Hour)
	e.correct("years-go", 2)
	if ids := e.evolve(true); len(ids) != 0 {
		t.Fatalf("a stale entry is not stamped again: %v", ids)
	}
	if again := e.entry("years-go"); again.StaleAt != first.StaleAt || again.StaleCorrectionCount != 3 {
		t.Fatalf("%+v vs %+v", again, first)
	}
	if a := e.entry("archived-one"); a.State != match.StateArchived || a.StaleAt != "" {
		t.Fatalf("an archived entry is never touched: %+v", a)
	}
	// Nothing automatic archives, whatever the counts.
	e.correct("years-go", 20)
	e.evolve(true)
	if e.entry("years-go").State != match.StateStale {
		t.Fatal("automatic processes never archive")
	}
}

func TestAnswerBank_Lifecycle_PinnedNeverChangesAutomatically(t *testing.T) {
	e := newEnv(t)
	e.writeBank(fixedEntry("years-go", "3", []string{yearsQ}, "pinned: true"))
	e.correct("years-go", 3)
	// Scenario "Pinned entry": three corrections, state stays active.
	if ids := e.evolve(true); len(ids) != 0 {
		t.Fatalf("%v", ids)
	}
	if ids := e.evolve(false); len(ids) != 0 {
		t.Fatalf("a dry run reports nothing for a pinned entry either: %v", ids)
	}
	if en := e.entry("years-go"); en.EffectiveState() != match.StateActive || en.StaleAt != "" || !en.Pinned {
		t.Fatalf("%+v", en)
	}
	// Candidate approval does not append to it either (covered with the approval
	// tests); the agent's add path never targets an existing entry.
}

func TestAnswerBank_Lifecycle_UsageReadDoesNotWriteBank(t *testing.T) {
	e := newEnv(t)
	e.writeBank(fixedEntry("years-go", "3", []string{yearsQ}))
	e.correct("years-go", 5)
	path := e.ws.Path(workspace.FileAnswerBank)
	text, st0 := e.bankText(), mustStat(t, path)
	for i := 0; i < 3; i++ {
		if _, err := e.s.Usage(context.Background(), UsageWindow); err != nil {
			t.Fatal(err)
		}
		if _, err := e.s.Candidates(context.Background(), ""); err != nil {
			t.Fatal(err)
		}
		if _, err := e.s.CountedCorrections(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if e.bankText() != text || !mustStat(t, path).ModTime().Equal(st0.ModTime()) {
		t.Fatal("a read changed answer-bank.yml")
	}
	if e.entry("years-go").State != "" {
		t.Fatal("five corrections and no maintenance run: the entry is untouched")
	}
}

func mustStat(t *testing.T, p string) os.FileInfo {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}

func TestAnswerBank_Lifecycle_OutcomeNeverStales(t *testing.T) {
	e := newEnv(t)
	e.writeBank(fixedEntry("years-go", "3", []string{yearsQ}))
	one := 0.95
	for i := 0; i < 10; i++ {
		for _, outcome := range []string{"rejected", "interview", "offer"} {
			e.record(Event{Kind: KindApplied, At: baseTime, Host: "jobs.example.com", Question: yearsQ, ControlKind: "text", EntryID: "years-go", Outcome: outcome,
				Confidence: &one, Lane: LaneAnswerBank, ReportNumber: int64(i + 1)})
		}
	}
	if ids := e.evolve(true); len(ids) != 0 || e.entry("years-go").State != "" {
		t.Fatalf("an application outcome moves nothing: %v", ids)
	}
}

func TestAnswerBank_Lifecycle_AgentCreatedNotCurated(t *testing.T) {
	e := newEnv(t)
	const example = "Which communication channel do you prefer for interview scheduling and application updates throughout the entire hiring process"
	const question = example + " at this company"
	// A curated example drives the long-example lane; an entry a candidate
	// approval or the agent created is flagged for review and does not.
	e.writeBank(fixedEntry("channel", "Email", []string{example}))
	if r := e.run("jobs.example.com", question); r.EntryID != "channel" {
		t.Fatalf("a curated long example matches: %+v", r)
	}
	e.writeBank(fixedEntry("channel", "Email", []string{example}, "created_by: agent", "needs_review: true"))
	if r := e.run("jobs.example.com", question); r.EntryID == "channel" && r.Outcome == match.OutcomeFilled {
		t.Fatalf("an agent-created entry gets no curated precedence: %+v", r)
	}
	// Approval writes exactly that marker.
	e.abstains(3, heardQ)
	e.generate()
	d, err := e.approve(e.only(StatePending).ID)
	if err != nil {
		t.Fatal(err)
	}
	if en := e.entry(d.EntryID); en.CreatedBy != match.CreatedByAgent || !en.NeedsReview {
		t.Fatalf("%+v", en)
	}
}

func TestAnswerBank_AddEntry_DuplicateOutcomes(t *testing.T) {
	e := newEnv(t)
	e.writeBank(
		fixedEntry("years-go", "3", []string{yearsQ}),
		fixedEntry("review-me", "Yes", []string{"Do you like review needed entries?"}, "needs_review: true"),
	)
	add := func(q, a string) error {
		_, err := e.s.AddEntry(context.Background(), AddEntryRequest{Question: q, Answer: a, Host: "jobs.example.com"})
		return err
	}
	if r := e.run("jobs.example.com", "Do you like review needed entries?"); r.Outcome != match.OutcomeFilledReview {
		t.Fatalf("fixture: %+v", r)
	}
	text := e.bankText()
	// Already answered with outcome filled: refused, and nothing is written.
	asConflict(t, add(yearsQ, "4"), ReasonAlreadyAnswered)
	if e.bankText() != text {
		t.Fatal("a refused add wrote the bank")
	}
	// Answered for review: the user's answer replaces the suggestion.
	if err := add("Do you like review needed entries?", "No"); err != nil {
		t.Fatal(err)
	}
	// Without an answer nothing is written.
	asConflict(t, add("Brand new question", " "), ReasonConfirmationRequired)
	if err := add("Brand new question", "A"); err != nil {
		t.Fatal(err)
	}
}

func TestAnswerBank_AddEntry_CapturedScopeShape(t *testing.T) {
	e := newEnv(t)
	res, err := e.s.AddEntry(context.Background(), AddEntryRequest{Question: "Which office would you join?", Answer: "Remote", Host: "Jobs.Example.Test.",
		Options: []string{"Remote", "On-site"}})
	if err != nil {
		t.Fatal(err)
	}
	en := e.entry(res.EntryID)
	if en.Kind != match.KindPickOption || len(en.Preferred) != 1 || en.Preferred[0] != "Remote" || en.FallbackText != "Remote" || en.State != match.StateActive ||
		en.CreatedBy != match.CreatedByUser || en.NeedsReview || en.CreatedAt != baseTime.Format(time.RFC3339) || len(en.Examples) != 1 || en.Examples[0] != "Which office would you join?" ||
		en.Description != "Which office would you join?" {
		t.Fatalf("%+v", en)
	}
	if len(en.CapturedScope) != 1 || en.CapturedScope[0].Host != "jobs.example.test" || en.CapturedScope[0].Question != "Which office would you join?" || len(en.CapturedScope[0].Options) != 2 {
		t.Fatalf("%+v", en.CapturedScope)
	}
	// Scenario "Captured scope": a similar question on another host does not
	// match it, and the same question on its own host does.
	if r := e.run("jobs.example.test", "Which office would you join?", "Remote", "On-site"); r.EntryID != en.ID {
		t.Fatalf("on its host: %+v", r)
	}
	if r := e.run("careers.example.org", "Which office would you join?", "Remote", "On-site"); r.EntryID == en.ID {
		t.Fatalf("another host must not match: %+v", r)
	}
	if r := e.run("jobs.example.test", "Which office would you join?", "Remote", "Hybrid"); r.EntryID == en.ID {
		t.Fatalf("another option set must not match: %+v", r)
	}
	// No options: a fixed entry, still scoped to its host.
	res, err = e.s.AddEntry(context.Background(), AddEntryRequest{Question: "Favourite colour", Answer: "Green", Host: "jobs.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if en = e.entry(res.EntryID); en.Kind != match.KindFixed || en.Answer != "Green" || len(en.CapturedScope) != 1 {
		t.Fatalf("%+v", en)
	}
}

// Everywhere is the user's choice: without it the entry stays scoped to the
// page's host (the route: TestApplyRunQuestions_*).
func TestAnswerBank_AddEntry_EverywhereIsTheUsers(t *testing.T) {
	e := newEnv(t)
	for _, everywhere := range []bool{false, true} {
		res, err := e.s.AddEntry(context.Background(), AddEntryRequest{Question: fmt.Sprint("Favourite season ", everywhere), Answer: "Spring", Host: "jobs.example.test", Everywhere: everywhere})
		if err != nil {
			t.Fatal(err)
		}
		if en := e.entry(res.EntryID); (len(en.CapturedScope) == 0) != everywhere || en.NeedsReview {
			t.Fatalf("everywhere %v: %+v", everywhere, en)
		}
	}
}

// F-4 over the starter bank a workspace is created with: the user's every-site
// answer to a question the matcher left on a blank entry fills that entry; an
// answer for this site only, and an entry no single answer
// fills (a compensation range, the skill-screening gate, which answers every
// skill question from the user's information, F-10) get a new entry and leave
// that one as it was. The every-site answer then answers the question on
// another site, through another control kind where the row gives one (flow
// round 3).
func TestAnswerBank_AddEntry_AUsersEverySiteAnswerFillsTheBlankEntryItsQuestionReached(t *testing.T) {
	cases := []struct {
		name, question, answer string
		options                []string
		everywhere             bool
		blank                  string
		fills                  bool
		// other is the question's option list on another site, a text box when
		// nil; elsewhere is what the bank answers there.
		other     []string
		elsewhere string
	}{
		{"a fixed entry", "County", "Kings", nil, true, "address-county", true, nil, "Kings"},
		{"a pick-option entry", "How did you hear about us?", "LinkedIn", []string{"LinkedIn", "Indeed", "Other"}, true, "referral-source", true, nil, "LinkedIn"},
		{"an answer for this site only", "County", "Kings", nil, false, "address-county", false, nil, ""},
		{"a compensation range", "Desired salary", "120000", nil, true, "salary-expectation", false, []string{"100000", "120000"}, "120000"},
		{"the skill-screening gate", "Do you have experience with Northwind Cloud?", "No", []string{"Yes", "No"}, true, "skill-experience-knowledge-gate", false, nil, "No"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			if err := os.WriteFile(e.ws.Path(workspace.FileAnswerBank), answerbank.DefaultBytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			if r := e.run("jobs.example.test", c.question, c.options...); r.Reason != match.ReasonPlaceholderEmpty || r.EntryID != c.blank {
				t.Fatalf("fixture: %+v", r)
			}
			entries := func() int {
				l, err := e.bank.File()
				if err != nil {
					t.Fatal(err)
				}
				return len(l.Bank.Entries)
			}
			n, was := entries(), e.entry(c.blank).Body
			res, err := e.s.AddEntry(context.Background(), AddEntryRequest{Question: c.question, Answer: c.answer, Options: c.options, Host: "jobs.example.test",
				Everywhere: c.everywhere})
			if err != nil {
				t.Fatal(err)
			}
			if c.fills != (res.EntryID == c.blank) || c.fills != (entries() == n) || !c.fills && (!reflect.DeepEqual(e.entry(c.blank).Body, was) || entries() != n+1) {
				t.Fatalf("wrote %q, %d entries (%d before), blank entry %+v", res.EntryID, entries(), n, e.entry(c.blank))
			}
			route, err := e.s.Routes()
			if err != nil {
				t.Fatal(err)
			}
			if got := route.Route("careers.example.org", match.Posting{}, c.question, c.other).Outright(); got.Answer != c.elsewhere || c.elsewhere != "" && got.EntryID != res.EntryID {
				t.Fatalf("on another site: %+v", got)
			}
		})
	}
}

// The user's answer to a stopped run's question answers it on every site
// unless the question is about this employer or posting (F-4).
func TestAnswerBank_NamesCompany(t *testing.T) {
	cases := []struct {
		question, company string
		names             bool
	}{
		{"Which office would you join?", "Northwind Example", false},
		{"How did you hear about us?", "Northwind Example", false},
		{"What is the best way to reach you?", "The Home Example", false},
		{"Why do you want to work at Northwind?", "Northwind Example", true},
		{"Have you applied here before?", "Northwind Example", true},
		{"Why this team?", "Northwind Example", true},
		{"What do you know about our company?", "Northwind Example", true},
		{"Do you want to join us?", "Northwind Example", true},
		{"Were you referred by a current employee?", "Northwind Example", true},
	}
	for _, c := range cases {
		if got := NamesCompany(c.question, c.company); got != c.names {
			t.Errorf("%q at %q: %v", c.question, c.company, got)
		}
	}
}

// A correction counts only when the value the system wrote is present and the
// committed value differs from it.
func TestAnswerBank_Corrections_CountOnlyWhenTheValueChanged(t *testing.T) {
	e := newEnv(t)
	mk := func(label, origin, before, after string) Upload {
		return Upload{EventID: eid(label), Kind: UploadCorrection, Origin: origin, Host: "jobs.example.test", Question: "City", ControlKind: "text",
			Before: before, After: after, EntryID: "address-city", At: baseTime.Add(-time.Hour)}
	}
	for _, c := range []struct {
		name string
		u    Upload
		want int
	}{
		{"no prior value", mk("no-before", OriginUserEdit, "", "Shelbyville"), 0},
		{"no prior value from a manual fill", mk("no-before-mf", OriginManualFill, "", "Shelbyville"), 0},
		{"same value", mk("same", OriginUserEdit, "Springfield", " springfield "), 0},
		{"emptied control", mk("emptied", OriginUserEdit, "Springfield", ""), 1},
		{"changed", mk("changed", OriginUserEdit, "Springfield", "Shelbyville"), 1},
		{"changed from a manual fill", mk("changed-mf", OriginManualFill, "Springfield", "Capital City"), 1},
	} {
		before := e.count("answer_events WHERE entry_id = 'address-city' AND counted = 1")
		if res := e.ingest(c.u); res.Accepted != 1 {
			t.Fatalf("%s: %+v", c.name, res)
		}
		if got := e.count("answer_events WHERE entry_id = 'address-city' AND counted = 1") - before; got != c.want {
			t.Errorf("%s: counted %d, want %d", c.name, got, c.want)
		}
	}
	if e.count("answer_events WHERE entry_id = 'address-city' AND kind = 'user-corrected'") != 6 {
		t.Fatal("every correction is stored, counted or not")
	}
}

// An entry captured on a live form is scoped to a host; without one nothing is
// written rather than an entry that answers everywhere.
func TestAnswerBank_AddEntry_RefusesWithoutHost(t *testing.T) {
	e := newEnv(t)
	for _, host := range []string{"", "  ", "not a host"} {
		text := e.bankText()
		_, err := e.s.AddEntry(context.Background(), AddEntryRequest{Question: "Favourite colour", Answer: "Green", Host: host})
		var refused *ConflictError
		if !errors.As(err, &refused) || refused.Reason != "host-required" {
			t.Fatalf("host %q: %v", host, err)
		}
		if e.bankText() != text {
			t.Fatal("a refused entry wrote the bank")
		}
	}
}

// The keep press (flow round 9, K-1): a value the user changed by hand over
// one an entry wrote becomes that entry's answer at one press, and the entry's
// corrections stop counting. It is offered only where the entry, as the press
// leaves it, answers the question with the value as the user's own answer.
func TestAnswerBank_KeepEdit_MakesTheValueTheEntrysAnswer(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	const workQ, employerQ, skillQ = "Preferred work arrangement", "Current employer", "Do you have experience with Python?"
	arrangements := []string{"Remote", "Hybrid", "On-site"}
	e.writeBank(
		fixedEntry("years-go", "3", []string{yearsQ}, "needs_review: true", "state: stale", `stale_at: "2031-03-01T00:00:00.000Z"`, "stale_correction_count: 3"),
		"  - id: work-mode\n    description: Work arrangement\n    kind: pick-option\n    preferred: [Remote]\n    fallback_text: Remote\n    examples: [\""+workQ+"\"]\n",
		fixedEntry("current-company", "", []string{employerQ}, "placeholder: true"),
		fixedEntry("gender-identity", "Decline to self-identify", []string{"Gender"}),
		fixedEntry("skill-experience-knowledge-gate", "Yes", []string{skillQ}, "skill_mode: profile_skill", "skill_terms: [Python]"),
		fixedEntry("address-city", "Springfield", []string{"City"}),
		fixedEntry("notice-period", "2 weeks", []string{"Notice period"}),
	)
	overridden := strings.Replace(e.bankText(), "overrides: {}\n", "overrides:\n  jobs.example.org:\n    - {id: address-city, answer: Shelbyville}\n", 1)
	if err := os.WriteFile(e.ws.Path(workspace.FileAnswerBank), []byte(overridden), 0o600); err != nil {
		t.Fatal(err)
	}
	e.s.facts = func() match.Facts {
		return match.Facts{Employment: []match.EmploymentFact{{Employer: "Northwind Example", Title: "Engineer", Start: "2029-01"}}}
	}
	other := e.issueRun()
	edit := func(run, host, question, entry, before, after string) Upload {
		u := fillUpload(host, question, after)
		u.Before, u.EntryID, u.RunID = before, entry, run
		return u
	}
	pick := edit(other, "jobs.example.com", workQ, "work-mode", "Remote", "2")
	pick.ControlKind, pick.Options, pick.OptionLabel = "select", arrangements, "Hybrid"
	res := e.ingest(
		edit(testRun, "jobs.example.com", yearsQ, "years-go", "3", "6"),
		edit(testRun, "jobs.example.com", yearsQ, "years-go", "3", "5"),
		edit(testRun, "jobs.example.com", yearsQ, "years-go", "3", "5"),
		pick,
		// Not offered: a value-withheld control, an answer the profile derives,
		// the skill gate, a host its override answers, a value the bank holds.
		edit(testRun, "jobs.example.com", "Gender", "gender-identity", "Decline to self-identify", "Female"),
		edit(testRun, "jobs.example.com", employerQ, "current-company", "Northwind Example", "Contoso Example"),
		edit(testRun, "jobs.example.com", skillQ, "skill-experience-knowledge-gate", "Yes", "No"),
		edit(testRun, "jobs.example.org", "City", "address-city", "Shelbyville", "Capital City"),
		edit(testRun, "jobs.example.com", "Notice period", "notice-period", "1 week", "2 weeks"),
	)
	if res.Accepted != 9 || e.count("answer_events WHERE kind = 'user-corrected' AND counted = 1") != 9 || e.count("manual_fills WHERE withheld <> ''") != 1 {
		t.Fatalf("fixture: nine counted corrections, one of a value-withheld control: %+v", res)
	}
	offered := func(run string) []string {
		t.Helper()
		got, err := e.s.Edits(ctx, run)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, x := range got {
			out = append(out, x.EntryID+"="+x.After)
		}
		slices.Sort(out)
		return out
	}
	if got := offered(""); !slices.Equal(got, []string{"work-mode=Hybrid", "years-go=5", "years-go=6"}) {
		t.Fatalf("offered: %v", got)
	}
	if got := offered(testRun); !slices.Equal(got, []string{"years-go=5", "years-go=6"}) {
		t.Fatalf("a run offers its own edits: %v", got)
	}
	fill := func(entry, value string) int64 {
		t.Helper()
		var id int64
		if err := e.st.Read().QueryRow(`SELECT max(id) FROM manual_fills WHERE entry_id = ? AND coalesce(option_label, value) = ?`, entry, value).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}

	text := e.bankText()
	var refused *ConflictError
	if _, err := e.s.KeepEdit(ctx, fill("current-company", "Contoso Example")); !errors.As(err, &refused) || refused.Reason != ReasonNotKept || e.bankText() != text {
		t.Fatalf("an answer the profile derives is not kept: %v", err)
	}
	if saved, err := e.s.KeepEdit(ctx, fill("years-go", "5")); err != nil || !saved.Changed {
		t.Fatalf("%+v %v", saved, err)
	}
	if r := e.run("jobs.example.com", yearsQ); r.Answer != "5" || r.Outcome != match.OutcomeFilled {
		t.Fatalf("the value answers outright from now on: %+v", r)
	}
	if counts, err := e.s.CountedCorrections(ctx); err != nil || counts["years-go"] != 0 || counts["work-mode"] != 1 {
		t.Fatalf("only the kept entry's corrections stop counting: %v %v", counts, err)
	}
	if got := offered(""); !slices.Equal(got, []string{"work-mode=Hybrid"}) {
		t.Fatalf("the entry's other edit is settled with it: %v", got)
	}
	if _, err := e.s.KeepEdit(ctx, fill("work-mode", "Hybrid")); err != nil {
		t.Fatal(err)
	}
	if r := e.run("jobs.example.com", workQ, arrangements...); r.Answer != "Hybrid" || r.Outcome != match.OutcomeFilled {
		t.Fatalf("a choice keeps the picked option's label: %+v", r)
	}
}

// The keep press's other answer (flow round 10, K2-1): the entry keeps its
// answer, its counted corrections to that value, the one offer, stop counting
// while its others still count, and a stale entry left below three is active
// again as it was written. A second press on the fill, either one, is refused.
func TestAnswerBank_LeaveEdit_SettlesTheOfferAndKeepsTheAnswer(t *testing.T) {
	for _, c := range []struct {
		name    string
		afters  []string // the hand edits of the entry's "3", oldest first; the newest is left
		offered []string // the values offered, and counted, after the press
		stale   bool     // the entry the stale rule moved stays stale
	}{
		{"the edit it names stops counting, the entry's other does not", []string{"6", "5"}, []string{"6"}, false},
		{"an older identical edit is the same offer and goes with it", []string{"5", "5"}, []string{}, false},
		{"a stale entry left below three is active again, its answer unchanged", []string{"6", "7", "5"}, []string{"7", "6"}, false},
		{"a stale entry left with three stays stale", []string{"6", "7", "8", "5"}, []string{"8", "7", "6"}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			ctx := context.Background()
			e.writeBank(fixedEntry("years-go", "3", []string{yearsQ}))
			for _, after := range c.afters {
				u := fillUpload("jobs.example.com", yearsQ, after)
				u.Before, u.EntryID = "3", "years-go"
				e.ingest(u)
			}
			var newest int64
			if err := e.st.Read().QueryRow(`SELECT max(id) FROM manual_fills`).Scan(&newest); err != nil {
				t.Fatal(err)
			}
			written := e.entry("years-go")
			if _, err := e.s.Evolve(ctx, true); err != nil {
				t.Fatal(err)
			}
			moved := e.entry("years-go")
			if (moved.State == match.StateStale) != (len(c.afters) >= match.StaleAfterCorrections) {
				t.Fatalf("the stale rule moves the entry at three corrections: %+v", moved.Meta)
			}
			want := written
			if c.stale {
				want = moved
			}
			wrote := !reflect.DeepEqual(moved, want) // the bank is saved only when the entry changes
			_, saved, err := e.s.LeaveEdit(ctx, newest)
			if got := e.entry("years-go"); err != nil || !reflect.DeepEqual(got, want) || saved.Changed != wrote {
				t.Fatalf("the press leaves %+v %+v, saved %+v: %v", got.Body, got.Meta, saved, err)
			}
			text := e.bankText()
			_, _, again := e.s.LeaveEdit(ctx, newest)
			_, keep := e.s.KeepEdit(ctx, newest)
			if !errors.Is(again, ErrNotFound) || !errors.Is(keep, ErrNotFound) || e.bankText() != text {
				t.Fatalf("a second press is refused and the bank is as it was: %v %v", again, keep)
			}
			got, err := e.s.Edits(ctx, "")
			offered := []string{}
			for _, x := range got {
				offered = append(offered, x.After)
			}
			counts, cerr := e.s.CountedCorrections(ctx)
			if err != nil || cerr != nil || !slices.Equal(offered, c.offered) || counts["years-go"] != len(c.offered) {
				t.Fatalf("offered %v, counted %v: %v %v", offered, counts, err, cerr)
			}
		})
	}
}

// Undo of a keep or leave press puts back what the press changed: the entry as
// it was and the settled corrections counting. An entry changed after the
// press keeps that change, and a press is undone once.
func TestAnswerBank_UndoEdit_PutsBackWhatThePressChanged(t *testing.T) {
	ctx := context.Background()
	for _, keep := range []bool{true, false} {
		e := newEnv(t)
		e.writeBank(fixedEntry("years-go", "3", []string{yearsQ}, "needs_review: true"))
		for _, after := range []string{"6", "7", "5"} { // three: the leave press activates the stale entry
			u := fillUpload("jobs.example.com", yearsQ, after)
			u.Before, u.EntryID = "3", "years-go"
			e.ingest(u)
		}
		if _, err := e.s.Evolve(ctx, true); err != nil {
			t.Fatal(err)
		}
		var fill int64
		if err := e.st.Read().QueryRow(`SELECT max(id) FROM manual_fills`).Scan(&fill); err != nil {
			t.Fatal(err)
		}
		was, counted := e.entry("years-go"), func() int { return e.count("answer_events WHERE counted = 1") }
		wasCounted := counted()
		pressOnce := func() {
			t.Helper()
			var err error
			if keep {
				_, err = e.s.KeepEdit(ctx, fill)
			} else {
				_, _, err = e.s.LeaveEdit(ctx, fill)
			}
			if err != nil || reflect.DeepEqual(e.entry("years-go"), was) || counted() == wasCounted {
				t.Fatalf("keep %v: the press changes the entry and the counts: %v", keep, err)
			}
		}
		pressOnce()
		if _, err := e.s.UndoEdit(ctx, fill); err != nil || !reflect.DeepEqual(e.entry("years-go"), was) || counted() != wasCounted {
			t.Fatalf("keep %v: undo leaves %+v, %d counted: %v", keep, e.entry("years-go").Meta, counted(), err)
		}
		var refused *ConflictError
		if _, err := e.s.UndoEdit(ctx, fill); !errors.As(err, &refused) || refused.Reason != ReasonNotUndoable {
			t.Fatalf("keep %v: a press is undone once: %v", keep, err)
		}
		pressOnce()
		e.writeBank(fixedEntry("years-go", "9", []string{yearsQ}))
		text := e.bankText()
		if _, err := e.s.UndoEdit(ctx, fill); !errors.As(err, &refused) || refused.Reason != ReasonEntryChanged || e.bankText() != text {
			t.Fatalf("keep %v: an entry changed since is refused and kept: %v", keep, err)
		}
	}
}
