package discovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"applysmith/internal/workspace"
)

var pageNumRE = regexp.MustCompile(`[?&](?:start|page|p)=(\d+)`)

// pagedRoute answers every list URL with three cards numbered after the page
// the URL asks for, and every posting page with a full description.
func pagedRoute(extra func(fakePage) fakePage) func(string) fakePage {
	return func(raw string) fakePage {
		n := 0
		if m := pageNumRE.FindStringSubmatch(raw); m != nil {
			n, _ = strconv.Atoi(m[1])
		}
		pg := cardsPage(manyCards(n*10+1, 3, "Acme")...)
		pg.detail = detailRead{Desc: postingText()}
		if extra != nil {
			pg = extra(pg)
		}
		return pg
	}
}

func zipPortals(t *testing.T, q map[string]any) workspace.Portals {
	if q == nil {
		q = map[string]any{"search": "engineer", "location": "Austin"}
	}
	return portalsWith(t, eng(), "ziprecruiter", q)
}

func challengePage() fakePage {
	return fakePage{probe: pageProbe{Title: "Just a moment", Text: "Please verify you are human to continue."}}
}

func TestBrowserScan_PageFailureRetriesOnceThenSkips(t *testing.T) {
	fb := newFakeBrowser(t, pagedRoute(nil))
	fb.route = func(raw string) fakePage {
		if strings.Contains(raw, "page=3") {
			return fakePage{err: errors.New("render timeout")}
		}
		return pagedRoute(nil)(raw)
	}
	e := browserEnv(t, fb)
	rec := e.run(ScanRequest{Source: "ziprecruiter", Portals: zipPortals(t, nil), Pages: 4})
	if rec.Status != RunCompleted {
		t.Fatalf("status %s: %s %s", rec.Status, rec.Reason, rec.Error)
	}
	if rec.Detail["page-failed"] != 1 || rec.Detail["retries"] != 1 {
		t.Fatalf("detail = %v", rec.Detail)
	}
	n := 0
	for _, u := range fb.visited() {
		if strings.Contains(u, "page=3") {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("page 3 was requested %d times, want 2 (one retry)", n)
	}
	if rec.Counts.Inserted != 9 {
		t.Fatalf("inserted %d, want the 9 rows of pages 1, 2 and 4", rec.Counts.Inserted)
	}
}

func TestBrowserScan_OfflineHostAbortsTheRun(t *testing.T) {
	fb := newFakeBrowser(t, func(string) fakePage { return fakePage{err: ErrOffline} })
	rec := browserEnv(t, fb).run(ScanRequest{Source: "ziprecruiter", Portals: zipPortals(t, nil)})
	if rec.Status != RunFailed || rec.Reason != "host-offline" {
		t.Fatalf("status %s reason %q", rec.Status, rec.Reason)
	}
}

func TestBrowserScan_SignInWallEndsNeedsSignIn(t *testing.T) {
	fb := newFakeBrowser(t, func(string) fakePage {
		return fakePage{nav: Nav{URL: "https://www.linkedin.com/uas/login?session_redirect=x"}}
	})
	e := browserEnv(t, fb)
	var told string
	e.sc.NeedsSignIn = func(_ context.Context, source, _ string) { told = source }
	rec := e.run(ScanRequest{Source: "linkedin", Portals: portalsWith(t, eng(), "linkedin", map[string]any{"search": "engineer"})})
	if rec.Status != RunNeedsSignIn || told != "linkedin" {
		t.Fatalf("status %s, told %q", rec.Status, told)
	}
	if fb.count("click") != 0 {
		t.Fatal("the scan acted on the sign-in page")
	}
}

func blockedEnv(t *testing.T) (*env, *fakeBrowser) {
	fb := newFakeBrowser(t, func(string) fakePage { return challengePage() })
	return browserEnv(t, fb), fb
}

func TestBrowserScan_Challenge_SetsPerSourceCooldownAndSkipsNextRun(t *testing.T) {
	e, fb := blockedEnv(t)
	req := ScanRequest{Source: "ziprecruiter", Portals: zipPortals(t, nil)}
	first := e.run(req)
	if first.Status != RunBlocked || first.Reason != blockChallenge {
		t.Fatalf("first run: %s %q", first.Status, first.Reason)
	}
	e.clk.advance(time.Hour)
	before := len(fb.visited())
	second := e.run(req)
	if second.Status != RunSkipped || second.Reason != ReasonCooldown || !strings.Contains(second.Error, "until") {
		t.Fatalf("second run: %s %q %q", second.Status, second.Reason, second.Error)
	}
	if len(fb.visited()) != before {
		t.Fatal("a skipped run opened a page")
	}
	ignored := e.run(ScanRequest{Source: "ziprecruiter", Portals: zipPortals(t, nil), IgnoreCooldown: true})
	if ignored.Status != RunBlocked {
		t.Fatalf("--ignore-cooldown run: %s", ignored.Status)
	}
	other := e.run(ScanRequest{Source: "glassdoor", Portals: portalsWith(t, eng(), "glassdoor", map[string]any{"search": "engineer"})})
	if other.Status == RunSkipped {
		t.Fatal("the cooldown of one source stopped another")
	}
}

func TestBrowserScan_Challenge_NotRaisedFromPostingText(t *testing.T) {
	fb := newFakeBrowser(t, pagedRoute(func(p fakePage) fakePage {
		p.probe = pageProbe{Text: "The selected posting asks for identity verification before the first day."}
		p.detail.Desc = "Identity verification and a background check apply. " + postingText()
		return p
	}))
	e := browserEnv(t, fb)
	rec := e.run(ScanRequest{Source: "ziprecruiter", Portals: zipPortals(t, nil)})
	if rec.Status != RunCompleted || rec.Counts.Inserted != 3 {
		t.Fatalf("status %s inserted %d", rec.Status, rec.Counts.Inserted)
	}
	if fb.count("probe") != 0 {
		t.Fatal("the page text was read although cards had rendered")
	}
	if again := e.run(ScanRequest{Source: "ziprecruiter", Portals: zipPortals(t, nil)}); again.Status == RunSkipped {
		t.Fatal("a cooldown was set from posting text")
	}
}

func TestBrowserScan_Challenge_CheckboxPressesAreBoundedPerRun(t *testing.T) {
	// A wall that comes back after every pass, or that no press passes, gets
	// two attempts, then the run ends blocked naming the last verdict.
	for _, c := range []struct {
		clear   ChallengeClear
		presses int
		verdict string
	}{
		{ChallengeClear{Present: true, Solved: true, Clicks: 1, Reason: "solved"}, maxChallengeClears, ""},
		{ChallengeClear{Present: true, Clicks: 3, Reason: "clicked-no-pass"}, maxChallengeClears, "checkbox clicked-no-pass"},
	} {
		fb := newFakeBrowser(t, func(string) fakePage { p := challengePage(); p.clear = c.clear; return p })
		rec := browserEnv(t, fb).run(ScanRequest{Source: "indeed", Portals: indeedPortals(t, nil)})
		if rec.Status != RunBlocked || rec.Reason != blockChallenge || fb.count("clear-challenge") != c.presses || !strings.Contains(rec.Error, c.verdict) {
			t.Fatalf("%s: status %s reason %q presses %d error %q", c.clear.Reason, rec.Status, rec.Reason, fb.count("clear-challenge"), rec.Error)
		}
	}
}

func TestBrowserScan_Challenge_AFailedAttemptIsRetriedOnAFreshPage(t *testing.T) {
	lists := 0
	fb := newFakeBrowser(t, func(raw string) fakePage {
		if !strings.Contains(raw, "/jobs") {
			return fakePage{}
		}
		lists++
		p := challengePage()
		switch lists {
		case 1:
			p.clear = ChallengeClear{Present: true, Clicks: 3, Reason: "clicked-no-pass"}
		case 2:
			p.clear = ChallengeClear{Present: true, Clicks: 1, Solved: true, Reason: "solved"}
		default:
			p = cardsPage(jobCard(1, "Backend Engineer", "Acme"))
		}
		return p
	})
	rec := browserEnv(t, fb).run(ScanRequest{Source: "indeed", Portals: indeedPortals(t, nil)})
	if rec.Status != RunCompleted || rec.Counts.Discovered != 1 || lists != 3 {
		t.Fatalf("status %s counts %+v list loads %d error %q", rec.Status, rec.Counts, lists, rec.Error)
	}
}

func TestBrowserScan_EmptyFreshnessWindowCompletesWithReason(t *testing.T) {
	fb := newFakeBrowser(t, func(string) fakePage { return fakePage{probe: pageProbe{Text: "No jobs match your search."}} })
	e := browserEnv(t, fb)
	rec := e.run(ScanRequest{Source: "ziprecruiter", Portals: zipPortals(t, nil)})
	if rec.Status != RunCompleted || rec.Reason != ReasonEmptyWindow {
		t.Fatalf("with the window: %s %q", rec.Status, rec.Reason)
	}
	p := zipPortals(t, map[string]any{"url": "https://www.ziprecruiter.com/jobs-search?search=engineer"})
	rec = e.run(ScanRequest{Source: "ziprecruiter", Portals: p})
	if rec.Status != RunFailed || rec.Reason != ReasonNoCards {
		t.Fatalf("without it: %s %q", rec.Status, rec.Reason)
	}
	if !strings.Contains(strings.Join(rec.Errors, " "), "jobs-search?search=engineer") {
		t.Fatalf("the failure does not name the URL: %v", rec.Errors)
	}
}

func TestBrowserScan_CardsWithoutRowsIsEmptyExtraction(t *testing.T) {
	fb := newFakeBrowser(t, func(string) fakePage { return fakePage{extract: extractResult{Cards: 25}} })
	rec := browserEnv(t, fb).run(ScanRequest{Source: "ziprecruiter", Portals: zipPortals(t, nil)})
	if rec.Status != RunFailed || rec.Reason != ReasonEmptyExtract {
		t.Fatalf("status %s reason %q", rec.Status, rec.Reason)
	}
}

func TestBrowserScan_RateLimitPageEndsBlockedNoRetry(t *testing.T) {
	fb := newFakeBrowser(t, func(string) fakePage { return fakePage{probe: pageProbe{Text: "Too many requests. Try again later."}} })
	rec := browserEnv(t, fb).run(ScanRequest{Source: "ziprecruiter", Portals: zipPortals(t, nil)})
	if rec.Status != RunBlocked || rec.Reason != blockRateLimit || len(fb.visited()) != 1 {
		t.Fatalf("status %s reason %q visits %v", rec.Status, rec.Reason, fb.visited())
	}
	if rec.Attempts[0].Status != AttemptRateLimited {
		t.Fatalf("attempt = %+v", rec.Attempts[0])
	}
}

// LinkedIn holds a scan back with HTTP 999, then sends the browser on to its
// sign-in wall: the run is rate-limited, not one that needs a sign-in (flow
// round 10).
func TestBrowserScan_LinkedInsStatus999IsARateLimit(t *testing.T) {
	fb := newFakeBrowser(t, func(string) fakePage {
		return fakePage{nav: Nav{URL: "https://www.linkedin.com/jobs/search/?keywords=engineer", Status: 999},
			probe: pageProbe{URL: "https://www.linkedin.com/authwall?trk=bf"}}
	})
	rec := browserEnv(t, fb).run(ScanRequest{Source: "linkedin", Portals: portalsWith(t, eng(), "linkedin", map[string]any{"search": "engineer"})})
	if rec.Status != RunBlocked || rec.Reason != blockRateLimit {
		t.Fatalf("status %s reason %q", rec.Status, rec.Reason)
	}
}

func TestBrowserScan_PostedWithinDrivesBoardParamAndLocalFilter(t *testing.T) {
	for _, c := range []struct {
		window time.Duration
		want   string
	}{{24 * time.Hour, "1"}, {72 * time.Hour, "5"}, {-1, ""}} {
		u, fresh, _ := zipURL("https://www.ziprecruiter.com", Query{Search: "engineer", P: map[string]string{}}, 0, windowOf(c.window))
		if got := mustQuery(t, u).Get("days"); got != c.want || fresh != (c.want != "") {
			t.Errorf("window %v: days=%q fresh=%v, want %q", c.window, got, fresh, c.want)
		}
	}
	// With no board parameter the local filter is the whole window.
	cards := []rawCard{jobCard(1, "Backend Engineer", "Acme"), jobCard(2, "Data Engineer", "Beta")}
	cards[0].Age, cards[1].Age = "10 hours ago", "4 days ago"
	fb := newFakeBrowser(t, func(string) fakePage { p := cardsPage(cards...); p.detail.Desc = postingText(); return p })
	p := zipPortals(t, map[string]any{"url": "https://www.ziprecruiter.com/jobs-search?search=engineer"})
	rec := browserEnv(t, fb).run(ScanRequest{Source: "ziprecruiter", Portals: p, Window: 24 * time.Hour})
	if rec.Counts.Inserted != 1 || rec.Counts.Filtered["stale"] != 1 {
		t.Fatalf("counts = %+v", rec.Counts)
	}
}

func windowOf(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d
}

func mustQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}

