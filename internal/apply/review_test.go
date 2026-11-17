package apply

import (
	"context"
	"strings"
	"testing"
	"time"

	"applysmith/internal/answerbank/bind"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
	"applysmith/internal/llm"
)

func written(sel, label, value string) engine.Row {
	return engine.Row{Selector: sel, Label: label, Outcome: "filled", Value: value, Kind: "text"}
}

func reviewEnv(t *testing.T, reply string) *ladderEnv {
	t.Helper()
	return newLadderEnv(t, fixOwn(), func(task, prompt string) string {
		if task == taskCheck {
			return reply
		}
		return ""
	}, nil)
}

func blocked(ps *pageState, loc string) gates.ReadinessResult {
	return gates.SubmitReadiness(gates.ReadinessInput{Pages: []gates.PageReport{ps.gatesReport(1)}, Final: false, PostingLocation: loc})
}

func TestApply_Review_FlagAtTheBlockingFloorBlocksSubmit(t *testing.T) {
	l := reviewEnv(t, `[{"id":1,"probability":0.70,"reason":"the profile prefers product work"}]`)
	ps := l.run(written("#team", "Preferred team", "Infrastructure"))
	if l.model.count(taskCheck) != 1 {
		t.Fatalf("%d review calls, want one per page", l.model.count(taskCheck))
	}
	if ps.report.Review != "ok" || len(ps.flags) != 1 {
		t.Fatalf("review %q flags %+v", ps.report.Review, ps.flags)
	}
	res := blocked(ps, "")
	if res.Allowed() || res.Code != "review_contradiction" || res.Class != gates.ClassDefect {
		t.Fatalf("verdict %+v", res)
	}
}

// fallbackModel answers as one of the task's fallbacks, not its own model.
type fallbackModel struct{ Model }

func (m fallbackModel) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	resp, err := m.Model.Complete(ctx, req)
	if resp != nil {
		r := *resp
		r.Provider, r.Model = "nvidia", "nvidia/nemotron-3.5-lightning-30b-a3b"
		resp = &r
	}
	return resp, err
}

func TestApply_Review_AFallbackModelsFlagIsRecordedAndNeverBlocks(t *testing.T) {
	l := reviewEnv(t, `[{"id":1,"probability":1.0,"reason":"the profile names no infrastructure work"}]`)
	l.g.r.d.Page.Model = fallbackModel{l.model.router}
	ps := l.run(written("#team", "Preferred team", "Infrastructure"))
	if ps.report.Review != "ok" || len(ps.flags) != 1 {
		t.Fatalf("review %q flags %+v", ps.report.Review, ps.flags)
	}
	if res := blocked(ps, ""); !res.Allowed() {
		t.Fatalf("a fallback's flag blocked: %+v", res)
	}
}

func TestApply_Review_FlagWithoutAProbabilityNeverBlocks(t *testing.T) {
	l := reviewEnv(t, `[{"id":1,"reason":"looks off"}, {"id":1,"probability":0.99}]`)
	ps := l.run(written("#team", "Preferred team", "Infrastructure"))
	if res := blocked(ps, ""); !res.Allowed() || len(ps.flags) != 1 {
		t.Fatalf("flags %+v verdict %+v", ps.flags, res)
	}
}

func TestApply_Review_PostingsOwnOfficeIsDropped(t *testing.T) {
	l := reviewEnv(t, `[{"id":1,"probability":0.95}]`)
	l.s.jd = "Platform Engineer\nLocation: Austin, Texas\nBuild things."
	ps := l.run(written("#loc", "Office", "Austin"))
	if len(ps.flags) != 0 {
		t.Fatalf("the posting's own office was flagged: %+v", ps.flags)
	}
	if !strings.Contains(strings.Join(ps.report.Notes, "\n"), "dropped") {
		t.Fatalf("the dropped flag is not recorded: %v", ps.report.Notes)
	}
	if !strings.Contains(l.model.prompts(taskCheck)[0], "Role location: Austin, Texas") {
		t.Fatal("the prompt does not carry the role location")
	}
}

