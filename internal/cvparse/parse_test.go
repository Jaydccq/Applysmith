package cvparse

import (
	"fmt"
	"maps"
	"testing"
)

const fictionalCV = `# Alex Example

## Experience

### Senior Software Engineer, Fictional Systems Inc.
2021-03 to present, Austin, TX

- Built Go services.

### Software Engineer, Sample Software LLC
2017-06 to 2021-02, Remote

### Platform Engineer at Northwind Example
London, United Kingdom 2015-01 to 2017-05

### Support Engineer, Placeholder Works (2013-09 to 2014-12)

### Analyst at Demo Holdings, Denver, CO 2012-01 - 2013-08

### Data Engineer, Example Analytics
2010-01 – 2011-12 · Remote

## Education

### B.S. Computer Science, Placeholder State University
2013-08 to 2017-05
`

func TestParseCV_ALocationLineIsNeverTheEmployer(t *testing.T) {
	jobs := Parse(fictionalCV).Employment
	want := []Job{
		{Employer: "Fictional Systems Inc.", Title: "Senior Software Engineer", Start: "2021-03", End: "present"},
		{Employer: "Sample Software LLC", Title: "Software Engineer", Start: "2017-06", End: "2021-02"},
		{Employer: "Northwind Example", Title: "Platform Engineer", Start: "2015-01", End: "2017-05"},
		{Employer: "Placeholder Works", Title: "Support Engineer", Start: "2013-09", End: "2014-12"},
		{Employer: "Demo Holdings", Title: "Analyst", Start: "2012-01", End: "2013-08"},
		{Employer: "Example Analytics", Title: "Data Engineer", Start: "2010-01", End: "2011-12"},
	}
	if len(jobs) != len(want) {
		t.Fatalf("jobs = %+v", jobs)
	}
	for i := range want {
		if jobs[i] != want[i] {
			t.Errorf("job %d = %+v, want %+v", i, jobs[i], want[i])
		}
	}
}

func TestParseCV_ADatedLineWithOnlyALocationAndNoHeadingIsDropped(t *testing.T) {
	d := Parse("## Experience\n\n2021-03 to present, Austin, TX\n")
	if len(d.Employment) != 0 {
		t.Errorf("a location is not an employer: %+v", d.Employment)
	}
}

func TestParseCV_AYearOnlyDateKeepsNoInventedMonth(t *testing.T) {
	cv := `## Experience

### Engineer, Acme Example Ltd (2019 to 2021)

### Analyst at Demo Holdings, 2021 - present

### Developer, Sample Works (2015-04 to 2018-11)

## Education

### B.Sc. Computing, Example University, 2014
`
	d := Parse(cv)
	wantJobs := []Job{
		{Employer: "Acme Example Ltd", Title: "Engineer", Start: "2019", End: "2021"},
		{Employer: "Demo Holdings", Title: "Analyst", Start: "2021", End: "present"},
		{Employer: "Sample Works", Title: "Developer", Start: "2015-04", End: "2018-11"},
	}
	if len(d.Employment) != len(wantJobs) {
		t.Fatalf("jobs = %+v", d.Employment)
	}
	for i := range wantJobs {
		if d.Employment[i] != wantJobs[i] {
			t.Errorf("job %d = %+v, want %+v", i, d.Employment[i], wantJobs[i])
		}
	}
	if len(d.Education) != 1 || d.Education[0].End != "2014" || d.Education[0].Start != "" {
		t.Errorf("education = %+v", d.Education)
	}
	if got := d.NeedsMonth(); got != 3 {
		t.Errorf("NeedsMonth = %d, want 3", got)
	}
}

func TestParseCV_AMixedEntryKeepsTheStatedMonth(t *testing.T) {
	d := Parse("## Experience\n\n### Engineer, Acme Example Ltd (2019-06 to 2021)\n")
	if len(d.Employment) != 1 || d.Employment[0].Start != "2019-06" || d.Employment[0].End != "2021" {
		t.Fatalf("jobs = %+v", d.Employment)
	}
}

func TestPlainSectionHeadingsKeepDatesAndSourceLocations(t *testing.T) {
	text := "Riley Tester\nPROFESSIONAL EXPERIENCE:\nEngineer, Northwind Systems (2020-01 to present)\nEducation\nBSc Computing, Northern College (2016-09 to 2019-06)\nTechnical Skills\nGo, SQL\nProjects\nInternal tooling (2022-01 to present)\n"
	d, loc := ParseLocated(text)
	if len(d.Employment) != 1 || d.Employment[0].Employer != "Northwind Systems" || d.Employment[0].Start != "2020-01" || len(d.Education) != 1 || len(d.Skills) != 2 {
		t.Fatalf("draft=%+v", d)
	}
	if loc.Employment[0].Line != 3 || loc.Employment[0].Section != "PROFESSIONAL EXPERIENCE:" || loc.Education[0].Line != 5 {
		t.Fatalf("locators=%+v", loc)
	}
	for _, prose := range []string{"My experience covers platform engineering", "skills in Go and SQL", "Employment Lawyer", "Career goals"} {
		if got := Parse(prose + "\nEngineer, Northwind Systems (2020-01 to present)\n"); got.Entries() != 0 {
			t.Fatalf("prose became heading: %q %+v", prose, got)
		}
	}
	if got := Parse("## Summary\nA profile summary\nExperience\n2020-01 to present, Austin, TX\n"); got.Entries() != 0 {
		t.Fatalf("previous section supplied employer: %+v", got)
	}
}

