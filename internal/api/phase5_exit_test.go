package api

import (
	"strings"
	"testing"
	"time"

	"applysmith/internal/api/wire"
	"applysmith/internal/discovery"
	"applysmith/internal/gmail"
)

// draftTurn is a pi process that answers a scheduled trigger turn with a reply
// to the recruiter and calls no tool.
func draftTurn(p *scriptedPi) {
	piStart(p)
	const answer = "Hi Sam, thanks for reaching out. I can talk Tuesday or Thursday afternoon this week. Best, Alex"
	p.emit(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": answer}})
	p.emit(assistantLine([]map[string]any{{"type": "text", "text": answer}}, "stop"))
	p.emit(map[string]any{"type": "agent_settled"})
}

// syncGmail starts a manual Gmail sync and waits for the wanted number of runs
// to have ended.
func (h *harness) syncGmail(t *testing.T, runs int) {
	t.Helper()
	if r := h.post("/api/gmail/sync", nil); r.StatusCode != 202 {
		t.Fatalf("sync: %d %s", r.StatusCode, r.body)
	}
	waitFor(t, "the Gmail sync to end", func() bool {
		list, _ := h.get("/api/gmail/runs").data()["runs"].([]any)
		return len(list) == runs && list[0].(map[string]any)["status"] != "running"
	})
}

// nextAnnouncement reads the desktop stream until an announcement that match
// accepts arrives.
func nextAnnouncement(t *testing.T, s *streamClient, what string, match func(wire.NotificationAnnounce) bool) wire.NotificationAnnounce {
	t.Helper()
	var seen []string
	for range 20 {
		m := s.next()
		if m.name != "announce" {
			continue
		}
		a := decodeEvent[wire.NotificationAnnounce](t, m)
		if match(a) {
			return a
		}
		seen = append(seen, a.Title+" "+a.Link)
	}
	t.Fatalf("no desktop announcement for %s; saw %q", what, seen)
	return wire.NotificationAnnounce{}
}

// TestPhase5Exit_ScheduledScanDigestDesktopAndRecruiterTriggerEndInADraft is
// the evidence for the Phase 5 exit check (task 5.7). One server over a
// temporary workspace, with every outside service a local fake: a Greenhouse
// board, Gmail's IMAP server, the Telegram Bot API and the pi process. It walks the
// whole day: a scheduled scan fills the inbox, a Gmail digest reaches the
// paired Telegram chat and the desktop through the notification policy, and a
// recruiter reply that arrives afterwards fires an Ask Agent trigger whose
// result is a stored draft and nothing sent.
func TestPhase5Exit_ScheduledScanDigestDesktopAndRecruiterTriggerEndInADraft(t *testing.T) {
	board := atsServer(t)
	bot := newBotAPI(t)
	gm := newFakeGmail(t)
	l := &scriptedLauncher{script: draftTurn, started: make(chan *scriptedPi, 8), hold: make(chan struct{})}
	h := startDomainHarness(t, false, func(c *Config) {
		c.Domain.ScanEndpoints = discovery.Endpoints{discovery.Greenhouse: board.URL}
		c.Domain.Gmail = gmail.Options{Dial: gm.Dial, NoWatch: true}
		c.Domain.TelegramAPI = bot.srv.URL
		c.Domain.PiLauncher = l
		c.Domain.PiPath = "/opt/pi"
	})
	l.release()
	d := h.domainOf()
	ctx := h.srv.bg

	// The agent is served by a pi login, so no model key is needed anywhere;
	// every other task stays unrouted and answers deterministically.
	r := h.send("PATCH", "/api/llm/routing", wire.LLMRoutingPatch{Routes: map[string]*wire.LLMRoute{"agent-kernel": {Provider: "pi:openai-codex", Model: "gpt-6.1-sol"}}})
	if r.StatusCode != 200 {
		t.Fatalf("route the agent: %d %s", r.StatusCode, r.body)
	}

	// Telegram is paired with the dashboard's code.
	if r := h.send("PUT", "/api/telegram/token", map[string]any{"token": bot.token}); r.StatusCode != 200 {
		t.Fatalf("telegram token: %d %s", r.StatusCode, r.body)
	}
	var code wire.Success[wire.TelegramPairingCode]
	strictInto(t, h.send("POST", "/api/telegram/pairing-code", nil).body, &code)
	bot.say(7, 8, code.Data.Code)
	bot.waitText(t, "Paired")

	// The digests are run by hand below; the 08:00 and 18:00 timer would race
	// them on the fake clock, so it is switched off before Gmail is connected.
	off := false
	if r := h.send("PATCH", "/api/gmail/settings", wire.GmailSettingsPatch{ScheduledDigests: &off}); r.StatusCode != 200 {
		t.Fatalf("settings: %d %s", r.StatusCode, r.body)
	}
	h.connectGmail()
	desktop := h.openStream("/api/notifications/events", h.token(), "")
	defer desktop.close()

	// 1. A scheduled scan of a synthetic board runs when its slot comes and
	// puts the posting in the inbox.
	if _, err := d.repo.RegistryAdd(ctx, "test", discovery.Board{ATS: discovery.Greenhouse, Slug: "example-co", Company: "Example Co"}); err != nil {
		t.Fatal(err)
	}
	sched := h.post("/api/schedules", map[string]any{"name": "morning registry scan", "kind": "discover.registry",
		"preset": map[string]any{"kind": "daily", "time": "03:10", "timezone": "UTC"}, "inputs": map[string]any{"topN": 0}})
	if sched.StatusCode != 201 {
		t.Fatalf("create the schedule: %d %s", sched.StatusCode, sched.body)
	}
	id := sched.data()["id"].(float64)
	if n := h.get("/api/inbox").data()["total"].(float64); n != 0 {
		t.Fatalf("the inbox holds %v rows before the scan", n)
	}
	// Steps under the scheduler's five-minute gap rule, so the slot is a fire
	// and not a missed one.
	for range 4 {
		h.clock.advance(2 * time.Minute)
		d.sched.Tick(ctx)
	}
	waitLong(t, "the scheduled scan to finish", func() bool {
		rows := scheduleHistoryRows(t, h, id)
		return len(rows) == 1 && rows[0].(map[string]any)["finishedAt"] != nil
	})
	row := scheduleHistoryRows(t, h, id)[0].(map[string]any)
	if row["outcome"] != "fired" || !strings.Contains(row["summary"].(string), "1 discovered, 1 new") {
		t.Fatalf("the scheduled scan: %v", row)
	}
	inbox := h.get("/api/inbox")
	var page wire.Success[wire.InboxPage]
	strictInto(t, inbox.body, &page)
	if page.Data.Total != 1 || page.Data.Rows[0].Status != wire.InboxPending || page.Data.Rows[0].Company != "Example Co" {
		t.Fatalf("inbox after the scan: %s", inbox.body)
	}

	// 2. A recruiter writes; the sync records the signal; the digest reaches
	// the Telegram fake and the desktop.
	gm.add(gmMessage{id: "f1", from: "Example Corp Recruiting <" + gmAddr("sam", "example.com") + ">", subject: "Interview with Example Corp",
		text: "Hi Alex, I'd love to hop on a quick call. Could you share your availability this week?", ms: h.ms(-time.Hour)})
	h.syncGmail(t, 1)
	dig := h.makeDigest()
	if dig.Delivery["telegram"] != "sent" || dig.Delivery["desktop"] != "sent" || len(dig.Items) == 0 {
		t.Fatalf("digest: %+v", dig)
	}
	if text := bot.waitText(t, "Gmail digest"); !strings.Contains(text, "Example Corp") {
		t.Errorf("the Telegram digest does not name the employer: %q", text)
	}
	shown := nextAnnouncement(t, desktop, "the digest", func(a wire.NotificationAnnounce) bool {
		return a.Link == gmail.DigestLink(int64(dig.ID))
	})
	if !strings.Contains(strings.ToLower(shown.Title), "digest") {
		t.Errorf("the desktop notification is titled %q", shown.Title)
	}

	// 3. A trigger on recruiter replies, then a new reply: the Ask Agent
	// fires on the next tick and the result is a draft.
	created := h.post("/api/agent/prompts", wire.AgentPromptCreate{Text: "Draft a short reply to the recruiter", Trigger: &wire.PromptTriggerSpec{Signals: []string{"recruiter_reply_needed"}}})
	prompt := decode[wire.AgentPrompt](t, created)
	if created.StatusCode != 201 && created.StatusCode != 200 || prompt.State != wire.PromptActive {
		t.Fatalf("create the trigger: %d %s", created.StatusCode, created.body)
	}
	h.clock.advance(time.Minute)
	gm.add(gmMessage{id: "f2", from: "Example Corp Recruiting <" + gmAddr("sam", "example.com") + ">", subject: "Interview with Example Corp",
		text: "Hi Alex, I'd love to hop on a quick call. Could you share your availability this week?", ms: h.ms(0)})
	h.clock.advance(time.Minute)
	h.syncGmail(t, 2)
	var firing wire.PromptFiring
	waitLong(t, "the trigger to end in a draft", func() bool {
		// A timer that is still running skips a tick, so the tick is repeated.
		d.sched.Tick(ctx)
		for _, p := range decode[wire.AgentPromptList](t, h.get("/api/agent/prompts")).Prompts {
			if p.ID == prompt.ID && len(p.Firings) == 1 && p.Firings[0].Draft != "" {
				firing = p.Firings[0]
				return true
			}
		}
		return false
	})
	if firing.SignalType != "recruiter_reply_needed" || firing.State != "answered" || !strings.HasPrefix(firing.Draft, "Hi Sam, thanks for reaching out.") || firing.SessionID == "" {
		t.Fatalf("firing = %+v", firing)
	}
	nextAnnouncement(t, desktop, "the trigger result", func(a wire.NotificationAnnounce) bool {
		return a.Link == "/ask?conversation="+firing.SessionID
	})

	// The first reply, which arrived before the trigger existed, did not fire,
	// and nothing was sent: the Gmail fake saw only reads, and no submission
	// or press claim exists.
	var firings int
	if err := h.srv.store.Load().Read().QueryRow(`SELECT count(*) FROM gmail_signal_events`).Scan(&firings); err != nil || firings != 2 {
		t.Fatalf("signal events = %d, %v", firings, err)
	}
	if l.count() != 1 {
		t.Errorf("pi was started %d times, want one for the one firing", l.count())
	}
	for _, c := range gm.Commands("") {
		if !gmailReads.MatchString(c) {
			t.Errorf("the Gmail fake saw %q", c)
		}
	}
	var claims int
	if err := h.srv.store.Load().Read().QueryRow(`SELECT count(*) FROM submissions`).Scan(&claims); err != nil || claims != 0 {
		t.Errorf("submission claims = %d, %v", claims, err)
	}
}
