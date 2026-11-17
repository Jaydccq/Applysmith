package match

import "testing"

// catalogWinner walks the whole catalog with every entry present and every
// control accepting.
func catalogWinner(text string, hasOptions bool) (string, routeWalk) {
	reworded, _ := routeVocabularyVariant(text)
	w := walkRoutes(routeCatalog(), text, reworded, routeEnv{
		hasOptions: hasOptions,
		lookup:     func(id string) *Entry { return &Entry{ID: id} },
		accepts:    func(*Entry) (bool, string) { return true, "" },
	})
	if w.Hit == nil {
		return "", w
	}
	return w.Hit.Target, w
}

// TestRoutes_SpecScenarios runs the scenarios of the route catalog requirement
// through the assembled catalog, so a rule that is right alone but shadowed by
// another family fails here.
func TestRoutes_SpecScenarios(t *testing.T) {
	cases := []struct {
		name       string
		text       string
		hasOptions bool
		want       string
	}{
		{"sponsorship wording is not work authorization", "Will you now or in the future require visa sponsorship for authorization to work in the United States?", false, "sponsorship-now-or-future"},
		{"plain work authorization", "Are you legally authorized to work in the United States?", false, "work-auth-us"},
		{"combined authorization and sponsorship", "Are you authorized to work in the United States and will you require future sponsorship?", true, "work-auth-and-future-sponsorship"},
		{"bare location with options", "Location", true, "preferred-work-location"},
		{"bare location without options", "Location", false, "address-city"},
		{"salary desired", "Salary Desired", false, "salary-expectation"},
		{"salary requirements", "Salary requirements", false, "salary-expectation"},
		{"attestation is not history", "I attest that I have no post-government employment restrictions", true, "mandatory-acknowledge"},
		{"count ask is not a link", "How many public example-host repositories do you maintain?", false, ""},
		{"link count still routes", "How many github profile links do you have?", false, "github-url"},
		{"last-resort screener", "Do you have strong analytical and communication skills?", true, "capability-self-assessment"},
		{"screener excluded by a year count", "Do you have 10 years of analytical skills?", true, ""},
		{"skill gate vetoed by a threshold", "Do you have 5+ years of Example Framework experience?", true, ""},
		{"cloud platform pick", "Which cloud platform do you have the most professional experience with?", true, "primary-cloud-platform"},
		{"total compensation first", "What is your total cash compensation expectation?", false, "total-cash-compensation"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, w := catalogWinner(c.text, c.hasOptions)
			if got != c.want {
				t.Fatalf("winner = %q, want %q (blocked %v)", got, c.want, w.Blocked)
			}
		})
	}
	t.Run("a vetoed route is recorded for the fuzzy stage", func(t *testing.T) {
		_, w := catalogWinner("I attest that I have no post-government employment restrictions", true)
		if !w.Blocked["government-employee-history"] {
			t.Fatalf("history entry must be barred from fuzzy: %v", w.Blocked)
		}
	})
	t.Run("salary requirement with the verb state", func(t *testing.T) {
		if got, _ := catalogWinner("Please state your base salary requirement (do not state negotiable)", false); got != "salary-expectation" {
			t.Fatalf("winner = %q", got)
		}
	})
}

