package discovery

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/workspace"
)

func TestInbox_TopN_EnqueuesOnlyTopRestPending(t *testing.T) {
	e := newEnv(t)
	var jobs []obj
	for i := 1; i <= 60; i++ {
		title := engineer(i)
		if i%3 == 0 {
			title = fmt.Sprintf("Backend Software Engineer %d", i)
		}
		if i%5 == 0 {
			title = fmt.Sprintf("Junior Rust Backend Software Engineer %d", i)
		}
		jobs = append(jobs, ghItem(i, title, "Austin, TX"))
	}
	e.serveGH("example-co", jobs, nil)
	e.add(ghBoard("example-co"))
	p := portals([]string{"software", "backend", "rust"}, nil)
	rec := e.run(ScanRequest{Portals: p})
	if rec.Counts.Inserted != 60 || rec.Counts.Queued != 15 {
		t.Fatalf("counts = %+v", rec.Counts)
	}
	queued := e.inbox(InboxQuery{Status: InboxQueued})
	pending := e.inbox(InboxQuery{Status: InboxPending})
	if len(queued) != 15 || len(pending) != 45 {
		t.Fatalf("queued %d pending %d", len(queued), len(pending))
	}
	minQueued := queued[len(queued)-1].Score
	for _, r := range pending {
		if r.Score > minQueued {
			t.Fatalf("pending row %d scores %d, above a queued row's %d", r.ID, r.Score, minQueued)
		}
	}
	if len(e.enq.reqs) != 15 {
		t.Fatalf("enqueued %d", len(e.enq.reqs))
	}
}

// While an evaluation task has no model, a scan sends none of its rows to
// evaluation: each would fail at no-route and spend an attempt on a posting
// it says nothing about. Every row waits, still one the inbox drain takes.
func TestInbox_TopN_NothingQueuedWhileEvaluationIsUnrouted(t *testing.T) {
	e := newEnv(t)
	e.serveGH("example-co", []obj{ghItem(1, "Software Engineer", "Austin, TX"), ghItem(2, "Backend Software Engineer", "Austin, TX")}, nil)
	e.add(ghBoard("example-co"))
	e.sc.Unrouted = func(context.Context) string { return "deep-eval" }
	rec := e.run(ScanRequest{Portals: eng()})
	if rec.Counts.Inserted != 2 || rec.Counts.Queued != 0 || len(e.enq.reqs) != 0 || rec.Unrouted != "deep-eval" {
		t.Fatalf("counts = %+v, enqueued %d, unrouted %q", rec.Counts, len(e.enq.reqs), rec.Unrouted)
	}
	if stored, err := e.rp.ScanGet(e.ctx, rec.ID); err != nil || stored.Unrouted != "deep-eval" {
		t.Fatalf("stored unrouted %q (%v)", stored.Unrouted, err)
	}
	for _, r := range e.inbox(InboxQuery{}) {
		if r.Status != InboxPending || !r.Waiting() {
			t.Fatalf("row = %+v", r)
		}
	}
}

func TestInbox_Promote_CarriesRowIDAndMode(t *testing.T) {
	e := newEnv(t)
	e.serveGH("example-co", []obj{ghItem(1, "Software Engineer", "Austin, TX")}, nil)
	e.add(ghBoard("example-co"))
	n := 0
	rec := e.run(ScanRequest{Portals: eng(), TopN: &n})
	if rec.Counts.Queued != 0 || len(e.enq.reqs) != 0 {
		t.Fatal("top-n 0 must enqueue nothing")
	}
	rows := e.inbox(InboxQuery{})
	queued, _, err := e.rp.Promote(e.ctx, e.enq, nil, rows[0].ID)
	if err != nil || len(queued) != 1 {
		t.Fatalf("%v %v", queued, err)
	}
	req := e.enq.reqs[0]
	if req.InboxID != rows[0].ID || req.Mode != "deep" || req.RunID != rec.ID || req.URL != rows[0].URL {
		t.Fatalf("request = %+v", req)
	}
	row, _ := e.rp.InboxGet(e.ctx, rows[0].ID)
	if row.Status != InboxQueued || row.JobID == "" || row.Attempts != 1 {
		t.Fatalf("row = %+v", row)
	}
	if _, _, err := e.rp.Promote(e.ctx, e.enq, nil, rows[0].ID); err == nil {
		t.Fatal("a queued row must not be promoted again")
	}
	// The job is attributed to the run so outcomes can arrive later.
	if err := e.rp.ScanOutcome(e.ctx, rec.ID, row.JobID, OutcomeReported); err != nil {
		t.Fatal(err)
	}
	after, _ := e.rp.ScanGet(e.ctx, rec.ID)
	if after.Counts.Outcomes[OutcomeReported] != 1 || after.Counts.Terminal() != rec.Counts.Terminal() {
		t.Fatalf("after = %+v", after.Counts)
	}
}

