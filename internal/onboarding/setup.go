package onboarding

import (
	"context"
	"time"

	"applysmith/internal/api/wire"
)

// RejectedError is input the command refuses (exit code 2).
type RejectedError struct{ Message string }

func (e *RejectedError) Error() string { return e.Message }

// StaleError says the input was built from an older revision of a file than the
// one on disk. Nothing was written.
type StaleError struct{ Message string }

func (e *StaleError) Error() string { return e.Message }

const staleProfile = "profile.yml changed since the preview was built; preview the documents again"

// State evaluates the whole catalog: every lane reads it, the wizard through
// the server and `applysmith setup status`. It recomputes everything from the
// stored data and writes nothing.
func State(ctx context.Context, env *Env) wire.SetupState {
	statuses := EvaluateAll(ctx, env)
	out := wire.SetupState{Progress: Progress(statuses), Steps: []wire.SetupStep{}, Secrets: []wire.SetupSecret{}}
	if next, ok := FirstIncomplete(statuses); ok {
		out.Next = next.ID
	}
	for _, st := range Catalog() {
		out.Steps = append(out.Steps, wireStep(st, statuses[st.ID], env.InServer))
	}
	for _, sec := range LLMSecrets(ctx, env) {
		out.Secrets = append(out.Secrets, wire.SetupSecret{Name: sec.Name, Present: sec.Present, SavedAt: sec.SavedAt})
	}
	return out
}

// NextReport is `applysmith setup next`: the first incomplete required step, its
// fields the input schema of its writer.
type NextReport struct {
	Done bool            `json:"done"`
	Step *wire.SetupStep `json:"step,omitempty"`
}

// Next names the first incomplete required step.
func Next(ctx context.Context, env *Env) NextReport {
	statuses := EvaluateAll(ctx, env)
	s, ok := FirstIncomplete(statuses)
	if !ok {
		return NextReport{Done: true}
	}
	v := wireStep(s, statuses[s.ID], env.InServer)
	return NextReport{Step: &v}
}

func lookup(id string) (Step, error) {
	s, ok := StepByID(id)
	if !ok {
		return Step{}, &RejectedError{Message: "unknown step " + id}
	}
	return s, nil
}

// Schema returns the input fields of a step's writer.
func Schema(id string) ([]Field, error) {
	s, err := lookup(id)
	if err != nil {
		return nil, err
	}
	return s.Schema, nil
}

// Apply runs a step's writer.
func Apply(ctx context.Context, env *Env, id string, input []byte, dry bool) (Applied, error) {
	s, err := lookup(id)
	if err != nil {
		return Applied{}, err
	}
	return s.Apply(ctx, env, input, dry)
}

// Ack records that the example values a step still holds are the user's own
// (persona matches) or that a reviewed list is confirmed. Seed text,
// placeholders, reserved-domain emails and fictional phone numbers are refused.
// The acknowledgement is bound to the data and stops applying when it changes.
func Ack(ctx context.Context, env *Env, id string) error {
	s, err := lookup(id)
	if err != nil {
		return err
	}
	if s.AckScope == nil {
		return &RejectedError{Message: "step " + id + " has nothing to acknowledge"}
	}
	d := s.Detect(ctx, env)
	for _, r := range d.Reasons {
		if r.Kind.needsValue() {
			return &RejectedError{Message: "cannot acknowledge: " + r.Text}
		}
	}
	if !d.has(ReasonPersona) && !d.has(ReasonReview) {
		return &RejectedError{Message: "step " + id + " has nothing to acknowledge"}
	}
	scope, err := s.AckScope(ctx, env)
	if err != nil {
		return &EnvError{Message: "the data to acknowledge cannot be read", Err: err}
	}
	if d.has(ReasonPersona) {
		if err := env.setDecision(ctx, ackKey(id), scope); err != nil {
			return &EnvError{Message: "the acknowledgement could not be stored", Err: err}
		}
	}
	if d.has(ReasonReview) {
		if err := env.setDecision(ctx, reviewKey(id), scope); err != nil {
			return &EnvError{Message: "the confirmation could not be stored", Err: err}
		}
	}
	return nil
}

// Skip records the decision to skip an optional step, or the optional voice
// part of the story step.
func Skip(ctx context.Context, env *Env, id string) error {
	if err := skippable(id, "cannot be skipped"); err != nil {
		return err
	}
	if err := env.setDecision(ctx, skipKey(id), env.now().UTC().Format(time.RFC3339)); err != nil {
		return &EnvError{Message: "the skip could not be stored", Err: err}
	}
	return nil
}

// Unskip removes a skip decision.
func Unskip(ctx context.Context, env *Env, id string) error {
	if err := skippable(id, "was never skipped"); err != nil {
		return err
	}
	return env.clearDecision(ctx, skipKey(id))
}

func skippable(id, refusal string) error {
	if id == voiceSkipID {
		return nil
	}
	s, err := lookup(id)
	if err != nil {
		return err
	}
	if s.Required {
		return &RejectedError{Message: "step " + id + " is required and " + refusal}
	}
	return nil
}
