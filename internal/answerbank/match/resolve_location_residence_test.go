package match

import "testing"

func TestAnswerBank_LocationPolicy_UnearnedResidenceOptionRejected(t *testing.T) {
	const q = "Are you willing to relocate to Seattle?"
	m := pickMatcher(relocEntry(alexPolicy(RelocationOpen, nil, nil)))

	t.Run("an option naming no place is not earned", func(t *testing.T) {
		r := matchReq(m, selectQ(q, "Yes - I currently live nearby", "Yes - I would move for the role", "No"))
		wantFill(t, r, "Yes - I would move for the role", OutcomeFilledReview)
	})

	t.Run("a residence option beside a relocation No leaves the field to the user", func(t *testing.T) {
		r := matchReq(m, selectQ(q, "Yes - I currently live in the Seattle area", "No - I would need to relocate"))
		if r.Outcome != OutcomeAbstained {
			t.Fatalf("got %+v", r)
		}
	})

	t.Run("the profile's home place earns the option", func(t *testing.T) {
		home := alexPolicy(RelocationOpen, nil, nil)
		home.Home.City = "Seattle"
		r := matchReq(pickMatcher(relocEntry(home)), selectQ(q, "Yes - I currently live in the Seattle area", "No - I would need to relocate"))
		if r.ChosenLabel != "Yes - I currently live in the Seattle area" {
			t.Fatalf("got %+v", r)
		}
	})
}

func TestAnswerBank_LocationPolicy_USResidenceFromAddress(t *testing.T) {
	m := New(Bank{}, DefaultStages())
	addr := func(country, region string) func(*Request) {
		return func(r *Request) {
			r.Facts.Address = AddressFact{City: "Example City", Region: region, Country: country}
		}
	}
	const cont = "Do you currently reside in the Continental United States?"
	cases := []struct {
		q    string
		mod  func(*Request)
		want string // "" leaves the question to the entries
	}{
		{cont, addr("US", "NC"), "Yes"},
		{cont, addr("US", "District of Columbia"), "Yes"},
		{cont, addr("US", "AK"), "No"},
		{cont, addr("US", "Hawaii"), "No"},
		{cont, addr("CA", "ON"), "No"},
		{cont, addr("PR", ""), "No"},
		{"Are you located in the lower 48 states? *", addr("US", "TX"), "Yes"},
		{"Do you live in the contiguous US?", addr("US", "HI"), "No"},
		{"Are you located in the US?", addr("US", "HI"), "Yes"},
		{"Do you currently live in the United States?", addr("CA", "BC"), "No"},
		{"Are you a resident of the United States?", addr("US", "WA"), "Yes"},
		// Branch (Greenhouse, live): where one "permanently" resides is where
		// one lives (its follow-up asks the city and state); a permanent
		// resident is an immigration status, not this lane's.
		{"Do you permanently reside within the United States?", addr("US", "SC"), "Yes"},
		{"Do you permanently reside within the United States?", addr("CA", "ON"), "No"},
		{"Are you a permanent resident of the United States?", addr("US", "SC"), ""},
		// ReadMe (Ashby): a note that only states the employer's hiring limit
		// adds nothing to answer; one that addresses the applicant does.
		{"🌎 Are you located within the United States? Unfortunately, due to the constraints associated with being a small startup, we're not currently able to hire outside of the United States.", addr("US", "SC"), "Yes"},
		{"Are you located in the US? If not, tell us where you are based.", addr("US", "SC"), ""},
		// Vesta (Ashby, live): a preamble that restates the US requirement, and
		// working from within the country it asks the applicant to be in.
		{"The role is U.S. based, you must currently live and work from within the U.S. Are you currently located in, and would be working remotely from within the United States?", addr("US", "SC"), "Yes"},
		{"The role is U.S. based, you must currently live and work from within the U.S. Are you currently located in, and would be working remotely from within the United States?", addr("CA", "ON"), "No"},
		{"If you are willing to relocate, say so. Are you located in the United States?", addr("US", "SC"), ""},
		// A state with no country names the United States.
		{cont, addr("", "OR"), "Yes"},
		// Nothing recorded, another place, a time or a willingness: not this lane's.
		{cont, addr("", ""), ""},
		// Another place beside the United States is the residence lane's
		// (TestXC_ResidingInANamedCountry), read from the address's country.
		{"Do you currently live in the US or Canada?", addr("US", "NC"), "Yes"},
		{"Will you reside in the continental United States during employment?", addr("US", "NC"), ""},
		{"Do you currently reside in the Continental United States or are you willing to relocate?", addr("US", "NC"), ""},
		{"Are you currently located outside the United States?", addr("US", "NC"), ""},
	}
	for _, c := range cases {
		r := matchReq(m, selectQ(c.q, "Yes", "No"), c.mod)
		derived := r.Source == SourceDerived
		if c.want == "" && derived || c.want != "" && (!derived || r.ChosenLabel != c.want) {
			t.Errorf("%q: want %q, got %+v", c.q, c.want, r)
		}
	}
	// A US address with no state cannot place the continental part.
	r := matchReq(m, selectQ(cont, "Yes", "No"), addr("US", ""))
	if r.Outcome != OutcomeAbstained || r.Reason != ReasonPlaceholderEmpty {
		t.Fatalf("no state: %+v", r)
	}
	// A territory is outside the continental part, and whether a question
	// about the United States counts it is unclear, whether the profile
	// records its code or the bank its name.
	const plain = "Are you located in the US?"
	named := pickMatcher(ent("address-country", "pick-option", withPreferred("Puerto Rico")))
	for name, r := range map[string]Result{
		"the profile's code": matchReq(m, selectQ(plain, "Yes", "No"), addr("PR", "")),
		"the bank's name":    matchReq(named, selectQ(plain, "Yes", "No")),
	} {
		if r.Outcome != OutcomeAbstained || r.Reason != ReasonAmbiguous {
			t.Errorf("a territory, %s: %+v", name, r)
		}
	}
	if r := matchReq(named, selectQ(cont, "Yes", "No")); r.Source != SourceDerived || r.ChosenLabel != "No" {
		t.Errorf("a territory named by the bank is outside the continental part: %+v", r)
	}
}

