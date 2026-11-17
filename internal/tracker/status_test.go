package tracker

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"applysmith/internal/store"
)

func TestTransitions_UserCorrection(t *testing.T) {
	f := newFixture(t)
	e := f.add(AddRow{Company: "Example Corp", Role: "Engineer", URL: "https://example.com/1"})
	f.setStatus(e.ID, Rejected, ActorUser)
	res := f.setStatus(e.ID, Interview, ActorUser)
	if !res.Changed || !res.Correction {
		t.Fatalf("%+v", res)
	}
	evs := f.events(e.ID, EventStatusCorrected)
	if len(evs) != 1 || evs[0].Actor != ActorUser || evs[0].PreviousStatus != Rejected || evs[0].NewStatus != Interview {
		t.Errorf("%+v", evs)
	}
}

func TestTransitions_SameStatusNoop(t *testing.T) {
	f := newFixture(t)
	e := f.add(AddRow{Company: "Example Corp", Role: "Engineer", URL: "https://example.com/1"})
	before := f.get(e.ID)
	res := f.setStatus(e.ID, Evaluated, ActorUser)
	after := f.get(e.ID)
	if res.Changed || len(after.Events) != len(before.Events) || after.Entry.Version != before.Entry.Version {
		t.Errorf("%+v", res)
	}
}

func TestTransitions_InferredAppliedEvent(t *testing.T) {
	f := newFixture(t)
	e := f.add(AddRow{Company: "Example Corp", Role: "Engineer", URL: "https://example.com/1"})
	res := f.setStatus(e.ID, Responded, ActorGmail)
	if !res.Inferred {
		t.Error("not flagged")
	}
	evs := f.events(e.ID, EventStatusChanged)
	if !strings.Contains(string(evs[0].Detail), `"inferred_applied":true`) {
		t.Errorf("%s", evs[0].Detail)
	}
	if got := f.get(e.ID).Entry; got.AppliedBy != "" || got.AppliedAt != "" {
		t.Errorf("a row never marked applied gets no stamp: %+v", got)
	}
}

func TestTransitions_RefusalWritesOneEventPerDay(t *testing.T) {
	f := newFixture(t)
	e := f.add(AddRow{Company: "Example Corp", Role: "Engineer", URL: "https://example.com/1"})
	f.setStatus(e.ID, Interview, ActorUser)
	for i := 0; i < 50; i++ {
		if _, err := f.tr.SetStatus(f.ctx, StatusChange{ID: e.ID, To: Applied, Actor: ActorGmail}); !errors.Is(err, ErrTransitionRefused) {
			t.Fatal(err)
		}
	}
	if n := len(f.events(e.ID, EventTransitionRefused)); n != 1 {
		t.Fatalf("%d events", n)
	}
	// A different target, a different actor and the next day each get their own.
	f.tr.SetStatus(f.ctx, StatusChange{ID: e.ID, To: Evaluated, Actor: ActorGmail})
	f.tr.SetStatus(f.ctx, StatusChange{ID: e.ID, To: Applied, Actor: ActorAgent, AppliedBy: ByAgent})
	f.clk.advance(24 * time.Hour)
	f.tr.SetStatus(f.ctx, StatusChange{ID: e.ID, To: Applied, Actor: ActorGmail})
	if n := len(f.events(e.ID, EventTransitionRefused)); n != 4 {
		t.Errorf("%d events, want 4", n)
	}
}

func TestTransitions_GuardReadsCurrentRowInTransaction(t *testing.T) {
	f := newFixture(t)
	e := f.add(AddRow{Company: "Example Corp", Role: "Engineer", URL: "https://example.com/1"})
	f.setStatus(e.ID, Applied, ActorUser)
	// An automated change planned while the row was applied ...
	planned := StatusChange{ID: e.ID, To: Interview, Actor: ActorGmail}
	// ... meets a row that has become rejected in the meantime.
	f.setStatus(e.ID, Rejected, ActorUser)
	if _, err := f.tr.SetStatus(f.ctx, planned); !errors.Is(err, ErrTransitionRefused) {
		t.Fatalf("%v", err)
	}
	if got := f.get(e.ID).Entry.Status; got != Rejected {
		t.Errorf("status = %s", got)
	}
}

