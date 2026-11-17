package answerbank

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/workspace"
)

func TestPreferencesFrom_TravelCeilingValidation(t *testing.T) {
	cases := []struct {
		value string
		valid bool
	}{{"0", true}, {"25", true}, {"100", true}, {"-1", false}, {"101", false}, {"25.5", false}, {"lots", false}}
	for _, c := range cases {
		t.Run(c.value, func(t *testing.T) {
			p, rep := workspace.ParseProfile([]byte("version: 1\npreferences:\n  max_travel_percent: " + c.value + "\n"))
			if rep.OK() != c.valid {
				t.Fatalf("profile validation of %s: %s", c.value, rep)
			}
			if !c.valid && !strings.Contains(fmt.Sprint(rep), "max_travel_percent") {
				t.Fatalf("the error must name the field: %s", rep)
			}
			if c.valid && (preferencesFrom(p).MaxTravelPercent == nil) {
				t.Fatalf("a valid ceiling %s is dropped", c.value)
			}
		})
	}
	// A value that got past the schema (a hand-built profile) is dropped on
	// read, so the matcher never sees an invalid ceiling.
	for _, v := range []int{-1, 101} {
		var p workspace.Profile
		p.Preferences.MaxTravelPct = &v
		if got := preferencesFrom(p).MaxTravelPercent; got != nil {
			t.Fatalf("out-of-range ceiling %d reached the matcher: %d", v, *got)
		}
	}
}

func TestPreferencesFrom_OfficeDaysValidation(t *testing.T) {
	cases := []struct {
		value string
		valid bool
	}{{"0", true}, {"3", true}, {"7", true}, {"-1", false}, {"8", false}, {"2.5", false}}
	for _, c := range cases {
		t.Run(c.value, func(t *testing.T) {
			p, rep := workspace.ParseProfile([]byte("version: 1\npreferences:\n  max_days_in_office: " + c.value + "\n"))
			if rep.OK() != c.valid {
				t.Fatalf("profile validation of %s: %s", c.value, rep)
			}
			if c.valid && preferencesFrom(p).MaxDaysInOffice == nil {
				t.Fatalf("a valid ceiling %s is dropped", c.value)
			}
		})
	}
	for _, v := range []int{-1, 8} {
		var p workspace.Profile
		p.Preferences.MaxDaysInOffice = &v
		if preferencesFrom(p).MaxDaysInOffice != nil {
			t.Fatalf("out-of-range days %d reached the matcher", v)
		}
	}
}

func statementMap(t *testing.T, profile string, now time.Time) map[string]string {
	t.Helper()
	p, rep := workspace.ParseProfile([]byte(profile))
	if !rep.OK() {
		t.Fatal(rep)
	}
	out := map[string]string{}
	for _, s := range statementsFrom(p, now) {
		v := s.Body.Answer
		if s.Kind == match.KindPickOption {
			v = strings.Join(s.Body.Preferred, "|")
		}
		if _, dup := out[s.ID]; dup {
			t.Fatalf("%s is stated twice", s.ID)
		}
		out[s.ID] = s.Step + ":" + v
	}
	return out
}

