package api

import (
	"bytes"
	"cmp"
	"context"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"runtime/pprof"
	"strings"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
)

type boundRoute struct {
	Route
	Handler apikit.Handler
}

// recorder remembers the status for the request log and whether the response
// has started, which decides whether a recovered panic can still be answered.
type recorder struct {
	http.ResponseWriter
	status int
	wrote  bool
	// failure is the error the response carried, for the request log.
	failure *apikit.Error
}

func (r *recorder) WriteHeader(code int) {
	if !r.wrote {
		r.status, r.wrote = code, true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(b)
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// credentialParams are query parameter names that carry a credential. A
// credential in a URL ends up in logs and history, so the request is refused.
var credentialParams = map[string]bool{
	"token": true, "access_token": true, "api_token": true, "api_key": true, "apikey": true,
	"bearer": true, "authorization": true, "session": true, "applysmith_session": true,
	"password": true, "secret": true,
}

// hasCredentialInQuery scans the raw query itself: url.Values drops pairs
// separated by a semicolon, which a proxy or a lenient parser may still read.
func hasCredentialInQuery(r *http.Request) bool {
	for _, pair := range strings.FieldsFunc(r.URL.RawQuery, func(c rune) bool { return c == '&' || c == ';' }) {
		name, _, _ := strings.Cut(pair, "=")
		if dec, err := url.QueryUnescape(name); err == nil {
			name = dec
		}
		if credentialParams[strings.ToLower(strings.TrimSpace(name))] {
			return true
		}
	}
	return false
}

// serve is the whole request pipeline of a listener.
func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	start := s.now()
	rid := newRequestID(r)
	h := w.Header()
	h.Set("X-Request-Id", rid)
	securityHeaders(h)
	if strings.HasPrefix(r.URL.Path, "/api/") {
		h.Set("Cache-Control", "no-store")
	}
	rec := &recorder{ResponseWriter: w}
	out := &outcome{route: "-"}
	defer func() {
		if p := recover(); p != nil {
			s.log.Error("handler panic", "request_id", rid, "route", out.route, "panic", p, "stack", string(debug.Stack()))
			if !rec.wrote {
				writeError(rec, rid, apikit.NewErr(wire.CodeInternal, "internal error"))
			}
		}
		attrs := []slog.Attr{slog.String("method", r.Method), slog.String("route", out.route), slog.Int("status", max(rec.status, 0)),
			slog.Int64("ms", s.now().Sub(start).Milliseconds()), slog.String("principal", string(out.principal)),
			slog.String("request_id", rid)}
		s.log.LogAttrs(r.Context(), slog.LevelInfo, "request", append(attrs, failureAttrs(rec.failure)...)...)
	}()
	s.pipeline(rec, r, rid, out)
}

// A read (a GET with no deadline of its own, such as a model call has) still
// running after stallAfter has the stacks of every goroutine written to
// logs/stall.txt: what it waits on is gone once it answers, and the request
// log only shows how long it took. One record an hour at most, each replacing
// the last; the stacks name functions, not values.
const (
	stallAfter = 2 * time.Second
	stallEvery = time.Hour
	stallFile  = "stall.txt"
)

func (s *Server) recordStall(route string) {
	now := time.Now().UnixNano()
	last := s.lastStall.Load()
	if last != 0 && time.Duration(now-last) < stallEvery || !s.lastStall.CompareAndSwap(last, now) {
		return
	}
	var b bytes.Buffer
	_ = pprof.Lookup("goroutine").WriteTo(&b, 1)
	path := filepath.Join(s.cfg.Workspace.StateDir(), "logs", stallFile)
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		s.log.Warn("the goroutine stacks of a slow read could not be written", "route", route, "error", err.Error())
		return
	}
	s.log.Warn("a read is still running; goroutine stacks recorded", "route", route, "after_ms", s.stallAfter.Milliseconds(), "path", path)
}

// failureAttrs say why a request was refused: its error code and, when the
// error names them, the reason and the field. Those are fixed words; the
// message is left out, since it may repeat what was sent.
func failureAttrs(e *apikit.Error) []slog.Attr {
	if e == nil {
		return nil
	}
	out := []slog.Attr{slog.String("code", string(e.Code))}
	if d := e.Details; d != nil && d.Reason != "" {
		out = append(out, slog.String("reason", d.Reason))
	}
	if d := e.Details; d != nil && d.Field != "" {
		out = append(out, slog.String("field", d.Field))
	}
	return out
}

