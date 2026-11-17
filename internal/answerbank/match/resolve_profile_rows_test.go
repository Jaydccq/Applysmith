package match

import (
	"fmt"
	"strings"
	"testing"
)

// pfWant checks a filled answer.
func pfWant(t *testing.T, r Result, answer string) {
	t.Helper()
	if r.Outcome == OutcomeAbstained || r.Answer != answer {
		t.Fatalf("want answer %q, got outcome=%s answer=%q reason=%s trace=%v", answer, r.Outcome, r.Answer, r.Reason, r.Trace)
	}
}

// pfAbstain checks an abstain, with its reason and terminal flag.
func pfAbstain(t *testing.T, r Result, reason Reason, terminal bool) {
	t.Helper()
	if r.Outcome != OutcomeAbstained || r.Reason != reason || r.Terminal != terminal {
		t.Fatalf("want abstain %s terminal=%t, got outcome=%s answer=%q reason=%s terminal=%t trace=%v",
			reason, terminal, r.Outcome, r.Answer, r.Reason, r.Terminal, r.Trace)
	}
}

var pfToday = pfDate(2026, 10, 2)

func manyOptions(n int, prefix string) []Option {
	out := make([]Option, n)
	for i := range out {
		out[i] = Option{Value: fmt.Sprint(i), Label: fmt.Sprintf("%s %03d", prefix, i)}
	}
	return out
}

// An unnumbered work block (SmartRecruiters' Experience: bare Title and
// Company boxes, row after row) reads its rows in the same profile order; a
// lone bare box is not a block, and a row past the history is left.
func TestAnswerBank_EmploymentRows_UnnumberedBlock(t *testing.T) {
	m := pfMatcher()
	f := alexFacts()
	block := []Question{textQ("t0", "Title"), textQ("c0", "Company *"), textQ("t1", "Title"), textQ("c1", "Company *"), textQ("t2", "Title"), textQ("c2", "Company *")}
	rs := pfRun(t, m, pfReq(f, pfToday, block...))
	for i, want := range []string{"Software Engineer", "Example Corp", "Intern", "Acme Test"} {
		pfWant(t, rs[i], want)
	}
	pfAbstain(t, rs[4], ReasonSectionPolicy, true)
	pfAbstain(t, rs[5], ReasonSectionPolicy, true)
	if r := pfOne(t, m, f, pfToday, textQ("c", "Company")); r.Outcome != OutcomeAbstained || r.Reason == ReasonSectionPolicy {
		t.Fatalf("a lone Company box is not a block row: %+v", r)
	}
	if r := pfRun(t, m, pfReq(f, pfToday, pfSelect("t", "Title", "Mr.", "Ms.", "Dr."), textQ("c", "Company")))[0]; r.Reason == ReasonSectionPolicy || r.Answer == "Software Engineer" {
		t.Fatalf("a Title list of forms of address is not a job title: %+v", r)
	}
}

// Citadel (live): its rows are named by attribute, and both rows' GPA had
// taken the latest grade average; the dates were left for the user.
func TestAnswerBank_EducationRow_CitadelNamedRows(t *testing.T) {
	m := pfMatcher()
	f := Facts{Education: []EducationFact{
		{School: "University of Westbridge", Degree: "Bachelor of Science (B.S)", Start: "2020-09", End: "2024-06", GradeAverage: "3.8"},
		{School: "Carver University", Degree: "Master of Science (M.S)", Start: "2024-08", End: "2026-05", GradeAverage: "3.4"},
	}}
	row := func(n int, field string) string {
		return fmt.Sprintf(`input[name="profile[education][new%d][%s]"][type="text"]`, n, field)
	}
	for _, c := range []struct{ sel, label, want string }{
		{row(0, "from"), "Start Date", "2024-08-01"},
		{row(0, "to"), "Graduation Date", "2026-05-01"},
		{row(0, "degree_measure][value"), "GPA", "3.4"},
		{row(1, "degree_measure][value"), "GPA", "3.8"},
		{row(1, "from"), "Start Date", "2020-09-01"},
	} {
		pfWant(t, pfOne(t, m, f, pfToday, textQ(c.sel, c.label)), c.want)
	}
	// The scale is not recorded, and a third row is not the profile's.
	pfAbstain(t, pfOne(t, m, f, pfToday, pfSelect(`select[name="profile[education][new1][degree_measure][highest_value]"]`, "GPA Scale", "4.0", "5.0", "0-100")), ReasonSectionPolicy, true)
	pfAbstain(t, pfOne(t, m, f, pfToday, textQ(row(2, "from"), "Start Date")), ReasonSectionPolicy, true)
}

