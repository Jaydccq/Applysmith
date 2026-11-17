package apply

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
)

// opEnv is a run that stopped on one form page and waits in its kept tab, with
// the fakes of the ladder rig: the scripted tab and page, a routed model that
// the chain would call for its rungs, and the user's own facts.
type opEnv struct {
	*ladderEnv
	rows []engine.Row
	vals map[string]string
	// stopped is set once the snapshot a stopping run leaves has been written.
	stopped bool
	// support is the probability the support check's model gives every answer.
	support string
}

func opRow(sel, label, kind string, options ...string) engine.Row {
	return engine.Row{Selector: sel, Label: label, Kind: kind, Required: true, Outcome: "abstained", MatcherReason: "no-entry", Reason: "abstained", Options: options}
}

// opPage is the form the tab shows: one input per row, a Next, a Submit and a
// Withdraw control.
func opPage(rows []engine.Row) *PageState {
	st := &PageState{URL: "https://careers.example.test/apply", Title: "Apply", Text: "Platform Engineer application. Remote OK for this role.",
		Lines: []string{"Platform Engineer"}, Forms: []FormInfo{{Fields: len(rows)}}}
	for i, r := range rows {
		st.Inputs = append(st.Inputs, InputInfo{I: i, Selector: r.Selector, Tag: "input", Type: "text", Kind: r.Kind, Label: r.Label, Required: true, Form: 0, Options: r.Options})
	}
	st.Controls = []Control{
		{I: 100, Tag: "button", Labels: []string{"Next"}, Form: 0},
		{I: 101, Tag: "button", Labels: []string{"Submit application"}, Form: 0},
		{I: 102, Tag: "button", Labels: []string{"Withdraw application"}, Form: 0},
	}
	return st
}

func newOp(t *testing.T, mod func(*Deps), rows ...engine.Row) *opEnv {
	t.Helper()
	e := &opEnv{rows: rows, vals: map[string]string{}, support: `[{"id":1,"p":0.9}]`}
	e.ladderEnv = newLadderEnv(t, ladderOwn, func(task, prompt string) string {
		switch {
		case task == taskComplete && strings.Contains(prompt, "=== SUPPORT CHECK ==="):
			return e.support
		case task == taskCheck:
			return "[]"
		}
		return "[]"
	}, func(d *Deps) {
		d.Page.Check = CheckTiming{Reread: time.Millisecond, Settle: time.Millisecond}
		if mod != nil {
			mod(d)
		}
	})
	e.tab.set(opPage(rows))
	e.page.fills = []*engine.PageResult{doneResult(rows...)}
	e.page.rows = func() []engine.InspectRow {
		var out []engine.InspectRow
		for _, r := range e.rows {
			out = append(out, engine.InspectRow{Selector: r.Selector, Label: r.Label, Kind: r.Kind, Required: r.Required, Value: e.vals[r.Selector]})
		}
		return out
	}
	e.page.answerFn = func(res []engine.Resolution) {
		for _, r := range res {
			e.vals[r.Selector] = firstNonEmpty(r.OptionLabel, r.Value)
		}
	}
	e.g.env.window = &adoptWindow{tab: e.tab}
	return e
}

func (e *opEnv) input() ResumeInput {
	return ResumeInput{RunID: "run_s", TabID: "kept", Input: Input{URL: pageURL, Caller: gates.CallerAskAgent, Tool: ToolAutofill, ExplicitRequest: true, ConversationID: "chat_1"}}
}

// handOver writes what a run that stopped leaves with its tab: the snapshot of
// what the page holds, which the next call reads the user's changes against.
func (e *opEnv) handOver() {
	run := &Run{ID: "run_s", Input: Input{URL: pageURL}}
	s := &session{r: e.g.r, e: &Exec{r: e.g.r, Run: run, Ctx: context.Background()}, page: e.page}
	s.saveHandoff(context.Background())
	e.stopped = true
}

