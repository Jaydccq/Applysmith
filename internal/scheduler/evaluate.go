package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// verdict is the result of admission for one occurrence.
type verdict struct {
	ok         bool
	kind       Kind
	reason     string
	detail     string
	deferrable bool // stays due and is retried on each tick
}

// admit runs the admission checks in their fixed order and returns the first
// that fails. A manual run skips only the enabled flag.
func (s *Scheduler) admit(ctx context.Context, sc Schedule, manual bool) verdict {
	if !manual && !sc.Enabled {
		return verdict{reason: ReasonDisabled, detail: "the schedule is disabled"}
	}
	k, ok := s.reg.Get(sc.Kind)
	if !ok {
		return verdict{reason: ReasonUnknownKind, detail: fmt.Sprintf("job kind %q is not registered", sc.Kind)}
	}
	if err := k.ValidateInputs(sc.Inputs); err != nil {
		return verdict{reason: ReasonValidation, detail: err.Error()}
	}
	if k.Network && !s.reachable(ctx) {
		return verdict{reason: ReasonOffline, detail: "the host cannot reach the internet", deferrable: true}
	}
	if sc.ActiveRunID != "" {
		return verdict{reason: ReasonBusy, detail: "the previous run of this schedule is still " + s.runState(sc.ActiveRunID) + " (" + sc.ActiveRunID + ")"}
	}
	busy, err := s.leases.free(ctx, k.Leases)
	if err != nil {
		return verdict{reason: ReasonError, detail: "checking leases: " + err.Error()}
	}
	if busy != nil {
		return verdict{reason: ReasonLease, detail: busy.String(), deferrable: true}
	}
	s.runMu.Lock()
	full := len(s.running) >= maxRunning && len(s.queue) >= maxWaiting
	s.runMu.Unlock()
	if full {
		return verdict{reason: ReasonBusy, detail: fmt.Sprintf("%d jobs run and %d wait", maxRunning, maxWaiting), deferrable: true}
	}
	return verdict{ok: true, kind: k}
}

func (s *Scheduler) runState(runID string) string {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if j, ok := s.running[runID]; ok && j.started {
		return "running"
	}
	return "queued"
}

// fireResult is what trying to fire one occurrence did.
type fireResult int

const (
	fired fireResult = iota
	consumed
	deferred
	unchanged // already decided, or the schedule changed since it was read
)

// tryFire admits one occurrence and either claims it, consumes it as skipped
// or leaves it due. The returned job is set when it fired.
func (s *Scheduler) tryFire(ctx context.Context, sc Schedule, occ time.Time, note string) (fireResult, *job, verdict) {
	s.admitMu.Lock()
	defer s.admitMu.Unlock()
	if s.runCtx.Err() != nil {
		return unchanged, nil, verdict{}
	}
	v := s.admit(ctx, sc, false)
	if !v.ok {
		if v.deferrable {
			return deferred, nil, v
		}
		return s.consume(ctx, sc, occ, v.reason, v.detail), nil, v
	}
	runID, err := s.repo.ClaimRun(ctx, Claim{Schedule: sc, ScheduledFor: occ, Detail: note})
	switch {
	case errors.Is(err, ErrDecided), errors.Is(err, errStaleSchedule), errors.Is(err, ErrNotFound):
		return unchanged, nil, v
	case err != nil:
		if ctx.Err() == nil {
			s.log.Error("claiming an occurrence failed", "schedule", sc.Name, "error", err.Error())
		}
		return unchanged, nil, v
	}
	j := s.newJob(sc, v.kind, runID, occ, false)
	s.submit(ctx, j)
	s.changed()
	return fired, j, v
}

// consume records an occurrence as skipped.
func (s *Scheduler) consume(ctx context.Context, sc Schedule, occ time.Time, reason, detail string) fireResult {
	err := s.repo.Skip(ctx, sc, occ, reason, detail)
	switch {
	case err == nil:
		s.changed()
		return consumed
	case errors.Is(err, ErrDecided), errors.Is(err, errStaleSchedule), errors.Is(err, ErrNotFound):
		return unchanged
	}
	if ctx.Err() == nil {
		s.log.Error("recording a skip failed", "schedule", sc.Name, "error", err.Error())
	}
	return unchanged
}

