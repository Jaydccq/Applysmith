package match

import (
	"slices"
	"strings"
	"testing"
)

func boundEntry(id, predicate, ifTrue, ifFalse string, pol *RelocationPolicy) Entry {
	e := blankEntry(id, KindFixed)
	e.InputTypes = []string{"yes_no", "single_select"}
	e.LocationPolicy = &LocationPolicy{Predicate: predicate, IfTrue: ifTrue, IfFalse: ifFalse, Resolved: pol}
	return e
}

func relocEntry(pol *RelocationPolicy) Entry {
	return boundEntry("willing-to-relocate", PredicateWillingToWorkAt, "Yes", "No", pol)
}

func requiresEntry(pol *RelocationPolicy) Entry {
	return boundEntry("requires-relocation", PredicateRequiresRelocation, "Yes", "No", pol)
}

func presenceEntry(pol *RelocationPolicy) Entry {
	return boundEntry("based-in-named-metro", PredicateCurrentlyAt, "Yes", "No", pol)
}

func withPosting(location string) func(*Request) {
	return func(r *Request) { r.Posting.Location = location }
}

func TestAnswerBank_Location_UnknownPredicateIgnored(t *testing.T) {
	e := boundEntry("willing-to-relocate", "teleports_to", "Yes", "No", alexPolicy(RelocationOpen, nil, nil))
	e.Answer, e.Placeholder = "Yes", false
	// An unknown predicate gives no verdict: the entry answers from its own body.
	wantFill(t, matchReq(pickMatcher(e), radioQ("Are you willing to relocate?", "Yes", "No")), "Yes", OutcomeFilled)
}

func TestAnswerBank_Location_PolicyAsVeto(t *testing.T) {
	// office-attendance-ok binds the predicate as a pure veto: no if_true.
	pol := alexPolicy(RelocationClosed, []string{"Boston"}, []string{"Seattle"})
	e := boundEntry("office-attendance-ok", PredicateWillingToWorkAt, "", "No", pol)
	e.Answer, e.Placeholder = "Yes", false
	m := pickMatcher(e)
	r := matchReq(m, radioQ("Are you able to work from our Seattle office?", "Yes", "No"))
	wantFill(t, r, "No", OutcomeFilled)
	// An acceptable place falls through to the entry's own answer.
	r = matchReq(m, radioQ("Are you able to work from our Boston office?", "Yes", "No"))
	wantFill(t, r, "Yes", OutcomeFilled)
}

func TestAnswerBank_Location_ConjunctionPresenceAndOnsite(t *testing.T) {
	m := pickMatcher(presenceEntry(alexPolicy(RelocationOpen, nil, nil)))
	wantFill(t, matchReq(m, radioQ("Are you currently in Seattle and willing to work on-site?", "Yes", "No")), "No", OutcomeFilled)
	wantFill(t, matchReq(m, radioQ("Are you currently in Sampleton and willing to work on-site?", "Yes", "No")), "Yes", OutcomeFilled)
}

func TestAnswerBank_Location_RequiresRelocationRoute(t *testing.T) {
	m := pickMatcher(requiresEntry(alexPolicy(RelocationClosed, []string{"Example City"}, nil)))
	q := radioQ("Will you require relocation?", "Yes", "No")
	// Different city of the home state, policy closed and not listing it: the willingness verdict.
	wantFill(t, matchReq(m, q, withPosting("Othertown, ES")), "No", OutcomeFilled)
	wantFill(t, matchReq(m, q, withPosting("Example City, EC")), "Yes", OutcomeFilled)
	wantFill(t, matchReq(m, q, withPosting("Remote")), "No", OutcomeFilled)
	wantFill(t, matchReq(m, q, withPosting("Sampleton, ES")), "No", OutcomeFilled)
}

func TestAnswerBank_Location_BranchPickShapes(t *testing.T) {
	m := pickMatcher(relocEntry(alexPolicy(RelocationOpen, nil, nil)))
	q := "Are you based in Seattle or open to relocating?"
	// A city picker is not a branch set.
	r := matchReq(m, selectQ(q, "Seattle", "Boston", "Denver"))
	if traceHas(r, "branch pick") {
		t.Fatalf("a city picker is not a branch set: %+v", r)
	}
	// Two affirmative options are not a branch set either.
	r = matchReq(m, selectQ(q, "Seattle based", "Yes", "Open to relocating"))
	if traceHas(r, "branch pick") {
		t.Fatalf("two affirmatives are not a branch set: %+v", r)
	}
}

func TestAnswerBank_Location_BranchPickDecisionOrder(t *testing.T) {
	q := "If you're not currently based in Seattle, are you open to relocating to Seattle?"
	labels := []string{"Yes", "No", "I'm already in Seattle"}
	open := pickMatcher(relocEntry(alexPolicy(RelocationOpen, nil, nil)))
	wantFill(t, matchReq(open, selectQ(q, labels...)), "Yes", OutcomeFilled)
	closed := pickMatcher(relocEntry(alexPolicy(RelocationClosed, nil, nil)))
	wantFill(t, matchReq(closed, selectQ(q, labels...)), "No", OutcomeFilled)
	excluded := pickMatcher(relocEntry(alexPolicy(RelocationOpen, nil, []string{"Seattle"})))
	wantFill(t, matchReq(excluded, selectQ(q, labels...)), "No", OutcomeFilled)
	// The applicant's home is the named place: the located option.
	home := alexPolicy(RelocationOpen, nil, nil)
	home.Home.City = "Seattle"
	wantFill(t, matchReq(pickMatcher(relocEntry(home)), selectQ(q, labels...)), "I'm already in Seattle", OutcomeFilled)
	// The three-option set with a negated presence clause beside the relocation clause.
	r := matchReq(open, selectQ("Are you based in Seattle or open to relocating?", "Yes", "No", "While I do not currently live in Seattle, I am open to relocation"))
	wantFill(t, r, "Yes", OutcomeFilled)
}

