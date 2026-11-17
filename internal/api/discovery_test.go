package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/api/wire"
	"applysmith/internal/discovery"
)

const leverURL = "https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-00000000000"

// addInbox adds a posting to the inbox as `applysmith inbox add` does and returns
// the new row.
func (h *harness) addInbox(u string) discovery.InboxRow {
	h.t.Helper()
	res, err := h.domainOf().repo.InboxAddURL(h.t.Context(), u)
	if err != nil || res.Row == nil {
		h.t.Fatalf("add %s: %+v %v", u, res, err)
	}
	return *res.Row
}

func TestInboxListDismissAndCounts(t *testing.T) {
	h := newDomainHarness(t)
	added := h.addInbox(leverURL + "1?lever-source=x")
	h.addInbox(leverURL + "2")
	list := h.get("/api/inbox?pageSize=1&page=2")
	var page wire.Success[wire.InboxPage]
	strictInto(t, list.body, &page)
	if list.StatusCode != 200 || page.Data.Total != 2 || len(page.Data.Rows) != 1 || page.Data.Counts[wire.InboxPending] != 2 || page.Data.Counts[wire.InboxDismissed] != 0 {
		t.Fatalf("list: %s", list.body)
	}
	id := itoa(int(added.ID))
	dismissed := h.post("/api/inbox/"+id+"/dismiss", nil)
	if dismissed.StatusCode != 200 || dismissed.data()["status"] != wire.InboxDismissed {
		t.Fatalf("dismiss: %d %s", dismissed.StatusCode, dismissed.body)
	}
	if again := h.get("/api/inbox?status=dismissed"); again.data()["total"].(float64) != 1 {
		t.Errorf("dismissed rows: %s", again.body)
	}
	if r := h.post("/api/inbox/9999/dismiss", nil); r.StatusCode != 404 {
		t.Errorf("unknown row: %d %s", r.StatusCode, r.body)
	}
	restored := h.post("/api/inbox/"+id+"/restore", nil)
	if restored.StatusCode != 200 || restored.data()["status"] != wire.InboxPending {
		t.Fatalf("restore: %d %s", restored.StatusCode, restored.body)
	}
	if again := h.post("/api/inbox/"+id+"/restore", nil); again.StatusCode != 409 {
		t.Errorf("restoring a waiting row: %d %s", again.StatusCode, again.body)
	}
	if r := h.get("/api/inbox?status=gone"); r.StatusCode != 422 {
		t.Errorf("bad status: %d", r.StatusCode)
	}
}