func TestBrowserScan_BoardAppliedWindowTrustsDatedRows(t *testing.T) {
	cards := []rawCard{jobCard(1, "Backend Engineer", "Acme")}
	cards[0].Age = "Yesterday"
	fb := newFakeBrowser(t, func(string) fakePage { return cardsPage(cards...) })
	// The default URL carries the board's own 24 hour parameter.
	rec := browserEnv(t, fb).run(ScanRequest{Source: "ziprecruiter", Portals: zipPortals(t, nil), Window: 24 * time.Hour})
	if rec.Counts.Inserted != 1 || rec.Counts.Filtered["stale"] != 0 {
		t.Fatalf("counts = %+v", rec.Counts)
	}
	cards[0].Age = "3 days ago"
	rec = browserEnv(t, newFakeBrowser(t, func(string) fakePage { return cardsPage(cards...) })).run(ScanRequest{Source: "ziprecruiter", Portals: zipPortals(t, nil), Window: 24 * time.Hour})
	if rec.Counts.Inserted != 1 {
		t.Fatalf("a dated row inside the board's window was dropped: %+v", rec.Counts)
	}
}

func TestBrowserScan_UndatedKeptOnlyWithSourceFreshnessParam(t *testing.T) {
	card := jobCard(1, "Backend Engineer", "Acme")
	card.Age = ""
	route := func(string) fakePage { return cardsPage(card) }
	rec := browserEnv(t, newFakeBrowser(t, route)).run(ScanRequest{Source: "ziprecruiter", Portals: zipPortals(t, nil)})
	if rec.Counts.Inserted != 1 || rec.Counts.Undated != 0 {
		t.Fatalf("with the parameter: %+v", rec.Counts)
	}
	p := zipPortals(t, map[string]any{"url": "https://www.ziprecruiter.com/jobs-search?search=engineer"})
	rec = browserEnv(t, newFakeBrowser(t, route)).run(ScanRequest{Source: "ziprecruiter", Portals: p})
	if rec.Counts.Inserted != 0 || rec.Counts.Undated != 1 || rec.Counts.Filtered["stale"] != 0 {
		t.Fatalf("without it: %+v", rec.Counts)
	}
}

