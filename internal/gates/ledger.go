// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"fmt"
	"strings"
)

// LiveWindow is how long a submitting row stays live from its claim.
const LiveWindow = 15 * Minute

// LedgerState is the state of a submission ledger row.
type LedgerState string

const (
	StateSubmitting       LedgerState = "submitting"
	StateSubmitted        LedgerState = "submitted"
	StateFailed           LedgerState = "failed"
	StateMayHaveSubmitted LedgerState = "may-have-submitted"
)

// Gate names and reason codes of the duplicate guard and the press claim.
const (
	GateDuplicateGuard Gate = "duplicate_guard"
	GatePressClaim     Gate = "press_claim"

	CodeNotSubmitting         = "not_submitting"
	CodeEvidenceUnreadable    = "evidence_unreadable"
	CodeLiveSubmitting        = "live_submitting"
	CodeAlreadySubmitted      = "already_submitted"
	CodeTrackerApplied        = "tracker_applied"
	CodeUnconfirmedSubmission = "unconfirmed_submission"
	CodeMayHaveSubmitted      = "may_have_submitted"
	CodeResubmitLifted        = "resubmit_lifted"
	CodeCorrectionLifted      = "correction_lifted"

	CodeClaimLedgerUnreadable = "claim_ledger_unreadable"
	CodeClaimUnwritable       = "claim_unwritable"
	CodeClaimLost             = "claim_lost"
)

// LedgerRow is a submission ledger row. At is the claim time for a submitting
// row and the write time otherwise.
type LedgerRow struct {
	RunID     string
	State     LedgerState
	Confirmed bool
	At        int64
	// Matched is the caller's verdict that the row belongs to this posting:
	// by identity key, by report number, or by the same-candidacy-strict
	// company and role predicate. The gate does not recompute identity.
	Matched bool
	// PID is the claiming process, for the crash sweep.
	PID int
}

// EffectiveState reads a submitting row older than the live window as
// may-have-submitted.
func EffectiveState(r LedgerRow, now int64) LedgerState {
	if r.State == StateSubmitting && now-r.At > LiveWindow {
		return StateMayHaveSubmitted
	}
	return r.State
}

// newestPerRun keeps each run's latest row: a later row for the same run id
// ends a may-have-submitted state.
func newestPerRun(rows []LedgerRow) []LedgerRow {
	idx := map[string]int{}
	var out []LedgerRow
	for _, r := range rows {
		if i, ok := idx[r.RunID]; ok && r.RunID != "" {
			if r.At >= out[i].At {
				out[i] = r
			}
			continue
		}
		idx[r.RunID] = len(out)
		out = append(out, r)
	}
	return out
}

// TrackerRelation says how a tracker row relates to the run.
type TrackerRelation string

const (
	// RelRunReport is the tracker row bound to the run's evaluated report.
	RelRunReport TrackerRelation = "run_report"
	// RelSameCandidacy is a different row with the same company and role
	// under the same-candidacy-strict predicate.
	RelSameCandidacy TrackerRelation = "same_candidacy"
	// RelConfirmedReport is a row whose report number appears among the
	// confirmed submission rows.
	RelConfirmedReport TrackerRelation = "confirmed_report"
)

// TrackerRow is the evidence a tracker row contributes.
type TrackerRow struct {
	Status   string
	Relation TrackerRelation
	Label    string
}

// AppliedOrLater reports whether a tracker status reads as applied or any
// later status of the canonical list. Evaluated, Discarded and SKIP are not.
func AppliedOrLater(status string) bool {
	switch strings.Trim(foldText(status), "* _") {
	case "applied", "responded", "interview", "offer", "rejected":
		return true
	}
	return false
}

// DuplicateInput holds the evidence for a submit-stage duplicate check.
type DuplicateInput struct {
	// Submit is false for a dry run, which duplicate evidence never blocks.
	Submit bool
	// Resubmit is the explicit resubmit flag; it lifts only unconfirmed and
	// may-have-submitted evidence.
	Resubmit bool
	// Correction is set only after CorrectionDoor allowed the run: it lifts
	// every evidence of an earlier submission except a live submitting row.
	Correction                      bool
	Now                             int64
	LedgerReadable, TrackerReadable bool
	Ledger                          []LedgerRow
	Tracker                         []TrackerRow
}

