package assist

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/llm"
)

// "Model-assisted lanes: eligibility, budgets and cache"

func TestAnswerBank_Fallback_DisabledByDefaultNoCall(t *testing.T) {
	e := newEnv(t)
	e.respond(func(_, _ string) string { return replyOption("Alpha") })
	res := e.runHand(match.ReasonNoEntry, Settings{LLMFallbackEnabled: false, Mode: ModeStrict}, profileSources(), roleQ(1))
	if res[0].Outcome != match.OutcomeAbstained || res[0].Reason != match.ReasonNoEntry {
		t.Fatalf("disabled lane changed the result: %+v", res[0])
	}
	if n := e.ledgerCalls(""); n != 0 {
		t.Fatalf("%d model calls with the fallback disabled", n)
	}
}

func TestAnswerBank_Fallback_NoRouteNoCall(t *testing.T) {
	m := &noRouteModel{}
	svc := New(Deps{Model: m, Now: func() time.Time { return testNow }})
	req, resp := hand(match.ReasonNoEntry, roleQ(1), textQ("#t", "Describe a project you are proud of.", true))
	got := svc.Run(context.Background(), RunInput{Req: req, Response: resp, Settings: enabled, Sources: profileSources()})
	for i, r := range got {
		if r.Outcome != match.OutcomeAbstained || r.Reason != match.ReasonNoEntry {
			t.Errorf("result %d changed without a route: %+v", i, r)
		}
	}
	if m.calls != 0 {
		t.Fatalf("%d calls without a route", m.calls)
	}
}

func TestAnswerBank_Fallback_TerminalAbstainNeverRetried(t *testing.T) {
	for _, reason := range []match.Reason{match.ReasonSectionPolicy, match.ReasonContextPolicy, match.ReasonDuplicateLink,
		match.ReasonSelfIDPolicy, match.ReasonAskMe, match.ReasonAIInstruction} {
		t.Run(string(reason), func(t *testing.T) {
			e := newEnv(t)
			e.respond(func(_, _ string) string { return replyOption("Alpha") })
			res := e.runHand(reason, enabled, profileSources(), roleQ(1), textQ("#t", "Describe a project you are proud of.", true))
			for _, r := range res {
				if r.Outcome != match.OutcomeAbstained || r.Reason != reason {
					t.Fatalf("a terminal %s abstain was offered to a lane: %+v", reason, r)
				}
			}
			if n := e.ledgerCalls(""); n != 0 {
				t.Fatalf("%d model calls for a terminal abstain", n)
			}
		})
	}
}

func TestLanes_ReasonsThatNeverReachAModel(t *testing.T) {
	for _, reason := range []match.Reason{match.ReasonInputMismatch, match.ReasonPlaceholderEmpty, match.ReasonDestructiveOption} {
		e := newEnv(t)
		e.respond(func(_, _ string) string { return replyOption("Alpha") })
		res := e.runHand(reason, enabled, profileSources(), roleQ(1), textQ("#t", "Describe a project you are proud of.", true))
		for _, r := range res {
			if r.Reason != reason || e.ledgerCalls("") != 0 {
				t.Fatalf("%s reached a model: %+v", reason, r)
			}
		}
	}
}

func TestAnswerBank_Fallback_InputTypeRefusalNotTypedIntoTextBox(t *testing.T) {
	// A Yes/No sponsorship entry refused for a free-text visa-type box stays
	// refused: the box is not filled by any lane and no model is called.
	e := newEnv(t)
	e.respond(func(_, _ string) string { return "H-1B" })
	res := e.runHand(match.ReasonInputMismatch, enabled, profileSources(), textQ("#visa", "What type of visa do you hold?", false))
	if res[0].Outcome != match.OutcomeAbstained || res[0].Answer != "" || e.ledgerCalls("") != 0 {
		t.Fatalf("visa-type box filled: %+v", res[0])
	}
	// Even when the matcher found no entry the composer refuses the shape.
	res = e.runHand(match.ReasonNoEntry, enabled, profileSources(), textQ("#visa", "What type of visa do you hold?", false))
	if res[0].Outcome != match.OutcomeAbstained || e.ledgerCalls("") != 0 {
		t.Fatalf("visa-type box composed: %+v", res[0])
	}
}

