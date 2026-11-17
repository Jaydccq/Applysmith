// Clean-room: docs/provenance.md#CR-AGENT-GOALS

// Package goals keeps the job-search goals of the Ask Agent: a small plan
// store (steps, a due date, a check-in cadence) that the model reads and the
// user edits. A goal is data. No gate reads it, a step that names an
// application or a scan is only text for the user, and a plan change the
// agent wants is a proposal that takes effect only when the user accepts it.
package goals

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"applysmith/internal/agent/memory"
	"applysmith/internal/store"
)

// Limits from the specification.
const (
	MaxTitleChars  = 120
	MaxSteps       = 20
	MaxStepChars   = 200
	MaxNoteChars   = 300
	MaxReasonChars = 300
	MaxActive      = 5
	maxRefChars    = 120
)

// States of a goal.
const (
	Active    = "active"
	Paused    = "paused"
	Completed = "completed"
	Archived  = "archived"
)

// Statuses of a step.
const (
	Pending    = "pending"
	InProgress = "in_progress"
	Done       = "done"
	Blocked    = "blocked"
)

// Errors the tools and the routes map onto their answers.
var (
	// ErrNotFound means no such goal, step or proposal.
	ErrNotFound = store.ErrNotFound
	// ErrInvalid means a field is over its limit or not one of its values.
	ErrInvalid = errors.New("goals: invalid")
	// ErrRefused means the text reads as an instruction to the assistant.
	ErrRefused = errors.New("goals: the text reads as an instruction to the assistant, so it was refused")
	// ErrTooManyActive means five goals are already active.
	ErrTooManyActive = errors.New("goals: 5 goals are already active; pause or complete one first")
	// ErrConflict means the goal or the proposal is not in a state that allows the call.
	ErrConflict = errors.New("goals: the goal is not in a state that allows this")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Step is one ordered step of a goal. ApplicationID and Company link it to an
// application or a company and mean nothing to any gate.
type Step struct {
	Text          string `json:"text"`
	Status        string `json:"status"`
	Note          string `json:"note,omitempty"`
	ApplicationID string `json:"applicationId,omitempty"`
	Company       string `json:"company,omitempty"`
}

// Cadence is the local weekdays (sun..sat) and the local time (HH:MM) of a
// goal's check-ins. The zero value is no cadence.
type Cadence struct {
	Days []string `json:"days,omitempty"`
	Time string   `json:"time,omitempty"`
}

// Empty reports whether there is no cadence.
func (c Cadence) Empty() bool { return len(c.Days) == 0 && c.Time == "" }

// Clean validates the cadence and returns it with its days lower-cased, unique
// and in week order, for other schedules of the Ask Agent that share its form.
func (c Cadence) Clean() (Cadence, error) { return c.clean() }

var weekdays = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// clean returns the cadence with its days lower-cased, unique and in week order.
func (c Cadence) clean() (Cadence, error) {
	if c.Empty() {
		return Cadence{}, nil
	}
	if len(c.Days) == 0 || c.Time == "" {
		return Cadence{}, invalid("a check-in cadence needs weekdays and a time")
	}
	h, m, ok := strings.Cut(c.Time, ":")
	if _, err := time.Parse("15:04", c.Time); err != nil || !ok || len(h) != 2 || len(m) != 2 {
		return Cadence{}, invalid("the check-in time %q must be HH:MM", c.Time)
	}
	var out Cadence
	out.Time = c.Time
	for _, d := range c.Days {
		d = strings.ToLower(strings.TrimSpace(d))
		if !slices.Contains(weekdays, d) {
			return Cadence{}, invalid("%q is not a weekday; use %s", d, strings.Join(weekdays, ", "))
		}
		if !slices.Contains(out.Days, d) {
			out.Days = append(out.Days, d)
		}
	}
	slices.SortFunc(out.Days, func(a, b string) int { return slices.Index(weekdays, a) - slices.Index(weekdays, b) })
	return out, nil
}

// Proposal is a plan change waiting for the user. Nothing in the goal changes
// until Accept.
type Proposal struct {
	ID        string
	GoalID    string
	Reason    string
	Change    Change
	CreatedAt time.Time
}

// Change is what a proposal would apply. Steps are the revised steps that
// replace every step that is not done; DueDate and Cadence replace the goal's
// when set (an empty DueDate or an empty Cadence clears it).
type Change struct {
	Steps   []Step   `json:"steps"`
	DueDate *string  `json:"dueDate,omitempty"`
	Cadence *Cadence `json:"cadence,omitempty"`
}

// Goal is one job-search goal.
type Goal struct {
	ID          string
	Title       string
	DueDate     string
	State       string
	Version     int
	Cadence     Cadence
	Steps       []Step
	Proposal    *Proposal
	ActiveSince time.Time
	LastSlot    time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Progress counts the steps that are done and all steps.
func (g Goal) Progress() (done, total int) {
	for _, s := range g.Steps {
		if s.Status == Done {
			done++
		}
	}
	return done, len(g.Steps)
}

// Next is the first step that is not done and its 1-based position, or 0.
func (g Goal) Next() (Step, int) {
	for i, s := range g.Steps {
		if s.Status != Done {
			return s, i + 1
		}
	}
	return Step{}, 0
}

// Overdue reports whether the due date is before the calendar day of now in loc.
func (g Goal) Overdue(now time.Time, loc *time.Location) bool {
	if g.DueDate == "" || g.State != Active {
		return false
	}
	return g.DueDate < now.In(loc).Format(time.DateOnly)
}

func chars(s string) int { return utf8.RuneCountInString(s) }

func trim(s *string) { *s = strings.TrimSpace(*s) }

// checkText applies the limit and the memory injection blocklist to one text.
func checkText(what, s string, limit int, required bool) error {
	switch {
	case required && s == "":
		return invalid("%s needs some text", what)
	case chars(s) > limit:
		return invalid("%s is at most %d characters", what, limit)
	case memory.InjectionShaped(s):
		return ErrRefused
	}
	return nil
}

func checkDue(d *string) error {
	trim(d)
	if *d == "" {
		return nil
	}
	if _, err := time.Parse(time.DateOnly, *d); err != nil {
		return invalid("the due date %q must be YYYY-MM-DD", *d)
	}
	return nil
}

func checkStep(s *Step, allowDone bool) error {
	trim(&s.Text)
	trim(&s.Note)
	trim(&s.ApplicationID)
	trim(&s.Company)
	if s.Status == "" {
		s.Status = Pending
	}
	if !slices.Contains([]string{Pending, InProgress, Done, Blocked}, s.Status) || (s.Status == Done && !allowDone) {
		return invalid("%q is not a step status here", s.Status)
	}
	for _, f := range []struct {
		what, v string
		limit   int
		req     bool
	}{{"a step", s.Text, MaxStepChars, true}, {"a step note", s.Note, MaxNoteChars, false},
		{"an application id", s.ApplicationID, maxRefChars, false}, {"a company", s.Company, maxRefChars, false}} {
		if err := checkText(f.what, f.v, f.limit, f.req); err != nil {
			return err
		}
	}
	return nil
}

func checkSteps(steps []Step, allowDone bool) error {
	if len(steps) > MaxSteps {
		return invalid("a goal has at most %d steps", MaxSteps)
	}
	for i := range steps {
		if err := checkStep(&steps[i], allowDone); err != nil {
			return err
		}
	}
	return nil
}

func checkTitle(t *string) error {
	trim(t)
	return checkText("a title", *t, MaxTitleChars, true)
}
