package match

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"applysmith/internal/lazyregexp"
)

// Place vocabulary of the relocation and location family: the metro alias
// table (resolve_profile_aliases.go), the US states and the policy's own home,
// listed and excluded values. Nothing here decides an answer; it reports which
// places a text names and whether two place strings are the same place.

type stateRow struct{ Abbr, Name string }

var usStateRows = []stateRow{
	{"AL", "Alabama"}, {"AK", "Alaska"}, {"AZ", "Arizona"}, {"AR", "Arkansas"}, {"CA", "California"},
	{"CO", "Colorado"}, {"CT", "Connecticut"}, {"DE", "Delaware"}, {"DC", "District of Columbia"},
	{"FL", "Florida"}, {"GA", "Georgia"}, {"HI", "Hawaii"}, {"ID", "Idaho"}, {"IL", "Illinois"},
	{"IN", "Indiana"}, {"IA", "Iowa"}, {"KS", "Kansas"}, {"KY", "Kentucky"}, {"LA", "Louisiana"},
	{"ME", "Maine"}, {"MD", "Maryland"}, {"MA", "Massachusetts"}, {"MI", "Michigan"}, {"MN", "Minnesota"},
	{"MS", "Mississippi"}, {"MO", "Missouri"}, {"MT", "Montana"}, {"NE", "Nebraska"}, {"NV", "Nevada"},
	{"NH", "New Hampshire"}, {"NJ", "New Jersey"}, {"NM", "New Mexico"}, {"NY", "New York"},
	{"NC", "North Carolina"}, {"ND", "North Dakota"}, {"OH", "Ohio"}, {"OK", "Oklahoma"}, {"OR", "Oregon"},
	{"PA", "Pennsylvania"}, {"RI", "Rhode Island"}, {"SC", "South Carolina"}, {"SD", "South Dakota"},
	{"TN", "Tennessee"}, {"TX", "Texas"}, {"UT", "Utah"}, {"VT", "Vermont"}, {"VA", "Virginia"},
	{"WA", "Washington"}, {"WV", "West Virginia"}, {"WI", "Wisconsin"}, {"WY", "Wyoming"},
}

// usNeighborPairs lists, once each, the states (and the District of Columbia)
// that share a border, a water border or a corner: "AL:FL GA" is Alabama with
// Florida and with Georgia.
const usNeighborPairs = `AL:FL GA MS TN AR:LA MO MS OK TN TX AZ:CA CO NM NV UT CA:NV OR CO:KS NE NM OK UT WY
CT:MA NY RI DC:MD VA DE:MD NJ PA FL:GA GA:NC SC TN IA:IL MN MO NE SD WI ID:MT NV OR UT WA WY IL:IN KY MI MO WI
IN:KY MI OH KS:MO NE OK KY:MO OH TN VA WV LA:MS TX MA:NH NY RI VT MD:PA VA WV ME:NH MI:MN OH WI MN:ND SD WI
MO:NE OK TN MS:TN MT:ND SD WY NC:SC TN VA ND:SD NE:SD WY NH:VT NJ:NY PA NM:OK TX UT NV:OR UT NY:PA RI VT
OH:PA WV OK:TX OR:WA PA:WV SD:WY TN:VA UT:WY VA:WV`

var usNeighbors = func() map[string][]string {
	out := map[string][]string{}
	var head string
	for _, f := range strings.Fields(usNeighborPairs) {
		if h, rest, ok := strings.Cut(f, ":"); ok {
			head, f = h, rest
		}
		out[head] = append(out[head], f)
		out[f] = append(out[f], head)
	}
	return out
}()

// usStateNeighborhood is a state with its neighbours.
func usStateNeighborhood(code string) map[string]bool {
	reach := map[string]bool{code: true}
	for _, n := range usNeighbors[code] {
		reach[n] = true
	}
	return reach
}

// usStateReach is a state with its neighbours and theirs: a daily commute
// crosses at most one state between home and work (West Virginia to the
// District of Columbia, Maine to Boston).
func usStateReach(code string) map[string]bool {
	reach := map[string]bool{code: true}
	for _, n := range usNeighbors[code] {
		reach[n] = true
		for _, m := range usNeighbors[n] {
			reach[m] = true
		}
	}
	return reach
}

