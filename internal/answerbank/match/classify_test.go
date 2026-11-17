package match

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// classifyWith runs curated then general classification over a synthetic bank.
func classifyWith(entries []Entry, q Question, routes []Route) (classification, *classifier) {
	p := prepareQuestion(q)
	cx := newClassifier(newClassIndex(entries), p.Q, nil)
	if routes != nil {
		cs, err := compileRoutes(routes)
		if err != nil {
			panic(err)
		}
		cx.routes = cs
	} else {
		cx.routes = nil
	}
	if cl, ok := cx.curated(); ok {
		return cl, cx
	}
	return cx.general(), cx
}

func entryID(cl classification) string {
	if cl.entry == nil {
		return ""
	}
	return cl.entry.ID
}

// SharedPhrasings reports exactly the wordings the exact-wording stage cannot
// decide: each reported one has several owners there, each other one a single
// owner. Explanation entries and archived ones are left out.
func TestSharedPhrasings(t *testing.T) {
	why := func(id string) Entry {
		return ent(id+"-why", "fixed", withExamples("Please explain"))
	}
	linked := func(id string) Entry {
		e := ent(id, "fixed", withExamples(id+" ever?"))
		e.LinkedExplainID = id + "-why"
		return e
	}
	gone := ent("gone", "fixed", withExamples("Visa status?"))
	gone.State = StateArchived
	es := []Entry{
		ent("auth", "fixed", withExamples("Are you authorized to work in the US?", "Visa status")),
		ent("visa", "fixed", withExamples("are you  authorized to work in the us", "AUTHORIZED*:")),
		ent("other", "fixed", withExamples("Authorized", "Are you authorized to work in the US.")),
		linked("debarred"), why("debarred"), linked("excluded"), why("excluded"), gone,
	}
	want := map[string][]SharedPhrasing{
		"auth":  {{"Are you authorized to work in the US?", []string{"visa", "other"}}},
		"visa":  {{"are you  authorized to work in the us", []string{"auth", "other"}}, {"AUTHORIZED*:", []string{"other"}}},
		"other": {{"Authorized", []string{"visa"}}, {"Are you authorized to work in the US.", []string{"auth", "visa"}}},
	}
	if got := SharedPhrasings(es); !reflect.DeepEqual(got, want) {
		t.Fatalf("SharedPhrasings = %+v\nwant %+v", got, want)
	}
	active := es[:len(es)-1]
	activeIx := newClassIndex(active)
	for _, e := range active {
		if strings.HasSuffix(e.ID, "-why") {
			continue
		}
		for _, ex := range e.Examples {
			_, _, decided := verbatimMatch(activeIx, ex, true, false, func(*Entry) bool { return true })
			shared := slices.ContainsFunc(want[e.ID], func(s SharedPhrasing) bool { return s.Phrasing == ex })
			if decided == shared {
				t.Errorf("%s %q: the exact-wording stage decides = %v, reported shared = %v", e.ID, ex, decided, shared)
			}
		}
	}
}

func TestClassify_ExactExampleHeadingStripped(t *testing.T) {
	es := []Entry{ent("citizenship-and-status", "fixed", withAnswer("US citizen"),
		withExamples("Please indicate your citizenship and immigration status"))}
	cl, _ := classifyWith(es, textQ("s", "Export Control Question 2: Please indicate your citizenship and immigration status."), nil)
	if entryID(cl) != "citizenship-and-status" || cl.start != 1.0 {
		t.Fatalf("classification = %+v", cl)
	}
	if _, ok := headingStripped(": no heading"); ok {
		t.Fatal("a colon at index 0 is no heading")
	}
	if _, ok := headingStripped("Heading: "); ok {
		t.Fatal("an empty tail is no heading")
	}
}

