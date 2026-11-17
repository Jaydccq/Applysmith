package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"maps"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/browser"
)

const (
	// sessionRequestTTL is how long a sync request waits for the extension.
	sessionRequestTTL = 120 * time.Second
	// extensionSilence is how long after its last check-in the extension still
	// counts as present.
	extensionSilence  = 120 * time.Second
	maxSessionCookies = 2000
	maxSessionBytes   = 1 << 20
	maxSessionSites   = 200
)

// Reasons a sign-in sync refusal names.
const (
	reasonNotAllowed       = "not-allowed"
	reasonStaleRequest     = "stale-request"
	reasonOutsideRequest   = "outside-request"
	reasonCookieOutOfScope = "cookie-out-of-scope"
	reasonApplyRunLive     = "apply-run-live"
	reasonSyncBusy         = "busy"
	reasonNoBrowser        = "browser-unavailable"
	reasonNotListed        = "not-listed"
	reasonNoCookies        = "no-cookies"
	reasonExtensionSilent  = "extension-silent"
)

// SessionBrowser is the managed browser as the sign-in sync sees it. The API
// has already checked the request: every cookie is unpartitioned and inside
// the domains.
//
// Import replaces the managed profile's cookies for the domains with the given
// ones: it keeps a snapshot first so the import can be undone, leaves a domain
// the cookies do not cover untouched, reads the profile back to count what is
// present and reports counts only. Undo restores that snapshot. Sites lists
// the hosts the sync may be asked about: the sign-in probe's sites, hosts with
// a stored sign-in account and hosts that hold cookies in the profile.
type SessionBrowser interface {
	Sites(ctx context.Context) ([]browser.SessionSite, error)
	Import(ctx context.Context, domains []string, cookies []wire.SessionCookie) (wire.SessionImportResult, error)
	Undo(ctx context.Context) (wire.SessionUndoResult, error)
	UndoAvailable() bool
	// CheckSignIn loads each listed site's account page and classifies it;
	// a site signed in or rate-limited in the last five minutes is answered
	// from that result.
	CheckSignIn(ctx context.Context, ids ...string) ([]browser.SignInCheck, error)
	// ImportedSites lists the sites whose sign-in was brought into the profile,
	// for the manage list. Forget clears one of them.
	ImportedSites(ctx context.Context) ([]browser.ImportedSite, error)
	Forget(ctx context.Context, host string) (int, error)
}

// noBrowser stands in for a server started without a managed browser.
type noBrowser struct{}

func (noBrowser) Sites(context.Context) ([]browser.SessionSite, error) { return nil, nil }
func (noBrowser) UndoAvailable() bool                                  { return false }

func (noBrowser) Import(context.Context, []string, []wire.SessionCookie) (wire.SessionImportResult, error) {
	return wire.SessionImportResult{}, errNoBrowser()
}

func (noBrowser) CheckSignIn(context.Context, ...string) ([]browser.SignInCheck, error) {
	return nil, errNoBrowser()
}

func (noBrowser) Undo(context.Context) (wire.SessionUndoResult, error) {
	return wire.SessionUndoResult{}, errNoBrowser()
}

func (noBrowser) ImportedSites(context.Context) ([]browser.ImportedSite, error) { return nil, nil }

func (noBrowser) Forget(context.Context, string) (int, error) { return 0, errNoBrowser() }

func errNoBrowser() *apikit.Error {
	return apikit.WithReason(apikit.NewErr(wire.CodeUnavailable, "this server has no managed browser to copy a sign-in into"), reasonNoBrowser)
}

// syncOutcome is how a request ended, for a run waiting on it.
type syncOutcome struct {
	result   wire.SessionImportResult
	err      error
	replaced bool
}

type syncRequest struct {
	id      string
	domains []string
	expires time.Time
	// done is set for a run's own-host request. It has room for one outcome.
	done chan syncOutcome
}

// sessionSync is the state the sign-in sync keeps in memory: the extension's
// last check-in with its cookie counts, and at most one pending request. The
// state holds no cookie.
type sessionSync struct {
	mu          sync.Mutex
	now         func() time.Time
	lastCheckin time.Time
	counts      map[string]int // nil when the last check-in carried no counts
	pending     *syncRequest
	busy        bool // an import is writing
	lastOutcome *wire.SessionOutcome
	lastImport  *wire.SessionImportSummary
}

func (y *sessionSync) livePendingLocked() *syncRequest {
	if y.pending != nil && !y.now().Before(y.pending.expires) {
		y.lastOutcome = &wire.SessionOutcome{State: outcomeFailed, Message: "Chrome did not answer the sign-in request in time", At: wire.Time{Time: y.pending.expires.UTC()}}
		y.pending = nil
	}
	return y.pending
}

