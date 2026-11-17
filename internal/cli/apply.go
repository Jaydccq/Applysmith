package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"applysmith/internal/api"
	"applysmith/internal/api/wire"
)

const applyUsage = `usage: applysmith apply <url> [--submit] [--resubmit] [--workspace <dir>] [--json]
       applysmith apply scorecard [--days N]   (run totals and the recurring defects)
       applysmith apply --list <lane>[,<lane>] [--limit N] [--submit] [--workspace <dir>] [--json]

lanes: apply-now, selective
without --submit the run fills every page and stops before the final Submit; it buys no captcha solve and creates an account when the application needs one
--submit presses Submit only if every safety check passes; the flag is the confirmation
--resubmit re-sends an application recorded as unconfirmed; it cannot be combined with --list
needs the app or "applysmith serve" running for the workspace
`

// applyPoll is how often the command asks how a run is going.
var applyPoll = time.Second

// applyAPI is the running server as the command uses it.
type applyAPI interface {
	runOne(ctx context.Context, req wire.ApplyRunRequest) (wire.RunAccepted, error)
	runList(ctx context.Context, req wire.ApplyBatchRequest) (wire.ApplyBatchAccepted, error)
	run(ctx context.Context, id string) (wire.ApplyRunView, error)
	batch(ctx context.Context, id string) (wire.ApplyBatchView, error)
	// entry is the start's runs-board entry, the only record of a start that
	// waits for the apply window.
	entry(ctx context.Context, id string) (wire.RunView, error)
}

type liveApply struct {
	e   *env
	srv *liveServer
}

func (l liveApply) runOne(ctx context.Context, req wire.ApplyRunRequest) (wire.RunAccepted, error) {
	var acc wire.RunAccepted
	err := l.e.liveSend(ctx, l.srv, http.MethodPost, "/api/apply/runs", req, &acc)
	return acc, err
}

func (l liveApply) runList(ctx context.Context, req wire.ApplyBatchRequest) (wire.ApplyBatchAccepted, error) {
	var acc wire.ApplyBatchAccepted
	err := l.e.liveSend(ctx, l.srv, http.MethodPost, "/api/apply/batches", req, &acc)
	return acc, err
}

func (l liveApply) run(ctx context.Context, id string) (wire.ApplyRunView, error) {
	var v wire.ApplyRunView
	err := l.e.liveCall(ctx, l.srv, http.MethodGet, "/api/apply/runs/"+url.PathEscape(id), &v)
	return v, err
}

func (l liveApply) batch(ctx context.Context, id string) (wire.ApplyBatchView, error) {
	var v wire.ApplyBatchView
	err := l.e.liveCall(ctx, l.srv, http.MethodGet, "/api/apply/batches/"+url.PathEscape(id), &v)
	return v, err
}

func (l liveApply) entry(ctx context.Context, id string) (wire.RunView, error) {
	var v wire.RunView
	err := l.e.liveCall(ctx, l.srv, http.MethodGet, "/api/runs/"+url.PathEscape(id), &v)
	return v, err
}

// applyArgs are the parsed flags of the command.
type applyArgs struct {
	url              string
	lanes            []string
	limit            int
	submit, resubmit bool
}

// parseApplyArgs checks the arguments before any server or browser is
// touched: a re-send together with a list is a usage error.
func (e *env) parseApplyArgs(args []string, wsFlag *string, asJSON *bool) (applyArgs, bool) {
	fs := e.flagsWithUsage("apply", applyUsage)
	fs.StringVar(wsFlag, "workspace", "", "workspace directory")
	fs.BoolVar(asJSON, "json", false, "print JSON")
	list := fs.String("list", "", "lanes to run, comma separated")
	limit := fs.Int("limit", 0, "most rows to run")
	submit := fs.Bool("submit", false, "press Submit when every safety check passes")
	resubmit := fs.Bool("resubmit", false, "re-send an unconfirmed application")
	pos, err := parseMixed(fs, args)
	if err != nil {
		return applyArgs{}, false
	}
	a := applyArgs{limit: *limit, submit: *submit, resubmit: *resubmit}
	switch {
	case *list != "" && len(pos) > 0, *list == "" && len(pos) != 1, *list != "" && a.resubmit, a.resubmit && !a.submit, *list == "" && a.limit != 0:
		fmt.Fprint(e.stderr, applyUsage)
		return applyArgs{}, false
	}
	if *list != "" {
		for _, l := range strings.Split(*list, ",") {
			if l = strings.TrimSpace(l); l != wire.LaneApplyNow && l != wire.LaneSelective {
				fmt.Fprint(e.stderr, applyUsage)
				return applyArgs{}, false
			}
			a.lanes = append(a.lanes, l)
		}
	} else {
		a.url = pos[0]
	}
	return a, true
}

