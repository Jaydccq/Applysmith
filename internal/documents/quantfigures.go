package documents

import (
	"strconv"
	"strings"
	"unicode"

	"applysmith/internal/kb"
	"applysmith/internal/lazyregexp"
)

var (
	numRE   = lazyregexp.New(`([$\x{20ac}\x{a3}])?\s?(\d+(?:,\d{3})*(?:\.\d+)?)`)
	tokenRE = lazyregexp.New(`[A-Za-z]+(?:-[A-Za-z]+)*`)
	// breakRE ends a clause: punctuation and a spaced dash.
	breakRE = lazyregexp.New(`[,;:.()!?\n\[\]]|\s[-\x{2013}\x{2014}]\s`)

	unitStop = stringSet("and", "or", "in", "of", "to", "for", "with", "by", "from", "the", "a", "an", "on", "at", "as", "is", "are", "was",
		"were", "across", "over", "into", "using", "via", "that", "which", "who", "while", "after", "before", "when", "than", "within", "per",
		"through", "throughout", "around", "among", "during", "under", "without", "since", "until", "between", "because", "but", "so", "then",
		"where", "including", "like")

	// numberWords are the spelled numbers zero to nineteen and the tens twenty to ninety.
	numberWords = func() map[string]float64 {
		words := map[string]float64{}
		for n, w := range strings.Fields("zero one two three four five six seven eight nine ten eleven twelve thirteen fourteen fifteen sixteen seventeen eighteen nineteen") {
			words[w] = float64(n)
		}
		for n, w := range strings.Fields("twenty thirty forty fifty sixty seventy eighty ninety") {
			words[w] = float64(20 + 10*n)
		}
		return words
	}()
	magnitudes = map[string]float64{"hundred": 100, "thousand": 1e3, "million": 1e6, "billion": 1e9}
	// suffixMagnitudes scale a digit number: 5K, 2.5M, 3 billion.
	suffixMagnitudes = map[string]float64{"k": 1e3, "thousand": 1e3, "m": 1e6, "mn": 1e6, "million": 1e6, "b": 1e9, "bn": 1e9, "billion": 1e9}
	// ratioWords are a figure in one word: "halved" is 0.5x, "doubled" 2x.
	ratioWords = map[string]float64{"halved": 0.5, "halving": 0.5, "doubled": 2, "doubling": 2, "double": 2, "twice": 2, "tripled": 3, "tripling": 3,
		"triple": 3, "thrice": 3, "quadrupled": 4, "quadruple": 4}
	// fractions are a share of a whole; they count as a ratio.
	fractions = map[string]float64{"half": 0.5, "halves": 0.5, "third": 1.0 / 3, "thirds": 1.0 / 3, "quarter": 0.25, "quarters": 0.25,
		"fifth": 0.2, "fifths": 0.2, "tenth": 0.1, "tenths": 0.1}
	// loneOneNext marks "one" as a pronoun or an article, not a count.
	loneOneNext = stringSet("of", "or", "another", "more", "can", "should", "could", "would", "will", "that", "which", "who", "is", "was",
		"has", "had", "and", "in", "on", "to", "for", "with", "by", "at", "as", "the", "a", "an", "may", "might", "must", "else", "thing",
		"off", "time", "click", "stop", "way", "size")
	loneOnePrev = stringSet("no", "any", "each", "every", "this", "that", "which", "whichever", "the", "at", "any")

	// fixedUnits fold the spellings of one unit to one: percent, durations
	// (scaled to milliseconds), calendar spans, currencies and ratios.
	fixedUnits = map[string]struct {
		unit  string
		scale float64
	}{
		"%": {"%", 1}, "percent": {"%", 1}, "pct": {"%", 1}, "percentage": {"%", 1},
		"ms": {"time", 1}, "msec": {"time", 1}, "msecs": {"time", 1}, "millisecond": {"time", 1}, "milliseconds": {"time", 1},
		"s": {"time", 1e3}, "sec": {"time", 1e3}, "secs": {"time", 1e3}, "second": {"time", 1e3}, "seconds": {"time", 1e3},
		"min": {"time", 6e4}, "mins": {"time", 6e4}, "minute": {"time", 6e4}, "minutes": {"time", 6e4},
		"h": {"time", 3.6e6}, "hr": {"time", 3.6e6}, "hrs": {"time", 3.6e6}, "hour": {"time", 3.6e6}, "hours": {"time", 3.6e6},
		"day": {"day", 1}, "days": {"day", 1}, "week": {"week", 1}, "weeks": {"week", 1}, "wk": {"week", 1}, "wks": {"week", 1},
		"month": {"month", 1}, "months": {"month", 1}, "mo": {"month", 1}, "mos": {"month", 1},
		"year": {"year", 1}, "years": {"year", 1}, "yr": {"year", 1}, "yrs": {"year", 1},
		"dollar": {"$", 1}, "dollars": {"$", 1}, "usd": {"$", 1}, "euro": {"€", 1}, "euros": {"€", 1}, "eur": {"€", 1}, "gbp": {"£", 1},
		"x": {"x", 1}, "times": {"x", 1}, "fold": {"x", 1},
	}
)

