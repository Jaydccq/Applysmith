package ledger

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"applysmith/internal/gates"
	"applysmith/internal/identity"
)

// Sources of a claim row: the extension's step walker, and the apply chain.
const (
	SourceExtensionClaim = "extension-claim"
	SourceApplyChain     = "apply-chain"
)

// ErrUnavailable means the duplicate evidence or the claim itself could not be
// read or written, which is never a reason to press. ErrUnknownClaim means the
// claim id names no live claim.
var (
	ErrUnavailable  = errors.New("ledger: press claim unavailable")
	ErrUnknownClaim = errors.New("ledger: unknown press claim")
)

// BlockedError refuses a claim because the posting has duplicate evidence or
// another claim holds it. Decision names the evidence.
type BlockedError struct{ Decision gates.Decision }

func (e *BlockedError) Error() string { return e.Decision.Reason }

// ClaimRequest is a posting about to be pressed. URL has no query or
// fragment.
type ClaimRequest struct {
	URL, Host, Company, Role string
	// Source names who claims; empty reads as the extension's step walker.
	Source string
	// RunID is the apply run that holds the claim, empty for the extension.
	RunID string
	// RunStartedAt is when the claiming run began; another run's row written
	// since then loses the claim. Zero reads as now.
	RunStartedAt time.Time
	// Resubmit lifts unconfirmed and may-have-submitted evidence (never a
	// confirmed row and never a tracker row at applied or later).
	Resubmit     bool
	ReportNumber *int64
	// Correction asks for a correction of the posting's submission; nil for
	// every other run.
	Correction *CorrectionRequest
}

// Claim is a live permission to press once.
type Claim struct {
	ID        string
	ExpiresAt time.Time
	// Decisions are the allowing decisions that granted the claim.
	Decisions []gates.Decision
	// Supersedes is the ledger row a correction's claim row supersedes, 0
	// for an ordinary claim.
	Supersedes int64
}

// claimKey is the posting's identity key; an address that cannot be keyed
// compares by its exact string.
func claimKey(rawURL string) string {
	if k, err := identity.URLKey(rawURL); err == nil {
		return k
	}
	return rawURL
}

func newClaimID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "claim_" + hex.EncodeToString(b[:]), nil
}

// UnavailableError is a claim or guard that could not be decided; it matches
// ErrUnavailable and carries the decision for the audit.
type UnavailableError struct{ Decision gates.Decision }

func (e *UnavailableError) Error() string        { return ErrUnavailable.Error() + ": " + e.Decision.Reason }
func (e *UnavailableError) Is(target error) bool { return target == ErrUnavailable }

func unavailable(d gates.Decision) error { return &UnavailableError{Decision: d} }

// DecisionOf returns the denying decision inside a claim or guard error, or
// false for any other error.
func DecisionOf(err error) (gates.Decision, bool) {
	var blocked *BlockedError
	var un *UnavailableError
	switch {
	case errors.As(err, &blocked):
		return blocked.Decision, true
	case errors.As(err, &un):
		return un.Decision, true
	}
	return gates.Decision{}, false
}

