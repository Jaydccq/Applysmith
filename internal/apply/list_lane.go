package apply

import (
	"context"
	"sync"
)

// RowLane is the one browser lane the rows of a list share: the apply window
// is a single lane, so a row's browser work never overlaps another row's. A
// row takes it before its pre-run checks and hands it back when its browser
// work is done, so only the record and learn tail of one row overlaps the next
// row's work.
type RowLane struct{ ch chan struct{} }

// NewRowLane returns a free lane.
func NewRowLane() *RowLane {
	l := &RowLane{ch: make(chan struct{}, 1)}
	l.ch <- struct{}{}
	return l
}

// Hold returns a row's claim on the lane. It is taken once and released once;
// releasing a claim that was never taken or already released does nothing.
func (l *RowLane) Hold() *LaneHold { return &LaneHold{lane: l} }

// LaneHold is one row's claim on a RowLane.
type LaneHold struct {
	lane *RowLane
	mu   sync.Mutex
	held bool
}

// Acquire waits for the lane or for ctx to end.
func (h *LaneHold) Acquire(ctx context.Context) error {
	h.mu.Lock()
	already := h.held
	h.mu.Unlock()
	if already {
		return nil
	}
	select {
	case <-h.lane.ch:
		h.mu.Lock()
		h.held = true
		h.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release hands the lane back.
func (h *LaneHold) Release() {
	if h == nil {
		return
	}
	h.mu.Lock()
	was := h.held
	h.held = false
	h.mu.Unlock()
	if was {
		h.lane.ch <- struct{}{}
	}
}

// keepsLane reports whether the lane stays with the row until its outcome has
// landed: the next row's checks must read a credential cooldown or an
// environment repair, so those two stops do not hand the lane back early.
func keepsLane(o Outcome) bool {
	return o.Kind == OutcomeStopped && (o.Class == ClassCredential || o.Class == ClassEnvironment)
}

// browserDone hands the lane back once the run's browser work is over, unless
// the stop keeps it.
func (r *Run) browserDone() {
	if !keepsLane(r.Outcome) {
		r.Input.Hold.Release()
	}
}
