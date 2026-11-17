// Clean-room: docs/provenance.md#CR-LIVENESS

package discovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"applysmith/internal/identity"
	"applysmith/internal/tracker"
)

// Audit bounds.
const (
	LivenessMethod      = "browser"
	LivenessInboxAge    = 7 * day
	LivenessRecheck     = 30 * day
	LivenessAuditLimit  = 200
	livenessConcurrency = 2
	livenessHostGap     = 2 * time.Second
	// FlagNeedsReview marks an inbox row whose URL shows another posting.
	FlagNeedsReview = "needs-review"
)

// LivenessRecord is a stored result.
type LivenessRecord struct {
	IdentityKey, URL string
	Liveness
	Method    string
	CheckedAt time.Time
}

// LivenessSave stores the latest result of an identity.
func (r *Repo) LivenessSave(ctx context.Context, key, rawURL string, l Liveness, method string) error {
	return r.s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO liveness_checks (identity_key, url, verdict, reason, method, checked_at) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (identity_key) DO UPDATE SET url = excluded.url, verdict = excluded.verdict, reason = excluded.reason,
				method = excluded.method, checked_at = excluded.checked_at`, key, rawURL, l.Verdict, l.Reason, method, r.stamp())
		return err
	})
}

// LivenessGet returns the stored result of an identity.
func (r *Repo) LivenessGet(ctx context.Context, key string) (LivenessRecord, bool, error) {
	var rec LivenessRecord
	var at string
	err := r.s.Read().QueryRowContext(ctx, `SELECT identity_key, url, verdict, reason, method, checked_at FROM liveness_checks WHERE identity_key = ?`, key).
		Scan(&rec.IdentityKey, &rec.URL, &rec.Verdict, &rec.Reason, &rec.Method, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return rec, false, nil
	}
	rec.CheckedAt, _ = time.Parse(timeLayout, at)
	return rec, err == nil, err
}

// InboxAddFlag adds a flag to a row's flags.
func (r *Repo) InboxAddFlag(ctx context.Context, id int64, flag string) error {
	return r.s.Write(ctx, func(tx *sql.Tx) error {
		var raw string
		if err := tx.QueryRowContext(ctx, `SELECT flags_json FROM inbox WHERE id = ?`, id).Scan(&raw); err != nil {
			return err
		}
		var flags []string
		_ = json.Unmarshal([]byte(raw), &flags)
		for _, f := range flags {
			if f == flag {
				return nil
			}
		}
		b, _ := json.Marshal(append(flags, flag))
		_, err := tx.ExecContext(ctx, `UPDATE inbox SET flags_json = ?, updated_at = ? WHERE id = ?`, string(b), r.stamp(), id)
		return err
	})
}

// Checker classifies posting URLs and keeps the results.
type Checker struct {
	Repo    *Repo
	Reader  PageReader
	Tracker *tracker.Tracker
	// Sleep is injectable for tests.
	Sleep func(context.Context, time.Duration) error

	pace pacer
}

// Check reads one URL in the browser, classifies it and stores the result.
// Nothing else changes; the audit applies the effects.
func (c *Checker) Check(ctx context.Context, rawURL string, exp Expect) (Liveness, error) {
	canon, err := postingURL(rawURL)
	if err != nil {
		return Liveness{}, err
	}
	if c.Reader == nil {
		return Liveness{}, errors.New("no browser is available to read the page")
	}
	if err := c.pace.wait(ctx, hostOf(canon), c.Repo.now, c.sleep()); err != nil {
		return Liveness{}, err
	}
	pg, err := c.Reader.Read(ctx, canon)
	if err != nil {
		return Liveness{}, err
	}
	if embed := AshbyEmbedURL(canon); embed != "" && (pg.Status == 404 || pg.Status == 410) {
		if form, ferr := c.Reader.Read(ctx, embed); ferr == nil && form.Status > 0 && form.Status < 400 {
			pg = form
		}
	}
	l := ClassifyLiveness(canon, pg, exp)
	key := identity.Posting{URL: canon}.IdentityKey()
	return l, c.Repo.LivenessSave(ctx, key, canon, l, LivenessMethod)
}

// Record keeps a result a browser read reached outside Check, such as the page
// read of an evaluation, and applies it to the rows of the posting as the audit
// does. rowID, when set, names the inbox row the read was for: it takes the
// result too, whatever identity key it was stored under.
func (c *Checker) Record(ctx context.Context, rawURL string, l Liveness, rowID int64) error {
	canon, err := postingURL(rawURL)
	if err != nil {
		return err
	}
	key := identity.Posting{URL: canon}.IdentityKey()
	if err := c.Repo.LivenessSave(ctx, key, canon, l, LivenessMethod); err != nil {
		return err
	}
	if err := c.apply(ctx, auditRow{key: key, url: canon}, l); err != nil {
		return err
	}
	if rowID > 0 && l.Verdict == LiveExpired {
		if err := c.Repo.InboxExpire(ctx, rowID); err != nil && !errors.Is(err, ErrBadTransition) {
			return err
		}
	}
	return nil
}

// BeforeEvaluation is the optional probe ahead of an evaluation: a URL with a
// stored result is answered from it, any other is read once with the expected
// company and role, so a wrong URL is caught at entry. It is best effort: a
// failure returns an unknown result and never an error.
func (c *Checker) BeforeEvaluation(ctx context.Context, rawURL string, exp Expect) Liveness {
	canon, err := postingURL(rawURL)
	if err != nil {
		return Liveness{LiveUncertain, "unknown"}
	}
	key := identity.Posting{URL: canon}.IdentityKey()
	if rec, ok, err := c.Repo.LivenessGet(ctx, key); err == nil && ok {
		return rec.Liveness
	}
	l, err := c.Check(ctx, canon, exp)
	if err != nil {
		return Liveness{LiveUncertain, "unknown"}
	}
	return l
}

func (c *Checker) sleep() func(context.Context, time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep
	}
	return sleepCtx
}

// pacer keeps requests to one host a fixed gap apart.
type pacer struct {
	mu   sync.Mutex
	next map[string]time.Time
}

func (p *pacer) wait(ctx context.Context, host string, now func() time.Time, sleep func(context.Context, time.Duration) error) error {
	p.mu.Lock()
	if p.next == nil {
		p.next = map[string]time.Time{}
	}
	at := p.next[host]
	if t := now(); at.Before(t) {
		at = t
	}
	p.next[host] = at.Add(livenessHostGap)
	d := at.Sub(now())
	p.mu.Unlock()
	return sleep(ctx, d)
}

// auditRow is one row the audit may check.
type auditRow struct {
	key, url, company, role string
	checked                 time.Time
}

// AuditResult counts what an audit did.
type AuditResult struct {
	Considered, Skipped, Checked, Failed int
	Expired, Mismatch, Uncertain, Active int
}

// Audit checks inbox rows older than seven days and evaluated tracker rows
// with no application. It skips an identity checked within 30 days, checks at
// most 200 per run, the longest unchecked first, and at most two pages at
// once.
func (c *Checker) Audit(ctx context.Context) (AuditResult, error) {
	rows, skipped, err := c.auditScope(ctx)
	if err != nil {
		return AuditResult{}, err
	}
	res := AuditResult{Considered: len(rows) + skipped, Skipped: skipped}
	var mu sync.Mutex
	sem := make(chan struct{}, livenessConcurrency)
	var wg sync.WaitGroup
	for _, row := range rows {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return res, ctx.Err()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			l, err := c.Check(ctx, row.url, Expect{Company: row.company, Role: row.role})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				res.Failed++
				return
			}
			res.Checked++
			switch l.Verdict {
			case LiveActive:
				res.Active++
			case LiveExpired:
				res.Expired++
			case LiveMismatch:
				res.Mismatch++
			default:
				res.Uncertain++
			}
			if err := c.apply(ctx, row, l); err != nil {
				res.Failed++
			}
		}()
	}
	wg.Wait()
	return res, ctx.Err()
}

// auditScope lists the rows to check, oldest check first, and how many were
// skipped for a recent check.
func (c *Checker) auditScope(ctx context.Context) ([]auditRow, int, error) {
	now := c.Repo.now()
	byKey := map[string]*auditRow{}
	var order []string
	add := func(rows *sql.Rows, err error) error {
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a auditRow
			if err := rows.Scan(&a.key, &a.url, &a.company, &a.role); err != nil {
				return err
			}
			if a.url == "" || a.key == "" || byKey[a.key] != nil {
				continue
			}
			byKey[a.key] = &a
			order = append(order, a.key)
		}
		return rows.Err()
	}
	cutoff := now.Add(-LivenessInboxAge).UTC().Format(timeLayout)
	if err := add(c.Repo.s.Read().QueryContext(ctx, `SELECT identity_key, url, company, role FROM inbox
		WHERE status IN ('pending', 'queued') AND first_seen_at < ? ORDER BY first_seen_at`, cutoff)); err != nil {
		return nil, 0, err
	}
	if err := add(c.Repo.s.Read().QueryContext(ctx, `SELECT identity_key, posting_url, company, role FROM tracker_entries
		WHERE status = 'evaluated' AND archived_at IS NULL AND posting_url <> '' ORDER BY id`)); err != nil {
		return nil, 0, err
	}
	var rows []auditRow
	skipped := 0
	for _, k := range order {
		a := byKey[k]
		if rec, ok, err := c.lastCheck(ctx, *a); err != nil {
			return nil, 0, err
		} else if ok {
			if now.Sub(rec.CheckedAt) < LivenessRecheck {
				skipped++
				continue
			}
			a.checked = rec.CheckedAt
		}
		rows = append(rows, *a)
	}
	// Never-checked rows first, then the longest-ago check; stable within.
	sortAuditRows(rows)
	if len(rows) > LivenessAuditLimit {
		rows = rows[:LivenessAuditLimit]
	}
	return rows, skipped, nil
}

// lastCheck is the latest stored result of a row: the one under its own key,
// or the one under its address's key, where every check of an address is
// stored; a row keyed by its board's job id has the two apart.
func (c *Checker) lastCheck(ctx context.Context, a auditRow) (LivenessRecord, bool, error) {
	rec, ok, err := c.Repo.LivenessGet(ctx, a.key)
	if err != nil {
		return rec, false, err
	}
	byURL := identity.Posting{URL: a.url}.IdentityKey()
	if byURL == "" || byURL == a.key {
		return rec, ok, nil
	}
	urec, uok, err := c.Repo.LivenessGet(ctx, byURL)
	if err != nil {
		return rec, false, err
	}
	if uok && (!ok || urec.CheckedAt.After(rec.CheckedAt)) {
		return urec, true, nil
	}
	return rec, ok, nil
}

func sortAuditRows(rows []auditRow) {
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].checked.Before(rows[j].checked) })
}

// apply carries a result to the rows that share the identity: an expired
// inbox row becomes expired and a mismatch flags it for review; an unapplied
// tracker row gets a note and an event with its status untouched.
func (c *Checker) apply(ctx context.Context, row auditRow, l Liveness) error {
	if l.Verdict != LiveExpired && l.Verdict != LiveMismatch {
		return nil
	}
	ids, err := c.rowsOf(ctx, `SELECT id FROM inbox WHERE identity_key = ?`, row.key)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if l.Verdict == LiveExpired {
			if err := c.Repo.InboxExpire(ctx, id); err != nil && !errors.Is(err, ErrBadTransition) {
				return err
			}
		} else if err := c.Repo.InboxAddFlag(ctx, id, FlagNeedsReview); err != nil {
			return err
		}
	}
	if c.Tracker == nil {
		return nil
	}
	tids, err := c.rowsOf(ctx, `SELECT id FROM tracker_entries WHERE identity_key = ? AND status = 'evaluated'`, row.key)
	if err != nil {
		return err
	}
	for _, id := range tids {
		if _, err := c.Tracker.AddNote(ctx, id, tracker.ActorAgent, fmt.Sprintf("liveness: %s (%s)", l.Verdict, l.Reason), 0); err != nil {
			return err
		}
	}
	return nil
}

func (c *Checker) rowsOf(ctx context.Context, q string, args ...any) ([]int64, error) {
	rows, err := c.Repo.s.Read().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
