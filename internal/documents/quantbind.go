package documents

import (
	"strings"
)

var (
	// conjWords start a new clause: a figure is not about words behind them.
	conjWords = stringSet("and", "but", "while", "which", "that", "after", "before", "when", "because", "so", "then", "where", "whereas",
		"who", "though", "although", "if", "plus", "including")
	// phraseEnd words close the phrase a figure is followed by.
	phraseEnd = stringSet("across", "per", "via", "using", "like", "around", "among", "through", "throughout")
	// preStop words open a new phrase: what stands before them is not what
	// the figure behind them is about.
	preStop = stringSet("across", "per", "via", "using", "like", "around", "among", "through", "throughout", "in", "on", "for", "at", "within", "with", "into")
	// bridge words are skipped between a figure and its subject.
	bridge = stringSet("of", "the", "a", "an", "by", "to", "from", "than", "over", "about", "up", "down", "approximately", "nearly", "almost", "roughly", "more", "less")
	// leadPreps may open the phrase a figure is about when nothing stands
	// before the figure: "saved $1.2M in cloud spend".
	leadPreps = stringSet("in", "on", "for", "from", "of")

	downStems = []string{"reduc", "decreas", "lower", "drop", "slash", "trim", "shrink", "shrank", "halv", "eliminat", "remov", "fewer", "minimi", "declin", "sav"}
	upStems   = []string{"increas", "grow", "grew", "boost", "rais", "expand", "doubl", "tripl", "quadrupl", "accelerat", "gain", "scaled", "scaling"}

	// periods are the spans a rate names.
	periods = map[string]string{"year": "year", "yr": "year", "annum": "year", "month": "month", "mo": "month", "week": "week", "wk": "week",
		"day": "day", "hour": "hour", "hr": "hour", "minute": "minute", "min": "minute", "second": "second", "sec": "second", "quarter": "quarter", "qtr": "quarter"}
	rateWords = map[string]string{"annual": "year", "annually": "year", "yearly": "year", "monthly": "month", "weekly": "week", "daily": "day",
		"hourly": "hour", "quarterly": "quarter"}
)

func dirOf(w string) int {
	switch w {
	case "cut", "cuts", "cutting", "less", "fall", "falls", "falling", "fell", "fallen":
		return -1
	case "more", "rise", "rises", "rising", "rose", "risen":
		return 1
	case "savvy":
		return 0
	}
	for _, p := range downStems {
		if strings.HasPrefix(w, p) {
			return -1
		}
	}
	for _, p := range upStems {
		if strings.HasPrefix(w, p) {
			return 1
		}
	}
	return 0
}

// pastVerbs are irregular past forms; a word ending in -ed or -ing is read as
// a verb too. Neither names what a figure is about.
var pastVerbs = stringSet("cut", "led", "ran", "wrote", "built", "drove", "grew", "took", "made", "won", "got", "set", "put", "sold", "spent",
	"taught", "brought", "kept", "held", "lost", "met", "sent", "began", "chose", "rose", "fell", "saw", "did", "found", "gave", "shrank",
	"hit", "split", "sped", "bought", "paid", "thought", "left", "forwent", "stood", "understood", "oversaw", "undertook", "used", "caught")

var edNouns = stringSet("speed", "breed", "hundred", "infrared", "kindred", "sacred", "naked", "wicked", "hatred", "seed", "embed")

// verbPrefixes join a verb with a hyphen or, before an irregular verb, glued:
// co-led, re-architected, rebuilt.
var verbPrefixes = []string{"co", "re", "pre", "de", "un", "non"}

// splitVerbPrefix returns the verb behind a prefix: "co-led" gives "led",
// "rebuilt" gives "built" when the rest is a known verb.
func splitVerbPrefix(w string) (string, bool) {
	for _, p := range verbPrefixes {
		if r, ok := strings.CutPrefix(w, p+"-"); ok && r != "" {
			return r, true
		}
		if r, ok := strings.CutPrefix(w, p); ok && (pastVerbs[r] || openerVerbs[r]) {
			return r, true
		}
	}
	return w, false
}

func pastVerb(w string) bool {
	n := len(w)
	switch {
	case pastVerbs[w]:
		return true
	case n >= 5 && strings.HasSuffix(w, "ed") && !edNouns[w]:
		return true
	case n >= 6 && strings.HasSuffix(w, "ing"):
		return true
	}
	if r, ok := splitVerbPrefix(w); ok {
		return pastVerb(r) || openerVerbs[r]
	}
	return false
}

// verbLike is true for a word read as a verb: a past or -ing form, a word of
// change, a base form where a clause begins, and any word after "to".
func verbLike(w string, clauseStart, afterTo bool) bool {
	if pastVerb(w) || dirOf(w) != 0 || changeNouns[w] || (afterTo && lemma(w) != "") {
		return true
	}
	r, _ := splitVerbPrefix(w)
	// A résumé summary speaks in the third person: "Works with Go".
	return clauseStart && (openerVerbs[r] || openerVerbs[strings.TrimSuffix(r, "s")])
}

