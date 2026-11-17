package match

import (
	"testing"
)

func sigTargets(q Question, existing ...string) []string {
	have := map[string]bool{}
	for _, id := range existing {
		have[id] = true
	}
	var out []string
	for _, s := range signatureCandidates(q, func(id string) bool { return len(existing) == 0 || have[id] }, nil) {
		out = append(out, s.Target)
	}
	return out
}

func sigQ(text string, labels ...string) Question {
	return Question{Selector: "s", Text: text, Kind: KindSelect, Options: opts(labels...)}
}

var countryLabels = []string{"Afghanistan", "Albania", "Algeria", "Andorra", "Angola", "Argentina", "Armenia", "Australia", "Austria", "Italy", "United States", "Other"}

func TestSignature_Table(t *testing.T) {
	cases := []struct {
		row, name string
		q         Question
		want      string // "" for none
	}{
		{"CL-036", "dial codes plus-first", sigQ("Please select", "+1 US - United States", "+44 GB - United Kingdom", "+61 AU - Australia", "+91 IN - India"), "phone-country-code"},
		{"CL-036", "dial codes plus-last", sigQ("Country", "Springfield +1", "Shelbyville +44", "Capital City +91"), "phone-country-code"},
		{"CL-036", "one stale placeholder never qualifies", sigQ("Phone", "+247 AC - Ascension"), ""},
		{"CL-036", "minority of dial codes", sigQ("Country", "+1 US", "France", "Spain", "Italy"), ""},
		{"CL-037", "lead times with immediately", sigQ("Start", "Immediately", "Two weeks notice", "1 month", "2 months"), "full-time-start-availability"},
		{"CL-037", "the notice period is its own entry's", sigQ("What is your notice period?", "Immediately", "Two weeks", "One month"), ""},
		// Groupon (Greenhouse, live): where the applicant lives, over countries.
		{"CL-037", "a country list under a residence question", sigQ("Do you currently reside where this role is based, or would you need to relocate?", countryLabels...), "address-country"},
		{"CL-037", "a country list under a relocation preference", sigQ("Which country would you like to relocate to?", countryLabels...), ""},
		{"CL-037", "work authorization statements that say now or in the future", sigQ("Which best describes your work authorization status for employment in the United States?",
			"I am currently legally authorized to work in the United States and do not require visa sponsorship now or in the future",
			"I am currently legally authorized to work in the United States but will require visa sponsorship now or in the future (for example, H 1B)",
			"I am not currently legally authorized to work in the United States"), ""},
		{"CL-037", "tenure list never offers immediately", sigQ("How long", "6 months", "1 year", "2 years"), ""},
		// Keystone (Greenhouse, live): months and terms under a start question.
		{"CL-037", "start months", sigQ("Please share when you are available to start at Keystone.", "As Soon as possible", "February 2026", "March 2026", "January - March 2027"), "full-time-start-availability"},
		{"CL-037", "graduation months are no start", sigQ("When do you expect to graduate?", "May 2026", "December 2026"), ""},
		{"CL-037", "a program start is no job start", sigQ("When will you start your graduate program?", "Fall 2026", "Spring 2027"), ""},
		{"CL-038", "data retention", sigQ("Keep my data", "Keep my data for 12 months", "Delete my data at the end of the application"), "data-retention-preference"},
		{"CL-038", "only one side", sigQ("x", "Keep my data for 12 months", "Something else"), ""},
		{"CL-039", "us work authorization status", sigQ("x", "I am authorized to work for any employer", "I am authorized for my present employer only"), "us-work-authorization-status"},
		{"CL-040", "office fit picker", sigQ("x", "I'm local and can come into the office", "I'm not local yet but willing to relocate", "I'm not able to work out of the office"), "relocation-or-local"},
		{"CL-040", "yes no is never an office picker", sigQ("x", "Yes", "No"), ""},
		// Rebar (Ashby, live): presence said as "located in".
		{"CL-040", "office fit picker in located wording", sigQ("x", "Yes, I'm located in New York and can accommodate", "No", "Yes, I'm looking to relocate to NYC", "Other - I'm open to discussing further"), "relocation-or-local"},
		{"CL-040", "relocated is no presence", sigQ("x", "I relocated in 2020", "I would relocate"), ""},
		// Unlock Health (Greenhouse, live; owner decision): "Yes, I am based in <office>" for each office.
		{"office picker", "based in each office", sigQ("Are you currently based near one of our offices in Chicago, Nashville, or New York?", "Yes, I am based in Chicago and am comfortable working from the Chicago office.", "Yes, I am based in New York and am comfortable working from the New York office.", "No, I am seeking a fully remote position."), "preferred-work-location"},
		{"office picker", "a place word that only points is no office", sigQ("x", "Yes, I am based in this area", "Yes, I am based in the area", "No"), ""},
		// Aquatic (Greenhouse, live): an experience title over year ranges.
		{"years ranges", "an experience title over year ranges", sigQ("Current Professional Experience", "Graduate (no full time experience)", "1-3", "4-10", "10+"), "years-full-time-work-experience-number"},
		{"years ranges", "a title that names no experience", sigQ("Team size", "1-3", "4-10", "10+"), ""},
		{"CL-041", "three-way sponsorship", sigQ("x", "I will require sponsorship in the future", "I will not require sponsorship", "I currently require sponsorship"), "work-auth-and-future-sponsorship"},
		{"CL-042", "two-way sponsorship", sigQ("x", "I will not require sponsorship", "I require sponsorship now"), "sponsorship-now-or-future"},
		{"CL-041", "three-way standings said with need (Partly)", sigQ("x", "Yes, I'm eligible and won't need sponsorship.", "Yes, I'm eligible now, but will need sponsorship in the future.", "No, I'm not eligible and will need sponsorship."), "work-auth-and-future-sponsorship"},
		{"CL-043", "military service status", sigQ("x", "Armed Forces service member", "NOAA Corps", "I have not served"), "military-service-status"},
		{"CL-043", "spouse options do not become service status", sigQ("x", "Yes, Active Duty Spouse", "No, not an Armed Forces Spouse", "Prefer not to identify"), ""},
		{"CL-044", "sms consent pair", sigQ("x", "Yes, I opt in to receive SMS messages", "No, do not send text messages"), "marketing-text-consent"},
		{"CL-044", "full-sentence consent pair", sigQ("x", "I consent to receive text messages about my application", "I do not consent to receive text messages"), "marketing-text-consent"},
		{"CL-044", "opt-in side alone", sigQ("x", "Yes, I opt in to receive SMS messages", "Maybe later"), ""},
		{"CL-045", "highest level of education", sigQ("Degree*", "High School", "Associate", "Bachelor's", "Master's", "Doctorate"), "highest-level-education"},
		{"CL-045 plural labels", "highest level of education", sigQ("Degree*", "Bachelors", "Masters", "Doctorate"), "highest-level-education"},
		{"CL-045", "currently pursuing is more specific", sigQ("Are you currently pursuing a degree?", "High School", "Associate", "Bachelor's", "Master's"), ""},
		{"CL-045", "two levels only", sigQ("Degree", "Bachelor's", "Master's"), ""},
		{"CL-046", "address type", sigQ("Type", "Home", "Work", "Other"), "address-type"},
		{"CL-046", "address type full text", sigQ("Address Type", "Select One", "Home", "Mailing"), "address-type"},
		{"CL-046", "any other question text", sigQ("What kind of place is it?", "Home", "Work", "Other"), ""},
		{"CL-046", "a label outside the set", sigQ("Type", "Home", "Work", "Cabin"), ""},
		{"CL-047", "referral source specify", sigQ("Please specify", "LinkedIn", "Indeed", "Glassdoor"), "referral-source-specify"},
		{"CL-047", "other text", sigQ("How did you hear about us?", "LinkedIn", "Indeed", "Glassdoor"), ""},
		{"CL-048", "cisgender pair", sigQ("I identify as:*", "Cisgender", "Transgender", "Decline"), "eeo-transgender"},
		{"CL-048", "lone transgender label", sigQ("I identify as:*", "Transgender", "Decline"), ""},
		{"CL-048", "a gender list beside cisgender and transgender options (Sony)", sigQ("Gender Identity", "Agender", "Cisgender/Female", "Cisgender/Male", "Female", "Male", "Transgender"), ""},
		{"CL-035", "plain yes no is never claimed", sigQ("Select One", "Select One", "Yes", "No"), ""},
		{"state list", "where you are located, US state names", sigQ("Where are you located? (There is a small list of states we do not hire in.)", "Select...", "Alabama", "Alaska", "Arizona", "California", "New York", "North Carolina", "South Carolina", "Texas", "Utah", "Virginia", "West Virgina"), "address-state"},
		{"state list", "current location, US state names (Found Energy)", sigQ("Current Location", "Not a current US resident", "Alabama", "Alaska", "Arizona", "California", "New York", "North Carolina", "South Carolina", "Texas", "Utah", "Virginia"), "address-state"},
		{"state list", "a location that is not the applicant's", sigQ("Preferred location", "Alabama", "Alaska", "Arizona", "California", "New York", "North Carolina", "South Carolina", "Texas", "Utah", "Virginia"), ""},
		{"state list", "state of residence, US state codes", sigQ("State of residence", "AL", "AK", "AZ", "CA", "NY", "NC", "SC", "TX", "UT", "VA"), "address-state"},
		{"state list", "a state you would move to is not where you live", sigQ("Which state are you willing to relocate to?", "Alabama", "Alaska", "Arizona", "California", "New York", "North Carolina", "South Carolina", "Texas", "Utah", "Virginia"), ""},
		{"state list", "too few states to be the list", sigQ("Where are you located?", "California", "Texas", "Other"), ""},
		{"work auth heading", "options state the US question", sigQ("Work Authorization", "Yes, I am authorized to work in the United States", "No, I am not authorized to work in the United States"), "work-auth-us"},
		{"work auth heading", "a question of its own is left to the routes", sigQ("Are you authorized to work for any employer?", "Yes, I am authorized to work in the United States", "No, I am not authorized to work in the United States"), ""},
		{"work auth heading", "sponsorship in an option is another question", sigQ("Work Authorization", "Yes, I am authorized to work in the United States without sponsorship", "No, I am not authorized to work in the United States"), ""},
	}
	for _, c := range cases {
		t.Run(c.row+"/"+c.name, func(t *testing.T) {
			got := sigTargets(c.q)
			if c.want == "" {
				if len(got) != 0 {
					t.Fatalf("fired %v", got)
				}
				return
			}
			if len(got) == 0 || got[0] != c.want {
				t.Fatalf("got %v, want %s first", got, c.want)
			}
		})
	}
}

func TestSignature_ClassifiedWhenQuestionTextIsMeaningless(t *testing.T) {
	es := []Entry{ent("phone-country-code", "pick-option")}
	q := sigQ("Please select", "+1 US", "+44 GB", "+49 DE")
	cl, _ := classifyWith(es, q, nil)
	if entryID(cl) != "phone-country-code" || cl.stage != stageSignature || cl.start != 0.95 {
		t.Fatalf("classification = %+v", cl)
	}
}
