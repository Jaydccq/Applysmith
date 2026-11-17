package scheduler

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// The hygiene report and the same-day audit read the schedule tables and
// change nothing: the schedules are the user's data. `applysmith doctor` shows
// both.

// Hygiene finding kinds.
const (
	HygieneDuplicate         = "duplicate"
	HygieneDisabledAndFailed = "disabled-and-failed"
	HygieneDeadLane          = "dead-lane"
	HygieneNeverRan          = "never-ran"
)

// Thresholds of the hygiene report.
const (
	// deadLaneWindow runs are looked at and deadLaneStreak consecutive failures
	// among the newest of them make a lane dead.
	deadLaneWindow = 5
	deadLaneStreak = 3
	// NeverRanAfter is how long an enabled schedule may go without a run
	// although its recurrence came due.
	NeverRanAfter = 14 * 24 * time.Hour
)

// HygieneFinding is one thing the user may want to fix.
type HygieneFinding struct {
	Kind        string
	ScheduleIDs []int64
	Name        string
	Action      string
}

// Hygiene lists duplicate schedules, disabled schedules whose last run failed,
// enabled ones whose newest runs all failed (a dead lane) and enabled ones
// that were due and never ran in 14 days. It never changes a schedule.
func (r *Repo) Hygiene(ctx context.Context, now time.Time) ([]HygieneFinding, error) {
	all, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	out := duplicateFindings(all)
	for _, s := range all {
		switch {
		case !s.Enabled && s.LastResult == ResultFailed:
			out = append(out, HygieneFinding{HygieneDisabledAndFailed, []int64{s.ID}, s.Name,
				"Disabled, and its last run failed. Delete it if it is no longer needed, or fix the cause and enable it."})
		case !s.Enabled:
		default:
			if f, err := r.deadLane(ctx, s); err != nil {
				return nil, err
			} else if f != nil {
				out = append(out, *f)
			}
			if f, err := r.neverRan(ctx, s, now); err != nil {
				return nil, err
			} else if f != nil {
				out = append(out, *f)
			}
		}
	}
	return out, nil
}

// deadLane finds an enabled schedule whose newest runs failed in a row. A
// cancelled run is the user's act and an interrupted one the server's, so
// neither is evidence about the lane, and neither is a follow-up of another
// kind recorded on the schedule (Kind.Then).
func (r *Repo) deadLane(ctx context.Context, s Schedule) (*HygieneFinding, error) {
	rows, err := r.st.Read().QueryContext(ctx, `SELECT status FROM runs WHERE schedule_id = ? AND kind = ? AND status IN ('succeeded', 'failed')
		AND NOT (status = 'failed' AND failure_reason = 'interrupted') ORDER BY COALESCE(finished_at, created_at) DESC, created_at DESC LIMIT ?`,
		s.ID, s.Kind, deadLaneWindow)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	streak := 0
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			return nil, err
		}
		if status != "failed" {
			break
		}
		streak++
	}
	if err := rows.Err(); err != nil || streak < deadLaneStreak {
		return nil, err
	}
	return &HygieneFinding{HygieneDeadLane, []int64{s.ID}, s.Name,
		fmt.Sprintf("Enabled, but its newest %d runs failed. Fix the sign-in or configuration, or disable it until it is fixed.", streak)}, nil
}

// neverRan finds an enabled schedule that has been enabled or edited for 14
// days, had an occurrence come due since and has never started a run.
func (r *Repo) neverRan(ctx context.Context, s Schedule, now time.Time) (*HygieneFinding, error) {
	if now.Sub(s.UpdatedAt) < NeverRanAfter {
		return nil, nil
	}
	rec, err := NewRecurrence(s.Preset, s.DTStart)
	if err != nil || len(rec.Between(s.UpdatedAt, now, 1)) == 0 {
		return nil, nil // a rule that does not parse is reported elsewhere; a sparse rule is not dead
	}
	var ran int
	if err := r.st.Read().QueryRowContext(ctx, `SELECT count(*) FROM schedule_history WHERE schedule_id = ? AND outcome IN ('fired', 'manual')`, s.ID).Scan(&ran); err != nil || ran > 0 {
		return nil, err
	}
	return &HygieneFinding{HygieneNeverRan, []int64{s.ID}, s.Name,
		fmt.Sprintf("Enabled %d days ago and due since, but it has never run. Check its recurrence and its history, or delete it.", int(now.Sub(s.UpdatedAt).Hours()/24))}, nil
}

// duplicateFindings groups schedules that look like one thing: the same name
// ignoring case and space, or the same kind, inputs and recurrence.
func duplicateFindings(all []Schedule) []HygieneFinding {
	parent := map[int64]int64{}
	var find func(int64) int64
	find = func(x int64) int64 {
		if parent[x] == x {
			return x
		}
		parent[x] = find(parent[x])
		return parent[x]
	}
	for _, s := range all {
		parent[s.ID] = s.ID
	}
	for _, key := range []func(Schedule) string{
		func(s Schedule) string { return strings.ToLower(strings.TrimSpace(s.Name)) },
		func(s Schedule) string { return s.Kind + "\x00" + marshal(s.Inputs) + "\x00" + marshal(s.Preset) },
	} {
		first := map[string]int64{}
		for _, s := range all {
			if id, ok := first[key(s)]; ok {
				parent[find(s.ID)] = find(id)
			} else {
				first[key(s)] = s.ID
			}
		}
	}
	groups := map[int64][]Schedule{}
	for _, s := range all {
		groups[find(s.ID)] = append(groups[find(s.ID)], s)
	}
	var out []HygieneFinding
	for _, g := range groups {
		if len(g) < 2 {
			continue
		}
		f := HygieneFinding{Kind: HygieneDuplicate, Name: g[0].Name}
		var names []string
		for _, s := range g {
			f.ScheduleIDs = append(f.ScheduleIDs, s.ID)
			names = append(names, s.Name)
		}
		f.Action = fmt.Sprintf("%d schedules look like the same thing (%s). Keep one; disable or delete the rest.", len(g), strings.Join(names, " / "))
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ScheduleIDs[0] < out[j].ScheduleIDs[0] })
	return out
}

