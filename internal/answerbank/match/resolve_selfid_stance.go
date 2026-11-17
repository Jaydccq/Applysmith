package match

import (
	"fmt"
	"slices"
	"strings"

	"applysmith/internal/lazyregexp"
)

// The stance model binds veteran and disability text to CONCEPTS, not strings.
// A stance is a SET of mutually exclusive worlds: "I am not a protected
// veteran" is true of a non-veteran and of an unprotected veteran, so a single
// label would have to guess. Race and Hispanic ethnicity are deliberately not
// modelled: race options carry parenthetical qualifiers ("Asian (Not Hispanic
// or Latino)"), so an ethnicity answer would bind every race option at once.

type stanceConcept string

const (
	conceptVeteran    stanceConcept = "veteran"
	conceptDisability stanceConcept = "disability"
)

const (
	worldProtected   = "veteran:protected"
	worldUnprotected = "veteran:veteran-unprotected"
	worldNonVeteran  = "veteran:non-veteran"
	worldHas         = "disability:has"
	worldNone        = "disability:none"
)

// conceptUniverse is every world of a concept; a bare "No" is the complement of
// what the question asks within it.
var conceptUniverse = map[stanceConcept][]string{
	conceptVeteran:    {worldProtected, worldUnprotected, worldNonVeteran},
	conceptDisability: {worldHas, worldNone},
}

// stanceReading is the set of worlds in which a text is true. It is never empty.
type stanceReading struct {
	concept stanceConcept
	worlds  []string
}

func (r stanceReading) has(w string) bool { return slices.Contains(r.worlds, w) }

func (r stanceReading) format() string {
	short := make([]string, len(r.worlds))
	for i, w := range r.worlds {
		short[i] = w[strings.IndexByte(w, ':')+1:]
	}
	slices.Sort(short)
	return strings.Join(short, "|")
}

func stanceOf(c stanceConcept, worlds ...string) (stanceReading, bool) {
	return stanceReading{c, worlds}, true
}

func anyToken(set map[string]bool, tokens ...string) bool {
	for _, t := range tokens {
		if set[t] {
			return true
		}
	}
	return false
}

// thirdPartyRE marks a text about someone else's status: "spouse of a veteran"
// and "family member with a disability" are not the applicant's stance.
var thirdPartyRE = lazyregexp.New(`\b(?:spouse|spousal|wife|husband|partner|family member|relative|friend|child|children|son|daughter|parent|sibling|dependent|caregiver|loved one|someone|person with|people with|individual with|member of my)\b`)

// readStance reads a stored answer or an option label into the worlds in which
// it is true. It reports false, "no conclusion, defer", when the concept word
// is absent, the text is a refusal or the negation scope contradicts itself.
func readStance(text string) (stanceReading, bool) {
	if strings.TrimSpace(text) == "" || isDeclineText(text) || thirdPartyRE.MatchString(strings.ToLower(text)) {
		return stanceReading{}, false
	}
	aff, neg := negationScope(text)
	vetAff, vetNeg := anyToken(aff, "veteran", "veterans"), anyToken(neg, "veteran", "veterans")
	if vetAff || vetNeg {
		protAff, protNeg := aff["protected"], neg["protected"]
		switch {
		case vetAff && vetNeg:
			// "a veteran, but not a protected veteran": only the qualifier makes
			// the clause boundary coherent.
			if protNeg {
				return stanceOf(conceptVeteran, worldUnprotected)
			}
			return stanceReading{}, false
		case vetNeg:
			if protNeg {
				return stanceOf(conceptVeteran, worldUnprotected, worldNonVeteran)
			}
			return stanceOf(conceptVeteran, worldNonVeteran)
		case protAff:
			return stanceOf(conceptVeteran, worldProtected)
		}
		return stanceOf(conceptVeteran, worldProtected, worldUnprotected)
	}
	disAff, disNeg := anyToken(aff, "disability", "disabilities", "disabled"), anyToken(neg, "disability", "disabilities", "disabled")
	if disAff != disNeg {
		if disAff {
			return stanceOf(conceptDisability, worldHas)
		}
		return stanceOf(conceptDisability, worldNone)
	}
	return stanceReading{}, false
}

// barePolarity reads a label that says nothing but yes or no.
func barePolarity(label string) (yes, ok bool) {
	switch normalizeLabel(label) {
	case "yes", "y":
		return true, true
	case "no", "n":
		return false, true
	}
	return false, false
}

