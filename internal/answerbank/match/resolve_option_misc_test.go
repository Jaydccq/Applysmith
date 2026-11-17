package match

import (
	"strings"
	"testing"
)

func TestClassify_PickOptionCannotBindPlaceholderLiteralNone(t *testing.T) {
	none := ent("none-pick", "pick-option", withPreferred("None"))
	if !none.bindsAnyOption(opts("Option A", "None"), nil) {
		t.Fatal("a preferred None binds a literal None")
	}
	if none.bindsAnyOption(opts("Option A", "No Selection"), nil) {
		t.Fatal("No Selection stays out")
	}
	fb := none
	fb.FallbackAnyOption = true
	if !fb.bindsAnyOption(opts("Option A"), nil) {
		t.Fatal("the any-option net always binds")
	}
	if !none.bindsAnyOption(nil, nil) {
		t.Fatal("a control with no real option has nothing to bind")
	}
	blank := ent("blank", "pick-option", emptyBody())
	if !blank.bindsAnyOption(opts("Option A", "Option B"), nil) {
		t.Fatal("an entry without search terms binds")
	}
	fixed := ent("fixed-one", "fixed")
	if !fixed.bindsAnyOption(opts("Option A"), nil) {
		t.Fatal("only pick-option entries are filtered")
	}
	// The search expansion counts: a state abbreviation binds a full-name list.
	st := ent("address-state", "pick-option", withPreferred("SC"))
	if !st.bindsAnyOption(opts("Georgia", "South Carolina"), nil) {
		t.Fatal("expanded values bind")
	}
}

func TestOptions_UnicodePromptsNeverBecomeFallbackAnswers(t *testing.T) {
	const question = "How did you hear about this team?"
	e := ent("referral-source", "pick-option", withPreferred("Employee referral"), withExamples(question))
	e.FallbackAnyOption = true
	for _, prompt := range []string{"\ufeffChoose\ufeff", "Choose\u202fan\u202foption", "Select\u00a0a\u00a0country", "--- Select - ."} {
		t.Run(prompt, func(t *testing.T) {
			r := matchReq(pickMatcher(e), selectQ(question, prompt, "Community event"))
			if r.ChosenLabel != "Community event" {
				t.Fatalf("fallback selected prompt instead of answer: %+v", r)
			}
			r = matchReq(pickMatcher(e), selectQ(question, prompt))
			if r.Outcome == OutcomeFilled || r.Outcome == OutcomeFilledReview {
				t.Fatalf("prompt-only control filled: %+v", r)
			}
		})
	}
}

func TestDefaults_SelfIDDeclineFilledReview(t *testing.T) {
	// The shape of a shipped self-identification entry: decline wordings preferred, needs review.
	e := ent("eeo-gender", "pick-option", withPreferred("Decline to self-identify", "I prefer not to answer", "Prefer not to say"), withExamples("Gender"), notCurated())
	r := matchReq(pickMatcher(e), selectQ("Gender", "Man", "Woman", "Non-Binary", "Prefer Not to Say"))
	wantFill(t, r, "Prefer Not to Say", OutcomeFilledReview)
	if r.Confidence > FuzzyConfidenceCap {
		t.Fatalf("confidence = %v", r.Confidence)
	}
	// With no decline option on the form the result is the terminal abstain.
	r = matchReq(pickMatcher(e), selectQ("Gender", "Man", "Woman", "Veteran"))
	wantAbstain(t, r, ReasonSelfIDPolicy, "")
}

// OptionsNamed finds a label only where it stands whole, in the order the text
// names it, whatever its case and spacing.
func TestOptionsNamed_WholeLabelsInTheTextsOrder(t *testing.T) {
	labels := []string{"Python", "Java", "Go", "C", "C++", "Yes, I agree", "Node  JS"}
	for _, c := range []struct{ text, want string }{
		{"Go and C++ daily, some python", "Go|C++|Python"},
		{"Google Cloud, JavaScript and C#", ""},
		{"yes,  i agree", "Yes, I agree"},
		{"node js; C", "Node  JS|C"},
		{"", ""},
	} {
		if got := strings.Join(OptionsNamed(c.text, labels), "|"); got != c.want {
			t.Errorf("%q: %q, want %q", c.text, got, c.want)
		}
	}
}
