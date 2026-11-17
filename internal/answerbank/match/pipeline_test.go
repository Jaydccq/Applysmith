package match

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPipeline_ContentlessAbstainsNoEntryFirst(t *testing.T) {
	called := false
	m := New(bankOf(ent("preferred-name", "fixed", withAnswer("Alex"), withExamples("Choose..."))),
		Stages{RowControls: func(*Step) (Draft, bool) { called = true; return Draft{}, false }, Fixed: fixedStage})
	for _, text := range []string{"Choose...", "MM/DD/YYYY", "Select One Required", "*", "\ufeffSelect\ufeffone\ufeff", "-- ... Select ... --"} {
		r := one(m, textQ("s", text))
		if r.Outcome != OutcomeAbstained || r.Reason != ReasonNoEntry || r.Terminal || !traceHas(r, "control's own prompt") {
			t.Errorf("%q: %+v", text, r)
		}
	}
	if called {
		t.Fatal("no stage runs for a prompt-only text")
	}
}

func TestPipeline_SanitationTraceAndNoMutation(t *testing.T) {
	m := New(bankOf(ent("preferred-name", "fixed", withAnswer("Alex"), withExamples("Preferred Name"))), echoStages())
	q := textQ("s", "Preferred Name (if applicable)")
	r := one(m, q)
	if r.EntryID != "preferred-name" || r.Answer != "Alex" {
		t.Fatalf("result = %+v", r)
	}
	if !traceHas(r, "sanitized questionText") || !traceHas(r, "Preferred Name (if applicable)") {
		t.Fatalf("trace = %v", r.Trace)
	}
	if q.Text != "Preferred Name (if applicable)" {
		t.Fatal("caller's question mutated")
	}
	clean := one(m, textQ("s", "Preferred Name"))
	if traceHas(clean, "sanitized questionText") {
		t.Fatal("clean text carries no sanitation line")
	}
}

func TestPipeline_TrapBeforeRowControls(t *testing.T) {
	called := false
	m := New(bankOf(), Stages{RowControls: func(*Step) (Draft, bool) { called = true; return Draft{Answer: "x"}, true }})
	r := one(m, Question{Selector: "school--1", Text: "School", Kind: KindText, Hidden: true})
	if r.Reason != ReasonAIInstruction || !r.Terminal || called {
		t.Fatalf("result = %+v called=%v", r, called)
	}
	r = one(m, textQ("s", "If you are human, leave this field empty"))
	if r.Reason != ReasonAIInstruction || r.Outcome != OutcomeAbstained {
		t.Fatalf("honeypot = %+v", r)
	}
}

func TestPipeline_GuardChainOrder(t *testing.T) {
	// One entry that breaks every policy at once; each step is cleared in order.
	e := ent("address-city", "fixed", withAnswer("Springfield"), withTypes(InputYesNo), withExamples("City"))
	e.Answer = AskMe
	q := Question{Selector: "s", Text: "City", Kind: KindText, SectionPath: []string{"Education"}}
	st := echoStages()
	ctx := true
	st.ContextPolicies = func(*Step) (Draft, bool) {
		if ctx {
			return Draft{Abstain: &Abstain{Reason: ReasonContextPolicy, Detail: DetailPostingCountry}}, true
		}
		return Draft{}, false
	}
	step := func() Result { return one(New(bankOf(e), st), q) }

	if r := step(); r.Reason != ReasonContextPolicy || r.Detail != DetailPostingCountry || !r.Terminal {
		t.Fatalf("1: %+v", r)
	}
	ctx = false
	if r := step(); r.Reason != ReasonSectionPolicy {
		t.Fatalf("2: %+v", r)
	}
	q.SectionPath = nil
	if r := step(); r.Reason != ReasonAskMe || !r.Terminal {
		t.Fatalf("3: %+v", r)
	}
	e.Answer = ""
	if r := step(); r.Reason != ReasonInputMismatch || r.EntryID != "address-city" || r.Terminal {
		t.Fatalf("4: %+v", r)
	}
	e.InputTypes = nil
	if r := step(); r.Reason != ReasonPlaceholderEmpty {
		t.Fatalf("5: %+v", r)
	}
	e.ID = IDCurrentAgeBracket
	if r := step(); r.Outcome == OutcomeAbstained {
		t.Fatalf("a derived entry is exempt from the empty-body abstain: %+v", r)
	}
	e.ID = "address-city"
	e.Answer = "Springfield"
	if r := step(); r.Outcome != OutcomeFilled || r.Answer != "Springfield" {
		t.Fatalf("6: %+v", r)
	}
}