// readOptionStance reads an option label, falling back to the question's scope
// when the label is a bare polarity word: the concept comes from the question,
// the polarity from the option, and neither alone is enough. "yes" and "no" are
// never concept words, or every Yes/No on every form would read as a stance.
func readOptionStance(label, questionText string) (stanceReading, bool) {
	if r, ok := readStance(label); ok {
		return r, true
	}
	if questionText == "" {
		return stanceReading{}, false
	}
	yes, ok := barePolarity(label)
	if !ok {
		return stanceReading{}, false
	}
	asked, ok := readStance(questionText)
	if !ok {
		return stanceReading{}, false
	}
	if yes {
		return asked, true
	}
	var rest []string
	for _, w := range conceptUniverse[asked.concept] {
		if !asked.has(w) {
			rest = append(rest, w)
		}
	}
	if len(rest) == 0 {
		return stanceReading{}, false
	}
	return stanceReading{asked.concept, rest}, true
}

const (
	scoreStanceExact    = 0.98
	scoreStanceEntailed = 0.94
)

type stanceDecision struct {
	score float64
	why   string
}

// stanceVerdictOf compares the answer's worlds with the option's. Disjoint sets
// reject (score 0); equal sets match at 0.98; an option true for the user but
// broader matches at 0.94; every other relation, including an option narrower
// than the answer, defers to the later rules so this layer can only add a
// decision or remove a false one.
func stanceVerdictOf(a *answerProfile, l *labelProfile) (stanceDecision, bool) {
	if !a.hasStanc {
		return stanceDecision{}, false
	}
	opt, ok := l.stanceOf()
	if !ok || opt.concept != a.stance.concept {
		return stanceDecision{}, false
	}
	ans := a.stance
	shared := 0
	for _, w := range ans.worlds {
		if opt.has(w) {
			shared++
		}
	}
	switch {
	case shared == 0:
		return stanceDecision{0, fmt.Sprintf("rejected: %s stance conflict (answer=%s option=%s)", ans.concept, ans.format(), opt.format())}, true
	case shared != len(ans.worlds):
		return stanceDecision{}, false
	case len(ans.worlds) == len(opt.worlds):
		return stanceDecision{scoreStanceExact, fmt.Sprintf("%s-stance exact (%s)", ans.concept, ans.format())}, true
	}
	return stanceDecision{scoreStanceEntailed, fmt.Sprintf("%s-stance entailed (%s within %s)", ans.concept, ans.format(), opt.format())}, true
}

var (
	// transitioningServiceRE also takes an active-duty service member and the
	// DoD SkillBridge program, open only to those leaving service (Axonius).
	transitioningServiceRE = lazyregexp.New(`(?i)\b(?:transitioning|separating|active[- ]duty)\s+(?:military\s+)?service\s*members?\b|\bskill\s*bridge\b`)
	neverServedRE          = lazyregexp.New(`(?i)\b(?:not|never)\s+(?:\w+\s+){0,2}served\b|\bno\s+military\s+service\b`)
)

// transitioningServiceMember answers "Are you a transitioning service member?"
// (Shield AI), or an active-duty one (Axonius), from the user's own
// military-service-status entry: No when it says they never served. A user
// who served may or may not be transitioning, which no entry records, so every
// other case is left to them; a served stance never maps to Yes.
func transitioningServiceMember(s *Step) (Draft, bool) {
	if !transitioningServiceRE.MatchString(s.Q.Text) {
		return Draft{}, false
	}
	no, ok := ownYesNo(s.Q.Options, false)
	if _, yes := ownYesNo(s.Q.Options, true); !ok || !yes {
		return Draft{}, false
	}
	stance := ownText(entryNamed(s, "military-service-status"))
	if !neverServedRE.MatchString(stance) {
		return abstainDraft(ReasonAmbiguous, "", "transitioning service member: the military-service entry (%q) does not say you never served -> left for you", stance), true
	}
	d := derivedDraft(no.Label, "", startProfileDerive, fmt.Sprintf("transitioning service member: your military-service entry says %q -> %q", stance, no.Label))
	d.ChosenLabel = no.Label
	d.Deterministic = true
	return d, true
}

// militaryAskRE asks about veteran status or military service.
var militaryAskRE = lazyregexp.New(`(?i)\bveterans?\b|\bserv(?:e|ed|ing|ice)\b[^?]*\bmilitary\b|\bmilitary\s+service\b`)

