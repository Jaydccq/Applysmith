package assist

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/kb"
)

// Evidence limits.
const (
	maxFacts     = 12
	maxBankFacts = 5
	maxCVFacts   = 4
	maxSnippets  = 4
	maxFactChars = 400
)

// FactKind is the source class of an evidence fact, in citation order.
type FactKind string

const (
	FactCorrection FactKind = "correction"
	FactBank       FactKind = "answer-bank"
	FactProfile    FactKind = "profile"
	FactCV         FactKind = "cv"
	FactSnippet    FactKind = "snippet"
)

// Fact is one piece of the user's own evidence; the model cites it by ID.
type Fact struct {
	ID   string
	Ref  string
	Kind FactKind
	Text string
}

// categoryWords decide when a profile field is offered: it appears only for a
// question that shares a word with its own category.
var categoryWords = map[Category][]string{
	CatName:       {"name", "first", "last", "full", "legal", "surname"},
	CatEduDates:   {"graduation", "graduate", "graduated", "dates", "year", "start", "end", "attended"},
	CatGPA:        {"gpa", "grade", "average", "cgpa"},
	CatEmployment: {"employer", "company", "role", "job", "position", "title", "career", "employed", "experience", "worked"},
	CatSkills:     {"skill", "skills", "experience", "proficiency", "language", "programming", "tool", "technology", "stack"},
	CatSalary:     {"salary", "compensation", "pay", "expected", "expectation", "desired", "wage", "rate"},
	CatWorkAuth:   {"authorized", "authorised", "citizen", "visa", "sponsorship", "immigration", "eligible", "legally", "permit"},
	CatLocation:   {"relocate", "relocation", "location", "commute", "onsite", "remote", "hybrid", "office"},
}

// identityFloor are the non-sensitive categories every question may see.
var identityFloor = map[Category]bool{CatCity: true, CatCountry: true, CatDegree: true, CatSchool: true}

// entryAnswerText is the answer an entry states, by kind; empty when it states
// none that can stand as a fact.
func entryAnswerText(e match.Entry) string {
	var v string
	switch e.Kind {
	case match.KindFixed:
		v = e.Answer
	case match.KindPickOption:
		for _, p := range e.Preferred {
			if strings.TrimSpace(p) != "" {
				v = p
				break
			}
		}
		if v == "" {
			v = e.FallbackText
		}
	case match.KindCompensationRange:
		v = e.DefaultAnswer
	case match.KindLanguageProficiency:
		v = e.Language
	case match.KindEmployerHistory:
		v = e.AnswerIfMatch
	case match.KindMultiSelect:
		v = strings.Join(e.Selected, ", ")
	}
	v = strings.TrimSpace(v)
	if v == "" || match.IsAskMe(v) || match.IsPlaceholderValue(v) {
		return ""
	}
	return v
}

type evidenceQuery struct {
	question string
	options  []string
	host     string
	pinnedID string
	// held says the question asks about pay the applicant already holds.
	held bool
	// archetype is the story axis of a behavioral question; the knowledge base
	// ranks the stories that carry it first.
	archetype string
	// narrative keeps the work-authorization fields out: a motivation answer
	// would cite them as evidence.
	narrative bool
	// option is the grounded option lane: work authorization, sponsorship and
	// pay are facts about the applicant that a model is never shown for an
	// option question, whatever the wording of the question.
	option bool
	// withEvidence says CV and profile evidence lines are offered, which carry
	// the employment and skills the profile fields would repeat.
	withEvidence bool
}

func (q evidenceQuery) tokens() (question, options []string) {
	question = tokens(q.question)
	for _, o := range q.options {
		options = append(options, tokens(o)...)
	}
	return question, options
}

type scoredFact struct {
	fact  Fact
	score float64
	id    string
}

