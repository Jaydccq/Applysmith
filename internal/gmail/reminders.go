package gmail

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"applysmith/internal/gmail/classify"
	"applysmith/internal/gmail/lifecycle"
)

// Deadline reminders, after mail-agent's todos: an open job thread whose
// deciding message states a deadline is a todo, reminded of the evening before
// its due day and on the morning of it. Nothing about a todo is stored but a
// snooze: the thread's lane, its done mark and its newest mail decide it.

const (
	// ReminderEveHour and ReminderDayHour are the local hours of the reminder
	// the evening before a due day and of the one on the day.
	ReminderEveHour = 19
	ReminderDayHour = 9
	// SnoozeCatchUp is how long after its time a snoozed reminder is still
	// sent, for a computer that was off at the time.
	SnoozeCatchUp = 12 * time.Hour
)

// deadlineTypes are the deciding types whose due time is the user's deadline.
// An interview's is a meeting, which the interview reminders cover.
var deadlineTypes = []string{classify.TypeAssessment, classify.TypeActionNeeded, classify.TypeReplyNeeded}

// ErrNoDeadline refuses to snooze a thread that has no open deadline.
var ErrNoDeadline = errors.New("gmail: that thread has no open deadline")

// Todo is an open job thread with a deadline.
type Todo struct {
	Thread Thread
	// Day is the due day, at midnight in the zone it was read in.
	Day time.Time
	// Snoozed is the time of the reminder Tomorrow set, zero when none.
	Snoozed time.Time
}

// Title words a todo as its reminders are titled: the employer, what is due
// and when, against now ("Acme: online assessment, due tomorrow").
func (td Todo) Title(now time.Time, loc *time.Location) string {
	t := td.Thread
	return employerName(t.Employer) + ": " + alertWhat[t.View.Latest.Type] + ", " + whenDue(td.Day, now, loc)
}

// todoOf reads a thread as a todo: its deciding message is an actionable
// deadline with a due time, and the thread waits on the user (the do-now or
// prepare lane). A thread marked done reads as closed and a dismissed one has
// no deciding message, so neither is one.
func todoOf(t Thread, loc *time.Location) (Todo, bool) {
	l := t.View.Latest
	if l.Tier != classify.TierActionable || t.View.LatestRule == lifecycle.ContinuityRule || t.View.Due.IsZero() ||
		!slices.Contains(deadlineTypes, l.Type) || t.View.Lane != lifecycle.LaneDoNow && t.View.Lane != lifecycle.LanePrepare {
		return Todo{}, false
	}
	return Todo{Thread: t, Day: dueDay(t.View.Due, loc)}, true
}

// dateOnly reports a due time that is a date: the classifier reads a date as
// 00:00 UTC of that day, with no time of day.
func dateOnly(due time.Time) bool {
	u := due.UTC()
	return u.Hour() == 0 && u.Minute() == 0 && u.Second() == 0 && u.Nanosecond() == 0
}

// dueDay is the calendar day of a due time, at midnight in loc: a date is that
// date wherever the user is, and an instant falls on its local day.
func dueDay(due time.Time, loc *time.Location) time.Time {
	d := due.In(loc)
	if dateOnly(due) {
		d = due.UTC()
	}
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
}

// DueWords words a due time: a date alone, or an instant with its local time.
func DueWords(due time.Time, loc *time.Location) string {
	if dateOnly(due) {
		return dueDay(due, loc).Format("Mon Jan 2")
	}
	return due.In(loc).Format("Mon Jan 2 15:04 MST")
}

// whenDue words a due day against now: due today, due tomorrow, due on a
// later day, or was due on a day that passed.
func whenDue(day, now time.Time, loc *time.Location) string {
	n := now.In(loc)
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
	switch {
	case day.Equal(today):
		return "due today"
	case day.Equal(today.AddDate(0, 0, 1)):
		return "due tomorrow"
	case day.Before(today):
		return "was due " + day.Format("Mon Jan 2")
	}
	return "due " + day.Format("Mon Jan 2")
}

// Reminder is a deadline reminder due now.
type Reminder struct {
	// Key makes its announcement once-only: the thread, the due day and the
	// slot, so a new due day is reminded of again.
	Key               string
	ThreadID          string
	Title, Body, Link string
}

// reminderSlot is one planned reminder of a todo, due from at until till.
type reminderSlot struct {
	name     string
	at, till time.Time
}

