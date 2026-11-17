package apply

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/bind"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
	"applysmith/internal/lazyregexp"
	"applysmith/internal/workspace"
)

const groundedJD = "Remote OK for this role. Salary range 140000 to 170000 per year."

var ladderOwn = Own{
	Facts: map[string]string{
		"skills":                "Go, SQL, Kubernetes",
		"preference.work-modes": "remote, hybrid",
		"salary.target":         "150000",
		"visa.status":           "a work status",
		"name.full":             "Alex Example",
	},
	CV:    "Alex Example\nBuilt a metrics service in Go.\nSkills: Go, SQL",
	Voice: "Plain and direct.",
}

// script answers the three kinds of ladder call from the user part of the
// prompt.
// promptQuestionRE is a numbered question line of a grounded prompt.
var promptQuestionRE = lazyregexp.New(`(?m)^\d+\. `)

func script(grounded, support, composed string) func(task, prompt string) string {
	return func(task, prompt string) string {
		if task != taskComplete {
			return ""
		}
		switch {
		case strings.Contains(prompt, "=== FACTS ==="):
			return grounded
		case strings.Contains(prompt, "=== SUPPORT CHECK ==="):
			return support
		case strings.Contains(prompt, "=== COMPOSE ==="):
			return composed
		}
		return ""
	}
}

func TestApply_Ladder_PageTheBankCoversMakesNoModelCall(t *testing.T) {
	m := &fakeMatcher{reply: func(req engine.MatchRequest) engine.MatchResponse {
		var out engine.MatchResponse
		for range req.Questions {
			out.Results = append(out.Results, engine.Resolution{Outcome: "filled", Value: "Alex", OptionLabel: "Alex"})
		}
		return out
	}}
	l := newLadderEnv(t, ladderOwn, script("[]", "[]", "[]"), func(d *Deps) { d.Page.Matcher = m })
	ps := l.run(engine.Row{Selector: "#a", Label: "Preferred first name", Required: true, Outcome: "skipped", Reason: "no-answer", Kind: "text"})
	if len(ps.leftovers) != 0 || !ps.report.Ladder.Covered || ps.report.Ladder.Rung1 != 1 || ps.report.Ladder.Calls != 0 {
		t.Fatalf("ladder %+v leftovers %+v", ps.report.Ladder, ps.leftovers)
	}
	if n := l.model.count(taskComplete); n != 0 {
		t.Fatalf("%d ladder calls for a page the bank covers", n)
	}
}

func TestApply_Ladder_Rung1BatchesSkippedFailedOnlyNeverAbstained(t *testing.T) {
	m := &fakeMatcher{reply: func(req engine.MatchRequest) engine.MatchResponse {
		out := engine.MatchResponse{}
		for _, q := range req.Questions {
			if q.Selector == "#a" {
				out.Results = append(out.Results, engine.Resolution{Outcome: "filled", Value: "Alex"})
			} else {
				out.Results = append(out.Results, engine.Resolution{Outcome: "abstained", Reason: "no-entry"})
			}
		}
		return out
	}}
	l := newLadderEnv(t, ladderOwn, script("[]", "[]", "[]"), func(d *Deps) { d.Page.Matcher = m })
	rows := []engine.Row{
		{Selector: "#a", Label: "Nickname", Required: true, Outcome: "skipped", Kind: "text"},
		{Selector: "#b", Label: "Pronunciation", Required: true, Outcome: "failed", Reason: "write-failed", Kind: "text"},
		{Selector: "#c", Label: "Describe your best project", Required: true, Outcome: "abstained", MatcherReason: "no-entry", Kind: "text"},
		{Selector: "#d", Label: "End date", Required: true, Outcome: "skipped", Kind: "text", Section: []string{"Education"}},
	}
	ps := l.run(rows...)
	if len(m.reqs) != 1 {
		t.Fatalf("%d bank requests, want one batch", len(m.reqs))
	}
	var sent []string
	for _, q := range m.reqs[0].Questions {
		sent = append(sent, q.Selector)
	}
	if strings.Join(sent, ",") != "#a,#b,#d" {
		t.Fatalf("sent %v: skipped and failed rows go, the abstained one never (a graduation date is still the bank's)", sent)
	}
	if v, ok := answeredValue(l.senv, "#a"); !ok || v != "Alex" {
		t.Fatalf("answers %+v", l.page.answers)
	}
	got := map[string]string{}
	for _, lo := range ps.leftovers {
		got[lo.Selector] = lo.Detail
	}
	if !strings.HasPrefix(got["#c"], "abstained:no-entry") || !strings.HasPrefix(got["#b"], "bank-declined") {
		t.Fatalf("leftover reasons %v", got)
	}
}

// A required listbox no lane claimed reaches the check with the options the
// engine read for its row (ENG-34): the bank is asked with them, and the
// answer is one of them.
func TestApply_Ladder_UnclaimedListboxIsAskedWithItsRowOptions(t *testing.T) {
	m := &fakeMatcher{reply: func(req engine.MatchRequest) engine.MatchResponse {
		out := engine.MatchResponse{}
		for _, q := range req.Questions {
			if len(q.Options) == 2 {
				out.Results = append(out.Results, engine.Resolution{Outcome: "filled", Value: "Yes", OptionLabel: "Yes"})
			} else {
				out.Results = append(out.Results, engine.Resolution{Outcome: "abstained", Reason: "no-entry"})
			}
		}
		return out
	}}
	l := newLadderEnv(t, ladderOwn, script("[]", "[]", "[]"), func(d *Deps) { d.Page.Matcher = m })
	l.run(engine.Row{Selector: "#ack", Label: "I certify my answers are true", Required: true, Outcome: "skipped", Reason: "no-lane-claimed",
		Kind: "custom-listbox", Options: []string{"Yes", "No"}})
	if len(m.reqs) != 1 || len(m.reqs[0].Questions) != 1 || strings.Join(m.reqs[0].Questions[0].Options, ",") != "Yes,No" {
		t.Fatalf("requests %+v", m.reqs)
	}
	if v, ok := answeredValue(l.senv, "#ack"); !ok || v != "Yes" {
		t.Fatalf("answers %+v", l.page.answers)
	}
}

func TestApply_Ladder_PromptAskedWithoutOptionsIsAskedWithWhatItsMenuHolds(t *testing.T) {
	l := newLadderEnv(t, ladderOwn, script("[]", "[]", "[]"), nil)
	// A Workday search prompt is asked without options; its menu turned out
	// to hold categories the answer was not among.
	l.observe("#src", "How Did You Hear About Us?", match.KindCustomListbox, match.InputSingleSelect)
	row := abstained("#src", "How Did You Hear About Us?", "custom-listbox")
	row.Options = []string{"Career Fair", "Internet Job Board / Job Search"}
	l.run(row)
	prompts := l.model.prompts(taskComplete)
	if len(prompts) == 0 || !strings.Contains(prompts[0], "Internet Job Board / Job Search") {
		t.Fatalf("the grounded question carries no menu options:\n%v", prompts)
	}
}

func TestApply_Ladder_CitationEnforcedByCode(t *testing.T) {
	grounded := `[
	 {"id":1,"answer":"Go and SQL","cite":["fact:skills"]},
	 {"id":2,"answer":"Yes","cite":["jd:remote ok"]},
	 {"id":3,"answer":"Rust","cite":["fact:not-supplied"]},
	 {"id":4,"answer":"Remote is fine","cite":["jd:Remote OK for this role"]},
	 {"id":5,"answer":"Python","cite":["fact:name.full"]}]`
	l := newLadderEnv(t, ladderOwn, script(grounded, `[{"id":1,"p":0.9},{"id":2,"p":0.9}]`, "[]"), nil)
	l.s.jd = groundedJD
	ps := l.run(
		abstained("#1", "Which languages do you work in?", "text"),
		abstained("#2", "Do you work remotely?", "text"),
		abstained("#3", "What other languages do you use?", "text"),
		abstained("#4", "How do you work with distributed teams?", "text"),
		abstained("#5", "Which scripting languages do you use?", "text"),
	)
	if v, ok := answeredValue(l.senv, "#1"); !ok || v != "Go and SQL" {
		t.Fatalf("answers %+v", l.page.answers)
	}
	if v, ok := answeredValue(l.senv, "#4"); !ok || v != "Remote is fine" {
		t.Fatalf("a 12+ character quote that occurs in the excerpt is a citation: %+v", l.page.answers)
	}
	for _, sel := range []string{"#2", "#3"} {
		if _, ok := answeredValue(l.senv, sel); ok {
			t.Errorf("%s was written without a verified citation", sel)
		}
	}
	why := map[string]string{}
	for _, lo := range ps.leftovers {
		why[lo.Selector] = lo.Detail
	}
	if !strings.HasSuffix(why["#2"], reasonUncited) || !strings.HasSuffix(why["#3"], reasonUncited) {
		t.Fatalf("reasons %v: a quote under 12 characters and an unsupplied key are uncited", why)
	}
	// Withheld facts never reach the prompt.
	p := l.model.prompts(taskComplete)[0]
	if strings.Contains(p, "visa.status") || strings.Contains(p, "name.full") || !strings.Contains(p, "skills: Go, SQL, Kubernetes") {
		t.Fatalf("prompt facts:\n%s", p)
	}
}

