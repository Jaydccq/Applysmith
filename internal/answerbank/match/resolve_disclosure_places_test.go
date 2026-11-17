package match

import (
	"testing"
)

func TestResolveLocation(t *testing.T) {
	cases := []struct {
		loc   string
		want  string
		state locationState
	}{
		{"", "", locationEmpty},
		{"San Francisco, CA", "US", locationResolved},
		{"Exampleville, TX", "US", locationResolved},
		{"Greenville, SC", "US", locationResolved},
		{"Austin, Texas", "US", locationResolved},
		{"New York, NY", "US", locationResolved},
		{"Albuquerque, New Mexico", "US", locationResolved},
		{"Remote (US)", "US", locationResolved},
		{"Remote - United States", "US", locationResolved},
		{"US Remote", "US", locationResolved},
		{"San Francisco Bay Area", "US", locationResolved},
		{"San Francisco office", "US", locationResolved},
		{"London HQ", "GB", locationResolved},
		// Orb (Ashby, live): the HQ label joined to the city by a bare hyphen.
		{"HQ-San Francisco", "US", locationResolved},
		{"Headquarters-London", "GB", locationResolved},
		{"London", "GB", locationResolved},
		{"London, UK", "GB", locationResolved},
		{"Greater London Area, United Kingdom", "GB", locationResolved},
		{"Paris", "FR", locationResolved},
		{"Seattle", "US", locationResolved},
		{"Gurgaon", "IN", locationResolved},
		{"Quebec City", "CA", locationResolved},
		{"Toronto, ON", "CA", locationResolved},
		{"Toronto, CA", "CA", locationResolved},
		{"Exampleville, ON", "CA", locationResolved},
		{"Berlin, DE", "DE", locationResolved},
		{"Bengaluru", "IN", locationResolved},
		{"Bengaluru, IN", "IN", locationResolved},
		{"Pune, Maharashtra", "IN", locationResolved},
		{"São Paulo, SP", "BR", locationResolved},
		{"Guadalajara, Jalisco, MX", "MX", locationResolved},
		{"Sydney, NSW", "AU", locationResolved},
		{"Sydney, WA", "AU", locationResolved},
		{"Hybrid - London", "GB", locationResolved},
		{"Coimbatore, India", "IN", locationResolved},
		{"Coimbatore, Tamil Nadu, India", "IN", locationResolved},
		{"Coimbatore, Tamil Nadu, IN", "IN", locationResolved},
		{"Bogota, CO", "CO", locationResolved},
		{"Denver, CO", "US", locationResolved},
		{"Boston, MA", "US", locationResolved},
		{"Goodyear, AZ", "US", locationResolved}, // HelloFresh's WMS posting
		{"Baltimore, MD", "US", locationResolved},
		{"Minneapolis, MN", "US", locationResolved},
		{"Birmingham, AL", "US", locationResolved},
		{"Little Rock, AR", "US", locationResolved},
		{"Bozeman, MT", "US", locationResolved},
		{"Birmingham, AL, USA", "US", locationResolved},
		{"Indore, IN, United States", "US", locationResolved},
		{"US-CA-San Jose", "US", locationResolved},
		{"IN-KA-Bengaluru", "IN", locationResolved},
		{"IN-MH-Exampleville", "IN", locationResolved},
		// A code that is a US state and an ISO country resolves only when
		// another part decides.
		{"Coimbatore, IN", "", locationAmbiguous},
		{"Indore, IN", "", locationAmbiguous},
		{"Kochi, IN", "", locationAmbiguous},
		{"Cali, CO", "", locationAmbiguous},
		{"Valletta, MT", "", locationAmbiguous},
		{"Casablanca, MA", "", locationAmbiguous},
		{"Chisinau, MD", "", locationAmbiguous},
		{"Tirana, AL", "", locationAmbiguous},
		{"Rosario, AR", "", locationAmbiguous},
		{"Ulaanbaatar, MN", "", locationAmbiguous},
		{"Exampleville, CA", "", locationAmbiguous},
		{"Greenville, CO", "", locationAmbiguous},
		{"Birmingham, AL, Albania", "", locationAmbiguous},
		// A US city settles a colliding code only as a city of that state.
		{"Boston, AL", "", locationAmbiguous},
		{"Austin, IN", "", locationAmbiguous},
		{"Indianapolis, IN", "US", locationResolved},
		{"Montgomery, AL", "US", locationResolved},
		// A state name that is also a country carries both readings.
		{"Georgia", "", locationAmbiguous},
		{"Remote - Georgia", "", locationAmbiguous},
		{"Georgia (Remote)", "", locationAmbiguous},
		{"Exampleville, Georgia", "", locationAmbiguous},
		{"Boston, Georgia", "", locationAmbiguous},
		{"Atlanta, Georgia", "US", locationResolved},
		{"Savannah, Georgia", "US", locationResolved},
		{"Georgia, USA", "US", locationResolved},
		// Present but not one country.
		{"Remote", "", locationUnresolved},
		{"Nationwide", "", locationUnresolved},
		{"Cambridge", "", locationUnresolved},
		{"Exampleville", "", locationUnresolved},
		{"US or Canada", "", locationAmbiguous},
		{"Remote - EMEA", "", locationAmbiguous},
		{"Remote - Europe", "", locationAmbiguous},
		{"Remote - North America", "", locationAmbiguous},
		// A town named for a foreign city resolves by a state or province of one
		// country beside it (Booz Allen's tracker row, "Rome, NY").
		{"Rome, NY", "US", locationResolved},
		{"London, ON", "CA", locationResolved},
		{"USA, NY, Rome (99 Otis St), United States of America", "US", locationResolved},
		{"Rome, NY, United States", "US", locationResolved},
		{"London, ON, Canada", "CA", locationResolved},
		{"Rome, United States", "", locationAmbiguous},
		{"Rome, Toronto, Canada", "", locationAmbiguous},
		{"Tbilisi, Georgia", "", locationAmbiguous},
		{"Vancouver, WA", "", locationAmbiguous},
		{"Exampleville, PE", "", locationAmbiguous},
		{"Paris, France / London, UK", "", locationAmbiguous},
	}
	for _, c := range cases {
		t.Run(c.loc, func(t *testing.T) {
			got, state := resolveLocation(c.loc)
			if got != c.want || state != c.state {
				t.Fatalf("got %q state %d, want %q state %d", got, state, c.want, c.state)
			}
		})
	}
}