func TestAnswerBank_EducationRow_SectionHeadingRows(t *testing.T) {
	m := pfMatcher()
	f := alexFacts()
	q := func(text string, path ...string) Question {
		return Question{Selector: "ctl-" + strings.ReplaceAll(text, " ", "-"), Text: text, Kind: KindText, SectionPath: path}
	}
	cases := []struct{ text, path, want string }{
		{"School Name", "Education 2", "Example College"},
		{"Name of the School or University", "Education 1", "Example State University"},
		{"Degree", "Education 2", "Bachelor of Science"},
		{"Degree Type", "Education 1", "Master of Science"},
		{"Major (required)", "Education 1", "Computer Science"},
		{"Field of Study", "Education 2", "Mathematics"},
		{"From year", "Education 2", "2020"},
		{"Start date month", "Education 1", "September"},
		{"To year", "Education 2", "2024"},
		{"Graduation month", "Education 1", "May"},
	}
	for _, c := range cases {
		t.Run("PR-006 "+c.text+" in "+c.path, func(t *testing.T) {
			pfWant(t, pfOne(t, m, f, pfToday, q(c.text, "Application", c.path)), c.want)
		})
	}
	t.Run("a longer question inside the section is not a row field", func(t *testing.T) {
		r := pfOne(t, m, f, pfToday, q("Honors received", "Education 1"))
		pfAbstain(t, r, ReasonNoEntry, false)
	})
	t.Run("row zero is not a row", func(t *testing.T) {
		r := pfOne(t, m, f, pfToday, q("School Name", "Education 0"))
		pfAbstain(t, r, ReasonNoEntry, false)
	})
}

func TestAnswerBank_EducationRow_NoFieldAbstains(t *testing.T) {
	m := pfMatcher()
	r := pfOne(t, m, alexFacts(), pfToday, textQ("profile.education[0].summary", "Summary"))
	pfAbstain(t, r, ReasonSectionPolicy, true)
	if !traceHas(r, "no education field") {
		t.Fatalf("trace = %v", r.Trace)
	}
}

func TestAnswerBank_EducationRow_OrphanRowAbstains(t *testing.T) {
	m := pfMatcher()
	f := alexFacts()
	t.Run("the profile has fewer entries", func(t *testing.T) {
		r := pfOne(t, m, f, pfToday, textQ("degree--2", "Degree"))
		pfAbstain(t, r, ReasonSectionPolicy, true)
		if !traceHas(r, "orphan row") {
			t.Fatalf("trace = %v", r.Trace)
		}
	})
	t.Run("no education at all", func(t *testing.T) {
		pfAbstain(t, pfOne(t, m, Facts{}, pfToday, textQ("school--0", "School")), ReasonSectionPolicy, true)
	})
	t.Run("the entry has no value for the field", func(t *testing.T) {
		g := Facts{Education: []EducationFact{{School: "Example College", Degree: "Bachelor"}}}
		pfAbstain(t, pfOne(t, m, g, pfToday, textQ("end-year--0", "End year")), ReasonSectionPolicy, true)
	})
	t.Run("a global degree entry does not fill an empty row", func(t *testing.T) {
		bank := ent("highest-level-education", "fixed", withAnswer("Master's degree"), withExamples("Degree"))
		r := pfOne(t, pfMatcher(bank), alexFacts(), pfToday, Question{Selector: "degree--5", Text: "Degree", Kind: KindText})
		pfAbstain(t, r, ReasonSectionPolicy, true)
	})
}

