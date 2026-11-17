package match

import (
	"strings"
	"unicode"
)

// foldSpaces turns every Unicode space (no-break, ideographic, line and
// paragraph separators, BOM) and every control whitespace into a plain space.
// RE2's \s is ASCII only, so every pattern in this package reads folded text.
func foldSpaces(s string) string {
	ascii := true
	for i := 0; i < len(s) && ascii; i++ {
		ascii = s[i] >= ' ' && s[i] < 0x7f
	}
	if ascii {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == ' ' || r == 0xFEFF:
			b.WriteRune(' ')
		case r == 0x200B || r == 0x200C || r == 0x200D || r == 0x2060:
			// zero-width characters carry no text
		case unicode.IsSpace(r) || unicode.Is(unicode.Zs, r) || r == 0x2028 || r == 0x2029:
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

var apostrophes = strings.NewReplacer(string(rune(0x2019)), "'", string(rune(0x2018)), "'", string(rune(0x02BC)), "'", string(rune(0x2032)), "'", "`", "'")

// hyphens folds the in-word hyphens (hyphen, non-breaking hyphen, figure dash,
// minus) to ASCII: "H\u20111B" is "H-1B". En and em dashes stay; they part
// clauses.
var hyphens = strings.NewReplacer(string(rune(0x2010)), "-", string(rune(0x2011)), "-", string(rune(0x2012)), "-", string(rune(0x2212)), "-")

// collapseSpaces folds Unicode spaces, collapses every run to one space and
// trims.
func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(foldSpaces(s)), " ")
}

// routeText is the form every route pattern reads: Unicode spaces folded,
// curly apostrophes straight, whitespace collapsed.
func routeText(s string) string {
	return collapseSpaces(apostrophes.Replace(s))
}

// labelSaysOptional reports whether a label itself says the control is
// optional: "(optional)" anywhere or "optional:" as a prefix word.
func labelSaysOptional(label string) bool {
	l := strings.ToLower(label)
	return strings.Contains(l, "(optional)") || strings.Contains(l, "optional:")
}
