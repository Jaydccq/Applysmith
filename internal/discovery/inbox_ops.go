package discovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// AddResult is the outcome of adding a posting by URL.
type AddResult struct {
	Row *InboxRow
	// Duplicate names the store that already holds the posting, and MatchID
	// the row there.
	Duplicate string
	MatchID   int64
}

// InboxAddURL canonicalizes a posting URL and adds it like a scan row: a
// posting already in the inbox, tracker, archive or history is reported, not
// added again.
func (r *Repo) InboxAddURL(ctx context.Context, rawURL string) (AddResult, error) {
	canon, err := postingURL(strings.TrimSpace(rawURL))
	if err != nil {
		return AddResult{}, fmt.Errorf("%q is not a posting URL: %w", rawURL, err)
	}
	p := manualPosting(canon)
	known, err := r.LoadKnown(ctx)
	if err != nil {
		return AddResult{}, err
	}
	if store, id, ok := known.Match(p, false); ok {
		return AddResult{Duplicate: store, MatchID: id}, nil
	}
	var id, matchID int64
	err = r.s.Write(ctx, func(tx *sql.Tx) error {
		inserted := false
		var err error
		if id, inserted, err = r.insertInbox(ctx, tx, p, rawURL, "added by url", ""); err != nil {
			return err
		}
		if !inserted {
			return tx.QueryRowContext(ctx, `SELECT id FROM inbox WHERE identity_key = ?`, p.key()).Scan(&matchID)
		}
		matchID = 0
		return r.historyUpsert(ctx, tx, p, HistoryInbox, nil, "manual")
	})
	if err != nil {
		return AddResult{}, err
	}
	if matchID != 0 {
		return AddResult{Duplicate: StoreInbox, MatchID: matchID}, nil
	}
	row, err := r.InboxGet(ctx, id)
	return AddResult{Row: &row}, err
}

// manualPosting is a posting known only by its canonical URL.
func manualPosting(canon string) Posting {
	p := Posting{Source: "manual", URL: canon, Company: hostCompany(canon)}
	if b, err := ResolveBoard(canon); err == nil {
		p.ATS, p.Board, p.Company = b.ATS, b.Slug, b.Slug
	}
	return p
}

// InboxRowFor finds the inbox row that holds the posting at rawURL, matched as
// InboxAddURL matches a posting already in the inbox. ok is false when no row
// holds it or the URL names no posting.
func (r *Repo) InboxRowFor(ctx context.Context, rawURL string) (row InboxRow, ok bool, err error) {
	canon, err := postingURL(strings.TrimSpace(rawURL))
	if err != nil {
		return InboxRow{}, false, nil
	}
	known, err := r.LoadKnown(ctx)
	if err != nil {
		return InboxRow{}, false, err
	}
	store, id, found := known.Match(manualPosting(canon), false)
	if !found || store != StoreInbox {
		return InboxRow{}, false, nil
	}
	row, err = r.InboxGet(ctx, id)
	return row, err == nil, err
}

func hostCompany(canon string) string {
	if u, err := url.Parse(canon); err == nil {
		return strings.TrimPrefix(u.Hostname(), "www.")
	}
	return ""
}

// evalRequest asks for the evaluation of a row in the given mode.
func evalRequest(row InboxRow, mode string) EvalRequest {
	return EvalRequest{InboxID: row.ID, URL: row.URL, Mode: mode, RunID: row.RunID, Source: row.Source,
		Title: row.Role, Company: row.Company, Location: row.Location, Scanned: row.Description, PostedAt: row.PostedAt}
}

// Promote enqueues rows for evaluation and moves them to queued. A row that
// is not pending is refused. gate, when given, is applied to every row; the
// first failing rule is returned in rejected and the row stays pending.
func (r *Repo) Promote(ctx context.Context, enq Enqueuer, gate Gate, ids ...int64) (queued []int64, rejected map[int64]string, err error) {
	rejected = map[int64]string{}
	for _, id := range ids {
		row, err := r.InboxGet(ctx, id)
		if err != nil {
			return queued, rejected, err
		}
		if row.Status != InboxPending {
			return queued, rejected, fmt.Errorf("%w: row %d is %s, only pending rows are promoted", ErrBadTransition, id, row.Status)
		}
		_, rule, err := r.QueueForEvaluation(ctx, row, gate, func() (string, error) {
			return enq.Enqueue(ctx, evalRequest(row, evalMode(row.Source, "")))
		})
		if err != nil {
			return queued, rejected, err
		}
		if rule != "" {
			rejected[id] = rule
			continue
		}
		queued = append(queued, id)
	}
	return queued, rejected, nil
}

// QueueForEvaluation is the step every evaluation of a pending row goes
// through, whether a promotion or a direct request starts it: the gate first
// (a failing rule is returned and the row stays pending), then pending to
// queued, then enqueue, which starts the evaluation. The job id enqueue
// returns, when it has one yet, is recorded on the row.
func (r *Repo) QueueForEvaluation(ctx context.Context, row InboxRow, gate Gate, enqueue func() (string, error)) (jobID, rule string, err error) {
	if gate != nil {
		if rule := gate(row); rule != "" {
			return "", rule, nil
		}
	}
	jobID, err = r.queueAndEnqueue(ctx, row.ID, enqueue)
	return jobID, "", err
}

