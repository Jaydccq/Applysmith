package wire

// The learning event upload, the manual-fill corpus, the candidates the user
// decides on and the usage derived from telemetry (answer-bank spec: the
// learning candidates, manual fills as ground truth, the event upload route and
// usage telemetry).

// LearningReport is the user's pointed-at field report. The server sanitizes it
// again whatever the sender did: URLs lose credentials, query and fragment,
// e-mail-like and phone-like strings in the text become [email] and [phone],
// lengths are clamped, and a snippet with an attribute outside the allowlist, a
// value on a non-choice element or script or style markup is rejected.
type LearningReport struct {
	Note           string `json:"note"`
	Tag            string `json:"tag"`
	Role           string `json:"role"`
	AccessibleName string `json:"accessibleName"`
	Path           string `json:"path"`
	NearbyText     string `json:"nearbyText"`
	URL            string `json:"url"`
	FrameURL       string `json:"frameUrl"`
	ShadowDepth    int    `json:"shadowDepth"`
	Snippet        string `json:"snippet"`
}

// LearningEvent is one uncertainty event, correction, user report, manual fill,
// unsafe-control or frame-unreachable record. EventID is the client's id: the server is
// idempotent by it and holds it only as a lowercase UUID. RunID is a run the
// server issued, EntryID names an entry of the user's or the defaults bank and
// Reason is one of a closed set; an event with any other shape or an unknown
// entry is rejected with a reason. Before
// is the value the system wrote, After the value the control holds now,
// OptionLabel the chosen option's label, EntryID the bank entry that produced
// Before. A correction carries Origin user-edit or manual-fill; any other
// origin is rejected. Section is the captions of the form sections around the
// control, outermost first. The server decides for itself which values it
// keeps: a password, one-time code, payment or government-identifier control
// stores no value, and a self-identification, date-of-birth or
// profile-identity control stores only that an edit happened, whatever the
// client sends. Of a value-withheld control it keeps no options, no section
// and no question text, only the category ("secret:password",
// "self-id:gender"); a report about such a control keeps its note, tag, role
// and input type only.
type LearningEvent struct {
	EventID      string   `json:"eventId"`
	Kind         string   `json:"kind"`
	Host         string   `json:"host"`
	Question     string   `json:"question"`
	Section      []string `json:"section,omitempty"`
	ControlKind  string   `json:"controlKind"`
	InputType    string   `json:"inputType,omitempty"`
	Autocomplete string   `json:"autocomplete,omitempty"`
	Options      []string `json:"options,omitempty"`
	At           Time     `json:"at"`
	Reason       string   `json:"reason,omitempty"`
	Before       string   `json:"before,omitempty"`
	After        string   `json:"after,omitempty"`
	OptionLabel  string   `json:"optionLabel,omitempty"`
	EntryID      string   `json:"entryId,omitempty"`
	Origin       string   `json:"origin,omitempty"`
	ReportNumber *SafeInt `json:"reportNumber,omitempty"`
	RunID        string   `json:"runId,omitempty"`
	// Selector names the control inside its run and is never stored.
	// TrustedInput is the sender's statement that the value came from a trusted
	// user input event. A manual fill is ground truth only with TrustedInput, a
	// RunID, and a value the system did not itself write anywhere in that run.
	Selector     string          `json:"selector,omitempty"`
	TrustedInput bool            `json:"trustedInput,omitempty"`
	Report       *LearningReport `json:"report,omitempty"`
}

// SystemWriteRequest tells the server which value the system itself wrote into
// a control during a run, so that a manual fill equal to it is not ground
// truth. RunID is a run the server issued.
type SystemWriteRequest struct {
	RunID    string `json:"runId"`
	Selector string `json:"selector"`
	Value    string `json:"value"`
}

// SystemWriteRecorded confirms a recorded system write.
type SystemWriteRecorded struct {
	Recorded bool `json:"recorded"`
}

