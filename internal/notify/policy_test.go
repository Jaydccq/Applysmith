// Clean-room: docs/provenance.md#CR-RUNS-BOARD

package notify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNotify_Policy_AnnouncedEventsClosedList(t *testing.T) {
	r := newRig(t)
	if err := r.policy.Announce(bg, Event{Key: "k", Kind: "newsletter", Title: "x"}); err == nil {
		t.Error("an event outside the closed list was accepted")
	}
	if err := r.policy.Announce(bg, Event{Key: "k", Kind: EventGmailDigest, Title: "x", NeverHold: true}); err == nil {
		t.Error("a digest asked to be exempt from quiet hours")
	}
	if err := r.policy.Announce(bg, Event{Key: "k", Kind: EventPromptResult, Title: "x", NeverHold: true}); err != nil {
		t.Errorf("a scheduled prompt result: %v", err)
	}
}

func TestNotify_Policy_DoneRuleByOriginAndOutcome(t *testing.T) {
	cases := []struct {
		name    string
		origin  Origin
		kind    Kind
		outcome string
		failed  bool
		want    bool
	}{
		{"user ok", OriginUser, KindEvaluate, OutcomeOK, false, true},
		{"user failed", OriginUser, KindEvaluate, OutcomeFailed, false, true},
		{"agent ok", OriginAgent, KindAgent, OutcomeOK, false, true},
		{"user cancelled", OriginUser, KindEvaluate, OutcomeCancelled, false, false},
		{"user interrupted", OriginUser, KindEvaluate, OutcomeInterrupted, false, false},
		{"scheduled ok", OriginSchedule, KindScan, OutcomeOK, true, false},
		{"scheduled scan failed, switch off", OriginSchedule, KindScan, OutcomeFailed, false, false},
		{"scheduled scan failed, switch on", OriginSchedule, KindScan, OutcomeFailed, true, true},
		{"scheduled task failed, switch on", OriginSchedule, KindTask, OutcomeFailed, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			if _, err := NewSettingsStore(r.st, r.clk.Now).Patch(bg, SettingsPatch{FailedScans: &c.failed}); err != nil {
				t.Fatal(err)
			}
			r.open("e", c.origin, c.kind)
			r.move("e", Done, MoveArgs{Outcome: c.outcome})
			r.clk.advance(DefaultDoneDelay + time.Second)
			r.process()
			if got := r.desktop.count() == 1; got != c.want {
				t.Errorf("announced = %v, want %v", got, c.want)
			}
		})
	}
}

func TestNotify_Policy_FailedScanSwitch(t *testing.T) {
	r := newRig(t)
	r.open("e", OriginSchedule, KindScan)
	r.move("e", Done, MoveArgs{Outcome: OutcomeFailed})
	r.clk.advance(time.Minute)
	r.process()
	if r.desktop.count() != 0 {
		t.Fatal("announced with the switch off")
	}
	if _, err := r.board.Get(bg, "e"); err != nil {
		t.Error("the board entry must remain")
	}
	on := true
	NewSettingsStore(r.st, r.clk.Now).Patch(bg, SettingsPatch{FailedScans: &on})
	r.open("f", OriginSchedule, KindScan)
	r.move("f", Done, MoveArgs{Outcome: OutcomeFailed})
	r.clk.advance(time.Minute)
	r.process()
	if r.desktop.count() != 1 || r.desktop.announced[0].Urgent {
		t.Errorf("announcements = %+v", r.desktop.announced)
	}
}

func TestNotify_Policy_UrgentSetExactlyThree(t *testing.T) {
	for _, reason := range needsYouReasons {
		r := newRig(t)
		r.open("e", OriginUser, KindApply)
		r.move("e", NeedsYou, MoveArgs{Reason: reason})
		r.process()
		if r.desktop.count() != 1 || !r.desktop.announced[0].Urgent {
			t.Errorf("needs-you/%s: %+v", reason, r.desktop.announced)
		}
	}
	r := newRig(t)
	r.open("b", OriginUser, KindApply)
	r.move("b", Blocked, MoveArgs{Reason: "key-missing"})
	r.process()
	if r.desktop.count() != 1 || !r.desktop.announced[0].Urgent {
		t.Errorf("blocked: %+v", r.desktop.announced)
	}
	r.open("d", OriginUser, KindEvaluate)
	r.move("d", Done, MoveArgs{Outcome: OutcomeOK})
	r.clk.advance(time.Minute)
	r.process()
	if last := r.desktop.announced[len(r.desktop.announced)-1]; last.Urgent {
		t.Error("a finished run is urgent")
	}
	// The third member is an interview within 24 hours; an offer alert and the
	// cost-cap alert are normal.
	for i, c := range []struct {
		name     string
		announce func() error
		urgent   bool
	}{
		{"interview in 20 hours", func() error {
			return r.policy.AnnounceGmailAlert(bg, GmailAlert{MessageID: "m1", Title: "Interview", InterviewStart: r.clk.Now().Add(20 * time.Hour)})
		}, true},
		{"offer", func() error { return r.policy.AnnounceGmailAlert(bg, GmailAlert{MessageID: "m2", Title: "Offer"}) }, false},
		{"cost cap", func() error {
			if !r.policy.AnnounceCostCap(bg, "2026-03-10", "spent") {
				return errors.New("not queued")
			}
			return nil
		}, false},
	} {
		if err := c.announce(); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		r.process()
		if n := r.desktop.count(); n != 3+i || r.desktop.announced[n-1].Urgent != c.urgent {
			t.Errorf("%s: announcements = %+v, want urgent %v", c.name, r.desktop.announced, c.urgent)
		}
	}
}

func TestNotify_Policy_GmailInterviewUrgency(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		start time.Time
		want  bool
	}{
		{"in 24 hours", now.Add(24 * time.Hour), true},
		{"in 6 days", now.Add(6 * 24 * time.Hour), false},
		{"already started", now.Add(-time.Hour), false},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			err := r.policy.AnnounceGmailAlert(bg, GmailAlert{MessageID: "m" + strconv.Itoa(i), Title: "Interview", Body: "Invitation", InterviewStart: c.start})
			if err != nil {
				t.Fatal(err)
			}
			r.process()
			if r.desktop.count() != 1 || r.desktop.announced[0].Urgent != c.want {
				t.Errorf("announcements = %+v, want urgent %v", r.desktop.announced, c.want)
			}
		})
	}
}

