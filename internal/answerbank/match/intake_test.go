package match

import (
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func labels(opts []Option) []string {
	out := make([]string, len(opts))
	for i, o := range opts {
		out[i] = o.Label
	}
	return out
}

func TestIntake_DedupeOptionLabels(t *testing.T) {
	in := []Option{{"a", "No Selection"}, {"b", "No Selection"}, {"c", "No"}, {"d", "No"}, {"e", "Yes"}, {"f", "Yes"}}
	got := dedupeOptionLabels(in)
	if !reflect.DeepEqual(labels(got), []string{"No Selection", "No", "Yes"}) {
		t.Fatalf("labels = %v", labels(got))
	}
	if got[0].Value != "a" || got[1].Value != "c" || got[2].Value != "e" {
		t.Fatalf("values changed: %v", got)
	}
	distinct := opts("Yes", "No")
	if !reflect.DeepEqual(dedupeOptionLabels(distinct), distinct) {
		t.Fatal("distinct labels must stay")
	}
}

func TestIntake_NumberedChoices(t *testing.T) {
	mk := func(text string) Question { return Question{Selector: "q", Text: text, Kind: KindText} }
	cases := []struct {
		name     string
		q        Question
		ok       bool
		wantOpts []string
		wantText string
	}{
		{"enumerated status", mk("What is your status (1. Citizen; 2. Permanent resident; 3. Other) Enter the number?"), true,
			[]string{"Citizen", "Permanent resident", "Other"}, "What is your status Enter the number?"},
		{"glued labels and trailing semicolon", mk("What is your work authorization (1.US Citizen; 2. Green Card ; 3. GC EAD; 4.Other; ) Please write numeric?"), true,
			[]string{"US Citizen", "Green Card", "GC EAD", "Other"}, "What is your work authorization Please write numeric?"},
		{"quantity ask is never a list", mk("How many years (1. Python; 2. Go)?"), false, nil, ""},
		{"part list is ignored", mk("Please indicate your (1) Citizenship and (2) Immigration Status"), false, nil, ""},
		{"decimal is no marker", mk("Rate 3.5 out of 5 stars"), false, nil, ""},
		{"a select with options is untouched", Question{Selector: "q", Text: "Pick (1. A; 2. B)", Kind: KindSelect, Options: opts("A", "B")}, false, nil, ""},
		{"label over sixty characters aborts", mk("Choose (1. " + strings.Repeat("x", 70) + "; 2. B)"), false, nil, ""},
		{"single choice is no list", mk("Choose (1. Only one)"), false, nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := rewriteNumberedChoices(c.q)
			if ok != c.ok {
				t.Fatalf("ok = %v want %v", ok, c.ok)
			}
			if !ok {
				return
			}
			if !reflect.DeepEqual(labels(got.Options), c.wantOpts) {
				t.Fatalf("options = %v want %v", labels(got.Options), c.wantOpts)
			}
			for i, o := range got.Options {
				if o.Value != strconv.Itoa(i+1) {
					t.Fatalf("option %d value %q", i, o.Value)
				}
			}
			if got.Text != c.wantText || got.Kind != KindSelect || got.InputType != InputSingleSelect {
				t.Fatalf("text %q kind %s type %s", got.Text, got.Kind, got.InputType)
			}
		})
	}
}

func TestIntake_RungLabels(t *testing.T) {
	rung := opts(
		"Professional/Fluent: Able to lead technical discussions",
		"Intermediate: Able to follow technical requirements but struggle with fluent writing",
		"Beginner/None: Limited ability to communicate in writing",
	)
	got, ok := scoreOptionsOnRung(rung)
	if !ok || !reflect.DeepEqual(labels(got), []string{"Professional/Fluent", "Intermediate", "Beginner/None"}) {
		t.Fatalf("rung: %v %v", ok, labels(got))
	}
	p := prepareQuestion(Question{Selector: "s", Text: "English proficiency", Kind: KindSelect, Options: rung})
	if p.restoreLabel("Professional/Fluent") != rung[0].Label {
		t.Fatalf("restore = %q", p.restoreLabel("Professional/Fluent"))
	}
	dated := opts("Q1: January 2027 - March 2027 for all of us", "Q2: April 2027 - June 2027 for all of us")
	if _, ok := scoreOptionsOnRung(dated); ok {
		t.Fatal("a dated description keeps its label")
	}
	dupe := opts("Basic: Able to read simple text well", "Basic: Able to speak simple words well")
	if _, ok := scoreOptionsOnRung(dupe); ok {
		t.Fatal("rungs must be distinct")
	}
	mixed := opts("Fluent: Able to lead technical discussions", "Plain label")
	if _, ok := scoreOptionsOnRung(mixed); ok {
		t.Fatal("every real option needs the rung shape")
	}
}