func TestStatementsFrom_SetupAnswersAreNotAskedAgain(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	h1b := `version: 1
candidate:
  date_of_birth: 1998-04-02
  address: {city: Durham, region: NC, country: US}
work_authorization:
  visa_status: "work_visa: H-1B"
  authorized_countries: [US]
  requires_sponsorship_now: true
  requires_sponsorship_future: true
  security_clearance: none
targets:
  compensation: {currency: USD, period: year, minimum: 120000, target: 150000}
history:
  employment:
    - {employer: Bluefin Analytics, title: Engineer, start: 2023-01, end: present}
    - {employer: Larkspur Health, title: Intern, start: 2022-06, end: 2022-08}
  education:
    - {school: Purdue University, degree: B.S., field: Computer Engineering, start: 2017-08, end: 2021-05}
    - {school: Carver University, degree: M.S., field: Computer Science, start: 2021-08, end: 2023-05, grade_average: "3.8"}
`
	want := map[string]string{
		"current-based-country":          "identity:United States",
		"currently-living-us-or-canada":  "identity:Yes",
		"based-in-us-or-canada":          "identity:Yes",
		"currently-located-outside-us":   "identity:No",
		"us-based-with-work-permit":      "work-authorization:Yes",
		"security-clearance":             "work-authorization:None",
		"work-auth-us":                   "work-authorization:Yes",
		"h1b-sponsorship":                "work-authorization:Yes",
		"permanent-work-authorization":   "work-authorization:No",
		"us-citizen-lpr-asylee-refugee":  "work-authorization:No",
		"us-person-itar-ear":             "work-authorization:No",
		"work-auth-us-any-employer":      "work-authorization:No",
		"unrestricted-right-to-work-us":  "work-authorization:No",
		"work-auth-based-on-visa-spouse": "work-authorization:No",
		"held-h1b-past-6-years":          "work-authorization:Yes",
		"currently-employer-sponsored":   "work-authorization:Yes",
		"currently-in-opt":               "work-authorization:No",
		"cpt-opt-work-authorization":     "work-authorization:No",
		"address-city":                   "identity:Durham",
		"address-state":                  "identity:North Carolina|North Carolina (NC)",
		"address-country":                "identity:United States",
		"current-job-title":              "cv:Engineer",
		"current-company":                "cv:Bluefin Analytics",
		"currently-employed":             "cv:Yes",
		"currently-enrolled-student":     "cv:No",
		"graduation-year-month":          "cv:May 2023",
		"most-recent-school":             "cv:Carver University",
		"latest-gpa-score":               "cv:3.8",
		"undergraduate-school":           "cv:Purdue University",
		"salary-currency":                "targets:USD",
		"work-country":                   "identity:United States",
		"did-you-graduate":               "cv:Yes",
		"highest-level-education":        "cv:Master's Degree|Master's",
		"undergraduate-major":            "cv:Computer Engineering",
		"field-of-study-choice":          "cv:Computer Science",
		"has-stem-degree":                "cv:Yes",
	}
	if got := statementMap(t, h1b, now); !reflect.DeepEqual(got, want) {
		t.Fatalf("statements\n got %v\nwant %v", got, want)
	}

	// A citizen whose only country is the US has permanent, unrestricted
	// authorization; a degree still running makes the user a student.
	citizen := `version: 1
work_authorization:
  visa_status: citizen
  authorized_countries: [US]
  requires_sponsorship_now: false
  requires_sponsorship_future: false
history:
  education:
    - {school: State University, degree: B.A., end: 2027-05}
`
	got := statementMap(t, citizen, now)
	for id, v := range map[string]string{"work-auth-us": "Yes", "h1b-sponsorship": "No", "permanent-work-authorization": "Yes", "us-citizen-lpr-asylee-refugee": "Yes",
		"work-auth-us-any-employer": "Yes", "currently-in-opt": "No", "currently-employer-sponsored": "No", "work-auth-based-on-visa-spouse": "No", "currently-enrolled-student": "Yes"} {
		if got[id] != "work-authorization:"+v && got[id] != "cv:"+v {
			t.Fatalf("citizen %s = %q, want %s", id, got[id], v)
		}
	}
	// A citizen who lists another country too may be that country's citizen:
	// the status is not read as US citizenship.
	dual := strings.Replace(citizen, "[US]", "[US, CA]", 1)
	if v, ok := statementMap(t, dual, now)["us-citizen-lpr-asylee-refugee"]; ok {
		t.Fatalf("dual listing derived %q", v)
	}

	// A student authorization's end date is its expiry; the phone's code and
	// the recorded citizenships answer the dialing code and export control.
	student := `version: 1
candidate:
  phone: "+12065550100"
  address: {country: US}
work_authorization:
  visa_status: "student_work_authorization: 2027-06-30"
  authorized_countries: [US]
  citizenships: [CA]
`
	got = statementMap(t, student, now)
	for id, v := range map[string]string{"work-authorization-expiry": "work-authorization:2027-06-30", "citizen-cuba-iran-north-korea-syria": "work-authorization:No",
		"phone-country-code": "identity:+1 US - United States|United States|+1"} {
		if got[id] != v {
			t.Fatalf("student %s = %q, want %s", id, got[id], v)
		}
	}

	// A wording the user wrote names its training; future sponsorship left
	// unanswered still decides yes from the current need, never no.
	opt := "version: 1\nwork_authorization:\n  visa_status: F-1 OPT\n  authorized_countries: [US]\n  requires_sponsorship_now: true\n"
	got = statementMap(t, opt, now)
	for id, v := range map[string]string{"currently-in-opt": "Yes", "cpt-opt-work-authorization": "Yes", "h1b-sponsorship": "Yes", "permanent-work-authorization": "No", "currently-employer-sponsored": "No"} {
		if got[id] != "work-authorization:"+v {
			t.Fatalf("F-1 OPT %s = %q, want %s", id, got[id], v)
		}
	}
	unknown := "version: 1\nwork_authorization:\n  visa_status: F-1 OPT\n  authorized_countries: [US]\n  requires_sponsorship_now: false\n"
	if v, ok := statementMap(t, unknown, now)["h1b-sponsorship"]; ok {
		t.Fatalf("an unanswered future need derived %q", v)
	}

	// An H-4 EAD is a spouse's status: nothing about employer ties or the
	// spouse question is derived from it. Nothing is derived from an empty
	// profile, and the date of birth never is.
	h4 := strings.Replace(h1b, `"work_visa: H-1B"`, `"H-4 EAD"`, 1)
	got = statementMap(t, h4, now)
	notFromH4 := map[string]bool{
		"work-auth-based-on-visa-spouse": true,
		"work-auth-us-any-employer":      true,
		"held-h1b-past-6-years":          true,
		"currently-in-opt":               true,
		"currently-employer-sponsored":   true,
	}
	for id := range notFromH4 {
		if v, ok := got[id]; ok {
			t.Fatalf("H-4 EAD derived %s = %q", id, v)
		}
	}
	if got := statementMap(t, "version: 1\n", now); len(got) != 0 {
		t.Fatalf("an empty profile states %v", got)
	}
	for _, p := range []string{h1b, citizen, opt} {
		if _, ok := statementMap(t, p, now)["date-of-birth"]; ok {
			t.Fatal("the date of birth reached the bank")
		}
	}
}

