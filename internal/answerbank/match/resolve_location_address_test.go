package match

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

// addressBank is the persona's address: the bank holds street, city, state with
// its code, postal code and country.
func addressBank(mods ...func(map[string]*Entry)) []Entry {
	es := map[string]*Entry{}
	put := func(id string, kind string, v ...string) {
		e := ent(id, kind)
		if kind == "fixed" {
			e.Answer = v[0]
		} else {
			e.Preferred = v
		}
		e.Placeholder = false
		es[id] = &e
	}
	put("address-street", "fixed", "1 Example Street")
	put("address-city", "fixed", "Sampleton")
	put("address-state", "pick-option", "Example State (ES)")
	put("address-zip", "fixed", "12345")
	put("address-country", "pick-option", "Exampleland")
	put("email-address", "fixed", "alex@example.com")
	put("phone-mobile", "fixed", "15555550100")
	for _, m := range mods {
		m(es)
	}
	var out []Entry
	for _, id := range []string{"address-street", "address-line2", "address-city", "address-state", "address-zip", "address-country", "email-address", "phone-mobile"} {
		if e, ok := es[id]; ok {
			out = append(out, *e)
		}
	}
	return out
}

func without(ids ...string) func(map[string]*Entry) {
	return func(es map[string]*Entry) {
		for _, id := range ids {
			delete(es, id)
		}
	}
}

func askMe(id string) func(map[string]*Entry) {
	return func(es map[string]*Entry) {
		if e, ok := es[id]; ok {
			if e.Kind == KindFixed {
				e.Answer = AskMe
			} else {
				e.Preferred = []string{AskMe}
			}
		}
	}
}

func TestAnswerBank_ApplicantContact_PhoneAndEmail(t *testing.T) {
	m := pickMatcher(addressBank()...)
	for _, c := range []struct{ q, want string }{
		{"What is the best phone number and email to reach you?", "Phone: +1 555-555-0100; Email: alex@example.com"},
		{"How can we contact you?", "Phone: +1 555-555-0100; Email: alex@example.com"},
		{"What is the best email to reach you?", "Email: alex@example.com"},
		{"Your contact information", "Phone: +1 555-555-0100; Email: alex@example.com"},
	} {
		r := matchReq(m, textQ("c", c.q))
		if r.Answer != c.want || r.Source != SourceComposite || r.Outcome != OutcomeFilled {
			t.Errorf("%q: %+v", c.q, r)
		}
	}
	// Not the applicant's own contact: a preference, consent, timing or someone else's.
	for _, q := range []string{
		"What is the best time to contact you?",
		"Do you consent to be contacted by phone or email?",
		"Please provide an emergency contact phone and email",
		"What is your preferred method to reach you by phone or email?",
	} {
		if r := matchReq(m, textQ("c", q)); r.Source == SourceComposite {
			t.Errorf("%q must not be a contact composite: %+v", q, r)
		}
	}
	// A channel the question names but the bank lacks: nothing is typed.
	r := matchReq(pickMatcher(addressBank(without("phone-mobile"))...), textQ("c", "What is the best phone number and email to reach you?"))
	wantAbstain(t, r, ReasonPlaceholderEmpty, "")
	// "How can we contact you" is answered with whichever channel exists.
	r = matchReq(pickMatcher(addressBank(without("phone-mobile"))...), textQ("c", "How can we contact you?"))
	if r.Answer != "Email: alex@example.com" {
		t.Errorf("%+v", r)
	}
	// The ask-me marker is the terminal ask-me.
	r = matchReq(pickMatcher(addressBank(askMe("phone-mobile"))...), textQ("c", "What is the best phone number and email to reach you?"))
	if r.Reason != ReasonAskMe || !r.Terminal {
		t.Errorf("%+v", r)
	}
	// A weaker entry caps the composite.
	capped := addressBank(func(es map[string]*Entry) { es["email-address"].NeedsReview = true })
	if r := matchReq(pickMatcher(capped...), textQ("c", "What is the best phone number and email to reach you?")); r.Outcome != OutcomeFilledReview || r.Confidence != FuzzyConfidenceCap {
		t.Errorf("the composite takes the lowest cap of the entries it read: %+v", r)
	}
}