// TestAnswerBank_Location_OfficeYesSplitsByResidence: an office question
// whose two Yes options differ only in where the applicant lives (HappyRobot)
// is answered by the location policy against the posting, as a relocation
// question is: the relocating Yes away from home, the living-there Yes at
// home. With no place known, or an office answer of No, it is the entry's
// ordinary pick.
func TestAnswerBank_Location_OfficeYesSplitsByResidence(t *testing.T) {
	const q = "Are you able to work in one of our offices? We embrace a hybrid schedule. Are you able to work from one of our office locations at least part of each week?"
	local, relocate, remote := "Yes, and I already live in the location listed on the job.", "Yes, but I don't live near an office and need to relocate.", "No, I need to work remotely."
	ask := radioQ(q, local, relocate, remote)
	matcher := func(answer string, pol *RelocationPolicy) *Matcher {
		return pickMatcher(fixedEnt(IDOfficeAttendanceOK, "Are you able to work from the office three days a week?", answer), relocEntry(pol))
	}
	open := alexPolicy(RelocationOpen, nil, nil)
	wantFill(t, matchReq(matcher("Yes", open), ask, withPosting("Example City, EC")), relocate, OutcomeFilled)
	wantFill(t, matchReq(matcher("Yes", open), ask, withPosting("Sampleton, ES")), local, OutcomeFilled)
	if r := matchReq(matcher("Yes", open), ask, withPosting("")); r.ChosenLabel == local || r.ChosenLabel == relocate {
		t.Fatalf("no place known: no branch is chosen: %+v", r)
	}
	if r := matchReq(matcher("No", open), ask, withPosting("Example City, EC")); r.ChosenLabel == local || r.ChosenLabel == relocate {
		t.Fatalf("an office answer of No never lands on a Yes: %+v", r)
	}
	if !isStatedRelocationOption(relocate) || isStatedRelocationOption("No, I don't need to relocate") || isStatedRelocationOption(remote) {
		t.Fatal("a stated need to move is an affirmative branch; a negated one and a remote No are none")
	}
	// Brex: "Yes, I'd relocate prior to the start of the role" states the move.
	brex := Question{Selector: "s", Kind: KindCustomListbox, Text: "This role requires in-office work three days per week (Mon, Wed, Thurs). Do you acknowledge and agree to this requirement?",
		Options: opts("Yes, I’m currently located here", "Yes, I’d relocate prior to the start of the role", "No, I’m not located nearby")}
	wantFill(t, matchReq(matcher("Yes", open), brex, withPosting("Example City, EC")), "Yes, I’d relocate prior to the start of the role", OutcomeFilled)
	if isStatedRelocationOption("No, I would not relocate") {
		t.Fatal("a refused move is no affirmative branch")
	}
}

// TestAnswerBank_Location_OfficeYesThatNamesAPlaceClaimsResidence: an office
// question that asks the applicant to confirm their current location makes a
// Yes that names a place a claim to live there (Gusto: "Please confirm your
// current long-term location and ability to work in this hybrid schedule"
// over "Yes - San Francisco" / "No"). An applicant who lives elsewhere is not
// answered with it, nor with the No: the question is left for them. At home,
// and where the question does not ask where they live, the Yes is the entry's.
func TestAnswerBank_Location_OfficeYesThatNamesAPlaceClaimsResidence(t *testing.T) {
	const confirm = "This is a hybrid role based in San Francisco. Our hybrid schedule requires those based in these locations to be in office on Tuesdays and Wednesdays. Please confirm your current long-term location and ability to work in this hybrid schedule (yes/no)"
	matcher := func(pol *RelocationPolicy) *Matcher {
		return pickMatcher(fixedEnt(IDOfficeAttendanceOK, "Are you able to work from the office three days a week?", "Yes"), relocEntry(pol))
	}
	ask := func(text string) Question {
		return Question{Selector: "s", Text: text, Kind: KindCustomListbox, Options: opts("Yes - San Francisco", "No")}
	}
	away := matchReq(matcher(alexPolicy(RelocationOpen, nil, nil)), ask(confirm), withPosting("San Francisco, CA"))
	if away.Outcome != OutcomeAbstained || !away.Terminal || away.Detail != DetailResidenceClaim {
		t.Fatalf("an applicant elsewhere is asked, by no lane answered: %+v", away)
	}
	home := alexPolicy(RelocationOpen, nil, nil)
	home.Home = Home{City: "San Francisco", Region: "CA", Country: "US"}
	if r := matchReq(matcher(home), ask(confirm), withPosting("San Francisco, CA")); r.ChosenLabel != "Yes - San Francisco" {
		t.Fatalf("at home the Yes is the applicant's: %+v", r)
	}
	plain := "Are you able to work in our office on Tuesdays and Wednesdays?"
	if r := matchReq(matcher(alexPolicy(RelocationOpen, nil, nil)), ask(plain), withPosting("San Francisco, CA")); r.ChosenLabel != "Yes - San Francisco" {
		t.Fatalf("a question that does not ask where the applicant lives keeps the entry's Yes: %+v", r)
	}
}

// TestAnswerBank_Location_CommuteDistance is the F-10 rule of "Relocation and
// location policy" for Greenhouse's commute list: a home city that is one of
// the offices is local, a home state beyond commuting reach of every office's
// state is not, and anything between stays with the user. Relocating is no
// reason to be local.
func TestAnswerBank_Location_CommuteDistance(t *testing.T) {
	const opswat = "Tampa or San Francisco"
	ask := func(offices string) Question {
		named := offices
		if offices == opswat {
			named = "San Francisco or Tampa"
		}
		return Question{Selector: "s", Kind: KindCustomListbox,
			Text:    "While we understand good talent can be anywhere in the US, candidates within a commutable distance of our " + offices + " locations are expected to join our onsite community.",
			Options: opts("I'm not local to "+named+".", "I'm local to "+named+" and am willing to work onsite.", "I'm local to "+named+" and cannot work onsite.")}
	}
	notLocal, local, refuse := "I'm not local to San Francisco or Tampa.", "I'm local to San Francisco or Tampa and am willing to work onsite.", "I'm local to San Francisco or Tampa and cannot work onsite."
	home := func(city, region, def string, places, excluded []string) *RelocationPolicy {
		return &RelocationPolicy{Default: def, Places: places, Excluded: excluded, Home: Home{City: city, Region: region, Country: "US"}}
	}
	for _, c := range []struct {
		name, offices string
		pol           *RelocationPolicy
		want          string // empty: nothing is chosen
	}{
		{"home is one of the offices", opswat, home("San Francisco", "CA", RelocationOpen, nil, nil), local},
		{"home office the policy refuses", opswat, home("Tampa", "FL", RelocationOpen, nil, []string{"Tampa"}), refuse},
		{"near miss: far away and will not relocate", opswat, home("Austin", "TX", RelocationClosed, nil, nil), notLocal},
		{"far away and would relocate there", opswat, home("Austin", "TX", RelocationOpen, []string{"San Francisco"}, nil), notLocal},
		{"another city of an office's state", opswat, home("Oakland", "CA", RelocationOpen, nil, nil), ""},
		{"one state between home and an office", opswat, home("Raleigh", "NC", RelocationOpen, nil, nil), ""},
		{"an office's state has no neighbours", "Hawaii", home("Kailua", "HI", RelocationOpen, nil, nil), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := matchReq(pickMatcher(relocEntry(c.pol)), ask(c.offices))
			if r.ChosenLabel != c.want || c.want != "" && (r.Outcome != OutcomeFilled || r.Source != SourceDerived) {
				t.Fatalf("chose %q (%s %s), want %q", r.ChosenLabel, r.Outcome, r.Source, c.want)
			}
		})
	}
}

