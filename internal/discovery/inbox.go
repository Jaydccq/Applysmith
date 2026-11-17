package discovery

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"applysmith/internal/workspace"
)

// Inbox statuses.
const (
	InboxPending   = "pending"
	InboxQueued    = "queued"
	InboxEvaluated = "evaluated"
	InboxDismissed = "dismissed"
	InboxExpired   = "expired"
)

// ErrBadTransition is a status change the inbox does not allow.
var ErrBadTransition = errors.New("discovery: inbox transition not allowed")

// InboxRow is one row of the pipeline inbox.
type InboxRow struct {
	ID                int64
	URL, OriginalURL  string
	Aliases           []string
	IdentityKey       string
	Company, Role     string
	Location          string
	Source, Detail    string
	Score             int
	Reasons           []string
	EarlyCareer       bool
	ClearanceRequired bool
	RequiredYears     *int
	Flags             []string
	Description       string
	PostedAt          time.Time
	Notes             string
	Status            string
	Attempts          int
	JobID, RunID      string
	FirstSeen         time.Time
	Updated           time.Time
}

// Posting is the row seen as a posting.
func (x InboxRow) Posting() Posting {
	return Posting{Source: x.Source, URL: x.URL, Company: x.Company, Title: x.Role, Location: x.Location, PostedAt: x.PostedAt,
		Description: x.Description, Score: x.Score, Reasons: x.Reasons, Flags: x.Flags,
		Signals: Signals{EarlyCareer: x.EarlyCareer, ClearanceRequired: x.ClearanceRequired, RequiredYears: x.RequiredYears}}
}

const inboxCols = `id, url, original_url, aliases_json, identity_key, company, role, location, source, detail, score, reasons_json,
	early_career, clearance_required, required_years, flags_json, description, posted_at, notes, status, attempts, job_id, run_id, first_seen_at, updated_at`

func scanInbox(rows *sql.Rows) ([]InboxRow, error) {
	defer rows.Close()
	var out []InboxRow
	for rows.Next() {
		var x InboxRow
		var aliases, reasons, flags, first, upd string
		var early, clr int
		var years sql.NullInt64
		var posted, job, run sql.NullString
		if err := rows.Scan(&x.ID, &x.URL, &x.OriginalURL, &aliases, &x.IdentityKey, &x.Company, &x.Role, &x.Location, &x.Source, &x.Detail,
			&x.Score, &reasons, &early, &clr, &years, &flags, &x.Description, &posted, &x.Notes, &x.Status, &x.Attempts, &job, &run, &first, &upd); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(aliases), &x.Aliases)
		_ = json.Unmarshal([]byte(reasons), &x.Reasons)
		x.Reasons = slices.DeleteFunc(x.Reasons, unspent)
		_ = json.Unmarshal([]byte(flags), &x.Flags)
		x.EarlyCareer, x.ClearanceRequired = early == 1, clr == 1
		if years.Valid {
			v := int(years.Int64)
			x.RequiredYears = &v
		}
		x.PostedAt, x.JobID, x.RunID = parseStamp(posted), job.String, run.String
		x.FirstSeen, _ = time.Parse(timeLayout, first)
		x.Updated, _ = time.Parse(timeLayout, upd)
		out = append(out, x)
	}
	return out, rows.Err()
}

// InboxQuery filters a listing. Zero values mean no filter.
type InboxQuery struct {
	Status string
	RunID  string
	// Text keeps the rows whose company, role, location or source holds it,
	// ignoring case.
	Text string
	// Since keeps the rows first seen at or after it, when set: the rows the
	// day's and the week funnel's found counts count.
	Since time.Time
	// Newest lists the rows found last first instead of best score first.
	Newest bool
	Limit  int
	// Offset skips that many rows of the ordered listing; it pages with Limit.
	Offset int
}