func TestAnswerBank_EducationRow_OptionBinding(t *testing.T) {
	m := pfMatcher()
	f := alexFacts()
	sel := func(selector, text string, labels ...string) Question { return pfSelect(selector, text, labels...) }
	t.Run("the degree is scored as its level", func(t *testing.T) {
		r := pfOne(t, m, f, pfToday, sel("degree--0", "Degree", "Select...", "High School Diploma", "Bachelor's Degree", "Master's Degree", "Doctorate"))
		pfWant(t, r, "Master's Degree")
		if r.ChosenLabel != "Master's Degree" || r.Confidence != 0.90 {
			t.Fatalf("result = %+v", r)
		}
		r = pfOne(t, m, f, pfToday, sel("degree--1", "Degree", "Associate Degree", "Bachelor of Arts", "Master of Arts"))
		pfWant(t, r, "Bachelor of Arts")
	})
	t.Run("a month binds the option naming the same month", func(t *testing.T) {
		for _, labels := range [][]string{
			{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"},
			{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"},
			{"01", "02", "03", "04", "05", "06", "07", "08", "09", "10", "11", "12"},
			{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12"},
		} {
			r := pfOne(t, m, f, pfToday, sel("start-month--0", "Start month", labels...))
			pfWant(t, r, labels[8])
		}
	})
	t.Run("a year binds the equal option", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfToday, sel("end-year--0", "End year", "2024", "2025", "2026", "2027")), "2026")
	})
	t.Run("the school binds the option scoring at least 0.5", func(t *testing.T) {
		r := pfOne(t, m, f, pfToday, sel("school--0", "School", "Example College", "Example State University", "Other School"))
		pfWant(t, r, "Example State University")
	})
	t.Run("among several options of the level, a named degree never stands in for another", func(t *testing.T) {
		// Visa's Workday lists its levels by ISCED, with an MBA beside the generic master's.
		isced := []string{"Bachelor’s or Equivalent First-Degree (ISCED 6: BA, BSc, BEng, LLB, etc.)", "Master of Business Administration - MBA ( (ISCED 7)",
			"Master’s or Equivalent Second-Degree (ISCED 7: MA, MSc, MEng, MPhil, professional licentiate)", "Doctorate or Equivalent (ISCED 8: PhD, DPhil, EdD)"}
		pfWant(t, pfOne(t, m, f, pfToday, sel("degree--0", "Degree", isced...)), isced[2])
		// The degree's own name wins over the generic option.
		pfWant(t, pfOne(t, m, f, pfToday, sel("degree--0", "Degree", "Masters", "Master of Science", "Master of Science Nursing")), "Master of Science")
		// Ensemble: the level alone beside two named bachelor's degrees the profile does not hold.
		pfWant(t, pfOne(t, m, f, pfToday, sel("degree--1", "Degree", "Bachelor of Science Nursing", "Bachelors", "Bachelor of Science Information Technology")), "Bachelors")
		r := pfOne(t, m, f, pfToday, sel("degree--0", "Degree", "Master of Business Administration", "Master of Science Nursing"))
		pfAbstain(t, r, ReasonInputMismatch, false)
		// KLA (Workday, live): an option's "or" names alternatives, one of them the degree's subject.
		kla := []string{"Bachelors of Arts or Science (Bachelors)", "Masters of Technology (Masters)", "Masters of Arts or Science or Business Administration (Masters)"}
		pfWant(t, pfOne(t, m, f, pfToday, sel("degree--0", "Degree", kla...)), kla[2])
		r = pfOne(t, m, f, pfToday, sel("degree--0", "Degree", "Masters of Technology", "Masters of Arts or Business Administration"))
		pfAbstain(t, r, ReasonInputMismatch, false)
	})
	t.Run("a closed set without the value abstains and does not fall to a global entry", func(t *testing.T) {
		r := pfOne(t, m, f, pfToday, sel("degree--0", "Degree", "Associate Degree", "Bachelor's Degree"))
		pfAbstain(t, r, ReasonInputMismatch, false)
	})
}

