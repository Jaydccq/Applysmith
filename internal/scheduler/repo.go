package scheduler

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"applysmith/internal/store"
)

// Repo is the schedule store over the workspace database.
type Repo struct {
	st  *store.Store
	now func() time.Time
}

// NewRepo returns the repository. now is the clock used for stored stamps.
func NewRepo(st *store.Store, now func() time.Time) *Repo { return &Repo{st: st, now: now} }

const scheduleColumns = `id, name, job_kind, inputs_json, preset, enabled, missed_policy, dtstart, COALESCE(pack_id, ''), COALESCE(pack_key, ''),
	COALESCE(last_fired_at, ''), COALESCE(last_evaluated_at, ''), COALESCE(active_run_id, ''), COALESCE(last_result, ''),
	COALESCE(last_skip_reason, ''), created_at, updated_at,
	COALESCE((SELECT run_id FROM schedule_history h WHERE h.schedule_id = schedules.id AND h.run_id <> '' ORDER BY h.id DESC LIMIT 1), ''),
	COALESCE((SELECT COALESCE(started_at, created_at) ` + lastDecision + `), ''),
	COALESCE((SELECT reason ` + lastDecision + `), ''), COALESCE((SELECT detail ` + lastDecision + `), '')`

// lastDecision picks a schedule's newest decision from its history: the row
// its last result, that result's time, reason and detail come from.
const lastDecision = `FROM schedule_history h WHERE h.schedule_id = schedules.id
	AND h.outcome IN ('fired', 'manual', 'skipped', 'interrupted') ORDER BY h.id DESC LIMIT 1`

type scanner interface{ Scan(dest ...any) error }

func scanSchedule(row scanner) (Schedule, error) {
	var s Schedule
	var inputs, preset, dtstart, fired, evaluated, created, updated, resultAt string
	var enabled int
	if err := row.Scan(&s.ID, &s.Name, &s.Kind, &inputs, &preset, &enabled, &s.Policy, &dtstart, &s.PackID, &s.PackKey,
		&fired, &evaluated, &s.ActiveRunID, &s.LastResult, &s.LastSkipReason, &created, &updated, &s.LastRunID, &resultAt, &s.LastReason, &s.LastDetail); err != nil {
		return Schedule{}, err
	}
	s.Enabled = enabled == 1
	s.DTStart, s.LastFiredAt, s.LastEvaluatedAt = parseTS(dtstart), parseTS(fired), parseTS(evaluated)
	s.CreatedAt, s.UpdatedAt, s.LastResultAt = parseTS(created), parseTS(updated), parseTS(resultAt)
	if err := json.Unmarshal([]byte(inputs), &s.Inputs); err != nil || s.Inputs == nil {
		s.Inputs = map[string]any{}
	}
	// A hand-edited preset that no longer parses is kept as it is; evaluating
	// it reports the problem for this schedule alone.
	_ = json.Unmarshal([]byte(preset), &s.Preset)
	return s, nil
}

func marshal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func newRunID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return "run_" + hex.EncodeToString(b[:])
}

// Create stores a new schedule. Its cursor starts at the creation instant so
// the time before creation is never replayed.
func (r *Repo) Create(ctx context.Context, s Schedule) (Schedule, error) {
	now := r.now().UTC()
	s.CreatedAt, s.UpdatedAt = now, now
	if s.Enabled {
		s.LastFiredAt = now
	}
	err := r.st.Write(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM schedules`).Scan(&n); err != nil {
			return err
		}
		if n >= MaxSchedules {
			return ErrCapacity
		}
		fired := any(nil)
		if !s.LastFiredAt.IsZero() {
			fired = ts(s.LastFiredAt)
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO schedules (name, job_kind, inputs_json, preset, enabled, missed_policy, dtstart, pack_id, pack_key,
			last_fired_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?)`,
			s.Name, s.Kind, marshal(s.Inputs), marshal(s.Preset), boolInt(s.Enabled), s.Policy, ts(s.DTStart), s.PackID, s.PackKey,
			fired, ts(now), ts(now))
		if err != nil {
			if isUnique(err) {
				return ErrNameConflict
			}
			return err
		}
		s.ID, err = res.LastInsertId()
		return err
	})
	return s, err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Get returns one schedule.
