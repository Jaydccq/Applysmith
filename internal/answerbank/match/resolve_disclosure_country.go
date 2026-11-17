package match

import (
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"applysmith/internal/lazyregexp"
)

// The country a work-authorization or sponsorship question is about. A fact is
// recorded for the countries in Facts.AuthorizedCountries, so a question is
// answered only when every country it is about is one of them: the countries it
// names, and the posting's country when it says "this country", "here" or the
// like or names none. Everything else is the terminal posting_country abstain.

// Region codes. A region fact (the profile may list EU) covers its member
// countries; a member country does not cover the region, since a permit for one
// member state is no right to work in the others.
const (
	regionEU     = "EU"
	regionEEA    = "EEA"
	regionEurope = "EUROPE"
)

var euMembers = setOf("AT", "BE", "BG", "HR", "CY", "CZ", "DK", "EE", "FI", "FR", "DE", "GR", "HU", "IE", "IT",
	"LV", "LT", "LU", "MT", "NL", "PL", "PT", "RO", "SK", "SI", "ES", "SE")

// countryAliases are the names a form uses that the ISO table does not.
var countryAliases = map[string]string{
	"united states of america": "US", "america": "US",
	"great britain": "GB", "britain": "GB", "england": "GB", "scotland": "GB", "wales": "GB", "northern ireland": "GB",
	"european union": regionEU, "european economic area": regionEEA, "europe": regionEurope,
	"holland": "NL", "czech republic": "CZ", "korea": "KR",
	// Adjectives that name only their country ("US or Canadian employment").
	"canadian": "CA", "mexican": "MX", "australian": "AU",
}

// notCountryNames are table names that are also a US state, a given name or a
// place that is mostly not the country; a form that writes one is not read as a
// country mention (its word is then unknown to the closed reading).
var notCountryNames = setOf("georgia", "jersey")

