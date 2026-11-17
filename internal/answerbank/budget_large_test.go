package answerbank

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"applysmith/internal/answerbank/match"
)

// raceEnabled reports a race-detector build (race_on_test.go sets it). The
// detector slows execution several-fold, so the CPU-time budgets are not held
// under it.
var raceEnabled bool

// largeOptions renders n options of w words each, drawn from a vocabulary heavy
// in the negations, stance words and category cues the scorer works hardest on.
// The labels are all distinct and none is a decline, so no option binds and the
// matcher scans every option for every search value: the worst case.
func largeOptions(n, w, seed int) []match.Option {
	words := strings.Fields("not veteran disability yes no am do have protected never without sponsorship require 10 years 25% 120k but however except male female asian hispanic latino degree school gender race")
	o := make([]match.Option, n)
	for i := range o {
		b := make([]string, 0, w+1)
		for j := 0; j < w; j++ {
			b = append(b, words[(i*7+j*13+seed)%len(words)])
		}
		o[i] = match.Option{Value: fmt.Sprint(i), Label: strings.Join(b, " ") + fmt.Sprint(" ", i)}
	}
	return o
}

// bindingTexts are questions whose entries are pick-options with preferred
// values, so classification reaches the option-binding check.
var bindingTexts = []string{
	"Are you a protected veteran?", "Do you have a disability?", "Will you now or in the future require sponsorship?",
	"What is your gender?", "What is your race?", "Highest level of education", "Country", "How did you hear about us?",
}

// answeredBank is the default bank as a user who answered it leaves it: every
// pick-option entry carries values, none of which any large option renders, so
// binding must scan every option for every value of every entry it considers.
func answeredBank(t *testing.T, preferred ...string) match.Bank {
	t.Helper()
	b := defaultBank(t)
	b.Entries = append([]match.Entry(nil), b.Entries...)
	for i := range b.Entries {
		if e := &b.Entries[i]; e.Kind == match.KindPickOption && len(e.Preferred) == 0 {
			e.Preferred = append([]string(nil), preferred...)
		}
	}
	return b
}

// largePage renders controls selects of options options each. With distinct
// false every control asks the same question; with distinct true each asks a
// different one, so each is routed to a different pick-option entry.
func largePage(controls, options, words int, distinct bool) []match.Question {
	qs := make([]match.Question, controls)
	for i := range qs {
		text := bindingTexts[0]
		if distinct {
			text = bindingTexts[i%len(bindingTexts)]
		}
		qs[i] = match.Question{Selector: fmt.Sprintf("q%d", i), Text: text,
			Kind: match.KindSelect, Options: largeOptions(options, words, i), Required: true}
	}
	return qs
}

// TestMatcher_LargeControlsStayWithinTheCPUBudget holds the spec's "several
// controls of 250 or more options within the request budget" on 500-option
// controls of three and of twenty words, in the CPU time of the matching thread
// (best of three), so the budget measures the matcher's work rather than the
// machine's load or other goroutines.
func TestMatcher_LargeControlsStayWithinTheCPUBudget(t *testing.T) {
	if raceEnabled {
		t.Skip("CPU budgets are measured without the race detector")
	}
	m := match.New(answeredBank(t, "Zebra crossing", "Moon landing", "Harbour lights"), match.DefaultStages())
	for _, c := range []struct {
		name                     string
		controls, options, words int
		distinct                 bool
	}{
		{"500 options of three words, five controls", 5, 500, 3, false},
		{"500 options of twenty words, five controls", 5, 500, 20, false},
		{"500 options of three words, five different questions", 5, 500, 3, true},
		{"500 options of twenty words, five different questions", 5, 500, 20, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			qs := largePage(c.controls, c.options, c.words, c.distinct)
			best := time.Hour
			for range 3 {
				start := cpuNow()
				resp, err := m.Match(match.Request{Host: "jobs.example.com", Questions: qs})
				if err != nil || len(resp.Results) != len(qs) {
					t.Fatalf("err %v, %d results", err, len(resp.Results))
				}
				best = min(best, cpuNow()-start)
			}
			if best >= time.Second {
				t.Fatalf("%d controls matched in %v of CPU time; the budget is 1,000 ms", c.controls, best)
			}
			t.Logf("best of three: %v", best)
		})
	}
}