func TestAnswerBank_ApplicantContact_WholeAddress(t *testing.T) {
	m := pickMatcher(addressBank()...)
	r := matchReq(m, textQ("a", "What is your current address?"))
	if r.Answer != "1 Example Street, Sampleton, ES 12345, Exampleland" || r.Source != SourceComposite {
		t.Fatalf("%+v", r)
	}
	// A line 2 joins the street.
	l2 := addressBank(func(es map[string]*Entry) {
		e := ent("address-line2", "fixed", withAnswer("Unit 4"))
		es["address-line2"] = &e
	})
	if r := matchReq(pickMatcher(l2...), textQ("a", "Home address")); !strings.HasPrefix(r.Answer, "1 Example Street, Unit 4, Sampleton") {
		t.Fatalf("%+v", r)
	}
	if r := matchReq(pickMatcher(l2...), textQ("a", "What is your mailing address?")); !strings.HasPrefix(r.Answer, "1 Example Street, Unit 4, Sampleton") {
		t.Fatalf("%+v", r)
	}
	// CoreWeave's Greenhouse "Legal Address" and Hiya's "Please confirm your
	// address" are the home address.
	for _, q := range []string{"Legal Address", "Address Please confirm your address this is for NDA and employment contract purposes."} {
		if r := matchReq(m, textQ("a", q)); r.Answer != "1 Example Street, Sampleton, ES 12345, Exampleland" {
			t.Fatalf("%q: %+v", q, r)
		}
	}
	// Missing street, city or state: abstain rather than answer part of it.
	for _, id := range []string{"address-street", "address-city", "address-state"} {
		wantAbstain(t, matchReq(pickMatcher(addressBank(without(id))...), textQ("a", "What is your current address?")), ReasonPlaceholderEmpty, "")
	}
	// Questions about the address, other people's addresses and entity sections are not this.
	for _, q := range []string{"Is your current address the same as your mailing address?", "What is your employer's address?", "How long have you lived at your current address?", "What is your email address?"} {
		if r := matchReq(m, textQ("a", q)); r.Source == SourceComposite {
			t.Errorf("%q: %+v", q, r)
		}
	}
	in := textQ("a", "What is your current address?")
	in.SectionPath = []string{"Work Experience", "Employer 1"}
	if r := matchReq(m, in); r.Source == SourceComposite {
		t.Errorf("never inside an entity section: %+v", r)
	}
	// The profile address fills a part the bank leaves blank.
	r = matchReq(pickMatcher(addressBank(without("address-street", "address-country"))...), textQ("a", "What is your current address?"), func(r *Request) {
		r.Facts.Address = AddressFact{Line1: "9 Profile Road", Country: "US"}
	})
	if r.Answer != "9 Profile Road, Sampleton, ES 12345, United States" {
		t.Errorf("%+v", r)
	}
}

// Expedia (Workday): "Please enter your current Address City" names one part of
// the address, so a line 2 left to the person does not hold it back.
func TestAnswerBank_WholeAddress_AQuestionNamingOnePartIsThatPart(t *testing.T) {
	l2 := addressBank(func(es map[string]*Entry) {
		e := ent("address-line2", "fixed", withAnswer(AskMe))
		es["address-line2"] = &e
	})
	m := pickMatcher(l2...)
	if r := matchReq(m, textQ("a", "Please enter your current Address City")); r.Answer != "Sampleton" {
		t.Errorf("city: %+v", r)
	}
	if r := matchReq(m, textQ("a", "Please enter your current Address State")); r.Answer != "Example State" {
		t.Errorf("state: %+v", r)
	}
	// The whole address still waits for the person's line 2.
	wantAbstain(t, matchReq(m, textQ("a", "What is your current address?")), ReasonAskMe, "")
}

// CoreWeave's "Legal Address": the second address line is optional, so the
// shipped ask-me placeholder on it never holds the whole address back; the
// profile's line 2, when there is one, takes its place.
func TestAnswerBank_WholeAddress_TheShippedLine2PlaceholderIsNoLine2(t *testing.T) {
	shipped := addressBank(func(es map[string]*Entry) {
		e := ent("address-line2", "fixed", withAnswer(AskMe))
		e.Placeholder = true
		es["address-line2"] = &e
	})
	m := pickMatcher(shipped...)
	if r := matchReq(m, textQ("a", "Legal Address")); r.Answer != "1 Example Street, Sampleton, ES 12345, Exampleland" {
		t.Errorf("no line 2: %+v", r)
	}
	r := matchReq(m, textQ("a", "Legal Address"), func(r *Request) { r.Facts.Address = AddressFact{Line2: "Unit 4"} })
	if !strings.HasPrefix(r.Answer, "1 Example Street, Unit 4, Sampleton") {
		t.Errorf("the profile's line 2: %+v", r)
	}
	// A required part's placeholder still holds it.
	street := addressBank(func(es map[string]*Entry) {
		es["address-street"].Answer = AskMe
		es["address-street"].Placeholder = true
	})
	wantAbstain(t, matchReq(pickMatcher(street...), textQ("a", "Legal Address")), ReasonAskMe, "")
}

