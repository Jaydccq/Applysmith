package match

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Address, contact and location composites (step 4, ContactComposite). The
// answers are assembled from the bank's address and contact entries, falling
// back to the profile address for a part the bank leaves blank, with source
// composite, at the lowest confidence cap of the entries they read. They abstain
// when a part is missing, abstain with the terminal ask-me when a part holds the
// ask-me marker, and are never given inside an entity section.

var (
	yesNoOpenerRE = lazyregexp.New(`(?i)^\s*(?:are|is|do|does|did|can|could|would|will|may|have|has|shall|should)\b`)

	fullAddressRE    = lazyregexp.New(`(?i)\b(?:current|home|mailing|residential|permanent|full|physical|postal|legal|primary|residence)\s+(?:street\s+|mailing\s+|home\s+|residence\s+)?address\b|\bwhat\s+is\s+your\s+address\b|\b(?:confirm|provide|enter)\s+your\s+(?:full\s+)?address\b`)
	notHomeAddressRE = lazyregexp.New(`(?i)\b(?:e-?mail|ip|web|wallet|employer|company|school|business|work|office|billing|reference|referee|emergency)\b|\bhow\s+long\b|\bsince\b|\bsame\s+as\b`)

	contactPhoneRE = lazyregexp.New(`(?i)\b(?:phone|mobile|cell|telephone)\b`)
	contactEmailRE = lazyregexp.New(`(?i)\be-?mail\b`)
	reachYouRE     = lazyregexp.New(`(?i)\b(?:reach|contact)\s+you\b|\bbest\s+way\s+to\s+(?:reach|contact)\b|\byour\s+contact\s+(?:info|information|details)\b`)
	notOwnContact  = lazyregexp.New(`(?i)\b(?:prefer|preferred|preference|consent|opt|receive|alerts?|newsletter|marketing|method|hear\s+about|text\s+messages?|sms|whatsapp|emergency|references?|referees?|employer|manager|supervisor|when|times?|days?|hours?|availability|available)\b`)

	// notOwnSubject keeps a location composite off a question about someone
	// else's place or about a preference.
	notOwnSubject = lazyregexp.New(`(?i)\b(?:prefer\w*|willing|relocat\w*|desired|employer|company|school|university|college|business|office|reference|referee|emergency|manager|supervisor|hiring)\b`)

	compositeLocationRE   = lazyregexp.New(`(?i)\bcity\b[^?!]*\b(?:country|nation)\b|\b(?:country|nation)\b[^?!]*\bcity\b`)
	compositeWantsStateRE = lazyregexp.New(`(?i)\b(?:state|province|region)\b`)
	countryRegionIdiomRE  = lazyregexp.New(`(?i)\bcountry\s*(?:/|\bor\b|&)\s*region\b|\bregion\s*(?:/|\bor\b|&)\s*country\b`)
	atomGapRE             = lazyregexp.New(`(?i)^[\s,;:/|&\x{00B7}()*\-\x{2013}\x{2014}]*(?:\b(?:and|or)\b[\s,;:/|&\x{00B7}()*\-\x{2013}\x{2014}]*)?$`)

	cityStateRE = lazyregexp.New(`(?i)\bcity\s*(?:,|/|and|&)\s*state\b`)
	// currentPlaceRE is the applicant's present place on a place search:
	// "Current location", "Where are you currently based?" (Ashby's location
	// field offers only places, never a bare country).
	currentPlaceRE  = lazyregexp.New(`(?i)\bcurrent\s+(?:work(?:ing)?\s+|home\s+)?location\b|\bwhere\s+are\s+you\s+(?:currently\s+|presently\s+|now\s+)?(?:based|located|living|residing)\b`)
	asksLocationRE  = lazyregexp.New(`(?i)\b(?:location|located|based|resid(?:e|ing)|live)\b`)
	regionWordOnlyE = lazyregexp.New(`(?i)^region$`)

	// locatedAskRE is a free-text "Where are you located?", "Where are you
	// based?" or "What is your location?". The city route (508) leaves located
	// wording alone because its option lists and eligibility sentences are not
	// the city field; a "currently" wording is the country entry's (route 119).
	// The ask may be a request (Lattice: "Please share where you are located
	// to help shape our potential conversation."), or name the country it is
	// within (Arcadia: "Where in the United States are you located?").
	locatedAskRE = lazyregexp.New(`(?i)^\W*(?:(?:please\s+)?(?:share|let\s+us\s+know)\s+where\s+you\s+are\s+(?:located|based)\b|where\s+(?:in\s+the\s+(?:united\s+states|u\.?s\.?a?\.?)\s+)?are\s+you\s+(?:located|based)\b|what\s+is\s+your\s+(?:current\s+)?location\b|your\s+(?:current\s+)?location\W*$)`)
	// workCountryAskRE is a free-text question about the country the applicant
	// will work from: "In which country will you work?", "Which country will
	// you be working from?".
	workCountryAskRE = lazyregexp.New(`(?i)^\W*(?:(?:in|from)\s+)?(?:which|what)\s+country\s+(?:will|would)\s+you\s+(?:be\s+)?work(?:ing)?\b`)
	// placeAskVetoRE keeps an arrangement (remote, hybrid, office, outside), a
	// preference or a work-authorization wording away from the place answers.
	placeAskVetoRE = lazyregexp.New(`(?i)\b(?:remote(?:ly)?|hybrid|on[- ]?site|in[- ](?:person|office)|outside|want|wish|open\s+to|authori[sz]\w*|sponsor\w*|visa|citizen\w*)\b`)
	// addressedRE is a sentence that speaks to the applicant or asks them for
	// something.
	addressedRE = lazyregexp.New(`(?i)\b(?:you|your|yours|yourself|please|share|tell|provide|describe|list|let\s+us\s+know)\b`)
)