// LearningEventsRequest holds 1 to learn.MaxBatch events; a larger upload is
// payload_too_large and the sender splits it.
type LearningEventsRequest struct {
	Events []LearningEvent `json:"events"`
}

// LearningEventRejection names an event that was not stored and why.
type LearningEventRejection struct {
	EventID string `json:"eventId"`
	Reason  string `json:"reason"`
}

// LearningEventsResult reports an upload. Accepted counts the events the server
// holds from it, new or already held; Duplicates names the ones it already
// held.
type LearningEventsResult struct {
	Accepted   SafeInt                  `json:"accepted"`
	Duplicates []string                 `json:"duplicates"`
	Rejected   []LearningEventRejection `json:"rejected"`
}

// ManualFill is what the user typed or chose themselves. Value, OptionLabel
// and PriorValue are absent for a value-withheld control (Withheld names the
// class). GroundTruth is false for a value whose shape contradicts a short
// identity label.
type ManualFill struct {
	ID           SafeInt  `json:"id"`
	At           Time     `json:"at"`
	ReceivedAt   Time     `json:"receivedAt"`
	Host         string   `json:"host"`
	Question     string   `json:"question"`
	ControlKind  string   `json:"controlKind"`
	Value        *string  `json:"value,omitempty"`
	OptionLabel  *string  `json:"optionLabel,omitempty"`
	Withheld     string   `json:"withheld"`
	GroundTruth  bool     `json:"groundTruth"`
	PriorValue   *string  `json:"priorValue,omitempty"`
	EntryID      string   `json:"entryId"`
	RunID        string   `json:"runId"`
	ReportNumber *SafeInt `json:"reportNumber,omitempty"`
}

// ManualFillList is one page of the manual-fill records, newest first.
type ManualFillList struct {
	Fills []ManualFill `json:"fills"`
	Total SafeInt      `json:"total"`
}

// ManualFillDeleted confirms a deletion. The support of every pending candidate
// that rested on the record dropped by one.
type ManualFillDeleted struct {
	ID SafeInt `json:"id"`
}

// Candidate states.
const (
	CandidatePending  = "pending"
	CandidateApproved = "approved"
	CandidateRejected = "rejected"
)

// AnswerCandidateReview is the model's sort of a waiting new-entry candidate;
// an empty Verdict is not reviewed yet. Verdict is reusable, per-application,
// optional, follow-up, company-specific, trap or sensitive: only an unreviewed
// or reusable candidate is suggested, the others are shown apart with their
// Reason. Draft is an answer drafted from the user's own profile and CV for a
// reusable one, with the keys of the facts it Cited; empty when the facts do
// not say or the candidate proposes its own answer.
type AnswerCandidateReview struct {
	Verdict string   `json:"verdict"`
	Reason  string   `json:"reason"`
	Draft   string   `json:"draft"`
	Cited   []string `json:"cited"`
}

// AnswerCandidate is a proposal the user decides on. A new-entry candidate
// proposes an entry (EntryID is the id it gets on approval); an
// extend-examples candidate proposes SeedQuestion as one more example of
// TargetEntryID and never an answer. LongForm marks an answer withheld from
// the proposal because it is prose. Sections and Options say where the evidence
// was seen; a value-withheld control contributes neither. Multiple marks a group
// of checkboxes: approving ticks the options the answer names.
type AnswerCandidate struct {
	ID             SafeInt               `json:"id"`
	Key            string                `json:"key"`
	Type           string                `json:"type"`
	State          string                `json:"state"`
	Source         string                `json:"source"`
	TargetEntryID  string                `json:"targetEntryId"`
	EntryID        string                `json:"entryId"`
	SeedQuestion   string                `json:"seedQuestion"`
	Questions      []string              `json:"questions"`
	Hosts          []string              `json:"hosts"`
	Sections       []string              `json:"sections"`
	Options        []string              `json:"options"`
	Multiple       bool                  `json:"multiple"`
	ProposedKind   string                `json:"proposedKind"`
	ProposedAnswer string                `json:"proposedAnswer"`
	LongForm       bool                  `json:"longForm"`
	Flags          []string              `json:"flags"`
	Support        SafeInt               `json:"support"`
	FirstSeenAt    Time                  `json:"firstSeenAt"`
	LastSeenAt     Time                  `json:"lastSeenAt"`
	CreatedAt      Time                  `json:"createdAt"`
	UpdatedAt      Time                  `json:"updatedAt"`
	DecidedAt      *Time                 `json:"decidedAt,omitempty"`
	Review         AnswerCandidateReview `json:"review"`
}

