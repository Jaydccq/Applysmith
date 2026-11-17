package answerbank

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"applysmith/internal/answerbank/match"
)

func floatPtr(v float64) *float64 { return &v }

func fixedEntry(id string, mutate func(*match.Entry)) match.Entry {
	e := match.Entry{ID: id, Description: "d", Kind: match.KindFixed, Body: match.Body{Answer: "x"}, Meta: match.Meta{Category: match.CategoryOther}}
	if mutate != nil {
		mutate(&e)
	}
	return e
}

func validateOne(e match.Entry) error {
	return Validate(match.Bank{Version: 1, Entries: []match.Entry{e}})
}

// Findings H1: a captured scope that cannot be read never widens to everywhere.

func TestScope_InvalidItemKeepsTheEntryScopedAndAdmitsNothing(t *testing.T) {
	r := mustParse(t, bankText(fixedYAML("a", "x", "captured_scope: [{host: '', question: Q}]")))
	e, ok := r.Bank.Find("a")
	if !ok || !e.Scoped() {
		t.Fatalf("the entry must stay and stay scoped: %+v", e.CapturedScope)
	}
	for _, host := range []string{"", "jobs.example.test"} {
		if e.Admits(host, "Q", nil) {
			t.Fatalf("an entry with an unreadable scope answered on %q", host)
		}
	}
	if !hasWarning(r, "captured_scope", "answers on no page") {
		t.Fatalf("warnings = %v", r.Warnings)
	}
}

