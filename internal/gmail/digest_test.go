package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/gmail/classify"
	"applysmith/internal/gmail/lifecycle"
)

func at(day, hh, mm int) time.Time { return time.Date(2027, 3, day, hh, mm, 0, 0, time.UTC) }

func TestDigest_SlotsAndSundayWeekly(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no zone database")
	}
	for _, c := range []struct {
		name string
		now  time.Time
		zone *time.Location
		want time.Time
		kind string
	}{
		{"before the morning slot, the evening of the day before", at(10, 3, 0), time.UTC, at(9, 18, 0), DigestDaily},
		{"morning", at(10, 8, 0), time.UTC, at(10, 8, 0), DigestDaily},
		{"afternoon", at(10, 17, 59), time.UTC, at(10, 8, 0), DigestDaily},
		{"evening", at(10, 18, 0), time.UTC, at(10, 18, 0), DigestDaily},
		{"Sunday morning is a plain digest", at(14, 8, 30), time.UTC, at(14, 8, 0), DigestDaily},
		{"Sunday evening is the weekly review", at(14, 18, 30), time.UTC, at(14, 18, 0), DigestWeekly},
		{"Monday morning after the review", at(15, 7, 0), time.UTC, at(14, 18, 0), DigestWeekly},
		{"local time, not UTC", at(10, 13, 30), ny, time.Date(2027, 3, 10, 8, 0, 0, 0, ny).UTC(), DigestDaily},
	} {
		got := SlotAt(c.now, c.zone)
		if !got.At.Equal(c.want) || got.Kind != c.kind {
			t.Errorf("%s: slot %v %s, want %v %s", c.name, got.At, got.Kind, c.want, c.kind)
		}
	}
}

// digestModel answers the gmail-digest task with JSON built from the prompt's
// thread numbers.
func digestModel(lines map[string]string, headline string) func(task, system, prompt string, n int) (Completion, error) {
	return func(task, system, prompt string, n int) (Completion, error) {
		if task != TaskDigest || strings.Contains(system, "draft") {
			return Completion{}, errors.New("no route")
		}
		raw, _ := json.Marshal(map[string]any{"headline": headline, "lines": lines})
		return Completion{Text: string(raw), Finish: "stop"}, nil
	}
}