// do takes the run's tab over for one call, as the API does for a tool call.
func (e *opEnv) do(fn func(*Operation) (string, error)) string {
	e.t().Helper()
	if !e.stopped {
		e.handOver()
	}
	o, err := e.g.r.Operate(context.Background(), e.input())
	if err != nil {
		e.t().Fatalf("Operate: %v", err)
	}
	defer o.Close(context.Background())
	out, err := fn(o)
	if err != nil {
		e.t().Fatalf("operation: %v", err)
	}
	return out
}

func (e *opEnv) fill() string {
	e.t().Helper()
	return e.do(func(o *Operation) (string, error) { return o.Fill(context.Background()) })
}

func (e *opEnv) answer(rows ...AnswerRow) string {
	e.t().Helper()
	return e.do(func(o *Operation) (string, error) { return o.Answer(context.Background(), rows) })
}

func (e *opEnv) press(label string) string {
	e.t().Helper()
	return e.do(func(o *Operation) (string, error) { return o.Press(context.Background(), label) })
}

func (e *opEnv) written(sel string) (string, bool) { return answeredValue(e.senv, sel) }

// The fill the tools make is the chain's fill with no model: the rows the rungs
// would have sent to a model stay open, with what the agent may do about each,
// and no call is made.
func TestOperate_FillMakesNoModelCallAndSaysWhatIsOpenAndWhatIsTheUsers(t *testing.T) {
	e := newOp(t, nil,
		engine.Row{Selector: "#first", Label: "First Name", Kind: "text", Required: true, Outcome: "filled", Value: "Alex"},
		engine.Row{Selector: "#tool", Label: "Favourite tool", Kind: "text", Required: true, Outcome: "filled", Value: "Go"},
		opRow("#why", "Why do you want to work here?", "text"),
		opRow("#mode", "Preferred work mode?", "select", "Remote", "Hybrid", "On-site"),
		opRow("#gender", "What is your gender?", "select", "Male", "Female", "Decline to answer"),
		opRow("#sig", "Signature", "text"))
	e.vals["#first"], e.vals["#tool"] = "Alex", "Go"
	out := e.fill()
	if n := e.model.count(taskComplete) + e.model.count(taskFix) + e.model.count(taskCheck); n != 0 {
		t.Fatalf("%d model calls from the fill", n)
	}
	for _, want := range []string{"4 required still open", "Favourite tool = Go", "#why", "#mode", `options: "Remote" | "Hybrid" | "On-site"`,
		"#gender | " + `"What is your gender?"` + " | select | left to the user (excluded_self_identification)",
		"#sig | " + `"Signature"` + " | text | left to the user (excluded_signature)",
		"First Name = (held)", `Controls apply_press may press: "Next"`, `never pressed by a tool`, `"Submit application"`} {
		if !strings.Contains(out, want) {
			t.Errorf("the page lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Alex") || strings.Contains(out, "Withdraw") {
		t.Errorf("the page shows a held identity value or a Withdraw control:\n%s", out)
	}
}

// Every answer is checked by the code the ladder's rungs use, so a refused row
// is refused for the rungs' own reason and nothing reaches the page.
func TestOperate_AnswerWritesAFactAndLeavesWhatItMayNotTouch(t *testing.T) {
	cases := []struct {
		name  string
		row   engine.Row
		ans   AnswerRow
		mod   func(*Deps)
		setup func(*opEnv)
		// want is a part of the verdict line; wrote is the value that must reach the page, "" for none.
		want, wrote string
	}{
		{"a fact the profile holds grounds a choice", opRow("#q", "Preferred work mode?", "select", "Remote", "Hybrid"), AnswerRow{ID: "#q", Value: "remote", Cite: []string{"fact:preference.work-modes"}}, nil, nil, "written", "Remote"},
		{"a terminal abstain of the matcher", func() engine.Row {
			r := opRow("#q", "Preferred work mode?", "select", "Remote", "Hybrid")
			r.MatcherReason = string(match.ReasonAskMe)
			return r
		}(), AnswerRow{ID: "#q", Value: "Remote", Cite: []string{"fact:preference.work-modes"}}, nil, nil, "left to the user (abstained:", ""},
		{"a field the page does not have", opRow("#q", "Preferred work mode?", "select", "Remote"), AnswerRow{ID: "#elsewhere", Value: "Remote", Cite: []string{"fact:preference.work-modes"}}, nil, nil, "no such field", ""},
		{"a field already answered", engine.Row{Selector: "#q", Label: "Preferred work mode?", Kind: "select", Required: true, Outcome: "filled", Value: "Hybrid", Options: []string{"Remote", "Hybrid"}},
			AnswerRow{ID: "#q", Value: "Remote", Cite: []string{"fact:preference.work-modes"}}, nil, func(e *opEnv) { e.vals["#q"] = "Hybrid" }, "not an open required field", ""},
		{"a field the fill's write failed on", engine.Row{Selector: "#q", Label: "Preferred work mode?", Kind: "select", Required: true, Outcome: "failed", Reason: "write-failed", Options: []string{"Remote", "Hybrid"}},
			AnswerRow{ID: "#q", Value: "Remote", Cite: []string{"fact:preference.work-modes"}}, nil, nil, "write to it failed", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newOp(t, c.mod, c.row)
			if c.setup != nil {
				c.setup(e)
			}
			e.fill()
			out := e.answer(c.ans)
			got, wrote := e.written("#q")
			if !strings.Contains(out, c.want) {
				t.Fatalf("the verdict lacks %q:\n%s", c.want, out)
			}
			if wrote != (c.wrote != "") || got != c.wrote {
				t.Fatalf("the page was written %q (%v), want %q:\n%s", got, wrote, c.wrote, out)
			}
		})
	}
}

// A written answer passes the identity review again, a refused row changes
// nothing, and an answer to a row names the knowledge or entry it cites.
func TestOperate_AnswerWritesWhatPassesAndReviewsIdentityAgain(t *testing.T) {
	e := newOp(t, func(d *Deps) {
		d.Page.Applicant = func(context.Context) (json.RawMessage, error) {
			return json.RawMessage(`{"candidate":{"fullName":"Alex Example","firstName":"Alex","lastName":"Example"}}`), nil
		}
	},
		engine.Row{Selector: "#first", Label: "First Name", Kind: "text", Required: true, Outcome: "filled", Value: "Alex"},
		opRow("#mode", "Preferred work mode?", "select", "Remote", "Hybrid"),
		opRow("#why", "Why do you want to work here?", "text"))
	e.vals["#first"] = "Alex"
	e.fill()
	before := e.g.audits(gates.GateIdentityReview)
	out := e.answer(
		AnswerRow{ID: "#mode", Value: "Hybrid", Cite: []string{"fact:preference.work-modes"}},
		AnswerRow{ID: "#why", Value: "Small teams that ship.", Cite: []string{"fact:invented"}})
	if after := e.g.audits(gates.GateIdentityReview); after != before+1 {
		t.Fatalf("identity reviews went from %d to %d: the page must be reviewed again after a write", before, after)
	}
	if v, ok := e.written("#mode"); !ok || v != "Hybrid" {
		t.Fatalf("the cited answer was not written: %q\n%s", v, out)
	}
	if _, ok := e.written("#why"); ok || !strings.Contains(out, "refused: uncited") {
		t.Fatalf("the badly cited answer was written or not refused:\n%s", out)
	}
	if !strings.Contains(out, "1 of 2 answer(s) written; 1 required field(s) are still open") {
		t.Fatalf("the summary:\n%s", out)
	}
	// A refusal alone writes nothing and reviews nothing again.
	before = e.g.audits(gates.GateIdentityReview)
	e.answer(AnswerRow{ID: "#why", Value: "x", Cite: []string{"fact:invented"}})
	if after := e.g.audits(gates.GateIdentityReview); after != before {
		t.Fatalf("a call that wrote nothing reviewed identity again (%d to %d)", before, after)
	}
}

// A value the user's own words name replaces the one a field holds, written
// over it, but a field left to the user stays theirs and a choice takes only
// one of its options.
func TestOperate_AChangeReplacesAFilledValueButNotTheUsersFields(t *testing.T) {
	e := newOp(t, nil,
		engine.Row{Selector: "#src", Label: "How did you hear about us?", Kind: "select", Required: true, Outcome: "filled", Value: "LinkedIn", Options: []string{"LinkedIn", "Other"}},
		engine.Row{Selector: "#auth", Label: "Are you legally authorized to work in the United States?", Kind: "select", Required: true, Outcome: "filled", Value: "Yes", Options: []string{"Yes", "No"}})
	e.vals["#src"], e.vals["#auth"] = "LinkedIn", "Yes"
	e.fill()
	out := e.answer(AnswerRow{ID: "#auth", Value: "No", Change: true})
	if _, ok := e.written("#auth"); ok || !strings.Contains(out, "left to the user (excluded_work_authorization)") {
		t.Fatalf("a work authorisation answer was changed:\n%s", out)
	}
	if out := e.answer(AnswerRow{ID: "#src", Value: "A friend", Change: true}); !strings.Contains(out, "exact text of one of its options") {
		t.Fatalf("a value that is no option was not refused:\n%s", out)
	}
	out = e.answer(AnswerRow{ID: "#src", Value: "other", Change: true})
	if v, ok := e.written("#src"); !ok || v != "Other" {
		t.Fatalf("the change was not written: %q\n%s", v, out)
	}
	for _, call := range e.page.answers {
		for _, r := range call {
			if r.Selector == "#src" && !(r.Replace && r.Overwrite) {
				t.Fatalf("the change is not written over the held value: %+v", r)
			}
		}
	}
}

// A value the review doubts is corrected the way the fix planner corrects one:
// with a profile fact's own value, and never with a value the call invents.
func TestOperate_AFlaggedValueIsCorrectedOnlyWithAFact(t *testing.T) {
	flagged := engine.Row{Selector: "#q", Label: "Preferred work mode?", Kind: "text", Required: true, Outcome: "filled", Value: "onsite"}
	cases := []struct {
		name  string
		ans   AnswerRow
		wrote string
	}{
		{"the fact's own value", AnswerRow{ID: "#q", Value: "remote, hybrid", Cite: []string{"fact:preference.work-modes"}}, "remote, hybrid"},
		{"no fact", AnswerRow{ID: "#q", Value: "remote, hybrid", Cite: []string{"jd:Remote OK for this role"}}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newOp(t, nil, flagged)
			e.vals["#q"] = "onsite"
			e.fill()
			// The review doubts the value.
			e.do(func(o *Operation) (string, error) {
				o.w.ps.flags = append(o.w.ps.flags, gates.LabeledFlag{Label: flagged.Label, Flag: gates.Flag{Value: "onsite", Probability: 0.9, HasProbability: true}})
				return "", nil
			})
			out := e.answer(c.ans)
			got, wrote := e.written("#q")
			if got != c.wrote || wrote != (c.wrote != "") {
				t.Fatalf("wrote %q (%v), want %q:\n%s", got, wrote, c.wrote, out)
			}
			if c.wrote == "" && !strings.Contains(out, "refused") {
				t.Fatalf("a refused correction is not said to be:\n%s", out)
			}
		})
	}
}

