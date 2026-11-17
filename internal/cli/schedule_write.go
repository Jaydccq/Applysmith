package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"applysmith/internal/api"
	"applysmith/internal/api/schedulesapi"
	"applysmith/internal/api/wire"
	"applysmith/internal/scheduler"
	"applysmith/internal/scheduler/jobs"
)

// scheduleFlags are the definition flags of add and edit, plus the flags of
// the other subcommands. set records which were given, so edit changes only
// those.
type scheduleFlags struct {
	name, kind, at, days, rrule, tz, policy string
	disabled, cancel                        bool
	limit                                   int
	pack                                    string
	inputs                                  stringList
	set                                     map[string]bool
}

// scheduleSubFlags lists the definition flags each subcommand takes, so its
// --help shows only those. A subcommand not listed takes none.
var scheduleSubFlags = map[string]string{
	"add":     "name kind at days rrule tz policy disabled input",
	"edit":    "name kind at days rrule tz policy input",
	"delete":  "cancel pack",
	"history": "limit",
	"preview": "at days rrule tz",
}

// register adds the flags the named subcommand takes.
func (f *scheduleFlags) register(fs *flag.FlagSet, sub string) {
	has := func(name string) bool { return slices.Contains(strings.Fields(scheduleSubFlags[sub]), name) }
	if has("name") {
		fs.StringVar(&f.name, "name", "", "schedule name")
	}
	if has("kind") {
		fs.StringVar(&f.kind, "kind", "", "job kind")
	}
	if has("at") {
		fs.StringVar(&f.at, "at", "", "local time HH:MM")
	}
	if has("days") {
		fs.StringVar(&f.days, "days", "", "weekdays, comma separated (sun..sat)")
	}
	if has("rrule") {
		fs.StringVar(&f.rrule, "rrule", "", "RFC 5545 recurrence rule without a start line")
	}
	if has("tz") {
		fs.StringVar(&f.tz, "tz", "", "IANA time zone name")
	}
	if has("policy") {
		fs.StringVar(&f.policy, "policy", "", "missed-fire policy: skip, fire-once or fire-all")
	}
	if has("disabled") {
		fs.BoolVar(&f.disabled, "disabled", false, "create the schedule switched off")
	}
	if has("cancel") {
		fs.BoolVar(&f.cancel, "cancel", false, "cancel a running job before deleting")
	}
	if has("limit") {
		fs.IntVar(&f.limit, "limit", 20, "history rows to show")
	}
	if has("pack") {
		fs.StringVar(&f.pack, "pack", "", "pack id")
	}
	if has("input") {
		fs.Var(&f.inputs, "input", "job input as key=value (repeatable)")
	}
}

// preset builds the recurrence the flags describe, starting from base (the
// current preset on edit, empty on add).
func (f *scheduleFlags) preset(base scheduler.Preset) (scheduler.Preset, error) {
	p := base
	if p.Timezone == "" || f.set["tz"] {
		p.Timezone = f.tz
		if p.Timezone == "" {
			p.Timezone = scheduler.LocalZoneName()
		}
	}
	switch {
	case f.set["rrule"] && (f.set["at"] || f.set["days"]):
		return p, &usageError{"--rrule cannot be combined with --at or --days"}
	case f.set["rrule"]:
		return scheduler.Preset{Kind: scheduler.PresetRRule, RRule: f.rrule, Timezone: p.Timezone}, nil
	}
	if f.set["at"] {
		p.Time, p.RRule = f.at, ""
	}
	if f.set["days"] {
		p.Weekdays = nil
		for _, d := range strings.Split(f.days, ",") {
			if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
				p.Weekdays = append(p.Weekdays, d)
			}
		}
	}
	p.Kind = scheduler.PresetDaily
	if len(p.Weekdays) > 0 {
		p.Kind = scheduler.PresetWeekly
	}
	if p.Time == "" && p.RRule == "" {
		return p, &usageError{"a recurrence is needed: --at <HH:MM> or --rrule <rule>"}
	}
	if p.RRule != "" {
		p.Kind = scheduler.PresetRRule
	}
	return p, nil
}