func TestInbox_TopN_SkipsGateRejectedRows(t *testing.T) {
	e := newEnv(t)
	var jobs []obj
	for i := 1; i <= 30; i++ {
		title := fmt.Sprintf("Software Backend Engineer %d", i)
		if i <= 6 {
			title = fmt.Sprintf("Staff Software Backend Engineer %d", i)
		}
		jobs = append(jobs, ghItem(i, title, "Austin, TX"))
	}
	e.serveGH("example-co", jobs, nil)
	e.add(ghBoard("example-co"))
	// Staff rows score highest here (seniority boost), then the gate rejects them.
	p := portals([]string{"software", "backend"}, nil)
	p.Filters.Title.SeniorityBoost = []workspace.Boost{{Term: "staff", Weight: 5}}
	e.sc.Gate = func(r InboxRow) string {
		if strings.Contains(r.Role, "Staff") {
			return "reject_seniority"
		}
		return ""
	}
	rec := e.run(ScanRequest{Portals: p})
	if rec.Counts.Queued != 15 || rec.Counts.GateRejected != 6 {
		t.Fatalf("counts = %+v", rec.Counts)
	}
	for _, r := range e.inbox(InboxQuery{Status: InboxPending}) {
		if strings.Contains(r.Role, "Staff") && !contains(strings.Join(r.Reasons, ";"), "gate:reject_seniority") {
			t.Fatalf("gate-rejected row %d lost its rule: %v", r.ID, r.Reasons)
		}
	}
	for _, r := range e.inbox(InboxQuery{Status: InboxQueued}) {
		if strings.Contains(r.Role, "Staff") {
			t.Fatalf("a rejected row was queued: %+v", r)
		}
	}
}

func TestDedup_SameRun_KeepsMostRecentlyPosted(t *testing.T) {
	e := newEnv(t)
	e.srv.json("/api/v1/widget/accounts/example-work", obj{"name": "Example Work", "jobs": []obj{
		workableItem("AB12CD34", "QA Engineer", "Exampleton", "CA", "United States", "2026-10-05"),
		workableItem("AB12CD34", "QA Engineer", "Exampleton", "CA", "United States", "2026-10-08"),
	}})
	e.add(Board{ATS: Workable, Slug: "example-work"})
	rec := e.run(ScanRequest{Portals: eng()})
	if rec.Counts.Inserted != 1 || rec.Counts.Duplicates[StoreRun] != 1 || rec.Counts.Discovered != 2 {
		t.Fatalf("counts = %+v", rec.Counts)
	}
	rows := e.inbox(InboxQuery{})
	if !rows[0].PostedAt.Equal(time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("kept %v", rows[0].PostedAt)
	}
}

func TestSources_Workable_MultiLocationGateBeforeDedup(t *testing.T) {
	e := newEnv(t)
	e.srv.json("/api/v1/widget/accounts/example-work", obj{"name": "Example Work", "jobs": []obj{
		workableItem("AB12CD34", "QA Engineer", "Berlin", "", "Germany", "2026-10-08"),
		workableItem("AB12CD34", "QA Engineer", "Austin", "TX", "United States", "2026-10-08"),
	}})
	e.add(Board{ATS: Workable, Slug: "example-work"})
	rec := e.run(ScanRequest{Portals: eng(), Scope: ScopeUS})
	rows := e.inbox(InboxQuery{})
	if rec.Counts.Inserted != 1 || rec.Counts.Filtered[FilterScope] != 1 || len(rows) != 1 || rows[0].Location != "Austin, TX, United States" {
		t.Fatalf("counts %+v rows %+v", rec.Counts, rows)
	}
}