func TestApply_Ladder_ApplicantClaimsNeedProfileFactCitation(t *testing.T) {
	grounded := `[{"id":1,"answer":"Yes","cite":["jd:Salary range 140000 to 170000"]},
	 {"id":2,"answer":"Yes","cite":["jd:Salary range 140000 to 170000","fact:salary.target"]}]`
	l2 := newLadderEnv(t, ladderOwn, script(grounded, `[{"id":1,"p":0.9}]`, "[]"), nil)
	l2.s.jd = groundedJD
	l2.observe("#1", "Is the compensation band within your expectations?", match.KindSelect, match.InputText, "Yes", "No")
	l2.observe("#2", "Is the compensation band what you are looking for?", match.KindSelect, match.InputText, "Yes", "No")
	ps2 := l2.run(
		abstained("#1", "Is the compensation band within your expectations?", "select"),
		abstained("#2", "Is the compensation band what you are looking for?", "select"),
	)
	why := map[string]string{}
	for _, lo := range ps2.leftovers {
		why[lo.Selector] = lo.Detail
	}
	if !strings.HasSuffix(why["#1"], reasonNeedsProfileFact) {
		t.Fatalf("reasons %v: a posting-only citation for a claim about the applicant is refused", why)
	}
	if v, ok := answeredValue(l2.senv, "#2"); !ok || v != "Yes" {
		t.Fatalf("the honest shape (the band and the applicant's target) is accepted: %+v", l2.page.answers)
	}
}

func TestApply_Ladder_Rung2LimitsAndOptionResolution(t *testing.T) {
	// Option resolution: label first, then a 1-based index, any case.
	opts := []string{"Yes", "No", "Maybe later"}
	for in, want := range map[string]string{"yes": "Yes", " NO ": "No", "3": "Maybe later", "maybe  LATER": "Maybe later"} {
		if got, ok := resolveOption(in, opts); !ok || got != want {
			t.Errorf("%q -> %q %v, want %q", in, got, ok, want)
		}
	}
	if _, ok := resolveOption("4", opts); ok {
		t.Error("an index past the options resolved")
	}
	// A multi-value list for a single control is declined by name; a one-element
	// list is taken; model talk about missing data is declined.
	rows := []*ladderRow{{label: "Q1", options: opts}, {label: "Q2"}, {label: "Q3"}}
	facts := map[string]string{"skills": "Go"}
	got := parseGrounded(`[{"id":1,"answer":["Yes","No"],"cite":"fact:skills"},{"id":2,"answer":["Go"],"cite":["fact:skills"]},{"id":3,"answer":"No information was provided about this","cite":["fact:skills"]}]`, rows, facts, nil, "")
	if len(got) != 1 || got[0].value != "Go" {
		t.Fatalf("answers %+v", got)
	}
	if rows[0].why != reasonMultiValue || rows[2].why != reasonMissingData {
		t.Fatalf("reasons %q %q", rows[0].why, rows[2].why)
	}
	// One call per page, at most 25 rows, one answer per distinct question.
	l := newLadderEnv(t, ladderOwn, script(`[{"id":1,"answer":"Go","cite":["fact:skills"]}]`, `[{"id":1,"p":0.9}]`, "[]"), nil)
	rs := []engine.Row{abstained("#dup1", "Which tools do you use?", "text"), abstained("#dup2", "Which tools do you use?", "text")}
	for i := 0; i < 30; i++ {
		rs = append(rs, abstained("#"+string(rune('a'+i%26))+string(rune('a'+i/26)), "Which languages do you use? "+strings.Repeat("x", i), "text"))
	}
	l.run(rs...)
	prompts := l.model.prompts(taskComplete)
	if len(prompts) < 1 || len(promptQuestionRE.FindAllString(prompts[0], -1)) != maxGroundedRows {
		t.Fatalf("31 distinct questions, at most %d in the prompt:\n%s", maxGroundedRows, prompts[0])
	}
	if n := l.model.count(taskComplete); n != 2 {
		t.Fatalf("%d calls: one grounded call and one support check for the page", n)
	}
	if got := strings.Count(prompts[0], "Which tools do you use?"); got != 1 {
		t.Fatalf("a question asked twice appears %d times in the prompt", got)
	}
}

func TestApply_Ladder_SupportCheckBelowPointSixDiscards(t *testing.T) {
	grounded := `[{"id":1,"answer":"Go","cite":["fact:skills"]},{"id":2,"answer":"Kubernetes","cite":["fact:skills"]}]`
	l := newLadderEnv(t, ladderOwn, script(grounded, `[{"id":1,"p":0.95},{"id":2,"p":0.59}]`, "[]"), nil)
	ps := l.run(abstained("#1", "Which language do you use most?", "text"), abstained("#2", "Which orchestrator do you use?", "text"))
	if _, ok := answeredValue(l.senv, "#2"); ok {
		t.Fatal("an answer below 0.60 was written")
	}
	if v, ok := answeredValue(l.senv, "#1"); !ok || v != "Go" {
		t.Fatalf("answers %+v", l.page.answers)
	}
	if !strings.HasSuffix(ps.leftovers[0].Detail, "support-check p=0.59") || ps.report.Ladder.SupportCheck != "ok" {
		t.Fatalf("leftovers %+v ladder %+v", ps.leftovers, ps.report.Ladder)
	}
	// The check is one unit of the run's budget; with the budget spent the
	// citation-key check alone stands and the page records the skip.
	l2 := newLadderEnv(t, ladderOwn, script(`[{"id":1,"answer":"Go","cite":["fact:skills"]}]`, `[{"id":1,"p":0.1}]`, "[]"), nil)
	for i := 0; i < MaxModelCalls-1; i++ {
		l2.e.budget.take()
	}
	ps2 := l2.run(abstained("#1", "Which language do you use most?", "text"))
	if v, ok := answeredValue(l2.senv, "#1"); !ok || v != "Go" || ps2.report.Ladder.SupportCheck != "skipped (budget-capped)" {
		t.Fatalf("answers %+v ladder %+v", l2.page.answers, ps2.report.Ladder)
	}
	if l2.model.count(taskComplete) != 1 {
		t.Fatalf("%d calls", l2.model.count(taskComplete))
	}
}

func rec(t *testing.T, d *Declines, key, build, rev, run string) bool {
	t.Helper()
	repeat, err := d.Record(context.Background(), key, build, rev, run)
	if err != nil {
		t.Fatal(err)
	}
	return repeat
}

// A decline outlives the process: a memory opened over the same store holds it
// after a restart, expires it after 30 days, and drops it when the build or the
// revision no longer matches.
func TestApply_Ladder_DeclineMemoryIsPersistedAndExpires(t *testing.T) {
	g := newRig(t, nil)
	now := t0
	clock := func() time.Time { return now }
	key := declineKey("careers.example.test", "Which tools do you use?", "text", nil)
	d1 := NewDeclines(g.st, clock)
	if rec(t, d1, key, "v1.2.0", "rev1", "run-a") {
		t.Fatal("one run is not a repeat")
	}

	d2 := NewDeclines(g.st, clock) // the next process
	if d2.Held(key, "v1.2.0", "rev1") {
		t.Fatal("held before the run-start read")
	}
	if err := d2.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !d2.Held(key, "v1.2.0", "rev1") {
		t.Fatal("a decline did not survive the restart")
	}
	if d2.Held(key, "v1.2.0", "rev2") || d2.Held(key, "v1.3.0", "rev1") {
		t.Fatal("a stored decline vouched for another revision or build")
	}
	if !rec(t, d2, key, "v1.2.0", "rev1", "run-b") {
		t.Fatal("a second run after the restart is a repeat")
	}

	now = now.Add(29 * 24 * time.Hour)
	d3 := NewDeclines(g.st, clock)
	if err := d3.Load(context.Background()); err != nil || !d3.Held(key, "v1.2.0", "rev1") {
		t.Fatalf("not reused inside 30 days: %v", err)
	}
	now = now.Add(2 * 24 * time.Hour)
	d4 := NewDeclines(g.st, clock)
	if err := d4.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d4.Held(key, "v1.2.0", "rev1") {
		t.Fatal("reused after 30 days")
	}
	if n := g.count(`SELECT count(*) FROM apply_declines`); n != 0 {
		t.Fatalf("%d expired rows were kept", n)
	}

	// A decline under a new revision replaces the old rows of the question.
	rec(t, d4, key, "v1.2.0", "rev1", "run-c")
	rec(t, d4, key, "v1.2.0", "rev2", "run-d")
	if n := g.count(`SELECT count(*) FROM apply_declines WHERE decline_key = ?1`, key); n != 1 {
		t.Fatalf("%d rows for one question under one revision", n)
	}
}