// inputValues parses --input pairs against the kind's declared fields so
// numbers and booleans arrive typed. An empty value maps to nil (removal).
func inputValues(reg *scheduler.Registry, kind string, pairs []string) (map[string]any, error) {
	fields := map[string]scheduler.FieldType{}
	if k, ok := reg.Get(kind); ok {
		for _, fl := range k.Fields {
			fields[fl.Name] = fl.Type
		}
	}
	out := map[string]any{}
	for _, p := range pairs {
		key, val, ok := strings.Cut(p, "=")
		if !ok || key == "" {
			return nil, &usageError{fmt.Sprintf("--input %q must be key=value", p)}
		}
		switch {
		case val == "":
			out[key] = nil
		case fields[key] == scheduler.FieldInt:
			n, err := strconv.Atoi(val)
			if err != nil {
				return nil, &usageError{fmt.Sprintf("--input %s must be a whole number", key)}
			}
			out[key] = n
		case fields[key] == scheduler.FieldBool:
			b, err := strconv.ParseBool(val)
			if err != nil {
				return nil, &usageError{fmt.Sprintf("--input %s must be true or false", key)}
			}
			out[key] = b
		default:
			out[key] = val
		}
	}
	return out, nil
}

func describeInputs(in map[string]any) string {
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%v", k, in[k])
	}
	return strings.Join(parts, " ")
}

func (e *env) scheduleAdd(ctx context.Context, x *scheduleEnv, pos []string, f *scheduleFlags, asJSON bool) error {
	if len(pos) > 0 {
		return &usageError{"add takes flags only"}
	}
	if f.name == "" || f.kind == "" {
		return &usageError{"--name and --kind are required"}
	}
	preset, err := f.preset(scheduler.Preset{})
	if err != nil {
		return err
	}
	inputs, err := inputValues(x.mgr.Registry(), f.kind, f.inputs)
	if err != nil {
		return err
	}
	for k, v := range inputs {
		if v == nil {
			delete(inputs, k)
		}
	}
	enabled := !f.disabled
	created, err := x.mgr.Create(ctx, scheduler.NewSchedule{Name: f.name, Kind: f.kind, Inputs: inputs, Preset: preset, Enabled: &enabled, Policy: f.policy})
	if err != nil {
		return err
	}
	return e.printCreated(ctx, x, created.ID, asJSON)
}

func (e *env) printCreated(ctx context.Context, x *scheduleEnv, id int64, asJSON bool) error {
	v, err := x.mgr.View(ctx, id)
	if err != nil {
		return err
	}
	if w := jobs.ScreenWarning(v.Kind, v.Inputs); w != "" {
		fmt.Fprintln(e.stderr, "warning: "+w)
	}
	if asJSON {
		return e.jsonOut(schedulesapi.WireSchedule(v))
	}
	e.printSchedule(v)
	return nil
}

func (e *env) scheduleEdit(ctx context.Context, x *scheduleEnv, pos []string, f *scheduleFlags, asJSON bool) error {
	id, err := scheduleID(pos)
	if err != nil {
		return err
	}
	if !(f.set["name"] || f.set["kind"] || f.set["policy"] || f.set["at"] || f.set["days"] || f.set["rrule"] || f.set["tz"] || f.set["input"]) {
		return &usageError{"edit needs at least one flag to change"}
	}
	cur, err := x.mgr.Repo().Get(ctx, id)
	if err != nil {
		return err
	}
	var p scheduler.Patch
	if f.set["name"] {
		p.Name = &f.name
	}
	if f.set["kind"] {
		p.Kind = &f.kind
	}
	if f.set["policy"] {
		p.Policy = &f.policy
	}
	if f.set["at"] || f.set["days"] || f.set["rrule"] || f.set["tz"] {
		preset, err := f.preset(cur.Preset)
		if err != nil {
			return err
		}
		p.Preset = &preset
	}
	if f.set["input"] {
		kind := cur.Kind
		if p.Kind != nil {
			kind = *p.Kind
		}
		changes, err := inputValues(x.mgr.Registry(), kind, f.inputs)
		if err != nil {
			return err
		}
		merged := map[string]any{}
		for k, v := range cur.Inputs {
			merged[k] = v
		}
		for k, v := range changes {
			if v == nil {
				delete(merged, k)
			} else {
				merged[k] = v
			}
		}
		p.Inputs = merged
	}
	if _, err := x.mgr.Update(ctx, id, p); err != nil {
		return err
	}
	return e.printCreated(ctx, x, id, asJSON)
}

