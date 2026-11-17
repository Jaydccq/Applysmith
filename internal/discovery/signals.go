package discovery

import (
	"strconv"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Signals are the three flags computed for a posting that has text.
type Signals struct {
	EarlyCareer       bool
	ClearanceRequired bool
	// RequiredYears is the largest stated minimum, nil when none is stated.
	RequiredYears *int
}

// ComputeSignals reads the title and description text.
func ComputeSignals(title, text string) Signals {
	return Signals{
		EarlyCareer:       EarlyCareer(title, text),
		ClearanceRequired: ClearanceRequired(text),
		RequiredYears:     RequiredYears(text),
	}
}

var (
	roleNouns = `engineer|developer|analyst|scientist|designer|architect|programmer|specialist|consultant|administrator|technician|researcher|accountant|associate|manager|coordinator|representative|writer|strategist|planner|officer`
	levelRE   = lazyregexp.New(`\b(?:` + roleNouns + `)\s+(?:i|1)\b`)
	earlyRE   = lazyregexp.New(`\b(?:new\s+grads?|new\s+graduates?|graduates?|entry\s+levels?|early\s+careers?|junior|associates?|universit(?:y|ies)|interns?|internships?|co\s*ops?|limited\s+tenure|ic1|level\s+(?:1|i))\b`)
	punctRE   = lazyregexp.New(`[^\p{L}\p{N}]+`)
)

func normalizeLevelText(s string) string {
	return strings.TrimSpace(punctRE.ReplaceAllString(strings.ToLower(s), " "))
}

// EarlyCareer is the one early-career predicate. It only ever rescues or
// boosts a posting; a caller that would drop one needs its own predicate.
func EarlyCareer(title, text string) bool {
	for _, s := range []string{title, text} {
		n := normalizeLevelText(s)
		if earlyRE.MatchString(n) || levelRE.MatchString(n) {
			return true
		}
	}
	return false
}

// sentences splits text at line breaks, semicolons and periods that end a
// sentence, after dotted initialisms (U.S., T.S./S.C.I.) are collapsed.
var (
	initialismRE = lazyregexp.New(`\b(?:[A-Za-z]\.){2,}`)
	splitRE      = lazyregexp.New(`[\n\r;\x{2022}]|\.(?:\s|$)`)
)

func splitSentences(text string) []string {
	text = initialismRE.ReplaceAllStringFunc(text, func(m string) string { return strings.ReplaceAll(m, ".", "") })
	var out []string
	for _, s := range splitRE.Split(text, -1) {
		if s = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(s), "-*")); s != "" {
			out = append(out, s)
		}
	}
	return out
}

var (
	clearanceMention = lazyregexp.New(`\bclearances?\b|\btop\s+secret\b|\bts\s*/?\s*sci\b`)
	clearanceLevel   = lazyregexp.New(`\btop\s+secret\b|\bts\s*/?\s*sci\b|\bsecret\b|\bsci\b`)
	clearanceCue     = lazyregexp.New(`\b(?:must|required|requires?|requirement|shall|need(?:s|ed)?|active|current(?:ly)?|hold(?:s|ing)?|possess(?:es|ing)?|eligib(?:le|ility)|ability\s+to\s+obtain|able\s+to\s+obtain|can\s+obtain|obtain(?:ed)?\s*,?\s*and\s+maintain)\b`)
	clearanceVeto    = lazyregexp.New(`\b(?:prefer(?:red|ably)?|desired?|desirable|a\s+plus|nice\s+to\s+have|bonus|clearable|not\s+(?:be\s+)?(?:required|necessary|needed|a\s+requirement)|no\s+(?:\w+\s+){0,2}clearance|does\s+not\s+require|do\s+not\s+require|doesn'?t\s+require|without\s+(?:a\s+)?(?:\w+\s+)?clearance|(?:after|upon|following)\s+(?:hire|hiring|start|joining|being\s+hired|offer)|will\s+sponsor|sponsor(?:ed|ship)?\s+(?:your|a|the)\s+(?:\w+\s+)?clearance)\b`)
	publicTrust      = lazyregexp.New(`\bpublic\s+trust\b`)
)

// ClearanceRequired reads the text one sentence at a time: a sentence that
// requires holding, being eligible for, or being able to obtain a clearance
// counts, unless the same sentence marks it preferred, negated, public trust
// only, or obtained only after hire.
func ClearanceRequired(text string) bool {
	for _, s := range splitSentences(text) {
		s = strings.ToLower(s)
		if !clearanceMention.MatchString(s) || !clearanceCue.MatchString(s) {
			continue
		}
		if clearanceVeto.MatchString(s) {
			continue
		}
		if publicTrust.MatchString(s) && !clearanceLevel.MatchString(s) {
			continue
		}
		return true
	}
	return false
}

