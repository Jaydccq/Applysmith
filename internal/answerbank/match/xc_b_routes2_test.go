package match_test

import (
	"strings"
	"testing"

	"applysmith/internal/answerbank/match"
)

func TestXC_RoutesDisclosures(t *testing.T) {
	coi := xcBNoAll("government-official-disclosure", "government-position", "government-position-family", "outside-business-activities",
		"competitor-or-related-investment", "ip-ownership-to-retain", "influence-decisions-employer", "director-officer-investor-5pct",
		"non-compete-or-non-solicitation", "competitive-outside-activities", "federal-procurement-involvement", "citizen-cuba-iran-north-korea-syria")
	rel := map[string]xcBSet{"relatives-employed-list": xcBAns("N/A"), "relatives-at-employer": xcBAns("No"), "former-employer": xcBAns("Example Corp")}
	longText := func(text string) match.Question {
		return match.Question{Selector: "s", Text: text, Kind: match.KindText, InputType: match.InputLongText}
	}
	jobs := map[string]xcBSet{"current-company": xcBAns("Example Corp"), "current-job-title": xcBAns("Software Engineer"), "most-recent-school": xcBAns("Example State University")}
	meets := map[string]xcBSet{"meets-role-requirements": xcBAns("Yes"), "earliest-start-date": xcBAns("2031-08-01")}
	xcBRunCases(t, []xcBCase{
		// XC-049
		{name: "XC-049 government official", sets: coi, facts: xcBFacts(),
			q: xcBYesNo("Are you related to, or have you been referred by, a government official?"), label: "No", notEntry: "outside-business-activities"},
		{name: "XC-049 outside business activities", sets: coi, facts: xcBFacts(),
			q: xcBYesNo("Do you have any outside business activities?"), entry: "outside-business-activities", label: "No"},
		{name: "XC-049 competitor investment", sets: coi, facts: xcBFacts(),
			q:     xcBYesNo("Do you have an investment in a private competitor of Example Corp or in a company that has a business relationship with Example Corp?"),
			entry: "competitor-or-related-investment", label: "No"},
		{name: "XC-049 intellectual property", sets: coi, facts: xcBFacts(),
			q: xcBYesNo("Do you own any intellectual property that you wish to retain?"), entry: "ip-ownership-to-retain", label: "No"},
		{name: "XC-049 influence decisions", sets: coi, facts: xcBFacts(),
			q: xcBYesNo("Do you have the ability to influence decisions related to Example Corp?"), entry: "influence-decisions-employer", label: "No"},
		{name: "XC-049 director officer or five percent investor", sets: coi, facts: xcBFacts(),
			q:     xcBYesNo("Are you a director, officer, adviser, consultant or partner of, or an investor holding more than 5 percent of, any for-profit company?"),
			entry: "director-officer-investor-5pct", label: "No"},
		{name: "XC-049 contractual restrictions", sets: coi, facts: xcBFacts(),
			q:     xcBYesNo("Are you subject to any contractual restrictions on working for Example Corp?"),
			entry: "non-compete-or-non-solicitation", label: "No"},
		{name: "XC-049 competing outside activities", sets: coi, facts: xcBFacts(),
			q:     xcBYesNo("Do you have outside activities that could be considered competitive with Example Corp?"),
			entry: "competitive-outside-activities", label: "No"},
		{name: "XC-049 defense contract", sets: coi, facts: xcBFacts(),
			q:     xcBYesNo("Have you been involved in the award or administration of contracts with the Department of Defense in the last five years?"),
			entry: "federal-procurement-involvement", label: "No"},
		{name: "XC-049 numbered national list", sets: coi, facts: xcBFacts(),
			q:     xcBYesNo("Are you a national of any of the following countries: 1. Cuba 2. Iran 3. North Korea 4. Syria?"),
			entry: "citizen-cuba-iran-north-korea-syria", label: "No"},
		{name: "XC-049 national of the United States", sets: coi, facts: xcBFacts(),
			q: xcBYesNo("Are you a national of the United States?"), notEntry: "citizen-cuba-iran-north-korea-syria", wantNone: true},

		// XC-050
		{name: "XC-050 list relatives text box", sets: rel, facts: xcBFacts(),
			q:     longText("Please list any relatives or friends currently employed by Example Corp. Write N/A if not applicable."),
			entry: "relatives-employed-list", answer: "N/A"},
		{name: "XC-050 relative name and relationship", sets: rel, facts: xcBFacts(),
			q:     longText("Name of Relative(s) and Relationship Employed by Example Corp"),
			entry: "relatives-employed-list", answer: "N/A"},
		{name: "XC-050 the yes detail in a short box", sets: rel, facts: xcBFacts(),
			q:     xcBText("If you answered yes to being related to a current Qualtrics employee, please specify name and relationship (N/A if not applicable)."),
			entry: "relatives-employed-list", answer: "N/A"},
		// Qualtrics (live): with the relatives list attested complete, the
		// relationship lane answered this box "No"; it is the list's.
		{name: "XC-050 the yes detail under a complete relatives list", sets: rel, facts: xcBAttestedRelatives(),
			q:     xcBText("If you answered yes to being related to a current Qualtrics employee, please specify name and relationship (N/A if not applicable)."),
			entry: "relatives-employed-list", answer: "N/A"},
		{name: "XC-050 yes no keeps its entry", sets: rel, facts: xcBFacts(),
			q: xcBYesNo("Do you have any relatives or friends employed by this company?"), entry: "relatives-at-employer", label: "No"},

		// XC-051
		{name: "XC-051 current or previous employer", sets: jobs, facts: xcBFacts(),
			q: xcBText("Who is your current or previous employer?"), entry: "current-company", answer: "Example Corp"},
		{name: "XC-051 most recent employer", sets: jobs, facts: xcBFacts(),
			q: xcBText("Current (or most recent) employer"), entry: "current-company", answer: "Example Corp"},
		{name: "XC-051 current or previous title", sets: jobs, facts: xcBFacts(),
			q: xcBText("What is your current or previous job title?"), entry: "current-job-title", answer: "Software Engineer"},
		{name: "XC-051 bare current title", sets: jobs, facts: xcBFacts(),
			q: xcBText("Current Title"), entry: "current-job-title", answer: "Software Engineer"},
		{name: "XC-051 most recent title", sets: jobs, facts: xcBFacts(),
			q: xcBText("Current (or most recent) title"), entry: "current-job-title", answer: "Software Engineer"},
		{name: "XC-051 blank employer stays blank", sets: map[string]xcBSet{}, facts: xcBFacts(),
			q: xcBText("Who is your current or previous employer?"), wantNone: true},

		// XC-052
		{name: "XC-052 meet these requirements", sets: meets, facts: xcBFacts(),
			q: xcBYesNo("Do you meet these requirements?"), entry: "meets-role-requirements", label: "Yes"},
		{name: "XC-052 basic job requirements", sets: meets, facts: xcBFacts(),
			q: xcBYesNo("Do you have at least the basic job requirements listed?"), entry: "meets-role-requirements", label: "Yes"},
		{name: "XC-052 immigration paragraph mentions the start date", sets: meets, facts: xcBFacts(),
			q:     xcBYesNo("Candidates must present their eligibility documents before the start date. Do you meet these requirements?"),
			entry: "meets-role-requirements", label: "Yes"},
		{name: "XC-052 earliest start date keeps its entry", sets: meets, facts: xcBFacts(),
			q: xcBText("What is your earliest start date?"), entry: "earliest-start-date"},
	})
}

