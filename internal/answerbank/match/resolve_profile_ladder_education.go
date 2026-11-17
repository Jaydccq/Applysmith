package match

import (
	"fmt"
	"math"
	"strings"

	"applysmith/internal/lazyregexp"
)

// An education-level picker ("highest level of education") names the same
// levels in different words than the user's degree. These helpers place a
// degree on that ordinal scale so the option pickers can choose the user's
// level, or the highest rung below it, and never a rung above.

// educationLevel orders the rungs; the gaps leave room for new rungs and only
// comparison is meant.
type educationLevel int

const (
	levelNone         educationLevel = 0
	levelHighSchool   educationLevel = 10
	levelSomeCollege  educationLevel = 20
	levelTwoYear      educationLevel = 30
	levelThreeYear    educationLevel = 35
	levelFourYear     educationLevel = 40
	levelPostGraduate educationLevel = 50
	levelDoctorate    educationLevel = 60
)

// degreeSpecialisationPenalty is how far an option that adds an unclaimed field
// of study is demoted: enough to drop a 0.89 containment hit under the 0.70
// gate while leaving an un-specialised 0.82 hit standing.
const degreeSpecialisationPenalty = 0.2

// educationRules are tried in order and a label takes the first it matches, so
// "Some College-No Degree" is read before the bare "college" of a four-year
// rung, "Post Graduate Degree" before "graduate" reads as completion, and
// "Less than high school" before the high school it names.
var educationRules = []struct {
	level educationLevel
	re    *lazyregexp.Regexp
}{
	{levelDoctorate, lazyregexp.New(`\b(?:doctoral|doctorate|doctor\s+of|juris\s+doctor|ph\.?\s?d|d\.?phil|m\.?d\b|j\.?d\b|ed\.?d)\b`)},
	{levelPostGraduate, lazyregexp.New(`\b(?:post[\s-]?grad(?:uate)?|graduate\s+degree|master(?:'?s)?|m\.?\s?s\.?c?\b|m\.?b\.?a\b|m\.?eng\b|m\.?a\b|advanced\s+degree)\b`)},
	{levelSomeCollege, lazyregexp.New(`\bsome\s+college\b|\bno\s+degree\b|\bsome\s+university\b`)},
	{levelFourYear, lazyregexp.New(`\b(?:4\s*[-\s]?year|four[\s-]?year|bachelor(?:'?s)?|b\.?\s?s\.?c?\b|b\.?a\b|b\.?eng\b|undergraduate\s+degree)\b`)},
	{levelThreeYear, lazyregexp.New(`\b(?:3\s*[-\s]?year|three[\s-]?year)\b`)},
	{levelTwoYear, lazyregexp.New(`\b(?:2\s*[-\s]?year|two[\s-]?year|associate(?:'?s)?|a\.a\b|a\.s\b)\b`)},
	{levelNone, lazyregexp.New(`\b(?:no\s+formal\s+education|less\s+than\s+high\s+school|none)\b`)},
	{levelHighSchool, lazyregexp.New(`\b(?:high\s*school|secondary\s+school|ged\b|equivalent)\b`)},
}

// associateInitialsRE is the two-year degree's initials as a label writes them,
// in capitals; lowercased they are the words "as" and "aa".
var associateInitialsRE = lazyregexp.New(`\bA\.?[AS]\b`)

var noLevelLabelRE = lazyregexp.New(`^(?:unknown|not\s+specified|prefer\s+not|n/?a)\b`)

// mastersNamedRE is a label that names a master's degree.
var mastersNamedRE = lazyregexp.New(`\bmaster(?:'?s)?\b|\bm\.?b\.?a\b`)

// educationLevelOf is the rung a label names; a placeholder, an "Unknown" row
// and an unrelated string name none. A rung that names a master's beside a
// doctorate ("Master's, JD, and/or PhD": Justworks) is the bucket a master's
// opens, so its level is the master's.
func educationLevelOf(label string) (educationLevel, bool) {
	text := normalizeLabel(apostrophes.Replace(associateInitialsRE.ReplaceAllString(label, "associate")))
	if text == "" || noLevelLabelRE.MatchString(text) {
		return 0, false
	}
	for _, r := range educationRules {
		if r.re.MatchString(text) {
			if r.level == levelDoctorate && mastersNamedRE.MatchString(text) {
				return levelPostGraduate, true
			}
			return r.level, true
		}
	}
	return 0, false
}

