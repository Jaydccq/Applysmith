package match

import (
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"applysmith/internal/lazyregexp"
)

// Sanitize cleans a question text before any classification, input-type
// inference, scope comparison or prompt. It is pure, returns clean text
// byte-identical, and applies its rules in a fixed order: later rules rely on
// earlier ones (caption pairs go before the duplicate collapse, control
// furniture before the glued-option strip, opaque ids last).
func Sanitize(text string, options []Option) string {
	if text == "" {
		return ""
	}
	s := decodeEntities(text)
	s = norm.NFKC.String(s)
	s = hyphens.Replace(apostrophes.Replace(foldSpaces(s)))
	s = sponsorshipTypo.ReplaceAllStringFunc(s, spellSponsorship)
	s = employmentTypo.ReplaceAllStringFunc(s, spellEmployment)
	s = yourForYouTypo.ReplaceAllString(s, "$1 you $2")
	s = countryPlural.ReplaceAllString(s, "${1}y")

	s = sapHint.ReplaceAllString(s, "")
	s = sapTail.ReplaceAllString(s, "")
	s = fieldErrorTail.ReplaceAllString(s, "")
	s = leadingStars.ReplaceAllString(s, "")
	s = leadingOrdinal.ReplaceAllString(s, "")
	s = cutElementIDs(s)
	s = cutReactUseID(s)
	s = collapseRequiredBadge(s)
	s = recoverSmartApplyQuestion(s)
	s = frameworkIDs.ReplaceAllString(s, " ")
	s = stripCamelCaseIDs(s)
	s = collapseCaptionWithValue(s)
	s = collapseRepeatedQuestion(s, options)
	s = stripDOMTokenLeaks(s)
	s = stripControlFurniture(s)
	s = stripGluedLeadingOption(s, options)
	s = stripOpaqueIDs(s)
	s = reassuranceTail.ReplaceAllString(s, "?")
	return strings.Join(strings.Fields(s), " ")
}

