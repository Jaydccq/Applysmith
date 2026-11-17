// Clean-room: docs/provenance.md#CR-FOLLOWUP
// Written from the application-tracker spec alone.

package tracker

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"applysmith/internal/workspace"
)

// SuggestionKind names what the user is nudged to consider.
type SuggestionKind string

// The four suggestion kinds.
const (
	KindFollowUp         SuggestionKind = "follow_up"
	KindCadenceExhausted SuggestionKind = "cadence_exhausted"
	KindConsiderClosing  SuggestionKind = "consider_closing"
	KindReplyCheck       SuggestionKind = "reply_check"
)

// replyCheckAfterDays is fixed: the policy has no key for it.
const replyCheckAfterDays = 3

// Suggestion is one computed nudge. It is never stored and nothing is sent.
type Suggestion struct {
	ID      int64  `json:"id"`
	Company string `json:"company"`
	Role    string `json:"role"`
	Status  Status `json:"status"`
	// Version is the row's version when the suggestion was computed, so a
	// button on a stale card can quote it and be refused once the row moved.
	Version int            `json:"version"`
	Kind    SuggestionKind `json:"kind"`
	// Number is the follow-up's ordinal for kind follow_up.
	Number int `json:"number,omitempty"`
	// Due is the calendar day the suggestion became or becomes due.
	Due string `json:"due,omitempty"`
}

// followupEvent is the part of an event the cadence reads.
type followupEvent struct {
	id     int64
	day    string
	actor  Actor
	kind   EventKind
	status Status
	days   int
	undoes int64 // the follow-up or snooze this event takes back
}

func addDays(day string, n int) string {
	d, _ := time.Parse(dateLayout, day)
	return d.AddDate(0, 0, n).Format(dateLayout)
}

func (t *Tracker) dayOf(at string) string {
	ts, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return truncate(at, 10)
	}
	return ts.In(t.now().Location()).Format(dateLayout)
}

