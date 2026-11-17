package answerbank

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"applysmith/internal/answerbank/match"
)

// Self-example invariant. An entry's own example, asked through a control the
// entry accepts, must be filled from that entry once the user has recorded an
// answer for it (or, for a derived entry, the facts it reads). An entry that is
// reached but answers nothing, or that is never reached, is a bug in a route,
// a guard or the example. The one place an abstain is accepted is seAllowed,
// and every row of it cites the requirement that makes the abstain correct.

type seCase struct {
	entry    match.Entry
	example  string
	input    match.InputType
	required bool
}

func (c seCase) name() string {
	req := "required"
	if !c.required {
		req = "optional"
	}
	return fmt.Sprintf("%s/%s/%s/%s", c.entry.ID, c.input, req, strings.ReplaceAll(c.example, " ", "_"))
}

// seCases is every entry x example x accepted input type x requiredness.
func seCases(b match.Bank) []seCase {
	var out []seCase
	for _, e := range b.Entries {
		for _, ex := range e.Examples {
			for _, in := range seInputTypes(e) {
				for _, required := range []bool{true, false} {
					out = append(out, seCase{e, ex, in, required})
				}
			}
		}
	}
	return out
}

// seRun gives the entry its recorded answer, asks the example through a control
// of the input type, and returns what the real matcher did.
func seRun(t *testing.T, c seCase) (match.Entry, match.Result) {
	t.Helper()
	b := defaultBank(t)
	var recorded match.Entry
	for i := range b.Entries {
		if b.Entries[i].ID == c.entry.ID {
			b.Entries[i] = seRecord(b.Entries[i], c.example, c.input)
			recorded = b.Entries[i]
		}
		if answer, ok := seCompanions[c.entry.ID][b.Entries[i].ID]; ok {
			b.Entries[i].Placeholder, b.Entries[i].Answer = false, answer
		}
	}
	resp, err := match.New(b, match.DefaultStages()).Match(match.Request{
		Host:                        "jobs.example.com",
		Posting:                     match.Posting{Company: "Example Corp", Title: "Engineer", Location: "Austin, TX"},
		Questions:                   []match.Question{seQuestion(c.entry, c.example, c.input, c.required)},
		Today:                       seToday,
		Facts:                       seFacts(),
		AutoAcceptLegalDeclarations: true,
	})
	if err != nil || len(resp.Results) != 1 {
		t.Fatalf("err %v, %d results", err, len(resp.Results))
	}
	return recorded, resp.Results[0]
}

// seFill reports a fill by the case's own entry from the recorded answer: the
// chosen label (or typed value) is what the user recorded, or, for a derived
// entry, any value, since the facts decide it.
func seFill(c seCase, rec match.Entry, r match.Result) string {
	if r.Outcome != match.OutcomeFilled && r.Outcome != match.OutcomeFilledReview {
		return fmt.Sprintf("not filled: %s", r.Outcome)
	}
	if r.EntryID != c.entry.ID {
		return fmt.Sprintf("filled from %q", r.EntryID)
	}
	if r.Source == match.SourceModelOption || r.Source == match.SourceComposed {
		return fmt.Sprintf("filled by a model lane (%s)", r.Source)
	}
	got := r.Answer
	if r.ChosenLabel != "" {
		got = r.ChosenLabel
	}
	if want, ok := seWant(c, rec); ok && !strings.EqualFold(got, want) {
		return fmt.Sprintf("answer %q, recorded %q", got, want)
	}
	if got == "" && len(r.ChosenLabels) == 0 {
		return "filled with nothing"
	}
	return ""
}

