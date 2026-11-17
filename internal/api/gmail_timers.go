package api

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	chatsessions "applysmith/internal/agent/sessions"
	"applysmith/internal/gmail"
	"applysmith/internal/notify"
	"applysmith/internal/notify/telegram"
	"applysmith/internal/scheduler"
)

// gmailNotReadable is why a Gmail timer waits.
const gmailNotReadable = "Gmail is not connected or refuses its app password"

// telegramDigest is the Telegram sink's source for the pages of a digest
// announcement or of the day summary. A digest that carries the day summary
// sends the summary's pages first, so the rating keyboard stays on the
// digest's last page.
func (s *Server) telegramDigest(ctx context.Context, link string) ([]string, *telegram.Markup, error) {
	d, err := s.dom()
	if err != nil {
		return nil, nil, err
	}
	if day, ok := strings.CutPrefix(link, dayLink("")); ok {
		if pages := s.dayPages(ctx, d, day, 0); len(pages) > 0 {
			return pages, nil, nil
		}
		return nil, nil, notify.ErrGone
	}
	id, ok := gmail.ParseDigestLink(link)
	if !ok {
		return nil, nil, notify.ErrGone
	}
	pages, rows, err := d.gmail.Digests.TelegramView(ctx, id)
	if errors.Is(err, gmail.ErrNoDigest) {
		return nil, nil, notify.ErrGone
	}
	return append(s.dayPages(ctx, d, "", id), pages...), ratingMarkup(rows), err
}

// ratingMarkup turns the digest's rating rows into an inline keyboard.
func ratingMarkup(rows [][]gmail.RatingButton) *telegram.Markup {
	if len(rows) == 0 {
		return nil
	}
	m := &telegram.Markup{}
	for _, r := range rows {
		var row []telegram.Button
		for _, b := range r {
			row = append(row, telegram.Button{Text: b.Text, Data: b.Data})
		}
		m.Rows = append(m.Rows, row)
	}
	return m
}

// gmailDigestTimer is the built-in timer for the digests at 08:00 and 18:00 and
// the Sunday weekly review. It runs in the process that holds the scheduler,
// only while Gmail is connected and scheduled digests are on.
type gmailDigestTimer struct{ d *domain }

func (gmailDigestTimer) Name() string { return "gmail-digest" }
func (gmailDigestTimer) Describe() string {
	return "Gmail digests at 08:00 and 18:00 local time, and the weekly review on Sunday 18:00, only while Gmail is connected and scheduled digests are on"
}

func (t gmailDigestTimer) Status(ctx context.Context, _ time.Time) scheduler.TimerStatus {
	switch {
	case t.d.gmail.Repo.Demo():
		return scheduler.TimerStatus{Reason: "a demo workspace never runs digests"}
	case !t.d.gmail.Service.Readable(ctx):
		return scheduler.TimerStatus{Reason: gmailNotReadable}
	case !t.d.gmail.Repo.Switches(ctx).ScheduledDigests:
		return scheduler.TimerStatus{Reason: "scheduled digests are switched off"}
	}
	return scheduler.TimerStatus{Active: true}
}

func (t gmailDigestTimer) Tick(ctx context.Context, now time.Time) error {
	return t.d.gmail.Digests.Tick(ctx, now, time.Local)
}

// gmailAlertsTimer evaluates the instant alerts on every tick, after
// resolving the links again when a tracker row was added.
type gmailAlertsTimer struct{ d *domain }

func (gmailAlertsTimer) Name() string { return "gmail-alerts" }
func (gmailAlertsTimer) Describe() string {
	return "Instant Gmail alerts, evaluated after every sync and on each tick while Gmail is connected"
}

func (t gmailAlertsTimer) Status(ctx context.Context, _ time.Time) scheduler.TimerStatus {
	if t.d.gmail.Repo.Demo() {
		return scheduler.TimerStatus{Reason: "a demo workspace never evaluates Gmail alerts"}
	}
	if !t.d.gmail.Service.Readable(ctx) {
		return scheduler.TimerStatus{Reason: gmailNotReadable}
	}
	return scheduler.TimerStatus{Active: true}
}

