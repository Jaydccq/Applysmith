package gmailapi

import (
	"context"
	"errors"
	"net/http"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/gmail"
)

// Sync starts a manual sync in the background. A sync can outlast a
// request, so the page reads its progress from the health read model.
func (s *Handlers) Sync(c *apikit.Call) (apikit.Reply, error) {
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	if stack.Syncer.Running() {
		return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "%s", gmail.ErrAlreadyRunning.Error())
	}
	s.BGWG.Add(1)
	go func() {
		defer s.BGWG.Done()
		if _, err := stack.Syncer.Run(s.BG(), gmail.TriggerManual); err != nil && !errors.Is(err, gmail.ErrAlreadyRunning) {
			s.Log().Warn("manual gmail sync did not run", "error", err.Error())
		}
	}()
	return apikit.Reply{Status: http.StatusAccepted, Data: wire.GmailSyncStarted{Started: true}}, nil
}

// Connect signs in to Gmail with an address and its Google app password and
// stores them only once Gmail accepts them; the watcher starts over with them
// and the user's paused sync schedules come back. A refusal names a field,
// never a value.
func (s *Handlers) Connect(c *apikit.Call) (apikit.Reply, error) {
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.GmailConnect
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	res, err := stack.Service.Connect(c.R.Context(), req.Address, req.Password)
	if err != nil {
		return apikit.Reply{}, signInFailure(err, true)
	}
	stack.Watcher.Wake()
	s.resumeGmailSync()
	s.Notify("gmail", "setup")
	h, err := stack.Service.Health(c.R.Context())
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: wire.GmailConnected{Previous: res.Previous, Health: WireGmailHealth(h)}}, nil
}

// Test signs in with the stored app password and counts the messages of the
// last day.
func (s *Handlers) Test(c *apikit.Call) (apikit.Reply, error) {
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	n, err := stack.Service.Test(c.R.Context())
	s.Notify("gmail")
	if err != nil {
		return apikit.Reply{}, signInFailure(err, false)
	}
	return apikit.Reply{Data: wire.GmailTest{Messages: wire.SafeInt(n)}}, nil
}

// signInFailure maps a failed sign-in onto the API errors. A pair the user
// just typed and Gmail refused is a field to fix; a stored one Gmail now
// refuses, or none stored, is unavailable until it is replaced.
func signInFailure(err error, typed bool) error {
	var ie *gmail.InputError
	var le *gmail.LoginError
	var ue *gmail.UnreachableError
	switch {
	case errors.As(err, &ie):
		return apikit.InvalidField(ie.Field, "%s", ie.Reason)
	case errors.As(err, &le) && typed:
		return apikit.InvalidField("password", "%s", le.ConnectRefusal())
	case errors.As(err, &le), errors.Is(err, gmail.ErrNotConnected):
		return apikit.WithReason(apikit.NewErr(wire.CodeUnavailable, "%s", err.Error()), gmail.ReasonNotSignedIn)
	case errors.As(err, &ue):
		e := apikit.NewErr(wire.CodeUnavailable, "%s", err.Error())
		e.Retryable = true
		return apikit.WithReason(e, gmail.ReasonUnreachable)
	case errors.Is(err, gmail.ErrNoAllMail):
		return apikit.NewErr(wire.CodeConflict, "%s", err.Error())
	}
	return err
}

// Disconnect removes the stored app password, which ends the watcher's
// connection, and pauses the user's Gmail sync schedules, which cannot run
// without it (scheduler.PauseGmailSync); connecting again brings them back.
// The address and the stored mail stay.
func (s *Handlers) Disconnect(c *apikit.Call) (apikit.Reply, error) {
	stack, err := s.Stack()
	if err != nil {
		return apikit.Reply{}, err
	}
	if _, err := stack.Account.Disconnect(c.R.Context()); err != nil {
		return apikit.Reply{}, err
	}
	stack.Watcher.Wake()
	if sc, err := s.Scheduler(); err == nil {
		if names, err := sc.Manager.PauseGmailSync(c.R.Context()); err != nil {
			s.Log().Warn("the Gmail sync schedules could not be paused", "error", err.Error())
		} else if len(names) > 0 {
			s.Notify("schedules")
		}
	}
	s.Notify("gmail", "setup")
	return s.gmailHealthReply(c.R.Context(), stack)
}

func (s *Handlers) gmailHealthReply(ctx context.Context, stack *gmail.Stack) (apikit.Reply, error) {
	h, err := stack.Service.Health(ctx)
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: WireGmailHealth(h)}, nil
}

// resumeGmailSync gives a newly connected Gmail back the sync schedules the
// user made, which disconnecting paused. The watcher reads the mail itself.
func (s *Handlers) resumeGmailSync() {
	sc, err := s.Scheduler()
	if err == nil {
		resumed, e := sc.Manager.ResumeGmailSync(s.BG())
		if err = e; len(resumed) > 0 {
			s.Notify("schedules")
		}
	}
	if err != nil {
		s.Log().Warn("the Gmail sync schedules could not be switched back on", "error", err.Error())
	}
}