func TestAnswerBank_Fallback_PerRequestCapEight(t *testing.T) {
	e := newEnv(t)
	e.respond(func(_, _ string) string { return replyOption("Alpha") })
	var qs []match.Question
	for i := 1; i <= 12; i++ {
		qs = append(qs, roleQ(i))
	}
	res := e.runHand(match.ReasonNoEntry, enabled, profileSources(), qs...)
	filled, exhausted := 0, 0
	for _, r := range res {
		switch {
		case r.Outcome == match.OutcomeFilledModel:
			filled++
		case r.Reason == match.ReasonBudgetExhausted:
			exhausted++
		}
	}
	if filled != 8 || exhausted != 4 {
		t.Fatalf("filled %d, budget-exhausted %d; want 8 and 4", filled, exhausted)
	}
	if n := e.ledgerCalls(TaskOption); n != MaxOptionCalls {
		t.Fatalf("%d option calls in the ledger, want %d", n, MaxOptionCalls)
	}
	for i := 8; i < 12; i++ {
		if res[i].Reason != match.ReasonBudgetExhausted {
			t.Errorf("question %d: %+v", i, res[i])
		}
	}
}

func TestAnswerBank_FreeText_BatchCapAndBillableAccounting(t *testing.T) {
	e := newEnv(t)
	var qs []match.Question
	for i := 1; i <= 7; i++ {
		qs = append(qs, textQ(fmt.Sprintf("#t%d", i), fmt.Sprintf("Describe project number %d that you are proud of and tell us about your background.", i), true))
	}
	src := profileSources()
	e.respond(func(_, p string) string {
		return "I enjoy building reliable software, and I studied computer science at Example University."
	})
	res := e.runHand(match.ReasonNoEntry, enabled, src, qs...)
	filled, exhausted := 0, 0
	for _, r := range res {
		if r.Outcome == match.OutcomeFilledModel {
			filled++
		}
		if r.Reason == match.ReasonBudgetExhausted {
			exhausted++
		}
	}
	if filled != MaxCompositions || exhausted != 2 {
		t.Fatalf("filled %d, exhausted %d; want %d and 2 (%+v)", filled, exhausted, MaxCompositions, res[0])
	}
	// Refusals decided before the call spend nothing.
	e2 := newEnv(t)
	e2.respond(func(_, _ string) string { return "x" })
	e2.runHand(match.ReasonNoEntry, enabled, src, textQ("#r", "What is your religion?", true), textQ("#l", "Please share a link to your portfolio.", false))
	if e2.ledgerCalls("") != 0 || e2.svc.Sessions().Count(SessionKey("jobs.example.com", "Example Corp")) != 0 {
		t.Fatal("a refusal spent budget")
	}
}

func TestAnswerBank_SessionBudget_LimitAndIdleReset(t *testing.T) {
	clock := &testClock{t: testNow}
	s := NewSessions(clock.now)
	key := SessionKey("jobs.example.com", "Example Corp")
	for i := 0; i < SessionCeiling; i++ {
		if !s.Take(key) {
			t.Fatalf("take %d refused", i)
		}
		clock.advance(time.Second) // refreshes the window
	}
	if s.Take(key) || s.Count(key) != SessionCeiling {
		t.Fatal("ceiling not enforced")
	}
	// A check alone does not extend the window.
	clock.advance(SessionIdle + time.Second)
	if s.Count(key) != 0 {
		t.Fatal("an idle session did not reset")
	}
	if !s.Take(key) {
		t.Fatal("fresh session refused a call")
	}
	clock.advance(SessionIdle - time.Second)
	_ = s.Count(key)
	clock.advance(2 * time.Second)
	// 10 minutes and 1 second since the last spend, though a check came between.
	if s.Count(key) != 0 {
		t.Fatal("a check extended the idle window")
	}
}