// The press is the walk's: the control is named by its label, the gate reads
// the label again at press time, and Submit, Withdraw and any other control that
// is not navigation are refused whatever the agent names.
func TestOperate_PressGoesThroughTheWalksGate(t *testing.T) {
	twoRows := []engine.Row{opRow("#next", "Anything else?", "text")}
	pageTwo := func() *PageState {
		p := opPage(twoRows)
		p.Title = "Step two"
		return p
	}
	cases := []struct {
		name    string
		label   string
		setup   func(*opEnv)
		clicked string
		want    string
	}{
		{"navigation", "Next", nil, "Next", "Pressed \"Next\""},
		{"Submit", "Submit application", nil, "", "Submit control is pressed only by the submit stage"},
		{"a control that is not on the page", "Finish", nil, "", "no control reads"},
		{"two controls with the label", "Next", func(e *opEnv) {
			st := opPage(e.rows)
			st.Controls = append(st.Controls, Control{I: 103, Tag: "button", Labels: []string{"Next"}, Form: 0})
			e.tab.set(st)
		}, "", "2 controls read"},
		{"a control something covers", "Next", func(e *opEnv) { e.tab.covered = map[int]bool{100: true} }, "", "covered"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newOp(t, nil, opRow("#q", "Preferred work mode?", "select", "Remote"))
			e.tab.onClick = func(t *scriptTab, _ Control) { e.rows = twoRows; t.set(pageTwo()) }
			e.fill()
			if c.setup != nil {
				c.setup(e)
			}
			out := e.press(c.label)
			if !strings.Contains(out, c.want) {
				t.Fatalf("the answer lacks %q:\n%s", c.want, out)
			}
			if got := strings.Join(e.tab.clicks, ","); got != c.clicked {
				t.Fatalf("clicked %q, want %q:\n%s", got, c.clicked, out)
			}
			if c.clicked != "" && !(strings.Contains(out, "titled \"Step two\"") && strings.Contains(out, "Anything else?")) {
				t.Fatalf("the page the press led to is not shown:\n%s", out)
			}
		})
	}
}