// Runway: the review model listed a "Yes" it cleared in its own reason, and
// the reviewer knew neither today's date (Two Sigma: an M.S. that ended in May
// read as in progress in November) nor that a value was the form's own pick
// (Citadel: "Engineering" from a list with no Computer Engineering).
func TestApply_Review_AClearedFlagPassesAndThePromptCarriesTodayAndThePicks(t *testing.T) {
	l := reviewEnv(t, `[{"id":1,"probability":0.9,"reason":"The profile says the applicant is in Charleston, so Yes is consistent; no values are contradicted."}]`)
	major := written("#major", "Major", "Engineering")
	major.Kind = "select"
	ps := l.run(written("#us", "Are you currently based in the U.S. or Canada?", "Yes"), major)
	if !blocked(ps, "").Allowed() {
		t.Fatalf("a flag its own reason cleared blocks Submit: %+v", blocked(ps, ""))
	}
	prompt := l.model.prompts(taskCheck)[0]
	if !strings.Contains(prompt, "Today: "+l.g.r.d.Now().Format(time.DateOnly)) || !strings.Contains(prompt, "Major => Engineering (picked from the form's list)") ||
		strings.Contains(prompt, "Yes (picked") {
		t.Fatalf("the review prompt:\n%s", prompt)
	}
}

// Wonderschool (Greenhouse, queue183): "Which of these fields have you worked
// in?" = None, over "Private equity", "Investment banking" and "Consulting",
// was flagged because the profile lists software engineering. The review now
// sees a picked value's list when it holds three to twelve choices; a Yes/No
// pair is left as it was.
func TestApply_Review_APickedValueCarriesItsList(t *testing.T) {
	l := reviewEnv(t, "[]")
	l.g.r.d.Page.Matches.Observe(bind.Request{RunID: "er1"}, bind.Outcome{
		Results: []match.Result{{Selector: "#fields", Outcome: match.OutcomeFilled}, {Selector: "#yn", Outcome: match.OutcomeFilled}},
		Prepared: []match.Question{
			{Selector: "#fields", Options: []match.Option{{Label: "Private equity"}, {Label: "Investment banking"}, {Label: "Consulting"}, {Label: "None"}}},
			{Selector: "#yn", Options: []match.Option{{Label: "Select..."}, {Label: "Yes"}, {Label: "No"}}},
		},
	})
	fields := written("#fields", "Which of these fields have you worked in?", "None")
	fields.Kind = "checkbox"
	yn := written("#yn", "Do you enjoy building products?", "Yes")
	yn.Kind = "select"
	// The fill names the run before the check; this page is checked alone.
	l.s.check(context.Background(), doneResult(fields, yn), &pageState{runID: "er1"})
	prompt := l.model.prompts(taskCheck)[0]
	if !strings.Contains(prompt, "Which of these fields have you worked in? => None (picked from the form's list: Private equity; Investment banking; Consulting; None)") ||
		!strings.Contains(prompt, "Do you enjoy building products? => Yes (picked from the form's list)\n") {
		t.Fatalf("the review prompt:\n%s", prompt)
	}
}

// Wonderschool again: the review, shown the list, flagged "None" with a reason
// that names its choices only as ones the profile lacks. The flag carries the
// list, so the gate reads it as agreeing with the value.
func TestApply_Review_ANoneFlagThatDeniesEveryChoiceIsAdvice(t *testing.T) {
	l := reviewEnv(t, `[{"id":1,"contradicted":true,"probability":0.9,"reason":"The profile shows software engineering and research experience, but no private equity, investment banking, or consulting experience; selecting None contradicts the work history."}]`)
	l.g.r.d.Page.Matches.Observe(bind.Request{RunID: "er1"}, bind.Outcome{
		Results:  []match.Result{{Selector: "#fields", Outcome: match.OutcomeFilled}},
		Prepared: []match.Question{{Selector: "#fields", Options: []match.Option{{Label: "Private equity"}, {Label: "Investment banking"}, {Label: "Consulting"}, {Label: "None"}}}},
	})
	fields := written("#fields", "Which of these fields have you worked in?", "None")
	fields.Kind = "checkbox"
	ps := &pageState{runID: "er1"}
	l.s.check(context.Background(), doneResult(fields), ps)
	if len(ps.flags) != 1 || !blocked(ps, "").Allowed() {
		t.Fatalf("flags %+v, readiness %+v", ps.flags, blocked(ps, ""))
	}
}

