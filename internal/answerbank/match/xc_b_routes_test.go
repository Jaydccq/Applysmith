package match_test

import (
	"testing"

	"applysmith/internal/answerbank/match"
)

// xcBNoAll fills the named fixed entries with "No".
func xcBNoAll(ids ...string) map[string]xcBSet {
	m := map[string]xcBSet{}
	for _, id := range ids {
		m[id] = xcBAns("No")
	}
	return m
}

func xcBFixedYesNo(name, text, entry, want string, sets map[string]xcBSet) xcBCase {
	return xcBCase{name: name, sets: sets, facts: xcBFacts(), q: xcBYesNo(text), entry: entry, label: want}
}

func TestXC_RoutesImmigration(t *testing.T) {
	status := map[string]xcBSet{
		"current-immigration-status": xcBPref("F-1 OPT"), "us-citizen-lpr-asylee-refugee": xcBAns("No"),
		"sponsorship-now-or-future": xcBAns("Yes"), "us-person-itar-ear": xcBAns("No"),
	}
	battery := xcBNoAll("held-h1b-past-6-years", "ever-held-j1-status", "lived-outside-us-12-consecutive-months",
		"pr-another-country-after-citizenship", "citizenship-in-another-country", "currently-located-outside-us")
	battery["sponsorship-now-or-future"] = xcBAns("Yes")
	auth := map[string]xcBSet{"work-auth-us": xcBAns("Yes"), "work-auth-us-any-employer": xcBAns("Yes"), "us-based-with-work-permit": xcBAns("Yes"),
		"sponsorship-now-or-future": xcBAns("Yes"), "commitments-to-another-employer": xcBAns("No"), "country-of-citizenship": xcBPref("Canada")}
	statusOpts := []string{"U.S. Citizen", "Permanent Resident", "F-1 OPT", "H-1B"}
	noNeed := xcBFacts()
	noNeed.RequiresSponsorshipFuture = xcBBool(false)
	const avride = "I am authorized to work in the United States and do not require current or future visa sponsorship."
	xcBRunCases(t, []xcBCase{
		// Avride (Greenhouse, live): the two-clause statement is true only when both clauses are.
		{name: "XC-047 authorized and needing no sponsorship states the statement", sets: auth, facts: noNeed,
			q: xcBYesNo(avride), entry: "sponsorship-now-or-future", label: "Yes"},
		// Both clauses state the favourable fact, so the statement is one claim:
		// a later need makes it false (Avride again, owner workspace).
		{name: "XC-047 a later need makes the statement No", sets: auth, facts: xcBFacts(),
			q: xcBYesNo(avride), entry: "sponsorship-now-or-future", label: "No"},
		// Clauses that point opposite ways are two questions one word cannot answer.
		{name: "XC-047 an authorization and a need asked together stay the user's", sets: auth, facts: noNeed,
			q: xcBYesNo("Are you legally authorized to work in the US and will you now or in the future require sponsorship?"), wantNone: true, reason: match.ReasonAmbiguous},
		// XC-044
		{name: "XC-044 nonimmigrant status", sets: status, facts: xcBFacts(),
			q: xcBQ("Please indicate your current nonimmigrant status", match.KindSelect, statusOpts...), entry: "current-immigration-status", label: "F-1 OPT"},
		{name: "XC-044 current visa status", sets: status, facts: xcBFacts(),
			q: xcBQ("Current visa status", match.KindSelect, statusOpts...), entry: "current-immigration-status", label: "F-1 OPT"},
		{name: "XC-044 citizen-only dropdown abstains", sets: status, facts: xcBFacts(),
			q: xcBQ("Citizenship status", match.KindSelect, "U.S. Citizen", "U.S. National"), wantNone: true},
		{name: "XC-044 US person under ITAR", sets: status, facts: xcBFacts(),
			q:     xcBYesNo("Are you a U.S. person as defined under ITAR?"),
			entry: "us-person-itar-ear", label: "No"},
		{name: "XC-044 EAR in a longer word is not export control", sets: status, facts: xcBFacts(),
			q: xcBYesNo("Do you have at least a year of early-career experience as a U.S. person?"), notEntry: "us-person-itar-ear", wantNone: true},

		// XC-045
		{name: "XC-045 H-1B in the preceding six years", sets: battery, facts: xcBFacts(),
			q: xcBYesNo("Have you held H-1B status within the preceding 6 years?"), entry: "held-h1b-past-6-years", label: "No"},
		{name: "XC-045 J-1 status", sets: battery, facts: xcBFacts(),
			q: xcBYesNo("Have you ever held J-1 status?"), entry: "ever-held-j1-status", label: "No"},
		{name: "XC-045 twelve consecutive months outside", sets: battery, facts: xcBFacts(),
			q: xcBYesNo("Have you lived or been located outside the U.S. for 12 consecutive months?"), entry: "lived-outside-us-12-consecutive-months", label: "No"},
		{name: "XC-045 permanent resident after citizenship", sets: battery, facts: xcBFacts(),
			q:     xcBYesNo("Since obtaining your most recent citizenship, did you afterwards become a permanent resident in another country?"),
			entry: "pr-another-country-after-citizenship", label: "No"},
		{name: "XC-045 hold residence or citizenship elsewhere", sets: battery, facts: xcBFacts(),
			q:     xcBYesNo("Do you currently hold permanent residence or citizenship in another country?"),
			entry: "citizenship-in-another-country", label: "No"},

		// XC-047
		{name: "XC-047 lawfully authorized", sets: auth, facts: xcBFacts(),
			q: xcBYesNo("Are you authorized to work lawfully in the United States?"), entry: "work-auth-us", label: "Yes"},
		{name: "XC-047 legally authorized", sets: auth, facts: xcBFacts(),
			q: xcBYesNo("Are you legally authorized to work in the United States?"), entry: "work-auth-us", label: "Yes"},
		// Being able to provide proof is the authorization fact (owner decision).
		{name: "XC-047 proof within three days of hire", sets: auth, facts: xcBFacts(),
			q: xcBYesNo("Can you provide proof that you are authorized to work in the United States within 3 days of hire?"), entry: "work-auth-us", label: "Yes"},
		// Huntington Ingalls' SuccessFactors: an offer lead, "documentation showing you are", the label doubled.
		{name: "XC-047 documentation showing eligibility if offered a position", sets: auth, facts: xcBFacts(),
			q:       xcBYesNo("If offered a position within Huntington Ingalls Industries, will you be able to provide documentation showing you are eligible to work in the United States? If offered a position within Huntington Ingalls Industries, will you be able to provide documentation showing you are eligible to work in the United States? is required"),
			posting: match.Posting{Location: "Newport News, VA"}, entry: "work-auth-us", label: "Yes"},
		// Eightfold (Qualcomm): the countries applied to are the posting's own.
		{name: "XC-047 verification upon employment for the countries applied to", sets: auth, facts: xcBFacts(),
			q:       xcBYesNo("Can you, upon employment, submit verification of your legal right to work in the countries to which you have applied?"),
			posting: match.Posting{Location: "San Diego, California, United States of America"}, entry: "work-auth-us", label: "Yes"},
		// Extreme Networks (Lever, live): a lead placing the role, and the
		// company's legal form after its name.
		{name: "XC-047 a lead placing the role and the company's legal form", sets: auth, facts: xcBFacts(),
			q:       xcBQ("If the position you are applying for is in the United States, are you authorized to work lawfully in the United States for Extreme Networks, Inc.?", match.KindRadioGroup, "Yes", "No", "Not applicable"),
			posting: match.Posting{Company: "Extreme Networks", Location: "Salem, New Hampshire, United States"}, entry: "work-auth-us", label: "Yes"},
		{name: "XC-047 a lead placing the applicant elsewhere", sets: auth, facts: xcBFacts(),
			q:        xcBQ("If the position you are applying for is in the United States, are you authorized to work lawfully in Canada for Extreme Networks, Inc.?", match.KindRadioGroup, "Yes", "No", "Not applicable"),
			posting:  match.Posting{Company: "Extreme Networks", Location: "Salem, New Hampshire, United States"},
			wantNone: true},
		// Metropolis (Greenhouse, live): the offer lead names the employment.
		{name: "XC-047 verification of legal right to work if offered employment", sets: auth, facts: xcBFacts(),
			q:       xcBYesNo("If offered employment, can you submit verification of legal right to work in the United States?"),
			posting: match.Posting{Location: "Remote - US"}, entry: "work-auth-us", label: "Yes"},
		// Adobe (Workday, live): the offer names the employer and the
		// documentation "establishes" the right to work.
		{name: "XC-047 documentation establishing identity and right to work if offered employment by the employer", sets: auth, facts: xcBFacts(),
			q:       xcBYesNo("If you are offered employment by Adobe, can you provide documentation establishing your identity and right to work in the country where this position will be based?"),
			posting: match.Posting{Company: "Adobe", Location: "San Jose, CA"}, entry: "work-auth-us", label: "Yes"},
		{name: "XC-047 verification for the countries applied to outside the recorded country", sets: auth, facts: xcBFacts(),
			q:        xcBYesNo("Can you, upon employment, submit verification of your legal right to work in the countries to which you have applied?"),
			posting:  match.Posting{Location: "Toronto, Ontario, Canada"},
			wantNone: true, reason: match.ReasonContextPolicy, detail: match.DetailPostingCountry},
		// Workday (Visa) and Eightfold (Netflix): the job's location is the posting's own.
		{name: "XC-047 eligible to work in the job's location", sets: auth, facts: xcBFacts(),
			q:       xcBYesNo("Are you legally eligible to work in the job\u2019s location?"),
			posting: match.Posting{Location: "San Francisco, CA"}, entry: "work-auth-us", label: "Yes"},
		{name: "XC-047 eligible to work in the job's location outside the recorded country", sets: auth, facts: xcBFacts(),
			q:        xcBYesNo("Are you legally eligible to work in the job\u2019s location?"),
			posting:  match.Posting{Location: "Toronto, Ontario, Canada"},
			wantNone: true, reason: match.ReasonContextPolicy, detail: match.DetailPostingCountry},
		// Clera (Ashby, live): "work authorization to work in" the job's country is no office question.
		{name: "XC-047 work authorization to work in the country where this job is located", sets: auth, facts: xcBFacts(),
			q:       xcBYesNo("Do you have work authorization to work in the country where this job is located?"),
			posting: match.Posting{Location: "San Francisco"}, entry: "work-auth-us", label: "Yes"},
		{name: "XC-047 work authorization to work in the job's country outside the recorded country", sets: auth, facts: xcBFacts(),
			q:        xcBYesNo("Do you have work authorization to work in the country where this job is located?"),
			posting:  match.Posting{Location: "Toronto, Ontario, Canada"},
			wantNone: true, reason: match.ReasonContextPolicy, detail: match.DetailPostingCountry},
		// Alnylam (Greenhouse) and Clera (Ashby), live: the job's country named as its "country/region" or as "that country".
		{name: "XC-047 authorized in the country/region of the job applied for", sets: auth, facts: xcBFacts(),
			q:       xcBYesNo("Are you legally authorized to work in the country/region of the job for which you are applying?"),
			posting: match.Posting{Location: "Cambridge, MA"}, entry: "work-auth-us", label: "Yes"},
		{name: "XC-047 authorized in the country/region of the job outside the recorded country", sets: auth, facts: xcBFacts(),
			q:        xcBYesNo("Are you legally authorized to work in the country/region of the job for which you are applying?"),
			posting:  match.Posting{Location: "London, United Kingdom"},
			wantNone: true, reason: match.ReasonContextPolicy, detail: match.DetailPostingCountry},
		{name: "XC-047 work authorization to work in that country", sets: auth, facts: xcBFacts(),
			q:       xcBYesNo("Do you have work authorization to work in that country?"),
			posting: match.Posting{Location: "San Francisco"}, entry: "work-auth-us", label: "Yes"},
		// Lime (Ashby, live).
		{name: "XC-047 authorized in the country in which this job is posted", sets: auth, facts: xcBFacts(),
			q:       xcBYesNo("Are you currently authorized to work in the country in which this job is posted?"),
			posting: match.Posting{Location: "Raleigh, NC"}, entry: "work-auth-us", label: "Yes"},
		{name: "XC-047 sponsorship, work authorization or any other assistance", sets: auth, facts: xcBFacts(),
			q:       xcBYesNo("Will you now or in the future require visa sponsorship, work authorization, or any other assistance to legally work?"),
			posting: match.Posting{Location: "Raleigh, NC"}, entry: "sponsorship-now-or-future", label: "Yes"},
		// Citadel (live): the country "this job is posted in", with no "where".
		{name: "XC-047 sponsorship in the country this job is posted in", sets: auth, facts: xcBFacts(),
			q:       xcBYesNo("Do you now, or will you in the future, need sponsorship from an employer to obtain, extend, or renew your authorization to work in the country this job is posted in?"),
			posting: match.Posting{Location: "Miami, FL"}, entry: "sponsorship-now-or-future", label: "Yes"},
		{name: "XC-047 sponsorship in the country this job is posted in outside the recorded country", sets: auth, facts: xcBFacts(),
			q:        xcBYesNo("Do you now, or will you in the future, need sponsorship from an employer to obtain, extend, or renew your authorization to work in the country this job is posted in?"),
			posting:  match.Posting{Location: "London, United Kingdom"},
			wantNone: true, reason: match.ReasonContextPolicy, detail: match.DetailPostingCountry},
		{name: "XC-047 eligible to work", sets: auth, facts: xcBFacts(),
			q: xcBYesNo("Are you eligible to work in the United States?"), entry: "work-auth-us", label: "Yes"},
		{name: "XC-047 sponsorship clause is the sponsorship question", sets: auth, facts: xcBFacts(),
			q: xcBYesNo("Are you authorized to work in the United States without requiring visa sponsorship?"), notEntry: "work-auth-us", entry: "sponsorship-now-or-future", label: "No"},
		{name: "XC-047 holding a permit is the authorization question", sets: auth, facts: xcBFacts(),
			q:       xcBYesNo("Do you have a work visa or work permit approved or in process for the country you are applying to?"),
			posting: match.Posting{Location: "Austin, TX"}, entry: "work-auth-us", wantNone: true, reason: match.ReasonAmbiguous},
		{name: "XC-047 any employer", sets: auth, facts: xcBFacts(),
			q: xcBYesNo("Are you authorized to work in the United States for any employer?"), entry: "work-auth-us-any-employer", label: "Yes"},
		{name: "XC-047 commitments to another employer", sets: auth, facts: xcBFacts(),
			q:     xcBYesNo("Do you have commitments to another employer that would interfere with employment here?"),
			entry: "commitments-to-another-employer", label: "No"},

		// XC-048: a fact recorded for one country answers only that country.
		{name: "XC-048 named other country", sets: auth, facts: xcBFacts(),
			q: xcBQ("Are you currently eligible to work in Canada?", match.KindRadioGroup,
				"Yes, I am currently eligible to work in the country where this role is based", "No"),
			wantNone: true, reason: match.ReasonContextPolicy, detail: match.DetailPostingCountry},
		{name: "XC-048 UK right to work paragraph", sets: auth, facts: xcBFacts(),
			q:        xcBYesNo("You must be able to demonstrate your right to work in the United Kingdom. Do you have the right to work in the UK?"),
			wantNone: true, reason: match.ReasonContextPolicy, detail: match.DetailPostingCountry},
	})
}

