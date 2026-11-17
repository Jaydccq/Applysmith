package match_test

import (
	"slices"
	"testing"

	"applysmith/internal/answerbank/match"
)

func TestXC_RoutesCommitments(t *testing.T) {
	comfort := map[string]xcBSet{
		"practical-technical-assessment": xcBAns("Yes"), "sign-onboarding-documents": xcBAns("Yes"), "comfortable-with-posted-range": xcBAns("Yes"),
		"multi-year-commitment": xcBAns("Yes"), "office-attendance-ok": xcBAns("Yes"), "days-per-week-onsite": xcBPref("3"),
		"intend-to-work-in-posted-state": xcBAns("Yes"), "ai-screening-opt-out": xcBPref("No"), "drug-bg-check": xcBAns("Yes"),
		"address-state": xcBAns("Example State"), "salary-expectation": xcBWith(xcBComp("120000"), xcBTypes("text", "number")),
		"based-in-us-or-canada": xcBAns("Yes"),
	}
	number := func(text string) match.Question {
		return match.Question{Selector: "s", Text: text, Kind: match.KindText, InputType: match.InputNumber}
	}
	history := map[string]xcBSet{"previously-applied-to-company": xcBAns("No"), "previously-interviewed-company": xcBAns("No"),
		"prior-company-interaction": xcBAns("No"), "relatives-at-employer": xcBAns("No")}
	student := map[string]xcBSet{"currently-in-opt": xcBAns("Yes"), "stem-opt-24-month-extension": xcBAns("No"), "cpt-opt-work-authorization": xcBAns("Yes"),
		"stem-opt-extension-expiry": xcBAns("2032-05-31"), "sponsorship-now-or-future": xcBAns("Yes")}
	xcBRunCases(t, []xcBCase{
		// XC-053
		{name: "XC-053 practical assessment", sets: comfort, facts: xcBFacts(),
			q: xcBYesNo("Are you comfortable completing a practical technical assessment?"), entry: "practical-technical-assessment", label: "Yes"},
		{name: "XC-053 onboarding documents", sets: comfort, facts: xcBFacts(),
			q: xcBYesNo("Are you willing to sign the onboarding documents (MoU/NDA)?"), entry: "sign-onboarding-documents", label: "Yes"},
		{name: "XC-053 acceptable compensation", sets: comfort, facts: xcBFacts(),
			q: xcBYesNo("Is that level of compensation acceptable for you?"), entry: "comfortable-with-posted-range", label: "Yes"},
		// Trail (Ashby, live): asked in a text box, as the engine sends one.
		{name: "XC-053 reviewed the compensation band in a text box", sets: comfort, facts: xcBFacts(),
			q:     match.Question{Selector: "s", Text: "Can you confirm you've reviewed the compensation band and are comfortable proceeding?", Kind: match.KindText, InputType: match.InputText},
			entry: "comfortable-with-posted-range", answer: "Yes"},
		{name: "XC-053 a range to name is not the comfort Yes", sets: comfort, facts: xcBFacts(),
			q: match.Question{Selector: "s", Text: "What salary range are you comfortable with?", Kind: match.KindText, InputType: match.InputText}, notEntry: "comfortable-with-posted-range", wantNone: true},
		{name: "XC-053 expected compensation box is not stolen", sets: comfort, facts: xcBFacts(),
			q: number("Expected compensation"), entry: "salary-expectation", answer: "120000"},
		{name: "XC-053 minimum acceptable monthly salary box is not stolen", sets: comfort, facts: xcBFacts(),
			q: number("Min. Acceptable Monthly Salary"), notEntry: "comfortable-with-posted-range", wantNone: true},
		{name: "XC-053 multi-year commitment", sets: comfort, facts: xcBFacts(),
			q: xcBYesNo("Are you able to make a two year commitment?"), entry: "multi-year-commitment", label: "Yes"},
		{name: "XC-053 located on-site", sets: comfort, facts: xcBFacts(),
			q: xcBYesNo("This role is located on-site. Are you able to work from this location?"), entry: "office-attendance-ok", label: "Yes"},
		{name: "XC-053 fixed office hours", sets: comfort, facts: xcBFacts(),
			q: xcBYesNo("Are you able to work M-F 8-6 in the office?"), entry: "office-attendance-ok", label: "Yes"},
		{name: "XC-053 onsite setting", sets: comfort, facts: xcBFacts(),
			q: xcBYesNo("Are you comfortable in an onsite setting?"), entry: "office-attendance-ok", label: "Yes"},
		{name: "XC-053 onsite location picker is not stolen", sets: comfort, facts: xcBFacts(),
			q: xcBQ("Which onsite setting location would you prefer?", match.KindSelect, "Austin", "Boston"), notEntry: "office-attendance-ok", wantNone: true},
		{name: "XC-053 days per week onsite before attendance comfort", sets: comfort, facts: xcBFacts(),
			q:     xcBQ("How many days per week are you able to be onsite in the office?", match.KindSelect, "0", "1", "2", "3", "4", "5"),
			entry: "days-per-week-onsite", label: "3"},
		{name: "XC-053 intend to work in the posted state", sets: comfort, facts: xcBFacts(),
			q: xcBYesNo("Do you intend to work in the state where this job is posted?"), entry: "intend-to-work-in-posted-state", label: "Yes"},
		{name: "XC-053 AI screening opt out", sets: comfort, facts: xcBFacts(),
			q:     xcBQ("You can choose to opt out of AI-assisted screening of your application. Do you want to opt out?", match.KindRadioGroup, "Yes", "No"),
			entry: "ai-screening-opt-out", label: "No"},
		{name: "XC-053 drug test and background check", sets: comfort, facts: xcBFacts(),
			q: xcBYesNo("Are you willing to undergo a drug test and a background check?"), entry: "drug-bg-check", label: "Yes"},
		{name: "XC-053 consent to misspelled background checks on yes/no buttons", sets: comfort, facts: xcBFacts(),
			q: xcBYesNo("Auror offers employment based on satisfactory completion of relevant backgound checks. Do you consent to Auror completing there checks if you are the successful candidate?"), entry: "drug-bg-check", label: "Yes"},

		// XC-054
		{name: "XC-054 applied before", sets: history, facts: xcBFacts(),
			q: xcBYesNo("Have you ever applied for work at Example Corp?"), entry: "previously-applied-to-company", label: "No"},
		{name: "XC-054 interviewed before", sets: history, facts: xcBFacts(),
			q: xcBYesNo("Have you ever interviewed with Example Corp?"), entry: "previously-interviewed-company", label: "No"},
		{name: "XC-054 prior interaction", sets: history, facts: xcBFacts(),
			q:     xcBYesNo("Have you had any prior interaction with Example Corp, such as workshops, tours or engagement activities?"),
			entry: "prior-company-interaction", label: "No"},
		{name: "XC-054 engagement previously", sets: history, facts: xcBFacts(),
			q: xcBYesNo("Have you had any engagement with our organization previously?"), entry: "prior-company-interaction", label: "No"},
		// Hatch (Ashby, live): one No beside Yes options split by detail.
		{name: "XC-054 applied or interviewed, Yes split by detail", sets: history, facts: xcBFacts(),
			q: xcBQ("Have you applied to, or interviewed, with Yelp (including any subsidiaries) within the last 12 months?", match.KindRadioGroup,
				"No", "Yes – I am currently interviewing for another role here", "Yes – I interviewed here within the last 12 months", "Yes – I previously applied within the last 12 months"),
			entry: "previously-applied-to-company", label: "No"},
		{name: "XC-054 technology experience is not here", sets: history, facts: xcBFacts(),
			q: xcBYesNo("Do you have prior experience with Kubernetes?"), notEntry: "prior-company-interaction", wantNone: true},

		// XC-055
		{name: "XC-055 period of OPT", sets: student, facts: xcBFacts(),
			q: xcBYesNo("Are you currently in a period of Optional Practical Training (OPT)?"), entry: "currently-in-opt", label: "Yes"},
		{name: "XC-055 STEM extension", sets: student, facts: xcBFacts(),
			q:     xcBYesNo("Are you eligible for a 24-month OPT extension based on a US degree in STEM?"),
			entry: "stem-opt-24-month-extension", label: "No"},
		{name: "XC-055 student or temporary visa names OPT", sets: student, facts: xcBFacts(),
			q: xcBYesNo("Are you on a student or temporary visa (e.g. OPT, CPT)?"), entry: "cpt-opt-work-authorization", label: "Yes"},
		{name: "XC-055 temporary work visa is not the student entry", sets: student, facts: xcBFacts(),
			q: xcBYesNo("Do you currently hold a temporary work visa?"), notEntry: "cpt-opt-work-authorization", wantNone: true},
		{name: "XC-055 expiry wording", sets: student, facts: xcBFacts(),
			q: xcBText("What is the expiration date of your STEM OPT extension?"), entry: "stem-opt-extension-expiry"},
	})
}

