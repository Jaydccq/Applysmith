package cli

import (
	"context"
	"fmt"
	"time"

	"applysmith/internal/apply"
	"applysmith/internal/workspace"
)

const scorecardUsage = `usage: applysmith apply scorecard [--days N] [--workspace <dir>] [--json]

prints the apply runs per UTC day and for the window: runs and minutes, submits and confirmations,
submit time p50 and p90, repeat runs, outcomes by class, page timings, model calls and the recurring defects
reads the database only; it works with the app stopped
`

func (e *env) applyScorecard(ctx context.Context, args []string) int {
	fs := e.flags("apply scorecard")
	wsFlag := fs.String("workspace", "", "workspace directory")
	days := fs.Int("days", 7, "window length in UTC days")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) > 0 || *days < 1 || *days > 366 {
		fmt.Fprint(e.stderr, scorecardUsage)
		return 2
	}
	ws, err := e.openWorkspace(*wsFlag)
	if err != nil {
		return e.fail("apply", err)
	}
	_, st, release, err := e.openStore(ctx, ws.Root, accessRead)
	if err != nil {
		return e.fail("apply", err)
	}
	defer release()
	saved := apply.NewestModTime(ws.Path(workspace.FileAnswerBank), ws.Path(workspace.FileProfile), ws.Path(workspace.FileCV))
	at, err := saved(ctx)
	if err != nil {
		return e.fail("apply", err)
	}
	card, err := apply.LoadScorecard(ctx, st, *days, time.Now(), at)
	if err != nil {
		return e.fail("apply", err)
	}
	if *asJSON {
		if e.jsonOut(card) != nil {
			return 1
		}
		return 0
	}
	for _, d := range append(card.Days, card.Total) {
		fmt.Fprintf(e.stdout, "%-10s runs %d (%.1f min), submitted %d, confirmed %d, repeat runs %d\n", d.Day, d.Runs, d.RunMinutes, d.Submitted, d.Confirmed, d.RepeatRuns)
	}
	for _, c := range card.Clusters {
		fmt.Fprintf(e.stdout, "defect: %s on %s, %d runs on %d postings, %d open: %s\n", c.Stage, c.System, c.Runs, c.Postings, c.Open, c.Shape)
	}
	return 0
}
