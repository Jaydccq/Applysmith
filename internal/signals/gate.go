package signals

import (
	"regexp"
	"strings"

	"applysmith/internal/discovery"
	"applysmith/internal/gates"
	"applysmith/internal/lazyregexp"
	"applysmith/internal/workspace"
)

// Gate reasons. A posting lists every one that fails.
const (
	ReasonRoleMismatch     = "role_mismatch"
	ReasonSeniorityExclude = "seniority_excluded"
	ReasonRegionMismatch   = "region_mismatch"
	ReasonNoSponsorship    = "no_sponsorship"
)

var defaultSeniority = []string{"staff", "principal", "senior", "sr.", "manager", "director", "vp", "vice president", "head of"}

var noSponsorRE = lazyregexp.New(`(?i)(?:unable|not\s+able|cannot|can't|will\s+not|won't|do(?:es)?\s+not|don't|doesn't)\s+(?:to\s+)?(?:provide\s+|offer\s+)?(?:visa\s+)?sponsor|no\s+(?:visa\s+)?sponsorship|without\s+(?:visa\s+)?sponsorship|sponsorship\s+(?:is\s+)?(?:not\s+(?:available|provided|offered)|unavailable)|must\s+(?:be\s+)?(?:legally\s+)?(?:authorized|eligible)\s+to\s+work[^.]{0,60}without\s+sponsorship`)

// Gater is the pure rule gate over a posting, built from the user's profile.
type Gater struct {
	keywords   []string
	seniority  []*lazyregexp.Regexp
	regions    []string
	scope      string
	hasRegions bool
	sponsor    bool
}

// NewGater compiles the gate from profile.yml.
func NewGater(p workspace.Profile) *Gater {
	g := &Gater{}
	var titles []string
	for _, r := range p.Targets.Roles {
		titles = append(titles, r.Title)
	}
	g.keywords = roleKeywords(titles)
	terms := p.EligibilityGate.RejectSeniority
	if len(terms) == 0 {
		terms = defaultSeniority
	}
	for _, t := range terms {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		g.seniority = append(g.seniority, lazyregexp.New(`(?:^|[^a-z])`+regexp.QuoteMeta(strings.TrimRight(t, "."))+`\.?(?:[^a-z]|$)`))
	}
	codes := map[string]bool{}
	for _, c := range p.WorkAuthorization.AuthorizedCountries {
		codes[strings.ToUpper(strings.TrimSpace(c))] = true
	}
	for _, l := range p.Targets.Locations {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		g.regions = append(g.regions, strings.ToLower(l))
		for _, mk := range markers {
			if mk.re.MatchString(l) {
				codes[mk.country] = true
			}
		}
	}
	g.hasRegions = len(g.regions) > 0 || len(p.WorkAuthorization.AuthorizedCountries) > 0
	var list []string
	for c := range codes {
		list = append(list, c)
	}
	g.scope = discovery.ScopeFromCountries(list)
	g.sponsor = (p.WorkAuthorization.RequiresSponsorshipNow != nil && *p.WorkAuthorization.RequiresSponsorshipNow) ||
		(p.WorkAuthorization.RequiresSponsorshipFuture != nil && *p.WorkAuthorization.RequiresSponsorshipFuture)
	return g
}

// NeedsSponsorship reports whether the user requires sponsorship.
func (g *Gater) NeedsSponsorship() bool { return g.sponsor }

// Check lists every failing reason; an empty result passes.
func (g *Gater) Check(title, location, description string) []string {
	var out []string
	lt := strings.ToLower(title)
	if len(g.keywords) > 0 {
		match := false
		for _, w := range strings.FieldsFunc(lt, func(r rune) bool { return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') }) {
			for _, k := range g.keywords {
				if wordMatch(w, k) {
					match = true
				}
			}
		}
		if !match {
			out = append(out, ReasonRoleMismatch)
		}
	}
	st := strings.ToLower(gates.SeniorityText(title))
	for _, re := range g.seniority {
		if re.MatchString(st) {
			out = append(out, ReasonSeniorityExclude)
			break
		}
	}
	if !g.regionOK(location) {
		out = append(out, ReasonRegionMismatch)
	}
	if g.sponsor && noSponsorRE.MatchString(description) {
		out = append(out, ReasonNoSponsorship)
	}
	return out
}

// regionOK passes a location naming a target region or inside the scope. A
// remote posting is held to the same rule: one placed in another country is
// usually remote within that country.
func (g *Gater) regionOK(location string) bool {
	if !g.hasRegions {
		return true
	}
	ll := strings.ToLower(location)
	for _, r := range g.regions {
		if strings.Contains(ll, r) {
			return true
		}
	}
	return discovery.ApplyScope(g.scope, location).Pass
}