// Alchemy (Ashby): "Are you able to conduct a coding and system design
// interview?"; Paxos (Ashby): consent to an identity check of a government ID
// and the location during the interviews. Owner decision: both Yes.
func TestXC_CodingInterviewAndInterviewIdentityCheck(t *testing.T) {
	sets := map[string]xcBSet{"interview-coding-system-design": xcBAns("Yes"), "interview-identity-verification": xcBAns("Yes")}
	xcBRunCases(t, []xcBCase{
		{name: "coding and system design", sets: sets, facts: xcBFacts(), q: xcBYesNo("Are you able to conduct a coding and system design interview?"), entry: "interview-coding-system-design", label: "Yes"},
		{name: "identity check", sets: sets, facts: xcBFacts(), q: xcBYesNo("Due to the sensitive nature of our work at Paxos, we may conduct identity verification during the interview process. This may include validating government-issued identification and confirming candidate location. Do you understand and consent to this process?"), entry: "interview-identity-verification", label: "Yes"},
		{name: "blank", facts: xcBFacts(), q: xcBYesNo("Are you able to conduct a coding and system design interview?"), entry: "interview-coding-system-design", wantNone: true},
	})
}

// Wayve (Ashby): "Notice Period (months)" takes the recorded notice in the
// unit the box names; a lead time the unit does not divide stays as recorded.
func TestXC_NoticePeriodInTheUnitTheBoxNames(t *testing.T) {
	month := map[string]xcBSet{"availability-notice-period": xcBPref("in 1 Month")}
	xcBRunCases(t, []xcBCase{
		{name: "months", sets: month, facts: xcBFacts(), q: xcBText("Notice Period (months)"), entry: "availability-notice-period", answer: "1"},
		// Wayve's box is type="number".
		{name: "a number box", sets: month, facts: xcBFacts(), q: match.Question{Selector: "s", Text: "Notice Period (months)", Kind: match.KindText, InputType: match.InputNumber}, entry: "availability-notice-period", answer: "1"},
		{name: "a number box with no unit", sets: month, facts: xcBFacts(), q: match.Question{Selector: "s", Text: "Notice period", Kind: match.KindText, InputType: match.InputNumber}, entry: "availability-notice-period", wantNone: true},
		{name: "weeks", sets: map[string]xcBSet{"availability-notice-period": xcBPref("2 weeks")}, facts: xcBFacts(), q: xcBText("Notice period (in weeks)"), entry: "availability-notice-period", answer: "2"},
		{name: "no unit", sets: month, facts: xcBFacts(), q: xcBText("What is your notice period?"), entry: "availability-notice-period", answer: "in 1 Month"},
		{name: "not divisible", sets: map[string]xcBSet{"availability-notice-period": xcBPref("10 days")}, facts: xcBFacts(), q: xcBText("Notice Period (months)"), entry: "availability-notice-period", answer: "10 days"},
	})
}