// catalogOptionCount is the grounded lane's cap: past it, no option list is
// answered, so the typeahead's menu is a catalog the client searches.
const catalogOptionCount = 20

var addressAtoms = []struct {
	atom, id string
	re       *lazyregexp.Regexp
}{
	{"street", "address-street", lazyregexp.New(`(?i)\b(?:street\s*address|address\s*line\s*1|street)\b`)},
	{"city", "address-city", lazyregexp.New(`(?i)\bcity\b`)},
	{"state", "address-state", compositeWantsStateRE},
	{"zip", "address-zip", lazyregexp.New(`(?i)\b(?:zip(?:\s*code)?|postal\s*code|post\s*code|postcode|cep)\b`)},
	{"country", "address-country", lazyregexp.New(`(?i)\b(?:country|nation)\b`)},
}

// addrPart is one value a composite reads.
type addrPart struct {
	text   string
	askMe  bool
	entry  *Entry
	source string
}

// partOf reads the entry's own text, else the profile's value for the part.
func partOf(s *Step, id string) addrPart {
	e := entryNamed(s, id)
	// An optional part (the second address line) whose ask-me is still the
	// shipped placeholder gives way to the profile, which most often has none.
	if e != nil && e.AsksMe() && !(e.Placeholder && slices.Contains(optionalIDs, id)) {
		return addrPart{askMe: true, entry: e}
	}
	if t := ownText(e); t != "" {
		return addrPart{text: t, entry: e, source: "bank"}
	}
	a := s.Req.Facts.Address
	var t string
	switch id {
	case "address-street":
		t = a.Line1
	case "address-line2":
		t = a.Line2
	case "address-city":
		t = a.City
	case "address-county":
		t = a.County
	case "address-state":
		t = a.Region
	case "address-zip":
		t = a.PostalCode
	case "address-country":
		t = countryName(a.Country)
	}
	return addrPart{text: strings.TrimSpace(t), entry: e, source: "profile"}
}

// stateCodeOfEntry reads the two-letter code of the state entry from any of its
// ranked values; the real bank ranks both "Name" and "Name (XX)".
func stateCodeOfEntry(s *Step) string {
	if e := entryNamed(s, "address-state"); e != nil {
		for _, v := range append(append([]string(nil), e.Preferred...), e.FallbackText, e.Answer) {
			if c := stateCodeOf(v); c != "" {
				return c
			}
		}
	}
	return stateCodeOf(s.Req.Facts.Address.Region)
}

// stateText renders the state for its context: a postal line (a zip is named, or
// no country is) wants the code, a prose line the name.
func stateText(s *Step, part addrPart, postal bool) string {
	name := stateProse(part.text)
	if postal {
		if code := stateCodeOfEntry(s); code != "" {
			return code
		}
	}
	return name
}

// compositeStart is the starting confidence of a composite: the lowest cap of
// the entries it read.
func compositeStart(parts ...addrPart) float64 {
	start := startWalkEnd
	for _, p := range parts {
		e := p.entry
		if e == nil {
			continue
		}
		if e.Confidence != nil && *e.Confidence < start {
			start = ClampConfidence(*e.Confidence)
		}
		if (e.NeedsReview || e.stale()) && start > FuzzyConfidenceCap {
			start = FuzzyConfidenceCap
		}
	}
	return start
}

// compositeDraft builds the draft of an assembled answer.
func compositeDraft(answer string, entry *Entry, trace string, parts ...addrPart) Draft {
	return Draft{Entry: entry, Answer: answer, Source: SourceComposite, Start: compositeStart(parts...),
		Deterministic: true, Trace: []string{trace}}
}

// compositeBlock reports the abstain of a composite that cannot be assembled: a
// part holding the ask-me marker is the terminal ask-me, a missing part is a
// placeholder-empty abstain.
func compositeBlock(what string, parts map[string]addrPart, order []string) (Draft, bool) {
	for _, id := range order {
		if p := parts[id]; p.askMe {
			return abstainDraft(ReasonAskMe, "", "%s: %s holds the ask-me marker", what, id), true
		}
	}
	for _, id := range order {
		if parts[id].text == "" {
			return abstainDraft(ReasonPlaceholderEmpty, "", "%s: %s is missing -> abstain rather than answer part of it", what, id), true
		}
	}
	return Draft{}, false
}

// compositeEligible is the common guard: a free-text box outside an entity
// section that is not a yes/no, preference or other-subject question.
func compositeEligible(q Question) bool {
	return q.Kind == KindText && !hasRenderedOptions(q.Options) && classifySection(q.SectionPath) == sectionNone &&
		!yesNoOpenerRE.MatchString(q.Text) && !isNarrativeText(q.Text)
}