func TestScope_ValidateRefusesAnInvalidScope(t *testing.T) {
	for name, scope := range map[string][]match.ScopeItem{
		"blank host":     {{Host: " ", Question: "Q"}},
		"blank question": {{Host: "jobs.example.test"}},
		"one good one bad": {{Host: "jobs.example.test", Question: "Q"},
			{Question: "Q"}},
	} {
		err := validateOne(fixedEntry("a", func(e *match.Entry) { e.CapturedScope = scope }))
		var ve *ValidationError
		if !errors.As(err, &ve) || !strings.Contains(err.Error(), "captured_scope") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	good := fixedEntry("a", func(e *match.Entry) {
		e.CapturedScope = []match.ScopeItem{{Host: "jobs.example.test", Question: "Q", Options: []string{"A"}}}
	})
	if err := validateOne(good); err != nil {
		t.Fatalf("a valid scope: %v", err)
	}
}

// Finding H2: callers can never mutate the cached bank.

func TestFile_ReturnsCopiesOfTheCachedBank(t *testing.T) {
	s, ws := newService(t)
	writeFile(t, ws, bankFile, "version: 1\nemployers:\n  - {name: Example Corp, aliases: [EC]}\nentries:\n"+
		fixedYAML("a", "one", "examples: [q1]", "captured_scope: [{host: jobs.example.test, question: Q, options: [A]}]", "confidence: 0.5")+
		"overrides:\n  example.test:\n    - {id: a, answer: two}\n")
	first, err := s.File()
	if err != nil {
		t.Fatal(err)
	}
	want := first.clone()
	first.Bank.Entries[0].Answer = "changed"
	first.Bank.Entries[0].Examples[0] = "changed"
	first.Bank.Entries[0].CapturedScope[0].Options[0] = "changed"
	*first.Bank.Entries[0].Confidence = 0.99
	first.Bank.Employers[0].Aliases[0] = "changed"
	first.Bank.Overrides["example.test"][0].Body.Answer = "changed"
	first.Bank.Overrides["example.test"][0].Fields[0] = "changed"
	first.Bank.Overrides["other.test"] = nil
	first.Warnings = append(first.Warnings, "changed")
	if len(first.Warnings) > 0 {
		first.Warnings[0] = "changed"
	}
	second, err := s.File()
	if err != nil || !reflect.DeepEqual(second.Bank, want.Bank) || !reflect.DeepEqual(second.Warnings, want.Warnings) {
		t.Fatalf("the cached bank was mutated through a returned copy: %v\n%+v", err, second.Bank)
	}
	stamped, _ := s.Bank()
	stamped.Bank.Entries[0].Answer = "changed"
	if third, _ := s.File(); third.Bank.Entries[0].Answer != "one" {
		t.Fatal("the stamped bank shares memory with the cache")
	}
}

func TestFile_LastGoodBankIsACopyToo(t *testing.T) {
	s, ws := newService(t)
	writeFile(t, ws, bankFile, bankText(fixedYAML("a", "one")))
	if _, err := s.File(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, ws, bankFile, "version: 9\n")
	l, err := s.File()
	if err == nil {
		t.Fatal("a broken file must fail")
	}
	l.Bank.Entries[0].Answer = "changed"
	if again, _ := s.File(); again.Bank.Entries[0].Answer != "one" {
		t.Fatal("the last good bank is shared with the caller")
	}
}

// Finding M1: the save refuses what the load would repair; the load warns
// about keys that make no sense for the kind.

func TestValidate_RefusesWhatTheLoadRepairs(t *testing.T) {
	cases := map[string]struct {
		yaml   string
		mutate func(*match.Entry)
		warn   string
	}{
		"unknown skill_mode": {"skill_mode: sometimes", func(e *match.Entry) { e.SkillMode = "sometimes" }, "skill_mode"},
		"unknown extractor": {"numeric_threshold: {extractor: nope}",
			func(e *match.Entry) { e.NumericThreshold = &match.NumericThreshold{Extractor: "nope"} }, "extractor"},
		"unknown predicate": {"location_policy: {predicate: nope}",
			func(e *match.Entry) { e.LocationPolicy = &match.LocationPolicy{Predicate: "nope"} }, "predicate"},
		"invalid negation pattern": {"question_negation_terms: ['(unclosed']",
			func(e *match.Entry) { e.QuestionNegationTerms = []string{"(unclosed"} }, "question_negation_terms"},
		"repeated allowed options": {"allowed_options: [Red, red]",
			func(e *match.Entry) { e.AllowedOptions = []string{"Red", "red"} }, "allowed_options"},
		"comma-joined skill terms": {"skill_terms: ['go, rust']",
			func(e *match.Entry) { e.SkillTerms = []string{"go, rust"} }, "skill_terms"},
		"padded linked_explain_id": {"linked_explain_id: ' other '",
			func(e *match.Entry) { e.LinkedExplainID = " other " }, "linked_explain_id"},
		"unknown state":        {"state: dormant", func(e *match.Entry) { e.State = "dormant" }, "state"},
		"unknown created_by":   {"created_by: robot", func(e *match.Entry) { e.CreatedBy = "robot" }, "created_by"},
		"unknown input type":   {"input_types: [hologram]", func(e *match.Entry) { e.InputTypes = []string{"hologram"} }, "input type"},
		"confidence above one": {"confidence: 1.5", func(e *match.Entry) { e.Confidence = floatPtr(1.5) }, "confidence"},
		"ask-me inside a list": {"", func(e *match.Entry) {
			e.Kind, e.Body = match.KindPickOption, match.Body{Preferred: []string{match.AskMe, "Yes"}}
		}, match.AskMe},
		"field of another kind": {"preferred: [A]", func(e *match.Entry) { e.Preferred = []string{"A"} }, "preferred"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := validateOne(fixedEntry("a", tc.mutate))
			var ve *ValidationError
			if !errors.As(err, &ve) || !strings.Contains(err.Error(), tc.warn) {
				t.Fatalf("the save must be refused for %s, err = %v", tc.warn, err)
			}
			if tc.yaml == "" {
				return
			}
			r := mustParse(t, bankText(fixedYAML("a", "x", "category: other", tc.yaml)))
			if !hasWarning(r, `entry "a"`, tc.warn) {
				t.Fatalf("the load stays silent: %v", r.Warnings)
			}
		})
	}
}

func TestLoad_WarnsAboutKeysThatDoNotBelongToTheKind(t *testing.T) {
	r := mustParse(t, "version: 1\nentries:\n"+
		"  - id: a\n    description: d\n    kind: fixed\n    answer: x\n    preferred: [A]\n    minimum: '5'\n"+
		"  - id: b\n    description: d\n    kind: auto-acknowledge\n    answer: leftover\n")
	if !hasWarning(r, `entry "a"`, "preferred", "kind fixed") || !hasWarning(r, `entry "a"`, "minimum") || !hasWarning(r, `entry "b"`, "answer", "auto-acknowledge") {
		t.Fatalf("warnings = %v", r.Warnings)
	}
	a, _ := r.Bank.Find("a")
	if a.Preferred != nil || a.Minimum != "" || a.Answer != "x" {
		t.Fatalf("the unused fields must not survive: %+v", a.Body)
	}
}

func TestValidate_AcceptsACleanBank(t *testing.T) {
	def, err := DefaultBank()
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(def.Bank); err != nil {
		t.Fatalf("the default bank must pass the save rules: %v", err)
	}
	def.Bank.Entries[0].Description = "changed"
	if again, _ := DefaultBank(); again.Bank.Entries[0].Description == "changed" {
		t.Fatal("DefaultBank shares memory with its cache")
	}
}

