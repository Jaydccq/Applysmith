package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"applysmith/internal/api"
	"applysmith/internal/cvparse"
	"applysmith/internal/onboarding"
	"applysmith/internal/workspace"
)

const setupUsage = `usage:
  applysmith setup [--answers <file|->]            walk the essential steps interactively (needs a terminal, or --answers)
  applysmith setup status [--json]                 every step with its status and outstanding reasons
  applysmith setup next [--json]                   the first incomplete required step with its schema and commands
  applysmith setup schema <step> [--json]          the input a step's writer accepts
  applysmith setup apply <step> [--file <path|->] [--dry-run] [--json]
  applysmith setup intake preview <file>... [--json]                    optional: compare several documents with the profile; a file is [<kind>:]<path>, kinds cv (default), profile-export, education-record, project, reference, course, repository
  applysmith setup intake apply <file>... --revision <rev> --select <file|->   apply the selections (a JSON list) of the preview built at <rev>
  applysmith setup ack <step>                      confirm example values are your own, or a reviewed list
  applysmith setup skip <step> | unskip <step>     skip or restore an optional step

every command takes --workspace <dir>; exit codes: 0 done, 1 error, 2 rejected input, 3 a person must act, 4 environment failure
`

func (e *env) setup(ctx context.Context, args []string) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return e.setupInteractive(ctx, args)
	}
	cmds := map[string]func(context.Context, []string) int{
		"status": e.setupStatus, "next": e.setupNext, "schema": e.setupSchema, "apply": e.setupApply,
		"intake": e.setupIntake, "ack": e.setupDecision("ack"), "skip": e.setupDecision("skip"), "unskip": e.setupDecision("unskip"),
	}
	if run, ok := cmds[args[0]]; ok {
		return run(ctx, args[1:])
	}
	fmt.Fprint(e.stderr, setupUsage)
	return 2
}

func (e *env) setupInteractive(ctx context.Context, args []string) int {
	fs := e.flagsWithUsage("setup", setupUsage)
	wsFlag := fs.String("workspace", "", "workspace directory")
	answers := fs.String("answers", "", "file of answers, one per line, or - for standard input")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprint(e.stderr, setupUsage)
		return 2
	}
	if *answers == "" && !e.isTerminal() {
		fmt.Fprintln(e.stderr, "applysmith setup: standard input is not a terminal; run it in a terminal, give --answers <file|->, or use the agent commands (`applysmith setup next`, `applysmith setup apply`)")
		return 2
	}
	in, closer, err := e.prompter(*answers)
	if err != nil {
		return e.report("setup", &onboarding.RejectedError{Message: err.Error()})
	}
	defer closer.Close()
	return e.withWriteAfterWorkspace(ctx, "setup", *wsFlag, func(oe *onboarding.Env, prepare func(context.Context) error) int {
		flow := &onboarding.Flow{Env: oe, In: in, Out: e.stdout, Reopen: prepare}
		err := flow.Run(ctx)
		if errors.Is(err, onboarding.ErrRestart) {
			fmt.Fprintf(e.stdout, "\nThe workspace is now %s. Run `applysmith setup --workspace %s` to continue there.\n", oe.Workspace.Root, oe.Workspace.Root)
			return 0
		}
		if err != nil {
			return e.report("setup", err)
		}
		return 0
	})
}

func (e *env) setupStatus(ctx context.Context, args []string) int {
	fs := e.flags("setup status")
	wsFlag := fs.String("workspace", "", "workspace directory")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return 2
	}
	return e.withRead(ctx, "setup status", *wsFlag, func(oe *onboarding.Env) int {
		rep := onboarding.State(ctx, oe)
		if *asJSON {
			return e.printJSON(rep)
		}
		fmt.Fprintf(e.stdout, "progress %d%%\n", rep.Progress)
		for _, s := range rep.Steps {
			req := "optional"
			if s.Required {
				req = "required"
			}
			fmt.Fprintf(e.stdout, "%2d %-20s %-9s %s\n", s.N, s.ID, s.Status, req)
			for _, r := range s.Reasons {
				fmt.Fprintf(e.stdout, "     - %s\n", r.Text)
			}
			for _, w := range s.Warnings {
				fmt.Fprintf(e.stdout, "     ! %s\n", w)
			}
		}
		for _, sec := range rep.Secrets {
			fmt.Fprintf(e.stdout, "secret %s present=%v saved=%s\n", sec.Name, sec.Present, sec.SavedAt)
		}
		return 0
	})
}

