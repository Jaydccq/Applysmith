package signals

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"applysmith/internal/discovery"
	"applysmith/internal/workspace"
)

func TestGate_ListsEveryFailingReasonAndRecordsVerdict(t *testing.T) {
	e := newEnv(t)
	e.profile()
	g := NewGater(e.sets.Profile)
	cases := []struct {
		title, loc, desc string
		want             []string
	}{
		{"Senior Software Engineer", "Berlin, Germany", "", []string{ReasonSeniorityExclude, ReasonRegionMismatch}},
		{"Account Executive", "New York, NY", "", []string{ReasonRoleMismatch}},
		{"Software Engineer", "Austin, TX", "", nil},
		{"Software Engineer II", "Unknown Place", "", nil},
		{"Head of Engineering, Software", "Paris, France", "", []string{ReasonSeniorityExclude, ReasonRegionMismatch}},
		{"Sr. Software Engineer", "Austin", "", []string{ReasonSeniorityExclude}},
		{"Software Engineering Manager", "Austin", "", []string{ReasonSeniorityExclude}},
		{"Member of Technical Staff - Software Engineer", "Austin, TX", "", nil},
		{"Staff Software Engineer", "Austin, TX", "", []string{ReasonSeniorityExclude}},
		// A remote posting placed in another country is held to the region
		// rule like any other, though the profile takes remote work; one that
		// names no place, or names the United States, passes.
		{"Software Engineer", "Zurich, Switzerland", "", []string{ReasonRegionMismatch}},
		{"Software Engineer", "Remote - Singapore", "", []string{ReasonRegionMismatch}},
		{"Software Engineer", "Remote", "", nil},
		{"Software Engineer", "Remote (US or Canada)", "", nil},
	}
	for _, c := range cases {
		got := g.Check(c.title, c.loc, c.desc)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%q @ %q = %v, want %v", c.title, c.loc, got, c.want)
		}
	}
	e.sets.Profile.WorkAuthorization.RequiresSponsorshipNow = boolp(true)
	g = NewGater(e.sets.Profile)
	if got := g.Check("Software Engineer", "Austin, TX", "We are unable to sponsor work visas."); len(got) != 1 || got[0] != ReasonNoSponsorship {
		t.Errorf("sponsorship = %v", got)
	}
	if got := g.Check("Software Engineer", "Austin, TX", "Visa sponsorship is available."); len(got) != 0 {
		t.Errorf("sponsorship offered = %v", got)
	}
	e.sets.Profile.WorkAuthorization.RequiresSponsorshipNow = boolp(false)
	if got := NewGater(e.sets.Profile).Check("Software Engineer", "Austin", "No sponsorship."); len(got) != 0 {
		t.Errorf("sponsorship not required = %v", got)
	}
	e.sets.Profile.EligibilityGate.RejectSeniority = []string{"intern"}
	if got := NewGater(e.sets.Profile).Check("Senior Software Engineer", "Austin", ""); len(got) != 0 {
		t.Errorf("custom seniority list ignored: %v", got)
	}
	// With no target roles the lane rule passes; with no regions the region rule does.
	var bare workspace.Profile
	if got := NewGater(bare).Check("Account Executive", "Berlin", ""); len(got) != 0 {
		t.Errorf("bare profile = %v", got)
	}

	// The verdict is stored at scan time with every reason.
	c := e.hot("Example Corp")
	e.identify(c, discovery.Greenhouse, "examplecorp")
	e.greenhouse("examplecorp", ghJob{1, "Senior Software Engineer", "Berlin, Germany", ""}, ghJob{2, "Software Engineer", "Remote - US", ""})
	e.sets.Profile.EligibilityGate.RejectSeniority = nil
	e.sets.Profile.WorkAuthorization.RequiresSponsorshipNow = nil
	sum := e.tick()
	if sum.Check.Seen != 2 || sum.Check.Passed != 1 {
		t.Fatalf("scan = %+v", sum.Check)
	}
	posts, _ := e.svc.repo.Postings(e.ctx, c.ID, false)
	for _, p := range posts {
		if p.ExternalID == "1" && (p.Verdict != "fail" || strings.Join(p.Reasons, ",") != "seniority_excluded,region_mismatch") {
			t.Fatalf("verdict = %+v", p)
		}
	}
}