var (
	entityRE = lazyregexp.New(`(?i)&(?:amp|lt|gt|quot|apos|nbsp|#x?[0-9a-f]+|[a-z][a-z0-9]*);`)

	// sponsorshipTypo is a misspelt "sponsorship" (Cohere's "sponorship"):
	// every sponsorship rule reads the word whole.
	sponsorshipTypo = lazyregexp.New(`(?i)\bspon[sore]{2,4}h?i?p\b`)
	// employmentTypo is a misspelt "employment" (Poshmark's "employement").
	employmentTypo = lazyregexp.New(`(?i)\bemploye{1,2}ment\b`)
	// yourForYouTypo is "your" for "you" between an auxiliary and a bare verb
	// (Customer.io's "How did your hear about this job?"); before a noun it stays.
	yourForYouTypo = lazyregexp.New(`(?i)\b(do|did|will|would|can|could)\s+your\s+(hear|learn|find|see|come|require|need|agree|consent)\b`)
	// countryPlural is "country(s)" (CircleCI's "the country(s) where the job
	// is posted"): the place rules read "the country where", and the posting's
	// countries are each read there.
	countryPlural = lazyregexp.New(`(?i)\b(countr)(?:y|ie)\s*\((?:s|ies|es)\)`)

	sapHint = lazyregexp.New(`(?i)^\s*One or more results available\.\s*Press Up or Down Arrow Keys to navigate the selection list\.(?:\s*Press Alt \+ Arrow keys to resize the selection list\.)?\s*`)
	sapTail = lazyregexp.New(`(?i)\s*(?:No Selection\s+)?\d+:_input\s*$`)
	// fieldErrorTail is Workday's required-field error, read into the label
	// once a step comes back refused.
	fieldErrorTail = lazyregexp.New(`(?i)\s*Error\s*:\s*The field\b.*\bis required and must have a value\.?\s*$`)
	// reassuranceTail is a short remark after the question that asks nothing
	// ("How many years ...? Any range is fine!"): it diluted the question's
	// match until the total-years entry no longer claimed it (Rebar).
	reassuranceTail = lazyregexp.New(`(?i)\?\s+(?:any|an?|your|rough|best)?\s*(?:rough\s+|best\s+)?(?:range|estimate|guess|approximation|ballpark(?:\s+figure)?|number)s?\s+(?:is|are|will\s+be)\s+(?:fine|ok|okay|welcome|great)\s*[.!]*\s*$`)
	leadingStars    = lazyregexp.New(`^\s*\*+\s*`)
	leadingOrdinal  = lazyregexp.New(`(?i)^\s*Q\d{1,2}\s*[.):]\s*`)
	elementIDStart  = lazyregexp.New(`(?i)(?:^|\s)(?:text-entity-list-form-component|urn:li:|urn-li-|easyapplyformelement-)`)
	useIDColon      = lazyregexp.New(`(?i):r[0-9a-z]+:`)
	requiredSplit   = lazyregexp.New(`\s+Required\s+`)
	requiredTail    = lazyregexp.New(`\s+Required\s*$`)
	trailingStar    = lazyregexp.New(`\s*\*\s*$`)
	frameworkIDs    = lazyregexp.New(`\bmultiQuestionAnswer\d+(?:_\d+)?\b|\bapplication_form\[[^\]]*\](?:\[[^\]]*\])*`)
	camelToken      = lazyregexp.New(`^[a-z]+[A-Z][A-Za-z]*\d*$`)
	snakeToken      = lazyregexp.New(`(?i)\S*(?:[a-z0-9]+_[a-z0-9]+)+\S*`)
	emailToken      = lazyregexp.New(`(?i)[\w.+-]+@[\w-]+(?:\.[\w-]+)+(?:\x{2026}|\.{1,3})?`)
	kebabToken      = lazyregexp.New(`[A-Za-z][A-Za-z0-9]*(?:-[A-Za-z0-9]+)+`)
	kebabSuffix     = lazyregexp.New(`(?i)-(?:input|inputs|field|fields|name|date|text|select|listbox|list|group|wrapper|container)$`)
	kebabControl    = lazyregexp.New(`(?i)^(?:form|input|field|select|custom)-control$`)
	leadSentence    = lazyregexp.New(`^(.{20,}?[.?!])\s+`)
	trailingOff     = lazyregexp.New(`(?i)^(.+?)\s+off\s*$`)
	threeLetters    = lazyregexp.New(`[A-Za-z]{3,}`)
	aspNetName      = lazyregexp.New(`\bctl\d+\$[\w$]+`)
	leadingSelect   = lazyregexp.New(`(?i)^\s*(?:select\s+one(?:\s+\[?required\]?)?\s+)+`)
	trailingPrompt  = lazyregexp.New(`(?i)\s*[*:]?\s*(?:select an option|select one(?:\s+\[?required\]?)?|(?:select|choose|pick)\s*(?:\.{2,}|\x{2026})|please (?:select|choose)(?: one)?|please enter a value|required field|this field is required)\s*$`)
	parenFlag       = lazyregexp.New(`(?i)\s*\(\s*(?:required|optional|if applicable|if any)\s*\)`)
	typingPrompt    = lazyregexp.New(`(?i)^\s*(?:enter|type)\s+(?:your\s+)?(.+?)\s*(?:\.{2,}|\x{2026})\s*$`)
	uuidRE          = lazyregexp.New(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	hexRun          = lazyregexp.New(`(?i)[0-9a-f]{12,}`)
	prefixedID      = lazyregexp.New(`(?i)^(?:question_|exp-desc-)[a-z0-9-]{6,}$`)
	labeledIDTail   = lazyregexp.New(`(?i)-labeled-(?:radio|checkbox)-\d+$`)
	shortMixedID    = lazyregexp.New(`(?i)^[a-z]{1,4}\d[a-z0-9]{2,6}$`)
	reactIDToken    = lazyregexp.New(`(?i)^:[a-z0-9]+:[a-z0-9]*$`)
	glueOnly        = lazyregexp.New(`^[-_]+$`)
)

var autocompleteTokens = setOf(
	"given-name", "family-name", "additional-name", "honorific-prefix", "honorific-suffix",
	"postal-code", "cc-name", "tel", "tel-country-code", "tel-national", "tel-local",
	"tel-extension", "street-address", "address-line1", "address-line2", "address-level1", "address-level2",
)

// decodeEntities decodes the six common HTML entities in one pass; every other
// entity becomes a space.
func decodeEntities(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	return entityRE.ReplaceAllStringFunc(s, func(m string) string {
		switch strings.ToLower(m) {
		case "&amp;":
			return "&"
		case "&lt;":
			return "<"
		case "&gt;":
			return ">"
		case "&quot;":
			return `"`
		case "&apos;":
			return "'"
		}
		return " "
	})
}

// cutElementIDs drops a leaked DOM element id and everything after it.
func cutElementIDs(s string) string {
	if loc := elementIDStart.FindStringIndex(s); loc != nil {
		return s[:loc[0]]
	}
	return s
}

// cutReactUseID cuts from the first guillemet id run (with the glued element
// word before it) to the end, and blanks colon-wrapped ids in place.
func cutReactUseID(s string) string {
	const open = "\xc2\xab"
	if i := strings.Index(s, open); i >= 0 && i+len(open) < len(s) && (s[i+len(open)] == 'r' || s[i+len(open)] == 'R') {
		start := i
		for start > 0 && s[start-1] != ' ' {
			start--
		}
		s = s[:start]
	}
	return useIDColon.ReplaceAllString(s, " ")
}

func sameQuestion(a, b string) bool {
	norm := func(x string) string {
		x = strings.ToLower(strings.Join(strings.Fields(x), " "))
		return strings.TrimRight(x, "?!. \t")
	}
	na, nb := norm(a), norm(b)
	return na != "" && na == nb
}

// collapseRequiredBadge keeps the visible label when a standalone Required
// badge separates two copies of it, and drops a trailing badge.
func collapseRequiredBadge(s string) string {
	if parts := requiredSplit.Split(s, -1); len(parts) == 2 && sameQuestion(parts[0], parts[1]) {
		s = parts[0]
	}
	return requiredTail.ReplaceAllString(s, "")
}

// recoverSmartApplyQuestion keeps only the question that precedes a job-board
// "Back to Job Posting" envelope, when it still has two words with letters.
func recoverSmartApplyQuestion(s string) string {
	const marker, legend = "Back to Job Posting", "Indicates a required field"
	i := strings.Index(s, marker)
	if i < 0 || !strings.Contains(s, legend) {
		return s
	}
	prefix := trailingStar.ReplaceAllString(s[:i], "")
	if IsContentless(prefix) {
		return s
	}
	words := 0
	for _, w := range strings.Fields(prefix) {
		if strings.IndexFunc(w, unicode.IsLetter) >= 0 {
			words++
		}
	}
	if words < 2 {
		return s
	}
	return strings.TrimSpace(prefix)
}

// stripCamelCaseIDs removes a camelCase field-id token, but only from text that
// is polluted by a repeat: a prose brand word such as a camel-cased product
// name never has its preceding window repeated.
func stripCamelCaseIDs(s string) string {
	var out strings.Builder
	last := 0
	lower := strings.ToLower(s)
	i := 0
	for i < len(s) {
		for i < len(s) && s[i] == ' ' {
			i++
		}
		j := i
		for j < len(s) && s[j] != ' ' {
			j++
		}
		if j > i && camelToken.MatchString(s[i:j]) {
			ws := i - 16
			if ws < 0 {
				ws = 0
			}
			for ws < i && !isRuneStart(s[ws]) {
				ws++
			}
			window := strings.TrimSpace(lower[ws:i])
			if len(window) >= 12 && strings.Count(lower, window) >= 2 {
				out.WriteString(s[last:i])
				last = j
			}
		}
		i = j
	}
	out.WriteString(s[last:])
	return out.String()
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// collapseCaptionWithValue reduces "<Caption>* <value> <Caption>*<value>" (a
// filled typeahead read twice) to the caption. It is the backreference form
// ^\s*(.{2,60}?\*)\s*(\S.*?)\s*\1\s*\2\s*$ written as an explicit scan.
func collapseCaptionWithValue(s string) string {
	t := strings.TrimLeft(s, " ")
	for i := 0; i < len(t) && i < 60; i++ {
		if t[i] != '*' || i+1 < 2 {
			continue
		}
		caption := t[:i+1]
		rest := strings.TrimLeft(t[i+1:], " ")
		rest = strings.TrimRight(rest, " ")
		for n := 1; n*2+len(caption) <= len(rest); n++ {
			v := rest[:n]
			if v[0] == ' ' {
				break
			}
			tail := strings.TrimLeft(rest[n:], " ")
			if !strings.HasPrefix(tail, caption) {
				continue
			}
			tail = strings.TrimLeft(tail[len(caption):], " ")
			if tail == v {
				return caption
			}
		}
	}
	return s
}

// collapseRepeatedQuestion keeps one copy of a question that the page rendered
// two or more times, dropping the control's own option labels between copies.
func collapseRepeatedQuestion(s string, options []Option) string {
	q := strings.IndexByte(s, '?')
	if q < 0 || strings.Count(s, "?") < 2 {
		return s
	}
	sentence := strings.TrimSpace(s[:q+1])
	if len(sentence) < 12 {
		return s
	}
	var locs [][2]int
	for i := 0; i+len(sentence) <= len(s); i++ {
		if strings.EqualFold(s[i:i+len(sentence)], sentence) {
			locs = append(locs, [2]int{i, i + len(sentence)})
			i += len(sentence) - 1
		}
	}
	if len(locs) < 2 {
		return s
	}
	labels := map[string]bool{}
	for _, o := range options {
		labels[strings.ToLower(strings.TrimSpace(o.Label))] = true
	}
	var chunks []string
	prev := 0
	for _, l := range locs {
		chunks = append(chunks, strings.TrimSpace(s[prev:l[0]]))
		prev = l[1]
	}
	chunks = append(chunks, strings.TrimSpace(s[prev:]))
	var keep []string
	for i, c := range chunks {
		junk := c == "" || labels[strings.ToLower(c)]
		if i == 0 {
			if !junk {
				keep = append(keep, c)
			}
			keep = append(keep, sentence)
			continue
		}
		if !junk {
			keep = append(keep, c)
		}
	}
	return strings.Join(keep, " ")
}

// stripDOMTokenLeaks removes control-class, field-id and sample-address tokens
// the page leaks into a label, then repeated lead sentences and repeated words.
func stripDOMTokenLeaks(s string) string {
	s = snakeToken.ReplaceAllString(s, " ")
	s = emailToken.ReplaceAllString(s, " ")
	text := s
	s = kebabToken.ReplaceAllStringFunc(s, func(tok string) string {
		if kebabSuffix.MatchString(tok) && echoesLabel(tok, text) || kebabControl.MatchString(tok) || autocompleteTokens[strings.ToLower(tok)] {
			return " "
		}
		return tok
	})
	if m := leadSentence.FindStringSubmatchIndex(s); m != nil {
		sent := s[m[2]:m[3]]
		if k := strings.Index(s[len(sent)+1:], sent); k >= 0 {
			s = strings.TrimSpace(s[:len(sent)+1+k])
		}
	}
	if m := trailingOff.FindStringSubmatch(s); m != nil && threeLetters.MatchString(m[1]) && len(strings.Fields(m[1])) >= 2 {
		s = m[1]
	}
	return dedupeShortLabelTokens(s)
}

// echoesLabel reports a control-shaped token ("email-input") whose leading
// words the text also says outside it, as an id derived from the label does;
// a hyphenated word of the question itself ("your ideal start-date?") says
// nothing twice.
func echoesLabel(tok, text string) bool {
	parts := strings.Split(strings.ToLower(tok), "-")
	rest := strings.Fields(strings.ToLower(strings.Replace(text, tok, " ", 1)))
	for _, p := range parts[:len(parts)-1] {
		for _, w := range rest {
			if strings.Trim(w, "*:?.,!()") == p {
				return true
			}
		}
	}
	return false
}

func tokenKey(t string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(t) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

var negationWords = setOf("not", "no", "never")

// dedupeShortLabelTokens drops a token that spells like the token right before
// it ("Email* email") in labels of ten tokens or fewer. Only an adjacent repeat
// goes: a word legitimately recurring in a sentence ("C and C++", "to ... to")
// stays. Pure-number and punctuation tokens always stay.
func dedupeShortLabelTokens(s string) string {
	toks := strings.Fields(s)
	if len(toks) > 10 {
		return strings.TrimSpace(s)
	}
	var out []string
	prev := ""
	for _, t := range toks {
		k := tokenKey(t)
		// A repeated negation is a double negation, not a stutter: dropping one
		// would turn it into a plain negation and flip the answer.
		if strings.IndexFunc(k, unicode.IsLetter) >= 0 && k == prev && !negationWords[k] {
			continue
		}
		prev = k
		out = append(out, t)
	}
	return strings.Join(out, " ")
}

// stripControlFurniture removes the control's own chrome: ASP.NET control
// names, repeated captions, "Select One" prompts, state words and typing
// prompts.
func stripControlFurniture(s string) string {
	if loc := aspNetName.FindStringIndex(s); loc != nil {
		if strings.TrimSpace(s[:loc[0]]) == "" {
			s = s[loc[1]:]
		} else {
			s = s[:loc[0]]
		}
	}
	if segs := strings.Split(s, "*"); len(segs) >= 3 {
		first := strings.ToLower(strings.TrimSpace(segs[0]))
		if first != "" && first == strings.ToLower(strings.TrimSpace(segs[1])) {
			s = strings.TrimSpace(segs[0]) + "*"
		}
	}
	if loc := leadingSelect.FindStringIndex(s); loc != nil && len(strings.Fields(s[loc[1]:])) >= 2 {
		s = s[loc[1]:]
	}
	for {
		next := trailingPrompt.ReplaceAllString(s, "")
		if next == s {
			break
		}
		s = next
	}
	s = parenFlag.ReplaceAllString(s, "")
	if m := typingPrompt.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	return strings.Join(strings.Fields(s), " ")
}

// stripGluedLeadingOption drops a leading run equal to one of the control's own
// option labels when a real question remains: pages often render the first
// option label glued before the question.
func stripGluedLeadingOption(s string, options []Option) string {
	if len(options) == 0 {
		return s
	}
	seen := map[string]bool{}
	var labels []string
	for _, o := range options {
		l := strings.TrimSpace(o.Label)
		if len(l) >= 2 && !seen[strings.ToLower(l)] {
			seen[strings.ToLower(l)] = true
			labels = append(labels, l)
		}
	}
	sort.SliceStable(labels, func(i, j int) bool { return len(labels[i]) > len(labels[j]) })
	body := strings.TrimLeft(s, " ")
	for _, l := range labels {
		if len(body) <= len(l) || !strings.EqualFold(body[:len(l)], l) || body[len(l)] != ' ' {
			continue
		}
		rest := strings.TrimSpace(body[len(l):])
		if strings.IndexFunc(rest, unicode.IsLetter) >= 0 && len(strings.Fields(rest)) >= 2 {
			return rest
		}
	}
	return s
}

// isOpaqueIDToken reports whether a whitespace-free token is an identifier
// rather than a word.
func isOpaqueIDToken(t string) bool {
	switch {
	case glueOnly.MatchString(t):
		return true
	case uuidRE.MatchString(t), hexRun.MatchString(t):
		return true
	case prefixedID.MatchString(t), labeledIDTail.MatchString(t):
		return true
	case len(t) >= 4 && len(t) <= 8 && shortMixedID.MatchString(t):
		return true
	case reactIDToken.MatchString(t):
		return true
	}
	return false
}

// stripOpaqueIDs drops identifier tokens (UUIDs, long hex runs, short mixed ids).
func stripOpaqueIDs(s string) string {
	toks := strings.Fields(s)
	out := toks[:0]
	for _, t := range toks {
		if !isOpaqueIDToken(t) {
			out = append(out, t)
		}
	}
	return strings.Join(out, " ")
}

// spellSponsorship spells a misspelt "sponsorship" whole, keeping its capital.
func spellEmployment(w string) string {
	if w[0] == 'E' {
		return "Employment"
	}
	return "employment"
}

func spellSponsorship(w string) string {
	if w[0] == 'S' {
		return "Sponsorship"
	}
	return "sponsorship"
}
