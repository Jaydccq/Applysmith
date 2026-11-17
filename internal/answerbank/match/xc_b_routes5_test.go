package match_test

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"applysmith/internal/answerbank/match"
)

func TestXC_RoutesEducationAndSelfID(t *testing.T) {
	edu := map[string]xcBSet{"highest-level-education": xcBPref("Master's Degree"), "employer-industry": xcBPref("Information Technology")}
	degrees := []string{"High School", "Bachelor's Degree", "Masters Degree", "Doctorate"}
	decline := "Decline to self-identify"
	rowDegree := xcBQ("Degree*", match.KindSelect, degrees...)
	rowDegree.Selector = "education-4f9a1c07--degree"
	rowDegree.SectionPath = []string{"Education", "Education 2"}
	industry := xcBQ("Type of business", match.KindSelect, "Healthcare", "Information Techn", "Finance")
	industry.SectionPath = []string{"Work Experience", "Work Experience 1"}
	race := xcBQ("Race", match.KindCheckbox, "East Asian", "South Asian", "White")
	race.InputType = match.InputMultiSelect
	xcBRunCases(t, []xcBCase{
		// XC-064
		{name: "XC-064 highest degree attained", sets: edu, facts: match.Facts{},
			q: xcBQ("Highest degree attained?", match.KindSelect, degrees...), entry: "highest-level-education", label: "Masters Degree"},
		{name: "XC-064 bare degree", sets: edu, facts: match.Facts{},
			q: xcBQ("Degree*", match.KindSelect, degrees...), entry: "highest-level-education", label: "Masters Degree"},
		{name: "XC-064 workday row degree abstains", sets: edu, facts: xcBFacts(), q: rowDegree, wantNone: true},
		{name: "XC-064 type of business with a cut label", sets: edu, facts: xcBFacts(), q: industry, entry: "employer-industry", label: "Information Techn"},

		// XC-065 and Ashby's survey
		{name: "race group", sets: map[string]xcBSet{"eeo-race": xcBPref("East Asian")}, q: race, entry: "eeo-race", label: "East Asian"},
		{name: "survey", sets: map[string]xcBSet{"eeo-transgender": xcBPref("No")},
			q: xcBQ("Do you identify as transgender?", match.KindRadioGroup, "No", "Yes", "Prefer not to say"), entry: "eeo-transgender", label: "No"},
		{name: "XC-065 bare gender identity", facts: xcBFacts(),
			q: xcBQ("Gender Identity", match.KindSelect, "Man", "Woman", "Non-binary", decline), entry: "eeo-gender", label: decline},
		// Sola (Ashby, live): a misspelt sponsorship question whose Yes names the employer.
		{name: "misspelt sponsorship", sets: map[string]xcBSet{"sponsorship-now-or-future": xcBPref("Yes")}, facts: xcBFacts(),
			q: xcBQ("Will you now or will you in the future require employment visa sponsporship?", match.KindRadioGroup,
				"Yes, I will require Sola to sponsor my employment", "No, I do not require sponsorship to work in the United Sates"),
			entry: "sponsorship-now-or-future", label: "Yes, I will require Sola to sponsor my employment"},
		// Partly (Ashby, live): eligibility and sponsorship in one question, its options the three standings.
		{name: "work authorization and sponsorship standings", facts: xcBFacts(),
			q: xcBQ("Are you legally eligible to work in the US, and will you need visa sponsorship now or in the future to remain eligible?", match.KindRadioGroup,
				"Yes, I'm eligible and won't need sponsorship.", "Yes, I'm eligible now, but will need sponsorship in the future.", "No, I'm not eligible and will need sponsorship."),
			posting: match.Posting{Company: "Partly", Location: "United States"},
			entry:   "work-auth-and-future-sponsorship", label: "Yes, I'm eligible now, but will need sponsorship in the future."},
		// Plain (Ashby, live): the Yes/No question drawn as two boxes.
		{name: "sponsorship as two boxes", sets: map[string]xcBSet{"sponsorship-now-or-future": xcBPref("Yes")}, facts: xcBFacts(),
			q: match.Question{Selector: "s", Text: "Would you require sponsorship now or in the future?", Kind: match.KindCheckbox, InputType: match.InputMultiSelect,
				Required: true, Options: xcBOpts("No", "Yes")}, posting: match.Posting{Company: "Plain", Location: "San Francisco office"},
			entry: "sponsorship-now-or-future", label: "Yes"},
		{name: "misspelt sponsorship over yes and no", sets: map[string]xcBSet{"sponsorship-now-or-future": xcBPref("Yes")}, facts: xcBFacts(),
			q: xcBYesNo("Will you now or will you in the future require employment visa sponsporship?"), entry: "sponsorship-now-or-future", label: "Yes"},
		// Sony (Greenhouse, live): a required gender list that also offers cisgender and transgender options takes the recorded gender.
		{name: "XC-065 gender identity over a list with cisgender options", sets: map[string]xcBSet{"eeo-gender": xcBPref("Male"), "eeo-transgender": xcBPref("No")},
			q: xcBQ("Gender Identity", match.KindSelect, "Agender", "Cisgender/Female", "Cisgender/Male", "Female", "Genderqueer", "Intersex", "Male",
				"Non-binary/Gender Non-Conforming/Gender Variant", "Transgender", "Two-Spirit", "I don't wish to answer"),
			entry: "eeo-gender", label: "Male"},
		{name: "XC-065 what is your gender identity", facts: xcBFacts(),
			q: xcBQ("What is your gender identity?", match.KindSelect, "Man", "Woman", "Non-binary", decline), entry: "eeo-gender", label: decline},
		{name: "XC-065 transgender question", facts: xcBFacts(),
			q: xcBQ("Do you identify as transgender?", match.KindSelect, "Yes, I identify as transgender", "No, I do not identify as transgender", decline), entry: "eeo-transgender", label: decline},
		{name: "XC-065 bare I identify as with both poles", facts: xcBFacts(),
			q: xcBQ("I identify as:", match.KindSelect, "Cisgender", "Transgender", "Non-binary", decline), entry: "eeo-transgender"},
		{name: "XC-065 sexual orientation", facts: xcBFacts(),
			q: xcBQ("Sexual Orientation", match.KindSelect, "Heterosexual", "Gay or Lesbian", "Bisexual", decline), entry: "eeo-orientation", label: decline},
		{name: "XC-065 disability of any kind", facts: xcBFacts(),
			q: xcBQ("Do you identify as a person with a disability of any kind, visible or invisible?", match.KindSelect,
				"Yes, I have a disability or have had one in the past", "No, I do not have a disability", decline),
			entry: "eeo-disability", label: decline},
		{name: "XC-065 bare disability select", facts: xcBFacts(),
			q: xcBQ("Disability", match.KindSelect, "Yes, I have a disability or have had one in the past", "No, I do not have a disability", decline), entry: "eeo-disability", label: decline},
		{name: "XC-065 accommodation request is not self-identification", facts: xcBFacts(),
			q: xcBText("Disability accommodation: please describe any reasonable adjustment you require"), notEntry: "eeo-disability", wantNone: true},
		{name: "XC-065 combined race and ethnic background", facts: xcBFacts(),
			q:     xcBQ("Racial/ethnic background", match.KindSelect, "Asian", "Black or African American", "Hispanic or Latino", "White", decline),
			entry: "eeo-race", label: decline},
		{name: "XC-065 hispanic regardless of race", facts: xcBFacts(),
			q:     xcBQ("Are you Hispanic or Latino? Please answer regardless of race.", match.KindSelect, "Yes, I am Hispanic or Latino", "No, I am not Hispanic or Latino", decline),
			entry: "eeo-ethnicity", label: decline},
	})
}