func TestScan_PostingSeenOncePerATSAndExternalID(t *testing.T) {
	e := newEnv(t)
	e.profile()
	c := e.hot("Example Corp")
	e.identify(c, discovery.Greenhouse, "examplecorp")
	e.greenhouse("examplecorp", ghJob{1, "Software Engineer", "Remote - US", ""})
	e.tick()
	e.clk.advance(25 * time.Hour)
	// The profile changes: the stored verdict is not recomputed.
	e.sets.Profile.EligibilityGate.RejectSeniority = []string{"software"}
	sum := e.tick()
	if sum.Check.Scanned != 1 || sum.Check.Seen != 0 {
		t.Fatalf("second scan = %+v", sum.Check)
	}
	if n := e.count(`SELECT count(*) FROM signal_postings`); n != 1 {
		t.Fatalf("postings = %d", n)
	}
	posts, _ := e.svc.repo.Postings(e.ctx, c.ID, false)
	if posts[0].Verdict != "pass" {
		t.Fatalf("verdict = %s", posts[0].Verdict)
	}
}

func TestScan_RevisionInsideBandDoesNotRetrigger(t *testing.T) {
	e := newEnv(t)
	e.profile()
	c := e.hot("Example Corp")
	e.identify(c, discovery.Greenhouse, "examplecorp")
	e.greenhouse("examplecorp", ghJob{1, "Software Engineer", "Remote - US", ""})
	if sum := e.tick(); sum.Check.Scanned != 1 {
		t.Fatalf("first = %+v", sum.Check)
	}
	e.clk.advance(2 * time.Hour)
	if sum := e.tick(); sum.Check.Scanned != 0 {
		t.Fatalf("a check re-ran inside the cooldown: %+v", sum.Check)
	}
	e.clk.advance(23 * time.Hour)
	if sum := e.tick(); sum.Check.Scanned != 1 {
		t.Fatalf("the next window did not check: %+v", sum.Check)
	}
}

