package browser

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/chromedp/chromedp"
)

// Failures that end a lease because the browser went away.
const (
	// CodeBrowserClosedByUser reports a browser that exited with a clean
	// status while a run held a lane: the user quit it. It is not relaunched.
	CodeBrowserClosedByUser Code = "browser-closed-by-user"
	// CodeBrowserCrashed reports a lease whose one automatic recovery was used
	// or failed. The message names the browser log.
	CodeBrowserCrashed Code = "browser-crashed"
)

// recoveryBudget bounds one automatic recovery: adopting or launching, the
// window and the re-attach.
var recoveryBudget = 30 * time.Second

// quitWindow is how recently the last tab must have closed for the exit of an
// adopted process (whose status is not ours to read) to count as a quit: a
// browser that is quit closes its windows first, a crashed one does not.
const quitWindow = 5 * time.Second

// exitInfo says how the browser went away.
type exitInfo struct {
	// Clean means the process exited with status 0: the user quit it.
	Clean bool
	// Cause is the wait status or the failed connection, for the event log.
	Cause string
	// Adoptable means only the connection was lost: the browser still runs
	// and answers, so its record stays and the next open adopts it.
	Adoptable bool
}

func (i *Instance) clock() time.Time {
	if i.now != nil {
		return i.now()
	}
	return time.Now()
}

func (i *Instance) deadInfo() *exitInfo {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.dead
}

// closeTabsWhenGone reacts to the browser process ending on its own: every tab
// is closed so what was staged for it is released, and the instance is marked
// dead so a lease can recover. It ends without acting when the instance is
// stopped or detached first.
func (i *Instance) closeTabsWhenGone(exited <-chan struct{}) {
	select {
	case <-exited:
		i.browserGone(i.exitOf())
	case <-i.root.Done():
		// The connection ended. A stop or a detach ends it on purpose; anything
		// else is a loss, and the process tells which kind.
		i.mu.Lock()
		deliberate := i.stopped || i.detached
		i.mu.Unlock()
		if !deliberate {
			i.confirmGone()
		}
	}
}

