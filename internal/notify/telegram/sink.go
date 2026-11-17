package telegram

import (
	"context"
	"errors"
	"strings"

	"applysmith/internal/notify"
	"applysmith/internal/vault"
)

// Sink delivers announcements to the paired chat. It is the policy's Telegram
// channel.
type Sink struct{ g *Gateway }

// Sink returns the policy sink of the gateway.
func (g *Gateway) Sink() *Sink { return &Sink{g: g} }

// Name implements notify.Sink.
func (*Sink) Name() string { return "telegram" }

// Available implements notify.Sink: the sink exists only with a token and a
// paired chat.
func (s *Sink) Available(ctx context.Context) bool {
	if _, err := s.g.cfg.Token(); err != nil {
		return false
	}
	_, paired := s.g.state.Owner(ctx)
	return paired
}

// text is the message of an announcement: its title, its body and, while
// remote access gives the dashboard a public address, the link to its page.
func (s *Sink) text(a notify.Announcement) string {
	out := a.Title
	if a.Body != "" {
		out += "\n" + a.Body
	}
	if s.g.cfg.LinkBase != nil && strings.HasPrefix(a.Link, "/") {
		if base, on := s.g.cfg.LinkBase(); on {
			out += "\n" + strings.TrimRight(base, "/") + a.Link
		}
	}
	return out
}

// Announce implements notify.Sink. An announcement of an entry carries a button
// to acknowledge it; a Gmail alert or deadline reminder carries the mail
// actions its thread offers.
func (s *Sink) Announce(ctx context.Context, a notify.Announcement) (string, error) {
	o, ok := s.g.state.Owner(ctx)
	if !ok {
		return "", notify.ErrGone
	}
	if (a.Kind == notify.EventGmailDigest || a.Kind == notify.EventDaySummary) && s.g.cfg.Digest != nil {
		return s.digest(ctx, a)
	}
	var markup *Markup
	var anchor Anchor
	if s.g.cfg.Conversation != nil {
		anchor.Session = s.g.cfg.Conversation(ctx, a)
	}
	if (a.Kind == notify.EventGmailAlert || a.Kind == notify.EventGmailReminder) && s.g.cfg.Mail != nil {
		if thread, actions := s.g.cfg.Mail(ctx, a.Kind, a.Link); thread != "" {
			markup, anchor.Thread = mailMarkup(thread, actions), thread
		}
	}
	if data := FormAck + ":" + a.EntryID; a.EntryID != "" && len(data) <= MaxCallbackBytes {
		markup = &Markup{Rows: [][]Button{{{Text: "Got it", Data: data}}}}
	}
	id, err := s.g.sender.Send(ctx, o.ChatID, vault.Redact(s.text(a)), markup, a.Urgent)
	if err != nil {
		return "", err
	}
	if err := s.g.state.SetAnchor(ctx, o.ChatID, id, anchor); err != nil {
		// The message is out; a reply to it reaches the chat's own conversation.
		s.g.cfg.Log.Warn("what a Telegram announcement belongs to could not be recorded", "error", err.Error())
	}
	return formatID(o.ChatID, id), nil
}

// Withdraw implements notify.Sink: it edits the sent message to say it was
// handled or superseded and removes its buttons. A message Telegram no longer
// lets the bot edit is left as it is.
func (s *Sink) Withdraw(ctx context.Context, a notify.Announcement, ext string, why notify.Why) error {
	if a.Kind == notify.EventGmailDigest {
		// The message holds the digest text, which an edit would replace.
		return nil
	}
	chat, msg, ok := parseID(ext)
	if !ok {
		return nil
	}
	marker := "Handled."
	if why == notify.Superseded {
		marker = "Superseded by a newer update."
	}
	err := s.g.sender.Edit(ctx, chat, msg, vault.Redact(s.text(a))+"\n\n"+marker, nil)
	var api *APIError
	if errors.As(err, &api) && api.Code == 400 {
		return nil
	}
	return err
}

// digest sends a Gmail digest or the day summary in full: its pages, with a
// digest's rating keyboard on the last. Only the announcement policy calls it,
// so quiet hours, the once-only key and withdrawal apply to it like to anything
// else.
func (s *Sink) digest(ctx context.Context, a notify.Announcement) (string, error) {
	pages, markup, err := s.g.cfg.Digest(ctx, a.Link)
	if err != nil {
		return "", err
	}
	chat, last, err := s.g.SendPages(ctx, a.ID, pages, markup)
	if errors.Is(err, ErrNotPaired) {
		return "", notify.ErrGone
	}
	if err != nil {
		return "", err
	}
	return formatID(chat, last), nil
}
