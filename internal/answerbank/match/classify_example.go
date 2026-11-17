package match

import (
	"slices"
	"strings"

	"applysmith/internal/lazyregexp"
)

// minCoverageTokens and minExampleCoverage define the curated long-example
// lane: a question that repeats at least 90 percent of a long example.
const (
	minCoverageTokens  = 12
	minExampleCoverage = 0.90
)

var (
	requiredGlyphs   = strings.NewReplacer("*", " ", string(rune(0x2731)), " ", string(rune(0x2732)), " ")
	trailingPunctRE  = lazyregexp.New(`[?:.!,;]$`)
	dateLayoutNameRE = lazyregexp.New(`(?i)\b(?:MM|DD|YYYY)(?:\s*[/.-]\s*(?:MM|DD|YYYY|YY)){1,2}\b`)
)

// normalizeExample is the verbatim comparison form: lowercase, required-marker
// glyphs as spaces, whitespace collapsed, one trailing punctuation mark removed.
func normalizeExample(s string) string {
	s = collapseSpaces(requiredGlyphs.Replace(strings.ToLower(s)))
	s = trailingPunctRE.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

// headingStripped returns the text after the first ": " (colon not at the
// start, tail non-empty) for the heading-stripped verbatim attempt.
func headingStripped(text string) (string, bool) {
	i := strings.Index(text, ": ")
	if i <= 0 {
		return "", false
	}
	tail := strings.TrimSpace(text[i+2:])
	if tail == "" {
		return "", false
	}
	return tail, true
}

// exampleOwners returns the entries (by index) among the candidates that list
// the normalized text as an example. An entry listing it twice counts once.
func exampleOwners(ix *classIndex, want string, include func(*Entry) bool) []int {
	if want == "" {
		return nil
	}
	var owners []int
	for i := range ix.entries {
		e := &ix.entries[i]
		if !e.classifiable() || !include(e) {
			continue
		}
		for _, v := range ix.examples[i] {
			if v.norm == want {
				owners = append(owners, i)
				break
			}
		}
	}
	return owners
}

// verbatimMatch finds the single entry that owns the question as an example.
// Two owners decide nothing; a hit the control cannot accept is no candidate.
// A heading-stripped attempt follows only when the whole text has no owner.
func verbatimMatch(ix *classIndex, text string, curated, headingToo bool, accepts func(*Entry) bool) (idx int, heading bool, ok bool) {
	entries := ix.entries
	include := func(e *Entry) bool { return e.curated() == curated }
	whole := normalizeExample(text)
	owners := exampleOwners(ix, whole, include)
	if len(owners) == 1 {
		if accepts(&entries[owners[0]]) {
			return owners[0], false, true
		}
		return 0, false, false
	}
	if len(owners) > 1 || !headingToo {
		return 0, false, false
	}
	tail, has := headingStripped(text)
	if !has {
		return 0, false, false
	}
	owners = exampleOwners(ix, normalizeExample(tail), include)
	if len(owners) == 1 && accepts(&entries[owners[0]]) {
		return owners[0], true, true
	}
	return 0, false, false
}

// longExampleMatch is the curated long-example lane: the question and an
// example each hold at least 12 distinct stemmed tokens and at least 90 percent
// of the example's tokens appear in the question. It decides only when exactly
// one entry qualifies.
func longExampleMatch(ix *classIndex, text string, accepts func(*Entry) bool) (idx int, coverage float64, ok bool) {
	entries := ix.entries
	qt := stemmedTokens(text)
	if len(qt) < minCoverageTokens {
		return 0, 0, false
	}
	qset := tokenSet(qt)
	winner, best := -1, 0.0
	for i := range entries {
		e := &entries[i]
		if !e.classifiable() || !e.curated() {
			continue
		}
		cov := 0.0
		for _, v := range ix.examples[i] {
			et := v.stems
			if len(et) < minCoverageTokens {
				continue
			}
			if c := float64(sharedCount(et, qset)) / float64(len(et)); c >= minExampleCoverage && c > cov {
				cov = c
			}
		}
		if cov == 0 {
			continue
		}
		if winner >= 0 {
			return 0, 0, false
		}
		winner, best = i, cov
	}
	if winner < 0 || !accepts(&entries[winner]) {
		return 0, 0, false
	}
	return winner, best, true
}

// SharedPhrasing is a wording an entry lists that other entries list too, as
// the entry writes it, with those entries' ids.
type SharedPhrasing struct {
	Phrasing string
	With     []string
}

// SharedPhrasings maps each entry id to the wordings it shares with other
// entries, compared as the exact-wording stages compare them: a wording two
// entries list decides nothing there, so a form asking it may get either
// answer, or none. Archived entries are never matched and are left out. An
// explanation entry (another entry's linked_explain_id) shares its generic
// "Please explain" with its siblings by design and is left out too: what
// routes it is the question it follows, not its wording.
func SharedPhrasings(entries []Entry) map[string][]SharedPhrasing {
	explains := map[string]bool{}
	for _, e := range entries {
		if e.LinkedExplainID != "" {
			explains[e.LinkedExplainID] = true
		}
	}
	owners := map[string][]string{}
	for _, e := range entries {
		if e.Archived() || explains[e.ID] {
			continue
		}
		seen := map[string]bool{}
		for _, ex := range e.Examples {
			if n := normalizeExample(ex); n != "" && !seen[n] {
				seen[n] = true
				owners[n] = append(owners[n], e.ID)
			}
		}
	}
	out := map[string][]SharedPhrasing{}
	for _, e := range entries {
		if e.Archived() || explains[e.ID] {
			continue
		}
		seen := map[string]bool{}
		for _, ex := range e.Examples {
			n := normalizeExample(ex)
			with := slices.DeleteFunc(slices.Clone(owners[n]), func(id string) bool { return id == e.ID })
			if len(with) > 0 && !seen[n] {
				seen[n] = true
				out[e.ID] = append(out[e.ID], SharedPhrasing{Phrasing: ex, With: with})
			}
		}
	}
	return out
}
