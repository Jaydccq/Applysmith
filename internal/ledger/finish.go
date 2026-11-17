package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"applysmith/internal/gates"
)

// Guard runs the duplicate guard alone, read-only, over the evidence of the
// posting, behind the correction door for a correction request: the apply
// chain asks it before any browser work so a run that would be refused at the
// press stops early. It takes no claim and writes nothing; the claim decides
// again. The error is a *BlockedError, an *UnavailableError or nil, and the
// judgement holds every decision taken either way.
func (l *Ledger) Guard(ctx context.Context, req ClaimRequest) (Judgement, error) {
	p := posting{key: claimKey(req.URL), url: req.URL, company: strings.TrimSpace(req.Company), role: strings.TrimSpace(req.Role)}
	db := l.s.Read()
	led, ledErr := loadRecords(ctx, db, "")
	trk, trkErr := loadTracker(ctx, db)
	_, rows, _, tracker := evidence(p, led, trk)
	return judge(ctx, db, req, p.key, gates.DuplicateInput{Submit: true, Resubmit: req.Resubmit, Now: l.now().UnixMilli(),
		LedgerReadable: ledErr == nil, TrackerReadable: trkErr == nil, Ledger: rows, Tracker: tracker})
}

// Verdict is how a claimed press ended.
type Verdict struct {
	// Landed is a proof that the application was received.
	Landed bool
	// PressDispatched is true once the press input was sent.
	PressDispatched bool
	// RefusalProven is true when the page, the application service or a
	// challenge provably refused the press.
	RefusalProven bool
	Confirmed     bool
	ConfirmSource string
	ReceiptID     string
	Company, Role string
	ReportNumber  *int64
	// Detail is one line for the row; it carries no secret.
	Detail string
}

