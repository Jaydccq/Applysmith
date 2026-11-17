package documents

import (
	"strings"
	"unicode"
)

// dashes are the Unicode hyphen and dash variants.
func isDash(r rune) bool {
	switch r {
	case 0x2010, 0x2011, 0x2012, 0x2013, 0x2014, 0x2015, 0x2212:
		return true
	}
	return false
}

func isInvisible(r rune) bool {
	switch r {
	case 0x200b, 0x200c, 0x200d, 0x2060, 0xfeff, 0x00ad:
		return true
	}
	return false
}

func foldRune(r rune) (string, bool) {
	switch r {
	case 0x2018, 0x2019, 0x201a, 0x201b:
		return "'", true
	case 0x201c, 0x201d, 0x201e, 0x201f:
		return `"`, true
	case 0x2026:
		return "...", true
	case 0x00a0, 0x202f, 0x2007:
		return " ", true
	}
	return "", false
}

// Normalize makes structured text ATS-safe before it reaches a template:
// dash variants in ranges become one ASCII hyphen, curly quotes become
// straight, an ellipsis becomes three periods, zero-width characters go and a
// nonbreaking space becomes an ordinary one. Accents and other letters stay.
// It is idempotent and works on text, never on markup.
func Normalize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case isInvisible(r):
		default:
			if f, ok := foldRune(r); ok {
				b.WriteString(f)
			} else {
				b.WriteRune(r)
			}
		}
	}
	return strings.TrimSpace(collapseSpaces(rangeDashes(b.String())))
}

// rangeDashes turns a dash variant between a digit and a digit or letter,
// with optional spaces around it, into one ASCII hyphen ("2019 - Present").
func rangeDashes(s string) string {
	rs := []rune(s)
	var out []rune
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if !isDash(r) {
			out = append(out, r)
			continue
		}
		j := i + 1
		for j < len(rs) && (rs[j] == ' ' || rs[j] == '\t') {
			j++
		}
		k := len(out)
		for k > 0 && (out[k-1] == ' ' || out[k-1] == '\t') {
			k--
		}
		if k > 0 && out[k-1] >= '0' && out[k-1] <= '9' && j < len(rs) && (unicode.IsLetter(rs[j]) || unicode.IsDigit(rs[j])) {
			out = append(out[:k], '-')
			i = j - 1
			continue
		}
		out = append(out, r)
	}
	return string(out)
}

func collapseSpaces(s string) string {
	var b strings.Builder
	prev := false
	for _, r := range s {
		sp := r == ' ' || r == '\t'
		if sp && prev {
			continue
		}
		prev = sp
		b.WriteRune(r)
	}
	return b.String()
}