// The commute question over options that lead with Yes or No takes the same
// rule (Hover): the Yes naming the home city, and for a home out of reach the
// No that plans to relocate only when the policy would.
func TestAnswerBank_Location_CommuteDistanceYesNo(t *testing.T) {
	const text = "Commute & Office Proximity: Are you currently located within commuting distance to either of our offices in San Francisco or New York City?"
	hover := []string{"Yes, San Francisco", "Yes, New York City", "No, but I am planning to relocate", "No (I understand this may disqualify me from this role)"}
	pol := func(city, region, def string) *RelocationPolicy {
		return &RelocationPolicy{Default: def, Home: Home{City: city, Region: region, Country: "US"}}
	}
	for _, c := range []struct {
		pol  *RelocationPolicy
		opts []string
		want string // empty: nothing is chosen
	}{
		{pol("San Francisco", "CA", RelocationOpen), []string{"Yes", "No"}, "Yes"},
		{pol("Austin", "TX", RelocationOpen), []string{"Yes", "No"}, "No"},
		{pol("Oakland", "CA", RelocationOpen), []string{"Yes", "No"}, ""},
		{pol("San Francisco", "CA", RelocationOpen), hover, "Yes, San Francisco"},
		{pol("Austin", "TX", RelocationOpen), hover, "No, but I am planning to relocate"},
		{pol("Austin", "TX", RelocationClosed), hover, "No (I understand this may disqualify me from this role)"},
		{pol("Austin", "TX", RelocationOpen), []string{"Yes", "No, and I am not willing to relocate"}, "No, and I am not willing to relocate"},
		{pol("Oakland", "CA", RelocationOpen), hover, ""},
	} {
		r := matchReq(pickMatcher(relocEntry(c.pol)), selectQ(text, c.opts...))
		if r.ChosenLabel != c.want || c.want != "" && (r.Outcome != OutcomeFilled || r.Source != SourceDerived) {
			t.Errorf("home %s (%s) over %v: chose %q (%s %s), want %q", c.pol.Home.City, c.pol.Default, c.opts, r.ChosenLabel, r.Outcome, r.Source, c.want)
		}
	}
}

// SecurityScorecard (Greenhouse): the onsite requirement is asked only of one
// within 75 miles of its offices, and the page has one beyond them select
// "Yes". A home beyond reach takes the option the page names; a home within
// reach is the willingness question, not the commute lane's.
func TestAnswerBank_Location_CommuteRequirementThePageWaivesBeyondReach(t *testing.T) {
	const text = "This role requires 1–2 days per week onsite only if you live within 75 miles of our Midtown NYC or Downtown Austin office. If you live within that range, are you willing and able to meet this requirement? (If you live outside that range, please select 'Yes' — this requirement does not apply to you.)"
	pol := func(city, region string) *RelocationPolicy {
		return &RelocationPolicy{Default: RelocationOpen, Home: Home{City: city, Region: region, Country: "US"}}
	}
	for _, c := range []struct {
		pol  *RelocationPolicy
		want string // empty: the commute lane does not answer
	}{
		{pol("Charleston", "SC"), "Yes"},
		{pol("Austin", "TX"), ""},
	} {
		r := matchReq(pickMatcher(relocEntry(c.pol)), selectQ(text, "Yes", "No"))
		if c.want == "" {
			if r.Source == SourceDerived && r.ChosenLabel != "" {
				t.Errorf("home %s: the commute lane chose %q\n%v", c.pol.Home.City, r.ChosenLabel, r.Trace)
			}
			continue
		}
		if r.ChosenLabel != c.want || r.Outcome != OutcomeFilled || r.Source != SourceDerived {
			t.Errorf("home %s: chose %q (%s %s), want %q\n%v", c.pol.Home.City, r.ChosenLabel, r.Outcome, r.Reason, c.want, r.Trace)
		}
	}
}

// Cockroach Labs (Greenhouse): "Please confirm you live near our NYC office
// location and understand the in-office expectation ..." over a bare Yes, a
// bare No and "Open to Relocating to NYC within 3 months of starting" is the
// same commute question: living near is a commuting reach, and the relocation
// option need not lead with No.
func TestAnswerBank_Location_LiveNearAnOfficeOrRelocate(t *testing.T) {
	const text = "Please confirm you live near our NYC office location and understand the in-office expectation of Mondays, Tuesdays and Thursdays."
	opts := []string{"Yes", "No", "Open to Relocating to NYC within 3 months of starting"}
	pol := func(city, region, def string) *RelocationPolicy {
		return &RelocationPolicy{Default: def, Home: Home{City: city, Region: region, Country: "US"}}
	}
	for _, c := range []struct {
		pol  *RelocationPolicy
		want string // empty: nothing is chosen
	}{
		{pol("New York", "NY", RelocationOpen), "Yes"},
		{pol("Charleston", "SC", RelocationOpen), "Open to Relocating to NYC within 3 months of starting"},
		{pol("Charleston", "SC", RelocationClosed), "No"},
		{pol("Newark", "NJ", RelocationOpen), ""},
	} {
		r := matchReq(pickMatcher(relocEntry(c.pol)), selectQ(text, opts...))
		if r.ChosenLabel != c.want || c.want != "" && (r.Outcome != OutcomeFilled || r.Source != SourceDerived) {
			t.Errorf("home %s (%s): chose %q (%s %s), want %q\n%v", c.pol.Home.City, c.pol.Default, r.ChosenLabel, r.Outcome, r.Reason, c.want, r.Trace)
		}
	}
}

