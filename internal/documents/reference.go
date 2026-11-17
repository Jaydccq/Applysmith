package documents

import (
	"regexp"
	"strings"
	"unicode"

	"applysmith/internal/lazyregexp"
)

// A quoted company fact is a slot the renderer fills; the model never writes
// one. Text after a slot must not point back at it, because a pronoun or a
// pro-form would let the applicant's own claim ride on the fact ("I have tuned
// it for years"). Only closed word classes are read here, never a list of verbs
// or nouns, so a rewording the lists do not hold cannot get past on vocabulary.
var (
	firstPersonRE = lazyregexp.New(`\b(?:I|[Mm]e|[Mm]y|[Mm]ine|[Mm]yself|[Ww]e|[Uu]s|[Oo]urs?|[Oo]urselves)\b`)
	selfRefRE     = lazyregexp.New(`(?i)\b(?:yours\s+truly|the\s+undersigned|(?:this|the)\s+(?:author|writer|applicant|candidate)|your\s+correspondent)\b`)
	youRE         = lazyregexp.New(`(?i)\b(?:you|your|yours|yourself|yourselves)\b`)
	sentenceEndRE = lazyregexp.New(`[.!?]+["')\]]*\s+|\n+`)
	clauseWordRE  = lazyregexp.New(`[\p{L}\p{N}]+`)

	auxiliaries = stringSet("is", "are", "was", "were", "be", "been", "am", "do", "does", "did", "have", "has", "had",
		"can", "could", "will", "would", "shall", "should", "may", "might", "must")

	// softPronouns point back only when the sentence also speaks of the
	// applicant or has no stated subject.
	softPronouns = stringSet("it", "its", "itself", "them", "they", "their", "theirs", "themselves", "there", "so")
	// hardPronouns always point back.
	hardPronouns   = stringSet("same", "likewise", "similarly", "ditto")
	demonstratives = stringSet("this", "that", "these", "those")
	// afterDemonstrative are the words that follow a demonstrative used as a
	// pronoun or as a complementizer rather than before a noun.
	afterDemonstrative = stringSet("and", "or", "but", "of", "for", "in", "on", "at", "by", "with", "to", "from", "as", "than",
		"the", "a", "an", "i", "we", "you", "he", "she", "they", "it", "my", "our", "your", "his", "her", "their", "its", "there",
		"some", "any", "all", "no", "each", "every", "many", "most", "more", "both", "not", "which", "who", "when", "where", "how", "why", "what")
	// attitude words take "that" as a complementizer ("I believe that ..."). They
	// say how the applicant feels, never what the applicant did, so a "that"
	// after one is not read as a pointer. Know and understand are left out:
	// "I know that database" claims experience.
	attitude = stringSet("believe", "think", "hope", "feel", "appreciate", "admire", "like", "love", "value", "notice",
		"glad", "happy", "excited", "pleased", "confident", "sure", "convinced", "delighted", "thrilled")
	coordinators = stringSet("and", "but", "or", "nor", "yet")
	// subjectStarters open a clause that states its own subject or is a
	// fronted phrase: pronouns, determiners, prepositions and subordinators.
	subjectStarters = stringSet("i", "we", "you", "he", "she", "they", "it", "there", "the", "a", "an", "this", "that", "these",
		"those", "my", "our", "your", "his", "her", "their", "its", "some", "any", "each", "every", "all", "no", "both", "many",
		"most", "more", "one", "if", "when", "while", "because", "since", "although", "though", "as", "after", "before", "with",
		"without", "in", "on", "at", "for", "from", "by", "of", "to", "into", "over", "under", "during", "despite", "given",
		"through", "across", "within", "what", "how", "why", "who", "where", "which", "whether", "not", "also", "then", "now",
		"here", "thus", "therefore", "however", "so", "yes", "whenever", "once", "until", "unless")

	// determiners open a noun phrase, so a word before one does not end a
	// clause on its own.
	determiners = stringSet("the", "a", "an", "this", "that", "these", "those", "each", "every", "some", "any", "such")
	// aheadNouns are the nouns of the letter's own layout that "next" points
	// at ("in the next paragraph").
	aheadNouns = stringSet("paragraph", "section", "sentence", "line", "bullet", "item", "page", "part", "list", "point")

	// demonstrativeNouns are the nouns a demonstrative may take after a slot:
	// they name the posting or the letter and cannot name a fact's subject.
	// "That team" and "that company" can, so they are not here.
	demonstrativeNouns = stringSet("role", "position", "job", "opening", "opportunity", "posting", "letter", "application")
	// postingNouns and the words of the role title may repeat a fact's words
	// without pointing at it.
	postingNouns = stringSet("role", "position", "job", "opening", "opportunity", "posting", "team", "company", "letter",
		"application", "resume", "cv", "email", "message", "conversation")

	// functionWords carry no claim; the other words of a fact are its content.
	functionWords = stringSet(
		"a", "an", "the", "this", "that", "these", "those", "each", "every", "some", "any", "such",
		"of", "in", "on", "at", "by", "for", "with", "from", "to", "into", "onto", "over", "under", "up", "down", "out", "off",
		"about", "above", "below", "between", "through", "during", "before", "after", "since", "until", "within", "without",
		"across", "against", "among", "around", "near", "per", "via", "than", "as", "like", "upon", "toward", "towards",
		"beyond", "amid", "despite",
		"and", "but", "or", "nor", "yet", "so", "because", "while", "whereas", "when", "whenever", "if", "unless",
		"although", "though", "once", "which", "who", "whom", "whose", "where", "why", "how", "what", "whether", "then",
		"it", "its", "itself", "they", "them", "their", "theirs", "themselves", "he", "she", "him", "her", "his", "hers",
		"be", "am", "is", "are", "was", "were", "been", "being", "have", "has", "had", "having", "do", "does", "did",
		"can", "could", "will", "would", "shall", "should", "may", "might", "must", "not", "also", "more", "most", "all", "its")

	irregular = map[string]string{"grew": "grow", "grown": "grow", "ran": "run", "built": "build", "led": "lead", "began": "begin",
		"begun": "begin", "bought": "buy", "brought": "bring", "drove": "drive", "driven": "drive", "found": "find", "held": "hold",
		"kept": "keep", "made": "make", "sold": "sell", "spent": "spend", "took": "take", "won": "win", "wrote": "write", "written": "write"}

	// candidateRE finds a fact that describes the person a company wants
	// rather than the company itself ("The ideal candidate has scaled ...").
	candidateRE = lazyregexp.New(`(?i)\b(?:candidates?|applicants?|hires?|ideal|successful|requirements?)\b|\blooking\s+for\b|\byou(?:'ll|\x{2019}ll|\s+will)\b`)
)

