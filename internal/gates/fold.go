// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// confusables maps lookalike letters to the Latin letter they imitate, after
// lowercasing. Compatibility decomposition already unifies full-width forms
// and ligatures; these have no decomposition. Escapes keep the source ASCII.
var confusables = map[rune]rune{
	'\u0131': 'i', // dotless i
	'\u017f': 's', // long s
	'\u0430': 'a', '\u0432': 'b', '\u0435': 'e', '\u043a': 'k', '\u043c': 'm', '\u043d': 'h',
	'\u043e': 'o', '\u0440': 'p', '\u0441': 'c', '\u0442': 't', '\u0443': 'y', '\u0445': 'x',
	'\u0455': 's', '\u0456': 'i', '\u0458': 'j', '\u04bb': 'h', '\u0501': 'd', '\u051b': 'q', '\u051d': 'w',
	'\u03b1': 'a', '\u03b2': 'b', '\u03b5': 'e', '\u03b9': 'i', '\u03ba': 'k', '\u03bd': 'v',
	'\u03bf': 'o', '\u03c1': 'p', '\u03c4': 't', '\u03c5': 'u', '\u03c7': 'x',
	'\u03b7': 'n', '\u03bc': 'u', '\u03c9': 'w', '\u03b3': 'y', '\u0475': 'v', '\u0251': 'a', '\u0261': 'g',
	// Armenian
	'\u0561': 'a', '\u0566': 'q', '\u0570': 'h', '\u0578': 'n', '\u057d': 'u', '\u0581': 'g', '\u0585': 'o',
	// Cherokee, matched in either case: lowercasing moves these letters.
	'\u13aa': 'a', '\u13f4': 'b', '\u13df': 'c', '\u13a0': 'd', '\u13ac': 'e', '\u13bb': 'h', '\u13c6': 'i',
	'\u13ab': 'j', '\u13e6': 'k', '\u13de': 'l', '\u13b7': 'm', '\u13e2': 'p', '\u13da': 's', '\u13a2': 't',
	'\u13d9': 'v', '\u13b3': 'w', '\u13c3': 'z',
	'\u2019': '\'', '\u2018': '\'', '\u02bc': '\'',
}

// foldText is the form every label and message is read in: compatibility and
// width forms unified, accents and format characters (zero-width, soft hyphen,
// direction marks) dropped, lookalike letters mapped to Latin, lowercased and
// white space collapsed. A control or message cannot dodge a word match by
// spelling it with lookalikes.
func foldText(s string) string {
	s = norm.NFKD.String(strings.ToValidUTF8(s, " "))
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Cf, r):
			continue
		case unicode.IsSpace(r) || unicode.IsControl(r):
			b.WriteByte(' ')
			continue
		}
		if m, ok := confusables[r]; ok {
			r = m
		} else if r = unicode.ToLower(r); confusables[r] != 0 {
			r = confusables[r]
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// scriptTables are the alphabets a label is read against; a letter of none of
// them counts as one more script of its own.
var scriptTables = []*unicode.RangeTable{unicode.Latin, unicode.Cyrillic, unicode.Greek, unicode.Armenian, unicode.Cherokee}

// hasMixedScriptWord reports whether any single word of s spells its letters
// from two or more alphabets. No honest label does, and a lookalike the map
// does not know yet would otherwise pass for the Latin word it imitates, so
// the caller fails closed instead of trusting the fold.
func hasMixedScriptWord(s string) bool {
	s = norm.NFKD.String(strings.ToValidUTF8(s, " "))
	first, inWord := -1, false
	for _, r := range s {
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		if !unicode.IsLetter(r) {
			first, inWord = -1, false
			continue
		}
		sc := len(scriptTables)
		for i, t := range scriptTables {
			if unicode.Is(t, r) {
				sc = i
				break
			}
		}
		switch {
		case !inWord:
			first, inWord = sc, true
		case sc != first:
			return true
		}
	}
	return false
}