var (
	stateByAbbr = map[string]string{}
	stateByName = map[string]stateRow{}
	// stateNameAlternation lists the state names longest first, so "New York" is
	// not read as "New".
	stateNameAlternation = func() string {
		names := make([]string, 0, len(usStateRows))
		for _, r := range usStateRows {
			stateByAbbr[strings.ToLower(r.Abbr)] = r.Name
			stateByName[strings.ToLower(r.Name)] = r
			names = append(names, regexp.QuoteMeta(r.Name))
		}
		sort.SliceStable(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
		return strings.Join(names, "|")
	}()
)

// usCountryRE is a lone US country text.
var usCountryRE = lazyregexp.New(`(?i)^(?:US|USA|U\.S\.?(?:A\.?)?|United States(?: of America)?)$`)

// isUSState reports a lower-cased US state (or DC) name or code.
func isUSState(s string) bool {
	_, abbr := stateByAbbr[s]
	_, name := stateByName[s]
	return abbr || name
}

// stateParenRE is the "(XX)" code a state value carries in the bank.
var stateParenRE = lazyregexp.New(`\s*\(([A-Za-z]{2})\)\s*$`)

// stateAliases returns the value first, then the other spelling of a state
// (abbreviation to name and back), de-duplicated by normalized form. A trailing
// "(XX)" is read as the code of the name before it. Unknown input returns
// itself alone.
func stateAliases(value string) []string {
	v := strings.TrimSpace(value)
	out := []string{value}
	add := func(s string) {
		for _, o := range out {
			if aliasKey(o) == aliasKey(s) {
				return
			}
		}
		out = append(out, s)
	}
	if m := stateParenRE.FindStringSubmatch(v); m != nil {
		add(strings.TrimSpace(stateParenRE.ReplaceAllString(v, "")))
		add(strings.ToUpper(m[1]))
		return out
	}
	if name, ok := stateByAbbr[strings.ToLower(v)]; ok && utf8.RuneCountInString(v) == 2 {
		add(name)
	} else if row, ok := stateByName[strings.ToLower(v)]; ok {
		add(row.Abbr)
	}
	return out
}

// stateCodeOf reads the two-letter code of a state value: the "(XX)" suffix, a
// bare code, or the code of a full name.
func stateCodeOf(v string) string {
	v = strings.TrimSpace(v)
	if m := stateParenRE.FindStringSubmatch(v); m != nil {
		return strings.ToUpper(m[1])
	}
	if _, ok := stateByAbbr[strings.ToLower(v)]; ok && utf8.RuneCountInString(v) == 2 {
		return strings.ToUpper(v)
	}
	if row, ok := stateByName[strings.ToLower(v)]; ok {
		return row.Abbr
	}
	return ""
}

// stateProse is a state value without its "(XX)" code: the name a sentence
// wants.
func stateProse(v string) string { return strings.TrimSpace(stateParenRE.ReplaceAllString(v, "")) }

// isStateName reports a full US state name.
func isStateName(v string) bool {
	_, ok := stateByName[strings.ToLower(strings.TrimSpace(v))]
	return ok
}

type placeForm struct {
	Form, Canon string
	State       bool
}

// tableForms is every alias of the metro table and every state name and
// abbreviation, longest form first.
var tableForms = func() []placeForm {
	var out []placeForm
	for _, row := range aliasRowsCity {
		for _, v := range row {
			out = append(out, placeForm{Form: v, Canon: row[0]})
		}
	}
	for _, r := range usStateRows {
		out = append(out, placeForm{Form: r.Name, Canon: r.Name, State: true}, placeForm{Form: r.Abbr, Canon: r.Name, State: true})
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].Form) > len(out[j].Form) })
	return out
}()

// shortFormMax is the length up to which a form is case-sensitive: "or", "in",
// "me" and "la" in prose never name a state.
const shortFormMax = 3