func TestAnswerBank_SessionBudget_KeyIsHostPlusCompany(t *testing.T) {
	if SessionKey("jobs.example.com", "Acme") == SessionKey("jobs.example.com", "Beta Co") {
		t.Fatal("two companies on one host share a key")
	}
	if SessionKey("jobs.example.com", "Acme") != SessionKey("JOBS.example.com", " acme ") {
		t.Fatal("the same company on one host does not share a key")
	}
	if SessionKey("a.example.com", "Acme") == SessionKey("b.example.com", "Acme") {
		t.Fatal("different hosts share a key")
	}
	if SessionKey("", "") != "unknown" {
		t.Fatal("empty host and company")
	}
	clock := &testClock{t: testNow}
	s := NewSessions(clock.now)
	for i := 0; i < SessionCeiling; i++ {
		s.Take(SessionKey("jobs.example.com", "Acme"))
	}
	if !s.Take(SessionKey("jobs.example.com", "Beta Co")) {
		t.Fatal("company B inherited company A's spent budget")
	}
}

func TestAnswerBank_Fallback_SessionBudgetExhaustedReason(t *testing.T) {
	e := newEnv(t)
	e.respond(func(_, _ string) string { return replyOption("Alpha") })
	key := SessionKey("jobs.example.com", "Example Corp")
	for e.svc.Sessions().Take(key) {
	}
	for _, reason := range []match.Reason{match.ReasonNoEntry, match.ReasonLowConfidence, match.ReasonAmbiguous} {
		res := e.runHand(reason, enabled, profileSources(), roleQ(1))
		if res[0].Reason != match.ReasonBudgetExhausted || res[0].Outcome != match.OutcomeAbstained {
			t.Fatalf("%s: %+v", reason, res[0])
		}
	}
	if e.ledgerCalls("") != 0 {
		t.Fatal("a call was made on an exhausted session")
	}
	// After the idle window the session starts again.
	e.clock.advance(SessionIdle + time.Minute)
	if res := e.runHand(match.ReasonNoEntry, enabled, profileSources(), roleQ(1)); res[0].Outcome != match.OutcomeFilledModel {
		t.Fatalf("session did not reset: %+v", res[0])
	}
}

func TestAnswerBank_Fallback_OnlyModelCallsConsumeBudget(t *testing.T) {
	e := newEnv(t)
	e.respond(func(_, _ string) string { return replyOption("Alpha") })
	key := SessionKey("jobs.example.com", "Example Corp")
	// A refusal, an empty evidence set, a too-many-options question and a cache
	// hit spend nothing.
	qs := []match.Question{
		selectQ("#g", "What is your gender?", "Female", "Male", "Prefer not to say"),
		selectQ("#many", "Which team would you prefer to join?", manyLabels(21)...),
	}
	e.runHand(match.ReasonNoEntry, enabled, profileSources(), qs...)
	if c := e.svc.Sessions().Count(key); c != 0 {
		t.Fatalf("pre-call refusals spent %d calls", c)
	}
	e.runHand(match.ReasonNoEntry, enabled, Sources{}, selectQ("#n", "Which framework do you know best?", "Alpha", "Beta"))
	if c := e.svc.Sessions().Count(key); c != 0 {
		t.Fatalf("an empty evidence set spent %d calls", c)
	}
	e.runHand(match.ReasonNoEntry, enabled, profileSources(), roleQ(1))
	if c := e.svc.Sessions().Count(key); c != 1 {
		t.Fatalf("one model call spent %d", c)
	}
	e.runHand(match.ReasonNoEntry, enabled, profileSources(), roleQ(1))
	if c := e.svc.Sessions().Count(key); c != 1 || e.ledgerCalls(TaskOption) != 1 {
		t.Fatalf("a cache hit spent budget: count %d, calls %d", c, e.ledgerCalls(TaskOption))
	}
}