func TestDedup_UnequalATSNativeKeysAreDistinct(t *testing.T) {
	e := newEnv(t)
	e.serveGH("example-co", []obj{ghItem(1001, "Software Engineer", "Austin, TX"), ghItem(2002, "Software Engineer", "Austin, TX")}, nil)
	e.add(ghBoard("example-co"))
	rec := e.run(ScanRequest{Portals: eng()})
	if rec.Counts.Inserted != 2 || sumMap(rec.Counts.Duplicates) != 0 {
		t.Fatalf("counts = %+v", rec.Counts)
	}
}

func TestDedup_CrossHostMirrorCollapsesByCompanyRole(t *testing.T) {
	e := newEnv(t)
	e.trackedRow("Example Co", "Software Engineer", "https://www.example.org/mirror/jobs/123")
	e.serveGH("example-co", []obj{ghItem(1001, "Software Engineer", "Austin, TX")}, nil)
	e.add(ghBoard("example-co"))
	rec := e.run(ScanRequest{Portals: eng()})
	if rec.Counts.Duplicates[StoreTracker] != 1 || rec.Counts.Inserted != 0 {
		t.Fatalf("counts = %+v", rec.Counts)
	}
}

func TestDedup_SameRoleDisjointLocationsNotDuplicates(t *testing.T) {
	e := newEnv(t)
	e.trackedRow("Example Co", "Software Engineer", "https://www.example.org/mirror/jobs/123")
	e.exec(`UPDATE tracker_entries SET location = 'Sampleville'`)
	e.serveGH("example-co", []obj{ghItem(1001, "Software Engineer", "Exampleton")}, nil)
	e.add(ghBoard("example-co"))
	rec := e.run(ScanRequest{Portals: eng()})
	if rec.Counts.Inserted != 1 {
		t.Fatalf("counts = %+v", rec.Counts)
	}
}

