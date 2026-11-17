package api

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/gmail/classify"
	"applysmith/internal/notify"
	"applysmith/internal/tracker"
)

func TestTelegram_Commands_StatusSummaryLiveValues(t *testing.T) {
	ctl := &fakeControl{status: browser.Status{State: browser.InstanceStopped}}
	h := newDomainHarness(t, func(c *Config) { c.BrowserControl = ctl })
	h.srv.setComponents()
	b := telegramBackend{s: h.srv}
	ctx := h.srv.bg
	d := h.domainOf()

	out, err := b.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// This harness explicitly simulates models offline, which health reports.
	for _, want := range []string{"Server: degraded", "- llm: degraded", "Runs: 0 working, 0 need you, 0 blocked", "Browser: stopped. Apply window: closed. Scan window: closed.",
		"Next schedules: none", "Gmail: sign-in not connected", "Model spend today: USD 0.00 of USD 2.00"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}

	// Each line follows the product: windows, counts, spend.
	ctl.mu.Lock()
	ctl.status = runningStatus(h.clock.now())
	ctl.mu.Unlock()
	if _, err := d.notify.board.Raise(ctx, notify.RaiseArgs{ID: "ent_s1", Kind: notify.KindApply, Origin: notify.OriginUser, Title: "Apply to Acme Labs",
		State: notify.NeedsYou, Reason: "sign-in"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.notify.board.Open(ctx, notify.OpenArgs{ID: "ent_s2", Kind: notify.KindScan, Origin: notify.OriginUser, Title: "Scan"}); err != nil {
		t.Fatal(err)
	}
	h.clock.advance(time.Minute) // the spend is read at most every 15 seconds
	execAll(t, h.srv.store.Load(), `INSERT INTO llm_ledger (call_id, started_at, task, provider_id, provider_kind, model, attempt, candidate_source, outcome, cost_usd, billing)
		VALUES ('spend', '`+h.clock.now().UTC().Format("2006-01-02T15:04:05.000Z")+`', 'narrative', 'p', 'openai-compatible', 'm', 1, 'primary', 'ok', 1.25, 'metered')`)
	h.clock.advance(time.Minute)
	out, err = b.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Runs: 1 working, 1 need you, 0 blocked", "Browser: running. Apply window: open, 1 tab. Scan window: closed.",
		"Model spend today: USD 1.25 of USD 2.00"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
}

func TestTelegram_Commands_TrackerCountsAndRecent(t *testing.T) {
	h := newDomainHarness(t)
	b := telegramBackend{s: h.srv}
	if out, err := b.Tracker(h.srv.bg); err != nil || !strings.Contains(out, "No applications yet.") {
		t.Fatalf("empty tracker: %q %v", out, err)
	}
	for i, status := range []string{"applied", "applied", "interview", "", "", ""} {
		row := h.addCandidacy(fmt.Sprintf("Company %d", i), "Engineer", fmt.Sprintf("https://jobs.example.com/c%d/1", i))
		if status != "" {
			if r := h.patchRow("/api/candidacies/"+idOf(row), map[string]any{"status": status}); r.StatusCode != 200 {
				t.Fatalf("status: %d %s", r.StatusCode, r.body)
			}
		}
		h.clock.advance(time.Minute)
	}
	out, err := b.Tracker(h.srv.bg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "applied 2") || !strings.Contains(out, "interview 1") {
		t.Errorf("counts by status are missing:\n%s", out)
	}
	if n := strings.Count(out, "\n- "); n != 5 {
		t.Errorf("%d recent rows, want the five most recently changed:\n%s", n, out)
	}
	if strings.Contains(out, "Company 0,") {
		t.Errorf("the oldest row is listed among the five newest:\n%s", out)
	}
	if i, j := strings.Index(out, "Company 5,"), strings.Index(out, "Company 4,"); i < 0 || j < 0 || i > j {
		t.Errorf("the newest row is not first:\n%s", out)
	}
}

func TestTelegram_Commands_PipelineInboxAndTopFive(t *testing.T) {
	h := newDomainHarness(t)
	b := telegramBackend{s: h.srv}
	now := h.clock.now().UTC().Format("2006-01-02T15:04:05.000Z")
	var stmts []string
	for i := 1; i <= 7; i++ {
		stmts = append(stmts, fmt.Sprintf(`INSERT INTO inbox (url, original_url, identity_key, company, role, source, score, status, first_seen_at, updated_at)
			VALUES ('https://jobs.example.com/p%[1]d', 'https://jobs.example.com/p%[1]d', 'k%[1]d', 'Company %[1]d', 'Engineer', 'registry', %[2]d, 'pending', '%[3]s', '%[3]s')`, i, i*10, now))
	}
	stmts = append(stmts, `INSERT INTO inbox (url, original_url, identity_key, company, role, source, score, status, first_seen_at, updated_at)
		VALUES ('https://jobs.example.com/q', 'https://jobs.example.com/q', 'kq', 'Queued Co', 'Engineer', 'registry', 99, 'queued', '`+now+`', '`+now+`')`)
	execAll(t, h.srv.store.Load(), stmts...)
	out, err := b.Pipeline(h.srv.bg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "7 postings waiting, 1 queued for evaluation") {
		t.Errorf("inbox size:\n%s", out)
	}
	want := []string{"Company 7", "Company 6", "Company 5", "Company 4", "Company 3"}
	last := -1
	for _, c := range want {
		at := strings.Index(out, c+",")
		if at <= last {
			t.Errorf("%s is missing or out of rank order:\n%s", c, out)
		}
		last = at
	}
	for _, c := range []string{"Company 2,", "Company 1,", "Queued Co"} {
		if strings.Contains(out, c) {
			t.Errorf("%s is listed among the five best waiting:\n%s", c, out)
		}
	}
}

func TestTelegram_Commands_QueueTodaysApplyList(t *testing.T) {
	h := newDomainHarness(t)
	b := telegramBackend{s: h.srv}
	if out, err := b.Queue(h.srv.bg); err != nil || !strings.Contains(out, "Nothing is on the list today.") {
		t.Fatalf("empty list: %q %v", out, err)
	}
	for i, c := range []struct {
		company string
		score   float64
	}{{"High Corp", 4.4}, {"Mid Corp", 3.7}} {
		score := c.score
		if _, err := h.upsertReport(h.srv.bg, tracker.Report{Number: int64(i + 1), Date: "2026-01-02", Outcome: tracker.OutcomeRecommended, Kind: tracker.ScoreFinal,
			Score: &score, Posting: postingOf(c.company)}); err != nil {
			t.Fatal(err)
		}
	}
	out, err := b.Queue(h.srv.bg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Apply list", "- High Corp, Engineer", "- Mid Corp, Engineer"} {
		if !strings.Contains(out, want) {
			t.Errorf("queue lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "apply-now: 1") || !strings.Contains(out, "selective: 1") {
		t.Errorf("the list is not grouped by lane with counts:\n%s", out)
	}
}

func TestTelegram_Commands_GmailLast24HoursAndHealth(t *testing.T) {
	h := newDomainHarness(t)
	b := telegramBackend{s: h.srv}
	now := h.clock.now()
	seedGmail(t, h,
		apiSignal{id: "recent", typ: classify.TypeInterview, employer: "Recent Co", at: now.Add(-2 * time.Hour)},
		apiSignal{id: "old", typ: classify.TypeReplyNeeded, employer: "Old Co", at: now.Add(-30 * time.Hour)})
	out, err := b.Gmail(h.srv.bg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Sign-in not connected") || !strings.Contains(out, "sync ") {
		t.Errorf("the sync health is missing:\n%s", out)
	}
	if !strings.Contains(out, "Last 24 hours:") || !strings.Contains(out, "Recent Co") || strings.Contains(out, "Old Co") {
		t.Errorf("signals of the last 24 hours only:\n%s", out)
	}
	h.clock.advance(23 * time.Hour) // the recent one is now 25 hours old
	if out, _ = b.Gmail(h.srv.bg); !strings.Contains(out, "No signals in the last 24 hours.") {
		t.Errorf("the window did not move:\n%s", out)
	}
}

func TestTelegram_Commands_OpenScreenRemoteOnOrOff(t *testing.T) {
	r := newRemoteRig(t)
	b := telegramBackend{s: r.srv}
	ctx := r.srv.bg
	off, err := b.Open(ctx, "tracker")
	if err != nil || !strings.Contains(off, "Open Tracker on your computer") || strings.Contains(off, "https://") {
		t.Fatalf("remote off: %q %v", off, err)
	}
	r.online()
	for screen, want := range map[string]string{"tracker": "https://" + tunnelHost + "/tracker", "Ask": "https://" + tunnelHost + "/ask",
		"schedules": "https://" + tunnelHost + "/discovery?tab=schedules"} {
		on, err := b.Open(ctx, screen)
		if err != nil || !strings.Contains(on, want) {
			t.Errorf("remote on, %s: %q %v", screen, on, err)
		}
	}
	unknown, _ := b.Open(ctx, "nowhere")
	if !strings.Contains(unknown, "do not know") || !strings.Contains(unknown, "tracker") || strings.Contains(unknown, "https://") {
		t.Errorf("unknown screen: %q", unknown)
	}
	r.disable()
	if again, _ := b.Open(ctx, "tracker"); !strings.Contains(again, "on your computer") {
		t.Errorf("remote off again: %q", again)
	}
}
