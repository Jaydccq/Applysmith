package onboarding

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"applysmith/internal/discovery"
	"applysmith/internal/scheduler"
)

// The schedule leaf of the discovery step. Having no schedule is a choice the
// product never makes for the user, so it warns and never fails.
func discoveryChecks() []Check {
	return []Check{{ID: "discovery.schedules", Run: checkDiscoverySchedules}, {ID: "discovery.registry", Run: checkRegistrySeeded},
		{ID: "discovery.schedule-hygiene", Run: checkScheduleHygiene}, {ID: "discovery.schedule-audit", Run: checkScheduleAudit}}
}

// checkScheduleHygiene reports duplicate schedules, disabled ones whose last
// run failed, dead lanes and schedules that never ran. It warns and never
// fails, and changes nothing: the schedules are the user's.
func checkScheduleHygiene(ctx context.Context, env *Env) Outcome {
	if env.Store == nil {
		return Outcome{Status: Skipped, Summary: "no workspace database"}
	}
	found, err := scheduler.NewRepo(env.Store, env.now).Hygiene(ctx, env.now())
	if err != nil {
		return Outcome{Status: Warn, Summary: "the schedules cannot be read: " + err.Error()}
	}
	if len(found) == 0 {
		return Outcome{Status: Pass, Summary: "no duplicate, dead or idle schedule"}
	}
	lines := make([]string, len(found))
	for i, f := range found {
		lines[i] = fmt.Sprintf("%s %q: %s", f.Kind, f.Name, f.Action)
	}
	return Outcome{Status: Warn, Summary: fmt.Sprintf("%d schedule problem(s): %s", len(found), strings.Join(lines, " | ")),
		Fix: "run `applysmith schedule list`, then `applysmith schedule disable <id>` or `applysmith schedule delete <id>` for the ones named"}
}

// checkScheduleAudit compares today's expected occurrences with the decisions
// recorded for them. A missing occurrence, one used up for a reason the user
// did not choose, or one that waited and was given up on is a warning; the
// ones still waiting and the skips the user or the machine chose are counted.
func checkScheduleAudit(ctx context.Context, env *Env) Outcome {
	if env.Store == nil {
		return Outcome{Status: Skipped, Summary: "no workspace database"}
	}
	audit, err := scheduler.NewRepo(env.Store, env.now).DayAudit(ctx, env.now(), time.Local)
	if err != nil {
		return Outcome{Status: Warn, Summary: "the schedule history cannot be read: " + err.Error()}
	}
	t := audit.Totals()
	if t.Expected == 0 {
		return Outcome{Status: Pass, Summary: "no occurrence was due yet today (" + audit.Date + ")"}
	}
	consumed, lost := 0, 0
	var reasons []string
	for _, s := range audit.Schedules {
		lost += s.Lost()
	}
	for reason, n := range t.Consumed {
		consumed += n
		reasons = append(reasons, fmt.Sprintf("%s %d", reason, n))
	}
	slices.Sort(reasons)
	summary := fmt.Sprintf("%s: %d due, %d ran, %d skipped (%s), %d deferred, %d missing", audit.Date, t.Expected, t.Fired+t.Interrupted,
		consumed, strings.Join(reasons, ", "), len(t.Deferred), len(t.Missing))
	if len(reasons) == 0 {
		summary = fmt.Sprintf("%s: %d due, %d ran, 0 skipped, %d deferred, %d missing", audit.Date, t.Expected, t.Fired+t.Interrupted, len(t.Deferred), len(t.Missing))
	}
	if len(t.Missing) == 0 && lost == 0 && t.ConsumedAfterDeferral == 0 {
		return Outcome{Status: Pass, Summary: summary}
	}
	var names []string
	for _, s := range audit.Schedules {
		if len(s.Missing) > 0 || s.Lost() > 0 || s.ConsumedAfterDeferral > 0 {
			names = append(names, s.Name)
		}
	}
	return Outcome{Status: Warn, Summary: summary + "; lost or missing for: " + strings.Join(names, ", "),
		Fix: "start the app so a missed occurrence is decided, and read `applysmith schedule history <id>` for the ones named"}
}

func checkDiscoverySchedules(ctx context.Context, env *Env) Outcome {
	if env.Store == nil {
		return Outcome{Status: Skipped, Summary: "no workspace database"}
	}
	all, err := scheduler.NewRepo(env.Store, env.now).List(ctx)
	if err != nil {
		return Outcome{Status: Warn, Summary: "the schedules cannot be read: " + err.Error()}
	}
	if len(all) == 0 {
		return Outcome{Status: Warn, Summary: "no schedule exists; postings are found only when you scan by hand",
			Fix: "run `applysmith schedule packs` and apply one with `applysmith schedule apply-pack <pack>`"}
	}
	enabled := 0
	for _, s := range all {
		if s.Enabled {
			enabled++
		}
	}
	return Outcome{Status: Pass, Summary: fmt.Sprintf("%d schedules, %d enabled", len(all), enabled)}
}

// checkRegistrySeeded warns when the ATS registry holds no board: the registry
// scan then has nothing to read.
func checkRegistrySeeded(ctx context.Context, env *Env) Outcome {
	if env.Store == nil {
		return Outcome{Status: Skipped, Summary: "no workspace database"}
	}
	stats, err := discovery.NewRepo(env.Store, env.now).RegistryStats(ctx)
	if err != nil {
		return Outcome{Status: Warn, Summary: "the registry cannot be read: " + err.Error()}
	}
	total := stats[discovery.StatusActive] + stats[discovery.StatusDead] + stats[discovery.StatusBlocked]
	if total == 0 {
		return Outcome{Status: Warn, Summary: "the registry is not seeded; the registry scan has no boards to read",
			Fix: "run `applysmith registry seed`"}
	}
	return Outcome{Status: Pass, Summary: fmt.Sprintf("%d boards, %d active", total, stats[discovery.StatusActive])}
}
