// Package gmailapi serves the Gmail routes of the local API: the connection,
// the sync runs and health, the bounded lookups, the action inbox and the
// digests. internal/api registers the handlers.
package gmailapi

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/gmail"
	"applysmith/internal/scheduler"
	"applysmith/internal/tracker"
)

// The Gmail routes. The lookups (code, activation, reset, sign-in link) make
// their own bounded Gmail request, read no stored mail and write nothing; what
// they return is a secret and travels in the response body only. They serve the
// paired extension and the dashboard, on the loopback listener.

// Deps is what the Gmail handlers use from the server. The log and the
// background context exist only once the server has started, so they are
// read through functions.
type Deps struct {
	Stack        func() (*gmail.Stack, error)
	Tracker      func() (*tracker.Tracker, error)
	Scheduler    func() (*scheduler.Scheduler, error)
	StartPass    func(ctx context.Context, p apikit.Pass) (apikit.Reply, error)
	TrackerError func(error) error
	Notify       func(topics ...string)
	Now          func() time.Time
	Log          func() *slog.Logger
	BG           func() context.Context // ends when the server shuts down
	BGWG         *sync.WaitGroup        // background loops that touch the store
}

// Handlers serves the Gmail routes.
type Handlers struct {
	Deps
}

func New(d Deps) *Handlers { return &Handlers{Deps: d} }

// lookupFailure maps the failure of a bounded lookup onto the closed set of API
// errors. Its text never carries a code, a link or a message body.
func lookupFailure(err error) error {
	var le *gmail.LookupError
	if !errors.As(err, &le) {
		return err
	}
	switch le.Kind {
	case gmail.KindBadRequest:
		return apikit.NewErr(wire.CodeBadRequest, "%s", le.Message)
	case gmail.KindNotFound:
		return apikit.NewErr(wire.CodeNotFound, "%s", le.Message)
	}
	e := apikit.NewErr(wire.CodeUnavailable, "%s", le.Message)
	e.Retryable = le.Retryable
	if le.Reason != "" {
		e = apikit.WithReason(e, le.Reason)
	}
	return e
}

// LatestOTP serves the newest verification code mailed after since.
func (s *Handlers) LatestOTP(c *apikit.Call) (apikit.Reply, error) {
	if e := apikit.CheckSubmissionPrincipal(c.P); e != nil {
		return apikit.Reply{}, e
	}
	q := c.R.URL.Query()
	since, domain, perr := gmail.ParseOTPRequest(gmail.OTPRequest{Since: q.Get("since"), FromDomain: q.Get("fromDomain")})
	if perr != nil {
		return apikit.Reply{}, lookupFailure(perr)
	}
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	res, err := stack.Lookups.LatestCode(c.R.Context(), since, domain)
	if err != nil {
		return apikit.Reply{}, lookupFailure(err)
	}
	return apikit.Reply{Data: wire.OneTimeCode{Code: res.Code, ReceivedAt: wire.Time{Time: res.ReceivedAt}, From: res.From}}, nil
}

// AccountLink serves the activation link (reset false) or the reset link of
// one tenant host.
func (s *Handlers) AccountLink(reset bool) apikit.Handler {
	return func(c *apikit.Call) (apikit.Reply, error) {
		if e := apikit.CheckSubmissionPrincipal(c.P); e != nil {
			return apikit.Reply{}, e
		}
		q := c.R.URL.Query()
		lq, perr := gmail.ParseLinkRequest(gmail.LinkRequest{Host: q.Get("host"), SinceDays: q.Get("sinceDays"), After: q.Get("after")}, reset)
		if perr != nil {
			return apikit.Reply{}, lookupFailure(perr)
		}
		stack, err := s.Stack()
		if err != nil {
			return apikit.Reply{}, err
		}
		find := stack.Lookups.ActivationLink
		if reset {
			find = stack.Lookups.ResetLink
		}
		res, err := find(c.R.Context(), lq)
		if err != nil {
			return apikit.Reply{}, lookupFailure(err)
		}
		return apikit.Reply{Data: wire.GmailAccountLink{URL: res.URL, Host: res.Host, ReceivedAt: wire.Time{Time: res.ReceivedAt}, From: res.From}}, nil
	}
}

