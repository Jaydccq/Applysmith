package api

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"applysmith/internal/discovery"
	"applysmith/internal/llm"
	"applysmith/internal/signals"
)

// seedSignal stores a company with one held and one ok assessment.
func seedSignal(t *testing.T, h *harness) (company int64, review int64) {
	t.Helper()
	svc := h.domainOf().signals
	repo := svc.Repo()
	ctx := t.Context()
	c, err := repo.UpsertCompany(ctx, "Example Corp", "example")
	if err != nil {
		t.Fatal(err)
	}
	at := h.clock.now().Add(-24 * time.Hour)
	doc, _, err := repo.InsertDocument(ctx, signals.Doc{SourceID: "signals-alerts", Adapter: "alerts", URL: "https://techcrunch.com/x", FetchedAt: at, PublishedAt: at,
		Title: "Example Corp raises", Body: "Example Corp raised $14M and will hire 20 engineers."})
	if err != nil {
		t.Fatal(err)
	}
	ev, _, err := repo.AttachEvent(ctx, doc, c.ID, signals.TypeFunding, at, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := signals.Assessment{EventID: ev, Rubric: signals.RubricVersion, Status: signals.StatusMidConfidence, Type: signals.TypeFunding, Intent: signals.IntentStrong,
		Teams: []string{"engineering"}, Scale: signals.ScaleHeadcount, Strength: 9, Confidence: 0.6, Reasoning: "r", Quotes: []signals.Quote{{DocumentID: doc, Quote: "will hire 20 engineers"}}}
	if err := repo.StoreAssessment(ctx, a, "", "mid_confidence", false); err != nil {
		t.Fatal(err)
	}
	items, err := svc.Review(ctx, false)
	if err != nil || len(items) != 1 {
		t.Fatalf("review = %v %v", items, err)
	}
	return c.ID, items[0].ID
}

func TestSignalsAPI_ListDetailReviewAndActions(t *testing.T) {
	h := newDomainHarness(t)
	company, review := seedSignal(t, h)
	if r := h.get("/api/signals"); r.StatusCode != 200 || len(r.data()["rows"].([]any)) != 0 {
		t.Fatalf("a held assessment is listed: %d %s", r.StatusCode, r.body)
	}
	q := h.get("/api/signals/review")
	items := q.data()["items"].([]any)
	if q.StatusCode != 200 || len(items) != 1 || items[0].(map[string]any)["reason"] != "mid_confidence" {
		t.Fatalf("review: %d %s", q.StatusCode, q.body)
	}
	// A rejection is undone and the item can be decided again (flow round 12, U-1).
	for _, step := range [][2]string{{"reject", "rejected"}, {"restore", "open"}} {
		if r := h.post("/api/signals/review/"+formatInt(review)+"/"+step[0], nil); r.StatusCode != 200 || r.data()["state"] != step[1] {
			t.Fatalf("%s: %d %s", step[0], r.StatusCode, r.body)
		}
	}
	ap := h.post("/api/signals/review/"+formatInt(review)+"/approve", nil)
	if ap.StatusCode != 200 || ap.data()["state"] != "approved" {
		t.Fatalf("approve: %d %s", ap.StatusCode, ap.body)
	}
	if again := h.post("/api/signals/review/"+formatInt(review)+"/reject", nil); again.StatusCode != 409 {
		t.Fatalf("reject after the approval: %d %s", again.StatusCode, again.body)
	}
	list := h.get("/api/signals?tier=hot")
	rows := list.data()["rows"].([]any)
	if list.StatusCode != 200 || len(rows) != 1 || rows[0].(map[string]any)["company"] != "Example Corp" {
		t.Fatalf("list: %d %s", list.StatusCode, list.body)
	}
	if r := h.get("/api/signals?from=nope"); r.StatusCode != 422 {
		t.Fatalf("bad date: %d", r.StatusCode)
	}
	d := h.get("/api/signals/" + formatInt(company))
	if d.StatusCode != 200 {
		t.Fatalf("detail: %d %s", d.StatusCode, d.body)
	}
	data := d.data()
	if len(data["events"].([]any)) != 1 || len(data["factors"].([]any)) != 1 || len(data["searchLinks"].([]any)) != 2 {
		t.Fatalf("detail = %s", d.body)
	}
	ev := data["events"].([]any)[0].(map[string]any)
	if len(ev["documents"].([]any)) != 1 || ev["assessment"].(map[string]any)["status"] != "ok" {
		t.Fatalf("chain = %s", d.body)
	}
	if r := h.get("/api/signals/999"); r.StatusCode != 404 {
		t.Fatalf("missing company: %d", r.StatusCode)
	}
	for action, state := range map[string]string{"watch": "watching", "dismiss": "dismissed"} {
		r := h.post("/api/signals/"+formatInt(company)+"/"+action, nil)
		if r.StatusCode != 200 || r.data()["state"] != state {
			t.Fatalf("%s: %d %s", action, r.StatusCode, r.body)
		}
	}
	st := h.get("/api/signals/status")
	if st.StatusCode != 200 || st.data()["tiers"] == nil {
		t.Fatalf("status: %d %s", st.StatusCode, st.body)
	}
	for _, l := range st.data()["lanes"].([]any) {
		lane := l.(map[string]any)
		if title, _ := lane["title"].(string); title == "" || title == lane["source"] {
			t.Errorf("lane %v has no display name: %v", lane["source"], lane)
		}
		// Only the SEC source sends something of the user's: the dashboard
		// shows this before it switches the source on.
		want := ""
		if lane["source"] == signals.SourceEdgar {
			want = signals.EdgarDisclosure
		}
		if got, _ := lane["disclosure"].(string); got != want {
			t.Errorf("lane %v disclosure = %q, want %q", lane["source"], got, want)
		}
	}
}

func TestSignalsAPI_TickIsALongOperationWithOneActiveInstance(t *testing.T) {
	h := newDomainHarness(t)
	r := h.send("POST", "/api/signals/ticks", map[string]any{"sources": []string{"signals-x"}}, "Idempotency-Key", freshKey())
	if r.StatusCode != 202 || r.data()["runId"] == "" {
		t.Fatalf("tick: %d %s", r.StatusCode, r.body)
	}
	run := h.waitRun(r.data()["runId"].(string))
	if run["status"] != "succeeded" {
		t.Fatalf("run = %v", run)
	}
	st := h.get("/api/signals/status")
	last, _ := st.data()["lastTick"].(map[string]any)
	if last == nil || last["status"] != "completed" || last["limited"] != true {
		t.Fatalf("status = %s", st.body)
	}
	if bad := h.send("POST", "/api/signals/ticks", map[string]any{"sources": []string{"signals-rss"}}, "Idempotency-Key", freshKey()); bad.StatusCode != 422 {
		t.Fatalf("unknown source: %d %s", bad.StatusCode, bad.body)
	}
	if noKey := h.send("POST", "/api/signals/ticks", map[string]any{}); noKey.StatusCode != 400 {
		t.Fatalf("no key: %d", noKey.StatusCode)
	}
	// A tick in progress refuses a second start and names itself.
	db, _ := h.srv.db()
	if err := db.Write(t.Context(), func(tx *sqlTx) error {
		_, err := tx.Exec(`INSERT INTO signal_ticks (run_id, status, started_at, heartbeat_at) VALUES ('', 'running', ?, ?)`, h.clock.now().UTC().Format(time.RFC3339), h.clock.now().UTC().Format(time.RFC3339))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	second := h.send("POST", "/api/signals/ticks", map[string]any{}, "Idempotency-Key", freshKey())
	if second.StatusCode != 409 || !strings.Contains(string(second.body), "already running") {
		t.Fatalf("second start: %d %s", second.StatusCode, second.body)
	}
}

type sqlTx = sql.Tx

func formatInt(n int64) string { return strconv.FormatInt(n, 10) }

func TestSignalsAPI_RescoreCountsCompaniesPerTierWithoutACall(t *testing.T) {
	h := newDomainHarness(t)
	_, review := seedSignal(t, h)
	if r := h.post("/api/signals/review/"+formatInt(review)+"/approve", nil); r.StatusCode != 200 {
		t.Fatalf("approve: %d %s", r.StatusCode, r.body)
	}
	r := h.post("/api/signals/rescore", nil)
	tiers, _ := r.data()["tiers"].(map[string]any)
	if r.StatusCode != 200 || r.data()["companies"].(float64) != 1 || tiers["hot"] != float64(1) {
		t.Fatalf("rescore: %d %s", r.StatusCode, r.body)
	}
}

func TestSignals_IngestionRetryThroughRoutesProducesOneReport(t *testing.T) {
	base := exitWeb(t)
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/alerts/feeds/1/2" {
			base.Config.Handler.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("If-None-Match") == `"owned-feed"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"owned-feed"`)
		fmt.Fprint(w, exitFeed)
	}))
	t.Cleanup(web.Close)
	target, _ := url.Parse(web.URL)
	fake := llm.NewFake()
	fake.SetGenerator(exitBrain)
	h := newDomainHarness(t, func(c *Config) {
		c.Domain.LLM = append(c.Domain.LLM, llm.WithFake(fake))
		c.Domain.Reader = immediateReader{text: postingBody("signal retry")}
		c.Domain.SignalEndpoints = signals.Endpoints{ATS: discovery.Endpoints{discovery.Greenhouse: web.URL}}
		c.Domain.ScanHTTP = &http.Client{Transport: toServer{target}}
	})
	if err := writeFile(h.ws.Root+"/profile.yml", exitProfile); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(h.ws.Root+"/portals.yml", exitPortals+"  signals-hn-hiring:\n    enabled: false\n"); err != nil {
		t.Fatal(err)
	}
	d := h.domainOf()
	st := h.srv.store.Load()
	if err := st.Write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec("CREATE TRIGGER owned_signal_refusal BEFORE INSERT ON signal_documents BEGIN SELECT RAISE(FAIL, 'owned signal ingestion refusal'); END")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	path := "/api/signals/ticks"
	body := map[string]any{}
	run := func() map[string]any {
		h.clock.advance(time.Second)
		d.sched.Wait()
		r := h.send("POST", path, body, "Idempotency-Key", freshKey())
		if r.StatusCode != 202 {
			t.Fatalf("start=%d %s", r.StatusCode, r.body)
		}
		return h.waitRun(r.data()["runId"].(string))
	}
	// Collector isolation allows the tick to finish; its source result
	// and summary must still disclose the refused ingest.
	first := run()
	if first["status"] != "succeeded" {
		t.Fatalf("isolated collector failure=%v", first)
	}
	status := h.get("/api/signals/status").data()["lastTick"].(map[string]any)
	found := false
	for _, raw := range status["sources"].([]any) {
		src := raw.(map[string]any)
		if src["id"] == signals.SourceAlerts {
			found = src["status"] == "failed" && strings.Contains(fmt.Sprint(src["reason"]), "owned signal ingestion refusal")
		}
	}
	if !found {
		t.Errorf("failed collector hidden=%v", status)
	}
	if got := h.get("/api/candidacies").data()["entries"].([]any); len(got) != 0 {
		t.Fatalf("refused ingest produced report=%v", got)
	}
	if err := st.Write(t.Context(), func(tx *sql.Tx) error { _, err := tx.Exec("DROP TRIGGER owned_signal_refusal"); return err }); err != nil {
		t.Fatal(err)
	}
	if next := run(); next["status"] != "succeeded" {
		t.Fatalf("manual recovery=%v", next)
	}
	var companyID int64
	if err := st.Read().QueryRow("SELECT id FROM signal_companies WHERE canon = 'example'").Scan(&companyID); err != nil {
		t.Fatal(err)
	}
	promoted := h.post("/api/signals/"+formatInt(companyID)+"/evaluate-postings", nil)
	if promoted.StatusCode != 200 || len(promoted.data()["queued"].([]any)) != 1 {
		t.Fatalf("promotion=%d %s", promoted.StatusCode, promoted.body)
	}
	if err := d.pool.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	entries := h.get("/api/candidacies").data()["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("tracker=%v", entries)
	}
	report := int(entries[0].(map[string]any)["reportNumber"].(float64))
	if r := h.get("/api/reports/" + itoa(report)); r.StatusCode != 200 {
		t.Fatalf("report=%d %s", r.StatusCode, r.body)
	}
	if again := run(); again["status"] != "succeeded" {
		t.Fatalf("repeat=%v", again)
	}
	if r := h.post("/api/signals/"+formatInt(companyID)+"/evaluate-postings", nil); r.StatusCode != 200 || len(r.data()["queued"].([]any)) != 0 {
		t.Fatalf("duplicate promotion=%d %s", r.StatusCode, r.body)
	}
	var docs, leads, claims int
	if err := st.Read().QueryRow("SELECT (SELECT count(*) FROM signal_documents), (SELECT count(*) FROM leads), (SELECT count(*) FROM submissions)").Scan(&docs, &leads, &claims); err != nil || docs != 1 || leads != 1 || claims != 0 || len(h.get("/api/candidacies").data()["entries"].([]any)) != 1 {
		t.Fatalf("repeat docs=%d leads=%d claims=%d err=%v", docs, leads, claims, err)
	}
}

func TestSignals_ManualTickCancellationUsesRunsOwner(t *testing.T) {
	entered, release := make(chan struct{}, 1), make(chan struct{})
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
			fmt.Fprint(w, exitFeed)
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(web.Close)
	target, _ := url.Parse(web.URL)
	h := newDomainHarness(t, func(c *Config) { c.Domain.ScanHTTP = &http.Client{Transport: toServer{target}} })
	t.Cleanup(func() { close(release) })
	if err := writeFile(h.ws.Root+"/portals.yml", exitPortals); err != nil {
		t.Fatal(err)
	}
	r := h.send("POST", "/api/signals/ticks", map[string]any{"sources": []string{signals.SourceAlerts}}, "Idempotency-Key", freshKey())
	if r.StatusCode != 202 {
		t.Fatalf("start=%d %s", r.StatusCode, r.body)
	}
	id := r.data()["runId"].(string)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("collector did not start")
	}
	cancelled := h.post("/api/runs/"+id+"/cancel", nil)
	if cancelled.StatusCode != 202 {
		t.Fatalf("cancel=%d %s", cancelled.StatusCode, cancelled.body)
	}
	if run := h.waitRun(id); run["status"] != "cancelled" {
		t.Fatalf("cancel outcome=%v", run)
	}
}