// Palantir (Lever): "Are you a resident of California?" is the recorded
// address's state against the one named. A place that is not a whole state
// name, a willingness, or an address that names no state is not this lane's.
func TestAnswerBank_LocationPolicy_StateResidenceFromAddress(t *testing.T) {
	m := New(Bank{}, DefaultStages())
	addr := func(country, region string) func(*Request) {
		return func(r *Request) {
			r.Facts.Address = AddressFact{City: "Example City", Region: region, Country: country}
		}
	}
	cases := []struct {
		q    string
		mod  func(*Request)
		want string // "" leaves the question to the entries
	}{
		{"Are you a resident of California?", addr("US", "SC"), "No"},
		{"Are you a resident of California?", addr("US", "CA"), "Yes"},
		{"Are you a current resident of the state of New York?", addr("US", "New York"), "Yes"},
		{"Do you currently reside in Texas?", addr("US", "South Carolina"), "No"},
		{"Are you a resident of New York City?", addr("US", "NY"), ""},
		{"Are you a resident of California?", addr("US", ""), ""},
		{"Are you willing to become a resident of California?", addr("US", "SC"), ""},
		{"Are you a resident of Ontario?", addr("CA", "ON"), ""},
	}
	for _, c := range cases {
		r := matchReq(m, selectQ(c.q, "Yes", "No"), c.mod)
		derived := r.Source == SourceDerived
		if c.want == "" && derived || c.want != "" && (!derived || r.ChosenLabel != c.want) {
			t.Errorf("%q: want %q, got %+v", c.q, c.want, r)
		}
	}
}

