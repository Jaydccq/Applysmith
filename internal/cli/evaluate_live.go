package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"applysmith/internal/api/wire"
	"applysmith/internal/evaluate"
	"applysmith/internal/workspace"
)

// evaluatePoll is how often the command asks how an evaluation is going.
var evaluatePoll = 500 * time.Millisecond

// evaluateLive runs the evaluation in the server that holds the workspace. The
// command cannot start a worker pool of its own beside a server: starting one
// ends every evaluation the server is running as interrupted.
func (e *env) evaluateLive(ctx context.Context, ws *workspace.Workspace, req evaluate.Request, rowID int64, asJSON bool) int {
	srv, err := e.attachLive(ctx, ws)
	if err != nil {
		return e.fail("evaluate", err)
	}
	if req.Source == "" {
		req.Source = "cli"
	}
	var key [12]byte
	_, _ = rand.Read(key[:])
	var acc wire.RunAccepted
	err = e.liveSend(ctx, srv, http.MethodPost, "/api/evaluations", wire.EvaluationRequest{
		URL: req.URL, Text: req.Text, Title: req.Title, Company: req.Company, Location: req.Location, Compensation: req.Compensation,
		Source: req.Source, InboxRowID: wire.SafeInt(rowID), Mode: string(req.Mode), Force: req.Force,
	}, &acc, "Idempotency-Key", "cli-"+hex.EncodeToString(key[:]))
	if err != nil {
		return e.fail("evaluate", err)
	}
	view, err := e.awaitEvaluation(ctx, srv, acc.RunID)
	if err != nil {
		return e.fail("evaluate", err)
	}
	res := evaluate.Result{RunID: view.RunID, Outcome: evaluate.Outcome(view.Outcome), Reason: view.Reason, Decision: view.Decision,
		Explanation: view.Explanation, Review: view.Review}
	if view.ReportNumber != nil {
		res.ReportNumber = int64(*view.ReportNumber)
		var rep wire.ReportDetail
		if err := e.liveCall(ctx, srv, http.MethodGet, fmt.Sprintf("/api/reports/%d", res.ReportNumber), &rep); err != nil {
			return e.fail("evaluate", err)
		}
		res.Score, res.Recommendation = rep.Report.Score, rep.Report.Recommendation
	}
	// The server made the read; what stopped its browser is not seen here.
	return e.printEvaluation(res, ws.Root, asJSON, nil)
}

// awaitEvaluation polls the job until it has an outcome, naming each phase it
// passes on stderr as the local command does.
func (e *env) awaitEvaluation(ctx context.Context, srv *liveServer, runID string) (wire.EvaluationView, error) {
	phase := ""
	var quiet silence
	for {
		var v wire.EvaluationView
		err := e.liveCall(ctx, srv, http.MethodGet, "/api/evaluations/"+url.PathEscape(runID), &v)
		if !quiet.tolerates(ctx, e, err) {
			return v, err
		}
		if err == nil {
			if v.Phase != "" && v.Phase != phase {
				phase = v.Phase
				fmt.Fprintf(e.stderr, "evaluation 1: %s\n", phase)
			}
			if v.Outcome != "" {
				return v, nil
			}
		}
		select {
		case <-ctx.Done():
			return v, errors.New("interrupted; the evaluation keeps working in the server")
		case <-time.After(evaluatePoll):
		}
	}
}

// scanLive runs the scan in the server that holds the workspace, as
// evaluateLive does, and prints its record when the run ends. The evaluations
// the scan queued go on in the server.
func (e *env) scanLive(ctx context.Context, ws *workspace.Workspace, req wire.ScanRequest, asJSON bool) int {
	fail := func(err error) int { return e.fail("scan "+req.Source, err) }
	srv, err := e.attachLive(ctx, ws)
	if err != nil {
		return fail(err)
	}
	var key [12]byte
	_, _ = rand.Read(key[:])
	var acc wire.RunAccepted
	if err := e.liveSend(ctx, srv, http.MethodPost, "/api/scans", req, &acc, "Idempotency-Key", "cli-"+hex.EncodeToString(key[:])); err != nil {
		return fail(err)
	}
	fmt.Fprintf(e.stderr, "scan %s: running in the app; its evaluations go on there\n", req.Source)
	var quiet silence
	var run wire.RunView
	for run.FinishedAt == nil {
		select {
		case <-ctx.Done():
			return fail(errors.New("interrupted; the scan keeps working in the app"))
		case <-time.After(evaluatePoll):
		}
		err := e.liveCall(ctx, srv, http.MethodGet, "/api/runs/"+url.PathEscape(acc.RunID), &run)
		if !quiet.tolerates(ctx, e, err) {
			return fail(err)
		}
	}
	var list wire.ScanList
	if err := e.liveCall(ctx, srv, http.MethodGet, "/api/scans?limit=20&source="+url.QueryEscape(req.Source), &list); err != nil {
		return fail(err)
	}
	for _, rec := range list.Scans {
		if rec.RunID == acc.RunID {
			return e.printScan(rec, asJSON)
		}
	}
	return fail(fmt.Errorf("the scan ended %s: %s", run.Status, run.FailureReason))
}
