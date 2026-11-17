package match_test

import (
	"testing"

	"applysmith/internal/answerbank/match"
)

func xcBFacts() match.Facts {
	y := func(v float64) *float64 { return &v }
	return match.Facts{
		Education: []match.EducationFact{{School: "Example State University", Degree: "Master of Science", Field: "Computer Science", Start: "2024-09", End: "2026-05"}},
		Skills: []match.SkillFact{
			{Name: "Python", Years: y(3), Level: "advanced"},
			{Name: "LLMs", Years: y(3), Level: "intermediate"},
		},
		AuthorizedCountries:       []string{"US"},
		DateOfBirth:               "2000-06-15",
		VisaStatus:                "F-1 OPT",
		RequiresSponsorshipNow:    xcBBool(false),
		RequiresSponsorshipFuture: xcBBool(true),
	}
}

// xcBNeeds is xcBFacts with the two sponsorship needs set.
func xcBNeeds(now, later bool) match.Facts {
	f := xcBFacts()
	f.RequiresSponsorshipNow, f.RequiresSponsorshipFuture = xcBBool(now), xcBBool(later)
	return f
}

func TestXC_Derived(t *testing.T) {
	startPast := map[string]xcBSet{"earliest-start-date": xcBAns("2031-06-20"), "available-full-time": xcBAns("Yes")}
	sponsor := map[string]xcBSet{"sponsorship-now-or-future": xcBAns("Yes")}
	threeWay := map[string]xcBSet{"work-auth-and-future-sponsorship": xcBPref("x"), "sponsorship-now-or-future": xcBAns("Yes")}
	livesInUS := xcBFacts()
	livesInUS.Address = match.AddressFact{City: "Charleston", Region: "SC", Country: "US"}
	xcBRunCases(t, []xcBCase{
		// XC-028: age is derived from the stored date of birth.
		{name: "XC-028 gate 18 plus", sets: map[string]xcBSet{"age-18": xcBAns("Yes")}, facts: xcBFacts(),
			q: xcBYesNo("Are you 18 years of age or older?"), label: "Yes"},
		// The owner's rule: proof of age is the legal working age (AmEx, live).
		{name: "XC-028 proof of age", sets: map[string]xcBSet{"age-18": xcBAns("Yes")}, facts: xcBFacts(),
			q: xcBYesNo("If hired, can you furnish proof of age?"), entry: "age-18", label: "Yes"},
		// The owner's rule: verification of identity is the identity the profile names (Visa, live).
		{name: "XC-028 verification of identity upon hire", sets: map[string]xcBSet{"identity-verification": xcBAns("Yes")}, facts: xcBFacts(),
			q: xcBYesNo("Can you provide verification of your identity upon hire?"), entry: "identity-verification", label: "Yes"},
		{name: "XC-028 bracket select", sets: map[string]xcBSet{"current-age-bracket": xcBTypes("single_select")}, facts: xcBFacts(),
			q:     xcBQ("What is your age range?", match.KindSelect, "Under 18", "18-24", "30 - 34", "65+"),
			entry: "current-age-bracket", label: "30 - 34"},
		{name: "XC-028 age text box", sets: map[string]xcBSet{"current-age-bracket": xcBTypes("single_select", "text", "number")}, facts: xcBFacts(),
			q: xcBText("What is your age?"), entry: "current-age-bracket", answer: "31"},
		{name: "XC-028 missing date of birth abstains", facts: match.Facts{}, q: xcBYesNo("Are you at least 21 years of age?"), wantNone: true},
		{name: "XC-028 hours are not an age", facts: xcBFacts(), q: xcBYesNo("Are you able to work at least 40 hours per week?"), wantNone: true},

		// XC-029: start availability comes from the earliest start date.
		{name: "XC-029 when picks a term option", sets: startPast, facts: xcBFacts(),
			q:     xcBQ("When will you be available to work full-time?", match.KindSelect, "Available to Start Immediately", "Summer 2031", "Fall 2031"),
			entry: "full-time-start-availability", label: "Summer 2031"},
		{name: "XC-029 whether keeps its own entry", sets: startPast, facts: xcBFacts(),
			q: xcBYesNo("Are you available to work full-time?"), entry: "available-full-time", label: "Yes"},
		{name: "XC-029 typed start date is never in the past", sets: startPast, facts: xcBFacts(),
			q: xcBText("When can you start?"), answer: "2031-08-03"},

		// XC-030: sponsorship from the recorded facts across the phrasing family.
		{name: "XC-030 visa or permit", sets: sponsor, facts: xcBFacts(),
			q: xcBYesNo("Do you require a visa or permit to work in the country for which you are applying?"), posting: match.Posting{Location: "Austin, TX"},
			entry: "sponsorship-now-or-future", label: "Yes"},
		{name: "XC-030 employer-sponsored authorization", sets: sponsor, facts: xcBFacts(),
			q:     xcBYesNo("Will you require employer-sponsored work authorization?"),
			entry: "sponsorship-now-or-future", label: "Yes"},
		{name: "XC-030 employer support to obtain or maintain", sets: sponsor, facts: xcBFacts(),
			q:     xcBYesNo("Will you require employer support to obtain or maintain authorization to work?"),
			entry: "sponsorship-now-or-future", label: "Yes"},
		{name: "XC-030 immigration support", sets: sponsor, facts: xcBFacts(),
			q:     xcBYesNo("Do you require Immigration Support to maintain work authorization?"),
			entry: "sponsorship-now-or-future", label: "Yes"},
		// A transfer is a different fact from sponsorship; the profile records none.
		{name: "XC-030 H-1B transfer", sets: sponsor, facts: xcBFacts(),
			q:     xcBYesNo("Will you require H-1B transfer sponsorship?"),
			entry: "sponsorship-now-or-future", wantNone: true, reason: match.ReasonAmbiguous},
		{name: "XC-030 bare visa support", sets: sponsor, facts: xcBFacts(),
			q:     xcBYesNo("Visa Support"),
			entry: "sponsorship-now-or-future", label: "Yes"},
		{name: "XC-030 negated form inverts", sets: sponsor, facts: xcBFacts(),
			q:     xcBYesNo("Are you able to work in the United States without requiring visa sponsorship?"),
			entry: "sponsorship-now-or-future", label: "No"},
		// Holding a permit is a different fact from being authorized (a citizen holds
		// none), so the closed work-authorization reading leaves it to the user.
		{name: "XC-030 holding a permit is the work authorization question", sets: map[string]xcBSet{"sponsorship-now-or-future": xcBAns("Yes"), "work-auth-us": xcBAns("Yes")}, facts: xcBFacts(),
			q: xcBYesNo("Do you have a valid work permit or approved visa for the country you are applying to?"), posting: match.Posting{Location: "Austin, TX"}, entry: "work-auth-us", wantNone: true, reason: match.ReasonAmbiguous},
		{name: "XC-030 relocation support is not sponsorship", sets: sponsor, facts: xcBFacts(),
			q: xcBYesNo("Will you require employer support to relocate?"), wantNone: true},
		{name: "XC-030 status picker is not sponsorship", sets: map[string]xcBSet{"current-immigration-status": xcBPref("F-1 OPT"), "sponsorship-now-or-future": xcBAns("Yes")}, facts: xcBFacts(),
			q:     xcBQ("Which best describes your current immigration status?", match.KindSelect, "U.S. Citizen", "Permanent Resident", "F-1 OPT", "H-1B"),
			entry: "current-immigration-status", label: "F-1 OPT"},

		// XC-031: three-option work-authorization and future-sponsorship pickers.
		{name: "XC-031 do require beats do not require", sets: threeWay, facts: xcBFacts(),
			q: xcBQ("Are you authorized to work lawfully in the United States and will you require future sponsorship?", match.KindSelect,
				"I am authorized to work lawfully in the United States and DO NOT require future sponsorship",
				"I am authorized to work lawfully in the United States and DO require future sponsorship",
				"I am NOT authorized to work in the United States"),
			entry: "work-auth-and-future-sponsorship", label: "I am authorized to work lawfully in the United States and DO require future sponsorship"},
		{name: "XC-031 sponsorship requirements heading", sets: threeWay, facts: xcBFacts(),
			q: xcBQ("Sponsorship Requirements", match.KindSelect,
				"I will not require sponsorship now or in the future", "I currently require sponsorship", "I will require sponsorship in the future"),
			entry: "work-auth-and-future-sponsorship", label: "I will require sponsorship in the future"},
		// NYISO (Greenhouse, live): the need now stated with no time beside the
		// need later; with no need recorded, the authorization without it.
		{name: "XC-031 a need stated with no time", sets: threeWay, facts: xcBFacts(),
			q: xcBQ("Do you currently have unrestricted work authorization for the United States, or would you require sponsorship for a working visa now or in the future?", match.KindSelect,
				"I have unrestricted work authorization and would NOT require sponsorship.", "I have work authorization now but would require sponsorship in the future.", "I would require sponsorship for a working visa."),
			entry: "work-auth-and-future-sponsorship", label: "I have work authorization now but would require sponsorship in the future."},
		{name: "XC-031 a need stated with no time, no need recorded", sets: threeWay, facts: xcBNeeds(false, false),
			q: xcBQ("Do you currently have unrestricted work authorization for the United States, or would you require sponsorship for a working visa now or in the future?", match.KindSelect,
				"I have unrestricted work authorization and would NOT require sponsorship.", "I have work authorization now but would require sponsorship in the future.", "I would require sponsorship for a working visa."),
			entry: "work-auth-and-future-sponsorship", label: "I have unrestricted work authorization and would NOT require sponsorship."},

		// XC-032: a Y / N letter pair is a Yes/No question.
		{name: "XC-032 letter pair", sets: sponsor, facts: xcBFacts(),
			q: xcBQ("Will you now or in the future require sponsorship?", match.KindRadioGroup, "Y", "N"), entry: "sponsorship-now-or-future", label: "Y"},
		{name: "XC-032 letter pair with a placeholder", sets: sponsor, facts: xcBFacts(),
			q: xcBQ("Will you now or in the future require sponsorship?", match.KindSelect, "Choose...", "Y", "N"), entry: "sponsorship-now-or-future", label: "Y"},
		{name: "XC-032 three letters is not a pair", sets: sponsor, facts: xcBFacts(),
			q: xcBQ("Will you now or in the future require sponsorship?", match.KindRadioGroup, "Y", "N", "M"), wantNone: true},
		{name: "XC-032 a misspelt sponsorship (Cohere)", sets: sponsor, facts: livesInUS,
			q:     xcBQ("Do you, now or in the future, require sponorship to work in the country you currently reside in?", match.KindRadioGroup, "Yes", "No"),
			entry: "sponsorship-now-or-future", label: "Yes"},

		// XC-033: nationality on a closed list.
		{name: "XC-033 demonym of the recorded citizenship", sets: map[string]xcBSet{"country-of-citizenship": xcBPref("Canada")}, facts: xcBFacts(),
			q:     xcBQ("Nationality", match.KindSelect, "American", "British", "Canadian", "Chinese", "Taiwanese"),
			entry: "country-of-citizenship", label: "Canadian"},
		{name: "XC-033 second nationality is not answered", sets: map[string]xcBSet{"country-of-citizenship": xcBPref("Canada")}, facts: xcBFacts(),
			q: xcBQ("Second nationality", match.KindSelect, "American", "British", "Canadian"), wantNone: true},
		{name: "XC-033 member countries question is not answered", sets: map[string]xcBSet{"country-of-citizenship": xcBPref("Canada")}, facts: xcBFacts(),
			q: xcBYesNo("Are you a national of one of our member countries?"), wantNone: true},

		// XC-034: export-control Yes/No with the country list in the text.
		{name: "XC-034 recorded country in the list", sets: map[string]xcBSet{"country-of-citizenship": xcBPref("Iran"), "citizen-cuba-iran-north-korea-syria": xcBAns("No")}, facts: xcBFacts(),
			q:     xcBYesNo("Are you a citizen of any of the following countries: Cuba, Iran, North Korea, Syria?"),
			entry: "country-of-citizenship", label: "Yes"},
		{name: "XC-034 list above", sets: map[string]xcBSet{"country-of-citizenship": xcBPref("Iran"), "citizen-cuba-iran-north-korea-syria": xcBAns("No")}, facts: xcBFacts(),
			q:     xcBYesNo("Export controls apply to the countries listed above: Cuba, Iran, North Korea, Syria. Are you a citizen of any of the countries listed above?"),
			entry: "country-of-citizenship", label: "Yes"},
		// The owner's decision on export control: a recorded citizenship the
		// list does not name answers No.
		{name: "XC-034 a recorded citizenship the list does not name", sets: map[string]xcBSet{"country-of-citizenship": xcBPref("Canada")}, facts: xcBFacts(),
			q: xcBYesNo("Are you a citizen of any of the following countries: Cuba, Iran, North Korea, Syria?"), label: "No"},
	})
}

