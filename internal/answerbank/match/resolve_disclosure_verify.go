package match

import (
	"math"
	"strconv"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Human-verification questions that carry their own answer (step 4,
// HumanVerification). Nothing is composed and no model is called: a typing gate
// behind a verification phrase, arithmetic anti-spam, a closed table of
// common-knowledge gates and, behind a verification phrase, an ordinal or
// letter pick on an option control, the one option saying the applicant is
// a person on a question asking whether they are a machine, and a number to
// spell out in words. The answer has
// source form-text. No gate applies to a sensitive, disclosure,
// acknowledgement or consent text.

var (
	verifyPhraseRE = lazyregexp.New(`(?i)\bif\s+you\s+(?:are|happen\s+to\s+be)\s+(?:an?\s+)?(?:\w+\s+)?human\b|\bapplying\s+as\s+(?:an?\s+)?human\b|\bto\s+(?:verify|confirm|prove|show|ensure)\s+(?:that\s+)?you(?:'re|\s+are)\s+(?:not\s+)?(?:an?\s+)?(?:human|bot|robot)\b|\bhuman\s+verification\b|\bhumans?\s+only\b|\b(?:filter|catch|block|prevent|stop|screen)\s+(?:out\s+)?(?:automated|bots?)\b|\banti-?spam\b|\bnot\s+(?:a\s+)?(?:bot|robot)\b|\bspam\s+(?:check|filter|prevention)\b|\bcaptcha\b|\bsecurity\s+(?:question|check)\b|\bverification\s+question\b`)

	// gateBlockedRE keeps the gates off sensitive, disclosure, acknowledgement
	// and consent wording.
	gateBlockedRE = lazyregexp.New(`(?i)\b(?:consent|agree|acknowledge|certify|authori[sz]e|attest|disabilit\w*|veteran|gender|race|racial|ethnic\w*|religio\w*|convict\w*|felony|criminal|misdemean\w*)\b`)

	typeDirectiveRE = lazyregexp.New(`(?i)\b(?:type|enter|write|respond\s+with|answer\s+with|fill\s+in)\b`)
	quotedLiteralRE = lazyregexp.New("[\"“'`‘]([^\"”'`’]{1,40})[\"”'`’]")
	typeWordRE      = lazyregexp.New(`(?i)\btype\s+the\s+word\s+([A-Za-z0-9]+)`)
	allCapsRunRE    = lazyregexp.New(`^\s*(?:the\s+)?((?:[A-Z][A-Z0-9'-]*)(?:\s+[A-Z][A-Z0-9'-]*)*)\b`)

	notArithmeticRE = lazyregexp.New(`(?i)\b(?:salary|compensation|range|rate|pay|hours?|years?|dates?|age)\b`)
	arithGateRE     = lazyregexp.New(`(?i)\b(?:anti-?spam|captcha|human|verify|verification|robot|bot|security)\b|\b(?:what\s+is|how\s+much\s+is|solve|calculate|compute)\b|=\s*\?`)
)

const numberToken = `\d{1,4}|(?:twenty|thirty|forty|fifty|sixty|seventy|eighty|ninety)(?:[\s-](?:one|two|three|four|five|six|seven|eight|nine))?|nineteen|eighteen|seventeen|sixteen|fifteen|fourteen|thirteen|twelve|eleven|ten|nine|eight|seven|six|five|four|three|two|one|zero|hundred`

var (
	infixRE = lazyregexp.New(`(?i)\b(` + numberToken + `)(\s*[+*\x{00D7}\x{00F7}/]\s*|\s+[-\x{2013}]\s+|\s+(?:plus|minus|times|divided\s+by|multiplied\s+by)\s+|\s*x\s*)(` + numberToken + `)\b`)

	phrasalREs = []struct {
		re *lazyregexp.Regexp
		op string
	}{
		{lazyregexp.New(`(?i)\b(?:sum|total)\s+of\s+(` + numberToken + `)\s+(?:and|\+|plus)\s+(` + numberToken + `)\b`), "+"},
		{lazyregexp.New(`(?i)\bproduct\s+of\s+(` + numberToken + `)\s+(?:and|x|\*|times)\s+(` + numberToken + `)\b`), "*"},
		{lazyregexp.New(`(?i)\bdifference\s+(?:of|between)\s+(` + numberToken + `)\s+and\s+(` + numberToken + `)\b`), "diff"},
		{lazyregexp.New(`(?i)\badd\s+(` + numberToken + `)\s+(?:and|to)\s+(` + numberToken + `)\b`), "+"},
		{lazyregexp.New(`(?i)\bmultiply\s+(` + numberToken + `)\s+(?:by|and)\s+(` + numberToken + `)\b`), "*"},
		{lazyregexp.New(`(?i)\bdivide\s+(` + numberToken + `)\s+by\s+(` + numberToken + `)\b`), "/"},
		{lazyregexp.New(`(?i)\bsubtract\s+(` + numberToken + `)\s+from\s+(` + numberToken + `)\b`), "from"},
	}
	comparisonRE = lazyregexp.New(`(?i)\bwhich\s+(?:number\s+)?is\s+(bigger|larger|greater|smaller|less|lower)\b[^?]*?\b(` + numberToken + `)\s+or\s+(` + numberToken + `)\b`)

	// numberWords is the value of each spelled number, read off the one list
	// of them (smallNumberWords, tensWords).
	numberWords = func() map[string]int {
		out := map[string]int{"hundred": 100}
		for n, w := range smallNumberWords {
			out[w] = n
		}
		for t, w := range tensWords {
			if w != "" {
				out[w] = t * 10
			}
		}
		return out
	}()
	// countWords is the alternation of the spelled counts a duration opens
	// with, one to twelve, for every pattern that reads one.
	countWords = strings.Join(smallNumberWords[1:13], "|")
)

// parseNumber reads a digit run of at most four digits or a spelled number up to
// a hundred.
func parseNumber(tok string) (int, bool) {
	tok = strings.ToLower(strings.TrimSpace(tok))
	if n, err := strconv.Atoi(tok); err == nil {
		return n, true
	}
	tens, units, split := strings.Cut(strings.ReplaceAll(tok, "-", " "), " ")
	t, ok := numberWords[tens]
	if !ok {
		return 0, false
	}
	if !split {
		return t, true
	}
	u, ok := numberWords[strings.TrimSpace(units)]
	if !ok || u > 9 || t < 20 {
		return 0, false
	}
	return t + u, true
}

func apply(op string, a, b int) (int, bool) {
	switch op {
	case "+":
		return a + b, true
	case "-":
		if a < b {
			return 0, false
		}
		return a - b, true
	case "*":
		return a * b, true
	case "/":
		if b == 0 || a%b != 0 {
			return 0, false
		}
		return a / b, true
	}
	return 0, false
}

func infixOp(raw string) string {
	r := strings.ToLower(strings.TrimSpace(raw))
	switch r {
	case "+", "plus":
		return "+"
	case "-", "–", "minus":
		return "-"
	case "*", "×", "x", "times", "multiplied by":
		return "*"
	}
	return "/"
}

// arithmeticAnswer solves an anti-spam sum: an infix expression behind a
// verification, interrogative or "= ?" gate, a phrasal form, or a comparison.
// The result must be a non-negative integer.
func arithmeticAnswer(text string) (int, bool) {
	for _, p := range phrasalREs {
		m := p.re.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		a, ok1 := parseNumber(m[1])
		b, ok2 := parseNumber(m[2])
		if !ok1 || !ok2 {
			continue
		}
		switch p.op {
		case "diff":
			return int(math.Abs(float64(a - b))), true
		case "from":
			return apply("-", b, a)
		}
		return apply(p.op, a, b)
	}
	if m := comparisonRE.FindStringSubmatch(text); m != nil {
		a, ok1 := parseNumber(m[2])
		b, ok2 := parseNumber(m[3])
		if ok1 && ok2 {
			if w := strings.ToLower(m[1]); w == "bigger" || w == "larger" || w == "greater" {
				return max(a, b), true
			}
			return min(a, b), true
		}
	}
	if notArithmeticRE.MatchString(text) || !arithGateRE.MatchString(text) {
		return 0, false
	}
	m := infixRE.FindStringSubmatch(text)
	if m == nil {
		return 0, false
	}
	a, ok1 := parseNumber(m[1])
	b, ok2 := parseNumber(m[3])
	if !ok1 || !ok2 {
		return 0, false
	}
	return apply(infixOp(m[2]), a, b)
}

type triviaRow struct {
	re     *lazyregexp.Regexp
	answer string
}

func trivia(pattern, answer string) triviaRow {
	return triviaRow{lazyregexp.New(`(?i)` + pattern), answer}
}

// triviaTable is the closed table of common-knowledge gates; anything outside it
// is never guessed.
var triviaTable = []triviaRow{
	trivia(`\bhow\s+many\s+days\s+(?:are\s+)?(?:there\s+)?in\s+(?:a|one)\s+week\b`, "7"),
	trivia(`\bhow\s+many\s+months\s+(?:are\s+)?(?:there\s+)?in\s+(?:a|one)\s+year\b`, "12"),
	trivia(`\bhow\s+many\s+hours\s+(?:are\s+)?(?:there\s+)?in\s+(?:a|one)\s+day\b`, "24"),
	trivia(`\bhow\s+many\s+minutes\s+(?:are\s+)?(?:there\s+)?in\s+(?:an|one)\s+hour\b`, "60"),
	trivia(`\bhow\s+many\s+seconds\s+(?:are\s+)?(?:there\s+)?in\s+(?:a|one)\s+minute\b`, "60"),
	trivia(`\bhow\s+many\s+days\s+(?:are\s+)?(?:there\s+)?in\s+(?:a|one)\s+(?:non-?leap\s+)?year\b`, "365"),
	trivia(`\bhow\s+many\s+letters\s+(?:are\s+)?(?:there\s+)?in\s+the\s+(?:english\s+)?alphabet\b`, "26"),
	trivia(`\bhow\s+many\s+legs\s+(?:does|do|has|have)\s+(?:a|an|the)?\s*spiders?\b`, "8"),
	trivia(`\bhow\s+many\s+legs\s+(?:does|do|has|have)\s+(?:a|an|the)?\s*(?:cats?|dogs?|horses?|cows?)\b`, "4"),
	trivia(`\bhow\s+many\s+legs\s+(?:does|do|has|have)\s+(?:a|an|the)?\s*(?:humans?|chickens?|birds?)\b`, "2"),
	trivia(`\bhow\s+many\s+wheels\s+(?:does|do|has|have)\s+(?:a|an|the)?\s*bicycles?\b`, "2"),
	trivia(`\bhow\s+many\s+wheels\s+(?:does|do|has|have)\s+(?:a|an|the)?\s*(?:car|automobile)s?\b`, "4"),
	trivia(`\bhow\s+many\s+sides\s+(?:does|do|has|have)\s+(?:a|an|the)?\s*triangles?\b`, "3"),
	trivia(`\bhow\s+many\s+sides\s+(?:does|do|has|have)\s+(?:a|an|the)?\s*squares?\b`, "4"),
	trivia(`\bwhat\s+colou?r\s+is\s+(?:the\s+)?sky\b`, "Blue"),
	trivia(`\bwhat\s+colou?r\s+is\s+(?:the\s+)?grass\b`, "Green"),
	trivia(`\bwhat\s+colou?r\s+is\s+(?:the\s+)?snow\b`, "White"),
	trivia(`\bwhat\s+colou?r\s+is\s+(?:a\s+|the\s+)?(?:ripe\s+)?banana\b`, "Yellow"),
	trivia(`\bwhat\s+colou?r\s+is\s+(?:human\s+)?blood\b`, "Red"),
	trivia(`\bwhat\s+is\s+the\s+first\s+letter\s+of\s+the\s+(?:english\s+)?alphabet\b`, "A"),
	trivia(`\bwhat\s+is\s+the\s+last\s+letter\s+of\s+the\s+(?:english\s+)?alphabet\b`, "Z"),
	// The applicant is a person (Prior Labs: "Are you an LLM?"); the whole
	// question only, so "Are you an AI engineer?" is no gate.
	trivia(`^\W*are\s+you\s+(?:an?\s+)?(?:llm|(?:large\s+)?language\s+model|ai|a\.i\.|artificial\s+intelligence|bot|robot|chatbot|ai\s+(?:agent|assistant|model))\W*$`, "No"),
	trivia(`^\W*are\s+you\s+(?:a\s+)?(?:human(?:\s+being)?|real\s+person)\W*$`, "Yes"),
}

var (
	machineAskRE  = lazyregexp.New(`(?i)^\W*are\s+you\s+(?:an?\s+)?(?:llm|(?:large\s+)?language\s+model|ai|a\.i\.|artificial\s+intelligence|bot|robot|chatbot)(?:\s+(?:agent|assistant|model|tool))?\s*(?:[,/?]|\bor\b|$)`)
	humanOptionRE = lazyregexp.New(`(?i)^\W*(?:(?:i\s+am|i['\x{2019}]m)\s+)?(?:an?\s+)?(?:human(?:\s+being)?|real\s+person|person)\W*$`)
)

// humanOption is the one option saying the applicant is a person on a
// question that opens by asking whether they are a machine (Found Energy:
// "Are you an LLM, AI agent, or other associated agentic tool? ... Select all
// that apply", among "I am a web based AI agent" ... "I am a human").
func humanOption(q Question) (Option, bool) {
	if !machineAskRE.MatchString(q.Text) {
		return Option{}, false
	}
	var hits []Option
	for _, o := range realOptions(q.Options) {
		if humanOptionRE.MatchString(o.Label) {
			hits = append(hits, o)
		}
	}
	if len(hits) != 1 {
		return Option{}, false
	}
	return hits[0], true
}

func triviaAnswer(text string) (string, bool) {
	for _, t := range triviaTable {
		if t.re.MatchString(text) {
			return t.answer, true
		}
	}
	return "", false
}

// typeLiteral extracts the literal a typing gate dictates: a quoted literal after
// the directive, "type the word X", or the contiguous all-capitals tokens after
// the directive. Anything else is ambiguous and is never guessed.
func typeLiteral(text string) (string, bool) {
	loc := typeDirectiveRE.FindStringIndex(text)
	if loc == nil {
		return "", false
	}
	after := text[loc[1]:]
	if m := quotedLiteralRE.FindStringSubmatch(after); m != nil {
		if lit := strings.TrimSpace(m[1]); lit != "" {
			return lit, true
		}
	}
	if m := typeWordRE.FindStringSubmatch(text); m != nil {
		return m[1], true
	}
	if m := allCapsRunRE.FindStringSubmatch(after); m != nil && len(m[1]) >= 2 {
		return m[1], true
	}
	return "", false
}

// spellNumberRE is a number to spell out in words, the whole question (Mux:
// "Spell the number '12'").
var spellNumberRE = lazyregexp.New(`(?i)^\W*(?:please\s+)?(?:spell|write)(?:\s+out)?\s+the\s+number\s+["'\x{2018}\x{201C}]?(\d{1,3})["'\x{2019}\x{201D}]?(?:\s+(?:out\s+)?in\s+(?:words|letters))?\W*$`)

var (
	smallNumberWords = []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten",
		"eleven", "twelve", "thirteen", "fourteen", "fifteen", "sixteen", "seventeen", "eighteen", "nineteen"}
	tensWords = []string{"", "", "twenty", "thirty", "forty", "fifty", "sixty", "seventy", "eighty", "ninety"}
)

// numberInWords spells 0 to 999 in English words: "forty-seven", "three
// hundred five".
func numberInWords(n int) string {
	switch {
	case n < 20:
		return smallNumberWords[n]
	case n < 100:
		if n%10 == 0 {
			return tensWords[n/10]
		}
		return tensWords[n/10] + "-" + smallNumberWords[n%10]
	case n%100 == 0:
		return smallNumberWords[n/100] + " hundred"
	}
	return smallNumberWords[n/100] + " hundred " + numberInWords(n%100)
}

// spelledNumber is the answer to a number to spell out.
func spelledNumber(text string) (string, bool) {
	m := spellNumberRE.FindStringSubmatch(text)
	if m == nil {
		return "", false
	}
	n, _ := strconv.Atoi(m[1])
	return numberInWords(n), true
}

var (
	ordinalWords = map[string]int{"first": 1, "second": 2, "third": 3, "fourth": 4, "fifth": 5, "sixth": 6, "seventh": 7, "eighth": 8, "ninth": 9, "tenth": 10}
	pickVerb     = `\b(?:select|choose|pick|click|check|tick)\s+`
	ordinalPick  = lazyregexp.New(`(?i)` + pickVerb + `(?:the\s+)?(first|second|third|fourth|fifth|sixth|seventh|eighth|ninth|tenth|\d{1,2}(?:st|nd|rd|th))\s+(?:option|choice|answer|one)\b`)
	labelPick    = lazyregexp.New(`(?i)` + pickVerb + `(?:the\s+)?(?:option|choice|answer)\s+["'\x{201c}]?([A-Za-z]|\d{1,2})(?:["'\x{201d}]|[^A-Za-z0-9]|$)`)
)

// pickedOption resolves an ordinal or letter pick among the real options; an
// ordinal beats a label, and a target that is not among the options is never
// guessed.
func pickedOption(text string, opts []Option) (Option, bool) {
	real := realOptions(opts)
	if m := ordinalPick.FindStringSubmatch(text); m != nil {
		n, ok := ordinalWords[strings.ToLower(m[1])]
		if !ok {
			n, _ = strconv.Atoi(strings.TrimRight(strings.ToLower(m[1]), "stndrh"))
		}
		if n >= 1 && n <= len(real) {
			return real[n-1], true
		}
		return Option{}, false
	}
	m := labelPick.FindStringSubmatch(text)
	if m == nil {
		return Option{}, false
	}
	want := strings.ToLower(m[1])
	for _, o := range real {
		l := normalizeLabel(o.Label)
		if l == want || l == "option "+want || strings.HasPrefix(l, want+" ") && len(l) > 2 && (strings.HasPrefix(o.Label, strings.ToUpper(want)+")") || strings.HasPrefix(o.Label, strings.ToUpper(want)+".")) {
			return o, true
		}
	}
	idx := -1
	if n, err := strconv.Atoi(want); err == nil {
		idx = n - 1
	} else if len(want) == 1 {
		idx = int(want[0] - 'a')
	}
	if idx >= 0 && idx < len(real) {
		return real[idx], true
	}
	return Option{}, false
}

// optionEqualing is the one option that reads as the answer: the same label, or
// the same whole number.
func optionEqualing(opts []Option, answer string) (Option, bool) {
	want := normalizeLabel(answer)
	n, wantNum := strconv.Atoi(want)
	var found []Option
	for _, o := range realOptions(opts) {
		l := normalizeLabel(o.Label)
		if l == want {
			found = append(found, o)
			continue
		}
		if v, err := strconv.Atoi(l); wantNum == nil && err == nil && v == n {
			found = append(found, o)
		}
	}
	if len(found) != 1 {
		return Option{}, false
	}
	return found[0], true
}

func verifyDraft(answer, line string, opt *Option) Draft {
	d := Draft{Answer: answer, Source: SourceFormText, Deterministic: true, Trace: []string{line}}
	if opt != nil {
		d.Answer, d.ChosenLabel, d.HasOption, d.OptionScore = opt.Value, opt.Label, true, 1
	}
	return d
}

// stepArithmetic solves the question's sum. The sanitizer drops a spaced hyphen
// as page chrome, so the question as the caller sent it is tried as well.
func stepArithmetic(s *Step) (int, bool) {
	if n, ok := arithmeticAnswer(s.Q.Text); ok {
		return n, true
	}
	return arithmeticAnswer(s.Raw.Text)
}

// humanVerification is the HumanVerification slot.
func humanVerification(s *Step) (Draft, bool) {
	q := s.Q
	if gateBlockedRE.MatchString(q.Text) {
		return Draft{}, false
	}
	if o, ok := humanOption(q); ok {
		d := verifyDraft("", "the applicant is a person: option "+quoteTerm(o.Label), &o)
		if q.Kind == KindCheckbox {
			d.ChosenLabels = []string{o.Label}
		}
		return d, true
	}
	if q.Kind == KindCheckbox {
		return Draft{}, false
	}
	phrase := verifyPhraseRE.MatchString(q.Text)
	if !hasRenderedOptions(q.Options) {
		if q.Kind != KindText {
			return Draft{}, false
		}
		if phrase && typeDirectiveRE.MatchString(q.Text) {
			lit, ok := typeLiteral(q.Text)
			if !ok {
				return abstainDraft(ReasonNoEntry, "", "verification typing gate: no unambiguous literal to type"), true
			}
			return verifyDraft(lit, "verification typing gate: the form dictates "+quoteTerm(lit), nil), true
		}
		if n, ok := stepArithmetic(s); ok {
			return verifyDraft(strconv.Itoa(n), "arithmetic anti-spam: "+strconv.Itoa(n), nil), true
		}
		if a, ok := triviaAnswer(q.Text); ok {
			return verifyDraft(a, "common-knowledge gate: "+quoteTerm(a), nil), true
		}
		if a, ok := spelledNumber(q.Text); ok {
			return verifyDraft(a, "number to spell: "+quoteTerm(a), nil), true
		}
		return Draft{}, false
	}
	if n, ok := stepArithmetic(s); ok {
		if o, found := optionEqualing(q.Options, strconv.Itoa(n)); found {
			return verifyDraft("", "arithmetic anti-spam: option "+quoteTerm(o.Label), &o), true
		}
		return Draft{}, false
	}
	if a, ok := triviaAnswer(q.Text); ok {
		if o, found := optionEqualing(q.Options, a); found {
			return verifyDraft("", "common-knowledge gate: option "+quoteTerm(o.Label), &o), true
		}
		return Draft{}, false
	}
	if phrase {
		if o, ok := pickedOption(q.Text, q.Options); ok {
			return verifyDraft("", "verification pick: option "+quoteTerm(o.Label), &o), true
		}
	}
	return Draft{}, false
}