// stem folds the common endings of one word to one form; it is applied to both
// sides of a comparison.
func stem(w string) string {
	w = strings.ToLower(w)
	if r, ok := irregular[w]; ok {
		return r
	}
	if strings.IndexFunc(w, unicode.IsDigit) >= 0 {
		return w
	}
	w = singular(w)
	for _, suf := range []string{"ing", "ed"} {
		if strings.HasSuffix(w, suf) && len(w)-len(suf) >= 3 {
			w = w[:len(w)-len(suf)]
			if n := len(w); n >= 2 && w[n-1] == w[n-2] && !strings.ContainsRune("lsz", rune(w[n-1])) {
				w = w[:n-1]
			}
			break
		}
	}
	return w
}

// contentStems are the stemmed content words of text.
func contentStems(text string) []string {
	var out []string
	for _, w := range lowerWords(text) {
		if functionWords[w] || (len(w) < 2 && !strings.ContainsAny(w, "0123456789")) {
			continue
		}
		out = append(out, stem(w))
	}
	return out
}

// lowerWords are the lower-case words of text, with an apostrophe ending a
// word ("it's" is "it", "s").
func lowerWords(text string) []string {
	var out []string
	for _, w := range clauseWordRE.FindAllString(text, -1) {
		out = append(out, strings.ToLower(w))
	}
	return out
}

