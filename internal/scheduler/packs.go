package scheduler

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// SetupPacks are the packs the setup flow offers; none creates nothing. The
// others are applied later from the Schedules tab or with `applysmith schedule
// apply-pack`.
var SetupPacks = []string{"none", "daily", "twice-daily"}

// PackSource is a discovery source that is enabled in the user's portals file.
type PackSource struct {
	Name    string
	Title   string // the name shown to the user, which the schedule's name carries
	Browser bool   // drives the scan window
}

// PackEnv is what a pack is built from: the machine's time zone and the
// enabled discovery sources, in catalog order.
type PackEnv struct {
	Zone    string
	Sources []PackSource
}

// PackSchedule is one schedule of a pack, defined as data.
type PackSchedule struct {
	Key    string
	Name   string
	Kind   string
	Inputs map[string]any
	Preset Preset
	Policy string
}

// Pack is a named list of schedules. It holds no personal value and no kind
// that submits an application.
type Pack struct {
	ID          string
	Title       string
	Description string
	Build       func(PackEnv) []PackSchedule
}

// Packs lists the packs the product ships.
func Packs() []Pack {
	return []Pack{
		{ID: "daily", Title: "Daily scan", Description: "One scan per enabled discovery source, starting at 07:00 local time, 10 minutes apart, then the evaluation of the postings they left waiting.",
			Build: func(e PackEnv) []PackSchedule { return scanRound(e, "Daily scan", "", "07:00") }},
		{ID: "twice-daily", Title: "Twice-daily scan", Description: "The daily scans and the evaluation after them, again from 17:00 local time.",
			Build: func(e PackEnv) []PackSchedule {
				return append(scanRound(e, "Twice-daily scan", "am", "07:00"), scanRound(e, "Twice-daily scan", "pm", "17:00")...)
			}},
		{ID: "browser-boards", Title: "Browser boards", Description: "Weekday scans of the browser-driven sources from 09:00 local time, 20 minutes apart.",
			Build: func(e PackEnv) []PackSchedule {
				var browser []PackSource
				for _, s := range e.Sources {
					if s.Browser {
						browser = append(browser, s)
					}
				}
				e.Sources = browser
				return sourceScans(e, "Browser board scan", "", "09:00", 20, []string{"mon", "tue", "wed", "thu", "fri"})
			}},
		{ID: "hiring-signals-daily", Title: "Hiring signals", Description: "Collects hiring signals every day at 06:30.",
			Build: func(e PackEnv) []PackSchedule {
				return []PackSchedule{{Key: "signals.collect", Name: "Hiring signals", Kind: "signals.collect", Policy: PolicyFireOnce,
					Preset: Preset{Kind: PresetDaily, Time: "06:30", Timezone: e.Zone}}}
			}},
		{ID: "memory-nightly", Title: "Nightly memory", Description: "Consolidates the agent's memory every night at 03:30.",
			Build: func(e PackEnv) []PackSchedule {
				return []PackSchedule{{Key: "memory.consolidate", Name: "Nightly memory consolidation", Kind: "memory.consolidate", Policy: PolicyFireOnce,
					Preset: Preset{Kind: PresetDaily, Time: "03:30", Timezone: e.Zone}}}
			}},
		{ID: "agent-improve-nightly", Title: "Ask Agent self-improvement", Description: "Every night at 04:10, replays the Ask Agent turns that went wrong and adopts better guidance when it wins.",
			Build: func(e PackEnv) []PackSchedule {
				return []PackSchedule{{Key: "agent.improve", Name: "Nightly Ask Agent self-improvement", Kind: "agent.improve", Policy: PolicyFireOnce,
					Preset: Preset{Kind: PresetDaily, Time: "04:10", Timezone: e.Zone}}}
			}},
		{ID: "maintenance", Title: "Maintenance", Description: "Weekly registry and price checks, a daily cost report, a weekly liveness audit and a monthly archive sweep that only reports until its apply input is set.",
			Build: func(e PackEnv) []PackSchedule {
				weekly := func(day, at string) Preset {
					return Preset{Kind: PresetWeekly, Time: at, Weekdays: []string{day}, Timezone: e.Zone}
				}
				return []PackSchedule{
					{Key: "registry.refresh", Name: "Weekly registry refresh", Kind: "registry.refresh", Policy: PolicyFireOnce, Preset: weekly("sun", "05:00")},
					{Key: "price.check", Name: "Weekly price check", Kind: "price.check", Policy: PolicyFireOnce, Preset: weekly("sun", "05:20")},
					{Key: "cost.report", Name: "Daily cost report", Kind: "cost.report", Policy: PolicyFireOnce,
						Preset: Preset{Kind: PresetDaily, Time: "07:10", Timezone: e.Zone}},
					{Key: "liveness.audit", Name: "Weekly liveness audit", Kind: "liveness.audit", Policy: PolicyFireOnce, Preset: weekly("sat", "05:00")},
					{Key: "archive.sweep", Name: "Monthly archive sweep", Kind: "archive.sweep", Policy: PolicyFireOnce,
						Preset: Preset{Kind: PresetRRule, RRule: "FREQ=MONTHLY;BYMONTHDAY=1;BYHOUR=3;BYMINUTE=50;BYSECOND=0", Timezone: e.Zone}},
				}
			}},
	}
}

