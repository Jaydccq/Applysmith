// Clean-room: docs/provenance.md#CR-AGENT-TRIGGERS

// Package triggers keeps the scheduled prompts of the Ask Agent, each on a
// cadence or on a Gmail signal, and fires them. A firing is a conversation in
// the scheduled envelope: read and draft tools only, and for a trigger the
// turn starts tainted because its input is an email. The mail text is filtered
// before the model sees it, and what a firing leaves is a draft or a proposal
// that does nothing until the user acts. This package imports no apply, form
// filling or submit code; the model turn is reached through the Runner
// interface.
package triggers

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"applysmith/internal/agent/goals"
	"applysmith/internal/lazyregexp"
	"applysmith/internal/store"
)

// Limits from the specification.
const (
	MaxPrompts     = 10
	MaxPromptChars = 2000
	maxCompany     = 120
)

// Kinds of a scheduled prompt.
type Kind string

const (
	KindCadence Kind = "cadence"
	KindTrigger Kind = "trigger"
)

// States of a scheduled prompt. An agent-made prompt is proposed until the
// user confirms it.
type State string

const (
	StateProposed State = "proposed"
	StateActive   State = "active"
	StatePaused   State = "paused"
)

// Origin says who created a prompt.
type Origin string

const (
	OriginUser  Origin = "user"
	OriginAgent Origin = "agent"
)

// Signal types a trigger may name.
const (
	TypeInterview  = "interview_scheduled"
	TypeReply      = "recruiter_reply_needed"
	TypeRejection  = "rejection"
	TypeAssessment = "online_assessment"
)

// SignalTypes are the signals a trigger can fire on, in display order.
var SignalTypes = []string{TypeInterview, TypeReply, TypeRejection, TypeAssessment}

// Errors the tools and routes map onto their answers.
var (
	ErrNotFound = store.ErrNotFound
	// ErrInvalid means a field is over its limit or not one of its values.
	ErrInvalid = errors.New("prompts: invalid")
	// ErrUnsupported means the prompt asks the agent to act, which a
	// scheduled turn cannot do.
	ErrUnsupported = errors.New("prompts: a scheduled turn can only read and draft, so a prompt that asks to apply, submit, register or write data is refused")
	// ErrLimit means ten prompts exist.
	ErrLimit = errors.New("prompts: 10 scheduled prompts exist; delete one first")
	// ErrConflict means the prompt is not in a state that allows the call.
	ErrConflict = errors.New("prompts: the prompt is not in a state that allows this")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Trigger is what a trigger prompt fires on: any of Types, optionally only
// for mail from one employer.
type Trigger struct {
	Types   []string
	Company string
}

// Last is the result of a prompt's latest run.
type Last struct {
	At        time.Time
	Status    string // answered, failed or timed_out
	SessionID string
	Summary   string
}

// Prompt is one scheduled prompt.
type Prompt struct {
	ID      string
	Kind    Kind
	Text    string
	State   State
	Origin  Origin
	Cadence goals.Cadence
	Trigger Trigger
	// ConfirmedAt is when the user activated it; ActiveSince is the later of
	// that, the last resume and the last change of its schedule. A slot or a
	// message before ActiveSince never runs.
	ConfirmedAt time.Time
	ActiveSince time.Time
	LastSlot    time.Time
	Last        *Last
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// NewPrompt is a prompt to create. Exactly one of Cadence and Trigger is set.
type NewPrompt struct {
	Text    string
	Cadence goals.Cadence
	Trigger Trigger
	Origin  Origin
}

// Patch is a change to a prompt; nil fields stay. The kind cannot change.
type Patch struct {
	Text    *string
	Cadence *goals.Cadence
	Trigger *Trigger
}

// actionVerbs are the words that make a prompt ask for work a scheduled turn
// cannot do: starting an application, registering, or changing stored data.
var actionVerbs = lazyregexp.New(`(?i)\b(?:apply|applying|submit|submitting|register|registering|autofill|fill|sign\s*up|write|save|store|record|update|edit|delete|remove|create|forget|remember|tailor)\b`)

func clean(text *string) error {
	*text = strings.TrimSpace(*text)
	switch {
	case *text == "":
		return invalid("a prompt needs some text")
	case utf8.RuneCountInString(*text) > MaxPromptChars:
		return invalid("a prompt is at most %d characters", MaxPromptChars)
	case actionVerbs.MatchString(*text):
		return ErrUnsupported
	}
	return nil
}

func (t *Trigger) clean() error {
	t.Company = strings.TrimSpace(t.Company)
	if utf8.RuneCountInString(t.Company) > maxCompany {
		return invalid("a company is at most %d characters", maxCompany)
	}
	if len(t.Types) == 0 {
		return invalid("a trigger needs at least one signal type: %s", strings.Join(SignalTypes, ", "))
	}
	var out []string
	for _, ty := range t.Types {
		ty = strings.TrimSpace(ty)
		if !slices.Contains(SignalTypes, ty) {
			return invalid("%q is not a trigger signal; use %s", ty, strings.Join(SignalTypes, ", "))
		}
		if !slices.Contains(out, ty) {
			out = append(out, ty)
		}
	}
	slices.SortFunc(out, func(a, b string) int { return slices.Index(SignalTypes, a) - slices.Index(SignalTypes, b) })
	t.Types = out
	return nil
}

// Matches reports whether the trigger fires for a signal: an incoming
// actionable one of a named type whose employer fits the company, when one is
// named, and that was received after the prompt became active.
func (p Prompt) Matches(e Event) bool {
	return p.Kind == KindTrigger && p.State == StateActive &&
		e.Tier == "actionable" && e.Direction == "incoming" &&
		slices.Contains(p.Trigger.Types, e.SignalType) &&
		!e.Received.Before(p.ActiveSince) &&
		companyMatches(p.Trigger.Company, e.Employer)
}

var nonWord = lazyregexp.New(`[^\p{L}\p{N}]+`)

func norm(s string) string {
	return strings.TrimSpace(nonWord.ReplaceAllString(strings.ToLower(s), " "))
}

// companyMatches is true for an empty company and otherwise when the employer
// holds the company's words as whole words.
func companyMatches(company, employer string) bool {
	c := norm(company)
	if c == "" {
		return true
	}
	return strings.Contains(" "+norm(employer)+" ", " "+c+" ")
}
