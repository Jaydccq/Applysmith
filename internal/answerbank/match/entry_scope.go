package match

import (
	"slices"
	"strings"
	"unicode"

	"applysmith/internal/lazyregexp"
)

// Scoped reports whether the entry is bound to captured scopes.
func (e Entry) Scoped() bool { return len(e.CapturedScope) > 0 }

// Valid reports whether the item names a host and a question. An item without
// either can never admit a question.
func (s ScopeItem) Valid() bool {
	return strings.TrimSpace(s.Host) != "" && strings.TrimSpace(s.Question) != ""
}

// Admits reports whether the entry may answer a question on host. An entry
// without a captured scope is always eligible. A scoped entry is eligible
// only when one valid scope item matches all three: the host (no suffix
// matching), the question text and the option labels as a set. An entry whose
// every item is invalid therefore answers nowhere: a scope that cannot be read
// never widens to "everywhere". question must already be sanitized.
func (e Entry) Admits(host, question string, options []string) bool {
	if !e.Scoped() {
		return true
	}
	h, q, o := scopeFoldHost(host), scopeFoldQuestion(question), scopeOptionSet(options)
	for _, s := range e.CapturedScope {
		if s.Valid() && scopeFoldHost(s.Host) == h && scopeFoldQuestion(s.Question) == q && slices.Equal(scopeOptionSet(s.Options), o) {
			return true
		}
	}
	return false
}

// scopeFoldHost folds a host the way the spec says: NFKC, trim, lowercase,
// whitespace collapsed, exact equality afterwards (no suffix matching).
func scopeFoldHost(s string) string {
	return strings.TrimSuffix(foldText(s), ".")
}

// scopeFoldQuestion folds a question text with the verbatim-example normalizer
// (lowercase, required marks removed, trailing punctuation stripped), so a
// required marker does not matter.
func scopeFoldQuestion(s string) string {
	s = normalizeExample(s)
	s = strings.TrimSpace(strings.TrimSuffix(s, "(required)"))
	return normalizeExample(s)
}

// scopeOptionSet folds option labels and compares them as a set.
func scopeOptionSet(options []string) []string {
	out := make([]string, 0, len(options))
	for _, o := range options {
		if o = foldText(o); o != "" {
			out = append(out, o)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// namedProgramRE asks about graduating from, completing or belonging to a
// programme ("a graduate of the RiseUp Program"); group 1 precedes the noun.
var (
	namedProgramRE = lazyregexp.New(`(?i)\b(?:graduat\w*|alumn\w*|completed|attended|participated|members?)\b(?:[^?.!]{0,30}?\b(?:of|from|in)\b)?\s+([^?!]{1,80}?)\s*\b(?:programs?|programmes?|bootcamps?|fellowships?|academy|apprenticeships?|cohorts?)\b`)
	programPunctRE = lazyregexp.New(`\([^)]*\)|[^\p{L}\p{N}'\s]`)
	programGeneric = strings.Fields("the our of and for degree graduate undergraduate bachelor's master's phd mba university college school internship")
)

// programNames: "our ..." or a proper name ("RiseUp") names a programme; "a
// degree program" none.
func programNames(text string) ([]string, bool) {
	m := namedProgramRE.FindStringSubmatch(text)
	if m == nil {
		return nil, false
	}
	words := strings.Fields(programPunctRE.ReplaceAllString(m[1], " "))
	upper := func(w string) bool { return unicode.IsUpper([]rune(w)[0]) }
	if len(words) == 0 || !upper(words[0]) && !slices.Contains([]string{"the", "our"}, words[0]) {
		return nil, false
	}
	own := strings.EqualFold(words[0], "our")
	var out []string
	for _, w := range words {
		if lw := strings.ToLower(w); !slices.Contains(programGeneric, lw) && (own || upper(w)) {
			out = append(out, lw)
		}
	}
	return out, own || len(out) > 0
}

// namesProgram: the entry's own words hold every word of the name.
func (e Entry) namesProgram(words []string) bool {
	hay := append([]string{e.ID, e.Description}, e.Examples...)
	for _, s := range e.CapturedScope {
		hay = append(hay, s.Question)
	}
	all := strings.ToLower(strings.Join(hay, " "))
	for _, w := range words {
		if wordIndex(all, w) < 0 {
			return false
		}
	}
	return len(words) > 0
}