// usPhoneDisplay shows an 11-digit number starting with 1 as +1 NNN-NNN-NNNN and
// any other number as stored.
func usPhoneDisplay(phone string) string {
	var digits strings.Builder
	for _, r := range phone {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	d := digits.String()
	if len(d) == 11 && d[0] == '1' {
		return "+1 " + d[1:4] + "-" + d[4:7] + "-" + d[7:]
	}
	return strings.TrimSpace(phone)
}

// applicantContact answers "best phone and email to reach you" with
// "Phone: <phone>; Email: <email>".
func applicantContact(s *Step) (Draft, bool) {
	q := s.Q
	if !compositeEligible(q) || notOwnContact.MatchString(q.Text) {
		return Draft{}, false
	}
	asksPhone, asksEmail := contactPhoneRE.MatchString(q.Text), contactEmailRE.MatchString(q.Text)
	if !(asksPhone && asksEmail || reachYouRE.MatchString(q.Text)) {
		return Draft{}, false
	}
	wantsPhone, wantsEmail := asksPhone || !asksEmail, asksEmail || !asksPhone
	parts := map[string]addrPart{
		"phone-mobile":  {text: entryText(s, "phone-mobile"), entry: entryNamed(s, "phone-mobile")},
		"email-address": {text: entryText(s, "email-address"), entry: entryNamed(s, "email-address")},
	}
	for id, p := range parts {
		if p.entry != nil && p.entry.AsksMe() {
			parts[id] = addrPart{askMe: true, entry: p.entry}
		}
	}
	phone, email := parts["phone-mobile"], parts["email-address"]
	if phone.askMe && wantsPhone || email.askMe && wantsEmail {
		return abstainDraft(ReasonAskMe, "", "applicant contact: a channel holds the ask-me marker"), true
	}
	// A channel the question names must be answerable; "how can we contact you"
	// is answered with whichever channel there is.
	if asksPhone && phone.text == "" || asksEmail && email.text == "" || phone.text == "" && email.text == "" {
		return abstainDraft(ReasonPlaceholderEmpty, "", "applicant contact: the bank lacks a channel the question names -> abstain"), true
	}
	var out []string
	var used []addrPart
	if wantsPhone && phone.text != "" {
		out, used = append(out, "Phone: "+usPhoneDisplay(phone.text)), append(used, phone)
	}
	if wantsEmail && email.text != "" {
		out, used = append(out, "Email: "+email.text), append(used, email)
	}
	return compositeDraft(strings.Join(out, "; "), nil, "applicant contact: phone and email from the bank", used...), true
}

// wholeAddress answers "what is your current address" with the whole address;
// "your current address state" names one part and is answered with that part.
func wholeAddress(s *Step) (Draft, bool) {
	q := s.Q
	if !compositeEligible(q) || !fullAddressRE.MatchString(q.Text) || notHomeAddressRE.MatchString(q.Text) {
		return Draft{}, false
	}
	if named := namedAddressAtoms(q.Text); len(named) == 1 {
		return addressPart(s, named[0])
	}
	ids := []string{"address-street", "address-line2", "address-city", "address-state", "address-zip", "address-country"}
	parts := map[string]addrPart{}
	for _, id := range ids {
		parts[id] = partOf(s, id)
	}
	if d, blocked := compositeBlock("whole address", parts, []string{"address-street", "address-city", "address-state"}); blocked {
		return d, true
	}
	for _, id := range ids {
		if parts[id].askMe {
			return abstainDraft(ReasonAskMe, "", "whole address: %s holds the ask-me marker", id), true
		}
	}
	street := parts["address-street"].text
	if l2 := parts["address-line2"].text; l2 != "" {
		street += ", " + l2
	}
	state := stateText(s, parts["address-state"], true)
	if zip := parts["address-zip"].text; zip != "" {
		state += " " + zip
	}
	out := []string{street, parts["address-city"].text, state}
	if c := parts["address-country"].text; c != "" {
		out = append(out, c)
	}
	var used []addrPart
	for _, id := range ids {
		used = append(used, parts[id])
	}
	return compositeDraft(strings.Join(out, ", "), nil, "whole address composed from the street, city, state, postal code and country", used...), true
}

// addressPart answers a question about one part of the current address with
// that part alone, the state in prose; only that part's own ask-me holds it.
func addressPart(s *Step, n namedAtom) (Draft, bool) {
	p := partOf(s, n.id)
	if d, blocked := compositeBlock("address part", map[string]addrPart{n.id: p}, []string{n.id}); blocked {
		return d, true
	}
	text := p.text
	if n.atom == "state" {
		text = stateProse(text)
	}
	return compositeDraft(text, p.entry, "address part: the "+n.atom+" of the current address", p), true
}

// compositeLocation answers a box naming city and country (and state, province
// or region) with the components in prose.
func compositeLocation(s *Step) (Draft, bool) {
	q := s.Q
	if !compositeEligible(q) || !compositeLocationRE.MatchString(q.Text) || notOwnSubject.MatchString(q.Text) {
		return Draft{}, false
	}
	parts := map[string]addrPart{}
	for _, id := range []string{"address-city", "address-state", "address-country"} {
		parts[id] = partOf(s, id)
	}
	if parts["address-state"].askMe {
		return abstainDraft(ReasonAskMe, "", "composite location: address-state holds the ask-me marker"), true
	}
	if d, blocked := compositeBlock("composite location", parts, []string{"address-city", "address-country"}); blocked {
		return d, true
	}
	out := []string{parts["address-city"].text}
	state := stateProse(parts["address-state"].text)
	if compositeWantsStateRE.MatchString(q.Text) && state != "" {
		out = append(out, state)
	}
	out = append(out, parts["address-country"].text)
	return compositeDraft(strings.Join(out, ", "), nil, "composite location: city, state and country in prose",
		parts["address-city"], parts["address-state"], parts["address-country"]), true
}

type namedAtom struct {
	atom, id string
	at, end  int
}

// namedAddressAtoms lists the address atoms a label names, in label order, and
// keeps only the leading run of adjacent atoms: a gap that carries words or
// digits means the next atom belongs to another field's label.
func namedAddressAtoms(text string) []namedAtom {
	idiom := countryRegionIdiomRE.MatchString(text)
	var found []namedAtom
	for _, a := range addressAtoms {
		loc := a.re.FindStringIndex(text)
		if loc == nil {
			continue
		}
		if a.atom == "state" && idiom && regionWordOnlyE.MatchString(text[loc[0]:loc[1]]) {
			continue
		}
		found = append(found, namedAtom{a.atom, a.id, loc[0], loc[1]})
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].at < found[j].at })
	if len(found) == 0 {
		return nil
	}
	run := found[:1]
	for i := 1; i < len(found); i++ {
		if !atomGapRE.MatchString(text[found[i-1].end:found[i].at]) {
			break
		}
		run = append(run, found[i])
	}
	return run
}