// xcBAttestedRelatives is xcBFacts with a relatives list the user stated is
// complete and that holds no one.
func xcBAttestedRelatives() match.Facts {
	f := xcBFacts()
	f.RelationshipsComplete = map[match.RelationshipFamily]string{match.FamilyRelative: "2026-10-01"}
	return f
}

// Ello (Ashby, live): "Will you require sponsorship to work in the U.S. right
// now? (Feel free to explain.)" asks the need now alone, which an F-1 OPT
// holder answers No; the invitation to explain asks for no story. Without a
// time word, or with "now or in the future", the need is the combined one.
func TestXC_SponsorshipRightNow(t *testing.T) {
	// The need is derived from the profile, as the shipped placeholder entry is.
	sets := map[string]xcBSet{"work-auth-us": xcBAns("Yes")}
	ello := match.Posting{Company: "Ello", Location: "San Francisco, CA"}
	// The engine reads a one-line box as text.
	box := func(text string) match.Question {
		q := xcBText(text)
		q.InputType = match.InputText
		return q
	}
	xcBRunCases(t, []xcBCase{
		{name: "right now, feel free to explain", sets: sets, facts: xcBFacts(), posting: ello,
			q: box("Immigration: Will you require sponsorship to work in the U.S. right now? (Feel free to explain.)"), entry: "sponsorship-now-or-future", answer: "No, I do not currently require sponsorship"},
		{name: "right now in a box", sets: sets, facts: xcBFacts(), posting: ello,
			q: box("Will you require sponsorship to work in the U.S. right now?"), entry: "sponsorship-now-or-future", answer: "No, I do not currently require sponsorship"},
		{name: "right now over Yes and No", sets: sets, facts: xcBFacts(), posting: ello,
			q: xcBYesNo("Will you require sponsorship to work in the U.S. right now?"), entry: "sponsorship-now-or-future", label: "No"},
		{name: "no time word", sets: sets, facts: xcBFacts(), posting: ello,
			q: xcBYesNo("Will you require sponsorship to work in the U.S.?"), entry: "sponsorship-now-or-future", label: "Yes"},
		{name: "now or in the future", sets: sets, facts: xcBFacts(), posting: ello,
			q: xcBYesNo("Will you require sponsorship to work in the U.S. now or in the future?"), entry: "sponsorship-now-or-future", label: "Yes"},
	})
}

// Meadow (Ashby, live): a note after the sponsorship question that only
// states the employer's limit ("We are only considering candidates who do not
// require sponsorship ... we will reject your application.") asks nothing; the
// question is answered from the record, whichever way it goes. A note that
// tells the answer is not set aside.
func TestXC_SponsorshipWithTheEmployersLimitNoted(t *testing.T) {
	sets := map[string]xcBSet{"work-auth-us": xcBAns("Yes")}
	const meadow = "Do you require, now or in the future, sponsorship for employment visa status to work in the United States? Please Note: We are only considering candidates who do not require sponsorship to work in the U.S. at this time. As such, if you require sponsorship, we will reject your application."
	citizen := xcBFacts()
	citizen.VisaStatus, citizen.RequiresSponsorshipFuture = "US Citizen", xcBBool(false)
	xcBRunCases(t, []xcBCase{
		{name: "a recorded need", sets: sets, facts: xcBFacts(), q: xcBYesNo(meadow), entry: "sponsorship-now-or-future", label: "Yes"},
		{name: "no recorded need", sets: sets, facts: citizen, q: xcBYesNo(meadow), entry: "sponsorship-now-or-future", label: "No"},
		{name: "a note that tells the answer", sets: sets, facts: xcBFacts(),
			q: xcBYesNo("Do you require sponsorship to work in the United States? We do not sponsor, so please answer No if you can work here."), wantNone: true},
	})
}

// Snowflake (Ashby, live): a purpose lead ("For Snowflake to anticipate
// possible immigration timelines and obligations, ...") and a request to
// confirm ("could you confirm you are currently authorized ...") ask the plain
// question: the recorded authorization answers it, and a negated statement
// takes its complement.
func TestXC_ConfirmAuthorizedAfterAPurposeLead(t *testing.T) {
	sets := map[string]xcBSet{"work-auth-us": xcBAns("Yes")}
	sf := match.Posting{Company: "Snowflake", Location: "US-CA-Menlo Park"}
	xcBRunCases(t, []xcBCase{
		{name: "purpose and confirm", sets: sets, facts: xcBFacts(), posting: sf,
			q: xcBYesNo("For Snowflake to anticipate possible immigration timelines and obligations, could you confirm you are currently authorized to work in the country to which you are applying?"), entry: "work-auth-us", label: "Yes"},
		{name: "a negated statement", sets: sets, facts: xcBFacts(), posting: sf,
			q: xcBYesNo("Can you confirm you are not authorized to work in the United States?"), entry: "work-auth-us", label: "No"},
	})
}

// Automox (Greenhouse, live): "Are you legally able to work in the US?" is
// the plain work-authorization question; "How many years of experience do you
// have ... in a high availability SaaS environment?" is no notice period, and
// the notice period's "in 1 Month" is never read as one million years.
func TestXC_AutomoxAuthorizationAndHighAvailability(t *testing.T) {
	sets := map[string]xcBSet{"work-auth-us": xcBAns("Yes"), "availability-notice-period": xcBPref("in 1 Month")}
	am := match.Posting{Company: "Automox", Location: "Remote - US"}
	years := xcBQ("How many years of experience do you have designing, developing, testing, and monitoring applications deployed in a high availability SaaS environment?",
		match.KindCustomListbox, "Less than 1", "1-2", "3-5", "6-8", "9+")
	xcBRunCases(t, []xcBCase{
		{name: "legally able", sets: sets, facts: xcBFacts(), posting: am, q: xcBYesNo("Are you legally able to work in the US?"), entry: "work-auth-us", label: "Yes"},
		{name: "high availability", sets: sets, facts: xcBFacts(), posting: am, q: years, notEntry: "availability-notice-period", wantNone: true},
	})
}

// Bree (Ashby, live): "Do you have 2+ years of experience with machine
// learning?" over a profile that records three years with LLMs: LLMs are a
// kind of machine learning, so the three years meet two; they are a lower
// bound, so five is not settled by them.
func TestXC_MachineLearningYearsFromAKindOfIt(t *testing.T) {
	bree := match.Posting{Company: "Bree", Location: "Remote"}
	xcBRunCases(t, []xcBCase{
		{name: "two years", facts: xcBFacts(), posting: bree, q: xcBYesNo("Do you have 2+ years of experience with machine learning?"), label: "Yes"},
		{name: "five years", facts: xcBFacts(), posting: bree, q: xcBYesNo("Do you have 5+ years of experience with machine learning?"), wantNone: true},
	})
}

