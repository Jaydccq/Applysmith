// Package schedulesapi serves the schedules in the local API: the user's
// schedules, their runs and history, the job kinds and the schedule packs.
// internal/api registers the handlers.
package schedulesapi

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/notify"
	"applysmith/internal/scheduler"
)

// The schedule routes. Management writes SQLite through the scheduler's
// manager; a manual run goes through the same admission checks as a scheduled
// one. Built-in timers are listed read-only.

// Deps is what the schedule handlers use from the server.
type Deps struct {
	Scheduler func() (*scheduler.Scheduler, error)
	// PackEnv is what the packs are built from, read from the user's portals
	// file.
	PackEnv     func() (scheduler.PackEnv, *apikit.Error)
	SourceTitle func(name string) string // a scan source's readable name
	Notify      func(topics ...string)
	Now         func() time.Time
}

// Handlers serves the schedule routes.
type Handlers struct{ Deps }

func New(d Deps) *Handlers { return &Handlers{Deps: d} }

// ScheduleError maps a scheduler failure onto the closed set of API errors.
func ScheduleError(err error) error {
	var inv *scheduler.InvalidError
	var refused *scheduler.RefusedError
	switch {
	case errors.As(err, &inv):
		return apikit.InvalidField(inv.Field, "%s", inv.Message)
	case errors.As(err, &refused):
		return apikit.WithReason(apikit.NewErr(wire.CodeConflict, "%s", refused.Error()), refused.Reason)
	case errors.Is(err, scheduler.ErrNotFound), errors.Is(err, scheduler.ErrUnknownPack):
		return apikit.NewErr(wire.CodeNotFound, "%s", err.Error())
	case errors.Is(err, scheduler.ErrNameConflict), errors.Is(err, scheduler.ErrCapacity), errors.Is(err, scheduler.ErrRunning), errors.Is(err, scheduler.ErrDecided):
		return apikit.NewErr(wire.CodeConflict, "%s", err.Error())
	case errors.Is(err, scheduler.ErrNotRunning):
		return apikit.Unavailable("the scheduler is not running")
	}
	return err
}

func toSchedulePreset(p scheduler.Preset) wire.SchedulePreset {
	return wire.SchedulePreset{Kind: p.Kind, Time: p.Time, Weekdays: p.Weekdays, RRule: p.RRule, Timezone: p.Timezone}
}

func FromPreset(p wire.SchedulePreset) scheduler.Preset {
	return scheduler.Preset{Kind: p.Kind, Time: p.Time, Weekdays: p.Weekdays, RRule: p.RRule, Timezone: p.Timezone}
}

// WireSchedule is the wire form of a schedule view, shared with the CLI.
func WireSchedule(v scheduler.View) wire.Schedule {
	out := wire.Schedule{
		ID: apikit.Safe64(v.ID), Name: v.Name, Kind: v.Kind, Inputs: v.Inputs, Preset: toSchedulePreset(v.Preset), Enabled: v.Enabled,
		MissedPolicy: v.Policy, DTStart: wire.Time{Time: v.DTStart}, PackID: v.PackID, PackKey: v.PackKey,
		LastFiredAt: apikit.TimePtr(v.LastFiredAt), LastEvaluatedAt: apikit.TimePtr(v.LastEvaluatedAt), ActiveRunID: v.ActiveRunID, LastRunID: v.LastRunID,
		LastResult: v.LastResult, LastSkipReason: v.LastSkipReason, LastResultAt: apikit.TimePtr(v.LastResultAt), LastReason: v.LastReason, LastDetail: v.LastDetail, CreatedAt: wire.Time{Time: v.CreatedAt}, UpdatedAt: wire.Time{Time: v.UpdatedAt},
		Next: make([]wire.Time, len(v.Next)), UnknownKind: v.UnknownKind, Problem: v.Problem,
	}
	if out.Inputs == nil {
		out.Inputs = map[string]any{}
	}
	for i, t := range v.Next {
		out.Next[i] = wire.Time{Time: t}
	}
	return out
}

