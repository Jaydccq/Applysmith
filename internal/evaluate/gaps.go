// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"applysmith/internal/identity"
	"applysmith/internal/lazyregexp"
	"applysmith/internal/store"
)

// The three states of a gap. Absence from the CV alone is not-evidenced: it
// never proves the user lacks the skill, so only the user's own confirmation
// makes it a confirmed-gap.
const (
	GapConfirmed    = "confirmed-gap"
	GapNotEvidenced = "not-evidenced"
	GapNeedsReview  = "needs-review"
)

// Requirement classes of a gap source.
const (
	ClassRequired  = "required"
	ClassPreferred = "preferred"
)

// Why a selected report is outside the analyzed sample.
const (
	ExcludedNotFound   = "not-found"
	ExcludedUnreadable = "unreadable"
	ExcludedNoEvidence = "no-requirement-evidence"
	ExcludedDuplicate  = "duplicate-posting"
)

// MaxGapReports bounds the sample of one analysis.
const MaxGapReports = 100

// ErrGapInvalid is a gap request the caller got wrong.
var ErrGapInvalid = errors.New("evaluate: invalid gap request")

func gapInvalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrGapInvalid, fmt.Sprintf(format, args...))
}

// GapSource is one posting's statement of a requirement the user's profile
// does not fully evidence.
type GapSource struct {
	Report      int64   `json:"report"`
	Company     string  `json:"company"`
	Role        string  `json:"role"`
	Requirement string  `json:"requirement"`
	Quote       string  `json:"quote,omitempty"`
	Class       string  `json:"class"`
	Hit         float64 `json:"hit"`
	// Evidence is the profile evidence that supports a partial hit; empty when
	// there is none, including a claimed hit that named no profile source.
	Evidence    string `json:"evidence,omitempty"`
	Unsupported bool   `json:"unsupported,omitempty"`
}

// Gap is one skill with every posting that asks for it.
type Gap struct {
	Skill     string      `json:"skill"`
	Label     string      `json:"label"`
	State     string      `json:"state"`
	Required  int         `json:"required"`
	Preferred int         `json:"preferred"`
	Sources   []GapSource `json:"sources"`
}

// InferredItem is a signal the evaluation read between the lines. It is
// listed apart from the gaps and never counted as demand.
type InferredItem struct {
	Report   int64  `json:"report"`
	Company  string `json:"company"`
	Role     string `json:"role"`
	Label    string `json:"label"`
	Evidence string `json:"evidence,omitempty"`
}

// GapExclusion says why a selected report is not in the sample.
type GapExclusion struct {
	Report      int64  `json:"report"`
	Reason      string `json:"reason"`
	DuplicateOf int64  `json:"duplicate_of,omitempty"`
}

// GapInput is the revision of a report the analysis read.
type GapInput struct {
	Report      int64  `json:"report"`
	CreatedAt   string `json:"created_at"`
	ContentHash string `json:"content_hash"`
}

// GapReport is the analysis artifact. It is not a posting report and carries
// no fit score.
type GapReport struct {
	ID          string         `json:"id"`
	GeneratedAt string         `json:"generated_at"`
	Selected    []int64        `json:"selected"`
	Sample      []GapInput     `json:"sample"`
	Excluded    []GapExclusion `json:"excluded"`
	Gaps        []Gap          `json:"gaps"`
	Inferred    []InferredItem `json:"inferred"`
	Plan        *Plan          `json:"plan,omitempty"`
	// Done lists the skills whose study exercise the user marked complete.
	Done []string `json:"done"`
}

var fitFence = lazyregexp.New("(?s)```scorecard\n(.*?)\n```")

// scorecardOf reads the scorecard block of the Fit section of a final report;
// a block in model-written prose elsewhere is not the scorecard.
func scorecardOf(markdown string) (scorecardDoc, bool) {
	_, fit, ok := strings.Cut(markdown, "\n## Fit\n")
	if !ok {
		return scorecardDoc{}, false
	}
	if i := strings.Index(fit, "\n## "); i >= 0 {
		fit = fit[:i]
	}
	m := fitFence.FindStringSubmatch(fit)
	if m == nil {
		return scorecardDoc{}, false
	}
	var doc scorecardDoc
	return doc, json.Unmarshal([]byte(m[1]), &doc) == nil
}

