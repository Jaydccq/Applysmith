package kb

import (
	"math"
	"sort"
	"strings"
	"time"
)

// BM25 parameters and the score adjustments of the specification.
const (
	bm25K1           = 1.5
	bm25B            = 0.75
	tagBoost         = 0.15
	companyKindBoost = 0.20
	triggerBoost     = 10.0
	archetypeBoost   = 5.0
	archetypePenalty = 5.0
	minSharedTerms   = 2
	defaultK         = 4
	maxK             = 20
)

// hit is a ranked section before it becomes facts.
type hit struct {
	doc      *indexedDoc
	sec      *indexedSection
	score    float64
	reasons  []string
	clearing bool // some section cleared the lexical floor
	stage    string
}

func (h hit) key() string {
	if h.doc.Kind == KindSnippet {
		return h.doc.ID
	}
	return h.doc.Kind + ":" + h.doc.Path + "#" + h.sec.Heading
}

type scoredSection struct {
	sec    *indexedSection
	score  float64
	shared int
}

// kindsFilter decides which documents a query may draw from.
type kindsFilter struct {
	kinds map[string]bool
	cats  map[string]bool
}

func newKindsFilter(kinds []string) kindsFilter {
	f := kindsFilter{kinds: map[string]bool{}, cats: map[string]bool{}}
	for _, k := range kinds {
		k = strings.ToLower(strings.TrimSpace(k))
		isCat := false
		for _, c := range Categories {
			if c == k {
				f.cats[k] = true
				isCat = true
			}
		}
		if !isCat && k != "" {
			f.kinds[k] = true
		}
	}
	return f
}

func (f kindsFilter) allows(d *indexedDoc) bool {
	if len(f.kinds) == 0 && len(f.cats) == 0 {
		return true
	}
	if d.Kind == KindSnippet {
		if len(f.cats) > 0 {
			return f.cats[d.Category]
		}
		return f.kinds[KindSnippet]
	}
	return f.kinds[d.Kind]
}

// lexical ranks the documents for the query with BM25 over sections plus the
// trigger, tag, company-kind and archetype adjustments.
func (s *Service) lexical(q Query, docs []*indexedDoc, briefOK func(*indexedDoc) bool) []hit {
	qTokens := Tokens(q.Query)
	qTerms := map[string]bool{}
	for _, t := range qTokens {
		qTerms[t] = true
	}
	filter := newKindsFilter(q.Kinds)

	var pool []*indexedDoc
	for _, d := range docs {
		if !filter.allows(d) || (d.Kind == KindBrief && !briefOK(d)) {
			continue
		}
		pool = append(pool, d)
	}

	// Collection statistics over the sections the query may draw from.
	df := map[string]int{}
	var total, count int
	for _, d := range pool {
		for i := range d.secs {
			if !sensitiveAllowed(d.secs[i].Sensitive, q.Query) {
				continue
			}
			count++
			total += len(d.secs[i].Tokens)
			for t := range termSet(d.secs[i].Tokens) {
				df[t]++
			}
		}
	}
	avg := 1.0
	if count > 0 && total > 0 {
		avg = float64(total) / float64(count)
	}
	score := func(sec *indexedSection) scoredSection {
		tf := map[string]int{}
		for _, t := range sec.Tokens {
			tf[t]++
		}
		ss := scoredSection{sec: sec}
		dl := float64(len(sec.Tokens))
		for _, t := range qTokens {
			f := float64(tf[t])
			if f == 0 {
				continue
			}
			idf := math.Log((float64(count)-float64(df[t])+0.5)/(float64(df[t])+0.5) + 1)
			ss.score += idf * f * (bm25K1 + 1) / (f + bm25K1*(1-bm25B+bm25B*dl/avg))
		}
		for t := range qTerms {
			if tf[t] > 0 {
				ss.shared++
			}
		}
		return ss
	}

	archTags := lowerSet(q.ArchetypeTags)
	queryTags := lowerSet(q.Tags)
	anyArchetype := false
	if len(archTags) > 0 {
		for _, d := range pool {
			if isStory(d) && sharesAny(d.Tags, archTags) {
				anyArchetype = true
				break
			}
		}
	}

	var hits []hit
	for _, d := range pool {
		var scored []scoredSection
		for i := range d.secs {
			if sensitiveAllowed(d.secs[i].Sensitive, q.Query) {
				scored = append(scored, score(&d.secs[i]))
			}
		}
		if len(scored) == 0 {
			continue
		}
		if d.Kind != KindSnippet {
			for _, ss := range scored {
				if ss.shared >= minSharedTerms {
					hits = append(hits, hit{doc: d, sec: ss.sec, score: ss.score, reasons: []string{"bm25"}, clearing: true, stage: StageLexical})
				}
			}
			continue
		}
		triggered := anyTrigger(compileTriggers(d.Triggers), q.Query)
		archMatch := isStory(d) && len(archTags) > 0 && sharesAny(d.Tags, archTags)
		var best *scoredSection
		for i := range scored {
			if scored[i].shared >= minSharedTerms && (best == nil || scored[i].score > best.score) {
				best = &scored[i]
			}
		}
		clearing := best != nil
		if !clearing && !triggered && !archMatch {
			continue
		}
		if best == nil {
			best = &scored[0]
			for i := range scored {
				if scored[i].score > best.score {
					best = &scored[i]
				}
			}
		}
		h := hit{doc: d, sec: best.sec, score: best.score, clearing: clearing, stage: StageLexical}
		if clearing {
			h.reasons = append(h.reasons, "bm25")
		}
		if n := countShared(d.Tags, queryTags); n > 0 {
			h.score += tagBoost * float64(n)
			h.reasons = append(h.reasons, "tag")
		}
		if q.CompanyKind != "" && containsFold(d.PreferForCompanyKinds, q.CompanyKind) {
			h.score += companyKindBoost
			h.reasons = append(h.reasons, "company-kind")
		}
		if triggered {
			h.score += triggerBoost
			h.reasons = append(h.reasons, "trigger")
		}
		switch {
		case archMatch:
			h.score += archetypeBoost
			h.reasons = append(h.reasons, "archetype")
		case anyArchetype && isStory(d):
			h.score -= archetypePenalty
		}
		hits = append(hits, h)
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].key() < hits[j].key()
	})
	return hits
}

func isStory(d *indexedDoc) bool { return d.Kind == KindSnippet && d.Category == CatBehavioral }

func termSet(tokens []string) map[string]bool {
	m := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		m[t] = true
	}
	return m
}

func lowerSet(in []string) map[string]bool {
	m := map[string]bool{}
	for _, s := range in {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			m[s] = true
		}
	}
	return m
}

func sharesAny(have []string, want map[string]bool) bool { return countShared(have, want) > 0 }

func countShared(have []string, want map[string]bool) int {
	seen := map[string]bool{}
	n := 0
	for _, h := range have {
		h = strings.ToLower(strings.TrimSpace(h))
		if want[h] && !seen[h] {
			seen[h] = true
			n++
		}
	}
	return n
}

func containsFold(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(strings.TrimSpace(x), strings.TrimSpace(v)) {
			return true
		}
	}
	return false
}

// briefUsable reports whether a company brief may be returned as evidence: a
// manual brief always, any other until it expires.
func (s *Service) briefUsable(written map[string]string, now time.Time) func(*indexedDoc) bool {
	return func(d *indexedDoc) bool {
		return !briefExpired(d, written, now)
	}
}