func (r *Repo) Get(ctx context.Context, id int64) (Schedule, error) {
	s, err := scanSchedule(r.st.Read().QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Schedule{}, ErrNotFound
	}
	return s, err
}

func (r *Repo) query(ctx context.Context, where string, args ...any) ([]Schedule, error) {
	rows, err := r.st.Read().QueryContext(ctx, `SELECT `+scheduleColumns+` FROM schedules `+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		s, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// List returns every schedule in id order.
func (r *Repo) List(ctx context.Context) ([]Schedule, error) { return r.query(ctx, "") }

// ListEnabled returns the enabled schedules; every tick calls it.
func (r *Repo) ListEnabled(ctx context.Context) ([]Schedule, error) {
	return r.query(ctx, "WHERE enabled = 1")
}

// ListPack returns the schedules that carry a pack's marker.
func (r *Repo) ListPack(ctx context.Context, pack string) ([]Schedule, error) {
	return r.query(ctx, "WHERE pack_id = ?", pack)
}

// Update reads a schedule, lets edit change it and writes it back in one
// transaction. Enabling a disabled schedule moves the cursor to the enabling
// instant so the disabled period is never replayed, and so does a change of
// the recurrence (the preset or its start): an edited time that is already
// past today waits for its next occurrence.
func (r *Repo) Update(ctx context.Context, id int64, edit func(*Schedule) error) (Schedule, error) {
	var out Schedule
	err := r.st.Write(ctx, func(tx *sql.Tx) error {
		cur, err := scanSchedule(tx.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE id = ?`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		next := cur
		if err := edit(&next); err != nil {
			return err
		}
		now := r.now().UTC()
		next.UpdatedAt = now
		if (next.Enabled && !cur.Enabled) || marshal(next.Preset) != marshal(cur.Preset) || !next.DTStart.Equal(cur.DTStart) {
			next.LastFiredAt = now
		}
		fired := any(nil)
		if !next.LastFiredAt.IsZero() {
			fired = ts(next.LastFiredAt)
		}
		_, err = tx.ExecContext(ctx, `UPDATE schedules SET name = ?, job_kind = ?, inputs_json = ?, preset = ?, enabled = ?, missed_policy = ?,
			dtstart = ?, pack_id = NULLIF(?, ''), pack_key = NULLIF(?, ''), last_fired_at = ?, updated_at = ? WHERE id = ?`,
			next.Name, next.Kind, marshal(next.Inputs), marshal(next.Preset), boolInt(next.Enabled), next.Policy, ts(next.DTStart),
			next.PackID, next.PackKey, fired, ts(now), id)
		if isUnique(err) {
			return ErrNameConflict
		}
		out = next
		return err
	})
	return out, err
}

// Delete removes a schedule, its history and the Ask Agent's pending proposal
// to change it. Runs it started stay on the runs board with no schedule.
func (r *Repo) Delete(ctx context.Context, id int64) error {
	return r.st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM schedules WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM agent_schedule_proposals WHERE schedule_id = ?`, id)
		return err
	})
}

// Touch records the tick that evaluated the schedules.
func (r *Repo) Touch(ctx context.Context, ids []int64, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	return r.st.Write(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE schedules SET last_evaluated_at = ? WHERE id = ?`, ts(at), id); err != nil {
				return err
			}
		}
		return nil
	})
}

// Claim is the decision to start an occurrence.
type Claim struct {
	Schedule     Schedule // as read for this evaluation
	ScheduledFor time.Time
	Manual       bool
	Detail       string
}

// ClaimRun records the decision to fire: the run row, the history row and the
// schedule's cursor are written in one transaction. A second claim of the same
// occurrence, or a claim after the schedule was edited or deleted since it was
// read, changes nothing and returns ErrDecided or errStaleSchedule.
func (r *Repo) ClaimRun(ctx context.Context, c Claim) (string, error) {
	runID := newRunID()
	err := r.st.Write(ctx, func(tx *sql.Tx) error {
		cur, err := scanSchedule(tx.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE id = ?`, c.Schedule.ID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if !cur.UpdatedAt.Equal(c.Schedule.UpdatedAt) {
			return errStaleSchedule
		}
		if !c.Manual && !cur.LastFiredAt.IsZero() && !c.ScheduledFor.After(cur.LastFiredAt) {
			return ErrDecided
		}
		now := ts(r.now())
		input := marshal(c.Schedule.Inputs)
		if _, err := tx.ExecContext(ctx, `INSERT INTO runs (id, kind, status, input_json, schedule_id, scheduled_for, created_at, updated_at)
			VALUES (?, ?, 'queued', ?, ?, ?, ?, ?)`, runID, c.Schedule.Kind, input, c.Schedule.ID, ts(c.ScheduledFor), now, now); err != nil {
			if isUnique(err) {
				return ErrDecided
			}
			return err
		}
		outcome := OutcomeFired
		if c.Manual {
			outcome = OutcomeManual
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schedule_history (schedule_id, scheduled_for, outcome, detail, run_id, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`, c.Schedule.ID, ts(c.ScheduledFor), outcome, c.Detail, runID, now); err != nil {
			if isUnique(err) {
				return ErrDecided
			}
			return err
		}
		if c.Manual {
			_, err = tx.ExecContext(ctx, `UPDATE schedules SET active_run_id = ?, last_result = 'running', last_skip_reason = NULL WHERE id = ?`, runID, c.Schedule.ID)
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE schedules SET last_fired_at = ?, active_run_id = ?, last_result = 'running', last_skip_reason = NULL WHERE id = ?`,
			ts(c.ScheduledFor), runID, c.Schedule.ID)
		return err
	})
	if err != nil {
		return "", err
	}
	return runID, nil
}

// ClaimFollowUp records the run of a follow-up (Kind.Then) on the schedule
// whose run declared it; sc carries the follow-up's kind and inputs. It has no
// scheduled instant, so it is not an occurrence: no history decision, no
// cursor move and not the schedule's active run.
func (r *Repo) ClaimFollowUp(ctx context.Context, sc Schedule) (string, error) {
	runID := newRunID()
	err := r.st.Write(ctx, func(tx *sql.Tx) error {
		now := ts(r.now())
		_, err := tx.ExecContext(ctx, `INSERT INTO runs (id, kind, status, input_json, schedule_id, created_at, updated_at)
			VALUES (?, ?, 'queued', ?, ?, ?, ?)`, runID, sc.Kind, marshal(sc.Inputs), sc.ID, now, now)
		return err
	})
	if err != nil {
		return "", err
	}
	return runID, nil
}

// Skip records the decision not to fire an occurrence and moves the cursor to
// it. It returns ErrDecided when the occurrence already has a decision.
func (r *Repo) Skip(ctx context.Context, sc Schedule, scheduledFor time.Time, reason, detail string) error {
	return r.st.Write(ctx, func(tx *sql.Tx) error {
		cur, err := scanSchedule(tx.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE id = ?`, sc.ID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if !cur.UpdatedAt.Equal(sc.UpdatedAt) {
			return errStaleSchedule
		}
		if !cur.LastFiredAt.IsZero() && !scheduledFor.After(cur.LastFiredAt) {
			return ErrDecided
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schedule_history (schedule_id, scheduled_for, outcome, reason, detail, created_at)
			VALUES (?, ?, 'skipped', ?, ?, ?)`, sc.ID, ts(scheduledFor), reason, detail, ts(r.now())); err != nil {
			if isUnique(err) {
				return ErrDecided
			}
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE schedules SET last_fired_at = ?, last_result = 'skipped', last_skip_reason = ? WHERE id = ?`,
			ts(scheduledFor), reason, sc.ID)
		return err
	})
}

// Defer notes, once per occurrence, that it stays due, and returns when the
// deferral began.
func (r *Repo) Defer(ctx context.Context, scheduleID int64, scheduledFor time.Time, reason, detail string) (since time.Time, firstReason string, err error) {
	err = r.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO schedule_history (schedule_id, scheduled_for, outcome, reason, detail, created_at)
			VALUES (?, ?, 'deferred', ?, ?, ?)`, scheduleID, ts(scheduledFor), reason, detail, ts(r.now())); err != nil {
			return err
		}
		var created string
		if err := tx.QueryRowContext(ctx, `SELECT created_at, reason FROM schedule_history WHERE schedule_id = ? AND scheduled_for = ? AND outcome = 'deferred'`,
			scheduleID, ts(scheduledFor)).Scan(&created, &firstReason); err != nil {
			return err
		}
		since = parseTS(created)
		return nil
	})
	return since, firstReason, err
}

// DeferredReason is the reason an occurrence was first deferred, if it was.
func (r *Repo) DeferredReason(ctx context.Context, scheduleID int64, scheduledFor time.Time) (string, bool) {
	var reason string
	err := r.st.Read().QueryRowContext(ctx, `SELECT reason FROM schedule_history WHERE schedule_id = ? AND scheduled_for = ? AND outcome = 'deferred'`,
		scheduleID, ts(scheduledFor)).Scan(&reason)
	return reason, err == nil
}

// Ended disables a schedule whose rule has no further occurrence and says why
// in its history.
func (r *Repo) Ended(ctx context.Context, sc Schedule, last time.Time) error {
	return r.st.Write(ctx, func(tx *sql.Tx) error {
		now := ts(r.now())
		res, err := tx.ExecContext(ctx, `UPDATE schedules SET enabled = 0, updated_at = ? WHERE id = ? AND updated_at = ? AND enabled = 1`,
			now, sc.ID, ts(sc.UpdatedAt))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO schedule_history (schedule_id, scheduled_for, outcome, reason, detail, created_at)
			VALUES (?, ?, 'ended', ?, 'the rule has no further occurrence; the schedule was disabled', ?)`,
			sc.ID, ts(last), ReasonDisabled, now)
		return err
	})
}

// Start marks the run as running.
func (r *Repo) Start(ctx context.Context, runID string, at time.Time) error {
	return r.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET status = 'running', updated_at = ? WHERE id = ? AND status = 'queued'`, ts(at), runID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE schedule_history SET started_at = ? WHERE run_id = ? AND started_at IS NULL`, ts(at), runID)
		return err
	})
}

// Finish records how a job ended on the run, the history row and the schedule.
type Finish struct {
	RunID    string
	Status   string // succeeded, failed or cancelled
	Reason   string
	Detail   string
	Attempts int
	Summary  string
	At       time.Time
	// Interrupted ends a fired occurrence with the outcome interrupted.
	Interrupted bool
}

// Finish writes the end of a run.
func (r *Repo) Finish(ctx context.Context, f Finish) error {
	return r.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET status = ?, failure_reason = ?, updated_at = ?, finished_at = ?
			WHERE id = ? AND status IN ('queued', 'running')`, f.Status, f.Reason, ts(f.At), ts(f.At), f.RunID); err != nil {
			return err
		}
		outcome := ""
		if f.Interrupted {
			outcome = OutcomeInterrupted
		}
		if _, err := tx.ExecContext(ctx, `UPDATE schedule_history SET finished_at = ?, attempts = ?, summary = ?, reason = ?,
			detail = CASE WHEN ? <> '' THEN ? ELSE detail END, outcome = CASE WHEN ? <> '' AND outcome = 'fired' THEN ? ELSE outcome END
			WHERE run_id = ? AND finished_at IS NULL`, ts(f.At), f.Attempts, f.Summary, f.Reason, f.Detail, f.Detail, outcome, outcome, f.RunID); err != nil {
			return err
		}
		result := ResultOK
		if f.Status != "succeeded" {
			result = ResultFailed
		}
		_, err := tx.ExecContext(ctx, `UPDATE schedules SET active_run_id = NULL, last_result = ?, last_skip_reason = NULL WHERE active_run_id = ?`, result, f.RunID)
		return err
	})
}

// SweepInterrupted marks every run of a schedule that is still queued or
// running as failed with reason interrupted, and the matching history rows as
// interrupted. Nothing resumes them.
func (r *Repo) SweepInterrupted(ctx context.Context) (int, error) {
	var n int
	err := r.st.Write(ctx, func(tx *sql.Tx) error {
		now := ts(r.now())
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET status = 'failed', failure_reason = 'interrupted', updated_at = ?1, finished_at = ?1
			WHERE schedule_id IS NOT NULL AND status IN ('queued', 'running')`, now); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE schedule_history SET outcome = 'interrupted', reason = 'interrupted', finished_at = ?
			WHERE outcome = 'fired' AND finished_at IS NULL`, now)
		if err != nil {
			return err
		}
		if c, _ := res.RowsAffected(); c > 0 {
			n = int(c)
		}
		// A manual run keeps its outcome; only its end is recorded.
		if _, err := tx.ExecContext(ctx, `UPDATE schedule_history SET reason = 'interrupted', finished_at = ?
			WHERE outcome = 'manual' AND finished_at IS NULL`, now); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE schedules SET active_run_id = NULL, last_result = 'failed' WHERE active_run_id IS NOT NULL`)
		return err
	})
	return n, err
}

// History returns a schedule's history, newest first.
func (r *Repo) History(ctx context.Context, scheduleID int64, limit int) ([]HistoryRow, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.st.Read().QueryContext(ctx, `SELECT id, schedule_id, scheduled_for, outcome, reason, detail, COALESCE(run_id, ''),
		COALESCE(started_at, ''), COALESCE(finished_at, ''), attempts, summary, created_at
		FROM schedule_history WHERE schedule_id = ? ORDER BY id DESC LIMIT ?`, scheduleID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HistoryRow
	for rows.Next() {
		var h HistoryRow
		var sched, started, finished, created string
		if err := rows.Scan(&h.ID, &h.ScheduleID, &sched, &h.Outcome, &h.Reason, &h.Detail, &h.RunID, &started, &finished, &h.Attempts, &h.Summary, &created); err != nil {
			return nil, err
		}
		h.ScheduledFor, h.StartedAt, h.FinishedAt, h.CreatedAt = parseTS(sched), parseTS(started), parseTS(finished), parseTS(created)
		out = append(out, h)
	}
	return out, rows.Err()
}

// HistoryRetention is how long history rows are kept.
const HistoryRetention = 90 * 24 * time.Hour

// PruneHistory deletes history rows older than the retention period.
func (r *Repo) PruneHistory(ctx context.Context) (int64, error) {
	var n int64
	err := r.st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM schedule_history WHERE created_at < ?`, ts(r.now().Add(-HistoryRetention)))
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}

// pausedMeta records the schedules the last pause-all switched off, so
// resume-all switches back on only those.
const pausedMeta = "scheduler.paused-all"

// pausedRef names a schedule by id and creation instant: ids are reused after
// a delete, so a new schedule never takes the place of one that was paused.
type pausedRef struct {
	ID      int64  `json:"id"`
	Created string `json:"created"`
}

// kindPausedMeta records the schedules of one job kind that PauseKind
// switched off.
func kindPausedMeta(kind string) string { return "scheduler.paused-kind." + kind }

func loadPaused(ctx context.Context, tx *sql.Tx, meta string) ([]pausedRef, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT value FROM workspace_meta WHERE key = ?`, meta).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var refs []pausedRef
	if json.Unmarshal([]byte(raw), &refs) != nil {
		return nil, nil // an unreadable record resumes nothing; the user's own switches stay
	}
	return refs, nil
}

// PauseAll switches off every enabled schedule in one transaction and adds
// them to the record resume-all reads. A second pause-all keeps what the first
// recorded. It returns the names it switched off.
func (r *Repo) PauseAll(ctx context.Context) ([]string, error) { return r.pause(ctx, pausedMeta, "") }

// PauseKind is PauseAll for the schedules of one job kind, with a record of
// its own that ResumeKind reads.
func (r *Repo) PauseKind(ctx context.Context, kind string) ([]string, error) {
	return r.pause(ctx, kindPausedMeta(kind), kind)
}

// ResumeKind is ResumeAll for what PauseKind switched off.
func (r *Repo) ResumeKind(ctx context.Context, kind string) ([]string, error) {
	return r.resume(ctx, kindPausedMeta(kind))
}

// pause switches off the enabled schedules, of one kind unless kind is empty,
// and adds them to the record meta names.
func (r *Repo) pause(ctx context.Context, meta, kind string) ([]string, error) {
	var names []string
	err := r.st.Write(ctx, func(tx *sql.Tx) error {
		refs, err := loadPaused(ctx, tx, meta)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id, name, created_at FROM schedules WHERE enabled = 1 AND (?1 = '' OR job_kind = ?1) ORDER BY id`, kind)
		if err != nil {
			return err
		}
		for rows.Next() {
			var ref pausedRef
			var name string
			if err := rows.Scan(&ref.ID, &name, &ref.Created); err != nil {
				rows.Close()
				return err
			}
			names, refs = append(names, name), append(refs, ref)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(names) == 0 {
			return nil
		}
		// updated_at moves, so an occurrence claimed from a copy read before
		// the pause is refused.
		if _, err := tx.ExecContext(ctx, `UPDATE schedules SET enabled = 0, updated_at = ?1 WHERE enabled = 1 AND (?2 = '' OR job_kind = ?2)`, ts(r.now()), kind); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO workspace_meta (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, meta, marshal(refs))
		return err
	})
	return names, err
}

