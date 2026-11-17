package assist

import (
	"strings"
	"unicode"

	"applysmith/internal/lazyregexp"
)

// A composed answer may name only what the facts it was given name. The check
// is deliberately heuristic: it extracts the spans a reader would take as claims
// (proper nouns, acronyms, numbers with a unit) and asks whether the allowed
// facts contain each one.

type spanKind int

const (
	spanName spanKind = iota
	spanFigure
)

type span struct {
	Text string
	Kind spanKind
	// Words is the number of words of a name; a figure counts as one claim.
	Words int
}

var (
	properRunRE = lazyregexp.New(`[A-Z][A-Za-z0-9]*(?:['\x{2019}]s)?(?:[ ][A-Z][A-Za-z0-9]*(?:['\x{2019}]s)?){0,4}`)
	acronymRE   = lazyregexp.New(`\b[A-Z]{2,8}(?:-?[0-9]+)?\b`)
	phdRE       = lazyregexp.New(`\bPh\.?D\b|\bPhd\b`)
	// A figure is a number with a unit: a symbol, a magnitude letter or a unit
	// word. A bare number is not a claim a reader checks.
	figureRE = lazyregexp.New(`\d+(?:[.,]\d+)?\+?(?:\s?%|\s?percent\b|[xXKMBkmb]\b|\s+(?i:years?|yrs?|months?|weeks?|days?|hours?|minutes?|seconds?|users?|customers?|clients?|engineers?|people|employees?|teams?|projects?|requests?|transactions?|million|billion|thousand|ms|gb|tb|mb)\b)(?:/(?:day|sec|min|hr|hour|second|minute|year))?`)
)

// leadNoise are capitalized words that open a sentence or a span without naming
// anything: determiners, pronouns, prepositions, connectives, greetings and the
// verbs a first-person answer starts with.
var leadNoise = setOf(
	"a", "an", "the", "this", "that", "these", "those", "my", "our", "your", "his", "her", "their", "its", "i", "we", "you", "he", "she", "they", "it",
	"and", "but", "or", "so", "yet", "for", "nor", "because", "although", "though", "while", "when", "where", "which", "who", "whom", "whose", "what", "why", "how",
	"in", "on", "at", "to", "of", "by", "with", "from", "into", "onto", "over", "under", "after", "before", "during", "since", "until", "through", "across", "about", "around",
	"as", "if", "then", "than", "also", "still", "even", "just", "only", "now", "today", "recently", "currently", "previously", "early", "later", "first", "second", "third", "finally",
	"yes", "no", "yeah", "sure", "thanks", "thank", "hello", "hi", "dear", "regards", "sincerely", "best", "cheers",
	"there", "here", "some", "many", "most", "much", "more", "less", "few", "several", "each", "every", "all", "both", "either", "neither", "any", "such", "same", "other", "another",
	"outside", "inside", "instead", "beyond", "within", "without", "between", "among", "throughout", "alongside", "despite", "unlike", "like", "beyond",
	"built", "build", "led", "lead", "ran", "run", "wrote", "write", "made", "make", "worked", "work", "helped", "help", "owned", "own", "shipped", "ship", "designed", "design",
	"developed", "develop", "created", "create", "managed", "manage", "taught", "learned", "learn", "joined", "join", "started", "start", "moved", "grew", "drove", "improved",
	"being", "having", "doing", "using", "working", "building", "leading", "looking", "seeing", "wanting", "hoping",
	"am", "is", "are", "was", "were", "be", "been", "have", "has", "had", "do", "does", "did", "can", "could", "will", "would", "should", "may", "might", "must",
	"not", "never", "always", "often", "sometimes", "really", "very", "quite", "rather", "pretty",
	"my", "mine", "myself", "ours", "yours", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten",
	"overall", "ultimately", "honestly", "frankly", "personally", "specifically", "generally", "basically", "additionally", "moreover", "furthermore", "however", "therefore", "thus", "hence",
	"please", "looking", "excited", "eager", "proud", "happy", "glad",
)

var safeAcronyms = setOf(
	"I", "OK", "USA", "US", "UK", "EU", "AI", "ML", "LLM", "LLMS", "API", "APIS", "SDK", "URL", "JSON", "SQL", "CSS", "HTML", "UI", "UX", "CLI",
	"SAAS", "OS", "CI", "CD", "PR", "RAG", "CRM", "ERP", "HR", "IT", "QA", "OKR", "KPI", "B2B", "B2C", "REST", "HTTP", "HTTPS", "XML", "YAML", "PDF", "GPU", "CPU", "RAM",
	"ETL", "SRE", "DEVOPS", "MVP", "ROI", "SEO", "SLA", "SSO", "TLS", "SSH", "VPN", "DNS", "TCP", "UDP", "CEO", "CTO", "VP", "NLP", "OCR", "IDE", "GUI", "TDD", "FAQ",
)

var monthsAndDays = setOf("january", "february", "march", "april", "may", "june", "july", "august", "september", "october", "november", "december",
	"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday")

