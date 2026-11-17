// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

// Package evaluate decides what a job posting is worth to one job seeker: the
// eligibility gate, the quick screen, the deep evaluation, the arithmetic that
// turns the model's evidence into a score, the report files and the worker
// pool that runs it all. The model supplies evidence; Go supplies every number.
package evaluate

import (
	"errors"
	"fmt"
	"time"

	"applysmith/internal/discovery"
)

// Mode says how far a request goes.
type Mode string

// The three modes. A user-started request defaults to deep.
const (
	ModeAuto   Mode = "auto"
	ModeScreen Mode = "screen"
	ModeDeep   Mode = "deep"
)

// Priority orders the queue: user-started before scan-started.
type Priority string

const (
	PriorityUser Priority = "user"
	PriorityScan Priority = "scan"
)

// Outcome is how a job ended.
type Outcome string

// The five outcomes. Every job ends in exactly one.
const (
	OutcomeReported  Outcome = "reported"
	OutcomeScreened  Outcome = "screened"
	OutcomeGated     Outcome = "gated"
	OutcomeDuplicate Outcome = "duplicate"
	OutcomeFailed    Outcome = "failed"
)

// Phase is where a job is. Phase changes flow through the run stream.
type Phase string

const (
	PhaseQueued     Phase = "queued"
	PhaseReading    Phase = "reading"
	PhaseGating     Phase = "gating"
	PhaseScreening  Phase = "screening"
	PhaseEvaluating Phase = "evaluating"
	PhaseWriting    Phase = "writing"
)

// Failure reasons carried in the run's failure_reason.
const (
	ReasonUnreadable  = "posting-unreadable"
	ReasonInvalid     = "invalid-structured-output"
	ReasonTimeout     = "timeout"
	ReasonInterrupted = "interrupted"
	ReasonCancelled   = "cancelled"
	ReasonProfile     = "profile-unavailable"
	ReasonPrompt      = "prompt-invalid"
	ReasonWrite       = "report-write-failed"
)

// ErrQueueFull is the refusal of a request beyond the queue bound. The API
// maps it to the envelope code unavailable, reason queue-full, retryable.
var ErrQueueFull = errors.New("evaluate: the evaluation queue is full")

// JobError is a failed job's reason.
type JobError struct {
	Reason string
	Err    error
}

func (e *JobError) Error() string {
	if e.Err == nil {
		return "evaluation failed: " + e.Reason
	}
	return fmt.Sprintf("evaluation failed: %s: %v", e.Reason, e.Err)
}

func (e *JobError) Unwrap() error { return e.Err }

// Request is one evaluation request, the same from every ingress.
type Request struct {
	URL  string
	Text string
	// Scanned is the text a scan read from the posting page. It stands in
	// only when the page read fails without showing the posting gone.
	Scanned string
	// Hints are used when the posting text does not carry the field.
	Title, Company, Location, Compensation string
	Seniority                              string
	Source                                 string
	InboxRowID                             int64
	// ScanRun is the scan run that queued the job; its outcome is added to
	// that run's record.
	ScanRun  string
	Mode     Mode
	Force    bool
	Priority Priority
	// PostedAt is when the source says the posting was published; zero if unknown.
	PostedAt time.Time
	// Review is the run id of the quick screen's review this evaluation in
	// depth answers, when the inbox drain took it (OpenReviews, F-8). The board
	// closes that review into this run's entry. Left out of the stored input
	// when empty, so the input of every other request reads as before.
	Review string `json:",omitempty"`
	// Drain is the run id of the inbox.evaluate run that queued the job. The
	// job is that run's work: its board entry joins the run's batch, and it is
	// told only through the run's roll-up (F-2, F-8). Left out when empty.
	Drain string `json:",omitempty"`
	// Applying marks the evaluation an application run asks for to ground
	// the cover letter a form asks for. The user chose to apply to the
	// posting, so the eligibility gate does not stop it. Left out when false.
	Applying bool `json:",omitempty"`
}

// Result is the outcome of a job.
type Result struct {
	RunID          string
	Outcome        Outcome
	ReportNumber   int64
	Reason         string
	Decision       string
	Score          *float64
	Recommendation string
	// Explanation says in plain words what a failed job needs from the user,
	// for the reasons where the code alone does not, or why a review waits for
	// the user (CapHeldReview).
	Explanation string
	// Review is true when a screening decision is review and the day's spend
	// limit kept it from the deep evaluation: the user is needed.
	Review bool
	// ReviewReason is the code of reviewReasons the quick screen gave for a
	// review.
	ReviewReason string
	// PostingURL and InboxRowID are the job's posting and the inbox row it came
	// from, as JobOutcome reads them.
	PostingURL string
	InboxRowID int64
	// Closed says the job's page read found the posting closed.
	Closed bool
	// charge is what the failure costs every inbox row the job settles, a
	// joined one too: a read that decided nothing about the posting (the app's
	// own machine stopped it, or the site throttled it) is undecided, and a
	// shutdown costs nothing.
	charge discovery.Charge
}

// Event is one message on the run stream.
type Event struct {
	RunID   string
	Phase   Phase
	Final   bool
	Outcome Outcome
	Result  *Result
}