// Krea (Ashby, queue192): an essay written from the user's knowledge note on
// the EIS internship named its "Coastal Imaging Lab", and the
// review, shown only the profile's "Example Institute of Sciences", read it as
// a contradiction. A value citing a knowledge section is shown with that
// section; a value citing none is shown alone.
func TestApply_Review_AKnowledgeAnswerCarriesItsNote(t *testing.T) {
	const note = "At the Coastal Imaging Lab (EIS) internship in summer 2023, I was handed a nationwide map-data ingestion job."
	l := reviewEnv(t, "[]")
	l.g.r.d.Page.Knowledge = func(context.Context, string) ([]match.KnowledgeSection, error) {
		return []match.KnowledgeSection{{Ref: "other", Text: "Unrelated."}, {Ref: "behavioral/bq-mining", Text: note}}, nil
	}
	essay := written("#essay", "Tell us about the most challenging project you worked on.", "At the Coastal Imaging Lab I rewrote the matching stage.")
	essay.Kind = "textarea"
	plain := written("#why", "Why Krea?", "I build tools for creators.")
	plain.Kind = "textarea"
	ps := &pageState{runID: "er1", answered: map[string]string{"#essay": ProvenanceKnowledge + "behavioral/bq-mining", "#why": ProvenanceCheck}}
	l.s.check(context.Background(), doneResult(essay, plain), ps)
	prompt := l.model.prompts(taskCheck)[0]
	if !strings.Contains(prompt, "=> At the Coastal Imaging Lab I rewrote the matching stage. (written from the applicant's own note: "+note+")\n") ||
		!strings.Contains(prompt, "Why Krea? => I build tools for creators.\n") || strings.Contains(prompt, "Unrelated.") {
		t.Fatalf("the review prompt:\n%s", prompt)
	}
}

// Clipbook (Ashby): "Are you available to begin full-time employment
// immediately?" = No beside an earliest start a month out was flagged as
// "the earliest start date is <a month out>, not immediately". The review is
// told a Yes or No answers the question before it.
func TestApply_Review_AYesOrNoAnswersItsQuestion(t *testing.T) {
	if system, _ := reviewPrompt(nil, nil, nil, nil, nil, nil, "", time.Time{}); !strings.Contains(system, "A Yes or No answers the question it follows") {
		t.Fatalf("the review is not told how a Yes or No reads:\n%s", system)
	}
}

// Runway again: the review listed the same "Yes" as "which is consistent" at a
// blocking probability; the reply now says whether a listed value is
// contradicted.
func TestApply_Review_AValueListedAsNotContradictedIsNoFlag(t *testing.T) {
	l := reviewEnv(t, `[{"id":1,"contradicted":false,"probability":0.95,"reason":"The profile says the applicant is in Charleston, which is consistent with the answer."},{"id":2,"contradicted":true,"probability":0.9,"reason":"the profile prefers infrastructure"}]`)
	// A residence question written from the record is not reviewed at all
	// (gates.RoleResidenceAnswer), so the cleared row is another question.
	ps := l.run(written("#rel", "Are you willing to relocate for this role?", "Yes"), written("#team", "Preferred team", "Research"))
	if len(ps.flags) != 1 || ps.flags[0].Label != "Preferred team" || blocked(ps, "").Allowed() {
		t.Fatalf("flags %+v", ps.flags)
	}
}

