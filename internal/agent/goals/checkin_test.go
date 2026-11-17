// Clean-room: docs/provenance.md#CR-AGENT-GOALS

package goals

import (
	"context"
	"strings"
	"testing"
	"time"
)

// monday09 is the rig's start; a cadence of Monday at 09:00 has its slot there.
func (r *rig) withCadence(t *testing.T, title string, c Cadence) Goal {
	t.Helper()
	g, err := r.s.Create(context.Background(), NewGoal{Title: title, DueDate: "2027-03-31", Steps: steps("one", "two"), Cadence: c})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func (r *rig) due(t *testing.T) []Checkin {
	t.Helper()
	list, err := r.s.DueCheckins(context.Background(), r.clock.now())
	if err != nil {
		t.Fatal(err)
	}
	return list
}

var mondays = Cadence{Days: []string{"mon"}, Time: "09:00"}

func TestCheckin_SlotMathUsesTheLocalZone(t *testing.T) {
	r := newRig(t)
	loc := r.s.Zone()
	now := time.Date(2027, 3, 3, 12, 0, 0, 0, loc) // Wednesday
	c := Cadence{Days: []string{"mon", "fri"}, Time: "09:00"}
	if got := LatestSlot(c, now, loc); !got.Equal(time.Date(2027, 3, 1, 9, 0, 0, 0, loc)) {
		t.Errorf("latest = %s", got.In(loc))
	}
	if got := NextSlot(c, now, loc); !got.Equal(time.Date(2027, 3, 5, 9, 0, 0, 0, loc)) {
		t.Errorf("next = %s", got.In(loc))
	}
	if !LatestSlot(Cadence{}, now, loc).IsZero() || !NextSlot(Cadence{}, now, loc).IsZero() {
		t.Error("an empty cadence has slots")
	}
}

// Scenario "Slot sent once".
func TestCheckin_FourTicksDuringADueSlotSendExactlyOne(t *testing.T) {
	r := newRig(t)
	g := r.withCadence(t, "Plan", mondays)
	r.clock.advance(7 * 24 * time.Hour) // the next Monday 09:00
	sent := 0
	var first Checkin
	for range 4 {
		list := r.due(t)
		sent += len(list)
		if len(list) > 0 {
			first = list[0]
		}
		r.clock.advance(time.Minute)
	}
	if sent != 1 || first.GoalID != g.ID {
		t.Fatalf("sent %d: %+v", sent, first)
	}
	if !strings.Contains(first.Title, "Plan") || !strings.Contains(first.Body, "0 of 2 steps done") || !strings.Contains(first.Body, "Next, step 1: one") ||
		!strings.Contains(first.Body, "Due 2027-03-31.") || strings.Contains(first.Body, "overdue") {
		t.Fatalf("check-in = %+v", first)
	}
}

// Scenario "Asleep within the window".
func TestCheckin_AMachineThatWakesTwoHoursLateStillSendsTheSlot(t *testing.T) {
	r := newRig(t)
	r.withCadence(t, "Plan", mondays)
	r.clock.advance(7*24*time.Hour + 2*time.Hour)
	if got := r.due(t); len(got) != 1 {
		t.Fatalf("due = %+v", got)
	}
}

// Scenario "Asleep past the window".
func TestCheckin_AMachineThatWakesFourHoursLateSkipsTheSlot(t *testing.T) {
	r := newRig(t)
	r.withCadence(t, "Plan", mondays)
	r.clock.advance(7*24*time.Hour + 4*time.Hour)
	if got := r.due(t); len(got) != 0 {
		t.Fatalf("due = %+v", got)
	}
	r.clock.advance(7*24*time.Hour - 4*time.Hour) // the Monday after: its slot is sent
	if got := r.due(t); len(got) != 1 {
		t.Fatalf("next slot: %+v", got)
	}
}

func TestCheckin_OnlyTheLatestSlotIsDue(t *testing.T) {
	r := newRig(t)
	r.withCadence(t, "Plan", Cadence{Days: []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}, Time: "09:00"})
	r.clock.advance(3*24*time.Hour + time.Hour) // three slots passed unseen; the latest is 1 hour old
	got := r.due(t)
	if len(got) != 1 || !got[0].Slot.Equal(time.Date(2027, 3, 4, 9, 0, 0, 0, r.s.Zone())) {
		t.Fatalf("due = %+v", got)
	}
}

func TestCheckin_ASlotBeforeCreationIsNeverSent(t *testing.T) {
	r := newRig(t)
	r.clock.advance(30 * time.Minute) // 09:30, after Monday's slot
	r.withCadence(t, "Plan", mondays)
	if got := r.due(t); len(got) != 0 {
		t.Fatalf("a slot before creation: %+v", got)
	}
}

// Scenario "Paused goal".
func TestCheckin_APausedGoalGetsNoneAndNoneForThatSlotAfterResuming(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	g := r.withCadence(t, "Plan", mondays)
	paused, active := Paused, Active
	if _, err := r.s.Update(ctx, g.ID, Patch{State: &paused}); err != nil {
		t.Fatal(err)
	}
	r.clock.advance(7 * 24 * time.Hour)
	if got := r.due(t); len(got) != 0 {
		t.Fatalf("paused: %+v", got)
	}
	r.clock.advance(time.Hour)
	if _, err := r.s.Update(ctx, g.ID, Patch{State: &active}); err != nil {
		t.Fatal(err)
	}
	r.clock.advance(-30 * time.Minute) // still inside the old slot's window
	if got := r.due(t); len(got) != 0 {
		t.Fatalf("after resuming: %+v", got)
	}
	for _, st := range []string{Completed, Archived} {
		state := st
		if _, err := r.s.Update(ctx, g.ID, Patch{State: &state}); err != nil {
			t.Fatal(err)
		}
		r.clock.advance(7 * 24 * time.Hour)
		if got := r.due(t); len(got) != 0 {
			t.Fatalf("%s: %+v", st, got)
		}
	}
}

func TestCheckin_IsBuiltFromTheRecordAndNamesAnOverdueDateAndAWaitingProposal(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	g := r.withCadence(t, "Plan", mondays)
	done := Done
	if _, err := r.s.SetStep(ctx, g.ID, 1, &done, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.Propose(ctx, g.ID, "why", Change{Steps: steps("x")}); err != nil {
		t.Fatal(err)
	}
	r.clock.advance(5 * 7 * 24 * time.Hour) // well past the due date
	got := r.due(t)
	if len(got) != 1 {
		t.Fatalf("due = %+v", got)
	}
	for _, want := range []string{"Due 2027-03-31 (overdue).", "1 of 2 steps done.", "Next, step 2: two", "A plan change is waiting"} {
		if !strings.Contains(got[0].Body, want) {
			t.Errorf("body lacks %q: %s", want, got[0].Body)
		}
	}
	if got[0].Key != "goal-checkin|"+g.ID+"|"+stamp(got[0].Slot) {
		t.Errorf("key = %s", got[0].Key)
	}
}