// highestEducationLevelIn is the highest rung named anywhere in the labels.
func highestEducationLevelIn(labels []string) (educationLevel, bool) {
	var best educationLevel
	found := false
	for _, l := range labels {
		if lv, ok := educationLevelOf(l); ok && (!found || lv > best) {
			best, found = lv, true
		}
	}
	return best, found
}

// abbrevRungs place the levels degreeAbbrevLevel reads on the ladder. A
// professional doctorate written as its initials (MD, JD, PharmD, DVM) is not
// placed: a ladder's doctoral rung is often "Ph.D.", and "Do you hold a PhD?"
// asks it, and it is no Ph.D. The bank's own entry, which setup states as
// Doctorate, answers for it.
var abbrevRungs = map[string]educationLevel{"Master": levelPostGraduate, "Bachelor": levelFourYear}

// degreeRung is the rung of the profile's degree: the label rules, else the
// abbreviation the degree opens with ("B.Tech").
func degreeRung(degree string) (educationLevel, bool) {
	if degreeLevel(degree) == "" && degreeAbbrevLevel(degree) == "Doctorate" {
		return 0, false
	}
	if lv, ok := educationLevelOf(degree); ok {
		return lv, true
	}
	lv, ok := abbrevRungs[degreeAbbrevLevel(degree)]
	return lv, ok
}

// userEducationLevel is the user's highest completed degree in the profile. A
// degree counts once its end month is the current month or earlier; one still in
// progress, or with no end, does not.
func userEducationLevel(f Facts, today Date) (educationLevel, bool) {
	var best educationLevel
	found := false
	for _, e := range f.Education {
		end, ok := parseYearMonth(e.End)
		if !ok || end.key() > monthOf(today).key() {
			continue
		}
		if lv, ok := degreeRung(e.Degree); ok && (!found || lv > best) {
			best, found = lv, true
		}
	}
	return best, found
}

// educationUnread reports a recorded degree that may be completed and that
// userEducationLevel could not place: its end is not a month, or it has ended
// and its name is no level the rules know ("MBBS"). A blank end is a degree
// in progress.
func educationUnread(f Facts, today Date) bool {
	for _, e := range f.Education {
		if strings.TrimSpace(e.End) == "" {
			continue
		}
		end, ok := parseYearMonth(e.End)
		if !ok {
			return true
		}
		if _, known := degreeRung(e.Degree); !known && end.key() <= monthOf(today).key() {
			return true
		}
	}
	return false
}

// educationAskRE is the wording of a question about the user's education level;
// othersEducationRE is one about another person's education, or about what the
// role requires, which the applicant's degree does not answer.
var (
	educationAskRE    = lazyregexp.New(`(?i)\b(?:highest|education|educational|degree|qualification)\b`)
	othersEducationRE = lazyregexp.New(`(?i)\b(?:parents?|guardians?|mother|father|spouse|partner|famil(?:y|ies)|siblings?)\b|\brequired\s+(?:for|by|of)\s+(?:this|the)\s+(?:role|position|job)\b|\b(?:role|position|job)\s+requires?\b`)
)

// othersEducationGuard keeps the user's highest-level-education entry off a
// question about another person's education or what the role requires: the
// applicant's degree does not answer it, so it is left to the user.
func othersEducationGuard(s *Step, e *Entry) (Abstain, string, bool) {
	if e.ID != "highest-level-education" || !othersEducationRE.MatchString(s.Q.Text) {
		return Abstain{}, "", false
	}
	return Abstain{Reason: ReasonContextPolicy}, "the question asks another person's education or the role's requirement, not the applicant's", true
}