func TestAnswerBank_EducationRow_TruncatedCatalogTypeAndPick(t *testing.T) {
	m := pfMatcher()
	f := alexFacts()
	big := func(selector, text string, n int, extra ...Option) Question {
		return Question{Selector: selector, Text: text, Kind: KindCustomListbox, Options: append(manyOptions(n, "Alpha Institute"), extra...)}
	}
	t.Run("a school catalog without the school gets the value", func(t *testing.T) {
		r := pfOne(t, m, f, pfToday, big("school--0", "School", 100))
		pfWant(t, r, "Example State University")
		if r.ChosenLabel != "Example State University" {
			t.Fatalf("chosen = %q", r.ChosenLabel)
		}
	})
	t.Run("a catalog that holds the school picks the real option", func(t *testing.T) {
		r := pfOne(t, m, f, pfToday, big("school--0", "School", 60, Option{Value: "x", Label: "Example State University"}))
		pfWant(t, r, "Example State University")
		if r.ChosenLabel != "Example State University" {
			t.Fatalf("chosen = %q", r.ChosenLabel)
		}
	})
	t.Run("the field of study is a catalog too", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfToday, big("discipline--0", "Discipline", 80)), "Computer Science")
	})
	t.Run("a closed degree list never type-and-picks", func(t *testing.T) {
		r := pfOne(t, m, f, pfToday, big("degree--0", "Degree", 80))
		pfAbstain(t, r, ReasonInputMismatch, false)
	})
	t.Run("a short school list is not a catalog", func(t *testing.T) {
		r := pfOne(t, m, f, pfToday, big("school--0", "School", 10))
		pfAbstain(t, r, ReasonInputMismatch, false)
	})
	// Greenhouse lists the first 25 schools of its catalog, A to Al, until
	// something is typed (10a Labs).
	page := func(labels ...string) Question {
		q := Question{Selector: "school--0", Text: "School", Kind: KindCustomListbox}
		for _, l := range labels {
			q.Options = append(q.Options, Option{Value: l, Label: l})
		}
		return q
	}
	first := func(n int, prefix string) []string {
		var out []string
		for _, o := range manyOptions(n, prefix) {
			out = append(out, o.Label)
		}
		return out
	}
	t.Run("a first page that ends before the school gets the value", func(t *testing.T) {
		r := pfOne(t, m, f, pfToday, page(first(25, "Aalborg")...))
		pfWant(t, r, "Example State University")
	})
	t.Run("a sorted list the school would fall inside is a closed set", func(t *testing.T) {
		r := pfOne(t, m, f, pfToday, page(append(first(24, "Aalborg"), "Zurich University")...))
		pfAbstain(t, r, ReasonInputMismatch, false)
	})
	t.Run("an unsorted list is a closed set", func(t *testing.T) {
		labels := first(25, "Aalborg")
		labels[0], labels[24] = labels[24], labels[0]
		r := pfOne(t, m, f, pfToday, page(labels...))
		pfAbstain(t, r, ReasonInputMismatch, false)
	})
	t.Run("a first page shorter than the floor is a closed set", func(t *testing.T) {
		r := pfOne(t, m, f, pfToday, page(first(19, "Aalborg")...))
		pfAbstain(t, r, ReasonInputMismatch, false)
	})
}

func TestAnswerBank_EducationRow_WorkdayOpaqueDegreeAbstains(t *testing.T) {
	m := pfMatcher()
	for _, sel := range []string{"education-24--degree", "#education-29--degree", `[id="education-29--degree"]`} {
		r := pfOne(t, m, alexFacts(), pfToday, pfSelect(sel, "Degree", "Bachelor's Degree", "Master's Degree"))
		pfAbstain(t, r, ReasonSectionPolicy, true)
	}
	t.Run("only the degree is scoped", func(t *testing.T) {
		r := pfOne(t, m, alexFacts(), pfToday, textQ("education-24--school", "School"))
		if r.Reason == ReasonSectionPolicy {
			t.Fatalf("result = %+v", r)
		}
	})
}