func manyLabels(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("Team %c%d", 'A'+i%26, i)
	}
	return out
}

func TestAnswerBank_Fallback_OptionCountBounds(t *testing.T) {
	cases := []struct {
		n      int
		reason match.Reason
		calls  int
	}{{0, match.ReasonNoEntry, 0}, {1, match.ReasonNoEntry, 0}, {2, "", 1}, {20, "", 1}, {21, match.ReasonTooManyOptions, 0}}
	for _, c := range cases {
		t.Run(fmt.Sprint(c.n), func(t *testing.T) {
			e := newEnv(t)
			labels := manyLabels(c.n)
			reply := replyOption("Alpha")
			if c.n > 0 {
				reply = replyOption(labels[0])
			}
			e.respond(func(_, _ string) string { return reply })
			q := selectQ("#q", "Which team would you prefer to join?", labels...)
			res := e.runHand(match.ReasonNoEntry, enabled, profileSources(), q)
			if got := e.ledgerCalls(TaskOption); got != c.calls {
				t.Fatalf("%d calls, want %d", got, c.calls)
			}
			if c.reason != "" && res[0].Reason != c.reason {
				t.Fatalf("reason %q, want %q", res[0].Reason, c.reason)
			}
			if c.reason == "" && res[0].Outcome != match.OutcomeFilledModel {
				t.Fatalf("not filled: %+v", res[0])
			}
		})
	}
	// A checkbox group is never offered: one label cannot answer a multi-select.
	e := newEnv(t)
	e.respond(func(_, _ string) string { return replyOption("Alpha") })
	q := roleQ(1)
	q.Kind = match.KindCheckbox
	if res := e.runHand(match.ReasonNoEntry, enabled, profileSources(), q); res[0].Outcome != match.OutcomeAbstained || e.ledgerCalls("") != 0 {
		t.Fatalf("checkbox group offered: %+v", res[0])
	}
}

func TestAnswerBank_Fallback_ContentlessQuestionNeverAsked(t *testing.T) {
	for _, text := range []string{"Select One Required", "Select One *", "  select one  "} {
		e := newEnv(t)
		e.respond(func(_, _ string) string { return replyOption("Yes") })
		res := e.runHand(match.ReasonNoEntry, enabled, profileSources(), selectQ("#c", text, "Select One", "Yes", "No"))
		if res[0].Outcome != match.OutcomeAbstained || e.ledgerCalls("") != 0 || e.svc.Sessions().Count(SessionKey("jobs.example.com", "Example Corp")) != 0 {
			t.Fatalf("%q: %+v", text, res[0])
		}
	}
}

func TestAnswerBank_Fallback_OptionCache(t *testing.T) {
	if CacheTTL != 24*time.Hour {
		t.Fatalf("the option cache window is %s, want 24 hours like free-text composition", CacheTTL)
	}
	e := newEnv(t)
	e.respond(func(_, _ string) string { return replyOption("Alpha") })
	src := profileSources()
	run := func(s Sources) match.Result { return e.runHand(match.ReasonNoEntry, enabled, s, roleQ(1))[0] }
	first := run(src)
	second := run(src)
	if first.ChosenLabel != second.ChosenLabel || e.ledgerCalls(TaskOption) != 1 {
		t.Fatalf("hit: %d calls, %+v / %+v", e.ledgerCalls(TaskOption), first, second)
	}
	// A different fact set is a different question to the cache.
	changed := profileSources()
	changed.Profile[0].Value = "Shelbyville"
	run(changed)
	if e.ledgerCalls(TaskOption) != 2 {
		t.Fatalf("an edited fact did not miss the cache: %d calls", e.ledgerCalls(TaskOption))
	}
	// 24 hours later the reply is asked for again.
	e.clock.advance(CacheTTL + time.Minute)
	run(src)
	if e.ledgerCalls(TaskOption) != 3 {
		t.Fatalf("an expired reply was reused: %d calls", e.ledgerCalls(TaskOption))
	}
	// A failure is not cached.
	f := newEnv(t, llm.Fixture{Task: TaskOption, Contains: "Which team", Steps: []llm.Step{{Fail: llm.KindTimeout}, {Text: replyOption("Alpha")}}})
	if r := f.runHand(match.ReasonNoEntry, enabled, profileSources(), roleQ(1))[0]; r.Reason != match.ReasonUpstreamError {
		t.Fatalf("%+v", r)
	}
	if r := f.runHand(match.ReasonNoEntry, enabled, profileSources(), roleQ(1))[0]; r.Outcome != match.OutcomeFilledModel {
		t.Fatalf("a failed reply was cached: %+v", r)
	}
}

