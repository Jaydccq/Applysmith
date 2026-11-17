package match

import (
	"cmp"
	"strconv"
	"strings"

	"applysmith/internal/lazyregexp"
)

// A Yes/No travel question is read for two things the entry's ordinary pick
// cannot see. A stated share of time the role travels is held against the
// ceiling the entry records ("Up to 25%", the profile's max_travel_percent
// stamp): a share within it is willing, one whose least share is above it is
// not, and one that runs across it is left for review (n8n: "travelling
// approximately 30–50% of the time ... Are you able and willing to meet this
// requirement?"). "Up to X%" asks about X itself; "at least X%" has no upper
// end; "100% remote" or "50% on-site" is the work mode, not travel. And a
// question that asks for impediments or restrictions is answered the other way
// round: a willing applicant has none. A positive question that states no share
// is the entry's ordinary pick. A share in weeks a month is read as a week
// being 7/30 of the time (Eagle: "roughly M-Th 2-3 weeks/month"). A share
// above the ceiling for a limited period ("for the first 100 days") is
// willing when the travel-above-ceiling-limited-period entry says Yes.

var (
	travelCeilingRE  = lazyregexp.New(`(?i)^\s*up\s+to\s+(\d{1,3})\s*%\s*$`)
	travelSentenceRE = lazyregexp.New(`(?i)\btravell?(?:ing|s)?\b`)
	travelWorkModeRE = lazyregexp.New(`(?i)\b\d{1,3}\s*%\s*(?:remote(?:ly)?|on[- ]?site|in[- ](?:office|person)|hybrid|virtual|from\s+home)\b`)
	travelRangeRE    = lazyregexp.New(`\b(\d{1,3})\s*%?\s*(?:-|–|—|to)\s*(\d{1,3})\s*%`)
	travelAtLeastRE  = lazyregexp.New(`(?i)\b(?:at\s+least|more\s+than|over|(?:a\s+)?minimum\s+of)\s+(\d{1,3})\s*%|\b(\d{1,3})\s*%\s*(?:\+|or\s+more)`)
	travelShareRE    = lazyregexp.New(`\b(\d{1,3})\s*%`)
	travelLimitedRE  = lazyregexp.New(`(?i)\bfor\s+the\s+first\s+\S+\s+(?:days?|weeks?|months?)\b|\b(?:during|throughout)\s+(?:the\s+)?(?:onboarding|training|ramp[- ]?up)\b`)
	travelWeeksRE    = lazyregexp.New(`(?i)\b(\d)(?:\s*(?:-|–|—|to)\s*(\d))?\s+weeks?\s*(?:/|\b(?:per|a|each|every)\b)\s*month\b`)
	travelInvertedRE = lazyregexp.New(`(?i)\b(?:any|have)\s+(?:\w+\s+){0,2}(?:impediments?|restrictions?|limitations?|constraints?)\b|\b(?:prevent|stop|keep|restrict|limit|preclude)\w*\s+you\s+from\b|\bunable\s+to\s+travel\b`)
)

// travelYesNo answers the willing-to-travel entry on a bare Yes/No whose
// question states a travel share or asks for impediments.
func travelYesNo(s *Step, e *Entry) (Draft, bool) {
	if e.ID != IDWillingToTravel || !bareYesNo(s.Q.Options) {
		return Draft{}, false
	}
	inverted := travelInvertedRE.MatchString(s.Q.Text)
	var willing bool
	var why string
	ceiling, hasCeiling := travelCeiling(e)
	lo, hi, hasShare := travelShare(s.Q.Text)
	switch {
	case hasCeiling && hasShare:
		stated := strconv.Itoa(lo) + "%"
		if hi > 100 {
			stated += " or more"
		} else if hi != lo {
			stated += " to " + strconv.Itoa(hi) + "%"
		}
		if hi > ceiling && travelLimitedRE.MatchString(s.Q.Text) && limitedTravelAccepted(s) {
			willing = true
			why = "the stated share (" + stated + ") above the recorded ceiling of " + strconv.Itoa(ceiling) + "% for a limited period, which the applicant accepts"
			break
		}
		if hi > ceiling && lo <= ceiling {
			return abstainDraft(ReasonAmbiguous, "", "travel: the stated share (%s) runs across the recorded ceiling of %d%%: left for review", stated, ceiling), true
		}
		willing = hi <= ceiling
		why = "the stated share (" + stated + ") against the recorded ceiling of " + strconv.Itoa(ceiling) + "%"
	case inverted:
		d := pickWalk(s, e)
		if d.Abstain != nil {
			return d, true
		}
		yes, _ := ownYesNo(s.Q.Options, true)
		willing = d.ChosenLabel == yes.Label
		why = "the recorded answer " + quoteTerm(d.ChosenLabel) + " to the willingness"
	default:
		return Draft{}, false
	}
	o, ok := ownYesNo(s.Q.Options, willing != inverted)
	if !ok {
		return Draft{}, false
	}
	if inverted {
		why += ", asked as impediments"
	}
	d := optionDraft(optionBinding{Label: o.Label, Value: o.Value, Score: 1}, e, SourceDerived, true)
	d.Start = startProfileDerive
	d.Trace = []string{"travel: " + why + " -> " + quoteTerm(o.Label)}
	return d, true
}

