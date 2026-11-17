package documents

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"applysmith/internal/kb"
	"applysmith/internal/lazyregexp"
)

const (
	maxRequirements = 10
	maxKeywords     = 20
	maxReqChars     = 200
)

var (
	requirementHeadRE = lazyregexp.New(`(?i)^(?:#+\s*)?(?:requirements?|qualifications?|what you(?:'|\x{2019})?ll need|what we(?:(?:'|\x{2019})?re| are)? look(?:ing)? for|you have|you bring|must[- ]have|minimum qualifications|basic qualifications|skills)\b[^\n]*:?\s*$`)
	niceHeadRE        = lazyregexp.New(`(?i)^(?:#+\s*)?(?:nice[- ]to[- ]have|preferred(?: qualifications)?|bonus(?: points)?|a plus|pluses)\b[^\n]*:?\s*$`)
	dutyHeadRE        = lazyregexp.New(`(?i)^(?:#+\s*)?(?:(?:responsibilities|what you(?:(?:'|\x{2019})?ll| will) do)\b[^\n]*|you(?:(?:'|\x{2019})?ll| will))\s*:?\s*$`)
	otherHeadRE       = lazyregexp.New(`(?i)^(?:#+\s*)?(?:about (?:us|the (?:role|team|company))|benefits|perks|compensation|our (?:mission|team|culture)|why join[^\n]*)\b[^\n]*:?\s*$`)
	cueRE             = lazyregexp.New(`(?i)\b(experience|proficien\w*|knowledge|ability|familiar\w*|skills?|years?|expertise|fluen\w*|degree|background)\b`)
	listMarkRE        = lazyregexp.New(`^\s*(?:[-*+\x{2022}]|\d+[.)])\s+`)
	// A recipient is a person's name after a label, on one line.
	recipientRE = lazyregexp.New(`(?m)(?i:\b(?:hiring manager|recruiter|talent partner|contact))[ \t]*[:\-\x{2013}\x{2014}][ \t]*([A-Z][a-z'\x{2019}-]+(?:[ \t]+[A-Z][a-z'\x{2019}-]+){1,2})[ \t]*(?:$|[,.;(])`)
	// notAName words mark a team, a department or a heading, not a person.
	notAName = stringSet("team", "teams", "acquisition", "department", "dept", "human", "resources", "recruiting", "recruitment", "hiring", "careers",
		"talent", "people", "hr", "office", "group", "staff", "support", "partners", "partner", "operations", "services", "engineering", "product",
		"products", "great", "build", "company", "inc", "llc", "ltd", "corp", "us", "manager", "recruiter", "director", "lead", "desk", "center",
		"centre", "division", "unit", "committee", "panel", "board", "organization", "organisation", "agency", "network", "platform")
)