// compositeAddress answers a label naming two or more address parts with those
// parts in label order, the zip hugging the state.
func compositeAddress(s *Step) (Draft, bool) {
	q := s.Q
	if !compositeEligible(q) || compositeLocationRE.MatchString(q.Text) || notOwnSubject.MatchString(q.Text) {
		return Draft{}, false
	}
	named := namedAddressAtoms(q.Text)
	if len(named) < 2 {
		return Draft{}, false
	}
	hasAtom := func(name string) bool {
		for _, n := range named {
			if n.atom == name {
				return true
			}
		}
		return false
	}
	postal := hasAtom("zip") || !hasAtom("country")
	parts := map[string]addrPart{}
	var order, names, used = []string{}, []string{}, []addrPart{}
	for _, n := range named {
		parts[n.id] = partOf(s, n.id)
		order = append(order, n.id)
		names = append(names, n.atom)
	}
	if d, blocked := compositeBlock("composite address", parts, order); blocked {
		return d, true
	}
	var out []string
	for i, n := range named {
		p := parts[n.id]
		used = append(used, p)
		text := p.text
		if n.atom == "state" {
			text = stateText(s, p, postal)
		}
		if n.atom == "zip" && i > 0 && named[i-1].atom == "state" && len(out) > 0 {
			out[len(out)-1] += " " + text
			continue
		}
		out = append(out, text)
	}
	return compositeDraft(strings.Join(out, ", "), nil, "composite address: "+strings.Join(names, "+")+" in label order", used...), true
}

// placeQuestion is the sentence that asks when a statement about the role
// leads it ("TypeSafe works in-office full time in downtown San Francisco.
// Where are you currently based?"): the statement names the role's office, not
// the place asked for.
func placeQuestion(text string) string {
	ss := sentencesOf(text)
	if len(ss) < 2 || !strings.HasSuffix(ss[len(ss)-1], "?") {
		return text
	}
	return ss[len(ss)-1]
}

// cityStateTypeahead answers a "(City, State)" or current-place location
// typeahead whose menu is a catalog, as the user's place with the geo lane's
// answer key, so the client seeds the search with the city and picks the
// matching menu row.
func cityStateTypeahead(s *Step) (Draft, bool) {
	q := s.Q
	if q.Kind != KindCustomListbox || classifySection(q.SectionPath) != sectionNone {
		return Draft{}, false
	}
	if n := len(q.Options); n != 0 && n <= catalogOptionCount {
		return Draft{}, false
	}
	// A menu of the US states is the state picker, not a catalog of places
	// (Found Energy's "Current Location"): the state signature answers it.
	if usStateList(signatureLabels(q.Options, s.sc)) {
		return Draft{}, false
	}
	cityState := cityStateRE.MatchString(q.Text) && asksLocationRE.MatchString(q.Text)
	ask := placeQuestion(q.Text)
	if !cityState && (!currentPlaceRE.MatchString(q.Text) || placeAskVetoRE.MatchString(ask)) || notOwnSubject.MatchString(ask) {
		return Draft{}, false
	}
	parts := map[string]addrPart{}
	for _, id := range []string{"address-city", "address-state", "address-country"} {
		parts[id] = partOf(s, id)
	}
	if d, blocked := compositeBlock("city-state typeahead", parts, []string{"address-city", "address-state", "address-country"}); blocked {
		return d, true
	}
	state := stateProse(parts["address-state"].text)
	answer := strings.Join([]string{parts["address-city"].text, state, parts["address-country"].text}, ", ")
	d := compositeDraft(answer, parts["address-city"].entry, "city-state typeahead: a catalog menu asked for City, State -> city, state, country for the geo lane",
		parts["address-city"], parts["address-state"], parts["address-country"])
	// Propel: a search that shows nothing until typed, whose question names
	// the states the employer hires in and not the recorded one, is the
	// state list's catch-all when the place itself is not found.
	if home, ok := usStateRowOf(state); ok && len(q.Options) == 0 {
		if listed := statesNamedIn(q.Text); len(listed) >= 3 && !listed[home.Abbr] {
			d.Alternatives = []string{"Other"}
			d.Trace = append(d.Trace, fmt.Sprintf("the question names %d states, not the recorded %s: \"Other\" is searched for when the place is not found", len(listed), home.Abbr))
		}
	}
	return d, true
}

