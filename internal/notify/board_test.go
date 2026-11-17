// Clean-room: docs/provenance.md#CR-RUNS-BOARD

package notify

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

var bg = context.Background()

func TestNotify_Board_OneEntryPerRunWrittenThroughOneInterface(t *testing.T) {
	r := newRig(t)
	e, err := r.board.Open(bg, OpenArgs{ID: "r-1", Kind: KindScan, Origin: OriginSchedule, Title: "Scan", Link: "/scans/r-1", Ref: "run:r-1"})
	if err != nil {
		t.Fatal(err)
	}
	if e.State != Working || e.Kind != KindScan || e.Origin != OriginSchedule || e.Link != "/scans/r-1" || e.Revision != 1 {
		t.Fatalf("entry = %+v", e)
	}
	again, err := r.board.Open(bg, OpenArgs{ID: "r-1", Kind: KindScan, Origin: OriginSchedule, Title: "other"})
	if err != nil || again.Title != "Scan" {
		t.Fatalf("a second open must return the entry as it is: %+v %v", again, err)
	}
}

func TestNotify_States_ExactVocabulary(t *testing.T) {
	want := map[State][]State{
		Working:  {NeedsYou, Blocked, Done, Idle},
		NeedsYou: {Working, Blocked, Done},
		Blocked:  {Working, Done},
		Done:     {Working},
		Idle:     {Working},
	}
	all := []State{Working, NeedsYou, Blocked, Done, Idle}
	for _, from := range all {
		for _, to := range all {
			permitted := false
			for _, w := range want[from] {
				permitted = permitted || w == to
			}
			if CanMove(from, to) != permitted {
				t.Errorf("CanMove(%s, %s) = %v, want %v", from, to, !permitted, permitted)
			}
		}
	}
}

func TestNotify_States_ReasonSetsClosed(t *testing.T) {
	r := newRig(t)
	r.open("r-1", OriginUser, KindApply)
	if _, err := r.board.Move(bg, "r-1", NeedsYou, MoveArgs{Reason: "lockout"}); !errors.Is(err, ErrBadReason) {
		t.Errorf("a blocked reason on needs-you: %v", err)
	}
	if _, err := r.board.Move(bg, "r-1", NeedsYou, MoveArgs{}); !errors.Is(err, ErrBadReason) {
		t.Errorf("no reason: %v", err)
	}
	if _, err := r.board.Move(bg, "r-1", Done, MoveArgs{Outcome: "great"}); !errors.Is(err, ErrBadReason) {
		t.Errorf("an unknown outcome: %v", err)
	}
	for _, reason := range needsYouReasons {
		if !validReason(NeedsYou, reason) {
			t.Errorf("needs-you reason %q refused", reason)
		}
	}
	for _, reason := range blockedReasons {
		if !validReason(Blocked, reason) {
			t.Errorf("blocked reason %q refused", reason)
		}
	}
}

func TestNotify_States_IllegalTransitionRejected(t *testing.T) {
	r := newRig(t)
	r.open("r-1", OriginUser, KindApply)
	r.move("r-1", Blocked, MoveArgs{Reason: "lockout"})
	before, _ := r.board.Get(bg, "r-1")
	if _, err := r.board.Move(bg, "r-1", NeedsYou, MoveArgs{Reason: "sign-in"}); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("blocked to needs-you: %v", err)
	}
	after, _ := r.board.Get(bg, "r-1")
	if after.Revision != before.Revision || after.State != Blocked {
		t.Errorf("a rejected change altered the entry: %+v", after)
	}
}

func TestNotify_States_IdleQuiet(t *testing.T) {
	r := newRig(t)
	r.open("r-1", OriginUser, KindTask)
	r.move("r-1", Idle, MoveArgs{})
	r.process()
	if r.desktop.count() != 0 || r.tg.count() != 0 {
		t.Error("an idle entry was announced")
	}
	list, _ := r.board.List(bg, Filter{})
	if len(list) != 0 {
		t.Errorf("the default view shows an idle entry: %d", len(list))
	}
	list, _ = r.board.List(bg, Filter{States: []State{Idle}})
	if len(list) != 1 {
		t.Errorf("asking for idle entries shows %d", len(list))
	}
}