// Finance, life-science and engineering applicants write degrees as
// abbreviations (BBA, B.Tech, PharmD, MFin) and fields outside computing. Setup
// reads the level, the undergraduate major and a STEM field on the DHS STEM
// Designated Degree Program List, so none of them blocks; a field off the list
// (finance, accounting, economics, business) leaves the STEM question to the
// user. The fill reads the level from the same rows.
func TestStatementsFrom_DegreeAbbreviationsAndDesignatedSTEMFields(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	today := match.Date{Year: 2026, Month: 10, Day: 17}
	bank := defaultBank(t)
	ladder := []string{"High School", "Bachelor's Degree", "Master's Degree", "Doctorate"}
	cases := []struct {
		bachelor, major, top, topField string
		level                          string // the rung of ladder both setup and the fill choose
		stem                           bool
	}{
		{"BBA", "Business Administration", "MFin", "Finance", "Master's Degree", false},
		{"B.Com", "Accounting", "MAcc", "Accounting", "Master's Degree", false},
		{"B.A.", "Economics", "JD", "Law", "Doctorate", false},
		{"B.Sc.", "Biology", "PharmD", "Pharmacy", "Doctorate", true},
		{"BSc", "Biochemistry", "M.Phil", "Neuroscience", "Master's Degree", true},
		{"BA", "Molecular and Cell Biology", "MD", "Medicine", "Doctorate", true},
		{"B.Tech", "Mechanical Engineering", "M.Tech", "Industrial Engineering", "Master's Degree", true},
		{"B.E.", "Chemical Engineering", "MPH", "Epidemiology", "Master's Degree", true},
		{"BS", "Finance", "MSF", "Quantitative Finance", "Master's Degree", true},
		{"BS", "Economics", "M.Res", "Econometrics", "Master's Degree", true},
		{"BCom", "Finance", "DVM", "Veterinary Medicine", "Doctorate", false},
	}
	for _, c := range cases {
		t.Run(c.bachelor+"+"+c.top, func(t *testing.T) {
			profile := fmt.Sprintf(`version: 1
history:
  education:
    - {school: Example College, degree: %q, field: %q, start: 2015-09, end: 2019-05}
    - {school: Example University, degree: %q, field: %q, start: 2019-09, end: 2023-05}
`, c.bachelor, c.major, c.top, c.topField)
			got := statementMap(t, profile, now)
			if lvl := got["highest-level-education"]; !strings.HasPrefix(lvl, "cv:"+c.level) {
				t.Errorf("highest-level-education = %q, want %s", lvl, c.level)
			}
			if got["undergraduate-major"] != "cv:"+c.major {
				t.Errorf("undergraduate-major = %q, want %s", got["undergraduate-major"], c.major)
			}
			if stem := got["has-stem-degree"]; (stem == "cv:Yes") != c.stem {
				t.Errorf("has-stem-degree = %q, want stated %v", stem, c.stem)
			}
			p, _ := workspace.ParseProfile([]byte(profile))
			stamped := bank.Stamp(match.Preferences{Statements: statementsFrom(p, now)})
			blocking := map[string]bool{}
			for _, n := range match.NeedsAnswer(stamped).Blocking() {
				blocking[n.ID] = true
			}
			if blocking["highest-level-education"] || blocking["undergraduate-major"] || blocking["has-stem-degree"] == c.stem {
				t.Errorf("blocking after setup: level %v, major %v, stem %v", blocking["highest-level-education"], blocking["undergraduate-major"], blocking["has-stem-degree"])
			}
			facts := match.Facts{Education: []match.EducationFact{
				{School: "Example College", Degree: c.bachelor, Field: c.major, Start: "2015-09", End: "2019-05"},
				{School: "Example University", Degree: c.top, Field: c.topField, Start: "2019-09", End: "2023-05"},
			}}
			pick := func(labels ...string) match.Result {
				return matchFacts(t, stamped, facts, today, match.Question{Selector: "#edu", Text: "What is your highest level of education?",
					Kind: match.KindSelect, InputType: match.InputSingleSelect, Options: opts(labels...)})
			}
			if r := pick(ladder...); r.ChosenLabel != c.level {
				t.Errorf("the fill chose %q, want %s: %+v", r.ChosenLabel, c.level, r)
			}
			yesNo := func(text string) match.Result {
				return matchFacts(t, stamped, facts, today, match.Question{Selector: "#q", Text: text, Kind: match.KindRadioGroup, Options: opts("Yes", "No")})
			}
			// A professional doctorate is no Ph.D.
			if c.level == "Doctorate" {
				for _, labels := range [][]string{{"Bachelor's Degree", "Master's Degree", "Ph.D."}, {"Bachelor's Degree", "Master's Degree", "Professional Degree (JD, MD)", "Ph.D."}} {
					if r := pick(labels...); r.ChosenLabel == "Ph.D." {
						t.Errorf("a %s chose Ph.D.: %+v", c.top, r)
					}
				}
				if r := yesNo("Do you hold a PhD?"); r.ChosenLabel == "Yes" {
					t.Errorf("a %s holds a PhD: %+v", c.top, r)
				}
			}
			// The stated STEM degree answers a STEM question, never one that
			// names fields the applicant did not study.
			if r := yesNo("Do you have a STEM degree?"); c.stem && r.ChosenLabel != "Yes" {
				t.Errorf("a STEM degree: %+v", r)
			}
			if r := yesNo("Do you have a Bachelor's degree in Computer Science, Statistics or Mathematics?"); r.ChosenLabel == "Yes" {
				t.Errorf("a degree in fields not studied answered Yes: %+v", r)
			}
		})
	}
}