// Alnylam (Greenhouse, live): a state notice offers "I am not a California
// applicant" beside "I confirm". That option is a fact of where the applicant
// lives and works, not a decline: it is chosen when the record and the posting
// both put the applicant elsewhere, the notice is acknowledged when either names
// the state, and anything less certain is the user's.
func TestXC_StateNoticeOutOfScopeOption(t *testing.T) {
	lives := func(region string) match.Facts {
		f := xcBFacts()
		f.Address = match.AddressFact{City: "Example City", Region: region, Country: "US"}
		return f
	}
	notice := xcBQ("California Applicants:", match.KindCustomListbox, "I confirm", "I am not a California applicant")
	notice.InputType, notice.Required = match.InputSingleSelect, true
	follow := xcBQ("If you answered I Confirm to the question above, please also respond to the following:", match.KindCustomListbox,
		"I confirm", "I do not confirm", "I am not a California Applicant")
	follow.InputType, follow.Required = match.InputSingleSelect, true
	ma, sf, us := match.Posting{Location: "Cambridge, MA"}, match.Posting{Location: "San Francisco, CA"}, match.Posting{Location: "United States"}
	xcBRunCases(t, []xcBCase{
		{name: "elsewhere by record and posting", facts: lives("SC"), posting: ma, legal: true, q: notice,
			entry: "mandatory-acknowledge", label: "I am not a California applicant"},
		{name: "the follow-up of a notice that is not the applicant's", facts: lives("SC"), posting: ma, legal: true, q: follow,
			entry: "mandatory-acknowledge", label: "I am not a California Applicant"},
		{name: "a resident of the state acknowledges", facts: lives("California"), posting: ma, legal: true, q: notice,
			entry: "mandatory-acknowledge", label: "I confirm"},
		{name: "a posting in the state acknowledges", facts: lives("SC"), posting: sf, legal: true, q: notice,
			entry: "mandatory-acknowledge", label: "I confirm"},
		{name: "a posting with no state is the user's", facts: lives("SC"), posting: us, legal: true, q: notice,
			wantNone: true, reason: match.ReasonAmbiguous},
		{name: "no recorded state is the user's", facts: lives(""), posting: ma, legal: true, q: notice,
			wantNone: true, reason: match.ReasonAmbiguous},
		{name: "a resident's three-way follow-up is the user's", facts: lives("CA"), posting: ma, legal: true, q: follow, wantNone: true},
	})
}

// Rebar (Ashby, live): the office policy's presence question over statements
// that say "located in" and "relocate" is the relocation-or-local picker, as
// a radio group or as checkboxes.
func TestXC_OfficePolicyPresenceOverLocatedStatements(t *testing.T) {
	const text = "Our office policy requires you to work onsite from our New York City office located in Flatiron. Are you currently located in New York and can accomodate this schedule?"
	labels := []string{"Yes, I'm located in New York and can accommodate", "No", "Yes, I'm looking to relocate to NYC", "Other - I'm open to discussing further"}
	sets := map[string]xcBSet{"relocation-or-local": xcBPref("relocate")}
	checkboxes := xcBQ(text, match.KindCheckbox, labels...)
	checkboxes.InputType = match.InputMultiSelect
	xcBRunCases(t, []xcBCase{
		{name: "radio group", sets: sets, q: xcBQ(text, match.KindRadioGroup, labels...), entry: "relocation-or-local", label: "Yes, I'm looking to relocate to NYC"},
		{name: "checkboxes", sets: sets, q: checkboxes, entry: "relocation-or-local", label: "Yes, I'm looking to relocate to NYC"},
	})
}

