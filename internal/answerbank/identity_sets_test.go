package answerbank

import (
	"fmt"
	"testing"

	"applysmith/internal/answerbank/match"
)

// TestSelfIdentification_RecordedAnswerNeverBindsAnotherGroup is the
// self-identification requirement end to end: a recorded gender, race, veteran
// or disability answer is bound only to a label that is that answer, and a set
// that offers only labels which merely contain it, or name someone else's
// status, ends in the terminal self-id-policy abstain instead of a guess.
func TestSelfIdentification_RecordedAnswerNeverBindsAnotherGroup(t *testing.T) {
	cases := []struct {
		name, entry, question, recorded string
		options                         []string
		want                            string // chosen label, or "" for the self-id-policy abstain
	}{
		{"male is not transgender male", "eeo-gender", "Gender", "Male", []string{"Female", "Transgender male", "Non-binary", "Prefer not to say"}, ""},
		{"male is not trans man", "eeo-gender", "Gender", "Male", []string{"Woman", "Trans man", "Non-binary", "Prefer not to say"}, ""},
		{"male picks the whole label", "eeo-gender", "Gender", "Male", []string{"Female", "Male", "Transgender male", "Prefer not to say"}, "Male"},
		{"male is man", "eeo-gender", "Gender", "Male", []string{"Woman", "Man", "Transgender man", "Prefer not to say"}, "Man"},
		{"asian is not east asian", "eeo-race", "Race", "Asian", []string{"White", "East Asian", "Southeast Asian"}, ""},
		{"asian takes the negated qualifier", "eeo-race", "Race", "Asian", []string{"White", "Asian (Not Hispanic or Latino)", "Decline to self-identify"}, "Asian (Not Hispanic or Latino)"},
		{"indian is not american indian", "eeo-race", "Race", "Indian", []string{"American Indian or Alaska Native", "White", "Asian Indian"}, ""},
		{"veteran is not a spouse of one", "eeo-veteran", "Veteran status", "Veteran", []string{"Spouse of a veteran", "Disabled veteran", "Not a veteran", "I prefer not to answer"}, ""},
		{"disability is not a relative's", "eeo-disability", "Disability status", "I have a disability", []string{"Family member of a person with a disability", "I do not have a disability", "I do not want to answer"}, ""},
		{"disability takes its own label", "eeo-disability", "Disability status", "I have a disability", []string{"I have a disability", "I do not have a disability", "I do not want to answer"}, "I have a disability"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bank := defaultBank(t)
			bank.Entries = append([]match.Entry(nil), bank.Entries...)
			for i := range bank.Entries {
				if e := &bank.Entries[i]; e.ID == c.entry {
					e.Preferred, e.NeedsReview = []string{c.recorded}, false
				}
			}
			opts := make([]match.Option, len(c.options))
			for i, l := range c.options {
				opts[i] = match.Option{Value: fmt.Sprint(i), Label: l}
			}
			resp, err := match.New(bank, match.DefaultStages()).Match(match.Request{Host: "jobs.example.com", Questions: []match.Question{
				{Selector: "#q", Text: c.question, Kind: match.KindSelect, Options: opts, Required: true}}})
			if err != nil {
				t.Fatal(err)
			}
			r := resp.Results[0]
			if c.want == "" {
				if r.Outcome != match.OutcomeAbstained || r.Reason != match.ReasonSelfIDPolicy {
					t.Fatalf("want the self-id-policy abstain, got %s %q at %.2f (%s)", r.Outcome, r.ChosenLabel, r.Confidence, r.Reason)
				}
				return
			}
			if r.Outcome == match.OutcomeAbstained || r.ChosenLabel != c.want {
				t.Fatalf("want %q, got %s %q (%s)", c.want, r.Outcome, r.ChosenLabel, r.Reason)
			}
		})
	}
}