func TestBrowserScan_DetailConcurrencyIsLesserOfSettingAndCap(t *testing.T) {
	for _, c := range []struct {
		source   string
		setting  int
		degraded bool
		want     int
	}{{"linkedin", 6, false, 2}, {"indeed", 6, false, 3}, {"indeed", 1, false, 1}, {"linkedin", 6, true, 1}} {
		if got := detailLimit(c.source, c.setting, c.degraded); got != c.want {
			t.Errorf("%+v: %d", c, got)
		}
	}
}

func TestBrowserScan_PerCompanyDetailCapIsFour(t *testing.T) {
	var cards []rawCard
	for i := 1; i <= 6; i++ {
		cards = append(cards, jobCard(i, fmt.Sprintf("Backend Engineer %d", i), "Same Company"))
	}
	fb := newFakeBrowser(t, func(string) fakePage { p := cardsPage(cards...); p.detail.Desc = postingText(); return p })
	rec := browserEnv(t, fb).run(ScanRequest{Source: "indeed", Portals: portalsWith(t, eng(), "indeed", map[string]any{"search": "engineer"})})
	if rec.Detail["detail-opens"] != 4 || rec.Counts.Enriched != 4 || rec.Counts.EnrichmentSkipped != 2 {
		t.Fatalf("detail %v counts %+v", rec.Detail, rec.Counts)
	}
	if rec.Counts.Inserted != 6 {
		t.Fatalf("inserted %d: the skipped rows stay on their list text", rec.Counts.Inserted)
	}
}