// Twilio (Greenhouse, queue164): "What is the source of your right to work
// where this role is listed?" over a citizen or permanent resident, a citizen
// of a country with an unlimited right, a valid visa with permission to work
// for any employer, and a sponsorship need is the status picker: an F-1 OPT
// holder holds the valid visa. Two options name citizenship, so a citizen's
// standing picks neither and the list is left to them.
func TestXC_SourceOfTheRightToWorkIsTheStatusPicker(t *testing.T) {
	opts := []string{"Citizen or permanent resident where this role is listed", "Citizen of a country with an unlimited right to work where this role is listed",
		"Valid visa with permission to work with any employer where this role is listed", "I do not currently have an unlimited right to work where this role is listed and will require sponsorship"}
	q := xcBQ("What is the source of your right to work where this role is listed?", match.KindCustomListbox, opts...)
	q.Required = true
	citizen := xcBFacts()
	citizen.VisaStatus, citizen.RequiresSponsorshipFuture = "citizen", xcBBool(false)
	sets := map[string]xcBSet{"us-work-authorization-status": xcBPref("I am authorized to work in this country for any employer", "for any employer"),
		"current-immigration-status": xcBPref("F-1 OPT", "OPT", "F1 OPT", "Optional Practical Training", "Other")}
	tw := match.Posting{Company: "Twilio", Location: "Remote - US"}
	xcBRunCases(t, []xcBCase{
		{name: "OPT holds a valid visa", sets: sets, facts: xcBFacts(), posting: tw, q: q, entry: "us-work-authorization-status", label: opts[2]},
		{name: "a citizen", sets: sets, facts: citizen, posting: tw, q: q, wantNone: true},
	})
}

