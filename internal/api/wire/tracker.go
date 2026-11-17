package wire

// Candidacy statuses; internal/tracker owns the table and a test compares.
const (
	StatusEvaluated = "evaluated"
	StatusSkipped   = "skipped"
	StatusApplied   = "applied"
	StatusResponded = "responded"
	StatusInterview = "interview"
	StatusOffer     = "offer"
	StatusRejected  = "rejected"
	StatusDiscarded = "discarded"
)

// Reasons of a conflict on the candidacy routes.
const ReasonDuplicateCandidacy = "duplicate_candidacy"

// Candidacy is one tracker row.
type Candidacy struct {
	ID           SafeInt  `json:"id"`
	Date         string   `json:"date"`
	Company      string   `json:"company"`
	Role         string   `json:"role"`
	Score        *float64 `json:"score,omitempty"`
	ScoreKind    string   `json:"scoreKind,omitempty"`
	Status       string   `json:"status"`
	AppliedBy    string   `json:"appliedBy,omitempty"`
	AppliedAt    string   `json:"appliedAt,omitempty"`
	ResumePDF    bool     `json:"resumePdf"`
	CoverLetter  bool     `json:"coverLetter"`
	ReportNumber *SafeInt `json:"reportNumber,omitempty"`
	Notes        string   `json:"notes"`
	PostingURL   string   `json:"postingUrl"`
	Location     string   `json:"location"`
	Source       string   `json:"source,omitempty"`
	IdentityKey  string   `json:"identityKey"`
	ArchivedAt   string   `json:"archivedAt,omitempty"`
	Version      SafeInt  `json:"version"`
	CreatedAt    Time     `json:"createdAt"`
	UpdatedAt    Time     `json:"updatedAt"`
	// Mail is the row's most pressing open linked email thread, set on the list
	// only and left out when the row has none.
	Mail *CandidacyMail `json:"mail,omitempty"`
}

// CandidacyMail is the mail state of a tracker row, read through the same links
// and lifecycle as the Gmail inbox. Lane, Owner and NextAction are the
// thread's; AgeDays is the whole days since its latest incoming message; Open
// counts the row's open threads.
type CandidacyMail struct {
	ThreadID   string  `json:"threadId"`
	Lane       string  `json:"lane"`
	Owner      string  `json:"owner"`
	NextAction string  `json:"nextAction"`
	AgeDays    SafeInt `json:"ageDays"`
	Open       SafeInt `json:"open"`
}

// CandidacyPage is one page of the candidacy list.
type CandidacyPage struct {
	Entries  []Candidacy `json:"entries"`
	Page     SafeInt     `json:"page"`
	PageSize SafeInt     `json:"pageSize"`
	Total    SafeInt     `json:"total"`
	// Archived counts the archived rows the filters also match.
	Archived SafeInt `json:"archived,omitempty"`
}

// CandidacyEvent is one entry of a row's append-only history. Detail is
// event-specific.
type CandidacyEvent struct {
	ID             SafeInt `json:"id"`
	At             Time    `json:"at"`
	Actor          string  `json:"actor"`
	Kind           string  `json:"kind"`
	PreviousStatus string  `json:"previousStatus,omitempty"`
	NewStatus      string  `json:"newStatus,omitempty"`
	Detail         any     `json:"detail,omitempty"`
}

// CandidacyDetail is a row with its history and merge aliases.
type CandidacyDetail struct {
	Entry   Candidacy        `json:"entry"`
	Events  []CandidacyEvent `json:"events"`
	Aliases []string         `json:"aliases"`
}

// StatusChangeRequest moves one row. The actor is the authenticated caller,
// never a field of the body. ExpectedVersion is the row version the caller
// last read, at least 1: an edit without it could overwrite a newer one.
type StatusChangeRequest struct {
	Status          string  `json:"status"`
	Note            string  `json:"note,omitempty"`
	On              string  `json:"on,omitempty"`
	ExpectedVersion SafeInt `json:"expectedVersion"`
}

// StatusChangeResult says what a status change did.
type StatusChangeResult struct {
	Entry      Candidacy `json:"entry"`
	Changed    bool      `json:"changed"`
	Correction bool      `json:"correction"`
	Inferred   bool      `json:"inferred"`
}