func TestBrowserScan_HostPressureCriticalRefusesWarnDegrades(t *testing.T) {
	run := func(h HostSample, req ScanRequest) (Record, *fakeBrowser) {
		fb := newFakeBrowser(t, pagedRoute(nil))
		e := browserEnv(t, fb)
		e.sc.Browser.Host = func(context.Context) HostSample { return h }
		req.Source, req.Portals = "ziprecruiter", zipPortals(t, nil)
		return e.run(req), fb
	}
	crit := HostSample{Verdict: PressureCritical, FreeMiB: 90, Load1: 1, CPUs: 4, OSVerdict: true}
	rec, fb := run(crit, ScanRequest{})
	if rec.Status != RunSkipped || rec.Reason != ReasonHostPressure || len(fb.visited()) != 0 || rec.Host == nil || rec.Host.FreeMiB != 90 {
		t.Fatalf("critical: %s %q host %+v", rec.Status, rec.Reason, rec.Host)
	}
	if !strings.Contains(rec.Error, "90 MiB free") {
		t.Fatalf("the measured values are not in the record: %q", rec.Error)
	}
	rec, _ = run(crit, ScanRequest{Force: true})
	if rec.Status != RunCompleted || rec.Host == nil || rec.Host.Verdict != PressureCritical {
		t.Fatalf("--force: %s host %+v", rec.Status, rec.Host)
	}
	rec, _ = run(HostSample{Verdict: PressureWarn, FreeMiB: 400, CPUs: 4}, ScanRequest{})
	if rec.Status != RunCompleted || rec.Detail["host_degraded"] != 1 {
		t.Fatalf("warn: %s %v", rec.Status, rec.Detail)
	}
	rec, _ = run(HostSample{Verdict: PressureNormal, FreeMiB: 8000, Load1: 5, CPUs: 1}, ScanRequest{})
	if rec.Detail["host_degraded"] != 1 {
		t.Fatalf("load 5 on 1 core did not degrade: %v", rec.Detail)
	}
	rec, _ = run(HostSample{Verdict: PressureNormal, FreeMiB: 8000, Load1: 160, CPUs: 4}, ScanRequest{})
	if rec.Status != RunCompleted || rec.Detail["host_degraded"] != 1 {
		t.Fatalf("load alone: %s %v", rec.Status, rec.Detail)
	}
	rec, _ = run(HostSample{Verdict: PressureNormal, FreeMiB: 8000, Load1: 1, CPUs: 4}, ScanRequest{})
	if rec.Detail["host_degraded"] != 0 || rec.Host == nil {
		t.Fatalf("a healthy host was degraded or unrecorded: %v %+v", rec.Detail, rec.Host)
	}
}

