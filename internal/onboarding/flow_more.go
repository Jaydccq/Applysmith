package onboarding

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"applysmith/internal/answerbank/match"
)

// The interactive questions of the steps after the profile: the story, the
// résumé, the answer bank and the orientation.

func readTextFile(path string) (string, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown", ".txt", "":
	default:
		return "", FieldErrors{"file": "only .md and .txt files can be read here; paste the text into a .txt file"}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", FieldErrors{"file": err.Error()}
	}
	if len(b) > 256<<10 {
		return "", FieldErrors{"file": "the file is larger than 256 KiB"}
	}
	return string(b), nil
}

func (f *Flow) askStory(ctx context.Context) (map[string]any, error) {
	in := map[string]any{}
	fmt.Fprintln(f.Out, "  Write your narrative in a text file with: a one-line headline, strengths with proof, what you want next, what you will not accept, and how you prefer to be described.")
	path, err := f.ask("Path to your narrative (.md or .txt); empty keeps the stored narrative.md", "")
	if err != nil {
		return nil, err
	}
	if path != "" {
		if in["narrative"], err = readTextFile(path); err != nil {
			return nil, err
		}
	}
	if _, skipped := f.Env.decision(ctx, skipKey(voiceSkipID)); skipped {
		return in, nil
	}
	voice, err := f.ask("Path to samples of your own writing (optional; empty skips the voice part)", "")
	if err != nil {
		return nil, err
	}
	if voice == "" {
		return in, Skip(ctx, f.Env, voiceSkipID)
	}
	if in["voice"], err = readTextFile(voice); err != nil {
		return nil, err
	}
	return in, nil
}

func (f *Flow) askResumes() (map[string]any, error) {
	path, err := f.ask("Path to a résumé PDF (at most 10 MiB)", "")
	if err != nil {
		return nil, err
	}
	id, err := f.ask("Short id for it (lowercase letters, digits and hyphens)", strings.TrimSuffix(strings.ToLower(filepath.Base(path)), ".pdf"))
	if err != nil {
		return nil, err
	}
	label, err := f.ask("Label", id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"add": map[string]any{"path": path, "id": id, "label": label, "default": true}}, nil
}

func (f *Flow) askAnswerBank(ctx context.Context) (map[string]any, error) {
	in := map[string]any{}
	d := detectAnswerBank(ctx, f.Env)
	answers := map[string]string{}
	stamped, _ := bankService(f.Env).Bank()
	report := match.NeedsAnswer(stamped.Bank)
	blocking := report.Blocking()
	oneByOne := false
	if len(blocking) > 0 {
		var err error
		oneByOne, err = f.yesNo(fmt.Sprintf("%d entries need your answer. Answer them one by one? (no marks every one as an answer you will give yourself)", len(blocking)))
		if err != nil {
			return nil, err
		}
	}
	for _, g := range blocking {
		if !oneByOne {
			answers[g.ID] = match.AskMe
			continue
		}
		label := g.ID
		if e, ok := stamped.Bank.Find(g.ID); ok && e.Description != "" {
			label = e.Description
		}
		a, err := f.ask(fmt.Sprintf("%s [%s] (an answer, or %s to leave it to yourself)", label, g.Category, match.AskMe), "")
		if err != nil {
			return nil, err
		}
		if a != "" {
			answers[g.ID] = a
		}
	}
	if len(answers) > 0 {
		in["answers"] = answers
	}
	if len(report.NeedsReview) > 0 {
		for _, r := range d.Reasons {
			if r.Field != "blocking" && strings.Contains(r.Text, "need your confirmation") {
				fmt.Fprintf(f.Out, "  %s\n", r.Text)
			}
		}
		yes, err := f.yesNo("Confirm the shipped defaults that are flagged for review?")
		if err != nil {
			return nil, err
		}
		if yes {
			in["confirm_review"] = true
		}
	}
	return in, nil
}

// orient shows the daily loop and the optional steps left open, then records
// the acknowledgement the user gives.
func (f *Flow) orient(ctx context.Context) error {
	fmt.Fprintln(f.Out, "  The daily loop: find a posting, evaluate it, read the report, prepare documents, autofill, track the outcome.")
	for _, line := range SkippedSteps(ctx, f.Env) {
		fmt.Fprintf(f.Out, "  not set up: %s\n", line)
	}
	yes, err := f.yesNo("Have you read this?")
	if err != nil || !yes {
		return err
	}
	return Ack(ctx, f.Env, "orientation")
}