func TestTiers_BudgetOverflowDeferredAndAudited(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 8; i++ {
		c := e.hot(fmt.Sprintf("Company%c Systems", 'A'+i))
		e.identify(c, discovery.Greenhouse, fmt.Sprintf("company%c", 'a'+i))
		e.greenhouse(fmt.Sprintf("company%c", 'a'+i), ghJob{1, "Software Engineer", "Remote - US", ""})
	}
	// Scan priorities that differ per company, so the scan order is the only variable.
	for i := 0; i < 8; i++ {
		e.exec(`UPDATE signal_tiers SET priority = ?, momentum = ? WHERE company_id = ?`, 1.0+float64(i)/10, 1.0+float64(i)/10, i+1)
	}
	st := Settings{Portals: e.sets.Portals, Profile: e.sets.Profile}
	stats, err := e.svc.checkDue(e.ctx, 0, st, e.clk.now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Scanned != 5 || stats.Deferred != 3 {
		t.Fatalf("stats = %+v", stats)
	}
	lines, _ := e.svc.repo.AuditList(e.ctx, "deferred", 10)
	if len(lines) != 3 {
		t.Fatalf("deferral lines = %d", len(lines))
	}
	// The five checked are the highest priority (H, G, F, E, D).
	checked := e.count(`SELECT count(*) FROM signal_checks WHERE company_id IN (SELECT company_id FROM signal_tiers WHERE priority >= 1.3)`)
	if checked != 5 {
		t.Fatalf("highest-priority companies checked = %d", checked)
	}
	// Deferred ones stay due.
	due, _, _ := e.svc.repo.dueCompanies(e.ctx, e.clk.now())
	if len(due[TierHot]) != 3 {
		t.Fatalf("still due = %d", len(due[TierHot]))
	}
}

func TestSignals_Tick_BudgetReachedLeavesCompaniesDue(t *testing.T) {
	e := newEnv(t)
	e.profile()
	for i := 0; i < 3; i++ {
		c := e.hot(fmt.Sprintf("Company%c Systems", 'A'+i))
		e.identify(c, discovery.Greenhouse, fmt.Sprintf("company%c", 'a'+i))
		e.greenhouse(fmt.Sprintf("company%c", 'a'+i), ghJob{1, "Software Engineer", "Remote - US", ""})
	}
	// The check's share of the budget is spent before the first company:
	// every one stays due.
	e.svc.d.Budget = time.Minute
	e.svc.hook = func(stage string) error {
		if stage == "check" {
			e.clk.advance(e.svc.d.Budget * 19 / 20)
		}
		return nil
	}
	sum := e.tick()
	e.svc.hook = nil
	if sum.Status != TickCompleted || !sum.BudgetReached || sum.Check.Scanned != 0 {
		t.Fatalf("summary = %+v", sum)
	}
	due, _, _ := e.svc.repo.dueCompanies(e.ctx, e.clk.now())
	if len(due[TierHot]) != 3 {
		t.Fatalf("due = %d", len(due[TierHot]))
	}
	e.svc.d.Budget = 0
	if sum := e.tick(); sum.Check.Scanned != 3 || sum.BudgetReached {
		t.Fatalf("next tick = %+v", sum.Check)
	}
}

func TestSignals_DismissAuditedAndExcludedFromAdmission(t *testing.T) {
	dismissedCompanyIsNeverAdmitted(t)
}

func dismissedCompanyIsNeverAdmitted(t *testing.T) {
	e := newEnv(t)
	c := e.hot("Example Corp")
	e.identify(c, discovery.Greenhouse, "examplecorp")
	e.greenhouse("examplecorp", ghJob{1, "Software Engineer", "Remote - US", ""})
	if err := e.svc.Dismiss(e.ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	sum := e.tick()
	if sum.Check.Scanned != 0 || sum.Notified.Hot != 0 || len(e.note.sent(NoticeHot)) != 0 {
		t.Fatalf("a dismissed company was admitted: %+v", sum)
	}
	if n := e.count(`SELECT count(*) FROM signal_audit WHERE kind = 'state' AND detail = 'dismissed'`); n != 1 {
		t.Fatalf("dismissal audited %d times", n)
	}
	if err := e.svc.Watch(e.ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if sum := e.tick(); sum.Check.Scanned != 1 {
		t.Fatalf("watched again but not admitted: %+v", sum.Check)
	}
}

// ---- resolver

func (e *env) ashbyBoard(slug string) {
	e.web.body("api.ashbyhq.com", "/posting-api/job-board/"+slug, "application/json",
		fmt.Sprintf(`{"jobs":[{"id":"j1","title":"Software Engineer","location":"Remote","isRemote":true,"jobUrl":"https://jobs.ashbyhq.com/%s/j1","descriptionPlain":"Build things"}]}`, slug))
}

func TestResolver_PostHarvestPrecedesGuessedProbing_ShortenerExpandedBounded(t *testing.T) {
	e := newEnv(t)
	c, _, _ := e.company("Example Corp", "Example Corp raised $20M. Careers: https://bit.ly/examplejobs", epoch.Add(-time.Hour), adapterAlerts)
	e.web.on("bit.ly", "/examplejobs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://jobs.ashbyhq.com/examplecorp")
		w.WriteHeader(http.StatusMovedPermanently)
	})
	id, err := e.svc.ensureIdentity(e.ctx, c, e.sets, new(int))
	if err != nil {
		t.Fatal(err)
	}
	if id.Status != IdentityResolved || id.ATS != discovery.Ashby || id.Slug != "examplecorp" || id.Lane != "evidence-link" {
		t.Fatalf("identity = %+v", id)
	}
	for _, h := range []string{"api.ashbyhq.com", "boards-api.greenhouse.io", "api.lever.co", "api.smartrecruiters.com", "apply.workable.com"} {
		if n := len(e.web.requests(h)); n != 0 {
			t.Fatalf("%d guessed probes reached %s before the harvested board", n, h)
		}
	}
	// At most five expansions per company.
	e2 := newEnv(t)
	var links []string
	for i := 0; i < 8; i++ {
		links = append(links, fmt.Sprintf("https://bit.ly/x%d", i))
	}
	c2, _, _ := e2.company("Beta Corp", "Beta Corp raised $20M. "+strings.Join(links, " "), epoch.Add(-time.Hour), adapterAlerts)
	e2.web.on("bit.ly", "/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://beta-news.test/story")
		w.WriteHeader(http.StatusFound)
	})
	if _, err := e2.svc.ensureIdentity(e2.ctx, c2, e2.sets, new(int)); err != nil {
		t.Fatal(err)
	}
	if n := len(e2.web.requests("bit.ly")); n > 5 {
		t.Fatalf("%d expansions for one company", n)
	}
}