// highestEducationLevel answers a highest-level-of-education ladder from the
// profile's highest completed degree: the rung of that level, else the highest
// rung below it, never one above. With no completed degree, or no rung at or
// below it, it stands aside so the user's own entry decides; a curated example
// that names the question outranks it like the other derived lanes.
func highestEducationLevel(s *Step) (Draft, bool) {
	q := s.Q
	if len(q.Options) == 0 || isEducationRowControl(q) || moreSpecificEduRE.MatchString(q.Text) || !educationAskRE.MatchString(q.Text) || othersEducationRE.MatchString(q.Text) {
		return Draft{}, false
	}
	// A completed degree the rules cannot place (a PharmD) may be the highest.
	level, ok := userEducationLevel(s.Req.Facts, s.Req.Today)
	if !ok || educationUnread(s.Req.Facts, s.Req.Today) {
		return Draft{}, false
	}
	rung, trace, ok := pickEducationRung(q.Options, level, nil)
	if !ok {
		return Draft{}, false
	}
	d := derivedDraft(rung.Value, "", startEducation, "highest completed degree in history.education; "+trace)
	d.ChosenLabel = rung.Label
	d.Deterministic = true
	return d, true
}

// educationRung is one rendered option and the level it names.
type educationRung struct {
	Option Option
	Level  educationLevel
}

// readEducationLadder reads the options as a ladder: at least three that name a
// level, all of distinct levels. A repeated level means a degree-name catalog
// (the word "bachelor" dozens of times), where level reasoning must not run.
func readEducationLadder(opts []Option) ([]educationRung, bool) {
	var rungs []educationRung
	seen := map[educationLevel]bool{}
	for _, o := range opts {
		lv, ok := educationLevelOf(o.Label)
		if !ok {
			continue
		}
		if seen[lv] {
			return nil, false
		}
		seen[lv] = true
		rungs = append(rungs, educationRung{o, lv})
	}
	return rungs, len(rungs) >= 3
}

// selectEducationRung is the rung equal to the user's level, else the highest
// rung below it. It never returns a rung above: claiming a degree the user lacks
// is a false statement, while understating by a rung is merely imprecise.
func selectEducationRung(rungs []educationRung, user educationLevel) (educationRung, bool) {
	var below educationRung
	found := false
	for _, r := range rungs {
		if r.Level == user {
			return r, true
		}
		if r.Level < user && (!found || r.Level > below.Level) {
			below, found = r, true
		}
	}
	return below, found
}

// contradictsEducationLevel reports an option that names a different level from
// the claimed one. The guard is symmetric: understating a degree is as false as
// overstating it, so such an option scores zero rather than being demoted. An
// option naming no level is untouched.
func contradictsEducationLevel(claimed educationLevel, optionLabel string) bool {
	lv, ok := educationLevelOf(optionLabel)
	return ok && lv != claimed
}

// degreeGenericTokens do not make one degree name a different degree: the level
// vocabulary, the connectives, and "higher" or "above" of an "or Higher" rung.
var degreeGenericTokens = setOf(
	"degree", "of", "in", "the", "and", "a", "an", "or", "s",
	"bachelor", "bachelors", "bach", "master", "masters", "mast", "mag",
	"doctor", "doctoral", "doctorate", "associate", "associates", "assoc",
	"graduate", "post", "postgraduate", "undergraduate", "year", "years",
	"university", "college", "school", "diploma", "certificate", "equivalent",
	"science", "arts", "art", "studies", "other", "general",
	"higher", "above",
)

var (
	degreeInitials  = setOf("aa", "as", "ba", "bs", "bsc", "ma", "ms", "msc", "etc")
	parentheticalRE = lazyregexp.New(`\(([^)]*)(?:\)|$)`)
)

// withoutInitialsUmbrella removes a parenthetical that only lists two or more
// degree initials ("(MS/MA/etc)"): it is an umbrella spelling of a level and
// names no field. A lone "(MA)" names one degree and stays.
func withoutInitialsUmbrella(label string) string {
	return parentheticalRE.ReplaceAllStringFunc(label, func(whole string) string {
		toks := symbolBearingTokens(parentheticalRE.FindStringSubmatch(whole)[1])
		if len(toks) < 2 {
			return whole
		}
		for _, t := range toks {
			if !degreeInitials[t] {
				return whole
			}
		}
		return " "
	})
}