// containsForm reports the form in the text with a non-word character, or the
// text edge, on both sides. Forms of three characters or fewer match with
// their case. The boundary is spelled out rather than \b so a trailing "." as
// in "Washington D.C." works.
func containsForm(text, form string) bool { return containsFormIn(text, strings.ToLower(text), form) }

// containsFormIn is containsForm with the text's lower-case form already made.
func containsFormIn(text, lower, form string) bool {
	if form == "" {
		return false
	}
	hay, needle := text, form
	if utf8.RuneCountInString(form) > shortFormMax {
		hay, needle = lower, strings.ToLower(form)
	}
	for from := 0; from <= len(hay)-len(needle); {
		i := strings.Index(hay[from:], needle)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(needle)
		if (start == 0 || !isWordByte(hay[start-1])) && (end == len(hay) || !isWordByte(hay[end])) {
			return true
		}
		from = start + 1
	}
	return false
}

// metroAsState and codeOfAnotherState are the metro aliases that also spell
// a state. metroAsState holds, for each alias that is the code or the name of
// a state (NY, LA, New York, Washington), the pattern of the alias standing as
// that state: after a city and a comma ("Buffalo, NY", "Seattle, Washington").
// codeOfAnotherState holds the keys of those that are the code of a state the
// metro's row does not name ("LA" beside Los Angeles is Louisiana's). The
// District of Columbia is the city of Washington, so DC is in neither.
var metroAsState, codeOfAnotherState = func() (map[string]*lazyregexp.Regexp, map[string]bool) {
	states := map[string]string{}
	for _, r := range usStateRows {
		if r.Abbr != "DC" {
			states[r.Abbr], states[strings.ToLower(r.Name)] = r.Name, r.Name
		}
	}
	patterns, codes := map[string]*lazyregexp.Regexp{}, map[string]bool{}
	for _, row := range aliasRowsCity {
		for _, v := range row {
			form, name := regexp.QuoteMeta(v), states[v]
			if utf8.RuneCountInString(v) > shortFormMax {
				form, name = "(?i:"+form+")", states[strings.ToLower(v)]
			}
			if name == "" {
				continue
			}
			patterns[v] = lazyregexp.New(`(\p{L}*\p{Ll}\p{L}*\.?\s*,)\s*` + form + `(\W|$)`)
			if v != name && !slices.Contains(row, name) {
				codes[aliasKey(v)] = true
			}
		}
	}
	return patterns, codes
}()

// namesMetro reports a metro form in the text that does not stand there as
// the state it also spells.
func namesMetro(text, form string) bool {
	re, ok := metroAsState[form]
	return !ok || containsForm(re.ReplaceAllString(text, "$1$2"), form)
}

// placeKeys is every normalized spelling of one place: the metro table row and
// the state table row it belongs to, and the value itself. A metro's row gives
// the code of another state only to the code as written, so Los Angeles is
// never Louisiana.
func placeKeys(v string) map[string]bool {
	keys := map[string]bool{}
	own := aliasKey(v)
	for _, a := range expandCityAliases(v) {
		if k := aliasKey(a); k == own || !codeOfAnotherState[k] {
			keys[k] = true
		}
	}
	for _, a := range stateAliases(v) {
		keys[aliasKey(a)] = true
	}
	delete(keys, "")
	return keys
}

// sharedCode is the metro and the state a relocation list entry names when it
// is a bare code the two share ("LA": Los Angeles, and Louisiana's code).
func sharedCode(entry string) (metro, state string, ok bool) {
	if !codeOfAnotherState[aliasKey(entry)] {
		return "", "", false
	}
	return expandCityAliases(entry)[0], stateByAbbr[strings.ToLower(strings.TrimSpace(entry))], true
}