// InboxList lists rows best score first, or found last first. A listing by
// score that holds pending rows scores them under the current target roles
// first (RankWith); an unreadable profile or a read-only store keeps the
// admitted scores.
func (r *Repo) InboxList(ctx context.Context, q InboxQuery) ([]InboxRow, error) {
	if !q.Newest && (q.Status == "" || q.Status == InboxPending) {
		_ = r.retarget(ctx)
	}
	where, args := q.where()
	order := ` ORDER BY score DESC, id`
	if q.Newest {
		order = ` ORDER BY first_seen_at DESC, id DESC`
	}
	sqlText := `SELECT ` + inboxCols + ` FROM inbox` + where + order
	switch {
	case q.Limit > 0:
		sqlText += fmt.Sprintf(` LIMIT %d OFFSET %d`, q.Limit, max(q.Offset, 0))
	case q.Offset > 0:
		sqlText += fmt.Sprintf(` LIMIT -1 OFFSET %d`, q.Offset)
	}
	rows, err := r.s.Read().QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	return scanInbox(rows)
}

func (q InboxQuery) where() (string, []any) {
	var conds []string
	var args []any
	if q.Status != "" {
		conds = append(conds, "status = ?")
		args = append(args, q.Status)
	}
	if q.RunID != "" {
		conds = append(conds, "run_id = ?")
		args = append(args, q.RunID)
	}
	if q.Text != "" {
		conds = append(conds, `(company || ' ' || role || ' ' || location || ' ' || source) LIKE ? ESCAPE '\'`)
		args = append(args, "%"+strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(q.Text)+"%")
	}
	if !q.Since.IsZero() {
		conds = append(conds, "first_seen_at >= ?")
		args = append(args, q.Since.UTC().Format(timeLayout))
	}
	if len(conds) == 0 {
		return "", nil
	}
	return ` WHERE ` + strings.Join(conds, " AND "), args
}

// InboxCount counts the rows a query selects, ignoring Limit and Offset.
func (r *Repo) InboxCount(ctx context.Context, q InboxQuery) (int, error) {
	where, args := q.where()
	var n int
	err := r.s.Read().QueryRowContext(ctx, `SELECT count(*) FROM inbox`+where, args...).Scan(&n)
	return n, err
}

// InboxCounts counts the rows per status, of those first seen since a time
// when it is set; a status with no rows is listed with zero.
func (r *Repo) InboxCounts(ctx context.Context, since time.Time) (map[string]int, error) {
	out := map[string]int{InboxPending: 0, InboxQueued: 0, InboxEvaluated: 0, InboxDismissed: 0, InboxExpired: 0}
	where, args := InboxQuery{Since: since}.where()
	rows, err := r.s.Read().QueryContext(ctx, `SELECT status, count(*) FROM inbox`+where+` GROUP BY status`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		out[status] = n
	}
	return out, rows.Err()
}

// InboxLocation is the location the scans recorded for the posting of this
// identity key, "" when no row holds it.
func (r *Repo) InboxLocation(ctx context.Context, key string) (string, error) {
	var loc string
	err := r.s.Read().QueryRowContext(ctx, `SELECT location FROM inbox WHERE identity_key = ?`, key).Scan(&loc)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return loc, err
}

func (r *Repo) InboxGet(ctx context.Context, id int64) (InboxRow, error) {
	rows, err := r.s.Read().QueryContext(ctx, `SELECT `+inboxCols+` FROM inbox WHERE id = ?`, id)
	if err != nil {
		return InboxRow{}, err
	}
	list, err := scanInbox(rows)
	if err != nil {
		return InboxRow{}, err
	}
	if len(list) == 0 {
		return InboxRow{}, fmt.Errorf("%w: inbox row %d", ErrNotFound, id)
	}
	return list[0], nil
}

