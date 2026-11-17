package kb

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// stopWords are dropped from queries and documents alike. Question scaffolding
// ("describe", "explain", "tell", "how", "what") is included on purpose: it
// appears in nearly every prompt, so it would otherwise outrank topic words.
var stopWords = setOf(
	"a", "about", "above", "after", "again", "against", "all", "also", "am", "an", "and", "any", "are", "as", "at",
	"be", "because", "been", "before", "being", "below", "between", "both", "but", "by",
	"can", "could", "did", "do", "does", "doing", "down", "during",
	"each", "else", "few", "for", "from", "further",
	"had", "has", "have", "having", "he", "her", "here", "hers", "him", "his", "how",
	"i", "if", "in", "into", "is", "it", "its", "just",
	"may", "me", "might", "more", "most", "must", "my", "mine",
	"no", "nor", "not", "of", "off", "on", "once", "only", "or", "other", "our", "ours", "out", "over", "own",
	"same", "shall", "she", "should", "so", "some", "such",
	"than", "that", "the", "their", "theirs", "them", "then", "there", "these", "they", "this", "those", "through", "to", "too",
	"under", "until", "up", "us", "very",
	"was", "we", "were", "what", "when", "where", "which", "while", "who", "whom", "whose", "why", "will", "with", "within", "without", "would",
	"you", "your", "yours",
	// Question scaffolding.
	"describe", "discuss", "explain", "experience", "give", "please", "share", "talk", "tell", "walk",
)

// frameWords mark a query as shaped like a question for the semantic stage.
var frameWords = setOf("describe", "explain", "tell", "walk", "share", "how", "what", "when", "where", "which", "who", "why", "please")

func setOf(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// unstemmed words keep their form because folding them would collide with a
// word of a different meaning.
var unstemmed = setOf("interesting")

// rawWord is one word of a text: its parts, and for a hyphenated word the
// parts joined ("open-source": open, source; opensource).
type rawWord struct {
	parts []string
	join  string
}

// unspaced reports a letter of a script written without spaces between words
// (Han, Hiragana, Katakana and the long-vowel mark).
func unspaced(r rune) bool {
	return r == '\u30fc' || unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana)
}

// signedNames are names whose letters alone are one character or a common
// word; each is read as one term when it stands as a word of its own (".net"
// also after a word, as in "asp.net").
var signedNames = []struct{ text, term string }{{"c++", "cplusplus"}, {"c#", "csharp"}, {"f#", "fsharp"}, {".net", "dotnet"}}

// signedName returns the term of the signed name that starts at rs[i], if one does.
func signedName(rs []rune, i int, isWord func(rune) bool) (string, int, bool) {
	for _, n := range signedNames {
		if rs[i] != rune(n.text[0]) {
			continue
		}
		end := i + len(n.text) // the names are ASCII
		if end > len(rs) || string(rs[i:end]) != n.text || (end < len(rs) && isWord(rs[end])) {
			continue
		}
		if n.text[0] != '.' && i > 0 && (isWord(rs[i-1]) || rs[i-1] == '.') {
			continue
		}
		return n.term, len(n.text), true
	}
	return "", 0, false
}

