package ledger

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"applysmith/internal/gates"
	"applysmith/internal/identity"
)

// record is a submissions row as the guard reads it.
type record struct {
	id          int64
	claimID     string
	runID       string
	identityKey string
	url         string
	company     string
	role        string
	report      int64
	state       gates.LedgerState
	confirmed   bool
	at          int64
	pid         int
}

// owner is the run a row belongs to: the apply run that claimed it, else the
// claim itself (the extension's step walker), else the observed row alone.
func (r record) owner() string {
	switch {
	case r.runID != "":
		return r.runID
	case r.claimID != "":
		return r.claimID
	}
	return fmt.Sprintf("observed-%d", r.id)
}

// trackerRow is a tracker row as the guard reads it.
type trackerRow struct {
	id      int64
	company string
	role    string
	key     string
	aliases []string
	status  string
	report  int64
}

func (t trackerRow) hasKey(k string) bool {
	if t.key == k {
		return true
	}
	for _, a := range t.aliases {
		if a == k {
			return true
		}
	}
	return false
}

func parseStamp(s string) (int64, error) {
	t, err := time.Parse(stampLayout, s)
	if err != nil {
		return 0, err
	}
	return t.UnixMilli(), nil
}

// queryer is what *sql.Tx and *sql.DB share.
type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// loadRecords reads every ledger row. The time a row counts from is its claim
// time while it is submitting and its last write otherwise.
func loadRecords(ctx context.Context, q queryer, where string, args ...any) ([]record, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, COALESCE(claim_id, ''), COALESCE(run_id, ''), identity_key, url, company, role, COALESCE(report_number, 0),
		state, confirmed, COALESCE(claimed_at, created_at), updated_at, COALESCE(pid, 0) FROM submissions `+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []record
	for rows.Next() {
		var r record
		var state, claimed, updated string
		if err := rows.Scan(&r.id, &r.claimID, &r.runID, &r.identityKey, &r.url, &r.company, &r.role, &r.report, &state, &r.confirmed, &claimed, &updated, &r.pid); err != nil {
			return nil, err
		}
		r.state = gates.LedgerState(state)
		stamp := updated
		if r.state == gates.StateSubmitting {
			stamp = claimed
		}
		if r.at, err = parseStamp(stamp); err != nil {
			return nil, fmt.Errorf("submission %d: %w", r.id, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// loadTracker reads every tracker row with its alias keys, archived rows
// included: an archived applied row is still evidence of an application.
func loadTracker(ctx context.Context, q queryer) ([]trackerRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, company, role, identity_key, status, COALESCE(report_number, 0) FROM tracker_entries ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []trackerRow
	byID := map[int64]int{}
	for rows.Next() {
		var t trackerRow
		if err := rows.Scan(&t.id, &t.company, &t.role, &t.key, &t.status, &t.report); err != nil {
			return nil, err
		}
		byID[t.id] = len(out)
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	arows, err := q.QueryContext(ctx, `SELECT entry_id, identity_key FROM tracker_aliases`)
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
		if i, ok := byID[id]; ok {
			out[i].aliases = append(out[i].aliases, key)
		}
	}
	return out, arows.Err()
}

// posting names what a claim is for: the identity key and URL, and the company
// and role the request carried, completed from the tracker row of the same
// posting when the request left them out.
type posting struct {
	key, url, company, role string
}

func (p posting) identity() identity.Posting {
	return identity.Posting{Key: p.key, Company: p.company, Role: p.role}
}

// evidence turns the rows into the gates' inputs. A ledger row matches by
// identity key, by the posting's own address, by the report number of the
// tracker row bound to the posting, or by the same-candidacy-strict predicate;
// a row with no company or role of its own borrows its report's.
func evidence(p posting, led []record, trk []trackerRow) (posting, []gates.LedgerRow, []gates.LedgerRow, []gates.TrackerRow) {
	var rows, claimRows []gates.LedgerRow
	var tracker []gates.TrackerRow
	reports := map[int64]bool{}
	byReport := map[int64]trackerRow{}
	bound := map[int64]bool{}
	for _, t := range trk {
		if t.report != 0 {
			byReport[t.report] = t
		}
		if t.hasKey(p.key) {
			bound[t.id] = true
			if t.report != 0 {
				reports[t.report] = true
			}
			if p.company == "" && p.role == "" {
				p.company, p.role = t.company, t.role
			}
		}
	}
	confirmed := map[int64]bool{}
	for _, r := range led {
		if r.state == gates.StateSubmitted && r.confirmed && r.report != 0 {
			confirmed[r.report] = true
		}
	}
	for _, r := range led {
		company, role := r.company, r.role
		if t, ok := byReport[r.report]; ok && r.report != 0 && (company == "" || role == "") {
			company, role = t.company, t.role
		}
		lr := gates.LedgerRow{RunID: r.owner(), State: r.state, Confirmed: r.confirmed, At: r.at, PID: r.pid}
		lr.Matched = r.identityKey == p.key || r.url == p.url || (r.report != 0 && reports[r.report]) ||
			identity.SameCandidacyStrict(p.identity(), identity.Posting{Key: r.identityKey, Company: company, Role: role})
		rows = append(rows, lr)
		if r.identityKey == p.key {
			claimRows = append(claimRows, lr)
		}
	}
	for _, t := range trk {
		var rel gates.TrackerRelation
		switch {
		case t.report != 0 && confirmed[t.report]:
			rel = gates.RelConfirmedReport
		case bound[t.id]:
			rel = gates.RelRunReport
		case identity.SameCandidacyStrict(p.identity(), identity.Posting{Key: t.key, Company: t.company, Role: t.role}):
			rel = gates.RelSameCandidacy
		default:
			continue
		}
		tracker = append(tracker, gates.TrackerRow{Status: t.status, Relation: rel, Label: fmt.Sprintf("#%d %s / %s", t.id, t.company, t.role)})
	}
	return p, rows, claimRows, tracker
}
