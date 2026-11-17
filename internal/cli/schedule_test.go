package cli

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"applysmith/internal/api/wire"
)

func (te *testEnv) schedule(args ...string) output {
	return te.run(context.Background(), append([]string{"schedule"}, append(args, "--workspace", te.ws)...)...)
}

type listedSchedule struct {
	ID      int64
	Name    string
	Kind    string
	Enabled bool
	PackID  string
	Inputs  map[string]any
	Preset  struct{ Time string }
}

func (te *testEnv) listJSON() []listedSchedule {
	te.t.Helper()
	o := te.schedule("list", "--json")
	if o.code != 0 {
		te.t.Fatalf("list: %d %s", o.code, o.stderr)
	}
	// The document of GET /api/schedules.
	var out struct{ Schedules []listedSchedule }
	if err := json.Unmarshal([]byte(o.stdout), &out); err != nil {
		te.t.Fatalf("%v: %s", err, o.stdout)
	}
	return out.Schedules
}

func TestScheduleCLI_AddShowEditEnableDisableDelete(t *testing.T) {
	te := newInitTestEnv(t)
	o := te.schedule("add", "--name", "Morning cost", "--kind", "cost.report", "--at", "07:30", "--tz", "UTC")
	if o.code != 0 || !strings.Contains(o.stdout, "daily at 07:30 (UTC)") || !strings.Contains(o.stdout, "next: ") {
		t.Fatalf("add = %d %q %q", o.code, o.stdout, o.stderr)
	}
	list := te.listJSON()
	if len(list) != 1 || !list[0].Enabled || list[0].Kind != "cost.report" {
		t.Fatalf("list = %+v", list)
	}
	id := fmtID(list[0].ID)

	if o := te.schedule("show", id); o.code != 0 || !strings.Contains(o.stdout, "name: Morning cost") || !strings.Contains(o.stdout, "enabled: true") || strings.Contains(o.stdout, "last run") {
		t.Fatalf("show = %d %q", o.code, o.stdout)
	}
	if o := te.schedule("edit", id, "--at", "08:15", "--days", "mon,wed"); o.code != 0 || !strings.Contains(o.stdout, "weekly mon,wed at 08:15 (UTC)") {
		t.Fatalf("edit = %d %q %q", o.code, o.stdout, o.stderr)
	}
	if o := te.schedule("edit", id, "--days", ""); o.code != 0 || !strings.Contains(o.stdout, "daily at 08:15 (UTC)") {
		t.Fatalf("edit days cleared = %d %q %q", o.code, o.stdout, o.stderr)
	}
	if o := te.schedule("disable", id); o.code != 0 {
		t.Fatalf("disable = %d %q", o.code, o.stderr)
	}
	if got := te.listJSON(); got[0].Enabled {
		t.Fatal("disable left it enabled")
	}
	if o := te.schedule("show", id); !strings.Contains(o.stdout, "next: none while disabled") {
		t.Fatalf("show disabled = %q", o.stdout)
	}
	if o := te.schedule("enable", id); o.code != 0 {
		t.Fatalf("enable = %d %q", o.code, o.stderr)
	}
	if o := te.schedule("delete", id); o.code != 0 || len(te.listJSON()) != 0 {
		t.Fatalf("delete = %d %q", o.code, o.stderr)
	}
}

func TestScheduleCLI_RejectedInputExitsTwoAndStoresNothing(t *testing.T) {
	te := newInitTestEnv(t)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no subcommand", nil, "usage:"},
		{"missing name", []string{"add", "--kind", "cost.report", "--at", "07:00"}, "--name and --kind"},
		{"unknown kind", []string{"add", "--name", "x", "--kind", "no.such", "--at", "07:00"}, "unknown job kind"},
		{"rrule too frequent", []string{"add", "--name", "x", "--kind", "cost.report", "--rrule", "FREQ=MINUTELY;INTERVAL=1"}, "closer than"},
		{"input not a number", []string{"add", "--name", "x", "--kind", "discover.registry", "--at", "07:00", "--input", "topN=many"}, "whole number"},
		{"id not a number", []string{"show", "abc"}, "not a schedule id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := te.schedule(c.args...)
			if c.args == nil {
				o = te.run(context.Background(), "schedule")
			}
			if o.code != 2 || !strings.Contains(o.stderr, c.want) {
				t.Fatalf("%v = %d %q", c.args, o.code, o.stderr)
			}
		})
	}
	if got := te.listJSON(); len(got) != 0 {
		t.Fatalf("rejected input stored %v", got)
	}
}