// Finding M2: overrides are validated like entries.

func overrideBank(host string, os ...match.Override) match.Bank {
	return match.Bank{Version: 1, Entries: []match.Entry{
		fixedEntry("a", nil),
		{ID: "p", Description: "d", Kind: match.KindPickOption, Body: match.Body{Preferred: []string{"A"}}, Meta: match.Meta{Category: match.CategoryOther}},
	}, Overrides: map[string][]match.Override{host: os}}
}

func TestValidate_OverridesFollowTheEntryRules(t *testing.T) {
	cases := map[string]struct {
		bank match.Bank
		warn string
	}{
		"unknown entry": {overrideBank("example.test", match.Override{ID: "ghost", Fields: []string{"answer"}, Body: match.Body{Answer: "y"}}), "ghost"},
		"unknown kind":  {overrideBank("example.test", match.Override{ID: "a", Kind: "hologram", Fields: []string{"answer"}, Body: match.Body{Answer: "y"}}), "unknown kind"},
		"kind change without its required field": {overrideBank("example.test",
			match.Override{ID: "a", Kind: match.KindPickOption, Fields: []string{"preferred"}, Body: match.Body{Preferred: []string{"A"}}}), "fallback_text"},
		"field the kind does not use": {overrideBank("example.test", match.Override{ID: "a", Fields: []string{"preferred"}, Body: match.Body{Preferred: []string{"A"}}}), "preferred"},
		"blank host":                  {overrideBank(" ", match.Override{ID: "a", Fields: []string{"answer"}, Body: match.Body{Answer: "y"}}), "host"},
		"override empties the answer": {overrideBank("example.test", match.Override{ID: "a", Fields: []string{"answer"}, Body: match.Body{Answer: ""}}), "empty"},
	}
	for name, tc := range cases {
		var ve *ValidationError
		if err := Validate(tc.bank); !errors.As(err, &ve) || !strings.Contains(err.Error(), tc.warn) {
			t.Errorf("%s: the save must be refused for %s, err = %v", name, tc.warn, err)
		}
	}
	good := overrideBank("Example.Test",
		match.Override{ID: "a", Fields: []string{"answer"}, Body: match.Body{Answer: "y"}},
		match.Override{ID: "a", Kind: match.KindPickOption, Fields: []string{"preferred", "fallback_text"}, Body: match.Body{Preferred: []string{"B"}}})
	good.Overrides = map[string][]match.Override{"example.test": good.Overrides["Example.Test"]}
	if err := Validate(good); err != nil {
		t.Fatalf("a valid override chain: %v", err)
	}
}

func TestLoad_OverridesAreCheckedLikeEntries(t *testing.T) {
	for name, tc := range map[string]struct{ override, warn string }{
		"field the kind does not use": {"{id: x, preferred: [A]}", "not used by kind fixed"},
		"unknown kind":                {"{id: x, kind: hologram, answer: y}", "unknown kind"},
		"key that is not an answer":   {"{id: x, answer: y, colour: red}", "not an answer field"},
	} {
		r := mustParse(t, overrideFile("  example.test:\n    - "+tc.override+"\n"))
		if !hasWarning(r, `override of "x"`, tc.warn) {
			t.Errorf("%s: warnings = %v", name, r.Warnings)
		}
		if got := r.Bank.EntriesForHost("example.test")[0]; got.Answer != "base" && name != "key that is not an answer" {
			t.Errorf("%s: the base entry changed: %+v", name, got)
		}
	}
}

func TestLoad_AnOverrideAfterAKindChangeSeesTheNewKind(t *testing.T) {
	r := mustParse(t, overrideFile("  example.test:\n    - {id: x, kind: pick-option, preferred: [A], fallback_text: A}\n    - {id: x, preferred: [B]}\n"))
	if len(r.Warnings) != 0 {
		t.Fatalf("warnings = %v", r.Warnings)
	}
	if got := r.Bank.EntriesForHost("example.test")[0]; got.Kind != match.KindPickOption || got.Preferred[0] != "B" {
		t.Fatalf("%+v", got)
	}
}

// Finding M3.

