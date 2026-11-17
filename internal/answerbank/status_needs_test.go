package answerbank

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/workspace"
)

// statusPersona is a profile as setup's steps 1 to 7 write it, with the
// work-authorization block left to each persona.
func statusPersona(workAuth string) string {
	return `version: 1
candidate:
  full_name: Alex Example
  email: alex@example.com
  phone: "+19195550100"
  address: {line1: 1 Example St, city: Durham, county: Durham County, region: NC, postal_code: "27701", country: US}
work_authorization:
` + workAuth + `targets:
  compensation: {currency: USD, period: year, minimum: 90000, target: 110000}
history:
  employment:
    - {employer: Example Corp, title: Analyst, start: "2024-07", end: present}
  education:
    - {school: Example University, degree: M.S., field: Computer Science, start: "2022-08", end: "2024-05"}
`
}

const (
	f1Persona = `  visa_status: "F-1 OPT"
  authorized_countries: [US]
  citizenships: [CN]
  requires_sponsorship_now: false
  requires_sponsorship_future: true
  security_clearance: none
`
	citizenPersona = `  visa_status: citizen
  authorized_countries: [US]
  citizenships: [US]
  requires_sponsorship_now: false
  requires_sponsorship_future: false
  security_clearance: none
`
	permanentResidentPersona = `  visa_status: permanent_resident
  authorized_countries: [US]
  citizenships: [IN]
  requires_sponsorship_now: false
  requires_sponsorship_future: false
  security_clearance: none
`
)

// setupNeeds seeds a fresh workspace with the profile and returns what setup's
// answer-bank step reads: the stamped statements and the blocking entries.
func setupNeeds(t *testing.T, profile string) (map[string]string, []string) {
	t.Helper()
	s, ws := newService(t)
	s.now = func() time.Time { return time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC) }
	writeFile(t, ws, workspace.FileProfile, profile)
	if _, err := s.Seed(); err != nil {
		t.Fatal(err)
	}
	r, err := s.Unanswered()
	if err != nil {
		t.Fatal(err)
	}
	var blocking []string
	for _, g := range r.Blocking() {
		blocking = append(blocking, g.ID)
	}
	return statementMap(t, profile, s.now()), blocking
}

// The owner's own case: an F-1 OPT applicant living in the US, a citizen of
// China. What setup states and what still stops it are pinned, so a change
// for other statuses cannot move them; the entries few applicants need
// (optionalIDs) are the only ones that left this list.
func TestSetupNeeds_F1OPTPersonaIsPinned(t *testing.T) {
	stated, blocking := setupNeeds(t, statusPersona(f1Persona))
	wantStated := map[string]string{
		"address-city":                        "identity:Durham",
		"address-country":                     "identity:United States",
		"address-county":                      "identity:Durham County",
		"address-state":                       "identity:North Carolina|North Carolina (NC)",
		"address-street":                      "identity:1 Example St",
		"address-zip":                         "identity:27701",
		"applicant-full-name":                 "identity:Alex Example",
		"based-in-us-or-canada":               "identity:Yes",
		"citizen-cuba-iran-north-korea-syria": "work-authorization:No",
		"cpt-opt-work-authorization":          "work-authorization:Yes",
		"current-based-country":               "identity:United States",
		"current-company":                     "cv:Example Corp",
		"current-job-title":                   "cv:Analyst",
		"currently-employed":                  "cv:Yes",
		"currently-employer-sponsored":        "work-authorization:No",
		"currently-enrolled-student":          "cv:No",
		"currently-in-opt":                    "work-authorization:Yes",
		"currently-living-us-or-canada":       "identity:Yes",
		"currently-located-outside-us":        "identity:No",
		"did-you-graduate":                    "cv:Yes",
		"email-address":                       "identity:alex@example.com",
		"field-of-study-choice":               "cv:Computer Science",
		"graduation-year-month":               "cv:May 2024",
		"h1b-sponsorship":                     "work-authorization:Yes",
		"has-stem-degree":                     "cv:Yes",
		"highest-level-education":             "cv:Master's Degree|Master's",
		"identity-verification":               "identity:Yes",
		"most-recent-school":                  "cv:Example University",
		"permanent-work-authorization":        "work-authorization:No",
		"phone-country-code":                  "identity:+1 US - United States|United States|+1",
		"phone-mobile":                        "identity:+19195550100",
		"preferred-full-name":                 "identity:Alex Example",
		"salary-currency":                     "targets:USD",
		"security-clearance":                  "work-authorization:None",
		"us-based-with-work-permit":           "work-authorization:Yes",
		"us-citizen-lpr-asylee-refugee":       "work-authorization:No",
		"us-person-itar-ear":                  "work-authorization:No",
		"work-auth-based-on-visa-spouse":      "work-authorization:No",
		"work-auth-us":                        "work-authorization:Yes",
		"work-country":                        "identity:United States",
	}
	if !reflect.DeepEqual(stated, wantStated) {
		t.Errorf("statements\n got %v\nwant %v", stated, wantStated)
	}
	wantBlocking := []string{"application-submitter-identity", "date-of-birth", "preferred-name", "salutation", "ok-to-contact", "phone-device-type",
		"preferred-method-of-contact", "address-type", "based-in-named-metro",
		"time-zone", "commitments-to-another-employer", "desired-career-level", "employer-industry",
		"employer-position-function", "employer-relationship-with-company", "former-employer", "previously-interviewed-company",
		"academic-transcripts-shareable", "latest-gpa-score", "undergraduate-major", "undergraduate-school", "citizenship-in-another-country",
		"us-citizen-status", "ever-held-j1-status", "held-h1b-past-6-years", "security-clearance-history",
		"work-authorization-other-countries", "sponsorship-other-countries", "active-immigration-case", "stem-opt-24-month-extension",
		"stem-opt-extension-expiry", "unrestricted-right-to-work-us", "us-work-authorization-status",
		"work-auth-us-any-employer", "work-authorization-expiry", "comfortable-with-posted-range", "hourly-pay-rate",
		"salary-aligns", "salary-expectation", "salary-range-basis", "total-cash-compensation", "current-salary"}
	if !slices.Equal(blocking, wantBlocking) {
		t.Errorf("blocking\n got %q\nwant %q", blocking, wantBlocking)
	}
}