// Gremlin (Greenhouse, queue147): a Yes the matcher wrote from the record and
// the review flagged only as unmentioned is advice; a model's value flagged
// the same way still blocks.
func TestApply_Review_AnAbsenceFlagBlocksOnlyAModelsValue(t *testing.T) {
	const label = "Do you have professional (paid) experience writing Java?"
	reply := `[{"id":1,"probability":0.9,"reason":"The profile does not mention Java experience"}]`
	ps := reviewEnv(t, reply).run(written("#java", label, "Yes"))
	if len(ps.flags) != 1 || !blocked(ps, "").Allowed() {
		t.Fatalf("record-written: flags %+v verdict %+v", ps.flags, blocked(ps, ""))
	}
	model := written("#java", label, "Yes")
	model.Review = true
	ps = reviewEnv(t, reply).run(model)
	if len(ps.flags) != 1 || blocked(ps, "").Allowed() {
		t.Fatalf("model-written: flags %+v", ps.flags)
	}
}

func TestApply_Review_ModelUnavailableOrFailingIsRecordedAndNeverBlocks(t *testing.T) {
	l := reviewEnv(t, `[{"id":1,"probability":0.99}]`)
	l.g.r.d.Page.Model = nil
	ps := l.run(written("#team", "Preferred team", "Infrastructure"))
	if ps.report.Review != "skipped (no-model)" || !blocked(ps, "").Allowed() {
		t.Fatalf("review %q verdict %+v", ps.report.Review, blocked(ps, ""))
	}
	l = reviewEnv(t, `this is not json`)
	ps = l.run(written("#team", "Preferred team", "Infrastructure"))
	if !strings.HasPrefix(ps.report.Review, "failed") || !blocked(ps, "").Allowed() {
		t.Fatalf("review %q", ps.report.Review)
	}
}

func TestApply_Review_ShapeFlagsStandRegardlessOfTheModel(t *testing.T) {
	l := reviewEnv(t, `[]`)
	ps := l.run(written("#why", "If you selected other, please specify which one", "Yes"))
	if len(ps.flags) != 1 || blocked(ps, "").Allowed() {
		t.Fatalf("the deterministic flag did not block: %+v", ps.flags)
	}
}

func TestApply_Review_RowsSentAreTheRowsThisRunWroteWithTheCaps(t *testing.T) {
	rows := []engine.Row{
		written("#name", "Full name", "Alex Example"),
		written("#sig", "Signature", "Alex Example"),
		written("#cert", "I certify the information above is true", "Yes"),
		written("#auth", "Do you require work authorization to work in the country of origin for this position?", "Yes"),
		written("#yrs", "How many years of relevant experience do you possess?", "Less than 1 year"),
		written("#go", "How many years of experience do you have with Go?", "3-5 years"),
		written("#py", "Do you have a minimum of 3+ years of experience coding in Python?", "No"),
		{Selector: "#rb", Label: "Do you have 5+ years of experience with Ruby?", Outcome: "filled", Value: "Yes", Kind: "radio-group", Review: true},
		written("#empty", "Nickname", ""),
		{Selector: "#pre", Label: "Prefilled thing", Outcome: "prefilled", Value: "kept"},
		written("#long", "Describe your work", strings.Repeat("x", 300)),
	}
	for i := 0; i < 50; i++ {
		rows = append(rows, written("#r"+string(rune('a'+i%26))+string(rune('a'+i/26)), "Question number "+strings.Repeat("q", i+1), "answer"))
	}
	l := reviewEnv(t, `[]`)
	l.run(rows...)
	prompt := l.model.prompts(taskCheck)[0]
	for _, banned := range []string{"Full name", "Signature", "I certify", "work authorization", "relevant experience", "coding in Python", "Nickname", "Prefilled thing"} {
		if strings.Contains(prompt, banned) {
			t.Errorf("the review received %q", banned)
		}
	}
	if !strings.Contains(prompt, "experience do you have with Go") {
		t.Error("an experience question about a named skill was not sent")
	}
	if !strings.Contains(prompt, "experience with Ruby") {
		t.Error("a model's answer to a years threshold was not sent")
	}
	if strings.Contains(prompt, strings.Repeat("x", 120)) || !strings.Contains(prompt, strings.Repeat("x", 119)+"…\n") {
		t.Error("a value is not cut to 120 characters with an ellipsis")
	}
	if n := strings.Count(prompt, " => "); n != gates.ReviewMaxRows {
		t.Errorf("%d rows sent, want %d", n, gates.ReviewMaxRows)
	}
	profile := prompt[strings.Index(prompt, "=== PROFILE ==="):strings.Index(prompt, "=== WRITTEN VALUES")]
	if n := strings.Count(profile, "\n") - 1; n > gates.ReviewProfileMaxLines {
		t.Errorf("%d profile lines", n)
	}
}

