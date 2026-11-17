package api

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"applysmith/internal/api/wire"
	"applysmith/internal/gmail"
	"applysmith/internal/gmail/classify"
)

// buttonsOf lists the callback data of the newest sent message holding sub.
func (b *botAPI) buttonsOf(sub string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := len(b.sent) - 1; i >= 0; i-- {
		if strings.Contains(b.sent[i]["text"].(string), sub) {
			return fmt.Sprint(b.sent[i]["reply_markup"])
		}
	}
	return ""
}

// editWith waits for an edit whose text holds sub and returns its text and
// keyboard.
func (b *botAPI) editWith(t *testing.T, sub string) (string, string) {
	t.Helper()
	var text, markup string
	waitLong(t, "an edit saying "+sub, func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		for _, e := range b.edits {
			if s, _ := e["text"].(string); strings.Contains(s, sub) {
				text, markup = s, fmt.Sprint(e["reply_markup"])
				return true
			}
		}
		return false
	})
	return text, markup
}

// A Gmail alert reaches the chat with Done, Not job mail and Draft reply, which
// act on the thread as the Gmail page does; the deadline's reminder the
// evening before carries Done and Tomorrow. Nothing is sent by mail.
func TestTelegram_Gateway_GmailAlertAndReminderButtonsActOnTheThread(t *testing.T) {
	bot := newBotAPI(t)
	g := newFakeGmail(t)
	h := startDomainHarness(t, false, func(c *Config) {
		c.Domain.TelegramAPI = bot.srv.URL
		c.Domain.Gmail = gmail.Options{Dial: g.Dial, NoWatch: true}
	})
	if r := h.send("PUT", "/api/telegram/token", map[string]any{"token": bot.token}); r.StatusCode != 200 {
		t.Fatalf("token: %d %s", r.StatusCode, r.body)
	}
	var code wire.Success[wire.TelegramPairingCode]
	strictInto(t, h.send("POST", "/api/telegram/pairing-code", nil).body, &code)
	bot.say(7, 8, code.Data.Code)
	bot.waitText(t, "Paired")
	d, ctx := h.domainOf(), context.Background()
	seedGmail(t, h)
	if _, err := d.gmail.EvaluateAlerts(ctx); err != nil { // the first evaluation seeds silently
		t.Fatal(err)
	}
	now := h.clock.now()
	due := time.Date(now.Year(), now.Month(), now.Day()+2, 0, 0, 0, 0, time.UTC)
	seedGmail(t, h, apiSignal{id: "a1", thread: "t-a", typ: classify.TypeAssessment, employer: "Acme", role: "Backend Engineer", at: now.Add(-time.Hour), due: due})
	if n, err := d.gmail.EvaluateAlerts(ctx); err != nil || n != 1 {
		t.Fatalf("alerts = %d, %v", n, err)
	}
	alert := bot.waitText(t, "Acme: online assessment")
	if kb := bot.buttonsOf("Acme: online assessment"); !strings.Contains(kb, "mail:done:t-a") || !strings.Contains(kb, "mail:dismiss:t-a") || !strings.Contains(kb, "mail:draft:t-a") {
		t.Fatalf("alert keyboard = %s", kb)
	}
	isDone := func() bool {
		td, ok, err := d.gmail.Repo.ThreadDetail(ctx, "t-a", h.clock.now())
		if err != nil || !ok {
			t.Fatalf("thread: %v %v", ok, err)
		}
		return td.Done
	}

	bot.tap(7, 8, "mail:done:t-a", alert)
	text, kb := bot.editWith(t, "Marked done")
	if !strings.Contains(kb, "mail:reopen:t-a") || !isDone() {
		t.Fatalf("after Done: %q %s done=%v", text, kb, isDone())
	}
	bot.tap(7, 8, "mail:reopen:t-a", text)
	bot.editWith(t, "Reopened.")
	if isDone() {
		t.Fatal("Undo left the thread done")
	}

	// No model is routed in this harness: the draft says why there is none.
	bot.tap(7, 8, "mail:draft:t-a", alert)
	bot.waitText(t, "No reply was drafted")

	// 19:00 local the evening before the due day.
	eve := time.Date(due.Year(), due.Month(), due.Day()-1, gmail.ReminderEveHour, 0, 0, 0, time.Local)
	if err := (gmailRemindersTimer{d: d}).Tick(ctx, eve); err != nil {
		t.Fatal(err)
	}
	reminder := bot.waitText(t, "Acme: online assessment, due tomorrow")
	if kb := bot.buttonsOf("due tomorrow"); !strings.Contains(kb, "mail:done:t-a") || !strings.Contains(kb, "mail:snooze:t-a") || strings.Contains(kb, "mail:draft") {
		t.Fatalf("reminder keyboard = %s", kb)
	}
	bot.tap(7, 8, "mail:snooze:t-a", reminder)
	bot.editWith(t, "I will remind you again")
	var snoozes int
	if err := h.srv.store.Load().Read().QueryRow(`SELECT count(*) FROM gmail_reminder_snoozes WHERE thread_id = 't-a'`).Scan(&snoozes); err != nil || snoozes != 1 {
		t.Fatalf("snoozes = %d, %v", snoozes, err)
	}

	// Not job mail ends the todo: no reminder comes for it again. Its Undo
	// puts the thread back with its deadline.
	bot.tap(7, 8, "mail:dismiss:t-a", alert)
	text, kb = bot.editWith(t, "Set aside as not job mail")
	if !strings.Contains(kb, "mail:restore:t-a") {
		t.Fatalf("after Not job mail: %q %s", text, kb)
	}
	todos := func() int {
		t.Helper()
		got, err := d.gmail.Repo.Todos(ctx, eve, time.Local)
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}
	if n := todos(); n != 0 {
		t.Fatalf("todos after Not job mail = %d", n)
	}
	bot.tap(7, 8, "mail:restore:t-a", text)
	bot.editWith(t, "Put back with the job mail.")
	if n := todos(); n != 1 {
		t.Fatalf("todos after its Undo = %d", n)
	}
}