// regionRowRE splits a region option into the country before a dash and the
// part of it after: Intercom's "US - Florida", "US - Illinois (Chicago
// Metro)", "US - Other".
var regionRowRE = lazyregexp.New(`^\s*([^-\x{2013}\x{2014}]{2,40}?)\s+[-\x{2013}\x{2014}]\s+(.+?)\s*$`)

// regionOption answers a current-place question over a closed list of
// regions, each a country before a dash and a part of it after, with
// catch-alls (Intercom's "Current Location": "US - Florida", "US - Other",
// "EMEA - Other"): no catalog to search, but the recorded state's row, else
// the recorded country's one catch-all. A state split across rows (a metro and
// the rest) is left to the user; a list with neither row stands aside.
func regionOption(s *Step) (Draft, bool) {
	q := s.Q
	opts := realOptions(q.Options)
	if len(opts) < 2 || q.Kind == KindCheckbox || q.InputType == InputMultiSelect || classifySection(q.SectionPath) != sectionNone ||
		!currentPlaceRE.MatchString(q.Text) || notOwnSubject.MatchString(placeQuestion(q.Text)) {
		return Draft{}, false
	}
	country, state := partOf(s, "address-country"), partOf(s, "address-state")
	own, ok := CountryCode(country.text)
	if !ok || country.askMe || state.askMe {
		return Draft{}, false
	}
	home := stateCodeOf(stateProse(state.text))
	var inState, other []Option
	for _, o := range opts {
		m := regionRowRE.FindStringSubmatch(o.Label)
		if m == nil {
			continue
		}
		if c, ok := CountryCode(m[1]); !ok || c != own {
			continue
		}
		region := strings.TrimSpace(parenRE.ReplaceAllString(m[2], ""))
		switch {
		case otherOptionRE.MatchString(region):
			other = append(other, o)
		case home != "" && stateCodeOf(region) == home:
			inState = append(inState, o)
		}
	}
	var chosen Option
	var why string
	switch {
	case len(inState) > 1:
		return abstainDraft(ReasonAmbiguous, "", "region list: %d rows name the recorded state %s -> left to the user", len(inState), home), true
	case len(inState) == 1:
		chosen, why = inState[0], "the recorded state's row"
	case len(other) == 1:
		chosen, why = other[0], "no row names the recorded state; the country's catch-all"
	default:
		return Draft{}, false
	}
	d := compositeDraft(chosen.Label, nil, "region list: "+why+" -> "+quoteTerm(chosen.Label), country, state)
	d.ChosenLabel, d.HasOption, d.OptionScore = chosen.Label, true, 1
	return d, true
}

// countryWideRestRE is what a row that is a whole country adds to the
// country's name: an arrangement over all of it ("US Remote", "Remote - United
// States", "India (Remote)") or nothing.
var countryWideRestRE = lazyregexp.New(`(?i)^\W*(?:(?:fully\s+)?remote|anywhere|nationwide)?\W*$`)

// placeOption answers a current-place question over a list of places, each a
// city or a whole country (Ema: "US Remote", "Mountain View, California",
// "India Remote", "Bengaluru, India"): the one row naming the recorded city in
// the recorded country, else the one row that is the recorded country as a
// whole. A city that is not the applicant's is no row of theirs; a list with
// neither row stands aside.
func placeOption(s *Step) (Draft, bool) {
	q := s.Q
	opts := realOptions(q.Options)
	if len(opts) < 2 || q.Kind == KindCheckbox || q.InputType == InputMultiSelect || classifySection(q.SectionPath) != sectionNone ||
		!currentPlaceRE.MatchString(q.Text) || notOwnSubject.MatchString(placeQuestion(q.Text)) {
		return Draft{}, false
	}
	city, country := partOf(s, "address-city"), partOf(s, "address-country")
	own, ok := CountryCode(country.text)
	if !ok || city.askMe || country.askMe {
		return Draft{}, false
	}
	words := func(t string) string { return " " + strings.Join(tokenize(foldPlace(t)), " ") + " " }
	cityKey := words(city.text)
	var inCity, whole []Option
	for _, o := range opts {
		if c, ok := LocationCountry(o.Label); !ok || c != own {
			continue
		}
		rest, _ := scanCountries(o.Label, false)
		switch {
		case strings.TrimSpace(cityKey) != "" && strings.Contains(words(o.Label), cityKey):
			inCity = append(inCity, o)
		case countryWideRestRE.MatchString(rest):
			whole = append(whole, o)
		}
	}
	var chosen Option
	var why string
	switch {
	case len(inCity) == 1:
		chosen, why = inCity[0], "the recorded city's row"
	case len(inCity) == 0 && len(whole) == 1:
		chosen, why = whole[0], "no row names the recorded city; the recorded country's row"
	default:
		return Draft{}, false
	}
	d := compositeDraft(chosen.Label, nil, "place list: "+why+" -> "+quoteTerm(chosen.Label), city, country)
	d.ChosenLabel, d.HasOption, d.OptionScore = chosen.Label, true, 1
	return d, true
}

// placeAskEligible is the guard of the two place answers below: a free-text
// box whose label opens with the ask, after any statements that address no
// one, and names no arrangement, preference, authorization or other subject.
func placeAskEligible(q Question, ask *lazyregexp.Regexp) bool {
	t := placeAsk(q.Text)
	return compositeEligible(q) && ask.MatchString(t) && !placeAskVetoRE.MatchString(t) && !notOwnSubject.MatchString(t)
}

