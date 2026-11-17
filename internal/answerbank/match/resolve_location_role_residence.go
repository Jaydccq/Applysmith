package match

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// A whole Yes/No "Do you currently reside in the location specified for this
// role?" (Nebius, posting location "United States") is read against the
// posting's own place and the address the user recorded. A posting in the
// United States as a whole is the country, a state alone is the state, and a
// city is the city in its state. An address inside the place is Yes; one
// outside the country or the state is No. A city the address does not name is
// left alone, since the applicant may live in its metro, as is a remote
// posting, a posting of several places, a place outside the United States, or
// an address that does not say.

var (
	roleResidenceRE = lazyregexp.New(`(?i)^\W*(?:do|are)\s+you\s+(?:currently\s+|presently\s+|now\s+)?(?:reside|residing|live|living|located|based)\s+(?:in|within)\s+` +
		`(?:the\s+location\s+(?:(?:specified|listed|stated|posted|indicated|given)\s+)?(?:for|of)\s+(?:this|the)\s+(?:role|position|job|opening)|` +
		`(?:the\s+(?:location|area)\s+)?where\s+(?:this|the)\s+(?:role|position|job)\s+is\s+(?:located|based))\W*$`)
	// severalPlacesRE is a posting location that lists more than one place; a
	// state code ("Portland, OR") is no conjunction.
	severalPlacesRE = lazyregexp.New(`[;|/]|\s(?:or|and|&)\s`)
	// roleCountryAskRE is a whole Yes/No "Do you currently reside in <place>?";
	// roleCountryResidence takes it when the place is the role's country.
	roleCountryAskRE = lazyregexp.New(`(?i)^\W*(?:do|are)\s+you\s+(?:currently\s+|presently\s+|now\s+)?(?:reside|residing|live|living|located|based)\s+(?:in|within)\s+(.+?)\W*$`)
)

// roleCountryResidence answers "Do you currently reside in the country this
// role is listed in?" (NerdWallet) from the posting's one country and the
// recorded address's country. A posting of no one country, or an address that
// names none, is left alone.
func roleCountryResidence(s *Step) (Draft, bool) {
	q := s.Q
	m := roleCountryAskRE.FindStringSubmatch(strings.TrimSpace(q.Text))
	if m == nil || !strings.Contains(strings.ToLower(m[1]), "country") || !deicticRE.MatchString(m[1]) ||
		strings.TrimSpace(deicticRE.ReplaceAllString(m[1], "")) != "" || classifySection(q.SectionPath) != sectionNone {
		return Draft{}, false
	}
	yes, okY := ownYesNo(q.Options, true)
	no, okN := ownYesNo(q.Options, false)
	job, st := postingCountry(s.Req.Posting)
	home := recordedHomeCountry(s)
	if !okY || !okN || st != locationResolved || home == "" {
		return Draft{}, false
	}
	chosen, why := no, "outside it"
	if home == job {
		chosen, why = yes, "inside it"
	}
	d := derivedDraft(chosen.Label, "", startProfileDerive, "role country residence: the posting's country "+job+"; the recorded address ("+home+") is "+why+" -> "+quoteTerm(chosen.Label))
	d.ChosenLabel = chosen.Label
	d.Deterministic = true
	return d, true
}

// roleLocationResidence answers whether the applicant lives in the role's
// location from the posting's place and the recorded address.
func roleLocationResidence(s *Step) (Draft, bool) {
	q := s.Q
	if !roleResidenceRE.MatchString(strings.TrimSpace(q.Text)) || classifySection(q.SectionPath) != sectionNone {
		return Draft{}, false
	}
	yes, okY := ownYesNo(q.Options, true)
	no, okN := ownYesNo(q.Options, false)
	loc := strings.TrimSpace(s.Req.Posting.Location)
	if !okY || !okN || loc == "" || severalPlacesRE.MatchString(loc) {
		return Draft{}, false
	}
	job, ok := parseLocationText(loc)
	jobState := stateCodeOf(job.Region)
	if !ok || job.Remote || job.Country != "" && !usCountryRE.MatchString(job.Country) || job.Region != "" && jobState == "" {
		return Draft{}, false
	}
	country, state, city := partOf(s, "address-country"), partOf(s, "address-state"), partOf(s, "address-city")
	if country.askMe || state.askMe || city.askMe {
		return Draft{}, false
	}
	addrState := stateCodeOf(state.text)
	inUS := usCountryRE.MatchString(country.text) || country.text == "" && addrState != ""
	if !inUS && country.text == "" {
		return Draft{}, false
	}
	var inside bool
	switch {
	case !inUS:
	case job.City == "" && job.Region == "":
		inside = true
	case job.City == "":
		if addrState == "" {
			return Draft{}, false
		}
		inside = addrState == jobState
	default:
		if addrState == "" || addrState != jobState || !strings.EqualFold(city.text, job.City) {
			return Draft{}, false
		}
		inside = true
	}
	chosen, why := no, "the recorded address is outside it"
	if inside {
		chosen, why = yes, "the recorded address is inside it"
	}
	d := derivedDraft(chosen.Label, "", startProfileDerive, "role residence: the posting's place "+quoteTerm(loc)+"; "+why+" ("+strings.TrimSpace(city.text+" "+state.text+" "+country.text)+") -> "+quoteTerm(chosen.Label))
	d.ChosenLabel = chosen.Label
	d.Deterministic = true
	return d, true
}