// A model call that fails still spends the request and the session budget: only
// attempts that reach the model count, and a failed attempt is one.
func TestAnswerBank_Fallback_FailedAttemptsStillCount(t *testing.T) {
	e := newEnv(t, llm.Fixture{Task: TaskOption, Contains: "Which team number", Steps: []llm.Step{{Fail: llm.KindTimeout}}})
	var qs []match.Question
	for i := 1; i <= MaxOptionCalls+1; i++ {
		qs = append(qs, roleQ(i))
	}
	res := e.runHand(match.ReasonNoEntry, enabled, profileSources(), qs...)
	for i, r := range res[:MaxOptionCalls] {
		if r.Reason != match.ReasonUpstreamError {
			t.Errorf("question %d: %+v", i, r)
		}
	}
	if last := res[MaxOptionCalls]; last.Reason != match.ReasonBudgetExhausted {
		t.Fatalf("the ninth question after eight failed attempts: %+v", last)
	}
	if c := e.svc.Sessions().Count(SessionKey("jobs.example.com", "Example Corp")); c != MaxOptionCalls {
		t.Fatalf("the session spent %d calls, want %d", c, MaxOptionCalls)
	}
	if n := e.ledgerFailures(TaskOption); n != MaxOptionCalls {
		t.Fatalf("%d failed calls in the ledger", n)
	}
}

// The lane picks one label, so a control that answers with several (a checkbox
// group, or any control the page declares multi-select) never reaches it.
func TestAnswerBank_Fallback_MultiSelectGroupsSkipped(t *testing.T) {
	multi := selectQ("#m", "Which team would you prefer to join?", "Alpha", "Beta", "Gamma")
	multi.InputType = match.InputMultiSelect
	group := match.Question{Selector: "#g", Text: "Which team would you prefer to join?", Kind: match.KindCheckbox, Options: opts("Alpha", "Beta", "Gamma")}
	listbox := match.Question{Selector: "#l", Text: "Which team would you prefer to join?", Kind: match.KindCustomListbox, InputType: match.InputMultiSelect, Options: opts("Alpha", "Beta", "Gamma")}
	e := newEnv(t)
	e.respond(func(_, _ string) string { return replyOption("Alpha") })
	for _, r := range e.runHand(match.ReasonNoEntry, enabled, profileSources(), multi, group, listbox) {
		if r.Outcome != match.OutcomeAbstained || r.Reason != match.ReasonNoEntry {
			t.Errorf("%s: %+v", r.Selector, r)
		}
	}
	if n := e.ledgerCalls(""); n != 0 {
		t.Fatalf("%d model calls for multi-answer controls", n)
	}
	// The same question as a single-answer picker is still offered.
	if r := e.runHand(match.ReasonNoEntry, enabled, profileSources(), selectQ("#s", "Which team would you prefer to join?", "Alpha", "Beta", "Gamma"))[0]; r.Outcome != match.OutcomeFilledModel {
		t.Fatalf("%+v", r)
	}
}