func TestBrowserScan_HostPressureVerdictRules(t *testing.T) {
	for _, c := range []struct {
		os, free int
		want     string
		fromOS   bool
	}{{1, 100, PressureNormal, true}, {2, 8000, PressureWarn, true}, {4, 8000, PressureCritical, true},
		{0, 150, PressureCritical, false}, {0, 199, PressureCritical, false}, {0, 200, PressureWarn, false}, {0, 511, PressureWarn, false}, {0, 512, PressureNormal, false}} {
		if v, os := HostPressure(c.os, c.free); v != c.want || os != c.fromOS {
			t.Errorf("os %d free %d: %s %v", c.os, c.free, v, os)
		}
	}
}

func TestBrowserScan_PartialEnrichmentIsKeptNotDiscarded(t *testing.T) {
	var cards []rawCard
	for i := 1; i <= 5; i++ {
		cards = append(cards, jobCard(i, fmt.Sprintf("Backend Engineer %d", i), fmt.Sprintf("Company %d", i)))
	}
	route := func(string) fakePage { p := cardsPage(cards...); p.detail.Desc = postingText(); return p }
	fb := newFakeBrowser(t, route)
	// The list tab is the first; the detail stage then opens two tabs and the
	// browser goes away.
	fb.failOpenAfter = 3
	e := browserEnv(t, fb)
	e.sc.Browser.Sleep = fb.sleep
	rec := e.run(ScanRequest{Source: "indeed", Portals: portalsWith(t, eng(), "indeed", map[string]any{"search": "engineer"})})
	if rec.Status != RunCompleted || rec.Counts.Enriched != 2 || rec.Counts.EnrichmentFailed != 3 || rec.Counts.Inserted != 5 {
		t.Fatalf("status %s counts %+v", rec.Status, rec.Counts)
	}
	// Nothing enriched at all fails the run.
	fb2 := newFakeBrowser(t, route)
	fb2.failOpenAfter = 1
	rec = browserEnv(t, fb2).run(ScanRequest{Source: "indeed", Portals: portalsWith(t, eng(), "indeed", map[string]any{"search": "engineer"})})
	if rec.Status != RunFailed {
		t.Fatalf("status %s, want failed", rec.Status)
	}
	if rec.Counts.Inserted != 0 || rec.Counts.Enriched != 0 {
		t.Fatalf("a failed run wrote rows: counts %+v", rec.Counts)
	}
}

func TestScan_ListingOnlyRejectionsPrecedeDetailFetch(t *testing.T) {
	cards := []rawCard{jobCard(1, "Sales Manager", "Acme"), jobCard(2, "Backend Engineer", "Blocked Co"), jobCard(3, "Backend Engineer", "Fine Co")}
	fb := newFakeBrowser(t, func(string) fakePage { p := cardsPage(cards...); p.detail.Desc = postingText(); return p })
	e := browserEnv(t, fb)
	if _, err := e.rp.BlockCompany(e.ctx, "Blocked Co", "test", "test"); err != nil {
		t.Fatal(err)
	}
	p := portals([]string{"engineer"}, []string{"manager"})
	p = portalsWith(t, p, "indeed", map[string]any{"search": "engineer"})
	rec := e.run(ScanRequest{Source: "indeed", Portals: p})
	if rec.Detail["detail-opens"] != 1 || rec.Counts.Filtered[FilterTitle] != 1 || rec.Counts.Filtered[FilterBlockedCompany] != 1 || rec.Counts.Inserted != 1 {
		t.Fatalf("detail %v counts %+v", rec.Detail, rec.Counts)
	}
}

