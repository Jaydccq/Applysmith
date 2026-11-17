package gmail

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"applysmith/internal/ledger"
)

// Trigger says who started a run.
type Trigger string

// The three triggers of a run.
const (
	TriggerScheduled Trigger = "scheduled"
	TriggerManual    Trigger = "manual"
	TriggerCLI       Trigger = "cli"
)

// Run stages, in order.
const (
	StageList     = "list"
	StageFetch    = "fetch"
	StageClassify = "classify"
	StageStore    = "store"
	StageLink     = "link"
)

var stageOrder = []string{StageList, StageFetch, StageClassify, StageStore, StageLink}

// Stage statuses.
const (
	StagePending = "pending"
	StageOK      = "ok"
	StageFailed  = "failed"
	StageSkipped = "skipped"
)

// Run modes.
const (
	ModeBootstrap  = "bootstrap"
	ModeIncr       = "incremental"
	ModeFallback   = "fallback"
	ModeReclassify = "reclassify"
)

// StageResult is the status of one stage of a run.
type StageResult struct {
	Stage  string `json:"stage"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// Counts are the per-run tallies.
type Counts struct {
	Listed       int `json:"listed"`
	Fetched      int `json:"fetched"`
	Classified   int `json:"classified"`
	Deduplicated int `json:"deduplicated"`
	Stored       int `json:"stored"`
	Review       int `json:"review"`
	Ignored      int `json:"ignored"`
	Linked       int `json:"linked"`
	Tombstoned   int `json:"tombstoned"`
	Failed       int `json:"failed"`
}

// RunRecord is one durable sync record.
type RunRecord struct {
	ID                int64         `json:"id"`
	Trigger           Trigger       `json:"trigger"`
	Status            string        `json:"status"`
	Mode              string        `json:"mode"`
	StartedAt         time.Time     `json:"startedAt"`
	EndedAt           *time.Time    `json:"endedAt,omitempty"`
	CoverageComplete  bool          `json:"coverageComplete"`
	FallbackUsed      bool          `json:"fallbackUsed"`
	ClassifierVersion string        `json:"classifierVersion"`
	QueryVersion      string        `json:"queryVersion"`
	HistoryStart      string        `json:"historyStart,omitempty"`
	HistoryEnd        string        `json:"historyEnd,omitempty"`
	ResumeToken       string        `json:"resumeToken,omitempty"`
	FailedStage       string        `json:"failedStage,omitempty"`
	Error             string        `json:"error,omitempty"`
	Stages            []StageResult `json:"stages"`
	Counts            Counts        `json:"counts"`
}

func newStages() []StageResult {
	out := make([]StageResult, len(stageOrder))
	for i, s := range stageOrder {
		out[i] = StageResult{Stage: s, Status: StagePending}
	}
	return out
}

func (r *RunRecord) stage(name string) *StageResult {
	for i := range r.Stages {
		if r.Stages[i].Stage == name {
			return &r.Stages[i]
		}
	}
	return nil
}

func (r *RunRecord) fail(stage string, err error) {
	if st := r.stage(stage); st != nil {
		st.Status, st.Error = StageFailed, err.Error()
	}
	if r.FailedStage == "" {
		r.FailedStage = stage
		r.Error = err.Error()
	}
}

func (r *RunRecord) markOK(stage string) {
	if st := r.stage(stage); st != nil && st.Status == StagePending {
		st.Status = StageOK
	}
}

// StaleRunAfter is how long a running row may stand before it is judged
// abandoned even when its process id is alive, which covers a recycled id.
const StaleRunAfter = 2 * time.Hour

// keyRunOwner holds "<pid>|<start>" of the process that holds the one-run
// claim. It lives in gmail_state so two processes on one database see it.
const keyRunOwner = "run_owner"

// runOwnerAlive reports that the claim on a running row belongs to a live
// process and is not stale.
func (r *Repo) runOwnerAlive(owner string, startedAt time.Time) bool {
	if r.now().Sub(startedAt) > StaleRunAfter {
		return false
	}
	pidText, _, _ := strings.Cut(owner, "|")
	pid, err := strconv.Atoi(pidText)
	if err != nil {
		return false
	}
	alive := r.alive
	if alive == nil {
		alive = ledger.ProcessRunning
	}
	return alive(pid)
}

// claimRun enforces the one-run rule in the database, so it holds across
// Syncer objects and across processes. A running row whose owner is gone is
// closed as failed; a live one refuses the new run. It runs inside the
// transaction that inserts the new run.
func (r *Repo) claimRun(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, started_at FROM gmail_runs WHERE status = 'running'`)
	if err != nil {
		return err
	}
	type running struct {
		id      int64
		started time.Time
	}
	var live []running
	for rows.Next() {
		var x running
		var started string
		if err := rows.Scan(&x.id, &started); err != nil {
			rows.Close()
			return err
		}
		x.started, _ = parseStamp(started)
		live = append(live, x)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(live) == 0 {
		return nil
	}
	var owner string
	_ = tx.QueryRowContext(ctx, `SELECT value FROM gmail_state WHERE key = ?`, keyRunOwner).Scan(&owner)
	for _, x := range live {
		if r.runOwnerAlive(owner, x.started) {
			return ErrAlreadyRunning
		}
	}
	return closeRunning(ctx, tx, stamp(r.now()))
}