func TestAnswerBank_CompositeLocation_CityStateCountry(t *testing.T) {
	m := pickMatcher(addressBank()...)
	for _, c := range []struct{ q, want string }{
		{"In which city, province/state and country are you currently located?", "Sampleton, Example State, Exampleland"},
		{"City and country of residence", "Sampleton, Exampleland"},
		{"Country and city", "Sampleton, Exampleland"},
	} {
		r := matchReq(m, textQ("l", c.q))
		if r.Answer != c.want || r.Source != SourceComposite {
			t.Errorf("%q: %+v", c.q, r)
		}
	}
	// Missing city or country: abstain, never hand the question to the country route.
	for _, id := range []string{"address-city", "address-country"} {
		r := matchReq(pickMatcher(addressBank(without(id))...), textQ("l", "In which city and country are you located?"))
		wantAbstain(t, r, ReasonPlaceholderEmpty, "")
	}
	// A single-component field stays on its own route.
	if r := matchReq(m, textQ("l", "City")); r.Source == SourceComposite {
		t.Errorf("%+v", r)
	}
}

// A residence menu that names a few countries and gathers the rest under one
// option is answered from the country entry: its country when listed, else
// the rest-of-world option. The word "Language" in the label does not send it
// to the language entry, and a menu with another kind of option is not guessed.
func TestAnswerBank_ResidenceMenu_CountryOrRestOfWorld(t *testing.T) {
	lives := func(country string) []Entry {
		return append(addressBank(func(es map[string]*Entry) { es["address-country"].Preferred = []string{country} }),
			ent("language-proficiency", "language-proficiency"))
	}
	const label = "Location of Residence and Language:"
	menu := func(labels ...string) Question {
		return required(selectQ(label, append([]string{"Select your location of residence and language"}, labels...)...))
	}
	wantFill(t, matchReq(pickMatcher(lives("Canada")...), menu("Germany", "Rest of World")), "Rest of World", OutcomeFilled)
	wantFill(t, matchReq(pickMatcher(lives("Germany")...), menu("Germany", "Rest of World")), "Germany", OutcomeFilled)
	if r := matchReq(pickMatcher(lives("Canada")...), menu("Germany", "Contractors", "Rest of World")); r.Outcome == OutcomeFilled || r.Outcome == OutcomeFilledReview {
		t.Fatalf("a menu with a non-country option is not guessed: %+v", r)
	}
}

// "Where are you located?" in a free-text box is the applicant's place: the
// city and state code in the United States, the city and country elsewhere.
// "In which country will you work?" is the user's own work-country answer,
// else the country of residence.
func TestAnswerBank_PlaceAsks_LocatedAndWorkCountry(t *testing.T) {
	us := addressBank(func(es map[string]*Entry) {
		es["address-state"].Preferred = []string{"North Carolina (NC)"}
		es["address-country"].Preferred = []string{"United States"}
	})
	abroad := addressBank()
	for _, c := range []struct {
		bank    []Entry
		q, want string
	}{
		{us, "Where are you located?", "Sampleton, NC"},
		{us, "Where are you based? *", "Sampleton, NC"},
		// Arcadia (Lever): the country named in the ask.
		{us, "Where in the United States are you located?", "Sampleton, NC"},
		{us, "Where in the US are you based?", "Sampleton, NC"},
		{abroad, "Where are you located?", "Sampleton, Exampleland"},
		{us, "What is your location?", "Sampleton, NC"},
		{us, "Your location", "Sampleton, NC"},
		// Lattice (Greenhouse, live): the ask as a request, a reason after it.
		{us, "Please share where you are located to help shape our potential conversation.", "Sampleton, NC"},
		{us, "Please let us know where you are based.", "Sampleton, NC"},
		{us, "In which country will you work?", "United States"},
		{us, "Which country will you be working from?", "United States"},
		{abroad, "What country would you be working in?", "Exampleland"},
		{append(us, ent("work-country", "fixed", withAnswer("Canada"))), "In which country will you work?", "Canada"},
	} {
		r := matchReq(pickMatcher(c.bank...), textQ("l", c.q))
		if r.Answer != c.want || r.Source != SourceComposite || r.Outcome != OutcomeFilled {
			t.Errorf("%q: want %q, got %+v", c.q, c.want, r)
		}
	}
	// A US place without its state abstains rather than answer the city alone.
	noState := addressBank(without("address-state"), func(es map[string]*Entry) { es["address-country"].Preferred = []string{"United States"} })
	wantAbstain(t, matchReq(pickMatcher(noState...), textQ("l", "Where are you located?")), ReasonPlaceholderEmpty, "")
	// Option lists, arrangement, preference, relocation and authorization
	// wordings are not these answers.
	m := pickMatcher(us...)
	for _, q := range []Question{
		textQ("l", "Where are you located? This role is remote within the US."),
		textQ("l", "Which country will you be working from: remote, hybrid or in the office?"),
		textQ("l", "In which country would you prefer to work?"),
		textQ("l", "What is your location preference?"),
		textQ("l", "In which country do you want to work?"),
		textQ("l", "Which country will you be working from if you relocate?"),
		textQ("l", "Which country will you be working from, and are you authorized to work there?"),
		textQ("l", "Are you located in the United States?"),
		textQ("l", "Please share where you are located and whether you are open to relocation."),
	} {
		if r := matchReq(m, q); r.Source == SourceComposite {
			t.Errorf("%q is not a place answer: %+v", q.Text, r)
		}
	}
}

