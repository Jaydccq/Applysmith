package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"applysmith/internal/api/schedulesapi"
	"applysmith/internal/scheduler"
	"applysmith/internal/scheduler/jobs"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

const scheduleUsage = `usage:
  applysmith schedule list [--json]
  applysmith schedule show <id> [--json]
  applysmith schedule add --name <name> --kind <kind> --at <HH:MM> [--days mon,tue,...] [--tz <zone>] [--input key=value] [--policy skip|fire-once|fire-all] [--disabled] [--json]
  applysmith schedule add --name <name> --kind <kind> --rrule <rule> [--tz <zone>] [--input key=value] [--policy <policy>] [--disabled] [--json]
  applysmith schedule edit <id> [--name <name>] [--kind <kind>] [--at <HH:MM>] [--days mon,tue,...] [--rrule <rule>] [--tz <zone>] [--input key=value] [--policy <policy>] [--json]
  applysmith schedule enable <id>
  applysmith schedule disable <id>
  applysmith schedule pause-all [--json]
  applysmith schedule resume-all [--json]
  applysmith schedule delete <id> [--cancel]
  applysmith schedule delete --pack <id>
  applysmith schedule run <id>
  applysmith schedule history <id> [--limit N] [--json]
  applysmith schedule packs [--json]
  applysmith schedule apply-pack <id> [--json]
  applysmith schedule preview <pack-id> [--json]
  applysmith schedule preview --at <HH:MM> [--days mon,tue,...] [--tz <zone>] [--json]
  applysmith schedule preview --rrule <rule> [--tz <zone>] [--json]

schedules are stored in the workspace database and work with no server running; they fire once a server runs
run and delete --cancel talk to the running server, because only its scheduler starts and cancels jobs
pause-all switches off every enabled schedule and remembers which; resume-all switches on only those, never one you switched off yourself
preview lists the next fire times of a pack or a recurrence without saving anything
--input is repeatable; key= removes an input on edit; --days with an empty value on edit makes the schedule daily
times are in the --tz zone, by default the machine's
every command takes --workspace <dir>
`

// stringList collects a repeatable flag.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

// scheduleEnv is what the subcommands share: the manager over the workspace
// database and the workspace itself.
type scheduleEnv struct {
	ws  *workspace.Workspace
	mgr *scheduler.Manager
	st  *store.Store
}

func (e *env) schedule(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(e.stderr, scheduleUsage)
		return 2
	}
	sub := args[0]
	cmds := map[string]func(context.Context, *scheduleEnv, []string, *scheduleFlags, bool) error{
		"list": e.scheduleList, "show": e.scheduleShow, "add": e.scheduleAdd, "edit": e.scheduleEdit,
		"enable": e.scheduleSwitch(true), "disable": e.scheduleSwitch(false), "delete": e.scheduleDelete,
		"run": e.scheduleRun, "history": e.scheduleHistory, "packs": e.schedulePacks, "apply-pack": e.scheduleApplyPack,
		"pause-all": e.scheduleBulk(true), "resume-all": e.scheduleBulk(false), "preview": e.schedulePreview,
	}
	run, ok := cmds[sub]
	if !ok {
		fmt.Fprint(e.stderr, scheduleUsage)
		return 2
	}
	name := "schedule " + sub
	fs := e.flags(name)
	wsFlag := fs.String("workspace", "", "workspace directory")
	asJSON := fs.Bool("json", false, "print JSON")
	f := &scheduleFlags{}
	f.register(fs, sub)
	pos, err := parseMixed(fs, args[1:])
	if err != nil {
		return 2
	}
	root, err := e.workspacePath(*wsFlag)
	if err != nil {
		return e.fail(name, err)
	}
	wsp, st, release, err := e.openStore(ctx, root, accessShared)
	if err != nil {
		return e.fail(name, err)
	}
	defer release()
	reg, err := jobs.Catalog()
	if err != nil {
		return e.fail(name, err)
	}
	x := &scheduleEnv{ws: wsp, mgr: scheduler.NewManager(st, reg, time.Now), st: st}
	f.set = map[string]bool{}
	fs.Visit(func(fl *flag.Flag) { f.set[fl.Name] = true })
	if err := run(ctx, x, pos, f, *asJSON); err != nil {
		return e.scheduleFail(name, err)
	}
	return 0
}

func (e *env) scheduleFail(name string, err error) int {
	var usage *usageError
	var inv *scheduler.InvalidError
	switch {
	case errors.As(err, &usage):
		fmt.Fprintf(e.stderr, "applysmith %s: %s\n\n%s", name, usage.msg, scheduleUsage)
		return 2
	case errors.As(err, &inv):
		fmt.Fprintf(e.stderr, "applysmith %s: %s: %s\n", name, inv.Field, inv.Message)
		return 2
	case errors.Is(err, scheduler.ErrNotFound):
		return e.fail(name, errors.New("no schedule with that id; `applysmith schedule list` shows them"))
	case errors.Is(err, scheduler.ErrUnknownPack):
		return e.fail(name, errors.New("no such pack; `applysmith schedule packs` lists them"))
	case errors.Is(err, scheduler.ErrRunning):
		return e.fail(name, errors.New("the schedule has a running job; pass --cancel to stop it first (needs the running server)"))
	}
	return e.fail(name, err)
}

