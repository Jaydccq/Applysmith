package answerbank

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/workspace"
)

var applicantNow = time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)

const applicantProfile = `version: 1
candidate:
  full_name: Alex Quinn Example
  preferred_name: Lex
  email: alex@example.com
  phone: "+1 555 0100"
  date_of_birth: "1990-01-01"
  address:
    line1: "@ask-me"
    line2: ""
    city: Exampleville
    county: Sample County
    region: Exampleshire
    postal_code: EX1 1AA
    country: GB
links:
  linkedin: linkedin.com/in/alex-example
work_authorization:
  requires_sponsorship_now: false
  visa_status: citizen
targets:
  compensation: {currency: USD, period: year, minimum: 100000, target: 150000}
skills:
  - {name: Go, years: 5}
history:
  employment:
    - {employer: Old Corp, title: Analyst, start: "2019-01", end: "2021-02", location: Sampletown, summary: Built reports.}
    - {employer: Example Corp, title: Engineer, start: "2021-07", end: present}
  education:
    - {school: Example University, degree: BSc, field: Computing, start: "2012-09", end: "2015-06", grade_average: "3.8"}
    - {school: Sample College, degree: MSc, start: "2025-09", end: "2026-09"}
`

func TestApplicant_SnapshotHoldsIdentityAndHistory(t *testing.T) {
	s, ws := newService(t)
	writeFile(t, ws, workspace.FileProfile, applicantProfile)
	writeFile(t, ws, bankFile, identityBankText())
	a, err := s.Applicant(applicantNow)
	if err != nil {
		t.Fatal(err)
	}
	if a.FullName != "Alex Quinn Example" || a.FirstName != "Alex" || a.MiddleName != "Quinn" || a.LastName != "Example" || a.PreferredName != "Lex" {
		t.Fatalf("names %+v", a)
	}
	if a.Email != "alex@example.com" || a.Phone != "+1 555 0100" || a.PhoneCountryCode != "+1" || a.PhoneNational != "5550100" {
		t.Fatalf("contact %+v", a)
	}
	if a.Address.City != "Exampleville" || a.Address.County != "Sample County" || a.Address.State != "Exampleshire" || a.Address.PostalCode != "EX1 1AA" || a.Address.Country != "GB" {
		t.Fatalf("address %+v", a.Address)
	}
	if a.LinkedIn != "https://www.linkedin.com/in/alex-example/" || a.GitHub != "" {
		t.Fatalf("links %q %q", a.LinkedIn, a.GitHub)
	}
	want := []ApplicantJob{
		{Employer: "Old Corp", Title: "Analyst", Location: "Sampletown", Summary: "Built reports.", StartDate: "2019-01", EndDate: "2021-02"},
		{Employer: "Example Corp", Title: "Engineer", StartDate: "2021-07", Current: true},
	}
	if !reflect.DeepEqual(a.Employment, want) {
		t.Fatalf("employment %+v", a.Employment)
	}
	if len(a.Education) != 2 || a.Education[0].EndDate != "2015-06" || a.Education[0].InProgress || a.Education[0].GradeAverage != "3.8" ||
		!a.Education[1].InProgress || a.Education[1].EndDate != "2026-09" {
		t.Fatalf("education %+v", a.Education)
	}
}

// A profile date names a month, so the snapshot carries the month and year
// only: no first- or last-of-month day is invented for an end date (or a
// start date), and the engine leaves a whole-date control empty while split
// month and year controls still resolve.
func TestSnapshot_EndDateWithoutDayOmitted(t *testing.T) {
	p := workspace.Profile{History: workspace.History{
		Employment: []workspace.Employment{{Employer: "Old Corp", Start: "2019-01", End: "2021-02"}},
		Education:  []workspace.Education{{School: "Example University", Start: "2012-09", End: "2015-06"}},
	}}
	a := buildApplicant(p, match.Bank{}, applicantNow)
	if j := a.Employment[0]; j.StartDate != "2019-01" || j.EndDate != "2021-02" || j.Current {
		t.Fatalf("employment dates %+v", j)
	}
	if e := a.Education[0]; e.StartDate != "2012-09" || e.EndDate != "2015-06" || e.InProgress {
		t.Fatalf("education dates %+v", e)
	}
}

func TestApplicant_AskMeFieldIsNotAValue(t *testing.T) {
	s, ws := newService(t)
	writeFile(t, ws, workspace.FileProfile, applicantProfile)
	writeFile(t, ws, bankFile, identityBankText())
	a, err := s.Applicant(applicantNow)
	if err != nil {
		t.Fatal(err)
	}
	if a.Address.Line1 != "" || !reflect.DeepEqual(a.AskMe, []string{"address.line1"}) {
		t.Fatalf("line1 %q askMe %v", a.Address.Line1, a.AskMe)
	}
}