// AnswerCandidateList lists candidates, pending first, and the edits the keep
// press is offered for.
type AnswerCandidateList struct {
	Candidates []AnswerCandidate `json:"candidates"`
	Edits      []AnswerEdit      `json:"edits,omitempty"`
}

// AnswerEdit is a value the user changed by hand (After, the picked option's
// label for a choice) over the value Before that the entry EntryID wrote. POST
// /api/answer-bank/manual-fills/{fillId}/keep, the keep press, makes it that
// entry's answer (flow round 9, K-1); .../leave keeps the entry's answer and
// answers with the edit (flow round 10, K2-1).
type AnswerEdit struct {
	FillID   SafeInt `json:"fillId"`
	EntryID  string  `json:"entryId"`
	Host     string  `json:"host"`
	Question string  `json:"question"`
	Before   string  `json:"before"`
	After    string  `json:"after"`
}

// AnswerCandidateGenerate asks for a generation run. SinceDays overrides the
// 30-day evidence window.
type AnswerCandidateGenerate struct {
	SinceDays *SafeInt `json:"sinceDays,omitempty"`
}

// AnswerCandidateGenerateResult reports a generation run. Generation only
// creates and refreshes pending candidates; it never approves one.
type AnswerCandidateGenerateResult struct {
	Created     SafeInt           `json:"created"`
	Refreshed   SafeInt           `json:"refreshed"`
	Reproposed  SafeInt           `json:"reproposed"`
	Corrections SafeInt           `json:"corrections"`
	Candidates  []AnswerCandidate `json:"candidates"`
}

// AnswerCandidateApprove approves a candidate. Answer, when not empty, is the
// user's own answer for a new entry, stored in place of the proposed one.
type AnswerCandidateApprove struct {
	Answer string `json:"answer,omitempty"`
}

// AnswerCandidateDecision is the candidate after the user approved or rejected
// it, or took either back. EntryID is the entry an approval created or
// extended, or a take-back removed or changed.
type AnswerCandidateDecision struct {
	Candidate AnswerCandidate `json:"candidate"`
	EntryID   string          `json:"entryId"`
}

// AnswerBankUsageRow is the usage of one entry, derived from events. The times
// are empty text when the entry never had that event; AverageConfidence is
// absent when no event carried one; ExampleQuestions are the latest distinct
// question texts, oldest first.
type AnswerBankUsageRow struct {
	EntryID           string   `json:"entryId"`
	Matches           SafeInt  `json:"matches"`
	Applies           SafeInt  `json:"applies"`
	Abstains          SafeInt  `json:"abstains"`
	Corrections       SafeInt  `json:"corrections"`
	LastMatchedAt     string   `json:"lastMatchedAt"`
	LastAppliedAt     string   `json:"lastAppliedAt"`
	LastCorrectedAt   string   `json:"lastCorrectedAt"`
	LastActivityAt    string   `json:"lastActivityAt"`
	AverageConfidence *float64 `json:"averageConfidence,omitempty"`
	ExampleQuestions  []string `json:"exampleQuestions"`
}

// AnswerBankUsage lists every bank entry, the most recently active first. The
// counters and the mean confidence cover the last WindowDays days; the times
// cover every retained event.
type AnswerBankUsage struct {
	WindowDays SafeInt              `json:"windowDays"`
	Rows       []AnswerBankUsageRow `json:"rows"`
}