func (e *env) scheduleDelete(ctx context.Context, x *scheduleEnv, pos []string, f *scheduleFlags, _ bool) error {
	if f.set["pack"] {
		if len(pos) > 0 {
			return &usageError{"delete --pack takes no schedule id"}
		}
		names, err := x.mgr.DeletePack(ctx, f.pack)
		if err != nil {
			return err
		}
		fmt.Fprintf(e.stdout, "removed %d schedule(s) of pack %s\n", len(names), f.pack)
		for _, n := range names {
			fmt.Fprintf(e.stdout, "  %s\n", n)
		}
		return nil
	}
	id, err := scheduleID(pos)
	if err != nil {
		return err
	}
	if f.cancel {
		if live, err := e.liveHeld(x); err != nil {
			return err
		} else if live {
			if err := e.liveSchedule(ctx, x, http.MethodDelete, "/api/schedules/"+pos[0]+"?cancel=true", nil); err != nil {
				return err
			}
			fmt.Fprintf(e.stdout, "deleted schedule %d\n", id)
			return nil
		}
	}
	if err := x.mgr.Delete(ctx, id); err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "deleted schedule %d\n", id)
	return nil
}

// liveHeld says whether a server holds the workspace.
func (e *env) liveHeld(x *scheduleEnv) (bool, error) { return api.LockHeld(x.ws.StateDir()) }

// liveSchedule calls the running server. A refusal carries the server's own
// message, which names the admission reason.
func (e *env) liveSchedule(ctx context.Context, x *scheduleEnv, method, path string, out any) error {
	srv, err := e.attachLive(ctx, x.ws)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, liveCallTimeout)
	defer cancel()
	resp, err := srv.do(ctx, method, path, nil)
	if err != nil {
		return fmt.Errorf("the running server did not answer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var f wire.Failure
		if json.NewDecoder(resp.Body).Decode(&f) == nil && f.Error.Message != "" {
			return errors.New(f.Error.Message)
		}
		return fmt.Errorf("the running server answered %s", resp.Status)
	}
	if out == nil {
		return nil
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return err
	}
	return json.Unmarshal(env.Data, out)
}

func (e *env) scheduleRun(ctx context.Context, x *scheduleEnv, pos []string, _ *scheduleFlags, asJSON bool) error {
	id, err := scheduleID(pos)
	if err != nil {
		return err
	}
	live, err := e.liveHeld(x)
	if err != nil {
		return err
	}
	if !live {
		return errors.New("no server runs for this workspace, and only a server's scheduler starts jobs; start the app or `applysmith serve`")
	}
	var acc wire.RunAccepted
	if err := e.liveSchedule(ctx, x, http.MethodPost, "/api/schedules/"+strconv.FormatInt(id, 10)+"/run", &acc); err != nil {
		return err
	}
	if asJSON {
		return e.jsonOut(acc)
	}
	fmt.Fprintf(e.stdout, "started run %s; `applysmith schedule history %d` shows how it ends\n", acc.RunID, id)
	return nil
}