// Gemini (Greenhouse, queue164): the required "Applicant Privacy Statement"
// checkbox group holds two consents, "By clicking this box ... you consent to
// our Applicant Privacy Statement." and the same to a third-party background
// check. Under the legal-declaration setting both are ticked; a marketing
// opt-in beside a consent keeps the group to the one consent; with the setting
// off nothing is.
func TestXC_AGroupOfConsentBoxesTicksEach(t *testing.T) {
	privacy := "By clicking this box and submitting your application, you consent to our Applicant Privacy Statement."
	check := "By clicking this box and submitting your application, you consent to Strider Technologies conducting third-party background checks and utilizing advanced tools to assess potential risks as part of your application process."
	// The engine reads a group of two boxes as a multi-value control.
	group := func(labels ...string) match.Question {
		return match.Question{Selector: "s", Text: "Applicant Privacy Statement", Kind: match.KindCheckbox, InputType: match.InputMultiSelect, Required: true, Options: xcBOpts(labels...)}
	}
	sets := map[string]xcBSet{"privacy-policy-consent": xcBAns("Yes")}
	gm := match.Posting{Company: "Gemini", Location: "New York City, NY"}
	xcBRunCases(t, []xcBCase{
		{name: "both consents", sets: sets, facts: xcBFacts(), posting: gm, legal: true, q: group(privacy, check),
			entry: "privacy-policy-consent", label: privacy, answer: privacy + ", " + check},
		{name: "a marketing opt-in beside the consent", sets: sets, facts: xcBFacts(), posting: gm, legal: true,
			q: group(privacy, "By clicking this box, you consent to receive marketing emails about future job opportunities."), entry: "privacy-policy-consent", label: privacy, answer: privacy},
		{name: "the setting off", sets: sets, facts: xcBFacts(), posting: gm, q: group(privacy, check), wantNone: true},
	})
}