// evaluate decides the occurrences of one schedule that are due. It returns how
// long until the schedule's next occurrence, or zero when it has none.
func (s *Scheduler) evaluate(ctx context.Context, sc Schedule, now time.Time, gap *gapInfo) time.Duration {
	rec, err := NewRecurrence(sc.Preset, sc.DTStart)
	if err != nil {
		s.log.Warn("a schedule's preset does not parse; its occurrences are not evaluated", "schedule", sc.Name, "error", err.Error())
		return 0
	}
	s.runMu.Lock()
	chained := s.chains[sc.ID]
	s.runMu.Unlock()
	if chained {
		return 0
	}
	cursor := sc.LastFiredAt
	if cursor.IsZero() {
		cursor = sc.DTStart.Add(-time.Millisecond)
	}
	if floor := now.Add(-lookback); cursor.Before(floor) {
		cursor = floor
	}
	// Occurrences are at least minOccurrenceGap apart, so the window holds no
	// more than lookbackScan of them; reading all keeps the newest.
	due := rec.Between(cursor, now.Add(dueGrace), lookbackScan)
	if len(due) > lookbackMax {
		due = due[len(due)-lookbackMax:]
	}
	missed := len(due)
	if gap != nil && sc.Policy == PolicySkip {
		// An occurrence inside the grace ahead of this tick did not fall due
		// during the gap. Skip is the one policy that would drop it; the other
		// two fire it with what was missed.
		for missed > 0 && due[missed-1].After(now) {
			missed--
		}
	}
	switch {
	case len(due) == 0:
	case gap != nil:
		if missed > 0 {
			s.recoverMissed(ctx, sc, due[:missed], gap)
			sc = s.refresh(ctx, sc)
		}
		if missed < len(due) {
			s.decideLatest(ctx, sc, due[missed:], now)
		}
	default:
		s.decideLatest(ctx, sc, due, now)
	}
	// The schedule may have been edited or disabled while it was decided;
	// the next tick reads it again.
	fresh, err := s.repo.Get(ctx, sc.ID)
	if err != nil || !fresh.Enabled {
		return 0
	}
	from := fresh.LastFiredAt
	if from.IsZero() {
		from = cursor
	}
	next, ok := rec.Next(from)
	if !ok {
		if rec.Ended(from) && fresh.ActiveRunID == "" {
			if err := s.repo.Ended(ctx, fresh, from); err != nil && ctx.Err() == nil {
				s.log.Warn("disabling an ended schedule failed", "schedule", sc.Name, "error", err.Error())
			} else {
				s.changed()
			}
		}
		return 0
	}
	if !next.After(now.Add(dueGrace)) {
		// Due and not yet decided: a deferral that is retried on each tick.
		return retryWake
	}
	return next.Sub(now)
}

// decideLatest handles the ordinary tick: only the latest due occurrence can
// still be fired; an older one that is still waiting was superseded and is
// consumed with the reason it was deferred for.
func (s *Scheduler) decideLatest(ctx context.Context, sc Schedule, due []time.Time, now time.Time) {
	for _, occ := range due[:len(due)-1] {
		reason, ok := s.repo.DeferredReason(ctx, sc.ID, occ)
		if !ok {
			reason = ReasonPolicy
		}
		s.consume(ctx, sc, occ, reason, "a later occurrence became due before this one could start")
		sc = s.refresh(ctx, sc)
	}
	latest := due[len(due)-1]
	res, _, v := s.tryFire(ctx, sc, latest, "")
	if res != deferred {
		return
	}
	_, first, err := s.repo.Defer(ctx, sc.ID, latest, v.reason, v.detail)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("recording a deferral failed", "schedule", sc.Name, "error", err.Error())
		}
		return
	}
	s.changed()
	if now.Sub(latest) >= deferLimit {
		s.consume(ctx, sc, latest, first, fmt.Sprintf("still not startable after %s: %s", deferLimit, v.detail))
	}
}

// refresh re-reads a schedule after one of its occurrences was consumed; the
// cursor moved but the edit stamp did not, so the copy stays valid. A schedule
// that changed or vanished keeps the stale copy and the next write is refused.
func (s *Scheduler) refresh(ctx context.Context, sc Schedule) Schedule {
	fresh, err := s.repo.Get(ctx, sc.ID)
	if err != nil || !fresh.UpdatedAt.Equal(sc.UpdatedAt) {
		return sc
	}
	return fresh
}