// Finish settles the claim of a run that pressed (or did not): the row becomes
// submitted, failed or may-have-submitted by gates.SettleClaim. A repeat may
// only raise confirmed; a row already submitted never goes back. The claim
// need not be inside the live window, because the owning process is the one
// settling it. ErrUnknownClaim names a claim with no row.
func (l *Ledger) Finish(ctx context.Context, claimID string, v Verdict) (gates.LedgerState, error) {
	state := gates.SettleClaim(gates.SettleInput{Landed: v.Landed, PressDispatched: v.PressDispatched, RefusalProven: v.RefusalProven})
	var final gates.LedgerState
	unknown := false
	err := l.s.Write(ctx, func(tx *sql.Tx) error {
		var cur string
		var confirmed bool
		err := tx.QueryRowContext(ctx, `SELECT state, confirmed FROM submissions WHERE claim_id = ?`, claimID).Scan(&cur, &confirmed)
		if errors.Is(err, sql.ErrNoRows) {
			unknown = true
			return nil
		}
		if err != nil {
			return err
		}
		final = gates.LedgerState(cur)
		now := l.stamp()
		if final == gates.StateSubmitted {
			if v.Confirmed && !confirmed {
				_, err = tx.ExecContext(ctx, `UPDATE submissions SET confirmed = 1, confirm_source = NULLIF(?, ''), receipt_message_id = COALESCE(NULLIF(?, ''), receipt_message_id), updated_at = ? WHERE claim_id = ?`,
					v.ConfirmSource, v.ReceiptID, now, claimID)
			}
			return err
		}
		if final == gates.StateFailed {
			return nil // a failed claim is terminal
		}
		final = state
		_, err = tx.ExecContext(ctx, `UPDATE submissions SET state = ?, confirmed = ?, confirm_source = NULLIF(?, ''), receipt_message_id = NULLIF(?, ''),
			company = CASE WHEN company = '' THEN ? ELSE company END, role = CASE WHEN role = '' THEN ? ELSE role END,
			report_number = COALESCE(report_number, ?), detail = NULLIF(?, ''), observed_at = ?, updated_at = ? WHERE claim_id = ?`,
			string(state), v.Confirmed && state == gates.StateSubmitted, v.ConfirmSource, v.ReceiptID,
			strings.TrimSpace(v.Company), strings.TrimSpace(v.Role), v.ReportNumber, v.Detail, now, now, claimID)
		return err
	})
	switch {
	case unknown:
		return "", ErrUnknownClaim
	case err != nil:
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return final, nil
}

// Confirm sources the apply chain does not write itself: the user's statement
// that a submission went through, and a receipt matched to it later.
const (
	ConfirmUser    = "user"
	ConfirmReceipt = "receipt"
)

// ErrNotWaiting names a submission that no longer waits to be settled.
var ErrNotWaiting = errors.New("ledger: the submission no longer waits")

// Waiting is a submission of the apply chain that still waits: unconfirmed, or
// confirmed by the page or the application service while no receipt arrived.
type Waiting struct {
	ID        int64
	Key       string
	Confirmed bool
	// At is when the press was claimed.
	At time.Time
}

// Waiting returns the newest waiting submission of the apply chain per
// identity key. An unconfirmed one waits only while no confirmed row of the
// posting stands beside it.
func (l *Ledger) Waiting(ctx context.Context) (map[string]Waiting, error) {
	rows, err := l.s.Read().QueryContext(ctx, `SELECT u.id, u.identity_key, u.confirmed, COALESCE(u.claimed_at, u.created_at) FROM submissions u
		WHERE u.source = ? AND u.state IN ('submitted', 'may-have-submitted') AND (
			(u.confirmed = 0 AND NOT EXISTS (SELECT 1 FROM submissions c WHERE c.identity_key = u.identity_key AND c.state = 'submitted' AND c.confirmed = 1))
			OR (u.confirmed = 1 AND u.confirm_source IN ('ats_response', 'page') AND u.receipt_message_id IS NULL))
		ORDER BY u.id`, SourceApplyChain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Waiting{}
	for rows.Next() {
		var w Waiting
		var at string
		if err := rows.Scan(&w.ID, &w.Key, &w.Confirmed, &at); err != nil {
			return nil, err
		}
		w.At, _ = time.Parse(stampLayout, at)
		out[w.Key] = w
	}
	return out, rows.Err()
}

// WaitingFor returns the newest waiting submission of the apply chain under
// any of a posting's identity keys.
func (l *Ledger) WaitingFor(ctx context.Context, keys []string) (Waiting, bool, error) {
	all, err := l.Waiting(ctx)
	if err != nil {
		return Waiting{}, false, err
	}
	var best Waiting
	for _, k := range keys {
		if w, ok := all[k]; ok && w.ID > best.ID {
			best = w
		}
	}
	return best, best.ID > 0, nil
}

// SettleWaiting ends the wait of a submission: by the user's statement
// (ConfirmUser) or by a receipt matched to it (ConfirmReceipt with its message
// id). A may-have-submitted row becomes submitted and an unconfirmed one
// confirmed, by what settled it. A receipt keeps the page's or the service's
// confirmation as the source; the user's statement replaces it, since the run
// record keeps the page's evidence. ErrNotWaiting names a row that no longer
// waits.
func (l *Ledger) SettleWaiting(ctx context.Context, id int64, by, receiptID string) error {
	if by != ConfirmUser && (by != ConfirmReceipt || receiptID == "") {
		return fmt.Errorf("%w: a submission is settled by the user or by a receipt", ErrInvalid)
	}
	var n int64
	err := l.s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE submissions SET state = 'submitted',
			confirm_source = CASE WHEN ? = 'receipt' AND confirmed = 1 THEN confirm_source ELSE ? END, confirmed = 1,
			receipt_message_id = COALESCE(NULLIF(?, ''), receipt_message_id), updated_at = ?
			WHERE id = ? AND source = ? AND state IN ('submitted', 'may-have-submitted')`, by, by, receiptID, l.stamp(), id, SourceApplyChain)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	switch {
	case err != nil:
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	case n == 0:
		return ErrNotWaiting
	}
	return nil
}
