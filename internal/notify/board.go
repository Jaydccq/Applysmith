// Clean-room: docs/provenance.md#CR-RUNS-BOARD

package notify

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"applysmith/internal/store"
)

const timeLayout = "2006-01-02T15:04:05.000Z"

func ts(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTS(s string) time.Time {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func nullTS(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return ts(t)
}

// Observer is told what the board changed. The policy is the observer: the
// board itself never decides what is announced.
type Observer interface {
	// Changed reports an entry after a change of state or reason, or a new
	// entry (before is nil). Progress inside a state is not reported.
	Changed(ctx context.Context, before *Entry, after Entry)
	// Retired reports an entry whose announcements must be withdrawn because
	// the user acknowledged it or it was deleted.
	Retired(ctx context.Context, e Entry)
}

// Topics are the board topics a change advances.
const (
	TopicRuns  = "runs"
	TopicInbox = "inbox"
)

// BoardOptions wire a board.
type BoardOptions struct {
	Now      func() time.Time
	Log      *slog.Logger
	Observer Observer
	// Changed is called after a change is committed, with the topics to
	// advance; a read that follows it already sees the change.
	Changed func(topics ...string)
	// Outcome says in one sentence what a mirrored run that ended came to,
	// read from the record its subsystem wrote (an evaluation's score and
	// report, a scan's counts); "" leaves the failure words.
	Outcome func(ctx context.Context, kind Kind, runID string) string
	// ScanTitle is the readable name of a scan source ("Company job boards"
	// for registry), from the side that knows the sources; nil, or "" for a
	// source, titles the scan by the source's id.
	ScanTitle func(source string) string
}

// Board is the runs board over the workspace database.
type Board struct {
	st  *store.Store
	o   BoardOptions
	obs Observer

	// swept is when SweepStale last ran and resumed when it last ran after a
	// pause of the process.
	sweepMu        sync.Mutex
	swept, resumed time.Time
	// runsSeen is the rowid of the newest run record at the last SyncRuns
	// that reached every record; zero before the first.
	runsSeen atomic.Int64
}

// NewBoard returns a board.
func NewBoard(st *store.Store, o BoardOptions) *Board {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &Board{st: st, o: o, obs: o.Observer}
}

// SetObserver replaces the observer; the server installs the policy once it
// exists.
func (b *Board) SetObserver(o Observer) { b.obs = o }

// SetOutcome installs the words of a run's outcome once the subsystems that
// own the records exist.
func (b *Board) SetOutcome(f func(ctx context.Context, kind Kind, runID string) string) {
	b.o.Outcome = f
}

func newID() string {
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	return "ent_" + hex.EncodeToString(raw[:])
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	cut := r[:n-1]
	// Never end in the middle of a word: back off to the last space when the
	// cut falls inside a word and a space exists in the second half.
	if !unicode.IsSpace(r[n-1]) {
		for i := len(cut) - 1; i >= n/2; i-- {
			if unicode.IsSpace(cut[i]) {
				cut = cut[:i]
				break
			}
		}
	}
	return strings.TrimRight(strings.TrimSpace(string(cut)), ",;:") + "…"
}

const entryColumns = `id, kind, origin, title, stage, detail, state, reason, outcome, batch_id, link, ref, focus_tab, revision, epoch,
	started_at, updated_at, state_at, finished_at, seen_at, acked_at, ref_updated`

type scanner interface{ Scan(dest ...any) error }

func scanEntry(r scanner) (Entry, error) {
	var e Entry
	var kind, origin, state string
	var started, updated, stateAt string
	var finished, seen, acked sql.NullString
	err := r.Scan(&e.ID, &kind, &origin, &e.Title, &e.Stage, &e.Detail, &state, &e.Reason, &e.Outcome, &e.BatchID, &e.Link, &e.Ref,
		&e.FocusTab, &e.Revision, &e.Epoch, &started, &updated, &stateAt, &finished, &seen, &acked, &e.RefUpdated)
	if err != nil {
		return Entry{}, err
	}
	e.Kind, e.Origin, e.State = Kind(kind), Origin(origin), State(state)
	e.StartedAt, e.UpdatedAt, e.StateAt = parseTS(started), parseTS(updated), parseTS(stateAt)
	e.FinishedAt, e.SeenAt, e.AckedAt = parseTS(finished.String), parseTS(seen.String), parseTS(acked.String)
	return e, nil
}

func getEntry(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (Entry, error) {
	e, err := scanEntry(q.QueryRowContext(ctx, `SELECT `+entryColumns+` FROM board_entries WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, ErrNotFound
	}
	return e, err
}

func saveEntry(ctx context.Context, tx *sql.Tx, e Entry) error {
	_, err := tx.ExecContext(ctx, `UPDATE board_entries SET kind=?, origin=?, title=?, stage=?, detail=?, state=?, reason=?, outcome=?, batch_id=?,
		link=?, ref=?, focus_tab=?, revision=?, epoch=?, updated_at=?, state_at=?, finished_at=?, seen_at=?, acked_at=?, ref_updated=? WHERE id=?`,
		string(e.Kind), string(e.Origin), e.Title, e.Stage, e.Detail, string(e.State), e.Reason, e.Outcome, e.BatchID, e.Link, e.Ref, e.FocusTab,
		e.Revision, e.Epoch, ts(e.UpdatedAt), ts(e.StateAt), nullTS(e.FinishedAt), nullTS(e.SeenAt), nullTS(e.AckedAt), e.RefUpdated, e.ID)
	return err
}

// OpenArgs start an entry. A blank ID makes one up.
type OpenArgs struct {
	ID       string
	Kind     Kind
	Origin   Origin
	Title    string
	Stage    string
	Detail   string
	BatchID  string
	Link     string
	Ref      string
	FocusTab string
	// RefUpdated is the stamp of the run record being mirrored.
	RefUpdated string
}

// Open creates an entry in `working`. Opening an id that exists returns the
// entry as it is, so a producer may open on every start.
func (b *Board) Open(ctx context.Context, a OpenArgs) (Entry, error) {
	if a.ID == "" {
		a.ID = newID()
	}
	now := b.o.Now()
	e := Entry{ID: a.ID, Kind: a.Kind, Origin: a.Origin, Title: clip(a.Title, MaxTitle), Stage: a.Stage, Detail: clip(a.Detail, MaxDetail),
		State: Working, BatchID: a.BatchID, Link: a.Link, Ref: a.Ref, FocusTab: a.FocusTab, Revision: 1, Epoch: 1,
		StartedAt: now, UpdatedAt: now, StateAt: now, RefUpdated: a.RefUpdated}
	var created bool
	err := b.st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO board_entries (id, kind, origin, title, stage, detail, state, reason, outcome, batch_id, link, ref, focus_tab,
			revision, epoch, started_at, updated_at, state_at, ref_updated) VALUES (?, ?, ?, ?, ?, ?, 'working', '', '', ?, ?, ?, ?, 1, 1, ?, ?, ?, ?)
			ON CONFLICT(id) DO NOTHING`, e.ID, string(e.Kind), string(e.Origin), e.Title, e.Stage, e.Detail, e.BatchID, e.Link, e.Ref, e.FocusTab,
			ts(now), ts(now), ts(now), e.RefUpdated)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		created = n == 1
		if err == nil && !created {
			e, err = getEntry(ctx, tx, e.ID)
		}
		return err
	})
	if err != nil {
		return Entry{}, err
	}
	if created {
		b.committed(ctx, nil, e)
	}
	return e, nil
}

// Patch changes the display fields of an entry without a state change. A nil
// field is left alone.
type Patch struct {
	Title      *string
	Stage      *string
	Detail     *string
	Link       *string
	FocusTab   *string
	RefUpdated *string
	// Origin corrects who started the run, for a record whose origin was
	// noted after its entry opened.
	Origin *Origin
}

// Update changes display fields. The revision grows only when something
// changed, and nothing is announced: progress inside a state is silent.
func (b *Board) Update(ctx context.Context, id string, p Patch) (Entry, error) {
	var after Entry
	changed := false
	err := b.st.Write(ctx, func(tx *sql.Tx) error {
		e, err := getEntry(ctx, tx, id)
		if err != nil {
			return err
		}
		set := func(dst *string, v *string, limit int) {
			if v == nil {
				return
			}
			s := *v
			if limit > 0 {
				s = clip(s, limit)
			}
			if s != *dst {
				*dst, changed = s, true
			}
		}
		set(&e.Title, p.Title, MaxTitle)
		set(&e.Stage, p.Stage, 0)
		set(&e.Detail, p.Detail, MaxDetail)
		set(&e.Link, p.Link, 0)
		set(&e.FocusTab, p.FocusTab, 0)
		if p.Origin != nil && *p.Origin != e.Origin {
			e.Origin, changed = *p.Origin, true
		}
		if p.RefUpdated != nil && *p.RefUpdated != e.RefUpdated {
			// The stamp of the mirrored record is bookkeeping, not a change the
			// user can see.
			e.RefUpdated = *p.RefUpdated
			if !changed {
				after = e
				return saveEntry(ctx, tx, e)
			}
		}
		after = e
		if !changed {
			return nil
		}
		e.Revision++
		e.UpdatedAt = b.o.Now()
		after = e
		return saveEntry(ctx, tx, e)
	})
	if err != nil {
		return Entry{}, err
	}
	if changed {
		b.fire(TopicRuns)
		if after.Waiting() {
			b.fire(TopicInbox)
		}
	}
	return after, nil
}

// Touch records that a working entry is alive.
func (b *Board) Touch(ctx context.Context, id string) error {
	return b.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE board_entries SET updated_at = ? WHERE id = ? AND state = 'working'`, ts(b.o.Now()), id)
		return err
	})
}

// MoveArgs describe a move between states.
type MoveArgs struct {
	Reason   string // required for needs-you and blocked
	Outcome  string // required for done
	Detail   string // replaces the detail line when not empty
	FocusTab string
	// If, when set, is checked against the entry inside the same write; a move
	// whose condition fails changes nothing and returns ErrStateChanged. It is
	// how two presses of one button cannot both win.
	If func(Entry) bool
}

// Move changes an entry's state. A move outside the permitted set, or a reason
// that does not fit the state, is rejected and logged, and the entry stays as
// it was. Moving a waiting entry to the same state with another reason is a
// change of reason, which clears the marks like a change of state.
func (b *Board) Move(ctx context.Context, id string, to State, a MoveArgs) (Entry, error) {
	var before, after Entry
	var moved bool
	err := b.st.Write(ctx, func(tx *sql.Tx) error {
		e, err := getEntry(ctx, tx, id)
		if err != nil {
			return err
		}
		before = e
		if a.If != nil && !a.If(e) {
			return ErrStateChanged
		}
		if !validReason(to, a.Reason) {
			return fmt.Errorf("%w: %q for %s", ErrBadReason, a.Reason, to)
		}
		if to == Done && !validOutcome(a.Outcome) {
			return fmt.Errorf("%w: outcome %q", ErrBadReason, a.Outcome)
		}
		sameWaiting := to == e.State && e.Waiting() && a.Reason != e.Reason
		if !CanMove(e.State, to) && !sameWaiting {
			return fmt.Errorf("%w: %s to %s", ErrIllegalTransition, e.State, to)
		}
		now := b.o.Now()
		e.State, e.Reason, e.Outcome = to, a.Reason, a.Outcome
		if to != Done {
			e.Outcome = ""
		}
		if a.Detail != "" {
			e.Detail = clip(a.Detail, MaxDetail)
		}
		if a.FocusTab != "" {
			e.FocusTab = a.FocusTab
		}
		e.Revision++
		e.Epoch++
		e.UpdatedAt, e.StateAt = now, now
		e.SeenAt, e.AckedAt = time.Time{}, time.Time{}
		e.FinishedAt = time.Time{}
		if to == Done {
			e.FinishedAt = now
		}
		after, moved = e, true
		return saveEntry(ctx, tx, e)
	})
	if err != nil {
		if errors.Is(err, ErrIllegalTransition) || errors.Is(err, ErrBadReason) {
			b.o.Log.Warn("a board change was rejected", "entry", id, "error", err.Error())
		}
		return Entry{}, err
	}
	if moved {
		b.committed(ctx, &before, after)
	}
	return after, nil
}

// Seen marks an entry seen, which happens when its page opens.
func (b *Board) Seen(ctx context.Context, id string) (Entry, error) {
	return b.mark(ctx, id, func(e *Entry, now time.Time) bool {
		if !e.SeenAt.IsZero() {
			return false
		}
		e.SeenAt = now
		return true
	}, false)
}

// Ack acknowledges a finished or waiting entry. It withdraws the entry's
// notifications and, for a waiting entry, takes it out of the inbox without
// changing its state.
func (b *Board) Ack(ctx context.Context, id string) (Entry, error) {
	return b.mark(ctx, id, func(e *Entry, now time.Time) bool {
		if e.Acked() {
			return false
		}
		e.AckedAt = now
		return true
	}, true)
}

func (b *Board) mark(ctx context.Context, id string, fn func(*Entry, time.Time) bool, ack bool) (Entry, error) {
	var after Entry
	var changed bool
	err := b.st.Write(ctx, func(tx *sql.Tx) error {
		e, err := getEntry(ctx, tx, id)
		if err != nil {
			return err
		}
		if ack && e.State != Done && !e.Waiting() {
			return ErrNotAckable
		}
		now := b.o.Now()
		if changed = fn(&e, now); changed {
			e.Revision++
			e.UpdatedAt = now
			if err := saveEntry(ctx, tx, e); err != nil {
				return err
			}
		}
		after = e
		return nil
	})
	if err != nil {
		return Entry{}, err
	}
	if changed {
		b.fire(TopicRuns, TopicInbox)
		if ack && b.obs != nil {
			b.obs.Retired(ctx, after)
		}
	}
	return after, nil
}

// Get returns one entry.
func (b *Board) Get(ctx context.Context, id string) (Entry, error) {
	return getEntry(ctx, b.st.Read(), id)
}

// Delete removes an entry and withdraws what was announced for it. The record
// of the owning subsystem is not touched.
func (b *Board) Delete(ctx context.Context, id string) error {
	var e Entry
	err := b.st.Write(ctx, func(tx *sql.Tx) error {
		var err error
		if e, err = getEntry(ctx, tx, id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM board_entries WHERE id = ?`, id)
		return err
	})
	if err != nil {
		return err
	}
	b.fire(TopicRuns, TopicInbox)
	if b.obs != nil {
		b.obs.Retired(ctx, e)
	}
	return nil
}

// committed runs the after-commit steps of a state change.
func (b *Board) committed(ctx context.Context, before *Entry, after Entry) {
	b.fire(TopicRuns, TopicInbox)
	if b.obs != nil {
		b.obs.Changed(ctx, before, after)
	}
}

func (b *Board) fire(topics ...string) {
	if b.o.Changed != nil {
		b.o.Changed(topics...)
	}
}