// River (Ashby): "Are you located in the Americas or Europe?" is answered from
// the recorded address's country, never by the named-metro entry's No; a region
// the lane has no country list for is left to the user.
func TestXC_LocatedInARegion(t *testing.T) {
	f := xcBFacts()
	f.Address = match.AddressFact{City: "Charleston", Region: "SC", Country: "US"}
	sets := map[string]xcBSet{"based-in-named-metro": xcBAns("No")}
	xcBRunCases(t, []xcBCase{
		{name: "the americas or europe", sets: sets, facts: f, q: xcBYesNo("Are you located in the Americas or Europe?"), label: "Yes"},
		{name: "north america", sets: sets, facts: f, q: xcBYesNo("Are you located in North America?"), label: "Yes"},
		{name: "europe", sets: sets, facts: f, q: xcBYesNo("Are you currently based in Europe?"), label: "No"},
		{name: "a region with no list", sets: sets, facts: f, q: xcBYesNo("Are you located in APAC?"), notEntry: "based-in-named-metro", wantNone: true},
		{name: "a metro still", sets: sets, facts: f, q: xcBYesNo("Are you located in San Francisco or New York City?"), entry: "based-in-named-metro", label: "No"},
	})
}

// BetterUp (Ashby): "now or at a future date" is the time qualifier. Dandy
// (Ashby): "Are you now or will you in the future require ... to secure or
// maintain authorization to work", and a bracketed note on what "other
// employer support" includes, which only widens Yes.
func TestXC_SponsorshipAtAFutureDateAndASupportDefinition(t *testing.T) {
	sets := map[string]xcBSet{"sponsorship-now-or-future": xcBAns("Yes")}
	us := match.Posting{Company: "Dandy", Location: "United States"}
	futureDate := xcBYesNo("Do you now or at a future date require visa sponsorship to work in the United States?")
	const dandy = "Are you now or will you in the future require sponsorship or other employer support to secure or maintain authorization to work in the United States?"
	bare := xcBYesNo(dandy)
	noted := xcBYesNo(dandy + " [“Other employer support” includes but is not limited to: 1) letters of support for visa applications of any type, including O or TN visas; and 2) completion of ICE Form I-983.]")
	xcBRunCases(t, []xcBCase{
		{name: "a future date, a need later", sets: sets, facts: xcBNeeds(false, true), posting: us, q: futureDate, entry: "sponsorship-now-or-future", label: "Yes"},
		{name: "a future date, no need", sets: sets, facts: xcBNeeds(false, false), posting: us, q: futureDate, entry: "sponsorship-now-or-future", label: "No"},
		{name: "to secure authorization, a need later", sets: sets, facts: xcBNeeds(false, true), posting: us, q: bare, entry: "sponsorship-now-or-future", label: "Yes"},
		{name: "to secure authorization, no need", sets: sets, facts: xcBNeeds(false, false), posting: us, q: bare, entry: "sponsorship-now-or-future", label: "No"},
		{name: "the support defined, a need later", sets: sets, facts: xcBNeeds(false, true), posting: us, q: noted, entry: "sponsorship-now-or-future", label: "Yes"},
		{name: "the support defined, no need", sets: sets, facts: xcBNeeds(false, false), posting: us, q: noted, entry: "sponsorship-now-or-future", wantNone: true, reason: match.ReasonAmbiguous},
	})
}

// Oura (Greenhouse): "require Ouraring sponsorship ... to work for Ouraring"
// writes the company as its legal name, the posting's name with a word joined
// to it.
func TestXC_SponsorshipNamingTheCompanysLegalName(t *testing.T) {
	sets := map[string]xcBSet{"sponsorship-now-or-future": xcBAns("Yes")}
	oura := match.Posting{Company: "Oura", Location: "United States"}
	// The board names the company "ŌURA": its case and accent are not the
	// question's.
	board := match.Posting{Company: "ŌURA", Location: "United States"}
	q := xcBYesNo("Do you now, or will you in the future, require Ouraring sponsorship for employment-based immigration status (e.g. H-1B or other status) in order to work for Ouraring?")
	plain := xcBYesNo("Will you now or in the future require sponsorship to work for Oura?")
	xcBRunCases(t, []xcBCase{
		{name: "a need later", sets: sets, facts: xcBNeeds(false, true), posting: oura, q: q, entry: "sponsorship-now-or-future", label: "Yes"},
		{name: "no need", sets: sets, facts: xcBNeeds(false, false), posting: oura, q: q, entry: "sponsorship-now-or-future", label: "No"},
		{name: "the board's name, a need later", sets: sets, facts: xcBNeeds(false, true), posting: board, q: q, entry: "sponsorship-now-or-future", label: "Yes"},
		{name: "the board's name, the plain name written", sets: sets, facts: xcBNeeds(false, true), posting: board, q: plain, entry: "sponsorship-now-or-future", label: "Yes"},
	})
}