// No model estimates years: a numeric years question with no recorded years is
// not offered to any lane and stays blank.
func TestAnswerBank_Fallback_YearsQuestionAbstainsWithoutRecordedYears(t *testing.T) {
	q := textQ("#y", "How many years of experience do you have with Go?", false)
	q.InputType = match.InputNumber
	e := newEnv(t)
	e.respond(func(_, _ string) string { return "7" })
	res := e.runHand(match.ReasonNoEntry, enabled, profileSources(), q)[0]
	if res.Outcome != match.OutcomeAbstained || res.Answer != "" || e.ledgerCalls("") != 0 {
		t.Fatalf("a years estimate was made: %+v", res)
	}
}

// ---- entry recognition --------------------------------------------------

// recognitionBank holds answered entries the rules know only by their own
// wording, plus entries the lane must never offer.
func recognitionBank() match.Bank {
	yn := func(id, desc, answer, example string) match.Entry {
		e := fixedEntry(id, desc, answer)
		e.InputTypes, e.Examples = []string{string(match.InputYesNo)}, []string{example}
		return e
	}
	sponsor := yn("h1b-sponsorship", "Whether the applicant needs a work visa sponsored", "No", "Will you require visa sponsorship?")
	sponsor.Category = match.CategoryWorkAuthorization
	gender := match.Entry{ID: "eeo-gender", Description: "Voluntary gender self-identification", Kind: match.KindPickOption,
		Body: match.Body{Preferred: []string{"Male"}}, Meta: match.Meta{Category: match.CategorySelfIdentification, InputTypes: []string{string(match.InputSingleSelect)}}}
	blank := yn("drivers-license-held", "Whether the applicant holds a driver's license", "", "Do you have a driver's license?")
	blank.Placeholder = true
	return match.Bank{Entries: []match.Entry{
		yn("relatives-at-employer", "Whether the applicant has relatives employed by the hiring company", "No", "Do you have relatives working here?"),
		yn("willing-to-relocate", "Whether the applicant is willing to relocate", "Yes", "Are you willing to relocate?"),
		yn("skill-experience-knowledge-gate", "Whether the applicant has the experience a screening question names", "Yes", "Do you have this experience?"),
		sponsor, gender, blank,
	}}
}

const relativeQuestion = "Are you a relative of any current Example Corp associate?"

func relativeQ() match.Question {
	return match.Question{Selector: "#rel", Text: relativeQuestion, Kind: match.KindRadioGroup, Options: opts("Yes", "No"), Required: true}
}

// runRecognition matches the questions with the real matcher and offers the
// abstains to the lanes with the matcher's own pinned resolution.
func (e *env) runRecognition(t *testing.T, bank match.Bank, set Settings, qs ...match.Question) []match.Result {
	t.Helper()
	req, resp := matchAll(t, bank, match.Request{Host: "jobs.example.com", Questions: qs})
	m := match.New(bank, match.DefaultStages())
	pin := func(ctx context.Context, i int, id string) (match.Result, bool) {
		r, err := m.MatchPinned(ctx, req, req.Questions[i], id)
		return r, err == nil
	}
	return e.svc.Run(context.Background(), RunInput{Req: req, Response: resp, Settings: set, Sources: Sources{Bank: bank.Entries}, Pin: pin})
}

func recognitionPrompts(e *env) []string {
	var out []string
	for _, p := range e.seen(TaskOption) {
		if strings.Contains(p, "=== SAVED ANSWERS") {
			out = append(out, p)
		}
	}
	return out
}