// slotsOf plans a todo's reminders: 19:00 the evening before its due day,
// until the day begins, and 09:00 on the day, while it lasts. A due time with
// a time of day ends the day's slot, so no reminder comes after it.
func slotsOf(td Todo) []reminderSlot {
	day, due := td.Day, td.Thread.View.Due
	y, m, d, loc := day.Year(), day.Month(), day.Day(), day.Location()
	end := time.Date(y, m, d+1, 0, 0, 0, 0, loc)
	if !dateOnly(due) && due.Before(end) {
		end = due
	}
	return []reminderSlot{
		{"eve", time.Date(y, m, d-1, ReminderEveHour, 0, 0, 0, loc), time.Date(y, m, d, 0, 0, 0, 0, loc)},
		{"day", time.Date(y, m, d, ReminderDayHour, 0, 0, 0, loc), end},
	}
}

// Todos are the open deadline todos at now in loc, the soonest due day first.
func (r *Repo) Todos(ctx context.Context, now time.Time, loc *time.Location) ([]Todo, error) {
	threads, err := r.Threads(ctx, now)
	if err != nil {
		return nil, err
	}
	snoozed, err := r.snoozes(ctx)
	if err != nil {
		return nil, err
	}
	var out []Todo
	for _, t := range threads {
		if td, ok := todoOf(t, loc); ok {
			td.Snoozed = snoozed[t.ID]
			out = append(out, td)
		}
	}
	slices.SortStableFunc(out, func(a, b Todo) int { return a.Day.Compare(b.Day) })
	return out, nil
}

// Reminders are the deadline reminders due at now in loc. A snooze takes the
// place of the slots up to its time with one reminder at it, still sent
// within SnoozeCatchUp. Each is announced once by its key, so a caller asks
// on every tick.
func (r *Repo) Reminders(ctx context.Context, now time.Time, loc *time.Location) ([]Reminder, error) {
	todos, err := r.Todos(ctx, now, loc)
	if err != nil {
		return nil, err
	}
	var out []Reminder
	for _, td := range todos {
		until, has := td.Snoozed, !td.Snoozed.IsZero()
		if has && !now.Before(until) && now.Sub(until) < SnoozeCatchUp {
			out = append(out, composeReminder(td, now, loc, "snooze-"+until.UTC().Format(time.RFC3339)))
		}
		for _, sl := range slotsOf(td) {
			if now.Before(sl.at) || !now.Before(sl.till) || has && !sl.at.After(until) {
				continue
			}
			out = append(out, composeReminder(td, now, loc, sl.name))
		}
	}
	return out, nil
}

// composeReminder words a reminder like the thread's alert, with when it is
// due in the title.
func composeReminder(td Todo, now time.Time, loc *time.Location, slot string) Reminder {
	t := td.Thread
	parts := []string{}
	if role := roleName(t.Role); role != "" {
		parts = append(parts, role+".")
	}
	parts = append(parts, "Due "+DueWords(t.View.Due, loc)+".")
	return Reminder{
		Key:      fmt.Sprintf("gmail-reminder|%s|%s|%s", t.ID, td.Day.Format(time.DateOnly), slot),
		ThreadID: t.ID,
		Title:    td.Title(now, loc),
		Body:     strings.Join(parts, " "),
		Link:     ThreadLink(t.ID),
	}
}

// Snooze moves a todo's next reminder to 09:00 tomorrow in loc, in place of
// the reminders before it, and returns that time. A thread with no open
// deadline is ErrNoDeadline.
func (r *Repo) Snooze(ctx context.Context, threadID string, loc *time.Location) (time.Time, error) {
	t, err := r.threadOf(ctx, threadID)
	if err != nil {
		return time.Time{}, err
	}
	if _, ok := todoOf(t, loc); !ok {
		return time.Time{}, ErrNoDeadline
	}
	now := r.now()
	n := now.In(loc)
	until := time.Date(n.Year(), n.Month(), n.Day()+1, ReminderDayHour, 0, 0, 0, loc)
	err = r.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO gmail_reminder_snoozes (thread_id, until_ms, set_at) VALUES (?, ?, ?)
			ON CONFLICT(thread_id) DO UPDATE SET until_ms = excluded.until_ms, set_at = excluded.set_at`, threadID, until.UnixMilli(), stamp(now))
		return err
	})
	return until, err
}

func (r *Repo) snoozes(ctx context.Context) (map[string]time.Time, error) {
	rows, err := r.st.Read().QueryContext(ctx, `SELECT thread_id, until_ms FROM gmail_reminder_snoozes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var id string
		var ms int64
		if err := rows.Scan(&id, &ms); err != nil {
			return nil, err
		}
		out[id] = time.UnixMilli(ms)
	}
	return out, rows.Err()
}

// ThreadLink is the dashboard address of a thread, which its alerts and
// reminders carry.
func ThreadLink(id string) string { return "/gmail?thread=" + id }

// ParseThreadLink reads the thread id out of ThreadLink.
func ParseThreadLink(link string) (string, bool) {
	id, ok := strings.CutPrefix(link, "/gmail?thread=")
	if !ok || id == "" || strings.ContainsAny(id, "?&#/") {
		return "", false
	}
	return id, true
}