// Verana (Greenhouse, live): the hybrid schedule asked of those within reach,
// with a distance statement per office for those beyond it. A home in one of
// the places is Yes; a home beyond reach is Yes by the owner's decision when
// the policy would relocate there and office attendance is Yes within its days.
func TestAnswerBank_Location_HybridOverDistanceStatements(t *testing.T) {
	const text = "Employees who live within 30 miles of our SF or NY office are required to work from the office up to 3 days per week. Are you able to work in this hybrid schedule?"
	opts := []string{"I live more than 30 miles away from the SF office.", "I live more than 30 miles away from the NY office.", "Yes", "No"}
	pol := func(city, region, def string) *RelocationPolicy {
		return &RelocationPolicy{Default: def, Home: Home{City: city, Region: region, Country: "US"}}
	}
	two := 2
	for _, c := range []struct {
		pol     *RelocationPolicy
		answer  string // the attendance entry's answer; empty: no entry
		maxDays *int
		want    string // empty: nothing is chosen
	}{
		{pol("New York", "NY", RelocationClosed), "", nil, "Yes"},
		{pol("Charleston", "SC", RelocationOpen), "Yes", nil, "Yes"},
		{pol("Charleston", "SC", RelocationClosed), "Yes", nil, ""},
		{pol("Charleston", "SC", RelocationOpen), "No", nil, ""},
		{pol("Charleston", "SC", RelocationOpen), "", nil, ""},
		{pol("Charleston", "SC", RelocationOpen), "Yes", &two, ""},
		{pol("Newark", "NJ", RelocationOpen), "Yes", nil, ""},
	} {
		entries := []Entry{relocEntry(c.pol)}
		if c.answer != "" {
			att := blankEntry(IDOfficeAttendanceOK, KindFixed)
			att.InputTypes, att.Answer, att.Placeholder = []string{"yes_no"}, c.answer, false
			att.NumericThreshold = &NumericThreshold{Extractor: ExtractorDaysPerWeekOnsite, MaxValue: c.maxDays, AboveAnswer: "No"}
			entries = append(entries, att)
		}
		r := matchReq(pickMatcher(entries...), selectQ(text, opts...))
		if r.ChosenLabel != c.want || c.want != "" && (r.Outcome != OutcomeFilled || r.Source != SourceDerived || !strings.Contains(strings.Join(r.Trace, "\n"), "commute distance: ")) {
			t.Errorf("home %s (%s) attendance %q max %v: chose %q (%s %s), want %q\n%v", c.pol.Home.City, c.pol.Default, c.answer, c.maxDays, r.ChosenLabel, r.Outcome, r.Reason, c.want, r.Trace)
		}
	}
}

// A commute question asked as a pick among the offices, with one option for
// none of them, takes the same rule (WeInfuse, Greenhouse): the office in the
// home city, and the none option for a home out of reach of every office;
// when the none option is split by relocation (MotherDuck), the policy picks.
func TestAnswerBank_Location_CommuteDistanceOfficePick(t *testing.T) {
	const text = "Which of our offices do you live within 1-hour driving distance of?"
	weinfuse := []string{"Austin, TX", "Dallas, TX", "N/A"}
	const hubs = "Are you located in commuting distance to one of our hubs?"
	motherduck := []string{"New York City", "San Francisco", "Seattle", "No, but willing to relocate", "No, NOT willing to relocate"}
	pol := func(city, region string) *RelocationPolicy {
		return &RelocationPolicy{Default: RelocationOpen, Home: Home{City: city, Region: region, Country: "US"}}
	}
	closed := func(city, region string) *RelocationPolicy {
		p := pol(city, region)
		p.Default = RelocationClosed
		return p
	}
	listed := func(city, region string) *RelocationPolicy {
		p := pol(city, region)
		p.Places = []string{"New York", "Chicago", "San Francisco", "Austin", "Boston", "Seattle"}
		return p
	}
	// BetterUp (Ashby): four hubs, each with its address, and no option for none.
	const hubs45 = "Are you located within 45 miles of one of the following metro areas? And can you come into the office a minimum of two days a week?"
	const ivoText = "Are you located near one of Ivo's US Offices?"
	ivo := []string{"I'm located in the San Francisco Bay Area", "I'm located in the New York Area", "I'm not located near an Ivo Office"}
	betterup := []string{"Austin, TX: 3100 E 5th St, Suite 350, TX 78702", "San Francisco, CA: 250 Montgomery St, Suite 400, San Francisco, CA 94104", "Arlington, VA: 1300 17th St N, Suite 800, Arlington, VA 22209", "New York, NY"}
	for _, c := range []struct {
		name  string
		text  string
		pol   *RelocationPolicy
		opts  []string
		multi bool
		want  string // empty: nothing is chosen
	}{
		{"home out of reach of every office", text, pol("Charleston", "SC"), weinfuse, false, "N/A"},
		{"home is an office's city", text, pol("Austin", "TX"), weinfuse, false, "Austin, TX"},
		{"commuting distance", "Which office are you within commuting distance of?", pol("Charleston", "SC"), []string{"Austin, TX", "Dallas, TX", "None of the above"}, false, "None of the above"},
		{"another city of an office's state", text, pol("Houston", "TX"), weinfuse, false, ""},
		{"a neighbouring state", text, pol("Shreveport", "LA"), weinfuse, false, ""},
		{"a question that asks no distance", "Which of our offices would you prefer to work from?", pol("Charleston", "SC"), weinfuse, false, ""},
		{"a longer drive", "Which of our offices do you live within a 5-hour drive of?", pol("Charleston", "SC"), weinfuse, false, ""},
		// Owner decision: with no option for none, an office out of reach is
		// picked as the relocation preference ranks it; listed places first.
		{"no option for none", text, pol("Charleston", "SC"), []string{"Austin, TX", "Dallas, TX"}, false, "Austin, TX"},
		{"no option for none, closed to relocating", text, closed("Charleston", "SC"), []string{"Austin, TX", "Dallas, TX"}, false, ""},
		{"no option for none, a preferred office", hubs45, listed("Charleston", "SC"), betterup, false, "New York, NY"},
		{"no option for none, several may be ticked", hubs45, listed("Charleston", "SC"), betterup, true, "New York, NY"},
		{"no option for none, an office beside the home state", hubs45, listed("Raleigh", "NC"), betterup, false, ""},
		{"an option that names no place", text, pol("Charleston", "SC"), []string{"Austin, TX", "Our flagship site", "N/A"}, false, ""},
		{"several offices may be ticked", text, pol("Austin", "TX"), weinfuse, true, ""},
		// MotherDuck (Ashby): "in commuting distance", the hubs as options and
		// the none option split by relocation.
		{"out of reach, open to relocating", hubs, pol("Charleston", "SC"), motherduck, false, "No, but willing to relocate"},
		{"out of reach, closed to relocating", hubs, closed("Charleston", "SC"), motherduck, false, "No, NOT willing to relocate"},
		{"home is a hub's city", hubs, pol("Seattle", "WA"), motherduck, false, "Seattle"},
		// Ivo (Ashby): each office an area the applicant is located in, and
		// the none option a sentence of its own.
		{"located near an office, none", ivoText, pol("Charleston", "SC"), ivo, false, "I'm not located near an Ivo Office"},
		{"located near an office, the home city's area", ivoText, pol("New York", "NY"), ivo, false, "I'm located in the New York Area"},
		{"located near an office, the Bay Area", ivoText, pol("San Francisco", "CA"), ivo, false, "I'm located in the San Francisco Bay Area"},
	} {
		t.Run(c.name, func(t *testing.T) {
			q := selectQ(c.text, c.opts...)
			if c.multi {
				q.Kind, q.InputType = KindCheckbox, InputMultiSelect
			}
			r := matchReq(pickMatcher(relocEntry(c.pol)), q)
			if r.ChosenLabel != c.want || c.want != "" && (r.Outcome != OutcomeFilled || r.Source != SourceDerived) {
				t.Fatalf("chose %q (%s %s), want %q; trace %v", r.ChosenLabel, r.Outcome, r.Source, c.want, r.Trace)
			}
			if want := []string{betterup[3], betterup[1], betterup[0], betterup[2]}; c.multi && c.want != "" && !slices.Equal(r.ChosenLabels, want) {
				t.Fatalf("ticked %q, want every hub in preference order %q", r.ChosenLabels, want)
			}
		})
	}
}