// Reddit (Greenhouse, queue164): "Are you a veteran/have you served in the
// military?" over kinds of service and "No military service". The veteran
// entry's "I am not a Veteran" cannot bind it (a non-veteran may have served),
// but the user's own military-service-status "No, I have not served" does, and
// a list's no-service option is true of no veteran. A stance of service, or
// none, leaves the list alone.
func TestXC_NoMilitaryServiceFromTheNeverServedStance(t *testing.T) {
	q := xcBQ("Are you a veteran/have you served in the military?", match.KindCustomListbox,
		"Active Reserve", "Inactive Reserve", "Other Protected Veteran", "Retired", "Unspecified Veteran", "Vietnam Era Veteran",
		"Vietnam Veteran and Other Protected Veteran", "No military service", "I don't wish to answer")
	q.Required = true
	veteran := xcBPref("I am not a Veteran", "I am not a protected veteran")
	xcBRunCases(t, []xcBCase{
		{name: "never served", facts: xcBFacts(), q: q, label: "No military service",
			sets: map[string]xcBSet{"eeo-veteran": veteran, "military-service-status": xcBPref("No, I have not served", "I have not served")}},
		{name: "served", facts: xcBFacts(), q: q, wantNone: true,
			sets: map[string]xcBSet{"eeo-veteran": veteran, "military-service-status": xcBPref("Yes, I have served")}},
		{name: "no service stance", facts: xcBFacts(), q: q, wantNone: true,
			sets: map[string]xcBSet{"eeo-veteran": veteran}},
	})
}