func (s *Handlers) scheduleList(c *apikit.Call) (wire.ScheduleList, error) {
	sc, err := s.Scheduler()
	if err != nil {
		return wire.ScheduleList{}, err
	}
	return List(c.R.Context(), sc.Manager, sc.Repo(), sc.Timers(c.R.Context()), s.Now())
}

// List is the document GET /api/schedules answers: the schedules with what
// needs attention and who paused them, and the built-in timers given.
func List(ctx context.Context, mgr *scheduler.Manager, repo *scheduler.Repo, timers []scheduler.TimerInfo, now time.Time) (wire.ScheduleList, error) {
	views, err := mgr.List(ctx)
	if err != nil {
		return wire.ScheduleList{}, err
	}
	paused, err := repo.Paused(ctx)
	if err != nil {
		return wire.ScheduleList{}, err
	}
	found, err := repo.Hygiene(ctx, now)
	if err != nil {
		return wire.ScheduleList{}, err
	}
	attention := map[int64][]string{}
	for _, f := range found {
		for _, id := range f.ScheduleIDs {
			attention[id] = append(attention[id], f.Action)
		}
	}
	out := wire.ScheduleList{Schedules: make([]wire.Schedule, len(views)), Timers: []wire.ScheduleTimer{}}
	for i, v := range views {
		out.Schedules[i] = WireSchedule(v)
		out.Schedules[i].Attention = attention[v.ID]
		if by, ok := paused[v.ID]; ok {
			out.Schedules[i].PausedBy = cmp.Or(by, "all")
			if by == "" {
				out.PausedAll++
			}
		}
	}
	for _, t := range timers {
		out.Timers = append(out.Timers, wire.ScheduleTimer{Name: t.Name, Description: t.Describe, Active: t.Active, Reason: t.Reason})
	}
	return out, nil
}

func (s *Handlers) List(c *apikit.Call) (apikit.Reply, error) {
	list, err := s.scheduleList(c)
	return apikit.Reply{Data: list}, err
}

func (s *Handlers) Create(c *apikit.Call) (apikit.Reply, error) {
	sc, err := s.Scheduler()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.ScheduleCreate
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	n := scheduler.NewSchedule{Name: req.Name, Kind: req.Kind, Inputs: req.Inputs, Preset: FromPreset(req.Preset), Enabled: req.Enabled, Policy: req.MissedPolicy}
	created, err := sc.Manager.Create(c.R.Context(), n)
	if err != nil {
		return apikit.Reply{}, ScheduleError(err)
	}
	view, err := sc.Manager.View(c.R.Context(), created.ID)
	if err != nil {
		return apikit.Reply{}, ScheduleError(err)
	}
	s.Notify("schedules")
	return apikit.Reply{Status: http.StatusCreated, Location: "/api/schedules/" + strconv.FormatInt(created.ID, 10), Data: WireSchedule(view)}, nil
}

func (s *Handlers) Get(c *apikit.Call) (apikit.Reply, error) {
	sc, err := s.Scheduler()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	view, err := sc.Manager.View(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, ScheduleError(err)
	}
	return apikit.Reply{Data: WireSchedule(view)}, nil
}

func (s *Handlers) Patch(c *apikit.Call) (apikit.Reply, error) {
	sc, err := s.Scheduler()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	var req wire.SchedulePatch
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	p := scheduler.Patch{Name: req.Name, Kind: req.Kind, Inputs: req.Inputs, Enabled: req.Enabled, Policy: req.MissedPolicy}
	if req.Preset != nil {
		pre := FromPreset(*req.Preset)
		p.Preset = &pre
	}
	if _, err := sc.Manager.Update(c.R.Context(), id, p); err != nil {
		return apikit.Reply{}, ScheduleError(err)
	}
	view, err := sc.Manager.View(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, ScheduleError(err)
	}
	s.Notify("schedules")
	return apikit.Reply{Data: WireSchedule(view)}, nil
}