func repeatTokens(prefix string, n int) string {
	var b []string
	for i := 0; i < n; i++ {
		b = append(b, prefix+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	return strings.Join(b, " ")
}

func TestClassify_CuratedLongExampleCoverage(t *testing.T) {
	notice := repeatTokens("notice", 20)
	es := []Entry{
		ent("export-notice", "fixed", withAnswer("Opt In"), withExamples(notice)),
		ent("us-person", "fixed", withAnswer("No"), withExamples("us person")),
	}
	q := textQ("s", notice+" plus one extra added sentence here")
	cl, _ := classifyWith(es, q, nil)
	if entryID(cl) != "export-notice" || cl.stage != stageCuratedLong || cl.start != 0.95 {
		t.Fatalf("classification = %+v", cl)
	}
	// Below 90 percent coverage the lane does not decide.
	cut := textQ("s", strings.Join(strings.Fields(notice)[:15], " ")+" "+repeatTokens("other", 6))
	if cl, _ = classifyWith(es, cut, nil); cl.stage == stageCuratedLong {
		t.Fatal("75 percent coverage must not qualify")
	}
	// Two entries qualifying decide nothing.
	dup := append([]Entry{ent("export-twin", "fixed", withAnswer("Opt Out"), withExamples(notice))}, es...)
	if cl, _ = classifyWith(dup, q, nil); cl.stage == stageCuratedLong {
		t.Fatal("two qualifying entries decide nothing")
	}
	// An uncurated entry never uses the lane.
	unconf := []Entry{ent("export-notice", "fixed", withAnswer("Opt In"), withExamples(notice), notCurated())}
	if cl, _ = classifyWith(unconf, q, nil); cl.stage == stageCuratedLong {
		t.Fatal("only curated entries use the long-example lane")
	}
}

func TestFuzzy_ThresholdBoundaries(t *testing.T) {
	ix := newClassIndex([]Entry{{ID: "a"}, {ID: "b"}})
	cases := []struct {
		name   string
		scored []fuzzyScore
		found  bool
		tie    bool
	}{
		{"floor exactly", []fuzzyScore{{0, 0.40, 2}}, true, false},
		{"under the score floor", []fuzzyScore{{0, 0.39, 2}}, false, false},
		{"one shared token", []fuzzyScore{{0, 0.9, 1}}, false, false},
		{"margin of 0.11", []fuzzyScore{{0, 0.60, 2}, {1, 0.49, 2}}, true, false},
		{"0.50 minus 0.40 is a lead of 0.10", []fuzzyScore{{0, 0.50, 2}, {1, 0.40, 2}}, true, false},
		{"0.62 against 0.58", []fuzzyScore{{0, 0.62, 3}, {1, 0.58, 3}}, false, true},
		{"runner-up below the floors still ties", []fuzzyScore{{0, 0.45, 2}, {1, 0.38, 1}}, false, true},
		{"clear lead", []fuzzyScore{{0, 0.80, 3}, {1, 0.40, 2}}, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := ix.fuzzyPick("anything", c.scored)
			if r.found != c.found || r.tie != c.tie {
				t.Fatalf("found %v tie %v, want %v %v", r.found, r.tie, c.found, c.tie)
			}
		})
	}
}

func fuzzyBank() []Entry {
	return []Entry{
		ent("kube-ops", "fixed", withAnswer("Yes"), notCurated(), withDescription("kubernetes cluster operations")),
		ent("db-admin", "fixed", withAnswer("No"), notCurated(), withDescription("database administration backups")),
	}
}

func TestFuzzy_TopicOverlapGuard(t *testing.T) {
	es := []Entry{ent("about-you", "fixed", notCurated(), withExamples("Tell us about yourself"))}
	ix := newClassIndex(es)
	if got := ix.scoreEntries("Please tell us about your submitted project", nil, func(int) bool { return true }); len(got) != 0 {
		t.Fatalf("opener words alone are not a topic: %+v", got)
	}
	if got := ix.scoreEntries("Tell us about yourself", nil, func(int) bool { return true }); len(got) != 1 {
		t.Fatalf("a question made only of opener words keeps the seed: %+v", got)
	}
}

func TestFuzzy_RunContextEntriesAreNeverCandidates(t *testing.T) {
	es := []Entry{
		{ID: "cover-letter-submitted", Kind: KindFixed, Body: Body{Answer: "Yes"}, Description: "submitted cover letter attached", Runtime: Runtime{RunContext: true}},
	}
	cl, _ := classifyWith(es, textQ("s", "cover letter submitted attached"), nil)
	if cl.entry != nil {
		t.Fatalf("fuzzy offered a run-context entry: %+v", cl)
	}
}

func TestFuzzy_SubjectGuard(t *testing.T) {
	es := []Entry{
		ent("ehr-software", "fixed", notCurated(), withExamples("Do you have experience with EHR software systems and practices?")),
		ent("backend-apis", "fixed", notCurated(), withAnswer("x"), withExamples("Do you build backend API services?")),
	}
	for _, id := range []string{"filler-one", "filler-two", "filler-three"} {
		es = append(es, ent(id, "fixed", notCurated(), withExamples("Describe your experience with software practices")))
	}
	ix := newClassIndex(es)
	q := "Describe experience with backend APIs and software practices"
	scored := ix.scoreEntries(q, nil, func(i int) bool { return i == 0 })
	if len(scored) == 0 {
		t.Fatal("setup: the EHR entry must score")
	}
	r := ix.fuzzyPick(q, scored)
	if r.found || !r.different {
		t.Fatalf("pick = %+v", r)
	}
}

func TestFuzzy_TieAbstainsAmbiguous(t *testing.T) {
	es := []Entry{
		ent("alpha-one", "fixed", notCurated(), withDescription("remote work policy preference")),
		ent("alpha-two", "fixed", notCurated(), withDescription("remote work policy notes")),
	}
	cl, _ := classifyWith(es, textQ("s", "remote work policy"), nil)
	if cl.entry != nil || cl.abstain == nil || cl.abstain.Reason != ReasonAmbiguous {
		t.Fatalf("classification = %+v", cl)
	}
}

func TestFuzzy_QuestionCoverageIsOnlyATraceNumber(t *testing.T) {
	cl, _ := classifyWith(fuzzyBank(), textQ("s", "Kubernetes cluster operations planning"), nil)
	if cl.entry == nil || cl.stage != stageFuzzy || !strings.Contains(cl.via, "qcov=") {
		t.Fatalf("classification = %+v", cl)
	}
	// The same entry wins with the same score whatever the question adds around it.
	wide, _ := classifyWith(fuzzyBank(), textQ("s", "Kubernetes cluster operations planning for next quarter"), nil)
	if wide.entry == nil || wide.entry.ID != cl.entry.ID {
		t.Fatalf("classification = %+v", wide)
	}
}

func TestClassify_SelfIdAxisRescue(t *testing.T) {
	es := []Entry{ent("eeo-disability", "pick-option"), ent("eeo-veteran", "pick-option")}
	q := Question{Selector: "s", Kind: KindRadioGroup, Text: "Please check one of the boxes below", Required: true,
		Options: opts("Yes, I have a disability or have had one in the past", "No, I do not have a disability", "I do not want to answer")}
	cl, _ := classifyWith(es, q, nil)
	if entryID(cl) != "eeo-disability" || cl.stage != stageOptionOnly {
		t.Fatalf("classification = %+v", cl)
	}
	vet := Question{Selector: "s", Kind: KindRadioGroup, Text: "Please check one", Options: opts("I am a protected veteran", "I am not a protected veteran", "Decline")}
	if cl, _ = classifyWith(es, vet, nil); entryID(cl) != "eeo-veteran" {
		t.Fatalf("veteran: %+v", cl)
	}
	race := Question{Selector: "s", Kind: KindRadioGroup, Text: "Please check one", Options: opts("Asian", "Black", "White", "Decline")}
	if cl, _ = classifyWith(es, race, nil); cl.entry != nil {
		t.Fatalf("other axes are not routed by option shape: %+v", cl)
	}
	if selfIDAxis(opts("A disability", "A veteran", "Decline"), nil) != "" {
		t.Fatal("two axes at once name no axis")
	}
}

func TestShape_ControlAcceptance(t *testing.T) {
	compEntry := func(answer string) Entry {
		e := ent("hourly-pay-rate", "fixed", withAnswer(answer), withTypes(InputNumber))
		e.Category = CategoryCompensation
		return e
	}
	selectOnly := ent("startup-yes", "fixed", withAnswer("Yes"), withTypes(InputYesNo, InputSingleSelect))
	city := ent("address-city", "fixed", withAnswer("Springfield"))
	cases := []struct {
		name string
		e    Entry
		q    Question
		ok   bool
	}{
		{"scalar in a one-line box", selectOnly, Question{Kind: KindText, InputType: InputText, Text: "Startup experience?"}, true},
		// General Mills (Workday, live).
		{"a bare yes never answers a when question", selectOnly, Question{Kind: KindText, InputType: InputText, Text: "When did you graduate from high school?"}, false},
		{"nor a date label", selectOnly, Question{Kind: KindText, InputType: InputText, Text: "High school graduation date"}, false},
		{"another scalar answers a which question", ent("x", "fixed", withAnswer("Springfield"), withTypes(InputSingleSelect)), Question{Kind: KindText, InputType: InputText, Text: "Which city do you live in?"}, true},
		{"scalar needs a one-line answer", ent("x", "fixed", withAnswer(strings.Repeat("a", 90)), withTypes(InputYesNo)), Question{Kind: KindText, InputType: InputText}, false},
		{"narrative box never gets yes", selectOnly, Question{Kind: KindText, InputType: InputLongText, Text: "Describe your experience with Python"}, false},
		{"async combobox with no options", city, Question{Kind: KindSelect, InputType: InputSingleSelect, Text: "City"}, true},
		{"stale single option", city, Question{Kind: KindCustomListbox, InputType: InputSingleSelect, Text: "City", Options: opts("+247 AC")}, true},
		{"real dropdown rejects a free-text entry", ent("x", "fixed", withAnswer("v"), withTypes(InputText)), Question{Kind: KindSelect, InputType: InputSingleSelect, Text: "City", Options: opts("A", "B", "C")}, false},
		{"fixed choice select with a select-less entry", ent("x", "fixed", withAnswer("v"), withTypes(InputYesNo)), Question{Kind: KindSelect, InputType: InputSingleSelect, Text: "City", Options: opts("A", "B", "C")}, false},
		{"employer open set", ent("current-company", "fixed", withAnswer("Example Labs"), withTypes(InputText)), Question{Kind: KindSelect, InputType: InputSingleSelect, Text: "Current employer", Options: opts("A", "B", "C")}, true},
		{"employer mentioned in a notice question", ent("current-company", "fixed", withAnswer("Example Labs"), withTypes(InputText)), Question{Kind: KindSelect, InputType: InputSingleSelect, Text: "What notice must you give your current employer?", Options: opts("A", "B", "C")}, false},
		{"date layout in a textarea", ent("grad", "fixed", withAnswer("2027-05"), withTypes(InputDate)), Question{Kind: KindText, InputType: InputLongText, Text: "Estimated graduation date (please enter in this format MM/YYYY)"}, true},
		{"date without a layout", ent("grad", "fixed", withAnswer("2027-05"), withTypes(InputDate)), Question{Kind: KindText, InputType: InputLongText, Text: "Graduation date"}, false},
		// PlantingSpace (Ashby, live): a textarea whose question asks for a date.
		{"a textarea whose question asks for a date", ent("earliest-start-date", "fixed", withAnswer("2026-12-01"), withTypes(InputDate)), Question{Kind: KindText, InputType: InputLongText, Text: "If you know already: what is the earliest date you would want to start working with us?"}, true},
		{"a date question that also asks why", ent("earliest-start-date", "fixed", withAnswer("2026-12-01"), withTypes(InputDate)), Question{Kind: KindText, InputType: InputLongText, Text: "What date could you start, and why?"}, false},
		// Procore (Workday, live): every questionnaire text answer is a textarea.
		{"a one-line answer in a textarea that asks for it", ent("current-job-title", "fixed", withAnswer("Software Engineer"), withTypes(InputText)), Question{Kind: KindText, InputType: InputLongText, Text: "What is your current or most recent job title?"}, true},
		{"a textarea that asks for a narrative", ent("current-job-title", "fixed", withAnswer("Software Engineer"), withTypes(InputText)), Question{Kind: KindText, InputType: InputLongText, Text: "Describe your current role and what you are responsible for"}, false},
		{"a one-line answer the entry never types", selectOnly, Question{Kind: KindText, InputType: InputLongText, Text: "Startup experience?"}, false},
		// Avride (Greenhouse, live): "Please share your hourly salary
		// requirements for this role." is a textarea; the hourly rate is a
		// compensation figure as the salary expectation is.
		{"an hourly rate in a textarea", compEntry("58"), Question{Kind: KindText, InputType: InputLongText, Text: "Please share your hourly salary requirements for this role."}, true},
		{"a compensation entry that is no figure in a textarea", compEntry("Depends on the role"), Question{Kind: KindText, InputType: InputLongText, Text: "Please share your hourly salary requirements for this role."}, false},
		// Microchip (Workday, live): a single choice asked as a checkbox group.
		{"a single choice in a checkbox group that asks for one", ent("desired-career-level", "pick-option", withTypes(InputSingleSelect)), Question{Kind: KindCheckbox, InputType: InputMultiSelect, Text: "Please select the career level desired?", Options: opts("Director", "Entry Level", "Manager")}, true},
		{"a checkbox group that asks for all that apply", ent("desired-career-level", "pick-option", withTypes(InputSingleSelect)), Question{Kind: KindCheckbox, InputType: InputMultiSelect, Text: "Select all that apply: career levels you would consider", Options: opts("Director", "Entry Level", "Manager")}, false},
		// Truveta (Greenhouse, live): a Yes/No asked as a checkbox group of Yes, No and Not Applicable.
		{"a Yes/No in a checkbox group of Yes and No", ent("willing-to-relocate", "fixed", withAnswer("Yes"), withTypes(InputYesNo)), Question{Kind: KindCheckbox, InputType: InputMultiSelect, Text: "If not located in the greater Seattle area, are you willing to relocate?", Options: opts("Yes", "No", "Not Applicable (I am already located in Seattle)")}, true},
		{"a Yes/No entry in a checkbox group of several statements", ent("willing-to-relocate", "fixed", withAnswer("Yes"), withTypes(InputYesNo)), Question{Kind: KindCheckbox, InputType: InputMultiSelect, Text: "Do you now OR in the future require visa sponsorship to continue working in the US?", Options: opts("Yes, I am on an F1 Visa/CPT/OPT", "Yes, I am on an H1B Visa", "No, I do not require sponsorship either now OR in the future", "Other")}, false},
		// The sponsorship entry takes the same group: its resolver places the Yes by the recorded visa or leaves it to the user.
		{"the sponsorship entry in a checkbox group whose Yes is split by visa", ent("sponsorship-now-or-future", "fixed", withAnswer("Yes"), withTypes(InputYesNo)), Question{Kind: KindCheckbox, InputType: InputMultiSelect, Text: "Do you now OR in the future require visa sponsorship to continue working in the US?", Options: opts("Yes, I am on an F1 Visa/CPT/OPT", "Yes, I am on an H1B Visa", "No, I do not require sponsorship either now OR in the future", "Other")}, true},
		// Envoy (Ashby, live): a profile-derived Yes/No asked in a multi-line box that asks for nothing more.
		{"a derived Yes/No in a textarea that asks it", ent("sponsorship-now-or-future", "fixed", withTypes(InputYesNo)), Question{Kind: KindText, InputType: InputLongText, Text: "Will you now or in the future require Envoy to sponsor an immigration case in order to employ you? If you’ll require Envoy to commence, i.e., “sponsor,” an immigration or work permit case in order to employ you, either now or at some point in the near future, then you should answer yes."}, true},
		{"a derived Yes/No in a textarea that asks for a list", ent("sponsorship-now-or-future", "fixed", withTypes(InputYesNo)), Question{Kind: KindText, InputType: InputLongText, Text: "Will you require sponsorship? If yes, please list your current visa type."}, false},
		{"a derived Yes/No in a textarea that asks a second question", ent("sponsorship-now-or-future", "fixed", withTypes(InputYesNo)), Question{Kind: KindText, InputType: InputLongText, Text: "Do you require sponsorship? When does your current status expire?"}, false},
		{"a derived Yes/No in a textarea that asks why", ent("sponsorship-now-or-future", "fixed", withTypes(InputYesNo)), Question{Kind: KindText, InputType: InputLongText, Text: "Will you require sponsorship? Please explain."}, false},
		{"exact curated example in a textarea", ent("cit", "fixed", withAnswer(strings.Repeat("long answer ", 12)), withExamples("Please indicate your citizenship and status"), withTypes(InputYesNo)), Question{Kind: KindText, InputType: InputLongText, Text: "Please indicate your citizenship and status"}, true},
		{"short exact example without narrative verbs", func() Entry {
			e := ent("cit", "fixed", withAnswer("US"), withExamples("Citizenship"), withTypes(InputYesNo))
			e.NeedsReview = true
			return e
		}(), Question{Kind: KindText, InputType: InputLongText, Text: "Citizenship"}, true},
		{"narrative verb blocks the short example", func() Entry {
			e := ent("cit", "fixed", withAnswer("US"), withExamples("Explain your citizenship"), withTypes(InputYesNo))
			e.NeedsReview = true
			return e
		}(), Question{Kind: KindText, InputType: InputLongText, Text: "Explain your citizenship"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := c.e
			if got := acceptsControl(&e, c.q); got.OK != c.ok {
				t.Fatalf("OK = %v, want %v (%s)", got.OK, c.ok, got.Note)
			}
		})
	}
	if v := acceptsControl(&selectOnly, Question{Kind: KindText, InputType: InputText}); !strings.Contains(v.Note, "shape gap") {
		t.Fatalf("the shape gap is recorded: %q", v.Note)
	}
}

// A fuzzy winner that leads the runner-up by exactly the margin wins: the
// difference of 0.5 and 0.4 is 0.09999999999999998 in floating point, and that
// rounding must not turn a lead of 0.10 into a tie.
func TestClassify_FuzzyLeadOfExactlyTheMarginWins(t *testing.T) {
	cases := []struct {
		name, question, descA, descB string
	}{
		{"0.50 over 0.40", "zebra mango kiwi plum lime",
			"zebra mango quartz walrus", "kiwi plum orbit falcon garnet"},
		{"1.00 over 0.90", "zebra mango kiwi plum lime pear apricot cherry melon grape",
			"zebra mango", "zebra kiwi plum lime pear apricot cherry melon grape walrus"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := ent("entry-a", "fixed", withAnswer("A-answer"), withDescription(c.descA))
			b := ent("entry-b", "fixed", withAnswer("B-answer"), withDescription(c.descB))
			cl, _ := classifyWith([]Entry{a, b}, textQ("#q", c.question), nil)
			if entryID(cl) != "entry-a" || cl.stage != stageFuzzy || cl.abstain != nil {
				t.Fatalf("a lead of exactly 0.10 must resolve to the leader: %+v", cl)
			}
		})
	}
}

func TestLeadsBy_AbsorbsRoundingOfScoreDifferences(t *testing.T) {
	for _, c := range []struct {
		hi, lo, margin float64
		want           bool
	}{{0.5, 0.4, 0.10, true}, {1.0, 0.9, 0.10, true}, {0.95, 0.80, 0.15, true}, {0.5, 0.41, 0.10, false}, {0.95, 0.81, 0.15, false}} {
		if got := leadsBy(c.hi-c.lo, c.margin); got != c.want {
			t.Errorf("leadsBy(%v-%v, %v) = %v, want %v", c.hi, c.lo, c.margin, got, c.want)
		}
	}
}