func TestConfidence_NaNIsRefused(t *testing.T) {
	r := mustParse(t, bankText(fixedYAML("a", "x", "confidence: .nan", "confidence_floor: .nan")))
	e, _ := r.Bank.Find("a")
	if e.Confidence != nil || e.ConfidenceFloor != nil || !hasWarning(r, "confidence") || !hasWarning(r, "confidence_floor") {
		t.Fatalf("%+v %v", e.Meta, r.Warnings)
	}
	for name, mutate := range map[string]func(*match.Entry){
		"confidence":       func(e *match.Entry) { e.Confidence = floatPtr(math.NaN()) },
		"confidence_floor": func(e *match.Entry) { e.ConfidenceFloor = floatPtr(math.NaN()) },
	} {
		if err := validateOne(fixedEntry("a", mutate)); err == nil {
			t.Errorf("%s: NaN passed the save", name)
		}
	}
	if err := validateOne(fixedEntry("a", func(e *match.Entry) { e.ConfidenceFloor = floatPtr(0.4) })); err != nil {
		t.Fatalf("a floor below 0.70 is clamped at use, not refused: %v", err)
	}
}

// Finding M4, L5: one plain document.

func TestLoad_RefusesAliasesMergeKeysAndExtraDocuments(t *testing.T) {
	cases := map[string]struct{ text, want string }{
		"alias":              {"version: 1\nentries:\n" + fixedYAML("a", "&v one") + fixedYAML("b", "*v"), "alias"},
		"alias as a key":     {"version: 1\nentries:\n  - id: a\n    description: &k answer\n    kind: fixed\n    *k : y\n", "alias"},
		"merge key":          {"version: 1\nentries:\n  - id: a\n    description: d\n    kind: fixed\n    <<: {answer: y}\n", "merge"},
		"two documents":      {"version: 1\nentries: []\n---\nversion: 1\nentries: []\n", "more than one"},
		"trailing separator": {"version: 1\nentries: []\n---\n", "more than one"},
	}
	for name, tc := range cases {
		_, err := Parse("answer-bank.yml", []byte(tc.text))
		var le *LoadError
		if !errors.As(err, &le) || !strings.Contains(le.Message, tc.want) || le.Line == 0 {
			t.Errorf("%s: err = %#v", name, err)
		}
	}
}

func TestLoad_AliasBombIsRefusedBeforeItExpands(t *testing.T) {
	var b strings.Builder
	b.WriteString("version: 1\nx: &a [l, l, l, l, l, l, l, l, l]\n")
	prev := "a"
	for _, name := range []string{"b", "c", "d", "e", "f", "g", "h", "i"} {
		b.WriteString("x" + name + ": &" + name + " [*" + prev + ", *" + prev + ", *" + prev + ", *" + prev + ", *" + prev + ", *" + prev + ", *" + prev + ", *" + prev + ", *" + prev + "]\n")
		prev = name
	}
	if _, err := Parse("answer-bank.yml", []byte(b.String())); err == nil || !strings.Contains(err.Error(), "alias") {
		t.Fatalf("err = %v", err)
	}
}

// Finding M5.

func TestLoad_SizeCapEqualsTheSaveCap(t *testing.T) {
	over := []byte("version: 1\n# " + strings.Repeat("x", MaxBankBytes))
	_, err := Parse("answer-bank.yml", over)
	var le *LoadError
	if !errors.As(err, &le) || !strings.Contains(le.Message, "larger than") {
		t.Fatalf("err = %v", err)
	}
	atCap := append([]byte("version: 1\nentries: []\n# "), []byte(strings.Repeat("x", MaxBankBytes-len("version: 1\nentries: []\n# ")))...)
	if len(atCap) != MaxBankBytes {
		t.Fatalf("test data is %d bytes", len(atCap))
	}
	if _, err := Parse("answer-bank.yml", atCap); err != nil {
		t.Fatalf("a file of exactly MaxBankBytes loads: %v", err)
	}
}

// Findings L1, L2.

func TestAskMe_TrimmedMarkerCountsAndAMarkerInsideAListWarns(t *testing.T) {
	r := mustParse(t, "version: 1\nentries:\n"+
		"  - {id: a, description: d, kind: fixed, answer: '  @ask-me  '}\n"+
		"  - {id: b, description: d, kind: pick-option, preferred: ['@ask-me', 'Yes'], fallback_text: ''}\n")
	rep := match.NeedsAnswer(r.Bank)
	if len(rep.AskMe) != 1 || rep.AskMe[0] != "a" {
		t.Fatalf("report = %+v", rep)
	}
	if !hasWarning(r, `entry "b"`, "@ask-me", "list of several values") {
		t.Fatalf("warnings = %v", r.Warnings)
	}
}

// Finding L4.

