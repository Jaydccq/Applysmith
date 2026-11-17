package answerbank

import (
	"errors"
	"strings"
	"testing"

	"applysmith/internal/answerbank/match"
)

func TestLoad_EmptyFileWarns(t *testing.T) {
	for _, text := range []string{"", "   \n", "# only a comment\n"} {
		r := mustParse(t, text)
		if len(r.Bank.Entries) != 0 || r.Bank.Version != 1 || len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "empty") {
			t.Fatalf("%q: %+v", text, r)
		}
	}
}

func TestLoad_VersionMismatchNamesVersion(t *testing.T) {
	_, err := Parse("answer-bank.yml", []byte("version: 2\nentries: []\n"))
	var le *LoadError
	if !errors.As(err, &le) || !strings.Contains(le.Message, "version 2") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Parse("answer-bank.yml", []byte("entries: []\n")); err == nil {
		t.Fatal("an absent version must fail")
	}
}

func TestLoad_UnknownTopLevelKeyIsError(t *testing.T) {
	_, err := Parse("answer-bank.yml", []byte("version: 1\nanswers: []\n"))
	var le *LoadError
	if !errors.As(err, &le) || le.Path != "answers" || le.Line != 2 {
		t.Fatalf("err = %#v", err)
	}
}

func TestLoad_DuplicateYAMLKeyIsError(t *testing.T) {
	text := bankText("  - id: a\n    description: d\n    kind: fixed\n    answer: one\n    answer: two\n")
	_, err := Parse("answer-bank.yml", []byte(text))
	var le *LoadError
	if !errors.As(err, &le) || le.File != "answer-bank.yml" || le.Path != "entries[0].answer" || le.Line != 8 {
		t.Fatalf("err = %#v", err)
	}
	if !strings.Contains(err.Error(), "answer-bank.yml: entries[0].answer (line 8)") {
		t.Fatalf("message = %q", err)
	}
}

func TestLoad_SchemaErrorsFailTheLoad(t *testing.T) {
	tests := map[string]string{
		"missing id":          bankText("  - description: d\n    kind: fixed\n    answer: x\n"),
		"wrong type":          bankText("  - id: a\n    kind: fixed\n    answer: [x]\n"),
		"entries not a list":  "version: 1\nentries: {}\n",
		"entry not a mapping": bankText("  - just text\n"),
		"syntax error":        "version: 1\nentries: [\n",
		"unknown category":    bankText(fixedYAML("a", "x", "category: hobbies")),
	}
	for name, text := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Parse("answer-bank.yml", []byte(text))
			var le *LoadError
			if !errors.As(err, &le) {
				t.Fatalf("err = %v", err)
			}
			if name == "unknown category" && !strings.Contains(err.Error(), "identity, contact") {
				t.Fatalf("the error must name the allowed categories: %v", err)
			}
		})
	}
}

func TestLoad_DuplicateIDFirstWins(t *testing.T) {
	r := mustParse(t, bankText(fixedYAML("relocation-willingness", "first"), fixedYAML("relocation-willingness", "second")))
	if len(r.Bank.Entries) != 1 || r.Bank.Entries[0].Answer != "first" || !hasWarning(r, "duplicate entry id", "relocation-willingness") {
		t.Fatalf("%+v", r)
	}
}

func TestLoad_InvalidFirstDuplicateStillShadows(t *testing.T) {
	bad := "  - id: a\n    description: d\n    kind: hologram\n"
	r := mustParse(t, bankText(bad, fixedYAML("a", "valid")))
	if len(r.Bank.Entries) != 0 || !hasWarning(r, "unknown kind") || !hasWarning(r, "duplicate entry id") {
		t.Fatalf("%+v", r)
	}
}

