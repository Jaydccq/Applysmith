package match

import (
	"slices"
	"sort"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Fuzzy thresholds: a candidate needs two shared stemmed tokens and a score of
// 0.40, and must lead the runner-up by 0.10.
const (
	minFuzzyInter     = 2
	minFuzzyScore     = 0.40
	minFuzzyMargin    = 0.10
	exampleMinCoverge = 0.5
)

// openerTokens are the stemmed generic opener words; a seed that shares only
// these says nothing about the topic.
var openerTokens = setOf(
	"tell", "us", "about", "describe", "share", "explain", "give", "provide", "briefly", "walk",
	"elaborate", "detail", "discuss", "summarize", "outline",
)

type seed struct {
	tokens  []string
	set     map[string]bool
	ask     []string // example seeds only
	isDesc  bool
	display string
}

// classIndex holds the per-request views the classification stages share. It is
// built once and never mutated afterwards.
type classIndex struct {
	entries []Entry
	byID    map[string]int
	seeds   [][]seed
	// used holds every stemmed token an entry's description or examples carry.
	used []map[string]bool
	// exampleUsers counts the entries whose examples use a token.
	exampleUsers map[string]int
	// examples are each entry's examples in its own order, read once.
	examples [][]exampleView
	// idToks are the stemmed words of each entry's id.
	idToks [][]string
}

// exampleView is one example as the example stages read it.
type exampleView struct {
	text, norm string
	stems      []string // stemmedTokens of the example
	topic      []string // its topic tokens: askTokensAll without the openers
}

var (
	idLikeRE     = lazyregexp.New(`[a-z0-9]+(?:-[a-z0-9]+)*`)
	parenGroupRE = lazyregexp.New(`\([^)]*\)`)
	enumeratedRE = lazyregexp.New(`:[^:]*,[^:]*$`)
)

func newClassIndex(entries []Entry) *classIndex {
	ix := &classIndex{
		entries:      entries,
		byID:         make(map[string]int, len(entries)),
		seeds:        make([][]seed, len(entries)),
		used:         make([]map[string]bool, len(entries)),
		exampleUsers: map[string]int{},
		examples:     make([][]exampleView, len(entries)),
		idToks:       make([][]string, len(entries)),
	}
	for i, e := range entries {
		if _, dup := ix.byID[e.ID]; !dup {
			ix.byID[e.ID] = i
		}
	}
	for i := range entries {
		e := &entries[i]
		used := map[string]bool{}
		exTokens := map[string]bool{}
		if d := strings.TrimSpace(e.Description); d != "" {
			toks := fuzzyTokens(ix.dropOtherIDs(d, i))
			ix.seeds[i] = append(ix.seeds[i], seed{tokens: toks, set: tokenSet(toks), isDesc: true, display: d})
			for _, t := range stemmedTokens(ix.dropOtherIDs(d, i)) {
				used[t] = true
			}
		}
		for _, ex := range e.Examples {
			ex = strings.TrimSpace(ex)
			if ex == "" {
				continue
			}
			toks := fuzzyTokens(ex)
			ask := askTokens(ex)
			if len(ask) == 0 {
				ask = toks
			}
			ix.seeds[i] = append(ix.seeds[i], seed{tokens: toks, set: tokenSet(toks), ask: ask, display: ex})
			for _, t := range stemmedTokens(ex) {
				used[t] = true
				exTokens[t] = true
			}
		}
		ix.used[i] = used
		ix.idToks[i] = stemmedTokens(strings.ReplaceAll(e.ID, "-", " "))
		for _, ex := range e.Examples {
			v := exampleView{text: ex, norm: normalizeExample(ex), stems: stemmedTokens(ex)}
			for _, t := range askTokensAll(ex) {
				if !openerTokens[t] {
					v.topic = append(v.topic, t)
				}
			}
			ix.examples[i] = append(ix.examples[i], v)
		}
		for t := range exTokens {
			ix.exampleUsers[t]++
		}
	}
	return ix
}

// dropOtherIDs removes from a description every word that is another entry's id,
// so a composite described by two other ids does not borrow their vocabulary.
func (ix *classIndex) dropOtherIDs(desc string, self int) string {
	return idLikeRE.ReplaceAllStringFunc(strings.ToLower(desc), func(tok string) string {
		if j, ok := ix.byID[tok]; ok && j != self {
			return " "
		}
		return tok
	})
}

// askTokens are an example's topic tokens: parenthesized groups and the
// enumerated option tail after the last colon are removed.
func askTokens(example string) []string {
	s := parenGroupRE.ReplaceAllString(example, " ")
	s = enumeratedRE.ReplaceAllString(s, " ")
	return fuzzyTokens(s)
}

type fuzzyScore struct {
	idx   int
	score float64
	inter int
}

// scoreEntries scores every included entry against the question. released
// holds the stemmed tokens of a claim the control could not accept: only seeds
// that hold all of them count.
func (ix *classIndex) scoreEntries(text string, released []string, include func(i int) bool) []fuzzyScore {
	qt := fuzzyTokens(text)
	if len(qt) == 0 {
		return nil
	}
	qset := tokenSet(qt)
	var topic []string
	for _, t := range qt {
		if !openerTokens[t] {
			topic = append(topic, t)
		}
	}
	var out []fuzzyScore
	for i := range ix.entries {
		if !ix.entries[i].classifiable() || !include(i) {
			continue
		}
		best := fuzzyScore{idx: i}
		for _, sd := range ix.seeds[i] {
			if len(sd.tokens) == 0 {
				continue
			}
			if len(released) > 0 && !allIn(released, sd.set) {
				continue
			}
			shared := sharedCount(qt, sd.set)
			if shared == 0 {
				continue
			}
			if len(topic) > 0 && sharedCount(topic, sd.set) == 0 {
				continue
			}
			denom := len(qt)
			if len(sd.tokens) < denom {
				denom = len(sd.tokens)
			}
			if denom < 2 {
				denom = 2
			}
			score := float64(shared) / float64(denom)
			if !sd.isDesc && len(sd.ask) > 0 {
				cov := float64(sharedCount(sd.ask, qset)) / float64(len(sd.ask))
				if cov < exampleMinCoverge && cov < score {
					score = cov
				}
			}
			if score > best.score {
				best.score, best.inter = score, shared
			}
		}
		if best.score > 0 {
			out = append(out, best)
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].score != out[b].score {
			return out[a].score > out[b].score
		}
		return out[a].inter > out[b].inter
	})
	return out
}