func TestAnswerBank_Location_ResidenceClaimGuard(t *testing.T) {
	m := pickMatcher(relocEntry(alexPolicy(RelocationOpen, nil, nil)))
	r := matchReq(m, selectQ("Are you willing to relocate to Seattle?", "Yes - I currently live in the Seattle area", "Yes - I would move for the role", "No"))
	if r.ChosenLabel != "Yes - I would move for the role" {
		t.Fatalf("a Yes never lands on an unearned residence claim: %+v", r)
	}
	if !traceHas(r, "states where the applicant lives") {
		t.Fatalf("the rejection is in the trace: %+v", r.Trace)
	}
	// When every option claims residence nothing is chosen.
	r = matchReq(m, selectQ("Are you willing to relocate to Seattle?", "Yes - I currently live in the Seattle area", "Yes - I am based in Seattle"))
	if r.Outcome != OutcomeAbstained {
		t.Fatalf("got %+v", r)
	}
}

// Cogent (Ashby): "Are you excited to work in person?" over a Yes for each
// office and "Yes, but I would require relocation" is the office picker; a
// home at none of the offices takes the move, never an office.
func TestAnswerBank_Location_OfficesBesideAMoveOption(t *testing.T) {
	const q = "Our team primarily works in office (4 days/week) from our San Francisco, CA & New York, NY offices. Are you excited to work in person?"
	labels := []string{"Yes, I can work in San Francisco", "Yes, I can work in New York", "Yes, but I would require relocation", "No, not interested working in person or relocation"}
	for _, c := range []struct{ city, region, want string }{{"Charleston", "SC", labels[2]}, {"New York", "NY", labels[1]}} {
		pol := &RelocationPolicy{Default: RelocationOpen, Places: []string{"New York", "San Francisco"}, Home: Home{City: c.city, Region: c.region, Country: "US"}}
		r := matchReq(pickMatcher(officeEntry(pol)), checkboxQ(q, labels...))
		if r.EntryID != IDPreferredWorkLocation || !slices.Contains(r.ChosenLabels, c.want) || slices.Contains(r.ChosenLabels, labels[2]) != (c.want == labels[2]) {
			t.Errorf("home %s: want %q, got %+v", c.city, c.want, r)
		}
	}
}

func officeEntry(pol *RelocationPolicy) Entry {
	e := blankEntry(IDPreferredWorkLocation, KindPickOption)
	e.InputTypes = []string{"single_select", "multi_select", "checkbox"}
	e.LocationPolicy = &LocationPolicy{Predicate: PredicateWillingToWorkAt, Resolved: pol}
	return e
}

func TestAnswerBank_Location_OfficePickerAbstains(t *testing.T) {
	m := pickMatcher(officeEntry(alexPolicy(RelocationClosed, nil, nil)))
	r := matchReq(m, selectQ("Preferred work location", "Select...", "Boston, MA", "Denver, CO"))
	if r.Outcome != OutcomeAbstained || !traceHas(r, "no rendered option is acceptable") {
		t.Fatalf("closed with nothing listed rendered: %+v", r)
	}
	// An office the applicant excluded is never committed.
	m = pickMatcher(officeEntry(alexPolicy(RelocationOpen, nil, []string{"Boston", "Denver"})))
	r = matchReq(m, selectQ("Preferred work location", "Boston, MA", "Denver, CO"))
	if r.Outcome != OutcomeAbstained {
		t.Fatalf("every option excluded: %+v", r)
	}
}

// n8n (Ashby, owner workspace): options that each state a visa need beside the
// place state the applicant's work standing there; the open relocation policy
// ticked all eight, claiming the UK and Germany without visa support. Such a
// list is a work-authorization question: it stays the user's, and no later
// lane takes it.
func TestAnswerBank_Location_OfficeOptionsStatingAVisaNeedAreNotPlaces(t *testing.T) {
	m := pickMatcher(officeEntry(alexPolicy(RelocationOpen, nil, nil)))
	r := matchReq(m, checkboxQ("Which office locations are you open to relocating to?",
		"I can be based in the UK and do not need visa support", "I want to be based in the UK but will need visa support",
		"I can be based in Germany and do not need visa support", "I will be based in the US and do not need visa support",
		"I will be based in the US and need visa support"))
	if r.Outcome != OutcomeAbstained || !r.Terminal {
		t.Fatalf("a visa need beside each place: %+v", r)
	}
	// An office list with no such clause is still the picker's.
	if r := matchReq(m, checkboxQ("Which office locations are you open to relocating to?", "Boston, MA", "Denver, CO")); r.Outcome != OutcomeFilled {
		t.Fatalf("plain offices: %+v", r)
	}
}

