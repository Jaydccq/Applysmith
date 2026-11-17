package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/discovery"
	"applysmith/internal/evaluate"
	"applysmith/internal/llm"
	"applysmith/internal/store"
)

const maxPostingText = 200_000

// sourceLabel names the ingress of an evaluation request; the body may refine
// it but the label always starts from who authenticated.
func sourceLabel(p apikit.Principal) string {
	if p.Kind == apikit.PrincipalDashboard {
		return "dashboard"
	}
	return "api"
}

func (s *Server) createEvaluation(c *apikit.Call) (apikit.Reply, error) {
	key, e := apikit.IdempotencyKey(c)
	if e != nil {
		return apikit.Reply{}, e
	}
	var w wire.EvaluationRequest
	if e := apikit.Decode(c.Body, &w); e != nil {
		return apikit.Reply{}, e
	}
	req := evaluate.Request{
		URL: strings.TrimSpace(w.URL), Text: strings.TrimSpace(w.Text), Title: strings.TrimSpace(w.Title), Company: strings.TrimSpace(w.Company),
		Location: strings.TrimSpace(w.Location), Compensation: strings.TrimSpace(w.Compensation), Source: strings.TrimSpace(w.Source),
		InboxRowID: int64(w.InboxRowID), Mode: evaluate.Mode(w.Mode), Force: w.Force, Priority: evaluate.PriorityUser,
	}
	if req.Source == "" {
		req.Source = sourceLabel(c.P)
	}
	if len(req.Text) > maxPostingText {
		return apikit.Reply{}, apikit.InvalidField("text", "the posting text holds at most %d characters", maxPostingText)
	}
	if req.URL != "" {
		if e := apikit.CheckPostingURL(req.URL); e != nil {
			return apikit.Reply{}, e
		}
	}
	if w.InboxRowID < 0 {
		return apikit.Reply{}, apikit.InvalidField("inboxRowId", "inboxRowId must be a positive whole number")
	}
	return s.submitEvaluation(c, key, req)
}

// retryEvaluation starts a failed or cancelled evaluation again with the
// request it was started with (the posting, its pasted text, the hints, the
// mode and the inbox row), as the user's own start: the scan or inbox drain
// that queued it is not named again.
func (s *Server) retryEvaluation(c *apikit.Call) (apikit.Reply, error) {
	key, e := apikit.IdempotencyKey(c)
	if e != nil {
		return apikit.Reply{}, e
	}
	st, err := s.db()
	if err != nil {
		return apikit.Reply{}, err
	}
	var status, input string
	err = st.Read().QueryRowContext(c.R.Context(), `SELECT status, input_json FROM runs WHERE id = ? AND kind = 'evaluate'`, c.R.PathValue("id")).Scan(&status, &input)
	if errors.Is(err, sql.ErrNoRows) {
		return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "no such evaluation")
	}
	if err != nil {
		return apikit.Reply{}, err
	}
	if status != wire.RunFailed && status != wire.RunCancelled {
		return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "only a failed or cancelled evaluation can be retried")
	}
	var req evaluate.Request
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		return apikit.Reply{}, err
	}
	req.ScanRun, req.Drain, req.Review, req.Priority = "", "", "", evaluate.PriorityUser
	return s.submitEvaluation(c, key, req)
}

// evaluationModelMissing refuses a press that starts an evaluation while an
// evaluation task has no model: the run would only fail at no-route, one
// failed run per posting pressed.
func evaluationModelMissing(ctx context.Context, d *domain) error {
	if task := d.router.EvaluationUnrouted(ctx); task != "" {
		return apikit.WithReason(apikit.Unavailable(llm.NoModel("the "+task+" task")), wire.ReasonNoModelRoute)
	}
	return nil
}

