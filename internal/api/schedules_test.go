package api

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"applysmith/internal/tracker"
)

func costSchedule(name string) map[string]any {
	return map[string]any{"name": name, "kind": "cost.report", "preset": map[string]any{"kind": "daily", "time": "23:30", "timezone": "UTC"}}
}

func scheduleHistoryRows(t *testing.T, h *harness, id float64) []any {
	t.Helper()
	r := h.get("/api/schedules/" + itoa(int(id)) + "/history")
	if r.StatusCode != 200 {
		t.Fatalf("history: %d %s", r.StatusCode, r.body)
	}
	rows, _ := r.data()["history"].([]any)
	return rows
}

func removeAndRecreate(path string) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	return os.WriteFile(path, nil, 0o600)
}

func TestSchedules_Lifecycle(t *testing.T) {
	h := newDomainHarness(t)
	if r := h.get("/api/schedules"); r.StatusCode != 200 || len(r.data()["schedules"].([]any)) != 0 {
		t.Fatalf("a new workspace has schedules: %s", r.body)
	}
	r := h.post("/api/schedules", costSchedule("cost"))
	if r.StatusCode != 201 {
		t.Fatalf("create: %d %s", r.StatusCode, r.body)
	}
	sc := r.data()
	id := sc["id"].(float64)
	if sc["missedPolicy"] != "skip" || sc["enabled"] != true || len(sc["next"].([]any)) != 5 {
		t.Fatalf("created schedule: %v", sc)
	}
	path := "/api/schedules/" + itoa(int(id))
	if r := h.post("/api/schedules", costSchedule("cost")); r.StatusCode != 409 {
		t.Fatalf("duplicate name: %d %s", r.StatusCode, r.body)
	}
	r = h.patch(path, map[string]any{"preset": map[string]any{"kind": "daily", "time": "22:00", "timezone": "UTC"}, "enabled": false})
	if r.StatusCode != 200 || r.data()["enabled"] != false || len(r.data()["next"].([]any)) != 0 {
		t.Fatalf("patch: %d %s", r.StatusCode, r.body)
	}
	if r := h.patch(path, map[string]any{"kind": "discover.nonexistent"}); r.StatusCode != 422 || !strings.Contains(string(r.body), "cost.report") {
		t.Fatalf("unknown kind: %d %s", r.StatusCode, r.body)
	}
	r = h.send("DELETE", path, nil)
	if r.StatusCode != 200 || len(r.data()["schedules"].([]any)) != 0 {
		t.Fatalf("delete: %d %s", r.StatusCode, r.body)
	}
	if r := h.get(path); r.StatusCode != 404 {
		t.Fatalf("after delete: %d", r.StatusCode)
	}
}

func TestSchedules_NoApplyKindAndNoSubmitInput(t *testing.T) {
	h := newDomainHarness(t)
	bad := costSchedule("apply")
	bad["kind"] = "apply.list"
	if r := h.post("/api/schedules", bad); r.StatusCode != 422 {
		t.Fatalf("apply kind: %d %s", r.StatusCode, r.body)
	}
	withSubmit := costSchedule("submit")
	withSubmit["inputs"] = map[string]any{"submit": true}
	if r := h.post("/api/schedules", withSubmit); r.StatusCode != 422 || !strings.Contains(string(r.body), "submit") {
		t.Fatalf("submit input: %d %s", r.StatusCode, r.body)
	}
}

func TestSchedules_ManualRunIsRecordedAndLinksToTheRun(t *testing.T) {
	h := newDomainHarness(t)
	id := h.post("/api/schedules", costSchedule("cost")).data()["id"].(float64)
	off := false
	if r := h.patch("/api/schedules/"+itoa(int(id)), map[string]any{"enabled": off}); r.StatusCode != 200 {
		t.Fatal(string(r.body))
	}
	r := h.post("/api/schedules/"+itoa(int(id))+"/run", nil)
	if r.StatusCode != 202 {
		t.Fatalf("run: %d %s", r.StatusCode, r.body)
	}
	runID := r.data()["runId"].(string)
	waitFor(t, "the manual run to finish", func() bool {
		rows := scheduleHistoryRows(t, h, id)
		return len(rows) == 1 && rows[0].(map[string]any)["finishedAt"] != nil
	})
	row := scheduleHistoryRows(t, h, id)[0].(map[string]any)
	if row["outcome"] != "manual" || row["runId"] != runID || !strings.Contains(row["summary"].(string), "model calls") {
		t.Fatalf("history row: %v", row)
	}
	if r := h.get("/api/runs/" + runID); r.StatusCode != 200 || r.data()["status"] != "succeeded" {
		t.Fatalf("run view: %d %s", r.StatusCode, r.body)
	}
}