// PromoteTop promotes the n best pending rows that the gate accepts.
func (r *Repo) PromoteTop(ctx context.Context, enq Enqueuer, gate Gate, n int) ([]int64, map[int64]string, error) {
	rows, err := r.InboxList(ctx, InboxQuery{Status: InboxPending})
	if err != nil {
		return nil, nil, err
	}
	var queued []int64
	rejected := map[int64]string{}
	for _, row := range rows {
		if len(queued) >= n {
			break
		}
		q, rej, err := r.Promote(ctx, enq, gate, row.ID)
		if err != nil {
			return queued, rejected, err
		}
		queued = append(queued, q...)
		for k, v := range rej {
			rejected[k] = v
		}
	}
	return queued, rejected, nil
}

// InboxEvaluateLimit is how many waiting rows one inbox.evaluate run sends to
// evaluation when the schedule names no limit.
const InboxEvaluateLimit = 20

// waitingAttempts is how many failed evaluations of a row the inbox.evaluate
// job retries: after the second failure the row waits for the user.
const waitingAttempts = 2

// WaitingResult counts one pass of EvaluateWaiting. Reviews is the quick-screen
// reviews it sent to evaluation in depth; Left is the rows still waiting
// afterwards; Capped says the day's spend limit stopped the pass.
type WaitingResult struct {
	Reviews, Queued, Rejected, Left int
	Capped                          bool
}

// Summary is the run's one line.
func (w WaitingResult) Summary() string {
	s := fmt.Sprintf("%d sent to evaluation, %d kept by the eligibility gate, %d still waiting", w.Queued, w.Rejected, w.Left)
	switch {
	case w.Reviews == 1:
		s = "1 review sent to evaluation in depth, " + s
	case w.Reviews > 1:
		s = fmt.Sprintf("%d reviews sent to evaluation in depth, ", w.Reviews) + s
	}
	if w.Capped {
		s += "; the daily model spend cap or an evaluation task's budget is reached"
	}
	return s
}

// EvaluateWaiting is the inbox.evaluate job. It sends the quick-screen reviews
// that wait first, each as the evaluation in depth that answers it (reviews,
// in their order), then the best-ranked waiting rows the gate accepts, in auto
// mode: at most limit in all, and none once capReached says the day's model
// spend limit is reached (F-1, F-8). A row the gate rejects keeps the rule in
// its reasons and uses no slot; a row whose evaluation failed twice is left to
// the user.
func (r *Repo) EvaluateWaiting(ctx context.Context, enq Enqueuer, gate Gate, limit int, capReached func(context.Context) bool, reviews []EvalRequest) (WaitingResult, error) {
	var res WaitingResult
	for _, rv := range reviews {
		if res.Reviews >= limit || ctx.Err() != nil {
			break
		}
		if capReached != nil && capReached(ctx) {
			res.Capped = true
			return r.waitingLeft(ctx, res), nil
		}
		if _, err := enq.Enqueue(ctx, rv); err != nil {
			return r.waitingLeft(ctx, res), fmt.Errorf("queueing the review %s: %w", rv.Review, err)
		}
		res.Reviews++
	}
	ids, err := r.waitingIDs(ctx)
	if err != nil {
		return res, err
	}
	for _, id := range ids {
		if res.Reviews+res.Queued >= limit || ctx.Err() != nil {
			break
		}
		row, err := r.InboxGet(ctx, id)
		if errors.Is(err, ErrNotFound) || (err == nil && !row.Waiting()) {
			continue // the row left waiting since it was listed
		}
		if err != nil {
			return r.waitingLeft(ctx, res), err
		}
		if capReached != nil && capReached(ctx) {
			res.Capped = true
			break
		}
		_, rule, err := r.QueueForEvaluation(ctx, row, gate, func() (string, error) { return enq.Enqueue(ctx, evalRequest(row, "auto")) })
		switch {
		case errors.Is(err, ErrBadTransition):
			continue // the row left waiting since it was listed
		case err != nil:
			return r.waitingLeft(ctx, res), fmt.Errorf("queueing inbox row %d: %w", row.ID, err)
		case rule != "":
			res.Rejected++
			if slices.Contains(row.Reasons, "gate:"+rule) {
				continue // an earlier pass wrote the rule
			}
			if err := r.addReason(ctx, row.ID, "gate:"+rule); err != nil {
				return r.waitingLeft(ctx, res), err
			}
			continue
		}
		res.Queued++
	}
	return r.waitingLeft(ctx, res), ctx.Err()
}

// waitingIDs lists the rows EvaluateWaiting may take, in the order InboxList
// gives pending rows: best score first, under the current target roles. A
// pass reads each row it reaches by its id, so the rows past its limit are
// never loaded with their descriptions.
func (r *Repo) waitingIDs(ctx context.Context) ([]int64, error) {
	_ = r.retarget(ctx)
	rows, err := r.s.Read().QueryContext(ctx, `SELECT id FROM inbox WHERE status = ? AND attempts < ? ORDER BY score DESC, id`, InboxPending, waitingAttempts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// HasWaiting reports whether a pending row is left that EvaluateWaiting would
// try: one whose evaluation has not failed twice.
func (r *Repo) HasWaiting(ctx context.Context) (bool, error) {
	var ok bool
	err := r.s.Read().QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM inbox WHERE status = ? AND attempts < ?)`, InboxPending, waitingAttempts).Scan(&ok)
	return ok, err
}

// waitingLeft counts the rows still waiting into a pass's result.
func (r *Repo) waitingLeft(ctx context.Context, res WaitingResult) WaitingResult {
	if n, err := r.InboxCount(context.WithoutCancel(ctx), InboxQuery{Status: InboxPending}); err == nil {
		res.Left = n
	}
	return res
}
