package scheduler

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // zone names validate the same on every platform
)

// Preset kinds.
const (
	PresetDaily  = "daily"
	PresetWeekly = "weekly"
	PresetRRule  = "rrule"
)

// Limits of a recurrence rule.
const (
	maxRuleLength     = 4096
	ruleHorizon       = 366 * 24 * time.Hour
	minOccurrenceGap  = 5 * time.Minute
	gapProbeOccurence = 50
)

var weekdayNames = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// Preset is the recurrence of a schedule. daily and weekly carry a local
// time; rrule carries an RFC 5545 rule without a start line. Occurrences are
// computed in Timezone from the schedule's dtstart.
type Preset struct {
	Kind     string   `json:"kind"`
	Time     string   `json:"time,omitempty"`
	Weekdays []string `json:"weekdays,omitempty"`
	RRule    string   `json:"rrule,omitempty"`
	Timezone string   `json:"timezone"`
}

// Validate checks the preset as a whole against dtstart and now. An rrule must
// parse, have an occurrence within the next 366 days and keep its next 50
// occurrences at least five minutes apart.
func (p Preset) Validate(dtstart, now time.Time) error {
	r, err := newRecurrence(p, dtstart)
	if err != nil {
		return err
	}
	if p.Kind != PresetRRule {
		return nil
	}
	horizon := now.Add(ruleHorizon)
	first := r.between(now, horizon, 1)
	if len(first) == 0 {
		return errors.New("the rule has no occurrence within the next 366 days")
	}
	next := r.between(now, now.Add(100*365*24*time.Hour), gapProbeOccurence)
	for i := 1; i < len(next); i++ {
		if next[i].Sub(next[i-1]) < minOccurrenceGap {
			return fmt.Errorf("occurrences are closer than %d minutes (%s and %s)", int(minOccurrenceGap.Minutes()),
				next[i-1].UTC().Format(time.RFC3339), next[i].UTC().Format(time.RFC3339))
		}
	}
	return nil
}

// PreviewHorizon bounds how far ahead a preview looks for occurrences.
const PreviewHorizon = 400 * 24 * time.Hour

// PreviewFires validates a preset that starts at start and lists its next n
// occurrences after now, for showing before anything is saved or applied.
func PreviewFires(p Preset, start, now time.Time, n int) ([]time.Time, error) {
	if err := p.Validate(start, now); err != nil {
		return nil, err
	}
	r, err := newRecurrence(p, start)
	if err != nil {
		return nil, err
	}
	return r.between(now, now.Add(PreviewHorizon), n), nil
}

// Recurrence is a compiled preset anchored at a start instant.
type Recurrence struct {
	p       Preset
	loc     *time.Location
	dtstart time.Time
	hour    int
	minute  int
	days    map[time.Weekday]bool
	rule    *rule
}

// NewRecurrence compiles a preset. The error names the first invalid field.
func NewRecurrence(p Preset, dtstart time.Time) (*Recurrence, error) {
	return newRecurrence(p, dtstart)
}

func newRecurrence(p Preset, dtstart time.Time) (*Recurrence, error) {
	loc, err := loadZone(p.Timezone)
	if err != nil {
		return nil, err
	}
	r := &Recurrence{p: p, loc: loc, dtstart: dtstart.Truncate(time.Second)}
	switch p.Kind {
	case PresetDaily, PresetWeekly:
		if r.hour, r.minute, err = parseClock(p.Time); err != nil {
			return nil, err
		}
		if p.RRule != "" {
			return nil, errors.New("rrule is only valid in an rrule preset")
		}
		if p.Kind == PresetDaily {
			if len(p.Weekdays) > 0 {
				return nil, errors.New("weekdays are only valid in a weekly preset")
			}
			return r, nil
		}
		if len(p.Weekdays) == 0 {
			return nil, errors.New("a weekly preset needs at least one weekday")
		}
		r.days = map[time.Weekday]bool{}
		for _, name := range p.Weekdays {
			i := slices.Index(weekdayNames, strings.ToLower(strings.TrimSpace(name)))
			if i < 0 {
				return nil, fmt.Errorf("unknown weekday %q; use %s", name, strings.Join(weekdayNames, ", "))
			}
			r.days[time.Weekday(i)] = true
		}
	case PresetRRule:
		if p.Time != "" || len(p.Weekdays) > 0 {
			return nil, errors.New("time and weekdays are not valid in an rrule preset")
		}
		if r.rule, err = parseRule(p.RRule, r.loc, r.dtstart); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown preset kind %q; use daily, weekly or rrule", p.Kind)
	}
	return r, nil
}