func (t gmailAlertsTimer) Tick(ctx context.Context, _ time.Time) error {
	var errs []error
	if _, err := t.d.gmail.Pipeline.RelinkIfRowsChanged(ctx); err != nil {
		errs = append(errs, err)
	}
	if _, err := t.d.gmail.EvaluateAlerts(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// gmailRemindersTimer announces the deadline reminders of open job threads
// (gmail.Repo.Reminders) on every tick; each is announced once by its key, and
// quiet hours hold it like any normal announcement. The alerts switch gates
// it.
type gmailRemindersTimer struct{ d *domain }

func (gmailRemindersTimer) Name() string { return "gmail-reminders" }
func (gmailRemindersTimer) Describe() string {
	return "Gmail deadline reminders: at 19:00 the evening before a job mail's deadline and at 09:00 on the day, while Gmail alerts are on"
}

func (t gmailRemindersTimer) Status(ctx context.Context, _ time.Time) scheduler.TimerStatus {
	switch {
	case t.d.gmail.Repo.Demo():
		return scheduler.TimerStatus{Reason: "a demo workspace never reminds of Gmail deadlines"}
	case !t.d.gmail.Repo.Switches(ctx).Alerts:
		return scheduler.TimerStatus{Reason: "Gmail alerts are switched off"}
	}
	return scheduler.TimerStatus{Active: true}
}

func (t gmailRemindersTimer) Tick(ctx context.Context, now time.Time) error {
	if t.d.gmail.Repo.Demo() || !t.d.gmail.Repo.Switches(ctx).Alerts {
		return nil
	}
	due, err := t.d.gmail.Repo.Reminders(ctx, now, time.Local)
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range due {
		errs = append(errs, t.d.notify.policy.Announce(ctx, notify.Event{Key: r.Key, Kind: notify.EventGmailReminder, Title: r.Title, Body: r.Body, Link: r.Link}))
	}
	return errors.Join(errs...)
}

// digestWords are the title and headline of a digest's announcement; the
// evening digest that carries the day summary says so (one push for both).
func (s *Server) digestWords(ctx context.Context, dig gmail.Digest) (string, string) {
	if c, ok := s.carryDay(ctx, dig); ok {
		return dig.AnnouncementTitle() + " and your day", c.Title + ". " + dig.Headline
	}
	return dig.AnnouncementTitle(), dig.Headline
}

// digestChannels wires the three places a digest is delivered: the paired
// Telegram chat with the rating keyboard, the desktop through the notification
// policy, and a new Ask Agent conversation.
func (s *Server) digestChannels(ns *notifyStack) gmail.Channels {
	return gmail.Channels{
		// Both go through the announcement policy: quiet hours hold them, the
		// key makes each once-only, and the Telegram sink pages the full text.
		Telegram: func(ctx context.Context, dig gmail.Digest) error {
			switch {
			case !ns.settings.Get(ctx).Telegram:
				return gmail.ErrChannelOff
			case !ns.tg.Sink().Available(ctx):
				return fmt.Errorf("%w: not paired", gmail.ErrChannelOff)
			}
			title, body := s.digestWords(ctx, dig)
			return ns.policy.AnnounceTo(ctx, "telegram", notify.Event{Key: "gmail-digest|telegram|" + strconv.FormatInt(dig.ID, 10),
				Kind: notify.EventGmailDigest, Title: title, Body: body, Link: gmail.DigestLink(dig.ID)})
		},
		Desktop: func(ctx context.Context, dig gmail.Digest) error {
			if !ns.settings.Get(ctx).Desktop {
				return gmail.ErrChannelOff
			}
			title, body := s.digestWords(ctx, dig)
			return ns.policy.AnnounceTo(ctx, "desktop", notify.Event{Key: "gmail-digest|desktop|" + strconv.FormatInt(dig.ID, 10),
				Kind: notify.EventGmailDigest, Title: title, Body: body, Link: gmail.DigestLink(dig.ID)})
		},
		Agent: func(ctx context.Context, dig gmail.Digest) (string, error) {
			d, err := s.dom()
			if err != nil {
				return "", err
			}
			at := dig.CreatedAt
			if !dig.Slot.IsZero() {
				at = dig.Slot
			}
			title := "Gmail digest, " + at.Local().Format("Mon Jan 2 15:04")
			if dig.Kind == gmail.DigestWeekly {
				title = "Gmail weekly review, " + at.Local().Format("Mon Jan 2")
			}
			sess, err := d.sessions.Create(ctx, chatsessions.Digest, title)
			if err != nil {
				return "", err
			}
			// The text comes from mail, so a later prompt that contains it starts untrusted.
			_, err = d.sessions.Append(ctx, chatsessions.Message{SessionID: sess.ID, Role: chatsessions.RoleAssistant, Body: dig.Body.Text,
				SystemMade: true, OutsideContent: true, CreatedAt: at})
			return sess.ID, err
		},
	}
}
