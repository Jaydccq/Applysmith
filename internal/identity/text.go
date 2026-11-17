package identity

import (
	"strings"
	"unicode"
)

// foldTable maps accented Latin letters to ASCII. Escapes keep the source
// ASCII-only.
var foldTable = func() map[rune]string {
	groups := map[string]string{
		"a":  "àáâãäåāăą",
		"c":  "çćĉċč",
		"d":  "ďđ",
		"e":  "èéêëēĕėęě",
		"g":  "ĝğġģ",
		"h":  "ĥħ",
		"i":  "ìíîïĩīĭįı",
		"j":  "ĵ",
		"k":  "ķ",
		"l":  "ĺļľŀł",
		"n":  "ñńņň",
		"o":  "òóôõöøōŏő",
		"r":  "ŕŗř",
		"s":  "śŝşš",
		"t":  "ţťŧ",
		"u":  "ùúûüũūŭůűų",
		"w":  "ŵ",
		"y":  "ýÿŷ",
		"z":  "źżž",
		"ss": "ß",
		"ae": "æ",
		"oe": "œ",
	}
	m := map[rune]string{}
	for ascii, letters := range groups {
		for _, r := range letters {
			m[r] = ascii
		}
	}
	return m
}()

// fold lowercases s, maps accented Latin letters to ASCII and drops combining
// marks, so "Cafe" and its accented spelling compare equal.
func fold(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.Is(unicode.Mn, r):
		case foldTable[r] != "":
			b.WriteString(foldTable[r])
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// words splits already-folded text into runs of letters and digits.
func words(folded string) []string {
	return strings.FieldsFunc(folded, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func wordSet(ws []string) map[string]bool {
	m := make(map[string]bool, len(ws))
	for _, w := range ws {
		m[w] = true
	}
	return m
}

// compact keeps only letters and digits of the folded text.
func compact(s string) string { return strings.Join(words(fold(s)), "") }

// Fold is the exported form of fold, for packages that compare names the way
// identity does.
func Fold(s string) string { return fold(s) }
