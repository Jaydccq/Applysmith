package match

import (
	"strconv"

	"applysmith/internal/lazyregexp"
)

var (
	// rateAbilityAskRE is a self-assessment framed as a rating (Hopper: "Rate
	// your ability to deliver repeatable reporting and analytics:").
	rateAbilityAskRE = lazyregexp.New(`(?i)^\W*(?:please\s+)?rate\s+your\s+(?:abilit(?:y|ies)|proficiency|skills?|level|capabilit(?:y|ies))\b`)
	// firstPersonClaimRE is an option that states what the applicant can do or
	// has done.
	firstPersonClaimRE = lazyregexp.New(`(?i)^\W*i\s+(?:can|have|am|do)\b|^\W*i['\x{2019}](?:ve|m)\b`)
)

// leastClaimedRating answers a rating of the applicant's own ability over
// first-person statements, each claiming more than the one before, with the
// owner's default for an experience the record does not settle: the least
// claimed (leastClaimed), always for review.
func leastClaimedRating(s *Step) (Draft, bool) {
	real := realOptions(s.Q.Options)
	if len(real) < 3 || !rateAbilityAskRE.MatchString(s.Q.Text) {
		return Draft{}, false
	}
	for _, o := range real {
		if !firstPersonClaimRE.MatchString(o.Label) {
			return Draft{}, false
		}
	}
	least, ok := leastClaimed(real)
	if !ok {
		return Draft{}, false
	}
	s.reviewOnly = true
	return chosenOption(s, least, "self-rating over first-person statements that no record settles: the owner's default, the least claimed, for review"), true
}

// leastClaimed is the least claimed of statements a form lists in order: the
// first, or the last when the statements that name a rung (capabilityRungOf)
// run from the strongest down. Rungs that run both ways give no order.
func leastClaimed(real []Option) (Option, bool) {
	up, down, seen := false, false, false
	var prev capabilityRung
	for _, o := range real {
		r, ok := capabilityRungOf(o.Label)
		if !ok {
			continue
		}
		if seen {
			up, down = up || r > prev, down || r < prev
		}
		prev, seen = r, true
	}
	switch {
	case up && down:
		return Option{}, false
	case down:
		return real[len(real)-1], true
	}
	return real[0], true
}

// A self-assessment scale ("Which best describes your experience with X?") is
// the user's own claim, so it is answered only from the level the user recorded
// for that skill in the profile; this file maps that claim onto whatever words
// the form uses. It never infers a rung.

// capabilityRung orders the rungs every such scale uses, weakest first.
type capabilityRung int

const (
	rungNone       capabilityRung = iota // no exposure
	rungExplored                         // has tried it
	rungProjects                         // personal, academic or prototype work
	rungProduction                       // professional work shipped to users
	rungLed                              // led or owned several such systems
)

// capabilityRules are tried in order and a label takes the first it matches:
// the bottom rung leads so "No AI experience" is not read by the word
// "experience", the leadership rung precedes production because it nearly
// always says production too, and "exploring" precedes "projects" because a
// label can say "experimented ... but not built". "Occasionally" describes
// frequency, so it belongs to projects, not to exploring.
var capabilityRules = []struct {
	rung capabilityRung
	re   *lazyregexp.Regexp
}{
	{rungNone, lazyregexp.New(`^\s*(?:no\b|none\b|never\b)|\bno\s+\S*\s*experience\b|\bnot\s+used\b|\bnot\s+familiar\b`)},
	{rungLed, lazyregexp.New(`\b(?:led|lead|leading|owned|architected|mentor(?:ed)?|drove)\b|\bmultiple\s+production\b`)},
	{rungProduction, lazyregexp.New(`\b(?:shipped|production|professional(?:ly)?|at\s+work|in\s+my\s+(?:current\s+)?(?:job|role)|customers?|deployed|daily)\b`)},
	{rungExplored, lazyregexp.New(`\b(?:experiment(?:ed|ing)?|explor(?:ed|ing)|tried|dabbl|played\s+with|aware|read\s+about|curious|a\s+few\s+times)\b`)},
	{rungProjects, lazyregexp.New(`\b(?:prototype|side\s+project|personal|academic|hobby|coursework|school|built|created|side|occasional(?:ly)?|sometimes)\b`)},
}

// capabilityRungOf is the rung a label names.
func capabilityRungOf(label string) (capabilityRung, bool) {
	text := normalizeLabel(label)
	if text == "" {
		return 0, false
	}
	for _, r := range capabilityRules {
		if r.re.MatchString(text) {
			return r.rung, true
		}
	}
	return 0, false
}