// submitEvaluation queues a checked request under the caller's key.
func (s *Server) submitEvaluation(c *apikit.Call, key string, req evaluate.Request) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	var row *discovery.InboxRow
	if req.InboxRowID > 0 {
		r, err := d.repo.InboxGet(c.R.Context(), req.InboxRowID)
		if err != nil {
			return apikit.Reply{}, inboxError(err)
		}
		row = &r
		req.FillFromInbox(r)
	}
	if _, err := evaluate.NormalizeRequest(req); err != nil {
		field := "url"
		if strings.Contains(err.Error(), "mode") {
			field = "mode"
		}
		return apikit.Reply{}, apikit.InvalidField(field, "%s", strings.TrimPrefix(err.Error(), "evaluate: "))
	}
	if err := evaluationModelMissing(c.R.Context(), d); err != nil {
		return apikit.Reply{}, err
	}
	var h evaluate.Handle
	submit := func() (string, error) {
		var err error
		h, err = d.pool.SubmitKeyed(c.R.Context(), evaluate.Claim{Principal: c.P.ID, Key: key}, req)
		return h.RunID, err
	}
	handled, err := s.queueInboxRow(c, d, row, submit)
	if !handled {
		_, err = submit()
	}
	switch {
	case errors.Is(err, evaluate.ErrQueueFull):
		qe := apikit.Unavailable("the evaluation queue is full; retry shortly")
		qe.Details = &wire.ErrorDetails{Reason: wire.ReasonQueueFull}
		return apikit.Reply{}, qe
	case errors.Is(err, store.ErrConflict):
		return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "this Idempotency-Key was used with a different request")
	case err != nil:
		return apikit.Reply{}, err
	}
	if !h.Replayed {
		s.notify("runs")
	}
	return apikit.Reply{Status: http.StatusAccepted, Location: "/api/runs/" + h.RunID, Data: wire.RunAccepted{RunID: h.RunID}}, nil
}

func (s *Server) getEvaluation(c *apikit.Call) (apikit.Reply, error) {
	st, err := s.db()
	if err != nil {
		return apikit.Reply{}, err
	}
	id := c.R.PathValue("id")
	res, phase, found, err := evaluate.JobOutcome(c.R.Context(), st, id)
	if err != nil {
		return apikit.Reply{}, err
	}
	if !found {
		return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "no such evaluation")
	}
	run, err := st.GetRun(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: WireEvaluation(res, run.Status, phase)}, nil
}

// WireEvaluation is the view of an evaluation run, shared with the CLI.
func WireEvaluation(res evaluate.Result, status, phase string) wire.EvaluationView {
	view := wire.EvaluationView{
		RunID: res.RunID, Status: status, Phase: phase, Outcome: string(res.Outcome), Reason: res.Reason, Explanation: res.Explanation,
		Decision: res.Decision, Review: res.Review, Score: res.Score, Recommendation: res.Recommendation,
		ReviewReason: evaluate.ReviewNote(res.ReviewReason), PostingURL: res.PostingURL,
	}
	if res.ReportNumber > 0 {
		n := apikit.Safe64(res.ReportNumber)
		view.ReportNumber = &n
	}
	if res.InboxRowID > 0 {
		n := apikit.Safe64(res.InboxRowID)
		view.InboxRowID = &n
	}
	return view
}

// queueInboxRow submits an evaluation that names a pending inbox row: the row
// is gated and queued first, as a promotion does, so the finished evaluation
// can move it on. handled is false when the request names no pending row and
// the caller submits it as it is; otherwise err is why the request was refused
// or the error submit returned.
func (s *Server) queueInboxRow(c *apikit.Call, d *domain, row *discovery.InboxRow, submit func() (string, error)) (handled bool, err error) {
	if row == nil || row.Status != discovery.InboxPending {
		return false, nil
	}
	profile, e := loadProfile(s.cfg.Workspace)
	if e != nil {
		return true, e
	}
	gate := evaluate.ScannerGate(profile, evaluate.BlockedFromDiscovery(d.repo))
	_, rule, err := d.repo.QueueForEvaluation(c.R.Context(), *row, gate, submit)
	if rule != "" {
		return true, apikit.WithReason(apikit.InvalidField("inboxRowId", "the eligibility gate keeps this row out of evaluation: %s", rule), "gate-rejected")
	}
	return true, err
}

// submitByURL submits an evaluation the Ask Agent or Telegram starts by URL.
// When an inbox row waits with that posting, the request names the row and
// queues it first, as an Evaluate press does but without the gate the user's
// own request overrides, so the finished evaluation moves the row on.
func (s *Server) submitByURL(ctx context.Context, d *domain, claim evaluate.Claim, req evaluate.Request) (evaluate.Handle, error) {
	var h evaluate.Handle
	submit := func() (string, error) {
		var err error
		h, err = d.pool.SubmitKeyed(ctx, claim, req)
		return h.RunID, err
	}
	if row, ok, err := d.repo.InboxRowFor(ctx, req.URL); err == nil && ok && row.Status == discovery.InboxPending {
		req.FillFromInbox(row)
		_, _, err := d.repo.QueueForEvaluation(ctx, row, nil, submit)
		if !errors.Is(err, discovery.ErrBadTransition) {
			s.notify("scans")
			return h, err
		}
		req.InboxRowID = 0 // the row left waiting meanwhile
	}
	_, err := submit()
	return h, err
}
