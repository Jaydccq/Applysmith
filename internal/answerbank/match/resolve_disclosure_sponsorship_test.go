package match

import (
	"strings"
	"testing"
)

func boolP(b bool) *bool { return &b }

func sponsorEntry() Entry {
	e := blankEntry("sponsorship-now-or-future", KindFixed)
	e.InputTypes = []string{"yes_no", "single_select", "text"}
	return e
}

func withSponsorship(now, future *bool) func(*Request) {
	return func(r *Request) {
		r.Facts.AuthorizedCountries = []string{"US"}
		r.Facts.RequiresSponsorshipNow, r.Facts.RequiresSponsorshipFuture = now, future
	}
}

// Mural (Ashby, Canada): two questions joined by "OR" ask whether either
// holds; a recorded need answers Yes to the plain one. A recorded No says
// nothing of the other.
func TestAnswerBank_Sponsorship_QuestionsJoinedByOr(t *testing.T) {
	m := pickMatcher(sponsorEntry())
	q := radioQ("If yes, is your eligibility to work dependent on sponsorship from your employer? OR Will you now or in the future need sponsorship from your employer in the location you will be working from?", "Yes", "No")
	wantFill(t, matchReq(m, q, withSponsorship(boolP(false), boolP(true))), "Yes", OutcomeFilled)
	if r := matchReq(m, q, withSponsorship(boolP(false), boolP(false))); r.Outcome != OutcomeAbstained || r.Reason != ReasonAmbiguous {
		t.Errorf("a recorded No answers neither question alone: %+v", r)
	}
	// The closing place names where the work is done, not a second tense.
	one := radioQ("Will you now or in the future need sponsorship from your employer in the location you will be working from?", "Yes", "No")
	wantFill(t, matchReq(m, one, withSponsorship(boolP(false), boolP(true))), "Yes", OutcomeFilled)
	wantFill(t, matchReq(m, one, withSponsorship(boolP(false), boolP(false))), "No", OutcomeFilled)
}

func TestAnswerBank_Sponsorship_MissingFactAbstains(t *testing.T) {
	m := pickMatcher(sponsorEntry())
	q := radioQ("Will you now or in the future require sponsorship?", "Yes", "No")
	wantAbstain(t, matchReq(m, q, withSponsorship(boolP(false), nil)), ReasonPlaceholderEmpty, "")
	wantAbstain(t, matchReq(m, q), ReasonPlaceholderEmpty, "")
	// A now-only question needs only the now fact.
	wantFill(t, matchReq(m, radioQ("Do you currently require sponsorship?", "Yes", "No"), withSponsorship(boolP(false), nil)), "No", OutcomeFilled)
	wantAbstain(t, matchReq(m, radioQ("Do you currently require sponsorship?", "Yes", "No"), withSponsorship(nil, boolP(true))), ReasonPlaceholderEmpty, "")
}

func TestAnswerBank_Sponsorship_NowOnlyComplement(t *testing.T) {
	m := pickMatcher(sponsorEntry())
	f := withSponsorship(boolP(false), boolP(true))
	r := matchReq(m, selectQ("Do you require sponsorship before you could begin working?", "Yes, I currently require sponsorship", "No, I do not currently require sponsorship"), f)
	if r.ChosenLabel != "No, I do not currently require sponsorship" {
		t.Fatalf("%+v", r)
	}
	// The check stage gets both facts when a now-only question was answered No.
	q := Question{Selector: "s", Text: "Do you require sponsorship before you could begin working?", Kind: KindRadioGroup, Options: opts("Yes", "No")}
	res := matchReq(m, q, f)
	timing, ok := TimingForReview(Facts{RequiresSponsorshipNow: boolP(false), RequiresSponsorshipFuture: boolP(true)}, q, res)
	if !ok || *timing.Future != true || *timing.Now != false {
		t.Fatalf("%+v %t", timing, ok)
	}
	if _, ok := TimingForReview(Facts{}, Question{Text: "Will you now or in the future require sponsorship?"}, res); ok {
		t.Fatal("a combined question carries no timing")
	}
}