func TestApply_Ladder_DeclineReuseCleanBuildOnly(t *testing.T) {
	d := NewDeclines(nil, func() time.Time { return t0 })
	dirty := declineKey("careers.example.test", "Other?", "text", nil)
	rec(t, d, dirty, "v1.2.0+dirty", "rev1", "run-a")
	if d.Held(dirty, "v1.2.0+dirty", "rev1") {
		t.Fatal("a build with local modifications vouched")
	}
	// Through the ladder: a held decline makes no call.
	l := newLadderEnv(t, ladderOwn, script(`[{"id":1,"decline":true}]`, "[]", "[]"), func(dd *Deps) {
		dd.Page.Revision = func(context.Context) string { return "rev1" }
		dd.Build = "v1.2.0"
	})
	l.run(abstained("#1", "Which tools do you use?", "text"))
	if l.model.count(taskComplete) != 1 {
		t.Fatal("the first decline is paid for")
	}
	l.run(abstained("#1", "Which tools do you use?", "text"))
	if l.model.count(taskComplete) != 1 {
		t.Fatalf("%d calls: a declined question is not paid for again", l.model.count(taskComplete))
	}
}

// Which questions rung 3 may compose is match.ModelWritten's table
// (TestModelWritten).
func TestApply_Ladder_ComposedRungFabricationScreen(t *testing.T) {
	// A date the CV holds is copied, not invented (Two Sigma's prior experience).
	cv := Own{CV: "Software Engineer Intern, Acme\nJul. 2023 – Sep. 2023"}
	for text, want := range map[string]string{
		"I have a $180K offer elsewhere": reasonFabricated, "Around 180,000 USD": reasonFabricated, "I interviewed in March 3": reasonFabricated,
		"I have 3 other offers": reasonFabricated, "I like building small tools": "",
		"Software Engineer Intern at Acme (Jul 2023 – Sep 2023)": "", "Intern at Acme (July 2023 to September 2023)": "",
		"Intern at Acme (Jul 2023 – Dec 2023)": reasonFabricated, "Intern at Acme since 07/01/2023": reasonFabricated,
	} {
		if got := composedFabricationReason(text, nil, cv); got != want {
			t.Errorf("%q: %q, want %q", text, got, want)
		}
	}
	composed := `[{"id":1,"answer":"I like building small tools that save people time."},{"id":2,"answer":"I have $180K in competing offers."}]`
	l := newLadderEnv(t, ladderOwn, script(`[{"id":1,"decline":true},{"id":2,"decline":true}]`, "[]", composed), nil)
	ps := l.run(abstained("#1", "Why do you want to work at Example Corp?", "text"), abstained("#2", "Why do you want to work here, and do you have other offers?", "text"))
	if v, ok := answeredValue(l.senv, "#1"); !ok || !strings.HasPrefix(v, "I like building") {
		t.Fatalf("answers %+v", l.page.answers)
	}
	if _, ok := answeredValue(l.senv, "#2"); ok || len(ps.leftovers) != 1 || !strings.HasSuffix(ps.leftovers[0].Detail, reasonFabricated) {
		t.Fatalf("leftovers %+v: a figure declines the answer as fabricated and the row stays the user's", ps.leftovers)
	}
	// A long text control may be longer than a short one, and both are cut.
	long := strings.Repeat("word ", 200)
	rows := []*ladderRow{{label: "q", long: false}, {label: "q2", long: true}}
	got := parseComposed(`[{"id":1,"answer":"`+long+`"},{"id":2,"answer":"`+long+`"}]`, rows, Own{})
	if len([]rune(got[0].value)) > shortComposedMax || len([]rune(got[1].value)) > longComposedMax || len(got[1].value) <= len(got[0].value) {
		t.Fatalf("lengths %d %d", len(got[0].value), len(got[1].value))
	}
	// The prompt carries the voice file, the projects at the end of a long CV
	// and the fit text, and no compensation section.
	own := ladderOwn
	own.CV = strings.Repeat("Education and work history. ", 500) + "Flagship metrics service"
	system, user := composedPrompt([]*ladderRow{{label: "Why us?"}}, own, "Role fit", "Engineer", "Example Corp")
	if !contains2(user, "Plain and direct.", "Flagship metrics service", "Role fit") {
		t.Fatalf("prompt:\n%s", user)
	}
	// Owner decision: an experience question is answered, never declined
	// (Humble Robotics' sensor-data project, which the notes do not hold).
	if !strings.Contains(system, "experience is never declined") {
		t.Fatalf("system:\n%s", system)
	}
}

func TestApply_Ladder_ReportFitExcludesCompensationSections(t *testing.T) {
	md := "# Report\n\n## Role Snapshot\nBuilds data tools.\nTarget comp is $190K.\n\n## CV Match & Gaps\nStrong Go.\n\n## Level Strategy\nAsk for senior.\n\n## Compensation\nFloor $170K, walk away below.\n\n## Negotiation\nHold firm.\n\n```json\n{\"floor\":170000}\n```\n\n## Quick Screen Summary\nGood fit.\n"
	fit := ReportFit(md)
	for _, bad := range []string{"170", "190", "Level Strategy", "Compensation", "Negotiation", "floor", "walk away"} {
		if strings.Contains(fit, bad) {
			t.Errorf("fit text carries %q:\n%s", bad, fit)
		}
	}
	for _, good := range []string{"Builds data tools.", "Strong Go.", "Good fit."} {
		if !strings.Contains(fit, good) {
			t.Errorf("fit text lost %q:\n%s", good, fit)
		}
	}
	// And it reaches the composed prompt through the report path, not the file.
	l := newLadderEnv(t, ladderOwn, script(`[{"id":1,"decline":true}]`, "[]", `[{"id":1,"answer":"I enjoy data tools."}]`), func(d *Deps) {
		d.Page.ReportText = func(context.Context, string) (string, error) { return md, nil }
	})
	l.e.Run.Report = &ReportBinding{Number: 7, Path: "reports/007.md", Role: "Engineer", Company: "Example Corp"}
	l.run(abstained("#1", "Why do you want to work here?", "text"))
	p := l.model.prompts(taskComplete)
	last := p[len(p)-1]
	if strings.Contains(last, "170") || !strings.Contains(last, "Builds data tools.") {
		t.Fatalf("composed prompt:\n%s", last)
	}
}

func TestApply_Ladder_BudgetCapAndHeldWrites(t *testing.T) {
	l := newLadderEnv(t, ladderOwn, script(`[{"id":1,"answer":"Go","cite":["fact:skills"]}]`, `[{"id":1,"p":0.9}]`, "[]"), nil)
	for i := 0; i < MaxModelCalls; i++ {
		l.e.budget.take()
	}
	ps := l.run(abstained("#1", "Which language do you use most?", "text"))
	if l.model.count(taskComplete) != 0 || len(ps.leftovers) != 1 || !strings.HasSuffix(ps.leftovers[0].Detail, whyBudgetCapped) {
		t.Fatalf("calls %d leftovers %+v: at the cap the row goes to rung 4 as budget-capped", l.model.count(taskComplete), ps.leftovers)
	}
	// Twelve calls in the run, and the thirteenth is never made.
	if l.e.budget.take() {
		t.Fatal("a thirteenth call was allowed")
	}
	// A write the guard refused because the control already holds an answer
	// counts as held, not as a new write.
	h := newLadderEnv(t, ladderOwn, script(`[{"id":1,"answer":"Go","cite":["fact:skills"]}]`, `[{"id":1,"p":0.9}]`, "[]"), nil)
	h.page.outcomes = map[string]string{"#1": "held"}
	ps2 := h.run(abstained("#1", "Which language do you use most?", "text"))
	if len(ps2.leftovers) != 0 || ps2.report.Ladder.Rung2 != 1 {
		t.Fatalf("leftovers %+v ladder %+v", ps2.leftovers, ps2.report.Ladder)
	}
	// A write the page did not take is not counted and is not a success.
	n := newLadderEnv(t, ladderOwn, script(`[{"id":1,"answer":"Go","cite":["fact:skills"]}]`, `[{"id":1,"p":0.9}]`, "[]"), nil)
	n.page.outcomes = map[string]string{"#1": "failed"}
	ps3 := n.run(abstained("#1", "Which language do you use most?", "text"))
	if len(ps3.leftovers) != 1 || ps3.report.Ladder.Rung2 != 0 {
		t.Fatalf("leftovers %+v ladder %+v", ps3.leftovers, ps3.report.Ladder)
	}
	// A dead model keeps the run going.
	dead := newLadderEnv(t, ladderOwn, func(string, string) string { return "" }, nil)
	ps4 := dead.run(abstained("#1", "Which language do you use most?", "text"))
	if len(ps4.leftovers) != 1 || !strings.Contains(ps4.leftovers[0].Detail, "failed:") {
		t.Fatalf("leftovers %+v", ps4.leftovers)
	}
}

func TestApply_Ladder_TerminalAbstainIsNotLaddered(t *testing.T) {
	l := newLadderEnv(t, ladderOwn, script(`[{"id":1,"answer":"Go","cite":["fact:skills"]}]`, "[]", "[]"), nil)
	row := abstained("#1", "Which language do you use most?", "text")
	row.MatcherReason = "ask-me"
	ps := l.run(row)
	if l.model.count(taskComplete) != 0 || len(ps.leftovers) != 1 || ps.leftovers[0].Detail != "abstained:ask-me" {
		t.Fatalf("calls %d leftovers %+v", l.model.count(taskComplete), ps.leftovers)
	}
	if len(l.page.answers) != 0 {
		t.Fatal("a value was written for an ask-me abstain")
	}
}