// settleSharedCodes reads a relocation list whose entries may be shared codes
// (sharedCode): beside cities and no state the code is its metro, beside
// states and no city it is its state, and the list is returned with the code
// spelled as that place. A list that does not settle a code keeps it as typed,
// and open holds the code's two places. A neighbour counts as a city when it
// is a metro, a US city or a "City, ST", as a state when it is a state's name
// or code, and as neither when it is both (NY) or unknown.
func settleSharedCodes(list []string) (settled, open []string) {
	if !slices.ContainsFunc(list, func(e string) bool { return codeOfAnotherState[aliasKey(e)] }) {
		return list, nil
	}
	cities, states := false, false
	for _, e := range list {
		v := strings.TrimSpace(e)
		if codeOfAnotherState[aliasKey(v)] {
			continue
		}
		_, tail, comma := strings.Cut(v, ",")
		state := isStateName(v) || stateCodeOf(v) != ""
		city := aliasCityIndex[aliasKey(v)] != nil || len(usCityStates[strings.ToLower(v)]) > 0 || comma && stateCodeOf(tail) != ""
		cities, states = cities || city && !state, states || state && !city
	}
	settled = slices.Clone(list)
	for i, e := range list {
		metro, state, ok := sharedCode(e)
		switch {
		case !ok:
		case cities && !states:
			settled[i] = metro
		case states && !cities:
			settled[i] = state
		default:
			open = append(open, metro, state)
		}
	}
	return settled, open
}

// ListNamesPlace reports whether a relocation list names place: an entry and
// the place share a place key, or one holds the other as whole words. A shared
// code reads as the rest of its list settles it (settleSharedCodes); open
// reports that place is a reading of a code the list leaves open.
func ListNamesPlace(list []string, place string) (named, open bool) {
	settled, codes := settleSharedCodes(list)
	keys := placeKeys(place)
	for _, c := range codes {
		open = open || keysIntersect(keys, placeKeys(c))
	}
	for _, e := range settled {
		if keysIntersect(keys, placeKeys(e)) || containsWord(e, place) || containsWord(place, e) {
			return true, open
		}
	}
	return false, open
}

func keysIntersect(a, b map[string]bool) bool {
	for k := range a {
		if b[k] {
			return true
		}
	}
	return false
}

// listHas reports a place in a list of place strings, alias-aware.
func listHas(list []string, place string) bool {
	want := placeKeys(place)
	for _, e := range list {
		if keysIntersect(placeKeys(e), want) {
			return true
		}
	}
	return false
}

func listIndex(list []string, place string) int {
	want := placeKeys(place)
	for i, e := range list {
		if keysIntersect(placeKeys(e), want) {
			return i
		}
	}
	return -1
}

// namedPlaces are the places a text names.
type namedPlaces struct {
	Cities       []string
	States       []string
	Unrecognized []string
}

func (n namedPlaces) all() []string {
	return slices.Concat(n.Cities, n.States, n.Unrecognized)
}

const properPlace = `[A-Z][\w.'-]*(?:\s+[A-Z][\w.'-]*){0,3}`

// destinationRE captures the list of capitalized places after a location
// preposition. The preposition is lower case only, and the "to" is optional
// after a relocation verb because the question sanitizer collapses repeated
// tokens in a short label ("relocate to Austin" arrives as "relocate Austin").
var destinationRE = lazyregexp.New(`\b(?:relocat\w*(?:\s+to)?|mov(?:e|ing)(?:\s+to)?|located\s+in|locations?\s+in|based\s+(?:in|out\s+of)|living\s+in|residing\s+in|resides?\s+in|work(?:ing)?\s+(?:from|in|out\s+of)|office\s+in|commute\s+to)\s+(?:the\s+|our\s+|your\s+)?(` +
	properPlace + `(?:\s*(?:,|\bor\b|\band\b)\s*(?:the\s+)?` + properPlace + `)*)`)

var (
	// placeSentenceEndRE is a period, a space and a capital: a sentence ends
	// there unless the word is an abbreviation a place carries ("St. Louis").
	// Orkes: "... Bellevue, WA. Are you currently located ..." is no "WA. Are".
	placeSentenceEndRE = lazyregexp.New(`\b(\w+)\.\s+[A-Z]`)
	placeAbbreviations = map[string]bool{"st": true, "ft": true, "mt": true, "pt": true}
	destinationSplitRE = lazyregexp.New(`\s*(?:,|\bor\b|\band\b)\s*`)
	placeSuffixRE      = lazyregexp.New(`(?i)\s+(?:Area|Metro|Region|Office|Campus|Headquarters|HQ)$`)
	leadingTheRE       = lazyregexp.New(`(?i)^the\s+`)
)