func TestNotify_QuietHours_WindowCrossesMidnightInZone(t *testing.T) {
	cases := []struct {
		name string
		q    QuietHours
		at   time.Time
		want bool
	}{
		{"inside, after midnight", QuietHours{true, "22:00", "08:00", "UTC"}, utc(2, 0), true},
		{"start is inclusive", QuietHours{true, "22:00", "08:00", "UTC"}, utc(22, 0), true},
		{"end is exclusive", QuietHours{true, "22:00", "08:00", "UTC"}, utc(8, 0), false},
		{"same-day window", QuietHours{true, "13:00", "15:00", "UTC"}, utc(14, 0), true},
		{"another zone, inside there", QuietHours{true, "22:00", "08:00", "America/Chicago"}, utc(4, 0), true}, // 23:00 in Chicago
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.q.Active(c.at); got != c.want {
				t.Errorf("Active = %v, want %v", got, c.want)
			}
		})
	}
}

func utc(h, m int) time.Time { return time.Date(2026, 3, 11, h, m, 0, 0, time.UTC) }

func TestNotify_QuietHours_BrokenConfigMeansOff(t *testing.T) {
	for _, q := range []QuietHours{
		{false, "22:00", "08:00", "UTC"},
		{true, "25:00", "08:00", "UTC"},
		{true, "22:00", "22:00", "UTC"},
		{true, "22:00", "08:00", "Mars/Olympus"},
	} {
		if q.Active(utc(23, 0)) {
			t.Errorf("%+v silenced the notifications", q)
		}
	}
}

func TestNotify_QuietHours_UrgentPassesNormalHeld(t *testing.T) {
	r := newRig(t)
	r.quiet = QuietHours{true, "22:00", "08:00", "UTC"}
	r.clk.t = time.Date(2026, 3, 10, 23, 30, 0, 0, time.UTC)
	r.open("n", OriginUser, KindApply)
	r.move("n", NeedsYou, MoveArgs{Reason: "one-time-code"})
	r.open("d", OriginUser, KindEvaluate)
	r.move("d", Done, MoveArgs{Outcome: OutcomeOK})
	r.clk.advance(time.Minute)
	r.process()
	if r.desktop.count() != 1 || r.tg.count() != 1 || !r.desktop.announced[0].Urgent {
		t.Fatalf("urgent not delivered alone: desktop %v telegram %v", r.desktop.titles(), r.tg.titles())
	}
	if r.outboxStates()["held"] != 1 {
		t.Errorf("states = %v", r.outboxStates())
	}
	if len(r.desktop.notices) != 1 {
		t.Errorf("a held announcement must be listed under Notices at once: %d notices", len(r.desktop.notices))
	}
	// Delivered at the first pass at or after 08:00.
	r.clk.t = time.Date(2026, 3, 11, 8, 0, 0, 0, time.UTC)
	r.process()
	if r.desktop.count() != 2 || r.tg.count() != 2 {
		t.Errorf("after the window: desktop %v telegram %v", r.desktop.titles(), r.tg.titles())
	}
	if !slices.Contains(r.desktop.seen, "d") {
		t.Errorf("the held item was shown but its notice stays unread: seen %v", r.desktop.seen)
	}
}

func TestNotify_QuietHours_HeldSurvivesRestartAndReleasesInOrder(t *testing.T) {
	r := newRig(t)
	r.quiet = QuietHours{true, "22:00", "08:00", "UTC"}
	r.clk.t = time.Date(2026, 3, 11, 1, 0, 0, 0, time.UTC)
	for _, id := range []string{"a", "b", "c"} {
		r.open(id, OriginUser, KindEvaluate)
		r.move(id, Done, MoveArgs{Outcome: OutcomeOK})
	}
	r.clk.advance(time.Minute)
	r.process()
	r.clk.t = time.Date(2026, 3, 11, 3, 0, 0, 0, time.UTC)
	r.build() // the server restarted in the window
	r.process()
	if r.desktop.count() != 0 {
		t.Fatal("delivered inside the window")
	}
	r.clk.t = time.Date(2026, 3, 11, 8, 1, 0, 0, time.UTC)
	r.process()
	// The morning brings one summary per sink that lists the held items in
	// order, not a burst of three.
	for _, s := range []*fakeSink{r.desktop, r.tg} {
		if s.count() != 1 || s.announced[0].Title != "3 updates while quiet hours were on" || s.announced[0].Body != "Entry a; Entry b; Entry c" || s.announced[0].Link != "/runs" {
			t.Errorf("%s = %+v", s.name, s.announced)
		}
	}
	if st := r.outboxStates(); st["settled"] != 5 {
		t.Errorf("states = %v", st)
	}
	for _, id := range []string{"a", "b", "c"} {
		if !slices.Contains(r.desktop.seen, id) {
			t.Errorf("%s is in the summary but its notice stays unread: seen %v", id, r.desktop.seen)
		}
	}
	r.clk.advance(time.Minute)
	r.process()
	if r.desktop.count() != 1 || r.tg.count() != 1 {
		t.Errorf("a held item was delivered again: %v %v", r.desktop.titles(), r.tg.titles())
	}
}

