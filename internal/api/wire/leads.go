package wire

// Lead is a hiring signal that is not yet a single posting, with the evidence
// that produced it.
type Lead struct {
	ID            SafeInt  `json:"id"`
	Source        string   `json:"source"`
	Category      string   `json:"category"`
	State         string   `json:"state"`
	Reason        string   `json:"reason,omitempty"`
	Company       string   `json:"company"`
	Roles         []string `json:"roles"`
	Locations     []string `json:"locations"`
	WorkModel     string   `json:"workModel,omitempty"`
	Compensation  string   `json:"compensation,omitempty"`
	ApplyLinks    []string `json:"applyLinks"`
	ContactEmails []string `json:"contactEmails"`
	ContactMethod string   `json:"contactMethod,omitempty"`
	Sponsorship   string   `json:"sponsorship,omitempty"`
	Confidence    float64  `json:"confidence"`
	PosterType    string   `json:"posterType,omitempty"`
	// Author is who posted the lead.
	Author string `json:"author,omitempty"`
	// Expanded is empty until a careers page was read, then `none` or a count
	// of postings.
	Expanded string `json:"expanded,omitempty"`
	// Evidence is what produced the lead; either Text or AbsentReason is set.
	EvidenceURL     string  `json:"evidenceUrl,omitempty"`
	EvidenceTitle   string  `json:"evidenceTitle,omitempty"`
	EvidenceText    string  `json:"evidenceText,omitempty"`
	EvidenceAdapter string  `json:"evidenceAdapter,omitempty"`
	EvidenceAt      *Time   `json:"evidenceAt,omitempty"`
	AbsentReason    string  `json:"evidenceAbsentReason,omitempty"`
	InboxID         SafeInt `json:"inboxId,omitempty"`
	// A listed lead promoted to the inbox carries that row's status and, once
	// its evaluation wrote them, the report number and the tracker row.
	InboxStatus  string   `json:"inboxStatus,omitempty"`
	ReportNumber *SafeInt `json:"reportNumber,omitempty"`
	TrackerID    SafeInt  `json:"trackerId,omitempty"`
	CreatedAt    Time     `json:"createdAt"`
	UpdatedAt    Time     `json:"updatedAt"`
	// Evaluable says an evaluation run naming this lead acts on it.
	Evaluable bool `json:"evaluable"`
}

// LeadPage is one page of leads with the count per category.
type LeadPage struct {
	Rows     []Lead             `json:"rows"`
	Page     SafeInt            `json:"page"`
	PageSize SafeInt            `json:"pageSize"`
	Total    SafeInt            `json:"total"`
	Counts   map[string]SafeInt `json:"counts"`
	// Evaluating and Expanding say a pass of that kind is running; another
	// start of it is refused until it ends.
	Evaluating bool `json:"evaluating,omitempty"`
	Expanding  bool `json:"expanding,omitempty"`
}

// LeadRunRequest starts a pass over the leads store. IDs restricts it to these
// leads; for an expansion they are read again even when the ledger holds their
// page.
type LeadRunRequest struct {
	IDs []SafeInt `json:"ids,omitempty"`
}