// Delete removes a schedule; with cancel=true a running job is
// cancelled first, without it a running job is a conflict.
func (s *Handlers) Delete(c *apikit.Call) (apikit.Reply, error) {
	sc, err := s.Scheduler()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	q := apikit.QueryOf(c)
	cancel := q.Boolean("cancel")
	if q.Err != nil {
		return apikit.Reply{}, q.Err
	}
	if err := sc.Remove(c.R.Context(), id, cancel); err != nil {
		return apikit.Reply{}, ScheduleError(err)
	}
	s.Notify("schedules")
	list, err := s.scheduleList(c)
	return apikit.Reply{Data: list}, err
}

// PauseAll switches off every enabled schedule and records which, so
// resume-all switches back on only those.
func (s *Handlers) PauseAll(c *apikit.Call) (apikit.Reply, error) {
	return s.bulkSchedules(c, (*scheduler.Repo).PauseAll)
}

// ResumeAll switches back on the schedules the last pause-all
// switched off; one the user switched on or deleted since is left alone.
func (s *Handlers) ResumeAll(c *apikit.Call) (apikit.Reply, error) {
	return s.bulkSchedules(c, (*scheduler.Repo).ResumeAll)
}

func (s *Handlers) bulkSchedules(c *apikit.Call, do func(*scheduler.Repo, context.Context) ([]string, error)) (apikit.Reply, error) {
	sc, err := s.Scheduler()
	if err != nil {
		return apikit.Reply{}, err
	}
	names, err := do(sc.Repo(), c.R.Context())
	if err != nil {
		return apikit.Reply{}, ScheduleError(err)
	}
	s.Notify("schedules")
	return apikit.Reply{Data: wire.SchedulePauseResult{Changed: apikit.NonNil(names)}}, nil
}

func (s *Handlers) Run(c *apikit.Call) (apikit.Reply, error) {
	sc, err := s.Scheduler()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	runID, err := sc.RunNow(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, ScheduleError(err)
	}
	return apikit.Reply{Status: http.StatusAccepted, Location: "/api/runs/" + runID, Data: wire.RunAccepted{RunID: runID}}, nil
}

func (s *Handlers) History(c *apikit.Call) (apikit.Reply, error) {
	sc, err := s.Scheduler()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	q := apikit.QueryOf(c)
	limit := q.Integer("limit", 50)
	if q.Err == nil && (limit < 1 || limit > 500) {
		q.Fail(apikit.InvalidField("limit", "limit must be between 1 and 500"))
	}
	if q.Err != nil {
		return apikit.Reply{}, q.Err
	}
	rows, err := sc.Manager.History(c.R.Context(), id, limit)
	if err != nil {
		return apikit.Reply{}, ScheduleError(err)
	}
	return apikit.Reply{Data: WireScheduleHistory(rows)}, nil
}

func WireScheduleHistory(rows []scheduler.HistoryRow) wire.ScheduleHistory {
	out := wire.ScheduleHistory{History: make([]wire.ScheduleHistoryRow, len(rows))}
	for i, r := range rows {
		out.History[i] = wire.ScheduleHistoryRow{ID: apikit.Safe64(r.ID), ScheduledFor: wire.Time{Time: r.ScheduledFor}, Outcome: r.Outcome, Reason: r.Reason,
			Detail: r.Detail, RunID: r.RunID, StartedAt: apikit.TimePtr(r.StartedAt), FinishedAt: apikit.TimePtr(r.FinishedAt), Attempts: apikit.Safe(r.Attempts),
			Summary: r.Summary, CreatedAt: wire.Time{Time: r.CreatedAt}}
	}
	return out
}

func (s *Handlers) Kinds(c *apikit.Call) (apikit.Reply, error) {
	sc, err := s.Scheduler()
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.ScheduleKindList{Kinds: []wire.ScheduleKind{}}
	for _, k := range sc.Registry().All() {
		wk := wire.ScheduleKind{Name: k.Name, Title: s.kindTitle(k.Name), Doc: k.Doc, Network: k.Network, Artifact: k.Artifact, Leases: []string{}, Inputs: []wire.ScheduleField{}}
		for _, l := range k.Leases {
			wk.Leases = append(wk.Leases, string(l))
		}
		for _, f := range k.Fields {
			wk.Inputs = append(wk.Inputs, wire.ScheduleField{Name: f.Name, Type: string(f.Type), Enum: f.Enum, Min: apikit.Safe(f.Min), Max: apikit.Safe(f.Max), Doc: f.Doc})
		}
		out.Kinds = append(out.Kinds, wk)
	}
	return apikit.Reply{Data: out}, nil
}

