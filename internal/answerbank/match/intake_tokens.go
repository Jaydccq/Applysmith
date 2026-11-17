package match

import (
	"slices"
	"strings"
	"unicode"
)

// stopWords are dropped from every stemmed token set.
var stopWords = setOf(
	"a", "an", "the", "is", "are", "was", "were", "be", "been", "being", "do", "does", "did",
	"to", "of", "in", "on", "for", "with", "at", "by", "from", "as", "and", "or", "but", "if",
	"your", "you", "yours", "this", "that", "these", "those", "it", "its", "any", "all", "some",
	"what", "which", "who", "whom", "whose", "how", "why", "when", "have", "has", "had", "will",
	"would", "could", "should", "may", "might", "ever", "please", "kindly", "applicant",
	"candidate", "myself", "yourself", "himself", "herself", "itself", "oneself", "ourselves",
	"yourselves", "themselves",
)

// fuzzyBoilerplate are form words the fuzzy overlap ignores on top of stopWords.
var fuzzyBoilerplate = setOf("select", "follow", "apply")

func setOf(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// tokenize splits on any run of characters that are neither a letter nor a
// number of any kind (\p{L}, \p{N}) and lowercases.
func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

// utf16Len is the length of w in UTF-16 code units, the unit every length
// cutoff of the stemmer and the token filter is stated in.
func utf16Len(w string) int {
	n := 0
	for _, r := range w {
		n++
		if r >= 0x10000 {
			n++
		}
	}
	return n
}

// stem applies the small suffix stemmer every overlap score reads. Its length
// cutoffs count characters (UTF-16 units), not bytes, so an accented token is
// cut at the same length as an ASCII one.
func stem(w string) string {
	n := utf16Len(w)
	cut := func(k int) string { return w[:len(w)-k] }
	switch {
	case n >= 6 && strings.HasSuffix(w, "ies"):
		return cut(3) + "y"
	case n >= 5 && strings.HasSuffix(w, "ing"):
		return cut(3)
	case n >= 5 && strings.HasSuffix(w, "ed"):
		return cut(2)
	case n >= 5 && strings.HasSuffix(w, "es"):
		return cut(2)
	case n >= 4 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss"):
		return cut(1)
	}
	return w
}

// stemmedTokens returns the ordered, distinct stemmed content tokens of s,
// read from the normalized label (NFKC, entities decoded, lowercase).
func stemmedTokens(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range tokenize(normalizeLabel(s)) {
		if utf16Len(t) < 2 || stopWords[t] {
			continue
		}
		st := stem(t)
		if !seen[st] {
			seen[st] = true
			out = append(out, st)
		}
	}
	return out
}

// fuzzyTokens is stemmedTokens without the form boilerplate words.
func fuzzyTokens(s string) []string {
	toks := stemmedTokens(s)
	return slices.DeleteFunc(toks, func(t string) bool { return fuzzyBoilerplate[t] })
}

func tokenSet(toks []string) map[string]bool {
	m := make(map[string]bool, len(toks))
	for _, t := range toks {
		m[t] = true
	}
	return m
}

// sharedCount counts the tokens of a that also occur in b.
func sharedCount(a []string, b map[string]bool) int {
	n := 0
	for _, t := range a {
		if b[t] {
			n++
		}
	}
	return n
}

// containsAllTokens reports whether needle occurs as a contiguous run in
// haystack; an empty needle is false.
func containsAllTokens(haystack, needle []string) bool { return optRunStart(haystack, needle) >= 0 }
