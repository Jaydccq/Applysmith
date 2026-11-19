// Clean-room: docs/provenance.md#CR-SCAN-SKELETON

package discovery

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// Scan run statuses.
const (
	RunRunning     = "running"
	RunCompleted   = "completed"
	RunFailed      = "failed"
	RunSkipped     = "skipped"
	RunBlocked     = "blocked"
	RunNeedsSignIn = "needs-sign-in"
	RunInterrupted = "interrupted"
	RunCancelled   = "cancelled"
)

// Counts are the numbers a run reports. Every discovered row ends in exactly
// one terminal count: filtered, undated, duplicates or inserted.
type Counts struct {
	Discovered int            `json:"discovered"`
	Filtered   map[string]int `json:"filtered"`
	Undated    int            `json:"undated"`
	Duplicates map[string]int `json:"duplicates"`
	Inserted   int            `json:"inserted"`

	Queued             int            `json:"queued"`
	Enriched           int            `json:"enriched"`
	EnrichmentDegraded int            `json:"enrichment_degraded"`
	EnrichmentFailed   int            `json:"enrichment_failed"`
	EnrichmentSkipped  int            `json:"enrichment_skipped"`
	GateRejected       int            `json:"gate_rejected"`
	Outcomes           map[string]int `json:"outcomes"`

	// TruncatedPostings is the number of postings not read because boards
	// were truncated.
	TruncatedPostings int `json:"truncated_postings"`
	BoardErrors       int `json:"board_errors"`
}

func newCounts() Counts {
	return Counts{Filtered: map[string]int{}, Duplicates: map[string]int{}, Outcomes: map[string]int{}}
}

func sumMap(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

// Terminal is the sum of the terminal counts.
func (c Counts) Terminal() int {
	return sumMap(c.Filtered) + c.Undated + sumMap(c.Duplicates) + c.Inserted
}

// Validate reports an unbalanced run: a defect, not a quiet state.
func (c Counts) Validate() error {
	if c.Discovered != c.Terminal() {
		return fmt.Errorf("scan counts do not reconcile: discovered %d, terminal counts sum to %d", c.Discovered, c.Terminal())
	}
	return nil
}

// Record is a scan run.
type Record struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	Error  string `json:"error,omitempty"`
	// Retryable preserves a typed temporary failure after its error becomes text.
	Retryable  bool      `json:"retryable,omitempty"`
	Started    time.Time `json:"started_at"`
	Updated    time.Time `json:"updated_at"`
	Finished   time.Time `json:"finished_at,omitempty"`
	DurationMS int64     `json:"duration_ms"`
	Counts     Counts    `json:"counts"`
	// JobOutcomes attributes each shared evaluation once within this scan.
	JobOutcomes map[string]string `json:"job_outcomes,omitempty"`
	Boards      int               `json:"boards_visited"`
	Truncated   []string          `json:"truncated,omitempty"`
	Deferred    map[string]int    `json:"deferred,omitempty"`
	Suspended   []string          `json:"suspended,omitempty"`
	// Detail holds the source-specific numbers of a run (target, search
	// depth, skipped-stale, exhausted-budget).
	Detail map[string]int `json:"detail,omitempty"`
	// Attempts is the bounded trail of list pages read.
	Attempts []Attempt `json:"attempts,omitempty"`
	// Host is what a browser scan measured before it opened a tab.
	Host *HostSample `json:"host,omitempty"`
	// Searches are the effective searches a browser scan ran, so an empty run
	// can be attributed to the search and not to the board.
	Searches []Search       `json:"searches,omitempty"`
	Params   map[string]any `json:"params,omitempty"`
	Errors   []string       `json:"errors,omitempty"`
	// Unrouted is the evaluation task that had no model when the run would
	// have sent its top rows to evaluation (Scanner.Unrouted); it sent none.
	Unrouted string `json:"unrouted,omitempty"`
}

// SpendCapped reports whether the run stopped at its source's spend cap. That
// stop and a scan's time budget both end completed/budget-reached; Exa alone
// has a spend cap, and its plan of at most 28 searches never lasts a scan's
// time budget.
func (r Record) SpendCapped() bool {
	return r.Source == "exa" && r.Status == RunCompleted && r.Reason == ReasonBudget
}