// successProfile reads the ranked requirements, core keywords, nice-to-haves
// and responsibilities of a posting. A section's lines are its list items, or
// its short lines when it has no list; prose after a list ends the section
// ("Salary $125,000" is not a requirement). It is deterministic: no model is
// involved, so it works without a provider and the same posting always gives
// the same profile.
func successProfile(posting string) (reqs []Requirement, keywords, nice, duties []string) {
	var mode string // "", "req", "nice", "duty"
	listed := false
	var reqLines, niceLines, cueLines []string
	for _, raw := range strings.Split(strings.ReplaceAll(posting, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		heading := true
		switch {
		case niceHeadRE.MatchString(line):
			mode = "nice"
		case requirementHeadRE.MatchString(line):
			mode = "req"
		case dutyHeadRE.MatchString(line):
			mode = "duty"
		case otherHeadRE.MatchString(line):
			mode = ""
		default:
			heading = false
		}
		if heading {
			listed = false
			continue
		}
		item := strings.TrimSpace(listMarkRE.ReplaceAllString(line, ""))
		isItem := item != line
		item = plain(item)
		if item == "" {
			continue
		}
		if mode != "" && !isItem && listed {
			mode = ""
		}
		listed = listed || isItem
		switch {
		case mode == "req" && (isItem || len(item) < 220):
			reqLines = append(reqLines, item)
		case mode == "nice" && (isItem || len(item) < 220):
			niceLines = append(niceLines, item)
		case mode == "duty" && (isItem || len(item) < 220):
			if len(duties) < maxRequirements {
				duties = append(duties, item)
			}
		case cueRE.MatchString(item):
			cueLines = append(cueLines, item)
		}
	}
	chosen := reqLines
	if len(chosen) == 0 {
		chosen = cueLines
	}
	for i, l := range chosen {
		if i >= maxRequirements {
			break
		}
		if r := []rune(l); len(r) > maxReqChars {
			l = string(r[:maxReqChars])
		}
		reqs = append(reqs, Requirement{ID: fmt.Sprintf("R%d", i+1), Text: l, Keywords: wordsOf(l)})
	}
	for i, l := range niceLines {
		if i >= maxRequirements {
			break
		}
		nice = append(nice, l)
	}
	return reqs, topKeywords(posting), nice, duties
}

// genericWords are posting words that say nothing about the role. An
// opportunity is a job, as a position is ("Submit Your Resume for Future
// Opportunities", Alnylam), and a letter's closing welcomes one.
var genericWords = map[string]bool{}

func init() {
	for _, w := range []string{"requirement", "requirements", "experience", "year", "years", "ability", "team", "teams", "role", "work", "working",
		"build", "building", "built", "nice", "have", "skill", "skills", "knowledge", "strong", "using", "use", "required", "preferred",
		"qualification", "qualifications", "responsibilities", "will", "must", "our", "you", "your", "the", "and", "company", "job",
		"position", "candidate", "ideal", "plus", "including", "related", "understanding", "familiarity", "excellent", "good", "great",
		"opportunity", "opportunities"} {
		genericWords[w] = true
	}
}

// topKeywords returns the most frequent content words of the posting, spelled
// the way the posting spells them, so a letter can print them as written.
func topKeywords(posting string) []string {
	count := map[string]int{}
	first := map[string]int{}
	forms := map[string]map[string]int{}
	for i, w := range surfaceWords(posting) {
		t := strings.ToLower(w.text)
		if len([]rune(t)) < 2 || genericWords[t] || isStop(t) || !hasLetter(t) {
			continue
		}
		count[t]++
		if _, ok := first[t]; !ok {
			first[t] = i
			forms[t] = map[string]int{}
		}
		// A capital that only starts a sentence says nothing about the word's
		// own spelling, so it is counted as the plain lower-case form.
		form := w.text
		if w.initial && form == capitalized(form) {
			form = t
		}
		forms[t][form]++
	}
	words := make([]string, 0, len(count))
	for w := range count {
		words = append(words, w)
	}
	sort.Slice(words, func(i, j int) bool {
		if count[words[i]] != count[words[j]] {
			return count[words[i]] > count[words[j]]
		}
		return first[words[i]] < first[words[j]]
	})
	if len(words) > maxKeywords {
		words = words[:maxKeywords]
	}
	for i, w := range words {
		words[i] = commonest(forms[w], w)
	}
	return words
}

// commonest is the spelling seen most often; a tie goes to the lower-case one,
// then to the alphabetically first, so the choice never depends on map order.
func commonest(forms map[string]int, lower string) string {
	best, n := lower, -1
	for f, c := range forms {
		switch {
		case c > n, c == n && f == lower, c == n && best != lower && f < best:
			best, n = f, c
		}
	}
	return best
}

func capitalized(w string) string {
	r := []rune(strings.ToLower(w))
	if len(r) > 0 {
		r[0] = unicode.ToUpper(r[0])
	}
	return string(r)
}

type surfaceWord struct {
	text string
	// initial is true when the word starts a sentence, a line or a list item.
	initial bool
}

// surfaceWords splits a text into words as written, keeping c++ and c# whole.
func surfaceWords(s string) []surfaceWord {
	isWord := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '+' || r == '#' }
	var out []surfaceWord
	start := true
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, surfaceWord{text: string(cur), initial: start})
			start = false
			cur = cur[:0]
		}
	}
	for _, r := range s {
		if isWord(r) {
			cur = append(cur, r)
			continue
		}
		flush()
		if r == '.' || r == '!' || r == '?' || r == ':' || r == '\n' || r == '\u2022' {
			start = true
		}
	}
	flush()
	return out
}

// wordsOf lower-cases the words of a text, keeping c++ and c# whole. These are
// the matching forms of a requirement; topKeywords keeps the posting's spelling.
func wordsOf(s string) []string {
	var out []string
	for _, w := range surfaceWords(s) {
		out = append(out, strings.ToLower(w.text))
	}
	return out
}

// termSet is the stemmed content terms of a text.
func termSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, t := range kb.Terms(s) {
		out[t] = true
	}
	return out
}

func overlap(a, b map[string]bool) int {
	n := 0
	for t := range a {
		if b[t] {
			n++
		}
	}
	return n
}

// recipientIn returns a hiring contact the posting names, or "".
func recipientIn(posting string) string {
	for _, m := range recipientRE.FindAllStringSubmatch(posting, -1) {
		if isPersonName(m[1]) {
			return m[1]
		}
	}
	return ""
}

func isPersonName(s string) bool {
	for _, w := range strings.Fields(s) {
		if notAName[strings.ToLower(strings.Trim(w, "'\u2019-"))] {
			return false
		}
	}
	return true
}