func TestResolver_DomainFromUnwrappedRedirectAndEscapedBodyURL(t *testing.T) {
	wrapped := StoredDoc{URL: "https://www.google.com/url?rct=j&sa=t&url=https%3A%2F%2Fexample-corp.test%2Fblog%2Fraise&ct=ga", Body: "x"}
	if got := InferDomain([]StoredDoc{wrapped}, "example corp"); got != "example-corp.test" {
		t.Fatalf("wrapper domain = %q", got)
	}
	body := StoredDoc{URL: "https://techcrunch.com/2026/10/01/x", Body: "See https://www.linkedin.com/company/x and &lt;a href=&quot;https://acme-labs.test/about&quot;&gt; or visit zeta-robotics.test"}
	if got := InferDomain([]StoredDoc{body}, "acme"); got != "acme-labs.test" {
		t.Fatalf("body domain = %q", got)
	}
	if got := InferDomain([]StoredDoc{{URL: "https://news.ycombinator.com/item?id=1", Body: "see twitter.com/x and bing.com"}}, "x"); got != "" {
		t.Fatalf("an excluded host became the domain: %q", got)
	}

	e := newEnv(t)
	c, _, _ := e.company("Example Corp", "Example Corp raised $20M.", epoch.Add(-time.Hour), adapterAlerts)
	e.exec(`UPDATE signal_documents SET url = ?, canon_url = ?`, wrapped.URL, wrapped.URL)
	e.web.body("example-corp.test", "/careers", "text/html", `<html><a href="https://boards.greenhouse.io/examplecorp">Open roles</a></html>`)
	id, err := e.svc.ensureIdentity(e.ctx, c, e.sets, new(int))
	if err != nil || id.Status != IdentityResolved || id.ATS != discovery.Greenhouse || id.Slug != "examplecorp" || id.Lane != "careers-link" {
		t.Fatalf("identity = %+v, %v", id, err)
	}
	if len(e.web.requests("example-corp.test")) == 0 {
		t.Fatal("the unwrapped domain was not read")
	}
}

const jobPostingPage = `<html><script type="application/ld+json">{"@context":"https://schema.org","@type":"JobPosting","title":"Software Engineer","identifier":{"@type":"PropertyValue","value":"se-1"},
"url":"https://example-corp.test/careers/se-1","description":"<p>Build things. We cannot sponsor visas.</p>","jobLocation":{"@type":"Place","address":{"addressLocality":"Austin","addressRegion":"TX","addressCountry":"US"}}}</script>
<script type="application/ld+json">{ not json</script></html>`

func TestResolver_CareersJSONLDResolvesGenericBoard(t *testing.T) {
	e := newEnv(t)
	e.profile()
	c, _, _ := e.company("Example Corp", "Example Corp raised $20M at https://example-corp.test", epoch.Add(-time.Hour), adapterAlerts)
	e.web.body("example-corp.test", "/careers", "text/html", jobPostingPage)
	id, err := e.svc.ensureIdentity(e.ctx, c, e.sets, new(int))
	if err != nil || id.ATS != genericATS || id.Status != IdentityResolved || id.Slug != "https://example-corp.test/careers" {
		t.Fatalf("identity = %+v, %v", id, err)
	}
	st := Settings{Portals: e.sets.Portals, Profile: e.sets.Profile}
	var stats CheckStats
	if err := e.svc.checkCompany(e.ctx, c, st, NewGater(e.sets.Profile), new(int), &stats); err != nil {
		t.Fatal(err)
	}
	posts, _ := e.svc.repo.Postings(e.ctx, c.ID, false)
	if len(posts) != 1 || posts[0].Title != "Software Engineer" || posts[0].Location != "Austin, TX, US" || posts[0].Verdict != "pass" || !strings.Contains(posts[0].Description, "cannot sponsor") {
		t.Fatalf("postings = %+v", posts)
	}
}