// LatestSigninLink serves a sign-in link for display only.
func (s *Handlers) LatestSigninLink(c *apikit.Call) (apikit.Reply, error) {
	if e := apikit.CheckSubmissionPrincipal(c.P); e != nil {
		return apikit.Reply{}, e
	}
	// A sign-in link has no sender to narrow by, so only since is read.
	since, _, perr := gmail.ParseOTPRequest(gmail.OTPRequest{Since: c.R.URL.Query().Get("since")})
	if perr != nil {
		return apikit.Reply{}, lookupFailure(perr)
	}
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	res, err := stack.Lookups.LatestSigninLink(c.R.Context(), since)
	if err != nil {
		return apikit.Reply{}, lookupFailure(err)
	}
	return apikit.Reply{Data: wire.GmailSigninLink{URL: res.URL, Host: res.Host, ReceivedAt: wire.Time{Time: res.ReceivedAt}, From: res.From,
		Subject: res.Subject, Reasons: res.Reasons, DisplayOnly: res.DisplayOnly}}, nil
}

func wireRun(r *gmail.RunRecord) wire.GmailRun {
	out := wire.GmailRun{
		ID: wire.SafeInt(r.ID), Trigger: string(r.Trigger), Status: r.Status, Mode: r.Mode, StartedAt: wire.Time{Time: r.StartedAt},
		CoverageComplete: r.CoverageComplete, FallbackUsed: r.FallbackUsed,
		FailedStage: r.FailedStage, Error: r.Error, Stages: make([]wire.GmailStage, 0, len(r.Stages)),
		Counts: wire.GmailCounts{
			Listed: wire.SafeInt(r.Counts.Listed), Fetched: wire.SafeInt(r.Counts.Fetched), Classified: wire.SafeInt(r.Counts.Classified),
			Deduplicated: wire.SafeInt(r.Counts.Deduplicated), Stored: wire.SafeInt(r.Counts.Stored), Review: wire.SafeInt(r.Counts.Review),
			Ignored: wire.SafeInt(r.Counts.Ignored), Linked: wire.SafeInt(r.Counts.Linked), Tombstoned: wire.SafeInt(r.Counts.Tombstoned),
			Failed: wire.SafeInt(r.Counts.Failed),
		},
	}
	for _, st := range r.Stages {
		out.Stages = append(out.Stages, wire.GmailStage{Stage: st.Stage, Status: st.Status, Error: st.Error})
	}
	return out
}

// Health serves the offline health read model; it makes no Gmail request.
func (s *Handlers) Health(c *apikit.Call) (apikit.Reply, error) {
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	return s.gmailHealthReply(c.R.Context(), stack)
}

func WireGmailHealth(h gmail.Health) wire.GmailHealth {
	out := wire.GmailHealth{Connected: h.Connected, Login: h.Login, LoginError: h.LoginError, Sync: h.Sync, Running: h.Running, FailedStage: h.FailedStage, Mailbox: h.Mailbox}
	if h.CursorAgeSeconds != nil {
		age := wire.SafeInt(*h.CursorAgeSeconds)
		out.CursorAgeSeconds = &age
	}
	if h.LatestRun != nil {
		run := wireRun(h.LatestRun)
		out.LatestRun = &run
	}
	return out
}

// Runs lists recent runs, newest first, at most gmail.MaxRuns.
func (s *Handlers) Runs(c *apikit.Call) (apikit.Reply, error) {
	q := apikit.QueryOf(c)
	limit := q.Integer("limit", gmail.MaxRuns)
	if q.Err == nil && limit < 1 {
		q.Fail(apikit.InvalidField("limit", "limit must be 1 or more"))
	}
	if q.Err != nil {
		return apikit.Reply{}, q.Err
	}
	limit = min(limit, gmail.MaxRuns)
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	runs, err := stack.Repo.Runs(c.R.Context(), limit)
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.GmailRunList{Runs: make([]wire.GmailRun, 0, len(runs))}
	for _, r := range runs {
		out.Runs = append(out.Runs, wireRun(r))
	}
	return apikit.Reply{Data: out}, nil
}

// Run serves one run with its stage statuses and counts.
func (s *Handlers) Run(c *apikit.Call) (apikit.Reply, error) {
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	rec, err := stack.Repo.Run(c.R.Context(), id)
	if errors.Is(err, gmail.ErrRunNotFound) {
		return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "no such run")
	}
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: wireRun(rec)}, nil
}