func TestApply_Review_NothingWrittenCostsNoCall(t *testing.T) {
	l := reviewEnv(t, `[]`)
	ps := l.run(engine.Row{Selector: "#a", Label: "Prefilled", Outcome: "prefilled", Value: "x"})
	if l.model.count(taskCheck) != 0 || !strings.HasPrefix(ps.report.Review, "skipped") {
		t.Fatalf("calls %d review %q", l.model.count(taskCheck), ps.report.Review)
	}
}

// Rockwell lists the profile's two degrees as Education 1 and 2 in its own
// order: a written Degree reaches the review with what its own entry holds,
// so a Masters is read against the school it belongs to.
func TestApply_Review_AnEntryRowCarriesItsOwnEntrysValues(t *testing.T) {
	entry := func(n string) []string { return []string{"My Experience", "Education", "Education " + n} }
	rows := []engine.Row{
		{Selector: "#s1", Label: "School or University", Section: entry("1"), Outcome: "prefilled", Value: "Carver University"},
		{Selector: "#d1", Label: "Degree", Section: entry("1"), Outcome: "filled", Kind: "custom-listbox", OptionLabel: "Masters"},
		{Selector: "#s2", Label: "School or University", Section: entry("2"), Outcome: "prefilled", Value: "University of Westbridge"},
		{Selector: "#d2", Label: "Degree", Section: entry("2"), Outcome: "prefilled", OptionLabel: "Bachelors"},
	}
	l := reviewEnv(t, `[]`)
	l.run(rows...)
	prompt := l.model.prompts(taskCheck)[0]
	var line string
	for _, ln := range strings.Split(prompt, "\n") {
		if strings.Contains(ln, "=> Masters") {
			line = ln
		}
	}
	if !strings.Contains(line, "Carver University") || strings.Contains(line, "Westbridge") {
		t.Fatalf("the Degree line is %q", line)
	}
}

// HPE lists both degrees under one Education heading: the entry a row belongs
// to ends where a label comes round again, so Carver's field of study is never
// read against the school above it.
func TestApply_Review_EntriesUnderOneHeadingEndWhereALabelRepeats(t *testing.T) {
	sec := []string{"Education"}
	rows := []engine.Row{
		{Selector: "#s0", Label: "School or University", Section: sec, Outcome: "prefilled", Value: "University of Westbridge"},
		{Selector: "#d0", Label: "Degree", Section: sec, Outcome: "filled", Kind: "select", OptionLabel: "Bachelors"},
		{Selector: "#f0", Label: "Field of study", Section: sec, Outcome: "filled", Kind: "select", OptionLabel: "Applied Mathematics"},
		{Selector: "#s1", Label: "School or University", Section: sec, Outcome: "prefilled", Value: "Carver University"},
		{Selector: "#d1", Label: "Degree", Section: sec, Outcome: "filled", Kind: "select", OptionLabel: "Masters"},
		{Selector: "#f1", Label: "Field of study", Section: sec, Outcome: "filled", Kind: "select", OptionLabel: "Computer Engineering"},
	}
	l := reviewEnv(t, `[]`)
	l.run(rows...)
	prompt := l.model.prompts(taskCheck)[0]
	for want, other := range map[string]string{"=> Computer Engineering": "Westbridge", "=> Applied Mathematics": "Carver"} {
		var line string
		for _, ln := range strings.Split(prompt, "\n") {
			if strings.Contains(ln, want) {
				line = ln
			}
		}
		if !strings.Contains(line, "same entry") || strings.Contains(line, other) {
			t.Errorf("the %s line is %q", want, line)
		}
	}
}