// Intercom (Greenhouse): "Current Location" over a closed list of regions,
// each a country before a dash and a part of it after, with catch-alls
// ("US - Other", "EMEA - Other"). It is no catalog to search: the recorded
// state's row is chosen, else the recorded country's catch-all; a state split
// across rows is left.
func TestXC_CurrentLocationOverARegionList(t *testing.T) {
	regions := []string{"Australia", "England", "India", "EMEA - Other", "APAC - Other",
		"US - California (San Francisco Metro)", "US - California (San Francisco Non-Metro)", "US - Colorado", "US - Florida",
		"US - Illinois (Chicago Metro)", "US - Illinois (Chicago Non-Metro)", "US - Massachusetts", "US - New York",
		"US - North Carolina", "US - Texas", "US - Washington", "US - Other",
		"Brazil", "Finland", "France", "Germany", "Greece", "Ireland", "Italy", "Netherlands", "New Zealand", "Portugal", "Spain", "Sweden", "Turkey", "Russia"}
	q := match.Question{Selector: "s", Text: "Current Location", Kind: match.KindCustomListbox, InputType: match.InputSingleSelect, Required: true, Options: xcBOpts(regions...)}
	at := func(city, region string) match.Facts {
		f := xcBFacts()
		f.Address = match.AddressFact{City: city, Region: region, Country: "US"}
		return f
	}
	xcBRunCases(t, []xcBCase{
		{name: "a state no row names", facts: at("Charleston", "SC"), q: q, label: "US - Other"},
		{name: "the state's own row", facts: at("Miami", "FL"), q: q, label: "US - Florida"},
		{name: "a state split across rows", facts: at("Springfield", "IL"), q: q, wantNone: true},
	})
}

// Tebra (Greenhouse): checkboxes labelled 1 to 5, what each number stands for
// given in a legend in the question; the production data technologies the
// user recorded tick their numbers.
func TestXC_NumberedOptionsReadThroughTheLegend(t *testing.T) {
	q := xcBQ("Which of the following data technologies have you operated in a production environment? (Select all that apply) [1 = Spark; 2 = Airflow; 3 = Kafka or other Streaming platforms; 4 = Snowflake, Databricks, or Delta Lake; 5 = Vector Databases (e.g., Pinecone, Milvus, Weaviate)]",
		match.KindCheckbox, "1", "2", "3", "4", "5")
	q.InputType = match.InputMultiSelect
	// "Databricks/Spark" binds whole to Spark and ticks no Databricks; "Apache
	// Kafka/Kinesis" binds through the option's own alternatives.
	sets := map[string]xcBSet{"production-data-pipeline-tech": xcBSel("Apache Airflow", "Databricks/Spark", "Apache Kafka/Kinesis", "Vector databases (pgvector)")}
	m := match.New(xcBBank(t, sets), match.DefaultStages())
	resp, err := m.Match(match.Request{Host: "boards.greenhouse.io", Questions: []match.Question{q}})
	if err != nil {
		t.Fatal(err)
	}
	if r := resp.Results[0]; r.EntryID != "production-data-pipeline-tech" || !slices.Equal(r.ChosenLabels, []string{"2", "1", "3", "5"}) {
		t.Fatalf("ticked %q via %s (%s), want the numbers 2, 1, 3, 5\n%v", r.ChosenLabels, r.EntryID, r.Outcome, r.Trace)
	}
}

// insitro (Ashby): the EAD category code, typed, from the recorded status and
// STEM extension (owner decision): F-1 OPT is (c)(3)(B), with the STEM
// extension (c)(3)(C); a status no EAD carries is "N/A" as the question asks;
// the user's own code wins; a status the table does not know stays with the user.
func TestXC_EADCategoryFromTheRecordedStatus(t *testing.T) {
	const insitro = "If you are authorized to work in the United States, is that authorization pursuant to an Employment Authorization Document (EAD)? If yes, please indicate the applicable EAD category code. If no, please put N/A."
	status := func(v string) match.Facts {
		f := xcBFacts()
		f.VisaStatus = v
		return f
	}
	stem := map[string]xcBSet{"stem-opt-24-month-extension": xcBAns("Yes")}
	own := map[string]xcBSet{"ead-category-code": xcBAns("(c)(3)(C)")}
	// The live box is one line, which the work-authorization entry also takes.
	line := xcBText(insitro)
	line.InputType = match.InputText
	xcBRunCases(t, []xcBCase{
		{name: "F-1 OPT with the STEM extension", sets: stem, facts: status("F-1 OPT"), q: xcBText(insitro), entry: "ead-category-code", answer: "Yes, (c)(3)(C)"},
		{name: "a one-line box", sets: map[string]xcBSet{"stem-opt-24-month-extension": xcBAns("Yes"), "work-auth-us": xcBAns("Yes")}, facts: status("F-1 OPT"), q: line, entry: "ead-category-code", answer: "Yes, (c)(3)(C)"},
		{name: "F-1 OPT", facts: status("F-1 OPT"), q: xcBText(insitro), entry: "ead-category-code", answer: "Yes, (c)(3)(B)"},
		{name: "the code alone", sets: stem, facts: status("student_work_authorization: F-1 OPT"), q: xcBText("EAD category code"), entry: "ead-category-code", answer: "(c)(3)(C)"},
		{name: "H-4 EAD", facts: status("work_visa: H-4 EAD"), q: xcBText(insitro), entry: "ead-category-code", answer: "Yes, (c)(26)"},
		{name: "H-1B carries no EAD", facts: status("work_visa: H-1B"), q: xcBText(insitro), entry: "ead-category-code", answer: "N/A"},
		{name: "a citizen", facts: status("citizen"), q: xcBText(insitro), entry: "ead-category-code", answer: "N/A"},
		{name: "the user's own code", sets: own, facts: status("F-1 OPT"), q: xcBText(insitro), entry: "ead-category-code", answer: "Yes, (c)(3)(C)"},
		{name: "a status the table does not know", facts: status("other: humanitarian parole"), q: xcBText(insitro), entry: "ead-category-code", wantNone: true},
	})
}