// Stellar (Ashby, live): the options split Yes by time, not by visa ("Yes, I
// will require immigration sponsorship now ...", "Yes, ... in the future
// ..."), so the recorded standing picks one: a need only later takes the
// future Yes, a need now the present one.
func TestXC_SponsorshipYesSplitByTime(t *testing.T) {
	sets := map[string]xcBSet{"work-auth-us": xcBAns("Yes")}
	const q = "Will you now or in the future require company sponsorship to retain or extend your work authorization in the country where the job is located?"
	const now, later, no = "Yes, I will require immigration sponsorship now to legally work in the country where the job is located.",
		"Yes, I will require immigration sponsorship in the future to legally work in the country where the job is located.",
		"No, I do not and will not require immigration sponsorship to legally work in the country where the job is located."
	needNow := xcBFacts()
	needNow.RequiresSponsorshipNow = xcBBool(true)
	ny := match.Posting{Company: "Stellar", Location: "New York"}
	xcBRunCases(t, []xcBCase{
		{name: "a need only later", sets: sets, facts: xcBFacts(), posting: ny, q: xcBQ(q, match.KindRadioGroup, now, later, no), entry: "sponsorship-now-or-future", label: later},
		{name: "a need now", sets: sets, facts: needNow, posting: ny, q: xcBQ(q, match.KindRadioGroup, now, later, no), entry: "sponsorship-now-or-future", label: now},
		// Tessera Labs (Ashby, live): the Yes options name only the time.
		{name: "bare times, a need only later", sets: sets, facts: xcBFacts(), posting: ny,
			q: xcBQ("Will you now or in the future require immigration sponsorship?", match.KindRadioGroup, "Yes, now", "Yes, in the future", "No"), entry: "sponsorship-now-or-future", label: "Yes, in the future"},
		{name: "bare times, a need now", sets: sets, facts: needNow, posting: ny,
			q: xcBQ("Will you now or in the future require immigration sponsorship?", match.KindRadioGroup, "Yes, now", "Yes, in the future", "No"), entry: "sponsorship-now-or-future", label: "Yes, now"},
	})
}

// Kinetic (Ashby, live): "How did you use AI to ship this project?" asks the
// manner of a project the previous answer named; no saved answer fills it.
func TestXC_AMannerAskedOfAProjectIsProse(t *testing.T) {
	q := xcBText("How did you use AI to ship this project?")
	q.InputType = match.InputLongText
	xcBRunCases(t, []xcBCase{
		{name: "how did you use", sets: map[string]xcBSet{}, facts: xcBFacts(), q: q, wantNone: true},
	})
}

// Tenex (Ashby, live): "If you were referred by a Current Employee, please
// list their name. If this is not applicable, please list N/A." asks a name
// only of a referred applicant; the recorded No referral makes it "N/A". Without
// a recorded answer, or with a Yes, the name stays the user's.
func TestXC_ReferrerNameWhenNoOneReferred(t *testing.T) {
	const tenex = "If you were referred by a Current Employee, please list their name. If this is not applicable, please list N/A."
	no := map[string]xcBSet{"referred-by-current-employee": xcBAns("No")}
	yes := map[string]xcBSet{"referred-by-current-employee": xcBAns("Yes")}
	box := func(text string, required bool) match.Question {
		q := xcBText(text)
		q.InputType, q.Required = match.InputText, required
		return q
	}
	xcBRunCases(t, []xcBCase{
		{name: "no referral, N/A asked", sets: no, facts: xcBFacts(), q: box(tenex, true), entry: "referrer-name", answer: "N/A"},
		{name: "no referral, required", sets: no, facts: xcBFacts(), q: box("If you were referred by a current employee, please provide their name.", true), entry: "referrer-name", answer: "N/A"},
		{name: "no referral, optional", sets: no, facts: xcBFacts(), q: box("If you were referred by a current employee, please provide their name.", false), wantNone: true},
		{name: "a referral", sets: yes, facts: xcBFacts(), q: box(tenex, true), wantNone: true},
		{name: "nothing recorded", facts: xcBFacts(), q: box(tenex, true), wantNone: true},
	})
}

// Circleback (Ashby, live): "Are you authorized to work in the United States?"
// over "Yes", "Yes, but I will need visa sponsorship in the future", "Yes, but
// I will need a visa transfer" and a No qualifies the Yes with the sponsorship
// standing: a need only later takes its option, no need the bare Yes. A visa
// transfer is the standing of a visa holder, which the facts do not record, so
// a need now stays the user's.
func TestXC_AuthorizedYesQualifiedByTheSponsorshipStanding(t *testing.T) {
	sets := map[string]xcBSet{"work-auth-us": xcBAns("Yes")}
	cb := match.Posting{Company: "Circleback", Location: "San Francisco"}
	const later = "Yes, but I will need visa sponsorship in the future"
	q := xcBQ("Are you authorized to work in the United States? We can transfer work visas and we will sponsor visas.", match.KindRadioGroup,
		later, "Yes, but I will need a visa transfer", "Yes", "No, I can't legally work in the US yet")
	q.InputType = match.InputSingleSelect
	xcBRunCases(t, []xcBCase{
		{name: "a need later", sets: sets, facts: xcBNeeds(false, true), posting: cb, q: q, entry: "work-auth-and-future-sponsorship", label: later},
		{name: "no need", sets: sets, facts: xcBNeeds(false, false), posting: cb, q: q, entry: "work-auth-and-future-sponsorship", label: "Yes"},
		{name: "a need now", sets: sets, facts: xcBNeeds(true, true), posting: cb, q: q, wantNone: true},
	})
}

// Cobot (Ashby, live): "Have you earned a Master's degree in one of the fields
// mentioned above, as listed in the Minimum Requirements section?" points at
// the posting's degree requirement, read from its text as if the question
// named its fields. A posting that lists none leaves it to the user.
func TestXC_DegreeInTheFieldsThePostingLists(t *testing.T) {
	sets := map[string]xcBSet{"has-stem-degree": xcBPref("Yes")}
	const ask = "Have you earned a Master's degree in one of the fields mentioned above, as listed in the Minimum Requirements section?"
	const jd = "Minimum Qualifications: Master’s degree in Machine Learning, AI, Computer Science, or a related technical field. Minimum 3 years of experience in applied ML research. Preferred Qualifications: PhD in Machine Learning, AI, Computer Science, or related field."
	cobot := match.Posting{Company: "Cobot", Location: "Seattle", Description: jd}
	xcBRunCases(t, []xcBCase{
		{name: "the posting's fields", sets: sets, facts: xcBFacts(), posting: cobot, q: xcBYesNo(ask), entry: "has-stem-degree", label: "Yes"},
		{name: "no fields in the posting", sets: sets, facts: xcBFacts(), posting: match.Posting{Company: "Cobot", Location: "Seattle", Description: "We build robots."}, q: xcBYesNo(ask), wantNone: true},
	})
}