func loadZone(name string) (*time.Location, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("a timezone is required")
	}
	if name == "Local" {
		return nil, errors.New("the timezone must be an IANA zone name, not Local")
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %q", name)
	}
	return loc, nil
}

func parseClock(s string) (hour, minute int, err error) {
	h, m, ok := strings.Cut(s, ":")
	if !ok || len(h) != 2 || len(m) != 2 {
		return 0, 0, fmt.Errorf("time %q must be HH:MM", s)
	}
	hour, e1 := strconv.Atoi(h)
	minute, e2 := strconv.Atoi(m)
	if e1 != nil || e2 != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("time %q must be HH:MM between 00:00 and 23:59", s)
	}
	return hour, minute, nil
}

// Between lists the occurrences after `after` and up to `upto` inclusive, at
// most max of them, in order.
func (r *Recurrence) Between(after, upto time.Time, max int) []time.Time {
	return r.between(after, upto, max)
}

func (r *Recurrence) between(after, upto time.Time, max int) []time.Time {
	if max <= 0 || !upto.After(after) {
		return nil
	}
	if r.rule != nil {
		return r.rule.between(after, upto, max)
	}
	var out []time.Time
	// Start a day early so a local date whose instant falls after `after` in
	// UTC terms is not skipped.
	d := civilDate(after.In(r.loc)).AddDate(0, 0, -1)
	if start := civilDate(r.dtstart.In(r.loc)).AddDate(0, 0, -1); d.Before(start) {
		d = start
	}
	end := civilDate(upto.In(r.loc)).AddDate(0, 0, 2)
	for ; !d.After(end) && len(out) < max; d = d.AddDate(0, 0, 1) {
		if r.days != nil && !r.days[d.Weekday()] {
			continue
		}
		at := resolveWall(r.loc, time.Date(d.Year(), d.Month(), d.Day(), r.hour, r.minute, 0, 0, time.UTC))
		if at.After(upto) {
			break
		}
		if at.After(after) && !at.Before(r.dtstart) {
			out = append(out, at.UTC())
		}
	}
	return out
}

// Next returns the first occurrence after `after`. ok is false when the rule
// has no further occurrence.
func (r *Recurrence) Next(after time.Time) (time.Time, bool) {
	if r.rule != nil {
		return r.rule.next(after)
	}
	got := r.between(after, after.Add(9*24*time.Hour), 1)
	if len(got) == 0 {
		return time.Time{}, false
	}
	return got[0], true
}

// Ended reports whether the rule can produce no occurrence after `after`. A
// rule that is merely sparse is not ended.
func (r *Recurrence) Ended(after time.Time) bool {
	if r.rule == nil {
		return false
	}
	return r.rule.ended(after)
}

// civilDate is the calendar date of t as midnight UTC, so date arithmetic has
// no daylight-saving steps.
func civilDate(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// resolveWall turns a wall-clock reading (given as the fields of a UTC time)
// into an instant in loc. A reading that does not exist on a transition day
// resolves to the first valid instant after the gap; a reading that occurs
// twice resolves to the first occurrence.
func resolveWall(loc *time.Location, wall time.Time) time.Time {
	w := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), 0, time.UTC)
	u := w.Unix()
	offsetAt := func(unix int64) int {
		_, off := time.Unix(unix, 0).In(loc).Zone()
		return off
	}
	offBefore, offAfter := offsetAt(u-36*3600), offsetAt(u+36*3600)
	var best time.Time
	found := false
	for _, off := range []int{offBefore, offAfter} {
		c := time.Unix(u-int64(off), 0)
		l := c.In(loc)
		if l.Year() == w.Year() && l.Month() == w.Month() && l.Day() == w.Day() && l.Hour() == w.Hour() &&
			l.Minute() == w.Minute() && l.Second() == w.Second() {
			if !found || c.Before(best) {
				best, found = c, true
			}
		}
	}
	if found {
		return best.In(loc)
	}
	// In a gap. The transition lies between the instant the later offset
	// would place the reading and the instant the earlier offset would.
	lo, hi := u-int64(max(offBefore, offAfter)), u-int64(min(offBefore, offAfter))
	target := offsetAt(hi)
	for lo < hi {
		mid := lo + (hi-lo)/2
		if offsetAt(mid) == target {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return time.Unix(hi, 0).In(loc)
}
