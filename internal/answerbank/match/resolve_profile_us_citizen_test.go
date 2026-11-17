package match

import "testing"

// GRVTY (Greenhouse, live): a Yes/No "Are you a U.S. citizen?", behind the
// note that the position requires citizenship, is read from the user's own
// us-citizen-status; a status outside its closed choices, an unfilled entry
// or another wording is left alone.
func TestAnswerBank_USCitizenYesNo_FromTheRecordedStatus(t *testing.T) {
	status := func(p ...string) Entry {
		e := ent(idUSCitizenStatus, "pick-option", withPreferred(p...), withTypes(InputSingleSelect))
		e.Placeholder = false
		return e
	}
	const grvty = "This position requires U.S. citizenship pursuant to a government contract. Are you a U.S. citizen?"
	// Manifold Industries (Ashby, live): a U.S.-based citizen is a citizen whose home is in the U.S.
	const based = "Are you a U.S.-based U.S. citizen?"
	// Promise (Ashby, live): a citizen or a green card holder. A bare No is no
	// citizen; a recorded need for sponsorship, which a permanent resident
	// never has, makes it no green card holder either.
	const either = "Are you a U.S. Citizen or Green Card holder?"
	yes := true
	for _, c := range []struct {
		name, text string
		entry      *Entry
		country    string
		want       string
		need       *bool
	}{
		{"a citizen", grvty, ptr(status("U.S. citizen")), "", "Yes", nil},
		{"a permanent resident", grvty, ptr(status("Lawful permanent resident")), "", "No", nil},
		// Synack (Greenhouse, live): the note says only citizens are hired.
		{"only citizens hired, neither", "Please note: We can only hire U.S. citizens for this position due to federal government contract requirements. Are you a U.S. citizen?", ptr(status("No")), "", "No", nil},
		// WalkMe (Lever, live): "Do you meet this requirement?" asks the citizenship the note requires.
		{"the requirement met, neither", "As this role supports our Federal businesses, US citizenship is required. Do you meet this requirement?", ptr(status("No")), "", "No", nil},
		{"the requirement met, a citizen", "As this role supports our Federal businesses, US citizenship is required. Do you meet this requirement?", ptr(status("U.S. citizen")), "", "Yes", nil},
		// Cyware (Greenhouse, live): "is a requirement", and a statute whose points end no sentence.
		{"a statute's requirement, neither", "US Citizenship is a requirement of this position in accordance with 8 U.S.C 1324b(a)(2)(C). Do you meet this requirement?", ptr(status("No")), "", "No", nil},
		{"a statute's requirement, a citizen", "US Citizenship is a requirement of this position in accordance with 8 U.S.C 1324b(a)(2)(C). Do you meet this requirement?", ptr(status("U.S. citizen")), "", "Yes", nil},
		{"a note running past U.S.", "US citizenship is required to work on U.S. government contracts. Do you meet this requirement?", ptr(status("No")), "", "No", nil},
		{"a requirement without the note", "Do you meet this requirement?", ptr(status("No")), "", "", nil},
		{"a national", "Are you a U.S. citizen?", ptr(status("U.S. national")), "", "No", nil},
		{"neither", "Are you a citizen of the United States?", ptr(status("Neither")), "", "No", nil},
		{"a legacy No", "Are you a US citizen?", ptr(status("No")), "", "No", nil},
		{"a status outside the choices", "Are you a U.S. citizen?", ptr(status("Dual")), "", "", nil},
		{"no entry", "Are you a U.S. citizen?", nil, "", "", nil},
		{"another wording", "Are you a U.S. citizen with an active clearance?", ptr(status("U.S. citizen")), "", "", nil},
		{"citizen or permanent resident, a citizen", "Are you a U.S. citizen or permanent resident?", ptr(status("U.S. citizen")), "", "Yes", nil},
		{"citizen or green card, a permanent resident", either, ptr(status("Lawful permanent resident")), "", "Yes", nil},
		{"citizen or green card, a No with a need", either, ptr(status("No")), "", "No", &yes},
		{"citizen or green card, neither", either, ptr(status("Neither")), "", "No", nil},
		{"citizen or green card, a No with no need recorded", either, ptr(status("No")), "", "", nil},
		{"U.S.-based, not a citizen", based, ptr(status("Lawful permanent resident")), "US", "No", nil},
		{"U.S.-based, a citizen at home", based, ptr(status("U.S. citizen")), "United States", "Yes", nil},
		{"U.S.-based, a citizen abroad", based, ptr(status("U.S. citizen")), "Germany", "", nil},
		{"U.S.-based, a citizen with no recorded home", based, ptr(status("U.S. citizen")), "", "", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			var es []Entry
			if c.entry != nil {
				es = append(es, *c.entry)
			}
			var f Facts
			f.Address.Country = c.country
			f.RequiresSponsorshipFuture = c.need
			r := pfOne(t, pfMatcher(es...), f, pfToday, yesNoQ("c", c.text))
			if c.want == "" {
				if r.Source == SourceDerived {
					t.Fatalf("answered: %+v", r)
				}
				return
			}
			if r.ChosenLabel != c.want || r.Source != SourceDerived {
				t.Fatalf("want %q, got %+v", c.want, r)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
