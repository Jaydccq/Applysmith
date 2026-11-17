package apply

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"applysmith/internal/gates"
	"applysmith/internal/lazyregexp"
	"applysmith/internal/store"
)

// Evidence supplies the facts a selection decides from. A source it cannot
// read is named in Facts.Unreadable instead of failing the call: a dry list
// goes on with fewer skips, a submitting list stops.
type Evidence interface {
	Facts(ctx context.Context, now time.Time) (Facts, error)
}

// Facts is a snapshot of everything the skip rules read.
type Facts struct {
	// Ledger is every submissions row, oldest first.
	Ledger  []LedgerFact
	Tracker []TrackerFact
	Reports []ReportFact
	// Runs are the newest run of every posting over the whole ledger and every
	// run of the last day or so, oldest first.
	Runs []RunFact
	// Interrupted are the rows an apply-list batch began and never ended.
	Interrupted []JournalFact
	// DataSavedAt is when the user last saved the answer bank, the profile or
	// the CV; zero when none can be read.
	DataSavedAt time.Time
	// DocumentsAt is, per tracker row, when it last got a ready résumé or
	// cover letter or a documents override. A source that cannot be read
	// leaves it empty: no stop is lifted by it then.
	DocumentsAt map[int64]time.Time
	// Unreadable names the sources that could not be read.
	Unreadable []string
}

// LedgerFact is one submissions row.
type LedgerFact struct {
	ID                              int64
	IdentityKey, URL, Company, Role string
	Report                          int64
	State                           gates.LedgerState
	Confirmed                       bool
	At                              time.Time
}

// TrackerFact is one tracker row with the address its report carries.
type TrackerFact struct {
	ID                    int64
	Company, Role         string
	Status                string
	Report                int64
	Key                   string
	Aliases               []string
	PostingURL, ReportURL string
}

// ReportFact is the identity of an evaluated report.
type ReportFact struct {
	Number             int64
	Company, Role, URL string
}

// JournalFact is a batch row that was started and never ended.
type JournalFact struct {
	Key       string
	StartedAt time.Time
}

// RunFact is one apply run as the list and the scorecard read it.
type RunFact struct {
	ID, URL, Identity, ATS, Company, Role string
	Report                                int64
	Started, Finished                     time.Time
	Kind                                  OutcomeKind
	Stage                                 Stage
	Class                                 Class
	Reason                                string
	Confirmed                             bool
	Build                                 string
	Signins                               []SigninRecord
	// HasResolve and ResolveDetail are the resolve stage line.
	HasResolve    bool
	ResolveDetail string
	Pages         []PageReport
	// Snapshot is the stop snapshot the learn stage line names.
	Snapshot string
}

// Minutes is the run's wall time.
func (r RunFact) Minutes() float64 {
	if r.Finished.IsZero() || r.Finished.Before(r.Started) {
		return 0
	}
	return r.Finished.Sub(r.Started).Minutes()
}

// keys are the address keys a run is found by.
func (r RunFact) keys() []string {
	k := postingKey(r.URL)
	if r.Identity != "" && r.Identity != k {
		return []string{k, r.Identity}
	}
	return []string{k}
}

// freshRunWindow is how far back every run is read; older runs matter only as
// the newest of their posting. A credential cooldown lasts a day and today
// never reaches back further.
const freshRunWindow = 26 * time.Hour

// SQLEvidence reads the facts from the tracking database.
type SQLEvidence struct {
	Store *store.Store
	// DataSavedAt reports when the user's answer bank, profile or CV was last
	// saved; nil reads as never, so a stop on the user's own data waits for the
	// user to name the company.
	DataSavedAt func(ctx context.Context) (time.Time, error)
}

// NewestModTime reads the save time of user files: the newest modification
// time among them, files that do not exist ignored.
func NewestModTime(paths ...string) func(context.Context) (time.Time, error) {
	return func(context.Context) (time.Time, error) {
		var newest time.Time
		for _, p := range paths {
			fi, err := os.Stat(p)
			switch {
			case errors.Is(err, os.ErrNotExist):
			case err != nil:
				return time.Time{}, err
			case fi.ModTime().After(newest):
				newest = fi.ModTime()
			}
		}
		return newest, nil
	}
}