// placeAsk is the label after its leading statements that address no one,
// the role's or the company's own (Windfall: "This position is a hybrid role
// in San Francisco or Denver. We are currently not considering relocation for
// candidates. Where are you located?").
func placeAsk(text string) string {
	ss := sentencesOf(text)
	i := 0
	for i < len(ss)-1 && strings.HasSuffix(ss[i], ".") && !addressedRE.MatchString(ss[i]) {
		i++
	}
	return strings.Join(ss[i:], " ")
}

// locatedPlace answers a free-text "Where are you located?" with the
// applicant's place: "City, ST" in the United States, "City, Country"
// elsewhere.
func locatedPlace(s *Step) (Draft, bool) {
	if !placeAskEligible(s.Q, locatedAskRE) {
		return Draft{}, false
	}
	parts := map[string]addrPart{"address-city": partOf(s, "address-city"), "address-country": partOf(s, "address-country")}
	order := []string{"address-city", "address-country"}
	if code, _ := CountryCode(parts["address-country"].text); code == "US" {
		parts["address-state"] = partOf(s, "address-state")
		order = []string{"address-city", "address-state"}
	}
	if d, blocked := compositeBlock("located place", parts, order); blocked {
		return d, true
	}
	last, used := parts["address-country"], []addrPart{parts["address-city"], parts["address-country"]}
	text := last.text
	if st, ok := parts["address-state"]; ok {
		text, used = stateText(s, st, true), append(used, st)
	}
	return compositeDraft(parts["address-city"].text+", "+text, nil, "located place: the city with the state code in the United States, else with the country", used...), true
}

// workCountry answers a free-text "In which country will you work?" with the
// user's own work-country answer, else the country of residence.
func workCountry(s *Step) (Draft, bool) {
	if !placeAskEligible(s.Q, workCountryAskRE) {
		return Draft{}, false
	}
	p, from := partOf(s, "work-country"), "work-country"
	if p.text == "" && !p.askMe {
		p, from = partOf(s, "address-country"), "address-country"
	}
	if d, blocked := compositeBlock("work country", map[string]addrPart{from: p}, []string{from}); blocked {
		return d, true
	}
	return compositeDraft(p.text, p.entry, "work country: "+from+" answers the country the applicant will work from", p), true
}

// stateAskRE asks for the state the applicant lives in: a bare "State" or
// "State of residence", also with the state's other words ("State/Region",
// "State, Province or Territory"), or "Which state do you reside in?"
// (Greenlight: "Please confirm which state you will reside and work."; Vultr:
// "Which U.S. State are you located in?").
var stateAskRE = lazyregexp.New(`(?i)^\W*(?:(?:current|home)\s+)?state(?:\s*(?:/|,|\bor\b|&)\s*(?:province|region|territory))*(?:\s+of\s+residence)?\W*$|\b(?:which|what)\s+(?:u\.?s\.?\s+)?state\s+(?:(?:(?:do|will)\s+)?you\s+(?:currently\s+|will\s+)?(?:reside|live)|are\s+you\s+(?:currently\s+)?(?:located|based|living|residing))\b`)

// usStateRE is "U.S. State", whose capital after the period no sentence
// starts.
var usStateRE = lazyregexp.New(`\b[Uu]\.[Ss]\.\s+([Ss]tates?)\b`)

// residenceState answers a free-text state-of-residence question with the
// state of the recorded address, written out. Only the sentence that asks is
// read for another subject: a statement before it may name the employer.
func residenceState(s *Step) (Draft, bool) {
	q := s.Q
	if !compositeEligible(q) {
		return Draft{}, false
	}
	for _, sentence := range sentencesOf(usStateRE.ReplaceAllString(q.Text, "US $1")) {
		if stateAskRE.MatchString(sentence) && !notOwnSubject.MatchString(sentence) {
			d, ok := addressPart(s, namedAtom{atom: "state", id: "address-state"})
			if name, known := stateByAbbr[strings.ToLower(d.Answer)]; known {
				d.Answer = name
			}
			return d, ok
		}
	}
	return Draft{}, false
}

// postalKey is a postal code without its spaces and hyphens, upper-cased; text
// holding anything else is not one.
func postalKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(s)) {
		switch {
		case r >= '0' && r <= '9', r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r == ' ' || r == '-':
		default:
			return ""
		}
	}
	return b.String()
}

// leadingPostal is the postal code an option opens with ("29401, Charleston,
// Berkeley, SC"): its first comma segment, holding a digit.
func leadingPostal(label string) string {
	first, _, _ := strings.Cut(label, ",")
	k := postalKey(first)
	if len(k) < 3 || len(k) > 10 || !strings.ContainsAny(k, "0123456789") {
		return ""
	}
	return k
}