func TestRecognition_NamedEntryIsResolvedByTheMatcherForReview(t *testing.T) {
	e := newEnv(t)
	bank := recognitionBank()
	if _, resp := matchAll(t, bank, match.Request{Host: "jobs.example.com", Questions: []match.Question{relativeQ()}}); resp.Results[0].Outcome != match.OutcomeAbstained {
		t.Fatalf("the rules alone should leave this paraphrase: %+v", resp.Results[0])
	}
	e.respond(func(task, prompt string) string {
		if strings.Contains(prompt, "=== SAVED ANSWERS") {
			return `{"entry": "relatives-at-employer", "option": "No"}`
		}
		return ""
	})
	r := e.runRecognition(t, bank, enabled, relativeQ())[0]
	if r.Outcome != match.OutcomeFilledReview || r.Source != match.SourceModelEntry || r.EntryID != "relatives-at-employer" || r.ChosenLabel != "No" {
		t.Fatalf("recognised entry not filled for review: %+v", r)
	}
	if r.Confidence > recognizeConfidence {
		t.Fatalf("confidence %.2f above the lane's %.2f", r.Confidence, recognizeConfidence)
	}
	ps := recognitionPrompts(e)
	if len(ps) != 1 {
		t.Fatalf("%d recognition calls, want 1", len(ps))
	}
	p := ps[0]
	if !strings.Contains(p, "id: relatives-at-employer") || !strings.Contains(p, "saved answer: No") || !strings.Contains(p, relativeQuestion) {
		t.Fatalf("prompt lacks the entry or the question:\n%s", p)
	}
	for _, never := range []string{"h1b-sponsorship", "eeo-gender", "drivers-license-held", "skill-experience-knowledge-gate"} {
		if strings.Contains(p, "id: "+never) {
			t.Errorf("the prompt offers %s", never)
		}
	}
	if bank.OwnAnswer(r) {
		t.Fatal("a recognised entry is never the user's own answer to the engine's consent rules")
	}
}

func TestRecognition_TheBinderHasTheLastWord(t *testing.T) {
	for name, reply := range map[string]string{
		"none":            `{"entry": "NONE"}`,
		"unlisted entry":  `{"entry": "h1b-sponsorship", "option": "No"}`,
		"option mismatch": `{"entry": "relatives-at-employer", "option": "Yes"}`,
		"no option":       `{"entry": "relatives-at-employer"}`,
		"unreadable":      `relatives-at-employer`,
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.respond(func(task, prompt string) string {
				if strings.Contains(prompt, "=== SAVED ANSWERS") {
					return reply
				}
				return `{"abstain": true}`
			})
			r := e.runRecognition(t, recognitionBank(), enabled, relativeQ())[0]
			if r.Outcome != match.OutcomeAbstained || r.Source == match.SourceModelEntry {
				t.Fatalf("filled despite %s: %+v", name, r)
			}
			hasNote := false
			for _, l := range r.Trace {
				hasNote = hasNote || strings.HasPrefix(l, "assist: recognition: ")
			}
			if !hasNote {
				t.Fatalf("no recognition trace line: %v", r.Trace)
			}
		})
	}
}

// Color: the model named the count of AI projects built for "which of the
// three possible projects ... resonates the most and why?", and "4" was
// written into an essay box.
func TestRecognition_ABareNumberNeverAnswersWhy(t *testing.T) {
	count := fixedEntry("projects-built", "How many projects the applicant has built", "4")
	count.InputTypes, count.Examples = []string{string(match.InputLongText)}, []string{"Describe an AI project you built"}
	bank := match.Bank{Entries: []match.Entry{count}}
	e := newEnv(t)
	e.respond(func(task, prompt string) string {
		if strings.Contains(prompt, "=== SAVED ANSWERS") {
			return `{"entry": "projects-built"}`
		}
		return `{"abstain": true}`
	})
	q := match.Question{Selector: "#why", Text: "From the job description, which of the three possible projects resonates the most and why?", Kind: match.KindText, InputType: match.InputLongText, Required: true}
	r := e.runRecognition(t, bank, enabled, q)[0]
	if r.Outcome != match.OutcomeAbstained || !strings.Contains(strings.Join(r.Trace, "\n"), "answers a bare number and the question asks why") {
		t.Fatalf("a count answered a why: %+v", r)
	}
}

