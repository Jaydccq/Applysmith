package wire

// Reasons carried by an unavailable error of the evaluation routes.
const ReasonQueueFull = "queue-full"

// Evaluation modes.
const (
	ModeAuto   = "auto"
	ModeScreen = "screen"
	ModeDeep   = "deep"
)

// EvaluationRequest starts an evaluation of one posting, given as a URL, as
// text, or both. Mode defaults to deep.
type EvaluationRequest struct {
	URL          string  `json:"url,omitempty"`
	Text         string  `json:"text,omitempty"`
	Title        string  `json:"title,omitempty"`
	Company      string  `json:"company,omitempty"`
	Location     string  `json:"location,omitempty"`
	Compensation string  `json:"compensation,omitempty"`
	Source       string  `json:"source,omitempty"`
	InboxRowID   SafeInt `json:"inboxRowId,omitempty"`
	Mode         string  `json:"mode,omitempty"`
	Force        bool    `json:"force,omitempty"`
}

// EvaluationView is the state and outcome of one evaluation job. Outcome is
// empty while the job runs.
type EvaluationView struct {
	RunID          string   `json:"runId"`
	Status         string   `json:"status"`
	Phase          string   `json:"phase,omitempty"`
	Outcome        string   `json:"outcome,omitempty"`
	ReportNumber   *SafeInt `json:"reportNumber,omitempty"`
	Reason         string   `json:"reason,omitempty"`
	Explanation    string   `json:"explanation,omitempty"`
	Decision       string   `json:"decision,omitempty"`
	Review         bool     `json:"review"`
	Score          *float64 `json:"score,omitempty"`
	Recommendation string   `json:"recommendation,omitempty"`
	// ReviewReason words why a quick screen kept the posting for the user;
	// PostingURL and InboxRowID are what "Evaluate in full" sends again.
	ReviewReason string   `json:"reviewReason,omitempty"`
	PostingURL   string   `json:"postingUrl,omitempty"`
	InboxRowID   *SafeInt `json:"inboxRowId,omitempty"`
}

// ReportSummary is one row of the report index.
type ReportSummary struct {
	Number            SafeInt  `json:"number"`
	Kind              string   `json:"kind"`
	Company           string   `json:"company"`
	Role              string   `json:"role"`
	PostingURL        string   `json:"postingUrl"`
	Score             *float64 `json:"score,omitempty"`
	ScoreKind         string   `json:"scoreKind"`
	Recommendation    string   `json:"recommendation"`
	Legitimacy        string   `json:"legitimacy"`
	Verification      string   `json:"verification"`
	CreatedAt         Time     `json:"createdAt"`
	DocumentsEligible bool     `json:"documentsEligible"`
	// TrackerStatus is the status of the posting's tracker row; absent when
	// the tracker holds no row for it.
	TrackerStatus string `json:"trackerStatus,omitempty"`
}

// ReportPage is one page of the report catalog.
type ReportPage struct {
	Reports  []ReportSummary `json:"reports"`
	Page     SafeInt         `json:"page"`
	PageSize SafeInt         `json:"pageSize"`
	Total    SafeInt         `json:"total"`
}

// ReportDetail is a report: its index row, the header fields parsed from the
// Markdown, and the Markdown itself.
type ReportDetail struct {
	Report  ReportSummary     `json:"report"`
	Header  map[string]string `json:"header"`
	Content string            `json:"content"`
}