// n8n (Ashby, live): a Yes/No travel question that states the share of time the
// role travels is answered from the recorded ceiling ("Up to 25%"): a stated
// share within it is Yes, one whose least share is above it No, and one that
// runs across it is left for review.
func TestXC_TravelYesNoAgainstTheRecordedCeiling(t *testing.T) {
	sets := map[string]xcBSet{"willing-to-travel": xcBPref("Up to 25%", "Occasionally", "Yes")}
	limited := map[string]xcBSet{"willing-to-travel": xcBPref("Up to 25%", "Occasionally", "Yes"), "travel-above-ceiling-limited-period": xcBAns("Yes")}
	c := func(name, text, label string) xcBCase {
		k := xcBCase{name: name, sets: sets, facts: xcBFacts(), q: xcBYesNo(text), entry: "willing-to-travel", label: label}
		if label == "" {
			k.wantNone, k.reason = true, match.ReasonAmbiguous
		}
		return k
	}
	xcBRunCases(t, []xcBCase{
		c("n8n", "This role involves travelling approximately 30–50% of the time, primarily across the US, including focused on-site customer engagements. Are you able and willing to meet this requirement?", "No"),
		c("within the ceiling", "This role requires up to 20% travel. Are you able and willing to meet this requirement?", "Yes"),
		c("up to a share above the ceiling", "Are you willing to travel up to 50% of the time?", "No"),
		c("at least the ceiling", "Are you willing to travel at least 25% of the time?", ""),
		c("a range across the ceiling", "This role travels 10-30% of the time. Are you willing to travel?", ""),
		c("no share", "Are you willing to travel?", "Yes"),
		c("weeks a month above the ceiling (Eagle)", "Are you willing to travel within the United States ? Travel will always be purpose-based, with the founders, and roughly M-Th 2-3 weeks/month for the first 100 days following an acquisition.", "No"),
		// The owner's Yes to more travel for a limited period; a lasting share is still the ceiling's.
		{name: "a limited period the applicant accepts", sets: limited, facts: xcBFacts(), entry: "willing-to-travel", label: "Yes",
			q: xcBYesNo("Are you willing to travel within the United States ? Travel will always be purpose-based, with the founders, and roughly M-Th 2-3 weeks/month for the first 100 days following an acquisition.")},
		{name: "a lasting share above the ceiling", sets: limited, facts: xcBFacts(), entry: "willing-to-travel", label: "No",
			q: xcBYesNo("Are you willing to travel up to 50% of the time?")},
		c("the work mode is not the travel share", "This is a 100% remote role with occasional travel to our HQ. Are you willing to travel?", "Yes"),
		c("an on-site share is not the travel share", "This role is 50% on-site and requires occasional travel. Are you willing to travel?", "Yes"),
		c("impediments: a willing applicant has none", "Do you have any impediments to traveling?", "No"),
		c("impediments to a share above the ceiling", "Do you have any impediments to traveling up to 50% of the time?", "Yes"),
		c("impediments within the ceiling", "Any impediments to traveling up to 20% of the time?", "No"),
	})
}

// n8n (Ashby, live): a list of fourteen states and "Other state" for an
// applicant in South Carolina takes the catch-all; a list naming the state in
// any spelling, or without a catch-all, never does.
func TestXC_AStateListWithoutTheStateTakesItsCatchAll(t *testing.T) {
	const text = "Which U.S. state would you primarily work from if hired?"
	n8n := []string{"California", "Colorado", "Florida", "Georgia", "Illinois", "Massachusetts", "New Jersey", "New York", "North Carolina",
		"Other state", "Pennsylvania", "Texas", "Virginia", "Washington"}
	sc := map[string]xcBSet{"address-state": xcBPref(match.StateChoices("SC")...)}
	listbox := func(labels ...string) match.Question {
		q := xcBQ(text, match.KindCustomListbox, labels...)
		q.InputType = match.InputSingleSelect
		return q
	}
	xcBRunCases(t, []xcBCase{
		{name: "n8n", sets: sc, facts: xcBFacts(), q: listbox(n8n...), entry: "address-state", label: "Other state"},
		{name: "the state is listed", sets: sc, facts: xcBFacts(), q: listbox(append(slices.Clone(n8n), "South Carolina")...), entry: "address-state", label: "South Carolina"},
		{name: "the state under another spelling", sets: sc, facts: xcBFacts(), q: listbox(append(slices.Clone(n8n), "South Carolina (Remote)")...), entry: "address-state", label: "South Carolina (Remote)"},
		{name: "no catch-all", sets: sc, facts: xcBFacts(), q: listbox("California", "Texas", "New York"), entry: "address-state", wantNone: true},
		{name: "two catch-alls", sets: sc, facts: xcBFacts(), q: listbox("California", "Texas", "Other state", "Other (outside the US)"), entry: "address-state", wantNone: true},
	})
}

// A Yes and a No that each say when to choose them are not a ladder: the
// condition can narrow the answer (a citizen-or-resident Yes for an applicant on
// OPT), so the label is read whole and a conditional Yes is left to the user.
// A Yes for anyone who holds a permit to work only restates the question
// (Etched: "YES: If you currently hold a valid work permit ..."), so the
// recorded answer takes it (owner decision).
func TestXC_AYesNoPairThatSaysWhenToChooseEachIsReadWhole(t *testing.T) {
	sets := map[string]xcBSet{"work-auth-us": xcBAns("Yes"), "sponsorship-now-or-future": xcBAns("Yes")}
	const q = "Are you legally authorized to work in the United States?"
	etched := xcBQ(q, match.KindRadioGroup, "YES: If you currently hold a valid work permit (such as OPT, CPT, or an existing H-1B transfer).", "NO: If you do not have any active, legal authorization to work in the country right now.")
	xcBRunCases(t, []xcBCase{
		{name: "a citizen-or-resident definition is not the bare Yes", sets: sets, facts: xcBFacts(),
			q: xcBQ(q, match.KindRadioGroup, "YES: If you are a U.S. citizen or permanent resident.", "NO: If you are not a U.S. citizen or permanent resident."), wantNone: true},
		{name: "Etched's work-permit definition", sets: sets, facts: xcBFacts(), q: etched, entry: "work-auth-us", label: "YES: If you currently hold a valid work permit (such as OPT, CPT, or an existing H-1B transfer)."},
		{name: "Etched's definition for an applicant with no permit", sets: map[string]xcBSet{"work-auth-us": xcBAns("No")}, facts: xcBFacts(), q: etched, entry: "work-auth-us", label: "NO: If you do not have any active, legal authorization to work in the country right now."},
		{name: "a permit held without sponsorship narrows the Yes", sets: sets, facts: xcBFacts(),
			q: xcBQ(q, match.KindRadioGroup, "YES: If you hold a valid work permit and will not need sponsorship.", "NO: If you do not have any active authorization to work here."), wantNone: true},
	})
}

