package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/learn"
	"applysmith/internal/api/learningapi"
	"applysmith/internal/api/wire"
)

// learnService opens the learning service over the workspace database in the
// way the command needs it: read-only for a report, beside a running server for
// a change.
func (e *env) learnService(ctx context.Context, wsFlag string, access storeAccess) (*learn.Service, func(), error) {
	root, err := e.workspacePath(wsFlag)
	if err != nil {
		return nil, nil, err
	}
	ws, st, release, err := e.openStore(ctx, root, access)
	if err != nil {
		return nil, nil, err
	}
	svc := learn.New(st, answerbank.New(ws, st), learn.Options{})
	return svc, func() { svc.Close(); release() }, nil
}

func (e *env) answerBankEvolve(ctx context.Context, args []string) int {
	fs := e.flags("answer-bank evolve")
	wsFlag, apply := fs.String("workspace", "", "workspace directory"), fs.Bool("apply", false, "write the candidates and the stale transitions")
	since, asJSON := fs.Int("since-days", 0, "evidence window in days (default 30)"), fs.Bool("json", false, "print JSON")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return 2
	}
	access := accessRead
	if *apply {
		access = accessShared
	}
	svc, release, err := e.learnService(ctx, *wsFlag, access)
	if err != nil {
		return e.fail("answer-bank evolve", err)
	}
	defer release()
	gen, err := svc.Generate(ctx, learn.GenerateOptions{SinceDays: *since, DryRun: !*apply})
	if err != nil {
		return e.fail("answer-bank evolve", err)
	}
	stale, err := svc.Evolve(ctx, *apply)
	if err != nil {
		return e.fail("answer-bank evolve", err)
	}
	if stale == nil {
		stale = []string{}
	}
	if *asJSON {
		return e.printJSON(map[string]any{"applied": *apply, "created": gen.Created, "refreshed": gen.Refreshed, "reproposed": gen.Reproposed,
			"corrections": gen.Corrections, "stale": stale})
	}
	verb := "would"
	if *apply {
		verb = "did"
	}
	fmt.Fprintf(e.stdout, "%s: %d new candidates, %d refreshed, %d brought back, %d corrections recorded, %d entries to stale\n",
		verb, gen.Created, gen.Refreshed, gen.Reproposed, gen.Corrections, len(stale))
	for _, id := range stale {
		fmt.Fprintln(e.stdout, "  stale:", id)
	}
	if !*apply {
		fmt.Fprintln(e.stdout, "nothing was written; run with --apply to write")
	}
	return 0
}

func (e *env) answerBankCandidates(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(e.stderr, answerBankUsage)
		return 2
	}
	switch args[0] {
	case "list":
		return e.candidatesList(ctx, args[1:])
	case "approve", "reject", "unapprove", "restore":
		return e.candidatesDecide(ctx, args[0], args[1:])
	}
	fmt.Fprint(e.stderr, answerBankUsage)
	return 2
}

func (e *env) candidatesList(ctx context.Context, args []string) int {
	fs := e.flags("answer-bank candidates list")
	wsFlag, state, asJSON := fs.String("workspace", "", "workspace directory"), fs.String("state", "", "pending, approved or rejected"), fs.Bool("json", false, "print JSON")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return 2
	}
	svc, release, err := e.learnService(ctx, *wsFlag, accessRead)
	if err != nil {
		return e.fail("answer-bank candidates list", err)
	}
	defer release()
	cs, err := svc.Candidates(ctx, *state)
	if err != nil {
		return e.fail("answer-bank candidates list", err)
	}
	if *asJSON {
		return e.printJSON(wire.AnswerCandidateList{Candidates: learningapi.WireCandidates(cs)})
	}
	if len(cs) == 0 {
		fmt.Fprintln(e.stdout, "no candidates; `applysmith answer-bank evolve --apply` proposes them from the answers you corrected or filled in yourself")
	}
	// A reviewed suggestion adds the review's verdict and its reason, and a
	// waiting one with no answer of its own the review's draft, which approve
	// stores only when it is passed as --answer.
	for _, c := range cs {
		line := fmt.Sprintf("%d\t%s\t%s\tsupport %d\t%s", c.ID, c.State, c.Type, c.Support, c.SeedQuestion)
		if c.Review.Verdict != "" {
			line += fmt.Sprintf("\t%s: %s", c.Review.Verdict, c.Review.Reason)
		}
		if c.State == learn.StatePending && c.Type == learn.TypeNewEntry && c.ProposedAnswer == "" && !c.LongForm && c.Review.Draft != "" {
			line += fmt.Sprintf("\tdraft: %q (check it, then approve with --answer)", c.Review.Draft)
		}
		fmt.Fprintln(e.stdout, line)
	}
	return 0
}

func (e *env) candidatesDecide(ctx context.Context, verb string, args []string) int {
	fs := e.flags("answer-bank candidates " + verb)
	wsFlag, answer := fs.String("workspace", "", "workspace directory"), new(string)
	if verb == "approve" {
		fs.StringVar(answer, "answer", "", "your own answer, stored in place of the proposed one (a new entry only)")
	}
	// The id may come before or after the flags.
	var idText string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		idText, args = args[0], args[1:]
	}
	if fs.Parse(args) != nil || fs.NArg() > 1 || (idText == "") == (fs.NArg() == 0) {
		fmt.Fprint(e.stderr, answerBankUsage)
		return 2
	}
	if idText == "" {
		idText = fs.Arg(0)
	}
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || id < 1 {
		fmt.Fprintf(e.stderr, "applysmith answer-bank candidates %s: %q is not a candidate id\n", verb, idText)
		return 2
	}
	svc, release, err := e.learnService(ctx, *wsFlag, accessShared)
	if err != nil {
		return e.fail("answer-bank candidates "+verb, err)
	}
	defer release()
	var d learn.Decision
	switch verb {
	case "approve":
		d, err = svc.Approve(ctx, id, *answer)
	case "reject":
		d, err = svc.Reject(ctx, id)
	case "unapprove":
		d, err = svc.Unapprove(ctx, id)
	default:
		d, err = svc.Restore(ctx, id)
	}
	var refused *learn.ConflictError
	switch {
	case errors.As(err, &refused):
		return e.fail("answer-bank candidates "+verb, fmt.Errorf("%s (%s)", refused.Message, refused.Reason))
	case err == learn.ErrNotFound:
		return e.fail("answer-bank candidates "+verb, fmt.Errorf("no candidate %d; `applysmith answer-bank candidates list` shows them", id))
	case err != nil:
		return e.fail("answer-bank candidates "+verb, err)
	}
	fmt.Fprintf(e.stdout, "%s candidate %d (%s)\n", d.Candidate.State, d.Candidate.ID, d.EntryID)
	return 0
}