// namesUnclaimedDiscipline reports a degree option that adds a field of study
// the answer never named. "Master of Science" is contained in "Master of
// Science, Electrical", and a containment scorer rewards the shorter label, so
// without this the specialised row could win and state a discipline the user
// did not study. Initials are free ("- M S" abbreviates the answer), so is the
// generic degree vocabulary, and so is a negated qualifier: "(non-MBA)" excludes
// a field and names none, while "in Business Administration (MBA)" is demoted.
func namesUnclaimedDiscipline(answer, optionLabel string) bool {
	if _, ok := educationLevelOf(optionLabel); !ok {
		return false
	}
	claimed := tokenSet(symbolBearingTokens(normalizeLabel(answer)))
	toks := symbolBearingTokens(withoutInitialsUmbrella(normalizeLabel(optionLabel)))
	for i := 0; i < len(toks); i++ {
		if toks[i] == "non" {
			i++
			continue
		}
		if t := toks[i]; !claimed[t] && len(t) > 1 && !degreeGenericTokens[t] {
			return true
		}
	}
	return false
}

// pickEducationRung chooses the option for the user's level from the rendered
// ladder. answers are the degree words the user claims; a rung that names a field
// none of them claim is a different degree and leaves the question to the user.
// The string is the trace line.
func pickEducationRung(opts []Option, user educationLevel, answers []string) (Option, string, bool) {
	ladder, ok := readEducationLadder(realOptions(opts))
	if !ok {
		return Option{}, "education ladder: the options are not a ladder of distinct levels", false
	}
	rung, ok := selectEducationRung(ladder, user)
	if !ok {
		return Option{}, "education ladder: every rung is above the user's level", false
	}
	if len(answers) > 0 {
		claimed := false
		for _, a := range answers {
			if !namesUnclaimedDiscipline(a, rung.Option.Label) {
				claimed = true
				break
			}
		}
		if !claimed {
			return Option{}, "education ladder: rung " + rung.Option.Label + " names a field the user does not claim", false
		}
	}
	return rung.Option, "education ladder: user level -> " + rung.Option.Label, true
}

// degreeHeldRE is a yes/no question asking whether the applicant holds a degree
// of a level: group 1 the level wording, group 2 what follows it.
var degreeHeldRE = lazyregexp.New(`(?i)^\W*(?:(?:do|did)\s+you\s+(?:currently\s+)?(?:have|hold|possess|obtain|earn|complete)|have\s+you\s+(?:completed|earned|obtained|received|attained))\s+(?:a|an|your)\s+` +
	`((?:(?:bachelor|master|associate|doctoral)(?:'?s)?|doctorate|ph\.?\s?d\.?|(?:4|four|2|two)[\s-]year|college|university|undergraduate|graduate|post-?graduate)(?:\s+(?:level|or\s+(?:higher|above|greater)))?(?:\s+degree)?|degree)\b(.*)$`)

// degreeHeldDisjunctionRE opens an alternative to the degree ("and/or
// equivalent experience"), degreeHeldQualifierRE is the one qualifier read as
// part of holding a degree at all, and degreeHeldLevelRE is a further level of
// the degree asked ("Bachelor's or Master's degree"), never an alternative.
var (
	degreeHeldDisjunctionRE = lazyregexp.New(`(?i)^\s*,?\s*(?:and\s*/\s*or|or)\b`)
	degreeHeldLevelRE       = lazyregexp.New(`(?i)^\s*(?:,\s*(?:(?:and\s*/\s*)?or\s+)?|/\s*|(?:and\s*/\s*)?or\s+)(?:an?\s+)?((?:bachelor|master|associate|doctoral)(?:'?s)?|doctorate|ph\.?\s?d\b\.?)(?:\s+degrees?)?`)
	degreeHeldQualifierRE   = lazyregexp.New(`(?i)^\s*(?:or\s+(?:higher|above|greater)\s*)?(?:from\s+an?\s+accredited\s+(?:college|university|institution|school)(?:\s+of\s+higher\s+(?:learning|education))?)?`)
	// degreeFieldTailRE is a field of study after the degree; degreeFieldAltRE
	// a word of an alternative to the degree within it.
	degreeFieldTailRE = lazyregexp.New(`(?i)^in\s+[^?]+[?.!*\s]*$`)
	degreeFieldAltRE  = lazyregexp.New(`(?i)\b(?:experience|years?|work|industry|practical|certif\w*|bootcamp)\b`)
)

