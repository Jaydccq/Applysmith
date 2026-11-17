package match

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"applysmith/internal/lazyregexp"
)

// optScore is the score of one option against one answer, 0..1, with the name
// of the branch that decided it for the trace.
type optScore struct {
	Score float64
	Why   string
}

// declineDemoted sits below the review band on purpose: a decline option loses
// to every truthful option and, alone, abstains instead of declining for the
// user.
const declineDemoted = 0.4

// answerProfile is everything the ladder reads from an answer, computed once
// for all the options it is scored against.
type answerProfile struct {
	raw      string // contractions expanded
	norm     string
	toks     []string
	neg      bool
	aff, ng  map[string]bool
	stems    []string
	stemSet  map[string]bool
	decline  bool
	stance   stanceReading
	hasStanc bool
	days     int
	hasDays  bool
	original string
	axis     string // the protected-category axis the text names, or ""
	disabled bool   // the text affirms a disability
	pair     pairReadings
}

// pairReadings are what the pairwise rungs of scoreProfiles read from one side,
// a label or an answer, alone. Each is read on first use and kept: a control
// scores every label against every distinct answer, so a reading made per pair
// repeated the same regular-expression work for every partner.
type pairReadings struct {
	num      *numericSide
	country  *countryReading
	identity *identityReading
}

func (r *pairReadings) numeric(text string) *numericSide {
	if r.num == nil {
		r.num = readNumericSide(text)
	}
	return r.num
}

func (r *pairReadings) countryOf(text string) *countryReading {
	if r.country == nil {
		r.country = readCountry(text)
	}
	return r.country
}

func (r *pairReadings) identityOf(raw string) *identityReading {
	if r.identity == nil {
		r.identity = readIdentity(raw)
	}
	return r.identity
}

func newAnswerProfile(answer string) *answerProfile {
	p := &answerProfile{original: answer, raw: expandContractions(answer)}
	p.norm = normalizeLabel(p.raw)
	p.toks = tokenize(p.norm)
	p.neg = hasNegationToken(p.norm)
	p.aff, p.ng = negationScope(p.norm)
	p.stems = stemWords(p.norm)
	p.stemSet = tokenSet(p.stems)
	p.decline = isDeclineAnswer(p.raw)
	p.stance, p.hasStanc = readStance(p.raw)
	p.days, p.hasDays = parseDurationDays(answer)
	p.axis = strictAxisOf(p.norm)
	p.disabled = anyToken(p.aff, "disability", "disabilities", "disabled")
	return p
}

// labelProfile is everything the ladder reads from one option label.
type labelProfile struct {
	label       string
	raw         string
	norm        string
	question    string // the text of the question that offers the label
	toks        []string
	neg         bool
	aff, ng     map[string]bool
	stems       []string
	stemSet     map[string]bool
	placeholder bool
	withdrawal  bool
	decline     string
	axis        string // the protected-category axis the label names, or ""
	disabled    bool   // the label affirms a disability
	pair        pairReadings
	only        labelReadings
}

// labelReadings are what only some rungs read from a label: its stance, when
// the answer states one; the duration it names, when the answer names one; and
// whether it puts the answer off, for a Yes to a now-question. Each is read on
// first use and kept, so a label scored only against answers that need none of
// them is never read for them.
type labelReadings struct {
	stance   *labelStance
	days     *labelDays
	deferred *bool
}

type labelStance struct {
	reading stanceReading
	ok      bool
}

type labelDays struct {
	n  int
	ok bool
}

// stanceOf is the label's stance in the scope of its question.
func (p *labelProfile) stanceOf() (stanceReading, bool) {
	if p.only.stance == nil {
		r, ok := readOptionStance(p.raw, p.question)
		p.only.stance = &labelStance{r, ok}
	}
	return p.only.stance.reading, p.only.stance.ok
}

