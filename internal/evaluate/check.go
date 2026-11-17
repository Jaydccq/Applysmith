// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"fmt"
	"strings"

	"applysmith/internal/discovery"
	"applysmith/internal/gates"
	"applysmith/internal/lazyregexp"
)

// CheckContext is what the cross-checks compare the model's items against.
type CheckContext struct {
	Stance gates.Stance
	// ProfileYears is the profile's years of experience; nil when unknown.
	ProfileYears *float64
	// Need is the profile's sponsorship need. It decides work-authorization
	// must-haves; the zero value applies the stance rules.
	Need Need
}

// Checked is the model's items after the deterministic checks.
type Checked struct {
	Must, Nice, Hidden, Align []Item
	RedFlags                  []Flag
	// Unsupported lists the ids of items set to 0.0 for lacking evidence.
	Unsupported []string
	// Demotions records gate classes without a trigger (cap 5.0, no effect).
	Demotions []Cap
	// MustMisses counts must-haves at hit level 0, demoted gates included.
	MustMisses int
	// HonoredGate is a missed must-have whose gate class has a trigger.
	HonoredGate bool
	HonoredIDs  []string
}

var evidenceSource = lazyregexp.New(`(?i)\b(cv|r[e\x{00e9}]sum[e\x{00e9}]|narrative|digest)\b|knowledge[:\s#\[]*\w|\btargets:`)

// NamesProfileSource reports whether evidence names a profile source: the CV,
// the narrative, the profile's targets ("targets:", its target roles,
// locations and work limits), a knowledge snippet id or an article digest.
func NamesProfileSource(evidence string) bool { return evidenceSource.MatchString(evidence) }

// sourceIsProfile matches an item's source field that is nothing but a profile
// source name ("CV", "narrative", "knowledge:story-3"). A model often cites the
// profile there instead of in the evidence; a posting quote is never so short.
var sourceIsProfile = lazyregexp.New(`(?i)^\s*(cv|r[e\x{00e9}]sum[e\x{00e9}]|narrative|(article\s+)?digest|knowledge[:\s#\[]*[\w-]+\]?)\s*$`)

// cited reports whether an item names the profile source its hit rests on, in
// its evidence or as its whole source field.
func cited(it Item) bool {
	return NamesProfileSource(it.Evidence) || sourceIsProfile.MatchString(it.Source)
}

// gateTriggers hold, per class, the wording the requirement's own label or
// quoted source must carry for the gate to be honored. The label is the
// requirement; the evidence describes the profile.
var gateTriggers = map[string]*lazyregexp.Regexp{
	GateWorkAuth:  lazyregexp.New(`(?i)\b(citizen\w*|permanent residen\w*|green card|work[- ]authori[sz]\w*|authori[sz]ed to work|authority to work|right to work|legally (authori[sz]ed|permitted)|export[- ]control\w*|visa|sponsorship)\b`),
	GateEducation: lazyregexp.New(`(?i)\b(bachelor\w*|master\w*|ph\.?d\.?|doctorate|doctoral|degree|mba|diploma)\b`),
	GateLicense:   lazyregexp.New(`(?i)\b(licen[sc]e[ds]?|licensure|licensed|CPA|bar admission|series (7|63|65|66))\b`),
	GateProgram:   lazyregexp.New(`(?i)\b(eligib\w*|veteran\w*)\b`),
	GateLanguage:  lazyregexp.New(`(?i)\b(fluen\w*|fluent|native[- ]speaker|native proficiency|bilingual)\b`),
	GateOnsite:    lazyregexp.New(`(?i)\b(on-?site|relocat\w*|residen\w*|located in|must (live|reside)|in[- ]office)\b`),
}

// workAuthAcronyms are matched case-sensitively: lowercased they collide with
// "opt-in" and with "itar" inside "military".
var workAuthAcronyms = lazyregexp.New(`\b(OPT|CPT|ITAR|EAD)\b`)

func gateTriggered(class string, it Item, c CheckContext) bool {
	text := it.Label + " " + it.Source
	switch class {
	case GateWorkAuth:
		return c.Stance == gates.StanceRestricted || c.Stance == gates.StanceRefuses || gateTriggers[class].MatchString(text) || workAuthAcronyms.MatchString(text)
	case GateClearance:
		return discovery.ClearanceRequired(text)
	case GateExperience:
		years, ok := gates.YearsMentioned(text)
		return ok && c.ProfileYears != nil && float64(years) > *c.ProfileYears
	}
	re, ok := gateTriggers[class]
	return ok && re.MatchString(text)
}

// Check applies the two deterministic checks to the model's items: a hit
// above 0 needs evidence naming a profile source, and a missed must-have's
// gate class is honored only when the requirement itself triggers it. A
// work_authorization must-have is decided from the profile's sponsorship need
// instead (c.Need): met when no sponsorship is needed, half a hit and never a
// gate or a miss when the answer is missing.
func Check(out DeepOutput, c CheckContext) Checked {
	var ck Checked
	clean := func(items []Item, must bool) []Item {
		res := make([]Item, len(items))
		for i, it := range items {
			if must && it.GateClass == GateWorkAuth && c.Need != NeedNeeded && c.Need != "" {
				// The profile's sponsorship answer decides this requirement, not
				// the model's citation of it: a user who needs no sponsorship
				// meets it, and an unanswered profile gates nothing and counts
				// as an unknown, not a miss.
				it.Hit = 1
				if c.Need == NeedUnanswered {
					it.Hit = 0.5
				}
				res[i] = it
				continue
			}
			if it.Hit > 0 && !cited(it) {
				it.Hit = 0
				ck.Unsupported = append(ck.Unsupported, it.ID)
			}
			if must && it.Hit == 0 {
				ck.MustMisses++
				if it.GateClass != "" && it.GateClass != GateNone {
					if gateTriggered(it.GateClass, it, c) {
						ck.HonoredGate = true
						ck.HonoredIDs = append(ck.HonoredIDs, it.ID)
					} else {
						ck.Demotions = append(ck.Demotions, Cap{CapGateDemoted, 5.0,
							fmt.Sprintf("%s: class %s has no trigger in the requirement", labelOf(it), it.GateClass)})
					}
				}
			}
			res[i] = it
		}
		return res
	}
	ck.Must = clean(out.Must, true)
	ck.Nice = clean(out.Nice, false)
	ck.Hidden = clean(out.Hidden, false)
	ck.Align = clean(AlignmentDimensions(out.Alignment), false)
	ck.RedFlags = out.RedFlags
	return ck
}

// AlignmentIDs are the four profile-alignment dimensions, in report order.
var AlignmentIDs = []string{"seniority", "domain", "location", "role"}

// AlignmentDimensions keeps the first item of each alignment dimension, in
// the fixed order, so the alignment score always averages the same four
// questions however the reply orders or repeats them.
func AlignmentDimensions(items []Item) []Item {
	var out []Item
	for _, id := range AlignmentIDs {
		for _, it := range items {
			if it.ID == id {
				out = append(out, it)
				break
			}
		}
	}
	return out
}

func labelOf(it Item) string {
	if it.Label != "" {
		return strings.TrimSpace(it.Label)
	}
	return it.ID
}

// Levels returns the hit levels of items.
func Levels(items []Item) []float64 {
	out := make([]float64, len(items))
	for i, it := range items {
		out[i] = it.Hit
	}
	return out
}

// FlagLevels returns the levels of red flags.
func FlagLevels(flags []Flag) []float64 {
	out := make([]float64, len(flags))
	for i, f := range flags {
		out[i] = f.Level
	}
	return out
}
