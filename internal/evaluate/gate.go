// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"applysmith/internal/discovery"
	"applysmith/internal/gates"
	"applysmith/internal/identity"
	"applysmith/internal/lazyregexp"
	"applysmith/internal/workspace"
)

// Gate rule names.
const (
	RuleSeniority  = "seniority"
	RuleGeography  = "geography"
	RuleCurrency   = "currency"
	RuleClearance  = "clearance"
	RuleUSPerson   = "us_person"
	RuleExperience = "experience"
	RuleBoard      = "board_index"
	RuleBlocked    = "blocked_employer"
)

// ladder ranks seniority terms, lowest first.
var ladder = []string{"intern", "junior", "mid", "senior", "lead", "staff", "principal", "manager", "director", "vp", "executive"}

// Sponsorship need of the profile.
type Need string

const (
	NeedNeeded     Need = "needed"
	NeedNotNeeded  Need = "not needed"
	NeedUnanswered Need = "unanswered"
)

// SponsorNeed reads the user's need from work_authorization. An unset answer
// is never read as false.
func SponsorNeed(wa workspace.WorkAuth) Need {
	now, future := wa.RequiresSponsorshipNow, wa.RequiresSponsorshipFuture
	switch {
	case now != nil && *now, future != nil && *future:
		return NeedNeeded
	case now != nil && future != nil:
		return NeedNotNeeded
	}
	return NeedUnanswered
}

// GateSettings is eligibility_gate with its derived defaults resolved. A rule
// whose setting resolves to nothing is off.
type GateSettings struct {
	RejectSeniority   []string
	AllowedCountries  []string
	AllowRemote       bool
	AllowedCurrencies []string
	RejectClearance   bool
	RejectUSPerson    bool
	MaxRequiredYears  *int
}

// ResolveGate applies the profile's eligibility_gate and the related groups.
func ResolveGate(p workspace.Profile) GateSettings {
	g, wa := p.EligibilityGate, p.WorkAuthorization
	s := GateSettings{AllowRemote: g.AllowRemote == nil || *g.AllowRemote, MaxRequiredYears: g.MaxRequiredYears}

	s.RejectSeniority = g.RejectSeniority
	if len(s.RejectSeniority) == 0 {
		s.RejectSeniority = derivedSeniority(p.Targets.Roles)
	}
	s.AllowedCountries = g.AllowedCountries
	if len(s.AllowedCountries) == 0 {
		s.AllowedCountries = wa.AuthorizedCountries
	}
	s.AllowedCurrencies = g.AllowedCurrencies
	if len(s.AllowedCurrencies) == 0 {
		for _, c := range s.AllowedCountries {
			if cur := currencyOf(strings.ToUpper(c)); cur != "" {
				s.AllowedCurrencies = append(s.AllowedCurrencies, cur)
			}
		}
	}
	need := SponsorNeed(wa)
	s.RejectClearance = need == NeedNeeded || strings.EqualFold(strings.TrimSpace(wa.SecurityClearance), "none")
	if g.RejectClearance != nil {
		s.RejectClearance = *g.RejectClearance
	}
	s.RejectUSPerson = need == NeedNeeded
	if g.RejectUSPerson != nil {
		s.RejectUSPerson = *g.RejectUSPerson
	}
	return s
}

// derivedSeniority lists every ladder term ranked above the highest target.
func derivedSeniority(roles []workspace.Role) []string {
	top := -1
	for _, r := range roles {
		for i, t := range ladder {
			if t == r.Seniority {
				top = max(top, i)
			}
		}
	}
	if top < 0 || top == len(ladder)-1 {
		return nil
	}
	return ladder[top+1:]
}

// GateInput is a posting as the gate reads it.
type GateInput struct {
	Title, Seniority, Location, Compensation, Text, URL string
	// BlockedWhy is non-empty when the employer is blocked: the personal block
	// or the employer-tier policy. The caller decides, the gate reports.
	BlockedWhy string
}

// Failure is one failed gate rule.
type Failure struct {
	Rule   string `json:"rule"`
	Detail string `json:"detail"`
}

