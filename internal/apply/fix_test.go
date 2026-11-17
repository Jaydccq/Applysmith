package apply

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"

	"applysmith/internal/answerbank/bind"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
)

const fixApplicant = `{"candidate":{"fullName":"Alex Example","firstName":"Alex","lastName":"Example","email":"alex@example.com","phone":"+1 555 555 0100"}}`

func fixOwn() Own {
	o := ladderOwn
	o.Facts = map[string]string{
		"skills":        "Go, SQL, Kubernetes",
		"experience.go": "Four years of Go in production",
		"phone.number":  "+1 555 555 0199",
	}
	return o
}

// fixScript answers the planner from a function of its prompt and records the
// order of model calls and page writes.
type order struct {
	mu  sync.Mutex
	log []string
}

func (o *order) add(s string) { o.mu.Lock(); o.log = append(o.log, s); o.mu.Unlock() }

func (o *order) index(s string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	for i, x := range o.log {
		if x == s {
			return i
		}
	}
	return -1
}

func fixScript(o *order, reply func(prompt string) string) func(task, prompt string) string {
	return func(task, prompt string) string {
		if task != taskFix {
			return ""
		}
		o.add("model")
		return reply(prompt)
	}
}

func TestApply_Fix_ThreeStrategiesCheapestFirst(t *testing.T) {
	o := &order{}
	reply := func(prompt string) string {
		return `[{"id":1,"key":"skills","value":"Go"},
		  {"id":1,"foo":"bar"},
		  {"id":2,"key":"experience.go","value":"Four years of Go in production"},
		  {"id":2,"key":"not-a-key","value":"x"}]`
	}
	l := newLadderEnv(t, fixOwn(), fixScript(o, reply), nil)
	l.page.answerFn = func(res []engine.Resolution) {
		for _, r := range res {
			o.add("answer:" + r.Selector)
		}
	}
	// The unreadable control is the widget retry's; the open row is the
	// planner's; the bare "Yes" to an explain box is the flag the planner
	// corrects with the profile's own value.
	l.page.rows = func() []engine.InspectRow {
		return []engine.InspectRow{{Selector: "#flag", Label: "Please explain your Go experience", Value: "Yes"}, {Selector: "#ok", Label: "Which languages do you use?", Value: "Go"}}
	}
	rows := []engine.Row{
		{Selector: "#gone", Label: "Preferred language", Required: true, Outcome: "filled", Value: "Go", OptionLabel: "Go", Kind: "text"},
		abstained("#ok", "Which languages do you use?", "text"),
		{Selector: "#flag", Label: "Please explain your Go experience", Required: true, Outcome: "filled", Value: "Yes", Kind: "text"},
	}
	ps := l.run(rows...)
	// The ladder has already spent its model calls; the planner is the third
	// strategy and runs after the widget retry wrote.
	ps.report.FixPlannerCalls, ps.report.FixAttempts, ps.report.FixFixed = 0, 0, 0
	o.mu.Lock()
	o.log = nil
	o.mu.Unlock()
	l.page.answers = nil
	l.s.fix(context.Background(), doneResult(rows...), ps)
	if ps.report.FixPlannerCalls != 1 {
		t.Fatalf("planner calls %d, want one batched call", ps.report.FixPlannerCalls)
	}
	if a, m := o.index("answer:#gone"), o.index("model"); a < 0 || m < 0 || a > m {
		t.Fatalf("order %v: the widget retry (no model) comes before the planner", o.log)
	}
	if v, ok := answeredValue(l.senv, "#flag"); !ok || v != "Four years of Go in production" {
		t.Fatalf("answers %+v: a flag is corrected with the profile's value byte for byte", l.page.answers)
	}
	if len(ps.flags) != 0 {
		t.Fatalf("flags %+v after the correction", ps.flags)
	}
}

