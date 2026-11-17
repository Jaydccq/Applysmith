package match

import "testing"

// empMatcher is the profile stages with the Derived slot calling the current
// employer resolver directly, so the tests do not depend on the dispatch.
func empMatcher(employers []Employer, es ...Entry) *Matcher {
	st := pfStages()
	st.Derived = guarded(func(s *Step) (Draft, bool) { return currentEmployer(s, s.Entry) })
	b := bankOf(es...)
	b.Employers = employers
	return New(b, st)
}

func blankCompany(examples ...string) Entry {
	return ent("current-company", "fixed", emptyBody(), withExamples(examples...), func(e *Entry) { e.Placeholder = true })
}

func TestAnswerBank_EmploymentDate_Fallback(t *testing.T) {
	m := pfMatcher()
	past := Facts{Employment: []EmploymentFact{{Employer: "Acme Test", Title: "Intern", Start: "2023-06", End: "2023-08"}}}
	cases := []struct{ name, text, want string }{
		{"start month", "Employment start month", "June"},
		{"end month", "Work history end date month", "August"},
		{"start year singular", "Start year of employment", "2023"},
		{"end year", "Year of leaving previous job", "2023"},
		{"hire date", "Month you were hired at your last company", "June"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := pfOne(t, m, past, pfToday, textQ("c", c.text))
			pfWant(t, r, c.want)
			if r.Source != SourceDerived || r.Confidence != 0.85 || r.Outcome != OutcomeFilledReview {
				t.Fatalf("result = %+v", r)
			}
		})
	}
	t.Run("a month listbox picks the matching option", func(t *testing.T) {
		pfWant(t, pfOne(t, m, past, pfToday, pfSelect("c", "Employment end month", "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep")), "Aug")
	})
	t.Run("the current job has no end to give", func(t *testing.T) {
		pfAbstain(t, pfOne(t, m, alexFacts(), pfToday, textQ("c", "Employment end year")), ReasonNoEntry, false)
	})
	t.Run("the current job start", func(t *testing.T) {
		pfWant(t, pfOne(t, m, alexFacts(), pfToday, textQ("c", "Employment start year")), "2024")
	})
	t.Run("an education date never borrows an employment date", func(t *testing.T) {
		g := alexFacts()
		pfWant(t, pfOne(t, m, g, pfToday, textQ("c", "Graduation year")), "2026")
		pfWant(t, pfOne(t, m, g, pfToday, textQ("c", "Employment start year")), "2024")
	})
	t.Run("without employment entries it stands down", func(t *testing.T) {
		pfAbstain(t, pfOne(t, m, eduOnly(), pfToday, textQ("c", "Employment start year")), ReasonNoEntry, false)
	})
	t.Run("a duration is never a date component", func(t *testing.T) {
		for _, text := range []string{
			"How many years of work experience related to this position",
			"Years working in a role related to this role",
			"How long did you work at your last job (months)",
		} {
			pfAbstain(t, pfOne(t, m, past, pfToday, pfSelect("c", text, "0-1", "2-3", "4+")), ReasonNoEntry, false)
		}
	})
	t.Run("a consent checkbox about job opportunities is not a date", func(t *testing.T) {
		q := Question{Selector: "c", Text: "Do you agree to allow us to contact you about job opportunities for up to 2 years?", Kind: KindCheckbox, Options: opts("I agree")}
		pfAbstain(t, pfOne(t, m, past, pfToday, q), ReasonNoEntry, false)
	})
	t.Run("an availability question is not an employment date", func(t *testing.T) {
		pfAbstain(t, pfOne(t, m, past, pfToday, textQ("c", "When is the earliest you would be available to work a full-time role? Please input month and year.")), ReasonNoEntry, false)
	})
	t.Run("a start in the new job is not the last job's start", func(t *testing.T) {
		for _, text := range []string{"Preferred start month for this position", "Expected employment start year", "Desired start month at our company"} {
			pfAbstain(t, pfOne(t, m, past, pfToday, textQ("c", text)), ReasonNoEntry, false)
		}
	})
	t.Run("a month and year asked together are both written", func(t *testing.T) {
		pfWant(t, pfOne(t, m, past, pfToday, textQ("c", "Employment start month and year")), "June 2023")
		pfAbstain(t, pfOne(t, m, past, pfToday, pfSelect("c", "Employment end month and year", "August 2022", "August 2024")), ReasonNoEntry, false)
	})
	t.Run("both directions abstain", func(t *testing.T) {
		pfAbstain(t, pfOne(t, m, past, pfToday, textQ("c", "Employment start to end year")), ReasonNoEntry, false)
	})
}

