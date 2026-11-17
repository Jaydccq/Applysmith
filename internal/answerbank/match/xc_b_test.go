package match_test

import (
	"strings"
	"testing"

	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/match"
)

// The cases below send the question shapes of the incident suites through the
// whole matcher, over the shipped default bank and the shipped routes, with
// only the user's own answers filled in. The default bank carries no personal
// fact, so each case states the answers the incident's user had recorded.

type xcBSet func(*match.Entry)

func xcBAns(a string) xcBSet { return func(e *match.Entry) { e.Answer = a; e.Placeholder = false } }
func xcBPref(p ...string) xcBSet {
	return func(e *match.Entry) { e.Preferred = p; e.Placeholder = false }
}
func xcBComp(def string) xcBSet {
	return func(e *match.Entry) { e.DefaultAnswer = def; e.Placeholder = false }
}
func xcBSel(s ...string) xcBSet {
	return func(e *match.Entry) { e.Selected = append([]string{}, s...); e.Placeholder = false }
}
func xcBHistory(yes, no string) xcBSet {
	return func(e *match.Entry) { e.AnswerIfMatch, e.AnswerIfNoMatch, e.Placeholder = yes, no, false }
}

func xcBBank(t *testing.T, sets map[string]xcBSet) match.Bank {
	t.Helper()
	r, err := answerbank.DefaultBank()
	if err != nil {
		t.Fatal(err)
	}
	b := r.Bank
	seen := map[string]bool{}
	for i := range b.Entries {
		if f, ok := sets[b.Entries[i].ID]; ok {
			f(&b.Entries[i])
			seen[b.Entries[i].ID] = true
		}
	}
	for id := range sets {
		if !seen[id] {
			t.Fatalf("the default bank has no entry %q", id)
		}
	}
	return b
}

func xcBOpts(labels ...string) []match.Option {
	out := make([]match.Option, len(labels))
	for i, l := range labels {
		out[i] = match.Option{Value: l, Label: l}
	}
	return out
}

func xcBQ(text string, kind match.ControlKind, labels ...string) match.Question {
	return match.Question{Selector: "s", Text: text, Kind: kind, Options: xcBOpts(labels...)}
}

func xcBYesNo(text string) match.Question {
	return xcBQ(text, match.KindRadioGroup, "Yes", "No")
}

func xcBText(text string) match.Question {
	return match.Question{Selector: "s", Text: text, Kind: match.KindText}
}

func xcBBool(v bool) *bool { return &v }

// xcBCase is one incident input and the outcome the incident requires.
type xcBCase struct {
	name    string
	sets    map[string]xcBSet
	facts   match.Facts
	today   match.Date
	posting match.Posting
	// legal is the runtime setting that ticks required legal declarations.
	legal  bool
	q      match.Question
	detail match.Detail
	// add holds entries the user's own bank would carry under the ids the
	// routes name and the default bank does not ship.
	add []match.Entry
	// A fill names the entry and, when set, the chosen label or answer. An
	// abstain (wantNone) may name the entry that claimed the question.
	entry, label, answer string
	wantNone             bool
	reason               match.Reason
	// notEntry is an entry that must not have claimed the question.
	notEntry string
}

var xcBDay = match.Date{Year: 2031, Month: 7, Day: 4}

func xcBRunCases(t *testing.T, cases []xcBCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bank := xcBBank(t, c.sets)
			bank.Entries = append(bank.Entries, c.add...)
			m := match.New(bank, match.DefaultStages())
			today := c.today
			if today == (match.Date{}) {
				today = xcBDay
			}
			resp, err := m.Match(match.Request{Host: "jobs.example.com", Facts: c.facts, Today: today, Posting: c.posting, AutoAcceptLegalDeclarations: c.legal, Questions: []match.Question{c.q}})
			if err != nil {
				t.Fatal(err)
			}
			r := resp.Results[0]
			desc := func() string {
				return strings.Join([]string{string(r.Outcome), "entry=" + r.EntryID, "label=" + r.ChosenLabel, "answer=" + r.Answer, "reason=" + string(r.Reason)}, " ")
			}
			t.Log(desc())
			if c.notEntry != "" && r.EntryID == c.notEntry && r.Outcome != match.OutcomeAbstained {
				t.Fatalf("%q was claimed by %s: %s", c.q.Text, c.notEntry, desc())
			}
			if c.wantNone {
				if r.Outcome != match.OutcomeAbstained || (c.reason != "" && r.Reason != c.reason) || (c.detail != "" && r.Detail != c.detail) || (c.entry != "" && r.EntryID != c.entry) {
					t.Fatalf("%q must abstain: %s", c.q.Text, desc())
				}
				return
			}
			if r.Outcome == match.OutcomeAbstained || (c.entry != "" && r.EntryID != c.entry) ||
				(c.label != "" && r.ChosenLabel != c.label) || (c.answer != "" && r.Answer != c.answer) {
				t.Fatalf("%q: %s", c.q.Text, desc())
			}
		})
	}
}

func xcBTypes(t ...string) xcBSet { return func(e *match.Entry) { e.InputTypes = t } }