func TestPipeline_EntrySetOrderAndScope(t *testing.T) {
	var seen []string
	st := Stages{Fixed: func(s *Step) (Draft, bool) {
		seen = nil
		for _, e := range s.Entries {
			seen = append(seen, e.ID)
		}
		return Draft{Answer: "ok"}, true
	}}
	unscoped := ent("plain-entry", "fixed", withAnswer("a"), withExamples("Alpha question"))
	scoped := ent("scoped-entry", "fixed", withAnswer("b"), withExamples("Beta question"))
	scoped.CapturedScope = []ScopeItem{{Host: "Jobs.Example.com ", Question: "Beta question*", Options: []string{"Yes", "No"}}}
	m := New(bankOf(unscoped, scoped), st)

	yn := func(text string) Question { return yesNoQ("s", text) }
	resp, _ := m.Match(Request{Host: "jobs.example.com", Questions: []Question{yn("Beta question")}})
	if resp.Results[0].EntryID != "scoped-entry" {
		t.Fatalf("scope admits the same host, question and option set: %+v", resp.Results[0])
	}
	want := []string{"plain-entry", "scoped-entry", "cover-letter-submitted", "resume-submitted"}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("entry set = %v", seen)
	}
	for name, req := range map[string]Request{
		"other host":     {Host: "other.example.com", Questions: []Question{yn("Beta question")}},
		"other options":  {Host: "jobs.example.com", Questions: []Question{{Selector: "s", Text: "Beta question", Kind: KindRadioGroup, Options: opts("Yes", "No", "Not sure")}}},
		"other question": {Host: "jobs.example.com", Questions: []Question{yn("Beta question two")}},
	} {
		resp, _ = m.Match(req)
		if resp.Results[0].EntryID == "scoped-entry" {
			t.Errorf("%s: the captured answer must not carry over", name)
		}
	}
	// Option order does not matter for the scope.
	resp, _ = m.Match(Request{Host: "jobs.example.com", Questions: []Question{{Selector: "s", Text: "Beta question", Kind: KindRadioGroup, Options: opts("No", "Yes")}}})
	if resp.Results[0].EntryID != "scoped-entry" {
		t.Fatalf("option order: %+v", resp.Results[0])
	}
}

func TestPipeline_TraceNamesRouteEntryKindAndScore(t *testing.T) {
	st := Stages{Fixed: func(s *Step) (Draft, bool) {
		s.Tracef("top option scores: %q=%.2f", "Yes", 1.0)
		return Draft{Answer: "Yes", ChosenLabel: "Yes", HasOption: true, OptionScore: 1.0, Deterministic: true}, true
	}}
	e := ent("work-auth-us", "fixed", withAnswer("Yes"), withExamples("Are you legally permitted to work here?"))
	r := one(New(bankOf(e), st), yesNoQ("s", "Are you legally permitted to work here?"))
	for _, want := range []string{"entry id=work-auth-us", "kind=fixed", "control=radio-group/yes_no", "example", "top option scores"} {
		if !traceHas(r, want) {
			t.Errorf("trace lacks %q: %v", want, r.Trace)
		}
	}
}

