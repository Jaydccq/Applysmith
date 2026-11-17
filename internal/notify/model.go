package notify

import (
	"errors"
	"slices"
	"time"
)

// Kind is the kind of work a board entry stands for.
type Kind string

// Entry kinds.
const (
	KindApply     Kind = "apply"
	KindScan      Kind = "scan"
	KindEvaluate  Kind = "evaluate"
	KindDocuments Kind = "documents"
	KindAgent     Kind = "agent"
	KindTask      Kind = "task"
)

// Origin says who started the work.
type Origin string

// Entry origins.
const (
	OriginUser     Origin = "user"
	OriginSchedule Origin = "schedule"
	OriginAgent    Origin = "agent"
)

// State is where an entry stands.
type State string

// Entry states.
const (
	Working  State = "working"
	NeedsYou State = "needs-you"
	Blocked  State = "blocked"
	Done     State = "done"
	Idle     State = "idle"
)

// Outcomes of a done entry.
const (
	OutcomeOK          = "ok"
	OutcomeFailed      = "failed"
	OutcomeCancelled   = "cancelled"
	OutcomeInterrupted = "interrupted"
)

// Reasons an entry waits for the user.
var needsYouReasons = []string{"sign-in", "answer", "challenge", "review", "one-time-code", "confirmation", "may-have-submitted", "other"}

// Reasons an entry is stopped by a missing precondition.
var blockedReasons = []string{"credential-refused", "lockout", "lane-unavailable", "key-missing", "cap-reached", "no-connection", "other"}

// Limits of the text of an entry.
const (
	MaxTitle  = 120
	MaxDetail = 300
)

// Entry is one row of the runs board.
type Entry struct {
	ID       string
	Kind     Kind
	Origin   Origin
	Title    string
	Stage    string
	Detail   string
	State    State
	Reason   string // for needs-you and blocked
	Outcome  string // for done
	BatchID  string
	Link     string // dashboard route of the entry
	Ref      string // the owning subsystem's run record
	FocusTab string // opaque handle of a browser tab kept for the user
	Revision int64
	Epoch    int64

	StartedAt  time.Time
	UpdatedAt  time.Time
	StateAt    time.Time
	FinishedAt time.Time
	SeenAt     time.Time
	AckedAt    time.Time

	// RefUpdated is the update stamp of the run record this entry mirrors.
	RefUpdated string
}

// Acked reports whether the user acknowledged the entry.
func (e Entry) Acked() bool { return !e.AckedAt.IsZero() }

// Waiting reports whether the entry is in the inbox states.
func (e Entry) Waiting() bool { return e.State == NeedsYou || e.State == Blocked }

// Errors of the board.
var (
	ErrNotFound          = errors.New("notify: no such board entry")
	ErrIllegalTransition = errors.New("notify: that transition is not permitted")
	ErrBadReason         = errors.New("notify: the reason does not fit the state")
	ErrNotAckable        = errors.New("notify: only a finished or waiting entry can be acknowledged")
	ErrStateChanged      = errors.New("notify: the entry is no longer in the state the move expected")
)

// transitions lists the permitted moves between states.
var transitions = map[State][]State{
	Working:  {NeedsYou, Blocked, Done, Idle},
	NeedsYou: {Working, Blocked, Done},
	Blocked:  {Working, Done},
	Done:     {Working},
	Idle:     {Working},
}

// CanMove reports whether an entry may move from one state to another.
func CanMove(from, to State) bool { return slices.Contains(transitions[from], to) }

func validReason(s State, reason string) bool {
	switch s {
	case NeedsYou:
		return slices.Contains(needsYouReasons, reason)
	case Blocked:
		return slices.Contains(blockedReasons, reason)
	}
	return reason == ""
}

func validOutcome(o string) bool {
	return slices.Contains([]string{OutcomeOK, OutcomeFailed, OutcomeCancelled, OutcomeInterrupted}, o)
}