func TestApply_Ladder_ContentlessPromptNeverReachesModel(t *testing.T) {
	l := newLadderEnv(t, ladderOwn, script(`[{"id":1,"answer":"Yes","cite":["fact:skills"]}]`, "[]", "[]"), nil)
	ps := l.run(abstained("#1", "Select One Required", "select"), abstained("#2", "MM/DD/YYYY", "text"))
	if l.model.count(taskComplete) != 0 || len(ps.leftovers) != 2 {
		t.Fatalf("calls %d leftovers %+v", l.model.count(taskComplete), ps.leftovers)
	}
	long := "Have you ever been convicted of a crime? " + strings.Repeat("Details follow. ", 200) + "Yes means explain."
	if q := questionText(long); q != "Have you ever been convicted of a crime?" {
		t.Fatalf("a legend past the cap reads as the question it opens with: %q", q)
	}
}

func TestApply_Ladder_EntryHeadingExcludesDateLabelsOnlyForRung2(t *testing.T) {
	cases := []struct {
		name     string
		row      engine.Row
		excluded bool
	}{
		{"date under a work experience heading", engine.Row{Label: "From", Section: []string{"Work Experience"}}, true},
		{"year under an education heading", engine.Row{Label: "To", Section: []string{"Education"}}, true},
		{"start date under work availability", engine.Row{Label: "Start date", Section: []string{"Work Availability"}}, false},
		{"date under position details", engine.Row{Label: "Start date", Section: []string{"Position Details"}}, false},
		{"date under employment preferences", engine.Row{Label: "Start date", Section: []string{"Employment Preferences"}}, false},
		{"employer name under an entry heading", engine.Row{Label: "Company name", Section: []string{"Work History 2"}}, true},
		{"an ordinary question", engine.Row{Label: "Which tools do you use?", Section: []string{"Work Experience"}}, false},
		// The user's ties to a company are answered from the profile's lists only (Q2).
		{"a tie to the company", engine.Row{Label: "Do you have a family or close personal relationship with any current Q2 employee or with anyone who either (1) has received a currently pending offer of employment with Q2; or (2) accepted an offer of employment with Q2, even if they have not yet started?"}, true},
		{"a tie to this company", engine.Row{Label: "Internal Candidate"}, true},
		{"an industry, not a company", engine.Row{Label: "Have you worked in the financial services industry?"}, false},
	}
	for _, c := range cases {
		if _, got := ladderExclusion(c.row, nil, false); got != c.excluded {
			t.Errorf("%s: excluded=%v, want %v", c.name, got, c.excluded)
		}
	}
}

func TestApply_Ladder_Rung4ReportsDraftEntry(t *testing.T) {
	l := newLadderEnv(t, ladderOwn, nil, nil)
	l.observe("#1", "Which cloud do you prefer?", match.KindSelect, match.InputText, "AWS", "GCP")
	ps := l.run(abstained("#1", "Which cloud do you prefer?", "select"))
	if len(ps.leftovers) != 1 {
		t.Fatalf("leftovers %+v", ps.leftovers)
	}
	d := ps.leftovers[0].Draft
	if d == nil || d.Pattern != "which cloud do you prefer?" || d.Kind != "select" || len(d.Options) != 2 {
		t.Fatalf("draft %+v", d)
	}
	// A row the runtime cannot drive is not bought a model call and has no draft.
	f := newLadderEnv(t, ladderOwn, script(`[{"id":1,"answer":"x","cite":["fact:skills"]}]`, "[]", "[]"), nil)
	row := abstained("#1", "Which cloud do you prefer?", "text")
	row.Outcome, row.Reason = "failed", "write-failed"
	ps2 := f.run(row)
	if f.model.count(taskComplete) != 0 || len(ps2.leftovers) != 1 || ps2.leftovers[0].Draft != nil {
		t.Fatalf("calls %d leftovers %+v", f.model.count(taskComplete), ps2.leftovers)
	}
}

func TestApply_Check_UnresolvedRowsCarryReasonVocabulary(t *testing.T) {
	l := newLadderEnv(t, ladderOwn, script("[]", "[]", "[]"), nil)
	rows := []engine.Row{
		abstained("#1", "Which cloud do you prefer?", "text"),
		{Selector: "#2", Label: "Pick a team", Required: true, Outcome: "skipped", Reason: "not-asked", Kind: "text"},
		{Selector: "#3", Label: "Visa details", Required: true, Outcome: "failed", MatcherReason: "no-entry", Kind: "text"},
		{Selector: "#4", Label: "LinkedIn", Required: true, Outcome: "failed", Reason: "format-not-accepted", Detail: "Please enter a valid LinkedIn profile URL.", Kind: "text"},
	}
	ps := l.run(rows...)
	got := map[string]string{}
	for _, lo := range ps.leftovers {
		got[lo.Selector] = lo.Detail
	}
	if !strings.HasPrefix(got["#1"], "abstained:no-entry") || got["#2"] != "declined" || got["#3"] != "no-entry" ||
		got["#4"] != "format-not-accepted: Please enter a valid LinkedIn profile URL." {
		t.Fatalf("reasons %v", got)
	}
	// An unreadable control keeps its own vocabulary and counts as a defect.
	u := newLadderEnv(t, ladderOwn, nil, nil)
	// The re-read returns other controls but not this one; a re-read that
	// returned none at all would be an unreadable page, not an unreadable control.
	u.page.rows = func() []engine.InspectRow {
		return []engine.InspectRow{{Selector: "#other", Label: "Another question", Kind: "text"}}
	}
	row := engine.Row{Selector: "#u", Label: "Country", Required: true, Outcome: "filled", Value: "Canada", OptionLabel: "Canada", Kind: "select"}
	psu := u.run(row)
	if len(psu.unreadable) != 1 || psu.unreadable[0].Reason != ReasonUnreadable {
		t.Fatalf("unreadable %+v", psu.unreadable)
	}
}

// The runner reads the stored declines when a run starts, so a decline written
// by an earlier process is held for the new one.
func TestApply_Ladder_RunStartReadsTheStoredDeclines(t *testing.T) {
	g := newRig(t, func(d *Deps) { d.Drive = func(context.Context, *Exec) *stop { return nil } })
	key := declineKey("careers.example.test", "Which tools do you use?", "text", nil)
	earlier := NewDeclines(g.st, g.r.d.Now)
	rec(t, earlier, key, "v1.2.0", "rev1", "run-a")
	held := g.r.d.Page.Declines
	if held.Held(key, "v1.2.0", "rev1") {
		t.Fatal("the runner held a decline it never read")
	}
	g.r.RunOne(context.Background(), Input{URL: pageURL, Caller: gates.CallerCLI})
	if !held.Held(key, "v1.2.0", "rev1") {
		t.Fatal("the run start did not read the stored declines")
	}
}

func TestApply_Ladder_FailedWriteNamesTheStageItStoppedAt(t *testing.T) {
	m := &fakeMatcher{reply: func(req engine.MatchRequest) engine.MatchResponse {
		var out engine.MatchResponse
		for range req.Questions {
			out.Results = append(out.Results, engine.Resolution{Outcome: "filled", Value: "He/Him/His", OptionLabel: "He/Him/His"})
		}
		return out
	}}
	l := newLadderEnv(t, ladderOwn, script("[]", "[]", "[]"), func(d *Deps) { d.Page.Matcher = m })
	l.page.outcomes = map[string]string{"#a": "failed", "#b": "abstained", "#c": "failed", "#d": "failed"}
	l.page.reasons = map[string]string{"#a": "listbox-not-opened", "#b": "option-not-found", "#c": "verify-mismatch"}
	ps := l.run(
		engine.Row{Selector: "#a", Label: "Pronouns", Required: true, Outcome: "skipped", Reason: "no-answer", Kind: "text"},
		engine.Row{Selector: "#b", Label: "Gender", Required: true, Outcome: "skipped", Reason: "no-answer", Kind: "text"},
		engine.Row{Selector: "#c", Label: "Title", Required: true, Outcome: "skipped", Reason: "no-answer", Kind: "text"},
		engine.Row{Selector: "#d", Label: "Suffix", Required: true, Outcome: "skipped", Reason: "no-answer", Kind: "text"},
	)
	got := map[string]string{}
	for _, lo := range ps.leftovers {
		got[lo.Selector] = lo.Detail
	}
	want := map[string]string{"#a": "write-failed:listbox-not-opened", "#b": "write-failed:option-not-found", "#c": "write-failed:verify-mismatch", "#d": "write-failed:failed"}
	for sel, w := range want {
		if !strings.Contains(got[sel], w) {
			t.Errorf("%s leftover %q lacks %q (all: %v)", sel, got[sel], w, got)
		}
	}
}