func TestPromotingInboxRowsQueuesEvaluationsAndMovesTheRows(t *testing.T) {
	h := newDomainHarness(t, withReader(immediateReader{text: postingBody("promote")}))
	id := int(h.addInbox(leverURL + "3").ID)
	before := h.boardRevision("scans")
	r := h.post("/api/inbox/promote", map[string]any{"ids": []int{id}})
	var env wire.Success[wire.InboxPromoteResult]
	strictInto(t, r.body, &env)
	if r.StatusCode != 200 || len(env.Data.Queued) != 1 || int(env.Data.Queued[0]) != id || len(env.Data.Rejected) != 0 {
		t.Fatalf("promote: %d %s", r.StatusCode, r.body)
	}
	// The evaluation finishes and the inbox row follows it to evaluated.
	deadline := time.Now().Add(10 * time.Second)
	pause := newPollPause()
	for {
		got := h.get("/api/inbox?status=evaluated")
		// The page reads its rows and its total one after the other, so the
		// row may be counted in a reply that does not list it yet: wait for
		// the row itself.
		if rows := got.data()["rows"].([]any); len(rows) == 1 {
			// The row carries its evaluation's outcome, so the list needs no
			// request per row to link the report.
			if row := rows[0].(map[string]any); row["outcome"] == nil || (row["outcome"] == "reported" && row["reportNumber"] == nil) {
				t.Errorf("evaluated row without its outcome: %v", row)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the row never reached evaluated: %s", h.get("/api/inbox").body)
		}
		pause()
	}
	if h.boardRevision("scans") <= before {
		t.Error("the board was not told about the inbox change")
	}
	// A row that is not pending is not promoted again.
	if again := h.post("/api/inbox/promote", map[string]any{"ids": []int{id}}); again.StatusCode != 409 || again.errCode() != "conflict" {
		t.Errorf("promote again: %d %s", again.StatusCode, again.body)
	}
	if r := h.post("/api/inbox/promote", map[string]any{"ids": []int{id}, "top": 1}); r.StatusCode != 422 {
		t.Errorf("ids with top: %d", r.StatusCode)
	}
}

// The scheduled inbox.evaluate job sends the waiting rows to evaluation
// without a press, behind the eligibility gate a promotion uses, and sends
// none while the day's model spend is at the daily cap.
func TestInboxEvaluateSchedule_DrainsTheWaitingRows(t *testing.T) {
	h := newDomainHarness(t)
	d := h.domainOf()
	ctx := t.Context()
	var ids []int64
	for _, u := range []string{"https://jobs.example.com/p/drain-1", "https://careers.blocked.example/p/drain-2"} {
		add, err := d.repo.InboxAddURL(ctx, u)
		if err != nil || add.Row == nil {
			t.Fatalf("add %s: %+v %v", u, add, err)
		}
		ids = append(ids, add.Row.ID)
	}
	if _, err := d.repo.BlockCompany(ctx, "careers.blocked.example", "test", "test"); err != nil {
		t.Fatal(err)
	}
	made := h.post("/api/schedules", map[string]any{"name": "inbox evaluation", "kind": "inbox.evaluate", "preset": map[string]any{"kind": "daily", "time": "23:30", "timezone": "UTC"}})
	if made.StatusCode != 201 {
		t.Fatalf("schedule: %d %s", made.StatusCode, made.body)
	}
	run := func() string {
		t.Helper()
		r := h.post("/api/schedules/"+itoa(int(made.data()["id"].(float64)))+"/run", nil)
		if r.StatusCode != 202 {
			t.Fatalf("run: %d %s", r.StatusCode, r.body)
		}
		id := r.data()["runId"].(string)
		if run := h.waitRun(id); run["status"] != "succeeded" {
			t.Fatalf("run %v", run)
		}
		if err := d.pool.Drain(ctx); err != nil {
			t.Fatal(err)
		}
		return id
	}
	row := func(i int) discovery.InboxRow {
		t.Helper()
		r, err := d.repo.InboxGet(ctx, ids[i])
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if err := h.srv.store.Load().Write(ctx, func(tx *sqlTx) error {
		_, err := tx.Exec(`INSERT INTO llm_ledger (call_id, started_at, task, provider_id, provider_kind, model, attempt, candidate_source, outcome, cost_usd, billing)
			VALUES ('spend', ?, 'deep-eval', 'p', 'openai-compatible', 'm', 1, 'primary', 'ok', 500, 'metered')`, h.clock.now().UTC().Format("2006-01-02T15:04:05.000Z"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	h.clock.advance(time.Minute) // past the router's cached read of the spend
	run()
	var jobs int
	if err := h.srv.store.Load().Read().QueryRow(`SELECT count(*) FROM evaluation_jobs`).Scan(&jobs); err != nil || jobs != 0 || row(0).Status != discovery.InboxPending {
		t.Fatalf("at the cap: %d jobs, row %s (%v)", jobs, row(0).Status, err)
	}
	if err := d.router.SetDailyCap(ctx, 0); err != nil {
		t.Fatal(err)
	}
	h.clock.advance(time.Minute)
	drain := run()
	if got := row(0).Status; got != discovery.InboxEvaluated {
		t.Errorf("the waiting row is %s", got)
	}
	// The evaluation is the drain run's, told through its roll-up (F-2).
	var sentBy string
	if err := h.srv.store.Load().Read().QueryRow(`SELECT json_extract(input_json, '$.Drain') FROM runs WHERE kind = 'evaluate'`).Scan(&sentBy); err != nil || sentBy != drain {
		t.Errorf("the evaluation was sent by %q (%v), want the drain run %s", sentBy, err, drain)
	}
	if r := row(1); r.Status != discovery.InboxPending || !slices.ContainsFunc(r.Reasons, func(s string) bool { return strings.HasPrefix(s, "gate:") }) {
		t.Errorf("the blocked company's row: %s %v", r.Status, r.Reasons)
	}
}

func TestBlockedCompaniesAddListAndRemove(t *testing.T) {
	h := newDomainHarness(t)
	add := h.post("/api/blocked-companies", map[string]any{"name": "Staffing Example Inc", "reason": "staffing firm"})
	var env wire.Success[wire.BlockedCompany]
	strictInto(t, add.body, &env)
	if add.StatusCode != 201 || env.Data.Name != "Staffing Example Inc" || env.Data.Reason != "staffing firm" || env.Data.AddedBy != "local-token" {
		t.Fatalf("add: %d %s", add.StatusCode, add.body)
	}
	list := h.get("/api/blocked-companies")
	var l wire.Success[wire.BlockedCompanyList]
	strictInto(t, list.body, &l)
	if len(l.Data.Companies) != 1 {
		t.Fatalf("list: %s", list.body)
	}
	if r := h.post("/api/blocked-companies", map[string]any{"name": "   "}); r.StatusCode != 422 {
		t.Errorf("blank name: %d %s", r.StatusCode, r.body)
	}
	if r := h.send("DELETE", "/api/blocked-companies?name=Nobody", nil); r.StatusCode != 404 {
		t.Errorf("unblock an unknown company: %d", r.StatusCode)
	}
	del := h.send("DELETE", "/api/blocked-companies?name=Staffing+Example+Inc", nil)
	if del.StatusCode != 200 || len(del.data()["companies"].([]any)) != 0 {
		t.Errorf("unblock: %d %s", del.StatusCode, del.body)
	}
}

// ---- scans ----

// atsServer serves one synthetic Greenhouse board.
func atsServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/boards/example-co/jobs":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jobs": []map[string]any{{
					"id": 41, "title": "Software Engineer", "location": map[string]any{"name": "Remote - United States"}, "company_name": "Example Co",
					"absolute_url": "https://job-boards.greenhouse.io/example-co/jobs/41", "updated_at": "2026-01-01T00:00:00Z",
				}},
				"meta": map[string]any{"total": 1},
			})
		case "/v1/boards/example-co/jobs/41":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 41, "content": "<p>Build backend services in Go for Example Co.</p>"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func scanHarness(t *testing.T) *harness {
	t.Helper()
	srv := atsServer(t)
	h := newDomainHarness(t, func(c *Config) {
		c.Domain.ScanEndpoints = discovery.Endpoints{discovery.Greenhouse: srv.URL}
	})
	if _, err := h.domainOf().repo.RegistryAdd(t.Context(), "test", discovery.Board{ATS: discovery.Greenhouse, Slug: "example-co", Company: "Example Co"}); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) startScan(key string, body map[string]any) result {
	h.t.Helper()
	return h.send("POST", "/api/scans", body, "Idempotency-Key", key)
}

func (h *harness) waitRun(id string) map[string]any {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	pause := newPollPause()
	for {
		r := h.get("/api/runs/" + id)
		if s, _ := r.data()["status"].(string); terminal(s) {
			return r.data()
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("run %s did not end: %s", id, r.body)
		}
		pause()
	}
}

func TestScanIsAcceptedRunsAndRecordsItself(t *testing.T) {
	h := scanHarness(t)
	r := h.startScan("key-scan-0001", map[string]any{"source": "registry", "topN": 0})
	id, _ := r.data()["runId"].(string)
	if r.StatusCode != http.StatusAccepted || id == "" || r.Header.Get("Location") != "/api/runs/"+id {
		t.Fatalf("%d %v %s", r.StatusCode, r.Header, r.body)
	}
	run := h.waitRun(id)
	if run["kind"] != "scan" || run["status"] != "succeeded" {
		t.Fatalf("run %v", run)
	}
	scans := h.get("/api/scans")
	var env wire.Success[wire.ScanList]
	strictInto(t, scans.body, &env)
	if len(env.Data.Scans) != 1 {
		t.Fatalf("scans: %s", scans.body)
	}
	rec := env.Data.Scans[0]
	if rec.Source != "registry" || rec.Status != "completed" || rec.Counts.Inserted != 1 || rec.Counts.Discovered != 1 || rec.FinishedAt == nil || rec.RunID != id {
		t.Errorf("record %+v", rec)
	}
	one := h.get("/api/scans/" + rec.ID)
	if one.StatusCode != 200 || one.data()["id"] != rec.ID {
		t.Errorf("one scan: %d %s", one.StatusCode, one.body)
	}
	if h.get("/api/scans/scan-missing").StatusCode != 404 {
		t.Error("unknown scan")
	}
	inbox := h.get("/api/inbox")
	if inbox.data()["total"].(float64) != 1 {
		t.Errorf("the inbox holds %v rows, want 1", inbox.data()["total"])
	}
	// The run stream of the ended scan names the scan record.
	s := h.openRunStream(id)
	s.next()
	var final wire.RunFinal
	if m := s.next(); m.name != "final" {
		t.Fatalf("event %+v", m)
	} else if err := json.Unmarshal([]byte(m.data), &final); err != nil {
		t.Fatal(err)
	}
	if final.Status != wire.RunSucceeded {
		t.Errorf("final %+v", final)
	}
}

func TestScanRequestValidation(t *testing.T) {
	h := scanHarness(t)
	if r := h.send("POST", "/api/scans", map[string]any{"source": "registry"}); r.StatusCode != 400 {
		t.Errorf("no key: %d", r.StatusCode)
	}
	if r := h.startScan(freshKey(), map[string]any{"source": "nowhere"}); r.StatusCode != 422 || r.errCode() != "validation_failed" {
		t.Errorf("unknown source: %d %s", r.StatusCode, r.body)
	}
	if h.runCount() != 0 {
		t.Errorf("%d runs created by invalid requests", h.runCount())
	}
}

func TestScanFromAnInvalidProfileCreatesNoRun(t *testing.T) {
	h := scanHarness(t)
	if err := writeFile(h.ws.Root+"/profile.yml", "version: not-a-number\n"); err != nil {
		t.Fatal(err)
	}
	r := h.startScan(freshKey(), map[string]any{"source": "registry"})
	if r.StatusCode != 422 || r.errCode() != "config_invalid" || h.runCount() != 0 {
		t.Errorf("%d %s (runs %d)", r.StatusCode, r.body, h.runCount())
	}
}

func TestConcurrentScanRequestsOfOneSourceAdmitOne(t *testing.T) {
	release := make(chan struct{})
	gate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		http.NotFound(w, r)
	}))
	t.Cleanup(gate.Close)
	h := newDomainHarness(t, func(c *Config) { c.Domain.ScanEndpoints = discovery.Endpoints{discovery.Greenhouse: gate.URL} })
	if _, err := h.domainOf().repo.RegistryAdd(t.Context(), "test", discovery.Board{ATS: discovery.Greenhouse, Slug: "example-co", Company: "Example Co"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- h.startScan(freshKey(), map[string]any{"source": "registry", "topN": 0}).StatusCode
		}()
	}
	wg.Wait()
	close(codes)
	accepted, refused := 0, 0
	for c := range codes {
		switch c {
		case 202:
			accepted++
		case 409:
			refused++
		}
	}
	close(release)
	if accepted != 1 || refused != 7 {
		t.Errorf("%d accepted, %d refused", accepted, refused)
	}
}

// routeScanWindow is an owned, read-only browser double at the HTTP boundary.
// Its payloads follow the extraction protocol; it never accesses a live site.
type routeScanWindow struct {
	mu     sync.Mutex
	opened int
	signIn bool
}

func (w *routeScanWindow) OpenTab(context.Context, string, string) (discovery.Tab, error) {
	w.mu.Lock()
	w.opened++
	w.mu.Unlock()
	return &routeScanTab{win: w}, nil
}

type routeScanTab struct {
	win *routeScanWindow
	url string
}

func (t *routeScanTab) Goto(_ context.Context, u string) (discovery.Nav, error) {
	t.url = u
	if t.win.signIn {
		t.url = "https://www.linkedin.com/login"
	}
	return discovery.Nav{URL: t.url, Status: 200}, nil
}
func (t *routeScanTab) Eval(_ context.Context, expr string, out any) error {
	if out == nil {
		return nil
	}
	var payload any
	switch {
	case strings.Contains(expr, "/*aj:probe*/"):
		payload = map[string]any{"url": t.url, "title": "Owned jobs", "text": "Jobs", "password": t.win.signIn}
	case strings.Contains(expr, "/*aj:detail*/"):
		payload = map[string]any{"url": t.url, "title": "Software Engineer", "company": "Owned Fixture", "location": "Remote - United States", "desc": postingBody("route-browser")}
	case strings.Contains(expr, "/*aj:scroll"):
		payload = 0
	case t.win.signIn:
		payload = map[string]any{"url": t.url, "cards": 0, "rows": []any{}}
	default:
		payload = map[string]any{"url": t.url, "cards": 1, "rows": []map[string]any{{"id": "987654321", "title": "Software Engineer", "company": "Owned Fixture", "location": "Remote - United States", "href": "https://www.linkedin.com/jobs/view/987654321", "age": "1 hour ago"}}}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
func (*routeScanTab) PopupURLs() []string { return nil }
func (*routeScanTab) Close()              {}
func (*routeScanTab) ClearChallenge(context.Context) (discovery.ChallengeClear, error) {
	return discovery.ChallengeClear{Reason: "no-challenge"}, nil
}

func TestBrowserScanRoutes_FirstManualSignInRaisesNeedsYou(t *testing.T) {
	win := &routeScanWindow{signIn: true}
	h := newDomainHarness(t, func(c *Config) { c.Domain.ScanWindow = win })
	r := h.startScan(freshKey(), map[string]any{"source": "linkedin", "search": "engineer", "topN": 0, "force": true})
	if r.StatusCode != 202 {
		t.Fatalf("start: %d %s", r.StatusCode, r.body)
	}
	h.waitRun(r.data()["runId"].(string))
	scans, err := h.domainOf().repo.ScanList(t.Context(), "linkedin", 10)
	if err != nil || len(scans) != 1 || scans[0].Status != discovery.RunNeedsSignIn {
		t.Fatalf("scans: %+v %v", scans, err)
	}
	var inbox wire.Success[wire.NeedsYouInbox]
	strictInto(t, h.get("/api/needs-you").body, &inbox)
	var item *wire.NeedsYouItem
	for i := range inbox.Data.Items {
		if inbox.Data.Items[i].Entry.ID == "scan-sign-in-linkedin" {
			item = &inbox.Data.Items[i]
		}
	}
	// The item links straight to the session sync of the site, which the user
	// starts, and offers the scan again.
	if item == nil || item.Entry.Fix == nil || item.Entry.Fix.To != "/browser?sync=linkedin.com" || item.Entry.Fix.Scan != "linkedin" || item.Entry.Fix.Label == "" {
		t.Fatalf("needs-you item %+v", item)
	}
	// The next scan of the source that gets past the sign-in page resolves it.
	win.signIn = false
	r = h.startScan(freshKey(), map[string]any{"source": "linkedin", "search": "engineer", "topN": 0, "limit": 1, "pages": 1, "postedWithin": "any", "force": true})
	if r.StatusCode != 202 {
		t.Fatalf("start: %d %s", r.StatusCode, r.body)
	}
	if run := h.waitRun(r.data()["runId"].(string)); run["status"] != "succeeded" {
		t.Fatalf("scan: %v", run)
	}
	b, _ := json.Marshal(h.get("/api/needs-you").data())
	if strings.Contains(string(b), "scan-sign-in-linkedin") {
		t.Fatalf("the item outlived a scan past the sign-in page: %s", b)
	}
}