// A US citizen's or permanent resident's recorded status settles the US
// immigration questions it answers truthfully for every holder; the history
// questions it does not settle (a naturalized citizen or a green-card holder
// may once have held H-1B or J-1 status, a permanent resident may have a
// naturalization case open) never stop setup, nor do the entries few
// applicants need (optionalIDs; the F-1 pin covers that status).
func TestSetupNeeds_CitizenAndPermanentResidentAreNotAskedWhatTheirStatusSettles(t *testing.T) {
	history := []string{"held-h1b-past-6-years", "ever-held-j1-status", "willing-to-obtain-clearance", "clearance-citizenship-gated",
		"countries-lived-outside-us", "lived-outside-us-12-consecutive-months", "live-in-listed-states", "under-18-work-permit"}
	cases := []struct {
		name, workAuth string
		want           map[string]string
		unasked        []string
	}{
		{"citizen", citizenPersona, map[string]string{
			"us-citizen-status":       "work-authorization:U.S. citizen|US Citizen|U.S. Citizen|Citizen",
			"active-immigration-case": "work-authorization:No",
		}, history},
		{"permanent resident", permanentResidentPersona, map[string]string{
			"us-citizen-status": "work-authorization:Lawful permanent resident|Permanent Resident|Green Card Holder",
		}, append(slices.Clone(history), "active-immigration-case")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stated, blocking := setupNeeds(t, statusPersona(c.workAuth))
			for id, v := range c.want {
				if stated[id] != v {
					t.Errorf("%s = %q, want %q", id, stated[id], v)
				}
				if slices.Contains(blocking, id) {
					t.Errorf("%s blocks although the status states it", id)
				}
			}
			for _, id := range c.unasked {
				if _, ok := stated[id]; ok {
					t.Errorf("%s is stated %q: the status does not settle it", id, stated[id])
				}
				if slices.Contains(blocking, id) {
					t.Errorf("%s blocks setup", id)
				}
			}
		})
	}
}

// Setup's answer-bank step writes a pay expectation as a bare figure; the
// targets step's currency is its denomination, so a question naming that
// currency is answered, and a save never writes the stamped currency.
func TestStamp_SalaryExpectationTakesThePayCurrency(t *testing.T) {
	s, ws := newService(t)
	writeFile(t, ws, workspace.FileProfile, statusPersona(f1Persona))
	if _, err := s.Seed(); err != nil {
		t.Fatal(err)
	}
	file, err := s.File()
	if err != nil {
		t.Fatal(err)
	}
	for i := range file.Bank.Entries {
		if e := &file.Bank.Entries[i]; e.ID == "salary-expectation" {
			e.DefaultAnswer, e.Placeholder = "120000", false
		}
	}
	if _, err := s.Save(file.Bank, ""); err != nil {
		t.Fatal(err)
	}
	stamped, err := s.Bank()
	if err != nil {
		t.Fatal(err)
	}
	r := matchFacts(t, stamped.Bank, match.Facts{}, match.Date{Year: 2026, Month: 10, Day: 17},
		match.Question{Selector: "#q", Kind: match.KindText, InputType: match.InputText, Text: "What are your salary expectations in USD?"})
	if r.EntryID != "salary-expectation" || r.Answer != "120000" {
		t.Fatalf("a question naming the recorded currency: %+v", r)
	}
	if _, err := s.Save(stamped.Bank, ""); err != nil {
		t.Fatal(err)
	}
	reloaded, err := s.File()
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := reloaded.Bank.Find("salary-expectation"); e.Currency != "" || e.DefaultAnswer != "120000" {
		t.Fatalf("the save wrote currency %q, figure %q", e.Currency, e.DefaultAnswer)
	}
}