// A box whose request carries its own condition (Avride: "If you require an
// accommodation ..., please describe the accommodation requested below.") asks
// the condition as its predicate: a maintained No makes the required box "N/A";
// a Yes, or no record, is not that case and the box keeps the readings it had.
func TestXC_ASelfGatedDescriptionUnderAMaintainedNo(t *testing.T) {
	const avride = "If you require an accommodation to participate in the application process or to perform the essential duties of the job, please describe the accommodation requested below. (Note: You are not required to disclose specific medical diagnoses)."
	box := xcBText(avride)
	box.InputType = match.InputLongText
	box.Required = true
	xcBRunCases(t, []xcBCase{
		{name: "no accommodation needed", sets: map[string]xcBSet{"accommodation-needed": xcBAns("No")}, facts: xcBFacts(), q: box, entry: "accommodation-needed", answer: "N/A"},
		{name: "an accommodation is needed", sets: map[string]xcBSet{"accommodation-needed": xcBAns("Yes")}, facts: xcBFacts(), q: box, wantNone: true},
		{name: "nothing recorded", sets: map[string]xcBSet{}, facts: xcBFacts(), q: box, wantNone: true},
	})
}

// A state's code is never a country's: "SC" is Seychelles and "CA" Canada by
// their ISO codes, so a residence question over a country list must not take
// either for an applicant in South Carolina or California; it takes the country.
// A state list that shows the code itself still takes it.
func TestXC_AStateCodeIsNeverACountry(t *testing.T) {
	countries := []string{"United States", "Canada", "United Kingdom", "Seychelles", "Germany", "India"}
	sc := map[string]xcBSet{"address-state": xcBPref("South Carolina", "South Carolina (SC)"), "address-country": xcBPref("United States"), "address-city": xcBAns("Charleston, South Carolina, United States")}
	ca := map[string]xcBSet{"address-state": xcBPref("California", "California (CA)"), "address-country": xcBPref("United States"), "address-city": xcBAns("San Jose, California, United States")}
	xcBRunCases(t, []xcBCase{
		{name: "South Carolina is not Seychelles", sets: sc, facts: xcBFacts(),
			q: xcBQ("Where do you currently live?", match.KindSelect, countries...), entry: "address-country", label: "United States"},
		{name: "California is not Canada", sets: ca, facts: xcBFacts(),
			q: xcBQ("Where do you currently live?", match.KindSelect, countries...), entry: "address-country", label: "United States"},
		{name: "a state list of codes takes the code", sets: sc, facts: xcBFacts(),
			q: xcBQ("State", match.KindSelect, "NC", "SC", "GA"), entry: "address-state", label: "SC"},
	})
}

// Starburst (Greenhouse): "Are you located in PST?" names one time zone; the
// recorded zone answers it. Working hours, a willingness, an alternative and a
// city are other questions.
func TestXC_LocatedInANamedTimeZone(t *testing.T) {
	sets := map[string]xcBSet{"time-zone": xcBPref("Eastern Time (ET)", "EST")}
	xcBRunCases(t, []xcBCase{
		{name: "PST for an applicant on Eastern time", sets: sets, facts: xcBFacts(), q: xcBYesNo("Are you located in PST?"), entry: "time-zone", label: "No"},
		{name: "the Pacific time zone", sets: sets, facts: xcBFacts(), q: xcBYesNo("Are you located in the Pacific time zone?"), entry: "time-zone", label: "No"},
		{name: "EST", sets: sets, facts: xcBFacts(), q: xcBYesNo("Are you based in EST?"), entry: "time-zone", label: "Yes"},
		{name: "Eastern Time", sets: sets, facts: xcBFacts(), q: xcBYesNo("Are you currently located in Eastern Time?"), entry: "time-zone", label: "Yes"},
		{name: "working hours are not a location", sets: sets, facts: xcBFacts(), q: xcBYesNo("Are you able to work PST hours?"), notEntry: "time-zone", wantNone: true},
		{name: "a city is not a zone", sets: sets, facts: xcBFacts(), q: xcBYesNo("Are you located in Seattle?"), notEntry: "time-zone", wantNone: true},
	})
}

// Datadog (Greenhouse): "authorised to work full-time in the country where
// this job is based" over a Yes, a Yes needing sponsorship later and a No
// needing it now: authorized now on OPT, needing sponsorship later.
func TestXC_AuthorisedToWorkFullTimeWithSponsorshipLater(t *testing.T) {
	sets := map[string]xcBSet{"work-auth-us": xcBAns("Yes"), "sponsorship-now-or-future": xcBAns("Yes")}
	ny := match.Posting{Location: "New York, New York, USA"}
	never := xcBFacts()
	never.RequiresSponsorshipFuture = xcBBool(false)
	xcBRunCases(t, []xcBCase{
		{name: "yes, sponsorship later", sets: sets, facts: xcBFacts(), posting: ny,
			q: xcBQ("Are you legally authorised to work full-time in the country where this job is based?", match.KindCustomListbox,
				"Yes, no restriction.", "Yes, but I will need sponsorship in the future.", "No, I need sponsorship now."),
			entry: "work-auth-us", label: "Yes, but I will need sponsorship in the future."},
		{name: "yes, never sponsorship", sets: sets, facts: never, posting: ny,
			q: xcBQ("Are you legally authorised to work full-time in the country where this job is based?", match.KindCustomListbox,
				"Yes, no restriction.", "Yes, but I will need sponsorship in the future.", "No, I need sponsorship now."),
			entry: "work-auth-us", label: "Yes, no restriction."},
	})
}