// BL-167: the challenge rate of a site is its blocked-by-challenge runs against
// the pages its scans opened, inside the period asked for.
func TestBrowserScan_ChallengeBaselinePerSite(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	finish := func(source, status, reason string, list, detail int) {
		t.Helper()
		rec, err := e.rp.ScanStart(ctx, source, nil)
		if err != nil {
			t.Fatal(err)
		}
		rec.Status, rec.Reason, rec.Detail = status, reason, map[string]int{"list-opens": list, "detail-opens": detail}
		if _, err := e.rp.ScanFinish(ctx, rec); err != nil {
			t.Fatal(err)
		}
		e.clk.advance(time.Minute)
	}
	finish("indeed", RunCompleted, "", 50, 50) // before the period
	e.clk.advance(40 * 24 * time.Hour)
	since := e.clk.now().Add(-30 * 24 * time.Hour)
	finish("indeed", RunCompleted, "", 8, 12)
	finish("indeed", RunBlocked, blockChallenge, 1, 0)
	finish("indeed", RunSkipped, ReasonCooldown, 0, 0)
	finish("ziprecruiter", RunBlocked, blockRateLimit, 1, 0)
	finish("registry", RunCompleted, "", 0, 0) // not a browser source

	got, err := e.rp.ChallengeBaseline(ctx, since)
	if err != nil {
		t.Fatal(err)
	}
	want := []Baseline{
		{Source: "indeed", Runs: 2, Visits: 21, Challenges: 1},
		{Source: "ziprecruiter", Runs: 1, Visits: 1, RateLimited: 1},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("baseline = %+v, want %+v", got, want)
	}
}