// A current-place question over a list of places, each a city or a whole
// country (Ema, Ashby): the row naming the recorded city, else the one row
// that is the recorded country as a whole; another city in the country is no
// row of the applicant's, and two whole-country rows leave the choice.
func TestAnswerBank_PlaceAsks_LocatedOverAListOfPlaces(t *testing.T) {
	us := addressBank(func(es map[string]*Entry) {
		es["address-state"].Preferred = []string{"North Carolina (NC)"}
		es["address-country"].Preferred = []string{"United States"}
	})
	m := pickMatcher(us...)
	for _, c := range []struct {
		q    Question
		want string
	}{
		{radioQ("Where are you located?", "US Remote", "Mountain View, California", "India Remote", "Bengaluru, India"), "US Remote"},
		{radioQ("Where are you located?", "US Remote", "Sampleton, North Carolina", "India Remote"), "Sampleton, North Carolina"},
		{selectQ("Where are you located?", "United States", "Canada", "Other"), "United States"},
		{selectQ("Where are you currently based?", "Remote - United States", "London, UK"), "Remote - United States"},
	} {
		r := matchReq(m, c.q)
		if r.Outcome != OutcomeFilled || r.ChosenLabel != c.want {
			t.Errorf("%q %v: want %q, got %+v", c.q.Text, c.q.Options, c.want, r)
		}
	}
	for _, q := range []Question{
		radioQ("Where are you located?", "US Remote", "United States", "Canada"),
		radioQ("Where are you located?", "Mountain View, California", "Bengaluru, India"),
		radioQ("Where is the company located?", "United States", "Canada"),
	} {
		if r := matchReq(m, q); r.Outcome == OutcomeFilled && r.Source == SourceComposite {
			t.Errorf("%q %v: want no place answer, got %+v", q.Text, q.Options, r)
		}
	}
}

func TestAnswerBank_CompositeAddress_AtomRunAndOrder(t *testing.T) {
	m := pickMatcher(addressBank()...)
	for _, c := range []struct{ q, want string }{
		{"City, State, Zip Code", "Sampleton, ES 12345"},
		{"Street Address, City", "1 Example Street, Sampleton"},
		{"Zip, City", "12345, Sampleton"},
		{"City / State / Country", "Sampleton, Example State, Exampleland"},
	} {
		r := matchReq(m, textQ("c", c.q))
		if r.Answer != c.want || r.Source != SourceComposite {
			t.Errorf("%q: %+v", c.q, r)
		}
	}
	// A contaminated glued label: only the leading adjacent atoms count.
	r := matchReq(m, textQ("c", "City (Exampleland) Legal First Name Please complete your information State (Exampleland)"))
	if r.Answer == "Sampleton, ES" || strings.Contains(r.Answer, "ES") {
		t.Errorf("no state may be appended to a city control: %+v", r)
	}
	// "country/region" is one atom.
	if atoms := namedAddressAtoms("What country/region are you currently located?"); len(atoms) != 1 {
		t.Errorf("country/region is one atom: %+v", atoms)
	}
	// A missing atom abstains.
	wantAbstain(t, matchReq(pickMatcher(addressBank(without("address-zip"))...), textQ("c", "City, State, Zip Code")), ReasonPlaceholderEmpty, "")
}