// Gate evaluates every rule and returns every failing one. It is pure.
func Gate(in GateInput, s GateSettings) []Failure {
	var out []Failure
	fail := func(rule, format string, args ...any) { out = append(out, Failure{rule, fmt.Sprintf(format, args...)}) }

	if title := gates.SeniorityText(in.Title + " " + in.Seniority); title != "" {
		for _, term := range s.RejectSeniority {
			if seniorityMatch(term).MatchString(title) {
				fail(RuleSeniority, "the title names %q, which the profile does not target", term)
				break
			}
		}
	}
	if v := Geography(in.Location, s.AllowedCountries, s.AllowRemote); !v.Pass {
		fail(RuleGeography, "%s", v.Detail)
	}
	if bad := disallowedCurrency(in.Compensation, s.AllowedCurrencies); bad != "" {
		fail(RuleCurrency, "compensation is stated in %s, which is not an allowed currency", bad)
	}
	if s.RejectClearance && discovery.ClearanceRequired(in.Title+". "+in.Text) {
		fail(RuleClearance, "the posting requires a security clearance")
	}
	if s.RejectUSPerson && gates.RequiresUSPerson(in.Text) {
		fail(RuleUSPerson, "the posting requires US-person status")
	}
	if s.MaxRequiredYears != nil {
		if years, ok := gates.RequiredYears(in.Text); ok && years > *s.MaxRequiredYears {
			fail(RuleExperience, "the posting requires %d years, above the limit of %d", years, *s.MaxRequiredYears)
		}
	}
	if IsBoardIndex(in.URL) {
		fail(RuleBoard, "the URL names a whole job board, not one posting")
	}
	if in.BlockedWhy != "" {
		fail(RuleBlocked, "%s", in.BlockedWhy)
	}
	return out
}

func seniorityMatch(term string) *lazyregexp.Regexp {
	t := strings.TrimSpace(strings.ToLower(term))
	if t == "vp" {
		return lazyregexp.New(`(?i)\b(vp|svp|evp|vice president)\b`)
	}
	return lazyregexp.New(`(?i)\b` + regexp.QuoteMeta(t) + `\b`)
}

var currencyMarker = lazyregexp.New(`(?:^|[^A-Za-z0-9])(US|CA|C|AU|A|S|NZ|HK)\$|[\x{00A3}\x{20AC}\x{00A5}\x{20B9}\x{20A9}]|\b(USD|CAD|EUR|GBP|AUD|NZD|SGD|JPY|CNY|INR|CHF|KRW|SEK|NOK|DKK|PLN|BRL|MXN|ILS|AED)\b`)

var markerCode = map[string]string{
	"US": "USD", "CA": "CAD", "C": "CAD", "AU": "AUD", "A": "AUD", "S": "SGD", "NZ": "NZD", "HK": "HKD",
	"£": "GBP", "€": "EUR", "¥": "JPY", "₹": "INR", "₩": "KRW",
}

// disallowedCurrency returns the first currency marker in text that is not in
// the allowed set. A bare dollar sign is ambiguous and passes.
func disallowedCurrency(text string, allowed []string) string {
	if len(allowed) == 0 || text == "" {
		return ""
	}
	set := map[string]bool{}
	for _, a := range allowed {
		set[strings.ToUpper(a)] = true
	}
	for _, m := range currencyMarker.FindAllStringSubmatch(text, -1) {
		code := m[2]
		if m[1] != "" {
			code = markerCode[m[1]]
		} else if code == "" {
			code = markerCode[m[0]]
		}
		if code == "JPY" && set["CNY"] {
			continue
		}
		if code != "" && !set[code] {
			return code
		}
	}
	return ""
}

// IsBoardIndex reports whether a URL names a whole ATS board rather than one
// posting: nothing after the slug, or an embed board URL. An embed application
// URL that names one posting is not a board index.
func IsBoardIndex(raw string) bool {
	if raw == "" {
		return false
	}
	if _, ok := identity.ATSKey(raw); ok {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	var segs []string
	for _, s := range strings.Split(u.Path, "/") {
		if s != "" {
			segs = append(segs, strings.ToLower(s))
		}
	}
	switch {
	case host == "boards.greenhouse.io" || host == "job-boards.greenhouse.io" || host == "boards.eu.greenhouse.io" || host == "job-boards.eu.greenhouse.io":
		if len(segs) > 0 && segs[0] == "embed" {
			return len(segs) > 1 && segs[1] == "job_board"
		}
		return len(segs) <= 1
	case host == "jobs.lever.co" || host == "jobs.eu.lever.co" || host == "jobs.ashbyhq.com" || host == "jobs.eu.ashbyhq.com" ||
		host == "apply.workable.com" || host == "careers.smartrecruiters.com" || host == "jobs.smartrecruiters.com":
		return len(segs) <= 1 && u.Query().Get("ashby_jid") == ""
	case strings.HasSuffix(host, ".workable.com"):
		return len(segs) == 0
	}
	return false
}