func TestAnswerBank_CurrentRoleCompany_Compound(t *testing.T) {
	title := ent("current-job-title", "fixed", withAnswer("Software Engineer"))
	company := ent("current-company", "fixed", withAnswer("Example Corp"))
	m := pfMatcher(title, company)
	q := textQ("c", "Current role & company*")
	t.Run("both facts", func(t *testing.T) {
		r := pfOne(t, m, Facts{}, pfToday, q)
		pfWant(t, r, "Software Engineer at Example Corp")
		if r.AnswerKey != "current_role_company" || r.Source != SourceDerived {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("shapes", func(t *testing.T) {
		for _, text := range []string{"Current role and company", "Present position / employer:", "Current job title & organization"} {
			pfWant(t, pfOne(t, m, Facts{}, pfToday, textQ("c", text)), "Software Engineer at Example Corp")
		}
	})
	t.Run("blank entries fall back to the profile history", func(t *testing.T) {
		blankTitle := ent("current-job-title", "fixed", emptyBody(), func(e *Entry) { e.Placeholder = true })
		r := pfOne(t, pfMatcher(blankTitle, blankCompany()), alexFacts(), pfToday, q)
		pfWant(t, r, "Software Engineer at Example Corp")
	})
	t.Run("a missing fact abstains placeholder-empty", func(t *testing.T) {
		pfAbstain(t, pfOne(t, pfMatcher(), Facts{}, pfToday, q), ReasonPlaceholderEmpty, false)
		onlyTitle := Facts{Employment: []EmploymentFact{{Title: "Engineer", Employer: " "}}}
		pfAbstain(t, pfOne(t, pfMatcher(), onlyTitle, pfToday, q), ReasonPlaceholderEmpty, false)
	})
	t.Run("an entity section keeps its row filler", func(t *testing.T) {
		in := Question{Selector: "c", Text: "Current role & company", Kind: KindText, SectionPath: []string{"Work Experience 1"}}
		if r := pfOne(t, m, Facts{}, pfToday, in); r.Answer == "Software Engineer at Example Corp" {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("a history selector keeps its row filler", func(t *testing.T) {
		if r := pfOne(t, m, Facts{}, pfToday, textQ("workExperience-3--role", "Current role & company")); r.Answer == "Software Engineer at Example Corp" {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("a control with options is not a text box", func(t *testing.T) {
		pfAbstain(t, pfOne(t, m, Facts{}, pfToday, pfSelect("c", "Current role & company", "A", "B")), ReasonInputMismatch, false)
	})
}

func TestAnswerBank_CurrentCompany_TenureAndPriorEmployerAbstain(t *testing.T) {
	cases := []struct{ name, text string }{
		{"how long", "How long have you been with your current employer?"},
		{"how many years", "How many years have you worked for your company?"},
		{"years at", "Years at your employer"},
		{"length of employment", "What is the length of your employment with this company?"},
		{"employment duration", "Employer duration"},
		{"second employer", "Second most recent employer"},
		{"third company", "3rd company you worked for"},
		{"next most recent", "Next most recent employer"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := empMatcher(nil, blankCompany(c.text))
			r := pfOne(t, m, alexFacts(), pfToday, textQ("c", c.text))
			pfAbstain(t, r, ReasonContextPolicy, true)
			if r.Detail != DetailEmployerDetail {
				t.Fatalf("detail = %q", r.Detail)
			}
		})
	}
	t.Run("the employer name is still answered", func(t *testing.T) {
		m := empMatcher(nil, blankCompany("Current employer"))
		pfWant(t, pfOne(t, m, alexFacts(), pfToday, textQ("c", "Current employer")), "Example Corp")
	})
	t.Run("a notice-period question is not routed to the current company", func(t *testing.T) {
		m := empMatcher(nil, blankCompany("Current employer"))
		r := pfOne(t, m, alexFacts(), pfToday, textQ("c", "What is your notice period with your current employer?"))
		if r.EntryID == "current-company" {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("other entries are not guarded", func(t *testing.T) {
		g := ent("favorite-color", "fixed", withAnswer("Blue"), withExamples("How long have you been at your employer"))
		a, _, hit := employerDetailGuard(&Step{Q: Question{Text: "How long have you been at your employer"}}, &g)
		if hit || a.Reason != "" {
			t.Fatal("guard fired on another entry")
		}
	})
}

func TestAnswerBank_CurrentCompany_DerivedFromHistory(t *testing.T) {
	q := textQ("c", "Current employer")
	t.Run("the profile fills a blank entry", func(t *testing.T) {
		r := pfOne(t, empMatcher(nil, blankCompany("Current employer")), alexFacts(), pfToday, q)
		pfWant(t, r, "Example Corp")
		if r.Source != SourceDerived || r.EntryID != "current-company" {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("the user's own answer wins", func(t *testing.T) {
		own := ent("current-company", "fixed", withAnswer("Own Corp"), withExamples("Current employer"))
		r := pfOne(t, empMatcher(nil, own), alexFacts(), pfToday, q)
		pfWant(t, r, "Own Corp")
		if r.Source != SourceEntry {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("a placeholder that holds a typed value still answers it", func(t *testing.T) {
		own := ent("current-company", "fixed", withAnswer("Own Corp"), withExamples("Current employer"), func(e *Entry) { e.Placeholder = true })
		pfWant(t, pfOne(t, empMatcher(nil, own), alexFacts(), pfToday, q), "Own Corp")
	})
	t.Run("neither source", func(t *testing.T) {
		pfAbstain(t, pfOne(t, empMatcher(nil, blankCompany("Current employer")), Facts{}, pfToday, q), ReasonPlaceholderEmpty, false)
	})
	t.Run("a blank employer in the history", func(t *testing.T) {
		f := Facts{Employment: []EmploymentFact{{Employer: "  ", End: "present"}}}
		pfAbstain(t, pfOne(t, empMatcher(nil, blankCompany("Current employer")), f, pfToday, q), ReasonPlaceholderEmpty, false)
	})
	t.Run("the ask-me marker names no employer", func(t *testing.T) {
		if got := CurrentEmployerAnswer(AskMe, Facts{}); got != "" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("a listed employer binds an option", func(t *testing.T) {
		m := empMatcher(nil, blankCompany("Current employer"))
		pfWant(t, pfOne(t, m, alexFacts(), pfToday, pfSelect("c", "Current employer", "Acme Test", "Example Corp", "Other")), "Example Corp")
	})
	t.Run("an alias of the employer binds an option", func(t *testing.T) {
		m := empMatcher([]Employer{{Name: "Example Corp", Aliases: []string{"Example Corporation"}}}, blankCompany("Current employer"))
		pfWant(t, pfOne(t, m, alexFacts(), pfToday, pfSelect("c", "Current employer", "Acme Test", "Example Corporation")), "Example Corporation")
	})
	t.Run("an unlisted employer binds nothing", func(t *testing.T) {
		m := empMatcher(nil, blankCompany("Current employer"))
		pfAbstain(t, pfOne(t, m, alexFacts(), pfToday, pfSelect("c", "Current employer", "Acme Test", "Other Co")), ReasonInputMismatch, false)
	})
	t.Run("the blank entry is exempt from the empty-body abstain", func(t *testing.T) {
		e := blankCompany("Current employer")
		if !e.derived() || !e.BodyEmpty() {
			t.Fatal("current-company must be derived while blank")
		}
	})
}
