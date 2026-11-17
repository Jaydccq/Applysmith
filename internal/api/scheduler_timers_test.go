package api

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"applysmith/internal/api/wire"
	"applysmith/internal/gmail/classify"
	"applysmith/internal/notify"
	"applysmith/internal/tracker"
	"applysmith/internal/workspace"
)

// tickOnce runs one scheduler tick and waits for the timers it started.
func tickOnce(t *testing.T, h *harness) {
	t.Helper()
	sc := h.domainOf().sched
	sc.Tick(context.Background())
	sc.Wait()
}

func countOutbox(t *testing.T, h *harness, where string) int {
	t.Helper()
	var n int
	if err := h.srv.store.Load().Read().QueryRow(`SELECT count(*) FROM notify_outbox WHERE ` + where).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// setQuietHours puts the 22:00 to 07:00 UTC window in the profile; the fake
// clock starts at 03:04 UTC.
func setQuietHours(t *testing.T, h *harness) {
	t.Helper()
	setQuietWindow(t, h, "22:00", "07:00")
}

// setQuietWindow puts a quiet-hours window, in UTC, in the profile.
func setQuietWindow(t *testing.T, h *harness, start, end string) {
	t.Helper()
	raw, err := h.ws.Read(workspace.FileProfile)
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw.Data)
	from := strings.Index(src, "\nnotifications:")
	if from < 0 {
		t.Fatalf("no notifications block in the seeded profile:\n%s", src)
	}
	quiet := "\nnotifications:\n  quiet_hours:\n    enabled: true\n    start: \"" + start + "\"\n    end: \"" + end + "\"\n    timezone: UTC\n"
	if err := os.WriteFile(h.ws.Path(workspace.FileProfile), []byte(src[:from]+quiet), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestScheduler_Timers_QuietHoursReleaseEachTick(t *testing.T) {
	h := newDomainHarness(t)
	setQuietHours(t, h)
	d := h.domainOf()
	ctx := context.Background()
	h.openStream("/api/notifications/events", h.token(), "") // the app is attached
	if err := d.notify.policy.Announce(ctx, notify.Event{Key: "checkin|quiet", Kind: notify.EventGoalCheckin, Title: "Goal check-in", Body: "Plan", Link: "/ask#goals"}); err != nil {
		t.Fatal(err)
	}
	if err := d.notify.policy.Process(ctx); err != nil {
		t.Fatal(err)
	}
	if countOutbox(t, h, `state = 'held'`) != 1 {
		t.Fatal("the announcement was not held in quiet hours")
	}
	tickOnce(t, h)
	if countOutbox(t, h, `state = 'held'`) != 1 {
		t.Fatal("a tick inside the window released it")
	}
	h.clock.advance(5 * time.Hour) // 08:04, after the window
	tickOnce(t, h)                 // nothing else wakes the policy: the tick's timer releases it
	if n := countOutbox(t, h, `state = 'held'`); n != 0 {
		t.Fatalf("%d announcements still held after the window", n)
	}
	var delivered int
	if err := h.srv.store.Load().Read().QueryRow(`SELECT count(*) FROM notify_deliveries WHERE sink = 'desktop' AND state = 'delivered'`).Scan(&delivered); err != nil || delivered != 1 {
		t.Fatalf("delivered %d: %v", delivered, err)
	}
}

func TestScheduler_Timers_CapAlertCheckEachTick(t *testing.T) {
	h := newDomainHarness(t)
	ctx := context.Background()
	// The day's spend is over the cap and no model call follows: only the
	// tick's check can raise the alert.
	err := h.srv.store.Load().Write(ctx, func(tx *sqlTx) error {
		_, err := tx.Exec(`INSERT INTO llm_ledger (call_id, started_at, task, provider_id, provider_kind, model, attempt, candidate_source, outcome, cost_usd, billing)
			VALUES ('spend', ?, 'narrative', 'p', 'openai-compatible', 'm', 1, 'primary', 'ok', 500, 'metered')`, h.clock.now().UTC().Format("2006-01-02T15:04:05.000Z"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if countOutbox(t, h, `kind = 'cost-cap'`) != 0 {
		t.Fatal("an alert before the check")
	}
	tickOnce(t, h)
	waitFor(t, "the cap alert", func() bool { return countOutbox(t, h, `kind = 'cost-cap'`) == 1 })
	tickOnce(t, h)
	tickOnce(t, h)
	if n := countOutbox(t, h, `kind = 'cost-cap'`); n != 1 {
		t.Fatalf("%d alerts for one day", n)
	}
}

func TestScheduler_Timers_DigestNeedsGmail(t *testing.T) {
	timer := func(h *harness, name string) wire.ScheduleTimer {
		for _, tm := range h.get("/api/schedules").data()["timers"].([]any) {
			m := tm.(map[string]any)
			if m["name"] == name {
				active, _ := m["active"].(bool)
				reason, _ := m["reason"].(string)
				return wire.ScheduleTimer{Name: name, Active: active, Reason: reason}
			}
		}
		t.Fatalf("no timer %s", name)
		return wire.ScheduleTimer{}
	}
	h, _ := unconnectedGmailHarness(t)
	if got := timer(h, "gmail-digest"); got.Active || got.Reason != gmailNotReadable {
		t.Fatalf("without Gmail: %+v", got)
	}
	h.connectGmail()
	if got := timer(h, "gmail-digest"); !got.Active {
		t.Fatalf("connected: %+v", got)
	}
	off := false
	if r := h.send("PATCH", "/api/gmail/settings", wire.GmailSettingsPatch{ScheduledDigests: &off}); r.StatusCode != 200 {
		t.Fatalf("settings: %d %s", r.StatusCode, r.body)
	}
	if got := timer(h, "gmail-digest"); got.Active || got.Reason != "scheduled digests are switched off" {
		t.Fatalf("digests off: %+v", got)
	}
	on := true
	h.send("PATCH", "/api/gmail/settings", wire.GmailSettingsPatch{ScheduledDigests: &on})
	// The 08:00 slot, caught up at the next tick, produces the digest.
	now := h.clock.now().In(time.Local) // the slots are local times
	slot := time.Date(now.Year(), now.Month(), now.Day(), 8, 4, 0, 0, time.Local)
	if !slot.After(now) {
		slot = slot.AddDate(0, 0, 1)
	}
	h.clock.advance(slot.Sub(now))
	tickOnce(t, h)
	var scheduled int
	if err := h.srv.store.Load().Read().QueryRow(`SELECT count(*) FROM gmail_digests`).Scan(&scheduled); err != nil || scheduled != 1 {
		t.Fatalf("digests after the 08:00 slot: %d %v", scheduled, err)
	}
	tickOnce(t, h)
	if err := h.srv.store.Load().Read().QueryRow(`SELECT count(*) FROM gmail_digests`).Scan(&scheduled); err != nil || scheduled != 1 {
		t.Fatalf("the slot ran twice: %d %v", scheduled, err)
	}
}

func TestScheduler_Timers_InstantAlertsEachTick(t *testing.T) {
	h, _ := gmailHarness(t)
	seedGmail(t, h, apiSignal{id: "ap", typ: classify.TypeApplied, at: h.clock.now().Add(-3 * time.Hour)})
	tickOnce(t, h) // the first evaluation after a sync seeds silently
	seedGmail(t, h, apiSignal{id: "a1", typ: classify.TypeAssessment, at: h.clock.now().Add(-time.Hour)})
	if countOutbox(t, h, `kind = 'gmail-alert'`) != 0 {
		t.Fatal("an alert before the tick")
	}
	tickOnce(t, h)
	waitFor(t, "the instant alert", func() bool { return countOutbox(t, h, `kind = 'gmail-alert'`) == 1 })
	tickOnce(t, h)
	if n := countOutbox(t, h, `kind = 'gmail-alert'`); n != 1 {
		t.Fatalf("%d alerts for one thread", n)
	}
}

// beforeEvening moves the clock to ten minutes before the next 18:00 local
// time, the day summary's slot, so what a test adds next is on the slot's day.
func beforeEvening(h *harness) time.Time { return beforeHour(h, daySummaryHour) }

// beforeHour moves the clock to ten minutes before the next hour:00 local time.
func beforeHour(h *harness, hour int) time.Time {
	now := h.clock.now().In(time.Local)
	slot := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, time.Local)
	for !slot.Add(-10 * time.Minute).After(now) {
		slot = slot.AddDate(0, 0, 1)
	}
	h.clock.advance(slot.Add(-10 * time.Minute).Sub(now))
	return slot
}

func (r narratorRig) tickDay() {
	r.t.Helper()
	if err := (daySummaryTimer{s: r.h.srv, d: r.d}).Tick(r.ctx, r.h.clock.now()); err != nil {
		r.t.Fatal(err)
	}
	if err := r.d.notify.policy.Process(r.ctx); err != nil {
		r.t.Fatal(err)
	}
}

// A day with nothing done and nothing left sends no summary; an application
// waiting for a reply asks nothing of the user, so it does not make one (F-2).
func TestNarrator_DaySummary_NothingOnAnEmptyDay(t *testing.T) {
	r := newNarratorRig(t)
	if _, err := r.d.tracker.Add(r.ctx, tracker.AddRow{Company: "Hooli", Role: "Engineer", Status: tracker.Applied}); err != nil {
		t.Fatal(err)
	}
	r.h.clock.advance(24 * time.Hour)
	beforeEvening(r.h)
	r.h.clock.advance(11 * time.Minute)
	r.tickDay()
	if n := countOutbox(t, r.h, `kind = 'day-summary'`); n != 0 {
		t.Fatalf("an empty day sent %d summaries", n)
	}
}

// The day summary goes out once a day. When the evening Gmail digest comes
// first it carries the summary in its push and the timer sends nothing more;
// on a day the timer sent it, a later digest does not carry it again (F-2).
func TestNarrator_DaySummary_OnceADayWithOrWithoutTheEveningDigest(t *testing.T) {
	r := newNarratorRig(t)
	r.h.connectGmail()
	digest := gmailDigestTimer{d: r.d}
	digests := 0
	for i, digestFirst := range []bool{true, false} {
		beforeEvening(r.h)
		r.apply(fmt.Sprintf("run_%d", i), "Acme", confirmed, 0)
		seedGmail(t, r.h, apiSignal{id: fmt.Sprintf("m%d", i), typ: classify.TypeReplyNeeded, at: r.h.clock.now().Add(-time.Hour)})
		r.h.clock.advance(11 * time.Minute)
		if digestFirst {
			if err := digest.Tick(r.ctx, r.h.clock.now()); err != nil {
				t.Fatal(err)
			}
		}
		r.h.clock.advance(digestWait)
		r.tickDay()
		if !digestFirst {
			if err := digest.Tick(r.ctx, r.h.clock.now()); err != nil {
				t.Fatal(err)
			}
		}
		claim, err := readDayClaim(r.ctx, r.h.srv.store.Load(), daySummaryMeta)
		if err != nil || claim.Title == "" {
			t.Fatalf("day %d: claim = %+v, %v", i, claim, err)
		}
		// The day's digest pushes are the rows after the earlier days' ones.
		var carried int
		err = r.h.srv.store.Load().Read().QueryRow(`SELECT count(*) FROM (SELECT body FROM notify_outbox WHERE kind = 'gmail-digest' ORDER BY id LIMIT -1 OFFSET ?)
			WHERE instr(body, ?) > 0`, digests, claim.Title).Scan(&carried)
		if err != nil {
			t.Fatal(err)
		}
		pushed := countOutbox(t, r.h, `kind = 'gmail-digest'`) - digests
		digests += pushed
		if summaries := countOutbox(t, r.h, `kind = 'day-summary'`); pushed == 0 || summaries != i || (carried > 0) != digestFirst {
			t.Errorf("day %d (digest first %v): %d day summaries, %d of the day's %d digest pushes carry it", i, digestFirst, summaries, carried, pushed)
		}
	}
}

// The morning brief tells the user at 08:00 what waits for them today, naming
// the runs that wait on them, once a morning and never on a morning when
// nothing does.
func TestNarrator_MorningBrief_SaysWhatWaitsOnceAMorning(t *testing.T) {
	r := newNarratorRig(t)
	tick := func() {
		t.Helper()
		if err := (morningBriefTimer{s: r.h.srv, d: r.d}).Tick(r.ctx, r.h.clock.now()); err != nil {
			t.Fatal(err)
		}
		if err := r.d.notify.policy.Process(r.ctx); err != nil {
			t.Fatal(err)
		}
	}
	beforeHour(r.h, morningBriefHour)
	r.h.clock.advance(11 * time.Minute)
	tick()
	if n := countOutbox(t, r.h, `kind = 'morning-brief'`); n != 0 {
		t.Fatalf("a morning with nothing waiting sent %d briefs", n)
	}
	beforeHour(r.h, morningBriefHour)
	if _, err := r.d.notify.board.Raise(r.ctx, notify.RaiseArgs{ID: "run_wait", Kind: notify.KindApply, Origin: notify.OriginUser, Title: "Apply to Contoso",
		State: notify.NeedsYou, Reason: "answer"}); err != nil {
		t.Fatal(err)
	}
	tick()
	if n := countOutbox(t, r.h, `kind = 'morning-brief'`); n != 0 {
		t.Fatalf("%d briefs before 08:00", n)
	}
	r.h.clock.advance(11 * time.Minute)
	tick()
	r.h.clock.advance(time.Hour)
	tick()
	var title, body string
	if err := r.h.srv.store.Load().Read().QueryRow(`SELECT title, body FROM notify_outbox WHERE kind = 'morning-brief'`).Scan(&title, &body); err != nil {
		t.Fatal(err)
	}
	if n := countOutbox(t, r.h, `kind = 'morning-brief'`); n != 1 || !strings.Contains(title, "1 thing waits for you") || !strings.Contains(body, "Apply to Contoso") {
		t.Fatalf("%d briefs; title %q body %q", n, title, body)
	}
}
