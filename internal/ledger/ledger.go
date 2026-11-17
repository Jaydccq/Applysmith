// Package ledger is the submissions ledger: the durable record of applications
// that were submitted. This file holds what a user-observed submission needs;
// the duplicate guard and the press claim of the apply chain use the same table.
package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"applysmith/internal/identity"
	"applysmith/internal/store"
)

// SourceUserObserved marks a row the browser extension recorded from the
// person's own submit.
const SourceUserObserved = "user-observed"

// ErrInvalid is returned for an observation that cannot be recorded.
var ErrInvalid = errors.New("ledger: invalid submission")

// How a record reached the ledger.
type Result string

const (
	Created  Result = "created"
	Upgraded Result = "upgraded"
	// Attached means the posting was already recorded and nothing was added.
	Attached Result = "duplicate_attached"
)

// Observation is an application the user submitted, as seen from their
// browser.
type Observation struct {
	EntryID      string
	URL          string
	Host         string
	Company      string
	Role         string
	Title        string
	Score        *float64
	ReportNumber *int64
	ObservedAt   time.Time
	Confirmed    bool
}

// Recorded is the ledger's answer to an observation.
type Recorded struct {
	ID          int64
	Result      Result
	IdentityKey string
}

// Ledger reads and writes the submissions table.
type Ledger struct {
	s   *store.Store
	now func() time.Time
	// pid is the process that takes press claims.
	pid int
}

// New returns a ledger over s. A nil now uses the wall clock.
func New(s *store.Store, now func() time.Time) *Ledger {
	if now == nil {
		now = time.Now
	}
	return &Ledger{s: s, now: now, pid: os.Getpid()}
}

const stampLayout = "2006-01-02T15:04:05.000Z"

func (l *Ledger) stamp() string { return l.now().UTC().Format(stampLayout) }

// RecordObserved writes one row for a submission the user made, under the
// ledger rules: a known entry id with confirmed true upgrades its row and never
// downgrades it; a posting that already has a confirmed row gets no second
// row; otherwise a row is created in state submitted with the confirmed flag as
// sent. The duplicate guard is deliberately not consulted, because the
// submission already happened.
func (l *Ledger) RecordObserved(ctx context.Context, o Observation) (Recorded, error) {
	key, err := identity.URLKey(o.URL)
	if err != nil {
		return Recorded{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if strings.TrimSpace(o.EntryID) == "" {
		return Recorded{}, fmt.Errorf("%w: an entry id is required", ErrInvalid)
	}
	var out Recorded
	err = l.s.Write(ctx, func(tx *sql.Tx) error {
		out = Recorded{IdentityKey: key}
		var id int64
		var confirmed bool
		err := tx.QueryRowContext(ctx, `SELECT id, confirmed FROM submissions WHERE entry_id = ?`, o.EntryID).Scan(&id, &confirmed)
		switch {
		case err == nil:
			out.ID, out.Result = id, Attached
			if o.Confirmed && !confirmed {
				out.Result = Upgraded
				_, err = tx.ExecContext(ctx, `UPDATE submissions SET confirmed = 1, updated_at = ? WHERE id = ?`, l.stamp(), id)
			}
			return err
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		err = tx.QueryRowContext(ctx, `SELECT id FROM submissions
			WHERE identity_key = ? AND state = 'submitted' AND confirmed = 1 ORDER BY id LIMIT 1`, key).Scan(&id)
		switch {
		case err == nil:
			out.ID, out.Result = id, Attached
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		now := l.stamp()
		res, err := tx.ExecContext(ctx, `INSERT INTO submissions
			(entry_id, identity_key, url, host, company, role, title, score, report_number, state, confirmed, source, observed_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'submitted', ?, ?, ?, ?, ?)`,
			o.EntryID, key, o.URL, o.Host, strings.TrimSpace(o.Company), strings.TrimSpace(o.Role), strings.TrimSpace(o.Title),
			o.Score, o.ReportNumber, o.Confirmed, SourceUserObserved, o.ObservedAt.UTC().Format(stampLayout), now, now)
		if err != nil {
			return err
		}
		out.ID, err = res.LastInsertId()
		out.Result = Created
		return err
	})
	return out, err
}

// Prior is the submission an applied banner quotes for a posting.
type Prior struct {
	Confirmed bool
	// Source names who submitted: SourceApplyChain, SourceUserObserved or
	// SourceExtensionClaim.
	Source     string
	ObservedAt time.Time
}

// PriorFor returns the submitted row of a posting the banner should quote: a
// confirmed row over an unconfirmed one, then the most recent. A row still
// submitting, failed or may-have-submitted is not a submission. ok is false
// when the posting has none.
func (l *Ledger) PriorFor(ctx context.Context, postingURL string) (p Prior, ok bool, err error) {
	key, err := identity.URLKey(postingURL)
	if err != nil {
		return Prior{}, false, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	var at string
	err = l.s.Read().QueryRowContext(ctx, `SELECT confirmed, source, observed_at FROM submissions
		WHERE identity_key = ? AND state = 'submitted' ORDER BY confirmed DESC, observed_at DESC, id DESC LIMIT 1`, key).Scan(&p.Confirmed, &p.Source, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return Prior{}, false, nil
	}
	if err != nil {
		return Prior{}, false, err
	}
	p.ObservedAt, err = time.Parse(stampLayout, at)
	return p, err == nil, err
}