// Scale AI (Greenhouse, queue190): "Do you have a security clearance?" lists
// the levels and splits No by interest: "No - I am interested in obtaining
// one." and "No - I am NOT interested in obtaining one.". A No to the history
// picks between them by the willing-to-obtain entry; without that entry the
// split stays the user's.
func TestXC_NoClearanceSplitByInterest(t *testing.T) {
	q := xcBQ("Do you have a security clearance?", match.KindCustomListbox, "TS/SCI w/ FSP", "TS/SCI w/ CI Poly", "TS/SCI (or equivalent)", "TS (or equivalent)",
		"Secret (or equivalent)", "No - I am interested in obtaining one.", "No - I am NOT interested in obtaining one.")
	q.Required = true
	never := xcBAns("No")
	xcBRunCases(t, []xcBCase{
		{name: "not willing", sets: map[string]xcBSet{"security-clearance-history": never, "willing-to-obtain-clearance": xcBPref("No")}, facts: xcBFacts(), q: q,
			label: "No - I am NOT interested in obtaining one."},
		{name: "willing", sets: map[string]xcBSet{"security-clearance-history": never, "willing-to-obtain-clearance": xcBPref("Yes")}, facts: xcBFacts(), q: q,
			label: "No - I am interested in obtaining one."},
		{name: "no willing entry", sets: map[string]xcBSet{"security-clearance-history": never, "willing-to-obtain-clearance": xcBPref()}, facts: xcBFacts(), q: q, wantNone: true},
		{name: "held one", sets: map[string]xcBSet{"security-clearance-history": xcBAns("Yes"), "willing-to-obtain-clearance": xcBPref("No")}, facts: xcBFacts(), q: q, wantNone: true},
	})
}

