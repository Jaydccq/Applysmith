package match

import (
	"strconv"
	"strings"
	"testing"
)

// disclosureEntry is a finished yes/no entry the user maintains, reached by its
// verbatim example.
func disclosureEntry(id, answer string, examples ...string) Entry {
	e := ent(id, "fixed", withAnswer(answer), withExamples(examples...))
	e.InputTypes = []string{"yes_no", "text", "single_select"}
	e.Placeholder = false
	return e
}

func matchAll(m *Matcher, qs ...Question) []Result {
	for i := range qs {
		qs[i].Selector = "q" + strconv.Itoa(i)
	}
	return runPasses(m, qs...)
}

const (
	employedQ  = "Have you ever been employed by Example Corp?"
	combinedQ  = "Have you ever been employed by Example Corp? If yes, please list the dates."
	relativesQ = "Do any of your relatives work for a competitor of Example Corp?"
)

func TestAnswerBank_CombinedDisclosure_OnlyMaintainedNo(t *testing.T) {
	r := matchReq(pickMatcher(disclosureEntry("employed-by-company", "No", employedQ)), textQ("c", combinedQ))
	if r.Answer != "No" || r.Source != SourceConditional || r.EntryID != "employed-by-company" || r.Outcome != OutcomeFilled {
		t.Fatalf("the box receives the primary's No with its entry: %+v", r)
	}
	if !traceHas(r, "yes-only details do not apply") {
		t.Fatalf("%+v", r.Trace)
	}
	// A Yes, or any other answer, leaves the detail to the user.
	r = matchReq(pickMatcher(disclosureEntry("employed-by-company", "Yes", employedQ)), textQ("c", combinedQ))
	wantAbstain(t, r, ReasonNoEntry, "")
	// The follow-up inherits a review band.
	weak := disclosureEntry("employed-by-company", "No", employedQ)
	weak.NeedsReview = true
	r = matchReq(pickMatcher(weak), textQ("c", combinedQ))
	if r.Answer != "No" || r.Outcome != OutcomeFilledReview || r.Confidence != FuzzyConfidenceCap {
		t.Fatalf("inherits the confidence and outcome of the answer it depends on: %+v", r)
	}
}

func TestAnswerBank_CombinedDisclosure_FuzzyPrimaryAbstains(t *testing.T) {
	// The primary is reachable only by fuzzy match.
	e := ent("worked-for-company", "fixed", withAnswer("No"), notCurated(), withDescription("employed by example corp previously"))
	e.InputTypes = []string{"yes_no", "text"}
	r := matchReq(pickMatcher(e), textQ("c", combinedQ))
	wantAbstain(t, r, ReasonNoEntry, "")
	// Unclassified.
	wantAbstain(t, matchReq(pickMatcher(), textQ("c", combinedQ)), ReasonNoEntry, "")
	// A blank predicate entry.
	blank := blankEntry("employed-by-company", KindFixed)
	blank.Examples, blank.InputTypes = []string{employedQ}, []string{"yes_no", "text"}
	wantAbstain(t, matchReq(pickMatcher(blank), textQ("c", combinedQ)), ReasonPlaceholderEmpty, "")
}

const narrowedQ = "B. Are you a current employee of a national government who is either: (i) participating in a matter involving Example Corp or (ii) responsible for a contract?"

func TestAnswerBank_NarrowedDisclosure_ShapeRecognised(t *testing.T) {
	m := pickMatcher(disclosureEntry("gov-employee", "No", "Are you a current employee of a national government?"))
	for _, q := range []Question{radioQ(narrowedQ, "Yes", "No"), textQ("n", narrowedQ)} {
		if r := matchReq(m, q); r.Source != SourceConditional || !strings.HasPrefix(resultLabel(r), "No") {
			t.Errorf("%v: %+v", q.Kind, r)
		}
	}
	if r := matchReq(m, radioQ("Are you a current employee of a national government?", "Yes", "No")); r.Source == SourceConditional {
		t.Errorf("the primary itself is not narrowed: %+v", r)
	}
}