// SkillKey is the identity of a skill across postings: the label folded to
// lowercase words.
func SkillKey(label string) string { return strings.Join(strings.Fields(identity.Fold(label)), " ") }

// gapPosting is one report in the sample.
type gapPosting struct {
	row  IndexRow
	card scorecardDoc
	hash string
}

// trackerEntries maps every identity key a tracker row answers to onto the
// row, so two reports whose keys differ can still be one opening the tracker
// already merged.
func trackerEntries(ctx context.Context, st *store.Store) (map[string]int64, error) {
	rows, err := st.Read().QueryContext(ctx, `SELECT identity_key, id FROM tracker_entries UNION ALL SELECT identity_key, entry_id FROM tracker_aliases`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var key string
		var id int64
		if err := rows.Scan(&key, &id); err != nil {
			return nil, err
		}
		out[key] = id
	}
	return out, rows.Err()
}

// dedupePostings keeps one report per opening, the newest, and records the
// others as excluded. Two reports are one opening when they share an identity
// key, resolve to one tracker row, or pass the shared same-opening test.
func dedupePostings(ps []gapPosting, entries map[string]int64) (kept []gapPosting, dropped []GapExclusion) {
	parent := make([]int, len(ps))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	same := func(a, b gapPosting) bool {
		if a.row.IdentityKey == b.row.IdentityKey {
			return true
		}
		ea, oka := entries[a.row.IdentityKey]
		eb, okb := entries[b.row.IdentityKey]
		if oka && okb && ea == eb {
			return true
		}
		pa := identity.Posting{Key: a.row.IdentityKey, URL: a.row.PostingURL, Company: a.row.Company, Role: a.row.Role}
		pb := identity.Posting{Key: b.row.IdentityKey, URL: b.row.PostingURL, Company: b.row.Company, Role: b.row.Role}
		return identity.SameOpening(pa, pb)
	}
	for i := range ps {
		for j := i + 1; j < len(ps); j++ {
			if same(ps[i], ps[j]) {
				parent[find(j)] = find(i)
			}
		}
	}
	newest := map[int]int{}
	for i := range ps {
		r := find(i)
		best, ok := newest[r]
		if !ok || newer(ps[i].row, ps[best].row) {
			newest[r] = i
		}
	}
	for i, p := range ps {
		keeper := newest[find(i)]
		if keeper == i {
			kept = append(kept, p)
			continue
		}
		dropped = append(dropped, GapExclusion{Report: p.row.Number, Reason: ExcludedDuplicate, DuplicateOf: ps[keeper].row.Number})
	}
	return kept, dropped
}

func newer(a, b IndexRow) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	return a.Number > b.Number
}

