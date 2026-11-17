package match

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// A self-identification option set is recognized from the OPTION LABELS alone,
// whichever entry the classifier picked, because any misroute onto any entry
// that guesses reopens the same hole.

// Axes of the closed, ordered cue table.
const (
	axisDisability      = "disability"
	axisNeurodivergence = "neurodivergence"
	axisVeteran         = "veteran"
	axisRefugee         = "refugee-or-immigrant"
	axisCaregiver       = "caregiver"
	axisRaceEthnicity   = "race-or-ethnicity"
	axisGender          = "gender"
	axisGenderIdentity  = "gender-identity"
	axisOrientation     = "sexual-orientation"
	axisFamily          = "pregnancy-or-family"
)

type axisCue struct {
	axis string
	res  []*lazyregexp.Regexp
	// manCue marks the gender axis, whose "man" cue must not fire inside the
	// country name "Isle of Man".
	manCue bool
}

// axisCues are matched against the normalized label as whole words. Each names
// a characteristic a person IS or HAS; words that only sit near diversity
// topics ("inclusion", "diversity", "community") are not cues. A label maps to
// the first axis with a hit.
var axisCues = []axisCue{
	{axis: axisDisability, res: mustCues(`\bdisabilit(?:y|ies)\b`, `\bdisabled\b`, `\bhandicap(?:ped)?\b`)},
	{axis: axisNeurodivergence, res: mustCues(`\bneuro\s*(?:diverse|divergent|diversity|divergence)\b`, `\bautis(?:m|tic)\b`, `\badhd\b`, `\bdyslexi(?:a|c)\b`)},
	{axis: axisVeteran, res: mustCues(`\bveterans?\b`, `\bmilitary\s+service\b`, `\barmed\s+forces\b`)},
	{axis: axisRefugee, res: mustCues(`\brefugees?\b`, `\bimmigrants?\b`, `\basylum\b`, `\bfirst\s+generation\s+immigrant\b`)},
	{axis: axisCaregiver, res: mustCues(`\bparent\b`, `\bcaregivers?\b`, `\bcaring\s+responsibilit`)},
	{axis: axisRaceEthnicity, res: mustCues(`\bhispanic\b`, `\blatino\b`, `\blatina\b`, `\blatinx\b`, `\bblack\b`, `\bafrican\s+american\b`,
		`\bwhite\b`, `\basian\b`, `\bnative\s+(?:american|hawaiian)\b`, `\balaska\s+native\b`, `\bpacific\s+islander\b`,
		`\btwo\s+or\s+more\s+races\b`, `\bethnicit(?:y|ies)\b`, `\brace\b`)},
	{axis: axisGender, res: mustCues(`\bfemale\b`, `\bwoman\b`, `\bwomen\b`, `\bmale\b`, `\bmen\b`), manCue: true},
	{axis: axisGenderIdentity, res: mustCues(`\btransgender\b`, `\btrans\b`, `\bnon\s*binary\b`, `\bgenderqueer\b`, `\bgender\s+identity\b`)},
	{axis: axisOrientation, res: mustCues(`\blgbtq?\b`, `\blgbtqia?\b`, `\bgay\b`, `\blesbian\b`, `\bbisexual\b`, `\bqueer\b`, `\bsexual\s+orientation\b`)},
	{axis: axisFamily, res: mustCues(`\bpregnan(?:t|cy)\b`, `\bmaternity\b`, `\bfamily\s+status\b`, `\bmarital\s+status\b`)},
}

var (
	manWordRE      = lazyregexp.New(`\bman\b`)
	noneAboveREs   = mustCues(`^none\s+of\s+the\s+above$`, `^none\s+of\s+these$`, `^none\b`, `^not\s+applicable$`, `^n\s*/?\s*a$`)
	isleOfSuffix   = "isle of "
	bareDeclineSet = setOf("decline", "i decline", "declined")
)

// hasManCue reports "man" as a whole word that is not the tail of "isle of ".
func hasManCue(norm string) bool {
	for _, loc := range manWordRE.FindAllStringIndex(norm, -1) {
		prefix := norm[:loc[0]]
		if strings.HasSuffix(prefix, isleOfSuffix) {
			before := len(prefix) - len(isleOfSuffix)
			if before == 0 || !isWordByte(prefix[before-1]) {
				continue
			}
		}
		return true
	}
	return false
}

// selfIDAxisOfLabel is the sensitive axis an option label names, or "". One
// shared cue table: a stricter caller (the completion ladder excludes a control
// as soon as ANY option names an axis) reads the same table.
func selfIDAxisOfLabel(label string) string { return selfIDAxisOfNorm(normalizeLabel(label)) }