func TestAnswerBank_NarrowedDisclosure_YesNeverCarriesOver(t *testing.T) {
	m := pickMatcher(disclosureEntry("gov-employee", "Yes", "Are you a current employee of a national government?"))
	r := matchReq(m, radioQ(narrowedQ, "Yes", "No"))
	wantAbstain(t, r, ReasonNoEntry, "")
	if !traceHas(r, "left to the user") {
		t.Fatalf("%+v", r.Trace)
	}
}

func TestAnswerBank_Literal_IndicateNo(t *testing.T) {
	q := textQ("c", "Do any of your relatives work for a competitor of Example Corp? If YES, please explain. If NO, please indicate NO.")
	r := matchReq(pickMatcher(disclosureEntry("relatives", "No", relativesQ)), q)
	if r.Answer != "NO" || r.Source != SourceConditional || r.Outcome != OutcomeFilled {
		t.Fatalf("%+v", r)
	}
	// Without a recorded answer nothing is typed.
	blank := blankEntry("relatives", KindFixed)
	blank.Examples, blank.InputTypes = []string{relativesQ}, []string{"yes_no", "text"}
	wantAbstain(t, matchReq(pickMatcher(blank), q), ReasonPlaceholderEmpty, "")
	wantAbstain(t, matchReq(pickMatcher(), q), ReasonNoEntry, "")
	// A maintained Yes is not an indicate-NO.
	wantAbstain(t, matchReq(pickMatcher(disclosureEntry("relatives", "Yes", relativesQ)), q), ReasonNoEntry, "")
	// It inherits the gate answer's confidence.
	weak := disclosureEntry("relatives", "No", relativesQ)
	weak.NeedsReview = true
	if r := matchReq(pickMatcher(weak), q); r.Answer != "NO" || r.Outcome != OutcomeFilledReview {
		t.Fatalf("%+v", r)
	}
}

func TestAnswerBank_Compose_RefusesSlotShapes(t *testing.T) {
	for _, c := range []struct {
		name string
		f    func(string) bool
		yes  []string
		no   []string
	}{
		{"criminal explanation", isCriminalHistoryExplanation,
			[]string{"If yes, please identify the offense, the date and explain the circumstances", "Describe the nature of the conviction"},
			[]string{"Have you ever been convicted of a felony?", "Please describe your project"}},
		{"conditional detail slot", isConditionalDetailSlot,
			[]string{"If yes, please explain your non-compete agreement", "If you answered yes, give the date"},
			[]string{"Tell us about yourself"}},
		{"third-party contact", isThirdPartyContactRequest,
			[]string{"Please provide three professional references (name, phone, email)", "Emergency contact", "List your references"},
			[]string{"Are you willing to provide references upon request?", "Can you provide references?"}},
		{"leave blank", isLeaveBlankInstruction,
			[]string{"If you're a human, leave this field blank", "Please leave this field blank", "This field is for bots"},
			[]string{"Middle name (leave blank if none)"}},
		{"specify slot", func(s string) bool { _, ok := specifyTrigger(s); return ok },
			[]string{"If Other, please specify:", "If you selected Referral, please specify who:", "Other (please specify)"},
			[]string{"If yes, please specify", "Please specify your preferred start date"}},
		{"conditional detail", isConditionalDetailText,
			[]string{"If YES, please indicate date and position:", "If yes, give their names", "If you answered No above, please explain"},
			[]string{"If yes, please explain your non-compete agreement", "If you could change one thing, tell us what and why", "What is your email?"}},
	} {
		for _, s := range c.yes {
			if !c.f(s) {
				t.Errorf("%s: %q should be recognized", c.name, s)
			}
		}
		for _, s := range c.no {
			if c.f(s) {
				t.Errorf("%s: %q must not be recognized", c.name, s)
			}
		}
	}
}

