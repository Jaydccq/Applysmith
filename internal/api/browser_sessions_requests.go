package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/browser"
)

// ownHostWait is how long a run waits for the extension to answer its request.
const ownHostWait = 45 * time.Second

// Statuses of a run's own-host sign-in request.
const (
	OwnHostImported    = "imported"
	OwnHostUnavailable = "unavailable"
	OwnHostTimeout     = "timeout"
	OwnHostReplaced    = "replaced"
	OwnHostFailed      = "failed"
)

// OwnHostSync is how a run's request for its own host's sign-in ended.
// Reason, set when Status is unavailable, is extension-silent or no-cookies.
type OwnHostSync struct {
	Status string
	Reason string
	Result wire.SessionImportResult
}

func replyOr(data any, err error) (apikit.Reply, error) {
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: data}, nil
}

// sessionError maps a managed-browser failure to the envelope. The text sent
// is the typed error's own message, which holds no cookie name or value.
func sessionError(err error) error {
	var be *browser.Error
	if !errors.As(err, &be) {
		return err
	}
	reason := string(be.Code)
	switch be.Code {
	case browser.CodeNoSnapshot:
		return apikit.WithReason(apikit.NewErr(wire.CodeNotFound, "%s", be.Message), reason)
	case browser.CodeCookieOutOfScope:
		return errOutOfScope()
	}
	e := apikit.WithReason(apikit.NewErr(wire.CodeUnavailable, "%s", be.Message), reason)
	e.Retryable = slices.Contains([]browser.Code{browser.CodeSnapshotFailed, browser.CodeReplaceFailed, browser.CodeBrowserDisconnected, browser.CodeLaunchTimeout, browser.CodeProfileInUse}, be.Code)
	return e
}

func errorText(err error) string {
	var e *apikit.Error
	if errors.As(err, &e) {
		return e.Message
	}
	return "the import failed"
}

func (s *Server) applyRunLive() bool {
	if s.cfg.ApplyRunLive != nil && s.cfg.ApplyRunLive() {
		return true
	}
	d := s.domain.Load()
	return d != nil && d.apply != nil && d.apply.runner.Live()
}

func errApplyRunLive() *apikit.Error {
	return apikit.WithReason(apikit.NewErr(wire.CodeConflict, "an apply run is driving the apply window; try again when it ends"), reasonApplyRunLive)
}

// sessionSyncRequest queues a request from the dashboard for sites on the
// sites list.
func (s *Server) sessionSyncRequest(c *apikit.Call) (apikit.Reply, error) {
	var req wire.SessionSyncRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if len(req.Sites) == 0 || len(req.Sites) > maxSessionSites {
		return apikit.Reply{}, apikit.InvalidField("sites", "name between 1 and %d sites", maxSessionSites)
	}
	var domains []string
	for _, d := range req.Sites {
		n, e := syncDomain("sites", d)
		if e != nil {
			return apikit.Reply{}, e
		}
		domains = append(domains, n)
	}
	domains = collapseDomains(domains)
	if s.applyRunLive() {
		return apikit.Reply{}, errApplyRunLive()
	}
	listed, err := s.sessionBrowser().Sites(c.R.Context())
	if err != nil {
		return apikit.Reply{}, sessionError(err)
	}
	for _, d := range domains {
		if !slices.ContainsFunc(listed, func(site browser.SessionSite) bool { return site.Domain != "" && inScope(d, site.Domain) }) {
			return apikit.Reply{}, apikit.WithReason(apikit.NewErr(wire.CodeValidationFailed, "%s is not on the sites list", d), reasonNotListed)
		}
	}
	pending, e := s.signin.queue(domains, nil)
	if e != nil {
		return apikit.Reply{}, e
	}
	return apikit.Reply{Status: http.StatusAccepted, Data: pendingView(pending)}, nil
}

func pendingView(p syncRequest) wire.SessionPending {
	return wire.SessionPending{RequestID: p.id, Domains: p.domains, ExpiresAt: wire.Time{Time: p.expires.UTC()}}
}