var irregularBase = map[string]string{"led": "lead", "built": "build", "ran": "run", "wrote": "write", "grew": "grow", "drove": "drive", "took": "take",
	"made": "make", "oversaw": "oversee", "taught": "teach", "sold": "sell", "spent": "spend", "won": "win", "rose": "rise", "shrank": "shrink", "brought": "bring", "caught": "catch",
	"held": "hold", "kept": "keep", "got": "get", "lost": "lose", "met": "meet", "sent": "send", "began": "begin", "chose": "choose", "fell": "fall", "saw": "see",
	"did": "do", "found": "find", "gave": "give", "sped": "speed", "bought": "buy", "paid": "pay", "thought": "think", "left": "leave", "stood": "stand",
	"understood": "understand", "undertook": "undertake",
	// A noun of change is read like its verb.
	"improvement": "improve", "improvements": "improve"}

// changeNouns are the nouns of change read like their verb: "a 22% improvement
// in throughput" is about throughput, as "improved throughput by 22%" is. A
// reduction or an increase is a noun of change through its direction already.
var changeNouns = stringSet("improvement", "improvements")

// verbClasses group verbs that say the same thing to a reader. A verb outside
// every class stands for itself.
var verbClasses = map[string][]string{
	"create":    {"build", "develop", "create", "construct", "engineer", "implement", "author", "write", "add", "introduce", "establish"},
	"ship":      {"ship", "launch", "release", "deploy"},
	"lead":      {"lead", "manage", "direct", "head", "oversee", "supervise", "spearhead"},
	"own":       {"own", "run", "operate", "maintain"},
	"mentor":    {"mentor", "coach", "train", "teach"},
	"architect": {"architect", "design"},
}

var verbClassOf = map[string]string{}

func init() {
	for class, members := range verbClasses {
		for _, m := range members {
			for _, f := range []string{m, m + "ed", m + "ing"} {
				verbClassOf[lemma(f)] = class
			}
		}
	}
}

// verbKey is the class of a verb: a reduction or an increase is one class, a
// listed synonym group another, anything else its lemma.
func verbKey(w string) string {
	if r, ok := splitVerbPrefix(w); ok {
		w = r
	}
	switch dirOf(w) {
	case -1:
		return "down"
	case 1:
		return "up"
	}
	if b, ok := irregularBase[w]; ok {
		w = b
	}
	l := lemma(w)
	if c, ok := verbClassOf[l]; ok {
		return c
	}
	return l
}

// lightVerbs carry no claim of their own: "delivered a 40% reduction" is about
// the reduction. A reduction or an increase is held to its direction instead.
var lightVerbs = stringSet("deliver", "achiev", "realiz", "realis", "generat", "produc", "driv", "secur", "attain", "yield", "record", "report",
	"see", "get", "earn", "enabl", "result", "contribut", "help", "support", "mak", "bring", "back")

// usageVerbs say who a thing is for when a counted figure follows them: "a
// pipeline used by forty teams" and "a platform serving forty teams" state
// what "a platform for forty teams" states. The counted noun still has to
// match.
var usageVerbs = stringSet("use", "serv", "adopt", "reli")

func counted(u string) bool {
	return u != "" && u != "%" && u != "x" && u != "time" && !strings.ContainsAny(u, "$€£")
}

// rateIn finds a rate in the first words behind a figure: "a year", "per
// day", "/yr", "daily". It returns the period and the index and count of the
// words it used. "for a year" is a duration, not a rate.
func rateIn(post string, words []string) (period string, at, n int) {
	slash := strings.HasPrefix(strings.TrimSpace(post), "/")
	for k := 0; k < len(words) && k < 4; k++ {
		w := strings.ToLower(words[k])
		if p, ok := rateWords[w]; ok {
			return p, k, 1
		}
		if k == 0 && slash {
			if p, ok := periods[w]; ok {
				return p, 0, 1
			}
		}
		if (w == "a" || w == "an" || w == "per" || w == "each" || w == "every") && k+1 < len(words) {
			if p, ok := periods[strings.ToLower(words[k+1])]; ok && (k == 0 || !durationPrep[strings.ToLower(words[k-1])]) {
				return p, k, 2
			}
		}
		// A new clause or phrase is not the figure's: "mentored four
		// engineers through weekly incident review" is no rate.
		if conjWords[w] || phraseEnd[w] {
			break
		}
	}
	return "", 0, 0
}

// comparatives follow a figure without being what it is about: "3x faster".
var comparatives = stringSet("faster", "slower", "higher", "lower", "better", "worse", "cheaper", "smaller", "larger", "bigger", "shorter",
	"longer", "quicker", "easier", "greater", "fewer", "more", "less")

