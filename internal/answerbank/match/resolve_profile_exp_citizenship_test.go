package match

import (
	"slices"
	"testing"
)

func citizenshipEntry(kind string, claims ...string) Entry {
	e := ent("country-of-citizenship", kind)
	if kind == "fixed" {
		e.Answer = claims[0]
		return e
	}
	e.Preferred = claims
	return e
}

func TestAnswerBank_Derived_ListedCountryCitizenship(t *testing.T) {
	listBefore := "Export controls apply to the countries listed above: Atlantis, Testland, Exampleland. Are you a citizen of any of the countries listed above?"
	listAfter := "Are you a citizen or national of any of the following countries: Atlantis, Testland, Exampleland?"
	const exportLaw = "To comply with United States export control laws, are you a citizen of any of the following countries: Cuba, Iran, North Korea, Syria?"
	cases := []struct {
		name  string
		entry Entry
		q     string
		want  string
	}{
		{"a citizenship the question names outside its list", citizenshipEntry("pick-option", "United States"), exportLaw, ""},
		{"a citizenship named in prose before a list above", citizenshipEntry("fixed", "United States"), "Under United States law the countries listed above are embargoed. Are you a citizen of any country listed above?", ""},
		{"a citizenship that is an item of that list", citizenshipEntry("fixed", "North Korea"), exportLaw, "Yes"},
		{"pick-option claim, list before", citizenshipEntry("pick-option", "Testland"), listBefore, "Yes"},
		{"fixed claim, list after", citizenshipEntry("fixed", "Testland"), listAfter, "Yes"},
		{"case-insensitive whole word", citizenshipEntry("pick-option", "testland"), listAfter, "Yes"},
		{"absent from the list is never No", citizenshipEntry("pick-option", "Elsewhere"), listAfter, ""},
		{"a substring is not a word", citizenshipEntry("pick-option", "Test"), listAfter, ""},
		{"a claim under three characters is ignored", citizenshipEntry("pick-option", "Te"), "Citizen of any of the following countries: Te, Atlantis?", ""},
		{"not a citizenship ask", citizenshipEntry("pick-option", "Testland"), "Do you work in any of the following countries: Testland?", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := pfOne(t, pfMatcher(c.entry), Facts{}, pfDate(2026, 10, 1), yesNoQ("s", c.q))
			if c.want == "" {
				if r.Outcome != OutcomeAbstained {
					t.Fatalf("answered: %+v", r)
				}
				return
			}
			if r.Outcome != OutcomeFilled || r.ChosenLabel != c.want || r.Source != SourceDerived || r.Confidence != startProfileDerive {
				t.Fatalf("result = %+v", r)
			}
			if r.EntryID != "country-of-citizenship" {
				t.Fatalf("entry id = %q", r.EntryID)
			}
		})
	}
	t.Run("an option-less box takes the literal Yes", func(t *testing.T) {
		r := pfOne(t, pfMatcher(citizenshipEntry("fixed", "Testland")), Facts{}, pfDate(2026, 10, 1), textQ("s", listAfter))
		if r.Answer != "Yes" || r.Outcome != OutcomeFilled {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("a control without a Yes option is left alone", func(t *testing.T) {
		q := pfSelect("s", listAfter, "Agree", "Disagree")
		r := pfOne(t, pfMatcher(citizenshipEntry("fixed", "Testland")), Facts{}, pfDate(2026, 10, 1), q)
		if r.Source == SourceDerived {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("no entry, no answer", func(t *testing.T) {
		r := pfOne(t, pfMatcher(), Facts{}, pfDate(2026, 10, 1), yesNoQ("s", listAfter))
		if r.Source == SourceDerived {
			t.Fatalf("result = %+v", r)
		}
	})
}

// Twitch (Greenhouse, live): "In which country/region do you have
// citizenship?" is a multi-value list of every country, so a dual national can
// pick each; it arrives without its options, past the engine's cap, and the
// citizenship entry, which picks one option, was refused for its shape. The
// list takes every recorded citizenship; one asking a second citizenship is
// left to its own lane.
func TestAnswerBank_Derived_CitizenshipAsAMultiValueList(t *testing.T) {
	const ask = "In which country/region do you have citizenship?"
	entry := citizenshipEntry("pick-option", "China", "Mainland China", "People's Republic of China")
	entry.InputTypes = []string{string(InputSingleSelect)}
	list := func(text string, options ...string) Question {
		q := Question{Selector: "s", Text: text, Kind: KindCustomListbox, InputType: InputMultiSelect, Required: true}
		for _, o := range options {
			q.Options = append(q.Options, Option{Label: o, Value: o})
		}
		return q
	}
	today := pfDate(2026, 10, 1)
	r := pfOne(t, pfMatcher(entry), Facts{}, today, list(ask))
	if r.Outcome == OutcomeAbstained || !slices.Equal(r.ChosenLabels, []string{"China"}) || r.EntryID != "country-of-citizenship" {
		t.Fatalf("one citizenship, no options: %+v", r)
	}
	dual := Facts{Citizenships: []string{"CN", "CA"}}
	r = pfOne(t, pfMatcher(entry), dual, today, list(ask, "Canada", "China", "France"))
	if r.Outcome == OutcomeAbstained || !slices.Equal(r.ChosenLabels, []string{"China", "Canada"}) {
		t.Fatalf("two citizenships over options: %+v", r)
	}
	r = pfOne(t, pfMatcher(entry), Facts{}, today, list(ask, "Canada", "France"))
	if r.Source == SourceDerived {
		t.Fatalf("a list without the citizenship: %+v", r)
	}
	r = pfOne(t, pfMatcher(entry), dual, today, list("Which country is your second country of citizenship?", "Canada", "China", "France"))
	if r.Source == SourceDerived {
		t.Fatalf("a second citizenship: %+v", r)
	}
}
