// Clean-room: docs/provenance.md#CR-FOLLOWUP
// Written from the application-tracker spec alone.

package tracker

import (
	"errors"
	"strings"
	"testing"
	"time"

	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

const day = 24 * time.Hour

func defaultFollowup() workspace.Followup { return workspace.DefaultPolicy().Tracker.Followup }

// appliedOn adds an applied row whose applied_at is the given day.
func (f *fixture) appliedOn(company, appliedAt string) Entry {
	f.t.Helper()
	return f.add(AddRow{Company: company, Role: "Engineer", URL: "https://example.com/" + strings.ToLower(company), Status: Applied, AppliedAt: appliedAt})
}

func (f *fixture) followups(pol workspace.Followup, dueOnly bool) []Suggestion {
	f.t.Helper()
	out, err := f.tr.Followups(f.ctx, pol, dueOnly)
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func kinds(s []Suggestion) string {
	parts := make([]string, len(s))
	for i, x := range s {
		parts[i] = string(x.Kind)
	}
	return strings.Join(parts, ",")
}

func TestFollowup_Cadence_Table(t *testing.T) {
	// Today is the fixture day. Each case is one applied row, listed with --due.
	tests := []struct {
		name      string
		appliedAt string
		setup     func(f *fixture, id int64)
		wantDue   string // kinds listed with --due
		wantNum   int
	}{
		{"seven days is early", "2027-03-03", nil, "", 0},
		{"eight days is due", "2027-03-02", nil, "follow_up", 1},
		{"one recorded follow-up waits seven days", "2027-02-20", func(f *fixture, id int64) {
			f.recordAt(id, "2027-03-04") // due again a week later
		}, "", 0},
		{"second follow-up due seven days after the first", "2027-02-20", func(f *fixture, id int64) {
			f.recordAt(id, "2027-03-03")
		}, "follow_up", 2},
		{"two recorded follow-ups exhaust the cadence", "2027-02-20", func(f *fixture, id int64) {
			f.recordAt(id, "2027-02-28")
			f.recordAt(id, "2027-03-07")
		}, "cadence_exhausted", 0},
		{"exhausted and 35 days after applying is listed once, as closing", "2027-02-03", func(f *fixture, id int64) {
			f.recordAt(id, "2027-02-12")
			f.recordAt(id, "2027-02-20")
		}, "consider_closing", 0},
		{"34 days is not yet closing", "2027-02-04", func(f *fixture, id int64) {
			f.recordAt(id, "2027-02-12")
			f.recordAt(id, "2027-02-20")
		}, "cadence_exhausted", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			e := f.appliedOn("Alpha", tt.appliedAt)
			if tt.setup != nil {
				tt.setup(f, e.ID)
			}
			got := f.followups(defaultFollowup(), true)
			if kinds(got) != tt.wantDue {
				t.Fatalf("kinds = %q, want %q (%+v)", kinds(got), tt.wantDue, got)
			}
			if tt.wantNum > 0 && got[0].Number != tt.wantNum {
				t.Errorf("number = %d, want %d", got[0].Number, tt.wantNum)
			}
		})
	}
}

// recordAt records a follow-up as if on the given day.
func (f *fixture) recordAt(id int64, on string) {
	f.t.Helper()
	real := f.clk.now()
	at, _ := time.Parse(dateLayout, on)
	f.clk.mu.Lock()
	f.clk.t = at.Add(9 * time.Hour)
	f.clk.mu.Unlock()
	_, err := f.tr.RecordFollowup(f.ctx, id, ActorUser, 0, workspace.Followup{FirstAfterDays: 1, RepeatEveryDays: 1, MaxFollowups: 99, CloseAfterDays: 35})
	f.clk.mu.Lock()
	f.clk.t = real
	f.clk.mu.Unlock()
	if err != nil {
		f.t.Fatal(err)
	}
}

func TestFollowup_FirstFollowupDue(t *testing.T) {
	f := newFixture(t)
	e := f.appliedOn("Alpha", "2027-03-02")
	got := f.followups(defaultFollowup(), true)
	if len(got) != 1 || got[0].ID != e.ID || got[0].Kind != KindFollowUp || got[0].Number != 1 || got[0].Due != "2027-03-10" {
		t.Fatalf("%+v", got)
	}
	// Without --due the upcoming ones are listed too.
	f.appliedOn("Beta", "2027-03-08")
	all := f.followups(defaultFollowup(), false)
	if len(all) != 2 || all[1].Due != "2027-03-16" {
		t.Errorf("%+v", all)
	}
	if due := f.followups(defaultFollowup(), true); len(due) != 1 {
		t.Errorf("--due listed an upcoming one: %+v", due)
	}
}

func TestFollowup_CadenceIsBounded(t *testing.T) {
	f := newFixture(t)
	e := f.appliedOn("Alpha", "2027-02-01")
	pol := defaultFollowup()
	for range 2 {
		if _, err := f.tr.RecordFollowup(f.ctx, e.ID, ActorUser, 0, pol); err != nil {
			t.Fatal(err)
		}
		f.clk.advance(8 * day)
	}
	got := f.followups(pol, false)
	if len(got) != 1 || got[0].Kind != KindConsiderClosing { // past the close day, so the one card is the closing one
		t.Fatalf("%+v", got)
	}
	for _, s := range got {
		if s.Kind == KindFollowUp {
			t.Errorf("a third follow-up was suggested: %+v", s)
		}
	}
	if _, err := f.tr.RecordFollowup(f.ctx, e.ID, ActorUser, 0, pol); !errors.Is(err, ErrInvalid) {
		t.Errorf("recording past max_followups: %v", err)
	}
	if n := len(f.events(e.ID, EventFollowupRecorded)); n != 2 {
		t.Errorf("recorded events = %d", n)
	}
}

func TestFollowup_NothingIsSent(t *testing.T) {
	f := newFixture(t)
	e := f.appliedOn("Alpha", "2027-02-01")
	before := f.get(e.ID)
	f.clk.advance(30 * day)
	f.followups(defaultFollowup(), false)
	after := f.get(e.ID)
	if after.Entry.Version != before.Entry.Version || len(after.Events) != len(before.Events) {
		t.Errorf("computing suggestions changed the row: %+v -> %+v", before.Entry, after.Entry)
	}
}

func TestFollowup_Snooze(t *testing.T) {
	f := newFixture(t)
	e := f.appliedOn("Alpha", "2027-03-02") // due today
	pol := defaultFollowup()
	if _, err := f.tr.SnoozeFollowup(f.ctx, e.ID, ActorUser, 3, 0, pol); err != nil {
		t.Fatal(err)
	}
	if got := f.followups(pol, true); len(got) != 0 {
		t.Errorf("snoozed row still due: %+v", got)
	}
	f.clk.advance(2 * day)
	if got := f.followups(pol, true); len(got) != 0 {
		t.Errorf("due one day early: %+v", got)
	}
	f.clk.advance(day)
	got := f.followups(pol, true)
	if len(got) != 1 || got[0].Kind != KindFollowUp || got[0].Number != 1 {
		t.Errorf("did not reappear after 3 days: %+v", got)
	}
	if n := len(f.events(e.ID, EventFollowupSnoozed)); n != 1 {
		t.Errorf("followup_snoozed events = %d", n)
	}
}

// An undo takes back the latest follow-up or snooze: the row is due exactly as
// before it, the history keeps both events, and a press against a row that
// moved since it was read is refused.
func TestFollowup_UndoRestoresTheCadenceAndQuotesTheVersion(t *testing.T) {
	f := newFixture(t)
	e := f.appliedOn("Alpha", "2027-02-20")
	pol := defaultFollowup()
	before := f.followups(pol, false)
	snoozed, err := f.tr.SnoozeFollowup(f.ctx, e.ID, ActorUser, 3, e.Version, pol)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.tr.RecordFollowup(f.ctx, e.ID, ActorUser, e.Version, pol); !errors.Is(err, store.ErrConflict) {
		t.Errorf("a stale record: %v", err)
	}
	recorded, err := f.tr.RecordFollowup(f.ctx, e.ID, ActorUser, snoozed.Version, pol)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.tr.UndoFollowup(f.ctx, e.ID, ActorUser, snoozed.Version); !errors.Is(err, store.ErrConflict) {
		t.Errorf("a stale undo: %v", err)
	}
	undone, err := f.tr.UndoFollowup(f.ctx, e.ID, ActorUser, recorded.Version)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.followups(pol, false); len(got) != 1 || got[0].Number != 1 || got[0].Due != "2027-03-13" {
		t.Errorf("after undoing the follow-up: %+v", got)
	}
	if _, err := f.tr.UndoFollowup(f.ctx, e.ID, ActorUser, undone.Version); err != nil {
		t.Fatal(err)
	}
	after := f.followups(pol, false)
	after[0].Version = before[0].Version
	if len(after) != 1 || after[0] != before[0] {
		t.Errorf("after undoing both: %+v, before %+v", after, before)
	}
	if n := len(f.events(e.ID, EventFollowupRecorded)) + len(f.events(e.ID, EventFollowupSnoozed)); n != 4 {
		t.Errorf("history holds %d follow-up events, want the 2 presses and the 2 undos", n)
	}
	if _, err := f.tr.UndoFollowup(f.ctx, e.ID, ActorUser, 0); !errors.Is(err, ErrInvalid) {
		t.Errorf("an undo with nothing left: %v", err)
	}
}

func TestFollowup_SnoozeNeedsAPendingFollowupAndAPositiveInteger(t *testing.T) {
	f := newFixture(t)
	e := f.appliedOn("Alpha", "2027-03-02")
	pol := defaultFollowup()
	for _, n := range []int{0, -2} {
		if _, err := f.tr.SnoozeFollowup(f.ctx, e.ID, ActorUser, n, 0, pol); !errors.Is(err, ErrInvalid) {
			t.Errorf("snooze %d: %v", n, err)
		}
	}
	ev := f.add(AddRow{Company: "Beta", Role: "Engineer", URL: "https://example.com/beta"})
	if _, err := f.tr.SnoozeFollowup(f.ctx, ev.ID, ActorUser, 2, 0, pol); !errors.Is(err, ErrInvalid) {
		t.Errorf("snooze of an evaluated row: %v", err)
	}
	if _, err := f.tr.RecordFollowup(f.ctx, ev.ID, ActorUser, 0, pol); !errors.Is(err, ErrInvalid) {
		t.Errorf("follow-up of an evaluated row: %v", err)
	}
	if len(f.events(e.ID, EventFollowupSnoozed))+len(f.events(ev.ID, EventFollowupRecorded)) != 0 {
		t.Error("a refused change wrote an event")
	}
}

func TestFollowup_MaxFollowupsZeroDisablesThem(t *testing.T) {
	f := newFixture(t)
	e := f.appliedOn("Alpha", "2027-01-01")
	pol := defaultFollowup()
	pol.MaxFollowups = 0
	if got := f.followups(pol, false); len(got) != 0 {
		t.Errorf("%+v", got)
	}
	if _, err := f.tr.RecordFollowup(f.ctx, e.ID, ActorUser, 0, pol); !errors.Is(err, ErrInvalid) {
		t.Errorf("%v", err)
	}
}

func TestFollowup_ReplyCheckIsFixedAtThreeDays(t *testing.T) {
	f := newFixture(t)
	e := f.add(AddRow{Company: "Alpha", Role: "Engineer", URL: "https://example.com/alpha", Status: Applied})
	f.setStatus(e.ID, Responded, ActorGmail)
	f.clk.advance(2 * day)
	if got := f.followups(defaultFollowup(), true); kinds(got) != "" {
		t.Fatalf("two days is early: %+v", got)
	}
	f.clk.advance(day)
	got := f.followups(defaultFollowup(), true)
	if len(got) != 1 || got[0].Kind != KindReplyCheck || got[0].ID != e.ID || got[0].Due != "2027-03-13" {
		t.Fatalf("%+v", got)
	}
	// The user acting on the row ends the nudge.
	before := f.get(e.ID).Entry
	noted, err := f.tr.AddNote(f.ctx, e.ID, ActorUser, "replied to the recruiter", before.Version)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.followups(defaultFollowup(), true); len(got) != 0 {
		t.Errorf("a note from the user did not end the nudge: %+v", got)
	}
	// Undoing the note reads the row as if it was never written; a press
	// against a row that moved since is refused.
	if _, err := f.tr.UndoNote(f.ctx, e.ID, ActorUser, before.Version); !errors.Is(err, store.ErrConflict) {
		t.Errorf("a stale undo: %v", err)
	}
	undone, err := f.tr.UndoNote(f.ctx, e.ID, ActorUser, noted.Version)
	if err != nil {
		t.Fatal(err)
	}
	if undone.Notes != before.Notes || kinds(f.followups(defaultFollowup(), true)) != "reply_check" {
		t.Errorf("after undoing the note: %q, %+v", undone.Notes, f.followups(defaultFollowup(), true))
	}
	if _, err := f.tr.UndoNote(f.ctx, e.ID, ActorUser, 0); !errors.Is(err, ErrInvalid) {
		t.Errorf("an undo with no note left: %v", err)
	}
	// The interval is not part of the policy.
	pol := defaultFollowup()
	pol.FirstAfterDays, pol.RepeatEveryDays = 1, 1
	g := newFixture(t)
	r := g.add(AddRow{Company: "Beta", Role: "Engineer", URL: "https://example.com/beta", Status: Applied})
	g.setStatus(r.ID, Responded, ActorGmail)
	g.clk.advance(2 * day)
	if got := g.followups(pol, true); len(got) != 0 {
		t.Errorf("reply check followed the policy: %+v", got)
	}
}

func TestFollowup_ArchivedAndClosedRowsAreExcluded(t *testing.T) {
	f := newFixture(t)
	a := f.appliedOn("Alpha", "2027-01-01")
	b := f.appliedOn("Beta", "2027-01-01")
	c := f.appliedOn("Gamma", "2027-01-01")
	f.exec(`UPDATE tracker_entries SET archived_at = '2027-02-01T00:00:00Z' WHERE id = ?`, a.ID)
	f.setStatus(b.ID, Rejected, ActorUser)
	f.setStatus(c.ID, Interview, ActorUser)
	if got := f.followups(defaultFollowup(), false); len(got) != 0 {
		t.Errorf("%+v", got)
	}
}

func TestFollowup_DaysAreCalendarDaysInTheClockZone(t *testing.T) {
	tz := time.FixedZone("test", -8*3600)
	f := newFixture(t)
	f.clk.t = time.Date(2027, 3, 10, 2, 0, 0, 0, tz) // 10:00 UTC, still the 10th locally
	e := f.add(AddRow{Company: "Alpha", Role: "Engineer", URL: "https://example.com/alpha", Status: Applied})
	if e.AppliedAt != "2027-03-10" {
		t.Fatalf("%+v", e)
	}
	f.clk.advance(7*day + 21*time.Hour) // the 17th locally at 23:00, the 18th in UTC
	if got := f.followups(defaultFollowup(), true); len(got) != 0 {
		t.Errorf("due a day early: %+v", got)
	}
	f.clk.advance(2 * time.Hour) // the 18th locally
	if got := f.followups(defaultFollowup(), true); len(got) != 1 {
		t.Errorf("not due on the eighth local day: %+v", got)
	}
}
