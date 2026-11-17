package answerbank

import (
	"testing"

	"applysmith/internal/answerbank/match"
)

func countryAsk(t *testing.T, m *match.Matcher, facts match.Facts, location, text string) match.Result {
	t.Helper()
	resp, err := m.Match(match.Request{Host: "jobs.example.com", Facts: facts,
		Posting:   match.Posting{Company: "Example Corp", Location: location},
		Questions: []match.Question{{Selector: "#q", Text: text, Kind: match.KindRadioGroup, Options: yesNo, Required: true}}})
	if err != nil {
		t.Fatal(err)
	}
	return resp.Results[0]
}

func countryFacts(need bool, countries ...string) match.Facts {
	return match.Facts{AuthorizedCountries: countries, RequiresSponsorshipNow: boolPtr(need), RequiresSponsorshipFuture: boolPtr(need)}
}

// TestCountry_CountryMismatch is the "Country mismatch" and "Other-country
// eligibility" scenarios of Work authorization and sponsorship: a question about
// the posting's country or another country is the terminal posting_country
// abstain unless the user's facts cover that country, wherever the posting is
// placed and whichever sponsorship facts are recorded (a posting nothing places
// is presumed in the United States, see the matcher's own table).
func TestCountry_CountryMismatch(t *testing.T) {
	m := match.New(bankAnswering(t, "Yes"), match.DefaultStages())
	for _, text := range []string{
		"Are you legally authorized to work in this country?",
		"Will you require sponsorship to work in this country?",
		"Do you require visa sponsorship to work in the EU?",
		"Will you require sponsorship for a role in Canada?",
	} {
		for _, need := range []bool{false, true} {
			for _, location := range []string{"London, United Kingdom", "Berlin, Germany", "Toronto, ON, Canada"} {
				r := countryAsk(t, m, countryFacts(need, "US"), location, text)
				if r.Outcome != match.OutcomeAbstained || r.Reason != match.ReasonContextPolicy || r.Detail != match.DetailPostingCountry || !r.Terminal || r.Confidence != 0 {
					t.Errorf("%q at %q (need=%t): %s %q reason=%s detail=%s confidence=%.2f", text, location, need, r.Outcome, r.ChosenLabel, r.Reason, r.Detail, r.Confidence)
				}
			}
		}
	}
}

func TestCountry_FactsCoverTheQuestionCountry(t *testing.T) {
	m := match.New(bankAnswering(t, "Yes"), match.DefaultStages())
	cases := []struct {
		text, location string
		countries      []string
		need           bool
		want           string // empty: the posting_country abstain
	}{
		{"Are you legally authorized to work in this country?", "Austin, TX", []string{"US"}, false, "Yes"},
		{"Will you require sponsorship to work in this country?", "Austin, TX", []string{"US"}, false, "No"},
		{"Will you require sponsorship to work in this country?", "Austin, TX", []string{"US"}, true, "Yes"},
		{"Are you legally authorized to work in this country?", "London, United Kingdom", []string{"US", "GB"}, false, "Yes"},
		{"Are you legally authorized to work in this country?", "London, UK", []string{"US"}, false, ""},
		{"Do you require visa sponsorship to work in the EU?", "", []string{"EU"}, false, "No"},
		{"Do you require visa sponsorship to work in the EU?", "", []string{"DE"}, false, ""},
		{"Do you require visa sponsorship to work in Germany?", "", []string{"EU"}, true, "Yes"},
		{"Will you require sponsorship for a role in Canada?", "", []string{"US", "CA"}, true, "Yes"},
		{"Will you require sponsorship for a role in Canada?", "Austin, TX", []string{"US"}, false, ""},
		{"Are you legally authorized to work in the United Kingdom?", "", []string{"US"}, false, ""},
		{"Are you authorized to work in the United States?", "London, United Kingdom", []string{"US"}, false, "Yes"},
		{"Are you legally authorized to work?", "Berlin, Germany", []string{"US"}, false, ""},
		{"Are you legally authorized to work?", "Berlin, Germany", []string{"DE"}, false, "Yes"},
		{"Are you legally authorized to work?", "", []string{"US"}, false, "Yes"},
		{"Will you require sponsorship?", "Toronto, ON, Canada", []string{"US"}, false, ""},
		{"Will you require sponsorship?", "Toronto, ON, Canada", []string{"CA"}, true, "Yes"},
		{"Can you work here without sponsorship?", "Austin, TX", []string{"US"}, false, "Yes"},
		// Owner decision: a posting whose location names no country is placed
		// in the one recorded country, for review.
		{"Are you legally authorized to work in this country?", "Remote", []string{"US"}, false, "Yes"},
		{"Are you legally authorized to work in this country?", "Remote, US", []string{"US"}, false, "Yes"},
	}
	for _, c := range cases {
		r := countryAsk(t, m, countryFacts(c.need, c.countries...), c.location, c.text)
		if c.want == "" {
			if r.Outcome != match.OutcomeAbstained || r.Detail != match.DetailPostingCountry {
				t.Errorf("%q at %q with %v: %s %q reason=%s detail=%s", c.text, c.location, c.countries, r.Outcome, r.ChosenLabel, r.Reason, r.Detail)
			}
			continue
		}
		if r.Outcome == match.OutcomeAbstained || r.ChosenLabel != c.want {
			t.Errorf("%q at %q with %v: %s %q (%s) want %q", c.text, c.location, c.countries, r.Outcome, r.ChosenLabel, r.Reason, c.want)
		}
	}
}

// TestCountry_TransferIsNotSponsorship: an H-1B transfer is a different fact
// from sponsorship and the profile records none, so it is never answered from
// the sponsorship facts.
func TestCountry_TransferIsNotSponsorship(t *testing.T) {
	m := match.New(bankAnswering(t, "Yes"), match.DefaultStages())
	for _, text := range []string{
		"Do you require an H-1B transfer?",
		"Will you require H-1B transfer sponsorship?",
		"Do you require sponsorship to continue working in the US (e.g. H-1B transfer)?",
	} {
		for _, need := range []bool{false, true} {
			if r := countryAsk(t, m, countryFacts(need, "US"), "Austin, TX", text); r.Outcome != match.OutcomeAbstained {
				t.Errorf("%q (need=%t) was answered %q", text, need, r.ChosenLabel)
			}
		}
	}
}

func TestCountry_CommonWordingsAreRead(t *testing.T) {
	m := match.New(bankAnswering(t, "Yes"), match.DefaultStages())
	cases := []struct {
		text string
		need bool
		want string
	}{
		{"Do you now or will you in the future require visa sponsorship?", true, "Yes"},
		{"Do you now or will you in the future require visa sponsorship?", false, "No"},
		{"Will you now or in the future require the Company to sponsor an immigration case?", true, "Yes"},
		{"Will you now or in the future require Example Corp to sponsor an immigration case in order to employ you (for example, H-1B visa case)?", false, "No"},
		{"Are you eligible to work in the United States without employer sponsorship?", true, "No"},
		{"Are you eligible to work in the United States without employer sponsorship?", false, "Yes"},
		{"Are you eligible to work in the United States now and in the future without employer sponsorship?", false, "Yes"},
	}
	for _, c := range cases {
		r := countryAsk(t, m, countryFacts(c.need, "US"), "Austin, TX", c.text)
		if r.Outcome != match.OutcomeFilled || r.ChosenLabel != c.want {
			t.Errorf("%q (need=%t): %s %q (%s) want %q", c.text, c.need, r.Outcome, r.ChosenLabel, r.Reason, c.want)
		}
	}
}