func TestAnswerBank_Location_CityStateCatalogTypeahead(t *testing.T) {
	m := pickMatcher(addressBank()...)
	many := make([]string, 100)
	for i := range many {
		many[i] = "Town " + strconv.Itoa(i) + ", ES"
	}
	q := Question{Selector: "t", Text: "Your Location (City, State)", Kind: KindCustomListbox, Options: opts(many...)}
	r := matchReq(m, q)
	if r.Answer != "Sampleton, Example State, Exampleland" || r.AnswerKey != "address_city" || r.Source != SourceComposite {
		t.Fatalf("%+v", r)
	}
	q.Options = nil
	if r := matchReq(m, q); r.AnswerKey != "address_city" {
		t.Errorf("no options: %+v", r)
	}
	// A short list stays on the option-picking route.
	q.Options = opts("Sampleton, ES", "Othertown, ES")
	if r := matchReq(m, q); r.AnswerKey == "address_city" && r.Source == SourceComposite {
		t.Errorf("a short list is not a catalog: %+v", r)
	}
	// Any part missing: not part of the place.
	wantAbstain(t, matchReq(pickMatcher(addressBank(without("address-state"))...), Question{Selector: "t", Text: "Your Location (City, State)", Kind: KindCustomListbox}), ReasonPlaceholderEmpty, "")
}

// Propel (Ashby): a "where are you currently based" search that shows nothing
// until typed, whose question names the states the employer hires in, and not
// the recorded one, also searches for the list's catch-all "Other" when the
// place itself is not found; a listed home state adds nothing.
func TestAnswerBank_Location_CurrentPlaceOutsideTheNamedStates(t *testing.T) {
	sc := func(es map[string]*Entry) {
		es["address-city"].Answer = "Charleston"
		es["address-state"].Preferred = []string{"South Carolina (SC)"}
		es["address-country"].Preferred = []string{"United States"}
	}
	const based = "Where are you currently based? We're currently accepting applications from the states we have an established entity in, which includes California, Colorado, District of Columbia, Florida, Georgia, Illinois, Maryland, Massachusetts, New Jersey, New York, Oregon, Pennsylvania, Texas, Virginia, and Washington."
	q := Question{Selector: "t", Text: based, Kind: KindCustomListbox}
	r := matchReq(pickMatcher(addressBank(sc)...), q)
	if r.Answer != "Charleston, South Carolina, United States" || !slices.Equal(r.Alternatives, []string{"Other"}) {
		t.Fatalf("%+v", r)
	}
	q.Text = strings.Replace(based, "Oregon", "South Carolina", 1)
	if r := matchReq(pickMatcher(addressBank(sc)...), q); len(r.Alternatives) != 0 {
		t.Fatalf("a listed home state: %+v", r)
	}
}