// Month names and a line of only dates under a heading are the shape of most
// real CVs (and of the original product's own cv.md); they once gave an
// employer "Jan" and dropped whole entries.
func TestParseCV_MonthNamesAndDateOnlyLinesUnderHeadings(t *testing.T) {
	cv := `# Riley Tester

## Experience

### Software Engineer — Bluefin Example
Jan 2023 – Present
- Built a Go ingestion service.

### Engineering Intern — Larkspur Example
Jun. 2022 – Aug. 2022

### Northwind Example Labs
**Research Assistant**  
Beijing, China  
Sept. 2019 – March 2020

## Education

### Example University
**Master of Science in Computer Engineering**  
Durham, NC, USA  
Aug. 2024 – May 2026

### B.S. Computer Science — Placeholder State University
May 2021

## Skills

### Languages
Go, Python
`
	d := Parse(cv)
	wantJobs := []Job{
		{Employer: "Bluefin Example", Title: "Software Engineer", Start: "2023-01", End: "present"},
		{Employer: "Larkspur Example", Title: "Engineering Intern", Start: "2022-06", End: "2022-08"},
		{Employer: "Northwind Example Labs", Title: "Research Assistant", Start: "2019-09", End: "2020-03"},
	}
	if len(d.Employment) != len(wantJobs) {
		t.Fatalf("jobs = %+v", d.Employment)
	}
	for i := range wantJobs {
		if d.Employment[i] != wantJobs[i] {
			t.Errorf("job %d = %+v, want %+v", i, d.Employment[i], wantJobs[i])
		}
	}
	wantStudies := []Study{
		{School: "Example University", Degree: "Master of Science", Field: "Computer Engineering", Start: "2024-08", End: "2026-05"},
		{School: "Placeholder State University", Degree: "B.S.", Field: "Computer Science", End: "2021-05"},
	}
	if len(d.Education) != len(wantStudies) {
		t.Fatalf("education = %+v", d.Education)
	}
	for i := range wantStudies {
		if d.Education[i] != wantStudies[i] {
			t.Errorf("study %d = %+v, want %+v", i, d.Education[i], wantStudies[i])
		}
	}
	if d.NeedsMonth() != 0 || len(d.Skills) != 2 {
		t.Errorf("needs month %d, skills %v", d.NeedsMonth(), d.Skills)
	}
}

// Identity answers the identity fields the CV's name and contact line state,
// and nothing a template, the example persona or a later section holds.
func TestIdentityReadsTheNameAndContactLine(t *testing.T) {
	for _, c := range []struct {
		name, cv string
		want     map[string]string
	}{
		{"one separated line and a markdown link",
			"# Riley Sample\n\nLeeds, West Yorkshire | riley.sample@mailbox.test | +1 415 555 0142\n[linkedin.com/in/riley](https://www.linkedin.com/in/riley)\n\n## Experience\n",
			map[string]string{"full_name": "Riley Sample", "city": "Leeds", "region": "West Yorkshire", "email": "riley.sample@mailbox.test", "phone": "+14155550142", "linkedin": "https://www.linkedin.com/in/riley"}},
		{"labelled lines, a mailto link and bare sites",
			"# Jordan Sample\n**Email:** [mail](mailto:jordan@mailbox.test)\n**Location:** Austin, TX, United States\nPhone: (512) 555-0199\ngithub.com/jordan · jordan.dev\n",
			map[string]string{"full_name": "Jordan Sample", "email": "jordan@mailbox.test", "city": "Austin", "region": "TX", "country": "United States", "phone": "(512) 555-0199", "github": "https://github.com/jordan", "portfolio": "https://jordan.dev"}},
		{"a title is no name and the first email wins",
			"# Résumé\n\nsam@mailbox.test | other@mailbox.test | Remote, anywhere\n",
			map[string]string{"email": "sam@mailbox.test"}},
		{"placeholders and the example persona are not the person's",
			"# [Your Name]\n\nalex@example.com | <phone> | http://www.example.org/alex\n",
			map[string]string{}},
		{"nothing under a section or before the name",
			"lee@mailbox.test\n## Contact\n# Lee Sample\nlee@mailbox.test\n",
			map[string]string{}},
	} {
		if got := Identity(c.cv); !maps.Equal(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// A comma inside parentheses does not split a skill ("AWS (ECS,
// Lambda, S3, DynamoDB)" came out as "AWS (ECS" and "DynamoDB)").
func TestParse_SkillGroupInParenthesesStaysWhole(t *testing.T) {
	d := Parse("Riley Tester\n## Skills\nGo, AWS (ECS, Lambda, S3, DynamoDB), Amazon Web Services (Elastic Container Service, Lambda)\n")
	want := []string{"Go", "AWS (ECS, Lambda, S3, DynamoDB)", "Amazon Web Services", "Elastic Container Service", "Lambda"}
	if fmt.Sprint(d.Skills) != fmt.Sprint(want) {
		t.Errorf("skills = %q, want %q", d.Skills, want)
	}
}

// A degree's field is split off where the degree ends: after initials, or at
// a separator after a spelled degree; "Master of Business Administration"
// names no field apart.
func TestStudy_FieldSplitsWhereTheDegreeEnds(t *testing.T) {
	for in, want := range map[string][2]string{
		"B.S. Computer Science":                   {"B.S.", "Computer Science"},
		"Bachelor of Science in Computer Science": {"Bachelor of Science", "Computer Science"},
		"Master of Business Administration":       {"Master of Business Administration", ""},
		"Bachelor of Arts":                        {"Bachelor of Arts", ""},
	} {
		if st := study("X", in, "", ""); st.Degree != want[0] || st.Field != want[1] {
			t.Errorf("%q: degree %q field %q, want %q", in, st.Degree, st.Field, want)
		}
	}
}