// readPostings loads the selected reports and sorts out the ones that carry no
// requirement evidence.
func (g *Gaps) readPostings(ctx context.Context, numbers []int64) (ps []gapPosting, excluded []GapExclusion, err error) {
	for _, n := range numbers {
		ref, err := FindReport(ctx, g.st, g.root, n)
		if errors.Is(err, store.ErrNotFound) {
			excluded = append(excluded, GapExclusion{Report: n, Reason: ExcludedNotFound})
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if ref.Row.Kind != string(KindFinal) {
			excluded = append(excluded, GapExclusion{Report: n, Reason: ExcludedNoEvidence})
			continue
		}
		md, err := ref.Markdown()
		if err != nil {
			excluded = append(excluded, GapExclusion{Report: n, Reason: ExcludedUnreadable})
			continue
		}
		card, ok := scorecardOf(string(md))
		if !ok {
			excluded = append(excluded, GapExclusion{Report: n, Reason: ExcludedNoEvidence})
			continue
		}
		sum := sha256.Sum256(md)
		ps = append(ps, gapPosting{row: ref.Row, card: card, hash: hex.EncodeToString(sum[:])})
	}
	return ps, excluded, nil
}

// gapsOf collects, per skill, every unmet requirement of the sample. Required
// and preferred counts are distinct postings. Items the evaluation inferred go
// to their own list.
func gapsOf(ps []gapPosting) ([]Gap, []InferredItem) {
	bySkill := map[string]*Gap{}
	seen := map[string]map[int64]string{} // skill -> report -> strongest class
	var inferred []InferredItem
	for _, p := range ps {
		unsupported := map[string]bool{}
		for _, id := range p.card.Unsupported {
			unsupported[id] = true
		}
		add := func(items []Item, class string) {
			for _, it := range items {
				key := SkillKey(it.Label)
				if key == "" || it.Hit >= 1 {
					continue
				}
				src := GapSource{Report: p.row.Number, Company: p.row.Company, Role: p.row.Role, Requirement: strings.TrimSpace(it.Label),
					Quote: strings.TrimSpace(it.Source), Class: class, Hit: it.Hit, Unsupported: unsupported[it.ID]}
				if it.Hit > 0 {
					src.Evidence = strings.TrimSpace(it.Evidence)
				}
				gap := bySkill[key]
				if gap == nil {
					gap = &Gap{Skill: key, Label: src.Requirement}
					bySkill[key] = gap
					seen[key] = map[int64]string{}
				}
				gap.Sources = append(gap.Sources, src)
				if seen[key][p.row.Number] != ClassRequired {
					seen[key][p.row.Number] = class
				}
			}
		}
		add(p.card.Items.Must, ClassRequired)
		add(p.card.Items.Nice, ClassPreferred)
		for _, it := range p.card.Items.Hidden {
			if it.Hit < 1 && SkillKey(it.Label) != "" {
				inferred = append(inferred, InferredItem{Report: p.row.Number, Company: p.row.Company, Role: p.row.Role, Label: strings.TrimSpace(it.Label), Evidence: strings.TrimSpace(it.Evidence)})
			}
		}
	}
	gaps := make([]Gap, 0, len(bySkill))
	for key, gap := range bySkill {
		for _, class := range seen[key] {
			if class == ClassRequired {
				gap.Required++
			} else {
				gap.Preferred++
			}
		}
		gaps = append(gaps, *gap)
	}
	slices.SortFunc(gaps, func(a, b Gap) int {
		switch {
		case a.Required != b.Required:
			return b.Required - a.Required
		case a.Preferred != b.Preferred:
			return b.Preferred - a.Preferred
		}
		return strings.Compare(a.Skill, b.Skill)
	})
	return gaps, inferred
}

// gapState is a gap's state given the skills the user confirmed. A partial hit
// or a claimed hit with no profile source needs a look before it counts as
// anything; the rest is plain absence of evidence.
func gapState(g Gap, confirmed map[string]bool) string {
	if confirmed[g.Skill] {
		return GapConfirmed
	}
	for _, s := range g.Sources {
		if s.Hit > 0 || s.Unsupported {
			return GapNeedsReview
		}
	}
	return GapNotEvidenced
}

// analyze builds the artifact for a set of reports. It reads saved report
// evidence only: no model is called and nothing outside the gap tables
// changes.
func (g *Gaps) analyze(ctx context.Context, numbers []int64) (GapReport, error) {
	ps, excluded, err := g.readPostings(ctx, numbers)
	if err != nil {
		return GapReport{}, err
	}
	entries, err := trackerEntries(ctx, g.st)
	if err != nil {
		return GapReport{}, err
	}
	kept, dropped := dedupePostings(ps, entries)
	excluded = append(excluded, dropped...)
	slices.SortFunc(excluded, func(a, b GapExclusion) int { return int(a.Report - b.Report) })
	rep := GapReport{GeneratedAt: g.now().UTC().Format(time.RFC3339), Selected: numbers, Sample: []GapInput{}, Excluded: nonNil(excluded), Done: []string{}}
	for _, p := range kept {
		rep.Sample = append(rep.Sample, GapInput{Report: p.row.Number, CreatedAt: p.row.CreatedAt.UTC().Format(time.RFC3339), ContentHash: p.hash})
	}
	slices.SortFunc(rep.Sample, func(a, b GapInput) int { return int(a.Report - b.Report) })
	rep.Gaps, rep.Inferred = gapsOf(kept)
	if rep.Inferred == nil {
		rep.Inferred = []InferredItem{}
	}
	return rep, nil
}