// referenceCheck knows the words that may repeat a fact's words harmlessly:
// the company's name, the nouns of the posting and the words of the role title.
type referenceCheck struct {
	skip    map[string]bool // stems never counted as reuse
	company *lazyregexp.Regexp
	facts   map[string]bool // keys of the names in the facts quoted so far
}

func (m Manifest) referenceCheck() referenceCheck {
	r := referenceCheck{skip: map[string]bool{}, facts: map[string]bool{}}
	for w := range postingNouns {
		r.skip[stem(w)] = true
	}
	for _, text := range []string{m.Role, m.Company} {
		for _, w := range contentStems(text) {
			r.skip[w] = true
		}
	}
	for _, n := range []string{m.Company, companySuffixRE.ReplaceAllString(m.Company, "")} {
		if n = strings.TrimSpace(n); n != "" {
			r.company = lazyregexp.New(`(?i)^\W*` + regexp.QuoteMeta(n) + `\b`)
			break
		}
	}
	return r
}

// builtinKeys are the keys of the technology floor list.
var builtinKeys = func() map[string]bool {
	out := map[string]bool{}
	for k := range builtinTech {
		out[key(k)] = true
	}
	return out
}()

// nameKey is the key of a token that names a technology, a product or a
// company, by the rules the grounding code reads names with. A capitalized
// word that only opens a sentence is not known to be a name.
func nameKey(t nameTok) (string, bool) {
	k := key(t.text)
	if plainWordTech[k] && !techSpelling(t) {
		return "", false
	}
	if builtinKeys[k] {
		return k, true
	}
	if !nameShaped(t) || (t.initial && plainCapitalized(t.text)) {
		return "", false
	}
	return k, true
}

func plainCapitalized(w string) bool {
	for i, r := range w {
		if (i == 0) != unicode.IsUpper(r) || !unicode.IsLetter(r) {
			return false
		}
	}
	return w != ""
}

// addFact makes the names of a quoted fact count as reuse in later text. Only
// names are held: an ordinary word of the fact ("platform", "reliable") is
// the applicant's to use, and a figure is the quantity rules' business.
func (r referenceCheck) addFact(text string) {
	for _, t := range nameTokens(text) {
		if k, ok := nameKey(t); ok && !r.skip[stem(k)] {
			r.facts[k] = true
		}
	}
}

// postingNoun says whether ws[k] is a noun a demonstrative may take.
func postingNoun(ws []string, k int) bool {
	return k < len(ws) && demonstrativeNouns[singular(ws[k])]
}

// refersBack reports whether one sentence of text that follows a slot holds a
// closed-class reference to earlier text that the applicant's own claim could
// ride on. Only a sentence of the applicant's own is held to it: one with a
// first person or a self-reference, or with no stated subject.
func (r referenceCheck) refersBack(sentence string) bool {
	toks := clauseWordRE.FindAllString(sentence, -1)
	ws := make([]string, len(toks))
	for i, t := range toks {
		ws[i] = strings.ToLower(t)
	}
	soft := false
	for k, w := range ws {
		next, next2 := "", ""
		if k+1 < len(ws) {
			next = ws[k+1]
		}
		if k+2 < len(ws) {
			next2 = ws[k+2]
		}
		switch {
		case softPronouns[w]:
			soft = true
		case hardPronouns[w]:
			return true
		case w == "such" && next != "as":
			return true
		case (w == "so" || w == "neither" || w == "nor") && auxiliaries[next]:
			return true
		case auxiliaries[w] && (next == "too" || (next == "as" && next2 == "well")):
			return true
		case w == "that" && k > 0 && attitude[ws[k-1]]:
		case demonstratives[w]:
			switch {
			case next == "" || auxiliaries[next]:
				soft = true
			case postingNoun(ws, k+1):
			case afterDemonstrative[next] && w == "that":
			case afterDemonstrative[next]:
				soft = true
			case w == "that":
				soft = true // a relative "that" or a demonstrative: not told apart
			default:
				return true // this/these/those before a noun
			}
		}
	}
	own := firstPersonRE.MatchString(sentence) || selfRefRE.MatchString(sentence) || !r.statesSubject(sentence, toks)
	if soft && own {
		return true
	}
	if own {
		// A name from a quoted fact in the applicant's own sentence points at
		// it without a pronoun ("I know Cassandra inside out").
		for _, t := range nameTokens(sentence) {
			if k, ok := nameKey(t); ok && r.facts[k] {
				return true
			}
		}
	}
	return false
}

