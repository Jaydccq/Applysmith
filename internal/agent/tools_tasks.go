// Clean-room: docs/provenance.md#CR-AGENT-TASKS

package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"applysmith/internal/agent/tasks"
)

// The delegated-task tools. delegate_task starts a task in the conversation,
// with the user's own message as its mandate; the other three are the task's
// bookkeeping, which changes only the task's own record, so they are drafts.
const (
	ToolDelegateTask = "delegate_task"
	ToolTaskUpdate   = "task_update"
	ToolTaskAsk      = "task_ask"
	ToolTaskDone     = "task_done"
)

const tasksTimeout = 10 * time.Second

// TaskTools builds the four task tools; a nil store leaves them out.
func TaskTools(s *tasks.Store) []Tool {
	if s == nil {
		return nil
	}
	return []Tool{delegateTask(s), taskUpdate(s), taskAsk(s), taskDone(s)}
}

// taskRefusal answers a store error the model can act on as text.
func taskRefusal(err error) (ToolResult, bool) {
	switch {
	case errors.Is(err, tasks.ErrInvalid), errors.Is(err, tasks.ErrRefused), errors.Is(err, tasks.ErrOpen), errors.Is(err, tasks.ErrTooMany):
		return ToolResult{Text: "Refused: " + strings.TrimPrefix(err.Error(), "tasks: "), Clean: true}, true
	case errors.Is(err, tasks.ErrConflict):
		return ToolResult{Text: "Refused: the task is paused or closed; tell the user, who resumes it from its panel.", Clean: true}, true
	}
	return ToolResult{}, false
}

// openTask finds the conversation's open task for a bookkeeping call.
func openTask(ctx context.Context, s *tasks.Store, c ToolCall) (tasks.Task, ToolResult, bool, error) {
	if c.Turn == nil {
		return tasks.Task{}, ToolResult{Text: "Refused: no turn is bound to this call.", Clean: true}, false, nil
	}
	t, ok, err := s.OpenIn(ctx, c.Turn.SessionID)
	if err != nil {
		return tasks.Task{}, ToolResult{}, false, err
	}
	if !ok {
		return tasks.Task{}, ToolResult{Text: "Refused: this conversation has no open task. Start one with delegate_task when the user hands you an outcome.", Clean: true}, false, nil
	}
	return t, ToolResult{}, true, nil
}

func delegateTask(s *tasks.Store) Tool {
	return Tool{Name: ToolDelegateTask, Label: "Taking on the task", Class: ClassWriteLocal, PII: PIILow, Timeout: tasksTimeout,
		Description: "Take on an outcome the user handed you that needs several steps or waits for runs. The task keeps the user's own message as its mandate and carries on by itself after this turn and after the runs it starts finish, until task_done, task_ask, or the user pauses or stops it. Give a short title and a plan of at most 12 steps, then work the first step in this turn. One open task per conversation.",
		Schema: `{"type":"object","additionalProperties":false,"required":["title","steps"],"properties":{
			"title":{"type":"string","minLength":1,"maxLength":120},
			"steps":{"type":"array","minItems":1,"maxItems":12,"items":{"type":"string","minLength":1,"maxLength":200}}}}`,
		Run: func(ctx context.Context, c ToolCall) (ToolResult, error) {
			var a struct {
				Title string
				Steps []string
			}
			if err := decodeArgs(c.Args, &a); err != nil {
				return ToolResult{}, err
			}
			switch t := c.Turn; {
			case t == nil:
				return ToolResult{Text: "Refused: no turn is bound to this call.", Clean: true}, nil
			case t.Continues:
				return ToolResult{Text: "Refused: this turn already works a task; record its plan with task_update.", Clean: true}, nil
			}
			steps := make([]tasks.Step, len(a.Steps))
			for i, st := range a.Steps {
				steps[i] = tasks.Step{Text: st}
			}
			tk, err := s.Create(ctx, tasks.NewTask{SessionID: c.Turn.SessionID, Title: a.Title, Mandate: c.Turn.UserMessage, Steps: steps})
			if res, ok := taskRefusal(err); ok {
				return res, nil
			}
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Text: fmt.Sprintf("Task %s started: %s. Work its first step now. When this turn ends the task carries on by itself; mark steps with task_update as you go.", tk.ID, tk.Title), Clean: true}, nil
		}}
}

