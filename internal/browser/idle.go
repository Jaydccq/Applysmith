package browser

import (
	"context"
	"sync"
	"time"
)

// touchLocked recomputes whether the instance is idle after something that
// holds it changed. The caller holds i.mu.
func (i *Instance) touchLocked() {
	if i.idleLocked() {
		if i.idleSince.IsZero() {
			i.idleSince = i.clock()
		}
		return
	}
	i.idleSince = time.Time{}
}

// idleLocked reports that nothing holds the instance: no lease, no operation
// in flight and no tab in either window. A tab the user left open, or one a
// run kept for them, keeps the browser running.
func (i *Instance) idleLocked() bool {
	return len(i.leases) == 0 && i.busy == 0 && len(i.tabs) == 0
}

// idleDueLocked reports that the instance has been idle for the whole idle
// period.
func (i *Instance) idleDueLocked() bool {
	return i.idleLocked() && !i.idleSince.IsZero() && i.clock().Sub(i.idleSince) >= i.idleAfter
}

// Begin marks an import, probe, print or capture as in flight, which keeps the
// instance from stopping itself. The returned function ends it.
func (i *Instance) Begin() func() {
	i.mu.Lock()
	i.busy++
	i.touchLocked()
	i.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			i.mu.Lock()
			i.busy--
			i.touchLocked()
			i.mu.Unlock()
		})
	}
}

// watchIdle stops the instance once it has been idle for the idle period. The
// stop sweeps cookies, asks Chrome to close and removes the instance record;
// the next demand launches a new instance.
func (i *Instance) watchIdle() {
	t := time.NewTicker(i.idleTick)
	defer t.Stop()
	for {
		select {
		case <-i.root.Done():
			return
		case <-t.C:
		}
		i.mu.Lock()
		due := i.idleDueLocked()
		i.mu.Unlock()
		if !due {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), cookieDeadline+stopGrace+killGrace+5*time.Second)
		_, stopped := i.stopWith(ctx, i.idleDueLocked, "no lease, no tab and no operation in flight")
		cancel()
		if stopped {
			return
		}
	}
}

// StopIdleOnExit stops the browser this process launched for the profile when
// no lease, operation in flight or tab kept for the user holds it. A
// command-line process calls it before it exits: the idle stop runs only in the
// process that opened the browser, so a browser left behind would hold the
// profile until a later process adopts it, with no lane labels when it never had
// both windows. A browser this process adopted is left as it was found.
func StopIdleOnExit(ctx context.Context, profileDir string) (StopOutcome, bool) {
	registry.mu.Lock()
	c := registry.calls[profileKey(profileDir)]
	registry.mu.Unlock()
	if c == nil {
		return "", false
	}
	select {
	case <-c.done:
	default:
		return "", false
	}
	i := c.inst
	if c.err != nil || i == nil || i.proc.unwatch != nil {
		return "", false
	}
	return i.stopWith(ctx, func() bool {
		if len(i.leases) > 0 || i.busy > 0 {
			return false
		}
		for _, t := range i.tabs {
			if t.isKept() {
				return false
			}
		}
		return true
	}, "the command that launched it ended")
}