func (e *env) apply(ctx context.Context, args []string) int {
	if len(args) > 0 && args[0] == "scorecard" {
		return e.applyScorecard(ctx, args[1:])
	}
	var wsFlag string
	var asJSON bool
	a, ok := e.parseApplyArgs(args, &wsFlag, &asJSON)
	if !ok {
		return 2
	}
	ws, err := e.openWorkspace(wsFlag)
	if err != nil {
		return e.fail("apply", err)
	}
	live, err := api.LockHeld(ws.StateDir())
	if err != nil {
		return e.fail("apply", err)
	}
	if !live {
		return e.fail("apply", errNoServer)
	}
	srv, err := e.attachLive(ctx, ws)
	if err != nil {
		return e.fail("apply", err)
	}
	return e.runApply(ctx, liveApply{e: e, srv: srv}, a, asJSON)
}

// runApply starts the run or the list and waits for how it ended. Typing
// --submit is the explicit request the server asks to be confirmed.
func (e *env) runApply(ctx context.Context, srv applyAPI, a applyArgs, asJSON bool) int {
	if a.url != "" {
		acc, err := srv.runOne(ctx, wire.ApplyRunRequest{URL: a.url, Submit: a.submit, Confirmed: a.submit, Resubmit: a.resubmit})
		if err != nil {
			return e.fail("apply", err)
		}
		if err := e.awaitQueued(ctx, srv, acc.RunID, acc.Ahead); err != nil {
			return e.fail("apply", err)
		}
		v, err := e.awaitRun(ctx, srv, acc.RunID)
		if err != nil {
			return e.fail("apply", err)
		}
		return e.printRun(v, asJSON)
	}
	acc, err := srv.runList(ctx, wire.ApplyBatchRequest{Lanes: a.lanes, Limit: wire.SafeInt(a.limit), Submit: a.submit, Confirmed: a.submit})
	if err != nil {
		return e.fail("apply", err)
	}
	for _, s := range acc.Skipped {
		fmt.Fprintf(e.stderr, "left for you: %s\n", s.Reason)
	}
	if err := e.awaitQueued(ctx, srv, acc.RunID, acc.Ahead); err != nil {
		return e.fail("apply", err)
	}
	return e.awaitBatch(ctx, srv, acc, asJSON)
}

// awaitQueued waits while a start waits for the apply window. A start that
// ended without a run, cancelled or with no row able to start, is an error
// naming why.
func (e *env) awaitQueued(ctx context.Context, srv applyAPI, id string, ahead wire.SafeInt) error {
	if ahead == 0 {
		return nil
	}
	fmt.Fprintf(e.stderr, "apply: queued (%d ahead); it starts when the apply window is free\n", ahead)
	var quiet silence
	for {
		v, err := srv.entry(ctx, id)
		if !quiet.tolerates(ctx, e, err) {
			return err
		}
		if b := v.Entry; err == nil && (b == nil || b.Stage != wire.BoardStageQueued) {
			return nil
		} else if err == nil && b.State != "working" {
			return errors.New("the queued start ended before it ran: " + b.Detail)
		}
		select {
		case <-ctx.Done():
			return errors.New("interrupted; the start stays queued in the app")
		case <-time.After(applyPoll):
		}
	}
}

