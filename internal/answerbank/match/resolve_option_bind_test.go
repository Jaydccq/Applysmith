package match

import (
	"math"
	"strings"
	"testing"
)

// fixedEnt is a finished fixed entry reached by its curated example.
func fixedEnt(id, question, answer string, mods ...func(*Entry)) Entry {
	return ent(id, "fixed", append([]func(*Entry){withAnswer(answer), withExamples(question)}, mods...)...)
}

func TestDerived_BestOptionDomOrderTie(t *testing.T) {
	const q = "Are you willing to travel?"
	e := fixedEnt("travel-ok", q, "Yes")
	r := matchReq(pickMatcher(e), radioQ(q, "Yes", "No"))
	wantFill(t, r, "Yes", OutcomeFilled)
	if !traceHas(r, `top option scores: "Yes"=1.00`) {
		t.Fatalf("the trace lists the top options: %v", r.Trace)
	}
	// Equal-valued options tie and the first in page order is filled.
	same := Question{Selector: "s", Text: q, Kind: KindSelect, InputType: InputSingleSelect,
		Options: []Option{{Value: "y", Label: "Yes"}, {Value: "y", Label: "YES"}, {Value: "n", Label: "No"}}}
	r = matchReq(pickMatcher(e), same)
	if r.ChosenLabel != "Yes" || r.Outcome != OutcomeFilled || !traceHas(r, "same-value tie") {
		t.Fatalf("same-value tie: %+v", r)
	}
}

func TestDerived_DistinctValueTieAbstainsAmbiguous(t *testing.T) {
	const q = "Are you willing to travel?"
	e := fixedEnt("travel-ok", q, "Yes")
	r := matchReq(pickMatcher(e), radioQ(q, "Yes, I agree to travel", "Yes, I accept travel"))
	wantAbstain(t, r, ReasonAmbiguous, "")
}

// Krea (Ashby): Yes options that each name one office tie on the Yes; the
// relocation preference's ranking picks the top acceptable office (owner
// decision), and a tie without a policy, or over an option naming no place,
// stays ambiguous.
func TestDerived_PlaceNamingYesTieTakesTheRelocationRanking(t *testing.T) {
	const q = "Are you available to work full-time in-person in SF or NYC?"
	pol := &RelocationPolicy{Default: RelocationOpen, Places: []string{"New York", "Chicago", "San Francisco"}, Home: Home{City: "Charleston", Region: "SC", Country: "US"}}
	e := fixedEnt("available-full-time", q, "Yes")
	reloc := ent("preferred-work-location", "fixed", func(e *Entry) { e.LocationPolicy = &LocationPolicy{Resolved: pol} })
	r := matchReq(pickMatcher(e, reloc), checkboxQ(q, "Yes, from SF", "Yes, from NYC", "No"))
	wantFill(t, r, "Yes, from NYC", OutcomeFilledReview)
	r = matchReq(pickMatcher(e), checkboxQ(q, "Yes, from SF", "Yes, from NYC", "No"))
	wantAbstain(t, r, ReasonAmbiguous, "")
	r = matchReq(pickMatcher(e, reloc), checkboxQ(q, "Yes, from SF", "Yes, hybrid", "No"))
	wantAbstain(t, r, ReasonAmbiguous, "")
	// A bare "LA" the list leaves open (Los Angeles or Louisiana) ranks nothing.
	open := &RelocationPolicy{Default: RelocationOpen, Places: []string{"LA"}, Home: pol.Home}
	reloc = ent("preferred-work-location", "fixed", func(e *Entry) { e.LocationPolicy = &LocationPolicy{Resolved: open} })
	const la = "Are you available to work full-time in-person in Los Angeles or Boston?"
	r = matchReq(pickMatcher(fixedEnt("available-full-time", la, "Yes"), reloc), checkboxQ(la, "Yes, from Los Angeles", "Yes, from Boston", "No"))
	if r.Outcome != OutcomeAbstained {
		t.Fatalf("an open shared code ranked the tie: %+v", r)
	}
}

func TestDerived_BareYesNoOnLadderAbstains(t *testing.T) {
	const q = "Which best describes your hands-on experience building AI applications?"
	e := fixedEnt("ai-fixed", q, "No")
	rungs := []string{"No AI experience.", "I have experimented with AI tools.", "I have shipped AI features to production.", "I have led several production AI systems."}
	r := matchReq(pickMatcher(e), radioQ(q, rungs...))
	wantAbstain(t, r, ReasonInputMismatch, "")
	// Qualified yes/no sets are not scales and still answer.
	f := fixedEnt("spouse", "Are you an Armed Forces Spouse?", "No")
	r = matchReq(pickMatcher(f), radioQ("Are you an Armed Forces Spouse?", "Yes, I am an Armed Forces Spouse/Partner", "No, not an Armed Forces Spouse/Partner"))
	wantFill(t, r, "No, not an Armed Forces Spouse/Partner", OutcomeFilledReview)
}

