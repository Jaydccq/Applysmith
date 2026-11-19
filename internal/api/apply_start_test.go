package api

import (
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/apply"
	"applysmith/internal/gates"
	"applysmith/internal/notify"
	"applysmith/internal/tracker"
	"applysmith/internal/workspace"
)

func TestApplyStart_ADryRunNeedsNoConfirmationAndIsRecordedForTheCaller(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	r := h.post("/api/apply/runs", wire.ApplyRunRequest{URL: "https://jobs.example.test/a/1", Company: "Example Co", Role: "Engineer"})
	if r.StatusCode != 202 || !strings.HasPrefix(decode[wire.RunAccepted](t, r).RunID, "run_") {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	run := waitForApplyRun(t, h)
	if run.caller != string(gates.CallerCLI) || run.url != "https://jobs.example.test/a/1" || run.stage != "preflight" {
		t.Fatalf("run = %+v", run)
	}
}

const leverJob = "https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000001"

// leaveLinkedIn answers lnkd.in with its leave-LinkedIn page, any other host
// with a plain page.
type leaveLinkedIn struct{}

func (leaveLinkedIn) RoundTrip(r *http.Request) (*http.Response, error) {
	page := "<html></html>"
	if r.URL.Host == "lnkd.in" {
		page = `<a data-tracking-control-name="external_url_click" href="` + leverJob + `">go</a>`
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/html"}}, Request: r, Body: io.NopCloser(strings.NewReader(page))}, nil
}

func TestApplyStart_AShortenedLinkStartsOnItsDestination(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow, func(c *Config) { c.Domain.ScanHTTP = &http.Client{Transport: leaveLinkedIn{}} })
	if r := h.post("/api/apply/runs", wire.ApplyRunRequest{URL: "https://lnkd.in/abc"}); r.StatusCode != 202 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if run := waitForApplyRun(t, h); run.url != leverJob {
		t.Fatalf("run = %+v", run)
	}
}

func TestApplyStart_TheDashboardSessionIsRecordedAsTheDashboard(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	r := h.chat(h.session(), "POST", "/api/apply/runs", wire.ApplyRunRequest{URL: "https://jobs.example.test/a/2"})
	if r.StatusCode != 202 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if run := waitForApplyRun(t, h); run.caller != string(gates.CallerDashboard) {
		t.Fatalf("run = %+v", run)
	}
}

func TestApplyStart_ServerRefusesAnUnconfirmedSubmit(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	r := h.post("/api/apply/runs", wire.ApplyRunRequest{URL: "https://jobs.example.test/a/3", Submit: true})
	if r.StatusCode != 422 || r.errField() != "confirmed" {
		t.Fatalf("single: %d %s", r.StatusCode, r.body)
	}
	b := h.post("/api/apply/batches", wire.ApplyBatchRequest{Submit: true})
	if b.StatusCode != 422 || b.errField() != "confirmed" {
		t.Fatalf("batch: %d %s", b.StatusCode, b.body)
	}
	if rows := applyRunRows(t, h); len(rows) != 0 {
		t.Fatalf("a run started without the confirmation: %+v", rows)
	}
}

func TestApplyStart_ConfirmedSubmitReachesTheRunnerAsAnExplicitRequest(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	r := h.post("/api/apply/runs", wire.ApplyRunRequest{URL: "https://jobs.example.test/a/4", Submit: true, Confirmed: true})
	if r.StatusCode != 202 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	// The browser is down, so the run stops at preflight before any press.
	if run := waitForApplyRun(t, h); run.kind != "stopped" || run.stage != "preflight" {
		t.Fatalf("run = %+v", run)
	}
	if q, err := apply.LoadRequest(t.Context(), h.srv.store.Load(), decode[wire.RunAccepted](t, r).RunID); err != nil || !q.Submit || !q.Explicit {
		t.Fatalf("the runner's request: %+v %v", q, err)
	}
}

// A re-send of an unconfirmed application is the command line's --resubmit.
// The resubmit door decides the request, a dashboard session is refused before
// any run exists, and both decisions are on the audit.
func TestApplyStart_ResubmitPassesTheResubmitDoorAndADashboardSessionIsRefused(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	req := wire.ApplyRunRequest{URL: "https://jobs.example.test/a/9", Submit: true, Confirmed: true, Resubmit: true}
	r := h.chat(h.session(), "POST", "/api/apply/runs", req)
	if r.StatusCode != 403 || !strings.Contains(string(r.body), "resubmit") {
		t.Fatalf("dashboard: %d %s", r.StatusCode, r.body)
	}
	if rows := applyRunRows(t, h); len(rows) != 0 {
		t.Fatalf("a refused re-send started a run: %+v", rows)
	}
	if n := h.rowCount("gate_decisions WHERE gate = 'resubmit_door' AND verdict = 'deny' AND code = 'resubmit_caller_refused' AND caller = 'dashboard'"); n != 1 {
		t.Fatalf("%d audited refusals of the dashboard's re-send, want 1", n)
	}
	if r := h.post("/api/apply/runs", req); r.StatusCode != 202 {
		t.Fatalf("command line: %d %s", r.StatusCode, r.body)
	}
	waitForApplyRun(t, h)
	if n := h.rowCount("gate_decisions WHERE gate = 'resubmit_door' AND verdict = 'allow' AND code = 'ok' AND caller = 'cli'"); n != 1 {
		t.Fatalf("%d audited allows of the typed --resubmit, want 1", n)
	}
}

func TestApplyStart_RefusesWhatIsNotAPostingAddress(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	for _, u := range []string{"ftp://jobs.example.test/a", "https://user:pw@jobs.example.test/a"} {
		if r := h.post("/api/apply/runs", wire.ApplyRunRequest{URL: u}); r.StatusCode != 422 || r.errField() != "url" {
			t.Errorf("%.30q: %d %s", u, r.StatusCode, r.body)
		}
	}
}

// A confirmed start while the apply window is held waits its turn instead of
// being refused; a queued start the user cancels never runs.
func TestApplyStart_AStartWhileTheWindowIsHeldWaitsItsTurn(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	a := h.domainOf().apply
	a.busy.Store(true)
	urls := []string{"https://jobs.example.test/q/1", "https://jobs.example.test/q/2", "https://jobs.example.test/q/3"}
	var ids []string
	for i, u := range urls {
		r := h.post("/api/apply/runs", wire.ApplyRunRequest{URL: u})
		if got := decode[wire.RunAccepted](t, r); r.StatusCode != 202 || int(got.Ahead) != i+1 {
			t.Fatalf("start %d: %d %s", i, r.StatusCode, r.body)
		}
		ids = append(ids, decode[wire.RunAccepted](t, r).RunID)
	}
	entry := func(id string) wire.BoardEntry {
		t.Helper()
		v := decode[wire.RunView](t, h.get("/api/runs/"+id))
		if v.Entry == nil {
			t.Fatalf("run %s has no board entry", id)
		}
		return *v.Entry
	}
	if r := h.post("/api/runs/"+ids[1]+"/cancel", nil); r.StatusCode != 200 {
		t.Fatalf("cancel: %d %s", r.StatusCode, r.body)
	}
	if e := entry(ids[1]); e.State != string(notify.Done) || e.Outcome != string(notify.OutcomeCancelled) {
		t.Fatalf("the cancelled start: %+v", e)
	}
	if e := entry(ids[2]); e.Stage != wire.BoardStageQueued || !strings.Contains(e.Detail, "Queued (2 ahead)") {
		t.Fatalf("the start behind it: %+v", e)
	}
	a.busy.Store(false)
	var runs []string
	waitLong(t, "the queued starts to run", func() bool {
		runs = runs[:0]
		rows, err := h.srv.store.Load().Read().Query(`SELECT run_id, posting_url FROM apply_runs WHERE outcome_kind <> 'running' ORDER BY started_at`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id, u string
			if err := rows.Scan(&id, &u); err != nil {
				t.Fatal(err)
			}
			runs = append(runs, id+" "+u)
		}
		return len(runs) == 2
	})
	if want := []string{ids[0] + " " + urls[0], ids[2] + " " + urls[2]}; !slices.Equal(runs, want) {
		t.Fatalf("runs %v, want %v: each start runs in turn under the id it answered with", runs, want)
	}
}

func TestApplyStart_ABatchLeavesTheSkipListToTheUser(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	score := 4.4
	for i, company := range []string{"Example Labs", "Google", "Sample Works"} {
		_, err := h.upsertReport(t.Context(), tracker.Report{Number: int64(i + 1), Date: "2026-01-02", Outcome: tracker.OutcomeRecommended, Kind: tracker.ScoreFinal, Score: &score,
			Posting: postingOf(company)})
		if err != nil {
			t.Fatal(err)
		}
	}
	r := h.post("/api/apply/batches", wire.ApplyBatchRequest{Lanes: []string{wire.LaneApplyNow, wire.LaneSelective}})
	if r.StatusCode != 202 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	got := decode[wire.ApplyBatchAccepted](t, r)
	if got.Rows != 2 || len(got.Skipped) != 1 || got.Skipped[0].Company != "Google" || !strings.Contains(got.Skipped[0].Reason, `on your batch skip list (as "Alphabet")`) {
		t.Fatalf("batch = %+v", got)
	}
	if run := waitForApplyRun(t, h); run.caller != string(gates.CallerCLI) {
		t.Fatalf("run = %+v", run)
	}
}

// Adding or replacing the transcript counts as saving the user's data, so a
// list runs again a row that stopped today for want of one.
func TestApplyStart_ATranscriptAddedCountsAsSavedData(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	dir := h.srv.cfg.Workspace.Path(workspace.TranscriptDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(time.Hour).Truncate(time.Second)
	if err := os.Chtimes(dir, at, at); err != nil {
		t.Fatal(err)
	}
	if got, err := h.srv.listEvidence().DataSavedAt(t.Context()); err != nil || !got.Equal(at) {
		t.Fatalf("data saved at %v, %v; want %v", got, err, at)
	}
}

func TestApplyStart_ABatchOverLanesItCannotRunIsRefused(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	for _, body := range []wire.ApplyBatchRequest{{Lanes: []string{wire.LaneMarkedApplied}}, {Limit: 101}} {
		if r := h.post("/api/apply/batches", body); r.StatusCode != 422 {
			t.Errorf("%+v: %d %s", body, r.StatusCode, r.body)
		}
	}
	if r := h.post("/api/apply/batches", wire.ApplyBatchRequest{}); r.StatusCode != 422 || r.errReason() != "nothing-to-run" {
		t.Errorf("an empty lane: %d %s", r.StatusCode, r.body)
	}
}

// The rows the user ticked run in the order they were ticked, and none other;
// a ticked row that is no longer in Apply now or Selective is reported, not run.
// A skipped or discarded row is one: only a single explicit start applies to it.
func TestApplyStart_ABatchOfChosenRowsRunsThoseRowsOnly(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	tr := h.domainOf().tracker
	var ids []wire.SafeInt
	for i, c := range []struct {
		company string
		score   float64
	}{{"Alpha Labs", 4.6}, {"Bravo Labs", 3.8}, {"Charlie Labs", 4.4}, {"Echo Labs", 3.0}, {"Foxtrot Labs", 4.8}, {"Golf Labs", 4.7}} {
		res, err := h.upsertReport(t.Context(), tracker.Report{Number: int64(i + 1), Date: "2026-01-02", Outcome: tracker.OutcomeRecommended, Kind: tracker.ScoreFinal,
			Score: &c.score, Posting: postingOf(c.company)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, apikit.Safe64(res.Entry.ID))
	}
	for i, to := range map[int]tracker.Status{4: tracker.Skipped, 5: tracker.Discarded} {
		if _, err := tr.SetStatus(t.Context(), tracker.StatusChange{ID: int64(ids[i]), To: to, Actor: tracker.ActorUser}); err != nil {
			t.Fatal(err)
		}
	}
	r := h.post("/api/apply/batches", wire.ApplyBatchRequest{Candidacies: []wire.SafeInt{ids[1], ids[3], ids[4], ids[0], ids[5]}})
	if r.StatusCode != 202 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	got := decode[wire.ApplyBatchAccepted](t, r)
	if got.Rows != 2 || len(got.Skipped) != 3 || got.Skipped[0].Company != "Echo Labs" || got.Skipped[1].Company != "Foxtrot Labs" || got.Skipped[2].Company != "Golf Labs" {
		t.Fatalf("batch = %+v", got)
	}
	waitLong(t, "the list to end", func() bool { return !h.domainOf().apply.busy.Load() })
	if v := decode[wire.ApplyBatchView](t, h.get("/api/apply/batches/"+got.BatchID)); len(v.Rows) != 2 || v.Rows[0].Company != "Bravo Labs" || v.Rows[1].Company != "Alpha Labs" {
		t.Fatalf("rows = %+v", v.Rows)
	}
}

func (r result) errField() string {
	e, _ := r.json()["error"].(map[string]any)
	d, _ := e["details"].(map[string]any)
	s, _ := d["field"].(string)
	return s
}

// A run id the server hands out can be read at once: the command polls it
// right after the start answers.
func TestApplyStart_ARunIdIsReadableTheMomentItIsHandedOut(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	score := 4.4
	if _, err := h.upsertReport(t.Context(), tracker.Report{Number: 1, Date: "2026-01-02", Outcome: tracker.OutcomeRecommended, Kind: tracker.ScoreFinal,
		Score: &score, Posting: postingOf("Example Labs")}); err != nil {
		t.Fatal(err)
	}
	single := decode[wire.RunAccepted](t, h.post("/api/apply/runs", wire.ApplyRunRequest{URL: "https://jobs.example.test/a/6"}))
	if r := h.get("/api/apply/runs/" + single.RunID); r.StatusCode != 200 {
		t.Fatalf("run %s: %d %s", single.RunID, r.StatusCode, r.body)
	}
	waitForApplyRun(t, h)
	waitLong(t, "the single run to release the window", func() bool { return !h.domainOf().apply.busy.Load() })
	acc := decode[wire.ApplyBatchAccepted](t, h.post("/api/apply/batches", wire.ApplyBatchRequest{Lanes: []string{wire.LaneApplyNow}}))
	if acc.Ahead != 0 {
		t.Fatalf("the batch waited behind nothing: %+v", acc)
	}
	if r := h.get("/api/apply/runs/" + acc.RunID); r.StatusCode != 200 {
		t.Errorf("batch run %s: %d %s", acc.RunID, r.StatusCode, r.body)
	}
	if r := h.get("/api/apply/batches/" + acc.BatchID); r.StatusCode != 200 {
		t.Errorf("batch %s: %d %s", acc.BatchID, r.StatusCode, r.body)
	}
}
