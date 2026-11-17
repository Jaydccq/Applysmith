package match

import (
	"fmt"
	"strings"
	"testing"
)

// scoreCase is one fixture of the option score ladder: the answer, one option
// label, the question text the option is offered under, and the expected score.
type scoreCase struct {
	row      string
	answer   string
	label    string
	question string
	want     float64
	why      string // substring of the deciding branch; empty: not asserted
}

func runScoreCases(t *testing.T, cases []scoreCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(fmt.Sprintf("%s/%s=>%s", c.row, c.answer, c.label), func(t *testing.T) {
			got := scoreOption(Option{Value: c.label, Label: c.label}, c.answer, c.question)
			if got.Score < c.want-0.0001 || got.Score > c.want+0.0001 {
				t.Fatalf("score = %.4f (%s), want %.2f", got.Score, got.Why, c.want)
			}
			if c.why != "" && !strings.Contains(got.Why, c.why) {
				t.Fatalf("why = %q, want it to contain %q", got.Why, c.why)
			}
		})
	}
}

// Stripe offers its countries as "US", "UK" and "UAE"; a bare Yes or No is no
// country code.
func TestOptionScore_CountryCodeOrAlias(t *testing.T) {
	runScoreCases(t, []scoreCase{
		{"PO-COUNTRY", "United States", "US", "", 0.95, "country-equivalent"},
		{"PO-COUNTRY", "United Kingdom", "UK", "", 0.95, "country-equivalent"},
		{"PO-COUNTRY", "United States of America", "United States", "", 0.95, "country-equivalent"},
		{"PO-COUNTRY", "United States", "UK", "", 0, ""},
		{"PO-COUNTRY", "Norway", "No", "", 0, ""},
	})
}

func TestOptionScore_DestructiveGuard(t *testing.T) {
	const withdraw = "No thank you - I withdraw my interest in the role"
	runScoreCases(t, []scoreCase{
		{"PO-005", withdraw, withdraw, "", 1, "exact"},
		{"PO-005", "I withdraw", withdraw, "", 0, "destructive"},
		{"PO-005", "No", withdraw, "", 0, "destructive"},
		{"PO-005", "Yes", "Remove me from consideration", "", 0, "destructive"},
		{"PO-005", "No", "I do not consent", "", 0.85, "no-variant"},
		{"PO-005", "No", "No", "", 1, ""},
	})
	for _, label := range withdrawalPhrases {
		if !isWithdrawalLabel("Please " + label + " today") {
			t.Errorf("%q is not read as withdrawal", label)
		}
	}
	if isWithdrawalLabel("I do not consent") {
		t.Error("an ordinary negative is not destructive")
	}
}

func TestOptionScore_PlaceholderLabels(t *testing.T) {
	for _, l := range []string{"", "---", "\u2026", "-- No answer --", "-- Select --", "Select", "Select One", "select an option",
		"Choose", "Choose an option", "Please select", "Please choose one", "Make a selection", "Make a selection here",
		"No selection", "None", "None selected", "Pick one", "n/a", "NA", "Select...", "Select\u2026"} {
		if !isScorePlaceholder(l) {
			t.Errorf("%q should be a placeholder", l)
		}
	}
	for _, l := range []string{"Selected papers", "Choose life", "Nonetheless", "N/A for me", "Pick up location"} {
		if isScorePlaceholder(l) {
			t.Errorf("%q should not be a placeholder", l)
		}
	}
	runScoreCases(t, []scoreCase{
		{"PO-006", "No", "No Selection", "", 0, "placeholder"},
		{"PO-006", "No", "-- No answer --", "", 0, "placeholder"},
		{"PO-006", "Yes", "None", "", 0, "placeholder"},
		{"PO-006", "None", "None", "", 1, "exact"},
		{"PO-006", "N/A", "N/A", "", 1, "exact"},
		{"PO-006", "-- No answer --", "-- No answer --", "", 1, "exact"},
	})
}

func TestOptionScore_EeoConceptHook(t *testing.T) {
	const q = "Are you a protected veteran?"
	runScoreCases(t, []scoreCase{
		{"PO-007", "I am not a protected veteran", "No", q, 0.98, "veteran-stance exact"},
		{"PO-007", "I am not a protected veteran", "Yes", q, 0, "veteran stance conflict"},
		{"PO-007", "I am not a protected veteran", "No", "", 0, "no signal"},
		{"PO-007", "I am not a protected veteran", "No", "Do you have a valid driver license?", 0, "no signal"},
		{"PO-007", "Alex Example", "No", q, 0, ""},
		{"PO-007", "I am not a protected veteran", "I am not a Veteran", "", 0.84, "token-overlap"},
		{"PO-007", "I am a veteran but not a protected veteran", "I am a Veteran, but not a Protected Veteran", "", 0.98, "veteran-stance exact"},
		{"PO-007", "I am not a protected veteran", "I am a protected veteran", "", 0, "stance conflict"},
		{"PO-007", "I am a protected veteran", "I am a veteran", "", 0.94, "entailed"},
	})
}