// bankFacts retrieves at most five bank entries: the pinned entry first, then
// lexical hits that share at least two distinct content terms with the question
// and options (one when the question has only one). A blanket gate is never
// one: its Yes holds for whatever a question names, so a model citing it would
// claim any experience the page asks about (LiveKit's "Do you contribute to
// open source repos?"). A self-identification, criminal-history or
// date-of-birth entry is never one either: those values sit behind the
// sensitive gate and reach no model, whatever the question shares with them.
func bankFacts(src Sources, q evidenceQuery) []Fact {
	qt, ot := q.tokens()
	ot = distinct(ot)
	all := distinct(append(append([]string(nil), qt...), ot...))
	floor := min(2, len(all))
	var pinned *Fact
	var hits []scoredFact
	for _, e := range src.Bank {
		if e.Archived() || e.Placeholder || e.RunContext || match.IsBlanketGate(e.ID) || sensitiveEntry(e) || (q.held && match.IsTargetCompensationEntry(e)) || (q.option && match.IsWorkAuthOrPayEntry(e)) {
			continue
		}
		if e.Scoped() && !e.Admits(q.host, q.question, q.options) {
			continue
		}
		ans := entryAnswerText(e)
		if ans == "" {
			continue
		}
		label := e.Description
		if label == "" {
			label = strings.ReplaceAll(e.ID, "-", " ")
		}
		f := Fact{Ref: "answer-bank#" + e.ID, Kind: FactBank, Text: clipRunes(label+": "+ans, maxFactChars)}
		if e.ID == q.pinnedID {
			cp := f
			pinned = &cp
			continue
		}
		hay := tokens(strings.ReplaceAll(e.ID, "-", " ") + " " + e.Description + " " + strings.Join(e.Examples, " ") + " " + ans)
		qm := matchCount(qt, hay)
		if qm == 0 && matchCount(ot, hay) == 0 {
			continue
		}
		if matchCount(all, hay) < floor {
			continue
		}
		om := min(matchCount(ot, hay), 3)
		hits = append(hits, scoredFact{fact: f, score: 2*float64(qm) + 0.1*float64(om), id: e.ID})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return len(hits[i].id) < len(hits[j].id)
	})
	var out []Fact
	if pinned != nil {
		out = append(out, *pinned)
	}
	for _, h := range hits {
		if len(out) >= maxBankFacts {
			break
		}
		out = append(out, h.fact)
	}
	return out
}

// sensitiveEntry reports an entry whose value only the rules may give.
func sensitiveEntry(e match.Entry) bool {
	return e.Category == match.CategorySelfIdentification || e.Category == match.CategoryCriminalHistory || e.ID == "date-of-birth"
}

func profileFacts(src Sources, q evidenceQuery) []Fact {
	qt, ot := q.tokens()
	all := append(append([]string(nil), qt...), ot...)
	var out []Fact
	for _, p := range src.Profile {
		if strings.TrimSpace(p.Value) == "" {
			continue
		}
		if p.Category == CatSalary && q.held || p.Category == CatWorkAuth && q.narrative || q.option && (p.Category == CatSalary || p.Category == CatWorkAuth) ||
			q.withEvidence && (p.Category == CatEmployment || p.Category == CatSkills) {
			continue
		}
		if !identityFloor[p.Category] && matchCount(categoryWords[p.Category], all) == 0 {
			continue
		}
		out = append(out, Fact{Ref: p.Ref, Kind: FactProfile, Text: clipRunes(p.Label+": "+p.Value, maxFactChars)})
	}
	return out
}