// A "where are you currently based" place search (Ashby's location field) has
// no country-only row, so it is seeded with the city, not the country entry.
func TestAnswerBank_Location_CurrentPlaceCatalogTypeahead(t *testing.T) {
	country := ent("current-based-country", "fixed")
	country.Answer, country.Placeholder = "Exampleland", false
	m := pickMatcher(append(addressBank(), country)...)
	q := Question{Selector: "t", Text: "Current location Where are you currently based?", Kind: KindCustomListbox}
	if r := matchReq(m, q); r.Answer != "Sampleton, Example State, Exampleland" || r.AnswerKey != "address_city" {
		t.Fatalf("%+v", r)
	}
	// TypeSafe (Ashby, live): a statement about the role's office before the
	// question does not make it another place than the applicant's own.
	led := q
	led.Text = "Current location TypeSafe works in-office full time in downtown San Francisco. Where are you currently based?"
	if r := matchReq(m, led); r.Answer != "Sampleton, Example State, Exampleland" || r.AnswerKey != "address_city" {
		t.Fatalf("led by a statement: %+v", r)
	}
	// Headway (Ashby, live): "current working location", the metro area its
	// description asks for, is the applicant's own place.
	work := q
	work.Text = "Please enter your current working location Please select your nearest metropolitan area"
	if r := matchReq(m, work); r.Answer != "Sampleton, Example State, Exampleland" || r.AnswerKey != "address_city" {
		t.Fatalf("current working location: %+v", r)
	}
	// Helion (Ashby, live): "residing", asked as City, State, Country, is the
	// applicant's place, not the state alone.
	residing := q
	residing.Text = "Where are you currently residing? Please list it as a City, State, Country"
	if r := matchReq(m, residing); r.Answer != "Sampleton, Example State, Exampleland" || r.AnswerKey != "address_city" {
		t.Fatalf("residing: %+v", r)
	}
	residing.Text = "Which city and state are you residing in?"
	if r := matchReq(m, residing); r.Answer != "Sampleton, Example State, Exampleland" || r.AnswerKey != "address_city" {
		t.Fatalf("city and state residing: %+v", r)
	}
	led.Text = "Current location TypeSafe works in-office full time. Where would you prefer to work from?"
	if r := matchReq(m, led); r.AnswerKey == "address_city" && r.Source == SourceComposite {
		t.Errorf("a preference asked after the statement: %+v", r)
	}
	// A free-text box keeps the country entry's answer; a short list is not a search.
	q.Kind = KindText
	if r := matchReq(m, q); r.AnswerKey == "address_city" {
		t.Errorf("text box: %+v", r)
	}
	q.Kind, q.Options = KindCustomListbox, opts("Exampleland", "Otherland")
	if r := matchReq(m, q); r.AnswerKey == "address_city" && r.Source == SourceComposite {
		t.Errorf("a short list is not a catalog: %+v", r)
	}
	// Another place than the applicant's own stays off it.
	q.Options = nil
	for _, text := range []string{"Preferred office location", "Where are you willing to relocate?"} {
		q.Text = text
		if r := matchReq(m, q); r.AnswerKey == "address_city" && r.Source == SourceComposite {
			t.Errorf("%q: %+v", text, r)
		}
	}
}

func TestAnswerBank_Location_CityOptionDisambiguation(t *testing.T) {
	m := pickMatcher(addressBank()...)
	r := matchReq(m, selectQ("City", "Sampleton, Other State, Exampleland", "North Sampleton, Example State, Exampleland", "Sampleton, Example State, Exampleland"))
	if r.ChosenLabel != "Sampleton, Example State, Exampleland" {
		t.Fatalf("the recorded state is never changed; the exact city segment wins: %+v", r)
	}
	// With only a longer city carrying the state, it is still the state-consistent one.
	r = matchReq(m, selectQ("City", "Sampleton, Other State, Exampleland", "North Sampleton, Example State, Exampleland"))
	if r.ChosenLabel != "North Sampleton, Example State, Exampleland" {
		t.Fatalf("%+v", r)
	}
	// The state code between spaces also qualifies.
	r = matchReq(m, selectQ("City", "Sampleton ES Exampleland", "Sampleton XX Exampleland"))
	if r.ChosenLabel != "Sampleton ES Exampleland" {
		t.Fatalf("%+v", r)
	}
}

func TestAnswerBank_Location_PostalCodeList(t *testing.T) {
	// The default bank's shape: the postal code is a text entry.
	zipText := func(es map[string]*Entry) { es["address-zip"].InputTypes = []string{string(InputText)} }
	list := func(labels ...string) Question {
		return Question{Selector: "z", Text: "Postal Code", Kind: KindCustomListbox, InputType: InputSingleSelect, Required: true, Options: opts(labels...)}
	}
	m := pickMatcher(addressBank(zipText)...)
	// Chase (Oracle) lists the city's postal codes, each opening its option.
	r := matchReq(m, list("12344, Sampleton, Example County, ES", "12345, Sampleton, Example County, ES", "12346, Sampleton, Example County, ES"))
	if r.Outcome == OutcomeAbstained || r.ChosenLabel != "12345, Sampleton, Example County, ES" {
		t.Fatalf("the option opening with the recorded code: %+v", r)
	}
	// A list without the recorded code is never answered with a neighbour.
	wantAbstain(t, matchReq(m, list("12344, Sampleton, ES", "12346, Sampleton, ES")), ReasonInputMismatch, "")
	// A ZIP+4 is read by its ZIP.
	plus4 := pickMatcher(addressBank(zipText, func(es map[string]*Entry) { es["address-zip"].Answer = "12345-6789" })...)
	if r := matchReq(plus4, list("12344, Sampleton, ES", "12345, Sampleton, ES")); r.ChosenLabel != "12345, Sampleton, ES" {
		t.Fatalf("ZIP+4: %+v", r)
	}
}
