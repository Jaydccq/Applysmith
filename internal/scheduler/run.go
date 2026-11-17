package scheduler

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// job is one claimed occurrence on its way through the queue to a goroutine.
type job struct {
	runID        string
	sc           Schedule
	kind         Kind
	scheduledFor time.Time
	manual       bool
	holder       string
	// followUp marks a run another run's kind declared (Kind.Then). It is not
	// an occurrence of the schedule and starts no follow-up of its own.
	followUp bool

	started bool
	done    chan struct{}
	cancel  context.CancelCauseFunc
}

func (s *Scheduler) newJob(sc Schedule, k Kind, runID string, at time.Time, manual bool) *job {
	return &job{runID: runID, sc: sc, kind: k, scheduledFor: at, manual: manual,
		holder: fmt.Sprintf("%s (schedule %q, run %s)", k.Name, sc.Name, runID), done: make(chan struct{})}
}

// submit starts a claimed job when a slot and its leases are free and queues it
// otherwise. A claimed occurrence is never dropped.
func (s *Scheduler) submit(ctx context.Context, j *job) {
	s.runMu.Lock()
	s.queue = append(s.queue, j)
	sort.SliceStable(s.queue, func(a, b int) bool { return s.queue[a].scheduledFor.Before(s.queue[b].scheduledFor) })
	s.runMu.Unlock()
	s.dispatch(ctx)
}

// dispatch starts queued jobs in order of scheduled instant while slots remain.
// A job whose leases are taken stays queued and a later one may start first.
func (s *Scheduler) dispatch(ctx context.Context) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.runCtx.Err() != nil {
		return // stopping: whatever is still queued is marked interrupted
	}
	for i := 0; i < len(s.queue) && len(s.running) < maxRunning; {
		j := s.queue[i]
		busy, err := s.leases.acquire(ctx, j.holder, j.kind.Leases)
		if err != nil || busy != nil {
			if err != nil && ctx.Err() == nil {
				s.log.Warn("taking leases failed", "run", j.runID, "error", err.Error())
			}
			i++
			continue
		}
		s.queue = slices.Delete(s.queue, i, i+1)
		j.started = true
		s.running[j.runID] = j
		jobCtx, cancel := context.WithCancelCause(s.runCtx)
		j.cancel = cancel
		s.wg.Add(1)
		go s.execute(jobCtx, j)
	}
}

// Cancel asks a queued or running job to stop. It reports whether the run was
// known to this scheduler.
func (s *Scheduler) Cancel(runID string) bool {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if j, ok := s.running[runID]; ok {
		j.cancel(errCancelled)
		return true
	}
	for i, j := range s.queue {
		if j.runID == runID {
			s.queue = slices.Delete(s.queue, i, i+1)
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.finishQueued(j, Finish{RunID: j.runID, Status: "cancelled", Reason: ReasonCancelled,
					Detail: "cancelled before it started", At: s.clock.Now()})
			}()
			return true
		}
	}
	return false
}

func (s *Scheduler) finishQueued(j *job, f Finish) {
	if err := s.repo.Finish(context.WithoutCancel(s.runCtx), f); err != nil {
		s.log.Error("recording the end of a queued run failed", "run", j.runID, "error", err.Error())
	}
	s.ended(f)
	close(j.done)
	s.changed()
}

// execute runs a job with its retry policy and always releases its leases.
func (s *Scheduler) execute(ctx context.Context, j *job) {
	defer s.wg.Done()
	end := context.WithoutCancel(ctx)
	defer func() {
		if err := s.leases.release(end, j.holder); err != nil {
			s.log.Error("releasing leases failed", "run", j.runID, "error", err.Error())
		}
		s.runMu.Lock()
		delete(s.running, j.runID)
		s.runMu.Unlock()
		close(j.done)
		s.changed()
		s.dispatch(end)
	}()
	if err := s.repo.Start(end, j.runID, s.clock.Now()); err != nil {
		s.log.Error("marking a run started failed", "run", j.runID, "error", err.Error())
	}
	s.changed()
	summary, attempts, err := s.attempts(ctx, j)
	f := Finish{RunID: j.runID, Attempts: attempts, Summary: summarize(summary, 200), At: s.clock.Now()}
	switch cause := context.Cause(ctx); {
	case errors.Is(cause, errStopping):
		f.Status, f.Reason, f.Detail, f.Interrupted = "failed", ReasonInterrupted, "the server stopped while the job ran", true
	case errors.Is(cause, errCancelled):
		f.Status, f.Reason, f.Detail = "cancelled", ReasonCancelled, "cancelled by the user"
	case err == nil:
		f.Status = "succeeded"
	default:
		f.Status, f.Reason, f.Detail = "failed", ReasonError, summarize(err.Error(), 300)
		if class, reason := classify(err); class == FailNeedsYou {
			f.Reason = reason
			if s.o.NeedsYou != nil {
				s.o.NeedsYou(end, NeedsYouItem{RunID: j.runID, ScheduleID: j.sc.ID, Schedule: j.sc.Name, Kind: j.kind.Name, Reason: reason, Message: summarize(err.Error(), 300)})
			}
		}
	}
	if err := s.repo.Finish(end, f); err != nil {
		s.log.Error("recording the end of a run failed", "run", j.runID, "error", err.Error())
	}
	s.ended(f)
	if f.Status != "succeeded" {
		return
	}
	if s.o.Recovered != nil {
		s.o.Recovered(end, j.sc.ID)
	}
	if !j.followUp {
		s.followUp(end, j)
	}
}