// insitro (Ashby): an AI-use statement about this application. The shipped
// ai-use-disclosure entry is blank, so the question stays the user's; the
// answer the user writes there is the one every AI-use statement gets.
const xcBLongDisclosure = "Yes. I used Applysmith, an AI job-application assistant, to fill in this application. It answers each question from my own résumé, profile and notes, and every fact in it is mine."

func TestXC_AIUseStatementTakesOnlyTheUsersOwnAnswer(t *testing.T) {
	q := xcBText("We welcome responsible AI use. Did you use an AI tool to tailor your résumé, write responses, or submit this application? If yes, name the tool, describe its role, and share the main prompt(s) you used.")
	yesNo := xcBYesNo("Did you use AI tools such as ChatGPT to complete this application?")
	written := func(a string) map[string]xcBSet { return map[string]xcBSet{"ai-use-disclosure": xcBAns(a)} }
	const disclosed = "Yes. An AI assistant filled in the form fields from my own résumé; I reviewed every answer."
	xcBRunCases(t, []xcBCase{
		{name: "blank: left to the user", q: q, wantNone: true, reason: match.ReasonContextPolicy},
		{name: "the user's disclosure", sets: written(disclosed), q: q, entry: "ai-use-disclosure", answer: disclosed},
		{name: "the user's No", sets: written("No"), q: yesNo, entry: "ai-use-disclosure", label: "No"},
		{name: "the user's Yes", sets: written("Yes"), q: yesNo, entry: "ai-use-disclosure", label: "Yes"},
		{name: "Calendly: a long disclosure shares few words with the question", sets: written(xcBLongDisclosure), q: xcBText("Was this application prepared or submitted in whole or in part by an AI system, language model, or automated agent?"), entry: "ai-use-disclosure", answer: xcBLongDisclosure},
		{name: "the user's No on a text question", sets: written("No"), q: xcBText("Was this application prepared or submitted in whole or in part by an AI system, language model, or automated agent?"), entry: "ai-use-disclosure", answer: "No"},
	})
}

// Akuity (Greenhouse, live): "Have you worked at a B2B SaaS startup ... for at
// least 1 year?" is No when every job the user recorded, counted whole months
// through today, does not reach the floor, whatever the kind of company; a
// record that reaches it, or a job without a start, is left to the ladder.
func TestXC_AWorkedForFloorAboveTheWholeRecordIsNo(t *testing.T) {
	short := xcBFacts()
	short.Employment = []match.EmploymentFact{{Employer: "Acme", Title: "Engineer", Start: "2026-06", End: "present"}, {Employer: "Lab", Title: "Intern", Start: "2023-07", End: "2023-09"}}
	today := match.Date{Year: 2026, Month: 11, Day: 5}
	long := short
	long.Employment = []match.EmploymentFact{{Employer: "Acme", Title: "Engineer", Start: "2024-06", End: "present"}}
	undated := short
	undated.Employment = []match.EmploymentFact{{Employer: "Acme", Title: "Engineer", End: "present"}}
	const q = "Have you worked at a B2B SaaS startup (Seed to Series C) for at least 1 year?"
	xcBRunCases(t, []xcBCase{
		{name: "nine months in all", facts: short, today: today, q: xcBYesNo(q), label: "No"},
		{name: "more than two years", facts: short, today: today, q: xcBYesNo("Have you ever worked in a regulated industry for more than 2 years?"), label: "No"},
		{name: "a record past the floor", facts: long, today: today, q: xcBYesNo(q), wantNone: true},
		{name: "a job without a start", facts: undated, today: today, q: xcBYesNo(q), wantNone: true},
		{name: "a future commitment", facts: short, today: today, q: xcBYesNo("Are you willing to work for us for at least 1 year?"), wantNone: true},
	})
}

// AngelList (Ashby, live): "What is your work authorization status in the
// United States?" over a citizen-or-resident option, a work-visa holder, no
// visa and a remote-abroad option is the status picker; an F-1 OPT holder is
// the work-visa holder.
func TestXC_AngelListWorkVisaHolder(t *testing.T) {
	opts := []string{"United States Citizen or Permanent Resident", "US Work Visa Holder", "No Visa; Seeking work authorization", "Not applicable; working remotely outside of United States"}
	q := xcBQ("What is your work authorization status in the United States?", match.KindRadioGroup, opts...)
	q.Required = true
	citizen := xcBFacts()
	citizen.VisaStatus, citizen.RequiresSponsorshipFuture = "citizen", xcBBool(false)
	unauthorized := xcBFacts()
	unauthorized.VisaStatus, unauthorized.RequiresSponsorshipNow = "not_authorized", xcBBool(true)
	sets := map[string]xcBSet{"us-work-authorization-status": xcBPref("I am authorized to work in this country for any employer", "for any employer"),
		"current-immigration-status": xcBPref("F-1 OPT", "OPT", "F1 OPT", "Optional Practical Training", "Other")}
	xcBRunCases(t, []xcBCase{
		{name: "OPT holds a work visa", sets: sets, facts: xcBFacts(), posting: match.Posting{Company: "AngelList", Location: "San Francisco"}, q: q, entry: "us-work-authorization-status", label: opts[1]},
		{name: "a citizen", sets: sets, facts: citizen, posting: match.Posting{Company: "AngelList", Location: "San Francisco"}, q: q, entry: "us-work-authorization-status", label: opts[0]},
		{name: "no authorization names no option", sets: sets, facts: unauthorized, posting: match.Posting{Company: "AngelList", Location: "San Francisco"}, q: q, wantNone: true},
	})
}

