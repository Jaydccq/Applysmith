package match

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"applysmith/internal/gates"
	"applysmith/internal/lazyregexp"
)

// "How many years of experience do you have with <skill>?" is answered from the
// years the user recorded for that skill in the profile and from nothing else:
// a skill with no recorded years abstains and no default is ever supplied.

var (
	// yearsAnchorRE marks a years or months of experience ask.
	yearsAnchorRE = lazyregexp.New(`(?i)\b(?:how\s+many\s+(?:years|months)|(?:years?|months?)\s+of\s+(?:work\s+|professional\s+|relevant\s+|total\s+|hands[-\s]?on\s+)?experience)\b`)
	// withSkillTailRE reads the skill after "with" or "using", up to an
	// "do you have" that closes the question (Nomic: "working full time with
	// biological data and algorithms do you have?").
	withSkillTailRE = lazyregexp.New(`(?i)\b(?:with|using)\s+(.+?)(?:\s+(?:do|did)\s+you\s+have)?\s*\??\s*$`)
	// workDoneRE reads the work an experience ask names after "do you have"
	// (Island: "... experience do you have developing production software in
	// Go?"); shortInTailRE is a short "in <skill>" that ends it, the skill.
	workDoneRE    = lazyregexp.New(`(?i)\bexperience\s+(?:do|did)\s+you\s+have\s+(?:developing|building|writing|designing|programming|coding|running|operating|working\s+(?:on|in))\s+(.+?)\s*\??\s*$`)
	shortInTailRE = lazyregexp.New(`(?i)\s(?:in|with|using)\s+((?:[\w+#./-]+\s*){1,3})$`)
	// embeddedSubjectRE reads the subject between "years of" and "experience".
	embeddedSubjectRE = lazyregexp.New(`(?i)\b(?:years?|months?)\s+of\s+(.+?)\s+experience\b`)
	howManyYearsRE    = lazyregexp.New(`(?i)\bhow\s+many\s+(?:years|months)\b`)
	// bareYearsOfRE reads a whole label such as "Years of backend development*".
	bareYearsOfRE = lazyregexp.New(`(?i)^\s*(?:years?|months?)\s+of\s+(.+?)\s*\*?\s*$`)
	// roleYearsRE reads the kind of role of "How many years of experience do
	// you have in a customer-facing technical role ...?" (BigID): a subject of
	// its own, never the career. postingRoleRE is a similar, related or the
	// same role, which is the posting's. Blumira: "How many years have you
	// spent in a customer-facing technical role?" asks the same.
	roleYearsRE = lazyregexp.New(`(?i)\b(?:experience\s+(?:do\s+you\s+have\s+)?(?:working\s+)?|have\s+you\s+(?:spent|worked)\s+)(?:in|as)\s+an?\s+((?:[\w-]+\s+){1,4}?)(?:role|position)s?\b`)
	// asRoleYearsRE reads the role of "... experience do you have as a
	// financial analyst?", which names the role without the word: the phrase
	// after "as a" up to the end, a mark or a preposition. notRoleRE is a
	// manner ("as a whole", "as a team") or the career itself ("as a
	// full-time employee"), never a role.
	asRoleYearsRE = lazyregexp.New(`(?i)\b(?:experience\s+(?:do\s+you\s+have\s+)?(?:working\s+)?|have\s+you\s+(?:spent|worked)\s+|worked\s+)as\s+an?\s+([\w-]+(?:\s+[\w-]+){0,3}?)\s*(?:[?.,;:*(]|$|\s(?:in|at|for|with|within|on|and|or)\b)`)
	notRoleRE     = lazyregexp.New(`(?i)^(?:(?:full|part)[- ]time\s+|paid\s+)?(?:whole|team|result|group|member|professional|employee|worker|total|minimum)(?:\s+of\b.*)?$`)
	postingRoleRE = lazyregexp.New(`(?i)^(?:similar|related|comparable|equivalent|same|like)\b`)
	// inSubjectRE reads the subject of "... experience do you have in
	// <subject>?" (Flexport: "in Accounts Payable, freight auditing, or
	// account reconciliation"); notSubjectRE is a kind of place or role ("in
	// this field", "in a similar role"), never a subject.
	inSubjectRE  = lazyregexp.New(`(?i)\bexperience\s+(?:do|did)\s+you\s+have\s+in\s+(.+?)\s*\??\s*$`)
	notSubjectRE = lazyregexp.New(`(?i)^(?:an?|the|this|that|these|those|your|our|such)\b|\b(?:roles?|positions?|fields?|capacity|capacities|jobs?)\b`)
	skillSplitRE = lazyregexp.New(`[^a-z0-9+#./]+`)
)

// skillStopWords name no skill on their own. The generic words at the end make
// "industry experience" or "relevant experience" an empty subject, which leaves
// the question to the total-years entry.
var skillStopWords = setOf(
	"the", "a", "an", "and", "or", "of", "in", "with", "to", "for", "using",
	"your", "you", "our", "their", "this", "that", "any", "some",
	"experience", "years", "year", "work", "working", "professional",
	"do", "have", "many", "how", "tool", "tools", "platform", "platforms",
	"relevant", "industry", "total", "overall", "hands", "on", "development",
)

// foldPlural folds a trailing plural s on tokens of four or more letters, so
// "LLMs" meets "LLM" while ios, aws and js are left alone.
func foldPlural(t string) string {
	if len(t) >= 4 && !strings.ContainsAny(t, ".+#/") && strings.HasSuffix(t, "s") && !strings.HasSuffix(t, "ss") {
		return t[:len(t)-1]
	}
	return t
}

