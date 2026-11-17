package scheduler

import (
	"errors"
	"fmt"
	"time"
)

// Missed-fire policies.
const (
	PolicySkip     = "skip"
	PolicyFireOnce = "fire-once"
	PolicyFireAll  = "fire-all"
)

// Last results of a schedule.
const (
	ResultRunning = "running"
	ResultOK      = "ok"
	ResultFailed  = "failed"
	ResultSkipped = "skipped"
)

// Skip reasons. The set is closed.
const (
	ReasonBusy        = "busy"
	ReasonDisabled    = "disabled"
	ReasonPolicy      = "policy"
	ReasonHostAsleep  = "host-asleep"
	ReasonHostStarved = "host-starved"
	ReasonOffline     = "offline"
	ReasonLease       = "lease"
	ReasonUnknownKind = "unknown-kind"
	ReasonValidation  = "validation"
	ReasonError       = "error"
)

// Reasons that are not skips: how a fired job ended without succeeding.
const (
	ReasonInterrupted = "interrupted"
	ReasonCancelled   = "cancelled"
	ReasonNeedsSignIn = "needs-sign-in"
	ReasonNeedsYou    = "needs-you"
)

// History outcomes. Fired, manual, skipped and interrupted are decisions;
// deferred and ended only explain one.
const (
	OutcomeFired       = "fired"
	OutcomeManual      = "manual"
	OutcomeSkipped     = "skipped"
	OutcomeInterrupted = "interrupted"
	OutcomeDeferred    = "deferred"
	OutcomeEnded       = "ended"
)

// SkipReasons is the closed set of skip reason codes.
var SkipReasons = []string{ReasonBusy, ReasonDisabled, ReasonPolicy, ReasonHostAsleep, ReasonHostStarved,
	ReasonOffline, ReasonLease, ReasonUnknownKind, ReasonValidation, ReasonError}

// Limits of the store.
const (
	MaxSchedules = 200
	MaxNameLen   = 80
)

// Errors of the schedule store and of management calls.
var (
	ErrNotFound      = errors.New("scheduler: no such schedule")
	ErrNameConflict  = errors.New("scheduler: a schedule with this name exists")
	ErrCapacity      = fmt.Errorf("scheduler: at most %d schedules may exist", MaxSchedules)
	ErrRunning       = errors.New("scheduler: the schedule has a running job")
	ErrDecided       = errors.New("scheduler: the occurrence was already decided")
	ErrNotRunning    = errors.New("scheduler: the scheduler is not running")
	ErrUnknownPack   = errors.New("scheduler: no such preset pack")
	errStaleSchedule = errors.New("scheduler: the schedule changed after it was read")
)

// InvalidError is a validation failure of a schedule definition. Field names
// the part that failed.
type InvalidError struct {
	Field   string
	Message string
}

func (e *InvalidError) Error() string { return e.Message }

func invalid(field, format string, args ...any) *InvalidError {
	return &InvalidError{Field: field, Message: fmt.Sprintf(format, args...)}
}

// RefusedError is a manual run that failed an admission check.
type RefusedError struct {
	Reason string
	Detail string
}

func (e *RefusedError) Error() string { return "the run was refused (" + e.Reason + "): " + e.Detail }

// Schedule is one stored schedule.
type Schedule struct {
	ID              int64
	Name            string
	Kind            string
	Inputs          map[string]any
	Preset          Preset
	Enabled         bool
	Policy          string
	DTStart         time.Time
	PackID, PackKey string
	LastFiredAt     time.Time // zero until the first decision
	LastEvaluatedAt time.Time
	ActiveRunID     string
	LastResult      string
	LastSkipReason  string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	LastRunID       string // the run of the newest occurrence that started one
	// LastResultAt is when LastResult happened: the newest decision's run
	// start, or when a skip was decided. LastFiredAt is the cursor, which an
	// edit, a resume or a Run now does not leave at the last run.
	LastResultAt time.Time
	// LastReason and LastDetail say why that decision ended as it did (the
	// history row's reason and detail): empty for a run that succeeded.
	LastReason, LastDetail string
}

// HistoryRow is one row of a schedule's history.
type HistoryRow struct {
	ID           int64
	ScheduleID   int64
	ScheduledFor time.Time
	Outcome      string
	Reason       string
	Detail       string
	RunID        string
	StartedAt    time.Time
	FinishedAt   time.Time
	Attempts     int
	Summary      string
	CreatedAt    time.Time
}

const tsLayout = "2006-01-02T15:04:05.000Z"

func ts(t time.Time) string { return t.UTC().Format(tsLayout) }

func parseTS(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