// postalCodeList answers a postal-code list whose options open with the code
// (Oracle's "29401, Charleston, Berkeley, SC") with the option opening with the
// recorded code, a ZIP+4 read by its ZIP. A list without it abstains rather
// than pick a neighbour.
func postalCodeList(s *Step) (Draft, bool) {
	q := s.Q
	if q.Kind != KindSelect && q.Kind != KindCustomListbox || classifySection(q.SectionPath) != sectionNone {
		return Draft{}, false
	}
	if named := namedAddressAtoms(q.Text); len(named) != 1 || named[0].atom != "zip" {
		return Draft{}, false
	}
	opts := realOptions(q.Options)
	if len(opts) < 2 {
		return Draft{}, false
	}
	for _, o := range opts {
		if leadingPostal(o.Label) == "" {
			return Draft{}, false
		}
	}
	p := partOf(s, "address-zip")
	if d, blocked := compositeBlock("postal code list", map[string]addrPart{"address-zip": p}, []string{"address-zip"}); blocked {
		return d, true
	}
	want := postalKey(p.text)
	if len(want) == 9 && digitsOnly(want) {
		want = want[:5]
	}
	var hits []Option
	for _, o := range opts {
		if leadingPostal(o.Label) == want {
			hits = append(hits, o)
		}
	}
	if len(hits) != 1 {
		return abstainDraft(ReasonInputMismatch, "", "postal code list: %d options open with the recorded postal code -> abstain", len(hits)), true
	}
	d := compositeDraft(hits[0].Label, p.entry, "postal code list: the option opening with the recorded postal code", p)
	d.ChosenLabel = hits[0].Label
	return d, true
}

// contactComposite is the ContactComposite slot.
var contactComposite = chain(applicantContact, wholeAddress, compositeLocation, compositeAddress, regionOption, placeOption, cityStateTypeahead, postalCodeList, locatedPlace, workCountry, residenceState)

// cityOption picks the option of a city list that carries the recorded city and
// the recorded state (its name, or its code between spaces) and country,
// preferring an option whose first comma-segment is the city. The recorded state
// is never changed.
func cityOption(s *Step, e *Entry) (Draft, bool) {
	q := s.Q
	if e == nil || e.ID != "address-city" || len(q.Options) < 2 {
		return Draft{}, false
	}
	city, state, country := partOf(s, "address-city"), partOf(s, "address-state"), partOf(s, "address-country")
	if city.text == "" || state.text == "" || country.text == "" {
		return Draft{}, false
	}
	cityKey, countryKey := normalizeLabel(city.text), normalizeLabel(country.text)
	stateName, code := normalizeLabel(stateProse(state.text)), strings.ToLower(stateCodeOfEntry(s))
	var candidates []Option
	for _, o := range q.Options {
		l := normalizeLabel(o.Label)
		if !strings.Contains(l, cityKey) || !strings.Contains(l, countryKey) {
			continue
		}
		if stateName != "" && strings.Contains(l, stateName) || code != "" && strings.Contains(" "+l+" ", " "+code+" ") {
			candidates = append(candidates, o)
		}
	}
	if len(candidates) == 0 {
		return Draft{}, false
	}
	chosen := candidates[0]
	exact := false
	for _, o := range candidates {
		first, _, _ := strings.Cut(o.Label, ",")
		if normalizeLabel(first) == cityKey {
			chosen, exact = o, true
			break
		}
	}
	d := optionDraft(optionBinding{Label: chosen.Label, Value: chosen.Value, Score: 1}, e, SourceEntry, true)
	d.Trace = []string{"address-city: full-location option " + quoteTerm(chosen.Label) + " disambiguated (" +
		strconv.Itoa(len(candidates)) + " state-consistent, exact city segment " + map[bool]string{true: "won", false: "absent"}[exact] + ")"}
	return d, true
}

// usResidenceRE is a whole yes/no question about living in the United States
// or in its continental (contiguous, lower 48) part, and nothing else: a second
// place, a willingness, a time ("during employment") or a negation leaves the
// question to the routes that read those. Where one "permanently" resides is
// where one lives (Branch, whose follow-up asks that city and state); a
// "permanent resident" is an immigration status and no question of this lane.
var usResidenceRE = lazyregexp.New(`(?i)^\W*(?:(?:do|are)\s+you\s+(?:currently\s+|presently\s+|now\s+|permanently\s+)?(?:reside|residing|live|living|located|based)\s+(?:in|within)(?:,?\s+and\s+(?:would|will|could)\s+(?:be\s+)?work(?:ing)?\s+(?:remotely\s+)?(?:from\s+)?(?:within|in))?|are\s+you\s+(?:currently\s+|presently\s+)?(?:a\s+)?resident\s+of)\s+(?:the\s+)?` +
	`(?:(continental|contiguous|mainland)\s+(?:united\s+states(?:\s+of\s+america)?|u\.?\s?s\.?(?:a\.?)?)|(lower\s*48|48\s+contiguous)(?:\s+(?:u\.?\s?s\.?\s+)?states)?|united\s+states(?:\s+of\s+america)?|u\.?\s?s\.?(?:a\.?)?)\W*$`)

// hiringLimitNoteRE marks a note after a residence question that addresses the
// applicant or adds a condition. A note without it only states the employer's
// hiring limit (ReadMe: "... we're not currently able to hire outside of the
// United States.") and adds nothing to answer.
var hiringLimitNoteRE = lazyregexp.New(`(?i)\b(?:you|your|yours|if|unless|willing|relocat\w*)\b`)

// usRequirementRE is a sentence naming the United States; usConditionRE one
// that adds a condition, a willingness or another place to it.
var (
	usRequirementRE = lazyregexp.New(`(?i)\bu\.?\s?s\.?(?:a\.?)?(?:\s|,|$)|\bunited\s+states\b`)
	usConditionRE   = lazyregexp.New(`(?i)\b(?:if|unless|willing|relocat\w*|or|except|canada|outside)\b`)
)