// Facts reads every source once.
func (e SQLEvidence) Facts(ctx context.Context, now time.Time) (Facts, error) {
	var f Facts
	db := e.Store.Read()
	var err error
	note := func(name string, err error) {
		if err != nil {
			f.Unreadable = append(f.Unreadable, name)
		}
	}
	f.Ledger, err = loadLedger(ctx, db)
	note("ledger", err)
	f.Tracker, err = loadTrackerFacts(ctx, db)
	note("tracker", err)
	f.Reports, err = loadReports(ctx, db)
	note("reports", err)
	f.Runs, err = loadRuns(ctx, db, false, `started_at >= ? OR run_id IN (SELECT run_id FROM (SELECT run_id,
		ROW_NUMBER() OVER (PARTITION BY identity_key ORDER BY started_at DESC, run_id DESC) AS rn FROM apply_runs) WHERE rn = 1)`,
		now.Add(-freshRunWindow).UTC().Format(stampLayout))
	note("runs", err)
	f.Interrupted, err = loadInterrupted(ctx, db)
	note("journal", err)
	if e.DataSavedAt != nil {
		if at, derr := e.DataSavedAt(ctx); derr == nil {
			f.DataSavedAt = at
		}
	}
	f.DocumentsAt, _ = loadDocumentsAt(ctx, db)
	return f, ctx.Err()
}

