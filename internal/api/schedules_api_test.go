package api

import (
	"context"
	"strings"
	"testing"

	"applysmith/internal/scheduler"
)

// holdKind registers a kind that holds the tracker lease until released.
func holdKind(t *testing.T, h *harness) (started, release chan struct{}) {
	t.Helper()
	started, release = make(chan struct{}, 4), make(chan struct{})
	err := h.domainOf().sched.Registry().Register(scheduler.Kind{Name: "test.hold", Leases: []scheduler.Lease{scheduler.LeaseTracker},
		Drives: scheduler.Drives{TrackerWrite: true},
		Run: func(ctx context.Context, _ scheduler.Invocation) (string, error) {
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			return "released", nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	return started, release
}

func holdSchedule(name string) map[string]any {
	return map[string]any{"name": name, "kind": "test.hold", "preset": map[string]any{"kind": "daily", "time": "23:30", "timezone": "UTC"}}
}

func TestScheduler_API_ValidationAndNotFound(t *testing.T) {
	h := newDomainHarness(t)
	badPreset := costSchedule("bad")
	badPreset["preset"] = map[string]any{"kind": "daily", "time": "25:99", "timezone": "UTC"}
	badInput := costSchedule("input")
	badInput["inputs"] = map[string]any{"nonsense": 1}
	for name, body := range map[string]map[string]any{"preset": badPreset, "catalog input": badInput} {
		r := h.post("/api/schedules", body)
		if r.StatusCode != 422 || r.errCode() != "validation_failed" {
			t.Errorf("%s: %d %s", name, r.StatusCode, r.body)
		}
	}
	if r := h.post("/api/schedules", badInput); !strings.Contains(string(r.body), "nonsense") {
		t.Errorf("the validator's message does not reach the caller: %s", r.body)
	}
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/schedules/999"}, {"PATCH", "/api/schedules/999"}, {"DELETE", "/api/schedules/999"},
		{"GET", "/api/schedules/999/history"}, {"POST", "/api/schedules/999/run"},
	} {
		var body any
		if c.method == "PATCH" {
			body = map[string]any{"enabled": false}
		}
		if r := h.send(c.method, c.path, body); r.StatusCode != 404 || r.errCode() != "not_found" {
			t.Errorf("%s %s: %d %s", c.method, c.path, r.StatusCode, r.body)
		}
	}
}

func TestScheduler_API_DeleteWhileRunningConflict(t *testing.T) {
	h := newDomainHarness(t)
	started, release := holdKind(t, h)
	defer close(release)
	id := int(h.post("/api/schedules", holdSchedule("hold")).data()["id"].(float64))
	if r := h.post("/api/schedules/"+itoa(id)+"/run", nil); r.StatusCode != 202 {
		t.Fatalf("run: %d %s", r.StatusCode, r.body)
	}
	<-started
	r := h.send("DELETE", "/api/schedules/"+itoa(id), nil)
	if r.StatusCode != 409 || r.errCode() != "conflict" {
		t.Fatalf("delete while running: %d %s", r.StatusCode, r.body)
	}
	if r := h.get("/api/schedules/" + itoa(id)); r.StatusCode != 200 {
		t.Fatalf("the schedule did not remain: %d", r.StatusCode)
	}
	if r := h.send("DELETE", "/api/schedules/"+itoa(id)+"?cancel=true", nil); r.StatusCode != 200 {
		t.Fatalf("delete with cancel: %d %s", r.StatusCode, r.body)
	}
	if r := h.get("/api/schedules/" + itoa(id)); r.StatusCode != 404 {
		t.Fatalf("after the delete: %d", r.StatusCode)
	}
}

func TestScheduler_API_ManualRunReasonNotAlways409(t *testing.T) {
	h := newDomainHarness(t)
	started, release := holdKind(t, h)
	defer close(release)
	a := int(h.post("/api/schedules", holdSchedule("a")).data()["id"].(float64))
	b := int(h.post("/api/schedules", holdSchedule("b")).data()["id"].(float64))
	if r := h.post("/api/schedules/"+itoa(a)+"/run", nil); r.StatusCode != 202 {
		t.Fatalf("run: %d %s", r.StatusCode, r.body)
	}
	<-started
	// The caller is told what stood in the way, not only that something did.
	if r := h.post("/api/schedules/"+itoa(a)+"/run", nil); r.StatusCode != 409 || r.errReason() != scheduler.ReasonBusy {
		t.Errorf("the same schedule again: %d %s", r.StatusCode, r.body)
	}
	if r := h.post("/api/schedules/"+itoa(b)+"/run", nil); r.StatusCode != 409 || r.errReason() != scheduler.ReasonLease || !strings.Contains(string(r.body), "tracker:write") {
		t.Errorf("another schedule behind the lease: %d %s", r.StatusCode, r.body)
	}
	if r := h.post("/api/schedules/999/run", nil); r.StatusCode != 404 {
		t.Errorf("unknown schedule: %d", r.StatusCode)
	}
	h.domainOf().sched.Stop(context.Background())
	if r := h.post("/api/schedules/"+itoa(b)+"/run", nil); r.StatusCode != 503 || r.errCode() != "unavailable" {
		t.Errorf("a stopped scheduler: %d %s", r.StatusCode, r.body)
	}
}

func TestScheduler_API_PauseAllResumeAllTouchOnlyWhatPauseAllSwitchedOff(t *testing.T) {
	h := newDomainHarness(t)
	a := int(h.post("/api/schedules", costSchedule("a")).data()["id"].(float64))
	b := int(h.post("/api/schedules", costSchedule("b")).data()["id"].(float64))
	h.send("PATCH", "/api/schedules/"+itoa(b), map[string]any{"enabled": false})
	enabled := func(id int) bool { return h.get("/api/schedules/" + itoa(id)).data()["enabled"].(bool) }

	r := h.post("/api/schedules/pause-all", nil)
	if r.StatusCode != 200 || len(r.data()["changed"].([]any)) != 1 || r.data()["changed"].([]any)[0] != "a" {
		t.Fatalf("pause-all: %d %s", r.StatusCode, r.body)
	}
	if enabled(a) || enabled(b) {
		t.Fatal("a schedule is still enabled after pause-all")
	}
	if got := h.get("/api/schedules").data()["pausedAll"]; got != float64(1) {
		t.Fatalf("pausedAll = %v", got)
	}
	r = h.post("/api/schedules/resume-all", nil)
	if r.StatusCode != 200 || len(r.data()["changed"].([]any)) != 1 || r.data()["changed"].([]any)[0] != "a" {
		t.Fatalf("resume-all: %d %s", r.StatusCode, r.body)
	}
	if !enabled(a) || enabled(b) {
		t.Fatalf("a enabled = %v, b enabled = %v: resume-all must restore only a", enabled(a), enabled(b))
	}
	if _, ok := h.get("/api/schedules").data()["pausedAll"]; ok {
		t.Fatal("pausedAll is still reported after resume-all")
	}
	if r := h.post("/api/schedules/resume-all", nil); r.StatusCode != 200 || len(r.data()["changed"].([]any)) != 0 {
		t.Fatalf("second resume-all: %d %s", r.StatusCode, r.body)
	}
}

func TestSchedules_RunNowRepeatedOccurrenceIsConflict(t *testing.T) {
	h := newDomainHarness(t)
	made := h.post("/api/schedules", map[string]any{"name": "owned repeat", "kind": "cost.report", "enabled": false, "preset": map[string]any{"kind": "daily", "time": "23:30", "timezone": "UTC"}})
	if made.StatusCode != 201 {
		t.Fatalf("schedule=%d %s", made.StatusCode, made.body)
	}
	path := "/api/schedules/" + itoa(int(made.data()["id"].(float64))) + "/run"
	first := h.post(path, nil)
	if first.StatusCode != 202 {
		t.Fatalf("first=%d %s", first.StatusCode, first.body)
	}
	if run := h.waitRun(first.data()["runId"].(string)); run["status"] != "succeeded" {
		t.Fatalf("first run=%v", run)
	}
	h.domainOf().sched.Wait()
	// Deliberately keep the same clock instant: this occurrence is already
	// claimed, even though its previous run has completed.
	again := h.post(path, nil)
	if again.StatusCode != 409 || !strings.Contains(string(again.body), "conflict") {
		t.Fatalf("repeat=%d %s", again.StatusCode, again.body)
	}
	id := made.data()["id"].(float64)
	if history := scheduleHistoryRows(t, h, id); len(history) != 1 {
		t.Fatalf("duplicate history=%v", history)
	}
	var runs int
	if err := h.srv.store.Load().Read().QueryRow("SELECT count(*) FROM runs WHERE schedule_id = ?", int64(id)).Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("duplicate runs=%d error=%v", runs, err)
	}
}