func TestAnswerBank_Sponsorship_OptionContradictingFactVetoed(t *testing.T) {
	m := pickMatcher(sponsorEntry())
	opts2 := []string{"No, I will never require sponsorship", "Yes, I will require sponsorship later"}
	r := matchReq(m, selectQ("Will you now or in the future require sponsorship?", opts2...), withSponsorship(boolP(false), boolP(true)))
	if r.ChosenLabel != "Yes, I will require sponsorship later" {
		t.Fatalf("only the second option can be chosen: %+v", r)
	}
	// The mixed label is not a plain negation.
	if optionNegatesSponsorship("I can work without sponsorship now, but will require sponsorship later") {
		t.Fatal("a mixed label is not vetoed when sponsorship is needed")
	}
	if contradictsSponsorship("I do not require sponsorship", false) {
		t.Fatal("a negated requirement is consistent with no need")
	}
	if !contradictsSponsorship("I will require sponsorship", false) || !contradictsSponsorship("I do not require sponsorship", true) {
		t.Fatal("the opposite of the fact is vetoed")
	}
}

// Riskified (Greenhouse, live): "Are you authorized to work in the U.S.
// indefinitely without sponsorship or transfer including H-1B or OPT?" is No
// for a recorded need: one who needs sponsorship is not authorized without it,
// whatever the transfer. A recorded no-need says nothing of a transfer.
func TestAnswerBank_Sponsorship_AuthorizedIndefinitelyWithoutSponsorshipOrTransfer(t *testing.T) {
	m := pickMatcher(sponsorEntry())
	for _, text := range []string{
		"Are you authorized to work in the U.S. indefinitely without sponsorship or transfer including H-1B or OPT?",
		"Are you authorized to work in the U.S. indefinitely without sponsorship?",
		"Are you authorized to work in the U.S. without sponsorship or transfer?",
	} {
		if r := matchReq(m, selectQ(text, "Yes", "No"), withSponsorship(boolP(false), boolP(true))); r.ChosenLabel != "No" || !isFilled(r) {
			t.Errorf("%q with a future need: %+v", text, r)
		}
	}
	if r := matchReq(m, selectQ("Are you authorized to work in the U.S. without sponsorship or transfer?", "Yes", "No"), withSponsorship(boolP(false), boolP(false))); isFilled(r) {
		t.Errorf("no need says nothing of a transfer: %+v", r)
	}
}

func TestAnswerBank_Sponsorship_DetailTextFromStatus(t *testing.T) {
	desc := ent("sponsorship-type-description", "fixed", withAnswer("H-1B cap-subject sponsorship after OPT"))
	desc.Placeholder = false
	m := pickMatcher(sponsorEntry(), desc)
	q := textQ("d", "Please provide additional details on your sponsorship")
	r := matchReq(m, q, withSponsorship(boolP(false), boolP(true)))
	if r.Answer != "H-1B cap-subject sponsorship after OPT" || r.Outcome != OutcomeFilled {
		t.Fatalf("%+v", r)
	}
	// Not needed: nothing to describe. Unknown: abstain.
	if r := matchReq(m, q, withSponsorship(boolP(false), boolP(false))); r.Outcome != OutcomeAbstained {
		t.Fatalf("%+v", r)
	}
	wantAbstain(t, matchReq(m, q), ReasonPlaceholderEmpty, "")
	// A blank description abstains.
	blank := ent("sponsorship-type-description", "fixed", withAnswer(""))
	wantAbstain(t, matchReq(pickMatcher(sponsorEntry(), blank), q, withSponsorship(boolP(true), boolP(true))), ReasonPlaceholderEmpty, "")
}

