package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"applysmith/internal/agent/memory"
	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/learn"
	"applysmith/internal/workspace"
)

// BankEditor is the door the Ask Agent edits the answer bank through: the
// learn service, which decides the entry, its wordings, its kind and its site
// answers, and writes nothing after which the matcher would not answer the
// question with the user's answer. The model supplies the user's words only.
type BankEditor interface {
	Teach(ctx context.Context, req learn.TeachRequest) (learn.TeachResult, error)
	Forget(ctx context.Context, req learn.ForgetRequest) (learn.ForgetResult, error)
	Unforget(ctx context.Context, id string) (learn.ForgetResult, error)
	FindEntries(ctx context.Context, query string, limit int) ([]learn.EntryView, int, error)
}

// foundPerQuery is how many entries answer_bank_find lists.
const foundPerQuery = 8

func answerBankFind(b BankEditor) Tool {
	return Tool{Name: "answer_bank_find", Label: "Looking through the answer bank", Class: ClassRead, PII: PIIHigh, Timeout: readTimeout,
		Description: "List the saved answers whose question, wordings or answer share words with the query, best first: each entry's id, its question, its answer and its state (to answer, ask me each time, from a setup step, only on some sites, different on some sites). " +
			"Use it to show the user what the bank holds and to find the entry id an edit or a removal names. answer_bank_lookup says how the bank answers one exact question.",
		Schema: `{"type":"object","additionalProperties":false,"required":["query"],"properties":{
			"query":{"type":"string","minLength":2,"maxLength":200}}}`,
		Run: func(ctx context.Context, c ToolCall) (ToolResult, error) {
			var a struct{ Query string }
			if err := decodeArgs(c.Args, &a); err != nil {
				return ToolResult{}, err
			}
			views, total, err := b.FindEntries(ctx, a.Query, foundPerQuery)
			if r, ok := bankRefusal(err); ok {
				return r, nil
			} else if err != nil {
				return ToolResult{}, err
			}
			if len(views) == 0 {
				return ToolResult{Text: "No saved answer shares a word with that."}, nil
			}
			var s strings.Builder
			for _, v := range views {
				fmt.Fprintf(&s, "- %s | %s | %s\n", v.ID, v.Question, entryState(v))
			}
			if total > len(views) {
				fmt.Fprintf(&s, "and %d more; a narrower query lists them.\n", total-len(views))
			}
			return ToolResult{Text: strings.TrimSpace(s.String())}, nil
		}}
}

func entryState(v learn.EntryView) string {
	var parts []string
	switch {
	case v.AskMe:
		parts = append(parts, "ask me each time")
	case v.Blank:
		parts = append(parts, "to answer")
	default:
		parts = append(parts, "answer: "+v.Answer)
	}
	if v.FromSetup != "" {
		parts = append(parts, "from the "+v.FromSetup+" setup step")
	}
	if len(v.Sites) > 0 {
		parts = append(parts, "only on "+strings.Join(v.Sites, ", "))
	}
	if len(v.SiteAnswers) > 0 {
		parts = append(parts, "different on "+strings.Join(v.SiteAnswers, ", "))
	}
	if v.ToReview {
		parts = append(parts, "to review")
	}
	return strings.Join(parts, " | ")
}