// NoteRequest appends a note to a row or, with Undo, takes back the caller's
// latest note still in force (the text is then ignored).
type NoteRequest struct {
	Text            string  `json:"text"`
	Undo            bool    `json:"undo,omitempty"`
	ExpectedVersion SafeInt `json:"expectedVersion"`
}

// FollowupSuggestion is one nudge due on an open row (tracker.Suggestion).
type FollowupSuggestion struct {
	ID      SafeInt `json:"id"`
	Company string  `json:"company"`
	Role    string  `json:"role"`
	Version SafeInt `json:"version"`
	Kind    string  `json:"kind"`
	Number  SafeInt `json:"number,omitempty"`
	Due     string  `json:"due,omitempty"`
}

// FollowupList is the suggestions due today.
type FollowupList struct {
	Suggestions []FollowupSuggestion `json:"suggestions"`
}

// FollowupRequest records a sent follow-up, snoozes the next SnoozeDays, or
// with Undo takes back the latest follow-up or snooze still in force.
// ExpectedVersion is the row version the caller last read, at least 1.
type FollowupRequest struct {
	SnoozeDays      SafeInt `json:"snoozeDays,omitempty"`
	Undo            bool    `json:"undo,omitempty"`
	ExpectedVersion SafeInt `json:"expectedVersion"`
}

// FollowupDraftRequest chooses the follow-up. An empty kind (recruiter_ping,
// status_check or thank_you) is read from the row's status; contact is the
// person's name and context what the user remembers of the last exchange.
type FollowupDraftRequest struct {
	Kind    string `json:"kind,omitempty"`
	Contact string `json:"contact,omitempty"`
	Context string `json:"context,omitempty"`
}

// OutreachDraftRequest names the person an outreach note goes to: contact is
// recruiter, hiring_manager, peer or interviewer; about is what the user
// knows of them, the only source about the person; date is an interview's.
type OutreachDraftRequest struct {
	Contact string `json:"contact"`
	Name    string `json:"name,omitempty"`
	About   string `json:"about,omitempty"`
	Date    string `json:"date,omitempty"`
}

// MessageVariant is one drafted message and the angle it takes.
type MessageVariant struct {
	Angle   string `json:"angle"`
	Message string `json:"message"`
}

// MessageDraft is a checked follow-up or outreach message for the user to copy
// and send; nothing is sent or recorded. Every message fits MaxChars.
// Confirmed lists the claims resting on the user's own material, ToConfirm
// what to check before sending and Dropped the messages the checks removed.
type MessageDraft struct {
	Kind         string           `json:"kind"`
	Primary      MessageVariant   `json:"primary"`
	Alternatives []MessageVariant `json:"alternatives"`
	MaxChars     int              `json:"maxChars"`
	Confirmed    []string         `json:"confirmed"`
	ToConfirm    []string         `json:"toConfirm"`
	Dropped      []string         `json:"dropped"`
	Notes        []string         `json:"notes"`
}

// CandidacyCreate records a posting the user applied to elsewhere.
type CandidacyCreate struct {
	Company   string `json:"company"`
	Role      string `json:"role"`
	URL       string `json:"url,omitempty"`
	Location  string `json:"location,omitempty"`
	Status    string `json:"status,omitempty"`
	AppliedAt string `json:"appliedAt,omitempty"`
	Source    string `json:"source,omitempty"`
}

// BulkStatusRequest changes up to 100 rows in one transaction.
type BulkStatusRequest struct {
	IDs    []SafeInt `json:"ids"`
	Status string    `json:"status"`
	Note   string    `json:"note,omitempty"`
}

// BulkStatusResult lists the results in request order.
type BulkStatusResult struct {
	Results []StatusChangeResult `json:"results"`
}

// TrackerSummary counts the rows per status: the non-archived ones, or all
// of them with ?archived=true.
type TrackerSummary struct {
	Counts map[string]SafeInt `json:"counts"`
	Total  SafeInt            `json:"total"`
}

// TrackerDuplicates are the groups `applysmith tracker dedup` lists.
type TrackerDuplicates struct {
	Groups []DuplicateGroup `json:"groups"`
}

// DuplicateGroup is one group with the row and status a merge keeps.
type DuplicateGroup struct {
	Survivor  SafeInt     `json:"survivor"`
	Status    string      `json:"status"`
	Rule      string      `json:"rule"`
	NeedsUser bool        `json:"needsUser"`
	Reason    string      `json:"reason,omitempty"`
	Rows      []Candidacy `json:"rows"`
}

