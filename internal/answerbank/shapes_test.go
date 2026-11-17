package answerbank

import (
	"strings"
	"testing"

	"applysmith/internal/answerbank/match"
)

// Every listed choice is an answer the matcher gives: with the choice written
// as the page writes it, a form offering every choice of the entry gets it.
func TestShapes_EveryListedChoiceIsPickedOnAFormThatOffersIt(t *testing.T) {
	def, err := DefaultBank()
	if err != nil {
		t.Fatal(err)
	}
	for id := range loadCatalog().choices {
		at := -1
		for i, e := range def.Bank.Entries {
			if e.ID == id {
				at = i
			}
		}
		if at < 0 {
			t.Errorf("%s is listed but no shipped entry has the id", id)
			continue
		}
		e := def.Bank.Entries[at]
		s := ShapeOf(e)
		if e.Kind != match.KindPickOption || s.Type != ShapeChoice {
			continue
		}
		// A form spells each choice as the label, or as its last wording.
		var options, spelled []match.Option
		for _, c := range s.Choices {
			options = append(options, match.Option{Value: c.Label, Label: c.Label})
			w := c.Matches[len(c.Matches)-1]
			spelled = append(spelled, match.Option{Value: w, Label: w})
		}
		question := e.Description
		if len(e.Examples) > 0 {
			question = e.Examples[0]
		}
		for _, c := range s.Choices {
			b := def.Bank.Clone()
			b.Entries[at].Preferred, b.Entries[at].FallbackText, b.Entries[at].Placeholder = c.Matches, c.Label, false
			for i, opts := range [][]match.Option{options, spelled} {
				want := []string{c.Label, c.Matches[len(c.Matches)-1]}[i]
				res, err := match.New(b, match.DefaultStages()).Match(match.Request{Host: "jobs.example.com", Today: match.Date{Year: 2031, Month: 3, Day: 4},
					Questions: []match.Question{{Selector: "q", Text: question, Kind: match.KindSelect, Options: opts}}})
				if err != nil {
					t.Fatal(err)
				}
				if r := res.Results[0]; r.EntryID != id || r.ChosenLabel != want {
					t.Errorf("%s %q: a form offering %q gets %q from %q (%s %s)", id, c.Label, want, r.ChosenLabel, r.EntryID, r.Outcome, r.Reason)
				}
			}
		}
	}
}

// The shipped answers fit their shapes, so no default is refused the first
// time the user saves it unchanged.
func TestShapes_TheShippedAnswersFit(t *testing.T) {
	def, err := DefaultBank()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range def.Bank.Entries {
		if p := AnswerProblem(e); p != "" {
			t.Errorf("%s: %s", e.ID, p)
		}
	}
}

func TestCheckAnswers_RefusesOnlyTheChangedAnswersThatDoNotFit(t *testing.T) {
	entry := func(id string, kind match.Kind, input string, b match.Body) match.Entry {
		return match.Entry{ID: id, Kind: kind, Body: b, Meta: match.Meta{InputTypes: []string{input}}}
	}
	before := match.Bank{Entries: []match.Entry{
		entry("relocate", match.KindFixed, match.InputYesNo, match.Body{Answer: "Yes, for the right role"}),
		entry("start", match.KindFixed, match.InputDate, match.Body{Answer: "2031-05-01"}),
		entry("salutation", match.KindPickOption, match.InputSingleSelect, match.Body{Preferred: []string{"Mr", "Mr."}, FallbackText: "Mr"}),
	}}
	// An answer a form taught earlier stays until it is changed.
	if err := CheckAnswers(before, before.Clone()); err != nil {
		t.Fatalf("an unchanged bank is refused: %v", err)
	}
	cases := []struct {
		name string
		at   int
		body match.Body
		want string
	}{
		{"yes or no", 0, match.Body{Answer: "Maybe"}, "one of: Yes, No"},
		{"date", 1, match.Body{Answer: "next spring"}, "a date"},
		{"choice", 2, match.Body{Preferred: []string{"Sir"}, FallbackText: "Sir"}, "one of: Mr, Ms"},
		{"choice without its wordings", 2, match.Body{Preferred: []string{"Ms"}, FallbackText: "Ms"}, "one of: Mr, Ms"},
	}
	for _, c := range cases {
		after := before.Clone()
		after.Entries[c.at].Body = c.body
		if err := CheckAnswers(before, after); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
	ok := before.Clone()
	ok.Entries[0].Body = match.Body{Answer: "No"}
	ok.Entries[2].Body = match.Body{Preferred: []string{"Ms", "Ms."}, FallbackText: "Ms"}
	if err := CheckAnswers(before, ok); err != nil {
		t.Fatalf("answers that fit are refused: %v", err)
	}
}
