package notify

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestNotify_CostCap_AlertDeliveredThroughPolicyReleasedWhenNoChannelTook(t *testing.T) {
	r := newRig(t)
	r.quiet = QuietHours{true, "22:00", "08:00", "UTC"}
	r.clk.t = time.Date(2026, 3, 10, 23, 30, 0, 0, time.UTC)
	const msg = "The daily model spend cap of USD 2.00 is reached (spend USD 2.05). Later calls use cheaper models. Change the cap with the daily_usd_cap setting."
	// The alert is a normal announcement: held overnight, then sent to both
	// sinks once however often the day's alert is raised.
	for range 2 {
		if !r.policy.AnnounceCostCap(bg, "2026-03-10", msg) {
			t.Fatal("the policy refused the alert")
		}
	}
	r.process()
	if r.desktop.count() != 0 || r.tg.count() != 0 || r.outboxStates()["held"] != 1 {
		t.Fatalf("not held overnight: desktop %v telegram %v states %v", r.desktop.titles(), r.tg.titles(), r.outboxStates())
	}
	r.clk.t = time.Date(2026, 3, 11, 8, 0, 0, 0, time.UTC)
	r.process()
	if r.desktop.count() != 1 || r.tg.count() != 1 {
		t.Fatalf("desktop %v telegram %v", r.desktop.titles(), r.tg.titles())
	}
	a := r.desktop.announced[0]
	if a.Urgent || a.Link != "/usage" || !strings.Contains(a.Body, "daily_usd_cap") || !strings.Contains(a.Body, "cheaper models") {
		t.Errorf("announcement %+v", a)
	}
	// When the policy cannot take the alert, it says so and the caller keeps
	// the day's claim open for a retry.
	gone, cancel := context.WithCancel(bg)
	cancel()
	if r.policy.AnnounceCostCap(gone, "2026-03-11", msg) {
		t.Error("an alert the policy could not queue was reported as accepted")
	}
	if !r.policy.AnnounceCostCap(bg, "2026-03-11", msg) {
		t.Error("the retry was refused")
	}
}

// Every announced kind has a reach, and the kinds that tell of a run reach
// the phone only when the run was asked for there.
func TestNotify_Reach_EveryKindHasOneAndRunsGoToTheAsker(t *testing.T) {
	kinds := []string{EventNeedsYou, EventBlocked, EventDone, EventBatch, EventGmailAlert, EventGmailDigest, EventCostCap, EventBurstSummary,
		EventPromptResult, EventGoalCheckin, EventTriggerRes, EventSignalHot, EventSignalDigest, EventModelGone, EventProviderRefused,
		EventDaySummary, EventInterviewReminder, EventGmailReminder, EventMorningBrief, EventTaskUpdate}
	if len(kinds) != len(eventKinds) {
		t.Fatalf("the test lists %d kinds, the policy %d: name the new kind's reach here", len(kinds), len(eventKinds))
	}
	for _, k := range kinds {
		r, ok := eventKinds[k]
		if !ok {
			t.Errorf("%s has no reach", k)
		}
		run := slices.Contains([]string{EventNeedsYou, EventBlocked, EventDone}, k)
		if run != (r == ReachAsker) {
			t.Errorf("%s reach = %d", k, r)
		}
	}
	for _, k := range []string{EventBatch, EventModelGone} {
		if eventKinds[k] != ReachDesk {
			t.Errorf("%s reaches the phone", k)
		}
	}
	if _, ok := eventKinds["notice"]; ok {
		t.Error("a kind outside the set has a reach")
	}
}

