package match

import (
	"fmt"
	"testing"
)

type orderMode int

const (
	lastFirst  orderMode = iota // the last route of A precedes the first route of B
	firstFirst                  // the first route of A precedes the first route of B
	firstLast                   // the first route of A precedes the last route of B
)

type orderPair struct {
	before, after string
	mode          orderMode
}

// mustPrecede are the documented pairs: a route sorted later would let a broader
// route claim the question first, so reordering one fails the build.
var mustPrecede = []orderPair{
	{"primary-cloud-platform", "skill-experience-knowledge-gate", lastFirst},
	{"sponsorship-now-or-future", "work-auth-us", firstFirst},
	{"permanent-work-authorization", "work-auth-us", lastFirst},
	{"work-auth-and-future-sponsorship", "work-auth-us", lastFirst},
	{"based-in-us-or-canada", "currently-living-us-or-canada", lastFirst},
	{"us-based-with-work-permit", "currently-living-us-or-canada", lastFirst},
	{"skill-experience-knowledge-gate", "language-proficiency", firstFirst},
	{"skill-experience-knowledge-gate", "years-experience-total", firstFirst},
	{"speaks-non-english-language", "language-proficiency", lastFirst},
	{"full-time-start-availability", "available-full-time", lastFirst},
	{"total-cash-compensation", "salary-expectation", lastFirst},
	{"referral-source-specify", "referral-source", lastFirst},
	{"ai-future-jobs-consent", "privacy-policy-consent", lastFirst},
	{"consider-other-roles-consent", "privacy-policy-consent", lastFirst},
	{"years-experience-python", "years-experience-total", lastFirst},
	{"current-level-education-pursuing", "highest-level-education", lastFirst},
	{"undergraduate-major", "degree-focus-pursuing", lastFirst},
	{"undergraduate-major", "field-of-study-choice", firstLast},
	{"graduation-year-month", "highest-level-education", lastFirst},
	{"has-stem-degree", "highest-level-education", lastFirst},
	{"expected-graduation-date", "expected-graduation-term", lastFirst},
	{"us-person-itar-ear", "us-citizen-lpr-asylee-refugee", lastFirst},
	{"previously-applied-to-company", "terminated-suspended", lastFirst},
	{"external-auditor-family", "audit-firm-employment", lastFirst},
	{"external-auditor-employer", "audit-firm-employment", lastFirst},
	{"clearance-citizenship-gated", "security-clearance", lastFirst},
	{"willing-to-obtain-clearance", "security-clearance", lastFirst},
	{"clearance-active-status", "security-clearance", lastFirst},
	{"intend-to-work-in-posted-state", "address-state", lastFirst},
	{"live-in-listed-states", "address-state", lastFirst},
	{"years-relevant-experience", "years-experience-total", lastFirst},
	{"years-full-time-work-experience-number", "years-experience-total", lastFirst},
	{"eeo-lgbtq-identification", "eeo-orientation", lastFirst},
	{"previously-interviewed-company", "employer-history", lastFirst},
	{"employer-history", "currently-employed-by-this-company", lastFirst},
	{"age-minimum-threshold", "age-18", lastFirst},
	{"wotc-felony-year-before-hire", "criminal-conviction-history", lastFirst},
	{"referrer-name", "referred-by-current-employee", lastFirst},
	{"w2-only-acceptable", "c2c-preference", lastFirst},
	{"most-recent-school", "did-you-graduate", firstFirst},
	{"currently-located-outside-us", "address-city", lastFirst},
	{"audit-firms-worked-at", "audit-firm-employment", lastFirst},
	{"meets-role-requirements", "earliest-start-date", lastFirst},
	{"built-ai-ml-project", "portfolio-url", firstLast},
	{"privacy-policy-consent", "truth-certification", firstFirst},
}

// beforeGenericConsent must precede the id-less consent acknowledge route, which
// would otherwise capture a Yes/No picker.
var beforeGenericConsent = []string{"aedt-consent", "marketing-text-consent", "healthcare-professional-license", "interview-recording-consent"}

func priorities(rs []compiledRoute, target string) (first, last int, ok bool) {
	for _, r := range rs {
		if r.Target != target {
			continue
		}
		if !ok {
			first = r.Priority
		}
		last, ok = r.Priority, true
	}
	return first, last, ok
}

// pairViolations lists the pairs whose routes the catalog orders the wrong way.
func pairViolations(rs []compiledRoute, pairs []orderPair) []string {
	var out []string
	for _, p := range pairs {
		bFirst, bLast, bOK := priorities(rs, p.before)
		aFirst, aLast, aOK := priorities(rs, p.after)
		if !bOK || !aOK {
			continue // a pair applies once both families exist in the catalog
		}
		var ok bool
		switch p.mode {
		case lastFirst:
			ok = bLast < aFirst
		case firstFirst:
			ok = bFirst < aFirst
		case firstLast:
			ok = bFirst < aLast
		}
		if !ok {
			out = append(out, fmt.Sprintf("%s (%d..%d) must precede %s (%d..%d)", p.before, bFirst, bLast, p.after, aFirst, aLast))
		}
	}
	return out
}

func TestRoutes_MustPrecedePairs(t *testing.T) {
	rs := routeCatalog()
	for _, v := range pairViolations(rs, mustPrecede) {
		t.Error(v)
	}
	for _, id := range beforeGenericConsent {
		_, last, ok := priorities(rs, id)
		if ok && last >= 494 {
			t.Errorf("%s (last %d) must precede the generic consent route at 494", id, last)
		}
	}
}

func TestRoutes_PairsAreSensitiveToReordering(t *testing.T) {
	rs := []Route{{Priority: 2, Target: "external-auditor-employer", Pattern: "x"}, {Priority: 1, Target: "audit-firm-employment", Pattern: "x"}}
	cs, err := compileRoutes(rs)
	if err != nil {
		t.Fatal(err)
	}
	if v := pairViolations(cs, mustPrecede); len(v) != 1 {
		t.Fatalf("the reordered pair must be reported once: %v", v)
	}
}
