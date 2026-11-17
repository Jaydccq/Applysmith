package apply

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"applysmith/internal/gates"
)

// fakeLane is the apply lane: its live tab list, the closes it was asked for.
type fakeLane struct {
	mu       sync.Mutex
	live     []string
	listErr  error
	closeErr map[string]error
	reads    int
	block    chan struct{}
	panics   bool
	closed   []string
	shown    []string
}

func (l *fakeLane) LiveTabs(context.Context) ([]string, error) {
	l.mu.Lock()
	l.reads++
	block, panics, err := l.block, l.panics, l.listErr
	out := slices.Clone(l.live)
	l.mu.Unlock()
	if panics {
		panic("the lane fell over")
	}
	if block != nil {
		<-block
	}
	return out, err
}

func (l *fakeLane) CloseTabID(_ context.Context, id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.closeErr[id]; err != nil {
		return err
	}
	l.closed = append(l.closed, id)
	l.live = slices.DeleteFunc(l.live, func(x string) bool { return x == id })
	return nil
}

func (l *fakeLane) ShowTab(_ context.Context, id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.shown = append(l.shown, id)
	return nil
}

func (l *fakeLane) readCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.reads
}

func (l *fakeLane) setLive(ids ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.live = ids
}

func (l *fakeLane) wasClosed(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Contains(l.closed, id)
}

// laneTabFake is a kept tab of a fakeLane.
type laneTabFake struct {
	Tab
	id       string
	lane     *fakeLane
	closeErr error
	panics   bool
}

func (f *laneTabFake) ID() string      { return f.id }
func (f *laneTabFake) Handle() TabLane { return f.lane }
func (f *laneTabFake) Close(ctx context.Context) error {
	if f.panics {
		panic("close blew up")
	}
	if f.closeErr != nil {
		return f.closeErr
	}
	return f.lane.CloseTabID(ctx, f.id)
}

type fakeBoard struct {
	mu      sync.Mutex
	rows    []KeptRow
	cleared []string
	panics  bool
}

func (b *fakeBoard) Rows() []KeptRow {
	if b.panics {
		panic("the board fell over")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.rows)
}

func (b *fakeBoard) Cleared(run string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cleared = append(b.cleared, run)
}

func (b *fakeBoard) wasCleared(run string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Contains(b.cleared, run)
}

// clock is a settable time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// admitAll admits tabs in order, each as the stop of its own run, one second
// apart, and returns the fakes by id.
func admitAll(k *Keeper, lane *fakeLane, clk *clock, ids ...string) map[string]*laneTabFake {
	tabs := map[string]*laneTabFake{}
	for _, id := range ids {
		lane.mu.Lock()
		lane.live = append(lane.live, id)
		lane.mu.Unlock()
		tabs[id] = &laneTabFake{id: id, lane: lane}
		clk.advance(time.Second)
		k.Admit(context.Background(), tabs[id], "run-"+id)
	}
	return tabs
}

func newKeeper(board KeptBoard) (*Keeper, *fakeLane, *clock) {
	clk := &clock{t: t0}
	return &Keeper{Board: board, Now: clk.Now}, &fakeLane{}, clk
}

