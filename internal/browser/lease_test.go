package browser

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// clock is a settable time source for the idle and sweep logic.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// bareInstance is an instance with no browser behind it, for the logic that
// only needs its tables.
// noBrowser is the allocator of a bare instance: it starts nothing. Under a
// plain context chromedp's default allocator launches the installed Chrome
// for the first session a test attaches; under this one that attach fails.
type noBrowser struct{}

func (noBrowser) Allocate(context.Context, ...chromedp.BrowserOption) (*chromedp.Browser, error) {
	return nil, errors.New("a bare instance has no browser")
}

func (noBrowser) Wait() {}

func bareInstance(t *testing.T) (*Instance, *clock) {
	t.Helper()
	root, cancel := chromedp.NewAllocatorContext(context.Background(), noBrowser{})
	t.Cleanup(cancel)
	clk := newClock()
	i := &Instance{
		root: root, tabs: map[target.ID]*tab{}, windows: map[int64]Lane{}, popups: map[int64][]time.Time{}, leases: map[*Lease]struct{}{},
		stopDone: make(chan struct{}), idleAfter: idleStopAfter, now: clk.now,
	}
	i.idleSince = clk.now()
	return i, clk
}

// countingHold swaps the process-wide power hold for one that counts.
func countingHold(t *testing.T) (starts, stops *int) {
	t.Helper()
	old := hold
	var s, e int
	hold = &awake{start: func() func() { s++; return func() { e++ } }}
	t.Cleanup(func() { hold = old })
	return &s, &e
}