// rawWords lowercases s and splits it into words of letters and digits. An
// inner apostrophe stays inside a part; an inner hyphen splits a word into
// parts; a possessive "'s" is dropped. Composed and decomposed accents read
// alike. A run of a script written without spaces is one word whose parts are
// its overlapping character pairs, so a phrase inside a sentence is found.
func rawWords(s string) []rawWord {
	rs := []rune(strings.ToLower(norm.NFC.String(s)))
	var out []rawWord
	var parts []string
	var cur []rune
	part := func() {
		if n := len(cur); n > 2 && cur[n-1] == 's' && (cur[n-2] == '\'' || cur[n-2] == '’') {
			cur = cur[:n-2]
		}
		if len(cur) > 0 {
			parts = append(parts, string(cur))
			cur = cur[:0]
		}
	}
	flush := func() {
		part()
		if len(parts) > 0 {
			w := rawWord{parts: parts}
			if len(parts) > 1 {
				w.join = strings.Join(parts, "")
			}
			out = append(out, w)
		}
		parts = nil
	}
	isWord := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if t, n, ok := signedName(rs, i, isWord); ok {
			flush()
			out = append(out, rawWord{parts: []string{t}})
			i += n - 1
			continue
		}
		if unspaced(r) {
			flush()
			j := i + 1
			for j < len(rs) && unspaced(rs[j]) {
				j++
			}
			w := rawWord{parts: []string{string(rs[i:j])}}
			if j-i > 1 {
				w.parts = nil
				for k := i; k+1 < j; k++ {
					w.parts = append(w.parts, string(rs[k:k+2]))
				}
			}
			out = append(out, w)
			i = j - 1
			continue
		}
		inner := len(cur) > 0 && i+1 < len(rs) && isWord(rs[i+1]) && !unspaced(rs[i+1])
		switch {
		case isWord(r):
			cur = append(cur, r)
		case r == '-' && inner:
			part()
		case (r == '\'' || r == '’') && inner:
			cur = append(cur, r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// rawTokens is every part and every joined form of the words of s, so a
// hyphenated word meets the words written apart or as one, in a query and a
// document alike.
func rawTokens(s string) []string {
	var out []string
	for _, w := range rawWords(s) {
		out = append(out, w.parts...)
		if w.join != "" {
			out = append(out, w.join)
		}
	}
	return out
}

// Tokens returns the indexed terms of s: lowercase, one-character tokens and
// stop words removed, plurals and -ing/-ed endings folded lightly. Repeats are
// kept. The same function serves queries and documents.
func Tokens(s string) []string {
	var out []string
	for _, t := range rawTokens(s) {
		if f, ok := term(t); ok {
			out = append(out, f)
		}
	}
	return out
}

// term is the indexed term of one raw token, or false for a one-character
// token or a stop word.
func term(t string) (string, bool) {
	if utf8.RuneCountInString(t) < 2 || stopWords[t] {
		return "", false
	}
	f := fold(t)
	if utf8.RuneCountInString(f) < 2 || stopWords[f] {
		return "", false
	}
	return f, true
}

// Terms returns the distinct terms of s in sorted order.
func Terms(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range Tokens(s) {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

func isVowel(r rune) bool {
	switch r {
	case 'a', 'e', 'i', 'o', 'u', 'y':
		return true
	}
	return false
}

func hasVowel(s string) bool {
	for _, r := range s {
		if isVowel(r) {
			return true
		}
	}
	return false
}

// fold applies the light stemmer. It is deliberately conservative: it only has
// to map the common forms of one word to one term, identically for the query
// and the index. A final "e" is dropped from every form, so "code", "codes",
// "coded" and "coding" all read "cod"; a two-letter -ed/-ing stem gets its "e"
// back ("used", "using" -> "use") because dropping it would leave a stop word
// or a fragment.
func fold(w string) string {
	if unstemmed[w] || !isAlpha(w) || len(w) < 3 {
		return w
	}
	if len(w) > 3 {
		switch {
		case strings.HasSuffix(w, "ies") && len(w) > 4:
			w = w[:len(w)-3] + "y"
		case strings.HasSuffix(w, "sses"):
			w = w[:len(w)-2]
		case hasSuffixAny(w, "xes", "ches", "shes", "zes") && len(w) > 4:
			w = w[:len(w)-2]
		case strings.HasSuffix(w, "s") && !hasSuffixAny(w, "ss", "us", "is"):
			w = w[:len(w)-1]
		}
		for _, suf := range []string{"ing", "ed"} {
			if strings.HasSuffix(w, suf) {
				stem := w[:len(w)-len(suf)]
				switch {
				case len(stem) >= 3 && hasVowel(stem):
					w = undouble(stem)
					if suf == "ed" && strings.HasSuffix(w, "e") {
						w += "e" // "agreed" is "agree" + "d"
					}
				case len(stem) == 2 && hasVowel(stem) && stem[1] != 'e':
					w = stem + "e" // "used", "using" -> "use"; "need" stays whole
				}
				break
			}
		}
	}
	if t := strings.TrimRight(w, "e"); t != w && len(t) >= 3 && !stopWords[t] {
		w = t
	}
	return w
}

func hasSuffixAny(w string, sufs ...string) bool {
	for _, s := range sufs {
		if strings.HasSuffix(w, s) {
			return true
		}
	}
	return false
}

func isAlpha(w string) bool {
	for _, r := range w {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

// undouble drops the second of a doubled final consonant ("running" -> "run"),
// except for the letters that legitimately double.
func undouble(stem string) string {
	n := len(stem)
	if n >= 2 && stem[n-1] == stem[n-2] && !isVowel(rune(stem[n-1])) && !strings.ContainsRune("lsz", rune(stem[n-1])) {
		return stem[:n-1]
	}
	return stem
}

// questionShaped reports whether the query reads as a question: it has a
// question mark or a frame word, and at least one content term.
func questionShaped(query string) bool {
	if len(Tokens(query)) == 0 {
		return false
	}
	if strings.Contains(query, "?") {
		return true
	}
	for _, t := range rawTokens(query) {
		if frameWords[t] {
			return true
		}
	}
	return false
}
