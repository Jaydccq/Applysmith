package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/workspace"
)

var answerBankFields = []Field{
	{Name: "answers", Type: "object", Doc: "A map of entry id to the answer, or to the ask-me marker " + match.AskMe + " to leave that question to yourself. Only entries that take a single answer can be set here; answer the others on the Answer Bank page."},
	{Name: "confirm_review", Type: "bool", Doc: "Confirm the shipped defaults that are flagged for review (self-identification, compliance declarations and consents). Listed in the step's reasons before you confirm."},
}

type answerBankInput struct {
	Answers       map[string]string `json:"answers"`
	ConfirmReview bool              `json:"confirm_review"`
}

func bankService(env *Env) *answerbank.Service {
	if env.Bank != nil {
		return env.Bank
	}
	return answerbank.New(env.Workspace, env.Store)
}

func detectAnswerBank(_ context.Context, env *Env) Detection {
	if env.Workspace == nil {
		return missing(errNoWorkspace.Error())
	}
	svc := bankService(env)
	loaded, err := svc.File()
	switch {
	case err != nil:
		return verdict(reasonf(ReasonInvalid, "answer-bank.yml", "answer-bank.yml fails the answer bank's validation: "+err.Error()))
	case !loaded.Exists:
		return missing("answer-bank.yml does not exist; complete the workspace step first")
	}
	report, err := svc.Unanswered()
	if err != nil {
		return verdict(reasonf(ReasonInvalid, "answer-bank.yml", err.Error()))
	}
	stamped, _ := svc.Bank()
	var rs []Reason
	if blocking := report.Blocking(); len(blocking) > 0 {
		head := fmt.Sprintf("%d blocking entries remain", len(blocking))
		if len(blocking) == 1 {
			head = "1 blocking entry remains"
		}
		rs = append(rs, reasonf(ReasonBlank, "blocking", head))
		for _, g := range blocking {
			text := g.ID
			if e, ok := stamped.Bank.Find(g.ID); ok && e.Description != "" {
				text = e.Description + " (" + g.ID + ")"
			}
			rs = append(rs, reasonf(ReasonBlank, g.Category, text))
		}
	}
	groups := map[string]int{}
	for _, g := range report.NeedsReview {
		groups[g.Category]++
	}
	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		what := strings.TrimSpace(n + " default")
		text := fmt.Sprintf("%d %ss need your confirmation; review them, then confirm", groups[n], what)
		if groups[n] == 1 {
			text = "1 " + what + " needs your confirmation; review it, then confirm"
		}
		rs = append(rs, reasonf(ReasonBlank, n, text))
	}
	return verdict(rs...)
}

// setAnswer writes a single answer into the position the entry's kind reads.
// An answer naming one of the entry's choices is written as that choice, with
// the wordings a form offers it under.
func setAnswer(e *match.Entry, v string) bool {
	c, isChoice := answerbank.ShapeOf(*e).ChoiceFor(v)
	switch e.Kind {
	case match.KindFixed:
		e.Answer = v
	case match.KindPickOption:
		e.Preferred = []string{v}
		if isChoice {
			e.Preferred = c.Matches
		}
	case match.KindCompensationRange:
		e.DefaultAnswer = v
	case match.KindMultiSelect:
		e.Selected = []string{v}
		if isChoice {
			e.Selected = []string{c.Label}
		}
	case match.KindEmployerHistory:
		// The one answer a person can give for every company outside the
		// employers list is No; a company on it then answers Yes.
		if !strings.EqualFold(v, "no") {
			return false
		}
		e.AnswerIfNoMatch, e.AnswerIfMatch = v, "Yes"
	default:
		return false
	}
	e.NeedsReview = false
	return true
}

func applyAnswerBank(ctx context.Context, env *Env, raw json.RawMessage, dry bool) (Applied, error) {
	var in answerBankInput
	if err := decodeInput(raw, answerBankFields, &in); err != nil {
		return Applied{}, err
	}
	if env.Workspace == nil {
		return Applied{}, &EnvError{Message: errNoWorkspace.Error()}
	}
	svc := bankService(env)
	loaded, err := svc.File()
	if err != nil {
		return Applied{}, &EnvError{Message: "answer-bank.yml cannot be loaded", Err: err}
	}
	if !loaded.Exists {
		return Applied{}, &EnvError{Message: "answer-bank.yml does not exist; complete the workspace step first"}
	}
	after := loaded.Bank.Clone()
	fe := FieldErrors{}
	ids := make([]string, 0, len(in.Answers))
	for id := range in.Answers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		v := strings.TrimSpace(in.Answers[id])
		key := "answers." + id
		idx := -1
		for i := range after.Entries {
			if after.Entries[i].ID == id {
				idx = i
				break
			}
		}
		switch {
		case idx < 0:
			fe.add(key, "no entry has this id")
		case v == "":
			fe.add(key, "is empty; give an answer or "+match.AskMe)
		case !setAnswer(&after.Entries[idx], v):
			fe.add(key, "this kind of entry takes more than one value; answer it on the Answer Bank page")
		}
	}
	if err := fe.orNil(); err != nil {
		return Applied{}, err
	}
	if in.ConfirmReview {
		stamped, _ := svc.Bank()
		for _, g := range match.NeedsAnswer(stamped.Bank).NeedsReview {
			for i := range after.Entries {
				if after.Entries[i].ID == g.ID {
					after.Entries[i].NeedsReview = false
				}
			}
		}
	}
	wrote := false
	if len(in.Answers) > 0 || in.ConfirmReview {
		err := answerbank.CheckAnswers(loaded.Bank, after)
		saved := workspace.Saved{}
		if err == nil && !dry {
			saved, err = svc.SaveChange(loaded.Bank, after, loaded.Revision)
		}
		var ve *answerbank.ValidationError
		if errors.As(err, &ve) {
			for _, is := range ve.Issues {
				fe.add("answers."+is.EntryID, is.Message)
			}
			return Applied{}, fe
		}
		if err != nil {
			return Applied{}, &EnvError{Message: "answer-bank.yml could not be written", Err: err}
		}
		wrote = saved.Changed
	}
	return Applied{Step: "answer-bank", DryRun: dry, Wrote: wrote, Detection: Evaluate(ctx, env, mustStep("answer-bank"))}, nil
}