// Gamma (Ashby): "Are you currently located in the Bay Area and open to this
// schedule?" over a located Yes, a not-there-but-relocating option and a
// remote No is the relocation-or-local branch set. The policy picks the branch
// though the entry's own words bind none of the options, so the named-metro
// No never lands on the remote option.
func TestAnswerBank_LocationPolicy_RelocationOrLocalBranchSet(t *testing.T) {
	const q = "This position is based onsite at our Potrero Hill office in San Francisco, 4–5 days per week. Are you currently located in the Bay Area and open to this schedule?"
	labels := []string{"Yes, I'm currently in the Bay Area and open to 4–5 days onsite", "I'm not currently in the Bay Area but am open to relocation and 4-5 days onsite", "No, I am looking for remote opportunities"}
	local := ent(IDRelocationOrLocal, "pick-option", withPreferred("willing to relocate", "open to relocating", "not local", "relocate"), withTypes(InputSingleSelect))
	metro := ent("based-in-named-metro", "fixed", withAnswer("No"), withTypes(InputYesNo))
	for _, c := range []struct{ name, city, want string }{
		{"away from the office", "Charleston", labels[1]},
		{"at the office", "San Francisco", labels[0]},
	} {
		t.Run(c.name, func(t *testing.T) {
			pol := alexPolicy(RelocationOpen, nil, nil)
			pol.Home = Home{City: c.city, Region: "CA", Country: "US"}
			if c.city == "Charleston" {
				pol.Home.Region = "SC"
			}
			r := matchReq(pickMatcher(local, metro, relocEntry(pol)), required(radioQ(q, labels...)), withPosting("San Francisco, CA"))
			if r.ChosenLabel != c.want || r.EntryID != IDRelocationOrLocal {
				t.Fatalf("want %q from %s, got %+v", c.want, IDRelocationOrLocal, r)
			}
		})
	}
}

// Harvey (Ashby, owner workspace): "Are you currently based in the listed
// location and able to work in person 3 days per week?" behind an "office
// location" sentence is the same branch set, its located option "based in
// this location", beside an "Other" aside. It is no office picker: under an
// open policy that would rank every option an unlisted place and tick the
// first, a home in a city the applicant does not live in.
func TestAnswerBank_LocationPolicy_BasedInTheListedLocationBranchSet(t *testing.T) {
	const q = "This role is tied to the office location listed in the job posting. Team members are expected to work from the office 3 days per week as part of Harvey's hybrid work model. Are you currently based in the listed location and able to work in person 3 days per week?"
	labels := []string{"Yes, I’m based in this location and able to work from the office 3 days per week", "No, I’m not based in this location but willing to relocate", "No, I’m only able to work remotely", "Other (optional context)"}
	local := ent(IDRelocationOrLocal, "pick-option", withPreferred("able and willing to relocate", "willing to relocate"), withTypes(InputSingleSelect))
	metro := ent("based-in-named-metro", "fixed", withAnswer("No"), withTypes(InputYesNo))
	for _, c := range []struct{ posting, city, region, want string }{
		{"Sydney", "Charleston", "SC", labels[1]},
		{"New York", "Charleston", "SC", labels[1]},
		{"New York", "New York", "NY", labels[0]},
	} {
		t.Run(c.posting+" from "+c.city, func(t *testing.T) {
			pol := alexPolicy(RelocationOpen, nil, nil)
			pol.Home = Home{City: c.city, Region: c.region, Country: "US"}
			r := matchReq(pickMatcher(local, metro, officeEntry(pol), relocEntry(pol)), required(radioQ(q, labels...)), withPosting(c.posting))
			if r.ChosenLabel != c.want || r.EntryID != IDRelocationOrLocal || r.Source != SourceDerived {
				t.Fatalf("want %q from the policy through %s, got %+v", c.want, IDRelocationOrLocal, r)
			}
		})
	}
}