func allIn(tokens []string, set map[string]bool) bool {
	for _, t := range tokens {
		if !set[t] {
			return false
		}
	}
	return true
}

type fuzzyResult struct {
	idx   int
	score float64
	inter int
	tie   bool
	// different is set when the winner is about another subject; no runner-up
	// inherits the question then.
	different bool
	found     bool
}

// fuzzyPick decides the fuzzy stage from scored candidates.
func (ix *classIndex) fuzzyPick(text string, scored []fuzzyScore) fuzzyResult {
	if len(scored) == 0 {
		return fuzzyResult{}
	}
	w := scored[0]
	if w.inter < minFuzzyInter || w.score < minFuzzyScore {
		return fuzzyResult{}
	}
	if len(scored) > 1 && !leadsBy(w.score-scored[1].score, minFuzzyMargin) {
		return fuzzyResult{idx: w.idx, score: w.score, inter: w.inter, tie: true}
	}
	if ix.isDifferentSubject(w.idx, text) || ix.addsDurationFloor(w.idx, text) {
		return fuzzyResult{different: true}
	}
	return fuzzyResult{idx: w.idx, score: w.score, inter: w.inter, found: true}
}

// durationFloorRE is a floor on how long something lasted ("for at least 1
// year", "for 6+ months", "a minimum of two years").
var durationFloorRE = lazyregexp.New(`(?i)\b(?:at\s+least|a\s+minimum\s+of|minimum\s+of|more\s+than|over)\s+(?:\d+|` + countWords + `)\s*\+?\s*(?:years?|yrs?|months?)\b|\b\d+\s*\+\s*(?:years?|yrs?|months?)\b`)

