package apply

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"applysmith/internal/gates"
	"applysmith/internal/store"
)

// saveRun writes the run's current state: the row is created on the first
// call and replaced on every later one. Pages, stages and sign-in records are
// JSON; none holds a secret or a form value.
func saveRun(ctx context.Context, st *store.Store, r *Run) error {
	stages, _ := json.Marshal(r.Stages)
	pages, _ := json.Marshal(r.Pages)
	signins, _ := json.Marshal(r.Signins)
	captcha, _ := json.Marshal(r.Captcha)
	var report any
	if r.Report != nil {
		report = r.Report.Number
	}
	var finished any
	if !r.FinishedAt.IsZero() {
		finished = r.FinishedAt.UTC().Format(stampLayout)
	}
	kind := string(r.Outcome.Kind)
	if kind == "" {
		kind = string(outcomeRunning)
	}
	return st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO apply_runs
			(run_id, started_at, finished_at, caller, posting_url, identity_key, ats, tenant, report_number, outcome_kind, outcome_stage, outcome_class, outcome_reason,
			 confirmed, confirm_source, build, trigger_conversation_id, trigger_message_hash, correction, stages_json, pages_json, signins_json, captcha_json, learning_events,
			 company, role, submit, batch_id, superseded_id)
			VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?15, ?16, NULLIF(?17, ''), NULLIF(?18, ''), ?19, ?20, ?21, ?22, ?23, ?24, ?25, ?26, ?27, NULLIF(?28, ''), ?29)
			ON CONFLICT (run_id) DO UPDATE SET finished_at = ?3, identity_key = ?6, ats = ?7, tenant = ?8, report_number = ?9, outcome_kind = ?10, outcome_stage = ?11,
			 outcome_class = ?12, outcome_reason = ?13, confirmed = ?14, confirm_source = ?15, correction = ?19, stages_json = ?20, pages_json = ?21, signins_json = ?22, captcha_json = ?23,
			 learning_events = ?24, company = ?25, role = ?26, submit = ?27, superseded_id = ?29`,
			r.ID, r.StartedAt.UTC().Format(stampLayout), finished, string(r.Input.Caller), redactURL(r.Input.URL), r.Identity, r.ATS, r.Tenant, report,
			kind, string(r.Outcome.Stage), string(r.Outcome.Class), clip(r.Outcome.Reason, maxReasonLen),
			r.Outcome.Confirmed, r.Outcome.ConfirmSource, r.Build, r.Input.ConversationID, r.MessageHash, r.Correction,
			string(stages), string(pages), string(signins), string(captcha), r.LearningEvents,
			r.company(), r.role(), r.Input.Submit, r.Input.BatchID, supersededID(r))
		return err
	})
}

// supersededID is the ledger row a correction run re-sends, as the run row
// stores it; NULL for every other run.
func supersededID(r *Run) any {
	if !r.Correction || r.Supersedes == 0 {
		return nil
	}
	return strconv.FormatInt(r.Supersedes, 10)
}

// runBounds are the per-run bounds a run spent. A run the user resumes with
// Done is the same run, so it continues them: at most 2 solves, one
// verification-email press and one password reset per host in all its legs.
type runBounds struct {
	Solves  int            `json:"solves,omitempty"`
	Resends int            `json:"resends,omitempty"`
	Resets  map[string]int `json:"resets,omitempty"`
}

// bounds reads what the run spent so far, its earlier legs included.
func (e *Exec) bounds() runBounds {
	b := runBounds{Solves: e.spent, Resends: e.resends, Resets: e.resets}
	if e.solver != nil {
		b.Solves = max(b.Solves, e.solver.Creations())
	}
	return b
}

func saveBounds(ctx context.Context, st *store.Store, runID string, b runBounds) error {
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE apply_runs SET bounds_json = ? WHERE run_id = ?`, string(raw), runID)
		return err
	})
}

// loadBounds reads what an earlier leg of the run spent; a run with no row has
// spent nothing.
func loadBounds(ctx context.Context, st *store.Store, runID string) (runBounds, error) {
	var raw string
	var b runBounds
	err := st.Read().QueryRowContext(ctx, `SELECT bounds_json FROM apply_runs WHERE run_id = ?`, runID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return b, nil
	}
	if err != nil {
		return b, err
	}
	return b, json.Unmarshal([]byte(raw), &b)
}

// redactURL drops the query and fragment, which can carry identifiers.
func redactURL(raw string) string {
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		return raw[:i]
	}
	return raw
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-3]) + "..."
}