// The engine keeps a value this run wrote unless the write asks to overwrite
// it, so a flagged value's correction asks (Color: the correction came back
// "existing-value" and the run stopped on "the page holds the opposite
// answer").
func TestApply_Fix_AFlaggedValueTheRunWroteIsReplaced(t *testing.T) {
	reply := func(string) string {
		return `[{"id":1,"key":"experience.go","value":"Four years of Go in production"}]`
	}
	l := newLadderEnv(t, fixOwn(), fixScript(&order{}, reply), nil)
	held := "Yes"
	l.page.outcomes = map[string]string{}
	l.page.answerFn = func(res []engine.Resolution) {
		for _, r := range res {
			if !r.Overwrite {
				l.page.outcomes[r.Selector] = "prefilled"
				continue
			}
			held = r.Value
			delete(l.page.outcomes, r.Selector)
		}
	}
	l.page.rows = func() []engine.InspectRow {
		return []engine.InspectRow{{Selector: "#flag", Label: "Please explain your Go experience", Kind: "text", Value: held}}
	}
	rows := []engine.Row{{Selector: "#flag", Label: "Please explain your Go experience", Required: true, Outcome: "filled", Value: "Yes", Kind: "text"}}
	ps := l.run(rows...)
	if len(ps.flags) != 1 {
		t.Fatalf("flags %+v: the bare Yes to an explain box is flagged", ps.flags)
	}
	l.s.fix(context.Background(), doneResult(rows...), ps)
	if held != "Four years of Go in production" || len(ps.notHeld) != 0 || len(ps.flags) != 0 {
		t.Fatalf("page holds %q, notHeld %+v, flags %+v: the correction replaces the run's own value", held, ps.notHeld, ps.flags)
	}
}

// A review flag stays on a row the fix left as it was (Harvey: the verify pass
// of an identity restore dropped every review flag, so a contradicted "Yes,
// I'm based in this location" was ready for Submit). The review judged the
// option's label, a long value cut; a row the fix rewrote leaves the flags.
func TestApply_Fix_AReviewFlagStaysOnARowTheFixLeft(t *testing.T) {
	l := newLadderEnv(t, fixOwn(), fixScript(&order{}, func(string) string { return "[]" }), nil)
	based := "Yes, I’m based in this location and able to work from the office 3 days per week"
	long := strings.Repeat("I led the migration. ", 8)
	rows := []engine.Row{
		{Selector: "#office", Label: "Are you currently based in the listed location?", Required: true, Outcome: "filled", Value: "opt-1", OptionLabel: based, Kind: "radio-group"},
		{Selector: "#story", Label: "Tell us about a project", Required: true, Outcome: "filled", Value: long, Kind: "textarea"},
	}
	l.page.rows = func() []engine.InspectRow {
		return []engine.InspectRow{{Selector: "#office", Label: rows[0].Label, Kind: "radio-group", Value: based}, {Selector: "#story", Label: rows[1].Label, Kind: "textarea", Value: long}}
	}
	ps := l.run(rows...)
	flag := func(label, value string) gates.LabeledFlag {
		return gates.LabeledFlag{Label: label, Flag: gates.Flag{Value: value, Probability: 1, HasProbability: true, Reason: "contradicted"}}
	}
	ps.flags = []gates.LabeledFlag{flag(rows[0].Label, based), flag(rows[1].Label, gates.CutReviewValue(long))}
	l.s.refreshAfterFix(context.Background(), ps)
	if len(ps.flags) != 2 || len(ps.report.Flags) != 2 {
		t.Fatalf("flags %+v, report %v: rows the fix left keep their review flags", ps.flags, ps.report.Flags)
	}
	i := slices.IndexFunc(ps.rows, func(r engine.Row) bool { return r.Selector == "#office" })
	ps.rows[i].Value, ps.rows[i].OptionLabel = "opt-2", "No, I’m not based in this location but willing to relocate"
	l.s.refreshAfterFix(context.Background(), ps)
	if len(ps.flags) != 1 || ps.flags[0].Label != rows[1].Label {
		t.Fatalf("flags %+v: the row the fix rewrote leaves the flags, the other keeps its own", ps.flags)
	}
}

func TestApply_Fix_AValueIsGroundedOnlyWhenTheFactHoldsItAsWords(t *testing.T) {
	if !grounds("Go, SQL, Kubernetes", "go", false) || grounds("Go, SQL", "Rust", false) {
		t.Fatal("a value is grounded when the fact holds it, and only then")
	}
	// Shield AI: the planner's "No" from "relocation: open: willing to
	// relocate to any place not excluded" turned a flagged Yes to No. A value
	// is held as whole words, never as letters inside another word.
	for _, fact := range []string{"open: willing to relocate to any place not excluded", "Applied Mathematics, University of Westbridge"} {
		if grounds(fact, "No", false) {
			t.Fatalf("%q does not hold a No", fact)
		}
	}
	if !grounds("requires sponsorship: No", "No", false) || !grounds("onsite, hybrid, remote", "Hybrid", false) {
		t.Fatal("a value the fact holds as words is grounded")
	}
	if !grounds("Four years of Go", "Four years of Go", true) || grounds("Four years of Go", "four years of go", true) {
		t.Fatal("a flag is corrected only with the profile's value byte for byte")
	}
}

