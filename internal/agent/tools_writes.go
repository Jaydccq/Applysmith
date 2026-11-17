package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"applysmith/internal/agent/memory"
	"applysmith/internal/agent/schedprop"
	"applysmith/internal/kb"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
)

// StoryRecorder folds the user's answer to a story follow-up into its draft.
type StoryRecorder interface {
	RecordAnswer(ctx context.Context, draftID int64, questionID, text string) (kb.Draft, error)
}

// SkipListEditor reads and edits the profile's batch skip list.
type SkipListEditor interface {
	// SkipList returns the effective list; defaulted says the profile has no
	// list of its own, so the built-in one applies.
	SkipList(ctx context.Context) (entries []string, defaulted bool, err error)
	AddSkip(ctx context.Context, company string) (changed bool, err error)
	RemoveSkip(ctx context.Context, company string) (changed bool, err error)
}

// WriteTools builds the write-local and draft tools whose services exist.
func WriteTools(d ToolDeps) []Tool {
	var out []Tool
	if d.BankEditor != nil {
		out = append(out, answerBankSave(d.BankEditor), answerBankForget(d.BankEditor))
	}
	if d.Wiki != nil {
		out = append(out, wikiWrite(d.Wiki))
	}
	if d.SkipList != nil {
		out = append(out, batchSkipList(d.SkipList))
	}
	if d.Stories != nil {
		out = append(out, recordStoryAnswer(d.Stories))
	}
	if d.ScheduleProposals != nil && d.Schedules != nil {
		out = append(out, proposeScheduleChange(d.ScheduleProposals, d.Schedules))
	}
	if d.GmailThreads != nil {
		out = append(out, gmailThreadAction(d.GmailThreads, d.local()))
	}
	if d.Tracker != nil {
		out = append(out, trackerSetStatus(d.Tracker))
	}
	return out
}

// trackerSetStatus moves one tracker row as the agent actor, so the tracker's
// transition table decides: it skips a row, takes a skipped row back, or
// discards a row the user does not want. Applied and the later statuses are
// the user's, Gmail's and the apply chain's.
func trackerSetStatus(tr *tracker.Tracker) Tool {
	return Tool{Name: "tracker_set_status", Label: "Updating the tracker", Class: ClassWriteLocal, PII: PIILow, Timeout: readTimeout,
		Description: "Change one tracker row's status when the user asks: skipped (not pursuing it for now), evaluated (take a skipped row back) or discarded (the user does not want it). " +
			"Find the row id with tracker_query. Marking a row applied, responded, interview, offer or rejected is not done here.",
		Schema: `{"type":"object","additionalProperties":false,"required":["id","status"],"properties":{
			"id":{"type":"integer","minimum":1},
			"status":{"type":"string","enum":["skipped","evaluated","discarded"]},
			"note":{"type":"string","maxLength":300,"description":"Why, in the user's words; kept in the row's notes."}}}`,
		Run: func(ctx context.Context, c ToolCall) (ToolResult, error) {
			var a struct {
				ID     int64
				Status string
				Note   string
			}
			if err := decodeArgs(c.Args, &a); err != nil {
				return ToolResult{}, err
			}
			note := strings.TrimSpace(a.Note)
			if memory.InjectionShaped(note) {
				return ToolResult{Text: "Refused: the note reads as an instruction to the assistant, so nothing was changed."}, nil
			}
			res, err := tr.SetStatus(ctx, tracker.StatusChange{ID: a.ID, To: tracker.Status(a.Status), Actor: tracker.ActorAgent, Note: note})
			var refused *tracker.RefusedError
			switch {
			case errors.As(err, &refused):
				return ToolResult{Text: fmt.Sprintf("Refused: row %d is %s, and %s.", a.ID, refused.From, refused.Reason)}, nil
			case errors.Is(err, store.ErrNotFound):
				return ToolResult{Text: fmt.Sprintf("Refused: there is no tracker row %d.", a.ID)}, nil
			case err != nil:
				return ToolResult{}, err
			}
			e := res.Entry
			if !res.Changed {
				// The status stands; the user's reason is still theirs to keep.
				if note != "" && !strings.Contains(e.Notes, note) {
					if _, err := tr.AddNote(ctx, e.ID, tracker.ActorAgent, note, 0); err != nil {
						return ToolResult{}, err
					}
					return ToolResult{Text: fmt.Sprintf("Row %d (%s, %s) is already %s; the note was added.", e.ID, e.Company, e.Role, e.Status)}, nil
				}
				return ToolResult{Text: fmt.Sprintf("Row %d (%s, %s) is already %s.", e.ID, e.Company, e.Role, e.Status)}, nil
			}
			return ToolResult{Text: fmt.Sprintf("Row %d (%s, %s) is now %s.", e.ID, e.Company, e.Role, e.Status)}, nil
		}}
}

