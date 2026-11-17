package assist

import (
	"strings"
	"unicode"

	"applysmith/internal/lazyregexp"
)

// Closed suffix set of the light stemmer: a shorter token of at least four
// characters matches a longer one only when the residue is one of these, so
// "citizen" matches "citizenship" and "over" does not match "overview".
var stemSuffixes = setOf("s", "es", "ed", "ing", "er", "ers", "or", "ors", "ship", "ness", "ity", "ties", "ly", "al", "ment", "able")

var stopwords = setOf(
	"a", "an", "the", "of", "is", "are", "was", "were", "be", "to", "in", "on", "at", "for", "and", "or", "do", "did", "does",
	"you", "your", "yours", "we", "our", "i", "my", "me", "this", "that", "these", "those", "with", "from", "by", "as", "if",
	"it", "its", "have", "has", "had", "will", "would", "can", "could", "should", "select", "option", "options", "required",
	"please", "answer", "currently", "current", "field", "form", "indicates", "yes", "no",
)

var (
	uuidRE      = lazyregexp.New(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	hexRunRE    = lazyregexp.New(`(?i)[0-9a-f]{12,}`)
	nonAlnumRE  = lazyregexp.New(`[^a-z0-9]+`)
	multiSpace  = lazyregexp.New(`\s+`)
	pureDigitRE = lazyregexp.New(`^[0-9]+$`)
)

func setOf(vs ...string) map[string]bool {
	m := make(map[string]bool, len(vs))
	for _, v := range vs {
		m[v] = true
	}
	return m
}

// tokens lowercases text into ASCII tokens: opaque ids stripped, non-ASCII
// letters acting as separators, stopwords, single characters and pure numbers
// removed.
func tokens(text string) []string {
	text = uuidRE.ReplaceAllString(text, " ")
	text = hexRunRE.ReplaceAllString(text, " ")
	var out []string
	for _, t := range nonAlnumRE.Split(strings.ToLower(text), -1) {
		if len(t) < 2 || stopwords[t] || pureDigitRE.MatchString(t) {
			continue
		}
		out = append(out, t)
	}
	return out
}

func distinct(ts []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range ts {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// stemEqual is token equality with the closed suffix set.
func stemEqual(a, b string) bool {
	if a == b {
		return true
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	return len(a) >= 4 && strings.HasPrefix(b, a) && stemSuffixes[b[len(a):]]
}

// matchCount counts the distinct query tokens that stem-match some haystack token.
func matchCount(query, haystack []string) int {
	n := 0
	for _, q := range distinct(query) {
		for _, h := range haystack {
			if stemEqual(q, h) {
				n++
				break
			}
		}
	}
	return n
}

var spelledNumbers = map[string]string{
	"one": "1", "two": "2", "three": "3", "four": "4", "five": "5", "six": "6",
	"seven": "7", "eight": "8", "nine": "9", "ten": "10", "eleven": "11", "twelve": "12",
}

// normalizeSupport is the text form support is judged in: lowercase, every run
// of non-alphanumeric ASCII one space, spelled numbers one to twelve as digits,
// and the man/male and woman/female pairs equal.
func normalizeSupport(s string) string {
	words := strings.Fields(nonAlnumRE.ReplaceAllString(strings.ToLower(s), " "))
	for i, w := range words {
		if d, ok := spelledNumbers[w]; ok {
			words[i] = d
		}
		switch w {
		case "man":
			words[i] = "male"
		case "woman":
			words[i] = "female"
		}
	}
	return " " + strings.Join(words, " ") + " "
}

func wordCount(s string) int { return len(strings.Fields(s)) }

func collapse(s string) string { return strings.TrimSpace(multiSpace.ReplaceAllString(s, " ")) }

// clipRunes cuts s to at most n runes, ending with an ellipsis when it was cut.
func clipRunes(s string, n int) string {
	s = collapse(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimRightFunc(string(r[:n-1]), unicode.IsSpace) + "\u2026"
}

// titles never end a sentence: a name follows them.
var titles = setOf("dr", "mr", "mrs", "ms", "jr", "sr", "st", "vs")

// abbreviations end a sentence only when a capital letter follows, so "an M.S.
// in physics" stays whole while "Acme Corp. I left" splits.
var abbreviations = setOf("inc", "corp", "ltd", "co", "llc", "etc", "approx", "e.g", "i.e", "m.s", "b.s", "b.a", "m.a", "m.eng", "ph.d", "u.s")

var initialsRE = lazyregexp.New(`^(?:[a-z]\.)+[a-z]?$`)

// sentences splits text after a terminator followed by white space or the end,
// except after a title, or after an abbreviation or a run of initials ("B.S.",
// "U.S.") that no capital letter follows. Every sentence keeps its terminator.
func sentences(text string) []string {
	var out []string
	r := []rune(text)
	start := 0
	for i := 0; i < len(r); i++ {
		if r[i] != '.' && r[i] != '!' && r[i] != '?' {
			continue
		}
		j := i
		for j+1 < len(r) && (r[j+1] == '.' || r[j+1] == '!' || r[j+1] == '?' || r[j+1] == '"' || r[j+1] == '\'' || r[j+1] == ')') {
			j++
		}
		if j+1 < len(r) && !unicode.IsSpace(r[j+1]) {
			i = j
			continue
		}
		if r[i] == '.' && continuesAfterAbbreviation(string(r[start:j+1]), nextRune(r, j+1)) {
			i = j
			continue
		}
		out = append(out, strings.TrimSpace(string(r[start:j+1])))
		start = j + 1
		i = j
	}
	if tail := strings.TrimSpace(string(r[start:])); tail != "" {
		out = append(out, tail)
	}
	return out
}

// nextRune is the first non-space rune from i, or 0 at the end.
func nextRune(r []rune, i int) rune {
	for ; i < len(r); i++ {
		if !unicode.IsSpace(r[i]) {
			return r[i]
		}
	}
	return 0
}

// continuesAfterAbbreviation reports whether the period that closes s belongs to
// an abbreviation. At the end of the text (next == 0) an abbreviation is not a
// sentence end, so a cut that lands there falls back to the sentence before.
func continuesAfterAbbreviation(s string, next rune) bool {
	s = strings.TrimRight(s, ".!?\"')")
	i := strings.LastIndexFunc(s, unicode.IsSpace)
	w := strings.ToLower(strings.TrimLeft(s[i+1:], "(\"'"))
	if titles[w] {
		return true
	}
	if abbreviations[w] || (strings.Contains(w, ".") && initialsRE.MatchString(w+".")) {
		return next == 0 || !unicode.IsUpper(next)
	}
	return false
}

func joinSentences(ss []string) string { return strings.Join(ss, " ") }