// Workable re-renders a question under new ids while the fill runs: one leftover.
func TestApply_Check_ReRenderedControlIsOneLeftover(t *testing.T) {
	l := newLadderEnv(t, ladderOwn, script("[]", "[]", "[]"), nil)
	q := "Is everything here true?"
	l.page.rows = func() []engine.InspectRow { return []engine.InspectRow{{Selector: "#live", Label: q, Kind: "text"}} }
	ps := l.run(engine.Row{Selector: "#live", Label: q, Required: true, Outcome: "skipped", Reason: "no-lane-claimed", Kind: "text"}, abstained("#old1", q, "text"), abstained("#old2", q, "text"))
	if len(ps.report.Unresolved) != 1 || ps.leftovers[0].Selector != "#live" || !strings.HasPrefix(ps.leftovers[0].Detail, "abstained:no-entry") {
		t.Fatalf("leftovers %+v", ps.leftovers)
	}
}

// The ladder works on the copy of a re-rendered control the page holds, with
// the options the matcher read for the copy it replaced; the dead copies get
// no rung and no write.
func TestApply_Ladder_ReRenderedControlIsWorkedOnItsLiveCopy(t *testing.T) {
	q := "Do you have a public track record of open-source work?"
	l := newLadderEnv(t, ladderOwn, script(`[{"id":1,"answer":"YES","cite":["fact:skills"]}]`, `[{"id":1,"p":0.9}]`, "[]"), nil)
	for _, sel := range []string{"#old1", "#old2"} {
		l.observe(sel, q, match.KindRadioGroup, match.InputSingleSelect, "YES", "NO")
	}
	l.page.rows = func() []engine.InspectRow {
		return []engine.InspectRow{{Selector: "#live", Label: q, Kind: "radio-group", Required: true}}
	}
	ps := l.run(engine.Row{Selector: "#live", Label: q, Required: true, Outcome: "skipped", Reason: "no-lane-claimed", Kind: "radio-group"},
		abstained("#old1", q, "radio-group"), abstained("#old2", q, "radio-group"))
	if v, ok := answeredValue(l.senv, "#live"); !ok || v != "YES" {
		t.Fatalf("answers %+v: the live copy is answered", l.page.answers)
	}
	for _, call := range l.page.answers {
		for _, r := range call {
			if r.Selector != "#live" {
				t.Fatalf("a dead copy was written: %+v", l.page.answers)
			}
		}
	}
	if p := l.model.prompts(taskComplete); len(p) != 2 || strings.Count(p[0], q) != 1 || ps.report.Ladder.Calls != 2 || ps.report.Ladder.Rung4 != 1 {
		t.Fatalf("ladder %+v prompts %q", ps.report.Ladder, p)
	}
}

// A radio group no lane claimed carries its options on its row: the bank is
// asked with them (the matcher infers Yes/No), a deterministic fact picks one
// of them, and the model rung answers it, each written to its own selector.
func TestApply_Ladder_UnclaimedRadioIsWorkedWithItsRowOptions(t *testing.T) {
	radio := func(sel, label string) engine.Row {
		return engine.Row{Selector: sel, Label: label, Required: true, Outcome: "skipped", Reason: "no-lane-claimed", Kind: "radio-group", Options: []string{"Yes", "No"}}
	}
	m := &fakeMatcher{reply: func(req engine.MatchRequest) engine.MatchResponse {
		return engine.MatchResponse{Results: []engine.Resolution{{Outcome: "abstained", Reason: "no-entry"}}}
	}}
	own := ladderOwn
	no, yes := false, true
	own.SponsorshipNow, own.SponsorshipFuture = &no, &yes
	l := newLadderEnv(t, own, script("[]", "[]", "[]"), func(d *Deps) { d.Page.Matcher = m })
	l.run(radio("#s", "Do you currently require sponsorship?"))
	if len(m.reqs) != 1 || strings.Join(m.reqs[0].Questions[0].Options, ",") != "Yes,No" || m.reqs[0].Questions[0].InputType != "" {
		t.Fatalf("requests %+v", m.reqs)
	}
	if v, ok := answeredValue(l.senv, "#s"); !ok || v != "No" {
		t.Fatalf("answers %+v: the fact picks an option of the row", l.page.answers)
	}
	g := newLadderEnv(t, ladderOwn, script(`[{"id":1,"answer":"Yes","cite":["fact:skills"]}]`, `[{"id":1,"p":0.9}]`, "[]"), nil)
	g.run(radio("#r", "Do you have a public track record of open-source work?"))
	if v, ok := answeredValue(g.senv, "#r"); !ok || v != "Yes" {
		t.Fatalf("answers %+v: the model rung answers from the row's options", g.page.answers)
	}
}

// ladderBankYAML is a made-up bank: three answers a model may be shown, then
// one entry for each rule that keeps an entry from a model.
var ladderBankYAML = `version: 1
entries:
  - {id: weekend-shifts, description: "Whether the applicant can work weekend shifts", kind: fixed, category: availability, input_types: [yes_no], examples: ["Are you able to work weekend shifts?"], answer: "Yes"}
  - {id: build-system, description: "The build system the applicant prefers", kind: fixed, category: experience, input_types: [text], examples: ["What is your preferred build system?"], answer: "Bazel"}
  - {id: team-size, description: "The team size the applicant prefers", kind: pick-option, category: other, input_types: [single_select], examples: ["Which team size do you prefer?"], preferred: ["Small team"], fallback_text: "Small team"}
  - {id: held-category, description: "A hobby", kind: fixed, category: self-identification, examples: ["What is your first hobby?"], answer: "Chess"}
  - {id: held-workauth, description: "A hobby", kind: fixed, category: work-authorization, examples: ["What is your second hobby?"], answer: "Hiking"}
  - {id: skill-experience-knowledge-gate, description: "Experience with the skill a question names", kind: fixed, category: experience, examples: ["Do you have experience with the tools listed?"], answer: "Yes"}
  - {id: current-company, description: "The employer the applicant works for now", kind: fixed, category: employment, examples: ["Who employs you today?"], answer: "Northwind Example"}
  - {id: held-placeholder, description: "A hobby", kind: fixed, category: other, placeholder: true, examples: ["What is your third hobby?"], answer: "Drawing"}
  - {id: held-review, description: "A hobby", kind: fixed, category: other, needs_review: true, examples: ["What is your fourth hobby?"], answer: "Painting"}
  - {id: held-stale, description: "A hobby", kind: fixed, category: other, state: stale, examples: ["What is your fifth hobby?"], answer: "Rowing"}
  - {id: held-scoped, description: "A hobby", kind: fixed, category: other, examples: ["What is your sixth hobby?"], captured_scope: [{host: jobs.example.com, question: "What is your sixth hobby?"}], answer: "Sailing"}
  - {id: held-askme, description: "A hobby", kind: fixed, category: other, examples: ["What is your seventh hobby?"], answer: "@ask-me"}
  - {id: held-language, description: "How well the applicant speaks French", kind: language-proficiency, category: other, examples: ["How well do you speak French?"], language: French, speaking: Fluent, listening: Fluent, reading: Fluent, writing: Fluent}
  - {id: held-question, description: "Whether the applicant was ever dismissed", kind: fixed, category: other, examples: ["Have you ever been terminated for cause?"], answer: "No"}
  - {id: held-filtered, description: "Whether the applicant wants job alerts", kind: fixed, category: other, examples: ["Would you like to receive job alerts?"], answer: "No"}
  - {id: held-prompt, description: "A hobby", kind: fixed, category: other, examples: ["What is your eighth hobby?"], answer: "Select one"}
  - {id: held-long, description: "A hobby", kind: fixed, category: other, examples: ["What is your ninth hobby?"], answer: "` + strings.Repeat("x", maxEntryAnswer+1) + `"}
`