func TestBrowser_Lease_ApplyAdmitsOneScanAdmitsMany(t *testing.T) {
	countingHold(t)
	i, _ := bareInstance(t)
	first, err := i.Acquire(LaneApply, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = i.Acquire(LaneApply, "run-2")
	if code, _ := CodeOf(err); code != CodeLaneBusy {
		t.Fatalf("second apply lease: code %q err %v, want lane-busy", code, err)
	}
	if err == nil || !strings.Contains(err.Error(), "run-1") {
		t.Fatalf("the refusal must name the holder: %v", err)
	}
	if first.isReleased() {
		t.Fatal("the holder was disturbed")
	}
	for n := range 5 {
		if _, err := i.Acquire(LaneScan, "scan-"+string(rune('a'+n))); err != nil {
			t.Fatalf("scan lease %d: %v", n, err)
		}
	}
	first.Release()
	if _, err := i.Acquire(LaneApply, "run-2"); err != nil {
		t.Fatalf("the lane is free after the release: %v", err)
	}
}

func TestBrowser_PendingCreationHoldsLease(t *testing.T) {
	countingHold(t)
	i, _ := bareInstance(t)
	l, err := i.Acquire(LaneScan, "scan-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := l.beginCreate(); err != nil {
		t.Fatal(err)
	}
	l.Release()
	if !i.Leased() {
		t.Fatal("the lease ended while a tab creation was unacknowledged")
	}
	if len(i.leases) != 1 || l.pending != 1 {
		t.Fatalf("%d leases, %d pending creations", len(i.leases), l.pending)
	}
	if err := l.beginCreate(); err == nil {
		t.Fatal("a released lease started another creation")
	}
	if released := l.endCreate(); !released {
		t.Fatal("the end of the creation was not told that the lease ended meanwhile")
	}
	if i.Leased() {
		t.Fatal("the lease outlived the creation it was waiting for")
	}
}

func TestBrowser_ReleasedLeaseClosesItsUnkeptTabsOnly(t *testing.T) {
	countingHold(t)
	i, _ := bareInstance(t)
	closed := map[target.ID]string{}
	i.closeFn = func(_ context.Context, id target.ID) error { return nil }
	l, _ := i.Acquire(LaneScan, "scan-1")
	mine := &tab{id: "mine", lane: LaneScan, lease: l}
	kept := &tab{id: "kept", lane: LaneApply, lease: l}
	kept.kept.Store(true)
	user := &tab{id: "user", lane: LaneApply}
	for _, tb := range []*tab{mine, kept, user} {
		i.tabs[tb.id] = tb
	}
	l.Release()
	for _, tb := range []*tab{mine, kept, user} {
		closed[tb.id] = tb.closeReason()
	}
	if !mine.closed.Load() || mine.closeReason() != "run-ended" {
		t.Fatalf("the lease's own tab: closed=%v reason=%q", mine.closed.Load(), mine.closeReason())
	}
	if kept.closed.Load() || user.closed.Load() {
		t.Fatalf("a kept tab or the user's tab was closed: %v", closed)
	}
}

func TestHost_PowerAssertion_RefCountedPerLease(t *testing.T) {
	starts, stops := countingHold(t)
	i, _ := bareInstance(t)
	a, _ := i.Acquire(LaneScan, "scan-1")
	b, _ := i.Acquire(LaneScan, "scan-2")
	if *starts != 1 || *stops != 0 {
		t.Fatalf("two overlapping leases: starts=%d stops=%d, want one hold", *starts, *stops)
	}
	a.Release()
	if *stops != 0 {
		t.Fatal("the hold ended with the first release")
	}
	b.Release()
	b.Release() // releasing twice must not release another lease's hold
	if *starts != 1 || *stops != 1 {
		t.Fatalf("after the last release: starts=%d stops=%d", *starts, *stops)
	}
	args := caffeinateArgs(4242)
	want := []string{"-i", "-m", "-w", "4242"}
	if len(args) != len(want) {
		t.Fatalf("args %v", args)
	}
	for n := range want {
		if args[n] != want[n] {
			t.Fatalf("args %v, want %v: idle and disk sleep, bound to the server process, never -s", args, want)
		}
	}
}

func TestBrowser_IdleStop_AfterTenMinutes(t *testing.T) {
	countingHold(t)
	i, clk := bareInstance(t)
	due := func() bool {
		i.mu.Lock()
		defer i.mu.Unlock()
		return i.idleDueLocked()
	}
	clk.advance(9*time.Minute + 59*time.Second)
	if due() {
		t.Fatal("stopped before ten idle minutes")
	}
	clk.advance(2 * time.Second)
	if !due() {
		t.Fatal("not due after ten idle minutes with nothing holding the instance")
	}
	// A lease restarts the count when it ends.
	l, _ := i.Acquire(LaneScan, "scan-1")
	clk.advance(30 * time.Minute)
	if due() {
		t.Fatal("stopped while a lease was held")
	}
	l.Release()
	clk.advance(9 * time.Minute)
	if due() {
		t.Fatal("the idle count did not restart at the release")
	}
	// An import in flight, a run-owned tab and a kept tab each hold it.
	end := i.Begin()
	clk.advance(20 * time.Minute)
	if due() {
		t.Fatal("stopped during an import")
	}
	end()
	end()
	clk.advance(time.Minute)
	if due() {
		t.Fatal("the count did not restart after the import")
	}
	i.mu.Lock()
	i.tabs["kept"] = &tab{id: "kept", lane: LaneApply}
	i.tabs["kept"].kept.Store(true)
	i.touchLocked()
	i.mu.Unlock()
	clk.advance(time.Hour)
	if due() {
		t.Fatal("stopped with a tab open in a window")
	}
	i.mu.Lock()
	delete(i.tabs, "kept")
	i.touchLocked()
	i.mu.Unlock()
	clk.advance(10*time.Minute + time.Second)
	if !due() {
		t.Fatal("not due ten minutes after the last tab closed")
	}
}

// The browser panel watches a tab a run drives and gives the user the rest: a
// run's own tab until it is kept, and the user's tab while a held lease works
// on it.
func TestBrowser_DrivenTabsAreARunsUnkeptTabsAndTabsALeaseWorksOn(t *testing.T) {
	countingHold(t)
	i, _ := bareInstance(t)
	runs := &tab{id: "runs", lane: LaneApply, owner: "run-1"}
	user := &tab{id: "user", lane: LaneApply}
	for _, tb := range []*tab{runs, user} {
		i.tabs[tb.id] = tb
	}
	if d, run := runs.drivenBy(); !d || run != "run-1" {
		t.Fatalf("a run's tab: %v %q", d, run)
	}
	if d, _ := user.drivenBy(); d {
		t.Fatal("the user's tab with no lease on it")
	}

	l, err := i.Acquire(LaneApply, "fill-tab")
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := user.drivenBy(); d {
		t.Fatal("a held lease that has not touched the tab")
	}
	if _, err := l.Lane().tab("user"); err != nil {
		t.Fatal(err)
	}
	if d, run := user.drivenBy(); !d || run != "" {
		t.Fatalf("the user's tab a lease works on: %v %q", d, run)
	}
	if _, err := i.Lane(LaneApply).tab("runs"); err != nil {
		t.Fatal(err)
	}
	l.Release()
	if d, _ := user.drivenBy(); d {
		t.Fatal("the lease was released")
	}

	runs.kept.Store(true)
	if d, _ := runs.drivenBy(); d {
		t.Fatal("a tab kept for the user")
	}
	if _, err := i.Lane(LaneScan).tab("user"); err == nil {
		t.Fatal("a scan handle reached an apply tab")
	}
}

// A tab's own lock is held for its whole attach, up to a page load. Listing the
// tabs and reading whether one is kept, both under the instance lock, wait for
// no tab.
func TestBrowser_ListingTabsDoesNotWaitForATabThatIsAttaching(t *testing.T) {
	i, h, _ := bareLane(t, LaneApply, 1)
	tb := i.tabs["tab-00"]
	tb.mu.Lock()
	defer tb.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = h.Tabs()
		_ = i.Leased()
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a listing under the instance lock waited for a tab's own lock")
	}
}