// sessionsView answers with the sites list, both cookie counts and the state of
// the sync. It holds counts and times only.
func (s *Server) sessionsView(c *apikit.Call) (apikit.Reply, error) {
	sites, err := s.sessionBrowser().Sites(c.R.Context())
	if err != nil {
		return apikit.Reply{}, sessionError(err)
	}
	last, counts, pending, outcome, imp := s.signin.view()
	view := wire.SessionsView{Sites: make([]wire.SessionSiteView, 0, len(sites)), LastCheckin: last, LastOutcome: outcome, LastImport: imp,
		UndoAvailable: s.sessionBrowser().UndoAvailable()}
	for _, site := range sites {
		row := wire.SessionSiteView{Domain: site.Domain, Label: site.Label, Cookies: site.Cookies, SignIn: site.SignIn, SignInCheckedAt: site.SignInCheckedAt, SignInURL: site.SignInURL}
		if n, ok := counts[site.Domain]; ok && site.Domain != "" {
			v := wire.SafeInt(n)
			row.SourceCookies = &v
		}
		view.Sites = append(view.Sites, row)
	}
	if pending != nil {
		p := pendingView(*pending)
		view.Pending = &p
	}
	return apikit.Reply{Data: view}, nil
}

// sessionCheck is the dashboard's trigger for the sign-in probe: it loads each
// listed site's account page in the scan window and reports what it read. A
// probe only loads and reads, so it does not wait for a run that drives the
// apply window.
func (s *Server) sessionCheck(c *apikit.Call) (apikit.Reply, error) {
	checks, err := s.sessionBrowser().CheckSignIn(c.R.Context())
	if err != nil {
		return apikit.Reply{}, sessionError(err)
	}
	out := wire.SignInChecks{Checks: make([]wire.SignInCheck, 0, len(checks))}
	for _, ch := range checks {
		row := wire.SignInCheck{Domain: ch.Site.Domain, Label: ch.Site.Label, State: string(ch.State), Next: browser.NextAction(ch.State), Cached: ch.Cached}
		if ch.Err == nil && ch.State == browser.StateSignedIn {
			s.siteSignedIn(c.R.Context(), ch.Site.Domain)
		}
		if !ch.CheckedAt.IsZero() {
			at := wire.Time{Time: ch.CheckedAt.UTC()}
			row.CheckedAt = &at
		}
		if ch.Err != nil {
			row.Error = "the site could not be checked"
			var be *browser.Error
			if errors.As(ch.Err, &be) {
				row.Error = be.Message
			}
		}
		out.Checks = append(out.Checks, row)
	}
	return apikit.Reply{Data: out}, nil
}

// sessionUndo restores the cookies the last import replaced.
func (s *Server) sessionUndo(c *apikit.Call) (apikit.Reply, error) {
	if s.applyRunLive() {
		return apikit.Reply{}, errApplyRunLive()
	}
	if e := s.signin.begin(); e != nil {
		return apikit.Reply{}, e
	}
	defer s.signin.end()
	res, err := s.sessionBrowser().Undo(c.R.Context())
	if err != nil {
		return apikit.Reply{}, sessionError(err)
	}
	s.signin.record(outcomeUndone, "", nil, nil)
	return apikit.Reply{Data: res}, nil
}

// SyncOwnHost asks the user's everyday Chrome, through the extension, for the
// sign-in of exactly host, for the apply run that is applying there. It
// returns at once when the extension has been silent or counted no cookies for
// the host, and otherwise waits at most 45 s, withdrawing the request when the
// wait ends so it cannot land later. A run's request is not refused while a
// run drives the apply window: that run is the one asking.
func (s *Server) SyncOwnHost(ctx context.Context, host string) (OwnHostSync, error) {
	d, e := syncDomain("host", host)
	if e != nil {
		return OwnHostSync{}, e
	}
	if reason := s.signin.ownHostUnavailable(d); reason != "" {
		return OwnHostSync{Status: OwnHostUnavailable, Reason: reason}, nil
	}
	done := make(chan syncOutcome, 1)
	req, e := s.signin.queue([]string{d}, done)
	if e != nil {
		return OwnHostSync{}, e
	}
	wait := time.NewTimer(cmpOrDuration(s.cfg.ownHostWait, ownHostWait))
	defer wait.Stop()
	select {
	case out := <-done:
		return ownHostResult(out), nil
	case <-wait.C:
	case <-ctx.Done():
	}
	if s.signin.withdraw(req.id) {
		if ctx.Err() != nil {
			return OwnHostSync{}, ctx.Err()
		}
		return OwnHostSync{Status: OwnHostTimeout}, nil
	}
	// The answer is being written or the request was replaced; both end soon.
	select {
	case out := <-done:
		return ownHostResult(out), nil
	case <-ctx.Done():
		return OwnHostSync{}, ctx.Err()
	}
}

func ownHostResult(out syncOutcome) OwnHostSync {
	switch {
	case out.replaced:
		return OwnHostSync{Status: OwnHostReplaced}
	case out.err != nil:
		return OwnHostSync{Status: OwnHostFailed}
	}
	return OwnHostSync{Status: OwnHostImported, Result: out.result}
}