// TestRoutes_AQuestionIsLeftToItsOwnEntry walks questions a broader route of
// another entry could claim: each reaches the entry that owns it, or none.
func TestRoutes_AQuestionIsLeftToItsOwnEntry(t *testing.T) {
	for _, c := range []struct {
		text       string
		hasOptions bool
		want       string
	}{
		// The applicant's availability for an interview, a call or a shift is
		// not the notice period before a start.
		{"What is your availability for an interview next week?", false, ""},
		{"Interview availability", false, ""},
		{"Please share your availability for a 30-minute call", false, ""},
		{"What is your availability to work weekends?", false, ""},
		{"Availability", false, "availability-notice-period"},
		{"What is your notice period?", false, "availability-notice-period"},
		// An ask about the current employer is not the employer's name.
		{"What is your job title at your current company?", false, ""},
		{"What is your current salary at your current employer?", false, "current-salary"},
		{"Reason for leaving current employer", false, ""},
		{"Current employer's industry", false, ""},
		{"Current company", false, "current-company"},
		// The languages one speaks are the list's; the languages besides
		// English stay the proficiency entry's.
		{"Which languages do you speak?", true, "languages-spoken"},
		{"Please select all the languages you speak fluently.", true, "languages-spoken"},
		{"Languages Spoken", true, "languages-spoken"},
		{"What other languages do you speak?", true, "language-proficiency"},
		// A kind of employer, or a named one, is no skill of the applicant's.
		{"Do you have experience working at a startup?", true, "startup-experience"},
		{"Do you have government employment experience?", true, "government-employee-history"},
		{"Do you have experience working for the federal government?", true, "government-employee-history"},
		{"Do you have experience working for a government agency?", true, "government-employee-history"},
		{"Do you have previous experience working at Acme?", true, ""},
		{"Do you have experience working at scale with Python?", true, "skill-experience-knowledge-gate"},
		// The salary held is not the salary wanted.
		{"Please provide your current salary.", false, "current-salary"},
		{"Please enter your current base salary", false, "current-salary"},
		{"Please indicate your current annual salary", false, "current-salary"},
		{"Please provide your most recent salary", false, ""},
		{"Please list your salary history", false, ""},
		// Where the applicant lives, asked after an attendance preamble, is
		// left by every office route.
		{"This is a hybrid position out of our Atlanta office, 3 days a week in the office. Do you currently reside in Atlanta?", true, ""},
		// Help to move, asked after the office is described.
		{"This role is located in Austin, TX. Will you require relocation assistance?", true, IDRelocationAssistance},
		{"This is a hybrid role based in San Francisco. Do you need relocation assistance?", true, IDRelocationAssistance},
		{"We work 3 days a week in the office. Will you require relocation assistance?", true, IDRelocationAssistance},
		// Holding an H-1B is not needing one sponsored.
		{"Do you currently hold an H-1B visa?", true, ""},
		{"Are you currently on an H-1B visa?", true, ""},
		{"Have you ever held an H-1B visa?", true, ""},
		{"Would you need an H-1B visa?", true, "h1b-sponsorship"},
		// A public agency is a government employer, never this employer.
		{"Have you been employed by a federal agency?", true, "government-employee-history"},
		{"Have you ever been employed by a local agency?", true, "government-employee-history"},
		{"Were you formerly employed by a municipal agency?", true, "government-employee-history"},
		// A pay per hour asked as a range is the hourly rate's.
		{"What are your hourly pay range expectations?", false, "hourly-pay-rate"},
		{"Hourly pay range expectations", false, "hourly-pay-rate"},
		{"What are your pay range expectations?", false, "salary-expectation"},
		// "AI" in a company's name is not artificial intelligence.
		{"Would you like to be considered for other roles at Scale AI?", true, "consider-other-roles-consent"},
		{"Are you applying to Fireworks AI via an automated script?", true, "automated-application-disclosure"},
		{"Are you able to complete a coding interview with the Fireworks AI engineering team?", true, "interview-coding-system-design"},
		{"Are you comfortable supporting executives at Hippocratic AI?", true, "comfortable-with-role-duties"},
		{"Are you able to complete a coding interview without AI?", true, ""},
		{"May we consider you for future roles using artificial intelligence?", true, "ai-future-jobs-consent"},
		// A relationship with an employee is not the applicant's own employment.
		{"Do you have a close personal relationship with a current employee of the company?", true, ""},
		{"Do you know a current employee at Acme?", true, ""},
		{"Are you a current employee of Acme?", true, "employer-history"},
		// The GSA exclusion list carries "Federal Procurement" in its name.
		{"Are you listed on the GSA List of Parties Excluded from Federal Procurement and Nonprocurement Programs?", true, "gsa-federal-program-exclusion"},
		{"Are you on the list of parties excluded from federal procurement?", true, "gsa-federal-program-exclusion"},
		{"Have you been involved in a federal procurement?", true, "federal-procurement-involvement"},
		// "C++" ends in no word character.
		{"Are you open to learning C++?", true, "comfortable-with-role-duties"},
		// The posting's own place is one question wherever the adjective sits.
		{"Are you able to work in the location listed for this role?", true, "willing-to-relocate"},
		{"Are you able to work in the listed location for this role?", true, "willing-to-relocate"},
		{"Are you able to work from the stated location of this role?", true, "willing-to-relocate"},
		// Based in a place "or willing to be based there" is no plain ask of
		// where the applicant is; with a cadence it is the attendance's.
		{"Are you located in Austin or able to be based in Austin to work on-site?", true, "office-attendance-ok"},
		{"Are you based in or planning to be based in Seattle?", true, ""},
		{"Are you based in Springfield or willing to relocate to work hybrid?", true, "willing-to-relocate"},
		{"Are you located in San Francisco or New York City?", true, "based-in-named-metro"},
		// Interest in a hybrid role in a named place is the relocation's.
		{"Would you be interested in a hybrid role based in Austin, TX?", true, "willing-to-relocate"},
		{"Are you comfortable with this hybrid role?", true, "office-attendance-ok"},
		// Snap the company is not the SNAP benefit.
		{"Have you interviewed with Snap in the past 6 months?", true, "previously-interviewed-company"},
		{"Have you received SNAP benefits in the last 6 months?", true, "wotc-snap-recipient"},
		{"Have you received Snap benefits?", true, "wotc-snap-recipient"},
		// A consent to train models on one's data is no skill; a checkbox of it
		// is an acknowledgement.
		{"Do you consent to your data being used to train AI models?", true, "mandatory-acknowledge"},
		// More than one conviction is the same question.
		{"Do you have any criminal convictions?", true, "criminal-conviction-history"},
		{"Have you had any felony convictions in the past 7 years?", true, "criminal-conviction-history"},
		// A request for samples of work asks no skill.
		{"Do you have coding samples?", true, ""},
		// A conviction "in any country/region" names no address.
		{"Have you been convicted in any country/region?", true, "criminal-conviction-history"},
		// U.S. citizenship alone is the citizenship status, and one other
		// class alone is no entry's.
		{"Are you a citizen of the U.S.?", true, "us-citizen-status"},
		{"Are you a citizen of the United States (required for this role)?", true, "us-citizen-status"},
		{"Are you a lawful permanent resident?", true, ""},
		{"Are you a refugee?", true, ""},
		{"Are you a citizen of the United States, a lawful permanent resident, an asylee or a refugee?", true, "us-citizen-lpr-asylee-refugee"},
		{"Are you a U.S. citizen or lawful permanent resident?", true, "us-citizen-lpr-asylee-refugee"},
		// A broader route leaves the wording a later route of another entry owns.
		{"How many years of relevant professional software development experience do you have in one or more general-purpose programming languages? (Python, Java, javascript, Go )", false, "years-experience-total"},
		{"How many years of experience do you have with Python?", false, "years-experience-python"},
		{"Do you have a family relationship or close personal contact with an employee of our external auditor?", true, "external-auditor-family"},
		{"Do you have an AWS certification?", false, "certifications-held"},
		{"Have you been sanctioned by any regulatory agency for health care fraud?", true, "healthcare-fraud-sanction"},
	} {
		if got, w := catalogWinner(c.text, c.hasOptions); got != c.want {
			t.Errorf("%q: winner = %q, want %q (blocked %v)", c.text, got, c.want, w.Blocked)
		}
	}
}