// ladderBank loads ladderBankYAML as the server does, with the binding whose
// Pinned verifies a citation of it.
func ladderBank(t *testing.T) (*bind.Binding, []match.Entry) {
	t.Helper()
	ws := workspace.Open(t.TempDir())
	path := ws.Path(workspace.FileAnswerBank)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(ladderBankYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	bank := answerbank.New(ws, nil)
	l, err := bank.Bank()
	if err != nil || len(l.Warnings) > 0 {
		t.Fatalf("the fixture bank: %v %v", err, l.Warnings)
	}
	return bind.New(bind.Deps{Bank: bank, Workspace: ws}), l.Bank.Entries
}

// A model maps a question to an answer the user saved; code holds it to that
// entry's own answer, and to the option the matcher's resolver picks for it.
func TestApply_Ladder_BankCitationIsTheEntrysOwnAnswer(t *testing.T) {
	binding, entries := ladderBank(t)
	shown := groundedBank(entries, nil, nil)
	_, user := groundedPrompt(nil, nil, nil, shown, "", time.Now())
	var ids []string
	for _, m := range regexp.MustCompile(`(?m)^bank:([a-z0-9-]+): `).FindAllStringSubmatch(user, -1) {
		ids = append(ids, m[1])
	}
	if want := []string{"weekend-shifts", "build-system", "team-size"}; !slices.Equal(ids, want) {
		t.Fatalf("the prompt shows %v, want %v: every other entry is of a kind no model is shown\n%s", ids, want, user)
	}
	l := newLadderEnv(t, ladderOwn, script("[]", "[]", "[]"), func(d *Deps) { d.Page.Pin = binding.Pinned })
	yesNo := []string{"Yes, I can", "No, I cannot"}
	for _, c := range []struct {
		name, label, kind string
		options           []string
		// entry is the one the matcher's abstain names; reply the model's answer.
		entry, reply string
		// want is the value written, "" when the answer is refused.
		want string
	}{
		{"the entry's own answer, in its spelling", "What is your preferred build system?", "text", nil, "", `"answer":"bazel","cite":["bank:build-system"]`, "Bazel"},
		{"another value citing the entry", "What is your preferred build system?", "text", nil, "", `"answer":"Make","cite":["bank:build-system"]`, ""},
		{"a choice through the option resolver", "Are you able to work weekend shifts?", "radio-group", yesNo, "", `"answer":"Yes, I can","cite":["bank:weekend-shifts"]`, "Yes, I can"},
		{"an option the resolver does not pick", "Are you able to work weekend shifts?", "radio-group", yesNo, "", `"answer":"No, I cannot","cite":["bank:weekend-shifts"]`, ""},
		{"an entry the rung did not show", "What is your fourth hobby?", "text", nil, "", `"answer":"Painting","cite":["bank:held-review"]`, ""},
		{"two entries", "What is your preferred build system?", "text", nil, "", `"answer":"bazel","cite":["bank:team-size","bank:build-system"]`, ""},
		{"an entry other than the one the matcher named", "What is your preferred build system?", "text", nil, "team-size", `"answer":"bazel","cite":["bank:build-system"]`, ""},
		{"a question kept from models", "Have you ever been terminated for cause?", "radio-group", []string{"Yes", "No"}, "", `"answer":"Yes","cite":["bank:weekend-shifts"]`, ""},
	} {
		row := &ladderRow{sel: "#q", label: c.label, kind: c.kind, options: c.options, entry: c.entry}
		ps := &pageState{rows: []engine.Row{{Selector: "#q", Label: c.label, Kind: c.kind, Options: c.options, Required: true}}}
		got := l.s.pinBank(context.Background(), "er1", ps, parseGrounded(`[{"id":1,`+c.reply+`}]`, []*ladderRow{row}, nil, shown, ""))
		switch {
		case c.want == "" && (len(got) != 0 || row.why != reasonNotBankAnswer):
			t.Errorf("%s: kept %+v, why %q", c.name, got, row.why)
		case c.want != "" && (len(got) != 1 || got[0].value != c.want || got[0].entry == ""):
			t.Errorf("%s: %+v, why %q, want %q", c.name, got, row.why, c.want)
		}
	}
}

// A rung run writes the saved answer the model cited, the support check does
// not overrule the entry, a row whose abstain names another entry keeps it,
// and the row's provenance names the entry for the keep and learning lanes.
// The citation rules themselves are TestApply_Ladder_BankCitationIsTheEntrysOwnAnswer's.
func TestApply_Ladder_Rung2AnswersFromASavedAnswerAndNamesTheEntry(t *testing.T) {
	binding, entries := ladderBank(t)
	own := ladderOwn
	own.Bank = entries
	reply := `[{"id":1,"answer":"bazel","cite":["bank:build-system"]},{"id":2,"answer":"Go","cite":["fact:skills"]},{"id":3,"answer":"bazel","cite":["bank:build-system"]}]`
	l := newLadderEnv(t, own, script(reply, `[{"id":1,"p":0.1},{"id":2,"p":0.1}]`, "[]"), func(d *Deps) { d.Page.Pin = binding.Pinned })
	l.g.r.d.Page.Matches.Observe(bind.Request{RunID: "er1"}, bind.Outcome{
		Results:  []match.Result{{Selector: "#tool", Outcome: match.OutcomeAbstained, Reason: match.ReasonPlaceholderEmpty, EntryID: "team-size"}},
		Prepared: []match.Question{{Selector: "#tool", Text: "What is your preferred build tool?", Kind: match.KindText}},
	})
	tool := abstained("#tool", "What is your preferred build tool?", "text")
	tool.MatcherReason = string(match.ReasonPlaceholderEmpty)
	ps := l.run(abstained("#build", "What is your preferred build system?", "text"), abstained("#lang", "Which languages do you use?", "text"), tool)
	l.s.pages = append(l.s.pages, ps)
	if v, ok := answeredValue(l.senv, "#build"); !ok || v != "Bazel" {
		t.Fatalf("the saved answer is not written as saved: %+v", l.page.answers)
	}
	if _, ok := answeredValue(l.senv, "#lang"); ok {
		t.Fatal("a fact answer the support check doubts was written")
	}
	if _, ok := answeredValue(l.senv, "#tool"); ok || ps.why["#tool"] != reasonNotBankAnswer {
		t.Fatalf("a citation of an entry the matcher did not name was written (%q)", ps.why["#tool"])
	}
	if got := ps.answered["#build"]; got != ProvenanceBank+"build-system" {
		t.Fatalf("provenance %q", got)
	}
	if ws := l.s.bankWrites(); len(ws) != 1 || ws[0].selector != "#build" || ws[0].entryID != "build-system" {
		t.Fatalf("bank writes %+v", ws)
	}
	for _, p := range l.model.prompts(taskComplete) {
		if strings.Contains(p, "=== SUPPORT CHECK ===") && strings.Contains(p, "build system") {
			t.Fatalf("the support check is asked about a saved answer:\n%s", p)
		}
	}
	// A page answered from the bank alone makes no support call.
	l2 := newLadderEnv(t, own, script(`[{"id":1,"answer":"bazel","cite":["bank:build-system"]}]`, "[]", "[]"), func(d *Deps) { d.Page.Pin = binding.Pinned })
	l2.run(abstained("#build", "What is your preferred build system?", "text"))
	if n := l2.model.count(taskComplete); n != 1 {
		t.Fatalf("%d model calls for a page answered from the bank alone", n)
	}
}

// A kb:<ref> citation holds only for a section the row was shown; it is the
// user's own evidence where a question needs that, and a choice still resolves
// through the option resolver (F-10).
func TestApply_Ladder_KnowledgeCitationIsASectionTheRowWasShown(t *testing.T) {
	const club = "knowledge/experience/club.md#Ticketing"
	sports := []match.KnowledgeSection{{Ref: club, Text: "Built ticketing for a sports league."}}
	yesNo := []string{"Yes", "No"}
	for _, c := range []struct {
		name, label string
		options     []string
		shown       []match.KnowledgeSection
		reply       string
		// want is the value kept, "" when refused for why.
		want, why string
	}{
		{"a section the row was shown", "Do you have experience working in the sports industry?", nil, sports, `"answer":"Yes","cite":["kb:` + club + `"]`, "Yes", ""},
		{"a section it was not shown", "Do you have experience working in the sports industry?", nil, sports, `"answer":"Yes","cite":["kb:knowledge/experience/other.md#Notes"]`, "", reasonKnowledgeNotShown},
		{"a section shown for another row", "Do you have experience working in the sports industry?", nil, nil, `"answer":"Yes","cite":["kb:` + club + `"]`, "", reasonKnowledgeNotShown},
		{"a choice through the option resolver", "Do you have experience working in the sports industry?", yesNo, sports, `"answer":"yes","cite":["kb:` + club + `"]`, "Yes", ""},
		{"a value that is not an option", "Do you have experience working in the sports industry?", yesNo, sports, `"answer":"Somewhat","cite":["kb:` + club + `"]`, "", reasonNotAnOption},
		{"the posting alone for the user's own experience", "Do you have experience working in the sports industry?", yesNo, sports, `"answer":"Yes","cite":["jd:Remote OK for this role"]`, "", reasonNeedsProfileFact},
		{"the posting alone for a disclosure", "Have you held a security clearance?", yesNo, nil, `"answer":"No","cite":["jd:Remote OK for this role"]`, "", reasonNeedsProfileFact},
		{"a knowledge section for a willingness", "Are you willing to work at sports events?", yesNo, sports, `"answer":"Yes","cite":["kb:` + club + `"]`, "Yes", ""},
	} {
		row := &ladderRow{sel: "#q", label: c.label, options: c.options, knowledge: c.shown}
		got := parseGrounded(`[{"id":1,`+c.reply+`}]`, []*ladderRow{row}, nil, nil, groundedJD)
		switch {
		case c.want == "" && (len(got) != 0 || row.why != c.why):
			t.Errorf("%s: kept %+v, why %q, want %q", c.name, got, row.why, c.why)
		case c.want != "" && (len(got) != 1 || got[0].value != c.want || got[0].kb != club):
			t.Errorf("%s: %+v, why %q", c.name, got, row.why)
		}
	}
}

// The computer-use rung (owner decision F-9) writes again, by trusted input,
// only a required row whose write failed in a way a person's input can cure,
// of a kind it writes, with the one value the chain chose; never what the
// owner keeps for themselves. It opens a list the fill asked with no options
// only when the abstain the missing options caused named an entry (CO-1).
func TestApply_Ladder_ComputerUseTakesOnlyCurableWritesOfTheChainsValue(t *testing.T) {
	row := func(kind, label string) engine.Row {
		return engine.Row{Selector: "#q", Label: label, Kind: kind, Required: true}
	}
	write := func(failure, v string) cuWrite { return cuWrite{value: v, option: v, rung: "rung1", failure: failure} }
	unread := func(reason, entry string) cuWrite {
		return cuWrite{rung: "fill", failure: "abstained:" + reason, entry: entry}
	}
	const sponsor = "Will you now or in the future require visa sponsorship?"
	yesNo := []string{"Yes", "No"}
	long := strings.Repeat("a", maxCUText+1)
	label65 := strings.Repeat("b", maxCUTypeAhead+1)
	for _, c := range []struct {
		name    string
		r       engine.Row
		w       cuWrite
		options []string
		legalOn bool
		want    string
	}{
		{"a box that let the typed value go", row("text", "City"), write("write-failed:write-not-committed", "Durham"), nil, false, ""},
		{"a list the engine could not open, options unread", row("custom-listbox", "How did you hear about us?"), write("write-failed:listbox-not-opened", "Referral"), nil, false, ""},
		{"a radio group holding the opposite", row("radio-group", "Are you willing to relocate?"), write(ReasonMismatch, "Yes"), yesNo, false, ""},
		{"a lone box to tick", row("checkbox", "Open to relocation"), write("write-failed:verify-mismatch", "Yes"), nil, false, ""},
		{"a declaration with the setting on", row("checkbox", "I certify the information is true"), write("write-failed:verify-mismatch", "Yes"), nil, true, ""},
		{"an optional row", engine.Row{Selector: "#q", Label: "City", Kind: "text"}, write("write-failed:write-not-committed", "Durham"), nil, false, "optional"},
		{"a file", row("file", "Resume"), write("write-failed:failed", "resume.pdf"), nil, false, "kind file"},
		{"a rich editor", row("contenteditable", "Cover letter"), write("write-failed:failed", "Plain words."), nil, false, "kind contenteditable"},
		{"a click the engine refused", row("custom-listbox", "Status"), write("write-failed:unsafe-click", "Yes"), yesNo, false, "failure write-failed:unsafe-click"},
		{"a verification-code box", row("text", "Verification code"), write("write-failed:write-not-committed", "424242"), nil, false, ReasonCodeBox},
		{"no value", row("text", "City"), write("write-failed:write-not-committed", " "), nil, false, "no value"},
		{"a line break, which a text box reads as Enter", row("textarea", "Why this role?"), write("write-failed:write-not-committed", "one\ntwo"), nil, false, "control character"},
		{"a value past the typing bound", row("textarea", "Why this role?"), write("write-failed:write-not-committed", long), nil, false, "too long"},
		{"a signature", row("text", "Signature"), write("write-failed:write-not-committed", "Alex Example"), nil, false, gates.ExcludedSignature},
		{"a declaration with the setting off", row("checkbox", "I certify the information is true"), write("write-failed:verify-mismatch", "Yes"), nil, false, gates.ExcludedLegalDeclaration},
		{"self-identification", row("radio-group", "Gender"), write(ReasonMismatch, "Man"), []string{"Man", "Woman", "Decline to answer"}, false, gates.ExcludedSelfID},
		{"a date of birth, whose value is withheld", row("text", "Date of birth"), write("write-failed:write-not-committed", "2000-01-01"), nil, false, gates.ExcludedSelfID},
		{"a government id number", row("text", "Passport number"), write("write-failed:write-not-committed", "X1234567"), nil, false, "withheld government-id"},
		{"a choice outside the options", row("radio-group", "Are you willing to relocate?"), write(ReasonMismatch, "Maybe"), yesNo, false, reasonNotAnOption},
		{"a lone box to clear", row("checkbox", "Text me about this job"), write("write-failed:verify-mismatch", "No"), nil, false, "untick"},
		{"a select label past the type-ahead bound", row("select", "Office"), write(ReasonNotCommitted, label65), []string{label65}, false, "too long"},
		{"an unread list a fixed entry's input types refused", row("custom-listbox", sponsor), unread("input-mismatch", "sponsorship-now-or-future"), nil, false, ""},
		{"an unread list whose abstain named no entry", row("custom-listbox", sponsor), unread("input-mismatch", ""), nil, false, "no entry"},
		{"an unread list two entries fit", row("custom-listbox", sponsor), unread("ambiguous", "sponsorship-now-or-future"), nil, false, "failure abstained:ambiguous"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := cuEligible(c.r, c.w, c.options, c.legalOn)
			if got != c.want || ok != (c.want == "") {
				t.Fatalf("cuEligible = %q, %v; want %q", got, ok, c.want)
			}
		})
	}
}