// kindTitle is a job kind's human name: a scan's is its source's title, any
// other kind's the one the runs board uses, else the kind itself.
func (s *Handlers) kindTitle(kind string) string {
	if name, ok := strings.CutPrefix(kind, "discover."); ok {
		return "Scan " + s.SourceTitle(name)
	}
	return cmp.Or(notify.TaskTitle(kind), kind)
}

// packPreviewCount is how many fire times each entry of a pack preview shows.
const packPreviewCount = 3

func (s *Handlers) ListPacks(c *apikit.Call) (apikit.Reply, error) {
	sc, err := s.Scheduler()
	if err != nil {
		return apikit.Reply{}, err
	}
	env, e := s.PackEnv()
	if e != nil {
		return apikit.Reply{}, e
	}
	existing, err := sc.Manager.Repo().List(c.R.Context())
	if err != nil {
		return apikit.Reply{}, err
	}
	applied := map[string]int{}
	for _, x := range existing {
		if x.PackID != "" {
			applied[x.PackID]++
		}
	}
	out := wire.SchedulePackList{Packs: []wire.SchedulePack{}}
	for _, p := range scheduler.Packs() {
		wp := wire.SchedulePack{ID: p.ID, Title: p.Title, Description: p.Description, Applied: apikit.Safe(applied[p.ID]), Schedules: []wire.SchedulePackEntry{}}
		for _, id := range scheduler.SetupPacks {
			wp.Setup = wp.Setup || id == p.ID
		}
		for _, d := range p.Build(env) {
			_, available := sc.Registry().Get(d.Kind)
			entry := wire.SchedulePackEntry{Key: d.Key, Name: d.Name, Kind: d.Kind, Preset: toSchedulePreset(d.Preset),
				Policy: d.Policy, Available: available, Next: []wire.Time{}}
			// The preview of applying the pack now: an entry whose preset does
			// not validate lists no fire times.
			now := s.Now()
			if fires, err := scheduler.PreviewFires(d.Preset, now, now, packPreviewCount); err == nil {
				for _, t := range fires {
					entry.Next = append(entry.Next, wire.Time{Time: t.UTC()})
				}
			}
			wp.Schedules = append(wp.Schedules, entry)
		}
		out.Packs = append(out.Packs, wp)
	}
	return apikit.Reply{Data: out}, nil
}

func (s *Handlers) ApplyPack(c *apikit.Call) (apikit.Reply, error) {
	sc, err := s.Scheduler()
	if err != nil {
		return apikit.Reply{}, err
	}
	env, e := s.PackEnv()
	if e != nil {
		return apikit.Reply{}, e
	}
	res, err := sc.Manager.ApplyPack(c.R.Context(), c.R.PathValue("id"), env)
	if err != nil {
		return apikit.Reply{}, ScheduleError(err)
	}
	s.Notify("schedules")
	out := wire.SchedulePackResult{Created: apikit.NonNil(res.Created), Updated: apikit.NonNil(res.Updated), Removed: apikit.NonNil(res.Removed),
		Unavailable: []wire.SchedulePackUnavailable{}}
	for _, u := range res.Unavailable {
		out.Unavailable = append(out.Unavailable, wire.SchedulePackUnavailable{Key: u.Key, Kind: u.Kind, Reason: u.Reason})
	}
	return apikit.Reply{Data: out}, nil
}

func (s *Handlers) DeletePack(c *apikit.Call) (apikit.Reply, error) {
	sc, err := s.Scheduler()
	if err != nil {
		return apikit.Reply{}, err
	}
	names, err := sc.Manager.DeletePack(c.R.Context(), c.R.PathValue("id"))
	if err != nil {
		return apikit.Reply{}, ScheduleError(err)
	}
	s.Notify("schedules")
	return apikit.Reply{Data: wire.SchedulePackDeleted{Removed: apikit.NonNil(names)}}, nil
}
