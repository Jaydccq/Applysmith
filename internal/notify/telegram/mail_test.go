package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"

	"applysmith/internal/notify"
)

func (r *rig) mailCalls() []string {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	return append([]string(nil), r.backend.mails...)
}

func TestTelegram_Mail_ParsesOnlyTheClosedSetOfActions(t *testing.T) {
	for data, ok := range map[string]bool{
		"mail:done:18f3a2b":                    true,
		"mail:reopen:t-a1":                     true,
		"mail:dismiss:18f3a2b":                 true,
		"mail:restore:18f3a2b":                 true,
		"mail:draft:18f3a2b":                   true,
		"mail:snooze:18f3a2b":                  true,
		"mail:send:18f3a2b":                    false,
		"mail:done":                            false,
		"mail:done:":                           false,
		"mail:done:a:b":                        false,
		"mail:done:a/b":                        false,
		"mail:done:" + strings.Repeat("a", 60): false,
		"Mail:done:18f3a2b":                    false,
	} {
		form, args := ParseCallback(data)
		if got := form == FormMail && len(args) == 2; got != ok {
			t.Errorf("%q: form %q args %v, want parsed %v", data, form, args, ok)
		}
	}
}

// A Gmail alert carries the buttons the product's hook names for its thread;
// a reminder its own; another announcement none of them.
func TestTelegram_Mail_AlertsAndRemindersCarryTheirThreadsButtons(t *testing.T) {
	r := newRig(t, func(c *Config) {
		c.Mail = func(_ context.Context, kind, link string) (string, []string) {
			if link != "/gmail?thread=t1" {
				return "", nil
			}
			if kind == notify.EventGmailReminder {
				return "t1", []string{MailDone, MailSnooze}
			}
			return "t1", []string{MailDone, MailDismiss, MailDraft}
		}
	})
	r.pair()
	r.serve()
	buttons := func(a notify.Announcement) string {
		t.Helper()
		if _, err := r.gw.Sink().Announce(bg, a); err != nil {
			t.Fatal(err)
		}
		m := r.bot.lastSent().Markup
		if m == nil || len(m.Rows) == 0 {
			return ""
		}
		var out []string
		for _, b := range m.Rows[0] {
			out = append(out, b.Text+"="+b.Data)
		}
		return strings.Join(out, ",")
	}
	alert := notify.Announcement{ID: 1, Kind: notify.EventGmailAlert, Title: "Acme: online assessment", Body: "Due Fri Mar 12.", Link: "/gmail?thread=t1"}
	if got := buttons(alert); got != "Done=mail:done:t1,Not job mail=mail:dismiss:t1,Draft reply=mail:draft:t1" {
		t.Errorf("alert buttons = %s", got)
	}
	reminder := alert
	reminder.ID, reminder.Kind = 2, notify.EventGmailReminder
	if got := buttons(reminder); got != "Done=mail:done:t1,Tomorrow=mail:snooze:t1" {
		t.Errorf("reminder buttons = %s", got)
	}
	other := alert
	other.ID, other.Link = 3, "/gmail?thread=gone"
	if got := buttons(other); got != "" {
		t.Errorf("an alert of an unknown thread = %s", got)
	}
}

// Done rewrites the alert with what happened and offers Undo; Undo rewrites it
// again from the alert's own text, not from the first answer.
func TestTelegram_Mail_DoneThenUndoRewriteTheAlert(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	alert := "Acme: online assessment\nDue Fri Mar 12."
	r.handle(cbUpdate(ownerUser, "mail:done:t1", alert))
	r.bot.mu.Lock()
	done := r.bot.edits[0]
	r.bot.mu.Unlock()
	if done.Text != alert+"\n\ndid done" || done.Markup == nil || len(done.Markup.Rows) != 1 || done.Markup.Rows[0][0].Data != "mail:reopen:t1" || done.Markup.Rows[0][0].Text != "Undo" {
		t.Fatalf("after Done: %+v", done)
	}
	r.handle(cbUpdate(ownerUser, "mail:reopen:t1", done.Text))
	r.bot.mu.Lock()
	undo := r.bot.edits[1]
	r.bot.mu.Unlock()
	if undo.Text != alert+"\n\ndid reopen" || undo.Markup == nil || len(undo.Markup.Rows) != 0 {
		t.Fatalf("after Undo: %+v", undo)
	}
	if got := strings.Join(r.mailCalls(), ","); got != "done:t1,reopen:t1" {
		t.Fatalf("backend calls = %s", got)
	}
}

func TestTelegram_Mail_ARefusalIsShownAndTheStrangerGetsNothing(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	r.backend.mu.Lock()
	r.backend.mailErr = errors.New("this thread asks nothing of you now; nothing changed")
	r.backend.mu.Unlock()
	r.handle(cbUpdate(ownerUser, "mail:done:t1", "Acme: offer received"))
	r.handle(cbUpdate(9009, "mail:dismiss:t1", "Acme: offer received"))
	r.bot.mu.Lock()
	defer r.bot.mu.Unlock()
	if len(r.bot.edits) != 1 || r.bot.edits[0].Text != "Acme: offer received\n\nThat did not work: this thread asks nothing of you now; nothing changed" {
		t.Fatalf("edits = %+v", r.bot.edits)
	}
	if got := strings.Join(r.mailCalls(), ","); got != "done:t1" {
		t.Fatalf("backend calls = %s", got)
	}
}

// A draft arrives as a message of its own and leaves the alert as it was; a
// failure says so in the same place.
func TestTelegram_Mail_ADraftIsAMessageOfItsOwn(t *testing.T) {
	r := newRig(t)
	r.pair()
	r.serve()
	r.handle(cbUpdate(ownerUser, "mail:draft:t1", "Acme: reply needed"))
	if got := r.bot.lastSent().Text; got != "did draft" {
		t.Fatalf("draft message = %q", got)
	}
	r.backend.mu.Lock()
	r.backend.mailErr = errors.New("no reply was drafted: the gmail-digest task has no model it can use")
	r.backend.mu.Unlock()
	r.handle(cbUpdate(ownerUser, "mail:draft:t2", "Acme: reply needed"))
	if got := r.bot.lastSent().Text; got != "No reply was drafted: the gmail-digest task has no model it can use" {
		t.Fatalf("failed draft = %q", got)
	}
	r.bot.mu.Lock()
	defer r.bot.mu.Unlock()
	if len(r.bot.edits) != 0 {
		t.Fatalf("a draft edited the alert: %+v", r.bot.edits)
	}
}