// degreeHeld answers "Do you have a bachelor's degree?" from the user's highest
// completed degree. The level asked is a minimum. A field of study, a school or
// any other condition after the degree leaves the question to the entries that
// read it; an alternative after it ("or equivalent experience") is met by the
// degree, so it is answered Yes, while a missing degree leaves the alternative
// to the user. With no education recorded, or a recorded degree it cannot
// place below the level asked (educationUnread), it stands aside: No is said
// only of a history read whole.
func degreeHeld(s *Step) (Draft, bool) {
	q := s.Q
	if len(s.Req.Facts.Education) == 0 || isEducationRowControl(q) {
		return Draft{}, false
	}
	m := degreeHeldRE.FindStringSubmatch(strings.TrimSpace(apostrophes.Replace(q.Text)))
	if m == nil {
		return Draft{}, false
	}
	yes, okY := ownYesNo(q.Options, true)
	no, okN := ownYesNo(q.Options, false)
	if !okY || !okN {
		return Draft{}, false
	}
	asked := levelTwoYear
	if lv, ok := educationLevelOf(m[1]); ok {
		asked = lv
	} else if l := strings.ToLower(m[1]); strings.Contains(l, "graduate") && !strings.Contains(l, "undergraduate") {
		asked = levelPostGraduate
	} else if strings.Contains(l, "college") || strings.Contains(l, "university") || strings.Contains(l, "undergraduate") {
		asked = levelTwoYear
		if strings.Contains(l, "undergraduate") {
			asked = levelFourYear
		}
	}
	tail := m[2]
	for lm := degreeHeldLevelRE.FindStringSubmatch(tail); lm != nil; lm = degreeHeldLevelRE.FindStringSubmatch(tail) {
		if lv, ok := educationLevelOf(lm[1]); ok {
			asked = min(asked, lv)
		}
		tail = tail[len(lm[0]):]
	}
	tail = strings.TrimSpace(tail)
	tail = strings.TrimSpace(tail[len(degreeHeldQualifierRE.FindString(tail)):])
	alternative := degreeHeldDisjunctionRE.MatchString(tail)
	level, have := userEducationLevel(s.Req.Facts, s.Req.Today)
	met := have && level >= asked
	if !met && educationUnread(s.Req.Facts, s.Req.Today) {
		return Draft{}, false
	}
	if !alternative && strings.Trim(tail, "?.!* ") != "" {
		// A field after a level the user has not reached is missed in every
		// field (Lendbuzz: "Do you have a PhD in Computer Science or
		// similar?"); any other condition, an alternative after the field, or
		// a level reached leaves it to the entries that read the field.
		if met || !degreeFieldTailRE.MatchString(tail) || degreeFieldAltRE.MatchString(tail) {
			return Draft{}, false
		}
	}
	if !met && alternative {
		return Draft{}, false
	}
	chosen, why := no, "no completed degree reaches the level asked"
	if met {
		chosen, why = yes, "a completed degree reaches the level asked"
	}
	d := derivedDraft(chosen.Label, "", startEducation, "degree held: "+why+" in history.education -> "+quoteTerm(chosen.Label))
	d.ChosenLabel = chosen.Label
	d.Deterministic = true
	return d, true
}

