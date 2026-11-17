package match

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"os"
	"reflect"
	"slices"
	"strconv"
	"testing"
)

func TestOutcome_BandBoundaries(t *testing.T) {
	cases := []struct {
		c    float64
		want Outcome
		ok   bool
	}{
		{1.0, OutcomeFilled, true},
		{0.90, OutcomeFilled, true},
		{0.8999, OutcomeFilledReview, true},
		{0.89, OutcomeFilledReview, true},
		{0.70, OutcomeFilledReview, true},
		{0.6999, OutcomeAbstained, false},
		{0, OutcomeAbstained, false},
	}
	for _, c := range cases {
		got, ok := OutcomeForConfidence(c.c)
		if got != c.want || ok != c.ok {
			t.Errorf("OutcomeForConfidence(%v) = %v, %v", c.c, got, ok)
		}
	}
	if ClampConfidence(-1) != 0 || ClampConfidence(2) != 1 {
		t.Fatal("clamp")
	}
}

func TestOutcome_ClosedSet(t *testing.T) {
	got := []Outcome{OutcomeFilled, OutcomeFilledReview, OutcomeFilledModel, OutcomeAbstained}
	want := []Outcome{"filled", "filled-review", "filled-model", "abstained"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("outcomes = %v", got)
	}
}

func TestAbstain_ClosedReasonVocabulary(t *testing.T) {
	terminal := map[Reason]bool{
		ReasonNoEntry: false, ReasonInputMismatch: false, ReasonNarrativeDeferred: false, ReasonPlaceholderEmpty: false,
		ReasonAmbiguous: false, ReasonLowConfidence: false, ReasonDestructiveOption: false,
		ReasonSectionPolicy: true, ReasonContextPolicy: true, ReasonDuplicateLink: true, ReasonSelfIDPolicy: true,
		ReasonAskMe: true, ReasonAIInstruction: true,
	}
	names := map[Reason]string{
		ReasonNoEntry: "no-entry", ReasonInputMismatch: "input-mismatch", ReasonNarrativeDeferred: "narrative-deferred",
		ReasonPlaceholderEmpty: "placeholder-empty", ReasonAmbiguous: "ambiguous", ReasonLowConfidence: "low-confidence",
		ReasonDestructiveOption: "destructive-option", ReasonSectionPolicy: "section-policy", ReasonContextPolicy: "context-policy",
		ReasonDuplicateLink: "duplicate-link", ReasonSelfIDPolicy: "self-id-policy", ReasonAskMe: "ask-me", ReasonAIInstruction: "ai-instruction",
	}
	for r, term := range terminal {
		if string(r) != names[r] {
			t.Errorf("reason %q spelled %q", names[r], r)
		}
		if r.IsTerminal() != term {
			t.Errorf("reason %q terminal = %v", r, r.IsTerminal())
		}
	}
	lane := []Reason{ReasonUnsupported, ReasonResponseMismatch, ReasonModelAbstain, ReasonBudgetExhausted,
		ReasonNoRoute, ReasonSensitiveCategory, ReasonNoGrounding, ReasonUpstreamError, ReasonTooManyOptions}
	wantLane := []string{"unsupported", "response-mismatch", "model-abstain", "budget-exhausted",
		"no-route", "sensitive-category", "no-grounding", "upstream-error", "too-many-options"}
	for i, r := range lane {
		if string(r) != wantLane[i] || r.IsTerminal() {
			t.Errorf("lane reason %q", r)
		}
	}
	details := []Detail{DetailPostingCountry, DetailLocalCurrency, DetailHeldCompensation, DetailEmployerDetail,
		DetailSponsorshipConjunction, DetailDisqualifyingDisclose, DetailLegalDeclarationOff, DetailMarketingOptIn, DetailApplicationStatement, DetailCounterpartyEmployer, DetailResidenceClaim}
	wantDetails := []string{"posting_country", "local_currency", "held_compensation", "employer_detail", "sponsorship_conjunction",
		"disqualifying_disclosure", "legal_declaration_off", "marketing_opt_in", "application_statement", "counterparty_employer", "residence_claim"}
	for i, d := range details {
		if string(d) != wantDetails[i] {
			t.Errorf("detail %q", d)
		}
	}
}