var durationPrep = stringSet("for", "in", "over", "within", "during", "after", "before", "of", "last", "next")

// bind reads what the figure is about from the clause around it: the words
// before it up to the previous figure or clause break, and the phrase after
// it. bare is true when no word stands before the figure.
func bind(sp figure, s string, prevEnd, nextStart int) (quantity, bool) {
	q := sp.q
	q.post, q.ctx = map[string]bool{}, map[string]bool{}
	if sp.start < prevEnd {
		prevEnd = sp.start
	}
	pre := s[prevEnd:sp.start]
	if locs := breakRE.FindAllStringIndex(pre, -1); len(locs) > 0 {
		pre = pre[locs[len(locs)-1][1]:]
	}
	post := s[sp.numEnd:max(nextStart, sp.numEnd)]
	if loc := breakRE.FindStringIndex(post); loc != nil {
		post = post[:loc[0]]
	}
	var preNouns, farNouns, postNouns []string
	words := tokenRE.FindAllString(pre, -1)
	bare, crossed := true, false
	for i, scanned := len(words)-1, 0; i >= 0 && scanned < 8; i, scanned = i-1, scanned+1 {
		w := strings.ToLower(words[i])
		if approxWords[w] {
			continue
		}
		if conjWords[w] || preStop[w] {
			break
		}
		if p, ok := rateWords[w]; ok {
			if q.per == "" {
				q.per = p
			}
			continue
		}
		if d := dirOf(w); d != 0 && q.dir == 0 {
			q.dir = d
		}
		l := lemma(w)
		if l == "" || bridge[w] {
			continue
		}
		bare = false
		q.ctx[l] = true
		if infinitive := i > 0 && strings.ToLower(words[i-1]) == "to"; verbLike(w, i == 0, infinitive) {
			k := verbKey(w)
			q.ctx["v:"+k] = true
			// "to serve" says why, not what: only the main verb is held.
			if k != "up" && k != "down" && !lightVerbs[k] && !infinitive && !(counted(q.unit) && usageVerbs[k]) {
				q.verbs = append(q.verbs, k)
			}
			crossed = true
			continue
		}
		// Nouns behind a verb belong to an earlier predicate ("rightsized
		// clusters to save $1M"): they are the head only as a last resort.
		switch {
		case !crossed && len(preNouns) < 3:
			preNouns = append(preNouns, l)
		case crossed && len(farNouns) < 3:
			farNouns = append(farNouns, l)
		}
	}
	postWords := tokenRE.FindAllString(post, 10)
	i, rateAt, rateN := 0, -1, 0
	if p, at, n := rateIn(post, postWords); n > 0 && !sp.lead {
		q.per, rateAt, rateN = p, at, n
	}
	for ; i < len(postWords); i++ {
		if i >= rateAt && i < rateAt+rateN {
			continue
		}
		w := strings.ToLower(postWords[i])
		if conjWords[w] || phraseEnd[w] {
			break
		}
		if w == "of" || w == "the" || w == "a" || w == "an" || (len(preNouns) == 0 && len(postNouns) == 0 && leadPreps[w]) {
			continue
		}
		if d := dirOf(w); d != 0 && q.dir == 0 {
			q.dir = d
		}
		if p, ok := rateWords[w]; ok {
			if q.per == "" {
				q.per = p
			}
			continue
		}
		l := lemma(w)
		if l == "" {
			break
		}
		q.post[l], q.ctx[l] = true, true
		if comparatives[w] {
			continue
		}
		if verbLike(w, false, false) {
			q.ctx["v:"+verbKey(w)] = true
			if len(postNouns) > 0 {
				break
			}
		} else if len(postNouns) < 3 {
			postNouns = append(postNouns, l)
		}
		if len(q.post) >= 3 {
			break
		}
	}
	// A counted noun is its own subject; anything else is about the words
	// beside it. The head is the noun nearest the figure before it, the last
	// noun of the phrase after it.
	if counted(q.unit) {
		return q, bare
	}
	article := false
	if len(words) > 0 {
		last := strings.ToLower(words[len(words)-1])
		article = (last == "a" || last == "an" || last == "the") && strings.TrimSpace(pre[strings.LastIndex(pre, words[len(words)-1])+len(last):]) == ""
	}
	switch {
	case len(preNouns) > 0 && !sp.lead && !(article && len(postNouns) > 0):
		q.head, q.mods = preNouns[0], preNouns[1:]
	case len(postNouns) > 0:
		q.head, q.mods = postNouns[len(postNouns)-1], postNouns[:len(postNouns)-1]
	case len(preNouns) > 0:
		q.head, q.mods = preNouns[0], preNouns[1:]
	case len(farNouns) > 0:
		q.head, q.mods = farNouns[0], farNouns[1:]
	}
	return q, bare
}