// sentenceInitial reports whether the byte offset starts a sentence of text.
func sentenceInitial(text string, off int) bool {
	i := off - 1
	for i >= 0 && (text[i] == ' ' || text[i] == '"' || text[i] == '\'' || text[i] == '(') {
		i--
	}
	return i < 0 || text[i] == '.' || text[i] == '!' || text[i] == '?' || text[i] == '\n' || text[i] == ':'
}

func stripPossessive(s string) string {
	s = strings.TrimSuffix(s, "'s")
	return strings.TrimSuffix(s, "\u2019s")
}

// extractSpans lists the claims of text in order of appearance.
func extractSpans(text string) []span {
	var out []span
	seen := map[string]bool{}
	add := func(s span) {
		k := strings.ToLower(s.Text)
		if s.Text != "" && !seen[k] {
			seen[k] = true
			out = append(out, s)
		}
	}
	for _, m := range properRunRE.FindAllStringIndex(text, -1) {
		// A capital glued to a digit or a letter is the middle of a token ("200M").
		if m[0] > 0 && isAlnum(rune(text[m[0]-1])) {
			continue
		}
		run := text[m[0]:m[1]]
		words := strings.Fields(run)
		// A credential is no part of a name ("MBA Program", "MS Excel", the
		// "Ph" of "Ph.D."); the acronym and the PhD passes below check it on
		// its own.
		isNoise := func(w string) bool {
			w = stripPossessive(w)
			_, cred := credentialKey(w)
			return leadNoise[strings.ToLower(w)] || monthsAndDays[strings.ToLower(w)] || (w == strings.ToUpper(w) && safeAcronyms[strings.ToUpper(w)]) || cred || w == "Ph"
		}
		for len(words) > 0 && isNoise(words[0]) {
			words = words[1:]
		}
		for len(words) > 0 && isNoise(words[len(words)-1]) {
			words = words[:len(words)-1]
		}
		if len(words) == 0 {
			continue
		}
		clean := make([]string, len(words))
		for i, w := range words {
			clean[i] = stripPossessive(w)
		}
		name := strings.Join(clean, " ")
		if len(clean) == 1 {
			low := strings.ToLower(clean[0])
			// A lone capital word that opens a sentence is just a capital.
			if sentenceInitial(text, m[0]) && len(words) == len(strings.Fields(run)) && commonWord(low) {
				continue
			}
			if monthsAndDays[low] || (len(clean[0]) >= 2 && safeAcronyms[strings.ToUpper(clean[0])] && clean[0] == strings.ToUpper(clean[0])) || clean[0] == "I" {
				continue
			}
			if strings.EqualFold(clean[0], "SaaS") {
				continue
			}
		}
		if monthsAndDays[strings.ToLower(name)] {
			continue
		}
		add(span{Text: name, Kind: spanName, Words: len(clean)})
	}
	for _, a := range acronymRE.FindAllString(text, -1) {
		if safeAcronyms[strings.ToUpper(a)] {
			continue
		}
		add(span{Text: a, Kind: spanName, Words: 1})
	}
	for _, d := range phdRE.FindAllString(text, -1) {
		add(span{Text: d, Kind: spanName, Words: 1})
	}
	for _, m := range figureRE.FindAllStringIndex(text, -1) {
		f := strings.TrimSpace(text[m[0]:m[1]])
		// A figure glued to letters ("B2B", "h2") is a name.
		if m[0] > 0 && unicode.IsLetter(rune(text[m[0]-1])) {
			continue
		}
		add(span{Text: f, Kind: spanFigure, Words: 1})
	}
	return out
}

// commonWord reports a word that is only capitalized because it opens a sentence.
// A lone capital word that opens a sentence is an ordinary word when it is in
// leadNoise, short, "Earlier", an -ly adverb of six letters or more or an -ing
// word of seven or more ("Applying"; "Spring" stays a name). Those openers were
// every stricter-grounding retry of 20 measured composed answers.
func commonWord(low string) bool {
	if leadNoise[low] || low == "earlier" {
		return true
	}
	if len(low) > 5 && strings.HasSuffix(low, "ly") || len(low) > 6 && strings.HasSuffix(low, "ing") {
		return true
	}
	return len(low) <= 3
}

var unitSynonyms = strings.NewReplacer(" yrs", " years", " yr", " year", " percent", "%", "+", "")

// normalizeFigure puts a figure and a corpus in one comparable form.
func normalizeFigure(s string) string {
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		if d, ok := spelledNumbers[strings.Trim(w, ".,;:")]; ok {
			words[i] = d
		}
	}
	return unitSynonyms.Replace(" " + strings.Join(words, " ") + " ")
}

// grounding is the allowed-fact corpus of one answer.
type grounding struct {
	facts []string
	lower []string
	norm  string
	whole string
	// own and ownLower are the applicant's own facts, the only ones a
	// credential claim rests on; creds are the recorded credentials among them,
	// lower-cased; masks are names (the company's) inside which a credential
	// acronym is no claim.
	own, ownLower, creds string
	masks                []string
}