// noMilitaryService answers a veteran or military-service list (Reddit: "Are
// you a veteran/have you served in the military?" over kinds of service and
// "No military service") from the user's own military-service-status entry:
// when it says they never served, the list's one option saying there was no
// service is chosen. That option is true of no veteran, so it answers the
// veteran half too. Any other stance, a question about someone else, or a list
// without exactly one such option is left to the other lanes.
func noMilitaryService(s *Step) (Draft, bool) {
	if !militaryAskRE.MatchString(s.Q.Text) || thirdPartyRE.MatchString(strings.ToLower(s.Q.Text)) {
		return Draft{}, false
	}
	var none []Option
	for _, o := range realOptions(s.Q.Options) {
		if neverServedRE.MatchString(o.Label) {
			none = append(none, o)
		}
	}
	if len(none) != 1 {
		return Draft{}, false
	}
	stance := ownText(entryNamed(s, "military-service-status"))
	if !neverServedRE.MatchString(stance) {
		return Draft{}, false
	}
	d := derivedDraft(none[0].Label, "", startProfileDerive, fmt.Sprintf("no military service: your military-service entry says %q -> %q", stance, none[0].Label))
	d.ChosenLabel = none[0].Label
	d.Deterministic = true
	return d, true
}

var (
	// usGovernmentAskRE asks whether the applicant worked for the U.S.
	// government.
	usGovernmentAskRE = lazyregexp.New(`(?i)\b(?:employed\s+(?:by|with)|work(?:ed|ing)?\s+(?:for|with)|(?:an?\s+)?employee\s+of)\s+(?:the\s+|a\s+|any\s+)?(?:u\.?s\.?\s+|united\s+states\s+|federal\s+)?government\b`)
	// usGovernmentElsewhereRE is a question the government routes own: a
	// senior official, a family member's employment, a client or contractor,
	// or a list of public employers wider than a government (route 611).
	usGovernmentElsewhereRE = lazyregexp.New(`(?i)\bsenior\s+government\s+official\b|\bimmediate\s+family\b|\bclients?\b|\bcontractors?\b|\bstate[-\s]owned\b|\bpublicly[-\s]funded\b`)
	militaryWordRE          = lazyregexp.New(`(?i)\bmilitary\b`)
	// postGovernmentRE asks for the limits a former government employee
	// carries into a new job (Scale AI: "restrictions on post-government
	// employment").
	postGovernmentRE = lazyregexp.New(`(?i)\bpost[-\s]government\b`)
)

// usGovernmentWithMilitary answers a U.S. government employment question that
// counts the military in it (Skydio: "Do you currently, or have you in the
// last 10 years, worked for the US government (e.g., Congressional staffer,
// member of military, state, or federal agencies)?"), and one about the
// restrictions a former government employee carries (Scale AI: "restrictions
// on post-government employment"). It takes two of the user's own entries, so
// it is No only when the government employment entry says No and the
// military-service entry says they never served; anything else is left to the
// user, as one entry alone does not answer it. A text box with no options
// takes a plain "No" (Scale AI asks both that way).
func usGovernmentWithMilitary(s *Step) (Draft, bool) {
	t := s.Q.Text
	if !(usGovernmentAskRE.MatchString(t) && militaryWordRE.MatchString(t) || postGovernmentRE.MatchString(t)) || usGovernmentElsewhereRE.MatchString(t) || thirdPartyRE.MatchString(strings.ToLower(t)) {
		return Draft{}, false
	}
	answer, text := "No", s.Q.Kind == KindText && len(realOptions(s.Q.Options)) == 0
	if !text {
		no, ok := ownYesNo(s.Q.Options, false)
		if _, yes := ownYesNo(s.Q.Options, true); !ok || !yes {
			return Draft{}, false
		}
		answer = no.Label
	}
	gov := ownText(entryNamed(s, "government-employee-history"))
	stance := ownText(entryNamed(s, "military-service-status"))
	if !noAnswers[normalizeLabel(gov)] || !neverServedRE.MatchString(stance) {
		return abstainDraft(ReasonAmbiguous, "", "U.S. government incl. the military: your government employment entry (%q) and military-service entry (%q) do not both say none -> left for you", gov, stance), true
	}
	d := derivedDraft(answer, "", startProfileDerive, fmt.Sprintf("U.S. government incl. the military: your government employment entry says %q and your military-service entry %q -> %q", gov, stance, answer))
	if !text {
		d.ChosenLabel = answer
	}
	d.Deterministic = true
	return d, true
}
