package match

import (
	"regexp"
	"testing"
)

// dFixed builds a curated fixed entry whose example is the question it answers.
func dFixed(id, answer, question string, mods ...func(*Entry)) Entry {
	e := Entry{ID: id, Kind: KindFixed, Body: Body{Answer: answer}, Meta: Meta{Examples: []string{question}}}
	for _, m := range mods {
		m(&e)
	}
	return e
}

func TestAnswerBank_QuestionNegationTerms_HitAbstainsInvalidDroppedAtLoad(t *testing.T) {
	const q = "What is your preferred name?"
	hit := dFixed("preferred-name", "Alex", q, func(e *Entry) {
		e.NegationPatterns = []*regexp.Regexp{regexp.MustCompile(`(?i)legal\b`), regexp.MustCompile(`(?i)preferred`)}
	})
	r := pfOne(t, pfMatcher(hit), Facts{}, Date{}, textQ("s", q))
	if r.Outcome != OutcomeAbstained || !traceHas(r, "question-negation matched") {
		t.Fatalf("a pattern hit abstains: %+v", r)
	}
	// A pattern RE2 cannot compile (lookbehind) is dropped by the loader, which
	// leaves its text and no compiled pattern: it is skipped, never an error.
	invalid := dFixed("preferred-name", "Alex", q, func(e *Entry) { e.QuestionNegationTerms = []string{`(?<=x)y`} })
	if r := pfOne(t, pfMatcher(invalid), Facts{}, Date{}, textQ("s", q)); r.Answer != "Alex" {
		t.Fatalf("invalid pattern is dropped: %+v", r)
	}
}

func TestAnswerBank_ExceptionTerms_WholeTokenAndAliasExpansion(t *testing.T) {
	const q = "Do you have friends or relatives who work at Alphabet Holdings?"
	mk := func(terms ...string) Entry {
		return dFixed("relatives-at-employer", "No", q, func(e *Entry) { e.AnswerIfException = "Yes"; e.ExceptionTerms = terms })
	}
	tests := []struct {
		name  string
		terms []string
		want  string
	}{
		{"listed alias of the user's company", []string{"Google"}, "Yes"},
		{"the literal name still matches", []string{"Alphabet"}, "Yes"},
		{"other company does not match", []string{"Acme Test"}, "No"},
		{"token inside a longer word does not match", []string{"Alpha"}, "No"},
		{"city alias expands both ways", []string{"NYC"}, "No"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := pfOne(t, pfMatcher(mk(tc.terms...)), Facts{}, Date{}, yesNoQ("s", q))
			if r.Answer != tc.want {
				t.Fatalf("%+v", r)
			}
		})
	}
	cityQ := "Are you willing to be in the Manhattan office?"
	city := dFixed("office-attendance-ok", "Yes", cityQ, func(e *Entry) { e.AnswerIfException = "No"; e.ExceptionTerms = []string{"NYC"} })
	if r := pfOne(t, pfMatcher(city), Facts{}, Date{}, yesNoQ("s", cityQ)); r.Answer != "No" {
		t.Fatalf("a city term expands to its metro variants: %+v", r)
	}
}

func TestAnswerBank_NumericThreshold_AboveWithinParseMiss(t *testing.T) {
	maxDays := 3
	mk := func(miss string, above string) Entry {
		e := dFixed("office-attendance-ok", "Yes", "x", func(e *Entry) {
			e.NumericThreshold = &NumericThreshold{Extractor: ExtractorDaysPerWeekOnsite, AboveAnswer: above, OnParseMiss: miss, MaxValue: &maxDays}
		})
		return e
	}
	tests := []struct {
		name, text, miss, above string
		want                    string
		abstain                 bool
	}{
		{"above answers the above answer", "This role is on site five days a week. Can you do that?", "", "No", "No", false},
		{"above without an above answer falls to the entry answer", "This role is in the office 4 days per week. Is that fine?", "", "", "Yes", false},
		{"within falls through", "We expect two days in the office. Is that fine?", "", "No", "Yes", false},
		{"parse miss falls through by default", "Is the office location acceptable?", "", "No", "Yes", false},
		{"parse miss review falls through", "Is the office location acceptable?", ParseMissReview, "No", "Yes", false},
		{"parse miss abstains on request", "Is the office location acceptable?", ParseMissAbstain, "No", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := mk(tc.miss, tc.above)
			e.Examples = []string{tc.text}
			r := pfOne(t, pfMatcher(e), Facts{}, Date{}, textQ("s", tc.text))
			if tc.abstain != (r.Outcome == OutcomeAbstained) || (!tc.abstain && r.Answer != tc.want) {
				t.Fatalf("%+v", r)
			}
		})
	}
	t.Run("no stamped maximum is inert", func(t *testing.T) {
		e := mk("", "No")
		e.NumericThreshold.MaxValue = nil
		e.Examples = []string{"On site five days a week?"}
		if r := pfOne(t, pfMatcher(e), Facts{}, Date{}, textQ("s", "On site five days a week?")); r.Answer != "Yes" {
			t.Fatalf("%+v", r)
		}
	})
}