func stringSet(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

func singular(w string) string {
	switch {
	case strings.HasSuffix(w, "ies") && len(w) > 4:
		return w[:len(w)-3] + "y"
	case strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && len(w) > 3:
		return w[:len(w)-1]
	}
	return w
}

func lemma(w string) string {
	if t := kb.Tokens(w); len(t) > 0 {
		return t[0]
	}
	return ""
}

// unitOf is the canonical unit a word names and the factor to its base.
func unitOf(w string) (string, float64) {
	if u, ok := fixedUnits[w]; ok {
		return u.unit, u.scale
	}
	return singular(w), 1
}

// suffixWord reads the word that follows a number, glued to it or after one
// space or hyphen; a percent sign counts as a word.
func suffixWord(s string, pos int) (string, int) {
	i := pos
	if i < len(s) && (s[i] == ' ' || s[i] == '-') {
		i++
	}
	if i < len(s) && s[i] == '%' {
		return "%", i + 1
	}
	j := i
	for j < len(s) && (s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z') {
		j++
	}
	if j == i {
		return "", pos
	}
	return strings.ToLower(s[i:j]), j
}

// identifierDigits are digits inside a name (p99, S3, ISO-27001) or a version
// (3.9.1), which no claim counts.
func identifierDigits(s string, at int) bool {
	if at == 0 {
		return false
	}
	p := rune(s[at-1])
	switch {
	case unicode.IsLetter(p):
		return true
	case p == '.' && at >= 2 && s[at-2] >= '0' && s[at-2] <= '9':
		return true
	case p == '-' && at >= 2 && unicode.IsLetter(rune(s[at-2])):
		return true
	}
	return false
}

// digitFigures finds every digit number with its unit: a magnitude suffix
// scales the value (5K is 5000), a percent sign or word is the unit "%", a
// currency sign is the unit of the amount, a duration is scaled to
// milliseconds, "+" is an approximation.
func digitFigures(s string) []figure {
	var out []figure
	for _, loc := range numRE.FindAllStringSubmatchIndex(s, -1) {
		if identifierDigits(s, loc[4]) {
			continue
		}
		v, err := strconv.ParseFloat(strings.ReplaceAll(s[loc[4]:loc[5]], ",", ""), 64)
		if err != nil {
			continue
		}
		unit, mod, cur := "", "", loc[5]
		if loc[2] >= 0 {
			unit = s[loc[2]:loc[3]]
		}
		plus := func() {
			if cur < len(s) && s[cur] == '+' {
				cur++
				mod = ">"
			}
		}
		plus()
		w, next := suffixWord(s, cur)
		if w == "per" {
			if w2, n2 := suffixWord(s, next); w2 == "cent" {
				w, next = "percent", n2
			}
		}
		if mg, ok := suffixMagnitudes[w]; ok {
			v *= mg
			cur = next
			plus()
			w, next = suffixWord(s, cur)
		}
		numEnd, end := cur, cur
		scale := 0.0
		switch {
		case unit != "" || w == "":
		case fixedUnits[w].unit != "":
			u, sc := unitOf(w)
			unit, v, cur = u, v*sc, next
			if u == "time" {
				scale = sc
			}
			plus()
			numEnd, end = cur, cur
		case rateWords[w] != "":
			// "2.5M daily requests": the rate is not the unit.
			if w2, n2 := suffixWord(s, next); w2 != "" && !unitStop[w2] && !pastVerb(w2) {
				unit, end = singular(w2), n2
			}
		case !unitStop[w] && !pastVerb(w):
			unit, end = singular(w), next
		}
		out = append(out, figure{q: quantity{value: v, unit: unit, scale: scale, mod: mod, raw: strings.TrimSpace(s[loc[0]:end])}, start: loc[0], numEnd: numEnd, end: end})
	}
	return out
}

// wordTok is one word of a text, or one hyphen part of a compound.
type wordTok struct {
	w          string
	start, end int
	joined     bool // hyphen-joined to the previous part
}

func wordToks(s string) []wordTok {
	var out []wordTok
	for _, loc := range tokenRE.FindAllStringIndex(s, -1) {
		pos := loc[0]
		for i, p := range strings.Split(s[loc[0]:loc[1]], "-") {
			out = append(out, wordTok{w: strings.ToLower(p), start: pos, end: pos + len(p), joined: i > 0})
			pos += len(p) + 1
		}
	}
	return out
}

// foldNumber reads "tenfold", "fivefold" and "twofold" as a ratio.
func foldNumber(w string) (float64, bool) {
	if p, ok := strings.CutSuffix(w, "fold"); ok && p != "" {
		if v, ok := numberWords[p]; ok && v > 0 {
			return v, true
		}
	}
	return 0, false
}

// wordFigures finds ratio words, fractions and number words: "halved", "a
// third", "fivefold", "a dozen", "forty percent", "twenty-five engineers",
// "two hundred", "three-step".
func wordFigures(s string) []figure {
	toks := wordToks(s)
	var out []figure
	for i := 0; i < len(toks); {
		t := toks[i]
		hasNext := i+1 < len(toks)
		if v, ok := ratioWords[t.w]; ok && !t.joined && !(hasNext && toks[i+1].joined) {
			out = append(out, figure{q: quantity{value: v, unit: "x", raw: t.w, dir: dirOf(t.w)}, start: t.start, numEnd: t.end, end: t.end, lead: true})
			i++
			continue
		}
		if v, ok := foldNumber(t.w); ok {
			out = append(out, figure{q: quantity{value: v, unit: "x", raw: t.w}, start: t.start, numEnd: t.end, end: t.end, lead: true})
			i++
			continue
		}
		if f, n, ok := fractionAt(s, toks, i); ok {
			out = append(out, f)
			i += n
			continue
		}
		if t.w == "dozen" && !t.joined && i > 0 && toks[i-1].w == "a" {
			sp := figure{q: quantity{value: 12}, start: toks[i-1].start, numEnd: t.end, end: t.end}
			sp = unitAfter(s, toks, i+1, sp)
			sp.q.raw = strings.TrimSpace(s[sp.start:sp.end])
			out = append(out, sp)
			i++
			continue
		}
		val, n := numberRun(s, toks[i:])
		if n == 0 {
			i++
			continue
		}
		last := toks[i+n-1]
		if n == 1 && val == 1 && t.w == "one" && !countedOne(toks, i) {
			i += n
			continue
		}
		if magnitudes[t.w] > 0 && (i == 0 || toks[i-1].w != "a") {
			i += n
			continue
		}
		sp := figure{q: quantity{value: val}, start: t.start, numEnd: last.end, end: last.end}
		used := n
		if j := i + n; j < len(toks) {
			nx := toks[j]
			adjacent := strings.TrimSpace(s[last.end:nx.start]) == "" || (nx.joined && nx.start == last.end+1)
			frac, isFrac := fractions[nx.w]
			switch {
			case !adjacent:
			case isFrac && !nx.joined:
				sp.q.value, sp.q.unit, sp.end, sp.lead = val*frac, "x", nx.end, true
				sp.numEnd, used = nx.end, n+1
			case nx.w == "dozen" && !nx.joined:
				sp.q.value, sp.numEnd, sp.end, used = val*12, nx.end, nx.end, n+1
				sp = unitAfter(s, toks, j+1, sp)
			case nx.w == "per" && j+1 < len(toks) && toks[j+1].w == "cent":
				sp.q.unit, sp.end, sp.numEnd, used = "%", toks[j+1].end, toks[j+1].end, n+2
			case fixedUnits[nx.w].unit != "":
				u, sc := unitOf(nx.w)
				sp.q.unit, sp.q.value, sp.end, sp.numEnd = u, val*sc, nx.end, nx.end
				if u == "time" {
					sp.q.scale = sc
				}
				used = n + 1
			case nx.joined || (!unitStop[nx.w] && !pastVerb(nx.w) && rateWords[nx.w] == ""):
				sp.q.unit, sp.end = singular(nx.w), nx.end
			}
		}
		sp.q.raw = strings.TrimSpace(s[sp.start:sp.end])
		out = append(out, sp)
		i += used
	}
	return out
}

// unitAfter reads the noun that follows a counted figure, as in "two dozen
// engineers".
func unitAfter(s string, toks []wordTok, j int, sp figure) figure {
	if j < len(toks) && !unitStop[toks[j].w] && strings.TrimSpace(s[sp.numEnd:toks[j].start]) == "" {
		sp.q.unit, sp.end = singular(toks[j].w), toks[j].end
	}
	return sp
}

// fractionAt finds "half", "in half", "a third", "a quarter of" and "half a
// million". An ordinal ("the third quarter", "third-party") is not a fraction.
func fractionAt(s string, toks []wordTok, i int) (figure, int, bool) {
	t := toks[i]
	f, ok := fractions[t.w]
	if !ok || t.joined || (i+1 < len(toks) && toks[i+1].joined) {
		return figure{}, 0, false
	}
	prev := ""
	if i > 0 {
		prev = toks[i-1].w
	}
	if t.w != "half" && t.w != "halves" && prev != "a" && prev != "an" {
		return figure{}, 0, false
	}
	fig := figure{q: quantity{value: f, unit: "x", raw: t.w}, start: t.start, numEnd: t.end, end: t.end, lead: true}
	n := 1
	switch {
	case t.w == "half" && prev == "in":
		fig.start, fig.q.raw, fig.q.dir = toks[i-1].start, "in half", -1
	case t.w == "half" && i+2 < len(toks) && (toks[i+1].w == "a" || toks[i+1].w == "an") && magnitudes[toks[i+2].w] >= 1e3:
		fig.q.value, fig.q.unit, fig.lead = f*magnitudes[toks[i+2].w], "", false
		fig.numEnd, fig.end, n = toks[i+2].end, toks[i+2].end, 3
		fig.q.raw = strings.TrimSpace(s[fig.start:fig.end])
	case prev == "a" || prev == "an":
		fig.start, fig.q.raw = toks[i-1].start, strings.TrimSpace(s[toks[i-1].start:t.end])
	}
	return fig, n, true
}

// countedOne is true when "one" at toks[i] is a count: "mentored one engineer",
// not "one of", "no one" or "the one".
func countedOne(toks []wordTok, i int) bool {
	if i+1 >= len(toks) || loneOneNext[toks[i+1].w] {
		return false
	}
	return i == 0 || !loneOnePrev[toks[i-1].w]
}

func isNumberWord(w string) bool {
	_, ok := numberWords[w]
	return ok
}

// numberRun parses the longest run of number words at the start of toks, as in
// "twenty five", "two hundred" or "one hundred fifty", and returns its value
// and the count of tokens it used.
func numberRun(s string, toks []wordTok) (float64, int) {
	var total, cur, best float64
	n := 0
	for k, t := range toks {
		if k > 0 {
			if gap := s[toks[k-1].end:t.start]; gap != " " && !(t.joined && gap == "-") {
				break
			}
		}
		if t.w == "and" && k > 0 && magnitudes[toks[k-1].w] > 0 && k+1 < len(toks) && isNumberWord(toks[k+1].w) {
			continue
		}
		if t.w == "zero" {
			if k == 0 {
				return 0, 1
			}
			break
		}
		low := int(cur) % 100
		v, isNum := numberWords[t.w]
		mag, isMag := magnitudes[t.w]
		switch {
		case isNum && v >= 10:
			if low != 0 {
				return best, n
			}
			cur += v
		case isNum:
			if low != 0 && (low < 20 || low%10 != 0) {
				return best, n
			}
			cur += v
		case isMag && mag == 100:
			if cur >= 100 || (cur == 0 && k > 0) {
				return best, n
			}
			if cur == 0 {
				cur = 1
			}
			cur *= 100
		case isMag:
			if cur == 0 && (k > 0 || total > 0) {
				return best, n
			}
			if cur == 0 {
				cur = 1
			}
			total += cur * mag
			cur = 0
		default:
			return best, n
		}
		best, n = total+cur, k+1
	}
	return best, n
}
