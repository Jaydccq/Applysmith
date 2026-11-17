package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrRestart is returned when the workspace changed and the run must start
// again in the new one.
var ErrRestart = errors.New("the workspace changed")

// StoppedError says a step could not be completed and the run stopped there.
type StoppedError struct {
	Step    string
	Reasons []string
}

func (e *StoppedError) Error() string {
	return "step " + e.Step + " is not complete: " + strings.Join(e.Reasons, "; ")
}

// Flow is the interactive `applysmith setup`: it walks the required steps in
// order from the first incomplete one and calls the same writers as every
// other lane.
type Flow struct {
	Env *Env
	In  Prompter
	Out io.Writer
	// Reopen refreshes Env after a step that created the database.
	Reopen func(ctx context.Context) error
}

const maxAttempts = 3

// Run walks the required steps that are not complete, offers the optional
// ones, and ends with the start phase, so that orientation can list what was
// skipped.
func (f *Flow) Run(ctx context.Context) error {
	if err := f.requiredSteps(ctx, false); err != nil {
		return err
	}
	if err := f.offerOptional(ctx); err != nil {
		return err
	}
	if err := f.requiredSteps(ctx, true); err != nil {
		return err
	}
	fmt.Fprintf(f.Out, "\nProgress: %d%%.\n", Progress(EvaluateAll(ctx, f.Env)))
	return nil
}

func (f *Flow) requiredSteps(ctx context.Context, start bool) error {
	steps := Catalog()
	for _, s := range steps {
		if !s.Required || (s.Phase == PhaseStart) != start {
			continue
		}
		if Evaluate(ctx, f.Env, s).Status == StepComplete {
			continue
		}
		fmt.Fprintf(f.Out, "\nStep %d of %d: %s\n%s\n", s.N, len(steps)-1, s.Title, s.Purpose)
		if err := f.runStep(ctx, s); err != nil {
			return err
		}
		if s.ID == "workspace" && f.Reopen != nil {
			if err := f.Reopen(ctx); err != nil {
				return err
			}
		}
		if d := Evaluate(ctx, f.Env, s); d.Status != StepComplete {
			return &StoppedError{Step: s.ID, Reasons: d.Texts()}
		}
		fmt.Fprintf(f.Out, "Step %d is complete.\n", s.N)
	}
	return nil
}

// offerOptional presents each optional step. Nothing is created unless the
// user answers: an empty script, a blank answer or "skip" leaves the step as it
// was or records the skip. A step that needs a person outside the terminal
// prints what to do and the commands; it is then left for the user.
func (f *Flow) offerOptional(ctx context.Context) error {
	for _, s := range Catalog() {
		if s.Required {
			continue
		}
		if err := f.offerStep(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func (f *Flow) offerStep(ctx context.Context, s Step) error {
	if st := Evaluate(ctx, f.Env, s).Status; st == StepComplete || st == StepSkipped {
		return nil
	}
	fmt.Fprintf(f.Out, "\nOptional step %d: %s\n%s\n", s.N, s.Title, s.Purpose)
	if s.Human != "" {
		return f.offerHuman(ctx, s)
	}
	in, err := f.questions(ctx, s)
	if err != nil {
		if errors.Is(err, ErrScriptExhausted) {
			fmt.Fprintf(f.Out, "  left for later: %s\n", s.Commands[0])
			return nil
		}
		return err
	}
	var answer struct {
		Pack string `json:"pack"`
	}
	if json.Unmarshal(in, &answer) == nil && answer.Pack == "skip" {
		if err := Skip(ctx, f.Env, s.ID); err != nil {
			return err
		}
		fmt.Fprintf(f.Out, "  skipped; %s\n", s.SkipCost)
		return nil
	}
	res, err := s.Apply(ctx, f.Env, in, false)
	var fe FieldErrors
	if errors.As(err, &fe) {
		for field, msg := range fe {
			fmt.Fprintf(f.Out, "  %s: %s\n", field, msg)
		}
		fmt.Fprintf(f.Out, "  left for later: %s\n", s.Commands[0])
		return nil
	}
	if err != nil {
		return err
	}
	for _, n := range res.Notes {
		fmt.Fprintf(f.Out, "  %s\n", n)
	}
	return nil
}

// offerHuman is the optional step only a person can finish: the user chooses
// to do it now (the instructions and commands are printed and the step is left
// open) or to skip it, which is the default.
func (f *Flow) offerHuman(ctx context.Context, s Step) error {
	a, err := f.ask("Set this up now? (yes or skip)", "skip")
	if err != nil {
		if errors.Is(err, ErrScriptExhausted) {
			fmt.Fprintf(f.Out, "  left for later: %s\n", s.Commands[0])
			return nil
		}
		return err
	}
	if strings.EqualFold(a, "yes") || strings.EqualFold(a, "y") {
		fmt.Fprintf(f.Out, "  %s\n", s.Human)
		for _, c := range s.Commands {
			fmt.Fprintf(f.Out, "  run: %s\n", c)
		}
		return nil
	}
	if err := Skip(ctx, f.Env, s.ID); err != nil {
		return err
	}
	fmt.Fprintf(f.Out, "  skipped; %s\n", s.SkipCost)
	return nil
}

// runStep asks, applies and, when the questions or the writer reject an
// answer, asks again.
func (f *Flow) runStep(ctx context.Context, s Step) error {
	var last error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		var res Applied
		input, err := f.questions(ctx, s)
		if err == nil {
			res, err = s.Apply(ctx, f.Env, input, false)
		}
		var fe FieldErrors
		if errors.As(err, &fe) {
			for field, msg := range fe {
				fmt.Fprintf(f.Out, "  %s: %s\n", field, msg)
			}
			last = err
			continue
		}
		if err != nil {
			return err
		}
		for _, n := range res.Notes {
			fmt.Fprintf(f.Out, "  %s\n", n)
		}
		if res.Restart {
			return ErrRestart
		}
		return f.afterApply(ctx, s, res)
	}
	return last
}

// afterApply handles what follows a write: example values the user may own,
// and the extracted CV draft.
func (f *Flow) afterApply(ctx context.Context, s Step, res Applied) error {
	if s.ID == "orientation" {
		return f.orient(ctx)
	}
	d := Evaluate(ctx, f.Env, s)
	for _, r := range d.Reasons {
		fmt.Fprintf(f.Out, "  still open: %s\n", r.Text)
	}
	if d.Status == StepTemplate && s.AckScope != nil {
		ok := true
		for _, r := range d.Reasons {
			if r.Kind != ReasonPersona {
				ok = false
			}
		}
		if ok {
			yes, err := f.yesNo("These match the shipped example values. Are they really yours?")
			if err != nil {
				return err
			}
			if yes {
				return Ack(ctx, f.Env, s.ID)
			}
		}
	}
	return nil
}

func (f *Flow) ask(label, def string) (string, error) { return f.In.Ask(label, def) }

// yesNo requires an explicit yes or no; there is no default.
func (f *Flow) yesNo(label string) (bool, error) {
	for i := 0; i < maxAttempts; i++ {
		a, err := f.In.Ask(label+" (yes/no)", "")
		if err != nil {
			return false, err
		}
		switch strings.ToLower(a) {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		fmt.Fprintln(f.Out, "  answer yes or no")
	}
	return false, &RejectedError{Message: "no clear yes or no was given for: " + label}
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