func TestOptionScore_GenderEquivalence(t *testing.T) {
	runScoreCases(t, []scoreCase{
		{"PO-009", "Male", "Man", "", 0.95, "gender-equivalent"},
		{"PO-009", "Female", "Woman", "", 0.95, "gender-equivalent"},
		{"PO-009", "Man", "Male", "", 0.95, "gender-equivalent"},
		{"PO-009", "Male", "Male", "", 1, "exact"},
		{"PO-009", "Female", "Man", "", 0, ""},
		{"PO-009", "Male", "Woman", "", 0, ""},
		{"PO-009", "Man", "Woman", "", 0, ""},
		{"PO-009", "Female", "Male", "", 0, ""},
		{"PO-009", "Male", "M", "", 0, ""},
		{"PO-009", "Male", "Non-Binary", "", 0, ""},
		{"SI-022", "Male", "Prefer Not to Say", "", 0, ""},
		{"SI-022", "Woman", "Female", "", 0.95, "gender-equivalent"},
	})
	// Trend Micro (Workday): "Regular" is the full-time worker type.
	runScoreCases(t, []scoreCase{
		{"PO-009", "Full-Time", "Regular", "", 0.9, "employment-type"},
		{"PO-009", "Permanent", "Regular", "", 0.9, "employment-type"},
		{"PO-009", "Full-Time", "Intern", "", 0, ""},
		{"PO-009", "Part-Time", "Regular", "", 0, ""},
	})
}

func TestOptionScore_NumericRangeBoundaries(t *testing.T) {
	runScoreCases(t, []scoreCase{
		{"PO-010", "120000", "120k to 130k", "", 0.96, "numeric-range contains"},
		{"PO-010", "120000", "110k to 120k", "", 0.95, "upper-edge"},
		{"PO-010", "115000", "110k to 120k", "", 0.96, "contains"},
		{"PO-010", "250000", "200k or more", "", 0.96, "contains"},
		{"PO-010", "40000", "under 50k", "", 0.96, "contains"},
		{"PO-010", "$1,500,000", "1M-2M", "", 0.96, "contains"},
		{"PO-010", "7", "5-10", "", 0.96, "contains"},
		{"PO-010", "25%", "0-25%", "", 0.95, "upper-edge"},
		{"PO-010", "25", "0-25%", "", 0.825, "contains-answer"}, // percent mismatch: not a range hit
		{"PO-010", "300000", "110k to 120k", "", 0, ""},
		{"PO-010", "3", "Three", "", 0, ""},
		// Automox (Greenhouse, live): "in 1 Month" is one month, never one
		// million, so it is in no years bucket.
		{"PO-010", "in 1 Month", "9+", "", 0, ""},
		// A duration is compared only in the unit both name.
		{"PO-010", "3 weeks", "More than 3 months", "", 0, ""},
		{"PO-010", "4 months", "More than 3 months", "", 0.96, "contains"},
	})
	for _, c := range []struct {
		label string
		lo    float64
		ok    bool
	}{
		{"110k to 120k", 110000, true}, {"5 - 10", 5, true}, {"10 plus", 10, true}, {"at least 3 years", 3, true},
		{"up to 25%", 0, true}, {"No more than 4", 0, true}, {"Seven", 0, false}, {"5 years", 0, false},
	} {
		r, ok := rangeOfTokens(c.label, numericTokens(c.label))
		if ok != c.ok || ok && c.lo != 0 && r.min != c.lo {
			t.Errorf("rangeOfTokens(%q) = %+v, %v", c.label, r, ok)
		}
	}
}

func TestOptionScore_CeilingAnswerBoundsBucket(t *testing.T) {
	runScoreCases(t, []scoreCase{
		{"PO-011", "Up to 25%", "0-25%", "", 0.96, "ceiling"},
		{"PO-011", "Up to 25%", "25-50%", "", 0.95, "ceiling bounds the bucket below"},
		{"PO-011", "Up to 40%", "25-50%", "", 0.96, "ceiling"},
		{"PO-011", "at most 25%", "0-25%", "", 0.96, "ceiling"},
		{"PO-011", "no more than 25%", "0-25%", "", 0.96, "ceiling"},
		{"PO-011", "under 25%", "25-50%", "", 0.95, "ceiling"},
	})
	a := scoreOption(Option{Label: "0-25%"}, "Up to 25%", "").Score
	b := scoreOption(Option{Label: "25-50%"}, "Up to 25%", "").Score
	if a <= b {
		t.Fatalf("the bucket the ceiling bounds must win: %.2f vs %.2f", a, b)
	}
}