// Flexport (Greenhouse, live): the years in a subject none of the user's
// information names are none, though the career's recorded years are a fuzzy
// match of the question's words.
func TestXC_YearsInASubjectNamedNowhereAreNoneOverTheCareersYears(t *testing.T) {
	facts := xcBFacts()
	facts.KnowledgeRead = true
	facts.Evidence = []string{"Built the billing service in Go", "Led the payments design review"}
	named := facts
	named.Evidence = append([]string{"Ran accounts payable for a freight broker"}, facts.Evidence...)
	q := xcBText("How many years of hands-on experience do you have in Accounts Payable, freight auditing, or account reconciliation?")
	q.InputType = match.InputNumber
	xcBRunCases(t, []xcBCase{
		{name: "the career's years recorded", sets: map[string]xcBSet{"years-full-time-work-experience-number": xcBAns("1")}, facts: facts, q: q, answer: "0"},
		// Named in the evidence, the subject is no longer none: the career's
		// recorded years answer it, in the review band.
		{name: "a subject the evidence names", sets: map[string]xcBSet{"years-full-time-work-experience-number": xcBAns("1")}, facts: named, q: q, answer: "1"},
		// n8n (Ashby, owner workspace): with no qualifier before "experience",
		// route 284 read the career's total and took the full-time years' "1"
		// ("1 to 2 years" of sales).
		{name: "a subject after a bare experience", sets: map[string]xcBSet{"years-full-time-work-experience-number": xcBAns("1")}, facts: facts,
			q: xcBQ("How many years of experience do you have in sales or as a sales development representative?", match.KindRadioGroup,
				"None", "Less than 1 year", "1 to 2 years", "2 to 3 years", "3 to 5 years", "More than 5 years"),
			notEntry: "years-full-time-work-experience-number", label: "None"},
	})
}

// Lime (Ashby, live): "To support team collaboration, this role requires you to
// reside in and work remotely from Canada. Do you meet this requirement?" took
// the role-requirements entry's Yes for an address in South Carolina, and "Do
// you currently reside in Canada?" was left. Living in a named country, asked
// whole or as a requirement to meet, is read from the recorded address's
// country; a name that is also a U.S. state (Georgia, "CA") is never read as
// the country.
func TestXC_ResidingInANamedCountry(t *testing.T) {
	us := xcBFacts()
	us.Address = match.AddressFact{City: "Charleston", Region: "SC", Country: "US"}
	ca := xcBFacts()
	ca.Address = match.AddressFact{City: "Toronto", Region: "ON", Country: "CA"}
	ga := xcBFacts()
	ga.Address = match.AddressFact{City: "Atlanta", Region: "GA", Country: "US"}
	sets := map[string]xcBSet{"meets-role-requirements": xcBAns("Yes"), "based-in-named-metro": xcBAns("No")}
	const lime = "To support team collaboration, this role requires you to reside in and work remotely from Canada. Do you meet this requirement?"
	xcBRunCases(t, []xcBCase{
		{name: "a requirement to live in another country", sets: sets, facts: us, q: xcBYesNo(lime), notEntry: "meets-role-requirements", label: "No"},
		{name: "a requirement to live in one's own country", sets: sets, facts: ca, q: xcBYesNo(lime), notEntry: "meets-role-requirements", label: "Yes"},
		{name: "the requirement in the United States", sets: sets, facts: us, q: xcBYesNo("This role requires you to reside in the United States. Do you meet this requirement?"), notEntry: "meets-role-requirements", label: "Yes"},
		{name: "another country asked whole", sets: sets, facts: us, q: xcBYesNo("Do you currently reside in Canada?"), label: "No"},
		{name: "one's own country asked whole", sets: sets, facts: ca, q: xcBYesNo("Are you based in Canada?"), label: "Yes"},
		{name: "a state that is a country's name too", sets: sets, facts: ga, q: xcBYesNo("Are you a resident of Georgia?"), label: "Yes"},
		{name: "a state code that is a country's code too", sets: sets, facts: ga, q: xcBYesNo("Do you currently reside in CA?"), label: "No"},
	})
}