func TestOptions_CategoryOtherDichotomy(t *testing.T) {
	const q = "Export control status"
	a := "A - US Citizen, US National, Lawful Permanent Resident, Refugee or Asylum"
	b := "B - Other (includes non-immigrant visa status such as B, E, F1, H, J, L, TN)"
	no := fixedEnt("export-status", q, "No")
	wantFill(t, matchReq(pickMatcher(no), selectQ(q, a, b)), b, OutcomeFilled)
	yes := fixedEnt("export-status", q, "Yes")
	wantFill(t, matchReq(pickMatcher(yes), selectQ(q, a, b)), a, OutcomeFilled)
	if _, ok := categoryDichotomy("No", opts("Yes, in person", "Other")); ok {
		t.Fatal("the category must not lead with yes")
	}
	if _, ok := categoryDichotomy("No", opts("US", "Other")); ok {
		t.Fatal("the category needs two words")
	}
	if _, ok := categoryDichotomy("Maybe", opts(a, b)); ok {
		t.Fatal("only a bare Yes or No")
	}
	if o, ok := categoryDichotomy("No", opts("A) Citizens of the US", "(b) Other", "Select...")); !ok || o.Label != "(b) Other" {
		t.Fatalf("option-letter prefixes and placeholders: %+v %v", o, ok)
	}
}

func TestOptions_UnearnedResidenceRejected(t *testing.T) {
	const q = "Are you willing to work in the office?"
	e := fixedEnt("office-ok", q, "Yes", func(e *Entry) {
		e.LocationPolicy = &LocationPolicy{Predicate: PredicateWillingToWorkAt, Resolved: &RelocationPolicy{Default: RelocationOpen}}
	})
	r := matchReq(pickMatcher(e), radioQ(q, "Yes - I currently live in the Example City area", "Yes, I am willing to work from the office", "No"))
	wantFill(t, r, "Yes, I am willing to work from the office", OutcomeFilledReview)
	if !traceHas(r, "states where the applicant lives") {
		t.Fatalf("rejection is traced: %v", r.Trace)
	}
	// Only for a policy-bound entry and only for a Yes.
	plain := fixedEnt("office-ok", q, "Yes")
	r = matchReq(pickMatcher(plain), radioQ(q, "Yes - I currently live in the Example City area", "No"))
	wantFill(t, r, "Yes - I currently live in the Example City area", OutcomeFilledReview)
	// BILL (Greenhouse): a custom listbox whose one Yes is the rejected
	// residence never hands "Yes" to the engine's search, which would pick it.
	bill := Question{Selector: "s", Text: q, Kind: KindCustomListbox,
		Options: opts("Yes, I currently live in one of the eligible states", "No, but I am open to relocating to an eligible state", "No, I am not open to relocation")}
	r = matchReq(pickMatcher(e), bill)
	if r.Outcome != OutcomeAbstained || traceHas(r, "open sample") {
		t.Fatalf("the rejected residence is not searched for: %+v", r)
	}
}

func TestOpenSet_OtherOnLongProperNounMenu(t *testing.T) {
	const q = "University"
	var schools []string
	for _, n := range []string{"Alder", "Birch", "Cedar", "Dogwood", "Elm", "Fir", "Gum", "Hazel"} {
		schools = append(schools, n+" University")
	}
	e := fixedEnt("most-recent-school", q, "Example Polytechnic")
	menu := append(append([]string(nil), schools...), "Other")
	r := matchReq(pickMatcher(e), selectQ(q, menu...))
	wantFill(t, r, "Other", OutcomeFilledReview)
	if r.Confidence > FuzzyConfidenceCap {
		t.Fatalf("always in the review band: %v", r.Confidence)
	}
	// A listed school is chosen by scoring first.
	listed := fixedEnt("most-recent-school", q, "Cedar University")
	wantFill(t, matchReq(pickMatcher(listed), selectQ(q, menu...)), "Cedar University", OutcomeFilled)
	// A short menu is not an open set.
	short := selectQ(q, "Alder University", "Birch University", "Other", "Other (please specify)", "Other:", "Other - please specify", "Other...", "Other (specify)", "Other")
	short.Options = short.Options[:3]
	if r := matchReq(pickMatcher(e), short); r.Outcome != OutcomeAbstained {
		t.Fatalf("short menu: %+v", r)
	}
	// Duplicate rows do not inflate a short menu.
	dup := selectQ(q, "Alder University", "Other")
	for i := 0; i < 10; i++ {
		dup.Options = append(dup.Options, Option{Value: "Alder University", Label: "Alder University " + strings.Repeat(" ", i)})
	}
	if _, ok := openSetOther(&e, dup); ok {
		t.Fatal("distinct values, not rows")
	}
	// Other is a list-gap choice for the five proper-noun entries only.
	race := fixedEnt("eeo-race", q, "Example Polytechnic")
	if _, ok := openSetOther(&race, selectQ(q, menu...)); ok {
		t.Fatal("a closed demographic set has no list gap")
	}
	for _, l := range []string{"Other", "other (specify)", "Other (please specify)", "Other:", "Other - please specify", "OTHER\u2026"} {
		if !otherLabel.MatchString(l) {
			t.Errorf("%q should be the Other label", l)
		}
	}
	if otherLabel.MatchString("Other Social Networking Sites") {
		t.Error("only the bare Other label")
	}
}