// skillTokens splits a skill phrase into lowercase tokens that keep + # . and /
// (so c++, c# and node.js survive), drops parentheses and stop words, and folds
// plurals.
func skillTokens(phrase string) []string {
	phrase = strings.NewReplacer("(", " ", ")", " ").Replace(strings.ToLower(phrase))
	var out []string
	for _, t := range skillSplitRE.Split(phrase, -1) {
		t = strings.Trim(t, "./")
		if t == "" || skillStopWords[t] {
			continue
		}
		out = append(out, foldPlural(t))
	}
	return out
}

// yearsSkillFromQuestion extracts the skill of a "years of experience with
// <skill>" question, or "" when the question is not one.
func yearsSkillFromQuestion(text string) string {
	text = askedSentence(text)
	if m := howLongWithRE.FindStringSubmatch(text); m != nil {
		return strings.TrimSpace(m[1])
	}
	if !yearsAnchorRE.MatchString(text) {
		return ""
	}
	if m := withSkillTailRE.FindStringSubmatch(text); m != nil {
		return strings.TrimSpace(m[1])
	}
	if m := workDoneRE.FindStringSubmatch(text); m != nil {
		if t := shortInTailRE.FindStringSubmatch(" " + m[1]); t != nil {
			return strings.TrimSpace(t[1])
		}
		return strings.TrimSpace(m[1])
	}
	return ""
}

// howLongWithRE asks a skill's years as a duration (PolicyMe: "How long have
// you been working with React.js?"). "In" names a skill only after a verb of
// writing code, so "working in the United States" names none.
var howLongWithRE = lazyregexp.New(`(?i)\bhow\s+long\s+have\s+you\s+(?:been\s+)?(?:(?:working|worked)\s+with|using|used|(?:programming|coding|developing)\s+(?:with|in))\s+(.+?)\s*\??\s*$`)

// askedSentence is the text up to its first question mark: the guidance after
// a question ("Please provide an accurate estimate based on projects ...")
// names no skill of it.
func askedSentence(text string) string {
	if i := strings.Index(text, "?"); i >= 0 {
		return text[:i+1]
	}
	return text
}

// embeddedYearsSubject extracts the subject of "How many years of <subject>
// experience" or a whole label "Years of <subject>"; a residue of only generic
// words names no skill.
func embeddedYearsSubject(text string) string {
	if yearsAnchorRE.MatchString(text) {
		if role := roleYearsSubject(text); role != "" {
			return role
		}
		if withSkillTailRE.MatchString(text) || !howManyYearsRE.MatchString(text) {
			return ""
		}
		if m := embeddedSubjectRE.FindStringSubmatch(text); m != nil {
			if subject := subjectOrEmpty(m[1]); subject != "" {
				return subject
			}
		}
		return inYearsSubject(text)
	}
	if m := bareYearsOfRE.FindStringSubmatch(text); m != nil {
		return subjectOrEmpty(m[1])
	}
	return ""
}

// inYearsSubject is the subject of "... experience do you have in
// <subject>?", or "" for a kind of place or role.
func inYearsSubject(text string) string {
	if m := inSubjectRE.FindStringSubmatch(askedSentence(text)); m != nil && !notSubjectRE.MatchString(m[1]) {
		return subjectOrEmpty(m[1])
	}
	return ""
}

// roleYearsSubject is the kind of role a years question asks about, or "".
func roleYearsSubject(text string) string {
	m := roleYearsRE.FindStringSubmatch(text)
	if m == nil {
		if m = asRoleYearsRE.FindStringSubmatch(text); m == nil || notRoleRE.MatchString(m[1]) {
			return ""
		}
	}
	if postingRoleRE.MatchString(m[1]) {
		return ""
	}
	return subjectOrEmpty(m[1])
}

// roleYears reports a years question about a kind of role, which a
// total-years route leaves.
func roleYears(text string) bool { return roleYearsSubject(text) != "" }

func subjectOrEmpty(captured string) string {
	phrase := strings.TrimSpace(strings.TrimRight(captured, "* "))
	if phrase == "" || len(skillTokens(phrase)) == 0 {
		return ""
	}
	return phrase
}

// experienceUnit is "months" when the question asks in months, else "years".
func experienceUnit(text string) string {
	if monthWordRE.MatchString(text) {
		return "months"
	}
	return "years"
}

// shortSkillLeadRE is what stands before a one-letter skill name a text asks
// about: nothing, a list mark, or a preposition, a conjunction or "your".
var shortSkillLeadRE = lazyregexp.New(`(?i)(?:^|[,/(:;]|\b(?:with|in|using|of|and|or|both|either|plus|your)\s)\s*$`)

// shortSkillWritten reports that text writes the skill name as a name. A name
// of one or two letters ("Go", "R", "C", "AI") is also an ordinary word or a
// letter of another term, so it is read only capitalised and as a word of its
// own: never inside "R&D", and at the head of a hyphenated compound only as
// an acronym ("AI-powered", not "Go-to-market" or "C-level"). A single letter
// must also stand where shortSkillLeadRE lets a subject stand ("with C",
// "Python, R"; "Series C" is no C). A longer name, or one with a symbol
// ("C#"), is read by its tokens alone.
func shortSkillWritten(text, name string) bool {
	name = strings.TrimSpace(name)
	if len(name) > 2 || strings.ContainsAny(name, "+#") {
		return true
	}
	joins := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("&+#", r) }
	for i := 0; i+len(name) <= len(text); i++ {
		word := text[i : i+len(name)]
		if !strings.EqualFold(word, name) || !unicode.IsUpper(rune(word[0])) {
			continue
		}
		before, _ := utf8.DecodeLastRuneInString(text[:i])
		after, _ := utf8.DecodeRuneInString(text[i+len(name):])
		acronym := len(word) == 2 && word == strings.ToUpper(word)
		switch {
		case i > 0 && joins(before), joins(after):
		case !acronym && (i > 0 && before == '-' || after == '-'):
		case len(name) == 1 && !shortSkillLeadRE.MatchString(text[:i]):
		default:
			return true
		}
	}
	return false
}