// residenceQuestion is the opening question of a text whose later sentences
// only state a hiring limit, or the closing question of one whose earlier
// sentences only restate the US requirement (Vesta: "The role is U.S. based,
// you must currently live and work from within the U.S."); any other text is
// returned whole.
func residenceQuestion(text string) string {
	sentences := sentencesOf(text)
	if len(sentences) < 2 {
		return text
	}
	if strings.HasSuffix(sentences[0], "?") && !hiringLimitNoteRE.MatchString(strings.Join(sentences[1:], " ")) {
		return sentences[0]
	}
	last := sentences[len(sentences)-1]
	if !strings.HasSuffix(last, "?") {
		return text
	}
	for _, p := range sentences[:len(sentences)-1] {
		if strings.Contains(p, "?") || !usRequirementRE.MatchString(p) || usConditionRE.MatchString(p) {
			return text
		}
	}
	return last
}

// usTerritoryCodes are the ISO codes of the US territories: in the country but
// outside the continental part.
var usTerritoryCodes = map[string]bool{"PR": true, "GU": true, "VI": true, "AS": true, "MP": true, "UM": true}

// usResidence answers "Do you currently reside in the (Continental) United
// States?" from the address the user recorded: the country, and for the
// continental part the state (the 48 contiguous states and DC; Alaska, Hawaii
// and the territories are outside it). An address that does not say stands
// aside, so the user's own entry decides. A note that only states the
// employer's hiring limit is set aside (residenceQuestion).
func usResidence(s *Step) (Draft, bool) {
	q := s.Q
	m := usResidenceRE.FindStringSubmatch(residenceQuestion(strings.TrimSpace(q.Text)))
	if m == nil || classifySection(q.SectionPath) != sectionNone {
		return Draft{}, false
	}
	yes, okY := ownYesNo(q.Options, true)
	no, okN := ownYesNo(q.Options, false)
	if !okY || !okN {
		return Draft{}, false
	}
	continental := m[1] != "" || m[2] != ""
	country, state := partOf(s, "address-country"), partOf(s, "address-state")
	if country.askMe || state.askMe {
		return Draft{}, false
	}
	code := stateCodeOf(stateProse(state.text))
	c := strings.ToUpper(strings.TrimSpace(s.Req.Facts.Address.Country))
	if country.source == "bank" {
		// The bank may hold the name ("Puerto Rico") where the profile holds
		// the code.
		if c = strings.ToUpper(country.text); len(c) != 2 {
			c, _ = CountryCode(country.text)
		}
	}
	var inUS, inContinental bool
	switch {
	case usCountryRE.MatchString(country.text), country.text == "" && code != "":
		inUS, inContinental = true, code != "" && code != "AK" && code != "HI"
		if continental && code == "" {
			return abstainDraft(ReasonPlaceholderEmpty, "", "US residence: the recorded address names no state, so the continental part cannot be told -> abstain"), true
		}
	case usTerritoryCodes[c]:
		if !continental {
			return abstainDraft(ReasonAmbiguous, "", "US residence: %s is a US territory; whether the question counts it is unclear -> abstain", c), true
		}
	case country.text == "":
		return Draft{}, false
	}
	chosen, why := no, "the recorded address is outside it"
	if inUS && (!continental || inContinental) {
		chosen, why = yes, "the recorded address is inside it"
	}
	place := "the United States"
	if continental {
		place = "the continental United States"
	}
	d := derivedDraft(chosen.Label, "", startProfileDerive, "US residence: "+place+"; "+why+" ("+strings.TrimSpace(country.text+" "+state.text)+") -> "+quoteTerm(chosen.Label))
	d.ChosenLabel = chosen.Label
	d.Deterministic = true
	return d, true
}

// stateResidenceRE asks whether the applicant is a resident of one named place
// and nothing else: "Are you a resident of California?", "Do you currently
// reside in the state of Texas?".
var stateResidenceRE = lazyregexp.New(`(?i)^\W*(?:are\s+you\s+(?:currently\s+|presently\s+)?(?:an?\s+)?(?:current\s+|legal\s+|permanent\s+)?resident\s+of|do\s+you\s+(?:currently\s+|presently\s+)?reside\s+in)\s+(?:the\s+state\s+of\s+)?([a-z][a-z .]{1,30}?)\s*\??\s*\*?\s*$`)

// usStateResidence answers whether the applicant resides in one named US state
// from the state of the address they recorded (Palantir: "Are you a resident
// of California?" for an address in South Carolina is No). The place must be a
// whole state name or code ("New York City" is not New York); an address that
// names no state stands aside, so the user's own entry decides.
func usStateResidence(s *Step) (Draft, bool) {
	q := s.Q
	m := stateResidenceRE.FindStringSubmatch(strings.TrimSpace(q.Text))
	if m == nil || classifySection(q.SectionPath) != sectionNone {
		return Draft{}, false
	}
	asked := stateCodeOf(m[1])
	if asked == "" {
		return Draft{}, false
	}
	yes, okY := ownYesNo(q.Options, true)
	no, okN := ownYesNo(q.Options, false)
	if !okY || !okN {
		return Draft{}, false
	}
	state := partOf(s, "address-state")
	home := stateCodeOf(stateProse(state.text))
	if state.askMe || home == "" {
		return Draft{}, false
	}
	chosen := no
	if home == asked {
		chosen = yes
	}
	d := derivedDraft(chosen.Label, "", startProfileDerive, "state residence: "+asked+"; the recorded address is in "+home+" -> "+quoteTerm(chosen.Label))
	d.ChosenLabel = chosen.Label
	d.Deterministic = true
	return d, true
}