// Cameo (Greenhouse, live): an Education entry's "Start date year" reaches the
// review under its heading, so the year a school began is never read as the
// applicant's start date.
func TestApply_Review_ADateRowOfAnEntryNamesItsHeading(t *testing.T) {
	l := reviewEnv(t, `[]`)
	l.run(engine.Row{Selector: "#y", Label: "Start date year", Section: []string{"Education"}, Outcome: "filled", Value: "2024"})
	var line string
	for _, ln := range strings.Split(l.model.prompts(taskCheck)[0], "\n") {
		if strings.Contains(ln, "=> 2024") {
			line = ln
		}
	}
	if !strings.Contains(line, "Education section") {
		t.Fatalf("the school year line is %q", line)
	}
}

// ngrok (Greenhouse, live): "require the support of ngrok to maintain that
// authorization" names no word of the review's work-authorization filter, but
// the matcher answered it from the sponsorship entry, whose facts no model is
// shown: the review never receives it, so its doubt cannot block Submit.
func TestApply_Review_AMatcherWorkAuthorizationAnswerIsNeverReviewed(t *testing.T) {
	const label = "Will you, now or in the future, require the support of ngrok to maintain that authorization?"
	own := fixOwn()
	own.Bank = []match.Entry{{ID: "sponsorship-now-or-future", Kind: match.KindFixed, Category: match.CategoryWorkAuthorization, Answer: "Yes"}}
	l := newLadderEnv(t, own, func(task, prompt string) string {
		if task == taskCheck {
			return `[{"id":1,"probability":0.99,"reason":"the profile gives no indication the applicant needs support"}]`
		}
		return ""
	}, nil)
	l.g.r.d.Page.Matches.Observe(bind.Request{RunID: "er1"}, bind.Outcome{
		Results:  []match.Result{{Selector: "#sup", Outcome: match.OutcomeFilled, Answer: "Yes", EntryID: "sponsorship-now-or-future"}},
		Prepared: []match.Question{{Selector: "#sup", Text: label, Kind: match.KindRadioGroup}},
	})
	// The page sets the run id before the check, as page.go does.
	ps := &pageState{runID: "er1"}
	l.s.check(context.Background(), doneResult(written("#sup", label, "Yes"), written("#team", "Preferred team", "Infrastructure")), ps)
	prompt := l.model.prompts(taskCheck)[0]
	if strings.Contains(prompt, "support of ngrok") {
		t.Fatal("the review received the sponsorship answer")
	}
	if !strings.Contains(prompt, "Preferred team") {
		t.Fatal("an ordinary row was not reviewed")
	}
	if len(ps.flags) != 1 || ps.flags[0].Label != "Preferred team" {
		t.Fatalf("flags %+v", ps.flags)
	}
}

// Lyft (Greenhouse, queue165): a menu whose only real option is "Thank you"
// beside its placeholder; a flag on that value is a flag on the only answer
// the control allows. A menu with a second option, another value and a
// selector the matcher never saw are not.
func TestMatchLog_SoleOption(t *testing.T) {
	m := &MatchLog{}
	q := func(sel string, labels ...string) match.Question {
		out := match.Question{Selector: sel}
		for _, l := range labels {
			out.Options = append(out.Options, match.Option{Label: l, Value: l})
		}
		return out
	}
	prepared := []match.Question{q("#ty", "Select...", "Thank you"), q("#two", "Thank you", "No thanks")}
	m.Observe(bind.Request{RunID: "r1"}, bind.Outcome{Results: []match.Result{{Selector: "#ty"}, {Selector: "#two"}}, Prepared: prepared})
	for _, c := range []struct {
		sel, value string
		want       bool
	}{
		{"#ty", "Thank you", true}, {"#ty", " thank you ", true}, {"#ty", "Select...", false},
		{"#two", "Thank you", false}, {"#none", "Thank you", false},
	} {
		if got := m.soleOption("r1", c.sel, c.value); got != c.want {
			t.Errorf("%s %q: got %v", c.sel, c.value, got)
		}
	}
	var nilLog *MatchLog
	if nilLog.soleOption("r1", "#ty", "Thank you") {
		t.Error("a nil log knows no option")
	}
}