func TestLoad_SharedAnswerKeyNoWarning(t *testing.T) {
	r := mustParse(t, bankText(fixedYAML("work-auth-us", "x", "answer_key: work_authorized"), fixedYAML("work-auth-us-any-employer", "y", "answer_key: work_authorized")))
	if len(r.Bank.Entries) != 2 || len(r.Warnings) != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestLoad_UnknownKindDroppedWithWarning(t *testing.T) {
	r := mustParse(t, bankText("  - id: a\n    description: d\n    kind: hologram\n", fixedYAML("b", "x")))
	if len(r.Bank.Entries) != 1 || r.Bank.Entries[0].ID != "b" || !hasWarning(r, `"a"`, "hologram") {
		t.Fatalf("%+v", r)
	}
}

func TestLoad_KindBodyRequiredFields(t *testing.T) {
	head := "  - id: a\n    description: d\n    kind: "
	tests := []struct {
		name string
		text string
		kept bool
	}{
		{"fixed with empty answer", head + "fixed\n    answer: \"\"\n", true},
		{"fixed without answer", head + "fixed\n", false},
		{"pick-option complete", head + "pick-option\n    preferred: []\n    fallback_text: \"\"\n", true},
		{"pick-option without fallback_text", head + "pick-option\n    preferred: [A]\n", false},
		{"compensation complete", head + "compensation-range\n    minimum: \"\"\n    maximum: \"\"\n    default_answer: \"\"\n    currency: \"\"\n", true},
		{"compensation incomplete", head + "compensation-range\n    minimum: \"1\"\n", false},
		{"language complete", head + "language-proficiency\n    language: \"\"\n    speaking: \"\"\n    listening: \"\"\n    reading: \"\"\n    writing: \"\"\n", true},
		{"language incomplete", head + "language-proficiency\n    language: x\n", false},
		{"employer-history complete", head + "employer-history\n    answer_if_match: \"\"\n    answer_if_no_match: \"\"\n", true},
		{"employer-history incomplete", head + "employer-history\n    answer_if_match: \"\"\n", false},
		{"auto-acknowledge has no body", head + "auto-acknowledge\n", true},
		{"multi-select may omit selected", head + "multi-select\n", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := mustParse(t, bankText(tc.text))
			if (len(r.Bank.Entries) == 1) != tc.kept || (!tc.kept && !hasWarning(r, "missing")) {
				t.Fatalf("%+v", r)
			}
		})
	}
}

func TestLoad_MultiSelectAbsentVsEmpty(t *testing.T) {
	head := "  - id: %s\n    description: d\n    kind: multi-select\n"
	r := mustParse(t, bankText(strings.Replace(head, "%s", "absent", 1), strings.Replace(head, "%s", "empty", 1)+"    selected: []\n"))
	absent, _ := r.Bank.Find("absent")
	empty, _ := r.Bank.Find("empty")
	if absent.Selected != nil || empty.Selected == nil || len(empty.Selected) != 0 {
		t.Fatalf("absent = %#v, empty = %#v", absent.Selected, empty.Selected)
	}
	needs := match.NeedsAnswer(r.Bank)
	if len(needs.Unanswered) != 1 || needs.Unanswered[0].ID != "absent" {
		t.Fatalf("unanswered = %+v", needs.Unanswered)
	}
}

func TestLoad_UnknownInputTypeWarns(t *testing.T) {
	r := mustParse(t, bankText(fixedYAML("a", "x", "input_types: [text, hologram]")))
	e, _ := r.Bank.Find("a")
	if len(e.InputTypes) != 1 || e.InputTypes[0] != "text" || !hasWarning(r, `"a"`, "hologram") {
		t.Fatalf("%+v %v", e.InputTypes, r.Warnings)
	}
}

func TestLoad_UnknownStateWarns(t *testing.T) {
	r := mustParse(t, bankText(fixedYAML("a", "x", "state: inactive")))
	e, _ := r.Bank.Find("a")
	if e.State != match.StateActive || !hasWarning(r, `"a"`, "inactive") {
		t.Fatalf("%q %v", e.State, r.Warnings)
	}
}

func TestLoad_ConfidenceRangeValidated(t *testing.T) {
	r := mustParse(t, bankText(fixedYAML("a", "x", "confidence: 1.5"), fixedYAML("b", "x", "confidence: 0.8", "confidence_floor: 0.4")))
	a, _ := r.Bank.Find("a")
	b, _ := r.Bank.Find("b")
	if a.Confidence != nil || !hasWarning(r, `"a"`, "confidence") {
		t.Fatalf("a = %v, %v", a.Confidence, r.Warnings)
	}
	if floor, ok := b.Floor(); b.Confidence == nil || !ok || floor != 0.70 {
		t.Fatalf("b floor = %v", floor)
	}
}