// Gamma (Ashby): the need is asked first and the details box follows it ("If
// yes, please share more details below:"); the recorded description answers
// both, and "Please explain." alone is no details box.
func TestAnswerBank_Sponsorship_DetailsAfterTheNeed(t *testing.T) {
	desc := ent("sponsorship-type-description", "fixed", withAnswer("H-1B cap-subject sponsorship after OPT"))
	desc.Placeholder = false
	m := pickMatcher(sponsorEntry(), desc)
	need := withSponsorship(boolP(false), boolP(true))
	q := Question{Selector: "d", Kind: KindText, InputType: InputLongText, Required: true,
		Text: "Do you now, or will you someday, require any visa sponsorship to work in the US? If yes, please share more details below:"}
	if r := matchReq(m, q, need); r.Answer != "H-1B cap-subject sponsorship after OPT" || r.EntryID != "sponsorship-type-description" {
		t.Fatalf("%+v", r)
	}
	// Hive (Lever, live): "If so, please explain in detail" asks the same details.
	q.Text = "Will you now or in the future require sponsorship for employment? If so, please explain in detail (e.g., OPT with STEM extension)."
	if r := matchReq(m, q, need); r.Answer != "H-1B cap-subject sponsorship after OPT" || r.EntryID != "sponsorship-type-description" {
		t.Fatalf("explain in detail: %+v", r)
	}
	// Artera (Lever, live): "If Yes, please elaborate below." asks the same details.
	q.Text = "Would you require any form of Sponsorship or Visa? If Yes, please elaborate below."
	if r := matchReq(m, q, need); r.Answer != "H-1B cap-subject sponsorship after OPT" || r.EntryID != "sponsorship-type-description" {
		t.Fatalf("elaborate: %+v", r)
	}
	q.Text = "Will you require sponsorship? Please explain."
	if r := matchReq(m, q, need); r.EntryID == "sponsorship-type-description" && r.Outcome != OutcomeAbstained {
		t.Fatalf("a bare explain is no details box: %+v", r)
	}
}

// Tabs (Ashby): a new sponsorship or the transfer of a held work visa follows
// from the recorded status: a student status is new, an employer's work visa
// a transfer; no need or an unknown status is left.
func TestAnswerBank_Sponsorship_NewOrTransfer(t *testing.T) {
	desc := ent("sponsorship-type-description", "fixed", withAnswer("H-1B cap-subject sponsorship after OPT"))
	desc.Placeholder = false
	m := pickMatcher(sponsorEntry(), desc)
	labels := []string{"New employment visa sponsorship", "Transfer of an existing employment visa"}
	q := radioQ(`If you answered "Yes" to the previous question, please indicate the type of employment visa sponsorship you require:`, labels...)
	status := func(v string) func(*Request) { return func(r *Request) { r.Facts.VisaStatus = v } }
	need := withSponsorship(boolP(false), boolP(true))
	for _, c := range []struct{ status, want string }{{"F-1 OPT", labels[0]}, {"STEM OPT", labels[0]}, {"H-1B", labels[1]}, {"L-1B", labels[1]}} {
		if r := matchReq(m, q, need, status(c.status)); r.ChosenLabel != c.want || r.EntryID != "sponsorship-type-description" {
			t.Fatalf("%s: want %q, got %+v", c.status, c.want, r)
		}
	}
	for name, mods := range map[string][]func(*Request){
		"no need":        {withSponsorship(boolP(false), boolP(false)), status("F-1 OPT")},
		"unknown status": {need, status("")},
		"unknown need":   {status("F-1 OPT")},
	} {
		if r := matchReq(m, q, mods...); r.Outcome != OutcomeAbstained {
			t.Fatalf("%s: left to the applicant, got %+v", name, r)
		}
	}
}