// CharterUP (Greenhouse): "No, but I am planning to relo before starting" is
// the affirmative branch, its No answering the presence the question asks
// about; the engine's search for a bare "Yes" picked the residence claim.
func TestAnswerBank_LocationPolicy_NoButRelocatingBranch(t *testing.T) {
	const q = "This role is hybrid and requires working from our Austin, TX office on Monday, Wednesday, and Friday. Are you currently located in Austin or willing to relocate prior to your start date?"
	labels := []string{"Yes, I live in Austin", "No, but I am planning to relo before starting", "No, and I am not planning to relo"}
	for _, c := range []struct{ def, want string }{{RelocationOpen, labels[1]}, {RelocationClosed, labels[2]}} {
		pol := &RelocationPolicy{Default: c.def, Home: Home{City: "Charleston", Region: "SC", Country: "US"}}
		r := matchReq(pickMatcher(relocEntry(pol)), selectQ(q, labels...), withPosting("Austin, Texas"))
		if r.ChosenLabel != c.want || r.Source != SourceDerived {
			t.Errorf("%s policy: want %q, got %+v", c.def, c.want, r)
		}
	}
}

// Socure (Ashby): "within 45 miles of one of our hubs" is the commute reach;
// a hybrid sentence after it does not make it the office-attendance question.
func TestAnswerBank_Location_WithinMilesOfTheHubs(t *testing.T) {
	const text = "Do you live within 45 miles of one of Socure's Tech talent hubs in the US? This is a hybrid role. While we are still expanding our office footprint, you will generally be expected to come into the office 2–3 days a week (where a location is available). Exact scheduling and flexibility may vary depending on your manager and team. San Francisco, CA New York City, NY Seattle, WA"
	office := ent("office-attendance-ok", "fixed", withAnswer("Yes"), withTypes(InputYesNo))
	for _, c := range []struct{ city, region, want string }{{"Charleston", "SC", "No"}, {"Seattle", "WA", "Yes"}} {
		pol := &RelocationPolicy{Default: RelocationOpen, Home: Home{City: c.city, Region: c.region, Country: "US"}}
		r := matchReq(pickMatcher(relocEntry(pol), office), required(radioQ(text, "Yes", "No")))
		if r.ChosenLabel != c.want || r.Source != SourceDerived {
			t.Errorf("home %s: want %q from the commute rule, got %+v", c.city, c.want, r)
		}
	}
}

// Socure (Ashby): "Do you currently reside in any of the following states: DE,
// HI, ...?" over Yes and No is the recorded home state against the list.
func TestAnswerBank_Location_ListedStatesYesNo(t *testing.T) {
	const text = "Do you currently reside in any of the following states: DE, HI, IA, KY, MS, NE, NM, SD, VT, WV, WY? Unfortunately we are unable to hire employees residing in these states."
	listed := ent("live-in-listed-states", "pick-option", withPreferred("No"), withTypes(InputSingleSelect))
	addr := func(region string) func(*Request) {
		return func(r *Request) { r.Facts.Address = AddressFact{City: "Example City", Region: region, Country: "US"} }
	}
	for _, c := range []struct{ region, want string }{{"SC", "No"}, {"South Carolina", "No"}, {"WV", "Yes"}, {"Vermont", "Yes"}} {
		if r := matchReq(pickMatcher(listed), required(radioQ(text, "Yes", "No")), addr(c.region)); r.ChosenLabel != c.want {
			t.Errorf("home %s: want %q, got %+v", c.region, c.want, r)
		}
	}
	if r := matchReq(pickMatcher(listed), required(radioQ(text, "Yes", "No")), addr("")); r.Outcome != OutcomeAbstained {
		t.Errorf("no recorded state: left, got %+v", r)
	}
}