func TestApply_Handoff_Admit_EvictsOldestNeverTheNewTab(t *testing.T) {
	t.Run("the fourth kept tab closes the oldest and the new one stays", func(t *testing.T) {
		k, lane, clk := newKeeper(nil)
		admitAll(k, lane, clk, "a", "b", "c", "d")
		if k.Count() != 3 || !lane.wasClosed("a") || lane.wasClosed("d") {
			t.Fatalf("kept %d, closed %v", k.Count(), lane.closed)
		}
	})
	t.Run("a tab whose run no longer needs the user goes first", func(t *testing.T) {
		board := &fakeBoard{rows: []KeptRow{
			{RunID: "run-a", TabID: "a", Since: t0.Add(time.Second), Waiting: true},
			{RunID: "run-b", TabID: "b", Since: t0.Add(2 * time.Second), Waiting: false},
			{RunID: "run-c", TabID: "c", Since: t0.Add(3 * time.Second), Waiting: true},
		}}
		k, lane, clk := newKeeper(board)
		admitAll(k, lane, clk, "a", "b", "c", "d")
		if !lane.wasClosed("b") || lane.wasClosed("a") || lane.wasClosed("d") {
			t.Fatalf("closed %v: the oldest waiting tab must outlive a finished one", lane.closed)
		}
		if !board.wasCleared("run-b") {
			t.Fatal("the board row of the closed tab still offers its jump")
		}
	})
	t.Run("the board's rows from an earlier process count, and are closed by id", func(t *testing.T) {
		board := &fakeBoard{rows: []KeptRow{
			{RunID: "old-1", TabID: "x1", Since: t0.Add(-3 * time.Hour), Waiting: true},
			{RunID: "old-2", TabID: "x2", Since: t0.Add(-2 * time.Hour), Waiting: true},
			{RunID: "old-3", TabID: "x3", Since: t0.Add(-1 * time.Hour), Waiting: true},
		}}
		k, lane, clk := newKeeper(board)
		lane.setLive("x1", "x2", "x3")
		admitAll(k, lane, clk, "n")
		if !lane.wasClosed("x1") || lane.wasClosed("n") || len(lane.closed) != 1 {
			t.Fatalf("closed %v", lane.closed)
		}
	})
	t.Run("a tab the user closed stops counting", func(t *testing.T) {
		k, lane, clk := newKeeper(nil)
		admitAll(k, lane, clk, "a", "b", "c")
		lane.setLive("b", "c") // a was closed by the user
		admitAll(k, lane, clk, "d")
		if len(lane.closed) != 0 || k.Count() != 3 {
			t.Fatalf("closed %v, kept %d", lane.closed, k.Count())
		}
	})
	t.Run("a lane that cannot be listed evicts nothing", func(t *testing.T) {
		k, lane, clk := newKeeper(nil)
		lane.listErr = errors.New("the lane is unreadable")
		admitAll(k, lane, clk, "a", "b", "c", "d", "e")
		if len(lane.closed) != 0 || k.Count() != 5 {
			t.Fatalf("closed %v, kept %d", lane.closed, k.Count())
		}
		bare := &Keeper{}
		bare.Admit(context.Background(), &fakeTab{id: "z"}, "run-z") // a tab with no lane
		if bare.Count() != 1 {
			t.Fatalf("kept %d", bare.Count())
		}
	})
	t.Run("a failing close is skipped, never thrown, and the next oldest goes", func(t *testing.T) {
		k, lane, clk := newKeeper(nil)
		lane.closeErr = map[string]error{"a": errors.New("the browser refused")}
		tabs := admitAll(k, lane, clk, "a", "b", "c")
		tabs["a"].closeErr = errors.New("the browser refused")
		admitAll(k, lane, clk, "d")
		if lane.wasClosed("a") || !lane.wasClosed("b") || lane.wasClosed("d") {
			t.Fatalf("closed %v", lane.closed)
		}
	})
	t.Run("a run keeps one tab and a later kept tab replaces its earlier one", func(t *testing.T) {
		k, lane, clk := newKeeper(nil)
		admitAll(k, lane, clk, "a", "b")
		lane.setLive("a", "b", "a2")
		k.Admit(context.Background(), &laneTabFake{id: "a2", lane: lane}, "run-a")
		if !lane.wasClosed("a") || k.Count() != 2 {
			t.Fatalf("closed %v, kept %d", lane.closed, k.Count())
		}
		if id, ok := k.TabOf("run-a"); !ok || id != "a2" {
			t.Fatalf("tab of the run: %q %v", id, ok)
		}
	})
	t.Run("the cap is three", func(t *testing.T) {
		if (&Keeper{}).max() != 3 {
			t.Fatal("the default cap is not 3")
		}
	})
}

type fakeTab struct {
	Tab
	id string
}

func (f *fakeTab) ID() string                  { return f.id }
func (f *fakeTab) Close(context.Context) error { return nil }