// Spire (Greenhouse, live): "Please select your citizenship." over a country
// list is the recorded citizenship, and "If you have dual citizenship, please
// select your second citizenship." over "Not Applicable" and the countries is
// Not Applicable when the profile records one citizenship and the entry no
// other. Unrecorded or dual citizenships leave the second to the user.
func TestXC_CitizenshipPickerAndTheSecondCitizenship(t *testing.T) {
	sets := map[string]xcBSet{"country-of-citizenship": xcBPref("China")}
	facts := func(cits ...string) match.Facts {
		f := xcBFacts()
		f.Citizenships = cits
		return f
	}
	spire := match.Posting{Company: "Spire", Location: "Boulder, Colorado, United States"}
	const first = "Spire collects the following information to comply with international laws and regulations. Please select your citizenship."
	const second = "If you have dual citizenship, please select your second citizenship."
	countries := []string{"Canada", "China", "Taiwan", "United States"}
	withNA := append([]string{"Not Applicable"}, countries...)
	xcBRunCases(t, []xcBCase{
		{name: "the citizenship picker", sets: sets, facts: facts("CN"), posting: spire,
			q: xcBQ(first, match.KindSelect, countries...), entry: "country-of-citizenship", label: "China"},
		{name: "one recorded citizenship has no second", sets: sets, facts: facts("CN"), posting: spire,
			q: xcBQ(second, match.KindSelect, withNA...), entry: "country-of-citizenship", label: "Not Applicable"},
		{name: "two recorded citizenships", sets: sets, facts: facts("CN", "CA"), posting: spire,
			q: xcBQ(second, match.KindSelect, withNA...), wantNone: true},
		{name: "the entry names another country", sets: map[string]xcBSet{"country-of-citizenship": xcBPref("Canada")}, facts: facts("CN"), posting: spire,
			q: xcBQ(second, match.KindSelect, withNA...), wantNone: true},
		{name: "unrecorded citizenships", sets: sets, facts: xcBFacts(), posting: spire,
			q: xcBQ(second, match.KindSelect, withNA...), wantNone: true},
		{name: "a second country of citizenship is not the first", sets: sets, facts: facts("CN"), posting: spire,
			q: xcBQ("Please select your second country of citizenship", match.KindSelect, countries...), wantNone: true},
		{name: "two recorded citizenships, a list sent without its options", sets: sets, facts: facts("CN", "CA"), posting: spire,
			q: xcBQ(second, match.KindCustomListbox), wantNone: true},
	})
	// The page's list of 197 is over the engine's option cap, so it arrives
	// without options: the answer names none, and the listbox is searched
	// for each other way of writing it in turn.
	t.Run("a list sent without its options", func(t *testing.T) {
		m := match.New(xcBBank(t, sets), match.DefaultStages())
		resp, err := m.Match(match.Request{Host: "jobs.example.com", Facts: facts("CN"), Today: xcBDay, Posting: spire,
			Questions: []match.Question{xcBQ(second, match.KindCustomListbox)}})
		if err != nil {
			t.Fatal(err)
		}
		r := resp.Results[0]
		if r.Outcome != match.OutcomeFilled || r.Answer != "Not Applicable" || strings.Join(r.Alternatives, "|") != "N/A|None" {
			t.Fatalf("result = %+v", r)
		}
	})
}

// Camber (Ashby, live): "Are you comfortable with that work arrangement?"
// follows "Are you aware that Camber is ... looking for team members to work
// hybrid from our office in Flatiron?" and takes the recorded acceptance that
// answered it. After a question no acceptance entry answered it is the user's.
func TestXC_ComfortableWithThatWorkArrangement(t *testing.T) {
	camber := match.Posting{Company: "Camber", Location: "New York, NY"}
	const aware = "Are you aware that Camber is based in New York City and looking for team members to work hybrid from our office in Flatiron?"
	const follow = "Are you comfortable with that work arrangement?"
	run := func(t *testing.T, ans string, first string) match.Result {
		t.Helper()
		m := match.New(xcBBank(t, map[string]xcBSet{"office-attendance-ok": xcBAns(ans)}), match.DefaultStages())
		q1, q2 := xcBYesNo(first), xcBYesNo(follow)
		q1.Selector, q2.Selector = "q1", "q2"
		resp, err := m.Match(match.Request{Host: "jobs.example.com", Facts: xcBFacts(), Today: xcBDay, Posting: camber, Questions: []match.Question{q1, q2}})
		if err != nil {
			t.Fatal(err)
		}
		return resp.Results[1]
	}
	for _, c := range []struct{ name, ans, first, want string }{
		{"a recorded Yes", "Yes", aware, "Yes"},
		{"a recorded No", "No", aware, "No"},
		{"after a question no acceptance answered", "Yes", "Are you aware that this role is a 12-month contract?", ""},
		// The question before asked whether the arrangement is a problem, and
		// its No is the acceptance's Yes.
		{"after a problem asked about it", "Yes", "We work on a hybrid model from our office in Flatiron. Will this present any challenges for you?", "Yes"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := run(t, c.ans, c.first)
			if c.want == "" {
				if r.Outcome != match.OutcomeAbstained {
					t.Fatalf("result = %+v", r)
				}
				return
			}
			if r.Outcome != match.OutcomeFilled || r.ChosenLabel != c.want || r.EntryID != "office-attendance-ok" {
				t.Fatalf("result = %+v", r)
			}
		})
	}
}

// Mirakl (Greenhouse, live): "Are you applying to this opportunity as a
// Northeastern Co-op?" asks for enrolment in a school's co-op program: No when
// no in-progress education is at that school. A school the profile names is
// left to the user.
func TestXC_ApplyingAsAnotherSchoolsCoop(t *testing.T) {
	mirakl := match.Posting{Company: "Mirakl", Location: "Boston, Massachusetts, United States"}
	const ask = "Are you applying to this opportunity as a Northeastern Co-op?"
	at := func(school, end string) match.Facts {
		f := xcBFacts()
		f.Education = []match.EducationFact{{School: school, Degree: "Master of Science", Field: "Computer Science", Start: "2029-09", End: end}}
		return f
	}
	xcBRunCases(t, []xcBCase{
		{name: "graduated elsewhere", facts: xcBFacts(), posting: mirakl, q: xcBYesNo(ask), label: "No"},
		{name: "enrolled elsewhere", facts: at("Example State University", "2032-05"), posting: mirakl, q: xcBYesNo(ask), label: "No"},
		{name: "enrolled at the school", facts: at("Northeastern University", "2032-05"), posting: mirakl, q: xcBYesNo(ask), wantNone: true},
		{name: "an undated education", facts: at("Example State University", ""), posting: mirakl, q: xcBYesNo(ask), wantNone: true},
	})
}