func TestRecognition_NeverForGatedQuestionsOrWithTheSwitchOff(t *testing.T) {
	e := newEnv(t)
	e.respond(func(task, prompt string) string { return `{"entry": "relatives-at-employer", "option": "No"}` })
	yn := func(sel, text string) match.Question {
		return match.Question{Selector: sel, Text: text, Kind: match.KindRadioGroup, Options: opts("Yes", "No"), Required: true}
	}
	e.runRecognition(t, recognitionBank(), enabled,
		yn("#wa", "Are you legally permitted to take up employment in this country without a visa?"),
		yn("#pay", "Is the posted salary range acceptable to you?"),
		match.Question{Selector: "#sid", Text: "How do you identify?", Kind: match.KindSelect, Options: opts("Male", "Female", "Decline to answer")},
		yn("#ack", "I acknowledge that I have read the privacy notice"),
		yn("#if", "If you selected Other, are any of your relatives Example Corp associates?"),
	)
	if ps := recognitionPrompts(e); len(ps) != 0 {
		t.Fatalf("%d recognition calls for gated questions:\n%s", len(ps), strings.Join(ps, "\n---\n"))
	}
	off := newEnv(t)
	off.respond(func(task, prompt string) string { return `{"entry": "relatives-at-employer", "option": "No"}` })
	r := off.runRecognition(t, recognitionBank(), Settings{Mode: ModeStrict}, relativeQ())[0]
	if r.Outcome != match.OutcomeAbstained || off.ledgerCalls("") != 0 {
		t.Fatalf("recognition ran with the fallback switch off: %+v", r)
	}
}

// fakeEmbedder scores a text by the words it shares with a fixed topic, so a
// test controls the ranking.
type fakeEmbedder struct {
	topic        string
	calls, texts int
	fail         bool
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, string, error) {
	f.calls++
	f.texts += len(texts)
	if f.fail {
		return nil, "", errBoom
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		hit := float32(0)
		if strings.Contains(strings.ToLower(t), f.topic) {
			hit = 1
		}
		out[i] = []float32{hit, 1 - hit}
	}
	return out, "fake-embedding", nil
}

func TestRecognition_ShortlistIsRankedBySimilarityAndEmbeddedOncePerRun(t *testing.T) {
	bank := recognitionBank()
	for k := 0; k < 12; k++ {
		e := fixedEntry(fmt.Sprintf("filler-%02d", k), fmt.Sprintf("Whether the applicant likes colour number %d", k), "Yes")
		e.InputTypes = []string{string(match.InputYesNo)}
		bank.Entries = append(bank.Entries, e)
	}
	for _, fail := range []bool{false, true} {
		e := newEnv(t)
		emb := &fakeEmbedder{topic: "relative", fail: fail}
		e.svc = New(Deps{Model: e.router, Knowledge: e.know, Embedder: emb, Now: e.clock.now, Store: e.st})
		e.respond(func(task, prompt string) string { return `{"entry": "NONE"}` })
		q2 := relativeQ()
		q2.Selector, q2.Text = "#rel2", "Is a relative of yours an Example Corp associate today?"
		e.runRecognition(t, bank, enabled, relativeQ(), q2)
		ps := recognitionPrompts(e)
		if len(ps) != 2 {
			t.Fatalf("fail=%v: %d recognition calls, want 2", fail, len(ps))
		}
		for _, p := range ps {
			if !strings.Contains(p, "[1] id: relatives-at-employer") || strings.Count(p, " id: ") != recognizeShortlist {
				t.Fatalf("fail=%v: the shortlist is not ranked or not cut to %d:\n%s", fail, recognizeShortlist, p)
			}
		}
		// The entries are embedded once per run and each question once; a
		// failed call is not retried within the run.
		if fail && emb.calls != 1 {
			t.Fatalf("a failed embedding was retried: %d calls", emb.calls)
		}
		prepared := relativeQ()
		prepared.InputType = match.InputYesNo
		if n := len(recognitionCandidates(Sources{Bank: bank.Entries}, "jobs.example.com", prepared, []string{"Yes", "No"})); !fail && (n <= recognizeShortlist || emb.texts != n+2) {
			t.Fatalf("%d texts embedded for %d candidates and two questions", emb.texts, n)
		}
	}
}