// loadFollowupEvents returns the events of the open applied and responded
// rows (or of one row when id is not zero), in order, keyed by row.
func (t *Tracker) loadFollowupEvents(ctx context.Context, q querier, id int64) (map[int64][]followupEvent, error) {
	text := `SELECT ev.entry_id, ev.id, ev.at, ev.actor, ev.kind, ev.new_status, ev.detail_json FROM tracker_events ev
		JOIN tracker_entries e ON e.id = ev.entry_id WHERE e.archived_at IS NULL AND e.status IN ('applied', 'responded')`
	var args []any
	if id != 0 {
		text += ` AND e.id = ?`
		args = append(args, id)
	}
	rows, err := q.QueryContext(ctx, text+` ORDER BY ev.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]followupEvent{}
	for rows.Next() {
		var entry int64
		var ev followupEvent
		var at, detail string
		var status sql.NullString
		if err := rows.Scan(&entry, &ev.id, &at, &ev.actor, &ev.kind, &status, &detail); err != nil {
			return nil, err
		}
		ev.day, ev.status = t.dayOf(at), Status(status.String)
		if ev.kind == EventFollowupSnoozed || ev.kind == EventFollowupRecorded || ev.kind == EventNoteAdded {
			var d struct {
				Days   int   `json:"days"`
				Undoes int64 `json:"undoes"`
			}
			_ = json.Unmarshal([]byte(detail), &d)
			ev.days, ev.undoes = d.Days, d.Undoes
		}
		out[entry] = append(out[entry], ev)
	}
	return out, rows.Err()
}

// appliedCadence is where an applied row stands.
type appliedCadence struct {
	recorded int
	due      string // the next follow-up's day
	lastDone string // the day of the latest recorded follow-up
}

// inForce leaves out the undo events and the follow-ups, snoozes and notes
// they take back, so the cadence replays as if those were never recorded.
func inForce(evs []followupEvent) []followupEvent {
	undone := map[int64]bool{}
	for _, ev := range evs {
		if ev.undoes != 0 {
			undone[ev.id], undone[ev.undoes] = true, true
		}
	}
	out := make([]followupEvent, 0, len(evs))
	for _, ev := range evs {
		if !undone[ev.id] {
			out = append(out, ev)
		}
	}
	return out
}

// cadenceOf replays the row's follow-up events in force: a recorded follow-up
// sets the next due day to the repeat interval after it, and a snooze pushes
// the due day to N days past the later of the due day and the day of the snooze.
func cadenceOf(e Entry, evs []followupEvent, pol workspace.Followup) appliedCadence {
	c := appliedCadence{due: addDays(e.AppliedAt, pol.FirstAfterDays)}
	for _, ev := range inForce(evs) {
		switch ev.kind {
		case EventFollowupRecorded:
			c.recorded++
			c.lastDone = ev.day
			c.due = addDays(ev.day, pol.RepeatEveryDays)
		case EventFollowupSnoozed:
			base := ev.day
			if c.due > base {
				base = c.due
			}
			c.due = addDays(base, ev.days)
		}
	}
	return c
}

// pending reports whether a further follow-up may still be suggested.
func (c appliedCadence) pending(pol workspace.Followup) bool {
	return pol.MaxFollowups > 0 && c.recorded < pol.MaxFollowups
}

// Followups computes the suggestions for the open, non-archived rows on the
// injected clock's calendar day. With dueOnly it leaves out a follow-up that
// is not due yet; a row whose cadence is exhausted is always listed. It reads
// only; nothing is stored and nothing is sent.
func (t *Tracker) Followups(ctx context.Context, pol workspace.Followup, dueOnly bool) ([]Suggestion, error) {
	tx, err := t.s.Read().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := queryEntries(ctx, tx, `SELECT `+entryColumns+` FROM tracker_entries WHERE archived_at IS NULL AND status IN ('applied', 'responded') ORDER BY id`)
	if err != nil {
		return nil, err
	}
	events, err := t.loadFollowupEvents(ctx, tx, 0)
	if err != nil {
		return nil, err
	}
	today := t.today()
	out := []Suggestion{}
	for _, e := range rows {
		s := Suggestion{ID: e.ID, Company: e.Company, Role: e.Role, Status: e.Status, Version: e.Version}
		switch e.Status {
		case Applied:
			out = append(out, t.appliedSuggestions(s, e, events[e.ID], pol, today, dueOnly)...)
		case Responded:
			if due, ok := replyCheckDue(events[e.ID]); ok && due <= today {
				s.Kind, s.Due = KindReplyCheck, due
				out = append(out, s)
			}
		}
	}
	return out, nil
}

func (t *Tracker) appliedSuggestions(s Suggestion, e Entry, evs []followupEvent, pol workspace.Followup, today string, dueOnly bool) []Suggestion {
	if pol.MaxFollowups == 0 || e.AppliedAt == "" {
		return nil
	}
	c := cadenceOf(e, evs, pol)
	if c.pending(pol) {
		if dueOnly && c.due > today {
			return nil
		}
		s.Kind, s.Number, s.Due = KindFollowUp, c.recorded+1, c.due
		return []Suggestion{s}
	}
	// A row past the close day is listed once, as consider_closing: it names
	// the one thing left to do, and a second card would offer the same Close.
	if closeOn := addDays(e.AppliedAt, pol.CloseAfterDays); closeOn <= today {
		s.Kind, s.Due = KindConsiderClosing, closeOn
		return []Suggestion{s}
	}
	s.Kind = KindCadenceExhausted
	return []Suggestion{s}
}

// replyCheckDue finds the response that put the row into responded and, when
// the user has done nothing on the row since (an undone press is nothing), the
// day the nudge is due.
func replyCheckDue(evs []followupEvent) (string, bool) {
	evs = inForce(evs)
	response := -1
	for i, ev := range evs {
		switch ev.kind {
		case EventCreated, EventStatusChanged, EventStatusCorrected:
			if ev.status == Responded {
				response = i
			}
		}
	}
	if response < 0 {
		return "", false
	}
	for _, ev := range evs[response+1:] {
		if ev.actor == ActorUser {
			return "", false
		}
	}
	return addDays(evs[response].day, replyCheckAfterDays), true
}

// openApplied loads the row and its follow-up history inside a write
// transaction and refuses a row the cadence does not cover, or one that moved
// past expectedVersion when that is positive.
func (t *Tracker) openApplied(ctx context.Context, tx *sql.Tx, id int64, actor Actor, expectedVersion int, pol workspace.Followup) (Entry, appliedCadence, error) {
	e, events, err := t.appliedEvents(ctx, tx, id, actor, expectedVersion)
	if err != nil {
		return e, appliedCadence{}, err
	}
	c := cadenceOf(e, events, pol)
	if !c.pending(pol) {
		return e, c, fmt.Errorf("%w: row %d has no follow-up pending (max_followups is %d, %d recorded)", ErrInvalid, id, pol.MaxFollowups, c.recorded)
	}
	return e, c, nil
}

func (t *Tracker) appliedEvents(ctx context.Context, tx *sql.Tx, id int64, actor Actor, expectedVersion int) (Entry, []followupEvent, error) {
	if !actor.valid() {
		return Entry{}, nil, fmt.Errorf("%w: unknown actor %q", ErrInvalid, actor)
	}
	e, err := getEntry(ctx, tx, id)
	if err != nil {
		return Entry{}, nil, err
	}
	if err := checkVersion(e, expectedVersion); err != nil {
		return e, nil, err
	}
	switch {
	case e.Status != Applied || e.AppliedAt == "":
		return e, nil, fmt.Errorf("%w: follow-ups are tracked for applied rows; row %d is %s", ErrInvalid, id, e.Status)
	case e.ArchivedAt != "":
		return e, nil, fmt.Errorf("%w: row %d is archived", ErrInvalid, id)
	}
	events, err := t.loadFollowupEvents(ctx, tx, id)
	return e, events[id], err
}

func (t *Tracker) touch(ctx context.Context, tx *sql.Tx, id int64) (Entry, error) {
	if _, err := tx.ExecContext(ctx, `UPDATE tracker_entries SET version = version + 1, updated_at = ? WHERE id = ?`, t.stamp(), id); err != nil {
		return Entry{}, err
	}
	return getEntry(ctx, tx, id)
}

// RecordFollowup records that the user followed up on an applied row. It
// sends nothing; it moves the next suggestion to the repeat interval from
// today. A positive expectedVersion refuses a row that moved since it was read.
func (t *Tracker) RecordFollowup(ctx context.Context, id int64, actor Actor, expectedVersion int, pol workspace.Followup) (Entry, error) {
	var out Entry
	err := t.s.Write(ctx, func(tx *sql.Tx) error {
		_, c, err := t.openApplied(ctx, tx, id, actor, expectedVersion, pol)
		if err != nil {
			return err
		}
		if err := t.addEvent(ctx, tx, id, actor, EventFollowupRecorded, "", "", map[string]any{"number": c.recorded + 1}); err != nil {
			return err
		}
		out, err = t.touch(ctx, tx, id)
		return err
	})
	return out, err
}

// SnoozeFollowup pushes the row's next follow-up N days past the later of its
// due day and today.
func (t *Tracker) SnoozeFollowup(ctx context.Context, id int64, actor Actor, days, expectedVersion int, pol workspace.Followup) (Entry, error) {
	if days < 1 {
		return Entry{}, fmt.Errorf("%w: a snooze is a whole number of days, at least 1", ErrInvalid)
	}
	var out Entry
	err := t.s.Write(ctx, func(tx *sql.Tx) error {
		_, c, err := t.openApplied(ctx, tx, id, actor, expectedVersion, pol)
		if err != nil {
			return err
		}
		base := t.today()
		if c.due > base {
			base = c.due
		}
		if err := t.addEvent(ctx, tx, id, actor, EventFollowupSnoozed, "", "", map[string]any{"days": days, "due": addDays(base, days)}); err != nil {
			return err
		}
		out, err = t.touch(ctx, tx, id)
		return err
	})
	return out, err
}

// UndoFollowup takes back the row's latest follow-up or snooze still in force.
// Events are append-only, so it appends one of the same kind naming the event
// it undoes: the history keeps both, and the cadence, the follow-up list and
// the drafts read the row as if it was never recorded.
func (t *Tracker) UndoFollowup(ctx context.Context, id int64, actor Actor, expectedVersion int) (Entry, error) {
	var out Entry
	err := t.s.Write(ctx, func(tx *sql.Tx) error {
		_, events, err := t.appliedEvents(ctx, tx, id, actor, expectedVersion)
		if err != nil {
			return err
		}
		live := inForce(events)
		for i := len(live) - 1; i >= 0; i-- {
			if ev := live[i]; ev.kind == EventFollowupRecorded || ev.kind == EventFollowupSnoozed {
				if err := t.addEvent(ctx, tx, id, actor, ev.kind, "", "", map[string]any{"undoes": ev.id}); err != nil {
					return err
				}
				out, err = t.touch(ctx, tx, id)
				return err
			}
		}
		return fmt.Errorf("%w: row %d has no follow-up or snooze to undo", ErrInvalid, id)
	})
	return out, err
}

// Undoes is the event an undo event takes back, or 0.
func (ev Event) Undoes() int64 {
	var d struct {
		Undoes int64 `json:"undoes"`
	}
	_ = json.Unmarshal(ev.Detail, &d)
	return d.Undoes
}

// FormatSuggestion is the one-line text the CLI prints.
func FormatSuggestion(s Suggestion) string {
	var b strings.Builder
	fmt.Fprintf(&b, "row %d %s: %s, %s", s.ID, s.Kind, s.Company, s.Role)
	if s.Kind == KindFollowUp {
		fmt.Fprintf(&b, " (follow-up %d)", s.Number)
	}
	if s.Due != "" {
		fmt.Fprintf(&b, ", due %s", s.Due)
	}
	return b.String()
}