func TestApplicant_BankIdentityEntriesFillBlankProfileKeys(t *testing.T) {
	s, ws := newService(t)
	writeFile(t, ws, workspace.FileProfile, "version: 1\ncandidate:\n  email: \"\"\n")
	writeFile(t, ws, bankFile, bankText(
		fixedYAML("applicant-full-name", "Sam Sample"),
		fixedYAML("email-address", "sam@example.com"),
		fixedYAML("phone-mobile", "\"\"", "placeholder: true"),
		fixedYAML("address-city", "Exampleville"),
		fixedYAML("address-county", "\"@ask-me\""),
		fixedYAML("github-url", "github.com/sam-sample"),
		fixedYAML("address-street", "1 Example Road"), // not an identity entry the snapshot reads
	))
	a, err := s.Applicant(applicantNow)
	if err != nil {
		t.Fatal(err)
	}
	if a.FullName != "Sam Sample" || a.FirstName != "Sam" || a.MiddleName != "" || a.LastName != "Sample" || a.Email != "sam@example.com" ||
		a.Address.City != "Exampleville" || a.GitHub != "https://github.com/sam-sample" {
		t.Fatalf("%+v", a)
	}
	if a.Phone != "" || a.PhoneCountryCode != "" || a.PhoneNational != "" {
		t.Fatalf("a blank placeholder supplied a phone: %+v", a)
	}
	if a.Address.Line1 != "" || a.Address.County != "" || !reflect.DeepEqual(a.AskMe, []string{"address.county"}) {
		t.Fatalf("the bank's street is not read, and its ask-me is reported: %+v %v", a.Address, a.AskMe)
	}
}

func TestApplicant_ProfileWinsOverBank(t *testing.T) {
	s, ws := newService(t)
	writeFile(t, ws, workspace.FileProfile, "version: 1\ncandidate:\n  email: profile@example.com\n")
	writeFile(t, ws, bankFile, bankText(fixedYAML("email-address", "bank@example.com")))
	a, err := s.Applicant(applicantNow)
	if err != nil || a.Email != "profile@example.com" {
		t.Fatalf("%v %+v", err, a)
	}
}

func TestApplicant_NameAndPhoneParts(t *testing.T) {
	for _, c := range []struct{ full, first, middle, last string }{
		{"", "", "", ""}, {"Alex", "Alex", "", ""}, {"Alex Example", "Alex", "", "Example"},
		{"Alex Quinn Example", "Alex", "Quinn", "Example"}, {"  Alex  Quinn  Q  Example ", "Alex", "Quinn Q", "Example"},
	} {
		f, m, l := splitName(c.full)
		if f != c.first || m != c.middle || l != c.last {
			t.Errorf("%q: %q %q %q", c.full, f, m, l)
		}
	}
	// Numbers are assembled so that no digit run in this file looks like a real one.
	plus := func(code, rest string) string { return "+" + code + " " + rest }
	for _, c := range []struct{ phone, code, national string }{
		{plus("1", "(555) 0100"), "+1", "5550100"}, {plus("7", "495 555 0100"), "+7", "4955550100"}, {plus("44", "7700 900123"), "+44", "7700900123"},
		{plus("353", "1 555 0100"), "+353", "15550100"}, {plus("971", "4 555 0100"), "+971", "45550100"}, {plus("86", "10 5550 0100"), "+86", "1055500100"},
		{"555 0100 123", "", "5550100123"}, {"+12", "", "12"}, {"", "", ""},
	} {
		code, national := splitPhone(c.phone)
		if code != c.code || national != c.national {
			t.Errorf("%q: %q %q", c.phone, code, national)
		}
	}
}

// The snapshot is identity and history only: what the profile holds about
// authorization, pay, birth, skills or any secret never reaches it.
func TestApplicant_SnapshotCarriesNoSensitiveValue(t *testing.T) {
	s, ws := newService(t)
	writeFile(t, ws, workspace.FileProfile, applicantProfile)
	writeFile(t, ws, bankFile, identityBankText())
	a, err := s.Applicant(applicantNow)
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"1990-01-01", "citizen", "150000", "100000"} {
		if strings.Contains(strings.ToLower(flatten(a)), banned) {
			t.Fatalf("the snapshot contains %q", banned)
		}
	}
}

func flatten(a Applicant) string {
	var b strings.Builder
	var walk func(v reflect.Value)
	walk = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.String:
			b.WriteString(v.String() + "\n")
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				walk(v.Field(i))
			}
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i))
			}
		}
	}
	walk(reflect.ValueOf(a))
	return b.String()
}