// Outcome states of the last request or undo.
const (
	outcomeSucceeded = "succeeded"
	outcomeFailed    = "failed"
	outcomeUndone    = "undone"
)

// record keeps how the last import or undo ended and, for an import, the
// sites it copied. Messages are the typed error's own text, which holds no
// cookie name or value.
func (y *sessionSync) record(state, message string, imported *wire.SessionImportResult, domains []string) {
	y.mu.Lock()
	defer y.mu.Unlock()
	at := wire.Time{Time: y.now().UTC()}
	y.lastOutcome = &wire.SessionOutcome{State: state, Message: message, At: at}
	if imported != nil {
		y.lastImport = &wire.SessionImportSummary{At: at, Domains: domains, Result: *imported}
	}
}

// view copies the state the sign-in sync page shows.
func (y *sessionSync) view() (last *wire.Time, counts map[string]int, pending *syncRequest, outcome *wire.SessionOutcome, imp *wire.SessionImportSummary) {
	y.mu.Lock()
	defer y.mu.Unlock()
	if !y.lastCheckin.IsZero() {
		t := wire.Time{Time: y.lastCheckin.UTC()}
		last = &t
	}
	counts = maps.Clone(y.counts)
	if p := y.livePendingLocked(); p != nil {
		c := *p
		pending = &c
	}
	return last, counts, pending, y.lastOutcome, y.lastImport
}

// checkin records a check-in and returns the request waiting for the
// extension. A check-in without counts is the first after the extension
// restarted and is no proof that the everyday Chrome is there.
func (y *sessionSync) checkin(counts map[string]int) *syncRequest {
	y.mu.Lock()
	defer y.mu.Unlock()
	y.lastCheckin = y.now()
	y.counts = counts
	if len(counts) == 0 {
		y.counts = nil
	}
	return y.livePendingLocked()
}

// queue makes a request for the domains, replacing the pending one. A run
// passes done to hear how its request ended.
func (y *sessionSync) queue(domains []string, done chan syncOutcome) (syncRequest, *apikit.Error) {
	var clean []string
	for _, d := range domains {
		n, e := syncDomain("domains", d)
		if e != nil {
			return syncRequest{}, e
		}
		clean = append(clean, n)
	}
	clean = collapseDomains(clean)
	if len(clean) == 0 {
		return syncRequest{}, apikit.InvalidField("domains", "name at least one domain")
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return syncRequest{}, apikit.NewErr(wire.CodeInternal, "internal error")
	}
	req := syncRequest{id: "sync_" + hex.EncodeToString(raw[:]), domains: clean, done: done}
	y.mu.Lock()
	defer y.mu.Unlock()
	req.expires = y.now().Add(sessionRequestTTL)
	if old := y.livePendingLocked(); old != nil && old.done != nil {
		old.done <- syncOutcome{replaced: true}
	}
	y.pending = &req
	return req, nil
}

// withdraw takes the request back, so it cannot land later. It reports false
// when the request is no longer waiting: an import took it, or it was replaced.
func (y *sessionSync) withdraw(id string) bool {
	y.mu.Lock()
	defer y.mu.Unlock()
	if p := y.livePendingLocked(); p != nil && p.id == id {
		y.pending = nil
		return true
	}
	return false
}

// ownHostUnavailable says why a run cannot ask for its own host's sign-in, or
// "" when it can: the extension must have checked in with counts recently, and
// the count for the host, or for any site above it, must not be zero.
func (y *sessionSync) ownHostUnavailable(host string) string {
	y.mu.Lock()
	defer y.mu.Unlock()
	if y.lastCheckin.IsZero() || y.now().Sub(y.lastCheckin) > extensionSilence || y.counts == nil {
		return reasonExtensionSilent
	}
	for site, n := range y.counts {
		if n == 0 && inScope(host, site) {
			return reasonNoCookies
		}
	}
	return ""
}

// claim takes the pending request named by id for an import. The request is
// consumed only when check accepts it, so a refused payload leaves it waiting.
func (y *sessionSync) claim(id string, check func(syncRequest) *apikit.Error) (syncRequest, *apikit.Error) {
	y.mu.Lock()
	defer y.mu.Unlock()
	p := y.livePendingLocked()
	if p == nil || p.id != id {
		return syncRequest{}, apikit.WithReason(apikit.NewErr(wire.CodeConflict, "no sign-in request with that id is waiting; it finished, expired or was replaced"), reasonStaleRequest)
	}
	if e := check(*p); e != nil {
		return syncRequest{}, e
	}
	if y.busy {
		return syncRequest{}, errSyncBusy()
	}
	y.busy, y.pending = true, nil
	return *p, nil
}

// begin takes the import slot for a push.
func (y *sessionSync) begin() *apikit.Error {
	y.mu.Lock()
	defer y.mu.Unlock()
	if y.busy {
		return errSyncBusy()
	}
	y.busy = true
	return nil
}

