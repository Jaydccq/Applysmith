package gmail

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// Watcher timings.
const (
	// WatchHold is one IDLE. Gmail ends an IDLE after about 29 minutes, and
	// RFC 2177 asks a client to renew it before then.
	WatchHold = 9 * time.Minute
	// WatchSettle lets the rest of a burst of new mail arrive before a sync.
	WatchSettle = 3 * time.Second
	// WatchSpacing keeps the syncs the watcher starts this far apart, so
	// Gmail's draft autosave, which changes All Mail every few seconds while
	// the user types, does not sign in again for each change.
	WatchSpacing = 20 * time.Second
	// WatchRetry doubles after each failure, up to WatchRetryCap.
	WatchRetry    = 5 * time.Second
	WatchRetryCap = 5 * time.Minute
	// WatchRecheck is how often a watcher that may not read looks again.
	WatchRecheck = 30 * time.Second
)

// errRestart ends a connection so the watcher starts over: the stored
// connection changed, Gmail may no longer be read, or the computer slept.
var errRestart = errors.New("gmail: the watch starts over")

// Watcher keeps one connection idling on All Mail and runs a sync as soon as
// Gmail announces a change there. It is the mailbox's automatic reader: the
// server runs it, it never reads a demo workspace, and it reads only while an
// app password is stored and Gmail accepts it. A sync it starts is recorded as
// scheduled, the product's own.
//
// Each sync signs in on a connection of its own, as before: go-imap's reader
// ends a session whose read deadline passes, and a sync pauses between
// commands longer than a command's deadline allows (classification, the link
// stage, the alerts), so the idling connection is not lent to it.
type Watcher struct {
	Client *Client
	Syncer *Syncer
	Repo   *Repo
	// Readable reports whether an app password is stored and not refused.
	Readable func(ctx context.Context) bool
	Log      *slog.Logger

	// Timings; zero is the package's constant. Tests shorten them.
	Hold, Settle, Spacing, Retry, RetryCap, Recheck time.Duration

	once sync.Once
	wake chan struct{}
	stop context.CancelFunc
	wg   sync.WaitGroup
}

func (w *Watcher) wakeup() chan struct{} {
	w.once.Do(func() { w.wake = make(chan struct{}, 1) })
	return w.wake
}

// Wake makes the watcher start over now: the stored connection changed.
func (w *Watcher) Wake() {
	select {
	case w.wakeup() <- struct{}{}:
	default:
	}
}

// Start runs the watcher in the background until Stop.
func (w *Watcher) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	w.stop = cancel
	w.wg.Go(func() { w.Run(ctx) })
}

// Stop ends the watcher and waits for it, with any sync it started.
func (w *Watcher) Stop() {
	if w.stop != nil {
		w.stop()
	}
	w.wg.Wait()
}

func (w *Watcher) log() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.New(slog.DiscardHandler)
}

// Run watches until ctx ends. A connection that ends in a failure is opened
// again after a growing pause; a refused app password is recorded by the
// sign-in itself, and the watcher then waits until one is accepted again.
func (w *Watcher) Run(ctx context.Context) {
	failures := 0
	for ctx.Err() == nil {
		if w.Repo.Demo() || !w.Readable(ctx) {
			failures = 0
			w.pause(ctx, orDuration(w.Recheck, WatchRecheck))
			continue
		}
		err := w.watch(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case errors.Is(err, errRestart):
			failures = 0
		default:
			failures++
			w.log().Warn("the Gmail watch connection ended", "error", err.Error())
			w.pause(ctx, w.backoff(failures))
		}
	}
}

// watch is the life of one connection: sign in, examine All Mail, and sync
// whenever its UIDNEXT or UIDVALIDITY differs from what the last complete
// sync started from, idling in between. UIDNEXT, not the cursor, says
// whether to sync: Gmail's draft autosave adds UIDs it expunges again, which
// no sync can move the cursor past.
func (w *Watcher) watch(ctx context.Context) error {
	connected := w.Repo.stateOr(ctx, keyConnectedAt)
	s, err := w.Client.open(ctx, nil)
	if err != nil {
		return err
	}
	defer s.close()
	hold := orDuration(w.Hold, WatchHold)
	// The IDLE command's deadline bounds the whole wait, and the session sits
	// unused while a sync runs, so its commands get the hold and a minute.
	s.c.Timeout = hold + time.Minute
	var synced folder
	var next time.Time // no sync starts before it
	failures := 0
	for {
		wait := hold
		if s.cur.next != synced.next || s.cur.validity != synced.validity {
			if d := time.Until(next); d > 0 {
				wait = d
			} else {
				if w.pause(ctx, orDuration(w.Settle, WatchSettle)) {
					return errRestart
				}
				seen, err := s.examine(s.all)
				if err != nil {
					return ctxErr(ctx, err)
				}
				started := time.Now()
				rec, err := w.Syncer.Run(ctx, TriggerScheduled)
				if ctx.Err() != nil {
					return ctx.Err()
				}
				next = started.Add(orDuration(w.Spacing, WatchSpacing))
				switch {
				case errors.Is(err, ErrAlreadyRunning):
					// Another sync reads now; look again after the spacing.
				case err != nil:
					failures++
					next = time.Now().Add(w.backoff(failures))
					w.log().Warn("a Gmail sync could not start", "error", err.Error())
				case rec.Status != "succeeded":
					failures++
					next = time.Now().Add(w.backoff(failures))
					w.log().Warn("a Gmail sync failed", "stage", rec.FailedStage, "error", rec.Error)
				default:
					failures = 0
					// A capped run leaves mail for the next one.
					if rec.CoverageComplete {
						synced = seen
					}
				}
				if err := w.still(ctx, connected); err != nil {
					return err
				}
				// Mail may have come while the sync ran.
				if _, err := s.examine(s.all); err != nil {
					return ctxErr(ctx, err)
				}
				continue
			}
		}
		end, err := s.idle(ctx, wait, w.wakeup())
		switch {
		case err != nil:
			return err
		case end == idleWoken, end == idleSlept:
			return errRestart
		}
		if err := w.still(ctx, connected); err != nil {
			return err
		}
		if _, err := s.examine(s.all); err != nil {
			return ctxErr(ctx, err)
		}
	}
}

// still reports errRestart once Gmail may no longer be read or the stored
// connection is not the one this session signed in with.
func (w *Watcher) still(ctx context.Context, connected string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !w.Readable(ctx) || w.Repo.stateOr(ctx, keyConnectedAt) != connected {
		return errRestart
	}
	return nil
}

// pause waits d, less when woken or when ctx ends; it reports a wake.
func (w *Watcher) pause(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	case <-w.wakeup():
		return true
	}
	return false
}

// backoff is the pause after the n-th failure in a row.
func (w *Watcher) backoff(n int) time.Duration {
	d, limit := orDuration(w.Retry, WatchRetry), orDuration(w.RetryCap, WatchRetryCap)
	for i := 1; i < n && d < limit; i++ {
		d *= 2
	}
	return min(d, limit)
}

func orDuration(v, d time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return d
}