// auditGrace keeps an occurrence that only just came due out of the audit: a
// tick decides it within the health window.
const auditGrace = healthWindow

// ScheduleAudit is what one schedule's occurrences of the day came to.
type ScheduleAudit struct {
	ScheduleID  int64
	Name        string
	Expected    int
	Fired       int
	Interrupted int
	// Consumed counts occurrences decided as skipped, by reason;
	// ConsumedAfterDeferral are those of them that waited first and were given
	// up on, the ones a retry could have saved.
	Consumed              map[string]int
	ConsumedAfterDeferral int
	// Deferred are occurrences still waiting, undecided; Missing have no
	// record at all.
	Deferred []time.Time
	Missing  []time.Time
}

// DayAudit compares, for the local day of now in zone, the occurrences the
// enabled schedules should have had with the decisions recorded for them.
type DayAudit struct {
	Date      string
	Schedules []ScheduleAudit
}

// Totals sums the schedules.
func (a DayAudit) Totals() (t ScheduleAudit) {
	t.Consumed = map[string]int{}
	for _, s := range a.Schedules {
		t.Expected, t.Fired, t.Interrupted = t.Expected+s.Expected, t.Fired+s.Fired, t.Interrupted+s.Interrupted
		t.ConsumedAfterDeferral += s.ConsumedAfterDeferral
		t.Deferred, t.Missing = append(t.Deferred, s.Deferred...), append(t.Missing, s.Missing...)
		for reason, n := range s.Consumed {
			t.Consumed[reason] += n
		}
	}
	return t
}

// DayAudit audits the day of now in zone. Occurrences before the schedule was
// last created, enabled or edited are not expected of it, and the last two
// minutes are left to the tick that has not decided them yet.
func (r *Repo) DayAudit(ctx context.Context, now time.Time, zone *time.Location) (DayAudit, error) {
	day := civilDate(now.In(zone))
	start := resolveWall(zone, day).UTC()
	out := DayAudit{Date: day.Format("2006-01-02")}
	scheds, err := r.ListEnabled(ctx)
	if err != nil {
		return out, err
	}
	for _, s := range scheds {
		rec, err := NewRecurrence(s.Preset, s.DTStart)
		if err != nil {
			continue
		}
		after := start.Add(-time.Millisecond)
		if s.UpdatedAt.After(after) {
			after = s.UpdatedAt
		}
		slots := rec.Between(after, now.Add(-auditGrace), 1000)
		if len(slots) == 0 {
			continue
		}
		a, err := r.auditSlots(ctx, s, slots)
		if err != nil {
			return out, err
		}
		out.Schedules = append(out.Schedules, a)
	}
	return out, nil
}

func (r *Repo) auditSlots(ctx context.Context, s Schedule, slots []time.Time) (ScheduleAudit, error) {
	a := ScheduleAudit{ScheduleID: s.ID, Name: s.Name, Expected: len(slots), Consumed: map[string]int{}}
	rows, err := r.st.Read().QueryContext(ctx, `SELECT scheduled_for, outcome, reason FROM schedule_history
		WHERE schedule_id = ? AND scheduled_for >= ? AND scheduled_for <= ?`, s.ID, ts(slots[0]), ts(slots[len(slots)-1]))
	if err != nil {
		return a, err
	}
	defer rows.Close()
	type seen struct {
		decision, reason string
		deferred         bool
	}
	bySlot := map[string]*seen{}
	for rows.Next() {
		var at, outcome, reason string
		if err := rows.Scan(&at, &outcome, &reason); err != nil {
			return a, err
		}
		if bySlot[at] == nil {
			bySlot[at] = &seen{}
		}
		switch outcome {
		case OutcomeFired, OutcomeSkipped, OutcomeInterrupted:
			bySlot[at].decision, bySlot[at].reason = outcome, reason
		case OutcomeDeferred:
			bySlot[at].deferred = true
		}
	}
	if err := rows.Err(); err != nil {
		return a, err
	}
	for _, slot := range slots {
		got := bySlot[ts(slot)]
		switch {
		case got == nil:
			a.Missing = append(a.Missing, slot)
		case got.decision == OutcomeFired:
			a.Fired++
		case got.decision == OutcomeInterrupted:
			a.Interrupted++
		case got.decision == OutcomeSkipped:
			a.Consumed[got.reason]++
			if got.deferred {
				a.ConsumedAfterDeferral++
			}
		case got.deferred:
			a.Deferred = append(a.Deferred, slot)
		default:
			a.Missing = append(a.Missing, slot)
		}
	}
	return a, nil
}

// LostReasons are the skip reasons that mean an occurrence was used up
// without running although the user wanted it to: the others (a disabled
// schedule, the policy, the host asleep) are the user's or the machine's
// choice.
var LostReasons = []string{ReasonBusy, ReasonLease, ReasonOffline, ReasonError, ReasonValidation, ReasonUnknownKind}

// Lost counts the occurrences consumed for a reason in LostReasons.
func (a ScheduleAudit) Lost() int {
	n := 0
	for reason, c := range a.Consumed {
		if slices.Contains(LostReasons, reason) {
			n += c
		}
	}
	return n
}