func TestResult_Shape(t *testing.T) {
	rt := reflect.TypeOf(Result{})
	for _, f := range []string{"Selector", "Outcome", "Answer", "ChosenLabel", "ChosenLabels", "EntryID", "AnswerKey", "Source", "Confidence", "Reason", "Detail", "Terminal", "Trace"} {
		if _, ok := rt.FieldByName(f); !ok {
			t.Errorf("Result lacks %s", f)
		}
	}
	sources := []Source{SourceEntry, SourceDerived, SourceComposite, SourceObservation, SourceFormText, SourceConditional,
		SourceSelfIDDecline, SourceModelOption, SourceModelEntry, SourceComposed, SourceOwnerDefault}
	want := []string{"entry", "derived", "composite", "observation", "form-text", "conditional", "self-id-decline", "model-option", "model-entry", "composed", "owner-default"}
	for i, s := range sources {
		if string(s) != want[i] {
			t.Errorf("source %q", s)
		}
	}
}

func TestConfidence_FloorClamp(t *testing.T) {
	if ConfidenceFloor(0.40) != 0.70 || ConfidenceFloor(0) != 0.70 || ConfidenceFloor(0.95) != 0.95 || ConfidenceFloor(3) != 1 {
		t.Fatal("floor clamp")
	}
	if ConfidenceFloor(math.NaN()) != 0.70 || ConfidenceFloor(math.Inf(-1)) != 0.70 || ConfidenceFloor(math.Inf(1)) != 1 {
		t.Fatal("a floor that is no number clamps like one")
	}
	if _, ok := (Entry{}).Floor(); ok {
		t.Fatal("an entry without a floor has none")
	}
	f := 0.85
	if got, ok := (Entry{Meta: Meta{ConfidenceFloor: &f}}).Floor(); !ok || got != 0.85 {
		t.Fatalf("Floor = %v, %v", got, ok)
	}
}

func finalizeFor(e *Entry, q Question, d Draft, start float64) Result {
	st := &Step{Raw: q, Q: q}
	return st.finalize(d, e, start)
}

func TestConfidence_BandsAndCaps(t *testing.T) {
	txt := Question{Selector: "q", Kind: KindText, Text: "x"}
	sel := Question{Selector: "q", Kind: KindSelect, Options: opts("A", "B", "C"), Text: "x"}
	plain := &Entry{ID: "a-b", Kind: KindFixed, Body: Body{Answer: "x"}}
	review := &Entry{ID: "a-b", Kind: KindFixed, Body: Body{Answer: "x"}, Meta: Meta{NeedsReview: true}}
	stale := &Entry{ID: "a-b", Kind: KindFixed, Body: Body{Answer: "x"}, Meta: Meta{State: StateStale}}
	capped := &Entry{ID: "a-b", Kind: KindFixed, Body: Body{Answer: "x"}}
	c := 0.8
	capped.Confidence = &c
	floor := 0.95
	floored := &Entry{ID: "a-b", Kind: KindFixed, Body: Body{Answer: "x"}, Meta: Meta{ConfidenceFloor: &floor}}

	cases := []struct {
		name    string
		e       *Entry
		q       Question
		d       Draft
		start   float64
		outcome Outcome
		conf    float64
		reason  Reason
	}{
		{"auto band", plain, sel, Draft{HasOption: true, OptionScore: 0.95}, 0.95, OutcomeFilled, 0.95, ""},
		{"fuzzy start is capped at 0.89", plain, txt, Draft{Answer: "x"}, 0.89, OutcomeFilledReview, 0.89, ""},
		{"needs review caps at 0.89", review, txt, Draft{Answer: "x"}, 0.97, OutcomeFilledReview, 0.89, ""},
		{"stale caps at 0.89", stale, txt, Draft{Answer: "x"}, 1.0, OutcomeFilledReview, 0.89, ""},
		{"entry confidence caps", capped, txt, Draft{Answer: "x"}, 0.95, OutcomeFilledReview, 0.8, ""},
		{"option score below the route start", plain, sel, Draft{HasOption: true, OptionScore: 0.85}, 0.95, OutcomeFilledReview, 0.85, ""},
		{"curated verbatim one", plain, txt, Draft{Answer: "x"}, 1.0, OutcomeFilled, 1.0, ""},
		{"deterministic lift on a text control", plain, txt, Draft{Answer: "x", Deterministic: true}, 0.55, OutcomeFilledReview, 0.70, ""},
		{"deterministic does not lift a select with options", plain, sel, Draft{Answer: "x", Deterministic: true}, 0.55, OutcomeAbstained, 0, ReasonLowConfidence},
		{"clear winner lift", plain, sel, Draft{HasOption: true, OptionScore: 0.66, OtherValueScore: 0.46}, 0.95, OutcomeFilledReview, 0.70, ""},
		{"winner not clear enough", plain, sel, Draft{HasOption: true, OptionScore: 0.66, OtherValueScore: 0.52}, 0.95, OutcomeAbstained, 0, ReasonLowConfidence},
		{"winner below 0.45", plain, sel, Draft{HasOption: true, OptionScore: 0.44}, 0.95, OutcomeAbstained, 0, ReasonLowConfidence},
		{"weak non-deterministic abstains", plain, txt, Draft{Answer: "x"}, 0.6, OutcomeAbstained, 0, ReasonLowConfidence},
		{"entry floor applies after the lift", floored, txt, Draft{Answer: "x", Deterministic: true}, 0.55, OutcomeAbstained, 0, ReasonLowConfidence},
		{"entry floor below confidence passes", floored, txt, Draft{Answer: "x"}, 0.97, OutcomeFilled, 0.97, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := finalizeFor(c.e, c.q, c.d, c.start)
			if r.Outcome != c.outcome || r.Confidence != c.conf || r.Reason != c.reason {
				t.Fatalf("got %s %.2f %q, want %s %.2f %q (trace %v)", r.Outcome, r.Confidence, r.Reason, c.outcome, c.conf, c.reason, r.Trace)
			}
		})
	}
}

