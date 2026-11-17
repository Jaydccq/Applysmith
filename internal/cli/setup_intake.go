package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"applysmith/internal/api/wire"
	"applysmith/internal/onboarding"
)

func (e *env) setupIntake(ctx context.Context, args []string) int {
	if len(args) == 0 || (args[0] != "preview" && args[0] != "apply") {
		fmt.Fprint(e.stderr, setupUsage)
		return 2
	}
	apply := args[0] == "apply"
	name := "setup intake " + args[0]
	fs := e.flags(name)
	wsFlag := fs.String("workspace", "", "workspace directory")
	asJSON := fs.Bool("json", false, "print JSON")
	revision := fs.String("revision", "", "the revision the preview reported (apply)")
	selectFile := fs.String("select", "", "JSON list of selections, a path or - for standard input (apply)")
	files, err := parseMixed(fs, args[1:])
	if err != nil || len(files) == 0 {
		fmt.Fprint(e.stderr, setupUsage)
		return 2
	}
	var sels []wire.IntakeSelection
	if apply {
		raw, err := e.readInput(*selectFile)
		if err != nil || *revision == "" || len(raw) == 0 {
			return e.report(name, &onboarding.RejectedError{Message: "apply needs --revision from the preview and --select with the selections"})
		}
		if err := json.Unmarshal(raw, &sels); err != nil {
			return e.report(name, &onboarding.RejectedError{Message: "--select must be a JSON list of selections: " + err.Error()})
		}
	}
	run := func(oe *onboarding.Env) int {
		docs, err := onboarding.LoadIntakeFiles(oe.Workspace, files)
		if err != nil {
			return e.report(name, err)
		}
		if apply {
			res, err := onboarding.IntakeAccept(ctx, oe, wire.IntakeAcceptRequest{Revision: *revision, Documents: docs, Selections: sels})
			if err != nil {
				return e.report(name, err)
			}
			if *asJSON {
				return e.printJSON(res)
			}
			fmt.Fprintf(e.stdout, "applied %d items; profile.yml %s (revision %s)\n", len(res.Applied), map[bool]string{true: "changed", false: "unchanged"}[res.Wrote], res.Revision)
			return 0
		}
		pv, err := onboarding.IntakePreview(ctx, oe, docs)
		if err != nil {
			return e.report(name, err)
		}
		if *asJSON {
			return e.printJSON(pv)
		}
		e.printIntake(pv)
		return 0
	}
	if apply {
		return e.withWrite(ctx, name, *wsFlag, run)
	}
	return e.withRead(ctx, name, *wsFlag, run)
}

func (e *env) printIntake(pv wire.IntakePreview) {
	fmt.Fprintf(e.stdout, "profile revision %s\n", pv.Revision)
	for _, d := range pv.Documents {
		fmt.Fprintf(e.stdout, "document %s (%s) %s\n", d.Label, d.Kind, d.Hash[:12])
	}
	for _, it := range pv.Items {
		fmt.Fprintf(e.stdout, "%-9s %s %s: %s\n", it.Type, it.ID, it.Kind, it.Summary)
		if it.Question != "" {
			fmt.Fprintf(e.stdout, "          %s\n", it.Question)
		}
		for _, f := range it.Fields {
			if f.Profile != "" {
				fmt.Fprintf(e.stdout, "          %s: profile has %q (choice %q keeps it)\n", f.Field, f.Profile, "keep")
			}
			for _, o := range f.Options {
				fmt.Fprintf(e.stdout, "          %s: %q from %s\n", f.Field, o.Value, intakeSources(o.Sources))
			}
		}
		if len(it.Fields) == 0 {
			fmt.Fprintf(e.stdout, "          from %s\n", intakeSources(it.Sources))
		}
		if len(it.NeedsMonth) > 0 {
			fmt.Fprintf(e.stdout, "          needs a month for: %s\n", strings.Join(it.NeedsMonth, ", "))
		}
	}
	for _, n := range pv.Notes {
		fmt.Fprintf(e.stdout, "note: %s\n", n)
	}
}

func intakeSources(ss []wire.IntakeSource) string {
	parts := make([]string, len(ss))
	for i, s := range ss {
		parts[i] = fmt.Sprintf("%s %s [%s, %s]", s.Label, s.Locator, s.Hash[:12], s.Origin)
	}
	return strings.Join(parts, "; ")
}
