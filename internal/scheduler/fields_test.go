package scheduler

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestScheduler_Store_FieldSet(t *testing.T) {
	h := newHarness(t)
	g := h.newGate()
	if err := h.reg.Register(Kind{Name: "test.opts", Run: g.job(),
		Fields: []Field{{Name: "mode", Type: FieldString}, {Name: "n", Type: FieldInt}}}); err != nil {
		t.Fatal(err)
	}
	h.start()
	on := true
	sc, err := h.mgr.create(context.Background(), NewSchedule{Name: "fields", Kind: "test.opts", Inputs: map[string]any{"mode": "a", "n": 3},
		Preset: hourly(), Policy: PolicyFireOnce, Enabled: &on, DTStart: baseTime.Add(-time.Hour)}, "daily", "discover.example")
	if err != nil {
		t.Fatal(err)
	}
	got := h.get(sc.ID)
	if got.Kind != "test.opts" || !reflect.DeepEqual(got.Inputs, map[string]any{"mode": "a", "n": float64(3)}) ||
		got.Preset.Kind != PresetRRule || !got.Enabled || got.Policy != PolicyFireOnce || !got.DTStart.Equal(baseTime.Add(-time.Hour)) ||
		got.PackID != "daily" || got.PackKey != "discover.example" {
		t.Fatalf("definition fields: %+v", got)
	}
	if got.ActiveRunID != "" || got.LastRunID != "" || got.LastResult != "" || !got.LastEvaluatedAt.IsZero() {
		t.Fatalf("a new schedule carries run state: %+v", got)
	}
	h.step(time.Hour) // 07:00 fires and the job holds
	g.waitStarted(t, 1)
	got = h.get(sc.ID)
	at := time.Date(2026, 5, 4, 7, 0, 0, 0, time.UTC)
	if got.ActiveRunID == "" || got.LastResult != ResultRunning || !got.LastFiredAt.Equal(at) || !got.LastEvaluatedAt.Equal(h.clock.Now()) {
		t.Fatalf("while the job runs: %+v", got)
	}
	ran := got.ActiveRunID
	close(g.release)
	h.s.Wait()
	// The finished run stays reachable from the schedule.
	if got = h.get(sc.ID); got.ActiveRunID != "" || got.LastResult != ResultOK || got.LastRunID != ran {
		t.Fatalf("after the job: %+v", got)
	}
}

func TestScheduler_Store_AddressedById(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.start()
	a := h.add("alpha", "test.fast", hourly(), PolicySkip)
	b := h.add("beta", "test.fast", hourly(), PolicySkip)
	h.step(time.Hour)
	name := "gamma"
	if _, err := h.mgr.Update(context.Background(), a.ID, Patch{Name: &name}); err != nil {
		t.Fatal(err)
	}
	if got := h.get(a.ID); got.ID != a.ID || got.Name != "gamma" {
		t.Fatalf("a rename changed the address: %+v", got)
	}
	if got := h.get(b.ID); got.Name != "beta" {
		t.Fatalf("an edit by id reached another schedule: %+v", got)
	}
	// The history follows the id through the rename.
	if d := h.decisions(a.ID); !eq(d, "fired") {
		t.Fatalf("history after the rename: %v", d)
	}
	// A name is not an address: the old name is free again and names a new schedule.
	again := h.add("alpha", "test.fast", hourly(), PolicySkip)
	if again.ID == a.ID || again.ID == b.ID {
		t.Fatalf("a reused name reused an id: %d", again.ID)
	}
	if err := h.mgr.Delete(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	all, _ := h.mgr.Repo().List(context.Background())
	for _, s := range all {
		ids = append(ids, s.ID)
	}
	if !slices.Equal(ids, []int64{b.ID, again.ID}) {
		t.Fatalf("delete by id left %v", ids)
	}
}