func TestAnswerBank_Sponsorship_StatusPickerOnlyRequirementOption(t *testing.T) {
	e := ent("work-status-ladder", "pick-option", withPreferred("for any employer"), withExamples("What is your work authorization status?"))
	e.AnswerKey, e.Placeholder = "work_authorized", false
	m := pickMatcher(e, sponsorEntry())
	q := "What is your work authorization status?"
	labels := []string{"Authorized to work in the US without sponsorship", "Authorized, I require employer sponsorship", "Not authorized"}
	r := matchReq(m, selectQ(q, labels...), withSponsorship(boolP(true), boolP(true)))
	if r.ChosenLabel != labels[1] || r.Outcome != OutcomeFilledReview {
		t.Fatalf("the only option stating a requirement, in the review band: %+v", r)
	}
	// Two options stating a requirement: not the only one.
	r = matchReq(m, selectQ(q, labels[1], "Authorized, I will need employer sponsorship soon", "Not authorized"), withSponsorship(boolP(true), boolP(true)))
	if r.Outcome != OutcomeAbstained {
		t.Fatalf("%+v", r)
	}
}

func conjunctionQ() Question {
	return radioQ("Do you require visa sponsorship to take this role? Select “Yes” if the position is exclusively on-site, AND you will need employer sponsorship. Select “No” if the role offers a remote option you intend to take, OR you already have authorization.", "Yes", "No")
}

func TestAnswerBank_ConditionalSponsorship_BranchParsing(t *testing.T) {
	others, ok := readConjuncts(conjunctionQ().Text)
	if !ok || len(others) != 1 || !strings.Contains(others[0], "on-site") {
		t.Fatalf("%v %t", others, ok)
	}
	if _, ok := readConjuncts("Do you require sponsorship? Select Yes if you will need sponsorship."); ok {
		t.Fatal("one conjunct is not a conjunction")
	}
	if _, ok := readConjuncts("Select Yes if you are on-site and a resident"); ok {
		t.Fatal("a conjunction without a sponsorship conjunct is not this form")
	}
}

func TestAnswerBank_ConditionalSponsorship_FactAndConjuncts(t *testing.T) {
	m := pickMatcher(sponsorEntry())
	q := conjunctionQ()
	none := withSponsorship(boolP(false), boolP(false))
	need := withSponsorship(boolP(false), boolP(true))
	wantFill(t, matchReq(m, q, none, withPosting("Sampleton, ES")), "No", OutcomeFilled)
	wantFill(t, matchReq(m, q, none, withPosting("Remote")), "No", OutcomeFilled)
	wantFill(t, matchReq(m, q, need, withPosting("Sampleton, ES")), "Yes", OutcomeFilled)
	// Remote or unknown posting: left for the user, terminal.
	for _, mod := range []func(*Request){withPosting("Remote"), withPosting("")} {
		r := matchReq(m, q, need, mod)
		wantAbstain(t, r, ReasonContextPolicy, DetailSponsorshipConjunction)
		if !r.Terminal {
			t.Fatalf("not fallback eligible: %+v", r)
		}
	}
	// An unevaluable conjunct.
	unk := radioQ("Select “Yes” only if you will be based outside the United States and you will need sponsorship. Select “No” otherwise.", "Yes", "No")
	wantAbstain(t, matchReq(m, unk, need, withPosting("Sampleton, ES")), ReasonContextPolicy, DetailSponsorshipConjunction)
	// A posting in another country than the facts describe, even when the need is none.
	wantAbstain(t, matchReq(m, q, none, withPosting("Toronto, Ontario, Canada")), ReasonContextPolicy, DetailSponsorshipConjunction)
	// Missing facts.
	wantAbstain(t, matchReq(m, q, withPosting("Sampleton, ES")), ReasonPlaceholderEmpty, "")
}