func TestScheduleCLI_UnknownIDAndDuplicateNameFailWithExitOne(t *testing.T) {
	te := newInitTestEnv(t)
	for _, args := range [][]string{{"show", "9"}, {"edit", "9", "--name", "x"}, {"enable", "9"}, {"delete", "9"}, {"history", "9"}} {
		if o := te.schedule(args...); o.code != 1 || !strings.Contains(o.stderr, "no schedule with that id") {
			t.Errorf("%v = %d %q", args, o.code, o.stderr)
		}
	}
	add := []string{"add", "--name", "Same", "--kind", "cost.report", "--at", "07:00"}
	if o := te.schedule(add...); o.code != 0 {
		t.Fatal(o.stderr)
	}
	if o := te.schedule(add...); o.code != 1 {
		t.Errorf("duplicate name = %d %q", o.code, o.stderr)
	}
}

func TestScheduleCLI_HistoryOfANewScheduleIsEmpty(t *testing.T) {
	te := newInitTestEnv(t)
	te.schedule("add", "--name", "H", "--kind", "cost.report", "--at", "07:00")
	id := fmtID(te.listJSON()[0].ID)
	if o := te.schedule("history", id); o.code != 0 || !strings.Contains(o.stdout, "no history") {
		t.Fatalf("history = %d %q %q", o.code, o.stdout, o.stderr)
	}
	if o := te.schedule("history", id, "--limit", "0"); o.code != 2 {
		t.Fatalf("history --limit 0 = %d", o.code)
	}
	if o := te.schedule("history", id, "--json"); o.code != 0 || strings.Join(strings.Fields(o.stdout), "") != `{"history":[]}` {
		t.Fatalf("history json = %d %q", o.code, o.stdout)
	}
}

func TestScheduleCLI_PacksListApplyReapplyAndDelete(t *testing.T) {
	te := newInitTestEnv(t)
	o := te.schedule("packs", "--json")
	var packs []packView
	if o.code != 0 || json.Unmarshal([]byte(o.stdout), &packs) != nil || len(packs) != 7 {
		t.Fatalf("packs = %d %q", o.code, o.stdout)
	}
	setup := map[string]bool{}
	for _, p := range packs {
		setup[p.ID] = p.Setup
	}
	if !setup["daily"] || !setup["twice-daily"] || setup["maintenance"] {
		t.Fatalf("setup flags %v", setup)
	}

	if o := te.schedule("apply-pack", "daily"); o.code != 0 || !strings.Contains(o.stdout, "pack daily:") {
		t.Fatalf("apply daily = %d %q %q", o.code, o.stdout, o.stderr)
	}
	first := te.listJSON()
	if len(first) == 0 || first[0].PackID != "daily" || first[0].Preset.Time != "07:00" {
		t.Fatalf("after daily: %+v", first)
	}
	te.schedule("add", "--name", "Mine", "--kind", "cost.report", "--at", "09:00")
	if o := te.schedule("apply-pack", "daily"); o.code != 0 || !strings.Contains(o.stdout, "0 created") {
		t.Fatalf("second apply = %d %q", o.code, o.stdout)
	}
	if o := te.schedule("apply-pack", "twice-daily"); o.code != 0 {
		t.Fatalf("twice-daily = %d %q", o.code, o.stderr)
	}
	if o := te.schedule("delete", "--pack", "twice-daily"); o.code != 0 || !strings.Contains(o.stdout, "removed") {
		t.Fatalf("delete --pack = %d %q %q", o.code, o.stdout, o.stderr)
	}
	for _, s := range te.listJSON() {
		if s.PackID == "twice-daily" {
			t.Errorf("delete --pack left %+v", s)
		}
	}
	if left := te.listJSON(); len(left) != len(first)+1 {
		t.Fatalf("delete --pack must leave the daily pack and the user's schedule alone: %+v", left)
	}
	if o := te.schedule("apply-pack", "nope"); o.code != 1 || !strings.Contains(o.stderr, "no such pack") {
		t.Fatalf("unknown pack = %d %q", o.code, o.stderr)
	}
}

func TestScheduleCLI_WorksOfflineAndRunNeedsAServer(t *testing.T) {
	te := newInitTestEnv(t)
	if o := te.schedule("add", "--name", "Offline", "--kind", "cost.report", "--at", "07:00"); o.code != 0 {
		t.Fatalf("add without a server = %d %q", o.code, o.stderr)
	}
	id := fmtID(te.listJSON()[0].ID)
	if o := te.schedule("run", id); o.code != 1 || !strings.Contains(o.stderr, "no server runs") {
		t.Fatalf("run without a server = %d %q", o.code, o.stderr)
	}
}