// recoverMissed applies the schedule's missed-fire policy to the occurrences
// that fell due while ticks did not run.
func (s *Scheduler) recoverMissed(ctx context.Context, sc Schedule, due []time.Time, gap *gapInfo) {
	detail := gap.detail()
	switch sc.Policy {
	case PolicyFireOnce:
		for _, occ := range due[:len(due)-1] {
			s.consume(ctx, sc, occ, gap.reason, detail)
			sc = s.refresh(ctx, sc)
		}
		latest := due[len(due)-1]
		if res, _, v := s.tryFire(ctx, sc, latest, "missed-fire: "+detail); res == deferred {
			if _, _, err := s.repo.Defer(ctx, sc.ID, latest, v.reason, v.detail); err != nil && ctx.Err() == nil {
				s.log.Warn("recording a deferral failed", "schedule", sc.Name, "error", err.Error())
			}
		}
	case PolicyFireAll:
		s.startChain(sc, due, gap)
	default:
		for _, occ := range due {
			s.consume(ctx, sc, occ, gap.reason, detail)
			sc = s.refresh(ctx, sc)
		}
	}
}

// startChain replays missed occurrences one after another, outside the tick.
// At most 24 fire; the rest are recorded as skipped with reason policy.
func (s *Scheduler) startChain(sc Schedule, due []time.Time, gap *gapInfo) {
	s.runMu.Lock()
	s.chains[sc.ID] = true
	s.runMu.Unlock()
	replay, rest := due, []time.Time(nil)
	if len(replay) > fireAllCap {
		replay, rest = due[:fireAllCap], due[fireAllCap:]
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.runMu.Lock()
			delete(s.chains, sc.ID)
			s.runMu.Unlock()
		}()
		ctx := s.runCtx
		cur := sc
		for _, occ := range replay {
			if ctx.Err() != nil {
				return
			}
			var ok bool
			if cur, ok = s.replayOne(ctx, cur, occ, gap); !ok {
				return
			}
		}
		for _, occ := range rest {
			s.consume(ctx, cur, occ, ReasonPolicy, fmt.Sprintf("only %d missed occurrences are replayed in one pass", fireAllCap))
			cur = s.refresh(ctx, cur)
		}
	}()
}

// replayOne fires one replayed occurrence and waits for its job to end. A
// deferral is retried until the limit, then consumed. It returns false when
// the replay must stop.
func (s *Scheduler) replayOne(ctx context.Context, sc Schedule, occ time.Time, gap *gapInfo) (Schedule, bool) {
	start := s.clock.Now()
	for {
		fresh, err := s.repo.Get(ctx, sc.ID)
		if err != nil {
			return sc, false
		}
		sc = fresh
		res, j, v := s.tryFire(ctx, sc, occ, "missed-fire replay: "+gap.detail())
		switch res {
		case fired:
			select {
			case <-j.done:
			case <-ctx.Done():
				return sc, false
			}
			return s.refresh(ctx, sc), true
		case consumed:
			return s.refresh(ctx, sc), true
		case unchanged:
			return sc, false
		}
		if s.clock.Now().Sub(start) >= deferLimit {
			s.consume(ctx, sc, occ, v.reason, fmt.Sprintf("still not startable after %s: %s", deferLimit, v.detail))
			return s.refresh(ctx, sc), true
		}
		if err := s.sleep(ctx, chainPollWait); err != nil {
			return sc, false
		}
	}
}

// RunNow starts a schedule outside its recurrence. It passes the same
// admission checks as a scheduled run except for the enabled flag, and a
// refused run returns its reason at once instead of being skipped.
func (s *Scheduler) RunNow(ctx context.Context, id int64) (string, error) {
	s.admitMu.Lock()
	defer s.admitMu.Unlock()
	s.mu.Lock()
	live := s.started && !s.stopped
	s.mu.Unlock()
	if !live {
		return "", ErrNotRunning
	}
	sc, err := s.repo.Get(ctx, id)
	if err != nil {
		return "", err
	}
	v := s.admit(ctx, sc, true)
	if !v.ok {
		return "", &RefusedError{Reason: v.reason, Detail: v.detail}
	}
	at := s.clock.Now().Truncate(time.Millisecond)
	runID, err := s.repo.ClaimRun(ctx, Claim{Schedule: sc, ScheduledFor: at, Manual: true})
	if err != nil {
		return "", err
	}
	s.submit(ctx, s.newJob(sc, v.kind, runID, at, true))
	s.changed()
	return runID, nil
}
