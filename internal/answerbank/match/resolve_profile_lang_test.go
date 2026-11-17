package match

import "testing"

func dLang(question string, mods ...func(*Entry)) Entry {
	e := Entry{ID: "language-proficiency", Kind: KindLanguageProficiency,
		Body: Body{Language: "English", Speaking: "Fluent", Listening: "Fluent", Reading: "Native", Writing: "Advanced"},
		Meta: Meta{Examples: []string{question}}}
	for _, m := range mods {
		m(&e)
	}
	return e
}

func langAnswer(t *testing.T, e Entry, q Question) Result {
	t.Helper()
	return pfOne(t, pfMatcher(e), Facts{}, Date{}, q)
}

func TestAnswerBank_Language_FieldDetectionWorkdayRowsAndOverall(t *testing.T) {
	cefr := opts("Beginner - A1", "Intermediate - B1", "Advanced - C1", "Proficient - C2")
	ordinary := opts("Excellent", "Good", "Poor")
	tests := []struct {
		name     string
		text     string
		section  []string
		opts     []Option
		selector string
		want     string
	}{
		{"language name", "Language*", nil, nil, "", "language"},
		{"named language without direction", "English", nil, nil, "", "language"},
		{"proficiency of a named language", "Indicate your proficiency of the English language:*", nil, nil, "", "speaking"},
		{"overall on a CEFR ladder, no section", "OVERALL*", nil, cefr, "", "speaking"},
		{"overall in a languages section", "OVERALL*", []string{"Languages 1"}, ordinary, "", "speaking"},
		{"overall on a Workday language row", "OVERALL*", nil, ordinary, "#language-6--3e82", "speaking"},
		{"overall on a language row the engine names", "OVERALL*", nil, ordinary, `[id="language-6--3e82"]`, "speaking"},
		{"overall on ordinary options outside a section", "OVERALL*", nil, ordinary, "", ""},
		{"bare Read in a languages section", "Read*", []string{"My Experience", "Languages 1"}, ordinary, "", "reading"},
		{"bare Speak on a language row id", "Speak*", nil, ordinary, "language-2--abc", "speaking"},
		{"bare Write on a CEFR ladder", "Write", nil, cefr, "", "writing"},
		{"bare Listen outside a languages context", "Listen", nil, ordinary, "", ""},
		{"written level means writing", "Written level (language)", nil, nil, "", "writing"},
		{"listening proficiency", "English listening level", nil, nil, "", "listening"},
		{"reading skill token without language context", "Reading", []string{"Languages"}, nil, "", "reading"},
		{"spoken proficiency with a language name", "Spoken French fluency", nil, nil, "", "speaking"},
		{"not a language question", "Years of experience", nil, ordinary, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := languageField(tc.text, tc.section, tc.opts, tc.selector); got != tc.want {
				t.Fatalf("languageField = %q want %q", got, tc.want)
			}
		})
	}
	// Through the pipeline: the answer comes from the detected field.
	const q = "Language*"
	r := langAnswer(t, dLang(q), Question{Selector: "s", Text: q, Kind: KindText})
	if r.Answer != "English" {
		t.Fatalf("%+v", r)
	}
	const o = "OVERALL*"
	r = langAnswer(t, dLang(o), Question{Selector: "s", Text: o, Kind: KindSelect, Options: cefr, SectionPath: nil})
	if r.ChosenLabel != "Advanced - C1" {
		t.Fatalf("%+v", r)
	}
	r = langAnswer(t, dLang(o), Question{Selector: "s", Text: o, Kind: KindSelect, Options: opts("Excellent", "Good", "Poor")})
	if r.Outcome != OutcomeAbstained {
		t.Fatalf("a bare OVERALL on ordinary options outside a languages section stays unanswered: %+v", r)
	}
}