// RunningError is returned when the source already has a running scan.
type RunningError struct{ Source, RunID string }

func (e *RunningError) Error() string {
	return fmt.Sprintf("a %s scan is already running (run %s)", e.Source, e.RunID)
}

func newRunID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "scan-" + hex.EncodeToString(b[:])
}

// ScanStart creates the run record. A second running scan of the same source
// is refused with a *RunningError and no record is created.
func (r *Repo) ScanStart(ctx context.Context, source string, params map[string]any) (Record, error) {
	rec := Record{ID: newRunID(), Source: source, Status: RunRunning, Started: r.now().UTC(), Counts: newCounts(), Params: params}
	rec.Updated = rec.Started
	body, _ := json.Marshal(rec)
	err := r.s.Write(ctx, func(tx *sql.Tx) error {
		var id string
		err := tx.QueryRowContext(ctx, `SELECT id FROM scan_runs WHERE source = ? AND status = 'running'`, source).Scan(&id)
		if err == nil {
			return &RunningError{Source: source, RunID: id}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO scan_runs (id, source, status, started_at, updated_at, record_json) VALUES (?, ?, 'running', ?, ?, ?)`,
			rec.ID, source, rec.Started.Format(timeLayout), rec.Updated.Format(timeLayout), string(body))
		return err
	})
	var re *RunningError
	if errors.As(err, &re) {
		return Record{}, re
	}
	return rec, err
}

// keepOutcomes preserves asynchronous evaluation results when a scan writes
// a snapshot that was captured before those jobs ended.
func keepOutcomes(ctx context.Context, tx *sql.Tx, rec *Record) error {
	var body string
	if err := tx.QueryRowContext(ctx, `SELECT record_json FROM scan_runs WHERE id = ?`, rec.ID).Scan(&body); err != nil {
		return err
	}
	var stored Record
	if err := json.Unmarshal([]byte(body), &stored); err != nil {
		return err
	}
	rec.Counts.Outcomes, rec.JobOutcomes = stored.Counts.Outcomes, stored.JobOutcomes
	return nil
}

// ScanSave stores the counts reached so far.
func (r *Repo) ScanSave(ctx context.Context, rec Record) error {
	rec.Updated = r.now().UTC()
	return r.s.Write(ctx, func(tx *sql.Tx) error {
		if err := keepOutcomes(ctx, tx, &rec); err != nil {
			return err
		}
		body, _ := json.Marshal(rec)
		_, err := tx.ExecContext(ctx, `UPDATE scan_runs SET updated_at = ?, record_json = ? WHERE id = ? AND status = 'running'`,
			rec.Updated.Format(timeLayout), string(body), rec.ID)
		return err
	})
}

// ScanFinish closes a run with its final status. Outcomes recorded so far are
// kept.
func (r *Repo) ScanFinish(ctx context.Context, rec Record) (Record, error) {
	now := r.now().UTC()
	rec.Updated, rec.Finished = now, now
	rec.DurationMS = now.Sub(rec.Started).Milliseconds()
	err := r.s.Write(ctx, func(tx *sql.Tx) error {
		if err := keepOutcomes(ctx, tx, &rec); err != nil {
			return err
		}
		body, _ := json.Marshal(rec)
		_, err := tx.ExecContext(ctx, `UPDATE scan_runs SET status = ?, reason = ?, error = ?, updated_at = ?, finished_at = ?, duration_ms = ?, record_json = ? WHERE id = ?`,
			rec.Status, rec.Reason, rec.Error, now.Format(timeLayout), now.Format(timeLayout), rec.DurationMS, string(body), rec.ID)
		return err
	})
	return rec, err
}

// ScanCloseOrphans closes runs a dead process left running. The workspace
// lock means no live runner owns them.
func (r *Repo) ScanCloseOrphans(ctx context.Context) error {
	rows, err := r.s.Read().QueryContext(ctx, `SELECT id FROM scan_runs WHERE status = 'running'`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		rec, err := r.ScanGet(ctx, id)
		if err != nil {
			// A document that cannot be read is closed by its columns alone, so
			// one damaged row never stops the server from starting.
			now := r.now().UTC().Format(timeLayout)
			if err := r.s.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `UPDATE scan_runs SET status = ?, reason = ?, updated_at = ?, finished_at = ? WHERE id = ?`,
					RunInterrupted, "process-exit", now, now, id)
				return err
			}); err != nil {
				return err
			}
			continue
		}
		// The column names the row, whatever the document says.
		rec.ID = id
		rec.Status, rec.Reason = RunInterrupted, "process-exit"
		if _, err := r.ScanFinish(ctx, rec); err != nil {
			return err
		}
	}
	return nil
}

// ScanGet returns one run.
func (r *Repo) ScanGet(ctx context.Context, id string) (Record, error) {
	var body string
	err := r.s.Read().QueryRowContext(ctx, `SELECT record_json FROM scan_runs WHERE id = ?`, id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, fmt.Errorf("%w: scan run %s", ErrNotFound, id)
	}
	if err != nil {
		return Record{}, err
	}
	var rec Record
	if err := json.Unmarshal([]byte(body), &rec); err != nil {
		return Record{}, err
	}
	// Status columns are authoritative.
	err = r.s.Read().QueryRowContext(ctx, `SELECT status, reason, error FROM scan_runs WHERE id = ?`, id).Scan(&rec.Status, &rec.Reason, &rec.Error)
	return rec, err
}

// ScanByRun returns the newest scan record of a runs-board run.
func (r *Repo) ScanByRun(ctx context.Context, runID string) (Record, error) {
	var id string
	err := r.s.Read().QueryRowContext(ctx, `SELECT id FROM scan_runs WHERE json_extract(record_json, '$.params.run_id') = ?
		ORDER BY started_at DESC LIMIT 1`, runID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, fmt.Errorf("%w: no scan of run %s", ErrNotFound, runID)
	}
	if err != nil {
		return Record{}, err
	}
	return r.ScanGet(ctx, id)
}

// ScanFilter selects scan runs: a zero field matches every run. From is
// inclusive and To exclusive, both on the start time.
type ScanFilter struct {
	Source, Status string
	From, To       time.Time
	Limit, Offset  int
}

func (f ScanFilter) where() (string, []any) {
	var conds []string
	var args []any
	if f.Source != "" {
		conds = append(conds, "source = ?")
		args = append(args, f.Source)
	}
	if f.Status != "" {
		conds = append(conds, "status = ?")
		args = append(args, f.Status)
	}
	if !f.From.IsZero() {
		conds = append(conds, "started_at >= ?")
		args = append(args, f.From.UTC().Format(timeLayout))
	}
	if !f.To.IsZero() {
		conds = append(conds, "started_at < ?")
		args = append(args, f.To.UTC().Format(timeLayout))
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// ScanList returns the newest runs, optionally for one source.
func (r *Repo) ScanList(ctx context.Context, source string, limit int) ([]Record, error) {
	out, _, err := r.ScanPage(ctx, ScanFilter{Source: source, Limit: limit})
	return out, err
}

// ScanPage returns one page of the newest runs that match the filter, with the
// number of runs that match in all.
func (r *Repo) ScanPage(ctx context.Context, f ScanFilter) ([]Record, int, error) {
	if f.Limit <= 0 {
		f.Limit = 20
	}
	where, args := f.where()
	var total int
	if err := r.s.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM scan_runs`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.s.Read().QueryContext(ctx, `SELECT id FROM scan_runs`+where+` ORDER BY started_at DESC, id DESC LIMIT ? OFFSET ?`, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	var out []Record
	for _, id := range ids {
		rec, err := r.ScanGet(ctx, id)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, rec)
	}
	return out, total, nil
}

// Evaluation outcomes a finished job reports to its run.
const (
	OutcomeReported = "reported"
	OutcomeScreened = "screened"
	OutcomeGated    = "gated"
	OutcomeFailed   = "failed"
	OutcomeTimedOut = "timed-out"
	// OutcomeDuplicate is a job that found a final report already existing.
	OutcomeDuplicate = "duplicate"
)

// ScanOutcome records a job's terminal result on an explicit scan. One job
// can serve several scans; each receives its own idempotent result entry.
func (r *Repo) ScanOutcome(ctx context.Context, runID, jobID, outcome string) error {
	if jobID == "" {
		return errors.New("a scan outcome needs an evaluation job id")
	}
	switch outcome {
	case OutcomeReported, OutcomeScreened, OutcomeGated, OutcomeFailed, OutcomeTimedOut, OutcomeDuplicate:
	default:
		return fmt.Errorf("unknown evaluation outcome %q", outcome)
	}
	return r.s.Write(ctx, func(tx *sql.Tx) error {
		var body string
		if err := tx.QueryRowContext(ctx, `SELECT record_json FROM scan_runs WHERE id = ?`, runID).Scan(&body); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		var rec Record
		if err := json.Unmarshal([]byte(body), &rec); err != nil {
			return err
		}
		if previous, ok := rec.JobOutcomes[jobID]; ok {
			if previous != outcome {
				return fmt.Errorf("evaluation job %s already ended %s on scan %s", jobID, previous, runID)
			}
			return nil
		}
		if rec.JobOutcomes == nil {
			rec.JobOutcomes = map[string]string{}
		}
		rec.JobOutcomes[jobID] = outcome
		if rec.Counts.Outcomes == nil {
			rec.Counts.Outcomes = map[string]int{}
		}
		rec.Counts.Outcomes[outcome]++
		out, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE scan_runs SET record_json = ? WHERE id = ?`, string(out), runID)
		return err
	})
}

func cloneMap(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// clone copies the record so a snapshot can be marshalled while the run
// keeps counting.
func (r Record) clone() Record {
	r.Counts.Filtered = cloneMap(r.Counts.Filtered)
	r.Counts.Duplicates = cloneMap(r.Counts.Duplicates)
	r.Counts.Outcomes = cloneMap(r.Counts.Outcomes)
	r.JobOutcomes = maps.Clone(r.JobOutcomes)
	if r.Deferred != nil {
		r.Deferred = cloneMap(r.Deferred)
	}
	if r.Detail != nil {
		r.Detail = cloneMap(r.Detail)
	}
	r.Attempts = append([]Attempt(nil), r.Attempts...)
	r.Searches = append([]Search(nil), r.Searches...)
	r.Truncated = append([]string(nil), r.Truncated...)
	r.Suspended = append([]string(nil), r.Suspended...)
	r.Errors = append([]string(nil), r.Errors...)
	return r
}

// HighScore is the final score from which a report counts as a strong match.
const HighScore = 4.5

// FunnelRow is what one source (lane) produced in a period: postings its scans
// fetched (a posting read again by a later scan counts again), the new ones of
// those put in the inbox, postings evaluated, evaluations that failed or were
// stopped by a gate, and how far the evaluated ones got in the tracker.
type FunnelRow struct {
	Lane                                                        string
	Discovered, Inserted, Evaluated, Failed, GateRejected, High int
	Applied, Responded, Interview                               int
}

// ManualLane holds evaluations the user started by hand.
const ManualLane = "manual"

// SourceFunnel reads the funnel of every source over the evaluations started
// and the scans run in [since, until). A posting is counted once however many
// times it was evaluated, under the source of its first finished evaluation in
// the period; where it stands is read from its tracker row. Lanes are sorted by
// postings evaluated, then by name.
func (r *Repo) SourceFunnel(ctx context.Context, since, until time.Time) ([]FunnelRow, error) {
	from, to := since.UTC().Format(timeLayout), until.UTC().Format(timeLayout)
	lanes := map[string]*FunnelRow{}
	lane := func(name string) *FunnelRow {
		if name = strings.TrimSpace(name); name == "" {
			name = ManualLane
		}
		if lanes[name] == nil {
			lanes[name] = &FunnelRow{Lane: name}
		}
		return lanes[name]
	}

	runs, err := r.s.Read().QueryContext(ctx, `SELECT source, record_json FROM scan_runs WHERE started_at >= ? AND started_at < ? AND status <> 'running'`, from, to)
	if err != nil {
		return nil, err
	}
	for runs.Next() {
		var source, body string
		if err := runs.Scan(&source, &body); err != nil {
			runs.Close()
			return nil, err
		}
		var rec Record
		if json.Unmarshal([]byte(body), &rec) == nil {
			row := lane(source)
			row.Discovered += rec.Counts.Discovered
			row.Inserted += rec.Counts.Inserted
		}
	}
	runs.Close()
	if err := runs.Err(); err != nil {
		return nil, err
	}

	// A tracker row is where a posting stands: its score and furthest status.
	tr, err := r.s.Read().QueryContext(ctx, `SELECT identity_key, score, status, applied_at IS NOT NULL FROM tracker_entries WHERE archived_at IS NULL`)
	if err != nil {
		return nil, err
	}
	type standing struct {
		score                         sql.NullFloat64
		applied, responded, interview bool
	}
	where := map[string]standing{}
	for tr.Next() {
		var key, status string
		var st standing
		var appliedAt bool
		if err := tr.Scan(&key, &st.score, &status, &appliedAt); err != nil {
			tr.Close()
			return nil, err
		}
		st.interview = status == "interview" || status == "offer"
		st.responded = st.interview || status == "responded"
		st.applied = appliedAt || st.responded || status == "applied"
		where[key] = st
	}
	tr.Close()
	if err := tr.Err(); err != nil {
		return nil, err
	}

	jobs, err := r.s.Read().QueryContext(ctx, `SELECT identity_key, source, coalesce(outcome, '') FROM evaluation_jobs
		WHERE created_at >= ? AND created_at < ? ORDER BY created_at, run_id`, from, to)
	if err != nil {
		return nil, err
	}
	defer jobs.Close()
	counted := map[string]bool{}
	for jobs.Next() {
		var key, source, outcome string
		if err := jobs.Scan(&key, &source, &outcome); err != nil {
			return nil, err
		}
		switch outcome {
		case "failed":
			lane(source).Failed++
		case "gated":
			lane(source).GateRejected++
		case "reported", "screened":
			if counted[key] {
				continue
			}
			counted[key] = true
			row := lane(source)
			row.Evaluated++
			st, ok := where[key]
			if !ok {
				continue
			}
			if st.score.Valid && st.score.Float64 >= HighScore {
				row.High++
			}
			if st.applied {
				row.Applied++
			}
			if st.responded {
				row.Responded++
			}
			if st.interview {
				row.Interview++
			}
		}
	}
	if err := jobs.Err(); err != nil {
		return nil, err
	}
	out := make([]FunnelRow, 0, len(lanes))
	for _, row := range lanes {
		out = append(out, *row)
	}
	slices.SortFunc(out, func(a, b FunnelRow) int {
		if a.Evaluated != b.Evaluated {
			return b.Evaluated - a.Evaluated
		}
		return strings.Compare(a.Lane, b.Lane)
	})
	return out, nil
}

// Stages counts the postings that reached each stage in a period, each once:
// found (into the inbox), evaluated (a report or a screening), and applied to
// offer (a tracker row not archived moved there, corrections included).
// Responded counts any reply: a row moved to Responded, Interview, Offer or
// Rejected, as employers often answer with the later status directly.
type Stages struct{ Found, Evaluated, Applied, Responded, Interview, Offer int }

// RepliedStatuses are the statuses whose reach the Responded stage counts.
var RepliedStatuses = []string{"responded", "interview", "offer", "rejected"}

// StageCounts reads the stages reached in [since, until).
func (r *Repo) StageCounts(ctx context.Context, since, until time.Time) (c Stages, err error) {
	q := `SELECT (SELECT count(*) FROM inbox WHERE first_seen_at >= ?1 AND first_seen_at < ?2), (SELECT count(DISTINCT identity_key)
		FROM evaluation_jobs WHERE outcome IN ('reported', 'screened') AND finished_at >= ?1 AND finished_at < ?2)`
	for _, s := range []string{"'applied'", "'" + strings.Join(RepliedStatuses, "', '") + "'", "'interview'", "'offer'"} {
		q += `, (SELECT count(DISTINCT entry_id) FROM tracker_events WHERE kind IN ('created', 'status_changed', 'status_corrected') AND new_status IN (` + s + `) AND at >= ?1 AND at < ?2
			AND entry_id IN (SELECT id FROM tracker_entries WHERE archived_at IS NULL))`
	}
	err = r.s.Read().QueryRowContext(ctx, q, since.UTC().Format(timeLayout), until.UTC().Format(timeLayout)).Scan(&c.Found, &c.Evaluated, &c.Applied, &c.Responded, &c.Interview, &c.Offer)
	return c, err
}