func TestResolver_UnstructuredCareersPageNeverModelExtracted(t *testing.T) {
	e := newEnv(t)
	c, _, _ := e.company("Example Corp", "Example Corp raised $20M at https://example-corp.test", epoch.Add(-time.Hour), adapterAlerts)
	e.web.body("example-corp.test", "/careers", "text/html", `<html><h1>Careers</h1><p>We are hiring a few engineers, write to us.</p></html>`)
	before := e.brain.total()
	id, err := e.svc.ensureIdentity(e.ctx, c, e.sets, new(int))
	if err != nil || id.Status != IdentityNoATS || id.CareersURL != "https://example-corp.test/careers" {
		t.Fatalf("identity = %+v, %v", id, err)
	}
	if err := e.svc.noATSLead(e.ctx, c, id); err != nil {
		t.Fatal(err)
	}
	leads := e.leads.byCategory(LeadCareersPage)
	if len(leads) != 1 || leads[0].ApplyLink != "https://example-corp.test/careers" {
		t.Fatalf("leads = %+v", e.leads.leads)
	}
	if e.brain.total() != before {
		t.Fatal("a model was called to read the careers page")
	}
}

func TestResolve_OfficialCareersPageFollowedAggregatorNeverRecorded(t *testing.T) {
	e := newEnv(t)
	c, _, _ := e.company("Example Corp", "Example Corp raised $20M and is hiring. Official site: example-corp.test", epoch.Add(-time.Hour), adapterAlerts)
	aggregator := "https://www.ziprecruiter.com/c/Example-Corp/Jobs"
	e.exec(`UPDATE signal_documents SET url = ?, canon_url = ?`, aggregator, aggregator)
	e.web.body("example-corp.test", "/jobs", "text/html", `<html><h1>Open positions</h1><p>Write to us.</p></html>`)
	cs := &countingSearch{url: aggregator}
	e.svc.d.Search = cs
	searches := searchesPerTick
	id, err := e.svc.ensureIdentity(e.ctx, c, e.sets, &searches)
	if err != nil || id.Status != IdentityNoATS || id.CareersURL != "https://example-corp.test/jobs" {
		t.Fatalf("identity = %+v, %v", id, err)
	}
	if cs.calls == 0 {
		t.Fatal("the name-keyed search never ran, so the aggregator result was not offered")
	}
	if n := len(e.web.requests("www.ziprecruiter.com")); n != 0 {
		t.Fatalf("the aggregator was read %d times", n)
	}
	if err := e.svc.noATSLead(e.ctx, c, id); err != nil {
		t.Fatal(err)
	}
	leads := e.leads.byCategory(LeadCareersPage)
	if len(leads) != 1 || leads[0].ApplyLink != "https://example-corp.test/jobs" {
		t.Fatalf("leads = %+v", e.leads.leads)
	}
	if posts, _ := e.svc.repo.Postings(e.ctx, c.ID, false); len(posts) != 0 {
		t.Fatalf("a careers page without structured jobs produced postings: %+v", posts)
	}
}

type countingSearch struct {
	calls int
	url   string
}

func (c *countingSearch) Name() string { return "fake" }
func (c *countingSearch) Search(_ context.Context, q string, n int, _ Scope) ([]SearchResult, error) {
	c.calls++
	return []SearchResult{{URL: c.url}}, nil
}

func TestResolver_NameKeyedSearchWithinPerTickBudget(t *testing.T) {
	e := newEnv(t)
	cs := &countingSearch{url: "https://jobs.lever.co/found-board"}
	e.svc.d.Search = cs
	searches := searchesPerTick
	for i := 0; i < 10; i++ {
		c, err := e.svc.repo.UpsertCompany(e.ctx, fmt.Sprintf("Zzq%c Qqx", 'a'+i), fmt.Sprintf("zzq%c qqx", 'a'+i))
		if err != nil {
			t.Fatal(err)
		}
		// Search is the lane that finds it, so make the guessed slugs miss.
		id, err := e.svc.ensureIdentity(e.ctx, c, e.sets, &searches)
		if err != nil {
			t.Fatal(err)
		}
		if i < 8 && (id.Status != IdentityResolved || id.Lane != "search" || id.ATS != discovery.Lever) {
			t.Fatalf("company %d identity = %+v", i, id)
		}
	}
	if cs.calls != 8 {
		t.Fatalf("searches = %d, want 8", cs.calls)
	}
}

