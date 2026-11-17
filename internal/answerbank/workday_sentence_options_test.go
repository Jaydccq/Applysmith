package answerbank

import (
	"testing"

	"applysmith/internal/answerbank/match"
)

// Workday's two standard questions are custom listboxes whose options are full
// sentences. The answer is the option whose sentence states the user's own
// recorded fact, never a default.
func workdaySentenceOptions(yes, no string) []match.Option {
	return []match.Option{{Value: "y", Label: yes}, {Value: "n", Label: no}}
}

func TestWorkday_AuthorizationSentenceOptionsFollowTheRecordedAnswer(t *testing.T) {
	const q = "Are you authorized to work lawfully in the country to which you are applying?"
	opts := workdaySentenceOptions(
		"Yes, I am authorized to work lawfully in the country to which I am applying.",
		"No, I am not authorized to work lawfully in the country to which I am applying.")
	for stored, want := range map[string]string{"Yes": opts[0].Label, "No": opts[1].Label} {
		m := match.New(bankAnswering(t, stored), match.DefaultStages())
		r := askYesNo(t, m, match.Facts{AuthorizedCountries: []string{"US"}}, q, opts)
		if r.Outcome == match.OutcomeAbstained || r.ChosenLabel != want {
			t.Errorf("recorded %s: got %s %q (%s), want %q", stored, r.Outcome, r.ChosenLabel, r.Reason, want)
		}
	}
}

func TestWorkday_SponsorshipSentenceOptionsFollowBothFacts(t *testing.T) {
	const q = "Will you now or in the future require sponsorship for an employment-based visa status?"
	yes := "Yes, I will require sponsorship for an employment-based visa status."
	no := "No, I will not require sponsorship for an employment-based visa status."
	opts := workdaySentenceOptions(yes, no)
	m := match.New(defaultBank(t), match.DefaultStages())
	cases := []struct {
		now, future *bool
		want        string // empty: left to the user
	}{
		{boolPtr(false), boolPtr(false), no},
		{boolPtr(false), boolPtr(true), yes},
		{boolPtr(true), boolPtr(false), yes},
		{boolPtr(false), nil, ""},
		{nil, nil, ""},
	}
	for _, c := range cases {
		facts := match.Facts{AuthorizedCountries: []string{"US"}, RequiresSponsorshipNow: c.now, RequiresSponsorshipFuture: c.future}
		r := askYesNo(t, m, facts, q, opts)
		switch {
		case c.want == "" && r.Outcome != match.OutcomeAbstained:
			t.Errorf("now=%v future=%v: an unset fact must not be answered, got %q", c.now, c.future, r.ChosenLabel)
		case c.want != "" && (r.Outcome == match.OutcomeAbstained || r.ChosenLabel != c.want):
			t.Errorf("now=%v future=%v: got %s %q (%s), want %q", c.now, c.future, r.Outcome, r.ChosenLabel, r.Reason, c.want)
		}
	}
	// A split of the two tenses is not the either-or question.
	r := askYesNo(t, m, match.Facts{AuthorizedCountries: []string{"US"}, RequiresSponsorshipNow: boolPtr(true), RequiresSponsorshipFuture: boolPtr(false)},
		"Will you require sponsorship for an employment-based visa status now but not in the future?", opts)
	if r.Outcome != match.OutcomeAbstained {
		t.Errorf("a contrasted-tense wording must be left to the user, got %s %q", r.Outcome, r.ChosenLabel)
	}
}