func batchSkipList(e SkipListEditor) Tool {
	return Tool{Name: "batch_skip_list", Label: "Editing the skip list", Class: ClassWriteLocal, PII: PIINone, Timeout: readTimeout,
		Description: "Show or edit the companies a list-wide application run leaves to the user (the batch skip list): action list, add or remove, with the company name for the last two. " +
			"A single application the user names is still started; only list runs skip these companies.",
		Schema: `{"type":"object","additionalProperties":false,"required":["action"],"properties":{
			"action":{"type":"string","enum":["list","add","remove"]},
			"company":{"type":"string","minLength":1,"maxLength":120}}}`,
		Run: func(ctx context.Context, c ToolCall) (ToolResult, error) {
			var a struct{ Action, Company string }
			if err := decodeArgs(c.Args, &a); err != nil {
				return ToolResult{}, err
			}
			a.Company = strings.TrimSpace(a.Company)
			switch a.Action {
			case "list":
				return listSkip(ctx, e)
			case "add", "remove":
				if a.Company == "" {
					return ToolResult{Text: "Refused: name the company."}, nil
				}
				if memory.InjectionShaped(a.Company) {
					return ToolResult{Text: "Refused: the text reads as an instruction to the assistant, so nothing was stored."}, nil
				}
				apply := e.AddSkip
				verb := "Added"
				if a.Action == "remove" {
					apply, verb = e.RemoveSkip, "Removed"
				}
				changed, err := apply(ctx, a.Company)
				if err != nil {
					return answerRefusal(err)
				}
				if !changed {
					return ToolResult{Text: fmt.Sprintf("The skip list already %s %s.", map[string]string{"add": "holds", "remove": "lacks"}[a.Action], a.Company)}, nil
				}
				return ToolResult{Text: fmt.Sprintf("%s %s on the skip list.", verb, a.Company)}, nil
			}
			return ToolResult{Text: "Refused: the action must be list, add or remove."}, nil
		}}
}

func listSkip(ctx context.Context, e SkipListEditor) (ToolResult, error) {
	entries, defaulted, err := e.SkipList(ctx)
	if err != nil {
		return ToolResult{}, err
	}
	if len(entries) == 0 {
		return ToolResult{Text: "The skip list is empty: a list run leaves no company to the user."}, nil
	}
	src := "the profile's list"
	if defaulted {
		src = "the built-in list, because the profile names none"
	}
	return ToolResult{Text: fmt.Sprintf("A list run leaves these companies to the user (%s): %s.", src, strings.Join(entries, ", "))}, nil
}

func recordStoryAnswer(r StoryRecorder) Tool {
	return Tool{Name: "record_story_answer", Label: "Recording a story answer", Class: ClassWriteLocal, PII: PIILow, Timeout: readTimeout,
		Description: "Record the user's own answer to one open follow-up question of a story draft (draft_id and question_id come from story_context or the story mining result). Only the user's words are recorded, never a guess. It never publishes the story.",
		Schema: `{"type":"object","additionalProperties":false,"required":["draft_id","question_id","text"],"properties":{
			"draft_id":{"type":"integer","minimum":1},
			"question_id":{"type":"string","minLength":1,"maxLength":80},
			"text":{"type":"string","minLength":1,"maxLength":2000}}}`,
		Run: func(ctx context.Context, c ToolCall) (ToolResult, error) {
			var a struct {
				DraftID    int64  `json:"draft_id"`
				QuestionID string `json:"question_id"`
				Text       string
			}
			if err := decodeArgs(c.Args, &a); err != nil {
				return ToolResult{}, err
			}
			if memory.InjectionShaped(a.Text) {
				return ToolResult{Text: "Refused: the text reads as an instruction to the assistant, so nothing was recorded."}, nil
			}
			d, err := r.RecordAnswer(ctx, a.DraftID, a.QuestionID, a.Text)
			var secret *kb.SecretError
			switch {
			case errors.As(err, &secret):
				return ToolResult{Text: "Refused: the answer looks like it holds a secret, so nothing was recorded."}, nil
			case errors.Is(err, kb.ErrNotFound):
				return ToolResult{Text: "There is no such draft or open question; check the ids."}, nil
			case errors.Is(err, kb.ErrInvalid), errors.Is(err, kb.ErrConflict):
				return ToolResult{Text: "Refused: " + strings.TrimPrefix(err.Error(), "kb: ") + "."}, nil
			case err != nil:
				return ToolResult{}, err
			}
			return ToolResult{Text: fmt.Sprintf("Recorded the answer in draft %d (revision %d). The story is not published.", d.ID, d.Revision)}, nil
		}}
}