// The walk fills a page before it leaves it and ends on an identity mismatch;
// so does a press of the tools.
func TestOperate_PressWaitsForTheFillAndForTheIdentityReview(t *testing.T) {
	e := newOp(t, func(d *Deps) {
		d.Page.Applicant = func(context.Context) (json.RawMessage, error) {
			return json.RawMessage(`{"candidate":{"fullName":"Alex Example","firstName":"Alex","lastName":"Example"}}`), nil
		}
	}, engine.Row{Selector: "#first", Label: "First Name", Kind: "text", Required: true, Outcome: "filled", Value: "Jordan"})
	e.vals["#first"] = "Jordan"
	if out := e.press("Next"); !strings.Contains(out, "not filled in this conversation") || len(e.tab.clicks) != 0 {
		t.Fatalf("a page left before it was filled: %q, clicks %v", out, e.tab.clicks)
	}
	if out := e.fill(); !strings.Contains(out, "identity mismatch") {
		t.Fatalf("the fill does not report the mismatch:\n%s", out)
	}
	if out := e.press("Next"); !strings.Contains(out, "does not match your profile") || len(e.tab.clicks) != 0 {
		t.Fatalf("a page with an identity mismatch was left: %q, clicks %v", out, e.tab.clicks)
	}
}

// The review asks the apply-check model for its independent reading and puts
// the readiness verdict over the page beside the identity review.
func TestOperate_ReviewAsksTheIndependentModel(t *testing.T) {
	e := newOp(t, nil, opRow("#q", "Preferred work mode?", "select", "Remote", "Hybrid"), opRow("#open", "Anything else?", "text"))
	e.fill()
	e.answer(AnswerRow{ID: "#q", Value: "Remote", Cite: []string{"fact:preference.work-modes"}})
	if n := e.model.count(taskCheck); n != 0 {
		t.Fatalf("%d apply-check calls before the review", n)
	}
	out := e.do(func(o *Operation) (string, error) { return o.Review(context.Background()) })
	if n := e.model.count(taskCheck); n != 1 {
		t.Fatalf("%d apply-check calls from the review, want 1", n)
	}
	for _, want := range []string{"Identity review: 0 mismatch(es)", "Independent review of the written values: ok", "not ready", "Anything else?"} {
		if !strings.Contains(out, want) {
			t.Errorf("the review lacks %q:\n%s", want, out)
		}
	}
}