func TestPipeline_CountersAreNotRestored(t *testing.T) {
	st := Stages{PickOption: func(s *Step) (Draft, bool) {
		return Draft{Answer: "Yes", ChosenLabel: "Yes", HasOption: true, OptionScore: 1.0}, true
	}}
	e := ent("sponsorship-now-or-future", "pick-option", withExamples("Will you require sponsorship?"))
	q := Question{Selector: "s", Text: "Will you require sponsorship?", Kind: KindRadioGroup,
		Options: []Option{{"a", "Choose... 1 of 3"}, {"b", "Yes 2 of 3"}, {"c", "No 3 of 3"}}}
	if r := one(New(bankOf(e), st), q); r.ChosenLabel != "Yes" {
		t.Fatalf("clean label expected, got %q", r.ChosenLabel)
	}
}

func TestPipeline_NumberedChoicesReturnTheNumber(t *testing.T) {
	var sawKind ControlKind
	st := Stages{PickOption: func(s *Step) (Draft, bool) {
		sawKind = s.Q.Kind
		return Draft{Answer: "2", ChosenLabel: "2", HasOption: true, OptionScore: 1.0}, true
	}}
	e := ent("current-immigration-status", "pick-option", withExamples("What is your status Enter the number?"), withPreferred("Permanent resident"))
	q := textQ("s", "What is your status (1. Citizen; 2. Permanent resident; 3. Other) Enter the number?")
	r := one(New(bankOf(e), st), q)
	if sawKind != KindSelect || r.Answer != "2" || r.Outcome != OutcomeFilled {
		t.Fatalf("kind %s result %+v", sawKind, r)
	}
}