// regionCountryRows list the countries of each region a residence question can
// name. Each list is whole, so a country outside every named region is
// outside them; a country Europe's edge names is not told either way.
const regionCountryRows = `
north america: US CA MX GL BM PM BZ CR SV GT HN NI PA AG AI AW BB BL BQ BS CU CW DM DO GD GP HT JM KN KY LC MF MQ MS PR SX TC TT VC VG VI
south america: AR BO BR CL CO EC FK GF GY PE PY SR UY VE
latin america: MX BZ CR SV GT HN NI PA CU DO HT PR AR BO BR CL CO EC GF PE PY UY VE
europe: AD AL AT AX BA BE BG BY CH CY CZ DE DK EE ES FI FO FR GB GG GI GR HR HU IE IM IS IT JE LI LT LU LV MC MD ME MK MT NL NO PL PT RO RS RU SE SI SJ SK SM UA VA XK
europe edge: TR GE AM AZ KZ
`

var (
	regionCountries = map[string]map[string]bool{}
	// regionNames are the folded names a question gives each listed region.
	regionNames = map[string]string{"north america": "north america", "south america": "south america", "latin america": "latin america",
		"latam": "latin america", "europe": "europe", "americas": "americas"}
	// regionResidenceRE is a whole Yes/No about living in the places it names
	// (River: "Are you located in the Americas or Europe?").
	regionResidenceRE = lazyregexp.New(`(?i)^\W*(?:(?:do|are)\s+you\s+(?:currently\s+|presently\s+|now\s+)?(?:reside|residing|live|living|located|based)\s+(?:in|within)|are\s+you\s+(?:currently\s+|presently\s+)?(?:a\s+)?resident\s+of)\s+(.+?)\W*$`)
	placeListSplitRE  = lazyregexp.New(`\s*(?:,|/|\+|&|\bor\b|\band\b)\s*`)
	// residenceRequirementRE is a requirement to live in a place, and
	// meetRequirementRE the closing question that asks whether the applicant
	// meets it (Lime: "this role requires you to reside in and work remotely
	// from Canada. Do you meet this requirement?"). residenceConditionRE adds
	// a willingness, an exception or another way to meet it.
	residenceRequirementRE = lazyregexp.New(`(?i)\b(?:requires?\s+(?:you|candidates|applicants)\s+to|(?:you|candidates|applicants)\s+(?:must|need\s+to|are\s+required\s+to))\s+(?:currently\s+)?(?:reside|live|be\s+based|be\s+located)\s+(?:in|within)(?:\s+and\s+work\s+(?:remotely\s+)?(?:from|in|within))?\s+(.+?)\W*$`)
	meetRequirementRE      = lazyregexp.New(`(?i)^\W*(?:do|can|will)\s+you\s+meet\s+(?:this|that|the|these|those)\s+(?:requirements?|criteria|criterion)\W*$`)
	residenceConditionRE   = lazyregexp.New(`(?i)\b(?:if|unless|willing|relocat\w*|except|exceptions?|outside)\b`)
)

// residencePlaces is the places a whole residence question names, or the ones
// a requirement to live there names when the closing question asks whether the
// applicant meets it; "" for any other text.
func residencePlaces(text string) string {
	if m := regionResidenceRE.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	sentences := sentencesOf(text)
	n := len(sentences)
	if n < 2 || !meetRequirementRE.MatchString(sentences[n-1]) {
		return ""
	}
	for _, p := range sentences[:n-1] {
		if strings.Contains(p, "?") || residenceConditionRE.MatchString(p) {
			return ""
		}
	}
	if m := residenceRequirementRE.FindStringSubmatch(sentences[n-2]); m != nil {
		return m[1]
	}
	return ""
}

func init() {
	for _, row := range strings.Split(strings.TrimSpace(regionCountryRows), "\n") {
		name, codes, _ := strings.Cut(row, ":")
		set := map[string]bool{}
		for _, c := range strings.Fields(codes) {
			set[c] = true
		}
		regionCountries[name] = set
	}
	americas := map[string]bool{}
	for _, r := range []string{"north america", "south america"} {
		for c := range regionCountries[r] {
			americas[c] = true
		}
	}
	regionCountries["americas"] = americas
}

// regionResidence answers a Yes/No about living in one or more regions or
// countries (residencePlaces) from the recorded address's country: inside any
// is Yes, outside all of them No. A place with no country list, a country on
// Europe's edge, a name that is also a U.S. state's (Georgia, "CA"), which the
// state lane reads, or an address with no country stands aside.
func regionResidence(s *Step) (Draft, bool) {
	q := s.Q
	places := residencePlaces(strings.TrimSpace(q.Text))
	if places == "" || classifySection(q.SectionPath) != sectionNone {
		return Draft{}, false
	}
	yes, okY := ownYesNo(q.Options, true)
	no, okN := ownYesNo(q.Options, false)
	home := recordedHomeCountry(s)
	if !okY || !okN || home == "" {
		return Draft{}, false
	}
	inside := false
	for _, part := range placeListSplitRE.Split(foldText(places), -1) {
		part = strings.TrimPrefix(strings.TrimSpace(part), "the ")
		if name, ok := regionNames[part]; ok {
			if name == "europe" && regionCountries["europe edge"][home] {
				return Draft{}, false
			}
			inside = inside || regionCountries[name][home]
			continue
		}
		code, ok := CountryCode(part)
		if !ok || stateCodeOf(part) != "" {
			return Draft{}, false
		}
		inside = inside || code == home
	}
	chosen, why := no, "the recorded address's country is outside them"
	if inside {
		chosen, why = yes, "the recorded address's country is inside one"
	}
	d := derivedDraft(chosen.Label, "", startProfileDerive, "residence: "+quoteTerm(places)+"; "+why+" ("+home+") -> "+quoteTerm(chosen.Label))
	d.ChosenLabel = chosen.Label
	d.Deterministic = true
	return d, true
}