// BulkArchiveRequest archives (or with archived false unarchives) up to 100
// rows in one transaction.
type BulkArchiveRequest struct {
	IDs      []SafeInt `json:"ids"`
	Archived bool      `json:"archived"`
}

// BulkArchiveResult names the rows that moved and the rows still in flight
// that were left; a row already where it should be is in neither.
type BulkArchiveResult struct {
	Changed []SafeInt `json:"changed"`
	Open    []SafeInt `json:"open"`
}

// DuplicateMergeRequest names every row of the group to merge, or carries the
// undo a merge answered to take that merge back.
type DuplicateMergeRequest struct {
	Members []SafeInt `json:"members,omitempty"`
	Undo    string    `json:"undo,omitempty"`
}

// DuplicateMergeResult is the kept row, and after a merge the token that undoes
// it within 15 minutes.
type DuplicateMergeResult struct {
	Kept Candidacy `json:"kept"`
	Undo string    `json:"undo,omitempty"`
}

// DuplicateDismissal says the rows of one listed group are not duplicates,
// or with undo takes that back.
type DuplicateDismissal struct {
	Members []SafeInt `json:"members"`
	Undo    bool      `json:"undo,omitempty"`
}

// CandidacyTransitions are the statuses the user can move a row to: ordinary
// transitions, and those reachable only as a correction.
type CandidacyTransitions struct {
	Status     string   `json:"status"`
	Ordinary   []string `json:"ordinary"`
	Correction []string `json:"correction"`
}

// StatusHistoryItem is one status a row held.
type StatusHistoryItem struct {
	Status string `json:"status"`
	At     Time   `json:"at"`
	Actor  string `json:"actor"`
}

// ApplicationLookup answers the lookup by posting URL. Found false is not an
// error. Submission is the submitted ledger row an applied banner quotes, set
// whether or not the tracker has a row.
type ApplicationLookup struct {
	Found       bool                `json:"found"`
	Entry       *Candidacy          `json:"entry,omitempty"`
	IdentityKey string              `json:"identityKey,omitempty"`
	History     []StatusHistoryItem `json:"history,omitempty"`
	Submission  *PriorSubmission    `json:"submission,omitempty"`
}

// PriorSubmission is the posting's submitted ledger row: a confirmed row over
// an unconfirmed one, then the most recent. Source is apply-chain for a
// submission by the application chain, user-observed or extension-claim for
// one the extension saw the person make.
type PriorSubmission struct {
	Confirmed bool   `json:"confirmed"`
	Source    string `json:"source"`
	At        Time   `json:"at"`
}

// Apply Next lanes.
const (
	LaneApplyNow      = "apply-now"
	LaneSelective     = "selective"
	LaneMarkedApplied = "marked-applied"
	LaneAwaiting      = "awaiting"
)

// ApplyItem is one row of the apply list.
type ApplyItem struct {
	ID           SafeInt  `json:"id"`
	Company      string   `json:"company"`
	Role         string   `json:"role"`
	Score        *float64 `json:"score,omitempty"`
	Lane         string   `json:"lane"`
	ReportNumber *SafeInt `json:"reportNumber,omitempty"`
	PostingHost  string   `json:"postingHost,omitempty"`
	PostingURL   string   `json:"postingUrl,omitempty"`
	Date         string   `json:"date"`
	// Version is the row's version when the list was read; a change made from
	// the list (Skip) quotes it so a row that moved since is refused.
	Version SafeInt `json:"version"`
	// LetterOffer is true for a row whose final report waits below the 4.5
	// floor for tailored documents with no override while the document
	// settings write a cover letter a form asks for only above that floor: its
	// submit confirmation offers the user's override. A screening or gated
	// report never offers it, since no override covers it.
	LetterOffer bool `json:"letterOffer,omitempty"`
	// SkipEntry is the batch skip list name that covers the company; a row
	// that has one is never run by a batch.
	SkipEntry string `json:"skipEntry,omitempty"`
	// State is one of the closed set below; Attempt is the newest run for the
	// posting, by identity key, and carries no form value.
	State   string        `json:"state"`
	Attempt *ApplyAttempt `json:"attempt,omitempty"`
	// RunID is the run in progress or the newest one; KeptTab is true while a
	// tab that run kept for the user is open. FormWaiting is true while that
	// run's filled form waits at Submit in the kept tab, for "Submit this form".
	RunID       string `json:"runId,omitempty"`
	KeptTab     bool   `json:"keptTab,omitempty"`
	FormWaiting bool   `json:"formWaiting,omitempty"`
	// LeftOut is why a list started now would leave the row out (a stop that
	// still holds, today's stop, the skip list, a stale link); empty when it
	// would run the row.
	LeftOut string `json:"leftOut,omitempty"`
	// SourceCheck is how the posting's last check reads when it did not see
	// the posting live: failed or unknown; empty when it did, or when nothing
	// was checked. Apply now and Selective rows only.
	SourceCheck string `json:"sourceCheck,omitempty"`
}

