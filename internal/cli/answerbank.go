package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"applysmith/internal/answerbank"
	"applysmith/internal/workspace"
)

const answerBankUsage = `usage:
  applysmith answer-bank status [--json]
  applysmith answer-bank evolve [--apply] [--since-days N] [--json]
  applysmith answer-bank candidates list [--state pending|approved|rejected] [--json]
  applysmith answer-bank candidates approve <id> [--answer <text>]
  applysmith answer-bank candidates reject <id>
  applysmith answer-bank candidates unapprove <id>
  applysmith answer-bank candidates restore <id>

status counts the entries that still need an answer, the blocking ones, the ones to review and the ones marked ask-me
evolve proposes candidates from the recurring misses and moves entries with three counted corrections to stale; without --apply it only reports
candidates approve writes the proposal, or your --answer to a new entry, into the bank; only this command, the dashboard or the agent after you confirm ever approves one
candidates unapprove takes an approval back (refused once you changed what it wrote); restore takes a rejection back; either puts the candidate back in the queue

every command takes --workspace <dir>
`

func (e *env) answerBank(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(e.stderr, answerBankUsage)
		return 2
	}
	switch args[0] {
	case "status":
		return e.answerBankStatus(args[1:])
	case "evolve":
		return e.answerBankEvolve(ctx, args[1:])
	case "candidates":
		return e.answerBankCandidates(ctx, args[1:])
	}
	fmt.Fprint(e.stderr, answerBankUsage)
	return 2
}

type bankStatus struct {
	Entries     int      `json:"entries"`
	Unanswered  int      `json:"unanswered"`
	Blocking    []string `json:"blocking"`
	NeedsReview []string `json:"needsReview"`
	AskMe       []string `json:"askMe"`
	Warnings    []string `json:"warnings"`
}

func (e *env) answerBankStatus(args []string) int {
	fs := e.flags("answer-bank status")
	wsFlag, asJSON := fs.String("workspace", "", "workspace directory"), fs.Bool("json", false, "print JSON")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return 2
	}
	root, err := e.workspacePath(*wsFlag)
	if err != nil {
		return e.fail("answer-bank status", err)
	}
	svc := answerbank.New(workspace.Open(root), nil)
	loaded, err := svc.Bank()
	if err != nil {
		return e.fail("answer-bank status", err)
	}
	report, _ := svc.Unanswered()
	out := bankStatus{Entries: len(loaded.Bank.Entries), Unanswered: len(report.Unanswered), Warnings: loaded.Warnings,
		Blocking: []string{}, NeedsReview: []string{}, AskMe: append([]string{}, report.AskMe...)}
	for _, g := range report.Blocking() {
		out.Blocking = append(out.Blocking, g.ID)
	}
	for _, g := range report.NeedsReview {
		out.NeedsReview = append(out.NeedsReview, g.ID)
	}
	if *asJSON {
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return 0
	}
	if !loaded.Exists {
		fmt.Fprintln(e.stdout, "no answer bank yet; start the server or run `applysmith setup`")
	}
	fmt.Fprintf(e.stdout, "%d entries, %d need an answer (%d blocking), %d to review, %d marked ask-me\n",
		out.Entries, out.Unanswered, len(out.Blocking), len(out.NeedsReview), len(out.AskMe))
	for _, id := range out.Blocking {
		fmt.Fprintln(e.stdout, "  blocking:", id)
	}
	for _, w := range out.Warnings {
		fmt.Fprintln(e.stderr, "warning:", w)
	}
	return 0
}