func TestAnswerBank_Location_OfficePickerCardinality(t *testing.T) {
	pol := alexPolicy(RelocationOpen, []string{"Denver", "Boston"}, []string{"Seattle"})
	m := pickMatcher(officeEntry(pol))
	labels := []string{"Select...", "Seattle, WA", "Boston, MA", "Denver, CO"}
	wantFill(t, matchReq(m, selectQ("Preferred work location", labels...)), "Denver, CO", OutcomeFilled)
	wantFill(t, matchReq(m, selectQ("Second work location preference", labels...)), "Boston, MA", OutcomeFilled)
	if r := matchReq(m, selectQ("Third work location preference", labels...)); r.Outcome != OutcomeAbstained {
		t.Fatalf("no option at that rank: %+v", r)
	}
	r := matchReq(m, checkboxQ("Which office locations are you open to relocating to?", labels[1:]...))
	if r.Outcome != OutcomeFilled || strings.Join(r.ChosenLabels, "|") != "Denver, CO|Boston, MA" {
		t.Fatalf("a checkbox group ticks every acceptable office in rank order: %+v", r)
	}
	// A checkbox group ticks every acceptable office whatever rank its text
	// names, also a rank past the acceptable ones.
	r = matchReq(m, checkboxQ("Third work location preference", labels[1:]...))
	if r.Outcome != OutcomeFilled || strings.Join(r.ChosenLabels, "|") != "Denver, CO|Boston, MA" {
		t.Fatalf("a checkbox group under an ordinal preference: %+v", r)
	}
}

func TestAnswerBank_Location_DictatedRelocationLiteral(t *testing.T) {
	m := pickMatcher(relocEntry(alexPolicy(RelocationOpen, nil, nil)), presenceEntry(alexPolicy(RelocationOpen, nil, nil)))
	q := textQ("w", `What is the address from which you plan on working? If you would need to relocate, please type "relocating".`)
	r := matchReq(m, q, withPosting("Othertown, XT"))
	if r.Answer != "relocating" || r.Source != SourceFormText || r.Outcome != OutcomeFilled {
		t.Fatalf("the form's own literal is typed for a relocating applicant: %+v", r)
	}
	for _, text := range []string{
		"Where will you work? If you would need to relocate, please type “relocating”.",
		"Where will you work? If you would need to relocate, please enter 'relocating'.",
	} {
		if r := matchReq(m, textQ("w", text), withPosting("Othertown, XT")); r.Answer != "relocating" {
			t.Errorf("%q: %+v", text, r)
		}
	}
	// The applicant lives at the work place, or would not relocate: nothing typed.
	if r := matchReq(m, q, withPosting("Sampleton, ES")); r.Outcome != OutcomeAbstained {
		t.Fatalf("applicant at the place: %+v", r)
	}
	closed := pickMatcher(relocEntry(alexPolicy(RelocationClosed, nil, nil)))
	if r := matchReq(closed, q, withPosting("Othertown, XT")); r.Outcome != OutcomeAbstained {
		t.Fatalf("would not relocate: %+v", r)
	}
	// Unconfigured preference and unknown place.
	if r := matchReq(pickMatcher(relocEntry(nil)), q, withPosting("Othertown, XT")); r.Outcome != OutcomeAbstained {
		t.Fatalf("unconfigured: %+v", r)
	}
	if r := matchReq(m, q, withPosting("")); r.Outcome != OutcomeAbstained {
		t.Fatalf("no place known: %+v", r)
	}
}

