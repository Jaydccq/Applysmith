package browser

import (
	"context"
	"sync"
)

// Lease errors.
const (
	// CodeLaneBusy reports a second request for the apply lane while a run
	// holds it. The message names the holder's run id.
	CodeLaneBusy Code = "lane-busy"
	// CodeLeaseReleased reports an operation on a lease that has ended.
	CodeLeaseReleased Code = "lease-released"
)

// Lease is the hold a run, a list or a scan has on one lane. It is acquired
// before the first operation and released when the holder ends in any way.
// Releasing never stops Chrome and never closes a tab kept for the user.
//
// The apply lane admits one lease at a time; the scan lane admits many, within
// its tab cap. A lease owns the tabs created through its handle, including a
// tab whose creation is still pending when the lease is released: the hold
// ends when that creation is known.
type Lease struct {
	lane Lane
	run  string

	mu        sync.Mutex
	inst      *Instance
	released  bool
	pending   int
	recovered bool
	lost      map[string]bool
	gone      error // set once the browser is gone for good

	recoverMu sync.Mutex // one recovery at a time for this lease
}

// Acquire gives run a lease on lane. The apply lane fails with lane-busy,
// naming the holder, while another lease holds it.
func (i *Instance) Acquire(lane Lane, run string) (*Lease, error) {
	i.mu.Lock()
	if i.stopped || i.dead != nil {
		i.mu.Unlock()
		return nil, newError(CodeBrowserDisconnected, "the browser is not running")
	}
	if h := i.applyHolder(); h != nil && lane == LaneApply {
		i.mu.Unlock()
		return nil, newError(CodeLaneBusy, "the apply lane is held by run %s", h.run)
	}
	l := &Lease{lane: lane, run: run, inst: i}
	i.leases[l] = struct{}{}
	i.touchLocked()
	i.mu.Unlock()
	hold.acquire()
	if lane == LaneApply {
		i.unfit()
	}
	return l, nil
}

// applyHolder is the lease on the apply lane, if any. i.mu must be held.
func (i *Instance) applyHolder() *Lease {
	for l := range i.leases {
		if l.lane == LaneApply {
			return l
		}
	}
	return nil
}

// Acquire gives run a lease on this handle's lane.
func (h *LaneHandle) Acquire(run string) (*Lease, error) { return h.in().Acquire(h.lane, run) }

// Lane returns the lane handle of the lease. Every tab it opens belongs to the
// lease.
func (l *Lease) Lane() *LaneHandle { return &LaneHandle{base: l.current(), lane: l.lane, lease: l} }

// Run is the holder's run id.
func (l *Lease) Run() string { return l.run }

func (l *Lease) current() *Instance {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inst
}

func (l *Lease) isReleased() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.released
}

// Release ends the hold. Tabs the lease opened and did not keep are closed;
// Chrome keeps running. A tab creation still pending keeps the lease listed
// until it is known, and its tab is closed on arrival.
func (l *Lease) Release() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return
	}
	l.released = true
	inst, pending := l.inst, l.pending
	l.mu.Unlock()
	inst.closeLeaseTabs(l)
	if pending == 0 {
		inst.dropLease(l)
	}
}

// beginCreate marks a tab creation pending, so the lease still holds the tab
// before the browser has acknowledged it.
func (l *Lease) beginCreate() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return newError(CodeLeaseReleased, "the lease on the %s lane has ended", l.lane)
	}
	l.pending++
	return nil
}

// endCreate marks the creation known. It reports whether the lease was
// released meanwhile, in which case the new tab must not outlive it.
func (l *Lease) endCreate() (released bool) {
	l.mu.Lock()
	l.pending--
	released = l.released
	drop := released && l.pending == 0
	inst := l.inst
	l.mu.Unlock()
	if drop {
		inst.dropLease(l)
	}
	return released
}

func (i *Instance) dropLease(l *Lease) {
	i.mu.Lock()
	_, held := i.leases[l]
	delete(i.leases, l)
	i.touchLocked()
	i.mu.Unlock()
	if held {
		hold.release()
	}
}

// closeLeaseTabs closes the tabs a released lease opened, kept ones apart.
func (i *Instance) closeLeaseTabs(l *Lease) {
	i.mu.Lock()
	var mine []*tab
	for _, t := range i.tabs {
		if t.lease == l && !t.closed.Load() {
			mine = append(mine, t)
		}
	}
	i.mu.Unlock()
	for _, t := range mine {
		if !t.isKept() {
			i.dropTab(context.Background(), t, "run-ended")
		}
	}
}

// Leased reports whether any lease is held.
func (i *Instance) Leased() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return len(i.leases) > 0
}