type outcome struct {
	route     string
	principal apikit.PrincipalKind
}

func (s *Server) pipeline(w *recorder, r *http.Request, rid string, out *outcome) {
	if e := s.admit(r, out); e != nil {
		writeError(w, rid, e)
		return
	}
	// The router sets the path values the handler reads.
	s.mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), stateKey{}, &reqState{rid: rid, out: out})))
}

type stateKey struct{}

type reqState struct {
	rid string
	out *outcome
}

// runRoute is the second half of the pipeline: credentials, limits, body and
// the handler. The router calls it once the route is known.
func (s *Server) runRoute(route *boundRoute, w http.ResponseWriter, r *http.Request) {
	st := r.Context().Value(stateKey{}).(*reqState)
	rid, out := st.rid, st.out
	p, e := s.authorize(w, r, route)
	if e != nil {
		writeError(w, rid, e)
		return
	}
	out.principal = p.Kind

	class, key := route.Limit, p.ID
	switch {
	case out.route == patternHealth:
		class = limitHealth
	case class == LimitGeneral && p.Kind == apikit.PrincipalDashboard && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		class = limitDashboardRead
	}
	if route.Access == AccessPublic || class == LimitPairing {
		key = "ip:" + s.source(r)
	}
	if ok, wait := s.limiter.allow(key, class); !ok {
		writeError(w, rid, errRateLimited(wait, ""))
		return
	}

	if !route.streams() {
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(s.readTimeout))
	}
	body, e := readBody(w, r, route.Body)
	if e != nil {
		writeError(w, rid, e)
		return
	}
	if !route.streams() && route.Body != BodyUpload {
		// The request is read. Left set, the deadline would end the server's
		// wait on the idle connection and cancel the handler's context with it.
		_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
	}

	ctx := r.Context()
	if !route.streams() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cmp.Or(route.Deadline, s.deadline))
		defer cancel()
		if r.Method == http.MethodGet && route.Deadline == 0 {
			defer time.AfterFunc(s.stallAfter, func() { s.recordStall(out.route) }).Stop()
		}
	}
	c := &apikit.Call{W: w, R: r.WithContext(ctx), P: p, RequestID: rid, Body: body}
	reply, err := route.Handler(c)
	switch {
	case err != nil:
		ae, unexpected := asError(err)
		if unexpected {
			s.log.Error("handler failed", "request_id", rid, "route", out.route, "error", err.Error())
		}
		if !c.Handled {
			writeError(w, rid, ae)
		}
	case !c.Handled:
		if reply.Location != "" {
			w.Header().Set("Location", reply.Location)
		}
		writeSuccess(w, cmpOr(reply.Status, http.StatusOK), rid, reply.Data)
	}
}

// admit applies the checks that come before authentication: host, credentials
// in the URL, preflight, route lookup and readiness. It records the matched
// route in out.
func (s *Server) admit(r *http.Request, out *outcome) *apikit.Error {
	if requestIsRemote(r) {
		if e := s.remote.admit(r); e != nil {
			return e
		}
	} else {
		if viaTunnel(r) {
			return s.remote.tunnelIngressError()
		}
		if !s.hosts[strings.ToLower(r.Host)] {
			return apikit.WithReason(apikit.NewErr(wire.CodeForbidden, "this host is not allowed"), wire.ReasonHostNotAllowed)
		}
	}
	if hasCredentialInQuery(r) {
		return apikit.NewErr(wire.CodeBadRequest, "credentials are never accepted in the URL")
	}
	if r.Method == http.MethodOptions {
		return apikit.NewErr(wire.CodeMethodNotAllowed, "cross-origin preflight is not supported")
	}
	_, pattern := s.mux.Handler(r)
	if s.routes[pattern] == nil {
		if requestIsRemote(r) {
			return remoteForbidden() // a route that does not exist is outside the allowlist too
		}
		return s.unmatched(r)
	}
	out.route = pattern
	// The router matches HEAD to a GET pattern; an event stream is for GET.
	if r.Method == http.MethodHead && s.routes[pattern].Response == PayloadEvents {
		return apikit.NewErr(wire.CodeMethodNotAllowed, "method HEAD is not allowed on an event stream")
	}
	if s.store.Load() == nil && pattern != patternHealth {
		return errStarting()
	}
	return nil
}