func scheduleID(pos []string) (int64, error) {
	if len(pos) != 1 {
		return 0, &usageError{"expected one schedule id"}
	}
	id, err := strconv.ParseInt(pos[0], 10, 64)
	if err != nil || id < 1 {
		return 0, &usageError{fmt.Sprintf("%q is not a schedule id; ids are positive integers", pos[0])}
	}
	return id, nil
}

func (e *env) scheduleList(ctx context.Context, x *scheduleEnv, pos []string, _ *scheduleFlags, asJSON bool) error {
	if len(pos) > 0 {
		return &usageError{"list takes no argument"}
	}
	if asJSON {
		// No scheduler runs in the command, so the document lists no timers.
		out, err := schedulesapi.List(ctx, x.mgr, scheduler.NewRepo(x.st, time.Now), nil, time.Now())
		if err != nil {
			return err
		}
		return e.jsonOut(out)
	}
	views, err := x.mgr.List(ctx)
	if err != nil {
		return err
	}
	if len(views) == 0 {
		fmt.Fprintln(e.stdout, "no schedules; `applysmith schedule packs` lists the presets, `applysmith schedule add` makes one")
		return nil
	}
	tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tKIND\tENABLED\tNEXT\tLAST")
	for _, v := range views {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%v\t%s\t%s\n", v.ID, v.Name, v.Kind, v.Enabled, nextOf(v), lastOf(v.Schedule))
	}
	return tw.Flush()
}

func nextOf(v scheduler.View) string {
	if len(v.Next) == 0 {
		return "-"
	}
	return localTime(v.Next[0])
}

func lastOf(s scheduler.Schedule) string {
	switch {
	case s.LastResult == "":
		return "-"
	case s.LastResult == scheduler.ResultSkipped && s.LastSkipReason != "":
		return "skipped (" + s.LastSkipReason + ")"
	}
	return s.LastResult
}

func (e *env) scheduleShow(ctx context.Context, x *scheduleEnv, pos []string, _ *scheduleFlags, asJSON bool) error {
	id, err := scheduleID(pos)
	if err != nil {
		return err
	}
	v, err := x.mgr.View(ctx, id)
	if err != nil {
		return err
	}
	if asJSON {
		return e.jsonOut(schedulesapi.WireSchedule(v))
	}
	e.printSchedule(v)
	// The newest history row, manual runs included; the schedule's own cursor
	// starts at creation and only scheduled firings move it.
	if h, err := x.mgr.History(ctx, id, 1); err == nil && len(h) > 0 {
		fmt.Fprintf(e.stdout, "last run: %s\n", localTime(h[0].ScheduledFor))
	}
	return nil
}

func (e *env) printSchedule(v scheduler.View) {
	w := e.stdout
	fmt.Fprintf(w, "id: %d\nname: %s\nkind: %s\nenabled: %v\npolicy: %s\n", v.ID, v.Name, v.Kind, v.Enabled, v.Policy)
	fmt.Fprintf(w, "recurrence: %s\n", describePreset(v.Preset))
	if len(v.Inputs) > 0 {
		fmt.Fprintf(w, "inputs: %s\n", describeInputs(v.Inputs))
	}
	if v.PackID != "" {
		fmt.Fprintf(w, "pack: %s (%s)\n", v.PackID, v.PackKey)
	}
	fmt.Fprintf(w, "last result: %s\n", lastOf(v.Schedule))
	for i, t := range v.Next {
		label := "next"
		if i > 0 {
			label = "then"
		}
		fmt.Fprintf(w, "%s: %s\n", label, localTime(t))
	}
	if len(v.Next) == 0 && !v.Enabled {
		fmt.Fprintln(w, "next: none while disabled")
	}
	if v.Problem != "" {
		fmt.Fprintf(w, "problem: %s\n", v.Problem)
	}
}

func describePreset(p scheduler.Preset) string {
	switch p.Kind {
	case scheduler.PresetRRule:
		return p.RRule + " (" + p.Timezone + ")"
	case scheduler.PresetWeekly:
		return "weekly " + strings.Join(p.Weekdays, ",") + " at " + p.Time + " (" + p.Timezone + ")"
	}
	return "daily at " + p.Time + " (" + p.Timezone + ")"
}

func (e *env) scheduleSwitch(on bool) func(context.Context, *scheduleEnv, []string, *scheduleFlags, bool) error {
	return func(ctx context.Context, x *scheduleEnv, pos []string, _ *scheduleFlags, _ bool) error {
		id, err := scheduleID(pos)
		if err != nil {
			return err
		}
		if _, err := x.mgr.Update(ctx, id, scheduler.Patch{Enabled: &on}); err != nil {
			return err
		}
		fmt.Fprintf(e.stdout, "schedule %d %s\n", id, map[bool]string{true: "enabled", false: "disabled"}[on])
		return nil
	}
}

func (e *env) jsonOut(v any) error {
	if e.printJSON(v) != 0 {
		return errors.New("the output could not be written")
	}
	return nil
}