// insertInbox writes a new row inside a transaction. inserted is false when a
// row with the same identity key exists already: another writer got there
// first, which is a duplicate and not a failure.
func (r *Repo) insertInbox(ctx context.Context, tx *sql.Tx, p Posting, original, detail, runID string) (id int64, inserted bool, err error) {
	reasons, _ := json.Marshal(nonNilStrings(p.Reasons))
	flags, _ := json.Marshal(nonNilStrings(p.Flags))
	var years any
	if p.Signals.RequiredYears != nil {
		years = *p.Signals.RequiredYears
	}
	now := r.stamp()
	if original == "" {
		original = p.URL
	}
	aliases, _ := json.Marshal(nonNilStrings(p.Aliases))
	res, err := tx.ExecContext(ctx, `INSERT INTO inbox (url, original_url, aliases_json, identity_key, company, role, location, source, detail, score, reasons_json,
		early_career, clearance_required, required_years, flags_json, description, posted_at, status, run_id, first_seen_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?)
		ON CONFLICT (identity_key) DO NOTHING`,
		p.URL, original, string(aliases), p.key(), p.Company, p.Title, p.Location, p.Source, detail, p.Score, string(reasons),
		boolInt(p.Signals.EarlyCareer), boolInt(p.Signals.ClearanceRequired), years, string(flags), p.Description, fmtTime(p.PostedAt),
		nullable(runID), now, now)
	if err != nil {
		return 0, false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return 0, false, err
	}
	id, err = res.LastInsertId()
	return id, true, err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// transition moves a row between statuses under the allowed edges.
func (r *Repo) transition(ctx context.Context, id int64, to string, allowed []string, set string, args ...any) error {
	return r.s.Write(ctx, func(tx *sql.Tx) error {
		return r.transitionInTx(ctx, tx, id, to, allowed, set, args...)
	})
}

// transitionInTx keeps admission's state change and attempt snapshot atomic.
func (r *Repo) transitionInTx(ctx context.Context, tx *sql.Tx, id int64, to string, allowed []string, set string, args ...any) error {
	var cur string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM inbox WHERE id = ?`, id).Scan(&cur); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: inbox row %d", ErrNotFound, id)
		}
		return err
	}
	ok := false
	for _, a := range allowed {
		ok = ok || a == cur
	}
	if !ok {
		return fmt.Errorf("%w: %s to %s", ErrBadTransition, cur, to)
	}
	q := `UPDATE inbox SET status = ?, updated_at = ?` + set + ` WHERE id = ?`
	all := append([]any{to, r.stamp()}, args...)
	_, err := tx.ExecContext(ctx, q, append(all, id)...)
	return err
}

// queueAndEnqueue is the one way a row is handed to the evaluation pool. The
// row is marked queued before the job exists, because a pool may run the job
// to completion inside Enqueue and the finishing transition needs a queued
// row. The job id is recorded afterwards without a status condition, so a job
// that already finished does not undo it. When the enqueue fails the row goes
// back to pending with the attempt uncounted and the failure noted. Once the
// row is queued, bounded accounting must finish even if the caller cancels.
// It owns only that attempt; a newer retry or user terminal state is preserved.
func (r *Repo) queueAndEnqueue(ctx context.Context, id int64, enqueue func() (string, error)) (string, error) {
	var attempt int
	if err := r.s.Write(ctx, func(tx *sql.Tx) error {
		if err := r.transitionInTx(ctx, tx, id, InboxQueued, []string{InboxPending}, `, attempts = attempts + 1`); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT attempts FROM inbox WHERE id = ?`, id).Scan(&attempt)
	}); err != nil {
		return "", err
	}
	jobID, err := enqueue()
	finishCtx, finish := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer finish()
	if err != nil {
		note := "enqueue failed: " + err.Error()
		if rerr := r.s.Write(finishCtx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(finishCtx, `UPDATE inbox SET status = ?, attempts = attempts - 1, notes = ?, updated_at = ? WHERE id = ? AND attempts = ? AND status = ?`,
				InboxPending, note, r.stamp(), id, attempt, InboxQueued)
			return err
		}); rerr != nil {
			return "", errors.Join(err, fmt.Errorf("reverting inbox row %d: %w", id, rerr))
		}
		return "", err
	}
	if jobID == "" {
		return "", nil
	}
	err = r.s.Write(finishCtx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(finishCtx, `UPDATE inbox SET job_id = ?, updated_at = ? WHERE id = ? AND attempts = ?`, jobID, r.stamp(), id, attempt)
		return err
	})
	if err != nil {
		return jobID, fmt.Errorf("recording job %s on inbox row %d: %w", jobID, id, err)
	}
	return jobID, nil
}

// InboxEvaluated moves queued to evaluated when the evaluation finishes with
// any outcome except failure.
func (r *Repo) InboxEvaluated(ctx context.Context, id int64) error {
	return r.transition(ctx, id, InboxEvaluated, []string{InboxQueued}, ``)
}

// Charge is what a failed evaluation may cost its inbox row (InboxEvalFailed).
type Charge int