// A call keeps the tab again and records what the page holds as the run's own;
// what the user changed between two calls is theirs and is not written over.
func TestOperate_TheTabIsKeptAgainAndTheUsersEditsStay(t *testing.T) {
	e := newOp(t, nil, opRow("#q", "Preferred work mode?", "select", "Remote", "Hybrid"))
	e.fill()
	if !e.tab.kept {
		t.Fatal("the tab was not kept again after the call")
	}
	// The user types an answer in the tab between two calls.
	e.vals["#q"] = "Hybrid"
	out := e.answer(AnswerRow{ID: "#q", Value: "Remote", Cite: []string{"fact:preference.work-modes"}})
	if v, ok := e.written("#q"); ok {
		t.Fatalf("the user's own answer was written over with %q:\n%s", v, out)
	}
}

// A page that asks for a sign-in or a bot check is the user's: nothing is
// filled, answered or pressed.
func TestOperate_ASignInOrABotCheckIsLeftToTheUser(t *testing.T) {
	botForm := opPage([]engine.Row{opRow("#q", "Preferred work mode?", "select", "Remote")})
	botForm.Title, botForm.Lines = "Just a moment...", []string{"Verify you are human"}
	for name, page := range map[string]*PageState{"a sign-in": loginPage(""), "a bot check": {URL: loginURL, Title: "Just a moment...", Lines: []string{"Verify you are human"}}, "a bot check over the form": botForm} {
		t.Run(name, func(t *testing.T) {
			e := newOp(t, nil, opRow("#q", "Preferred work mode?", "select", "Remote"))
			e.tab.set(page)
			for _, out := range []string{e.fill(), e.answer(AnswerRow{ID: "#q", Value: "Remote", Cite: []string{"fact:skills"}}), e.press("Next")} {
				if !strings.Contains(out, "is the user's") {
					t.Errorf("the page was worked on: %q", out)
				}
			}
			if len(e.tab.clicks) != 0 || len(e.page.answers) != 0 || e.page.nFills != 0 {
				t.Fatalf("clicks %v, answers %v, fills %d", e.tab.clicks, e.page.answers, e.page.nFills)
			}
		})
	}
}

