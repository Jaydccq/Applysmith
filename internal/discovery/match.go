package discovery

import (
	"regexp"
	"strings"

	"applysmith/internal/lazyregexp"
)

// term matches case-insensitively on whole words. With suffixes it also
// matches the plural and gerund forms (s, es, ing); a multi-word term matches
// as a phrase with flexible whitespace, except a positive title term
// (newEveryWordTerm). A substring inside a longer word never counts.
type term struct {
	text string
	re   *lazyregexp.Regexp
	// each, when set, holds one whole-word pattern per word, all of which
	// must match (newEveryWordTerm); re is then unused.
	each []*lazyregexp.Regexp
}

func newTerm(text string, suffixes bool) (term, bool) {
	words := strings.Fields(text)
	if len(words) == 0 {
		return term{}, false
	}
	for i, w := range words {
		words[i] = regexp.QuoteMeta(w)
	}
	expr := `(?i)(?:^|[^\p{L}\p{N}])` + strings.Join(words, `\s+`)
	if suffixes {
		expr += `(?:s|es|ing)?`
	}
	expr += `(?:$|[^\p{L}\p{N}])`
	return term{text: strings.Join(strings.Fields(text), " "), re: lazyregexp.New(expr)}, true
}

// newEveryWordTerm is a term of the title's positive list: each of its words
// must appear as a whole word, in any order and with anything between, so
// "Backend Engineer" matches "Software Engineer, Backend" and "Backend/API
// Engineer". A one-word term is newTerm's.
func newEveryWordTerm(text string, suffixes bool) (term, bool) {
	words := strings.Fields(text)
	if len(words) < 2 {
		return newTerm(text, suffixes)
	}
	t := term{text: strings.Join(words, " ")}
	for _, w := range words {
		one, _ := newTerm(w, suffixes)
		t.each = append(t.each, one.re)
	}
	return t, true
}

func (t term) match(s string) bool {
	if t.each != nil {
		for _, re := range t.each {
			if !re.MatchString(s) {
				return false
			}
		}
		return true
	}
	return t.re.MatchString(s)
}

func compileTerms(texts []string, suffixes bool) []term {
	return compileWith(newTerm, texts, suffixes)
}

func compileWith(compile func(string, bool) (term, bool), texts []string, suffixes bool) []term {
	var out []term
	for _, s := range texts {
		if t, ok := compile(s, suffixes); ok {
			out = append(out, t)
		}
	}
	return out
}

func anyMatch(terms []term, s string) (string, bool) {
	for _, t := range terms {
		if t.match(s) {
			return t.text, true
		}
	}
	return "", false
}
