package apply

import (
	"context"
	"sync"
	"testing"
	"time"
)

// readBound bounds a status read while the keeper's lane or board work is
// held: the work stays held until every read has returned, so a read that
// waited on it would never return in time, however slow the machine.
const readBound = 2 * time.Second

// gateBoard is a runs board whose reads or clears wait until the gate opens,
// like a board call queued behind a busy database writer. entered closes when
// the first such call reaches its gate.
type gateBoard struct {
	fakeBoard
	rowsGate, clearGate chan struct{}
	once                sync.Once
	entered             chan struct{}
}

func newGateBoard(rows, clear bool) *gateBoard {
	b := &gateBoard{entered: make(chan struct{})}
	if rows {
		b.rowsGate = make(chan struct{})
	}
	if clear {
		b.clearGate = make(chan struct{})
	}
	return b
}

func (b *gateBoard) Rows() []KeptRow {
	if b.rowsGate != nil {
		b.once.Do(func() { close(b.entered) })
		<-b.rowsGate
	}
	return b.fakeBoard.Rows()
}

func (b *gateBoard) Cleared(run string) {
	if b.clearGate != nil {
		b.once.Do(func() { close(b.entered) })
		<-b.clearGate
	}
	b.fakeBoard.Cleared(run)
}

// held waits until the keeper's call is inside the board.
func (b *gateBoard) held(t *testing.T) {
	t.Helper()
	select {
	case <-b.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the keeper never reached the board")
	}
}

// within runs read and reports whether it returned inside readBound; a read
// that does not is left to finish when the test opens its gate.
func within(t *testing.T, name string, read func()) bool {
	t.Helper()
	done := make(chan struct{})
	go func() { read(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(readBound):
		t.Errorf("%s waited on the keeper's lane or board work for more than %v", name, readBound)
		return false
	}
}

// readsStayFast runs every read a status route makes against the keeper and
// fails when one waits on the keeper's slow work.
func readsStayFast(t *testing.T, k *Keeper, run, tab string) {
	t.Helper()
	within(t, "TabOf", func() { k.TabOf(run) })
	within(t, "IsOpen", func() { k.IsOpen(tab) })
	within(t, "Count", func() { k.Count() })
}

func TestKeeper_AStatusReadDoesNotWaitOnTheLaneListingATab(t *testing.T) {
	k, lane, clk := newKeeper(nil)
	gate := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(open)
	lane.setLive("a")
	lane.block = gate
	tab := &laneTabFake{id: "a", lane: lane}
	clk.advance(time.Second)
	admitted := make(chan struct{})
	go func() { k.Admit(context.Background(), tab, "run-a"); close(admitted) }()
	deadline := time.Now().Add(5 * time.Second)
	for lane.readCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if lane.readCount() == 0 {
		t.Fatal("the admit never listed the lane")
	}
	readsStayFast(t, k, "run-a", "a")
	var id string
	var ok bool
	if within(t, "TabOf", func() { id, ok = k.TabOf("run-a") }) && (!ok || id != "a") {
		t.Errorf("the admitted tab is not recorded while the lane is read: %q %v", id, ok)
	}
	open()
	<-admitted
}

func TestKeeper_AStatusReadDoesNotWaitOnTheBoardNamingRows(t *testing.T) {
	board := newGateBoard(true, false)
	k, lane, clk := newKeeper(board)
	var once sync.Once
	open := func() { once.Do(func() { close(board.rowsGate) }) }
	t.Cleanup(open)
	lane.setLive("a")
	tab := &laneTabFake{id: "a", lane: lane}
	clk.advance(time.Second)
	admitted := make(chan struct{})
	go func() { k.Admit(context.Background(), tab, "run-a"); close(admitted) }()
	board.held(t)
	readsStayFast(t, k, "run-a", "a")
	open()
	<-admitted
}

func TestKeeper_AStatusReadDoesNotWaitOnTheBoardClearingARow(t *testing.T) {
	board := newGateBoard(false, true)
	k, lane, clk := newKeeper(board)
	var once sync.Once
	open := func() { once.Do(func() { close(board.clearGate) }) }
	t.Cleanup(open)
	admitAll(k, lane, clk, "a", "b")
	forgotten := make(chan struct{})
	go func() { k.Forget("a"); close(forgotten) }()
	board.held(t)
	readsStayFast(t, k, "run-b", "b")
	open()
	<-forgotten
}

func TestKeeper_AStatusReadDoesNotWaitOnTheBoardWhenARefreshClearsClosedTabs(t *testing.T) {
	board := newGateBoard(false, true)
	k, lane, clk := newKeeper(board)
	var once sync.Once
	open := func() { once.Do(func() { close(board.clearGate) }) }
	t.Cleanup(open)
	admitAll(k, lane, clk, "a", "b")
	lane.setLive("b") // the user closed a
	clk.advance(time.Minute)
	k.IsOpen("b") // starts the background read of the lane
	board.held(t) // the refresh is inside the board's clear
	readsStayFast(t, k, "run-b", "b")
	open()
}