var (
	// degreeInFieldRE asks for a degree of the levels named in the fields
	// listed (group 1 the levels, group 2 the fields), with a graduation window
	// (groups 3 and 4) when it gives one: Gem's "Do you have a Bachelor's or
	// Master's degree in Computer Science, ..., or a related field, with a
	// graduation date between January 2026 and November 2026?".
	degreeInFieldRE = lazyregexp.New(`(?i)^\W*do\s+you\s+(?:currently\s+)?(?:have|hold|possess)\s+an?\s+` +
		`((?:bachelor|master|doctoral)(?:'?s)?(?:\s*(?:,|/|or|and\s*/\s*or)\s*(?:an?\s+)?(?:bachelor|master|doctoral)(?:'?s)?)*)\s+degrees?\s+in\s+(.+?)` +
		`(?:,?\s+with\s+(?:a|an|your)\s+(?:graduation|completion)\s+date\s+(?:between|from)\s+([a-z]+\.?\s+\d{4})\s+(?:and|to|through|-)\s+([a-z]+\.?\s+\d{4}))?\s*[?.!*]*$`)
	degreeLevelWordRE = lazyregexp.New(`(?i)(?:bachelor|master|doctoral)(?:'?s)?`)
	fieldSplitRE      = lazyregexp.New(`(?i)\s*,\s*(?:or\s+|and\s+)?|\s+(?:or|and)\s+`)
	relatedFieldRE    = lazyregexp.New(`(?i)^(?:an?\s+|any\s+|other\s+)?(?:closely\s+)?(?:related|equivalent|similar)\s+(?:fields?|disciplines?|areas?|majors?)(?:\s+of\s+study)?$`)
)

// degreeInField answers Yes for a degree in a listed field: an entry of a level
// the question names, whose field is one it lists and which ended in the
// question's graduation window, or, with no window, has ended. Anything else
// is left to the user, who judges what a "related field" covers.
func degreeInField(s *Step) (Draft, bool) {
	q := s.Q
	if len(s.Req.Facts.Education) == 0 || isEducationRowControl(q) {
		return Draft{}, false
	}
	m := degreeInFieldRE.FindStringSubmatch(strings.TrimSpace(apostrophes.Replace(q.Text)))
	if m == nil {
		return Draft{}, false
	}
	yes, ok := ownYesNo(q.Options, true)
	if _, okN := ownYesNo(q.Options, false); !ok || !okN {
		return Draft{}, false
	}
	levels := map[educationLevel]bool{}
	for _, w := range degreeLevelWordRE.FindAllString(m[1], -1) {
		if lv, ok := educationLevelOf(w); ok {
			levels[lv] = true
		}
	}
	fields := map[string]bool{}
	for _, f := range fieldSplitRE.Split(m[2], -1) {
		if f = strings.TrimSpace(f); f != "" && !relatedFieldRE.MatchString(f) {
			fields[normalizeLabel(f)] = true
		}
	}
	window := monthRange{from: math.MinInt, to: monthKey(s.Req.Today.Year, s.Req.Today.Month)}
	if m[3] != "" {
		from, okF := parseSeasonBracket(m[3])
		to, okT := parseSeasonBracket(m[4])
		if !okF || !okT {
			return Draft{}, false
		}
		window = monthRange{from: from.from, to: to.to}
	}
	for _, e := range s.Req.Facts.Education {
		lv, okL := degreeRung(e.Degree)
		end, okE := parseYearMonth(e.End)
		if okL && okE && levels[lv] && fields[normalizeLabel(e.Field)] && end.key() >= window.from && end.key() <= window.to {
			d := derivedDraft(yes.Label, "", startEducation, "degree in a listed field: "+e.Degree+" in "+e.Field+" ending "+e.End+" in history.education -> "+quoteTerm(yes.Label))
			d.ChosenLabel = yes.Label
			d.Deterministic = true
			return d, true
		}
	}
	return Draft{}, false
}

// stemFieldsAskRE is a question the has-stem-degree routes take by the fields
// it names ("a Bachelor's degree in Computer Science, Statistics or
// Mathematics?") rather than by STEM itself.
var (
	stemFieldsAskRE = lazyregexp.New(`(?i)\b` + pickDegree + `\b` + pickSentenceGap + `\bin\b` + pickSentenceGap + `\b` + pickStemField + `\b` +
		`|\bmajor(?:ing|ed)\s+in\b[^.?!]*\b(?:engineering|computer\s+science|data\s+science|statistics|mathematics|quantitative)\b` +
		`|\bdo\s+you\s+have\b` + pickSentenceGap + `\b(?:bachelor(?:'s)?|master(?:'s)?|ph\.?d)\b` + pickSentenceGap + `\b(?:computer\s+science|data\s+science|mathematics|statistics|quantitative)\b`)
	stemWordRE = lazyregexp.New(`(?i)\bstem\b`)
)