// CloudZero (Ashby, live): "Work Model Preference. If a local CloudZero office
// is available to you, what is your preferred work model?" over Hybrid, "Open
// to either hybrid or fully remote, no preference" and "Prefer fully remote":
// a profile whose work modes hold every mode the no-preference option names
// takes it. A profile without one of them, or without work modes, leaves it.
func TestXC_NoPreferenceAmongTheRecordedWorkModes(t *testing.T) {
	cz := match.Posting{Company: "CloudZero", Location: "Boston, MA"}
	const ask = "Work Model Preference. If a local CloudZero office is available to you, what is your preferred work model?"
	const either = "Open to either hybrid or fully remote, no preference"
	modes := func(m ...string) match.Facts {
		f := xcBFacts()
		f.WorkModes = m
		return f
	}
	q := xcBQ(ask, match.KindRadioGroup, "Hybrid (in-office + remote flexibility)", either, "Prefer fully remote")
	xcBRunCases(t, []xcBCase{
		{name: "every mode", facts: modes("onsite", "hybrid", "remote"), posting: cz, q: q, label: either},
		{name: "hybrid and remote", facts: modes("hybrid", "remote"), posting: cz, q: q, label: either},
		{name: "no remote", facts: modes("onsite", "hybrid"), posting: cz, q: q, wantNone: true},
		{name: "no work modes", facts: xcBFacts(), posting: cz, q: q, wantNone: true},
	})
	// Agave (Ashby, queue192): "Which work arrangement are you looking for?"
	// over boxes whose "Flexible / open to any of the above" names its modes
	// through the other options.
	agave := match.Posting{Company: "Agave", Location: "San Francisco"}
	const anyAbove = "Flexible / open to any of the above"
	box := xcBQ("Which work arrangement are you looking for?", match.KindCheckbox, "Fully remote, open to travel", "In person (5 days per week in San Francisco office)", anyAbove)
	box.Required = true
	xcBRunCases(t, []xcBCase{
		{name: "any of the above, every mode", facts: modes("onsite", "hybrid", "remote"), posting: agave, q: box, label: anyAbove},
		{name: "any of the above, no onsite", facts: modes("hybrid", "remote"), posting: agave, q: box, wantNone: true},
	})
}

// Hightouch (Ashby, queue147): "Are you currently authorized to work in the
// country where this role is located, for any employer?" on a "Remote (North
// America)" posting. A remote role is worked from where the applicant lives,
// so a region that holds the recorded address's country is that country,
// answered for review. Offices in several countries that include the one
// recorded country are that country, for review (owner decision); a region
// without the home country, and a home country not recorded as authorized,
// are not.
func TestXC_RemoteRegionPostingIsTheHomeCountry(t *testing.T) {
	const ask = "Are you currently authorized to work in the country where this role is located, for any employer?"
	us := xcBFacts()
	us.Address = match.AddressFact{City: "Charleston", Region: "SC", Country: "US"}
	ca := xcBFacts()
	ca.Address = match.AddressFact{City: "Toronto", Region: "ON", Country: "CA"}
	post := func(loc string) match.Posting { return match.Posting{Company: "Hightouch", Location: loc} }
	auth := map[string]xcBSet{"work-auth-us-any-employer": xcBAns("Yes")}
	xcBRunCases(t, []xcBCase{
		{sets: auth, name: "remote North America, home US", facts: us, posting: post("Remote (North America)"), q: xcBYesNo(ask), label: "Yes"},
		{sets: auth, name: "remote US or Canada, home US", facts: us, posting: post("Remote - US or Canada"), q: xcBYesNo(ask), label: "Yes"},
		// Oneleet (Ashby, live): a country beside a region.
		{sets: auth, name: "remote US or the EU, home US", facts: us, posting: post("US/EU - Remote"), q: xcBYesNo(ask), label: "Yes"},
		{sets: auth, name: "offices in two countries", facts: us, posting: post("New York, NY; Toronto, ON"), q: xcBYesNo(ask), label: "Yes"},
		{sets: auth, name: "remote EMEA, home US", facts: us, posting: post("Remote (EMEA)"), q: xcBYesNo(ask), wantNone: true},
		{sets: auth, name: "remote North America, home Canada", facts: ca, posting: post("Remote (North America)"), q: xcBYesNo(ask), wantNone: true},
		// CircleCI (Greenhouse, live): "the country(s) where the job is posted" over remote countries that include the US.
		{sets: map[string]xcBSet{"work-auth-us": xcBAns("Yes")}, name: "the country(s) where the job is posted", facts: us,
			posting: post("Canada (Remote), United States (Remote), United Kingdom (Remote) Mexico City, Mexico - Hybrid"),
			q:       xcBYesNo("Are you currently eligible to work in the country(s) where the job is posted?"), label: "Yes"},
	})
}

// Sentra (Ashby, live): "Are you currently residing and authorized to work in
// the United States?" asks two facts; Yes when the recorded address is in the
// country and the authorization is Yes, else it is left. CodeRabbit (Ashby,
// live): "If you require employer sponsorship ..., please explain your current
// work authorization status" asks the status.
func TestXC_ResidingAndAuthorizedAndTheStatusExplained(t *testing.T) {
	us := xcBFacts()
	us.Address = match.AddressFact{City: "Charleston", Region: "SC", Country: "US"}
	ca := xcBFacts()
	ca.Address = match.AddressFact{City: "Toronto", Region: "ON", Country: "CA"}
	sentra := match.Posting{Company: "Sentra", Location: "San Francisco / Bay Area"}
	yes := "Yes, I am currently residing and legally authorized to work in the United States"
	q := xcBQ("Are you currently residing and authorized to work in the United States?", match.KindRadioGroup, yes,
		"No, but I am elibile for a work visa if sponsored (for example TN visa)", "No, and I am not eligble for visa sponsorship")
	auth := map[string]xcBSet{"work-auth-us": xcBAns("Yes")}
	xcBRunCases(t, []xcBCase{
		{name: "residing in the US and authorized", sets: auth, facts: us, posting: sentra, q: q, entry: "work-auth-us", label: yes},
		{name: "residing elsewhere", sets: auth, facts: ca, posting: sentra, q: q, wantNone: true},
		{name: "the status explained", sets: map[string]xcBSet{"current-immigration-status": xcBPref("F-1 OPT")}, facts: us, posting: match.Posting{Company: "CodeRabbit", Location: "San Francisco"},
			q:     xcBText("If you require employer sponsorship (H-1B, etc.) to legally work in the U.S., please explain your current work authorization status"),
			entry: "current-immigration-status", answer: "F-1 OPT"},
	})
}

