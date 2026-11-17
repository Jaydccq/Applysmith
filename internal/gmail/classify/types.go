package classify

import "time"

// Signal types.
const (
	TypeApplied      = "applied_confirmation"
	TypeReplyNeeded  = "recruiter_reply_needed"
	TypeAssessment   = "online_assessment"
	TypeInterview    = "interview_scheduled"
	TypeReminder     = "interview_reminder"
	TypeOffer        = "offer"
	TypeRejection    = "rejection"
	TypeActionNeeded = "action_required"
	TypeReview       = "review"
	TypeLowPriority  = "low_priority"
)

// Tiers.
const (
	TierActionable = "actionable"
	TierReview     = "review"
	TierLow        = "low_priority"
	TierSuppressed = "suppressed"
)

// Directions.
const (
	DirIncoming = "incoming"
	DirOutgoing = "outgoing"
)

// Ignore reasons.
const (
	ReasonNoMatch        = "no_event_match"
	ReasonAllBlocked     = "all_event_candidates_blocked"
	ReasonInvalidMessage = "invalid_message"
)

// Review reasons.
const (
	ReviewLowConfidence   = "confidence_below_threshold"
	ReviewCompanyUnknown  = "company_unknown"
	ReviewCompanyConflict = "company_identity_conflict"
	ReviewCompanyBoard    = "company_is_job_board"
	// ReviewSenderUnverified marks a claim from a sender that no taxonomy list
	// and no employer-domain match vouches for.
	ReviewSenderUnverified = "sender_unverified"
	// ReviewSignupWindowUnknown marks account-lifecycle mail classified while
	// no source of the product's own sign-up windows was configured.
	ReviewSignupWindowUnknown = "signup_window_unknown"
)

// RuleAssessmentSubmitted is the rule id of the confirmation that an
// assessment was submitted; the thread then waits on the company.
const RuleAssessmentSubmitted = "assessment.completion_confirmation"

// ReviewThreshold is the confidence below which a signal goes to review.
const ReviewThreshold = 0.55

// Placeholders stored for an unresolved value; they are never scored as text.
const (
	UnknownCompany = "Unknown Company"
	UnknownRole    = "Unknown Role"
)

// Sender is a parsed From, To or Cc address.
type Sender struct{ Name, Email string }

// Input is one message in the shape the classifier reads.
type Input struct {
	MessageID, ThreadID string
	// From, To, Cc and Subject are the raw header values.
	From, To, Cc, Subject string
	Labels                []string
	Snippet               string
	// Text is the decoded text (HTML parts with markup removed); Raw is the
	// same parts with markup intact, where hrefs survive.
	Text, Raw  string
	ReceivedAt time.Time
	// Mailbox is the authorized address; mail from it is outgoing.
	Mailbox string
}

// Span says where in the message a piece of evidence was found.
type Span struct {
	Source  string `json:"source"` // subject, body, sender_name, sender_domain, sender_local_part
	Snippet string `json:"snippet"`
	Start   int    `json:"start"`
}

// Company and role resolution strategies.
const (
	StratSubject       = "subject_pattern"
	StratATSName       = "ats_sender_name"
	StratBody          = "body_pattern"
	StratTenantSlug    = "ats_tenant_slug"
	StratWorkdayTenant = "workday_subdomain"
	StratDisplayName   = "sender_display_name"
	StratSenderDomain  = "sender_domain"
	StratCoRecipient   = "co_recipient"
	StratUnknown       = "unknown"
)

// CompanyEvidence is how the employer was found.
type CompanyEvidence struct {
	Value      string   `json:"value"`
	Strategy   string   `json:"strategy"`
	Confidence float64  `json:"confidence"`
	Span       *Span    `json:"span,omitempty"`
	Conflict   bool     `json:"conflict,omitempty"`
	Conflicts  []string `json:"conflictingValues,omitempty"`
}

// RoleEvidence is how the role was found.
type RoleEvidence struct {
	Value          string `json:"value"`
	Strategy       string `json:"strategy"` // subject, body, fallback_keyword, unknown
	StrippedPrefix bool   `json:"strippedPrefix,omitempty"`
	Span           *Span  `json:"span,omitempty"`
}

// EventEvidence is the family rule that fired.
type EventEvidence struct {
	Type   string `json:"type"`
	RuleID string `json:"ruleId"`
}

// Identifiers are structured posting identifiers read from ATS URLs or labeled
// text.
type Identifiers struct {
	JobID         string `json:"jobId,omitempty"`
	ApplicationID string `json:"applicationId,omitempty"`
	ReqID         string `json:"reqId,omitempty"`
	ATS           string `json:"ats,omitempty"`
	Tenant        string `json:"tenant,omitempty"`
	Source        string `json:"source,omitempty"`
}

// Confidence is the overall score and its per-dimension parts.
type Confidence struct {
	Overall     float64 `json:"overall"`
	Event       float64 `json:"event"`
	Company     float64 `json:"company"`
	Role        float64 `json:"role"`
	SenderTrust float64 `json:"senderTrust"`
}

// Candidate is a family that matched, with the guard that blocked it (empty
// for the winner).
type Candidate struct {
	Type      string `json:"type"`
	RuleID    string `json:"ruleId"`
	Source    string `json:"source"`
	BlockedBy string `json:"blockedBy,omitempty"`
}

// Evidence is the trail stored with a signal.
type Evidence struct {
	Company     CompanyEvidence `json:"company"`
	Role        RoleEvidence    `json:"role"`
	Event       EventEvidence   `json:"event"`
	Identifiers *Identifiers    `json:"identifiers,omitempty"`
	Confidence  Confidence      `json:"confidence"`
	Candidates  []Candidate     `json:"candidates,omitempty"`
	// Spans lists the matched text spans of the winning rule.
	Spans []Span `json:"spans,omitempty"`
}

// Signal is the classifier's output for a message that matched an event rule.
type Signal struct {
	MessageID, ThreadID string
	Type                string
	Tier                string
	Direction           string
	Company, Role       string
	Confidence          float64
	ReviewReasons       []string
	DueAt               time.Time // zero when none
	ReceivedAt          time.Time
	Sender, Subject     string
	Snippet             string
	Evidence            Evidence
}

// Result is the verdict on one message: a signal, or an ignored trace.
type Result struct {
	Signal *Signal
	// Reason is set when the message was ignored.
	Reason     string
	Candidates []Candidate
}