func (e *env) scheduleHistory(ctx context.Context, x *scheduleEnv, pos []string, f *scheduleFlags, asJSON bool) error {
	id, err := scheduleID(pos)
	if err != nil {
		return err
	}
	if f.limit < 1 || f.limit > 500 {
		return &usageError{"--limit must be between 1 and 500"}
	}
	rows, err := x.mgr.History(ctx, id, f.limit)
	if err != nil {
		return err
	}
	if asJSON {
		return e.jsonOut(schedulesapi.WireScheduleHistory(rows))
	}
	if len(rows) == 0 {
		fmt.Fprintln(e.stdout, "no history yet")
		return nil
	}
	tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SCHEDULED FOR\tOUTCOME\tREASON\tSUMMARY")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", localTime(r.ScheduledFor), r.Outcome, r.Reason, r.Summary)
	}
	return tw.Flush()
}

// packView is one pack as `packs` shows it.
type packView struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Setup       bool              `json:"setup"`
	Applied     int               `json:"applied"`
	Schedules   int               `json:"schedules"`
	Unavailable map[string]string `json:"unavailable,omitempty"`
}

// packViews describes every pack against the workspace's enabled sources and
// the kinds this build implements.
func packViews(ctx context.Context, x *scheduleEnv) ([]packView, error) {
	env, err := jobs.PackEnvFor(x.ws)
	if err != nil {
		return nil, err
	}
	existing, err := x.mgr.Repo().List(ctx)
	if err != nil {
		return nil, err
	}
	applied := map[string]int{}
	for _, s := range existing {
		if s.PackID != "" {
			applied[s.PackID]++
		}
	}
	var out []packView
	for _, p := range scheduler.Packs() {
		v := packView{ID: p.ID, Title: p.Title, Description: p.Description, Applied: applied[p.ID]}
		for _, id := range scheduler.SetupPacks {
			v.Setup = v.Setup || id == p.ID
		}
		for _, d := range p.Build(env) {
			v.Schedules++
			if _, ok := x.mgr.Registry().Get(d.Kind); !ok {
				if v.Unavailable == nil {
					v.Unavailable = map[string]string{}
				}
				v.Unavailable[d.Key] = jobs.UnavailableReason(d.Kind)
			}
		}
		out = append(out, v)
	}
	return out, nil
}

func (e *env) schedulePacks(ctx context.Context, x *scheduleEnv, pos []string, _ *scheduleFlags, asJSON bool) error {
	if len(pos) > 0 {
		return &usageError{"packs takes no argument"}
	}
	views, err := packViews(ctx, x)
	if err != nil {
		return err
	}
	if asJSON {
		return e.jsonOut(views)
	}
	tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PACK\tSETUP\tSCHEDULES\tAPPLIED\tNOTE")
	for _, v := range views {
		note := v.Description
		if len(v.Unavailable) > 0 {
			note = fmt.Sprintf("%d of %d unavailable: %s", len(v.Unavailable), v.Schedules, firstReason(v.Unavailable))
		}
		fmt.Fprintf(tw, "%s\t%v\t%d\t%d\t%s\n", v.ID, v.Setup, v.Schedules, v.Applied, note)
	}
	return tw.Flush()
}

func firstReason(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys[0] + " " + m[keys[0]]
}

type packResultJSON struct {
	Created     []string          `json:"created"`
	Updated     []string          `json:"updated"`
	Removed     []string          `json:"removed"`
	Unavailable map[string]string `json:"unavailable"`
}

func (e *env) scheduleApplyPack(ctx context.Context, x *scheduleEnv, pos []string, _ *scheduleFlags, asJSON bool) error {
	if len(pos) != 1 {
		return &usageError{"expected one pack id"}
	}
	env, err := jobs.PackEnvFor(x.ws)
	if err != nil {
		return err
	}
	res, err := x.mgr.ApplyPack(ctx, pos[0], env)
	if err != nil {
		return err
	}
	if asJSON {
		out := packResultJSON{append([]string{}, res.Created...), append([]string{}, res.Updated...), append([]string{}, res.Removed...), map[string]string{}}
		for _, u := range res.Unavailable {
			out.Unavailable[u.Key] = jobs.UnavailableReason(u.Kind)
		}
		return e.jsonOut(out)
	}
	fmt.Fprintf(e.stdout, "pack %s: %d created, %d updated, %d removed\n", pos[0], len(res.Created), len(res.Updated), len(res.Removed))
	for _, u := range res.Unavailable {
		fmt.Fprintf(e.stdout, "  not created: %s (%s %s)\n", u.Key, u.Kind, jobs.UnavailableReason(u.Kind))
	}
	return nil
}