func TestRequest_Bounds(t *testing.T) {
	m := New(bankOf(), Stages{})
	many := make([]Question, MaxQuestions+1)
	for i := range many {
		many[i] = textQ(fmt.Sprintf("s%d", i), "Question")
	}
	if _, err := m.Match(Request{Host: "h", Questions: many}); !errors.Is(err, ErrValidation) {
		t.Fatalf("301 questions: %v", err)
	}
	if _, err := m.Match(Request{Host: "h", Questions: many[:MaxQuestions]}); err != nil {
		t.Fatalf("300 questions: %v", err)
	}
	big := Question{Selector: "s", Text: "Degree", Kind: KindSelect, Options: make([]Option, 268)}
	for i := range big.Options {
		big.Options[i] = Option{Value: fmt.Sprint(i), Label: fmt.Sprintf("Option %d", i)}
	}
	if _, err := m.Match(Request{Host: "h", Questions: []Question{big}}); err != nil {
		t.Fatalf("268 options: %v", err)
	}
	// The bound is exactly 500 options: 500 pass, 501 are refused.
	at := big
	at.Options = append([]Option(nil), big.Options...)
	for len(at.Options) < MaxOptionsPerItem {
		at.Options = append(at.Options, Option{Value: fmt.Sprint(len(at.Options)), Label: fmt.Sprintf("Option %d", len(at.Options))})
	}
	if MaxOptionsPerItem != 500 || MaxQuestions != 300 {
		t.Fatalf("bounds are %d questions and %d options", MaxQuestions, MaxOptionsPerItem)
	}
	if _, err := m.Match(Request{Host: "h", Questions: []Question{at}}); err != nil {
		t.Fatalf("500 options: %v", err)
	}
	at.Options = append(at.Options, Option{Value: "x", Label: "One more"})
	if _, err := m.Match(Request{Host: "h", Questions: []Question{at}}); !errors.Is(err, ErrValidation) {
		t.Fatalf("501 options: %v", err)
	}
	big.Options = append(big.Options, make([]Option, MaxOptionsPerItem)...)
	if _, err := m.Match(Request{Host: "h", Questions: []Question{big}}); !errors.Is(err, ErrValidation) {
		t.Fatalf("over the option bound: %v", err)
	}
	for name, q := range map[string]Question{
		"no selector": {Text: "x", Kind: KindText}, "bad kind": {Selector: "s", Text: "x", Kind: "weird"},
		"bad type": {Selector: "s", Text: "x", Kind: KindText, InputType: "weird"}, "negative length": {Selector: "s", Text: "x", Kind: KindText, MaxLength: -1},
	} {
		if _, err := m.Match(Request{Host: "h", Questions: []Question{q}}); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestDeterminism_ByteIdenticalTwiceAndClockInjected(t *testing.T) {
	st := echoStages()
	st.ApplicationStatement = func(s *Step) (Draft, bool) {
		if !strings.Contains(strings.ToLower(s.Q.Text), "today") {
			return Draft{}, false
		}
		d := s.Req.Today
		return Draft{Answer: fmt.Sprintf("%02d/%02d/%04d", d.Month, d.Day, d.Year), Source: SourceFormText}, true
	}
	m := New(bankOf(ent("alpha-fact", "fixed", withAnswer("A"), withExamples("Alpha"))), st)
	req := Request{Host: "h", Today: Date{2031, 3, 4}, Questions: []Question{textQ("s", "Alpha"), textQ("t", "Today's date"), yesNoQ("u", "Something else entirely")}}
	a, _ := m.Match(req)
	b, _ := m.Match(req)
	if !reflect.DeepEqual(a, b) || fmt.Sprintf("%#v", a) != fmt.Sprintf("%#v", b) {
		t.Fatal("two runs differ")
	}
	if a.Results[1].Answer != "03/04/2031" {
		t.Fatalf("today = %q", a.Results[1].Answer)
	}
}

// The matcher keeps no memo between calls, so a matcher that has already served
// other requests answers exactly as a fresh one does.
func TestDeterminism_ResultsDoNotDependOnCallHistory(t *testing.T) {
	bank := bankOf(ent("alpha-fact", "fixed", withAnswer("A"), withExamples("Alpha")))
	req := Request{Host: "h", Questions: []Question{textQ("s", "Alpha"), yesNoQ("u", "Something else entirely")}}
	warm := New(bank, echoStages())
	for _, q := range []string{"Alpha", "Beta", "Gamma?"} {
		if _, err := warm.Match(Request{Host: "other", Questions: []Question{textQ("w", q)}}); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := warm.Match(req)
	b, _ := New(bank, echoStages()).Match(req)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("history changed the answer:\n%+v\n%+v", a, b)
	}
}

// raceEnabled reports a race-detector build (race_on_test.go sets it). The
// detector slows execution several-fold, so the CPU-time budgets are not held
// under it.
var raceEnabled bool

// TestPerf_LargePageUnderOneSecond is the page budget on the binding path: the
// large controls are questions of pick-option entries, so classification scores
// their options against the entries' values. The time is the thread's CPU time, not
// wall time, and the results are checked so a page that skipped the binding
// cannot pass.
func TestPerf_LargePageUnderOneSecond(t *testing.T) {
	var es []Entry
	for i := 0; i < 60; i++ {
		value := "Choice number 7"
		if i >= 30 {
			value = fmt.Sprintf("Missing value %d", i)
		}
		es = append(es, ent(fmt.Sprintf("picker-%02d", i), "pick-option", withExamples(fmt.Sprintf("Picker example number %d about topic%d", i, i)),
			withDescription(fmt.Sprintf("picker description topic%d words", i)), withPreferred(value)))
	}
	mkOpts := func(n int) []Option {
		o := make([]Option, n)
		for i := range o {
			o[i] = Option{Value: fmt.Sprint(i), Label: fmt.Sprintf("Choice number %d", i)}
		}
		return o
	}
	var qs []Question
	for i := 0; i < 5; i++ {
		qs = append(qs, Question{Selector: fmt.Sprintf("bound%d", i), Text: fmt.Sprintf("Picker example number %d about topic%d", i, i), Kind: KindSelect, Options: mkOpts(268)})
	}
	for i := 30; i < 35; i++ {
		qs = append(qs, Question{Selector: fmt.Sprintf("unbound%d", i), Text: fmt.Sprintf("Picker example number %d about topic%d", i, i), Kind: KindSelect, Options: mkOpts(268)})
	}
	long := strings.Repeat("I acknowledge and agree to the terms of this lengthy attestation notice. ", 42)
	qs = append(qs, Question{Selector: "attest", Text: long[:3000], Kind: KindCheckbox, Required: true})
	for len(qs) < 40 {
		qs = append(qs, textQ(fmt.Sprintf("t%d", len(qs)), fmt.Sprintf("Free text question number %d about topic%d", len(qs), len(qs))))
	}
	m := New(bankOf(es...), DefaultStages())
	routeCatalog() // the catalog compiles once per process, not per request
	best := time.Hour
	for run := 0; run < 3; run++ {
		start := cpuNow()
		resp, err := m.Match(Request{Host: "h", Questions: qs})
		if err != nil || len(resp.Results) != 40 {
			t.Fatalf("err %v results %d", err, len(resp.Results))
		}
		best = min(best, cpuNow()-start)
		for _, r := range resp.Results[:5] {
			wantFill(t, r, "Choice number 7", OutcomeFilled)
		}
		for _, r := range resp.Results[5:10] {
			if r.Outcome != OutcomeAbstained {
				t.Fatalf("an entry whose value no option renders must not fill: %+v", r)
			}
		}
	}
	// The race detector slows execution several-fold; the budget is for a plain build.
	if best > time.Second && !raceEnabled {
		t.Fatalf("matched in %v of CPU time", best)
	}
	t.Logf("best of three: %v", best)
}

// MatchPinned resolves a question against the entry the caller names with the
// resolver order's own guards and binding; it never classifies.
func TestPipeline_MatchPinnedResolvesTheNamedEntryUnderEveryGuard(t *testing.T) {
	m := pickMatcher(
		ent("relatives-at-employer", "fixed", withAnswer("No"), withTypes(InputYesNo), withExamples("Do you have relatives working here?")),
		ent("current-company", "fixed", withAnswer("Example Labs"), withTypes(InputText), withExamples("Current employer")),
		ent("eeo-gender", "pick-option", withPreferred("Male"), withTypes(InputSingleSelect)),
	)
	pin := func(q Question, id string) Result {
		r, err := m.MatchPinned(context.Background(), Request{Host: "jobs.example.com", Questions: []Question{q}}, q, id)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	q := required(radioQ("Are you a relative of any current Example Corp associate?", "Yes", "No"))
	if r := one(m, q); r.Outcome != OutcomeAbstained {
		t.Fatalf("the rules alone should not reach this paraphrase: %+v", r)
	}
	r := pin(q, "relatives-at-employer")
	if r.Outcome == OutcomeAbstained || r.ChosenLabel != "No" || r.EntryID != "relatives-at-employer" || !traceHas(r, "stage pinned") {
		t.Fatalf("pinned entry did not bind its option: %+v", r)
	}
	if r.Confidence > startTopicLane {
		t.Fatalf("a pinned entry starts at the topic lane's confidence, got %.2f", r.Confidence)
	}
	if r := pin(q, "current-company"); r.Outcome != OutcomeAbstained || r.Reason != ReasonInputMismatch {
		t.Fatalf("a control that cannot accept the entry abstains input-mismatch: %+v", r)
	}
	if r := pin(q, "no-such-entry"); r.Outcome != OutcomeAbstained || r.Reason != ReasonNoEntry {
		t.Fatalf("an entry outside the set abstains no-entry: %+v", r)
	}
	if r := pin(textQ("s", "Choose..."), "current-company"); r.Reason != ReasonNoEntry || !traceHas(r, "control's own prompt") {
		t.Fatalf("intake still runs first: %+v", r)
	}
	// The self-identification set check runs whatever entry was named.
	sid := required(selectQ("Please tell us about yourself", "Male", "Female", "I do not wish to answer"))
	if r := pin(sid, "relatives-at-employer"); r.Outcome != OutcomeAbstained && r.EntryID == "relatives-at-employer" {
		t.Fatalf("a self-identification list is never answered from another entry: %+v", r)
	}
}