func TestApply_Fix_ForbiddenRowsNeverInPromptIdentityOnPostFix(t *testing.T) {
	o := &order{}
	var seen []string
	reply := func(prompt string) string {
		seen = append(seen, prompt)
		return `[{"id":1,"key":"skills","value":"Go"}]`
	}
	l := newLadderEnv(t, fixOwn(), fixScript(o, reply), func(d *Deps) {
		d.Page.Applicant = func(context.Context) (json.RawMessage, error) { return json.RawMessage(fixApplicant), nil }
	})
	phone := "+1 555 555 0100"
	l.page.rows = func() []engine.InspectRow {
		return []engine.InspectRow{{Selector: "#phone", Label: "Phone number", Value: phone}, {Selector: "#lang", Label: "Which languages do you use?", Value: "Go"}}
	}
	// The write that fixes the language also leaves the phone box holding
	// another number: the identity review reads the page after the fix.
	l.page.answerFn = func(res []engine.Resolution) {
		for _, r := range res {
			if r.Selector == "#lang" {
				phone = "+1 555 555 0177"
			}
		}
	}
	rows := []engine.Row{
		{Selector: "#phone", Label: "Phone number", Required: true, Outcome: "filled", Value: "+1 555 555 0100", OptionLabel: "+1 555 555 0100", Kind: "text"},
		abstained("#gender", "What is your gender identity?", "select"),
		abstained("#sig", "Signature", "text"),
		abstained("#ack", "I certify that the information is true", "checkbox"),
		{Selector: "#emp", Label: "Company name", Required: true, Outcome: "abstained", MatcherReason: "no-entry", Kind: "text", Section: []string{"Work Experience"}},
		abstained("#lang", "Which languages do you use?", "text"),
	}
	l.observe("#gender", "What is your gender identity?", "select", "", "Woman", "Man")
	l.s.pages = nil
	ps := &pageState{}
	res := doneResult(rows...)
	l.s.check(context.Background(), res, ps)
	if len(ps.mismatch) != 0 {
		t.Fatalf("mismatch %+v before the fix", ps.mismatch)
	}
	l.s.fix(context.Background(), res, ps)
	if len(seen) != 1 {
		t.Fatalf("%d planner prompts", len(seen))
	}
	for _, bad := range []string{"Phone number", "gender", "Signature", "certify", "Company name"} {
		if strings.Contains(seen[0], bad) {
			t.Errorf("the planner prompt carries %q:\n%s", bad, seen[0])
		}
	}
	if !strings.Contains(seen[0], "Which languages do you use?") {
		t.Fatalf("prompt:\n%s", seen[0])
	}
	if len(ps.mismatch) != 1 || ps.mismatch[0].Label != "Phone number" {
		t.Fatalf("mismatch %+v: the identity review runs on the post-fix report", ps.mismatch)
	}
	// A page that already failed identity costs nothing.
	phone = "+1 555 555 0177"
	seen = nil
	o2 := &order{}
	l2 := newLadderEnv(t, fixOwn(), fixScript(o2, reply), func(d *Deps) {
		d.Page.Applicant = func(context.Context) (json.RawMessage, error) { return json.RawMessage(fixApplicant), nil }
	})
	l2.page.rows = func() []engine.InspectRow {
		return []engine.InspectRow{{Selector: "#phone", Label: "Phone number", Value: phone}}
	}
	ps2 := &pageState{}
	res2 := doneResult(rows[0], abstained("#lang", "Which languages do you use?", "text"))
	l2.s.check(context.Background(), res2, ps2)
	before := len(l2.page.answers)
	l2.s.fix(context.Background(), res2, ps2)
	if len(ps2.mismatch) != 1 || len(seen) != 0 || len(l2.page.answers) != before || ps2.report.FixLoops != 0 {
		t.Fatalf("mismatch %d prompts %d answers %d loops %d", len(ps2.mismatch), len(seen), len(l2.page.answers)-before, ps2.report.FixLoops)
	}
}