// Abacus (Greenhouse, live): one text box under a certification whose
// description asks "Please sign and date below." takes the typed signature
// and today's date; a box that asks only to sign keeps the name alone.
func TestXC_SignAndDateInOneBox(t *testing.T) {
	sets := map[string]xcBSet{"applicant-full-name": xcBAns("Ada Example")}
	const cert = "I certify that my answers are true and complete to the best of my knowledge. If this application leads to employment, I understand that false or misleading information in my application or interview may result in my release."
	xcBRunCases(t, []xcBCase{
		{name: "sign and date", sets: sets, facts: xcBFacts(), q: xcBText(cert + " Please sign and date below."), answer: "Ada Example, 07/04/2031"},
		{name: "a signature and the date", sets: sets, facts: xcBFacts(), q: xcBText("Signature and date"), answer: "Ada Example, 07/04/2031"},
		{name: "sign alone", sets: sets, facts: xcBFacts(), q: xcBText("Please sign by typing your full name."), answer: "Ada Example"},
		// Cohere Health (Greenhouse, live): the name and the date typed under a certification.
		{name: "type your name and the date", sets: sets, facts: xcBFacts(),
			q: xcBText("I HAVE READ THE CERTIFICATION ABOVE AND UNDERSTAND AND AGREE TO ITS TERMS. Please type your name and the date."), answer: "Ada Example, 07/04/2031"},
	})
}

// Hopper (Ashby, live): "Rate your ability to ..." over first-person
// statements that claim more and more is a self-assessment the record does not
// settle; the owner's default is the least claimed, the first, for review. A
// list that is not all such statements is left.
func TestXC_RateYourAbilityTakesTheLeastClaimed(t *testing.T) {
	report := []string{"I can write SQL and build one-off analyses on request", "I can build dashboards that others use without my help",
		"I can design reusable templates (SQL, notebooks, prompts) that produce consistent reports across many customers",
		"I can automate end-to-end reporting pipelines, including AI-assisted narrative generation",
		"I have owned a reporting product used by external customers on a regular cadence"}
	talk := []string{"I can document technical projects and processes", "I can present results to cross-functional internal teams",
		"I can write reports and narratives for non-technical leadership", "I can present to external partners and executives and handle hard questions live"}
	q := func(text string, opts ...string) match.Question {
		q := xcBQ(text, match.KindCheckbox, opts...)
		q.Required = true
		return q
	}
	xcBRunCases(t, []xcBCase{
		{name: "reporting", facts: xcBFacts(), q: q("Rate your ability to deliver repeatable reporting and analytics:", report...), label: report[0]},
		{name: "communication", facts: xcBFacts(), q: q("Rate your ability to communicate technical and strategic work", talk...), label: talk[0]},
		{name: "not all statements", facts: xcBFacts(), q: q("Rate your ability to communicate technical and strategic work", append([]string{"Beginner"}, talk...)...), wantNone: true},
	})
}

// Dave (Ashby, live): "Are you permanently authorized to work in the United
// States without visa sponsorship? Select "Yes" if you are a U.S. citizen,
// Green Card holder, or have a work permit that does not require company
// sponsorship." asks the permanence: No on F-1 OPT with a need later, Yes for
// a citizen.
func TestXC_PermanentAuthorizationWithoutSponsorship(t *testing.T) {
	q := xcBYesNo("Are you permanently authorized to work in the United States without visa sponsorship? Select \"Yes\" if you are a U.S. citizen, Green Card holder, or have a work permit that does not require company sponsorship.")
	q.Required = true
	citizen := xcBFacts()
	citizen.VisaStatus, citizen.RequiresSponsorshipFuture = "citizen", xcBBool(false)
	dave := match.Posting{Company: "Dave", Location: "United States"}
	xcBRunCases(t, []xcBCase{
		{name: "F-1 OPT", facts: xcBFacts(), posting: dave, q: q, entry: "permanent-work-authorization", label: "No"},
		{name: "a citizen", facts: citizen, posting: dave, q: q, entry: "permanent-work-authorization", label: "Yes"},
		// Axios (Greenhouse, live): asked for in words, the eligibility is the
		// work-authorization detail; asked as a Yes/No, the permanence.
		{name: "asked in words", facts: xcBFacts(), sets: map[string]xcBSet{"work-authorization-expiry": xcBAns("2032-08-02")},
			q: xcBText("What is your eligibility to work permanently in the US?"), entry: "work-authorization-detail", answer: "F-1 visa, currently on OPT; work authorization valid through 2032-08-02"},
		{name: "eligible to work permanently", facts: xcBFacts(), q: xcBYesNo("Are you eligible to work permanently in the United States?"), entry: "permanent-work-authorization", label: "No"},
	})
}