// seWant is the value the recorded answer must come out as; ok is false where
// the facts or a range decide the value. Every employer-history example names
// the hiring company ("this company"), a current employer in the facts, so it
// answers its match value.
func seWant(c seCase, rec match.Entry) (string, bool) {
	switch {
	case seDerived(rec) || rec.Kind == match.KindCompensationRange || rec.Kind == match.KindMultiSelect:
		return "", false
	case rec.Kind == match.KindAutoAcknowledge && c.input == match.InputYesNo:
		return "Yes", true
	case rec.Kind == match.KindAutoAcknowledge:
		return "I agree", true
	case rec.Kind == match.KindFixed && c.input == match.InputSingleSelect && slices.Contains(rec.InputTypes, string(match.InputNumber)):
		return "", false
	case rec.Kind == match.KindFixed:
		return rec.Answer, true
	case rec.Kind == match.KindEmployerHistory:
		return rec.AnswerIfMatch, true
	case rec.Kind == match.KindLanguageProficiency && c.input == match.InputYesNo:
		return "Yes", true
	case rec.Kind == match.KindLanguageProficiency:
		return rec.Speaking, true
	case rec.Kind == match.KindPickOption && rec.Category == match.CategorySelfIdentification && c.input != match.InputYesNo && c.input != match.InputText:
		return rec.Preferred[0], true
	case rec.Kind == match.KindPickOption && len(rec.Preferred) > 0 && (c.input == match.InputSingleSelect || c.input == match.InputMultiSelect || c.input == match.InputYesNo || c.input == match.InputCheckbox):
		return rec.Preferred[0], true
	case rec.Kind == match.KindPickOption:
		return rec.FallbackText, true
	}
	return "", false
}

func TestDefaultBank_EveryExampleFillsFromItsOwnEntry(t *testing.T) {
	b := defaultBank(t)
	for _, e := range b.Entries {
		if len(e.Examples) == 0 {
			t.Errorf("%s has no example to ask", e.ID)
		}
	}
	cases := seCases(b)
	if len(cases) < 600 {
		t.Fatalf("only %d cases built", len(cases))
	}
	t.Logf("%d entries, %d entry x example x input type x requiredness cases", len(b.Entries), len(cases))
	used := map[int]bool{}
	for _, c := range cases {
		t.Run(c.name(), func(t *testing.T) {
			rec, r := seRun(t, c)
			why := seFill(c, rec, r)
			if why == "" {
				return
			}
			i := slices.IndexFunc(seAllowed, func(a seAbstain) bool { return a.covers(c, r) })
			if i < 0 {
				t.Fatalf("%s; outcome %s, entry %q, source %q, reason %q, detail %q, answer %q\ntrace: %s",
					why, r.Outcome, r.EntryID, r.Source, r.Reason, r.Detail, r.Answer, strings.Join(r.Trace, " | "))
			}
			used[i] = true
		})
	}
	for i, a := range seAllowed {
		if !used[i] {
			t.Errorf("allowlist row %d (%v) matched no case: remove it", i, a.ids)
		}
	}
}

// seAbstain is one reviewed exception: an abstain the specification requires.
type seAbstain struct {
	ids      []string
	optional bool // only the control that is not required
	reason   match.Reason
	detail   match.Detail
	cite     string
}

func (a seAbstain) covers(c seCase, r match.Result) bool {
	return slices.Contains(a.ids, c.entry.ID) && a.optional == !c.required &&
		r.Outcome == match.OutcomeAbstained && r.EntryID == c.entry.ID && r.Reason == a.reason && r.Detail == a.detail
}

// seAllowed is the allowlist. A row may not accept an entry whose example is
// never reached (no-entry, input-mismatch, ambiguous, low-confidence), a
// narrative deferral, a blank placeholder of an entry that is derived or was
// answered, or a statement about this application.
var seAllowed = []seAbstain{
	{
		ids:      []string{"mandatory-acknowledge", "privacy-policy-consent", "truth-certification"},
		optional: true,
		reason:   match.ReasonContextPolicy,
		detail:   match.DetailMarketingOptIn,
		cite: `answer-bank spec, "Legal declarations, signatures and statements about the application", ` +
			`acknowledgement order step 3: a control that is not required is never ticked`,
	},
}