func TestOptionScore_DurationEquivalence(t *testing.T) {
	runScoreCases(t, []scoreCase{
		{"PO-012", "in 1 Month", "30 days", "", 0.95, "duration match"},
		{"PO-012", "in 1 Month", "28 days", "", 0.9, "duration match"},
		{"PO-012", "in 1 Month", "15 days", "", 0, ""},
		{"PO-012", "in 1 Month", "60 days", "", 0, ""},
		{"PO-012", "2 weeks", "14 days", "", 0.95, "duration match"},
		{"PO-012", "1 year", "12 months", "", 0, ""},
		{"PO-012", "0-1 years", "1 year", "", 0.75, "contained in answer"},
	})
	if _, ok := parseDurationDays("Not applicable"); ok {
		t.Fatal("a non-duration does not parse")
	}
}

func TestOptionScore_YesNoGuards(t *testing.T) {
	runScoreCases(t, []scoreCase{
		{"PO-014", "No", "I do not want to answer", "", 0, "decline synonym"},
		{"SI-019", "Yes", "I prefer not to answer", "", 0, "decline synonym"},
		{"SI-019", "No", "No, I am not willing to relocate", "", 0.85, "no-variant"},
		{"PO-014", "Yes", "Maybe, I am willing", "", 0, "MAYBE"},
		{"PO-014", "Yes", "Perhaps", "", 0, "MAYBE"},
		{"PO-014", "Yes", "Unlikely, I have constraints that would make this difficult", "", 0, "negative-hedge"},
		{"PO-014", "Yes", "Yes. I could realistically travel for the interview", "", 0.85, "yes-variant"},
		{"PO-014", "Yes", "Yes, it is unlikely to be a problem", "", 0.85, ""},
		{"PO-014", "Yes", "Rarely, but yes", "", 0, "negative-hedge"},
	})
	// The hedge rung never absorbs a Yes whatever the page order is.
	rungs := []string{"Unlikely, I have constraints that would make this difficult", "Yes. I could realistically travel for the interview", "No, I am unable to travel"}
	for _, order := range [][]int{{0, 1, 2}, {1, 0, 2}, {2, 0, 1}, {2, 1, 0}} {
		var os []Option
		for _, i := range order {
			os = append(os, Option{Value: rungs[i], Label: rungs[i]})
		}
		got := scoreAll(newOptionScorer("", nil), os, "Yes")
		sortScored(got)
		if got[0].opt.Label != rungs[1] {
			t.Fatalf("order %v chose %q", order, got[0].opt.Label)
		}
	}
}

func TestOptionScore_HyphenGlue(t *testing.T) {
	for _, c := range []struct {
		label string
		want  bool
		start int
	}{
		{"no-code platforms", false, 0}, {"no-op", false, 0}, {"non-technical", false, 0},
		{"no", true, 0}, {"No, I do not have a disability", true, 0}, {"No but willing to learn", true, 0},
		{"No experience: I have not integrated", true, 0}, {"Yes, I am authorized", true, 0},
		{"no - code", true, 0}, {"mostly no-code but no experience", true, 4},
	} {
		l := &labelProfile{norm: c.label, toks: tokenize(c.label)}
		h, ok := findVariantHit(l, sortedVariants("no", "yes"))
		if c.label == "Yes, I am authorized" {
			h, ok = findVariantHit(l, sortedVariants("yes"))
		}
		if ok != c.want || ok && h.start != c.start {
			t.Errorf("findVariantHit(%q) = %+v, %v; want %v at %d", c.label, h, ok, c.want, c.start)
		}
	}
	got := scoreOption(Option{Label: "No-code platforms"}, "No", "")
	if strings.Contains(got.Why, "no-variant") {
		t.Fatalf("a glued no is not a variant hit: %+v", got)
	}
}

func TestOptionScore_ReverseContainment(t *testing.T) {
	runScoreCases(t, []scoreCase{
		{"PO-019", "Example Labs of America", "Example Labs", "", 0.75, "contained in answer"},
		{"PO-019", "not available on weekends", "available", "", 0, ""},
		{"PO-019", "Senior Software Engineer", "Software Engineer", "", 0.75, "contained in answer"},
		{"PO-019", "Bachelor's degree in Example Studies", "Bachelor\u2019s degree", "", 0.75, "contained in answer"},
		{"PO-019", "Master\u2019s degree in Example Studies", "Master's degree", "", 0.75, "contained in answer"},
		{"PO-019", "I'm not a protected veteran", "I\u2019m", "", 0, ""},
	})
}

