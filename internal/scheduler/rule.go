package scheduler

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/teambition/rrule-go"
)

// maxRuleIterations bounds the work of one lookup into a sparse rule.
const maxRuleIterations = 400_000

// rule evaluates an RFC 5545 recurrence rule in a zone. The library computes
// calendar arithmetic on wall-clock readings carried as UTC times; every
// reading is then mapped to an instant by resolveWall, so the zone's
// transitions follow one rule (a gap fires after the gap, a repeated hour
// fires once) instead of the library's own handling.
type rule struct {
	opt     rrule.ROption
	loc     *time.Location
	dtstart time.Time // wall-clock reading of the anchor, as UTC fields
	step    time.Duration
}

func parseRule(src string, loc *time.Location, dtstart time.Time) (*rule, error) {
	s := strings.TrimSpace(src)
	switch {
	case s == "":
		return nil, errors.New("an rrule preset needs a rule")
	case len(s) > maxRuleLength:
		return nil, fmt.Errorf("the rule is longer than %d characters", maxRuleLength)
	case strings.ContainsAny(s, "\r\n"):
		return nil, errors.New("the rule must be a single line")
	}
	s = strings.TrimPrefix(s, "RRULE:")
	var untilZ string
	for _, part := range strings.Split(s, ";") {
		key, val, _ := strings.Cut(part, "=")
		switch strings.ToUpper(key) {
		case "DTSTART":
			return nil, errors.New("the rule must not carry a start line; the schedule's dtstart is the anchor")
		case "UNTIL":
			untilZ = val
		}
	}
	opt, err := rrule.StrToROption(s)
	if err != nil {
		return nil, fmt.Errorf("invalid rule: %w", err)
	}
	wall := dtstart.In(loc)
	opt.Dtstart = time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), 0, time.UTC)
	if !opt.Until.IsZero() && strings.HasSuffix(strings.ToUpper(untilZ), "Z") {
		// A UTC UNTIL is an instant; the library compares wall readings.
		w := opt.Until.In(loc)
		opt.Until = time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), w.Second(), 0, time.UTC)
	}
	if _, err := rrule.NewRRule(*opt); err != nil {
		return nil, fmt.Errorf("invalid rule: %w", err)
	}
	r := &rule{opt: *opt, loc: loc, dtstart: opt.Dtstart}
	if opt.Count == 0 {
		n := time.Duration(max(opt.Interval, 1))
		switch opt.Freq {
		case rrule.SECONDLY:
			r.step = n * time.Second
		case rrule.MINUTELY:
			r.step = n * time.Minute
		case rrule.HOURLY:
			r.step = n * time.Hour
		}
	}
	return r, nil
}

// iterator returns the rule's readings from the first one at or after the wall
// reading `from`. A sub-daily rule without COUNT starts from an anchor moved
// forward by whole steps, which yields the same readings without walking from
// the original start.
func (r *rule) iterator(from time.Time) rrule.Next {
	opt := r.opt
	if r.step > 0 {
		if k := (from.Sub(opt.Dtstart) - 24*time.Hour) / r.step; k > 0 {
			opt.Dtstart = opt.Dtstart.Add(k * r.step)
		}
	}
	rr, err := rrule.NewRRule(opt)
	if err != nil {
		return func() (time.Time, bool) { return time.Time{}, false }
	}
	return rr.Iterator()
}

func wallOf(t time.Time, loc *time.Location) time.Time {
	w := t.In(loc)
	return time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), w.Second(), 0, time.UTC)
}

// walk calls yield with each distinct instant after `after`, in order, until
// yield returns false, the rule ends or the iteration bound is reached.
// exhausted is true only when the rule itself ended.
func (r *rule) walk(after time.Time, yield func(time.Time) bool) (exhausted bool) {
	next := r.iterator(wallOf(after, r.loc).Add(-48 * time.Hour))
	var last time.Time
	for i := 0; i < maxRuleIterations; i++ {
		w, ok := next()
		if !ok {
			return true
		}
		at := resolveWall(r.loc, w)
		if !last.IsZero() && !at.After(last) {
			continue
		}
		last = at
		if !at.After(after) {
			continue
		}
		if !yield(at.UTC()) {
			return false
		}
	}
	return false
}

func (r *rule) between(after, upto time.Time, max int) []time.Time {
	var out []time.Time
	r.walk(after, func(t time.Time) bool {
		if t.After(upto) {
			return false
		}
		out = append(out, t)
		return len(out) < max
	})
	return out
}

func (r *rule) next(after time.Time) (time.Time, bool) {
	var found time.Time
	r.walk(after, func(t time.Time) bool { found = t; return false })
	return found, !found.IsZero()
}

func (r *rule) ended(after time.Time) bool {
	found := false
	exhausted := r.walk(after, func(time.Time) bool { found = true; return false })
	return exhausted && !found
}