// The stemmer's length cutoffs and the whole stop-word list are pinned: every
// fuzzy score, coverage lane and subject guard reads them, so a change moves
// every threshold.
func TestIntake_StemmerBoundariesAndStopList(t *testing.T) {
	for in, want := range map[string]string{
		"cities": "city", "dies": "die", "doing": "do", "used": "used", "walked": "walk", "boxes": "box",
		"bus": "bus", "gas": "gas", "less": "less", "cats": "cat", "ties": "tie",
	} {
		if got := stem(in); got != want {
			t.Errorf("stem(%q) = %q want %q", in, got, want)
		}
	}
	if len(stopWords) != 70 {
		t.Errorf("%d stop words; a change to the list moves every fuzzy threshold", len(stopWords))
	}
	// The exact list, sorted: it equals the old matcher's stop list word for word.
	const want = "a all an and any applicant are as at be been being but by candidate could did do does ever for from had has have herself himself how if in is it its itself kindly may might myself of on oneself or ourselves please should some that the themselves these this those to was were what when which who whom whose why will with would you your yours yourself yourselves"
	got := make([]string, 0, len(stopWords))
	for w := range stopWords {
		got = append(got, w)
	}
	slices.Sort(got)
	if strings.Join(got, " ") != want {
		t.Errorf("stop list drifted:\n%s", strings.Join(got, " "))
	}
	// Length cutoffs count characters, not bytes: an accented three-letter
	// plural keeps its s, a one-letter accented token is dropped like any other.
	for in, wantStem := range map[string]string{
		"ñus": "ñus", "ñuss": "ñuss", "niños": "niño", "ñeed": "ñeed", "ñing": "ñing", "tiés": "tié",
		"ies": "ies", "pies": "pie", "lies": "lie", "babies": "baby", "cries": "cri",
	} {
		if got := stem(in); got != wantStem {
			t.Errorf("stem(%q) = %q want %q", in, got, wantStem)
		}
	}
	if got := stemmedTokens("é a ñ ab walking Cities boxes"); !slices.Equal(got, []string{"ab", "walk", "city", "box"}) {
		t.Errorf("tokens = %v", got)
	}
	// Numbers of every kind are token characters (\p{N}), and the label is read
	// normalized: entities decoded, compatibility forms folded.
	if got := stemmedTokens("Tier a\u0bf0b tier &amp; \u2468\u2468 ones"); !slices.Equal(got, []string{"tier", "a\u0bf0b", "99", "one"}) {
		t.Errorf("tokens = %v", got)
	}
}

func TestIntake_Contentless(t *testing.T) {
	prompts := []string{"Choose...", "MM/DD/YYYY", "YYYY", "Select One Required", "*", "Select an option", "Please select", "-- Select --", "Pick date...", "e.g. 555-0100", "Type here", "Enter text", "None selected", "mm/yyyy", "Select One (required)"}
	for _, p := range prompts {
		if !IsContentless(p) {
			t.Errorf("%q should be contentless", p)
		}
	}
	questions := []string{"Select your gender", "Choose your office", "Location", "Required certifications", "Date of birth", "Are you over 18?"}
	for _, q := range questions {
		if IsContentless(q) {
			t.Errorf("%q should keep its question", q)
		}
	}
	for _, v := range []string{"Select a country", "-- Select --", "Please choose", "..."} {
		if !IsPlaceholderValue(v) {
			t.Errorf("%q should be a placeholder value", v)
		}
	}
	for _, v := range []string{"", "Canada", "Yes"} {
		if IsPlaceholderValue(v) {
			t.Errorf("%q should not be a placeholder value", v)
		}
	}
}

func TestIntake_UnicodePromptBoundaries(t *testing.T) {
	for _, text := range []string{"\ufeffSelect\ufeffone\ufeff", "Select\u202fone", "MM\u00a0/\u00a0YYYY", "-- ... Select ... --"} {
		if !IsContentless(text) {
			t.Errorf("prompt-only question retained: %q", text)
		}
	}
	for _, value := range []string{"\ufeffChoose\ufeff", "Choose\u202fan\u202foption", "Select\u00a0a\u00a0country", "--- Select - ."} {
		if !IsPlaceholderValue(value) {
			t.Errorf("prompt option retained: %q", value)
		}
	}
	for _, text := range []string{"Select your future team", "Required certifications", "Make a selection for the committee"} {
		if IsContentless(text) {
			t.Errorf("real question removed: %q", text)
		}
	}
	for _, value := range []string{"Selection committee", "Selected your team", "PİCK ONE", "None", "No", "*", "2027-04-18"} {
		if IsPlaceholderValue(value) {
			t.Errorf("real option removed: %q", value)
		}
	}
}

