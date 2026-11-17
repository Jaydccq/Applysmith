package notify

import (
	"testing"
	"time"
)

// A batch parent counts each listed row once, however the rows overlap in time
// and however often a row's own producer reports it.
func TestRuns_BatchParent_CountsOverlappingRowsOnce(t *testing.T) {
	r := newRig(t)
	open := func(id string) {
		t.Helper()
		if _, err := r.board.Open(bg, OpenArgs{ID: id, Kind: KindApply, Origin: OriginUser, Title: "Apply " + id, BatchID: "batch-o"}); err != nil {
			t.Fatal(err)
		}
	}
	open("a")
	open("b")
	open("a") // the same row reported again while both rows are running
	open("c")
	r.move("a", Done, MoveArgs{Outcome: OutcomeOK})
	r.move("c", Done, MoveArgs{Outcome: OutcomeOK})
	r.process()
	if r.desktop.count() != 0 {
		t.Fatalf("announced before the last row finished: %v", r.desktop.titles())
	}
	r.move("b", Done, MoveArgs{Outcome: OutcomeOK})
	r.clk.advance(time.Minute)
	r.process()
	if r.desktop.count() != 1 || r.desktop.announced[0].Body != "3 of 3 finished, 0 need you" {
		t.Fatalf("announcements = %+v", r.desktop.announced)
	}
}