// daysOf is the duration the label names, in days.
func (p *labelProfile) daysOf() (int, bool) {
	if p.only.days == nil {
		n, ok := parseDurationDays(p.label)
		p.only.days = &labelDays{n, ok}
	}
	return p.only.days.n, p.only.days.ok
}

// deferredOf reports a put-off or conditional option offered to a now-question.
func (p *labelProfile) deferredOf() bool {
	if p.only.deferred == nil {
		d := p.question != "" && !futureQuestionRE.MatchString(p.question) && deferralRE.MatchString(pickGuidanceRE.ReplaceAllString(p.norm, " "))
		p.only.deferred = &d
	}
	return *p.only.deferred
}

func newLabelProfile(o Option, questionText string) *labelProfile {
	p := &labelProfile{label: o.Label, raw: expandContractions(o.Label), question: questionText}
	p.norm = normalizeLabel(p.raw)
	p.toks = tokenize(p.norm)
	p.neg = hasNegationToken(p.norm)
	p.aff, p.ng = negationScope(p.norm)
	p.stems = stemWords(p.norm)
	p.stemSet = tokenSet(p.stems)
	p.placeholder = isScorePlaceholder(o.Label)
	p.withdrawal = isWithdrawalLabel(p.norm)
	p.decline = declineIn(p.raw)
	p.axis = strictAxisOf(p.norm)
	p.disabled = anyToken(p.aff, "disability", "disabilities", "disabled")
	return p
}

// scoreOption scores one option against an answer by trying the ladder rules in
// order and returning at the first that applies. The question text is read only
// by the stance layer, so a bare Yes or No option can be understood in the scope
// of the question that offers it.
func scoreOption(o Option, answer, questionText string) optScore {
	return scoreProfiles(newLabelProfile(o, questionText), newAnswerProfile(answer))
}