// followUp matches a Yes/No gate answered by an entry, then a text follow-up.
func followUp(t *testing.T, gateAnswer string, follow Question, extra ...Entry) []Result {
	t.Helper()
	gate := disclosureEntry("example-gate", gateAnswer, "Have you ever worked for Example Corp?")
	return matchAll(pickMatcher(append([]Entry{gate}, extra...)...), radioQ("Have you ever worked for Example Corp?", "Yes", "No"), follow)
}

func TestAnswerBank_CrossQuestion_GateNoFillsNA(t *testing.T) {
	rs := followUp(t, "No", required(textQ("s", "If YES, please indicate date and position:")))
	if rs[1].Answer != "N/A" || rs[1].Source != SourceConditional || rs[1].Outcome != OutcomeFilled || rs[1].Confidence != rs[0].Confidence {
		t.Fatalf("required slot: %+v", rs[1])
	}
	rs = followUp(t, "No", textQ("s", "If YES, please indicate date and position:"))
	if rs[1].Outcome != OutcomeFilled || rs[1].Answer != "" {
		t.Fatalf("optional slot is a blank fill: %+v", rs[1])
	}
	// The slot inherits a review band from its gate.
	weak := disclosureEntry("example-gate", "No", "Have you ever worked for Example Corp?")
	weak.NeedsReview = true
	rs = matchAll(pickMatcher(weak), radioQ("Have you ever worked for Example Corp?", "Yes", "No"), required(textQ("s", "If YES, please indicate date and position:")))
	if rs[1].Outcome != OutcomeFilledReview || rs[1].Confidence != rs[0].Confidence {
		t.Fatalf("%+v", rs[1])
	}
	// A non-follow-up question is never given an N/A.
	rs = followUp(t, "No", required(textQ("s", "What is your email?")))
	if rs[1].Answer == "N/A" {
		t.Fatalf("%+v", rs[1])
	}
	rs = followUp(t, "No", required(textQ("s", "Please provide your full name")))
	if rs[1].Answer == "N/A" {
		t.Fatalf("a name box is not a follow-up slot: %+v", rs[1])
	}
}

func TestAnswerBank_CrossQuestion_GateYesRevokesNA(t *testing.T) {
	// A fill the slot received is revoked, so the user types real data.
	slot := required(textQ("s", "If YES, please indicate date and position:"))
	fill := disclosureEntry("slot-fill", "N/A", "If YES, please indicate date and position:")
	rs := followUp(t, "Yes", slot, fill)
	wantAbstain(t, rs[1], ReasonNoEntry, "")
	// An unknown gate (the question before is not a gate) abstains too.
	rs = matchAll(pickMatcher(fill), textQ("g", "Your nickname"), slot)
	if rs[1].Outcome != OutcomeAbstained {
		t.Fatalf("%+v", rs[1])
	}
	// And an unanswered gate.
	rs = matchAll(pickMatcher(fill), radioQ("Have you ever worked for Example Corp?", "Yes", "No"), slot)
	if rs[1].Outcome != OutcomeAbstained {
		t.Fatalf("%+v", rs[1])
	}
}

// The engine names a bare stem after the question before it. A No gate
// decides that named slot as it decides a bare one; under any other gate the
// answer saved to the whole question stands.
func TestAnswerBank_CrossQuestion_ASlotNamedAfterItsGate(t *testing.T) {
	const named = "Have you ever worked for Example Corp? If YES, please indicate date and position:"
	if rs := followUp(t, "No", required(textQ("s", named))); rs[1].Answer != "N/A" || rs[1].Source != SourceConditional {
		t.Fatalf("under a No gate: %+v", rs[1])
	}
	own := disclosureEntry("example-dates", "2019 to 2021, analyst", named)
	if rs := followUp(t, "Yes", required(textQ("s", named)), own); rs[1].Answer != "2019 to 2021, analyst" {
		t.Fatalf("under a Yes gate: %+v", rs[1])
	}
}