// Apply item source checks.
const (
	SourceCheckFailed  = "failed"
	SourceCheckUnknown = "unknown"
)

// Apply item states.
const (
	ApplyStateIdle      = "idle"
	ApplyStateQueued    = "queued"
	ApplyStateRunning   = "running"
	ApplyStateNeedsUser = "needs-user"
	ApplyStateSubmitted = "submitted"
	ApplyStateSkipped   = "skipped"
	ApplyStateFailed    = "failed"
)

// ApplyAttempt is the newest run for a posting.
type ApplyAttempt struct {
	RunID   string       `json:"runId"`
	At      Time         `json:"at"`
	Mode    string       `json:"mode"`
	Outcome ApplyOutcome `json:"outcome"`
}

// ApplyLane is one lane with its page of items.
type ApplyLane struct {
	Lane  string      `json:"lane"`
	Total SafeInt     `json:"total"`
	Items []ApplyItem `json:"items"`
}

// ApplySkipStatus says whether a company is on the batch skip list, so a
// dialog that submits an application can state it. Entry is the list name that
// covers the company.
type ApplySkipStatus struct {
	Company string `json:"company"`
	Listed  bool   `json:"listed"`
	Entry   string `json:"entry,omitempty"`
}

// ApplyList is the read-only apply list.
type ApplyList struct {
	Lanes    []ApplyLane `json:"lanes"`
	Page     SafeInt     `json:"page"`
	PageSize SafeInt     `json:"pageSize"`
	// Counts are per state over the rows of the page and per lane over all
	// the lane's rows.
	Counts ApplyCounts `json:"counts"`
}

// ApplyCounts count the page's rows by state and each lane's rows.
type ApplyCounts struct {
	States map[string]SafeInt `json:"states"`
	Lanes  map[string]SafeInt `json:"lanes"`
}

// OutcomeStageReach is one rate of the outcome summary: how many of the
// applications with event data reached a stage by the observation cutoff.
// Open counts applications that have not reached the stage and are still open;
// Unknown counts the cohort's applications whose events cannot say.
type OutcomeStageReach struct {
	Stage       string  `json:"stage"`
	Numerator   SafeInt `json:"numerator"`
	Denominator SafeInt `json:"denominator"`
	Open        SafeInt `json:"open"`
	Unknown     SafeInt `json:"unknown"`
}

// OutcomeGroup is the summary of the cohort or of one source channel or role
// family in it. An empty key means the field was not recorded. Insufficient
// lists why the group cannot support a comparison; empty when it can.
type OutcomeGroup struct {
	Key          string              `json:"key"`
	Applications SafeInt             `json:"applications"`
	Known        SafeInt             `json:"known"`
	Unknown      SafeInt             `json:"unknown"`
	Open         SafeInt             `json:"open"`
	Closed       SafeInt             `json:"closed"`
	Stages       []OutcomeStageReach `json:"stages"`
	Insufficient []string            `json:"insufficient"`
}

// TrackerOutcomes is the read-only outcome summary of an application-date
// cohort as of an observation cutoff. Folded counts rows that duplicate
// another row of the same posting; NoApplicationDate counts submitted rows no
// cohort can hold. It states no cause for any outcome.
type TrackerOutcomes struct {
	From              string         `json:"from"`
	To                string         `json:"to"`
	AsOf              string         `json:"asOf"`
	Overall           OutcomeGroup   `json:"overall"`
	BySource          []OutcomeGroup `json:"bySource"`
	ByRoleFamily      []OutcomeGroup `json:"byRoleFamily"`
	Folded            SafeInt        `json:"folded"`
	NoApplicationDate SafeInt        `json:"noApplicationDate"`
}