func TestApply_Fix_BoundsAndCleanPageCostsNothing(t *testing.T) {
	o := &order{}
	n := 0
	reply := func(prompt string) string {
		n++
		switch n {
		case 1:
			return `[{"id":1,"key":"skills","value":"Go"},{"id":2,"abstain":true,"reason":"no fact"}]`
		case 2:
			return `[{"id":1,"key":"skills","value":"SQL"}]`
		}
		return `[{"id":1,"key":"skills","value":"Kubernetes"}]`
	}
	l := newLadderEnv(t, fixOwn(), fixScript(o, reply), nil)
	// Answering #a reveals #d; answering #d would reveal #e, which the bound
	// leaves alone.
	revealed := map[string]bool{}
	vals := map[string]string{}
	l.page.answerFn = func(res []engine.Resolution) {
		for _, r := range res {
			vals[r.Selector] = r.Value
			switch r.Selector {
			case "#a":
				revealed["#d"] = true
			case "#d":
				revealed["#e"] = true
			}
		}
	}
	l.page.rows = func() []engine.InspectRow {
		out := []engine.InspectRow{{Selector: "#a", Label: "Which languages do you use?", Required: true, Value: vals["#a"]}, {Selector: "#b", Label: "Which databases do you use?", Required: true}}
		if revealed["#d"] {
			out = append(out, engine.InspectRow{Selector: "#d", Label: "Which stores do you use?", Required: true, Kind: "text", Value: vals["#d"]})
		}
		if revealed["#e"] {
			out = append(out, engine.InspectRow{Selector: "#e", Label: "Which queues do you use?", Required: true, Kind: "text"})
		}
		return out
	}
	rows := []engine.Row{
		abstained("#a", "Which languages do you use?", "text"),
		abstained("#b", "Which databases do you use?", "text"),
	}
	ps := &pageState{}
	res := doneResult(rows...)
	l.s.check(context.Background(), res, ps) // the ladder declines: it has no apply-complete script
	n = 0
	l.s.fix(context.Background(), res, ps)
	if ps.report.FixPlannerCalls != 2 || ps.report.FixLoops != 2 {
		t.Fatalf("planner calls %d loops %d: at most two loops and one call per loop (fixLoops=2)", ps.report.FixPlannerCalls, ps.report.FixLoops)
	}
	left := map[string]bool{}
	for _, lo := range ps.leftovers {
		left[lo.Selector] = true
	}
	if !left["#b"] || !left["#e"] || left["#a"] || left["#d"] {
		t.Fatalf("leftovers %+v", ps.leftovers)
	}
	// A clean page costs zero model calls and zero writes.
	c := newLadderEnv(t, fixOwn(), fixScript(&order{}, reply), nil)
	psc := &pageState{}
	clean := doneResult(engine.Row{Selector: "#x", Label: "First name", Required: true, Outcome: "prefilled", Value: "Alex", Kind: "text"})
	c.s.check(context.Background(), clean, psc)
	before := len(c.page.answers)
	c.s.fix(context.Background(), clean, psc)
	if c.model.count(taskFix) != 0 || len(c.page.answers) != before || psc.report.FixLoops != 0 {
		t.Fatalf("a clean page cost %d calls, %d writes, %d loops", c.model.count(taskFix), len(c.page.answers)-before, psc.report.FixLoops)
	}
}