func scoreProfiles(l *labelProfile, a *answerProfile) optScore {
	label, ans := l.norm, a.norm
	if label == "" || ans == "" {
		return optScore{0, "empty label or answer"}
	}
	if label == ans {
		return optScore{1, "exact-normalized-match"}
	}
	if dropApos(label) == dropApos(ans) {
		return optScore{1, "exact-apostrophe-folded-match"}
	}
	// A hyphen that splits one word is the same word ("Bi-Sexual", Klaviyo).
	if (strings.Contains(l.label, "-") || strings.Contains(a.original, "-")) && wordJoinRemover.Replace(label) == wordJoinRemover.Replace(ans) {
		return optScore{1, "exact-hyphen-folded-match"}
	}
	if isAbbreviation(label, ans) {
		return optScore{0.9, "abbreviation-prefix"}
	}
	if l.withdrawal {
		return optScore{0, "destructive-option guard: only an exact match may select this option"}
	}
	if l.placeholder {
		return optScore{0, "placeholder-option guard: only an exact match may select this option"}
	}

	// A label that is a veteran and also disabled asserts a second status the
	// answer did not state.
	if l.disabled && !a.disabled && anyToken(l.aff, "veteran", "veterans") {
		return optScore{0, "label asserts a disability the answer does not state"}
	}

	// Stance layer: on the veteran and disability sets an option is read by its
	// meaning, and a decline is demoted below a stated stance.
	if l.decline == "" {
		if v, ok := stanceVerdictOf(a, l); ok {
			return optScore{v.score, v.why}
		}
	} else if !a.decline && a.hasStanc {
		return optScore{declineDemoted, "decline option demoted: answer states a " + string(a.stance.concept) + " stance"}
	}

	if countriesEqual(l.pair.countryOf(l.label), a.pair.countryOf(a.original)) {
		return optScore{0.95, "country-equivalent-match"}
	}
	if isGenderEquivalent(label, ans) {
		return optScore{0.95, "gender-equivalent-match"}
	}
	if isListedOrientation(label, ans) {
		return optScore{0.95, "orientation-listed-match"}
	}
	if isRegularEmployment(label, ans) {
		return optScore{0.9, "employment-type-equivalent-match"}
	}
	if an := a.pair.numeric(a.original); an.digits {
		if s, why, ok := scoreNumericRange(l.pair.numeric(l.label), an); ok {
			return optScore{s, why}
		}
	}
	if a.hasDays {
		if days, ok := l.daysOf(); ok && abs(a.days-days) <= 2 {
			s := 0.9
			if a.days == days {
				s = 0.95
			}
			return optScore{s, fmt.Sprintf("duration match (%dd~%dd)", a.days, days)}
		}
	}
	if a.decline && l.decline != "" {
		return optScore{0.9, fmt.Sprintf("decline-synonym match (label contains %q)", l.decline)}
	}
	if ans == "yes" || ans == "no" {
		if s, ok := scoreYesNo(l, ans == "yes"); ok {
			return s
		}
	}

	if a.neg || l.neg {
		if tok := polarityConflictOf(a, l); tok != "" {
			return optScore{0, fmt.Sprintf("rejected: polarity conflict on %q", tok)}
		}
	}
	// Protected categories: a label that merely contains the answer names a
	// different group. Category axes match whole labels only; the stance axes
	// are read by meaning above, so only the contains-answer rung is withheld.
	li, ai := l.pair.identityOf(l.raw), a.pair.identityOf(a.raw)
	wholeIdentity := sameIdentityLabel(li, ai)
	if !wholeIdentity && identityCovers(li, ai) {
		return optScore{0.9, "the option joins the answer's group with a neighbouring one"}
	}
	if !wholeIdentity && (l.axis == axisRaceEthnicity || a.axis == axisRaceEthnicity) && hasAllTokens(l.toks, a.toks) && raceListed(l.raw, a.raw) {
		return optScore{0.9, "the option lists the answer's race among other groups (the owner's decision)"}
	}
	if (categoryAxes[l.axis] || categoryAxes[a.axis]) && !wholeIdentity {
		return optScore{0, "self-identification category: only an equal or equivalent whole label counts"}
	}
	containsAnswer := optRunStart(l.toks, a.toks) >= 0 || optRunStart(l.stems, a.stems) >= 0
	if containsAnswer && (l.axis != "" || a.axis != "") && !wholeIdentity {
		containsAnswer = false
	}
	if containsAnswer {
		if !a.neg && l.neg && negationPrecedes(l.toks, a.toks) {
			return optScore{0, "rejected: negation negates answer span (contains-answer)"}
		}
		ratio := float64(len(a.toks)) / float64(max(1, len(l.toks)))
		return optScore{0.7 + 0.25*ratio, fmt.Sprintf("contains-answer (label-tokens=%d)", len(l.toks))}
	}
	if optRunStart(a.toks, l.toks) >= 0 || optRunStart(a.stems, l.stems) >= 0 {
		if a.neg != l.neg {
			return optScore{0, "rejected: negation mismatch (option-in-answer)"}
		}
		return optScore{0.75, "option fully contained in answer"}
	}
	if a.neg != l.neg {
		return optScore{0, "rejected: negation mismatch (token-overlap)"}
	}
	if len(a.stemSet) > 0 && sameKeys(a.stemSet, l.stemSet) {
		return optScore{0.85, "stemmed-set-equality"}
	}
	// A form's typo of the whole answer ("West Virgina") outranks the shorter
	// option the answer contains ("Virginia", 0.75 above).
	if misspeltWhole(l.toks, a.toks) {
		return optScore{0.85, "misspelt whole answer"}
	}
	if ov := setOverlap(l.toks, a.toks); ov >= 0.5 {
		return optScore{min(0.5+ov/2, 0.84), fmt.Sprintf("token-overlap=%.2f", ov)}
	}
	return optScore{0, "no signal"}
}

// minSlipLetters is the shortest word a slip is read in: in a shorter one a
// slip turns one name into another ("Laos", "Lagos").
const minSlipLetters = 5

