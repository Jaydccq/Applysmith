package api

import (
	"strings"
	"testing"

	"applysmith/internal/api/wire"
)

const systemWritesPath = "/api/answer-bank/system-writes"

func (h *harness) fillGroundTruth(label, run, selector, question, value string) bool {
	h.t.Helper()
	ev := learnEvent(label, "manual-fill", map[string]any{"question": question, "after": value, "runId": run, "selector": selector})
	if r := h.postEvents(ev); r.StatusCode != 200 || r.data()["accepted"] != float64(1) {
		h.t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	for _, f := range h.get("/api/answer-bank/manual-fills").data()["fills"].([]any) {
		if m := f.(map[string]any); m["question"] == question {
			return m["groundTruth"].(bool)
		}
	}
	h.t.Fatalf("no fill for %q", question)
	return false
}

// The client's own profile pass never passes through the match route, so it
// reports what it wrote here; an equal manual fill in that run is then not
// ground truth, whichever selector it names.
func TestSystemWritesRouteRemembersAValueAndDemotesItsEcho(t *testing.T) {
	h := newDomainHarness(t)
	run := h.issueRun()
	r := h.post(systemWritesPath, wire.SystemWriteRequest{RunID: run, Selector: "#city", Value: "Springfield"})
	if r.StatusCode != 200 || r.data()["recorded"] != true {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if h.fillGroundTruth("echo-same", run, "#city", "Which city", "Springfield") ||
		h.fillGroundTruth("echo-other", run, "#other", "Which town", "springfield") ||
		h.fillGroundTruth("echo-none", run, "", "Where do you live", "Springfield") {
		t.Fatal("the system's own value is not the user's")
	}
	if !h.fillGroundTruth("own", run, "#city", "Which city is it", "Shelbyville") {
		t.Fatal("another value is the user's")
	}
	if h.rowCount("system_writes") != 1 {
		t.Fatal("one digest for one value")
	}
}

func TestSystemWritesRouteRefusals(t *testing.T) {
	h := newDomainHarness(t)
	run := h.issueRun()
	for _, c := range []struct {
		name, field string
		req         wire.SystemWriteRequest
	}{
		{"a well-formed run id the server never issued", "runId", wire.SystemWriteRequest{RunID: "run_0123456789abcdef01234567", Selector: "#a", Value: "b"}},
		{"a value over the bound", "value", wire.SystemWriteRequest{RunID: run, Selector: "#a", Value: strings.Repeat("v", 4001)}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := h.post(systemWritesPath, c.req)
			if r.StatusCode != 422 || r.errCode() != "validation_failed" || !strings.Contains(string(r.body), `"field":"`+c.field+`"`) {
				t.Fatalf("%d %s", r.StatusCode, r.body)
			}
		})
	}
	if n := h.rowCount("system_writes"); n != 0 {
		t.Fatalf("%d rows were written", n)
	}
}

// A value the match route tells the engine to write is remembered under the
// run of the call, so the same value coming back as a manual fill is not
// ground truth.
func TestAnswerMatchRemembersWhatItTellsTheEngineToWrite(t *testing.T) {
	h := matchHarness(t)
	run := h.issueRun()
	r := h.post("/api/answer-bank/match", wire.AnswerMatchRequest{Host: "jobs.example.com", RunID: run, Questions: []wire.AnswerMatchQuestion{
		{Selector: `[id="county"]`, Text: "County", Kind: "text"}}})
	if r.StatusCode != 200 || r.data()["results"].([]any)[0].(map[string]any)["answer"] != "Durham County" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if h.rowCount("system_writes") != 1 {
		t.Fatal("the write was not remembered")
	}
	if h.fillGroundTruth("county-echo", run, "", "County", "Durham County") {
		t.Fatal("the engine's own value came back as the user's")
	}
}