func TestAnswerBank_Language_NamedOtherLanguageAbstains(t *testing.T) {
	const spanish = "What is your Spanish fluency?"
	r := langAnswer(t, dLang(spanish), pfSelect("s", spanish, "Basic", "Fluent", "Native"))
	if r.Outcome != OutcomeAbstained || !traceHas(r, "another language") {
		t.Fatalf("%+v", r)
	}
	const english = "What is your English fluency?"
	if r := langAnswer(t, dLang(english), pfSelect("s", english, "Basic", "Fluent", "Native")); r.ChosenLabel != "Fluent" {
		t.Fatalf("%+v", r)
	}
	const both = "Rate your fluency in English and Spanish"
	if r := langAnswer(t, dLang(both), pfSelect("s", both, "Basic", "Fluent", "Native")); r.Outcome != OutcomeAbstained || !traceHas(r, "another language") {
		t.Fatalf("one level for the entry's language and another one claims the other: %+v", r)
	}
	const either = "Rate your fluency in English or Spanish"
	if r := langAnswer(t, dLang(either), pfSelect("s", either, "Basic", "Fluent", "Native")); r.ChosenLabel != "Fluent" {
		t.Fatalf("either language is answered by the entry's: %+v", r)
	}
	if !langNamesOnlyOthers("Spanish fluency", "English") || langNamesOnlyOthers("fluency", "English") || langNamesOnlyOthers("english or french", "English") {
		t.Fatal("named-language guard")
	}
	// A Yes/No on fluency in the entry's language and another is Yes only
	// when either answers it.
	for text, want := range map[string]string{
		"Are you fluent in English?":                  "Yes",
		"Are you fluent in English or Spanish?":       "Yes",
		"Are you fluent in both English and Spanish?": "",
		"Are you fluent in English and Spanish?":      "",
	} {
		if r := langAnswer(t, dLang(text, func(e *Entry) { e.InputTypes = []string{InputYesNo} }), yesNoQ("s", text)); r.ChosenLabel != want {
			t.Fatalf("%q: want %q, got %+v", text, want, r)
		}
	}
	// An empty field value abstains instead of typing nothing.
	const eq = "What is your English speaking level?"
	if r := langAnswer(t, dLang(eq, func(e *Entry) { e.Speaking = "" }), textQ("s", eq)); r.Outcome != OutcomeAbstained || r.Reason != ReasonPlaceholderEmpty {
		t.Fatalf("%+v", r)
	}
}

func TestAnswerBank_Language_FluentMapsToC1NeverC2(t *testing.T) {
	cefr := opts("Beginner - A1", "Elementary - A2", "Intermediate - B1", "Upper Intermediate - B2", "Advanced - C1", "Proficient - C2")
	tests := []struct{ level, want string }{
		{"Fluent", "Advanced - C1"},
		{"Native", "Proficient - C2"},
		{"Bilingual", "Proficient - C2"},
		{"Professional", "Advanced - C1"},
		{"Intermediate", "Intermediate - B1"},
		{"Elementary", "Elementary - A2"},
		{"Basic", "Beginner - A1"},
		{"Conversational", ""},
	}
	for _, tc := range tests {
		got, ok := langCEFROption(tc.level, cefr)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("%q = %q,%v", tc.level, got, ok)
		}
	}
	if _, ok := langCEFROption("Fluent", opts("Basic", "Fluent")); ok {
		t.Error("a non-CEFR picker is untouched")
	}
	const q = "Overall English proficiency"
	r := langAnswer(t, dLang(q), Question{Selector: "s", Text: q, Kind: KindSelect, Options: opts("Advanced - C1", "Proficient - C2")})
	if r.ChosenLabel != "Advanced - C1" {
		t.Fatalf("%+v", r)
	}
	// The ladder-name field is not translated.
	const ln = "Language*"
	r = langAnswer(t, dLang(ln), Question{Selector: "s", Text: ln, Kind: KindSelect, Options: opts("English", "French")})
	if r.ChosenLabel != "English" {
		t.Fatalf("%+v", r)
	}
}

func TestAnswerBank_Language_NameFieldsAndModelQuestionsAreNotLanguageQuestions(t *testing.T) {
	for _, text := range []string{"Alternate Language First Name", "Have you fine-tuned large language models?"} {
		r := langAnswer(t, dLang(text), textQ("s", text))
		if r.Outcome != OutcomeAbstained || r.Answer != "" {
			t.Fatalf("%q: %+v", text, r)
		}
	}
}

// Datadog (Greenhouse, queue161): "Please select all the languages you speak
// fluently." over a multi-value listbox. The record holds one language, so the
// fluent one is selected for review; a lower level selects nothing.
func TestAnswerBank_Language_AListOfTheLanguagesSpokenFluently(t *testing.T) {
	q := Question{Selector: "s", Text: "Please select all the languages you speak fluently.", Kind: KindCustomListbox, InputType: InputMultiSelect,
		Options: opts("Arabic", "Cantonese", "English", "French", "Mandarin", "Spanish")}
	limited := func(e *Entry) { e.InputTypes = []string{"single_select", "yes_no"} }
	r := langAnswer(t, dLang("English language proficiency", limited), q)
	if r.Outcome != OutcomeFilledReview || r.ChosenLabel != "English" && (len(r.ChosenLabels) != 1 || r.ChosenLabels[0] != "English") {
		t.Fatalf("fluent English = %+v", r)
	}
	low := langAnswer(t, dLang("English language proficiency", limited, func(e *Entry) { e.Speaking = "Intermediate" }), q)
	if low.Outcome == OutcomeFilled || low.Outcome == OutcomeFilledReview {
		t.Fatalf("an intermediate level was selected as fluent: %+v", low)
	}
}
