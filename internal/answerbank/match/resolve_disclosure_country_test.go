package match

import (
	"strings"
	"testing"
)

func guardStep(location, text string, countries ...string) *Step {
	return &Step{
		Req: &Request{Posting: Posting{Location: location, Company: "Example Corp"}, Facts: Facts{AuthorizedCountries: countries}},
		Q:   Question{Text: text},
	}
}

// TestAnswerBank_WorkAuth_PostingCountryGuardTable is the "Country mismatch"
// and "Other-country eligibility" scenarios: a work-authorization or sponsorship
// question is answered only when every country it is about is one the user
// recorded.
func TestAnswerBank_WorkAuth_PostingCountryGuardTable(t *testing.T) {
	const (
		here    = "Are you legally authorized to work in this country?"
		sponsor = "Will you require sponsorship to work in this country?"
		bare    = "Will you require sponsorship?"
		bareWA  = "Are you legally authorized to work?"
	)
	cases := []struct {
		name, text, location string
		countries            []string
		blocked              bool
	}{
		{"this country, London, US fact", here, "London, United Kingdom", []string{"US"}, true},
		{"this country, Berlin, US fact", here, "Berlin, Germany", []string{"US"}, true},
		{"this country, Toronto, US fact", here, "Toronto, ON, Canada", []string{"US"}, true},
		{"this country, US city and state", here, "San Jose, CA", []string{"US"}, false},
		{"this country, US with country", here, "Exampleville, USA", []string{"US"}, false},
		{"this country, state name only", here, "Austin, Texas", []string{"US"}, false},
		{"this country, remote with no country", here, "Remote", []string{"US"}, false},
		{"this country, remote in the US", here, "Remote, US", []string{"US"}, false},
		{"this country, London, UK fact", here, "London, United Kingdom", []string{"GB"}, false},
		{"this country, London, UK written as UK", here, "London, UK", []string{"UK"}, false},
		{"sponsorship in this country, London", sponsor, "London, United Kingdom", []string{"US"}, true},
		{"EU question, US fact", "Do you require visa sponsorship to work in the EU?", "", []string{"US"}, true},
		{"EU question, EU fact", "Do you require visa sponsorship to work in the EU?", "", []string{"EU"}, false},
		{"EU question, a member country is no region fact", "Do you require visa sponsorship to work in the EU?", "", []string{"DE"}, true},
		{"member country question, EU fact", "Are you authorized to work in Germany?", "", []string{"EU"}, false},
		{"EEA question, EU fact", "Are you authorized to work in the EEA?", "", []string{"EU"}, true},
		{"Canada named, US fact", "Will you require sponsorship for a role in Canada?", "", []string{"US"}, true},
		{"Canada named, both facts", "Will you require sponsorship for a role in Canada?", "", []string{"US", "CA"}, false},
		{"UK named, UK fact", "Do you have the right to work in the UK?", "", []string{"GB"}, false},
		{"US named on a London posting", "Are you legally authorized to work in the United States?", "London, United Kingdom", []string{"US"}, false},
		{"US named in a placing lead too", "If located in the US, are you currently authorized to work in the US?", "", []string{"US"}, false},
		{"US named, no recorded country", "Are you legally authorized to work in the United States?", "", nil, true},
		{"nothing named, nothing known", bare, "", []string{"US"}, false},
		{"nothing named, London", bare, "London, United Kingdom", []string{"US"}, true},
		{"nothing named, US posting", bare, "Austin, TX", []string{"US"}, false},
		{"authorization, nothing named, Berlin", bareWA, "Berlin, Germany", []string{"US"}, true},
		{"the country you are applying to, London", "Are you legally authorized to work in the country you are applying to?", "London, United Kingdom", []string{"US"}, true},
		{"the country you are applying to, US posting", "Are you legally authorized to work in the country you are applying to?", "Durham, NC", []string{"US"}, false},
		{"the country you are applying, London", "Are you eligible to work in the country you are applying?", "London, United Kingdom", []string{"US"}, true},
		{"the country you are applying, US posting", "Are you eligible to work in the country you are applying?", "Remote, US", []string{"US"}, false},
		{"the country you're applying to, London", "Are you legally authorized to work in the country you're applying to?", "London, United Kingdom", []string{"US"}, true},
		{"the country for which you applied, a US posting of several US cities", "Are you authorized to work in the country for which you applied?", "San Francisco, CA \u2022 New York, NY \u2022 United States", []string{"US"}, false},
		{"the country for which you applied, London", "Are you authorized to work in the country for which you applied?", "London, United Kingdom", []string{"US"}, true},
		{"the country of the office you selected, a key of offices, San Francisco", "Work Authorization: Are you currently legally authorized to work in the country of the office you selected? San Francisco = United States. Melbourne = Australia.", "San Francisco, CA", []string{"US"}, false},
		{"the country of the office you selected, a key of offices, Melbourne", "Work Authorization: Are you currently legally authorized to work in the country of the office you selected? San Francisco = United States. Melbourne = Australia.", "Melbourne, Australia", []string{"US"}, true},
		{"the country you have applied to, Toronto", "Do you require sponsorship to work in the country you have applied to?", "Toronto, ON, Canada", []string{"US"}, true},
		{"country of residence", "Are you authorized to work in the country where you reside?", "Austin, TX", []string{"US"}, true},
		{"several countries, one not recorded", "Are you authorized to work in the US and Canada?", "", []string{"US"}, true},
		{"an H-1B example names the United States, Remote", "Will you now or in the future require sponsorship for employment visa status (e.g., H-1B)?", "Remote", []string{"US"}, false},
		{"an H-1B example, only Canada recorded", "Will you now or in the future require sponsorship for employment visa status (e.g., H-1B)?", "Remote", []string{"CA"}, true},
		{"the company name is no country", "Do you require sponsorship to work for Example Corp?", "Austin, TX", []string{"US"}, false},
		{"pronoun us is no country", "Do you need us to sponsor you?", "Austin, TX", []string{"US"}, false},
		{"New Mexico is no Mexico", "Are you authorized to work in New Mexico?", "", []string{"US"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, id := range []string{"work-auth-us", idSponsorshipNowOrFuture} {
				e := ent(id, "fixed", withAnswer("Yes"))
				a, line, hit := postingCountryGuard(guardStep(c.location, c.text, c.countries...), &e)
				if hit != c.blocked {
					t.Fatalf("%s: blocked = %v (%s)", id, hit, line)
				}
				if hit && (a.Reason != ReasonContextPolicy || a.Detail != DetailPostingCountry || !a.Terminal()) {
					t.Fatalf("%s: abstain = %+v", id, a)
				}
			}
		})
	}
	// Owner decision: a posting in several countries that lists the one
	// recorded country is placed there; one that does not list it is ambiguous.
	t.Run("the country for which you applied, a posting in two countries", func(t *testing.T) {
		e := ent("work-auth-us", "fixed", withAnswer("Yes"))
		if a, line, hit := postingCountryGuard(guardStep("Remote (Canada); Remote (US)", "Are you authorized to work in the country for which you applied?", "US"), &e); hit {
			t.Fatalf("listing the recorded country: want no block, got %+v (%s)", a, line)
		}
		a, line, hit := postingCountryGuard(guardStep("Remote (Canada); Remote (Mexico)", "Are you authorized to work in the country for which you applied?", "US"), &e)
		if !hit || a.Reason != ReasonAmbiguous {
			t.Fatalf("not listing it: want the ambiguous abstain, got %+v (%s)", a, line)
		}
	})
	// Sourcegraph (Remote): the country the applicant lives in is the recorded
	// address's country, answered only when it is an authorized country.
	t.Run("the country you live in is the address's country", func(t *testing.T) {
		const current = "Will you need employment visa sponsorship now or in the future to work in your current location?"
		for _, c := range []struct {
			text, home string
			blocked    bool
		}{
			{current, "United States", false},
			{"Are you authorized to work in the country where you reside?", "United States", false},
			{current, "United Kingdom", true},
			{current, "", true},
			{"Will you now, or in the future, require visa sponsorship to work in the country that you are residing in?", "United States", false},
			{"Will you now, or in the future, require visa sponsorship to work in the country that you are residing in?", "United Kingdom", true},
			{"Are you authorized to work in the country you live in?", "United States", false},
		} {
			for _, id := range []string{"work-auth-us", idSponsorshipNowOrFuture} {
				e := ent(id, "fixed", withAnswer("Yes"))
				st := guardStep("Remote", c.text, "US")
				st.Req.Facts.Address.Country = c.home
				if _, line, hit := postingCountryGuard(st, &e); hit != c.blocked {
					t.Errorf("%s, home %q, %s: blocked = %v (%s)", c.text, c.home, id, hit, line)
				}
			}
		}
	})
	t.Run("a captured scope exempts the entry", func(t *testing.T) {
		e := ent("work-auth-us", "fixed", withAnswer("Yes"))
		e.CapturedScope = []ScopeItem{{Host: "jobs.example.com", Question: here}}
		if _, _, hit := postingCountryGuard(guardStep("London, United Kingdom", here, "US"), &e); hit {
			t.Fatal("scoped entry")
		}
	})
	t.Run("other entries are not guarded", func(t *testing.T) {
		e := ent("current-company", "fixed")
		if _, _, hit := postingCountryGuard(guardStep("London, United Kingdom", here, "US"), &e); hit {
			t.Fatal("not a fact about work authorization")
		}
	})
	t.Run("through the pipeline the guard is terminal", func(t *testing.T) {
		m := pfMatcher(ent("work-auth-us", "fixed", withAnswer("Yes")))
		req := pfReq(alexFacts(), pfDate(2030, 9, 1), yesNoQ("w", here))
		req.Posting.Location = "Toronto, Canada"
		r := pfRun(t, m, req)[0]
		if r.Outcome != OutcomeAbstained || r.Reason != ReasonContextPolicy || r.Detail != DetailPostingCountry || !r.Terminal || r.Confidence != 0 {
			t.Fatalf("got %+v", r)
		}
		req.Posting.Location = "San Jose, CA"
		if r := pfRun(t, m, req)[0]; r.Outcome == OutcomeAbstained {
			t.Fatalf("a US posting answers: %+v", r)
		}
	})
}