func TestAuthStatus_AWordingNamesAStatusOnlyWhenItNamesExactlyOne(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	for v, want := range map[string]string{
		"citizen":                           "citizen",
		"citizen: naturalized":              "citizen",
		"work_visa: H-1B":                   "work_visa",
		"Citizen":                           "citizen",
		"U.S. citizen":                      "citizen",
		"Citizen of the United States":      "citizen",
		"Green card holder":                 "permanent_resident",
		"F-1 OPT":                           "student_work_authorization",
		"H-1B":                              "work_visa",
		"H-4 EAD":                           "work_visa",
		"Not authorized to work in the US":  "not_authorized",
		"Canadian citizen, TN":              "",
		"Canadian citizen":                  "",
		"not a US citizen (H-1B)":           "",
		"non-citizen":                       "",
		"F-1 OPT, H-1B next":                "",
		"Green card pending":                "",
		"no longer on F-1":                  "",
		"not authorized, U.S. citizen soon": "",
		"Martian pass":                      "",
	} {
		got, ok := AuthStatus(v)
		if got != want || ok != (want != "") {
			t.Errorf("AuthStatus(%q) = %q, %v; want %q", v, got, ok, want)
		}
	}
	// A wording that names no single status stamps nothing on the citizen and
	// U.S.-person entries.
	for _, v := range []string{"Canadian citizen, TN", "not a US citizen (H-1B)", "Canadian citizen"} {
		got := statementMap(t, "version: 1\nwork_authorization:\n  visa_status: \""+v+"\"\n  authorized_countries: [US]\n", now)
		for _, id := range []string{"us-citizen-lpr-asylee-refugee", "us-person-itar-ear", "permanent-work-authorization", "unrestricted-right-to-work-us"} {
			if a, ok := got[id]; ok {
				t.Errorf("%q derived %s = %q", v, id, a)
			}
		}
	}
}

