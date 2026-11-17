// Package apply is the application chain: one staged runner that resolves a
// posting, signs in, walks and fills every page, checks the result, and, only
// when the user asked it to submit, presses the final Submit once and records
// what happened. Every decision that guards a side effect goes through
// internal/gates and is written to the gate audit.
package apply

import (
	"context"
	"time"

	"applysmith/internal/gates"
)

// Stage names, in run order. Check and fix run inside the page walk, once per
// page, and roll up into one record each.
type Stage string

const (
	StagePreflight Stage = "preflight"
	StageResolve   Stage = "resolve"
	StageAccount   Stage = "account"
	StageFill      Stage = "fill"
	StageCheck     Stage = "check"
	StageFix       Stage = "fix"
	StageSubmit    Stage = "submit"
	StageConfirm   Stage = "confirm"
	StageRecord    Stage = "record"
	StageLearn     Stage = "learn"
)

// Stages lists every stage in run order.
var Stages = []Stage{StagePreflight, StageResolve, StageAccount, StageFill, StageCheck, StageFix, StageSubmit, StageConfirm, StageRecord, StageLearn}

// Class is the failure class of a stop. The stage that fails assigns it from
// what that stage knows; it is never derived from message text.
type Class string

const (
	ClassEnvironment Class = "environment" // the machine is not ready
	ClassUnavailable Class = "unavailable" // the site is down for a while
	ClassCredential  Class = "credential"  // a sign-in the system does not hold
	ClassPosting     Class = "posting"     // nothing to apply to
	ClassAnswerGap   Class = "answer-gap"  // the form asked what the data does not answer
	ClassDefect      Class = "defect"      // the system did something wrong
	// ClassVerification is something only the person can do in the page that
	// this run did not or could not do: a code the site emailed, or a bot check
	// (a captcha or an interstitial). The person finishes it in the kept tab.
	ClassVerification Class = "verification"
)

// Status of one stage record.
type Status string

const (
	StatusOK      Status = "ok"
	StatusSkipped Status = "skipped"
	StatusFailed  Status = "failed"
)

// StageLine is one stage's status, one-line detail and timing. A detail
// never holds a password, a one-time code, a token or a key.
type StageLine struct {
	Stage     Stage     `json:"stage"`
	Status    Status    `json:"status"`
	Detail    string    `json:"detail"`
	StartedAt time.Time `json:"startedAt"`
	Ms        int64     `json:"ms"`
}

// OutcomeKind is exactly one of three.
type OutcomeKind string

const (
	OutcomeSubmitted OutcomeKind = "submitted"
	OutcomeFilled    OutcomeKind = "filled"
	OutcomeStopped   OutcomeKind = "stopped"
	outcomeRunning   OutcomeKind = "running"
)

// Confirm sources: which evidence decided a confirmation.
const (
	ConfirmReceipt     = "receipt"
	ConfirmATSResponse = "ats_response"
	ConfirmPage        = "page"
	ConfirmObserved    = "observed"
)

// Outcome is the verdict of a run.
type Outcome struct {
	Kind          OutcomeKind `json:"kind"`
	Stage         Stage       `json:"stage,omitempty"`
	Class         Class       `json:"class,omitempty"`
	Reason        string      `json:"reason,omitempty"`
	Confirmed     bool        `json:"confirmed,omitempty"`
	ConfirmSource string      `json:"confirmSource,omitempty"`
	// Board is the runs-board reason of a stop that needs the user.
	Board string `json:"board,omitempty"`
}

// Tools that may carry the submit capability (the gates' lane).
const (
	ToolAutofill  = gates.ToolAutofillApplication
	ToolFromList  = gates.ToolApplyFromList
	defaultTool   = gates.ToolAutofillApplication
	maxReasonLen  = 600
	maxDetailLen  = 400
	runIDTimePart = "20060102T150405.000"
)

