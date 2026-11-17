package wire

// RunAccepted answers the routes that start a long operation.
type RunAccepted struct {
	RunID string `json:"runId"`
	// Ahead is set for an application start that waits for the apply window:
	// how many starts go before it, the one holding the window included.
	Ahead SafeInt `json:"ahead,omitempty"`
}

// Run statuses.
const (
	RunRunning        = "running"
	RunSucceeded      = "succeeded"
	RunFailed         = "failed"
	RunCancelling     = "cancelling"
	RunCancelled      = "cancelled"
	RunReviewRequired = "review_required"
)

// RunView is the state of one run.
type RunView struct {
	RunID         string `json:"runId"`
	Kind          string `json:"kind"`
	Status        string `json:"status"`
	Stage         string `json:"stage,omitempty"`
	FailureReason string `json:"failureReason,omitempty"`
	CreatedAt     Time   `json:"createdAt"`
	UpdatedAt     Time   `json:"updatedAt"`
	FinishedAt    *Time  `json:"finishedAt,omitempty"`
	// Entry is the run's entry on the runs board, set by GET /api/runs/{id}
	// when the board holds one.
	Entry *BoardEntry `json:"entry,omitempty"`
	// Conversation is the Ask Agent conversation that started the run, set by
	// GET /api/runs/{id} when one did.
	Conversation string `json:"conversation,omitempty"`
}

// RunPhase is a phase change on the run stream.
type RunPhase struct {
	RunID string `json:"runId"`
	Phase string `json:"phase"`
}

// RunProgress is a progress line of a run that has no phases, such as a scan.
type RunProgress struct {
	RunID string `json:"runId"`
	Line  string `json:"line"`
}

// RunFinal ends a run stream with the outcome of the run.
type RunFinal struct {
	RunID        string    `json:"runId"`
	Status       string    `json:"status"`
	Outcome      string    `json:"outcome,omitempty"`
	ReportNumber *SafeInt  `json:"reportNumber,omitempty"`
	Reason       string    `json:"reason,omitempty"`
	Decision     string    `json:"decision,omitempty"`
	Review       bool      `json:"review,omitempty"`
	ScanID       string    `json:"scanId,omitempty"`
	Documents    []SafeInt `json:"documents,omitempty"`
}