func TestOverrides_HostCaseSurvivesASaveAndMatchingStaysCaseInsensitive(t *testing.T) {
	s, ws := newService(t)
	writeFile(t, ws, bankFile, overrideFile("  Jobs.Example.Test:\n    - {id: x, answer: site}\n"))
	l, err := s.File()
	if err != nil {
		t.Fatal(err)
	}
	if got := l.Bank.EntriesForHost("JOBS.example.test")[0].Answer; got != "site" {
		t.Fatalf("answer = %q", got)
	}
	l.Bank.Entries[0].Description = "edited"
	if _, err := s.Save(l.Bank, l.Revision); err != nil {
		t.Fatal(err)
	}
	if text := readFile(t, ws, bankFile); !strings.Contains(text, "Jobs.Example.Test:") || strings.Contains(text, "jobs.example.test:") {
		t.Fatalf("the host was rewritten:\n%s", text)
	}
	again, _ := s.File()
	if again.Bank.HostName("jobs.example.test") != "Jobs.Example.Test" {
		t.Fatalf("spelling = %v", again.Bank.HostSpelling)
	}
}

func TestOverrides_HostsThatDifferOnlyInCaseAreMergedWithAWarning(t *testing.T) {
	r := mustParse(t, overrideFile("  Example.test:\n    - {id: x, answer: one}\n  example.TEST:\n    - {id: x, answer: two}\n"))
	if !hasWarning(r, "differ only in case") || len(r.Bank.Overrides["example.test"]) != 2 {
		t.Fatalf("%v %v", r.Warnings, r.Bank.Overrides)
	}
}

// Finding L6: no message repeats what the file holds.

func TestErrorsAndWarningsNeverRepeatFileContent(t *testing.T) {
	const secret = "Zelda Quux Private"
	cases := map[string]string{
		"wrong type":        "version: 1\nentries:\n" + fixedYAML("a", "x", "confidence: "+secret),
		"wrong type list":   "version: 1\nentries:\n" + fixedYAML("a", "x", "examples: {k: "+secret+"}"),
		"bad yaml":          "version: 1\nentries: [" + secret + "\n",
		"unknown kind":      "version: 1\nentries:\n  - {id: a, description: d, kind: '" + secret + "', answer: x}\n",
		"unknown category":  "version: 1\nentries:\n" + fixedYAML("a", "x", "category: '"+secret+"'"),
		"unknown state":     "version: 1\nentries:\n" + fixedYAML("a", "x", "category: other", "state: '"+secret+"'"),
		"unknown input":     "version: 1\nentries:\n" + fixedYAML("a", "x", "category: other", "input_types: ['"+secret+"']"),
		"unknown top key":   "version: 1\n'" + secret + "': 1\n",
		"version":           "version: '" + secret + "'\n",
		"unknown field":     "version: 1\nentries:\n" + fixedYAML("a", "x", "category: other", "'"+secret+"': 1"),
		"bad pattern":       "version: 1\nentries:\n" + fixedYAML("a", "x", "category: other", "question_negation_terms: ['("+secret+"']"),
		"unknown predicate": "version: 1\nentries:\n" + fixedYAML("a", "x", "category: other", "location_policy: {predicate: '"+secret+"'}"),
		"override field":    overrideFile("  example.test:\n    - {id: x, '" + secret + "': 1}\n"),
		"override kind":     overrideFile("  example.test:\n    - {id: x, kind: '" + secret + "'}\n"),
		"duplicate key":     "version: 1\nentries:\n" + fixedYAML("a", "x", "'"+secret+"': 1", "'"+secret+"': 2"),
	}
	for name, text := range cases {
		r, err := Parse("answer-bank.yml", []byte(text))
		all := strings.Join(r.Warnings, "\n")
		if err != nil {
			all += "\n" + err.Error()
		}
		if strings.Contains(all, "Quux") || strings.Contains(all, "Zelda") {
			t.Errorf("%s repeats the file content: %s", name, all)
		}
		if err == nil && len(r.Warnings) == 0 {
			t.Errorf("%s: expected a problem to be reported", name)
		}
	}
	err := validateOne(fixedEntry("a", func(e *match.Entry) { e.Kind = match.Kind(secret) }))
	if err == nil || strings.Contains(err.Error(), "Quux") {
		t.Fatalf("validation repeats the value: %v", err)
	}
	err = validateOne(fixedEntry("a", func(e *match.Entry) { e.Category, e.State = secret, secret }))
	if err == nil || strings.Contains(err.Error(), "Quux") {
		t.Fatalf("validation repeats the value: %v", err)
	}
}