// Base Power (Ashby): ten statements of status under "Are you legally
// authorized to work in the U.S.?"; the recorded F-1 OPT with the STEM
// extension is the STEM OPT statement, without it the plain OPT one.
func TestXC_StatusStatementsTellStemOptFromOpt(t *testing.T) {
	const opt, stem = "I currently hold OPT and will require H-1B sponsorship in the future", "I currently hold STEM OPT and will require H-1B sponsorship in the future"
	q := xcBQ("Are you legally authorized to work in the U.S.?", match.KindCustomListbox,
		"I am a U.S. citizen or permanent resident (Green Card holder)", "I currently hold an H-1B visa and would need a transfer to Base",
		"I hold a TN visa and can work for Base with a new employer petition, or I am a Canadian Citizen eligible for a TN1A visa under the USCMA",
		"I am an Australian citizen and eligible for an E-3 visa", opt, stem,
		"I am authorized to work in the U.S. under CPT (Curricular Practical Training) for the duration of my academic program",
		"I require initial H-1B sponsorship to work for Base now", "I require another type of visa sponsorship to work for Base now",
		"I am not currently authorized to work in the U.S.")
	q.InputType = match.InputSingleSelect
	status := xcBPref("I am authorized to work in this country for any employer", "authorized to work in the united states for any employer", "for any employer")
	with := func(ext string) map[string]xcBSet {
		return map[string]xcBSet{"work-auth-us": xcBAns("Yes"), "us-work-authorization-status": status, "stem-opt-24-month-extension": xcBAns(ext)}
	}
	base := match.Posting{Company: "Base Power Company", Location: "Austin, TX (On-site)"}
	xcBRunCases(t, []xcBCase{
		{name: "with the STEM extension", sets: with("Yes"), facts: xcBFacts(), posting: base, q: q, entry: "us-work-authorization-status", label: stem},
		{name: "without it", sets: with("No"), facts: xcBFacts(), posting: base, q: q, entry: "us-work-authorization-status", label: opt},
		{name: "the extension unrecorded", sets: map[string]xcBSet{"work-auth-us": xcBAns("Yes"), "us-work-authorization-status": status}, facts: xcBFacts(), posting: base, q: q, wantNone: true},
	})
}

// TRM Labs (Ashby, queue148): a long statement over one option that
// acknowledges it; the statement's "a truly global business" took the work
// country by a resemblance. A lone acknowledging option is the
// acknowledgement. The agreement not to use AI interview tools stays the
// owner's, as every question that forbids AI does.
func TestXC_LoneAcknowledgingOptionUnderALongStatement(t *testing.T) {
	trm := match.Posting{Company: "TRM Labs", Location: "San Francisco, California"}
	one := func(text, opt string) match.Question {
		q := match.Question{Selector: "q", Text: text, Kind: match.KindCustomListbox, InputType: match.InputSingleSelect, Required: true, Options: xcBOpts(opt)}
		return q
	}
	const hpe = "High Performance Environment: TRM is a high intensity, high ownership company. We move with urgency, solve hard problems, and maintain exceptionally high standards. The work is demanding, the pace is relentless, and we expect our team to consistently lean in, push through challenges, and rise to the occasion when the stakes are highest. We're also a truly global business. Our teammates, customers,"
	const hpeOpt = "I have read and understand the expectations of working at TRM. I understand this is a high intensity, high ownership environment, and I wish to be considered for a role with these expectations."
	xcBRunCases(t, []xcBCase{
		{name: "the expectations acknowledged", facts: xcBFacts(), posting: trm, legal: true, q: one(hpe, hpeOpt), entry: "mandatory-acknowledge", label: hpeOpt},
		{name: "no AI interview tools", facts: xcBFacts(), posting: trm, legal: true,
			q: one("By submitting your application, you are agreeing not to use AI interview tools during the TRM interview process unless explicitly permitted in the interview materials or by a TRM interviewer.", "I agree"), wantNone: true},
	})
}

// TRM Labs (Ashby, queue148): "your current country of employment" is where
// the applicant works now, the recorded address's country, answered from the
// recorded authorization; a past remote or hybrid job is no acceptance of
// this office's attendance.
func TestXC_CurrentCountryOfEmploymentAndAPastHybridJob(t *testing.T) {
	trm := match.Posting{Company: "TRM Labs", Location: "San Francisco, California"}
	us := xcBFacts()
	us.Address = match.AddressFact{City: "Charleston", Region: "SC", Country: "US"}
	ca := xcBFacts()
	ca.Address = match.AddressFact{City: "Toronto", Region: "ON", Country: "CA"}
	const ask = "Are you legally authorized to work in your current country of employment?"
	auth := map[string]xcBSet{"work-auth-us": xcBAns("Yes")}
	xcBRunCases(t, []xcBCase{
		{name: "works in the US", sets: auth, facts: us, posting: trm, q: xcBYesNo(ask), entry: "work-auth-us", label: "Yes"},
		{name: "works in Canada", sets: auth, facts: ca, posting: trm, q: xcBYesNo(ask), wantNone: true},
		{name: "a past hybrid job", sets: map[string]xcBSet{"office-attendance-ok": xcBAns("Yes")}, facts: us, posting: trm,
			q: xcBQ("Have you previously worked in a remote or hybrid environment?", match.KindCustomListbox, "Yes, fully remote", "Yes, hybrid", "No"), notEntry: "office-attendance-ok", wantNone: true},
	})
}

// Binti (Ashby, queue149): "Which of the following do you have experience
// working with directly?" over Ruby, React and GraphQL. React is on the
// résumé's skills line, not among the profile's skills; the list ticks what
// the user's information names whole, and a one- or two-letter name only from
// the profile's skills.
func TestXC_ChecklistTicksWhatTheResumeNames(t *testing.T) {
	f := xcBFacts()
	f.Evidence = []string{"Java, JavaScript, TypeScript, Python, C/C++, SQL, Lua, R, MATLAB, Shell, HTML/CSS, React, Next.js, Vite, ElementUI", "Built a tool that lets teams go faster"}
	binti := match.Posting{Company: "Binti", Location: "San Francisco, CA"}
	const ask = "Which of the following do you have experience working with directly?"
	xcBRunCases(t, []xcBCase{
		{name: "React from the résumé", facts: f, posting: binti, q: xcBQ(ask, match.KindCheckbox, "Ruby", "React", "GraphQL"), entry: "skill-checklist", answer: "React"},
		{name: "a short name from prose", facts: f, posting: binti, q: xcBQ(ask, match.KindCheckbox, "Go", "Rust", "Elixir"), wantNone: true},
		{name: "nothing named", facts: f, posting: binti, q: xcBQ(ask, match.KindCheckbox, "Ruby", "Elixir", "GraphQL"), wantNone: true},
	})
	// A hit that does not depend on the option ticks none: notes the knowledge
	// base ranks near the question, or initials the question itself spells.
	near := xcBFacts()
	near.Evidence, near.KnowledgeNear = f.Evidence, map[string][]string{ask: {"notes/stack.md"}}
	spelt := xcBFacts()
	spelt.Evidence = append([]string{"AWS (EC2, S3)"}, f.Evidence...)
	const askSpelt = "Which of the following Amazon Web Services alternatives do you have experience working with directly?"
	xcBRunCases(t, []xcBCase{
		{name: "related notes tick only what is named", facts: near, posting: binti, q: xcBQ(ask, match.KindCheckbox, "Ruby", "React", "GraphQL"), entry: "skill-checklist", answer: "React"},
		{name: "related notes name nothing", facts: near, posting: binti, q: xcBQ(ask, match.KindCheckbox, "Ruby", "Elixir", "GraphQL"), wantNone: true},
		{name: "initials the question spells", facts: spelt, posting: binti, q: xcBQ(askSpelt, match.KindCheckbox, "Ruby", "Elixir", "GraphQL"), wantNone: true},
	})
}

