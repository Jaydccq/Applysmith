package signals

import (
	"strings"
	"testing"
	"time"
)

func TestNotify_HotEntry_OncePerRevisionUnsentKeepsSlot(t *testing.T) {
	e := newEnv(t)
	e.profile()
	c := e.readyCompany("Example Corp", "examplecorp", ghJob{1, "Software Engineer", "Remote - US", ""})
	// No channel took the first attempt: a no-op that keeps the slot.
	e.note.delivery = Delivery{}
	e.tick()
	if len(e.note.sent(NoticeHot)) != 0 || e.count(`SELECT count(*) FROM signal_notifications WHERE state = 'noop'`) == 0 {
		t.Fatal("the unsent attempt was not recorded as a no-op")
	}
	if e.count(`SELECT count(*) FROM signal_notifications WHERE state = 'sent' AND kind = 'hot'`) != 0 {
		t.Fatal("an undelivered notice used its slot")
	}
	// A later tick (same revision) still goes out.
	e.note.delivery = Delivery{Queued: true}
	e.clk.advance(time.Hour)
	e.tick()
	hot := e.note.sent(NoticeHot)
	if len(hot) != 1 {
		t.Fatalf("hot notices = %d", len(hot))
	}
	n := hot[0]
	if !strings.Contains(n.Title, "Example Corp") || !strings.Contains(n.Body, "hot") || !strings.Contains(n.Body, "hire 30 engineers") ||
		!strings.Contains(n.Body, "https://techcrunch.com/2026/10/example") || !strings.Contains(n.Body, "Software Engineer") {
		t.Fatalf("notice = %+v", n)
	}
	// The same revision twice is one notification.
	if ok, _ := e.svc.announce(e.ctx, n, c.ID); ok {
		t.Fatal("the same key was announced twice")
	}
	e.clk.advance(time.Hour)
	e.tick()
	if len(e.note.sent(NoticeHot)) != 1 {
		t.Fatalf("hot notices after more ticks = %d", len(e.note.sent(NoticeHot)))
	}
}

func TestNotify_NoTelegramFallsBackToDesktop(t *testing.T) {
	e := newEnv(t)
	e.readyCompany("Example Corp", "examplecorp", ghJob{1, "Software Engineer", "Remote - US", ""})
	e.note.delivery = Delivery{Queued: true, NoOp: []string{"telegram"}}
	e.tick()
	if len(e.note.sent(NoticeHot)) != 1 {
		t.Fatal("the desktop notice was not raised")
	}
	if n := e.count(`SELECT count(*) FROM signal_notifications WHERE state = 'noop' AND detail LIKE 'telegram%'`); n < 1 {
		t.Fatal("the Telegram attempt was not recorded as a no-op")
	}
	if n := e.count(`SELECT count(*) FROM signal_notifications WHERE state = 'sent' AND kind = 'hot'`); n != 1 {
		t.Fatalf("sent rows = %d", n)
	}
}

func TestDigest_OncePerDateWithNewHotBaseline(t *testing.T) {
	e := newEnv(t)
	e.profile()
	secret := "x-" + strings.Repeat("k", 12)
	e.keys[KeyX] = secret
	e.readyCompany("Example Corp", "examplecorp", ghJob{1, "Software Engineer", "Remote - US", ""})
	e.exec(`INSERT INTO signal_review (assessment_id, reason, created_at) SELECT id, 'mid_confidence', ? FROM signal_assessments LIMIT 1`, fmtTS(e.clk.now()))
	alarms := []Alarm{{Source: SourceX, Status: "payment required"}}
	ok, err := e.svc.sendDigest(e.ctx, alarms)
	if err != nil || !ok {
		t.Fatalf("digest %v %v", ok, err)
	}
	if again, _ := e.svc.sendDigest(e.ctx, alarms); again {
		t.Fatal("a second digest went out on the same date")
	}
	d := e.note.sent(NoticeDigest)
	if len(d) != 1 {
		t.Fatalf("digests = %d", len(d))
	}
	for _, want := range []string{"Newly hot: 1", "- Example Corp", "Lane alarms: 1", "signals-x: payment required", "review queue: 1"} {
		if !strings.Contains(d[0].Body, want) {
			t.Errorf("digest lacks %q:\n%s", want, d[0].Body)
		}
	}
	e.clk.advance(26 * time.Hour)
	if ok, err := e.svc.sendDigest(e.ctx, nil); err != nil || !ok {
		t.Fatalf("the next date had no digest: %v", err)
	}
	for _, n := range e.note.sent(NoticeDigest) {
		if strings.Contains(n.Body, secret) {
			t.Fatalf("the digest carries the X key:\n%s", n.Body)
		}
	}
}