// Skydio (Ashby, queue167): "Prior US Government Employment? Do you
// currently, or have you in the last 10 years, worked for the US government
// (e.g., Congressional staffer, member of military, state, or federal
// agencies)?" counts the military in the government, so it is No only when
// the government employment entry says No and the military-service entry says
// the user never served.
func TestXC_USGovernmentThatCountsTheMilitary(t *testing.T) {
	q := xcBQ("Prior US Government Employment? Do you currently, or have you in the last 10 years, worked for the US government (e.g., Congressional staffer, member of military, state, or federal agencies)?",
		match.KindRadioGroup, "Yes", "No")
	q.Required = true
	never := xcBPref("No, I have not served", "I have not served")
	xcBRunCases(t, []xcBCase{
		{name: "neither", facts: xcBFacts(), q: q, label: "No",
			sets: map[string]xcBSet{"government-employee-history": xcBAns("No"), "military-service-status": never}},
		{name: "served", facts: xcBFacts(), q: q, wantNone: true,
			sets: map[string]xcBSet{"government-employee-history": xcBAns("No"), "military-service-status": xcBPref("Yes, I have served")}},
		{name: "a government job", facts: xcBFacts(), q: q, wantNone: true,
			sets: map[string]xcBSet{"government-employee-history": xcBAns("Yes"), "military-service-status": never}},
		{name: "no military-service entry", facts: xcBFacts(), q: q, wantNone: true,
			sets: map[string]xcBSet{"government-employee-history": xcBAns("No")}},
	})
}