func (y *sessionSync) end() {
	y.mu.Lock()
	y.busy = false
	y.mu.Unlock()
}

func errSyncBusy() *apikit.Error {
	return apikit.WithReason(apikit.NewErr(wire.CodeConflict, "another sign-in import is running"), reasonSyncBusy)
}

func errNotAllowed(domain string) *apikit.Error {
	return apikit.WithReason(apikit.NewErr(wire.CodeForbidden, "%s is never synced", domain), reasonNotAllowed)
}

// syncDomain normalizes a domain the sync may name. google.com, youtube.com and
// everything below them are refused.
func syncDomain(field, d string) (string, *apikit.Error) {
	n := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(d)), ".")
	if len(n) > 253 || !hostPattern.MatchString(n) {
		return "", apikit.InvalidField(field, "%s must hold plain domain names such as jobs.example.com", field)
	}
	if forbiddenSyncDomain(n) {
		return "", errNotAllowed(n)
	}
	return n, nil
}

func forbiddenSyncDomain(d string) bool {
	return inScope(d, "google.com") || inScope(d, "youtube.com")
}

// inScope reports whether domain is site or lies below it.
func inScope(domain, site string) bool { return domain == site || strings.HasSuffix(domain, "."+site) }

// collapseDomains drops duplicates and every domain below another in the list.
func collapseDomains(ds []string) []string {
	var out []string
	for _, d := range ds {
		covered := false
		for _, o := range ds {
			if o != d && inScope(d, o) {
				covered = true
			}
		}
		if !covered && !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	slices.Sort(out)
	return out
}

func (s *Server) sessionBrowser() SessionBrowser {
	if b := s.cfg.SessionBrowser; b != nil {
		return b
	}
	return noBrowser{}
}

func (s *Server) sessionCheckin(c *apikit.Call) (apikit.Reply, error) {
	var req wire.SessionCheckin
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if len(req.Sites) > maxSessionSites {
		return apikit.Reply{}, apikit.InvalidField("sites", "a check-in counts at most %d sites", maxSessionSites)
	}
	s.recordExtensionCheckin(c)
	counts := make(map[string]int, len(req.Sites))
	for _, site := range req.Sites {
		d, e := syncDomain("sites", site.Domain)
		if e != nil {
			return apikit.Reply{}, e
		}
		if site.Cookies < 0 {
			return apikit.Reply{}, apikit.InvalidField("sites", "a cookie count cannot be negative")
		}
		counts[d] = int(site.Cookies)
	}
	pending := s.signin.checkin(counts)
	known, err := s.sessionBrowser().Sites(c.R.Context())
	if err != nil {
		return apikit.Reply{}, sessionError(err)
	}
	reply := wire.SessionCheckinReply{Sites: []string{}}
	// The pending request's domains come first so the cap never drops them.
	var order []string
	if pending != nil {
		order = append(order, pending.domains...)
		reply.Pending = &wire.SessionPending{RequestID: pending.id, Domains: pending.domains, ExpiresAt: wire.Time{Time: pending.expires.UTC()}}
	}
	for _, site := range known {
		order = append(order, site.Domain)
	}
	for _, d := range order {
		if n := strings.ToLower(strings.TrimSpace(d)); hostPattern.MatchString(n) && !forbiddenSyncDomain(n) && !slices.Contains(reply.Sites, n) && len(reply.Sites) < maxSessionSites {
			reply.Sites = append(reply.Sites, n)
		}
	}
	slices.Sort(reply.Sites)
	return apikit.Reply{Data: reply}, nil
}

// checkCookies validates the cookies' shape and that none belongs to a
// forbidden domain. It never repeats a value.
func checkCookies(cookies []wire.SessionCookie) *apikit.Error {
	if len(cookies) > maxSessionCookies {
		return apikit.NewErr(wire.CodePayloadTooLarge, "a sync holds at most %d cookies", maxSessionCookies)
	}
	for i, ck := range cookies {
		bad := func(what string) *apikit.Error {
			return apikit.InvalidField("cookies", "cookie %d has an invalid %s", i, what)
		}
		switch {
		case ck.Name == "" || len(ck.Name) > 1024 || hasControl(ck.Name):
			return bad("name")
		case len(ck.Value) > 4096 || hasControl(ck.Value):
			return bad("value")
		case !strings.HasPrefix(ck.Path, "/") || len(ck.Path) > 1024 || hasControl(ck.Path):
			return bad("path")
		case !slices.Contains([]string{"no_restriction", "lax", "strict", "unspecified"}, ck.SameSite):
			return bad("sameSite")
		case ck.ExpirationDate != nil && (math.IsNaN(*ck.ExpirationDate) || math.IsInf(*ck.ExpirationDate, 0) || *ck.ExpirationDate <= 0):
			return bad("expirationDate")
		}
		d := strings.TrimPrefix(strings.ToLower(ck.Domain), ".")
		if len(d) > 253 || !hostPattern.MatchString(d) {
			return bad("domain")
		}
		if forbiddenSyncDomain(d) {
			return errNotAllowed(d)
		}
	}
	return nil
}

func hasControl(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}

// cookiesWithin reports that every cookie lies within one of the domains.
func cookiesWithin(cookies []wire.SessionCookie, domains []string) bool {
	for _, ck := range cookies {
		d := strings.TrimPrefix(strings.ToLower(ck.Domain), ".")
		if !slices.ContainsFunc(domains, func(site string) bool { return inScope(d, site) }) {
			return false
		}
	}
	return true
}

func errOutOfScope() *apikit.Error {
	return apikit.WithReason(apikit.NewErr(wire.CodeValidationFailed, "a cookie lies outside the requested domains; nothing was changed"), reasonCookieOutOfScope)
}

// sessionImport answers the pending request with the extension's cookies. A
// request the user queued is not answered while a run drives the apply
// window; the request a run queued for its own host is that run asking.
func (s *Server) sessionImport(c *apikit.Call) (apikit.Reply, error) {
	if len(c.Body) > maxSessionBytes {
		return apikit.Reply{}, apikit.NewErr(wire.CodePayloadTooLarge, "a sync body is at most %d bytes", maxSessionBytes)
	}
	var req wire.SessionImport
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if req.RequestID == "" || len(req.RequestID) > 64 {
		return apikit.Reply{}, apikit.InvalidField("requestId", "requestId is required")
	}
	if e := checkCookies(req.Cookies); e != nil {
		return apikit.Reply{}, e
	}
	skipped := make([]string, len(req.Skipped))
	for i, d := range req.Skipped {
		n := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(d)), ".")
		if len(n) > 253 || !hostPattern.MatchString(n) {
			return apikit.Reply{}, apikit.InvalidField("skipped", "skipped must hold plain domain names")
		}
		skipped[i] = n
	}
	live := s.applyRunLive()
	pending, e := s.signin.claim(req.RequestID, func(p syncRequest) *apikit.Error {
		if live && p.done == nil {
			return errApplyRunLive()
		}
		for _, d := range skipped {
			if !slices.ContainsFunc(p.domains, func(site string) bool { return inScope(d, site) }) {
				return apikit.WithReason(apikit.NewErr(wire.CodeValidationFailed, "the answer names a domain that was not requested"), reasonOutsideRequest)
			}
		}
		if !cookiesWithin(req.Cookies, p.domains) {
			return errOutOfScope()
		}
		return nil
	})
	if e != nil {
		return apikit.Reply{}, e
	}
	defer s.signin.end()
	res, err := s.copyCookies(c, pending.domains, req.Cookies, req.Truncated)
	if pending.done != nil {
		pending.done <- syncOutcome{result: res, err: err}
	}
	return replyOr(res, err)
}