// destinationPhrases lists the capitalized places after a location
// preposition, each as written and with its shape suffix stripped.
func destinationPhrases(text string) [][2]string {
	var out [][2]string
	for _, m := range destinationRE.FindAllStringSubmatch(text, -1) {
		for _, part := range destinationSplitRE.Split(placeSentence(m[1]), -1) {
			full := strings.TrimSpace(leadingTheRE.ReplaceAllString(part, ""))
			bare := strings.TrimSpace(placeSuffixRE.ReplaceAllString(full, ""))
			if bare != "" {
				out = append(out, [2]string{full, bare})
			}
		}
	}
	return out
}

// placeSentence cuts a captured place list where its sentence ends.
func placeSentence(s string) string {
	for _, m := range placeSentenceEndRE.FindAllStringSubmatchIndex(s, -1) {
		if !placeAbbreviations[strings.ToLower(s[m[2]:m[3]])] {
			return s[:m[3]]
		}
	}
	return s
}

// homeValues are the places that identify "home": the address city, county and
// region, each region also in its other spelling.
func homeValues(p *RelocationPolicy) []string {
	if p == nil {
		return nil
	}
	var out []string
	add := func(vs ...string) {
		for _, v := range vs {
			if v = strings.TrimSpace(v); v != "" && !slices.ContainsFunc(out, func(o string) bool { return aliasKey(o) == aliasKey(v) }) {
				out = append(out, v)
			}
		}
	}
	add(p.Home.City, p.Home.County)
	if p.Home.Region != "" {
		add(stateAliases(p.Home.Region)...)
	}
	return out
}

// policyVocabulary is the vocabulary a policy makes decision-relevant.
func policyVocabulary(p *RelocationPolicy) []string {
	if p == nil {
		return nil
	}
	return slices.Concat(homeValues(p), p.Places, p.Excluded)
}

// namedPlacesWith reports the cities and states a text names, and the capitalized
// destinations that no table or policy value knows. It decides nothing.
// The policy's home region is passed too: a configured value that is that region
// is a qualifier like a state, never a city.
func namedPlacesWith(text string, extra, regions []string) namedPlaces {
	var out namedPlaces
	seenCity, seenState := map[string]bool{}, map[string]bool{}
	lower := strings.ToLower(text)
	for _, f := range tableForms {
		if !containsFormIn(text, lower, f.Form) {
			continue
		}
		if f.State && !seenState[f.Canon] {
			seenState[f.Canon] = true
			out.States = append(out.States, f.Canon)
		} else if !f.State && !seenCity[f.Canon] && namesMetro(text, f.Form) {
			seenCity[f.Canon] = true
			out.Cities = append(out.Cities, f.Canon)
		}
	}
	for _, v := range extra {
		v = strings.TrimSpace(v)
		if v == "" || !containsFormIn(text, lower, v) {
			continue
		}
		if isStateName(v) || slices.ContainsFunc(regions, func(r string) bool { return aliasKey(r) == aliasKey(v) }) {
			if !seenState[v] {
				seenState[v] = true
				out.States = append(out.States, v)
			}
		} else if !seenCity[v] {
			seenCity[v] = true
			out.Cities = append(out.Cities, v)
		}
	}
	known := map[string]bool{}
	for _, p := range slices.Concat(out.Cities, out.States) {
		known[aliasKey(p)] = true
	}
	seen := map[string]bool{}
phrases:
	for _, ph := range destinationPhrases(text) {
		for _, form := range ph {
			for k := range placeKeys(form) {
				if known[k] {
					continue phrases
				}
			}
		}
		if !seen[ph[1]] {
			seen[ph[1]] = true
			out.Unrecognized = append(out.Unrecognized, ph[1])
		}
	}
	return out
}

// StateChoices is how the address-state entry ranks a recorded region: a US
// state as its name and as "Name (XX)", any other region as written.
func StateChoices(region string) []string {
	region = strings.TrimSpace(region)
	if region == "" {
		return nil
	}
	if code := stateCodeOf(region); code != "" {
		name := stateByAbbr[strings.ToLower(code)]
		return []string{name, name + " (" + code + ")"}
	}
	return []string{region}
}