// profileSkillYears is the years recorded for the asked skill. Every token of
// the profile skill's name must be among the asked tokens, so a Microsoft Excel
// row never answers a Word question, and the skill with the most tokens wins.
// A skill with no recorded or a negative figure is ignored, and so is a short
// name the asked phrase does not write as one (shortSkillWritten).
func profileSkillYears(skill string, skills []SkillFact) (years float64, name string, ok bool) {
	asked := tokenSet(skillTokens(skill))
	if len(asked) == 0 {
		return 0, "", false
	}
	bestTokens := 0
	for _, sk := range skills {
		if sk.Years == nil || *sk.Years < 0 || math.IsNaN(*sk.Years) || math.IsInf(*sk.Years, 0) {
			continue
		}
		toks := skillTokens(sk.Name)
		if len(toks) == 0 || !shortSkillWritten(skill, sk.Name) {
			continue
		}
		all := true
		for _, t := range toks {
			if !asked[t] {
				all = false
				break
			}
		}
		if all && len(toks) > bestTokens {
			years, name, bestTokens, ok = *sk.Years, sk.Name, len(toks), true
		}
	}
	if ok {
		return years, name, ok
	}
	// No row names the skill: a row of a kind of it holds years with it too,
	// at least as many (Bree: three years with LLMs are three with machine
	// learning).
	for _, sk := range skills {
		if sk.Years == nil || *sk.Years < 0 || math.IsNaN(*sk.Years) || math.IsInf(*sk.Years, 0) {
			continue
		}
		for _, field := range skillFields[strings.Join(skillTokens(sk.Name), " ")] {
			if ftoks := skillTokens(field); len(ftoks) > 0 && hasEveryToken(asked, ftoks) && (!ok || *sk.Years > years) {
				years, name, ok = *sk.Years, sk.Name+" (a kind of "+field+")", true
			}
		}
	}
	return years, name, ok
}

// skillFields names the fields a recorded skill is a kind of, keyed by its
// tokens: years with the skill are years with the field, never the reverse.
var skillFields = func() map[string][]string {
	ml := []string{"machine learning", "ml", "artificial intelligence", "ai"}
	out := map[string][]string{}
	for _, kind := range []string{"llm", "large language model", "deep learning", "neural network", "nlp", "natural language processing",
		"computer vision", "reinforcement learning", "generative ai", "genai"} {
		out[kind] = ml
	}
	out["machine learning"] = []string{"artificial intelligence", "ai"}
	out["ml"] = []string{"artificial intelligence", "ai"}
	return out
}()

// hasEveryToken reports whether every token is in the set.
func hasEveryToken(set map[string]bool, toks []string) bool {
	for _, t := range toks {
		if !set[t] {
			return false
		}
	}
	return true
}

// yearsBucket is a numeric range read from an option label. Bounds are whole
// numbers; a missing bound is unbounded. over marks a strict bound ("more than
// 5"): lo is the whole number after it, and a fraction above the bound is in
// the range too.
type yearsBucket struct {
	lo, hi float64
	over   bool
}

var (
	bucketLessRE  = lazyregexp.New(`^\s*(less\s+than|under|fewer\s+than|below|<)\s*(\d+)`)
	bucketUpToRE  = lazyregexp.New(`^\s*up\s+to\s+(\d+)`)
	bucketRangeRE = lazyregexp.New(`(\d+)\s*(?:-|to|\x{2013})\s*(\d+)`)
	bucketOpenRE  = lazyregexp.New(`(\d+)\s*(?:\+|plus\b|or\s+(?:more|above|over|greater|higher)\b|and\s+(?:above|over|up|higher)\b)`)
	bucketFloorRE = lazyregexp.New(`(?:at\s+least|minimum(?:\s+of)?|more\s+than|over|greater\s+than)\s+(\d+)`)
	// bucketBetweenRE is a range stated by both its bounds ("More than 1 year
	// but less than 2 years"): a value on the lower bound is in it.
	bucketBetweenRE = lazyregexp.New(`(?:more\s+than|over|greater\s+than|at\s+least)\s+(\d+)\b.*?\b(?:less\s+than|fewer\s+than|under|below)\s+(\d+)`)
	// bucketMonthsToYearsRE is a range from months up to years ("6 months to 1
	// year"), which parseYearsBucket gives in years.
	bucketMonthsToYearsRE = lazyregexp.New(`(\d+)\s*months?\s*(?:-|to|\x{2013})\s*(\d+)\s*(?:years?|yrs?)\b`)
)

// bucketUnit is the unit an option's numbers count: the label's own when it
// names months alone or years, else the unit the question asks in.
func bucketUnit(label, asked string) string {
	switch {
	case yearWordRE.MatchString(label):
		return "years"
	case monthWordRE.MatchString(label):
		return "months"
	}
	return asked
}

// bucketInYears is an option's range in years, whatever unit it counts.
func bucketInYears(label, asked string) (yearsBucket, bool) {
	b, ok := parseYearsBucket(label)
	if ok && bucketUnit(label, asked) == "months" {
		b.lo, b.hi = b.lo/12, b.hi/12
	}
	return b, ok
}