// A site that restores its own draft (Workable keeps the last values typed in
// its form) shows a name and an email the profile does not hold. The identity
// review only passes the profile's value, so the fix writes it into each box
// the site filled and the run never wrote, once, and the review reads the page
// again. A box the run wrote itself, or one the page will not take, still stops.
func TestApply_Fix_ASiteRestoredIdentityValueIsReplacedByTheProfiles(t *testing.T) {
	for _, c := range []struct {
		name       string
		nameOut    string // the outcome of the first-name row
		readOnly   bool
		writes     int
		mismatches int
	}{
		{"site-filled boxes take the profile's values", "prefilled", false, 1, 0},
		{"a box the run wrote is not rewritten", "filled", false, 1, 1},
		{"a box that refuses the write still stops", "prefilled", true, 1, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			l := newLadderEnv(t, fixOwn(), script("[]", "[]", "[]"), func(d *Deps) {
				d.Page.Applicant = func(context.Context) (json.RawMessage, error) { return json.RawMessage(fixApplicant), nil }
			})
			var mu sync.Mutex
			held := map[string]string{"#first": "Morgan", "#mail": "morgan@example.org"}
			l.page.rows = func() []engine.InspectRow {
				mu.Lock()
				defer mu.Unlock()
				return []engine.InspectRow{{Selector: "#first", Label: "First name", Kind: "text", Value: held["#first"]}, {Selector: "#mail", Label: "Email", Kind: "text", Value: held["#mail"]}}
			}
			l.page.answerFn = func(res []engine.Resolution) {
				mu.Lock()
				defer mu.Unlock()
				for _, r := range res {
					if !(c.readOnly && r.Selector == "#first") {
						held[r.Selector] = r.Value
					}
				}
			}
			res := doneResult(
				engine.Row{Selector: "#first", Label: "First name", Kind: "text", Outcome: c.nameOut, Value: "Morgan"},
				engine.Row{Selector: "#mail", Label: "Email", Kind: "text", Outcome: "prefilled", Value: "morgan@example.org"},
			)
			ps := &pageState{}
			l.s.check(context.Background(), res, ps)
			if len(ps.mismatch) != 2 {
				t.Fatalf("the check found %d mismatches, want both", len(ps.mismatch))
			}
			l.s.fix(context.Background(), res, ps)
			if len(l.page.answers) != c.writes {
				t.Fatalf("%d write batches, want %d", len(l.page.answers), c.writes)
			}
			for _, r := range l.page.answers[0] {
				if r.Selector == "#first" && c.nameOut == "filled" {
					t.Fatal("a value the run wrote itself was rewritten")
				}
			}
			if held["#mail"] != "alex@example.com" {
				t.Fatalf("the email box holds %q", held["#mail"])
			}
			if len(ps.mismatch) != c.mismatches {
				t.Fatalf("mismatches after the fix %+v, want %d", ps.mismatch, c.mismatches)
			}
			if l.model.count(taskFix) != 0 {
				t.Fatal("an identity value cost a model call")
			}
		})
	}
}

// Anduril (Greenhouse, owner workspace): a row the matcher left terminal by its
// flag, not by its reason (a blank entry only the user's answer fills), is
// never put to the planner.
func TestApply_Fix_ATerminalMatchIsNeverPlanned(t *testing.T) {
	o := &order{}
	var seen []string
	reply := func(prompt string) string {
		seen = append(seen, prompt)
		return `[{"id":1,"key":"skills","value":"Go"}]`
	}
	l := newLadderEnv(t, fixOwn(), fixScript(o, reply), func(d *Deps) {
		d.Page.Applicant = func(context.Context) (json.RawMessage, error) { return json.RawMessage(fixApplicant), nil }
	})
	l.page.rows = func() []engine.InspectRow {
		return []engine.InspectRow{{Selector: "#lang", Label: "Which languages do you use?", Value: "Go"}}
	}
	const transcripts = "Are you willing to share your academic transcripts?"
	l.g.r.d.Page.Matches.Observe(bind.Request{RunID: "er1"}, bind.Outcome{
		Results:  []match.Result{{Selector: "#tr", Outcome: match.OutcomeAbstained, Reason: match.ReasonPlaceholderEmpty, Terminal: true}},
		Prepared: []match.Question{{Selector: "#tr", Text: transcripts, Kind: match.KindSelect, Options: []match.Option{{Value: "Yes", Label: "Yes"}, {Value: "No", Label: "No"}}}},
	})
	tr := abstained("#tr", transcripts, "select")
	tr.MatcherReason = string(match.ReasonPlaceholderEmpty)
	ps := &pageState{}
	res := doneResult(tr, abstained("#lang", "Which languages do you use?", "text"))
	l.s.check(context.Background(), res, ps)
	l.s.fix(context.Background(), res, ps)
	if len(seen) != 1 || strings.Contains(seen[0], "transcripts") || !strings.Contains(seen[0], "Which languages do you use?") {
		t.Fatalf("%d planner prompts: %q", len(seen), seen)
	}
}