// Input is one run's request. The caller is recorded with the run and read by
// the submit authorization, a dry run's kept tab and a correction request.
type Input struct {
	URL string
	// Submit false is a dry run: every stage up to check and fix, no claim,
	// no tracker change, no captcha solve, and an account only when the user
	// started the run.
	Submit bool
	// UserStarted is set by the caller when a user's own act started the run:
	// a start from the dashboard or a typed command, an Ask Agent call in a
	// turn the user typed, and each continuation of one of them. A batch the
	// app resumed after a restart leaves it unset (AP-1). Only such a run may
	// create an account, whether it submits or not (F-8).
	UserStarted bool
	// ExplicitRequest is derived by the caller from the user's own act: the
	// Ask Agent's guard chain, a typed --submit, or the dashboard's
	// confirmation flag. A model's choice never sets it.
	ExplicitRequest bool
	Resubmit        bool
	// NamedRetry is set only when the user named the row's company.
	NamedRetry bool
	// EmployerURL is a mirror resolution a list already made; the identity
	// stays URL.
	EmployerURL string
	// Preflight is the environment verdict a list took once for its rows;
	// nil makes the run take its own.
	Preflight *Verdict
	// PreflightFn takes the verdict when the run needs it, for a list whose
	// first row should carry the preflight so a blocked one is a run record.
	PreflightFn func(ctx context.Context) *Verdict
	Caller      gates.Caller
	// Tool is the submit lane the caller belongs to; empty reads as the
	// single-posting tool.
	Tool string
	// Company and Role come from a list row and fill in what the report and
	// tracker do not hold.
	Company, Role  string
	ConversationID string
	UserMessage    string
	// OwnWords is the part of UserMessage the user wrote, without pasted or
	// quoted text, as the Ask Agent reads it; a correction is asked and decided
	// from it alone.
	OwnWords string
	// UserMessageHash is the salted hash of the message a run of a resumed
	// batch was asked by; UserMessage is empty then, and the text is never kept.
	UserMessageHash string
	// Started is called once with the run id before the first stage, so a
	// caller that returns early can name the run it started.
	Started func(runID string)
	// RunID names the run: a queued start answers with the id before the run
	// exists. Empty makes one up.
	RunID string
	// BatchID names the list the run belongs to; empty for a single posting.
	BatchID string
	// Hold is the row's claim on the list's browser lane; nil for a run that
	// has the window to itself.
	Hold *LaneHold
}

// PageReport is the roll-up of one page of the walk.
type PageReport struct {
	URL        string   `json:"url"`
	Step       int      `json:"step"`
	Filled     int      `json:"filled"`
	Prefilled  int      `json:"prefilled"`
	Unresolved []string `json:"unresolved,omitempty"`
	// Leftovers are the needs-user leftovers. One with an answer-bank entry
	// draft is a question the user can answer from the run record; one without
	// (a value the site refused) only the user completes, in the kept tab.
	Leftovers   []Leftover `json:"leftovers,omitempty"`
	Uncommitted []string   `json:"uncommitted,omitempty"`
	Mismatches  []string   `json:"identityMismatches,omitempty"`
	Flags       []string   `json:"flags,omitempty"`
	// Review is the advisory review's status: "ok", "failed (<cause>)" or
	// "skipped (<cause>)".
	Review          string `json:"review,omitempty"`
	BlockedSubmits  int    `json:"blockedSubmits,omitempty"`
	Readiness       string `json:"readiness,omitempty"`
	FillMs          int64  `json:"fillMs"`
	CheckMs         int64  `json:"checkMs"`
	FixMs           int64  `json:"fixMs"`
	FixLoops        int    `json:"fixLoops,omitempty"`
	FixAttempts     int    `json:"fixAttempts,omitempty"`
	FixPlannerCalls int    `json:"fixPlannerCalls,omitempty"`
	FixFixed        int    `json:"fixFixed,omitempty"`
	FixAbstained    int    `json:"fixAbstained,omitempty"`
	FixFailed       int    `json:"fixFailed,omitempty"`
	// Ladder is the completion ladder's accounting for the page.
	Ladder          LadderReport `json:"ladder"`
	TimingAnswers   int          `json:"timingAnswers,omitempty"`
	TimingReviewed  int          `json:"timingReviewed,omitempty"`
	Notes           []string     `json:"notes,omitempty"`
	EngineReinjects int          `json:"reinjections,omitempty"`
}