// exitOf reads how the process ended. The wait status of a child we started is
// exact. An adopted process is not our child; the evidence is then whether it
// closed its windows before leaving.
func (i *Instance) exitOf() exitInfo {
	if i.proc != nil {
		if e := i.proc.exit.Load(); e != nil {
			return exitInfo{Clean: e.clean, Cause: "process ended: " + e.status}
		}
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	quit := len(i.tabs) == 0 && !i.lastClosed.IsZero() && i.clock().Sub(i.lastClosed) < quitWindow
	return exitInfo{Clean: quit, Cause: "process ended: status not observable (adopted browser)"}
}

// browserGone records that the browser process or connection is lost. It is
// the first closer: every in-flight operation fails at once because the root
// context ends, and the next call on a lease decides about recovery.
func (i *Instance) browserGone(info exitInfo) {
	i.mu.Lock()
	if i.stopped || i.dead != nil {
		i.mu.Unlock()
		return
	}
	i.dead = &info
	// A tab creation under way as the loss is marked may have had its tab made
	// all the same; it is counted here, before it fails and stops counting.
	abandoned := i.creating > 0 || len(i.liveAbandoned()) > 0
	i.mu.Unlock()
	alive := i.proc != nil && i.proc.alive()
	kind := EventBrowserExited
	if !info.Clean && alive {
		kind = EventBrowserDisconnected
	}
	i.logEvent(kind, "", fmt.Sprintf("%s; clean=%t", info.Cause, info.Clean))
	// The record is settled before the instance is unregistered, so the next
	// open, which may start at once, finds the browser it must adopt.
	switch {
	case i.proc == nil:
	case info.Adoptable || alive:
		// The browser still holds the profile, answering or not; the record is
		// the way back to it, and the next open adopts or retires it. A server
		// stop that comes first stops it (stopLost).
		i.proc.stopWatching()
		i.saveRecord(abandoned)
		registry.lose(i)
	default:
		if r, ok := ReadRecord(i.recordPath()); ok && r.PID == i.proc.PID {
			_ = os.Remove(i.recordPath())
		}
	}
	i.unregister()
	if i.cancelRoot != nil {
		i.cancelRoot()
	}
	if i.cancelAll != nil {
		i.cancelAll()
	}
	i.closeAllTabs("browser-exited")
	i.markGone()
}

// confirmGone is called when an operation failed for a reason that is neither
// a timeout nor a closed tab. A process that is gone is waited for briefly so
// its wait status is known; a process that is alive but whose endpoint no
// longer answers is a lost connection, and so is one that answers after the
// connection ended without a stop or a detach (the next open adopts it). It
// reports whether the browser is gone.
func (i *Instance) confirmGone() bool {
	if i.deadInfo() != nil {
		return true
	}
	if i.proc == nil || i.isStopped() {
		return false
	}
	if !processAlive(i.proc.PID) {
		select {
		case <-i.proc.done:
		case <-time.After(2 * time.Second):
		}
		i.browserGone(i.exitOf())
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := verifyEndpoint(ctx, i.proc.PID, i.proc.Start, i.proc.Profile, i.proc.Port); err != nil {
		// A process just killed is a zombie until it is reaped, and still
		// "alive" to a signal check; the wait status settles which it is.
		select {
		case <-i.proc.done:
			i.browserGone(i.exitOf())
		case <-time.After(2 * time.Second):
			i.browserGone(exitInfo{Cause: "connection lost: the debugging endpoint no longer answers"})
		}
		return true
	}
	i.mu.Lock()
	lost := i.root.Err() != nil && !i.detached
	i.mu.Unlock()
	if lost {
		i.browserGone(exitInfo{Cause: "connection lost: the browser still runs and answers; the next open adopts it" + i.lostReason(), Adoptable: true})
		return true
	}
	return false
}

// lostReason is what ended the DevTools connection, as the driver read it
// (an end of stream, a close frame, a read error), for the event log: a call
// made after the loss returns it.
func (i *Instance) lostReason() string {
	c := chromedp.FromContext(i.root)
	if c == nil || c.Browser == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Browser.Call(ctx, "Browser.getVersion", nil, nil); err != nil && ctx.Err() == nil {
		return " (" + err.Error() + ")"
	}
	return ""
}

// ready returns the instance the handle can work on. A handle without a lease
// reports a lost browser; a handle of a lease recovers it once.
func (h *LaneHandle) ready(ctx context.Context) (*Instance, error) {
	if h.lease != nil {
		return h.lease.ensure(ctx)
	}
	inst := h.base
	if inst.deadInfo() != nil {
		return nil, newError(CodeBrowserDisconnected, "the browser exited")
	}
	return inst, nil
}

// ensure returns the instance the lease works on. When the browser died it
// performs the lease's one automatic recovery, unless the lease was released;
// tabs open before the failure are not restored.
func (l *Lease) ensure(ctx context.Context) (*Instance, error) {
	l.mu.Lock()
	gone, inst := l.gone, l.inst
	l.mu.Unlock()
	if gone != nil {
		return nil, gone
	}
	d := inst.deadInfo()
	if d == nil {
		if inst.isStopped() {
			return nil, newError(CodeBrowserDisconnected, "the browser was stopped")
		}
		return inst, nil
	}
	return l.recover(ctx, inst, *d)
}

func (l *Lease) fail(err error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.gone == nil {
		l.gone, _ = err.(*Error)
	}
	if l.gone == nil {
		return err
	}
	return l.gone
}

func (l *Lease) recover(ctx context.Context, dead *Instance, info exitInfo) (*Instance, error) {
	l.recoverMu.Lock()
	defer l.recoverMu.Unlock()
	l.mu.Lock()
	gone, cur, used, released := l.gone, l.inst, l.recovered, l.released
	l.mu.Unlock()
	switch {
	case gone != nil:
		return nil, gone
	case cur != dead:
		return cur, nil // another caller of this lease recovered meanwhile
	case released:
		// The handle of an ended run still reaches the tabs it kept, but a
		// recovery would hold the lane again for that run.
		return nil, newError(CodeBrowserDisconnected, "the browser exited")
	case info.Clean:
		return nil, l.fail(newError(CodeBrowserClosedByUser, "the browser was quit while the %s lane was in use", l.lane))
	case used:
		return nil, l.fail(newError(CodeBrowserCrashed, "the browser failed again under the same lease; see the browser event log"))
	}
	l.mu.Lock()
	l.recovered = true
	l.mu.Unlock()
	dead.logEvent(EventRecoveryAttempted, l.lane, "run "+l.run)
	rctx, cancel := context.WithTimeout(ctx, recoveryBudget)
	defer cancel()
	next, err := Open(rctx, dead.opts)
	if err != nil {
		return nil, l.fail(wrapError(CodeBrowserCrashed, err, "the browser could not be recovered; see the browser event log"))
	}
	if !next.takeLease(l) {
		return nil, l.fail(newError(CodeBrowserCrashed, "the recovered browser stopped at once; see the browser event log"))
	}
	// A release during the open ended the hold on the dead instance. The lease
	// must not then hold the lane of this one, which no release would reach.
	l.mu.Lock()
	released = l.released
	if !released {
		l.inst = next
	}
	l.mu.Unlock()
	if released {
		next.mu.Lock()
		delete(next.leases, l)
		next.touchLocked()
		next.mu.Unlock()
		return nil, newError(CodeBrowserDisconnected, "the browser exited")
	}
	next.logEvent(EventRecoveryAttempted, l.lane, "recovered for run "+l.run)
	return next, nil
}

// takeLease moves a lease onto this instance after a recovery.
func (i *Instance) takeLease(l *Lease) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.stopped || i.dead != nil {
		return false
	}
	i.leases[l] = struct{}{}
	i.touchLocked()
	return true
}

// markLost remembers a tab the lease lost when the browser went away.
func (l *Lease) markLost(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lost == nil {
		l.lost = map[string]bool{}
	}
	l.lost[id] = true
}

func (l *Lease) wasLost(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lost[id]
}