func proposeScheduleChange(p *schedprop.Store, s Schedules) Tool {
	return Tool{Name: "propose_schedule_change", Label: "Proposing a schedule change", Class: ClassDraft, PII: PIINone, Timeout: readTimeout,
		Description: "Propose a change to one schedule of list_schedules: switch it on or off (enabled) and/or give it a new cadence (cadence_kind daily or weekly, time as HH:MM local, weekdays for weekly), with a short reason. " +
			"The schedule does not change until the user accepts the proposal in the dashboard; a newer proposal for the same schedule replaces the pending one.",
		Schema: `{"type":"object","additionalProperties":false,"required":["schedule_id","reason"],"properties":{
			"schedule_id":{"type":"integer","minimum":1},
			"reason":{"type":"string","minLength":1,"maxLength":300},
			"enabled":{"type":"boolean"},
			"cadence_kind":{"type":"string","enum":["daily","weekly"]},
			"time":{"type":"string","maxLength":5,"description":"Local time as HH:MM."},
			"weekdays":{"type":"array","maxItems":7,"items":{"type":"string","enum":["sun","mon","tue","wed","thu","fri","sat"]}}}}`,
		Run: func(ctx context.Context, c ToolCall) (ToolResult, error) {
			var a struct {
				ScheduleID  int64 `json:"schedule_id"`
				Reason      string
				Enabled     *bool
				CadenceKind string `json:"cadence_kind"`
				Time        string
				Weekdays    []string
			}
			if err := decodeArgs(c.Args, &a); err != nil {
				return ToolResult{}, err
			}
			lines, err := s.Schedules(ctx)
			if err != nil {
				return ToolResult{}, err
			}
			if !slices.ContainsFunc(lines, func(l ScheduleLine) bool { return l.ID == a.ScheduleID }) {
				return ToolResult{Text: "There is no such schedule; check the ids with list_schedules."}, nil
			}
			ch := schedprop.Change{Enabled: a.Enabled}
			if a.CadenceKind != "" || a.Time != "" || len(a.Weekdays) > 0 {
				pre, err := cadenceOf(a.CadenceKind, a.Time, a.Weekdays)
				if err != nil {
					return refusal(err)
				}
				ch.Preset = &pre
			}
			prop, err := p.Propose(ctx, a.ScheduleID, a.Reason, ch)
			switch {
			case errors.Is(err, schedprop.ErrInvalid):
				return refusal(err)
			case err != nil:
				return ToolResult{}, err
			}
			return ToolResult{Text: fmt.Sprintf("Proposed as %s. The schedule is unchanged until the user accepts it in the dashboard.", prop.ID)}, nil
		}}
}

// cadenceOf builds the proposed preset. The timezone is left empty: accepting
// keeps the schedule's own.
func cadenceOf(kind, at string, days []string) (schedprop.Preset, error) {
	if kind == "" || at == "" {
		return schedprop.Preset{}, errors.New("a cadence needs cadence_kind and time")
	}
	if _, err := time.Parse("15:04", at); err != nil || len(at) != 5 {
		return schedprop.Preset{}, errors.New("time must be HH:MM")
	}
	if kind == "weekly" && len(days) == 0 {
		return schedprop.Preset{}, errors.New("a weekly cadence needs weekdays")
	}
	if kind == "daily" && len(days) > 0 {
		return schedprop.Preset{}, errors.New("a daily cadence takes no weekdays")
	}
	return schedprop.Preset{Kind: kind, Time: at, Weekdays: days}, nil
}