var (
	// Abbreviations are matched with their case: "US" is the country, "us" the
	// pronoun.
	abbrevCountries = []struct {
		re   *lazyregexp.Regexp
		code string
	}{
		{lazyregexp.New(`\bU\.S(?:\.(?:A\.?)?|\b)|\b(?:USA|US)\b`), "US"},
		{lazyregexp.New(`\bU\.K\.?|\bUK\b`), "GB"},
		{lazyregexp.New(`\bEEA\b`), regionEEA},
		{lazyregexp.New(`\bEU\b`), regionEU},
		{lazyregexp.New(`\bUAE\b`), "AE"},
	}
	foldAbbrevRE = lazyregexp.New(`(?i)\bU\.S(?:\.(?:A\.?)?|\b)|\b(?:USA|US)\b|\bU\.K\.?|\bUK\b|\bEEA\b|\bEU\b|\bUAE\b`)
	namedRE      *lazyregexp.Regexp
	nameCode     = map[string]string{}
	newPlaceRE   = lazyregexp.New(`(?i)\bnew (mexico|jersey)\b`)
	// placeLegendRE is a key that maps a choice to its country ("San Francisco
	// = United States. Melbourne = Australia."): it lists the options, not the
	// countries the question is about.
	placeLegendRE = lazyregexp.New(`[^.?!;:=\n]{1,40}=[^.?!;,=\n]{1,40}`)
	// usOrRoleCountryRE is the United States with a bracketed alternative for
	// a role elsewhere (EnergyHub: "in the United States (or, if the position
	// is not based in the United States., do you have ... authorization to work
	// in the country where the position is located)"): either way the question
	// is about the role's country.
	usOrRoleCountryRE = lazyregexp.New(`(?i)\b(?:the\s+)?(?:united\s+states|u\.?s\.?a?)\s*\(\s*or\s*,?\s+if\s+(?:the|this)\s+(?:position|role|job)\s+is\s+not\s+(?:based|located)\s+in\s+(?:the\s+)?(?:united\s+states|u\.?s\.?a?)\.?\s*,?[^()]*?\b(?:the\s+)?country\s+(?:where|in\s+which)\s+(?:the|this)\s+(?:position|role|job)\s+is\s+(?:located|based)\s*\)`)

	// bareCountryRE is "in the country" closing a question with no clause of
	// its own (Plume: "Are you authorized to work in the country for any
	// employer?"): the posting's country, read as "this country".
	bareCountryRE = lazyregexp.New(`(?i)\bin the country((?:\s+for\s+any\s+employer)?\s*(?:[?.!]|$))`)

	deicticRE = lazyregexp.New(`(?i)\b(?:` +
		// Dash0 (Ashby): "to work in the given location".
		`(?:th(?:is|at)|the given) (?:country|location)|` +
		// Red Hat: "the country of origin for this position" is the posting's.
		`(?:the )?country (?:of origin )?(?:of|for) this (?:role|position|job)|` +
		// Babylist: "the country listed for this role"; Sysdig: "the country outlined for this job".
		`(?:the )?country (?:listed|posted|named|specified|stated|outlined) (?:for|in|on) (?:this|the) (?:role|position|job(?: posting)?|posting)|` +
		// Alnylam: "the country/region of the job for which you are applying".
		`(?:the )?country ?/ ?region of the (?:role|position|job)(?: (?:to|for) which you are applying| you are applying (?:to|for))?|` +
		`(?:the )?country of hire|` +
		// Gem: "the country or jurisdiction where the position to which you are applying is located";
		// Kong (Ashby): "the country where this position will be worked".
		// HappyRobot (Ashby): "the country in which the job you're applying for is located".
		// EarnIn: "the country that this role is based", the "in" left out;
		// NerdWallet: "the country this role is listed in"; Appian: "the
		// city/country where this position is located"; Mindbody: "the country
		// where the job office is located"; Ginkgo: "the country where the
		// job is open".
		`(?:the )?(?:jurisdiction ?/ ?country|country ?/ ?jurisdiction|city ?/ ?country|country ?/ ?city|country(?: or jurisdiction)?|jurisdiction(?: or country)?) (?:(?:where|in which|that) )?(?:this|the) (?:(?:role|position|job)(?: office)?|opportunity)(?: (?:to|for) which you(?: are|['\x{2019}]re) applying| you(?: are|['\x{2019}]re) applying (?:to|for))? (?:is (?:located|based|posted|listed|open)|will be (?:located|based|posted|worked|performed))(?: in)?|` +
		// Nuro: "the country which you are applying", the preposition left out;
		// HackerOne (Ashby): "the country for which job you are applying for".
		`(?:the )?countr(?:y|ies) (?:(?:for|in|to) )?which (?:(?:job|role|position) )?you (?:are applying|(?:have )?applied)(?: (?:for|to))?|` +
		// Docker (Ashby): "the country where you are applying"; EnergyHub: "the
		// country in which you're applying to work"; Coalition: "the country
		// that you are applying to"; Neo4j: "the region you are applying for";
		// Delinea (Ashby): "for the location you are applying for".
		`(?:the )?(?:country|region|location) (?:(?:where|in which|that) )?you(?:(?: are|['\x{2019}]re) applying| (?:have )?applied) (?:to work(?: in)?|to|in|for)|` +
		// GC AI: "in the country you plan to live and work in"; Synthesia: "the
		// country you wish to work in".
		`(?:the )?country (?:(?:where|in which) )?you (?:plan|intend|expect|are planning|are going|wish|want|would like) to (?:live and |reside and )?work(?: in)?|` +
		// ClickUp (Ashby): "in the country where you will be working"; ClassPass:
		// "in the country where you will perform the duties of this job".
		`(?:the )?country (?:(?:where|in which) )?you (?:will|would) (?:be (?:working|employed)|perform (?:the )?(?:duties|work) of (?:this|the) (?:job|role|position))(?: in)?|` +
		// PostHog: "to work in your desired location", where the applicant would work this role;
		// Meticulous: "for your selected location".
		`your (?:desired|preferred|intended|selected|chosen) (?:work )?(?:location|country)|` +
		// Bending Spoons: "in the country you selected" is the one the form asked for the role just above.
		`(?:the )?country (?:that )?you (?:have )?(?:selected|chose|chosen|indicated)(?: above| earlier)?|` +
		// Artificial Analysis: "in the country of the office you selected" after the office question.
		`(?:the )?country of the (?:office|location|site|city) (?:that )?you (?:have )?(?:selected|chose|chosen|indicated)(?: above| earlier)?|` +
		// Conga: "where this opportunity is located".
		`where (?:this|the) (?:role|position|job|opportunity) is (?:located|based|posted)|` +
		// Ambience (Ashby): "in the location the job is based"; Sentry
		// (Ashby): "in the location where this job is posted".
		`(?:the )?location (?:(?:where|in which) )?(?:this|the) (?:role|position|job) is (?:located|based|posted)(?: in| at)?|` +
		`(?:the )?(?:job|role|position)(?:['\x{2019}]?s)? location|` +
		// Brex: "authorized to work in the stated location of this role".
		`(?:the )?(?:(?:stated|listed|posted|specified) )?location of (?:this|the) (?:role|position|job)|` +
		// Block: "to work lawfully in the location posted for this position".
		`(?:the )?location (?:posted|listed|advertised|stated|specified) (?:for|in|on) (?:this|the) (?:role|position|job)(?: posting)?|` +
		// HP: "to work in the job posting country".
		`(?:the )?(?:job )?posting(?:['\x{2019}]?s)? country|` +
		`(?:the )?countr(?:y|ies) (?:listed|named|specified) (?:in|on) (?:the|this) (?:job )?(?:posting|description|listing)|` +
		`here)\b|\bcountry (?:where )?you(?: are|['\x{2019}]re) applying\s*(?:[?.!]|$)`)
	// residenceRE is the country the applicant lives in (ClickUp: "the country
	// you are based in"; Slate: "the location you are based"; TRM: "your
	// current country of employment", work being done where the applicant
	// is), with the "or" and the brackets that join it to a
	// named country (LiveKit: "in the US (or country of your residence)"); the
	// named country and the home country must then both be covered.
	residenceRE = lazyregexp.New(`(?i)(?:\(\s*)?(?:\bor\s+(?:in\s+)?)?(?:\b(?:the )?country (?:(?:where|in which|that) )?you (?:currently )?(?:reside|live|are (?:currently )?(?:residing|living|based|located|domiciled))(?: in)?\b|\b(?:your |the )?(?:current )?country of (?:your )?(?:current )?(?:residence|employment|domicile)\b|\byour current (?:location|country)\b|` +
		// Slate: "to work in the location you are based"; Camunda (a remote
		// role): "the country where you're planning to work from", which is
		// where the applicant is.
		`\bthe location (?:(?:where|in which|that) )?you (?:are )?(?:currently )?(?:based|located|living|residing|reside|live)(?: in)?\b|` +
		`\b(?:the )?country (?:(?:where|in which|that) )?you(?:['\x{2019}]re| are)? (?:planning|plan|intend|intending) to (?:live and )?work from\b)(?:\s*\))?`)
)