// Two panels on one tab share the viewer session and its one screencast: the
// first to leave must not stop the frames of the other.
func TestBrowser_LiveViewsOfOneTabStopTheScreencastWithTheLastOne(t *testing.T) {
	v := &viewer{casts: 2}
	stops := 0
	v.endCast(func() { stops++ })
	if stops != 0 {
		t.Fatal("the screencast was stopped while another live view still read it")
	}
	v.endCast(func() { stops++ })
	if stops != 1 {
		t.Fatalf("%d stops after the last live view ended, want 1", stops)
	}
}

// A run's popup is the run's. A window the user opens from a tab kept for them
// (a sign-in popup) is the user's: the ended run's lease and name stay off it,
// so nothing attaches to it, answers its dialogs or closes it.
func TestBrowser_APopupOfAKeptTabIsTheUsers(t *testing.T) {
	countingHold(t)
	i, _ := bareInstance(t)
	l, _ := i.Acquire(LaneApply, "run-1")
	opener := &tab{id: "opener", lane: LaneApply, owner: "run-1", lease: l}
	runs := &tab{id: "p1"}
	if how := runs.inherit(opener); how != "popup" || runs.owner != "run-1" || runs.lease != l || runs.isKept() || runs.lane != LaneApply {
		t.Fatalf("a run's popup: %s %+v", how, runs)
	}
	opener.kept.Store(true)
	users := &tab{id: "p2"}
	if how := users.inherit(opener); how != "user" || users.owner != "" || users.lease != nil || !users.isKept() || users.lane != LaneApply {
		t.Fatalf("a kept tab's popup: %s owner %q lease %v kept %v", how, users.owner, users.lease, users.isKept())
	}
	if d, _ := users.drivenBy(); d {
		t.Fatal("the user's popup reads as driven by a run")
	}
}

// A lease released while its recovery is opening the browser holds nothing on
// the recovered one: the lane is free and the power hold is given up once.
func TestBrowser_Lease_ReleasedDuringItsRecoveryHoldsNothingAfterwards(t *testing.T) {
	_, stops := countingHold(t)
	dead, _ := bareInstance(t)
	dead.opts.ProfileDir = t.TempDir()
	l, err := dead.Acquire(LaneApply, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dead.Acquire(LaneScan, "run-2"); err != nil {
		t.Fatal(err)
	}
	dead.mu.Lock()
	dead.dead = &exitInfo{Cause: "test"}
	dead.mu.Unlock()
	// The recovery's open waits on this call, as it waits on a launch.
	next, _ := bareInstance(t)
	key, call := profileKey(dead.opts.ProfileDir), &openCall{done: make(chan struct{})}
	registry.mu.Lock()
	registry.calls[key] = call
	registry.mu.Unlock()
	t.Cleanup(func() { registry.forget(key, call) })
	got := make(chan error, 1)
	go func() { _, err := l.ensure(context.Background()); got <- err }()
	for !l.Recovered() {
		time.Sleep(time.Millisecond)
	}
	l.Release()
	registry.mu.Lock()
	call.inst = next
	registry.mu.Unlock()
	close(call.done)
	var typed *Error
	if err := <-got; !errors.As(err, &typed) || typed.Code != CodeBrowserDisconnected {
		t.Fatalf("the recovery of a released lease returned %v", err)
	}
	if held := len(next.leases); held != 0 {
		t.Fatalf("the released lease holds the recovered browser: %d leases", held)
	}
	if hold.n != 1 || *stops != 0 {
		t.Fatalf("the power hold counts %d leases (stops %d), want the one scan lease", hold.n, *stops)
	}
}

// The scheme rule is for pages a run drives. A tab the user opened by hand is
// theirs, as a kept tab is: its new-tab or settings page is left alone.
func TestBrowser_AUserTabOnALocalPageIsLeftAlone(t *testing.T) {
	i, _ := bareInstance(t)
	i.tabs["user"] = &tab{id: "user", lane: LaneApply}
	i.tabs["run"] = &tab{id: "run", lane: LaneApply, owner: "run-1"}
	blocked := func() (n int) {
		for _, e := range i.Events() {
			if e.Kind == EventNavigationBlocked {
				n++
			}
		}
		return n
	}
	i.onPageChanged(&target.Info{TargetID: "user", Type: "page", URL: "chrome://newtab/"})
	if blocked() != 0 || i.tabs["user"].ctx != nil {
		t.Fatalf("a tab the user opened was acted on: %d blocked, session %v", blocked(), i.tabs["user"].ctx != nil)
	}
	i.onPageChanged(&target.Info{TargetID: "run", Type: "page", URL: "chrome://settings/"})
	if blocked() != 1 {
		t.Fatalf("a run's tab on a local page: %d blocked events, want 1", blocked())
	}
}