// parseYearsBucket reads "1-3", "1 to 3 years", "7+", "at least 7", "less than
// 1", "up to 2" and "more than 1 but less than 3". A bare integer is not a range; the integer ladder owns it.
// The numbers are in the label's unit (bucketUnit).
func parseYearsBucket(label string) (yearsBucket, bool) {
	l := strings.ToLower(strings.ReplaceAll(label, ",", ""))
	num := func(s string) float64 { n, _ := strconv.Atoi(s); return float64(n) }
	inf := math.Inf(1)
	switch {
	case bucketMonthsToYearsRE.MatchString(l):
		m := bucketMonthsToYearsRE.FindStringSubmatch(l)
		return yearsBucket{lo: num(m[1]) / 12, hi: num(m[2])}, true
	case bucketBetweenRE.MatchString(l):
		m := bucketBetweenRE.FindStringSubmatch(l)
		if lo, hi := num(m[1]), num(m[2])-1; hi >= lo {
			return yearsBucket{lo: lo, hi: hi}, true
		}
		return yearsBucket{}, false
	case bucketLessRE.MatchString(l):
		m := bucketLessRE.FindStringSubmatch(l)
		return yearsBucket{lo: -inf, hi: num(m[2]) - 1}, true
	case bucketUpToRE.MatchString(l):
		return yearsBucket{lo: -inf, hi: num(bucketUpToRE.FindStringSubmatch(l)[1])}, true
	}
	if m := bucketRangeRE.FindStringSubmatch(l); m != nil {
		lo, hi := num(m[1]), num(m[2])
		if hi >= lo {
			return yearsBucket{lo: lo, hi: hi}, true
		}
		return yearsBucket{}, false
	}
	if m := bucketOpenRE.FindStringSubmatch(l); m != nil {
		return yearsBucket{lo: num(m[1]), hi: inf}, true
	}
	if m := bucketFloorRE.FindStringSubmatch(l); m != nil {
		lo := num(m[1])
		over := strings.HasPrefix(m[0], "more") || strings.HasPrefix(m[0], "over") || strings.HasPrefix(m[0], "greater")
		if over {
			lo++
		}
		return yearsBucket{lo, inf, over}, true
	}
	return yearsBucket{}, false
}

var integerRungRE = lazyregexp.New(`(?i)^\s*(\d{1,2})\s*(\+)?\s*(?:years?|yrs?)?\s*(\+)?\s*$`)

// pickIntegerYearsRung places a number on a ladder of bare integers: the rung
// equal to the whole number, else the highest open "N+" rung at or below it. A
// ladder that stops short of the number gives nothing, never a rounded-down rung.
func pickIntegerYearsRung(opts []Option, whole int) (Option, bool) {
	var open Option
	openN, haveOpen := -1, false
	for _, o := range opts {
		if isPlaceholderOption(o.Label) {
			continue
		}
		m := integerRungRE.FindStringSubmatch(o.Label)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		isOpen := m[2] != "" || m[3] != ""
		if !isOpen && n == whole {
			return o, true
		}
		if isOpen && n <= whole && n > openN {
			open, openN, haveOpen = o, n, true
		}
	}
	return open, haveOpen
}

// pickYearsBucket returns the option whose range contains the years, each
// counted in the option's own unit (bucketUnit, asked being the question's):
// of several, the one for which the whole number is the lower bound, or whose
// strict bound the amount is past, so contiguous buckets that share an edge do
// not tie. Without a range it tries the integer ladder.
func pickYearsBucket(opts []Option, years float64, asked string) (Option, bool) {
	in := func(unit string) float64 {
		if unit == "months" {
			return years*12 + 1e-9
		}
		return years + 1e-9
	}
	var first, lower *Option
	for i := range opts {
		o := &opts[i]
		if isPlaceholderOption(o.Label) {
			continue
		}
		b, ok := parseYearsBucket(o.Label)
		amount := in(bucketUnit(o.Label, asked))
		n := math.Floor(amount)
		if !ok || n > b.hi || amount < b.lo && !(b.over && amount > b.lo-1) {
			continue
		}
		if first == nil {
			first = o
		}
		if lower == nil && (b.lo == n || b.over) {
			lower = o
		}
	}
	switch {
	case lower != nil:
		return *lower, true
	case first != nil:
		return *first, true
	}
	return pickIntegerYearsRung(opts, int(math.Floor(in(asked))))
}

// levelOfExperienceRE asks for a level of experience with a skill; it is a
// years question when every option is a years range (InRule: "What is your
// level of professional experience with React?" over None / Less than 2 years
// / 2 to 4 years / 5+ years).
var levelOfExperienceRE = lazyregexp.New(`(?i)\blevel\s+of\s+(?:professional\s+|work\s+|hands[-\s]?on\s+)?experience\b`)

// howMuchExperienceRE is "How much professional experience do you have
// writing production Python?" (Fairmarkit), or "Do you have hands-on
// experience applying engineering principles ...?" (Energy Solutions); over
// a years menu it asks the years with the captured skill.
var howMuchExperienceRE = lazyregexp.New(`(?i)\b(?:how\s+much\s+(?:professional\s+|work\s+|hands[-\s]?on\s+)?experience\s+do\s+you\s+have|do\s+you\s+have\s+(?:any\s+)?(?:professional\s+|work\s+|hands[-\s]?on\s+)?experience)\s+(?:with|using|in|writing|building|developing|applying)\s+(?:production\s+)?(.+?)\s*\??\s*$`)

// skillYears is the pre-classification lane for a skill-qualified years
// question ("... experience with <skill>").
func skillYears(s *Step) (Draft, bool) {
	skill := yearsSkillAsked(s.Q.Text, s.Q.Options)
	if skill == "" {
		return Draft{}, false
	}
	return skillYearsAnswer(s, skill)
}