// Scale AI (Greenhouse, queue190) asks the same in text boxes: "Are you a
// current or former civilian or military employee of the United States
// Government? (If yes, ...)" and "Do you have any restrictions on
// post-government employment? (If yes, please describe.)". Both are "No" when
// the government employment and military-service entries both say none; a
// restriction the record cannot see otherwise stays the user's.
func TestXC_USGovernmentInATextBox(t *testing.T) {
	q := xcBText("Are you a current or former civilian or military employee of the United States Government? (If yes, when/where were you last employed and what was your highest grade/rank and title?)")
	q.Required = true
	post := xcBText("Do you have any restrictions on post-government employment? (If yes, please describe.)")
	post.Required = true
	never := xcBPref("No, I have not served", "I have not served")
	neither := map[string]xcBSet{"government-employee-history": xcBAns("No"), "military-service-status": never}
	xcBRunCases(t, []xcBCase{
		{name: "neither", facts: xcBFacts(), q: q, answer: "No", sets: neither},
		{name: "served", facts: xcBFacts(), q: q, wantNone: true,
			sets: map[string]xcBSet{"government-employee-history": xcBAns("No"), "military-service-status": xcBPref("Yes, I have served")}},
		{name: "post-government, neither", facts: xcBFacts(), q: post, answer: "No", sets: neither},
		{name: "post-government, a government job", facts: xcBFacts(), q: post, wantNone: true,
			sets: map[string]xcBSet{"government-employee-history": xcBAns("Yes"), "military-service-status": never}},
	})
}

