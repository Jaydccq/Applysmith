package documents

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// countWords state a count without a number. The noun after one is its unit:
// "hundreds of services" is about services, like "12 services".
var countWords = stringSet("hundreds", "thousands", "millions", "billions", "dozens", "tens", "scores", "several", "many", "numerous",
	"countless", "multiple", "few", "couple", "handful", "myriad")

// ofOnly words are plain nouns or verbs unless "of" follows.
var ofOnly = stringSet("tens", "scores")

// aOnly words state a count only after an article ("a couple", "a handful").
var aOnly = stringSet("couple", "handful")

// intensifiers state a degree and nothing a reader can check. The adjective
// and the adverb of one root are one word.
var intensifiers = map[string]string{
	"significant": "significant", "significantly": "significant",
	"dramatic": "dramatic", "dramatically": "dramatic",
	"massive": "massive", "massively": "massive",
	"substantial": "substantial", "substantially": "substantial",
	"drastic": "drastic", "drastically": "drastic",
	"vast": "vast", "vastly": "vast",
	"huge": "huge", "hugely": "huge",
	"enormous": "enormous", "enormously": "enormous",
	"tremendous": "tremendous", "tremendously": "tremendous",
	"exponential": "exponential", "exponentially": "exponential",
	"considerable": "considerable", "considerably": "considerable",
	"radical": "radical", "radically": "radical",
	"marked": "marked", "markedly": "marked",
	"greatly": "great", "sharply": "sharp",
}

// rankClaims are superlatives and firsts: nothing a reader can check unless a
// source says the same. Each pattern is the phrase that makes the claim, so an
// ordinary "first-party" or "kept a record of" is not one.
var rankClaims = []struct {
	word string
	re   *lazyregexp.Regexp
}{
	{"first", lazyregexp.New(`(?i)\b(?:(?:a|the) first\b|first[- ]ever\b|first of its kind\b|first to\b|first in the (?:company|team|industry)\b)`)},
	{"only", lazyregexp.New(`(?i)\b(?:the only\b|one and only\b|sole (?:engineer|owner|maintainer|developer)\b)`)},
	{"top", lazyregexp.New(`(?i)(?:#\s?1\b|\bno\.\s?1\b|\bnumber[- ](?:one|1)\b|\bnumber 1\b|\bnumber one\b)`)},
	{"record", lazyregexp.New(`(?i)\b(?:(?:new|all-time|company|team|personal) record\b|record[- ](?:breaking|setting|high|low|number|quarter|year)\b|set a record\b)`)},
	{"best", lazyregexp.New(`(?i)\b(?:best[- ]ever\b|best[- ]in[- ](?:class|company)\b|all-time best\b)`)},
}

// rankFigures finds the first, only, top-ranked, record and best-ever claims.
func rankFigures(s string) []figure {
	var out []figure
	for _, c := range rankClaims {
		for _, loc := range c.re.FindAllStringIndex(s, -1) {
			out = append(out, figure{q: quantity{vague: c.word, raw: s[loc[0]:loc[1]]}, start: loc[0], numEnd: loc[1], end: loc[1], lead: true})
		}
	}
	return out
}

// discourseNouns name the parts of the letter itself. A count of them ("two
// examples", "a few reasons") says how the letter is organized and measures
// nothing about the applicant's work.
var discourseNouns = stringSet("example", "point", "reason", "paragraph", "thing", "way", "item", "bullet", "section", "question", "result", "highlight",
	"outcome", "pair", "pass", "body", "sentence", "span", "requirement")

// partNoun: a part named after the figure or one modifier ("one measurable outcome").
func partNoun(s string, f figure) bool {
	ws := append(lastWords(s[f.numEnd:min(len(s), f.numEnd+40)], 9), "", "")
	part := func(w string) bool { return discourseNouns[singular(w)] || discourseNouns[lemma(w)] }
	return part(ws[0]) || !strings.HasSuffix(ws[0], "s") && englishEnding(ws[0]) && part(ws[1])
}

// possessives make "one" a pronoun ("the strongest one its own line").
var possessives = stringSet("its", "their", "his", "her", "my", "your", "our")

// ordinalSubjects follow an ordinal ("the first week") without being what it
// ranks: a position in the letter or in time.
var ordinalSubjects = stringSet("week", "day", "month", "year", "quarter", "paragraph", "section", "sentence", "line", "item", "bullet", "example", "point", "reason", "question", "step")

// discourseCount says whether a figure is a count of the letter's own parts or
// a count with no noun to measure: a vague or small count is a claim only when
// it is bound to a measurable subject. "A few services" and "two teams" are
// claims; "a few examples", "two reasons" and "a few are below" are not.
func discourseCount(s string, f figure) bool {
	q := f.q
	switch {
	case q.vague == "first":
		return discourseFirst(s, f)
	case countWords[q.vague]:
		return partNoun(s, f) || (q.unit == "" && !magnitudeBefore(s[:f.start]))
	case q.vague == "":
		// "Improved the score by 3 points" counts a measure, not a part of the
		// letter; "bullet 3" names one.
		p := append([]string{"", ""}, lastWords(s[:f.start], 2)...)
		p = p[len(p)-2:]
		if p[1] == "and" || p[1] == "to" {
			p[1] = p[0]
		}
		return discourseNouns[singular(p[1])] && strings.ContainsAny(q.raw, "0123456789") ||
			possessives[q.unit] || partNoun(s, f) && !(q.unit == "point" && strings.ContainsAny(q.raw, "0123456789"))
	}
	return false
}