func TestAnswerBank_Residence_CountryBecomesYesOnYesNoControl(t *testing.T) {
	const q = "Are you currently based in the USA?"
	e := dFixed("currently-living-us-or-canada", "United States", q)
	if r := pfOne(t, pfMatcher(e), Facts{}, Date{}, yesNoQ("s", q)); r.Answer != "Yes" || r.ChosenLabel != "Yes" {
		t.Fatalf("a US residence reads Yes: %+v", r)
	}
	elsewhere := dFixed("currently-living-us-or-canada", "Exampleland", q)
	if r := pfOne(t, pfMatcher(elsewhere), Facts{}, Date{}, yesNoQ("s", q)); r.Outcome != OutcomeAbstained {
		t.Fatalf("a country that is not US or Canada is never an affirmative: %+v", r)
	}
	const tq = "Which country do you currently live in?"
	text := dFixed("currently-living-us-or-canada", "Canada", tq)
	if r := pfOne(t, pfMatcher(text), Facts{}, Date{}, textQ("s", tq)); r.Answer != "Canada" {
		t.Fatalf("a text control keeps the country: %+v", r)
	}
}

func TestAnswerBank_Placeholder_EmptyBodyPerKind(t *testing.T) {
	const q = "What is your example value?"
	blank := func(kind Kind, b Body) Entry {
		return Entry{ID: "example-entry", Kind: kind, Body: b, Meta: Meta{Examples: []string{q}, Placeholder: true, NeedsReview: true, InputTypes: []string{InputText}}}
	}
	tests := []struct {
		name string
		e    Entry
		want Reason
	}{
		{"fixed empty", blank(KindFixed, Body{}), ReasonPlaceholderEmpty},
		{"fixed whitespace only", blank(KindFixed, Body{Answer: "   "}), ReasonPlaceholderEmpty},
		{"compensation all empty", blank(KindCompensationRange, Body{}), ReasonPlaceholderEmpty},
		{"language all empty", blank(KindLanguageProficiency, Body{}), ReasonPlaceholderEmpty},
		{"employer-history all empty", blank(KindEmployerHistory, Body{}), ReasonPlaceholderEmpty},
		{"multi-select absent", blank(KindMultiSelect, Body{}), ReasonPlaceholderEmpty},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := pfOne(t, pfMatcher(tc.e), Facts{}, Date{}, textQ("s", q))
			if r.Outcome != OutcomeAbstained || r.Reason != tc.want {
				t.Fatalf("%+v", r)
			}
		})
	}
	t.Run("a partial compensation body is not empty", func(t *testing.T) {
		e := blank(KindCompensationRange, Body{Currency: "USD"})
		e.NeedsReview = false
		if r := pfOne(t, pfMatcher(e), Facts{}, Date{}, textQ("s", q)); traceHas(r, "has an empty body") {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("an empty multi-select list is an answer", func(t *testing.T) {
		e := blank(KindMultiSelect, Body{Selected: []string{}})
		e.NeedsReview = false
		if r := pfOne(t, pfMatcher(e), Facts{}, Date{}, textQ("s", q)); traceHas(r, "has an empty body") {
			t.Fatalf("%+v", r)
		}
	})
}

func TestAnswerBank_PickOption_TextControlEmptyFallbackAbstains(t *testing.T) {
	cases := []struct {
		fallback  string
		preferred []string
		want      string
		ok        bool
	}{
		{"", nil, "", false},
		{"   ", []string{"  "}, "", false},
		{"", []string{"Referral", "LinkedIn"}, "", false},
		{"", []string{"Referral"}, "Referral", true},
		{"Employee referral", []string{"Referral"}, "Employee referral", true},
	}
	for _, tc := range cases {
		e := Entry{ID: "job-source", Kind: KindPickOption, Body: Body{Preferred: tc.preferred, FallbackText: tc.fallback}}
		got, ok := pickOptionText(&e)
		if got != tc.want || ok != tc.ok {
			t.Errorf("fallback %q preferred %q = %q,%v", tc.fallback, tc.preferred, got, ok)
		}
	}
	if _, ok := pickOptionText(&Entry{Kind: KindFixed, Body: Body{FallbackText: "x"}}); ok {
		t.Error("only a pick-option entry has a text form")
	}
}

func TestAnswerBank_Derive_QualifiedAffirmativeWithoutTieBreakAbstainsAmbiguous(t *testing.T) {
	const q = "Are you legally authorized to work in the United States?"
	e := dFixed("work-auth-us", "Yes", q)
	r := pfOne(t, pfMatcher(e), Facts{AuthorizedCountries: []string{"US"}}, Date{}, pfSelect("s", q,
		"Yes - I am a citizen", "Yes - I hold a green card", "Yes - I hold a valid visa", "No"))
	if r.Outcome != OutcomeAbstained || r.Reason != ReasonAmbiguous {
		t.Fatalf("a tie between distinct qualified affirmatives needs the standing: %+v", r)
	}
}

// P&G: a Yes/No asking to share personal information with advertising
// partners is never opted into.
func TestAnswerBank_AdvertisingShareIsDeclined(t *testing.T) {
	m := pfMatcher()
	share := yesNoQ("a", "I would like P&G to share my personal information with its advertising partners to show me news and updates about P&G careers. Your decision to opt-in or opt-out will not disqualify you.")
	if r := pfOne(t, m, alexFacts(), pfDate(2026, 10, 31), share); r.Answer != "No" {
		t.Errorf("%+v, want No", r)
	}
	if r := pfOne(t, m, alexFacts(), pfDate(2026, 10, 31), yesNoQ("b", "May we share your resume with our hiring partners?")); r.Answer == "No" && r.Outcome == OutcomeFilled {
		t.Errorf("a resume shared with hiring partners is not an advertising opt-in: %+v", r)
	}
}

// RWE (SuccessFactors): who may see the profile and resume is answered with the
// one choice that keeps them to the jobs applied to; a list with no such choice
// is left to the user.
func TestAnswerBank_ProfileVisibilityKeepsToTheJobsAppliedTo(t *testing.T) {
	m := pfMatcher()
	q := "Allow My Profile and Resume to be visible or shared with:"
	r := pfOne(t, m, alexFacts(), pfDate(2026, 10, 31), pfSelect("v", q,
		"Any RWE company recruiter worldwide", "Any RWE company recruiter in my country/region of residence", "Only RWE recruiters managing jobs I apply to"))
	if r.ChosenLabel != "Only RWE recruiters managing jobs I apply to" {
		t.Errorf("%+v, want the jobs-applied-to choice", r)
	}
	if r := pfOne(t, m, alexFacts(), pfDate(2026, 10, 31), pfSelect("w", q, "Any recruiter worldwide", "Any recruiter in my country")); r.Outcome == OutcomeFilled || r.Outcome == OutcomeFilledReview {
		t.Errorf("no choice keeps the profile to the jobs applied to: %+v", r)
	}
}

// A name that ends in a symbol or holds a letter outside ASCII is still a
// whole name: the boundary is any character that is not a letter, a digit or
// an underscore.
func TestAnswerBank_Fixed_AWholeNameMayEndInASymbol(t *testing.T) {
	opts := []Option{{Label: "Rust"}, {Label: "C++"}, {Label: "C#"}, {Label: "C"}}
	for answer, want := range map[string]string{
		"Mostly C++ these days, some Rust": "C++",
		"C# at work, C++ at home":          "C#",
		"Rust, then C":                     "Rust",
		"We write Kotlin":                  "",
	} {
		if o, ok := toolNamedFirst(answer, opts); o.Label != want || ok != (want != "") {
			t.Errorf("toolNamedFirst(%q) = %q, %v; want %q", answer, o.Label, ok, want)
		}
	}
	e := Entry{ID: "ecole-alumni", Description: "A graduate of the École 42 program", Meta: Meta{Examples: []string{"Did you attend the Founders' Fellowship?"}}}
	for _, c := range []struct {
		words []string
		want  bool
	}{
		{[]string{"école"}, true},
		{[]string{"founders'"}, true},
		{[]string{"cole"}, false},
		{[]string{"found"}, false},
		{nil, false},
	} {
		if got := e.namesProgram(c.words); got != c.want {
			t.Errorf("namesProgram(%q) = %t, want %t", c.words, got, c.want)
		}
	}
}
