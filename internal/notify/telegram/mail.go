package telegram

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"

	"applysmith/internal/vault"
)

// Mail actions: what the buttons of a Gmail alert or deadline reminder do to
// its thread, through the operations of the Gmail page (the gmail package's
// thread actions, by the same names). Nothing is sent.
const (
	MailDone    = "done"    // mark the thread done
	MailReopen  = "reopen"  // undo Done
	MailDismiss = "dismiss" // not job mail
	MailRestore = "restore" // undo Not job mail
	MailDraft   = "draft"   // write a reply draft, shown to copy
	MailSnooze  = "snooze"  // remind of the deadline tomorrow at 09:00
)

// MailActions is the closed set of mail actions.
var MailActions = []string{MailDone, MailReopen, MailDismiss, MailRestore, MailDraft, MailSnooze}

var mailLabels = map[string]string{MailDone: "Done", MailReopen: "Undo", MailDismiss: "Not job mail", MailRestore: "Undo", MailDraft: "Draft reply", MailSnooze: "Tomorrow"}

// mailMarkup is one row of buttons, one per action on the thread.
func mailMarkup(thread string, actions []string) *Markup {
	var row []Button
	for _, a := range actions {
		data := FormMail + ":" + a + ":" + thread
		if label, ok := mailLabels[a]; ok && len(data) <= MaxCallbackBytes {
			row = append(row, Button{Text: label, Data: data})
		}
	}
	if len(row) == 0 {
		return nil
	}
	return &Markup{Rows: [][]Button{row}}
}

// mail runs a mail button. A draft comes as a message of its own and the
// alert keeps its buttons; any other action rewrites the message with what
// happened, and Done and Not job mail offer Undo.
func (g *Gateway) mail(ctx context.Context, cb *Callback, chat int64, action, thread string, toast func(string)) (string, int64, error) {
	name := "callback:mail:" + action
	if action == MailDraft {
		toast("Writing a draft.")
		stop := g.working(ctx, chat)
		text, err := g.cfg.Backend.Mail(ctx, action, thread)
		stop()
		if err != nil {
			// The backend's error says that no draft was written, and why.
			text = upperFirst(strings.Join(strings.Fields(err.Error()), " "))
		}
		return name, chat, g.reply(ctx, chat, vault.Redact(text), nil)
	}
	text, err := g.cfg.Backend.Mail(ctx, action, thread)
	var markup *Markup
	switch {
	case err != nil:
		toast("That did not work.")
		text = "That did not work: " + clipLine(err.Error())
	case action == MailDone:
		toast("Done.")
		markup = mailMarkup(thread, []string{MailReopen})
	case action == MailDismiss:
		toast("Done.")
		markup = mailMarkup(thread, []string{MailRestore})
	default:
		toast("Done.")
	}
	// The announcement has no blank line; what a button said follows one.
	base, _, _ := strings.Cut(strings.TrimSpace(cb.Message.Text), "\n\n")
	return name, chat, g.sender.Edit(ctx, chat, cb.Message.MessageID, vault.Redact(base+"\n\n"+text), markup)
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}
