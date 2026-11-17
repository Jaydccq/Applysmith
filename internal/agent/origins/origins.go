// Package origins remembers which conversation started a run and, for an
// application run, the request it was started from. A run's end is told to its
// conversation, the cap on agent-started runs counts them, and a resumed
// application run decides its submit again from the request recorded here.
package origins

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"applysmith/internal/notify"
	"applysmith/internal/store"
)

const timeLayout = "2006-01-02T15:04:05.000Z"

// Retention is how long an origin is kept; the board keeps its own entries for
// at most 30 days.
const Retention = 45 * 24 * time.Hour

// Origin ties a run to the conversation that started it.
type Origin struct {
	RunID     string
	SessionID string
	Tool      string
	CreatedAt time.Time
	// Told is the end of the run (EndOf) the conversation was last told, empty
	// while the run goes on.
	Told string
}

// ApplyRequest is what an application run was started from: the facts its
// submit gate decides from. UserMessage is the user's own message of the turn.
type ApplyRequest struct {
	RunID, TurnID, Caller, Tool string
	Submit, Explicit            bool
	// Outside says the turn had read outside content; Confirmed says the call
	// ran on an approved confirmation card.
	Outside, Confirmed bool
	UserMessage        string
	URL, EmployerURL   string
	Company, Role      string
	Named, Resubmit    bool
	PressedBy          string
	PressedAt          time.Time
}

// ErrNotFound is a run with no recorded origin.
var ErrNotFound = errors.New("origins: no such run")

// Store is the table of origins.
type Store struct {
	db  *store.Store
	now func() time.Time
}

// New returns a store.
func New(db *store.Store, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, now: now}
}

func (s *Store) stamp() string { return s.now().UTC().Format(timeLayout) }

// Record notes that a conversation started a run. Noting a run twice keeps the
// first.
func (s *Store) Record(ctx context.Context, runID, sessionID, tool string) error {
	if runID == "" || sessionID == "" {
		return nil
	}
	return s.db.Write(ctx, func(tx *sql.Tx) error { return s.record(ctx, tx, runID, sessionID, tool) })
}

func (s *Store) record(ctx context.Context, tx *sql.Tx, runID, sessionID, tool string) error {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO agent_run_origins (run_id, session_id, tool, created_at) VALUES (?, ?, ?, ?)`,
		runID, sessionID, tool, s.stamp())
	return err
}

// RecordApply notes an application run with the request it was started from.
func (s *Store) RecordApply(ctx context.Context, sessionID string, r ApplyRequest) error {
	if r.RunID == "" || sessionID == "" {
		return nil
	}
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		if err := s.record(ctx, tx, r.RunID, sessionID, r.Tool); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO agent_apply_requests
			(run_id, turn_id, caller, tool, submit, explicit, outside, confirmed, user_message, url, employer_url, company, role, named, resubmit)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.RunID, r.TurnID, r.Caller, r.Tool, b2i(r.Submit), b2i(r.Explicit), b2i(r.Outside), b2i(r.Confirmed), r.UserMessage,
			r.URL, r.EmployerURL, r.Company, r.Role, b2i(r.Named), b2i(r.Resubmit))
		return err
	})
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Get reads the origin of a run.
func (s *Store) Get(ctx context.Context, runID string) (Origin, error) {
	var o Origin
	var at string
	err := s.db.Read().QueryRowContext(ctx, `SELECT run_id, session_id, tool, created_at, told FROM agent_run_origins WHERE run_id = ?`, runID).
		Scan(&o.RunID, &o.SessionID, &o.Tool, &at, &o.Told)
	if errors.Is(err, sql.ErrNoRows) {
		return Origin{}, ErrNotFound
	}
	if err != nil {
		return Origin{}, err
	}
	o.CreatedAt, _ = time.Parse(timeLayout, at)
	return o, nil
}

// Apply reads the request an application run was started from.
func (s *Store) Apply(ctx context.Context, runID string) (ApplyRequest, error) {
	var r ApplyRequest
	var submit, explicit, outside, confirmed, named, resubmit int
	var pressedAt sql.NullString
	err := s.db.Read().QueryRowContext(ctx, `SELECT run_id, turn_id, caller, tool, submit, explicit, outside, confirmed, user_message, url, employer_url,
		company, role, named, resubmit, pressed_by, pressed_at FROM agent_apply_requests WHERE run_id = ?`, runID).
		Scan(&r.RunID, &r.TurnID, &r.Caller, &r.Tool, &submit, &explicit, &outside, &confirmed, &r.UserMessage, &r.URL, &r.EmployerURL,
			&r.Company, &r.Role, &named, &resubmit, &r.PressedBy, &pressedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ApplyRequest{}, ErrNotFound
	}
	if err != nil {
		return ApplyRequest{}, err
	}
	r.Submit, r.Explicit, r.Outside, r.Confirmed, r.Named, r.Resubmit = submit == 1, explicit == 1, outside == 1, confirmed == 1, named == 1, resubmit == 1
	r.PressedAt, _ = time.Parse(timeLayout, pressedAt.String)
	return r, nil
}

// Pressed records who pressed Done on a run and when.
func (s *Store) Pressed(ctx context.Context, runID, by string) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE agent_apply_requests SET pressed_by = ?, pressed_at = ? WHERE run_id = ?`, by, s.stamp(), runID)
		return err
	})
}

