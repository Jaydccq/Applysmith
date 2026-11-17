package scheduler

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestScheduler_Store_PersistsInSQLite(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	sc := h.add("a", "test.fast", dailyAt("07:30"), PolicyFireOnce)
	got := h.get(sc.ID)
	if got.Name != "a" || got.Kind != "test.fast" || got.Preset.Time != "07:30" || got.Policy != PolicyFireOnce || !got.Enabled {
		t.Fatalf("%+v", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() || !got.DTStart.Equal(baseTime) || !got.LastFiredAt.Equal(baseTime) {
		t.Fatalf("stamps %+v", got)
	}
	if got.LastResult != "" || got.ActiveRunID != "" || got.PackID != "" {
		t.Fatalf("fresh schedule carries state: %+v", got)
	}
}

func TestScheduler_Store_DefaultPolicySkip(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	sc, err := h.mgr.Create(context.Background(), NewSchedule{Name: "a", Kind: "test.fast", Preset: dailyAt("07:30")})
	if err != nil || sc.Policy != PolicySkip {
		t.Fatalf("%+v %v", sc, err)
	}
}

func TestScheduler_Store_DuplicateNameConflict(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.add("a", "test.fast", dailyAt("07:30"), PolicySkip)
	_, err := h.mgr.Create(context.Background(), NewSchedule{Name: "a", Kind: "test.fast", Preset: dailyAt("08:00")})
	if !errors.Is(err, ErrNameConflict) {
		t.Fatalf("err = %v", err)
	}
}

func TestScheduler_Store_CapacityLimit200(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	for i := range MaxSchedules {
		if _, err := h.mgr.Create(context.Background(), NewSchedule{Name: fmt.Sprintf("s%03d", i), Kind: "test.fast", Preset: dailyAt("07:30")}); err != nil {
			t.Fatalf("schedule %d: %v", i, err)
		}
	}
	_, err := h.mgr.Create(context.Background(), NewSchedule{Name: "one too many", Kind: "test.fast", Preset: dailyAt("07:30")})
	if !errors.Is(err, ErrCapacity) || !strings.Contains(err.Error(), "200") {
		t.Fatalf("err = %v", err)
	}
}

func TestScheduler_Store_ConcurrentEditsSerialize(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	sc := h.add("a", "test.fast", dailyAt("07:30"), PolicySkip)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := dailyAt(fmt.Sprintf("0%d:00", i))
			if _, err := h.mgr.Update(context.Background(), sc.ID, Patch{Preset: &p}); err != nil {
				t.Errorf("edit %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	if got := h.get(sc.ID).Preset; got.Kind != PresetDaily || !strings.HasSuffix(got.Time, ":00") {
		t.Fatalf("preset %+v", got)
	}
}

func TestScheduler_Store_UpdateValidatesTheWhole(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	sc := h.add("a", "test.fast", dailyAt("07:30"), PolicySkip)
	bad := "discover.nonexistent"
	_, err := h.mgr.Update(context.Background(), sc.ID, Patch{Kind: &bad})
	var inv *InvalidError
	if !errors.As(err, &inv) || !strings.Contains(inv.Message, "test.fast") {
		t.Fatalf("err = %v", err)
	}
	if got := h.get(sc.ID); got.Kind != "test.fast" {
		t.Fatalf("a failed edit changed the schedule: %+v", got)
	}
	pol := "sometimes"
	if _, err := h.mgr.Update(context.Background(), sc.ID, Patch{Policy: &pol}); err == nil {
		t.Fatal("an unknown policy was accepted")
	}
}

func TestScheduler_Store_DeleteRemovesHistory(t *testing.T) {
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.start()
	sc := h.add("a", "test.fast", hourly(), PolicySkip)
	h.step(time.Hour)
	if err := h.mgr.Delete(context.Background(), sc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.mgr.Repo().Get(context.Background(), sc.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if rows := h.history(sc.ID); len(rows) != 0 {
		t.Fatalf("history remains: %v", rows)
	}
}

func fakeEnv(zone string, sources ...string) PackEnv {
	env := PackEnv{Zone: zone}
	for _, s := range sources {
		env.Sources = append(env.Sources, PackSource{Name: s, Title: s, Browser: strings.HasPrefix(s, "browser-")})
	}
	return env
}

func (h *harness) kindsFor(names ...string) {
	h.t.Helper()
	for _, n := range names {
		h.kind(n, nil, false, nil)
	}
}

func scheduleNames(t *testing.T, h *harness) []string {
	t.Helper()
	all, err := h.mgr.Repo().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range all {
		out = append(out, s.Name)
	}
	return out
}

func TestScheduler_NewWorkspace_NoSchedules(t *testing.T) {
	h := newHarness(t)
	h.kindsFor("gmail.sync")
	views, err := h.mgr.List(context.Background())
	if err != nil || len(views) != 0 {
		t.Fatalf("%v %v", views, err)
	}
	if SetupPacks[0] != "none" || len(SetupPacks) != 3 {
		t.Fatalf("setup offers %v", SetupPacks)
	}
}

func TestScheduler_Packs_DailyOnePerEnabledSourceStaggered(t *testing.T) {
	h := newHarness(t)
	h.kindsFor("discover.registry", "discover.tracked", "inbox.evaluate")
	if _, err := h.mgr.ApplyPack(context.Background(), "daily", fakeEnv("America/New_York", "registry", "tracked")); err != nil {
		t.Fatal(err)
	}
	all, _ := h.mgr.Repo().List(context.Background())
	scans := packScans(all)
	if len(scans) != 2 || scans[0].Preset.Time != "07:00" || scans[1].Preset.Time != "07:10" || scans[0].Policy != PolicyFireOnce || scans[0].Preset.Timezone != "America/New_York" {
		t.Fatalf("%+v", scans)
	}
	if got := packOrder(all); got != "scan inbox.evaluate" {
		t.Fatalf("order %q", got)
	}
	for _, s := range all {
		if s.PackID != "daily" || s.PackKey == "" || strings.ContainsAny(s.Name, "0123456789") {
			t.Errorf("marker or name: %+v", s)
		}
	}
	// With no source enabled the pack schedules nothing, not a lone evaluation.
	for _, id := range []string{"daily", "twice-daily"} {
		p, _ := findPack(id)
		if got := p.Build(fakeEnv("UTC")); len(got) != 0 {
			t.Errorf("%s without sources: %+v", id, got)
		}
	}
}

// packScans are the discovery scans among a pack's schedules.
func packScans(all []Schedule) []Schedule {
	var out []Schedule
	for _, s := range all {
		if strings.HasPrefix(s.Kind, "discover.") {
			out = append(out, s)
		}
	}
	return out
}

// packOrder lists a pack's schedules by time of day, the scans of a round read
// as one "scan": the inbox evaluation must follow each round's scans. At equal
// times the evaluation sorts first, since nothing orders it after a scan.
func packOrder(all []Schedule) string {
	sorted := slices.SortedFunc(slices.Values(all), func(a, b Schedule) int {
		return cmp.Or(strings.Compare(a.Preset.Time, b.Preset.Time), strings.Compare(b.Kind, a.Kind))
	})
	var out []string
	for _, s := range sorted {
		k := s.Kind
		if strings.HasPrefix(k, "discover.") {
			k = "scan"
		}
		if k != "scan" || len(out) == 0 || out[len(out)-1] != "scan" {
			out = append(out, k)
		}
	}
	return strings.Join(out, " ")
}

// No pack reads Gmail: the server's watcher reads mail as it arrives, and a
// gmail.sync schedule is one the user made.
func TestScheduler_Packs_NoneSchedulesTheGmailSync(t *testing.T) {
	for _, p := range Packs() {
		for _, s := range p.Build(fakeEnv("UTC", "registry", "tracked", "browser-a")) {
			if s.Kind == "gmail.sync" {
				t.Errorf("pack %s schedules the Gmail sync", p.ID)
			}
		}
	}
}

// Disconnecting Gmail switches off the user's gmail.sync schedules, which
// would only fail; connecting again switches back on only those, so one the
// user had switched off stays off, and adds none.
func TestScheduler_Packs_GmailDisconnectPausesTheSyncAndConnectResumesIt(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.kindsFor("gmail.sync", "discover.registry")
	on := h.add("My mail", "gmail.sync", dailyAt("12:00"), PolicySkip)
	off := h.add("Weekend mail", "gmail.sync", dailyAt("13:00"), PolicySkip)
	other := h.add("Scan", "discover.registry", dailyAt("07:00"), PolicySkip)
	no := false
	if _, err := h.mgr.Update(ctx, off.ID, Patch{Enabled: &no}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.mgr.PauseGmailSync(ctx); err != nil {
		t.Fatal(err)
	}
	if h.get(on.ID).Enabled || !h.get(other.ID).Enabled {
		t.Fatalf("after disconnect: mine=%v scan=%v", h.get(on.ID).Enabled, h.get(other.ID).Enabled)
	}
	// The list says why: the disconnect paused "My mail", the user "Weekend mail".
	if by, _ := h.mgr.Repo().Paused(ctx); !maps.Equal(by, map[int64]string{on.ID: "gmail.sync"}) {
		t.Fatalf("Paused = %v", by)
	}
	resumed, err := h.mgr.ResumeGmailSync(ctx)
	if err != nil || len(resumed) != 1 || !h.get(on.ID).Enabled || h.get(off.ID).Enabled {
		t.Fatalf("after connect: resumed=%v mine=%v weekend=%v, %v", resumed, h.get(on.ID).Enabled, h.get(off.ID).Enabled, err)
	}
	if names := scheduleNames(t, h); len(names) != 3 {
		t.Fatalf("connecting changed the schedules: %v", names)
	}
}

func TestScheduler_Packs_ReapplyAndEditDetach(t *testing.T) {
	h := newHarness(t)
	h.kindsFor("discover.registry", "discover.tracked")
	ctx := context.Background()
	env := fakeEnv("UTC", "registry", "tracked")
	if _, err := h.mgr.ApplyPack(ctx, "daily", env); err != nil {
		t.Fatal(err)
	}
	res, err := h.mgr.ApplyPack(ctx, "daily", env)
	if err != nil || len(res.Created) != 0 || len(res.Updated) != 2 {
		t.Fatalf("second application: %+v %v", res, err)
	}
	if all, _ := h.mgr.Repo().List(ctx); len(all) != 2 {
		t.Fatalf("%d schedules after applying twice", len(all))
	}
	all, _ := h.mgr.Repo().List(ctx)
	edited := dailyAt("11:11")
	if _, err := h.mgr.Update(ctx, all[0].ID, Patch{Preset: &edited}); err != nil {
		t.Fatal(err)
	}
	if got := h.get(all[0].ID); got.PackID != "" {
		t.Fatalf("an edit did not remove the marker: %+v", got)
	}
	res, err = h.mgr.ApplyPack(ctx, "daily", env)
	if err != nil || len(res.Created) != 1 || len(res.Updated) != 1 {
		t.Fatalf("after the edit: %+v %v", res, err)
	}
	if got := h.get(all[0].ID); got.Preset.Time != "11:11" {
		t.Fatalf("the edited schedule lost the user's time: %+v", got)
	}
	if n := len(scheduleNames(t, h)); n != 3 {
		t.Fatalf("%d schedules", n)
	}
	// Switching a pack schedule off is not an edit of what it does.
	off := false
	pack := h.packSchedule("daily", "discover.tracked")
	if _, err := h.mgr.Update(ctx, pack.ID, Patch{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if got := h.get(pack.ID); got.PackID != "daily" {
		t.Fatalf("disabling removed the marker: %+v", got)
	}
}

func (h *harness) packSchedule(pack, key string) Schedule {
	h.t.Helper()
	all, err := h.mgr.Repo().ListPack(context.Background(), pack)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, s := range all {
		if s.PackKey == key {
			return s
		}
	}
	h.t.Fatalf("no schedule %s/%s", pack, key)
	return Schedule{}
}

func TestScheduler_Packs_SourcesFollowTheUsersChoices(t *testing.T) {
	h := newHarness(t)
	h.kindsFor("discover.registry", "discover.tracked", "discover.exa")
	ctx := context.Background()
	if _, err := h.mgr.ApplyPack(ctx, "daily", fakeEnv("UTC", "registry", "tracked", "exa")); err != nil {
		t.Fatal(err)
	}
	// The user turns two sources off; the next application drops their schedules.
	res, err := h.mgr.ApplyPack(ctx, "daily", fakeEnv("UTC", "registry", "exa"))
	if err != nil || len(res.Removed) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	all, _ := h.mgr.Repo().List(ctx)
	if len(all) != 2 || all[0].Preset.Time != "07:00" || all[1].Preset.Time != "07:10" {
		t.Fatalf("%+v", all)
	}
}

func TestScheduler_Packs_UnavailableKindsAreReportedNotFaked(t *testing.T) {
	h := newHarness(t)
	h.kindsFor("price.check", "cost.report")
	res, err := h.mgr.ApplyPack(context.Background(), "maintenance", fakeEnv("UTC"))
	if err != nil || len(res.Created) != 2 || len(res.Unavailable) != 3 {
		t.Fatalf("%+v %v", res, err)
	}
	for _, u := range res.Unavailable {
		if !strings.Contains(u.Reason, u.Kind) {
			t.Errorf("%+v", u)
		}
	}
}

func TestScheduler_Packs_ContainNoSubmitKindAndNoPersonalValue(t *testing.T) {
	env := fakeEnv("UTC", "registry", "browser-a")
	for _, p := range Packs() {
		for _, d := range p.Build(env) {
			if strings.HasPrefix(d.Kind, "apply") || len(d.Inputs) != 0 {
				t.Errorf("pack %s: %+v", p.ID, d)
			}
			if _, err := NewRecurrence(d.Preset, baseTime); err != nil {
				t.Errorf("pack %s entry %s: %v", p.ID, d.Key, err)
			}
			if err := d.Preset.Validate(baseTime, baseTime); err != nil {
				t.Errorf("pack %s entry %s: %v", p.ID, d.Key, err)
			}
		}
	}
	ids := map[string]bool{}
	for _, p := range Packs() {
		ids[p.ID] = true
	}
	for _, want := range []string{"daily", "twice-daily", "browser-boards", "hiring-signals-daily", "memory-nightly", "maintenance"} {
		if !ids[want] {
			t.Errorf("pack %s missing", want)
		}
	}
}

func TestScheduler_Packs_DeleteByPack(t *testing.T) {
	h := newHarness(t)
	h.kindsFor("discover.registry", "gmail.sync", "signals.collect")
	ctx := context.Background()
	mine := h.add("mine", "gmail.sync", dailyAt("12:00"), PolicySkip)
	if _, err := h.mgr.ApplyPack(ctx, "daily", fakeEnv("UTC", "registry")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.mgr.ApplyPack(ctx, "hiring-signals-daily", fakeEnv("UTC")); err != nil {
		t.Fatal(err)
	}
	names, err := h.mgr.DeletePack(ctx, "daily")
	if err != nil || len(names) != 1 {
		t.Fatalf("%v %v", names, err)
	}
	if got := scheduleNames(t, h); len(got) != 2 {
		t.Fatalf("remaining %v", got)
	}
	if _, err := h.mgr.Repo().Get(ctx, mine.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.mgr.DeletePack(ctx, "nonexistent"); !errors.Is(err, ErrUnknownPack) {
		t.Fatalf("err = %v", err)
	}
}

func TestScheduler_Store_PauseAllResumeAllRestoresOnlyWhatItPaused(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	a := h.add("a", "test.fast", dailyAt("07:00"), PolicySkip)
	b := h.add("b", "test.fast", dailyAt("08:00"), PolicySkip)
	c := h.add("c", "test.fast", dailyAt("09:00"), PolicySkip)
	off := false
	if _, err := h.mgr.Update(ctx, b.ID, Patch{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	repo := h.mgr.Repo()
	names, err := repo.PauseAll(ctx)
	if err != nil || !eq(names, "a", "c") {
		t.Fatalf("paused %v, %v", names, err)
	}
	if by, _ := repo.Paused(ctx); !maps.Equal(by, map[int64]string{a.ID: "", c.ID: ""}) {
		t.Fatalf("Paused = %v", by)
	}
	// A second pause-all switches off nothing and keeps what the first recorded.
	if names, err = repo.PauseAll(ctx); err != nil || len(names) != 0 {
		t.Fatalf("second pause %v, %v", names, err)
	}
	// The user switches c on again, and b stays off by the user's own choice.
	h.clock.Advance(time.Minute)
	on := true
	if _, err := h.mgr.Update(ctx, c.ID, Patch{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	names, err = repo.ResumeAll(ctx)
	if err != nil || !eq(names, "a") {
		t.Fatalf("resumed %v, %v", names, err)
	}
	if !h.get(a.ID).Enabled || h.get(b.ID).Enabled || !h.get(c.ID).Enabled {
		t.Fatalf("a=%v b=%v c=%v", h.get(a.ID).Enabled, h.get(b.ID).Enabled, h.get(c.ID).Enabled)
	}
	if !h.get(a.ID).LastFiredAt.Equal(h.clock.Now()) {
		t.Fatalf("resumed cursor %v, want now", h.get(a.ID).LastFiredAt)
	}
	// The record is spent.
	if names, _ = repo.ResumeAll(ctx); len(names) != 0 {
		t.Fatalf("second resume %v", names)
	}
	if by, _ := repo.Paused(ctx); len(by) != 0 {
		t.Fatalf("Paused = %v after resume", by)
	}
}

func TestScheduler_Store_ResumeAllIgnoresADeletedScheduleWhoseIDIsReused(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	a := h.add("a", "test.fast", dailyAt("07:00"), PolicySkip)
	if _, err := h.mgr.Repo().PauseAll(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.mgr.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(time.Hour)
	off := false
	fresh, err := h.mgr.Create(ctx, NewSchedule{Name: "fresh", Kind: "test.fast", Preset: dailyAt("07:00"), Enabled: &off})
	if err != nil || fresh.ID != a.ID {
		t.Fatalf("the id was not reused: %+v %v", fresh, err)
	}
	if names, err := h.mgr.Repo().ResumeAll(ctx); err != nil || len(names) != 0 || h.get(fresh.ID).Enabled {
		t.Fatalf("resumed %v, %v; fresh enabled = %v", names, err, h.get(fresh.ID).Enabled)
	}
}

func TestScheduler_Store_PausedSchedulesDoNotFireAndResumeDoesNotReplayThePause(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.start()
	sc := h.add("a", "test.fast", dailyAt("07:00"), PolicyFireAll)
	if _, err := h.mgr.Repo().PauseAll(ctx); err != nil {
		t.Fatal(err)
	}
	h.step(26 * time.Hour) // the 07:00 of today and of tomorrow pass while paused
	if h.called("test.fast") != 0 {
		t.Fatalf("a paused schedule fired %d times", h.called("test.fast"))
	}
	if _, err := h.mgr.Repo().ResumeAll(ctx); err != nil {
		t.Fatal(err)
	}
	h.step(24 * time.Hour)
	if h.called("test.fast") != 1 {
		t.Fatalf("after the resume the schedule fired %d times, want 1", h.called("test.fast"))
	}
	if d := h.decisions(sc.ID); !eq(d, "fired") {
		t.Fatalf("decisions %v: the paused time was replayed", d)
	}
}

// hygieneKinds registers a kind that fails and one that succeeds.
func (h *harness) hygieneKinds() {
	h.kind("test.fail", nil, false, func(context.Context, Invocation) (string, error) { return "", errors.New("signed out") })
	h.kind("test.fast", nil, false, nil)
}

func findings(t *testing.T, h *harness) map[string][]string {
	t.Helper()
	got, err := h.mgr.Repo().Hygiene(context.Background(), h.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, f := range got {
		out[f.Kind] = append(out[f.Kind], f.Name)
	}
	return out
}

func TestScheduler_Hygiene_FindsDuplicatesDeadLanesAndIdleSchedules(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.hygieneKinds()
	h.start()
	dead := h.add("dead", "test.fail", hourly(), PolicySkip)
	h.add("healthy", "test.fast", hourly(), PolicySkip)
	retired := h.add("retired", "test.fail", dailyAt("23:00"), PolicySkip) // disabled before it ever ran
	// Two schedules that differ only in case, and two that differ only in name.
	h.add("Scan", "test.fast", dailyAt("10:15"), PolicySkip)
	h.add("scan", "test.fast", dailyAt("11:15"), PolicySkip)
	h.add("twin one", "test.fast", dailyAt("12:45"), PolicySkip)
	h.add("twin two", "test.fast", dailyAt("12:45"), PolicySkip)
	h.step(4 * time.Hour)
	off := false
	if _, err := h.mgr.Update(ctx, retired.ID, Patch{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	h.step(18 * time.Hour)
	got := findings(t, h)
	if !eq(got[HygieneDeadLane], "dead") {
		t.Errorf("dead lanes %v", got[HygieneDeadLane])
	}
	if len(got[HygieneDuplicate]) != 2 {
		t.Errorf("duplicates %v", got[HygieneDuplicate])
	}
	// A disabled schedule that never failed is nobody's problem.
	if len(got[HygieneDisabledAndFailed]) != 0 || len(got[HygieneNeverRan]) != 0 {
		t.Errorf("findings %v", got)
	}
	// A schedule disabled after a failed run is reported with that.
	if _, err := h.mgr.Update(ctx, dead.ID, Patch{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	got = findings(t, h)
	if !eq(got[HygieneDisabledAndFailed], "dead") || len(got[HygieneDeadLane]) != 0 {
		t.Errorf("after disabling: %v", got)
	}
}

func TestScheduler_Hygiene_AFailureStreakNeedsThreeInARowAndIgnoresCancelsAndInterrupts(t *testing.T) {
	h := newHarness(t)
	h.hygieneKinds()
	h.start()
	sc := h.add("lane", "test.fail", hourly(), PolicySkip)
	h.step(3 * time.Hour) // 07:00, 08:00, 09:00 fail
	// The newest of the three is interrupted by a stop: it is not evidence.
	h.exec(`UPDATE runs SET status = 'failed', failure_reason = 'interrupted' WHERE schedule_id = ? AND scheduled_for = ?`, sc.ID, ts(baseTime.Add(3*time.Hour)))
	if got := findings(t, h); len(got[HygieneDeadLane]) != 0 {
		t.Fatalf("two failures and an interrupt made a dead lane: %v", got)
	}
	// A follow-up of another kind recorded on the schedule (Kind.Then) is not
	// evidence about the lane.
	at := ts(baseTime.Add(3*time.Hour + 30*time.Minute))
	h.exec(`INSERT INTO runs (id, kind, status, failure_reason, schedule_id, created_at, updated_at, finished_at) VALUES ('follow-up', 'test.fast', 'failed', 'error', ?, ?, ?, ?)`,
		sc.ID, at, at, at)
	if got := findings(t, h); len(got[HygieneDeadLane]) != 0 {
		t.Fatalf("a follow-up's failure counted toward the lane: %v", got)
	}
	h.step(time.Hour)
	if got := findings(t, h); !eq(got[HygieneDeadLane], "lane") {
		t.Fatalf("three failures in a row are not a dead lane: %v", got)
	}
	// A cancelled run is the user's act, not the lane's.
	h.exec(`UPDATE runs SET status = 'cancelled', failure_reason = 'cancelled' WHERE schedule_id = ? AND scheduled_for = ?`, sc.ID, ts(baseTime.Add(4*time.Hour)))
	if got := findings(t, h); len(got[HygieneDeadLane]) != 0 {
		t.Fatalf("a cancelled run counted as a failure: %v", got)
	}
	h.exec(`UPDATE runs SET status = 'failed', failure_reason = 'error' WHERE schedule_id = ? AND scheduled_for = ?`, sc.ID, ts(baseTime.Add(4*time.Hour)))
	if got := findings(t, h); !eq(got[HygieneDeadLane], "lane") {
		t.Fatalf("restored streak: %v", got)
	}
	// A success among the newest runs ends the streak.
	h.exec(`UPDATE runs SET status = 'succeeded', failure_reason = '' WHERE schedule_id = ? AND scheduled_for = ?`, sc.ID, ts(baseTime.Add(4*time.Hour)))
	if got := findings(t, h); len(got[HygieneDeadLane]) != 0 {
		t.Fatalf("a success did not end the streak: %v", got)
	}
}

func TestScheduler_Hygiene_NeverRanNeedsDueOccurrencesAndFourteenDays(t *testing.T) {
	h := newHarness(t)
	h.hygieneKinds()
	h.add("idle", "test.fast", dailyAt("09:00"), PolicySkip)
	h.add("yearly", "test.fast", Preset{Kind: PresetRRule, RRule: "FREQ=YEARLY;BYMONTH=12;BYMONTHDAY=25;BYHOUR=9;BYMINUTE=0;BYSECOND=0", Timezone: "UTC"}, PolicySkip)
	h.clock.Advance(13 * 24 * time.Hour)
	if got := findings(t, h); len(got[HygieneNeverRan]) != 0 {
		t.Fatalf("flagged before 14 days: %v", got)
	}
	h.clock.Advance(2 * 24 * time.Hour)
	if got := findings(t, h); !eq(got[HygieneNeverRan], "idle") {
		t.Fatalf("never-ran = %v; a sparse rule with nothing due must not count", got[HygieneNeverRan])
	}
	// One run clears it.
	h.exec(`INSERT INTO schedule_history (schedule_id, scheduled_for, outcome, created_at) SELECT id, ?, 'fired', ? FROM schedules WHERE name = 'idle'`,
		ts(baseTime.Add(24*time.Hour)), ts(baseTime))
	if got := findings(t, h); len(got[HygieneNeverRan]) != 0 {
		t.Fatalf("a run did not clear it: %v", got)
	}
}

func TestScheduler_Audit_ExpectedSlotsAgainstDecisions(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	sc := h.add("hourly", "test.fast", hourly(), PolicySkip)
	repo := h.mgr.Repo()
	slot := func(hour int) time.Time { return time.Date(2026, 5, 4, hour, 0, 0, 0, time.UTC) }
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	cur := func() Schedule { return h.get(sc.ID) }
	must(repo.Skip(ctx, cur(), slot(7), ReasonHostAsleep, "gap"))     // consumed: the machine slept
	_, _, err := repo.Defer(ctx, sc.ID, slot(8), ReasonLease, "held") // still waiting
	must(err)
	_, _, err = repo.Defer(ctx, sc.ID, slot(9), ReasonLease, "held") // waited, then given up on
	must(err)
	must(repo.Skip(ctx, cur(), slot(9), ReasonLease, "still not startable"))
	// 10:00 has no record at all.
	_, err = repo.ClaimRun(ctx, Claim{Schedule: cur(), ScheduledFor: slot(11)})
	must(err)
	_, err = repo.ClaimRun(ctx, Claim{Schedule: cur(), ScheduledFor: slot(12)})
	must(err)

	h.clock.Advance(6*time.Hour + 30*time.Minute) // 12:30
	audit, err := repo.DayAudit(ctx, h.clock.Now(), time.UTC)
	must(err)
	if audit.Date != "2026-05-04" || len(audit.Schedules) != 1 {
		t.Fatalf("audit %+v", audit)
	}
	a := audit.Schedules[0]
	if a.Expected != 6 || a.Fired != 2 || a.Consumed[ReasonHostAsleep] != 1 || a.Consumed[ReasonLease] != 1 || a.ConsumedAfterDeferral != 1 {
		t.Errorf("counts %+v", a)
	}
	if len(a.Deferred) != 1 || !a.Deferred[0].Equal(slot(8)) {
		t.Errorf("deferred %v", a.Deferred)
	}
	if len(a.Missing) != 1 || !a.Missing[0].Equal(slot(10)) {
		t.Errorf("missing %v", a.Missing)
	}
	if a.Lost() != 1 {
		t.Errorf("lost = %d: a host-asleep skip is the machine's, a lease skip is not", a.Lost())
	}
	// The slot that only just came due is left to the tick.
	h.clock.Advance(-29 * time.Minute) // 12:01
	audit, _ = repo.DayAudit(ctx, h.clock.Now(), time.UTC)
	if got := audit.Totals().Expected; got != 5 {
		t.Errorf("expected %d with the 12:00 slot inside the grace", got)
	}
}

func TestScheduler_Audit_OccurrencesBeforeTheLastEditAreNotExpected(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.kind("test.fast", nil, false, nil)
	h.clock.Advance(8 * time.Hour) // 14:00, the schedule is created late in the day
	h.add("late", "test.fast", hourly(), PolicySkip)
	h.clock.Advance(2*time.Hour + 30*time.Minute) // 16:30: 15:00 and 16:00 are expected
	audit, err := h.mgr.Repo().DayAudit(ctx, h.clock.Now(), time.UTC)
	if err != nil || audit.Totals().Expected != 2 || len(audit.Totals().Missing) != 2 {
		t.Fatalf("%+v %v", audit, err)
	}
}