func TestAnswerBank_Derived_ThreeOptionSponsorshipPicker(t *testing.T) {
	e := blankEntry("work-auth-and-future-sponsorship", KindPickOption)
	e.InputTypes = []string{"single_select"}
	m := pickMatcher(e)
	labels := []string{
		"I am authorized to work and DO NOT require sponsorship now or in the future",
		"I am authorized to work now and DO require future sponsorship",
		"I am NOT authorized to work and currently require sponsorship",
	}
	q := selectQ("Are you authorized to work in the US and will you require sponsorship?", labels...)
	// A need now does not say the applicant is not authorized (an H-1B holder
	// needs a transfer): that option takes a recorded No, and a recorded Yes
	// or none leaves the question.
	wantAbstain(t, matchReq(m, q, withSponsorship(boolP(true), boolP(true))), ReasonPlaceholderEmpty, "")
	wantFill(t, matchReq(pickMatcher(e, ent("work-auth-us", "fixed", withAnswer("No"))), q, withSponsorship(boolP(true), boolP(true))), labels[2], OutcomeFilled)
	if r := matchReq(pickMatcher(e, ent("work-auth-us", "fixed", withAnswer("Yes"))), q, withSponsorship(boolP(true), boolP(true))); r.Outcome != OutcomeAbstained {
		t.Fatalf("an authorized applicant who needs sponsorship now: %+v", r)
	}
	wantFill(t, matchReq(m, q, withSponsorship(boolP(false), boolP(true))), labels[1], OutcomeFilled)
	wantFill(t, matchReq(m, q, withSponsorship(boolP(false), boolP(false))), labels[0], OutcomeFilled)
	wantAbstain(t, matchReq(m, q), ReasonPlaceholderEmpty, "")
	wantAbstain(t, matchReq(m, q, withSponsorship(boolP(false), nil)), ReasonPlaceholderEmpty, "")
}

// Sphinx Defense asks only "Are you authorized to work lawfully in the United
// States?" and each option leads with the answer to it and then states a
// sponsorship need: the option leading with the recorded authorization whose
// need the facts do not contradict is chosen.
func TestAnswerBank_Sponsorship_OptionsLedByTheAuthorizationAnswer(t *testing.T) {
	q := selectQ("Are you authorized to work lawfully in the United States?",
		"Yes, and I will not require sponsorship for employment visa status now or in the future.",
		"Yes, but I will require sponsorship for employment visa status in the future.",
		"No, and I will require sponsorship for employment visa status in the future.")
	for _, c := range []struct {
		authorized  string
		now, future bool
		want        string
	}{
		{"Yes", true, true, q.Options[1].Label},
		{"Yes", false, true, q.Options[1].Label},
		{"Yes", false, false, q.Options[0].Label},
		{"No", true, true, q.Options[2].Label},
	} {
		m := pickMatcher(sponsorEntry(), ent("work-auth-us", "fixed", withAnswer(c.authorized)))
		r := matchReq(m, q, withSponsorship(boolP(c.now), boolP(c.future)), withPosting("Colorado Springs, CO"))
		if r.Outcome == OutcomeAbstained || r.ChosenLabel != c.want {
			t.Errorf("authorized %s, now %v, future %v: %s %q (%s) %v", c.authorized, c.now, c.future, r.Outcome, r.ChosenLabel, r.Reason, r.Trace)
		}
	}
	// Not authorized and needing no sponsorship: no option states both.
	m := pickMatcher(sponsorEntry(), ent("work-auth-us", "fixed", withAnswer("No")))
	if r := matchReq(m, q, withSponsorship(boolP(false), boolP(false)), withPosting("Colorado Springs, CO")); r.Outcome != OutcomeAbstained {
		t.Errorf("no option fits: %+v", r)
	}
	// Temporal (Ashby, live): the No states the authorization alone; the Yes
	// options are still split by the sponsorship need.
	temporal := radioQ("Are you legally authorized to work in this location?",
		"Yes, and I will not require sponsorship now or in the future",
		"Yes, but I will require sponsorship now or in the future to maintain work authorization",
		"No, I am not currently authorized to work in this location")
	for _, c := range []struct {
		authorized  string
		now, future bool
		want        string
	}{
		{"Yes", true, true, temporal.Options[1].Label},
		{"Yes", false, false, temporal.Options[0].Label},
		{"No", true, true, temporal.Options[2].Label},
	} {
		m := pickMatcher(sponsorEntry(), ent("work-auth-us", "fixed", withAnswer(c.authorized)))
		r := matchReq(m, temporal, withSponsorship(boolP(c.now), boolP(c.future)), withPosting("United States (Remote)"))
		if r.Outcome == OutcomeAbstained || r.ChosenLabel != c.want {
			t.Errorf("Temporal: authorized %s, now %v, future %v: %s %q (%s) %v", c.authorized, c.now, c.future, r.Outcome, r.ChosenLabel, r.Reason, r.Trace)
		}
	}
	// A Yes that states no sponsorship need is no split Yes.
	bare := radioQ("Are you legally authorized to work in this location?", "Yes", "Yes, but I will require sponsorship in the future", "No")
	m = pickMatcher(sponsorEntry(), ent("work-auth-us", "fixed", withAnswer("Yes")))
	if r := matchReq(m, bare, withSponsorship(boolP(false), boolP(false)), withPosting("United States (Remote)")); r.ChosenLabel == "Yes" && traceHas(r, "the options lead with the authorization answer") {
		t.Errorf("a bare Yes is not answer-led: %+v", r)
	}
}