// The page holds what the rung wrote when it shows the value, a text box's own
// formatting of it, or a list display carrying the label; a placeholder, the
// value from before, or the opposite answer is no write.
func TestApply_Ladder_ComputerUseCountsOnlyAValueThePageHolds(t *testing.T) {
	for _, c := range []struct {
		name              string
		text              bool
		want, before, now string
		held              bool
	}{
		{"the value itself", false, "No", "", "no", true},
		{"the value the page already held", true, "Durham", "Durham", "Durham", true},
		{"a box's own formatting", true, "5550100", "", "555-0100", true},
		{"a list display with the label", false, "Yes", "", "Yes, I am", true},
		{"the placeholder", true, "Durham", "", "Select...", false},
		{"what the page held before", true, "Durham", "Raleigh", "Raleigh", false},
		{"the opposite answer", false, "Yes", "", "No", false},
		{"another label of the list", false, "No", "", "None of the above", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := cuHeld(c.text, c.want, c.before, c.now); got != c.held {
				t.Fatalf("cuHeld = %v", got)
			}
		})
	}
}

// inputLog is a tab on which every control is found and on screen, and which
// records the input sent to it. A list on it shows the options of shown, and
// is open already when expanded; a native select on it has selected selected.
type inputLog struct {
	Tab
	input    []string
	shown    cuShown
	expanded bool
	selected string
}

func (t *inputLog) Eval(_ context.Context, expr string, out any) error {
	switch {
	case strings.Contains(expr, `("cuTarget",{"kind":"select"`):
		return setJSON(out, cuTarget{Found: true, Tag: "select", Editable: true})
	case strings.Contains(expr, `("cuTarget",`):
		return setJSON(out, cuTarget{Found: true, Tag: "input", Editable: true, Expanded: t.expanded})
	case strings.Contains(expr, `("cuOptions",`):
		return setJSON(out, t.shown)
	case strings.Contains(expr, `("cuSelected",`):
		return setJSON(out, t.selected)
	case strings.Contains(expr, `("locate",`):
		return setJSON(out, Located{Found: true, Visible: true, Hit: true, X: 5, Y: 5})
	case strings.Contains(expr, `("focus",`):
		return setJSON(out, true)
	}
	return nil
}

func (t *inputLog) Click(context.Context, float64, float64) error {
	t.input = append(t.input, "click")
	return nil
}

func (t *inputLog) TypeText(context.Context, string) error {
	t.input = append(t.input, "type")
	return nil
}

func (t *inputLog) PressKey(_ context.Context, key string) error {
	t.input = append(t.input, key)
	return nil
}

// refusingHolder is an engine that will not hold the page's input.
type refusingHolder struct{ released int }

func (h *refusingHolder) HoldInputIn(context.Context, string) error {
	return errors.New("a fill owns the page")
}

func (h *refusingHolder) ReleaseInputIn(context.Context, string, []engine.Resolution) error {
	h.released++
	return nil
}

// holdingHolder is an engine that holds the page's input.
type holdingHolder struct{}

func (holdingHolder) HoldInputIn(context.Context, string) error { return nil }

func (holdingHolder) ReleaseInputIn(context.Context, string, []engine.Resolution) error { return nil }

// An unread list takes the one option the matcher picks among those it shows,
// a native select by type-ahead without a press; a list whose options the
// matcher picks none of is closed with Escape while the focus is in it, under
// the engine's hold, and no option is pressed (CO-1).
func TestApply_Ladder_ComputerUseWritesOnlyTheOptionTheMatcherPicks(t *testing.T) {
	l := newLadderEnv(t, ladderOwn, script("[]", "[]", "[]"), nil)
	l.page.rows = func() []engine.InspectRow { return []engine.InspectRow{{Selector: "#sp", Value: "Yes"}} }
	w := cuWrite{rung: "fill", failure: "abstained:input-mismatch", entry: "sponsorship-now-or-future"}
	shown := []string{"Yes", "No"}
	yes := engine.Resolution{Outcome: "filled", Value: "Yes", OptionLabel: "Yes"}
	for _, c := range []struct {
		name              string
		kind              string
		pin               engine.Resolution
		focused, expanded bool
		holder            inputHolder
		want              []string
	}{
		{"a native select's pick, typed", "select", yes, false, false, holdingHolder{}, []string{"type", "Tab"}},
		{"an abstain", "custom-listbox", engine.Resolution{Outcome: "abstained", Reason: "input-mismatch"}, true, false, holdingHolder{}, []string{"click", "Escape"}},
		{"two options at once", "custom-listbox", engine.Resolution{Outcome: "filled", Value: "Yes", OptionLabel: "Yes", OptionLabels: shown}, true, false, holdingHolder{}, []string{"click", "Escape"}},
		{"a pick outside the options", "custom-listbox", engine.Resolution{Outcome: "filled", Value: "Maybe", OptionLabel: "Maybe"}, true, false, holdingHolder{}, []string{"click", "Escape"}},
		{"the focus outside the list", "custom-listbox", engine.Resolution{Outcome: "abstained"}, false, false, holdingHolder{}, []string{"click"}},
		{"an open list whose engine does not hold the input", "custom-listbox", engine.Resolution{Outcome: "abstained"}, true, true, &refusingHolder{}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			l.g.r.d.Page.Pin = func(context.Context, engine.MatchRequest, []string) ([]engine.Resolution, error) {
				return []engine.Resolution{c.pin}, nil
			}
			r := engine.Row{Selector: "#sp", Label: "Will you now or in the future require visa sponsorship?", Kind: c.kind, Required: true}
			tab := &inputLog{shown: cuShown{Options: shown, Focused: c.focused}, expanded: c.expanded, selected: "Yes"}
			l.s.tab = tab
			cu := &cuRun{s: l.s, holder: c.holder, docs: []cuDoc{{}}}
			_, options, step := cu.choose(context.Background(), "er1", []engine.Row{r}, r, r.Selector, w, false, "")
			if (step == "") != (c.pin.Outcome == "filled" && c.kind == "select") || !slices.Equal(options, shown) || !slices.Equal(tab.input, c.want) {
				t.Fatalf("step %q, options %q, input %v; want input %v", step, options, tab.input, c.want)
			}
		})
	}
}