// amountWords before a figure make it an amount rather than a count of
// things ("cut latency by a few").
var amountWords = stringSet("by", "than", "to", "from", "over", "about", "up", "down", "approximately", "nearly", "almost", "roughly", "more", "less")

func magnitudeBefore(prefix string) bool {
	w := lastWords(prefix, 1)
	return len(w) == 1 && amountWords[w[0]]
}

func lastWords(s string, n int) []string {
	ws := tokenRE.FindAllString(s, -1)
	if len(ws) > n {
		ws = ws[len(ws)-n:]
	}
	for i := range ws {
		ws[i] = strings.ToLower(ws[i])
	}
	return ws
}

var (
	wordAfterRE    = lazyregexp.New(`^\s+(\p{L}+)`)
	articleFirstRE = lazyregexp.New(`(?i)^(?:a|the) first$`)
)

// discourseFirst is true for "the first" that ranks a part of the letter or a
// stretch of time ("the first week") or stands alone as a pointer ("the first
// is below"). "I was the first." and "the first to ship" are rank claims.
func discourseFirst(s string, f figure) bool {
	if !articleFirstRE.MatchString(f.q.raw) {
		return false
	}
	copula := copulaBefore(s[:f.start])
	rest := s[f.end:]
	if m := wordAfterRE.FindStringSubmatch(rest); m != nil {
		w := m[1]
		return ordinalSubjects[singular(strings.ToLower(w))] || (auxiliaries[strings.ToLower(w)] && !copula)
	}
	loc := breakRE.FindStringIndex(rest)
	return !copula && (strings.TrimSpace(rest) == "" || (loc != nil && loc[0] == 0))
}

// copulaBefore says whether the text ends in a word that makes a bare "the
// first" a predicate about the applicant ("I was the first").
func copulaBefore(prefix string) bool {
	w := lastWords(prefix, 1)
	return len(w) == 1 && (auxiliaries[w[0]] || w[0] == "became" || w[0] == "become" || w[0] == "as" || w[0] == "being")
}

// vagueFigures finds the counts and degrees a claim states without a number.
func vagueFigures(s string) []figure {
	toks := wordToks(s)
	out := rankFigures(s)
	for i, t := range toks {
		if t.joined || (i+1 < len(toks) && toks[i+1].joined) {
			continue
		}
		prev, next := "", ""
		if i > 0 {
			prev = toks[i-1].w
		}
		if i+1 < len(toks) {
			next = toks[i+1].w
		}
		switch {
		case (t.w == "order" || t.w == "orders") && next == "of" && i+2 < len(toks) && toks[i+2].w == "magnitude":
			out = append(out, figure{q: quantity{vague: "magnitude", raw: strings.TrimSpace(s[t.start:toks[i+2].end])}, start: t.start, numEnd: toks[i+2].end, end: toks[i+2].end, lead: true})
		case intensifiers[t.w] != "":
			out = append(out, figure{q: quantity{vague: intensifiers[t.w], raw: t.w}, start: t.start, numEnd: t.end, end: t.end, lead: true})
		case countWords[t.w]:
			if (ofOnly[t.w] && next != "of") || (aOnly[t.w] && prev != "a") {
				continue
			}
			fig := figure{q: quantity{vague: t.w, raw: t.w}, start: t.start, numEnd: t.end, end: t.end}
			if prev == "a" && (t.w == "few" || aOnly[t.w]) {
				fig.start, fig.q.raw = toks[i-1].start, "a "+t.w
			}
			j := i + 1
			for j < len(toks) && (toks[j].w == "of" || toks[j].w == "the" || toks[j].w == "a" || toks[j].w == "an") {
				j++
			}
			if j < len(toks) && !unitStop[toks[j].w] && !toks[j].joined && !breakRE.MatchString(s[t.end:toks[j].start]) {
				u, _ := unitOf(toks[j].w)
				fig.q.unit, fig.end = u, toks[j].end
			}
			out = append(out, fig)
		}
	}
	return out
}

var modRE = lazyregexp.New(`(?i)(~|\b(approximately|approx|roughly|about|around|circa|nearly|almost|over|under|more than|less than|fewer than|up to|at least|at most|exceeding|upwards of))\s*$`)

var modClass = map[string]string{"~": "~", "approximately": "~", "approx": "~", "roughly": "~", "about": "~", "around": "~", "circa": "~",
	"nearly": "<", "almost": "<", "under": "<", "less than": "<", "fewer than": "<", "up to": "<", "at most": "<",
	"over": ">", "more than": ">", "at least": ">", "exceeding": ">", "upwards of": ">"}

// modBefore reads the approximation word right before a figure.
func modBefore(prefix string) string {
	m := modRE.FindStringSubmatch(strings.TrimRight(prefix, " "))
	if m == nil {
		return ""
	}
	return modClass[strings.ToLower(m[1])]
}

// approxWords are skipped, not read as the subject, when a clause is scanned.
var approxWords = stringSet("approximately", "approx", "roughly", "about", "around", "circa", "nearly", "almost", "over", "under", "exceeding",
	"upwards", "least", "most", "fewer", "than")