var (
	preferredHeading = lazyregexp.New(`^(?:preferred|nice\s+to\s+have|bonus|desired|pluses|good\s+to\s+have|ideal|what\s+would\s+make\s+you\s+stand\s+out|extra\s+credit)\b`)
	requiredHeading  = lazyregexp.New(`^(?:required|minimum|basic|must\s+have|requirements?|qualifications?|what\s+you(?:'ll|\s+will)?\s+(?:need|bring)|who\s+you\s+are|what\s+we(?:'re|\s+are)\s+looking\s+for|about\s+you|you\s+have|responsibilities)\b`)
	preferredMarker  = lazyregexp.New(`\b(?:prefer(?:red|ably)?|nice\s+to\s+have|bonus|a\s+plus|desirable|desired|ideally|is\s+a\s+plus|plus)\b`)
	requirementCue   = lazyregexp.New(`\b(?:requires?|required|must\s+have|minimum|at\s+least|must\s+possess|need)\b`)
	historyMarker    = lazyregexp.New(`\b(?:we\s+(?:have|'ve)\s+been|we\s+were\s+founded|our\s+(?:team|engineers|people|leaders|leadership|staff|company|founders?)|on\s+(?:the|our)\s+(?:\w+\s+)?team|many\s+(?:of\s+whom|with)|colleagues|founded|since\s+\d{4}|years\s+in\s+business|years\s+of\s+history|over\s+\d+\s+years)\b`)
	experienceWord   = lazyregexp.New(`experience|\bexp\b|proficien|expertise|background|working|worked|industry|professional`)
	yearsRangeRE     = lazyregexp.New(`(\d{1,2})\s*(?:-|\x{2013}|to)\s*(\d{1,2})\s*\+?\s*(?:years?|yrs?)\b`)
	yearsPlusRE      = lazyregexp.New(`(\d{1,2})\s*\+\s*(?:years?|yrs?)\b`)
	yearsOrMoreRE    = lazyregexp.New(`(\d{1,2})\s+or\s+more\s+(?:years?|yrs?)\b`)
	yearsAtLeastRE   = lazyregexp.New(`(?:at\s+least|minimum(?:\s+of)?|min\.?)\s+(\d{1,2})\s*(?:years?|yrs?)\b`)
	yearsPlainRE     = lazyregexp.New(`\b(\d{1,2})\s+(?:years?|yrs?)\s+(?:of\s+)?(?:\w+\s+){0,3}?experience\b`)
	numberWords      = strings.NewReplacer("one", "1", "two", "2", "three", "3", "four", "4", "five", "5", "six", "6", "seven", "7", "eight", "8", "nine", "9", "ten", "10")
	numberWordRE     = lazyregexp.New(`\b(?:one|two|three|four|five|six|seven|eight|nine|ten)\b(?:\s*\+|\s+or\s+more|\s*-\s*\w+)?\s+(?:years?|yrs?)\b`)
)

// ageYearsRE is an age floor ("at least 18 years of age", "21 years old"), never experience.
var ageYearsRE = lazyregexp.New(`(?i)\b\d{1,2}\s*\+?\s*(?:years?|yrs?)\s+(?:of\s+age|old|or\s+older)\b`)

const maxRequiredYears = 30

// RequiredYears extracts the largest stated minimum of years of experience.
// A figure on a sentence that states a requirement outranks any incidental
// mention; preferred sentences and everything under a preferred heading (until
// a required heading reopens the scope) are ignored; a range contributes its
// lower bound.
func RequiredYears(text string) *int {
	preferred := false
	var required, incidental []int
	for _, seg := range splitSentences(text) {
		low := strings.ToLower(seg)
		if len(low) <= 60 {
			head := strings.TrimSpace(strings.Trim(low, " :-*#"))
			switch {
			case preferredHeading.MatchString(head):
				preferred = true
				continue
			case requiredHeading.MatchString(head) && !yearsAny(head):
				preferred = false
				continue
			}
		}
		if preferred || preferredMarker.MatchString(low) || historyMarker.MatchString(low) {
			continue
		}
		low = numberWordRE.ReplaceAllStringFunc(low, func(m string) string { return numberWords.Replace(m) })
		vals := yearFigures(low)
		if len(vals) == 0 || !(requirementCue.MatchString(low) || experienceWord.MatchString(low)) {
			continue
		}
		if requirementCue.MatchString(low) {
			required = append(required, vals...)
		} else {
			incidental = append(incidental, vals...)
		}
	}
	pool := required
	if len(pool) == 0 {
		pool = incidental
	}
	if len(pool) == 0 {
		return nil
	}
	best := pool[0]
	for _, v := range pool {
		best = max(best, v)
	}
	best = min(best, maxRequiredYears)
	return &best
}

func yearsAny(s string) bool { return len(yearFigures(s)) > 0 }

// yearFigures returns the minimum of each years phrase in a sentence.
func yearFigures(s string) []int {
	var out []int
	s = ageYearsRE.ReplaceAllString(s, " ")
	add := func(re *lazyregexp.Regexp, group int) {
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			if n, err := strconv.Atoi(m[group]); err == nil {
				out = append(out, n)
			}
		}
	}
	add(yearsRangeRE, 1)
	rest := yearsRangeRE.ReplaceAllString(s, " ")
	for _, re := range []*lazyregexp.Regexp{yearsPlusRE, yearsOrMoreRE, yearsAtLeastRE, yearsPlainRE} {
		for _, m := range re.FindAllStringSubmatch(rest, -1) {
			if n, err := strconv.Atoi(m[1]); err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}