// TestAnswerBank_WorkAuth_AnUnplacedPostingIsPresumedInTheUS is the owner's
// decision of flow round 8: a question about the posting's country, on a
// posting nothing places, is answered as for a posting in the United States
// (the same outcome, choice and confidence as on an Austin posting) and says
// so in its detail, unless the posting's title or description head names
// another country or its title a region; a posting placed anywhere keeps
// today's reading. The presumed country is then covered like any other: a
// profile not authorized in the United States is not answered.
func TestAnswerBank_WorkAuth_AnUnplacedPostingIsPresumedInTheUS(t *testing.T) {
	m := pickMatcher(ent("work-auth-us", "fixed", withAnswer("Yes")), sponsorEntry())
	const (
		applying = "Are you eligible to work in the country you are applying?"
		desc     = "Platform Engineer\nBuild the platform every team ships on."
	)
	ask := func(text, location, title, description string, countries []string) Result {
		return matchReq(m, radioQ(text, "Yes", "No"), withSponsorship(boolP(false), boolP(false)), func(r *Request) {
			r.Posting = Posting{Company: "Northwind Example", Title: title, Description: description, Location: location}
			if countries != nil {
				r.Facts.AuthorizedCountries = countries
			}
		})
	}
	for _, c := range []struct {
		name, text, location, title, description string
		want                                     string // presumed, answered or abstained
		countries                                []string
	}{
		{"the country you are applying, nothing placed", applying, "", "Platform Engineer", desc, "presumed", nil},
		{"this country, nothing placed", "Are you legally authorized to work in this country?", "", "Platform Engineer", desc, "presumed", nil},
		{"the country you are applying to", "Are you legally authorized to work in the country you are applying to?", "", "", "", "presumed", nil},
		{"the country for which you applied", "Are you authorized to work in the country for which you applied?", "", "", desc, "presumed", nil},
		{"Nuro's country which you are applying, no preposition", "Do you now, or will you in the future, require sponsorship for employment in the country which you are applying?", "", "", desc, "presumed", nil},
		{"HackerOne's country for which job you are applying for", "Will you now or in the future require employer sponsorship to work in the country for which job you are applying for?", "", "", desc, "presumed", nil},
		{"Oracle's jurisdiction/country where the job is posted", "Are you legally authorized to work in the jurisdiction/country where the job is posted?", "", "", desc, "presumed", nil},
		{"Gem's country or jurisdiction of the position you are applying to", "Are you legally authorized to work in the country or jurisdiction where the position to which you are applying is located?", "", "", desc, "presumed", nil},
		{"the country of hire, a sponsorship question", "Do you now, or will you in the future, require visa sponsorship to work for Northwind Example in the country of hire?", "", "", desc, "presumed", nil},
		{"here", "Can you work here without sponsorship?", "", "", desc, "presumed", nil},
		{"a title that names the United States", applying, "", "Platform Engineer - US Remote", desc, "presumed", nil},
		{"a description that names Canada", applying, "", "Platform Engineer", desc + "\nYou will work from our office in Canada.", "abstained", nil},
		{"a title that names a region", applying, "", "Platform Engineer - EMEA Remote", desc, "abstained", nil},
		{"a US location", applying, "Austin, TX", "Platform Engineer", desc, "answered", nil},
		{"a question that points at no country keeps its own reading", "Will you now or in the future require visa sponsorship?", "", "Platform Engineer", desc, "answered", nil},
		{"a location outside the US", applying, "Toronto, ON, Canada", "Platform Engineer", desc, "abstained", nil},
		{"nothing placed, a profile authorized only in Canada", applying, "", "Platform Engineer", desc, "abstained", []string{"CA"}},
	} {
		r := ask(c.text, c.location, c.title, c.description, c.countries)
		us := ask(c.text, "Austin, TX", c.title, c.description, c.countries)
		switch c.want {
		case "presumed":
			if us.Outcome == OutcomeAbstained || us.Detail != "" || r.Detail != DetailPostingCountryPresumed ||
				r.Outcome != us.Outcome || r.ChosenLabel != us.ChosenLabel || r.Confidence != us.Confidence {
				t.Errorf("%s: %+v\nas for a US posting: %+v", c.name, r, us)
			}
		case "answered":
			if r.Outcome == OutcomeAbstained || r.Detail != "" {
				t.Errorf("%s: %+v", c.name, r)
			}
		default:
			if r.Outcome != OutcomeAbstained || r.Reason != ReasonContextPolicy || r.Detail != DetailPostingCountry {
				t.Errorf("%s: %+v", c.name, r)
			}
		}
	}
}