func TestOutcomes_HitWithLeadDaysMissAndPending(t *testing.T) {
	e := newEnv(t)
	e.profile()
	day := 24 * time.Hour
	mk := func(name string, eventAge time.Duration) (Company, int64) {
		c, ev, _ := e.company(name, name+" raised.", epoch.Add(-eventAge), adapterAlerts)
		e.assessment(ev, Assessment{})
		return c, ev.ID
	}
	hit, _ := mk("Hit Corp", 20*day)
	miss, _ := mk("Miss Corp", 100*day)
	pend, _ := mk("Pending Corp", 5*day)
	gateFail, _ := mk("Gate Corp", 20*day)
	post := func(c Company, ext, verdict string, seenAge time.Duration) {
		e.exec(`INSERT INTO signal_postings (company_id, ats, board, external_id, url, title, location, verdict, first_seen_at) VALUES (?, 'greenhouse', ?, ?, 'https://x.example.test/j', 't', 'l', ?, ?)`,
			c.ID, c.Canon, ext, verdict, fmtTS(epoch.Add(-seenAge)))
	}
	post(hit, "1", "pass", 8*day)      // 12 days after the event
	post(gateFail, "1", "fail", 8*day) // fails the gate: not a hit
	post(miss, "1", "pass", 1*day)     // after the 90-day window closed
	if err := e.svc.repo.UpdateOutcomes(e.ctx); err != nil {
		t.Fatal(err)
	}
	out := func(c Company) (string, int) {
		var s string
		var d int
		e.st.Read().QueryRowContext(e.ctx, `SELECT status, COALESCE(lead_days, -1) FROM signal_outcomes WHERE company_id = ?`, c.ID).Scan(&s, &d)
		return s, d
	}
	if s, d := out(hit); s != "hit" || d != 12 {
		t.Errorf("hit = %s %d", s, d)
	}
	if s, _ := out(miss); s != "miss" {
		t.Errorf("miss = %s", s)
	}
	if s, _ := out(pend); s != "pending" {
		t.Errorf("pending = %s", s)
	}
	if s, _ := out(gateFail); s != "pending" {
		t.Errorf("a posting that fails the gate counted: %s", s)
	}
	// Re-checked: the pending one hits later and the others keep their verdict.
	post(pend, "2", "pass", 1*day)
	e.svc.repo.UpdateOutcomes(e.ctx)
	if s, d := out(pend); s != "hit" || d != 4 {
		t.Errorf("pending later = %s %d", s, d)
	}
	st, _ := e.svc.repo.OutcomeStats(e.ctx)
	if st.Hits != 2 || st.Misses != 1 || st.Pending != 1 || st.MedianLead != 8 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestLaneHealth_SilentSourceAlarmNotForNeverProducedOrDisabled(t *testing.T) {
	e := newEnv(t)
	e.enable(SourceX, "hiring")
	e.keys[KeyX] = "x-" + strings.Repeat("k", 12)
	e.enable(SourceHN, "q")
	e.disable(SourceHNHiring)
	e.web.body(hnHost, "/api/v1/search_by_date", "application/json", hnSearchJSON())
	// The X lane produced a week ago.
	e.exec(`INSERT INTO signal_lanes (source_id, last_produced_at, last_status, updated_at) VALUES (?, ?, 'ok', ?)`, SourceX, fmtTS(e.clk.now().Add(-7*24*time.Hour)), fmtTS(e.clk.now()))
	e.web.status(xHost, "/twitter", 402)
	sum := e.tick()
	if len(sum.Alarms) != 1 {
		t.Fatalf("alarms = %+v", sum.Alarms)
	}
	a := sum.Alarms[0]
	if a.Source != SourceX || a.Retryable || !strings.Contains(a.RequiredAction, "Top up") || a.LastProduced.IsZero() {
		t.Fatalf("alarm = %+v", a)
	}
	// Never produced (hn) and disabled (hn-hiring) raise nothing; the alarm
	// reaches the needs-you inbox.
	if len(e.note.alerts) != 1 || !strings.Contains(e.note.alerts[0].Title, SourceX) {
		t.Fatalf("needs-you items = %+v", e.note.alerts)
	}
	if !strings.Contains(e.note.sent(NoticeDigest)[0].Body, "signals-x") {
		t.Fatal("the digest lacks the alarm")
	}
	if act, retry := ClassifyStatus("signals-x", "authentication failed (key rejected)"); retry || act == "" {
		t.Fatalf("auth text: %q %v", act, retry)
	}
	if _, retry := ClassifyStatus("signals-x", "connection reset"); !retry {
		t.Fatal("a network failure reads as non-retryable")
	}
}
