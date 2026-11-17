package match

import (
	"slices"
	"strings"
	"unicode"

	"applysmith/internal/lazyregexp"
)

// Shared vocabulary of the option scorer and the self-identification model:
// the placeholder shape, the withdrawal phrases and the one decline vocabulary.

// optTokens is the token list of the normalized text.
func optTokens(s string) []string { return tokenize(normalizeLabel(s)) }

// optRunStart returns the index where needle occurs as a contiguous run of
// tokens in hay, or -1; an empty needle never occurs.
func optRunStart(hay, needle []string) int {
	if len(needle) == 0 || len(needle) > len(hay) {
		return -1
	}
outer:
	for i := 0; i+len(needle) <= len(hay); i++ {
		for j := range needle {
			if hay[i+j] != needle[j] {
				continue outer
			}
		}
		return i
	}
	return -1
}

// placeholderShape is the control's own empty state: dashes, "-- No answer --",
// "Select one", "Choose an option", "No selection", "None", "N/A". The scorer
// lets such a label through only on an exact match.
var placeholderShape = lazyregexp.New(`(?i)^\s*(?:[-\x{2013}\x{2014}.]+|[-\x{2013}\x{2014}]{2,}\s*[^-\x{2013}\x{2014}]{1,40}?\s*[-\x{2013}\x{2014}]{2,}|` +
	`select(?:\s+(?:one|an?\s+option))?|choose(?:\s+(?:one|an?\s+option))?|` +
	`please\s+(?:select|choose)(?:\s+(?:one|an?\s+option))?|make\s+(?:a\s+)?selection(?:\s+here)?|` +
	`no\s+selection|none(?:\s+selected)?|pick\s+one|n/?a)[\s.\x{2026}]*$`)

// isScorePlaceholder is the wider placeholder test of the scorer: besides the
// control's own prompt it reads "None" and "N/A" as an empty state, which only
// an exact answer may choose.
func isScorePlaceholder(label string) bool {
	return isPlaceholderOption(label) || strings.TrimSpace(label) == "" || placeholderShape.MatchString(label)
}

// withdrawalPhrases name an option that ends the candidacy. Such an option is
// chosen only by an answer equal to it verbatim.
var withdrawalPhrases = []string{
	"withdraw my interest", "withdraw my application", "withdraw my candidacy", "withdraw from the process",
	"withdraw from consideration", "withdraw this application", "no longer wish to be considered",
	"no longer interested in this role", "no longer interested in the role", "remove me from consideration",
	"decline to proceed", "do not want to proceed",
}

var withdrawalTokens = func() [][]string {
	out := make([][]string, len(withdrawalPhrases))
	for i, p := range withdrawalPhrases {
		out[i] = tokenize(p)
	}
	return out
}()

// isWithdrawalLabel reports a label holding any withdrawal phrase as whole words.
func isWithdrawalLabel(label string) bool {
	toks := optTokens(label)
	if len(toks) == 0 {
		return false
	}
	for _, p := range withdrawalTokens {
		if optRunStart(toks, p) >= 0 {
			return true
		}
	}
	return false
}

// The decline vocabulary is one list for scoring, roles and stance reading: a
// refusal is recognized by meaning whichever wording a form uses.
var (
	declineLeads   = [][]string{{"decline", "to"}, {"prefer", "not", "to"}, {"choose", "not", "to"}, {"wish", "not", "to"}, {"do", "not", "wish", "to"}, {"do", "not", "want", "to"}, {"do", "not", "care", "to"}, {"do", "not", "choose", "to"}}
	declineObjects = [][]string{{"answer"}, {"disclose"}, {"share"}, {"state"}, {"say"}, {"self", "identify"}}
	declineWhole   = [][]string{{"not", "specified"}, {"no", "response"}}
	declineList    = buildDeclineList()
)

func buildDeclineList() [][]string {
	var out [][]string
	for _, l := range declineLeads {
		for _, o := range declineObjects {
			out = append(out, append(append([]string(nil), l...), o...))
		}
	}
	return append(out, declineWhole...)
}

// declineIn returns the decline phrase the text holds as whole words, or "".
func declineIn(text string) string {
	toks := optTokens(expandContractions(text))
	for _, p := range declineList {
		if optRunStart(toks, p) >= 0 {
			return strings.Join(p, " ")
		}
	}
	return ""
}

// isDeclineText reports a text that holds a decline phrase: a refusal is never
// read as a stance.
func isDeclineText(text string) bool { return declineIn(text) != "" }

// isDeclineAnswer reports an answer that is exactly a decline phrase (a leading
// "I" is ignored).
func isDeclineAnswer(text string) bool {
	toks := optTokens(expandContractions(text))
	if len(toks) > 1 && toks[0] == "i" {
		toks = toks[1:]
	}
	for _, p := range declineList {
		if len(p) == len(toks) && optRunStart(toks, p) == 0 {
			return true
		}
	}
	return false
}

// OptionsNamed lists the labels the text names, in the order the text names
// them. A label counts only where it stands whole: "Go" is not in "Google" and
// "C" is not in "C++". Case and runs of spaces do not matter.
func OptionsNamed(text string, labels []string) []string {
	fold := func(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }
	t := fold(text)
	joins := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) || r == '+' || r == '#' }
	type hit struct {
		at    int
		label string
	}
	var hits []hit
	seen := map[string]bool{}
	for _, l := range labels {
		f := fold(l)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		if at := phraseAt(t, f, func(b, a rune) bool { return !joins(b) && !joins(a) }); at >= 0 {
			hits = append(hits, hit{at, l})
		}
	}
	slices.SortStableFunc(hits, func(a, b hit) int { return a.at - b.at })
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.label
	}
	return out
}