// Faire (Greenhouse demographic questions): a self-identification question
// drawn as a multi-value listbox takes the recorded choice, or the page's
// decline, as its one selection, as a select-all box group does.
func TestXC_SelfIdentificationInAMultiValueListbox(t *testing.T) {
	listbox := func(text string, labels ...string) match.Question {
		q := xcBQ(text, match.KindCustomListbox, labels...)
		q.Required, q.InputType = true, match.InputMultiSelect
		return q
	}
	xcBRunCases(t, []xcBCase{
		{name: "lgbtq recorded", sets: map[string]xcBSet{"eeo-lgbtq-identification": xcBPref("No")}, facts: xcBFacts(),
			q: listbox("Do you consider yourself a member of the LGBTQIA+ community?", "Yes", "No", "I don't wish to answer"), entry: "eeo-lgbtq-identification", label: "No"},
		{name: "veteran recorded", sets: map[string]xcBSet{"eeo-veteran": xcBPref("I am not a Veteran")}, facts: xcBFacts(),
			q: listbox("Do you identify as a military veteran or service member?", "Yes", "No", "I don't wish to answer"), entry: "eeo-veteran", label: "No"},
		{name: "gender declined", sets: map[string]xcBSet{"eeo-gender": xcBPref("Decline to self-identify")}, facts: xcBFacts(),
			q:     listbox("How do you currently describe your gender identity?", "Man, male or masculine", "Woman, female or feminine", "I prefer to self describe:", "I don't wish to answer"),
			entry: "eeo-gender", label: "I don't wish to answer"},
		// Instacart: "LGBT2QIA+" is the same question, and a race listbox whose
		// options carry their EEO-1 definitions takes the recorded whole label;
		// "Asian" alone never takes "Asian or Asian-American" (PO-018).
		{name: "lgbt2qia", sets: map[string]xcBSet{"eeo-lgbtq-identification": xcBPref("No")}, facts: xcBFacts(),
			q: xcBQ("Do you identify as a member of the LGBT2QIA+ community?", match.KindCustomListbox, "Yes", "No", "I don't wish to answer"), entry: "eeo-lgbtq-identification", label: "No"},
		{name: "race with definitions", sets: map[string]xcBSet{"eeo-race": xcBPref("Asian or Asian-American")}, facts: xcBFacts(),
			q: listbox("What is your race and/or ethnicity? Please check all that apply.",
				"White / Caucasian (A person having origins in any of the original peoples of Europe, the Middle East, or North Africa)",
				"Black or African-American (A person of African, Caribbean, West Indian descent)",
				"Asian or Asian-American (A person having origins in any of the original peoples of Asia and the Indian subcontinent including all 48 Asian countries - ie. Cambodia, China, India, Japan, Korea, Malaysia, Pakistan, the Philippine Islands, Thailand, Vietnam)",
				"A race, ethnicity, or origin not shown", "I don't wish to answer"),
			entry: "eeo-race", label: "Asian or Asian-American (A person having origins in any of the original peoples of Asia and the Indian subcontinent including all 48 Asian countries - ie. Cambodia, China, India, Japan, Korea, Malaysia, Pakistan, the Philippine Islands, Thailand, Vietnam)"},
	})
}

// Lyft (Greenhouse): a required choice whose only option thanks the applicant
// for following an instruction ("Please enter your relevant employment and
// military service above ...", "Thank you") takes it; it states nothing.
func TestXC_AChoiceWhoseOnlyOptionIsThanks(t *testing.T) {
	q := xcBQ("Please enter your relevant employment and military service above using the + Add Another Employment link.", match.KindCustomListbox, "Thank you")
	q.Required = true
	optional := q
	optional.Required = false
	xcBRunCases(t, []xcBCase{
		{name: "required", facts: xcBFacts(), q: q, label: "Thank you"},
		{name: "optional", facts: xcBFacts(), q: optional, wantNone: true},
	})
}

// DoorDash (Greenhouse, live): a race list whose rows carry their group as a
// prefix ("Asian: East Asian") is read without a prefix the row already
// names; "Asian: Chinese" names more, and "Asian Other" is another row.
func TestXC_ARaceRowPrefixedWithItsOwnGroup(t *testing.T) {
	q := xcBQ("Race (*Please select one option that best describes how you identify*)", match.KindSelect,
		"Asian: East Asian", "Asian: South Asian", "White", "Asian: Southeast Asian", "Asian Other", "Native Hawaiian or Other Pacific Islander",
		"Hispanic or Latino", "Middle Eastern / North African", "Black or African American", "American Indian or Alaska Native", "Two or More Races", "I don't wish to answer")
	xcBRunCases(t, []xcBCase{
		{name: "east asian", sets: map[string]xcBSet{"eeo-race": xcBPref("Asian: Chinese", "East Asian", "Asian")}, facts: xcBFacts(), q: q, entry: "eeo-race", label: "Asian: East Asian"},
		{name: "south asian", sets: map[string]xcBSet{"eeo-race": xcBPref("South Asian")}, facts: xcBFacts(), q: q, entry: "eeo-race", label: "Asian: South Asian"},
		{name: "a bare asian is no subgroup", sets: map[string]xcBSet{"eeo-race": xcBPref("Asian")}, facts: xcBFacts(), q: q, notEntry: "eeo-race"},
	})
}

// SoFi (Greenhouse, live): an adverb inside "to lawfully work" still asks the
// job's country; a date "of application" said as today's is today's, in the
// two-digit year the label names; and a license question whose condition is
// being employed by the company asks about licenses, not about the applicant's
// history with it.
func TestXC_SoFiWordings(t *testing.T) {
	auth := map[string]xcBSet{"work-auth-us": xcBAns("Yes"), "sponsorship-now-or-future": xcBAns("Yes")}
	sofi := match.Posting{Company: "SoFi", Location: "CA - San Francisco; WA - Seattle; UT - Cottonwood Heights"}
	xcBRunCases(t, []xcBCase{
		{name: "to lawfully work in the role's country", sets: auth, facts: xcBFacts(), posting: sofi,
			q: xcBYesNo("Are you authorized to lawfully work in the country where this role is located?"), entry: "work-auth-us", label: "Yes"},
		{name: "today's date of application in MM/DD/YY", facts: xcBFacts(), today: match.Date{Year: 2026, Month: 11, Day: 4},
			q: xcBText("Today's Date of Application (MM/DD/YY Format)"), answer: "11/04/26"},
		{name: "the date of a previous application is not today", facts: xcBFacts(),
			q: xcBText("Date of your previous application"), wantNone: true},
	})
	const finra = "Do you currently hold, or intend to hold, any FINRA licenses if employed by SoFi? (i.e., Series 7, Series 24, Series 27, Series 99, etc.)"
	if match.IsRelationshipQuestion(finra) || !match.IsRelationshipQuestion("Have you ever been employed by SoFi?") {
		t.Fatal("a condition of being employed read as the applicant's history, or the history question no longer read")
	}
}