// scanRound is one round of the daily packs: a scan per source 10 minutes
// apart, then inbox.evaluate in the next slot, so the rows the scans leave
// waiting go to evaluation. A scan still running holds llm:evaluation, which
// defers the evaluation until it ends.
func scanRound(e PackEnv, title, slot, start string) []PackSchedule {
	out := sourceScans(e, title, slot, start, 10, nil)
	if len(out) == 0 {
		return out
	}
	key, name := "inbox.evaluate", title+" · inbox evaluation"
	if slot != "" {
		key += "." + slot
		name += " · " + slotWords[slot]
	}
	p := Preset{Kind: PresetDaily, Time: clockAfter(start, len(out)*10), Timezone: e.Zone}
	return append(out, PackSchedule{Key: key, Name: name, Kind: "inbox.evaluate", Preset: p, Policy: PolicyFireOnce})
}

var slotWords = map[string]string{"am": "morning", "pm": "evening"}

// clockAfter is the HH:MM that is minutes after start.
func clockAfter(start string, minutes int) string {
	h, m, _ := parseClock(start)
	at := h*60 + m + minutes
	return fmt.Sprintf("%02d:%02d", at/60%24, at%60)
}

// sourceScans builds one scan per source, each `step` minutes after the
// previous, from the start time. With weekdays set the preset is weekly.
func sourceScans(e PackEnv, title, slot, start string, step int, weekdays []string) []PackSchedule {
	var out []PackSchedule
	for i, src := range e.Sources {
		p := Preset{Kind: PresetDaily, Time: clockAfter(start, i*step), Timezone: e.Zone}
		if len(weekdays) > 0 {
			p.Kind, p.Weekdays = PresetWeekly, weekdays
		}
		key, name := "discover."+src.Name, title+" · "+src.Title
		if slot != "" {
			key += "." + slot
			name += " · " + slotWords[slot]
		}
		out = append(out, PackSchedule{Key: key, Name: name, Kind: "discover." + src.Name, Preset: p, Policy: packPolicy(weekdays)})
	}
	return out
}

func packPolicy(weekdays []string) string {
	if len(weekdays) > 0 {
		return PolicySkip // a browser window that opens after a wake is not wanted
	}
	return PolicyFireOnce
}

// PackResult says what applying a pack did.
type PackResult struct {
	Created     []string
	Updated     []string
	Removed     []string
	Unavailable []PackUnavailable
}

// PackUnavailable is an entry of a pack whose job kind does not exist yet.
type PackUnavailable struct {
	Key    string
	Kind   string
	Reason string
}