// stemFieldsNamed keeps a STEM degree that setup stated from answering a
// question that names fields: the statement covers every field on the STEM
// list, so a biology degree would answer "a degree in statistics?". Unless a
// recorded field is one the question names, it is left to the user; the
// user's own has-stem-degree answer is theirs to apply.
func stemFieldsNamed(s *Step) (Draft, bool) {
	e := entryNamed(s, "has-stem-degree")
	if e == nil || e.UserWritten() || ownText(e) == "" || stemWordRE.MatchString(s.Q.Text) || !stemFieldsAskRE.MatchString(s.Q.Text) {
		return Draft{}, false
	}
	q := normalizeLabel(s.Q.Text)
	for _, ed := range s.Req.Facts.Education {
		if f := normalizeLabel(ed.Field); f != "" && strings.Contains(q, f) {
			return Draft{}, false
		}
	}
	return abstainDraft(ReasonAmbiguous, "", "stem-fields-named: the question names fields no recorded field is; the STEM degree setup stated does not answer it -> abstain"), true
}

var (
	// postedFieldsRefRE points a degree question at the fields the posting
	// lists (Cobot: "Have you earned a Master's degree in one of the fields
	// mentioned above, as listed in the Minimum Requirements section?").
	postedFieldsRefRE = lazyregexp.New(`(?i)\b(?:(?:any|one)\s+of\s+)?the\s+(?:fields?|disciplines?|areas?|majors?)(?:\s+of\s+study)?\s+(?:mentioned|listed|named|specified|stated|outlined)\s+(?:above|in\s+the\s+(?:job\s+)?(?:description|posting|requirements?|qualifications?))` +
		`(?:\s*,?\s*(?:as\s+)?(?:listed|stated|described|specified|outlined|mentioned)\s+(?:in|under)\s+(?:the\s+)?[\w\s-]{0,40}?(?:section|requirements?|qualifications?))?`)
	// postedDegreeFieldsRE is a degree requirement in the posting's text:
	// group 1 its level, group 2 the fields it lists.
	postedDegreeFieldsRE = lazyregexp.New(`(?i)\b(bachelor|master|doctoral|ph\.?\s?d)\S*\s+(?:degree\s+)?in\s+([^.;:\n]{3,200}?)\s*(?:[.;:\n]|$)`)
)

// postedDegreeFieldsPass answers a degree question that points at the fields
// the posting lists: the reference is replaced with the fields of the
// posting's requirement at the level the question names, and the question is
// matched again, as a question that names them is.
func postedDegreeFieldsPass(req *Request, qs []Question, results []Result) {
	for i := range results {
		if i >= len(qs) || isFilled(results[i]) {
			continue
		}
		q := req.Questions[i]
		loc := postedFieldsRefRE.FindStringIndex(q.Text)
		if loc == nil {
			continue
		}
		fields, ok := postedDegreeFields(req.Posting.Description, q.Text[:loc[0]])
		if !ok {
			continue
		}
		q.Text = q.Text[:loc[0]] + fields + q.Text[loc[1]:]
		rr, err := req.Rematch(q)
		if err != nil || !isFilled(rr) {
			continue
		}
		rr.Selector = results[i].Selector
		rr.Trace = append([]string{fmt.Sprintf("fields the posting lists: %q reads the posting's degree fields %q", clip(req.Questions[i].Text, 60), fields)}, rr.Trace...)
		results[i] = rr
	}
}

// postedDegreeFields is the fields of the posting's degree requirement at the
// level the question names before its reference, or of the posting's only
// requirement when it names none.
func postedDegreeFields(desc, before string) (string, bool) {
	ms := postedDegreeFieldsRE.FindAllStringSubmatch(apostrophes.Replace(desc), -1)
	asked, named := educationLevelOf(degreeLevelWordRE.FindString(apostrophes.Replace(before)))
	var hits []string
	for _, m := range ms {
		lv, ok := educationLevelOf(m[1])
		if ok && (!named || lv == asked) {
			hits = append(hits, strings.TrimSpace(m[2]))
		}
	}
	if len(hits) == 0 || !named && len(hits) > 1 {
		return "", false
	}
	return hits[0], true
}