// ZipRecruiter (Greenhouse, live): the professional years in a discipline,
// asked as ranges whose bounds are strict ("Less than 1 year", "More than 1
// year ... but less than 2 years"), take the recorded total years; a value on
// a bound is in the range that starts there.
func TestXC_ProfessionalYearsInADisciplineAsStrictRanges(t *testing.T) {
	q := xcBQ("How many years of professional experience do you have in software engineering?", match.KindCustomListbox,
		"Less than 1 year of professional experience", "More than 1 year of professional experience but less than 2 years",
		"More than 2 years of professional experience but less than 3 years", "More than 3 years of professional experience but less than 5 years",
		"More than 5 year of professional experience but less than 8 years", "More than 8 years of professional experience")
	years := func(n string) map[string]xcBSet {
		return map[string]xcBSet{"years-full-time-work-experience-number": xcBAns(n)}
	}
	xcBRunCases(t, []xcBCase{
		{name: "one year", sets: years("1"), facts: xcBFacts(), q: q, entry: "years-full-time-work-experience-number", label: "More than 1 year of professional experience but less than 2 years"},
		{name: "half a year", sets: years("0.5"), facts: xcBFacts(), q: q, entry: "years-full-time-work-experience-number", label: "Less than 1 year of professional experience"},
		{name: "ten years", sets: years("10"), facts: xcBFacts(), q: q, entry: "years-full-time-work-experience-number", label: "More than 8 years of professional experience"},
	})
}

// SoFi (Greenhouse, live): "Home Address CEP (Brazil Only)" is a box for
// applicants in Brazil; an applicant who lives in the United States leaves it
// blank, while one who lives in Brazil still answers it.
func TestXC_AFieldForAnotherCountryOnlyIsLeftBlank(t *testing.T) {
	us, br := xcBFacts(), xcBFacts()
	us.Address = match.AddressFact{Line1: "68 Calhoun St", City: "Charleston", Region: "SC", PostalCode: "29401", Country: "US"}
	br.Address = match.AddressFact{Line1: "Av. Paulista 1000", City: "Sao Paulo", Region: "SP", PostalCode: "01310-100", Country: "BR"}
	xcBRunCases(t, []xcBCase{
		{name: "a U.S. applicant", facts: us, q: xcBText("Home Address CEP (Brazil Only)"), wantNone: true},
		{name: "a U.S. applicant, for applicants", facts: us, q: xcBText("CPF (for Brazil applicants only)"), wantNone: true},
		{name: "a U.S. field for a U.S. applicant", facts: us, q: xcBText("Home Address Zip Code (US only)"), answer: "29401"},
		{name: "a Brazil applicant's CEP", facts: br, q: xcBText("Home Address CEP (Brazil Only)"), answer: "01310-100"},
	})
}

// The owner's defaults: the hiring company may contact the
// current employer, and every channel the recorded text consent allows is
// ticked (Lyft, Stripe and Elastic on Greenhouse, live).
func TestXC_EmployerContactAndCommunicationChannels(t *testing.T) {
	yes := map[string]xcBSet{"contact-current-employer": xcBAns("Yes"), "marketing-text-consent": xcBAns("Yes")}
	xcBRunCases(t, []xcBCase{
		{name: "Lyft: may we contact your current employer", sets: yes, facts: xcBFacts(),
			q: xcBQ("May we contact your current employer?", match.KindCustomListbox, "Yes", "No"), entry: "contact-current-employer", label: "Yes"},
		{name: "Stripe: a WhatsApp opt-in", sets: yes, facts: xcBFacts(),
			q: xcBQ("Do you opt-in to receive WhatsApp messages from Stripe Recruiting?", match.KindCustomListbox, "Yes", "No"), entry: "marketing-text-consent", label: "Yes"},
		{name: "obligations to a current employer are not the contact", sets: yes, facts: xcBFacts(),
			q: xcBYesNo("Do you have obligations to a current employer that would limit this role?"), wantNone: true, notEntry: "contact-current-employer"},
	})
	channels := []string{"Email", "Phone Call", "Text Message", "WhatsApp"}
	for _, c := range []struct {
		consent string
		want    []string
	}{
		{"Yes", channels},
		{"No", []string{"Email", "Phone Call"}},
	} {
		bank := xcBBank(t, map[string]xcBSet{"marketing-text-consent": xcBAns(c.consent)})
		m := match.New(bank, match.DefaultStages())
		resp, err := m.Match(match.Request{Host: "jobs.example.com", Facts: xcBFacts(), Today: xcBDay,
			Questions: []match.Question{xcBQ("How should we communicate with you?", match.KindCheckbox, channels...)}})
		if err != nil {
			t.Fatal(err)
		}
		r := resp.Results[0]
		if r.Outcome == match.OutcomeAbstained || r.EntryID != "marketing-text-consent" || !slices.Equal(r.ChosenLabels, c.want) {
			t.Errorf("text consent %s: %s entry=%s labels=%q reason=%s, want %q", c.consent, r.Outcome, r.EntryID, r.ChosenLabels, r.Reason, c.want)
		}
	}
}

// SoFi (Greenhouse, live): which FINRA licences the applicant holds, "if any",
// over "N/A" and each Series: a recorded No ticks N/A; a Yes leaves which ones
// to the user.
func TestXC_AListOfWhichOnesIfAnyTakesItsNoneOptionOnANo(t *testing.T) {
	q := xcBQ("What FINRA license(s), if any, do you currently hold?", match.KindCheckbox, "N/A", "Series 3 (S3)", "Series 7 (S7)", "Series 24 (S24)", "Series 63 (S63)")
	xcBRunCases(t, []xcBCase{
		{name: "no licence", sets: map[string]xcBSet{"finra-registered": xcBAns("No")}, facts: xcBFacts(), q: q, entry: "finra-registered", label: "N/A"},
		{name: "a licence", sets: map[string]xcBSet{"finra-registered": xcBAns("Yes")}, facts: xcBFacts(), q: q, wantNone: true},
	})
}