// Confirm records that the user confirmed the submit of a run whose filled
// form waited at Submit: its request now submits, explicitly and confirmed, so
// a later Done decides from it as from an approved card.
func (s *Store) Confirm(ctx context.Context, runID string) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE agent_apply_requests SET submit = 1, explicit = 1, confirmed = 1 WHERE run_id = ?`, runID)
		return err
	})
}

// Active counts the runs conversations started that are still going: a board
// entry that is working, or a run record that is queued or running.
func (s *Store) Active(ctx context.Context) (int, error) {
	var n int
	err := s.db.Read().QueryRowContext(ctx, `SELECT count(*) FROM agent_run_origins o
		WHERE EXISTS (SELECT 1 FROM board_entries b WHERE b.id = o.run_id AND b.state = 'working')
		   OR EXISTS (SELECT 1 FROM runs r WHERE r.id = o.run_id AND r.status IN ('queued', 'running'))`).Scan(&n)
	return n, err
}

// RunsOf lists the runs a conversation started, oldest first.
func (s *Store) RunsOf(ctx context.Context, sessionID string) ([]string, error) {
	rows, err := s.db.Read().QueryContext(ctx, `SELECT run_id FROM agent_run_origins WHERE session_id = ? ORDER BY created_at, run_id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ActiveIn counts what a conversation's delegated task waits for: the runs the
// conversation started that are still going, as Active counts them for every
// conversation, the ends of its runs it was not told yet, the evaluations its
// scans queued (their scan's batch on the board) that are still working, and
// the starts it queued that have not begun. A scan ends with its evaluations
// queued, and a task that went on then would find nothing to do. A run's end is one write on the board and its
// note a later one; a task that went on between them took its turn with that
// end untold. Active leaves untold ends and queued starts out: the cap counts
// runs going, and a queued start waits for that count to drop.
func (s *Store) ActiveIn(ctx context.Context, sessionID string) (int, error) {
	var n int
	err := s.db.Read().QueryRowContext(ctx, `SELECT (SELECT count(*) FROM agent_run_origins o WHERE o.session_id = ? AND (
		   EXISTS (SELECT 1 FROM board_entries b WHERE b.id = o.run_id AND (b.state = 'working' OR `+untold+`))
		OR EXISTS (SELECT 1 FROM runs r WHERE r.id = o.run_id AND r.status IN ('queued', 'running'))))
		+ (SELECT count(*) FROM agent_run_origins o JOIN board_entries e ON e.batch_id = '`+notify.ScanBatch+`' || o.run_id
			WHERE o.session_id = ? AND e.state = 'working')
		+ (SELECT count(*) FROM agent_queued_starts q WHERE q.session_id = ?)`, sessionID, sessionID, sessionID).Scan(&n)
	return n, err
}