func TestLoad_NumericThresholdSanitized(t *testing.T) {
	good := "numeric_threshold: {extractor: days_per_week_onsite, max_value_profile_key: preferences.max_days_in_office, above_answer: 'No', on_parse_miss: review}"
	r := mustParse(t, bankText(
		fixedYAML("good", "Yes", good),
		fixedYAML("bad", "Yes", "numeric_threshold: {extractor: weekly_hours}"),
		fixedYAML("miss", "Yes", "numeric_threshold: {extractor: days_per_week_onsite, on_parse_miss: loud}")))
	g, _ := r.Bank.Find("good")
	bad, _ := r.Bank.Find("bad")
	miss, _ := r.Bank.Find("miss")
	if g.NumericThreshold == nil || g.NumericThreshold.MaxValue != nil || g.NumericThreshold.MissMode() != "review" {
		t.Fatalf("good = %+v", g.NumericThreshold)
	}
	if bad.NumericThreshold != nil || !hasWarning(r, `"bad"`, "numeric_threshold") {
		t.Fatalf("bad entry keeps its threshold or no warning: %v", r.Warnings)
	}
	if miss.NumericThreshold == nil || miss.NumericThreshold.MissMode() != "no-op" || !hasWarning(r, `"miss"`, "on_parse_miss") {
		t.Fatalf("miss = %+v", miss.NumericThreshold)
	}
}

func TestLoad_LocationPolicySanitized(t *testing.T) {
	r := mustParse(t, bankText(
		fixedYAML("good", "x", "location_policy: {predicate: currently_at, if_true: 'Yes', if_false: 'No'}"),
		fixedYAML("bad", "x", "location_policy: {predicate: lives_near}")))
	g, _ := r.Bank.Find("good")
	bad, _ := r.Bank.Find("bad")
	if g.LocationPolicy == nil || g.LocationPolicy.IfTrue != "Yes" || bad.LocationPolicy != nil || !hasWarning(r, `"bad"`, "location_policy") {
		t.Fatalf("good = %+v bad = %+v %v", g.LocationPolicy, bad.LocationPolicy, r.Warnings)
	}
}

func TestLoad_InvalidNegationPatternDropped(t *testing.T) {
	r := mustParse(t, bankText(fixedYAML("a", "x", `question_negation_terms: ["(?<=a)b", "\\bnever\\b", "([unclosed"]`)))
	e, _ := r.Bank.Find("a")
	if len(e.NegationPatterns) != 1 || !e.NegationPatterns[0].MatchString("never") || !hasWarning(r, `"a"`, "question_negation_terms") {
		t.Fatalf("patterns = %v, warnings = %v", e.NegationPatterns, r.Warnings)
	}
	if len(e.QuestionNegationTerms) != 3 {
		t.Fatal("the written terms must be kept so a save loses nothing")
	}
}

// A loaded question_negation_terms pattern is tested on the question without
// regard to case, as the matcher compiles a pattern it is handed uncompiled.
func TestLoad_NegationPatternIgnoresCase(t *testing.T) {
	r := mustParse(t, bankText(fixedYAML("first-name", "Alex", "category: other", "examples: [First name, Legal first name]", `question_negation_terms: ['legal\b']`)))
	ask := func(text string) match.Result {
		return matchFacts(t, r.Bank, match.Facts{}, match.Date{}, match.Question{Selector: "#q", Text: text, Kind: match.KindText, Required: true})
	}
	if got := ask("First name"); got.Answer != "Alex" {
		t.Fatalf("the plain question answers from the entry: %+v", got)
	}
	if got := ask("Legal first name"); got.Outcome != match.OutcomeAbstained || !strings.Contains(strings.Join(got.Trace, "\n"), "question-negation matched") {
		t.Fatalf("a capitalised hit abstains on the negation term: %+v", got)
	}
}

func TestLoad_MetadataSanitization(t *testing.T) {
	r := mustParse(t, bankText(fixedYAML("a", "x",
		"linked_explain_id: '  a-reason  '",
		"skill_mode: profile_skill", "skill_terms: ['Go, go, Rust', ' rust ']",
		"allowed_options: ['Red  Pill', 'red pill', '', 'Blue']",
		"captured_scope: [{host: jobs.example.test, question: Q, options: [A]}, {host: '', question: Q}]",
		"hand_copied_field: 1")))
	e, _ := r.Bank.Find("a")
	if e.LinkedExplainID != "a-reason" || e.SkillMode != "profile_skill" || strings.Join(e.SkillTerms, "|") != "Go|Rust" ||
		strings.Join(e.AllowedOptions, "|") != "Red Pill|Blue" || len(e.CapturedScope) != 2 {
		t.Fatalf("%+v", e.Meta)
	}
	if e.Admits("jobs.example.test", "q", []string{"a"}) != true || e.Admits("other.example.test", "q", nil) {
		t.Fatal("the valid scope item still admits and the broken one admits nothing")
	}
	if !hasWarning(r, "hand_copied_field") || !hasWarning(r, "captured_scope") || !hasWarning(r, "allowed_options") ||
		!hasWarning(r, "skill_terms") || !hasWarning(r, "linked_explain_id") {
		t.Fatalf("warnings = %v", r.Warnings)
	}
}