func TestAnswerBank_Location_JobLocationFromJdText(t *testing.T) {
	for _, c := range []struct {
		name, text string
		want       jobPlace
		ok         bool
	}{
		{"labelled city and state", "About us\nLocation: Sampleton, ES\nWe build things.", jobPlace{City: "Sampleton", Region: "ES"}, true},
		{"spelled state", "Location: New York, New York, United States", jobPlace{City: "New York", Region: "New York"}, true},
		{"two-word state on one collapsed line", "Location: Salt Lake City, Utah Responsibilities include", jobPlace{City: "Salt Lake City", Region: "Utah"}, true},
		{"remote line", "Office: Remote (US)", jobPlace{Remote: true}, true},
		{"based in line", "Based in: Boston, MA", jobPlace{City: "Boston", Region: "MA"}, true},
		{"remote wording", "This is a fully remote role.", jobPlace{Remote: true}, true},
		{"remote dash us", "Remote - US", jobPlace{Remote: true}, true},
		{"nothing", "We are hiring a builder.", jobPlace{}, false},
		{"a lower-case word is not a region", "Location: Sampleton, shire here", jobPlace{}, false},
		{"beyond the head", strings.Repeat("x ", 1100) + "Location: Boston, MA", jobPlace{}, false},
	} {
		got, ok := jobPlaceFromPostingText(c.text)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: %+v %t, want %+v %t", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestAnswerBank_Location_JdTextCityRegion(t *testing.T) {
	// The state-name alternation is longest first and the region is not open-ended.
	got, _ := jobPlaceFromPostingText("Location: Austin, Texas and more words follow here")
	if got.City != "Austin" || got.Region != "Texas" {
		t.Fatalf("%+v", got)
	}
	// A country after the city is the country, whole, and never a region; a
	// region that is no country stays one.
	for text, want := range map[string]jobPlace{
		"Location: Manchester, United Kingdom":            {City: "Manchester", Country: "United Kingdom"},
		"Location: Paris, France About the role":          {City: "Paris", Country: "France"},
		"Location: Toronto, Ontario":                      {City: "Toronto", Region: "Ontario"},
		"Location: Atlanta, Georgia":                      {City: "Atlanta", Region: "Georgia"},
		"Location: Sampleton, ES":                         {City: "Sampleton", Region: "ES"},
		"Location: Dublin, Ireland. We are a small team.": {City: "Dublin", Country: "Ireland"},
	} {
		if got, _ := jobPlaceFromPostingText(text); got != want {
			t.Errorf("%q: %+v, want %+v", text, got, want)
		}
	}
}

func TestAnswerBank_Location_OfficeTheApplicationIsFor(t *testing.T) {
	// Zoox (Lever): "Which location are you applying for?" is the office the
	// posting names, not the one the relocation ranking prefers.
	m := pickMatcher(officeEntry(alexPolicy(RelocationOpen, nil, nil)))
	q := selectQ("Which location are you applying for?", "Foster City, CA", "San Diego, CA")
	wantFill(t, matchReq(m, q, withPosting("San Diego, CA")), "San Diego, CA", OutcomeFilled)
	// A posting no option names leaves the ranked pick.
	if r := matchReq(m, q, withPosting("Austin, TX")); traceHas(r, "the option naming the posting's city") {
		t.Fatalf("no option names the posting's city: %+v", r)
	}
	// An office the applicant excluded is never committed.
	excl := pickMatcher(officeEntry(alexPolicy(RelocationOpen, nil, []string{"San Diego"})))
	if r := matchReq(excl, q, withPosting("San Diego, CA")); r.ChosenLabel == "San Diego, CA" {
		t.Fatalf("an excluded office was chosen: %+v", r)
	}
}

func TestAnswerBank_Location_OfficesOnlyWhereAuthorized(t *testing.T) {
	// Datadog (Greenhouse): "In what cities are you available to work?" over
	// cities on four continents. The relocation policy and the work
	// authorization are judged apart: a city is ticked
	// only where the policy takes it and the applicant may work, and a city
	// nothing places in a country is not claimed.
	m := pickMatcher(officeEntry(alexPolicy(RelocationOpen, nil, []string{"Denver"})))
	q := checkboxQ("In what cities are you available to work?", "Amsterdam", "Atlanta", "Bangalore", "Boston", "Denver", "Dubai", "Mexico City", "New York City", "Toronto, Canada")
	r := matchReq(m, q)
	if strings.Join(r.ChosenLabels, "|") != "Atlanta|Boston|New York City" {
		t.Fatalf("ticked = %q: %+v", r.ChosenLabels, r)
	}
	// One office: a listed place in a country the applicant may not work in is
	// passed over for one where they may.
	listed := pickMatcher(officeEntry(alexPolicy(RelocationOpen, []string{"Toronto"}, nil)))
	wantFill(t, matchReq(listed, selectQ("Which office location are you able to work from?", "Toronto, Canada", "Boston, MA")), "Boston, MA", OutcomeFilled)
	// A question of willingness takes the relocation policy alone.
	wantFill(t, matchReq(listed, selectQ("Preferred work location", "Toronto, Canada", "Boston, MA")), "Toronto, Canada", OutcomeFilled)
	relocate := matchReq(m, checkboxQ("Which locations would you be willing to relocate to?", "Amsterdam", "Boston", "Toronto, Canada"))
	if strings.Join(relocate.ChosenLabels, "|") != "Amsterdam|Boston|Toronto, Canada" {
		t.Fatalf("willing to relocate = %q: %+v", relocate.ChosenLabels, relocate)
	}
	// Lightmatter (Greenhouse): "Select the location you can commute or
	// relocate to" asks where the applicant can work: the US offices, never
	// Toronto, nor "Remote", which nothing places.
	lm := matchReq(m, checkboxQ("Select the location you can commute or relocate to", "Mountain View, CA", "Boston, MA", "Toronto, Canada", "Remote"))
	if got := slices.Sorted(slices.Values(lm.ChosenLabels)); strings.Join(got, "|") != "Boston, MA|Mountain View, CA" {
		t.Fatalf("commute or relocate = %q: %+v", lm.ChosenLabels, lm)
	}
	// No recorded authorization claims no city.
	none := matchReq(m, q, func(r *Request) { r.Facts.AuthorizedCountries = nil })
	if none.Outcome != OutcomeAbstained {
		t.Fatalf("no authorized country: %+v", none)
	}
}

// Steadily (Ashby, live): "If you do not currently reside near this role's
// office location, you must be willing to relocate." accepts one who would
// move there; a home beyond commuting reach takes Yes when the policy would
// work at the posting's place, and No when it would not.
func TestAnswerBank_Location_CommuteQuestionThatAcceptsRelocation(t *testing.T) {
	const text = "Are you able to be physically in office at least four days a week? If you do not currently reside near this role's office location, you must be willing to relocate."
	home := Home{City: "Charleston", Region: "SC", Country: "US"}
	for _, c := range []struct {
		name string
		pol  *RelocationPolicy
		want string
	}{
		{"open to relocation", &RelocationPolicy{Default: RelocationOpen, Home: home}, "Yes"},
		{"closed to relocation", &RelocationPolicy{Default: RelocationClosed, Home: home}, "No"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := matchReq(pickMatcher(relocEntry(c.pol)), Question{Selector: "s", Text: text, Kind: KindRadioGroup, Options: opts("Yes", "No")}, withPosting("Austin, TX"))
			if r.ChosenLabel != c.want || r.Source != SourceDerived {
				t.Fatalf("chose %q (%s %s), want %q; trace %v", r.ChosenLabel, r.Outcome, r.Source, c.want, r.Trace)
			}
		})
	}
}