// Enveritas (Greenhouse, queue164): "Are you able to work during our core
// hours (10 am to 2 pm United States Eastern Time), Monday through Friday?"
// takes Yes from the user's own working-hours entry when it says they overlap
// with the team's core hours in any U.S. time zone; an entry that says
// nothing of it, or core hours in no U.S. zone, leave it.
func TestXC_CoreHoursFromTheWorkingHoursEntry(t *testing.T) {
	const own = "Flexible. I am based in Charleston, SC, in Eastern Time (ET), and usually work 9 AM to 6 PM ET; I am happy to shift my hours to overlap with the team's core hours in any U.S. time zone."
	q := xcBQ("Are you able to work during our core hours (10 am to 2 pm United States Eastern Time), Monday through Friday? (This is a full time role, meaning that you would either start your day earlier or finish your day later as suits you.)",
		match.KindCustomListbox, "Yes", "No")
	q.Required = true
	abroad := xcBQ("Are you able to work during our core hours (9 am to 1 pm Central European Time)?", match.KindCustomListbox, "Yes", "No")
	xcBRunCases(t, []xcBCase{
		{name: "the overlap the entry states", sets: map[string]xcBSet{"working-hours": xcBAns(own)}, facts: xcBFacts(), q: q, label: "Yes"},
		{name: "an entry silent on core hours", sets: map[string]xcBSet{"working-hours": xcBAns("9 AM to 6 PM ET.")}, facts: xcBFacts(), q: q, wantNone: true},
		{name: "core hours in no U.S. zone", sets: map[string]xcBSet{"working-hours": xcBAns(own)}, facts: xcBFacts(), q: abroad, wantNone: true},
	})
}

// Reducto and Pogo (Ashby, queue191): "What institution(s) did you graduate
// from?" in a text box lists the schools whose education has ended, most
// recent first; a singular school takes the most recent ended one. An
// education still in progress is no school graduated from, and with none
// ended the box stays the user's.
func TestXC_TheSchoolsGraduatedFrom(t *testing.T) {
	now := match.Date{Year: 2026, Month: 11, Day: 7}
	both := xcBFacts()
	both.Education = []match.EducationFact{
		{School: "Carver University", Degree: "Master of Science", End: "2026-05"},
		{School: "University of Westbridge", Degree: "Bachelor of Engineering", End: "2024-06"},
	}
	enrolled := xcBFacts()
	enrolled.Education = []match.EducationFact{
		{School: "Carver University", Degree: "Master of Science", End: "2027-05"},
		{School: "University of Westbridge", Degree: "Bachelor of Engineering", End: "2024-06"},
	}
	studying := xcBFacts()
	studying.Education = []match.EducationFact{{School: "Carver University", Degree: "Master of Science", End: "2027-05"}}
	plural := xcBText("What institution(s) did you graduate from?")
	plural.Required = true
	single := xcBText("Which university did you graduate from?")
	xcBRunCases(t, []xcBCase{
		{name: "both ended", facts: both, today: now, q: plural, answer: "Carver University; University of Westbridge"},
		{name: "one in progress", facts: enrolled, today: now, q: plural, answer: "University of Westbridge"},
		{name: "singular", facts: both, today: now, q: single, answer: "Carver University"},
		{name: "none ended", facts: studying, today: now, q: plural, wantNone: true},
	})
}