// New Relic (Greenhouse, live): "2+ years of professional experience with Go
// or Python" is a choice of fields; the recorded professional total below the
// floor answers No for every one of them, and nothing else answers it.
func TestXC_AYearsThresholdOverAChoiceOfFields(t *testing.T) {
	q := xcBQ("Do you have 2+ years of professional experience with Go or Python programming", match.KindSelect, "Yes", "No")
	total := func(n string) map[string]xcBSet {
		return map[string]xcBSet{"years-full-time-work-experience-number": xcBAns(n)}
	}
	xcBRunCases(t, []xcBCase{
		{name: "one year in total", sets: total("1"), facts: xcBFacts(), q: q, label: "No"},
		{name: "three years in total", sets: total("3"), facts: xcBFacts(), q: q, wantNone: true, notEntry: "years-full-time-work-experience-number"},
		{name: "experience that need not be professional", sets: total("1"), facts: xcBFacts(),
			q: xcBQ("Do you have 2+ years of experience with Go or Python?", match.KindSelect, "Yes", "No"), wantNone: true},
	})
}

// Elastic (Greenhouse, live): "In what countries do you have the unrestricted
// right to work?" in a text box, answered from the profile: every country of
// citizenship, and an authorization the right covers. A visa that needs
// sponsorship is no unrestricted right.
func TestXC_RightToWorkCountriesFromTheProfile(t *testing.T) {
	f := xcBFacts()
	f.Citizenships, f.AuthorizedCountries = []string{"CN"}, []string{"US"}
	f.RequiresSponsorshipNow, f.RequiresSponsorshipFuture = xcBBool(true), xcBBool(true)
	free := f
	free.RequiresSponsorshipNow, free.RequiresSponsorshipFuture = xcBBool(false), xcBBool(false)
	unknown := f
	unknown.Citizenships = nil
	unrestricted := xcBText("In what countries do you have the unrestricted right to work?")
	xcBRunCases(t, []xcBCase{
		{name: "unrestricted: the citizenship only", facts: f, q: unrestricted, entry: "right-to-work-countries", answer: "China"},
		{name: "any right: the citizenship and the authorization", facts: f, q: xcBText("In which countries are you authorized to work?"), entry: "right-to-work-countries", answer: "China, United States"},
		{name: "unrestricted, the US entry says Yes", facts: free, sets: map[string]xcBSet{"unrestricted-right-to-work-us": xcBAns("Yes")}, q: unrestricted, entry: "right-to-work-countries", answer: "China, United States"},
		{name: "unrestricted, nothing settles the US", facts: free, q: unrestricted, wantNone: true},
		{name: "no citizenship recorded", facts: unknown, q: unrestricted, wantNone: true},
		{name: "without sponsorship is unrestricted", facts: f, q: xcBText("In which countries are you authorized to work without sponsorship?"), entry: "right-to-work-countries", answer: "China"},
	})
}

// Lyft and Dropbox (Greenhouse's own form, live): the numbered employment
// rows take each job's own dates, never the latest school's; a current job
// ticks Current role and leaves its end boxes.
func TestXC_GreenhouseEmploymentRowsTakeTheirJobsDates(t *testing.T) {
	f := xcBFacts()
	f.Employment = []match.EmploymentFact{
		{Employer: "Northwind Labs", Title: "Software Engineer", Start: "2026-06", End: "present"},
		{Employer: "Example Institute of Sciences", Title: "Researcher Intern", Start: "2023-07", End: "2023-09"},
	}
	f.Education = []match.EducationFact{
		{School: "University of Westbridge", Start: "2020-09", End: "2024-06"},
		{School: "Carver University", Start: "2024-08", End: "2026-05"},
	}
	months := []string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}
	var qs []match.Question
	want := map[string]string{}
	for i, row := range [][]string{{"Northwind Labs", "Software Engineer", "June", "2026", "", "", "Yes"}, {"Example Institute of Sciences", "Researcher Intern", "July", "2023", "September", "2023", "No"}} {
		n := strconv.Itoa(i)
		for j, c := range []struct{ id, label string }{
			{"company-name-" + n, "Company name"}, {"title-" + n, "Title"}, {"start-date-month-" + n, "Start date month"}, {"start-date-year-" + n, "Start date year"},
			{"end-date-month-" + n, "End date month"}, {"end-date-year-" + n, "End date year"}, {"current-role-" + n + "_1", "Current role"},
		} {
			q := match.Question{Selector: `[id="` + c.id + `"]`, Text: c.label, Kind: match.KindText}
			switch {
			case strings.HasSuffix(c.label, "month"):
				q.Kind, q.Options = match.KindCustomListbox, xcBOpts(months...)
			case c.label == "Current role":
				q.Kind = match.KindCheckbox
			}
			qs = append(qs, q)
			want[q.Selector] = row[j]
		}
	}
	m := match.New(xcBBank(t, nil), match.DefaultStages())
	resp, err := m.Match(match.Request{Host: "job-boards.greenhouse.io", Facts: f, Today: xcBDay, Questions: qs})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range resp.Results {
		got := r.Answer
		if r.ChosenLabel != "" {
			got = r.ChosenLabel
		}
		if r.Outcome == match.OutcomeAbstained {
			got = ""
		}
		if got != want[r.Selector] {
			t.Errorf("%s = %q (%s %s), want %q", r.Selector, got, r.Outcome, r.Reason, want[r.Selector])
		}
	}
}

// GoFundMe (Greenhouse, live): a typed-signature box whose declaration
// mentions "additional information provided in my resume" is still the name
// box, not a prose ask.
func TestXC_ATypedSignatureOverALongDeclarationTakesTheName(t *testing.T) {
	q := xcBText("By typing my name below, I confirm that the facts set forth in this application, including any additional information provided in my resume or other supplementary materials, are true and complete to the best of my knowledge. I understand that if I am employed, false statements, omissions or misrepresentations may result in my dismissal.")
	q.Required = true
	xcBRunCases(t, []xcBCase{
		{name: "GoFundMe's typed signature", sets: map[string]xcBSet{"applicant-full-name": xcBAns("Ada Example")}, facts: xcBFacts(),
			q: q, entry: "applicant-full-name", answer: "Ada Example"},
		{name: "a prose ask that only mentions typing is still prose", sets: map[string]xcBSet{"applicant-full-name": xcBAns("Ada Example")}, facts: xcBFacts(),
			q: xcBText("Please share any additional information about your experience typing reports."), notEntry: "applicant-full-name", wantNone: true},
	})
}