func TestNotify_Test_SendsNowOnEachSinkAndSaysWhy(t *testing.T) {
	r := newRig(t)
	r.quiet = QuietHours{true, "00:00", "23:59", "UTC"}
	r.tg.setErr(errDown)
	got := r.policy.Test(bg)
	if len(got) != 2 || got[0] != (TestResult{Sink: "desktop", State: "sent"}) || got[1].State != "failed" || got[1].Err != errDown {
		t.Fatalf("results = %+v", got)
	}
	if r.desktop.count() != 1 || r.desktop.announced[0].Title != "Test notification" {
		t.Errorf("desktop = %v", r.desktop.titles())
	}
	r.tg.off = true
	if _, err := r.policy.o.Settings.Patch(bg, SettingsPatch{Desktop: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	got = r.policy.Test(bg)
	if got[0].State != "off" || got[1].State != "unavailable" || r.desktop.count() != 1 {
		t.Errorf("results = %+v", got)
	}
	if len(r.outboxStates()) != 0 {
		t.Errorf("a test was queued: %v", r.outboxStates())
	}
}

func TestNotify_QuietHours_HeldDoneSurvivesRestartAndAckedIsDiscarded(t *testing.T) {
	r := newRig(t)
	r.quiet = QuietHours{true, "22:00", "08:00", "UTC"}
	r.clk.t = time.Date(2026, 3, 11, 1, 0, 0, 0, time.UTC)
	for _, id := range []string{"keep", "acked"} {
		r.open(id, OriginUser, KindEvaluate)
		r.move(id, Done, MoveArgs{Outcome: OutcomeOK})
	}
	r.clk.advance(time.Minute)
	r.process()
	r.clk.t = time.Date(2026, 3, 11, 7, 0, 0, 0, time.UTC)
	r.board.Ack(bg, "acked")
	r.clk.t = time.Date(2026, 3, 11, 8, 0, 0, 0, time.UTC)
	r.process()
	if got := strings.Join(r.desktop.titles(), ","); got != "Entry keep" {
		t.Errorf("delivered = %s", got)
	}
	if r.outboxStates()["discarded"] != 1 {
		t.Errorf("states = %v", r.outboxStates())
	}
}

func TestNotify_QuietHours_FailedDeliveryRetriedDroppedAfter24h(t *testing.T) {
	r := newRig(t)
	r.desktop.setErr(errDown)
	r.open("n", OriginUser, KindApply)
	r.move("n", NeedsYou, MoveArgs{Reason: "answer"})
	r.tg.off = true
	for i := 0; i < 3; i++ {
		r.clk.advance(time.Minute)
		r.process()
	}
	if r.outboxStates()["pending"] != 1 {
		t.Fatalf("a failed delivery must stay queued: %v", r.outboxStates())
	}
	r.desktop.setErr(nil)
	r.process()
	if r.desktop.count() != 1 || r.outboxStates()["settled"] != 1 {
		t.Errorf("retry: %d announced, %v", r.desktop.count(), r.outboxStates())
	}
	// Another one that never gets through.
	r.desktop.setErr(errDown)
	r.open("m", OriginUser, KindApply)
	r.move("m", Blocked, MoveArgs{Reason: "lockout"})
	r.clk.advance(25 * time.Hour)
	r.process()
	if r.outboxStates()["dropped"] != 1 {
		t.Errorf("states = %v", r.outboxStates())
	}
}

func TestNotify_QuietHours_CrashMidReleaseNeverSendsTwice(t *testing.T) {
	r := newRig(t)
	r.open("a", OriginUser, KindApply)
	r.move("a", NeedsYou, MoveArgs{Reason: "answer"})
	r.process()
	if r.desktop.count() != 1 {
		t.Fatal("not delivered")
	}
	// The process died after the sink accepted it but before the row settled.
	if err := r.st.Write(bg, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE notify_outbox SET state = 'pending'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r.build()
	r.process()
	if r.desktop.count() != 1 {
		t.Errorf("announced %d times", r.desktop.count())
	}
}

func TestNotify_QuietHours_UnpairedChatDropsHeldItem(t *testing.T) {
	r := newRig(t)
	r.tg.setErr(ErrGone)
	r.open("a", OriginUser, KindApply)
	r.move("a", NeedsYou, MoveArgs{Reason: "answer"})
	r.process()
	var state string
	if err := r.st.Read().QueryRow(`SELECT state FROM notify_deliveries WHERE sink = 'telegram'`).Scan(&state); err != nil || state != "dropped" {
		t.Errorf("telegram delivery = %q %v", state, err)
	}
	if r.desktop.count() != 1 || r.outboxStates()["settled"] != 1 {
		t.Errorf("desktop %d, %v", r.desktop.count(), r.outboxStates())
	}
}

func TestNotify_Dedup_OncePerKeyAcrossRestart(t *testing.T) {
	r := newRig(t)
	r.open("a", OriginUser, KindApply)
	e := r.move("a", NeedsYou, MoveArgs{Reason: "sign-in"})
	r.process()
	r.build()
	r.policy.Changed(bg, nil, e) // the same state is reported again after a restart
	r.process()
	if r.desktop.count() != 1 || r.tg.count() != 1 {
		t.Errorf("repeated: desktop %d telegram %d", r.desktop.count(), r.tg.count())
	}
	// Entering the same state again later is a new epoch and a new announcement.
	r.move("a", Working, MoveArgs{})
	r.move("a", NeedsYou, MoveArgs{Reason: "sign-in"})
	r.process()
	if r.desktop.count() != 2 {
		t.Errorf("a new epoch was not announced: %d", r.desktop.count())
	}
}

func TestNotify_Dedup_ProgressIsSilent(t *testing.T) {
	r := newRig(t)
	r.open("a", OriginUser, KindApply)
	r.move("a", NeedsYou, MoveArgs{Reason: "answer"})
	r.process()
	if _, err := r.board.Update(bg, "a", Patch{Detail: ptr("still waiting")}); err != nil {
		t.Fatal(err)
	}
	r.process()
	if r.desktop.count() != 1 {
		t.Errorf("announced %d times", r.desktop.count())
	}
}

func TestNotify_Burst_ThreeThenOneSummaryUrgentNeverDelayed(t *testing.T) {
	r := newRig(t)
	r.tg.off = true
	for i := 0; i < 10; i++ {
		id := "e" + strconv.Itoa(i)
		r.open(id, OriginUser, KindEvaluate)
		r.move(id, Done, MoveArgs{Outcome: OutcomeOK})
	}
	r.clk.advance(DefaultDoneDelay + time.Second)
	r.process()
	if r.desktop.count() != 3 {
		t.Fatalf("delivered %d at once, want 3", r.desktop.count())
	}
	// An urgent one in the burst is delivered at once.
	r.open("u", OriginUser, KindApply)
	r.move("u", NeedsYou, MoveArgs{Reason: "answer"})
	r.process()
	if r.desktop.count() != 4 {
		t.Fatalf("the urgent announcement was delayed: %d", r.desktop.count())
	}
	r.clk.advance(30 * time.Second)
	r.process()
	if r.desktop.count() != 4 {
		t.Errorf("a summary came before the window cleared")
	}
	r.clk.advance(31 * time.Second)
	r.process()
	if r.desktop.count() != 5 || r.desktop.announced[4].Body != "7 more updates" {
		t.Fatalf("summary = %+v", r.desktop.announced)
	}
	r.clk.advance(time.Minute)
	r.process()
	if r.desktop.count() != 5 {
		t.Errorf("more than one summary: %d", r.desktop.count())
	}
}

func TestNotify_DoneDelay_CancelledByAck(t *testing.T) {
	r := newRig(t)
	r.open("a", OriginUser, KindEvaluate)
	r.move("a", Done, MoveArgs{Outcome: OutcomeOK})
	r.clk.advance(5 * time.Second)
	r.board.Ack(bg, "a")
	r.process()
	r.clk.advance(time.Minute)
	r.process()
	if r.desktop.count() != 0 {
		t.Errorf("announced after an acknowledgement: %v", r.desktop.titles())
	}
	r.open("b", OriginUser, KindEvaluate)
	r.move("b", Done, MoveArgs{Outcome: OutcomeOK})
	r.clk.advance(19 * time.Second)
	r.process()
	if r.desktop.count() != 0 {
		t.Error("announced before the 20 seconds")
	}
	r.clk.advance(2 * time.Second)
	r.process()
	if r.desktop.count() != 1 {
		t.Errorf("not announced after the delay: %d", r.desktop.count())
	}
}

func TestNotify_DoneDelay_LeavingDoneCancels(t *testing.T) {
	r := newRig(t)
	r.open("a", OriginUser, KindEvaluate)
	r.move("a", Done, MoveArgs{Outcome: OutcomeOK})
	r.move("a", Working, MoveArgs{})
	r.clk.advance(time.Minute)
	r.process()
	if r.desktop.count() != 0 {
		t.Errorf("announced a finished run that is running again")
	}
}

func TestNotify_Rollup_BatchOneAnnouncementWaitingMembersIndividually(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 6; i++ {
		_, err := r.board.Open(bg, OpenArgs{ID: "m" + strconv.Itoa(i), Kind: KindApply, Origin: OriginUser, Title: "Apply " + strconv.Itoa(i), BatchID: "batch-1"})
		if err != nil {
			t.Fatal(err)
		}
	}
	r.move("m5", NeedsYou, MoveArgs{Reason: "answer"})
	r.process()
	if r.desktop.count() != 1 || !r.desktop.announced[0].Urgent {
		t.Fatalf("the waiting member must be announced when it happened: %v", r.desktop.titles())
	}
	for i := 0; i < 5; i++ {
		r.move("m"+strconv.Itoa(i), Done, MoveArgs{Outcome: OutcomeOK})
	}
	r.clk.advance(time.Minute)
	r.process()
	if r.desktop.count() != 2 || r.desktop.announced[1].Body != "5 of 6 finished, 1 need you" {
		t.Errorf("announcements = %+v", r.desktop.announced)
	}
}

func TestNotify_Withdraw_DesktopEventAndTelegramEditWithoutButtons(t *testing.T) {
	r := newRig(t)
	r.open("a", OriginUser, KindApply)
	r.move("a", NeedsYou, MoveArgs{Reason: "sign-in"})
	r.process()
	r.move("a", Working, MoveArgs{})
	if len(r.desktop.withdrawn) != 1 || r.desktop.withdrawn[0].Ext != "desktop-1" || r.desktop.withdrawn[0].Why != Handled {
		t.Errorf("desktop withdrawals = %+v", r.desktop.withdrawn)
	}
	if len(r.tg.withdrawn) != 1 || r.tg.withdrawn[0].Why != Handled || r.tg.withdrawn[0].A.Title != "Entry a" {
		t.Errorf("telegram withdrawals = %+v", r.tg.withdrawn)
	}
}

func TestNotify_Ack_WithdrawsNotification(t *testing.T) {
	r := newRig(t)
	r.open("a", OriginUser, KindApply)
	r.move("a", NeedsYou, MoveArgs{Reason: "sign-in"})
	r.process()
	r.board.Ack(bg, "a")
	if len(r.desktop.withdrawn) != 1 || len(r.tg.withdrawn) != 1 {
		t.Errorf("withdrawn: desktop %d telegram %d", len(r.desktop.withdrawn), len(r.tg.withdrawn))
	}
	r.board.Ack(bg, "a") // a second acknowledgement withdraws nothing more
	if len(r.desktop.withdrawn) != 1 {
		t.Error("withdrawn twice")
	}
}

func TestNotify_Withdraw_EscalationReplacesDesktopSupersedesTelegram(t *testing.T) {
	r := newRig(t)
	r.open("a", OriginUser, KindApply)
	r.move("a", NeedsYou, MoveArgs{Reason: "sign-in"})
	r.process()
	r.move("a", Blocked, MoveArgs{Reason: "lockout"})
	r.process()
	if r.desktop.count() != 2 || r.desktop.announced[0].Handle() != r.desktop.announced[1].Handle() {
		t.Errorf("the desktop needs one identifier: %+v", r.desktop.announced)
	}
	if r.tg.count() != 2 {
		t.Errorf("telegram messages: %d", r.tg.count())
	}
	if len(r.tg.withdrawn) != 1 || r.tg.withdrawn[0].Why != Superseded {
		t.Errorf("telegram withdrawals = %+v", r.tg.withdrawn)
	}
}

func TestNotify_Settings_SinkSwitchedOffAndEachChannelFailureIsolated(t *testing.T) {
	r := newRig(t)
	off := false
	NewSettingsStore(r.st, r.clk.Now).Patch(bg, SettingsPatch{Telegram: &off})
	r.open("a", OriginUser, KindApply)
	r.move("a", NeedsYou, MoveArgs{Reason: "answer"})
	r.process()
	if r.desktop.count() != 1 || r.tg.count() != 0 {
		t.Errorf("desktop %d telegram %d", r.desktop.count(), r.tg.count())
	}
	// A failing sink does not stop the other one.
	on := true
	NewSettingsStore(r.st, r.clk.Now).Patch(bg, SettingsPatch{Telegram: &on})
	r.tg.setErr(errDown)
	r.open("b", OriginUser, KindApply)
	r.move("b", NeedsYou, MoveArgs{Reason: "answer"})
	r.process()
	if r.desktop.count() != 2 {
		t.Errorf("a failing telegram stopped the desktop: %d", r.desktop.count())
	}
	r.tg.setErr(nil)
	r.process()
	if r.tg.count() != 1 || r.desktop.count() != 2 {
		t.Errorf("retry: desktop %d telegram %d", r.desktop.count(), r.tg.count())
	}
}

func TestNotify_Content_BoundedAndNoSecrets(t *testing.T) {
	long := strings.Repeat("word ", 100)
	title, body := Compose(long, long)
	if len([]rune(title)) > MaxNoticeTitle || len([]rune(body)) > MaxNoticeBody {
		t.Errorf("lengths %d %d", len([]rune(title)), len([]rune(body)))
	}
	secretToken := "ghp_" + strings.Repeat("a1B2", 8)
	for _, in := range []string{
		"Your code is 482913.", "OTP: 77-1234", "password: hunter2xyz", "token=" + strings.Repeat("f0", 20),
		"open https://example.com/verify?code=12345 now", "open https://example.com/#access_token=abc",
		"use " + secretToken, "bot 123456789:" + strings.Repeat("Ab1_", 10),
	} {
		_, got := Compose("t", in)
		for _, bad := range []string{"482913", "hunter2", "12345", "access_token", "77-1234", secretToken, "123456789:"} {
			if strings.Contains(got, bad) {
				t.Errorf("%q leaked %q: %q", in, bad, got)
			}
		}
	}
	if _, got := Compose("t", "A verification code is needed. See https://example.com/runs/r-1"); got != "A verification code is needed. See https://example.com/runs/r-1" {
		t.Errorf("harmless text changed: %q", got)
	}
}

func TestNotify_Content_OneTimeCodeStatesNeedAndNoCode(t *testing.T) {
	r := newRig(t)
	r.open("a", OriginUser, KindApply)
	r.move("a", NeedsYou, MoveArgs{Reason: "one-time-code", Detail: "Enter 884213 on the page"})
	r.process()
	body := r.desktop.announced[0].Body
	if !strings.Contains(body, "verification code is needed") || strings.Contains(body, "884213") {
		t.Errorf("body = %q", body)
	}
}

func TestNotify_Producers_AllSendThroughOnePolicy(t *testing.T) {
	r := newRig(t)
	if !r.policy.AnnounceCostCap(bg, "2026-03-10", "The daily model spend cap of USD 5.00 is reached.") {
		t.Fatal("cost-cap alert refused")
	}
	if !r.policy.AnnounceCostCap(bg, "2026-03-10", "The daily model spend cap of USD 5.00 is reached.") {
		t.Fatal("cost-cap alert refused the second time")
	}
	r.process()
	if r.desktop.count() != 1 || r.tg.count() != 1 {
		t.Errorf("the cost-cap alert was announced %d/%d times", r.desktop.count(), r.tg.count())
	}
	r.quiet = QuietHours{true, "00:00", "23:59", "UTC"}
	r.policy.Announce(bg, Event{Key: "p1", Kind: EventPromptResult, Title: "Prompt", Body: "done", NeverHold: true})
	r.policy.Announce(bg, Event{Key: "p2", Kind: EventGmailDigest, Title: "Digest", Body: "morning"})
	r.process()
	if got := strings.Join(r.desktop.titles(), ","); !strings.Contains(got, "Prompt") || strings.Contains(got, "Digest") {
		t.Errorf("quiet hours: %s", got)
	}
}

// Gone model, quiet hours: the announcement is in the closed list, normal
// class, held through the window, delivered once afterwards, and a key that
// repeats is ignored.
func TestNotify_Policy_ModelGoneHeldOnceAndDeduplicated(t *testing.T) {
	r := newRig(t)
	r.quiet = QuietHours{true, "22:00", "08:00", "UTC"}
	r.clk.t = time.Date(2026, 3, 10, 23, 30, 0, 0, time.UTC)
	ev := Event{Key: "model-gone|narrative|retired/model|ep1", Kind: EventModelGone, Title: "A routed model left the catalog",
		Body: "The narrative task routes to retired/model, which OpenRouter no longer lists.", Link: "/settings"}
	if err := r.policy.Announce(bg, ev); err != nil {
		t.Fatalf("model-gone is not in the closed list: %v", err)
	}
	if err := r.policy.Announce(bg, ev); err != nil {
		t.Fatal(err)
	}
	r.process()
	if r.desktop.count() != 0 || r.tg.count() != 0 || r.outboxStates()["held"] != 1 {
		t.Fatalf("held in the window: desktop %v telegram %v states %v", r.desktop.titles(), r.tg.titles(), r.outboxStates())
	}
	r.clk.t = time.Date(2026, 3, 11, 8, 0, 0, 0, time.UTC)
	r.process()
	// The routing is changed in Settings, so the phone is not told.
	if r.desktop.count() != 1 || r.tg.count() != 0 {
		t.Fatalf("after the window: desktop %v telegram %v", r.desktop.titles(), r.tg.titles())
	}
	if r.desktop.announced[0].Urgent {
		t.Error("a gone model is not urgent")
	}
	if err := r.policy.Announce(bg, ev); err != nil {
		t.Fatal(err)
	}
	r.process()
	if r.desktop.count() != 1 {
		t.Errorf("the same key was announced again: %d", r.desktop.count())
	}
}

func TestNotify_Desktop_UnattachedGoesToNoticesNotDelivered(t *testing.T) {
	r := newRig(t)
	r.desktop.off = true
	r.open("a", OriginUser, KindApply)
	r.move("a", NeedsYou, MoveArgs{Reason: "answer"})
	r.process()
	if r.desktop.count() != 0 || len(r.desktop.notices) != 1 || r.desktop.notices[0].Handle() != "a" || r.tg.count() != 1 {
		t.Fatalf("desktop %v notices %+v telegram %v", r.desktop.titles(), r.desktop.notices, r.tg.titles())
	}
	var n int
	if err := r.st.Read().QueryRow(`SELECT count(*) FROM notify_deliveries WHERE sink = 'desktop'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("the desktop recorded a delivery nobody saw: %d %v", n, err)
	}
	r.move("a", Working, MoveArgs{})
	if !slices.Contains(r.desktop.forgotten, "a") {
		t.Errorf("handled, its notice was not removed: %v", r.desktop.forgotten)
	}
	// Switched off, the channel keeps nothing either.
	off := false
	if _, err := r.policy.o.Settings.Patch(bg, SettingsPatch{Desktop: &off}); err != nil {
		t.Fatal(err)
	}
	r.quiet = QuietHours{true, "00:00", "23:59", "UTC"}
	r.open("b", OriginUser, KindEvaluate)
	r.move("b", Done, MoveArgs{Outcome: OutcomeOK})
	r.open("c", OriginUser, KindApply)
	r.move("c", NeedsYou, MoveArgs{Reason: "answer"})
	r.clk.advance(time.Minute)
	r.process()
	if len(r.desktop.notices) != 1 {
		t.Errorf("a switched-off desktop channel kept notices: %+v", r.desktop.notices)
	}
}

func TestNotify_Desktop_NoticesReplaceByHandleKeepReadCapAndExpire(t *testing.T) {
	r := newRig(t)
	changed := 0
	d := NewDesktop(DesktopOptions{Store: r.st, Now: r.clk.Now, Changed: func() { changed++ }})
	if d.Available(bg) {
		t.Error("available with no client attached")
	}
	list := func() []string {
		t.Helper()
		got, err := d.Notices(bg)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, n := range got.Notices {
			out = append(out, fmt.Sprintf("%s:%v", n.Title, n.Read))
		}
		return append(out, fmt.Sprint(got.Unread))
	}
	d.Notice(bg, Announcement{EntryID: "e1", Title: "Apply to Acme", Body: "Sign in to continue."})
	d.Text(bg, "Telegram paired", "This chat can now talk to the app.", "/settings")
	d.Text(bg, "Telegram paired", "This chat can now talk to the app.", "/settings")
	if got := strings.Join(list(), " "); got != "Telegram paired:false Telegram paired:false Apply to Acme:false 3" {
		t.Fatalf("list = %s", got)
	}
	if err := d.Read(bg, nil); err != nil {
		t.Fatal(err)
	}
	// The same text again stays read; new text for the entry replaces it, unread.
	d.Notice(bg, Announcement{EntryID: "e1", Title: "Apply to Acme", Body: "Sign in to continue."})
	if got := strings.Join(list(), " "); got != "Telegram paired:true Telegram paired:true Apply to Acme:true 0" {
		t.Fatalf("after the same text = %s", got)
	}
	d.Notice(bg, Announcement{EntryID: "e1", Title: "Apply to Acme", Body: "It finished."})
	if got := strings.Join(list(), " "); got != "Telegram paired:true Telegram paired:true Apply to Acme:false 1" {
		t.Fatalf("after new text = %s", got)
	}
	// Shown natively after all (released after quiet hours), it is read.
	d.Seen(bg, "e1")
	d.Seen(bg, "e1")
	if got := strings.Join(list(), " "); got != "Telegram paired:true Telegram paired:true Apply to Acme:true 0" {
		t.Fatalf("after seen = %s", got)
	}
	d.Forget(bg, "e1")
	d.Forget(bg, "e-none")
	if got := strings.Join(list(), " "); got != "Telegram paired:true Telegram paired:true 0" {
		t.Fatalf("after forget = %s", got)
	}
	if changed != 7 {
		t.Errorf("changed %d times, want 7 (the same text again, a second seen and a forget with nothing to remove are silent)", changed)
	}
	for i := range noticesKept + 5 {
		d.Text(bg, fmt.Sprintf("n%d", i), "", "")
	}
	if got := list(); len(got) != noticesKept+1 || got[0] != fmt.Sprintf("n%d:false", noticesKept+4) {
		t.Fatalf("kept %d, newest %s", len(got)-1, got[0])
	}
	r.clk.advance(DoneRetention + time.Hour)
	d.Text(bg, "fresh", "", "")
	if err := r.policy.Prune(bg); err != nil {
		t.Fatal(err)
	}
	if got := list(); len(got) != 2 || got[0] != "fresh:false" {
		t.Errorf("after the sweep = %v", got)
	}
}

// scanRecord is a scan_runs row as discovery.Repo writes it: the run id of the
// scan's run record is in its params.
func scanRecord(t *testing.T, r *rig, id, status, runID string) {
	t.Helper()
	now := ts(r.clk.Now())
	err := r.st.Write(bg, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO scan_runs (id, source, status, started_at, updated_at, record_json) VALUES (?, 'jobright', ?, ?, ?, ?)`,
			id, status, now, now, `{"id":"`+id+`","source":"jobright","status":"`+status+`","counts":{"discovered":0},"params":{"run_id":"`+runID+`"}}`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// insertEval is an evaluation's run record and job row as the evaluator writes
// them, queued by the scan record scanRun (blank for none).
func insertEval(t *testing.T, r *rig, id, scanRun, identity string) {
	t.Helper()
	now := ts(r.clk.Now())
	err := r.st.Write(bg, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO runs (id, kind, status, input_json, created_at, updated_at) VALUES (?, 'evaluate', 'queued', ?, ?, ?)`, id,
			`{"URL":"https://jobs.example.com/`+identity+`","ScanRun":"`+scanRun+`","Source":"jobright","Mode":"screen","InboxRowID":85}`, now, now); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO evaluation_jobs (run_id, identity_key, mode, priority, source, posting_url, created_at) VALUES (?, ?, 'screen', 'scan', 'jobright', '', ?)`,
			id, identity, now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The evaluations a scan queued are the scan's work: they join its batch with
// its origin, none is announced on its own, and the scan is announced once,
// after the last of them ended, with what they came to. A scheduled scan's
// evaluations, a review among them, announce nothing (F-2).
func TestNotify_Scan_EvaluationsAreToldThroughTheScansRollup(t *testing.T) {
	r := newRig(t)
	r.board.SetOutcome(func(_ context.Context, kind Kind, id string) string {
		if kind == KindScan {
			return "Jobright: 2 found, 2 new, 2 queued for evaluation."
		}
		return ""
	})
	r.policy.SetScanRollup(func(_ context.Context, scan Entry, members []Entry) string {
		return fmt.Sprintf("%s Evaluated: %d.", scan.Detail, len(members))
	})
	scanRecord(t, r, "scan-a", "running", "s-1")
	insertRun(t, r, "s-1", "scan", "running", "", false)
	insertEval(t, r, "e-1", "scan-a", "k1")
	insertEval(t, r, "e-2", "scan-a", "k2")
	r.board.SyncRuns(bg)
	if e, _ := r.board.Get(bg, "e-1"); e.BatchID != ScanBatch+"s-1" || e.Origin != OriginUser {
		t.Fatalf("a scan's evaluation = %+v", e)
	}
	r.clk.advance(time.Second)
	setRun(t, r, "s-1", "succeeded", "")
	setRun(t, r, "e-1", "review_required", "")
	r.board.SyncRuns(bg)
	r.clk.advance(DefaultDoneDelay + time.Second)
	r.process()
	if r.desktop.count() != 0 {
		t.Fatalf("announced before the last evaluation ended: %+v", r.desktop.announced)
	}
	setRun(t, r, "e-2", "succeeded", "")
	r.board.SyncRuns(bg)
	r.clk.advance(DefaultDoneDelay + time.Second)
	r.process()
	if r.desktop.count() != 1 || r.desktop.announced[0].EntryID != "s-1" || r.desktop.announced[0].Body != "Jobright: 2 found, 2 new, 2 queued for evaluation. Evaluated: 2." {
		t.Fatalf("announced = %+v", r.desktop.announced)
	}
	// A scheduled scan that ended in the pass its evaluation first appeared.
	insertRun(t, r, "s-2", "discover.jobright", "succeeded", "", true)
	scanRecord(t, r, "scan-b", "completed", "s-2")
	insertEval(t, r, "e-3", "scan-b", "k3")
	r.board.SyncRuns(bg)
	if e, _ := r.board.Get(bg, "e-3"); e.BatchID != ScanBatch+"s-2" || e.Origin != OriginSchedule || e.State != Working {
		t.Fatalf("a scheduled scan's evaluation = %+v", e)
	}
	r.clk.advance(time.Second)
	setRun(t, r, "e-3", "review_required", "")
	r.board.SyncRuns(bg)
	r.clk.advance(DefaultDoneDelay + time.Second)
	r.process()
	if r.desktop.count() != 1 {
		t.Errorf("a scheduled scan's review was announced: %+v", r.desktop.announced)
	}
}

// A later evaluation of a posting answers the review an earlier one waits on.
func TestNotify_Scan_ALaterEvaluationResolvesTheReview(t *testing.T) {
	r := newRig(t)
	insertEval(t, r, "e-1", "", "k1")
	insertEval(t, r, "e-2", "", "k1")
	r.board.SyncRuns(bg)
	r.clk.advance(time.Second)
	setRun(t, r, "e-1", "review_required", "")
	r.board.SyncRuns(bg)
	r.clk.advance(time.Second)
	setRun(t, r, "e-2", "succeeded", "")
	r.board.SyncRuns(bg)
	if e, _ := r.board.Get(bg, "e-1"); e.State != Done || e.Outcome != OutcomeOK || !e.Acked() {
		t.Errorf("the answered review = %+v", e)
	}
}

// The evaluations the inbox drain sent are its work, a review's evaluation in
// depth among them, and a row an earlier scan found: they join the drain's
// batch with the drain's origin, none is told on its own, and the drain is told
// once, after its own end and the last of theirs, whichever comes last, with
// what they came to. The review leaves Needs you as soon as its evaluation's
// entry opens, closed into it, and its own notification goes (F-2, F-8).
func TestNotify_Drain_ItsEvaluationsAreToldOnceThroughItsRollup(t *testing.T) {
	for _, drainFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("the drain ends first: %v", drainFirst), func(t *testing.T) {
			r := newRig(t)
			r.policy.SetScanRollup(func(_ context.Context, _ Entry, members []Entry) string {
				return fmt.Sprintf("%d evaluated.", len(members))
			})
			insertRun(t, r, "s-1", "discover.jobright", "succeeded", "", false)
			scanRecord(t, r, "scan-a", "completed", "s-1")
			// The user's own quick screen, held back by the daily cap.
			insertEval(t, r, "e-0", "", "k0")
			r.board.SyncRuns(bg)
			r.clk.advance(time.Second)
			setRun(t, r, "e-0", "review_required", "")
			r.board.SyncRuns(bg)
			r.clk.advance(DefaultDoneDelay + time.Second) // the scan's own end is told
			r.process()
			// A scheduled drain sends a row the scan found, a new row and the review.
			insertRun(t, r, "d-1", "inbox.evaluate", "running", "", true)
			for _, ev := range []struct{ id, scanRun, identity, set string }{
				{"e-1", "scan-a", "k1", `'$.Drain', 'd-1'`},
				{"e-2", "", "k2", `'$.Drain', 'd-1'`},
				{"e-3", "", "k0", `'$.Drain', 'd-1', '$.Review', 'e-0', '$.Mode', 'deep'`},
			} {
				insertEval(t, r, ev.id, ev.scanRun, ev.identity)
				if err := r.st.Write(bg, func(tx *sql.Tx) error {
					_, err := tx.Exec(`UPDATE runs SET input_json = json_set(input_json, `+ev.set+`) WHERE id = ?`, ev.id)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			r.board.SyncRuns(bg)
			for _, id := range []string{"e-1", "e-2", "e-3"} {
				if e, _ := r.board.Get(bg, id); e.BatchID != ScanBatch+"d-1" || e.Origin != OriginSchedule {
					t.Errorf("%s = %+v, want the drain's batch and origin", id, e)
				}
			}
			if e, _ := r.board.Get(bg, "e-0"); e.State != Done || e.Outcome != OutcomeOK || !e.Acked() {
				t.Errorf("the review sent to an evaluation in depth = %+v", e)
			}
			if in, _ := r.board.Inbox(bg); len(in) != 0 {
				t.Errorf("Needs you holds %+v", in)
			}
			if !slices.ContainsFunc(r.desktop.withdrawn, func(w withdrawal) bool { return w.A.EntryID == "e-0" }) {
				t.Errorf("the review's notification stayed: withdrawn %+v", r.desktop.withdrawn)
			}
			before := r.desktop.count()
			end := func(ids ...string) {
				t.Helper()
				r.clk.advance(time.Minute)
				for _, id := range ids {
					if id == "e-2" {
						setRun(t, r, id, "failed", "error")
					} else {
						setRun(t, r, id, "succeeded", "")
					}
				}
				r.board.SyncRuns(bg)
				r.clk.advance(DefaultDoneDelay + time.Second)
				r.process()
			}
			if drainFirst {
				end("d-1")
				end("e-1", "e-2", "e-3")
			} else {
				end("e-1", "e-2", "e-3")
				end("d-1")
			}
			var told []string
			for _, a := range r.desktop.announced[before:] {
				told = append(told, a.EntryID+": "+a.Body)
			}
			if !slices.Equal(told, []string{"d-1: 3 evaluated."}) {
				t.Errorf("announced %q, want the drain once", told)
			}
		})
	}
}

// Repeated stops of one posting fold into one entry, told once; the newest
// stop owns what was shown, so handling it closes that notification (F-2). A
// stop of another posting at the same company is its own, though its entry
// reads alike: the apply chain's run record names the posting.
func TestNotify_Fold_RepeatedStopsOfOnePostingAreOneEntryToldOnce(t *testing.T) {
	r := newRig(t)
	for _, run := range []struct{ id, key string }{{"a-1", "greenhouse:teamsnap/1"}, {"a-2", "greenhouse:teamsnap/1"}, {"a-3", "greenhouse:teamsnap/1"}, {"b-1", "greenhouse:teamsnap/2"}} {
		if err := r.st.Write(bg, func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO apply_runs (run_id, started_at, caller, posting_url, identity_key) VALUES (?, '2026-03-10T12:00:00.000Z', 'dashboard', ?, ?)`,
				run.id, "https://boards.example.test/"+run.key, run.key)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.board.Open(bg, OpenArgs{ID: run.id, Kind: KindApply, Origin: OriginUser, Title: "Apply to TeamSnap"}); err != nil {
			t.Fatal(err)
		}
	}
	r.move("a-1", NeedsYou, MoveArgs{Reason: "answer"})
	r.process()
	r.clk.advance(time.Minute)
	r.move("b-1", NeedsYou, MoveArgs{Reason: "answer"})
	r.process()
	if r.desktop.count() != 2 {
		t.Fatalf("another posting's stop was folded: %+v", r.desktop.announced)
	}
	r.move("a-2", NeedsYou, MoveArgs{Reason: "answer"})
	r.process()
	if r.desktop.count() != 2 {
		t.Fatalf("a repeated stop was announced again: %+v", r.desktop.announced)
	}
	in, _ := r.board.Inbox(bg)
	var ids []string
	for _, e := range in {
		ids = append(ids, e.ID)
	}
	if slices.Sort(ids); !slices.Equal(ids, []string{"a-2", "b-1"}) {
		t.Fatalf("inbox = %v", ids)
	}
	// Another reason is another stop.
	r.move("a-3", NeedsYou, MoveArgs{Reason: "sign-in"})
	r.process()
	if r.desktop.count() != 3 {
		t.Fatalf("a new stop was not announced: %+v", r.desktop.announced)
	}
	if _, err := r.board.Ack(bg, "a-2"); err != nil {
		t.Fatal(err)
	}
	if len(r.desktop.withdrawn) != 1 || r.desktop.withdrawn[0].Ext != "desktop-1" {
		t.Errorf("the folded stop's notification stayed: %+v", r.desktop.withdrawn)
	}
}

// Stops already on the board when the server starts were never retired by a
// later run's start, which acts on its own posting, nor compared by fold, which
// acts when a stop arrives. At start, once and silently: an application's stop
// whose posting has a later run is acknowledged, waiting or blocked, except a
// possible submission (F-3); the newest run's stop and a stop of another
// posting at the same company stay; repeated stops of one evaluation fold, the
// newest staying (F-2). The waits left take the names the workspace learned of
// their postings after they were titled; one nothing names keeps its title. A
// second start finds nothing to do.
func TestNotify_Fold_AtStartStaleStopsLeaveAndRepeatsFoldOnce(t *testing.T) {
	r := newRig(t)
	seed := func(q string, args ...any) {
		t.Helper()
		if err := r.st.Write(bg, func(tx *sql.Tx) error { _, err := tx.Exec(q, args...); return err }); err != nil {
			t.Fatal(err)
		}
	}
	r.board.SetObserver(nil) // a process that recorded the stops without retiring or folding them
	for _, run := range []struct {
		id, key string
		to      State
		reason  string
	}{{"a-1", "lever:northwind/1", NeedsYou, "sign-in"}, {"b-1", "lever:northwind/2", NeedsYou, "answer"}, {"a-2", "lever:northwind/1", Blocked, "lane-unavailable"},
		{"a-3", "lever:northwind/1", NeedsYou, "may-have-submitted"}, {"a-4", "lever:northwind/1", NeedsYou, "answer"}} {
		seed(`INSERT INTO apply_runs (run_id, started_at, caller, posting_url, identity_key) VALUES (?, ?, 'dashboard', ?, ?)`,
			run.id, ts(r.clk.Now()), "https://jobs.lever.co/"+strings.TrimPrefix(run.key, "lever:"), run.key)
		if _, err := r.board.Open(bg, OpenArgs{ID: run.id, Kind: KindApply, Origin: OriginUser, Title: "Apply to northwind"}); err != nil {
			t.Fatal(err)
		}
		r.move(run.id, run.to, MoveArgs{Reason: run.reason})
		r.clk.advance(time.Minute)
	}
	for _, id := range []string{"e-0", "e-1"} {
		insertRun(t, r, id, "evaluate", "review_required", "", false)
		seed(`INSERT INTO evaluation_jobs (run_id, identity_key, mode, priority, created_at) VALUES (?, 'lever:northwind/1', 'auto', 'user', ?)`, id, ts(r.clk.Now()))
		r.board.SyncRuns(bg)
		r.clk.advance(time.Minute)
	}
	seed(`INSERT INTO inbox (url, original_url, identity_key, company, role, source, first_seen_at, updated_at) VALUES (?1, ?1, 'lever:northwind/1', 'Northwind Example',
		'Senior Backend Engineer', 'lever', ?2, ?2)`, "https://jobs.lever.co/northwind/1", ts(r.clk.Now()))
	r.board.SetObserver(r.policy)
	if n, err := r.policy.FoldRepeats(bg); err != nil || n != 3 {
		t.Fatalf("acknowledged %d, %v", n, err)
	}
	in, _ := r.board.Inbox(bg)
	var ids []string
	for _, e := range in {
		ids = append(ids, e.ID)
	}
	if slices.Sort(ids); !slices.Equal(ids, []string{"a-3", "a-4", "b-1", "e-1"}) {
		t.Fatalf("inbox = %v", ids)
	}
	if _, err := r.board.RenameWaits(bg); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"a-3": "Apply to Northwind Example: Senior Backend Engineer", "a-4": "Apply to Northwind Example: Senior Backend Engineer",
		"b-1": "Apply to northwind", "e-1": "Evaluate Senior Backend Engineer at Northwind Example"} {
		if e, err := r.board.Get(bg, id); err != nil || e.Title != want {
			t.Errorf("%s = %q, %v, want %q", id, e.Title, err, want)
		}
	}
	// A stale stop's notice leaves with it, as at a run's start; a folded
	// stop's notice moves to the newer one.
	r.process()
	if forgot := slices.Sorted(slices.Values(r.desktop.forgotten)); r.desktop.count() != 0 || !slices.Equal(forgot, []string{"a-1", "a-2"}) || len(r.desktop.withdrawn) != 0 {
		t.Errorf("the start passes were not silent: %d announced, %v forgotten, %d withdrawn", r.desktop.count(), forgot, len(r.desktop.withdrawn))
	}
	if n, err := r.policy.FoldRepeats(bg); err != nil || n != 0 {
		t.Errorf("a second start acknowledged %d, %v", n, err)
	}
	if n, err := r.board.RenameWaits(bg); err != nil || n != 0 {
		t.Errorf("a second start renamed %d, %v", n, err)
	}
}
