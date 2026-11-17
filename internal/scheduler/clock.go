package scheduler

import (
	"context"
	"time"
)

// Clock is the scheduler's source of time. Now is the wall clock; Mono is a
// monotonic reading used only to tell a starved process from a sleeping host.
type Clock interface {
	Now() time.Time
	Mono() time.Duration
}

// SystemClock reads the operating system. On platforms whose monotonic clock
// stops during suspend, a long sleep reads as a gap in which the monotonic
// clock did not advance, which is how host-asleep is told from host-starved.
type SystemClock struct{ start time.Time }

// NewSystemClock returns the production clock.
func NewSystemClock() *SystemClock { return &SystemClock{start: time.Now()} }

// Now returns the wall clock.
func (c *SystemClock) Now() time.Time { return time.Now() }

// Mono returns the monotonic time elapsed since the clock was created.
func (c *SystemClock) Mono() time.Duration { return time.Since(c.start) }

// sleepContext waits d or until ctx ends.
func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