// yearsSkillAsked is the skill a years question asks about, or "".
func yearsSkillAsked(text string, opts []Option) string {
	skill := yearsSkillFromQuestion(text)
	if skill == "" && levelOfExperienceRE.MatchString(text) && yearsMenu(opts) {
		if m := withSkillTailRE.FindStringSubmatch(text); m != nil {
			skill = strings.TrimSpace(m[1])
		}
	}
	if skill == "" && yearsMenu(opts) {
		if m := howMuchExperienceRE.FindStringSubmatch(text); m != nil {
			skill = strings.TrimSpace(m[1])
		}
	}
	return skill
}

// OwnYearsQuestion reports a question about the user's own years with a named
// skill, or in a subject or kind of role (Akuity: "in a technical marketing
// role", which skillYearsEmbedded reads): its F-10 search reads the knowledge
// base like an own-experience question's.
func OwnYearsQuestion(text string, opts []Option) bool {
	return yearsSkillAsked(text, opts) != "" || embeddedYearsSubject(text) != ""
}

// yearsMenu reports options that are all years ranges, one of them possibly
// the option for none.
func yearsMenu(opts []Option) bool {
	real := realOptions(opts)
	ranges := 0
	for _, o := range real {
		if _, ok := parseYearsBucket(o.Label); ok {
			ranges++
		} else if !noneYearsRE.MatchString(o.Label) {
			return false
		}
	}
	return ranges >= 2
}

// noneYearsRE is the option for no experience at all.
var noneYearsRE = lazyregexp.New(`(?i)^\s*(?:none|no\s+experience|0(?:\s+years?)?)\s*$`)

// skillYearsEmbedded handles "How many years of <subject> experience" and "Years
// of <subject>" only when classification finds no entry for the question, so an
// entry the user curated for it keeps the question. A kind of role ("in a
// customer-facing technical role") gives way to no fuzzy pick: the race finds
// the career's years by the words they share. Nor does a subject none of the
// user's information names (Flexport's Accounts Payable): it has no years.
func skillYearsEmbedded(s *Step) (Draft, bool) {
	skill := embeddedYearsSubject(s.Q.Text)
	if skill == "" {
		return Draft{}, false
	}
	cx := newClassifier(s.Index, s.Q, s.scorer())
	if _, hit := cx.curated(); hit {
		return Draft{}, false
	}
	if cl := cx.general(); cl.entry != nil && (cl.stage != stageFuzzy || roleYearsSubject(s.Q.Text) == "" && !namedNowhere(s, skill)) {
		return Draft{}, false
	}
	return skillYearsAnswer(s, skill)
}

// namedNowhere reports a subject that none of the user's information names,
// read whole.
func namedNowhere(s *Step, subject string) bool {
	if _, _, ok := profileSkillYears(subject, s.Req.Facts.Skills); ok {
		return false
	}
	return s.ownMentions(s.Q.Text, s.Q.Text, subjectTerms(subject), true).Absent()
}

// subjectTerms are the words a years subject is searched by. A word naming the
// applicant's whole field says nothing of the subject while another word names
// it: "technical indicators" is no technical marketing (Akuity). Two field
// words run together ("softwareengineer") are field words too, so "as a
// software engineer" is searched by every word and an "Engineer" title is a
// mention.
func subjectTerms(subject string) []string {
	terms := AskedAbout(subject)
	field := func(t string) bool {
		for w := range thresholdFieldWords {
			if rest, ok := strings.CutPrefix(t, w); ok && (rest == "" || thresholdFieldWords[rest]) {
				return true
			}
		}
		return false
	}
	if content := slices.DeleteFunc(slices.Clone(terms), field); len(content) > 0 {
		return content
	}
	return terms
}

// skillYearsAnswer answers the control from the profile's years for the skill.
func skillYearsAnswer(s *Step, skill string) (Draft, bool) {
	q := s.Q
	if roleYearsSubject(q.Text) == skill {
		if d, ok := roleTitleYears(s, skill); ok {
			return d, true
		}
	}
	wantsBucket := len(q.Options) > 0
	if q.Kind != KindText && !wantsBucket {
		return Draft{}, false
	}
	years, name, ok := profileSkillYears(skill, s.Req.Facts.Skills)
	if !ok && !isYesNoOptionSet(q.Options) {
		// None of the user's information names the skill: no years with it
		// (F-10; Huntress's "How many years of experience do you have working
		// with Ruby on Rails?").
		o := s.ownMentions(s.Q.Text, s.Q.Text, subjectTerms(skill), true)
		if o.Absent() {
			return noYears(s, o.Terms)
		}
		switch {
		case !o.Complete:
			s.Tracef("skill-experience-years: the knowledge base was not read whole, so %q is not known to be named nowhere", skill)
		default:
			s.Tracef("skill-experience-years: %q is named in %s", skill, strings.Join(o.Found, "; "))
		}
	}
	if !ok {
		if d, bounded := boundedByTotalYears(s); bounded {
			return d, true
		}
		// A Yes/No goes on to the experience rule of the option lane.
		if isYesNoOptionSet(q.Options) {
			return abstainDraft(ReasonNoEntry, "", "skill-experience-years: skill=%q has no recorded years in the profile -> abstain", skill), true
		}
		return unrecordedYears(s, skill)
	}
	if isYesNoOptionSet(q.Options) {
		// The years threshold did not settle it: the Yes/No goes on to the
		// experience rule of the option lane.
		return abstainDraft(ReasonNoEntry, "", "skill-experience-years: %s does not settle the Yes/No for skill=%q -> abstain", name, skill), true
	}
	unit := experienceUnit(q.Text)
	amount := int(math.Floor(years + 1e-9))
	if unit == "months" {
		amount = int(math.Floor(years*12 + 1e-9))
	}
	trace := "skill-experience-years: skill=" + strconv.Quote(skill) + " profile skill " + strconv.Quote(name) +
		" = " + strconv.FormatFloat(years, 'f', -1, 64) + " years, unit=" + unit + " -> " + strconv.Itoa(amount)
	if !wantsBucket {
		return derivedDraft(yearsAmountText(q.Text, amount, unit), "", startProfileRow, trace), true
	}
	opt, found := pickYearsBucket(q.Options, years, unit)
	if !found {
		return abstainDraft(ReasonInputMismatch, "",
			"%s, no bucket among %d options -> abstain", trace, len(q.Options)), true
	}
	d := derivedDraft(opt.Label, "", startProfileRow, trace+" -> bucket "+strconv.Quote(opt.Label))
	d.ChosenLabel, d.HasOption, d.OptionScore = opt.Label, true, 1
	return d, true
}