// GHX (Greenhouse, live): a box that states its own referral condition
// follows only a question about a referral. Under a salary question answered
// Yes its own answer stands; under a referral answered Yes the name is the
// user's.
func TestAnswerBank_CrossQuestion_AReferralSlotTakesOnlyAReferralGate(t *testing.T) {
	const ghx = "If you were referred to this position by a current GHX employee, or other individual, please provide the person's name."
	slot := required(textQ("s", ghx))
	fill := disclosureEntry("slot-fill", "N/A", ghx)
	if rs := followUp(t, "Yes", slot, fill); rs[1].Answer != "N/A" || rs[1].Outcome != OutcomeFilled {
		t.Fatalf("under an unrelated gate: %+v", rs[1])
	}
	referred := disclosureEntry("referred-gate", "Yes", "Were you referred by a current employee?")
	rs := matchAll(pickMatcher(referred, fill), radioQ("Were you referred by a current employee?", "Yes", "No"), slot)
	wantAbstain(t, rs[1], ReasonNoEntry, "")
}

func TestAnswerBank_CrossQuestion_VisaFollowUpDefers(t *testing.T) {
	status := ent("current-immigration-status", "pick-option", withPreferred("F-1 OPT", "Other"))
	status.Placeholder = false
	slot := required(textQ("s", "If yes, please identify the type of work visa you hold and its expiration date"))
	rs := followUp(t, "Yes", slot, status)
	if rs[1].Answer != "F-1 OPT" || rs[1].EntryID != "current-immigration-status" || rs[1].Source != SourceConditional {
		t.Fatalf("%+v", rs[1])
	}
	// Blank entry: placeholder-empty.
	rs = followUp(t, "Yes", slot, blankEntry("current-immigration-status", KindPickOption))
	wantAbstain(t, rs[1], ReasonPlaceholderEmpty, "")
	// A different conditional-detail question under a Yes gate stays an abstain.
	rs = followUp(t, "Yes", required(textQ("s", "If yes, please indicate date and position")), status)
	if rs[1].Outcome != OutcomeAbstained {
		t.Fatalf("%+v", rs[1])
	}
}

func TestAnswerBank_SponsorshipFollowUp_YesGateUsesDescription(t *testing.T) {
	desc := ent("sponsorship-type-description", "fixed", withAnswer("H-1B after OPT"))
	desc.Placeholder = false
	m := pickMatcher(sponsorEntry(), desc)
	gateQ := radioQ("Will you now or in the future require sponsorship?", "Yes", "No")
	slot := required(textQ("s", "If yes, please explain:"))
	// Lithic (Greenhouse, live): "If so, please specify the type of sponsorship required."
	// Belvedere (Lever, live) asks the type with an example list.
	for _, text := range []string{slot.Text, "If so, please specify the type of sponsorship required.", "If yes, what type of sponsorship will you require? (Ex. F-1, H1-B, OPT, etc.)"} {
		s := slot
		s.Text = text
		resp, _ := m.Match(Request{Host: "h", Questions: []Question{{Selector: "g", Text: gateQ.Text, Kind: gateQ.Kind, Options: gateQ.Options}, s},
			Facts: Facts{AuthorizedCountries: []string{"US"}, RequiresSponsorshipNow: boolP(false), RequiresSponsorshipFuture: boolP(true)}})
		if resp.Results[1].Answer != "H-1B after OPT" || resp.Results[1].EntryID != "sponsorship-type-description" || resp.Results[1].Source != SourceConditional {
			t.Fatalf("%q: %+v", text, resp.Results[1])
		}
	}
	// No gate: the generic rule gives the N/A. No description entry: the box is
	// left to the user.
	resp, _ := m.Match(Request{Host: "h", Questions: []Question{{Selector: "g", Text: gateQ.Text, Kind: gateQ.Kind, Options: gateQ.Options}, slot},
		Facts: Facts{AuthorizedCountries: []string{"US"}, RequiresSponsorshipNow: boolP(false), RequiresSponsorshipFuture: boolP(false)}})
	if resp.Results[1].Answer != "N/A" {
		t.Fatalf("%+v", resp.Results[1])
	}
	resp, _ = pickMatcher(sponsorEntry()).Match(Request{Host: "h", Questions: []Question{{Selector: "g", Text: gateQ.Text, Kind: gateQ.Kind, Options: gateQ.Options}, slot},
		Facts: Facts{AuthorizedCountries: []string{"US"}, RequiresSponsorshipNow: boolP(true), RequiresSponsorshipFuture: boolP(true)}})
	if resp.Results[1].Outcome != OutcomeAbstained {
		t.Fatalf("%+v", resp.Results[1])
	}
}

