package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// timerLimit bounds one run of a built-in timer.
var timerLimit = 60 * time.Second

// Built-in timer cadences.
const (
	walEvery      = 15 * time.Minute
	retentionHour = 4
	retentionMeta = "scheduler.retention.slot"
)

// Timer is a built-in timer: work the scheduler drives that is not a row in
// the schedule store. The dashboard shows timers read-only. A timer that needs
// longer than a minute for one run also has a Limit() time.Duration method.
type Timer interface {
	Name() string
	Describe() string
	Status(ctx context.Context, now time.Time) TimerStatus
	// Tick is called on every scheduler tick and decides for itself whether it
	// is due. A failure is logged and affects nothing else.
	Tick(ctx context.Context, now time.Time) error
}

// TimerStatus is what the dashboard shows of a timer.
type TimerStatus struct {
	Active bool
	Reason string // why it is inactive
}

// AddTimer registers a timer, replacing one of the same name. A later task
// replaces a pending seam by registering a timer under the seam's name.
func (s *Scheduler) AddTimer(t Timer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, old := range s.timers {
		if old.Name() == t.Name() {
			s.timers[i] = t
			return
		}
	}
	s.timers = append(s.timers, t)
}

// TimerInfo is one row of the read-only built-in section.
type TimerInfo struct {
	Name     string
	Describe string
	TimerStatus
}

// Timers lists the built-in timers.
func (s *Scheduler) Timers(ctx context.Context) []TimerInfo {
	s.mu.Lock()
	ts := append([]Timer(nil), s.timers...)
	s.mu.Unlock()
	now := s.clock.Now()
	out := make([]TimerInfo, len(ts))
	for i, t := range ts {
		out[i] = TimerInfo{Name: t.Name(), Describe: t.Describe(), TimerStatus: t.Status(ctx, now)}
	}
	return out
}

// runTimers starts every active timer that is not still running. Each runs in
// its own goroutine with a limit of 60 seconds, so a slow or failing timer
// delays neither the tick nor the others.
func (s *Scheduler) runTimers(ctx context.Context, now time.Time) {
	s.mu.Lock()
	ts := append([]Timer(nil), s.timers...)
	s.mu.Unlock()
	for _, t := range ts {
		name := t.Name()
		s.mu.Lock()
		busy := s.timerBusy[name]
		if !busy {
			s.timerBusy[name] = true
		}
		s.mu.Unlock()
		if busy {
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() {
				s.mu.Lock()
				delete(s.timerBusy, name)
				s.mu.Unlock()
			}()
			s.runTimer(ctx, t, now)
		}()
	}
}

func (s *Scheduler) runTimer(ctx context.Context, t Timer, now time.Time) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("a built-in timer panicked", "timer", t.Name(), "panic", fmt.Sprint(r))
		}
	}()
	if !t.Status(ctx, now).Active {
		return
	}
	limit := timerLimit
	if l, ok := t.(interface{ Limit() time.Duration }); ok {
		limit = l.Limit()
	}
	tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), limit)
	defer cancel()
	if err := t.Tick(tctx, now); err != nil {
		s.log.Error("a built-in timer failed", "timer", t.Name(), "error", err.Error())
	}
}

type funcTimer struct {
	name, describe string
	every          time.Duration // zero runs on every tick
	fn             func(ctx context.Context) error
	mu             sync.Mutex
	last           time.Time
}

// EveryTick returns a timer that runs fn on every scheduler tick.
func EveryTick(name, describe string, fn func(ctx context.Context) error) Timer {
	return &funcTimer{name: name, describe: describe, fn: fn}
}

// Every returns a timer that runs fn every d, the first time d after its first
// tick.
func Every(name, describe string, d time.Duration, fn func(ctx context.Context) error) Timer {
	return &funcTimer{name: name, describe: describe, every: d, fn: fn}
}

func (t *funcTimer) Name() string                                  { return t.name }
func (t *funcTimer) Describe() string                              { return t.describe }
func (t *funcTimer) Status(context.Context, time.Time) TimerStatus { return TimerStatus{Active: true} }
func (t *funcTimer) Tick(ctx context.Context, now time.Time) error {
	if t.every > 0 {
		t.mu.Lock()
		if t.last.IsZero() {
			t.last = now
		}
		due := now.Sub(t.last) >= t.every
		if due {
			t.last = now
		}
		t.mu.Unlock()
		if !due {
			return nil
		}
	}
	return t.fn(ctx)
}

// retention is the daily sweep at 04:00 local time. Each product capability
// registers its retention rule; a failing rule is logged and the others run.
type retention struct {
	s     *Scheduler
	mu    sync.Mutex
	rules []retentionRule
}

type retentionRule struct {
	name string
	fn   func(ctx context.Context) error
}

// AddRetention registers a retention rule for the daily sweep.
func (s *Scheduler) AddRetention(name string, fn func(ctx context.Context) error) {
	s.ret.mu.Lock()
	defer s.ret.mu.Unlock()
	s.ret.rules = append(s.ret.rules, retentionRule{name, fn})
}

func (r *retention) Name() string { return "retention-sweep" }
func (r *retention) Describe() string {
	return "Daily at 04:00 local time: applies every retention rule of the product"
}
func (r *retention) Status(context.Context, time.Time) TimerStatus { return TimerStatus{Active: true} }

// slot is the latest 04:00 at or before now in the timer's zone.
func (r *retention) slot(now time.Time) time.Time { return dailySlot(r.s.o.Zone, now, retentionHour) }

// dailySlot is the latest hh:00 at or before now in zone, as an instant.
func dailySlot(zone *time.Location, now time.Time, hour int) time.Time {
	d := civilDate(now.In(zone))
	at := resolveWall(zone, time.Date(d.Year(), d.Month(), d.Day(), hour, 0, 0, 0, time.UTC))
	if at.After(now) {
		d = d.AddDate(0, 0, -1)
		at = resolveWall(zone, time.Date(d.Year(), d.Month(), d.Day(), hour, 0, 0, 0, time.UTC))
	}
	return at.UTC()
}

// Tick runs the sweep once per 04:00 slot. A machine that was asleep at 04:00
// runs the missed slot's sweep at its first tick, because every rule is
// idempotent; a fresh database runs it at the first tick.
func (r *retention) Tick(ctx context.Context, now time.Time) error {
	slot := r.slot(now)
	var last string
	err := r.s.o.Store.Read().QueryRowContext(ctx, `SELECT value FROM workspace_meta WHERE key = ?`, retentionMeta).Scan(&last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if !parseTS(last).Before(slot) && last != "" {
		return nil
	}
	r.mu.Lock()
	rules := append([]retentionRule(nil), r.rules...)
	r.mu.Unlock()
	var failed []string
	for _, rule := range rules {
		if err := r.runRule(ctx, rule); err != nil {
			failed = append(failed, rule.name+": "+err.Error())
			r.s.log.Error("a retention rule failed", "rule", rule.name, "error", err.Error())
		}
	}
	if err := r.s.setMeta(ctx, retentionMeta, ts(slot)); err != nil {
		return err
	}
	if len(failed) > 0 {
		return fmt.Errorf("retention rules failed: %s", strings.Join(failed, "; "))
	}
	return nil
}

func (r *retention) runRule(ctx context.Context, rule retentionRule) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return rule.fn(ctx)
}

func (s *Scheduler) setMeta(ctx context.Context, key, value string) error {
	return s.o.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO workspace_meta (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
		return err
	})
}