// Owner decisions: English is Fluent; on-call and a W-2 arrangement are Yes;
// no Security+ certification is held; the government-employment history is
// the bank's No; the job search timeline and the working hours are the
// owner's own text.
func TestXC_OwnerAnswersForLanguageOnCallW2AndCertification(t *testing.T) {
	long := func(text string) match.Question {
		return match.Question{Selector: "s", Text: text, Kind: match.KindText, InputType: match.InputLongText}
	}
	sets := map[string]xcBSet{
		"language-proficiency": xcBLang("English", "Fluent", "Fluent"), "on-call-ok": xcBAns("Yes"),
		"w2-only-acceptable": xcBAns("Yes"), "security-plus-certification": xcBAns("No"),
		"government-employee-history": xcBAns("No"),
		"job-search-timeline":         xcBAns("Owner timeline text."), "working-hours": xcBAns("Owner hours text."),
	}
	xcBRunCases(t, []xcBCase{
		{name: "SecurityScorecard English at work", sets: sets, facts: xcBFacts(),
			q: xcBYesNo("Are you able to communicate effectively in English, both verbally and in writing, in a professional work environment?"), entry: "language-proficiency", label: "Yes"},
		{name: "Dash0 fluent in English", sets: sets, facts: xcBFacts(),
			q: xcBYesNo("Are you fluent in English?"), entry: "language-proficiency", label: "Yes"},
		{name: "Splice on-call rotation", sets: sets, facts: xcBFacts(),
			q: xcBYesNo("Are you comfortable participating in an on-call rotation and troubleshooting production data pipeline failures?"), entry: "on-call-ok", label: "Yes"},
		{name: "TRM on-call requirements", sets: sets, facts: xcBFacts(),
			q: xcBYesNo("On-Call Requirements: Are you willing and able to participate in this job's on-call requirements? (please refer to the job description's on-call requirement section)"), entry: "on-call-ok", label: "Yes"},
		{name: "Khan Academy W-2 through a payroll agency", sets: sets, facts: xcBFacts(),
			q: xcBYesNo("This role is a W-2 position employed through a payroll agency, rather than a 1099 independent contractor arrangement. Are you comfortable with this employment arrangement?"), entry: "w2-only-acceptable", label: "Yes"},
		{name: "Raft Security+", sets: sets, facts: xcBFacts(),
			q: xcBYesNo("Do you have an active Security+ certification?"), entry: "security-plus-certification", label: "No"},
		{name: "Snowflake government or publicly funded employer", sets: sets, facts: xcBFacts(),
			q:     xcBYesNo("Have you ever been directly employed by (1) any government or military entity, state-owned enterprise, or publicly-funded institution, or (2) a government contractor in a role that recommended Snowflake as part of Government procurement?"),
			entry: "government-employee-history", label: "No"},
		{name: "TRM job search timeline", sets: sets, facts: xcBFacts(),
			q: long("What is your current job search timeline? Are there any deadlines or competing processes we should be aware of?"), entry: "job-search-timeline", answer: "Owner timeline text."},
		{name: "TRM working hours and time zone", sets: sets, facts: xcBFacts(),
			q: long("We are a distributed company. What are your preferred working hours, and what hours are you willing to work? Please include your time zone."), entry: "working-hours", answer: "Owner hours text."},
		{name: "an on-call question about pay is not the willingness", sets: sets, facts: xcBFacts(),
			q: long("What on-call compensation do you expect?"), notEntry: "on-call-ok", wantNone: true},
		{name: "a bare time zone picker stays the time zone", sets: sets, facts: xcBFacts(),
			q: xcBQ("Which time zone are you in?", match.KindSelect, "Eastern", "Pacific"), notEntry: "working-hours", wantNone: true},
		{name: "competing offers stay competing offers", sets: sets, facts: xcBFacts(),
			q: xcBYesNo("Do you have any competing offer deadlines?"), notEntry: "job-search-timeline", wantNone: true},
	})
}

// Pivotal (Lever): "What is your target start date?" in a free-text box asks
// for a date, which a past stored date makes thirty days from today. Sweetgreen (Greenhouse):
// "Are you 18 years of age or older?" over two age statements takes the one
// that holds the applicant's age.
func TestXC_TargetStartDateBoxAndAnAgeStatementList(t *testing.T) {
	sets := map[string]xcBSet{"earliest-start-date": xcBAns("2020-01-02")}
	born := xcBFacts()
	born.DateOfBirth = "1990-04-12"
	young := xcBFacts()
	young.DateOfBirth = "2015-04-12"
	ages := xcBQ("All employees must be 18 years of age or older. Are you 18 years of age or older?", match.KindSelect, "I am 18 years of age or older", "I am 17 years of age or younger")
	xcBRunCases(t, []xcBCase{
		{name: "target start date in a text box", sets: sets, facts: xcBFacts(),
			q:     match.Question{Selector: "s", Text: "What is your target start date?", Kind: match.KindText, InputType: match.InputLongText},
			entry: "earliest-start-date", answer: "2031-08-03"},
		{name: "18 or older over two statements", sets: sets, facts: born, q: ages, entry: "age-minimum-threshold", label: "I am 18 years of age or older"},
		{name: "a minor takes the younger statement", sets: sets, facts: young, q: ages, entry: "age-minimum-threshold", label: "I am 17 years of age or younger"},
		{name: "statements that are no ages are left", sets: sets, facts: born,
			q: xcBQ("Are you 18 years of age or older?", match.KindSelect, "I meet the age requirement", "I do not"), wantNone: true},
	})
}