// Openly (Ashby, live): a posted range asked against the applicant's
// requirements is the posted-range comfort, and a relatives question whose
// examples close on "etc." with a follow-up only for a Yes takes the stored No
// in its text box.
func TestXC_OpenlyRangeAndRelatives(t *testing.T) {
	sets := map[string]xcBSet{"comfortable-with-posted-range": xcBAns("Yes"), "relatives-at-employer": xcBAns("No")}
	openly := match.Posting{Company: "Openly", Location: "Remote - United States"}
	rel := xcBText("Do you have a close relative (spouse/partner, child, parent/parent-in-law, sibling/sibling-in-law, etc.), or someone who is living in the same household as you, who is currently employed at Openly? If yes, please share the name of the employee and the relationship.")
	rel.Required = true
	xcBRunCases(t, []xcBCase{
		{name: "the range meets the requirements", sets: sets, facts: xcBFacts(), posting: openly,
			q: xcBYesNo("Will the target salary range listed ($145,600 - $163,800) meet your salary requirements?"), entry: "comfortable-with-posted-range", label: "Yes"},
		{name: "relatives", sets: sets, facts: xcBFacts(), posting: openly, q: rel, entry: "relatives-at-employer", answer: "No"},
	})
}

// Apollo.io (Greenhouse, live): one text box asks the need and, of a Yes, the
// current authorization: a need is "Yes." and the recorded detail, no need a
// bare "No".
func TestXC_SponsorshipThenCurrentAuthorizationInWords(t *testing.T) {
	q := xcBText("Will you now or in the future require work visa sponsorship to legally work in the United States? If yes, what is your current work authorization?")
	q.Required = true
	sets := map[string]xcBSet{"sponsorship-now-or-future": xcBAns("Yes"), "work-authorization-expiry": xcBAns("2032-08-02")}
	none := xcBFacts()
	none.VisaStatus, none.RequiresSponsorshipFuture = "citizen", xcBBool(false)
	apollo := match.Posting{Company: "Apollo.io", Location: "Remote, United States"}
	xcBRunCases(t, []xcBCase{
		{name: "a need", sets: sets, facts: xcBFacts(), posting: apollo, q: q, entry: "sponsorship-now-or-future",
			answer: "Yes. F-1 visa, currently on OPT; work authorization valid through 2032-08-02"},
		{name: "no need", sets: sets, facts: none, posting: apollo, q: q, entry: "sponsorship-now-or-future", answer: "No"},
	})
}

// KnowBe4 (Greenhouse, live): the sanctions screen names its places in the
// question and offers them as options beside one that opens with "No"; its
// consent to be contacted names "any employment references I provide", which
// asks for no one's details.
func TestXC_KnowBe4SanctionsAndContactConsent(t *testing.T) {
	f := xcBFacts()
	f.Citizenships = []string{"China"}
	f.Address = match.AddressFact{City: "Charleston", Region: "SC", Country: "US"}
	places := []string{"Cuba", "Iran", "North Korea", "Syria", "Crimea Region", "Donetsk Region", "Luhansk Region", "Kherson Region", "Zaporizhzhia Region",
		"No. I am not located in or a national of Cuba, Iran, North Korea, or Syria, or currently located in the Crimea, Donetsk, Luhansk, Kherson, or Zaporizhzhia territories"}
	san := xcBQ("Are you located in or a national of Cuba, Iran, North Korea, or Syria, or currently located in the Crimea, Donetsk, Luhansk, Kherson, or Zaporizhzhia territories? If so, indicate which territories.", match.KindCustomListbox, places...)
	san.InputType, san.Required = match.InputMultiSelect, true
	con := xcBQ("By submitting this application, I hereby provide consent to KnowBe4 to communicate directly with me, and any employment references I provide to KnowBe4, via phone call, text message, email, and/or LinkedIn regarding my application for and potential employment with KnowBe4. If at any time I want to opt-out of any form of communication, I will promptly inform KnowBe4 of this choice.", match.KindCustomListbox, "I understand")
	con.Required = true
	cuban := xcBFacts()
	cuban.Citizenships = []string{"Cuba"}
	cuban.Address = f.Address
	xcBRunCases(t, []xcBCase{
		{name: "sanctions", facts: f, q: san, label: places[9]},
		// A place that holds is left for the applicant.
		{name: "sanctions, a named citizenship", facts: cuban, q: san, wantNone: true},
		{name: "contact consent", facts: f, legal: true, q: con, label: "I understand"},
	})
}

// Voxel (Ashby, live): a note after the sponsorship question tells who selects
// "Yes", its examples abbreviated ("e.g.", "etc.") inside brackets.
func TestXC_SponsorshipSelectYesNoteWithExamples(t *testing.T) {
	const text = "Will you require U.S. visa sponsorship now or in the future? Select “Yes” if you currently require, or will require, employer-sponsored work authorization to work in the U.S. (e.g. H-1B, O-1, TN, etc.), either now or at any point in the future."
	xcBRunCases(t, []xcBCase{
		xcBFixedYesNo("Voxel", text, "sponsorship-now-or-future", "Yes", map[string]xcBSet{"sponsorship-now-or-future": xcBAns("Yes")}),
	})
}