// limitedTravelAccepted reports the applicant's Yes to travel above the
// ceiling for a limited period.
func limitedTravelAccepted(s *Step) bool {
	e := entryNamed(s, IDTravelLimitedPeriod)
	return e != nil && !e.Placeholder && plainYesRE.MatchString(e.Answer)
}

// travelCeiling is the "Up to N%" the entry records.
func travelCeiling(e *Entry) (int, bool) {
	for _, p := range e.Preferred {
		if m := travelCeilingRE.FindStringSubmatch(p); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n, n <= 100
		}
	}
	return 0, false
}

// travelShare reads the share of time the role travels from the sentences
// that speak of travel: lo and hi bound it, and hi above 100 has no upper end.
func travelShare(text string) (lo, hi int, ok bool) {
	var parts []string
	for _, s := range sentencesOf(text) {
		if travelSentenceRE.MatchString(s) {
			parts = append(parts, s)
		}
	}
	t := travelWorkModeRE.ReplaceAllString(strings.Join(parts, " "), "")
	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	if m := travelRangeRE.FindStringSubmatch(t); m != nil {
		lo, hi = atoi(m[1]), atoi(m[2])
		return min(lo, hi), max(lo, hi), max(lo, hi) <= 100
	}
	if m := travelAtLeastRE.FindStringSubmatch(t); m != nil {
		n := atoi(m[1] + m[2])
		return n, 101, n <= 100
	}
	if ms := travelShareRE.FindAllStringSubmatch(t, -1); len(ms) == 1 {
		n := atoi(ms[0][1])
		return n, n, n <= 100
	}
	if m := travelWeeksRE.FindStringSubmatch(t); m != nil {
		lo, hi = atoi(m[1])*700/30, atoi(cmp.Or(m[2], m[1]))*700/30
		return min(lo, hi), max(lo, hi), max(lo, hi) <= 100
	}
	return 0, 0, false
}

// travelShareAskRE asks for the share of travel the applicant accepts (AHEAD:
// "What % of travel are you open to?").
var travelShareAskRE = lazyregexp.New(`(?i)\b(?:what|how\s+much)\s+(?:%|percent(?:age)?)\s+(?:of\s+)?travel\b`)

// travelShareBox types the travel ceiling the profile stamps on the
// willing-to-travel entry ("Up to 25%") into a text box asking for the share.
func travelShareBox(s *Step) (Draft, bool) {
	q := s.Q
	if q.Kind != KindText || hasRenderedOptions(q.Options) || !travelShareAskRE.MatchString(q.Text) {
		return Draft{}, false
	}
	e := entryNamed(s, IDWillingToTravel)
	if e == nil || len(e.Preferred) == 0 || !travelCeilingRE.MatchString(e.Preferred[0]) {
		return Draft{}, false
	}
	d := derivedDraft(e.Preferred[0], e.EffectiveAnswerKey(), startProfileRow, "travel share: the profile's travel ceiling -> "+quoteTerm(e.Preferred[0]))
	d.Entry = e
	return d, true
}