// ApplyPack creates the pack's schedules, marked with the pack id and a key.
// Applying again replaces only the schedules that carry the marker with the
// pack's current definitions and touches nothing else. A schedule the user
// edited lost its marker, so it is left alone and the pack creates a fresh one
// for its key. An entry whose kind is not registered is reported, not created.
func (m *Manager) ApplyPack(ctx context.Context, id string, env PackEnv) (PackResult, error) {
	pack, ok := findPack(id)
	if !ok {
		return PackResult{}, ErrUnknownPack
	}
	var res PackResult
	var defs []PackSchedule
	for _, d := range pack.Build(env) {
		if _, ok := m.reg.Get(d.Kind); !ok {
			res.Unavailable = append(res.Unavailable, PackUnavailable{Key: d.Key, Kind: d.Kind, Reason: "the job kind " + d.Kind + " is not available yet"})
			continue
		}
		defs = append(defs, d)
	}
	all, err := m.repo.List(ctx)
	if err != nil {
		return res, err
	}
	marked := map[string]Schedule{}
	for _, s := range all {
		if s.PackID == id {
			marked[s.PackKey] = s
		}
	}
	creating := 0
	for _, d := range defs {
		if _, ok := marked[d.Key]; !ok {
			creating++
		}
	}
	removing := 0
	for key := range marked {
		if !slices.ContainsFunc(defs, func(d PackSchedule) bool { return d.Key == key }) {
			removing++
		}
	}
	if len(all)+creating-removing > MaxSchedules {
		return res, ErrCapacity
	}
	taken := map[string]int64{}
	for _, s := range all {
		taken[s.Name] = s.ID
	}
	for key, s := range marked {
		if !slices.ContainsFunc(defs, func(d PackSchedule) bool { return d.Key == key }) {
			if err := m.repo.Delete(ctx, s.ID); err != nil {
				return res, err
			}
			delete(taken, s.Name)
			res.Removed = append(res.Removed, s.Name)
		}
	}
	for _, d := range defs {
		if cur, ok := marked[d.Key]; ok {
			name := freeName(taken, d.Name, cur.ID)
			if _, err := m.repo.Update(ctx, cur.ID, func(s *Schedule) error {
				s.Name, s.Kind, s.Inputs, s.Preset, s.Policy = name, d.Kind, nonNil(d.Inputs), d.Preset, d.Policy
				return m.validate(s)
			}); err != nil {
				return res, err
			}
			taken[name] = cur.ID
			res.Updated = append(res.Updated, name)
			continue
		}
		name := freeName(taken, d.Name, 0)
		if _, err := m.create(ctx, NewSchedule{Name: name, Kind: d.Kind, Inputs: nonNil(d.Inputs), Preset: d.Preset, Policy: d.Policy}, id, d.Key); err != nil {
			return res, err
		}
		taken[name] = -1
		res.Created = append(res.Created, name)
	}
	return res, nil
}

// ResumeGmailSync switches back on the gmail.sync schedules PauseGmailSync
// switched off. Connecting Gmail adds no schedule: the server's Gmail watcher
// reads mail as it arrives, and a gmail.sync schedule is one the user made.
func (m *Manager) ResumeGmailSync(ctx context.Context) ([]string, error) {
	return m.repo.ResumeKind(ctx, "gmail.sync")
}

// PauseGmailSync is what disconnecting Gmail does to its schedules, which
// cannot run without it: every gmail.sync schedule is switched off for
// ResumeGmailSync to switch back on. It returns the names it changed.
func (m *Manager) PauseGmailSync(ctx context.Context) ([]string, error) {
	return m.repo.PauseKind(ctx, "gmail.sync")
}

func nonNil(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// freeName returns name, or name with a suffix when another schedule has it.
func freeName(taken map[string]int64, name string, self int64) string {
	try := name
	for n := 1; ; n++ {
		if id, used := taken[try]; !used || id == self {
			return try
		}
		try = name + " (pack)"
		if n > 1 {
			try = fmt.Sprintf("%s (pack %d)", name, n)
		}
	}
}

func findPack(id string) (Pack, bool) {
	for _, p := range Packs() {
		if p.ID == id {
			return p, true
		}
	}
	return Pack{}, false
}

// DeletePack removes the schedules that carry a pack's marker and returns
// their names.
func (m *Manager) DeletePack(ctx context.Context, id string) ([]string, error) {
	if _, ok := findPack(id); !ok {
		return nil, ErrUnknownPack
	}
	marked, err := m.repo.ListPack(ctx, id)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, s := range marked {
		if err := m.repo.Delete(ctx, s.ID); err != nil {
			return names, err
		}
		names = append(names, s.Name)
	}
	return names, nil
}

// LocalZoneName is the IANA name of the machine's time zone, or UTC when it
// cannot be determined. Pack times are interpreted in it.
func LocalZoneName() string {
	if tz := os.Getenv("TZ"); tz != "" && tz != "Local" {
		if _, err := time.LoadLocation(strings.TrimPrefix(tz, ":")); err == nil {
			return strings.TrimPrefix(tz, ":")
		}
	}
	if name := time.Local.String(); name != "Local" && name != "" {
		return name
	}
	if target, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
		if _, after, ok := strings.Cut(filepath.ToSlash(target), "zoneinfo/"); ok {
			if _, err := time.LoadLocation(after); err == nil {
				return after
			}
		}
	}
	return "UTC"
}
