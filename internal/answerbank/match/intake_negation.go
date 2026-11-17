package match

import (
	"strings"

	"golang.org/x/text/unicode/norm"

	"applysmith/internal/lazyregexp"
)

var (
	negativeVerbRE = lazyregexp.New(`(?i)\b(?:[a-z]*n\s*'\s*t|cannot)\b`)
	// negationTokens open a negation scope in option scoring.
	negationTokens = setOf("not", "no", "non", "never", "without", "neither", "nor")
	// refusalNegationTokens widen the acknowledgement scan only: scoring must not
	// read "Decline to self-identify" as a negation of its own words.
	refusalNegationTokens = setOf("not", "no", "non", "never", "without", "neither", "nor",
		"refuse", "reject", "decline", "dont", "doesnt", "havent", "haven", "wont")
	clauseBoundary = setOf("but", "however", "although", "though", "yet", "except", "while", "whereas")
	// scopeFunctionWords are skipped when partitioning a clause: "I am not a
	// veteran" must not negate "a", which nearly every option affirms.
	scopeFunctionWords = setOf(
		"a", "an", "the", "i", "me", "my", "mine", "we", "us", "our", "you", "your", "he", "she", "it",
		"they", "them", "their", "this", "that", "these", "those", "am", "is", "are", "was", "were", "be",
		"been", "being", "do", "does", "did", "have", "has", "had", "will", "would", "shall", "should",
		"can", "could", "may", "might", "must", "to", "of", "in", "on", "for", "with", "at", "by", "from",
		"as", "and", "or", "if", "so", "than", "then", "there", "here", "any", "all", "one", "ever",
		"please", "currently", "also",
	)
)

// expandContractions normalizes the established entity, Unicode and quote
// forms before rewriting contracted negatives. Scoring and stance readers
// must see the same negation before apostrophes become token separators.
func expandContractions(s string) string {
	s = apostrophes.Replace(foldSpaces(norm.NFKC.String(decodeEntities(s))))
	// Every contracted negative holds an apostrophe; the regular expression
	// is most of the cost on long option lists.
	if !strings.Contains(s, "'") && !hasFold(s, "cannot") {
		return s
	}
	return negativeVerbRE.ReplaceAllStringFunc(s, func(word string) string {
		word = strings.Join(strings.Fields(word), "")
		if strings.EqualFold(word, "cannot") {
			return word[:3] + " not"
		}
		root := word[:len(word)-3]
		switch strings.ToLower(root) {
		case "ca":
			root += "n"
		case "sha":
			root += "ll"
		case "wo":
			root = "will"
			if word[0] == 'W' {
				root = "Will"
			}
		}
		return root + " not"
	})
}

// negationTokenList tokenizes a text after expanding contractions.
func negationTokenList(s string) []string { return tokenize(expandContractions(s)) }

// hasNegationBefore reports a negation token within the window of tokens before
// the span start.
func hasNegationBefore(tokens []string, spanStart, window int) bool {
	lo := spanStart - window
	if lo < 0 {
		lo = 0
	}
	for i := lo; i < spanStart && i < len(tokens); i++ {
		if refusalNegationTokens[tokens[i]] {
			return true
		}
	}
	return false
}

// negationScope partitions the content tokens of a text into those it affirms
// and those it negates. A negation token opens a scope and a clause boundary
// closes it; a token can be in both.
func negationScope(text string) (affirmed, negated map[string]bool) {
	affirmed, negated = map[string]bool{}, map[string]bool{}
	inScope := false
	for _, t := range negationTokenList(text) {
		switch {
		case negationTokens[t]:
			inScope = true
		case clauseBoundary[t]:
			inScope = false
		case scopeFunctionWords[t]:
		case inScope:
			negated[t] = true
		default:
			affirmed[t] = true
		}
	}
	return affirmed, negated
}

// hasNegationToken reports any negation token in the text.
func hasNegationToken(text string) bool {
	for _, t := range negationTokenList(text) {
		if negationTokens[t] {
			return true
		}
	}
	return false
}

// ackWords are the acknowledgement verbs, also as the past form a lone box
// labels its state with (Sysdig: "Acknowledged").
var ackWords = setOf("accept", "acknowledge", "agree", "confirm", "accepted", "acknowledged", "agreed", "confirmed")

// hasUnnegatedAcknowledgement reports an acknowledgement word in the label with
// no negation in the four tokens before it, or "have read and understand(ood)"
// (TRM: "I have read and understand the expectations of working at TRM ...");
// "understand" alone is no acknowledgement ("I understand basic SQL").
func hasUnnegatedAcknowledgement(label string) bool {
	toks := negationTokenList(strings.ToLower(label))
	for i, t := range toks {
		if ackWords[t] && !hasNegationBefore(toks, i, 4) {
			return true
		}
		if (t == "understand" || t == "understood") && i >= 3 && toks[i-1] == "and" && toks[i-2] == "read" && (toks[i-3] == "have" || toks[i-3] == "has") {
			return true
		}
	}
	return false
}

// hasFold reports whether s holds sub in any letter case.
func hasFold(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if strings.EqualFold(s[i:i+len(sub)], sub) {
			return true
		}
	}
	return false
}