func TestNotify_List_TabbedKeepsTheEntriesThatOfferATabBeforeTheLimit(t *testing.T) {
	r := newRig(t)
	r.open("r-tab", OriginUser, KindApply)
	r.move("r-tab", NeedsYou, MoveArgs{Reason: "answer", FocusTab: "tab-1"})
	// Two entries updated later, neither offering a tab.
	for _, id := range []string{"r-2", "r-3"} {
		r.open(id, OriginUser, KindApply)
		r.move(id, NeedsYou, MoveArgs{Reason: "answer"})
	}
	list, err := r.board.List(bg, Filter{Tabbed: true, Limit: 1})
	if err != nil || len(list) != 1 || list[0].ID != "r-tab" {
		t.Fatalf("%+v %v", list, err)
	}
}

// After a host sleep the sweep's first pass finds every working entry older
// than five minutes, the ones of live producers too: they had no chance to
// touch. Nothing is finalized until five minutes of running time have passed.
func TestNotify_Liveness_ASweepAfterAPauseWaitsForTheProducers(t *testing.T) {
	r := newRig(t)
	r.open("alive", OriginUser, KindTask)
	r.open("dead", OriginUser, KindTask)
	if n, err := r.board.SweepStale(bg); err != nil || n != 0 {
		t.Fatalf("sweep = %d %v", n, err)
	}
	r.clk.advance(time.Hour) // the host slept: no sweep, no touch
	if n, err := r.board.SweepStale(bg); err != nil || n != 0 {
		t.Fatalf("the first sweep after the pause finalized %d entries (%v) before a producer could touch", n, err)
	}
	for range 6 {
		r.clk.advance(time.Minute)
		if err := r.board.Touch(bg, "alive"); err != nil {
			t.Fatal(err)
		}
		if _, err := r.board.SweepStale(bg); err != nil {
			t.Fatal(err)
		}
	}
	alive, _ := r.board.Get(bg, "alive")
	dead, _ := r.board.Get(bg, "dead")
	if alive.State != Working || dead.State != Done || dead.Outcome != OutcomeInterrupted {
		t.Fatalf("alive = %s, dead = %s/%s", alive.State, dead.State, dead.Outcome)
	}
}

func TestNotify_Liveness_RestartKeepsWaitingEntries(t *testing.T) {
	r := newRig(t)
	r.open("w", OriginUser, KindApply)
	r.open("n", OriginUser, KindApply)
	r.move("n", NeedsYou, MoveArgs{Reason: "sign-in"})
	r.build()
	if n, err := r.board.Restart(bg); err != nil || n != 1 {
		t.Fatalf("restart = %d %v", n, err)
	}
	w, _ := r.board.Get(bg, "w")
	n, _ := r.board.Get(bg, "n")
	if w.State != Done || w.Outcome != OutcomeInterrupted || n.State != NeedsYou {
		t.Errorf("w = %s/%s n = %s", w.State, w.Outcome, n.State)
	}
}

func TestNotify_Seen_OpeningMarksSeen(t *testing.T) {
	r := newRig(t)
	r.open("a", OriginUser, KindApply)
	r.open("b", OriginUser, KindApply)
	r.move("a", NeedsYou, MoveArgs{Reason: "answer"})
	r.move("b", NeedsYou, MoveArgs{Reason: "answer"})
	if _, err := r.board.Seen(bg, "a"); err != nil {
		t.Fatal(err)
	}
	s, _ := r.board.Summary(bg)
	if s.Unseen != 1 || s.NeedsYou != 2 {
		t.Errorf("summary = %+v", s)
	}
	in, _ := r.board.Inbox(bg)
	if len(in) != 2 {
		t.Errorf("a seen entry left the inbox: %d", len(in))
	}
}