// StartRequest is what a run the user started from the dashboard or the
// command line was started from: the facts its submit gate decides from. A run
// that goes on in its kept tab is rebuilt from it, never from the press that
// continued it.
type StartRequest struct {
	RunID            string
	Caller           gates.Caller
	Tool             string
	Submit, Explicit bool
	URL, EmployerURL string
	Company, Role    string
	Named, Resubmit  bool
}

// RequestRetention is how long a start request is kept; the runs board keeps
// its entries for at most 30 days.
const RequestRetention = 45 * 24 * time.Hour

// ErrNoRequest is a run with no recorded start request.
var ErrNoRequest = errors.New("apply: no start request is recorded for the run")

// Input is the run input the request rebuilds. A recorded request is the
// user's own start exactly when it is explicit: every start from the dashboard
// or the command line is, and a row of a batch resumed after a restart is not.
func (q StartRequest) Input() Input {
	return Input{URL: q.URL, EmployerURL: q.EmployerURL, Submit: q.Submit, ExplicitRequest: q.Explicit, UserStarted: q.Explicit, Resubmit: q.Resubmit, NamedRetry: q.Named,
		Caller: q.Caller, Tool: q.Tool, Company: q.Company, Role: q.Role}
}

// SaveRequest records the request a run was started from; a later record of
// the same run replaces it, as a confirmed "Submit this form" does.
func SaveRequest(ctx context.Context, st *store.Store, q StartRequest, now time.Time) error {
	if q.RunID == "" {
		return errors.New("apply: a start request needs its run id")
	}
	return st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO apply_requests (run_id, caller, tool, submit, explicit, url, employer_url, company, role, named, resubmit, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (run_id) DO UPDATE SET caller = excluded.caller, tool = excluded.tool, submit = excluded.submit, explicit = excluded.explicit,
			 url = excluded.url, employer_url = excluded.employer_url, company = excluded.company, role = excluded.role, named = excluded.named, resubmit = excluded.resubmit`,
			q.RunID, string(q.Caller), q.Tool, q.Submit, q.Explicit, q.URL, q.EmployerURL, q.Company, q.Role, q.Named, q.Resubmit, now.UTC().Format(stampLayout))
		return err
	})
}

// LoadRequest reads the request a run was started from.
func LoadRequest(ctx context.Context, st *store.Store, runID string) (StartRequest, error) {
	q := StartRequest{RunID: runID}
	var caller string
	err := st.Read().QueryRowContext(ctx, `SELECT caller, tool, submit, explicit, url, employer_url, company, role, named, resubmit FROM apply_requests WHERE run_id = ?`, runID).
		Scan(&caller, &q.Tool, &q.Submit, &q.Explicit, &q.URL, &q.EmployerURL, &q.Company, &q.Role, &q.Named, &q.Resubmit)
	if errors.Is(err, sql.ErrNoRows) {
		return StartRequest{}, ErrNoRequest
	}
	q.Caller = gates.Caller(caller)
	return q, err
}

// PruneRequests deletes the start requests older than RequestRetention.
func PruneRequests(ctx context.Context, st *store.Store, now time.Time) error {
	return st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM apply_requests WHERE created_at < ?`, now.Add(-RequestRetention).UTC().Format(stampLayout))
		return err
	})
}

// SweepRuns ends every run row still marked running whose process is gone: a
// run row survives a crash as running, and nothing else would ever close it.
// It returns how many rows it changed. The submissions ledger has its own
// sweep; this one only keeps the run record honest.
func SweepRuns(ctx context.Context, st *store.Store, now time.Time) (int, error) {
	n := 0
	err := st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE apply_runs SET outcome_kind = 'stopped', outcome_stage = 'record', outcome_class = 'environment',
			outcome_reason = 'the process ended before the run did; a press that was dispatched is recorded in the submissions ledger',
			finished_at = ? WHERE outcome_kind = 'running'`, now.UTC().Format(stampLayout))
		if err != nil {
			return err
		}
		c, _ := res.RowsAffected()
		n = int(c)
		return nil
	})
	return n, err
}

// company and role are the names the run is recorded under: the bound report's,
// else the list row's.
func (r *Run) company() string {
	if r.Report != nil && r.Report.Company != "" {
		return r.Report.Company
	}
	return r.Input.Company
}

func (r *Run) role() string {
	if r.Report != nil && r.Report.Role != "" {
		return r.Report.Role
	}
	return r.Input.Role
}