// GitLab: a Yes split by visa ("No" and "Yes, <visa>" options) arrives as a
// single select. The shipped entry takes Yes/No controls only; it takes this
// one too, a recorded No picks the lone No, and a recorded need is the user's
// to place among the visas unless the recorded status names one of them.
func TestAnswerBank_Sponsorship_AYesSplitByVisaTakesTheLoneNo(t *testing.T) {
	e := blankEntry("sponsorship-now-or-future", KindFixed)
	e.InputTypes = []string{"yes_no"}
	m := pickMatcher(e)
	split := radioQ("Will you now or in the future require sponsorship for a visa to remain in your current location?",
		"No", "Yes, Netherlands Highly Skilled Migrant Visa", "Yes, EU Blue Card", "Yes, USMCA Professional (TN) Visa (USA)", "Yes, but not one of the visas listed here")
	inUS := func(now, future *bool) func(*Request) {
		return func(r *Request) { withSponsorship(now, future)(r); r.Facts.Address.Country = "US" }
	}
	wantFill(t, matchReq(m, split, inUS(boolP(false), boolP(false))), "No", OutcomeFilled)
	if r := matchReq(m, split, inUS(boolP(false), boolP(true))); r.Outcome != OutcomeAbstained || r.Reason != ReasonAmbiguous {
		t.Fatalf("a recorded need: %s %q (%s)", r.Outcome, r.ChosenLabel, r.Reason)
	}
	// A recorded status that one Yes names places the need among the visas
	// (GitLab's list also offers "Yes, F-1 Visa OPT (USA)"), in the review band.
	withOPT := radioQ(split.Text, "No", "Yes, Netherlands Highly Skilled Migrant Visa", "Yes, EU Blue Card",
		"Yes, USMCA Professional (TN) Visa (USA)", "Yes, F-1 Visa OPT (USA)", "Yes, but not one of the visas listed here")
	status := func(v string) func(*Request) {
		return func(r *Request) { inUS(boolP(false), boolP(true))(r); r.Facts.VisaStatus = v }
	}
	wantFill(t, matchReq(m, withOPT, status("F-1 OPT")), "Yes, F-1 Visa OPT (USA)", OutcomeFilledReview)
	// A status no Yes names stays the user's to place.
	if r := matchReq(m, split, status("F-1 OPT")); r.Outcome != OutcomeAbstained || r.Reason != ReasonAmbiguous {
		t.Fatalf("a status no Yes names: %s %q (%s)", r.Outcome, r.ChosenLabel, r.Reason)
	}
	if r := matchReq(m, withOPT, status("H-1B")); r.Outcome != OutcomeAbstained || r.Reason != ReasonAmbiguous {
		t.Fatalf("an H-1B status: %s %q (%s)", r.Outcome, r.ChosenLabel, r.Reason)
	}
	// Point One: a Yes split into a new visa and a visa transfer. A transfer
	// moves an employer's work visa the applicant holds; a student status holds
	// none, so it needs the new one. No status recorded stays the user's.
	kinds := radioQ("Visa Sponsorship", "Yes (new visa)", "Yes (visa transfer)", "No")
	wantFill(t, matchReq(m, kinds, status("F-1 OPT")), "Yes (new visa)", OutcomeFilledReview)
	wantFill(t, matchReq(m, kinds, status("F-1 STEM OPT")), "Yes (new visa)", OutcomeFilledReview)
	wantFill(t, matchReq(m, kinds, status("H-1B")), "Yes (visa transfer)", OutcomeFilledReview)
	if r := matchReq(m, kinds, status("")); r.Outcome != OutcomeAbstained || r.Reason != ReasonAmbiguous {
		t.Fatalf("no status over new or transfer: %s %q (%s)", r.Outcome, r.ChosenLabel, r.Reason)
	}
	// Edra: the same split, each Yes saying who needs it first.
	edra := radioQ("Do you now, or will you in the future require visa sponsorship to work in the country in which you are applying? Visa sponsorship is considered on a case-by-case basis and may be available for some roles",
		"No", "Yes, I will need to transfer an existing work visa", "Yes, I will require a new visa sponsorship")
	wantFill(t, matchReq(m, edra, status("F-1 OPT")), "Yes, I will require a new visa sponsorship", OutcomeFilledReview)
	wantFill(t, matchReq(m, edra, status("H-1B")), "Yes, I will need to transfer an existing work visa", OutcomeFilledReview)
	// CSC Generation: the H-1B named in each Yes.
	csc := radioQ("Will you now, or in the future, require sponsorship for employment visa status (e.g. H-1B visa status)?",
		"Yes, I would need an H1b transfer", "Yes, I would need full H1b sponsorship", "No")
	wantFill(t, matchReq(m, csc, status("F-1 OPT")), "Yes, I would need full H1b sponsorship", OutcomeFilledReview)
	wantFill(t, matchReq(m, csc, status("H-1B")), "Yes, I would need an H1b transfer", OutcomeFilledReview)
	// Two No options are no split Yes: the shape stays the single select's.
	two := radioQ("Will you now or in the future require sponsorship?", "No", "No, but later", "Yes, H-1B", "Yes, other")
	if r := matchReq(m, two, withSponsorship(boolP(false), boolP(false))); r.Outcome != OutcomeAbstained || r.Reason != ReasonInputMismatch {
		t.Fatalf("two No options: %s %q (%s)", r.Outcome, r.ChosenLabel, r.Reason)
	}
	// Truveta (Greenhouse, owner workspace): the same split drawn as a group of
	// boxes beside a catch-all "Other". One box is ticked, as one option is
	// chosen.
	boxes := Question{Selector: "s", Kind: KindCheckbox, InputType: InputCheckbox, Required: true,
		Text:    "Do you now OR in the future require visa sponsorship to continue working in the US?",
		Options: opts("Yes, I am on an F1 Visa/CPT/OPT", "Yes, I am on an H1B Visa", "No, I do not require sponsorship either now OR in the future", "Other")}
	wantFill(t, matchReq(m, boxes, status("F-1 OPT")), "Yes, I am on an F1 Visa/CPT/OPT", OutcomeFilledReview)
	wantFill(t, matchReq(m, boxes, inUS(boolP(false), boolP(false))), "No, I do not require sponsorship either now OR in the future", OutcomeFilledReview)
}