func eventually(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	end := time.Now().Add(within)
	for time.Now().Before(end) {
		if cond() {
			return
		}
		time.Sleep(within / 300)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestHandoff_JumpAction_OpenUntilReadUnreadableLaneNeverClosed(t *testing.T) {
	t.Run("open until the lane has been read", func(t *testing.T) {
		k, lane, _ := newKeeper(nil)
		if !k.IsOpen("a") {
			t.Fatal("a tab reads as closed before any lane is known")
		}
		lane.block = make(chan struct{})
		lane.live = []string{"a"}
		k.UseLane(lane)
		// The read is in flight and blocked: the answer is still "open", for a
		// tab the lane will not list as well, and no second read starts.
		if !k.IsOpen("ghost") || !k.IsOpen("ghost") || !k.IsOpen("a") {
			t.Fatal("the answer changed while the lane was being read")
		}
		eventually(t, 3*time.Second, "the first read", func() bool { return lane.readCount() == 1 })
		for range 5 {
			k.IsOpen("a")
		}
		time.Sleep(20 * time.Millisecond)
		if lane.readCount() != 1 {
			t.Fatalf("%d reads in flight", lane.readCount())
		}
		close(lane.block)
		eventually(t, 3*time.Second, "the read to land", func() bool { return !k.IsOpen("ghost") })
		if !k.IsOpen("a") {
			t.Fatal("a listed tab reads as closed")
		}
	})
	t.Run("an unreadable lane never makes a tab read as closed", func(t *testing.T) {
		k, lane, clk := newKeeper(nil)
		lane.listErr = errors.New("unreadable")
		k.UseLane(lane)
		for range 3 {
			clk.advance(time.Minute)
			if !k.IsOpen("a") {
				t.Fatal("an unread lane closed the tab")
			}
			time.Sleep(10 * time.Millisecond)
		}
		// A lane read once, then unreadable, keeps its last answer.
		lane.mu.Lock()
		lane.listErr, lane.live = nil, []string{"a"}
		lane.mu.Unlock()
		clk.advance(time.Minute)
		k.IsOpen("a")
		eventually(t, 3*time.Second, "a good read", func() bool { clk.advance(time.Minute); return !k.IsOpen("ghost") })
		lane.mu.Lock()
		lane.listErr, lane.live = errors.New("unreadable again"), nil
		lane.mu.Unlock()
		for range 3 {
			clk.advance(time.Minute)
			if !k.IsOpen("a") || k.IsOpen("ghost") {
				t.Fatal("a failed read changed the last answer")
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	t.Run("a read older than the TTL refreshes in the background and the user's closing shows", func(t *testing.T) {
		board := &fakeBoard{}
		k, lane, clk := newKeeper(board)
		lane.live = []string{"x", "y"}
		tabs := admitAll(k, lane, clk, "x", "y")
		_ = tabs
		eventually(t, 3*time.Second, "the first read", func() bool { return !k.IsOpen("ghost") })
		reads := lane.readCount()
		k.IsOpen("x")
		k.IsOpen("x")
		if lane.readCount() != reads {
			t.Fatal("a read inside the TTL started another")
		}
		lane.setLive("y") // the user closes x
		clk.advance(k.ttl() + time.Second)
		if !k.IsOpen("x") {
			t.Fatal("the stale answer was not returned at once")
		}
		eventually(t, 3*time.Second, "the refresh", func() bool { return !k.IsOpen("x") })
		if !k.IsOpen("y") || !board.wasCleared("run-x") {
			t.Fatalf("y open %v, cleared %v", k.IsOpen("y"), board.cleared)
		}
	})
}

func TestHandoff_BookkeepingNeverStopsRun(t *testing.T) {
	stopped := Outcome{Kind: OutcomeStopped, Stage: StageFill, Class: ClassDefect, Reason: "a field the walk would not finish"}
	rel := func(g *rig, tab Tab, mod func(*Exec)) (*Run, *Exec) {
		run := &Run{ID: "run_book", Input: Input{URL: pageURL, Caller: gates.CallerCLI}, Outcome: stopped}
		e := &Exec{r: g.r, Run: run, Ctx: context.Background()}
		if mod != nil {
			mod(e)
		}
		s := &session{r: g.r, e: e, tab: tab}
		s.release(context.Background())
		return run, e
	}
	t.Run("a faulty board and lane never reach the run", func(t *testing.T) {
		board := &fakeBoard{panics: true}
		k, lane, _ := newKeeper(board)
		g := newRig(t, func(d *Deps) { d.Page.Keep = k })
		tab := newScriptTab(appPage())
		lt := &laneScript{scriptTab: tab, lane: lane}
		lane.live = []string{"t1"}
		run, _ := rel(g, lt, nil)
		if !tab.kept || tab.closed {
			t.Fatalf("kept %v closed %v: a bookkeeping fault must leave the tab with the user", tab.kept, tab.closed)
		}
		if run.Outcome != stopped {
			t.Fatalf("outcome changed to %+v", run.Outcome)
		}
		lane.panics = true
		second := newScriptTab(appPage())
		if run, _ := rel(g, &laneScript{scriptTab: second, lane: lane}, nil); run.Outcome != stopped || !second.kept || second.closed {
			t.Fatalf("outcome %+v kept %v closed %v", run.Outcome, second.kept, second.closed)
		}
	})
	t.Run("a Keeper that is not configured is no keeper", func(t *testing.T) {
		g := newRig(t, nil)
		tab := newScriptTab(appPage())
		rel(g, tab, nil)
		if !tab.kept {
			t.Fatal("the tab was not kept")
		}
	})
	t.Run("gate tabs that no stage used are closed at the end of the run", func(t *testing.T) {
		for name, c := range map[string]struct {
			adopted   int
			sameTab   bool
			adoptedTb bool
			closedTab bool
			want      bool
		}{
			"unused gate tab":            {want: true},
			"a gate tab a stage used":    {adopted: 1},
			"the run's own tab":          {sameTab: true},
			"a tab the run did not open": {adoptedTb: true},
			"a tab already closed":       {closedTab: true},
		} {
			t.Run(name, func(t *testing.T) {
				g := newRig(t, nil)
				own := newScriptTab(appPage())
				gate := &closableGate{scriptTab: newScriptTab(appPage()), isClosed: c.closedTab}
				var gt Tab = gate
				if c.adoptedTb {
					// An adopted tab's Close only releases its lease, which a
					// fake cannot observe: this case shows the run goes on.
					gt = &adoptedTab{}
				}
				if c.sameTab {
					gt = own
				}
				rel(g, own, func(e *Exec) { e.gate = &gateTab{tab: gt, adopted: c.adopted} })
				if got := gate.closes == 1; got != c.want || gate.closes > 1 {
					t.Fatalf("gate tab closed %d times, want closed = %v", gate.closes, c.want)
				}
				if own.closed || !own.kept {
					t.Fatalf("the run's own tab closed = %v kept = %v: it is kept, never closed", own.closed, own.kept)
				}
			})
		}
	})
	t.Run("a gate tab that blows up on close never stops the run", func(t *testing.T) {
		g := newRig(t, nil)
		own := newScriptTab(appPage())
		run, _ := rel(g, own, func(e *Exec) { e.gate = &gateTab{tab: &laneTabFake{id: "g", lane: &fakeLane{}, panics: true}} })
		if run.Outcome != stopped || !own.kept {
			t.Fatalf("outcome %+v kept %v", run.Outcome, own.kept)
		}
	})
}

// laneScript is a scripted tab that also names a lane, as a laneTab does.
type laneScript struct {
	*scriptTab
	lane *fakeLane
}

func (l *laneScript) ID() string      { return "t1" }
func (l *laneScript) Handle() TabLane { return l.lane }

// closableGate reports whether the scripted tab was closed under the run and
// counts the Close calls it gets.
type closableGate struct {
	*scriptTab
	isClosed bool
	closes   int
}

func (c *closableGate) Closed() bool { return c.isClosed }

func (c *closableGate) Close(ctx context.Context) error {
	c.closes++
	return c.scriptTab.Close(ctx)
}

func TestKeeper_CloseEndsTheTabKeptForARunAndClearsItsRow(t *testing.T) {
	board := &fakeBoard{}
	k, lane, clk := newKeeper(board)
	ctx := context.Background()
	admitAll(k, lane, clk, "a", "b")
	if err := k.Close(ctx, "run-a", ""); err != nil || !lane.wasClosed("a") || !board.wasCleared("run-a") {
		t.Fatalf("the kept tab of a run: %v closed %v cleared %v", err, lane.closed, board.cleared)
	}
	if _, kept := k.TabOf("run-a"); kept || k.Count() != 1 {
		t.Fatal("a closed tab is still counted")
	}
	// After a restart the board row names the tab; the lane closes it by id.
	lane.setLive("b", "tab-earlier")
	if err := k.Close(ctx, "run-earlier", "tab-earlier"); err != nil || !lane.wasClosed("tab-earlier") || !board.wasCleared("run-earlier") {
		t.Fatalf("a tab an earlier process kept: %v closed %v", err, lane.closed)
	}
	lane.closeErr = map[string]error{"b": errors.New("the browser is busy")}
	if err := k.Close(ctx, "run-b", ""); err == nil || board.wasCleared("run-b") {
		t.Fatalf("a close that failed still cleared the row: %v", err)
	}
	if _, kept := k.TabOf("run-b"); !kept {
		t.Fatal("a tab whose close failed stopped being counted")
	}
}