func (e *env) awaitRun(ctx context.Context, srv applyAPI, id string) (wire.ApplyRunView, error) {
	var quiet silence
	shown := map[string]string{} // the status of each stage already named on stderr
	for {
		v, err := srv.run(ctx, id)
		if !quiet.tolerates(ctx, e, err) {
			return v, err
		}
		if err == nil && v.FinishedAt != nil {
			return v, nil
		}
		for _, s := range v.Stages {
			if err == nil && shown[s.Stage] != s.Status {
				shown[s.Stage] = s.Status
				fmt.Fprintln(e.stderr, strings.TrimSpace(fmt.Sprintf("apply: %s %s %s", s.Stage, s.Status, s.Detail)))
			}
		}
		select {
		case <-ctx.Done():
			return v, errors.New("interrupted; the run keeps working in the app")
		case <-time.After(applyPoll):
		}
	}
}

func (e *env) awaitBatch(ctx context.Context, srv applyAPI, acc wire.ApplyBatchAccepted, asJSON bool) int {
	var quiet silence
	done := map[wire.SafeInt]bool{} // the rows whose outcome is already named on stderr
	for {
		v, err := srv.batch(ctx, acc.BatchID)
		if !quiet.tolerates(ctx, e, err) {
			return e.fail("apply", err)
		}
		if err == nil && v.State == wire.BatchClosed {
			return e.printBatch(v, asJSON)
		}
		for _, r := range v.Rows {
			if err == nil && r.Outcome != nil && !done[r.Index] {
				done[r.Index] = true
				fmt.Fprintf(e.stderr, "apply: %d/%d %s: %s\n", r.Index+1, len(v.Rows), r.Company, outcomeLine(*r.Outcome))
			}
		}
		select {
		case <-ctx.Done():
			return e.fail("apply", errors.New("interrupted; the list keeps working in the app"))
		case <-time.After(applyPoll):
		}
	}
}

func (e *env) printRun(v wire.ApplyRunView, asJSON bool) int {
	if asJSON {
		if e.jsonOut(v) != nil {
			return 1
		}
	} else {
		fmt.Fprintf(e.stdout, "%s %s: %s\n", v.Host, v.Mode, outcomeLine(v.Outcome))
		for _, s := range v.Stages {
			fmt.Fprintf(e.stdout, "  %-9s %-8s %5d ms  %s\n", s.Stage, s.Status, s.Ms, s.Detail)
		}
		if v.Mode == wire.ApplyModeDryRun {
			fmt.Fprintln(e.stdout, "Nothing was submitted.")
			if v.KeptTab && v.Outcome.Kind == "filled" {
				fmt.Fprintf(e.stdout, "The filled form waits at Submit in its kept tab in the app. To submit it, open run %s on the Runs page of the dashboard and press Submit this form.\n", v.RunID)
			}
		}
	}
	if v.Outcome.Kind == "stopped" {
		return 1
	}
	return 0
}

func (e *env) printBatch(v wire.ApplyBatchView, asJSON bool) int {
	bad := 0
	for _, r := range v.Rows {
		if r.Outcome != nil && r.Outcome.Kind == "stopped" {
			bad++
		}
	}
	if asJSON {
		if e.jsonOut(v) != nil {
			return 1
		}
	} else {
		for _, r := range v.Rows {
			line := r.Note
			if r.Outcome != nil {
				line = outcomeLine(*r.Outcome)
			}
			fmt.Fprintf(e.stdout, "%d. %s (%s): %s\n", r.Index+1, r.Company, r.Host, line)
		}
		for _, g := range v.Owners {
			fmt.Fprintf(e.stdout, "%s: %s\n", g.Owner, strings.Join(g.Companies, ", "))
		}
	}
	if bad > 0 {
		return 1
	}
	return 0
}

func outcomeLine(o wire.ApplyOutcome) string {
	switch o.Kind {
	case "stopped":
		return fmt.Sprintf("stopped at %s (%s): %s", o.Stage, o.Class, o.Reason)
	case "submitted":
		if !o.Confirmed {
			return "submitted; the confirmation is not read yet"
		}
	}
	return o.Kind
}
