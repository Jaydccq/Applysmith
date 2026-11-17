package cli

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"

	"applysmith/internal/api/trackerapi"
	"applysmith/internal/tracker"
	"applysmith/internal/workspace"
)

func (e *env) trackerOutcomes(ctx context.Context, args []string) int {
	tf := e.trackerFlags("outcomes")
	from := tf.fs.String("from", "", "first application date of the cohort (YYYY-MM-DD)")
	to := tf.fs.String("to", "", "last application date of the cohort (YYYY-MM-DD)")
	asOf := tf.fs.String("as-of", "", "observation cutoff (YYYY-MM-DD), default today")
	if !e.trackerParse(tf, "outcomes", args) {
		return 2
	}
	return e.withTracker(ctx, "tracker outcomes", *tf.ws, accessRead, func(tr *tracker.Tracker, _ *workspace.Workspace) int {
		o, err := tr.Outcomes(ctx, tracker.OutcomeQuery{From: *from, To: *to, AsOf: *asOf})
		if err != nil {
			return e.fail("tracker outcomes", err)
		}
		if *tf.asJSON {
			return e.printJSON(trackerapi.WireOutcomes(o))
		}
		printOutcomes(e, o)
		return 0
	})
}

func printOutcomes(e *env, o tracker.Outcomes) {
	fmt.Fprintf(e.stdout, "cohort: applications dated %s to %s, read as of %s\n", dateOr(o.From, "the start"), dateOr(o.To, "today"), o.AsOf)
	sections := []struct {
		title  string
		groups []tracker.OutcomeGroup
	}{{"all", []tracker.OutcomeGroup{o.Overall}}, {"by source", o.BySource}, {"by role family", o.ByRoleFamily}}
	for _, s := range sections {
		fmt.Fprintf(e.stdout, "\n%s\n", s.title)
		w := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "GROUP\tAPPLIED\tKNOWN\tUNKNOWN\tOPEN\tREPLIED\tINTERVIEW\tOFFER\tREJECTED\tNOTE")
		for _, g := range s.groups {
			key := g.Key
			if s.title != "all" && key == "" {
				key = "(not recorded)"
			}
			if s.title == "all" {
				key = "cohort"
			}
			fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d", key, g.Applications, g.Known, g.Unknown, g.Open)
			for _, st := range g.Stages {
				fmt.Fprintf(w, "\t%d/%d", st.Numerator, st.Denominator)
			}
			fmt.Fprintf(w, "\t%s\n", strings.Join(g.Insufficient, "; "))
		}
		w.Flush()
	}
	fmt.Fprintf(e.stdout, "\n%d duplicate rows folded; %d submitted rows have no application date.\nRates count applications with event data; open ones may still advance. No cause is inferred for any outcome.\n", o.Folded, o.NoApplicationDate)
}

func dateOr(d, fallback string) string {
	if d == "" {
		return fallback
	}
	return d
}
