package wire

// SubmissionRecord is an application the person submitted, as the browser
// extension observed it. It carries the posting and the time, never a form
// value. ClaimID is set only when the extension's own step walker pressed the
// final control after the server granted a press claim.
type SubmissionRecord struct {
	EntryID      string   `json:"entryId"`
	URL          string   `json:"url"`
	Host         string   `json:"host"`
	Company      string   `json:"company,omitempty"`
	Role         string   `json:"role,omitempty"`
	Title        string   `json:"title"`
	Score        *float64 `json:"score,omitempty"`
	ReportNumber *SafeInt `json:"reportNumber,omitempty"`
	ObservedAt   Time     `json:"observedAt"`
	Confirmed    bool     `json:"confirmed"`
	ClaimID      string   `json:"claimId,omitempty"`
}

// SubmissionSettled is the result when the record settled a press claim.
const SubmissionSettled = "settled"

// What a record did to the tracker.
const (
	TrackerCreated   = "created"
	TrackerApplied   = "applied"
	TrackerUnchanged = "unchanged"
)

// SubmissionRecorded answers a record: the ledger row, what happened to it and
// what happened to the tracker.
type SubmissionRecorded struct {
	LedgerID SafeInt `json:"ledgerId"`
	Result   string  `json:"result"`
	Tracker  string  `json:"tracker"`
}

// PressClaimRequest asks to press the final submit control of a posting. The
// page address loses its query and fragment before anything is kept.
type PressClaimRequest struct {
	URL     string `json:"url"`
	Host    string `json:"host"`
	Company string `json:"company,omitempty"`
	Role    string `json:"role,omitempty"`
}

// PressClaimGranted is the only answer that lets the extension press. Anything
// else, an error included, means the press is not made.
type PressClaimGranted struct {
	ClaimID   string `json:"claimId"`
	ExpiresAt Time   `json:"expiresAt"`
}

// Reasons of a refused or unavailable press claim and of a held or unreadable
// sign-in.
const (
	ReasonDuplicateSubmit      = "duplicate_submit_blocked"
	ReasonClaimUnavailable     = "claim_unavailable"
	ReasonUnknownClaim         = "unknown_claim"
	ReasonSigninHeld           = "signin_held"
	ReasonSigninHoldUnreadable = "signin_hold_unreadable"
)