// mayBePrompt only skips work: every value placeholderValue accepts passes it.
// The corpus joins prompt words and their near misses with the punctuation and
// the Unicode spaces and zero-width characters the fold removes or keeps.
func TestIntake_MayBePromptHoldsForEveryPlaceholder(t *testing.T) {
	leads := []string{"", " ", "\u00a0", "\ufeff", "\u200b", "\t", "-", "-- ", "\u2014", "\u2026", "_", "*", ". ", "("}
	words := []string{"Select", "select", "SELECT", "Se\u200blect", "\u017felect", "Choose", "PICK", "Please", "please",
		"Make", "a", "an", "one", "your", "the", "selection", "None", "no", "Not", "nothing", "selected", "option", "country", "..."}
	seps := []string{" ", "\u202f", "\ufeff", "\u00a0 "}
	tails := []string{"", "...", "*", " *", " --", "\u2026", "_"}
	var texts []string
	for i, a := range words {
		texts = append(texts, a)
		for j, b := range words {
			texts = append(texts, a+seps[(i+j)%len(seps)]+b)
		}
	}
	texts = append(texts, "Make a selection", "please select one", "Please choose an option", "Select your country of residence",
		"nothing selected", "Choose\u202fan\u202foption", "- - -", "._.", "\u2026\u2026")
	checked, prompts := 0, 0
	for _, lead := range leads {
		for _, text := range texts {
			for _, tail := range tails {
				v := lead + text + tail
				checked++
				if !placeholderValue(v) {
					continue
				}
				prompts++
				if !mayBePrompt(v) {
					t.Errorf("mayBePrompt(%q) = false for a placeholder value", v)
				}
			}
		}
	}
	if prompts < 1000 {
		t.Fatalf("the corpus holds %d placeholders in %d values; it no longer exercises the condition", prompts, checked)
	}
}

func TestIntake_LocatorAndNarrativeShapes(t *testing.T) {
	narrative := []string{
		"Tell us about yourself", "Describe your role", "Explain the gap", "Why do you want this job?", "How would you handle it?",
		"What motivates you?", "Share a time you led a team", "Provide two examples of achievements", "Attach a cover letter",
		"Additional comments", "In your own words", "Briefly describe your goals", "Walk us through your design",
	}
	for _, n := range narrative {
		if !isNarrativeText(n) {
			t.Errorf("%q should be narrative", n)
		}
	}
	for _, n := range []string{"Preferred name", "How many years of Go?", "Phone number", "Do you have a license?",
		// Avride: "(please describe)" belongs to the "other" choice of the example list.
		"How did you hear about this position? For example: Indeed, LinkedIn, recruiter outreach or other (please describe fully)."} {
		if isNarrativeText(n) {
			t.Errorf("%q should not be narrative", n)
		}
	}
}

func TestIntake_PrepareTrace(t *testing.T) {
	raw := Question{Selector: "s", Text: "  Yes Are you currently subject to any agreement that limits your work?", Kind: KindRadioGroup, Options: opts("Yes", "No")}
	p := prepareQuestion(raw)
	if p.Q.Text != "Are you currently subject to any agreement that limits your work?" {
		t.Fatalf("text = %q", p.Q.Text)
	}
	if raw.Text != "  Yes Are you currently subject to any agreement that limits your work?" {
		t.Fatal("the caller's question was mutated")
	}
	if len(p.trace) != 1 || !strings.Contains(p.trace[0], "sanitized questionText") || !strings.Contains(p.trace[0], "Yes Are you") {
		t.Fatalf("trace = %v", p.trace)
	}
	clean := prepareQuestion(Question{Selector: "s", Text: "How many years of professional work experience do you have?", Kind: KindText})
	if len(clean.trace) != 0 || clean.Q.InputType != InputNumber {
		t.Fatalf("clean question: trace %v type %s", clean.trace, clean.Q.InputType)
	}
}

// A text box whose label names only a link is a URL box (Lever's link fields
// are plain text inputs); a question that names a link is not.
func TestIntake_ALinkOnlyLabelIsAURLBox(t *testing.T) {
	for _, s := range []string{"Other URL", "GitHub URL", "Personal Website URL", "LinkedIn Profile", "Portfolio", "Website*", "Google Scholar URL", "Other URL (optional)", "Link"} {
		if !isLinkBox(s) {
			t.Errorf("%q is a link box", s)
		}
	}
	for _, s := range []string{"Are you on LinkedIn", "How did you hear about us? LinkedIn", "Please share a link to your best project and what you built",
		"Describe your portfolio", "What is your website"} {
		if isLinkBox(s) {
			t.Errorf("%q is no link box", s)
		}
	}
	p := prepareQuestion(Question{Text: "Other URL", Kind: KindText, InputType: InputText})
	if p.Q.InputType != InputURL {
		t.Fatalf("input type %q", p.Q.InputType)
	}
}
