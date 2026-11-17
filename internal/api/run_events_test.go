package api

import (
	"encoding/json"
	"testing"
	"time"

	"applysmith/internal/api/wire"
	"applysmith/internal/store"
)

// openRunStream opens GET /api/runs/{id}/events.
func (h *harness) openRunStream(id string) *streamClient {
	return h.openStream("/api/runs/"+id+"/events", h.token(), "")
}

func decodeEvent[T any](t *testing.T, m sseMsg) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(m.data), &v); err != nil {
		t.Fatalf("%s event %q: %v", m.name, m.data, err)
	}
	return v
}

// Progress is observable: each phase change in order, then one final event
// with the outcome, after which the stream ends.
func TestRunStreamReportsEachPhaseThenTheOutcome(t *testing.T) {
	gate := newGateReader(postingBody("stream"))
	h := newDomainHarness(t, withReader(gate))
	r := h.startEvaluation(freshKey(), map[string]any{"url": "https://jobs.example.com/p/stream"})
	id := r.data()["runId"].(string)
	s := h.openRunStream(id)
	state := s.next()
	if state.name != "state" {
		t.Fatalf("first event %+v", state)
	}
	if view := decodeEvent[wire.RunView](t, state); view.RunID != id || view.Kind != "evaluate" || view.Status == wire.RunSucceeded {
		t.Fatalf("state %+v", view)
	}
	close(gate.release)
	var phases []string
	var final wire.RunFinal
	for {
		m := s.next()
		if m.name == "phase" {
			phases = append(phases, decodeEvent[wire.RunPhase](t, m).Phase)
			continue
		}
		if m.name == "final" {
			final = decodeEvent[wire.RunFinal](t, m)
			break
		}
		if m.comment == "" {
			t.Fatalf("unexpected event %+v", m)
		}
	}
	// The stream may have joined while the job was reading or after it moved on.
	want := []string{"reading", "gating", "evaluating", "writing"}
	for _, p := range phases {
		found := false
		for _, w := range want {
			found = found || w == p
		}
		if !found {
			t.Errorf("unexpected phase %q in %v", p, phases)
		}
	}
	if len(phases) == 0 || phases[len(phases)-1] != "writing" {
		t.Errorf("phases = %v, want to end with writing", phases)
	}
	if final.RunID != id || final.Outcome != "reported" || final.Status != wire.RunSucceeded || final.ReportNumber == nil {
		t.Errorf("final = %+v", final)
	}
	s.expectClosed()
}

// A subscriber that arrives after the run ended gets its state and final
// event, and the stream closes.
func TestRunStreamOfAFinishedRunDeliversTheFinalEventAndCloses(t *testing.T) {
	h := newDomainHarness(t)
	out := h.evaluate("finished")
	id := out["runId"].(string)
	// The live stream is gone after a restart; the stored state must still answer.
	h.restart()
	s := h.openRunStream(id)
	if m := s.next(); m.name != "state" {
		t.Fatalf("first event %+v", m)
	}
	m := s.next()
	if m.name != "final" {
		t.Fatalf("second event %+v", m)
	}
	if f := decodeEvent[wire.RunFinal](t, m); f.Outcome != "reported" || f.ReportNumber == nil || f.Status != wire.RunSucceeded {
		t.Errorf("final = %+v", f)
	}
	s.expectClosed()
}

func TestRunStreamNeedsAKnownRunAndACredential(t *testing.T) {
	h := newDomainHarness(t)
	if r := h.do(call{path: "/api/runs/run_missing/events", useToken: true}); r.StatusCode != 404 || r.errCode() != "not_found" {
		t.Errorf("unknown run: %d %s", r.StatusCode, r.body)
	}
	if r := h.do(call{path: "/api/runs/run_missing/events"}); r.StatusCode != 401 {
		t.Errorf("no credential: %d", r.StatusCode)
	}
}

func TestCancelStopsARunningEvaluationWithoutAReport(t *testing.T) {
	gate := newGateReader(postingBody("cancel"))
	h := newDomainHarness(t, withReader(gate))
	r := h.startEvaluation(freshKey(), map[string]any{"url": "https://jobs.example.com/p/cancel"})
	id := r.data()["runId"].(string)
	s := h.openRunStream(id)
	s.next() // state
	c := h.post("/api/runs/"+id+"/cancel", nil)
	if c.StatusCode != 202 || c.Header.Get("Location") != "/api/runs/"+id {
		t.Fatalf("cancel: %d %s", c.StatusCode, c.body)
	}
	var final wire.RunFinal
	for {
		m := s.next()
		if m.name == "final" {
			final = decodeEvent[wire.RunFinal](t, m)
			break
		}
	}
	if final.Status != wire.RunCancelled || final.Outcome != "failed" || final.Reason != "cancelled" || final.ReportNumber != nil {
		t.Errorf("final = %+v", final)
	}
	var reports int
	if err := h.srv.store.Load().Read().QueryRow(`SELECT count(*) FROM report_index`).Scan(&reports); err != nil || reports != 0 {
		t.Errorf("%d reports after a cancel, %v", reports, err)
	}
	// An ended run cannot be cancelled again.
	again := h.post("/api/runs/"+id+"/cancel", nil)
	if again.StatusCode != 409 || again.errCode() != "conflict" {
		t.Errorf("second cancel: %d %s", again.StatusCode, again.body)
	}
	if u := h.post("/api/runs/run_missing/cancel", nil); u.StatusCode != 404 {
		t.Errorf("unknown run: %d", u.StatusCode)
	}
}