func TestAnswerBank_WorkRow_ResolvesFromHistoryRow(t *testing.T) {
	m := pfMatcher()
	f := alexFacts()
	t.Run("an orphan row is the terminal abstain", func(t *testing.T) {
		pfAbstain(t, pfOne(t, m, f, pfToday, textQ("NewWorkExperience_Organization5", "Organization")), ReasonSectionPolicy, true)
	})
	t.Run("a blank field is an orphan", func(t *testing.T) {
		g := Facts{Employment: []EmploymentFact{{Employer: "Example Corp"}}}
		pfAbstain(t, pfOne(t, m, g, pfToday, textQ("NewWorkExperience_JobTitle0", "Job Title")), ReasonSectionPolicy, true)
	})
	t.Run("no employment at all", func(t *testing.T) {
		pfAbstain(t, pfOne(t, m, Facts{}, pfToday, textQ("NewWorkExperience_Organization0", "Organization")), ReasonSectionPolicy, true)
	})
	t.Run("abbreviated month labels", func(t *testing.T) {
		labels := []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
		pfWant(t, pfOne(t, m, f, pfToday, pfSelect("NewWorkExperience_FromMonth1", "From Month", labels...)), "Jun")
	})
	t.Run("the year option", func(t *testing.T) {
		pfWant(t, pfOne(t, m, f, pfToday, pfSelect("NewWorkExperience_FromYear0", "From Year", "2022", "2023", "2024")), "2024")
	})
	t.Run("an employer catalog gets the value for type-and-pick", func(t *testing.T) {
		q := Question{Selector: "NewWorkExperience_Organization1", Text: "Organization", Kind: KindCustomListbox, Options: manyOptions(50, "Beta Company")}
		r := pfOne(t, m, f, pfToday, q)
		pfWant(t, r, "Acme Test")
	})
	t.Run("a row answers from the history, not from the global entry", func(t *testing.T) {
		bank := ent("current-company", "fixed", withAnswer("Global Corp"), withExamples("Organization"))
		r := pfOne(t, pfMatcher(bank), f, pfToday, textQ("NewWorkExperience_Organization0", "Organization"))
		pfWant(t, r, "Example Corp")
	})
}

func TestAnswerBank_WorkRow_NonIndexedControlAbstains(t *testing.T) {
	bank := ent("current-company", "fixed", withAnswer("Example Corp"), withExamples("Company"))
	q := Question{Selector: "company", Text: "Company", Kind: KindText, SectionPath: []string{"Work Experience 1"}}
	r := pfOne(t, pfMatcher(bank), alexFacts(), pfToday, q)
	pfAbstain(t, r, ReasonSectionPolicy, true)
}

// GoodLeap (Lever, live): numbered company boxes ("Company 1", "Company 2")
// each followed by a bare "Title" make a block of employment rows; the Nth
// bare title is the Nth job's, never the current title twice.
func TestAnswerBank_WorkBlock_NumberedCompaniesWithBareTitles(t *testing.T) {
	m := pfMatcher()
	f := Facts{Employment: []EmploymentFact{
		{Employer: "Example Labs", Title: "Software Engineer", Start: "2026-06"},
		{Employer: "Example Institute", Title: "Researcher Intern", Start: "2023-07", End: "2023-09"},
	}}
	qs := []Question{
		textQ("c1", "Company 1"), textQ("t1", "Title"), textQ("y1", "Years Worked"),
		textQ("c2", "Company 2"), textQ("t2", "Title"), textQ("y2", "Years Worked"),
	}
	resp, err := m.Match(Request{Host: "jobs.lever.co", Facts: f, Today: pfToday, Questions: qs})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"c1": "Example Labs", "t1": "Software Engineer", "c2": "Example Institute", "t2": "Researcher Intern"}
	for _, r := range resp.Results {
		if w, ok := want[r.Selector]; ok && r.Answer != w {
			t.Errorf("%s: want %q, got %+v", r.Selector, w, r)
		}
	}
}