// Booz Allen (Workday, live): authorization for an indefinite period is
// authorization that never needs sponsorship; a recorded Yes with any need is No.
func TestAnswerBank_WorkAuth_AnIndefinitePeriodNeedsNoSponsorshipEver(t *testing.T) {
	const booz = "Are you currently legally authorized to work for Booz Allen Hamilton on a full-time basis for an indefinite period of time in the country where this role is based?"
	for _, c := range []struct {
		name, stored, text string
		now, future        *bool
		want               string // the answer, or "" for an abstain
	}{
		{"F-1 OPT: authorized now, sponsorship needed", "Yes", booz, boolP(true), boolP(true), "No"},
		{"sponsorship needed only later", "Yes", booz, boolP(false), boolP(true), "No"},
		{"no sponsorship ever", "Yes", booz, boolP(false), boolP(false), "Yes"},
		{"not authorized", "No", booz, boolP(false), boolP(false), "No"},
		{"a sponsorship fact unset", "Yes", booz, boolP(false), nil, ""},
		{"behind a note that places the answer in the role's country", "Yes", booz + " If this role is located in the United States, answer \u2018Yes/No\u2019 based on your authorization to work in the United States. If this role is not located in the United States, answer \u2018Yes/No\u2019 based on your authorization to work in the country where the role is based.", boolP(true), boolP(true), "No"},
		{"behind a note that asks for more", "Yes", booz + " If this role is located in the United States, answer Yes only if you hold a green card.", boolP(false), boolP(false), ""},
		{"a negated wording", "Yes", "Are you not legally authorized to work in the United States for an indefinite period of time?", boolP(true), boolP(true), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			auth := ent("work-auth-us", "fixed", withAnswer(c.stored))
			auth.AnswerKey = "work_authorized"
			m := pickMatcher(auth, sponsorEntry())
			r := matchReq(m, radioQ(c.text, "Yes", "No"), withSponsorship(c.now, c.future), func(r *Request) {
				r.Posting = Posting{Company: "Booz Allen Hamilton", Title: "Software Engineer", Location: "McLean, VA"}
			})
			got := ""
			if r.Outcome == OutcomeFilled || r.Outcome == OutcomeFilledReview {
				got = r.Answer
			}
			if got != c.want {
				t.Fatalf("answer %q (%s %s), want %q\n%s", got, r.Outcome, r.Reason, c.want, strings.Join(r.Trace, "\n"))
			}
		})
	}
}
