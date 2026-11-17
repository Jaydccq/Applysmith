package agent

import (
	"errors"
	"sync"
	"sync/atomic"
)

// MaxQueuedSteers is how many steering messages may wait for a step boundary.
const MaxQueuedSteers = 5

// ErrSteerFull means five steering messages already wait.
var ErrSteerFull = errors.New("agent: five steering messages are already queued")

// ActiveTurn is the handle of a running model-loop turn: the means to steer
// and cancel it, and to name the background runs it started.
type ActiveTurn struct {
	ID        string
	SessionID string

	cancelCh   chan struct{}
	cancelOnce sync.Once
	steerCh    chan string
	queued     atomic.Int32
	run        atomic.Pointer[run]
	// routed marks a turn the intent router answered: it has no model to steer.
	routed bool
}

func newActiveTurn(id, session string) *ActiveTurn {
	return &ActiveTurn{ID: id, SessionID: session, cancelCh: make(chan struct{}), steerCh: make(chan string, MaxQueuedSteers)}
}

// Cancel asks the turn to stop. It returns at once; the turn ends as cancelled.
func (a *ActiveTurn) Cancel() { a.cancelOnce.Do(func() { close(a.cancelCh) }) }

// TrySteer queues a steering message and returns how many now wait.
func (a *ActiveTurn) TrySteer(text string) (int, error) {
	for {
		n := a.queued.Load()
		if n >= MaxQueuedSteers {
			return int(n), ErrSteerFull
		}
		if a.queued.CompareAndSwap(n, n+1) {
			select {
			case a.steerCh <- text:
				return int(n) + 1, nil
			default:
				a.queued.Add(-1)
				return int(n), ErrSteerFull
			}
		}
	}
}

// delivered notes that a queued message reached the model.
func (a *ActiveTurn) delivered() {
	for {
		n := a.queued.Load()
		if n == 0 || a.queued.CompareAndSwap(n, n-1) {
			return
		}
	}
}

// Runs names the background runs the turn has started so far.
func (a *ActiveTurn) Runs() []string {
	if r := a.run.Load(); r != nil {
		return r.startedRuns()
	}
	return nil
}