// followUp starts the work j's kind declares for after a run that succeeded:
// one run of the named kind, recorded on j's schedule with no occurrence. It
// starts none while an enabled schedule of that kind exists, when Due finds
// nothing to do, while the server stops or when the queue is full. It holds
// that kind's own leases, so it waits in the queue until j releases its own.
func (s *Scheduler) followUp(ctx context.Context, j *job) {
	then := j.kind.Then
	if then == nil {
		return
	}
	k, ok := s.reg.Get(then.Kind)
	if !ok {
		return
	}
	scheds, err := s.repo.ListEnabled(ctx)
	if err != nil {
		s.log.Warn("reading the schedules for a follow-up failed", "run", j.runID, "error", err.Error())
		return
	}
	if slices.ContainsFunc(scheds, func(sc Schedule) bool { return sc.Kind == k.Name }) {
		return
	}
	if then.Due != nil {
		due, err := then.Due(ctx)
		if err != nil {
			s.log.Warn("checking a follow-up failed", "run", j.runID, "kind", k.Name, "error", err.Error())
		}
		if !due {
			return
		}
	}
	s.admitMu.Lock()
	defer s.admitMu.Unlock()
	if s.runCtx.Err() != nil {
		return
	}
	s.runMu.Lock()
	full := len(s.running) >= maxRunning && len(s.queue) >= maxWaiting
	s.runMu.Unlock()
	if full {
		return
	}
	sc := j.sc
	sc.Kind, sc.Inputs = k.Name, map[string]any{}
	runID, err := s.repo.ClaimFollowUp(ctx, sc)
	if err != nil {
		s.log.Warn("recording a follow-up failed", "run", j.runID, "kind", k.Name, "error", err.Error())
		return
	}
	next := s.newJob(sc, k, runID, s.clock.Now(), false)
	next.followUp = true
	s.submit(ctx, next)
	s.changed()
}

func (s *Scheduler) ended(f Finish) {
	if s.o.Ended != nil {
		s.o.Ended(f)
	}
}

// attempts calls the job up to three times. Only a network kind is retried,
// only after a transient failure, waiting 30 seconds and then 2 minutes with
// the leases kept.
func (s *Scheduler) attempts(ctx context.Context, j *job) (summary string, n int, err error) {
	for n = 1; ; n++ {
		summary, err = s.call(ctx, j, n)
		if err == nil || ctx.Err() != nil {
			return summary, n, err
		}
		class, _ := classify(err)
		if class != FailTransient || !j.kind.Network || n >= maxAttempts {
			return summary, n, err
		}
		s.log.Info("a job failed transiently and will be retried", "run", j.runID, "kind", j.kind.Name, "attempt", n, "error", err.Error())
		if werr := s.sleep(ctx, retryWaits[n-1]); werr != nil {
			return summary, n, err
		}
	}
}

// call runs the job once; a panic becomes a failure.
func (s *Scheduler) call(ctx context.Context, j *job, attempt int) (summary string, err error) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("a job panicked", "run", j.runID, "kind", j.kind.Name, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			summary, err = "", fmt.Errorf("the job panicked: %v", r)
		}
	}()
	in := Invocation{Schedule: j.sc.Name, ScheduleID: j.sc.ID, RunID: j.runID, Manual: j.manual, Attempt: attempt,
		Inputs: j.sc.Inputs, Progress: func(line string) {
			if s.o.Progress != nil {
				s.o.Progress(j.runID, line)
			}
		}}
	// A schedule edited since the occurrence was claimed does not change the
	// inputs of this run; they were validated when it was admitted.
	return j.kind.Run(ctx, in)
}

// summarize is s on one line, cut to at most n bytes at a character boundary.
func summarize(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > n {
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		return s[:n]
	}
	return s
}
