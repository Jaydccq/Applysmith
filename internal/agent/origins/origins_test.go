package origins

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	chatsessions "applysmith/internal/agent/sessions"
	"applysmith/internal/notify"
	"applysmith/internal/store"
)

var bg = context.Background()

type rig struct {
	st    *store.Store
	o     *Store
	board *notify.Board
	chat  string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	now := func() time.Time { return time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC) }
	st, err := store.Open(bg, filepath.Join(t.TempDir(), "applysmith.db"), store.Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sess, err := chatsessions.New(st, now).Create(bg, chatsessions.Dashboard, "Chat")
	if err != nil {
		t.Fatal(err)
	}
	return &rig{st: st, o: New(st, now), board: notify.NewBoard(st, notify.BoardOptions{Now: now}), chat: sess.ID}
}

func TestOrigins_RecordKeepsTheFirstAndIgnoresARunWithNoConversation(t *testing.T) {
	r := newRig(t)
	if err := r.o.Record(bg, "run_1", r.chat, "scan_portal"); err != nil {
		t.Fatal(err)
	}
	if err := r.o.Record(bg, "run_1", r.chat, "evaluate_job"); err != nil {
		t.Fatal(err)
	}
	if err := r.o.Record(bg, "run_2", "", "scan_portal"); err != nil {
		t.Fatal(err)
	}
	o, err := r.o.Get(bg, "run_1")
	if err != nil || o.Tool != "scan_portal" || o.SessionID != r.chat {
		t.Fatalf("origin = %+v %v", o, err)
	}
	if _, err := r.o.Get(bg, "run_2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a run with no conversation has an origin: %v", err)
	}
}

func TestOrigins_AnApplyRequestRoundTripsAndRecordsWhoPressedDone(t *testing.T) {
	r := newRig(t)
	in := ApplyRequest{RunID: "run_1", TurnID: "turn_1", Caller: "ask-agent", Tool: "autofill_application", Submit: true, Explicit: true, Outside: true,
		Confirmed: true, UserMessage: "apply to https://jobs.example.com/p/1", URL: "https://jobs.example.com/p/1", Company: "Example Corp", Role: "Engineer", Named: true}
	if err := r.o.RecordApply(bg, r.chat, in); err != nil {
		t.Fatal(err)
	}
	got, org, err := r.o.Request(bg, "run_1")
	if err != nil || org.SessionID != r.chat {
		t.Fatalf("%+v %+v %v", got, org, err)
	}
	if got.PressedBy != "" || !got.PressedAt.IsZero() {
		t.Fatalf("pressed before any press: %+v", got)
	}
	got.PressedBy, got.PressedAt = "", time.Time{}
	if got != in {
		t.Fatalf("round trip lost a fact:\n got %+v\nwant %+v", got, in)
	}
	if err := r.o.Pressed(bg, "run_1", "telegram"); err != nil {
		t.Fatal(err)
	}
	got, _, _ = r.o.Request(bg, "run_1")
	if got.PressedBy != "telegram" || got.PressedAt.IsZero() {
		t.Fatalf("press = %+v", got)
	}
	if _, _, err := r.o.Request(bg, "run_x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown run: %v", err)
	}
}