// A doubted value marks the fill for the user's review and the fill-only run
// still ends filled; a run that presses Submit stops at the check unpressed.
func TestApply_Review_ADoubtedValueIsMarkedInAFillAndStopsASubmit(t *testing.T) {
	doubt := func(x *senv) {
		x.s.pages[0].rows = []engine.Row{written("#team", "Preferred team", "Infrastructure")}
		x.s.pages[0].flags = []gates.LabeledFlag{{Label: "Preferred team", Flag: gates.Flag{Value: "Infrastructure", Probability: 0.9, HasProbability: true}}}
	}
	fill := newSubmit(t, nil)
	fill.e.Run.Input.Submit = false
	doubt(fill)
	if st := fill.g.r.afterWalk(context.Background(), fill.e, fill.s, t0, fill.wr, nil); st != nil {
		t.Fatalf("a fill stopped on a doubted value: %+v", st)
	}
	if !strings.Contains(fill.e.held, `marked for your review: "Preferred team"`) || len(fill.tab.clicks) != 0 {
		t.Fatalf("held %q clicks %v", fill.e.held, fill.tab.clicks)
	}
	if l := stageOf(fill.e.Run, StageSubmit); !strings.Contains(l.Detail, "marked for your review") {
		t.Fatalf("submit line %+v", l)
	}
	sub := newSubmit(t, nil)
	doubt(sub)
	st := sub.g.r.afterWalk(context.Background(), sub.e, sub.s, t0, sub.wr, nil)
	if st == nil || st.at != StageCheck || !strings.Contains(st.reason, "looks wrong") || len(sub.tab.clicks) != 0 {
		t.Fatalf("a submit run went past a doubted value: %+v clicks %v", st, sub.tab.clicks)
	}
}

// A required statement that no AI helped with the application is the user's
// own to make: a fill-only run leaves it blank, names it, and ends filled; a
// run that presses Submit stops at the check with it unanswered.
func TestApply_Review_AStatementLeftToTheUserEndsAFillFilled(t *testing.T) {
	const ai = "I confirm that I did not use AI to complete this application."
	run := func(submit bool) (*senv, *stop) {
		x := newSubmit(t, nil)
		x.e.Run.Input.Submit = submit
		rows := []engine.Row{written("#why", "Why do you want to work here?", "The product."),
			{Selector: "#ai", Label: ai, Required: true, Outcome: "abstained", Reason: "context-policy", Kind: "checkbox"}}
		x.s.check(context.Background(), doneResult(rows...), x.s.pages[0])
		return x, x.g.r.afterWalk(context.Background(), x.e, x.s, t0, x.wr, nil)
	}
	fill, st := run(false)
	if l := fill.s.pages[0].leftovers; len(l) != 1 || l[0].Reason != ReasonStatementBlank {
		t.Fatalf("leftovers %+v", l)
	}
	if st != nil || !strings.Contains(fill.e.held, `left for your own answer: "`+ai+`"`) || len(fill.tab.clicks) != 0 {
		t.Fatalf("stop %+v held %q", st, fill.e.held)
	}
	sub, st := run(true)
	if st == nil || st.at != StageCheck || !strings.Contains(st.reason, "required and not answered") || len(sub.tab.clicks) != 0 {
		t.Fatalf("a submit run went past a blank statement: %+v", st)
	}
}