// Claim runs the duplicate guard over the ledger and tracker evidence and, when
// it allows, takes the per-posting press claim: a submitting row under a new
// claim id, with this process id and the claim time. Reading the evidence and
// writing the row happen in one write transaction, so two claims for one
// posting cannot both pass. The returned error is a *BlockedError for
// evidence or a live claim, and an *UnavailableError (matching ErrUnavailable)
// for anything that could not be read or written; DecisionOf names the
// decision either way. On success Claim.Decisions holds the allowing
// decisions, for the caller's audit.
func (l *Ledger) Claim(ctx context.Context, req ClaimRequest) (Claim, error) {
	id, err := newClaimID()
	if err != nil {
		return Claim{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	p := posting{key: claimKey(req.URL), url: req.URL, company: strings.TrimSpace(req.Company), role: strings.TrimSpace(req.Role)}
	var out Claim
	var refused error
	err = l.s.Write(ctx, func(tx *sql.Tx) error {
		now := l.now()
		nowMs := now.UnixMilli()
		led, ledErr := loadRecords(ctx, tx, "")
		trk, trkErr := loadTracker(ctx, tx)
		eff, rows, claimRows, tracker := evidence(p, led, trk)
		startedMs := nowMs
		if !req.RunStartedAt.IsZero() {
			startedMs = req.RunStartedAt.UnixMilli()
		}
		j, jerr := judge(ctx, tx, req, eff.key, gates.DuplicateInput{Submit: true, Resubmit: req.Resubmit, Now: nowMs, LedgerReadable: ledErr == nil, TrackerReadable: trkErr == nil,
			Ledger: rows, Tracker: tracker})
		if jerr != nil {
			refused = jerr
			return nil
		}
		owner := req.RunID
		if owner == "" {
			owner = id
		}
		d := gates.PressClaim(gates.ClaimInput{RunID: owner, RunStartedAt: startedMs, Now: nowMs, LedgerReadable: ledErr == nil, ClaimWritable: true, Rows: claimRows})
		if !d.Allowed() {
			refused = refusal(d)
			return nil
		}
		stamp := now.UTC().Format(stampLayout)
		source := req.Source
		if source == "" {
			source = SourceExtensionClaim
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO submissions
			(identity_key, url, host, company, role, report_number, state, confirmed, source, observed_at, created_at, updated_at, claim_id, pid, claimed_at, run_id, supersedes, correction)
			VALUES (?, ?, ?, ?, ?, ?, 'submitting', 0, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?)`,
			eff.key, req.URL, req.Host, eff.company, eff.role, req.ReportNumber, source, stamp, stamp, stamp, id, l.pid, stamp, req.RunID, supersedesArg(j.Supersedes), j.Supersedes != 0)
		if err != nil {
			refused = unavailable(gates.PressClaim(gates.ClaimInput{LedgerReadable: true, ClaimWritable: false}))
			return err
		}
		out = Claim{ID: id, ExpiresAt: now.Add(time.Duration(gates.LiveWindow) * time.Millisecond), Decisions: append(j.Decisions, d), Supersedes: j.Supersedes}
		return nil
	})
	switch {
	case refused != nil:
		return Claim{}, refused
	case err != nil:
		return Claim{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return out, nil
}

// refusal maps a denying decision: a posting-class denial is a duplicate the
// person can read, anything else is the guard being unable to decide.
func refusal(d gates.Decision) error {
	if d.Class == gates.ClassPosting {
		return &BlockedError{Decision: d}
	}
	return unavailable(d)
}

// Settle turns a claim's submitting row into the submission the person made,
// with the confirmed flag as sent, and returns its ledger id. A repeat for the
// same claim changes nothing except to raise confirmed. A claim that was never
// issued, has outlived the live window unsettled, or was swept or failed is
// ErrUnknownClaim and writes nothing.
func (l *Ledger) Settle(ctx context.Context, claimID string, o Observation) (int64, error) {
	var id int64
	unknown := false
	err := l.s.Write(ctx, func(tx *sql.Tx) error {
		var state, claimed string
		var confirmed bool
		err := tx.QueryRowContext(ctx, `SELECT id, state, confirmed, COALESCE(claimed_at, created_at) FROM submissions WHERE claim_id = ?`, claimID).Scan(&id, &state, &confirmed, &claimed)
		if errors.Is(err, sql.ErrNoRows) {
			unknown = true
			return nil
		}
		if err != nil {
			return err
		}
		now := l.now()
		switch gates.LedgerState(state) {
		case gates.StateSubmitting:
			at, err := parseStamp(claimed)
			if err != nil {
				return err
			}
			if now.UnixMilli()-at > gates.LiveWindow {
				unknown = true
				return nil
			}
			_, err = tx.ExecContext(ctx, `UPDATE submissions SET state = ?, confirmed = ?, observed_at = ?, title = ?, score = ?, report_number = ?,
				company = CASE WHEN company = '' THEN ? ELSE company END, role = CASE WHEN role = '' THEN ? ELSE role END,
				entry_id = CASE WHEN ? = '' OR EXISTS (SELECT 1 FROM submissions WHERE entry_id = ?) THEN entry_id ELSE ? END,
				updated_at = ? WHERE id = ?`,
				string(gates.SettleClaim(gates.SettleInput{Landed: true})), o.Confirmed, o.ObservedAt.UTC().Format(stampLayout), strings.TrimSpace(o.Title), o.Score, o.ReportNumber,
				strings.TrimSpace(o.Company), strings.TrimSpace(o.Role), o.EntryID, o.EntryID, o.EntryID, now.UTC().Format(stampLayout), id)
			return err
		case gates.StateSubmitted:
			if o.Confirmed && !confirmed {
				_, err = tx.ExecContext(ctx, `UPDATE submissions SET confirmed = 1, updated_at = ? WHERE id = ?`, now.UTC().Format(stampLayout), id)
				return err
			}
			return nil
		}
		unknown = true
		return nil
	})
	switch {
	case unknown:
		return 0, ErrUnknownClaim
	case err != nil:
		return 0, err
	}
	return id, nil
}

// SweepStartup is the crash sweep. It changes every submitting row whose
// claiming process is gone to may-have-submitted, so the guard blocks the
// posting until the person resubmits, and leaves a row whose process still
// runs to its owner. Each row is read again just before it is written. It
// returns how many rows it changed.
func (l *Ledger) SweepStartup(ctx context.Context, running func(pid int) bool) (int, error) {
	pending, err := loadRecords(ctx, l.s.Read(), "WHERE state = 'submitting'")
	if err != nil {
		return 0, err
	}
	swept := 0
	for _, r := range pending {
		err := l.s.Write(ctx, func(tx *sql.Tx) error {
			cur, err := loadRecords(ctx, tx, "WHERE id = ?", r.id)
			if err != nil || len(cur) != 1 {
				return err
			}
			lr := gates.LedgerRow{State: cur[0].state, PID: cur[0].pid}
			next, changed := gates.SweepRow(lr, l.pid, lr.PID > 0 && running(lr.PID))
			if !changed {
				return nil
			}
			swept++
			_, err = tx.ExecContext(ctx, `UPDATE submissions SET state = ?, updated_at = ? WHERE id = ? AND state = 'submitting'`, string(next), l.stamp(), r.id)
			return err
		})
		if err != nil {
			return swept, err
		}
	}
	return swept, nil
}