// Without a pin the matcher cannot verify a saved answer, and none is written.
func TestOperate_ASavedAnswerWithoutAMatcherIsRefused(t *testing.T) {
	_, entries := ladderBank(t)
	own := ladderOwn
	own.Bank = entries
	e := newOp(t, func(d *Deps) { d.Page.Own = func(context.Context) (Own, error) { return own, nil } }, opRow("#q", "What is your preferred build system?", "text"))
	e.fill()
	out := e.answer(AnswerRow{ID: "#q", Value: "Bazel", Cite: []string{"bank:build-system"}})
	if _, ok := e.written("#q"); ok || !strings.Contains(out, "bank-unavailable") {
		t.Fatalf("a saved answer was written with no matcher to hold it to the entry:\n%s", out)
	}
}

// An answer can open a follow-up question; the page then lists it as open, so
// the agent does not hand over a page with a required field it never saw.
func TestOperate_AFollowUpTheAnswerOpensIsOpen(t *testing.T) {
	e := newOp(t, nil, opRow("#q", "Preferred work mode?", "select", "Remote", "Hybrid"))
	base := e.page.rows
	e.page.rows = func() []engine.InspectRow {
		rows := base()
		if e.vals["#q"] == "Remote" {
			rows = append(rows, engine.InspectRow{Selector: "#why", Label: "Why remote?", Kind: "text", Required: true})
		}
		return rows
	}
	e.fill()
	out := e.answer(AnswerRow{ID: "#q", Value: "Remote", Cite: []string{"fact:preference.work-modes"}})
	if !strings.Contains(out, "1 of 1 answer(s) written; 1 required field(s) are still open") {
		t.Fatalf("the follow-up is not open:\n%s", out)
	}
}

// The agent's memory of a page serves that page alone, and one call carries at
// most the rungs' page limit of answers.
func TestOperate_APageThatChangedIsNotWrittenFromTheMemoryOfAnother(t *testing.T) {
	e := newOp(t, nil, opRow("#q", "Preferred work mode?", "select", "Remote", "Hybrid"))
	e.fill()
	e.tab.set(opPage([]engine.Row{opRow("#other", "Anything else?", "text")}))
	out := e.answer(AnswerRow{ID: "#q", Value: "Remote", Cite: []string{"fact:preference.work-modes"}})
	if _, ok := e.written("#q"); ok || !strings.Contains(out, "changed since") {
		t.Fatalf("a page that is not the one the fill read was written:\n%s", out)
	}
	e.tab.set(opPage(e.rows))
	var rows []AnswerRow
	for i := 0; i <= maxAnswerRows; i++ {
		rows = append(rows, AnswerRow{ID: "#q", Value: "Remote", Cite: []string{"fact:preference.work-modes"}})
	}
	if out := e.answer(rows...); !strings.Contains(out, "at most 25 answers") {
		t.Fatalf("a call over the limit:\n%s", out)
	}
	if _, ok := e.written("#q"); ok {
		t.Fatal("a call over the limit wrote")
	}
}
