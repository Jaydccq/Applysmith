package match

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// A question that joins skills with "and", "&" or "as well as" asks about every
// one of them, so Yes needs evidence for each. The operands are the few words
// next to each conjunction; the words around them (cue phrases, prepositions,
// determiners) end an operand, which keeps a clause like "and the ability to
// work independently" from being read as a skill.

const (
	maxOperandWords = 4
	maxOperands     = 8
)

var (
	aswellasRE     = lazyregexp.New(`\bas\s+well\s+as\b`)
	operandLexemes = lazyregexp.New(`[a-z0-9][a-z0-9+#./'-]*|[&,;:?!()]`)
	operandStops   = setOf(
		"a", "an", "the", "in", "with", "of", "using", "on", "at", "for", "to", "as", "by", "from",
		"do", "does", "did", "you", "your", "have", "has", "had", "are", "is", "be", "been", "can",
		"could", "would", "will", "any", "some", "both", "either", "each", "such", "like", "including",
		"experience", "experienced", "proficient", "proficiency", "familiar", "familiarity", "knowledge",
		"knowledgeable", "skills", "skill", "expertise", "working", "work", "worked", "hands-on",
		"strong", "good", "solid", "what", "which", "how", "why", "or", "also", "well", "that", "this",
		"these", "those", "if", "when", "than", "between", "into", "within", "through", "per", "plus",
	)
)

func isConjunction(w string) bool { return w == "and" || w == "&" }

func isOperandBoundary(w string) bool {
	return operandStops[w] || isConjunction(w) || strings.ContainsAny(w[:1], ",;:?!()")
}