func answerBankSave(b BankEditor) Tool {
	return Tool{Name: "answer_bank_save", Label: "Saving an answer", Class: ClassWriteLocal, RequiresApproval: true, PII: PIIHigh, Timeout: readTimeout,
		Description: "Save the user's answer to one application question, so forms that ask it are filled with it. Give the question as a form would ask it and the answer in the user's own words. " +
			"The bank decides the rest: it fills the entry the question reaches, adds the wording, or makes a new entry, and refuses a change after which it would not answer the question with this answer. " +
			"options: the choices, when the user names them; the answer is then the exact text of one. host: only when the user wants this answer on one site alone. entry_id: only when the user means an entry answer_bank_find listed. " +
			"Never invent an answer, never save an answer the user did not give, and one call saves one question. The user approves the exact call on a card first.",
		Schema: `{"type":"object","additionalProperties":false,"required":["question","answer"],"properties":{
			"question":{"type":"string","minLength":2,"maxLength":500},
			"answer":{"type":"string","minLength":1,"maxLength":2000},
			"options":{"type":"array","maxItems":50,"items":{"type":"string","maxLength":200}},
			"host":{"type":"string","maxLength":253,"description":"A site's host or page URL, to keep the answer to that site."},
			"entry_id":{"type":"string","maxLength":120}}}`,
		Conflict: func(ctx context.Context, c ToolCall) error {
			req, refused, err := teachRequest(c.Args)
			if err != nil || refused != "" {
				return refusedAs(refused, err)
			}
			req.DryRun = true
			res, err := b.Teach(ctx, req)
			if r, ok := bankRefusal(err); ok {
				return refusedAs(r.Text, nil)
			}
			if err == nil && res.Action == learn.ActionUnchanged {
				return refusedAs(fmt.Sprintf("The bank already answers %q with %s; there is nothing to save (entry %s).", res.Question, strings.TrimSpace(req.Answer), res.EntryID), nil)
			}
			return nil
		},
		Run: func(ctx context.Context, c ToolCall) (ToolResult, error) {
			req, refused, err := teachRequest(c.Args)
			if err != nil {
				return ToolResult{}, err
			} else if refused != "" {
				return ToolResult{Text: refused}, nil
			}
			// The guard chain's approval requirement ran before this point, so
			// a call that reaches the tool carries the user's approval.
			res, err := b.Teach(ctx, req)
			if r, ok := bankRefusal(err); ok {
				return r, nil
			} else if err != nil {
				return ToolResult{}, err
			}
			answer := strings.TrimSpace(req.Answer)
			var text string
			switch res.Action {
			case learn.ActionAdded:
				text = fmt.Sprintf("Saved a new answer for %q: %s", res.Question, answer)
				if req.Host != "" {
					text += ", on " + learn.HostOf(req.Host) + " only"
				}
			case learn.ActionSiteAnswer:
				text = fmt.Sprintf("On %s, %q is now answered %s (it was %s); other sites keep the entry's answer", learn.HostOf(req.Host), res.Question, answer, beforeText(res.Before))
			case learn.ActionUnchanged:
				return ToolResult{Text: fmt.Sprintf("The bank already answers %q with %s; nothing changed (entry %s).", res.Question, answer, res.EntryID)}, nil
			default:
				text = fmt.Sprintf("Changed %q from %s to %s", res.Question, beforeText(res.Before), answer)
			}
			if res.Phrasing {
				text += "; the question's wording now reaches this entry too"
			}
			return ToolResult{Text: fmt.Sprintf("%s (entry %s). Forms that ask it get this answer from now on.", text, res.EntryID)}, nil
		}}
}

