package api

import (
	"fmt"
	"testing"

	"applysmith/internal/api/wire"
)

func TestTrackerOutcomesCountsInterviewThenRejectedAndWritesNothing(t *testing.T) {
	h := newDomainHarness(t)
	row := h.addCandidacy("Acme Labs", "Platform Engineer", "https://jobs.example.com/acme/1")
	h.addCandidacy("Zeta Works", "Data Engineer", "https://jobs.example.com/zeta/1")
	path := "/api/candidacies/" + idOf(row)
	for _, status := range []string{"applied", "interview", "rejected"} {
		if r := h.patchRow(path, map[string]any{"status": status}); r.StatusCode != 200 {
			t.Fatalf("%s: %d %s", status, r.StatusCode, r.body)
		}
	}
	before := fmt.Sprint(h.get("/api/tracker/summary").data())
	revision := h.boardRevision("tracker")

	var env wire.Success[wire.TrackerOutcomes]
	r := h.get("/api/tracker/outcomes")
	strictInto(t, r.body, &env)
	o := env.Data
	if r.StatusCode != 200 || o.AsOf == "" || o.Overall.Applications != 1 || o.Overall.Known != 1 || o.Overall.Closed != 1 {
		t.Fatalf("outcomes: %d %s", r.StatusCode, r.body)
	}
	var interview wire.OutcomeStageReach
	for _, s := range o.Overall.Stages {
		if s.Stage == "interview" {
			interview = s
		}
	}
	if interview.Numerator != 1 || interview.Denominator != 1 || len(o.BySource) != 1 || o.BySource[0].Key != "manual" {
		t.Fatalf("interview-then-rejected must still count as an interview: %+v", o)
	}
	if len(o.Overall.Insufficient) == 0 {
		t.Errorf("one application is not enough to compare: %+v", o.Overall)
	}
	if fmt.Sprint(h.get("/api/tracker/summary").data()) != before {
		t.Fatal("reading the outcome summary changed the tracker")
	}
	if got := h.boardRevision("tracker"); got != revision {
		t.Fatalf("tracker revision %d -> %d", revision, got)
	}
	if cohort := h.get("/api/tracker/outcomes?from=2999-01-01"); cohort.data()["overall"].(map[string]any)["applications"].(float64) != 0 {
		t.Errorf("a cohort that starts in the future is empty: %s", cohort.body)
	}
}

func TestTrackerOutcomesRejectsBadDates(t *testing.T) {
	h := newDomainHarness(t)
	for name, q := range map[string]string{"from": "from=soon", "order": "from=2027-02-01&to=2027-01-01"} {
		if r := h.get("/api/tracker/outcomes?" + q); r.StatusCode != 422 || r.errCode() != "validation_failed" {
			t.Errorf("%s: %d %s", name, r.StatusCode, r.body)
		}
	}
}