// yearsAmountText writes an amount of experience for a box: the bare number
// when the question names its unit ("How many years ..."), else the number
// with its unit (PolicyMe: "How long have you been working with React.js?" ->
// "1 year").
func yearsAmountText(text string, amount int, unit string) string {
	if unitWordRE.MatchString(text) {
		return strconv.Itoa(amount)
	}
	if amount == 1 {
		unit = strings.TrimSuffix(unit, "s")
	}
	return strconv.Itoa(amount) + " " + unit
}

var unitWordRE = lazyregexp.New(`(?i)\b(?:years?|yrs?|months?)\b`)

// unrecordedYears answers a years question about a subject the user's
// information names but records no years for with the owner's default for an
// experience the record does not settle: one year in a box, twelve months when
// months are asked, and the lowest bucket that is not "none" in a list
// (Tailscale's "<1"), always for review.
func unrecordedYears(s *Step, skill string) (Draft, bool) {
	s.reviewOnly = true
	trace := "skill-experience-years: skill=" + strconv.Quote(skill) + " has no recorded years -> the owner's default of one year, for review"
	if len(s.Q.Options) == 0 {
		amount, unit := 1, experienceUnit(s.Q.Text)
		if unit == "months" {
			amount = 12
		}
		d := derivedDraft(yearsAmountText(s.Q.Text, amount, unit), "", startProfileRow, trace)
		d.Source = SourceOwnerDefault
		return d, true
	}
	low := lowestYearsOption(s.Q.Options, experienceUnit(s.Q.Text))
	if low == nil {
		return abstainDraft(ReasonInputMismatch, "", "%s, no years bucket among %d options -> abstain", trace, len(s.Q.Options)), true
	}
	d := derivedDraft(low.Label, "", startProfileRow, trace+" -> the lowest bucket "+strconv.Quote(low.Label))
	d.ChosenLabel, d.HasOption, d.OptionScore = low.Label, true, 1
	d.Source = SourceOwnerDefault
	return d, true
}

// lowestYearsOption is the lowest option of a years list that is not "none":
// a range, or a rung of a ladder of whole years ("1 year", "2 years", "3+
// years"), which parseYearsBucket leaves to the integer ladder.
func lowestYearsOption(opts []Option, unit string) *Option {
	var low *Option
	var lowB yearsBucket
	for _, o := range realOptions(opts) {
		b, ok := bucketInYears(o.Label, unit)
		if m := integerRungRE.FindStringSubmatch(o.Label); !ok && m != nil {
			n, _ := strconv.Atoi(m[1])
			b, ok = yearsBucket{lo: float64(n), hi: float64(n)}, n > 0
			if m[2] != "" || m[3] != "" {
				b.hi = math.Inf(1)
			}
		}
		if !ok || noneYearsRE.MatchString(o.Label) || b.hi < 0 {
			continue
		}
		if low == nil || b.lo < lowB.lo || b.lo == lowB.lo && b.hi < lowB.hi {
			low, lowB = &o, b
		}
	}
	return low
}

// boundedByTotalYears answers a professional years question about a skill with
// no recorded years from the recorded total years (totalYears): the skill's
// years lie between none and the total's upper bound, so the one option whose range holds
// the whole span is the answer (Mattermost: "How many years of professional
// full-stack experience?" over "0-2 years", "3-4 years", ... with one year of
// work). Another option inside the span ("None", "Less than 1 year") parts it,
// and the question is left.
func boundedByTotalYears(s *Step) (Draft, bool) {
	if len(s.Q.Options) == 0 || !professionalYearsRE.MatchString(strings.ToLower(s.Q.Text)) {
		return Draft{}, false
	}
	_, total, entry, ok := totalYears(s.Entries)
	if !ok || total < 0 || math.IsInf(total, 1) {
		return Draft{}, false
	}
	var span *Option
	for _, o := range realOptions(s.Q.Options) {
		b, ok := bucketInYears(o.Label, experienceUnit(s.Q.Text))
		switch {
		case noneYearsRE.MatchString(o.Label), ok && b.lo <= total && b.hi >= 0 && (b.lo > 0 || b.hi < total):
			return Draft{}, false
		case ok && b.lo <= 0 && b.hi >= total:
			if span != nil {
				return Draft{}, false
			}
			span = &o
		}
	}
	if span == nil {
		return Draft{}, false
	}
	trace := "skill-experience-years: no years recorded for the skill; its professional years lie within the total of " +
		quoteTerm(entry) + ", all in " + strconv.Quote(span.Label)
	d := derivedDraft(span.Label, "", startProfileRow, trace)
	d.ChosenLabel, d.HasOption, d.OptionScore = span.Label, true, 1
	return d, true
}