// scheduleBulk is pause-all (pause true) or resume-all. Both write the
// database, so they work with no server; a running scheduler sees the change
// on its next tick.
func (e *env) scheduleBulk(pause bool) func(context.Context, *scheduleEnv, []string, *scheduleFlags, bool) error {
	return func(ctx context.Context, x *scheduleEnv, pos []string, _ *scheduleFlags, asJSON bool) error {
		if len(pos) > 0 {
			return &usageError{"pause-all and resume-all take no argument"}
		}
		do, verb := x.mgr.Repo().ResumeAll, "resumed"
		if pause {
			do, verb = x.mgr.Repo().PauseAll, "paused"
		}
		names, err := do(ctx)
		if err != nil {
			return err
		}
		if asJSON {
			return e.jsonOut(wire.SchedulePauseResult{Changed: append([]string{}, names...)})
		}
		fmt.Fprintf(e.stdout, "%d schedule(s) %s\n", len(names), verb)
		for _, n := range names {
			fmt.Fprintf(e.stdout, "  %s\n", n)
		}
		return nil
	}
}

// previewEntry is one schedule of a preview with its next fire times.
type previewEntry struct {
	Key    string      `json:"key,omitempty"`
	Name   string      `json:"name,omitempty"`
	Kind   string      `json:"kind,omitempty"`
	Preset string      `json:"recurrence"`
	Next   []time.Time `json:"next"`
}

// schedulePreview lists the next fire times of a pack's schedules, or of the
// recurrence the flags describe, before anything is applied or saved.
func (e *env) schedulePreview(ctx context.Context, x *scheduleEnv, pos []string, f *scheduleFlags, asJSON bool) error {
	now := time.Now()
	var entries []previewEntry
	add := func(key, name, kind string, p scheduler.Preset) error {
		fires, err := scheduler.PreviewFires(p, now, now, scheduler.UpcomingCount)
		if err != nil {
			return fmt.Errorf("%s: %w", cmp.Or(name, "the recurrence"), err)
		}
		entries = append(entries, previewEntry{key, name, kind, describePreset(p), fires})
		return nil
	}
	switch {
	case len(pos) == 1 && !f.set["at"] && !f.set["days"] && !f.set["rrule"]:
		env, err := jobs.PackEnvFor(x.ws)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(scheduler.Packs(), func(p scheduler.Pack) bool { return p.ID == pos[0] })
		if i < 0 {
			return scheduler.ErrUnknownPack
		}
		for _, d := range scheduler.Packs()[i].Build(env) {
			if err := add(d.Key, d.Name, d.Kind, d.Preset); err != nil {
				return err
			}
		}
	case len(pos) == 0:
		p, err := f.preset(scheduler.Preset{})
		if err != nil {
			return err
		}
		if err := add("", "", "", p); err != nil {
			return err
		}
	default:
		return &usageError{"preview takes one pack id, or --at/--rrule flags"}
	}
	if asJSON {
		return e.jsonOut(entries)
	}
	for _, en := range entries {
		if en.Name != "" {
			fmt.Fprintf(e.stdout, "%s (%s): %s\n", en.Name, en.Kind, en.Preset)
		} else {
			fmt.Fprintf(e.stdout, "%s\n", en.Preset)
		}
		if len(en.Next) == 0 {
			fmt.Fprintln(e.stdout, "  no occurrence in the next 400 days")
		}
		for _, t := range en.Next {
			fmt.Fprintf(e.stdout, "  %s\n", localTime(t))
		}
	}
	return nil
}