// TestAnswerBank_WorkAuth_NoLocationAnswersForReview is the one case a
// country-less question is answered without a location: the posting says
// nothing and exactly one country is recorded, and then only for review.
func TestAnswerBank_WorkAuth_NoLocationAnswersForReview(t *testing.T) {
	m := pickMatcher(sponsorEntry())
	q := radioQ("Will you now or in the future require sponsorship?", "Yes", "No")
	run := func(location string, countries ...string) Result {
		return matchReq(m, q, withSponsorship(boolP(false), boolP(false)), withPosting(location),
			func(r *Request) { r.Facts.AuthorizedCountries = countries })
	}
	if r := run("", "US"); r.Outcome != OutcomeFilledReview || r.ChosenLabel != "No" || r.Confidence >= AutoFillConfidence || r.Confidence < ReviewFillConfidence {
		t.Fatalf("one country, no location: %+v", r)
	}
	if r := run("Austin, TX", "US"); r.Outcome != OutcomeFilled || r.ChosenLabel != "No" {
		t.Fatalf("one country, known US location fills: %+v", r)
	}
	for _, countries := range [][]string{{"US", "CA"}, {"EU"}} {
		r := run("", countries...)
		if r.Outcome != OutcomeAbstained || r.Reason != ReasonAmbiguous || r.Confidence != 0 {
			t.Fatalf("%v, no location: %+v", countries, r)
		}
	}
	if r := run(""); r.Outcome != OutcomeAbstained || r.Reason != ReasonContextPolicy || r.Detail != DetailPostingCountry {
		t.Fatalf("no recorded country: %+v", r)
	}
	if r := run("London", "US"); r.Outcome != OutcomeAbstained || r.Detail != DetailPostingCountry || r.Confidence != 0 {
		t.Fatalf("London, US fact: %+v", r)
	}
	// Owner decision: several countries that list the one recorded country
	// are answered for it, for review; a region without it is ambiguous.
	if r := run("US or Canada", "US"); r.Outcome != OutcomeFilledReview || r.ChosenLabel != "No" {
		t.Fatalf("US or Canada: %+v", r)
	}
	if r := run("Remote - EMEA", "US"); r.Outcome != OutcomeAbstained || r.Reason != ReasonAmbiguous || r.Confidence != 0 {
		t.Fatalf("Remote - EMEA: %+v", r)
	}
}

