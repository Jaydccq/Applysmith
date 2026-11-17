package match_test

import (
	"testing"

	"applysmith/internal/answerbank/match"
)

// Ideogram (Ashby, Toronto): "Work Authorization" over boxes "I am authorized
// to work in Canada" and "I will need sponsorship to work in Canada". The
// statements are about Canada, which the profile does not record, so the
// user's other-country answers say which hold: not authorized, sponsorship
// needed. With no posting location the options' country is the question's,
// never the one recorded country (both boxes were ticked from the U.S.
// answers). Blank other-country answers, or a country of a recorded
// citizenship, keep the abstain; U.S. statements stay the profile's.
func TestXC_WorkAuthStatementsAboutAnUnrecordedCountry(t *testing.T) {
	toronto := match.Posting{Company: "Ideogram", Location: "Toronto, Ontario, Canada"}
	f := xcBFacts()
	f.RequiresSponsorshipNow, f.RequiresSponsorshipFuture = xcBBool(false), xcBBool(true)
	f.Citizenships = []string{"CN"}
	written := map[string]xcBSet{"work-auth-us": xcBAns("Yes"), "work-authorization-other-countries": xcBAns("No"), "sponsorship-other-countries": xcBAns("Yes")}
	blank := map[string]xcBSet{"work-auth-us": xcBAns("Yes")}
	boxes := func(place string) match.Question {
		q := xcBQ("Work Authorization", match.KindCheckbox, "I am authorized to work in "+place, "I will need sponsorship to work in "+place)
		q.InputType = match.InputMultiSelect
		return q
	}
	xcBRunCases(t, []xcBCase{
		{name: "a Toronto posting", sets: written, facts: f, posting: toronto, q: boxes("Canada"),
			entry: "work-auth-and-future-sponsorship", label: "I will need sponsorship to work in Canada", answer: "I will need sponsorship to work in Canada"},
		{name: "no posting location: the options name the country", sets: written, facts: f, posting: match.Posting{Company: "Ideogram"}, q: boxes("Canada"),
			entry: "work-auth-and-future-sponsorship", answer: "I will need sponsorship to work in Canada"},
		{name: "blank other-country answers keep the abstain", sets: blank, facts: f, posting: toronto, q: boxes("Canada"),
			wantNone: true, reason: match.ReasonContextPolicy, detail: match.DetailPostingCountry},
		{name: "a country of citizenship keeps the abstain", sets: written, facts: f, posting: match.Posting{Company: "Ideogram", Location: "Shanghai, China"}, q: boxes("China"),
			wantNone: true, reason: match.ReasonContextPolicy, detail: match.DetailPostingCountry},
		// Ideogram's other posting ("Canada (Eastern Time)") asks the same
		// statements as one choice under "Work authorization in Canada".
		{name: "one choice under a heading naming the country", sets: written, facts: f, posting: match.Posting{Company: "Ideogram", Location: "Canada (Easten Time)"},
			q:     xcBQ("Work authorization in Canada", match.KindRadioGroup, "I am authorized to work in Canada", "I will need sponsorship to work in Canada"),
			entry: "work-auth-and-future-sponsorship", label: "I will need sponsorship to work in Canada"},
		{name: "a heading naming a city is no such heading", sets: written, facts: f, posting: toronto, q: xcBYesNo("Work authorization in Toronto"),
			notEntry: "work-auth-and-future-sponsorship", wantNone: true},
		{name: "U.S. statements are the profile's", sets: written, facts: f, posting: match.Posting{Company: "Ideogram", Location: "New York, NY"}, q: boxes("the U.S."),
			entry: "work-auth-and-future-sponsorship", answer: "I am authorized to work in the U.S., I will need sponsorship to work in the U.S."},
	})
}