func count(t *testing.T, p *pipeEnv, q string) int {
	t.Helper()
	var n int
	if err := p.st.Read().QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDigest_SlotOnceAndCatchUp(t *testing.T) {
	p := newPipeEnv(t)
	p.clk.set(at(10, 8, 0))
	p.seed(sig{id: "a", typ: classify.TypeReplyNeeded, at: at(10, 7, 0)})
	for range 3 {
		if err := p.stack.Digests.Tick(p.ctx, p.clk.now(), time.UTC); err != nil {
			t.Fatal(err)
		}
		p.clk.advance(time.Minute)
	}
	if n := count(t, p, `SELECT count(*) FROM gmail_digests`); n != 1 {
		t.Fatalf("%d digest records for one slot", n)
	}
	// Woken at 11:00 after sleeping through the 18:00 of the day before: the
	// 08:00 slot of today is within six hours, the 18:00 one is not.
	q := newPipeEnv(t)
	q.clk.set(at(10, 11, 0))
	if err := q.stack.Digests.Tick(q.ctx, q.clk.now(), time.UTC); err != nil {
		t.Fatal(err)
	}
	if n := count(t, q, `SELECT count(*) FROM gmail_digests WHERE slot = '2027-03-10T08:00:00.000Z'`); n != 1 {
		t.Fatalf("the 08:00 slot did not run at 11:00")
	}
	r := newPipeEnv(t)
	r.clk.set(at(10, 15, 0))
	if err := r.stack.Digests.Tick(r.ctx, r.clk.now(), time.UTC); err != nil {
		t.Fatal(err)
	}
	if n := count(t, r, `SELECT count(*) FROM gmail_digests`); n != 0 {
		t.Fatalf("a slot seven hours old ran at 15:00")
	}
}

func TestDigest_ScheduledSwitchAuthorizationAndFailedSlotNotRetried(t *testing.T) {
	p := newPipeEnv(t)
	p.clk.set(at(10, 8, 5))
	off := false
	_ = p.repo.SetSwitches(p.ctx, nil, &off)
	_ = p.stack.Digests.Tick(p.ctx, p.clk.now(), time.UTC)
	if count(t, p, `SELECT count(*) FROM gmail_digests`) != 0 {
		t.Fatal("a switched-off schedule produced a digest")
	}
	// The switch gates scheduled digests only: a manual one still runs.
	if d, err := p.stack.Digests.Run(p.ctx); err != nil || d.Trigger != TriggerHand {
		t.Fatalf("manual = %+v %v", d, err)
	}
	on := true
	_ = p.repo.SetSwitches(p.ctx, nil, &on)
	// A slot whose run failed is not retried on the next tick.
	p.exec(`DROP TABLE gmail_link_rejections`)
	if err := p.stack.Digests.Tick(p.ctx, p.clk.now(), time.UTC); err == nil {
		t.Fatal("a failing slot reported success")
	}
	p.clk.advance(time.Minute)
	if err := p.stack.Digests.Tick(p.ctx, p.clk.now(), time.UTC); err != nil {
		t.Fatalf("the failed slot was retried: %v", err)
	}
	var status string
	_ = p.st.Read().QueryRow(`SELECT status FROM gmail_digests WHERE slot IS NOT NULL`).Scan(&status)
	if status != "failed" {
		t.Fatalf("slot status = %q", status)
	}
	// Without an app password the timer does nothing.
	q := newPipeEnv(t)
	q.clk.set(at(10, 8, 5))
	if err := q.vault.Delete(secretAppPassword); err != nil {
		t.Fatal(err)
	}
	_ = q.stack.Digests.Tick(q.ctx, q.clk.now(), time.UTC)
	if count(t, q, `SELECT count(*) FROM gmail_digests`) != 0 {
		t.Fatal("a digest ran without an app password")
	}
}

func TestDigest_WindowStartRules(t *testing.T) {
	p := newPipeEnv(t)
	p.clk.set(at(10, 8, 0))
	first, err := p.stack.Digests.Run(p.ctx)
	if err != nil || !first.WindowStart.Equal(at(9, 8, 0)) {
		t.Fatalf("first window = %v %v", first.WindowStart, err)
	}
	p.clk.set(at(10, 18, 0))
	second, _ := p.stack.Digests.Run(p.ctx)
	if !second.WindowStart.Equal(at(10, 8, 0)) {
		t.Fatalf("second starts at the previous digest: %v", second.WindowStart)
	}
	// An agent-made digest and a weekly review do not start the next window.
	p.clk.set(at(10, 19, 0))
	if _, err := p.stack.Digests.Generate(p.ctx); err != nil {
		t.Fatal(err)
	}
	p.clk.set(at(10, 19, 30))
	if _, err := p.stack.Digests.produce(p.ctx, DigestWeekly, TriggerSlot, at(14, 18, 0)); err != nil {
		t.Fatal(err)
	}
	p.clk.set(at(10, 20, 0))
	third, _ := p.stack.Digests.Run(p.ctx)
	if !third.WindowStart.Equal(at(10, 18, 0)) {
		t.Fatalf("third window starts %v, want the second digest's end", third.WindowStart)
	}
	// Never more than 36 hours back.
	p.clk.set(at(14, 8, 0))
	fourth, _ := p.stack.Digests.Run(p.ctx)
	if !fourth.WindowStart.Equal(at(14, 8, 0).Add(-36 * time.Hour)) {
		t.Fatalf("fourth window starts %v", fourth.WindowStart)
	}
}

func TestDigest_CollectRankingAndCap(t *testing.T) {
	p := newPipeEnv(t)
	p.clk.set(at(10, 8, 0))
	var batch []sig
	for i := range 45 {
		s := sig{id: fmt.Sprintf("m%02d", i), typ: classify.TypeReplyNeeded, at: at(10, 7, 0).Add(-time.Duration(i) * time.Minute), employer: fmt.Sprintf("Company %02d", i)}
		batch = append(batch, s)
	}
	batch = append(batch,
		sig{id: "rc1", typ: classify.TypeApplied, at: at(10, 6, 0)}, sig{id: "rc2", typ: classify.TypeApplied, at: at(10, 6, 0)},
		sig{id: "iv", typ: classify.TypeInterview, at: at(10, 5, 0), due: at(12, 15, 0), employer: "Prep Co"},
		sig{id: "out", typ: classify.TypeRejection, at: at(10, 4, 0), employer: "Closed Co"},
		sig{id: "old", typ: classify.TypeReplyNeeded, at: at(8, 4, 0), employer: "Older Co"})
	p.seed(batch...)
	d, err := p.stack.Digests.Run(p.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Body.Items) != 30 || d.Body.Omitted != 17 || d.Body.Receipts != 2 {
		t.Fatalf("items %d omitted %d receipts %d", len(d.Body.Items), d.Body.Omitted, d.Body.Receipts)
	}
	for _, it := range d.Body.Items {
		if it.Company == "Older Co" {
			t.Error("a thread outside the window was listed")
		}
	}
	if d.Body.Items[0].Lane != lifecycle.LaneDoNow || d.Body.Items[0].Company != "Company 00" {
		t.Errorf("first item = %+v", d.Body.Items[0])
	}
	if !strings.Contains(d.Body.Text, "17 more threads were omitted") || !strings.Contains(d.Body.Text, "2 application confirmations counted, not listed") {
		t.Errorf("text tail:\n%s", d.Body.Text)
	}
	// Ordering by lane: prepare comes after do_now, and closed last.
	q := newPipeEnv(t)
	q.clk.set(at(10, 8, 0))
	q.seed(sig{id: "cl", typ: classify.TypeRejection, at: at(10, 7, 0), employer: "Closed Co"},
		sig{id: "pr", typ: classify.TypeInterview, at: at(10, 6, 0), due: at(12, 15, 0), employer: "Prep Co"},
		sig{id: "dn", typ: classify.TypeAssessment, at: at(10, 5, 0), employer: "Todo Co"})
	e, _ := q.stack.Digests.Run(q.ctx)
	var lanes []string
	for _, it := range e.Body.Items {
		lanes = append(lanes, it.Lane)
	}
	if strings.Join(lanes, ",") != "do_now,prepare,closed" {
		t.Errorf("lanes = %v", lanes)
	}
}

func TestDigest_ComposeFallbacks(t *testing.T) {
	seed := func(p *pipeEnv) {
		p.clk.set(at(10, 8, 0))
		p.seed(sig{id: "a", typ: classify.TypeReplyNeeded, at: at(10, 7, 0), employer: "Alpha Co"}, sig{id: "b", typ: classify.TypeAssessment, at: at(10, 6, 0), employer: "Beta Co"})
	}
	// A good reply is used; an invented thread number is dropped; a skipped one keeps its deterministic line.
	p := newPipeEnv(t)
	seed(p)
	p.model.fn = digestModel(map[string]string{"1": "Alpha Co wrote and wants a reply.", "7": "Ghost thread.", "": "x"}, "Two threads need you today.")
	d, _ := p.stack.Digests.Run(p.ctx)
	if d.Method != MethodModel || d.Headline != "Two threads need you today." || d.Body.Items[0].Line != "Alpha Co wrote and wants a reply." || !strings.HasPrefix(d.Body.Items[1].Line, "Beta Co: ") {
		t.Fatalf("model digest = %+v", d)
	}
	if strings.Contains(d.Body.Text, "Ghost") {
		t.Error("an invented thread key was kept")
	}
	// Each failure keeps the whole text deterministic.
	for name, fn := range map[string]func(string, string, string, int) (Completion, error){
		"model error": func(string, string, string, int) (Completion, error) { return Completion{}, errors.New("down") },
		"not json": func(string, string, string, int) (Completion, error) {
			return Completion{Text: "here is your digest", Finish: "stop"}, nil
		},
		"invented number": digestModel(map[string]string{"1": "Alpha Co wants a reply within 9 days."}, "Two threads."),
		"truncated": func(string, string, string, int) (Completion, error) {
			return Completion{Text: `{"headline":"x"`, Finish: "length"}, nil
		},
		"link": digestModel(map[string]string{"1": "Open https://example.org/x now."}, "Two threads."),
	} {
		q := newPipeEnv(t)
		seed(q)
		q.model.fn = fn
		d, err := q.stack.Digests.Run(q.ctx)
		if err != nil || d.Method != MethodDeterministic || !strings.Contains(d.Headline, "2 threads") || !strings.HasPrefix(d.Body.Items[0].Line, "Alpha Co: ") {
			t.Errorf("%s: %+v %v", name, d, err)
		}
	}
	// An empty window never calls the model.
	r := newPipeEnv(t)
	r.clk.set(at(10, 8, 0))
	r.model.fn = digestModel(nil, "x")
	if _, err := r.stack.Digests.Run(r.ctx); err != nil || r.model.count(TaskDigest) != 0 {
		t.Fatalf("empty window called the model %d times (%v)", r.model.count(TaskDigest), err)
	}
}

func TestDigest_EmptyNotPushedAndStoredForTheDashboard(t *testing.T) {
	p := newPipeEnv(t)
	p.clk.set(at(10, 8, 0))
	var sent []string
	p.stack.Digests.Channels = Channels{
		Telegram: func(context.Context, Digest) error { sent = append(sent, "telegram"); return nil },
		Desktop: func(context.Context, Digest) error {
			sent = append(sent, "desktop")
			return nil
		},
		Agent: func(context.Context, Digest) (string, error) { sent = append(sent, "agent"); return "s", nil },
	}
	d, err := p.stack.Digests.Run(p.ctx)
	if err != nil || len(sent) != 0 {
		t.Fatalf("an empty digest was pushed: %v %v", sent, err)
	}
	if got, err := p.repo.Digest(p.ctx, d.ID); err != nil || got.Status != "done" || got.Body.Text == "" {
		t.Fatalf("stored = %+v %v", got, err)
	}
	// An all-zero weekly review is not pushed either.
	if _, err := p.stack.Digests.produce(p.ctx, DigestWeekly, TriggerSlot, at(14, 18, 0)); err != nil || len(sent) != 0 {
		t.Fatalf("an all-zero review was pushed: %v %v", sent, err)
	}
	// A digest with one receipt is pushed.
	p.clk.set(at(10, 9, 0))
	p.seed(sig{id: "rc", typ: classify.TypeApplied, at: at(10, 8, 30)})
	if _, err := p.stack.Digests.Run(p.ctx); err != nil || len(sent) != 3 {
		t.Fatalf("a receipt-only digest: %v %v", sent, err)
	}
}

func TestDigest_ChannelsIsolatedStoredBeforeDeliveryAndBusy(t *testing.T) {
	p := newPipeEnv(t)
	p.clk.set(at(10, 8, 0))
	p.seed(sig{id: "a", typ: classify.TypeReplyNeeded, at: at(10, 7, 0)})
	var busy error
	var rowStatus string
	p.stack.Digests.Channels = Channels{
		Telegram: func(context.Context, Digest) error { return errors.New("429 too many requests") },
		Desktop: func(ctx context.Context, _ Digest) error {
			_ = p.st.Read().QueryRow(`SELECT status FROM gmail_digests ORDER BY id DESC LIMIT 1`).Scan(&rowStatus)
			_, busy = p.stack.Digests.Run(ctx)
			return nil
		},
		Agent: func(context.Context, Digest) (string, error) { panic("agent channel blew up") },
	}
	d, err := p.stack.Digests.Run(p.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(d.Delivery["telegram"], "failed: ") || d.Delivery["desktop"] != "sent" || !strings.HasPrefix(d.Delivery["agent"], "failed: ") {
		t.Fatalf("delivery = %v", d.Delivery)
	}
	if rowStatus != "done" {
		t.Errorf("the digest was not stored before delivery: %q", rowStatus)
	}
	if !errors.Is(busy, ErrBusy) {
		t.Errorf("a request during production = %v, want busy", busy)
	}
	got, _ := p.repo.Digest(p.ctx, d.ID)
	if got.Delivery["telegram"] != d.Delivery["telegram"] || got.Delivery["desktop"] != "sent" {
		t.Errorf("stored delivery = %v", got.Delivery)
	}
}

func TestDigest_ManualDeliveredLikeScheduledAgentDeliversNowhere(t *testing.T) {
	p := newPipeEnv(t)
	p.clk.set(at(10, 8, 0))
	p.seed(sig{id: "a", typ: classify.TypeReplyNeeded, at: at(10, 7, 0)})
	var n int
	p.stack.Digests.Channels.Desktop = func(context.Context, Digest) error { n++; return nil }
	if _, err := p.stack.Digests.Run(p.ctx); err != nil || n != 1 {
		t.Fatalf("manual run delivered %d times, %v", n, err)
	}
	d, err := p.stack.Digests.Generate(p.ctx)
	if err != nil || n != 1 || len(d.Delivery) != 0 || d.StartsWindow {
		t.Fatalf("agent digest: %+v delivered=%d %v", d, n, err)
	}
}

func TestDigest_WeeklyFunnelAndNarrative(t *testing.T) {
	p := newPipeEnv(t)
	p.clk.set(at(14, 18, 0))
	p.seed(
		sig{id: "a1", thread: "ta", typ: classify.TypeApplied, at: at(9, 9, 0)},
		sig{id: "a2", thread: "ta", typ: classify.TypeInterview, at: at(11, 9, 0)},
		sig{id: "b1", thread: "tb", typ: classify.TypeApplied, at: at(10, 9, 0)},
		sig{id: "b2", thread: "tb", typ: classify.TypeApplied, at: at(11, 9, 0)}, // same thread, same state: counted once
		sig{id: "c1", thread: "tc", typ: classify.TypeRejection, at: at(12, 9, 0)},
		sig{id: "d1", thread: "td", typ: classify.TypeReplyNeeded, at: at(1, 9, 0)}, // waiting on you, outside the 7 days
		sig{id: "e1", thread: "te", typ: classify.TypeInterview, at: at(5, 9, 0)},   // waiting on the company over 7 days
	)
	p.model.fn = func(task, system, prompt string, n int) (Completion, error) {
		return Completion{Text: "A busy week with two applications, one interview and one rejection in the last 7 days.", Finish: "stop"}, nil
	}
	d, err := p.stack.Digests.produce(p.ctx, DigestWeekly, TriggerSlot, at(14, 18, 0))
	if err != nil {
		t.Fatal(err)
	}
	f := d.Body.Funnel
	if f["applied"] != 2 || f["interview"] != 1 || f["rejected"] != 1 || f["responded"] != 0 {
		t.Fatalf("funnel = %v", f)
	}
	if d.Method != MethodModel || !strings.Contains(d.Body.Narrative, "busy week") {
		t.Errorf("narrative = %q (%s)", d.Body.Narrative, d.Method)
	}
	var onYou, follow []string
	for _, it := range d.Body.Items {
		switch it.Section {
		case SectionWaitingOnYou:
			onYou = append(onYou, it.Key)
		case SectionFollowUp:
			follow = append(follow, it.Key)
		}
	}
	if len(onYou) != 1 || onYou[0] != "td" || len(follow) != 1 || follow[0] != "te" {
		t.Errorf("lists on-you %v follow-up %v", onYou, follow)
	}
	if fl := sectionItems(d.Body.Items, SectionFollowUp); len(fl) != 1 || !strings.HasSuffix(fl[0].Line, "(last heard "+at(5, 9, 0).Local().Format("Mon Jan 2")+")") {
		t.Errorf("follow-up items %+v: the line says when the company last wrote", fl)
	}
	// A narrative with an invented number falls back.
	q := newPipeEnv(t)
	q.clk.set(at(14, 18, 0))
	q.seed(sig{id: "a1", thread: "ta", typ: classify.TypeApplied, at: at(9, 9, 0)})
	q.model.fn = modelReturning(Completion{Text: "A quiet week with 40 new applications and nothing else to report.", Finish: "stop"})
	e, _ := q.stack.Digests.produce(q.ctx, DigestWeekly, TriggerSlot, at(14, 18, 0))
	if e.Method != MethodDeterministic || !strings.HasPrefix(e.Body.Narrative, "In the last 7 days:") {
		t.Errorf("fallback narrative = %q", e.Body.Narrative)
	}
	// Each list is capped at ten with the rest counted.
	r := newPipeEnv(t)
	r.clk.set(at(14, 18, 0))
	var many []sig
	for i := range 14 {
		many = append(many, sig{id: fmt.Sprintf("w%02d", i), typ: classify.TypeAssessment, at: at(13, 9, 0), employer: fmt.Sprintf("Co %02d", i)})
	}
	r.seed(many...)
	w, _ := r.stack.Digests.produce(r.ctx, DigestWeekly, TriggerSlot, at(14, 18, 0))
	if len(sectionItems(w.Body.Items, SectionWaitingOnYou)) != 10 || w.Body.MoreWaiting != 4 || !strings.Contains(w.Body.Text, "and 4 more") || !strings.Contains(w.Body.Text, "Waiting on you (14)") {
		t.Errorf("cap: %d listed, more %d\n%s", len(sectionItems(w.Body.Items, SectionWaitingOnYou)), w.Body.MoreWaiting, w.Body.Text)
	}
}

// An interview thread is listed neither as waiting nor as a follow-up to send
// once a later thread of the same candidacy holds a rejection; one of another
// candidacy, an earlier one, or one with no candidacy leaves it listed.
func TestDigest_ARejectionInAnotherThreadOfTheCandidacyClosesTheFollowUp(t *testing.T) {
	now := at(20, 18, 0)
	th := func(id string, cand int64, lane, owner, state string, last time.Time) Thread {
		return Thread{ID: id, Employer: "Example Corp", CandidacyID: cand, LastIncoming: last, LastUpdate: last,
			View: lifecycle.View{Lane: lane, Owner: owner, State: state, NextAction: "Waiting to hear back after the interview"}}
	}
	waiting := func(id string, cand int64, last time.Time) Thread {
		return th(id, cand, lifecycle.LaneWaiting, lifecycle.OwnerCompany, lifecycle.StateInterview, last)
	}
	rejected := func(id string, cand int64, last time.Time) Thread {
		return th(id, cand, lifecycle.LaneClosed, lifecycle.OwnerNone, lifecycle.StateRejected, last)
	}
	threads := []Thread{
		waiting("closed", 8, at(3, 9, 0)), rejected("r8", 8, at(5, 9, 0)),
		waiting("other", 9, at(3, 9, 0)),
		waiting("before", 10, at(6, 9, 0)), rejected("r10", 10, at(4, 9, 0)),
		waiting("unlinked", 0, at(3, 9, 0)), rejected("r0", 0, at(5, 9, 0)),
		th("nudge", 8, lifecycle.LaneDoNow, lifecycle.OwnerUser, lifecycle.StateInterview, at(2, 9, 0)),
	}
	you, follow, _, _ := weeklyLists(threads, now)
	var listed []string
	for _, it := range append(you, follow...) {
		listed = append(listed, it.Key)
	}
	slices.Sort(listed)
	if !slices.Equal(listed, []string{"before", "other", "unlinked"}) {
		t.Errorf("listed %v", listed)
	}
}

// A thread waiting on the company keeps no due time already past (the weekly
// review listed "Waiting to hear back after the interview" with the due time of
// an interview months over); one still ahead, or one on the user, keeps it.
func TestDigest_APastDueOnACompanyThreadIsNoDeadline(t *testing.T) {
	now := at(14, 18, 0)
	th := func(owner string, due time.Time) Thread {
		return Thread{ID: "t", Employer: "Example", View: lifecycle.View{Lane: lifecycle.LaneWaiting, Owner: owner, NextAction: "Waiting to hear back after the interview", Due: due}}
	}
	if it := itemOf(th(lifecycle.OwnerCompany, at(6, 15, 0)), lifecycle.LaneWaiting, now); it.Due != "" || strings.Contains(it.Line, "due") {
		t.Errorf("past due on the company's thread: %+v", it)
	}
	if it := itemOf(th(lifecycle.OwnerCompany, at(16, 15, 0)), lifecycle.LaneWaiting, now); it.Due == "" {
		t.Errorf("a due still ahead is kept: %+v", it)
	}
	if it := itemOf(th(lifecycle.OwnerUser, at(6, 15, 0)), lifecycle.LaneDoNow, now); it.Due == "" {
		t.Errorf("an overdue item on the user is kept: %+v", it)
	}
}

// westOfUTC is a zone eight hours behind UTC, where a date's UTC midnight falls
// on the afternoon of the day before.
var westOfUTC = time.FixedZone("WEST", -8*60*60)

// A digest line names a date alone as that date, not as the local time of its
// UTC midnight, and an instant with its local time.
func TestDigest_ADateIsWordedAsTheDate(t *testing.T) {
	it := DigestItem{Company: "Example", NextAction: "Complete the assessment", Due: friday.Format(time.RFC3339)}
	if got := deterministicLine(it, westOfUTC); got != "Example: Complete the assessment (due Fri Mar 12)" {
		t.Errorf("a date: %q", got)
	}
	it.Due = at(12, 15, 30).Format(time.RFC3339)
	if got := deterministicLine(it, westOfUTC); got != "Example: Complete the assessment (due Fri Mar 12 07:30 WEST)" {
		t.Errorf("an instant: %q", got)
	}
}

func TestDigest_TelegramRatingKeyboardAndPages(t *testing.T) {
	p := newPipeEnv(t)
	p.clk.set(at(10, 8, 0))
	var batch []sig
	for i := range 11 {
		batch = append(batch, sig{id: fmt.Sprintf("m%02d", i), typ: classify.TypeReplyNeeded, at: at(10, 7, 0).Add(-time.Duration(i) * time.Minute), employer: fmt.Sprintf("Company %02d", i)})
	}
	p.seed(batch...)
	d, err := p.stack.Digests.Run(p.ctx)
	if err != nil {
		t.Fatal(err)
	}
	pages, rows, err := p.stack.Digests.TelegramView(p.ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 8 items at two per row", len(rows))
	}
	for _, r := range rows {
		if len(r) != 4 {
			t.Errorf("a row of %d buttons", len(r))
		}
		for _, b := range r {
			if len(b.Data) > 64 || !strings.HasPrefix(b.Data, "rate:") {
				t.Errorf("button data %q", b.Data)
			}
		}
	}
	if rows[0][0].Data != RatingData(d.ID, 1, "up") || rows[3][3].Data != RatingData(d.ID, 8, "down") {
		t.Errorf("first %q last %q", rows[0][0].Data, rows[3][3].Data)
	}
	if len(pages) != 1 {
		t.Errorf("pages = %d", len(pages))
	}
	// Tapping thumbs-up rates the item; a second tap clears it; the verdict in force is marked.
	ref := fmt.Sprintf("%d.%d", d.ID, 2)
	if err := p.stack.Digests.RateItem(p.ctx, ref, VerdictUp); err != nil {
		t.Fatal(err)
	}
	v, _ := p.repo.ThreadVerdicts(p.ctx)
	if len(v) != 1 {
		t.Fatalf("verdicts = %v", v)
	}
	marked := ratingRows(d, v)
	if marked[0][2].Text != "[2 +]" || marked[0][3].Text != "2 -" {
		t.Errorf("marked = %v", marked[0])
	}
	if err := p.stack.Digests.RateItem(p.ctx, ref, VerdictUp); err != nil {
		t.Fatal(err)
	}
	if v, _ = p.repo.ThreadVerdicts(p.ctx); len(v) != 0 {
		t.Errorf("a second tap did not clear: %v", v)
	}
	// Stale or malformed buttons record nothing.
	before := count(t, p, `SELECT count(*) FROM gmail_digest_feedback`)
	for _, bad := range []string{"", "x", "99.1", fmt.Sprintf("%d.9", d.ID), fmt.Sprintf("%d.0", d.ID), fmt.Sprintf("%d.20", d.ID), "1.2.3"} {
		if err := p.stack.Digests.RateItem(p.ctx, bad, VerdictUp); !errors.Is(err, ErrStaleRating) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if err := p.stack.Digests.RateItem(p.ctx, ref, "sideways"); !errors.Is(err, ErrStaleRating) {
		t.Errorf("verdict sideways: %v", err)
	}
	if count(t, p, `SELECT count(*) FROM gmail_digest_feedback`) != before {
		t.Error("a bad button recorded a rating")
	}
	// Long text splits on line boundaries under 4,000 characters.
	long := strings.Repeat("a line of the digest text that is fairly long\n", 400)
	for _, pg := range SplitPages(long, TelegramPageChars) {
		if len(pg) >= 4000 || strings.HasPrefix(pg, "\n") {
			t.Fatalf("page of %d characters", len(pg))
		}
	}
	if got := strings.Join(SplitPages(long, TelegramPageChars), "\n"); got != strings.TrimRight(long, "\n") {
		t.Error("splitting lost text")
	}
}

func TestDigest_FeedbackMuteUntilStateChanges(t *testing.T) {
	p := newPipeEnv(t)
	p.clk.set(at(10, 8, 0))
	p.seed(sig{id: "a", thread: "t1", typ: classify.TypeReplyNeeded, at: at(10, 7, 0), employer: "Alpha Co"},
		sig{id: "b", thread: "t2", typ: classify.TypeAssessment, at: at(10, 6, 0), employer: "Beta Co"})
	d, _ := p.stack.Digests.Run(p.ctx)
	if err := p.repo.Rate(p.ctx, d.ID, "t1", VerdictDown); err != nil {
		t.Fatal(err)
	}
	if err := p.repo.Rate(p.ctx, d.ID, "missing", VerdictUp); !errors.Is(err, ErrNotInDigest) {
		t.Errorf("a thread the digest does not list: %v", err)
	}
	if err := p.repo.Rate(p.ctx, 999, "t1", VerdictUp); !errors.Is(err, ErrNoDigest) {
		t.Errorf("an unknown digest: %v", err)
	}
	// Next digest: more mail in t1 with the same lane and state keeps it muted.
	p.clk.set(at(10, 18, 0))
	p.seed(sig{id: "c", thread: "t1", typ: classify.TypeReplyNeeded, at: at(10, 17, 0), employer: "Alpha Co"})
	e, _ := p.stack.Digests.Run(p.ctx)
	if len(e.Body.Items) != 0 || len(e.Body.Muted) != 1 || e.Body.Muted[0].Key != "t1" || !strings.Contains(e.Body.Text, "1 thread muted by your ratings") {
		t.Fatalf("muted digest = %+v\n%s", e.Body, e.Body.Text)
	}
	// "Show again": a verdict can be given on a muted thread.
	if err := p.repo.Rate(p.ctx, e.ID, "t1", VerdictClear); err != nil {
		t.Fatal(err)
	}
	// Cleared, it is listed again; rated down again and then a new event changes its state.
	p.clk.set(at(10, 19, 0))
	p.seed(sig{id: "d", thread: "t1", typ: classify.TypeReplyNeeded, at: at(10, 18, 30), employer: "Alpha Co"})
	f, _ := p.stack.Digests.Run(p.ctx)
	if len(f.Body.Items) != 1 {
		t.Fatalf("a cleared verdict still mutes: %+v", f.Body)
	}
	if err := p.repo.Rate(p.ctx, f.ID, "t1", VerdictDown); err != nil {
		t.Fatal(err)
	}
	p.clk.set(at(10, 20, 0))
	p.seed(sig{id: "e", thread: "t1", typ: classify.TypeInterview, at: at(10, 19, 30), due: at(12, 15, 0), employer: "Alpha Co"})
	g, _ := p.stack.Digests.Run(p.ctx)
	if len(g.Body.Items) != 1 || g.Body.Items[0].Lane != lifecycle.LanePrepare || len(g.Body.Muted) != 0 {
		t.Fatalf("an interview invitation did not bring the thread back: %+v", g.Body)
	}
	// The 12 most recent ratings reach the digest prompt.
	if rated, _ := p.repo.RecentRatings(p.ctx, RatingExamples); len(rated) == 0 || rated[0].Verdict != VerdictDown || rated[0].Company != "Alpha Co" {
		t.Errorf("ratings = %+v", rated)
	}
}

func TestDigest_DraftCapAndBudget(t *testing.T) {
	p := newPipeEnv(t)
	p.clk.set(at(10, 8, 0))
	var batch []sig
	for i := range 5 {
		batch = append(batch, sig{id: fmt.Sprintf("r%d", i), typ: classify.TypeReplyNeeded, at: at(10, 7, 0).Add(-time.Duration(i) * time.Minute), employer: fmt.Sprintf("Co %d", i)})
	}
	p.seed(batch...)
	var draftCalls int
	var mu sync.Mutex
	p.model.fn = func(task, system, prompt string, n int) (Completion, error) {
		if strings.Contains(system, "draft") {
			mu.Lock()
			draftCalls++
			mu.Unlock()
			return Completion{Text: "Thank you for the note, I will send my availability by tomorrow morning.", Finish: "stop"}, nil
		}
		return Completion{}, errors.New("no route")
	}
	d, err := p.stack.Digests.Run(p.ctx)
	if err != nil {
		t.Fatal(err)
	}
	withDraft := 0
	for i, it := range d.Body.Items {
		if it.Draft != "" {
			withDraft++
			if i >= 3 {
				t.Errorf("item %d has a draft; only the first three get one", i+1)
			}
		}
	}
	if withDraft != 3 || draftCalls != 3 || !strings.Contains(d.Body.Text, "Nothing was sent") {
		t.Fatalf("drafts %d calls %d", withDraft, draftCalls)
	}
	// An unchanged thread reuses its draft: no model call.
	p.clk.set(at(10, 9, 0))
	if _, err := p.stack.Digests.Run(p.ctx); err != nil {
		t.Fatal(err)
	}
	if draftCalls != 3 {
		t.Errorf("a cached draft was regenerated: %d calls", draftCalls)
	}
}

func TestDigest_DraftMissingTheBudgetIsAbsentAndNeverBlocks(t *testing.T) {
	old := draftBudget
	draftBudget = 100 * time.Millisecond
	t.Cleanup(func() { draftBudget = old })
	p := newPipeEnv(t)
	p.clk.set(at(10, 8, 0))
	p.seed(sig{id: "a", typ: classify.TypeReplyNeeded, at: at(10, 7, 0)})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	p.stack.Model = hangingModel{release: release}
	start := time.Now()
	d, err := p.stack.Digests.Run(p.ctx)
	if err != nil || time.Since(start) > 5*time.Second {
		t.Fatalf("a hung drafter blocked the digest: %v after %v", err, time.Since(start))
	}
	if len(d.Body.Items) != 1 || d.Body.Items[0].Draft != "" {
		t.Fatalf("items = %+v", d.Body.Items)
	}
}

type hangingModel struct{ release chan struct{} }

func (h hangingModel) Complete(ctx context.Context, _, _, _ string) (Completion, error) {
	select {
	case <-ctx.Done():
	case <-h.release:
	}
	return Completion{}, ctx.Err()
}

func TestAlerts_AlertedThreadStillInNextDigest(t *testing.T) {
	p := newPipeEnv(t)
	p.clk.set(at(10, 8, 0))
	_, _ = p.stack.EvaluateAlerts(p.ctx)
	p.seed(sig{id: "a", thread: "t1", typ: classify.TypeAssessment, at: at(10, 7, 0)})
	_, _ = p.stack.EvaluateAlerts(p.ctx)
	if len(p.alerts) != 1 {
		t.Fatalf("alerts = %d", len(p.alerts))
	}
	d, _ := p.stack.Digests.Run(p.ctx)
	if len(d.Body.Items) != 1 || d.Body.Items[0].Key != "t1" {
		t.Fatalf("the alerted thread is not in the digest: %+v", d.Body.Items)
	}
}
