// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"applysmith/internal/discovery"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

// ScannerGate is the eligibility gate a scan plugs in: it runs the same pure
// rules as the evaluation entry on an inbox row, so a posting the gate would
// reject never reaches promotion. It returns the failing rules, or "".
func ScannerGate(p workspace.Profile, blocked func(company string) bool) discovery.Gate {
	settings := ResolveGate(p)
	return func(row discovery.InboxRow) string {
		in := GateInput{Title: row.Role, Location: row.Location, Text: row.Description, URL: row.URL}
		tier := ClassifyTier(row.Company, row.Description)
		switch {
		case blocked != nil && row.Company != "" && blocked(row.Company):
			in.BlockedWhy = "the employer is on the blocked-companies list or in skip_companies"
		case p.EligibilityGate.EmployerTiers[string(tier)].Policy == "block":
			in.BlockedWhy = fmt.Sprintf("employers of tier %s are blocked by the profile", tier)
		}
		var rules []string
		for _, f := range Gate(in, settings) {
			rules = append(rules, f.Rule)
		}
		return strings.Join(rules, ",")
	}
}

// BlockedFromDiscovery adapts the scan's blocked-employer rule
// (discovery.Filters.CompanyBlocked) as the blocked store and skip_companies
// stand when it is called: a gate built for one operation asks it for every
// row. A rule that cannot be read blocks no one.
func BlockedFromDiscovery(repo *discovery.Repo) func(company string) bool {
	rule, err := repo.BlockedRule(context.Background())
	if err != nil {
		return func(string) bool { return false }
	}
	return rule
}

// DiscoveryInbox adapts the pipeline inbox: a failed evaluation returns the
// row to pending with a note; any other outcome marks it evaluated.
type DiscoveryInbox struct{ Repo *discovery.Repo }

// ReleaseQueued implements Inbox.
func (d DiscoveryInbox) ReleaseQueued(ctx context.Context, note string) (int, error) {
	return d.Repo.InboxReleaseQueued(ctx, note)
}

// Release implements Inbox.
func (d DiscoveryInbox) Release(ctx context.Context, rowID int64, note string, c discovery.Charge) (bool, error) {
	if err := ignoreTransition(d.Repo.InboxEvalFailed(ctx, rowID, note, c)); err != nil {
		return false, err
	}
	row, err := d.Repo.InboxGet(ctx, rowID)
	return err == nil && row.Waiting(), nil
}

// Done implements Inbox.
func (d DiscoveryInbox) Done(ctx context.Context, rowID int64, _ Result) error {
	return ignoreTransition(d.Repo.InboxEvaluated(ctx, rowID))
}

// ignoreTransition accepts a row that is no longer queued (dismissed or
// already settled): there is nothing left to move.
func ignoreTransition(err error) error {
	if errors.Is(err, discovery.ErrBadTransition) {
		return nil
	}
	return err
}

// FillFromInbox names the inbox row on the request and takes the posting
// address, hints, scanned text and posting date it does not already carry
// from the row.
func (r *Request) FillFromInbox(row discovery.InboxRow) {
	r.InboxRowID = row.ID
	if r.PostedAt.IsZero() {
		r.PostedAt = row.PostedAt
	}
	for _, f := range []struct {
		dst  *string
		from string
	}{{&r.URL, row.URL}, {&r.Title, row.Role}, {&r.Company, row.Company}, {&r.Location, row.Location}, {&r.Scanned, row.Description}} {
		if *f.dst == "" {
			*f.dst = f.from
		}
	}
}

// ScanOutcomes attributes a shared job to the scan that requested its result.
// Completion can arrive before enqueue returns; the record update is atomic
// and idempotent for each scan/job pair.
func ScanOutcomes(repo *discovery.Repo) func(ctx context.Context, scanRun, jobID string, r Result) error {
	return func(ctx context.Context, scanRun, jobID string, r Result) error {
		return repo.ScanOutcome(ctx, scanRun, jobID, scanOutcomeName(r))
	}
}

func scanOutcomeName(r Result) string {
	switch r.Outcome {
	case OutcomeReported:
		return discovery.OutcomeReported
	case OutcomeScreened:
		return discovery.OutcomeScreened
	case OutcomeGated:
		return discovery.OutcomeGated
	case OutcomeDuplicate:
		return discovery.OutcomeDuplicate
	}
	if r.Reason == ReasonTimeout {
		return discovery.OutcomeTimedOut
	}
	return discovery.OutcomeFailed
}