func TestOptionScore_OverlapAndStemEquality(t *testing.T) {
	runScoreCases(t, []scoreCase{
		{"PO-020", "Degree Bachelors", "Bachelor Degree", "", 0.85, "stemmed-set-equality"},
		{"PO-020", "Programs Masters", "Master Program", "", 0.85, "stemmed-set-equality"},
		{"PO-020", "Option A", "Option B", "", 0, ""},
		{"PO-020", "Python SQL Java", "Python SQL Go", "", 0.75, "token-overlap=0.50"},
		{"PO-020", "I am not a protected veteran", "I Am A Veteran", "", 0, "polarity"},
		{"PO-020", "Python SQL Java Rust", "Python SQL Java Go", "", 0.5 + 0.6/2, "token-overlap"},
	})
	if got := scoreOption(Option{Label: "Python SQL Java Go Rust"}, "Python SQL Java Go Rust Zig", "").Score; got > 0.84 {
		t.Fatalf("an overlap score is capped at 0.84, got %.2f", got)
	}
}

// A form that misspells a name in its options ("West Virgina" on a Lever state
// list) still offers that name: a label that is the whole answer with one slip
// in one word scores above the 0.75 of a shorter option the answer contains.
// A changed letter, a slip in a short word, two slips and a label that is not
// the whole answer name something else.
func TestOptionScore_MisspeltWholeAnswer(t *testing.T) {
	runScoreCases(t, []scoreCase{
		{"slip", "New Hampshire", "New Hampshre", "", 0.85, ""},
		{"slip", "Massachusetts", "Massachussetts", "", 0.85, ""},
		{"slip", "Wisconsin", "Wiscosnin", "", 0.85, ""},
		{"slip", "Ireland", "Iceland", "", 0, ""},
		{"slip", "Laos", "Lagos", "", 0, ""},
		{"slip", "Austria", "Australia", "", 0, ""},
		{"slip", "Rhode Island", "Rhdoe Islnad", "", 0, ""},
		{"slip", "Virginia", "West Virgina", "", 0, ""},
	})
}

func TestOptionScore_IrregularNegativeVerbs(t *testing.T) {
	for _, c := range []struct{ before, after string }{
		{"I can't relocate", "I can not relocate"},
		{"I cannot relocate", "I can not relocate"},
		{"I won't relocate", "I will not relocate"},
		{"I shan't relocate", "I shall not relocate"},
	} {
		if got := scoreOption(Option{Label: c.after}, c.before, ""); got.Score != 1 {
			t.Errorf("%q != %q: %+v", c.before, c.after, got)
		}
		if !PolarityConflict(c.before, "I relocate") {
			t.Errorf("negative verb lost polarity: %q", c.before)
		}
	}
}

// One list holds the spelled numbers and one the job boards; every pattern
// that reads a count or a board is built from it, so none can lack a word.
func TestAnswerBank_NumberWordsAndJobBoards_OneListServesEveryReader(t *testing.T) {
	words := strings.Split(countWords, "|")
	if len(words) != 12 || words[0] != "one" || words[11] != "twelve" {
		t.Fatalf("count words = %q", words)
	}
	for i, w := range words {
		n := i + 1
		if numberWords[w] != n {
			t.Errorf("numberWords[%q] = %d, want %d", w, numberWords[w], n)
		}
		if days, ok := parseLeadTime(w + " weeks"); !ok || days != n*7 {
			t.Errorf("parseLeadTime(%q weeks) = %d, %v", w, days, ok)
		}
		if days, ok := availabilityLeadDays(w + " weeks"); !ok || days != n*7 {
			t.Errorf("availabilityLeadDays(%q weeks) = %d, %v", w, days, ok)
		}
		if days, ok := parseDurationDays(w + " days"); !ok || days != n {
			t.Errorf("parseDurationDays(%q days) = %d, %v", w, days, ok)
		}
		if !durationFloorRE.MatchString("at least " + w + " years") {
			t.Errorf("durationFloorRE does not read %q", w)
		}
		if m := workedForFloorRE.FindStringSubmatch("Have you worked at a startup for at least " + w + " months?"); m == nil || m[3] != w {
			t.Errorf("workedForFloorRE does not read %q: %q", w, m)
		}
	}
	if days, ok := parseLeadTime("a month"); !ok || days != 30 {
		t.Errorf("parseLeadTime(a month) = %d, %v", days, ok)
	}
	for _, b := range strings.Split(jobBoards, "|") {
		if !jobBoardRE.MatchString(b) || !sourceBoardRE.MatchString(strings.ToUpper(b)) {
			t.Errorf("a job board pattern does not know %q", b)
		}
	}
}
