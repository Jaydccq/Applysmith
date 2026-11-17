package match

import (
	"slices"
	"sort"
	"strings"

	"applysmith/internal/lazyregexp"
)

// The relocation policy and its three predicates. Every question about where
// the applicant will work is decided from one policy built from
// preferences.relocation and the home address. The predicates are three-valued
// and an unconfigured policy (nil) makes every one of them unknown, so the
// bound entry answers from its own body.

type verdict int

const (
	verdictUnknown verdict = iota
	verdictNo
	verdictYes
)

func (v verdict) String() string {
	switch v {
	case verdictYes:
		return "yes"
	case verdictNo:
		return "no"
	}
	return "unknown"
}

// jobPlace is where the role is performed, as read from the posting.
type jobPlace struct {
	City, Region, Country string
	Remote                bool
}

// finest is the most specific place the posting gives: the city, else the
// region, else the country.
func (j jobPlace) finest() string {
	for _, v := range []string{j.City, j.Region, j.Country} {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// known reports a posting that names any place.
func (j jobPlace) known() bool { return j.finest() != "" }

// homeFinest is the finest granularity used to compare the posting with home:
// the city, else the region; the country alone never counts.
func (j jobPlace) homeFinest() string {
	for _, v := range []string{j.City, j.Region} {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// isHome reports a place that is one of the home values.
func isHome(p *RelocationPolicy, place string) bool { return listHas(homeValues(p), place) }

// listed and excluded are the policy's two lists as the predicates read them:
// a code a metro and another state share is the place its own list makes it
// (settleSharedCodes).
func (p *RelocationPolicy) listed() []string {
	out, _ := settleSharedCodes(p.Places)
	return out
}

func (p *RelocationPolicy) excluded() []string {
	out, _ := settleSharedCodes(p.Excluded)
	return out
}

// openSharedCode reports a place of the question that the policy lists only as
// a shared code its list does not settle: the question's text and options name
// one of the code's two places, or the posting's place is one. The predicates
// would decide such a place by a reading the user never chose.
func openSharedCode(p *RelocationPolicy, s *Step) (string, bool) {
	_, open := settleSharedCodes(p.Places)
	_, more := settleSharedCodes(p.Excluded)
	if open = append(open, more...); len(open) == 0 {
		return "", false
	}
	text := s.Q.Text
	for _, o := range s.Q.Options {
		text += "\n" + o.Label
	}
	places := questionPlaces(p, text)
	if job := jobPtr(s.Req); job != nil && !job.Remote && job.finest() != "" {
		places = append(places, job.finest())
	}
	for _, pl := range places {
		for _, reading := range open {
			if keysIntersect(placeKeys(pl), placeKeys(reading)) {
				return pl, true
			}
		}
	}
	return "", false
}

// sharedCodeGuard holds a question of an entry bound to the relocation policy
// when it turns on a shared code the list leaves open (openSharedCode).
func sharedCodeGuard(s *Step, e *Entry) (Abstain, string, bool) {
	if e.LocationPolicy == nil || e.LocationPolicy.Resolved == nil {
		return Abstain{}, "", false
	}
	pl, hit := openSharedCode(e.LocationPolicy.Resolved, s)
	if !hit {
		return Abstain{}, "", false
	}
	return Abstain{Reason: ReasonPlaceholderEmpty}, sharedCodeTrace(pl), true
}

func sharedCodeTrace(place string) string {
	return "the relocation list names " + quoteTerm(place) + " only by a code that is also another place, and the rest of the list does not say which -> abstain"
}

// heldOnSharedCode runs a lane that answers from the relocation policy without
// a bound entry, and holds its answer when the question turns on a shared code
// the list leaves open (openSharedCode).
func heldOnSharedCode(lane StageFunc) StageFunc {
	return func(s *Step) (Draft, bool) {
		d, ok := lane(s)
		if !ok || d.Abstain != nil {
			return d, ok
		}
		if pol := policyOf(s); pol != nil {
			if pl, hit := openSharedCode(pol, s); hit {
				return abstainDraft(ReasonPlaceholderEmpty, "", "%s", sharedCodeTrace(pl)), true
			}
		}
		return d, true
	}
}

// acceptable is THE acceptability rule: refused when excluded (even at home and
// even under open); accepted at home or when listed; otherwise the default
// decides.
func acceptable(p *RelocationPolicy, place string) bool {
	switch {
	case listHas(p.excluded(), place):
		return false
	case isHome(p, place), listHas(p.listed(), place):
		return true
	}
	return p.Default != RelocationClosed
}

// unnamedVerdict answers a relocation question that names no place: open is yes
// and closed is yes only when at least one place is listed.
func unnamedVerdict(p *RelocationPolicy) verdict {
	if p.Default != RelocationClosed || len(p.Places) > 0 {
		return verdictYes
	}
	return verdictNo
}

func verdictForPlaces(p *RelocationPolicy, places []string) verdict {
	if len(places) == 0 {
		return unnamedVerdict(p)
	}
	for _, pl := range places {
		if acceptable(p, pl) {
			return verdictYes
		}
	}
	return verdictNo
}

// namedPlacesFor reads a text under this policy's vocabulary.
func namedPlacesFor(p *RelocationPolicy, text string) namedPlaces {
	var regions []string
	if p.Home.Region != "" {
		regions = stateAliases(p.Home.Region)
	}
	return namedPlacesWith(text, policyVocabulary(p), regions)
}

// questionPlaces are the places a text names under this policy's vocabulary.
func questionPlaces(p *RelocationPolicy, text string) []string {
	return namedPlacesFor(p, text).all()
}

// willingToWorkAt: yes when any named place is acceptable. With no named place
// the posting's place is evaluated instead (remote is yes); with neither the
// unnamed-destination verdict applies. Living somewhere counts as willing to
// work there, which is what collapses "located in X or willing to relocate to X".
func willingToWorkAt(p *RelocationPolicy, text string, job *jobPlace) verdict {
	if p == nil {
		return verdictUnknown
	}
	named := questionPlaces(p, text)
	if len(named) == 0 && job != nil {
		if job.Remote {
			return verdictYes
		}
		if f := job.finest(); f != "" {
			return verdictForPlaces(p, []string{f})
		}
	}
	return verdictForPlaces(p, named)
}

// currentlyAt is a fact read only from the home places; the preference is never
// consulted. With no named place the posting's finest place can promote it to
// yes when that place is home, and never to no.
func currentlyAt(p *RelocationPolicy, text string, job *jobPlace) verdict {
	if p == nil {
		return verdictUnknown
	}
	if named := questionPlaces(p, text); len(named) > 0 {
		for _, pl := range named {
			if isHome(p, pl) {
				return verdictYes
			}
		}
		return verdictNo
	}
	if job != nil {
		if f := job.homeFinest(); f != "" && isHome(p, f) {
			return verdictYes
		}
	}
	return verdictUnknown
}

// requiresRelocation: a remote posting is no, being at the place is no, a known
// posting place is folded into the question's places and judged by the
// acceptability rule, and otherwise the willingness verdict of the text. It is
// never a bare yes asserted from the posting alone.
func requiresRelocation(p *RelocationPolicy, text string, job *jobPlace) verdict {
	if p == nil {
		return verdictUnknown
	}
	if job != nil && job.Remote {
		return verdictNo
	}
	if currentlyAt(p, text, job) == verdictYes {
		return verdictNo
	}
	if job != nil {
		if f := job.finest(); f != "" {
			return verdictForPlaces(p, append(questionPlaces(p, text), f))
		}
	}
	return willingToWorkAt(p, text, nil)
}

// predicateVerdict evaluates one of the three predicates a binding names.
func predicateVerdict(p *RelocationPolicy, predicate, text string, job *jobPlace) verdict {
	switch predicate {
	case PredicateWillingToWorkAt:
		return willingToWorkAt(p, text, job)
	case PredicateCurrentlyAt:
		return currentlyAt(p, text, job)
	case PredicateRequiresRelocation:
		return requiresRelocation(p, text, job)
	}
	return verdictUnknown
}

// rankedOption is a rendered option as the policy sees it.
type rankedOption struct {
	Label      string
	Index      int
	Acceptable bool
	// Rank is the position in the places list, -1 for an unlisted place.
	Rank   int
	Reason string // excluded, home, listed or unlisted
}

type placeRead struct {
	acceptable bool
	rank       int
	reason     string
}

// readPlace is the per-place verdict for ranking: listed is checked before
// home, so a home place that is also listed ranks by its list position.
func readPlace(p *RelocationPolicy, place string) placeRead {
	if listHas(p.excluded(), place) {
		return placeRead{false, -1, "excluded"}
	}
	if i := listIndex(p.listed(), place); i >= 0 {
		return placeRead{true, i, "listed"}
	}
	if isHome(p, place) {
		return placeRead{true, -1, "home"}
	}
	return placeRead{p.Default != RelocationClosed, -1, "unlisted"}
}

// optionPlaces is every place an option label names. A state beside a city is
// that city's qualifier, so states decide only when the label names no city.
func optionPlaces(p *RelocationPolicy, label string) []string {
	n := namedPlacesFor(p, label)
	if cities := slices.Concat(n.Cities, n.Unrecognized); len(cities) > 0 {
		return cities
	}
	if city, ok := unknownCityBesideState(label, n.States); ok {
		return []string{city}
	}
	return n.States
}

var cityHeadRE = lazyregexp.New(`^` + properPlace + `$`)

// unknownCityBesideState reads "<city>, <state>" where the tables do not know
// the city: the city is its own place and the state only its qualifier, so a
// city the user never listed is not home just because it sits in the home
// state. A label whose head is a generic place word ("Remote, US", "Texas
// offices") keeps the state-level reading.
func unknownCityBesideState(label string, states []string) (string, bool) {
	head, rest, ok := strings.Cut(strings.TrimSpace(label), ",")
	head, rest = strings.TrimSpace(head), strings.TrimSpace(rest)
	if !ok || len(states) == 0 || !cityHeadRE.MatchString(head) || placeSuffixRE.MatchString(" "+head) || genericPlaceHeadRE.MatchString(head) {
		return "", false
	}
	if isStateName(head) || stateCodeOf(head) != "" {
		return "", false
	}
	for _, st := range states {
		if keysIntersect(placeKeys(rest), placeKeys(st)) {
			return head, true
		}
	}
	return "", false
}

var genericPlaceHeadRE = lazyregexp.New(`(?i)^(?:remote|hybrid|on-?site|anywhere|other|multiple|various|united states|usa?|u\.s\.a?\.?)\b`)

// rankRenderedPlaces ranks the labels by the policy: an unlisted home first,
// then listed places in list order, then unlisted places (acceptable under open
// only); the rendered order breaks every tie. An option naming no known place
// is an unlisted place. An option naming several places is refused only when
// every one is, and ranks by its best listed place.
func rankRenderedPlaces(p *RelocationPolicy, labels []string) []rankedOption {
	ranked := make([]rankedOption, len(labels))
	for i, label := range labels {
		places := optionPlaces(p, label)
		if len(places) == 0 {
			places = []string{label}
		}
		var best *placeRead
		for _, pl := range places {
			r := readPlace(p, pl)
			if !r.acceptable {
				continue
			}
			if best == nil || rankOrInf(r.rank) < rankOrInf(best.rank) {
				r := r
				best = &r
			}
		}
		if best == nil {
			ranked[i] = rankedOption{Label: label, Index: i, Rank: -1, Reason: "excluded"}
			continue
		}
		ranked[i] = rankedOption{Label: label, Index: i, Acceptable: true, Rank: best.rank, Reason: best.reason}
	}
	key := func(r rankedOption) int {
		switch r.Reason {
		case "home":
			return -1
		case "listed":
			return r.Rank
		}
		return 1 << 30
	}
	sort.SliceStable(ranked, func(a, b int) bool {
		if ka, kb := key(ranked[a]), key(ranked[b]); ka != kb {
			return ka < kb
		}
		return ranked[a].Index < ranked[b].Index
	})
	return ranked
}

func rankOrInf(rank int) int {
	if rank < 0 {
		return 1 << 30
	}
	return rank
}