// SurveyMonkey (Ashby, queue154): Ashby's location typeahead asks "In which
// city are you located?", and the privacy notice asks for an acceptance "by
// selecting ' Yes ' below".
func TestXC_CityYouAreLocatedInAndAcceptanceBySelectingYes(t *testing.T) {
	facts := xcBFacts()
	facts.Address = match.AddressFact{City: "Charleston", Region: "SC", Country: "US"}
	sm := match.Posting{Company: "SurveyMonkey", Location: "San Mateo, CA"}
	ack := xcBYesNo("Before submitting your application please review the below: For more information on how SurveyMonkey (including its subsidiary and affiliated companies) processes your personal data as a job candidate or applicant, please see our Global Applicant and Candidate Data Privacy Notice . Please review this and acknowledge your acceptance of its content by selecting ' Yes ' below. Should you have any queries in relation to this, please contact . Do not send your resume/CV/application to this email address.")
	ack.Required = true
	xcBRunCases(t, []xcBCase{
		{name: "the city typeahead", sets: map[string]xcBSet{"address-city": xcBAns("Charleston")}, facts: facts, posting: sm,
			q: xcBQ("In which city are you located?", match.KindCustomListbox), entry: "address-city", answer: "Charleston, SC, United States"},
		{name: "the acceptance", sets: map[string]xcBSet{}, facts: facts, posting: sm, legal: true, q: ack, entry: "privacy-policy-consent", label: "Yes"},
	})
	// Datadog (Greenhouse, queue161): a closing "I understand ..." statement
	// asked over a Yes/No menu.
	dd := xcBQ("I understand my application will be processed in accordance with Datadog’s Candidate Privacy Policy.", match.KindCustomListbox, "Yes", "No")
	dd.Required = true
	// Suno (Ashby, queue162): an in-office policy closing with "Please confirm
	// that you have read and understand ...", over Yes/No buttons; the
	// employer's own accommodation offer for disabilities asks nothing of the
	// applicant.
	suno := xcBYesNo("Suno fosters an in-office culture with reasonable flexibility. We expect teammates to work from one of our offices Monday through Friday because we believe frequent in-person collaboration helps us move faster, make better decisions, and build the best possible products. While we recognize there are occasions where life gets in the way and reasonable flexibility is appropriate, being together in person is a core part of how we work. Suno reserves the right to update these expectations as needed. We will consider requests for accommodation to this policy and, upon request, will work with employees to explore reasonable accommodations for physical or mental disabilities or for other reasons recognized by applicable law. Please confirm that you have read and understand Suno's in-office expectations and policy.")
	suno.Required = true
	xcBRunCases(t, []xcBCase{{name: "an I-understand statement", sets: map[string]xcBSet{}, facts: facts,
		posting: match.Posting{Company: "Datadog", Location: "New York, NY"}, legal: true, q: dd, entry: "privacy-policy-consent", label: "Yes"},
		{name: "a request to confirm reading and understanding", sets: map[string]xcBSet{}, facts: facts,
			posting: match.Posting{Company: "Suno", Location: "New York, NY"}, legal: true, q: suno, entry: "mandatory-acknowledge", label: "Yes"}})
}

// Motional (Greenhouse, queue154/155): "Current / Most Recent Employer" over a
// list of autonomous-vehicle companies with "Other" before its last name. The
// recorded employer is on none of them, so the list's own gap choice is the
// answer; a short list without "Other" still abstains.
func TestXC_EmployerAbsentFromAnOpenListTakesOther(t *testing.T) {
	mot := match.Posting{Company: "Motional", Location: "Las Vegas, Nevada, United States"}
	sets := map[string]xcBSet{"current-company": xcBAns("Northwind Labs")}
	long := xcBQ("Current / Most Recent Employer", match.KindCustomListbox, "Amazon", "Apple", "Aptiv", "Argo AI", "Audi", "Aurora", "Bosch", "Boston Dynamics",
		"Cruise", "Google", "Lyft", "NVIDIA", "Tesla", "Waymo", "Zoox", "Other", "Latitude AI")
	long.Required = true
	short := xcBQ("Current / Most Recent Employer", match.KindSelect, "Amazon", "Apple", "Aptiv")
	short.Required = true
	// The live control is a multi-value listbox ("question_...[]") that asks for one.
	multi := long
	multi.InputType = match.InputMultiSelect
	xcBRunCases(t, []xcBCase{
		{name: "the list's gap choice", sets: sets, facts: xcBFacts(), posting: mot, q: long, entry: "current-company", label: "Other"},
		{name: "the gap choice in a multi-value listbox", sets: sets, facts: xcBFacts(), posting: mot, q: multi, entry: "current-company", label: "Other"},
		{name: "a short list without one", sets: sets, facts: xcBFacts(), posting: mot, q: short, wantNone: true},
	})
}

// Everlywell (Lever, queue154): "Are you comfortable supporting executives
// and handling urgent IT requests?" asks about the role's own duties; the
// owner's word is a default Yes. Pay, place, schedule and
// physical demands are other questions.
func TestXC_ComfortableWithTheRolesDuties(t *testing.T) {
	ev := match.Posting{Company: "Everlywell", Location: "Austin, TX"}
	sets := map[string]xcBSet{"comfortable-with-role-duties": xcBAns("Yes")}
	xcBRunCases(t, []xcBCase{
		{name: "the duties", sets: sets, facts: xcBFacts(), posting: ev,
			q: xcBYesNo("Are you comfortable supporting executives and handling urgent IT requests?"), entry: "comfortable-with-role-duties", label: "Yes"},
		{name: "customer calls", sets: sets, facts: xcBFacts(), posting: ev,
			q: xcBYesNo("Would you be comfortable working directly with customers on technical issues?"), entry: "comfortable-with-role-duties", label: "Yes"},
		{name: "lifting is a physical demand", sets: sets, facts: xcBFacts(), posting: ev,
			q: xcBYesNo("Are you comfortable lifting and handling equipment up to 50 lbs?"), wantNone: true},
		{name: "weekend shifts are a schedule", sets: sets, facts: xcBFacts(), posting: ev,
			q: xcBYesNo("Are you comfortable handling urgent requests on weekends and holidays?"), wantNone: true},
	})
}

// Squarespace (Greenhouse, queue164): after "What is the name of your current
// or most recent employer?", "Are you currently still employed with this
// company?" asks whether that job goes on. "Still" presupposes the job, so
// "this company" is the applicant's employer, not the hiring company, and the
// recorded history answers it. Without "still" the question keeps its
// hiring-company reading.
func TestXC_StillEmployedWithThisCompanyReadsTheMostRecentJob(t *testing.T) {
	current := xcBFacts()
	current.Employment = []match.EmploymentFact{{Employer: "Example Corp", Title: "Software Engineer", Start: "2026-06", End: "present"}}
	ended := xcBFacts()
	ended.Employment = []match.EmploymentFact{{Employer: "Example Corp", Title: "Software Engineer", Start: "2024-06", End: "2026-05"}}
	// The live profile attests its employment list complete, which lets the
	// relationship lane answer No for a company the list lacks.
	attested := current
	attested.RelationshipsComplete = map[match.RelationshipFamily]string{match.FamilyEmployment: "2026-11-01"}
	sq := match.Posting{Company: "Squarespace", Location: "New York, NY"}
	still := xcBQ("Are you currently still employed with this company?", match.KindCustomListbox, "Yes", "No")
	xcBRunCases(t, []xcBCase{
		{name: "a job that goes on", sets: map[string]xcBSet{}, facts: current, posting: sq, q: still, label: "Yes", notEntry: "currently-employed-by-this-company"},
		{name: "an attested employment list", sets: map[string]xcBSet{}, facts: attested, posting: sq, q: still, label: "Yes"},
		{name: "a job that ended", sets: map[string]xcBSet{}, facts: ended, posting: sq, q: still, label: "No", notEntry: "currently-employed-by-this-company"},
		{name: "no history", sets: map[string]xcBSet{}, facts: xcBFacts(), posting: sq, q: still, wantNone: true, notEntry: "currently-employed-by-this-company"},
		{name: "the hiring company", sets: map[string]xcBSet{}, facts: current, posting: sq,
			q: xcBQ("Are you currently employed by this company?", match.KindCustomListbox, "Yes", "No"), entry: "currently-employed-by-this-company", label: "No"},
	})
}