func TestAnswerBank_SiblingConditional_WorkAuthYesNoAllPriors(t *testing.T) {
	auth := ent("work-auth-us", "fixed", withAnswer("Yes"), withExamples("Are you authorized to work in the US?"))
	auth.InputTypes, auth.Placeholder, auth.AnswerKey = []string{"yes_no"}, false, "work_authorized"
	spouse := ent("work-auth-based-on-visa-spouse", "fixed", withAnswer("No"), withExamples("Is your work authorization based on your spouse's visa?"))
	spouse.InputTypes, spouse.Placeholder = []string{"yes_no"}, false
	m := pickMatcher(auth, spouse, sponsorEntry())
	cond := radioQ("Did you answer 'no' to question 1 and/or 'yes' to question 2 or 3?", "Yes", "No")
	qs := func() []Question {
		return []Question{
			radioQ("Are you authorized to work in the US?", "Yes", "No"),
			radioQ("Is your work authorization based on your spouse's visa?", "Yes", "No"),
			radioQ("Will you now or in the future require sponsorship?", "Yes", "No"),
			cond,
		}
	}
	run := func(f func(*Request), qs []Question) []Result {
		req := Request{Host: "h", Questions: qs}
		f(&req)
		for i := range req.Questions {
			req.Questions[i].Selector = "q" + strconv.Itoa(i)
		}
		resp, err := m.Match(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp.Results
	}
	// All three present, none triggers: No.
	rs := run(withSponsorship(boolP(false), boolP(false)), qs())
	if rs[3].Answer != "No" || rs[3].ChosenLabel != "No" || rs[3].AnswerKey != "work_auth_conditional" || rs[3].Source != SourceConditional {
		t.Fatalf("%+v", rs[3])
	}
	// Sponsorship Yes triggers.
	rs = run(withSponsorship(boolP(false), boolP(true)), qs())
	if rs[3].Answer != "Yes" {
		t.Fatalf("%+v", rs[3])
	}
	// A missing prior could have been a trigger: no "No".
	rs = run(withSponsorship(boolP(false), boolP(false)), []Question{qs()[0], cond})
	if rs[1].Answer == "No" {
		t.Fatalf("a missing prior could have triggered: %+v", rs[1])
	}
	// No prior present: left alone.
	rs = run(withSponsorship(boolP(false), boolP(false)), []Question{cond})
	if rs[0].Answer != "" {
		t.Fatalf("%+v", rs[0])
	}
}

func hearBank() []Entry {
	src := ent("referral-source", "pick-option", withPreferred("Job board"), withExamples("How did you hear about us?"))
	src.Placeholder = false
	spec := ent("referral-source-specify", "pick-option", withPreferred("LinkedIn"))
	spec.Placeholder = false
	return []Entry{src, spec}
}

func hearPicker(labels ...string) Question {
	return Question{Text: "How did you hear about us?", Kind: KindSelect, InputType: InputSingleSelect, Options: opts(labels...)}
}

func TestAnswerBank_SpecifySlot_ParentResolution(t *testing.T) {
	m := pickMatcher(hearBank()...)
	slot := textQ("s", "If Other, please specify:")
	// The trigger option is "rendered" when a label carries the word other and is short.
	rs := matchAll(m, hearPicker("Job board", "Other"), slot)
	if rs[1].Outcome != OutcomeFilled || rs[1].Answer != "" {
		t.Fatalf("parent selected Job board: %+v", rs[1])
	}
	// A closed list (no options yet) counts by its label.
	if rs = matchAll(m, hearPicker(), slot); rs[1].Outcome != OutcomeFilled || rs[1].Answer != "" {
		t.Fatalf("closed list: %+v", rs[1])
	}
	// A block of slots after one picker shares its parent.
	rs = matchAll(m, hearPicker("Job board", "Referral", "Other"), textQ("a", "If you selected Referral, please specify who:"), textQ("b", "If Other, please specify:"))
	if rs[1].Outcome != OutcomeFilled || rs[1].Answer != "" || rs[2].Outcome != OutcomeFilled || rs[2].Answer != "" {
		t.Fatalf("%+v %+v", rs[1], rs[2])
	}
}

func TestAnswerBank_SpecifySlot_DecisionTable(t *testing.T) {
	m := pickMatcher(hearBank()...)
	slot := textQ("s", "If Other, please specify:")
	// No licensed parent, bare Other: no change (the question may be its own parent).
	rs := matchAll(m, slot)
	if rs[0].Source == SourceConditional {
		t.Fatalf("%+v", rs[0])
	}
	// A named trigger with no parent abstains.
	named := textQ("s", "If you selected Referral, please specify who:")
	if rs = matchAll(m, named); rs[0].Source == SourceConditional {
		t.Fatalf("%+v", rs[0])
	}
	// The parent is unanswered: abstain, even if a lane wrote the slot.
	parent := Question{Text: "Which office are you applying to?", Kind: KindSelect, InputType: InputSingleSelect, Options: opts("Alpha", "Beta", "Other")}
	pm := pickMatcher(append(hearBank(), disclosureEntry("slot-fill", "LinkedIn", "If Other, please specify:"))...)
	rs = matchAll(pm, parent, slot)
	if rs[1].Outcome != OutcomeAbstained {
		t.Fatalf("an unanswered parent cannot license the slot: %+v", rs[1])
	}
	// A source list without the answer takes its Other, and the slot names the answer.
	rs = matchAll(m, hearPicker("Alpha", "Beta", "Other"), slot)
	if rs[0].ChosenLabel != "Other" || rs[1].Source != SourceConditional || rs[1].Answer != "LinkedIn" {
		t.Fatalf("%+v %+v", rs[0], rs[1])
	}
}

func TestAnswerBank_SpecifySlot_ParentOtherOptionLeavesBlank(t *testing.T) {
	rs := matchAll(pickMatcher(hearBank()...), hearPicker("Job board", "Other"), textQ("s", "If Other, please specify:"))
	r := rs[1]
	if r.Outcome != OutcomeFilled || r.Answer != "" || r.Source != SourceConditional || !traceHas(r, "leave blank") {
		t.Fatalf("%+v", r)
	}
}

// iCapital (Greenhouse, live): the slot says what to type for the other choices.
func TestAnswerBank_SpecifySlot_TheSlotsOwnAnswerForTheOtherChoices(t *testing.T) {
	const ask = `If "Employee Referral" was selected for "How did you hear about this job?" please type the full name of the referring employee.`
	icap := textQ("s", ask+` If not, please type in "N/A".`)
	icap.Required = true
	rs := matchAll(pickMatcher(hearBank()...), hearPicker("Job board", "Employee Referral", "Other"), icap)
	if r := rs[1]; r.Outcome != OutcomeFilled || r.Answer != "N/A" || r.Source != SourceConditional {
		t.Fatalf("not referred: %+v", r)
	}
	refSrc := ent("referral-source", "pick-option", withPreferred("Employee Referral"), withExamples("How did you hear about us?"))
	refSrc.Placeholder = false
	if r := matchAll(pickMatcher(refSrc), hearPicker("Job board", "Employee Referral"), icap)[1]; r.Answer == "N/A" {
		t.Fatalf("referred: %+v", r)
	}
	if r := matchAll(pickMatcher(hearBank()...), hearPicker("Job board", "Employee Referral"), textQ("s", ask))[1]; r.Outcome != OutcomeFilled || r.Answer != "" {
		t.Fatalf("no instruction: %+v", r)
	}
}

func TestAnswerBank_SpecifySlot_OtherUsesReferralSourceSpecify(t *testing.T) {
	// The parent selected Other: answered from referral-source-specify.
	src := ent("referral-source", "pick-option", withPreferred("Other"), withExamples("How did you hear about us?"))
	src.Placeholder = false
	spec := ent("referral-source-specify", "pick-option", withPreferred("LinkedIn"))
	spec.Placeholder = false
	rs := matchAll(pickMatcher(src, spec), hearPicker("Job board", "Other"), textQ("s", "If Other, please specify:"))
	if rs[1].Answer != "LinkedIn" || rs[1].EntryID != "referral-source-specify" || rs[1].Source != SourceConditional {
		t.Fatalf("%+v", rs[1])
	}
	// A blank entry abstains.
	blank := blankEntry("referral-source-specify", KindPickOption)
	rs = matchAll(pickMatcher(src, blank), hearPicker("Job board", "Other"), textQ("s", "If Other, please specify:"))
	wantAbstain(t, rs[1], ReasonPlaceholderEmpty, "")
	// A named trigger the parent selected has no bank source.
	refSrc := ent("referral-source", "pick-option", withPreferred("Referral"), withExamples("How did you hear about us?"))
	refSrc.Placeholder = false
	rs = matchAll(pickMatcher(refSrc, spec), hearPicker("Job board", "Referral"), textQ("s", "If you selected Referral, please specify who:"))
	if rs[1].Outcome != OutcomeAbstained {
		t.Fatalf("%+v", rs[1])
	}
}

// An Other slot takes the detail of the parent's own family: a citizenship
// list's Other is the recorded immigration status, never the referral detail
// (a short "Other" once got "LinkedIn"); a parent of no known family abstains.
// Epirus (Greenhouse, live): "If (f) Other, please explain:" under a lettered list.
func TestAnswerBank_SpecifySlot_OtherTakesTheParentFamilysDetail(t *testing.T) {
	itar := ent("us-person-itar-ear", "fixed", withAnswer("No"))
	itar.Placeholder, itar.Category = false, "work-authorization"
	status := ent("current-immigration-status", "pick-option", withPreferred("F-1 OPT", "Other"))
	status.Placeholder, status.Category = false, "work-authorization"
	spec := ent("referral-source-specify", "pick-option", withPreferred("LinkedIn"))
	spec.Placeholder = false
	m := pickMatcher(itar, status, spec)
	cit := Question{Text: "Citizenship Status", Kind: KindSelect, InputType: InputSingleSelect}
	short := cit
	short.Options = opts("U.S. Citizen", "Permanent Resident", "Other")
	lettered := cit
	lettered.Options = opts("(a) U.S. citizen or national of the United States", "(b) U.S. lawful permanent resident", "(f) Other (please explain)")
	for _, c := range []struct {
		parent Question
		slot   string
	}{
		{short, "If Other, please specify:"},
		{lettered, "If (f) Other, please explain:"},
	} {
		rs := matchAll(m, c.parent, textQ("s", c.slot))
		if !strings.Contains(resultLabel(rs[0]), "Other") || rs[1].Answer != "F-1 OPT" || rs[1].EntryID != "current-immigration-status" || rs[1].Source != SourceConditional {
			t.Errorf("%q: %+v / %+v", c.slot, rs[0], rs[1])
		}
	}
	// An Other picked by an entry of no known family leaves the slot to the user.
	office := disclosureEntry("office-pick", "Other", "Which office are you applying to?")
	rs := matchAll(pickMatcher(office, spec), Question{Text: "Which office are you applying to?", Kind: KindSelect, InputType: InputSingleSelect, Options: opts("Alpha", "Beta", "Other")}, textQ("s", "If Other, please specify:"))
	if rs[1].Answer == "LinkedIn" || rs[1].Outcome != OutcomeAbstained {
		t.Fatalf("an unknown family: %+v / %+v", rs[0], rs[1])
	}
}

func TestAnswerBank_SpecifySlot_DerivedValueDropped(t *testing.T) {
	// A named slot re-sent alone: whatever a generic route derived is dropped.
	fill := disclosureEntry("referral-source-specify", "LinkedIn", "If you selected Referral, please specify who:")
	rs := matchAll(pickMatcher(fill), textQ("s", "If you selected Referral, please specify who:"))
	if rs[0].Outcome != OutcomeAbstained {
		t.Fatalf("%+v", rs[0])
	}
	// A bare yes/no written into the slot is dropped.
	yes := disclosureEntry("yes-fill", "Yes", "If Other, please specify:")
	rs = matchAll(pickMatcher(yes), textQ("s", "If Other, please specify:"))
	if rs[0].Outcome != OutcomeAbstained {
		t.Fatalf("%+v", rs[0])
	}
}

// Tanium (Greenhouse, live): a required "If you selected Employee Referral,
// Student Organization, University fair or networking event, or Other, please
// specify below" under a source answered LinkedIn does not apply: it takes
// "N/A", as a required slot under a No gate does. A selection the slot names
// is left to the user.
func TestAnswerBank_SpecifySlot_ARequiredSlotThatDoesNotApply(t *testing.T) {
	slot := textQ("s", "If you selected Employee Referral, Student Organization, University fair or networking event, or Other, please specify below (e.g., name of employee, organization, or event).")
	slot.Required = true
	rs := matchAll(pickMatcher(hearBank()...), hearPicker("Job board", "Indeed", "Employee Referral", "Other"), slot)
	if r := rs[1]; rs[0].ChosenLabel != "Job board" || r.Outcome != OutcomeFilled || r.Answer != "N/A" || r.Source != SourceConditional {
		t.Fatalf("not selected: %+v %+v", rs[0], r)
	}
	refSrc := ent("referral-source", "pick-option", withPreferred("Employee Referral"), withExamples("How did you hear about us?"))
	refSrc.Placeholder = false
	if r := matchAll(pickMatcher(refSrc), hearPicker("Job board", "Employee Referral", "Other"), slot)[1]; r.Answer == "N/A" || r.Outcome != OutcomeAbstained {
		t.Fatalf("a selection the slot names: %+v", r)
	}
	optional := slot
	optional.Required = false
	if r := matchAll(pickMatcher(hearBank()...), hearPicker("Job board", "Other"), optional)[1]; r.Answer != "" {
		t.Fatalf("optional: %+v", r)
	}
}

// Oklo (Greenhouse, live): the page puts the required "If you selected
// "Other," please specify:" before the source list it qualifies, after a pay
// box. With no choice group before it that offers "Other", the list right
// after it is its parent: answered LinkedIn, the slot does not apply and takes
// "N/A". A following list that offers no "Other" is no parent.
func TestAnswerBank_SpecifySlot_BeforeTheListItQualifies(t *testing.T) {
	pay := textQ("pay", "What is your desired base compensation for this role?")
	slot := textQ("s", `If you selected "Other," please specify:`)
	slot.Required = true
	rs := matchAll(pickMatcher(hearBank()...), pay, slot, hearPicker("Job board", "Indeed", "Other"))
	if r := rs[1]; rs[2].ChosenLabel != "Job board" || r.Outcome != OutcomeFilled || r.Answer != "N/A" || r.Source != SourceConditional {
		t.Fatalf("slot before its list: %+v %+v", rs[2], r)
	}
	if r := matchAll(pickMatcher(hearBank()...), pay, slot, hearPicker("Job board", "Indeed"))[1]; r.Outcome == OutcomeFilled {
		t.Fatalf("a following list without Other: %+v", r)
	}
}