func (e *env) setupNext(ctx context.Context, args []string) int {
	fs := e.flags("setup next")
	wsFlag := fs.String("workspace", "", "workspace directory")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return 2
	}
	return e.withRead(ctx, "setup next", *wsFlag, func(oe *onboarding.Env) int {
		n := onboarding.Next(ctx, oe)
		if *asJSON {
			return e.printJSON(n)
		}
		if n.Done {
			fmt.Fprintln(e.stdout, "every required step is complete")
			return 0
		}
		s := n.Step
		fmt.Fprintf(e.stdout, "step %d: %s (%s)\n%s\nguide: %s\n", s.N, s.Title, s.ID, s.Purpose, s.Guide)
		for _, r := range s.Reasons {
			fmt.Fprintf(e.stdout, "  - %s\n", r.Text)
		}
		for _, c := range s.Commands {
			fmt.Fprintf(e.stdout, "  run: %s\n", c)
		}
		if s.NeedsHuman {
			fmt.Fprintf(e.stdout, "needs a person: %s\n", s.Instruction)
		}
		return 0
	})
}

func (e *env) setupSchema(ctx context.Context, args []string) int {
	fs := e.flags("setup schema")
	ignoredWorkspace(fs)
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprint(e.stderr, setupUsage)
		return 2
	}
	fields, err := onboarding.Schema(pos[0])
	if err != nil {
		return e.report("setup schema", err)
	}
	if *asJSON {
		return e.printJSON(fields)
	}
	for _, f := range fields {
		doc := f.Doc
		if len(f.Enum) > 0 {
			doc += " One of: " + strings.Join(f.Enum, ", ") + "."
		}
		fmt.Fprintf(e.stdout, "%-28s %-11s %s\n", f.Name, f.Type, doc)
	}
	return 0
}

func (e *env) readInput(file string) ([]byte, error) {
	if file == "" {
		return nil, nil
	}
	if file == "-" {
		return io.ReadAll(io.LimitReader(e.in(), 4<<20))
	}
	return os.ReadFile(file)
}

func (e *env) setupApply(ctx context.Context, args []string) int {
	fs := e.flags("setup apply")
	wsFlag := fs.String("workspace", "", "workspace directory")
	file := fs.String("file", "", "JSON input, a path or - for standard input")
	dry := fs.Bool("dry-run", false, "validate and write nothing")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprint(e.stderr, setupUsage)
		return 2
	}
	input, err := e.readInput(*file)
	if err != nil {
		return e.report("setup apply", &onboarding.RejectedError{Message: err.Error()})
	}
	run := func(oe *onboarding.Env) int {
		res, err := onboarding.Apply(ctx, oe, pos[0], input, *dry)
		if err != nil {
			return e.report("setup apply", err)
		}
		if *asJSON {
			return e.printJSON(onboarding.WireApplied(res, oe.InServer))
		}
		fmt.Fprintf(e.stdout, "%s: %s\n", res.Step, res.Detection.Status)
		for _, r := range res.Detection.Reasons {
			fmt.Fprintf(e.stdout, "  - %s\n", r.Text)
		}
		for _, n := range res.Notes {
			fmt.Fprintf(e.stdout, "  %s\n", n)
		}
		for _, u := range res.Untouched {
			fmt.Fprintf(e.stdout, "  untouched: %s\n", u)
		}
		if res.Draft != nil {
			e.printJSON(res.Draft)
		}
		return 0
	}
	if *dry {
		return e.withRead(ctx, "setup apply", *wsFlag, run)
	}
	return e.withWrite(ctx, "setup apply", *wsFlag, run)
}