// SumUp (Greenhouse, queue154): "Are you willing and able to commute to our
// Boulder, Colorado office Daily (M-F). If you live outside of the greater
// Boulder area, please let us know why Boulder is a target area for you." in
// one box: the answer and, for one who lives elsewhere, the reason, written
// from the address and the relocation policy (the owner's word:
// the agent writes it). One who would not move, or who has no recorded
// office-attendance Yes, is left.
func TestAnswerBank_Location_OfficeCommuteWithAReasonForANonLocal(t *testing.T) {
	const text = "Are you willing and able to commute to our Boulder, Colorado office Daily (M-F). If you live outside of the greater Boulder area, please let us know why Boulder is a target area for you."
	office := ent("office-attendance-ok", "fixed", withAnswer("Yes"), withTypes(InputYesNo))
	pol := func(city, region, def string) *RelocationPolicy {
		return &RelocationPolicy{Default: def, Home: Home{City: city, Region: region, Country: "US"}}
	}
	at := func(city, region string) func(*Request) {
		return func(r *Request) {
			r.Posting = Posting{Company: "SumUp", Location: "Boulder, Colorado"}
			r.Facts.Address = AddressFact{City: city, Region: region, Country: "US"}
		}
	}
	q := Question{Selector: "s", Text: text, Kind: KindText}
	const reason = "Yes. I currently live in Charleston, SC, and I am willing to relocate to Boulder, Colorado to work from the office daily. I am open to relocating for the right role, and this position at SumUp is what makes Boulder a target area for me."
	if r := matchReq(pickMatcher(relocEntry(pol("Charleston", "SC", RelocationOpen)), office), q, at("Charleston", "SC")); r.Answer != reason || r.Outcome == OutcomeAbstained {
		t.Fatalf("one who lives elsewhere: %s %q\n%v", r.Outcome, r.Answer, r.Trace)
	}
	if r := matchReq(pickMatcher(relocEntry(pol("Boulder", "CO", RelocationOpen)), office), q, at("Boulder", "CO")); r.Answer != "Yes" || r.Outcome == OutcomeAbstained {
		t.Fatalf("one who lives there: %s %q\n%v", r.Outcome, r.Answer, r.Trace)
	}
	if r := matchReq(pickMatcher(relocEntry(pol("Charleston", "SC", RelocationClosed)), office), q, at("Charleston", "SC")); r.Outcome != OutcomeAbstained {
		t.Fatalf("one who would not move: %s %q", r.Outcome, r.Answer)
	}
	if r := matchReq(pickMatcher(relocEntry(pol("Charleston", "SC", RelocationOpen))), q, at("Charleston", "SC")); r.Outcome != OutcomeAbstained {
		t.Fatalf("no recorded office attendance: %s %q", r.Outcome, r.Answer)
	}
	// The reason promises only the attendance the question names: an office
	// some days a week is no daily office, and a box that names no office
	// gets none.
	for text, want := range map[string]string{
		"Our Boulder office is hybrid, three days a week. If you live outside of the greater Boulder area, please let us know why Boulder is a target area for you.": "Yes. I currently live in Charleston, SC, and I am willing to relocate to Boulder, Colorado to work from the office. I am open to relocating for the right role, and this position at SumUp is what makes Boulder a target area for me.",
		"If you live outside of the greater Boulder area, please let us know why Boulder is a target area for you.":                                                  "Yes. I currently live in Charleston, SC, and I am willing to relocate to Boulder, Colorado. I am open to relocating for the right role, and this position at SumUp is what makes Boulder a target area for me.",
	} {
		r := matchReq(pickMatcher(relocEntry(pol("Charleston", "SC", RelocationOpen)), office), Question{Selector: "s", Text: text, Kind: KindText}, at("Charleston", "SC"))
		if r.Outcome == OutcomeAbstained || r.Answer != want {
			t.Errorf("%q: %s %q", text, r.Outcome, r.Answer)
		}
	}
}

// Everlywell (Lever, queue154-156): "... Are you able to work out of our
// Austin office? If you are not located in ATX, are you willing to relocate?"
// reaches the relocation entry, over a Yes and two Noes of which only one
// offers a move: one in Charleston who would relocate takes the willing No,
// one in Austin the Yes, and one who would not move the other No.
func TestAnswerBank_Location_OfficeOrRelocateThreeOptionsOnTheRelocationEntry(t *testing.T) {
	const text = "Everlywell operates in a hybrid model in Austin, TX. Employees are required to be in office 3 days a week (Tuesday - Thursday). Are you able to work out of our Austin office? If you are not located in ATX, are you willing to relocate?"
	opts := []string{"Yes, I am in ATX and can work in office Tues-Thurs", "No, I am not in ATX but am willing to relocate and work in office Tues-Thurs", "No, I am not in ATX or willing to work in office"}
	pol := func(city, region, def string) *RelocationPolicy {
		return &RelocationPolicy{Default: def, Home: Home{City: city, Region: region, Country: "US"}}
	}
	for _, c := range []struct {
		pol  *RelocationPolicy
		want string
	}{
		{pol("Charleston", "SC", RelocationOpen), opts[1]},
		{pol("Austin", "TX", RelocationOpen), opts[0]},
		{pol("Charleston", "SC", RelocationClosed), opts[2]},
	} {
		r := matchReq(pickMatcher(relocEntry(c.pol)), radioQ(text, opts...))
		if r.ChosenLabel != c.want || r.Outcome != OutcomeFilled {
			t.Errorf("home %s (%s): chose %q (%s %s), want %q\n%v", c.pol.Home.City, c.pol.Default, r.ChosenLabel, r.Outcome, r.Reason, c.want, r.Trace)
		}
	}
}

// AHEAD (Lever): "If so, where would you be open to relocating?" in a text
// box lists the places the relocation preference names, in its order; open
// with none listed says so; closed with none listed is left.
func TestAnswerBank_Location_WhereOpenToRelocatingListsThePlaces(t *testing.T) {
	q := Question{Selector: "s", Text: "If so, where would you be open to relocating?", Kind: KindText, InputType: InputText}
	listed := alexPolicy(RelocationOpen, []string{"New York", "Chicago", "Seattle"}, nil)
	if r := matchReq(pickMatcher(relocEntry(listed), officeEntry(listed)), q); r.Answer != "New York, Chicago, Seattle" || r.Outcome == OutcomeAbstained {
		t.Fatalf("the listed places: %+v", r)
	}
	open := alexPolicy(RelocationOpen, nil, nil)
	if r := matchReq(pickMatcher(relocEntry(open), officeEntry(open)), q); r.Answer != "Open to relocating anywhere" {
		t.Fatalf("open with none listed: %+v", r)
	}
	closed := alexPolicy(RelocationClosed, nil, nil)
	if r := matchReq(pickMatcher(relocEntry(closed), officeEntry(closed)), q); r.Outcome != OutcomeAbstained {
		t.Fatalf("closed with none listed: %+v", r)
	}
}

// Verisoul (Ashby, queue775): a lone required box stating US work
// authorization and in-person work at a place is ticked when the user's own
// work-auth-us says Yes and the relocation policy would work there; a place
// the policy would not work at leaves it.
func TestAnswerBank_Location_AuthorizedAndOnsiteStatement(t *testing.T) {
	auth := blankEntry("work-auth-us", KindFixed)
	auth.Answer, auth.Placeholder = "Yes", false
	pol := &RelocationPolicy{Default: RelocationClosed, Places: []string{"Austin"}, Home: Home{City: "Charleston", Region: "SC", Country: "US"}}
	m := pickMatcher(relocEntry(pol), auth)
	for _, c := range []struct {
		name, place string
		want        bool
	}{
		{"a place the policy would work at", "Austin, Texas", true},
		{"a place it would not", "Denver, Colorado", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			label := "I'm authorized to work in the US and am excited to work in-person in " + c.place
			r := matchReq(m, required(checkboxQ("Work in "+c.place, label)), withPosting(c.place))
			if got := r.Outcome == OutcomeFilled && r.ChosenLabel == label; got != c.want {
				t.Fatalf("ticked=%v (%s %q %s), want %v", got, r.Outcome, r.ChosenLabel, r.Reason, c.want)
			}
		})
	}
}
