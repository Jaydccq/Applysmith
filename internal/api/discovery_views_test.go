package api

import (
	"cmp"
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"applysmith/internal/api/wire"
	"applysmith/internal/browser"
	"applysmith/internal/discovery"
)

func finishedScan(t *testing.T, h *harness, source, status string) discovery.Record {
	t.Helper()
	repo := h.domainOf().repo
	rec, err := repo.ScanStart(t.Context(), source, map[string]any{"run_id": "run-" + source})
	if err != nil {
		t.Fatal(err)
	}
	rec.Status = status
	if rec, err = repo.ScanFinish(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func sourceByName(list wire.ScanSourceList, name string) wire.ScanSource {
	for _, s := range list.Sources {
		if s.Name == name {
			return s
		}
	}
	return wire.ScanSource{}
}

func TestScanSources_ListsTheCatalogWithReadinessRunningCooldownAndLastRun(t *testing.T) {
	h := newDomainHarness(t, func(c *Config) { c.SessionBrowser = &fakeBrowser{} })
	finishedScan(t, h, "indeed", discovery.RunBlocked)
	finishedScan(t, h, "linkedin", discovery.RunNeedsSignIn)
	if _, err := h.domainOf().repo.ScanStart(t.Context(), "registry", nil); err != nil {
		t.Fatal(err)
	}
	var env wire.Success[wire.ScanSourceList]
	r := h.get("/api/scan-sources")
	strictInto(t, r.body, &env)
	if r.StatusCode != 200 || len(env.Data.Sources) != len(discovery.Sources) {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if s := sourceByName(env.Data, "theirstack"); s.Needs != "key" || s.KeyName != "api-key-theirstack" || s.Readiness != "needs key" {
		t.Errorf("a keyed source with no key: %+v", s)
	}
	if s := sourceByName(env.Data, "linkedin"); s.Needs != "session" || s.Readiness != "needs sign-in" || s.LastScan == nil || s.LastScan.RunID != "run-linkedin" {
		t.Errorf("a source that stopped at the sign-in wall: %+v", s)
	}
	if s := sourceByName(env.Data, "indeed"); s.CooldownUntil == nil {
		t.Errorf("a blocked source is cooling down: %+v", s)
	}
	if s := sourceByName(env.Data, "registry"); !s.Running || s.CooldownUntil != nil {
		t.Errorf("a running scan: %+v", s)
	}
	if s := sourceByName(env.Data, "github"); s.LastScan != nil || s.Running || s.Readiness != "ready" {
		t.Errorf("an idle source: %+v", s)
	}
}

// sitesBrowser is a managed browser whose sign-in probe holds the given sites.
type sitesBrowser struct {
	*fakeBrowser
	sites []browser.SessionSite
}

func (b sitesBrowser) Sites(context.Context) ([]browser.SessionSite, error) { return b.sites, nil }

func TestScanSources_BrowserSourcesReportTheirRealPrecondition(t *testing.T) {
	checked := wire.Time{Time: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
	site := func(domain, state string) browser.SessionSite {
		return browser.SessionSite{Domain: domain, Label: domain, SignIn: state, SignInCheckedAt: &checked}
	}
	read := func(h *harness) wire.ScanSourceList {
		var env wire.Success[wire.ScanSourceList]
		strictInto(t, h.get("/api/scan-sources").body, &env)
		return env.Data
	}

	none := read(newDomainHarness(t))
	if s := sourceByName(none, "indeed"); s.Readiness != "needs browser" || !strings.Contains(s.Reason, "No managed browser") {
		t.Errorf("no managed browser: %+v", s)
	}
	if s := sourceByName(none, "a16z"); s.Readiness != "ready" || s.Reason != "" {
		t.Errorf("an API source needs no browser: %+v", s)
	}

	probedH := newDomainHarness(t, func(c *Config) {
		c.SessionBrowser = sitesBrowser{&fakeBrowser{}, []browser.SessionSite{
			site("linkedin.com", "signed_out"), site("glassdoor.com", "signed_in"), site("jobright.ai", "unknown"),
		}}
	})
	if err := writeFile(probedH.ws.Root+"/portals.yml", "version: 1\nsources:\n  indeed:\n    queries: [platform engineer]\n  glassdoor:\n    queries: [platform engineer]\n"); err != nil {
		t.Fatal(err)
	}
	probed := read(probedH)
	if s := sourceByName(probed, "indeed"); s.Readiness != "ready" {
		t.Errorf("a browser source without a login: %+v", s)
	}
	if s := sourceByName(probed, "linkedin"); s.Readiness != "needs sign-in" || !strings.Contains(s.Reason, "2026-10-01") || !strings.Contains(s.Reason, "signed out") {
		t.Errorf("signed out: %+v", s)
	}
	if s := sourceByName(probed, "glassdoor"); s.Readiness != "ready" || s.Reason != "" {
		t.Errorf("signed in: %+v", s)
	}
	if s := sourceByName(probed, "jobright"); s.Readiness != "sign-in unchecked" || s.Reason == "" {
		t.Errorf("never checked: %+v", s)
	}
}

// A registry scan with no active board ends skipped as registry-empty, so the
// Run tab says what to add before it starts; a registry switched off in
// portals.yml reads off; a saved-search source with no saved search says what
// to add instead of reading ready.
func TestScanSources_ASourceWithNothingToReadSaysWhatItNeeds(t *testing.T) {
	const off = "version: 1\nregistry:\n  enabled: false\n"
	for _, tc := range []struct {
		name      string
		source    string
		board     bool
		portals   string
		readiness string
	}{
		{name: "empty registry", readiness: "needs boards"},
		{name: "an active board", board: true, readiness: "ready"},
		{name: "switched off with an active board", board: true, portals: off, readiness: "off"},
		{name: "no saved search", source: "builtin", readiness: "needs searches"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newDomainHarness(t)
			if tc.board {
				if _, err := h.domainOf().repo.RegistryAdd(t.Context(), "seed", discovery.Board{ATS: discovery.Greenhouse, Slug: "northwind-example"}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.portals != "" {
				if err := writeFile(h.ws.Root+"/portals.yml", tc.portals); err != nil {
					t.Fatal(err)
				}
			}
			var env wire.Success[wire.ScanSourceList]
			strictInto(t, h.get("/api/scan-sources").body, &env)
			s := sourceByName(env.Data, cmp.Or(tc.source, "registry"))
			if s.Readiness != tc.readiness || (tc.readiness == "needs boards") != strings.Contains(s.Reason, "registry is empty") || (tc.readiness == "ready") != (s.Reason == "") {
				t.Errorf("%+v, want %s", s, tc.readiness)
			}
		})
	}
}

func TestScanList_FiltersByStatusAndDateAndPages(t *testing.T) {
	h := newDomainHarness(t)
	finishedScan(t, h, "registry", discovery.RunCompleted)
	finishedScan(t, h, "github", discovery.RunCompleted)
	finishedScan(t, h, "indeed", discovery.RunBlocked)
	var env wire.Success[wire.ScanList]
	r := h.get("/api/scans?status=completed&limit=1&page=2")
	strictInto(t, r.body, &env)
	if r.StatusCode != 200 || env.Data.Total != 2 || env.Data.Page != 2 || len(env.Data.Scans) != 1 {
		t.Fatalf("paged: %s", r.body)
	}
	if got := h.get("/api/scans?to=2000-01-01"); got.data()["total"].(float64) != 0 {
		t.Errorf("before every run: %s", got.body)
	}
	if r := h.get("/api/scans?from=yesterday"); r.StatusCode != 422 {
		t.Errorf("from=yesterday: %d", r.StatusCode)
	}
}

func TestSchedulePreview_ListsTheNextFiveFireTimesBeforeSaving(t *testing.T) {
	h := newDomainHarness(t)
	r := h.post("/api/schedules/preview", map[string]any{"preset": map[string]any{"kind": "weekly", "time": "07:00", "weekdays": []string{"mon"}, "timezone": "UTC"}})
	var env wire.Success[wire.SchedulePreview]
	strictInto(t, r.body, &env)
	if r.StatusCode != 200 || len(env.Data.Next) != 5 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	for _, at := range env.Data.Next {
		if at.Time.Weekday() != time.Monday || at.Time.Hour() != 7 {
			t.Errorf("not a Monday 07:00: %s", at.Time)
		}
	}
	if got := h.get("/api/schedules"); len(got.data()["schedules"].([]any)) != 0 {
		t.Error("a preview saved a schedule")
	}
	bad := h.post("/api/schedules/preview", map[string]any{"preset": map[string]any{"kind": "daily", "time": "25:00", "timezone": "UTC"}})
	if bad.StatusCode != 422 || !strings.Contains(string(bad.body), "preset") {
		t.Errorf("an invalid recurrence: %d %s", bad.StatusCode, bad.body)
	}
}

func TestLeadsEvaluate_RunsInTheBackgroundAndRefusesASecondPass(t *testing.T) {
	h := newDomainHarness(t)
	r := h.send("POST", "/api/leads/evaluate", map[string]any{}, "Idempotency-Key", "key-leads-eval-1")
	id, _ := r.data()["runId"].(string)
	if r.StatusCode != 202 || id == "" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	run := h.waitRun(id)
	if run["kind"] != "leads" {
		t.Fatalf("run %v", run)
	}
	if r := h.post("/api/leads/evaluate", map[string]any{}); r.StatusCode != 400 {
		t.Errorf("no Idempotency-Key: %d", r.StatusCode)
	}
	if r := h.send("POST", "/api/leads/evaluate", map[string]any{"ids": []int{1, 0}}, "Idempotency-Key", "key-leads-eval-2"); r.StatusCode != 422 {
		t.Errorf("an id of zero: %d %s", r.StatusCode, r.body)
	}
	// While a pass runs the list says so, and another start is refused.
	d := h.domainOf()
	d.scans.claim(leadRunClaim("evaluate"))
	if got := h.get("/api/leads").data(); got["evaluating"] != true || got["expanding"] != nil {
		t.Errorf("running pass not listed: %v", got)
	}
	if r := h.send("POST", "/api/leads/evaluate", map[string]any{"ids": []int{1}}, "Idempotency-Key", "key-leads-eval-3"); r.StatusCode != 409 {
		t.Errorf("second pass: %d %s", r.StatusCode, r.body)
	}
	d.scans.release(leadRunClaim("evaluate"))
	if got := h.get("/api/leads").data(); got["evaluating"] != nil {
		t.Errorf("ended pass still listed: %v", got)
	}
}

type pageTransport struct{ hits *atomic.Int32 }

func (p pageTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	p.hits.Add(1)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/html"}}, Request: r,
		Body: io.NopCloser(strings.NewReader("<html><body><p>No openings right now.</p></body></html>"))}, nil
}

func TestLeadsExpand_ReadsTheCareersPageOnceAndRecordsIt(t *testing.T) {
	var hits atomic.Int32
	h := newDomainHarness(t, func(c *Config) { c.Domain.ScanHTTP = &http.Client{Transport: pageTransport{&hits}} })
	repo := h.domainOf().repo
	if err := repo.AddCareersPage(t.Context(), "https://example.com/careers", "Example Corp", "exa"); err != nil {
		t.Fatal(err)
	}
	r := h.send("POST", "/api/leads/expand", map[string]any{}, "Idempotency-Key", "key-leads-expand-1")
	id, _ := r.data()["runId"].(string)
	if r.StatusCode != 202 || id == "" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if run := h.waitRun(id); run["kind"] != "leads" || run["status"] != "succeeded" {
		t.Fatalf("run %v", run)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("the careers page was read %d times, want once", n)
	}
	var page wire.Success[wire.LeadPage]
	strictInto(t, h.get("/api/leads").body, &page)
	if len(page.Data.Rows) != 1 || page.Data.Rows[0].Expanded != "none" {
		t.Errorf("the lead does not say its page was read and held nothing: %s", h.get("/api/leads").body)
	}
}

func TestDiscoveryAPI_SourceFunnel_TotalsLanesAndRefusesABadPeriod(t *testing.T) {
	h := newDomainHarness(t)
	empty := h.get("/api/discovery/funnel")
	if empty.StatusCode != 200 || len(empty.data()["lanes"].([]any)) != 0 || empty.data()["days"] != float64(30) {
		t.Fatalf("empty: %d %s", empty.StatusCode, empty.body)
	}
	repo := h.domainOf().repo
	for source, found := range map[string]int{"indeed": 12, "registry": 30} {
		rec, err := repo.ScanStart(t.Context(), source, nil)
		if err != nil {
			t.Fatal(err)
		}
		rec.Status, rec.Counts.Discovered = discovery.RunCompleted, found
		if _, err := repo.ScanFinish(t.Context(), rec); err != nil {
			t.Fatal(err)
		}
	}
	res := h.get("/api/discovery/funnel?days=7")
	v := res.data()
	lanes := v["lanes"].([]any)
	total := v["total"].(map[string]any)
	if res.StatusCode != 200 || v["days"] != float64(7) || len(lanes) != 2 || total["discovered"] != float64(42) || total["lane"] != "total" {
		t.Fatalf("%d %s", res.StatusCode, res.body)
	}
	if r := h.get("/api/discovery/funnel?days=400"); r.StatusCode != 422 {
		t.Errorf("days=400: %d %s", r.StatusCode, r.body)
	}
}

func TestDiscoveryAPI_FunnelWeek_CountsEachStageOncePerWeek(t *testing.T) {
	h := newDomainHarness(t)
	now := h.clock.now()
	row := h.addCandidacy("Acme Labs", "Platform Engineer", "https://jobs.example.com/acme/1")
	h.patchRow("/api/candidacies/"+idOf(row), map[string]any{"status": "applied"})
	h.patchRow("/api/candidacies/"+idOf(row), map[string]any{"status": "responded"})
	// A rejection is a reply too, though the row never passed through Responded.
	other := h.addCandidacy("Contoso", "Data Engineer", "https://jobs.example.com/contoso/2")
	h.patchRow("/api/candidacies/"+idOf(other), map[string]any{"status": "applied"})
	h.patchRow("/api/candidacies/"+idOf(other), map[string]any{"status": "rejected"})
	day := func(n int) string { return now.AddDate(0, 0, -n).UTC().Format(time.RFC3339) }
	inbox := "INSERT INTO inbox (url, original_url, identity_key, source, first_seen_at, updated_at) VALUES (?1, ?1, ?1, 's', ?2, ?2)"
	job := "INSERT INTO runs (id, kind, status, created_at, updated_at) VALUES (?1, 'evaluate', 'succeeded', ?2, ?2); " +
		"INSERT INTO evaluation_jobs (run_id, identity_key, mode, priority, outcome, created_at, finished_at) VALUES (?1, ?3, 'auto', 'scan', ?4, ?2, ?2)"
	stmts := [][]any{
		{inbox, "a", day(1)}, {inbox, "b", day(2)}, {inbox, "c", day(9)},
		{job, "r1", day(1), "k1", "reported"}, {job, "r2", day(2), "k1", "screened"}, {job, "r3", day(1), "k2", "failed"}, {job, "r4", day(10), "k3", "reported"},
		{"INSERT INTO tracker_events (entry_id, at, actor, kind, previous_status, new_status) VALUES (?1, ?2, 'user', 'status_changed', 'evaluated', 'applied')", row["id"], day(8)},
	}
	if err := h.srv.store.Load().Write(t.Context(), func(tx *sqlTx) error {
		for _, st := range stmts {
			if _, err := tx.Exec(st[0].(string), st[1:]...); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var got wire.Success[wire.FunnelWeek]
	strictInto(t, h.get("/api/discovery/funnel/week").body, &got)
	// The week is today and the six local days before it.
	want := wire.FunnelWeek{Since: now.Local().AddDate(0, 0, -6).Format(time.DateOnly), Current: wire.FunnelStages{Found: 2, Evaluated: 1, Applied: 2, Responded: 2}, Previous: wire.FunnelStages{Found: 1, Evaluated: 1, Applied: 1}}
	if got.Data != want {
		t.Fatalf("got %+v, want %+v", got.Data, want)
	}
	// The stage's link lists the rows it counts.
	var page wire.Success[wire.CandidacyPage]
	strictInto(t, h.get("/api/candidacies?reached=responded,interview,offer,rejected&since="+want.Since).body, &page)
	if page.Data.Total != 2 {
		t.Fatalf("the Responded link lists %d rows, want 2", page.Data.Total)
	}
}