// KnowBe4 (Greenhouse, live): the sources one used to research the company
// are the user's selections, each that the list offers ticked.
func TestXC_CompanyResearchSources(t *testing.T) {
	q := match.Question{Selector: "s", Text: "How well do you know us? Please select all of the following sources/tools that you have used to research KnowBe4 and learn more about us.",
		Kind: match.KindCheckbox, InputType: match.InputCheckbox, Required: true,
		Options: xcBOpts("Indeed", "Glassdoor", "The Muse", "Googled \"Top Workplaces Tampa Bay\"", "Facebook", "Instagram", "Twitter", "YouTube", "KnowBe4 Careers Blog", "I am a current employee", "Other")}
	bank := xcBBank(t, map[string]xcBSet{"company-research-sources": xcBSel("Company website", "Careers blog", "LinkedIn", "Glassdoor", "Indeed")})
	resp, err := match.New(bank, match.DefaultStages()).Match(match.Request{Host: "jobs.example.com", Facts: xcBFacts(), Today: xcBDay, Questions: []match.Question{q}})
	if err != nil {
		t.Fatal(err)
	}
	r := resp.Results[0]
	if r.Outcome == match.OutcomeAbstained || r.EntryID != "company-research-sources" || !slices.Equal(r.ChosenLabels, []string{"KnowBe4 Careers Blog", "Glassdoor", "Indeed"}) {
		t.Fatalf("%s entry=%s labels=%q reason=%s", r.Outcome, r.EntryID, r.ChosenLabels, r.Reason)
	}
	// A fresh bank leaves the question to the user.
	resp, err = match.New(xcBBank(t, nil), match.DefaultStages()).Match(match.Request{Host: "jobs.example.com", Facts: xcBFacts(), Today: xcBDay, Questions: []match.Question{q}})
	if err != nil {
		t.Fatal(err)
	}
	if r := resp.Results[0]; r.Outcome != match.OutcomeAbstained {
		t.Fatalf("a fresh bank answered %q", r.ChosenLabels)
	}
}

// Regard (Ashby, live): "Which AI coding tool do you use most often?" over
// named tools is answered from the daily-driver entry's own text, which names
// its daily driver first; a text naming none of the options stays the user's,
// and the Yes/No keeps the fixed answer.
func TestXC_AICodingToolNamedInTheDailyDriverAnswer(t *testing.T) {
	const driver = "Claude Code is my daily driver for repo-first coding. I also use ChatGPT for research, and Codex-style workflows for repo-aligned agents."
	tools := []string{"Cursor", "Claude Code", "GitHub Copilot", "Open AI Codex", "ChatGPT", "Other", "None"}
	run := func(answer string, q match.Question) match.Result {
		bank := xcBBank(t, map[string]xcBSet{"ai-coding-tools-daily-driver": xcBAns(answer)})
		m := match.New(bank, match.DefaultStages())
		q.Selector = "#ai"
		resp, err := m.Match(match.Request{Host: "jobs.ashbyhq.com", Facts: xcBFacts(), Today: xcBDay, Posting: match.Posting{Company: "Regard"}, Questions: []match.Question{q}})
		if err != nil {
			t.Fatal(err)
		}
		return resp.Results[0]
	}
	most := xcBQ("Which AI coding tool do you use most often in day-to-day software development?", match.KindRadioGroup, tools[:6]...)
	if r := run(driver, most); r.ChosenLabel != "Claude Code" {
		t.Fatalf("most often: %+v", r)
	}
	if r := run("I mostly use an in-house assistant.", most); r.Outcome != match.OutcomeAbstained {
		t.Fatalf("a text naming no option filled one: %+v", r)
	}
	if r := run("Yes", xcBYesNo("Do you use AI coding tools daily?")); r.ChosenLabel != "Yes" {
		t.Fatalf("Yes/No: %+v", r)
	}
}

// Rain (Ashby, live): a hybrid office ask over Yes / No / "Open to relocation"
// / "Open to discussing" is a Yes/No beside two hedges; the recorded Yes is
// chosen, never a hedge.
func TestXC_OfficeDaysWithOpenToHedges(t *testing.T) {
	q := xcBQ("Are you able to work in a hybrid role and come into a NYC office 3 days a week?", match.KindRadioGroup, "Yes", "No", "Open to relocation", "Open to discussing")
	xcBRunCases(t, []xcBCase{{name: "office days with hedges", sets: map[string]xcBSet{"office-attendance-ok": xcBAns("Yes")}, facts: xcBFacts(),
		posting: match.Posting{Company: "Rain", Location: "New York, NY"}, q: q, entry: "office-attendance-ok", label: "Yes"}})
}

// Amount (Greenhouse, live): familiarity with team tooling (Confluence, Jira,
// PagerDuty, Sentry, Kibana) is a skill gate as a language or a cloud is: the
// recorded skills answer it, and without them the gate abstains for the
// evidence lanes.
func TestXC_FamiliarWithTeamTooling(t *testing.T) {
	q := xcBQ("Are you familiar with tools such as Confluence, Jira, and on-call management software such as PagerDuty and experience with error monitoring software (Sentry, Kibana)?", match.KindCustomListbox, "Yes", "No")
	sets := map[string]xcBSet{"skill-experience-knowledge-gate": xcBAns("Yes")}
	amount := match.Posting{Company: "Amount", Location: "Chicago, IL"}
	var tools []match.SkillFact
	for _, n := range []string{"Confluence", "Jira", "PagerDuty", "Sentry", "Kibana"} {
		tools = append(tools, match.SkillFact{Name: n})
	}
	xcBRunCases(t, []xcBCase{
		{name: "tools recorded", sets: sets, facts: match.Facts{Skills: tools}, posting: amount, q: q, entry: "skill-experience-knowledge-gate", label: "Yes"},
		{name: "tools not recorded", sets: sets, facts: xcBFacts(), posting: amount, q: q, entry: "skill-experience-knowledge-gate", wantNone: true},
		{name: "the example of a category not recorded", sets: sets, facts: match.Facts{Skills: tools[:2]}, posting: amount, q: q, entry: "skill-experience-knowledge-gate", wantNone: true},
	})
}