func TestCancelOfAQueuedEvaluationNeverStartsIt(t *testing.T) {
	gate := newGateReader(postingBody("queued"))
	h := newDomainHarness(t, withReader(gate), func(c *Config) { c.Domain.EvalQueueMax = 50 })
	t.Cleanup(func() { close(gate.release) })
	// Fill the pool: the offline provider runs eight jobs at once.
	var ids []string
	for i := range 9 {
		r := h.startEvaluation(freshKey(), map[string]any{"url": "https://jobs.example.com/p/fill" + itoa(i)})
		ids = append(ids, r.data()["runId"].(string))
	}
	deadline := time.Now().Add(5 * time.Second)
	for h.domainOf().pool.Stats().Queued != 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	queued := ids[8]
	if c := h.post("/api/runs/"+queued+"/cancel", nil); c.StatusCode != 202 {
		t.Fatalf("cancel queued: %d %s", c.StatusCode, c.body)
	}
	got := h.get("/api/evaluations/" + queued).data()
	if got["status"] != wire.RunCancelled || got["outcome"] != "failed" {
		t.Errorf("queued job after cancel: %v", got)
	}
}

// Subscribing to runs that already ended leaves no stream behind.
func TestRunStreamOfFinishedRunsIsNotKept(t *testing.T) {
	h := newBareHarness(t)
	st := h.srv.store.Load()
	for i := range 3 {
		ref, err := st.CreateRun(t.Context(), store.RunRequest{Principal: "p", Operation: "x", Key: "stream-key-" + itoa(i), Kind: "scan", Input: []byte("{}")})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetRun(t.Context(), ref.RunID, "succeeded", "", ""); err != nil {
			t.Fatal(err)
		}
		if r := h.do(call{path: "/api/runs/" + ref.RunID + "/events", useToken: true}); r.StatusCode != 200 {
			t.Fatalf("status %d %s", r.StatusCode, r.body)
		}
	}
	h.srv.runs.mu.Lock()
	n := len(h.srv.runs.items)
	h.srv.runs.mu.Unlock()
	if n != 0 {
		t.Errorf("%d run streams kept for finished runs", n)
	}
}

// A client that lost its connection while the run was ending resumes with the
// id of the last event it read: the kept stream knows that id, so the client
// gets the state and the final event instead of a reset.
func TestRunStreamResumedAfterTheRunEndedDeliversTheFinalEvent(t *testing.T) {
	h := newBareHarness(t)
	st := h.srv.store.Load()
	ref, err := st.CreateRun(t.Context(), store.RunRequest{Principal: "p", Operation: "x", Key: "resume-key", Kind: "scan", Input: []byte("{}")})
	if err != nil {
		t.Fatal(err)
	}
	h.srv.runs.publish(ref.RunID, "progress", wire.RunProgress{Line: "one"})
	h.srv.runs.publish(ref.RunID, "progress", wire.RunProgress{Line: "two"})
	if err := st.SetRun(t.Context(), ref.RunID, wire.RunSucceeded, "", ""); err != nil {
		t.Fatal(err)
	}
	h.srv.runs.publish(ref.RunID, "final", wire.RunFinal{RunID: ref.RunID, Status: wire.RunSucceeded})
	h.srv.runs.retire(ref.RunID)
	s := h.openStream("/api/runs/"+ref.RunID+"/events", h.token(), "1")
	if m := s.next(); m.name != "state" {
		t.Fatalf("first event %+v, want the stored state", m)
	}
	m := s.next()
	if m.name != "final" {
		t.Fatalf("second event %+v, want the final event", m)
	}
	if f := decodeEvent[wire.RunFinal](t, m); f.RunID != ref.RunID || f.Status != wire.RunSucceeded {
		t.Errorf("final = %+v", f)
	}
	s.expectClosed()
}

// An event stream is opened by GET only.
func TestHeadOnAnEventRouteIsRefusedAndOpensNoStream(t *testing.T) {
	h := newBareHarness(t)
	for _, path := range []string{"/api/board/events", "/api/runs/run_x/events"} {
		r := h.do(call{method: "HEAD", path: path, useToken: true})
		if r.StatusCode != 405 {
			t.Errorf("HEAD %s: %d", path, r.StatusCode)
		}
	}
	h.srv.streams.mu.Lock()
	n := len(h.srv.streams.conns)
	h.srv.streams.mu.Unlock()
	if n != 0 {
		t.Errorf("%d streams open after HEAD", n)
	}
}