// Smartsheet (Greenhouse, live): a select-all-that-apply race list whose rows
// list their members in brackets with no "including" ("East Asian (Chinese /
// Hong-Kong / Japanese ...)") takes the recorded group's row.
func TestXC_ARaceRowListingItsMembers(t *testing.T) {
	east := "East Asian (Chinese / Hong-Kong / Japanese / Korean / Mongolian / Tibetan / Taiwanese)"
	q := xcBQ("I identify my race / ethnicity as (select all that apply):", match.KindCustomListbox,
		"Alaska Native / American Indian / Indigenous/Aboriginal / Native American", "Black / African-American / Black-British / Caribbean", "Hispanic / Latinx", east,
		"Middle Eastern / North African", "Native Hawaiian / Pacific Islander", "South Asian (Bangladeshi / Bhutanese / Indian / Nepali / Pakistani / Sri Lankan)",
		"Southeast Asian (Burmese / Cambodian / Filipino / Hmong / Indonesian / Laotian / Malaysian / Mien / Singaporean / Thai / Vietnamese)", "White / European", "I don't wish to answer")
	q.InputType, q.Required = match.InputMultiSelect, true
	xcBRunCases(t, []xcBCase{
		{name: "east asian", sets: map[string]xcBSet{"eeo-race": xcBPref("Asian: Chinese", "East Asian", "Asian")}, facts: xcBFacts(), q: q, entry: "eeo-race", label: east},
		{name: "a bare asian is no listed group", sets: map[string]xcBSet{"eeo-race": xcBPref("Asian")}, facts: xcBFacts(), q: q, notEntry: "eeo-race"},
	})
}

// Block (Greenhouse, live): "authorized to work lawfully in the location
// posted" is the posting country's work authorization.
func TestXC_AuthorizedToWorkLawfullyInTheLocationPosted(t *testing.T) {
	q := xcBYesNo("Are you authorized to work lawfully in the location posted for this position?")
	sets := map[string]xcBSet{"work-auth-us": xcBAns("Yes")}
	xcBRunCases(t, []xcBCase{
		{name: "a US posting", sets: sets, facts: xcBFacts(), posting: match.Posting{Company: "Block", Location: "New York, NY, United States of America"}, q: q, entry: "work-auth-us", label: "Yes"},
	})
}

// Block (Greenhouse, live): a note on how the answer is used ("This
// information will not be used in assessing your qualifications") asks
// nothing, so its "not" never turns the question.
func TestXC_SponsorshipWithANoteOnHowTheAnswerIsUsed(t *testing.T) {
	q := xcBQ("Will you now or in the future require sponsorship for work authorization? (This information will not be used in assessing your qualifications for any position.)", match.KindCustomListbox, "Yes", "No")
	q.Required = true
	sp := "sponsorship-now-or-future"
	sets := map[string]xcBSet{sp: xcBAns("Yes")}
	ny := match.Posting{Company: "Block", Location: "New York, NY, United States of America"}
	noNeed := xcBFacts()
	noNeed.RequiresSponsorshipNow, noNeed.RequiresSponsorshipFuture = xcBBool(false), xcBBool(false)
	xcBRunCases(t, []xcBCase{
		{name: "a recorded need", sets: sets, facts: xcBFacts(), posting: ny, q: q, entry: sp, label: "Yes"},
		{name: "no need", sets: sets, facts: noNeed, posting: ny, q: q, entry: sp, label: "No"},
	})
}

// ZoomInfo (Greenhouse, live): "What is your Legal Last Name?" took the full
// name through the fuzzy rung. A part of the name is never the whole one; a
// question that asks for the whole name still takes it.
func TestXC_ANamePartIsNeverTheFullName(t *testing.T) {
	sets := map[string]xcBSet{"applicant-full-name": xcBAns("Ada Example")}
	xcBRunCases(t, []xcBCase{
		{name: "legal last name", sets: sets, facts: xcBFacts(), q: xcBText("What is your Legal Last Name?"), notEntry: "applicant-full-name", wantNone: true},
		{name: "legal first name", sets: sets, facts: xcBFacts(), q: xcBText("What is your Legal First Name?"), notEntry: "applicant-full-name", wantNone: true},
		{name: "full legal name", sets: sets, facts: xcBFacts(), q: xcBText("Full legal name"), entry: "applicant-full-name", answer: "Ada Example"},
	})
}

// Cerebras (Ashby, live): two options say "not a U.S. person" and part on
// citizenship or residence of the sanctioned countries they name; the
// recorded citizenships and home decide which, and nothing recorded leaves it.
func TestXC_NotAUSPersonPartedBySanctionedCountries(t *testing.T) {
	const ask = "In order to comply with these regulations (and for no other reason), we are asking you to confirm citizenship and permanent residency as defined below. Please check the field that applies to you:"
	opts := []string{"I am a U.S. person – this includes any individual who is a citizen of the United States, a permanent resident alien of the United States, or a protected individual as defined by 8 U.S.C. 1324b(a)(3).",
		"I am not a U.S. person, and I am not a current citizen or permanent resident of Cuba, Iran, North Korea, or Syria",
		"I am not a U.S. person, and I am a current citizen or permanent resident of Cuba, Iran, North Korea, or Syria"}
	q := xcBQ("Cerebras products are subject to the U.S. Export Administration Regulations (EAR) and may require a license to release technology to employees who are not U.S. persons. "+ask, match.KindRadioGroup, opts...)
	q.Required = true
	cn := xcBFacts()
	cn.Citizenships, cn.Address.Country = []string{"CN"}, "US"
	cn.RequiresSponsorshipNow, cn.RequiresSponsorshipFuture = xcBBool(true), xcBBool(true)
	ir := cn
	ir.Citizenships = []string{"IR"}
	none := cn
	none.Citizenships = nil
	xcBRunCases(t, []xcBCase{
		{name: "a Chinese citizen living in the US", facts: cn, q: q, entry: "us-person-itar-ear", label: opts[1]},
		{name: "an Iranian citizen", facts: ir, q: q, entry: "us-person-itar-ear", label: opts[2]},
		{name: "no citizenship recorded", facts: none, q: q, wantNone: true},
	})
}