func TestResolver_StaleNegativeReprobedAfterFourteenDays_NoLaneNotPersisted(t *testing.T) {
	e := newEnv(t)
	c, err := e.svc.repo.UpsertCompany(e.ctx, "Example Corp", "example")
	if err != nil {
		t.Fatal(err)
	}
	put := func(status string, age time.Duration, version int) {
		e.exec(`DELETE FROM signal_ats_identities`)
		e.exec(`INSERT INTO signal_ats_identities (company_id, status, ats, slug, resolver_version, resolved_at) VALUES (?, ?, '', '', ?, ?)`,
			c.ID, status, version, fmtTS(e.clk.now().Add(-age)))
	}
	day := 24 * time.Hour
	now := e.clk.now()
	for _, tc := range []struct {
		status  string
		age     time.Duration
		version int
		want    bool
	}{
		{IdentityNoATS, 15 * day, ResolverVersion, true}, {IdentityNoATS, 13 * day, ResolverVersion, false}, {IdentityUnresolved, 2 * day, ResolverVersion - 1, true},
		{IdentityResolved, 90 * day, ResolverVersion - 1, false},
	} {
		put(tc.status, tc.age, tc.version)
		id, have, _ := e.svc.repo.Identity(e.ctx, c.ID)
		if got := needsResolve(id, have, now); got != tc.want {
			t.Errorf("%s age %v v%d: needsResolve = %v", tc.status, tc.age, tc.version, got)
		}
	}
	if !needsResolve(Identity{}, false, now) {
		t.Error("no identity must resolve")
	}
	// The stale negative is probed again and the new answer replaces it.
	put(IdentityNoATS, 15*day, ResolverVersion)
	e.ashbyBoard("example")
	id, err := e.svc.ensureIdentity(e.ctx, c, e.sets, new(int))
	if err != nil || id.Status != IdentityResolved || id.ATS != discovery.Ashby {
		t.Fatalf("identity = %+v, %v", id, err)
	}
	reqs := len(e.web.requests(""))
	if _, err := e.svc.ensureIdentity(e.ctx, c, e.sets, new(int)); err != nil || len(e.web.requests("")) != reqs {
		t.Fatalf("a resolved identity was probed again")
	}

	// No lane could run (no usable name, no evidence, the search budget is
	// spent): nothing is cached, so a later tick tries again.
	tiny, _ := e.svc.repo.UpsertCompany(e.ctx, "Q1", "q1")
	cs := &countingSearch{url: "https://jobs.lever.co/q1"}
	e.svc.d.Search = cs
	zero := 0
	got, err := e.svc.ensureIdentity(e.ctx, tiny, e.sets, &zero)
	if err != nil || got.Status == IdentityResolved {
		t.Fatalf("identity = %+v, %v", got, err)
	}
	if _, have, _ := e.svc.repo.Identity(e.ctx, tiny.ID); have {
		t.Fatal("a resolution that ran no lane was cached")
	}
	one := 1
	if got, _ := e.svc.ensureIdentity(e.ctx, tiny, e.sets, &one); got.Status != IdentityResolved || got.Lane != "search" {
		t.Fatalf("the retry did not run the search: %+v", got)
	}
}