// statesSubject is false when the sentence opens without a subject or has a
// clause that continues the one before it without one ("and tuned it").
func (r referenceCheck) statesSubject(sentence string, toks []string) bool {
	if len(toks) == 0 {
		return false
	}
	first := strings.ToLower(toks[0])
	if !subjectStarters[first] && !unicode.IsDigit([]rune(first)[0]) && !(r.company != nil && r.company.MatchString(sentence)) {
		return false
	}
	for k := 0; k+1 < len(toks); k++ {
		if coordinators[strings.ToLower(toks[k])] {
			n := toks[k+1]
			if !subjectStarters[strings.ToLower(n)] && !unicode.IsUpper([]rune(n)[0]) && !unicode.IsDigit([]rune(n)[0]) {
				return false
			}
		}
	}
	return true
}

// splitSentences cuts text at terminal punctuation followed by space.
func splitSentences(text string) []string {
	var out []string
	prev := 0
	for _, loc := range sentenceEndRE.FindAllStringIndex(text, -1) {
		if s := strings.TrimSpace(text[prev:loc[1]]); s != "" {
			out = append(out, s)
		}
		prev = loc[1]
	}
	if s := strings.TrimSpace(text[prev:]); s != "" {
		out = append(out, s)
	}
	return out
}

// told says whether a word is a participle of showing or telling.
func told(w string) bool {
	return strings.HasSuffix(w, "ed") || w == "shown" || w == "given" || w == "seen" || w == "written" || w == "set" || w == "said"
}

// pointsAhead reports whether a sentence holds a closed-class pointer at what
// comes next in the letter: "below", "the following", "as follows", "further
// down", "after this", and "next" or "later" after a word of showing or
// telling ("described next for years", "mentioned later in this letter") or
// before a noun of the letter's layout ("the next paragraph"). "My next role",
// "work on next" and "I later became lead" are not pointers.
func pointsAhead(sentence string) bool {
	toks := wordToks(sentence)
	for i, t := range toks {
		if t.joined || (i+1 < len(toks) && toks[i+1].joined) {
			continue
		}
		prev, next := "", ""
		if i > 0 {
			prev = toks[i-1].w
		}
		clauseEnd := i+1 == len(toks) || breakRE.MatchString(sentence[t.end:toks[i+1].start])
		if !clauseEnd {
			next = toks[i+1].w
		}
		openClass := !clauseEnd && !(functionWords[next] && !determiners[next])
		switch t.w {
		case "follows":
			if prev == "as" {
				return true
			}
		case "following":
			if prev == "the" || clauseEnd {
				return true
			}
		case "below":
			if clauseEnd || !openClass {
				return true
			}
		case "next", "later":
			// Only after a word that says something is shown or told
			// ("described next", "mentioned later in this letter"), or
			// before a noun of the letter's layout; "work on next" and "I
			// later became lead" are about the applicant's own life.
			if (told(prev) && (clauseEnd || !openClass)) || (t.w == "next" && aheadNouns[singular(next)]) {
				return true
			}
		case "this":
			if prev == "after" {
				return true
			}
		case "down":
			if prev == "further" {
				return true
			}
		}
	}
	return false
}
