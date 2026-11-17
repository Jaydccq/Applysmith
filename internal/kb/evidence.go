package kb

import (
	"context"
	"sort"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Retrieval stages.
const (
	StageLexical  = "lexical"
	StageSemantic = "semantic"
)

// Result statuses.
const (
	StatusGrounded           = "grounded"
	StatusNoMatch            = "no_match"
	StatusSourcesUnavailable = "sources_unavailable"
	StatusPartial            = "partial"
)

// Trust tiers, highest first.
const (
	TrustCorrection = "correction"
	TrustCurated    = "curated"
	TrustProfile    = "profile"
	TrustEvidential = "evidential"
)

var trustRank = map[string]int{TrustCorrection: 4, TrustCurated: 3, TrustProfile: 2, TrustEvidential: 1}

func trustOf(kind string) string {
	switch kind {
	case KindCorrection:
		return TrustCorrection
	case KindAnswerBank:
		return TrustCurated
	case KindProfile:
		return TrustProfile
	}
	return TrustEvidential
}

// Fact is a piece of evidence with its provenance.
type Fact struct {
	ID            string   `json:"id"`
	Text          string   `json:"text"`
	SourceKind    string   `json:"sourceKind"`
	SourceRef     string   `json:"sourceRef"`
	Score         float64  `json:"score"`
	Trust         string   `json:"trust"`
	Stage         string   `json:"stage"`
	Reasons       []string `json:"reasons,omitempty"`
	AllowedFacts  []string `json:"allowedFacts,omitempty"`
	Provenance    []string `json:"provenance,omitempty"`
	ConflictsWith []string `json:"conflictsWith,omitempty"`
}

// Query is one retrieval request.
type Query struct {
	Query string `json:"query"`
	// Kinds restricts the sources: a source kind (snippet, cv, profile,
	// narrative, company-brief) or a snippet category (projects, experience,
	// behavioral, answer-patterns). Naming only categories limits retrieval to
	// the snippets of those categories.
	Kinds         []string `json:"kinds,omitempty"`
	K             int      `json:"k,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	ArchetypeTags []string `json:"archetypeTags,omitempty"`
	CompanyKind   string   `json:"companyKind,omitempty"`
}

// Result is the answer to a Query.
type Result struct {
	Facts       []Fact          `json:"facts"`
	Status      string          `json:"status"`
	Failed      []SourceFailure `json:"failed,omitempty"`
	Diagnostics []string        `json:"diagnostics,omitempty"`
}

// Source is an evidence source that lives outside this package, such as the
// curated answers or the user's corrections. A returned error marks the source
// as unavailable for this call.
type Source interface {
	Kind() string
	Search(ctx context.Context, q Query) ([]Fact, error)
}

var labeledRE = lazyregexp.New(`^\s*(?:[-*+]\s+)?([A-Za-z][A-Za-z0-9/&'-]*(?: [A-Za-z0-9/&'-]+){0,4}):\s+(\S.*?)\s*$`)

// labeled splits a "Label: value" line.
func labeled(line string) (label, value string, ok bool) {
	m := labeledRE.FindStringSubmatch(line)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

func normLabel(l string) string { return strings.Join(strings.Fields(strings.ToLower(l)), " ") }

func normText(t string) string {
	t = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(t), "-*+ "))
	return strings.TrimRight(strings.Join(strings.Fields(strings.ToLower(t)), " "), ".")
}

// factFromSection builds the primary fact of a matched section and, for each
// "Label: value" line it holds or cites, a labeled fact that shares an id with
// the same fact stated elsewhere.
func factFromSection(d *indexedDoc, sec *indexedSection, score float64, stage string, reasons []string) []Fact {
	ref := sectionRef(d, sec)
	allowed := sec.Facts
	if len(allowed) == 0 {
		allowed = d.Facts()
	}
	text := clip(sec.Text, sectionBudget)
	base := Fact{
		SourceKind: d.Kind, SourceRef: ref, Score: score, Trust: trustOf(d.Kind), Stage: stage,
		Reasons: reasons, AllowedFacts: allowed, Provenance: []string{d.Kind + ":" + ref},
	}
	var out []Fact
	single := false
	if label, value, ok := labeled(text); ok && !strings.Contains(strings.TrimSpace(text), "\n") {
		single = true
		f := base
		f.ID, f.Text = "semantic:"+normLabel(label), label+": "+value
		out = append(out, f)
	}
	if !single {
		f := base
		f.ID, f.Text = d.Kind+":"+ref, text
		out = append(out, f)
		lines := strings.Split(sec.Text, "\n")
		lines = append(lines, allowed...)
		seen := map[string]bool{}
		for _, line := range lines {
			label, value, ok := labeled(line)
			if !ok || seen[normLabel(label)+"|"+normText(value)] {
				continue
			}
			seen[normLabel(label)+"|"+normText(value)] = true
			sub := base
			sub.ID, sub.Text = "semantic:"+normLabel(label), label+": "+value
			sub.Reasons = append(append([]string{}, reasons...), "labeled")
			out = append(out, sub)
		}
	}
	return out
}

// sectionRef names a section as its document's path and, when it has one, its
// heading: "knowledge/projects/a.md#Detail".
func sectionRef(d *indexedDoc, sec *indexedSection) string {
	if sec.Heading == "" {
		return d.Path
	}
	return d.Path + "#" + sec.Heading
}

func clip(s string, n int) string {
	if runeLen(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// mergeFacts merges facts with the same id and text (higher score, union of
// provenance), keeps facts with the same id and different text side by side
// with each naming the others, and sorts the result.
func mergeFacts(in []Fact) []Fact {
	type group struct{ facts []Fact }
	order := []string{}
	groups := map[string]*group{}
	for _, f := range in {
		g := groups[f.ID]
		if g == nil {
			g = &group{}
			groups[f.ID] = g
			order = append(order, f.ID)
		}
		merged := false
		for i := range g.facts {
			if normText(g.facts[i].Text) == normText(f.Text) {
				g.facts[i] = mergeTwo(g.facts[i], f)
				merged = true
				break
			}
		}
		if !merged {
			g.facts = append(g.facts, f)
		}
	}
	var out []Fact
	for _, id := range order {
		g := groups[id]
		if len(g.facts) > 1 {
			for i := range g.facts {
				for j := range g.facts {
					if i != j {
						g.facts[i].ConflictsWith = addUnique(g.facts[i].ConflictsWith, g.facts[j].SourceKind+":"+g.facts[j].SourceRef)
					}
				}
			}
		}
		out = append(out, g.facts...)
	}
	sortFacts(out)
	return out
}

func mergeTwo(a, b Fact) Fact {
	if b.Score > a.Score || (b.Score == a.Score && trustRank[b.Trust] > trustRank[a.Trust]) {
		a.Score, a.Trust, a.Stage = b.Score, b.Trust, b.Stage
	}
	for _, r := range b.Reasons {
		a.Reasons = addUnique(a.Reasons, r)
	}
	for _, p := range b.Provenance {
		a.Provenance = addUnique(a.Provenance, p)
	}
	for _, f := range b.AllowedFacts {
		a.AllowedFacts = addUnique(a.AllowedFacts, f)
	}
	return a
}

func addUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// sortFacts orders by stage (lexical results always rank above semantic ones),
// then score descending, then source reference.
func sortFacts(fs []Fact) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if (a.Stage == StageSemantic) != (b.Stage == StageSemantic) {
			return b.Stage == StageSemantic
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.SourceRef != b.SourceRef {
			return a.SourceRef < b.SourceRef
		}
		if la, lb := strings.HasPrefix(a.ID, "semantic:"), strings.HasPrefix(b.ID, "semantic:"); la != lb {
			return lb
		}
		return a.ID < b.ID
	})
}

// Sensitive categories are returned only when the query itself is about them.
var sensitiveQuery = map[string]*lazyregexp.Regexp{
	SensWorkAuth: lazyregexp.New(`(?i)authori[sz]ed to work|work authori[sz]|work permit|sponsorship|\bvisa\b|citizen|clearance|right to work|eligible to work|immigration|green card`),
	SensComp:     lazyregexp.New(`(?i)salary|compensation|\bpay\b|\bwages?\b|\bcomp\b|remunerat|bonus|expected earnings|rate expectation|stock option|\bequity (grant|package)|\brsus?\b`),
	SensDOB:      lazyregexp.New(`(?i)date of birth|birth ?date|\bdob\b|born on|\bage\b|how old`),
	SensSelfID:   lazyregexp.New(`(?i)gender|\brace\b|ethnic|veteran|disabilit|pronoun|sexual orientation|self-identif|hispanic|\beeo\b`),
	SensCriminal: lazyregexp.New(`(?i)criminal|convict|felony|misdemeanor|background check`),
}

// sensitiveAllowed reports whether a section of the given sensitive category
// may be returned for the query.
func sensitiveAllowed(category, query string) bool {
	if category == "" {
		return true
	}
	re := sensitiveQuery[category]
	return re != nil && re.MatchString(query)
}
