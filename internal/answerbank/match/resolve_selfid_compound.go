package match

import (
	"fmt"
	"strings"

	"applysmith/internal/lazyregexp"
)

// A compound race list (a US federal EEO-1 layout) states race and Hispanic or
// Latino status in one label: "Asian (Not Hispanic or Latino)" beside a bare
// "Hispanic or Latino"; a combined list states it by the bare option alone. The race entry alone cannot bind such a label, because
// the label also asserts a status that entry does not record. The compound
// answer is therefore built from BOTH recorded entries, and only when both
// speak: the status entry says Hispanic or not, the race entry names the race.

var (
	// notHispanicRE is the status qualifier of a compound label, parenthesised
	// or set off by a comma, semicolon or dash; Visa's says "Not Latinx or
	// Hispanic".
	notHispanicRE  = lazyregexp.New(`(?i)\s*(?:\(\s*(?:not|non)[\s-]+(?:hispanic|latin[oax])[^)]*\)|(?:[,;]|\s[-\x{2013}\x{2014}]\s)\s*(?:not|non)[\s-]+(?:hispanic|latin[oax])(?:\s+or\s+(?:latin[oax]|hispanic))?\b)`)
	trailParenRE   = lazyregexp.New(`\s*\([^)]*\)\s*$`)
	parenRE        = lazyregexp.New(`\s*\([^)]*\)`)
	hispanicOnlyRE = lazyregexp.New(`^(?:hispanic|latino|latina|latinx)(?:\s*(?:or|and|/|-)?\s*(?:hispanic|latino|latina|latinx))?$`)
	notHispanicAns = lazyregexp.New(`\b(?:not|non)[\s-]+(?:hispanic|latin[oax])`)
	hispanicAns    = lazyregexp.New(`\b(?:hispanic|latin[oax])\b`)
)

// hispanicStatus reads the recorded Hispanic or Latino status of the status
// entry: true for Hispanic, false for not. Every preferred value must say the
// same thing; a blank entry, a decline or an unreadable value is no status.
func hispanicStatus(e *Entry) (hispanic, ok bool) {
	if e == nil || e.Kind != KindPickOption || len(e.Preferred) == 0 {
		return false, false
	}
	var yes, no bool
	for _, p := range e.Preferred {
		n := normalizeLabel(p)
		switch {
		case n == "" || isDeclineText(p):
			return false, false
		case notHispanicAns.MatchString(n) || n == "no" || n == "n":
			no = true
		case hispanicAns.MatchString(n) || n == "yes" || n == "y":
			yes = true
		default:
			return false, false
		}
	}
	return yes, yes != no
}

// sharedTrailingParen is the parenthesised suffix every option ends with ("(United
// States of America)" on a Workday list), or "". It qualifies the whole list and
// says nothing about a race, so the race walk reads the labels without it.
func sharedTrailingParen(opts []Option) string {
	shared := ""
	for i, o := range opts {
		p := strings.TrimSpace(trailParenRE.FindString(o.Label))
		if p == "" || i > 0 && p != shared {
			return ""
		}
		shared = p
	}
	return shared
}

// recordedEntry is the effective bank entry of an id that holds the user's
// answer, or nil.
func (s *Step) recordedEntry(id string) *Entry {
	for i := range s.Entries {
		e := &s.Entries[i]
		if e.ID == id && e.Kind == KindPickOption && !e.RunContext && entryHoldsStance(e) {
			return e
		}
	}
	return nil
}

// compoundRaceDraft answers a compound race list from the recorded status and
// race. A recorded Hispanic or Latino status takes the bare Hispanic option
// whatever the race (the EEO-1 convention these lists follow); a recorded
// non-Hispanic status takes the compound option of the recorded race. Anything
// else is not handled, so the caller's refusal stands.
func compoundRaceDraft(s *Step) (Draft, bool) {
	if s.Entry == nil || !raceEthnicityIDs[s.Entry.ID] {
		return Draft{}, false
	}
	shared := sharedTrailingParen(s.Q.Options)
	var compound []Option
	var origin []int
	hispanicAt, qualified := -1, false
	for i, o := range s.Q.Options {
		stripped := notHispanicRE.ReplaceAllString(o.Label, "")
		switch {
		case stripped != o.Label:
			qualified = true
		case hispanicOnlyRE.MatchString(normalizeLabel(parenRE.ReplaceAllString(o.Label, ""))):
			if hispanicAt >= 0 {
				return Draft{}, false
			}
			hispanicAt = i
			continue
		}
		// A label that still names a Hispanic or Latino status is never a race of
		// a non-Hispanic applicant.
		if hispanicAns.MatchString(normalizeLabel(stripped)) {
			continue
		}
		compound = append(compound, Option{Value: o.Value, Label: strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(stripped), shared))})
		origin = append(origin, i)
	}
	// A compound list qualifies its races; a combined list (GM's and Capital
	// One's Workday) asks race and ethnicity at once, the races beside one bare
	// Hispanic or Latino option, and may qualify only the race whose EEO-1 name
	// carries the status ("Two or More Races (Not Hispanic or Latino)"). Either
	// way its other options are the races of a non-Hispanic applicant.
	if !qualified && hispanicAt < 0 {
		return Draft{}, false
	}
	hispanic, ok := hispanicStatus(s.recordedEntry("eeo-ethnicity"))
	if !ok {
		return Draft{}, false
	}
	pick := func(i int, why string) (Draft, bool) {
		o := s.Q.Options[i]
		return Draft{Answer: o.Value, ChosenLabel: o.Label, Start: declineConfidence,
			Trace: []string{fmt.Sprintf("compound race list: %s -> %q", why, o.Label)}}, true
	}
	if hispanic {
		if hispanicAt < 0 {
			return Draft{}, false
		}
		return pick(hispanicAt, "recorded Hispanic or Latino status")
	}
	race := s.recordedEntry("eeo-race")
	if race == nil {
		return Draft{}, false
	}
	byLabel := map[string]int{}
	for k, o := range compound {
		if _, dup := byLabel[o.Label]; dup {
			return Draft{}, false
		}
		byLabel[o.Label] = origin[k]
	}
	t := *s
	t.Q.Options = compound
	t.trace = nil
	d := pickWalk(&t, race)
	i, found := byLabel[d.ChosenLabel]
	if d.Abstain != nil || !found {
		return Draft{}, false
	}
	return pick(i, "recorded race with recorded non-Hispanic status")
}
