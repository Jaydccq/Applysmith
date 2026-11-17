package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"applysmith/internal/api"
	"applysmith/internal/api/wire"
	"applysmith/internal/apply"
)

const autofillUsage = `usage: applysmith autofill [--workspace <dir>] [--json]

fills the tab the apply window shows, once: it writes the answers it holds, checks the page, and stops
it never navigates, presses Next, Continue, Review or Submit, or closes the tab; the filled form is left to you
the command waits for the result and prints how many controls were filled, left for review, missing or skipped
needs the app or "applysmith serve" running for the workspace
`

// fillPoll is how often the command asks for the result.
var fillPoll = 500 * time.Millisecond

// fillAPI is the running server as the command uses it.
type fillAPI interface {
	start(ctx context.Context) (runID string, err error)
	result(ctx context.Context, runID string) (wire.AutofillTabResult, error)
}

type liveFill struct {
	e   *env
	srv *liveServer
}

func (l liveFill) start(ctx context.Context) (string, error) {
	var acc wire.RunAccepted
	err := l.e.liveCall(ctx, l.srv, http.MethodPost, "/api/autofill/active-tab", &acc)
	return acc.RunID, err
}

func (l liveFill) result(ctx context.Context, id string) (wire.AutofillTabResult, error) {
	var res wire.AutofillTabResult
	err := l.e.liveCall(ctx, l.srv, http.MethodGet, "/api/autofill/active-tab/"+id, &res)
	return res, err
}

func (e *env) autofill(ctx context.Context, args []string) int {
	fs := e.flagsWithUsage("autofill", autofillUsage)
	wsFlag := fs.String("workspace", "", "workspace directory")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseMixed(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) > 0 {
		fmt.Fprint(e.stderr, autofillUsage)
		return 2
	}
	ws, err := e.openWorkspace(*wsFlag)
	if err != nil {
		return e.fail("autofill", err)
	}
	live, err := api.LockHeld(ws.StateDir())
	if err != nil {
		return e.fail("autofill", err)
	}
	if !live {
		return e.fail("autofill", errNoServer)
	}
	srv, err := e.attachLive(ctx, ws)
	if err != nil {
		return e.fail("autofill", err)
	}
	return e.runAutofill(ctx, liveFill{e: e, srv: srv}, *asJSON)
}

// runAutofill starts the fill and waits for its result. The server bounds a
// fill at the page deadline, so the wait is that and a margin.
func (e *env) runAutofill(ctx context.Context, srv fillAPI, asJSON bool) int {
	id, err := srv.start(ctx)
	if err != nil {
		return e.fail("autofill", err)
	}
	ctx, cancel := context.WithTimeout(ctx, apply.FillDeadline+30*time.Second)
	defer cancel()
	var res wire.AutofillTabResult
	var quiet silence
	for {
		res, err = srv.result(ctx, id)
		if !quiet.tolerates(ctx, e, err) {
			return e.fail("autofill", err)
		}
		if err == nil && res.Status != wire.RunRunning {
			break
		}
		select {
		case <-ctx.Done():
			return e.fail("autofill", errors.New("the fill did not finish in time; the tab is as it was left"))
		case <-time.After(fillPoll):
		}
	}
	if asJSON {
		if e.jsonOut(res) != nil {
			return 1
		}
	} else {
		e.printFill(res)
	}
	if res.Status != wire.RunSucceeded {
		return 1
	}
	return 0
}

func (e *env) printFill(r wire.AutofillTabResult) {
	if r.Status != wire.RunSucceeded {
		fmt.Fprintf(e.stderr, "applysmith autofill: the fill %s: %s\n", r.Status, r.Reason)
		return
	}
	fmt.Fprintf(e.stdout, "%s\nfilled %d, review %d, missing %d, skipped %d\n", r.URL, len(r.Filled), len(r.Review), len(r.Missing), len(r.Skipped))
	for _, g := range []struct {
		name  string
		items []wire.AutofillControl
	}{{"review", r.Review}, {"missing", r.Missing}, {"skipped", r.Skipped}} {
		for _, c := range g.items {
			line := g.name + ": " + c.Label
			if c.Reason != "" {
				line += " (" + c.Reason + ")"
			}
			fmt.Fprintln(e.stdout, "  "+strings.TrimSpace(line))
		}
	}
	fmt.Fprintf(e.stdout, "drafts awaiting review %d, flagged values %d, learning events %d\n", r.Drafts, r.Flagged, r.Events)
	fmt.Fprintln(e.stdout, "Nothing was submitted; the tab and its form are left to you.")
}
