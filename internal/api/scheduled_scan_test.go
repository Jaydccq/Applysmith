package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"applysmith/internal/api/wire"
	"applysmith/internal/discovery"
)

// gatedBoard serves the example board once release is closed; a request that
// arrives earlier waits for it or for its own cancellation.
func gatedBoard(t *testing.T) (h *harness, release chan struct{}, entered chan struct{}) {
	t.Helper()
	release, entered = make(chan struct{}), make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/boards/example-co/jobs" {
			http.NotFound(w, r)
			return
		}
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jobs": []map[string]any{{
			"id": 41, "title": "Software Engineer", "location": map[string]any{"name": "Remote - United States"}, "company_name": "Example Co",
			"absolute_url": "https://job-boards.greenhouse.io/example-co/jobs/41", "updated_at": "2026-01-01T00:00:00Z",
		}}, "meta": map[string]any{"total": 1}})
	}))
	t.Cleanup(srv.Close)
	h = newDomainHarness(t, func(c *Config) { c.Domain.ScanEndpoints = discovery.Endpoints{discovery.Greenhouse: srv.URL} })
	if _, err := h.domainOf().repo.RegistryAdd(t.Context(), "test", discovery.Board{ATS: discovery.Greenhouse, Slug: "example-co", Company: "Example Co"}); err != nil {
		t.Fatal(err)
	}
	return h, release, entered
}

func runScheduledRegistryScan(t *testing.T, h *harness) string {
	t.Helper()
	made := h.post("/api/schedules", map[string]any{"name": "registry", "kind": "discover.registry", "preset": map[string]any{"kind": "daily", "time": "23:30", "timezone": "UTC"}})
	if made.StatusCode != 201 && made.StatusCode != 200 {
		t.Fatalf("schedule: %d %s", made.StatusCode, made.body)
	}
	r := h.post("/api/schedules/"+itoa(int(made.data()["id"].(float64)))+"/run", nil)
	id, _ := r.data()["runId"].(string)
	if r.StatusCode != 202 || id == "" {
		t.Fatalf("run: %d %s", r.StatusCode, r.body)
	}
	return id
}

func scanOfRun(t *testing.T, h *harness, runID string) *wire.ScanRecord {
	t.Helper()
	var list wire.Success[wire.ScanList]
	strictInto(t, h.get("/api/scans?status=running").body, &list)
	for _, rec := range list.Data.Scans {
		if rec.RunID == runID {
			return &rec
		}
	}
	return nil
}

func TestScheduledScan_HasARunTheMonitorCanFollowAndEnd(t *testing.T) {
	h, release, entered := gatedBoard(t)
	id := runScheduledRegistryScan(t, h)
	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the scan never reached the board")
	}
	rec := scanOfRun(t, h, id)
	if rec == nil || rec.Source != "registry" {
		t.Fatalf("the running scan does not carry the run id %s", id)
	}
	s := h.openRunStream(id)
	if m := s.next(); m.name != "state" {
		t.Fatalf("first event %+v", m)
	}
	close(release)
	sawProgress := false
	for {
		m := s.next()
		if m.name == "progress" {
			sawProgress = true
			continue
		}
		if m.name == "final" {
			if f := decodeEvent[wire.RunFinal](t, m); f.Status != wire.RunSucceeded || f.RunID != id {
				t.Errorf("final %+v", f)
			}
			break
		}
	}
	if !sawProgress {
		t.Error("the stream carried no log line of the scan")
	}
	if run := h.waitRun(id); run["status"] != "succeeded" {
		t.Errorf("run %v", run)
	}
}

// A scan schedule the user made has no inbox evaluation after it, so a row
// left waiting by earlier scans goes to evaluation through the inbox.evaluate
// run that follows the scan, recorded on the scan's schedule (F-1).
func TestScheduledScan_AHandMadeScanIsFollowedByTheInboxEvaluation(t *testing.T) {
	h, release, _ := gatedBoard(t)
	close(release)
	d := h.domainOf()
	add, err := d.repo.InboxAddURL(t.Context(), "https://jobs.example.com/p/waiting-1")
	if err != nil || add.Row == nil {
		t.Fatalf("add: %+v %v", add, err)
	}
	scan := runScheduledRegistryScan(t, h)
	if run := h.waitRun(scan); run["status"] != "succeeded" {
		t.Fatalf("scan %v", run)
	}
	var drain string
	waitFor(t, "the inbox evaluation after the scan", func() bool {
		return h.srv.store.Load().Read().QueryRow(`SELECT r.id FROM runs r JOIN runs s ON s.schedule_id = r.schedule_id WHERE s.id = ? AND r.kind = 'inbox.evaluate'`, scan).Scan(&drain) == nil
	})
	if run := h.waitRun(drain); run["status"] != "succeeded" {
		t.Fatalf("inbox evaluation %v", run)
	}
	if err := d.pool.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if row, err := d.repo.InboxGet(t.Context(), add.Row.ID); err != nil || row.Status != discovery.InboxEvaluated {
		t.Fatalf("the waiting row: %+v %v", row, err)
	}
}

func TestScheduledScan_CancelFromTheRunsRouteStopsItAndEndsTheStream(t *testing.T) {
	h, _, entered := gatedBoard(t)
	id := runScheduledRegistryScan(t, h)
	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the scan never reached the board")
	}
	s := h.openRunStream(id)
	s.next()
	r := h.post("/api/runs/"+id+"/cancel", nil)
	if r.StatusCode != 202 {
		t.Fatalf("cancel: %d %s", r.StatusCode, r.body)
	}
	for {
		m := s.next()
		if m.name == "final" {
			if f := decodeEvent[wire.RunFinal](t, m); f.Status != wire.RunCancelled {
				t.Errorf("final %+v", f)
			}
			break
		}
	}
	if run := h.waitRun(id); run["status"] != "cancelled" {
		t.Errorf("run %v", run)
	}
	if r := h.post("/api/runs/"+id+"/cancel", nil); r.StatusCode != 409 || !strings.Contains(string(r.body), "ended") {
		t.Errorf("cancelling twice: %d %s", r.StatusCode, r.body)
	}
	// The schedule's history records the user's cancel as its reason; the
	// schedule's last result stays within its closed set (failed).
	sched := h.get("/api/schedules").data()["schedules"].([]any)[0].(map[string]any)
	if sched["lastResult"] != "failed" || sched["activeRunId"] != nil && sched["activeRunId"] != "" {
		t.Errorf("a cancelled run leaves the schedule failed and idle: %v", sched)
	}
	found := false
	for _, row := range h.get("/api/schedules/" + itoa(int(sched["id"].(float64))) + "/history").data()["history"].([]any) {
		row := row.(map[string]any)
		if row["runId"] == id {
			found = true
			if row["reason"] != "cancelled" {
				t.Errorf("history row %v; want reason cancelled", row)
			}
		}
	}
	if !found {
		t.Errorf("no history row for run %s", id)
	}
}