func TestSchedules_ManualRunRefusedNamesTheReason(t *testing.T) {
	h := newDomainHarness(t)
	id := h.post("/api/schedules", costSchedule("cost")).data()["id"].(float64)
	err := h.srv.store.Load().Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE schedules SET inputs_json = '{"bogus":1}' WHERE id = ?`, int(id))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	r := h.post("/api/schedules/"+itoa(int(id))+"/run", nil)
	if r.StatusCode != 409 || r.errReason() != "validation" {
		t.Fatalf("run: %d %s", r.StatusCode, r.body)
	}
	if r := h.post("/api/schedules/99/run", nil); r.StatusCode != 404 {
		t.Fatalf("unknown schedule: %d", r.StatusCode)
	}
}

func TestSchedules_PacksListApplyAndDelete(t *testing.T) {
	h := newDomainHarness(t)
	list := h.get("/api/schedule-packs")
	if list.StatusCode != 200 || len(h.get("/api/schedules").data()["schedules"].([]any)) != 0 {
		t.Fatalf("listing packs wrote schedules: %s", list.body)
	}
	apply := func(pack string) map[string]any {
		t.Helper()
		r := h.post("/api/schedule-packs/"+pack+"/apply", nil)
		if r.StatusCode != 200 {
			t.Fatalf("apply %s: %d %s", pack, r.StatusCode, r.body)
		}
		return r.data()
	}
	if got := apply("hiring-signals-daily"); len(got["created"].([]any)) != 1 {
		t.Fatalf("first application: %v", got)
	}
	if r := h.post("/api/schedule-packs/nonexistent/apply", nil); r.StatusCode != 404 {
		t.Fatalf("unknown pack: %d", r.StatusCode)
	}
	if r := h.send("DELETE", "/api/schedule-packs/hiring-signals-daily", nil); r.StatusCode != 200 || len(r.data()["removed"].([]any)) != 1 {
		t.Fatalf("delete pack: %d %s", r.StatusCode, r.body)
	}
}

func TestLock_HeldReadsAsLostWhenTheFileIsReplaced(t *testing.T) {
	dir := t.TempDir()
	l, err := AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if !l.Held() {
		t.Fatal("a fresh lock is not held")
	}
	if err := removeAndRecreate(LockPath(dir)); err != nil {
		t.Fatal(err)
	}
	if l.Held() {
		t.Fatal("a replaced lock file still reads as held")
	}
	if (*Lock)(nil).Held() {
		t.Fatal("a nil lock reads as held")
	}
}

// archive.sweep previews unless the schedule sets apply, and archives under the
// system actor when it does.
func TestSchedules_ArchiveSweepPreviewsUnlessApplyIsSet(t *testing.T) {
	h := newDomainHarness(t)
	ctx := context.Background()
	d := h.domainOf()
	old, err := d.tracker.Add(ctx, tracker.AddRow{Company: "Example Corp", Role: "Engineer", URL: "https://example.com/jobs/1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.tracker.SetStatus(ctx, tracker.StatusChange{ID: old.ID, To: tracker.Rejected, Actor: tracker.ActorUser}); err != nil {
		t.Fatal(err)
	}
	h.clock.advance(45 * 24 * time.Hour)

	run := func(name string, inputs map[string]any) string {
		t.Helper()
		body := map[string]any{"name": name, "kind": "archive.sweep", "inputs": inputs,
			"preset": map[string]any{"kind": "daily", "time": "23:30", "timezone": "UTC"}}
		cr := h.post("/api/schedules", body)
		if cr.StatusCode != 201 && cr.StatusCode != 200 {
			t.Fatalf("create: %d %s", cr.StatusCode, cr.body)
		}
		id := cr.data()["id"].(float64)
		if r := h.post("/api/schedules/"+itoa(int(id))+"/run", nil); r.StatusCode != 202 {
			t.Fatalf("run: %d %s", r.StatusCode, r.body)
		}
		waitFor(t, "the sweep to finish", func() bool {
			rows := scheduleHistoryRows(t, h, id)
			return len(rows) == 1 && rows[0].(map[string]any)["finishedAt"] != nil
		})
		row := scheduleHistoryRows(t, h, id)[0].(map[string]any)
		if row["reason"] == "error" {
			t.Fatalf("the sweep failed: %v", row)
		}
		return row["summary"].(string)
	}
	archived := func() int {
		var n int
		if err := h.srv.store.Load().Read().QueryRow(`SELECT count(*) FROM tracker_entries WHERE archived_at IS NOT NULL`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if sum := run("preview", nil); !strings.HasPrefix(sum, "1 rows would be archived") || archived() != 0 {
		t.Fatalf("default run: %q, %d archived", sum, archived())
	}
	if sum := run("applied", map[string]any{"apply": true}); sum != "1 rows archived" || archived() != 1 {
		t.Fatalf("apply run: %q, %d archived", sum, archived())
	}
	var actor string
	if err := h.srv.store.Load().Read().QueryRow(`SELECT actor FROM tracker_events WHERE kind = 'archived'`).Scan(&actor); err != nil || actor != "system" {
		t.Fatalf("archived event actor %q: %v", actor, err)
	}
}

// The daily sweep applies the retention of the gate decisions (180 days) and
// of the agent's idempotency claims (30 days).
func TestSchedules_RetentionSweepPrunesGateDecisionsAndAgentClaims(t *testing.T) {
	h := newDomainHarness(t)
	sched := h.domainOf().sched
	sched.Wait() // the start-up sweep
	stamp := func(days int) string {
		return h.clock.now().Add(-time.Duration(days) * 24 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	}
	err := h.srv.store.Load().Write(context.Background(), func(tx *sql.Tx) error {
		for _, q := range []string{
			`INSERT INTO gate_decisions (at, gate, verdict, code, reason) VALUES ('` + stamp(200) + `', 'tool', 'deny', 'old', 'r'), ('` + stamp(100) + `', 'tool', 'deny', 'kept', 'r')`,
			`INSERT INTO agent_claims (tool, key, claimed_at) VALUES ('t', 'old', '` + stamp(40) + `'), ('t', 'kept', '` + stamp(10) + `')`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	h.clock.advance(25 * time.Hour) // the next 04:00 slot
	sched.Tick(context.Background())
	sched.Wait()
	waitFor(t, "the retention sweep", func() bool {
		return h.rowCount("gate_decisions WHERE code = 'old'") == 0 && h.rowCount("agent_claims WHERE key = 'old'") == 0
	})
	if h.rowCount("gate_decisions WHERE code = 'kept'") != 1 || h.rowCount("agent_claims WHERE key = 'kept'") != 1 {
		t.Fatal("the sweep removed rows inside their retention")
	}
}