func init() {
	for code, name := range countryByCode {
		lower := strings.ToLower(name)
		if strings.Contains(lower, ",") || notCountryNames[lower] {
			continue
		}
		nameCode[lower] = code
	}
	for name, code := range countryAliases {
		nameCode[name] = code
	}
	names := make([]string, 0, len(nameCode))
	for n := range nameCode {
		names = append(names, regexp.QuoteMeta(n))
	}
	sort.Slice(names, func(i, j int) bool {
		return len(names[i]) > len(names[j]) || len(names[i]) == len(names[j]) && names[i] < names[j]
	})
	namedRE = lazyregexp.New(`(?i)\b(?:` + strings.Join(names, "|") + `)\b`)
}

// scanCountries removes every country or region mention from text and returns
// the rest with the codes found, in order. fold matches the abbreviations
// without regard to case, for a posting location that is not a sentence.
func scanCountries(text string, fold bool) (rest string, codes []string) {
	text, codes = markCountries(text, fold)
	// A list of countries ("US or Canada", "US/Canada") is one mention: its
	// joining words are no words of the question.
	text = countrySeriesRE.ReplaceAllString(text, countrySlot)
	return strings.ReplaceAll(text, countrySlot, " "), dedupe(codes)
}

// orSeriesRE is a text whose country mentions are one list joined by "or"
// ("the United States or Canada", "the US, Canada, or Mexico") or "and/or"
// (Tonal: "the US and/or Canada").
var orSeriesRE = lazyregexp.New(`^[^\x00]*\x00(?:\s*,\s*(?:the\s+)?\x00)*\s*,?\s*(?:\band\s*/\s*)?\bor\b\s*(?:the\s+)?\x00[^\x00]*$`)

// namesCountriesByOr reports a question that asks about any one of a list of
// countries.
func namesCountriesByOr(text string) bool {
	marked, _ := markCountries(text, false)
	return orSeriesRE.MatchString(marked)
}

// markCountries puts a slot in place of every country or region mention and
// returns the codes found, in order.
func markCountries(text string, fold bool) (string, []string) {
	var codes []string
	text = newPlaceRE.ReplaceAllStringFunc(text, func(m string) string { return strings.ReplaceAll(m, " ", "") })
	if fold {
		text = foldAbbrevRE.ReplaceAllStringFunc(text, func(m string) string {
			for _, a := range abbrevCountries {
				if a.re.MatchString(strings.ToUpper(m)) {
					codes = append(codes, a.code)
					break
				}
			}
			return countrySlot
		})
	} else {
		for _, a := range abbrevCountries {
			text = a.re.ReplaceAllStringFunc(text, func(string) string {
				codes = append(codes, a.code)
				return countrySlot
			})
		}
	}
	text = namedRE.ReplaceAllStringFunc(text, func(m string) string {
		codes = append(codes, nameCode[strings.ToLower(m)])
		return countrySlot
	})
	return text, codes
}