// The source funnel counts a posting once under the source of its first finished
// evaluation, reads how far it got from its tracker row, and keeps failures and
// gate rejections out of the evaluated count.
func TestSourceFunnel_PerLaneFromScansEvaluationsAndTracker(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	now := e.clk.now()
	stamp := func(d time.Duration) string { return now.Add(d).UTC().Format(time.RFC3339) }
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		n := 0
		job := func(identity, source, outcome string, at time.Duration) {
			n++
			id := fmt.Sprintf("run-%d", n)
			if _, err := tx.Exec(`INSERT INTO runs (id, kind, status, created_at, updated_at) VALUES (?, 'evaluation', 'succeeded', ?, ?)`, id, stamp(at), stamp(at)); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(`INSERT INTO evaluation_jobs (run_id, identity_key, mode, priority, source, outcome, created_at) VALUES (?, ?, 'auto', 'scan', ?, ?, ?)`,
				id, identity, source, outcome, stamp(at)); err != nil {
				t.Fatal(err)
			}
		}
		row := func(identity, status string, score float64, applied bool) {
			var appliedAt any
			if applied {
				appliedAt = stamp(0)
			}
			if _, err := tx.Exec(`INSERT INTO tracker_entries (date, company, role, score, score_kind, status, applied_at, identity_key, created_at, updated_at)
				VALUES ('2027-03-01', ?, 'Engineer', ?, 'final', ?, ?, ?, ?, ?)`, identity, score, status, appliedAt, identity, stamp(0), stamp(0)); err != nil {
				t.Fatal(err)
			}
		}
		job("a", "indeed", "reported", -48*time.Hour)
		job("a", "linkedin", "reported", -24*time.Hour) // the same posting again: counted once, under indeed
		job("b", "indeed", "screened", -47*time.Hour)
		job("c", "indeed", "reported", -46*time.Hour)
		job("d", "indeed", "failed", -45*time.Hour)
		job("e", "indeed", "gated", -44*time.Hour)
		job("f", "", "reported", -43*time.Hour)          // started by hand
		job("g", "indeed", "reported", -90*24*time.Hour) // outside the period
		row("a", "interview", 4.7, true)
		row("b", "evaluated", 3.9, false)
		row("c", "applied", 4.5, true)
		row("f", "responded", 4.1, true)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []struct {
		source string
		found  int
		fresh  int
		at     time.Duration
	}{{"indeed", 40, 6, -30 * time.Hour}, {"indeed", 25, 2, -2 * time.Hour}, {"registry", 7, 7, -3 * time.Hour}, {"indeed", 99, 9, -100 * 24 * time.Hour}} {
		rec, err := e.rp.ScanStart(ctx, s.source, nil)
		if err != nil {
			t.Fatal(err)
		}
		rec.Status, rec.Counts.Discovered, rec.Counts.Inserted, rec.Started = RunCompleted, s.found, s.fresh, now.Add(s.at)
		if _, err := e.rp.ScanFinish(ctx, rec); err != nil {
			t.Fatal(err)
		}
		err = e.st.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE scan_runs SET started_at = ? WHERE id = ?`, stamp(s.at), rec.ID)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	got, err := e.rp.SourceFunnel(ctx, now.Add(-30*24*time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	want := []FunnelRow{
		{Lane: "indeed", Discovered: 65, Inserted: 8, Evaluated: 3, Failed: 1, GateRejected: 1, High: 2, Applied: 2, Responded: 1, Interview: 1},
		{Lane: ManualLane, Evaluated: 1, Responded: 1, Applied: 1},
		{Lane: "registry", Discovered: 7, Inserted: 7},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("funnel =\n%+v\nwant\n%+v", got, want)
	}
}

func TestBrowserScan_HTTPFailuresHaveProtocolPrecedence(t *testing.T) {
	for _, status := range []int{404, 503} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			fb := newFakeBrowser(t, func(string) fakePage {
				return fakePage{nav: Nav{Status: status}, probe: pageProbe{Text: "No jobs match your search."}}
			})
			rec := browserEnv(t, fb).run(ScanRequest{Source: "ziprecruiter", Portals: zipPortals(t, nil)})
			if rec.Status != RunFailed || rec.Reason == ReasonEmptyWindow || rec.Detail["page-failed"] != 1 || len(fb.visited()) != 2 {
				t.Fatalf("HTTP error is not empty success: status=%s reason=%s detail=%v visits=%v", rec.Status, rec.Reason, rec.Detail, fb.visited())
			}
			if rec.Retryable != (status >= 500) {
				t.Fatalf("HTTP %d retryable=%v", status, rec.Retryable)
			}
			if len(rec.Attempts) != 1 || rec.Attempts[0].HTTP != status || rec.Attempts[0].N != 2 || rec.Attempts[0].Status != AttemptError {
				t.Fatalf("attempts=%+v", rec.Attempts)
			}
		})
	}
	for _, tc := range []struct {
		status        int
		title, reason string
	}{
		{403, "Forbidden", blockChallenge}, {429, "Busy", blockRateLimit}, {503, "Just a moment", blockChallenge},
	} {
		t.Run(tc.reason+strconv.Itoa(tc.status), func(t *testing.T) {
			fb := newFakeBrowser(t, func(string) fakePage { return fakePage{nav: Nav{Status: tc.status}, probe: pageProbe{Title: tc.title}} })
			rec := browserEnv(t, fb).run(ScanRequest{Source: "ziprecruiter", Portals: zipPortals(t, nil)})
			if rec.Retryable {
				t.Fatal("a challenge or rate limit must not become a scheduler retry")
			}
			if rec.Status != RunBlocked || rec.Reason != tc.reason || len(fb.visited()) != 1 || rec.Detail["retries"] != 0 || rec.Detail["page-failed"] != 0 {
				t.Fatalf("wall must not retry: status=%s reason=%s detail=%v visits=%v", rec.Status, rec.Reason, rec.Detail, fb.visited())
			}
		})
	}
}

func TestBrowserScan_HTTPWallsDoNotDependOnDOMReads(t *testing.T) {
	for _, status := range []int{403, 429} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			fb := newFakeBrowser(t, func(string) fakePage {
				return fakePage{nav: Nav{Status: status}, eval: map[string]any{"probe": make(chan int)}}
			})
			rec := browserEnv(t, fb).run(ScanRequest{Source: "ziprecruiter", Portals: zipPortals(t, nil)})
			t.Logf("status=%s reason=%s retries=%v visited=%d", rec.Status, rec.Reason, rec.Detail["retries"], len(fb.visited()))
			if rec.Status != RunBlocked || len(fb.visited()) != 1 || rec.Detail["retries"] != 0 {
				t.Errorf("known protocol wall retried: %+v", rec)
			}
		})
	}
}

func TestBrowserScan_HTTPPartialFailuresContinueOtherQueriesAndPages(t *testing.T) {
	fb := newFakeBrowser(t, func(raw string) fakePage {
		u, _ := url.Parse(raw)
		if u.Query().Get("search") == "broken" || u.Query().Get("page") == "2" {
			return fakePage{nav: Nav{Status: 503}}
		}
		return pagedRoute(nil)(raw)
	})
	rec := browserEnv(t, fb).run(ScanRequest{
		Source: "ziprecruiter", Pages: 3,
		Portals: portalsWith(t, eng(), "ziprecruiter", map[string]any{"search": "broken"}, map[string]any{"search": "engineer"}),
	})
	if rec.Status != RunCompleted || rec.Counts.Inserted != 6 || rec.Detail["page-failed"] != 4 || rec.Detail["retries"] != 4 {
		t.Fatalf("partial failure lost later query/pages: status=%s inserted=%d detail=%v", rec.Status, rec.Counts.Inserted, rec.Detail)
	}
}

func TestBrowserScan_MixedFailedQueriesKeepTemporaryCause(t *testing.T) {
	fb := newFakeBrowser(t, func(raw string) fakePage {
		status := 404
		if strings.Contains(raw, "temporary") {
			status = 503
		}
		return fakePage{nav: Nav{Status: status}, probe: pageProbe{Text: "No jobs match your search."}}
	})
	e := browserEnv(t, fb)
	rec := e.run(ScanRequest{Source: "ziprecruiter", Pages: 1, Portals: portalsWith(t, eng(), "ziprecruiter", map[string]any{"search": "permanent"}, map[string]any{"search": "temporary"})})
	if rec.Status != RunFailed || !rec.Retryable {
		t.Fatalf("temporary second query was lost: status=%s retryable=%v error=%s", rec.Status, rec.Retryable, rec.Error)
	}
	stored, err := e.rp.ScanGet(e.ctx, rec.ID)
	if err != nil || !stored.Retryable {
		t.Fatalf("retry classification was not durable: %+v %v", stored, err)
	}
}

func TestBrowserScan_TransientHistoryDoesNotOverrideTerminalStatus(t *testing.T) {
	for _, want := range []string{RunCompleted, RunBlocked, RunNeedsSignIn} {
		t.Run(want, func(t *testing.T) {
			fb := newFakeBrowser(t, func(raw string) fakePage {
				u, _ := url.Parse(raw)
				if u.Query().Get("search") == "broken" {
					return fakePage{nav: Nav{Status: 503}}
				}
				switch want {
				case RunBlocked:
					return fakePage{nav: Nav{Status: 403}}
				case RunNeedsSignIn:
					return fakePage{nav: Nav{URL: "https://www.ziprecruiter.com/login"}}
				}
				return pagedRoute(nil)(raw)
			})
			e := browserEnv(t, fb)
			rec := e.run(ScanRequest{Source: "ziprecruiter", Pages: 1, Portals: portalsWith(t, eng(), "ziprecruiter", map[string]any{"search": "broken"}, map[string]any{"search": "engineer"})})
			stored, err := e.rp.ScanGet(e.ctx, rec.ID)
			if err != nil {
				t.Fatal(err)
			}
			if rec.Status != want || rec.Retryable || stored.Retryable {
				t.Fatalf("earlier transient overrode terminal: status=%s retry=%v stored=%v", rec.Status, rec.Retryable, stored.Retryable)
			}
		})
	}
}

func TestBrowserScan_RunBudgetIncludesHostPreflight(t *testing.T) {
	fb := newFakeBrowser(t, pagedRoute(nil))
	e := browserEnv(t, fb)
	measured := false
	fire := e.budgetOnCue()
	e.sc.Browser.Host = func(ctx context.Context) HostSample {
		fire()
		<-ctx.Done()
		measured = true
		return HostSample{}
	}
	rec := e.run(ScanRequest{Source: "ziprecruiter", Portals: zipPortals(t, nil)})
	if !measured || rec.Status != RunCompleted || rec.Reason != ReasonBudget || rec.Counts.Queued != 0 {
		t.Fatalf("host preflight exceeded budget: measured=%t rec=%+v", measured, rec)
	}
	if _, cooling := e.rp.CooldownUntil(e.ctx, "ziprecruiter", 6, ""); cooling {
		t.Fatal("budget created a challenge cooldown")
	}
}

func TestBrowserScan_LayoutHistoryDoesNotOverrideTerminalStatus(t *testing.T) {
	for _, want := range []string{RunBlocked, RunNeedsSignIn} {
		t.Run(want, func(t *testing.T) {
			fb := newFakeBrowser(t, func(raw string) fakePage {
				u, _ := url.Parse(raw)
				if u.Query().Get("search") == "empty" {
					return fakePage{extract: extractResult{Cards: 1}}
				}
				if want == RunBlocked {
					return fakePage{nav: Nav{Status: 403}}
				}
				return fakePage{nav: Nav{URL: "https://www.ziprecruiter.com/login"}}
			})
			e := browserEnv(t, fb)
			notified := false
			e.sc.NeedsSignIn = func(context.Context, string, string) { notified = true }
			rec := e.run(ScanRequest{Source: "ziprecruiter", Pages: 1, Portals: portalsWith(t, eng(), "ziprecruiter", map[string]any{"search": "empty"}, map[string]any{"search": "engineer"})})
			if rec.Status != want || rec.Retryable || !strings.Contains(strings.Join(rec.Errors, "; "), ReasonEmptyExtract) {
				t.Fatalf("layout overrode terminal outcome: %+v", rec)
			}
			if notified != (want == RunNeedsSignIn) {
				t.Fatalf("sign-in notification=%t status=%s", notified, rec.Status)
			}
		})
	}
}