func TestTransitions_ArchivedRowRefusesAutomatedActors(t *testing.T) {
	f := newFixture(t)
	e := f.add(AddRow{Company: "Example Corp", Role: "Engineer", URL: "https://example.com/1"})
	f.setStatus(e.ID, Applied, ActorUser)
	f.exec(`UPDATE tracker_entries SET archived_at = '2027-01-01T00:00:00Z' WHERE id = ?`, e.ID)
	if _, err := f.tr.SetStatus(f.ctx, StatusChange{ID: e.ID, To: Responded, Actor: ActorGmail}); !errors.Is(err, ErrTransitionRefused) {
		t.Fatalf("%v", err)
	}
	if _, err := f.tr.SetStatus(f.ctx, StatusChange{ID: e.ID, To: Responded, Actor: ActorUser}); err != nil {
		t.Errorf("the user may change an archived row: %v", err)
	}
}

func TestTransitions_UnknownStatusAndRows(t *testing.T) {
	f := newFixture(t)
	e := f.add(AddRow{Company: "Example Corp", Role: "Engineer", URL: "https://example.com/1"})
	if _, err := f.tr.SetStatus(f.ctx, StatusChange{ID: e.ID, To: "maybe", Actor: ActorUser}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "discarded") {
		t.Errorf("%v", err)
	}
	if _, err := f.tr.SetStatus(f.ctx, StatusChange{ID: 99, To: Applied, Actor: ActorUser}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("%v", err)
	}
	if _, err := f.tr.SetStatus(f.ctx, StatusChange{ID: e.ID, To: Applied, Actor: "root"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("%v", err)
	}
}

func TestTracker_AgentMark_RelayedUserStatement(t *testing.T) {
	f := newFixture(t)
	e := f.add(AddRow{Company: "Example Corp", Role: "Engineer", URL: "https://example.com/1"})
	res, err := f.tr.SetStatus(f.ctx, StatusChange{ID: e.ID, To: Applied, Actor: ActorAgent, AppliedBy: ByUser})
	if err != nil {
		t.Fatal(err)
	}
	if res.Entry.AppliedBy != ByUser {
		t.Errorf("%+v", res.Entry)
	}
	if ev := f.events(e.ID, EventStatusChanged)[0]; !strings.Contains(string(ev.Detail), "relayed_user_statement") || ev.Actor != ActorAgent {
		t.Errorf("%+v", ev)
	}
	e2 := f.add(AddRow{Company: "Sample Corp", Role: "Engineer", URL: "https://example.org/2"})
	if _, err := f.tr.SetStatus(f.ctx, StatusChange{ID: e2.ID, To: Applied, Actor: ActorAgent}); !errors.Is(err, ErrInvalid) {
		t.Errorf("the agent must pass applied_by explicitly: %v", err)
	}
}

func TestTracker_AppliedByUserOrGmail(t *testing.T) {
	f := newFixture(t)
	for i, actor := range []Actor{ActorUser, ActorGmail} {
		e := f.add(AddRow{Company: "Example Corp", Role: "Engineer " + string(rune('A'+i)), URL: "https://example.com/" + string(rune('a'+i))})
		if got := f.setStatus(e.ID, Applied, actor).Entry.AppliedBy; got != ByUser {
			t.Errorf("%s: applied_by = %q", actor, got)
		}
	}
}

func TestTracker_AgentMark_UserOrGmailSettlingAChainSubmission(t *testing.T) {
	f := newFixture(t)
	press := func(e Entry, source string) {
		f.t.Helper()
		if err := f.st.Write(f.ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO submissions (identity_key, url, host, state, confirmed, source, observed_at, created_at, updated_at)
				VALUES (?, ?, 'example.com', 'may-have-submitted', 0, ?, '2027-03-10T09:00:00.000Z', '2027-03-10T09:00:00.000Z', '2027-03-10T09:00:00.000Z')`,
				e.IdentityKey, e.PostingURL, source)
			return err
		}); err != nil {
			f.t.Fatal(err)
		}
	}
	for i, actor := range []Actor{ActorUser, ActorGmail} {
		e := f.add(AddRow{Company: "Example Corp", Role: "Engineer " + string(rune('A'+i)), URL: "https://example.com/" + string(rune('a'+i))})
		press(e, "apply-chain")
		res, err := f.tr.SetStatus(f.ctx, StatusChange{ID: e.ID, To: Applied, Actor: actor, AppliedBy: ByAgent})
		if err != nil || res.Entry.AppliedBy != ByAgent {
			t.Fatalf("%s: %+v %v", actor, res.Entry, err)
		}
		if ev := f.events(e.ID, EventStatusChanged)[0]; ev.Actor != actor {
			t.Errorf("the history names %s as who changed it: %+v", actor, ev)
		}
	}
	t.Run("without a press of the chain the mark is refused", func(t *testing.T) {
		e := f.add(AddRow{Company: "Sample Corp", Role: "Engineer", URL: "https://example.org/3"})
		press(e, "extension-claim")
		if _, err := f.tr.SetStatus(f.ctx, StatusChange{ID: e.ID, To: Applied, Actor: ActorGmail, AppliedBy: ByAgent}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("err = %v", err)
		}
		if got := f.get(e.ID).Entry; got.Status != Evaluated || got.AppliedBy != "" {
			t.Fatalf("a refused change changed the row: %+v", got)
		}
	})
}

func TestTracker_AppliedAtStickyOnFirstEntry(t *testing.T) {
	f := newFixture(t)
	e := f.add(AddRow{Company: "Example Corp", Role: "Engineer", URL: "https://example.com/1"})
	f.setStatus(e.ID, Applied, ActorUser)
	f.clk.advance(72 * time.Hour)
	f.setStatus(e.ID, Rejected, ActorGmail)
	got := f.get(e.ID).Entry
	if got.AppliedAt != "2027-03-10" {
		t.Errorf("applied_at = %q", got.AppliedAt)
	}
	f.setStatus(e.ID, Evaluated, ActorUser) // correction back
	f.setStatus(e.ID, Applied, ActorUser)
	if got := f.get(e.ID).Entry.AppliedAt; got != "2027-03-10" {
		t.Errorf("a correction must keep the stamp, got %q", got)
	}
}

func TestTracker_LaterStatusesKeepTheAgentMark(t *testing.T) {
	f := newFixture(t)
	e := f.upsert(report(1, "Example Corp", "Engineer", "https://example.com/1", 4.0)).Entry
	f.setStatus(e.ID, Applied, ActorApplyChain)
	got := f.setStatus(e.ID, Interview, ActorGmail).Entry
	if got.AppliedBy != ByAgent || got.DisplayStatus() != "interview (agent)" {
		t.Errorf("%+v", got)
	}
}

func TestTracker_SetStatusOnDate(t *testing.T) {
	f := newFixture(t)
	e := f.add(AddRow{Company: "Example Corp", Role: "Engineer", URL: "https://example.com/1"})
	res, err := f.tr.SetStatus(f.ctx, StatusChange{ID: e.ID, To: Applied, Actor: ActorUser, On: "2027-03-01"})
	if err != nil || res.Entry.AppliedAt != "2027-03-01" {
		t.Fatalf("%+v %v", res.Entry, err)
	}
	if n := len(f.events(e.ID, EventAppliedDateSet)); n != 1 {
		t.Errorf("applied_date_set events = %d", n)
	}
	if _, err := f.tr.SetStatus(f.ctx, StatusChange{ID: e.ID, To: Offer, Actor: ActorUser, On: "yesterday"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("%v", err)
	}
	// Only a row that was applied for has an application date.
	if _, err := f.tr.Add(f.ctx, AddRow{Company: "Seen Corp", Role: "Engineer", URL: "https://example.com/s", Status: Evaluated, AppliedAt: "2027-03-01"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("an application date on an evaluated row: %v", err)
	}
	// Only the user may set the date.
	e2 := f.add(AddRow{Company: "Sample Corp", Role: "Engineer", URL: "https://example.org/2"})
	r, _ := f.tr.SetStatus(f.ctx, StatusChange{ID: e2.ID, To: Applied, Actor: ActorGmail, On: "2027-01-01"})
	if r.Entry.AppliedAt != "2027-03-10" {
		t.Errorf("gmail set the date: %q", r.Entry.AppliedAt)
	}
}

func TestEvents_ChangeAndEventAreAtomic(t *testing.T) {
	f := newFixture(t)
	e := f.add(AddRow{Company: "Example Corp", Role: "Engineer", URL: "https://example.com/1"})
	f.exec(`CREATE TRIGGER fail_status_event BEFORE INSERT ON tracker_events WHEN NEW.kind = 'status_changed' BEGIN SELECT RAISE(ABORT, 'disk said no'); END`)
	if _, err := f.tr.SetStatus(f.ctx, StatusChange{ID: e.ID, To: Applied, Actor: ActorUser}); err == nil {
		t.Fatal("the change succeeded although its event failed")
	}
	got := f.get(e.ID)
	if got.Entry.Status != Evaluated || got.Entry.Version != 1 || len(got.Events) != 1 {
		t.Errorf("partial write: %+v", got)
	}
}

func TestEvents_AppendOnly(t *testing.T) {
	f := newFixture(t)
	e := f.add(AddRow{Company: "Example Corp", Role: "Engineer", URL: "https://example.com/1"})
	for _, q := range []string{
		`UPDATE tracker_events SET kind = 'archived'`,
		`UPDATE tracker_events SET actor = 'agent'`,
		`UPDATE tracker_events SET detail_json = '{}'`,
		`DELETE FROM tracker_events`,
	} {
		err := f.st.Write(f.ctx, func(tx *sql.Tx) error { _, err := tx.Exec(q); return err })
		if err == nil {
			t.Errorf("%s was accepted", q)
		}
	}
	if len(f.get(e.ID).Events) != 1 {
		t.Error("events changed")
	}
}

func TestTracker_StaleVersionConflict(t *testing.T) {
	f := newFixture(t)
	e := f.add(AddRow{Company: "Example Corp", Role: "Engineer", URL: "https://example.com/1"})
	if _, err := f.tr.AddNote(f.ctx, e.ID, ActorUser, "first", e.Version); err != nil {
		t.Fatal(err)
	}
	_, err := f.tr.AddNote(f.ctx, e.ID, ActorUser, "second", e.Version)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("%v", err)
	}
	if _, err := f.tr.SetStatus(f.ctx, StatusChange{ID: e.ID, To: Applied, Actor: ActorUser, ExpectedVersion: e.Version}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("%v", err)
	}
	got := f.get(e.ID).Entry
	if strings.Contains(got.Notes, "second") || got.Status != Evaluated {
		t.Errorf("stale edit applied: %+v", got)
	}
}

func TestNotes_CapAndEvents(t *testing.T) {
	f := newFixture(t)
	e := f.add(AddRow{Company: "Example Corp", Role: "Engineer", URL: "https://example.com/1"})
	for i := 1; i <= 11; i++ {
		if _, err := f.tr.AddNote(f.ctx, e.ID, ActorUser, "note number "+string(rune('a'+i)), 0); err != nil {
			t.Fatal(err)
		}
	}
	got := f.get(e.ID)
	lines := strings.Split(got.Entry.Notes, "\n")
	if len(lines) != 10 || strings.Contains(got.Entry.Notes, "note number b") || !strings.Contains(got.Entry.Notes, "2027-03-10 user: note number l") {
		t.Errorf("%q", got.Entry.Notes)
	}
	if n := len(f.events(e.ID, EventNoteAdded)); n != 11 {
		t.Errorf("the oldest note must remain in its event: %d events", n)
	}
	if _, err := f.tr.AddNote(f.ctx, e.ID, ActorUser, strings.Repeat("x", 1001), 0); !errors.Is(err, ErrInvalid) {
		t.Errorf("%v", err)
	}
	if _, err := f.tr.AddNote(f.ctx, e.ID, ActorUser, "  ", 0); !errors.Is(err, ErrInvalid) {
		t.Errorf("%v", err)
	}
}

func TestDocs_StickyFlags(t *testing.T) {
	f := newFixture(t)
	e := f.upsert(report(1, "Example Corp", "Engineer", "https://example.com/1", 3.0)).Entry
	if _, err := f.tr.SetDocumentFlag(f.ctx, e.ID, ActorAgent, DocResume, true); err != nil {
		t.Fatal(err)
	}
	// Re-evaluation, mirror attach and score update leave the flag alone.
	f.upsert(report(2, "Example Corp", "Engineer", "https://example.com/1", 4.5))
	f.upsert(report(3, "Example Corp", "Engineer", "https://mirror.example.net/9", 4.9))
	if !f.get(e.ID).Entry.ResumePDF {
		t.Error("re-evaluation reset resume_pdf")
	}
	if _, err := f.tr.SetDocumentFlag(f.ctx, e.ID, ActorAgent, DocResume, false); !errors.Is(err, ErrForbidden) {
		t.Errorf("%v", err)
	}
	if !f.get(e.ID).Entry.ResumePDF {
		t.Error("an agent cleared the flag")
	}
	got, err := f.tr.SetDocumentFlag(f.ctx, e.ID, ActorUser, DocResume, false)
	if err != nil || got.ResumePDF {
		t.Fatalf("%+v %v", got, err)
	}
	if len(f.events(e.ID, EventDocumentFlagSet)) != 1 || len(f.events(e.ID, EventDocumentFlagCleared)) != 1 {
		t.Error("flag events missing")
	}
	// Setting an already-set flag writes nothing.
	if _, err := f.tr.SetDocumentFlag(f.ctx, e.ID, ActorUser, DocCover, true); err != nil {
		t.Fatal(err)
	}
	before := len(f.get(e.ID).Events)
	if _, err := f.tr.SetDocumentFlag(f.ctx, e.ID, ActorUser, DocCover, true); err != nil {
		t.Fatal(err)
	}
	if len(f.get(e.ID).Events) != before {
		t.Error("no-op wrote an event")
	}
}

func TestBulkStatus_AllOrNothing(t *testing.T) {
	f := newFixture(t)
	var ids []int64
	for _, n := range []string{"A", "B", "C", "D"} {
		ids = append(ids, f.add(AddRow{Company: n + " Corp", Role: "Engineer", URL: "https://example.com/" + n}).ID)
	}
	if _, err := f.tr.BulkSetStatus(f.ctx, append(ids, 999), Applied, ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("%v", err)
	}
	for _, id := range ids {
		if f.get(id).Entry.Status != Evaluated {
			t.Errorf("row %d changed", id)
		}
	}
	res, err := f.tr.BulkSetStatus(f.ctx, ids, Rejected, "bulk close")
	if err != nil || len(res) != 4 || !res[0].Changed {
		t.Fatalf("%v %v", res, err)
	}
	if _, err := f.tr.BulkSetStatus(f.ctx, nil, Applied, ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("%v", err)
	}
	if _, err := f.tr.BulkSetStatus(f.ctx, make([]int64, 101), Applied, ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("%v", err)
	}
	if _, err := f.tr.BulkSetStatus(f.ctx, ids, "maybe", ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("%v", err)
	}
}

func TestAdd_Rules(t *testing.T) {
	f := newFixture(t)
	e := f.add(AddRow{Company: "Example Corp", Role: "Engineer", URL: "https://example.com/jobs/1?utm_source=x", Status: Applied, AppliedAt: "2027-02-01"})
	if e.Source != "manual" || e.AppliedBy != ByUser || e.AppliedAt != "2027-02-01" || e.PostingURL != "https://example.com/jobs/1" || e.Status != Applied {
		t.Errorf("%+v", e)
	}
	if evs := f.events(e.ID, EventCreated); len(evs) != 1 || evs[0].Actor != ActorUser {
		t.Errorf("%+v", evs)
	}
	_, err := f.tr.Add(f.ctx, AddRow{Company: "Example Corp", Role: "Engineer", URL: "https://example.com/jobs/1"})
	var dup *DuplicateError
	if !errors.Is(err, ErrDuplicate) || !errors.As(err, &dup) || dup.ID != e.ID {
		t.Fatalf("%v", err)
	}
	if f.count(`SELECT count(*) FROM tracker_entries`) != 1 {
		t.Error("a duplicate created a row")
	}
	if _, err := f.tr.Add(f.ctx, AddRow{Company: "X Corp", Role: "Y", Status: "maybe"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("%v", err)
	}
	// A placeholder company with no URL still gets a row of its own.
	a := f.add(AddRow{Company: "Unknown", Role: "Engineer"})
	b := f.add(AddRow{Company: "Unknown", Role: "Engineer"})
	if a.ID == b.ID || !strings.HasPrefix(a.IdentityKey, "unkeyed:") {
		t.Errorf("%+v %+v", a, b)
	}
}

func TestAdd_NoURLRowsKeyOnCompanyRoleAndPlace(t *testing.T) {
	f := newFixture(t)
	a := f.add(AddRow{Company: "Example Corp", Role: "Engineer", Location: "Exampleton"})
	if a.IdentityKey != "example|engineer|exampleton" {
		t.Errorf("%q", a.IdentityKey)
	}
	if _, err := f.tr.Add(f.ctx, AddRow{Company: "Example, Inc.", Role: "engineer", Location: "Exampleton"}); !errors.Is(err, ErrDuplicate) {
		t.Errorf("%v", err)
	}
	if _, err := f.tr.Add(f.ctx, AddRow{Company: "Example Corp", Role: "Engineer", Location: "Sampleville"}); err != nil {
		t.Errorf("a second place is a second opening: %v", err)
	}
}

func TestTracker_SetStatusOnIsRefusedWhenItCannotApply(t *testing.T) {
	f := newFixture(t)
	e := f.add(AddRow{Company: "Example Corp", Role: "Engineer", URL: "https://example.com/1"})
	for _, to := range []Status{Skipped, Interview, Offer} {
		_, err := f.tr.SetStatus(f.ctx, StatusChange{ID: e.ID, To: to, Actor: ActorUser, On: "2027-03-01"})
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "--on") {
			t.Errorf("--on with %s must be refused, got %v", to, err)
		}
	}
	if got := f.get(e.ID); got.Entry.Status != Evaluated || len(got.Events) != 1 {
		t.Errorf("a refused change wrote something: %+v", got)
	}
	f.setStatus(e.ID, Applied, ActorUser)
	_, err := f.tr.SetStatus(f.ctx, StatusChange{ID: e.ID, To: Applied, Actor: ActorUser, On: "2027-03-01"})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("--on on an already applied row must be refused, got %v", err)
	}
}

func TestStatus_DetailNamesTheSourceInTheEventAndTheTrackersKeysWin(t *testing.T) {
	f := newFixture(t)
	e := f.add(AddRow{Company: "Example Corp", Role: "Engineer", URL: "https://example.com/1"})
	_, err := f.tr.SetStatus(f.ctx, StatusChange{ID: e.ID, To: Applied, Actor: ActorGmail,
		Detail: map[string]any{"source_message_id": "m-1", "signal_type": "applied_confirmation", "applied_by": "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	evs := f.events(e.ID, EventStatusChanged)
	if len(evs) != 1 {
		t.Fatalf("events = %+v", evs)
	}
	var detail map[string]any
	if err := json.Unmarshal(evs[0].Detail, &detail); err != nil {
		t.Fatal(err)
	}
	if detail["source_message_id"] != "m-1" || detail["signal_type"] != "applied_confirmation" || detail["applied_by"] != "user" {
		t.Errorf("detail = %v", detail)
	}
}

// RevertStatus is the one-transaction undo of a change a source made: the row
// goes back with the caller's own write, or neither happens.
func TestStatus_RevertPutsTheRowBackWithTheCallersWriteOrNeither(t *testing.T) {
	f := newFixture(t)
	e := f.add(AddRow{Company: "Example Corp", Role: "Engineer", URL: "https://example.com/1"})
	if _, err := f.tr.SetStatus(f.ctx, StatusChange{ID: e.ID, To: Interview, Actor: ActorUser, Note: "Phone screen booked."}); err != nil {
		t.Fatal(err)
	}
	move := func() {
		if _, err := f.tr.SetStatus(f.ctx, StatusChange{ID: e.ID, To: Offer, Actor: ActorUser, Note: "Offer from mail.", Detail: map[string]any{"source_message_id": "m-1"}}); err != nil {
			t.Fatal(err)
		}
	}
	move()
	ran := 0
	also := func(err error) func(*sql.Tx) error { return func(*sql.Tx) error { ran++; return err } }
	if _, err := f.tr.RevertStatus(f.ctx, e.ID, "source_message_id", "m-1", also(errors.New("boom"))); err == nil || f.get(e.ID).Entry.Status != Offer {
		t.Fatalf("a failed caller write left the row at %s (%v)", f.get(e.ID).Entry.Status, err)
	}
	if got, err := f.tr.RevertStatus(f.ctx, e.ID, "source_message_id", "m-1", also(nil)); err != nil || got.Status != Interview || ran != 2 {
		t.Fatalf("revert = %+v, %v, also ran %d", got, err, ran)
	}
	// The reverted change's note goes with it; an earlier note stays.
	if n := f.get(e.ID).Entry.Notes; strings.Contains(n, "Offer from mail.") || !strings.Contains(n, "Phone screen booked.") {
		t.Fatalf("notes after the revert = %q", n)
	}
	// A row moved on since the change is left as it is, and the caller's write
	// does not run.
	move()
	f.setStatus(e.ID, Rejected, ActorUser)
	if _, err := f.tr.RevertStatus(f.ctx, e.ID, "source_message_id", "m-1", also(nil)); !errors.Is(err, store.ErrConflict) || f.get(e.ID).Entry.Status != Rejected || ran != 2 {
		t.Fatalf("after a later change: %v, %s, also ran %d", err, f.get(e.ID).Entry.Status, ran)
	}
}