const (
	// ChargeAttempt spends one of the row's attempts.
	ChargeAttempt Charge = iota
	// ChargeUndecided is for a read that decided nothing about the posting:
	// the app's own machine stopped it, or the posting's site throttled it.
	// It spends no attempt the row's first freeFailures times and one each
	// time after, so a page that always fails so is left to the user instead
	// of taking a slot of every drain run.
	ChargeUndecided
	// ChargeNone spends nothing: a shutdown cut the evaluation short, or no
	// model was routed for it.
	ChargeNone
)

// freeFailures is how many undecided reads a row is evaluated again for
// without spending an attempt. The drain takes a row at most once a run and
// the scan packs drain once or twice a day, so a throttle or a busy machine has
// a day and a half or more to pass, and a page that always fails so is left to
// the user after five runs: three free, then its two attempts.
const freeFailures = 3

// unspentTag counts a row's free undecided reads in its stored reasons; a row
// read leaves it out, so no reader of the reasons shows it.
const unspentTag = "unspent-failures:"

func unspent(reason string) bool { return strings.HasPrefix(reason, unspentTag) }

// storedReasons are a row's reasons as stored, its count of free failures
// included.
func storedReasons(ctx context.Context, tx *sql.Tx, id int64) ([]string, error) {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT reasons_json FROM inbox WHERE id = ?`, id).Scan(&raw); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var reasons []string
	_ = json.Unmarshal([]byte(raw), &reasons)
	return reasons, nil
}

// Waiting reports whether the inbox drain still takes the row: it waits, with
// an attempt left.
func (x InboxRow) Waiting() bool { return x.Status == InboxPending && x.Attempts < waitingAttempts }

// InboxEvalFailed moves queued back to pending with a failure note and takes
// what the failure costs the row. A failure that spends nothing leaves the
// attempt uncounted, as a refused enqueue does.
func (r *Repo) InboxEvalFailed(ctx context.Context, id int64, note string, c Charge) error {
	return r.s.Write(ctx, func(tx *sql.Tx) error {
		set, args := `, notes = ?`, []any{note}
		if c == ChargeUndecided {
			reasons, err := storedReasons(ctx, tx, id)
			if err != nil {
				return err
			}
			i := slices.IndexFunc(reasons, unspent)
			n := 0
			if i < 0 {
				i, reasons = len(reasons), append(reasons, "")
			} else {
				n, _ = strconv.Atoi(strings.TrimPrefix(reasons[i], unspentTag))
			}
			if n < freeFailures {
				reasons[i] = unspentTag + strconv.Itoa(n+1)
				b, _ := json.Marshal(reasons)
				set, args, c = set+`, reasons_json = ?`, append(args, string(b)), ChargeNone
			}
		}
		if c == ChargeNone {
			set += `, attempts = attempts - 1`
		}
		return r.transitionInTx(ctx, tx, id, InboxPending, []string{InboxQueued}, set, args...)
	})
}

// InboxReleaseQueued returns every queued row to pending with a note and says
// how many moved. It is for a start with no evaluation in flight: a row still
// queued then lost its job (or never got one) to a crash.
func (r *Repo) InboxReleaseQueued(ctx context.Context, note string) (int, error) {
	var n int64
	err := r.s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE inbox SET status = ?, notes = ?, updated_at = ? WHERE status = ?`, InboxPending, note, r.stamp(), InboxQueued)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return int(n), err
}

// InboxDismiss is a user or agent action.
func (r *Repo) InboxDismiss(ctx context.Context, id int64) error {
	return r.transition(ctx, id, InboxDismissed, []string{InboxPending, InboxQueued, InboxEvaluated, InboxExpired}, ``)
}

// InboxRestore puts a dismissed row back to waiting: the undo of a dismissal.
func (r *Repo) InboxRestore(ctx context.Context, id int64) error {
	return r.transition(ctx, id, InboxPending, []string{InboxDismissed}, ``)
}

// InboxExpire records a liveness result.
func (r *Repo) InboxExpire(ctx context.Context, id int64) error {
	return r.transition(ctx, id, InboxExpired, []string{InboxPending, InboxQueued, InboxEvaluated}, ``)
}