// PoolEnqueuer lets a scan start evaluations in the pool. The scan does not
// wait for them; their outcomes attach to the run by job id. Priority is
// scan-started unless set: the user's own Evaluate press is user-started.
// Drain names the inbox.evaluate run that starts them, when one does.
type PoolEnqueuer struct {
	Pool     *Pool
	Priority Priority
	Drain    string
}

// Enqueue implements discovery.Enqueuer.
func (e PoolEnqueuer) Enqueue(ctx context.Context, r discovery.EvalRequest) (string, error) {
	h, err := e.Pool.Submit(ctx, Request{URL: r.URL, Text: r.Text, Scanned: r.Scanned, Title: r.Title, Company: r.Company, Location: r.Location, Source: r.Source, InboxRowID: r.InboxID, ScanRun: r.RunID, Mode: Mode(r.Mode), Priority: cmp.Or(e.Priority, PriorityScan), Review: r.Review, Drain: e.Drain, PostedAt: r.PostedAt})
	return h.RunID, err
}

// OpenReviews lists the quick-screen reviews the inbox drain evaluates in depth
// before any new row (F-8), oldest first and at most limit, each as the
// evaluation that answers it: the review's own request in mode deep, naming the
// review and no scan. A review is open while it waits for the user (not
// skipped or acknowledged), and again when every evaluation the drain started
// for it was cut short by a restart. It is never taken while an evaluation of
// its posting runs, nor once its posting was evaluated in depth: a final
// report, or an evaluation in depth of the posting or one started for the
// review that ended other than interrupted. A review whose inbox row the user
// dismissed, or whose posting closed, is not taken either.
func OpenReviews(ctx context.Context, st *store.Store, limit int) ([]discovery.EvalRequest, error) {
	rows, err := st.Read().QueryContext(ctx, `WITH answers AS (SELECT json_extract(input_json, '$.Review') AS review, failure_reason = ?1 AS interrupted
			FROM runs WHERE kind = 'evaluate' AND json_extract(input_json, '$.Review') IS NOT NULL)
		SELECT r.id, r.input_json FROM runs r
		JOIN evaluation_jobs j ON j.run_id = r.id
		JOIN board_entries b ON b.id = r.id
		WHERE r.kind = 'evaluate' AND r.status = 'review_required'
			AND ((b.state = 'needs-you' AND b.reason = 'review' AND b.acked_at IS NULL)
				OR (b.state = 'done' AND r.id IN (SELECT review FROM answers)))
			AND r.id NOT IN (SELECT review FROM answers WHERE NOT interrupted)
			AND NOT EXISTS (SELECT 1 FROM report_index x WHERE x.identity_key = j.identity_key AND x.kind = 'final')
			AND NOT EXISTS (SELECT 1 FROM evaluation_jobs l JOIN runs lr ON lr.id = l.run_id
				WHERE l.identity_key = j.identity_key AND l.run_id <> r.id
					AND (lr.status IN ('queued', 'running') OR (l.mode = 'deep' AND lr.failure_reason <> ?1)))
			AND NOT EXISTS (SELECT 1 FROM inbox i WHERE i.id = j.inbox_row_id AND i.status IN (?2, ?3))
		ORDER BY j.created_at, r.id LIMIT ?4`, ReasonInterrupted, discovery.InboxDismissed, discovery.InboxExpired, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []discovery.EvalRequest
	for rows.Next() {
		var id, input string
		if err := rows.Scan(&id, &input); err != nil {
			return nil, err
		}
		var r Request
		if err := json.Unmarshal([]byte(input), &r); err != nil {
			return nil, fmt.Errorf("the request of review %s: %w", id, err)
		}
		out = append(out, discovery.EvalRequest{InboxID: r.InboxRowID, URL: r.URL, Mode: string(ModeDeep), Source: r.Source,
			Text: r.Text, Title: r.Title, Company: r.Company, Location: r.Location, Scanned: r.Scanned, PostedAt: r.PostedAt, Review: id})
	}
	return out, rows.Err()
}
