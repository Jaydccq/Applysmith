package discovery

import (
	"strings"

	"applysmith/internal/gates"
	"applysmith/internal/identity"
	"applysmith/internal/workspace"
)

type boostTerm struct {
	term   term
	weight int
}

// TitleRule is a compiled title filter: positive and negative terms and the
// seniority weights.
type TitleRule struct {
	positive, negative []term
	boost              []boostTerm
}

// NewTitleRule compiles a portals title filter.
func NewTitleRule(f workspace.TitleFilter) TitleRule {
	r := TitleRule{positive: compileWith(newEveryWordTerm, f.Positive, true), negative: compileTerms(f.Negative, true)}
	for _, b := range f.SeniorityBoost {
		if t, ok := newTerm(b.Term, true); ok {
			r.boost = append(r.boost, boostTerm{t, b.Weight})
		}
	}
	return r
}

// TitleResult is the verdict on one title.
type TitleResult struct {
	Pass   bool
	Reason string   // why a title failed
	Hits   []string // positive terms found
	// Boost is the sum of the seniority weights found; Boosted is false when no
	// seniority terms are configured.
	Boost   int
	Boosted bool
	Terms   []string
}

// Check applies the rule: a title passes when it has at least one positive
// term (or there are none) and no negative term. Negative wins.
func (r TitleRule) Check(title string) TitleResult {
	var res TitleResult
	if t, ok := anyMatch(r.negative, title); ok {
		res.Reason = "negative:" + t
		return res
	}
	for _, t := range r.positive {
		if t.match(title) {
			res.Hits = append(res.Hits, t.text)
		}
	}
	if len(r.positive) > 0 && len(res.Hits) == 0 {
		res.Reason = "no-positive"
		return res
	}
	res.Pass = true
	if len(r.boost) > 0 {
		res.Boosted = true
		st := gates.SeniorityText(title)
		for _, b := range r.boost {
			if b.term.match(st) {
				res.Boost += b.weight
				res.Terms = append(res.Terms, b.term.text)
			}
		}
	}
	return res
}

// Filters is everything a scan filters with.
type Filters struct {
	Title   TitleRule
	include []term
	exclude []term
	skip    []term
	blocked map[string]bool
}

// NewFilters compiles the portals filters. blocked holds the normalized names
// of the blocked-companies store.
func NewFilters(p workspace.Portals, blocked []string) Filters {
	f := Filters{
		Title:   NewTitleRule(p.Filters.Title),
		include: compileTerms(p.Filters.Locations.Include, false),
		exclude: compileTerms(p.Filters.Locations.Exclude, false),
		skip:    compileTerms(p.Filters.SkipCompanies, false),
		blocked: map[string]bool{},
	}
	for _, b := range blocked {
		f.blocked[b] = true
	}
	return f
}

// CompanyBlocked reports whether a company is dropped before any detail
// fetch: it is in the blocked store (normalized equality) or matches a
// skip_companies entry by whole word.
func (f Filters) CompanyBlocked(company string) bool {
	if strings.TrimSpace(company) == "" {
		return false
	}
	for _, k := range identity.CompanyKeys(company) {
		if f.blocked[k] {
			return true
		}
	}
	_, hit := anyMatch(f.skip, company)
	return hit
}

// LocationOK applies the include and exclude lists; exclude wins.
func (f Filters) LocationOK(loc string) bool {
	if _, hit := anyMatch(f.exclude, loc); hit {
		return false
	}
	if len(f.include) == 0 {
		return true
	}
	_, hit := anyMatch(f.include, loc)
	return hit
}

// NormalizeCompany is the form the blocked-companies store compares on.
func NormalizeCompany(name string) string {
	if k := identity.CompanyKeys(name); len(k) > 0 {
		return k[0]
	}
	return ""
}