// selfIDAxisOfNorm is selfIDAxisOfLabel on a label already normalized.
func selfIDAxisOfNorm(norm string) string {
	if !mayHoldAxisCue(norm) {
		return ""
	}
	for _, c := range axisCues {
		for _, re := range c.res {
			if re.MatchString(norm) {
				return c.axis
			}
		}
		if c.manCue && hasManCue(norm) {
			return c.axis
		}
	}
	return ""
}

// cueStems are fragments every cue contains; a label with none of them holds no
// cue, which spares the regular expressions on the long proper-noun lists.
var cueStems = []string{"disab", "handicap", "neuro", "autis", "adhd", "dyslex", "veteran", "military", "armed", "refugee",
	"immigrant", "asylum", "parent", "caregiver", "caring", "hispanic", "latin", "black", "african", "white", "asian", "native",
	"alaska", "pacific", "races", "ethnic", "race", "male", "woman", "women", "man", "men", "trans", "binary", "genderqueer",
	"gender", "lgbt", "gay", "lesbian", "bisexual", "queer", "sexual", "pregnan", "maternity", "family", "marital"}

func mayHoldAxisCue(norm string) bool {
	for _, st := range cueStems {
		if strings.Contains(norm, st) {
			return true
		}
	}
	return false
}

// Option roles in a self-identification set.
const (
	roleOther   = "other"
	roleAxis    = "axis"
	roleDecline = "decline"
	roleNone    = "none-of-the-above"
)

type selfIDRole struct {
	kind string
	axis string
}

// selfIDRoleOf reads one label. A decline is checked BEFORE the axis cues, so
// "I prefer not to say whether I am a veteran" is never a veteran stance.
func selfIDRoleOf(label string) selfIDRole {
	if isDeclineText(label) || bareDeclineSet[normalizeLabel(label)] {
		return selfIDRole{kind: roleDecline}
	}
	norm := normalizeLabel(label)
	for _, re := range noneAboveREs {
		if re.MatchString(norm) {
			return selfIDRole{kind: roleNone}
		}
	}
	if axis := selfIDAxisOfNorm(norm); axis != "" {
		return selfIDRole{kind: roleAxis, axis: axis}
	}
	return selfIDRole{kind: roleOther}
}

// selfIDSet is an option list recognized as a self-identification set.
type selfIDSet struct {
	// roles is index-aligned with the question's options.
	roles []selfIDRole
	// axes are the distinct axes the set names, in first-seen order.
	axes []string
}

func (s *selfIDSet) names(axis string) bool {
	for _, a := range s.axes {
		if a == axis {
			return true
		}
	}
	return false
}

// declineIndex is the index of the first decline option, or -1.
func (s *selfIDSet) declineIndex() int {
	for i, r := range s.roles {
		if r.kind == roleDecline {
			return i
		}
	}
	return -1
}

// categoryAxes are the axes whose options name a category rather than a
// yes/no stance.
var categoryAxes = map[string]bool{axisRaceEthnicity: true, axisGender: true, axisGenderIdentity: true, axisOrientation: true}

// readSelfIDSet reads an option list as a self-identification set, or nil. A set
// names at least two distinct axes, one axis together with a decline or a
// none-of-the-above option, or one category axis (race, gender, gender
// identity, orientation) in two or more options, such as a race list with no
// decline. A dedicated single-axis Yes/No form is not a set: its options state
// a stance rather than a category, and its entry answers it from the record.
func readSelfIDSet(opts []Option) *selfIDSet { return readSelfIDSetUsing(opts, nil) }

// readSelfIDSetUsing is readSelfIDSet reading the roles through a control's
// scorer, so a control read several times is classified once.
func readSelfIDSetUsing(opts []Option, sc *optionScorer) *selfIDSet {
	if len(opts) == 0 {
		return nil
	}
	set := &selfIDSet{roles: make([]selfIDRole, len(opts))}
	hatch := false
	categoryOptions := 0
	for i, o := range opts {
		r := sc.role(o.Label)
		set.roles[i] = r
		switch r.kind {
		case roleAxis:
			if categoryAxes[r.axis] {
				categoryOptions++
			}
			if !set.names(r.axis) {
				set.axes = append(set.axes, r.axis)
			}
		case roleDecline, roleNone:
			hatch = true
		}
	}
	if len(set.axes) == 0 || len(set.axes) < 2 && !hatch && categoryOptions < 2 {
		return nil
	}
	return set
}