// The rung's input is bracketed by the engine's hold or not sent at all: a
// page whose engine does not hold its input gets no click and no key, neither
// a box nor a native select, which is typed into without a press.
func TestApply_Ladder_ComputerUseSendsNoInputTheEngineDoesNotHold(t *testing.T) {
	l := newLadderEnv(t, ladderOwn, script("[]", "[]", "[]"), nil)
	for _, r := range []engine.Row{
		{Selector: "#langs", Label: "Which programming languages do you use?", Kind: "text", Required: true},
		{Selector: "#country", Label: "Country", Kind: "select", Required: true},
	} {
		tab, h := &inputLog{}, &refusingHolder{}
		l.s.tab = tab
		c := &cuRun{s: l.s, holder: h, docs: []cuDoc{{}}}
		w := cuWrite{value: "Canada", option: "Canada", rung: "rung1", failure: "write-failed:write-not-committed"}
		step := c.write(context.Background(), []engine.Row{r}, r, r.Selector, w, []string{"Canada"}, "")
		if !strings.HasPrefix(step, "the page's input could not be held") || len(tab.input) > 0 || h.released > 0 {
			t.Fatalf("%s: step %q, input %v, %d releases", r.Kind, step, tab.input, h.released)
		}
	}
}

// The rung tries each row's last write that did not take, in page order: the
// ladder's from its record, a fill write the check found not held with the
// fill's value, and a fill write that failed outright with the matcher's
// choice, a review-band one included; a row nobody chose an answer for has
// nothing to write, but an open list the matcher was asked about with no
// options is tried for the entry its abstain named (CO-1).
func TestApply_Ladder_ComputerUseTriesTheChainsLastWriteOfEachRow(t *testing.T) {
	l := newLadderEnv(t, ladderOwn, script("[]", "[]", "[]"), nil)
	chose := func(sel string, o match.Outcome, answer string) {
		l.g.r.d.Page.Matches.Observe(bind.Request{RunID: "er1"}, bind.Outcome{
			Results:  []match.Result{{Selector: sel, Outcome: o, Answer: answer, ChosenLabel: answer}},
			Prepared: []match.Question{{Selector: sel, Text: sel, Kind: match.KindCustomListbox}},
		})
	}
	chose("#c", match.OutcomeFilled, "No")
	chose("#e", match.OutcomeFilledReview, "Yes")
	chose("#f", match.OutcomeAbstained, "")
	l.g.r.d.Page.Matches.Observe(bind.Request{RunID: "er1"}, bind.Outcome{
		Results: []match.Result{
			{Selector: "#g", Outcome: match.OutcomeAbstained, Reason: match.ReasonInputMismatch, EntryID: "sponsorship-now-or-future"},
			{Selector: "#h", Outcome: match.OutcomeAbstained, Reason: match.ReasonLowConfidence, EntryID: "job-source"},
			{Selector: "#i", Outcome: match.OutcomeAbstained, Reason: match.ReasonLowConfidence, EntryID: "job-source"},
			{Selector: "#j", Outcome: match.OutcomeAbstained, Reason: match.ReasonInputMismatch, EntryID: "work-auth-us"},
		},
		Prepared: []match.Question{{Selector: "#g"}, {Selector: "#h", Options: []match.Option{{Label: "LinkedIn"}, {Label: "Referral"}}}, {Selector: "#i"}, {Selector: "#j"}},
	})
	ps := &pageState{rows: []engine.Row{
		{Selector: "#a", Outcome: "skipped"},
		{Selector: "#b", Outcome: "filled", Value: "Alex"},
		{Selector: "#c", Outcome: "failed", Reason: "listbox-not-opened"},
		{Selector: "#d", Outcome: "failed", Reason: "listbox-not-opened"},
		{Selector: "#e", Outcome: "failed", Reason: "write-not-committed"},
		{Selector: "#f", Outcome: "failed", Reason: "listbox-not-opened"},
		{Selector: "#g", Kind: "custom-listbox", Required: true, Outcome: "abstained"},
		{Selector: "#h", Kind: "custom-listbox", Required: true, Outcome: "abstained"},
		{Selector: "#i", Kind: "text", Required: true, Outcome: "abstained"},
		{Selector: "#j", Kind: "custom-listbox", Required: true, Outcome: "filled", Value: "Yes"},
	}}
	ps.unwrite(answerWrite{idx: 0, value: "Go", rung: "rung2"}, "write-failed:write-not-committed")
	ps.notHeld = []Leftover{{Selector: "#b", Reason: ReasonNotCommitted}}
	want := []cuWrite{
		{idx: 0, value: "Go", rung: "rung2", failure: "write-failed:write-not-committed"},
		{idx: 1, value: "Alex", rung: "fill", failure: ReasonNotCommitted},
		{idx: 2, value: "No", option: "No", rung: "fill", failure: "write-failed:listbox-not-opened"},
		{idx: 4, value: "Yes", option: "Yes", rung: "fill", failure: "write-failed:write-not-committed", review: true},
		{idx: 6, rung: "fill", failure: "abstained:input-mismatch", entry: "sponsorship-now-or-future"},
	}
	if got := l.s.cuWrites("er1", ps); !reflect.DeepEqual(got, want) {
		t.Fatalf("writes %+v, want %+v", got, want)
	}
}

// A list the rung wrote with the option the matcher picked for the entry the
// fill's abstain named is that entry's write, so an edit of it later counts
// against the entry; a write the ladder chose names none (CO-1).
func TestApply_Ladder_ComputerUseNamesTheEntryItWroteFrom(t *testing.T) {
	l := newLadderEnv(t, ladderOwn, script("[]", "[]", "[]"), nil)
	ps := &pageState{report: PageReport{URL: "https://apply.example.test/form"}, rows: []engine.Row{
		{Selector: "#sp", Label: "Will you now or in the future require visa sponsorship?", Kind: "custom-listbox", Required: true, Outcome: "abstained"},
		{Selector: "#au", Label: "Are you legally authorized to work in the United States?", Kind: "custom-listbox", Required: true, Outcome: "failed"},
	}}
	ps.cuWrote(cuWrite{idx: 0, value: "No", option: "No", rung: "fill", failure: "abstained:input-mismatch", entry: "sponsorship-now-or-future"})
	ps.cuWrote(cuWrite{idx: 1, value: "Yes", option: "Yes", rung: "rung2", failure: "write-failed:option-not-found"})
	l.s.pages = []*pageState{ps}
	if ws := l.s.bankWrites(); len(ws) != 1 || ws[0].selector != "#sp" || ws[0].entryID != "sponsorship-now-or-future" {
		t.Fatalf("bank writes %+v", ws)
	}
}

// A write of the rung the page holds answers its row: the row is filled and
// counted, no longer a value the page lacks nor a reason left open, and a
// review-band answer stays flagged for the user to review.
func TestApply_Ladder_ComputerUseWriteAnswersItsRow(t *testing.T) {
	ps := &pageState{rows: []engine.Row{{Selector: "#e", Label: "Sponsorship", Kind: "custom-listbox", Required: true, Outcome: "failed", Reason: "write-not-committed"}}}
	ps.notHeld = []Leftover{{Selector: "#e", Label: "Sponsorship", Reason: ReasonNotCommitted}}
	ps.report.Uncommitted = []string{"Sponsorship"}
	ps.setWhy("#e", "write-failed:write-not-committed")
	ps.cuWrote(cuWrite{idx: 0, value: "No", option: "No", rung: "fill", failure: "write-failed:write-not-committed", review: true})
	r := ps.rows[0]
	if r.Outcome != "filled" || !r.Review || open(r) || len(ps.notHeld) != 0 || len(ps.report.Uncommitted) != 0 || ps.why["#e"] != "" || ps.report.Ladder.ComputerUse != 1 {
		t.Fatalf("row %+v, not held %v, uncommitted %v, why %q, ladder %+v", r, ps.notHeld, ps.report.Uncommitted, ps.why["#e"], ps.report.Ladder)
	}
}