// EndOf is how a run's board entry stands once the run stopped going: its
// state, reason and outcome. A conversation is told each end of a run it
// started once.
func EndOf(e notify.Entry) string { return string(e.State) + "|" + e.Reason + "|" + e.Outcome }

// untold holds when the board entry b stands at an end the origin o was not
// told; the end is EndOf in SQL.
const untold = `b.state IN ('done', 'needs-you', 'blocked') AND o.told <> b.state || '|' || b.reason || '|' || b.outcome`

// Untold lists the runs whose board entry stands at an end their conversation
// was not told yet and that came at or before by, in the order they came.
func (s *Store) Untold(ctx context.Context, by time.Time) ([]Origin, error) {
	rows, err := s.db.Read().QueryContext(ctx, `SELECT o.run_id, o.session_id, o.tool, o.created_at, o.told FROM agent_run_origins o
		JOIN board_entries b ON b.id = o.run_id WHERE `+untold+` AND b.state_at <= ? ORDER BY b.state_at, o.run_id`, by.UTC().Format(timeLayout))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Origin
	for rows.Next() {
		var o Origin
		var at string
		if err := rows.Scan(&o.RunID, &o.SessionID, &o.Tool, &at, &o.Told); err != nil {
			return nil, err
		}
		o.CreatedAt, _ = time.Parse(timeLayout, at)
		out = append(out, o)
	}
	return out, rows.Err()
}

// Told records the end of a run (EndOf) its conversation was told; empty when
// the run goes on again, so its next end is told even when it is the same.
func (s *Store) Told(ctx context.Context, runID, end string) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE agent_run_origins SET told = ? WHERE run_id = ?`, end, runID)
		return err
	})
}

// QueuedStart is a start that waits for one of the agent's runs to finish: the
// board line it shows as, the conversation it was asked in, and its label.
type QueuedStart struct {
	EntryID, SessionID, Label string
}

// Queue records a waiting start; recording it twice keeps the first.
func (s *Store) Queue(ctx context.Context, q QueuedStart) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO agent_queued_starts (entry_id, session_id, label, created_at) VALUES (?, ?, ?, ?)`,
			q.EntryID, q.SessionID, q.Label, s.stamp())
		return err
	})
}

// Unqueue forgets a start that began, was refused, gave up or was told.
func (s *Store) Unqueue(ctx context.Context, entryID string) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM agent_queued_starts WHERE entry_id = ?`, entryID)
		return err
	})
}

// QueuedStarts lists the waiting starts, oldest first.
func (s *Store) QueuedStarts(ctx context.Context) ([]QueuedStart, error) {
	rows, err := s.db.Read().QueryContext(ctx, `SELECT entry_id, session_id, label FROM agent_queued_starts ORDER BY created_at, entry_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueuedStart
	for rows.Next() {
		var q QueuedStart
		if err := rows.Scan(&q.EntryID, &q.SessionID, &q.Label); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// Retain deletes the origins older than Retention.
func (s *Store) Retain(ctx context.Context) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM agent_run_origins WHERE created_at < ?`, s.now().Add(-Retention).UTC().Format(timeLayout))
		return err
	})
}

// Request reads the origin of a run and the request it was started from; a run
// that is not an application run of a conversation is ErrNotFound.
func (s *Store) Request(ctx context.Context, runID string) (ApplyRequest, Origin, error) {
	r, err := s.Apply(ctx, runID)
	if err != nil {
		return ApplyRequest{}, Origin{}, err
	}
	o, err := s.Get(ctx, runID)
	return r, o, err
}