func TestResolver_WrongDomainCorrectedOnlyBySlugOverride(t *testing.T) {
	e := newEnv(t)
	c, _, _ := e.company("Example Corp", "Example Corp raised $20M at https://wrong-site.test", epoch.Add(-time.Hour), adapterAlerts)
	e.web.body("wrong-site.test", "/careers", "text/html", `<a href="https://boards.greenhouse.io/wrongco">jobs</a>`)
	id, _ := e.svc.ensureIdentity(e.ctx, c, e.sets, new(int))
	if id.Slug != "wrongco" {
		t.Fatalf("identity = %+v", id)
	}
	e.sets.Portals.Companies = []workspace.Company{{Name: "Example Corp", ATS: "lever", Slug: "examplecorp"}}
	id, err := e.svc.ensureIdentity(e.ctx, c, e.sets, new(int))
	if err != nil || id.ATS != "lever" || id.Slug != "examplecorp" || id.Lane != "override" {
		t.Fatalf("identity after the override = %+v, %v", id, err)
	}
	if cached, _, _ := e.svc.repo.Identity(e.ctx, c.ID); cached.Slug != "examplecorp" {
		t.Fatalf("cached = %+v", cached)
	}
}

func TestSignals_UnfinishedCheckRetriesSameRevisionWithoutDuplicatingPostings(t *testing.T) {
	for _, why := range []string{"cancelled", "budget", "finish-write"} {
		t.Run(why, func(t *testing.T) {
			e := newEnv(t)
			e.profile()
			e.disable(SourceHNHiring)
			c := e.hot("Example Corp")
			e.identify(c, discovery.Greenhouse, "examplecorp")
			e.greenhouse("examplecorp", ghJob{1, "Software Engineer", "Remote - US", "Build things."})
			ctx, cancel := context.WithCancel(e.ctx)
			defer cancel()
			if why == "finish-write" {
				e.exec(`CREATE TRIGGER refuse_check_finish BEFORE UPDATE ON signal_checks WHEN NEW.status = 'ok' BEGIN SELECT RAISE(FAIL, 'owned check finish refusal'); END`)
			} else {
				if why == "budget" {
					e.svc.d.Budget = time.Minute
				}
				e.web.on("", "/v1/boards/examplecorp/jobs", func(w http.ResponseWriter, r *http.Request) {
					if why == "cancelled" {
						cancel()
					} else {
						e.clk.advance(time.Minute)
					}
					select {
					case <-r.Context().Done():
					case <-time.After(10 * time.Second):
					}
				})
			}
			sum, err := e.svc.Tick(ctx, TickRequest{})
			if why == "budget" {
				if err != nil || !sum.BudgetReached || sum.Status != TickCompleted {
					t.Fatalf("budget=%+v %v", sum, err)
				}
			} else if err == nil || sum.Status != TickFailed {
				t.Fatalf("interruption=%+v %v", sum, err)
			}
			if why == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel cause=%v", err)
			}
			due, _, err := e.svc.repo.dueCompanies(e.ctx, e.clk.now())
			if err != nil || len(due[TierHot]) != 1 {
				t.Fatalf("not retryable=%v %v", due, err)
			}
			var id int64
			var revision, status string
			if err := e.st.Read().QueryRow(`SELECT id, revision_key, status FROM signal_checks`).Scan(&id, &revision, &status); err != nil || status != "started" {
				t.Fatalf("unfinished claim=%s %v", status, err)
			}
			if why == "finish-write" {
				e.exec(`DROP TRIGGER refuse_check_finish`)
				if e.count(`SELECT count(*) FROM signal_postings`) != 1 {
					t.Fatal("partial durable posting lost")
				}
			}
			e.svc.d.Budget = 0
			e.greenhouse("examplecorp", ghJob{1, "Software Engineer", "Remote - US", "Build things."})
			next := e.tick()
			if next.Check.Scanned != 1 || next.Check.AlreadyDone != 0 || next.Handoff.Inserted != 1 || e.count(`SELECT count(*) FROM signal_postings`) != 1 {
				t.Fatalf("same revision recovery=%+v", next)
			}
			if n := e.count(`SELECT count(*) FROM signal_checks WHERE id = ? AND revision_key = ? AND status = 'ok' AND tick_id = ?`, id, revision, next.TickID); n != 1 || e.count(`SELECT count(*) FROM signal_checks`) != 1 {
				t.Fatal("revision was duplicated or not completed")
			}
			if again := e.tick(); again.Check.Scanned != 0 || again.Handoff.Inserted != 0 || e.count(`SELECT count(*) FROM inbox`) != 1 {
				t.Fatalf("completed work repeated=%+v", again)
			}
		})
	}
}