var (
	// listedStatesAskRE asks whether the applicant lives in any of the states
	// the question lists (Socure: "Do you currently reside in any of the
	// following states: DE, HI, IA, ...?").
	listedStatesAskRE = lazyregexp.New(`(?i)\b(?:live|reside|residing|located|based)\s+in\s+any\s+of\s+the\s+following\s+(?:u\.?s\.?\s+)?states\b`)
	// listedStateCodeRE is an upper-case two-letter word, a state code in a list.
	listedStateCodeRE = lazyregexp.New(`\b[A-Z]{2}\b`)
)

// listedStatesYesNo answers "do you live in any of the following states" over
// Yes and No from the recorded home state: Yes when the list names it, No when
// it names two states or more and not it. A home state that is not recorded,
// a home outside the US and a list that names fewer than two states are left.
func listedStatesYesNo(s *Step) (Draft, bool) {
	q := s.Q
	if !isYesNoOptionSet(q.Options) || !listedStatesAskRE.MatchString(q.Text) {
		return Draft{}, false
	}
	addr := s.Req.Facts.Address
	home, ok := usStateRowOf(addr.Region)
	if c := strings.TrimSpace(addr.Country); !ok || c != "" && !usCountryRE.MatchString(c) {
		return Draft{}, false
	}
	listed := statesNamedIn(q.Text)
	if len(listed) < 2 {
		return Draft{}, false
	}
	o, found := ownYesNo(q.Options, listed[home.Abbr])
	if !found {
		return Draft{}, false
	}
	d := optionDraft(optionBinding{Label: o.Label, Value: o.Value, Score: 1}, entryNamed(s, "live-in-listed-states"), SourceDerived, true)
	d.Start = startProfileDerive
	d.Trace = []string{fmt.Sprintf("listed states: the recorded home state %s against %d listed -> %s", home.Abbr, len(listed), quoteTerm(o.Label))}
	return d, true
}

var (
	// listedPlaceResidenceAskRE asks which of the places a list offers the
	// applicant lives in (Keystone: "In which Keystone office location do you
	// reside?"; "In which of the following locations do you live?"; Lighthouse:
	// "What city, state, and/or municipality are you currently based in?"),
	// or whether they live in one of them over the places (Ironclad: "Do you
	// live in one of the following metro areas?").
	listedPlaceResidenceAskRE = lazyregexp.New(`(?i)^\W*do\s+you\s+(?:currently\s+|presently\s+)?(?:live|reside)\s+in\s+(?:one|any)\s+of\s+(?:the\s+following|these)\s+(?:locations?|cit(?:y|ies)|offices?|metros?|metro\s+areas?|hubs?)\W*$|^\W*(?:in\s+)?(?:which|what)\s+(?:of\s+(?:the\s+following|these)\s+|(?:[\w.&'-]+\s+){0,3}?)(?:locations?|cit(?:y|ies)|offices?|metros?|metro\s+areas?|hubs?)(?:\s*[,/]?\s*(?:and/or|and|or)?\s+(?:state|municipality|region|province|county|area)s?){0,3}\s+(?:do|are)\s+you\s+(?:currently\s+|presently\s+)?(?:reside|live|residing|living|based|located)(?:\s+(?:in|out\s+of))?\W*$`)
	// noListedPlaceOptionRE is a place list's catch-all for a home it does not
	// name ("None - I'm looking to relocate", "None of the above", "Other").
	noListedPlaceOptionRE = lazyregexp.New(`(?i)^\W*(?:none|other|not\s+listed|n\s*/\s*a)\b`)
	// placeMoveRE is a catch-all that states a move, made or refused.
	placeMoveRE = lazyregexp.New(`(?i)\brelocat\w*|\bmov(?:e|ing)\b`)
)