// ST-021: the old loader also dropped any option without a Latin letter and any
// option with a CJK character, to clean a bilingual field-of-study list file.
// Lists here are the user's own, so only whitespace, blanks and repeats go; a
// numeric option ("5+") and an option on a form in another language stay.
func TestAnswerBank_Load_AllowedOptionsCleaned(t *testing.T) {
	r := mustParse(t, bankText(fixedYAML("a", "x",
		"allowed_options: ['Red  Pill', 'red pill', '', '5+', '10-15', '\u8ba1\u7b97\u673a\u79d1\u5b66', 'Computer Science (\u8ba1\u7b97\u673a\u79d1\u5b66)', '\uff29\uff34', 'Blue']")))
	e, _ := r.Bank.Find("a")
	if got := strings.Join(e.AllowedOptions, "|"); got != "Red Pill|5+|10-15|\u8ba1\u7b97\u673a\u79d1\u5b66|Computer Science (\u8ba1\u7b97\u673a\u79d1\u5b66)|\uff29\uff34|Blue" {
		t.Fatalf("allowed options = %q", got)
	}
}

func TestLoad_CategoryRoundTrips(t *testing.T) {
	r := mustParse(t, bankText(fixedYAML("a", "x", "category: identity")))
	if e, _ := r.Bank.Find("a"); e.Category != "identity" {
		t.Fatalf("category = %q", e.Category)
	}
}

func overrideFile(overrides string) string {
	return "version: 1\nentries:\n" + fixedYAML("x", "base", "category: other", "examples: [q]") + "overrides:\n" + overrides
}

// An override of an entry the load does not give is ignored with a warning: a
// shipped default the file holds is the file's, also when the load drops it.
func TestOverrides_UnknownIDIgnoredWithWarning(t *testing.T) {
	for id, text := range map[string]string{
		"ghost":         overrideFile("  example.test:\n    - {id: ghost, answer: boo}\n"),
		"date-of-birth": "version: 1\nentries:\n  - {id: date-of-birth, description: d, kind: hologram}\noverrides:\n  example.test:\n    - {id: date-of-birth, answer: boo}\n",
	} {
		if r := mustParse(t, text); !hasWarning(r, "unknown entry id", id) {
			t.Errorf("%s: warnings = %v", id, r.Warnings)
		}
	}
}

func TestOverrides_InvalidKindChangeWarns(t *testing.T) {
	r := mustParse(t, overrideFile("  example.test:\n    - {id: x, kind: pick-option, preferred: [A]}\n"))
	if !hasWarning(r, "ignored", "without stating") {
		t.Fatalf("warnings = %v", r.Warnings)
	}
	if got := r.Bank.EntriesForHost("example.test")[0]; got.Kind != match.KindFixed || got.Answer != "base" {
		t.Fatalf("base changed: %+v", got)
	}
}

func TestOverrides_KindChangeKeepsMetadata(t *testing.T) {
	r := mustParse(t, overrideFile("  example.test:\n    - {id: x, kind: pick-option, preferred: [A], fallback_text: A}\n"))
	got := r.Bank.EntriesForHost("example.test")[0]
	if got.Kind != match.KindPickOption || got.Category != "other" || got.Examples[0] != "q" || len(r.Warnings) != 0 {
		t.Fatalf("%+v %v", got, r.Warnings)
	}
}

func TestOverrides_EmptyListIsAccepted(t *testing.T) {
	// The workspace seed template writes an empty list.
	if r := mustParse(t, "version: 1\nemployers: []\nentries: []\noverrides: []\n"); len(r.Bank.Overrides) != 0 || len(r.Warnings) != 0 {
		t.Fatalf("%+v", r)
	}
}