func newGrounding(facts []string) *grounding {
	g := &grounding{}
	seen := map[string]bool{}
	for _, f := range facts {
		f = collapse(f)
		if f == "" || seen[strings.ToLower(f)] {
			continue
		}
		seen[strings.ToLower(f)] = true
		g.facts = append(g.facts, f)
		g.lower = append(g.lower, strings.ToLower(f))
	}
	g.whole = strings.Join(g.lower, "\n")
	g.norm = normalizeFigure(g.whole)
	g.own, g.ownLower = strings.Join(g.facts, "\n"), g.whole
	return g
}

// withOwn narrows the facts a credential claim may rest on to own, names the
// recorded credentials and the masks.
func (g *grounding) withOwn(own, creds, masks []string) *grounding {
	var kept []string
	for _, f := range own {
		if f = collapse(f); f != "" {
			kept = append(kept, f)
		}
	}
	g.own = strings.Join(kept, "\n")
	g.ownLower = strings.ToLower(g.own)
	g.creds = strings.ToLower(strings.Join(creds, "\n"))
	g.masks = masks
	return g
}

func wordBoundaryContains(hay, needle string) bool {
	for from := 0; ; {
		i := strings.Index(hay[from:], needle)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(needle)
		before := i == 0 || !isAlnum(rune(hay[i-1]))
		after := end == len(hay) || !isAlnum(rune(hay[end]))
		if before && after {
			return true
		}
		from = i + 1
	}
}

func isAlnum(r rune) bool { return r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)) }

// supported reports whether the facts contain the span, or the span contains a
// fact of its own (a longer name built on a supported one). A credential
// acronym is supported only by the applicant's own facts.
func (g *grounding) supported(s span) bool {
	low := strings.ToLower(s.Text)
	if key, ok := credentialKey(s.Text); ok && s.Kind == spanName {
		return g.holds(key)
	}
	if s.Kind == spanFigure {
		return strings.Contains(g.norm, strings.TrimRight(normalizeFigure(s.Text), " ")+" ") || strings.Contains(g.norm, normalizeFigure(s.Text)) || figureNumberInFacts(g, s)
	}
	if strings.Contains(g.whole, low) {
		return true
	}
	// A plural common noun opening a sentence ("Releases", "Payments") is held
	// by its singular in the facts.
	if s.Words == 1 && len(low) > 4 && strings.HasSuffix(low, "s") && wordBoundaryContains(g.whole, strings.TrimSuffix(low, "s")) {
		return true
	}
	for _, f := range g.lower {
		if len(f) >= 3 && len(f) <= len(low) && wordBoundaryContains(low, f) {
			return true
		}
	}
	return false
}

// figureNumberInFacts supports a bare-number figure ("18%") by the same number
// and unit in a fact, tolerating a "+" and a different spacing.
func figureNumberInFacts(g *grounding, s span) bool {
	n := normalizeFigure(s.Text)
	n = strings.Join(strings.Fields(n), " ")
	return n != "" && strings.Contains(strings.Join(strings.Fields(g.norm), " "), n)
}

// unsupportedSpans are the spans of text the facts do not contain, then the
// credential claims the applicant's own facts do not hold.
func (g *grounding) unsupportedSpans(text string) []span {
	var out []span
	seen := map[string]bool{}
	masked := g.masked(text)
	for _, s := range extractSpans(text) {
		if _, ok := credentialKey(s.Text); ok && s.Kind == spanName && !wordBoundaryContains(masked, s.Text) {
			continue
		}
		if !g.supported(s) {
			out = append(out, s)
			seen[strings.ToLower(s.Text)] = true
		}
	}
	for _, s := range g.unheldClaims(masked) {
		if !seen[strings.ToLower(s.Text)] {
			seen[strings.ToLower(s.Text)] = true
			out = append(out, s)
		}
	}
	return out
}

// blocking selects the spans that stop a draft. strict is never laxer than
// flex: both stop an unsupported name and an unsupported figure. Dangerous mode
// reports and blocks nothing.
func blocking(mode Mode, unsupported []span) []span {
	if mode == ModeDangerous {
		return nil
	}
	return unsupported
}

// stripSentencesWithSpans removes every sentence that contains a span as a whole
// word, case-insensitively, and joins the rest with single spaces.
func stripSentencesWithSpans(text string, spans []span) string {
	var kept []string
	for _, sent := range sentences(text) {
		low := strings.ToLower(sent)
		drop := false
		for _, s := range spans {
			if wordBoundaryContains(low, strings.ToLower(s.Text)) {
				drop = true
				break
			}
		}
		if !drop {
			kept = append(kept, sent)
		}
	}
	return joinSentences(kept)
}

// salvage removes the sentences of a draft that hold the blocking spans, but only
// when every span is a single word. The rest is kept only if it passes the check
// again, has at least 25 words and keeps at least 40 percent of the original.
func salvage(g *grounding, mode Mode, text string, block []span) (string, bool) {
	for _, s := range block {
		if s.Kind == spanName && s.Words > 1 {
			return "", false
		}
	}
	rest := stripSentencesWithSpans(text, block)
	n := wordCount(rest)
	if n < minSalvage || n*10 < wordCount(text)*4 {
		return "", false
	}
	if len(blocking(mode, g.unsupportedSpans(rest))) > 0 {
		return "", false
	}
	return rest, true
}
