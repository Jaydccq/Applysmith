package documents

import (
	"sort"
	"strings"

	"applysmith/internal/kb"
)

// A quantity is any token of a claim that states a magnitude, a count, a ratio
// or an approximation: a number, a number word, a fraction, a -fold form, a
// dozen, "hundreds of", "several", an intensifier such as "significantly". A
// quantity passes only when one evidence line holds the same value (or the
// same word), with the same unit, the same rate and approximation and the same
// subject. Anything the extractor cannot place is still a quantity, and an
// unplaced quantity is rejected.
type quantity struct {
	value float64
	unit  string
	// scale is the factor a duration unit applied to value (hours to ms).
	scale float64
	raw   string
	// vague is the canonical word of a magnitude that carries no number
	// ("hundreds", "several", "significant"); value is 0 then.
	vague string
	// mod is the approximation on the figure: "~" about, "<" nearly, ">" over.
	mod string
	// per is the period of a rate: "year" for "a year", "per year", "annual".
	per string
	// head is the noun the figure is about; mods are the words that qualify it.
	head string
	mods []string
	// verbs are the verb classes of the clause before the figure.
	verbs []string
	// dir is -1 for a reduction, +1 for an increase and 0 when the text says
	// neither.
	dir int
	// post holds the lemmas right after the figure, ctx every lemma of the
	// clause around it, line every lemma and verb class of the whole text.
	post, ctx, line map[string]bool
}

// figure is one quantity found in a text with the offsets that bind it. A lead
// figure is itself the predicate ("halved", "significantly"), so its subject
// follows it.
type figure struct {
	q                  quantity
	start, numEnd, end int
	lead               bool
}

// quantities returns every quantity of s with its unit and subject.
func quantities(s string) []quantity {
	figs := append(digitFigures(s), wordFigures(s)...)
	figs = append(figs, vagueFigures(s)...)
	sort.SliceStable(figs, func(i, j int) bool { return figs[i].start < figs[j].start })
	var kept []figure
	for _, f := range figs {
		if len(kept) == 0 || f.start >= kept[len(kept)-1].end {
			kept = append(kept, f)
		}
	}
	// A count of the letter's own parts measures nothing.
	n := 0
	for _, f := range kept {
		if !discourseCount(s, f) {
			kept[n] = f
			n++
		}
	}
	kept = kept[:n]
	// "from 12 to 4 minutes": one unit closes both figures of a range.
	for i := 0; i+1 < len(kept); i++ {
		a, b := &kept[i].q, kept[i+1].q
		if a.unit == "" && a.vague == "" && measure(b.unit) && rangeGap(s[kept[i].end:kept[i+1].start]) {
			a.unit = b.unit
			if b.scale > 0 {
				a.value, a.scale = a.value*b.scale, b.scale
			}
		}
	}
	line := lineLemmas(s)
	out := make([]quantity, len(kept))
	bare := make([]bool, len(kept))
	for i, f := range kept {
		prev, next := 0, len(s)
		if i > 0 {
			prev = kept[i-1].end
		}
		if i+1 < len(kept) {
			next = kept[i+1].start
		}
		out[i], bare[i] = bind(f, s, prev, next)
		out[i].line = line
		if out[i].mod == "" {
			out[i].mod = modBefore(s[:f.start])
		}
	}
	// "from 5 h to 20 min": the second figure of a pair is about what the
	// first one is about.
	for i := 1; i < len(out); i++ {
		if bare[i] && pairGap(s[kept[i-1].end:kept[i].start]) && !counted(out[i].unit) && !counted(out[i-1].unit) {
			out[i].head, out[i].mods = out[i-1].head, out[i-1].mods
			out[i].verbs = append(out[i].verbs, out[i-1].verbs...)
			if out[i].dir == 0 {
				out[i].dir = out[i-1].dir
			}
			for k := range out[i-1].ctx {
				out[i].ctx[k] = true
			}
		}
	}
	return out
}

func rangeGap(gap string) bool {
	switch strings.ToLower(strings.TrimSpace(gap)) {
	case "to", "-", "\u2013", "through", "and", "or":
		return true
	}
	return false
}

func measure(u string) bool { return u == "%" || u == "x" || u == "time" }

func pairGap(gap string) bool {
	for _, w := range tokenRE.FindAllString(gap, -1) {
		if !pairWords[strings.ToLower(w)] {
			return false
		}
	}
	return !breakRE.MatchString(strings.ReplaceAll(gap, " - ", " "))
}

var pairWords = stringSet("to", "from", "and", "or", "vs", "versus", "into", "through", "down", "up", "then")

// lineLemmas holds every lemma of a text and the verb class of each verb.
func lineLemmas(s string) map[string]bool {
	out := map[string]bool{}
	prevEnd, prevWord := 0, ""
	for _, loc := range tokenRE.FindAllStringIndex(s, -1) {
		word := strings.ToLower(s[loc[0]:loc[1]])
		if l := lemma(word); l != "" {
			out[l] = true
		}
		atStart := prevWord == "" || prevWord == "to" || conjWords[prevWord] || breakRE.MatchString(s[prevEnd:loc[0]])
		if verbLike(word, atStart, prevWord == "to") {
			out["v:"+verbKey(word)] = true
		}
		prevEnd, prevWord = loc[1], word
	}
	return out
}

// corpus is the set of evidence quantities a claim may rest on, with the
// words of the user's own sources that may qualify a subject.
type corpus struct {
	qs    []quantity
	vocab map[string]bool
	// summary lets a claim without a named subject rest on any evidence line
	// that holds its figure and verb class.
	summary bool
}

// corpusQuantities reads the evidence texts; vocab are the user's sources
// whose words may qualify a subject (the evidence texts when nil).
func corpusQuantities(texts, vocab []string) corpus {
	c := corpus{vocab: map[string]bool{}}
	for _, t := range texts {
		c.qs = append(c.qs, quantities(t)...)
	}
	if vocab == nil {
		vocab = texts
	}
	for _, t := range vocab {
		for _, l := range kb.Tokens(t) {
			c.vocab[l] = true
		}
	}
	return c
}

// checkClaims fails every quantified claim of text that the corpus lacks.
func checkClaims(g *Gate, where, text string, c corpus) {
	// Bold marks the metric of a bullet ("**forty** product teams"); the
	// markers are not words between a figure and its noun.
	for _, q := range quantities(strings.ReplaceAll(text, "**", "")) {
		if !supported(q, c) {
			g.fail("unsupported-figure", "%s: %q is not in the selected evidence", where, q.raw)
		}
	}
}