// listedPlaceResidence answers which of the listed places the applicant lives
// in from the recorded home: the one option that names it, else the list's one
// catch-all when every listed place is beyond commuting reach of the home
// state (farFrom). A catch-all that states a move ("None - I'm looking to
// relocate") needs a policy that would work at one of the listed places, and
// one that refuses a move needs a policy that would work at none of them. A
// home that is not recorded, a place the tables cannot place and a list
// without one catch-all are left.
func listedPlaceResidence(s *Step) (Draft, bool) {
	q := s.Q
	if len(q.Options) < 2 || !listedPlaceResidenceAskRE.MatchString(strings.TrimSpace(q.Text)) || mselMultiAnswer(q, len(q.Options)) {
		return Draft{}, false
	}
	pol := policyOf(s)
	if pol == nil || len(homeValues(pol)) == 0 {
		return Draft{}, false
	}
	var homeOpts, catchAll []Option
	var listed namedPlaces
	acceptableElsewhere := false
	for _, o := range q.Options {
		switch {
		case isPlaceholderOption(o.Label):
			continue
		case noListedPlaceOptionRE.MatchString(o.Label):
			catchAll = append(catchAll, o)
			continue
		}
		places := optionPlaces(pol, o.Label)
		if len(places) == 0 {
			return Draft{}, false
		}
		if slices.ContainsFunc(places, func(pl string) bool { return isHome(pol, pl) }) {
			homeOpts = append(homeOpts, o)
		}
		n := namedPlacesFor(pol, o.Label)
		listed.Cities, listed.States = append(listed.Cities, n.Cities...), append(listed.States, n.States...)
		listed.Unrecognized = append(listed.Unrecognized, n.Unrecognized...)
		acceptableElsewhere = acceptableElsewhere || slices.ContainsFunc(places, func(pl string) bool { return acceptable(pol, pl) })
	}
	var chosen Option
	var why string
	switch {
	case len(homeOpts) == 1:
		chosen, why = homeOpts[0], "the option that names the recorded home"
	case len(homeOpts) > 1 || len(catchAll) != 1 || len(listed.Unrecognized) > 0 || !farFrom(pol, listed):
		return Draft{}, false
	case placeMoveRE.MatchString(catchAll[0].Label) && hasNegationToken(expandContractions(catchAll[0].Label)) == acceptableElsewhere:
		return Draft{}, false
	default:
		chosen, why = catchAll[0], "every listed place is beyond reach of the recorded home"
	}
	d := optionDraft(optionBinding{Label: chosen.Label, Value: chosen.Value, Score: 1}, entryNamed(s, "live-in-listed-states"), SourceDerived, true)
	d.Start = startProfileDerive
	d.Trace = []string{"listed places: " + why + " -> " + quoteTerm(chosen.Label)}
	return d, true
}

// statesNamedIn is the set of US state codes a text names, by code or by name.
func statesNamedIn(text string) map[string]bool {
	listed := map[string]bool{}
	for _, code := range listedStateCodeRE.FindAllString(text, -1) {
		if r, ok := usStateRowOf(code); ok {
			listed[r.Abbr] = true
		}
	}
	lower := strings.ToLower(text)
	for _, r := range usStateRows {
		if containsWord(lower, strings.ToLower(r.Name)) {
			listed[r.Abbr] = true
		}
	}
	return listed
}

// countryLikeRegions are the state codes and names that also name a country
// ("Berlin, DE", "Bangalore, IN", "Tbilisi, Georgia").
var countryLikeRegions = []string{"AR", "DE", "ID", "IL", "IN", "Georgia"}

// USRegion reports whether a place's region says the United States: a state,
// or the District of Columbia, by its code or name, other than one that also
// names a country.
func USRegion(region string) bool {
	_, ok := usStateRowOf(region)
	return ok && !slices.ContainsFunc(countryLikeRegions, func(c string) bool { return strings.EqualFold(c, strings.TrimSpace(region)) })
}

// usStateRowOf finds a state by its code or name.
func usStateRowOf(v string) (stateRow, bool) {
	v = strings.TrimSpace(v)
	for _, r := range usStateRows {
		if strings.EqualFold(v, r.Abbr) || strings.EqualFold(v, r.Name) {
			return r, true
		}
	}
	return stateRow{}, false
}