// begin inserts the running row after claiming the workspace's one run slot.
// prep, when given, runs in the same transaction once the claim is held, so
// work that must not happen beside a live run (a resync clearing the cursor)
// can never run without the claim.
func (r *Repo) beginRun(ctx context.Context, trig Trigger, mode, classifierVersion string, prep func(tx *sql.Tx) error) (*RunRecord, error) {
	rec := &RunRecord{Trigger: trig, Status: "running", Mode: mode, StartedAt: r.now().UTC(),
		ClassifierVersion: classifierVersion, QueryVersion: QueryVersion, Stages: newStages()}
	stages, _ := json.Marshal(rec.Stages)
	counts, _ := json.Marshal(rec.Counts)
	err := r.st.Write(ctx, func(tx *sql.Tx) error {
		if err := r.claimRun(ctx, tx); err != nil {
			return err
		}
		if prep != nil {
			if err := prep(tx); err != nil {
				return err
			}
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO gmail_runs (trigger_kind, status, mode, started_at, classifier_version, query_version, stages_json, counts_json)
			VALUES (?, 'running', ?, ?, ?, ?, ?, ?)`, string(trig), mode, stamp(rec.StartedAt), classifierVersion, QueryVersion, string(stages), string(counts))
		if err != nil {
			return err
		}
		if rec.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		return setState(ctx, tx, map[string]string{keyRunOwner: strconv.Itoa(os.Getpid()) + "|" + stamp(rec.StartedAt)})
	})
	if err != nil {
		return nil, err
	}
	return rec, nil
}

// saveRun writes the run's current state; the optional fn runs in the same
// transaction (the cursor moves there, as the last step).
func (r *Repo) saveRun(ctx context.Context, rec *RunRecord, fn func(tx *sql.Tx) error) error {
	stages, _ := json.Marshal(rec.Stages)
	counts, _ := json.Marshal(rec.Counts)
	var ended any
	if rec.EndedAt != nil {
		ended = stamp(*rec.EndedAt)
	}
	return r.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE gmail_runs SET status = ?, mode = ?, ended_at = ?, coverage_complete = ?, fallback_used = ?,
			history_start = ?, history_end = ?, resume_token = ?, failed_stage = ?, error = ?, stages_json = ?, counts_json = ? WHERE id = ?`,
			rec.Status, rec.Mode, ended, b2i(rec.CoverageComplete), b2i(rec.FallbackUsed), rec.HistoryStart, rec.HistoryEnd,
			rec.ResumeToken, rec.FailedStage, rec.Error, string(stages), string(counts), rec.ID); err != nil {
			return err
		}
		if fn != nil {
			return fn(tx)
		}
		return nil
	})
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

const runColumns = `id, trigger_kind, status, mode, started_at, ended_at, coverage_complete, fallback_used, classifier_version,
	query_version, history_start, history_end, resume_token, failed_stage, error, stages_json, counts_json`

func scanRun(sc interface{ Scan(...any) error }) (*RunRecord, error) {
	var rec RunRecord
	var trig, started, stages, counts string
	var ended sql.NullString
	var cov, fb int
	if err := sc.Scan(&rec.ID, &trig, &rec.Status, &rec.Mode, &started, &ended, &cov, &fb, &rec.ClassifierVersion,
		&rec.QueryVersion, &rec.HistoryStart, &rec.HistoryEnd, &rec.ResumeToken, &rec.FailedStage, &rec.Error, &stages, &counts); err != nil {
		return nil, err
	}
	rec.Trigger = Trigger(trig)
	rec.CoverageComplete, rec.FallbackUsed = cov == 1, fb == 1
	rec.StartedAt, _ = parseStamp(started)
	if ended.Valid {
		t, _ := parseStamp(ended.String)
		rec.EndedAt = &t
	}
	if err := json.Unmarshal([]byte(stages), &rec.Stages); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(counts), &rec.Counts); err != nil {
		return nil, err
	}
	return &rec, nil
}

// ErrRunNotFound answers a run id that names no record.
var ErrRunNotFound = errors.New("gmail: no such run")

// Run returns one run record.
func (r *Repo) Run(ctx context.Context, id int64) (*RunRecord, error) {
	rec, err := scanRun(r.st.Read().QueryRowContext(ctx, `SELECT `+runColumns+` FROM gmail_runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRunNotFound
	}
	return rec, err
}

// MaxRuns bounds a run listing.
const MaxRuns = 50

// Runs returns recent runs, newest first, at most MaxRuns.
func (r *Repo) Runs(ctx context.Context, limit int) ([]*RunRecord, error) {
	if limit <= 0 || limit > MaxRuns {
		limit = MaxRuns
	}
	rows, err := r.st.Read().QueryContext(ctx, `SELECT `+runColumns+` FROM gmail_runs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*RunRecord
	for rows.Next() {
		rec, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// closeRunning marks every running row as failed.
func closeRunning(ctx context.Context, tx *sql.Tx, at string) error {
	_, err := tx.ExecContext(ctx, `UPDATE gmail_runs SET status = 'failed', ended_at = ?, failed_stage = 'list',
		error = ?, stages_json = ? WHERE status = 'running'`, at, "the process ended before the run finished",
		mustJSON([]StageResult{{Stage: StageList, Status: StageFailed, Error: "the process ended before the run finished"}}))
	return err
}

// interruptAbandoned closes the running rows whose owner is gone. A row held
// by a live process is left alone, so a second process starting up cannot
// close a run that is still going.
func (r *Repo) interruptAbandoned(ctx context.Context) error {
	return r.st.Write(ctx, func(tx *sql.Tx) error {
		err := r.claimRun(ctx, tx)
		if errors.Is(err, ErrAlreadyRunning) {
			return nil
		}
		return err
	})
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("gmail: marshal: %v", err))
	}
	return string(b)
}