func taskUpdate(s *tasks.Store) Tool {
	return Tool{Name: ToolTaskUpdate, Label: "Recording the task's progress", Class: ClassDraft, PII: PIILow, Timeout: tasksTimeout,
		Description: "Record progress on the conversation's open task: set one step's status (pending, doing, done or skipped) and/or a short note by its 1-based position, or add steps at the end with add (position 0). Notes say what happened, such as which runs started or what a run reported.",
		Schema: `{"type":"object","additionalProperties":false,"required":["position"],"properties":{
			"position":{"type":"integer","minimum":0,"maximum":12},
			"status":{"type":"string","enum":["pending","doing","done","skipped"]},
			"note":{"type":"string","maxLength":300},
			"add":{"type":"array","maxItems":12,"items":{"type":"string","minLength":1,"maxLength":200}}}}`,
		Run: func(ctx context.Context, c ToolCall) (ToolResult, error) {
			var a struct {
				Position     int
				Status, Note string
				Add          []string
			}
			if err := decodeArgs(c.Args, &a); err != nil {
				return ToolResult{}, err
			}
			tk, res, ok, err := openTask(ctx, s, c)
			if !ok || err != nil {
				return res, err
			}
			var add []tasks.Step
			for _, st := range a.Add {
				add = append(add, tasks.Step{Text: st})
			}
			tk, err = s.SetStep(ctx, tk.ID, a.Position, a.Status, a.Note, add)
			if res, ok := taskRefusal(err); ok {
				return res, nil
			}
			if err != nil {
				return ToolResult{}, err
			}
			done, total := tk.Progress()
			return ToolResult{Text: fmt.Sprintf("Recorded. %d of %d steps are done or skipped.", done, total), Clean: true}, nil
		}}
}

func taskAsk(s *tasks.Store) Tool {
	return Tool{Name: ToolTaskAsk, Label: "Asking you about the task", Class: ClassDraft, PII: PIILow, Timeout: tasksTimeout,
		Description: "Ask the user for a fact or a decision the task cannot go on without and no tool can give you. Put every open question of the task in one message. The task waits until the user answers in this conversation; your turn ends after this call.",
		Schema:      `{"type":"object","additionalProperties":false,"required":["question"],"properties":{"question":{"type":"string","minLength":1,"maxLength":1000}}}`,
		Run: func(ctx context.Context, c ToolCall) (ToolResult, error) {
			var a struct{ Question string }
			if err := decodeArgs(c.Args, &a); err != nil {
				return ToolResult{}, err
			}
			tk, res, ok, err := openTask(ctx, s, c)
			if !ok || err != nil {
				return res, err
			}
			_, err = s.Ask(ctx, tk.ID, a.Question)
			if res, ok := taskRefusal(err); ok {
				return res, nil
			}
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Text: "The task now waits for the user's answer. Put the question in your final answer and make no more calls.", Clean: true, Final: true}, nil
		}}
}

func taskDone(s *tasks.Store) Tool {
	return Tool{Name: ToolTaskDone, Label: "Closing the task", Class: ClassDraft, PII: PIILow, Timeout: tasksTimeout,
		Description: "Close the conversation's open task when its outcome is achieved, or when it cannot be achieved and asking the user would not change that. The outcome says what was done, with the runs, applications or drafts it produced, and what is left for the user (such as forms waiting at Submit).",
		Schema:      `{"type":"object","additionalProperties":false,"required":["outcome"],"properties":{"outcome":{"type":"string","minLength":1,"maxLength":2000}}}`,
		Run: func(ctx context.Context, c ToolCall) (ToolResult, error) {
			var a struct{ Outcome string }
			if err := decodeArgs(c.Args, &a); err != nil {
				return ToolResult{}, err
			}
			tk, res, ok, err := openTask(ctx, s, c)
			if !ok || err != nil {
				return res, err
			}
			_, err = s.Finish(ctx, tk.ID, a.Outcome)
			if res, ok := taskRefusal(err); ok {
				return res, nil
			}
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Text: "The task is closed. Tell the user its outcome in your final answer.", Clean: true, Final: true}, nil
		}}
}