// DuplicateGuard blocks a submitting run when the posting already has
// duplicate evidence. Unreadable evidence blocks too: missing evidence is not
// read as no evidence.
func DuplicateGuard(in DuplicateInput) Decision {
	const g = GateDuplicateGuard
	if !in.Submit {
		return decide(g, Allow, CodeNotSubmitting, "dry run; duplicate evidence does not block a run that cannot submit")
	}
	if !in.LedgerReadable || !in.TrackerReadable {
		src := "submission ledger"
		if in.LedgerReadable {
			src = "tracker"
		}
		return decide(g, Deny, CodeEvidenceUnreadable, "duplicate evidence cannot be read (%s); no Submit is pressed", src).withClass(ClassEnvironment)
	}
	var liftable *LedgerRow
	var liftableState LedgerState
	rows := newestPerRun(in.Ledger)
	for i := range rows {
		r := rows[i]
		if !r.Matched {
			continue
		}
		switch s := EffectiveState(r, in.Now); {
		case s == StateSubmitting:
			return decide(g, Deny, CodeLiveSubmitting, "run %s is submitting this posting right now; wait for its verdict", r.RunID).withClass(ClassPosting)
		case s == StateSubmitted && r.Confirmed && !in.Correction:
			return decide(g, Deny, CodeAlreadySubmitted, "already submitted: confirmed submission by run %s at %s", r.RunID, stamp(r.At)).withClass(ClassPosting)
		case s == StateSubmitted || s == StateMayHaveSubmitted:
			if liftable == nil {
				liftable, liftableState = &rows[i], s
			}
		}
	}
	if in.Correction {
		return decide(g, Allow, CodeCorrectionLifted, "the correction door lifts the earlier submission of this posting")
	}
	for _, t := range in.Tracker {
		if t.Relation == RelConfirmedReport || AppliedOrLater(t.Status) {
			return decide(g, Deny, CodeTrackerApplied, "tracker row %q reads %q (%s); no flag lifts this", t.Label, t.Status, t.Relation).withClass(ClassPosting)
		}
	}
	if liftable == nil {
		return decide(g, Allow, CodeOK, "no duplicate evidence")
	}
	if in.Resubmit {
		return decide(g, Allow, CodeResubmitLifted, "resubmit lifts the unconfirmed row of run %s at %s", liftable.RunID, stamp(liftable.At))
	}
	code := CodeUnconfirmedSubmission
	if liftableState == StateMayHaveSubmitted {
		code = CodeMayHaveSubmitted
	}
	return decide(g, Deny, code, "%s row of run %s at %s; not sent again unless you ask to resubmit it", liftableState, liftable.RunID, stamp(liftable.At)).withClass(ClassPosting)
}

// ClaimInput holds the facts for the per-posting press claim.
type ClaimInput struct {
	RunID        string
	RunStartedAt int64
	Now          int64
	// LedgerReadable and ClaimWritable are false when the read or the write
	// inside the claim transaction failed.
	LedgerReadable, ClaimWritable bool
	// Rows are the ledger rows of the posting identity key read inside the
	// claim transaction.
	Rows []LedgerRow
}

// PressClaim decides the claim taken immediately before the press. Another
// run's row written since this run began, or another run's live submitting
// row, loses the claim.
func PressClaim(in ClaimInput) Decision {
	const g = GatePressClaim
	if !in.LedgerReadable {
		return decide(g, Deny, CodeClaimLedgerUnreadable, "the submission ledger cannot be read; no Submit is pressed").withClass(ClassEnvironment)
	}
	for _, r := range in.Rows {
		if r.RunID == in.RunID {
			continue
		}
		if r.At >= in.RunStartedAt || EffectiveState(r, in.Now) == StateSubmitting {
			return decide(g, Deny, CodeClaimLost, "duplicate submit blocked: another run (%s) wrote a row for this posting; nothing is pressed", r.RunID).withClass(ClassPosting)
		}
	}
	if !in.ClaimWritable {
		return decide(g, Deny, CodeClaimUnwritable, "the press claim cannot be written; no Submit is pressed").withClass(ClassEnvironment)
	}
	return decide(g, Allow, CodeOK, "claim taken for run %s", in.RunID)
}

// SettleInput holds what is known when the run's finalization settles its row.
type SettleInput struct {
	// Landed is a verdict proving the application was received.
	Landed bool
	// PressDispatched is true once a press input was sent.
	PressDispatched bool
	// RefusalProven is true when the page, the application service or a
	// challenge provably refused the press.
	RefusalProven bool
}

// SettleClaim returns the state a claimed row ends in.
func SettleClaim(in SettleInput) LedgerState {
	switch {
	case in.Landed:
		return StateSubmitted
	case !in.PressDispatched || in.RefusalProven:
		return StateFailed
	}
	return StateMayHaveSubmitted
}

// SweepRow is the start-up sweep for one row: a submitting row whose claiming
// process is gone becomes may-have-submitted. A missing pid, or this
// process's own pid, counts as dead; a row whose process still runs is left
// to its owner.
func SweepRow(r LedgerRow, ownPID int, pidRunning bool) (LedgerState, bool) {
	if r.State != StateSubmitting {
		return r.State, false
	}
	if r.PID == 0 || r.PID == ownPID || !pidRunning {
		return StateMayHaveSubmitted, true
	}
	return StateSubmitting, false
}

// stamp renders Unix milliseconds as an RFC 3339 UTC time without the time
// package, using the days-to-civil conversion.
func stamp(ms int64) string {
	days, rem := ms/Day, ms%Day
	if rem < 0 {
		days, rem = days-1, rem+Day
	}
	z := days + 719468
	era := z / 146097
	if z < 0 {
		era = (z - 146096) / 146097
	}
	doe := z - era*146097
	yoe := (doe - doe/1460 + doe/36524 - doe/146096) / 365
	doy := doe - (365*yoe + yoe/4 - yoe/100)
	mp := (5*doy + 2) / 153
	d := doy - (153*mp+2)/5 + 1
	m := mp + 3
	if m > 12 {
		m -= 12
	}
	y := yoe + era*400
	if m <= 2 {
		y++
	}
	s := rem / Second
	return fmt.Sprintf("%04d-%02d-%02dT%02d:%02d:%02dZ", y, m, d, s/3600, s%3600/60, s%60)
}