// capabilityFramingRE is the framing of a scale question; an ordinary question
// about a skill never reads as one.
var capabilityFramingRE = lazyregexp.New(`\b(?:which|what)\b[^?]*\b(?:best\s+describes?|would\s+you\s+say|closest)\b|\bselect\s+one\s+of\s+the\s+below\b|\bextent\s+of\s+your\b|\bhow\s+would\s+you\s+describe\b|\bwhich\s+of\s+the\s+following\s+best\b`)

// capabilityRungChoice is one rendered option and the rung it names.
type capabilityRungChoice struct {
	Option Option
	Rung   capabilityRung
}

// readCapabilityLadder reads the options as a ladder: at least three, every one
// naming a rung, strictly ascending in page order. A set whose rungs jump around
// is one this reader has misread, and it is left to other lanes.
func readCapabilityLadder(opts []Option) ([]capabilityRungChoice, bool) {
	var out []capabilityRungChoice
	for _, o := range opts {
		r, ok := capabilityRungOf(o.Label)
		if !ok {
			return nil, false
		}
		out = append(out, capabilityRungChoice{o, r})
	}
	if len(out) < 3 {
		return nil, false
	}
	for i := 1; i < len(out); i++ {
		if out[i].Rung <= out[i-1].Rung {
			return nil, false
		}
	}
	return out, true
}

// selectCapabilityRung is the rung equal to the user's, else the highest below
// it, never above: the recorded rung is a ceiling on what is claimed.
func selectCapabilityRung(rungs []capabilityRungChoice, user capabilityRung) (capabilityRungChoice, bool) {
	var below capabilityRungChoice
	found := false
	for _, r := range rungs {
		if r.Rung == user {
			return r, true
		}
		if r.Rung < user && (!found || r.Rung > below.Rung) {
			below, found = r, true
		}
	}
	return below, found
}

// levelRung maps a recorded skill level onto the scale.
func levelRung(level string) (capabilityRung, bool) {
	switch level {
	case "beginner":
		return rungExplored, true
	case "intermediate":
		return rungProjects, true
	case "advanced":
		return rungProduction, true
	case "expert":
		return rungLed, true
	}
	return 0, false
}

// profileSkillLevel finds the profile skill the question names, the one whose
// name has the most tokens all present in the question, a short name only
// where the question writes it as one (shortSkillWritten). Two such skills
// with different recorded levels make the topic ambiguous.
func profileSkillLevel(text string, skills []SkillFact) (name, level string, state string) {
	asked := tokenSet(skillTokens(text))
	best := 0
	for _, sk := range skills {
		toks := skillTokens(sk.Name)
		if len(toks) == 0 || !shortSkillWritten(text, sk.Name) {
			continue
		}
		all := true
		for _, t := range toks {
			if !asked[t] {
				all = false
				break
			}
		}
		switch {
		case !all:
		case len(toks) > best:
			name, level, state, best = sk.Name, sk.Level, "ok", len(toks)
		case len(toks) == best && sk.Level != level:
			state = "ambiguous"
		}
	}
	return name, level, state
}

// capabilityScale answers an ascending self-assessment scale from the level the
// user recorded for the skill the question names.
func capabilityScale(s *Step) (Draft, bool) {
	real := realOptions(s.Q.Options)
	if len(real) < 3 || !capabilityFramingRE.MatchString(normalizeLabel(s.Q.Text)) {
		return Draft{}, false
	}
	ladder, ok := readCapabilityLadder(real)
	if !ok {
		return Draft{}, false
	}
	name, level, state := profileSkillLevel(s.Q.Text, s.Req.Facts.Skills)
	switch {
	case state == "ambiguous":
		return abstainDraft(ReasonAmbiguous, "", "self-assessment: the question names several profile skills with different levels -> abstain"), true
	case state != "ok" || level == "":
		return abstainDraft(ReasonPlaceholderEmpty, "", "self-assessment: no level is recorded in the profile for the skill the question names -> abstain"), true
	}
	user, ok := levelRung(level)
	if !ok {
		return abstainDraft(ReasonPlaceholderEmpty, "", "self-assessment: level %q is not a known level -> abstain", level), true
	}
	pick, ok := selectCapabilityRung(ladder, user)
	if !ok {
		return abstainDraft(ReasonInputMismatch, "", "self-assessment: every rung is above the recorded level %q -> abstain", level), true
	}
	d := derivedDraft(pick.Option.Label, "self_assessment", startProfileDerive,
		"self-assessment: skill "+strconv.Quote(name)+" recorded "+level+" -> rung "+strconv.Quote(pick.Option.Label))
	d.ChosenLabel, d.HasOption, d.OptionScore = pick.Option.Label, true, 1
	return d, true
}