func TestAbstain_ShapeAndAnswerKey(t *testing.T) {
	e := &Entry{ID: "work-auth-us", Kind: KindFixed, Body: Body{Answer: "x"}}
	r := finalizeFor(e, Question{Selector: "s"}, Draft{Abstain: &Abstain{Reason: ReasonSectionPolicy}}, 0.95)
	if r.Outcome != OutcomeAbstained || !r.Terminal || r.Confidence != 0 || r.Answer != "" || r.AnswerKey != "work_auth_us" || r.EntryID != "work-auth-us" {
		t.Fatalf("abstain = %+v", r)
	}
	if !traceHas(r, "abstain-reason: section-policy") {
		t.Fatalf("trace = %v", r.Trace)
	}
	e.AnswerKey = "work_authorized"
	if got := finalizeFor(e, Question{}, Draft{Answer: "Yes"}, 0.95); got.AnswerKey != "work_authorized" || got.Source != SourceEntry {
		t.Fatalf("answer key %q source %q", got.AnswerKey, got.Source)
	}
	blank := finalizeFor(e, Question{}, Draft{Answer: ""}, 0.95)
	if blank.Outcome != OutcomeFilled || blank.Answer != "" {
		t.Fatalf("a blank fill is filled with an empty answer: %+v", blank)
	}
}

func TestTerminal_ReasonsAreTerminal(t *testing.T) {
	for _, r := range []Reason{ReasonSectionPolicy, ReasonContextPolicy, ReasonDuplicateLink, ReasonSelfIDPolicy, ReasonAskMe, ReasonAIInstruction} {
		if !(Abstain{Reason: r}).Terminal() {
			t.Errorf("%s must be terminal", r)
		}
	}
	for _, r := range []Reason{ReasonNoEntry, ReasonInputMismatch, ReasonNarrativeDeferred, ReasonPlaceholderEmpty, ReasonAmbiguous, ReasonLowConfidence, ReasonDestructiveOption} {
		if (Abstain{Reason: r}).Terminal() {
			t.Errorf("%s must not be terminal", r)
		}
	}
}

// The engine names matcher reasons too (engine/src/answers.ts); its test reads
// the same file, so a reason renamed on one side fails on the other.
const sharedReasons = "../../../engine/test/matcher-reasons.json"

func TestAbstain_ReasonVocabularySharedWithTheEngine(t *testing.T) {
	raw, err := os.ReadFile(sharedReasons)
	if err != nil {
		t.Fatal(err)
	}
	var shared struct {
		Reasons []string `json:"reasons"`
	}
	if err := json.Unmarshal(raw, &shared); err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "outcome.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var declared []string
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		if typ, ok := spec.Type.(*ast.Ident); !ok || typ.Name != "Reason" {
			return true
		}
		for _, v := range spec.Values {
			text, err := strconv.Unquote(v.(*ast.BasicLit).Value)
			if err != nil {
				t.Fatal(err)
			}
			declared = append(declared, text)
		}
		return true
	})
	if len(declared) == 0 || !slices.Equal(declared, shared.Reasons) {
		t.Fatalf("outcome.go declares %v, the shared vocabulary lists %v", declared, shared.Reasons)
	}
	if !slices.Contains(shared.Reasons, string(ReasonPlaceholderEmpty)) {
		t.Fatalf("the shared vocabulary lacks %q", ReasonPlaceholderEmpty)
	}
}