const identityProfile = `version: 1
candidate:
  full_name: Alex Example
  preferred_name: Alex
  email: alex@example.com
  phone: "+1 555 0100"
  address:
    line1: "@ask-me"
    city: Exampleville
    county: Sample County
links:
  linkedin: linkedin.com/in/alex-example
  github: https://github.com/alex-example
history:
  employment:
    - {employer: Old Corp, title: Analyst, start: "2019-01", end: "2021-06"}
    - {employer: Example Corp, title: Engineer, start: "2021-07", end: present}
    - {employer: old corp, title: Intern, start: "2018-01", end: "2018-06"}
`

// identityBankText holds the identity entries as blank placeholders, with a
// comment a write must keep.
func identityBankText() string {
	var b strings.Builder
	b.WriteString("# my bank\nversion: 1\nemployers:\n  - name: Old Corp\n    aliases: [OC]\nentries:\n")
	for _, id := range []string{"applicant-full-name", "preferred-name", "email-address", "phone-mobile", "linkedin-url", "github-url", "portfolio-url", "address-city", "address-county", "address-street", "current-job-title", "identity-verification"} {
		b.WriteString("  - id: " + id + "\n    description: d\n    kind: fixed\n    answer: \"\"\n    placeholder: true\n")
	}
	b.WriteString("  - id: unrelated\n    description: d\n    kind: fixed\n    answer: keep\n  # trailing note\noverrides: {}\n")
	return b.String()
}

// The full name the applicant goes by is the full name, with the preferred
// first name in its first name's place when the profile records one (AmEx's
// required "Preferred Full Name"); an answer the user wrote wins.
func TestStatementsFrom_PreferredFullNameIsTheNameTheApplicantGoesBy(t *testing.T) {
	for _, tc := range []struct{ name, profile, bank, want string }{
		{"a preferred first name", "version: 1\ncandidate:\n  full_name: Alexandra Q. Example\n  preferred_name: Alex\n", "version: 1\n", "identity:Alex Example"},
		{"no preferred name", "version: 1\ncandidate:\n  full_name: Alexandra Example\n", "version: 1\n", "identity:Alexandra Example"},
		{"the user's own answer", "version: 1\ncandidate:\n  full_name: Alexandra Example\n  preferred_name: Alex\n",
			"version: 1\nentries:\n  - id: preferred-full-name\n    description: d\n    kind: fixed\n    answer: Sandra Example\n", ":Sandra Example"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, ws := newService(t)
			writeFile(t, ws, workspace.FileProfile, tc.profile)
			writeFile(t, ws, bankFile, tc.bank)
			l, err := s.Bank()
			if err != nil {
				t.Fatal(err)
			}
			e, _ := l.Bank.Find("preferred-full-name")
			if got := e.DerivedFrom + ":" + e.Answer; got != tc.want {
				t.Fatalf("preferred-full-name = %q, want %q", got, tc.want)
			}
		})
	}
}