// loadDocumentsAt reads when each tracker row last got a ready résumé or cover
// letter (documents) or a documents override (its tracker event).
func loadDocumentsAt(ctx context.Context, db *sql.DB) (map[int64]time.Time, error) {
	rows, err := db.QueryContext(ctx, `SELECT candidacy_id, max(created_at) FROM documents
			WHERE candidacy_id IS NOT NULL AND pdf_state = 'ready' AND kind IN ('resume', 'cover-letter') GROUP BY candidacy_id
		UNION ALL SELECT entry_id, max(at) FROM tracker_events WHERE kind = 'documents_override' GROUP BY entry_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]time.Time{}
	for rows.Next() {
		var id int64
		var at string
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		if t := parseStampText(at); t.After(out[id]) {
			out[id] = t
		}
	}
	return out, rows.Err()
}

func parseStampText(s string) time.Time {
	for _, layout := range []string{stampLayout, time.RFC3339Nano} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func loadLedger(ctx context.Context, db *sql.DB) ([]LedgerFact, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, identity_key, url, company, role, COALESCE(report_number, 0), state, confirmed, updated_at
		FROM submissions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LedgerFact
	for rows.Next() {
		var l LedgerFact
		var state, at string
		if err := rows.Scan(&l.ID, &l.IdentityKey, &l.URL, &l.Company, &l.Role, &l.Report, &state, &l.Confirmed, &at); err != nil {
			return nil, err
		}
		l.State, l.At = gates.LedgerState(state), parseStampText(at)
		out = append(out, l)
	}
	return out, rows.Err()
}

func loadTrackerFacts(ctx context.Context, db *sql.DB) ([]TrackerFact, error) {
	rows, err := db.QueryContext(ctx, `SELECT t.id, t.company, t.role, t.status, COALESCE(t.report_number, 0), t.identity_key, t.posting_url, COALESCE(r.posting_url, '')
		FROM tracker_entries t LEFT JOIN report_index r ON r.report_number = t.report_number ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrackerFact
	at := map[int64]int{}
	for rows.Next() {
		var t TrackerFact
		if err := rows.Scan(&t.ID, &t.Company, &t.Role, &t.Status, &t.Report, &t.Key, &t.PostingURL, &t.ReportURL); err != nil {
			return nil, err
		}
		at[t.ID] = len(out)
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	arows, err := db.QueryContext(ctx, `SELECT entry_id, identity_key FROM tracker_aliases`)
	if err != nil {
		return nil, err
	}
	defer arows.Close()
	for arows.Next() {
		var id int64
		var key string
		if err := arows.Scan(&id, &key); err != nil {
			return nil, err
		}
		if i, ok := at[id]; ok {
			out[i].Aliases = append(out[i].Aliases, key)
		}
	}
	return out, arows.Err()
}

func loadReports(ctx context.Context, db *sql.DB) ([]ReportFact, error) {
	rows, err := db.QueryContext(ctx, `SELECT report_number, company, role, posting_url FROM report_index ORDER BY report_number`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReportFact
	for rows.Next() {
		var r ReportFact
		if err := rows.Scan(&r.Number, &r.Company, &r.Role, &r.URL); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func loadInterrupted(ctx context.Context, db *sql.DB) ([]JournalFact, error) {
	rows, err := db.QueryContext(ctx, `SELECT url, updated_at FROM apply_batch_rows WHERE state = 'started'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JournalFact
	for rows.Next() {
		var u, at string
		if err := rows.Scan(&u, &at); err != nil {
			return nil, err
		}
		out = append(out, JournalFact{Key: postingKey(u), StartedAt: parseStampText(at)})
	}
	return out, rows.Err()
}

var snapshotRE = lazyregexp.New(`snapshot (\S+?)\)`)

// loadRuns reads runs matching where, oldest first. The pages are read for a
// stop on an answer gap at check (the user-data rule reads them) and for every
// run when allPages is set.
func loadRuns(ctx context.Context, db *sql.DB, allPages bool, where string, args ...any) ([]RunFact, error) {
	flag := 0
	if allPages {
		flag = 1
	}
	rows, err := db.QueryContext(ctx, `SELECT run_id, started_at, COALESCE(finished_at, ''), posting_url, identity_key, ats, COALESCE(report_number, 0), company, role,
		outcome_kind, outcome_stage, outcome_class, outcome_reason, confirmed, build, signins_json, stages_json,
		CASE WHEN ? = 1 OR (outcome_class = 'answer-gap' AND outcome_stage = 'check') THEN pages_json ELSE '[]' END
		FROM apply_runs WHERE `+where+` ORDER BY started_at, run_id`, append([]any{flag}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunFact
	for rows.Next() {
		var r RunFact
		var started, finished, kind, stage, class, signins, stages, pages string
		if err := rows.Scan(&r.ID, &started, &finished, &r.URL, &r.Identity, &r.ATS, &r.Report, &r.Company, &r.Role,
			&kind, &stage, &class, &r.Reason, &r.Confirmed, &r.Build, &signins, &stages, &pages); err != nil {
			return nil, err
		}
		r.Started, r.Finished = parseStampText(started), parseStampText(finished)
		r.Kind, r.Stage, r.Class = OutcomeKind(kind), Stage(stage), Class(class)
		if err := errors.Join(json.Unmarshal([]byte(signins), &r.Signins), json.Unmarshal([]byte(pages), &r.Pages)); err != nil {
			return nil, fmt.Errorf("run %s: %w", r.ID, err)
		}
		var lines []StageLine
		if err := json.Unmarshal([]byte(stages), &lines); err != nil {
			return nil, fmt.Errorf("run %s: %w", r.ID, err)
		}
		for _, l := range lines {
			switch l.Stage {
			case StageResolve:
				r.HasResolve, r.ResolveDetail = true, l.Detail
			case StageLearn:
				if m := snapshotRE.FindStringSubmatch(l.Detail); m != nil {
					r.Snapshot = m[1]
				}
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Precheck is a stop a mirror resolution or a liveness probe reached before
// the walk: it is written to the same ledger as a walk's stop, stamped with the
// build that reached it, so the holds read it like any other.
type Precheck struct {
	URL, Company, Role string
	ReportNumber       *int64
	Reason             string
	Class              Class
	Build              string
	At                 time.Time
}

// PrecheckRecorder writes a precheck stop.
type PrecheckRecorder interface {
	RecordPrecheck(ctx context.Context, p Precheck) error
}

// StorePrecheck writes precheck stops as apply_runs rows.
type StorePrecheck struct{ Store *store.Store }

// RecordPrecheck writes one stopped run at the resolve stage.
func (p StorePrecheck) RecordPrecheck(ctx context.Context, c Precheck) error {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return err
	}
	at := c.At.UTC().Format(stampLayout)
	reason := clip(c.Reason, maxReasonLen)
	stages, _ := json.Marshal([]StageLine{{Stage: StageResolve, Status: StatusFailed, Detail: clip(c.Reason, maxDetailLen), StartedAt: c.At.UTC()}})
	var report any
	if c.ReportNumber != nil {
		report = *c.ReportNumber
	}
	return p.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO apply_runs (run_id, started_at, finished_at, caller, posting_url, identity_key, report_number,
			outcome_kind, outcome_stage, outcome_class, outcome_reason, build, stages_json, company, role, submit)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'stopped', 'resolve', ?, ?, ?, ?, ?, ?, 0)`,
			"precheck-"+hex.EncodeToString(b[:]), at, at, string(gates.CallerApplyList), redactURL(c.URL), postingKey(c.URL), report,
			string(c.Class), reason, c.Build, string(stages), c.Company, c.Role)
		return err
	})
}