const countrySlot = "\x00"

// countrySeriesRE is a list of country slots; a joined name may keep its
// article (Lumos: "Canada or the United States"); "and/or" is one joint.
var countrySeriesRE = lazyregexp.New(`\x00(?:\s*(?:,|\band\s*/\s*or\b|/|&|\bor\b|\band\b)\s*(?:the\s+)?\x00)+`)

func dedupe(in []string) []string {
	var out []string
	for _, c := range in {
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}

// questionScope is what a question says about where: the countries it names,
// whether it points at the posting's country, and whether it asks about the
// country the applicant lives in (a country no fact here records).
type questionScope struct {
	codes     []string
	deictic   bool
	residence bool
}

// stripScope takes the country words out of a question and says what they
// were. The company's own name is not read for a country ("Canada Life").
func stripScope(text, company string) (rest string, sc questionScope) {
	// The company's own name is set aside while the countries are read and put
	// back after, so its words stay the closed reading's business.
	own := strings.TrimSpace(company)
	if lower := strings.ToLower(text); len(own) >= 3 && len(lower) == len(text) {
		if i := strings.Index(lower, strings.ToLower(own)); i >= 0 {
			held := text[i : i+len(own)]
			text = text[:i] + companyMark + text[i+len(own):]
			defer func() { rest = strings.Replace(rest, companyMark, held, 1) }()
		}
	}
	text = placeLegendRE.ReplaceAllString(text, " ")
	text = usOrRoleCountryRE.ReplaceAllString(text, " the country where the position is located ")
	if residenceRE.MatchString(text) {
		sc.residence = true
		text = residenceRE.ReplaceAllString(text, " ")
	}
	text = bareCountryRE.ReplaceAllString(text, "in this country$1")
	if deicticRE.MatchString(text) {
		sc.deictic = true
		text = deicticRE.ReplaceAllString(text, " ")
	}
	rest, sc.codes = scanCountries(text, false)
	// A company-sponsored visa only the United States issues (H-1B, L-1, E-3,
	// O-1, TN) names the United States.
	if employerVisaRE.MatchString(text) && !slices.Contains(sc.codes, "US") {
		sc.codes = append(sc.codes, "US")
	}
	return rest, sc
}

const companyMark = " zzcompanyzz "

// postingCountry is the posting's country from the request: the location text,
// or its labelled line in the posting text, read by resolveLocation.
func postingCountry(p Posting) (string, locationState) {
	return resolveLocation(postingLocation(p))
}

// postingLocation is the posting's location text, or its labelled line in the
// posting text, with a comma before a state code written after its city
// without one (commaBeforeStateCode) and the colliding state codes its text
// settles written as the states (settleCollidingStates).
func postingLocation(p Posting) string {
	loc := strings.TrimSpace(p.Location)
	if loc == "" {
		head := p.Description
		if len(head) > jobHeadChars {
			head = head[:jobHeadChars]
		}
		if m := locationLineRE.FindStringSubmatch(head); m != nil {
			loc = strings.TrimSpace(m[1])
		}
	}
	return settleCollidingStates(commaBeforeStateCode(loc), p.Description)
}

// uniformSponsorship answers the sponsorship-need entry on a posting open in
// several countries, or a question that names several, when the need is the
// same in each of them: a recorded authorized country takes the recorded need,
// any other country needs sponsorship, and at least one of them must be
// recorded. A need that differs between the countries is never guessed. A
// posting location that names no place ("Any Location / Remote") may be any
// country, and one that names a region ("Americas Remote", Customer.io) any of
// its countries, so only a recorded Yes is the same in each.
func uniformSponsorship(s *Step, e *Entry, named []string) (string, bool) {
	if e.ID != idSponsorshipNowOrFuture || len(named) == 1 {
		return "", false
	}
	need, known := sponsorshipNeed(s.Req.Facts, sponsorshipScope(s.Q.Text))
	codes, where := named, "the question names "
	if len(named) == 0 {
		loc := s.Req.postingLocation()
		var ok bool
		if codes, ok = locationCountries(loc); !ok {
			codes, ok = northAmerica(loc)
		}
		if !ok {
			region := regionRE.MatchString(foldPlace(loc))
			if !known || !need || !namesNoPlace(loc) && !region || len(recordedCountries(s.Req.Facts.AuthorizedCountries)) == 0 {
				return "", false
			}
			what := "names no place"
			if region {
				what = "names a region"
			}
			return "the posting's location " + what + " and the sponsorship need is Yes in the recorded countries and every other: answered for review", true
		}
		where = "the posting is open in "
	}
	if !known || len(codes) < 2 {
		return "", false
	}
	recorded := false
	for _, c := range codes {
		switch {
		case covers(s.Req.Facts.AuthorizedCountries, c):
			recorded = true
		case !need:
			return "", false
		}
	}
	if !recorded {
		return "", false
	}
	return where + strings.Join(codes, ", ") + " and the sponsorship need is " + boolWord(need) + " in each: answered for review", true
}

var anyPlaceRE = lazyregexp.New(`^(?:any|all|multiple|various)\s+`)

// namesNoPlace reports a location made only of work-arrangement words
// ("Remote", "Any Location / Remote").
func namesNoPlace(loc string) bool {
	for _, p := range locSplitRE.Split(foldPlace(loc), -1) {
		if p = anyPlaceRE.ReplaceAllString(strings.TrimSpace(p), ""); p != "" && !placeFillerRE.MatchString(p) {
			return false
		}
	}
	return strings.TrimSpace(loc) != ""
}

var northAmericaRE = lazyregexp.New(`\bnorth america\b`)

// northAmerica reads a location that names North America and otherwise only a
// work arrangement ("Remote - North America") as the United States, Canada and
// Mexico. Only the sponsorship need reads a region this way.
func northAmerica(loc string) ([]string, bool) {
	folded := foldPlace(loc)
	rest := northAmericaRE.ReplaceAllString(folded, ",")
	if rest == folded {
		return nil, false
	}
	for _, p := range locSplitRE.Split(rest, -1) {
		if p = strings.TrimSpace(p); p != "" && !placeFillerRE.MatchString(p) {
			return nil, false
		}
	}
	return []string{"US", "CA", "MX"}, true
}

// covers reports that the recorded countries include code: the code itself, or
// a region fact that has it as a member.
func covers(recorded []string, code string) bool {
	for _, r := range recorded {
		r = strings.ToUpper(strings.TrimSpace(r))
		if r == "UK" {
			r = "GB"
		}
		switch {
		case r == code:
			return true
		case r == regionEU && euMembers[code]:
			return true
		case r == regionEEA && (euMembers[code] || code == regionEU || code == "IS" || code == "LI" || code == "NO"):
			return true
		}
	}
	return false
}

// workFactEntry reports an entry that states the applicant's work
// authorization or sponsorship need. An entry scoped to a captured question is
// the user's own answer to it and is not judged.
func workFactEntry(e *Entry) bool {
	if len(e.CapturedScope) > 0 {
		return false
	}
	return workFactEntries[e.ID] || e.EffectiveAnswerKey() == "work_authorized"
}

var workFactEntries = setOf(idSponsorshipNowOrFuture, idWorkAuthAndFutureSponsorship, idWorkAuthUS,
	"work-auth-us-any-employer", "h1b-sponsorship", "permanent-work-authorization", "us-work-authorization-status",
	"cpt-opt-work-authorization", "work-auth-based-on-visa-spouse")

// usStatusEntries hold the applicant's standing in the United States:
// citizen, national, permanent resident, refugee or asylee, U.S. person.
var usStatusEntries = setOf("us-citizen-lpr-asylee-refugee", idUSCitizenStatus, IDUSPerson)

// foreignStatusGuard keeps a U.S. status entry off a question that names other
// countries and not the United States ("Are you a Canadian citizen or
// permanent resident?"): the entry says nothing of them. The abstain is
// no-entry, not a terminal policy, so the evidence lanes may still answer
// from the recorded citizenships.
func foreignStatusGuard(s *Step, e *Entry) (Abstain, string, bool) {
	if !usStatusEntries[e.ID] {
		return Abstain{}, "", false
	}
	_, sc := stripScope(s.Q.Text, s.Req.Posting.Company)
	if len(sc.codes) == 0 || slices.Contains(sc.codes, "US") {
		return Abstain{}, "", false
	}
	return Abstain{Reason: ReasonNoEntry}, "entry " + strconv.Quote(e.ID) + " holds the U.S. status; the question asks about " + strings.Join(sc.codes, ", "), true
}

// postingCountryGuard binds a work-authorization or sponsorship question to the
// user's facts. Every country the question is about (the ones it names, and the
// posting's when it points at it or names none) must be one of the recorded
// authorized countries, or the result is the terminal posting_country abstain.
// A posting location that is present but not exactly one country is never
// guessed: it is posting_country when no country can be read from it and the
// non-terminal ambiguous abstain when it names a region, several countries or
// evidence that disagrees, unless the posting is remote across countries that
// include the recorded address's (remoteHome), which is then the country, for
// review. Only a posting with no location at all, and exactly
// one recorded country, is answered, and then for review; so is the
// sponsorship-need entry on a posting of several countries, or a question that
// names several, when the need is the same in each (uniformSponsorship). A
// question that points at the posting's country, on a posting with no location
// at all, is about the United States when the posting's own words name no
// other place (presumedInUS). A question behind a sponsorship notice that also
// asks where the applicant lives (readAuthNotice) is about the home country
// when the places it asks about include it, and reads no work fact when they do
// not. A question about the country the applicant lives in ("to work in your
// current location") is about the recorded address's country.
func postingCountryGuard(s *Step, e *Entry) (Abstain, string, bool) {
	if !workFactEntry(e) {
		return Abstain{}, "", false
	}
	if e.ID == idUSWorkAuthStatus && needsSponsorshipOnSoleOption(s) {
		// The one option stating a requirement says what the recorded need says
		// in any country, so no authorized country has to cover it; the pick
		// takes it as the sponsorship standing (sponsorshipStandingOption).
		return Abstain{}, "", false
	}
	_, sc := stripScope(s.Q.Text, s.Req.Posting.Company)
	block := Abstain{Reason: ReasonContextPolicy, Detail: DetailPostingCountry}
	codes := sc.codes
	if len(codes) == 0 && !sc.deictic && !sc.residence {
		if c, ok := statementsCountry(s); ok {
			codes = []string{c}
			s.Tracef("the statements to tick are about %s", c)
		}
	}
	presumed := false
	if sc.residence {
		// The country the applicant lives in is the recorded address's country;
		// the facts answer for it only when it is an authorized country.
		home := ""
		if p := partOf(s, "address-country"); !p.askMe {
			home, _ = CountryCode(p.text)
		}
		if home == "" {
			return block, "country mismatch: the question asks about the country you live in and the recorded address names no country", true
		}
		codes = append(codes[:len(codes):len(codes)], home)
	} else if a, ok := readAuthNotice(s.Q.Text, s.Req.Posting.Company); ok && a.residence && e.EffectiveAnswerKey() == "work_authorized" {
		home, places, pres, ok := residenceOf(s, a.ask)
		switch {
		case !ok:
			return block, "country mismatch: the question asks where you live and the posting does not say which countries it is open in", true
		case home == "":
			return Abstain{Reason: ReasonPlaceholderEmpty}, "the question asks where you live and the recorded address names no country", true
		case !slices.Contains(places, home):
			s.presumedUS = s.presumedUS || pres
			return Abstain{}, "", false
		}
		codes, presumed = append(codes[:len(codes):len(codes)], home), pres
	} else if sc.deictic || len(codes) == 0 {
		pc, state := postingCountry(s.Req.Posting)
		switch state {
		case locationResolved:
			codes = append(codes[:len(codes):len(codes)], pc)
		case locationAmbiguous:
			if line, ok := uniformSponsorship(s, e, sc.codes); ok {
				s.reviewOnly = true
				s.Tracef("%s", line)
				return Abstain{}, "", false
			}
			if home, ok := remoteHome(s); ok {
				codes = append(codes[:len(codes):len(codes)], home)
				s.reviewOnly = true
				s.Tracef("the posting is remote across countries that include the recorded address's %s, where the role would be worked: answered for %s, for review", home, home)
				break
			}
			if c, ok := recordedPlace(s, state); ok {
				codes = append(codes[:len(codes):len(codes)], c)
				break
			}
			return Abstain{Reason: ReasonAmbiguous}, "ambiguous: the posting's location names a region, several countries or conflicting places", true
		case locationUnresolved:
			if line, ok := uniformSponsorship(s, e, sc.codes); ok {
				s.reviewOnly = true
				if !slices.Contains(s.trace, line) {
					s.Tracef("%s", line)
				}
				return Abstain{}, "", false
			}
			if c, ok := recordedPlace(s, state); ok {
				codes = append(codes[:len(codes):len(codes)], c)
				break
			}
			return block, "country mismatch: the posting's location does not name exactly one country", true
		default:
			if sc.deictic {
				if !presumedInUS(s.Req.Posting) {
					return block, "country mismatch: the question is about the posting's country and the posting does not say it", true
				}
				presumed = true
				codes = append(codes[:len(codes):len(codes)], "US")
				if line := "the posting has no location and names no other country: answered as for a posting in the United States"; !slices.Contains(s.trace, line) {
					s.Tracef("%s", line)
				}
				break
			}
			switch countries := recordedCountries(s.Req.Facts.AuthorizedCountries); {
			case len(countries) == 0:
				return block, "country mismatch: the posting has no location and no authorized country is recorded", true
			case len(countries) > 1 || isRegion(countries[0]):
				return Abstain{Reason: ReasonAmbiguous}, "ambiguous: the posting has no location and the recorded authorized countries are not exactly one country", true
			}
			s.reviewOnly = true
			if line := "the posting has no location: answered for the one recorded country, for review"; !slices.Contains(s.trace, line) {
				s.Tracef("%s", line)
			}
		}
	}
	// GC AI: "Are you legally authorized to work in the United States or
	// Canada?" on a posting in the United States asks whether either holds;
	// the posting's own country, recorded as authorized, answers it.
	if e.EffectiveAnswerKey() == "work_authorized" && !sc.residence && len(sc.codes) >= 2 && namesCountriesByOr(s.Q.Text) {
		if pc, state := postingCountry(s.Req.Posting); state == locationResolved && slices.Contains(sc.codes, pc) && covers(s.Req.Facts.AuthorizedCountries, pc) {
			s.Tracef("the question asks about any of %s; the posting's country %s is one of them and is recorded as authorized", strings.Join(sc.codes, ", "), pc)
			return Abstain{}, "", false
		}
		// FutureFit AI: "Are you legally eligible to work in Canada or the
		// United States?" on a "Remote (North America)" posting, which is
		// worked from the recorded address's country.
		if home, ok := remoteHome(s); ok && slices.Contains(sc.codes, home) && covers(s.Req.Facts.AuthorizedCountries, home) {
			s.reviewOnly = true
			s.Tracef("the question asks about any of %s; the posting is remote across countries that include the recorded address's %s, recorded as authorized: for review", strings.Join(sc.codes, ", "), home)
			return Abstain{}, "", false
		}
	}
	for _, c := range codes {
		if !covers(s.Req.Facts.AuthorizedCountries, c) {
			if line, ok := uniformSponsorship(s, e, sc.codes); ok && len(sc.codes) == len(codes) {
				s.reviewOnly = true
				if !slices.Contains(s.trace, line) {
					s.Tracef("%s", line)
				}
				return Abstain{}, "", false
			}
			if !slices.ContainsFunc(codes, func(c string) bool { return covers(s.Req.Facts.AuthorizedCountries, c) }) {
				s.otherCountries = codes
			}
			return block, "country mismatch: the question is about " + c + " and the recorded authorized countries do not cover it", true
		}
	}
	s.presumedUS = s.presumedUS || presumed
	return Abstain{}, "", false
}

// statementsCountry is the one country a list of work-authorization and
// sponsorship statements names when the question names none (Ideogram:
// "Work Authorization" over "I am authorized to work in Canada" and "I will
// need sponsorship to work in Canada"): the statements are about it, whatever
// the posting says or leaves out.
func statementsCountry(s *Step) (string, bool) {
	opts := realOptions(s.Q.Options)
	labels := make([]string, len(opts))
	var codes []string
	for i, o := range opts {
		labels[i] = o.Label
		_, osc := stripScope(o.Label, s.Req.Posting.Company)
		for _, c := range osc.codes {
			if !slices.Contains(codes, c) {
				codes = append(codes, c)
			}
		}
	}
	if len(codes) != 1 || !workAuthStatementList(labels) {
		return "", false
	}
	return codes[0], true
}

// remoteHome is the recorded address's country when the posting is remote and
// open across countries that include it: a remote role is worked from where
// the applicant lives (Hightouch: "Remote (North America)" for a Charleston,
// SC address). A country named beside a region counts as well (Oneleet: "US/EU
// - Remote"). A posting with offices in several countries is not remote, and
// stays ambiguous.
func remoteHome(s *Step) (string, bool) {
	loc := s.Req.postingLocation()
	if !remoteWordRE.MatchString(loc) {
		return "", false
	}
	codes, ok := locationCountries(loc)
	if !ok {
		codes, ok = northAmerica(loc)
	}
	if !ok {
		_, codes = scanCountries(loc, true)
		ok = len(codes) > 0
	}
	p := partOf(s, "address-country")
	if !ok || p.askMe {
		return "", false
	}
	home, _ := CountryCode(p.text)
	return home, home != "" && slices.Contains(codes, home)
}

// needsSponsorshipOnSoleOption reports a user recorded as needing sponsorship
// on a picker with exactly one option stating a requirement.
func needsSponsorshipOnSoleOption(s *Step) bool {
	if need, known := sponsorshipFactOf(s); !known || !need {
		return false
	}
	n := 0
	for _, o := range realOptions(s.Q.Options) {
		if optionStatesSponsorshipRequirement(o.Label) {
			n++
		}
	}
	return n == 1
}

// presumedInUS reports whether a posting nothing places is taken to be in the
// United States (owner decision, flow round 8): its title and the head of its
// description name no country but the United States, and its title names no
// region ("EMEA Remote", "North America").
func presumedInUS(p Posting) bool { return presumedIn(p, "US") }

// presumedIn is presumedInUS for any one country.
func presumedIn(p Posting, code string) bool {
	head := p.Description
	if len(head) > jobHeadChars {
		head = head[:jobHeadChars]
	}
	_, codes := scanCountries(p.Title+"\n"+head, false)
	for _, c := range codes {
		if c != code {
			return false
		}
	}
	return !regionRE.MatchString(foldPlace(p.Title))
}

// recordedPlace places a posting whose location names no one country in the
// one country the user is recorded as authorized in, for review (owner
// decision: an unknown posting country is answered, never left; the search is
// the user's, in their own countries). A location naming no country ("Remote")
// is placed there unless the posting's title or text names another; one naming
// several countries or a region only when it lists that country. A question
// about the country an earlier answer picked is the picker's, never presumed.
func recordedPlace(s *Step, state locationState) (string, bool) {
	if previousPlacesRE.MatchString(s.Q.Text) || thatCountryRE.MatchString(s.Q.Text) {
		return "", false
	}
	countries := recordedCountries(s.Req.Facts.AuthorizedCountries)
	if len(countries) != 1 || isRegion(countries[0]) {
		return "", false
	}
	c := countries[0]
	switch state {
	case locationUnresolved:
		if !presumedIn(s.Req.Posting, c) {
			return "", false
		}
	case locationAmbiguous:
		if listed, ok := locationCountries(postingLocation(s.Req.Posting)); !ok || !slices.Contains(listed, c) {
			return "", false
		}
	default:
		return "", false
	}
	s.reviewOnly = true
	if line := "the posting's location names no one country: answered for the one recorded country, " + c + ", for review"; !slices.Contains(s.trace, line) {
		s.Tracef("%s", line)
	}
	return c, true
}

// residenceOf reads a question that asks where the applicant lives: home is the
// address's country (empty when it names none the table knows) and places the
// countries ask is about, the ones it names and the posting's when it points at
// it or names none. ok is false when the posting's countries cannot be read.
func residenceOf(s *Step, ask string) (home string, places []string, presumed, ok bool) {
	if p := partOf(s, "address-country"); !p.askMe {
		home, _ = CountryCode(p.text)
	}
	_, sc := stripScope(ask, s.Req.Posting.Company)
	places = sc.codes
	if sc.deictic || len(places) == 0 {
		codes, pres, ok := postingCountries(s.Req.Posting)
		if !ok {
			return home, nil, false, false
		}
		places, presumed = append(places[:len(places):len(places)], codes...), pres
	}
	return home, places, presumed, true
}

// postingCountries is every country the posting is open in: its one country,
// each country a several-country location lists, or the United States for a
// posting with no location that presumedInUS places there.
func postingCountries(p Posting) (codes []string, presumed, ok bool) {
	switch pc, state := postingCountry(p); state {
	case locationResolved:
		return []string{pc}, false, true
	case locationAmbiguous:
		codes, ok = locationCountries(postingLocation(p))
		return codes, false, ok
	case locationEmpty:
		if presumedInUS(p) {
			return []string{"US"}, true, true
		}
	}
	return nil, false, false
}

// recordedCountries is the distinct recorded countries, upper-cased.
func recordedCountries(recorded []string) []string {
	var out []string
	for _, r := range recorded {
		r = strings.ToUpper(strings.TrimSpace(r))
		if r == "UK" {
			r = "GB"
		}
		if r != "" && !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

func isRegion(code string) bool { return code == regionEU || code == regionEEA || code == regionEurope }

// countryOnlyRE is a field limited to one country's applicants, said in
// brackets: SoFi's "Home Address CEP (Brazil Only)", "CPF (for Brazil
// applicants only)".
var countryOnlyRE = lazyregexp.New(`(?i)\(\s*(?:for\s+)?([a-z][a-z .'-]{1,40}?)\s+(?:(?:applicants|candidates|residents|citizens)\s+)?only\s*\)`)

// otherCountryField reports a field limited to a country other than the one
// the applicant's address is in, and names that country.
func otherCountryField(s *Step) (string, bool) {
	m := countryOnlyRE.FindStringSubmatch(s.Q.Text)
	if m == nil {
		return "", false
	}
	scoped, ok := CountryCode(m[1])
	if !ok {
		return "", false
	}
	p := partOf(s, "address-country")
	if p.askMe {
		return "", false
	}
	home, ok := CountryCode(p.text)
	return m[1], ok && home != scoped
}