func TestOrigins_ActiveCountsTheRunsThatAreStillGoing(t *testing.T) {
	r := newRig(t)
	for _, id := range []string{"run_1", "run_2", "run_3"} {
		if err := r.o.Record(bg, id, r.chat, "scan_portal"); err != nil {
			t.Fatal(err)
		}
	}
	// A run another origin never started is not counted.
	if _, err := r.board.Open(bg, notify.OpenArgs{ID: "run_other", Kind: notify.KindScan, Origin: notify.OriginUser, Title: "x"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"run_1", "run_2", "run_3"} {
		if _, err := r.board.Open(bg, notify.OpenArgs{ID: id, Kind: notify.KindScan, Origin: notify.OriginAgent, Title: id}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := r.o.Active(bg); err != nil || n != 3 {
		t.Fatalf("active = %d %v", n, err)
	}
	if _, err := r.board.Move(bg, "run_1", notify.Done, notify.MoveArgs{Outcome: notify.OutcomeOK}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.board.Move(bg, "run_2", notify.NeedsYou, notify.MoveArgs{Reason: "sign-in"}); err != nil {
		t.Fatal(err)
	}
	if n, _ := r.o.Active(bg); n != 1 {
		t.Fatalf("a finished run and a waiting run still count: %d", n)
	}
	// Per conversation, as a delegated task waits on its own runs only.
	other, err := chatsessions.New(r.st, nil).Create(bg, chatsessions.Dashboard, "Other")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.o.Record(bg, "run_4", other.ID, "scan_portal"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.board.Open(bg, notify.OpenArgs{ID: "run_4", Kind: notify.KindScan, Origin: notify.OriginAgent, Title: "run_4"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"run_1", "run_2"} {
		e, err := r.board.Get(bg, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.o.Told(bg, id, EndOf(e)); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := r.o.ActiveIn(bg, r.chat); err != nil || n != 1 {
		t.Fatalf("active in the conversation = %d %v", n, err)
	}
	if runs, err := r.o.RunsOf(bg, other.ID); err != nil || len(runs) != 1 || runs[0] != "run_4" {
		t.Fatalf("runs of the other conversation = %v %v", runs, err)
	}
	if _, err := r.board.Resume(bg, "run_2", ""); err == nil {
		t.Fatal("a scan run was resumed")
	}
}

// A run's end counts for its conversation's task until the conversation is told
// it: the end is one write on the board and the note a later one, and a task
// that read the board between them took its turn with that end untold. The
// cap's own count is the runs going and leaves told or untold ends out.
func TestOrigins_AnEndCountsForItsConversationUntilItIsTold(t *testing.T) {
	r := newRig(t)
	for _, id := range []string{"run_1", "run_2", "run_3"} {
		if err := r.o.Record(bg, id, r.chat, "evaluate_job"); err != nil {
			t.Fatal(err)
		}
		if _, err := r.board.Open(bg, notify.OpenArgs{ID: id, Kind: notify.KindEvaluate, Origin: notify.OriginAgent, Title: id}); err != nil {
			t.Fatal(err)
		}
	}
	move := func(id string, to notify.State, a notify.MoveArgs) {
		t.Helper()
		if _, err := r.board.Move(bg, id, to, a); err != nil {
			t.Fatal(err)
		}
	}
	move("run_1", notify.Done, notify.MoveArgs{Outcome: notify.OutcomeOK})
	move("run_2", notify.Done, notify.MoveArgs{Outcome: notify.OutcomeFailed})
	move("run_3", notify.NeedsYou, notify.MoveArgs{Reason: "sign-in"})
	if n, err := r.o.ActiveIn(bg, r.chat); err != nil || n != 3 {
		t.Fatalf("ActiveIn = %d, %v; want the three untold ends", n, err)
	}
	if n, err := r.o.Active(bg); err != nil || n != 0 {
		t.Fatalf("Active = %d, %v; no run is going", n, err)
	}
	untold := func() []string {
		t.Helper()
		ends, err := r.o.Untold(bg, time.Date(2999, 1, 1, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, e := range ends {
			if e.SessionID != r.chat {
				t.Fatalf("end %+v is not of the conversation", e)
			}
			ids = append(ids, e.RunID)
		}
		return ids
	}
	if got := untold(); len(got) != 3 {
		t.Fatalf("untold = %v", got)
	}
	if ends, err := r.o.Untold(bg, time.Date(2026, 3, 10, 11, 0, 0, 0, time.UTC)); err != nil || len(ends) != 0 {
		t.Fatalf("ends before they came: %+v %v", ends, err)
	}
	for _, id := range []string{"run_1", "run_3"} {
		e, err := r.board.Get(bg, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.o.Told(bg, id, EndOf(e)); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := r.o.ActiveIn(bg, r.chat); n != 1 {
		t.Fatalf("ActiveIn = %d with one end untold", n)
	}
	if got := untold(); len(got) != 1 || got[0] != "run_2" {
		t.Fatalf("untold = %v, want run_2", got)
	}
	// A later end of a run it was told about is told again.
	move("run_3", notify.NeedsYou, notify.MoveArgs{Reason: "answer"})
	if got := untold(); len(got) != 2 || got[1] != "run_3" {
		t.Fatalf("untold = %v, want run_2 and run_3's new end", got)
	}
}

// The evaluations a conversation's scan queued are its work too: the scan ends
// with them queued, and a task that went on then would find nothing to do.
func TestOrigins_AScansEvaluationsCountForItsConversationWhileGoing(t *testing.T) {
	r := newRig(t)
	if err := r.o.Record(bg, "run_scan", r.chat, "scan_portal"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.board.Open(bg, notify.OpenArgs{ID: "run_scan", Kind: notify.KindScan, Origin: notify.OriginAgent, Title: "scan"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"run_e1", "run_e2"} {
		if _, err := r.board.Open(bg, notify.OpenArgs{ID: id, Kind: notify.KindEvaluate, Origin: notify.OriginAgent, Title: id, BatchID: notify.ScanBatch + "run_scan"}); err != nil {
			t.Fatal(err)
		}
	}
	e, err := r.board.Move(bg, "run_scan", notify.Done, notify.MoveArgs{Outcome: notify.OutcomeOK})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.o.Told(bg, "run_scan", EndOf(e)); err != nil {
		t.Fatal(err)
	}
	if n, err := r.o.ActiveIn(bg, r.chat); err != nil || n != 2 {
		t.Fatalf("ActiveIn = %d, %v; want the scan's two evaluations", n, err)
	}
	for _, id := range []string{"run_e1", "run_e2"} {
		if _, err := r.board.Move(bg, id, notify.Done, notify.MoveArgs{Outcome: notify.OutcomeOK}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := r.o.ActiveIn(bg, r.chat); err != nil || n != 0 {
		t.Fatalf("ActiveIn = %d, %v; the evaluations ended", n, err)
	}
}

// A queued start is work its conversation still has going, so a delegated task
// waits for it; the cap's own count leaves it out, or the start would wait for
// itself.
func TestOrigins_AQueuedStartCountsForItsConversationOnlyUntilUnqueued(t *testing.T) {
	r := newRig(t)
	q := QueuedStart{EntryID: "agent_q1", SessionID: r.chat, Label: "Starting a scan"}
	if err := r.o.Queue(bg, q); err != nil {
		t.Fatal(err)
	}
	if err := r.o.Queue(bg, QueuedStart{EntryID: "agent_q1", SessionID: r.chat, Label: "again"}); err != nil {
		t.Fatal(err)
	}
	if n, err := r.o.ActiveIn(bg, r.chat); err != nil || n != 1 {
		t.Fatalf("ActiveIn = %d, %v; want the queued start", n, err)
	}
	if n, err := r.o.Active(bg); err != nil || n != 0 {
		t.Fatalf("Active = %d, %v; a queued start is not a run going", n, err)
	}
	if got, err := r.o.QueuedStarts(bg); err != nil || len(got) != 1 || got[0] != q {
		t.Fatalf("QueuedStarts = %+v, %v", got, err)
	}
	if err := r.o.Unqueue(bg, q.EntryID); err != nil {
		t.Fatal(err)
	}
	if n, _ := r.o.ActiveIn(bg, r.chat); n != 0 {
		t.Fatalf("ActiveIn = %d after the start was unqueued", n)
	}
	if got, _ := r.o.QueuedStarts(bg); len(got) != 0 {
		t.Fatalf("QueuedStarts = %+v after unqueue", got)
	}
}

func TestOrigins_RetainDeletesOldOriginsWithTheirRequests(t *testing.T) {
	r := newRig(t)
	old := New(r.st, func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) })
	if err := old.RecordApply(bg, r.chat, ApplyRequest{RunID: "run_old", Tool: "autofill_application", Caller: "ask-agent", URL: "https://jobs.example.com/p/1"}); err != nil {
		t.Fatal(err)
	}
	if err := r.o.Record(bg, "run_new", r.chat, "scan_portal"); err != nil {
		t.Fatal(err)
	}
	if err := r.o.Retain(bg); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.o.Request(bg, "run_old"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an old origin or its request stayed: %v", err)
	}
	if _, err := r.o.Get(bg, "run_new"); err != nil {
		t.Fatalf("a new origin was deleted: %v", err)
	}
}

func TestOrigins_DeletingAConversationDeletesItsOrigins(t *testing.T) {
	r := newRig(t)
	if err := r.o.RecordApply(bg, r.chat, ApplyRequest{RunID: "run_1", Tool: "autofill_application", Caller: "ask-agent", URL: "https://jobs.example.com/p/1"}); err != nil {
		t.Fatal(err)
	}
	if err := chatsessions.New(r.st, nil).Delete(bg, r.chat); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.o.Request(bg, "run_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the origin outlived its conversation: %v", err)
	}
}