// teachRequest reads answer_bank_save's arguments; refused is the answer to
// a text shaped like an instruction to the assistant.
func teachRequest(args json.RawMessage) (req learn.TeachRequest, refused string, err error) {
	var a struct {
		Question, Answer, Host string
		Options                []string
		EntryID                string `json:"entry_id"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return req, "", err
	}
	if memory.InjectionShaped(a.Question) || memory.InjectionShaped(a.Answer) {
		return req, "Refused: the text reads as an instruction to the assistant, so nothing was stored.", nil
	}
	return learn.TeachRequest{Question: a.Question, Answer: a.Answer, Options: a.Options, Host: a.Host, EntryID: a.EntryID}, "", nil
}

// refusedAs is a Conflict's answer: the refusal text as ErrRefused, or the
// error that kept the check from deciding, which lets the call be held.
func refusedAs(text string, err error) error {
	if err != nil || text == "" {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrRefused, text)
}

func beforeText(s string) string {
	if strings.TrimSpace(s) == "" {
		return "no answer"
	}
	return s
}

func answerBankForget(b BankEditor) Tool {
	return Tool{Name: "answer_bank_forget", Label: "Removing a saved answer", Class: ClassWriteLocal, RequiresApproval: true, PII: PIIHigh, Timeout: readTimeout,
		Description: "Remove a saved answer named by its entry id from answer_bank_find: action delete removes the entry, ask_me keeps it and leaves its question to the user on every form, undo puts back an entry deleted in the last hour. " +
			"With host, delete removes only the entry's answer on that site. An entry shipped with the app or set by a setup step cannot be deleted; ask_me is the way out. The user approves the exact call on a card first.",
		Schema: `{"type":"object","additionalProperties":false,"required":["entry_id","action"],"properties":{
			"entry_id":{"type":"string","minLength":1,"maxLength":120},
			"action":{"type":"string","enum":["delete","ask_me","undo"]},
			"host":{"type":"string","maxLength":253}}}`,
		Conflict: func(ctx context.Context, c ToolCall) error {
			var a struct {
				EntryID string `json:"entry_id"`
				Action  string
				Host    string
			}
			if err := decodeArgs(c.Args, &a); err != nil || a.Action == "undo" {
				return nil
			}
			_, err := b.Forget(ctx, learn.ForgetRequest{EntryID: a.EntryID, Host: a.Host, AskMe: a.Action == "ask_me", DryRun: true})
			if r, ok := bankRefusal(err); ok {
				return refusedAs(r.Text, nil)
			}
			return nil
		},
		Run: func(ctx context.Context, c ToolCall) (ToolResult, error) {
			var a struct {
				EntryID string `json:"entry_id"`
				Action  string
				Host    string
			}
			if err := decodeArgs(c.Args, &a); err != nil {
				return ToolResult{}, err
			}
			var res learn.ForgetResult
			var err error
			switch a.Action {
			case "undo":
				res, err = b.Unforget(ctx, a.EntryID)
			case "ask_me", "delete":
				res, err = b.Forget(ctx, learn.ForgetRequest{EntryID: a.EntryID, Host: a.Host, AskMe: a.Action == "ask_me"})
			default:
				return ToolResult{Text: "Refused: the action must be delete, ask_me or undo."}, nil
			}
			if r, ok := bankRefusal(err); ok {
				return r, nil
			} else if err != nil {
				return ToolResult{}, err
			}
			switch res.Action {
			case learn.ActionAskMe:
				return ToolResult{Text: fmt.Sprintf("Forms that ask %q are now left to the user each time (the answer was %s).", res.Question, beforeText(res.Before))}, nil
			case learn.ActionSiteRemoved:
				return ToolResult{Text: fmt.Sprintf("Removed the answer %s for %q on %s; the entry's own answer applies there again.", beforeText(res.Before), res.Question, learn.HostOf(a.Host))}, nil
			case learn.ActionRestored:
				return ToolResult{Text: fmt.Sprintf("Put back %q with its answer %s.", res.Question, beforeText(res.Before))}, nil
			}
			return ToolResult{Text: fmt.Sprintf("Deleted %q (its answer was %s); forms that ask it are left to the user. Undo works for an hour.", res.Question, beforeText(res.Before))}, nil
		}}
}

// bankRefusal turns a refusal of the learn service, a bank another writer
// changed meanwhile or a change the bank's rules refuse into the tool's
// answer.
func bankRefusal(err error) (ToolResult, bool) {
	var refused *learn.ConflictError
	var stale *workspace.ConflictError
	var invalid *answerbank.ValidationError
	switch {
	case errors.As(err, &refused):
		return ToolResult{Text: "Not changed: " + refused.Message + "."}, true
	case errors.As(err, &stale):
		return ToolResult{Text: "Not changed: the bank changed while this was saved; read it again and retry once."}, true
	case errors.As(err, &invalid):
		return ToolResult{Text: "Not changed: the bank refuses it (" + invalid.Error() + ")."}, true
	}
	return ToolResult{}, false
}