func TestInbox_DismissedStillBlocks(t *testing.T) {
	e := newEnv(t)
	e.serveGH("example-co", []obj{ghItem(1, "Software Engineer", "Austin, TX")}, nil)
	e.add(ghBoard("example-co"))
	e.run(ScanRequest{Portals: eng()})
	row := e.inbox(InboxQuery{})[0]
	if err := e.rp.InboxDismiss(e.ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	rec := e.run(ScanRequest{Portals: eng()})
	if rec.Counts.Duplicates[StoreInbox] != 1 || rec.Counts.Inserted != 0 {
		t.Fatalf("counts = %+v", rec.Counts)
	}
}

func TestDedup_RetryKnown_OnlyHistory(t *testing.T) {
	e := newEnv(t)
	e.serveGH("example-co", []obj{ghItem(1, "Software Engineer", "Austin, TX"), ghItem(2, "Software Engineer B", "Austin, TX")}, nil)
	e.add(ghBoard("example-co"))
	// Job 1 is known only to the scan history; job 2 is in the tracker.
	e.exec(`INSERT INTO scan_history (canonical_url, identity_key, first_seen_at, last_seen_at, source, title, company, location, status, last_run_id)
		VALUES ('https://job-boards.greenhouse.io/example-co/jobs/1', 'greenhouse:example-co/1', ?, ?, 'registry', 'Software Engineer', 'Example Co', '', 'filtered', 'scan-old')`, iso(epoch), iso(epoch))
	e.trackedRow("Example Co", "Software Engineer B", "https://job-boards.greenhouse.io/example-co/jobs/2")
	plain := e.run(ScanRequest{Portals: eng()})
	if plain.Counts.Duplicates[StoreHistory] != 1 || plain.Counts.Duplicates[StoreTracker] != 1 || plain.Counts.Inserted != 0 {
		t.Fatalf("plain = %+v", plain.Counts)
	}
	retry := e.run(ScanRequest{Portals: eng(), RetryKnown: true})
	if retry.Counts.Inserted != 1 || retry.Counts.Duplicates[StoreTracker] != 1 || retry.Counts.Duplicates[StoreHistory] != 0 {
		t.Fatalf("retry = %+v", retry.Counts)
	}
}

func TestSources_Greenhouse_DescriptionIncludesFormQuestionsFlagsClearance(t *testing.T) {
	e := newEnv(t)
	e.serveGH("example-co", []obj{ghItem(7, "Software Engineer", "Austin, TX")}, func(id int) obj {
		return obj{"id": id, "content": "<p>Build systems. No special requirements listed here.</p>",
			"questions": []obj{{"label": "Must be able to obtain and maintain a security clearance. Do you agree?", "required": true}}}
	})
	e.add(ghBoard("example-co"))
	e.run(ScanRequest{Portals: eng()})
	row := e.inbox(InboxQuery{})[0]
	if !row.ClearanceRequired || !contains(row.Description, "security clearance") {
		t.Fatalf("row = %+v", row)
	}
}

func TestRun_DetailFailureKeepsRowDegraded(t *testing.T) {
	e := newEnv(t)
	e.srv.json("/v1/boards/example-co/jobs", ghJobs("example-co", ghItem(1, "Software Engineer", "Austin, TX")))
	e.add(ghBoard("example-co"))
	rec := e.run(ScanRequest{Portals: eng()})
	if rec.Counts.Inserted != 1 || rec.Counts.EnrichmentDegraded != 1 || rec.Counts.Enriched != 0 {
		t.Fatalf("counts = %+v", rec.Counts)
	}
	if row := e.inbox(InboxQuery{})[0]; row.Description != "" {
		t.Fatalf("a failed detail read leaves the description empty: %q", row.Description)
	}
}

func TestFilters_BlockedBeforeDetailFetch(t *testing.T) {
	e := newEnv(t)
	e.serveGH("blocked-co", []obj{ghItem(1, "Software Engineer", "Austin, TX")}, nil)
	e.add(ghBoard("blocked-co"))
	if _, err := e.rp.BlockCompany(e.ctx, "Blocked Co", "", "user"); err != nil {
		t.Fatal(err)
	}
	rec := e.run(ScanRequest{Portals: eng()})
	if rec.Counts.Filtered[FilterBlockedCompany] != 1 || e.srv.hitCount("/v1/boards/blocked-co/jobs/1") != 0 || rec.Counts.Inserted != 0 {
		t.Fatalf("counts = %+v detail hits=%d", rec.Counts, e.srv.hitCount("/v1/boards/blocked-co/jobs/1"))
	}
}

func TestRun_ScopeResolution(t *testing.T) {
	e := newEnv(t)
	e.serveGH("example-co", []obj{ghItem(1, "Software Engineer", "Berlin, Germany"), ghItem(2, "Software Engineer", "Austin, TX")}, nil)
	e.add(ghBoard("example-co"))
	var prof workspace.Profile
	prof.EligibilityGate.AllowedCountries = []string{"DE"}
	rec := e.run(ScanRequest{Portals: eng(), Profile: prof})
	if rec.Params["scope"] != ScopeEurope || rec.Params["scope_from"] != "eligibility_gate.allowed_countries" || rec.Counts.Inserted != 1 {
		t.Fatalf("params = %v counts %+v", rec.Params, rec.Counts)
	}
	// With no allowed countries the countries the user may work in decide it,
	// as the evaluation's geography rule reads them.
	var authorized workspace.Profile
	authorized.WorkAuthorization.AuthorizedCountries = []string{"US"}
	rec = e.run(ScanRequest{Portals: eng(), Profile: authorized})
	if rec.Params["scope"] != ScopeUS || rec.Params["scope_from"] != "work_authorization.authorized_countries" || rec.Counts.Inserted != 1 {
		t.Fatalf("params = %v counts %+v", rec.Params, rec.Counts)
	}
	if _, err := e.sc.Run(e.ctx, ScanRequest{Source: "registry", Scope: "moon"}); err == nil || !contains(err.Error(), "worldwide") {
		t.Fatalf("err = %v", err)
	}
	if recs, _ := e.rp.ScanList(e.ctx, "registry", 10); len(recs) != 2 {
		t.Fatal("a refused start created a record")
	}
}

func TestRegistry_Harvest_AllURLFieldsIdempotent(t *testing.T) {
	e := newEnv(t)
	e.srv.json("/v1/boards/example-co/jobs", ghJobs("example-co", obj{"id": 1, "title": "Software Engineer", "location": obj{"name": "Austin, TX"},
		"absolute_url": "https://jobs.lever.co/new-slug/5d1c0a9e-0000-4000-8000-000000000009"}))
	e.srv.json("/v1/boards/example-co/jobs/1", obj{"id": 1, "content": "x"})
	e.add(ghBoard("example-co"))
	e.run(ScanRequest{Portals: eng()})
	row := e.registry(Board{ATS: Lever, Slug: "new-slug"})
	if row.Source != "registry" {
		t.Fatalf("harvested row = %+v", row)
	}
	// An existing row is left unchanged.
	if added, _ := e.rp.RegistryAdd(e.ctx, "other", Board{ATS: Lever, Slug: "new-slug"}); added != 0 {
		t.Fatal("existing row re-added")
	}
	if e.registry(Board{ATS: Lever, Slug: "new-slug"}).Source != "registry" {
		t.Fatal("source changed")
	}
}

func TestHistory_RowPerURLUpdatesLastSeen(t *testing.T) {
	e := newEnv(t)
	e.serveGH("example-co", []obj{ghItem(1, "Software Engineer", "Austin, TX")}, nil)
	e.add(ghBoard("example-co"))
	first := e.run(ScanRequest{Portals: eng()})
	url := "https://job-boards.greenhouse.io/example-co/jobs/1"
	h1, err := e.rp.HistoryGet(e.ctx, url)
	if err != nil || h1.Status != HistoryInbox || h1.Score == nil || h1.LastRunID != first.ID {
		t.Fatalf("h1 = %+v %v", h1, err)
	}
	e.clk.advance(24 * time.Hour)
	second := e.run(ScanRequest{Portals: eng()})
	h2, err := e.rp.HistoryGet(e.ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if !h2.FirstSeen.Equal(h1.FirstSeen) || !h2.LastSeen.After(h1.LastSeen) || h2.LastRunID != second.ID || h2.Status != HistoryInbox {
		t.Fatalf("h2 = %+v", h2)
	}
	var n int
	if err := e.st.Read().QueryRow(`SELECT COUNT(*) FROM scan_history`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("history rows = %d", n)
	}
}

func TestScan_FilteredRowsNotWrittenToHistory(t *testing.T) {
	e := newEnv(t)
	e.serveGH("example-co", []obj{ghItem(1, "Barista", "Austin, TX")}, nil)
	e.add(ghBoard("example-co"))
	e.run(ScanRequest{Portals: eng()})
	var n int
	if err := e.st.Read().QueryRow(`SELECT COUNT(*) FROM scan_history`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("history rows = %d", n)
	}
}

func TestScan_NeverUsesAModel(t *testing.T) {
	out, err := goListImports(t)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range out {
		if strings.Contains(imp, "internal/llm") || strings.Contains(imp, "internal/agent") || strings.Contains(imp, "internal/browser") {
			t.Fatalf("discovery imports %s; sweeps and rankings call no model and no browser", imp)
		}
	}
}

func TestScan_NeverSubmitsOrTypes(t *testing.T) {
	e := newEnv(t)
	e.serveGH("example-co", []obj{ghItem(1, "Software Engineer", "Austin, TX")}, nil)
	e.add(ghBoard("example-co"))
	e.run(ScanRequest{Portals: eng()})
	e.srv.mu.Lock()
	defer e.srv.mu.Unlock()
	if len(e.srv.methods) != 1 || e.srv.methods["GET"] == 0 {
		t.Fatalf("a scan may only read: %v", e.srv.methods)
	}
}

func TestSources_DescriptionCappedAndOnlyForSurvivors(t *testing.T) {
	e := newEnv(t)
	long := obj{"id": "5d1c0a9e-0000-4000-8000-000000000001", "text": "Platform Engineer", "createdAt": epoch.UnixMilli(),
		"hostedUrl": "https://jobs.lever.co/example-lever/5d1c0a9e-0000-4000-8000-000000000001", "categories": obj{"location": "Austin, TX"},
		"descriptionPlain": strings.Repeat("é", 30000)}
	barista := obj{"id": "5d1c0a9e-0000-4000-8000-000000000002", "text": "Barista", "createdAt": epoch.UnixMilli(),
		"hostedUrl": "https://jobs.lever.co/example-lever/5d1c0a9e-0000-4000-8000-000000000002", "categories": obj{"location": "Austin, TX"},
		"descriptionPlain": "Pour coffee."}
	e.srv.json("/v0/postings/example-lever", []obj{long, barista})
	e.add(Board{ATS: Lever, Slug: "example-lever", Company: "Example Lever"})
	e.run(ScanRequest{Portals: eng()})
	rows := e.inbox(InboxQuery{})
	if len(rows) != 1 || len([]rune(rows[0].Description)) != 20000 {
		t.Fatalf("rows = %d, description length %d", len(rows), len([]rune(rows[0].Description)))
	}
}

// Outcomes arrive from evaluation jobs while the run record is still being
// rewritten from memory; closing the run must not drop them.
func TestScanFinish_KeepsOutcomesRecordedBeforeIt(t *testing.T) {
	e := newEnv(t)
	rec, err := e.rp.ScanStart(e.ctx, "registry", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.rp.ScanOutcome(e.ctx, rec.ID, "job-1", OutcomeReported); err != nil {
		t.Fatal(err)
	}
	// The in-memory record has not seen the outcome.
	if err := e.rp.ScanSave(e.ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := e.rp.ScanOutcome(e.ctx, rec.ID, "job-2", OutcomeGated); err != nil {
		t.Fatal(err)
	}
	rec.Status = RunCompleted
	closed, err := e.rp.ScanFinish(e.ctx, rec)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := e.rp.ScanGet(e.ctx, rec.ID)
	for name, c := range map[string]Counts{"returned": closed.Counts, "stored": got.Counts} {
		if c.Outcomes[OutcomeReported] != 1 || c.Outcomes[OutcomeGated] != 1 {
			t.Errorf("%s outcomes = %v", name, c.Outcomes)
		}
	}
}

func TestScanOutcome_ConcurrentNotificationsSurviveStaleSnapshots(t *testing.T) {
	e := newEnv(t)
	var recs []Record
	for _, source := range []string{"registry", "tracked"} {
		rec, err := e.rp.ScanStart(e.ctx, source, nil)
		if err != nil {
			t.Fatal(err)
		}
		recs = append(recs, rec)
	}
	var wg sync.WaitGroup
	for _, rec := range recs {
		for n := 0; n < 16; n++ {
			wg.Add(1)
			go func(rec Record, n int) {
				defer wg.Done()
				if err := e.rp.ScanOutcome(e.ctx, rec.ID, "shared", OutcomeReported); err != nil {
					t.Error(err)
				}
				if err := e.rp.ScanOutcome(e.ctx, rec.ID, fmt.Sprintf("unique-%d", n), OutcomeGated); err != nil {
					t.Error(err)
				}
				if err := e.rp.ScanSave(e.ctx, rec); err != nil {
					t.Error(err)
				}
			}(rec, n)
		}
	}
	wg.Wait()
	for _, rec := range recs {
		rec.Status = RunCompleted
		closed, err := e.rp.ScanFinish(e.ctx, rec)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.rp.ScanOutcome(e.ctx, rec.ID, "shared", OutcomeReported); err != nil {
			t.Error(err)
		}
		if err := e.rp.ScanOutcome(e.ctx, rec.ID, "shared", OutcomeFailed); err == nil {
			t.Error("conflicting terminal outcome accepted")
		}
		got, err := e.rp.ScanGet(e.ctx, rec.ID)
		if err != nil {
			t.Fatal(err)
		}
		for name, r := range map[string]Record{"closed": closed, "stored": got} {
			if r.Counts.Outcomes[OutcomeReported] != 1 || r.Counts.Outcomes[OutcomeGated] != 16 || len(r.JobOutcomes) != 17 {
				t.Errorf("%s %s counts=%v map=%v", rec.Source, name, r.Counts.Outcomes, r.JobOutcomes)
			}
		}
		clone := got.clone()
		clone.JobOutcomes["shared"] = OutcomeFailed
		if got.JobOutcomes["shared"] != OutcomeReported {
			t.Error("clone aliases attribution map")
		}
	}
}

// A scan queued only the posting address, so the evaluation took the page's
// own heading as the role: a careers site that heads every posting with the
// employer's name filed the row as that name twice. The row's role, company
// and place go with the request, from a scan and from a promotion alike, and
// so does the date the board gave the posting.
func TestInbox_EnqueueCarriesTheRowsRoleCompanyAndPlace(t *testing.T) {
	e := newEnv(t)
	first, second := ghItem(1, "Software Engineer", "Austin, TX"), ghItem(2, "Backend Software Engineer", "Austin, TX")
	first["first_published"], second["first_published"] = "2026-01-05T00:00:00Z", "2026-01-06T00:00:00Z"
	e.serveGH("example-co", []obj{first, second}, nil)
	e.add(ghBoard("example-co"))
	one := 1
	if rec := e.run(ScanRequest{Portals: eng(), TopN: &one}); rec.Counts.Queued != 1 {
		t.Fatalf("counts = %+v", rec.Counts)
	}
	pending := e.inbox(InboxQuery{Status: InboxPending})
	if len(pending) != 1 {
		t.Fatalf("pending = %+v", pending)
	}
	if _, _, err := e.rp.Promote(e.ctx, e.enq, nil, pending[0].ID); err != nil {
		t.Fatal(err)
	}
	if len(e.enq.reqs) != 2 {
		t.Fatalf("requests = %+v", e.enq.reqs)
	}
	for _, req := range e.enq.reqs {
		row, err := e.rp.InboxGet(e.ctx, req.InboxID)
		if err != nil {
			t.Fatal(err)
		}
		if row.Role == "" || row.Company == "" || req.Title != row.Role || req.Company != row.Company || req.Location != row.Location ||
			row.PostedAt.IsZero() || !req.PostedAt.Equal(row.PostedAt) {
			t.Errorf("request %+v for row %q / %q / %q", req, row.Company, row.Role, row.Location)
		}
	}
}

// "Use my target roles" gives a saved search only to the sources the catalog
// marks SavedSearch; the mark must be true of the source's own run, which
// skips without a saved search even when the profile names target roles.
func TestCatalog_SavedSearchSourcesSkipARunWithoutOne(t *testing.T) {
	profile := workspace.Profile{Targets: workspace.Targets{Roles: []workspace.Role{{Title: "Data Engineer"}}}}
	for _, s := range Sources {
		if !s.SavedSearch {
			continue
		}
		err := feedReaderFor(s.Name)(t.Context(), &run{req: ScanRequest{Source: s.Name, Profile: profile}})
		var end *endRun
		if !errors.As(err, &end) || end.status != RunSkipped {
			t.Errorf("%s without a saved search: %v", s.Name, err)
		}
	}
}

// "Use my target roles" on the shipped portals.yml: the role titles the filter
// lacks (trimmed, case folded) join it, the primary role becomes the one search
// of each enabled saved-search board that has none, and what is there stays.
func TestTargetRoles_WritesTheRolesOnceIntoTheShippedPortalsFile(t *testing.T) {
	ws := workspace.Open(filepath.Join(t.TempDir(), "ws"))
	if _, err := ws.Init(); err != nil {
		t.Fatal(err)
	}
	cur, err := ws.Read(workspace.FilePortals)
	if err != nil {
		t.Fatal(err)
	}
	set := func(path, value string) workspace.Op {
		return workspace.Op{Kind: workspace.OpSet, Path: path, Value: value}
	}
	src, err := workspace.ApplyOps(cur.Data, []workspace.Op{
		set("sources.indeed.enabled", "true"), set("sources.glassdoor", "{}"), set("sources.linkedin.enabled", "true"),
		set("sources.builtin.queries", `["Platform Engineer"]`), set("sources.ziprecruiter.enabled", "false"),
	})
	if err != nil {
		t.Fatal(err)
	}
	parse := func(b []byte) workspace.Portals {
		p, rep := workspace.ParsePortals(b)
		if !rep.OK() {
			t.Fatalf("portals.yml is invalid: %s\n%s", rep.String(), b)
		}
		return p
	}
	profile := workspace.Profile{Targets: workspace.Targets{Roles: []workspace.Role{{Title: "Data Engineer", Priority: "secondary"}, {Title: " Backend Engineer "}, {Title: "data engineer"}}}}
	before := parse(src)
	out, err := workspace.ApplyOps(src, PlanTargetRoles(before, profile).Ops())
	if err != nil {
		t.Fatal(err)
	}
	after := parse(out)
	if !slices.Equal(after.Filters.Title.Positive, []string{"Data Engineer", "Backend Engineer"}) || !slices.Equal(after.Filters.Title.Negative, before.Filters.Title.Negative) {
		t.Errorf("title filter %+v", after.Filters.Title)
	}
	for src, want := range map[string]string{"indeed": "Backend Engineer", "glassdoor": "Backend Engineer", "builtin": "Platform Engineer", "linkedin": "", "ziprecruiter": ""} {
		var got []string
		for _, q := range after.Sources[src].Queries {
			got = append(got, q.Value)
		}
		if strings.Join(got, ",") != want {
			t.Errorf("%s searches %q, want %q", src, got, want)
		}
	}
	if again := PlanTargetRoles(after, profile); len(again.Ops()) != 0 {
		t.Errorf("a second plan writes %+v", again)
	}
}