// The identity step's values answer the identity entries at every load, the
// way every other setup statement does: nothing is copied into the file, a
// profile edit is followed on the next load, and an answer or ask-me the user
// wrote in the bank wins.
func TestStatementsFrom_IdentityIsStampedLive(t *testing.T) {
	s, ws := newService(t)
	writeFile(t, ws, workspace.FileProfile, identityProfile)
	writeFile(t, ws, bankFile, identityBankText())
	stamped := func() match.Bank {
		t.Helper()
		l, err := s.Bank()
		if err != nil {
			t.Fatal(err)
		}
		return l.Bank
	}
	b := stamped()
	for id, want := range map[string]string{
		"applicant-full-name": "identity:Alex Example", "preferred-name": "identity:Alex", "email-address": "identity:alex@example.com",
		"phone-mobile": "identity:+1 555 0100", "linkedin-url": "identity:https://www.linkedin.com/in/alex-example/",
		"github-url": "identity:https://github.com/alex-example", "address-city": "identity:Exampleville", "address-county": "identity:Sample County",
		"address-street": "identity:" + match.AskMe, "current-job-title": "cv:Engineer",
		// The owner's rule: the person the profile names can verify who they are.
		"identity-verification": "identity:Yes",
		"portfolio-url":         ":", // the profile holds no portfolio
		"unrelated":             ":keep",
	} {
		e, _ := b.Find(id)
		if got := e.DerivedFrom + ":" + e.Answer; got != want {
			t.Errorf("%s = %q, want %q", id, got, want)
		}
	}
	rep := match.NeedsAnswer(b)
	rep.Unanswered = slices.DeleteFunc(rep.Unanswered, func(g match.Gap) bool { e, _ := b.Find(g.ID); return e.Shipped })
	if !reflect.DeepEqual(rep.AskMe, []string{"address-street"}) || len(rep.Unanswered) != 1 || rep.Unanswered[0].ID != "portfolio-url" {
		t.Fatalf("ask-me %v unanswered %+v", rep.AskMe, rep.Unanswered)
	}
	if readFile(t, ws, bankFile) != identityBankText() {
		t.Fatal("a load wrote the bank")
	}
	if l, _ := s.File(); len(l.Bank.Employers) != 1 {
		t.Fatalf("the profile's employers reached the employers list: %+v", l.Bank.Employers)
	}

	writeFile(t, ws, workspace.FileProfile, strings.Replace(identityProfile, "alex@example.com", "alex@example.org", 1))
	if e, _ := stamped().Find("email-address"); e.Answer != "alex@example.org" {
		t.Fatalf("a profile edit was not followed: %q", e.Answer)
	}

	blank := func(id string) string { return "id: " + id + "\n    description: d\n    kind: fixed\n    answer: \"\"" }
	text := strings.Replace(identityBankText(), blank("email-address"), "id: email-address\n    description: d\n    kind: fixed\n    answer: old@example.com", 1)
	text = strings.Replace(text, blank("phone-mobile"), "id: phone-mobile\n    description: d\n    kind: fixed\n    answer: ' @ask-me '", 1)
	writeFile(t, ws, bankFile, text)
	b = stamped()
	if e, _ := b.Find("email-address"); e.Answer != "old@example.com" || e.DerivedFrom != "" {
		t.Fatalf("the user's answer was replaced: %+v", e)
	}
	if e, _ := b.Find("phone-mobile"); !e.AsksMe() || e.DerivedFrom != "" {
		t.Fatalf("the user's ask-me was replaced: %+v", e)
	}

	// The state and country the identity step recorded rank their pick lists;
	// a fallback the user wrote is theirs.
	writeFile(t, ws, workspace.FileProfile, strings.Replace(identityProfile, "    county: Sample County\n", "    county: Sample County\n    region: NC\n    country: US\n", 1))
	pick := func(id, fallback string) string {
		return "  - id: " + id + "\n    description: d\n    kind: pick-option\n    placeholder: true\n    preferred: []\n    fallback_text: '" + fallback + "'\n"
	}
	writeFile(t, ws, bankFile, bankText(pick("address-state", "")+pick("address-country", "")))
	b = stamped()
	state, _ := b.Find("address-state")
	country, _ := b.Find("address-country")
	if strings.Join(state.Preferred, "|") != "North Carolina|North Carolina (NC)" || strings.Join(country.Preferred, "|") != "United States" || state.DerivedFrom != "identity" {
		t.Fatalf("state %+v country %+v", state.Body, country.Body)
	}
	writeFile(t, ws, bankFile, bankText(pick("address-country", "United States of America")))
	if e, _ := stamped().Find("address-country"); len(e.Preferred) != 0 || e.DerivedFrom != "" {
		t.Fatalf("a written fallback was replaced: %+v", e)
	}
}

func TestCanonicalLinks(t *testing.T) {
	tests := map[string]string{
		"linkedin.com/in/alex-example":                 "https://www.linkedin.com/in/alex-example/",
		"https://linkedin.com/in/alex-example":         "https://www.linkedin.com/in/alex-example/",
		"https://uk.linkedin.com/in/alex-example/":     "https://www.linkedin.com/in/alex-example/",
		"HTTPS://WWW.LinkedIn.com/in/Alex-Example":     "https://www.linkedin.com/in/Alex-Example/",
		"https://www.linkedin.com/company/example":     "https://www.linkedin.com/company/example",
		"https://evil.example.com/linkedin.com/in/abc": "https://evil.example.com/linkedin.com/in/abc",
		"": "",
	}
	for in, want := range tests {
		if got := CanonicalLinkedIn(in); got != want {
			t.Errorf("CanonicalLinkedIn(%q) = %q, want %q", in, got, want)
		}
	}
	if CanonicalLink("github.com/alex") != "https://github.com/alex" || CanonicalLink("") != "" || CanonicalLink("http://a.test") != "http://a.test" {
		t.Fatal("CanonicalLink")
	}
}