// Run is the record of one run. It is written at every stage boundary and
// again when the run ends.
type Run struct {
	ID         string         `json:"id"`
	Input      Input          `json:"-"`
	StartedAt  time.Time      `json:"startedAt"`
	FinishedAt time.Time      `json:"finishedAt,omitempty"`
	Identity   string         `json:"identityKey"`
	ATS        string         `json:"ats"`
	Tenant     string         `json:"tenant"`
	Report     *ReportBinding `json:"report,omitempty"`
	Stages     []StageLine    `json:"stages"`
	Pages      []PageReport   `json:"pages"`
	Outcome    Outcome        `json:"outcome"`
	Captcha    []string       `json:"captcha,omitempty"`
	Signins    []SigninRecord `json:"signins,omitempty"`
	Build      string         `json:"build,omitempty"`
	// Claim is the press claim's id once the submit stage took it.
	Claim string `json:"claim,omitempty"`
	// Correction is true once the correction door opened for the run;
	// Supersedes is the ledger row it re-sends.
	Correction bool  `json:"correction,omitempty"`
	Supersedes int64 `json:"supersedes,omitempty"`
	// Pressed is true once a Submit press was dispatched; MayHaveSubmitted when
	// the press went out and its verdict could not be read.
	Pressed          bool `json:"pressed,omitempty"`
	MayHaveSubmitted bool `json:"mayHaveSubmitted,omitempty"`
	// ModelCalls counts the calls spent on the ladder and the walker's page
	// decisions, of at most MaxModelCalls.
	ModelCalls int `json:"modelCalls,omitempty"`
	// Repeats are the questions the grounded rung declined for a second
	// distinct run inside 30 days: the user is asked once.
	Repeats []string `json:"repeatDeclines,omitempty"`
	// Presses counts the Submit presses of the run on its one claim.
	Presses int `json:"presses,omitempty"`
	// Repaired records the one repair round after a refused Submit.
	Repaired string `json:"repaired,omitempty"`
	// LearningEvents counts the events the learn stage handled.
	LearningEvents int `json:"learningEvents,omitempty"`
	// MessageHash is the salted hash of the user's message; the text itself is
	// never stored.
	MessageHash string `json:"-"`
	// LearningLane is where the stopped run's lesson went; Snapshot is the stop
	// snapshot's path when one was written (the work item of a defect names it).
	LearningLane Lane   `json:"learningLane,omitempty"`
	Snapshot     string `json:"snapshot,omitempty"`
	// Warnings are failures of bookkeeping that never change the outcome.
	Warnings []string `json:"warnings,omitempty"`
	// TrackerRow is the tracker row the record stage set to applied; 0 when it
	// set none.
	TrackerRow int64 `json:"trackerRow,omitempty"`
	// PostingFacts are statements about the posting itself (closed, already
	// applied, email only) that hold the row off lists.
	PostingFacts []string `json:"postingFacts,omitempty"`
	// LeftForAnotherHost names the host the application moved to when the
	// Apply control led off the posting's site; empty when it stayed.
	LeftForAnotherHost string `json:"leftForAnotherHost,omitempty"`
	// Stated is what the first page the browser stages opened says of its
	// posting, read once. It is text from a page, so its names only name the
	// run on the board, after the request, the report and the workspace, and
	// its place locates the posting only where no board does (postingLoc); it
	// is never written to the run's record, which a retry reads back as the
	// request.
	Stated Stated `json:"-"`
}

// SigninRecord is the per-host sign-in record: candidate labels and what
// happened, never an email or a password.
type SigninRecord struct {
	Host       string   `json:"host"`
	Tried      []string `json:"tried,omitempty"`
	Refusals   int      `json:"refusals,omitempty"`
	Succeeded  string   `json:"succeeded,omitempty"`
	Held       bool     `json:"held,omitempty"`
	Exhausted  bool     `json:"exhausted,omitempty"`
	Lockout    bool     `json:"lockout,omitempty"`
	ResetState string   `json:"reset,omitempty"`
	Session    string   `json:"session,omitempty"`
	Google     string   `json:"google,omitempty"`
	// Signup is the account creation gate's refusal code when the host needed
	// a new account and the gate said no.
	Signup string `json:"signup,omitempty"`
	// Mail is MailNotConnected when a code or link mail for the host could not
	// be read because Gmail is not connected.
	Mail string `json:"mail,omitempty"`
}

// MailNotConnected is SigninRecord.Mail for a host whose mail could not be
// read because Gmail is not connected or refuses its app password.
const MailNotConnected = "not-connected"

// ReportBinding is the evaluated report a run is bound to. Company and role
// come from the tracker row, else the report title, never from the URL host.
type ReportBinding struct {
	Number  int64  `json:"number"`
	Path    string `json:"path"`
	Company string `json:"company"`
	Role    string `json:"role"`
	// TrackerID is the live tracker row of the report, 0 when none.
	TrackerID int64 `json:"trackerId,omitempty"`
	// Kind is the report's kind: final (a full evaluation), screening (a quick
	// screen with no role snapshot) or gated.
	Kind string `json:"kind,omitempty"`
}

// stop is a stage's reason to end the run.
type stop struct {
	class  Class
	reason string
	// at is the stage that stopped; the runner fills it in.
	at Stage
	// board is the runs-board vocabulary for a stop that needs the user or is
	// blocked, set by the stage that knows it.
	board string
	// moved marks a fill that ended because the tab moved to another origin:
	// the walk reads the new page rather than stopping.
	moved bool
}

func (s *stop) Error() string { return s.reason }