func (e *env) setupDecision(kind string) func(context.Context, []string) int {
	return func(ctx context.Context, args []string) int {
		fs := e.flags("setup " + kind)
		wsFlag := fs.String("workspace", "", "workspace directory")
		pos, err := parseMixed(fs, args)
		if err != nil || len(pos) != 1 {
			fmt.Fprint(e.stderr, setupUsage)
			return 2
		}
		return e.withWrite(ctx, "setup "+kind, *wsFlag, func(oe *onboarding.Env) int {
			var err error
			switch kind {
			case "ack":
				err = onboarding.Ack(ctx, oe, pos[0])
			case "skip":
				err = onboarding.Skip(ctx, oe, pos[0])
			default:
				err = onboarding.Unskip(ctx, oe, pos[0])
			}
			if err != nil {
				return e.report("setup "+kind, err)
			}
			fmt.Fprintf(e.stdout, "%s %s\n", kind, pos[0])
			return 0
		})
	}
}

const cvUsage = "usage: applysmith cv import <file> [--workspace <dir>] [--answers <file|->]\n"

// cvImport saves only the confirmed CV, without opening the database or
// approving any profile facts. The workspace lock and revision check apply
// while the person reviews the draft, just as they do for other writers.
func (e *env) cvImport(ctx context.Context, args []string) int {
	const name = "cv import"
	if len(args) == 0 || args[0] != "import" {
		fmt.Fprint(e.stderr, cvUsage)
		return 2
	}
	fs := e.flags(name)
	wsFlag := fs.String("workspace", "", "workspace directory")
	answers := fs.String("answers", "", "confirmation answer, one per line, or - for standard input")
	pos, err := parseMixed(fs, args[1:])
	if err != nil || len(pos) != 1 {
		fmt.Fprint(e.stderr, cvUsage)
		return 2
	}
	if _, err := os.Stat(pos[0]); err != nil {
		return e.report(name, &onboarding.RejectedError{Message: pos[0] + ": " + errors.Unwrap(err).Error()})
	}
	if *answers == "" && !e.isTerminal() {
		return e.report(name, &onboarding.RejectedError{Message: "review requires a terminal, or --answers <file|->"})
	}
	oe := e.onboardingEnv(*wsFlag)
	if oe.ResolveErr != nil {
		return e.fail(name, oe.ResolveErr)
	}
	if oe.Workspace == nil {
		return e.report(name, &onboarding.EnvError{Message: "no workspace could be resolved"})
	}
	if _, err := os.Stat(oe.Workspace.Path(workspace.FileProfile)); err != nil {
		return e.report(name, &onboarding.EnvError{Message: "initialise the workspace with `applysmith init` first", Err: err})
	}
	lock, err := api.AcquireLock(oe.Workspace.StateDir())
	if err != nil {
		return e.report(name, &onboarding.EnvError{Message: "the workspace is in use; use the dashboard or stop its server", Err: err})
	}
	defer lock.Release()
	cur, err := oe.Workspace.Read(workspace.FileCV)
	if err != nil {
		return e.report(name, err)
	}
	text, err := onboarding.ImportCVFile(ctx, pos[0])
	if err != nil {
		return e.report(name, err)
	}
	in, closer, err := e.prompter(*answers)
	if err != nil {
		return e.report(name, &onboarding.RejectedError{Message: err.Error()})
	}
	defer closer.Close()
	fmt.Fprintf(e.stdout, "CV text to review:\n%s\n\nExtracted profile draft (not stored):\n", text)
	if code := e.printJSON(cvparse.Parse(text)); code != 0 {
		return code
	}
	answer, err := in.Ask("Save this text as cv.md? (yes/no)", "no")
	if err != nil {
		return e.report(name, err)
	}
	if a := strings.ToLower(strings.TrimSpace(answer)); a != "yes" && a != "y" {
		return e.report(name, &onboarding.RejectedError{Message: "CV import was not confirmed; no CV or profile was changed"})
	}
	if err := ctx.Err(); err != nil {
		return e.report(name, err)
	}
	if _, err := oe.Workspace.Write(workspace.FileCV, []byte(text), cur.Revision); err != nil {
		return e.report(name, err)
	}
	fmt.Fprintln(e.stdout, "Saved cv.md. Review employment, education and skills in Setup before adding them to your profile.")
	return 0
}