// Alnylam (Greenhouse, live): a posting in "Norton, MA" left "Are you legally
// authorized to work in the country/region of the job for which you are
// applying?" because "MA" is Massachusetts and Morocco and Norton is no city
// the table knows. A colliding code is the state when the posting's own text
// names the state or the United States and never the other country.
func TestXC_AStateCodeThatIsACountryCodeSettledByThePostingText(t *testing.T) {
	const auth = "Are you legally authorized to work in the country/region of the job for which you are applying?"
	sets := map[string]xcBSet{"work-auth-us": xcBAns("Yes"), "sponsorship-now-or-future": xcBAns("Yes")}
	posting := func(loc, jd string) match.Posting {
		return match.Posting{Company: "Alnylam", Location: loc, Description: jd}
	}
	const ma = "This role is based at our manufacturing site in Norton, Massachusetts. Alnylam is a U.S. biopharmaceutical company."
	xcBRunCases(t, []xcBCase{
		{name: "the state named in the posting", sets: sets, facts: xcBFacts(), posting: posting("Norton, MA", ma), q: xcBYesNo(auth), entry: "work-auth-us", label: "Yes"},
		{name: "the United States named in the posting", sets: sets, facts: xcBFacts(), posting: posting("Redding, CA", "Join our team in the United States."), q: xcBYesNo(auth), entry: "work-auth-us", label: "Yes"},
		{name: "several places, each settled", sets: sets, facts: xcBFacts(), posting: posting("North Reading, MA; Westborough, MA", ma), q: xcBYesNo(auth), entry: "work-auth-us", label: "Yes"},
		{name: "the other country named too", sets: sets, facts: xcBFacts(), posting: posting("Redding, CA", "Offices in the United States and Canada."), q: xcBYesNo(auth), wantNone: true},
		{name: "no posting text", sets: sets, facts: xcBFacts(), posting: posting("Norton, MA", ""), q: xcBYesNo(auth), wantNone: true},
		{name: "a posting that names the other country", sets: sets, facts: xcBFacts(), posting: posting("Pune, IN", "Our engineering centre in India is growing."), q: xcBYesNo(auth), wantNone: true},
	})
}

