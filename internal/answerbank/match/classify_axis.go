package match

import "strings"

// selfIDAxis routes a question by its options when its text names no axis: the
// options must be a self-identification set naming exactly ONE axis, and only
// the axes the profile records (disability, veteran) are routed. A set naming an
// axis nobody records must reach the decline path, not a bank entry that would
// score protected-characteristic options by word overlap.
func selfIDAxis(opts []Option, sc *optionScorer) string {
	set := readSelfIDSetUsing(opts, sc)
	if set == nil || len(set.axes) != 1 {
		return ""
	}
	switch set.axes[0] {
	case axisDisability, axisVeteran:
		return set.axes[0]
	}
	return ""
}

// axisEntryID maps a self-ID axis to the entry a rescue routes to.
func axisEntryID(axis string) string {
	switch axis {
	case "disability":
		return "eeo-disability"
	case "veteran":
		return "eeo-veteran"
	}
	return ""
}

// ackByOption reports a required question with an option that carries an
// unnegated acknowledgement phrase.
func ackByOption(q Question) bool {
	if !q.IsRequired() || len(q.Options) == 0 {
		return false
	}
	for _, o := range q.Options {
		if hasUnnegatedAcknowledgement(o.Label) {
			return true
		}
	}
	return false
}

func lowerTrim(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