// conjunctionOperands lists the operands the question joins with a conjunction.
// It returns nil when no conjunction joins two operands, and when an operand is
// also joined by "or" (the grouping is then not readable).
func conjunctionOperands(question string) []string {
	text := aswellasRE.ReplaceAllString(strings.ToLower(question), " & ")
	items := operandLexemes.FindAllString(text, -1)
	for i, w := range items {
		if len(w) > 1 {
			items[i] = strings.TrimRight(w, ".'-")
		}
	}
	var out []string
	seen := map[int]bool{}
	add := func(lo, hi int) bool {
		if lo >= hi || seen[lo] || len(out) >= maxOperands {
			return false
		}
		seen[lo] = true
		out = append(out, strings.Join(items[lo:hi], " "))
		return true
	}
	for i, w := range items {
		if !isConjunction(w) {
			continue
		}
		hi := i + 1
		for hi < len(items) && hi-i <= maxOperandWords && !isOperandBoundary(items[hi]) {
			hi++
		}
		if hi < len(items) && items[hi] == "or" {
			return nil
		}
		add(i+1, operandExamplesEnd(items, hi))

		// An Oxford comma, then a comma-separated list to the left.
		end := i
		for end > 0 {
			if items[end-1] == "," {
				end--
			}
			lo := end
			for lo > 0 && end-lo < maxOperandWords && !isOperandBoundary(items[lo-1]) {
				lo--
			}
			if lo > 0 && items[lo-1] == "or" {
				return nil
			}
			if !add(lo, end) {
				break
			}
			if lo == 0 || items[lo-1] != "," {
				break
			}
			end = lo
		}
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

// operandExamplesEnd extends an operand that ends at hi over the examples a cue
// gives it ("on-call management software such as PagerDuty"): the examples
// name the category, so evidence for one of them covers it.
func operandExamplesEnd(items []string, hi int) int {
	j := hi
	switch {
	case j+1 < len(items) && items[j] == "such" && items[j+1] == "as":
		j += 2
	case j < len(items) && (items[j] == "like" || items[j] == "including"):
		j++
	default:
		return hi
	}
	end := j
	for end < len(items) && end-j < 2*maxOperandWords && (items[end] == "," || !isOperandBoundary(items[end])) {
		end++
	}
	if end == j {
		return hi
	}
	return end
}

// What a question about the user's own experience asks about (F-10) is read
// with the same lexemes and boundaries: the words between them, less the
// frame's ("have you previously supported", "a public track record") and the
// heads that only place the subject ("the sports industry", "a Managed Service
// Provider environment"). The frame holds the time words too ("before",
// "recently"), so an entry that writes them names nothing the question asks
// about. A phrase with a common other spelling is one term in both spellings,
// and its words are never read alone.
var (
	askedFrame = setOf(
		"us", "our", "we", "ever", "before", "earlier", "lately", "previously", "already", "recently", "currently", "actively", "personally", "professionally", "directly",
		"formerly", "prior", "previous", "past", "professional", "relevant", "practical", "proven", "demonstrated", "public",
		"track", "record", "background", "exposure", "experiences", "graduate", "graduates", "graduated", "alumnus", "alumna", "alumni", "member",
		"participant", "involved", "part", "supported", "used", "built", "developed", "managed", "led", "deployed",
		"implemented", "maintained", "maintain", "contribute", "contributes", "contributed", "participated", "completed", "attended", "served", "held",
		"owned", "operated", "administered", "designed", "created", "shipped", "launched", "delivered", "handled",
		"authored", "certified", "fluent", "skilled", "etc", "industry", "sector", "field", "space", "spaces", "environment",
		"setting", "company", "organization", "organisation", "firm", "business", "capacity", "role", "roles", "position", "team",
		"application", "program", "programme", "programmes", "project", "contribution")
	askedAliases = []struct {
		re    *lazyregexp.Regexp
		terms []string
	}{
		{lazyregexp.New(`\bmanaged[\s-]+services?[\s-]+providers?\b|\bmsps?\b`), []string{"managed service provider", "msp"}},
		{lazyregexp.New(`\bsoftware[\s-]+as[\s-]+a[\s-]+service\b|\bsaas\b`), []string{"software as a service", "saas"}},
		{lazyregexp.New(`\bopen[\s-]*source\b`), []string{"open source", "opensource"}},
		{lazyregexp.New(`\bartificial[\s-]+intelligence\b|\bai\b`), []string{"artificial intelligence", "ai"}},
		{lazyregexp.New(`\bmachine[\s-]+learning\b|\bml\b`), []string{"machine learning", "ml"}},
		{lazyregexp.New(`\blarge[\s-]+language[\s-]+models?\b|\bllms?\b`), []string{"large language model", "llm"}},
		{lazyregexp.New(`\bgolang\b|\bgo\b`), []string{"golang", "go"}},
		{lazyregexp.New(`\bjavascript\b|\bjs\b`), []string{"javascript", "js"}},
		{lazyregexp.New(`\bkubernetes\b|\bk8s\b`), []string{"kubernetes", "k8s"}},
		{lazyregexp.New(`\bpostgres(?:ql)?\b`), []string{"postgresql", "postgres"}},
	}
)

// AskedAbout lists what a question about the user's own experience asks about:
// each aliased phrase it holds in both spellings, then its other content
// words, lowercase, once each by stem, each two in a row also as one word
// ("full stack" is "fullstack" too), and a library named with a js ending
// also without it ("ReactJS" is "react" too). The text keeps its punctuation,
// so a closing "C#?" is still C#.
func AskedAbout(question string) []string {
	text := foldText(decodeEntities(question))
	var out []string
	seen := map[string]bool{}
	add := func(t string) {
		if k := stem(t); !seen[k] {
			seen[k] = true
			out = append(out, t)
		}
	}
	for _, a := range askedAliases {
		if a.re.MatchString(text) {
			for _, t := range a.terms {
				add(t)
			}
			text = a.re.ReplaceAllString(text, " , ")
		}
	}
	prev := ""
	for _, w := range operandLexemes.FindAllString(text, -1) {
		if isOperandBoundary(w) {
			prev = ""
			continue
		}
		if i := strings.IndexByte(w, '\''); i > 0 {
			w = w[:i] // a possessive or a contraction: Python's, you've
		}
		for _, t := range symbolBearingTokens(w) {
			if (len(t) > 1 || strings.ContainsAny(t, "+#")) && !operandStops[t] && !askedFrame[t] && !askedFrame[stem(t)] {
				add(t)
				if name, ok := strings.CutSuffix(t, "js"); ok && name != "" {
					add(name)
				}
				if prev != "" && !strings.ContainsAny(prev+t, "+#") {
					add(prev + t)
				}
				prev = t
			} else {
				prev = ""
			}
		}
	}
	return out
}

// uncoveredOperands returns the operands no supported term appears in, and
// whether any operand is covered. Only a conjunction that joins at least one
// recorded skill is judged here; an adjective pair ("scalable and reliable")
// with no recorded skill in it leaves the question to the ordinary match.
func uncoveredOperands(question string, supported []string) (missing []string, joinsRecordedSkill bool) {
	for _, op := range conjunctionOperands(question) {
		if len(matchingSkillTerms(op, supported)) > 0 {
			joinsRecordedSkill = true
		} else {
			missing = append(missing, op)
		}
	}
	return missing, joinsRecordedSkill
}