// A work-authorization or sponsorship question about a country the profile
// does not record (Lime, Toronto) is the user's other-country entry's, for
// review (owner decision); blank, it stays the posting_country abstain. A
// negated wording still takes its complement, and a question that also names
// a recorded country keeps the abstain. A citizen may work in their own
// country, so a country of a recorded citizenship is never the other-country
// answer's: it stays the abstain.
func TestXC_AnUnrecordedCountryIsTheUsersOtherCountryAnswer(t *testing.T) {
	toronto := match.Posting{Company: "Lime", Location: "Toronto, Ontario, Canada"}
	shanghai := match.Posting{Company: "Lime", Location: "Shanghai, China"}
	f := xcBFacts()
	f.RequiresSponsorshipNow, f.RequiresSponsorshipFuture = xcBBool(false), xcBBool(true)
	f.Citizenships = []string{"CN"}
	written := map[string]xcBSet{"work-auth-us": xcBAns("Yes"), "work-authorization-other-countries": xcBAns("No"), "sponsorship-other-countries": xcBAns("Yes")}
	blank := map[string]xcBSet{"work-auth-us": xcBAns("Yes")}
	auth := xcBYesNo("Are you legally authorized to work in the country where this role is located?")
	sponsor := xcBYesNo("Will you now or in the future require visa sponsorship to work in the country where this role is located?")
	xcBRunCases(t, []xcBCase{
		{name: "work authorization from the user's No", sets: written, facts: f, posting: toronto, q: auth, entry: "work-authorization-other-countries", label: "No"},
		{name: "sponsorship from the user's Yes", sets: written, facts: f, posting: toronto, q: sponsor, entry: "sponsorship-other-countries", label: "Yes"},
		{name: "a negated work-authorization wording takes the complement", sets: written, facts: f, posting: toronto,
			q: xcBYesNo("Can you confirm you are not authorized to work in the country where this role is located?"), entry: "work-authorization-other-countries", label: "Yes"},
		{name: "blank entries keep the abstain", sets: blank, facts: f, posting: toronto, q: auth,
			wantNone: true, reason: match.ReasonContextPolicy, detail: match.DetailPostingCountry},
		{name: "a recorded country beside an unrecorded one keeps the abstain", sets: written, facts: f, q: xcBYesNo("Are you legally authorized to work in the United States and Canada?"),
			wantNone: true},
		{name: "a posting in the recorded country is the profile's", sets: written, facts: f, posting: match.Posting{Location: "Raleigh, NC"}, q: auth, entry: "work-auth-us", label: "Yes"},
		// MotherDuck (Ashby, Amsterdam) and others: a question that names the
		// country beyond Canada is the work-authorization question.
		{name: "eligible to work in the Netherlands", sets: written, facts: f, posting: match.Posting{Company: "MotherDuck", Location: "Amsterdam"},
			q: xcBYesNo("Are you currently eligible to work in the Netherlands?"), entry: "work-authorization-other-countries", label: "No"},
		{name: "authorized to work in Germany", sets: written, facts: f, posting: match.Posting{Company: "Clera", Location: "Munich, Germany"},
			q: xcBYesNo("Are you authorized to work in Germany?"), entry: "work-authorization-other-countries", label: "No"},
		{name: "eligible to work in the UK", sets: written, facts: f, posting: match.Posting{Company: "Harvey", Location: "London, UK"},
			q: xcBYesNo("Are you eligible to work in the UK?"), entry: "work-authorization-other-countries", label: "No"},
		{name: "a role naming no country is no such question", sets: written, facts: f, posting: toronto,
			q: xcBYesNo("Are you eligible to work in this role on a hybrid schedule?"), notEntry: "work-authorization-other-countries", wantNone: true},
		// Shield AI (Lever, Melbourne): working there without sponsorship is
		// the complement of needing it.
		{name: "the ability to work without sponsorship takes the complement", sets: written, facts: f, posting: match.Posting{Company: "Shield AI", Location: "Melbourne"},
			q: xcBYesNo("Do you have the ability to work in Australia without the need for sponsorship?"), entry: "sponsorship-other-countries", label: "No"},
		// Groupon (Greenhouse, Milan): the sponsorship question points back to
		// the posting's country with "there".
		{name: "sponsorship to work there", sets: written, facts: f, posting: match.Posting{Company: "Groupon", Location: "Milan"},
			q: xcBYesNo("Will you now or in the future require employer sponsorship to work there?"), entry: "sponsorship-other-countries", label: "Yes"},
		{name: "a posting in a country of citizenship keeps the abstain", sets: written, facts: f, posting: shanghai, q: auth,
			wantNone: true, reason: match.ReasonContextPolicy, detail: match.DetailPostingCountry},
		{name: "and so does its sponsorship question", sets: written, facts: f, posting: shanghai, q: sponsor,
			wantNone: true, reason: match.ReasonContextPolicy, detail: match.DetailPostingCountry},
	})
}

