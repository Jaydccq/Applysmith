package match

import (
	"testing"
)

// The profile-family tests use the persona Alex Example and the profile stages
// only, so a test does not depend on another family's stages.

// pfStages is the stage set of the profile family alone.
func pfStages() Stages {
	var st Stages
	profileStages(&st)
	return st
}

// pfMatcher builds a matcher over a bank with the profile stages.
func pfMatcher(es ...Entry) *Matcher { return New(bankOf(es...), pfStages()) }

// pfDate builds a calendar date.
func pfDate(y, m, d int) Date { return Date{Year: y, Month: m, Day: d} }

// alexFacts is the persona's profile: two degrees, two jobs and three skills.
func alexFacts() Facts {
	years := func(v float64) *float64 { return &v }
	return Facts{
		Education: []EducationFact{
			{School: "Example State University", Degree: "Master of Science", Field: "Computer Science", Start: "2024-09", End: "2026-05", GradeAverage: "3.9"},
			{School: "Example College", Degree: "Bachelor of Science", Field: "Mathematics", Start: "2020-09", End: "2024-05", GradeAverage: "3.7"},
		},
		Employment: []EmploymentFact{
			{Employer: "Example Corp", Title: "Software Engineer", Start: "2024-06", End: "present"},
			{Employer: "Acme Test", Title: "Intern", Start: "2023-06", End: "2023-08"},
		},
		Skills: []SkillFact{
			{Name: "Python", Years: years(3), Level: "advanced"},
			{Name: "React", Years: years(2), Level: "intermediate"},
			{Name: "Go", Level: "beginner"},
		},
		AuthorizedCountries: []string{"US"},
		DateOfBirth:         "2000-06-15",
		VisaStatus:          "F-1 OPT",
	}
}

// pfReq is a request from the persona's host with the given facts and day.
func pfReq(f Facts, today Date, qs ...Question) Request {
	return Request{Host: "jobs.example.com", Facts: f, Today: today, Questions: qs}
}

// pfRun matches the questions and returns one result per question.
func pfRun(t *testing.T, m *Matcher, req Request) []Result {
	t.Helper()
	resp, err := m.Match(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp.Results
}

// pfOne matches a single question.
func pfOne(t *testing.T, m *Matcher, f Facts, today Date, q Question) Result {
	t.Helper()
	return pfRun(t, m, pfReq(f, today, q))[0]
}

// pfSelect is a select question with the given option labels.
func pfSelect(selector, text string, labels ...string) Question {
	return Question{Selector: selector, Text: text, Kind: KindSelect, Options: opts(labels...)}
}