// sessionPush copies one site's sign-in on the user's click. It passes the same
// scope, live-run and write checks as an answered request, without using the
// pending request.
func (s *Server) sessionPush(c *apikit.Call) (apikit.Reply, error) {
	if len(c.Body) > maxSessionBytes {
		return apikit.Reply{}, apikit.NewErr(wire.CodePayloadTooLarge, "a sync body is at most %d bytes", maxSessionBytes)
	}
	var req wire.SessionPush
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	site, e := syncDomain("site", req.Site)
	if e != nil {
		return apikit.Reply{}, e
	}
	if e := checkCookies(req.Cookies); e != nil {
		return apikit.Reply{}, e
	}
	if !cookiesWithin(req.Cookies, []string{site}) {
		return apikit.Reply{}, errOutOfScope()
	}
	if s.applyRunLive() {
		return apikit.Reply{}, errApplyRunLive()
	}
	if e := s.signin.begin(); e != nil {
		return apikit.Reply{}, e
	}
	defer s.signin.end()
	res, err := s.copyCookies(c, []string{site}, req.Cookies, req.Truncated)
	return replyOr(res, err)
}

// copyCookies hands the cookies to the managed browser and records how the
// import ended.
func (s *Server) copyCookies(c *apikit.Call, domains []string, cookies []wire.SessionCookie, truncated bool) (wire.SessionImportResult, error) {
	res, err := s.sessionBrowser().Import(c.R.Context(), domains, cookies)
	if err != nil {
		err = sessionError(err)
		s.signin.record(outcomeFailed, errorText(err), nil, nil)
		return wire.SessionImportResult{}, err
	}
	res.Truncated = truncated
	s.signin.record(outcomeSucceeded, "", &res, domains)
	return res, nil
}