// Gigs (Greenhouse, live): based-in-named-metro holds the user's No for the
// places a form names; a question that names the recorded home is Yes from the
// address, and one that names only other places keeps the No.
func TestAnswerBank_LocationPolicy_NamedMetroThatIsHome(t *testing.T) {
	metro := Entry{ID: "based-in-named-metro", Kind: KindFixed, Answer: "No", InputTypes: []string{string(InputYesNo)}}
	m := pickMatcher(relocEntry(alexPolicy(RelocationOpen, nil, nil)), metro)
	for _, c := range []struct{ q, want string }{
		{"Are you currently based in Sampleton?", "Yes"},
		{"Are you currently based in New York or Sampleton?", "Yes"},
		{"Are you currently based in New York or San Francisco, and will to work hybrid?", "No"},
		{"Are you currently based in New York or San Francisco?", "No"},
	} {
		r := matchReq(m, radioQ(c.q, "Yes", "No"))
		if r.EntryID != "based-in-named-metro" || r.ChosenLabel != c.want {
			t.Errorf("%q: got %s %q (%s), want %q", c.q, r.EntryID, r.ChosenLabel, r.Outcome, c.want)
		}
	}
}

// Keystone (Greenhouse, live): "In which Keystone office location do you
// reside?" over the offices and "None - I'm looking to relocate" is where the
// applicant lives, never an office preference. The office that is home is
// picked; a home beyond commuting reach of every office takes the catch-all,
// whose stated move needs a policy that would work at one of the offices; an
// office within reach of home, or no recorded home, is left.
func TestAnswerBank_Location_ResidenceAmongListedPlaces(t *testing.T) {
	offices := []string{"Boston", "New York", "San Francisco", "Washington D.C.", "None - I'm looking to relocate"}
	home := func(city, region string, def string) *RelocationPolicy {
		return &RelocationPolicy{Default: def, Home: Home{City: city, Region: region, Country: "US"}}
	}
	for _, c := range []struct {
		name, q string
		pol     *RelocationPolicy
		opts    []string
		want    string
	}{
		{"home beyond reach", "In which Keystone office location do you reside?", home("Example City", "SC", RelocationOpen), offices, "None - I'm looking to relocate"},
		{"following locations", "In which of the following locations do you live?", home("Example City", "SC", RelocationOpen), offices, "None - I'm looking to relocate"},
		{"home is an office", "In which Keystone office location do you reside?", home("New York", "NY", RelocationOpen), offices, "New York"},
		{"office within reach", "In which Keystone office location do you reside?", home("Example City", "MA", RelocationOpen), offices, ""},
		{"no move", "In which Keystone office location do you reside?", home("Example City", "SC", RelocationClosed), offices, ""},
		{"plain none", "Which of these locations do you live in?", home("Example City", "SC", RelocationClosed), []string{"Boston", "New York", "None of the above"}, "None of the above"},
		{"no home", "In which Keystone office location do you reside?", home("", "", RelocationOpen), offices, ""},
		// Lighthouse (Greenhouse, live): the city asked over the posting's cities and "Other".
		{"what city", "What city, state, and/or municipality are you currently based in?", home("Example City", "SC", RelocationOpen), []string{"Denver", "Dallas", "Other"}, "Other"},
		{"what city at home", "What city are you currently based in?", home("Dallas", "TX", RelocationOpen), []string{"Denver", "Dallas", "Other"}, "Dallas"},
		{"what city within reach", "What city are you currently based in?", home("Example City", "OK", RelocationOpen), []string{"Denver", "Dallas", "Other"}, ""},
		// Ironclad (Ashby, live): whether the applicant lives in one of the
		// metros, asked over the metros; over Yes and No it is no list of places.
		{"one of the following metro areas", "Do you live in one of the following metro areas?", home("Example City", "SC", RelocationOpen), []string{"Chicago Metro Area", "NYC Metro Area", "None", "San Francisco Bay Area", "Seattle Metro Area"}, "None"},
		{"one of the following metro areas over yes and no", "Do you live in one of the following metro areas?", home("Example City", "SC", RelocationOpen), []string{"Yes", "No"}, ""},
	} {
		r := matchReq(pickMatcher(relocEntry(c.pol)), required(selectQ(c.q, c.opts...)))
		if c.want == "" {
			if r.Outcome != OutcomeAbstained {
				t.Errorf("%s: want left, got %q (%s) %v", c.name, r.ChosenLabel, r.Outcome, r.Trace)
			}
			continue
		}
		if r.ChosenLabel != c.want {
			t.Errorf("%s: want %q, got %q (%s) %v", c.name, c.want, r.ChosenLabel, r.Outcome, r.Trace)
		}
	}
}

