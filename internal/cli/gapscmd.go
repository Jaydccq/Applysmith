package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"

	"applysmith/internal/api"
	"applysmith/internal/evaluate"
)

const gapsUsage = `usage:
  applysmith evaluate gaps --report <number>[,<number>...] [--report <number>...] [--json]

analyzes the saved requirement evidence of 1 to 100 final reports; confirming gaps and planning study is done on the Reports page
`

// reportNumbers reads the repeatable --report values, each a number or a
// comma-separated list.
func reportNumbers(values []string) ([]int64, error) {
	var out []int64
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			n, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("%q is not a report number", part)
			}
			out = append(out, n)
		}
	}
	return out, nil
}

func (e *env) evaluateGaps(ctx context.Context, args []string) int {
	fs := e.flags("evaluate gaps")
	ws := fs.String("workspace", "", "workspace directory")
	asJSON := fs.Bool("json", false, "print JSON")
	var reports stringList
	fs.Var(&reports, "report", "a report number, repeatable or comma-separated")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 || len(reports) == 0 {
		fmt.Fprint(e.stderr, gapsUsage)
		return 2
	}
	nums, err := reportNumbers(reports)
	if err != nil {
		return e.fail("evaluate gaps", &usageError{err.Error()})
	}
	root, err := e.workspacePath(*ws)
	if err != nil {
		return e.fail("evaluate gaps", err)
	}
	_, st, release, err := e.openStore(ctx, root, accessShared)
	if err != nil {
		return e.fail("evaluate gaps", err)
	}
	defer release()
	g := evaluate.NewGaps(st, root, evaluate.GapOptions{})
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return e.fail("evaluate gaps", err)
	}
	id, _, err := g.Start(ctx, "cli", "cli-"+hex.EncodeToString(raw[:]), nums)
	if err != nil {
		return e.fail("evaluate gaps", err)
	}
	rep, err := g.Get(ctx, id)
	if err != nil {
		return e.fail("evaluate gaps", err)
	}
	if *asJSON {
		return e.printJSON(api.WireGapReport(rep))
	}
	printGaps(e, rep)
	return 0
}

func printGaps(e *env, r evaluate.GapReport) {
	fmt.Fprintf(e.stdout, "analysis %s, %d report(s) analyzed, %d excluded\n", r.ID, len(r.Sample), len(r.Excluded))
	for _, x := range r.Excluded {
		fmt.Fprintf(e.stdout, "  report %d excluded: %s\n", x.Report, x.Reason)
	}
	w := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SKILL\tSTATE\tREQUIRED\tPREFERRED")
	for _, g := range r.Gaps {
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\n", g.Label, g.State, g.Required, g.Preferred)
	}
	w.Flush()
	fmt.Fprintf(e.stdout, "%d inferred signal(s) are listed in the JSON output and not counted.\nAbsence from the CV is not-evidenced, not proof you lack the skill; confirm a gap on the Reports page before planning to learn it.\n", len(r.Inferred))
}