// Oklo (Greenhouse, live): the options are A, B and C, and only the
// description under the question says what each letter stands for (owner
// decision: the engine reads that text). Each letter is read as its
// definition: A the U.S. person, B a non-U.S. person who is a citizen only of
// the List I countries the definition names, C every other non-U.S. person.
// The recorded citizenships decide B or C; nothing recorded leaves it.
func TestXC_LetteredOptionsReadTheirDefinitionsFromTheDescription(t *testing.T) {
	const desc = "The questions in this section are for the sole purpose of ensuring compliance with U.S. regulations concerning the export of controlled or protected technologies or information, including 10 CFR Part 810. " +
		"Please identify yourself as one of the following A, B, or C as described below: * ________________________________________________________________________ " +
		"A . U.S. Person, which is defined for export control purposes as either: a U.S. citizen a U.S. national (one born in American Samoa or the Commonwealth of the Northern Mariana Islands) a U.S. lawful permanent resident (commonly referred to as a “green card holder”), or a protected person under the Immigration and Nationality Act ________________________________________________________________________ " +
		"B . Not a U.S. Person, as defined above, and affiliation as a citizen, national, or permanent resident of only countries listed in List I below. This list is derived from 10 CFR Part 810, Appendix A, which is linked here . " +
		"List I Australia Germany Poland Austria Greece Portugal Belgium Hungary Romania Brazil Indonesia Slovakia Bulgaria Ireland Slovenia Canada Italy South Africa Colombia Japan Spain Croatia Korea, Republic of Sweden Cyprus Latvia Switzerland Czech Republic Lithuania Taiwan Denmark Luxembourg Turkey Egypt Malta United Arab Emirates Estonia Morocco United Kingdom Finland Netherlands Vietnam France Norway __________________________________________________________________________ " +
		"C. Not a U.S. Person, as defined in A above, and not a citizen, national, or permanent resident of only countries listed in List I in B, above. ________________________________________________________________________ " +
		"Please identify yourself as one of the following A, B, or C as described above: *"
	q := xcBQ("Export Control Question:", match.KindCustomListbox, "A", "B", "C")
	q.Description, q.Required = desc, true
	cn := xcBFacts()
	cn.Citizenships, cn.Address.Country = []string{"CN"}, "US"
	cn.RequiresSponsorshipNow, cn.RequiresSponsorshipFuture = xcBBool(false), xcBBool(true)
	gb := cn
	gb.Citizenships = []string{"GB"}
	dual := cn
	dual.Citizenships = []string{"GB", "CN"}
	none := cn
	none.Citizenships = nil
	us := xcBFacts()
	us.Citizenships = []string{"US"}
	bare := xcBQ("Export Control Question:", match.KindCustomListbox, "A", "B", "C")
	bare.Required = true
	xcBRunCases(t, []xcBCase{
		{name: "a U.S. person is A", sets: map[string]xcBSet{"us-person-itar-ear": xcBAns("Yes")}, facts: us, q: q, entry: "us-person-itar-ear", label: "A"},
		{name: "a Chinese citizen is C", facts: cn, q: q, entry: "us-person-itar-ear", label: "C"},
		{name: "a British citizen is B, for review", facts: gb, q: q, entry: "us-person-itar-ear", label: "B"},
		{name: "a citizen of a List I country and another is C", facts: dual, q: q, entry: "us-person-itar-ear", label: "C"},
		{name: "no citizenship recorded leaves it", facts: none, q: q, wantNone: true},
		{name: "without the description the letters say nothing", facts: cn, q: bare, wantNone: true},
	})
}

// Method (Ashby) and Treasury Prime (Greenhouse): "Employment Status" over
// "I'm authorized to work in the United States" and "I'm looking for visa
// sponsorship" asks the sponsorship standing in its two options. A need now
// or later is the sponsorship option; no need is the authorization.
func TestXC_EmploymentStatusOverAuthorizedOrLookingForSponsorship(t *testing.T) {
	authorized, looking := "I'm authorized to work in the United States", "I'm looking for visa sponsorship"
	q := xcBQ("Employment Status", match.KindRadioGroup, authorized, looking)
	sets := map[string]xcBSet{"work-auth-us": xcBAns("Yes")}
	xcBRunCases(t, []xcBCase{
		{name: "a need only later", sets: sets, facts: xcBNeeds(false, true), q: q, entry: "work-auth-and-future-sponsorship", label: looking},
		{name: "a need now", sets: sets, facts: xcBNeeds(true, true), q: q, entry: "work-auth-and-future-sponsorship", label: looking},
		{name: "no need", sets: sets, facts: xcBNeeds(false, false), q: q, entry: "work-auth-and-future-sponsorship", label: authorized},
		{name: "an employment status of another kind", sets: sets, facts: xcBNeeds(false, true), q: xcBQ("Employment Status", match.KindRadioGroup, "Employed", "Unemployed", "Student"), notEntry: "work-auth-and-future-sponsorship", wantNone: true},
	})
}

// Anduril (Greenhouse, owner workspace): a blank entry only the user's answer
// fills is terminal, so no model lane is asked it; once written it fills.
func TestXC_ABlankOwnAnswerEntryIsTheUsersAlone(t *testing.T) {
	m := match.New(xcBBank(t, nil), match.DefaultStages())
	for _, c := range []struct{ q, entry string }{
		{"Are you willing to share your academic transcripts?", "academic-transcripts-shareable"},
		// Skydio (Ashby, owner workspace): the drone pilot certificate.
		{"Do you currently have your FAA Part 107?", "faa-part-107"},
		// Applied Intuition (Ashby, owner workspace): an immigration case in progress.
		{"Do you currently have an active immigration case (ex H-1B extension, green card)?", "active-immigration-case"},
	} {
		resp, err := m.Match(match.Request{Host: "jobs.example.com", Facts: xcBFacts(), Today: xcBDay, Questions: []match.Question{xcBYesNo(c.q)}})
		if err != nil {
			t.Fatal(err)
		}
		if r := resp.Results[0]; r.Outcome != match.OutcomeAbstained || r.Reason != match.ReasonPlaceholderEmpty || !r.Terminal || r.EntryID != c.entry {
			t.Fatalf("blank %s: %+v", c.entry, r)
		}
		xcBRunCases(t, []xcBCase{{name: "written " + c.entry, sets: map[string]xcBSet{c.entry: xcBAns("Yes")}, facts: xcBFacts(),
			q: xcBYesNo(c.q), entry: c.entry, label: "Yes"}})
	}
}
