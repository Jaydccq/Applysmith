package release

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Print writes the step table, the metrics table and the pending items.
func (r Report) Print(w io.Writer) {
	fmt.Fprintf(w, "\nrelease-check steps (export: %s)\n", r.Export)
	for _, s := range r.Steps {
		first, rest, _ := strings.Cut(s.Detail, "\n")
		fmt.Fprintf(w, "  [%-4s] %-17s %6.1fs  %s\n", s.Status, s.Name, s.Elapsed.Seconds(), first)
		if rest != "" {
			for _, l := range strings.Split(rest, "\n") {
				fmt.Fprintf(w, "         %s\n", l)
			}
		}
	}
	fmt.Fprintln(w, "\nmetrics (exported tree)")
	rows := [][2]string{}
	if m := r.Metrics; m != nil {
		rows = append(rows,
			[2]string{"tracked files", strconv.Itoa(m.Files)},
			[2]string{"tracked bytes", strconv.FormatInt(m.Bytes, 10)},
			[2]string{"Go lines", strconv.Itoa(m.GoLines)},
			[2]string{"TypeScript lines", strconv.Itoa(m.TSLines)},
			[2]string{"direct Go dependencies", strconv.Itoa(m.GoDirectDeps)},
			[2]string{"dashboard runtime dependencies", strconv.Itoa(m.DashboardDeps)},
		)
	}
	app := "not measured"
	if r.Package != nil {
		app = fmt.Sprintf("%s (limit %s)", humanBytes(r.Package.Bytes), humanBytes(maxAppBytes))
	}
	rows = append(rows, [2]string{"packaged app size", app})
	for _, row := range rows {
		fmt.Fprintf(w, "  %-32s %s\n", row[0], row[1])
	}
	if len(r.Excluded) > 0 {
		fmt.Fprintf(w, "\nclass B paths left out of the export: %d\n", len(r.Excluded))
	}
	if r.Package != nil && len(r.Package.Pending) > 0 {
		fmt.Fprintln(w, "\npending (not failures)")
		for _, p := range r.Package.Pending {
			fmt.Fprintf(w, "  %s\n", p)
		}
	}
	if r.Failed() {
		fmt.Fprintln(w, "\nrelease-check: FAILED")
	} else {
		fmt.Fprintln(w, "\nrelease-check: passed")
	}
}