// misspeltWhole reports a label that is the answer's words in order with
// exactly one of them misspelt by a slip.
func misspeltWhole(label, answer []string) bool {
	if len(label) != len(answer) {
		return false
	}
	slipped := false
	for i := range label {
		if label[i] == answer[i] {
			continue
		}
		if slipped || !isSlip(label[i], answer[i]) {
			return false
		}
		slipped = true
	}
	return slipped
}

// isSlip reports two words of letters that differ by one letter dropped or
// added, or by two neighbouring letters swapped. A changed letter is never a
// slip: it turns one real name into another ("Ireland", "Iceland").
func isSlip(a, b string) bool {
	if !lettersOnly.MatchString(a) || !lettersOnly.MatchString(b) || min(len(a), len(b)) < minSlipLetters {
		return false
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	i := 0
	for i < len(a) && a[i] == b[i] {
		i++
	}
	switch len(b) - len(a) {
	case 0:
		return i+1 < len(a) && a[i] == b[i+1] && a[i+1] == b[i] && a[i+2:] == b[i+2:]
	case 1:
		return a[i:] == b[i+1:]
	}
	return false
}

func sameKeys(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

var aposRemover = strings.NewReplacer("'", "", "\u2019", "")

// dropApos removes straight and curly apostrophes.
func dropApos(s string) string { return aposRemover.Replace(s) }

// wordJoinRemover drops the spaces and hyphens between the parts of a word.
var wordJoinRemover = strings.NewReplacer(" ", "", "-", "")

// nonAbbreviations are never read as a cut-off word.
var nonAbbreviations = setOf("no", "not", "non", "yes", "ye")

var (
	lettersAtLeast2 = lazyregexp.New(`^[a-z]{2,}$`)
	lettersOnly     = lazyregexp.New(`^[a-z]+$`)
)

// isAbbreviation reports a label that is a cut-off form of the answer: one word
// that is a prefix shorter by two letters (one letter from four letters up), or
// several words whose last is such a prefix of at least three letters.
func isAbbreviation(short, long string) bool {
	words := strings.Split(short, " ")
	if len(words) > 1 {
		longWords := strings.Split(long, " ")
		last := words[len(words)-1]
		return len(longWords) == len(words) && len(last) >= 3 &&
			slices.Equal(words[:len(words)-1], longWords[:len(longWords)-1]) &&
			isAbbreviation(last, longWords[len(longWords)-1])
	}
	if !lettersAtLeast2.MatchString(short) || !lettersOnly.MatchString(long) || nonAbbreviations[short] {
		return false
	}
	cut := 2
	if len(short) >= 4 {
		cut = 1
	}
	return len(long) >= len(short)+cut && strings.HasPrefix(long, short)
}

// isGenderEquivalent reports two whole labels in the same gender group: each
// is one word of the group, or a list of only its words (Faire's "Man, male or
// masculine"). Only whole labels: "woman" contains "man" and must never select
// the opposite, and any other word ("Transgender man") names another group.
func isGenderEquivalent(label, ans string) bool {
	for _, g := range genderGroups {
		if genderWordsOnly(ans, g) && genderWordsOnly(label, g) {
			return true
		}
	}
	return false
}

var genderGroups = [][]string{{"male", "man", "masculine"}, {"female", "woman", "feminine"}}

// genderWordsOnly reports a normalized label whose words, "or" aside, are all
// words of the group. It runs for every option scored, so it reads the words
// in place.
func genderWordsOnly(label string, group []string) bool {
	n := 0
	for rest := label; rest != ""; {
		i := strings.IndexFunc(rest, unicode.IsLetter)
		if i < 0 {
			break
		}
		rest = rest[i:]
		j := strings.IndexFunc(rest, notLetter)
		if j < 0 {
			j = len(rest)
		}
		w := rest[:j]
		rest = rest[j:]
		if w == "or" {
			continue
		}
		if !slices.Contains(group, w) {
			return false
		}
		n++
	}
	return n > 0
}

func notLetter(r rune) bool { return !unicode.IsLetter(r) }

// orientationWords are the sexual orientations a self-identification list
// names.
var (
	orientationWords = []string{"heterosexual", "straight", "gay", "lesbian", "bisexual", "pansexual", "queer", "asexual", "demisexual", "homosexual"}
	// orientationListWords adds the "and" of "and/or"; genderWordsOnly skips "or".
	orientationListWords = append(slices.Clone(orientationWords), "and")
)

// isListedOrientation reports a label that lists only orientations, one of
// them the answer, itself one orientation (Gusto's "Bisexual, pansexual and/or
// queer" for "Bisexual"). Any other word ("questioning") names another group.
func isListedOrientation(label, ans string) bool {
	return slices.Contains(orientationWords, ans) && genderWordsOnly(label, orientationListWords) &&
		slices.Contains(strings.FieldsFunc(label, notLetter), ans)
}

// isRegularEmployment reports Workday's "Regular" worker type for a
// full-time or permanent answer (Trend Micro's Regular / Intern).
func isRegularEmployment(label, ans string) bool {
	return (label == "regular" || label == "regular employee") &&
		(ans == "full time" || ans == "full-time" || ans == "fulltime" || ans == "permanent")
}

var durationRE = lazyregexp.New(`(\d+)\s*(day|days|week|weeks|month|months|year|years)\b`)
var digitRangeRE = lazyregexp.New(`\d\s*[-\x{2013}]\s*\d`)

// numberWordRE is a duration's count spelled out (Fluidstack: "One week",
// "Two weeks"); openDurationRE an open one ("One month +", "6 months or more").
var (
	numberWordRE   = lazyregexp.New(`(?i)\b(` + countWords + `)(\s+(?:days?|weeks?|months?|years?))\b`)
	openDurationRE = lazyregexp.New(`(?i)\+|\bor\s+(?:more|longer)\b|\bplus\b|\bmore\s+than\b|\bover\b`)
	// durationUnitRE is the cheap test before a spelled-out count is read.
	durationUnitRE = lazyregexp.New(`(?i)day|week|month|year`)
)

// parseDurationDays reads a plain duration ("in 1 month", "30 days", "Two
// weeks") as days. A range ("0-1 years"), an open duration ("One month +") and
// a non-duration do not parse.
func parseDurationDays(text string) (int, bool) {
	if !strings.ContainsAny(text, "0123456789") {
		if !durationUnitRE.MatchString(text) {
			return 0, false
		}
		text = numberWordRE.ReplaceAllStringFunc(text, func(m string) string {
			sm := numberWordRE.FindStringSubmatch(m)
			return strconv.Itoa(numberWords[strings.ToLower(sm[1])]) + sm[2]
		})
		if !strings.ContainsAny(text, "0123456789") {
			return 0, false
		}
	}
	norm := normalizeLabel(text)
	if digitRangeRE.MatchString(norm) {
		return 0, false
	}
	m := durationRE.FindStringSubmatch(norm)
	if m == nil || openDurationRE.MatchString(text) {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	unit := map[string]int{"day": 1, "week": 7, "month": 30, "year": 365}[strings.TrimSuffix(m[2], "s")]
	return n * unit, true
}

// polarityConflictOf is polarityConflict on the scopes already computed.
func polarityConflictOf(a *answerProfile, l *labelProfile) string {
	if t := firstShared(a.ng, l.aff); t != "" {
		return t
	}
	return firstShared(a.aff, l.ng)
}

func firstShared(a, b map[string]bool) string {
	best := ""
	for t := range a {
		if b[t] && (best == "" || t < best) {
			best = t
		}
	}
	return best
}

// negationPrecedes reports a negation token directly before the first run of
// needle in hay: the negation negates the matched span, not a later qualifier.
func negationPrecedes(hay, needle []string) bool {
	i := optRunStart(hay, needle)
	return i > 0 && negationTokens[hay[i-1]]
}

func stemWords(s string) []string {
	toks := tokenize(dropApos(s))
	for i, t := range toks {
		toks[i] = stem(t)
	}
	return toks
}

// setOverlap is the Jaccard overlap of two token sets.
func setOverlap(a, b []string) float64 {
	sa, sb := tokenSet(a), tokenSet(b)
	if len(sa) == 0 || len(sb) == 0 {
		return 0
	}
	inter := 0
	for t := range sa {
		if sb[t] {
			inter++
		}
	}
	return float64(inter) / float64(len(sa)+len(sb)-inter)
}

// strictAxes are the protected-category axes the score ladder guards: a label
// that merely contains the answer ("Transgender male" for "Male", "American
// Indian or Alaska Native" for "Indian") names another group.
var strictAxes = map[string]bool{
	axisDisability: true, axisNeurodivergence: true, axisVeteran: true, axisRaceEthnicity: true,
	axisGender: true, axisGenderIdentity: true, axisOrientation: true,
}

func strictAxisOf(norm string) string {
	if axis := selfIDAxisOfNorm(norm); strictAxes[axis] {
		return axis
	}
	return ""
}

var identityQualifierRE = lazyregexp.New(`(?i)\(\s*(?:(?:not|non|including|inclusive of|such as|for example|a person)\b|e\.\s?g\.)[^)]*\)`)

// sameIdentityLabel reports two labels that differ at most by a negated
// parenthetical ("Asian (Not Hispanic or Latino)" for "Asian"), one that
// lists members ("East Asian (including Chinese, Japanese, ...)") or one that
// gives the EEO-1 definition ("(A person having origins in ...)", Instacart).
// Such a qualifier names what the group excludes, holds or is, so it is not
// part of the match; any other qualifier ("Male (transgender)") names a
// different group.
func sameIdentityLabel(a, b *identityReading) bool {
	return a.key == b.key
}

// identityKey is an identity label without its qualifiers and without a list
// of the regions its EEO-1 definition names ("Asian (Far East, South East
// Asia, Indian)", Trend Micro's Workday): the regions describe the category,
// while a subgroup ("Asian (Chinese)") names another one and is kept.
//
// A leading "Group:" whose every word the rest already names is dropped too
// (DoorDash's "Asian: East Asian" is "East Asian"); one that names more
// ("Asian: Chinese") is kept.
func identityKey(label string) string {
	if i := strings.Index(label, ":"); i > 0 {
		head, rest := strings.Fields(normalizeLabel(label[:i])), strings.Fields(normalizeLabel(label[i+1:]))
		if len(head) > 0 && len(rest) > 0 && !slices.ContainsFunc(head, func(w string) bool { return !slices.Contains(rest, w) }) {
			label = label[i+1:]
		}
	}
	l := identityQualifierRE.ReplaceAllString(label, " ")
	l = parenRE.ReplaceAllStringFunc(l, func(p string) string {
		if memberList(p) {
			return " "
		}
		for _, item := range regionSplitRE.Split(strings.Trim(p, "() "), -1) {
			if !eeoRegions[strings.ToLower(strings.TrimSpace(item))] {
				return p
			}
		}
		return " "
	})
	return normalizeLabel(l)
}

var (
	memberSplitRE = lazyregexp.New(`\s*(?:[,/;]|\bor\b|\band\b)\s*`)
	memberNameRE  = lazyregexp.New(`^\p{L}[\p{L}'-]*(?:\s+\p{L}[\p{L}'-]*){0,2}$`)
)

// memberList reports a bracket that lists two or more of a group's members
// by name with no "including" ("East Asian (Chinese / Hong-Kong / Japanese
// ...)", Smartsheet): like the including list, it describes the group. One
// name in brackets ("Asian (Chinese)") is a subgroup and is kept.
func memberList(p string) bool {
	items := memberSplitRE.Split(strings.Trim(p, "() "), -1)
	if len(items) < 2 {
		return false
	}
	for _, it := range items {
		if !memberNameRE.MatchString(strings.TrimSpace(it)) {
			return false
		}
	}
	return true
}

// eeoRegions are the places the EEO-1 race definitions name.
var eeoRegions = setOf("far east", "south east asia", "southeast asia", "indian", "indian subcontinent",
	"europe", "middle east", "north africa", "africa", "hawaii", "guam", "samoa", "pacific islands",
	"north america", "south america", "central america", "caribbean")

var regionSplitRE = lazyregexp.New(`\s*(?:,|\bor\b|\band\b)\s*`)

// identityJoinRE reads an identity label that joins two neighbouring groups
// under one head word ("east or southeast asian").
var identityJoinRE = lazyregexp.New(`^(\S+) or (\S+) (\S+)$`)

// identityCovers reports an option that joins two groups under one head word
// ("East or Southeast Asian (e.g. Japanese)") and an answer that names one of
// them ("East Asian"). The option holds the answer's group; an answer naming
// both never takes an option naming one.
func identityCovers(option, answer *identityReading) bool {
	m := option.join
	return m != nil && (answer.plain == m[1]+" "+m[3] || answer.plain == m[2]+" "+m[3])
}

// identityReading is what the identity rungs read from one label or answer:
// its key (sameIdentityLabel), the text without qualifiers, and the two groups
// that text joins under one head word, or nil (identityCovers).
type identityReading struct {
	key   string
	plain string
	join  []string
}

func readIdentity(s string) *identityReading {
	plain := normalizeLabel(identityQualifierRE.ReplaceAllString(s, " "))
	return &identityReading{key: identityKey(s), plain: plain, join: identityJoinRE.FindStringSubmatch(plain)}
}

// hasAllTokens reports a label holding every answer token: the cheap test
// before raceListed reads the groups.
func hasAllTokens(label, answer []string) bool {
	for _, t := range answer {
		if !slices.Contains(label, t) {
			return false
		}
	}
	return len(answer) > 0
}

// raceGroupSplitRE splits a race label into the groups it lists.
var raceGroupSplitRE = lazyregexp.New(`\s*(?:/|,|\bor\b|\band\b)\s*`)

// raceListed reports a race option that lists the answer's groups among
// others ("Asian or Pacific Islander", "Black or African American"): the
// owner's decision (docs/architecture/apply.md, Postscript) lets a recorded
// race take such a label. Each
// group counts whole, so "Indian" never takes "American Indian or Alaska
// Native", nor "Asian" "East Asian".
func raceListed(option, answer string) bool {
	groups := func(label string) []string {
		var out []string
		for _, g := range raceGroupSplitRE.Split(identityKey(label), -1) {
			if g = strings.TrimSpace(g); g != "" {
				out = append(out, g)
			}
		}
		return out
	}
	opt, ans := groups(option), groups(answer)
	if len(opt) < 2 || len(ans) == 0 || len(ans) >= len(opt) {
		return false
	}
	for _, g := range ans {
		if !slices.Contains(opt, g) {
			return false
		}
	}
	return true
}

// countryEquivalent reports an option and an answer that name one country, one
// of them by its code or common alias (Stripe's "US" for "United States"). A
// bare Yes or No is never read as a code: NO is Norway.
func countryEquivalent(label, answer string) bool {
	return countriesEqual(readCountry(label), readCountry(answer))
}

// countryReading is the country one text names, read for countryEquivalent.
type countryReading struct {
	code string
	ok   bool
}

func readCountry(s string) *countryReading {
	if bareYesNoRE.MatchString(strings.TrimSpace(s)) {
		return &countryReading{}
	}
	code, ok := CountryCode(s)
	return &countryReading{code, ok}
}

// countriesEqual is countryEquivalent over the readings of the two texts.
func countriesEqual(l, a *countryReading) bool {
	return l.ok && a.ok && l.code == a.code
}