// noYears answers a years question about a skill none of the user's
// information names: "0" in a box, the menu's option for none, else the range
// that holds zero.
func noYears(s *Step, terms []string) (Draft, bool) {
	trace := "skill-experience-years: none of the user's information mentions " + strings.Join(terms, ", ") + " -> 0 years (F-10)"
	if len(s.Q.Options) == 0 {
		return derivedDraft("0", "", startProfileRow, trace), true
	}
	for _, o := range realOptions(s.Q.Options) {
		if noneYearsRE.MatchString(o.Label) {
			d := derivedDraft(o.Label, "", startProfileRow, trace+" -> "+strconv.Quote(o.Label))
			d.ChosenLabel, d.HasOption, d.OptionScore = o.Label, true, 1
			return d, true
		}
	}
	opt, found := pickYearsBucket(s.Q.Options, 0, experienceUnit(s.Q.Text))
	if !found {
		return abstainDraft(ReasonInputMismatch, "", "%s, no option holds zero -> abstain", trace), true
	}
	d := derivedDraft(opt.Label, "", startProfileRow, trace+" -> bucket "+strconv.Quote(opt.Label))
	d.ChosenLabel, d.HasOption, d.OptionScore = opt.Label, true, 1
	return d, true
}

// isYearsEntry reports an entry that stores years of experience as a number
// or the range that holds it.
func isYearsEntry(e *Entry) bool {
	return e != nil && (strings.HasPrefix(e.ID, YearsExperiencePrefix) || e.ID == IDYearsFullTimeWork)
}

// yearsEntryNumber is the whole number a years entry's answer stands for: the
// lower bound of the range it stores ("3-5" is 3, "5+" is 5, "Less than 1
// year" is 0), else its first integer. It reports false for an answer with
// neither.
func yearsEntryNumber(answer string) (int, bool) {
	if b, ok := parseYearsBucket(answer); ok {
		return int(math.Max(b.lo, 0)), true
	}
	n, err := strconv.Atoi(firstDigitsRE.FindString(answer))
	return n, err == nil
}

// yearsEntryBucket places the total a years entry stores ("0-1 years", "5+",
// "3") on a bucket menu by its whole number (yearsEntryNumber), never by
// reading the stored range as the bare number. It reports false when the
// answer has no number or no bucket holds it, so the caller leaves the
// question rather than guess.
func yearsEntryBucket(answer string, opts []Option) (Option, bool) {
	n, ok := yearsEntryNumber(answer)
	if !ok {
		return Option{}, false
	}
	return pickYearsBucket(opts, float64(n), "years")
}

// "Do you have 3+ years of experience with Python?" is a threshold the role
// sets (gates.YearsThreshold), answered Yes or No against the years the user
// recorded and nothing else: the profile's years for each skill the question
// names (a skill the CV lists counts at its default years), none for a subject
// none of the user's information mentions (F-10), and the total-years entry
// for experience the question does not narrow. A narrower experience never
// has more years than a broader one, so a skill or the total below the floor
// is No however the question narrows it; Yes needs every word naming the
// subject to be a recorded skill's, or none at all.

// thresholdFrameWords say how the experience was gained, not what it is in.
var thresholdFrameWords = setOf(
	"coding", "code", "programming", "building", "build", "scaling", "writing", "developing", "using", "based",
	"production", "commercial", "hand", "practical", "direct", "solid", "strong", "proven", "prior", "previous",
	"minimum", "least", "plus", "role", "roles", "position", "positions", "setting", "environment",
)

// professionalYearsRE says the experience is the applicant's working years,
// which the total-years entry bounds from above.
var professionalYearsRE = lazyregexp.New(`\b(?:professional|work|industry|commercial|full[- ]?time)\b`)

// thresholdFieldWords name the applicant's whole field rather than a skill:
// the total bounds them from above but need not all be in them.
var thresholdFieldWords = setOf("software", "engineering", "engineer", "developer", "technical", "technology", "tech")

// workedForFloorRE asks whether the applicant has worked somewhere for at
// least a span (Akuity: "Have you worked at a B2B SaaS startup (Seed to Series
// C) for at least 1 year?"): the floor's kind, number and unit.
var workedForFloorRE = lazyregexp.New(`(?i)\bhave\s+you\s+(?:ever\s+)?(?:worked|been\s+(?:employed|working))\b[^?]*?\bfor\s+(?:(at\s+least|a\s+minimum\s+of|minimum\s+of)|(more\s+than|over))\s+(\d+|` + countWords + `)\s*\+?\s*(years?|yrs?|months?)\b`)

// workedForFloor answers No to having worked somewhere for at least a span
// that every job the user recorded together does not reach: whole months
// from each start through its end, an ongoing job through today. A narrower
// kind of employer never holds more. It abstains on a record that reaches the
// floor, which says nothing of the kind, or on a job without a start.
func workedForFloor(s *Step) (Draft, bool) {
	m := workedForFloorRE.FindStringSubmatch(s.Q.Text)
	if m == nil || len(s.Req.Facts.Employment) == 0 || s.Req.Today.Year == 0 {
		return Draft{}, false
	}
	no, okN := ownYesNo(s.Q.Options, false)
	if _, okY := ownYesNo(s.Q.Options, true); !okY || !okN {
		return Draft{}, false
	}
	n, err := strconv.Atoi(m[3])
	if err != nil {
		n = numberWords[strings.ToLower(m[3])]
	}
	floor := n
	if !strings.HasPrefix(strings.ToLower(m[4]), "m") {
		floor = n * 12
	}
	today := monthOf(s.Req.Today).key()
	months := 0
	for _, e := range s.Req.Facts.Employment {
		start, ok := parseYearMonth(e.Start)
		if !ok {
			return Draft{}, false
		}
		end := today
		if !e.OngoingEnd() {
			ym, ok := parseYearMonth(e.End)
			if !ok {
				return Draft{}, false
			}
			end = min(ym.key(), today)
		}
		months += max(end-start.key()+1, 0)
	}
	if strict := m[2] != ""; months > floor || !strict && months == floor {
		return Draft{}, false
	}
	d := derivedDraft(no.Label, "", startProfileRow, fmt.Sprintf("worked-for floor (%d months): every recorded job together spans %d months -> %q", floor, months, no.Label))
	d.ChosenLabel, d.HasOption, d.OptionScore, d.Deterministic = no.Label, true, 1, true
	return d, true
}