func TestOpenSet_EmployerLabelGate(t *testing.T) {
	cc := fixedEnt("current-company", "x", "Example Labs", withTypes(InputText))
	for q, want := range map[string]bool{
		"Current employer":                                 true,
		"* Current Employer *":                             true,
		"What is your current employer?":                   true,
		"Most recent company (if applicable)":              true,
		"Current/Previous Employer":                        true,
		"Present company":                                  true,
		"Who is or was your current employer?":             true,
		"What notice must you give your current employer?": false,
		"Current employer restrictions":                    false,
		"Employer":                                         false,
	} {
		if got := allowsEmployerSelection(&cc, q); got != want {
			t.Errorf("allowsEmployerSelection(%q) = %v, want %v", q, got, want)
		}
	}
	other := fixedEnt("most-recent-school", "x", "Example", withTypes(InputText))
	if allowsEmployerSelection(&other, "Current employer") {
		t.Error("only the current-company entry")
	}
	pick := ent("current-company", "pick-option")
	if allowsEmployerSelection(&pick, "Current employer") {
		t.Error("only a fixed entry")
	}
	noText := fixedEnt("current-company", "x", "Example Labs", withTypes(InputYesNo))
	if allowsEmployerSelection(&noText, "Current employer") {
		t.Error("an entry that does not allow text")
	}
}

func TestDerived_BestOptionBelowFloorPaths(t *testing.T) {
	const q = "Preferred language"
	// A clear winner below the gate (entry cap) fills in the review band.
	capped := fixedEnt("pref-lang", q, "Yes", withCap(0.5))
	r := matchReq(pickMatcher(capped), radioQ(q, "Yes", "No"))
	wantFill(t, r, "Yes", OutcomeFilledReview)
	if r.Confidence != ReviewFillConfidence || !traceHas(r, "clear winner") {
		t.Fatalf("margin fill: %+v", r)
	}
	// No option reaches the floor and no winner: low confidence.
	e := fixedEnt("pref-lang", q, "Klingon")
	r = matchReq(pickMatcher(e), selectQ(q, "English", "Spanish", "French"))
	wantAbstain(t, r, ReasonLowConfidence, "")
	// A lone non-matching option on a free-text entry is a stale typeahead: defer.
	city := fixedEnt("address-city", "City", "Springfield", withTypes(InputText))
	lb := Question{Selector: "s", Text: "City", Kind: KindCustomListbox, InputType: InputSingleSelect, Options: opts("+247 AC")}
	r = matchReq(pickMatcher(city), lb)
	if r.Answer != "Springfield" || r.ChosenLabel != "Springfield" {
		t.Fatalf("placeholder typeahead: %+v", r)
	}
}

func TestOptionMargin_PureHelpers(t *testing.T) {
	type row struct {
		value, label string
		score        float64
	}
	mk := func(rows ...row) []scoredOption {
		var out []scoredOption
		for i, r := range rows {
			out = append(out, scoredOption{opt: Option{Value: r.value, Label: r.label}, index: i, optScore: optScore{Score: r.score}})
		}
		return out
	}
	if clearWinnerMargin(nil) != 0 {
		t.Fatal("empty list")
	}
	if !math.IsInf(clearWinnerMargin(mk(row{"y", "Yes", 0.9}, row{"y", "YES", 0.9})), 1) {
		t.Fatal("no distinct competitor is an infinite lead")
	}
	l := mk(row{"y", "Yes", 0.9}, row{"y", "YES", 0.8}, row{"n", "No", 0.5})
	if m := clearWinnerMargin(l); math.Abs(m-0.4) > 1e-9 {
		t.Fatalf("margin skips same-value runners-up: %v", m)
	}
	if !isClearWinner(l) {
		t.Fatal("clear winner")
	}
	if isClearWinner(mk(row{"a", "A", 0.44}, row{"b", "B", 0})) {
		t.Fatal("below the value floor")
	}
	if isClearWinner(mk(row{"a", "A", 0.6}, row{"b", "B", 0.5})) {
		t.Fatal("margin under 0.15")
	}
	if ClearWinnerFloor != 0.45 || ClearWinnerLead != 0.15 {
		t.Fatal("audited thresholds")
	}
	// Empty values compare by label, so two real choices are never one value.
	if optionValue(Option{Label: "A"}) == optionValue(Option{Label: "B"}) {
		t.Fatal("empty values")
	}
}