// TestMatcher_StopsAtTheContextAndAbstains is the budget stop: a request whose
// context is done scores nothing further, and every control it had not finished
// abstains with budget-exhausted instead of the matcher overrunning.
func TestMatcher_StopsAtTheContextAndAbstains(t *testing.T) {
	m := match.New(answeredBank(t, "Zebra crossing"), match.DefaultStages())
	qs := largePage(8, 500, 20, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := cpuNow()
	resp, err := m.MatchContext(ctx, match.Request{Host: "jobs.example.com", Questions: qs})
	spent := cpuNow() - start
	if err != nil || len(resp.Results) != len(qs) {
		t.Fatalf("err %v, %d results", err, len(resp.Results))
	}
	for _, r := range resp.Results {
		if r.Outcome != match.OutcomeAbstained || r.Reason != match.ReasonBudgetExhausted {
			t.Fatalf("a control after the budget must abstain budget-exhausted: %+v", r)
		}
	}
	if spent > 100*time.Millisecond {
		t.Fatalf("a request with a done context still spent %v of CPU time", spent)
	}
}

// TestMatcher_ContextEndingMidControlAbstainsThatControl cancels the context
// while a large control is being scored and checks that the control abstains and
// the scan stops well before it would have finished.
func TestMatcher_ContextEndingMidControlAbstainsThatControl(t *testing.T) {
	m := match.New(answeredBank(t, "Zebra crossing"), match.DefaultStages())
	qs := largePage(8, 500, 20, true)
	var one time.Duration
	for range 2 { // the second run is warm, so one is a control's own cost
		full := cpuNow()
		if _, err := m.Match(match.Request{Host: "jobs.example.com", Questions: qs[:1]}); err != nil {
			t.Fatal(err)
		}
		one = cpuNow() - full
	}
	ctx, cancel := context.WithTimeout(context.Background(), max(one/10, time.Millisecond))
	defer cancel()
	start := cpuNow()
	resp, err := m.MatchContext(ctx, match.Request{Host: "jobs.example.com", Questions: qs})
	spent := cpuNow() - start
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []match.Result{resp.Results[0], resp.Results[len(qs)-1]} {
		if r.Outcome != match.OutcomeAbstained || r.Reason != match.ReasonBudgetExhausted {
			t.Fatalf("the interrupted control and the last one must abstain budget-exhausted: %+v", r)
		}
	}
	// The first control is cut off at the deadline: a matcher that checked the
	// context only between controls would spend one whole control first.
	if spent > one/2 {
		t.Fatalf("the stopped request spent %v; one full control costs %v", spent, one)
	}
}

// TestMatcher_RepeatedListKeepsEachControlsValues: controls that repeat one
// option list share its scoring, but each answer is its own control's value.
func TestMatcher_RepeatedListKeepsEachControlsValues(t *testing.T) {
	m := match.New(answeredBank(t, "LinkedIn"), match.DefaultStages())
	opts := func(prefix string) []match.Option {
		return []match.Option{{Value: prefix + "1", Label: "Company website"}, {Value: prefix + "2", Label: "LinkedIn"}}
	}
	text := "How did you hear about us?"
	qs := []match.Question{
		{Selector: "q0", Text: text, Kind: match.KindSelect, Options: opts("a"), Required: true},
		{Selector: "q1", Text: text, Kind: match.KindSelect, Options: opts("b"), Required: true},
	}
	resp, err := m.Match(match.Request{Host: "jobs.example.com", Questions: qs})
	if err != nil {
		t.Fatal(err)
	}
	if a, b := resp.Results[0], resp.Results[1]; a.Answer != "a2" || b.Answer != "b2" {
		t.Fatalf("answers %q (%s) and %q (%s); want each control's own value\n%v", a.Answer, a.Outcome, b.Answer, b.Outcome, a.Trace)
	}
}