// yearsThreshold answers a Yes/No years threshold.
func yearsThreshold(s *Step) (Draft, bool) {
	q := s.Q
	floor, strict, rest, ok := gates.YearsThreshold(q.Text)
	anyOf := false
	if !ok {
		if floor, strict, rest, ok = gates.YearsThresholdAnyOf(q.Text); !ok {
			return Draft{}, false
		}
		anyOf = true
	}
	yes, okY := ownYesNo(q.Options, true)
	no, okN := ownYesNo(q.Options, false)
	if !okY || !okN {
		return Draft{}, false
	}
	need := strconv.Itoa(floor) + " years"
	if strict {
		need = "more than " + need
	}
	answer := func(meets bool, why string) (Draft, bool) {
		o := no
		if meets {
			o = yes
		}
		d := derivedDraft(o.Label, "", startProfileRow, "years threshold ("+need+"): "+why+" -> "+quoteTerm(o.Label))
		d.ChosenLabel, d.HasOption, d.OptionScore, d.Deterministic = o.Label, true, 1, true
		return d, true
	}
	below := func(years float64) bool { return years < float64(floor) || strict && years <= float64(floor) }
	// A choice of fields ("Go or Python") is answered only by the bound every
	// field shares: professional experience below the floor in total is No.
	if anyOf {
		_, hi, entry, ok := totalYears(s.Entries)
		if ok && professionalYearsRE.MatchString(rest) && hi < math.Inf(1) && below(hi) {
			return answer(false, "the total of "+quoteTerm(entry)+" is below it, and every field the question offers is part of it")
		}
		return Draft{}, false
	}

	var content, field []string
	for _, t := range skillTokens(rest) {
		switch {
		case thresholdFrameWords[t]:
		case thresholdFieldWords[t]:
			field = append(field, t)
		default:
			content = append(content, t)
		}
	}
	if len(content) == 0 {
		lo, hi, entry, ok := totalYears(s.Entries)
		switch {
		case !ok:
			return Draft{}, false
		case hi < math.Inf(1) && below(hi):
			return answer(false, "the total of "+quoteTerm(entry)+" is below it")
		case len(field) == 0 && !below(lo):
			return answer(true, "the total of "+quoteTerm(entry)+" meets it")
		}
		s.Tracef("years threshold (%s): the total %q does not settle experience in %s", need, entry, strings.Join(field, " "))
		return Draft{}, false
	}

	// Narrower experience inside the field, or in work, is never more years
	// than the total (BitGo: "5+ years of backend software engineering").
	if len(field) > 0 || professionalYearsRE.MatchString(rest) {
		if _, hi, entry, ok := totalYears(s.Entries); ok && hi < math.Inf(1) && below(hi) {
			return answer(false, "the total of "+quoteTerm(entry)+" is below it, and the question narrows experience within it")
		}
	}
	asked, covered := setOf(content...), map[string]bool{}
	var met, short []string
	for _, sk := range s.Req.Facts.Skills {
		toks := skillTokens(sk.Name)
		if sk.Years == nil || *sk.Years < 0 || math.IsNaN(*sk.Years) || math.IsInf(*sk.Years, 0) || len(toks) == 0 || !allIn(toks, asked) || !shortSkillWritten(q.Text, sk.Name) {
			continue
		}
		for _, t := range toks {
			covered[t] = true
		}
		line := sk.Name + " " + strconv.FormatFloat(*sk.Years, 'f', -1, 64) + " years"
		if below(*sk.Years) {
			short = append(short, line)
		} else {
			met = append(met, line)
		}
	}
	switch {
	case len(short) > 0:
		return answer(false, "profile skills ["+strings.Join(short, ", ")+"]")
	case len(met) > 0 && len(field) == 0 && allIn(content, covered):
		return answer(true, "profile skills ["+strings.Join(met, ", ")+"]")
	case len(met) > 0:
		s.Tracef("years threshold (%s): profile skills [%s] meet it but the question narrows them", need, strings.Join(met, ", "))
		return Draft{}, false
	}
	// Years with a kind of the field are at least years with it: they meet the
	// floor or settle nothing (Bree: three years with LLMs meet "2+ years of
	// experience with machine learning").
	if years, name, ok := profileSkillYears(strings.Join(content, " "), s.Req.Facts.Skills); ok && len(field) == 0 && !below(years) {
		return answer(true, "profile skill "+quoteTerm(name)+" "+strconv.FormatFloat(years, 'f', -1, 64)+" years")
	}
	if o := s.ownMentions(q.Text, q.Text, AskedAbout(strings.Join(content, " ")), true); o.Absent() {
		return answer(false, "none of the user's information mentions ["+strings.Join(o.Terms, ", ")+"] (F-10)")
	}
	return Draft{}, false
}

// totalYears reads the total-years entry the user wrote: a number, or the
// range it stores ("3-5", "5+"), as its bounds.
func totalYears(entries []Entry) (lo, hi float64, answer string, ok bool) {
	for _, e := range entries {
		if e.ID != IDYearsFullTimeWork || e.Placeholder || e.AsksMe() || !e.UserWritten() || e.EffectiveState() != StateActive {
			continue
		}
		answer = strings.TrimSpace(e.Answer)
		if n, err := strconv.ParseFloat(answer, 64); err == nil && n >= 0 {
			return n, n, answer, true
		}
		if b, found := parseYearsBucket(answer); found {
			return math.Max(b.lo, 0), b.hi, answer, true
		}
	}
	return 0, 0, "", false
}