// A run started at the desk is told on the desktop alone; the same run asked
// for from the phone is told on both, its stop and its end.
func TestNotify_Reach_ADeskRunNeverReachesThePhone(t *testing.T) {
	r := newRig(t)
	r.desk["desk"] = true
	r.open("desk", OriginUser, KindApply)
	r.open("phone", OriginUser, KindEvaluate)
	r.move("desk", NeedsYou, MoveArgs{Reason: "confirmation"})
	r.move("phone", NeedsYou, MoveArgs{Reason: "review"})
	r.process()
	if got := r.desktop.titles(); !slices.Equal(got, []string{"Entry desk", "Entry phone"}) {
		t.Errorf("desktop = %v", got)
	}
	if got := r.tg.titles(); !slices.Equal(got, []string{"Entry phone"}) {
		t.Fatalf("phone = %v", got)
	}
	r.move("desk", Working, MoveArgs{})
	r.move("desk", Done, MoveArgs{Outcome: OutcomeOK})
	r.move("phone", Working, MoveArgs{})
	r.move("phone", Done, MoveArgs{Outcome: OutcomeOK})
	r.clk.advance(DefaultDoneDelay + time.Second)
	r.process()
	if got := r.tg.titles(); !slices.Equal(got, []string{"Entry phone", "Entry phone"}) {
		t.Errorf("phone after the ends = %v", got)
	}
	if r.desktop.count() != 4 {
		t.Errorf("desktop after the ends = %v", r.desktop.titles())
	}
	// Nothing desk-only waits for the phone: every row settled.
	if st := r.outboxStates(); st["pending"] != 0 || st["held"] != 0 {
		t.Errorf("outbox = %v", st)
	}
}

// Without an Asked function no run reaches the phone, and announcements of
// the phone's own kinds still do.
func TestNotify_Reach_NoAskedMeansNoRunOnThePhone(t *testing.T) {
	r := newRig(t)
	r.policy.o.Asked = nil
	r.open("a", OriginAgent, KindEvaluate)
	r.move("a", Blocked, MoveArgs{Reason: "cap-reached"})
	if err := r.policy.Announce(bg, Event{Key: "task|1", Kind: EventTaskUpdate, Title: "A task needs you: follow-ups", Body: "Which one first?"}); err != nil {
		t.Fatal(err)
	}
	r.process()
	if got := r.tg.titles(); !slices.Equal(got, []string{"A task needs you: follow-ups"}) {
		t.Errorf("phone = %v", got)
	}
	if r.desktop.count() != 2 {
		t.Errorf("desktop = %v", r.desktop.titles())
	}
}

// At the end of quiet hours the phone's summary lists only what reaches the
// phone; desk items are released to the desktop alone.
func TestNotify_Reach_QuietHoursSummaryOnThePhoneLeavesDeskItemsOut(t *testing.T) {
	r := newRig(t)
	r.quiet = QuietHours{true, "22:00", "08:00", "UTC"}
	r.clk.t = time.Date(2026, 3, 10, 23, 0, 0, 0, time.UTC)
	for _, ev := range []Event{
		{Key: "brief|1", Kind: EventMorningBrief, Title: "Good morning"},
		{Key: "sig|1", Kind: EventSignalDigest, Title: "Hiring signals"},
		{Key: "model|1", Kind: EventModelGone, Title: "A routed model left the catalog"},
	} {
		if err := r.policy.Announce(bg, ev); err != nil {
			t.Fatal(err)
		}
	}
	r.desk["d"] = true
	r.open("d", OriginUser, KindDocuments)
	r.move("d", Done, MoveArgs{Outcome: OutcomeOK})
	r.clk.advance(DefaultDoneDelay + time.Second)
	r.process()
	if r.desktop.count()+r.tg.count() != 0 {
		t.Fatalf("delivered in the window: desktop %v phone %v", r.desktop.titles(), r.tg.titles())
	}
	r.clk.t = time.Date(2026, 3, 11, 8, 0, 0, 0, time.UTC)
	r.process()
	phone := r.tg.announced
	if len(phone) != 1 || phone[0].Title != "2 updates while quiet hours were on" || phone[0].Body != "Good morning; Hiring signals" {
		t.Errorf("phone = %+v", phone)
	}
	desk := r.desktop.announced
	if len(desk) != 1 || desk[0].Title != "4 updates while quiet hours were on" {
		t.Errorf("desktop = %+v", desk)
	}
}