// TestAnswerBank_WorkAuth_SeveralCountriesWithOneNeed is the sponsorship need on
// a posting open in several countries: answered for review when the need is the
// same in each (a recorded country takes the recorded need, any other country
// needs sponsorship), never when it differs or no country is recorded.
func TestAnswerBank_WorkAuth_SeveralCountriesWithOneNeed(t *testing.T) {
	m := pickMatcher(sponsorEntry())
	const mercury = "San Francisco, CA, New York, NY, Portland, OR, or Remote within Canada or United States"
	run := func(text, location string, now, future bool, countries ...string) Result {
		return matchReq(m, radioQ(text, "Yes", "No"), withSponsorship(boolP(now), boolP(future)), withPosting(location),
			func(r *Request) { r.Facts.AuthorizedCountries = countries })
	}
	const q = "Will you now or at any time in the future require visa sponsorship?"
	for _, loc := range []string{mercury, "US or Canada", "Toronto, ON or Austin, TX", "New York, NY; London, UK", "North America", "Remote - North America"} {
		r := run(q, loc, false, true, "US")
		if r.Outcome != OutcomeFilledReview || r.ChosenLabel != "Yes" || r.Confidence >= AutoFillConfidence {
			t.Fatalf("%s, needs sponsorship in the recorded country and is not authorized in the others: %+v", loc, r)
		}
	}
	if r := run(q, "US or Canada", false, false, "US", "CA"); r.Outcome != OutcomeFilledReview || r.ChosenLabel != "No" {
		t.Fatalf("authorized in every country with no need: %+v", r)
	}
	// GDIT's "Any Location / Remote" names no place: a recorded Yes is the same
	// in each country, and a No is answered for the one recorded country
	// (owner decision), for review.
	for _, loc := range []string{"Any Location / Remote", "Remote", "Multiple Locations"} {
		if r := run(q, loc, true, true, "US"); r.Outcome != OutcomeFilledReview || r.ChosenLabel != "Yes" || r.Confidence >= AutoFillConfidence {
			t.Fatalf("%s, needs sponsorship in the recorded country: %+v", loc, r)
		}
		if r := run(q, loc, false, false, "US"); r.Outcome != OutcomeFilledReview || r.ChosenLabel != "No" {
			t.Fatalf("%s, no need in the recorded country: %+v", loc, r)
		}
		if r := run(q, loc, true, true); r.Outcome != OutcomeAbstained {
			t.Fatalf("%s, no country recorded: %+v", loc, r)
		}
	}
	// A region is a narrower "any country" (Customer.io: "Americas Remote"): a
	// recorded Yes is the same in each of its countries; a No is not.
	for _, loc := range []string{"Americas Remote", "Remote - EMEA or Austin, TX", "North America or EMEA"} {
		if r := run(q, loc, false, true, "US"); r.Outcome != OutcomeFilledReview || r.ChosenLabel != "Yes" || r.Confidence >= AutoFillConfidence {
			t.Fatalf("%s, needs sponsorship in the recorded country: %+v", loc, r)
		}
		if r := run(q, loc, false, false, "US"); r.Outcome != OutcomeAbstained {
			t.Fatalf("%s, no need in the recorded country: %+v", loc, r)
		}
	}
	// A place the table does not know is placed in the one recorded country
	// (owner decision), for review.
	if r := run(q, "Springfield", true, true, "US"); r.Outcome != OutcomeFilledReview || r.ChosenLabel != "Yes" {
		t.Fatalf("a place the table does not know: %+v", r)
	}
	// Spade names the countries in the question itself.
	for _, named := range []string{
		"Will you now or in the future require sponsorship for US or Canadian employment (e.g., H1B visa)?",
		"Will you now or in the future require sponsorship to work in the US and Canada?",
	} {
		if r := run(named, "Remote (US/Canada)", true, true, "US"); r.Outcome != OutcomeFilledReview || r.ChosenLabel != "Yes" {
			t.Fatalf("%s, needs sponsorship in the recorded country and is not authorized in the other: %+v", named, r)
		}
		if r := run(named, "Remote (US/Canada)", false, false, "US"); r.Outcome != OutcomeAbstained {
			t.Fatalf("%s, no need in the recorded country only: %+v", named, r)
		}
	}
	cases := []struct {
		name, text, location string
		now, future          bool
		countries            []string
	}{
		{"no country of the posting recorded", q, "London or Toronto", false, true, []string{"US"}},
		{"North America with no need in the United States only", q, "North America", false, false, []string{"US"}},
		{"a part that names no place", q, "Exampleville or Austin, TX or Toronto, ON", false, true, []string{"US"}},
		{"the question names a country", "Will you require sponsorship to work in Canada?", mercury, false, true, []string{"US"}},
	}
	for _, c := range cases {
		if r := run(c.text, c.location, c.now, c.future, c.countries...); r.Outcome != OutcomeAbstained {
			t.Fatalf("%s: %+v", c.name, r)
		}
	}
	// Owner decision: a posting that lists the one recorded country among
	// others is answered for it, for review, the need and the authorization
	// alike.
	if r := run(q, mercury, false, false, "US"); r.Outcome != OutcomeFilledReview || r.ChosenLabel != "No" {
		t.Fatalf("no need in the recorded country, another country not recorded: %+v", r)
	}
	e := ent("work-auth-us", "fixed", withAnswer("Yes"))
	if a, line, hit := postingCountryGuard(guardStep(mercury, "Are you legally authorized to work in the country for which you applied?", "US"), &e); hit {
		t.Fatalf("authorization in the recorded country among others: %+v (%s)", a, line)
	}
}

// TestPostingCountry_ACityAndItsStateCodeWithoutAComma: Greenhouse writes
// Verana Health's offices "New York NY, San Francisco CA", the code after the
// city with no comma; a known US city of that state reads as the state. A code
// beside a city of another state, or beside no known city, stays as it was.
func TestPostingCountry_ACityAndItsStateCodeWithoutAComma(t *testing.T) {
	for loc, want := range map[string]string{
		"New York NY, San Francisco CA": "US",
		"San Francisco CA":              "US",
		"Austin TX":                     "US",
		"Remote - Austin TX":            "US",
		"Zzyzx Placeholder CA":          "",
		"Boston CA":                     "",
	} {
		if got, _ := postingCountry(Posting{Location: loc}); got != want {
			t.Errorf("%q: got %q, want %q", loc, got, want)
		}
	}
	if got := commaBeforeStateCode("Toronto ON, Austin TX"); got != "Toronto ON, Austin, TX" {
		t.Errorf("only a US city of the state takes the comma: %q", got)
	}
}