// Orum (Ashby, live): "Are you currently located in either the San Francisco
// Bay Area or Austin, Texas?" over "Yes or willing to relocate" and "No and
// not willing to relocate". The Yes takes one who would relocate: a policy
// that would work at a named place picks it although the home is elsewhere; a
// policy that would work at none picks the No; a named place outside the
// vocabulary is left.
func TestAnswerBank_LocationPolicy_NamedMetroOrWillingToRelocate(t *testing.T) {
	metro := Entry{ID: "based-in-named-metro", Kind: KindFixed, Answer: "No", InputTypes: []string{string(InputYesNo)}}
	opts := []string{"Yes or willing to relocate", "No and not willing to relocate "}
	const q = "Are you currently located in either the San Francisco Bay Area or Austin, Texas?"
	for _, c := range []struct {
		name string
		pol  *RelocationPolicy
		q    string
		want string
	}{
		{"open to anywhere", alexPolicy(RelocationOpen, nil, nil), q, opts[0]},
		{"the places listed", alexPolicy(RelocationClosed, []string{"Austin"}, nil), q, opts[0]},
		{"no move", alexPolicy(RelocationClosed, nil, nil), q, opts[1]},
		{"home is named", alexPolicy(RelocationClosed, nil, nil), "Are you currently located in either Sampleton or Austin, Texas?", opts[0]},
	} {
		r := matchReq(pickMatcher(relocEntry(c.pol), metro), required(radioQ(c.q, opts...)))
		if r.EntryID != "based-in-named-metro" || r.ChosenLabel != c.want {
			t.Errorf("%s: got %s %q (%s) %v, want %q", c.name, r.EntryID, r.ChosenLabel, r.Outcome, r.Trace, c.want)
		}
	}
	// Orkes (Greenhouse, live): a Yes and two Noes split by relocation, the
	// places named in the sentence before the question.
	three := []string{"Yes, I currently live in the San Francisco Bay Area or the Seattle, WA area.",
		"No, I do not currently live in the San Francisco Bay area/Seattle and I am open to relocation.",
		"No, I do not currently live in the San Francisco Bay area and am not open to relocation."}
	const orkes = "This role is located in Santa Clara, CA or Bellevue, WA. Are you currently located within a drivable distance of either of those areas?"
	for _, c := range []struct {
		name string
		pol  *RelocationPolicy
		q    string
		want string
	}{
		{"three: open to anywhere", alexPolicy(RelocationOpen, nil, nil), orkes, three[1]},
		{"three: no move", alexPolicy(RelocationClosed, nil, nil), orkes, three[2]},
		{"three: home is named", alexPolicy(RelocationClosed, nil, nil), "This role is located in Sampleton or Bellevue, WA. Are you currently located within a drivable distance of either of those areas?", three[0]},
	} {
		r := matchReq(pickMatcher(relocEntry(c.pol), metro), required(radioQ(c.q, three...)))
		if r.EntryID != "based-in-named-metro" || r.ChosenLabel != c.want {
			t.Errorf("%s: got %s %q (%s) %v, want %q", c.name, r.EntryID, r.ChosenLabel, r.Outcome, r.Trace, c.want)
		}
	}
}