func TestNotify_Ack_DoneLeavesViewWaitingLeavesInbox(t *testing.T) {
	r := newRig(t)
	r.open("d", OriginUser, KindEvaluate)
	r.move("d", Done, MoveArgs{Outcome: OutcomeOK})
	r.open("n", OriginUser, KindApply)
	r.move("n", NeedsYou, MoveArgs{Reason: "review"})
	if _, err := r.board.Ack(bg, "d"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.board.Ack(bg, "n"); err != nil {
		t.Fatal(err)
	}
	s, _ := r.board.Summary(bg)
	if s.DoneUnack != 0 || s.NeedsYou != 0 {
		t.Errorf("summary = %+v", s)
	}
	n, _ := r.board.Get(bg, "n")
	if n.State != NeedsYou {
		t.Errorf("acknowledging a waiting entry changed its state to %s", n.State)
	}
	list, _ := r.board.List(bg, Filter{})
	for _, e := range list {
		if e.ID == "d" {
			t.Error("the default view still shows the acknowledged finished entry")
		}
	}
	r.open("w", OriginUser, KindTask)
	if _, err := r.board.Ack(bg, "w"); !errors.Is(err, ErrNotAckable) {
		t.Errorf("acknowledging a working entry: %v", err)
	}
}

func TestNotify_Ack_NewProblemClearsMarks(t *testing.T) {
	r := newRig(t)
	r.open("e", OriginUser, KindApply)
	r.move("e", Blocked, MoveArgs{Reason: "lockout"})
	r.board.Seen(bg, "e")
	r.board.Ack(bg, "e")
	r.move("e", Working, MoveArgs{})
	e := r.move("e", NeedsYou, MoveArgs{Reason: "answer"})
	if e.Acked() || !e.SeenAt.IsZero() {
		t.Fatalf("marks survived: %+v", e)
	}
	in, _ := r.board.Inbox(bg)
	s, _ := r.board.Summary(bg)
	if len(in) != 1 || s.Unseen != 1 {
		t.Errorf("inbox %d, unseen %d", len(in), s.Unseen)
	}
	// A change of reason inside the state counts too.
	r.board.Seen(bg, "e")
	e = r.move("e", NeedsYou, MoveArgs{Reason: "challenge"})
	if !e.SeenAt.IsZero() {
		t.Error("a change of reason kept the seen mark")
	}
}

func TestNotify_Inbox_OrderNeedsYouBeforeBlockedLongestFirst(t *testing.T) {
	r := newRig(t)
	for _, id := range []string{"b3", "n1", "n2"} {
		r.open(id, OriginUser, KindApply)
	}
	r.move("b3", Blocked, MoveArgs{Reason: "lockout"})
	r.clk.advance(time.Hour)
	r.move("n2", NeedsYou, MoveArgs{Reason: "answer"})
	r.clk.advance(time.Hour)
	r.move("n1", NeedsYou, MoveArgs{Reason: "answer"})
	in, err := r.board.Inbox(bg)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range in {
		got = append(got, e.ID)
	}
	if strings.Join(got, " ") != "n2 n1 b3" {
		t.Errorf("order = %v", got)
	}
}

func TestNotify_API_RevisionAfterCommit(t *testing.T) {
	r := newRig(t)
	var seen Entry
	r.board = NewBoard(r.st, BoardOptions{Now: r.clk.Now, Changed: func(...string) {
		// The change must already be readable when the topic advances.
		seen, _ = r.board.Get(bg, "r-1")
	}})
	r.open("r-1", OriginUser, KindTask)
	if seen.ID != "r-1" {
		t.Error("the revision event came before the change was readable")
	}
	r.move("r-1", Done, MoveArgs{Outcome: OutcomeOK})
	if seen.State != Done {
		t.Errorf("state at the event = %s", seen.State)
	}
}

func TestNotify_Retention_DoneFourteenDaysWaitingThirtyKeepsOwnerRecord(t *testing.T) {
	r := newRig(t)
	now := ts(r.clk.Now())
	if err := r.st.Write(bg, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO runs (id, kind, status, created_at, updated_at) VALUES ('old', 'evaluate', 'succeeded', ?, ?)`, now, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r.open("old", OriginUser, KindEvaluate)
	r.move("old", Done, MoveArgs{Outcome: OutcomeOK})
	r.open("wait", OriginUser, KindApply)
	r.move("wait", NeedsYou, MoveArgs{Reason: "answer"})
	r.clk.advance(15 * 24 * time.Hour)
	if err := r.board.Retain(bg); err != nil {
		t.Fatal(err)
	}
	if _, err := r.board.Get(bg, "old"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a finished entry of 15 days survived: %v", err)
	}
	if w, _ := r.board.Get(bg, "wait"); w.State != NeedsYou {
		t.Errorf("a 15 day waiting entry is %s", w.State)
	}
	r.clk.advance(16 * 24 * time.Hour)
	if err := r.board.Retain(bg); err != nil {
		t.Fatal(err)
	}
	w, _ := r.board.Get(bg, "wait")
	if w.State != Done || w.Outcome != OutcomeInterrupted {
		t.Errorf("a 31 day waiting entry is %s/%s", w.State, w.Outcome)
	}
	// The owning subsystem's run record is a different table and is not touched.
	var n int
	if err := r.st.Read().QueryRow(`SELECT count(*) FROM runs WHERE id = 'old'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the run record was deleted with the entry: %d %v", n, err)
	}
}

func insertRun(t *testing.T, r *rig, id, kind, status, failure string, scheduled bool) {
	t.Helper()
	now := ts(r.clk.Now())
	err := r.st.Write(bg, func(tx *sql.Tx) error {
		if scheduled {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO schedules (id, name, job_kind, inputs_json, preset, enabled, missed_policy, dtstart, created_at, updated_at)
				VALUES (1, 's', ?, '{}', '{}', 1, 'skip', ?, ?, ?)`, kind, now, now, now); err != nil {
				return err
			}
			_, err := tx.Exec(`INSERT INTO runs (id, kind, status, failure_reason, schedule_id, scheduled_for, created_at, updated_at) VALUES (?, ?, ?, ?, 1, ?, ?, ?)`,
				id, kind, status, failure, now, now, now)
			return err
		}
		_, err := tx.Exec(`INSERT INTO runs (id, kind, status, failure_reason, input_json, created_at, updated_at) VALUES (?, ?, ?, ?, '{"url":"https://jobs.example.com/a"}', ?, ?)`,
			id, kind, status, failure, now, now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func setRun(t *testing.T, r *rig, id, status, failure string) {
	t.Helper()
	err := r.st.Write(bg, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE runs SET status = ?, failure_reason = ?, updated_at = ? WHERE id = ?`, status, failure, ts(r.clk.Now()), id)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A sync that runs out of time stops there instead of failing each record it
// has not reached, one warning apiece; the next sync puts them on the board.
func TestNotify_Board_ASyncOutOfTimeStopsAndTheNextFollows(t *testing.T) {
	r := newRig(t)
	ids := []string{"r-1", "r-2", "r-3"}
	for _, id := range ids {
		insertRun(t, r, id, "evaluate", "running", "", false)
	}
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	var logs bytes.Buffer
	// The first entry it opens ends the sync's time.
	b := NewBoard(r.st, BoardOptions{Now: r.clk.Now, Log: slog.New(slog.NewTextHandler(&logs, nil)), Changed: func(...string) { cancel() }})
	if _, err := b.SyncRuns(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the sync's own end", err)
	}
	if logs.Len() != 0 {
		t.Fatalf("a sync out of time logged:\n%s", logs.String())
	}
	if _, err := r.board.SyncRuns(bg); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if e, err := r.board.Get(bg, id); err != nil || e.State != Working {
			t.Errorf("%s after the next sync: %+v %v", id, e, err)
		}
	}
}

// A sync whose time ends between a record's stamp and its move leaves the
// entry working with the record's stamp. The next sync still moves it, so a
// run that waits for the user is not left for the stale sweep to end.
func TestNotify_Board_AFinishedRecordStillWorkingMovesAtTheNextSync(t *testing.T) {
	r := newRig(t)
	insertRun(t, r, "r-1", "evaluate", "running", "", false)
	if _, err := r.board.SyncRuns(bg); err != nil {
		t.Fatal(err)
	}
	r.clk.advance(time.Second)
	setRun(t, r, "r-1", "review_required", "")
	var stamp string
	if err := r.st.Read().QueryRow(`SELECT updated_at || '|' || status FROM runs WHERE id = 'r-1'`).Scan(&stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := r.board.Update(bg, "r-1", Patch{RefUpdated: &stamp}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.board.SyncRuns(bg); err != nil {
		t.Fatal(err)
	}
	if e, err := r.board.Get(bg, "r-1"); err != nil || e.State != NeedsYou || e.Reason != "review" {
		t.Errorf("r-1 after the next sync: %+v %v", e, err)
	}
}

// The reviews a later evaluation answers are resolved before its own entry
// moves: a sync whose time ends as that entry finishes has left none open.
func TestNotify_Board_ASyncOutOfTimeAfterAnEvaluationMovedLeavesNoReviewOpen(t *testing.T) {
	r := newRig(t)
	insertEval(t, r, "e-1", "", "k1")
	insertEval(t, r, "e-2", "", "k1")
	r.board.SyncRuns(bg)
	r.clk.advance(time.Second)
	setRun(t, r, "e-1", "review_required", "")
	r.board.SyncRuns(bg)
	r.clk.advance(time.Second)
	setRun(t, r, "e-2", "succeeded", "")
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	var logs bytes.Buffer
	var b *Board
	b = NewBoard(r.st, BoardOptions{Now: r.clk.Now, Log: slog.New(slog.NewTextHandler(&logs, nil)), Changed: func(...string) {
		if e, err := b.Get(bg, "e-2"); err == nil && e.State != Working {
			cancel()
		}
	}})
	b.SyncRuns(ctx)
	if logs.Len() != 0 {
		t.Fatalf("the sync logged:\n%s", logs.String())
	}
	if e, _ := r.board.Get(bg, "e-1"); e.State != Done || e.Outcome != OutcomeOK || !e.Acked() {
		t.Errorf("the answered review = %+v", e)
	}
	if e, _ := r.board.Get(bg, "e-2"); e.State != Done || e.Outcome != OutcomeOK {
		t.Errorf("the later evaluation = %+v", e)
	}
}

func TestNotify_Board_RunRecordsAreMirroredWithTheRunID(t *testing.T) {
	r := newRig(t)
	insertRun(t, r, "r-1", "evaluate", "running", "", false)
	insertRun(t, r, "r-2", "discover.greenhouse", "running", "", true)
	insertRun(t, r, "r-3", "autofill.extension", "running", "", false)
	if n, err := r.board.SyncRuns(bg); err != nil || n != 2 {
		t.Fatalf("sync = %d %v", n, err)
	}
	e, err := r.board.Get(bg, "r-1")
	if err != nil || e.Kind != KindEvaluate || e.Origin != OriginUser || e.State != Working || !strings.Contains(e.Title, "jobs.example.com") || e.Link != "/runs/r-1" {
		t.Fatalf("evaluation entry = %+v %v", e, err)
	}
	s, _ := r.board.Get(bg, "r-2")
	if s.Kind != KindScan || s.Origin != OriginSchedule || s.State != Working || s.Title != "Scan greenhouse" {
		t.Errorf("scheduled scan entry = %+v", s)
	}
	if _, err := r.board.Get(bg, "r-3"); err != ErrNotFound {
		t.Errorf("an extension fill is on the board: %v", err)
	}
	// The record ends: the entry follows and a second sync changes nothing.
	r.clk.advance(time.Second)
	setRun(t, r, "r-1", "succeeded", "")
	setRun(t, r, "r-2", "failed", "greenhouse is down")
	r.board.SyncRuns(bg)
	e, _ = r.board.Get(bg, "r-1")
	s, _ = r.board.Get(bg, "r-2")
	if e.State != Done || e.Outcome != OutcomeOK || s.Outcome != OutcomeFailed || s.Detail != "greenhouse is down" {
		t.Errorf("after the end: %+v %+v", e, s)
	}
	if n, _ := r.board.SyncRuns(bg); n != 0 {
		t.Errorf("a second sync changed %d entries", n)
	}
}

// A run's end is announced with what it came to: the producer's outcome
// sentence is on the entry before the move announces it (F-2).
func TestNotify_Board_ARunEndIsAnnouncedWithItsOutcome(t *testing.T) {
	r := newRig(t)
	r.board.SetOutcome(func(_ context.Context, kind Kind, id string) string {
		if kind == KindEvaluate && id == "r-1" {
			return "Scored 4.2 (Apply now), report 37."
		}
		return ""
	})
	insertRun(t, r, "r-1", "evaluate", "running", "", false)
	insertRun(t, r, "r-2", "evaluate", "running", "", false)
	r.board.SyncRuns(bg)
	r.clk.advance(time.Second)
	setRun(t, r, "r-1", "succeeded", "")
	setRun(t, r, "r-2", "failed", "auth")
	r.board.SyncRuns(bg)
	if e, _ := r.board.Get(bg, "r-1"); e.Detail != "Scored 4.2 (Apply now), report 37." {
		t.Fatalf("detail = %q", e.Detail)
	}
	r.clk.advance(DefaultDoneDelay + time.Second)
	r.process()
	bodies := map[string]string{}
	for _, a := range r.desktop.announced {
		bodies[a.EntryID] = a.Body
	}
	if bodies["r-1"] != "Scored 4.2 (Apply now), report 37." || !strings.HasPrefix(bodies["r-2"], "It failed. The model provider refused the API key") {
		t.Errorf("announced = %q", bodies)
	}
}

// A run the Ask Agent started is labelled with the agent's origin, also when
// the agent noted the origin only after a sync had opened the entry as the
// user's (an evaluation that ended at once as a duplicate).
func TestNotify_Board_ARunTheAgentStartedIsTheAgents(t *testing.T) {
	r := newRig(t)
	now := ts(r.clk.Now())
	origin := func(run string) {
		t.Helper()
		if err := r.st.Write(bg, func(tx *sql.Tx) error {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO chat_sessions (id, channel, created_at, updated_at) VALUES ('ses-1', 'dashboard', ?, ?)`, now, now); err != nil {
				return err
			}
			_, err := tx.Exec(`INSERT INTO agent_run_origins (run_id, session_id, tool, created_at) VALUES (?, 'ses-1', 'evaluate_job', ?)`, run, now)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	insertRun(t, r, "a-1", "evaluate", "running", "", false)
	origin("a-1")
	insertRun(t, r, "a-2", "evaluate", "succeeded", "", false)
	insertRun(t, r, "u-1", "evaluate", "running", "", false)
	r.board.SyncRuns(bg)
	for id, want := range map[string]Origin{"a-1": OriginAgent, "a-2": OriginUser, "u-1": OriginUser} {
		if e, _ := r.board.Get(bg, id); e.Origin != want {
			t.Errorf("%s before its origin was noted: %q, want %q", id, e.Origin, want)
		}
	}
	origin("a-2")
	if n, err := r.board.SyncRuns(bg); err != nil || n != 1 {
		t.Fatalf("sync after the late origin = %d %v", n, err)
	}
	if e, _ := r.board.Get(bg, "a-2"); e.Origin != OriginAgent || e.State != Done {
		t.Errorf("late origin entry = %+v", e)
	}
	if n, _ := r.board.SyncRuns(bg); n != 0 {
		t.Errorf("a second sync changed %d entries", n)
	}
}

func TestNotify_Board_ReviewRequiredNeedsYouAndSyncKeepsWorkingAlive(t *testing.T) {
	r := newRig(t)
	insertRun(t, r, "r-1", "evaluate", "running", "", false)
	insertRun(t, r, "r-2", "evaluate", "running", "", false)
	r.board.SyncRuns(bg)
	r.clk.advance(4 * time.Minute)
	r.board.SyncRuns(bg) // touches the working entries: the record has not ended
	r.clk.advance(2 * time.Minute)
	setRun(t, r, "r-1", "review_required", "")
	r.board.SyncRuns(bg)
	if n, _ := r.board.SweepStale(bg); n != 0 {
		t.Errorf("a mirrored run that has not ended was finalized as silent")
	}
	e, _ := r.board.Get(bg, "r-1")
	if e.State != NeedsYou || e.Reason != "review" {
		t.Errorf("r-1 = %s/%s", e.State, e.Reason)
	}
	// A wait raised by someone else survives the record ending.
	r.board.Raise(bg, RaiseArgs{ID: "r-2", Kind: KindEvaluate, Origin: OriginUser, Title: "t", State: Blocked, Reason: "key-missing"})
	setRun(t, r, "r-2", "failed", "x")
	r.board.SyncRuns(bg)
	if e, _ := r.board.Get(bg, "r-2"); e.State != Blocked {
		t.Errorf("r-2 = %s", e.State)
	}
}

func TestNotify_Board_RaiseAndResolve(t *testing.T) {
	r := newRig(t)
	a := RaiseArgs{ID: "gmail-auth", Kind: KindTask, Origin: OriginSchedule, Title: "Gmail needs you", State: NeedsYou, Reason: "sign-in", Detail: "Reconnect Gmail", Link: "/gmail"}
	e, err := r.board.Raise(bg, a)
	if err != nil || e.State != NeedsYou {
		t.Fatalf("raise = %+v %v", e, err)
	}
	again, _ := r.board.Raise(bg, a)
	if again.Revision != e.Revision {
		t.Errorf("raising twice changed the entry: %d -> %d", e.Revision, again.Revision)
	}
	if in, _ := r.board.Inbox(bg); len(in) != 1 {
		t.Fatalf("inbox = %d", len(in))
	}
	if err := r.board.Resolve(bg, "gmail-auth"); err != nil {
		t.Fatal(err)
	}
	if in, _ := r.board.Inbox(bg); len(in) != 0 {
		t.Errorf("inbox after reconnect = %d", len(in))
	}
	// A new outage after the old one was resolved is a new problem.
	if e, err := r.board.Raise(bg, a); err != nil || e.State != NeedsYou || e.Epoch < 3 {
		t.Errorf("second outage = %+v %v", e, err)
	}
	// Escalating from needs-you to blocked goes through working.
	if e, err := r.board.Raise(bg, RaiseArgs{ID: "gmail-auth", Kind: KindTask, Origin: OriginSchedule, Title: "t", State: Blocked, Reason: "no-connection"}); err != nil || e.State != Blocked {
		t.Errorf("escalation = %+v %v", e, err)
	}
}