// addReason adds a reason to a row's stored ones unless it is there already (a
// gate rejection keeps its rule there).
func (r *Repo) addReason(ctx context.Context, id int64, reason string) error {
	return r.s.Write(ctx, func(tx *sql.Tx) error {
		reasons, err := storedReasons(ctx, tx, id)
		if err != nil || slices.Contains(reasons, reason) {
			return err
		}
		b, _ := json.Marshal(append(reasons, reason))
		_, err = tx.ExecContext(ctx, `UPDATE inbox SET reasons_json = ?, updated_at = ? WHERE id = ?`, string(b), r.stamp(), id)
		return err
	})
}

// retargetMeta keeps the target roles the pending rows were last scored
// under, as a digest.
const retargetMeta = "discovery.inbox_targets"

// RankBasis reads the seeker's files ranking and the blocked-employer rule
// (BlockedRule) depend on.
type RankBasis func() (workspace.Profile, workspace.Portals, error)

// RankWith makes the score-ordered listings of pending rows follow the
// current target roles: an inbox row keeps the score it was admitted with, so
// a target role saved after a scan would otherwise never reach the rows
// already waiting.
func (r *Repo) RankWith(b RankBasis) { r.basis = b }

// retarget re-scores the pending rows' target terms when the target roles
// differ from the ones they were scored under; every other term stays as
// admitted.
func (r *Repo) retarget(ctx context.Context) error {
	if r.basis == nil {
		return nil
	}
	profile, portals, err := r.basis()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(profile.Targets.Roles)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	var have string
	switch err := r.s.Read().QueryRowContext(ctx, `SELECT value FROM workspace_meta WHERE key = ?`, retargetMeta).Scan(&have); {
	case err == nil && have == digest:
		return nil
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return err
	}
	targets := NewRankTargets(profile.Targets.Roles)
	return r.s.Write(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, company, role, score, reasons_json FROM inbox WHERE status = ?`, InboxPending)
		if err != nil {
			return err
		}
		type change struct {
			id      int64
			score   int
			reasons string
		}
		var changes []change
		for rows.Next() {
			var id int64
			var company, title, reasonsJSON string
			var score int
			if err := rows.Scan(&id, &company, &title, &score, &reasonsJSON); err != nil {
				rows.Close()
				return err
			}
			var reasons []string
			_ = json.Unmarshal([]byte(reasonsJSON), &reasons)
			boosted := slices.ContainsFunc(reasons, func(s string) bool { return strings.HasPrefix(s, "seniority(") }) || boostsFor(portals, company)
			ns, nr := retargetRow(title, score, reasons, boosted, targets)
			if b, _ := json.Marshal(nr); ns != score || string(b) != reasonsJSON {
				changes = append(changes, change{id, ns, string(b)})
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, c := range changes {
			if _, err := tx.ExecContext(ctx, `UPDATE inbox SET score = ?, reasons_json = ? WHERE id = ?`, c.score, c.reasons, c.id); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO workspace_meta (key, value) VALUES (?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`, retargetMeta, digest)
		return err
	})
}

// boostsFor reports whether the title rule a company is scanned with has
// seniority weights: its own filter when it has one, else the shared one.
func boostsFor(portals workspace.Portals, company string) bool {
	for _, c := range portals.Companies {
		if c.Filters.Title != nil && strings.EqualFold(c.Name, company) {
			return len(c.Filters.Title.SeniorityBoost) > 0
		}
	}
	return len(portals.Filters.Title.SeniorityBoost) > 0
}

// retargetRow replaces a row's target terms with the ones the targets give
// now, after its title hits as Rank orders them.
func retargetRow(title string, score int, reasons []string, boosted bool, t RankTargets) (int, []string) {
	kept := make([]string, 0, len(reasons))
	at := 0
	for _, r := range reasons {
		if why, n, ok := reasonTerm(r); ok && (strings.HasPrefix(why, "target-role(") || why == "senior-wording" || why == "early-career") {
			score -= n
			continue
		}
		kept = append(kept, r)
		if strings.HasPrefix(r, "title(") {
			at = len(kept)
		}
	}
	var fresh []string
	for _, tt := range targetTerms(title, boosted, t) {
		score += tt.n
		fresh = append(fresh, fmt.Sprintf("%s:%+d", tt.why, tt.n))
	}
	return score, slices.Insert(kept, at, fresh...)
}

// reasonTerm reads a scored reason, "why:+n".
func reasonTerm(r string) (string, int, bool) {
	i := strings.LastIndex(r, ":")
	if i < 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(r[i+1:])
	return r[:i], n, err == nil
}