// ResumeAll switches back on the schedules the last pause-all switched off and
// that are still off, moves each cursor to now so the paused time is never
// replayed, and clears the record. A schedule the user switched on, edited
// away or deleted since is left as it is. It returns the names it enabled.
func (r *Repo) ResumeAll(ctx context.Context) ([]string, error) { return r.resume(ctx, pausedMeta) }

func (r *Repo) resume(ctx context.Context, meta string) ([]string, error) {
	var names []string
	err := r.st.Write(ctx, func(tx *sql.Tx) error {
		refs, err := loadPaused(ctx, tx, meta)
		if err != nil {
			return err
		}
		now := ts(r.now())
		for _, ref := range refs {
			var name string
			err := tx.QueryRowContext(ctx, `SELECT name FROM schedules WHERE id = ? AND created_at = ? AND enabled = 0`, ref.ID, ref.Created).Scan(&name)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE schedules SET enabled = 1, last_fired_at = ?1, updated_at = ?1 WHERE id = ?2`, now, ref.ID); err != nil {
				return err
			}
			names = append(names, name)
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM workspace_meta WHERE key = ?`, meta)
		return err
	})
	return names, err
}

// Paused names, for each schedule a bulk pause switched off that is still
// off, the record holding it: "" for pause-all's, else the job kind PauseKind
// paused. One both hold reads as pause-all's, since resume-all brings it back.
func (r *Repo) Paused(ctx context.Context) (map[int64]string, error) {
	rows, err := r.st.Read().QueryContext(ctx, `SELECT key, value FROM workspace_meta WHERE key = ? OR key LIKE ? ORDER BY key DESC`, pausedMeta, kindPausedMeta("%"))
	if err != nil {
		return nil, err
	}
	type record struct {
		by   string
		refs []pausedRef
	}
	var records []record
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		var refs []pausedRef
		if json.Unmarshal([]byte(raw), &refs) == nil {
			records = append(records, record{strings.TrimPrefix(key, kindPausedMeta("")), refs})
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := map[int64]string{}
	for _, rec := range records {
		if rec.by == pausedMeta {
			rec.by = ""
		}
		for _, ref := range rec.refs {
			var one int
			err := r.st.Read().QueryRowContext(ctx, `SELECT 1 FROM schedules WHERE id = ? AND created_at = ? AND enabled = 0`, ref.ID, ref.Created).Scan(&one)
			if err == nil {
				out[ref.ID] = rec.by
			} else if !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
		}
	}
	return out, nil
}
