package match_test

import (
	"testing"

	"applysmith/internal/answerbank/match"
)

func xcBLang(language, speaking, writing string) xcBSet {
	return func(e *match.Entry) {
		e.Language, e.Speaking, e.Writing, e.Listening, e.Reading, e.Placeholder = language, speaking, writing, speaking, writing, false
	}
}

// xcBWith applies several setters to one entry.
func xcBWith(fs ...xcBSet) xcBSet {
	return func(e *match.Entry) {
		for _, f := range fs {
			f(e)
		}
	}
}

func xcBEmployment() match.Facts {
	f := xcBFacts()
	f.Employment = []match.EmploymentFact{{Employer: "Acme Test", Title: "Intern", Start: "2023-06", End: "2023-08"}}
	return f
}

func TestXC_LanguageEmployerMulti(t *testing.T) {
	// The shipped language entry lists single_select only; a free-text
	// "which languages" box needs the user to allow text.
	lang := map[string]xcBSet{"language-proficiency": xcBWith(xcBLang("English", "Fluent", "Intermediate"), xcBTypes("single_select", "text"))}
	history := map[string]xcBSet{"employer-history": xcBWith(xcBHistory("Yes", "No"), xcBTypes("single_select", "yes_no", "checkbox", "text"))}
	firms := map[string]xcBSet{"audit-firms-worked-at": xcBWith(xcBSel(), xcBTypes("multi_select", "yes_no", "checkbox"))}
	cases := []xcBCase{
		// XC-041: language questions.
		{name: "XC-041 alternate first name", sets: lang, facts: xcBFacts(), q: xcBText("Alternate/Local Language First Name"), wantNone: true, notEntry: "language-proficiency"},
		{name: "XC-041 alternate last name", sets: lang, facts: xcBFacts(), q: xcBText("Alternate/Local Language Last Name"), wantNone: true, notEntry: "language-proficiency"},
		{name: "XC-041 directionless proficiency uses speaking", sets: lang, facts: xcBFacts(),
			q:     xcBQ("Indicate your proficiency of the English language", match.KindSelect, "Native", "Fluent", "Intermediate", "Basic"),
			entry: "language-proficiency", label: "Fluent"},
		{name: "XC-041 level of fluency", sets: lang, facts: xcBFacts(),
			q:     xcBQ("* What is your level of fluency English?", match.KindSelect, "A1 - Beginner", "A2 - Elementary", "B1 - Intermediate", "B2 - Upper intermediate", "C1 - Advanced", "C2 - Proficient"),
			entry: "language-proficiency", label: "C1 - Advanced"},
		{name: "XC-041 languages spoken", sets: map[string]xcBSet{"language-proficiency": lang["language-proficiency"], "languages-spoken": xcBSel("English", "Mandarin")}, facts: xcBFacts(),
			q: xcBText("What languages do you speak?"), entry: "languages-spoken", answer: "English, Mandarin"},
		{name: "XC-041 written level means writing", sets: lang, facts: xcBFacts(),
			q:     xcBQ("Written level of the English language", match.KindSelect, "Native", "Fluent", "Intermediate", "Basic"),
			entry: "language-proficiency", label: "Intermediate"},
		{name: "XC-041 prose ladder binds speaking", sets: lang, facts: xcBFacts(),
			q: xcBQ("English proficiency", match.KindSelect,
				"Professional/Fluent: can conduct business", "Intermediate: can hold a conversation", "Beginner/None: a few phrases"),
			entry: "language-proficiency", label: "Professional/Fluent: can conduct business"},
	}
	// XC-042: the employer-history phrasing family answers No for an employer
	// that is not in the employment history.
	for _, f := range []struct{ name, text string }{
		{"XC-042 phrasing 1", "Have you ever worked for Example Corp?"},
		{"XC-042 phrasing 2", "Have you previously worked at Example Corp?"},
		{"XC-042 phrasing 3", "Have you worked for Example Corp before?"},
		{"XC-042 phrasing 4", "Have you worked for Example Corp in the past?"},
		{"XC-042 phrasing 5", "Have you worked as an intern for Example Corp?"},
		{"XC-042 phrasing 6", "Have you ever been an employee of Example Corp?"},
		{"XC-042 phrasing 7", "Were you employed by Example Corp?"},
		{"XC-042 phrasing 8", "Are you a current or former Example Corp employee?"},
		{"XC-042 phrasing 9", "Have you previously provided services (employee, vendor, consultant) for Example Corp?"},
	} {
		cases = append(cases, xcBCase{name: f.name, sets: history, facts: xcBEmployment(), q: xcBYesNo(f.text),
			entry: "employer-history", label: "No"})
	}
	cases = append(cases,
		xcBCase{name: "XC-042 listed employer answers Yes", sets: history, facts: xcBEmployment(), q: xcBYesNo("Have you ever worked for Acme Test?"),
			entry: "employer-history", label: "Yes"},
		xcBCase{name: "XC-042 technology is not an employer", sets: history, facts: xcBEmployment(), q: xcBYesNo("Have you worked with TypeScript?"),
			notEntry: "employer-history", wantNone: true},
		xcBCase{name: "XC-042 clients are not an employer", sets: history, facts: xcBEmployment(), q: xcBYesNo("Have you provided services to clients in the healthcare industry?"),
			notEntry: "employer-history", wantNone: true},
		xcBCase{name: "XC-042 relative is not an employer", sets: history, facts: xcBEmployment(), q: xcBYesNo("Are you a close relative of an Example Corp employee?"),
			notEntry: "employer-history", wantNone: true},
		xcBCase{name: "XC-042 text control", sets: history, facts: xcBEmployment(), q: xcBText("Have you previously worked for Example Corp?"),
			entry: "employer-history", answer: "No"},

		// XC-043: an empty selection answers No to a firm-list question.
		xcBCase{name: "XC-043 firm list with an empty selection", sets: firms, facts: xcBFacts(), q: xcBYesNo("Are you a current or former employee of Deloitte or PwC?"),
			label: "No"},
	)
	xcBRunCases(t, cases)
}