func TestScheduleCLI_RunAgainstARunningServerIsRecordedAsManual(t *testing.T) {
	te := newInitTestEnv(t)
	te.schedule("add", "--name", "Manual", "--kind", "cost.report", "--at", "07:00", "--disabled")
	id := fmtID(te.listJSON()[0].ID)
	te.startServe()
	if o := te.schedule("run", id); o.code != 0 || !strings.Contains(o.stdout, "started run") {
		t.Fatalf("run = %d %q %q", o.code, o.stdout, o.stderr)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		o := te.schedule("history", id, "--json")
		var h wire.ScheduleHistory
		if err := json.Unmarshal([]byte(o.stdout), &h); err == nil && len(h.History) == 1 && h.History[0].Outcome == "manual" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no manual row in the history: %q", o.stdout)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if o := te.schedule("delete", id, "--cancel"); o.code != 0 {
		t.Fatalf("delete --cancel = %d %q", o.code, o.stderr)
	}
}

func fmtID(id int64) string { return strconv.FormatInt(id, 10) }

func TestScheduleCLI_PauseAllAndResumeAllRestoreOnlyWhatPauseAllSwitchedOff(t *testing.T) {
	te := newInitTestEnv(t)
	for _, name := range []string{"one", "two", "three"} {
		if o := te.schedule("add", "--name", name, "--kind", "cost.report", "--at", "07:30", "--tz", "UTC"); o.code != 0 {
			t.Fatalf("add %s: %d %s", name, o.code, o.stderr)
		}
	}
	two := fmtID(te.listJSON()[1].ID)
	if o := te.schedule("disable", two); o.code != 0 {
		t.Fatal(o.stderr)
	}
	o := te.schedule("pause-all")
	if o.code != 0 || !strings.Contains(o.stdout, "2 schedule(s) paused") || strings.Contains(o.stdout, "two") {
		t.Fatalf("pause-all = %d %q %q", o.code, o.stdout, o.stderr)
	}
	for _, s := range te.listJSON() {
		if s.Enabled {
			t.Fatalf("%s is still enabled", s.Name)
		}
	}
	o = te.schedule("resume-all", "--json")
	var got struct{ Changed []string }
	if o.code != 0 || json.Unmarshal([]byte(o.stdout), &got) != nil || strings.Join(got.Changed, ",") != "one,three" {
		t.Fatalf("resume-all = %d %q %q", o.code, o.stdout, o.stderr)
	}
	for _, s := range te.listJSON() {
		if s.Enabled != (s.Name != "two") {
			t.Fatalf("%s enabled = %v after resume-all", s.Name, s.Enabled)
		}
	}
	if o := te.schedule("pause-all", "extra"); o.code != 2 || !strings.Contains(o.stderr, "take no argument") {
		t.Fatalf("pause-all with an argument = %d %q", o.code, o.stderr)
	}
}

func TestScheduleCLI_PreviewListsNextFiresOfAPackOrARecurrenceAndSavesNothing(t *testing.T) {
	te := newInitTestEnv(t)
	o := te.schedule("preview", "--at", "07:30", "--days", "mon", "--tz", "UTC")
	if o.code != 0 || !strings.Contains(o.stdout, "weekly mon at 07:30 (UTC)") || strings.Count(o.stdout, "\n") != 1+5 {
		t.Fatalf("preview of a recurrence = %d %q %q", o.code, o.stdout, o.stderr)
	}
	o = te.schedule("preview", "hiring-signals-daily", "--json")
	var entries []struct {
		Key  string
		Next []time.Time
	}
	if o.code != 0 || json.Unmarshal([]byte(o.stdout), &entries) != nil || len(entries) != 1 || entries[0].Key != "signals.collect" || len(entries[0].Next) != 5 {
		t.Fatalf("preview of a pack = %d %q %q", o.code, o.stdout, o.stderr)
	}
	// The pack fires daily at 06:30 local time: consecutive days, whatever a
	// daylight-saving change between them does to the gap.
	first, second := entries[0].Next[0].Local(), entries[0].Next[1].Local()
	if y, m, d := first.AddDate(0, 0, 1).Date(); second.Hour() != 6 || second.Minute() != 30 || first.Hour() != 6 || first.Minute() != 30 ||
		second.Year() != y || second.Month() != m || second.Day() != d {
		t.Fatalf("preview of a pack: fires %v then %v, want 06:30 on consecutive days", first, second)
	}
	if o := te.schedule("preview", "nonexistent"); o.code == 0 || !strings.Contains(o.stderr, "no such pack") {
		t.Fatalf("unknown pack = %d %q", o.code, o.stderr)
	}
	if o := te.schedule("preview", "--rrule", "FREQ=DAILY;\nBYHOUR=3"); o.code == 0 || !strings.Contains(o.stderr, "single line") {
		t.Fatalf("a rule with a line break = %d %q", o.code, o.stderr)
	}
	if o := te.schedule("preview"); o.code != 2 || !strings.Contains(o.stderr, "recurrence is needed") {
		t.Fatalf("no input = %d %q", o.code, o.stderr)
	}
	if got := te.listJSON(); len(got) != 0 {
		t.Fatalf("previewing saved %v", got)
	}
}