// addsDurationFloor reports a question that bounds how long when none of the
// fuzzy winner's examples does: a Yes to "Have you worked at a startup?" says
// nothing of a year at one (Akuity).
func (ix *classIndex) addsDurationFloor(winner int, text string) bool {
	for _, sd := range ix.seeds[winner] {
		if !sd.isDesc && !floorOnlyIn(text, sd.display) {
			return false
		}
	}
	return durationFloorRE.MatchString(text)
}

// floorOnlyIn reports a duration floor the question sets and the example
// does not.
func floorOnlyIn(question, example string) bool {
	return durationFloorRE.MatchString(question) && !durationFloorRE.MatchString(example)
}

// isDifferentSubject reports a fuzzy winner about another subject than the
// question. A subject token is a stemmed token used by one or two entries'
// examples in the whole bank.
func (ix *classIndex) isDifferentSubject(winner int, text string) bool {
	qt := stemmedTokens(text)
	qset := tokenSet(qt)
	isSubject := func(t string) bool { n := ix.exampleUsers[t]; return n == 1 || n == 2 }
	var winnerSubj []string
	for _, sd := range ix.seeds[winner] {
		if sd.isDesc {
			continue
		}
		for _, t := range stemmedTokens(sd.display) {
			if isSubject(t) && !slices.Contains(winnerSubj, t) {
				winnerSubj = append(winnerSubj, t)
			}
		}
	}
	if len(winnerSubj) == 0 {
		return false
	}
	for _, t := range winnerSubj {
		if qset[t] {
			return false
		}
	}
	for _, t := range qt {
		if isSubject(t) && !ix.used[winner][t] {
			return true
		}
	}
	return false
}

const (
	maxTopicQuestionTokens = 8
	maxTopicExampleTokens  = 4
)

// topicLane is the curated topic lane: a curated example whose few topic tokens
// all appear in a short question that adds only a few more, and whose topic
// tokens are not all named by any other entry.
func (ix *classIndex) topicLane(text string, accepts func(*Entry) bool) (int, bool) {
	qt := stemmedTokens(text)
	if len(qt) == 0 || len(qt) > maxTopicQuestionTokens {
		return 0, false
	}
	qset := tokenSet(qt)
	winner := -1
	for i := range ix.entries {
		e := &ix.entries[i]
		if !e.classifiable() || !e.curated() || !accepts(e) {
			continue
		}
		matched := false
		for _, v := range ix.examples[i] {
			ask := v.topic
			if len(ask) < 1 || len(ask) > maxTopicExampleTokens || !allIn(ask, qset) {
				continue
			}
			if len(qt)-len(ask) > len(ask)+1 || ix.namesAnotherEntry(ask, i) || floorOnlyIn(text, v.text) {
				continue
			}
			matched = true
		}
		if !matched {
			continue
		}
		if winner >= 0 && winner != i {
			return 0, false
		}
		winner = i
	}
	return winner, winner >= 0
}

func askTokensAll(example string) []string {
	s := parenGroupRE.ReplaceAllString(example, " ")
	s = enumeratedRE.ReplaceAllString(s, " ")
	return stemmedTokens(s)
}

// namesAnotherEntry reports whether every topic token occurs in the
// description, id or examples of some other entry.
func (ix *classIndex) namesAnotherEntry(ask []string, self int) bool {
	for j := range ix.entries {
		if j == self {
			continue
		}
		have, idToks := ix.used[j], ix.idToks[j]
		ok := true
		for _, t := range ask {
			if !have[t] && !slices.Contains(idToks, t) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