func cvFacts(src Sources, q evidenceQuery) []Fact {
	qt, ot := q.tokens()
	all := append(append([]string(nil), qt...), ot...)
	// A line needs the same two distinct terms a bank entry needs (one when the
	// question has only one), so a single shared word never admits a CV line.
	floor := min(2, len(distinct(all)))
	var hits []scoredFact
	for i, line := range src.CV {
		if s := matchCount(all, tokens(line)); s > 0 && s >= floor {
			hits = append(hits, scoredFact{fact: Fact{Ref: fmt.Sprintf("cv#fact-%d", i+1), Kind: FactCV, Text: clipRunes(line, maxFactChars)}, score: float64(s)})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	var out []Fact
	for _, h := range hits {
		if len(out) >= maxCVFacts {
			break
		}
		out = append(out, h.fact)
	}
	return out
}

func correctionFacts(src Sources, q evidenceQuery) []Fact {
	want := normalizeSupport(q.question)
	var out []Fact
	for _, c := range src.Corrections {
		if strings.TrimSpace(c.Value) == "" || normalizeSupport(match.Sanitize(c.Question, nil)) != want {
			continue
		}
		out = append(out, Fact{Ref: c.Ref, Kind: FactCorrection, Text: "Previously corrected answer for this question: " + clipRunes(c.Value, maxFactChars)})
	}
	return out
}

// snippetFacts asks the knowledge base for the top snippets. A failing or absent
// knowledge base contributes nothing; the user's other facts still stand.
func (s *Service) snippetFacts(ctx context.Context, q evidenceQuery, k int) []Fact {
	query := q.question
	if len(q.options) > 0 {
		query += " " + strings.Join(q.options, " ")
	}
	kq := kb.Query{Query: query, Kinds: []string{kb.KindSnippet}, K: k}
	if q.archetype != "" {
		kq.ArchetypeTags = []string{q.archetype}
	}
	return s.knowledgeFacts(ctx, kq, kb.KindSnippet)
}

// storyFacts asks the knowledge base for the sections of the user's own
// narrative.md (what they look for, their proof points) that share two terms
// with the question and the role. voice.md is a style source and is never a
// fact.
func (s *Service) storyFacts(ctx context.Context, question, role string, k int) []Fact {
	return s.knowledgeFacts(ctx, kb.Query{Query: strings.TrimSpace(question + " " + role), Kinds: []string{kb.KindNarrative}, K: k}, kb.KindNarrative)
}

func (s *Service) knowledgeFacts(ctx context.Context, kq kb.Query, kind string) []Fact {
	if s.deps.Knowledge == nil {
		return nil
	}
	res, err := s.deps.Knowledge.Search(ctx, kq)
	if err != nil {
		s.deps.Log.Warn("assist: knowledge search failed", "err", err)
		return nil
	}
	var out []Fact
	for _, f := range res.Facts {
		if f.SourceKind != kind || strings.TrimSpace(f.Text) == "" {
			continue
		}
		out = append(out, Fact{Ref: f.SourceRef, Kind: FactSnippet, Text: clipRunes(f.Text, maxFactChars)})
	}
	return out
}

// Composer evidence bounds: about 500 tokens of CV and profile lines.
const (
	maxEvidenceLines    = 12
	maxEvidencePerEntry = 3
	maxEvidenceChars    = 2000
)

// questionScaffold are the words of a question's frame, which say nothing about
// which of the applicant's lines answers it.
var questionScaffold = setOf("describe", "tell", "us", "about", "time", "what", "why", "how", "when", "where", "which", "who",
	"give", "example", "share", "briefly", "explain", "walk", "through", "situation", "most", "some", "any", "want", "like",
	"re", "ve", "ll", "position", "role", "job", "company", "interest", "interests", "interested", "draws")

// topicCues are the words a CV line uses for the axis a question names without
// using them: "improved the performance" is told as "cut p99 latency from 1.8
// seconds to 420 milliseconds" (measured: without them no
// performance question was offered a latency line). A cue counts two points.
var topicCues = func() map[string][]string {
	speed := []string{"latency", "faster", "p99", "throughput", "milliseconds", "seconds", "minutes", "hours", "slow"}
	lead := []string{"led", "mentored", "review", "owned"}
	reliable := []string{"outage", "incident", "pages", "uptime", "duplicate"}
	return map[string][]string{
		"performance": speed, "faster": speed, "speed": speed, "optimize": speed, "optimized": speed, "efficient": speed, "efficiency": speed,
		"lead": lead, "leading": lead, "leadership": lead,
		"reliability": reliable, "reliable": reliable, "stability": reliable,
	}
}()

// composeEvidence selects the CV and profile lines a composition may rest on:
// each line is scored by the question's content terms it shares (three
// points each), their topic cues (two each) and the posting's terms it shares
// (one each, at most six); the
// roles and projects are ranked by their best line and visited in turn, so the
// best line of every relevant role comes before the second line of any. A broad
// question (a narrative or long-text one) may use every line, ranked by the
// posting when the question names no topic; a short one only lines that share
// its terms. A line another line of the same role already states in full (a
// profile summary the CV bullet extends) is dropped. At most 12 lines, 3 per
// role and 2000 characters.
func composeEvidence(lines []EvidenceLine, question, posting string, broad bool) []EvidenceLine {
	var qt []string
	for _, t := range distinct(tokens(question)) {
		if !questionScaffold[t] {
			qt = append(qt, t)
		}
	}
	var ct []string
	for _, t := range qt {
		ct = append(ct, topicCues[t]...)
	}
	ct = distinct(ct)
	pt := distinct(tokens(posting))
	floor := min(2, len(qt))
	type scored struct {
		line  EvidenceLine
		score int
	}
	groups := map[string][]scored{}
	var order []string
	norm := make([]string, len(lines))
	for i, l := range lines {
		norm[i] = strings.Join(tokens(l.Text), " ")
	}
	restated := func(i int) bool {
		for j, l := range lines {
			if j != i && l.Entry == lines[i].Entry && len(norm[j]) > len(norm[i]) && strings.Contains(norm[j], norm[i]) {
				return true
			}
		}
		return false
	}
	for i, l := range lines {
		if strings.TrimSpace(l.Text) == "" || restated(i) {
			continue
		}
		hay := tokens(l.Entry + " " + l.Text)
		qm := matchCount(qt, hay)
		if !broad && (floor == 0 || qm < floor) {
			continue
		}
		sc := scored{line: l, score: 3*qm + 2*matchCount(ct, hay) + min(6, matchCount(distinct(hay), pt))}
		if _, ok := groups[l.Entry]; !ok {
			order = append(order, l.Entry)
		}
		groups[l.Entry] = append(groups[l.Entry], sc)
	}
	best := map[string]int{}
	for k, g := range groups {
		sort.SliceStable(g, func(i, j int) bool { return g[i].score > g[j].score })
		best[k] = g[0].score
	}
	sort.SliceStable(order, func(i, j int) bool { return best[order[i]] > best[order[j]] })
	var out []EvidenceLine
	chars := 0
	for round := 0; round < maxEvidencePerEntry && len(out) < maxEvidenceLines; round++ {
		for _, k := range order {
			g := groups[k]
			if round >= len(g) || len(out) >= maxEvidenceLines {
				continue
			}
			t := clipRunes(g[round].line.Text, maxFactChars)
			if chars+len(t) > maxEvidenceChars {
				continue
			}
			l := g[round].line
			l.Text = t
			out = append(out, l)
			chars += len(t)
		}
	}
	return out
}

// gather builds the evidence of an option question in citation order:
// corrections, bank entries, profile fields, CV lines, snippets; at most twelve
// facts, numbered f1 onward.
func (s *Service) gather(ctx context.Context, src Sources, q evidenceQuery) []Fact {
	var all []Fact
	all = append(all, correctionFacts(src, q)...)
	all = append(all, bankFacts(src, q)...)
	all = append(all, profileFacts(src, q)...)
	all = append(all, cvFacts(src, q)...)
	all = append(all, s.snippetFacts(ctx, q, maxSnippets)...)
	all = dedupeFacts(all)
	if len(all) > maxFacts {
		all = all[:maxFacts]
	}
	for i := range all {
		all[i].ID = fmt.Sprintf("f%d", i+1)
	}
	return all
}

// dedupeFacts drops a fact whose text an earlier, higher-priority fact already
// states. Facts that disagree are both kept, in priority order.
func dedupeFacts(fs []Fact) []Fact {
	seen := map[string]bool{}
	var out []Fact
	for _, f := range fs {
		k := strings.TrimSpace(normalizeSupport(f.Text))
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, f)
	}
	return out
}
