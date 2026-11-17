// Clean-room: docs/provenance.md#CR-AGENT-GOALS

package goals

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"
)

// CheckinWindow is how long a slot stays due, so a machine that was asleep at
// the slot still sends it on waking.
const CheckinWindow = 3 * time.Hour

// Checkin is one due check-in. It is built from the goal record alone.
type Checkin struct {
	GoalID string
	Slot   time.Time
	// Key is stable per goal and slot, so announcing it twice sends one message.
	Key   string
	Title string
	Body  string
}

// LatestSlot is the latest check-in slot of the cadence at or before now, or
// the zero time when the cadence is empty.
func LatestSlot(c Cadence, now time.Time, loc *time.Location) time.Time {
	if c.Empty() {
		return time.Time{}
	}
	local := now.In(loc)
	for back := 0; back <= 7; back++ {
		d := local.AddDate(0, 0, -back)
		if at, ok := slotOn(c, d, loc); ok && !at.After(now) {
			return at.UTC()
		}
	}
	return time.Time{}
}

// NextSlot is the first slot after now, or the zero time.
func NextSlot(c Cadence, now time.Time, loc *time.Location) time.Time {
	if c.Empty() {
		return time.Time{}
	}
	local := now.In(loc)
	for ahead := 0; ahead <= 7; ahead++ {
		d := local.AddDate(0, 0, ahead)
		if at, ok := slotOn(c, d, loc); ok && at.After(now) {
			return at.UTC()
		}
	}
	return time.Time{}
}

func slotOn(c Cadence, day time.Time, loc *time.Location) (time.Time, bool) {
	if !slices.Contains(c.Days, weekdays[day.Weekday()]) {
		return time.Time{}, false
	}
	t, err := time.Parse("15:04", c.Time)
	if err != nil {
		return time.Time{}, false
	}
	return time.Date(day.Year(), day.Month(), day.Day(), t.Hour(), t.Minute(), 0, 0, loc), true
}

// NextCheckin is the next slot of an active goal with a cadence, else zero.
func (s *Store) NextCheckin(g Goal) time.Time {
	if g.State != Active {
		return time.Time{}
	}
	return NextSlot(g.Cadence, s.now(), s.loc)
}

// DueCheckins claims and returns the check-ins due at now. Only the latest
// slot of a goal is due, only inside CheckinWindow, never one before the
// goal's creation or last resume, and never one already claimed. The claim is
// written before the caller sends anything, so a crash cannot repeat a slot.
func (s *Store) DueCheckins(ctx context.Context, now time.Time) ([]Checkin, error) {
	list, err := s.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	var out []Checkin
	for _, g := range list {
		slot := LatestSlot(g.Cadence, now, s.loc)
		if slot.IsZero() || now.Sub(slot) > CheckinWindow || slot.Before(g.ActiveSince) || !slot.After(g.LastSlot) {
			continue
		}
		var claimed bool
		err := s.db.Write(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, `UPDATE agent_goals SET last_slot = ? WHERE id = ? AND state = 'active' AND last_slot < ?`, stamp(slot), g.ID, stamp(slot))
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			claimed = n == 1
			return nil
		})
		if err != nil {
			return out, err
		}
		if claimed {
			out = append(out, s.compose(g, slot, now))
		}
	}
	return out, nil
}

func (s *Store) compose(g Goal, slot, now time.Time) Checkin {
	done, total := g.Progress()
	var parts []string
	if g.DueDate != "" {
		due := "Due " + g.DueDate
		if g.Overdue(now, s.loc) {
			due += " (overdue)"
		}
		parts = append(parts, due+".")
	}
	parts = append(parts, fmt.Sprintf("%d of %d steps done.", done, total))
	if st, pos := g.Next(); pos > 0 {
		parts = append(parts, fmt.Sprintf("Next, step %d: %s", pos, oneLine(st.Text)))
	}
	if g.Proposal != nil {
		parts = append(parts, "A plan change is waiting for your decision.")
	}
	return Checkin{GoalID: g.ID, Slot: slot, Key: "goal-checkin|" + g.ID + "|" + stamp(slot),
		Title: "Goal check-in: " + oneLine(g.Title), Body: strings.Join(parts, " ")}
}