func cmpOr(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// unmatched answers a request no route claims. The static catch-all matches
// every GET path, so a wrong method on a real API path is told apart from an
// unknown path by probing the other methods.
func (s *Server) unmatched(r *http.Request) *apikit.Error {
	for _, m := range methods {
		probe := r.Clone(r.Context())
		probe.Method = m
		if _, pat := s.mux.Handler(probe); pat != "" && pat != patternStatic {
			return apikit.NewErr(wire.CodeMethodNotAllowed, "method %s is not allowed on this path", r.Method)
		}
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		return apikit.NewErr(wire.CodeNotFound, "no such route")
	}
	return apikit.NewErr(wire.CodeMethodNotAllowed, "method %s is not allowed on this path", r.Method)
}

// authorize resolves the principal for a route and applies the credential
// rules that depend on the kind of credential.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, route *boundRoute) (apikit.Principal, *apikit.Error) {
	if requestIsRemote(r) {
		return s.authorizeRemote(w, r, route)
	}
	if route.Access == AccessAgentProcess {
		return s.processCredential(r)
	}
	p := s.credential(r)
	if route.Access == AccessPublic {
		return p, nil
	}
	if p.Kind == apikit.PrincipalNone {
		return p, errUnauthenticated()
	}
	if route.SessionOnly && p.Kind != apikit.PrincipalDashboard {
		return p, apikit.WithReason(apikit.NewErr(wire.CodeForbidden, "this route accepts the dashboard session only"), wire.ReasonSessionOnly)
	}
	if p.Kind == apikit.PrincipalDashboard {
		if e := checkBrowserOrigin(r, "http"); e != nil {
			return p, e
		}
	}
	return p, nil
}

// credential identifies the caller. An Authorization header is the only
// credential considered when present, so the Origin exemption of the bearer
// token cannot be claimed by a request that actually rides on a cookie. A
// cookie counts only together with the session key: browsers send the cookie
// to every server on the host, whatever its port, so on its own it proves
// nothing about who holds it.
func (s *Server) credential(r *http.Request) apikit.Principal {
	if tok, present := bearerFrom(r); present {
		if s.token.valid(tok) {
			s.exts.seen(r.Header.Get(extensionHeader), s.now().UTC())
			return apikit.Principal{Kind: apikit.PrincipalLocalToken, ID: string(apikit.PrincipalLocalToken)}
		}
		return apikit.Principal{}
	}
	if ck, err := r.Cookie(SessionCookie); err == nil && s.sessions.valid(ck.Value, r.Header.Get(SessionKeyHeader)) {
		return apikit.Principal{Kind: apikit.PrincipalDashboard, ID: string(apikit.PrincipalDashboard)}
	}
	return apikit.Principal{}
}

// checkBrowserOrigin applies the cookie-only rules: no fetches from another
// origin and, for mutations, an Origin equal to the request's own origin.
// "same-site" is another origin too: a page on another localhost port is
// same-site with the dashboard, and cookies do not separate ports.
func checkBrowserOrigin(r *http.Request, scheme string) *apikit.Error {
	switch strings.ToLower(r.Header.Get("Sec-Fetch-Site")) {
	case "cross-site", "same-site":
		return apikit.WithReason(apikit.NewErr(wire.CodeForbidden, "requests from another origin are refused"), wire.ReasonCrossSite)
	}
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		if !strings.EqualFold(r.Header.Get("Origin"), scheme+"://"+r.Host) {
			return apikit.WithReason(apikit.NewErr(wire.CodeForbidden, "the Origin header must match this server"), wire.ReasonOriginMismatch)
		}
	}
	return nil
}

// processCredential accepts only the bearer credential of a live pi process.
// A user's token or session is no credential here, and a process credential
// is none anywhere else, because credential() never considers it.
func (s *Server) processCredential(r *http.Request) (apikit.Principal, *apikit.Error) {
	tok, present := bearerFrom(r)
	d := s.domain.Load()
	if !present || d == nil || !d.agentBridge.Authenticate(tok) {
		return apikit.Principal{}, errUnauthenticated()
	}
	sum := digest(tok)
	return apikit.Principal{Kind: apikit.PrincipalAgentProcess, ID: "agent-process:" + hex.EncodeToString(sum[:6])}, nil
}
